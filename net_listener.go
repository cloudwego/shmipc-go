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
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const defaultBacklog = 4096 // backlog number is the stream to accept channel size

type listener struct {
	listener net.Listener // the raw listener

	sessions map[*Session]*sync.WaitGroup // all established sessions
	mu       sync.Mutex                   // lock sessions

	closed  uint32        // to mark if the raw listener is closed when accept returns error
	closeCh chan struct{} // to make select returns when closed
	backlog chan net.Conn // all accepted streams(will never be closed otherwise may send to closed channel)
}

// create listener and run background goroutines
func newListener(rawListener net.Listener, backlog int) *listener {
	listener := &listener{
		listener: rawListener,
		sessions: make(map[*Session]*sync.WaitGroup),
		backlog:  make(chan net.Conn, backlog),
		closeCh:  make(chan struct{}),
	}
	go listener.listenLoop()
	return listener
}

// accept connection from the raw listener in loop,
// spawn another goroutine to create session with the connection, save it, and then accept streams from the session.
func (l *listener) listenLoop() {
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			if atomic.LoadUint32(&l.closed) == 1 {
				internalLogger.infof("listener closed: %s", err)
				return
			}
			internalLogger.errorf("error when accept: %s", err)
			continue
		}
		internalLogger.info("receive a new incoming raw connection")
		go func() {
			session, err := Server(conn, DefaultConfig())
			if err != nil {
				internalLogger.errorf("error when create session: %s", err)
				return
			}
			internalLogger.info("new session created")

			l.mu.Lock()
			if atomic.LoadUint32(&l.closed) == 1 {
				l.mu.Unlock()
				_ = session.Close()
				internalLogger.infof("listener is closed and the session should be closed")
				return
			}

			// Here we maintain a ref counter for every session.
			// The listener holds 1 ref, and every stream holds 1 ref.
			// Only when listener closed and all stream closed, the session be terminated.
			wg := new(sync.WaitGroup)
			wg.Add(1)
			l.sessions[session] = wg
			l.mu.Unlock()

			go func() {
				wg.Wait()
				_ = session.Close()
				internalLogger.infof("wait group finished, session is closed")
			}()

			for {
				stream, err := session.AcceptStream()
				if err != nil {
					if err != ErrSessionShutdown {
						internalLogger.errorf("error when accept new stream: %s", err)
					}
					_ = session.Close()
					internalLogger.infof("session is closed early: %s", err)

					l.mu.Lock()
					if _, ok := l.sessions[session]; ok {
						delete(l.sessions, session)
						wg.Done()
					}
					l.mu.Unlock()
					return
				}
				internalLogger.info("accepted a new stream")
				conn := newStreamWrapper(stream, stream.LocalAddr(), stream.RemoteAddr(), wg)
				select {
				case <-l.closeCh:
					return
				case l.backlog <- conn:
				}
			}
		}()
	}
}

// accept gets connections from the backlog channel
func (l *listener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.backlog:
		return conn, nil
	case <-l.closeCh:
		return nil, errors.New("listener is closed")
	}
}

// When listen closed, all sessions should be closed which otherwise would leak.
// Because the underlying connection is closed, all streams will be closed too.
// Note: The close behaviour here may differs from a normal connection.
func (l *listener) Close() (err error) {
	// mark closed and close the listener
	swapped := atomic.CompareAndSwapUint32(&l.closed, 0, 1)
	err = l.listener.Close()
	// close the closeCh to make blocking call return
	if swapped {
		close(l.closeCh)
	}
	// closed and clear sessions to avoid leaking
	l.mu.Lock()
	for _, wg := range l.sessions {
		wg.Done()
	}
	l.sessions = map[*Session]*sync.WaitGroup{}
	l.mu.Unlock()
	return
}

// Addr is forwarded to the raw listener
func (l *listener) Addr() net.Addr {
	return l.listener.Addr()
}

// Listen create listener with default backlog size(4096)
// shmIPCAddress is uds address used as underlying connection, the returned value is net.Listener
// Remember close the listener if it is created successfully, or goroutine may leak
// Should I use Listen?
//  If you want the best performance, you should use low level API(not this one) to marshal and unmarshal manually,
//  which can achieve better batch results.
//  If you just care about the compatibility, you can use this high level API. For example, you can hardly change grpc
//  and protobuf, then you can use this listener to make it compatible with a little bit improved performance.
func Listen(shmIPCAddress string) (net.Listener, error) {
	return ListenWithBacklog(shmIPCAddress, defaultBacklog)
}

// ListenWithBacklog create listener with given backlog size
// shmIPCAddress is uds address used as underlying connection, the returned value is net.Listener
// Remember close the listener if it is created successfully, or goroutine may leak
// Should I use ListenWithBacklog?
//  If you want the best performance, you should use low level API(not this one) to marshal and unmarshal manually,
//  which can achieve better batch results.
//  If you just care about the compatibility, you can use this high level API. For example, you can hardly change grpc
//  and protobuf, then you can use this listener to make it compatible with a little bit improved performance.
func ListenWithBacklog(shmIPCAddress string, backlog int) (net.Listener, error) {
	rawListener, err := net.Listen("unix", shmIPCAddress)
	if err != nil {
		return nil, err
	}
	return newListener(rawListener, backlog), nil
}

// A wrapper around a stream to impl net.Conn
func newStreamWrapper(stream *Stream, localAddr, remoteAddr net.Addr, wg *sync.WaitGroup) net.Conn {
	wg.Add(1)
	return &streamWrapper{stream: stream, localAddr: localAddr, remoteAddr: remoteAddr, wg: wg}
}

type streamWrapper struct {
	stream     *Stream
	localAddr  net.Addr
	remoteAddr net.Addr

	closed uint32
	wg     *sync.WaitGroup
}

func (s *streamWrapper) Read(b []byte) (n int, err error) {
	return s.stream.copyRead(b)
}

func (s *streamWrapper) Write(b []byte) (n int, err error) {
	return s.stream.copyWriteAndFlush(b)
}

func (s *streamWrapper) Close() error {
	if atomic.CompareAndSwapUint32(&s.closed, 0, 1) {
		_ = s.stream.Close()
		s.wg.Done()
	}
	return nil
}

func (s *streamWrapper) LocalAddr() net.Addr {
	return s.localAddr
}

func (s *streamWrapper) RemoteAddr() net.Addr {
	return s.remoteAddr
}

func (s *streamWrapper) SetDeadline(t time.Time) error {
	return s.stream.SetDeadline(t)
}

func (s *streamWrapper) SetReadDeadline(t time.Time) error {
	return s.stream.SetReadDeadline(t)
}

func (s *streamWrapper) SetWriteDeadline(t time.Time) error {
	return s.stream.SetWriteDeadline(t)
}
