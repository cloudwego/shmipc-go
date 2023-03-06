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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBufferManager_CreateAndMapping(t *testing.T) {
	//create
	mem := make([]byte, 32<<20)
	bm1, err := createBufferManager([]*SizePercentPair{
		{4096, 70},
		{16 * 1024, 20},
		{64 * 1024, 10},
	}, "", mem, 0)
	if err != nil {
		t.Fatal("create buffer manager failed, err=" + err.Error())
	}

	allocateFunc := func(bm *bufferManager) {
		for i := 0; i < 10; i++ {
			_, err := bm.allocShmBuffer(4096)
			assert.Equal(t, nil, err)
			_, err = bm.allocShmBuffer(16 * 1024)
			assert.Equal(t, nil, err)
			_, err = bm.allocShmBuffer(64 * 1024)
			assert.Equal(t, nil, err)
		}
	}
	allocateFunc(bm1)

	//mapping
	bm2, err := mappingBufferManager("", mem, 0)
	if err != nil {
		t.Fatal("mapping buffer manager failed, err=" + err.Error())
	}

	for i := range bm1.lists {
		assert.Equal(t, *bm1.lists[i].capPerBuffer, *bm2.lists[i].capPerBuffer)
		assert.Equal(t, *bm1.lists[i].size, *bm2.lists[i].size)
		assert.Equal(t, bm1.lists[i].offsetInShm, bm2.lists[i].offsetInShm)
	}

	allocateFunc(bm2)

	for i := range bm1.lists {
		assert.Equal(t, *bm1.lists[i].capPerBuffer, *bm2.lists[i].capPerBuffer)
		assert.Equal(t, *bm1.lists[i].size, *bm2.lists[i].size)
		assert.Equal(t, bm1.lists[i].offsetInShm, bm2.lists[i].offsetInShm)
	}
}

func TestBufferManager_ReadBufferSlice(t *testing.T) {
	mem := make([]byte, 1<<20)
	bm, err := createBufferManager([]*SizePercentPair{
		{Size: uint32(4096), Percent: 100},
	}, "", mem, 0)
	assert.Equal(t, nil, err)

	s, err := bm.allocShmBuffer(4096)
	assert.Equal(t, nil, err)
	data := make([]byte, 4096)
	rand.Read(data)
	assert.Equal(t, 4096, s.append(data...))
	assert.Equal(t, 4096, s.size())
	s.update()

	s2, err := bm.readBufferSlice(s.offsetInShm)
	assert.Equal(t, nil, err)
	assert.Equal(t, s.capacity(), s2.capacity())
	assert.Equal(t, s.size(), s2.size())

	getData, err := s2.read(4096)
	assert.Equal(t, nil, err)
	assert.Equal(t, data, getData)

	s3, err := bm.readBufferSlice(s.offsetInShm + 1<<20)
	assert.NotEqual(t, nil, err)
	assert.Equal(t, (*bufferSlice)(nil), s3)

	s4, err := bm.readBufferSlice(s.offsetInShm + 4096)
	assert.NotEqual(t, nil, err)
	assert.Equal(t, (*bufferSlice)(nil), s4)
}

func TestBufferManager_AllocRecycle(t *testing.T) {
	//allocBuffer
	mem := make([]byte, 1<<20)
	bm, err := createBufferManager([]*SizePercentPair{
		{Size: 4096, Percent: 50},
		{Size: 8192, Percent: 50},
	}, "", mem, 0)
	assert.Equal(t, nil, err)
	// use first two buffer to record buffer list info（List header）
	assert.Equal(t, uint32(1<<20-4096-8192), bm.remainSize())

	numOfSlice := bm.sliceSize()
	buffers := make([]*bufferSlice, 0, 1024)
	for {
		buf, err := bm.allocShmBuffer(4096)
		if err != nil {
			break
		}
		buffers = append(buffers, buf)
	}
	for i := range buffers {
		bm.recycleBuffer(buffers[i])
	}
	buffers = buffers[:0]

	//allocBuffers, recycleBuffers
	slices := newSliceList()
	size := bm.allocShmBuffers(slices, 256*1024)
	assert.Equal(t, int(size), 256*1024)
	linkedBufferSlices := newEmptyLinkedBuffer(bm)
	for slices.size() > 0 {
		linkedBufferSlices.appendBufferSlice(slices.popFront())
	}
	linkedBufferSlices.done(false)
	bm.recycleBuffers(linkedBufferSlices.sliceList.popFront())
	assert.Equal(t, numOfSlice, bm.sliceSize())
}

