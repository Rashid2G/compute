package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCloneFileCOW_ContentIntegrity(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "source.raw")
	dstPath := filepath.Join(tmpDir, "dest.raw")

	// Create test file with patterned non-zero data and zero sparse sections
	testData := make([]byte, 1024*1024) // 1MB
	for i := 0; i < len(testData); i++ {
		if i%4096 < 512 {
			testData[i] = byte(i % 255)
		}
	}

	if err := os.WriteFile(srcPath, testData, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Run CloneFileCOW
	if err := CloneFileCOW(srcPath, dstPath); err != nil {
		t.Fatalf("CloneFileCOW failed: %v", err)
	}

	// Verify destination exists and size matches
	fi, err := os.Stat(dstPath)
	if err != nil {
		t.Fatalf("dest file not found: %v", err)
	}
	if fi.Size() != int64(len(testData)) {
		t.Fatalf("expected dest size %d, got %d", len(testData), fi.Size())
	}

	// Verify content matches exactly
	readBack, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("failed to read back dest file: %v", err)
	}
	if !bytes.Equal(testData, readBack) {
		t.Fatalf("dest file content does not match source file")
	}
}

func TestCloneFileCOW_SparseFile(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "sparse_src.raw")
	dstPath := filepath.Join(tmpDir, "sparse_dst.raw")

	// Create a large sparse file (10MB) with data only at offset 5MB
	f, err := os.Create(srcPath)
	if err != nil {
		t.Fatalf("failed to create sparse file: %v", err)
	}
	const totalSize = 10 * 1024 * 1024
	if err := f.Truncate(totalSize); err != nil {
		t.Fatalf("failed to truncate: %v", err)
	}
	data := []byte("hello sparse cow world")
	if _, err := f.WriteAt(data, 5*1024*1024); err != nil {
		t.Fatalf("failed to write data: %v", err)
	}
	f.Close()

	if err := CloneFileCOW(srcPath, dstPath); err != nil {
		t.Fatalf("CloneFileCOW failed on sparse file: %v", err)
	}

	dstStat, err := os.Stat(dstPath)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if dstStat.Size() != totalSize {
		t.Fatalf("expected size %d, got %d", totalSize, dstStat.Size())
	}

	// Verify content at offset
	dstF, err := os.Open(dstPath)
	if err != nil {
		t.Fatalf("failed to open dst: %v", err)
	}
	defer dstF.Close()

	buf := make([]byte, len(data))
	if _, err := dstF.ReadAt(buf, 5*1024*1024); err != nil {
		t.Fatalf("failed to read at offset: %v", err)
	}
	if !bytes.Equal(buf, data) {
		t.Fatalf("expected %q, got %q", string(data), string(buf))
	}
}

func TestReflinkStrict_UnsupportedHandling(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "reflink_src.raw")
	dstPath := filepath.Join(tmpDir, "reflink_dst.raw")

	if err := os.WriteFile(srcPath, []byte("data"), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	err := ReflinkStrict(srcPath, dstPath)
	// In ext4 without reflink support, ReflinkStrict should return ErrReflinkNotSupported
	if err != nil {
		if !os.IsNotExist(err) {
			// destination should be cleaned up on error
			if _, statErr := os.Stat(dstPath); !os.IsNotExist(statErr) {
				t.Fatalf("expected destination to be removed after ReflinkStrict failure")
			}
		}
	}
}

func TestCloneFileCOW_SameSourceAndDestination(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "golden_template.raw")
	origContent := []byte("critical-golden-template-data-do-not-truncate")

	if err := os.WriteFile(srcPath, origContent, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// 1. Same exact path string
	if err := CloneFileCOW(srcPath, srcPath); err != nil {
		t.Fatalf("CloneFileCOW with identical path failed: %v", err)
	}

	data, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("failed to read srcPath: %v", err)
	}
	if !bytes.Equal(data, origContent) {
		t.Fatalf("srcPath was truncated or corrupted when cloned to itself; got %q", string(data))
	}

	// 2. Different path resolving to same inode (hardlink) must decouple inode
	hardlinkPath := filepath.Join(tmpDir, "same_inode.raw")
	if err := os.Link(srcPath, hardlinkPath); err != nil {
		t.Fatalf("failed to create hardlink for test: %v", err)
	}

	if err := CloneFileCOW(srcPath, hardlinkPath); err != nil {
		t.Fatalf("CloneFileCOW with same inode hardlink failed: %v", err)
	}

	srcStat, err := os.Stat(srcPath)
	if err != nil {
		t.Fatalf("failed to stat src: %v", err)
	}
	dstStat, err := os.Stat(hardlinkPath)
	if err != nil {
		t.Fatalf("failed to stat dst: %v", err)
	}
	if os.SameFile(srcStat, dstStat) {
		t.Fatalf("expected hardlink to be decoupled into an independent inode, but os.SameFile returned true")
	}

	// Mutating decoupled destination must NOT mutate source
	mutatedData := []byte("mutated-destination-data-no-bleed")
	if err := os.WriteFile(hardlinkPath, mutatedData, 0644); err != nil {
		t.Fatalf("failed to write to destination: %v", err)
	}

	dataAfterHardlink, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("failed to read srcPath after hardlink clone: %v", err)
	}
	if !bytes.Equal(dataAfterHardlink, origContent) {
		t.Fatalf("MUTATION BLEED: srcPath was modified by write to decoupled clone; got %q, expected %q", string(dataAfterHardlink), string(origContent))
	}
}

func TestCloneFileCOW_CanonicalPathMismatch_NoDeletion(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to getwd: %v", err)
	}
	defer func() { _ = os.Chdir(origWd) }()

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir to tmpDir: %v", err)
	}

	fileName := "ubuntu.raw"
	absPath := filepath.Join(tmpDir, fileName)
	content := []byte("critical-ubuntu-golden-image-data-do-not-delete")
	if err := os.WriteFile(absPath, content, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// 1. src is relative path ("ubuntu.raw"), dst is absolute path (/tmp/.../ubuntu.raw)
	relPath := fileName
	if err := CloneFileCOW(relPath, absPath); err != nil {
		t.Fatalf("CloneFileCOW failed on relative vs absolute same file: %v", err)
	}

	// Verify file was NOT deleted
	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("FILE WAS CATASTROPHICALLY DELETED! os.ReadFile failed: %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("file content was corrupted; got %q, expected %q", string(data), string(content))
	}

	// 2. Vice versa: src is absolute, dst is relative
	if err := CloneFileCOW(absPath, relPath); err != nil {
		t.Fatalf("CloneFileCOW failed on absolute vs relative same file: %v", err)
	}
	data, err = os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("FILE WAS DELETED on absolute->relative clone! %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("file content corrupted; got %q, expected %q", string(data), string(content))
	}

	// 3. Symlink pointing to the same file
	symlinkPath := filepath.Join(tmpDir, "symlink_ubuntu.raw")
	if err := os.Symlink(absPath, symlinkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	if err := CloneFileCOW(symlinkPath, absPath); err != nil {
		t.Fatalf("CloneFileCOW failed on symlink vs target: %v", err)
	}
	data, err = os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("FILE WAS DELETED on symlink->target clone! %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("file content corrupted; got %q, expected %q", string(data), string(content))
	}

	// 4. Also test ReflinkStrict with relative vs absolute
	if err := ReflinkStrict(relPath, absPath); err != nil {
		t.Fatalf("ReflinkStrict failed on relative vs absolute same file: %v", err)
	}
	data, err = os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("FILE WAS DELETED on ReflinkStrict! %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("file content corrupted; got %q, expected %q", string(data), string(content))
	}
}
