package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/config"
	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/Rashid2G/compute/pkg/api"
)

func pollUntil(t *testing.T, timeout time.Duration, desc string, cond func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		ok, err := cond()
		if err != nil {
			t.Fatalf("%s: %v", desc, err)
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", desc)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestBulkCreateVMsQueued(t *testing.T) {
	h := newM2Harness(t, config.ResourceLimits{
		MaxRunningVMs:      32,
		MaxTotalVCPUs:      64,
		MaxTotalMemoryMB:   65536,
		MaxTotalDiskMB:     512000,
		MinFreeDiskSpaceMB: 0,
	})

	items := make([]vm.CreateRequest, 0, 10)
	for i := 1; i <= 10; i++ {
		items = append(items, vm.CreateRequest{
			Name:      fmt.Sprintf("sbx-batch-%02d", i),
			VCPUs:     1,
			MemoryMB:  512,
			DiskMB:    1024,
			AutoStart: true,
		})
	}

	asyncTrue := true
	resp, body := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/bulk-create", BulkCreateVMsRequest{
		Items: items,
		Async: &asyncTrue,
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", resp.StatusCode, string(body))
	}

	var bulkResp api.BulkVMsResponse
	if err := json.Unmarshal(body, &bulkResp); err != nil {
		t.Fatalf("decode bulk-create response: %v", err)
	}
	if bulkResp.Action != "create" || bulkResp.Status != "queued" {
		t.Fatalf("unexpected response metadata: %+v", bulkResp)
	}
	if bulkResp.Requested != 10 || bulkResp.Queued != 10 || bulkResp.TaskID == "" {
		t.Fatalf("unexpected counts/task_id: %+v", bulkResp)
	}

	pollUntil(t, 10*time.Second, "all 10 queued sandboxes created and running", func() (bool, error) {
		vms, err := h.store.ListVMs(context.Background())
		if err != nil {
			return false, err
		}
		if len(vms) < 10 {
			return false, nil
		}
		for _, v := range vms {
			if v.Status != db.StatusRunning {
				return false, nil
			}
		}
		return true, nil
	})

	vms, err := h.store.ListVMs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vms) != 10 {
		t.Fatalf("expected 10 VMs, got %d", len(vms))
	}
	seenNames := make(map[string]bool)
	seenIPs := make(map[string]bool)
	for _, v := range vms {
		if seenNames[v.Name] {
			t.Fatalf("duplicate VM name %s", v.Name)
		}
		seenNames[v.Name] = true
		if v.IPAddress == "" {
			t.Fatalf("VM %s missing IP address", v.Name)
		}
		if seenIPs[v.IPAddress] {
			t.Fatalf("duplicate VM IP %s", v.IPAddress)
		}
		seenIPs[v.IPAddress] = true
	}
}

func TestBulkCloneVMsQueued(t *testing.T) {
	h := newM2Harness(t, config.ResourceLimits{
		MaxRunningVMs:      16,
		MaxTotalVCPUs:      32,
		MaxTotalMemoryMB:   32768,
		MaxTotalDiskMB:     512000,
		MinFreeDiskSpaceMB: 0,
	})

	resp, body := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", vm.CreateRequest{
		Name:      "clone-src",
		VCPUs:     1,
		MemoryMB:  512,
		DiskMB:    1024,
		AutoStart: false,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create source VM failed: %d %s", resp.StatusCode, string(body))
	}
	var src db.VM
	if err := json.Unmarshal(body, &src); err != nil {
		t.Fatal(err)
	}

	asyncTrue := true
	cResp, cBody := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/bulk-clone", BulkCloneVMsRequest{
		SourceID:  src.ID,
		Names:     []string{"pool-1", "pool-2", "pool-3"},
		VCPUs:     2,
		AutoStart: true,
		Async:     &asyncTrue,
	})
	if cResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", cResp.StatusCode, string(cBody))
	}

	var bulkResp api.BulkVMsResponse
	if err := json.Unmarshal(cBody, &bulkResp); err != nil {
		t.Fatal(err)
	}
	if bulkResp.Action != "clone" || bulkResp.Status != "queued" || bulkResp.Queued != 3 {
		t.Fatalf("unexpected bulk-clone response: %+v", bulkResp)
	}

	pollUntil(t, 10*time.Second, "all 3 cloned sandboxes created and running", func() (bool, error) {
		vms, err := h.store.ListVMs(context.Background())
		if err != nil {
			return false, err
		}
		runningClones := 0
		for _, v := range vms {
			if v.ID != src.ID && v.Status == db.StatusRunning && v.VCPUs == 2 {
				runningClones++
			}
		}
		return runningClones == 3, nil
	})
}

