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

	"github.com/cloudwego/shmipc-go/example/best_practice/idl"
)

var count uint64

func handleConn(conn net.Conn) {
	defer conn.Close()

	req := &idl.Request{}
	resp := &idl.Response{}
	for {
		//1.deserialize Request
		readBuffer := idl.BufferPool.Get().([]byte)
		n, err := conn.Read(readBuffer)
		if err != nil {
			fmt.Println("conn.Read", err)
			return
		}
		req.Deserialize(readBuffer[:n])
		idl.BufferPool.Put(readBuffer)

		{
			//2.handle request
			atomic.AddUint64(&count, 1)
		}

		//3.serialize Response
		resp.ID = req.ID
		resp.Name = req.Name
		resp.Image = req.Key
		writeBuffer := req.Serialize()
		idl.MustWrite(conn, writeBuffer)
		idl.BufferPool.Put(writeBuffer)

		req.Reset()
		resp.Reset()
	}
}

func init() {
	go func() {
		lastCount := count
		for range time.Tick(time.Second) {
			curCount := atomic.LoadUint64(&count)
			fmt.Println("net_server qps:", curCount-lastCount)
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
			fmt.Printf("accept error:%s now exit \n", err.Error())
			return
		}
		go handleConn(conn)
	}

	fmt.Println("shmipc server exited")
}
