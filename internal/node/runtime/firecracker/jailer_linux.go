//go:build linux

package firecracker

import "syscall"

func setNamespaces(attr *syscall.SysProcAttr, useNamespaces bool, netNS string) {
	if !useNamespaces {
		return
	}
	attr.Cloneflags = CloneNewPID | CloneNewNS | CloneNewIPC | CloneNewUTS | CloneNewCgroup | CloneNewNet
	if netNS == "" {
		attr.Cloneflags &^= CloneNewNet
	}
}
