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
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Session is used to wrap a reliable ordered connection and to
// multiplex it into multiple streams.
type Session struct {
	// nextStreamID is the next stream we should send.
	// in client mode, nextStreamID is odd number.
	// in server mode, nextStreamID is even number.
	// todo support bidirectional streaming
	nextStreamID uint32

	// config holds our configuration
	config *Config

	// logger is used for our logs
	logger *logger

	dispatcher dispatcher
	// conn is the underlying connection
	connFd int
	//netConn only using for get remote address and local address
	netConn   net.Conn
	eventConn eventConn
	// streams maps a stream id to a stream  protected by streamLock.
	streams    map[uint32]*Stream
	streamLock sync.RWMutex

	// acceptCh is used to pass ready streams to the client
	acceptCh chan *Stream

	// sendCh is used to mark a stream as ready to send,
	// or to send a header out directly.
	sendCh                chan sendReady
	notifyContinueWriteCh chan struct{}

	// shutdown is used to safely close a session
	shutdown     uint32
	shutdownErr  error
	shutdownCh   chan struct{}
	shutdownLock sync.Mutex

	isClient      bool
	handshakeDone bool
	unhealthy     uint32
	writing       uint32
	waitWrite     int32

	bufferManager *bufferManager
	queueManager  *queueManager
	stats         stats
	monitor       Monitor

	communicationVersion uint8

	// used to print debug info, equal to queue path
	name      string
	state     sessionSateType
	sessionID int
	epochID   uint64
	randID    uint64
	manager   *SessionManager
	listener  *Listener
	mu        sync.Mutex
}

// sendReady is used to either mark a stream as ready
// or to directly send a header
type sendReady struct {
	Hdr  []byte
	Body []byte
	Err  chan error
}

// Server return a shmipc server with the giving connection and configuration
func Server(conn net.Conn, conf *Config) (*Session, error) {
	return newSession(conf, conn, false)
}

// newSession is used to construct a new session
func newSession(config *Config, conn net.Conn, isClient bool) (*Session, error) {
	if config == nil {
		config = DefaultConfig()
	}

	if err := VerifyConfig(config); err != nil {
		return nil, fmt.Errorf("VerifyConfig failed: %s", err.Error())
	}

	if config.MemMapType == MemMapTypeMemFd {
		if conn.LocalAddr().Network() != unixNetwork {
			return nil, errors.New("conn.Network must be unix when config.MemMapType is MemMapTypeMemFd")
		}
	}

	fd, err := getConnDupFd(conn)
	if err != nil {
		return nil, fmt.Errorf("could get fd from conn,reason=%s", err.Error())
	}
	// had dup fd, we manage the dup fd in internal's event loop
	defer conn.Close()

	ensureDefaultDispatcherInit()
	s := &Session{
		config:                config,
		dispatcher:            defaultDispatcher,
		connFd:                int(fd.Fd()),
		netConn:               conn,
		logger:                newSessionLogger(isClient, config.LogOutput),
		streams:               make(map[uint32]*Stream, 4096),
		sendCh:                make(chan sendReady, 4096), // TODO(zjb): config?
		notifyContinueWriteCh: make(chan struct{}, 1),
		shutdownCh:            make(chan struct{}),
		isClient:              isClient,
		communicationVersion:  protoVersion, //when finishing protocol init,which maybe change
		monitor:               config.Monitor,
	}

	//on server mode the backend goroutine will use acceptCh to transfer new stream.
	if !isClient {
		s.acceptCh = make(chan *Stream, 1024)
		s.nextStreamID = 2
	} else {
		s.nextStreamID = 1
	}

	if err := s.initMemManager(); err != nil {
		return nil, fmt.Errorf("create share memory buffer manager failed ,error=%w", err)
	}
	if err := s.initProtocol(); err != nil {
		if s.queueManager != nil {
			s.queueManager.unmap()
		}
		if s.bufferManager != nil {
			addGlobalBufferManagerRefCount(s.bufferManager.path, -1)
		}
		return nil, err
	}

	s.eventConn = s.dispatcher.newConnection(fd)
	if err := s.eventConn.setCallback(s); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.name = s.queueManager.path
	s.mu.Unlock()
	//currently, netConn only using for get remote address and local address.
	//maybe it could be optimized in the future
	go s.send()
	go s.monitorLoop()
	return s, nil
}

