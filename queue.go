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
	syscall "golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"unsafe"
)

const (
	queueHeaderLength = 24
)

type queueManager struct {
	path        string
	sendQueue   *queue
	recvQueue   *queue
	mem         []byte
	mmapMapType MemMapType
	memFd       int
}

// default cap is 16384, which mean that 16384 * 8 = 128 KB memory.
type queue struct {
	sync.Mutex
	head               *int64  // consumer write, producer read
	tail               *int64  // producer write, consumer read
	workingFlag        *uint32 //when peer is consuming the queue, the workingFlag is 1, otherwise 0.
	cap                int64
	queueBytesOnMemory []byte // it could be from share memory or process memory.
}

type queueElement struct {
	seqID          uint32
	offsetInShmBuf uint32
	status         uint32
}

func createQueueManagerWithMemFd(queuePathName string, queueCap uint32) (*queueManager, error) {
	memFd, err := MemfdCreate(queuePathName, 0)
	if err != nil {
		return nil, err
	}

	memSize := countQueueMemSize(queueCap) * queueCount
	if err := syscall.Ftruncate(memFd, int64(memSize)); err != nil {
		return nil, fmt.Errorf("createQueueManagerWithMemFd truncate share memory failed,%w", err)
	}

	mem, err := syscall.Mmap(memFd, 0, memSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(mem); i++ {
		mem[i] = 0
	}

	return &queueManager{
		sendQueue:   createQueueFromBytes(mem[:memSize/2], queueCap),
		recvQueue:   createQueueFromBytes(mem[memSize/2:], queueCap),
		mem:         mem,
		path:        queuePathName,
		mmapMapType: MemMapTypeMemFd,
		memFd:       memFd,
	}, nil
}

func createQueueManager(shmPath string, queueCap uint32) (*queueManager, error) {
	//ignore mkdir error
	_ = os.MkdirAll(filepath.Dir(shmPath), os.ModePerm)
	if pathExists(shmPath) {
		return nil, errors.New("queue was existed,path" + shmPath)
	}
	memSize := countQueueMemSize(queueCap) * queueCount
	if !canCreateOnDevShm(uint64(memSize), shmPath) {
		return nil, fmt.Errorf("err:%s path:%s, size:%d", ErrShareMemoryHadNotLeftSpace.Error(), shmPath, memSize)
	}
	f, err := os.OpenFile(shmPath, os.O_CREATE|os.O_RDWR, os.ModePerm)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if err := f.Truncate(int64(memSize)); err != nil {
		return nil, fmt.Errorf("truncate share memory failed,%s", err.Error())
	}
	mem, err := syscall.Mmap(int(f.Fd()), 0, memSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(mem); i++ {
		mem[i] = 0
	}
	return &queueManager{
		sendQueue: createQueueFromBytes(mem[:memSize/2], queueCap),
		recvQueue: createQueueFromBytes(mem[memSize/2:], queueCap),
		mem:       mem,
		path:      shmPath,
	}, nil
}

func mappingQueueManagerMemfd(queuePathName string, memFd int) (*queueManager, error) {
	var fileInfo syscall.Stat_t
	if err := syscall.Fstat(memFd, &fileInfo); err != nil {
		return nil, err
	}

	mappingSize := int(fileInfo.Size)
	//a queueManager have two queue, a queue's head and tail should align to 8 byte boundary
	if isArmArch() && mappingSize%16 != 0 {
		return nil, fmt.Errorf("the memory size of queue should be a multiple of 16")
	}
	mem, err := syscall.Mmap(memFd, 0, mappingSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	return &queueManager{
		sendQueue:   mappingQueueFromBytes(mem[mappingSize/2:]),
		recvQueue:   mappingQueueFromBytes(mem[:mappingSize/2]),
		mem:         mem,
		path:        queuePathName,
		memFd:       memFd,
		mmapMapType: MemMapTypeMemFd,
	}, nil
}

func mappingQueueManager(shmPath string) (*queueManager, error) {
	f, err := os.OpenFile(shmPath, os.O_RDWR, os.ModePerm)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fileInfo, err := f.Stat()
	if err != nil {
		return nil, err
	}
	mappingSize := int(fileInfo.Size())

	//a queueManager have two queue, a queue's head and tail should align to 8 byte boundary
	if isArmArch() && mappingSize%16 != 0 {
		return nil, fmt.Errorf("the memory size of queue should be a multiple of 16")
	}
	mem, err := syscall.Mmap(int(f.Fd()), 0, mappingSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	return &queueManager{
		sendQueue: mappingQueueFromBytes(mem[mappingSize/2:]),
		recvQueue: mappingQueueFromBytes(mem[:mappingSize/2]),
		mem:       mem,
		path:      shmPath,
	}, nil
}

func countQueueMemSize(queueCap uint32) int {
	return queueHeaderLength + queueElementLen*int(queueCap)
}

func createQueueFromBytes(data []byte, cap uint32) *queue {
	*(*uint32)(unsafe.Pointer(&data[0])) = cap
	q := mappingQueueFromBytes(data)
	*q.head = 0
	*q.tail = 0
	*q.workingFlag = 0
	return q
}

func mappingQueueFromBytes(data []byte) *queue {
	cap := *(*uint32)(unsafe.Pointer(&data[0]))
	queueStartOffset := queueHeaderLength
	queueEndOffset := queueHeaderLength + cap*queueElementLen
	if isArmArch() {
		// align 8 byte boundary for head and tail
		return &queue{
			cap:                int64(cap),
			workingFlag:        (*uint32)(unsafe.Pointer(&data[4])),
			head:               (*int64)(unsafe.Pointer(&data[8])),
			tail:               (*int64)(unsafe.Pointer(&data[16])),
			queueBytesOnMemory: data[queueStartOffset:queueEndOffset],
		}
	}
	return &queue{
		cap:                int64(cap),
		head:               (*int64)(unsafe.Pointer(&data[4])),
		tail:               (*int64)(unsafe.Pointer(&data[12])),
		workingFlag:        (*uint32)(unsafe.Pointer(&data[20])),
		queueBytesOnMemory: data[queueStartOffset:queueEndOffset],
	}
}

// cap prefer equals 2^n
func createQueue(cap uint32) *queue {
	return createQueueFromBytes(make([]byte, queueHeaderLength+int(cap*queueElementLen)), cap)
}

func (q *queueManager) unmap() {
	if err := syscall.Munmap(q.mem); err != nil {
		internalLogger.warnf("queueManager unmap error:" + err.Error())
	}
	if q.mmapMapType == MemMapTypeDevShmFile {
		if err := os.Remove(q.path); err != nil {
			internalLogger.warnf("queueManager remove file:%s failed, error=%s", q.path, err.Error())
		} else {
			internalLogger.infof("queueManager remove file:%s", q.path)
		}
	} else {
		if err := syscall.Close(q.memFd); err != nil {
			internalLogger.warnf("queueManager close queue fd:%d, error:%s", q.memFd, err.Error())
		} else {
			internalLogger.infof("queueManager close queue fd:%d", q.memFd)
		}
	}
}

func (q *queue) isFull() bool {
	return q.size() == q.cap
}

func (q *queue) isEmpty() bool {
	return q.size() == 0
}

func (q *queue) size() int64 {
	return atomic.LoadInt64(q.tail) - atomic.LoadInt64(q.head)
}

func (q *queue) pop() (e queueElement, err error) {
	//atomic ensure the data that  peer write to share memory could be see.
	head := atomic.LoadInt64(q.head)
	if head >= atomic.LoadInt64(q.tail) {
		err = errQueueEmpty
		return
	}
	queueOffset := (head % q.cap) * queueElementLen
	e.seqID = *(*uint32)(unsafe.Pointer(&q.queueBytesOnMemory[queueOffset]))
	e.offsetInShmBuf = *(*uint32)(unsafe.Pointer(&q.queueBytesOnMemory[queueOffset+4]))
	e.status = *(*uint32)(unsafe.Pointer(&q.queueBytesOnMemory[queueOffset+8]))
	atomic.AddInt64(q.head, 1)
	return
}

func (q *queue) put(e queueElement) error {
	//ensure that increasing q.tail and writing queueElement are both atomic.
	//because if firstly increase q.tail, the peer will think that the queue have new element and will consume it.
	//but at this moment, the new element hadn't finished writing, the peer will get a old element.
	q.Lock()
	tail := atomic.LoadInt64(q.tail)
	if tail-atomic.LoadInt64(q.head) >= q.cap {
		q.Unlock()
		return ErrQueueFull
	}
	queueOffset := (tail % q.cap) * queueElementLen
	*(*uint32)(unsafe.Pointer(&q.queueBytesOnMemory[queueOffset])) = e.seqID
	*(*uint32)(unsafe.Pointer(&q.queueBytesOnMemory[queueOffset+4])) = e.offsetInShmBuf
	*(*uint32)(unsafe.Pointer(&q.queueBytesOnMemory[queueOffset+8])) = e.status
	atomic.AddInt64(q.tail, 1)
	q.Unlock()
	return nil
}

func (q *queue) consumerIsWorking() bool {
	return (atomic.LoadUint32(q.workingFlag)) > 0
}

func (q *queue) markWorking() bool {
	return atomic.CompareAndSwapUint32(q.workingFlag, 0, 1)
}

func (q *queue) markNotWorking() bool {
	atomic.StoreUint32(q.workingFlag, 0)
	if q.size() == 0 {
		return true
	}
	atomic.StoreUint32(q.workingFlag, 1)
	return false
}