func TestRestartVMSingleRequest(t *testing.T) {
	h := newM2Harness(t, config.ResourceLimits{
		MaxRunningVMs:      8,
		MaxTotalVCPUs:      16,
		MaxTotalMemoryMB:   16384,
		MaxTotalDiskMB:     128000,
		MinFreeDiskSpaceMB: 0,
	})

	resp, body := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", vm.CreateRequest{
		Name:      "restart-me",
		VCPUs:     1,
		MemoryMB:  512,
		DiskMB:    1024,
		AutoStart: true,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create VM failed: %d %s", resp.StatusCode, string(body))
	}
	var created db.VM
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if created.Status != db.StatusRunning || created.PID == 0 {
		t.Fatalf("expected running VM with non-zero PID, got status=%s pid=%d", created.Status, created.PID)
	}

	rResp, rBody := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/"+created.ID+"/restart", nil)
	if rResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from restart, got %d: %s", rResp.StatusCode, string(rBody))
	}
	var restarted db.VM
	if err := json.Unmarshal(rBody, &restarted); err != nil {
		t.Fatal(err)
	}
	if restarted.Status != db.StatusRunning || restarted.PID == created.PID || restarted.PID == 0 {
		t.Fatalf("expected restarted VM running with new PID (old=%d), got status=%s pid=%d", created.PID, restarted.Status, restarted.PID)
	}
}

func TestBulkActionQueuedLifecycleAndProtect(t *testing.T) {
	h := newM2Harness(t, config.ResourceLimits{
		MaxRunningVMs:      16,
		MaxTotalVCPUs:      32,
		MaxTotalMemoryMB:   32768,
		MaxTotalDiskMB:     256000,
		MinFreeDiskSpaceMB: 0,
	})

	var ids []string
	initialPIDs := make(map[string]int)
	for i := 1; i <= 3; i++ {
		resp, body := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", vm.CreateRequest{
			Name:      fmt.Sprintf("bulk-act-%d", i),
			VCPUs:     1,
			MemoryMB:  512,
			DiskMB:    1024,
			AutoStart: true,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %d failed: %d %s", i, resp.StatusCode, string(body))
		}
		var created db.VM
		if err := json.Unmarshal(body, &created); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, created.ID)
		initialPIDs[created.ID] = created.PID
	}

	sendBulkAction := func(action string) {
		t.Helper()
		resp, body := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/bulk-action", api.BulkVMsRequest{
			IDs:    ids,
			Action: action,
			Async:  true,
		})
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("bulk-action %s expected 202, got %d: %s", action, resp.StatusCode, string(body))
		}
	}

	sendBulkAction("protect")
	pollUntil(t, 5*time.Second, "all VMs protected", func() (bool, error) {
		for _, id := range ids {
			v, err := h.store.GetVM(context.Background(), id)
			if err != nil || !v.Protected {
				return false, err
			}
		}
		return true, nil
	})

	sendBulkAction("stop")
	pollUntil(t, 5*time.Second, "all VMs stopped", func() (bool, error) {
		for _, id := range ids {
			v, err := h.store.GetVM(context.Background(), id)
			if err != nil || v.Status != db.StatusStopped {
				return false, err
			}
		}
		return true, nil
	})

	sendBulkAction("start")
	pollUntil(t, 5*time.Second, "all VMs started", func() (bool, error) {
		for _, id := range ids {
			v, err := h.store.GetVM(context.Background(), id)
			if err != nil || v.Status != db.StatusRunning {
				return false, err
			}
		}
		return true, nil
	})

	sendBulkAction("restart")
	pollUntil(t, 5*time.Second, "all VMs restarted with new PIDs", func() (bool, error) {
		for _, id := range ids {
			v, err := h.store.GetVM(context.Background(), id)
			if err != nil || v.Status != db.StatusRunning || v.PID == initialPIDs[id] {
				return false, err
			}
		}
		return true, nil
	})

	sendBulkAction("unprotect")
	pollUntil(t, 5*time.Second, "all VMs unprotected", func() (bool, error) {
		for _, id := range ids {
			v, err := h.store.GetVM(context.Background(), id)
			if err != nil || v.Protected {
				return false, err
			}
		}
		return true, nil
	})

	// Bulk delete queued cleanly deletes all 3 VMs.
	dResp, dBody := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/bulk-delete", api.BulkVMsRequest{
		IDs:   ids,
		Async: true,
	})
	if dResp.StatusCode != http.StatusAccepted {
		t.Fatalf("bulk-delete expected 202, got %d: %s", dResp.StatusCode, string(dBody))
	}
	t.Logf("bulk-delete response: %s", string(dBody))
	pollUntil(t, 5*time.Second, "all VMs deleted", func() (bool, error) {
		vms, err := h.store.ListVMs(context.Background())
		if err != nil {
			return false, err
		}
		return len(vms) == 0, nil
	})
}

