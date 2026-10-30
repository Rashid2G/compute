package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/storage"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
)

type testRunner struct{}

func (testRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte("ok"), nil
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	root := t.TempDir()
	store, err := db.NewStore(filepath.Join(root, "db", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	imagesDir := filepath.Join(root, "images")
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imagesDir, "rootfs.ext4"), []byte("test rootfs"), 0o600); err != nil {
		t.Fatal(err)
	}
	storMgr, err := storage.NewManager(storage.Config{
		BaseImagesDir: imagesDir,
		VMsDir:        filepath.Join(root, "vms"),
		DefaultRootfs: "rootfs.ext4",
	})
	if err != nil {
		t.Fatal(err)
	}
	netMgr, err := network.NewManager(network.Config{SubnetCIDR: "192.168.50.0/24"}, testRunner{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(vm.NewManager(vm.Config{}, store, netMgr, storMgr)))
	t.Cleanup(server.Close)
	return server
}

func TestHealth(t *testing.T) {
	server := newTestServer(t)
	resp, err := http.Get(server.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "ok" {
		t.Fatalf("body = %#v, want status ok", got)
	}
}

func TestVMEndpoints(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()
	request := func(method, path string, body any) (*http.Response, error) {
		t.Helper()
		var reader *strings.Reader
		if body != nil {
			data, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			reader = strings.NewReader(string(data))
		} else {
			reader = strings.NewReader("")
		}
		req, err := http.NewRequest(method, server.URL+path, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return client.Do(req)
	}

	resp, err := request(http.MethodGet, "/api/v1/vms", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	createReq := vm.CreateRequest{Name: "api-created", VCPUs: 2, MemoryMB: 512}
	resp, err = request(http.MethodPost, "/api/v1/vms", createReq)
	if err != nil {
		t.Fatal(err)
	}
	var created db.VM
	if resp.StatusCode != http.StatusCreated {
		resp.Body.Close()
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if created.ID == "" || created.Name != "api-created" {
		t.Fatalf("unexpected created VM: %+v", created)
	}

	resp, err = request(http.MethodGet, "/api/v1/vms/"+created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var fetched db.VM
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("get status = %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&fetched); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if fetched.ID != created.ID {
		t.Fatalf("fetched VM ID = %q, want %q", fetched.ID, created.ID)
	}

	resp, err = request(http.MethodGet, "/api/v1/vms/does-not-exist", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing VM status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	resp, err = request(http.MethodPost, "/api/v1/vms/"+created.ID+"/stop", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	resp, err = request(http.MethodDelete, "/api/v1/vms/"+created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestNetworkEndpoints(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()

	// 1. GET /api/v1/network
	resp, err := client.Get(server.URL + "/api/v1/network")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /network status = %d", resp.StatusCode)
	}
	var info network.NetworkInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.Mode != network.ModeNAT {
		t.Fatalf("expected mode nat, got %s", info.Mode)
	}

	// 2. GET /api/v1/network/allocations
	resp2, err := client.Get(server.URL + "/api/v1/network/allocations")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET /network/allocations status = %d", resp2.StatusCode)
	}

	// 3. POST /api/v1/network/prune
	resp3, err := client.Post(server.URL+"/api/v1/network/prune", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("POST /network/prune status = %d", resp3.StatusCode)
	}
	var pruneRes struct {
		Status     string   `json:"status"`
		PrunedTAPs []string `json:"pruned_taps"`
	}
	if err := json.NewDecoder(resp3.Body).Decode(&pruneRes); err != nil {
		t.Fatal(err)
	}
	if pruneRes.Status != "ok" {
		t.Fatalf("expected status ok, got %s", pruneRes.Status)
	}
}
