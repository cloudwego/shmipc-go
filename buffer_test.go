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
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

var (
	shmPath = "/tmp/ipc.test"
	bm      *bufferManager
)

func initShm() {
	os.Remove(shmPath)
	shmSize := 10 << 20
	var err error
	mem := make([]byte, shmSize)
	bm, err = createBufferManager([]*SizePercentPair{
		{defaultSingleBufferSize, 70},
		{16 * 1024, 20},
		{64 * 1024, 10},
	}, "", mem, 0)
	if err != nil {
		panic(err)
	}
}

func newLinkedBufferWithSlice(manager *bufferManager, slice *bufferSlice) *linkedBuffer {
	l := &linkedBuffer{
		sliceList:     newSliceList(),
		pinnedList:    newSliceList(),
		bufferManager: manager,
		endStream:     true,
		isFromShm:     slice.isFromShm,
	}
	l.sliceList.pushBack(slice)
	l.sliceList.writeSlice = l.sliceList.back()
	return l
}

func TestLinkedBuffer_ReadWrite(t *testing.T) {
	initShm()
	factory := func() *linkedBuffer {
		buf, err := bm.allocShmBuffer(1024)
		if err != nil {
			t.Fatal(err)
			return nil
		}
		return newLinkedBufferWithSlice(bm, buf)
	}
	testLinkedBufferReadBytes(t, factory)
}

func TestLinkedBuffer_ReleasePreviousRead(t *testing.T) {
	initShm()
	slice, err := bm.allocShmBuffer(1024)
	if err != nil {
		t.Fatal(err)
	}
	buf := newLinkedBufferWithSlice(bm, slice)
	sliceNum := 100
	for i := 0; i < sliceNum*4096; i++ {
		assert.Equal(t, nil, buf.WriteByte(byte(i)))
	}
	buf.done(true)

	for i := 0; i < sliceNum/2; i++ {
		r, err := buf.ReadBytes(4096)
		assert.Equal(t, 4096, len(r), "buf.Len():%d", buf.Len())
		assert.Equal(t, nil, err)
	}
	assert.Equal(t, (sliceNum/2)-1, buf.pinnedList.size())
	_, _ = buf.Discard(buf.Len())

	buf.releasePreviousReadAndReserve()
	assert.Equal(t, 0, buf.pinnedList.size())
	assert.Equal(t, 0, buf.Len())
	//the last slice shouldn't release
	assert.Equal(t, 1, buf.sliceList.size())
	assert.Equal(t, true, buf.sliceList.writeSlice != nil)

	buf.ReleasePreviousRead()
	assert.Equal(t, 0, buf.sliceList.size())
	assert.Equal(t, true, buf.sliceList.writeSlice == nil)
}

func TestLinkedBuffer_FallbackWhenWrite(t *testing.T) {
	mem := make([]byte, 10*1024)
	bm, err := createBufferManager([]*SizePercentPair{
		{Size: 1024, Percent: 100},
	}, "", mem, 0)
	assert.Equal(t, nil, err)

	buf, err := bm.allocShmBuffer(1024)
	assert.Equal(t, nil, err)
	writer := newLinkedBufferWithSlice(bm, buf)
	dataSize := 1024
	mockDataArray := make([][]byte, 100)
	for i := range mockDataArray {
		mockDataArray[i] = make([]byte, dataSize)
		rand.Read(mockDataArray[i])
		n, err := writer.WriteBytes(mockDataArray[i])
		assert.Equal(t, dataSize, n)
		assert.Equal(t, err, nil)
		assert.Equal(t, dataSize*(i+1), writer.Len())
	}
	assert.Equal(t, false, writer.isFromShm)

	reader := writer.done(false)
	all := dataSize * len(mockDataArray)
	assert.Equal(t, all, writer.Len())

	for i := range mockDataArray {
		assert.Equal(t, all-i*dataSize, reader.Len())
		get, err := reader.ReadBytes(dataSize)
		if err != nil {
			t.Fatal("reader.ReadBytes error", err, i)
		}
		assert.Equal(t, mockDataArray[i], get)
	}
}

func testBufferReadString(t *testing.T, createBufferWriter func() *linkedBuffer) {
	writer := createBufferWriter()
	oneSliceSize := 16 << 10
	strBytesArray := make([][]byte, 100)
	for i := 0; i < len(strBytesArray); i++ {
		strBytesArray[i] = make([]byte, oneSliceSize)
		rand.Read(strBytesArray[i])
		_, err := writer.WriteBytes(strBytesArray[i])
		assert.Equal(t, true, nil == err)
	}

	reader := writer.done(false)
	for i := 0; i < len(strBytesArray); i++ {
		str, err := reader.ReadString(oneSliceSize)
		assert.Equal(t, true, nil == err)
		assert.Equal(t, string(strBytesArray[i]), str)
	}
}

