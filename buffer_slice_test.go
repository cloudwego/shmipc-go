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
	"math/rand"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
)

func TestBufferSlice_ReadWrite(t *testing.T) {
	size := 8192
	slice := newBufferSlice(nil, make([]byte, size), 0, false)

	for i := 0; i < size; i++ {
		n := slice.append(byte(i))
		assert.Equal(t, 1, n)
	}
	n := slice.append(byte(10))
	assert.Equal(t, 0, n)

	data, err := slice.read(size * 10)
	assert.Equal(t, size, len(data))
	assert.Equal(t, err, ErrNotEnoughData)

	//verify read data.
	for i := 0; i < size; i++ {
		assert.Equal(t, byte(i), data[i])
	}
}

func TestBufferSlice_Skip(t *testing.T) {
	slice := newBufferSlice(nil, make([]byte, 8192), 0, false)
	slice.append(make([]byte, slice.capacity())...)
	remain := slice.capacity()

	n := slice.skip(10)
	remain -= n
	assert.Equal(t, remain, slice.size())

	n = slice.skip(100)
	remain -= n
	assert.Equal(t, remain, slice.size())

	n = slice.skip(10000)
	assert.Equal(t, 0, slice.size())
}

func TestBufferSlice_Reserve(t *testing.T) {
	size := 8192
	slice := newBufferSlice(nil, make([]byte, size), 0, false)
	data1, err := slice.reserve(100)
	assert.Equal(t, nil, err)
	assert.Equal(t, 100, len(data1))

	data2, err := slice.reserve(size)
	assert.Equal(t, ErrNoMoreBuffer, err)
	assert.Equal(t, 0, len(data2))

	for i := range data1 {
		data1[i] = byte(i)
	}
	for i := range data2 {
		data2[i] = byte(i)
	}

	readData, err := slice.read(100)
	assert.Equal(t, nil, err)
	assert.Equal(t, len(data1), len(readData))

	for i := 0; i < len(data1); i++ {
		assert.Equal(t, data1[i], readData[i])
	}

	readData, err = slice.read(10000)
	assert.Equal(t, ErrNotEnoughData, err)
	assert.Equal(t, len(data2), len(readData))

	for i := 0; i < len(data2); i++ {
		assert.Equal(t, data2[i], readData[i])
	}
}

func TestBufferSlice_Update(t *testing.T) {
	size := 8192
	header := make([]byte, bufferHeaderSize)
	*(*uint32)(unsafe.Pointer(&header[bufferCapOffset])) = uint32(size)
	slice := newBufferSlice(header, make([]byte, size), 0, true)

	n := slice.append(make([]byte, size)...)
	assert.Equal(t, size, n)
	slice.update()
	assert.Equal(t, size, int(*(*uint32)(unsafe.Pointer(&slice.bufferHeader[bufferSizeOffset]))))
}

func TestBufferSlice_linkedNext(t *testing.T) {
	size := 8192
	sliceNum := 100

	slices := make([]*bufferSlice, 0, sliceNum)
	mem := make([]byte, 10<<20)
	bm, err := createBufferManager([]*SizePercentPair{
		{Size: uint32(size), Percent: 100},
	}, "", mem, 0)
	assert.Equal(t, nil, err)

	writeDataArray := make([][]byte, 0, sliceNum)

	for i := 0; i < sliceNum; i++ {
		s, err := bm.allocShmBuffer(uint32(size))
		assert.Equal(t, nil, err, "i:%d", i)
		data := make([]byte, size)
		rand.Read(data)
		writeDataArray = append(writeDataArray, data)
		assert.Equal(t, size, s.append(data...))
		s.update()
		slices = append(slices, s)
	}

	for i := 0; i <= len(slices)-2; i++ {
		slices[i].linkNext(slices[i+1].offsetInShm)
	}

	next := slices[0].offsetInShm
	for i := 0; i < sliceNum; i++ {
		s, err := bm.readBufferSlice(next)
		assert.Equal(t, nil, err)
		assert.Equal(t, size, s.capacity())
		assert.Equal(t, size, s.size())
		readData, err := s.read(size)
		assert.Equal(t, nil, err, "i:%d offset:%d", i, next)
		assert.Equal(t, readData, writeDataArray[i])
		isLastSlice := i == sliceNum-1
		assert.Equal(t, !isLastSlice, s.hasNext())
		next = s.nextBufferOffset()
	}
}

func TestSliceList_PushPop(t *testing.T) {
	//1. twice push , twice pop
	l := newSliceList()
	l.pushBack(newBufferSlice(nil, make([]byte, 1024), 0, false))
	assert.Equal(t, l.front(), l.back())
	assert.Equal(t, 1, l.size())

	l.pushBack(newBufferSlice(nil, make([]byte, 1024), 0, false))
	assert.Equal(t, 2, l.size())
	assert.Equal(t, false, l.front() == l.back())

	assert.Equal(t, l.front(), l.popFront())
	assert.Equal(t, 1, l.size())
	assert.Equal(t, l.front(), l.back())

	assert.Equal(t, l.front(), l.popFront())
	assert.Equal(t, 0, l.size())
	assert.Equal(t, true, l.front() == nil)
	assert.Equal(t, true, l.back() == nil)

	// multi push and pop
	const iterCount = 100
	for i := 0; i < iterCount; i++ {
		l.pushBack(newBufferSlice(nil, make([]byte, 1024), 0, false))
		assert.Equal(t, i+1, l.size())
	}
	for i := 0; i < iterCount; i++ {
		l.popFront()
		assert.Equal(t, iterCount-(i+1), l.size())
	}
	assert.Equal(t, 0, l.size())
	assert.Equal(t, true, l.front() == nil)
	assert.Equal(t, true, l.back() == nil)
}

func TestSliceList_SplitFromWrite(t *testing.T) {
	//1. sliceList's size == 1
	l := newSliceList()
	l.pushBack(newBufferSlice(nil, make([]byte, 1024), 0, false))
	l.writeSlice = l.front()
	assert.Equal(t, true, l.splitFromWrite() == nil)
	assert.Equal(t, 1, l.size())
	assert.Equal(t, l.front(), l.back())
	assert.Equal(t, l.back(), l.writeSlice)

	//2. sliceList's size == 2, writeSlice's index is 0
	l = newSliceList()
	l.pushBack(newBufferSlice(nil, make([]byte, 1024), 0, false))
	l.pushBack(newBufferSlice(nil, make([]byte, 1024), 0, false))
	l.writeSlice = l.front()
	assert.Equal(t, l.back(), l.splitFromWrite())
	assert.Equal(t, 1, l.size())
	assert.Equal(t, l.front(), l.back())
	assert.Equal(t, l.back(), l.writeSlice)

	//2. sliceList's size == 2, writeSlice's index is 1
	l = newSliceList()
	l.pushBack(newBufferSlice(nil, make([]byte, 1024), 0, false))
	l.pushBack(newBufferSlice(nil, make([]byte, 1024), 0, false))
	l.writeSlice = l.back()
	assert.Equal(t, true, l.splitFromWrite() == nil)
	assert.Equal(t, 2, l.size())
	assert.Equal(t, l.back(), l.writeSlice)

	//3. sliceList's size == 2, writeSlice's index is 50
	l = newSliceList()
	for i := 0; i < 100; i++ {
		l.pushBack(newBufferSlice(nil, make([]byte, 1024), 0, false))
		if i == 50 {
			l.writeSlice = l.back()
		}
	}
	l.splitFromWrite()
	assert.Equal(t, l.writeSlice, l.back())
	assert.Equal(t, 51, l.size())
}
