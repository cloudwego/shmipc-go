//go:build race
// +build race

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
	"encoding/binary"
	"fmt"
	syscall "golang.org/x/sys/unix"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

var (
	_ dispatcher = &epollDispatcher{}
	_ eventConn  = &connEventHandler{}
)

func init() {
	defaultDispatcher = newEpollDispatcher()
}

type connEventHandler struct {
	dispatcher     *epollDispatcher
	file           *os.File
	callback       eventConnCallback
	readBuffer     []byte
	onWriteReadyCh chan struct{}
	ioves          [256]syscall.Iovec
	readStartOff   int
	readEndOff     int
	fd             int
	isClose        uint32
}

func (c *connEventHandler) handleEvent(events int, d *epollDispatcher) {
	if events&syscall.EPOLLRDHUP != 0 {
		c.onRemoteClose()
		return
	}

	if events&syscall.EPOLLIN != 0 {
		if err := c.onReadReady(); err != nil {
			internalLogger.warnf("read failed fd:%d reason:%s", c.fd, err.Error())
		}
	}

	if events&syscall.EPOLLOUT != 0 {
		if err := c.onWriteReady(); err != nil {
			internalLogger.warnf("write failed fd:%d reason:%s", c.fd, err.Error())
		}
	}
}

func (c *connEventHandler) onRemoteClose() {
	c.callback.onRemoteClose()
	c.deferredClose()
}

// avoiding write concurrently, blocking until return
func (c *connEventHandler) writev(data ...[]byte) error {
	if len(data) == 0 {
		return nil
	}
	writtenSliceNum := 0
	for writtenSliceNum < len(data) {
		n, err := c.doWritev(data[writtenSliceNum:]...)
		if err != nil {
			return err
		}
		writtenSliceNum += n
	}
	return nil
}

func (c *connEventHandler) doWritev(data ...[]byte) (int, error) {
	needSubmitIovecLen := 0
	for needSubmitIovecLen < len(data) && needSubmitIovecLen < len(c.ioves) {
		sliceLen := len(data[needSubmitIovecLen])
		c.ioves[needSubmitIovecLen].Len = uint64(sliceLen)
		c.ioves[needSubmitIovecLen].Base = &data[needSubmitIovecLen][0]
		needSubmitIovecLen++
	}
	writtenSliceNum := needSubmitIovecLen

	writtenVec := 0
	for needSubmitIovecLen > 0 {
		if atomic.LoadUint32(&c.isClose) == 1 {
			return -1, syscall.EPIPE
		}
		n, _, err := syscall.Syscall(syscall.SYS_WRITEV, uintptr(c.fd),
			uintptr(unsafe.Pointer(&c.ioves[writtenVec])), uintptr(needSubmitIovecLen))

		if err == syscall.EAGAIN {
			<-c.onWriteReadyCh
			continue
		}

		if err != syscall.Errno(0) {
			return -1, err
		}

		//ack write
		for writtenSize := uint64(n); writtenSize > 0; {
			if writtenSize >= c.ioves[writtenVec].Len {
				writtenSize -= c.ioves[writtenVec].Len
				needSubmitIovecLen--
				writtenVec++
			} else {
				c.ioves[writtenVec].Len -= writtenSize
				startOff := uint64(len(data[writtenVec])) - c.ioves[writtenVec].Len
				c.ioves[writtenVec].Base = &data[writtenVec][startOff]
				break
			}
		}

	}
	return writtenSliceNum, nil
}

// avoiding write concurrently, blocking until return
func (c *connEventHandler) write(data []byte) error {
	written, size := 0, len(data)
	for written < size {
		if atomic.LoadUint32(&c.isClose) == 1 {
			return syscall.EPIPE
		}
		n, _, err := syscall.Syscall(syscall.SYS_WRITE, uintptr(c.fd), uintptr(unsafe.Pointer(&data[written])),
			uintptr(size-written))
		if err == syscall.EAGAIN {
			<-c.onWriteReadyCh
			continue
		}
		if err != syscall.Errno(0) {
			return err
		}
		written += int(n)
	}

	return nil
}

func (c *connEventHandler) maybeExpandReadBuffer() {
	bufRemain := len(c.readBuffer) - c.readEndOff
	if bufRemain == 0 {
		newBuf := make([]byte, 2*len(c.readBuffer))
		c.readEndOff = copy(newBuf, c.readBuffer[c.readStartOff:c.readEndOff])
		c.readStartOff = 0
		c.readBuffer = newBuf
	}
}

// we could ensure that when the onReadReady() was called, the connection's fd must be open.
func (c *connEventHandler) onReadReady() (err error) {
	const onDataThreshold = 1 * 1024 * 1024
	for {
		c.maybeExpandReadBuffer()
		n, _, errCode := syscall.RawSyscall(syscall.SYS_READ, uintptr(c.fd),
			uintptr(unsafe.Pointer(&c.readBuffer[c.readEndOff])), uintptr(len(c.readBuffer)-c.readEndOff))

		if errCode == syscall.EAGAIN {
			break
		}

		if errCode != 0 {
			return
		}
		if n == 0 {
			c.onRemoteClose()
			break
		}

		c.readEndOff += int(n)
		if c.readEndOff-c.readStartOff >= onDataThreshold {
			if err = c.callback.onEventData(c.readBuffer[c.readStartOff:c.readEndOff], c); err != nil {
				return err
			}
		}
	}

	return c.callback.onEventData(c.readBuffer[c.readStartOff:c.readEndOff], c)
}

