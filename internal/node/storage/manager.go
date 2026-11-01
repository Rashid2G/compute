package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	ErrVolumeNotFound      = errors.New("volume not found")
	ErrReflinkNotSupported = errors.New("reflink (FICLONE) not supported on this filesystem")
)

// VolumeManager manages raw sparse block volumes and reflink-based cloning.
type VolumeManager struct {
	volumesDir string
}

// NewVolumeManager initializes a volume manager storing images in volumesDir.
func NewVolumeManager(volumesDir string) (*VolumeManager, error) {
	if err := os.MkdirAll(volumesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create volumes directory: %w", err)
	}
	return &VolumeManager{volumesDir: volumesDir}, nil
}

// Path returns the absolute path of a volume file given its ID.
func (m *VolumeManager) Path(volumeID string) string {
	return filepath.Join(m.volumesDir, volumeID+".img")
}

// CreateSparse creates a new sparse raw disk image of the specified size in bytes.
func (m *VolumeManager) CreateSparse(volumeID string, sizeBytes int64) (string, error) {
	if sizeBytes <= 0 {
		return "", fmt.Errorf("volume size must be positive: %d", sizeBytes)
	}

	targetPath := m.Path(volumeID)
	f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0640)
	if err != nil {
		return "", fmt.Errorf("failed to create sparse volume file %s: %w", targetPath, err)
	}
	defer f.Close()

	if err := f.Truncate(sizeBytes); err != nil {
		_ = os.Remove(targetPath)
		return "", fmt.Errorf("failed to set sparse volume size: %w", err)
	}

	return targetPath, nil
}

// Clone clones a source volume into a new volume using FICLONE (reflink).
// If the underlying filesystem does not support FICLONE, it falls back to sparse copy.
func (m *VolumeManager) Clone(srcPath string, dstVolumeID string) (string, error) {
	dstPath := m.Path(dstVolumeID)
	err := m.CloneFile(srcPath, dstPath)
	if err != nil {
		return "", err
	}
	return dstPath, nil
}

// CloneFile executes a reflink clone from srcPath to dstPath with fallback to sparse copy.
func (m *VolumeManager) CloneFile(srcPath, dstPath string) error {
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", srcPath, err)
	}
	defer srcFile.Close()

	srcStat, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat source file: %w", err)
	}

	dstFile, err := os.OpenFile(dstPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0640)
	if err != nil {
		return fmt.Errorf("failed to create destination file %s: %w", dstPath, err)
	}
	defer dstFile.Close()

	// Attempt kernel reflink via ioctl(FICLONE)
	err = unix.IoctlFileClone(int(dstFile.Fd()), int(srcFile.Fd()))
	if err == nil {
		return nil
	}

	// If FICLONE is unsupported on this filesystem, fall back to sparse copy
	return copySparse(srcFile, dstFile, srcStat.Size())
}

// ReflinkStrict attempts ONLY an ioctl(FICLONE) reflink clone and fails if unsupported.
func (m *VolumeManager) ReflinkStrict(srcPath, dstPath string) error {
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dstPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0640)
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

// Resize expands the volume to the new size in bytes. Shrinking is rejected.
func (m *VolumeManager) Resize(volumeID string, newSizeBytes int64) error {
	targetPath := m.Path(volumeID)
	f, err := os.OpenFile(targetPath, os.O_RDWR, 0640)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrVolumeNotFound
		}
		return fmt.Errorf("failed to open volume for resize: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat volume: %w", err)
	}

	if newSizeBytes < fi.Size() {
		return fmt.Errorf("shrinking volumes is not supported: current %d, requested %d", fi.Size(), newSizeBytes)
	}
	if newSizeBytes == fi.Size() {
		return nil
	}

	if err := f.Truncate(newSizeBytes); err != nil {
		return fmt.Errorf("failed to truncate volume to new size: %w", err)
	}
	return nil
}

// Usage returns the actual disk allocation (allocatedBytes) and logical capacity (totalBytes).
func (m *VolumeManager) Usage(volumeID string) (allocatedBytes int64, totalBytes int64, err error) {
	targetPath := m.Path(volumeID)
	var stat syscall.Stat_t
	if err := syscall.Stat(targetPath, &stat); err != nil {
		if os.IsNotExist(err) {
			return 0, 0, ErrVolumeNotFound
		}
		return 0, 0, fmt.Errorf("failed to stat volume file: %w", err)
	}

	// 512-byte blocks allocated
	allocated := stat.Blocks * 512
	return allocated, stat.Size, nil
}

// Delete removes the volume file from disk.
func (m *VolumeManager) Delete(volumeID string) error {
	targetPath := m.Path(volumeID)
	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete volume %s: %w", targetPath, err)
	}
	return nil
}

// copySparse copies a file preserving sparse holes using SEEK_DATA and SEEK_HOLE when available,
// or chunk-based zero detection.
func copySparse(src *os.File, dst *os.File, size int64) error {
	if err := dst.Truncate(size); err != nil {
		return fmt.Errorf("failed to truncate destination to sparse size: %w", err)
	}

	var offset int64
	for offset < size {
		// Try using lseek SEEK_DATA to jump past holes
		dataOffset, err := unix.Seek(int(src.Fd()), offset, unix.SEEK_DATA)
		if err != nil {
			// If SEEK_DATA error is ENXIO, no more data blocks exist
			if err == unix.ENXIO {
				break
			}
			// If not supported, standard chunk copy
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
			return fmt.Errorf("failed to copy data chunk: %w", err)
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
			if isAllZeros(buf[:n]) {
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

func isAllZeros(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
