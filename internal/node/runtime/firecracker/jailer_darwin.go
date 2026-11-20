//go:build !linux && !windows

package firecracker

import "syscall"

func setNamespaces(attr *syscall.SysProcAttr, useNamespaces bool, netNS string) {
}
