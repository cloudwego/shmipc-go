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
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/shirou/gopsutil/v3/disk"
)

var (
	timerPool = &sync.Pool{
		New: func() interface{} {
			timer := time.NewTimer(time.Hour * 1e6)
			timer.Stop()
			return timer
		},
	}
)

// asyncSendErr is used to try an async send of an error
func asyncSendErr(ch chan error, err error) {
	if ch == nil {
		return
	}
	select {
	case ch <- err:
	default:
	}
}

// asyncNotify is used to signal a waiting goroutine
func asyncNotify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// min computes the minimum of two values
func min(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a < b {
		return b
	}
	return a
}

func string2bytesZeroCopy(s string) []byte {
	stringHeader := (*reflect.StringHeader)(unsafe.Pointer(&s))

	bh := reflect.SliceHeader{
		Data: stringHeader.Data,
		Len:  stringHeader.Len,
		Cap:  stringHeader.Len,
	}

	return *(*[]byte)(unsafe.Pointer(&bh))
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	if err != nil {
		return os.IsExist(err)
	}
	return true
}

//In Linux OS,  there is a limitation which is the capacity of the tmpfs (which usually on the directory /dev/shm).
//if we do mmap on /dev/shm/xxx and the free memory of the tmpfs is not enough, mmap have no any error.
//but when program is  running, it maybe crashed due to the bus error.
func canCreateOnDevShm(size uint64, path string) bool {
	if runtime.GOOS == "linux" && strings.Contains(path, "/dev/shm") {
		stat, err := disk.Usage("/dev/shm")
		if err != nil {
			internalLogger.warnf("could read /dev/shm free size, canCreateOnDevShm default return true")
			return false
		}
		return stat.Free >= size
	}
	return true
}

// delete only existing files
func safeRemoveUdsFile(filename string) bool {
	fileInfo, err := os.Stat(filename)
	if err != nil {
		internalLogger.warnf("%s Stat error %+v", filename, err)
		return false
	}

	if fileInfo.IsDir() {
		return false
	}

	if err := os.Remove(filename); err != nil {
		internalLogger.warnf("%s Remove error %+v", filename, err)
		return false
	}

	return true
}

func isArmArch() bool {
	return runtime.GOARCH == "arm" || runtime.GOARCH == "arm64"
}
