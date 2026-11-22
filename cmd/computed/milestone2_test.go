package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
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
	"github.com/Rashid2G/compute/internal/worker"
	"github.com/Rashid2G/compute/pkg/api"
)

type m2Harness struct {
	srv    *Server
	ts     *httptest.Server
	vmMgr  *vm.Manager
	store  *db.Store
	cancel context.CancelFunc
}

type m2Worker struct {
	id     string
	vmMgr  *vm.Manager
	store  *db.Store
	client *worker.Client
	cancel context.CancelFunc
}

func newM2VMManager(t *testing.T, root string, limits config.ResourceLimits, subnet string) (*vm.Manager, *db.Store) {
	t.Helper()
	store, err := db.NewStore(filepath.Join(root, "db", "compute.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	imagesDir := filepath.Join(root, "images")
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imagesDir, "rootfs.ext4"), []byte("test rootfs image payload"), 0o600); err != nil {
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
	netMgr, err := network.NewManager(network.Config{SubnetCIDR: subnet}, testRunner{})
	if err != nil {
		t.Fatal(err)
	}
	vmMgr := vm.NewManager(vm.Config{
		CgroupParent: filepath.Join(root, "cgroup"),
		Resources:    limits,
	}, store, netMgr, storMgr)

	var mu sync.Mutex
	alive := make(map[int]bool)
	nextPID := 50000
	vmMgr.SetProcessChecker(func(pid int) bool {
		mu.Lock()
		defer mu.Unlock()
		return alive[pid]
	})
	vmMgr.SetLauncher(func(ctx context.Context, v *db.VM) error {
		mu.Lock()
		nextPID++
		pid := nextPID
		alive[pid] = true
		mu.Unlock()
		v.PID = pid
		v.Status = db.StatusRunning
		v.DesiredRunning = true
		return nil
	})
	vmMgr.SetSnapshotter(func(ctx context.Context, v *db.VM, snapPath, memPath string) error {
		if err := os.MkdirAll(filepath.Dir(snapPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(snapPath, []byte("snapshot-state-data-"+v.ID), 0o600); err != nil {
			return err
		}
		return os.WriteFile(memPath, []byte("snapshot-mem-data-"+v.ID), 0o600)
	})
	return vmMgr, store
}

func newM2Harness(t *testing.T, limits config.ResourceLimits) *m2Harness {
	t.Helper()
	root := t.TempDir()
	limits.MinFreeDiskSpaceMB = 0
	vmMgr, store := newM2VMManager(t, root, limits, "192.168.60.0/24")
	if stor := vmMgr.GetStorageManager(); stor != nil {
		if avail, err := stor.GetAvailableDiskSpaceMB(); err == nil && avail < 256 {
			t.Skipf("skipping test: insufficient free disk space on %s (%d MB < 256 MB)", root, avail)
		}
	}
	cfg := config.DefaultConfig()
	cfg.ConfigPath = filepath.Join(root, "daemon.json")
	cfg.Jobs.LocalExecution = false
	cfg.Resources = limits

	policy := db.DefaultSchedulingPolicy()
	policy.HostReservedDiskMB = 0
	if err := store.SaveSchedulingPolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(vmMgr, cfg)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	return &m2Harness{
		srv:    srv,
		ts:     ts,
		vmMgr:  vmMgr,
		store:  store,
		cancel: cancel,
	}
}

func (h *m2Harness) connectWorker(t *testing.T, name, poolID string, limits config.ResourceLimits, subnet string) *m2Worker {
	t.Helper()
	tokResp, tokBody := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/worker-tokens", api.WorkerTokenCreateRequest{
		Name:   name,
		PoolID: poolID,
	})
	if tokResp.StatusCode != http.StatusCreated {
		t.Fatalf("create worker token failed: %d %s", tokResp.StatusCode, string(tokBody))
	}
	var tok api.WorkerTokenResponse
	if err := json.Unmarshal(tokBody, &tok); err != nil {
		t.Fatal(err)
	}

	wRoot := t.TempDir()
	wMgr, wStore := newM2VMManager(t, wRoot, limits, subnet)

	wCtx, wCancel := context.WithCancel(context.Background())
	t.Cleanup(wCancel)

	wc := &worker.Client{
		Config: worker.Config{
			ControlPlaneURL: h.ts.URL,
			Name:            name,
			Token:           tok.Token,
			PoolID:          poolID,
			MaxVCPUs:        limits.MaxTotalVCPUs,
			MaxMemoryMB:     limits.MaxTotalMemoryMB,
			MaxDiskMB:       int(limits.MaxTotalDiskMB),
		},
		VMManager: wMgr,
	}
	go func() {
		_ = wc.Run(wCtx)
	}()

	var workerID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		nodes, _ := h.store.ListWorkerNodes(context.Background())
		for _, n := range nodes {
			if n.Name == name && h.srv.workers != nil {
				if _, ok := h.srv.workers.Get(n.ID); ok {
					workerID = n.ID
					break
				}
			}
		}
		if workerID != "" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if workerID == "" {
		t.Fatalf("worker %s failed to connect and register", name)
	}

	return &m2Worker{
		id:     workerID,
		vmMgr:  wMgr,
		store:  wStore,
		client: wc,
		cancel: wCancel,
	}
}

func TestMilestone2_MultiWorkerPlacementAndRemoteVMLifecycle(t *testing.T) {
	h := newM2Harness(t, config.ResourceLimits{
		MaxTotalVCPUs:    2,
		MaxTotalMemoryMB: 1024,
		MaxTotalDiskMB:   20000,
	})

	// Cordon local host so automatic placement chooses between remote workers cleanly.
	cordonResp, cordonBody := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/workers/nod_host_local/cordon", nil)
	if cordonResp.StatusCode != http.StatusOK {
		t.Fatalf("cordon nod_host_local failed: %d %s", cordonResp.StatusCode, string(cordonBody))
	}

	wA := h.connectWorker(t, "worker-a", "pool-a", config.ResourceLimits{
		MaxTotalVCPUs:    4,
		MaxTotalMemoryMB: 4096,
		MaxTotalDiskMB:   6000,
	}, "192.168.61.0/24")
	wB := h.connectWorker(t, "worker-b", "pool-b", config.ResourceLimits{
		MaxTotalVCPUs:    4,
		MaxTotalMemoryMB: 4096,
		MaxTotalDiskMB:   6000,
	}, "192.168.62.0/24")

	// 1. Explicit worker_id placement
	resp1, body1 := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", map[string]interface{}{
		"name":      "vm-on-a",
		"worker_id": wA.id,
		"vcpus":     1,
		"memory_mb": 512,
		"disk_mb":   1024,
	})
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("create vm-on-a failed: %d %s", resp1.StatusCode, string(body1))
	}
	var vmA db.VM
	_ = json.Unmarshal(body1, &vmA)
	if vmA.WorkerID != wA.id || vmA.PoolID != "pool-a" {
		t.Fatalf("expected vm-on-a on %s/pool-a, got worker=%s pool=%s", wA.id, vmA.WorkerID, vmA.PoolID)
	}
	if localCopy, _ := wA.vmMgr.GetVM(context.Background(), vmA.ID); localCopy == nil {
		t.Fatalf("expected vm-on-a to exist in worker-a manager")
	}

	// 2. Explicit pool_id placement -> selects worker-b in pool-b
	resp2, body2 := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", map[string]interface{}{
		"name":      "vm-on-b",
		"pool_id":   "pool-b",
		"vcpus":     2,
		"memory_mb": 1024,
		"disk_mb":   2048,
	})
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("create vm-on-b failed: %d %s", resp2.StatusCode, string(body2))
	}
	var vmB db.VM
	_ = json.Unmarshal(body2, &vmB)
	if vmB.WorkerID != wB.id || vmB.PoolID != "pool-b" {
		t.Fatalf("expected vm-on-b on %s/pool-b, got worker=%s pool=%s", wB.id, vmB.WorkerID, vmB.PoolID)
	}

	// 3. Remote lifecycle: start -> save-state -> resume -> stop
	for _, tc := range []struct {
		path   string
		status db.VMStatus
	}{
		{"/run", db.StatusRunning},
		{"/save-state", db.StatusPaused},
		{"/resume", db.StatusRunning},
		{"/stop", db.StatusStopped},
	} {
		r, b := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/"+vmA.ID+tc.path, nil)
		if r.StatusCode != http.StatusOK {
			t.Fatalf("%s failed: %d %s", tc.path, r.StatusCode, string(b))
		}
		var updated db.VM
		_ = json.Unmarshal(b, &updated)
		if updated.Status != tc.status {
			t.Fatalf("after %s expected status %s, got %s", tc.path, tc.status, updated.Status)
		}
		if updated.WorkerID != wA.id {
			t.Fatalf("after %s WorkerID drifted to %q", tc.path, updated.WorkerID)
		}
	}

	// 4. Start vmB and verify /api/v1/workers shows used resources on wB
	rStart, bStart := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/"+vmB.ID+"/run", nil)
	if rStart.StatusCode != http.StatusOK {
		t.Fatalf("start vmB failed: %d %s", rStart.StatusCode, string(bStart))
	}
	wResp, wBody := doJSONRequest(t, http.MethodGet, h.ts.URL+"/api/v1/workers/"+wB.id, nil)
	if wResp.StatusCode != http.StatusOK {
		t.Fatalf("GET worker-b failed: %d %s", wResp.StatusCode, string(wBody))
	}
	var nodeB db.WorkerNode
	_ = json.Unmarshal(wBody, &nodeB)
	if nodeB.RunningVMs != 1 || nodeB.UsedVCPUs != 2 || nodeB.UsedMemMB != 1024 || nodeB.UsedDiskMB != 2048 {
		t.Fatalf("unexpected worker-b usage: %+v", nodeB)
	}

	// 5. Strategy switch: bin_pack vs least_loaded
	rStartA, _ := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/"+vmA.ID+"/run", nil)
	if rStartA.StatusCode != http.StatusOK {
		t.Fatalf("start vmA failed")
	}
	// Create another VM on wB so wB has higher utilization (3/4 vCPU, 2048/4096 mem, 3072/6000 disk)
	// while still leaving plenty of headroom for vm-bin-pack (1 vCPU, 512 mem, 1024 disk).
	rB2, bB2 := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", map[string]interface{}{
		"name":       "vm-on-b-2",
		"worker_id":  wB.id,
		"vcpus":      1,
		"memory_mb":  1024,
		"disk_mb":    1024,
		"auto_start": true,
	})
	if rB2.StatusCode != http.StatusCreated {
		t.Fatalf("create vm-on-b-2 failed: %d %s", rB2.StatusCode, string(bB2))
	}

	// Set policy to bin_pack -> should choose wB (higher utilization)
	polResp, polBody := doJSONRequest(t, http.MethodPut, h.ts.URL+"/api/v1/scheduler/policy", map[string]interface{}{
		"strategy": "bin_pack",
	})
	if polResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT policy bin_pack failed: %d %s", polResp.StatusCode, string(polBody))
	}
	rPack, bPack := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", map[string]interface{}{
		"name":      "vm-bin-pack",
		"vcpus":     1,
		"memory_mb": 512,
		"disk_mb":   1024,
	})
	if rPack.StatusCode != http.StatusCreated {
		t.Fatalf("create vm-bin-pack failed: %d %s", rPack.StatusCode, string(bPack))
	}
	var vmPack db.VM
	_ = json.Unmarshal(bPack, &vmPack)
	if vmPack.WorkerID != wB.id {
		t.Fatalf("expected bin_pack to place on fuller worker %s, got %s", wB.id, vmPack.WorkerID)
	}

	// Set policy to least_loaded -> should choose wA (more headroom)
	polResp2, polBody2 := doJSONRequest(t, http.MethodPut, h.ts.URL+"/api/v1/scheduler/policy", map[string]interface{}{
		"strategy": "least_loaded",
	})
	if polResp2.StatusCode != http.StatusOK {
		t.Fatalf("PUT policy least_loaded failed: %d %s", polResp2.StatusCode, string(polBody2))
	}
	rLeast, bLeast := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", map[string]interface{}{
		"name":      "vm-least-loaded",
		"vcpus":     1,
		"memory_mb": 512,
		"disk_mb":   1024,
	})
	if rLeast.StatusCode != http.StatusCreated {
		t.Fatalf("create vm-least-loaded failed: %d %s", rLeast.StatusCode, string(bLeast))
	}
	var vmLeast db.VM
	_ = json.Unmarshal(bLeast, &vmLeast)
	if vmLeast.WorkerID != wA.id {
		t.Fatalf("expected least_loaded to place on %s, got %s", wA.id, vmLeast.WorkerID)
	}

	// 6. Remote delete removes both control-plane and worker records
	delResp, delBody := doJSONRequest(t, http.MethodDelete, h.ts.URL+"/api/v1/vms/"+vmA.ID, nil)
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete remote vmA failed: %d %s", delResp.StatusCode, string(delBody))
	}
	if got, _ := h.store.GetVM(context.Background(), vmA.ID); got != nil {
		t.Fatalf("expected vmA deleted from control plane DB")
	}
	if got, _ := wA.store.GetVM(context.Background(), vmA.ID); got != nil {
		t.Fatalf("expected vmA deleted from worker DB")
	}
}

