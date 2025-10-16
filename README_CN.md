# Shmipc

[English](README.md) | 中文 

## 简介

Shmipc是一个由字节跳动开发的高性能进程间通讯库。它基于Linux的共享内存构建，使用unix/tcp连接进行进程同步，实现进程间通讯零拷贝。在IO密集型场景或大包场景能够获得显著的性能收益。

## 特性

### 零拷贝

在工业生产环境中，Unix domain socket和Tcp loopback常用于进程间通讯，读写均涉及通讯数据在用户态buffer与内核态buffer的来回拷贝。而Shmipc使用共享内存存放通讯数据，相对于前者没有数据拷贝。

### 批量收割IO

Shmipc在共享内存中引入了一个IO队列来描述通讯数据的元信息，一个进程可以并发地将多个请求的元信息放入IO队列，另外一个进程只要需要一次同步就能批量收割IO.这在IO密集的场景下能够有效减少进程同步带来的system call。

## 性能测试

源码中 bench_test.go 进行了Shmipc与Unix domain socket在ping-pong场景下不同数据包大小的性能对比，结果如下所示: 小包场景性能相当，包越大Shmipc的性能优势越明显。

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

- BenchmarkParallelPingPongByUds，基于Unix domain socket进行ping-pong通讯。
- BenchmarkParallelPingPongByShmipc，基于Shmipc进行ping-pong通讯。
- 后缀为ping-pong的数据包大小， 从 64 Byte ~ 4MB 不等。
- Stream.BufferWriter()和Stream.BufferReader()提供了共享内存的buffer读写接口，其中使用ReadBytes()和Reserve()方法可以进行零拷贝的读和写.

### 快速开始

#### HelloWorld

- [HelloWorld客户端](https://github.com/cloudwego/shmipc-go/blob/main/example/helloworld/greeter_client/main.go)
- [HelloWorld服务端](https://github.com/cloudwego/shmipc-go/blob/main/example/helloworld/greeter_server/main.go)

#### 与应用集成

- [使用Stream的Buffer接口进行对象的序列化与反序列化](https://github.com/cloudwego/shmipc-go/blob/main/example/best_practice/idl/example.go)
- [使用Shmipc同步接口的Client实现](https://github.com/cloudwego/shmipc-go/blob/main/example/best_practice/shmipc_client/main.go)
- [使用Shmipc同步接口的Server实现](https://github.com/cloudwego/shmipc-go/blob/main/example/best_practice/shmipc_server/main.go)
- [使用Shmipc异步接口的Client实现](https://github.com/cloudwego/shmipc-go/blob/main/example/best_practice/shmipc_async_client/client.go)
- [使用Shmipc异步接口的Server实现](https://github.com/cloudwego/shmipc-go/blob/main/example/best_practice/shmipc_async_server/server.go) 

#### 热升级

[热升级demo](https://github.com/cloudwego/shmipc-go/blob/main/example/hot_restart_test/README.md)
