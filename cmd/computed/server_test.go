package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/coder/websocket"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/config"
	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/network"
	"github.com/Rashid2G/compute/internal/storage"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/Rashid2G/compute/pkg/api"
	"github.com/Rashid2G/compute/pkg/version"
)

type testRunner struct{}

func (testRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte("ok"), nil
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server, _ := newTestServerWithVMManager(t)
	return server
}

// withTestHostCapacity gives unit-test hosts a generous logical capacity so
// tests can exercise explicit disk/memory sizes without depending on the size
// of the CI runner's root filesystem.
func withTestHostCapacity(cfg *config.GlobalConfig) {
	cfg.Resources.MaxTotalVCPUs = 256
	cfg.Resources.MaxTotalMemoryMB = 1 << 20
	cfg.Resources.MaxTotalDiskMB = 1 << 20
	cfg.Resources.MinFreeDiskSpaceMB = 0
}

func newTestServerWithVMManager(t *testing.T) (*httptest.Server, *vm.Manager) {
	t.Helper()
	root := t.TempDir()
	store, err := db.NewStore(filepath.Join(root, "db", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	policy := db.DefaultSchedulingPolicy()
	policy.HostReservedDiskMB = 0
	if err := store.SaveSchedulingPolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}

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
	if avail, err := storMgr.GetAvailableDiskSpaceMB(); err == nil && avail < 256 {
		t.Skipf("skipping test: insufficient free disk space on %s (%d MB < 256 MB)", root, avail)
	}
	netMgr, err := network.NewManager(network.Config{SubnetCIDR: "192.168.50.0/24"}, testRunner{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.ConfigPath = "config/daemon.json"
	// Unit tests should not boot job sandboxes in the background.
	cfg.Jobs.LocalExecution = false
	// Keep unit-test disks small: the test host root filesystem is often a few
	// GB, and physical capacity is now enforced during placement.
	cfg.Defaults.DiskSizeMB = 1024
	withTestHostCapacity(&cfg)
	vmMgr := vm.NewManager(vm.Config{Defaults: cfg.Defaults, Resources: cfg.Resources}, store, netMgr, storMgr)
	server := httptest.NewServer(NewServer(vmMgr, cfg))
	t.Cleanup(server.Close)
	return server, vmMgr
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

func TestConfigEndpoint(t *testing.T) {
	server := newTestServer(t)
	resp, err := http.Get(server.URL + "/api/v1/config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/config status = %d", resp.StatusCode)
	}

	var cfg config.GlobalConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode config error: %v", err)
	}
	if cfg.Server.ListenAddr == "" {
		t.Fatalf("expected listen address in config response")
	}
	if cfg.ConfigPath != "config/daemon.json" {
		t.Fatalf("expected config path config/daemon.json, got %s", cfg.ConfigPath)
	}
	if cfg.Defaults.VCPUs <= 0 {
		t.Fatalf("expected positive default vCPUs")
	}
}

func TestQueueEndpoint(t *testing.T) {
	server := newTestServer(t)
	resp, err := http.Get(server.URL + "/api/v1/queue")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/queue status = %d", resp.StatusCode)
	}

	var res struct {
		QueuedVMs []*vm.QueueItem `json:"queued_vms"`
		Count     int             `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode queue error: %v", err)
	}
	if res.Count != 0 {
		t.Fatalf("expected 0 queued vms, got %d", res.Count)
	}
}

func TestUnsupportedImageOnCreate(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()

	// Try creating with a nonexistent/unsupported image
	createReq := vm.CreateRequest{Name: "bad-img-vm", Image: "nonexistent-distro"}
	data, _ := json.Marshal(createReq)
	resp, err := client.Post(server.URL+"/api/v1/vms", "application/json", strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for unsupported image, got %d", resp.StatusCode)
	}

	var errResp map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&errResp)
	if !strings.Contains(errResp["error"], "unsupported image") {
		t.Fatalf("expected error mentioning 'unsupported image', got: %v", errResp)
	}
	// Verify it does NOT misclassify as disk space issue
	if strings.Contains(errResp["error"], "disk space issue") {
		t.Fatalf("unexpected 'disk space issue' in image error: %v", errResp)
	}
}

func TestProtectedVMDeleteReportsHumanReadableName(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()

	// Create a protected sandbox named user-flow-box
	prot := true
	createReq := vm.CreateRequest{Name: "user-flow-box", Protected: &prot}
	data, _ := json.Marshal(createReq)
	resp, err := client.Post(server.URL+"/api/v1/vms", "application/json", strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", resp.StatusCode)
	}

	// Attempt to delete it
	delReq, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/vms/user-flow-box", nil)
	delResp, err := client.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	defer delResp.Body.Close()

	if delResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", delResp.StatusCode)
	}
	var errResp map[string]string
	_ = json.NewDecoder(delResp.Body).Decode(&errResp)

	errMsg := errResp["error"]
	expectedMsg := "item user-flow-box is protected against deletion"
	if errMsg != expectedMsg {
		t.Fatalf("expected exact error %q, got %q", expectedMsg, errMsg)
	}
	// Verify no internal identifier ins_ was leaked
	if strings.Contains(errMsg, "ins_") {
		t.Fatalf("internal database identifier leaked in error: %q", errMsg)
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

func TestServer_DeletionProtectionEndpoints(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()

	request := func(method, path string, body interface{}) (*http.Response, error) {
		var r io.Reader
		if body != nil {
			data, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			r = bytes.NewReader(data)
		}
		req, err := http.NewRequest(method, server.URL+path, r)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return client.Do(req)
	}

	prot := true
	createReq := vm.CreateRequest{Name: "api-prot", VCPUs: 1, MemoryMB: 512, Protected: &prot}
	resp, err := request(http.MethodPost, "/api/v1/vms", createReq)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created db.VM
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if !created.Protected {
		t.Fatalf("expected created VM to be protected")
	}

	// DELETE should be rejected with 400 Bad Request
	resp, err = request(http.MethodDelete, "/api/v1/vms/"+created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete protected status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	resp.Body.Close()

	// POST /protect should be idempotent
	resp, err = request(http.MethodPost, "/api/v1/vms/"+created.ID+"/protect", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("protect status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// POST /unprotect should unprotect
	resp, err = request(http.MethodPost, "/api/v1/vms/"+created.ID+"/unprotect", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unprotect status = %d", resp.StatusCode)
	}
	var unprotVM db.VM
	_ = json.NewDecoder(resp.Body).Decode(&unprotVM)
	resp.Body.Close()
	if unprotVM.Protected {
		t.Fatalf("expected unprotect to set Protected = false")
	}

	// DELETE should now succeed
	resp, err = request(http.MethodDelete, "/api/v1/vms/"+created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete unprotected status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	resp.Body.Close()
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

func TestStatsAndInspectEndpoints(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()

	// GET /api/v1/stats
	resp, err := client.Get(server.URL + "/api/v1/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /stats status = %d", resp.StatusCode)
	}
	var stats vm.StatsResult
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatal(err)
	}

	// Create a VM to inspect
	createReq := vm.CreateRequest{Name: "test-inspect", VCPUs: 1, MemoryMB: 512}
	data, _ := json.Marshal(createReq)
	resp2, err := client.Post(server.URL+"/api/v1/vms", "application/json", strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	var created db.VM
	_ = json.NewDecoder(resp2.Body).Decode(&created)
	resp2.Body.Close()

	// GET /api/v1/network/inspect/<id>
	resp3, err := client.Get(server.URL + "/api/v1/network/inspect/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("GET /network/inspect status = %d", resp3.StatusCode)
	}
	var inspect vm.NetworkInspectResult
	if err := json.NewDecoder(resp3.Body).Decode(&inspect); err != nil {
		t.Fatal(err)
	}
	if inspect.VMID != created.ID {
		t.Fatalf("expected inspect VMID %s, got %s", created.ID, inspect.VMID)
	}
}

func TestImageAndTemplateEndpoints(t *testing.T) {
	root := t.TempDir()
	store, err := db.NewStore(filepath.Join(root, "db", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	imagesDir := filepath.Join(root, "images")
	templatesDir := filepath.Join(root, "templates")
	os.MkdirAll(imagesDir, 0755)
	os.MkdirAll(templatesDir, 0755)

	os.WriteFile(filepath.Join(imagesDir, "rootfs.ext4"), []byte("base-rootfs"), 0644)
	os.WriteFile(filepath.Join(imagesDir, "vmlinux"), []byte("base-kernel"), 0644)

	storMgr, err := storage.NewManager(storage.Config{
		BaseImagesDir: imagesDir,
		TemplatesDir:  templatesDir,
		VMsDir:        filepath.Join(root, "vms"),
		DefaultRootfs: "rootfs.ext4",
		DefaultKernel: "vmlinux",
	})
	if err != nil {
		t.Fatal(err)
	}
	if avail, err := storMgr.GetAvailableDiskSpaceMB(); err == nil && avail < 256 {
		t.Skipf("skipping test: insufficient free disk space on %s (%d MB < 256 MB)", root, avail)
	}
	policy := db.DefaultSchedulingPolicy()
	policy.HostReservedDiskMB = 0
	if err := store.SaveSchedulingPolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	netMgr, err := network.NewManager(network.Config{SubnetCIDR: "192.168.60.0/24"}, testRunner{})
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfig()
	cfg.Defaults.DiskSizeMB = 1024
	withTestHostCapacity(&cfg)
	vmMgr := vm.NewManager(vm.Config{Defaults: cfg.Defaults, Resources: cfg.Resources}, store, netMgr, storMgr)
	vmMgr.GetImageBuilder().SetBuildFunc(func(ctx context.Context, req vm.BuildImageRequest, outputPath string, sshPubKey string) error {
		return os.WriteFile(outputPath, []byte("pkg-installed-rootfs"), 0644)
	})

	srv := httptest.NewServer(NewServer(vmMgr, cfg))
	defer srv.Close()

	// 1. GET /api/v1/images
	resp, err := http.Get(srv.URL + "/api/v1/images")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/images failed: status %d, err %v", resp.StatusCode, err)
	}
	var imgs []map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&imgs)
	resp.Body.Close()
	if len(imgs) == 0 {
		t.Fatalf("expected at least base image in images list")
	}

	// 2. POST /api/v1/images (build image with packages)
	buildPayload := `{"name":"dev-stack","description":"Dev tools stack","packages":["nodejs","npm","curl"]}`
	resp, err = http.Post(srv.URL+"/api/v1/images", "application/json", strings.NewReader(buildPayload))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/v1/images failed: status %d, err %v", resp.StatusCode, err)
	}
	var createdImg db.CustomImage
	json.NewDecoder(resp.Body).Decode(&createdImg)
	resp.Body.Close()
	if createdImg.Name != "dev-stack" || len(createdImg.Packages) != 3 {
		t.Fatalf("unexpected created image: %+v", createdImg)
	}

	// 3. GET /api/v1/images/dev-stack
	resp, err = http.Get(srv.URL + "/api/v1/images/dev-stack")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/images/dev-stack failed: status %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 4. Create VM from that custom image
	createVMPayload := `{"name":"test-dev-vm","image":"dev-stack","tier":"1vcpu"}`
	resp, err = http.Post(srv.URL+"/api/v1/vms", "application/json", strings.NewReader(createVMPayload))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/v1/vms from custom image failed: status %d", resp.StatusCode)
	}
	var devVM db.VM
	json.NewDecoder(resp.Body).Decode(&devVM)
	resp.Body.Close()

	// 5. POST /api/v1/templates (create template from dev VM disk)
	tplPayload := `{"name":"web-template","description":"Web server template","vm_id":"` + devVM.ID + `"}`
	resp, err = http.Post(srv.URL+"/api/v1/templates", "application/json", strings.NewReader(tplPayload))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/v1/templates failed: status %d", resp.StatusCode)
	}
	var tpl db.Template
	json.NewDecoder(resp.Body).Decode(&tpl)
	resp.Body.Close()
	if tpl.Name != "web-template" || tpl.SourceVMID != devVM.ID {
		t.Fatalf("unexpected template created: %+v", tpl)
	}

	// 6. GET /api/v1/templates
	resp, err = http.Get(srv.URL + "/api/v1/templates")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/templates failed: status %d", resp.StatusCode)
	}
	var tpls []db.Template
	json.NewDecoder(resp.Body).Decode(&tpls)
	resp.Body.Close()
	if len(tpls) != 1 || tpls[0].Name != "web-template" {
		t.Fatalf("expected 1 template in list, got %+v", tpls)
	}

	// 7. GET /api/v1/templates/web-template
	resp, err = http.Get(srv.URL + "/api/v1/templates/web-template")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/templates/web-template failed: status %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 8. POST /api/v1/templates/web-template/launch
	launchPayload := `{"name":"launched-from-tpl","tier":"2vcpu"}`
	resp, err = http.Post(srv.URL+"/api/v1/templates/web-template/launch", "application/json", strings.NewReader(launchPayload))
	// In test, firecracker isn't present, so launch may return VM or queued
	if err != nil {
		t.Fatalf("POST /api/v1/templates/web-template/launch failed: %v", err)
	}
	resp.Body.Close()

	// 9. DELETE /api/v1/templates/web-template
	reqDel, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/templates/web-template", nil)
	respDel, err := http.DefaultClient.Do(reqDel)
	if err != nil || respDel.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /api/v1/templates/web-template failed: status %d", respDel.StatusCode)
	}
	respDel.Body.Close()

	// 10. DELETE /api/v1/images/dev-stack
	reqDelImg, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/images/dev-stack", nil)
	respDelImg, err := http.DefaultClient.Do(reqDelImg)
	if err != nil || respDelImg.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /api/v1/images/dev-stack failed: status %d", respDelImg.StatusCode)
	}
	respDelImg.Body.Close()
}

func TestVMOperationsByName(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()

	// 1. Create a VM named "frontend-srv"
	createReq := vm.CreateRequest{Name: "frontend-srv", VCPUs: 2, MemoryMB: 512}
	data, _ := json.Marshal(createReq)
	resp, err := client.Post(server.URL+"/api/v1/vms", "application/json", strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", resp.StatusCode)
	}

	var created db.VM
	_ = json.NewDecoder(resp.Body).Decode(&created)
	if created.ID != "frontend-srv" || created.Name != "frontend-srv" {
		t.Fatalf("expected ID and Name to be frontend-srv, got %+v", created)
	}
	if created.InternalID == "" || created.InternalID == created.ID {
		t.Fatalf("expected distinct internal ID, got %q", created.InternalID)
	}

	// 2. GET by name
	getResp, err := client.Get(server.URL + "/api/v1/vms/frontend-srv")
	if err != nil || getResp.StatusCode != http.StatusOK {
		t.Fatalf("failed to GET VM by name: status=%d, err=%v", getResp.StatusCode, err)
	}
	getResp.Body.Close()

	// 3. POST stop by name
	stopResp, err := client.Post(server.URL+"/api/v1/vms/frontend-srv/stop", "application/json", nil)
	if err != nil || stopResp.StatusCode != http.StatusOK {
		t.Fatalf("failed to POST stop by name: status=%d, err=%v", stopResp.StatusCode, err)
	}
	stopResp.Body.Close()

	// 4. DELETE by name
	delReq, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/vms/frontend-srv", nil)
	delResp, err := client.Do(delReq)
	if err != nil || delResp.StatusCode != http.StatusOK {
		t.Fatalf("failed to DELETE VM by name: status=%d, err=%v", delResp.StatusCode, err)
	}
	delResp.Body.Close()
}

func TestPublicIPEndpoints(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()

	// 1. Create VM with public_ip: "off"
	reqData := `{"name": "private-vm", "vcpus": 1, "memory_mb": 512, "public_ip": "off"}`
	resp, err := client.Post(server.URL+"/api/v1/vms", "application/json", strings.NewReader(reqData))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", resp.StatusCode)
	}
	var created db.VM
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.PublicIPEnabled {
		t.Fatalf("expected PublicIPEnabled to be false")
	}
	if created.PublicIP != "" {
		t.Fatalf("expected PublicIP to be empty, got %s", created.PublicIP)
	}

	// 2. Turn public IP ON via POST /api/v1/vms/{id}/public-ip
	toggleOn := `{"public_ip": "on"}`
	resp2, err := client.Post(server.URL+"/api/v1/vms/"+created.ID+"/public-ip", "application/json", strings.NewReader(toggleOn))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp2.StatusCode)
	}
	var updated db.VM
	if err := json.NewDecoder(resp2.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if !updated.PublicIPEnabled {
		t.Fatalf("expected PublicIPEnabled to be true after toggle")
	}
	if updated.PublicIP != updated.IPAddress {
		t.Fatalf("expected PublicIP %s, got %s", updated.IPAddress, updated.PublicIP)
	}

	// 3. Inspect network to verify PublicIP and PublicIPEnabled are returned
	resp3, err := client.Get(server.URL + "/api/v1/network/inspect/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	var inspect vm.NetworkInspectResult
	if err := json.NewDecoder(resp3.Body).Decode(&inspect); err != nil {
		t.Fatal(err)
	}
	if !inspect.PublicIPEnabled || inspect.PublicIP != updated.IPAddress {
		t.Fatalf("inspect mismatch: enabled=%v, public_ip=%s", inspect.PublicIPEnabled, inspect.PublicIP)
	}

	// 4. Turn public IP OFF via query param ?public_ip=off
	resp4, err := client.Post(server.URL+"/api/v1/vms/"+created.ID+"/public-ip?public_ip=off", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp4.Body.Close()
	var toggledOff db.VM
	if err := json.NewDecoder(resp4.Body).Decode(&toggledOff); err != nil {
		t.Fatal(err)
	}
	if toggledOff.PublicIPEnabled || toggledOff.PublicIP != "" {
		t.Fatalf("expected PublicIP to be disabled and empty, got enabled=%v ip=%s", toggledOff.PublicIPEnabled, toggledOff.PublicIP)
	}
}

func TestWebSocketEndpoint(t *testing.T) {
	server := newTestServer(t)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket.Dial failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	msgType, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("conn.Read failed: %v", err)
	}
	if msgType != websocket.MessageText {
		t.Fatalf("expected text message, got %v", msgType)
	}
	var snap map[string]interface{}
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal snapshot failed: %v", err)
	}
	if snap["type"] != "snapshot" {
		t.Fatalf("expected type snapshot, got %v", snap["type"])
	}
}

func TestSSHKeyEndpoints(t *testing.T) {
	server := newTestServer(t)

	validPubKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKVMQHURyGt9mXytbJpOeTM4z4jIIhwMQ+aeXq/vDwAj test@client"

	// 1. Initially empty
	resp, err := http.Get(server.URL + "/api/v1/keys")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var listResp map[string][]db.SSHKey
	_ = json.NewDecoder(resp.Body).Decode(&listResp)
	if len(listResp["keys"]) != 0 {
		t.Fatalf("expected 0 keys initially, got %d", len(listResp["keys"]))
	}

	// 2. Reject invalid key
	badReq, _ := json.Marshal(map[string]string{"name": "bad", "public_key": "not-a-valid-key"})
	respBad, err := http.Post(server.URL+"/api/v1/keys", "application/json", bytes.NewReader(badReq))
	if err != nil {
		t.Fatal(err)
	}
	defer respBad.Body.Close()
	if respBad.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad key, got %d", respBad.StatusCode)
	}

	// 3. Add valid key
	addReq, _ := json.Marshal(map[string]string{"name": "my-laptop", "public_key": validPubKey})
	respAdd, err := http.Post(server.URL+"/api/v1/keys", "application/json", bytes.NewReader(addReq))
	if err != nil {
		t.Fatal(err)
	}
	defer respAdd.Body.Close()
	if respAdd.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", respAdd.StatusCode)
	}
	var createdKey db.SSHKey
	_ = json.NewDecoder(respAdd.Body).Decode(&createdKey)
	if createdKey.ID == "" || createdKey.Name != "my-laptop" || !strings.HasPrefix(createdKey.Fingerprint, "SHA256:") {
		t.Fatalf("unexpected created key: %+v", createdKey)
	}

	// 4. Get key by ID and by Name
	respGet, err := http.Get(server.URL + "/api/v1/keys/" + createdKey.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer respGet.Body.Close()
	var gotKey db.SSHKey
	_ = json.NewDecoder(respGet.Body).Decode(&gotKey)
	if gotKey.ID != createdKey.ID {
		t.Fatalf("expected key ID %s, got %s", createdKey.ID, gotKey.ID)
	}

	respGetName, err := http.Get(server.URL + "/api/v1/keys/my-laptop")
	if err != nil {
		t.Fatal(err)
	}
	defer respGetName.Body.Close()
	if respGetName.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for lookup by name, got %d", respGetName.StatusCode)
	}

	// 4b. Toggle account-wide scope and apply it
	patchBody, _ := json.Marshal(map[string]interface{}{"all_sandboxes": true})
	patchReq, _ := http.NewRequest(http.MethodPatch, server.URL+"/api/v1/keys/"+createdKey.ID, bytes.NewReader(patchBody))
	respPatch, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		t.Fatal(err)
	}
	defer respPatch.Body.Close()
	if respPatch.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for patch, got %d", respPatch.StatusCode)
	}
	var patched db.SSHKey
	_ = json.NewDecoder(respPatch.Body).Decode(&patched)
	if !patched.AllSandboxes {
		t.Fatalf("expected all_sandboxes=true after patch, got %+v", patched)
	}

	respApply, err := http.Post(server.URL+"/api/v1/keys/"+createdKey.ID+"/apply", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer respApply.Body.Close()
	if respApply.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for apply, got %d", respApply.StatusCode)
	}
	var applyResp struct {
		Key  db.SSHKey `json:"key"`
		Sync *struct {
			Applied []string `json:"applied"`
			Failed  []struct {
				VMID string `json:"vm_id"`
			} `json:"failed"`
		} `json:"sync"`
	}
	_ = json.NewDecoder(respApply.Body).Decode(&applyResp)
	if !applyResp.Key.AllSandboxes {
		t.Fatalf("expected applied key to be account-wide, got %+v", applyResp.Key)
	}

	// 5. Reject a private key supplied inline as ssh_public_key
	badVMReq, _ := json.Marshal(vm.CreateRequest{
		Name:         "bad-key-vm",
		VCPUs:        1,
		MemoryMB:     512,
		SSHPublicKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----",
	})
	respBadVM, err := http.Post(server.URL+"/api/v1/vms", "application/json", bytes.NewReader(badVMReq))
	if err != nil {
		t.Fatal(err)
	}
	defer respBadVM.Body.Close()
	if respBadVM.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for private key as ssh_public_key, got %d", respBadVM.StatusCode)
	}

	// 5b. Create VM with embedded SSH key
	vmReq, _ := json.Marshal(vm.CreateRequest{
		Name:     "key-vm",
		VCPUs:    1,
		MemoryMB: 512,
		SSHKeyID: "my-laptop",
	})
	respVM, err := http.Post(server.URL+"/api/v1/vms", "application/json", bytes.NewReader(vmReq))
	if err != nil {
		t.Fatal(err)
	}
	defer respVM.Body.Close()
	var createdVM db.VM
	_ = json.NewDecoder(respVM.Body).Decode(&createdVM)
	if createdVM.SSHKeyName != "my-laptop" || createdVM.SSHPublicKey != validPubKey {
		t.Fatalf("expected VM to have embedded key, got name=%s pubkey=%s", createdVM.SSHKeyName, createdVM.SSHPublicKey)
	}

	// 6. Delete key
	delReq, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/keys/my-laptop", nil)
	respDel, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	defer respDel.Body.Close()
	if respDel.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for delete, got %d", respDel.StatusCode)
	}

	// Verify deleted
	respCheck, _ := http.Get(server.URL + "/api/v1/keys/my-laptop")
	if respCheck.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after deletion, got %d", respCheck.StatusCode)
	}
}

func TestSSHKeyDefaultSelection(t *testing.T) {
	server := newTestServer(t)

	key1Pub := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKVMQHURyGt9mXytbJpOeTM4z4jIIhwMQ+aeXq/vDwAj test@laptop"
	key2Pub := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKVMQHURyGt9mXytbJpOeTM4z4jIIhwMQ+aeXq/vDwAj test@workstation"

	createKey := func(name, pub string) db.SSHKey {
		req, _ := json.Marshal(map[string]string{"name": name, "public_key": pub})
		resp, err := http.Post(server.URL+"/api/v1/keys", "application/json", bytes.NewReader(req))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201 for key %s, got %d", name, resp.StatusCode)
		}
		var key db.SSHKey
		_ = json.NewDecoder(resp.Body).Decode(&key)
		return key
	}
	createVM := func(noDefaultKey bool) db.VM {
		body := map[string]interface{}{"name": fmt.Sprintf("def-vm-%d", time.Now().UnixNano()), "vcpus": 1, "memory_mb": 512}
		if noDefaultKey {
			body["ssh_no_default_key"] = true
		}
		req, _ := json.Marshal(body)
		resp, err := http.Post(server.URL+"/api/v1/vms", "application/json", bytes.NewReader(req))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201 for sandbox create, got %d", resp.StatusCode)
		}
		var created db.VM
		_ = json.NewDecoder(resp.Body).Decode(&created)
		return created
	}

	// The first key on the account becomes the default automatically.
	key1 := createKey("laptop", key1Pub)
	if !key1.IsDefault {
		t.Fatalf("expected first key to become the default, got %+v", key1)
	}
	key2 := createKey("workstation", key2Pub)
	if key2.IsDefault {
		t.Fatalf("expected later keys to not steal the default, got %+v", key2)
	}

	// A sandbox created without a key embeds the account default.
	vm1 := createVM(false)
	if vm1.SSHKeyName != key1.Name || vm1.SSHPublicKey != key1Pub {
		t.Fatalf("expected default key embedded, got name=%q pubkey=%q", vm1.SSHKeyName, vm1.SSHPublicKey)
	}

	// Opting out leaves the sandbox without an account key.
	vm2 := createVM(true)
	if vm2.SSHKeyName != "" || vm2.SSHPublicKey != "" {
		t.Fatalf("expected no key after opt-out, got name=%q pubkey=%q", vm2.SSHKeyName, vm2.SSHPublicKey)
	}

	// An explicit key always wins over the default.
	vmReq, _ := json.Marshal(vm.CreateRequest{Name: "explicit-key-vm", VCPUs: 1, MemoryMB: 512, SSHKeyID: key2.Name})
	respVM, err := http.Post(server.URL+"/api/v1/vms", "application/json", bytes.NewReader(vmReq))
	if err != nil {
		t.Fatal(err)
	}
	defer respVM.Body.Close()
	var vm3 db.VM
	_ = json.NewDecoder(respVM.Body).Decode(&vm3)
	if vm3.SSHKeyName != key2.Name || vm3.SSHPublicKey != key2Pub {
		t.Fatalf("expected explicit key embedded, got name=%q pubkey=%q", vm3.SSHKeyName, vm3.SSHPublicKey)
	}

	// Promoting key2 to default moves the flag off key1 and future creations
	// embed key2.
	patchBody, _ := json.Marshal(map[string]interface{}{"is_default": true})
	patchReq, _ := http.NewRequest(http.MethodPatch, server.URL+"/api/v1/keys/"+key2.ID, bytes.NewReader(patchBody))
	respPatch, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		t.Fatal(err)
	}
	defer respPatch.Body.Close()
	var patched db.SSHKey
	_ = json.NewDecoder(respPatch.Body).Decode(&patched)
	if !patched.IsDefault {
		t.Fatalf("expected key2 to be default after patch, got %+v", patched)
	}
	vm4 := createVM(false)
	if vm4.SSHKeyName != key2.Name || vm4.SSHPublicKey != key2Pub {
		t.Fatalf("expected new default key embedded, got name=%q pubkey=%q", vm4.SSHKeyName, vm4.SSHPublicKey)
	}
	respStale, err := http.Get(server.URL + "/api/v1/keys/" + key1.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer respStale.Body.Close()
	var stale db.SSHKey
	_ = json.NewDecoder(respStale.Body).Decode(&stale)
	if stale.IsDefault {
		t.Fatalf("expected old default flag to be cleared, got %+v", stale)
	}

	// Clearing the default returns creation to keyless behavior.
	clearBody, _ := json.Marshal(map[string]interface{}{"is_default": false})
	clearReq, _ := http.NewRequest(http.MethodPatch, server.URL+"/api/v1/keys/"+key2.ID, bytes.NewReader(clearBody))
	respClear, err := http.DefaultClient.Do(clearReq)
	if err != nil {
		t.Fatal(err)
	}
	defer respClear.Body.Close()
	var cleared db.SSHKey
	_ = json.NewDecoder(respClear.Body).Decode(&cleared)
	if cleared.IsDefault {
		t.Fatalf("expected default flag cleared, got %+v", cleared)
	}
	vm5 := createVM(false)
	if vm5.SSHKeyName != "" || vm5.SSHPublicKey != "" {
		t.Fatalf("expected no key with no default set, got name=%q pubkey=%q", vm5.SSHKeyName, vm5.SSHPublicKey)
	}
}

func TestSSHEndpoint(t *testing.T) {
	server := newTestServer(t)

	// 1. WebSocket on /api/v1/vms/non-existent/ssh
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/vms/non-existent/ssh"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		defer conn.Close(websocket.StatusNormalClosure, "")
		_, data, readErr := conn.Read(ctx)
		if readErr == nil && !strings.Contains(string(data), "VM not found") {
			t.Fatalf("expected VM not found notice, got %s", string(data))
		}
	}

	// 2. WebSocket on direct endpoint /api/v1/ssh/non-existent
	wsURL2 := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ssh/non-existent"
	conn2, _, err2 := websocket.Dial(ctx, wsURL2, nil)
	if err2 == nil {
		defer conn2.Close(websocket.StatusNormalClosure, "")
		_, data, readErr := conn2.Read(ctx)
		if readErr == nil && !strings.Contains(string(data), "VM not found") {
			t.Fatalf("expected VM not found notice, got %s", string(data))
		}
	}

	// 3. HTTP GET to /api/v1/vms/non-existent/ssh without upgrade should return 404
	resp, err := http.Get(server.URL + "/api/v1/vms/non-existent/ssh")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}

	// 4. HTTP GET to /api/v1/ssh/non-existent without upgrade should return 404
	resp2, err := http.Get(server.URL + "/api/v1/ssh/non-existent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp2.StatusCode)
	}

	// 5. Create a VM (status will be created / not running)
	createReq := vm.CreateRequest{Name: "ssh-test-vm", VCPUs: 1, MemoryMB: 512}
	data, _ := json.Marshal(createReq)
	respCreate, err := http.Post(server.URL+"/api/v1/vms", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer respCreate.Body.Close()
	var created db.VM
	_ = json.NewDecoder(respCreate.Body).Decode(&created)

	// Connect to created VM which is not running
	wsURLVM := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/vms/" + created.ID + "/ssh"
	connVM, _, errVM := websocket.Dial(ctx, wsURLVM, nil)
	if errVM == nil {
		defer connVM.Close(websocket.StatusNormalClosure, "")
		_, msgData, readErr := connVM.Read(ctx)
		if readErr == nil && !strings.Contains(string(msgData), "not running") && !strings.Contains(string(msgData), "currently") {
			t.Fatalf("expected not running notice, got %s", string(msgData))
		}
	}
}

func TestTerminalEndpoint(t *testing.T) {
	server := newTestServer(t)

	// 1. Terminal on non-existent VM should return notice or 404
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/vms/non-existent/terminal"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		defer conn.Close(websocket.StatusNormalClosure, "")
		_, data, readErr := conn.Read(ctx)
		if readErr == nil && !strings.Contains(string(data), "VM not found") {
			t.Fatalf("expected VM not found notice, got %s", string(data))
		}
	}
}

func TestWorkerAndJobEndpoints(t *testing.T) {
	server := newTestServer(t)

	// 1. Create a worker token
	tokenBody := bytes.NewBufferString(`{"name":"test-worker","pool_id":"default"}`)
	resp, err := http.Post(server.URL+"/api/v1/worker-tokens", "application/json", tokenBody)
	if err != nil {
		t.Fatalf("failed to create worker token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created for worker token, got %d", resp.StatusCode)
	}
	var tokenResp api.WorkerTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		t.Fatalf("failed to decode worker token response: %v", err)
	}
	if tokenResp.Token == "" {
		t.Fatalf("expected non-empty token")
	}

	// 2. List worker pools
	respPools, err := http.Get(server.URL + "/api/v1/pools")
	if err != nil {
		t.Fatalf("failed to get pools: %v", err)
	}
	defer respPools.Body.Close()
	if respPools.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for pools, got %d", respPools.StatusCode)
	}

	// 3. Create a pool
	poolBody := bytes.NewBufferString(`{"name":"gpu-pool"}`)
	respCreatePool, err := http.Post(server.URL+"/api/v1/pools", "application/json", poolBody)
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer respCreatePool.Body.Close()
	if respCreatePool.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created for pool, got %d", respCreatePool.StatusCode)
	}

	// 4. Submit a Job
	jobReq := `{"type":"command","pool_id":"default","priority":10,"spec":{"image_id":"ubuntu-24","architecture":"amd64","vcpus":2,"memory_mb":1024,"command":["echo","hello"]}}`
	respJob, err := http.Post(server.URL+"/api/v1/jobs", "application/json", bytes.NewBufferString(jobReq))
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}
	defer respJob.Body.Close()
	if respJob.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created for job, got %d", respJob.StatusCode)
	}
	var jobCreated db.Job
	if err := json.NewDecoder(respJob.Body).Decode(&jobCreated); err != nil {
		t.Fatalf("failed to decode created job: %v", err)
	}
	if jobCreated.ID == "" || jobCreated.State != "queued" {
		t.Fatalf("expected queued job with valid ID, got %+v", jobCreated)
	}

	// 5. Query the created job
	respGetJob, err := http.Get(server.URL + "/api/v1/jobs/" + jobCreated.ID)
	if err != nil {
		t.Fatalf("failed to get job: %v", err)
	}
	defer respGetJob.Body.Close()
	if respGetJob.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for job get, got %d", respGetJob.StatusCode)
	}

	// 6. Cancel the job
	respCancel, err := http.Post(server.URL+"/api/v1/jobs/"+jobCreated.ID+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatalf("failed to cancel job: %v", err)
	}
	defer respCancel.Body.Close()
	if respCancel.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for cancel, got %d", respCancel.StatusCode)
	}
}

func TestVersionEndpoint(t *testing.T) {
	server := newTestServer(t)
	resp, err := http.Get(server.URL + "/api/v1/version")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var got map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["version"] != version.Version {
		t.Fatalf("version = %v, want %s", got["version"], version.Version)
	}
	if got["name"] != "Compute Daemon" {
		t.Fatalf("name = %v, want Compute Daemon", got["name"])
	}
	if got["service"] != "computed" {
		t.Fatalf("service = %v, want computed", got["service"])
	}
}

func TestExecStreamMissingVM(t *testing.T) {
	server := newTestServer(t)

	body := bytes.NewBufferString(`{"command":"echo hi"}`)
	resp, err := http.Post(server.URL+"/api/v1/vms/does-not-exist/exec-stream", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestVersionEndpointAdvertisesPublicURL(t *testing.T) {
	s := &Server{cfg: config.GlobalConfig{Server: config.ServerConfig{PublicURL: "compute-lab.tailb1be99.ts.net/"}}}
	rec := httptest.NewRecorder()
	s.handleVersion(rec, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got struct {
		Version   string `json:"version"`
		PublicURL string `json:"public_url"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.PublicURL != "https://compute-lab.tailb1be99.ts.net" {
		t.Fatalf("public_url = %q", got.PublicURL)
	}
	if got.Version != version.Version {
		t.Fatalf("version = %q", got.Version)
	}
}
