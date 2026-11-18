package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/scheduler"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/Rashid2G/compute/pkg/api"
	"github.com/google/uuid"
)

const (
	backendOpQueueWorkers = 4
	// backendOpQueueMaxDepth bounds how many operations may sit in the backend
	// queue at once. Bulk handlers reject new work with 429 above this depth so
	// a client cannot enqueue unbounded closures (memory DoS / cross-tenant
	// starvation).
	backendOpQueueMaxDepth = 1024
	// maxBulkRequestItems bounds how many items a single bulk request may
	// carry. Larger requests are rejected with 400.
	maxBulkRequestItems = 100
	// maxTrackedTasks caps the in-memory task registry used by
	// GET /api/v1/tasks/{id}; oldest entries are evicted first.
	maxTrackedTasks = 256
)

var errBackendQueueFull = errors.New("backend operation queue is full; retry shortly")

// serverOpQueue is a bounded-concurrency, bounded-depth FIFO backend execution
// queue so bulk and long-running requests accepted from HTTP handlers keep
// executing cleanly in order even if the browser disconnects or refreshes.
type serverOpQueue struct {
	mu       sync.Mutex
	cond     *sync.Cond
	items    []func()
	active   int
	maxDepth int
}

func newServerOpQueue(workers, maxDepth int) *serverOpQueue {
	q := &serverOpQueue{maxDepth: maxDepth}
	q.cond = sync.NewCond(&q.mu)
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		go q.workerLoop()
	}
	return q
}

func (q *serverOpQueue) workerLoop() {
	for {
		q.mu.Lock()
		for len(q.items) == 0 {
			q.cond.Wait()
		}
		fn := q.items[0]
		q.items[0] = nil
		q.items = q.items[1:]
		q.active++
		q.mu.Unlock()

		func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("[queue] recovered panic in backend queued op: %v", rec)
				}
			}()
			fn()
		}()

		q.mu.Lock()
		q.active--
		q.mu.Unlock()
	}
}

func (q *serverOpQueue) enqueue(fn func()) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.maxDepth > 0 && len(q.items) >= q.maxDepth {
		return errBackendQueueFull
	}
	q.items = append(q.items, fn)
	q.cond.Signal()
	return nil
}

func (q *serverOpQueue) pendingCount() int {
	q.mu.Lock()
	n := len(q.items) + q.active
	q.mu.Unlock()
	return n
}

// wouldExceed reports whether admitting n more operations would push the queue
// past its depth bound.
func (q *serverOpQueue) wouldExceed(n int) bool {
	if q.maxDepth <= 0 {
		return false
	}
	q.mu.Lock()
	depth := len(q.items) + q.active
	q.mu.Unlock()
	return depth+n > q.maxDepth
}

func (s *Server) ensureOpQueue() *serverOpQueue {
	s.opQueueOnce.Do(func() {
		s.opQueue = newServerOpQueue(backendOpQueueWorkers, backendOpQueueMaxDepth)
	})
	return s.opQueue
}

func (s *Server) enqueueBackendOp(fn func(context.Context)) error {
	q := s.ensureOpQueue()
	return q.enqueue(func() {
		fn(context.Background())
	})
}

func (s *Server) backendOpPending() int {
	// Route through the same sync.Once that creates the queue so concurrent
	// /queue and cluster-snapshot polling never race the first bulk request.
	return s.ensureOpQueue().pendingCount()
}

type httpStatusError struct {
	code int
	err  error
}

func (e *httpStatusError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *httpStatusError) Unwrap() error {
	return e.err
}

func writeDetachedError(w http.ResponseWriter, r *http.Request, err error) {
	if writeResourceLimitError(w, r, err) {
		return
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) && statusErr != nil {
		httpError(w, statusErr.Error(), statusErr.code)
		return
	}
	httpError(w, err.Error(), http.StatusInternalServerError)
}

// vmStartWarning reports that a sandbox record was created successfully but the
// requested auto-start did not complete. The VM exists, so the request is not a
// failure, but callers should surface the reason instead of reporting a plain
// success.
type vmStartWarning struct {
	vmID   string
	reason string
}

func (w *vmStartWarning) Error() string { return w.reason }

// runDetached executes fn with context.Background() so an in-flight request
// continues to completion even if the HTTP client disconnects or refreshes.
// If the client stays connected, its result is returned directly.
//
// A panic inside fn is recovered and delivered as an error: a bare goroutine
// panic would otherwise escape net/http's per-request recover and crash the
// daemon.
func runDetached[T any](r *http.Request, fn func(context.Context) (T, error)) (T, error, bool) {
	type result struct {
		val T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				stack := make([]byte, 16<<10)
				n := runtime.Stack(stack, false)
				log.Printf("[computed] recovered panic in detached request: %v\n%s", rec, stack[:n])
				var zero T
				ch <- result{val: zero, err: fmt.Errorf("internal error: %v", rec)}
			}
		}()
		val, err := fn(context.Background())
		ch <- result{val: val, err: err}
	}()
	select {
	case res := <-ch:
		return res.val, res.err, false
	case <-r.Context().Done():
		var zero T
		return zero, r.Context().Err(), true
	}
}

func runDetachedErr(r *http.Request, fn func(context.Context) error) (error, bool) {
	_, err, disconnected := runDetached(r, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, fn(ctx)
	})
	return err, disconnected
}

