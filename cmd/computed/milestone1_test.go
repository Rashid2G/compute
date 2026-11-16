package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/config"
	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/network"
	"github.com/Rashid2G/compute/internal/storage"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/Rashid2G/compute/pkg/api"
	"github.com/coder/websocket"
)

func newMilestone1Server(t *testing.T, limits config.ResourceLimits) (*httptest.Server, *vm.Manager, *db.Store) {
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
		TemplatesDir:  filepath.Join(root, "templates"),
		DefaultRootfs: "rootfs.ext4",
	})
	if err != nil {
		t.Fatal(err)
	}
	netMgr, err := network.NewManager(network.Config{SubnetCIDR: "192.168.55.0/24"}, testRunner{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.ConfigPath = filepath.Join(root, "daemon.json")
	cfg.Jobs.LocalExecution = false
	cfg.Resources = limits

	vmMgr := vm.NewManager(vm.Config{
		CgroupParent: filepath.Join(root, "cgroup"),
		Resources:    limits,
	}, store, netMgr, storMgr)
	srv := NewServer(vmMgr, cfg)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, vmMgr, store
}

func doJSONRequest(t *testing.T, method, url string, payload interface{}) (*http.Response, []byte) {
	t.Helper()
	var bodyReader *bytes.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func TestMilestone1_WorkerEnrollmentPoolAndHeartbeat(t *testing.T) {
	ts, _, store := newMilestone1Server(t, config.ResourceLimits{
		MaxTotalVCPUs:    4,
		MaxTotalMemoryMB: 2048,
		MaxTotalDiskMB:   4096,
	})
	ctx := context.Background()

	// Create a worker token pinned to pool "gpu-pool"
	tokenResp, tokenBody := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/worker-tokens", api.WorkerTokenCreateRequest{
		PoolID: "gpu-pool",
	})
	if tokenResp.StatusCode != http.StatusCreated {
		t.Fatalf("create worker token failed: %d %s", tokenResp.StatusCode, string(tokenBody))
	}
	var tok api.WorkerTokenResponse
	if err := json.Unmarshal(tokenBody, &tok); err != nil {
		t.Fatal(err)
	}

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/worker/connect"
	wsCtx, wsCancel := context.WithTimeout(ctx, 10*time.Second)
	defer wsCancel()

	conn, _, err := websocket.Dial(wsCtx, wsURL, nil)
	if err != nil {
		t.Fatalf("worker websocket dial failed: %v", err)
	}

	// Send WorkerHello with hello.PoolID="cpu-pool" (token.PoolID="gpu-pool" must win)
	// and max_vcpus / max_memory_mb / max_disk_mb overrides.
	hello := api.WorkerHello{
		WorkerMessage: api.WorkerMessage{
			Version:  1,
			Type:     "worker.hello",
			ID:       "wrk-gpu-1",
			WorkerID: "wrk-gpu-1",
		},
		Name:         "gpu-node-01",
		Token:        tok.Token,
		PoolID:       "cpu-pool",
		Architecture: "amd64",
		AgentVersion: "1.0.0-test",
		Capabilities: map[string]interface{}{
			"vcpus":               32,
			"memory_mb":           65536,
			"memory_available_mb": 48000,
			"disk_mb":             200000,
			"disk_available_mb":   150000,
			"max_vcpus":           16,
			"max_memory_mb":       32768,
			"max_disk_mb":         100000,
			"underlay_ip":         "10.99.0.10",
		},
	}
	helloBytes, _ := json.Marshal(hello)
	if err := conn.Write(wsCtx, websocket.MessageText, helloBytes); err != nil {
		t.Fatal(err)
	}

	_, ackBytes, err := conn.Read(wsCtx)
	if err != nil {
		t.Fatalf("read WorkerHelloAck failed: %v", err)
	}
	var ack api.WorkerHelloAck
	if err := json.Unmarshal(ackBytes, &ack); err != nil {
		t.Fatal(err)
	}
	if !ack.Accepted || ack.WorkerID != "wrk-gpu-1" || ack.PoolID != "gpu-pool" {
		t.Fatalf("unexpected WorkerHelloAck: %+v", ack)
	}

	// Verify pool "gpu-pool" was auto-created and node fields persisted
	pool, err := store.GetWorkerPool(ctx, "gpu-pool")
	if err != nil || pool == nil {
		t.Fatalf("expected gpu-pool to be auto-created, got pool=%+v err=%v", pool, err)
	}
	node, err := store.GetWorkerNode(ctx, "wrk-gpu-1")
	if err != nil || node == nil {
		t.Fatalf("expected worker node wrk-gpu-1, got node=%+v err=%v", node, err)
	}
	if node.PoolID != "gpu-pool" || node.Name != "gpu-node-01" {
		t.Fatalf("unexpected node pool/name: %+v", node)
	}
	if node.PhysicalVCPUs != 32 || node.PhysicalMemMB != 65536 || node.PhysicalDiskMB != 200000 {
		t.Fatalf("unexpected physical metrics: vcpus=%d mem=%d disk=%d", node.PhysicalVCPUs, node.PhysicalMemMB, node.PhysicalDiskMB)
	}
	if node.AllocatedVCPUs != 16 || node.AllocatedMemMB != 32768 || node.AllocatedDiskMB != 100000 {
		t.Fatalf("expected max_* overrides on Allocated*, got vcpus=%d mem=%d disk=%d", node.AllocatedVCPUs, node.AllocatedMemMB, node.AllocatedDiskMB)
	}

	// Cordon worker, then send a worker.heartbeat and ensure Name, PoolID, Allocated*, and "cordoned" status are preserved
	cordonResp, cordonBody := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/workers/wrk-gpu-1/cordon", nil)
	if cordonResp.StatusCode != http.StatusOK {
		t.Fatalf("cordon failed: %d %s", cordonResp.StatusCode, string(cordonBody))
	}

	hb := api.WorkerHeartbeat{
		WorkerMessage: api.WorkerMessage{
			Version:  1,
			Type:     "worker.heartbeat",
			ID:       "msg-hb-1",
			WorkerID: "wrk-gpu-1",
		},
		Status: "ready",
		Metrics: map[string]interface{}{
			"running_jobs": 0,
		},
		Capabilities: map[string]interface{}{
			"vcpus":               32,
			"memory_mb":           65536,
			"memory_available_mb": 42000,
			"disk_mb":             200000,
			"disk_available_mb":   140000,
		},
	}
	hbBytes, _ := json.Marshal(hb)
	if err := conn.Write(wsCtx, websocket.MessageText, hbBytes); err != nil {
		t.Fatal(err)
	}

	// Poll briefly for async heartbeat handling
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		updated, _ := store.GetWorkerNode(ctx, "wrk-gpu-1")
		if updated != nil && strings.Contains(updated.CapabilitiesJSON, "42000") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	afterHB, err := store.GetWorkerNode(ctx, "wrk-gpu-1")
	if err != nil || afterHB == nil {
		t.Fatal(err)
	}
	if afterHB.Status != "cordoned" {
		t.Fatalf("heartbeat overwrote cordoned status: got %q", afterHB.Status)
	}
	if afterHB.Name != "gpu-node-01" || afterHB.PoolID != "gpu-pool" {
		t.Fatalf("heartbeat overwrote Name/PoolID: name=%q pool=%q", afterHB.Name, afterHB.PoolID)
	}
	if afterHB.AllocatedVCPUs != 16 || afterHB.AllocatedMemMB != 32768 || afterHB.AllocatedDiskMB != 100000 {
		t.Fatalf("heartbeat overwrote Allocated*: vcpus=%d mem=%d disk=%d", afterHB.AllocatedVCPUs, afterHB.AllocatedMemMB, afterHB.AllocatedDiskMB)
	}
	if !strings.Contains(afterHB.CapabilitiesJSON, "42000") || !strings.Contains(afterHB.CapabilitiesJSON, "140000") {
		t.Fatalf("heartbeat failed to update CapabilitiesJSON: %s", afterHB.CapabilitiesJSON)
	}

	_ = conn.Close(websocket.StatusNormalClosure, "done")

	// Verify second worker without token uses hello.PoolID -> auto-creates "cpu-pool"
	conn2, _, err := websocket.Dial(wsCtx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	hello2 := api.WorkerHello{
		WorkerMessage: api.WorkerMessage{Version: 1, Type: "worker.hello", ID: "wrk-cpu-1", WorkerID: "wrk-cpu-1"},
		Name:          "cpu-node-01",
		PoolID:        "cpu-pool",
		Capabilities:  map[string]interface{}{"vcpus": 8, "memory_mb": 16384, "disk_mb": 100000},
	}
	b2, _ := json.Marshal(hello2)
	if err := conn2.Write(wsCtx, websocket.MessageText, b2); err != nil {
		t.Fatal(err)
	}
	_, ack2Bytes, err := conn2.Read(wsCtx)
	if err != nil {
		t.Fatal(err)
	}
	var ack2 api.WorkerHelloAck
	_ = json.Unmarshal(ack2Bytes, &ack2)
	if ack2.PoolID != "cpu-pool" {
		t.Fatalf("expected hello.PoolID fallback cpu-pool, got %q", ack2.PoolID)
	}
	_ = conn2.Close(websocket.StatusNormalClosure, "done")

	// Verify unbound worker token (empty PoolID) does NOT shadow hello.PoolID ("P1")
	unboundResp, unboundBody := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/worker-tokens", api.WorkerTokenCreateRequest{
		Name: "unbound-node",
	})
	if unboundResp.StatusCode != http.StatusCreated {
		t.Fatalf("create unbound worker token failed: %d %s", unboundResp.StatusCode, string(unboundBody))
	}
	var unboundTok api.WorkerTokenResponse
	if err := json.Unmarshal(unboundBody, &unboundTok); err != nil {
		t.Fatal(err)
	}
	if unboundTok.PoolID != "" {
		t.Fatalf("expected unbound token PoolID to remain empty, got %q", unboundTok.PoolID)
	}
	conn3, _, err := websocket.Dial(wsCtx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	hello3 := api.WorkerHello{
		WorkerMessage: api.WorkerMessage{Version: 1, Type: "worker.hello", ID: "wrk-p1", WorkerID: "wrk-p1"},
		Name:          "unbound-node",
		Token:         unboundTok.Token,
		PoolID:        "P1",
		Capabilities:  map[string]interface{}{"vcpus": 4, "memory_mb": 8192, "disk_mb": 50000},
	}
	b3, _ := json.Marshal(hello3)
	if err := conn3.Write(wsCtx, websocket.MessageText, b3); err != nil {
		t.Fatal(err)
	}
	_, ack3Bytes, err := conn3.Read(wsCtx)
	if err != nil {
		t.Fatal(err)
	}
	var ack3 api.WorkerHelloAck
	_ = json.Unmarshal(ack3Bytes, &ack3)
	if ack3.PoolID != "P1" {
		t.Fatalf("expected unbound token to allow hello.PoolID=P1, got %q", ack3.PoolID)
	}
	nodeP1, err := store.GetWorkerNode(ctx, "wrk-p1")
	if err != nil || nodeP1 == nil || nodeP1.PoolID != "P1" {
		t.Fatalf("expected persisted node wrk-p1 in pool P1, got %+v err=%v", nodeP1, err)
	}
	_ = conn3.Close(websocket.StatusNormalClosure, "done")

	// Verify GET /api/v1/workers includes nod_host_local and enriched wrk-gpu-1 Available* fields
	workersResp, workersBody := doJSONRequest(t, http.MethodGet, ts.URL+"/api/v1/workers", nil)
	if workersResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/workers failed: %d", workersResp.StatusCode)
	}
	var nodes []*db.WorkerNode
	if err := json.Unmarshal(workersBody, &nodes); err != nil {
		t.Fatal(err)
	}
	var local, gpuNode *db.WorkerNode
	for _, n := range nodes {
		if n.ID == "nod_host_local" {
			local = n
		}
		if n.ID == "wrk-gpu-1" {
			gpuNode = n
		}
	}
	if local == nil {
		t.Fatalf("nod_host_local missing from /api/v1/workers")
	}
	if local.AllocatedVCPUs != 4 || local.AllocatedMemMB != 2048 || local.AllocatedDiskMB != 4096 {
		t.Fatalf("unexpected nod_host_local Allocated*: %+v", local)
	}
	if local.PhysicalVCPUs <= 0 || local.PhysicalMemMB <= 0 || local.PhysicalDiskMB <= 0 {
		t.Fatalf("expected positive nod_host_local Physical* metrics: %+v", local)
	}
	if gpuNode == nil {
		t.Fatalf("wrk-gpu-1 missing from /api/v1/workers")
	}
	if gpuNode.AvailableMemMB != 42000 || gpuNode.AvailableDiskMB != 140000 {
		t.Fatalf("expected enriched AvailableMemMB=42000 AvailableDiskMB=140000, got mem=%d disk=%d", gpuNode.AvailableMemMB, gpuNode.AvailableDiskMB)
	}
}