func newSessionLogger(isClient bool, out io.Writer) *logger {
	if isClient {
		return newLogger("client session", out)
	}
	return newLogger("server session", out)
}

func (s *Session) initProtocol() error {
	s.logger.infof("starting initializes shmipc protocol")
	resultCh := make(chan error, 1)
	timeout := time.NewTimer(s.config.InitializeTimeout)
	defer timeout.Stop()

	go func() {
		// initializing protocol , maybe block
		protoAdaptor := newProtocolAdaptor(s)
		initializer, err := protoAdaptor.getProtocolInitializer()
		if err != nil {
			asyncSendErr(resultCh, fmt.Errorf("getProtocolInitializer failed ,error=%w", err))
			return
		}
		s.communicationVersion = initializer.Version()
		if err = initializer.Init(); err != nil {
			asyncSendErr(resultCh, err)
			return
		}

		asyncSendErr(resultCh, nil)
	}()

	select {
	case err := <-resultCh:
		return err
	case <-timeout.C:
		return fmt.Errorf("protocolInitializer init timeout:%d ms",
			s.config.InitializeTimeout/time.Millisecond)
	}
}

// IsClosed does a safe check to see if we have shutdown
func (s *Session) IsClosed() bool {
	return s.getSessionShutdown() == 1
}

func (s *Session) getSessionShutdown() uint32 {
	return atomic.LoadUint32(&s.shutdown)
}

// IsHealthy return whether the session is healthy
func (s *Session) IsHealthy() bool {
	return atomic.LoadUint32(&s.unhealthy) == 0
}

// CloseChan returns a read-only channel which is closed as
// soon as the session is closed.
func (s *Session) CloseChan() <-chan struct{} {
	return s.shutdownCh
}

// GetActiveStreamCount returns the number of currently open streams
func (s *Session) GetActiveStreamCount() int {
	s.streamLock.RLock()
	num := len(s.streams)
	s.streamLock.RUnlock()
	return num
}

// OpenStream is used to create a new stream
func (s *Session) OpenStream() (*Stream, error) {
	if s.IsClosed() {
		return nil, s.shutdownErr
	}
	if !s.IsHealthy() {
		return nil, ErrSessionUnhealthy
	}

	// Get an ID, and check for stream exhaustion
	id := atomic.AddUint32(&s.nextStreamID, 1)

	// Register the stream
	stream := newStream(s, id)
	s.streamLock.Lock()
	if _, ok := s.streams[id]; ok {
		s.streamLock.Unlock()
		return nil, ErrStreamsExhausted
	}
	s.streams[id] = stream
	s.streamLock.Unlock()
	s.logger.tracef("open stream:%d", id)

	// FIXME(zjb): we can't send anything to peer, so peer don't know we open an new stream
	return stream, nil
}

// AcceptStream is used to block until the next available stream
// is ready to be accepted.
func (s *Session) AcceptStream() (*Stream, error) {
	select {
	case stream := <-s.acceptCh:
		s.logger.tracef("accept stream:%d", stream.id)
		return stream, nil
	case <-s.shutdownCh:
		return nil, s.shutdownErr
	}
}

