/*
 * Copyright 2023 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package shmipc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	//cap 4 + size 4 + start 4 + next 4 + flag 4
	bufferHeaderSize      = 4 + 4 + 4 + 4 + 4
	bufferCapOffset       = 0
	bufferSizeOffset      = bufferCapOffset + 4
	bufferDataStartOffset = bufferSizeOffset + 4
	nextBufferOffset      = bufferDataStartOffset + 4
	bufferFlagOffset      = nextBufferOffset + 4
	//size 4 | cap 4 | head 4 | tail 4 | capPerBuffer 4 | push count 8  | pop count 8
	bufferListHeaderSize = 36

	bufferManagerHeaderSize = 8
	bmCapOffset             = 4
	//bmListNumOffset         = 0
)

const (
	hasNextBufferFlag = 1 << iota
	sliceInUsedFlag
)

var (
	bufferManagers = &globalBufferManager{
		bms: make(map[string]*bufferManager, 8),
	}
)

// todo mmap interface ?
// bufferManager's layout in share memory: listSize 2 byte | bufferList  n byte
type bufferManager struct {
	//ascending ordered by list.capPerBuffer
	lists        []*bufferList
	mem          []byte
	minSliceSize uint32
	maxSliceSize uint32
	path         string
	refCount     int32
	mmapMapType  MemMapType
	memFd        int
}

type globalBufferManager struct {
	sync.Mutex
	bms map[string]*bufferManager
}

// bufferList's layout in share memory: size 4 byte | cap 4 byte | head 4 byte | tail 4 byte | capPerBuffer 4 byte | bufferRegion n bye
// thead safe, lock free. support push && pop concurrently even cross different process.
type bufferList struct {
	// the number of free buffer
	size *int32
	//the max size of list
	cap *uint32
	// it points to the first free buffer, whose offset in bufferRegion
	head *uint32
	// it points to the last free buffer, whose offset in bufferRegion
	tail         *uint32
	capPerBuffer *uint32
	pushCount    *uint64
	popCount     *uint64
	//underlying memory
	bufferRegion            []byte
	bufferRegionOffsetInShm uint32
	//the bufferList's location offset in share memory
	offsetInShm uint32
}

// A SizePercentPair describe a buffer list's specification
type SizePercentPair struct {
	//A single buffer slice's capacity of buffer list,
	Size uint32
	//The used percent of buffer list in the total share memory
	Percent uint32
}

type sizePercentPairs []*SizePercentPair

var _ sort.Interface = &sizePercentPairs{}

func (s sizePercentPairs) Len() int {
	return len([]*SizePercentPair(s))
}

func (s sizePercentPairs) Less(i, j int) bool {
	return s[i].Size < s[j].Size
}

func (s sizePercentPairs) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func getGlobalBufferManagerWithMemFd(bufferPathName string, memFd int, capacity uint32, create bool,
	pairs []*SizePercentPair) (*bufferManager, error) {
	bufferManagers.Lock()
	defer bufferManagers.Unlock()

	if bm, ok := bufferManagers.bms[bufferPathName]; ok {
		atomic.AddInt32(&bm.refCount, 1)
		if memFd > 0 && !create {
			_ = syscall.Close(memFd)
		}
		return bm, nil
	}

	var (
		bm  *bufferManager
		err error
	)

	if create {
		memFd, err = MemfdCreate(bufferPathName, 0)
		if err != nil {
			return nil, fmt.Errorf("getGlobalBufferManagerWithMemFd MemfdCreate failed:%w", err)
		}

		if err := syscall.Ftruncate(memFd, int64(capacity)); err != nil {
			return nil, fmt.Errorf("getGlobalBufferManagerWithMemFd truncate share memory failed:%w", err)
		}
	} else {
		var fInfo syscall.Stat_t
		err = syscall.Fstat(memFd, &fInfo)
		if err != nil {
			return nil, fmt.Errorf("getGlobalBufferManagerWithMemFd mapping failed:%w", err)
		}
		capacity = uint32(fInfo.Size)
	}

	mem, err := syscall.Mmap(memFd, 0, int(capacity), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("getGlobalBufferManagerWithMemFd Mmap failed:%w", err)
	}

	if create {
		sort.Sort(sizePercentPairs(pairs))
		bm, err = createBufferManager(pairs, bufferPathName, mem, 0)
	} else {
		bm, err = mappingBufferManager(bufferPathName, mem, 0)
	}

	if err != nil {
		_ = syscall.Munmap(mem)
		return nil, err
	}

	bm.memFd = memFd
	bm.mmapMapType = MemMapTypeMemFd

	bufferManagers.bms[bufferPathName] = bm
	return bm, nil
}

func getGlobalBufferManager(shmPath string, capacity uint32, create bool, pairs []*SizePercentPair) (*bufferManager, error) {
	bufferManagers.Lock()
	defer bufferManagers.Unlock()
	if bm, ok := bufferManagers.bms[shmPath]; ok {
		atomic.AddInt32(&bm.refCount, 1)
		return bm, nil
	}

	//ignore mkdir error
	_ = os.MkdirAll(filepath.Dir(shmPath), os.ModePerm)

	var (
		shmFile *os.File
		err     error
	)

	if create {
		if !canCreateOnDevShm(uint64(capacity), shmPath) {
			return nil, fmt.Errorf("err:%s path:%s, size:%d", ErrShareMemoryHadNotLeftSpace.Error(), shmPath, capacity)
		}

		shmFile, err = os.OpenFile(shmPath, os.O_CREATE|os.O_RDWR|os.O_EXCL, os.ModePerm)
		if err != nil {
			return nil, err
		}

		if err := shmFile.Truncate(int64(capacity)); err != nil {
			return nil, fmt.Errorf("getGlobalBufferManager truncate share memory failed,%s", err.Error())
		}
	} else {
		//file flag don't include os.O_CREATE, because in this case, the share memory should created by peer.
		shmFile, err = os.OpenFile(shmPath, os.O_RDWR, os.ModePerm)
		if err != nil {
			return nil, err
		}

		fi, err := shmFile.Stat()
		if err != nil {
			return nil, fmt.Errorf("getGlobalBufferManager mapping failed,%s", err.Error())
		}
		capacity = uint32(fi.Size())
	}
	defer shmFile.Close()

	mem, err := syscall.Mmap(int(shmFile.Fd()), 0, int(capacity), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, err
	}

	var bm *bufferManager
	if create {
		sort.Sort(sizePercentPairs(pairs))
		bm, err = createBufferManager(pairs, shmPath, mem, 0)
	} else {
		bm, err = mappingBufferManager(shmPath, mem, 0)
	}

	if err != nil {
		_ = syscall.Munmap(mem)
		return nil, err
	}
	bufferManagers.bms[shmPath] = bm
	return bm, nil
}

func addGlobalBufferManagerRefCount(path string, c int) {
	bufferManagers.Lock()
	if bm, ok := bufferManagers.bms[path]; ok {
		if atomic.AddInt32(&bm.refCount, int32(c)) <= 0 {
			internalLogger.warnf("clean buffer manager:%s", path)
			bm.unmap()
			delete(bufferManagers.bms, path)
		}
	}
	bufferManagers.Unlock()
}

func createBufferManager(listSizePercent []*SizePercentPair, path string, mem []byte, offset uint32) (*bufferManager, error) {
	if len(mem) <= int(offset) {
		return nil, fmt.Errorf("mem's size is at least:%d but:%d", offset+1, len(mem))
	}

	//number of list 2 byte | 2 byte reserve | used_length 4 byte
	bufferRegionCap := uint64(len(mem) - int(offset) - bufferListHeaderSize*len(listSizePercent) - bufferManagerHeaderSize)
	internalLogger.infof("createBufferManager path:%s config:%+v memSize:%d bufferRegionCap:%d offset:%d",
		path, listSizePercent, len(mem), bufferRegionCap, offset)
	*(*uint16)(unsafe.Pointer(&mem[offset])) = uint16(len(listSizePercent))

	hadUsedOffset := bufferManagerHeaderSize + offset
	freeBufferLists := make([]*bufferList, 0, len(listSizePercent))
	sumPercent := uint32(0)
	for _, pair := range listSizePercent {
		sumPercent += pair.Percent
		if sumPercent > 100 {
			return nil, errors.New("the sum of all SizePercentPair's percent must be equals 100")
		}
		bufferNum := uint32(bufferRegionCap*uint64(pair.Percent)/100) / (pair.Size + bufferHeaderSize)
		needSize := countBufferListMemSize(bufferNum, pair.Size)
		freeList, err := createFreeBufferList(bufferNum, pair.Size, mem, hadUsedOffset)
		if err != nil {
			return nil, err
		}
		freeBufferLists = append(freeBufferLists, freeList)
		hadUsedOffset += needSize
	}
	ret := &bufferManager{
		path:         path,
		mem:          mem,
		lists:        freeBufferLists,
		minSliceSize: listSizePercent[0].Size,
		maxSliceSize: listSizePercent[len(listSizePercent)-1].Size,
		refCount:     1,
	}
	*(*uint32)(unsafe.Pointer(&mem[offset+bmCapOffset])) = hadUsedOffset - bufferManagerHeaderSize
	return ret, nil
}

func mappingBufferManager(path string, mem []byte, bufferRegionStartOffset uint32) (*bufferManager, error) {
	if len(mem) <= int(bufferRegionStartOffset+bmCapOffset) || len(mem) <= int(bufferRegionStartOffset) {
		return nil, fmt.Errorf("mem's size is at least:%d but:%d bufferRegionStartOffset:%d", bufferRegionStartOffset+bmCapOffset+1, len(mem), bufferRegionStartOffset)
	}

	listNum := int(*(*uint16)(unsafe.Pointer(&mem[bufferRegionStartOffset])))
	freeLists := make([]*bufferList, 0, listNum)
	length := *(*uint32)(unsafe.Pointer(&mem[bufferRegionStartOffset+bmCapOffset]))
	if len(mem) < bufferManagerHeaderSize+int(length) || listNum == 0 {
		return nil, fmt.Errorf("could not mappingBufferManager ,listNum:%d len(mem) at least:%d but:%d",
			listNum, length+bufferManagerHeaderSize, len(mem))
	}
	hadUsedOffset := uint32(bufferManagerHeaderSize)
	internalLogger.infof("mappingBufferManager, listNum:%d length:%d", listNum, length)

	for i := 0; i < listNum; i++ {
		l, err := mappingFreeBufferList(mem, bufferRegionStartOffset+hadUsedOffset)
		if err != nil {
			return nil, err
		}
		internalLogger.infof("mappingFreeBufferList offset:%d size:%d head:%d tail:%d capPerBuffer:%d ",
			bufferRegionStartOffset+hadUsedOffset, *l.size, *l.head, *l.tail, *l.capPerBuffer)
		size := countBufferListMemSize(*l.cap, *l.capPerBuffer)
		hadUsedOffset += size
		freeLists = append(freeLists, l)
	}
	//sort by capPerBuffer
	ret := &bufferManager{
		path:         path,
		mem:          mem,
		minSliceSize: *freeLists[0].capPerBuffer,
		maxSliceSize: *freeLists[len(freeLists)-1].capPerBuffer,
		lists:        freeLists,
		refCount:     1,
	}
	return ret, nil
}

func countBufferListMemSize(bufferNum, capPerBuffer uint32) uint32 {
	return bufferListHeaderSize + bufferNum*(capPerBuffer+bufferHeaderSize)
}

func createFreeBufferList(bufferNum, capPerBuffer uint32, mem []byte, offsetInMem uint32) (*bufferList, error) {
	if bufferNum == 0 || capPerBuffer == 0 {
		return nil, fmt.Errorf("bufferNum:%d or capPerBuffer:%d cannot be 0 ", bufferNum, capPerBuffer)
	}
	atLeastSize := countBufferListMemSize(bufferNum, capPerBuffer)
	if len(mem) < int(offsetInMem+atLeastSize) || offsetInMem > uint32(len(mem)) || atLeastSize > uint32(len(mem)) {
		return nil, fmt.Errorf("mem's size is at least:%d but:%d offsetInMem:%d atLeastSize:%d", offsetInMem+atLeastSize, len(mem), offsetInMem, atLeastSize)
	}

	bufferRegionStart := offsetInMem + bufferListHeaderSize
	bufferRegionEnd := offsetInMem + atLeastSize
	if bufferRegionEnd <= bufferRegionStart {
		return nil, fmt.Errorf("bufferRegionStart:%d bufferRegionEnd:%d slice bounds out of range", bufferRegionStart, bufferRegionEnd)
	}

	b := &bufferList{
		size:                    (*int32)(unsafe.Pointer(&mem[offsetInMem+0])),
		cap:                     (*uint32)(unsafe.Pointer(&mem[offsetInMem+4])),
		head:                    (*uint32)(unsafe.Pointer(&mem[offsetInMem+8])),
		tail:                    (*uint32)(unsafe.Pointer(&mem[offsetInMem+12])),
		capPerBuffer:            (*uint32)(unsafe.Pointer(&mem[offsetInMem+16])),
		pushCount:               (*uint64)(unsafe.Pointer(&mem[offsetInMem+20])),
		popCount:                (*uint64)(unsafe.Pointer(&mem[offsetInMem+28])),
		bufferRegion:            mem[offsetInMem+bufferListHeaderSize : offsetInMem+atLeastSize],
		bufferRegionOffsetInShm: offsetInMem + bufferListHeaderSize,
		offsetInShm:             offsetInMem,
	}
	*b.size = int32(bufferNum)
	*b.cap = bufferNum
	*b.head = 0
	*b.tail = (bufferNum - 1) * (capPerBuffer + bufferHeaderSize)
	*b.capPerBuffer = capPerBuffer
	*b.pushCount = 0
	*b.popCount = 0
	internalLogger.infof("createFreeBufferList  bufferNum:%d capPerBuffer:%d offsetInMem:%d needSize:%d bufferRegionLen:%d",
		bufferNum, capPerBuffer, offsetInMem, atLeastSize, len(b.bufferRegion))

	current, next := uint32(0), uint32(0)
	for i := 0; i < int(bufferNum); i++ {
		next = current + capPerBuffer + bufferHeaderSize
		//set buffer's header.
		*(*uint32)(unsafe.Pointer(&b.bufferRegion[current])) = capPerBuffer
		*(*uint32)(unsafe.Pointer(&b.bufferRegion[current+bufferSizeOffset])) = 0
		*(*uint32)(unsafe.Pointer(&b.bufferRegion[current+bufferDataStartOffset])) = 0
		if i < int(bufferNum-1) {
			*(*uint32)(unsafe.Pointer(&b.bufferRegion[current+nextBufferOffset])) = next
			b.bufferRegion[current+bufferFlagOffset] |= hasNextBufferFlag
		}
		current = next
	}
	bufferHeader(b.bufferRegion[*b.tail:]).clearFlag()
	return b, nil
}

func mappingFreeBufferList(mem []byte, offset uint32) (*bufferList, error) {
	if len(mem) < bufferListHeaderSize+int(offset) {
		return nil, fmt.Errorf("mappingFreeBufferList failed, mem's size is at least %d", bufferListHeaderSize+int(offset))
	}

	b := &bufferList{
		size:         (*int32)(unsafe.Pointer(&mem[offset+0])),
		cap:          (*uint32)(unsafe.Pointer(&mem[offset+4])),
		head:         (*uint32)(unsafe.Pointer(&mem[offset+8])),
		tail:         (*uint32)(unsafe.Pointer(&mem[offset+12])),
		capPerBuffer: (*uint32)(unsafe.Pointer(&mem[offset+16])),
		pushCount:    (*uint64)(unsafe.Pointer(&mem[offset+20])),
		popCount:     (*uint64)(unsafe.Pointer(&mem[offset+28])),
		offsetInShm:  offset,
	}
	needSize := countBufferListMemSize(*b.cap, *b.capPerBuffer)
	if offset+needSize > uint32(len(mem)) || (offset+needSize) < (offset+bufferListHeaderSize) {
		return nil, fmt.Errorf("mappingFreeBufferList failed, size:%d cap:%d head:%d tail:%d capPerBuffer:%d err: mem's size is at least %d but:%d",
			*b.size, *b.cap, *b.head, *b.tail, *b.capPerBuffer, needSize, len(mem))
	}
	b.bufferRegion = mem[offset+bufferListHeaderSize : offset+needSize]
	b.bufferRegionOffsetInShm = offset + bufferListHeaderSize
	return b, nil
}

func (b *bufferList) pop() (*bufferSlice, error) {
	oldHead := atomic.LoadUint32(b.head)
	remain := atomic.AddInt32(b.size, -1)
	if remain <= 0 {
		atomic.AddInt32(b.size, 1)
		return nil, ErrNoMoreBuffer
	}
	//when data races occurred, max retry 200 times.
	for i := 0; i < 200; i++ {
		bh := bufferHeader(b.bufferRegion[oldHead : oldHead+bufferHeaderSize])
		if bh.hasNext() {
			if atomic.CompareAndSwapUint32(b.head, oldHead, bh.nextBufferOffset()) {
				h := bufferHeader(b.bufferRegion[oldHead : oldHead+bufferHeaderSize])
				h.clearFlag()
				h.setInUsed()
				atomic.AddUint64(b.popCount, 1)
				return newBufferSlice(h,
					b.bufferRegion[oldHead+bufferHeaderSize:oldHead+bufferHeaderSize+*b.capPerBuffer],
					oldHead+b.bufferRegionOffsetInShm, true), nil
			}
		} else {
			//don't alloc the last slice.
			if *b.size <= 1 {
				atomic.AddInt32(b.size, 1)
				return nil, ErrNoMoreBuffer
			}
		}
		oldHead = atomic.LoadUint32(b.head)
	}
	atomic.AddInt32(b.size, 1)
	return nil, ErrNoMoreBuffer
}

func (b *bufferList) push(buffer *bufferSlice) {
	buffer.reset()
	for {
		oldTail := atomic.LoadUint32(b.tail)
		newTail := buffer.offsetInShm - b.bufferRegionOffsetInShm
		if atomic.CompareAndSwapUint32(b.tail, oldTail, newTail) {
			bufferHeader(b.bufferRegion[oldTail : oldTail+bufferHeaderSize]).linkNext(newTail)
			atomic.AddInt32(b.size, 1)
			atomic.AddUint64(b.pushCount, 1)
			return
		}
	}
}

func (b *bufferList) remain() int {
	//when the size is 1, not allow pop for solving problem about concurrent operating.
	return int(atomic.LoadInt32(b.size) - 1)
}

func (b *bufferManager) remainSize() uint32 {
	var result uint32
	for _, pair := range b.lists {
		remain := int(*pair.size) * int(*pair.capPerBuffer)
		if remain > 0 {
			result += uint32(remain)
		}
	}

	return result
}

// alloc single buffer slice , whose performance better than allocShmBuffers.
func (b *bufferManager) allocShmBuffer(size uint32) (*bufferSlice, error) {
	if size <= b.maxSliceSize {
		for i := range b.lists {
			if size <= *b.lists[i].capPerBuffer {
				buf, err := b.lists[i].pop()
				if err != nil {
					continue
				}
				return buf, nil
			}
		}
	}
	return nil, ErrNoMoreBuffer
}

func (b *bufferManager) allocShmBuffers(slices *sliceList, size uint32) (allocSize int64) {
	remain := int64(size)
	for i := len(b.lists) - 1; i >= 0 && remain > 0; i-- {
		for remain > 0 {
			buf, err := b.lists[i].pop()
			if err != nil {

				break
			}
			slices.pushBack(buf)
			allocSize += int64(buf.cap)
			remain -= int64(buf.cap)
		}
	}
	return allocSize
}

func (b *bufferManager) recycleBuffer(slice *bufferSlice) {
	if slice == nil {
		return
	}
	if slice.isFromShm {
		for i := range b.lists {
			if slice.cap == *b.lists[i].capPerBuffer {
				b.lists[i].push(slice)

				break
			}
		}
	}
	putBackBufferSlice(slice)
}

func (b *bufferManager) recycleBuffers(slice *bufferSlice) {
	if slice == nil {
		return
	}
	if slice.isFromShm {
		var err error
		for {
			if !slice.hasNext() {
				b.recycleBuffer(slice)
				return
			}
			nextSliceOffset := slice.nextBufferOffset()
			b.recycleBuffer(slice)
			slice, err = b.readBufferSlice(nextSliceOffset)
			if err != nil {
				internalLogger.error("bufferManager recycleBuffers readBufferSlice failed,err=" + err.Error())
				return
			}
		}
	}
}

func (b *bufferManager) sliceSize() (size int) {
	for i := range b.lists {
		size += int(*b.lists[i].size)
	}
	return
}

func (b *bufferManager) readBufferSlice(offset uint32) (*bufferSlice, error) {
	if int(offset)+bufferHeaderSize >= len(b.mem) {
		return nil, fmt.Errorf("broken share memory. readBufferSlice unexpected offset:%d buffers cap:%d",
			offset, len(b.mem))
	}
	bufCap := *(*uint32)(unsafe.Pointer(&b.mem[offset+bufferCapOffset]))
	bufEndOffset := offset + uint32(bufferHeaderSize) + bufCap
	if bufEndOffset > uint32(len(b.mem)) {
		return nil, fmt.Errorf("broken share memory. readBufferSlice unexpected bufferEndOffset:%d. bufferStartOffset:%d buffers cap:%d",
			bufEndOffset, offset, len(b.mem))
	}
	return newBufferSlice(b.mem[offset:offset+bufferHeaderSize], b.mem[offset+bufferHeaderSize:bufEndOffset], offset, true), nil
}

func (b *bufferManager) unmap() {
	// spin 5s to check if all buffer are returned, if timeout, we still force unmap TODO: ?
	for i := 0; i < 50; i++ {
		if b.checkBufferReturned() {
			internalLogger.info("all buffer returned before unmap")

			break
		}
		time.Sleep(time.Microsecond * 100)
	}
	if err := syscall.Munmap(b.mem); err != nil {
		internalLogger.warnf("bufferManager unmap error:" + err.Error())
	}

	if b.mmapMapType == MemMapTypeDevShmFile {
		if err := os.Remove(b.path); err != nil {
			internalLogger.warnf("bufferManager remove file:%s failed, error=%s", b.path, err.Error())
		} else {
			internalLogger.infof("bufferManager removed file:%s", b.path)
		}
	}

	if b.mmapMapType == MemMapTypeMemFd {
		if err := syscall.Close(b.memFd); err != nil {
			internalLogger.warnf("bufferManager close fd:%d failed, error=%s", b.memFd, err.Error())
		} else {
			internalLogger.infof("bufferManager close fd:%d", b.memFd)
		}
	}
}

func (b *bufferManager) checkBufferReturned() bool {
	for _, l := range b.lists {
		if uint32(atomic.LoadInt32(l.size)) != atomic.LoadUint32(l.cap) {
			return false
		}
		if atomic.LoadUint64(l.pushCount) != atomic.LoadUint64(l.popCount) {
			return false
		}
	}
	return true
}
