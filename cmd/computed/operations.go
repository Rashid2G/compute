package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/google/uuid"
)

const (
	// operationRetention bounds how long finished operation rows are kept. The
	// rows are small and give users a short history of completed work.
	operationRetention = 7 * 24 * time.Hour
	// operationListLimit is the default page size for GET /api/v1/operations.
	operationListLimit = 100
)

// operationSpec describes a unit of long-running work that must be persisted
// before it is dispatched to the backend.
type operationSpec struct {
	OwnerID    string
	Kind       string
	TargetID   string
	TargetName string
	TaskID     string
	Actor      string
	// Request is a redacted summary of the request, never raw secrets.
	Request interface{}
}

func newOperationID() string {
	return "op_" + strings.ReplaceAll(uuid.NewString()[:16], "-", "")
}

func (s *Server) operationStore() *db.Store {
	if s == nil || s.vmMgr == nil {
		return nil
	}
	return s.vmMgr.GetStore()
}

func marshalOperationPayload(v interface{}) string {
	if v == nil {
		return "{}"
	}
	if raw, ok := v.(string); ok {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func operationTargetLabel(op *db.Operation) string {
	if op == nil {
		return "cluster"
	}
	if strings.TrimSpace(op.TargetName) != "" {
		return op.TargetName
	}
	if strings.TrimSpace(op.TargetID) != "" {
		return op.TargetID
	}
	return "cluster"
}

// createOperation persists an operation row and its "requested" audit event
// before any backend work is dispatched. If either write fails the operation is
// not executed, so a queued request is never invisible to the user or missing
// from the audit trail.
func (s *Server) createOperation(ctx context.Context, spec operationSpec) (*db.Operation, error) {
	store := s.operationStore()
	if store == nil {
		return nil, errors.New("operation store unavailable")
	}
	if strings.TrimSpace(spec.OwnerID) == "" {
		spec.OwnerID = "default"
	}
	if strings.TrimSpace(spec.Actor) == "" {
		spec.Actor = "system"
	}
	if strings.TrimSpace(spec.Kind) == "" {
		spec.Kind = "operation"
	}
	now := time.Now().UTC()
	op := &db.Operation{
		ID:          newOperationID(),
		OwnerID:     spec.OwnerID,
		Kind:        spec.Kind,
		TargetID:    spec.TargetID,
		TargetName:  spec.TargetName,
		Status:      db.OperationStatusQueued,
		Progress:    0,
		RequestJSON: marshalOperationPayload(spec.Request),
		TaskID:      spec.TaskID,
		Actor:       spec.Actor,
		TraceID:     strings.ReplaceAll(uuid.NewString(), "-", ""),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.CreateOperation(ctx, op); err != nil {
		return nil, fmt.Errorf("persist operation: %w", err)
	}

	auditEvt := &db.AuditEvent{
		ID:         "evt_" + strings.ReplaceAll(uuid.NewString()[:12], "-", ""),
		Timestamp:  now,
		Event:      "operation.requested",
		Actor:      op.Actor,
		Target:     operationTargetLabel(op),
		Status:     "queued",
		DurationMs: 0,
		Payload: marshalOperationPayload(map[string]interface{}{
			"operation_id": op.ID,
			"kind":         op.Kind,
			"target_id":    op.TargetID,
			"target_name":  op.TargetName,
			"task_id":      op.TaskID,
			"request":      json.RawMessage(op.RequestJSON),
		}),
	}
	if err := s.persistAuditEvent(auditEvt); err != nil {
		// The audit trail is a hard prerequisite: do not execute unaudited work.
		// Mark the row failed so it does not linger as a phantom queued request.
		op.Status = db.OperationStatusFailed
		op.ErrorMsg = "audit persistence failed: " + err.Error()
		finished := time.Now().UTC()
		op.FinishedAt = &finished
		op.UpdatedAt = finished
		_ = store.SaveOperation(ctx, op)
		return nil, fmt.Errorf("record operation audit event: %w", err)
	}
	s.publishAuditEvent(auditEvt)
	return op, nil
}

// startOperation marks a persisted operation as running.
func (s *Server) startOperation(ctx context.Context, op *db.Operation) {
	if op == nil {
		return
	}
	now := time.Now().UTC()
	op.Status = db.OperationStatusRunning
	op.StartedAt = &now
	op.UpdatedAt = now
	if op.Progress < 5 {
		op.Progress = 5
	}
	if store := s.operationStore(); store != nil {
		_ = store.SaveOperation(ctx, op)
	}
}

// finishOperation records the terminal state of an operation and audits it.
func (s *Server) finishOperation(ctx context.Context, op *db.Operation, result interface{}, err error) {
	if op == nil {
		return
	}
	now := time.Now().UTC()
	op.FinishedAt = &now
	op.UpdatedAt = now
	op.Progress = 100

	switch {
	case err == nil:
		op.Status = db.OperationStatusSucceeded
		if result != nil {
			op.ResultJSON = marshalOperationPayload(result)
		}
	default:
		var warn *vmStartWarning
		if errors.As(err, &warn) {
			// The record exists; only the boot did not finish. Treat the
			// operation as successful with a warning so the UI does not report
			// a failed creation for a sandbox that can still be started.
			op.Status = db.OperationStatusSucceeded
			op.ErrorMsg = warn.reason
		} else {
			op.Status = db.OperationStatusFailed
			op.ErrorMsg = err.Error()
		}
	}

	if resultVM, ok := result.(*db.VM); ok && resultVM != nil {
		if resultVM.InternalID != "" {
			op.TargetID = resultVM.InternalID
		} else if resultVM.ID != "" {
			op.TargetID = resultVM.ID
		}
		if resultVM.Name != "" {
			op.TargetName = resultVM.Name
		}
	}

	if store := s.operationStore(); store != nil {
		_ = store.SaveOperation(ctx, op)
	}

	status := "delivered"
	if op.Status == db.OperationStatusFailed {
		status = "failed"
	}
	payload := map[string]interface{}{
		"operation_id": op.ID,
		"kind":         op.Kind,
		"target_id":    op.TargetID,
		"target_name":  op.TargetName,
		"task_id":      op.TaskID,
	}
	if op.ErrorMsg != "" {
		payload["error"] = op.ErrorMsg
	}
	s.recordEvent("operation."+op.Status, op.Actor, operationTargetLabel(op), status, 0, payload)
}

// summarizeCreateRequest returns a redacted view of a sandbox creation request.
// SSH passwords and public keys are intentionally omitted from the audit trail.
func summarizeCreateRequest(req vm.CreateRequest) map[string]interface{} {
	summary := map[string]interface{}{
		"name":        req.Name,
		"vcpus":       req.VCPUs,
		"memory_mb":   req.MemoryMB,
		"disk_mb":     req.DiskMB,
		"auto_start":  req.AutoStart,
		"worker_id":   req.WorkerID,
		"pool_id":     req.PoolID,
		"description": req.Description,
	}
	if req.Image != "" {
		summary["image"] = req.Image
	}
	if req.Template != "" {
		summary["template"] = req.Template
	}
	if req.CloneFrom != "" {
		summary["clone_from"] = req.CloneFrom
	}
	if req.NetworkIsolation != "" {
		summary["network_isolation"] = req.NetworkIsolation
	}
	return summary
}

// bulkOperationSpec describes one item in a bulk request that needs its own
// durable operation row.
type bulkOperationSpec struct {
	TargetID   string
	TargetName string
	Request    interface{}
	// Skip marks an item that failed validation before execution. It gets no
	// operation row because no work will be dispatched for it.
	Skip bool
}

// prepareBulkOperations persists one operation row per valid bulk item before
// any item is dispatched. The returned slice is aligned with specs. On failure
// it marks the already-created rows failed so no phantom queued work remains.
func (s *Server) prepareBulkOperations(ctx context.Context, ownerID, kind, taskID, actor string, specs []bulkOperationSpec) ([]*db.Operation, error) {
	ops := make([]*db.Operation, len(specs))
	created := make([]*db.Operation, 0, len(specs))
	for i := range specs {
		spec := specs[i]
		if spec.Skip {
			continue
		}
		op, err := s.createOperation(ctx, operationSpec{
			OwnerID:    ownerID,
			Kind:       kind,
			TargetID:   spec.TargetID,
			TargetName: spec.TargetName,
			TaskID:     taskID,
			Actor:      actor,
			Request:    spec.Request,
		})
		if err != nil {
			for _, prev := range created {
				s.finishOperation(ctx, prev, nil, err)
			}
			return nil, err
		}
		ops[i] = op
		created = append(created, op)
	}
	return ops, nil
}

func operationIDs(ops []*db.Operation) []string {
	ids := make([]string, 0, len(ops))
	for _, op := range ops {
		if op != nil {
			ids = append(ids, op.ID)
		}
	}
	return ids
}

// handleOperations serves GET /api/v1/operations and GET /api/v1/operations/{id}.
// Operations are the durable view of work that has been accepted but has not
// necessarily finished yet.
func (s *Server) handleOperations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	store := s.operationStore()
	if store == nil {
		httpError(w, "operation store unavailable", http.StatusServiceUnavailable)
		return
	}
	ownerID, isAdmin := s.currentOwnerID(r)
	ctx := r.Context()

	path := strings.TrimPrefix(r.URL.Path, "/api/v1/operations")
	path = strings.Trim(path, "/")
	if path != "" {
		op, err := store.GetOperation(ctx, path)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if op == nil || (!isAdmin && op.OwnerID != ownerID && !s.canAccessOwner(r, op.OwnerID)) {
			httpError(w, "operation not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(op)
		return
	}

	filter := db.OperationFilter{AllOwners: isAdmin, Limit: operationListLimit}
	if !isAdmin {
		filter.OwnerIDs = s.ownerIDsFor(r)
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("status")); raw != "" {
		for _, status := range strings.Split(raw, ",") {
			if status = strings.ToLower(strings.TrimSpace(status)); status != "" {
				filter.Statuses = append(filter.Statuses, status)
			}
		}
	}
	filter.TaskID = strings.TrimSpace(r.URL.Query().Get("task_id"))
	filter.Kind = strings.TrimSpace(r.URL.Query().Get("kind"))
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			filter.Limit = n
		}
	}

	ops, err := store.ListOperations(ctx, filter)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ops == nil {
		ops = []*db.Operation{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ops)
}