func TestMilestone1_HTTP429ResourceLimitResponses(t *testing.T) {
	assert429 := func(t *testing.T, label string, resp *http.Response, body []byte, wantResource string) {
		t.Helper()
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("%s: expected 429 Too Many Requests, got %d: %s", label, resp.StatusCode, string(body))
		}
		var rle api.ResourceLimitError
		if err := json.Unmarshal(body, &rle); err != nil {
			t.Fatalf("%s: failed decoding ResourceLimitError JSON: %v (%s)", label, err, string(body))
		}
		if rle.Code != api.ErrCodeResourceLimitExceeded || rle.Resource != wantResource || rle.LimitType != "hard" || rle.Message == "" {
			t.Fatalf("%s: unexpected ResourceLimitError payload: %+v", label, rle)
		}
	}

	t.Run("disk_limits", func(t *testing.T) {
		ts, _, _ := newMilestone1Server(t, config.ResourceLimits{
			MaxTotalVCPUs:      4,
			MaxTotalMemoryMB:   2048,
			MaxTotalDiskMB:     2048,
			MinFreeDiskSpaceMB: 0,
		})

		// 1. Create VM1 (1024 MB disk) -> 201
		resp1, body1 := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/vms", map[string]interface{}{
			"name":      "vm-base",
			"vcpus":     1,
			"memory_mb": 256,
			"disk_mb":   1024,
		})
		if resp1.StatusCode != http.StatusCreated {
			t.Fatalf("create vm-base failed: %d %s", resp1.StatusCode, string(body1))
		}
		var vm1 db.VM
		_ = json.Unmarshal(body1, &vm1)

		// 2. Create VM2 exceeding MaxTotalDiskMB (1024 + 1500 > 2048) -> 429
		respOver, bodyOver := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/vms", map[string]interface{}{
			"name":      "vm-disk-over",
			"vcpus":     1,
			"memory_mb": 256,
			"disk_mb":   1500,
		})
		assert429(t, "CreateVM disk limit", respOver, bodyOver, "disk")

		// 3. Clone VM1 overriding disk_mb to 1500 -> 429
		respClone, bodyClone := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/vms/"+vm1.ID+"/clone", map[string]interface{}{
			"name":    "vm-clone-over",
			"disk_mb": 1500,
		})
		assert429(t, "CloneVM disk limit", respClone, bodyClone, "disk")

		// 4. Create data volume of 2 GB (2048 MB) -> 1024 + 2048 > 2048 -> 429
		respVol, bodyVol := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/volumes", map[string]interface{}{
			"name":    "vol-over",
			"size_gb": 2,
		})
		assert429(t, "CreateDataVolume disk limit", respVol, bodyVol, "disk")

		// 5. Create 1 GB volume (1024 + 1024 == 2048) -> 201, then expand by 1 GB -> 429
		respVolOK, bodyVolOK := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/volumes", map[string]interface{}{
			"name":    "vol-ok",
			"size_gb": 1,
		})
		if respVolOK.StatusCode != http.StatusCreated {
			t.Fatalf("CreateDataVolume within limit failed: %d %s", respVolOK.StatusCode, string(bodyVolOK))
		}
		var vol db.Volume
		_ = json.Unmarshal(bodyVolOK, &vol)

		respExpand, bodyExpand := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/volumes/"+vol.ID+"/expand", map[string]interface{}{
			"add_gb": 1,
		})
		assert429(t, "ExpandDataVolume disk limit", respExpand, bodyExpand, "disk")

		// 6. Snapshot vol-ok (needs 1024 MB logical; 2048 + 1024 > 2048) -> 429
		respSnap, bodySnap := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/volumes/"+vol.ID+"/snapshot", nil)
		assert429(t, "SnapshotDataVolume disk limit", respSnap, bodySnap, "disk")

		// 7. BuildImage exceeding MaxTotalDiskMB -> 429
		respImg, bodyImg := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/images", map[string]interface{}{
			"name":    "img-over",
			"size_mb": 2048,
		})
		assert429(t, "BuildImage disk limit", respImg, bodyImg, "disk")
	})

	t.Run("cpu_template_and_savestate_limits", func(t *testing.T) {
		ts, vmMgr, store := newMilestone1Server(t, config.ResourceLimits{
			MaxTotalVCPUs:      1,
			MaxTotalMemoryMB:   512,
			MaxTotalDiskMB:     0,
			MinFreeDiskSpaceMB: 0,
		})
		ctx := context.Background()

		var mu sync.Mutex
		alivePIDs := map[int]bool{}
		nextPID := 60000
		vmMgr.SetProcessChecker(func(pid int) bool {
			mu.Lock()
			defer mu.Unlock()
			return alivePIDs[pid]
		})
		vmMgr.SetLauncher(func(ctx context.Context, v *db.VM) error {
			mu.Lock()
			nextPID++
			pid := nextPID
			alivePIDs[pid] = true
			mu.Unlock()
			v.PID = pid
			v.Status = db.StatusRunning
			v.DesiredRunning = true
			return nil
		})
		vmMgr.SetSnapshotter(func(ctx context.Context, v *db.VM, snapPath, memPath string) error {
			return nil
		})

		// 1. Create VM1 (1 vCPU, 256 MB RAM) -> 201
		resp1, body1 := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/vms", map[string]interface{}{
			"name":      "vm-base",
			"vcpus":     1,
			"memory_mb": 256,
			"disk_mb":   1024,
		})
		if resp1.StatusCode != http.StatusCreated {
			t.Fatalf("create vm-base failed: %d %s", resp1.StatusCode, string(body1))
		}
		var vm1 db.VM
		_ = json.Unmarshal(body1, &vm1)

		// 2. Create VM2 with 4 vCPUs (MaxTotalVCPUs=1, hard ratio=2.0 -> hard limit 2) and run -> 429 CPU limit
		respCPUCreate, bodyCPUCreate := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/vms", map[string]interface{}{
			"name":      "vm-cpu-over",
			"vcpus":     4,
			"memory_mb": 256,
		})
		if respCPUCreate.StatusCode != http.StatusCreated {
			t.Fatalf("create vm-cpu-over failed: %d %s", respCPUCreate.StatusCode, string(bodyCPUCreate))
		}
		var vmCPU db.VM
		_ = json.Unmarshal(bodyCPUCreate, &vmCPU)

		respRunCPU, bodyRunCPU := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/vms/"+vmCPU.ID+"/run", nil)
		assert429(t, "RunVM CPU hard limit", respRunCPU, bodyRunCPU, "cpu")

		// 3. Create Template from vm1, then launch with 4 vCPUs -> 429 CPU limit
		respTpl, bodyTpl := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/templates", map[string]interface{}{
			"name":  "tpl-m1",
			"vm_id": vm1.ID,
		})
		if respTpl.StatusCode != http.StatusCreated {
			t.Fatalf("CreateTemplate failed: %d %s", respTpl.StatusCode, string(bodyTpl))
		}
		var tpl db.Template
		_ = json.Unmarshal(bodyTpl, &tpl)

		respLaunch, bodyLaunch := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/templates/"+tpl.ID+"/launch", map[string]interface{}{
			"name":  "vm-from-tpl",
			"vcpus": 4,
		})
		assert429(t, "LaunchTemplate CPU hard limit", respLaunch, bodyLaunch, "cpu")

		// 4. SaveState hitting MinFreeDiskSpaceMB -> 429
		loadedVM1, _ := store.GetVM(ctx, vm1.ID)
		loadedVM1.Status = db.StatusRunning
		loadedVM1.PID = 61001
		mu.Lock()
		alivePIDs[61001] = true
		mu.Unlock()
		_ = store.SaveVM(ctx, loadedVM1)

		vmMgr.SetResourceLimits(config.ResourceLimits{
			MaxTotalVCPUs:      1,
			MaxTotalMemoryMB:   512,
			MaxTotalDiskMB:     0,
			MinFreeDiskSpaceMB: 999999999,
		})
		respSave, bodySave := doJSONRequest(t, http.MethodPost, ts.URL+"/api/v1/vms/"+vm1.ID+"/save-state", nil)
		assert429(t, "SaveState disk reserve limit", respSave, bodySave, "disk")
	})
}
