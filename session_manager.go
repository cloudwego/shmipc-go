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
	"context"
	"errors"
	"math/rand"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	sessionRoundRobinThreshold = 32
)

var (
	sessionManagerHandlers = []sessionManagerHandler{
		typeHotRestart: handleSessionManagerHotRestart,
	}
)

type sessionManagerHandler func(manager *SessionManager, params interface{})

type sessionManagerHotRestartParams struct {
	epoch   uint64
	session *Session
}

// SessionManager will start multi Session with the peer process.
// when peer process was crashed or the underlying connection was closed, SessionManager could retry connect.
// and SessionManager could cooperate with peer process to finish hot restart.
type SessionManager struct {
	ctx        context.Context
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
	config     *SessionManagerConfig
	pools      []*streamPool
	// holds the stream pool prior to hot restart. Normally, it is closed by server
	reservePools map[int]*streamPool
	count        uint64
	epoch        uint64
	randID       uint64
	state        sessionSateType
	sync.RWMutex
}

// SessionManagerConfig is the configuration of SessionManager
type SessionManagerConfig struct {
	*Config
	UnixPath string //Deprecated , please use Network and Address.
	Network  string //tcp or unix
	Address  string //tcp address or unix domain socket path

	//SessionNum is similar to concurrency.
	//A session have dependent io queue, dependent tcp/unix connection.
	//we recommend the value equal peer process' thread count, and every thread keep a session.
	//if the peer process written by golang, recommend SessionNum = cpu cores / 4
	SessionNum int
	//Max number of stream per session's stream pool
	MaxStreamNum int
	//The idle time to close a stream
	StreamMaxIdleTime time.Duration
}

type streamPool struct {
	sync.Mutex
	session  atomic.Value
	streams  []*Stream
	head     uint64
	tail     uint64
	capacity uint32
}

var (
	globalSM *SessionManager
	smMux    sync.Mutex
)

// GlobalSessionManager return a global SessionManager. return nil if global SessionManager hadn't initialized
func GlobalSessionManager() *SessionManager {
	smMux.Lock()
	defer smMux.Unlock()
	return globalSM
}

// DefaultSessionManagerConfig return the default SessionManager's configuration
func DefaultSessionManagerConfig() *SessionManagerConfig {
	return &SessionManagerConfig{
		Config:            DefaultConfig(),
		Address:           "",
		SessionNum:        1,
		MaxStreamNum:      4096,
		StreamMaxIdleTime: time.Second * 30,
	}
}

// InitGlobalSessionManager initializes a global SessionManager and could use in every where in process
func InitGlobalSessionManager(config *SessionManagerConfig) (*SessionManager, error) {
	smMux.Lock()
	defer smMux.Unlock()
	if globalSM != nil {
		return globalSM, nil
	}

	sm, err := NewSessionManager(config)
	if err != nil {
		return nil, err
	}
	globalSM = sm
	return globalSM, nil
}

// NewSessionManager return a SessionManager with giving configuration
func NewSessionManager(config *SessionManagerConfig) (*SessionManager, error) {
	sm := &SessionManager{
		config: config,
		pools:  make([]*streamPool, 0, config.SessionNum),
		ctx:    context.Background(),
	}
	for i := 0; i < config.SessionNum; i++ {
		session, err := newClientSession(i, 0, 0, config)
		if err != nil {
			for k := 0; k < len(sm.pools); k++ {
				sm.pools[k].close()
			}
			return nil, err
		}
		session.manager = sm
		p := newStreamPool(uint32(config.MaxStreamNum))
		p.session.Store(session)
		sm.pools = append(sm.pools, p)
	}
	sm.background()
	return sm, nil
}

func newStreamPool(poolCapacity uint32) *streamPool {
	return &streamPool{streams: make([]*Stream, poolCapacity), capacity: poolCapacity}
}

// Close will shutdown the SessionManager's background goroutine and close all stream in stream pool
func (sm *SessionManager) Close() error {
	sm.cancelFunc()
	sm.wg.Wait()
	for i := 0; i < len(sm.pools); i++ {
		sm.pools[i].close()
	}
	return nil
}

// GetStream return a shmipc's Stream from stream pool.
// Every stream should explicitly call PutBack() to return it to SessionManager for next time using,
// otherwise it will cause resource leak.
func (sm *SessionManager) GetStream() (*Stream, error) {
	i := (atomic.AddUint64(&sm.count, 1) / sessionRoundRobinThreshold) % uint64(len(sm.pools))
	return sm.pools[i].getOrOpenStream()
}

