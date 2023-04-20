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
	"os"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func testConn() (*net.UnixConn, *net.UnixConn) {
	return testUdsConn()
}

func testUdsConn() (client *net.UnixConn, server *net.UnixConn) {
	udsPath := "shmipc_unit_test" + strconv.Itoa(int(rand.Int63())) + "_" + strconv.Itoa(time.Now().Nanosecond())
	_ = syscall.Unlink(udsPath)
	addr := &net.UnixAddr{udsPath, "unix"}
	notifyCh := make(chan struct{})
	go func() {
		defer func() {
			_ = syscall.Unlink(udsPath)
		}()
		ln, err := net.ListenUnix("unix", addr)
		if err != nil {
			panic("create listener failed:" + err.Error())
		}
		notifyCh <- struct{}{}
		server, err = ln.AcceptUnix()
		if err != nil {
			panic("accept conn failed:" + err.Error())
		}
		ln.Close()
		notifyCh <- struct{}{}
	}()
	<-notifyCh
	var err error
	client, err = net.DialUnix("unix", nil, addr)
	if err != nil {
		panic("dial uds failed:" + err.Error())
	}
	<-notifyCh
	return
}

func testConf() *Config {
	conf := DefaultConfig()
	conf.MemMapType = MemMapTypeMemFd
	conf.ConnectionWriteTimeout = 25000 * time.Millisecond
	conf.ShareMemoryPathPrefix = "/Volumes/RAMDisk/shmipc.test"
	if runtime.GOOS == "linux" {
		conf.ShareMemoryPathPrefix = "/dev/shm/shmipc.test_" + strconv.Itoa(int(rand.Int63()))
	}
	conf.ShareMemoryBufferCap = 32 * 1024 * 1024 // 32M
	conf.LogOutput = os.Stdout
	return conf
}

func testClientServer() (*Session, *Session) {
	return testClientServerConfig(testConf())
}

func testClientServerConfig(conf *Config) (*Session, *Session) {
	clientConn, serverConn := testConn()
	var server *Session
	ok := make(chan struct{})

	go func() {
		var sErr error
		serverConf := *conf
		server, sErr = newSession(&serverConf, serverConn, false)
		if sErr != nil {
			panic(sErr)
		}
		close(ok)
	}()
	clientConf := *conf
	client, cErr := newSession(&clientConf, clientConn, true)
	if cErr != nil {
		panic(cErr)
	}
	<-ok
	return client, server
}

func TestSession_OpenStream(t *testing.T) {
	fmt.Println("----------test session open stream----------")
	client, server := testClientServer()

	// case1: session closed
	client.Close()
	server.Close()
	assert.Equal(t, true, client.IsClosed())
	stream, err := client.OpenStream()
	assert.Equal(t, (*Stream)(nil), stream)
	assert.Equal(t, ErrSessionShutdown, err)
	// client's Close will cause server's Close
	assert.Equal(t, true, server.IsClosed())

	//case2: CircuitBreaker triggered
	client2, server2 := testClientServer()
	client2.openCircuitBreaker()
	stream2, err := client2.OpenStream()
	assert.Equal(t, (*Stream)(nil), stream2)
	assert.Equal(t, ErrSessionUnhealthy, err)
	client2.Close()
	server2.Close()

	// case3: stream exist
	client3, server3 := testClientServer()
	client3.streams[client3.nextStreamID+1] = newStream(client3, client3.nextStreamID+1)
	stream3, err := client3.OpenStream()
	assert.Equal(t, (*Stream)(nil), stream3)
	assert.Equal(t, ErrStreamsExhausted, err)
	client3.Close()
	server3.Close()
}

