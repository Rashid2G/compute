package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/pkg/api"
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

	policy := db.DefaultSchedulingPolicy()
	if ps, ok := s.store.(interface {
		GetSchedulingPolicy(ctx context.Context) (db.ClusterSchedulingPolicy, error)
	}); ok {
		if p, err := ps.GetSchedulingPolicy(ctx); err == nil {
			policy = p
		}
	}

	dbNodesByID := make(map[string]*db.WorkerNode)
	if ns, ok := s.store.(interface {
		ListWorkerNodes(ctx context.Context) ([]*db.WorkerNode, error)
	}); ok {
		if nodes, err := ns.ListWorkerNodes(ctx); err == nil {
			for _, n := range nodes {
				if n != nil {
					dbNodesByID[n.ID] = n
				}
			}
		}
	}

	assigned := 0
	workers := s.registry.List()
	workerByID := make(map[string]*Worker, len(workers))
	nodeViews := make(map[string]*db.WorkerNode, len(workers))
	for _, w := range workers {
		workerByID[w.ID] = w
		dbNode := dbNodesByID[w.ID]
		if dbNode == nil {
			dbNode, _ = s.store.GetWorkerNode(ctx, w.ID)
		}
		nodeViews[w.ID] = buildWorkerNodeView(w, dbNode)
	}

	for _, job := range jobs {
		var spec api.JobSpec
		if err := json.Unmarshal([]byte(job.SpecJSON), &spec); err != nil {
			continue
		}

		if (job.Type == "image-build" || job.Type == "image.build" || spec.TargetTemplate != "") && s.local != nil {
			if s.local.TryStart(ctx, job, spec) {
				assigned++
				continue
			}
		}

		req := Requirements{
			Architecture: spec.Architecture,
			VCPUs:        spec.VCPUs,
			MemoryMB:     spec.MemoryMB,
			DiskMB:       spec.DiskMB,
			PoolID:       job.PoolID,
		}
		placeReq := SandboxPlacementRequest{
			PoolID:   job.PoolID,
			VCPUs:    spec.VCPUs,
			MemoryMB: spec.MemoryMB,
			DiskMB:   spec.DiskMB,
		}

		eligibleNodes := make([]*db.WorkerNode, 0, len(workers))
		for _, w := range workers {
			nv := nodeViews[w.ID]
			if nv == nil || nv.Status != "ready" {
				continue
			}
			if req.PoolID != "" && nv.PoolID != req.PoolID {
				continue
			}
			if req.Architecture != "" && stringCapability(w.Capabilities, "architecture") != req.Architecture {
				continue
			}
			labelsMatch := true
			for k, v := range req.Labels {
				if w.Labels[k] != v {
					labelsMatch = false
					break
				}
			}
			if !labelsMatch {
				continue
			}
			if err := checkNodeFits(nv, placeReq, policy); err != nil {
				continue
			}
			eligibleNodes = append(eligibleNodes, nv)
		}

		orderedNodes := orderNodesByStrategy(eligibleNodes, policy)
		dispatched := false
		for _, nv := range orderedNodes {
			w := workerByID[nv.ID]
			if w == nil {
				continue
			}
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

			execSpec := spec
			if job.RepoID != "" {
				if ghStore, ok := s.store.(interface {
					GetGitHubConnection(ctx context.Context, id string) (*db.GitHubConnection, error)
				}); ok {
					if conn, err := ghStore.GetGitHubConnection(ctx, job.RepoID); err == nil && conn != nil && conn.GitHubToken != "" {
						authorized := spec.AllowCrossOwner || conn.OwnerID == job.OwnerID || (conn.OwnerID == "default" && job.OwnerID == "default")
						if !authorized {
							for _, oid := range spec.AuthorizedOwnerIDs {
								if oid != "" && oid == conn.OwnerID {
									authorized = true
									break
								}
							}
						}
						if authorized {
							execSpec.Env = make(map[string]string, len(spec.Env)+1)
							for k, v := range spec.Env {
								execSpec.Env[k] = v
							}
							if execSpec.Env["GITHUB_TOKEN"] == "" {
								execSpec.Env["GITHUB_TOKEN"] = conn.GitHubToken
							}
						}
					}
				}
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
				Spec:      execSpec,
			}
			if err := s.registry.Send(ctx, w.ID, msg); err == nil {
				if updater, ok := s.store.(interface {
					UpdateJobExecution(ctx context.Context, id, vmID, workerID string) error
				}); ok {
					_ = updater.UpdateJobExecution(ctx, job.ID, "", w.ID)
				}
				// Update local capacity snapshot for subsequent jobs in this batch.
				nv.UsedVCPUs += spec.VCPUs
				nv.UsedMemMB += spec.MemoryMB
				nv.UsedDiskMB += spec.DiskMB
				nv.RunningVMs++
				assigned++
				dispatched = true
				break
			}
			// Delivery failed; put the job back so another worker (or the
			// local fallback) can pick it up instead of leaving it stuck.
			_ = s.store.UpdateJobState(ctx, job.ID, "queued", "")
		}

		// Fall back to running the job on the control-plane host when no
		// eligible worker could accept it (either no workers are connected,
		// the job targets the default pool, or no ready workers exist in the
		// requested pool) so CLI submissions make progress instead of sitting
		// in the queue forever.
		canFallbackLocal := len(workers) == 0 || job.PoolID == "default"
		if !canFallbackLocal {
			hasReadyWorkerInPool := false
			for _, w := range workers {
				nv := nodeViews[w.ID]
				if nv != nil && nv.Status == "ready" && (req.PoolID == "" || nv.PoolID == req.PoolID) {
					hasReadyWorkerInPool = true
					break
				}
			}
			if !hasReadyWorkerInPool {
				hasUnconnectedReadyDBNode := false
				for id, n := range dbNodesByID {
					if _, connected := workerByID[id]; !connected && n.Status == "ready" && (req.PoolID == "" || n.PoolID == req.PoolID) {
						hasUnconnectedReadyDBNode = true
						break
					}
				}
				if !hasUnconnectedReadyDBNode {
					canFallbackLocal = true
				}
			} else {
				canFallbackLocal = true
			}
		}
		if !dispatched && s.local != nil && canFallbackLocal {
			if s.local.TryStart(ctx, job, spec) {
				assigned++
			}
		}
	}

	return assigned, nil
}

