//go:build !unix

package storage

import "os"

func copySparseDataHole(src, dst *os.File, size int64) error {
	return fallbackChunkCopy(src, dst, size)
}
