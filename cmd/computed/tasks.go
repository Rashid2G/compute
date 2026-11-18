package main

import (
	"encoding/json"
	"fmt"
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
	Status    string                `json:"status"` // queued | running | completed | partial | failed
	Stage     string                `json:"stage,omitempty"`
	Requested int                   `json:"requested"`
	Queued    int                   `json:"queued"`
	Completed int                   `json:"completed"`
	Succeeded []string              `json:"succeeded"`
	Failed    []api.BulkVMItemError `json:"failed"`
	Warnings  []api.BulkVMItemError `json:"warnings,omitempty"`
	ElapsedMs int64                 `json:"elapsed_ms,omitempty"`
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
	if rec.ElapsedMs <= 0 && !rec.CreatedAt.IsZero() {
		rec.ElapsedMs = now.Sub(rec.CreatedAt).Milliseconds()
	}
	if rec.Succeeded == nil {
		rec.Succeeded = []string{}
	}
	if rec.Failed == nil {
		rec.Failed = []api.BulkVMItemError{}
	}
	rec.Completed = len(rec.Succeeded) + len(rec.Failed)

	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if s.tasks == nil {
		s.tasks = make(map[string]*taskRecord)
	}
	if prev, exists := s.tasks[rec.ID]; exists && prev != nil {
		if !prev.CreatedAt.IsZero() {
			rec.CreatedAt = prev.CreatedAt
			rec.ElapsedMs = now.Sub(prev.CreatedAt).Milliseconds()
		}
	} else {
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
	stage := "Queued for execution"
	if resp.Queued > 0 {
		stage = fmt.Sprintf("Queued %d item(s) for %s", resp.Queued, resp.Action)
	}
	s.saveTask(&taskRecord{
		ID:        resp.TaskID,
		owner:     ownerID,
		Action:    resp.Action,
		Status:    "queued",
		Stage:     stage,
		Requested: resp.Requested,
		Queued:    resp.Queued,
		Succeeded: []string{},
		Failed:    resp.Failed,
		Warnings:  resp.Warnings,
	})
}

// recordTaskProgress updates an in-flight task's completed count and stage.
func (s *Server) recordTaskProgress(ownerID, taskID, action string, requested int, succeeded []string, failed []api.BulkVMItemError, warnings []api.BulkVMItemError, stage string) {
	queued := requested
	owner := ownerID
	var createdAt time.Time
	if prev, ok := s.getTask(taskID); ok {
		if prev.Queued > 0 {
			queued = prev.Queued
		}
		if owner == "" {
			owner = prev.owner
		}
		createdAt = prev.CreatedAt
	}
	succCopy := append([]string(nil), succeeded...)
	failCopy := append([]api.BulkVMItemError(nil), failed...)
	warnCopy := append([]api.BulkVMItemError(nil), warnings...)
	s.saveTask(&taskRecord{
		ID:        taskID,
		owner:     owner,
		Action:    action,
		Status:    "running",
		Stage:     stage,
		Requested: requested,
		Queued:    queued,
		Succeeded: succCopy,
		Failed:    failCopy,
		Warnings:  warnCopy,
		CreatedAt: createdAt,
	})
}

// recordTaskSummary stores the terminal state of a bulk operation, carrying
// over the queued count recorded when the request was accepted.
func (s *Server) recordTaskSummary(ownerID string, resp api.BulkVMsResponse) {
	queued := resp.Queued
	owner := ownerID
	var createdAt time.Time
	if prev, ok := s.getTask(resp.TaskID); ok {
		if queued == 0 {
			queued = prev.Queued
		}
		if owner == "" {
			owner = prev.owner
		}
		createdAt = prev.CreatedAt
	}
	status := taskStatusFor(resp.Succeeded, resp.Failed)
	stage := fmt.Sprintf("Completed %d of %d", len(resp.Succeeded), resp.Requested)
	if status == "failed" {
		stage = "Failed"
	} else if status == "partial" {
		stage = fmt.Sprintf("Completed %d of %d (%d failed)", len(resp.Succeeded), resp.Requested, len(resp.Failed))
	}
	s.saveTask(&taskRecord{
		ID:        resp.TaskID,
		owner:     owner,
		Action:    resp.Action,
		Status:    status,
		Stage:     stage,
		Requested: resp.Requested,
		Queued:    queued,
		Succeeded: resp.Succeeded,
		Failed:    resp.Failed,
		Warnings:  resp.Warnings,
		CreatedAt: createdAt,
	})
}

func (s *Server) getTask(id string) (*taskRecord, bool) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	rec, ok := s.tasks[id]
	if !ok || rec == nil {
		return nil, false
	}
	cp := *rec
	cp.Succeeded = append([]string(nil), rec.Succeeded...)
	cp.Failed = append([]api.BulkVMItemError(nil), rec.Failed...)
	cp.Warnings = append([]api.BulkVMItemError(nil), rec.Warnings...)
	if rec.Status == "queued" || rec.Status == "running" {
		cp.ElapsedMs = time.Since(rec.CreatedAt).Milliseconds()
	}
	return &cp, true
}

// handleListTasks serves GET /api/v1/tasks (recent and active background tasks).
func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ownerID, isAdmin := s.currentOwnerID(r)

	s.taskMu.Lock()
	out := make([]*taskRecord, 0, len(s.taskOrder))
	for i := len(s.taskOrder) - 1; i >= 0; i-- {
		id := s.taskOrder[i]
		rec := s.tasks[id]
		if rec == nil {
			continue
		}
		if !isAdmin && rec.owner != "" && rec.owner != ownerID {
			continue
		}
		cp := *rec
		cp.Succeeded = append([]string(nil), rec.Succeeded...)
		cp.Failed = append([]api.BulkVMItemError(nil), rec.Failed...)
		cp.Warnings = append([]api.BulkVMItemError(nil), rec.Warnings...)
		if rec.Status == "queued" || rec.Status == "running" {
			cp.ElapsedMs = time.Since(rec.CreatedAt).Milliseconds()
		}
		out = append(out, &cp)
	}
	s.taskMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
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
