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
	"sync"
	"unsafe"
)

var (
	bufferSlicePool = &sync.Pool{
		New: func() interface{} {
			return &bufferSlice{}
		},
	}
)

type bufferHeader []byte

type bufferSlice struct {
	//bufferHeader layout: cap 4 byte | size 4 byte | start 4 byte | next 4 byte | flag 2 byte
	bufferHeader
	data []byte
	cap  uint32
	//use for prepend
	start       uint32
	offsetInShm uint32
	readIndex   int
	writeIndex  int
	isFromShm   bool
	nextSlice   *bufferSlice
}

func (s *bufferSlice) next() *bufferSlice {
	return s.nextSlice
}

func newBufferSlice(header []byte, data []byte, offsetInShm uint32, isFromShm bool) *bufferSlice {
	s := bufferSlicePool.Get().(*bufferSlice)
	if isFromShm && header != nil {
		s.cap = *(*uint32)(unsafe.Pointer(&header[bufferCapOffset]))
		s.start = *(*uint32)(unsafe.Pointer(&header[bufferDataStartOffset]))
		s.writeIndex = int(s.start + *(*uint32)(unsafe.Pointer(&(header[bufferSizeOffset]))))
	} else {
		s.cap = uint32(cap(data))
	}
	s.bufferHeader = header
	s.data = data
	s.offsetInShm = offsetInShm
	s.isFromShm = isFromShm
	return s
}

func putBackBufferSlice(s *bufferSlice) {
	s.isFromShm = false
	s.offsetInShm = 0
	s.data = nil
	s.bufferHeader = nil
	s.cap = 0
	s.writeIndex = 0
	s.readIndex = 0
	s.start = 0
	s.nextSlice = nil
	bufferSlicePool.Put(s)
}

func (s bufferHeader) nextBufferOffset() uint32 {
	return *(*uint32)(unsafe.Pointer(&s[nextBufferOffset]))
}

func (s bufferHeader) hasNext() bool {
	return (s[bufferFlagOffset] & hasNextBufferFlag) > 0
}

func (s bufferHeader) clearFlag() {
	s[bufferFlagOffset] = 0
}

func (s bufferHeader) setInUsed() {
	s[bufferFlagOffset] |= sliceInUsedFlag
}

func (s bufferHeader) isInUsed() bool {
	return (s[bufferFlagOffset] & sliceInUsedFlag) > 0
}

func (s bufferHeader) linkNext(next uint32) {
	*(*uint32)(unsafe.Pointer(&s[nextBufferOffset])) = next
	s[bufferFlagOffset] |= hasNextBufferFlag
}

func (s *bufferSlice) update() {
	if s.bufferHeader != nil {
		*(*uint32)(unsafe.Pointer(&s.bufferHeader[bufferSizeOffset])) = uint32(s.size())
		*(*uint32)(unsafe.Pointer(&s.bufferHeader[bufferDataStartOffset])) = s.start
		if s.nextSlice != nil {
			s.linkNext(s.nextSlice.offsetInShm)
		}
	}
}

func (s *bufferSlice) reset() {
	if s.bufferHeader != nil {
		*(*uint32)(unsafe.Pointer(&s.bufferHeader[bufferSizeOffset])) = 0
		*(*uint32)(unsafe.Pointer(&s.bufferHeader[bufferDataStartOffset])) = 0
		s.bufferHeader.clearFlag()
	}
	s.writeIndex = 0
	s.readIndex = 0
	s.nextSlice = nil
}

func (s *bufferSlice) size() int {
	return s.writeIndex - s.readIndex
}

func (s *bufferSlice) remain() int {
	return int(s.cap) - s.writeIndex
}

func (s *bufferSlice) capacity() int {
	return int(s.cap)
}

func (s *bufferSlice) reserve(size int) ([]byte, error) {
	start := s.writeIndex
	remain := s.remain()
	if remain >= size {
		s.writeIndex += size
		return s.data[start:s.writeIndex], nil
	}
	return nil, ErrNoMoreBuffer
}

func (s *bufferSlice) prepend() {
	panic("TODO")
}

func (s *bufferSlice) append(data ...byte) int {
	if len(data) == 0 {
		return 0
	}
	copySize := copy(s.data[s.writeIndex:], data)
	s.writeIndex += copySize
	return copySize
}

func (s *bufferSlice) read(size int) (data []byte, err error) {
	unRead := s.size()
	if unRead < size {
		size = unRead
		err = ErrNotEnoughData
	}
	data = s.data[s.readIndex : s.readIndex+size]
	s.readIndex += size
	return
}

func (s *bufferSlice) peek(size int) (data []byte, err error) {
	origin := s.readIndex
	data, err = s.read(size)
	s.readIndex = origin
	return
}

func (s *bufferSlice) skip(size int) int {
	unRead := s.size()
	if unRead > size {
		s.readIndex += size
		return size
	}
	s.readIndex += unRead
	return unRead
}

type sliceList struct {
	frontSlice *bufferSlice
	writeSlice *bufferSlice
	backSlice  *bufferSlice
	len        int
}

func newSliceList() *sliceList {
	return &sliceList{}
}

func (l *sliceList) front() *bufferSlice {
	return l.frontSlice
}

func (l *sliceList) back() *bufferSlice {
	return l.backSlice
}

func (l *sliceList) size() int {
	return l.len
}

func (l *sliceList) pushBack(s *bufferSlice) {
	if s == nil {
		return
	}

	if l.len > 0 {
		l.backSlice.nextSlice = s
	} else {
		l.frontSlice = s
	}

	l.backSlice = s
	l.len++
}

func (l *sliceList) popFront() *bufferSlice {
	r := l.frontSlice

	if l.len > 0 {
		l.len--
		l.frontSlice = l.frontSlice.nextSlice
	}

	if l.len == 0 {
		l.frontSlice = nil
		l.backSlice = nil
	}

	return r
}

func (l *sliceList) splitFromWrite() *bufferSlice {
	nextListHead := l.writeSlice.nextSlice
	l.backSlice = l.writeSlice
	l.backSlice.nextSlice = nil
	nextListSize := 0
	for s := nextListHead; s != nil; s = s.nextSlice {
		nextListSize++
	}
	l.len -= nextListSize
	return nextListHead
}
