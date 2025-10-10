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
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"
	"unsafe"
)

type logger struct {
	name      string
	out       io.Writer
	callDepth int
}

var (
	internalLogger = &logger{"", os.Stdout, 3}
	protocolLogger = &logger{"protocol trace", os.Stdout, 4}
	level          int
	debugMode      = false

	magenta = string([]byte{27, 91, 57, 53, 109}) // Trace
	green   = string([]byte{27, 91, 57, 50, 109}) // Debug
	blue    = string([]byte{27, 91, 57, 52, 109}) // Info
	yellow  = string([]byte{27, 91, 57, 51, 109}) // Warn
	red     = string([]byte{27, 91, 57, 49, 109}) // Error
	reset   = string([]byte{27, 91, 48, 109})

	colors = []string{
		magenta,
		green,
		blue,
		yellow,
		red,
	}

	levelName = []string{
		"Trace",
		"Debug",
		"Info",
		"Warn",
		"Error",
	}
)

const (
	levelTrace = iota
	levelDebug
	levelInfo
	levelWarn
	levelError
	levelNoPrint
)

func init() {
	level = levelWarn
	if os.Getenv("SHMIPC_LOG_LEVEL") != "" {
		if n, err := strconv.Atoi(os.Getenv("SHMIPC_LOG_LEVEL")); err == nil {
			if n <= levelNoPrint {
				level = n
			}
		}
	}

	if os.Getenv("SHMIPC_DEBUG_MODE") != "" {
		debugMode = true
	}
}

// SetLogLevel used to change the internal logger's level and the default level is Warning.
// The process env `SHMIPC_LOG_LEVEL` also could set log level
func SetLogLevel(l int) {
	if l <= levelNoPrint {
		level = l
	}
}

func newLogger(name string, out io.Writer) *logger {
	if out == nil {
		out = os.Stdout
	}
	return &logger{
		name:      name,
		out:       out,
		callDepth: 3,
	}
}

func (l *logger) errorf(format string, a ...interface{}) {
	if level > levelError {
		return
	}
	fmt.Fprintf(l.out, l.prefix(levelError)+format+reset+"\n", a...)
}

func (l *logger) error(v interface{}) {
	if level > levelError {
		return
	}
	fmt.Fprintln(l.out, l.prefix(levelError), v, reset)
}

func (l *logger) warnf(format string, a ...interface{}) {
	if level > levelWarn {
		return
	}
	fmt.Fprintf(l.out, l.prefix(levelWarn)+format+reset+"\n", a...)
}

func (l *logger) warn(v interface{}) {
	if level > levelWarn {
		return
	}
	fmt.Fprintln(l.out, l.prefix(levelWarn), v, reset)
}

func (l *logger) infof(format string, a ...interface{}) {
	if level > levelInfo {
		return
	}
	fmt.Fprintf(l.out, l.prefix(levelInfo)+format+reset+"\n", a...)
}

func (l *logger) info(v interface{}) {
	if level > levelInfo {
		return
	}
	fmt.Fprintln(l.out, l.prefix(levelInfo), v, reset)
}

func (l *logger) debugf(format string, a ...interface{}) {
	if level > levelDebug {
		return
	}
	fmt.Fprintf(l.out, l.prefix(levelDebug)+format+reset+"\n", a...)
}

func (l *logger) debug(v interface{}) {
	if level > levelDebug {
		return
	}
	fmt.Fprintln(l.out, l.prefix(levelDebug), v, reset)
}

func (l *logger) tracef(format string, a ...interface{}) {
	if level > levelTrace {
		return
	}
	//todo optimized
	fmt.Fprintf(l.out, l.prefix(levelTrace)+format+reset+"\n", a...)
}

func (l *logger) trace(v interface{}) {
	if level > levelTrace {
		return
	}
	fmt.Fprintln(l.out, l.prefix(levelTrace), v, reset)
}

func (l *logger) prefix(level int) string {
	var buffer [64]byte
	buf := bytes.NewBuffer(buffer[:0])
	_, _ = buf.WriteString(colors[level])
	_, _ = buf.WriteString(levelName[level])
	_ = buf.WriteByte(' ')
	_, _ = buf.WriteString(time.Now().Format("2006-01-02 15:04:05.999999"))
	_ = buf.WriteByte(' ')
	_, _ = buf.WriteString(l.location())
	_ = buf.WriteByte(' ')
	_, _ = buf.WriteString(l.name)
	_ = buf.WriteByte(' ')
	return buf.String()
}

