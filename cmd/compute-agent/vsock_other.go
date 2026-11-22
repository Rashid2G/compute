//go:build !linux

package main

import (
	"errors"
	"net"
)

func listenVsock(port uint32) (net.Listener, error) {
	return nil, errors.New("AF_VSOCK is only supported on Linux")
}
