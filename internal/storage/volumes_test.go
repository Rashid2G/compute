package storage

import (
	"os"
	"testing"
)

func newVolumeTestManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	mgr, err := NewManager(Config{
		BaseImagesDir: root + "/images",
		VMsDir:        root + "/vms",
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

func TestDataVolumeLifecycle(t *testing.T) {
	mgr := newVolumeTestManager(t)

	path, err := mgr.CreateDataVolume("vol_test", 1, "ext4")
	if err != nil {
		t.Fatalf("CreateDataVolume: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat volume: %v", err)
	}
	if got := info.Size(); got != 1<<30 {
		t.Fatalf("volume size = %d, want %d", got, int64(1<<30))
	}

	if err := mgr.ExpandDataVolume(path, 2, "ext4"); err != nil {
		t.Fatalf("ExpandDataVolume: %v", err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatalf("stat expanded volume: %v", err)
	}
	if got := info.Size(); got != 2<<30 {
		t.Fatalf("expanded volume size = %d, want %d", got, int64(2<<30))
	}

	snapshot, err := mgr.SnapshotDataVolume(path, 1)
	if err != nil {
		t.Fatalf("SnapshotDataVolume: %v", err)
	}
	if _, err := os.Stat(snapshot); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}

	if err := mgr.DeleteDataVolume(path); err != nil {
		t.Fatalf("DeleteDataVolume: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("volume image still exists after delete: %v", err)
	}
	if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
		t.Fatalf("snapshot still exists after delete: %v", err)
	}
}

func TestExpandDataVolumeRejectsXFS(t *testing.T) {
	mgr := newVolumeTestManager(t)
	path, err := mgr.CreateDataVolume("vol_xfs", 1, "xfs")
	if err != nil {
		t.Skipf("xfs tooling unavailable: %v", err)
	}
	if err := mgr.ExpandDataVolume(path, 2, "xfs"); err == nil {
		t.Fatal("expected offline XFS expansion to be rejected")
	}
}