func TestSession_AcceptStreamNormally(t *testing.T) {
	fmt.Println("----------test session accept stream normally----------")
	done := make(chan struct{})
	notifyRead := make(chan struct{})
	client, server := testClientServer()
	defer client.Close()
	defer server.Close()
	// case1: accept stream normally
	go func() {
		cStream, err := client.OpenStream()
		defer cStream.Close()
		if err != nil {
			t.Fatalf("Failed to open stream:%s", err.Error())
		}
		// only when we actually send something, the server can
		// aware that a new stream created, therefore we need to
		// send a byte to notify server
		_ = cStream.BufferWriter().WriteString("1")
		cStream.Flush(true)
		// wait resp
		<-notifyRead

		respData, err := cStream.BufferReader().ReadBytes(1)
		if err != nil {
			t.Fatalf("Failed to read bytes:%s", err.Error())
		}
		assert.Equal(t, "1", string(respData))
		close(done)
	}()

	sStream, err := server.AcceptStream()
	if err != nil {
		t.Fatalf("Failed to accept stream:%s", err.Error())
	}
	defer sStream.Close()
	respData, err := sStream.BufferReader().ReadBytes(1)
	if err != nil {
		t.Fatalf("Failed to read bytes:%s", err.Error())
	}
	assert.Equal(t, "1", string(respData))

	// write back
	_ = sStream.BufferWriter().WriteString("1")
	sStream.Flush(true)
	close(notifyRead)
	<-done
	fmt.Println("----------test session accept stream normally done----------")
}

func TestSession_AcceptStreamWhenSessionClosed(t *testing.T) {
	fmt.Println("----------test session accept stream when session closed----------")
	client, server := testClientServer()
	defer client.Close()
	defer server.Close()
	done := make(chan struct{})
	notifyAccept := make(chan struct{})
	// case 2: accept when session closed
	go func() {
		<-notifyAccept
		// try accept after session shutdown
		_, err := server.AcceptStream()
		assert.Error(t, ErrSessionShutdown, err)
		close(done)
	}()

	cStream2, err := client.OpenStream()
	defer cStream2.Close() //lint:ignore SA5001

	if err != nil {
		t.Fatalf("Failed to malloc buf:%s", err.Error())
	}
	_ = cStream2.BufferWriter().WriteString("1")
	cStream2.Flush(true)

	// now shutdown session
	client.Close()
	server.Close()
	assert.Equal(t, true, server.shutdown == 1)
	close(notifyAccept)
	<-done
	fmt.Println("----------test session accept stream when session closed done----------")
}

func TestSendData_Small(t *testing.T) {
	client, server := testClientServer()
	defer client.Close()
	defer server.Close()
	wg := &sync.WaitGroup{}
	wg.Add(2)

	go func() {
		defer wg.Done()
		stream, err := server.AcceptStream()
		if err != nil {
			t.Logf("err: %v", err)
		}

		if server.GetActiveStreamCount() != 1 {
			t.Fatal("num of streams is ", server.GetActiveStreamCount())
		}

		size := 0
		for size < 4*100 {
			bs, err := stream.BufferReader().ReadBytes(4)
			size += 4
			if err != nil {
				t.Fatalf("read err: %v", err)
			}
			if string(bs) != "test" {
				t.Logf("bad: %s", string(bs))
			}
		}

		if err := stream.Close(); err != nil {
			t.Logf("err: %v", err)
		}
	}()

	go func() {
		defer wg.Done()
		stream, err := client.OpenStream()
		if err != nil {
			t.Logf("err: %v", err)
		}

		if client.GetActiveStreamCount() != 1 {
			t.Logf("bad")
		}

		for i := 0; i < 100; i++ {
			_, err := stream.BufferWriter().WriteBytes([]byte("test"))
			if err != nil {
				t.Logf("err: %v", err)
			}
			err = stream.Flush(false)
			if err != nil {
				t.Logf("err: %v", err)
			}
		}

		if err := stream.Close(); err != nil {
			t.Logf("err: %v", err)
		}
	}()

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(time.Second * 35):
		panic("timeout")
	}

	if client.GetActiveStreamCount() != 0 {
		t.Fatalf("bad, streams:%d", client.GetActiveStreamCount())
	}
	if server.GetActiveStreamCount() != 0 {
		t.Fatalf("bad")
	}
}

