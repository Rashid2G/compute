package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/storage"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
	"github.com/Rashid2G/on-demand-vms/pkg/api"
	"github.com/Rashid2G/on-demand-vms/pkg/version"
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
	cfg := config.DefaultConfig()
	cfg.ConfigPath = "config/daemon.json"
	// Unit tests should not boot job sandboxes in the background.
	cfg.Jobs.LocalExecution = false
	server := httptest.NewServer(NewServer(vm.NewManager(vm.Config{}, store, netMgr, storMgr), cfg))
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

func TestDiskSpaceIssueOnCreate(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()

	// Try creating with a nonexistent image
	createReq := vm.CreateRequest{Name: "bad-img-vm", Image: "nonexistent-distro"}
	data, _ := json.Marshal(createReq)
	resp, err := client.Post(server.URL+"/api/v1/vms", "application/json", strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for disk space issue, got %d", resp.StatusCode)
	}

	var errResp map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&errResp)
	if !strings.Contains(errResp["error"], "disk space issue") {
		t.Fatalf("expected error mentioning 'disk space issue', got: %v", errResp)
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
	netMgr, err := network.NewManager(network.Config{SubnetCIDR: "192.168.60.0/24"}, testRunner{})
	if err != nil {
		t.Fatal(err)
	}

	vmMgr := vm.NewManager(vm.Config{}, store, netMgr, storMgr)
	vmMgr.GetImageBuilder().SetBuildFunc(func(ctx context.Context, req vm.BuildImageRequest, outputPath string, sshPubKey string) error {
		return os.WriteFile(outputPath, []byte("pkg-installed-rootfs"), 0644)
	})

	srv := httptest.NewServer(NewServer(vmMgr, config.DefaultConfig()))
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

	// 5. Create VM with embedded SSH key
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
	jobReq := `{"type":"command","pool_id":"default","priority":10,"spec":{"image_id":"alpine","architecture":"amd64","vcpus":2,"memory_mb":1024,"command":["echo","hello"]}}`
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
	if got["service"] != "microforged" {
		t.Fatalf("service = %v, want microforged", got["service"])
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
