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
	"net"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/bytedance/gopkg/util/gopool"
)

type streamState uint32
type streamCallbackState uint32

var (
	_ net.Conn = &Stream{}
)

const (
	streamOpened streamState = iota
	streamClosed
	streamHalfClosed
)

const (
	callbackDefault streamCallbackState = iota
	callbackWaitExit
)

// StreamCallbacks provide asynchronous programming mode for improving performance in some scenarios
type StreamCallbacks interface {
	//OnData() will be call by new goroutine, When the stream receive new data
	OnData(reader BufferReader)
	//OnLocalClose was called when the stream was closed by local process
	OnLocalClose()
	//OnRemoteClose was called when the stream was closed by remote process
	OnRemoteClose()
}

// Stream is used to represent a logical stream
// within a session.
type Stream struct {
	id    uint32
	state uint32

	session *Session
	pool    *streamPool

	recvBuf     *linkedBuffer
	sendBuf     *linkedBuffer
	pendingData *pendingData

	recvNotifyCh    chan struct{}
	closeNotifyCh   chan struct{}
	closeNotifyOnce sync.Once

	readDeadline  time.Time
	writeDeadline time.Time
	readTimer     *time.Timer

	// if inFallbackState is set to true, sending should use uds
	inFallbackState bool

	// using by asynchronous mode
	callback          *StreamCallbacks
	callbackInProcess uint32
	asyncGoroutineWg  sync.WaitGroup
	// when callback.OnData inner call stream.Close set this field
	// after OnData return check state and call stream.Close again
	callbackCloseState uint32
}

// newStream is used to construct a new stream within
// a given session for an ID
func newStream(session *Session, id uint32) *Stream {
	s := &Stream{
		id:            id,
		session:       session,
		state:         uint32(streamOpened),
		recvBuf:       newEmptyLinkedBuffer(session.bufferManager),
		sendBuf:       newEmptyLinkedBuffer(session.bufferManager),
		pendingData:   new(pendingData),
		recvNotifyCh:  make(chan struct{}, 1),
		closeNotifyCh: make(chan struct{}),
	}
	s.recvBuf.bindStream(s)
	s.sendBuf.bindStream(s)
	s.pendingData.stream = s
	return s
}

//SetCallbacks used to set the StreamCallbacks.
//Notice: It was just called only once, or return the error named ErrStreamCallbackHadExisted.
func (s *Stream) SetCallbacks(callback StreamCallbacks) error {
	if s.getCallbacks() != nil {
		return ErrStreamCallbackHadExisted
	}
	s.setCallbacks(callback)
	atomic.StoreUint32(&s.callbackInProcess, 0)
	return nil
}

// Session returns the associated stream session
func (s *Stream) Session() *Session {
	return s.session
}

// StreamID returns the ID of this stream
func (s *Stream) StreamID() uint32 {
	return s.id
}

/* return underlying read buffer, whose'size >= minSize.
* if current's size is not enough, which will block until the read buffer's size greater than minSize.
 */
func (s *Stream) readMore(minSize int) (err error) {
	s.pendingData.moveTo(s.recvBuf)
	recvLen := s.recvBuf.Len()
	if recvLen >= minSize {
		return nil
	}

	if recvLen == 0 && atomic.LoadUint32(&s.state) != uint32(streamOpened) {
		return ErrEndOfStream
	}

	var timeoutCh <-chan time.Time
	deadline := s.readDeadline
	if !deadline.IsZero() {
		if s.readTimer == nil {
			s.readTimer = time.NewTimer(time.Until(deadline))
		} else {
			s.readTimer.Reset(time.Until(deadline))
		}
		timeoutCh = s.readTimer.C
	}

	defer func() {
		if s.readTimer != nil && !s.readTimer.Stop() {
			select {
			case <-s.readTimer.C:
			default:
			}
		}
	}()
	for {
		select {
		case <-s.recvNotifyCh:
			s.pendingData.moveTo(s.recvBuf)
			if s.recvBuf.Len() >= minSize {
				return nil
			}
		case <-s.closeNotifyCh:
			s.pendingData.moveTo(s.recvBuf)
			if s.recvBuf.Len() >= minSize {
				return nil
			}
			if atomic.LoadUint32(&s.state) == uint32(streamHalfClosed) {
				return ErrEndOfStream
			}
			return ErrStreamClosed
		case <-timeoutCh:
			return ErrTimeout
		}
	}
}

