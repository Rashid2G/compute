package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStorageManager(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "compute-storage-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	baseImages := filepath.Join(tmpDir, "images")
	vmsDir := filepath.Join(tmpDir, "vms")
	os.MkdirAll(baseImages, 0755)

	// Create dummy base rootfs for Ubuntu 24
	dummyRootfs := filepath.Join(baseImages, "ubuntu-24.04.ext4")
	if err := os.WriteFile(dummyRootfs, []byte("dummy-ext4-data"), 0644); err != nil {
		t.Fatalf("failed to create dummy rootfs: %v", err)
	}

	mgr, err := NewManager(Config{
		BaseImagesDir: baseImages,
		VMsDir:        vmsDir,
		DefaultRootfs: "ubuntu-24.04.ext4",
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

	// Test disk cloning
	clonePath, err := mgr.CloneVMDisk("vm-test-123", "vm-test-456", 0)
	if err != nil {
		t.Fatalf("CloneVMDisk failed: %v", err)
	}
	cloneData, err := os.ReadFile(clonePath)
	if err != nil || string(cloneData) != "dummy-ext4-data" {
		t.Fatalf("unexpected content in cloned vm disk: %v", err)
	}

	snapState, snapMem := mgr.GetSnapshotPaths("vm-test-123")
	if filepath.Base(snapState) != "snapshot.state" || filepath.Base(snapMem) != "snapshot.mem" {
		t.Fatalf("unexpected snapshot paths: %s, %s", snapState, snapMem)
	}

	if err := mgr.CleanupVM("vm-test-123"); err != nil {
		t.Fatalf("CleanupVM failed: %v", err)
	}
	if err := mgr.CleanupVM("vm-test-456"); err != nil {
		t.Fatalf("CleanupVM clone failed: %v", err)
	}

	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Fatalf("expected disk to be removed after cleanup")
	}
	if _, err := os.Stat(clonePath); !os.IsNotExist(err) {
		t.Fatalf("expected cloned disk to be removed after cleanup")
	}
}

func TestStorageManager_TemplatesAndCustomImages(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "compute-storage-tpl-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	baseImages := filepath.Join(tmpDir, "images")
	templatesDir := filepath.Join(tmpDir, "templates")
	vmsDir := filepath.Join(tmpDir, "vms")
	os.MkdirAll(baseImages, 0755)
	os.MkdirAll(templatesDir, 0755)

	dummyRootfs := filepath.Join(baseImages, "ubuntu-24.04.ext4")
	if err := os.WriteFile(dummyRootfs, []byte("rootfs-data"), 0644); err != nil {
		t.Fatalf("failed to write dummy rootfs: %v", err)
	}
	dummyKernel := filepath.Join(baseImages, "vmlinux")
	if err := os.WriteFile(dummyKernel, []byte("kernel-data"), 0644); err != nil {
		t.Fatalf("failed to write dummy kernel: %v", err)
	}

	mgr, err := NewManager(Config{
		BaseImagesDir: baseImages,
		TemplatesDir:  templatesDir,
		VMsDir:        vmsDir,
		DefaultRootfs: "ubuntu-24.04.ext4",
		DefaultKernel: "vmlinux",
	})
	if err != nil {
		t.Fatalf("failed to create storage manager: %v", err)
	}

	// 1. Initialize a VM disk
	vmDisk, err := mgr.InitVMDisk("vm-src-1")
	if err != nil {
		t.Fatalf("InitVMDisk failed: %v", err)
	}
	// Modify the VM disk slightly to simulate user shell modifications
	if err := os.WriteFile(vmDisk, []byte("rootfs-with-user-shell-changes"), 0644); err != nil {
		t.Fatalf("failed to modify vm disk: %v", err)
	}

	// 2. Create template from that modified VM disk
	tplPath, err := mgr.CreateTemplateDiskFromVM("vm-src-1", "tpl-web-custom")
	if err != nil {
		t.Fatalf("CreateTemplateDiskFromVM failed: %v", err)
	}

	tplData, err := os.ReadFile(tplPath)
	if err != nil || string(tplData) != "rootfs-with-user-shell-changes" {
		t.Fatalf("template disk does not contain user shell modifications: %s", string(tplData))
	}

	// 3. Resolve template by name
	kPath, rPath, err := mgr.ResolveImage("tpl-web-custom")
	if err != nil {
		t.Fatalf("ResolveImage failed for template: %v", err)
	}
	if rPath != tplPath || kPath != dummyKernel {
		t.Fatalf("unexpected resolve paths: %s, %s", kPath, rPath)
	}

	// 4. Provision a new VM from that template
	newVMDisk, err := mgr.InitVMDiskFromImage("vm-from-tpl", tplPath)
	if err != nil {
		t.Fatalf("InitVMDiskFromImage failed: %v", err)
	}
	newVMData, err := os.ReadFile(newVMDisk)
	if err != nil || string(newVMData) != "rootfs-with-user-shell-changes" {
		t.Fatalf("new VM cloned from template does not match template: %s", string(newVMData))
	}

	// 5. Clean up
	_ = mgr.DeleteTemplateFile("tpl-web-custom")
	if _, err := os.Stat(tplPath); !os.IsNotExist(err) {
		t.Fatalf("expected template disk file to be deleted")
	}
}