func TestDetachedExecutionSurvivesClientDisconnect(t *testing.T) {
	h := newM2Harness(t, config.ResourceLimits{
		MaxRunningVMs:      8,
		MaxTotalVCPUs:      16,
		MaxTotalMemoryMB:   16384,
		MaxTotalDiskMB:     128000,
		MinFreeDiskSpaceMB: 0,
	})

	started := make(chan struct{})
	release := make(chan struct{})
	var launcherCtxCanceled atomic.Bool
	var once sync.Once
	var pidSeq atomic.Int64
	pidSeq.Store(60000)

	h.vmMgr.SetProcessChecker(func(pid int) bool { return pid > 60000 })
	h.vmMgr.SetLauncher(func(ctx context.Context, v *db.VM) error {
		once.Do(func() { close(started) })
		<-release
		if ctx.Err() != nil {
			launcherCtxCanceled.Store(true)
			return ctx.Err()
		}
		v.PID = int(pidSeq.Add(1))
		v.Status = db.StatusRunning
		v.DesiredRunning = true
		return nil
	})

	payload, _ := json.Marshal(vm.CreateRequest{
		Name:      "detached-sbx",
		VCPUs:     1,
		MemoryMB:  512,
		DiskMB:    1024,
		AutoStart: true,
	})

	reqCtx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, h.ts.URL+"/api/v1/vms", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	httpDone := make(chan error, 1)
	go func() {
		resp, reqErr := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		httpDone <- reqErr
	}()

	<-started
	// Simulate user refreshing the page / closing browser while POST /api/v1/vms is mid-boot.
	cancel()
	if reqErr := <-httpDone; reqErr == nil {
		t.Fatalf("expected client request error after cancel")
	}

	// Let backend launch finish after HTTP disconnect.
	close(release)

	pollUntil(t, 5*time.Second, "detached VM finished booting despite HTTP disconnect", func() (bool, error) {
		v, err := h.store.GetVM(context.Background(), "detached-sbx")
		if err != nil {
			return false, nil
		}
		return v != nil && v.Status == db.StatusRunning && v.PID > 60000, nil
	})

	if launcherCtxCanceled.Load() {
		t.Fatalf("expected detached backend context not to be canceled when HTTP client disconnected")
	}
}