// PutBack is used to return unused stream to stream pool for next time using.
func (sm *SessionManager) PutBack(stream *Stream) {
	if stream != nil && stream.pool != nil {
		stream.pool.putOrCloseStream(stream)
	}
}

func (sm *SessionManager) background() {
	sm.ctx, sm.cancelFunc = context.WithCancel(sm.ctx)
	sm.wg.Add(len(sm.pools))
	for i := 0; i < len(sm.pools); i++ {
		go func(id int) {
			defer sm.wg.Done()
			for {
				sm.RLock()
				if sm.state == hotRestartState {
					sm.RUnlock()
					time.Sleep(500 * time.Millisecond)
					continue
				}
				pool := sm.pools[id]
				sm.RUnlock()

				select {
				case <-pool.Session().CloseChan():
					sm.RLock()
					// in hotrestart state break this select and wait for hotrestar done
					if sm.state == hotRestartState {
						sm.RUnlock()
						break
					}
					sm.RUnlock()

					pool.close()
					for {
						if sm.config.rebuildInterval == 0 {
							sm.config.rebuildInterval = sessionRebuildInterval
						}
						rebuildTimer := time.NewTimer(sm.config.rebuildInterval)
						select {
						case <-sm.ctx.Done():
							return
						case <-rebuildTimer.C:
						}
						sm.Lock()
						// after rebuildTimer, check if Session.CloseChan caused by hotrestart,
						// pool.epochId != sm.pools[id].epochId represent the pools has been replaced by the new epoch pools,
						// this time Session.CloseChan will not rebuild session.
						sessionHadChangedByHotrestart := sm.pools[id].Session().epochID != pool.Session().epochID
						if sessionHadChangedByHotrestart {
							sm.Unlock()
							break
						}
						// both new epoch pools and old epoch pools which has not been replaced will use new epochId to rebuild session
						session, err := newClientSession(id, sm.epoch, sm.randID, sm.config)
						sm.Unlock()
						if err != nil {
							internalLogger.errorf("rebuild stream pool's sessionID %d %s failed, reason:%s. retry after %s", id, pool.Session().name, err.Error(), sessionRebuildInterval.String())
							continue
						}
						session.manager = sm
						pool.session.Store(session)
						internalLogger.warnf("rebuild stream pool's sessionID %d %s success", id, pool.Session().name)
						break
					}
				case <-sm.ctx.Done():
					return
				}
			}
		}(i)
	}
}

func (sm *SessionManager) checkHotRestart() {
	timeout := time.NewTimer(hotRestartCheckTimeout)
	defer timeout.Stop()
	ticker := time.NewTicker(hotRestartCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sm.Lock()
			if len(sm.reservePools) == len(sm.pools) {
				sm.state = defaultState
				for _, p := range sm.reservePools {
					if err := p.Session().hotRestart(sm.epoch, typeHotRestartAck); err != nil {
						internalLogger.warnf("SessionManager [epoch:%d] ack hotRestart error %+v", sm.epoch, err)
					}
				}
				internalLogger.warnf("SessionManager [epoch:%d] checkHotRestart done", sm.epoch)
				sm.Unlock()
				return
			}
			sm.Unlock()
		case <-timeout.C:
			sm.Lock()
			sm.state = defaultState
			for _, p := range sm.reservePools {
				p.close()
			}
			sm.reservePools = nil
			internalLogger.errorf("SessionManager [epoch:%d] checkHotRestart timeout", sm.epoch)
			sm.Unlock()
			return
		}
	}
}

func (sm *SessionManager) handleEvent(event eventType, param interface{}) {
	if int(event) >= len(sessionManagerHandlers) || sessionManagerHandlers[event] == nil {
		internalLogger.errorf("SessionManager handle unsupported event %d %s", event, event.String())
		return
	}

	sessionManagerHandlers[event](sm, param)
}

