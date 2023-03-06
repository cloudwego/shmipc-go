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
	"flag"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/cloudwego/shmipc/example/best_practice/idl"
)

var count uint64

func init() {
	go func() {
		lastCount := count
		for range time.Tick(time.Second) {
			curCount := atomic.LoadUint64(&count)
			fmt.Println("net_client qps:", curCount-lastCount)
			lastCount = curCount
		}
	}()
	go func() {
		http.ListenAndServe(":20001", nil)
	}()
	runtime.GOMAXPROCS(1)
}

func main() {
	packageSize := flag.Int("p", 1024, "-p 1024 mean that request and response's size are both near 1KB")
	flag.Parse()

	randContent := make([]byte, *packageSize)
	rand.Read(randContent)

	// 1. dial unix domain socket
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	address := filepath.Join(dir, "../ipc_test.sock")
	network := "unix"

	concurrency := 500
	qps := 50000000

	for i := 0; i < concurrency; i++ {
		go func() {
			req := &idl.Request{}
			resp := &idl.Response{}
			n := qps / concurrency
			conn, err := net.Dial(network, address)
			if err != nil {
				fmt.Println("dial error", err)
				return
			}
			for range time.Tick(time.Second) {
				for k := 0; k < n; k++ {
					now := time.Now()
					//serialize request
					req.Reset()
					req.ID = uint64(now.UnixNano())
					req.Name = "xxx"
					req.Key = randContent
					writeBuffer := req.Serialize()
					idl.MustWrite(conn, writeBuffer)
					idl.BufferPool.Put(writeBuffer)

					//wait and read response
					buf := idl.BufferPool.Get().([]byte)
					n, err := conn.Read(buf)
					if err != nil {
						fmt.Println("conn.Read error ", err)
						return
					}
					resp.Reset()
					resp.Deserialize(buf[:n])
					idl.BufferPool.Put(buf)

					{
						//handle response...
						atomic.AddUint64(&count, 1)
					}
				}
			}
		}()
	}

	time.Sleep(1200 * time.Second)
}