func buildWorkerNodeView(w *Worker, dbNode *db.WorkerNode) *db.WorkerNode {
	nv := &db.WorkerNode{
		ID:     w.ID,
		Name:   w.ID,
		PoolID: w.PoolID,
		Status: w.Status,
	}
	if dbNode != nil {
		*nv = *dbNode
	}
	if w.Status != "ready" {
		nv.Status = w.Status
	} else if dbNode != nil && (dbNode.Status == "cordoned" || dbNode.Status == "draining" || dbNode.Status == "offline") {
		nv.Status = dbNode.Status
	} else if nv.Status == "" {
		nv.Status = "ready"
	}
	if nv.PoolID == "" {
		nv.PoolID = w.PoolID
	}
	if len(w.Labels) > 0 && nv.LabelsJSON == "" {
		if b, err := json.Marshal(w.Labels); err == nil {
			nv.LabelsJSON = string(b)
		}
	}
	if v := integerCapability(w.Capabilities, "vcpus"); v > 0 && nv.PhysicalVCPUs == 0 {
		nv.PhysicalVCPUs = v
	}
	if v := integerCapability(w.Capabilities, "memory_mb"); v > 0 && nv.PhysicalMemMB == 0 {
		nv.PhysicalMemMB = v
	}
	if v := integerCapability(w.Capabilities, "disk_mb"); v > 0 && nv.PhysicalDiskMB == 0 {
		nv.PhysicalDiskMB = v
	}
	if v := integerCapability(w.Capabilities, "max_vcpus"); v > 0 && nv.AllocatedVCPUs == 0 {
		nv.AllocatedVCPUs = v
	}
	if v := integerCapability(w.Capabilities, "max_memory_mb"); v > 0 && nv.AllocatedMemMB == 0 {
		nv.AllocatedMemMB = v
	}
	if v := integerCapability(w.Capabilities, "max_disk_mb"); v > 0 && nv.AllocatedDiskMB == 0 {
		nv.AllocatedDiskMB = v
	}
	if v, ok := metricInt(w.Metrics, w.Capabilities, "used_vcpus"); ok {
		nv.UsedVCPUs = v
	}
	if v, ok := metricInt(w.Metrics, w.Capabilities, "used_mem_mb", "used_memory_mb"); ok {
		nv.UsedMemMB = v
	}
	if v, ok := metricInt(w.Metrics, w.Capabilities, "used_disk_mb"); ok {
		nv.UsedDiskMB = v
	}
	if v, ok := metricInt(w.Metrics, w.Capabilities, "running_vms"); ok {
		nv.RunningVMs = v
	}
	if v, ok := metricInt(w.Metrics, w.Capabilities, "memory_available_mb"); ok {
		nv.AvailableMemMB = v
	}
	if v, ok := metricInt(w.Metrics, w.Capabilities, "disk_available_mb"); ok {
		nv.AvailableDiskMB = v
	}
	return nv
}

