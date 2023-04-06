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
	"math/rand"
	"net"
	_ "net/http/pprof"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func testSessionMgrConf() *SessionManagerConfig {
	return &SessionManagerConfig{
		Config:            DefaultConfig(),
		Address:           "/tmp/ipc_sm.sock",
		Network:           "unix",
		SessionNum:        10,
		MaxStreamNum:      5,
		StreamMaxIdleTime: 10 * time.Second,
	}
}

func newClientServerByNewClientSession(*SessionManagerConfig) (*Session, *Session) {
	done := make(chan struct{})
	serverDone := make(chan struct{})
	conf := testSessionMgrConf()
	conf.MemMapType = MemMapTypeMemFd
	var client, server *Session
	syscall.Unlink(conf.Address)

	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: conf.Address, Net: "unix"})
	if err != nil {
		panic("Listen uds failed, " + err.Error())
	}
	defer ln.Close()

	go func() {
		close(serverDone)
		conn, err := ln.Accept()
		if err != nil {
			panic(err)
		}
		server, err = Server(conn, conf.Config)
		if err != nil {
			panic(err)
		}
		// ensure mmap done
		for !server.handshakeDone { //lint:ignore SA5002
			time.Sleep(time.Millisecond * 100)
		}
		close(done)
	}()
	<-serverDone
	client, err = newClientSession(1, 0, 0, conf)
	if err != nil {
		panic(err)
	}
	<-done

	return client, server
}

func TestStreamPool_Put(t *testing.T) {
	client, server := newClientServerByNewClientSession(testSessionMgrConf())
	defer client.Close()
	defer server.Close()
	sp := newStreamPool(1)
	sp.session.Store(client)

	stream, err := client.OpenStream()

	if err != nil {
		t.Fatalf("open stream failed:%s", err.Error())
	}
	defer stream.Close()

	stream2, err := client.OpenStream()
	if err != nil {
		t.Fatalf("open stream failed:%s", err.Error())
	}
	defer stream2.Close()

	id := stream.id
	sp.putOrCloseStream(stream)
	assert.Equal(t, uint32(streamOpened), stream.state)
	// ring full close the second one
	sp.putOrCloseStream(stream2)
	assert.Equal(t, uint32(streamClosed), stream2.state)
	// get the previous one
	stream, _ = sp.getOrOpenStream()
	assert.Equal(t, id, stream.id)
	// add some bytes to recvbuf making reset failure
	stream.recvBuf = newEmptyLinkedBuffer(stream.session.bufferManager)
	_ = stream.recvBuf.WriteString("test")
	sp.putOrCloseStream(stream)
	// reset fail, make it closed
	assert.Equal(t, uint32(streamClosed), stream.state)
	// now the stream was closed, try put it again
	sp.putOrCloseStream(stream)
	// closed stream will not put into streamPool
	assert.Equal(t, sp.pop() == nil, true)
}

func TestStreamPool_Get(t *testing.T) {
	client, server := newClientServerByNewClientSession(testSessionMgrConf())
	defer client.Close()
	defer server.Close()
	sp := newStreamPool(2)
	sp.session.Store(client)

	stream1, _ := client.OpenStream()
	stream2, _ := client.OpenStream()
	assert.Equal(t, uint32(streamOpened), atomic.LoadUint32(&stream1.state))
	assert.Equal(t, uint32(streamOpened), atomic.LoadUint32(&stream2.state))
	// record id
	id1 := stream1.id
	id2 := stream2.id

	// test normal put and get
	sp.putOrCloseStream(stream1)
	sp.putOrCloseStream(stream2)
	stream1, _ = sp.getOrOpenStream()
	stream2, _ = sp.getOrOpenStream()
	assert.Equal(t, id1, stream1.id)
	assert.Equal(t, id2, stream2.id)

	// test put and get, when a stream is closed
	stream1.Close()
	sp.putOrCloseStream(stream1)
	sp.putOrCloseStream(stream2)
	stream1, _ = sp.getOrOpenStream()
	stream2, _ = sp.getOrOpenStream()
	assert.NotEqual(t, id1, stream1.id)
	assert.NotEqual(t, id2, stream2.id)
	assert.Equal(t, id2, stream1.id)

	// test get, if a stream is closed after it was put into streamPool
	id1 = stream1.id
	id2 = stream2.id
	sp.putOrCloseStream(stream1)
	sp.putOrCloseStream(stream2)
	stream2.Close()
	stream1, _ = sp.getOrOpenStream()
	stream2, _ = sp.getOrOpenStream()
	assert.Equal(t, id1, stream1.id)
	assert.NotEqual(t, id2, stream2.id)

	// test get, if a session is unhealthy
	sp.putOrCloseStream(stream1)
	client.openCircuitBreaker()
	stream, err := sp.getOrOpenStream()
	assert.Equal(t, (*Stream)(nil), stream)
	assert.Equal(t, ErrSessionUnhealthy, err)
}

