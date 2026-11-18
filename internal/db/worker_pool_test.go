package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkerPoolLookupAndDeleteByName(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "compute-db-pool-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	pool := &WorkerPool{ID: "pool_test1", Name: "gpu-pool", LabelsJSON: "{}", PolicyJSON: "{}"}
	if err := store.SaveWorkerPool(ctx, pool); err != nil {
		t.Fatalf("SaveWorkerPool: %v", err)
	}

	byName, err := store.GetWorkerPool(ctx, "gpu-pool")
	if err != nil {
		t.Fatalf("GetWorkerPool by name: %v", err)
	}
	if byName == nil || byName.ID != pool.ID {
		t.Fatalf("GetWorkerPool by name = %+v, want ID %q", byName, pool.ID)
	}

	byID, err := store.GetWorkerPool(ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetWorkerPool by id: %v", err)
	}
	if byID == nil || byID.Name != pool.Name {
		t.Fatalf("GetWorkerPool by id = %+v, want name %q", byID, pool.Name)
	}

	if err := store.DeleteWorkerPool(ctx, "gpu-pool"); err != nil {
		t.Fatalf("DeleteWorkerPool by name: %v", err)
	}
	deleted, err := store.GetWorkerPool(ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetWorkerPool after delete: %v", err)
	}
	if deleted != nil {
		t.Fatalf("pool still present after delete: %+v", deleted)
	}
}
