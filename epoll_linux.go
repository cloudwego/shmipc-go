//go:build !arm64
// +build !arm64

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
	syscall "golang.org/x/sys/unix"

	"unsafe"
)

const epollModeET = syscall.EPOLLET

type epollEvent struct {
	events uint32
	data   [8]byte
}

func epollCtl(epfd int, op int, fd int, event *epollEvent) (err error) {
	_, _, errCode := syscall.RawSyscall6(syscall.SYS_EPOLL_CTL, uintptr(epfd), uintptr(op), uintptr(fd), uintptr(unsafe.Pointer(event)), 0, 0)
	if errCode != syscall.Errno(0) {
		err = errCode
	}
	return err
}

func epollWait(epfd int, events []epollEvent, msec int) (n int, err error) {
	var n_ uintptr
	n_, _, errNo := syscall.Syscall6(syscall.SYS_EPOLL_WAIT, uintptr(epfd), uintptr(unsafe.Pointer(&events[0])),
		uintptr(len(events)), uintptr(msec), 0, 0)
	if errNo == syscall.Errno(0) {
		err = nil
	}
	return int(n_), err
}
