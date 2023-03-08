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
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
)

// Config is used to tune the shmipc session
type Config struct {
	// ConnectionWriteTimeout is meant to be a "safety valve" timeout after
	// we which will suspect a problem with the underlying connection and
	// close it. This is only applied to writes, where's there's generally
	// an expectation that things will move along quickly.
	ConnectionWriteTimeout time.Duration

	//In the initialization phase, client and server will exchange metadata and mapping share memory.
	//the InitializeTimeout specify how long time could use in this phase.
	InitializeTimeout time.Duration

	//The max number of pending io request. default is 8192
	QueueCap uint32

	//Share memory path of the underlying queue.
	QueuePath string

	//The capacity of buffer in share memory. default is 32MB
	ShareMemoryBufferCap uint32

	//The share memory path prefix of buffer.
	ShareMemoryPathPrefix string

	//LogOutput is used to control the log destination.
	LogOutput io.Writer

	//BufferSliceSizes could adjust share memory buffer slice size.
	//which could improve performance if most of all request or response's could write into single buffer slice instead of multi buffer slices.
	//Because multi buffer slices mean that more allocate and free operation,
	//and if the payload cross different buffer slice, it mean that payload in memory isn't continuous.
	//Default value is:
	// 1. 50% share memory hold on buffer slices that every slice is near to 8KB.
	// 2. 30% share memory hold on buffer slices that every slice is near to 32KB.
	// 3. 20% share memory hold on buffer slices that every slice is near to 128KB.
	BufferSliceSizes []*SizePercentPair

	// Server side's asynchronous API
	listenCallback ListenCallback

	//MemMapTypeDevShmFile or MemMapTypeMemFd (client set)
	MemMapType MemMapType

	//Session will emit some metrics to the Monitor with periodically (default 30s)
	Monitor Monitor

	// client rebuild session interval
	rebuildInterval time.Duration
}

//DefaultConfig is used to return a default configuration
func DefaultConfig() *Config {
	return &Config{
		ConnectionWriteTimeout: 10 * time.Second,
		InitializeTimeout:      1000 * time.Millisecond,
		QueueCap:               defaultQueueCap,
		ShareMemoryBufferCap:   defaultShareMemoryCap,
		ShareMemoryPathPrefix:  "/dev/shm/shmipc",
		QueuePath:              "/dev/shm/shmipc_queue",
		LogOutput:              os.Stdout,
		MemMapType:             MemMapTypeDevShmFile,
		BufferSliceSizes: []*SizePercentPair{
			{8192 - bufferHeaderSize, 50},
			{32*1024 - bufferHeaderSize, 30},
			{128*1024 - bufferHeaderSize, 20},
		},
		rebuildInterval: sessionRebuildInterval,
	}
}

//VerifyConfig is used to verify the sanity of configuration
func VerifyConfig(config *Config) error {
	if config.ShareMemoryBufferCap < (1 << 20) {
		return fmt.Errorf("share memory size is too small:%d, must greater than %d", config.ShareMemoryBufferCap, 1<<20)
	}
	if len(config.BufferSliceSizes) == 0 {
		return fmt.Errorf("BufferSliceSizes could not be nil")
	}

	sum := 0
	for _, pair := range config.BufferSliceSizes {
		sum += int(pair.Percent)
		if pair.Size > config.ShareMemoryBufferCap {
			return fmt.Errorf("BufferSliceSizes's Size:%d couldn't greater than ShareMemoryBufferCap:%d",
				pair.Size, config.ShareMemoryBufferCap)
		}
	}
	if sum != 100 {
		return errors.New("the sum of BufferSliceSizes's Percent should be 100")
	}

	if config.ShareMemoryPathPrefix == "" || config.QueuePath == "" {
		return errors.New("buffer path or queue path could not be nil")
	}

	if runtime.GOOS != "linux" {
		return ErrOSNonSupported
	}

	return nil
}
