package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/pkg/api"
)

var errTaskStoreUnavailable = errors.New("task store unavailable")

// taskRecord tracks the lifecycle of an accepted asynchronous bulk operation so
// clients can resolve a "queued" acknowledgement into a final outcome instead
// of guessing from cluster state. Records are persisted in SQLite and cached in
// memory so they survive daemon restarts and concurrent reads.
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

func (rec *taskRecord) toStoreRecord() *db.TaskRecord {
	succeeded, _ := json.Marshal(rec.Succeeded)
	failed, _ := json.Marshal(rec.Failed)
	warnings, _ := json.Marshal(rec.Warnings)
	return &db.TaskRecord{
		ID:            rec.ID,
		OwnerID:       rec.owner,
		Action:        rec.Action,
		Status:        rec.Status,
		Requested:     rec.Requested,
		Queued:        rec.Queued,
		SucceededJSON: string(succeeded),
		FailedJSON:    string(failed),
		WarningsJSON:  string(warnings),
		CreatedAt:     rec.CreatedAt,
		UpdatedAt:     rec.UpdatedAt,
	}
}

func taskRecordFromStore(stored *db.TaskRecord) *taskRecord {
	if stored == nil {
		return nil
	}
	rec := &taskRecord{
		ID:        stored.ID,
		owner:     stored.OwnerID,
		Action:    stored.Action,
		Status:    stored.Status,
		Requested: stored.Requested,
		Queued:    stored.Queued,
		Succeeded: []string{},
		Failed:    []api.BulkVMItemError{},
		Warnings:  []api.BulkVMItemError{},
		CreatedAt: stored.CreatedAt,
		UpdatedAt: stored.UpdatedAt,
	}
	_ = json.Unmarshal([]byte(stored.SucceededJSON), &rec.Succeeded)
	_ = json.Unmarshal([]byte(stored.FailedJSON), &rec.Failed)
	_ = json.Unmarshal([]byte(stored.WarningsJSON), &rec.Warnings)
	if rec.Succeeded == nil {
		rec.Succeeded = []string{}
	}
	if rec.Failed == nil {
		rec.Failed = []api.BulkVMItemError{}
	}
	return rec
}

// saveTask persists a task record and refreshes the in-memory cache, evicting
// the oldest entries once the registry reaches maxTrackedTasks.
func (s *Server) saveTask(ctx context.Context, rec *taskRecord) error {
	if rec == nil || rec.ID == "" {
		return nil
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

	var persistErr error
	if store := s.operationStore(); store != nil {
		persistErr = store.SaveTaskRecord(ctx, rec.toStoreRecord())
	} else {
		persistErr = errTaskStoreUnavailable
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
	return persistErr
}

// recordQueuedTask stores the acknowledgement returned when a bulk operation is
// accepted for background execution.
func (s *Server) recordQueuedTask(ctx context.Context, ownerID string, resp api.BulkVMsResponse) error {
	return s.saveTask(ctx, &taskRecord{
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
func (s *Server) recordTaskSummary(ctx context.Context, ownerID string, resp api.BulkVMsResponse) {
	queued := resp.Queued
	owner := ownerID
	if prev, ok := s.getTask(ctx, resp.TaskID); ok {
		if queued == 0 {
			queued = prev.Queued
		}
		if owner == "" {
			owner = prev.owner
		}
	}
	if err := s.saveTask(ctx, &taskRecord{
		ID:        resp.TaskID,
		owner:     owner,
		Action:    resp.Action,
		Status:    taskStatusFor(resp.Succeeded, resp.Failed),
		Requested: resp.Requested,
		Queued:    queued,
		Succeeded: resp.Succeeded,
		Failed:    resp.Failed,
		Warnings:  resp.Warnings,
	}); err != nil {
		log.Printf("[tasks] persist summary for %s: %v", resp.TaskID, err)
	}
}

func (s *Server) getTask(ctx context.Context, id string) (*taskRecord, bool) {
	s.taskMu.Lock()
	rec, ok := s.tasks[id]
	s.taskMu.Unlock()
	if ok {
		return rec, true
	}
	store := s.operationStore()
	if store == nil {
		return nil, false
	}
	stored, err := store.GetTaskRecord(ctx, id)
	if err != nil || stored == nil {
		return nil, false
	}
	rec = taskRecordFromStore(stored)
	return rec, true
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
	rec, ok := s.getTask(r.Context(), id)
	if !ok {
		httpError(w, "task not found", http.StatusNotFound)
		return
	}
	// Task records describe another tenant's sandbox ids and errors, so hide
	// them from anyone but the submitter (and admins).
	if ownerID, isAdmin := s.currentOwnerID(r); !isAdmin && rec.owner != "" && rec.owner != ownerID && !s.canAccessOwner(r, rec.owner) {
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
