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
	"time"
)

const (
	// protoVersion is the only version we support
	protoVersion           uint8  = 2
	maxSupportProtoVersion uint8  = 3
	magicNumber            uint16 = 0x7758
)

type eventType uint8

type sessionSateType uint32

// MemMapType is the mapping type of shared memory
type MemMapType uint8

const (
	// MemMapTypeDevShmFile maps share memory to /dev/shm (tmpfs)
	MemMapTypeDevShmFile MemMapType = 0
	// MemMapTypeMemFd maps share memory to memfd (Linux OS v3.17+)
	MemMapTypeMemFd MemMapType = 1
)

const (
	defaultState sessionSateType = iota
	// server: send hot restart event; client: recv hot restart event
	hotRestartState
	// sercer: recv hot restart ack event; client: send hot restart ack event
	hotRestartDoneState
)

const (
	memfdCreateName = "shmipc"

	memfdDataLen = 4
	memfdCount   = 2

	bufferPathSuffix = "_buffer"
	unixNetwork      = "unix"

	hotRestartCheckTimeout  = 2 * time.Second
	hotRestartCheckInterval = 100 * time.Millisecond

	sessionRebuildInterval = time.Second * 60

	epochIDLen = 8
	// linux file name max length
	fileNameMaxLen = 255
	// buffer path = %s_epoch_${epochID}_${randID}
	// len("_epoch_") + maxUint64StrLength + len("_") + maxUint64StrLength
	epochInfoMaxLen = 7 + 20 + 1 + 20
	// _queue_{sessionID int}
	queueInfoMaxLen = 7 + 20
)

const (
	defaultQueueCap         = 8192
	defaultShareMemoryCap   = 32 * 1024 * 1024
	defaultSingleBufferSize = 4096
	queueElementLen         = 12
	queueCount              = 2
)

const (
	sizeOfLength  = 4
	sizeOfMagic   = 2
	sizeOfVersion = 1
	sizeOfType    = 1

	headerSize = sizeOfLength + sizeOfMagic + sizeOfVersion + sizeOfType
)

var (
	zeroTime                = time.Time{}
	pollingEventWithVersion [maxSupportProtoVersion + 1]header
)
