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
	"net"
)

var (
	protocolVersionInitializersFactory                     = make(map[int]func(session *Session, firstEvent header) protocolInitializer)
	_                                  protocolInitializer = &protocolInitializerV2{}
	_                                  protocolInitializer = &protocolInitializerV3{}
)

func init() {
	protocolVersionInitializersFactory[2] = func(session *Session, firstEvent header) protocolInitializer {
		return &protocolInitializerV2{session: session, firstEvent: firstEvent}
	}

	protocolVersionInitializersFactory[3] = func(session *Session, firstEvent header) protocolInitializer {
		return &protocolInitializerV3{session: session, firstEvent: firstEvent}
	}
}

type protocolInitializer interface {
	Version() uint8
	Init() error
}

type protocolInitializerV2 struct {
	session    *Session
	netConn    net.Conn
	firstEvent header
}

func (p *protocolInitializerV2) Init() error {
	if !p.session.isClient {
		if p.firstEvent.MsgType() != typeShareMemoryByFilePath {
			return fmt.Errorf("protocolInitializerV2 expect first event is:%d(%s),but:%d",
				typeShareMemoryByFilePath, typeShareMemoryByFilePath.String(), p.firstEvent.MsgType())
		}
		return handleShareMemoryByFilePath(p.session, p.firstEvent)
	}
	return sendShareMemoryByFilePath(p.session)
}

func (p *protocolInitializerV2) Version() uint8 {
	return 2
}

type protocolInitializerV3 struct {
	session    *Session
	firstEvent header
}

func (p *protocolInitializerV3) Init() error {
	if p.session.isClient {
		return p.clientInit()
	}
	return p.serverInit()
}

func (p *protocolInitializerV3) Version() uint8 {
	return 3
}

func (p *protocolInitializerV3) serverInit() error {
	if p.firstEvent.MsgType() != typeExchangeProtoVersion {
		return fmt.Errorf("protocolInitializerV3 expect firsts event is:%d(%s) but:%d",
			typeExchangeProtoVersion, typeExchangeProtoVersion.String(), p.firstEvent.MsgType())
	}

	//1.exchange version
	if err := handleExchangeVersion(p.session, p.firstEvent); err != nil {
		return errors.New("protocolInitializerV3 exchangeVersion failed, reason:" + err.Error())
	}

	//2.recv and mapping share memory
	h, err := blockReadEventHeader(p.session.connFd)
	if err != nil {
		return errors.New("protocolInitializerV3 blockReadEventHeader failed,reason:" + err.Error())
	}
	switch h.MsgType() {
	case typeShareMemoryByFilePath:
		err = handleShareMemoryByFilePath(p.session, h)
	case typeShareMemoryByMemfd:
		err = handleShareMemoryByMemFd(p.session, h)
	default:
		return fmt.Errorf("expect event type is typeShareMemoryByFilePath or typeShareMemoryByMemfd but:%d %s",
			h.MsgType(), h.MsgType().String())
	}

	if err != nil {
		return err
	}

	//3.ack share memory
	respHeader := header(make([]byte, headerSize))
	respHeader.encode(headerSize, p.session.communicationVersion, typeAckShareMemory)
	protocolTrace(respHeader, nil, true)
	return blockWriteFull(p.session.connFd, respHeader)
}

func (p *protocolInitializerV3) clientInit() error {
	var err error
	memType := p.session.config.MemMapType
	switch memType {
	case MemMapTypeDevShmFile:
		err = sendShareMemoryByFilePath(p.session)
	case MemMapTypeMemFd:
		err = sendMemFdToPeer(p.session)
	default:
		err = fmt.Errorf("unknown memory type:%d", memType)
	}

	if err != nil {
		return err
	}
	_, err = waitEventHeader(p.session.connFd, typeAckShareMemory)

	return err
}
