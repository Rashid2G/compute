//go:build !linux

package agent

import (
	"errors"
	"os"
)

var errNonLinux = errors.New("platform feature not supported on non-linux systems")

func openPTY(rows, cols uint16) (*os.File, *os.File, error) {
	return nil, nil, errNonLinux
}

func setPTYSize(fd int, rows, cols uint16) error {
	return errNonLinux
}

func setClock(unixNano int64) error {
	return errNonLinux
}

func mountDev(dev, target, fstype string, readOnly bool) error {
	return errNonLinux
}

func unmountDev(target string) error {
	return errNonLinux
}

func syncAndReboot() error {
	return errNonLinux
}

func readPlatformStats() (*GuestStats, error) {
	return &GuestStats{
		MemTotalMB: 1024,
		MemUsedMB:  256,
		UptimeSec:  100,
	}, nil
}
