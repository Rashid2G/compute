package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/scheduler"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/Rashid2G/compute/pkg/api"
	"github.com/google/uuid"
)

const backendOpQueueWorkers = 4

// serverOpQueue is a bounded-concurrency, unbounded-FIFO backend execution
// queue so bulk and long-running requests accepted from HTTP handlers keep
// executing cleanly in order even if the browser disconnects or refreshes.
type serverOpQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []func()
	active int
}

func newServerOpQueue(workers int) *serverOpQueue {
	q := &serverOpQueue{}
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

func (q *serverOpQueue) enqueue(fn func()) {
	q.mu.Lock()
	q.items = append(q.items, fn)
	q.cond.Signal()
	q.mu.Unlock()
}

func (q *serverOpQueue) pendingCount() int {
	q.mu.Lock()
	n := len(q.items) + q.active
	q.mu.Unlock()
	return n
}

func (s *Server) ensureOpQueue() *serverOpQueue {
	s.opQueueOnce.Do(func() {
		s.opQueue = newServerOpQueue(backendOpQueueWorkers)
	})
	return s.opQueue
}

func (s *Server) enqueueBackendOp(fn func(context.Context)) {
	q := s.ensureOpQueue()
	q.enqueue(func() {
		fn(context.Background())
	})
}

func (s *Server) backendOpPending() int {
	if s.opQueue == nil {
		return 0
	}
	return s.opQueue.pendingCount()
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

// runDetached executes fn with context.Background() so an in-flight request
// continues to completion even if the HTTP client disconnects or refreshes.
// If the client stays connected, its result is returned directly.
func runDetached[T any](r *http.Request, fn func(context.Context) (T, error)) (T, error, bool) {
	type result struct {
		val T
		err error
	}
	ch := make(chan result, 1)
	go func() {
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
	Protected        *bool           `json:"protected,omitempty"`
	WorkerID         string          `json:"worker_id,omitempty"`
	PoolID           string          `json:"pool_id,omitempty"`
	Async            *bool           `json:"async,omitempty"`
}

func (s *Server) validateCreateItem(ctx context.Context, r *http.Request, ownerID string, isAdmin bool, authorizedOwners []string, req *vm.CreateRequest) error {
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

func (s *Server) executeCreateVM(ctx context.Context, isAdmin bool, req vm.CreateRequest) (*db.VM, error) {
	vcpus, memoryMB, diskMB := s.estimateRequestResources(ctx, req)
	if err := s.enforceCreateQuotaWithAdmin(ctx, isAdmin, req.OwnerID, vcpus, memoryMB, diskMB); err != nil {
		return nil, &httpStatusError{code: http.StatusTooManyRequests, err: err}
	}

	s.createPlaceMu.Lock()
	var selectedNode *db.WorkerNode
	if s.vmMgr != nil && s.vmMgr.GetStore() != nil {
		workers, localNode, policy, pErr := s.buildPlacementNodes(ctx)
		if pErr == nil && (len(workers) > 0 || localNode != nil) {
			if len(workers) == 0 && req.WorkerID == "" && (req.PoolID == "" || (localNode != nil && localNode.PoolID == req.PoolID)) {
				if localNode != nil && localNode.Status != "ready" {
					s.createPlaceMu.Unlock()
					return nil, &httpStatusError{
						code: http.StatusBadRequest,
						err:  fmt.Errorf("worker %q is not ready (status: %s)", localNode.ID, localNode.Status),
					}
				}
				selectedNode = localNode
			} else {
				chosen, selErr := scheduler.SelectNodeForSandbox(ctx, workers, localNode, scheduler.SandboxPlacementRequest{
					WorkerID: req.WorkerID,
					PoolID:   req.PoolID,
					VCPUs:    vcpus,
					MemoryMB: memoryMB,
					DiskMB:   diskMB,
				}, policy)
				if selErr != nil {
					s.createPlaceMu.Unlock()
					var rle *api.ResourceLimitError
					if errors.As(selErr, &rle) {
						return nil, selErr
					}
					if strings.Contains(selErr.Error(), "not found") {
						return nil, &httpStatusError{code: http.StatusNotFound, err: selErr}
					}
					return nil, &httpStatusError{code: http.StatusBadRequest, err: selErr}
				}
				selectedNode = chosen
			}
		}
	}

	var created *db.VM
	var err error
	if selectedNode != nil && selectedNode.ID != "nod_host_local" {
		created, err = s.dispatchRemoteVMCreate(ctx, req, selectedNode)
		s.createPlaceMu.Unlock()
	} else {
		if selectedNode != nil {
			req.WorkerID = selectedNode.ID
			if req.PoolID == "" && selectedNode.PoolID != "" {
				req.PoolID = selectedNode.PoolID
			}
		}
		created, err = s.vmMgr.CreateVM(ctx, req)
		s.createPlaceMu.Unlock()
		if err == nil && req.AutoStart {
			if qErr := s.enforceRunQuotaWithAdmin(ctx, isAdmin, created); qErr == nil {
				if runErr := s.vmMgr.RunVM(ctx, created.ID); runErr == nil {
					if started, gErr := s.vmMgr.GetVM(ctx, created.ID); gErr == nil && started != nil {
						created = started
						if created.Status == db.StatusRunning {
							s.mountAttachedVolumes(ctx, created)
							s.applyEnvironment(ctx, created)
							s.applyPortForwardsForVM(ctx, created)
						}
					}
				} else {
					log.Printf("[computed] create %s auto_start skipped/failed: %v", created.ID, runErr)
				}
			}
		}
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
	s.broadcastClusterUpdate()
	return created, nil
}

func (s *Server) executeCloneVM(ctx context.Context, isAdmin bool, sourceVMID string, req vm.CreateRequest) (*db.VM, error) {
	vcpus, memoryMB, diskMB := s.estimateRequestResources(ctx, req)
	if err := s.enforceCreateQuotaWithAdmin(ctx, isAdmin, req.OwnerID, vcpus, memoryMB, diskMB); err != nil {
		return nil, &httpStatusError{code: http.StatusTooManyRequests, err: err}
	}
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
	if req.AutoStart {
		if qErr := s.enforceRunQuotaWithAdmin(ctx, isAdmin, cloned); qErr != nil {
			return nil, &httpStatusError{code: http.StatusTooManyRequests, err: qErr}
		}
		if runErr := s.vmMgr.RunVM(ctx, cloned.ID); runErr != nil {
			log.Printf("[computed] clone %s auto_start skipped/failed: %v", cloned.ID, runErr)
		} else if started, getErr := s.vmMgr.GetVM(ctx, cloned.ID); getErr == nil && started != nil {
			cloned = started
			if cloned.Status == db.StatusRunning {
				s.mountAttachedVolumes(ctx, cloned)
				s.applyEnvironment(ctx, cloned)
				s.applyPortForwardsForVM(ctx, cloned)
			}
		}
	}
	s.broadcastClusterUpdate()
	return cloned, nil
}

// restartSingleVM restarts one VM. When waitSSH is true it blocks until the
// guest SSH port answers (so a synchronous caller returns a usable VM);
// background queue workers pass false to avoid occupying a worker for the
// full SSH wait timeout.
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
		if waitSSH {
			s.waitGuestSSH(ctx, v, 30*time.Second)
		}
		s.mountAttachedVolumes(ctx, v)
		s.applyEnvironment(ctx, v)
		s.applyPortForwardsForVM(ctx, v)
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
		}
		for _, t := range targets {
			if t.resolveErr != "" {
				resp.Failed = append(resp.Failed, api.BulkVMItemError{ID: t.label, Error: t.resolveErr})
			}
		}

		if validCount > 0 {
			var remaining atomic.Int64
			remaining.Store(int64(validCount))
			var mu sync.Mutex
			succeeded := make([]string, 0, validCount)
			failed := append([]api.BulkVMItemError{}, resp.Failed...)

			for _, t := range targets {
				if t.resolveErr != "" {
					continue
				}
				target := t
				s.enqueueBackendOp(func(bgCtx context.Context) {
					created, err := s.executeCreateVM(bgCtx, isAdmin, target.req)
					mu.Lock()
					if err != nil {
						failed = append(failed, api.BulkVMItemError{ID: target.label, Error: err.Error()})
					} else if created != nil {
						succeeded = append(succeeded, created.ID)
						s.recordEvent("sandbox.created", actor, created.Name, "delivered", 85, map[string]interface{}{
							"vm_id":     created.ID,
							"vcpus":     created.VCPUs,
							"memory_mb": created.MemoryMB,
							"worker_id": created.WorkerID,
							"pool_id":   created.PoolID,
							"task_id":   taskID,
						})
					}
					mu.Unlock()
					if remaining.Add(-1) == 0 {
						mu.Lock()
						summary := api.BulkVMsResponse{
							Action:    "create",
							Status:    "completed",
							TaskID:    taskID,
							Requested: len(req.Items),
							Succeeded: succeeded,
							Failed:    failed,
						}
						mu.Unlock()
						s.recordEvent("sandbox.bulk_created", actor, fmt.Sprintf("%d sandboxes", len(summary.Succeeded)), "delivered", 80, summary)
						s.broadcastClusterUpdate()
					}
				})
			}
		}

		s.recordEvent("sandbox.bulk_queued", actor, fmt.Sprintf("create %d sandboxes", validCount), "queued", 0, resp)
		s.broadcastClusterUpdate()
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
		return
	}

	resp, _, disconnected := runDetached(r, func(bgCtx context.Context) (api.BulkVMsResponse, error) {
		out := api.BulkVMsResponse{
			Action:    "create",
			Status:    "completed",
			TaskID:    taskID,
			Requested: len(req.Items),
			Succeeded: []string{},
			Failed:    []api.BulkVMItemError{},
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
				created, err := s.executeCreateVM(bgCtx, isAdmin, target.req)
				resMu.Lock()
				if err != nil {
					out.Failed = append(out.Failed, api.BulkVMItemError{ID: target.label, Error: err.Error()})
				} else if created != nil {
					out.Succeeded = append(out.Succeeded, created.ID)
					s.recordEvent("sandbox.created", actor, created.Name, "delivered", 85, map[string]interface{}{
						"vm_id":     created.ID,
						"vcpus":     created.VCPUs,
						"memory_mb": created.MemoryMB,
						"worker_id": created.WorkerID,
						"pool_id":   created.PoolID,
					})
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
					AutoStart:        req.AutoStart,
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

		if validCount > 0 {
			var remaining atomic.Int64
			remaining.Store(int64(validCount))
			var mu sync.Mutex
			succeeded := make([]string, 0, validCount)
			failed := append([]api.BulkVMItemError{}, resp.Failed...)

			for _, t := range targets {
				if t.resolveErr != "" {
					continue
				}
				target := t
				s.enqueueBackendOp(func(bgCtx context.Context) {
					cloned, err := s.executeCloneVM(bgCtx, isAdmin, target.sourceID, target.req)
					mu.Lock()
					if err != nil {
						failed = append(failed, api.BulkVMItemError{ID: target.label, Error: err.Error()})
					} else if cloned != nil {
						succeeded = append(succeeded, cloned.ID)
						s.recordEvent("sandbox.cloned", actor, cloned.Name, "delivered", 60, map[string]string{
							"source_id": target.sourceID,
							"cloned_id": cloned.ID,
							"task_id":   taskID,
						})
					}
					mu.Unlock()
					if remaining.Add(-1) == 0 {
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
						s.recordEvent("sandbox.bulk_cloned", actor, fmt.Sprintf("%d sandboxes", len(summary.Succeeded)), "delivered", 60, summary)
						s.broadcastClusterUpdate()
					}
				})
			}
		}

		s.recordEvent("sandbox.bulk_queued", actor, fmt.Sprintf("clone %d sandboxes", validCount), "queued", 0, resp)
		s.broadcastClusterUpdate()
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
		return
	}

	resp, _, disconnected := runDetached(r, func(bgCtx context.Context) (api.BulkVMsResponse, error) {
		out := api.BulkVMsResponse{
			Action:    "clone",
			Status:    "completed",
			TaskID:    taskID,
			Requested: len(items),
			Succeeded: []string{},
			Failed:    []api.BulkVMItemError{},
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
				cloned, err := s.executeCloneVM(bgCtx, isAdmin, target.sourceID, target.req)
				resMu.Lock()
				if err != nil {
					out.Failed = append(out.Failed, api.BulkVMItemError{ID: target.label, Error: err.Error()})
				} else if cloned != nil {
					out.Succeeded = append(out.Succeeded, cloned.ID)
					s.recordEvent("sandbox.cloned", actor, cloned.Name, "delivered", 60, map[string]string{
						"source_id": target.sourceID,
						"cloned_id": cloned.ID,
					})
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
