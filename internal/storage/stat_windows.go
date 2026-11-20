//go:build windows

package storage

import "os"

func fileNlink(fi os.FileInfo) uint64 {
	return 1
}
