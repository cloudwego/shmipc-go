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
)

var (
	//ErrInvalidVersion means that we received a message with an invalid version
	ErrInvalidVersion = errors.New("invalid protocol version")

	//ErrInvalidMsgType means that we received a message with an invalid message type
	ErrInvalidMsgType = errors.New("invalid msg type")

	//ErrSessionShutdown means that the session is shutdown
	ErrSessionShutdown = errors.New("session shutdown")

	//ErrStreamsExhausted means that the stream id was used out and maybe have some streams leaked.
	ErrStreamsExhausted = errors.New("streams exhausted")

	//ErrTimeout is used when we reach an IO deadline
	ErrTimeout = errors.New("i/o deadline reached")

	//ErrStreamClosed was returned when using a closed stream
	ErrStreamClosed = errors.New("stream closed")

	//ErrConnectionWriteTimeout means that the write timeout was happened in tcp/unix connection.
	ErrConnectionWriteTimeout = errors.New("connection write timeout")

	//ErrEndOfStream means that the stream is end, user shouldn't to read from the stream.
	ErrEndOfStream = errors.New("end of stream")

	//ErrSessionUnhealthy occurred at Session.OpenStream(), which mean that the session is overload.
	//user should retry after 60 seconds(now). the followings situation will result in ErrSessionUnhealthy.
	//on client side:
	//  1. when local share memory is not enough, client send request data via unix domain socket.
	//  2. when peer share memory is not enough, client receive response data from unix domain socket.
	ErrSessionUnhealthy = errors.New("now the session is unhealthy, please retry later")

	//ErrNotEnoughData means that the real read size < expect read size.
	ErrNotEnoughData = errors.New("current buffer is not enough data to read")

	//ErrNoMoreBuffer means that the share memory is busy, and not more buffer to allocate.
	ErrNoMoreBuffer = errors.New("share memory not more buffer")

	//ErrShareMemoryHadNotLeftSpace means that reached the limitation of the file system when using MemMapTypeDevShm.
	ErrShareMemoryHadNotLeftSpace = errors.New("share memory had not left space")

	//ErrStreamCallbackHadExisted was returned if the Stream'Callbacks had existed
	ErrStreamCallbackHadExisted = errors.New("stream callbacks had existed")

	//ErrOSNonSupported means that shmipc couldn't work in current OS. (only support Linux now)
	ErrOSNonSupported = errors.New("shmipc just support linux OS now")

	//ErrArchNonSupported means that shmipc only support amd64 and arm64
	ErrArchNonSupported = errors.New("shmipc just support amd64 or arm64 arch")

	//ErrHotRestartInProgress was returned by Listener.HotRestart when the Session had under the hot restart state
	ErrHotRestartInProgress = errors.New("hot restart in progress, try again later")

	//ErrInHandshakeStage was happened in the case that the uninitialized session doing hot restart.
	ErrInHandshakeStage = errors.New("session in handshake stage, try again later")

	//ErrFileNameTooLong mean that eht Config.ShareMemoryPathPrefixFile's length reached the limitation of the OS.
	ErrFileNameTooLong = errors.New("share memory path prefix too long")

	//ErrQueueFull mean that the server is so busy that the io queue is full
	ErrQueueFull = errors.New("the io queue is full")

	errQueueEmpty = errors.New("the io queue is empty")
)
