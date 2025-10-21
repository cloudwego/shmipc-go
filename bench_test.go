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
	"bytes"
	"encoding/json"
	"fmt"
	syscall "golang.org/x/sys/unix"
	"io"
	"math/rand"
	"net"
	"net/http"
	_ "net/http/pprof"
	"strconv"
	"testing"
	"time"
)

type pingpong struct {
	Data []byte `json:"data"`
}

var (
	likelySizes = map[string]int{
		"64B":   100,
		"512B":  696,
		"1KB":   1380,
		"4KB":   5476,
		"16KB":  21860,
		"64KB":  87396,
		"256KB": 349540,
		"512KB": 699064,
		"1MB":   1398116,
		"4MB":   5592420,
	}

	dataSizes = map[string]int{
		"64B":   64,
		"512B":  512,
		"1KB":   1 << 10,
		"4KB":   4 << 10,
		"16KB":  16 << 10,
		"64KB":  64 << 10,
		"256KB": 256 << 10,
		"512KB": 512 << 10,
		"1MB":   1 << 20,
		"4MB":   4 << 20,
	}
)

func init() {
	SetLogLevel(levelNoPrint)
	go func() {
		fmt.Println(http.ListenAndServe("0.0.0.0:29998", nil)) //nolint:errcheck
	}()
}

func benchmarkConfig() *Config {
	c := DefaultConfig()
	c.QueueCap = 65535
	c.ConnectionWriteTimeout = time.Second
	c.ShareMemoryBufferCap = 256 << 20
	c.MemMapType = MemMapTypeMemFd
	return c
}

func newBenchmarkClientServer(likelySize uint32) (client, server *Session) {
	config := benchmarkConfig()
	if likelySize >= 4<<20 {
		config.ShareMemoryBufferCap = 512 << 20
	}
	config.BufferSliceSizes = []*SizePercentPair{
		{likelySize, 100},
	}
	config.ShareMemoryPathPrefix += strconv.Itoa(int(rand.Int63()))
	addr := &net.UnixAddr{Name: "/dev/shm/shmipc.sock", Net: "unix"}
	serverStartNotifyCh := make(chan struct{})
	go func() {
		ln, err := net.ListenUnix("unix", addr)
		if err != nil {
			panic("create listener failed:" + err.Error())
		}
		serverStartNotifyCh <- struct{}{}
		conn, err := ln.Accept()
		if err != nil {
			panic("accept conn failed:" + err.Error())
		}
		defer ln.Close()
		server, err = Server(conn, config)
		if err != nil {
			panic("create shmipc server failed:" + err.Error())
		}
		serverStartNotifyCh <- struct{}{}
	}()
	<-serverStartNotifyCh
	conn, err := net.DialUnix("unix", nil, addr)
	if err != nil {
		panic("dial uds failed:" + err.Error())
	}
	client, err = newSession(config, conn, true)
	if err != nil {
		panic("create ipc client failed:" + err.Error())
	}
	<-serverStartNotifyCh
	return
}

func BenchmarkParallelPingPongByShmipc64B(b *testing.B) {
	benchmarkParallelPingPongByShmipc(b, "64B")
}

func BenchmarkParallelPingPongByShmipc512B(b *testing.B) {
	benchmarkParallelPingPongByShmipc(b, "512B")
}

func BenchmarkParallelPingPongByShmipc1KB(b *testing.B) {
	benchmarkParallelPingPongByShmipc(b, "1KB")
}

func BenchmarkParallelPingPongByShmipc4KB(b *testing.B) {
	benchmarkParallelPingPongByShmipc(b, "4KB")
}

func BenchmarkParallelPingPongByShmipc16KB(b *testing.B) {
	benchmarkParallelPingPongByShmipc(b, "16KB")
}

func BenchmarkParallelPingPongByShmipc64KB(b *testing.B) {
	benchmarkParallelPingPongByShmipc(b, "64KB")
}

