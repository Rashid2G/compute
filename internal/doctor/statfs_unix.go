//go:build unix

package doctor

import "syscall"

func statfsMB(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	availMB := int64(stat.Bavail) * int64(stat.Bsize) / (1024 * 1024)
	return availMB, nil
}
