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
	"net"
	"os"
	"sync"
)

var defaultDispatcher dispatcher

type eventConnCallback interface {
	onEventData(buf []byte, conn eventConn) error
	onRemoteClose()
	onLocalClose()
}

type eventConn interface {
	commitRead(n int)
	setCallback(cb eventConnCallback) error
	write(data []byte) error
	writev(data ...[]byte) error
	close() error
}

//only serve for connection now
type dispatcher interface {
	runLoop() error
	newConnection(connFd *os.File) eventConn
	shutdown() error
	post(f func())
}

var (
	dispatcherInitOnce sync.Once
)

func ensureDefaultDispatcherInit() {
	if defaultDispatcher != nil {
		dispatcherInitOnce.Do(func() {
			defaultDispatcher.runLoop()
		})
	}
}

func getConnDupFd(conn net.Conn) (*os.File, error) {
	type hasFile interface {
		File() (f *os.File, err error)
	}
	f, ok := conn.(hasFile)
	if !ok {
		return nil, fmt.Errorf("conn has no method File() (f *os.File, err error)")
	}
	return f.File()
}