// BulkCreateVMsRequest provisions multiple sandboxes in a single backend request.
type BulkCreateVMsRequest struct {
	Items []vm.CreateRequest `json:"items"`
	Async *bool              `json:"async,omitempty"`
}

// BulkCloneItem describes one target clone in a bulk clone request.
type BulkCloneItem struct {
	SourceID string `json:"source_id"`
	vm.CreateRequest
}

// BulkCloneVMsRequest replicates one or more source sandboxes in a single request.
type BulkCloneVMsRequest struct {
	SourceID         string          `json:"source_id,omitempty"`
	Names            []string        `json:"names,omitempty"`
	Items            []BulkCloneItem `json:"items,omitempty"`
	Description      string          `json:"description,omitempty"`
	VCPUs            int             `json:"vcpus,omitempty"`
	MemoryMB         int             `json:"memory_mb,omitempty"`
	DiskMB           int             `json:"disk_mb,omitempty"`
	DiskGB           int             `json:"disk_gb,omitempty"`
	NetworkIsolation string          `json:"network_isolation,omitempty"`
	NetworkIsolated  *bool           `json:"network_isolated,omitempty"`
	AutoStart        bool            `json:"auto_start,omitempty"`
	Start            *bool           `json:"start,omitempty"` // Alias for auto_start
	Protected        *bool           `json:"protected,omitempty"`
	WorkerID         string          `json:"worker_id,omitempty"`
	PoolID           string          `json:"pool_id,omitempty"`
	Async            *bool           `json:"async,omitempty"`
}

func (s *Server) validateCreateItem(ctx context.Context, r *http.Request, ownerID string, isAdmin bool, authorizedOwners []string, req *vm.CreateRequest) error {
	if !req.AutoStart && req.Start != nil && *req.Start {
		req.AutoStart = true
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Name)), "job-") {
		return &httpStatusError{code: http.StatusBadRequest, err: errors.New("name prefix 'job-' is reserved for ephemeral CI jobs")}
	}
	if isAdmin {
		if strings.TrimSpace(req.OwnerID) == "" {
			req.OwnerID = ownerID
		}
		req.AllowCrossOwner = true
	} else {
		req.OwnerID = ownerID
		req.AuthorizedOwnerIDs = authorizedOwners
	}

	if req.SSHPublicKey == "" && req.SSHKeyID == "" && !req.SSHNoDefaultKey && req.OwnerID == ownerID {
		if def, err := s.vmMgr.GetStore().GetDefaultSSHKey(ctx, ownerID); err != nil {
			log.Printf("[ssh-keys] default key lookup for %s: %v", ownerID, err)
		} else if def != nil {
			req.SSHKeyID = def.ID
		}
	}

	if req.CloneFrom != "" {
		src, err := s.resolveAccessibleVM(ctx, r, req.CloneFrom)
		if err != nil || src == nil || !s.canAccessOwner(r, src.OwnerID) {
			return &httpStatusError{code: http.StatusNotFound, err: errors.New("clone source not found")}
		}
		req.CloneFrom = vmIdentifier(src)
		if req.WorkerID == "" && src.WorkerID != "" {
			req.WorkerID = src.WorkerID
		}
	}
	if req.Template != "" {
		tpl, err := s.resolveAccessibleTemplate(ctx, r, req.Template)
		if err != nil || tpl == nil || !s.canAccessOwner(r, tpl.OwnerID) {
			return &httpStatusError{code: http.StatusNotFound, err: errors.New("template not found")}
		}
		req.Template = templateIdentifier(tpl)
	}
	if req.Image != "" {
		img, err := s.resolveAccessibleImage(ctx, r, req.Image)
		if err != nil {
			return &httpStatusError{code: http.StatusInternalServerError, err: err}
		}
		if img != nil {
			req.Image = imageIdentifier(img)
		} else {
			tpl, tplErr := s.resolveAccessibleTemplate(ctx, r, req.Image)
			if tplErr != nil {
				return &httpStatusError{code: http.StatusInternalServerError, err: tplErr}
			}
			if tpl != nil {
				req.Image = templateIdentifier(tpl)
			} else if !s.vmMgr.IsPlatformBaseImage(req.Image) {
				if globalImg, _ := s.vmMgr.GetImage(ctx, req.Image); globalImg != nil {
					return &httpStatusError{code: http.StatusNotFound, err: errors.New("image not found")}
				}
				if globalTpl, _ := s.vmMgr.GetTemplate(ctx, req.Image); globalTpl != nil {
					return &httpStatusError{code: http.StatusNotFound, err: errors.New("image not found")}
				}
			}
		}
	}
	return nil
}

