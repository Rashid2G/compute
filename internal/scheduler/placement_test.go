package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/pkg/api"
)

func TestSelectNodeForSandbox_Strategies(t *testing.T) {
	ctx := context.Background()
	nodes := []*db.WorkerNode{
		{
			ID:             "wrk-light",
			Name:           "light",
			PoolID:         "default",
			Status:         "ready",
			AllocatedVCPUs: 16,
			AllocatedMemMB: 16384,
			UsedVCPUs:      2,
			UsedMemMB:      2048,
			RunningVMs:     1,
		},
		{
			ID:             "wrk-busy",
			Name:           "busy",
			PoolID:         "default",
			Status:         "ready",
			AllocatedVCPUs: 16,
			AllocatedMemMB: 16384,
			UsedVCPUs:      10,
			UsedMemMB:      10240,
			RunningVMs:     4,
		},
		{
			ID:             "wrk-empty",
			Name:           "empty",
			PoolID:         "default",
			Status:         "ready",
			AllocatedVCPUs: 16,
			AllocatedMemMB: 16384,
			UsedVCPUs:      0,
			UsedMemMB:      0,
			RunningVMs:     0,
		},
	}

	req := SandboxPlacementRequest{VCPUs: 2, MemoryMB: 1024, DiskMB: 1024}

	t.Run("least_loaded", func(t *testing.T) {
		p := db.DefaultSchedulingPolicy()
		p.Strategy = "least_loaded"
		chosen, err := SelectNodeForSandbox(ctx, nodes, nil, req, p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if chosen.ID != "wrk-empty" {
			t.Fatalf("expected wrk-empty for least_loaded, got %s", chosen.ID)
		}
	})

	t.Run("bin_pack", func(t *testing.T) {
		p := db.DefaultSchedulingPolicy()
		p.Strategy = "bin_pack"
		chosen, err := SelectNodeForSandbox(ctx, nodes, nil, req, p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if chosen.ID != "wrk-busy" {
			t.Fatalf("expected wrk-busy for bin_pack, got %s", chosen.ID)
		}
	})

	t.Run("spread", func(t *testing.T) {
		p := db.DefaultSchedulingPolicy()
		p.Strategy = "spread"
		spreadNodes := []*db.WorkerNode{
			{ID: "wrk-a", PoolID: "default", Status: "ready", AllocatedVCPUs: 8, AllocatedMemMB: 8192, RunningVMs: 3, UsedMemMB: 1024},
			{ID: "wrk-b", PoolID: "default", Status: "ready", AllocatedVCPUs: 8, AllocatedMemMB: 8192, RunningVMs: 1, UsedMemMB: 4096},
			{ID: "wrk-c", PoolID: "default", Status: "ready", AllocatedVCPUs: 8, AllocatedMemMB: 8192, RunningVMs: 1, UsedMemMB: 512},
		}
		chosen, err := SelectNodeForSandbox(ctx, spreadNodes, nil, req, p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if chosen.ID != "wrk-c" {
			t.Fatalf("expected wrk-c (lowest RunningVMs, lowest UsedMemMB) for spread, got %s", chosen.ID)
		}
	})

	t.Run("round_robin", func(t *testing.T) {
		p := db.DefaultSchedulingPolicy()
		p.Strategy = "round_robin"
		rrNodes := []*db.WorkerNode{
			{ID: "wrk-1", PoolID: "default", Status: "ready", AllocatedVCPUs: 8, AllocatedMemMB: 8192},
			{ID: "wrk-2", PoolID: "default", Status: "ready", AllocatedVCPUs: 8, AllocatedMemMB: 8192},
			{ID: "wrk-3", PoolID: "default", Status: "ready", AllocatedVCPUs: 8, AllocatedMemMB: 8192},
		}
		seen := make(map[string]bool)
		for i := 0; i < 3; i++ {
			chosen, err := SelectNodeForSandbox(ctx, rrNodes, nil, req, p)
			if err != nil {
				t.Fatalf("round_robin %d error: %v", i, err)
			}
			seen[chosen.ID] = true
		}
		if len(seen) != 3 {
			t.Fatalf("expected round_robin to cycle across all 3 nodes, saw %v", seen)
		}
	})
}

func TestSelectNodeForSandbox_ConstraintsAndExclusion(t *testing.T) {
	ctx := context.Background()
	p := db.DefaultSchedulingPolicy()

	local := &db.WorkerNode{
		ID:             "nod_host_local",
		Name:           "local",
		PoolID:         "default",
		Status:         "cordoned",
		AllocatedVCPUs: 8,
		AllocatedMemMB: 8192,
	}
	workers := []*db.WorkerNode{
		{
			ID:             "wrk-drain",
			Name:           "drain-node",
			PoolID:         "gpu",
			Status:         "draining",
			AllocatedVCPUs: 16,
			AllocatedMemMB: 32768,
		},
		{
			ID:             "wrk-off",
			Name:           "off-node",
			PoolID:         "gpu",
			Status:         "offline",
			AllocatedVCPUs: 16,
			AllocatedMemMB: 32768,
		},
		{
			ID:             "wrk-gpu",
			Name:           "gpu-node",
			PoolID:         "gpu",
			Status:         "ready",
			AllocatedVCPUs: 8,
			AllocatedMemMB: 16384,
			LabelsJSON:     `{"tier":"gpu","zone":"eu-1"}`,
		},
	}

	req := SandboxPlacementRequest{VCPUs: 1, MemoryMB: 512, DiskMB: 1024}

	// Automatic placement skips cordoned, draining, and offline nodes
	chosen, err := SelectNodeForSandbox(ctx, workers, local, req, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chosen.ID != "wrk-gpu" {
		t.Fatalf("expected wrk-gpu, got %s", chosen.ID)
	}

	// Explicit pool constraint succeeds for gpu, fails for missing pool
	reqPool := req
	reqPool.PoolID = "gpu"
	chosen, err = SelectNodeForSandbox(ctx, workers, local, reqPool, p)
	if err != nil || chosen.ID != "wrk-gpu" {
		t.Fatalf("expected pool=gpu -> wrk-gpu, got chosen=%+v err=%v", chosen, err)
	}

	reqMissingPool := req
	reqMissingPool.PoolID = "nonexistent"
	if _, err := SelectNodeForSandbox(ctx, workers, local, reqMissingPool, p); err == nil {
		t.Fatalf("expected error for missing pool")
	}

	// Explicit WorkerID rejects cordoned/draining/offline/missing
	for _, badID := range []string{"nod_host_local", "wrk-drain", "wrk-off", "missing"} {
		badReq := req
		badReq.WorkerID = badID
		if _, err := SelectNodeForSandbox(ctx, workers, local, badReq, p); err == nil {
			t.Fatalf("expected error selecting unavailable worker %s", badID)
		}
	}

	// Explicit WorkerID + mismatched PoolID fails
	mismatchReq := req
	mismatchReq.WorkerID = "wrk-gpu"
	mismatchReq.PoolID = "default"
	if _, err := SelectNodeForSandbox(ctx, workers, local, mismatchReq, p); err == nil {
		t.Fatalf("expected error when worker pool mismatches requested pool")
	}

	// Label filtering
	lblReq := req
	lblReq.Labels = map[string]string{"tier": "gpu"}
	chosen, err = SelectNodeForSandbox(ctx, workers, local, lblReq, p)
	if err != nil || chosen.ID != "wrk-gpu" {
		t.Fatalf("expected label match wrk-gpu, got %+v err=%v", chosen, err)
	}
	lblReq.Labels = map[string]string{"tier": "tpu"}
	if _, err := SelectNodeForSandbox(ctx, workers, local, lblReq, p); err == nil {
		t.Fatalf("expected error when no node matches labels")
	}
}

func TestSelectNodeForSandbox_ResourceLimits(t *testing.T) {
	ctx := context.Background()
	p := db.DefaultSchedulingPolicy()
	p.CPUHardOvercommitRatio = 2.0
	p.HostReservedDiskMB = 2048

	assertLimitErr := func(t *testing.T, err error, wantResource string) {
		t.Helper()
		var rle *api.ResourceLimitError
		if !errors.As(err, &rle) {
			t.Fatalf("expected *api.ResourceLimitError (%s), got %T: %v", wantResource, err, err)
		}
		if rle.Resource != wantResource || rle.LimitType != "hard" {
			t.Fatalf("unexpected ResourceLimitError: %+v", rle)
		}
	}

	t.Run("disk_allocated_hard_limit", func(t *testing.T) {
		n := &db.WorkerNode{
			ID:              "w-disk",
			Status:          "ready",
			AllocatedVCPUs:  8,
			AllocatedMemMB:  8192,
			AllocatedDiskMB: 10000,
			UsedDiskMB:      8000,
			AvailableDiskMB: 50000,
		}
		_, err := SelectNodeForSandbox(ctx, []*db.WorkerNode{n}, nil, SandboxPlacementRequest{
			VCPUs: 1, MemoryMB: 512, DiskMB: 3000,
		}, p)
		assertLimitErr(t, err, "disk")
	})

	t.Run("disk_host_reserve_limit", func(t *testing.T) {
		n := &db.WorkerNode{
			ID:              "w-reserve",
			Status:          "ready",
			AllocatedVCPUs:  8,
			AllocatedMemMB:  8192,
			AllocatedDiskMB: 100000,
			AvailableDiskMB: 1024, // below HostReservedDiskMB=2048
		}
		_, err := SelectNodeForSandbox(ctx, []*db.WorkerNode{n}, nil, SandboxPlacementRequest{
			VCPUs: 1, MemoryMB: 512, DiskMB: 1024,
		}, p)
		assertLimitErr(t, err, "disk")
	})

	t.Run("disk_allocated_zero_uses_total_and_physical_and_available", func(t *testing.T) {
		// 1. AllocatedDiskMB=0 falls back to TotalDiskMB
		nTotal := &db.WorkerNode{
			ID:              "w-disk-total",
			Status:          "ready",
			AllocatedVCPUs:  8,
			AllocatedMemMB:  8192,
			AllocatedDiskMB: 0,
			TotalDiskMB:     10000,
			UsedDiskMB:      8500,
		}
		_, err := SelectNodeForSandbox(ctx, []*db.WorkerNode{nTotal}, nil, SandboxPlacementRequest{
			VCPUs: 1, MemoryMB: 512, DiskMB: 2048,
		}, p)
		assertLimitErr(t, err, "disk")

		// 2. AllocatedDiskMB=0 and TotalDiskMB=0 falls back to PhysicalDiskMB - HostReservedDiskMB
		nPhys := &db.WorkerNode{
			ID:              "w-disk-phys",
			Status:          "ready",
			AllocatedVCPUs:  8,
			AllocatedMemMB:  8192,
			AllocatedDiskMB: 0,
			TotalDiskMB:     0,
			PhysicalDiskMB:  8192, // effective = 8192 - 2048 = 6144
			UsedDiskMB:      5000,
		}
		_, err = SelectNodeForSandbox(ctx, []*db.WorkerNode{nPhys}, nil, SandboxPlacementRequest{
			VCPUs: 1, MemoryMB: 512, DiskMB: 2048,
		}, p)
		assertLimitErr(t, err, "disk")

		// 3. AllocatedDiskMB=0, TotalDiskMB=0, PhysicalDiskMB=0 still enforces AvailableDiskMB
		nAvail := &db.WorkerNode{
			ID:              "w-disk-avail",
			Status:          "ready",
			AllocatedVCPUs:  8,
			AllocatedMemMB:  8192,
			AllocatedDiskMB: 0,
			AvailableDiskMB: 3000,
		}
		_, err = SelectNodeForSandbox(ctx, []*db.WorkerNode{nAvail}, nil, SandboxPlacementRequest{
			VCPUs: 1, MemoryMB: 512, DiskMB: 4096,
		}, p)
		assertLimitErr(t, err, "disk")
	})

	t.Run("memory_hard_limit", func(t *testing.T) {
		n := &db.WorkerNode{
			ID:             "w-mem",
			Status:         "ready",
			AllocatedVCPUs: 8,
			AllocatedMemMB: 4096,
			UsedMemMB:      3500,
		}
		_, err := SelectNodeForSandbox(ctx, []*db.WorkerNode{n}, nil, SandboxPlacementRequest{
			VCPUs: 1, MemoryMB: 1024, DiskMB: 1024,
		}, p)
		assertLimitErr(t, err, "memory")
	})

	t.Run("cpu_overcommit_allowed_then_hard_limit", func(t *testing.T) {
		n := &db.WorkerNode{
			ID:             "w-cpu",
			Status:         "ready",
			AllocatedVCPUs: 4, // hard limit = 4 * 2.0 = 8
			AllocatedMemMB: 16384,
			UsedVCPUs:      6,
		}
		// 6 + 2 = 8 <= 8 -> allowed
		chosen, err := SelectNodeForSandbox(ctx, []*db.WorkerNode{n}, nil, SandboxPlacementRequest{
			VCPUs: 2, MemoryMB: 512, DiskMB: 1024,
		}, p)
		if err != nil || chosen.ID != "w-cpu" {
			t.Fatalf("expected CPU overcommit within 2x ratio to succeed, got %+v err=%v", chosen, err)
		}
		// 6 + 3 = 9 > 8 -> hard CPU limit error
		_, err = SelectNodeForSandbox(ctx, []*db.WorkerNode{n}, nil, SandboxPlacementRequest{
			VCPUs: 3, MemoryMB: 512, DiskMB: 1024,
		}, p)
		assertLimitErr(t, err, "cpu")
	})
}

type policyMockStore struct {
	*mockStore
	policy db.ClusterSchedulingPolicy
}

func (p *policyMockStore) GetSchedulingPolicy(ctx context.Context) (db.ClusterSchedulingPolicy, error) {
	return p.policy, nil
}

func (p *policyMockStore) ListWorkerNodes(ctx context.Context) ([]*db.WorkerNode, error) {
	out := make([]*db.WorkerNode, 0, len(p.nodes))
	for _, n := range p.nodes {
		out = append(out, n)
	}
	return out, nil
}

func TestSchedulerScheduleNext_PolicyAndExclusion(t *testing.T) {
	reg := NewWorkerRegistry()
	base := newMockStore()
	policy := db.DefaultSchedulingPolicy()
	policy.Strategy = "bin_pack"
	policy.CPUHardOvercommitRatio = 2.0
	store := &policyMockStore{mockStore: base, policy: policy}
	sched := NewScheduler(reg, store)

	// Register 3 workers with nil conn just to inspect selection? Send requires conn, so we verify eligibility filtering cleanly:
	// Put a cordoned node in DB and ready nodes in DB; ScheduleNext excludes cordoned even if Worker.Status == "ready".
	store.nodes["w-cordoned"] = &db.WorkerNode{
		ID:             "w-cordoned",
		PoolID:         "default",
		Status:         "cordoned",
		AllocatedVCPUs: 16,
		AllocatedMemMB: 16384,
	}
	store.nodes["w-pack"] = &db.WorkerNode{
		ID:             "w-pack",
		PoolID:         "default",
		Status:         "ready",
		AllocatedVCPUs: 4, // hard limit = 8 with 2x ratio
		AllocatedMemMB: 8192,
		UsedVCPUs:      3,
		UsedMemMB:      4096,
	}
	store.nodes["w-empty"] = &db.WorkerNode{
		ID:             "w-empty",
		PoolID:         "default",
		Status:         "ready",
		AllocatedVCPUs: 8,
		AllocatedMemMB: 16384,
		UsedVCPUs:      0,
		UsedMemMB:      0,
	}

	// Verify buildWorkerNodeView + orderNodesByStrategy directly mirrors ScheduleNext ranking
	wCordoned := &Worker{ID: "w-cordoned", PoolID: "default", Status: "ready", Capabilities: map[string]interface{}{"architecture": "amd64"}}
	wPack := &Worker{ID: "w-pack", PoolID: "default", Status: "ready", Capabilities: map[string]interface{}{"architecture": "amd64"}}
	wEmpty := &Worker{ID: "w-empty", PoolID: "default", Status: "ready", Capabilities: map[string]interface{}{"architecture": "amd64"}}

	nvCordoned := buildWorkerNodeView(wCordoned, store.nodes["w-cordoned"])
	if nvCordoned.Status != "cordoned" {
		t.Fatalf("expected cordoned DB status preserved, got %q", nvCordoned.Status)
	}
	nvPack := buildWorkerNodeView(wPack, store.nodes["w-pack"])
	nvEmpty := buildWorkerNodeView(wEmpty, store.nodes["w-empty"])

	// Job requests 3 vCPUs (exceeds w-pack physical 4-3=1, but fits 2x overcommit 3+3=6 <= 8)
	placeReq := SandboxPlacementRequest{VCPUs: 3, MemoryMB: 1024}
	if err := checkNodeFits(nvPack, placeReq, policy); err != nil {
		t.Fatalf("expected w-pack to fit with 2x CPU overcommit, got %v", err)
	}
	ordered := orderNodesByStrategy([]*db.WorkerNode{nvEmpty, nvPack}, policy)
	if len(ordered) != 2 || ordered[0].ID != "w-pack" {
		t.Fatalf("expected bin_pack to rank w-pack first, got %+v", ordered)
	}

	// Ensure ScheduleNext with only cordoned worker in registry falls back to local instead of assigning cordoned worker
	_ = reg.workers
	reg.mu.Lock()
	reg.workers["w-cordoned"] = wCordoned
	reg.mu.Unlock()
	local := &mockLocalExecutor{}
	sched.SetLocalExecutor(local)
	specJSON, _ := json.Marshal(api.JobSpec{Architecture: "amd64", VCPUs: 1, MemoryMB: 512, Command: []string{"echo", "ok"}})
	store.jobs = append(store.jobs, &db.Job{
		ID:       "job-cordoned-skip",
		Type:     "command",
		State:    "queued",
		SpecJSON: string(specJSON),
		QueuedAt: time.Now().UTC(),
	})
	assigned, err := sched.ScheduleNext(context.Background())
	if err != nil {
		t.Fatalf("ScheduleNext error: %v", err)
	}
	if assigned != 0 {
		t.Fatalf("expected cordoned worker not to be assigned job, got assigned=%d", assigned)
	}
	if len(store.attempts) != 0 {
		t.Fatalf("expected 0 attempts on cordoned worker, got %d", len(store.attempts))
	}
}
