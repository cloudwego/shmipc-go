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
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cloudwego/shmipc-go"
)

func main() {
	// 1. listen unix domain socket
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	udsPath := filepath.Join(dir, "../ipc_test.sock")

	_ = syscall.Unlink(udsPath)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: udsPath, Net: "unix"})
	if err != nil {
		panic(err)
	}
	defer ln.Close()

	// 2. accept a unix domain socket
	conn, err := ln.Accept()
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	// 3. create server session
	conf := shmipc.DefaultConfig()
	s, err := shmipc.Server(conn, conf)
	if err != nil {
		panic("new ipc server failed " + err.Error())
	}
	defer s.Close()

	// 4. accept a stream
	stream, err := s.AcceptStream()
	if err != nil {
		panic("accept stream failed " + err.Error())
	}
	defer stream.Close()

	// 5.read request data
	reader := stream.BufferReader()
	reqData, err := reader.ReadBytes(len("client say hello world!!!"))
	if err != nil {
		panic("reqBuf readData failed " + err.Error())
	}
	fmt.Println("server receive request message:" + string(reqData))

	// 6.write data to response buffer
	respMsg := "server hello world!!!"
	writer := stream.BufferWriter()
	err = writer.WriteString(respMsg)
	if err != nil {
		panic("respBuf WriteString failed " + err.Error())
	}

	// 7.flush response buffer to peer.
	err = stream.Flush(true)
	if err != nil {
		panic("stream write response failed," + err.Error())
	}
	fmt.Println("server reply response: " + respMsg)

	time.Sleep(time.Second)
}
