
# Shmipc

English | [中文](README_CN.md)

## Introduction

Shmipc is a high performance inter-process communication library developed by ByteDance.
It is built on Linux's shared memory technology and uses unix or tcp connection to do process synchronization and finally implements zero copy communication across inter-processes. 
In IO-intensive or large-package scenarios, it has better performance.

## Features

### Zero copy

In an industrial production environment, the unix domain socket and tcp loopback are often used in inter-process communication.The read operation or the write operation need copy data between user space buffer and kernel space buffer.But shmipc directly store data to the share memory, so no copy compared to the former.

### batch IO

An IO queue was mapped to share memory, which describe the metadata of communication data.
so that a process could put many request to the IO queue, and other process  could handle a batch IO per synchronization. It could effectively reduce the system calls which was brought by process synchronization.

## Performance Testing

The source code bench_test.go, doing a performance comparison between shmipc and unix domain in ping-pong scenario with different package size. The result is as follows: the performance of small packet scenarios is comparable and the performance of large packet scenarios is significantly improved.

```
go test -bench=BenchmarkParallelPingPong -run BenchmarkParallelPingPong
goos: linux
goarch: amd64
pkg: github.com/cloudwego/shmipc-go
cpu: Intel(R) Xeon(R) Platinum 8260 CPU @ 2.40GHz
BenchmarkParallelPingPongByShmipc64B-8     	  144470	      7740 ns/op	  25.84 MB/s	     272 B/op	       6 allocs/op
BenchmarkParallelPingPongByShmipc512B-8    	  145243	      8727 ns/op	 159.51 MB/s	    1170 B/op	       6 allocs/op
BenchmarkParallelPingPongByShmipc1KB-8     	  137221	     11467 ns/op	 240.69 MB/s	    2199 B/op	       6 allocs/op
BenchmarkParallelPingPongByShmipc4KB-8     	   67123	     16574 ns/op	 660.78 MB/s	    8403 B/op	       6 allocs/op
BenchmarkParallelPingPongByShmipc16KB-8    	   37604	     34197 ns/op	1278.49 MB/s	   33711 B/op	       6 allocs/op
BenchmarkParallelPingPongByShmipc64KB-8    	   12418	     97118 ns/op	1799.79 MB/s	  138413 B/op	       6 allocs/op
BenchmarkParallelPingPongByShmipc256KB-8   	    3885	    347648 ns/op	2010.89 MB/s	  561896 B/op	       7 allocs/op
BenchmarkParallelPingPongByShmipc512KB-8   	    2122	    567535 ns/op	2463.51 MB/s	 1126969 B/op	       7 allocs/op
BenchmarkParallelPingPongByShmipc1MB-8     	    1147	   1078216 ns/op	2593.39 MB/s	 2258771 B/op	       7 allocs/op
BenchmarkParallelPingPongByShmipc4MB-8     	     302	   4163412 ns/op	2686.46 MB/s	 9185775 B/op	       8 allocs/op
BenchmarkParallelPingPongByUds64B-8        	  227320	      5523 ns/op	  36.21 MB/s	     720 B/op	      10 allocs/op
BenchmarkParallelPingPongByUds512B-8       	  123703	      8154 ns/op	 170.72 MB/s	    3988 B/op	      10 allocs/op
BenchmarkParallelPingPongByUds1KB-8        	  100774	     10796 ns/op	 255.66 MB/s	    7837 B/op	      10 allocs/op
BenchmarkParallelPingPongByUds4KB-8        	   39502	     31889 ns/op	 343.44 MB/s	   33147 B/op	      10 allocs/op
BenchmarkParallelPingPongByUds16KB-8       	   10116	    114273 ns/op	 382.59 MB/s	  134418 B/op	      10 allocs/op
BenchmarkParallelPingPongByUds64KB-8       	    5803	    216532 ns/op	 807.23 MB/s	  521737 B/op	      11 allocs/op
BenchmarkParallelPingPongByUds256KB-8      	    2078	    520211 ns/op	1343.84 MB/s	 2043899 B/op	      11 allocs/op
BenchmarkParallelPingPongByUds512KB-8      	    1237	    892046 ns/op	1567.33 MB/s	 4061270 B/op	      11 allocs/op
BenchmarkParallelPingPongByUds1MB-8        	     703	   2626436 ns/op	1064.65 MB/s	 8087850 B/op	      11 allocs/op
BenchmarkParallelPingPongByUds4MB-8        	     171	   5893266 ns/op	1897.90 MB/s	33747030 B/op	      13 allocs/op
PASS
ok  	github.com/cloudwego/shmipc-go	43.834s

```

- BenchmarkParallelPingPongByUds, the ping-pong communication base on Unix domain socket.
- BenchmarkParallelPingPongByShmipc, the ping-pong communication base on shmipc.
- the suffix of the testing case name is the package size of communication, which from 64 Byte to 4 MB.
- Stream.BufferWriter() and Stream.BufferReader() provide buffer read-write interfaces for shared memory, where the ReadBytes() and Reserve() methods can be used for zero-copy read and write.

### Quick start

#### HelloWorld

- [HelloWorldClient](https://github.com/cloudwego/shmipc-go/blob/main/example/helloworld/greeter_client/main.go)
- [HelloWorldServer](https://github.com/cloudwego/shmipc-go/blob/main/example/helloworld/greeter_server/main.go)

#### Integrate with application

- [serialization and deserialization](https://github.com/cloudwego/shmipc-go/blob/main/example/best_practice/idl/example.go)
- [client which using synchronous interface](https://github.com/cloudwego/shmipc-go/blob/main/example/best_practice/shmipc_client/main.go)
- [server which using synchronous interface](https://github.com/cloudwego/shmipc-go/blob/main/example/best_practice/shmipc_server/main.go)
- [client which using asynchronous interface](https://github.com/cloudwego/shmipc-go/blob/main/example/best_practice/shmipc_async_client/client.go)
- [server which using asynchronous interface](https://github.com/cloudwego/shmipc-go/blob/main/example/best_practice/shmipc_async_server/server.go)

#### HotRestart

[hot restart demo](https://github.com/cloudwego/shmipc-go/blob/main/example/hot_restart_test/README.md)