func handleSessionManagerHotRestart(sm *SessionManager, params interface{}) {
	defer func() {
		if err := recover(); err != nil {
			internalLogger.warnf("SessionManager [epoch:%d] handleSessionManagerHotRestart params %+v recover error %+v", sm.epoch, params, err)
		}
	}()

	sm.Lock()
	defer sm.Unlock()

	hParams := params.(*sessionManagerHotRestartParams)
	if sm.state == hotRestartState && sm.epoch != hParams.epoch {
		internalLogger.warnf("SessionManager [epoch:%d] handleSessionManagerHotRestart get invalid params %+v ", sm.epoch, params)
		return
	}

	// the first session to receive hot restart event will clear reservePools and set state
	if sm.state != hotRestartState {
		sm.state = hotRestartState
		sm.epoch = hParams.epoch
		sm.randID = uint64(time.Now().UnixNano()) + rand.Uint64()
		for _, p := range sm.reservePools {
			p.close()
		}
		sm.reservePools = nil

		go func() {
			sm.checkHotRestart()
		}()
	}

	if sm.reservePools[hParams.session.sessionID] != nil {
		internalLogger.warnf("SessionManager [epoch:%d] handleSessionManagerHotRestart get repeat sessionID:%d ", sm.epoch, hParams.session.sessionID)
		return
	}

	newSession, err := newClientSession(hParams.session.sessionID, sm.epoch, sm.randID, sm.config)
	if err != nil {
		internalLogger.warnf("SessionManager [epoch:%d] handleSessionManagerHotRestart newClientSession sessionID:%d error %+v", sm.epoch, hParams.session.sessionID, err)
		return
	}
	newSession.manager = sm
	p := newStreamPool(uint32(sm.config.MaxStreamNum))
	p.session.Store(newSession)
	if sm.pools[hParams.session.sessionID] == nil {
		internalLogger.warnf("SessionManager [epoch:%d] handleSessionManagerHotRestart pools sessionID:%d absence", sm.epoch, hParams.session.sessionID)
		return
	}
	if len(sm.reservePools) == 0 {
		sm.reservePools = make(map[int]*streamPool, 0)
	}
	sm.reservePools[hParams.session.sessionID] = sm.pools[hParams.session.sessionID]
	sm.pools[hParams.session.sessionID] = p
}

func newClientSession(sessionID int, epochID, randID uint64, config *SessionManagerConfig) (*Session, error) {
	var conn net.Conn
	var err error
	if config.UnixPath != "" {
		conn, err = net.DialTimeout("unix", config.UnixPath, config.ConnectionWriteTimeout)
	} else {
		conn, err = net.DialTimeout(config.Network, config.Address, config.ConnectionWriteTimeout)
	}
	if err != nil {
		return nil, err
	}

	conf := *config.Config
	conf.ShareMemoryPathPrefix += "_" + strconv.Itoa(os.Getpid())
	if config.MemMapType == MemMapTypeDevShmFile {
		if len(conf.ShareMemoryPathPrefix)+epochInfoMaxLen+queueInfoMaxLen > fileNameMaxLen {
			return nil, ErrFileNameTooLong
		}
	}
	if epochID > 0 {
		conf.ShareMemoryPathPrefix += "_epoch_" + strconv.FormatUint(epochID, 10) + "_" + strconv.FormatUint(randID, 10)
	}
	if conf.ShareMemoryPathPrefix != "" {
		conf.QueuePath = conf.ShareMemoryPathPrefix + "_queue_" + strconv.Itoa(sessionID)
	}

	session, err := newSession(&conf, conn, true)
	if err != nil {
		return nil, err
	}

	session.sessionID = sessionID
	session.epochID = epochID
	session.randID = randID
	return session, nil
}

func (p *streamPool) Session() *Session {
	load := p.session.Load()
	if load != nil {
		return load.(*Session)
	}
	return nil
}

func (p *streamPool) close() {
	if p.session.Load() != nil {
		p.session.Load().(*Session).Close()
	}
	for {
		s := p.pop()
		if s == nil {
			break
		}
		s.Close()
	}
}

func (p *streamPool) getOrOpenStream() (*Stream, error) {
	if !p.Session().IsHealthy() {
		return nil, ErrSessionUnhealthy
	}
	for stream := p.pop(); stream != nil; stream = p.pop() {
		if !stream.Session().IsClosed() {
			// ensure return an open stream
			if stream.IsOpen() {
				return stream, nil
			}
		}
	}

	stream, err := p.Session().OpenStream()
	if err != nil {
		return nil, err
	}
	stream.pool = p
	return stream, nil
}

func (p *streamPool) putOrCloseStream(s *Stream) {
	// if the stream is in fallback state, we will not reuse it
	if s.inFallbackState {
		s.Close()
		return
	}

	if err := s.reset(); err == nil {
		s.ReleaseReadAndReuse()
		if p.push(s) != nil {
			s.Close()
		}
	} else {
		s.Close()
	}
}

func (p *streamPool) pop() *Stream {
	p.Lock()
	if p.tail > p.head {
		s := p.streams[p.head%uint64(p.capacity)]
		p.head++
		p.Unlock()
		return s
	}
	p.Unlock()
	return nil
}

var errPoolFull = errors.New("stream pool is full")

func (p *streamPool) push(s *Stream) error {
	p.Lock()
	if p.tail-p.head < uint64(p.capacity) {
		p.streams[p.tail%uint64(p.capacity)] = s
		p.tail++
		p.Unlock()
		return nil
	}
	p.Unlock()
	return errPoolFull
}
