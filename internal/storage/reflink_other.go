//go:build !linux

package storage

import "errors"

func ioctlFileClone(dstFd, srcFd int) error {
	return errors.New("reflink not supported on this platform")
}
