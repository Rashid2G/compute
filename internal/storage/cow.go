package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	// ErrReflinkNotSupported is returned when FICLONE ioctl is not supported on the filesystem.
	ErrReflinkNotSupported = errors.New("reflink (FICLONE) not supported on this filesystem")
)

// isIdenticalFile determines whether srcPath and dstPath refer to the exact same directory entry or canonical file on disk.
func isIdenticalFile(srcPath, dstPath string) bool {
	srcClean := filepath.Clean(srcPath)
	dstClean := filepath.Clean(dstPath)
	if srcClean == dstClean {
		return true
	}
	srcAbs, err1 := filepath.Abs(srcPath)
	dstAbs, err2 := filepath.Abs(dstPath)
	if err1 == nil && err2 == nil {
		if srcAbs == dstAbs {
			return true
		}
		srcReal, err3 := filepath.EvalSymlinks(srcAbs)
		dstReal, err4 := filepath.EvalSymlinks(dstAbs)
		if err3 == nil && err4 == nil && srcReal == dstReal {
			return true // Identical canonical file on disk! Do not delete!
		}
	}
	return false
}

// CloneFileCOW performs a copy-on-write clone of srcPath to dstPath.
// It first attempts the Linux FICLONE ioctl (unix.IoctlFileClone) for instant block-level reflink.
// If the underlying filesystem returns EOPNOTSUPP, EXDEV, or ENOTTY, it cleanly falls back
// to cp --reflink=auto --sparse=always, and finally to sparse hole-preserving copy.
func CloneFileCOW(srcPath, dstPath string) error {
	if isIdenticalFile(srcPath, dstPath) {
		return nil
	}

	srcFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", srcPath, err)
	}
	defer srcFile.Close()

	srcStat, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat source file %s: %w", srcPath, err)
	}

	// Check if destination exists and refers to the identical file/inode (hardlink alias)
	if dstStat, err := os.Stat(dstPath); err == nil {
		if isIdenticalFile(srcPath, dstPath) {
			return nil
		}
		if os.SameFile(srcStat, dstStat) {
			// Decouple hardlink / alias only if paths point to distinct directory entries
			// and link count > 1
			var nlink uint64 = 1
			if stat, ok := dstStat.Sys().(*syscall.Stat_t); ok {
				nlink = stat.Nlink
			}
			if nlink > 1 {
				if err := os.Remove(dstPath); err != nil {
					return fmt.Errorf("failed to remove hardlink alias %s: %w", dstPath, err)
				}
			} else {
				return nil
			}
		}
	}

	// Create or truncate destination file
	dstFile, err := os.OpenFile(dstPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create destination file %s: %w", dstPath, err)
	}

	// 1. Attempt kernel reflink via ioctl(FICLONE)
	cloneErr := unix.IoctlFileClone(int(dstFile.Fd()), int(srcFile.Fd()))
	_ = dstFile.Close()

	if cloneErr == nil {
		return nil
	}

	// 2. If FICLONE is unsupported or cross-device (EOPNOTSUPP, EXDEV, ENOTTY, EINVAL),
	// attempt cp --reflink=auto --sparse=always
	_ = os.Remove(dstPath)
	cmd := exec.Command("cp", "--reflink=auto", "--sparse=always", srcPath, dstPath)
	if err := cmd.Run(); err == nil {
		return nil
	}

	// 3. Fallback to sparse hole-preserving block copy
	return copySparseFileFast(srcPath, dstPath, srcStat.Size())
}

// ReflinkStrict attempts ONLY an ioctl(FICLONE) reflink clone and returns ErrReflinkNotSupported if unsupported.
func ReflinkStrict(srcPath, dstPath string) error {
	if isIdenticalFile(srcPath, dstPath) {
		return nil
	}

	srcFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source: %w", err)
	}
	defer srcFile.Close()

	srcStat, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat source: %w", err)
	}

	if dstStat, err := os.Stat(dstPath); err == nil {
		if isIdenticalFile(srcPath, dstPath) {
			return nil
		}
		if os.SameFile(srcStat, dstStat) {
			var nlink uint64 = 1
			if stat, ok := dstStat.Sys().(*syscall.Stat_t); ok {
				nlink = stat.Nlink
			}
			if nlink > 1 {
				if err := os.Remove(dstPath); err != nil {
					return fmt.Errorf("failed to remove hardlink alias %s: %w", dstPath, err)
				}
			} else {
				return nil
			}
		}
	}

	dstFile, err := os.OpenFile(dstPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create destination: %w", err)
	}
	defer dstFile.Close()

	err = unix.IoctlFileClone(int(dstFile.Fd()), int(srcFile.Fd()))
	if err != nil {
		_ = os.Remove(dstPath)
		return fmt.Errorf("%w: %v", ErrReflinkNotSupported, err)
	}
	return nil
}

// copySparseFileFast copies a file preserving sparse holes using SEEK_DATA and SEEK_HOLE when available,
// or chunk-based zero detection.
func copySparseFileFast(srcPath, dstPath string, size int64) error {
	if srcPath == dstPath {
		return nil
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer dst.Close()

	if err := dst.Truncate(size); err != nil {
		return fmt.Errorf("failed to truncate destination to sparse size: %w", err)
	}

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

func fallbackChunkCopy(src, dst *os.File, size int64) error {
	const chunkSize = 256 * 1024
	buf := make([]byte, chunkSize)

	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return err
	}

	var offset int64
	for offset < size {
		n, err := src.Read(buf)
		if n > 0 {
			if isAllZero(buf[:n]) {
				offset += int64(n)
				continue
			}
			if _, err := dst.Seek(offset, io.SeekStart); err != nil {
				return err
			}
			if _, err := dst.Write(buf[:n]); err != nil {
				return err
			}
			offset += int64(n)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}
	return nil
}

func isAllZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