// BufferWriter return the buffer to write, and after wrote done you should call Stream.Flush(endStream).
func (s *Stream) BufferWriter() BufferWriter {
	return s.sendBuf
}

// BufferReader return the buffer to read.
func (s *Stream) BufferReader() BufferReader {
	return s.recvBuf
}

// Flush the buffered stream data to peer. If the endStream is true,
// it mean that this stream hadn't send any data to peer after flush, and the peer could close stream after receive data
func (s *Stream) Flush(endStream bool) error {
	if s.sendBuf.Len() == 0 {
		return nil
	}
	atomic.AddUint64(&s.session.stats.outFlowBytes, uint64(s.sendBuf.Len()))
	state := atomic.LoadUint32(&s.state)
	if state != uint32(streamOpened) {
		s.sendBuf.recycle()
		return ErrStreamClosed
	}
	s.sendBuf.done(endStream)
	defer s.sendBuf.clean()
	// Once we send data using uds, for this stream we will always use uds later to avoid unordering
	if !s.sendBuf.isFromShareMemory() {
		s.inFallbackState = true
	}
	if s.inFallbackState {
		return s.writeFallback(s.state, ErrNoMoreBuffer)
	}
	buf := s.sendBuf
	// s.session.logger.tracef("stream:%d send buf, size:%d cap:%d offset:%d", s.id, buf.Len(), buf.Cap(),
	// buf.rootBufOffset())
	err := s.session.sendQueue().put(queueElement{
		seqID:          s.id,
		offsetInShmBuf: buf.rootBufOffset(),
		status:         state,
	})
	if err == ErrQueueFull {
		atomic.AddUint64(&s.session.stats.queueFullErrorCount, 1)
		var writeDeadlineCh <-chan time.Time
		if !s.writeDeadline.IsZero() {
			writeDeadlineCh = time.NewTimer(s.writeDeadline.Sub(time.Now())).C
		}
		//retry 10 times.
		for i := 0; err == ErrQueueFull && i < 10; i++ {
			retryTimer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-retryTimer.C:
				err = s.session.sendQueue().put(queueElement{
					seqID:          s.id,
					offsetInShmBuf: buf.rootBufOffset(),
					status:         state,
				})
			case <-writeDeadlineCh:
				err = ErrTimeout
			case <-s.closeNotifyCh:
				err = ErrStreamClosed
			}
		}
	}
	if err != nil {
		buf.recycle()
		return err
	}
	return s.session.wakeUpPeer()
}

func (s *Stream) writeFallback(streamStatus uint32, err error) error {
	s.session.logger.warnf("stream fallback seqID:%d len:%d reason:%s, sendBuf.isFromShareMemory: %t",
		s.id, s.sendBuf.Len(), err.Error(), s.sendBuf.isFromShareMemory())
	var event fallbackDataEvent
	event.encode(len(event)+s.sendBuf.Len(), s.session.communicationVersion, s.id, streamStatus)
	data := make([]byte, 0, s.sendBuf.Len()+len(event))
	underlyingSlices := s.sendBuf.underlyingData()
	data = append(data, event[:]...)
	for i := range underlyingSlices {
		data = append(data, underlyingSlices[i]...)
	}
	s.sendBuf.recycle()
	s.session.openCircuitBreaker()
	atomic.AddUint64(&s.session.stats.fallbackWriteCount, 1)
	return s.session.waitForSend(nil, data)
}

//Close used to close the stream, which maybe block if there is StreamCallbacks running.
//if a stream was leaked, it's also mean that some share memory was leaked.
func (s *Stream) Close() error {
	if s.getCallbacks() != nil {
		atomic.StoreUint32(&s.callbackCloseState, uint32(callbackWaitExit))
	}
	if atomic.LoadUint32(&s.callbackInProcess) == 1 {
		atomic.CompareAndSwapUint32(&s.state, uint32(streamOpened), uint32(streamHalfClosed))
		return nil
	}

	return s.close()
}

