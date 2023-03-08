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

// Monitor could emit some metrics with periodically
type Monitor interface {
	// OnEmitSessionMetrics was called by Session with periodically.
	OnEmitSessionMetrics(PerformanceMetrics, StabilityMetrics, ShareMemoryMetrics, *Session)
	// flush metrics
	Flush() error
}

type stats struct {
	allocShmErrorCount     uint64
	fallbackWriteCount     uint64
	fallbackReadCount      uint64
	eventConnErrorCount    uint64
	queueFullErrorCount    uint64
	recvPollingEventCount  uint64
	sendPollingEventCount  uint64
	outFlowBytes           uint64
	inFlowBytes            uint64
	hotRestartSuccessCount uint64
	hotRestartErrorCount   uint64
}

//PerformanceMetrics is the metrics about performance
type PerformanceMetrics struct {
	ReceiveSyncEventCount uint64 //the SyncEvent count that session had received
	SendSyncEventCount    uint64 //the SyncEvent count that session had sent
	OutFlowBytes          uint64 //the out flow in bytes that session had sent
	InFlowBytes           uint64 //the in flow in bytes that session had receive
	SendQueueCount        uint64 //the pending count of send queue
	ReceiveQueueCount     uint64 //the pending count of receive queue
}

//StabilityMetrics is the metrics about stability
type StabilityMetrics struct {
	AllocShmErrorCount uint64 //the error count of allocating share memory
	FallbackWriteCount uint64 //the count of the fallback data write to unix/tcp connection
	FallbackReadCount  uint64 //the error count of receiving fallback data from unix/tcp connection every period

	//the error count of unix/tcp connection
	//which usually happened in that the peer's process exit(crashed or other reason)
	EventConnErrorCount uint64

	//the error count due to the IO-Queue(SendQueue or ReceiveQueue) is full
	//which usually happened in that the peer was busy
	QueueFullErrorCount uint64

	//current all active stream count
	ActiveStreamCount uint64

	//the successful count of hot restart
	HotRestartSuccessCount uint64
	//the failed count of hot restart
	HotRestartErrorCount uint64
}

//ShareMemoryMetrics is the metrics about share memory's status
type ShareMemoryMetrics struct {
	CapacityOfShareMemoryInBytes uint64 //capacity of all share memory
	AllInUsedShareMemoryInBytes  uint64 //current in-used share memory
}
