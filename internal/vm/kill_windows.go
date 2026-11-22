//go:build windows

package vm

import (
	"os"
	"syscall"
)

func killProcess(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
