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
	"io"
	"math/rand"
	"net"
	"net/http"
	_ "net/http/pprof"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func init() {
	SetLogLevel(levelNoPrint)
	go func() {
		fmt.Println(http.ListenAndServe("0.0.0.0:29998", nil))
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
		{likelySize + 256, 70},
		{16 << 10, 20},
		{64 << 10, 10},
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
	const payloadSize = 64
	benchmarkParallelPingPongByShmipc(b, payloadSize, payloadSize)
}

func BenchmarkParallelPingPongByShmipc512B(b *testing.B) {
	const payloadSize = 512
	benchmarkParallelPingPongByShmipc(b, payloadSize, payloadSize)
}

func BenchmarkParallelPingPongByShmipc1KB(b *testing.B) {
	const payloadSize = 1024
	benchmarkParallelPingPongByShmipc(b, payloadSize, payloadSize)
}

func BenchmarkParallelPingPongByShmipc4KB(b *testing.B) {
	const payloadSize = 4096
	benchmarkParallelPingPongByShmipc(b, payloadSize, payloadSize)
}

func BenchmarkParallelPingPongByShmipc16KB(b *testing.B) {
	const payloadSize = 16 << 10
	benchmarkParallelPingPongByShmipc(b, payloadSize, payloadSize)
}

func BenchmarkParallelPingPongByShmipc32KB(b *testing.B) {
	const payloadSize = 32 << 10
	benchmarkParallelPingPongByShmipc(b, payloadSize, payloadSize)
}

func BenchmarkParallelPingPongByShmipc64KB(b *testing.B) {
	const payloadSize = 64 << 10
	benchmarkParallelPingPongByShmipc(b, payloadSize, payloadSize)
}

func BenchmarkParallelPingPongByShmipc256KB(b *testing.B) {
	const payloadSize = 256 << 10
	benchmarkParallelPingPongByShmipc(b, payloadSize, payloadSize)
}

func BenchmarkParallelPingPongByShmipc512KB(b *testing.B) {
	const payloadSize = 512 << 10
	benchmarkParallelPingPongByShmipc(b, payloadSize, payloadSize)
}

func BenchmarkParallelPingPongByShmipc1MB(b *testing.B) {
	const payloadSize = 1 << 20
	benchmarkParallelPingPongByShmipc(b, payloadSize, payloadSize)
}

func BenchmarkParallelPingPongByShmipc4MB(b *testing.B) {
	const payloadSize = 4 << 20
	benchmarkParallelPingPongByShmipc(b, payloadSize, payloadSize)
}

func mustWrite(s *Stream, size int, b *testing.B) {
	s.sendBuf.writeEmpty(size)
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
	n, err := s.BufferReader().Discard(size)
	if err == ErrStreamClosed || err == ErrEndOfStream {
		return false
	} else if err != nil {
		panic(fmt.Sprintf("err: %s", err.Error()))
	}
	if err != nil {
		panic(fmt.Sprintf("should discard :%d  but real discard:%d err:%s", size, n, err.Error()))
	}
	return true
}

func benchmarkParallelPingPongByShmipc(b *testing.B, reqSize, respSize int) {
	//SetLogLevel(levelTrace)
	client, server := newBenchmarkClientServer(uint32(reqSize))
	defer func() {
		client.Close()
		server.Close()
	}()

	b.SetBytes(int64(reqSize + respSize))
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
					if !mustRead(stream, reqSize, b) {
						return
					}
					stream.BufferReader().ReleasePreviousRead()
					mustWrite(stream, respSize, b)
				}
			}()
			stream, err := client.OpenStream()
			if err != nil {
				b.Fatalf("err: %v", err)
			}
			for ; hasNext; hasNext = pb.Next() {
				//wg.Add(1)
				//fmt.Println("send")
				mustWrite(stream, reqSize, b)
				mustRead(stream, respSize, b)
				stream.ReleaseReadAndReuse()
			}
			// make server side stream could receive notification.
			stream.Close()
			<-doneCh
		}
	})
}

func BenchmarkParallelPingPongByUds64B(b *testing.B) {
	benchmarkParallelPingPongByUds(b, 64, 64)
}

func BenchmarkParallelPingPongByUds512B(b *testing.B) {
	benchmarkParallelPingPongByUds(b, 512, 512)
}

func BenchmarkParallelPingPongByUds1KB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, 1<<10, 1<<10)
}

func BenchmarkParallelPingPongByUds4KB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, 4<<10, 4<<10)
}

func BenchmarkParallelPingPongByUds16KB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, 16<<10, 16<<10)
}

func BenchmarkParallelPingPongByUds64KB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, 64<<10, 64<<10)
}

func BenchmarkParallelPingPongByUds256KB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, 256<<10, 256<<10)
}

func BenchmarkParallelPingPongByUds512KB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, 512<<10, 512<<10)
}

func BenchmarkParallelPingPongByUds1MB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, 1<<20, 1<<20)
}

func BenchmarkParallelPingPongByUds4MB(b *testing.B) {
	benchmarkParallelPingPongByUds(b, 4<<20, 4<<20)
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

func benchmarkParallelPingPongByUds(b *testing.B, reqSize, respSize int) {
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
			readBuffer := make([]byte, reqSize)
			writeBuffer := make([]byte, respSize)
			go func(conn net.Conn) {
				defer conn.Close()
				for {
					if !udsMustRead(conn, readBuffer, reqSize) {
						return
					}
					udsMustWrite(conn, writeBuffer)
				}
			}(conn)
		}
	}()

	b.SetBytes(int64(reqSize + respSize))
	b.ReportAllocs()
	b.ResetTimer()
	<-serverStartNotifyCh
	b.RunParallel(func(pb *testing.PB) {
		clientConn, err := net.DialUnix("unix", nil, addr)
		if err != nil {
			panic("create uds connection failed:" + err.Error())
		}
		defer clientConn.Close()
		readBuffer := make([]byte, respSize)
		writeBuffer := make([]byte, reqSize)
		for pb.Next() {
			udsMustWrite(clientConn, writeBuffer)
			udsMustRead(clientConn, readBuffer, respSize)
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