func TestMilestone2_WorkerUpdateAndPolicyValidation(t *testing.T) {
	h := newM2Harness(t, config.ResourceLimits{
		MaxTotalVCPUs:    2,
		MaxTotalMemoryMB: 1024,
		MaxTotalDiskMB:   2048,
	})

	// Policy GET & invalid PUT validation
	getPolResp, getPolBody := doJSONRequest(t, http.MethodGet, h.ts.URL+"/api/v1/scheduler/policy", nil)
	if getPolResp.StatusCode != http.StatusOK {
		t.Fatalf("GET policy failed: %d %s", getPolResp.StatusCode, string(getPolBody))
	}
	var pol db.ClusterSchedulingPolicy
	_ = json.Unmarshal(getPolBody, &pol)
	if pol.Strategy != "least_loaded" {
		t.Fatalf("expected default strategy least_loaded, got %q", pol.Strategy)
	}

	for _, bad := range []map[string]interface{}{
		{"strategy": "bogus"},
		{"cpu_soft_overcommit_ratio": 0.5},
		{"cpu_soft_overcommit_ratio": 2.0, "cpu_hard_overcommit_ratio": 1.5},
	} {
		r, _ := doJSONRequest(t, http.MethodPut, h.ts.URL+"/api/v1/scheduler/policy", bad)
		if r.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for invalid policy %v, got %d", bad, r.StatusCode)
		}
	}

	// Ensure nod_host_local exists with physical limits via GET /api/v1/workers
	_, _ = doJSONRequest(t, http.MethodGet, h.ts.URL+"/api/v1/workers", nil)
	localNode, err := h.store.GetWorkerNode(context.Background(), "nod_host_local")
	if err != nil || localNode == nil {
		t.Fatalf("nod_host_local missing: %v", err)
	}

	// Allocated > Physical must fail with 400
	badUpdResp, _ := doJSONRequest(t, http.MethodPatch, h.ts.URL+"/api/v1/workers/nod_host_local", map[string]interface{}{
		"allocated_vcpus": localNode.PhysicalVCPUs + 100,
	})
	if badUpdResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when allocated_vcpus > physical_vcpus, got %d", badUpdResp.StatusCode)
	}

	// Valid PATCH updates DB and syncs vmMgr resource limits
	updResp, updBody := doJSONRequest(t, http.MethodPatch, h.ts.URL+"/api/v1/workers/nod_host_local", map[string]interface{}{
		"allocated_vcpus":   1,
		"allocated_mem_mb":  512,
		"allocated_disk_mb": 4096,
		"pool_id":           "local-pool",
		"labels":            map[string]string{"role": "control"},
	})
	if updResp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH nod_host_local failed: %d %s", updResp.StatusCode, string(updBody))
	}
	limits := h.vmMgr.GetResourceLimits()
	if limits.MaxTotalVCPUs != 1 || limits.MaxTotalMemoryMB != 512 || limits.MaxTotalDiskMB != 4096 {
		t.Fatalf("expected vmMgr limits synced from PATCH worker, got %+v", limits)
	}
}