func (l *logger) location() string {
	_, file, line, ok := runtime.Caller(l.callDepth)
	if !ok {
		file = "???"
		line = 0
	}
	file = filepath.Base(file)
	return file + ":" + strconv.Itoa(line)
}

func computeFreeSliceNum(list *bufferList) int {
	freeSlices := 0
	offset := atomic.LoadUint32(list.head)
	for {
		freeSlices++
		bh := bufferHeader(list.bufferRegion[offset:])
		hasNext := bh.hasNext()
		if !hasNext && offset != atomic.LoadUint32(list.tail) {
			fmt.Printf("something error, expectedTailOffset:%d but:%d current freeSlices:%d \n",
				atomic.LoadUint32(list.tail), offset, freeSlices)
		}
		if !hasNext {
			break
		}
		offset = bh.nextBufferOffset()
		if offset >= uint32(len(list.bufferRegion)) {
			fmt.Printf("something error , next offset is :%d ,greater than bufferRegion length:%d\n",
				offset, len(list.bufferRegion))
			break
		}
	}
	return freeSlices
}

// 1.occurred memory leak, if list's free slice number != expect free slice number.
// 2.print the metadata and payload of the leaked slice.
func debugBufferListDetail(path string, bufferMgrHeaderSize int, bufferHeaderSize int) {
	mem, err := ioutil.ReadFile(path)
	if err != nil {
		fmt.Println(err)
		return
	}
	bm, _ := mappingBufferManager(path, mem, 0)
	expectAllSliceNum := uint32(0)
	for i, list := range bm.lists {
		fmt.Printf("%d. list capacity:%d size:%d realSize:%d perSliceCap:%d headOffset:%d tailOffset:%d\n",
			i, *list.cap, *list.size, computeFreeSliceNum(list), *list.capPerBuffer, *list.head, *list.tail)
		expectAllSliceNum += *list.cap
	}
	freeSliceNum := uint32(bm.sliceSize())
	fmt.Printf("summary:memory leak:%t all free slice num:%d expect num:%d\n",
		freeSliceNum != expectAllSliceNum, freeSliceNum, expectAllSliceNum)

	if freeSliceNum != expectAllSliceNum {
		fmt.Println("now check the buffer slice which is in used")
	}
	printLeakShareMemory(bm, bufferMgrHeaderSize, bufferHeaderSize)
}
func printLeakShareMemory(bm *bufferManager, bufferMgrHeaderSize int, bufferHeaderSize int) {
	offsetInShm := bufferMgrHeaderSize
	for i := range bm.lists {
		offsetInShm += bufferListHeaderSize
		data := bm.lists[i].bufferRegion
		offset := 0
		realSize := 0
		for offset < len(data) {
			realSize++
			bh := bufferHeader(data[offset:])
			size := int(*(*uint32)(unsafe.Pointer(&data[offset+bufferSizeOffset])))
			flag := int(*(*uint8)(unsafe.Pointer(&data[offset+bufferFlagOffset])))
			if !bh.hasNext() || bh.isInUsed() {
				fmt.Printf("offset in shm :%d next:%d len:%d flag:%d inused:%t data:%s\n",
					offset+offsetInShm, bh.nextBufferOffset(), size, flag, bh.isInUsed(),
					string(data[offset+bufferHeaderSize:offset+bufferHeaderSize+size]))
			}
			offset += int(*bm.lists[i].capPerBuffer) + bufferHeaderSize
		}
		offsetInShm += offset
	}
}

// DebugBufferListDetail print all BufferList's status in share memory located in the `path`
// if MemMapType is MemMapTypeMemFd, you could using the command that
// `lsof -p $PID` to found the share memory which was mmap by memfd,
// and the command `cat /proc/$PID/$MEMFD > $path` dump the share memory to file system.
func DebugBufferListDetail(path string) {
	debugBufferListDetail(path, 8, 20)
}

// DebugQueueDetail print IO-Queue's status which was mmap in the `path`
func DebugQueueDetail(path string) {
	mem, err := ioutil.ReadFile(path)
	if err != nil {
		fmt.Println(err)
		return
	}
	sendQueue := mappingQueueFromBytes(mem[len(mem)/2:])
	recvQueue := mappingQueueFromBytes(mem[:len(mem)/2])
	printFunc := func(name string, q *queue) {
		fmt.Printf("path:%s name:%s, cap:%d head:%d tail:%d size:%d flag:%d\n",
			name, path, q.cap, *q.head, *q.tail, q.size(), *q.workingFlag)
	}
	printFunc("sendQueue", sendQueue)
	printFunc("recvQueue", recvQueue)
}
