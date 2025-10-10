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
	syscall "golang.org/x/sys/unix"
	"io"
)

func blockReadFull(connFd int, data []byte) error {
	readSize := 0
	for readSize < len(data) {
		n, err := syscall.Read(connFd, data[readSize:])
		if err != nil {
			return fmt.Errorf("ReadFull failed, had readSize:%d reason:%s", readSize, err.Error())
		}
		readSize += n
		if n == 0 {
			return io.EOF
		}
	}
	return nil
}

func blockWriteFull(connFd int, data []byte) error {
	written := 0
	for written < len(data) {
		n, err := syscall.Write(connFd, data[written:])
		if err != nil {
			return err
		}
		written += n
	}
	return nil
}

func sendFd(connFd int, oob []byte) error {
	err := syscall.Sendmsg(connFd, nil, oob, nil, 0)
	return err
}

func blockReadOutOfBoundForFd(connFd int, oob []byte) (oobn int, err error) {
	_, oobn, _, _, err = syscall.Recvmsg(connFd, nil, oob, 0)
	return
}