func TestSendData_Large(t *testing.T) {
	client, server := testClientServer()
	defer client.Close()
	defer server.Close()

	const (
		sendSize = 2 * 1024 * 1024
		recvSize = 4 * 1024
	)

	data := make([]byte, recvSize)
	for idx := range data {
		data[idx] = byte(idx % 256)
	}

	wg := &sync.WaitGroup{}
	wg.Add(2)

	go func() {
		defer wg.Done()
		stream, err := server.AcceptStream()
		if err != nil {
			t.Logf("err: %v", err)
		}
		defer stream.Close()
		for hadRead := 0; hadRead < sendSize; hadRead++ {
			if bt, err := stream.BufferReader().ReadByte(); bt != byte(hadRead%256) || err != nil {
				t.Logf("bad: %v %v", hadRead, bt)
			}

		}
	}()

	go func() {
		defer wg.Done()
		stream, err := client.OpenStream()
		if err != nil {
			t.Logf("err: %v", err)
		}

		for i := 0; i < sendSize/recvSize; i++ {
			_, _ = stream.BufferWriter().WriteBytes(data)
			err = stream.Flush(true)
			if err != nil {
				t.Logf("err: %v", err)
			}
		}

		if err := stream.Close(); err != nil {
			t.Logf("err: %v", err)
		}
	}()

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		panic("timeout")
	}
}

func TestManyStreams(t *testing.T) {
	client, server := testClientServer()
	defer server.Close()
	defer client.Close()

	wg := &sync.WaitGroup{}
	const writeSize = 8
	acceptor := func(i int) {
		defer wg.Done()
		stream, err := server.AcceptStream()
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		defer stream.Close()

		for {
			stream.SetReadDeadline(time.Now().Add(1 * time.Second))
			buf := stream.BufferReader()
			n, err := buf.Discard(writeSize)
			if err == ErrEndOfStream {
				return
			}
			if err == io.EOF || err == ErrTimeout {
				// t.Logf("ret err:%s", err)
				return
			}
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if buf.Len() != 0 {
				t.Fatalf("err!0: %d n:%d", buf.Len(), n)
			}
		}
	}
	sender := func(i int) {
		defer wg.Done()
		stream, err := client.OpenStream()
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		defer stream.Close()

		var msg [writeSize]byte
		if _, err := stream.BufferWriter().WriteBytes(msg[:]); err != nil {
			t.Fatal("err:", err.Error())
		}
		err = stream.Flush(true)
		if err != nil {
			t.Fatalf("err: %v", err)
		} else {
			//fmt.Println("write len:", n)
		}
	}

	for i := 0; i < 1; i++ {
		wg.Add(2)
		go acceptor(i)
		go sender(i)
	}

	wg.Wait()
}

func TestSendData_Small_Memfd(t *testing.T) {
	conf := testConf()
	conf.MemMapType = MemMapTypeMemFd
	client, server := testClientServer()

	defer client.Close()
	defer server.Close()
	wg := &sync.WaitGroup{}
	wg.Add(2)

	go func() {
		defer wg.Done()
		stream, err := server.AcceptStream()
		if err != nil {
			t.Logf("err: %v", err)
		}

		if server.GetActiveStreamCount() != 1 {
			t.Fatal("num of streams is ", server.GetActiveStreamCount())
		}

		size := 0
		for size < 4*100 {
			bs, err := stream.BufferReader().ReadBytes(4)
			size += 4
			if err != nil {
				t.Fatalf("read err: %v", err)
			}
			if string(bs) != "test" {
				t.Logf("bad: %s", string(bs))
			}
		}

		if err := stream.Close(); err != nil {
			t.Logf("err: %v", err)
		}
	}()

	go func() {
		defer wg.Done()
		stream, err := client.OpenStream()
		if err != nil {
			t.Logf("err: %v", err)
		}

		if client.GetActiveStreamCount() != 1 {
			t.Logf("bad")
		}

		for i := 0; i < 100; i++ {
			_, err := stream.BufferWriter().WriteBytes([]byte("test"))
			if err != nil {
				t.Logf("err: %v", err)
			}
			err = stream.Flush(false)
			if err != nil {
				t.Logf("err: %v", err)
			}
		}

		if err := stream.Close(); err != nil {
			t.Logf("err: %v", err)
		}
	}()

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(time.Second * 35):
		panic("timeout")
	}

	if client.GetActiveStreamCount() != 0 {
		t.Fatalf("bad, streams:%d", client.GetActiveStreamCount())
	}
	if server.GetActiveStreamCount() != 0 {
		t.Fatalf("bad")
	}
}
