package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/master/auth"
	"github.com/google/uuid"
)

func getHostResources(cfgVCPUs, cfgMemMB, cfgDiskMB int) (vcpus int, memMB int, diskMB int) {
	vcpus = cfgVCPUs
	if vcpus <= 0 {
		vcpus = runtime.NumCPU()
	}
	memMB = cfgMemMB
	if memMB <= 0 {
		if data, err := os.ReadFile("/proc/meminfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "MemTotal:") {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						if kb, err := strconv.Atoi(fields[1]); err == nil && kb > 0 {
							memMB = kb / 1024
						}
					}
					break
				}
			}
		}
		if memMB <= 0 {
			memMB = 4096
		}
	}
	diskMB = cfgDiskMB
	if diskMB <= 0 {
		var stat syscall.Statfs_t
		if err := syscall.Statfs("/", &stat); err == nil {
			diskMB = int((stat.Blocks * uint64(stat.Bsize)) / (1024 * 1024))
		}
		if diskMB <= 0 {
			diskMB = 51200
		}
	}
	return vcpus, memMB, diskMB
}

// ===== Volumes API =====

// rootVolumeIDFor derives a deterministic rootfs volume ID for a VM. Tenant
// owners are encoded so two users can use the same VM name without colliding.
func rootVolumeIDFor(v *db.VM) string {
	if v == nil {
		return ""
	}
	owner := v.OwnerID
	if owner == "" || owner == "default" {
		return "vol_root_" + v.Name
	}
	return "vol_root_" + strings.TrimPrefix(owner, "usr_") + "_" + v.Name
}

