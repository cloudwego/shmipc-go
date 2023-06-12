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
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//ListenCallback is server's asynchronous API
type ListenCallback interface {
	//OnNewStream was called when accept a new stream
	OnNewStream(s *Stream)
	//OnShutdown was called when the listener was stopped
	OnShutdown(reason string)
}

//ListenerConfig is the configuration of Listener
type ListenerConfig struct {
	*Config
	Network string //Only support unix or tcp
	//If Network is "tcp', the ListenPath is ip address and port, such as 0.0.0.0:6666(ipv4), [::]:6666 (ipv6)
	//If Network is "unix", the ListenPath is a file path, such as /your/socket/path/xx_shmipc.sock
	ListenPath string
}

//Listener listen socket and accept connection as shmipc server connection
type Listener struct {
	mu                 sync.Mutex
	dispatcher         dispatcher
	config             *ListenerConfig
	sessions           *sessions
	ln                 net.Listener
	logger             *logger
	callback           ListenCallback
	shutdownErrStr     string
	isClose            bool
	state              sessionSateType
	epoch              uint64
	hotRestartAckCount int
	unlinkOnClose      bool
}

//NewDefaultListenerConfig return the default Listener's config
func NewDefaultListenerConfig(listenPath string, network string) *ListenerConfig {
	return &ListenerConfig{
		Config:     DefaultConfig(),
		Network:    network,
		ListenPath: listenPath,
	}
}

//NewListener will try listen the ListenPath of the configuration, and return the Listener if no error happened.
func NewListener(callback ListenCallback, config *ListenerConfig) (*Listener, error) {
	if callback == nil {
		return nil, errors.New("ListenCallback couldn't be nil")
	}

	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("only support linux OS")
	}

	safeRemoveUdsFile(config.ListenPath)
	ln, err := net.Listen(config.Network, config.ListenPath)

	if err != nil {
		return nil, fmt.Errorf("create listener failed, reason%s", err.Error())
	}

	return &Listener{
		config:        config,
		ln:            ln,
		dispatcher:    defaultDispatcher,
		sessions:      newSessions(),
		logger:        newLogger("listener", nil),
		callback:      callback,
		unlinkOnClose: true,
	}, nil
}

// Close closes the listener.
// Any blocked Accept operations will be unblocked and return errors.
func (l *Listener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.isClose {
		return nil
	}
	l.isClose = true

	if l.shutdownErrStr != "" {
		l.callback.OnShutdown(l.shutdownErrStr)
	} else {
		l.callback.OnShutdown("close by Listener.Close()")
	}
	l.ln.Close()
	if l.config.Network == "unix" && l.unlinkOnClose {
		os.Remove(l.config.ListenPath)
	}
	l.sessions.closeAll()
	return nil
}

//Addr returns the listener's network address.
func (l *Listener) Addr() net.Addr {
	return l.ln.Addr()
}

//Accept doesn't work, whose existence just adapt to the net.Listener interface.
func (l *Listener) Accept() (net.Conn, error) {
	return nil, errors.New("not support now, just compact net.Listener interface")
}

//Run starting a loop to listen socket
func (l *Listener) Run() error {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			if nerr, ok := err.(net.Error); ok && nerr.Temporary() {
				continue
			}
			if strings.Contains(err.Error(), "too many open file") {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			l.shutdownErrStr = "accept failed,reason:" + err.Error()
			l.logger.errorf("run accept error %s", l.shutdownErrStr)
			l.Close()
			break
		}

		configCopy := *l.config.Config
		configCopy.listenCallback = &sessionCallback{l}
		session, err := newSession(&configCopy, conn, false)
		if err != nil {
			conn.Close()
			l.logger.warnf("new server session failed, reason" + err.Error())
			continue
		}
		session.listener = l
		l.sessions.add(session)
	}
	return nil
}

