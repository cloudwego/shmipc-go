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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var (
	hotRestartUDSPath = "/tmp/hot_restart_test.sock"

	firstMsg  = "hello start"
	secondMsg = "hello restart"

	oldListenerExit chan struct{}
	firstMsgDone    chan struct{}
	secondMsgDone   chan struct{}

	_ ListenCallback  = &listenCbImpl{}
	_ StreamCallbacks = &streamCbImpl{}
)

type listenCbImpl struct{}

func (l listenCbImpl) OnNewStream(s *Stream) {
	if err := s.SetCallbacks(streamCbImpl{stream: s}); err != nil {
		fmt.Printf("OnNewStream SetCallbacks error %+v\n", err)
	}
}

func (l listenCbImpl) OnShutdown(reason string) {
}

type streamCbImpl struct {
	stream *Stream
}

func (s streamCbImpl) OnData(reader BufferReader) {
	_, _ = reader.Peek(1)
	ret, err := reader.ReadString(reader.Len())
	if err != nil {
		fmt.Println("streamCbImpl OnData err ", err)
		return
	}

	if ret == firstMsg {
		fmt.Println("old server recv msg")
		s.stream.BufferWriter().WriteString(ret)
		s.stream.Flush(false)
		s.stream.ReleaseReadAndReuse()

		firstMsgDone <- struct{}{}
	}

	if ret == secondMsg {
		fmt.Println("new server recv msg")
		s.stream.BufferWriter().WriteString(ret)
		s.stream.Flush(false)
		s.stream.ReleaseReadAndReuse()

		secondMsgDone <- struct{}{}
	}
}

func (s streamCbImpl) OnLocalClose() {
	fmt.Println("streamCbImpl OnLocalClose")
}

func (s streamCbImpl) OnRemoteClose() {
	fmt.Println("streamCbImpl OnRemoteClose")
}

func getSessionManagerConfig() *SessionManagerConfig {
	conf := DefaultSessionManagerConfig()
	conf.Address = hotRestartUDSPath
	conf.Network = "unix"
	conf.SessionNum = 4
	conf.MemMapType = MemMapTypeMemFd

	return conf
}

func getListenerConfig() *ListenerConfig {
	return NewDefaultListenerConfig(hotRestartUDSPath, "unix")
}

func genListenerByConfig(config *ListenerConfig) *Listener {
	listener, err := NewListener(&listenCbImpl{}, config)
	if err != nil {
		fmt.Println("NewListener error ", err)
		return nil
	}
	return listener
}

// mock hot restart, more details in the directory example/hot_restart_test
// step1. start one server and one client
// step2. client send the message `hello start`
// step3. after server receive message, which will start a new server and do hot restart.
// step4. client send the message `hello restart`
// step5. the new server will receive the message `hello restart`
func TestHotRestart(t *testing.T) {
	firstMsgDone = make(chan struct{})
	secondMsgDone = make(chan struct{})
	oldListenerExit = make(chan struct{})

	oldListener := genListenerByConfig(getListenerConfig())
	assert.NotNil(t, oldListener)
	oldListener.SetUnlinkOnClose(false)
	go func() {
		runErr := oldListener.Run()
		fmt.Println("oldListener run exit ", runErr)
		assert.Nil(t, runErr)
		oldListenerExit <- struct{}{}
	}()

	sessionManager, err := InitGlobalSessionManager(getSessionManagerConfig())
	assert.NotNil(t, sessionManager)
	assert.Nil(t, err)

	if sessionManager == nil || oldListener == nil {
		fmt.Println("create listener or session manager error")
		return
	}

	// send first message
	stream, err := sessionManager.GetStream()
	assert.NotNil(t, stream)
	assert.Nil(t, err)
	err = stream.BufferWriter().WriteString(firstMsg)
	assert.Nil(t, err)
	err = stream.Flush(false)
	assert.Nil(t, err)
	ret, err := stream.BufferReader().ReadString(len(firstMsg))
	assert.Nil(t, err)
	assert.Equal(t, ret, firstMsg)
	sessionManager.PutBack(stream)

	<-firstMsgDone
	fmt.Println("begin hot restart")
	// after handle first message, and then do hot restart
	newListener := genListenerByConfig(getListenerConfig())
	assert.NotNil(t, newListener)
	newListener.SetUnlinkOnClose(false)
	go func() {
		runErr := newListener.Run()
		fmt.Println("newListener run exit ", runErr)
		assert.Nil(t, runErr)
	}()
	err = oldListener.HotRestart(1024)
	assert.Nil(t, err)

	// wait old server reply
	for !oldListener.IsHotRestartDone() {
		time.Sleep(time.Millisecond * 100)
	}
	err = oldListener.Close()
	assert.Nil(t, err)

	<-oldListenerExit
	fmt.Println("hot restart done, old server exit")
	stream, err = sessionManager.GetStream()
	assert.NotNil(t, stream)
	assert.Nil(t, err)
	err = stream.BufferWriter().WriteString(secondMsg)
	assert.Nil(t, err)
	err = stream.Flush(false)
	assert.Nil(t, err)
	ret, err = stream.BufferReader().ReadString(len(secondMsg))
	assert.Nil(t, err)
	assert.Equal(t, ret, secondMsg)
	sessionManager.PutBack(stream)

	<-secondMsgDone
	err = newListener.Close()
	assert.Nil(t, err)
	time.Sleep(1 * time.Second)
}
