package db

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkStoreSaveAndGetVM(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")
	store, err := NewStore(dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vmID := fmt.Sprintf("vm-bench-%d", i)
		vm := &VM{
			ID:         vmID,
			Name:       "bench",
			Status:     StatusCreated,
			VCPUs:      1,
			MemoryMB:   512,
			IPAddress:  "172.16.1.10",
			MACAddress: "06:00:ac:10:01:0a",
			TAPDevice:  "mftap-bench",
			CreatedAt:  time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
		}
		if err := store.SaveVM(ctx, vm); err != nil {
			b.Fatalf("SaveVM failed: %v", err)
		}
		_, err = store.GetVM(ctx, vmID)
		if err != nil {
			b.Fatalf("GetVM failed: %v", err)
		}
	}
}

func BenchmarkStoreListVMs(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench-list.db")
	store, err := NewStore(dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	// Prepopulate with 50 VMs
	for i := 0; i < 50; i++ {
		vmID := fmt.Sprintf("vm-seed-%d", i)
		_ = store.SaveVM(ctx, &VM{
			ID:         vmID,
			Name:       "seed",
			Status:     StatusRunning,
			VCPUs:      2,
			MemoryMB:   1024,
			IPAddress:  fmt.Sprintf("172.16.0.%d", i+2),
			MACAddress: "06:00:ac:10:00:02",
			TAPDevice:  "tap-seed",
			CreatedAt:  time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vms, err := store.ListVMs(ctx)
		if err != nil || len(vms) != 50 {
			b.Fatalf("ListVMs failed: %v", err)
		}
	}
}