// Close is used to close the session and all streams.
// Attempts to send a GoAway before closing the connection.
func (s *Session) Close() error {
	if !atomic.CompareAndSwapUint32(&s.shutdown, 0, 1) {
		return nil
	}
	s.logger.infof("close session %s hadShutDown:%d connFd:%d", s.name, atomic.LoadUint32(&s.shutdown), s.connFd)

	s.shutdownLock.Lock()
	if s.shutdownErr == nil {
		s.shutdownErr = ErrSessionShutdown
	}
	shutdownErr := s.shutdownErr.Error()
	s.shutdownLock.Unlock()

	if s.config.listenCallback != nil {
		s.config.listenCallback.OnShutdown(shutdownErr)
	}

	s.streamLock.Lock()
	for _, stream := range s.streams {
		stream.safeCloseNotify()
	}
	s.streamLock.Unlock()

	close(s.shutdownCh)
	s.dispatcher.post(func() {
		s.shutdownLock.Lock()
		defer s.shutdownLock.Unlock()
		//firstly close eventConn
		s.eventConn.close()

		s.streamLock.Lock()
		streams := s.streams
		s.streams = nil
		s.streamLock.Unlock()

		for _, stream := range streams {
			stream.Close()
			stream.asyncGoroutineWg.Wait()
		}

		if s.bufferManager != nil {
			addGlobalBufferManagerRefCount(s.bufferManager.path, -1)
		}
		if s.queueManager != nil {
			s.queueManager.unmap()
			s.queueManager = nil
		}
	})

	return nil
}

// LocalAddr is used to get the local address of the
// underlying connection.
func (s *Session) LocalAddr() net.Addr {
	return s.netConn.LocalAddr()
}

// RemoteAddr is used to get the address of remote end
// of the underlying connection
func (s *Session) RemoteAddr() net.Addr {
	return s.netConn.RemoteAddr()
}

func (s *Session) generateShmMetadata(eventType eventType) (data []byte) {
	data = make([]byte, headerSize+2+len(s.queueManager.path)+2+len(s.bufferManager.path))
	//queue share memory path
	offset := headerSize
	binary.BigEndian.PutUint16(data[offset:offset+2], uint16(len(s.queueManager.path)))
	offset += 2
	copy(data[offset:offset+len(s.queueManager.path)], s.queueManager.path)
	offset += len(s.queueManager.path)
	//buffer share memory path
	binary.BigEndian.PutUint16(data[offset:offset+2], uint16(len(s.bufferManager.path)))
	offset += 2
	copy(data[offset:offset+len(s.bufferManager.path)], s.bufferManager.path)
	header(data).encode(uint32(len(data)), s.communicationVersion, eventType)
	return
}

// exitErr is used to handle an error that is causing the
// session to terminate.
func (s *Session) exitErr(err error) {
	s.logger.errorf("%s exitErr:%s", s.sessionName(), err.Error())
	atomic.AddUint64(&s.stats.eventConnErrorCount, 1)
	s.shutdownLock.Lock()
	if s.shutdownErr == nil {
		s.shutdownErr = err
	}
	s.shutdownLock.Unlock()
	s.Close()
}

// waitForSendErr waits to send a header, checking for a potential shutdown
func (s *Session) waitForSend(hdr header, body []byte) error {
	protocolTrace(hdr, nil, true)
	errCh := make(chan error, 1)
	return s.waitForSendErr(hdr, body, errCh)
}

// waitForSendErr waits to send a header with optional data, checking for a
// potential shutdown. Since there's the expectation that sends can happen
// in a timely manner, we enforce the connection write timeout here.
func (s *Session) waitForSendErr(hdr header, body []byte, errCh chan error) error {
	t := timerPool.Get()
	timer := t.(*time.Timer)
	timer.Reset(s.config.ConnectionWriteTimeout)
	defer func() {
		timer.Stop()
		select {
		case <-timer.C:
		default:
		}
		timerPool.Put(t)
	}()

	ready := sendReady{Hdr: hdr, Body: body, Err: errCh}
	select {
	case s.sendCh <- ready:
	case <-s.shutdownCh:
		return s.shutdownErr
	case <-timer.C:
		s.logger.debugf("write timeout, s.sendCh is full, whose length is %d. timeout:%d",
			len(s.sendCh), s.config.ConnectionWriteTimeout)
		return ErrConnectionWriteTimeout
	}

	select {
	case err := <-errCh:
		return err
	case <-s.shutdownCh:
		return s.shutdownErr
	case <-timer.C:
		s.logger.debugf("write timeout. hadn't receive result ,timeout:%f",
			s.config.ConnectionWriteTimeout.Seconds())
		return ErrConnectionWriteTimeout
	}
}

