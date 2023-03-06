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
	"os"
	"path/filepath"
	"runtime"

	"github.com/cloudwego/shmipc"
)

func main() {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	udsPath := filepath.Join(dir, "../ipc_test.sock")

	// 1.create client session manager
	conf := shmipc.DefaultSessionManagerConfig()
	conf.ShareMemoryPathPrefix = "/dev/shm/client.ipc.shm"
	conf.Network = "unix"
	conf.Address = udsPath
	if runtime.GOOS == "darwin" {
		conf.ShareMemoryPathPrefix = "/tmp/client.ipc.shm"
		conf.QueuePath = "/tmp/client.ipc.shm_queue"
	}

	s, err := shmipc.NewSessionManager(conf)
	if err != nil {
		panic("create client session failed, " + err.Error())
	}
	defer s.Close()

	// 2.create stream
	stream, err := s.GetStream()
	if err != nil {
		panic("client open stream failed, " + err.Error())
	}
	defer s.PutBack(stream)

	// 3. write message
	requestMsg := "client say hello world!!!"
	writer := stream.BufferWriter()
	err = writer.WriteString(requestMsg)
	if err != nil {
		panic("buffer writeString failed " + err.Error())
	}

	// 4. flush the stream buffer data to peer
	fmt.Println("client stream send request:" + requestMsg)
	err = stream.Flush(true)
	if err != nil {
		panic("stream Flush failed," + err.Error())
	}

	reader := stream.BufferReader()
	// 5.read response
	respData, err := reader.ReadBytes(len("server hello world!!!"))
	if err != nil {
		panic("respBuf ReadBytes failed," + err.Error())
	}

	fmt.Println("client stream receive response " + string(respData))
}