func metricInt(metrics, caps map[string]interface{}, keys ...string) (int, bool) {
	for _, m := range []map[string]interface{}{metrics, caps} {
		if m == nil {
			continue
		}
		for _, k := range keys {
			if _, exists := m[k]; exists {
				return integerCapability(m, k), true
			}
		}
	}
	return 0, false
}

// HandleWorkerMessage processes inbound protocol messages from a connected worker.
func (s *Scheduler) HandleWorkerMessage(ctx context.Context, w *Worker, envelope api.WorkerMessage, raw []byte) error {
	switch envelope.Type {
	case "worker.heartbeat":
		var hb api.WorkerHeartbeat
		_ = json.Unmarshal(raw, &hb)
		now := time.Now().UTC()
		w.Capabilities = hb.Capabilities
		w.Metrics = hb.Metrics
		capJSON, _ := json.Marshal(hb.Capabilities)

		node, err := s.store.GetWorkerNode(ctx, w.ID)
		var prevStatus string
		if err != nil || node == nil {
			node = &db.WorkerNode{
				ID:        w.ID,
				Name:      w.ID,
				PoolID:    w.PoolID,
				Role:      "worker",
				Status:    "ready",
				CreatedAt: now,
			}
		} else {
			prevStatus = node.Status
		}
		if node.PoolID == "" {
			node.PoolID = w.PoolID
		}
		if node.Role == "" {
			node.Role = "worker"
		}
		if node.Status != "cordoned" && node.Status != "draining" {
			if hb.Status != "" {
				node.Status = hb.Status
			} else {
				node.Status = "ready"
			}
		}
		if node.Status != "" {
			w.Status = node.Status
		}
		if v := integerCapability(hb.Capabilities, "vcpus"); v > 0 {
			node.PhysicalVCPUs = v
			if node.TotalVCPUs == 0 {
				node.TotalVCPUs = v
			}
		}
		if v := integerCapability(hb.Capabilities, "memory_mb"); v > 0 {
			node.PhysicalMemMB = v
			if node.TotalMemMB == 0 {
				node.TotalMemMB = v
			}
		}
		if v := integerCapability(hb.Capabilities, "disk_mb"); v > 0 {
			node.PhysicalDiskMB = v
			if node.TotalDiskMB == 0 {
				node.TotalDiskMB = v
			}
		}
		if v, ok := metricInt(hb.Metrics, hb.Capabilities, "used_vcpus"); ok {
			node.UsedVCPUs = v
		}
		if v, ok := metricInt(hb.Metrics, hb.Capabilities, "used_mem_mb", "used_memory_mb"); ok {
			node.UsedMemMB = v
		}
		if v, ok := metricInt(hb.Metrics, hb.Capabilities, "used_disk_mb"); ok {
			node.UsedDiskMB = v
		}
		if v, ok := metricInt(hb.Metrics, hb.Capabilities, "running_vms"); ok {
			node.RunningVMs = v
		}
		if v, ok := metricInt(hb.Metrics, hb.Capabilities, "memory_available_mb"); ok && v > 0 {
			node.AvailableMemMB = v
		}
		if v, ok := metricInt(hb.Metrics, hb.Capabilities, "disk_available_mb"); ok && v > 0 {
			node.AvailableDiskMB = v
		}
		node.CapabilitiesJSON = string(capJSON)
		node.LastHeartbeat = &now
		node.UpdatedAt = now
		_ = s.store.SaveWorkerNode(ctx, node)
		firstHeartbeat := !w.heartbeatSeen.Swap(true)
		statusChanged := prevStatus != "" && prevStatus != node.Status
		if firstHeartbeat || statusChanged {
			log.Printf("[scheduler] worker heartbeat id=%s pool=%s status=%s avail_mem=%dMB avail_disk=%dMB",
				w.ID, node.PoolID, node.Status, node.AvailableMemMB, node.AvailableDiskMB)
		}
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
