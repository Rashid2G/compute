package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Rashid2G/compute/internal/config"
	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/network"
	"github.com/Rashid2G/compute/internal/storage"
	"github.com/Rashid2G/compute/internal/vm"
)

func BenchmarkServerHealth(b *testing.B) {
	tmpDir := b.TempDir()
	store, _ := db.NewStore(filepath.Join(tmpDir, "bench.db"))
	defer store.Close()
	netMgr, _ := network.NewManager(network.Config{BridgeName: "br0", BridgeIP: "172.16.0.1/16", SubnetCIDR: "172.16.0.0/16"}, nil)
	storMgr, _ := storage.NewManager(storage.Config{BaseImagesDir: tmpDir, VMsDir: filepath.Join(tmpDir, "vms")})
	vmMgr := vm.NewManager(vm.Config{}, store, netMgr, storMgr)
	srv := NewServer(vmMgr, config.DefaultConfig())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("expected 200, got %d", rec.Code)
		}
	}
}

func BenchmarkServerListVMs(b *testing.B) {
	tmpDir := b.TempDir()
	store, _ := db.NewStore(filepath.Join(tmpDir, "bench.db"))
	defer store.Close()
	netMgr, _ := network.NewManager(network.Config{BridgeName: "br0", BridgeIP: "172.16.0.1/16", SubnetCIDR: "172.16.0.0/16"}, nil)
	storMgr, _ := storage.NewManager(storage.Config{BaseImagesDir: tmpDir, VMsDir: filepath.Join(tmpDir, "vms")})
	vmMgr := vm.NewManager(vm.Config{}, store, netMgr, storMgr)
	srv := NewServer(vmMgr, config.DefaultConfig())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/vms", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("expected 200, got %d", rec.Code)
		}
	}
}
