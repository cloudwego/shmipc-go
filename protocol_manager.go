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
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
)

// Ensure that the index of the handler matches the message type
var (
	protocolHandlers = []protocolHandler{
		typePolling:       handlePolling,
		typeStreamClose:   handleStreamClose,
		typeFallbackData:  handleFallbackData,
		typeHotRestart:    handleHotRestart,
		typeHotRestartAck: handleHotRestartAck,
	}

	protocolTraceMode = false
)

type protocolHandler func(s *Session, eventHeader header, buf []byte) (consumed int, stop bool, err error)

func init() {
	if os.Getenv("SHMIPC_PROTOCOL_TRACE") != "" {
		protocolTraceMode = true
	}
}

type protocolAdaptor struct {
	session *Session
}

func newProtocolAdaptor(session *Session) (pm *protocolAdaptor) {
	return &protocolAdaptor{session: session}
}

func createProtoVersionInitializer(session *Session, version uint8, firstEvent header) (protocolInitializer, error) {
	if f, ok := protocolVersionInitializersFactory[int(version)]; ok {
		return f(session, firstEvent), nil
	}
	return nil, fmt.Errorf("not support the protocol version:%d, maxSupportVersion is %d",
		version, maxSupportProtoVersion)
}

func (p *protocolAdaptor) getProtocolInitializer() (protocolInitializer, error) {
	if p.session.isClient {
		return p.clientGetProtocolInitializer()
	}
	return p.serverGetProtocolInitializer()
}

func (p *protocolAdaptor) clientGetProtocolInitializer() (initializer protocolInitializer, err error) {
	//temporarily ensure version compatibility.
	//when all server upgrade to new version, delete this following codes.
	if p.session.config.MemMapType == MemMapTypeDevShmFile {
		return &protocolInitializerV2{session: p.session}, nil
	}

	//send version to peer
	h := header(make([]byte, headerSize))
	clientVersion := int(maxSupportProtoVersion)
	h.encode(headerSize, uint8(clientVersion), typeExchangeProtoVersion)
	protocolTrace(h, nil, true)
	if err := blockWriteFull(p.session.connFd, h); err != nil {
		return nil, err
	}
	// recv peer's version
	var recvHeader header
	if recvHeader, err = waitEventHeader(p.session.connFd, typeExchangeProtoVersion); err != nil {
		return nil, errors.New("protocolInitializerV3 clientInit failed,reason:" + err.Error())
	}

	serverVersion := recvHeader.Version()
	chosenVersion := uint8(minInt(clientVersion, int(serverVersion)))

	initializer, err = createProtoVersionInitializer(p.session, chosenVersion, nil)
	if err != nil {
		return nil, err
	}
	return
}

func (p *protocolAdaptor) serverGetProtocolInitializer() (protocolInitializer, error) {
	//ensure version compatibility
	h, err := blockReadEventHeader(p.session.connFd)
	if err != nil {
		return nil, err
	}

	initializer, err := createProtoVersionInitializer(p.session, h.Version(), h)
	if err != nil {
		return nil, err
	}

	return initializer, nil
}

// handleShareMemoryByFilePath
func handleShareMemoryByFilePath(s *Session, hdr header) error {
	s.logger.infof("handleShareMemoryMetadata head:%+v", hdr)
	body := make([]byte, hdr.Length()-headerSize)
	err := blockReadFull(s.connFd, body)
	if err != nil {
		//todo
		if err != io.EOF && !strings.Contains(err.Error(), "closed") && !strings.Contains(err.Error(), "reset by peer") {
			s.logger.errorf("shmipc: Failed to read pathlen: %s", err.Error())
		}
		return err
	}
	bufferPath, queuePath := s.extractShmMetadata(body)
	qm, err := mappingQueueManager(queuePath)
	if err != nil {
		return fmt.Errorf("handleShareMemoryByFilePath mappingQueueManager failed,queuePathLen:%d path:%s err=%s",
			len(queuePath), queuePath, err.Error())
	}
	s.queueManager = qm

	bm, err := getGlobalBufferManager(bufferPath, 0, false, nil)
	if err != nil {
		return fmt.Errorf("handleShareMemoryByFilePath mappingBufferManager failed, bufferPathLen:%d path:%s err=%s",
			len(bufferPath), bufferPath, err.Error())
	}
	s.bufferManager = bm

	s.handshakeDone = true
	return nil
}

