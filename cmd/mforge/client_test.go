package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
)

func TestDaemonClient(t *testing.T) {
	mux := http.NewServeMux()

	vmMock := &db.VM{
		ID:        "vm-test1234",
		Name:      "test-vm",
		Status:    db.StatusRunning,
		IPAddress: "172.16.0.2",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	mux.HandleFunc("/api/v1/vms", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode([]*db.VM{vmMock})
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(vmMock)
		}
	})

	mux.HandleFunc("/api/v1/vms/vm-test1234", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(vmMock)
		case http.MethodDelete:
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
		}
	})

	mux.HandleFunc("/api/v1/vms/vm-test1234/run", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(vmMock)
	})

	mux.HandleFunc("/api/v1/vms/vm-test1234/save-state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		snapVM := *vmMock
		snapVM.Status = db.StatusPaused
		json.NewEncoder(w).Encode(&snapVM)
	})

	mux.HandleFunc("/api/v1/vms/vm-test1234/resume", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(vmMock)
	})

	mux.HandleFunc("/api/v1/vms/vm-test1234/stop", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		stopVM := *vmMock
		stopVM.Status = db.StatusStopped
		json.NewEncoder(w).Encode(&stopVM)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewDaemonClient(server.URL)
	ctx := context.Background()

	// 1. Create
	created, err := client.CreateVM(ctx, vm.CreateRequest{Name: "test-vm"})
	if err != nil || created.ID != "vm-test1234" {
		t.Fatalf("CreateVM failed: %v, got %+v", err, created)
	}

	// 2. List
	vms, err := client.ListVMs(ctx)
	if err != nil || len(vms) != 1 {
		t.Fatalf("ListVMs failed: %v, got %d", err, len(vms))
	}

	// 3. Get
	got, err := client.GetVM(ctx, "vm-test1234")
	if err != nil || got.ID != "vm-test1234" {
		t.Fatalf("GetVM failed: %v", err)
	}

	// 4. Run
	ran, err := client.RunVM(ctx, "vm-test1234")
	if err != nil || ran.Status != db.StatusRunning {
		t.Fatalf("RunVM failed: %v", err)
	}

	// 5. SaveState
	saved, err := client.SaveState(ctx, "vm-test1234")
	if err != nil || saved.Status != db.StatusPaused {
		t.Fatalf("SaveState failed: %v", err)
	}

	// 6. Resume
	resumed, err := client.ResumeVM(ctx, "vm-test1234")
	if err != nil || resumed.Status != db.StatusRunning {
		t.Fatalf("ResumeVM failed: %v", err)
	}

	// 7. Stop
	stopped, err := client.StopVM(ctx, "vm-test1234")
	if err != nil || stopped.Status != db.StatusStopped {
		t.Fatalf("StopVM failed: %v", err)
	}

	// 8. Delete
	if err := client.DeleteVM(ctx, "vm-test1234"); err != nil {
		t.Fatalf("DeleteVM failed: %v", err)
	}
}