func (s *Server) handleVolumes(w http.ResponseWriter, r *http.Request) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	switch r.Method {
	case http.MethodGet:
		// Sync running / existing VMs into volumes store so rootfs volumes are reflected.
		// Only VMs visible to the caller are synchronised.
		vms, _ := s.listAccessibleVMs(ctx, r)
		liveRootVolumes := map[string]bool{}
		for _, v := range vms {
			if v == nil {
				continue
			}
			volID := rootVolumeIDFor(v)
			liveRootVolumes[volID] = true
			existing, _ := st.GetVolume(ctx, volID)
			status := "available"
			attachedVM := ""
			if v.Status == db.StatusRunning {
				status = "attached"
				attachedVM = v.Name
			}
			sizeGB := v.MemoryMB / 1024
			if sizeGB < 10 {
				sizeGB = 10
			}
			volOwner := v.OwnerID
			if volOwner == "" {
				volOwner = "default"
			}
			if existing == nil {
				_ = st.SaveVolume(ctx, &db.Volume{
					ID:         volID,
					OwnerID:    volOwner,
					Name:       v.Name + "-rootfs",
					Pool:       "default",
					SizeGB:     sizeGB,
					UsedGB:     1.8,
					Status:     status,
					AttachedVM: attachedVM,
					DevicePath: "/dev/vda",
					MountPoint: "/",
					Filesystem: "ext4",
					IOPS:       4500,
					Throughput: "180 MB/s",
					Snapshots:  0,
				})
			} else {
				if existing.Status != status || existing.AttachedVM != attachedVM || existing.OwnerID != volOwner {
					existing.Status = status
					existing.AttachedVM = attachedVM
					existing.OwnerID = volOwner
					_ = st.SaveVolume(ctx, existing)
				}
			}
		}

		// Drop synthetic rootfs volumes whose sandbox no longer exists so the
		// storage view never shows attachments to deleted VMs.
		if allVols, err := s.listAccessibleVolumes(ctx, r); err == nil {
			for _, vol := range allVols {
				if strings.HasPrefix(vol.ID, "vol_root_") && !liveRootVolumes[vol.ID] {
					_ = st.DeleteVolume(ctx, vol.ID)
				}
			}
		}

		vols, err := s.listAccessibleVolumes(ctx, r)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		filteredVols := make([]*db.Volume, 0, len(vols))
		for _, vol := range vols {
			if vol == nil || vol.OwnerID == "jobs" || strings.HasPrefix(vol.Name, "job-") || strings.HasPrefix(vol.AttachedVM, "job-") {
				continue
			}
			filteredVols = append(filteredVols, vol)
		}
		json.NewEncoder(w).Encode(filteredVols)

	case http.MethodPost:
		var req struct {
			Name       string `json:"name"`
			Pool       string `json:"pool"`
			SizeGB     int    `json:"size_gb"`
			Filesystem string `json:"filesystem"`
			AttachedVM string `json:"attached_vm"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		ownerID, isAdmin, ok := s.callerIdentity(r)
		if !ok {
			httpError(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			httpError(w, "name is required", http.StatusBadRequest)
			return
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Name)), "job-") {
			httpError(w, "name prefix 'job-' is reserved for ephemeral CI jobs", http.StatusBadRequest)
			return
		}
		if req.SizeGB <= 0 {
			req.SizeGB = 10
		}
		if req.Pool == "" {
			req.Pool = "default"
		}
		if req.Filesystem == "" {
			req.Filesystem = "ext4"
		}
		if req.Filesystem != "ext4" && req.Filesystem != "xfs" {
			httpError(w, "filesystem must be ext4 or xfs", http.StatusBadRequest)
			return
		}
		volOwner := ownerID
		if req.AttachedVM != "" {
			vm, _ := s.resolveAccessibleVM(ctx, r, req.AttachedVM)
			if vm == nil || (!isAdmin && !s.canAccessOwner(r, vm.OwnerID)) {
				httpError(w, "sandbox not found: "+req.AttachedVM, http.StatusNotFound)
				return
			}
			if vm.Status == db.StatusRunning || vm.Status == db.StatusPaused {
				httpError(w, "stop the sandbox before attaching a volume", http.StatusConflict)
				return
			}
			req.AttachedVM = vm.Name
			if vm.OwnerID != "" {
				volOwner = vm.OwnerID
			}
		}
		status := "available"
		mountPoint := ""
		devicePath := ""
		if req.AttachedVM != "" {
			status = "attached"
			mountPoint = "/mnt/data"
			devicePath = "/dev/vdb"
		}

		if err := s.enforceDiskQuota(ctx, r, volOwner, req.SizeGB*1024); err != nil {
			httpError(w, err.Error(), http.StatusTooManyRequests)
			return
		}

		volID := "vol_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
		path, err := s.vmMgr.GetStorageManager().CreateDataVolume(volID, req.SizeGB, req.Filesystem)
		if err != nil {
			httpError(w, "failed to allocate volume: "+err.Error(), http.StatusBadRequest)
			return
		}

		vol := &db.Volume{
			ID:         volID,
			OwnerID:    volOwner,
			Name:       req.Name,
			Pool:       req.Pool,
			SizeGB:     req.SizeGB,
			UsedGB:     0.0,
			Status:     status,
			AttachedVM: req.AttachedVM,
			DevicePath: devicePath,
			MountPoint: mountPoint,
			Filesystem: req.Filesystem,
			Path:       path,
			IOPS:       3200,
			Throughput: "250 MB/s",
			Snapshots:  0,
			CreatedAt:  time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
		}
		if err := st.SaveVolume(ctx, vol); err != nil {
			_ = s.vmMgr.GetStorageManager().DeleteDataVolume(path)
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.recordEvent("volume.created", getActor(r), vol.Name, "delivered", 25, vol)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(vol)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleVolumeActions(w http.ResponseWriter, r *http.Request) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	path := strings.TrimPrefix(r.URL.Path, "/api/v1/volumes/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		httpError(w, "volume id required", http.StatusBadRequest)
		return
	}
	id := parts[0]

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			vol, err := s.resolveAccessibleVolume(ctx, r, id)
			if err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if vol == nil {
				httpError(w, "volume not found", http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(vol)

		case http.MethodDelete:
			vol, err := s.resolveAccessibleVolume(ctx, r, id)
			if err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if vol == nil {
				httpError(w, "volume not found", http.StatusNotFound)
				return
			}
			if strings.HasPrefix(vol.ID, "vol_root_") {
				httpError(w, "rootfs volumes are managed automatically by their sandbox", http.StatusConflict)
				return
			}
			if vol.Path != "" {
				if err := s.vmMgr.GetStorageManager().DeleteDataVolume(vol.Path); err != nil {
					httpError(w, "failed to delete volume image: "+err.Error(), http.StatusInternalServerError)
					return
				}
			}
			// Delete strictly by the resolved unique ID (and owner) so a
			// same-named volume belonging to another tenant is never removed.
			if err := st.DeleteVolumeByID(ctx, vol.ID, vol.OwnerID); err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			s.recordEvent("volume.deleted", getActor(r), vol.ID, "delivered", 15, nil)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

		default:
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	action := parts[1]
	if r.Method != http.MethodPost {
		httpError(w, "action requires POST", http.StatusMethodNotAllowed)
		return
	}

	vol, err := s.resolveAccessibleVolume(ctx, r, id)
	if err != nil || vol == nil {
		httpError(w, "volume not found", http.StatusNotFound)
		return
	}
	if strings.HasPrefix(vol.ID, "vol_root_") {
		httpError(w, "rootfs volumes are managed automatically by their sandbox", http.StatusConflict)
		return
	}

	switch action {
	case "attach":
		var req struct {
			VMID string `json:"vm_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.VMID == "" {
			httpError(w, "vm_id is required", http.StatusBadRequest)
			return
		}
		vm, _ := s.resolveAccessibleVM(ctx, r, req.VMID)
		if vm == nil || !s.canAccessOwner(r, vm.OwnerID) {
			httpError(w, "sandbox not found: "+req.VMID, http.StatusNotFound)
			return
		}
		if vm.Status == db.StatusRunning || vm.Status == db.StatusPaused {
			httpError(w, "stop the sandbox before attaching a volume", http.StatusConflict)
			return
		}
		if vol.Path == "" {
			httpError(w, "volume has no backing image", http.StatusBadRequest)
			return
		}
		vol.Status = "attached"
		vol.AttachedVM = vm.Name
		vol.MountPoint = "/mnt/data"
		vol.DevicePath = "/dev/vdb"
		if err := st.SaveVolume(ctx, vol); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("volume.attached", getActor(r), vol.Name, "delivered", 30, map[string]string{"vm_id": vm.Name})
		json.NewEncoder(w).Encode(vol)

	case "detach":
		if vol.AttachedVM != "" {
			if vm, _ := st.GetVMByOwner(ctx, vol.OwnerID, vol.AttachedVM); vm != nil && (vm.Status == db.StatusRunning || vm.Status == db.StatusPaused) {
				httpError(w, "stop the sandbox before detaching this volume", http.StatusConflict)
				return
			}
		}
		vol.Status = "available"
		vol.AttachedVM = ""
		vol.MountPoint = ""
		vol.DevicePath = ""
		if err := st.SaveVolume(ctx, vol); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("volume.detached", getActor(r), vol.Name, "delivered", 25, nil)
		json.NewEncoder(w).Encode(vol)

	case "expand":
		var req struct {
			AddGB int `json:"add_gb"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.AddGB <= 0 {
			req.AddGB = 10
		}
		if vol.AttachedVM != "" {
			if vm, _ := st.GetVMByOwner(ctx, vol.OwnerID, vol.AttachedVM); vm != nil && (vm.Status == db.StatusRunning || vm.Status == db.StatusPaused) {
				httpError(w, "stop the sandbox before expanding this volume", http.StatusConflict)
				return
			}
		}
		if err := s.enforceDiskQuota(ctx, r, vol.OwnerID, req.AddGB*1024); err != nil {
			httpError(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		if err := s.vmMgr.GetStorageManager().ExpandDataVolume(vol.Path, vol.SizeGB+req.AddGB, vol.Filesystem); err != nil {
			httpError(w, "failed to expand volume: "+err.Error(), http.StatusBadRequest)
			return
		}
		vol.SizeGB += req.AddGB
		if err := st.SaveVolume(ctx, vol); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("volume.expanded", getActor(r), vol.Name, "delivered", 20, map[string]interface{}{"added_gb": req.AddGB, "new_size": vol.SizeGB})
		json.NewEncoder(w).Encode(vol)

	case "snapshot":
		if vol.AttachedVM != "" {
			if vm, _ := st.GetVMByOwner(ctx, vol.OwnerID, vol.AttachedVM); vm != nil && (vm.Status == db.StatusRunning || vm.Status == db.StatusPaused) {
				httpError(w, "stop the sandbox before snapshotting this volume", http.StatusConflict)
				return
			}
		}
		if _, err := s.vmMgr.GetStorageManager().SnapshotDataVolume(vol.Path, vol.Snapshots+1); err != nil {
			httpError(w, "failed to snapshot volume: "+err.Error(), http.StatusBadRequest)
			return
		}
		vol.Snapshots++
		if err := st.SaveVolume(ctx, vol); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("volume.snapshot.created", getActor(r), vol.Name, "delivered", 35, map[string]interface{}{"snapshots": vol.Snapshots})
		json.NewEncoder(w).Encode(vol)

	default:
		httpError(w, "unknown action", http.StatusNotFound)
	}
}

// ===== Webhooks API =====

func (s *Server) handleWebhooks(w http.ResponseWriter, r *http.Request) {
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
		hooks, err := st.ListWebhooks(ctx)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if hooks == nil {
			hooks = []*db.WebhookEndpoint{}
		}
		json.NewEncoder(w).Encode(hooks)

	case http.MethodPost:
		var req struct {
			URL         string   `json:"url"`
			Description string   `json:"description"`
			Secret      string   `json:"secret"`
			Events      []string `json:"events"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.URL == "" {
			httpError(w, "url is required", http.StatusBadRequest)
			return
		}
		secret := req.Secret
		if secret == "" {
			secret = "whsec_" + strings.ReplaceAll(uuid.NewString()[:16], "-", "")
		}
		if req.Events == nil {
			req.Events = []string{"sandbox.started", "sandbox.stopped", "snapshot.created", "ci.job.completed"}
		}

		hook := &db.WebhookEndpoint{
			ID:              "whk_" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""),
			URL:             req.URL,
			Description:     req.Description,
			Secret:          secret,
			Events:          req.Events,
			Active:          true,
			LastDelivery:    "Never",
			SuccessRate:     "100%",
			DeliveriesCount: 0,
			CreatedAt:       time.Now().UTC(),
			UpdatedAt:       time.Now().UTC(),
		}
		if err := st.SaveWebhook(ctx, hook); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.recordEvent("webhook.created", getActor(r), hook.URL, "delivered", 20, hook)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(hook)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleWebhookActions(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAccess(w, r) {
		return
	}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	path := strings.TrimPrefix(r.URL.Path, "/api/v1/webhooks/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		httpError(w, "webhook id required", http.StatusBadRequest)
		return
	}
	id := parts[0]

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			hook, err := st.GetWebhook(ctx, id)
			if err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if hook == nil {
				httpError(w, "webhook not found", http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(hook)

		case http.MethodPatch, http.MethodPut:
			hook, err := st.GetWebhook(ctx, id)
			if err != nil || hook == nil {
				httpError(w, "webhook not found", http.StatusNotFound)
				return
			}
			var update struct {
				URL         *string   `json:"url"`
				Description *string   `json:"description"`
				Secret      *string   `json:"secret"`
				Events      *[]string `json:"events"`
				Active      *bool     `json:"active"`
			}
			if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
				httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if update.URL != nil {
				hook.URL = *update.URL
			}
			if update.Description != nil {
				hook.Description = *update.Description
			}
			if update.Secret != nil {
				hook.Secret = *update.Secret
			}
			if update.Events != nil {
				hook.Events = *update.Events
			}
			if update.Active != nil {
				hook.Active = *update.Active
			}
			if err := st.SaveWebhook(ctx, hook); err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			s.recordEvent("webhook.updated", getActor(r), hook.ID, "delivered", 15, hook)
			json.NewEncoder(w).Encode(hook)

		case http.MethodDelete:
			if err := st.DeleteWebhook(ctx, id); err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			s.recordEvent("webhook.deleted", getActor(r), id, "delivered", 15, nil)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

		default:
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	if parts[1] == "test" {
		if r.Method != http.MethodPost {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		hook, err := st.GetWebhook(ctx, id)
		if err != nil || hook == nil {
			httpError(w, "webhook not found", http.StatusNotFound)
			return
		}
		code, deliverErr := s.deliverWebhook(ctx, hook, "webhook.test", map[string]string{
			"message": "Test delivery from the Compute control plane",
		})
		if deliverErr != nil {
			httpError(w, "webhook delivery failed: "+deliverErr.Error(), http.StatusBadGateway)
			return
		}
		s.recordEvent("webhook.tested", getActor(r), hook.URL, "delivered", 25, map[string]interface{}{"code": code})
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"code":   code,
		})
		return
	}

	httpError(w, "unknown action", http.StatusNotFound)
}

// ===== OpenTelemetry API =====

func (s *Server) handleOTel(w http.ResponseWriter, r *http.Request) {
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
		cfg, err := st.GetOTelConfig(ctx)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(cfg)

	case http.MethodPut, http.MethodPost:
		cfg, _ := st.GetOTelConfig(ctx)
		if cfg == nil {
			cfg = &db.OTelConfig{}
		}
		if err := json.NewDecoder(r.Body).Decode(cfg); err != nil {
			httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := st.SaveOTelConfig(ctx, cfg); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("otel.configured", getActor(r), "otel_collector", "delivered", 20, cfg)
		json.NewEncoder(w).Encode(cfg)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleOTelTest(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAccess(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	cfg, err := st.GetOTelConfig(ctx)
	if err != nil || cfg == nil {
		httpError(w, "otel config not found", http.StatusInternalServerError)
		return
	}
	if !cfg.Enabled {
		httpError(w, "telemetry export is disabled", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		httpError(w, "OTLP endpoint is not configured", http.StatusBadRequest)
		return
	}

	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if !strings.HasSuffix(endpoint, "/v1/traces") {
		endpoint += "/v1/traces"
	}
	now := time.Now().UTC()
	spanID := hex.EncodeToString(randomBytes(8))
	traceID := hex.EncodeToString(randomBytes(16))
	body, _ := json.Marshal(map[string]interface{}{
		"resourceSpans": []interface{}{map[string]interface{}{
			"resource": map[string]interface{}{
				"attributes": []interface{}{map[string]interface{}{
					"key":   "service.name",
					"value": map[string]string{"stringValue": cfg.ServiceName},
				}},
			},
			"scopeSpans": []interface{}{map[string]interface{}{
				"scope": map[string]string{"name": "compute-control-plane"},
				"spans": []interface{}{map[string]interface{}{
					"traceId":           traceID,
					"spanId":            spanID,
					"name":              "otel.export.test",
					"kind":              1,
					"startTimeUnixNano": strconv.FormatInt(now.Add(-time.Millisecond).UnixNano(), 10),
					"endTimeUnixNano":   strconv.FormatInt(now.UnixNano(), 10),
					"status":            map[string]interface{}{"code": 1},
				}},
			}},
		}},
	})

	exportCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(exportCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		httpError(w, "invalid OTLP endpoint: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Compute-Exporter", "computed")
	if strings.TrimSpace(cfg.AuthHeader) != "" {
		req.Header.Set("Authorization", strings.TrimSpace(cfg.AuthHeader))
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		s.recordEvent("otel.export_failed", getActor(r), "otel_collector", "failed", 10, map[string]string{"error": err.Error()})
		httpError(w, "OTLP export failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.recordEvent("otel.export_failed", getActor(r), "otel_collector", "failed", 10, map[string]interface{}{"code": resp.StatusCode})
		httpError(w, "OTLP endpoint returned HTTP "+strconv.Itoa(resp.StatusCode), http.StatusBadGateway)
		return
	}

	cfg.SpansExported++
	cfg.LastExportAt = time.Now().UTC().Format(time.RFC3339)
	_ = st.SaveOTelConfig(ctx, cfg)
	s.recordEvent("otel.test_exported", getActor(r), "otel_collector", "delivered", 30, map[string]interface{}{"spans": 1, "endpoint": endpoint})
	json.NewEncoder(w).Encode(cfg)
}

// ===== Events & SSE Stream API =====

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	switch r.Method {
	case http.MethodGet:
		events, err := st.ListAuditEvents(ctx, 100)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, isAdmin := s.currentOwnerID(r); !isAdmin {
			username := ""
			if authCtx := auth.GetAuth(ctx); authCtx != nil && authCtx.User != nil {
				username = authCtx.User.Username
			}
			filtered := make([]*db.AuditEvent, 0, len(events))
			for _, evt := range events {
				if evt != nil && username != "" && evt.Actor == username {
					filtered = append(filtered, evt)
				}
			}
			events = filtered
		}
		if events == nil {
			events = []*db.AuditEvent{}
		}
		json.NewEncoder(w).Encode(events)

	case http.MethodPost:
		var req struct {
			Event      string      `json:"event"`
			Actor      string      `json:"actor"`
			Target     string      `json:"target"`
			Status     string      `json:"status"`
			DurationMs int         `json:"duration_ms"`
			Payload    interface{} `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		actor := req.Actor
		if actor == "" {
			actor = getActor(r)
		}
		evt := s.recordEvent(req.Event, actor, req.Target, req.Status, req.DurationMs, req.Payload)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(evt)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleEventsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpError(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	eventCh := make(chan *db.AuditEvent, 32)
	s.addEventSubscriber(eventCh)
	defer s.removeEventSubscriber(eventCh)

	// Send initial comment to establish the connection
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	_, isAdmin := s.currentOwnerID(r)
	username := ""
	if authCtx := auth.GetAuth(r.Context()); authCtx != nil && authCtx.User != nil {
		username = authCtx.User.Username
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case evt, ok := <-eventCh:
			if !ok {
				return
			}
			if !isAdmin && (evt == nil || username == "" || evt.Actor != username) {
				continue
			}
			b, err := json.Marshal(evt)
			if err == nil {
				fmt.Fprintf(w, "data: %s\n\n", string(b))
				flusher.Flush()
			}
		}
	}
}

// ===== Access API Keys API =====

func (s *Server) handleAccessAPIKeys(w http.ResponseWriter, r *http.Request) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	switch r.Method {
	case http.MethodGet:
		var keys []*db.AccessAPIKey
		var err error
		if _, isAdmin := s.currentOwnerID(r); isAdmin {
			keys, err = st.ListAccessAPIKeys(ctx)
		} else if authCtx := auth.GetAuth(ctx); authCtx != nil && authCtx.User != nil {
			keys, err = st.ListAccessAPIKeysByCreator(ctx, authCtx.User.Username)
		}
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if keys == nil {
			keys = []*db.AccessAPIKey{}
		}
		// Never expose the full secret token after creation.
		for _, k := range keys {
			k.Token = ""
		}
		json.NewEncoder(w).Encode(keys)

	case http.MethodPost:
		var req struct {
			Name      string `json:"name"`
			Role      string `json:"role"`
			Scope     string `json:"scope"`
			ExpiresAt string `json:"expires_at"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		name := req.Name
		if name == "" {
			name = "Access API Key"
		}
		scope := req.Scope
		if scope == "" {
			scope = "admin"
		}
		expiresAt := normalizeAccessKeyExpiration(req.ExpiresAt)

		b := make([]byte, 16)
		_, _ = rand.Read(b)
		tokenHex := hex.EncodeToString(b)
		fullToken := "cmp_live_" + tokenHex
		prefix := "cmp_live_" + tokenHex[:4] + "..." + tokenHex[len(tokenHex)-4:]
		id := "key_live_" + tokenHex[:8]

		role := req.Role
		if role == "" && scope == "admin" {
			role = "admin"
		}

		key := &db.AccessAPIKey{
			ID:        id,
			Name:      name,
			Role:      role,
			Prefix:    prefix,
			Token:     fullToken,
			Scope:     scope,
			LastUsed:  "Never",
			ExpiresAt: expiresAt,
			CreatedBy: getActor(r),
			CreatedAt: time.Now().UTC(),
		}
		if err := st.SaveAccessAPIKey(ctx, key); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.recordEvent("api_key.created", getActor(r), key.Name, "delivered", 15, map[string]string{"id": key.ID, "prefix": key.Prefix})
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(key)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAccessAPIKeyActions(w http.ResponseWriter, r *http.Request) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	id := strings.TrimPrefix(r.URL.Path, "/api/v1/api-keys/")
	id = strings.TrimSpace(id)
	if id == "" {
		httpError(w, "key id required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		key, err := st.GetAccessAPIKey(ctx, id)
		if err != nil || key == nil {
			httpError(w, "key not found", http.StatusNotFound)
			return
		}
		if _, isAdmin := s.currentOwnerID(r); !isAdmin {
			if authCtx := auth.GetAuth(ctx); authCtx == nil || authCtx.User == nil || key.CreatedBy != authCtx.User.Username {
				httpError(w, "key not found", http.StatusNotFound)
				return
			}
		}
		key.Token = ""
		json.NewEncoder(w).Encode(key)

	case http.MethodDelete:
		key, err := st.GetAccessAPIKey(ctx, id)
		if err != nil || key == nil {
			httpError(w, "key not found", http.StatusNotFound)
			return
		}
		if _, isAdmin := s.currentOwnerID(r); !isAdmin {
			if authCtx := auth.GetAuth(ctx); authCtx == nil || authCtx.User == nil || key.CreatedBy != authCtx.User.Username {
				httpError(w, "key not found", http.StatusNotFound)
				return
			}
		}
		if err := st.DeleteAccessAPIKey(ctx, id); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("api_key.deleted", getActor(r), id, "delivered", 15, nil)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ===== Environments API =====

func (s *Server) handleEnvironments(w http.ResponseWriter, r *http.Request) {
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
		envs, err := st.ListEnvironments(ctx)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(envs) == 0 {
			base := &db.EnvironmentProfile{
				Name:                "base",
				IsDefault:           true,
				Version:             1,
				SecretsEnabled:      true,
				ComputeCredsEnabled: true,
				BoatCredsEnabled:    true,
				AgentCredsEnabled:   true,
				CredentialProfile:   "Owner's default",
				GitHubReposEnabled:  true,
				GitHubRepos:         []string{},
				Secrets:             "",
				UpdatedAt:           time.Now().UTC(),
			}
			_ = st.SaveEnvironment(ctx, base)
			envs = []*db.EnvironmentProfile{base}
		}
		json.NewEncoder(w).Encode(envs)

	case http.MethodPost:
		var env db.EnvironmentProfile
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if env.Name == "" {
			httpError(w, "name is required", http.StatusBadRequest)
			return
		}
		if env.Version == 0 {
			env.Version = 1
		}
		if err := st.SaveEnvironment(ctx, &env); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("environment.updated", getActor(r), env.Name, "delivered", 20, env)
		json.NewEncoder(w).Encode(env)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleEnvironmentActions(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAccess(w, r) {
		return
	}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	name := strings.TrimPrefix(r.URL.Path, "/api/v1/environments/")
	name = strings.TrimSpace(name)
	if name == "" {
		httpError(w, "environment name required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		env, err := st.GetEnvironment(ctx, name)
		if err != nil || env == nil {
			httpError(w, "environment not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(env)

	case http.MethodPut, http.MethodPost:
		var env db.EnvironmentProfile
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		env.Name = name
		if env.Version == 0 {
			env.Version = 1
		}
		if err := st.SaveEnvironment(ctx, &env); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("environment.updated", getActor(r), env.Name, "delivered", 20, env)
		json.NewEncoder(w).Encode(env)

	case http.MethodDelete:
		if err := st.DeleteEnvironment(ctx, name); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("environment.deleted", getActor(r), name, "delivered", 15, nil)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ===== Network Port Forwards API =====

func (s *Server) handlePortForwards(w http.ResponseWriter, r *http.Request) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	switch r.Method {
	case http.MethodGet:
		var pfs []*db.PortForward
		var err error
		if _, isAdmin := s.currentOwnerID(r); isAdmin {
			pfs, err = st.ListPortForwards(ctx)
		} else {
			seen := make(map[string]bool)
			for _, oid := range s.ownerIDsFor(r) {
				owned, listErr := st.ListPortForwardsByOwner(ctx, oid)
				if listErr != nil {
					err = listErr
					break
				}
				for _, pf := range owned {
					if pf == nil || seen[pf.ID] {
						continue
					}
					seen[pf.ID] = true
					pfs = append(pfs, pf)
				}
			}
		}
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if pfs == nil {
			pfs = []*db.PortForward{}
		}
		json.NewEncoder(w).Encode(pfs)

	case http.MethodPost:
		var req struct {
			HostPort  int    `json:"host_port"`
			VMID      string `json:"vm_id"`
			VMName    string `json:"vm_name"`
			GuestPort int    `json:"guest_port"`
			Protocol  string `json:"protocol"`
			Subdomain string `json:"subdomain"`
			Public    *bool  `json:"public,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		ownerID, isAdmin, ok := s.callerIdentity(r)
		if !ok {
			httpError(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if req.HostPort <= 0 || req.HostPort > 65535 || req.GuestPort <= 0 || req.GuestPort > 65535 {
			httpError(w, "host_port and guest_port must be between 1 and 65535", http.StatusBadRequest)
			return
		}

		// Anti-collision guard: privileged host ports (< 1024) require administrator privileges
		if req.HostPort < 1024 && !isAdmin {
			httpError(w, fmt.Sprintf("privileged host port %d requires administrator privileges", req.HostPort), http.StatusForbidden)
			return
		}

		// Wrap collision check, host port availability probe, and insert in s.portForwardMu to prevent check-then-use races
		s.portForwardMu.Lock()
		defer s.portForwardMu.Unlock()

		// Conflict detection: ensure host port is not already allocated in SQLite
		allPFs, err := st.ListPortForwards(ctx)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, existing := range allPFs {
			if existing.HostPort == req.HostPort {
				httpError(w, fmt.Sprintf("port conflict: host port %d is already allocated by port forward %s (sandbox: %s)", req.HostPort, existing.ID, existing.VMName), http.StatusConflict)
				return
			}
		}

		// Anti-collision guard: check if host port is currently bound or in use by a host service
		if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", req.HostPort)); err == nil {
			_ = ln.Close()
		} else {
			httpError(w, fmt.Sprintf("port conflict: host port %d is already in use by a host service: %v", req.HostPort, err), http.StatusConflict)
			return
		}

		vmName := req.VMName
		var targetVM *db.VM
		lookup := req.VMID
		if lookup == "" {
			lookup = req.VMName
		}
		if lookup != "" {
			v, _ := s.resolveAccessibleVM(ctx, r, lookup)
			if v != nil {
				if !s.canAccessOwner(r, v.OwnerID) {
					httpError(w, "sandbox not found", http.StatusNotFound)
					return
				}
				targetVM = v
				if vmName == "" {
					vmName = v.Name
				}
				if req.VMID == "" {
					req.VMID = v.ID
				}
			} else if !isAdmin {
				httpError(w, "sandbox not found", http.StatusNotFound)
				return
			} else if vmName == "" {
				vmName = req.VMID
			}
		}
		protocol := req.Protocol
		if protocol == "" {
			protocol = "tcp"
		}
		publicURL := ""
		if req.Subdomain != "" {
			publicURL = fmt.Sprintf("https://%s.compute.dev", req.Subdomain)
		} else {
			publicURL = fmt.Sprintf("http://localhost:%d", req.HostPort)
		}

		status := "pending"
		if targetVM != nil && targetVM.IPAddress != "" {
			if err := s.vmMgr.NetManager().AddPortForward(ctx, protocol, req.HostPort, targetVM.IPAddress, req.GuestPort); err != nil {
				httpError(w, "failed to install host forwarding rule: "+err.Error(), http.StatusInternalServerError)
				return
			}
			status = "active"
		}

		pfOwner := ownerID
		if targetVM != nil && targetVM.OwnerID != "" {
			pfOwner = targetVM.OwnerID
		}

		isPublic := false
		if req.Public != nil && *req.Public {
			isPublic = true
		}

		pf := &db.PortForward{
			ID:        "pf_" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""),
			OwnerID:   pfOwner,
			HostPort:  req.HostPort,
			VMID:      req.VMID,
			VMName:    vmName,
			GuestPort: req.GuestPort,
			Protocol:  protocol,
			Subdomain: req.Subdomain,
			PublicURL: publicURL,
			Status:    status,
			Public:    isPublic,
			CreatedAt: time.Now().UTC(),
		}
		if err := st.SavePortForward(ctx, pf); err != nil {
			errStr := strings.ToLower(err.Error())
			if strings.Contains(errStr, "unique") || strings.Contains(errStr, "constraint") {
				httpError(w, fmt.Sprintf("port conflict: host port %d is already allocated: %v", req.HostPort, err), http.StatusConflict)
				return
			}
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.recordEvent("port_forward.created", getActor(r), fmt.Sprintf("%d->%d", pf.HostPort, pf.GuestPort), "delivered", 15, pf)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(pf)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePortForwardActions(w http.ResponseWriter, r *http.Request) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	id := strings.TrimPrefix(r.URL.Path, "/api/v1/network/port-forwards/")
	id = strings.TrimSpace(id)
	if id == "" {
		httpError(w, "port forward id required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		pf, err := st.GetPortForward(ctx, id)
		if err != nil || pf == nil || !s.canAccessOwner(r, pf.OwnerID) {
			httpError(w, "port forward not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(pf)

	case http.MethodDelete:
		pf, err := st.GetPortForward(ctx, id)
		if err != nil || pf == nil || !s.canAccessOwner(r, pf.OwnerID) {
			httpError(w, "port forward not found", http.StatusNotFound)
			return
		}
		if pf != nil {
			if v, _ := st.GetVMByOwner(ctx, pf.OwnerID, pf.VMID); v != nil && v.IPAddress != "" {
				_ = s.vmMgr.NetManager().RemovePortForward(ctx, pf.Protocol, pf.HostPort, v.IPAddress, pf.GuestPort)
			}
		}
		if err := st.DeletePortForward(ctx, id); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("port_forward.deleted", getActor(r), id, "delivered", 15, nil)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