func TestStreamPool_Close(t *testing.T) {
	client, server := newClientServerByNewClientSession(testSessionMgrConf())
	defer client.Close()
	defer server.Close()
	sp := newStreamPool(2)
	assert.Equal(t, (*Session)(nil), sp.Session())
	sp.session.Store(client)
	assert.Equal(t, client, sp.Session())

	stream1, _ := client.OpenStream()
	stream2, _ := client.OpenStream()
	sp.putOrCloseStream(stream1)
	sp.putOrCloseStream(stream2)
	sp.close()
	assert.Equal(t, uint32(streamClosed), stream1.state)
	assert.Equal(t, uint32(streamClosed), stream2.state)
}

func TestSM_NewClientSession(t *testing.T) {
	conf := testSessionMgrConf()
	done := make(chan struct{})
	mockDataLen := 10
	mockData := make([]byte, mockDataLen)
	rand.Read(mockData)
	client, server := newClientServerByNewClientSession(conf)
	defer client.Close()
	defer server.Close()

	go func() {
		stream, err := server.AcceptStream()
		if err != nil {
			panic("accept stream failed " + err.Error())
		}
		defer stream.Close()
		buf := stream.BufferReader()
		reqData, err := buf.ReadBytes(mockDataLen)
		if err != nil {
			panic("readBuf failed" + err.Error())
		}
		assert.Equal(t, mockData, reqData)
		close(done)
	}()

	stream, err := client.OpenStream()
	if err != nil {
		t.Fatalf("client open stream failed:%s", err.Error())
	}
	defer stream.Close()

	_, err = stream.BufferWriter().WriteBytes(mockData)
	if err != nil {
		t.Fatalf("buffer writeString failed:%s", err.Error())
	}

	err = stream.Flush(true)
	if err != nil {
		t.Fatalf("stream Flush failed:%s", err.Error())
	}
	<-done
}

func TestSM_Background(t *testing.T) {
	fmt.Println("----------test session manager background----------")
	config := testSessionMgrConf()
	config.Config.rebuildInterval = time.Second * 10
	config.SessionNum = 1
	notifyConn := make(chan struct{})
	notifyClose := make(chan struct{})
	done := make(chan struct{})
	wg := &sync.WaitGroup{}
	wg.Add(2)

	syscall.Unlink(config.Address)

	go func() {
		ln, _ := net.ListenUnix("unix", &net.UnixAddr{Name: config.Address, Net: "unix"})
		defer ln.Close()
		servers := make([]*Session, 2)

		defer func() {
			for _, s := range servers {
				if s == nil {
					continue
				}
				s.Close()
			}
		}()
		close(notifyConn)
		for i := 0; i < 2; i++ {
			conn, _ := ln.Accept()
			server, err := Server(conn, config.Config)
			if err != nil {
				t.Fatalf("Server error:%s", err.Error())
			}
			// ensure mmap done
			for !server.handshakeDone { //lint:ignore SA5002
				time.Sleep(100 * time.Millisecond)
			}
			servers[i] = server
			wg.Done()
			if i == 0 {
				close(notifyClose)
			}
		}
		<-done
	}()

	<-notifyConn
	sm, err := NewSessionManager(config)
	if err != nil {
		t.Fatalf("Create session manager failed:%s", err.Error())
	}
	<-notifyClose
	// now sm has 1 session, try to close it
	s1 := sm.pools[0].Session()
	s1.Close()
	assert.Equal(t, true, atomic.LoadUint32(&s1.shutdown) == 1)
	// wait until all pre init done
	fmt.Printf("wait init %v\n", config.Config.rebuildInterval)
	wg.Wait()
	// wait for session reconnect
	fmt.Println("init done")
	// + 1*time.Second ensure now session has been restarted
	time.Sleep(config.Config.rebuildInterval + 1*time.Second)
	// now session has been restarted
	s1 = sm.pools[0].Session()
	assert.Equal(t, false, s1.shutdown == 1)

	close(done)
	sm.Close()
}