func TestMilestone2_IngressProxyAndTunnelOverWorker(t *testing.T) {
	h := newM2Harness(t, config.ResourceLimits{})
	w := h.connectWorker(t, "worker-tun", "default", config.ResourceLimits{
		MaxTotalVCPUs:    4,
		MaxTotalMemoryMB: 4096,
		MaxTotalDiskMB:   20000,
	}, "192.168.63.0/24")

	// Start HTTP service simulating guest port on worker host
	guestLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer guestLn.Close()
	guestPort := guestLn.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("proxied-from-worker:" + r.URL.Query().Get("k")))
	})
	guestSrv := &http.Server{Handler: mux}
	go func() { _ = guestSrv.Serve(guestLn) }()
	defer guestSrv.Close()

	// Persist a running remote VM on worker-tun pointing to 127.0.0.1
	remoteVM := &db.VM{
		ID:        "vm-remote-web",
		Name:      "remote-web",
		OwnerID:   "default",
		Status:    db.StatusRunning,
		IPAddress: "127.0.0.1",
		WorkerID:  w.id,
		PoolID:    "default",
	}
	if err := h.store.SaveVM(context.Background(), remoteVM); err != nil {
		t.Fatal(err)
	}

	if err := h.store.SavePortForward(context.Background(), &db.PortForward{
		ID: "pf_remote_web", VMID: remoteVM.ID, VMName: remoteVM.Name,
		GuestPort: guestPort, Protocol: "tcp", Subdomain: "remote-web",
		Status: "active", Public: true,
	}); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, h.ts.URL+"/hello?k=m2", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "remote-web.localhost"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET serve URL failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from remote worker ingress proxy, got %d: %s", resp.StatusCode, string(body))
	}
	if string(body) != "proxied-from-worker:m2" {
		t.Fatalf("unexpected proxied body: %q", string(body))
	}
}

