//go:build !linux && !windows

package firecracker

import (
	"fmt"
	"syscall"
)

func setNamespaces(attr *syscall.SysProcAttr, useNamespaces bool, netNS string) {
}

func bindMountFile(src, dst string) error {
	return fmt.Errorf("bind mounts are only supported on Linux")
}

func unmountFile(target string) error {
	return nil
}
