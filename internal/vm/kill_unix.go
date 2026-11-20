//go:build unix

package vm

import "syscall"

func killProcess(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}
