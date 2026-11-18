package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Rashid2G/compute/pkg/api"
)

func TestTaskProgressAndList(t *testing.T) {
	s := &Server{}

	s.recordQueuedTask("alice", api.BulkVMsResponse{
		TaskID:    "task-progress-1",
		Action:    "create",
		Requested: 3,
		Queued:    3,
	})
	s.recordTaskProgress("alice", "task-progress-1", "create", 3, []string{"a"}, nil, nil, "Provisioning sandboxes (1/3 finished · a)")
	s.recordTaskSummary("alice", api.BulkVMsResponse{
		TaskID:    "task-progress-1",
		Action:    "create",
		Requested: 3,
		Succeeded: []string{"a", "b", "c"},
		Failed:    []api.BulkVMItemError{},
	})

	rec, ok := s.getTask("task-progress-1")
	if !ok || rec == nil {
		t.Fatal("expected task to exist")
	}
	if rec.Status != "completed" || rec.Completed != 3 || rec.Requested != 3 {
		t.Fatalf("unexpected terminal task: %+v", rec)
	}
	if rec.Stage == "" {
		t.Fatal("expected terminal stage to be set")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	rr := httptest.NewRecorder()
	s.handleListTasks(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var list []*taskRecord
	if err := json.NewDecoder(rr.Body).Decode(&list); err != nil {
		t.Fatalf("decode tasks: %v", err)
	}
	if len(list) != 1 || list[0].ID != "task-progress-1" {
		t.Fatalf("unexpected task list: %+v", list)
	}
}
