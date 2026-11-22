//go:build linux

package storage

import "golang.org/x/sys/unix"

func ioctlFileClone(dstFd, srcFd int) error {
	return unix.IoctlFileClone(dstFd, srcFd)
}
