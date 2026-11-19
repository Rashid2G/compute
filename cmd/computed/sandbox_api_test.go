package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Rashid2G/compute/internal/config"
	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/network"
	"github.com/Rashid2G/compute/internal/storage"
	"github.com/Rashid2G/compute/internal/vm"
)

func setupTestServerForSandboxAPI(t *testing.T) (*Server, *db.Store) {
	t.Helper()
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "test.db")
	store, err := db.NewStore(storePath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	imagesDir := filepath.Join(tmpDir, "images")
	_ = os.MkdirAll(imagesDir, 0o755)
	_ = os.WriteFile(filepath.Join(imagesDir, "rootfs.ext4"), []byte("test rootfs"), 0o600)

	netMgr, err := network.NewManager(network.Config{SubnetCIDR: "192.168.100.0/24"}, testRunner{})
	if err != nil {
		t.Fatalf("failed to create netMgr: %v", err)
	}

	storMgr, err := storage.NewManager(storage.Config{
		BaseImagesDir: imagesDir,
		VMsDir:        filepath.Join(tmpDir, "vms"),
		DefaultRootfs: "rootfs.ext4",
	})
	if err != nil {
		t.Fatalf("failed to create storMgr: %v", err)
	}

	vmMgr := vm.NewManager(vm.Config{
		Defaults: config.DefaultsConfig{
			VCPUs:      1,
			MemoryMB:   512,
			DiskSizeMB: 1024,
		},
	}, store, netMgr, storMgr)

	cfg := config.DefaultConfig()
	cfg.Server.AuthEnabled = false
	s := NewServer(vmMgr, cfg)
	return s, store
}

func TestSandboxAPI_Files_Validation(t *testing.T) {
	s, store := setupTestServerForSandboxAPI(t)
	ctx := context.Background()

	// 1. Sandbox doesn't exist
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/non-existent/files?path=/tmp/test.txt", nil)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing sandbox, got %d", rr.Code)
	}

	// 2. Sandbox exists but is stopped
	v := &db.VM{
		ID:        "sb-stopped",
		Name:      "stopped-sandbox",
		Status:    db.StatusStopped,
		IPAddress: "192.168.100.15",
	}
	if err := store.SaveVM(ctx, v); err != nil {
		t.Fatalf("failed to save VM: %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/sb-stopped/files?path=/tmp/test.txt", nil)
	rr = httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for stopped sandbox, got %d", rr.Code)
	}
}

func TestSandboxAPI_Processes_Validation(t *testing.T) {
	s, store := setupTestServerForSandboxAPI(t)
	ctx := context.Background()

	v := &db.VM{
		ID:        "sb-stopped-proc",
		Name:      "stopped-proc-sandbox",
		Status:    db.StatusStopped,
		IPAddress: "192.168.100.16",
	}
	if err := store.SaveVM(ctx, v); err != nil {
		t.Fatalf("failed to save VM: %v", err)
	}

	// GET processes on stopped sandbox -> 409
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/sb-stopped-proc/processes", nil)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 for stopped sandbox processes, got %d", rr.Code)
	}
}

func TestSandboxAPI_ServeProxy(t *testing.T) {
	s, store := setupTestServerForSandboxAPI(t)
	ctx := context.Background()

	// 1. Unknown serve host does not proxy to any sandbox.
	req := httptest.NewRequest(http.MethodGet, "/not-a-daemon-route", nil)
	req.Host = "unknown.localhost"
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown serve host, got %d", rr.Code)
	}

	// 2. Stopped sandbox -> 503.
	vStopped := &db.VM{
		ID:        "preview-stopped",
		Name:      "preview-stopped",
		Status:    db.StatusStopped,
		IPAddress: "192.168.100.20",
	}
	_ = store.SaveVM(ctx, vStopped)
	_ = store.SavePortForward(ctx, &db.PortForward{
		ID: "pf_stopped", VMID: vStopped.ID, VMName: vStopped.Name,
		GuestPort: 3000, Protocol: "tcp", Subdomain: "preview-stopped",
		Status: "pending", Public: true,
	})
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "preview-stopped.localhost"
	rr = httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for stopped sandbox serve, got %d", rr.Code)
	}

	// 3. Running sandbox proxies to the guest with the request path unchanged.
	mockGuestServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Mock-Guest", "true")
		fmt.Fprintf(w, "Hello from guest port! Path: %s", r.URL.Path)
	}))
	defer mockGuestServer.Close()

	mockURL, _ := url.Parse(mockGuestServer.URL)
	mockPort, _ := strconv.Atoi(mockURL.Port())
	ip := mockURL.Hostname()

	vRunning := &db.VM{
		ID:        "preview-running",
		Name:      "preview-running",
		Status:    db.StatusRunning,
		IPAddress: ip,
	}
	_ = store.SaveVM(ctx, vRunning)
	_ = store.SavePortForward(ctx, &db.PortForward{
		ID: "pf_running", VMID: vRunning.ID, VMName: vRunning.Name,
		GuestPort: mockPort, Protocol: "tcp", Subdomain: "preview-running",
		Status: "active", Public: true,
	})

	req = httptest.NewRequest(http.MethodGet, "/app/dashboard", nil)
	req.Host = "preview-running.localhost"
	rr = httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 from serve proxy, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("X-Mock-Guest") != "true" {
		t.Fatalf("expected X-Mock-Guest header from proxied response")
	}
	expectedBody := "Hello from guest port! Path: /app/dashboard"
	if rr.Body.String() != expectedBody {
		t.Fatalf("expected body %q, got %q", expectedBody, rr.Body.String())
	}
}

func TestSandboxAPI_CreateWithStartupScript(t *testing.T) {
	s, store := setupTestServerForSandboxAPI(t)
	ctx := context.Background()

	script := "#!/bin/bash\nset -e\necho 'hello from startup script' > /tmp/init.log\n"
	body := fmt.Sprintf(`{"name":"sbx-script-test","vcpus":1,"memory_mb":512,"startup_script":%q}`, script)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", rr.Code, rr.Body.String())
	}

	var created db.VM
	if err := json.NewDecoder(rr.Body).Decode(&created); err != nil {
		t.Fatalf("failed to decode created VM response: %v", err)
	}
	if created.StartupScript != script {
		t.Fatalf("created VM startup_script mismatch: got %q, want %q", created.StartupScript, script)
	}

	// Verify persistence in store
	fromStore, err := store.GetVM(ctx, created.ID)
	if err != nil || fromStore == nil {
		t.Fatalf("GetVM from store failed: %v", err)
	}
	if fromStore.StartupScript != script {
		t.Fatalf("store VM startup_script mismatch: got %q, want %q", fromStore.StartupScript, script)
	}

	// Verify GET /api/v1/sandboxes/:id returns startup_script
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+created.ID, nil)
	getRR := httptest.NewRecorder()
	s.ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from GET, got %d: %s", getRR.Code, getRR.Body.String())
	}
	var inspected db.VM
	if err := json.NewDecoder(getRR.Body).Decode(&inspected); err != nil {
		t.Fatalf("failed to decode GET response: %v", err)
	}
	if inspected.StartupScript != script {
		t.Fatalf("inspected VM startup_script mismatch: got %q, want %q", inspected.StartupScript, script)
	}
}

