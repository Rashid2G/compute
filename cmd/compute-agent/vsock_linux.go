//go:build linux

package main

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func listenVsock(port uint32) (net.Listener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("socket(AF_VSOCK) failed: %w", err)
	}

	sa := &unix.SockaddrVM{
		CID:  unix.VMADDR_CID_ANY,
		Port: port,
	}

	if err := unix.Bind(fd, sa); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind vsock failed: %w", err)
	}

	if err := unix.Listen(fd, 32); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("listen vsock failed: %w", err)
	}

	file := os.NewFile(uintptr(fd), "vsock-listener")
	return net.FileListener(file)
}
