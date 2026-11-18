package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newOperationsTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "operations.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestOperationLifecycle(t *testing.T) {
	store := newOperationsTestStore(t)
	ctx := context.Background()

	op := &Operation{
		ID:          "op_test1",
		OwnerID:     "usr_a",
		Kind:        "sandbox.create",
		TargetName:  "sbx-one",
		Status:      OperationStatusQueued,
		RequestJSON: `{"name":"sbx-one"}`,
		TaskID:      "task_test",
		Actor:       "tester",
	}
	if err := store.CreateOperation(ctx, op); err != nil {
		t.Fatalf("create operation: %v", err)
	}
	if err := store.CreateOperation(ctx, op); err == nil {
		t.Fatal("expected duplicate create to fail")
	}

	got, err := store.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected operation to be persisted")
	}
	if got.Status != OperationStatusQueued || got.OwnerID != "usr_a" || got.TaskID != "task_test" {
		t.Fatalf("unexpected operation: %+v", got)
	}
	if got.StartedAt != nil || got.FinishedAt != nil {
		t.Fatalf("queued operation should have no timestamps: %+v", got)
	}

	started := time.Now().UTC()
	got.Status = OperationStatusRunning
	got.Progress = 40
	got.StartedAt = &started
	if err := store.SaveOperation(ctx, got); err != nil {
		t.Fatalf("save running operation: %v", err)
	}
	refreshed, err := store.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Status != OperationStatusRunning || refreshed.Progress != 40 || refreshed.StartedAt == nil {
		t.Fatalf("unexpected running operation: %+v", refreshed)
	}
}

func TestListOperationsScopingAndFilters(t *testing.T) {
	store := newOperationsTestStore(t)
	ctx := context.Background()

	for _, op := range []*Operation{
		{ID: "op_a1", OwnerID: "usr_a", Kind: "sandbox.create", Status: OperationStatusQueued, TaskID: "task_1"},
		{ID: "op_a2", OwnerID: "usr_a", Kind: "sandbox.clone", Status: OperationStatusSucceeded, TaskID: "task_1"},
		{ID: "op_b1", OwnerID: "usr_b", Kind: "sandbox.create", Status: OperationStatusRunning, TaskID: "task_2"},
	} {
		if err := store.CreateOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
	}

	scoped, err := store.ListOperations(ctx, OperationFilter{OwnerIDs: []string{"usr_a"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 2 {
		t.Fatalf("expected 2 operations for usr_a, got %d", len(scoped))
	}
	for _, op := range scoped {
		if op.OwnerID != "usr_a" {
			t.Fatalf("owner leak: %+v", op)
		}
	}

	if got, _ := store.ListOperations(ctx, OperationFilter{OwnerIDs: []string{}}); len(got) != 0 {
		t.Fatalf("empty owner scope must return no rows, got %d", len(got))
	}

	byTask, err := store.ListOperations(ctx, OperationFilter{AllOwners: true, TaskID: "task_1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byTask) != 2 {
		t.Fatalf("expected 2 operations for task_1, got %d", len(byTask))
	}

	active, err := store.ListOperations(ctx, OperationFilter{AllOwners: true, Statuses: []string{OperationStatusQueued, OperationStatusRunning}})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("expected 2 active operations, got %d", len(active))
	}
}

func TestPruneOperationsKeepsActiveRows(t *testing.T) {
	store := newOperationsTestStore(t)
	ctx := context.Background()

	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	for _, op := range []*Operation{
		{ID: "op_done", OwnerID: "usr_a", Kind: "sandbox.create", Status: OperationStatusSucceeded, UpdatedAt: old},
		{ID: "op_live", OwnerID: "usr_a", Kind: "sandbox.create", Status: OperationStatusQueued, UpdatedAt: old},
	} {
		op.CreatedAt = old
		if err := store.CreateOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
	}
	// CreateOperation normalizes UpdatedAt to now; force the old value back so
	// the retention cutoff applies.
	done, _ := store.GetOperation(ctx, "op_done")
	done.UpdatedAt = old
	if err := store.SaveOperation(ctx, done); err != nil {
		t.Fatal(err)
	}

	n, err := store.PruneOperations(ctx, time.Now().UTC().Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected to prune 1 terminal operation, got %d", n)
	}
	if got, _ := store.GetOperation(ctx, "op_live"); got == nil {
		t.Fatal("active operation must not be pruned")
	}
}

func TestTaskRecordPersistence(t *testing.T) {
	store := newOperationsTestStore(t)
	ctx := context.Background()

	rec := &TaskRecord{
		ID:            "task_abc",
		OwnerID:       "usr_a",
		Action:        "create",
		Status:        "queued",
		Requested:     3,
		Queued:        2,
		SucceededJSON: `[]`,
		FailedJSON:    `[{"id":"bad","error":"invalid"}]`,
		WarningsJSON:  `[]`,
	}
	if err := store.SaveTaskRecord(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTaskRecord(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Status != "queued" || got.Requested != 3 || got.Queued != 2 {
		t.Fatalf("unexpected task record: %+v", got)
	}

	rec.Status = "completed"
	rec.Queued = 0
	rec.SucceededJSON = `["vm_1","vm_2"]`
	if err := store.SaveTaskRecord(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetTaskRecord(ctx, rec.ID)
	if got.Status != "completed" || got.SucceededJSON != `["vm_1","vm_2"]` {
		t.Fatalf("task update not persisted: %+v", got)
	}
}