// TODO: ensure reserving logic
func TestLinkedBuffer_Reserve(t *testing.T) {
	initShm()

	// alloc 3 buffer slice
	buffer := newLinkedBuffer(bm, (64+64+64)*1024)
	assert.Equal(t, 3, buffer.sliceList.size())
	assert.Equal(t, true, buffer.isFromShm)
	assert.Equal(t, buffer.sliceList.front(), buffer.sliceList.writeSlice)

	// reserve a buf in first slice
	ret, err := buffer.Reserve(60 * 1024)
	if err != nil {
		t.Fatal("LinkedBuffer Reserve error", err)
	}
	assert.Equal(t, len(ret), 60*1024)
	assert.Equal(t, 3, buffer.sliceList.size())
	assert.Equal(t, true, buffer.isFromShm)
	assert.Equal(t, buffer.sliceList.front(), buffer.sliceList.writeSlice)

	// reserve a buf in the second slice when the first one is not enough
	ret, err = buffer.Reserve(6 * 1024)
	if err != nil {
		t.Fatal("LinkedBuffer Reserve error", err)
	}
	assert.Equal(t, len(ret), 6*1024)
	assert.Equal(t, 3, buffer.sliceList.size())
	assert.Equal(t, true, buffer.isFromShm)
	assert.Equal(t, buffer.sliceList.front().next(), buffer.sliceList.writeSlice)

	// reserve a buf in a new allocated slice
	ret, err = buffer.Reserve(128 * 1024)
	if err != nil {
		t.Fatal("LinkedBuffer Reserve error", err)
	}
	assert.Equal(t, len(ret), 128*1024)
	assert.Equal(t, 4, buffer.sliceList.size())
	assert.Equal(t, false, buffer.isFromShm)
	assert.Equal(t, buffer.sliceList.back(), buffer.sliceList.writeSlice)
}

func newLinkedBuffer(manager *bufferManager, size uint32) *linkedBuffer {
	l := &linkedBuffer{
		sliceList:     newSliceList(),
		pinnedList:    newSliceList(),
		bufferManager: manager,
		isFromShm:     true,
	}
	l.alloc(size)
	l.sliceList.writeSlice = l.sliceList.front()
	return l
}

func TestLinkedBuffer_Done(t *testing.T) {
	initShm()
	mockDataSize := 128 * 1024
	mockData := make([]byte, mockDataSize)
	rand.Read(mockData)
	// alloc 3 buffer slice
	buffer := newLinkedBuffer(bm, (64+64+64)*1024)
	assert.Equal(t, 3, buffer.sliceList.size())

	// write data to full 2 slice, remove one
	_, _ = buffer.WriteBytes(mockData)
	reader := buffer.done(true)
	assert.Equal(t, 2, buffer.sliceList.size())
	getBytes, _ := reader.ReadBytes(mockDataSize)
	assert.Equal(t, mockData, getBytes)
}

func testLinkedBufferReadBytes(t *testing.T, createBufferWriter func() *linkedBuffer) {

	writeAndRead := func(buf *linkedBuffer) {
		//2 MB
		size := 1 << 21
		data := make([]byte, size)
		rand.Read(data)
		for buf.Len() < size {
			oneWriteSize := rand.Intn(size / 10)
			if buf.Len()+oneWriteSize > size {
				oneWriteSize = size - buf.Len()
			}
			n, err := buf.WriteBytes(data[buf.Len() : buf.Len()+oneWriteSize])
			assert.Equal(t, true, err == nil, err)
			assert.Equal(t, oneWriteSize, n)
		}

		buf.done(false)
		read := 0
		for buf.Len() > 0 {
			oneReadSize := rand.Intn(size / 10000)
			if read+oneReadSize > buf.Len() {
				oneReadSize = buf.Len()
			}
			//do nothing
			_, _ = buf.Peek(oneReadSize)

			readData, err := buf.ReadBytes(oneReadSize)
			assert.Equal(t, true, err == nil, err)
			if len(readData) == 0 {
				assert.Equal(t, oneReadSize, 0)
			} else {
				assert.Equal(t, data[read:read+oneReadSize], readData)
			}
			read += oneReadSize
		}
		assert.Equal(t, 1<<21, read)
		_, _ = buf.ReadBytes(-10)
		_, _ = buf.ReadBytes(0)
		buf.ReleasePreviousRead()
	}

	for i := 0; i < 100; i++ {
		writeAndRead(createBufferWriter())
	}
}