// reserveNodePlacement selects a placement target and reserves in-flight CPU/RAM/Disk
// capacity under createPlaceMu so concurrent creates run disk/SSH I/O in parallel
// without over-committing any worker or nod_host_local.
func (s *Server) reserveNodePlacement(ctx context.Context, req vm.CreateRequest, vcpus, memoryMB, diskMB int) (*db.WorkerNode, func(), error) {
	s.createPlaceMu.Lock()
	defer s.createPlaceMu.Unlock()

	var selectedNode *db.WorkerNode
	if s.vmMgr != nil && s.vmMgr.GetStore() != nil {
		workers, localNode, policy, pErr := s.buildPlacementNodes(ctx)
		if pErr == nil && (len(workers) > 0 || localNode != nil) {
			applyInFlight := func(n *db.WorkerNode) {
				if n == nil {
					return
				}
				if v := s.inFlightNodeVCPUs[n.ID]; v > 0 {
					n.UsedVCPUs += v
				}
				if v := s.inFlightNodeMemMB[n.ID]; v > 0 {
					n.UsedMemMB += v
				}
				if v := s.inFlightNodeDisk[n.ID]; v > 0 {
					n.UsedDiskMB += v
				}
			}
			for _, w := range workers {
				applyInFlight(w)
			}
			applyInFlight(localNode)

			chosen, selErr := scheduler.SelectNodeForSandbox(ctx, workers, localNode, scheduler.SandboxPlacementRequest{
				WorkerID:     req.WorkerID,
				PoolID:       req.PoolID,
				VCPUs:        vcpus,
				MemoryMB:     memoryMB,
				DiskMB:       diskMB,
				SkipCPUCheck: !req.AutoStart,
			}, policy)
			if selErr != nil {
				var rle *api.ResourceLimitError
				if errors.As(selErr, &rle) {
					return nil, nil, selErr
				}
				if strings.Contains(selErr.Error(), "not found") {
					return nil, nil, &httpStatusError{code: http.StatusNotFound, err: selErr}
				}
				return nil, nil, &httpStatusError{code: http.StatusBadRequest, err: selErr}
			}
			selectedNode = chosen
		}
	}

	if selectedNode == nil {
		return nil, func() {}, nil
	}

	if s.inFlightNodeVCPUs == nil {
		s.inFlightNodeVCPUs = make(map[string]int)
	}
	if s.inFlightNodeMemMB == nil {
		s.inFlightNodeMemMB = make(map[string]int)
	}
	if s.inFlightNodeDisk == nil {
		s.inFlightNodeDisk = make(map[string]int)
	}
	nodeID := selectedNode.ID
	resVCPUs := 0
	if req.AutoStart {
		resVCPUs = vcpus
	}
	s.inFlightNodeVCPUs[nodeID] += resVCPUs
	s.inFlightNodeMemMB[nodeID] += memoryMB
	s.inFlightNodeDisk[nodeID] += diskMB

	var released atomic.Bool
	release := func() {
		if !released.CompareAndSwap(false, true) {
			return
		}
		s.createPlaceMu.Lock()
		s.inFlightNodeVCPUs[nodeID] -= resVCPUs
		if s.inFlightNodeVCPUs[nodeID] <= 0 {
			delete(s.inFlightNodeVCPUs, nodeID)
		}
		s.inFlightNodeMemMB[nodeID] -= memoryMB
		if s.inFlightNodeMemMB[nodeID] <= 0 {
			delete(s.inFlightNodeMemMB, nodeID)
		}
		s.inFlightNodeDisk[nodeID] -= diskMB
		if s.inFlightNodeDisk[nodeID] <= 0 {
			delete(s.inFlightNodeDisk, nodeID)
		}
		s.createPlaceMu.Unlock()
	}
	return selectedNode, release, nil
}

func (s *Server) executeCreateVM(ctx context.Context, isAdmin bool, req vm.CreateRequest) (*db.VM, error) {
	if !req.AutoStart && req.Start != nil && *req.Start {
		req.AutoStart = true
	}
	vcpus, memoryMB, diskMB := s.estimateRequestResources(ctx, req)
	if err := s.enforceCreateQuotaWithAdmin(ctx, isAdmin, req.OwnerID, vcpus, memoryMB, diskMB); err != nil {
		return nil, &httpStatusError{code: http.StatusTooManyRequests, err: err}
	}

	selectedNode, releasePlacement, err := s.reserveNodePlacement(ctx, req, vcpus, memoryMB, diskMB)
	if err != nil {
		var rle *api.ResourceLimitError
		if errors.As(err, &rle) {
			return nil, err
		}
		return nil, err
	}
	defer releasePlacement()

	var created *db.VM
	if selectedNode != nil && selectedNode.ID != "nod_host_local" {
		created, err = s.dispatchRemoteVMCreate(ctx, req, selectedNode)
	} else {
		if selectedNode != nil {
			req.WorkerID = selectedNode.ID
			if req.PoolID == "" && selectedNode.PoolID != "" {
				req.PoolID = selectedNode.PoolID
			}
		}
		created, err = s.vmMgr.CreateVM(ctx, req)
	}

	if err != nil {
		var rle *api.ResourceLimitError
		if errors.As(err, &rle) {
			return nil, err
		}
		if strings.Contains(err.Error(), "ssh key not found") {
			return nil, &httpStatusError{code: http.StatusNotFound, err: err}
		}
		if strings.Contains(err.Error(), "base rootfs image not found") || strings.Contains(err.Error(), "image not found") || strings.Contains(err.Error(), "custom image rootfs not found") || (strings.Contains(err.Error(), "template '") && strings.Contains(err.Error(), "' not found")) {
			return nil, &httpStatusError{code: http.StatusNotFound, err: err}
		}
		if strings.Contains(err.Error(), "disk space issue") || strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "unsupported") || strings.Contains(err.Error(), "conflicting") {
			return nil, &httpStatusError{code: http.StatusBadRequest, err: err}
		}
		return nil, err
	}

	// Auto-start runs outside createPlaceMu while the node placement reservation
	// remains held until RunVM updates the VM status in SQLite.
	var startErr error
	if created != nil && req.AutoStart {
		if qErr := s.enforceRunQuotaWithAdmin(ctx, isAdmin, created); qErr != nil {
			startErr = qErr
		} else if runErr := s.vmMgr.RunVM(ctx, created.ID); runErr != nil {
			startErr = runErr
		} else if started, gErr := s.vmMgr.GetVM(ctx, created.ID); gErr == nil && started != nil {
			created = started
			if created.Status == db.StatusRunning {
				s.schedulePostBootSetup(ctx, created)
				if strings.TrimSpace(req.Command) != "" {
					s.schedulePostBootBuildCommand(ctx, created, req.Command, req.ExecUser)
				}
			}
		}
		if startErr != nil {
			log.Printf("[computed] create %s auto_start failed: %v", created.ID, startErr)
			s.recordEvent("sandbox.auto_start_failed", "", created.Name, "failed", 0, map[string]interface{}{
				"vm_id":  created.ID,
				"reason": startErr.Error(),
			})
		}
	}
	releasePlacement()

	s.broadcastClusterUpdate()
	if startErr != nil && created != nil {
		return created, &vmStartWarning{vmID: created.ID, reason: startErr.Error()}
	}
	return created, nil
}

