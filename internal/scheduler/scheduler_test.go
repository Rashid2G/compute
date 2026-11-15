package scheduler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/pkg/api"
)

type mockStore struct {
	jobs      []*db.Job
	jobStates map[string]string
	attempts  []*db.JobAttempt
	leases    []*db.WorkerLease
	logs      []*db.JobLog
	nodes     map[string]*db.WorkerNode
}

func newMockStore() *mockStore {
	return &mockStore{
		jobStates: make(map[string]string),
		nodes:     make(map[string]*db.WorkerNode),
	}
}

func (m *mockStore) ListQueuedJobs(ctx context.Context) ([]*db.Job, error) {
	out := []*db.Job{}
	for _, j := range m.jobs {
		if st, ok := m.jobStates[j.ID]; !ok || st == "queued" {
			out = append(out, j)
		}
	}
	return out, nil
}

func (m *mockStore) GetJob(ctx context.Context, id string) (*db.Job, error) {
	for _, j := range m.jobs {
		if j.ID == id {
			return j, nil
		}
	}
	return nil, nil
}

func (m *mockStore) UpdateJobState(ctx context.Context, id, state, resultJSON string) error {
	m.jobStates[id] = state
	return nil
}

func (m *mockStore) SaveJobAttempt(ctx context.Context, a *db.JobAttempt) error {
	m.attempts = append(m.attempts, a)
	return nil
}

func (m *mockStore) SaveWorkerLease(ctx context.Context, l *db.WorkerLease) error {
	m.leases = append(m.leases, l)
	return nil
}

func (m *mockStore) AppendJobLog(ctx context.Context, l *db.JobLog) error {
	m.logs = append(m.logs, l)
	return nil
}

func (m *mockStore) SaveWorkerNode(ctx context.Context, n *db.WorkerNode) error {
	m.nodes[n.ID] = n
	return nil
}

func (m *mockStore) UpdateWorkerNodeStatus(ctx context.Context, id, status string) error {
	if n, ok := m.nodes[id]; ok {
		n.Status = status
	}
	return nil
}

func (m *mockStore) GetWorkerNode(ctx context.Context, id string) (*db.WorkerNode, error) {
	return m.nodes[id], nil
}

func TestMatches(t *testing.T) {
	w := &Worker{
		ID:     "w1",
		PoolID: "pool-a",
		Status: "ready",
		Capabilities: map[string]interface{}{
			"architecture":   "amd64",
			"vcpus":          8,
			"used_vcpus":     2,
			"memory_mb":      16384,
			"used_memory_mb": 4096,
			"disk_mb":        50000,
			"used_disk_mb":   10000,
		},
		Labels: map[string]string{
			"env": "prod",
		},
	}

	reqMatch := Requirements{
		Architecture: "amd64",
		VCPUs:        4,
		MemoryMB:     8192,
		DiskMB:       20000,
		PoolID:       "pool-a",
		Labels: map[string]string{
			"env": "prod",
		},
	}
	if !Matches(w, reqMatch) {
		t.Fatalf("expected worker to match")
	}

	reqOverCPU := reqMatch
	reqOverCPU.VCPUs = 10
	if Matches(w, reqOverCPU) {
		t.Fatalf("expected worker to not match due to CPU oversubscription")
	}

	reqWrongPool := reqMatch
	reqWrongPool.PoolID = "pool-b"
	if Matches(w, reqWrongPool) {
		t.Fatalf("expected worker to not match due to pool mismatch")
	}

	w.Status = "offline"
	if Matches(w, reqMatch) {
		t.Fatalf("expected offline worker to not match")
	}
}

type mockLocalExecutor struct {
	started []string
}

func (m *mockLocalExecutor) TryStart(ctx context.Context, job *db.Job, spec api.JobSpec) bool {
	m.started = append(m.started, job.ID)
	return true
}

func (m *mockLocalExecutor) Cancel(jobID string) {}

func TestSchedulerLocalFallback(t *testing.T) {
	reg := NewWorkerRegistry()
	store := newMockStore()
	sched := NewScheduler(reg, store)
	local := &mockLocalExecutor{}
	sched.SetLocalExecutor(local)

	specJSON, _ := json.Marshal(api.JobSpec{
		Architecture: "amd64",
		VCPUs:        2,
		MemoryMB:     2048,
		Command:      []string{"echo", "hello"},
	})
	store.jobs = append(store.jobs, &db.Job{
		ID:       "job-local",
		Type:     "command",
		State:    "queued",
		SpecJSON: string(specJSON),
		QueuedAt: time.Now().UTC(),
	})

	assigned, err := sched.ScheduleNext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if assigned != 1 {
		t.Fatalf("expected 1 assigned via local fallback, got %d", assigned)
	}
	if len(local.started) != 1 || local.started[0] != "job-local" {
		t.Fatalf("expected local executor to start job-local, got %v", local.started)
	}
}

func TestSchedulerMatching(t *testing.T) {
	reg := NewWorkerRegistry()
	store := newMockStore()
	sched := NewScheduler(reg, store)

	spec := api.JobSpec{
		Architecture: "amd64",
		VCPUs:        2,
		MemoryMB:     2048,
		DiskMB:       5000,
		Command:      []string{"echo", "hello"},
	}
	specJSON, _ := json.Marshal(spec)

	store.jobs = append(store.jobs, &db.Job{
		ID:       "job-1",
		Type:     "command",
		State:    "queued",
		SpecJSON: string(specJSON),
		QueuedAt: time.Now().UTC(),
	})

	// No workers registered yet
	assigned, err := sched.ScheduleNext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if assigned != 0 {
		t.Fatalf("expected 0 assigned, got %d", assigned)
	}
}
