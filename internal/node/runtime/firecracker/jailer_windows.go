//go:build windows

package firecracker

import "syscall"

func newSysProcAttr(useNamespaces bool, netNS string) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
