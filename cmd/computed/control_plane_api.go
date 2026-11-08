package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/db"
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

func (s *Server) handleVolumes(w http.ResponseWriter, r *http.Request) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	switch r.Method {
	case http.MethodGet:
		// Sync running / existing VMs into volumes store so rootfs volumes are reflected
		vms, _ := s.vmMgr.ListVMs(ctx)
		liveVMs := map[string]bool{}
		for _, v := range vms {
			liveVMs[v.Name] = true
			volID := "vol_root_" + v.Name
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
			if existing == nil {
				_ = st.SaveVolume(ctx, &db.Volume{
					ID:         volID,
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
				if existing.Status != status || existing.AttachedVM != attachedVM {
					existing.Status = status
					existing.AttachedVM = attachedVM
					_ = st.SaveVolume(ctx, existing)
				}
			}
		}

		// Drop synthetic rootfs volumes whose sandbox no longer exists so the
		// storage view never shows attachments to deleted VMs.
		if allVols, err := st.ListVolumes(ctx); err == nil {
			for _, vol := range allVols {
				if strings.HasPrefix(vol.ID, "vol_root_") {
					name := strings.TrimPrefix(vol.ID, "vol_root_")
					if !liveVMs[name] {
						_ = st.DeleteVolume(ctx, vol.ID)
					}
				}
			}
		}

		vols, err := st.ListVolumes(ctx)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if vols == nil {
			vols = []*db.Volume{}
		}
		json.NewEncoder(w).Encode(vols)

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
		if req.Name == "" {
			httpError(w, "name is required", http.StatusBadRequest)
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
		if req.AttachedVM != "" {
			vm, _ := s.vmMgr.GetVM(ctx, req.AttachedVM)
			if vm == nil {
				httpError(w, "sandbox not found: "+req.AttachedVM, http.StatusNotFound)
				return
			}
			if vm.Status == db.StatusRunning || vm.Status == db.StatusPaused {
				httpError(w, "stop the sandbox before attaching a volume", http.StatusConflict)
				return
			}
			req.AttachedVM = vm.Name
		}
		status := "available"
		mountPoint := ""
		devicePath := ""
		if req.AttachedVM != "" {
			status = "attached"
			mountPoint = "/mnt/data"
			devicePath = "/dev/vdb"
		}

		volID := "vol_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
		path, err := s.vmMgr.GetStorageManager().CreateDataVolume(volID, req.SizeGB, req.Filesystem)
		if err != nil {
			httpError(w, "failed to allocate volume: "+err.Error(), http.StatusBadRequest)
			return
		}

		vol := &db.Volume{
			ID:         volID,
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
			vol, err := st.GetVolume(ctx, id)
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
			if strings.HasPrefix(id, "vol_root_") {
				httpError(w, "rootfs volumes are managed automatically by their sandbox", http.StatusConflict)
				return
			}
			if vol, _ := st.GetVolume(ctx, id); vol != nil && vol.Path != "" {
				if err := s.vmMgr.GetStorageManager().DeleteDataVolume(vol.Path); err != nil {
					httpError(w, "failed to delete volume image: "+err.Error(), http.StatusInternalServerError)
					return
				}
			}
			if err := st.DeleteVolume(ctx, id); err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			s.recordEvent("volume.deleted", getActor(r), id, "delivered", 15, nil)
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

	vol, err := st.GetVolume(ctx, id)
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
		vm, _ := s.vmMgr.GetVM(ctx, req.VMID)
		if vm == nil {
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
			if vm, _ := s.vmMgr.GetVM(ctx, vol.AttachedVM); vm != nil && (vm.Status == db.StatusRunning || vm.Status == db.StatusPaused) {
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
			if vm, _ := s.vmMgr.GetVM(ctx, vol.AttachedVM); vm != nil && (vm.Status == db.StatusRunning || vm.Status == db.StatusPaused) {
				httpError(w, "stop the sandbox before expanding this volume", http.StatusConflict)
				return
			}
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
			if vm, _ := s.vmMgr.GetVM(ctx, vol.AttachedVM); vm != nil && (vm.Status == db.StatusRunning || vm.Status == db.StatusPaused) {
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

	for {
		select {
		case <-r.Context().Done():
			return
		case evt, ok := <-eventCh:
			if !ok {
				return
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
		keys, err := st.ListAccessAPIKeys(ctx)
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
		expiresAt := req.ExpiresAt
		if expiresAt == "" {
			expiresAt = "in 90 days"
		}

		b := make([]byte, 16)
		_, _ = rand.Read(b)
		tokenHex := hex.EncodeToString(b)
		fullToken := "cmp_live_" + tokenHex
		prefix := "cmp_live_" + tokenHex[:4] + "..." + tokenHex[len(tokenHex)-4:]
		id := "key_live_" + tokenHex[:8]

		key := &db.AccessAPIKey{
			ID:        id,
			Name:      name,
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
		key.Token = ""
		json.NewEncoder(w).Encode(key)

	case http.MethodDelete:
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

// ===== Agent Profiles API =====

func (s *Server) handleAgentProfiles(w http.ResponseWriter, r *http.Request) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	switch r.Method {
	case http.MethodGet:
		profs, err := st.ListAgentProfiles(ctx)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if profs == nil {
			profs = []*db.AgentProfile{}
		}
		json.NewEncoder(w).Encode(profs)

	case http.MethodPost:
		var req struct {
			Name         string `json:"name"`
			Provider     string `json:"provider"`
			UpstreamURL  string `json:"upstream_url"`
			InjectHeader string `json:"inject_header"`
			SecretRef    string `json:"secret_ref"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			httpError(w, "name is required", http.StatusBadRequest)
			return
		}
		if req.Provider == "" {
			req.Provider = "Anthropic"
		}
		if req.UpstreamURL == "" {
			req.UpstreamURL = "https://api.anthropic.com/v1"
		}

		prof := &db.AgentProfile{
			ID:              "prof_" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""),
			Name:            req.Name,
			Provider:        req.Provider,
			UpstreamURL:     req.UpstreamURL,
			InjectHeader:    req.InjectHeader,
			SecretRef:       req.SecretRef,
			SandboxesLinked: 0,
			Status:          "active",
			CreatedAt:       time.Now().UTC(),
			UpdatedAt:       time.Now().UTC(),
		}
		if err := st.SaveAgentProfile(ctx, prof); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.recordEvent("agent_profile.created", getActor(r), prof.Name, "delivered", 20, prof)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(prof)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAgentProfileActions(w http.ResponseWriter, r *http.Request) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	id := strings.TrimPrefix(r.URL.Path, "/api/v1/agent-profiles/")
	id = strings.TrimSpace(id)
	if id == "" {
		httpError(w, "profile id required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		prof, err := st.GetAgentProfile(ctx, id)
		if err != nil || prof == nil {
			httpError(w, "profile not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(prof)

	case http.MethodPatch, http.MethodPut:
		prof, err := st.GetAgentProfile(ctx, id)
		if err != nil || prof == nil {
			httpError(w, "profile not found", http.StatusNotFound)
			return
		}
		var update struct {
			Name            *string `json:"name"`
			Provider        *string `json:"provider"`
			UpstreamURL     *string `json:"upstream_url"`
			InjectHeader    *string `json:"inject_header"`
			SecretRef       *string `json:"secret_ref"`
			SandboxesLinked *int    `json:"sandboxes_linked"`
			Status          *string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if update.Name != nil {
			prof.Name = *update.Name
		}
		if update.Provider != nil {
			prof.Provider = *update.Provider
		}
		if update.UpstreamURL != nil {
			prof.UpstreamURL = *update.UpstreamURL
		}
		if update.InjectHeader != nil {
			prof.InjectHeader = *update.InjectHeader
		}
		if update.SecretRef != nil {
			prof.SecretRef = *update.SecretRef
		}
		if update.SandboxesLinked != nil {
			prof.SandboxesLinked = *update.SandboxesLinked
		}
		if update.Status != nil {
			prof.Status = *update.Status
		}
		if err := st.SaveAgentProfile(ctx, prof); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("agent_profile.updated", getActor(r), prof.ID, "delivered", 15, prof)
		json.NewEncoder(w).Encode(prof)

	case http.MethodDelete:
		if err := st.DeleteAgentProfile(ctx, id); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("agent_profile.deleted", getActor(r), id, "delivered", 15, nil)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ===== Environments API =====

func (s *Server) handleEnvironments(w http.ResponseWriter, r *http.Request) {
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
				Name:               "base",
				IsDefault:          true,
				Version:            1,
				SecretsEnabled:     true,
				BoatCredsEnabled:   true,
				AgentCredsEnabled:  true,
				CredentialProfile:  "Owner's default",
				GitHubReposEnabled: true,
				GitHubRepos:        []string{},
				Secrets:            "",
				UpdatedAt:          time.Now().UTC(),
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
		pfs, err := st.ListPortForwards(ctx)
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
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.HostPort <= 0 || req.GuestPort <= 0 {
			httpError(w, "host_port and guest_port must be positive", http.StatusBadRequest)
			return
		}
		vmName := req.VMName
		var targetVM *db.VM
		if req.VMID != "" {
			if v, _ := s.vmMgr.GetVM(ctx, req.VMID); v != nil {
				targetVM = v
				if vmName == "" {
					vmName = v.Name
				}
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

		pf := &db.PortForward{
			ID:        "pf_" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""),
			HostPort:  req.HostPort,
			VMID:      req.VMID,
			VMName:    vmName,
			GuestPort: req.GuestPort,
			Protocol:  protocol,
			Subdomain: req.Subdomain,
			PublicURL: publicURL,
			Status:    status,
			CreatedAt: time.Now().UTC(),
		}
		if err := st.SavePortForward(ctx, pf); err != nil {
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
		if err != nil || pf == nil {
			httpError(w, "port forward not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(pf)

	case http.MethodDelete:
		if pf, _ := st.GetPortForward(ctx, id); pf != nil {
			if v, _ := s.vmMgr.GetVM(ctx, pf.VMID); v != nil && v.IPAddress != "" {
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
