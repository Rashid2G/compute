//go:build unix

package storage

import (
	"path/filepath"
	"syscall"
)

func getFSFreeMB(checkPath string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(checkPath, &stat); err != nil {
		parent := filepath.Dir(checkPath)
		if err2 := syscall.Statfs(parent, &stat); err2 != nil {
			return 0, err2
		}
	}
	freeBytes := stat.Bavail * uint64(stat.Bsize)
	return int64(freeBytes / (1024 * 1024)), nil
}
