//go:build !windows

package firecracker

import "syscall"

func newSysProcAttr(useNamespaces bool, netNS string) *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{Setpgid: true}
	setNamespaces(attr, useNamespaces, netNS)
	return attr
}

func signalProcess(ip *InstanceProcess, sig syscall.Signal) error {
	if ip == nil {
		return nil
	}
	if ip.PID > 0 {
		if err := syscall.Kill(-ip.PID, sig); err == nil {
			return nil
		}
		if err := syscall.Kill(ip.PID, sig); err == nil {
			return nil
		}
	}
	if ip.Cmd != nil && ip.Cmd.Process != nil {
		return ip.Cmd.Process.Signal(sig)
	}
	return nil
}