func TestMilestone2_VMMigrationAndPoolMigration(t *testing.T) {
	h := newM2Harness(t, config.ResourceLimits{
		MaxTotalVCPUs:    8,
		MaxTotalMemoryMB: 8192,
		MaxTotalDiskMB:   40000,
	})
	w1 := h.connectWorker(t, "mig-w1", "pool-1", config.ResourceLimits{
		MaxTotalVCPUs:    8,
		MaxTotalMemoryMB: 8192,
		MaxTotalDiskMB:   40000,
	}, "192.168.64.0/24")
	w2 := h.connectWorker(t, "mig-w2", "pool-2", config.ResourceLimits{
		MaxTotalVCPUs:    8,
		MaxTotalMemoryMB: 8192,
		MaxTotalDiskMB:   40000,
	}, "192.168.65.0/24")

	// Create a local stopped VM
	createResp, createBody := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", map[string]interface{}{
		"name":      "vm-mig",
		"worker_id": "nod_host_local",
		"vcpus":     1,
		"memory_mb": 512,
		"disk_mb":   1024,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create local vm-mig failed: %d %s", createResp.StatusCode, string(createBody))
	}
	var v db.VM
	_ = json.Unmarshal(createBody, &v)

	// Write >64 KiB incompressible pseudo-random + zero-hole content into local disk file
	// to verify large WS RPC frames (SetReadLimit(16<<20)) and sparse chunk transfer.
	incompressible := make([]byte, 128*1024)
	rng := rand.New(rand.NewSource(42))
	_, _ = rng.Read(incompressible)
	diskData := append(incompressible, make([]byte, 32*1024)...)
	diskData = append(diskData, []byte("TAIL-CHECKPOINT-BYTES")...)
	if err := os.WriteFile(v.DiskPath, diskData, 0o600); err != nil {
		t.Fatal(err)
	}
	wantSum := sha256.Sum256(diskData)

	// 1. Migrate local -> worker-1
	m1Resp, m1Body := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/"+v.ID+"/migrate", api.MigrateVMRequest{
		TargetWorkerID: w1.id,
	})
	if m1Resp.StatusCode != http.StatusOK {
		t.Fatalf("migrate local->w1 failed: %d %s", m1Resp.StatusCode, string(m1Body))
	}
	var afterM1 db.VM
	_ = json.Unmarshal(m1Body, &afterM1)
	if afterM1.WorkerID != w1.id {
		t.Fatalf("expected WorkerID %s, got %s", w1.id, afterM1.WorkerID)
	}
	if _, err := os.Stat(v.DiskPath); !os.IsNotExist(err) {
		t.Fatalf("expected local source disk cleaned up after migration")
	}
	w1VM, _ := w1.vmMgr.GetVM(context.Background(), v.ID)
	if w1VM == nil {
		t.Fatalf("expected VM in w1 manager")
	}
	gotW1Bytes, err := os.ReadFile(w1VM.DiskPath)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(gotW1Bytes) != wantSum {
		t.Fatalf("disk checksum mismatch after local->w1 migration")
	}

	// 2. Start VM on w1, verify migrate without force returns 409, then migrate w1 -> w2 with force=true
	rRun, bRun := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/"+v.ID+"/run", nil)
	if rRun.StatusCode != http.StatusOK {
		t.Fatalf("run on w1 failed: %d %s", rRun.StatusCode, string(bRun))
	}
	conflictResp, _ := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/"+v.ID+"/migrate", api.MigrateVMRequest{
		TargetWorkerID: w2.id,
	})
	if conflictResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict migrating running VM without force, got %d", conflictResp.StatusCode)
	}

	m2Resp, m2Body := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/"+v.ID+"/migrate", api.MigrateVMRequest{
		TargetWorkerID: w2.id,
		Force:          true,
	})
	if m2Resp.StatusCode != http.StatusOK {
		t.Fatalf("migrate w1->w2 with force failed: %d %s", m2Resp.StatusCode, string(m2Body))
	}
	var afterM2 db.VM
	_ = json.Unmarshal(m2Body, &afterM2)
	if afterM2.WorkerID != w2.id || afterM2.Status != db.StatusRunning {
		t.Fatalf("expected running VM on %s after forced migration, got worker=%s status=%s", w2.id, afterM2.WorkerID, afterM2.Status)
	}
	if stale, _ := w1.store.GetVM(context.Background(), v.ID); stale != nil {
		t.Fatalf("expected source VM removed from w1")
	}
	w2VM, _ := w2.vmMgr.GetVM(context.Background(), v.ID)
	gotW2Bytes, err := os.ReadFile(w2VM.DiskPath)
	if err != nil || sha256.Sum256(gotW2Bytes) != wantSum {
		t.Fatalf("disk checksum mismatch after w1->w2 migration: err=%v", err)
	}

	// 3. Stop VM on w2, migrate w2 -> nod_host_local
	_, _ = doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/"+v.ID+"/stop", nil)
	m3Resp, m3Body := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/"+v.ID+"/migrate", api.MigrateVMRequest{
		TargetWorkerID: "nod_host_local",
	})
	if m3Resp.StatusCode != http.StatusOK {
		t.Fatalf("migrate w2->local failed: %d %s", m3Resp.StatusCode, string(m3Body))
	}
	var afterM3 db.VM
	_ = json.Unmarshal(m3Body, &afterM3)
	if afterM3.WorkerID != "nod_host_local" {
		t.Fatalf("expected nod_host_local after migration back, got %s", afterM3.WorkerID)
	}
	gotLocalBytes, err := os.ReadFile(afterM3.DiskPath)
	if err != nil || sha256.Sum256(gotLocalBytes) != wantSum {
		t.Fatalf("disk checksum mismatch after w2->local migration: err=%v", err)
	}

	// 4. Pool migration: create two VMs in pool-1 (on w1), then POST /api/v1/pools/pool-1/migrate -> pool-2
	var pool1IDs []string
	for i := 0; i < 2; i++ {
		pr, pb := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", map[string]interface{}{
			"name":      fmt.Sprintf("pool1-vm-%d", i),
			"worker_id": w1.id,
			"vcpus":     1,
			"memory_mb": 256,
			"disk_mb":   512,
		})
		if pr.StatusCode != http.StatusCreated {
			t.Fatalf("create pool1 VM %d failed: %d %s", i, pr.StatusCode, string(pb))
		}
		var pv db.VM
		_ = json.Unmarshal(pb, &pv)
		pool1IDs = append(pool1IDs, pv.ID)
	}

	pmResp, pmBody := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/pools/pool-1/migrate", api.MigratePoolRequest{
		TargetPoolID:     "pool-2",
		MigrateSandboxes: true,
	})
	if pmResp.StatusCode != http.StatusOK {
		t.Fatalf("pool migrate failed: %d %s", pmResp.StatusCode, string(pmBody))
	}
	var pm api.PoolMigrationResponse
	_ = json.Unmarshal(pmBody, &pm)
	if pm.SandboxesMigrated != 2 || pm.SandboxesFailed != 0 {
		t.Fatalf("expected 2 sandboxes migrated with 0 failures, got %+v", pm)
	}
	for _, id := range pool1IDs {
		got, _ := h.store.GetVM(context.Background(), id)
		if got == nil || got.WorkerID != w2.id || got.PoolID != "pool-2" {
			t.Fatalf("expected VM %s migrated to %s/pool-2, got %+v", id, w2.id, got)
		}
	}
}