func (s *Server) executeCloneVM(ctx context.Context, isAdmin bool, sourceVMID string, req vm.CreateRequest) (*db.VM, error) {
	if !req.AutoStart && req.Start != nil && *req.Start {
		req.AutoStart = true
	}
	vcpus, memoryMB, diskMB := s.estimateRequestResources(ctx, req)
	if err := s.enforceCreateQuotaWithAdmin(ctx, isAdmin, req.OwnerID, vcpus, memoryMB, diskMB); err != nil {
		return nil, &httpStatusError{code: http.StatusTooManyRequests, err: err}
	}
	placeReq := req
	if placeReq.WorkerID == "" && s.vmMgr != nil {
		if src, gErr := s.vmMgr.GetVM(ctx, sourceVMID); gErr == nil && src != nil {
			if src.WorkerID != "" {
				placeReq.WorkerID = src.WorkerID
			} else {
				placeReq.WorkerID = "nod_host_local"
			}
			if placeReq.PoolID == "" {
				placeReq.PoolID = src.PoolID
			}
			if req.VCPUs <= 0 && src.VCPUs > 0 {
				vcpus = src.VCPUs
			}
			if req.MemoryMB <= 0 && src.MemoryMB > 0 {
				memoryMB = src.MemoryMB
			}
			if req.DiskMB <= 0 && req.DiskGB <= 0 && src.DiskMB > 0 {
				diskMB = src.DiskMB
			}
		}
	}
	_, releasePlacement, pErr := s.reserveNodePlacement(ctx, placeReq, vcpus, memoryMB, diskMB)
	if pErr != nil {
		return nil, pErr
	}
	defer releasePlacement()

	cloned, err := s.vmMgr.CloneVM(ctx, sourceVMID, req)
	if err != nil {
		var rle *api.ResourceLimitError
		if errors.As(err, &rle) {
			return nil, err
		}
		if strings.Contains(err.Error(), "ssh key not found") {
			return nil, &httpStatusError{code: http.StatusNotFound, err: err}
		}
		if strings.Contains(err.Error(), "disk space issue") || strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "unsupported") || strings.Contains(err.Error(), "conflicting") {
			return nil, &httpStatusError{code: http.StatusBadRequest, err: err}
		}
		return nil, err
	}
	var startErr error
	if req.AutoStart {
		if qErr := s.enforceRunQuotaWithAdmin(ctx, isAdmin, cloned); qErr != nil {
			startErr = qErr
		} else if runErr := s.vmMgr.RunVM(ctx, cloned.ID); runErr != nil {
			startErr = runErr
		} else if started, getErr := s.vmMgr.GetVM(ctx, cloned.ID); getErr == nil && started != nil {
			cloned = started
			if cloned.Status == db.StatusRunning {
				s.schedulePostBootSetup(ctx, cloned)
			}
		}
		if startErr != nil {
			log.Printf("[computed] clone %s auto_start failed: %v", cloned.ID, startErr)
			s.recordEvent("sandbox.auto_start_failed", "", cloned.Name, "failed", 0, map[string]interface{}{
				"vm_id":  cloned.ID,
				"reason": startErr.Error(),
			})
		}
	}
	releasePlacement()
	s.broadcastClusterUpdate()
	if startErr != nil && cloned != nil {
		return cloned, &vmStartWarning{vmID: cloned.ID, reason: startErr.Error()}
	}
	return cloned, nil
}

