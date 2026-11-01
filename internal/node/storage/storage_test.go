package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVolumeManager_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewVolumeManager(tmpDir)
	if err != nil {
		t.Fatalf("NewVolumeManager failed: %v", err)
	}

	volID := "vol_test1"
	const size50GB = 50 * 1024 * 1024 * 1024 // 50 GiB

	// 1. Create sparse file
	path, err := mgr.CreateSparse(volID, size50GB)
	if err != nil {
		t.Fatalf("CreateSparse failed: %v", err)
	}

	// 2. Verify usage: logical size is 50GB, physical allocation is near zero
	allocated, total, err := mgr.Usage(volID)
	if err != nil {
		t.Fatalf("Usage failed: %v", err)
	}
	if total != size50GB {
		t.Errorf("expected logical size %d, got %d", size50GB, total)
	}
	if allocated > 10*1024*1024 { // Sparse file should consume < 10MB
		t.Errorf("expected allocated blocks to be tiny, got %d bytes", allocated)
	}

	// 3. Clone
	cloneID := "vol_test1_clone"
	clonePath, err := mgr.Clone(path, cloneID)
	if err != nil {
		t.Fatalf("Clone failed: %v", err)
	}
	_ = clonePath
	defer mgr.Delete(cloneID)

	cloneAlloc, cloneTotal, err := mgr.Usage(cloneID)
	if err != nil {
		t.Fatalf("Usage of clone failed: %v", err)
	}
	if cloneTotal != size50GB {
		t.Errorf("expected clone logical size %d, got %d", size50GB, cloneTotal)
	}
	if cloneAlloc > 10*1024*1024 {
		t.Errorf("expected clone allocated bytes to be tiny, got %d", cloneAlloc)
	}

	// 4. Resize
	const size60GB = 60 * 1024 * 1024 * 1024
	if err := mgr.Resize(volID, size60GB); err != nil {
		t.Fatalf("Resize failed: %v", err)
	}
	_, newTotal, err := mgr.Usage(volID)
	if err != nil {
		t.Fatalf("Usage after resize failed: %v", err)
	}
	if newTotal != size60GB {
		t.Errorf("expected new size %d, got %d", size60GB, newTotal)
	}

	// Shrinking must fail
	const size40GB = 40 * 1024 * 1024 * 1024
	if err := mgr.Resize(volID, size40GB); err == nil {
		t.Error("expected error when shrinking volume, got nil")
	}

	// 5. Delete
	if err := mgr.Delete(volID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file %s still exists after delete", path)
	}
}

func BenchmarkReflink50GB(b *testing.B) {
	tmpDir := b.TempDir()
	mgr, err := NewVolumeManager(tmpDir)
	if err != nil {
		b.Fatalf("NewVolumeManager failed: %v", err)
	}

	const size50GB = 50 * 1024 * 1024 * 1024
	srcPath, err := mgr.CreateSparse("bench_src", size50GB)
	if err != nil {
		b.Fatalf("CreateSparse failed: %v", err)
	}

	// Write some non-zero data at start to simulate a real rootfs partition header
	f, err := os.OpenFile(srcPath, os.O_WRONLY, 0640)
	if err != nil {
		b.Fatalf("failed to open src: %v", err)
	}
	dummyData := make([]byte, 64*1024)
	for i := range dummyData {
		dummyData[i] = byte(i % 256)
	}
	_, _ = f.Write(dummyData)
	_ = f.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dstID := filepath.Join(tmpDir, "dst")
		start := time.Now()
		_ = mgr.CloneFile(srcPath, dstID)
		elapsed := time.Since(start)
		_ = os.Remove(dstID)
		b.ReportMetric(float64(elapsed.Milliseconds()), "ms/clone")
	}
}