func TestMilestone2_BulkDeleteAndBulkAction(t *testing.T) {
	h := newM2Harness(t, config.ResourceLimits{
		MaxTotalVCPUs:    32,
		MaxTotalMemoryMB: 32768,
		MaxTotalDiskMB:   100000,
	})
	w1 := h.connectWorker(t, "bulk-w1", "bulk-pool", config.ResourceLimits{
		MaxTotalVCPUs:    32,
		MaxTotalMemoryMB: 32768,
		MaxTotalDiskMB:   100000,
	}, "192.168.66.0/24")
	w2 := h.connectWorker(t, "bulk-w2", "bulk-pool", config.ResourceLimits{
		MaxTotalVCPUs:    32,
		MaxTotalMemoryMB: 32768,
		MaxTotalDiskMB:   100000,
	}, "192.168.67.0/24")
	w3 := h.connectWorker(t, "bulk-w3", "bulk-pool", config.ResourceLimits{
		MaxTotalVCPUs:    32,
		MaxTotalMemoryMB: 32768,
		MaxTotalDiskMB:   100000,
	}, "192.168.68.0/24")

	nodes := []string{"nod_host_local", w1.id, w2.id, w3.id}
	var allIDs []string
	var protectedID string

	// Create 25 VMs distributed across local & 3 remote workers; last one protected.
	for i := 0; i < 25; i++ {
		targetWorker := nodes[i%len(nodes)]
		payload := map[string]interface{}{
			"name":      fmt.Sprintf("bulk-vm-%02d", i),
			"worker_id": targetWorker,
			"vcpus":     1,
			"memory_mb": 128,
			"disk_mb":   512,
		}
		if i == 24 {
			payload["protected"] = true
		}
		r, b := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", payload)
		if r.StatusCode != http.StatusCreated {
			t.Fatalf("create bulk vm %d failed: %d %s", i, r.StatusCode, string(b))
		}
		var created db.VM
		_ = json.Unmarshal(b, &created)
		allIDs = append(allIDs, created.ID)
		if i == 24 {
			protectedID = created.ID
		}
	}

	// Bulk start all 25 VMs
	startResp, startBody := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/bulk-action", api.BulkVMsRequest{
		IDs:    allIDs,
		Action: "start",
	})
	if startResp.StatusCode != http.StatusOK {
		t.Fatalf("bulk start failed: %d %s", startResp.StatusCode, string(startBody))
	}
	var startRes api.BulkVMsResponse
	_ = json.Unmarshal(startBody, &startRes)
	if len(startRes.Succeeded) != 25 || len(startRes.Failed) != 0 {
		t.Fatalf("expected 25 succeeded on bulk start, got %+v", startRes)
	}

	// Bulk stop all 25 VMs
	stopResp, stopBody := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/bulk-action", api.BulkVMsRequest{
		IDs:    allIDs,
		Action: "stop",
	})
	if stopResp.StatusCode != http.StatusOK {
		t.Fatalf("bulk stop failed: %d %s", stopResp.StatusCode, string(stopBody))
	}
	var stopRes api.BulkVMsResponse
	_ = json.Unmarshal(stopBody, &stopRes)
	if len(stopRes.Succeeded) != 25 || len(stopRes.Failed) != 0 {
		t.Fatalf("expected 25 succeeded on bulk stop, got %+v", stopRes)
	}

	// Bulk-action "delete" must also reject protected VM and delete unprotected VMs.
	bulkActDelResp, bulkActDelBody := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/bulk-action", api.BulkVMsRequest{
		IDs:    []string{allIDs[0], protectedID},
		Action: "delete",
	})
	if bulkActDelResp.StatusCode != http.StatusOK {
		t.Fatalf("bulk-action delete failed: %d %s", bulkActDelResp.StatusCode, string(bulkActDelBody))
	}
	var bulkActDelRes api.BulkVMsResponse
	_ = json.Unmarshal(bulkActDelBody, &bulkActDelRes)
	if len(bulkActDelRes.Succeeded) != 1 || bulkActDelRes.Succeeded[0] != allIDs[0] ||
		len(bulkActDelRes.Failed) != 1 || bulkActDelRes.Failed[0].ID != protectedID ||
		!strings.Contains(bulkActDelRes.Failed[0].Error, "protected") {
		t.Fatalf("unexpected bulk-action delete result: %+v", bulkActDelRes)
	}

	// Bulk delete remaining 24 VMs -> 23 deleted, 1 protected failed
	delResp, delBody := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/bulk-delete", api.BulkVMsRequest{
		IDs: allIDs[1:],
	})
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("bulk delete failed: %d %s", delResp.StatusCode, string(delBody))
	}
	var delRes api.BulkVMsResponse
	_ = json.Unmarshal(delBody, &delRes)
	if delRes.Requested != 24 || len(delRes.Succeeded) != 23 || len(delRes.Failed) != 1 {
		t.Fatalf("expected 23 succeeded and 1 failed (protected), got %+v", delRes)
	}
	if delRes.Failed[0].ID != protectedID || !strings.Contains(delRes.Failed[0].Error, "protected") {
		t.Fatalf("unexpected failed item: %+v", delRes.Failed[0])
	}

	remaining, err := h.store.ListVMs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].ID != protectedID {
		t.Fatalf("expected only protected VM %s remaining, got %d VMs", protectedID, len(remaining))
	}
}