// restartSingleVM restarts one VM and runs post-boot guest setup asynchronously
// when attached volumes or environment profiles exist.
func (s *Server) restartSingleVM(ctx context.Context, isAdmin bool, v *db.VM, waitSSH bool) (*db.VM, error) {
	if v == nil {
		return nil, fmt.Errorf("vm not found")
	}
	vmKey := vmIdentifier(v)
	if v.Status == db.StatusRunning || v.Status == db.StatusPaused {
		if vm.IsRemoteVM(v) {
			updated, err := s.callRemoteVMLifecycle(ctx, v, "vm.stop")
			if err != nil {
				return nil, err
			}
			if updated != nil {
				v = updated
			}
			s.removePortForwardsForVM(ctx, v, false)
		} else {
			if isAdmin {
				if err := s.vmMgr.StopVM(ctx, vmKey); err != nil {
					return nil, err
				}
			} else if err := s.vmMgr.StopVMForOwner(ctx, v.OwnerID, vmKey); err != nil {
				return nil, err
			}
			s.removePortForwardsForVM(ctx, v, false)
			if refreshed, gErr := s.vmMgr.GetVM(ctx, vmKey); gErr == nil && refreshed != nil {
				v = refreshed
			}
		}
	}
	if err := s.enforceRunQuotaWithAdmin(ctx, isAdmin, v); err != nil {
		return nil, &httpStatusError{code: http.StatusTooManyRequests, err: err}
	}
	if vm.IsRemoteVM(v) {
		updated, err := s.callRemoteVMLifecycle(ctx, v, "vm.start")
		if err != nil {
			return nil, err
		}
		s.broadcastClusterUpdate()
		return updated, nil
	}
	if err := s.vmMgr.RunVM(ctx, vmKey); err != nil {
		return nil, err
	}
	v, _ = s.vmMgr.GetVM(ctx, vmKey)
	if v != nil && v.Status == db.StatusRunning {
		_ = waitSSH
		s.schedulePostBootSetup(ctx, v)
	}
	s.broadcastClusterUpdate()
	return v, nil
}