func (s *Session) writeEventData(data []byte, ch chan error) {
	if err := s.eventConn.write(data); err != nil {
		//if _, err := s.netConn.Write(data[:]); err != nil {
		s.logger.errorf("shmipc: Failed to write data: %s", err.Error())
		asyncSendErr(ch, err)
		s.exitErr(err)
		return
	}
}

// send is a long running goroutine that sends data
func (s *Session) send() {
	s.logger.debugf("%s start send loop", s.name)
	defer s.logger.debugf("%s exit send loop", s.name)
	for {
		select {
		case ready := <-s.sendCh:
			for !atomic.CompareAndSwapUint32(&s.writing, 0, 1) {
				<-s.notifyContinueWriteCh
			}
			// Send a header if ready
			if ready.Hdr != nil {
				s.writeEventData(ready.Hdr, ready.Err)
			}
			// Send data from a body if given
			if ready.Body != nil {
				s.writeEventData(ready.Body, ready.Err)
			}
			atomic.StoreUint32(&s.writing, 0)

			// No error, successful send
			asyncSendErr(ready.Err, nil)
		case <-s.shutdownCh:
			return
		}
	}
}

func (s *Session) monitorLoop() {
	if s.monitor == nil {
		return
	}
	tick := time.NewTicker(time.Second * 30)
	emitFunc := func() {
		performanceMetrics, stabilityMetrics, shareMemoryMetrics := s.GetMetrics()
		s.monitor.OnEmitSessionMetrics(performanceMetrics, stabilityMetrics, shareMemoryMetrics, s)
	}
	defer func() {
		tick.Stop()
		s.monitor.Flush()
	}()
	for {
		select {
		case <-tick.C:
			emitFunc()
		case <-s.shutdownCh:
			emitFunc()
			return
		}
	}
}

func (s *Session) onEventData(buf []byte, conn eventConn) error {
	if s.IsClosed() {
		return nil
	}
	consumed, err := s.handleEvents(buf)
	conn.commitRead(consumed)

	if err != nil && !s.IsClosed() {
		s.exitErr(err)
	}
	return nil
}

// pass data race check
func (s *Session) sessionName() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.name
}

func (s *Session) onLocalClose() {}

func (s *Session) onRemoteClose() {
	s.exitErr(fmt.Errorf("connection was reset by peer. localAddress:%s remoteAddress:%s",
		s.netConn.LocalAddr().String(), s.netConn.RemoteAddr().String()))
}

func (s *Session) handleEvents(buf []byte) (consumed int, err error) {
	for len(buf[consumed:]) >= headerSize {
		eventHeader := header(buf[consumed : consumed+headerSize])
		if err = checkEventValid(eventHeader); err != nil {
			return consumed + headerSize, ErrInvalidMsgType
		}

		msgType := int(eventHeader.MsgType())
		if msgType >= len(protocolHandlers) || msgType < 0 {
			return consumed + headerSize, ErrInvalidMsgType
		}

		if protocolHandlers[msgType] == nil {
			return consumed + headerSize, ErrInvalidMsgType
		}
		n, stop, err := protocolHandlers[msgType](s, eventHeader, buf[consumed+headerSize:])
		consumed += n
		if err != nil {
			return consumed, err
		}
		if stop {
			break
		}
	}
	return
}

func (s *Session) openCircuitBreaker() {
	if debugMode {
		return
	}

	if !atomic.CompareAndSwapUint32(&s.unhealthy, 0, 1) {
		return
	}
	//todo duration
	time.AfterFunc(time.Second*30, func() {
		atomic.StoreUint32(&s.unhealthy, 0)
	})
}

