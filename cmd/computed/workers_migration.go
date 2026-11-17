package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
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

const migrationChunkBytes = 4 * 1024 * 1024 // 4 MiB

// buildPlacementNodes gathers live enriched worker nodes, the local node, and cluster scheduling policy.
// Disconnected remote nodes are marked "offline" in the returned slice without mutating DB status.
func (s *Server) buildPlacementNodes(ctx context.Context) ([]*db.WorkerNode, *db.WorkerNode, db.ClusterSchedulingPolicy, error) {
	policy := db.DefaultSchedulingPolicy()
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		return nil, nil, policy, fmt.Errorf("store not available")
	}
	st := s.vmMgr.GetStore()
	if p, err := st.GetSchedulingPolicy(ctx); err == nil {
		policy = p
	}

	// Refresh nod_host_local metrics/limits in DB before listing.
	if localNode, err := st.GetWorkerNode(ctx, "nod_host_local"); err == nil && localNode != nil {
		now := time.Now().UTC()
		localNode.LastHeartbeat = &now
		limits := s.vmMgr.GetResourceLimits()
		physVCPUs, physMemMB, physDiskMB := getHostResources(0, 0, 0)
		vcpus, memMB, diskMB := getHostResources(limits.MaxTotalVCPUs, limits.MaxTotalMemoryMB, int(limits.MaxTotalDiskMB))
		localNode.TotalVCPUs = vcpus
		localNode.TotalMemMB = memMB
		localNode.TotalDiskMB = diskMB
		localNode.PhysicalVCPUs = physVCPUs
		localNode.PhysicalMemMB = physMemMB
		localNode.PhysicalDiskMB = physDiskMB
		if limits.MaxTotalVCPUs > 0 {
			localNode.AllocatedVCPUs = limits.MaxTotalVCPUs
		}
		if limits.MaxTotalMemoryMB > 0 {
			localNode.AllocatedMemMB = limits.MaxTotalMemoryMB
		}
		if limits.MaxTotalDiskMB > 0 {
			localNode.AllocatedDiskMB = int(limits.MaxTotalDiskMB)
		}
		_ = st.SaveWorkerNode(ctx, localNode)
	}

	nodes, err := st.ListWorkerNodes(ctx)
	if err != nil {
		return nil, nil, policy, err
	}
	s.enrichWorkerNodes(nodes)

	var localNode *db.WorkerNode
	var workers []*db.WorkerNode
	for _, n := range nodes {
		if n == nil {
			continue
		}
		cp := *n
		if cp.ID == "nod_host_local" {
			localNode = &cp
			continue
		}
		if s.workers != nil {
			if _, connected := s.workers.Get(cp.ID); !connected {
				cp.Status = "offline"
			}
		}
		workers = append(workers, &cp)
	}
	return workers, localNode, policy, nil
}

// resolveCreateSSHKeys resolves explicit SSH key and owner-wide global keys for remote worker VM creation.
func (s *Server) resolveCreateSSHKeys(ctx context.Context, req vm.CreateRequest) (string, []string, error) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		return req.SSHPublicKey, nil, nil
	}
	st := s.vmMgr.GetStore()
	explicitPubKey := strings.TrimSpace(req.SSHPublicKey)
	if explicitPubKey == "" && req.SSHKeyID != "" {
		var key *db.SSHKey
		var err error
		if req.AllowCrossOwner {
			key, err = st.GetSSHKey(ctx, req.SSHKeyID)
		} else {
			key, err = st.GetSSHKeyByOwner(ctx, req.OwnerID, req.SSHKeyID)
			if key == nil {
				for _, oid := range req.AuthorizedOwnerIDs {
					if oid != "" && oid != req.OwnerID {
						if k, _ := st.GetSSHKeyByOwner(ctx, oid, req.SSHKeyID); k != nil {
							key = k
							break
						}
					}
				}
			}
		}
		if err != nil {
			return "", nil, err
		}
		if key == nil {
			return "", nil, fmt.Errorf("ssh key not found: %s", req.SSHKeyID)
		}
		explicitPubKey = strings.TrimSpace(key.PublicKey)
	}
	globalKeys, err := s.vmMgr.GlobalSSHKeys(ctx, req.OwnerID)
	if err != nil {
		return explicitPubKey, nil, err
	}
	return explicitPubKey, globalKeys, nil
}

