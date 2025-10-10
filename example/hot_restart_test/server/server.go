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

package main

import (
	"encoding/binary"
	"fmt"
	syscall "golang.org/x/sys/unix"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/cloudwego/shmipc-go"
)

var (
	count    uint64
	errCount uint64

	sendStr = "hello world"

	udsPath   = ""
	adminPath = ""

	ENV_IS_HOT_RESTART_KEY    = "IS_HOT_RESTART"
	ENV_HOT_RESTART_EPOCH_KEY = "HOT_RESTART_EPOCH"

	hotRestartEpochId = 0
)

func main() {
	// Initialize some variables, start debug port, print qps info ...
	Init()

	if os.Getenv(ENV_IS_HOT_RESTART_KEY) == "1" {
		// hotrestart server
		restart()
	} else {
		// first startup
		start()
	}
}

func Init() {
	go func() {
		lastCount := count
		for range time.Tick(time.Second) {
			curCount := atomic.LoadUint64(&count)
			fmt.Println("qps:", curCount-lastCount, " errcount ", atomic.LoadUint64(&errCount), " count ", atomic.LoadUint64(&count))
			lastCount = curCount
		}
	}()
	runtime.GOMAXPROCS(4)

	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	udsPath = filepath.Join(dir, "../ipc_test.sock")
	adminPath = filepath.Join(dir, "../admin.sock")
	fmt.Printf("shmipc udsPath %s adminPath %s\n", udsPath, adminPath)

	debugPort := 20000
	if os.Getenv("DEBUG_PORT") != "" {
		debugPort, err = strconv.Atoi(os.Getenv("DEBUG_PORT"))
		if err != nil {
			panic(err)
		}
	}

	go func() {
		http.ListenAndServe(fmt.Sprintf(":%d", debugPort), nil) //nolint:errcheck
	}()
}

func listenFD(ln net.Listener) (fd int, f *os.File) {
	defer func() {
		if fd < 0 && f == nil {
			panic(fmt.Errorf("hot-restart can't parse listener, which type %T", ln))
		}
	}()
	if getter, ok := ln.(interface{ Fd() (fd int) }); ok {
		return getter.Fd(), nil
	}
	switch l := ln.(type) {
	case *net.TCPListener:
		f, _ = l.File()
	case *net.UnixListener:
		f, _ = l.File()
	}
	return int(f.Fd()), f
}

