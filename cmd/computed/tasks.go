package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// deriveStage reconstructs a human-readable stage for task records loaded from
// the durable store, where the transient stage text is not persisted.
func (rec *taskRecord) deriveStage() string {
	done := len(rec.Succeeded) + len(rec.Failed)
	switch rec.Status {
	case "queued":
		return "Queued for execution"
	case "running":
		return fmt.Sprintf("In progress (%d/%d finished)", done, rec.Requested)
	case "completed":
		return fmt.Sprintf("Completed %d of %d", len(rec.Succeeded), rec.Requested)
	case "partial":
		return fmt.Sprintf("Completed %d of %d (%d failed)", len(rec.Succeeded), rec.Requested, len(rec.Failed))
	default:
		return "Failed"
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
	rec.Completed = len(rec.Succeeded) + len(rec.Failed)
	if !rec.CreatedAt.IsZero() && !rec.UpdatedAt.IsZero() {
		rec.ElapsedMs = rec.UpdatedAt.Sub(rec.CreatedAt).Milliseconds()
	}
	rec.Stage = rec.deriveStage()
	return rec
}

// saveTask persists a task record and refreshes the in-memory cache, evicting
// the oldest entries once the registry reaches maxTrackedTasks.
func (s *Server) saveTask(ctx context.Context, rec *taskRecord) error {
	if rec == nil || rec.ID == "" {
		return nil
	}
	now := time.Now().UTC()

	// Preserve the original creation time across progress updates before the
	// durable record is written.
	s.taskMu.Lock()
	if prev, exists := s.tasks[rec.ID]; exists && prev != nil && !prev.CreatedAt.IsZero() {
		rec.CreatedAt = prev.CreatedAt
	}
	s.taskMu.Unlock()

	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	if rec.ElapsedMs <= 0 {
		rec.ElapsedMs = now.Sub(rec.CreatedAt).Milliseconds()
	}
	if rec.Succeeded == nil {
		rec.Succeeded = []string{}
	}
	if rec.Failed == nil {
		rec.Failed = []api.BulkVMItemError{}
	}
	rec.Completed = len(rec.Succeeded) + len(rec.Failed)
	if rec.Stage == "" {
		rec.Stage = rec.deriveStage()
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
	stage := "Queued for execution"
	if resp.Queued > 0 {
		stage = fmt.Sprintf("Queued %d item(s) for %s", resp.Queued, resp.Action)
	}
	return s.saveTask(ctx, &taskRecord{
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
func (s *Server) recordTaskProgress(ctx context.Context, ownerID, taskID, action string, requested int, succeeded []string, failed []api.BulkVMItemError, warnings []api.BulkVMItemError, stage string) {
	queued := requested
	owner := ownerID
	var createdAt time.Time
	if prev, ok := s.getTask(ctx, taskID); ok && prev != nil {
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
	if err := s.saveTask(ctx, &taskRecord{
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
	}); err != nil {
		log.Printf("[tasks] persist progress for %s: %v", taskID, err)
	}
}

// recordTaskSummary stores the terminal state of a bulk operation, carrying
// over the queued count recorded when the request was accepted.
func (s *Server) recordTaskSummary(ctx context.Context, ownerID string, resp api.BulkVMsResponse) {
	queued := resp.Queued
	owner := ownerID
	var createdAt time.Time
	if prev, ok := s.getTask(ctx, resp.TaskID); ok && prev != nil {
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
	if err := s.saveTask(ctx, &taskRecord{
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
	}); err != nil {
		log.Printf("[tasks] persist summary for %s: %v", resp.TaskID, err)
	}
}

func (s *Server) getTask(ctx context.Context, id string) (*taskRecord, bool) {
	s.taskMu.Lock()
	rec, ok := s.tasks[id]
	s.taskMu.Unlock()
	if ok && rec != nil {
		cp := *rec
		cp.Succeeded = append([]string(nil), rec.Succeeded...)
		cp.Failed = append([]api.BulkVMItemError(nil), rec.Failed...)
		cp.Warnings = append([]api.BulkVMItemError(nil), rec.Warnings...)
		cp.Completed = len(cp.Succeeded) + len(cp.Failed)
		if cp.Status == "queued" || cp.Status == "running" {
			cp.ElapsedMs = time.Since(cp.CreatedAt).Milliseconds()
		}
		return &cp, true
	}
	store := s.operationStore()
	if store == nil {
		return nil, false
	}
	stored, err := store.GetTaskRecord(ctx, id)
	if err != nil || stored == nil {
		return nil, false
	}
	return taskRecordFromStore(stored), true
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
		if !isAdmin && rec.owner != "" && rec.owner != ownerID && !s.canAccessOwner(r, rec.owner) {
			continue
		}
		cp := *rec
		cp.Succeeded = append([]string(nil), rec.Succeeded...)
		cp.Failed = append([]api.BulkVMItemError(nil), rec.Failed...)
		cp.Warnings = append([]api.BulkVMItemError(nil), rec.Warnings...)
		cp.Completed = len(cp.Succeeded) + len(cp.Failed)
		if cp.Status == "queued" || cp.Status == "running" {
			cp.ElapsedMs = time.Since(cp.CreatedAt).Milliseconds()
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
