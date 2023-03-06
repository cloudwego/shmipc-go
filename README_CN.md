``

# Cloudwego/Shmipc

## 简介

Shmipc是一个由字节跳动开发的高性能进程间通讯库。它基于Linux的共享内存构建，使用unix/tcp连接进行进程同步，实现进程间通讯零拷贝。在IO密集型场景或大包场景能够获得显著的性能收益。

设计参考: https://bytedance.feishu.cn/docx/CyVTd1jrIoBnsvxqJMBcSfcWntg

## 特性

### 零拷贝

在工业生产环境中，Unix domain socket和Tcp loopback常用于进程间通讯，读写均涉及通讯数据在用户态buffer与内核态buffer的来回拷贝。而Shmipc使用共享内存存放通讯数据，相对于前者没有数据拷贝。

### 批量收割IO

Shmipc在共享内存中引入了一个IO队列来描述通讯数据的元信息，一个进程可以并发地讲多个请求的元信息放入IO队列，另外一个进程只要需要一次同步就能批量收割IO.这在IO密集的场景下能够有效减少进程同步带来的system call。

## 性能测试

源码中 bench_test.go 进行了Shmipc与Unix domain socket在ping-pong场景下不同数据包大小的性能对比，结果如下所示: 从小包到大包均有性能提升。

```
go test -bench=BenchmarkParallelPingPong -run BenchmarkParallelPingPong
goos: linux
goarch: amd64
pkg: github.com/cloudwego/shmipc
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

- BenchmarkParallelPingPongByUds，基于Unix domain socket进行ping-pong通讯。
- BenchmarkParallelPingPongByShmipc，基于Shmipc进行ping-pong通讯。
- 后缀为ping-pong的数据包大小， 从 64 Byte ~ 4MB 不等。

### 快速开始

#### HelloWorld

- 客户端：example/helloword/greeter_client/main.go
- 服务端：example/helloword/greeter_server/main.go

#### 与应用集成

- example/best_practice/idl/example.go 提供了使用Stream的Buffer接口进行对象的序列化与反序列化
- example/best_practice/shmipc_client.go 提供了使用Shmipc同步接口的Client实现
- example/best_practice/shmipc_server.go 提供了使用Shmipc同步接口的Server实现
- example/best_practice/shmipc_async_client.go 提供了使用Shmipc异步接口的Client实现
- example/best_practice/shmipc_async_server.go 提供了使用Shmipc异步接口的Server实现

#### 热升级

example/hot_restart_test/README.md