func TestSM_GlobalCreation(t *testing.T) {
	fmt.Println("----------test session manager global creation----------")
	config := testSessionMgrConf()
	// we need not create really connection here
	// this work have been done in background test
	config.SessionNum = 0
	gsm, _ := InitGlobalSessionManager(config)
	gsm2 := GlobalSessionManager()
	assert.Equal(t, gsm, gsm2)
	GlobalSessionManager().Close()
	//ensure share memory was clean
	time.Sleep(2 * time.Second)
}

func TestSM_GetAndPutStream(t *testing.T) {
	fmt.Println("----------test session get and put stream----------")
	config := testSessionMgrConf()
	notifyConn := make(chan struct{})
	done := make(chan struct{})
	sessionPairNum := 1
	config.SessionNum = sessionPairNum
	wg := &sync.WaitGroup{}
	wg.Add(sessionPairNum)

	syscall.Unlink(config.Address)

	go func() {
		ln, _ := net.ListenUnix("unix", &net.UnixAddr{Name: config.Address, Net: "unix"})
		servers := make([]*Session, sessionPairNum)
		defer ln.Close()

		defer func() {
			for _, s := range servers {
				s.Close()
			}
		}()

		close(notifyConn)
		for i := 0; i < sessionPairNum; i++ {
			conn, _ := ln.Accept()
			server, err := Server(conn, config.Config)
			if err != nil {
				t.Fatalf("Server error:%s", err.Error())
			}
			// ensure mmap done
			for !server.handshakeDone { //lint:ignore SA5002
				time.Sleep(time.Millisecond * 100)
			}
			servers[i] = server
			wg.Done()
		}
		<-done
	}()

	<-notifyConn
	sm, err := NewSessionManager(config)
	if err != nil {
		t.Fatalf("Create session manager failed:%s", err.Error())
	}
	// wait until all pre init done
	wg.Wait()

	s, err := sm.GetStream()
	if err != nil {
		t.Fatalf("Create session manager failed:%s", err.Error())
	}
	assert.Equal(t, uint32(2), s.id)
	s2, err := sm.GetStream()
	if err != nil {
		t.Fatalf("Create session manager failed:%s", err.Error())
	}
	assert.Equal(t, uint32(3), s2.id)
	// put them back
	sm.PutBack(s)
	sm.PutBack(s2)
	// get again
	s3, err := sm.GetStream()
	s4, err := sm.GetStream()
	assert.Equal(t, s, s3)
	assert.Equal(t, s2, s4)

	sm.Close()
	close(done)
}

func TestStreamPool_PutAndPopWithConcurrently(t *testing.T) {
	pool := newStreamPool(4096)
	expectedStreamN := uint32(0)
	var wg sync.WaitGroup
	concurrency := 1000
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			for n := 0; n < 10000; n++ {
				s := pool.pop()
				if s == nil {
					s = &Stream{pool: pool, id: atomic.AddUint32(&expectedStreamN, 1)}
				}
				runtime.Gosched()
				assert.Equal(t, nil, pool.push(s))
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, pool.tail-pool.head, uint64(expectedStreamN))

	verify := make(map[uint32]bool)
	for s := pool.pop(); s != nil; s = s.pool.pop() {
		verify[s.id] = true
	}
	assert.Equal(t, expectedStreamN, uint32(len(verify)))
}