// handleBulkCreateVMs handles POST /api/v1/vms/bulk-create and /api/v1/sandboxes/bulk-create.
func (s *Server) handleBulkCreateVMs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req BulkCreateVMsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Items) == 0 {
		httpError(w, "items is required", http.StatusBadRequest)
		return
	}
	if len(req.Items) > maxBulkRequestItems {
		httpError(w, fmt.Sprintf("too many items: %d (max %d per request)", len(req.Items), maxBulkRequestItems), http.StatusBadRequest)
		return
	}

	var dummyReq vm.CreateRequest
	ownerID, isAdmin, ok := s.authorizeCreateRequest(w, r, &dummyReq)
	if !ok {
		return
	}
	authorizedOwners := dummyReq.AuthorizedOwnerIDs
	actor := getActor(r)
	ctx := r.Context()

	type createTarget struct {
		label      string
		req        vm.CreateRequest
		resolveErr string
		op         *db.Operation
	}
	targets := make([]createTarget, len(req.Items))
	validCount := 0
	for i := range req.Items {
		item := req.Items[i]
		label := strings.TrimSpace(item.Name)
		if label == "" {
			label = fmt.Sprintf("item-%d", i+1)
		}
		if err := s.validateCreateItem(ctx, r, ownerID, isAdmin, authorizedOwners, &item); err != nil {
			targets[i] = createTarget{label: label, resolveErr: err.Error()}
			continue
		}
		targets[i] = createTarget{label: label, req: item}
		validCount++
	}

	async := req.Async == nil || *req.Async
	taskID := "task_" + uuid.NewString()[:8]

	if async {
		resp := api.BulkVMsResponse{
			Action:    "create",
			Status:    "queued",
			TaskID:    taskID,
			Requested: len(req.Items),
			Queued:    validCount,
			Succeeded: []string{},
			Failed:    []api.BulkVMItemError{},
			Warnings:  []api.BulkVMItemError{},
		}
		for _, t := range targets {
			if t.resolveErr != "" {
				resp.Failed = append(resp.Failed, api.BulkVMItemError{ID: t.label, Error: t.resolveErr})
			}
		}
		if validCount > 0 && s.ensureOpQueue().wouldExceed(validCount) {
			httpError(w, errBackendQueueFull.Error(), http.StatusTooManyRequests)
			return
		}
		// Persist every queued item before dispatch so the user can see what is
		// pending and the audit trail exists before execution starts.
		specs := make([]bulkOperationSpec, len(targets))
		for i, t := range targets {
			specs[i] = bulkOperationSpec{
				TargetName: t.label,
				Skip:       t.resolveErr != "",
				Request:    summarizeCreateRequest(t.req),
			}
		}
		ops, opErr := s.prepareBulkOperations(ctx, ownerID, "sandbox.create", taskID, actor, specs)
		if opErr != nil {
			httpError(w, "could not persist queued operations: "+opErr.Error(), http.StatusInternalServerError)
			return
		}
		for i := range ops {
			targets[i].op = ops[i]
		}
		resp.OperationIDs = operationIDs(ops)
		if err := s.recordQueuedTask(ctx, ownerID, resp); err != nil {
			for _, op := range ops {
				s.finishOperation(ctx, op, nil, err)
			}
			httpError(w, "could not persist task record: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if validCount > 0 {
			var remaining atomic.Int64
			remaining.Store(int64(validCount))
			var mu sync.Mutex
			succeeded := make([]string, 0, validCount)
			failed := append([]api.BulkVMItemError{}, resp.Failed...)
			warnings := make([]api.BulkVMItemError, 0)

			finish := func(bgCtx context.Context, lastLabel string) {
				if remaining.Add(-1) != 0 {
					mu.Lock()
					done := len(succeeded) + len(failed)
					stage := fmt.Sprintf("Provisioning sandboxes (%d/%d finished · %s)", done, len(req.Items), lastLabel)
					s.recordTaskProgress(bgCtx, ownerID, taskID, "create", len(req.Items), succeeded, failed, warnings, stage)
					mu.Unlock()
					s.broadcastClusterUpdate()
					return
				}
				mu.Lock()
				summary := api.BulkVMsResponse{
					Action:    "create",
					Status:    "completed",
					TaskID:    taskID,
					Requested: len(req.Items),
					Succeeded: succeeded,
					Failed:    failed,
					Warnings:  warnings,
				}
				mu.Unlock()
				s.recordTaskSummary(bgCtx, ownerID, summary)
				s.recordEvent("sandbox.bulk_created", actor, fmt.Sprintf("%d sandboxes", len(summary.Succeeded)), "delivered", 80, summary)
				s.broadcastClusterUpdate()
			}

			for _, t := range targets {
				if t.resolveErr != "" {
					continue
				}
				target := t
				enqErr := s.enqueueBackendOp(func(bgCtx context.Context) {
					s.startOperation(bgCtx, target.op)
					created, err := s.executeCreateVM(bgCtx, isAdmin, target.req)
					s.finishOperation(bgCtx, target.op, created, err)
					mu.Lock()
					switch {
					case created != nil:
						succeeded = append(succeeded, created.ID)
						if err != nil {
							warnings = append(warnings, api.BulkVMItemError{ID: target.label, Error: err.Error()})
						}
						s.recordEvent("sandbox.created", actor, created.Name, "delivered", 85, map[string]interface{}{
							"vm_id":     created.ID,
							"vcpus":     created.VCPUs,
							"memory_mb": created.MemoryMB,
							"worker_id": created.WorkerID,
							"pool_id":   created.PoolID,
							"task_id":   taskID,
						})
					case err != nil:
						failed = append(failed, api.BulkVMItemError{ID: target.label, Error: err.Error()})
					}
					mu.Unlock()
					finish(bgCtx, target.label)
				})
				if enqErr != nil {
					s.finishOperation(context.Background(), target.op, nil, enqErr)
					mu.Lock()
					failed = append(failed, api.BulkVMItemError{ID: target.label, Error: enqErr.Error()})
					mu.Unlock()
					finish(context.Background(), target.label)
				}
			}
		}

		s.recordEvent("sandbox.bulk_queued", actor, fmt.Sprintf("create %d sandboxes", validCount), "queued", 0, resp)
		s.broadcastClusterUpdate()
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
		return
	}

	syncSpecs := make([]bulkOperationSpec, len(targets))
	for i, t := range targets {
		syncSpecs[i] = bulkOperationSpec{
			TargetName: t.label,
			Skip:       t.resolveErr != "",
			Request:    summarizeCreateRequest(t.req),
		}
	}
	syncOps, syncOpErr := s.prepareBulkOperations(ctx, ownerID, "sandbox.create", taskID, actor, syncSpecs)
	if syncOpErr != nil {
		httpError(w, "could not persist operations: "+syncOpErr.Error(), http.StatusInternalServerError)
		return
	}
	for i := range syncOps {
		targets[i].op = syncOps[i]
	}

	resp, _, disconnected := runDetached(r, func(bgCtx context.Context) (api.BulkVMsResponse, error) {
		out := api.BulkVMsResponse{
			Action:       "create",
			Status:       "completed",
			TaskID:       taskID,
			Requested:    len(req.Items),
			Succeeded:    []string{},
			Failed:       []api.BulkVMItemError{},
			OperationIDs: operationIDs(syncOps),
		}
		const maxWorkers = 4
		sem := make(chan struct{}, maxWorkers)
		var wg sync.WaitGroup
		var resMu sync.Mutex
		for _, t := range targets {
			if t.resolveErr != "" {
				resMu.Lock()
				out.Failed = append(out.Failed, api.BulkVMItemError{ID: t.label, Error: t.resolveErr})
				resMu.Unlock()
				continue
			}
			wg.Add(1)
			go func(target createTarget) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				s.startOperation(bgCtx, target.op)
				created, err := s.executeCreateVM(bgCtx, isAdmin, target.req)
				s.finishOperation(bgCtx, target.op, created, err)
				resMu.Lock()
				switch {
				case created != nil:
					out.Succeeded = append(out.Succeeded, created.ID)
					if err != nil {
						out.Warnings = append(out.Warnings, api.BulkVMItemError{ID: target.label, Error: err.Error()})
					}
					s.recordEvent("sandbox.created", actor, created.Name, "delivered", 85, map[string]interface{}{
						"vm_id":     created.ID,
						"vcpus":     created.VCPUs,
						"memory_mb": created.MemoryMB,
						"worker_id": created.WorkerID,
						"pool_id":   created.PoolID,
					})
				case err != nil:
					out.Failed = append(out.Failed, api.BulkVMItemError{ID: target.label, Error: err.Error()})
				}
				resMu.Unlock()
			}(t)
		}
		wg.Wait()
		s.recordEvent("sandbox.bulk_created", actor, fmt.Sprintf("%d sandboxes", len(out.Succeeded)), "delivered", 80, out)
		s.broadcastClusterUpdate()
		return out, nil
	})
	if disconnected {
		return
	}
	json.NewEncoder(w).Encode(resp)
}

