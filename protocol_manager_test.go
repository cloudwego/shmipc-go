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

package shmipc

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	_ "net/http/pprof"
	"testing"
)

const udsPath = "/tmp/shmipc.sock"

func TestProtocolCompatibilityForNetUnixConn(t *testing.T) {
	//testProtocolCompatibility(t, MemMapTypeDevShmFile)
	testProtocolCompatibility(t, MemMapTypeMemFd)
}

func testProtocolCompatibility(t *testing.T, memType MemMapType) {
	fmt.Println("----------bengin test protocolAdaptor MemMapType ----------", memType)
	clientConn, serverConn := testUdsConn()
	conf := testConf()
	conf.MemMapType = memType
	go func() {
		sconf := testConf()
		server, err := Server(serverConn, sconf)
		assert.Equal(t, true, err == nil, err)
		if err == nil {
			server.Close()
		}
	}()

	client, err := newSession(conf, clientConn, true)
	assert.Equal(t, true, err == nil, err)
	if err == nil {
		client.Close()
	}

	fmt.Println("----------end test protocolAdaptor client V2 to server V2 ----------")
}