// close the stream. after close stream, any operation will return ErrStreamClosed.
// unread data will be drained and released.
func (s *Stream) close() error {
	oldState := atomic.LoadUint32(&s.state)
	if oldState == uint32(streamClosed) {
		return nil
	}

	if atomic.CompareAndSwapUint32(&s.state, oldState, uint32(streamClosed)) {
		if s.getCallbacks() != nil {
			s.asyncGoroutineWg.Wait()
		}
		s.clean()
		if oldState == uint32(streamOpened) {
			s.safeCloseNotify()
			callback := s.getCallbacks()
			if callback != nil {
				if atomic.LoadUint32(&s.session.shutdown) == 1 {
					callback.OnRemoteClose()
				} else {
					callback.OnLocalClose()
				}
			}
			if atomic.LoadUint32(&s.session.shutdown) == 1 {
				return nil
			}
			// notify peer
			err := s.session.sendQueue().put(queueElement{seqID: s.id, status: uint32(streamClosed)})
			if err != nil {
				atomic.AddUint64(&s.session.stats.queueFullErrorCount, 1)
				// notify fallback
				var streamCloseEvent [headerSize + 4]byte
				header(streamCloseEvent[:]).encode(headerSize+4, s.session.communicationVersion, typeStreamClose)
				binary.BigEndian.PutUint32(streamCloseEvent[headerSize:], s.id)
				return s.session.waitForSend(nil, streamCloseEvent[:])
			}
			return s.session.wakeUpPeer()
		}
	}
	return nil
}

func (s *Stream) clean() {
	s.session.onStreamClose(s.id, streamState(atomic.LoadUint32(&s.state)))
	s.pendingData.clear()
	s.recvBuf.recycle()
	s.sendBuf.recycle()
}

func (s *Stream) halfClose() {
	if atomic.CompareAndSwapUint32(&s.state, uint32(streamOpened), uint32(streamHalfClosed)) {
		s.safeCloseNotify()
		callback := s.getCallbacks()
		if callback != nil {
			callback.OnRemoteClose()
		}
	}
}

// clean the stream's all status for reusing.
func (s *Stream) reset() error {
	if atomic.LoadUint32(&s.state) != uint32(streamOpened) {
		return ErrStreamClosed
	}
	// return error if has any unread data
	unreadSize := s.recvBuf.Len()
	if unreadSize > 0 {
		return fmt.Errorf("stream had unread data, size:%d ", unreadSize)
	}

	s.pendingData.Lock()
	if len(s.pendingData.unread) > 0 {
		s.pendingData.Unlock()
		return fmt.Errorf("stream had unread pending data, unread slice len:%d ", len(s.pendingData.unread))
	}
	s.pendingData.Unlock()

	s.readDeadline = zeroTime
	s.writeDeadline = zeroTime
	s.inFallbackState = false

	// drain readCh
	select {
	case <-s.recvNotifyCh:
	default:
	}
	s.setCallbacks(nil)

	return nil
}

// ReleaseReadAndReuse used to Release the data previous read by Stream.BufferReader(),
// and reuse the last share memory buffer slice of read buffer for next write by Stream.BufferWriter()
func (s *Stream) ReleaseReadAndReuse() {
	s.recvBuf.releasePreviousReadAndReserve()
	if s.recvBuf.len == 0 && s.recvBuf.sliceList.size() == 1 {
		s.recvBuf, s.sendBuf = s.sendBuf, s.recvBuf
	}
}

// fillDataToReadBuffer is used to handle a data frame
func (s *Stream) fillDataToReadBuffer(buf bufferSliceWrapper) error {
	s.pendingData.add(buf)
	//stream had closed, which maybe closed by user due to timeout.
	if atomic.LoadUint32(&s.state) == uint32(streamClosed) {
		s.pendingData.clear()
		s.recvBuf.recycle()
		return nil
	}
	// Unblock any readers
	asyncNotify(s.recvNotifyCh)
	callback := s.getCallbacks()
	if callback != nil {
		// callback OnData maybe block, make sure OnData called once and chan recvNotifyCh be notified
		if atomic.CompareAndSwapUint32(&s.callbackInProcess, 0, 1) {
			s.asyncGoroutineWg.Add(1)
			gopool.Go(func() {
				for {
					s.pendingData.moveTo(s.recvBuf)
					for s.IsOpen() && s.recvBuf.Len() > 0 {
						callback.OnData(s.recvBuf)
						s.pendingData.moveTo(s.recvBuf)
					}

					atomic.StoreUint32(&s.callbackInProcess, 0)
					if atomic.LoadUint32(&s.callbackCloseState) == uint32(callbackWaitExit) {
						s.asyncGoroutineWg.Done()
						s.close()
						return
					}

					if !(len(s.pendingData.unread) > 0 && atomic.CompareAndSwapUint32(&s.callbackInProcess, 0, 1)) {
						break
					}
				}
				s.asyncGoroutineWg.Done()
			})
		}
	}

	return nil
}

