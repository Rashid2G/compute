package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStorageManager(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mforge-storage-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	baseImages := filepath.Join(tmpDir, "images")
	vmsDir := filepath.Join(tmpDir, "vms")
	os.MkdirAll(baseImages, 0755)

	// Create dummy base rootfs
	dummyRootfs := filepath.Join(baseImages, "alpine-3.20.ext4")
	if err := os.WriteFile(dummyRootfs, []byte("dummy-ext4-data"), 0644); err != nil {
		t.Fatalf("failed to create dummy rootfs: %v", err)
	}

	mgr, err := NewManager(Config{
		BaseImagesDir: baseImages,
		VMsDir:        vmsDir,
	})
	if err != nil {
		t.Fatalf("failed to create storage manager: %v", err)
	}

	diskPath, err := mgr.InitVMDisk("vm-test-123")
	if err != nil {
		t.Fatalf("InitVMDisk failed: %v", err)
	}

	data, err := os.ReadFile(diskPath)
	if err != nil || string(data) != "dummy-ext4-data" {
		t.Fatalf("unexpected content in cloned disk: %v", err)
	}

	snapState, snapMem := mgr.GetSnapshotPaths("vm-test-123")
	if filepath.Base(snapState) != "snapshot.state" || filepath.Base(snapMem) != "snapshot.mem" {
		t.Fatalf("unexpected snapshot paths: %s, %s", snapState, snapMem)
	}

	if err := mgr.CleanupVM("vm-test-123"); err != nil {
		t.Fatalf("CleanupVM failed: %v", err)
	}

	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Fatalf("expected disk to be removed after cleanup")
	}
}
