package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "mforge-db-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	vm := &VM{
		ID:         "vm-test-1",
		Name:       "test-vm",
		Status:     StatusCreated,
		VCPUs:      1,
		MemoryMB:   512,
		IPAddress:  "172.16.0.2",
		MACAddress: "06:00:ac:10:00:02",
		TAPDevice:  "mftap-test-1",
		RootfsPath: "/tmp/rootfs.ext4",
		KernelPath: "/tmp/vmlinux",
		DiskPath:   "/tmp/disk.ext4",
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}

	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatalf("failed to save VM: %v", err)
	}

	got, err := store.GetVM(ctx, "vm-test-1")
	if err != nil {
		t.Fatalf("failed to get VM: %v", err)
	}
	if got == nil || got.ID != "vm-test-1" || got.Status != StatusCreated {
		t.Fatalf("unexpected VM retrieved: %+v", got)
	}

	// Update status
	got.Status = StatusRunning
	if err := store.SaveVM(ctx, got); err != nil {
		t.Fatalf("failed to update VM: %v", err)
	}

	updated, _ := store.GetVM(ctx, "vm-test-1")
	if updated.Status != StatusRunning {
		t.Fatalf("expected status running, got %s", updated.Status)
	}

	vms, err := store.ListVMs(ctx)
	if err != nil || len(vms) != 1 {
		t.Fatalf("expected 1 VM, got %d (err: %v)", len(vms), err)
	}

	ips, err := store.GetAllocatedIPs(ctx)
	if err != nil || !ips["172.16.0.2"] {
		t.Fatalf("expected IP 172.16.0.2 in allocated list: %v", ips)
	}

	if err := store.DeleteVM(ctx, "vm-test-1"); err != nil {
		t.Fatalf("failed to delete VM: %v", err)
	}

	deleted, _ := store.GetVM(ctx, "vm-test-1")
	if deleted != nil {
		t.Fatalf("expected nil after delete, got %+v", deleted)
	}
}