func (c *connEventHandler) onWriteReady() error {
	//fmt.Println("onWriteReady fd:", c.fd)
	if atomic.LoadUint32(&c.isClose) == 0 {
		asyncNotify(c.onWriteReadyCh)
	}
	return nil
}

func (c *connEventHandler) commitRead(n int) {
	c.readStartOff += n
	if c.readStartOff == c.readEndOff {
		// The threshold for resizing is 4M
		minResizedBufferSize := 4 * 1024 * 1024
		// The size of readBuffer is even times of 64K
		if len(c.readBuffer) > minResizedBufferSize {
			// We need not reset buffer size to 64k immediately
			// since we may expand next time.
			// Reduce it gradually
			minResizedBufferSize = len(c.readBuffer) / 2
			c.readBuffer = c.readBuffer[:minResizedBufferSize]
		}
		c.readStartOff = 0
		c.readEndOff = 0
	}
}

func (c *connEventHandler) close() error {
	c.callback.onLocalClose()
	c.deferredClose()
	return nil
}

func (c *connEventHandler) deferredClose() {
	if atomic.CompareAndSwapUint32(&c.isClose, 0, 1) {
		close(c.onWriteReadyCh)
		c.dispatcher.post(func() {
			epollCtl(c.dispatcher.epollFd, syscall.EPOLL_CTL_DEL, c.fd, nil)
			c.dispatcher.lock.Lock()
			delete(c.dispatcher.conns, c.fd)
			c.dispatcher.lock.Unlock()
			c.file.Close()
		})
	}
}

func (c *connEventHandler) setCallback(cb eventConnCallback) error {
	if err := syscall.SetNonblock(c.fd, true); err != nil {
		return fmt.Errorf("fd:%d couldn't set nobloking,reason=%s", c.fd, err)
	}
	event := &epollEvent{
		events: syscall.EPOLLIN | syscall.EPOLLOUT | epollModeET | syscall.EPOLLRDHUP,
	}
	binary.BigEndian.PutUint32(event.data[0:4], uint32(c.fd))
	c.dispatcher.lock.Lock()
	defer c.dispatcher.lock.Unlock()
	c.callback = cb
	if err := epollCtl(c.dispatcher.epollFd, syscall.EPOLL_CTL_ADD, c.fd, event); err != nil {
		return fmt.Errorf("epollCt fd:%d failed, reason=%s", c.fd, err)
	}
	c.dispatcher.conns[c.fd] = c
	return nil
}

type epollDispatcher struct {
	epollFd        int
	epollFile      *os.File
	conns          map[int]*connEventHandler
	lock           sync.Mutex
	waitLoopExitWg sync.WaitGroup
	toCloseConns   []*connEventHandler
	pendingLambda  []func()
	runningLambda  []func()
	lambdaLock     sync.Mutex
	isShutdown     bool
}

func newEpollDispatcher() *epollDispatcher {
	return &epollDispatcher{
		conns:         make(map[int]*connEventHandler, 8),
		pendingLambda: make([]func(), 0, 32),
		runningLambda: make([]func(), 0, 32),
	}
}

func (d *epollDispatcher) post(f func()) {
	d.lambdaLock.Lock()
	d.pendingLambda = append(d.pendingLambda, f)
	d.lambdaLock.Unlock()
}

func (d *epollDispatcher) newConnection(file *os.File) eventConn {
	return &connEventHandler{
		fd:             int(file.Fd()), //notice: the function file.Fd() will set fd blocking
		file:           file,
		dispatcher:     d,
		readBuffer:     make([]byte, 64*1024),
		onWriteReadyCh: make(chan struct{}, 1),
		isClose:        0,
	}
}

func (d *epollDispatcher) runLambda() {
	d.lambdaLock.Lock()
	d.runningLambda, d.pendingLambda = d.pendingLambda, d.runningLambda
	d.lambdaLock.Unlock()
	for _, f := range d.runningLambda {
		if d.isShutdown {
			return
		}
		f()
	}
	d.runningLambda = d.runningLambda[:0]
}

func (d *epollDispatcher) runLoop() error {
	epollFd, err := syscall.EpollCreate1(0)
	if err != nil {
		return err
	}
	d.epollFd = epollFd
	d.waitLoopExitWg.Add(1)
	go func() {
		defer d.waitLoopExitWg.Done()
		var events [128]epollEvent
		timeout := 0
		for {
			n, err := epollWait(d.epollFd, events[:], timeout)
			if err != nil {
				fmt.Println("epoll wait error", err)
				return
			}
			if n <= 0 {
				timeout = 1000
				runtime.Gosched()
				d.runLambda()
				continue
			}
			timeout = 0
			d.lock.Lock()
			for i := 0; i < n; i++ {
				fd := int(binary.BigEndian.Uint32(events[i].data[:4]))
				h := d.conns[fd]
				h.handleEvent(int(events[i].events), d)
			}
			d.lock.Unlock()
			d.runLambda()
		}
		runtime.KeepAlive(d)
	}()
	return nil
}

func (d *epollDispatcher) shutdown() error {
	d.epollFile.Close()
	d.waitLoopExitWg.Wait()
	d.lock.Lock()
	defer d.lock.Unlock()
	d.isShutdown = true
	for fd := range d.conns {
		syscall.Close(fd)
		delete(d.conns, fd)
	}
	return nil
}