// SetDeadline sets the read timeout for blocked and future Read calls.
func (s *Stream) SetDeadline(t time.Time) error {
	s.readDeadline = t
	s.writeDeadline = t
	return nil
}

// SetReadDeadline is the same as net.Conn
func (s *Stream) SetReadDeadline(t time.Time) error {
	s.readDeadline = t
	return nil
}

// SetWriteDeadline is the same as net.Conn
func (s *Stream) SetWriteDeadline(t time.Time) error {
	s.writeDeadline = t
	return nil
}

//IsOpen return whether the stream is open
func (s *Stream) IsOpen() bool {
	return atomic.LoadUint32(&s.state) == uint32(streamOpened)
}

func (s *Stream) safeCloseNotify() {
	s.closeNotifyOnce.Do(func() { close(s.closeNotifyCh) })
}

type bufferSliceWrapper struct {
	fallbackSlice *bufferSlice
	offset        uint32
}

type pendingData struct {
	sync.Mutex
	stream *Stream
	unread []bufferSliceWrapper
}

func (r *pendingData) moveToWithoutLock(toBuf *linkedBuffer) {
	if len(r.unread) == 0 {
		return
	}
	preLen := toBuf.Len()
	for i := range r.unread {
		if r.unread[i].fallbackSlice != nil {
			toBuf.appendBufferSlice(r.unread[i].fallbackSlice)
			r.stream.inFallbackState = true
			continue
		}
		for offset := r.unread[i].offset; ; {
			slice, err := r.stream.session.bufferManager.readBufferSlice(offset)
			if err != nil {
				// it means that something bug about protocol occurred, underlying connection will be closed.
				r.stream.session.logger.error("readBufferSlice error" + err.Error())
				break
			}
			toBuf.appendBufferSlice(slice)
			if !slice.hasNext() {
				break
			}
			offset = slice.nextBufferOffset()
		}
	}
	atomic.AddUint64(&r.stream.session.stats.inFlowBytes, uint64(toBuf.Len()-preLen))
	r.unread = r.unread[:0]
	return
}

func (r *pendingData) moveTo(toBuf *linkedBuffer) {
	r.Lock()
	r.moveToWithoutLock(toBuf)
	r.Unlock()
}

func (r *pendingData) add(w bufferSliceWrapper) {
	r.Lock()
	r.unread = append(r.unread, w)
	r.Unlock()
}

func (r *pendingData) clear() {
	r.Lock()
	if len(r.unread) > 0 {
		for i := range r.unread {
			if r.unread[i].fallbackSlice != nil {
				putBackBufferSlice(r.unread[i].fallbackSlice)
				continue
			}
			slice, err := r.stream.session.bufferManager.readBufferSlice(r.unread[i].offset)
			if err != nil {
				r.stream.session.logger.error("readBufferSlice failed:" + err.Error())
				break
			}
			r.stream.session.bufferManager.recycleBuffers(slice)
		}
		r.unread = r.unread[:0]
	}
	r.Unlock()
	return
}

func (s *Stream) setCallbacks(sc StreamCallbacks) {
	atomic.StorePointer((*unsafe.Pointer)(unsafe.Pointer(&s.callback)), unsafe.Pointer(&sc))
}

func (s *Stream) getCallbacks() StreamCallbacks {
	callback := atomic.LoadPointer((*unsafe.Pointer)(unsafe.Pointer(&s.callback)))
	if callback != nil {
		return *(*StreamCallbacks)(callback)
	}
	return nil
}

//Low performance api, it just adapt to the interface net.Conn, which will copy data from read buffer to `p`
//please use BufferReader() API to implement zero copy read
func (s *Stream) Read(p []byte) (int, error) {
	return s.copyRead(p)
}

//Low performance api, it just adapt to the interface net.Conn, which will do copy data from `p` to write buffer
//please use BufferWriter() API to implement zero copy write
func (s *Stream) Write(p []byte) (int, error) {
	return s.copyWriteAndFlush(p)
}

func (s *Stream) copyRead(p []byte) (int, error) {
	return s.recvBuf.read(p)
}

func (s *Stream) copyWriteAndFlush(p []byte) (int, error) {
	return s.sendBuf.copyWriteAndFlush(p)
}

// LocalAddr returns the local address
func (s *Stream) LocalAddr() net.Addr {
	return s.session.netConn.LocalAddr()
}

// RemoteAddr returns the remote address
func (s *Stream) RemoteAddr() net.Addr {
	return s.session.netConn.RemoteAddr()
}