// dispatchRemoteVMCreate sends vm.create RPC to targetNode and mirrors the resulting VM record locally.
func (s *Server) dispatchRemoteVMCreate(ctx context.Context, req vm.CreateRequest, targetNode *db.WorkerNode) (*db.VM, error) {
	if s.workers == nil {
		return nil, fmt.Errorf("worker registry not available")
	}
	explicitPubKey, globalKeys, err := s.resolveCreateSSHKeys(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(req.AuthorizedSSHKeys) > 0 {
		globalKeys = append(globalKeys, req.AuthorizedSSHKeys...)
	}

	poolID := strings.TrimSpace(req.PoolID)
	if poolID == "" {
		poolID = targetNode.PoolID
	}
	if poolID == "" {
		poolID = "default"
	}

	rpcReq := api.WorkerVMCreateRequest{
		Name:              req.Name,
		OwnerID:           req.OwnerID,
		Tier:              req.Tier,
		VCPUs:             req.VCPUs,
		MemoryMB:          req.MemoryMB,
		DiskMB:            req.DiskMB,
		DiskGB:            req.DiskGB,
		Description:       req.Description,
		AutoStart:         req.AutoStart,
		Image:             req.Image,
		Template:          req.Template,
		IPAddress:         req.IPAddress,
		MACAddress:        req.MACAddress,
		PublicIP:          req.PublicIP,
		PublicIPEnabled:   req.PublicIPEnabled,
		DedicatedIP:       req.DedicatedIP,
		NetworkIsolation:  req.NetworkIsolation,
		Isolated:          req.Isolated,
		NetworkIsolated:   req.NetworkIsolated,
		DHCP:              req.DHCP,
		SSHPublicKey:      explicitPubKey,
		AuthorizedSSHKeys: globalKeys,
		SSHPassword:       req.SSHPassword,
		Protected:         req.Protected,
		PoolID:            poolID,
		WorkerID:          targetNode.ID,
	}

	var remoteVM db.VM
	if err := s.workers.CallRPC(ctx, targetNode.ID, "vm.create", rpcReq, &remoteVM); err != nil {
		return nil, err
	}
	remoteVM.WorkerID = targetNode.ID
	remoteVM.PoolID = poolID
	if remoteVM.InternalID == "" {
		remoteVM.InternalID = remoteVM.ID
	}
	if err := s.vmMgr.SaveVMRecord(ctx, &remoteVM); err != nil {
		// Clean up on worker if DB persistence fails.
		_ = s.workers.CallRPC(context.Background(), targetNode.ID, "vm.delete", api.WorkerVMIDRequest{VMID: remoteVM.ID}, nil)
		return nil, err
	}
	return &remoteVM, nil
}

// syncRemoteVMRecord updates the control-plane DB record from a worker's returned VM state while preserving WorkerID.
func (s *Server) syncRemoteVMRecord(ctx context.Context, local *db.VM, remote *db.VM) (*db.VM, error) {
	if local == nil {
		return nil, fmt.Errorf("local vm record is nil")
	}
	if remote != nil {
		workerID := local.WorkerID
		poolID := local.PoolID
		local.Status = remote.Status
		local.DesiredRunning = remote.DesiredRunning
		local.IPAddress = remote.IPAddress
		local.PublicIP = remote.PublicIP
		local.PublicIPEnabled = remote.PublicIPEnabled
		local.MACAddress = remote.MACAddress
		local.TAPDevice = remote.TAPDevice
		local.PID = remote.PID
		local.DiskPath = remote.DiskPath
		local.SnapshotPath = remote.SnapshotPath
		local.MemFilePath = remote.MemFilePath
		local.UpdatedAt = time.Now().UTC()
		if workerID != "" {
			local.WorkerID = workerID
		}
		if poolID != "" {
			local.PoolID = poolID
		}
		if err := s.vmMgr.SaveVMRecord(ctx, local); err != nil {
			return local, err
		}
	}
	return local, nil
}

func (s *Server) remoteVMTargetID(v *db.VM) string {
	if v == nil {
		return ""
	}
	if v.InternalID != "" {
		return v.InternalID
	}
	return v.ID
}

func (s *Server) callRemoteVMLifecycle(ctx context.Context, v *db.VM, method string) (*db.VM, error) {
	if s.workers == nil {
		return nil, fmt.Errorf("worker registry not available")
	}
	var remote db.VM
	if err := s.workers.CallRPC(ctx, v.WorkerID, method, api.WorkerVMIDRequest{VMID: s.remoteVMTargetID(v)}, &remote); err != nil {
		return nil, err
	}
	return s.syncRemoteVMRecord(ctx, v, &remote)
}

func (s *Server) syncRemoteVMSSHKeys(ctx context.Context, v *db.VM) error {
	if s.workers == nil || s.vmMgr == nil {
		return fmt.Errorf("worker registry not available")
	}
	keys, err := s.vmMgr.GlobalSSHKeys(ctx, v.OwnerID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(v.SSHPublicKey) != "" {
		keys = append(keys, strings.TrimSpace(v.SSHPublicKey))
	}
	return s.workers.CallRPC(ctx, v.WorkerID, "vm.ssh_sync", api.WorkerVMSSHSyncRequest{
		VMID:       s.remoteVMTargetID(v),
		PublicKeys: keys,
	}, nil)
}

func (s *Server) dialVMAddress(ctx context.Context, v *db.VM, networkName, addr string) (net.Conn, error) {
	if vm.IsRemoteVM(v) {
		if s.workers == nil {
			return nil, fmt.Errorf("worker registry not available")
		}
		return s.workers.OpenTunnel(ctx, v.WorkerID, addr)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return dialer.DialContext(ctx, networkName, addr)
}

// handleWorkerUpdate handles PATCH/PUT /api/v1/workers/:id.
func (s *Server) handleWorkerUpdate(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	node, err := st.GetWorkerNode(ctx, id)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if node == nil {
		httpError(w, "worker not found", http.StatusNotFound)
		return
	}
	s.enrichWorkerNodes([]*db.WorkerNode{node})

	var req api.UpdateWorkerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.AllocatedVCPUs != nil {
		if *req.AllocatedVCPUs < 0 {
			httpError(w, "allocated_vcpus must be >= 0", http.StatusBadRequest)
			return
		}
		if node.PhysicalVCPUs > 0 && *req.AllocatedVCPUs > node.PhysicalVCPUs {
			httpError(w, fmt.Sprintf("allocated_vcpus (%d) cannot exceed physical_vcpus (%d)", *req.AllocatedVCPUs, node.PhysicalVCPUs), http.StatusBadRequest)
			return
		}
		node.AllocatedVCPUs = *req.AllocatedVCPUs
		if *req.AllocatedVCPUs > 0 {
			node.TotalVCPUs = *req.AllocatedVCPUs
		}
	}
	if req.AllocatedMemMB != nil {
		if *req.AllocatedMemMB < 0 {
			httpError(w, "allocated_mem_mb must be >= 0", http.StatusBadRequest)
			return
		}
		if node.PhysicalMemMB > 0 && *req.AllocatedMemMB > node.PhysicalMemMB {
			httpError(w, fmt.Sprintf("allocated_mem_mb (%d) cannot exceed physical_mem_mb (%d)", *req.AllocatedMemMB, node.PhysicalMemMB), http.StatusBadRequest)
			return
		}
		node.AllocatedMemMB = *req.AllocatedMemMB
		if *req.AllocatedMemMB > 0 {
			node.TotalMemMB = *req.AllocatedMemMB
		}
	}
	if req.AllocatedDiskMB != nil {
		if *req.AllocatedDiskMB < 0 {
			httpError(w, "allocated_disk_mb must be >= 0", http.StatusBadRequest)
			return
		}
		if node.PhysicalDiskMB > 0 && *req.AllocatedDiskMB > node.PhysicalDiskMB {
			httpError(w, fmt.Sprintf("allocated_disk_mb (%d) cannot exceed physical_disk_mb (%d)", *req.AllocatedDiskMB, node.PhysicalDiskMB), http.StatusBadRequest)
			return
		}
		node.AllocatedDiskMB = *req.AllocatedDiskMB
		if *req.AllocatedDiskMB > 0 {
			node.TotalDiskMB = *req.AllocatedDiskMB
		}
	}
	if req.PoolID != nil {
		pool := strings.TrimSpace(*req.PoolID)
		if pool == "" {
			httpError(w, "pool_id cannot be empty", http.StatusBadRequest)
			return
		}
		node.PoolID = pool
		if s.workers != nil {
			if live, ok := s.workers.Get(node.ID); ok {
				live.PoolID = pool
			}
		}
	}
	if req.Labels != nil {
		b, err := json.Marshal(*req.Labels)
		if err != nil {
			httpError(w, "invalid labels: "+err.Error(), http.StatusBadRequest)
			return
		}
		node.LabelsJSON = string(b)
		if s.workers != nil {
			if live, ok := s.workers.Get(node.ID); ok {
				live.Labels = *req.Labels
			}
		}
	}
	if req.Status != nil {
		stVal := strings.ToLower(strings.TrimSpace(*req.Status))
		switch stVal {
		case "ready", "cordoned", "draining", "offline":
			node.Status = stVal
			if s.workers != nil {
				if live, ok := s.workers.Get(node.ID); ok {
					live.Status = stVal
				}
			}
		default:
			httpError(w, fmt.Sprintf("invalid status %q (allowed: ready, cordoned, draining, offline)", stVal), http.StatusBadRequest)
			return
		}
	}

	if err := st.SaveWorkerNode(ctx, node); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Sync nod_host_local limits directly into the local VM manager so enforcement updates immediately.
	if node.ID == "nod_host_local" && s.vmMgr != nil {
		limits := s.vmMgr.GetResourceLimits()
		if req.AllocatedVCPUs != nil {
			limits.MaxTotalVCPUs = *req.AllocatedVCPUs
		}
		if req.AllocatedMemMB != nil {
			limits.MaxTotalMemoryMB = *req.AllocatedMemMB
		}
		if req.AllocatedDiskMB != nil {
			limits.MaxTotalDiskMB = int64(*req.AllocatedDiskMB)
		}
		s.vmMgr.SetResourceLimits(limits)
	}

	s.enrichWorkerNodes([]*db.WorkerNode{node})
	s.recordEvent("worker.updated", getActor(r), node.ID, "delivered", 15, req)
	json.NewEncoder(w).Encode(node)
}

// handleSchedulerPolicy handles GET/PUT /api/v1/scheduler/policy.
func (s *Server) handleSchedulerPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAccess(w, r) {
		return
	}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	switch r.Method {
	case http.MethodGet:
		policy, err := st.GetSchedulingPolicy(ctx)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(policy)

	case http.MethodPut, http.MethodPatch:
		policy, err := st.GetSchedulingPolicy(ctx)
		if err != nil {
			policy = db.DefaultSchedulingPolicy()
		}
		if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
			httpError(w, "invalid scheduling policy payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		policy.Strategy = strings.ToLower(strings.TrimSpace(policy.Strategy))
		if policy.Strategy == "" {
			policy.Strategy = "least_loaded"
		}
		switch policy.Strategy {
		case "least_loaded", "round_robin", "bin_pack", "spread":
		default:
			httpError(w, fmt.Sprintf("invalid strategy %q (allowed: least_loaded, round_robin, bin_pack, spread)", policy.Strategy), http.StatusBadRequest)
			return
		}
		if policy.CPUSoftOvercommitRatio < 1.0 {
			httpError(w, "cpu_soft_overcommit_ratio must be >= 1.0", http.StatusBadRequest)
			return
		}
		if policy.CPUHardOvercommitRatio < policy.CPUSoftOvercommitRatio {
			httpError(w, "cpu_hard_overcommit_ratio must be >= cpu_soft_overcommit_ratio", http.StatusBadRequest)
			return
		}
		if policy.CPUWeight < 0 || policy.MemoryWeight < 0 || policy.DiskWeight < 0 {
			httpError(w, "strategy weights must be >= 0", http.StatusBadRequest)
			return
		}
		if policy.HostReservedVCPUs < 0 || policy.HostReservedMemoryMB < 0 || policy.HostReservedDiskMB < 0 {
			httpError(w, "host reserved values must be >= 0", http.StatusBadRequest)
			return
		}
		if err := st.SaveSchedulingPolicy(ctx, policy); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("scheduler.policy_updated", getActor(r), "cluster", "delivered", 15, policy)
		json.NewEncoder(w).Encode(policy)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleVMMigrate handles POST /api/v1/vms/:id/migrate.
func (s *Server) handleVMMigrate(w http.ResponseWriter, r *http.Request, v *db.VM) {
	if !s.requireAdminAccess(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req api.MigrateVMRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	actor := getActor(r)
	migrated, err, disconnected := runDetached(r, func(bgCtx context.Context) (*db.VM, error) {
		m, mErr := s.migrateSingleVM(bgCtx, v, req)
		if mErr == nil && m != nil {
			s.recordEvent("sandbox.migrated", actor, m.Name, "delivered", 100, map[string]interface{}{
				"vm_id":     m.ID,
				"worker_id": m.WorkerID,
				"pool_id":   m.PoolID,
			})
			s.broadcastClusterUpdate()
		}
		return m, mErr
	})
	if disconnected {
		return
	}
	if err != nil {
		if writeResourceLimitError(w, r, err) {
			return
		}
		var conflictErr *migrationConflictError
		if errors.As(err, &conflictErr) {
			httpError(w, conflictErr.Error(), http.StatusConflict)
			return
		}
		if strings.Contains(err.Error(), "not found") {
			httpError(w, err.Error(), http.StatusNotFound)
			return
		}
		if strings.Contains(err.Error(), "already on") || strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "not ready") {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.encodeVM(w, migrated)
}

type migrationConflictError struct {
	msg string
}

func (e *migrationConflictError) Error() string { return e.msg }

func (s *Server) migrateSingleVM(ctx context.Context, v *db.VM, req api.MigrateVMRequest) (*db.VM, error) {
	if v == nil {
		return nil, fmt.Errorf("vm not found")
	}
	sourceWorkerID := v.WorkerID
	if sourceWorkerID == "" {
		sourceWorkerID = "nod_host_local"
	}
	if req.TargetWorkerID != "" && req.TargetWorkerID == sourceWorkerID {
		return nil, fmt.Errorf("sandbox %s is already on worker %s", v.Name, sourceWorkerID)
	}

	if v.Status == db.StatusRunning && !req.Force {
		return nil, &migrationConflictError{msg: fmt.Sprintf("sandbox %s is currently running; stop/snapshot it first or pass force=true", v.Name)}
	}

	workers, localNode, policy, err := s.buildPlacementNodes(ctx)
	if err != nil {
		return nil, err
	}
	// Exclude the source node when choosing an automatic target.
	var filteredWorkers []*db.WorkerNode
	for _, n := range workers {
		if n.ID == sourceWorkerID {
			continue
		}
		filteredWorkers = append(filteredWorkers, n)
	}
	var filteredLocal *db.WorkerNode
	if localNode != nil && localNode.ID != sourceWorkerID {
		filteredLocal = localNode
	}

	targetNode, err := scheduler.SelectNodeForSandbox(ctx, filteredWorkers, filteredLocal, scheduler.SandboxPlacementRequest{
		WorkerID: req.TargetWorkerID,
		PoolID:   req.TargetPoolID,
		VCPUs:    v.VCPUs,
		MemoryMB: v.MemoryMB,
		DiskMB:   v.DiskMB,
	}, policy)
	if err != nil {
		return nil, err
	}

	wasRunning := v.Status == db.StatusRunning
	if wasRunning {
		if sourceWorkerID == "nod_host_local" {
			if err := s.vmMgr.StopVM(ctx, vmIdentifier(v)); err != nil {
				return nil, fmt.Errorf("stop source vm before migration: %w", err)
			}
			if refreshed, gerr := s.vmMgr.GetVM(ctx, vmIdentifier(v)); gerr == nil && refreshed != nil {
				v = refreshed
			}
		} else {
			updated, err := s.callRemoteVMLifecycle(ctx, v, "vm.stop")
			if err != nil {
				return nil, fmt.Errorf("stop source remote vm before migration: %w", err)
			}
			v = updated
		}
	}

	prevVM := *v
	vmKey := s.remoteVMTargetID(v)

	restartSourceOnErr := func() {
		if !wasRunning {
			return
		}
		bgCtx, bgCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer bgCancel()
		if sourceWorkerID == "nod_host_local" {
			if rerr := s.vmMgr.RunVM(bgCtx, vmIdentifier(&prevVM)); rerr != nil {
				log.Printf("[migration] warning: rollback restart local source vm %s failed: %v", vmKey, rerr)
			}
		} else {
			srcCopy := prevVM
			if _, rerr := s.callRemoteVMLifecycle(bgCtx, &srcCopy, "vm.start"); rerr != nil {
				log.Printf("[migration] warning: rollback restart remote source vm %s on %s failed: %v", vmKey, sourceWorkerID, rerr)
			}
		}
	}

	vmMetaJSON, err := json.Marshal(v)
	if err != nil {
		restartSourceOnErr()
		return nil, err
	}

	// Determine artifact kinds to transfer.
	kinds := []string{"disk"}
	if v.Status == db.StatusPaused {
		kinds = append(kinds, "snapshot", "mem")
	}

	cleanupTargetOnErr := func() {
		if targetNode.ID == "nod_host_local" {
			_ = s.vmMgr.CleanupLocalVMFiles(context.Background(), vmKey)
		} else if s.workers != nil {
			_ = s.workers.CallRPC(context.Background(), targetNode.ID, "vm.delete", api.WorkerVMIDRequest{VMID: vmKey}, nil)
		}
	}

	var importedVM *db.VM
	for ki, kind := range kinds {
		isLastKind := ki == len(kinds)-1
		var offset int64
		for {
			var chunk *api.WorkerVMExportDiskResponse
			if sourceWorkerID == "nod_host_local" {
				c, exportErr := s.vmMgr.ExportVMArtifactChunk(ctx, vmKey, kind, offset, migrationChunkBytes)
				if exportErr != nil {
					if kind != "disk" && os.IsNotExist(exportErr) {
						break
					}
					cleanupTargetOnErr()
					restartSourceOnErr()
					return nil, fmt.Errorf("export local %s: %w", kind, exportErr)
				}
				chunk = c
			} else {
				var c api.WorkerVMExportDiskResponse
				exportErr := s.workers.CallRPC(ctx, sourceWorkerID, "vm.export_disk", api.WorkerVMExportDiskRequest{
					VMID:   vmKey,
					Kind:   kind,
					Offset: offset,
					Length: migrationChunkBytes,
				}, &c)
				if exportErr != nil {
					if kind != "disk" && strings.Contains(exportErr.Error(), "no such file") {
						break
					}
					cleanupTargetOnErr()
					restartSourceOnErr()
					return nil, fmt.Errorf("export remote %s from %s: %w", kind, sourceWorkerID, exportErr)
				}
				chunk = &c
			}

			finalize := isLastKind && chunk.EOF
			importReq := api.WorkerVMImportDiskRequest{
				VMID:      vmKey,
				Kind:      kind,
				Offset:    chunk.Offset,
				TotalSize: chunk.TotalSize,
				Data:      chunk.Data,
				Gzipped:   chunk.Gzipped,
				Finalize:  finalize,
			}
			if finalize || (offset == 0 && kind == "disk") {
				importReq.VM = vmMetaJSON
			}

			var targetResult db.VM
			if targetNode.ID == "nod_host_local" {
				res, impErr := s.vmMgr.ImportVMArtifactChunk(ctx, importReq)
				if impErr != nil {
					cleanupTargetOnErr()
					restartSourceOnErr()
					return nil, fmt.Errorf("import %s to local node: %w", kind, impErr)
				}
				if res != nil {
					targetResult = *res
				}
			} else {
				if impErr := s.workers.CallRPC(ctx, targetNode.ID, "vm.import_disk", importReq, &targetResult); impErr != nil {
					cleanupTargetOnErr()
					restartSourceOnErr()
					return nil, fmt.Errorf("import %s to worker %s: %w", kind, targetNode.ID, impErr)
				}
			}
			if finalize {
				importedVM = &targetResult
			}
			if chunk.EOF {
				break
			}
			offset += int64(chunk.RawLength)
			if chunk.RawLength <= 0 {
				break
			}
		}
	}

	if importedVM == nil {
		// Ensure finalization ran if optional trailing kinds were skipped.
		var targetResult db.VM
		finReq := api.WorkerVMImportDiskRequest{
			VMID:     vmKey,
			Kind:     "disk",
			Finalize: true,
			VM:       vmMetaJSON,
		}
		if targetNode.ID == "nod_host_local" {
			res, impErr := s.vmMgr.ImportVMArtifactChunk(ctx, finReq)
			if impErr != nil {
				cleanupTargetOnErr()
				restartSourceOnErr()
				return nil, impErr
			}
			if res != nil {
				targetResult = *res
			}
		} else {
			if impErr := s.workers.CallRPC(ctx, targetNode.ID, "vm.import_disk", finReq, &targetResult); impErr != nil {
				cleanupTargetOnErr()
				restartSourceOnErr()
				return nil, impErr
			}
		}
		importedVM = &targetResult
	}

	// Update authoritative control-plane DB record to point to targetNode.
	v.WorkerID = targetNode.ID
	if targetNode.PoolID != "" {
		v.PoolID = targetNode.PoolID
	}
	v.IPAddress = importedVM.IPAddress
	v.PublicIP = importedVM.PublicIP
	v.MACAddress = importedVM.MACAddress
	v.TAPDevice = importedVM.TAPDevice
	v.DiskPath = importedVM.DiskPath
	v.KernelPath = importedVM.KernelPath
	v.RootfsPath = importedVM.RootfsPath
	v.SSHKeyPath = importedVM.SSHKeyPath
	v.SnapshotPath = importedVM.SnapshotPath
	v.MemFilePath = importedVM.MemFilePath
	v.Status = importedVM.Status
	v.PID = 0
	v.SocketPath = ""
	v.UpdatedAt = time.Now().UTC()
	if err := s.vmMgr.SaveVMRecord(ctx, v); err != nil {
		*v = prevVM
		cleanupTargetOnErr()
		restartSourceOnErr()
		return nil, err
	}

	// Clean up source artifacts only after target import and DB update succeeded.
	if sourceWorkerID == "nod_host_local" {
		if err := s.vmMgr.CleanupLocalVMFiles(ctx, vmKey); err != nil {
			log.Printf("[migration] warning: local cleanup for %s failed: %v", vmKey, err)
		}
	} else if s.workers != nil {
		if err := s.workers.CallRPC(ctx, sourceWorkerID, "vm.delete", api.WorkerVMIDRequest{VMID: vmKey}, nil); err != nil {
			log.Printf("[migration] warning: source worker %s cleanup for %s failed: %v", sourceWorkerID, vmKey, err)
		}
	}

	if wasRunning {
		if targetNode.ID == "nod_host_local" {
			if err := s.vmMgr.RunVM(ctx, vmIdentifier(v)); err != nil {
				return v, fmt.Errorf("migrated to %s but failed to restart vm: %w", targetNode.ID, err)
			}
			if refreshed, gerr := s.vmMgr.GetVM(ctx, vmIdentifier(v)); gerr == nil && refreshed != nil {
				v = refreshed
			}
		} else {
			if updated, err := s.callRemoteVMLifecycle(ctx, v, "vm.start"); err != nil {
				return v, fmt.Errorf("migrated to %s but failed to restart remote vm: %w", targetNode.ID, err)
			} else {
				v = updated
			}
		}
	}

	return v, nil
}

// handlePoolMigrate handles POST /api/v1/pools/:id/migrate.
func (s *Server) handlePoolMigrate(w http.ResponseWriter, r *http.Request, poolID string) {
	if !s.requireAdminAccess(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req api.MigratePoolRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.TargetPoolID == "" && req.TargetWorkerID == "" {
		httpError(w, "target_pool_id or target_worker_id is required", http.StatusBadRequest)
		return
	}
	if req.TargetPoolID != "" && req.TargetPoolID == poolID {
		httpError(w, "target_pool_id must differ from source pool", http.StatusBadRequest)
		return
	}

	migrateWorkers := req.MigrateWorkers
	migrateSandboxes := req.MigrateSandboxes || !req.MigrateWorkers
	if migrateWorkers && req.TargetPoolID == "" {
		httpError(w, "target_pool_id is required when migrate_workers is true", http.StatusBadRequest)
		return
	}

	actor := getActor(r)
	resp, err, disconnected := runDetached(r, func(bgCtx context.Context) (api.PoolMigrationResponse, error) {
		st := s.vmMgr.GetStore()
		allVMs, err := st.ListVMs(bgCtx)
		if err != nil {
			return api.PoolMigrationResponse{}, err
		}

		// Build worker->pool map before moving workers so VMs with empty PoolID resolve accurately.
		nodes, _ := st.ListWorkerNodes(bgCtx)
		workerPoolBefore := make(map[string]string, len(nodes))
		for _, n := range nodes {
			if n != nil {
				workerPoolBefore[n.ID] = n.PoolID
			}
		}

		var candidates []*db.VM
		for _, v := range allVMs {
			if v == nil {
				continue
			}
			effectivePool := v.PoolID
			if effectivePool == "" {
				wid := v.WorkerID
				if wid == "" {
					wid = "nod_host_local"
				}
				effectivePool = workerPoolBefore[wid]
			}
			if effectivePool == poolID {
				candidates = append(candidates, v)
			}
		}

		out := api.PoolMigrationResponse{
			SourcePoolID:   poolID,
			TargetPoolID:   req.TargetPoolID,
			MovedWorkerIDs: []string{},
			MigratedVMIDs:  []string{},
		}

		movedWorkerSet := make(map[string]bool)
		if migrateWorkers && req.TargetPoolID != "" {
			for _, n := range nodes {
				if n == nil || n.PoolID != poolID {
					continue
				}
				n.PoolID = req.TargetPoolID
				if err := st.SaveWorkerNode(bgCtx, n); err != nil {
					out.WorkersFailed++
					out.Errors = append(out.Errors, fmt.Sprintf("worker %s: %v", n.ID, err))
					continue
				}
				if s.workers != nil {
					if live, ok := s.workers.Get(n.ID); ok {
						live.PoolID = req.TargetPoolID
					}
				}
				movedWorkerSet[n.ID] = true
				out.WorkersMoved++
				out.MovedWorkerIDs = append(out.MovedWorkerIDs, n.ID)
			}

			if !migrateSandboxes {
				for _, v := range allVMs {
					if v == nil {
						continue
					}
					wid := v.WorkerID
					if wid == "" {
						wid = "nod_host_local"
					}
					if movedWorkerSet[wid] && v.PoolID != req.TargetPoolID {
						v.PoolID = req.TargetPoolID
						v.UpdatedAt = time.Now().UTC()
						if serr := s.vmMgr.SaveVMRecord(bgCtx, v); serr != nil {
							out.Errors = append(out.Errors, fmt.Sprintf("update pool_id for vm %s: %v", v.ID, serr))
						}
					}
				}
			}
		}

		if migrateSandboxes {
			var toMigrate []*db.VM
			for _, v := range candidates {
				wid := v.WorkerID
				if wid == "" {
					wid = "nod_host_local"
				}
				if movedWorkerSet[wid] && (req.TargetWorkerID == "" || req.TargetWorkerID == wid) {
					if v.PoolID != req.TargetPoolID {
						v.PoolID = req.TargetPoolID
						v.UpdatedAt = time.Now().UTC()
						if serr := s.vmMgr.SaveVMRecord(bgCtx, v); serr != nil {
							out.SandboxesFailed++
							out.Errors = append(out.Errors, fmt.Sprintf("vm %s: %v", v.ID, serr))
						}
					}
					continue
				}
				toMigrate = append(toMigrate, v)
			}

			if len(toMigrate) > 0 {
				if err := s.preflightPoolMigrationCapacity(bgCtx, toMigrate, req); err != nil {
					var rle *api.ResourceLimitError
					if errors.As(err, &rle) {
						return out, err
					}
					if strings.Contains(err.Error(), "not found") {
						return out, &httpStatusError{code: http.StatusNotFound, err: err}
					}
					return out, &httpStatusError{code: http.StatusBadRequest, err: err}
				}
			}

			for _, v := range toMigrate {
				_, merr := s.migrateSingleVM(bgCtx, v, api.MigrateVMRequest{
					TargetWorkerID: req.TargetWorkerID,
					TargetPoolID:   req.TargetPoolID,
					Force:          req.Force,
				})
				if merr != nil {
					out.SandboxesFailed++
					out.Errors = append(out.Errors, fmt.Sprintf("vm %s: %v", v.ID, merr))
				} else {
					out.SandboxesMigrated++
					out.MigratedVMIDs = append(out.MigratedVMIDs, v.ID)
				}
			}
		}

		s.recordEvent("pool.migrated", actor, poolID, "delivered", 100, out)
		s.broadcastClusterUpdate()
		return out, nil
	})
	if disconnected {
		return
	}
	if err != nil {
		writeDetachedError(w, r, err)
		return
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) preflightPoolMigrationCapacity(ctx context.Context, candidates []*db.VM, req api.MigratePoolRequest) error {
	workers, localNode, policy, err := s.buildPlacementNodes(ctx)
	if err != nil {
		return err
	}
	simWorkers := make([]*db.WorkerNode, 0, len(workers))
	for _, n := range workers {
		if n == nil {
			continue
		}
		cp := *n
		simWorkers = append(simWorkers, &cp)
	}
	var simLocal *db.WorkerNode
	if localNode != nil {
		cp := *localNode
		simLocal = &cp
	}

	for _, v := range candidates {
		sourceWorkerID := v.WorkerID
		if sourceWorkerID == "" {
			sourceWorkerID = "nod_host_local"
		}
		var filteredWorkers []*db.WorkerNode
		for _, n := range simWorkers {
			if n.ID == sourceWorkerID {
				continue
			}
			filteredWorkers = append(filteredWorkers, n)
		}
		var filteredLocal *db.WorkerNode
		if simLocal != nil && simLocal.ID != sourceWorkerID {
			filteredLocal = simLocal
		}

		chosen, selErr := scheduler.SelectNodeForSandbox(ctx, filteredWorkers, filteredLocal, scheduler.SandboxPlacementRequest{
			WorkerID: req.TargetWorkerID,
			PoolID:   req.TargetPoolID,
			VCPUs:    v.VCPUs,
			MemoryMB: v.MemoryMB,
			DiskMB:   v.DiskMB,
		}, policy)
		if selErr != nil {
			return selErr
		}

		chosen.UsedDiskMB += v.DiskMB
		if chosen.AvailableDiskMB > 0 {
			if chosen.AvailableDiskMB > v.DiskMB {
				chosen.AvailableDiskMB -= v.DiskMB
			} else {
				chosen.AvailableDiskMB = 0
			}
		}
		if v.Status == db.StatusRunning {
			chosen.RunningVMs++
			chosen.UsedVCPUs += v.VCPUs
			chosen.UsedMemMB += v.MemoryMB
			if chosen.AvailableMemMB > 0 {
				if chosen.AvailableMemMB > v.MemoryMB {
					chosen.AvailableMemMB -= v.MemoryMB
				} else {
					chosen.AvailableMemMB = 0
				}
			}
		}
	}
	return nil
}

// handleBulkDeleteVMs handles POST /api/v1/vms/bulk-delete and /api/v1/sandboxes/bulk-delete.
func (s *Server) handleBulkDeleteVMs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req api.BulkVMsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.IDs) == 0 && len(req.VMIDs) > 0 {
		req.IDs = req.VMIDs
	}
	if len(req.IDs) == 0 {
		httpError(w, "ids is required", http.StatusBadRequest)
		return
	}
	if len(req.IDs) > maxBulkRequestItems {
		httpError(w, fmt.Sprintf("too many ids: %d (max %d per request)", len(req.IDs), maxBulkRequestItems), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	actor := getActor(r)
	callerOwner, _ := s.currentOwnerID(r)

	// Pre-resolve accessible VMs serially (HTTP request auth check) before running workers.
	type deleteTarget struct {
		requestedID string
		vm          *db.VM
		resolveErr  string
	}
	targets := make([]deleteTarget, len(req.IDs))
	validCount := 0
	for i, id := range req.IDs {
		v, err := s.resolveAccessibleVM(ctx, r, id)
		if err != nil {
			targets[i] = deleteTarget{requestedID: id, resolveErr: err.Error()}
			continue
		}
		if v == nil {
			targets[i] = deleteTarget{requestedID: id, resolveErr: "vm not found"}
			continue
		}
		if v.Protected {
			display := v.Name
			if display == "" {
				display = id
			}
			targets[i] = deleteTarget{requestedID: id, resolveErr: fmt.Sprintf("item %s is protected against deletion", display)}
			continue
		}
		targets[i] = deleteTarget{requestedID: id, vm: v}
		validCount++
	}

	execOneDelete := func(bgCtx context.Context, v *db.VM) error {
		vmKey := vmIdentifier(v)
		s.removePortForwardsForVM(bgCtx, v, true)
		var delErr error
		if vm.IsRemoteVM(v) {
			if s.workers != nil {
				delErr = s.workers.CallRPC(bgCtx, v.WorkerID, "vm.delete", api.WorkerVMIDRequest{VMID: s.remoteVMTargetID(v)}, nil)
				if delErr != nil && strings.Contains(strings.ToLower(delErr.Error()), "not found") {
					delErr = nil
				}
			}
			if delErr == nil {
				delErr = s.vmMgr.DeleteVMRecord(bgCtx, vmKey)
			}
		} else {
			delErr = s.vmMgr.DeleteVMDeferred(bgCtx, vmKey)
		}
		return delErr
	}

	if req.Async {
		taskID := "task_" + uuid.NewString()[:8]
		resp := api.BulkVMsResponse{
			Action:    "delete",
			Status:    "queued",
			TaskID:    taskID,
			Requested: len(req.IDs),
			Queued:    validCount,
			Succeeded: []string{},
			Failed:    []api.BulkVMItemError{},
		}
		for _, t := range targets {
			if t.resolveErr != "" {
				resp.Failed = append(resp.Failed, api.BulkVMItemError{ID: t.requestedID, Error: t.resolveErr})
			}
		}
		if validCount > 0 && s.ensureOpQueue().wouldExceed(validCount) {
			httpError(w, errBackendQueueFull.Error(), http.StatusTooManyRequests)
			return
		}
		s.recordQueuedTask(callerOwner, resp)
		if validCount > 0 {
			var remaining atomic.Int64
			remaining.Store(int64(validCount))
			var mu sync.Mutex
			succeeded := make([]string, 0, validCount)
			failed := append([]api.BulkVMItemError{}, resp.Failed...)
			reconcileOwners := make(map[string]bool)
			localDeleted := false

			finish := func(bgCtx context.Context) {
				if remaining.Add(-1) != 0 {
					return
				}
				mu.Lock()
				ownersCopy := make([]string, 0, len(reconcileOwners))
				for owner := range reconcileOwners {
					ownersCopy = append(ownersCopy, owner)
				}
				doReconcile := localDeleted
				summary := api.BulkVMsResponse{
					Action:    "delete",
					Status:    "completed",
					TaskID:    taskID,
					Requested: len(req.IDs),
					Succeeded: succeeded,
					Failed:    failed,
				}
				mu.Unlock()
				s.recordTaskSummary(callerOwner, summary)
				if doReconcile && s.vmMgr != nil {
					for _, owner := range ownersCopy {
						s.vmMgr.ReconcileAccountNetworkIsolation(bgCtx, owner)
					}
					s.vmMgr.ProcessQueue(bgCtx)
				}
				s.recordEvent("sandbox.bulk_deleted", actor, fmt.Sprintf("%d sandboxes", len(summary.Succeeded)), "delivered", 50, summary)
				s.broadcastClusterUpdate()
			}

			for _, t := range targets {
				if t.resolveErr != "" {
					continue
				}
				target := t
				enqErr := s.enqueueBackendOp(func(bgCtx context.Context) {
					delErr := execOneDelete(bgCtx, target.vm)
					mu.Lock()
					if delErr != nil {
						failed = append(failed, api.BulkVMItemError{ID: target.requestedID, Error: delErr.Error()})
					} else {
						succeeded = append(succeeded, target.requestedID)
						if !vm.IsRemoteVM(target.vm) {
							localDeleted = true
							owner := target.vm.OwnerID
							if owner == "" {
								owner = "default"
							}
							reconcileOwners[owner] = true
						}
					}
					mu.Unlock()
					s.broadcastClusterUpdate()
					finish(bgCtx)
				})
				if enqErr != nil {
					mu.Lock()
					failed = append(failed, api.BulkVMItemError{ID: target.requestedID, Error: enqErr.Error()})
					mu.Unlock()
					finish(context.Background())
				}
			}
		}
		s.recordEvent("sandbox.bulk_queued", actor, fmt.Sprintf("delete %d sandboxes", validCount), "queued", 0, resp)
		s.broadcastClusterUpdate()
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
		return
	}

	resp, _, disconnected := runDetached(r, func(bgCtx context.Context) (api.BulkVMsResponse, error) {
		const maxBulkWorkers = 4
		sem := make(chan struct{}, maxBulkWorkers)
		var wg sync.WaitGroup
		var resMu sync.Mutex

		out := api.BulkVMsResponse{
			Action:    "delete",
			Requested: len(req.IDs),
			Succeeded: []string{},
			Failed:    []api.BulkVMItemError{},
		}
		reconcileOwners := make(map[string]bool)
		localDeleted := false

		for _, t := range targets {
			if t.resolveErr != "" {
				resMu.Lock()
				out.Failed = append(out.Failed, api.BulkVMItemError{ID: t.requestedID, Error: t.resolveErr})
				resMu.Unlock()
				continue
			}
			wg.Add(1)
			go func(t deleteTarget) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				delErr := execOneDelete(bgCtx, t.vm)

				resMu.Lock()
				if delErr != nil {
					out.Failed = append(out.Failed, api.BulkVMItemError{ID: t.requestedID, Error: delErr.Error()})
				} else {
					out.Succeeded = append(out.Succeeded, t.requestedID)
					if !vm.IsRemoteVM(t.vm) {
						localDeleted = true
						owner := t.vm.OwnerID
						if owner == "" {
							owner = "default"
						}
						reconcileOwners[owner] = true
					}
				}
				resMu.Unlock()
			}(t)
		}
		wg.Wait()

		if localDeleted && s.vmMgr != nil {
			for owner := range reconcileOwners {
				s.vmMgr.ReconcileAccountNetworkIsolation(bgCtx, owner)
			}
			s.vmMgr.ProcessQueue(bgCtx)
		}

		s.recordEvent("sandbox.bulk_deleted", actor, fmt.Sprintf("%d sandboxes", len(out.Succeeded)), "delivered", 50, out)
		s.broadcastClusterUpdate()
		return out, nil
	})
	if disconnected {
		return
	}
	json.NewEncoder(w).Encode(resp)
}

// handleBulkActionVMs handles POST /api/v1/vms/bulk-action and /api/v1/sandboxes/bulk-action.
func (s *Server) handleBulkActionVMs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req api.BulkVMsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.IDs) == 0 && len(req.VMIDs) > 0 {
		req.IDs = req.VMIDs
	}
	if len(req.IDs) == 0 {
		httpError(w, "ids is required", http.StatusBadRequest)
		return
	}
	if len(req.IDs) > maxBulkRequestItems {
		httpError(w, fmt.Sprintf("too many ids: %d (max %d per request)", len(req.IDs), maxBulkRequestItems), http.StatusBadRequest)
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	switch action {
	case "start", "run", "stop", "restart", "save-state", "snapshot", "resume", "protect", "unprotect", "delete":
	default:
		httpError(w, fmt.Sprintf("unsupported bulk action %q (allowed: start, stop, restart, save-state, snapshot, resume, protect, unprotect, delete)", req.Action), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	callerOwner, isAdmin := s.currentOwnerID(r)
	actor := getActor(r)

	type actionTarget struct {
		requestedID string
		vm          *db.VM
		resolveErr  string
	}
	targets := make([]actionTarget, len(req.IDs))
	validCount := 0
	for i, id := range req.IDs {
		v, err := s.resolveAccessibleVM(ctx, r, id)
		if err != nil {
			targets[i] = actionTarget{requestedID: id, resolveErr: err.Error()}
			continue
		}
		if v == nil {
			targets[i] = actionTarget{requestedID: id, resolveErr: "vm not found"}
			continue
		}
		if action == "delete" && v.Protected {
			display := v.Name
			if display == "" {
				display = id
			}
			targets[i] = actionTarget{requestedID: id, resolveErr: fmt.Sprintf("item %s is protected against deletion", display)}
			continue
		}
		if action == "start" || action == "run" || action == "resume" {
			if qErr := s.enforceRunQuotaWithAdmin(ctx, isAdmin, v); qErr != nil {
				targets[i] = actionTarget{requestedID: id, resolveErr: qErr.Error()}
				continue
			}
		}
		targets[i] = actionTarget{requestedID: id, vm: v}
		validCount++
	}

	execOneAction := func(bgCtx context.Context, v *db.VM) error {
		vmKey := vmIdentifier(v)
		switch action {
		case "delete":
			s.removePortForwardsForVM(bgCtx, v, true)
			if vm.IsRemoteVM(v) {
				var opErr error
				if s.workers != nil {
					opErr = s.workers.CallRPC(bgCtx, v.WorkerID, "vm.delete", api.WorkerVMIDRequest{VMID: s.remoteVMTargetID(v)}, nil)
					if opErr != nil && strings.Contains(strings.ToLower(opErr.Error()), "not found") {
						opErr = nil
					}
				}
				if opErr == nil {
					opErr = s.vmMgr.DeleteVMRecord(bgCtx, vmKey)
				}
				return opErr
			}
			return s.vmMgr.DeleteVMDeferred(bgCtx, vmKey)

		case "protect":
			t := true
			_, opErr := s.vmMgr.UpdateVMConfig(bgCtx, vmKey, vm.UpdateVMConfigRequest{Protected: &t})
			return opErr

		case "unprotect":
			f := false
			_, opErr := s.vmMgr.UpdateVMConfig(bgCtx, vmKey, vm.UpdateVMConfigRequest{Protected: &f})
			return opErr

		case "restart":
			_, opErr := s.restartSingleVM(bgCtx, isAdmin, v, false)
			return opErr
		}

		if vm.IsRemoteVM(v) {
			var method string
			switch action {
			case "start", "run":
				method = "vm.start"
			case "stop":
				method = "vm.stop"
			case "save-state", "snapshot":
				method = "vm.save_state"
			case "resume":
				method = "vm.resume"
			}
			_, opErr := s.callRemoteVMLifecycle(bgCtx, v, method)
			if opErr == nil && action == "stop" {
				s.removePortForwardsForVM(bgCtx, v, false)
			}
			return opErr
		}

		switch action {
		case "start", "run":
			if qErr := s.enforceRunQuotaWithAdmin(bgCtx, isAdmin, v); qErr != nil {
				return qErr
			}
			opErr := s.vmMgr.RunVM(bgCtx, vmKey)
			if opErr == nil {
				if refreshed, gErr := s.vmMgr.GetVM(bgCtx, vmKey); gErr == nil && refreshed != nil && refreshed.Status == db.StatusRunning {
					s.mountAttachedVolumes(bgCtx, refreshed)
					s.applyEnvironment(bgCtx, refreshed)
					s.applyPortForwardsForVM(bgCtx, refreshed)
				}
			}
			return opErr
		case "stop":
			opErr := s.vmMgr.StopVMDeferred(bgCtx, vmKey)
			if opErr == nil {
				s.removePortForwardsForVM(bgCtx, v, false)
			}
			return opErr
		case "save-state", "snapshot":
			return s.vmMgr.SaveState(bgCtx, vmKey)
		case "resume":
			if qErr := s.enforceRunQuotaWithAdmin(bgCtx, isAdmin, v); qErr != nil {
				return qErr
			}
			opErr := s.vmMgr.ResumeVM(bgCtx, vmKey)
			if opErr == nil {
				if refreshed, gErr := s.vmMgr.GetVM(bgCtx, vmKey); gErr == nil && refreshed != nil && refreshed.Status == db.StatusRunning {
					s.mountAttachedVolumes(bgCtx, refreshed)
					s.applyEnvironment(bgCtx, refreshed)
					s.applyPortForwardsForVM(bgCtx, refreshed)
				}
			}
			return opErr
		}
		return nil
	}

	if req.Async {
		taskID := "task_" + uuid.NewString()[:8]
		resp := api.BulkVMsResponse{
			Action:    action,
			Status:    "queued",
			TaskID:    taskID,
			Requested: len(req.IDs),
			Queued:    validCount,
			Succeeded: []string{},
			Failed:    []api.BulkVMItemError{},
		}
		for _, t := range targets {
			if t.resolveErr != "" {
				resp.Failed = append(resp.Failed, api.BulkVMItemError{ID: t.requestedID, Error: t.resolveErr})
			}
		}
		if validCount > 0 && s.ensureOpQueue().wouldExceed(validCount) {
			httpError(w, errBackendQueueFull.Error(), http.StatusTooManyRequests)
			return
		}
		s.recordQueuedTask(callerOwner, resp)
		if validCount > 0 {
			var remaining atomic.Int64
			remaining.Store(int64(validCount))
			var mu sync.Mutex
			succeeded := make([]string, 0, validCount)
			failed := append([]api.BulkVMItemError{}, resp.Failed...)
			reconcileOwners := make(map[string]bool)
			localNeedsReconcile := false

			finish := func(bgCtx context.Context) {
				if remaining.Add(-1) != 0 {
					return
				}
				mu.Lock()
				ownersCopy := make([]string, 0, len(reconcileOwners))
				for owner := range reconcileOwners {
					ownersCopy = append(ownersCopy, owner)
				}
				doReconcile := localNeedsReconcile
				summary := api.BulkVMsResponse{
					Action:    action,
					Status:    "completed",
					TaskID:    taskID,
					Requested: len(req.IDs),
					Succeeded: succeeded,
					Failed:    failed,
				}
				mu.Unlock()
				s.recordTaskSummary(callerOwner, summary)
				if doReconcile && s.vmMgr != nil {
					for _, owner := range ownersCopy {
						s.vmMgr.ReconcileAccountNetworkIsolation(bgCtx, owner)
					}
					s.vmMgr.ProcessQueue(bgCtx)
				}
				s.recordEvent("sandbox.bulk_"+action, actor, fmt.Sprintf("%d sandboxes", len(summary.Succeeded)), "delivered", 50, summary)
				s.broadcastClusterUpdate()
			}

			for _, t := range targets {
				if t.resolveErr != "" {
					continue
				}
				target := t
				enqErr := s.enqueueBackendOp(func(bgCtx context.Context) {
					opErr := execOneAction(bgCtx, target.vm)
					mu.Lock()
					if opErr != nil {
						failed = append(failed, api.BulkVMItemError{ID: target.requestedID, Error: opErr.Error()})
					} else {
						succeeded = append(succeeded, target.requestedID)
						if !vm.IsRemoteVM(target.vm) && (action == "stop" || action == "delete") {
							localNeedsReconcile = true
							owner := target.vm.OwnerID
							if owner == "" {
								owner = "default"
							}
							reconcileOwners[owner] = true
						}
					}
					mu.Unlock()
					s.broadcastClusterUpdate()
					finish(bgCtx)
				})
				if enqErr != nil {
					mu.Lock()
					failed = append(failed, api.BulkVMItemError{ID: target.requestedID, Error: enqErr.Error()})
					mu.Unlock()
					finish(context.Background())
				}
			}
		}
		s.recordEvent("sandbox.bulk_queued", actor, fmt.Sprintf("%s %d sandboxes", action, validCount), "queued", 0, resp)
		s.broadcastClusterUpdate()
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(resp)
		return
	}

	resp, _, disconnected := runDetached(r, func(bgCtx context.Context) (api.BulkVMsResponse, error) {
		const maxBulkWorkers = 4
		sem := make(chan struct{}, maxBulkWorkers)
		var wg sync.WaitGroup
		var resMu sync.Mutex

		out := api.BulkVMsResponse{
			Action:    action,
			Requested: len(req.IDs),
			Succeeded: []string{},
			Failed:    []api.BulkVMItemError{},
		}
		reconcileOwners := make(map[string]bool)
		localNeedsReconcile := false

		for _, t := range targets {
			if t.resolveErr != "" {
				resMu.Lock()
				out.Failed = append(out.Failed, api.BulkVMItemError{ID: t.requestedID, Error: t.resolveErr})
				resMu.Unlock()
				continue
			}
			wg.Add(1)
			go func(t actionTarget) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				opErr := execOneAction(bgCtx, t.vm)

				resMu.Lock()
				if opErr != nil {
					out.Failed = append(out.Failed, api.BulkVMItemError{ID: t.requestedID, Error: opErr.Error()})
				} else {
					out.Succeeded = append(out.Succeeded, t.requestedID)
					if !vm.IsRemoteVM(t.vm) && (action == "stop" || action == "delete") {
						localNeedsReconcile = true
						owner := t.vm.OwnerID
						if owner == "" {
							owner = "default"
						}
						reconcileOwners[owner] = true
					}
				}
				resMu.Unlock()
			}(t)
		}
		wg.Wait()

		if localNeedsReconcile && s.vmMgr != nil {
			for owner := range reconcileOwners {
				s.vmMgr.ReconcileAccountNetworkIsolation(bgCtx, owner)
			}
			s.vmMgr.ProcessQueue(bgCtx)
		}

		s.recordEvent("sandbox.bulk_"+action, actor, fmt.Sprintf("%d sandboxes", len(out.Succeeded)), "delivered", 50, out)
		s.broadcastClusterUpdate()
		return out, nil
	})
	if disconnected {
		return
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) proxyRemoteFiles(w http.ResponseWriter, r *http.Request, v *db.VM, subParts []string) {
	ctx := r.Context()
	subAction := ""
	if len(subParts) > 0 {
		subAction = subParts[0]
	}

	var rpcReq api.WorkerVMFilesRequest
	rpcReq.VMID = s.remoteVMTargetID(v)

	switch {
	case r.Method == http.MethodPost && (subAction == "" || subAction == "write"):
		var req FileWriteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Path == "" {
			httpError(w, "missing path", http.StatusBadRequest)
			return
		}
		rpcReq.Action = "write"
		rpcReq.Path = req.Path
		rpcReq.Content = req.Content
		rpcReq.Format = req.Format

	case r.Method == http.MethodPost && subAction == "mkdir":
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			httpError(w, "missing or invalid path", http.StatusBadRequest)
			return
		}
		rpcReq.Action = "mkdir"
		rpcReq.Path = req.Path

	case r.Method == http.MethodDelete || (r.Method == http.MethodPost && subAction == "remove"):
		path := r.URL.Query().Get("path")
		if path == "" && r.Method == http.MethodPost {
			var req struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			path = req.Path
		}
		if path == "" {
			httpError(w, "missing path query parameter", http.StatusBadRequest)
			return
		}
		rpcReq.Action = "remove"
		rpcReq.Path = path

	case r.Method == http.MethodGet && (subAction == "list" || r.URL.Query().Get("action") == "list"):
		path := r.URL.Query().Get("path")
		if path == "" {
			path = "/workspace"
		}
		rpcReq.Action = "list"
		rpcReq.Path = path

	case r.Method == http.MethodGet:
		path := r.URL.Query().Get("path")
		if path == "" {
			httpError(w, "missing path query parameter", http.StatusBadRequest)
			return
		}
		rpcReq.Action = "read"
		rpcReq.Path = path
		rpcReq.Format = r.URL.Query().Get("format")

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var raw json.RawMessage
	if err := s.workers.CallRPC(ctx, v.WorkerID, "vm.files", rpcReq, &raw); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Server) proxyRemoteProcesses(w http.ResponseWriter, r *http.Request, v *db.VM, subParts []string) {
	ctx := r.Context()
	subAction := ""
	if len(subParts) > 0 {
		subAction = subParts[0]
	}

	var rpcReq api.WorkerVMProcessesRequest
	rpcReq.VMID = s.remoteVMTargetID(v)

	switch {
	case r.Method == http.MethodGet:
		rpcReq.Action = "list"
	case r.Method == http.MethodPost && subAction == "kill":
		var req ProcessKillRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PID <= 0 {
			httpError(w, "invalid process kill request", http.StatusBadRequest)
			return
		}
		rpcReq.Action = "kill"
		rpcReq.PID = req.PID
		rpcReq.Signal = req.Signal
	case r.Method == http.MethodPost && subAction == "":
		var req ProcessStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Command) == "" {
			httpError(w, "missing command", http.StatusBadRequest)
			return
		}
		rpcReq.Action = "start"
		rpcReq.Command = req.Command
		rpcReq.Cwd = req.Cwd
		rpcReq.Env = req.Env
	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var raw json.RawMessage
	if err := s.workers.CallRPC(ctx, v.WorkerID, "vm.processes", rpcReq, &raw); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// execRemoteVMStream streams exec output via vm.exec RPC and emits SSE events.
func (s *Server) execRemoteVMStream(ctx context.Context, v *db.VM, req vm.ExecRequest, emit func(vm.ExecStreamEvent) error) error {
	var res vm.ExecResponse
	err := s.workers.CallRPC(ctx, v.WorkerID, "vm.exec", api.WorkerVMExecRequest{
		VMID:           s.remoteVMTargetID(v),
		Command:        req.Command,
		User:           req.User,
		TimeoutSeconds: req.Timeout,
	}, &res)
	if err != nil {
		return err
	}
	if res.Stdout != "" {
		if err := emit(vm.ExecStreamEvent{Stream: "stdout", Data: res.Stdout}); err != nil {
			return err
		}
	}
	if res.Stderr != "" {
		if err := emit(vm.ExecStreamEvent{Stream: "stderr", Data: res.Stderr}); err != nil {
			return err
		}
	}
	code := res.ExitCode
	return emit(vm.ExecStreamEvent{ExitCode: &code, DurationMs: res.DurationMs})
}

// syncRemoteSSHKeysForOwner reconciles SSH keys on remote VMs owned by ownerID.
func (s *Server) syncRemoteSSHKeysForOwner(ctx context.Context, ownerID string) *vm.SSHKeySyncResult {
	res := &vm.SSHKeySyncResult{}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil || s.workers == nil {
		return res
	}
	vms, err := s.vmMgr.GetStore().ListVMsByOwner(ctx, ownerID)
	if err != nil {
		return res
	}
	for _, v := range vms {
		if !vm.IsRemoteVM(v) {
			continue
		}
		if err := s.syncRemoteVMSSHKeys(ctx, v); err != nil {
			res.Failed = append(res.Failed, vm.SSHKeySyncFailure{VMID: v.ID, Name: v.Name, Error: err.Error()})
		} else {
			res.Applied = append(res.Applied, v.ID)
		}
	}
	return res
}

// Unused import guard for base64
var _ = base64.StdEncoding
