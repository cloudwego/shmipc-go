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
	"fmt"
	_ "net/http/pprof"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var (
	queueCap    = 1000000
	parallelism = 100
)

func TestQueueManager_CreateMapping(t *testing.T) {
	path := "/tmp/ipc.queue"
	qm1, err := createQueueManager(path, 8192)
	assert.Equal(t, nil, err)
	qm2, err := mappingQueueManager(path)
	assert.Equal(t, nil, err)

	assert.Equal(t, nil, qm1.sendQueue.put(queueElement{}))
	_, err = qm2.recvQueue.pop()
	assert.Equal(t, nil, err)

	assert.Equal(t, nil, qm2.sendQueue.put(queueElement{}))
	_, err = qm1.recvQueue.pop()
	assert.Equal(t, nil, err)

	qm1.unmap()
}

func TestQueueOperate(t *testing.T) {
	q := createQueue(defaultQueueCap)

	fmt.Println("-----------test queue operate ----------------")
	assert.Equal(t, true, q.isEmpty(), "queue should be empty")
	assert.Equal(t, false, q.isFull(), "queue is not full")
	assert.Equal(t, int64(0), q.size(), "queue size should be 0")

	putCount, popCount := 0, 0
	var err error
	for i := 0; i < defaultQueueCap; i++ {
		err = q.put(queueElement{seqID: uint32(i), offsetInShmBuf: uint32(i), status: uint32(i)})
		assert.Equal(t, nil, err)
		putCount++
	}
	err = q.put(queueElement{1, 1, 1})
	assert.Equal(t, ErrQueueFull, err)
	assert.Equal(t, true, q.isFull(), "queue should be full")
	assert.Equal(t, false, q.isEmpty(), "queue is not empty")
	assert.Equal(t, int64(putCount), q.size(), "queue size")

	for i := 0; i < defaultQueueCap; i++ {
		e, err := q.pop()
		assert.Equal(t, nil, err)
		popCount++
		assert.Equal(t, i, int(e.seqID), "queue pop verify seqID")
		assert.Equal(t, i, int(e.offsetInShmBuf), "queue pop verify offset")
		assert.Equal(t, i, int(e.status), "queue pop verify offset")
	}
	_, err = q.pop()
	assert.Equal(t, errQueueEmpty, err)
	assert.Equal(t, false, q.isFull(), "queue is not full")
	assert.Equal(t, true, q.isEmpty(), "queue should be empty")
	assert.Equal(t, int64(0), q.size(), "queue size")

	fmt.Println("-----------test queue status ----------------")
	assert.Equal(t, false, q.consumerIsWorking(), "consumer should be not working")
	q.markWorking()
	assert.Equal(t, true, q.consumerIsWorking(), "consumer should be working")
	q.markNotWorking()
	assert.Equal(t, false, q.consumerIsWorking(), "consumer should be not working")

	q.put(queueElement{1, 1, 1})
	q.markNotWorking()
	assert.Equal(t, true, q.consumerIsWorking(), "consumer should be working")
}

func TestQueueMultiProducerAndSingleConsumer(t *testing.T) {
	fmt.Println("-----------test queue multi-producer single consumer ----------------")
	q := createQueue(uint32(queueCap))
	var wg sync.WaitGroup
	popCount := 0
	for i := 0; i < parallelism; i++ {
		//producer
		go func() {
			for k := 0; k < queueCap/parallelism; k++ {
				wg.Add(1)
				if err := q.put(queueElement{seqID: 1, offsetInShmBuf: 1, status: 1}); err != nil {
					panic(err)
				}
			}
		}()
	}

	//consumer
	for popCount != queueCap {
		_, err := q.pop()
		if err == nil {
			wg.Done()
			popCount++
		} else {
			time.Sleep(time.Microsecond)
		}
	}
	wg.Wait()
}

func BenchmarkQueuePut(b *testing.B) {
	q := createQueue(uint32(b.N))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		q.put(queueElement{seqID: uint32(i), offsetInShmBuf: uint32(i)})
	}
}

func BenchmarkQueuePop(b *testing.B) {
	q := createQueue(uint32(b.N))
	for i := 0; i < b.N; i++ {
		q.put(queueElement{seqID: uint32(i), offsetInShmBuf: uint32(i)})
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		q.pop()
	}
}

func BenchmarkQueueMultiPut(b *testing.B) {
	b.SetParallelism(50)
	q := createQueue(uint32(b.N))
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		c := 0
		for pb.Next() {
			c++
			q.put(queueElement{seqID: uint32(c), offsetInShmBuf: uint32(c)})

		}
	})
}

func BenchmarkQueueMultiPop(b *testing.B) {
	b.SetParallelism(50)
	q := createQueue(uint32(b.N))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		q.put(queueElement{seqID: uint32(i), offsetInShmBuf: uint32(i)})
	}
	b.RunParallel(func(pb *testing.PB) {

		for pb.Next() {
			q.pop()
		}
	})
}
