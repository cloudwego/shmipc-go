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
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/cloudwego/shmipc"
	"github.com/cloudwego/shmipc/example/best_practice/idl"
)

var count uint64

func handleStream(s *shmipc.Stream) {
	req := &idl.Request{}
	resp := &idl.Response{}
	for {
		//1.deserialize Request
		if err := req.ReadFromShm(s.BufferReader()); err != nil {
			fmt.Println("stream read request, err=" + err.Error())
			return
		}

		{
			//2.handle request
			atomic.AddUint64(&count, 1)
		}

		//3.serialize Response
		resp.ID = req.ID
		resp.Name = req.Name
		resp.Image = req.Key
		if err := resp.WriteToShm(s.BufferWriter()); err != nil {
			fmt.Println("stream write response failed, err=" + err.Error())
			return
		}
		if err := s.Flush(false); err != nil {
			fmt.Println("stream write response failed, err=" + err.Error())
			return
		}
		req.Reset()
		resp.Reset()
	}
}

func init() {
	go func() {
		lastCount := count
		for range time.Tick(time.Second) {
			curCount := atomic.LoadUint64(&count)
			fmt.Println("shmipc_server qps:", curCount-lastCount)
			lastCount = curCount
		}
	}()
	go func() {
		http.ListenAndServe(":20000", nil)
	}()
	runtime.GOMAXPROCS(1)
}

func main() {
	// 1. listen unix domain socket
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	udsPath := filepath.Join(dir, "../ipc_test.sock")

	syscall.Unlink(udsPath)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: udsPath, Net: "unix"})
	if err != nil {
		panic(err)
	}
	defer ln.Close()

	// 2. accept a unix doamin socket
	for {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Printf("accept error:%s now exit", err.Error())
			return
		}
		go func() {
			defer conn.Close()

			// 3. create server session
			conf := shmipc.DefaultConfig()
			server, err := shmipc.Server(conn, conf)
			if err != nil {
				panic("new ipc server failed " + err.Error())
			}
			defer server.Close()

			// 4. accept stream and handle
			for {
				stream, err := server.AcceptStream()
				if err != nil {
					fmt.Println("shmipc server accept stream failed, err=" + err.Error())
					break
				}
				go handleStream(stream)
			}
		}()
	}

	fmt.Println("shmipc server exited")
}
