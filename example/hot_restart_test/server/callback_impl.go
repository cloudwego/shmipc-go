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

package main

import (
	"fmt"
	"sync/atomic"

	"github.com/cloudwego/shmipc-go"
)

var (
	_ shmipc.ListenCallback  = &listenCbImpl{}
	_ shmipc.StreamCallbacks = &streamCbImpl{}
)

type listenCbImpl struct{}

func (l listenCbImpl) OnNewStream(s *shmipc.Stream) {
	//fmt.Printf("OnNewStream StreamID %d\n", s.StreamID())
	if err := s.SetCallbacks(streamCbImpl{stream: s}); err != nil {
		fmt.Printf("OnNewStream SetCallbacks error %+v\n", err)
	}
}

func (l listenCbImpl) OnShutdown(reason string) {
	//fmt.Printf("OnShutdown reason %s\n", reason)
}

type streamCbImpl struct {
	stream *shmipc.Stream
}

func (s streamCbImpl) OnData(reader shmipc.BufferReader) {
	ret, err := reader.ReadString(len(sendStr))
	if err != nil {
		fmt.Printf("OnData stream ReadString request error %+v\n", err)
		atomic.AddUint64(&errCount, 1)
		return
	}

	if ret != sendStr {
		fmt.Println("OnData ret ", ret)
	}

	atomic.AddUint64(&count, 1)

	err = s.stream.BufferWriter().WriteString(sendStr)
	if err != nil {
		fmt.Printf("OnData stream WriteString response failed error %+v\n", err)
		return
	}
	err = s.stream.Flush(false)
	if err != nil {
		fmt.Printf("OnData stream Flush response failed error %+v\n", err)
		return
	}

	s.stream.ReleaseReadAndReuse()
}

func (s streamCbImpl) OnLocalClose() {
	fmt.Printf("StreamCallbacks OnLocalClose StreamID %+v\n", s.stream.StreamID())
}

func (s streamCbImpl) OnRemoteClose() {
	fmt.Printf("StreamCallbacks OnRemoteClose StreamID %+v\n", s.stream.StreamID())
}
