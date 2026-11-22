package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/Rashid2G/compute/internal/agent"
)

func main() {
	portFlag := flag.Int("port", agent.DefaultVsockPort, "vsock port to listen on")
	tcpFallback := flag.Int("tcp-port", 0, "fallback TCP port if vsock is unavailable (0 to disable)")
	flag.Parse()

	isPID1 := os.Getpid() == 1 || filepath.Base(os.Args[0]) == "compute-init"

	if isPID1 {
		setupGuestEnvironment()
		go reapZombies()
	}

	vsockPort := uint32(*portFlag)
	ln, err := listenVsock(vsockPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compute-agent: failed to listen on vsock port %d: %v\n", vsockPort, err)
		fallbackPort := *tcpFallback
		if fallbackPort <= 0 {
			fallbackPort = int(vsockPort)
		}
		ln, err = net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", fallbackPort))
		if err != nil {
			fmt.Fprintf(os.Stderr, "compute-agent: failed to listen on fallback tcp %d: %v\n", fallbackPort, err)
			if isPID1 {
				select {} // Keep PID 1 alive
			}
			os.Exit(1)
		}
		fmt.Printf("compute-agent: listening on fallback tcp 0.0.0.0:%d (PID 1: %v)\n", fallbackPort, isPID1)
	} else {
		fmt.Printf("compute-agent: listening on vsock port %d (PID 1: %v)\n", vsockPort, isPID1)
	}
	defer ln.Close()

	srv := agent.NewServer()

	for {
		conn, err := ln.Accept()
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go srv.HandleConn(conn)
	}
}
