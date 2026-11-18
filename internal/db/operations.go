package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Operation status values. An operation is always persisted before the work it
// describes is dispatched, so clients can observe queued work that has not
// started yet.
const (
	OperationStatusQueued    = "queued"
	OperationStatusRunning   = "running"
	OperationStatusSucceeded = "succeeded"
	OperationStatusFailed    = "failed"
	OperationStatusCanceled  = "canceled"
)

// Operation is the durable record of a long-running unit of work (sandbox
// creation, image build, volume snapshot, ...). The row exists before execution
// starts and is updated as the work progresses, which makes queued work visible
// to the owning user instead of living only in memory.
type Operation struct {
	ID          string     `json:"id"`
	OwnerID     string     `json:"owner_id"`
	Kind        string     `json:"kind"`
	TargetID    string     `json:"target_id,omitempty"`
	TargetName  string     `json:"target_name,omitempty"`
	Status      string     `json:"status"`
	Progress    int        `json:"progress"`
	RequestJSON string     `json:"request_json,omitempty"`
	ResultJSON  string     `json:"result_json,omitempty"`
	ErrorMsg    string     `json:"error_msg,omitempty"`
	TaskID      string     `json:"task_id,omitempty"`
	Actor       string     `json:"actor,omitempty"`
	TraceID     string     `json:"trace_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

// TaskRecord is the durable summary of a bulk request. It replaces the
// in-memory-only task registry so a queued bulk request remains resolvable
// across daemon restarts.
type TaskRecord struct {
	ID            string    `json:"id"`
	OwnerID       string    `json:"owner_id"`
	Action        string    `json:"action"`
	Status        string    `json:"status"`
	Requested     int       `json:"requested"`
	Queued        int       `json:"queued"`
	SucceededJSON string    `json:"succeeded_json"`
	FailedJSON    string    `json:"failed_json"`
	WarningsJSON  string    `json:"warnings_json"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// OperationFilter narrows a ListOperations query. AllOwners bypasses owner
// scoping for administrators; OwnerIDs scopes results to the caller's tenants.
type OperationFilter struct {
	AllOwners bool
	OwnerIDs  []string
	Statuses  []string
	TaskID    string
	Kind      string
	Limit     int
}

func normalizeOperation(op *Operation) {
	now := time.Now().UTC()
	if op.OwnerID == "" {
		op.OwnerID = "default"
	}
	if op.Status == "" {
		op.Status = OperationStatusQueued
	}
	if op.CreatedAt.IsZero() {
		op.CreatedAt = now
	}
	if op.UpdatedAt.IsZero() {
		op.UpdatedAt = now
	}
	if op.RequestJSON == "" {
		op.RequestJSON = "{}"
	}
}

// CreateOperation inserts a new operation row. It fails if the ID already
// exists, which keeps the "persist once, then execute" invariant honest.
func (s *Store) CreateOperation(ctx context.Context, op *Operation) error {
	if op == nil || strings.TrimSpace(op.ID) == "" {
		return fmt.Errorf("operation id is required")
	}
	normalizeOperation(op)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO operations (
		id, owner_id, kind, target_id, target_name, status, progress,
		request_json, result_json, error_msg, task_id, actor, trace_id,
		created_at, started_at, updated_at, finished_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		op.ID, op.OwnerID, op.Kind, op.TargetID, op.TargetName, op.Status, op.Progress,
		op.RequestJSON, op.ResultJSON, op.ErrorMsg, op.TaskID, op.Actor, op.TraceID,
		op.CreatedAt, op.StartedAt, op.UpdatedAt, op.FinishedAt,
	)
	return err
}

// SaveOperation inserts or replaces an operation row. Callers own the record
// for the duration of one unit of work, so last-write-wins is safe here.
func (s *Store) SaveOperation(ctx context.Context, op *Operation) error {
	if op == nil || strings.TrimSpace(op.ID) == "" {
		return fmt.Errorf("operation id is required")
	}
	normalizeOperation(op)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO operations (
		id, owner_id, kind, target_id, target_name, status, progress,
		request_json, result_json, error_msg, task_id, actor, trace_id,
		created_at, started_at, updated_at, finished_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		owner_id=excluded.owner_id,
		kind=excluded.kind,
		target_id=excluded.target_id,
		target_name=excluded.target_name,
		status=excluded.status,
		progress=excluded.progress,
		request_json=excluded.request_json,
		result_json=excluded.result_json,
		error_msg=excluded.error_msg,
		task_id=excluded.task_id,
		actor=excluded.actor,
		trace_id=excluded.trace_id,
		started_at=excluded.started_at,
		updated_at=excluded.updated_at,
		finished_at=excluded.finished_at`,
		op.ID, op.OwnerID, op.Kind, op.TargetID, op.TargetName, op.Status, op.Progress,
		op.RequestJSON, op.ResultJSON, op.ErrorMsg, op.TaskID, op.Actor, op.TraceID,
		op.CreatedAt, op.StartedAt, op.UpdatedAt, op.FinishedAt,
	)
	return err
}

func scanOperation(scanner interface {
	Scan(dest ...interface{}) error
}) (*Operation, error) {
	op := new(Operation)
	var started, finished sql.NullTime
	if err := scanner.Scan(
		&op.ID, &op.OwnerID, &op.Kind, &op.TargetID, &op.TargetName,
		&op.Status, &op.Progress, &op.RequestJSON, &op.ResultJSON, &op.ErrorMsg,
		&op.TaskID, &op.Actor, &op.TraceID, &op.CreatedAt, &started,
		&op.UpdatedAt, &finished,
	); err != nil {
		return nil, err
	}
	if started.Valid {
		t := started.Time
		op.StartedAt = &t
	}
	if finished.Valid {
		t := finished.Time
		op.FinishedAt = &t
	}
	return op, nil
}

const operationColumns = `id, owner_id, kind, target_id, target_name, status, progress,
	request_json, result_json, error_msg, task_id, actor, trace_id,
	created_at, started_at, updated_at, finished_at`

// GetOperation loads a single operation by ID.
func (s *Store) GetOperation(ctx context.Context, id string) (*Operation, error) {
	if strings.TrimSpace(id) == "" {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	op, err := scanOperation(s.db.QueryRowContext(ctx,
		`SELECT `+operationColumns+` FROM operations WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return op, err
}

// ListOperations returns operations matching filter, newest first.
func (s *Store) ListOperations(ctx context.Context, filter OperationFilter) ([]*Operation, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	var where []string
	var args []interface{}
	if !filter.AllOwners {
		if len(filter.OwnerIDs) == 0 {
			return []*Operation{}, nil
		}
		placeholders := make([]string, 0, len(filter.OwnerIDs))
		for _, owner := range filter.OwnerIDs {
			if strings.TrimSpace(owner) == "" {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, owner)
		}
		if len(placeholders) == 0 {
			return []*Operation{}, nil
		}
		where = append(where, "owner_id IN ("+strings.Join(placeholders, ",")+")")
	}
	if len(filter.Statuses) > 0 {
		placeholders := make([]string, 0, len(filter.Statuses))
		for _, status := range filter.Statuses {
			status = strings.ToLower(strings.TrimSpace(status))
			if status == "" {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, status)
		}
		if len(placeholders) > 0 {
			where = append(where, "status IN ("+strings.Join(placeholders, ",")+")")
		}
	}
	if taskID := strings.TrimSpace(filter.TaskID); taskID != "" {
		where = append(where, "task_id = ?")
		args = append(args, taskID)
	}
	if kind := strings.TrimSpace(filter.Kind); kind != "" {
		where = append(where, "kind = ?")
		args = append(args, kind)
	}

	query := `SELECT ` + operationColumns + ` FROM operations`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Operation, 0, limit)
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// FailInterruptedOperations marks operations that were queued or running when
// the daemon stopped as failed. The in-memory execution queue cannot be
// replayed, so leaving rows "queued" would misrepresent pending work.
func (s *Store) FailInterruptedOperations(ctx context.Context, reason string) (int64, error) {
	if strings.TrimSpace(reason) == "" {
		reason = "interrupted by daemon restart"
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `UPDATE operations
		SET status = ?, progress = 100, error_msg = ?, updated_at = ?, finished_at = ?
		WHERE status IN (?, ?)`,
		OperationStatusFailed, reason, now, now,
		OperationStatusQueued, OperationStatusRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// FailInterruptedTaskRecords marks bulk task summaries that never reached a
// terminal state as failed after a restart.
func (s *Store) FailInterruptedTaskRecords(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `UPDATE task_records SET status = ?, updated_at = ? WHERE status = ?`,
		"failed", time.Now().UTC(), "queued")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneOperations deletes terminal operations older than the retention cutoff.
func (s *Store) PruneOperations(ctx context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM operations
		WHERE status IN (?, ?, ?) AND updated_at < ?`,
		OperationStatusSucceeded, OperationStatusFailed, OperationStatusCanceled, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SaveTaskRecord inserts or replaces the durable summary of a bulk request.
func (s *Store) SaveTaskRecord(ctx context.Context, rec *TaskRecord) error {
	if rec == nil || strings.TrimSpace(rec.ID) == "" {
		return fmt.Errorf("task id is required")
	}
	now := time.Now().UTC()
	if rec.OwnerID == "" {
		rec.OwnerID = "default"
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	if rec.SucceededJSON == "" {
		rec.SucceededJSON = "[]"
	}
	if rec.FailedJSON == "" {
		rec.FailedJSON = "[]"
	}
	if rec.WarningsJSON == "" {
		rec.WarningsJSON = "[]"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO task_records (
		id, owner_id, action, status, requested, queued,
		succeeded_json, failed_json, warnings_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		owner_id=excluded.owner_id,
		action=excluded.action,
		status=excluded.status,
		requested=excluded.requested,
		queued=excluded.queued,
		succeeded_json=excluded.succeeded_json,
		failed_json=excluded.failed_json,
		warnings_json=excluded.warnings_json,
		updated_at=excluded.updated_at`,
		rec.ID, rec.OwnerID, rec.Action, rec.Status, rec.Requested, rec.Queued,
		rec.SucceededJSON, rec.FailedJSON, rec.WarningsJSON, rec.CreatedAt, rec.UpdatedAt,
	)
	return err
}

// GetTaskRecord loads a durable bulk task summary.
func (s *Store) GetTaskRecord(ctx context.Context, id string) (*TaskRecord, error) {
	if strings.TrimSpace(id) == "" {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec := new(TaskRecord)
	err := s.db.QueryRowContext(ctx, `SELECT id, owner_id, action, status, requested, queued,
		succeeded_json, failed_json, warnings_json, created_at, updated_at
		FROM task_records WHERE id = ?`, id).Scan(
		&rec.ID, &rec.OwnerID, &rec.Action, &rec.Status, &rec.Requested, &rec.Queued,
		&rec.SucceededJSON, &rec.FailedJSON, &rec.WarningsJSON, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// PruneTaskRecords deletes finished bulk task summaries older than the cutoff.
func (s *Store) PruneTaskRecords(ctx context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM task_records WHERE updated_at < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