// todo with stream'timeout
func handleFallbackData(s *Session, h header, buf []byte) (int, bool, error) {
	eventLen := int(h.Length())
	payloadLen := eventLen - headerSize
	if len(buf) < payloadLen {
		return 0, true, nil
	}
	data := make([]byte, payloadLen)
	copy(data, buf[:payloadLen])
	const fallbackDataHeader = 8
	// fallback data layout:  eventHeader | seqID | status | payload
	seqID := binary.BigEndian.Uint32(data[:4])
	// now the first byte of status is streamState, and the other byte of status is undefined .
	status := binary.BigEndian.Uint32(data[4:8]) & 0xff
	s.logger.warnf("receive fallback data , length:%d seqID:%d status:%d",
		h.Length()-headerSize-fallbackDataHeader, seqID, status)
	s.openCircuitBreaker()
	fallbackSlice := newBufferSlice(nil, data[fallbackDataHeader:], 0, false)
	fallbackSlice.writeIndex = len(data[fallbackDataHeader:])
	atomic.AddUint64(&s.stats.fallbackReadCount, 1)
	stream := s.getStream(seqID, streamState(status))
	if stream == nil {
		return eventLen, false, nil
	}
	return eventLen, false, s.handleStreamMessage(stream, bufferSliceWrapper{fallbackSlice: fallbackSlice}, streamState(status))
}

func handleExchangeVersion(s *Session, h header) error {
	respHeader := header(make([]byte, headerSize))
	respHeader.encode(headerSize, maxSupportProtoVersion, typeExchangeProtoVersion)
	s.communicationVersion = uint8(minInt(int(h.Version()), int(maxSupportProtoVersion)))
	protocolTrace(respHeader, nil, true)
	return blockWriteFull(s.connFd, respHeader)
}

func handleShareMemoryByMemFd(s *Session, h header) error {
	s.logger.infof("recv memfd, header:%s", h.String())

	//1.recv shm metadata
	body := make([]byte, h.Length()-headerSize)
	err := blockReadFull(s.connFd, body)
	if err != nil {
		return errors.New("read shm metadata failed,reason:" + err.Error())
	}
	bufferPath, queuePath := s.extractShmMetadata(body)

	//2.send AckReadyRecvFD
	ack := header(make([]byte, headerSize))
	ack.encode(headerSize, s.communicationVersion, typeAckReadyRecvFD)
	s.logger.infof("response typeAckReadyRecvFD")
	if err := blockWriteFull(s.connFd, ack); err != nil {
		return errors.New("send ack typeAckReadyRecvFD failed reason:" + err.Error())
	}
	s.logger.infof("typeAckReadyRecvFD send finished")
	//3.recv fd
	oob := make([]byte, syscall.CmsgSpace(memfdCount*memfdDataLen))

	s.logger.infof("send ack finished")
	oobn, err := blockReadOutOfBoundForFd(s.connFd, oob)
	s.logger.infof("recvmsg finished, oob expect len:%d, len:%d", len(oob), oobn)
	if err != nil {
		return errors.New("try recv fd from peer failed,reason:" + err.Error())
	}
	if oobn != len(oob) {
		return fmt.Errorf("handleShareMemoryByMemFd failed,reason:"+
			"ReadOutOfBoundForFd ,expect oobnLen:%d, but oobnLen:%d",
			len(oob), oobn)
	}
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return errors.New("parse socket control message failed,reason:" + err.Error())
	}
	if len(msgs) == 0 {
		return errors.New("parse socket control message ret is nil")
	}
	fds, err := syscall.ParseUnixRights(&msgs[0])
	if err != nil {
		return errors.New("parse fd from unix domain failed,reason:" + err.Error())
	}
	if len(fds) < memfdCount {
		s.logger.warnf("ParseUnixRights len fds:%d", len(fds))
		return errors.New("the number of memfd received is wrong")
	}

	bufferFd, queueFd := fds[0], fds[1]
	s.logger.infof("recv memfd, bufferPath:%s queuePath:%s bufferFd:%d  queueFd:%d",
		bufferPath, queuePath, bufferFd, queueFd)

	//4.mapping share memory
	qm, err := mappingQueueManagerMemfd(queuePath, queueFd)
	if err != nil {
		return err
	}
	s.queueManager = qm
	bm, err := getGlobalBufferManagerWithMemFd(bufferPath, bufferFd, 0, false, nil)
	if err != nil {
		return err
	}

	s.bufferManager = bm
	s.handshakeDone = true
	s.logger.infof("handleShareMemoryByMemFd done")
	return nil
}