func (s *Session) getStream(id uint32, state streamState) (stream *Stream) {
	s.streamLock.Lock()
	stream, ok := s.streams[id]
	//server mode
	if !s.isClient && state == streamOpened && !ok {
		// TODO(zjb): opt protocol for dup stream
		// accept a new stream
		stream = newStream(s, id)
		s.streams[id] = stream
		s.streamLock.Unlock()
		if s.config.listenCallback != nil {
			s.config.listenCallback.OnNewStream(stream)
		} else {
			select {
			case s.acceptCh <- stream:
			case <-s.shutdownCh:
			}
		}
		return
	}
	s.streamLock.Unlock()
	return

}

func (s *Session) getStreamById(id uint32) *Stream {
	s.streamLock.RLock()
	stream := s.streams[id]
	s.streamLock.RUnlock()
	return stream
}

// handleStreamMessage handles either a data frame
func (s *Session) handleStreamMessage(stream *Stream, wrapper bufferSliceWrapper, state streamState) error {

	//s.logger.debugf("receive peer stream id:%d state:%d", stream.id, state)
	if state == streamClosed {
		stream.halfClose()
		return nil
	}

	// Read the new data
	if err := stream.fillDataToReadBuffer(wrapper); err != nil {
		return err
	}

	return nil
}

func (s *Session) onStreamClose(id uint32, state streamState) {
	s.logger.tracef("stream:%d close state:%d", id, state)
	s.streamLock.Lock()
	delete(s.streams, id)
	s.streamLock.Unlock()
}

func (s *Session) wakeUpPeer() error {
	if !s.queueManager.sendQueue.markWorking() {
		return nil
	}
	atomic.AddUint64(&s.stats.sendPollingEventCount, 1)
	if atomic.CompareAndSwapUint32(&s.writing, 0, 1) {
		//fast path
		s.writeEventData(pollingEventWithVersion[s.communicationVersion], nil)
		atomic.StoreUint32(&s.writing, 0)
		asyncNotify(s.notifyContinueWriteCh)
	} else {
		//slow path
		s.sendCh <- sendReady{nil, pollingEventWithVersion[s.communicationVersion], nil}
	}
	return nil
}

func (s *Session) sendQueue() *queue {
	return s.queueManager.sendQueue
}

// client init buffer manager and queue manager
func (s *Session) initMemManager() error {
	if !s.isClient {
		return nil
	}

	mmapMapType := s.config.MemMapType

	var (
		err error
		bm  *bufferManager
		qm  *queueManager
	)

	if mmapMapType == MemMapTypeDevShmFile {
		if bm, err = getGlobalBufferManager(s.config.ShareMemoryPathPrefix+bufferPathSuffix,
			s.config.ShareMemoryBufferCap, true, s.config.BufferSliceSizes); err != nil {
			os.Remove(s.config.ShareMemoryPathPrefix + bufferPathSuffix)
			return fmt.Errorf("create share memory buffer manager failed ,error=%w", err)
		}
		if qm, err = createQueueManager(s.config.QueuePath, s.config.QueueCap); err != nil {
			os.Remove(s.config.QueuePath)
			return fmt.Errorf("create share memory queue manager failed ,error=%w", err)
		}
	} else {
		if bm, err = getGlobalBufferManagerWithMemFd(s.config.ShareMemoryPathPrefix+bufferPathSuffix,
			0, s.config.ShareMemoryBufferCap, true, s.config.BufferSliceSizes); err != nil {
			return fmt.Errorf("create share memory buffer manager failed ,error=%w", err)
		}
		if qm, err = createQueueManagerWithMemFd(s.config.QueuePath, s.config.QueueCap); err != nil {
			return fmt.Errorf("create share memory queue manager failed ,error=%w", err)
		}
	}

	s.bufferManager = bm
	s.queueManager = qm

	return nil
}

func (s *Session) extractShmMetadata(body []byte) (bufferPath string, queuePath string) {
	offset := 0
	queuePathLen := int(binary.BigEndian.Uint16(body[0:2]))
	offset += 2
	queuePath = string(body[offset : offset+queuePathLen])
	offset += queuePathLen

	bufferPathLen := int(binary.BigEndian.Uint16(body[offset : offset+2]))
	offset += 2
	bufferPath = string(body[offset : offset+bufferPathLen])
	return
}