//HotRestart will do shmipc server hot restart
func (l *Listener) HotRestart(epoch uint64) error {
	l.logger.warnf("begin HotRestart epoch:%d", epoch)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state == hotRestartState {
		return ErrHotRestartInProgress
	}

	l.state = hotRestartState
	l.epoch = epoch

	l.sessions.sessionMu.Lock()
	defer l.sessions.sessionMu.Unlock()

	for session := range l.sessions.data {
		if !session.handshakeDone {
			return ErrInHandshakeStage
		}
		if session.state != defaultState {
			continue
		}
		if err := session.hotRestart(epoch, typeHotRestart); err != nil {
			session.logger.warnf("%s hotRestart epoch %d error %+v", session.name, epoch, err)
			l.state = defaultState
			return err
		}
		session.state = hotRestartState
		l.hotRestartAckCount++
	}

	go func() {
		l.checkHotRestart()
	}()

	return nil
}

//IsHotRestartDone return whether the Listener is under the hot restart state.
func (l *Listener) IsHotRestartDone() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.state != hotRestartState
}

//SetUnlinkOnClose sets whether unlink unix socket path when Listener was stopped
func (l *Listener) SetUnlinkOnClose(unlink bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.unlinkOnClose = unlink
	if lnUnix, ok := l.ln.(*net.UnixListener); ok {
		lnUnix.SetUnlinkOnClose(unlink)
	}
}

// since the hot restart process is establish connection operation
// so waiting timeout is set short
func (l *Listener) checkHotRestart() {
	timeout := time.NewTimer(hotRestartCheckTimeout)
	defer timeout.Stop()
	ticker := time.NewTicker(hotRestartCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.mu.Lock()
			if l.state != hotRestartState {
				l.mu.Unlock()
				return
			}

			if l.hotRestartAckCount == 0 {
				l.logger.warnf("[epoch:%d] checkHotRestart done", l.epoch)
				l.state = hotRestartDoneState
				l.sessions.onHotRestart(true)
				l.mu.Unlock()
				return
			}

			l.mu.Unlock()
		case <-timeout.C:
			l.logger.errorf("[epoch:%d] checkHotRestart timeout", l.epoch)
			l.resetState()
			l.sessions.onHotRestart(false)
			return
		}
	}
}

func (l *Listener) resetState() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.state = defaultState
	l.hotRestartAckCount = 0

	l.sessions.sessionMu.Lock()
	defer l.sessions.sessionMu.Unlock()

	for session := range l.sessions.data {
		session.state = defaultState
	}
}

type sessionCallback struct {
	listener *Listener
}

func (c *sessionCallback) OnNewStream(s *Stream) {
	c.listener.callback.OnNewStream(s)
}

func (c *sessionCallback) OnShutdown(reason string) {
	c.listener.logger.warnf("on session shutdown,reason:" + reason)
	c.listener.sessions.removeShutdownSession()
}

var _ ListenCallback = &sessionCallback{}

type sessions struct {
	sessionMu sync.Mutex
	data      map[*Session]struct{}
}

func newSessions() *sessions { return &sessions{data: make(map[*Session]struct{}, 8)} }

func (s *sessions) add(session *Session) {
	s.sessionMu.Lock()
	if s.data != nil {
		s.data[session] = struct{}{}
	} else {
		session.logger.warnf("listener is closed, session %s will not be add", session.name)
		session.Close()
	}
	s.sessionMu.Unlock()
}

func (s *sessions) removeShutdownSession() {
	s.sessionMu.Lock()
	for session := range s.data {
		if session.IsClosed() {
			delete(s.data, session)
		}
	}
	s.sessionMu.Unlock()
}

func (s *sessions) closeAll() {
	s.sessionMu.Lock()
	toCloseSessions := s.data
	s.data = nil
	s.sessionMu.Unlock()
	for session := range toCloseSessions {
		session.Close()
	}
}

func (s *sessions) onHotRestart(success bool) {
	s.sessionMu.Lock()
	for session := range s.data {
		if success {
			atomic.AddUint64(&session.stats.hotRestartSuccessCount, 1)
		} else {
			atomic.AddUint64(&session.stats.hotRestartErrorCount, 1)
		}
	}
	s.sessionMu.Unlock()
}