func TestBufferList_PutPop(t *testing.T) {
	capPerBuffer := uint32(4096)
	bufferNum := uint32(1000)
	mem := make([]byte, countBufferListMemSize(bufferNum, capPerBuffer))

	l, err := createFreeBufferList(bufferNum, capPerBuffer, mem, 0)
	if err != nil {
		t.Fatal(err)
	}

	buffers := make([]*bufferSlice, 0, 1024)
	originSize := l.remain()
	for i := 0; l.remain() > 0; i++ {
		b, err := l.pop()
		if err != nil {
			t.Fatal(err)
		}
		buffers = append(buffers, b)
		assert.Equal(t, capPerBuffer, b.cap)
		assert.Equal(t, 0, b.size())
		assert.Equal(t, false, b.hasNext())
	}

	for i := range buffers {
		l.push(buffers[i])
	}

	assert.Equal(t, originSize, l.remain())
	for i := 0; l.remain() > 0; i++ {
		b, err := l.pop()
		if err != nil {
			t.Fatal(err)
		}
		buffers = append(buffers, b)
		assert.Equal(t, capPerBuffer, b.cap)
		assert.Equal(t, 0, b.size())
		assert.Equal(t, false, b.hasNext())
	}
}

func TestBufferList_ConcurrentPutPop(t *testing.T) {
	capPerBuffer := uint32(10)
	bufferNum := uint32(10)
	mem := make([]byte, countBufferListMemSize(bufferNum, capPerBuffer))
	l, err := createFreeBufferList(bufferNum, capPerBuffer, mem, 0)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var finishedWg sync.WaitGroup
	var startWg sync.WaitGroup
	concurrency := 100
	finishedWg.Add(concurrency)
	startWg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer finishedWg.Done()
			//put and pop
			startWg.Done()
			<-start
			for j := 0; j < 10000; j++ {
				var err error
				var b *bufferSlice
				b, err = l.pop()
				for err != nil {
					time.Sleep(time.Millisecond)
					b, err = l.pop()
				}
				assert.Equal(t, capPerBuffer, b.cap)
				assert.Equal(t, 0, b.size())
				assert.Equal(t, false, b.hasNext(), "offset:%d next:%d", b.offsetInShm, b.nextBufferOffset())
				l.push(b)
			}
		}()
	}
	startWg.Wait()
	close(start)
	finishedWg.Wait()
	assert.Equal(t, bufferNum, uint32(*l.size))
}

func TestBufferList_CreateAndMappingFreeBufferList(t *testing.T) {
	capPerBuffer := uint32(10)
	bufferNum := uint32(10)
	mem := make([]byte, countBufferListMemSize(bufferNum, capPerBuffer))
	l, err := createFreeBufferList(0, capPerBuffer, mem, 0)
	assert.NotEqual(t, nil, err)
	assert.Equal(t, (*bufferList)(nil), l)

	mem = make([]byte, countBufferListMemSize(bufferNum, capPerBuffer))
	l, err = createFreeBufferList(bufferNum+1, capPerBuffer, mem, 0)
	assert.NotEqual(t, nil, err)
	assert.Equal(t, (*bufferList)(nil), l)

	mem = make([]byte, countBufferListMemSize(bufferNum, capPerBuffer))
	l, err = createFreeBufferList(bufferNum, capPerBuffer, mem, 0)
	assert.Equal(t, nil, err)
	assert.NotEqual(t, (*bufferList)(nil), l)

	testMem := make([]byte, 10)
	ml, err := mappingFreeBufferList(testMem, 0)
	assert.NotEqual(t, nil, err)
	assert.Equal(t, (*bufferList)(nil), ml)

	ml, err = mappingFreeBufferList(mem, 10)
	assert.NotEqual(t, nil, err)
	assert.Equal(t, (*bufferList)(nil), ml)

	ml, err = mappingFreeBufferList(mem, 0)
	assert.Equal(t, nil, err)
	assert.NotEqual(t, (*bufferList)(nil), ml)

	if err != nil {
		t.Fatalf("fail to mapping bufferlist:%s", err.Error())
	}

}

func BenchmarkBufferList_PutPop(b *testing.B) {
	capPerBuffer := uint32(10)
	bufferNum := uint32(10000)
	mem := make([]byte, countBufferListMemSize(bufferNum, capPerBuffer))
	l, err := createFreeBufferList(bufferNum, capPerBuffer, mem, 0)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		buf, err := l.pop()
		if err != nil {
			b.Fatal(err)
		}
		l.push(buf)
	}
}

func BenchmarkBufferList_PutPopParallel(b *testing.B) {
	capPerBuffer := uint32(1)
	bufferNum := uint32(100 * 10000)
	mem := make([]byte, countBufferListMemSize(bufferNum, capPerBuffer))
	l, err := createFreeBufferList(bufferNum, capPerBuffer, mem, 0)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			var err error
			var buf *bufferSlice
			buf, err = l.pop()
			for err != nil {
				time.Sleep(time.Millisecond)
				buf, err = l.pop()
			}
			l.push(buf)
		}
	})
}

func TestCreateFreeBufferList(t *testing.T) {
	_, err := createFreeBufferList(4294967295, 4294967295, []byte{'w'}, 4294967279)
	assert.NotNil(t, err)
}