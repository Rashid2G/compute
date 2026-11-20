//go:build windows

package firecracker

import (
	"fmt"
	"os"
	"syscall"
)

func newSysProcAttr(useNamespaces bool, netNS string) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

func signalProcess(ip *InstanceProcess, sig syscall.Signal) error {
	if ip == nil {
		return nil
	}
	if ip.Cmd != nil && ip.Cmd.Process != nil {
		return ip.Cmd.Process.Kill()
	}
	if ip.PID > 0 {
		p, err := os.FindProcess(ip.PID)
		if err == nil {
			return p.Kill()
		}
	}
	return nil
}

func bindMountFile(src, dst string) error {
	return fmt.Errorf("bind mounts are only supported on Linux")
}

func unmountFile(target string) error {
	return nil
}
