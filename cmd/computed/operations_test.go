package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/config"
	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/Rashid2G/compute/pkg/api"
)

func testOperationLimits() config.ResourceLimits {
	return config.ResourceLimits{
		MaxRunningVMs:      32,
		MaxTotalVCPUs:      64,
		MaxTotalMemoryMB:   65536,
		MaxTotalDiskMB:     512000,
		MinFreeDiskSpaceMB: 0,
	}
}

func getOperation(t *testing.T, baseURL, id string) *db.Operation {
	t.Helper()
	resp, body := doJSONRequest(t, http.MethodGet, baseURL+"/api/v1/operations/"+id, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get operation %s: %d %s", id, resp.StatusCode, string(body))
	}
	var op db.Operation
	if err := json.Unmarshal(body, &op); err != nil {
		t.Fatalf("decode operation: %v", err)
	}
	return &op
}

func TestSingleCreateRecordsOperationBeforeCompletion(t *testing.T) {
	h := newM2Harness(t, testOperationLimits())

	resp, body := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", vm.CreateRequest{
		Name:      "op-single",
		VCPUs:     1,
		MemoryMB:  512,
		DiskMB:    1024,
		AutoStart: false,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create sandbox: %d %s", resp.StatusCode, string(body))
	}
	opID := resp.Header.Get("X-Compute-Operation-ID")
	if opID == "" {
		t.Fatal("expected X-Compute-Operation-ID header")
	}

	op := getOperation(t, h.ts.URL, opID)
	if op.Kind != "sandbox.create" {
		t.Fatalf("kind = %q, want sandbox.create", op.Kind)
	}
	if op.Status != db.OperationStatusSucceeded {
		t.Fatalf("status = %q, want succeeded (%s)", op.Status, op.ErrorMsg)
	}
	if op.TargetName != "op-single" {
		t.Fatalf("target_name = %q, want op-single", op.TargetName)
	}
	if op.TargetID == "" {
		t.Fatal("expected operation target_id to be populated")
	}

	// The request must be in the durable audit trail before/around execution.
	events, err := h.store.ListAuditEvents(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var sawRequested, sawFinished bool
	for _, ev := range events {
		if strings.Contains(ev.Payload, opID) {
			switch ev.Event {
			case "operation.requested":
				sawRequested = true
			case "operation.succeeded":
				sawFinished = true
			}
		}
	}
	if !sawRequested {
		t.Fatal("expected operation.requested audit event")
	}
	if !sawFinished {
		t.Fatal("expected operation.succeeded audit event")
	}
}

func TestFailedCreateLeavesFailedOperation(t *testing.T) {
	h := newM2Harness(t, testOperationLimits())

	create := func() (*http.Response, []byte) {
		return doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms", vm.CreateRequest{
			Name:      "op-dup",
			VCPUs:     1,
			MemoryMB:  512,
			DiskMB:    1024,
			AutoStart: false,
		})
	}
	if resp, body := create(); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first create: %d %s", resp.StatusCode, string(body))
	}
	resp, body := create()
	if resp.StatusCode == http.StatusCreated {
		t.Fatalf("duplicate create unexpectedly succeeded: %s", string(body))
	}
	opID := resp.Header.Get("X-Compute-Operation-ID")
	if opID == "" {
		t.Fatal("expected operation id for failed create")
	}
	op := getOperation(t, h.ts.URL, opID)
	if op.Status != db.OperationStatusFailed {
		t.Fatalf("status = %q, want failed", op.Status)
	}
	if !strings.Contains(strings.ToLower(op.ErrorMsg), "already exists") {
		t.Fatalf("error_msg = %q, want duplicate-name reason", op.ErrorMsg)
	}
}

func TestBulkCreateOperationsAreVisibleWhileQueued(t *testing.T) {
	h := newM2Harness(t, testOperationLimits())

	items := make([]vm.CreateRequest, 0, 6)
	for i := 1; i <= 6; i++ {
		items = append(items, vm.CreateRequest{
			Name:      fmt.Sprintf("op-batch-%02d", i),
			VCPUs:     1,
			MemoryMB:  512,
			DiskMB:    1024,
			AutoStart: true,
		})
	}
	async := true
	resp, body := doJSONRequest(t, http.MethodPost, h.ts.URL+"/api/v1/vms/bulk-create", BulkCreateVMsRequest{
		Items: items,
		Async: &async,
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("bulk create: %d %s", resp.StatusCode, string(body))
	}
	var bulk api.BulkVMsResponse
	if err := json.Unmarshal(body, &bulk); err != nil {
		t.Fatal(err)
	}
	if len(bulk.OperationIDs) != len(items) {
		t.Fatalf("operation_ids = %d, want %d", len(bulk.OperationIDs), len(items))
	}
	if bulk.TaskID == "" {
		t.Fatal("expected task id")
	}

	// Every accepted item must already have a durable operation row, even
	// before the backend workers get to it.
	listResp, listBody := doJSONRequest(t, http.MethodGet, h.ts.URL+"/api/v1/operations?task_id="+bulk.TaskID, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list operations: %d %s", listResp.StatusCode, string(listBody))
	}
	var ops []db.Operation
	if err := json.Unmarshal(listBody, &ops); err != nil {
		t.Fatal(err)
	}
	if len(ops) != len(items) {
		t.Fatalf("persisted operations = %d, want %d", len(ops), len(items))
	}
	names := make(map[string]bool, len(ops))
	for _, op := range ops {
		if op.Kind != "sandbox.create" {
			t.Fatalf("unexpected operation kind %q", op.Kind)
		}
		if op.TaskID != bulk.TaskID {
			t.Fatalf("operation task_id = %q, want %q", op.TaskID, bulk.TaskID)
		}
		names[op.TargetName] = true
	}
	for _, item := range items {
		if !names[item.Name] {
			t.Fatalf("missing operation for %q", item.Name)
		}
	}

	pollUntil(t, 15*time.Second, "bulk task and operations complete", func() (bool, error) {
		taskResp, taskBody := doJSONRequest(t, http.MethodGet, h.ts.URL+"/api/v1/tasks/"+bulk.TaskID, nil)
		if taskResp.StatusCode != http.StatusOK {
			return false, fmt.Errorf("task endpoint: %d %s", taskResp.StatusCode, string(taskBody))
		}
		var task taskRecord
		if err := json.Unmarshal(taskBody, &task); err != nil {
			return false, err
		}
		if task.Status == "queued" {
			return false, nil
		}
		_, opsBody := doJSONRequest(t, http.MethodGet, h.ts.URL+"/api/v1/operations?task_id="+bulk.TaskID, nil)
		var finalOps []db.Operation
		if err := json.Unmarshal(opsBody, &finalOps); err != nil {
			return false, err
		}
		if len(finalOps) != len(items) {
			return false, fmt.Errorf("operations = %d, want %d", len(finalOps), len(items))
		}
		for _, op := range finalOps {
			if op.Status != db.OperationStatusSucceeded {
				return false, nil
			}
		}
		return true, nil
	})
}
