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
	"math"
	"math/rand"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/cloudwego/shmipc"
	"github.com/cloudwego/shmipc/example/best_practice/idl"
)

var (
	count uint64
	_     shmipc.StreamCallbacks = &streamCbImpl{}
)

func init() {
	go func() {
		lastCount := count
		for range time.Tick(time.Second) {
			curCount := atomic.LoadUint64(&count)
			fmt.Println("shmipc_async_client qps:", curCount-lastCount)
			lastCount = curCount
		}
	}()
	runtime.GOMAXPROCS(1)

	go func() {
		http.ListenAndServe(":20001", nil)
	}()
}

type streamCbImpl struct {
	req    idl.Request
	resp   idl.Response
	stream *shmipc.Stream
	smgr   *shmipc.SessionManager
	key    []byte
	loop   uint64
	n      uint64
}

func (s *streamCbImpl) OnData(reader shmipc.BufferReader) {
	//wait and read response
	s.resp.Reset()
	if err := s.resp.ReadFromShm(reader); err != nil {
		fmt.Println("write request to share memory failed, err=" + err.Error())
		return
	}
	s.stream.ReleaseReadAndReuse()

	{
		//handle response...
		atomic.AddUint64(&count, 1)
	}
	s.send()
}

func (s *streamCbImpl) send() {
	s.n++
	if s.n >= s.loop {
		return
	}
	now := time.Now()
	//serialize request
	s.req.Reset()
	s.req.ID = uint64(now.UnixNano())
	s.req.Name = "xxx"
	s.req.Key = s.key
	if err := s.req.WriteToShm(s.stream.BufferWriter()); err != nil {
		fmt.Println("write request to share memory failed, err=" + err.Error())
		return
	}
	if err := s.stream.Flush(false); err != nil {
		fmt.Println(" flush request to peer failed, err=" + err.Error())
		return
	}
}

func (s *streamCbImpl) OnLocalClose() {
	//fmt.Println("stream OnLocalClose")
}

func (s *streamCbImpl) OnRemoteClose() {
	//fmt.Println("stream OnRemoteClose")

}

func main() {
	// 1. dial unix domain socket
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	// 2. init session manager
	conf := shmipc.DefaultSessionManagerConfig()
	conf.Address = filepath.Join(dir, "../ipc_test.sock")
	conf.Network = "unix"
	conf.SessionNum = 1
	conf.ShareMemoryBufferCap = 32 << 20
	conf.MemMapType = shmipc.MemMapTypeMemFd
	smgr, err := shmipc.InitGlobalSessionManager(conf)
	if err != nil {
		panic(err)
	}

	concurrency := 500

	for i := 0; i < concurrency; i++ {
		go func() {
			//for range time.Tick(time.Second) {
			key := make([]byte, 1024)
			rand.Read(key)
			s := &streamCbImpl{key: key, smgr: smgr, loop: math.MaxUint64}
			stream, err := smgr.GetStream()
			if err != nil {
				fmt.Println("get stream error:" + err.Error())
				return
			}
			s.stream = stream
			s.n = 0
			stream.SetCallbacks(s)
			s.send()
			//}
		}()
	}

	time.Sleep(10030 * time.Second)
	fmt.Println("smgr.Close():", smgr.Close())
}
