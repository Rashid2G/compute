//go:build linux

package firecracker

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func setNamespaces(attr *syscall.SysProcAttr, useNamespaces bool, netNS string) {
	if !useNamespaces {
		return
	}
	attr.Cloneflags = CloneNewPID | CloneNewNS | CloneNewIPC | CloneNewUTS | CloneNewCgroup | CloneNewNet
	if netNS == "" {
		attr.Cloneflags &^= CloneNewNet
	}
}

func bindMountFile(src, dst string) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create bind mount target %s: %w", dst, err)
	}
	_ = f.Close()

	if err := unix.Mount(src, dst, "", unix.MS_BIND, ""); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("unix.Mount MS_BIND failed: %w", err)
	}
	return nil
}

func unmountFile(target string) error {
	if err := unix.Unmount(target, unix.MNT_DETACH); err != nil {
		if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	return nil
}