// handleBulkCloneVMs handles POST /api/v1/vms/bulk-clone and /api/v1/sandboxes/bulk-clone.
func (s *Server) handleBulkCloneVMs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req BulkCloneVMsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	items := req.Items
	if len(items) == 0 && strings.TrimSpace(req.SourceID) != "" && len(req.Names) > 0 {
		items = make([]BulkCloneItem, 0, len(req.Names))
		for _, name := range req.Names {
			items = append(items, BulkCloneItem{
				SourceID: req.SourceID,
				CreateRequest: vm.CreateRequest{
					Name:             name,
					Description:      req.Description,
					VCPUs:            req.VCPUs,
					MemoryMB:         req.MemoryMB,
					DiskMB:           req.DiskMB,
					DiskGB:           req.DiskGB,
					NetworkIsolation: req.NetworkIsolation,
					NetworkIsolated:  req.NetworkIsolated,
					AutoStart:        req.AutoStart || (req.Start != nil && *req.Start),
					Start:            req.Start,
					Protected:        req.Protected,
					WorkerID:         req.WorkerID,
					PoolID:           req.PoolID,
				},
			})
		}
	}
	if len(items) == 0 {
		httpError(w, "items or (source_id and names) is required", http.StatusBadRequest)
		return
	}
	if len(items) > maxBulkRequestItems {
		httpError(w, fmt.Sprintf("too many items: %d (max %d per request)", len(items), maxBulkRequestItems), http.StatusBadRequest)
		return
	}

	var dummyReq vm.CreateRequest
	ownerID, isAdmin, ok := s.authorizeCreateRequest(w, r, &dummyReq)
	if !ok {
		return
	}
	authorizedOwners := dummyReq.AuthorizedOwnerIDs
	actor := getActor(r)
	ctx := r.Context()

	type cloneTarget struct {
		label      string
		sourceID   string
		req        vm.CreateRequest
		resolveErr string
		op         *db.Operation
	}
	targets := make([]cloneTarget, len(items))
	validCount := 0
	for i := range items {
		item := items[i]
		label := strings.TrimSpace(item.Name)
		if label == "" {
			label = item.SourceID
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.Name)), "job-") {
			targets[i] = cloneTarget{label: label, resolveErr: "sandbox name prefix 'job-' is reserved for ephemeral CI jobs"}
			continue
		}
		src, err := s.resolveAccessibleVM(ctx, r, item.SourceID)
		if err != nil {
			targets[i] = cloneTarget{label: label, resolveErr: err.Error()}
			continue
		}
		if src == nil || !s.canAccessOwner(r, src.OwnerID) {
			targets[i] = cloneTarget{label: label, resolveErr: "vm not found"}
			continue
		}
		createReq := item.CreateRequest
		if isAdmin {
			if strings.TrimSpace(createReq.OwnerID) == "" {
				createReq.OwnerID = ownerID
			}
			createReq.AllowCrossOwner = true
		} else {
			createReq.OwnerID = ownerID
			createReq.AuthorizedOwnerIDs = authorizedOwners
		}
		targets[i] = cloneTarget{
			label:    label,
			sourceID: vmIdentifier(src),
			req:      createReq,
		}
		validCount++
	}

	async := req.Async == nil || *req.Async
	taskID := "task_" + uuid.NewString()[:8]

	if async {
		resp := api.BulkVMsResponse{
			Action:    "clone",
			Status:    "queued",
			TaskID:    taskID,
			Requested: len(items),
			Queued:    validCount,
			Succeeded: []string{},
			Failed:    []api.BulkVMItemError{},
		}
		for _, t := range targets {
			if t.resolveErr != "" {
				resp.Failed = append(resp.Failed, api.BulkVMItemError{ID: t.label, Error: t.resolveErr})
			}
		}

		if validCount > 0 && s.ensureOpQueue().wouldExceed(validCount) {
			httpError(w, errBackendQueueFull.Error(), http.StatusTooManyRequests)
			return
		}
		// Persist every queued clone before dispatch so the user can see what is
		// pending and the audit trail exists before execution starts.
		specs := make([]bulkOperationSpec, len(targets))
		for i, t := range targets {
			request := summarizeCreateRequest(t.req)
			request["source_id"] = t.sourceID
			specs[i] = bulkOperationSpec{
				TargetName: t.label,
				Skip:       t.resolveErr != "",
				Request:    request,
			}
		}
		ops, opErr := s.prepareBulkOperations(ctx, ownerID, "sandbox.clone", taskID, actor, specs)
		if opErr != nil {
			httpError(w, "could not persist queued operations: "+opErr.Error(), http.StatusInternalServerError)
			return
		}
		for i := range ops {
			targets[i].op = ops[i]
		}
		resp.OperationIDs = operationIDs(ops)
		if err := s.recordQueuedTask(ctx, ownerID, resp); err != nil {
			for _, op := range ops {
				s.finishOperation(ctx, op, nil, err)
			}
			httpError(w, "could not persist task record: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if validCount > 0 {
			var remaining atomic.Int64
			remaining.Store(int64(validCount))
			var mu sync.Mutex
			succeeded := make([]string, 0, validCount)
			failed := append([]api.BulkVMItemError{}, resp.Failed...)

			finish := func(bgCtx context.Context, lastLabel string) {
				if remaining.Add(-1) != 0 {
					mu.Lock()
					done := len(succeeded) + len(failed)
					stage := fmt.Sprintf("Cloning sandboxes (%d/%d finished · %s)", done, len(items), lastLabel)
					s.recordTaskProgress(bgCtx, ownerID, taskID, "clone", len(items), succeeded, failed, nil, stage)
					mu.Unlock()
					s.broadcastClusterUpdate()
					return
				}
				mu.Lock()
				summary := api.BulkVMsResponse{
					Action:    "clone",
					Status:    "completed",
					TaskID:    taskID,
					Requested: len(items),
					Succeeded: succeeded,
					Failed:    failed,
				}
				mu.Unlock()
				s.recordTaskSummary(bgCtx, ownerID, summary)
				s.recordEvent("sandbox.bulk_cloned", actor, fmt.Sprintf("%d sandboxes", len(summary.Succeeded)), "delivered", 60, summary)
				s.broadcastClusterUpdate()
			}

			for _, t := range targets {
				if t.resolveErr != "" {
					continue
				}
				target := t
				enqErr := s.enqueueBackendOp(func(bgCtx context.Context) {
					s.startOperation(bgCtx, target.op)
					cloned, err := s.executeCloneVM(bgCtx, isAdmin, target.sourceID, target.req)
					s.finishOperation(bgCtx, target.op, cloned, err)
					mu.Lock()
					switch {
					case cloned != nil:
						succeeded = append(succeeded, cloned.ID)
						s.recordEvent("sandbox.cloned", actor, cloned.Name, "delivered", 60, map[string]string{
							"source_id": target.sourceID,
							"cloned_id": cloned.ID,
							"task_id":   taskID,
						})
					case err != nil:
						failed = append(failed, api.BulkVMItemError{ID: target.label, Error: err.Error()})
					}
					mu.Unlock()
					finish(bgCtx, target.label)
				})
				if enqErr != nil {
					s.finishOperation(context.Background(), target.op, nil, enqErr)
					mu.Lock()
					failed = append(failed, api.BulkVMItemError{ID: target.label, Error: enqErr.Error()})
					mu.Unlock()
					finish(context.Background(), target.label)
				}
			}
		}

		s.recordEvent("sandbox.bulk_queued", actor, fmt.Sprintf("clone %d sandboxes", validCount), "queued", 0, resp)
		s.broadcastClusterUpdate()
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
		return
	}

	syncSpecs := make([]bulkOperationSpec, len(targets))
	for i, t := range targets {
		request := summarizeCreateRequest(t.req)
		request["source_id"] = t.sourceID
		syncSpecs[i] = bulkOperationSpec{
			TargetName: t.label,
			Skip:       t.resolveErr != "",
			Request:    request,
		}
	}
	syncOps, syncOpErr := s.prepareBulkOperations(ctx, ownerID, "sandbox.clone", taskID, actor, syncSpecs)
	if syncOpErr != nil {
		httpError(w, "could not persist operations: "+syncOpErr.Error(), http.StatusInternalServerError)
		return
	}
	for i := range syncOps {
		targets[i].op = syncOps[i]
	}

	resp, _, disconnected := runDetached(r, func(bgCtx context.Context) (api.BulkVMsResponse, error) {
		out := api.BulkVMsResponse{
			Action:       "clone",
			Status:       "completed",
			TaskID:       taskID,
			Requested:    len(items),
			Succeeded:    []string{},
			Failed:       []api.BulkVMItemError{},
			OperationIDs: operationIDs(syncOps),
		}
		const maxWorkers = 4
		sem := make(chan struct{}, maxWorkers)
		var wg sync.WaitGroup
		var resMu sync.Mutex
		for _, t := range targets {
			if t.resolveErr != "" {
				resMu.Lock()
				out.Failed = append(out.Failed, api.BulkVMItemError{ID: t.label, Error: t.resolveErr})
				resMu.Unlock()
				continue
			}
			wg.Add(1)
			go func(target cloneTarget) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				s.startOperation(bgCtx, target.op)
				cloned, err := s.executeCloneVM(bgCtx, isAdmin, target.sourceID, target.req)
				s.finishOperation(bgCtx, target.op, cloned, err)
				resMu.Lock()
				switch {
				case cloned != nil:
					out.Succeeded = append(out.Succeeded, cloned.ID)
					if err != nil {
						out.Warnings = append(out.Warnings, api.BulkVMItemError{ID: target.label, Error: err.Error()})
					}
					s.recordEvent("sandbox.cloned", actor, cloned.Name, "delivered", 60, map[string]string{
						"source_id": target.sourceID,
						"cloned_id": cloned.ID,
					})
				case err != nil:
					out.Failed = append(out.Failed, api.BulkVMItemError{ID: target.label, Error: err.Error()})
				}
				resMu.Unlock()
			}(t)
		}
		wg.Wait()
		s.recordEvent("sandbox.bulk_cloned", actor, fmt.Sprintf("%d sandboxes", len(out.Succeeded)), "delivered", 60, out)
		s.broadcastClusterUpdate()
		return out, nil
	})
	if disconnected {
		return
	}
	json.NewEncoder(w).Encode(resp)
}
