//go:build windows

package vm

import "os"

func fileAllocatedBlocksBytes(fi os.FileInfo) (int64, bool) {
	return 0, false
}