func BenchmarkParallelPingPongByShmipc256KB(b *testing.B) {
	benchmarkParallelPingPongByShmipc(b, "256KB")
}

func BenchmarkParallelPingPongByShmipc512KB(b *testing.B) {
	const payloadSize = 512 << 10
	benchmarkParallelPingPongByShmipc(b, "512KB")
}

func BenchmarkParallelPingPongByShmipc1MB(b *testing.B) {
	const payloadSize = 1 << 20
	benchmarkParallelPingPongByShmipc(b, "1MB")
}

func BenchmarkParallelPingPongByShmipc4MB(b *testing.B) {
	benchmarkParallelPingPongByShmipc(b, "4MB")
}

func mustWrite(s *Stream, bodySize string, b *testing.B) {
	request := pingpong{
		Data: make([]byte, dataSizes[bodySize]),
	}
	reserve, err := s.BufferWriter().Reserve(likelySizes[bodySize])
	if err != nil {
		panic(err)
	}

	buf := bytes.NewBuffer(reserve)
	buf.Reset()
	encoder := json.NewEncoder(buf)
	err = encoder.Encode(request)
	if err != nil {
		panic(err)
	}

	for {
		err := s.Flush(false)
		if err == ErrQueueFull {
			time.Sleep(time.Microsecond)
			continue
		}
		if err != nil {
			panic("must write err:" + err.Error())
		}
		return
	}
}

func mustRead(s *Stream, size int, b *testing.B) bool {
	_, err := s.BufferReader().ReadBytes(size)
	if err == ErrStreamClosed || err == ErrEndOfStream {
		return false
	} else if err != nil {
		panic(fmt.Sprintf("err: %s", err.Error()))
	}

	return true
}

func benchmarkParallelPingPongByShmipc(b *testing.B, bodySize string) {
	client, server := newBenchmarkClientServer(uint32(likelySizes[bodySize]))
	defer func() {
		client.Close()
		server.Close()
	}()

	b.SetBytes(int64(likelySizes[bodySize] + likelySizes[bodySize]))
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		hasNext := pb.Next()
		doneCh := make(chan struct{})
		if hasNext {
			go func() {
				defer close(doneCh)
				stream, err := server.AcceptStream()
				if err != nil {
					b.Fatalf("accept error:%s", err.Error())
					return
				}
				defer stream.Close()
				for {
					if !mustRead(stream, likelySizes[bodySize], b) {
						return
					}
					stream.BufferReader().ReleasePreviousRead()
					mustWrite(stream, bodySize, b)
				}
			}()
			stream, err := client.OpenStream()
			if err != nil {
				b.Fatalf("err: %v", err)
			}
			for ; hasNext; hasNext = pb.Next() {
				mustWrite(stream, bodySize, b)
				mustRead(stream, likelySizes[bodySize], b)
				stream.ReleaseReadAndReuse()
			}
			// make server side stream could receive notification.
			stream.Close()
			<-doneCh
		}
	})
}

func BenchmarkParallelPingPongByUds64B(b *testing.B) {
	benchmarkParallelPingPongByUds(b, "64B")
}

func BenchmarkParallelPingPongByUds512B(b *testing.B) {
	benchmarkParallelPingPongByUds(b, "512B")
}

func BenchmarkParallelPingPongByUds1KB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, "1KB")
}

func BenchmarkParallelPingPongByUds4KB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, "4KB")
}

func BenchmarkParallelPingPongByUds16KB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, "16KB")
}

func BenchmarkParallelPingPongByUds64KB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, "64KB")
}

func BenchmarkParallelPingPongByUds256KB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, "256KB")
}

func BenchmarkParallelPingPongByUds512KB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, "512KB")
}

func BenchmarkParallelPingPongByUds1MB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, "1MB")
}

func BenchmarkParallelPingPongByUds4MB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, "4MB")
}

