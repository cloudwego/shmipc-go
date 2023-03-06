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
	"github.com/cloudwego/shmipc"
	"github.com/cloudwego/shmipc/example/best_practice/idl"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	count uint64
	_     shmipc.StreamCallbacks = &streamCbImpl{}
	_     shmipc.ListenCallback  = &listenCbImpl{}
)

type listenCbImpl struct{}

func (l *listenCbImpl) OnNewStream(s *shmipc.Stream) {
	s.SetCallbacks(&streamCbImpl{stream: s})
}

func (l *listenCbImpl) OnShutdown(reason string) {
	fmt.Println("OnShutdown reason:" + reason)
}

type streamCbImpl struct {
	req    idl.Request
	resp   idl.Response
	stream *shmipc.Stream
}

func (s *streamCbImpl) OnData(reader shmipc.BufferReader) {
	//1.deserialize Request
	if err := s.req.ReadFromShm(reader); err != nil {
		fmt.Println("stream read request, err=" + err.Error())
		return
	}

	{
		//2.handle request
		atomic.AddUint64(&count, 1)
	}

	//3.serialize Response
	s.resp.ID = s.req.ID
	s.resp.Name = s.req.Name
	s.resp.Image = s.req.Key
	if err := s.resp.WriteToShm(s.stream.BufferWriter()); err != nil {
		fmt.Println("stream write response failed, err=" + err.Error())
		return
	}
	if err := s.stream.Flush(false); err != nil {
		fmt.Println("stream write response failed, err=" + err.Error())
		return
	}
	s.stream.ReleaseReadAndReuse()
	s.req.Reset()
	s.resp.Reset()
}

func (s *streamCbImpl) OnLocalClose() {
	//fmt.Println("stream OnLocalClose")
}

func (s *streamCbImpl) OnRemoteClose() {
	//fmt.Println("stream OnRemoteClose")
}

func init() {
	go func() {
		lastCount := count
		for range time.Tick(time.Second) {
			curCount := atomic.LoadUint64(&count)
			fmt.Println("shmipc_async_server qps:", curCount-lastCount)
			lastCount = curCount
		}
	}()
	runtime.GOMAXPROCS(1)

	go func() {
		http.ListenAndServe(":20000", nil)
	}()
}

func main() {
	// 1. listen unix domain socket
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	udsPath := filepath.Join(dir, "../ipc_test.sock")

	syscall.Unlink(udsPath)
	config := shmipc.NewDefaultListenerConfig(udsPath, "unix")
	ln, err := shmipc.NewListener(&listenCbImpl{}, config)
	if err != nil {
		fmt.Println(err)
		return
	}
	ln.Run()
}