func TestMilestone2_PoolMigrationIntoEmptyPool(t *testing.T) {
	h := newM2Harness(t, config.ResourceLimits{
		MaxTotalVCPUs:    8,
		MaxTotalMemoryMB: 8192,
		MaxTotalDiskMB:   40000,
	})
	wSrc := h.connectWorker(t, "empty-pool-w1", "pool-src", config.ResourceLimits{
		MaxTotalVCPUs:    8,
		MaxTotalMemoryMB: 8192,
		MaxTotalDiskMB:   40000,
	}, "192.168.70.0/24")

	// Create VM1 on wSrc (in pool-src) and VM2 on nod_host_local tagged with pool_id="pool-src".
	r1, b1 := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", map[string]interface{}{
		"name":      "vm-on-moved-worker",
		"worker_id": wSrc.id,
		"vcpus":     1,
		"memory_mb": 256,
		"disk_mb":   512,
	})
	if r1.StatusCode != http.StatusCreated {
		t.Fatalf("create vm1 failed: %d %s", r1.StatusCode, string(b1))
	}
	var vm1 db.VM
	_ = json.Unmarshal(b1, &vm1)

	// Put nod_host_local temporarily in pool-src to create vm2 with PoolID="pool-src", then move nod_host_local back to default.
	pPatch1, pBody1 := doJSONRequest(t, http.MethodPatch, h.ts.URL+"/api/v1/workers/nod_host_local", map[string]interface{}{
		"pool_id": "pool-src",
	})
	if pPatch1.StatusCode != http.StatusOK {
		t.Fatalf("patch nod_host_local to pool-src failed: %d %s", pPatch1.StatusCode, string(pBody1))
	}
	r2, b2 := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", map[string]interface{}{
		"name":      "vm-on-local-in-src-pool",
		"worker_id": "nod_host_local",
		"pool_id":   "pool-src",
		"vcpus":     1,
		"memory_mb": 256,
		"disk_mb":   512,
	})
	if r2.StatusCode != http.StatusCreated {
		t.Fatalf("create vm2 failed: %d %s", r2.StatusCode, string(b2))
	}
	var vm2 db.VM
	_ = json.Unmarshal(b2, &vm2)
	pPatch2, pBody2 := doJSONRequest(t, http.MethodPatch, h.ts.URL+"/api/v1/workers/nod_host_local", map[string]interface{}{
		"pool_id": "default",
	})
	if pPatch2.StatusCode != http.StatusOK {
		t.Fatalf("restore nod_host_local to default failed: %d %s", pPatch2.StatusCode, string(pBody2))
	}

	// Target pool "pool-empty" has zero workers prior to migration.
	// MigrateWorkers=true & MigrateSandboxes=true must move wSrc first so vm2 can migrate onto wSrc in pool-empty,
	// and vm1 (already on wSrc) must have its PoolID updated to pool-empty.
	pmResp, pmBody := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/pools/pool-src/migrate", api.MigratePoolRequest{
		TargetPoolID:     "pool-empty",
		MigrateWorkers:   true,
		MigrateSandboxes: true,
	})
	if pmResp.StatusCode != http.StatusOK {
		t.Fatalf("pool migration into empty pool failed: %d %s", pmResp.StatusCode, string(pmBody))
	}
	var pm api.PoolMigrationResponse
	_ = json.Unmarshal(pmBody, &pm)
	if pm.WorkersMoved != 1 || pm.WorkersFailed != 0 || pm.SandboxesMigrated != 1 || pm.SandboxesFailed != 0 {
		t.Fatalf("unexpected pool migration summary: %+v", pm)
	}

	gotWorker, _ := h.store.GetWorkerNode(context.Background(), wSrc.id)
	if gotWorker == nil || gotWorker.PoolID != "pool-empty" {
		t.Fatalf("expected worker %s moved to pool-empty, got %+v", wSrc.id, gotWorker)
	}
	gotVM1, _ := h.store.GetVM(context.Background(), vm1.ID)
	if gotVM1 == nil || gotVM1.WorkerID != wSrc.id || gotVM1.PoolID != "pool-empty" {
		t.Fatalf("expected vm1 on %s with PoolID=pool-empty, got %+v", wSrc.id, gotVM1)
	}
	gotVM2, _ := h.store.GetVM(context.Background(), vm2.ID)
	if gotVM2 == nil || gotVM2.WorkerID != wSrc.id || gotVM2.PoolID != "pool-empty" {
		t.Fatalf("expected vm2 migrated onto %s in pool-empty, got %+v", wSrc.id, gotVM2)
	}
}

