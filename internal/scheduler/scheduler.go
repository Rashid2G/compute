package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/pkg/api"
	"github.com/google/uuid"
)

// Store defines database operations required by the scheduler.
type Store interface {
	ListQueuedJobs(ctx context.Context) ([]*db.Job, error)
	GetJob(ctx context.Context, id string) (*db.Job, error)
	UpdateJobState(ctx context.Context, id, state, resultJSON string) error
	SaveJobAttempt(ctx context.Context, a *db.JobAttempt) error
	SaveWorkerLease(ctx context.Context, l *db.WorkerLease) error
	AppendJobLog(ctx context.Context, l *db.JobLog) error
	SaveWorkerNode(ctx context.Context, n *db.WorkerNode) error
	UpdateWorkerNodeStatus(ctx context.Context, id, status string) error
	GetWorkerNode(ctx context.Context, id string) (*db.WorkerNode, error)
}

// Requirements are the resources and placement constraints requested by a job.
type Requirements struct {
	Architecture string            `json:"architecture"`
	VCPUs        int               `json:"vcpus"`
	MemoryMB     int               `json:"memory_mb"`
	DiskMB       int               `json:"disk_mb"`
	PoolID       string            `json:"pool_id,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
}

// Matches reports whether a ready worker can satisfy a job's placement and resource requirements.
func Matches(w *Worker, req Requirements) bool {
	if w == nil || w.Status != "ready" {
		return false
	}
	if req.PoolID != "" && req.PoolID != w.PoolID {
		return false
	}
	if req.Architecture != "" && stringCapability(w.Capabilities, "architecture") != req.Architecture {
		return false
	}
	for k, v := range req.Labels {
		if w.Labels[k] != v {
			return false
		}
	}
	for key, required := range map[string]int{"vcpus": req.VCPUs, "memory_mb": req.MemoryMB, "disk_mb": req.DiskMB} {
		available := integerCapability(w.Capabilities, key)
		used := integerCapability(w.Capabilities, "used_"+key)
		if required > 0 && available-used < required {
			return false
		}
	}
	return true
}

func stringCapability(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func integerCapability(m map[string]interface{}, key string) int {
	if m == nil {
		return 0
	}
	val := m[key]
	if val == nil && key == "vcpus" {
		val = m["cpus"]
	}
	switch v := val.(type) {
	case int:
		return v
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
}

// LocalExecutor runs jobs on the control-plane host when no worker is
// connected. It is used as a fallback so jobs do not sit in the queue forever.
type LocalExecutor interface {
	TryStart(ctx context.Context, job *db.Job, spec api.JobSpec) bool
	Cancel(jobID string)
}

// Scheduler handles queue management, capability matching, and job assignment dispatch.
type Scheduler struct {
	registry *WorkerRegistry
	store    Store
	local    LocalExecutor
	mu       sync.Mutex
	stopCh   chan struct{}
}

// NewScheduler creates a new scheduler instance.
func NewScheduler(registry *WorkerRegistry, store Store) *Scheduler {
	return &Scheduler{
		registry: registry,
		store:    store,
		stopCh:   make(chan struct{}),
	}
}

// Start begins the background scheduling ticker.
func (s *Scheduler) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stopCh:
				return
			case <-ticker.C:
				_, _ = s.ScheduleNext(ctx)
			}
		}
	}()
}

// SetLocalExecutor installs a fallback executor used when no worker is connected.
func (s *Scheduler) SetLocalExecutor(e LocalExecutor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.local = e
}

// Stop stops background scheduling.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}

// ScheduleNext evaluates queued jobs and assigns them to compatible ready workers.
func (s *Scheduler) ScheduleNext(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs, err := s.store.ListQueuedJobs(ctx)
	if err != nil {
		return 0, err
	}

	assigned := 0
	workers := s.registry.List()

	for _, job := range jobs {
		var spec api.JobSpec
		if err := json.Unmarshal([]byte(job.SpecJSON), &spec); err != nil {
			continue
		}

		req := Requirements{
			Architecture: spec.Architecture,
			VCPUs:        spec.VCPUs,
			MemoryMB:     spec.MemoryMB,
			DiskMB:       spec.DiskMB,
			PoolID:       job.PoolID,
		}

		dispatched := false
		for _, w := range workers {
			if Matches(w, req) {
				attemptID := fmt.Sprintf("att_%s", uuid.NewString()[:8])
				leaseID := fmt.Sprintf("lea_%s", uuid.NewString()[:8])
				now := time.Now().UTC()
				expiresAt := now.Add(60 * time.Second)

				attempt := &db.JobAttempt{
					ID:        attemptID,
					JobID:     job.ID,
					WorkerID:  w.ID,
					State:     "assigned",
					CreatedAt: now,
				}
				if err := s.store.SaveJobAttempt(ctx, attempt); err != nil {
					continue
				}

				lease := &db.WorkerLease{
					ID:        leaseID,
					JobID:     job.ID,
					AttemptID: attemptID,
					WorkerID:  w.ID,
					ExpiresAt: expiresAt,
					State:     "active",
				}
				if err := s.store.SaveWorkerLease(ctx, lease); err != nil {
					continue
				}

				if err := s.store.UpdateJobState(ctx, job.ID, "assigned", ""); err != nil {
					continue
				}

				msg := api.WorkerAssignment{
					WorkerMessage: api.WorkerMessage{
						Version:  1,
						Type:     "worker.assignment",
						ID:       uuid.NewString(),
						WorkerID: w.ID,
						JobID:    job.ID,
					},
					LeaseID:   leaseID,
					AttemptID: attemptID,
					OwnerID:   job.OwnerID,
					Spec:      spec,
				}
				if err := s.registry.Send(ctx, w.ID, msg); err == nil {
					assigned++
					dispatched = true
					break
				}
				// Delivery failed; put the job back so another worker (or the
				// local fallback) can pick it up instead of leaving it stuck.
				_ = s.store.UpdateJobState(ctx, job.ID, "queued", "")
			}
		}

		// No workers are connected at all: fall back to running the job on the
		// control-plane host so CLI submissions make progress instead of
		// sitting in the queue forever.
		if !dispatched && s.local != nil && len(workers) == 0 {
			if s.local.TryStart(ctx, job, spec) {
				assigned++
			}
		}
	}

	return assigned, nil
}

// HandleWorkerMessage processes inbound protocol messages from a connected worker.
func (s *Scheduler) HandleWorkerMessage(ctx context.Context, w *Worker, envelope api.WorkerMessage, raw []byte) error {
	switch envelope.Type {
	case "worker.heartbeat":
		var hb api.WorkerHeartbeat
		_ = json.Unmarshal(raw, &hb)
		now := time.Now().UTC()
		w.Capabilities = hb.Capabilities
		capJSON, _ := json.Marshal(hb.Capabilities)
		_ = s.store.SaveWorkerNode(ctx, &db.WorkerNode{
			ID:               w.ID,
			Name:             w.ID,
			PoolID:           w.PoolID,
			Role:             "worker",
			Status:           "ready",
			CapabilitiesJSON: string(capJSON),
			LastHeartbeat:    &now,
			UpdatedAt:        now,
		})
		_ = s.registry.Send(ctx, w.ID, api.WorkerHeartbeatAck{
			WorkerMessage: api.WorkerMessage{
				Version:  1,
				Type:     "worker.heartbeat_ack",
				ID:       uuid.NewString(),
				WorkerID: w.ID,
			},
			ServerTime: time.Now().Unix(),
		})

	case "worker.assignment_ack":
		_ = s.store.UpdateJobState(ctx, envelope.JobID, "running", "")

	case "worker.assignment_reject":
		_ = s.store.UpdateJobState(ctx, envelope.JobID, "queued", "")

	case "worker.workload_event":
		var evt api.WorkerWorkloadEvent
		if err := json.Unmarshal(raw, &evt); err == nil {
			if evt.Event == "job.succeeded" {
				resJSON, _ := json.Marshal(evt.Data)
				_ = s.store.UpdateJobState(ctx, envelope.JobID, "succeeded", string(resJSON))
			} else if evt.Event == "job.failed" {
				resJSON, _ := json.Marshal(evt.Data)
				_ = s.store.UpdateJobState(ctx, envelope.JobID, "failed", string(resJSON))
			}
		}

	case "worker.log_chunk":
		var chunk api.WorkerLogChunk
		if err := json.Unmarshal(raw, &chunk); err == nil {
			_ = s.store.AppendJobLog(ctx, &db.JobLog{
				JobID:     envelope.JobID,
				AttemptID: chunk.AttemptID,
				Stream:    chunk.Stream,
				Seq:       chunk.Seq,
				Data:      chunk.Data,
				CreatedAt: time.Now().UTC(),
			})
		}
	}
	return nil
}
