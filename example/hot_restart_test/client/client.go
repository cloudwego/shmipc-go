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
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/cloudwego/shmipc"
)

var (
	count    uint64
	errCount uint64

	sendStr = "hello world"
)

func init() {
	go func() {
		lastCount := atomic.LoadUint64(&count)
		for range time.Tick(time.Second) {
			curCount := atomic.LoadUint64(&count)
			err := atomic.LoadUint64(&errCount)
			fmt.Println("qps:", curCount-lastCount, "  errCount = ", err, " count ", atomic.LoadUint64(&count))
			lastCount = curCount
		}
	}()
	runtime.GOMAXPROCS(4)

	go func() {
		http.ListenAndServe(":20001", nil)
	}()
}

func addCount(isErr bool) {
	if isErr {
		atomic.AddUint64(&errCount, 1)
	}
	atomic.AddUint64(&count, 1)
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
	conf.SessionNum = 4
	conf.ShareMemoryBufferCap = 32 << 20
	//conf.MemMapType = shmipc.MemMapTypeMemFd
	conf.MemMapType = shmipc.MemMapTypeDevShmFile
	if os.Getenv("PATH_PREFIX") != "" {
		conf.ShareMemoryPathPrefix = os.Getenv("PATH_PREFIX")
	}
	smgr, err := shmipc.InitGlobalSessionManager(conf)
	if err != nil {
		panic(err)
	}

	for i := 0; i < 50; i++ {
		go func() {
			for {
				stream, err := smgr.GetStream()
				if err != nil {
					fmt.Printf("stream GetStream error %+v\n", err)
					time.Sleep(time.Second * 5)
					continue
				}

				err = stream.BufferWriter().WriteString(sendStr)
				if err != nil {
					fmt.Printf("stream WriteString error %+v\n", err)
					addCount(true)
					continue
				}

				err = stream.Flush(false)
				if err != nil {
					fmt.Printf("stream Flush error %+v\n", err)
					addCount(true)
					continue
				}

				ret, err := stream.BufferReader().ReadString(len("hello world"))
				if err != nil {
					fmt.Printf("stream ReadString error %+v\n", err)
					addCount(true)
				} else {
					addCount(false)
				}

				if ret != sendStr {
					fmt.Printf("stream ret %+v err\n", ret)
				}

				smgr.PutBack(stream)

				time.Sleep(time.Millisecond * 10)
			}
		}()
	}

	time.Sleep(100000 * time.Second)
	fmt.Println("smgr.Close():", smgr.Close())
}
