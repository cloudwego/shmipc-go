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
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/cloudwego/shmipc-go"
	"github.com/cloudwego/shmipc-go/example/best_practice/idl"
)

var count uint64

func init() {
	go func() {
		lastCount := count
		for range time.Tick(time.Second) {
			curCount := atomic.LoadUint64(&count)
			fmt.Println("shmipc_client qps:", curCount-lastCount)
			lastCount = curCount
		}
	}()

	go func() {
		http.ListenAndServe(":20001", nil) //nolint:errcheck
	}()

	runtime.GOMAXPROCS(1)
}

func main() {
	packageSize := flag.Int("p", 1024, "-p 1024 mean that request and response's size are both near 1KB")
	flag.Parse()

	randContent := make([]byte, *packageSize)
	rand.Read(randContent)

	// 1. get current directory
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	// 2. init session manager
	conf := shmipc.DefaultSessionManagerConfig()
	conf.Address = filepath.Join(dir, "../ipc_test.sock")
	conf.Network = "unix"
	conf.MemMapType = shmipc.MemMapTypeMemFd
	conf.SessionNum = 1
	conf.InitializeTimeout = 100 * time.Second
	smgr, err := shmipc.NewSessionManager(conf)
	if err != nil {
		panic(err)
	}

	concurrency := 500
	qps := 50000000

	for i := 0; i < concurrency; i++ {
		go func() {
			req := &idl.Request{}
			resp := &idl.Response{}
			n := qps / concurrency

			for range time.Tick(time.Second) {
				// 3. get stream from SessionManager
				stream, err := smgr.GetStream()
				for k := 0; k < n; k++ {
					now := time.Now()
					if err != nil {
						fmt.Println("get stream error:" + err.Error())
						continue
					}

					// 4. set request object
					req.Reset()
					req.ID = uint64(now.UnixNano())
					req.Name = "xxx"
					req.Key = randContent

					// 5. write req to buffer of stream
					if err := req.WriteToShm(stream.BufferWriter()); err != nil {
						fmt.Println("write request to share memory failed, err=" + err.Error())
						return
					}

					// 6. flush the buffered data of stream to peer
					if err := stream.Flush(false); err != nil {
						fmt.Println(" flush request to peer failed, err=" + err.Error())
						return
					}

					// 7. wait and read response
					resp.Reset()
					if err := resp.ReadFromShm(stream.BufferReader()); err != nil {
						fmt.Println("write request to share memory failed, err=" + err.Error())
						continue
					}

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
