//go:build unix

package storage

import (
	"os"
	"syscall"
)

func fileNlink(fi os.FileInfo) uint64 {
	if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Nlink)
	}
	return 1
}
