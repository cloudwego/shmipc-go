
# Shmipc

English | [中文](README_CN.md)

## Introduction

Shmipc is a high performance inter-process communication library developed by ByteDance.
It is built on Linux's shared memory technology and uses unix or tcp connection to do process synchronization and finally implements zero copy communication across inter-processes. 
In IO-intensive and large-package scenarios, it has better performance.

## Features

### Zero copy

In an industrial production environment, the unix domain socket and tcp loopback are often used in inter-process communication.The read operation or the write operation need copy data between user space buffer and kernel space buffer.But shmipc directly store data to the share memory, so no copy compared to the former.

### batch IO

An IO queue was mapped to share memory, which describe the metadata of communication data.
so that a process could put many request to the IO queue, and other process  could handle a batch IO per synchronization. It could effectively reduce the system calls which was brought by process synchronization.

## Performance Testing

The source code bench_test.go, doing a performance comparison between shmipc and unix domain in ping-pong scenario with different package size. The result is as follows: having a performance improvement whatever small package or large package.

```
go test -bench=BenchmarkParallelPingPong -run BenchmarkParallelPingPong
goos: linux
goarch: amd64
pkg: github.com/cloudwego/shmipc-go
cpu: Intel(R) Xeon(R) CPU E5-2630 v4 @ 2.20GHz
BenchmarkParallelPingPongByShmipc64B-40      	  733821	      1970 ns/op	  64.97 MB/s	       0 B/op	       0 allocs/op
BenchmarkParallelPingPongByShmipc512B-40     	  536190	      1990 ns/op	 514.45 MB/s	       0 B/op	       0 allocs/op
BenchmarkParallelPingPongByShmipc1KB-40      	  540517	      2045 ns/op	1001.62 MB/s	       0 B/op	       0 allocs/op
BenchmarkParallelPingPongByShmipc4KB-40      	  509047	      2063 ns/op	3970.91 MB/s	       0 B/op	       0 allocs/op
BenchmarkParallelPingPongByShmipc16KB-40     	  590398	      1996 ns/op	16419.46 MB/s	       0 B/op	       0 allocs/op
BenchmarkParallelPingPongByShmipc32KB-40     	  607756	      1937 ns/op	33829.82 MB/s	       0 B/op	       0 allocs/op
BenchmarkParallelPingPongByShmipc64KB-40     	  609824	      1995 ns/op	65689.31 MB/s	       0 B/op	       0 allocs/op
BenchmarkParallelPingPongByShmipc256KB-40    	  622755	      1793 ns/op	292363.56 MB/s	       0 B/op	       0 allocs/op
BenchmarkParallelPingPongByShmipc512KB-40    	  695401	      1993 ns/op	526171.77 MB/s	       0 B/op	       0 allocs/op
BenchmarkParallelPingPongByShmipc1MB-40      	  538208	      1873 ns/op	1119401.64 MB/s	       0 B/op	       0 allocs/op
BenchmarkParallelPingPongByShmipc4MB-40      	  606144	      1891 ns/op	4436936.93 MB/s	       0 B/op	       0 allocs/op
BenchmarkParallelPingPongByUds64B-40         	  446019	      2657 ns/op	  48.18 MB/s	       0 B/op	       0 allocs/op
BenchmarkParallelPingPongByUds512B-40        	  450124	      2665 ns/op	 384.30 MB/s	       0 B/op	       0 allocs/op
BenchmarkParallelPingPongByUds1KB-40         	  446389	      2680 ns/op	 764.29 MB/s	       0 B/op	       0 allocs/op
BenchmarkParallelPingPongByUds4KB-40         	  383552	      3093 ns/op	2648.83 MB/s	       1 B/op	       0 allocs/op
BenchmarkParallelPingPongByUds16KB-40        	  307816	      3884 ns/op	8436.27 MB/s	       8 B/op	       0 allocs/op
BenchmarkParallelPingPongByUds64KB-40        	  103027	     10259 ns/op	12776.17 MB/s	     102 B/op	       0 allocs/op
BenchmarkParallelPingPongByUds256KB-40       	   25286	     46352 ns/op	11311.01 MB/s	    1661 B/op	       0 allocs/op
BenchmarkParallelPingPongByUds512KB-40       	    9788	    122873 ns/op	8533.84 MB/s	    8576 B/op	       0 allocs/op
BenchmarkParallelPingPongByUds1MB-40         	    4177	    283729 ns/op	7391.38 MB/s	   40178 B/op	       0 allocs/op
BenchmarkParallelPingPongByUds4MB-40         	     919	   1253338 ns/op	6693.01 MB/s	  730296 B/op	       1 allocs/op
PASS
ok  	github.com/cloudwego/shmipc	42.138s

```

- BenchmarkParallelPingPongByUds, the ping-pong communication base on Unix domain socket.
- BenchmarkParallelPingPongByShmipc, the ping-pong communication base on shmipc.
- the suffix of the testing case name is the package size of communication, which from 64 Byte to 4 MB.

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
