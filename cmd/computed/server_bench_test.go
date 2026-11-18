package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/config"
	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/network"
	"github.com/Rashid2G/compute/internal/storage"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/Rashid2G/compute/pkg/api"
)

func newBenchServer(b *testing.B) (*Server, *db.Store) {
	b.Helper()
	tmpDir := b.TempDir()
	store, err := db.NewStore(filepath.Join(tmpDir, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	netMgr, err := network.NewManager(network.Config{BridgeName: "br0", BridgeIP: "172.16.0.1/16", SubnetCIDR: "172.16.0.0/16"}, nil)
	if err != nil {
		b.Fatal(err)
	}
	storMgr, err := storage.NewManager(storage.Config{BaseImagesDir: tmpDir, VMsDir: filepath.Join(tmpDir, "vms")})
	if err != nil {
		b.Fatal(err)
	}
	vmMgr := vm.NewManager(vm.Config{}, store, netMgr, storMgr)
	return NewServer(vmMgr, config.DefaultConfig()), store
}

func seedBenchVMs(b *testing.B, store *db.Store, n int) {
	b.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		v := &db.VM{
			ID:        fmt.Sprintf("vm-bench-%04d", i),
			OwnerID:   "bench",
			Name:      fmt.Sprintf("bench-%04d", i),
			VCPUs:     2,
			MemoryMB:  1024,
			DiskMB:    10240,
			Status:    db.StatusRunning,
			WorkerID:  "nod_host_local",
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := store.SaveVM(ctx, v); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkServerHealth(b *testing.B) {
	srv, _ := newBenchServer(b)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("expected 200, got %d", rec.Code)
		}
	}
}

func BenchmarkServerListVMs100(b *testing.B) {
	srv, store := newBenchServer(b)
	seedBenchVMs(b, store, 100)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/vms", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("expected 200, got %d", rec.Code)
		}
	}
}

func BenchmarkServerListTasks100(b *testing.B) {
	srv, _ := newBenchServer(b)
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		_ = srv.saveTask(ctx, &taskRecord{
			ID:        fmt.Sprintf("task-bench-%04d", i),
			Action:    "create",
			Status:    "completed",
			Requested: 10,
			Queued:    10,
			Succeeded: []string{"a", "b", "c"},
			Failed:    []api.BulkVMItemError{},
		})
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("expected 200, got %d", rec.Code)
		}
	}
}

func BenchmarkServerListWorkers100VMs(b *testing.B) {
	srv, store := newBenchServer(b)
	seedBenchVMs(b, store, 100)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.SaveWorkerNode(ctx, &db.WorkerNode{
		ID:             "nod_host_local",
		Name:           "bench-host",
		PoolID:         "default",
		Status:         "ready",
		TotalVCPUs:     64,
		TotalMemMB:     65536,
		TotalDiskMB:    512000,
		PhysicalVCPUs:  64,
		PhysicalMemMB:  65536,
		PhysicalDiskMB: 512000,
		LastHeartbeat:  &now,
	}); err != nil {
		b.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workers", nil)
	// The workers endpoint is admin-only when auth is enabled; the benchmark
	// server runs with auth disabled, so the request is allowed.

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("expected 200, got %d", rec.Code)
		}
	}
}