func (s *Session) hotRestart(epoch uint64, event eventType) error {
	s.logger.warnf("%s [epoch:%d] begin hotRestart event type %d %s", s.name, epoch, event, event.String())
	if event != typeHotRestart && event != typeHotRestartAck {
		return fmt.Errorf("hotRestart invalid event type %d %s", event, event.String())
	}

	data := make([]byte, headerSize+8)
	offset := headerSize
	binary.BigEndian.PutUint64(data[offset:offset+8], epoch)
	header(data).encode(uint32(len(data)), s.communicationVersion, event)

	if atomic.CompareAndSwapUint32(&s.writing, 0, 1) {
		//fast path
		s.writeEventData(data, nil)
		atomic.StoreUint32(&s.writing, 0)
		asyncNotify(s.notifyContinueWriteCh)
	} else {
		//slow path
		s.sendCh <- sendReady{nil, data, nil}
	}

	return nil
}

// GetMetrics return the session's metrics for monitoring
func (s *Session) GetMetrics() (PerformanceMetrics, StabilityMetrics, ShareMemoryMetrics) {
	activeStreamCount := uint64(s.GetActiveStreamCount())

	//session will close shutdownCH when it  was stopping. and monitorLoop will wake up and flush metrics.
	//if queueManager do unmap at the moment, there will be panic.
	//so here we need ensure that the session hadn't shutdown.
	s.shutdownLock.Lock()
	var sendQueueCount, receiveQueueCount uint64
	if s.queueManager != nil && s.getSessionShutdown() == 0 {
		sendQueueCount = uint64(atomic.LoadInt64(s.queueManager.sendQueue.tail))
		receiveQueueCount = uint64(atomic.LoadInt64(s.queueManager.recvQueue.tail))
	}
	s.shutdownLock.Unlock()
	var smm ShareMemoryMetrics
	if s.bufferManager != nil && s.getSessionShutdown() == 0 {
		allShmBytes, inUsedShmBytes := uint32(0), uint32(0)
		for _, l := range s.bufferManager.lists {
			allShmBytes += (*l.cap) * (*l.capPerBuffer)
			inUsedShmBytes += (*l.cap - uint32(*l.size)) * (*l.capPerBuffer)
		}

		smm.CapacityOfShareMemoryInBytes = uint64(allShmBytes)
		smm.AllInUsedShareMemoryInBytes = uint64(inUsedShmBytes)
	}

	return PerformanceMetrics{
			ReceiveSyncEventCount: atomic.LoadUint64(&s.stats.recvPollingEventCount),
			SendSyncEventCount:    atomic.LoadUint64(&s.stats.sendPollingEventCount),
			OutFlowBytes:          atomic.LoadUint64(&s.stats.outFlowBytes),
			InFlowBytes:           atomic.LoadUint64(&s.stats.inFlowBytes),
			SendQueueCount:        sendQueueCount,
			ReceiveQueueCount:     receiveQueueCount,
		}, StabilityMetrics{
			AllocShmErrorCount:  atomic.LoadUint64(&s.stats.allocShmErrorCount),
			FallbackWriteCount:  atomic.LoadUint64(&s.stats.fallbackWriteCount),
			FallbackReadCount:   atomic.LoadUint64(&s.stats.fallbackReadCount),
			EventConnErrorCount: atomic.LoadUint64(&s.stats.eventConnErrorCount),
			QueueFullErrorCount: atomic.LoadUint64(&s.stats.queueFullErrorCount),
			ActiveStreamCount:   activeStreamCount,
		}, smm
}

// IsClient return the session whether is a client
func (s *Session) IsClient() bool { return s.isClient }

// ID return the a string to identify unique shmipc session in a process
func (s *Session) ID() string { return s.name + "_" + strconv.Itoa(s.sessionID) }