func TestRunDetachedRecoversPanic(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vms", nil)
	val, err, disconnected := runDetached(req, func(ctx context.Context) (string, error) {
		panic("boom in detached op")
	})
	if disconnected {
		t.Fatal("unexpected disconnect")
	}
	if err == nil {
		t.Fatal("expected an error from a panicking detached op")
	}
	if !strings.Contains(err.Error(), "internal error") || !strings.Contains(err.Error(), "boom in detached op") {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "" {
		t.Fatalf("expected zero value, got %q", val)
	}
}

func TestServerOpQueueBoundsDepth(t *testing.T) {
	q := newServerOpQueue(1, 1)
	release := make(chan struct{})
	started := make(chan struct{}, 1)

	if err := q.enqueue(func() {
		started <- struct{}{}
		<-release
	}); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	<-started
	// The single worker is busy, so one more item fits the depth of 1.
	if err := q.enqueue(func() {}); err != nil {
		t.Fatalf("second enqueue: %v", err)
	}
	// A third would exceed the bound.
	if err := q.enqueue(func() {}); !errors.Is(err, errBackendQueueFull) {
		t.Fatalf("expected errBackendQueueFull, got %v", err)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for q.pendingCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBulkCreateRejectsOversizedRequest(t *testing.T) {
	h := newM2Harness(t, config.ResourceLimits{
		MaxRunningVMs:      200,
		MaxTotalVCPUs:      400,
		MaxTotalMemoryMB:   200000,
		MaxTotalDiskMB:     512000,
		MinFreeDiskSpaceMB: 0,
	})

	items := make([]vm.CreateRequest, maxBulkRequestItems+1)
	for i := range items {
		items[i] = vm.CreateRequest{Name: fmt.Sprintf("too-many-%d", i), VCPUs: 1, MemoryMB: 128, DiskMB: 1024}
	}
	resp, body := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/bulk-create", BulkCreateVMsRequest{Items: items})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized bulk request, got %d: %s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "too many items") {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestBulkTaskStatusResolves(t *testing.T) {
	h := newM2Harness(t, config.ResourceLimits{
		MaxRunningVMs:      16,
		MaxTotalVCPUs:      32,
		MaxTotalMemoryMB:   32768,
		MaxTotalDiskMB:     512000,
		MinFreeDiskSpaceMB: 0,
	})

	asyncTrue := true
	resp, body := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/bulk-create", BulkCreateVMsRequest{
		Items: []vm.CreateRequest{
			{Name: "task-a", VCPUs: 1, MemoryMB: 256, DiskMB: 1024, AutoStart: true},
			{Name: "task-b", VCPUs: 1, MemoryMB: 256, DiskMB: 1024, AutoStart: true},
		},
		Async: &asyncTrue,
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", resp.StatusCode, string(body))
	}
	var queued api.BulkVMsResponse
	if err := json.Unmarshal(body, &queued); err != nil {
		t.Fatal(err)
	}
	if queued.TaskID == "" {
		t.Fatal("expected task_id in queued response")
	}

	var last taskRecord
	pollUntil(t, 10*time.Second, "bulk create task completed", func() (bool, error) {
		tResp, tBody := doJSONRequest(t, http.MethodGet, h.ts.URL+"/api/v1/tasks/"+queued.TaskID, nil)
		if tResp.StatusCode != http.StatusOK {
			return false, fmt.Errorf("task status %d: %s", tResp.StatusCode, string(tBody))
		}
		if err := json.Unmarshal(tBody, &last); err != nil {
			return false, err
		}
		return last.Status == "completed" && len(last.Succeeded) == 2, nil
	})

	if last.Action != "create" || last.Requested != 2 || last.Queued != 2 {
		t.Fatalf("unexpected task record: %+v", last)
	}

	// Unknown task ids are a clean 404.
	missing, _ := doJSONRequest(t, http.MethodGet, h.ts.URL+"/api/v1/tasks/task_missing", nil)
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown task, got %d", missing.StatusCode)
	}
}

func TestCreateAutoStartFailureReportsWarning(t *testing.T) {
	h := newM2Harness(t, config.ResourceLimits{
		MaxRunningVMs:      8,
		MaxTotalVCPUs:      16,
		MaxTotalMemoryMB:   16384,
		MaxTotalDiskMB:     128000,
		MinFreeDiskSpaceMB: 0,
	})
	h.vmMgr.SetLauncher(func(ctx context.Context, v *db.VM) error {
		return fmt.Errorf("simulated boot failure")
	})

	resp, body := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", vm.CreateRequest{
		Name:      "boot-fail",
		VCPUs:     1,
		MemoryMB:  512,
		DiskMB:    1024,
		AutoStart: true,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 with warning, got %d: %s", resp.StatusCode, string(body))
	}
	warn := resp.Header.Get("X-Compute-Boot-Warning")
	if !strings.Contains(warn, "simulated boot failure") {
		t.Fatalf("expected boot warning header, got %q", warn)
	}
	var created db.VM
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if created.Status == db.StatusRunning {
		t.Fatalf("expected VM to remain stopped after boot failure, got %s", created.Status)
	}
}
