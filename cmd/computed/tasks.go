package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Rashid2G/compute/pkg/api"
)

// taskRecord tracks the lifecycle of an accepted asynchronous bulk operation so
// clients can resolve a "queued" acknowledgement into a final outcome instead
// of guessing from cluster state.
type taskRecord struct {
	ID        string                `json:"id"`
	Action    string                `json:"action"`
	Status    string                `json:"status"` // queued | completed | partial | failed
	Requested int                   `json:"requested"`
	Queued    int                   `json:"queued"`
	Succeeded []string              `json:"succeeded"`
	Failed    []api.BulkVMItemError `json:"failed"`
	Warnings  []api.BulkVMItemError `json:"warnings,omitempty"`
	owner     string
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func taskStatusFor(succeeded []string, failed []api.BulkVMItemError) string {
	switch {
	case len(failed) == 0:
		return "completed"
	case len(succeeded) == 0:
		return "failed"
	default:
		return "partial"
	}
}

// saveTask inserts or replaces a task record, evicting the oldest entries once
// the registry reaches maxTrackedTasks.
func (s *Server) saveTask(rec *taskRecord) {
	if rec == nil || rec.ID == "" {
		return
	}
	now := time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	if rec.Succeeded == nil {
		rec.Succeeded = []string{}
	}
	if rec.Failed == nil {
		rec.Failed = []api.BulkVMItemError{}
	}

	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if s.tasks == nil {
		s.tasks = make(map[string]*taskRecord)
	}
	if _, exists := s.tasks[rec.ID]; !exists {
		s.taskOrder = append(s.taskOrder, rec.ID)
	}
	s.tasks[rec.ID] = rec
	for len(s.taskOrder) > maxTrackedTasks {
		oldest := s.taskOrder[0]
		s.taskOrder = s.taskOrder[1:]
		delete(s.tasks, oldest)
	}
}

// recordQueuedTask stores the acknowledgement returned when a bulk operation is
// accepted for background execution.
func (s *Server) recordQueuedTask(ownerID string, resp api.BulkVMsResponse) {
	s.saveTask(&taskRecord{
		ID:        resp.TaskID,
		owner:     ownerID,
		Action:    resp.Action,
		Status:    "queued",
		Requested: resp.Requested,
		Queued:    resp.Queued,
		Succeeded: []string{},
		Failed:    resp.Failed,
		Warnings:  resp.Warnings,
	})
}

// recordTaskSummary stores the terminal state of a bulk operation, carrying
// over the queued count recorded when the request was accepted.
func (s *Server) recordTaskSummary(ownerID string, resp api.BulkVMsResponse) {
	queued := resp.Queued
	owner := ownerID
	if prev, ok := s.getTask(resp.TaskID); ok {
		if queued == 0 {
			queued = prev.Queued
		}
		if owner == "" {
			owner = prev.owner
		}
	}
	s.saveTask(&taskRecord{
		ID:        resp.TaskID,
		owner:     owner,
		Action:    resp.Action,
		Status:    taskStatusFor(resp.Succeeded, resp.Failed),
		Requested: resp.Requested,
		Queued:    queued,
		Succeeded: resp.Succeeded,
		Failed:    resp.Failed,
		Warnings:  resp.Warnings,
	})
}

func (s *Server) getTask(id string) (*taskRecord, bool) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	rec, ok := s.tasks[id]
	return rec, ok
}

// handleGetTask serves GET /api/v1/tasks/{id}.
func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/")
	id = strings.TrimSpace(strings.Trim(id, "/"))
	if id == "" {
		httpError(w, "task id is required", http.StatusBadRequest)
		return
	}
	rec, ok := s.getTask(id)
	if !ok {
		httpError(w, "task not found", http.StatusNotFound)
		return
	}
	// Task records describe another tenant's sandbox ids and errors, so hide
	// them from anyone but the submitter (and admins).
	if ownerID, isAdmin := s.currentOwnerID(r); !isAdmin && rec.owner != "" && rec.owner != ownerID {
		httpError(w, "task not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rec)
}

// headerSafe makes an error message safe for use as an HTTP header value.
func headerSafe(msg string) string {
	msg = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(msg)
	if len(msg) > 512 {
		msg = msg[:512]
	}
	return msg
}
