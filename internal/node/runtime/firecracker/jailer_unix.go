//go:build !windows

package firecracker

import "syscall"

func newSysProcAttr(useNamespaces bool, netNS string) *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{Setpgid: true}
	setNamespaces(attr, useNamespaces, netNS)
	return attr
}
