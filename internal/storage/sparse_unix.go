//go:build unix

package storage

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func copySparseDataHole(src, dst *os.File, size int64) error {
	var offset int64
	for offset < size {
		dataOffset, err := unix.Seek(int(src.Fd()), offset, unix.SEEK_DATA)
		if err != nil {
			if err == unix.ENXIO {
				// No more data blocks exist
				break
			}
			// SEEK_DATA unsupported on this filesystem, fall back to chunk-based zero detection
			return fallbackChunkCopy(src, dst, size)
		}

		holeOffset, err := unix.Seek(int(src.Fd()), dataOffset, unix.SEEK_HOLE)
		if err != nil {
			holeOffset = size
		}

		dataLen := holeOffset - dataOffset
		if _, err := src.Seek(dataOffset, io.SeekStart); err != nil {
			return err
		}
		if _, err := dst.Seek(dataOffset, io.SeekStart); err != nil {
			return err
		}

		copied, err := io.CopyN(dst, src, dataLen)
		if err != nil && err != io.EOF {
			return fmt.Errorf("failed to copy sparse data chunk: %w", err)
		}
		offset = dataOffset + copied
	}
	return nil
}
