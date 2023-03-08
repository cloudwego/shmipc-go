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
	"strconv"
)

// event type for internal implements
const (
	typeShareMemoryByFilePath eventType = 0
	// notify peer start consume
	typePolling eventType = 1
	// stream level event notify peer stream close
	typeStreamClose eventType = 2
	// typePing // TODO
	typeFallbackData eventType = 3
	// exchange proto version
	typeExchangeProtoVersion eventType = 4
	//query the mem map type supported by the server
	typeShareMemoryByMemfd eventType = 5
	// when server mapping share memory success, give the ack to client.
	typeAckShareMemory eventType = 6
	typeAckReadyRecvFD eventType = 7
	typeHotRestart     eventType = 8
	typeHotRestartAck  eventType = 9

	minEventType = typeShareMemoryByFilePath
	maxEventType = typeHotRestartAck
)

func init() {
	for i := 0; i < int(maxSupportProtoVersion)+1; i++ {
		pollingEventWithVersion[i] = make([]byte, headerSize)
		pollingEventWithVersion[i].encode(headerSize, uint8(i), typePolling)
	}
}

type header []byte

func (h header) Length() uint32 {
	return binary.BigEndian.Uint32(h[0:4])
}

func (h header) Magic() uint16 {
	return binary.BigEndian.Uint16(h[4:6])
}

func (h header) Version() uint8 {
	return h[6]
}

func (h header) MsgType() eventType {
	return eventType(h[7])
}

func (h header) String() string {
	return fmt.Sprintf("Length:%d Magic:%d Version:%d Type:%s ",
		h.Length(), h.Magic(), h.Version(), h.MsgType().String())
}

func (h header) encode(length uint32, version uint8, msgType eventType) {
	binary.BigEndian.PutUint32(h[0:4], length)
	binary.BigEndian.PutUint16(h[4:6], magicNumber)
	h[6] = version
	h[7] = uint8(msgType)
}

// header | seqID | status
type fallbackDataEvent [headerSize + 8]byte

func (f *fallbackDataEvent) encode(length int, version uint8, seqID uint32, status uint32) {
	binary.BigEndian.PutUint32(f[0:4], uint32(length))
	binary.BigEndian.PutUint16(f[4:6], magicNumber)
	f[6] = version
	f[7] = uint8(typeFallbackData)
	binary.BigEndian.PutUint32(f[8:12], seqID)
	binary.BigEndian.PutUint32(f[12:16], status)
}

func (t eventType) String() string {
	switch t {
	case typeShareMemoryByFilePath:
		return "ShareMemoryByFilePath"
	case typePolling:
		return "Polling"
	case typeStreamClose:
		return "StreamClose"
	case typeFallbackData:
		return "FallbackData"
	case typeShareMemoryByMemfd:
		return "ShareMemoryByMemfd"
	case typeExchangeProtoVersion:
		return "ExchangeProtoVersion"
	case typeAckShareMemory:
		return "AckShareMemory"
	case typeAckReadyRecvFD:
		return "AckReadyRecvFD"
	case typeHotRestart:
		return "HotRestart"
	case typeHotRestartAck:
		return "HotRestartAck"
	}

	return "<UNSET>" + strconv.Itoa(int(t))
}

func checkEventValid(hdr header) error {
	// Verify the magic&version
	if hdr.Magic() != magicNumber || hdr.Version() == 0 {
		internalLogger.errorf("shmipc: Invalid magic or version %d, %d", hdr.Magic(), hdr.Version())
		return ErrInvalidVersion
	}
	mt := hdr.MsgType()
	if mt < minEventType || mt > maxEventType {
		internalLogger.errorf("shmipc, invalid protocol header: " + hdr.String())
		return ErrInvalidMsgType
	}
	return nil
}