func TestMilestone2_MigrateAuthzNonAdmin403(t *testing.T) {
	env := newMultiTenantServer(t)
	mtSignup(t, env.ts, "miguser@example.com", "miguser", "SuperSecret123!")
	token, _ := mtLogin(t, env.ts, "miguser", "SuperSecret123!")

	createResp, createBody := mtRequest(t, env.ts, http.MethodPost, "/api/v1/vms", token, map[string]interface{}{
		"name":      "nonadmin-vm",
		"vcpus":     1,
		"memory_mb": 256,
		"disk_mb":   512,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create vm failed: %d %s", createResp.StatusCode, string(createBody))
	}
	var created db.VM
	_ = json.Unmarshal(createBody, &created)

	migResp, migBody := mtRequest(t, env.ts, http.MethodPost, "/api/v1/vms/"+created.ID+"/migrate", token, api.MigrateVMRequest{
		TargetWorkerID: "some-worker",
	})
	if migResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for non-admin migrate request, got %d: %s", migResp.StatusCode, string(migBody))
	}
}

func TestMilestone2_MigrateRollbackRestartsRunningSourceVM(t *testing.T) {
	h := newM2Harness(t, config.ResourceLimits{
		MaxTotalVCPUs:    8,
		MaxTotalMemoryMB: 8192,
		MaxTotalDiskMB:   40000,
	})
	w1 := h.connectWorker(t, "rb-w1", "rb-pool-1", config.ResourceLimits{
		MaxTotalVCPUs:    8,
		MaxTotalMemoryMB: 8192,
		MaxTotalDiskMB:   40000,
	}, "192.168.71.0/24")
	w2 := h.connectWorker(t, "rb-w2", "rb-pool-2", config.ResourceLimits{
		MaxTotalVCPUs:    8,
		MaxTotalMemoryMB: 8192,
		MaxTotalDiskMB:   40000,
	}, "192.168.72.0/24")

	createResp, createBody := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", map[string]interface{}{
		"name":       "vm-rollback",
		"worker_id":  w1.id,
		"vcpus":      1,
		"memory_mb":  256,
		"disk_mb":    512,
		"auto_start": true,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create running vm on w1 failed: %d %s", createResp.StatusCode, string(createBody))
	}
	var created db.VM
	_ = json.Unmarshal(createBody, &created)
	if created.Status != db.StatusRunning {
		t.Fatalf("expected created VM running, got %s", created.Status)
	}

	// Lower w2's local manager disk limit below 512 MB so control-plane placement selects w2,
	// but w2's ImportVMArtifactChunk rejects the incoming disk transfer.
	w2.vmMgr.SetResourceLimits(config.ResourceLimits{
		MaxTotalVCPUs:    8,
		MaxTotalMemoryMB: 8192,
		MaxTotalDiskMB:   100,
	})

	migResp, migBody := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/"+created.ID+"/migrate", api.MigrateVMRequest{
		TargetWorkerID: w2.id,
		Force:          true,
	})
	if migResp.StatusCode == http.StatusOK {
		t.Fatalf("expected migration to fail on target import disk limit, got 200: %s", string(migBody))
	}

	// Source VM must still reside on w1 and have been restarted back to StatusRunning.
	cpVM, err := h.store.GetVM(context.Background(), created.ID)
	if err != nil || cpVM == nil {
		t.Fatalf("get control-plane VM failed: %v", err)
	}
	if cpVM.WorkerID != w1.id || cpVM.Status != db.StatusRunning {
		t.Fatalf("expected rollback to keep VM on %s in running state, got worker=%s status=%s", w1.id, cpVM.WorkerID, cpVM.Status)
	}
	srcVM, err := w1.vmMgr.GetVM(context.Background(), created.ID)
	if err != nil || srcVM == nil || srcVM.Status != db.StatusRunning {
		t.Fatalf("expected source worker VM restarted to running, got %+v err=%v", srcVM, err)
	}
	if targetVM, _ := w2.vmMgr.GetVM(context.Background(), created.ID); targetVM != nil {
		t.Fatalf("expected target worker to have no VM record after rollback, got %+v", targetVM)
	}
}