func handlePolling(s *Session, hdr header, buf []byte) (int, bool, error) {
	atomic.AddUint64(&s.stats.recvPollingEventCount, 1)
	consumedCount := 0
	var retErr error
	for {
		for ele, err := s.queueManager.recvQueue.pop(); err == nil; ele, err = s.queueManager.recvQueue.pop() {
			consumedCount++
			state := streamState(ele.status & 0xff)
			stream := s.getStream(ele.seqID, state)
			if stream == nil && state == streamOpened {
				slice, err := s.bufferManager.readBufferSlice(ele.offsetInShmBuf)
				if err != nil {
					return headerSize, false, err
				}
				s.bufferManager.recycleBuffers(slice)
				continue
			}
			if stream == nil {
				continue
			}
			retErr = s.handleStreamMessage(stream, bufferSliceWrapper{offset: ele.offsetInShmBuf}, state)
		}

		runtime.Gosched()
		if s.queueManager.recvQueue.markNotWorking() {
			break
		}
	}
	//followings code will bring runtime.convT64, which maybe result in GC.
	//s.logger.infof("queue consumer consume size:%d on path:%s now wait", consumedCount, s.queueManager.path)
	return headerSize, false, retErr
}

func handleHotRestart(s *Session, hdr header, buf []byte) (int, bool, error) {
	if len(buf) < epochIDLen {
		return 0, true, nil
	}
	epochID := binary.BigEndian.Uint64(buf[:epochIDLen])
	s.logger.warnf("%s [epoch:%d] receive hot restart", s.sessionName(), epochID)

	s.dispatcher.post(func() {
		s.manager.handleEvent(typeHotRestart, &sessionManagerHotRestartParams{epoch: epochID, session: s})
	})

	return headerSize + epochIDLen, false, nil
}

func handleHotRestartAck(s *Session, hdr header, buf []byte) (int, bool, error) {
	if len(buf) < epochIDLen {
		return 0, true, nil
	}
	epochID := binary.BigEndian.Uint64(buf[:epochIDLen])
	s.logger.warnf("%s [epoch:%d] receive hot restart ack", s.name, epochID)

	s.listener.mu.Lock()
	defer s.listener.mu.Unlock()

	if epochID == s.listener.epoch {
		s.listener.hotRestartAckCount--
		s.state = hotRestartDoneState
	}

	return headerSize + epochIDLen, false, nil
}

func handleStreamClose(s *Session, hdr header, buf []byte) (int, bool, error) {
	const idLen = 4
	if len(buf) < idLen {
		return 0, true, nil
	}
	id := binary.BigEndian.Uint32(buf[:4])
	s.logger.debugf("receive peer stream[%d] goaway.", id)

	stream := s.getStreamById(id)
	if stream == nil {
		s.logger.warnf("missing stream: %d", id)
		return headerSize + idLen, false, nil
	}

	stream.halfClose()
	return headerSize + idLen, false, nil
}

func protocolTrace(h header, body []byte, send bool) {
	if !protocolTraceMode {
		return
	}
	if len(h) < headerSize {
		return
	}
	if send {
		protocolLogger.warnf("send, header:%s body:%s", h.String(), string(body))
	} else {
		protocolLogger.warnf("recv, header:%s body:%s", h.String(), string(body))
	}
}

func sendShareMemoryByFilePath(s *Session) error {
	data := s.generateShmMetadata(typeShareMemoryByFilePath)
	s.logger.infof("send share memory address to peer, size:%d queuePath:%s cap:%d bufferPath:%s cap:%d ",
		len(data), s.queueManager.path, len(s.queueManager.mem), s.bufferManager.path, len(s.bufferManager.mem))
	protocolTrace(data[:headerSize], data[headerSize:], true)
	if err := blockWriteFull(s.connFd, data); err != nil {
		return err
	}
	return nil
}

func sendMemFdToPeer(s *Session) error {
	event := s.generateShmMetadata(typeShareMemoryByMemfd)
	s.logger.infof("sendMemFdToPeer buffer fd:%d queue fd:%d header:%s",
		s.bufferManager.memFd, s.queueManager.memFd, header(event).String())
	protocolTrace(event[:headerSize], event[headerSize:], true)

	//1. client send the message with typeShareMemoryByMemfd
	//2. server reply the message with typeAckReadyRecvFD
	//3. client send memfd to server.
	if err := blockWriteFull(s.connFd, event); err != nil {
		return err
	}
	if _, err := waitEventHeader(s.connFd, typeAckReadyRecvFD); err != nil {
		return err
	}
	return sendFd(s.connFd, syscall.UnixRights(s.bufferManager.memFd, s.queueManager.memFd))
}

func waitEventHeader(connFd int, expectEventType eventType) (header, error) {
	h, err := blockReadEventHeader(connFd)
	if err != nil {
		return nil, err
	}
	if h.MsgType() != expectEventType {
		return nil, fmt.Errorf("expect eventType:%d %s, but:%d", expectEventType, expectEventType.String(), h.MsgType())
	}

	return h, nil
}

func blockReadEventHeader(connFd int) (header, error) {
	buf := make([]byte, headerSize)
	if err := blockReadFull(connFd, buf); err != nil {
		return nil, err
	}

	h := header(buf)
	if err := checkEventValid(h); err != nil {
		return nil, err
	}
	protocolTrace(h, nil, false)
	return h, nil
}
