//go:build unix

package vm

import (
	"os"
	"syscall"
)

func fileAllocatedBlocksBytes(fi os.FileInfo) (int64, bool) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st != nil {
		return st.Blocks * 512, true
	}
	return 0, false
}