func udsMustRead(conn net.Conn, buf []byte, expectedSize int) bool {
	start := 0
	for start < expectedSize {
		n, err := conn.Read(buf[start:])
		if err == io.EOF {
			return false
		}
		if err != nil {
			return false
		}
		start += n
	}
	return true
}

func udsMustWrite(conn net.Conn, buf []byte) {
	var wrote int
	for {
		n, err := conn.Write(buf[wrote:])
		if err != nil {
			panic("uds client conn write failed:")
		}
		wrote += n
		if wrote == len(buf) {
			break
		}
	}
}

func benchmarkParallelPingPongByUds(b *testing.B, bodySize string) {
	addr := &net.UnixAddr{Name: fmt.Sprintf("/dev/shm/uds_%d.sock", time.Now().UnixNano()), Net: "unix"}
	defer syscall.Unlink(addr.Name)

	serverStartNotifyCh := make(chan struct{})

	//starting ud server
	go func() {
		ln, err := net.ListenUnix("unix", addr)
		if err != nil {
			panic("create listener failed:" + err.Error())
		}
		time.AfterFunc(time.Millisecond*10, func() {
			close(serverStartNotifyCh)
		})
		defer ln.Close()
		for {
			conn, err := ln.Accept()
			if err != nil {
				panic("accept conn failed:" + err.Error())
			}
			go func(conn net.Conn) {
				defer conn.Close()
				for {
					readBuffer := make([]byte, likelySizes[bodySize])
					writeBuffer := make([]byte, likelySizes[bodySize])
					request := pingpong{
						Data: make([]byte, dataSizes[bodySize]),
					}
					buf := bytes.NewBuffer(writeBuffer)
					buf.Reset()
					encoder := json.NewEncoder(buf)
					err = encoder.Encode(request)
					if err != nil {
						panic(err)
					}

					if !udsMustRead(conn, readBuffer, likelySizes[bodySize]) {
						return
					}
					udsMustWrite(conn, writeBuffer)
				}
			}(conn)
		}
	}()

	b.SetBytes(int64(likelySizes[bodySize] + likelySizes[bodySize]))
	b.ReportAllocs()
	b.ResetTimer()
	<-serverStartNotifyCh
	b.RunParallel(func(pb *testing.PB) {
		clientConn, err := net.DialUnix("unix", nil, addr)
		if err != nil {
			panic("create uds connection failed:" + err.Error())
		}
		defer clientConn.Close()
		for pb.Next() {
			readBuffer := make([]byte, likelySizes[bodySize])
			writeBuffer := make([]byte, likelySizes[bodySize])

			request := pingpong{
				Data: make([]byte, dataSizes[bodySize]),
			}
			buf := bytes.NewBuffer(writeBuffer)
			buf.Reset()
			encoder := json.NewEncoder(buf)
			err = encoder.Encode(request)
			if err != nil {
				panic(err)
			}

			udsMustWrite(clientConn, writeBuffer)
			udsMustRead(clientConn, readBuffer, likelySizes[bodySize])
		}
	})
}

func (l *linkedBuffer) writeEmpty(size int) {
	if size == 0 {
		return
	}
	wrote := 0
	for {
		if l.sliceList.writeSlice == nil {
			l.alloc(uint32(size - wrote))
			l.sliceList.writeSlice = l.sliceList.front()
		}
		wrote += l.sliceList.writeSlice.writeEmpty(size - wrote)
		if wrote < size {
			if l.sliceList.writeSlice.next() == nil {
				l.alloc(uint32(size - wrote))
			}
			l.sliceList.writeSlice = l.sliceList.writeSlice.next()
		} else {
			break
		}
	}
	l.len += size
}

func (s *bufferSlice) writeEmpty(size int) (wrote int) {
	s.writeIndex += size
	if s.writeIndex > len(s.data) {
		wrote = len(s.data) - (s.writeIndex - size)
		s.writeIndex = len(s.data)
		return wrote
	}
	return size
}