// admin handle hotrestart
func listenAdmin(svr *shmipc.Listener, admin net.Listener) {
	// step 1. accept connection, begin hotrestart
	adminln, ok := admin.(*net.UnixListener)
	if !ok {
		panic("admin ln error")
	}
	adminln.SetUnlinkOnClose(false)
	conn, err := adminln.AcceptUnix()
	if err != nil {
		panic(fmt.Errorf("adminln AcceptUnix error %+v", err))
	}

	// step 2. get admin listener fd
	adminFd, _ := listenFD(admin)
	fmt.Printf("dump listenAdmin adminFd %d\n", adminFd)
	syscall.SetNonblock(adminFd, true)

	// step 3. send admin listener fd
	var buf [8]byte // recv epoch id, 8 bytes
	rights := syscall.UnixRights(adminFd)
	var writeN, oobN int
	writeN, oobN, err = conn.WriteMsgUnix(buf[:], rights, nil)
	fmt.Println("WriteMsgUnix writeN ", writeN, " oobN ", oobN, " err ", err)
	if err != nil {
		panic(err)
	}

	// step 4. after recv epoch id, the new server is ready for hot restart
	_, err = conn.Read(buf[:])
	if err != nil {
		panic(err)
	}
	epochId := binary.BigEndian.Uint64(buf[:])
	fmt.Println("recv new server epoch id ", epochId)
	if epochId <= 0 {
		panic("epochId error")
	}

	// step 5. begin shmipc hot restart
	fmt.Println("old server begin shmipc hot restart epoch id = ", epochId)
	err = svr.HotRestart(uint64(epochId))
	if err != nil {
		panic(err)
	}

	// step 6. wait for the hot restart done
	for {
		if svr.IsHotRestartDone() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Println("old server finish shmipc hot restart")

	time.Sleep(1 * time.Second)
	// step 6. hot restart done, old server exit
	err = svr.Close()
	if err != nil {
		panic(err)
	}
	err = admin.Close()
	if err != nil {
		panic(err)
	}
}

func recvFds(conn *net.UnixConn) []int {
	var buf [1]byte
	var rightsBuf [1024]byte
	readN, oobN, _, _, err := conn.ReadMsgUnix(buf[:], rightsBuf[:])
	fmt.Println("readN = ", readN, " oobN = ", oobN)
	if err != nil {
		panic(err)
	}

	rights := rightsBuf[:oobN]
	ctrlMsgs, err := syscall.ParseSocketControlMessage(rights)
	if err != nil {
		panic(err)
	}
	fds, err := syscall.ParseUnixRights(&ctrlMsgs[0])
	if err != nil {
		panic(err)
	}

	return fds
}

func rebuildListener(fd int) (net.Listener, error) {
	file := os.NewFile(uintptr(fd), "")
	if file == nil {
		return nil, fmt.Errorf("hot-restart failed to new file with fd %d", fd)
	}
	// can't close file here !
	ln, err := net.FileListener(file)
	if err != nil {
		return nil, err
	}
	return ln, nil
}

func start() {
	_ = syscall.Unlink(udsPath)
	syscall.Unlink(adminPath)
	fmt.Printf("server normal start\n")

	// step 1. create shmipc listener
	config := shmipc.NewDefaultListenerConfig(udsPath, "unix")
	shmipcListener, err := shmipc.NewListener(&listenCbImpl{}, config)
	if err != nil {
		panic(fmt.Errorf("shmipc NewListener error %+v", err))
	}
	shmipcListener.SetUnlinkOnClose(false)

	// step 2. create admin listener
	adminln, err := net.Listen("unix", adminPath)
	if err != nil {
		panic(fmt.Errorf("listener adminPath %s error %+v", adminPath, err))
	}
	if admin, ok := adminln.(*net.UnixListener); ok {
		admin.SetUnlinkOnClose(false)
	}

	// step 3. admin handle hot restart protocols
	go listenAdmin(shmipcListener, adminln)

	// step 4. server run
	err = shmipcListener.Run()
	fmt.Println("shmipcListener Run Ret err ", err)
}

func restart() {
	// step 1. send Epoch Id to old server
	epoch := os.Getenv(ENV_HOT_RESTART_EPOCH_KEY)
	var err error
	hotRestartEpochId, err = strconv.Atoi(epoch)
	fmt.Println("hot restart epoch id = ", hotRestartEpochId)
	if err != nil {
		panic(fmt.Errorf("%s parse hotRestartEpochId error %+v", ENV_HOT_RESTART_EPOCH_KEY, err))
	}

	// step 2. connect admin socket
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: adminPath, Net: "unix"})
	if err != nil {
		panic(err)
	}

	// step 3. recv admin fds
	fds := recvFds(conn)
	fmt.Println("new server recv fds ", fds)
	if len(fds) != 1 {
		panic(fmt.Errorf("fds len is err"))
	}

	// step 3. use fd rebuild listener
	var adminln net.Listener
	adminln, err = rebuildListener(fds[0])
	if err != nil {
		panic(err)
	}
	fmt.Println("rebuildListener adminln.addr ", adminln.Addr())

	// step 4. create new shmipc listener
	config := shmipc.NewDefaultListenerConfig(udsPath, "unix")
	srvLn, err := shmipc.NewListener(&listenCbImpl{}, config)
	if err != nil {
		fmt.Println(err)
		return
	}
	srvLn.SetUnlinkOnClose(false)

	// step 5. send to old server
	go func() {
		// sleep make sure new shmipc listener run
		time.Sleep(500 * time.Millisecond)
		fmt.Println("start hot restart")

		epochId := make([]byte, 8)
		binary.BigEndian.PutUint64(epochId, uint64(hotRestartEpochId))
		_, err := conn.Write(epochId)
		if err != nil {
			panic(err)
		}

		_ = conn.Close()
		// new server continues to perform admin duties
		go listenAdmin(srvLn, adminln)
	}()

	// step 6. new server run
	err = srvLn.Run()
	fmt.Println("server Run ret err ", err)
}
