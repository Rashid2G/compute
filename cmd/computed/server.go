package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/jobs"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/scheduler"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
	"github.com/Rashid2G/on-demand-vms/pkg/api"
	"github.com/Rashid2G/on-demand-vms/pkg/version"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

type Server struct {
	vmMgr     *vm.Manager
	cfg       config.GlobalConfig
	mux       *http.ServeMux
	workers   *scheduler.WorkerRegistry
	scheduler *scheduler.Scheduler
	jobRunner *jobs.Runner
}

func NewServer(vmMgr *vm.Manager, cfg config.GlobalConfig) *Server {
	workers := scheduler.NewWorkerRegistry()
	var sched *scheduler.Scheduler
	var jobRunner *jobs.Runner
	if vmMgr != nil && vmMgr.GetStore() != nil {
		sched = scheduler.NewScheduler(workers, vmMgr.GetStore())
		if cfg.Jobs.LocalExecution {
			jobRunner = jobs.NewRunner(vmMgr, cfg.Jobs.MaxConcurrent, time.Duration(cfg.Jobs.BootTimeoutSeconds)*time.Second)
			sched.SetLocalExecutor(jobRunner)
			if n, err := vmMgr.GetStore().FailStaleJobs(context.Background()); err == nil && n > 0 {
				log.Printf("[computed] marked %d interrupted job(s) as failed", n)
			}
		}
		sched.Start(context.Background())
	}
	s := &Server{
		vmMgr:     vmMgr,
		cfg:       cfg,
		mux:       http.NewServeMux(),
		workers:   workers,
		scheduler: sched,
		jobRunner: jobRunner,
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/api/v1/health", s.handleHealth)
	s.mux.HandleFunc("/api/v1/version", s.handleVersion)
	s.mux.HandleFunc("/api/v1/stats", s.handleStats)
	s.mux.HandleFunc("/api/v1/tiers", s.handleTiers)
	s.mux.HandleFunc("/api/v1/vms", s.handleVMs)
	s.mux.HandleFunc("/api/v1/vms/", s.handleVMActions)
	s.mux.HandleFunc("/api/v1/queue", s.handleQueue)
	s.mux.HandleFunc("/api/v1/config", s.handleConfig)
	s.mux.HandleFunc("/api/v1/network", s.handleNetworkInfo)
	s.mux.HandleFunc("/api/v1/network/allocations", s.handleNetworkAllocations)
	s.mux.HandleFunc("/api/v1/network/inspect/", s.handleNetworkInspect)
	s.mux.HandleFunc("/api/v1/network/prune", s.handleNetworkPrune)
	s.mux.HandleFunc("/api/v1/network/config", s.handleNetworkConfig)
	s.mux.HandleFunc("/api/v1/images", s.handleImages)
	s.mux.HandleFunc("/api/v1/images/", s.handleImageActions)
	s.mux.HandleFunc("/api/v1/templates", s.handleTemplates)
	s.mux.HandleFunc("/api/v1/templates/", s.handleTemplateActions)
	s.mux.HandleFunc("/api/v1/keys", s.handleKeys)
	s.mux.HandleFunc("/api/v1/keys/", s.handleKeyActions)
	s.mux.HandleFunc("/api/v1/ws", s.handleWS)
	s.mux.HandleFunc("/api/v1/worker/connect", s.handleWorkerConnect)
	s.mux.HandleFunc("/api/v1/worker-tokens", s.handleWorkerTokens)
	s.mux.HandleFunc("/api/v1/workers", s.handleWorkers)
	s.mux.HandleFunc("/api/v1/workers/", s.handleWorkerActions)
	s.mux.HandleFunc("/api/v1/pools", s.handlePools)
	s.mux.HandleFunc("/api/v1/pools/", s.handlePoolActions)
	s.mux.HandleFunc("/api/v1/jobs", s.handleJobs)
	s.mux.HandleFunc("/api/v1/jobs/", s.handleJobActions)
	s.mux.HandleFunc("/api/v1/ssh/", s.handleSSHDirect)
}

func (s *Server) SetGUIHandler(h http.Handler) {
	s.mux.Handle("/", h)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("Upgrade") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"version": version.Version,
		"name":    "Compute Daemon",
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	info := version.Get("computed", "Compute Daemon")
	_ = json.NewEncoder(w).Encode(info)
}

func (s *Server) handleTiers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	json.NewEncoder(w).Encode(config.GetSupportedTiers())
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Return the latest active limits and network mode as well
	cfg := s.cfg
	cfg.Resources = s.vmMgr.GetResourceLimits()
	cfg.Network = s.vmMgr.GetNetworkManager().GetConfig()
	cfg.Defaults = s.vmMgr.GetDefaults()
	json.NewEncoder(w).Encode(cfg)
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	queue, err := s.vmMgr.GetQueue(r.Context())
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if queue == nil {
		queue = []*vm.QueueItem{}
	}
	stats, _ := s.vmMgr.GetStats(r.Context())
	json.NewEncoder(w).Encode(map[string]interface{}{
		"queued_vms":      queue,
		"count":           len(queue),
		"running_vms":     stats.RunningVMs,
		"running_vcpus":   stats.RunningVCPUs,
		"running_mem_mb":  stats.RunningMemoryMB,
		"resource_limits": s.vmMgr.GetResourceLimits(),
	})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stats, err := s.vmMgr.GetStats(r.Context())
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(stats)
}

func (s *Server) handleNetworkInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	info, err := s.vmMgr.GetNetworkInfo(r.Context())
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(info)
}

func (s *Server) handleNetworkAllocations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	allocs, err := s.vmMgr.GetStore().GetNetworkAllocations(r.Context())
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if allocs == nil {
		allocs = []*db.NetworkAllocation{}
	}
	json.NewEncoder(w).Encode(allocs)
}

func (s *Server) handleNetworkInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/network/inspect/")
	vmID := strings.Trim(path, "/")
	if vmID == "" {
		httpError(w, "missing vm id", http.StatusBadRequest)
		return
	}
	res, err := s.vmMgr.InspectNetwork(r.Context(), vmID)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(res)
}

func (s *Server) handleNetworkPrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pruned, err := s.vmMgr.ReconcileAndPrune(r.Context())
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if pruned == nil {
		pruned = []string{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "ok",
		"pruned_taps": pruned,
	})
}

func (s *Server) handleNetworkConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := s.vmMgr.GetNetworkManager().GetConfig()
	json.NewEncoder(w).Encode(cfg)
}

func (s *Server) handleVMs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		vms, err := s.vmMgr.ListVMs(ctx)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if vms == nil {
			vms = []*db.VM{}
		}
		json.NewEncoder(w).Encode(vms)

	case http.MethodPost:
		var req vm.CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		created, err := s.vmMgr.CreateVM(ctx, req)
		if err != nil {
			if strings.Contains(err.Error(), "disk space issue") || strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "unsupported") {
				httpError(w, err.Error(), http.StatusBadRequest)
				return
			}
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(created)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleVMActions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/vms/")
	parts := strings.Split(strings.Trim(path, "/"), "/")

	if len(parts) == 0 || parts[0] == "" {
		httpError(w, "missing vm id", http.StatusBadRequest)
		return
	}

	vmID := parts[0]

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			v, err := s.vmMgr.GetVM(ctx, vmID)
			if err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if v == nil {
				httpError(w, "vm not found", http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(v)

		case http.MethodDelete:
			if err := s.vmMgr.DeleteVM(ctx, vmID); err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": vmID})

		case http.MethodPatch, http.MethodPut:
			var req vm.UpdateVMConfigRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
				return
			}
			updated, err := s.vmMgr.UpdateVMConfig(ctx, vmID, req)
			if err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(updated)

		default:
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	action := parts[1]
	if action == "ssh" {
		s.handleVMSSH(w, r, vmID)
		return
	}
	if action == "terminal" {
		s.handleVMTerminal(w, r, vmID)
		return
	}
	if r.Method != http.MethodPost {
		httpError(w, "action requires POST", http.StatusMethodNotAllowed)
		return
	}

	switch action {
	case "clone":
		var req vm.CreateRequest
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		cloned, err := s.vmMgr.CloneVM(ctx, vmID, req)
		if err != nil {
			if strings.Contains(err.Error(), "disk space issue") || strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "unsupported") {
				httpError(w, err.Error(), http.StatusBadRequest)
				return
			}
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(cloned)

	case "run", "start":
		if err := s.vmMgr.RunVM(ctx, vmID); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		v, _ := s.vmMgr.GetVM(ctx, vmID)
		json.NewEncoder(w).Encode(v)

	case "save-state", "snapshot":
		if err := s.vmMgr.SaveState(ctx, vmID); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		v, _ := s.vmMgr.GetVM(ctx, vmID)
		json.NewEncoder(w).Encode(v)

	case "resume":
		if err := s.vmMgr.ResumeVM(ctx, vmID); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		v, _ := s.vmMgr.GetVM(ctx, vmID)
		json.NewEncoder(w).Encode(v)

	case "stop":
		if err := s.vmMgr.StopVM(ctx, vmID); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		v, _ := s.vmMgr.GetVM(ctx, vmID)
		json.NewEncoder(w).Encode(v)

	case "exec":
		var req vm.ExecRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		res, err := s.vmMgr.ExecVM(ctx, vmID, req)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(res)

	case "exec-stream":
		var req vm.ExecRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if v, err := s.vmMgr.GetVM(ctx, vmID); err != nil || v == nil {
			httpError(w, "vm not found", http.StatusNotFound)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			httpError(w, "streaming is not supported by this server", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		send := func(ev vm.ExecStreamEvent) error {
			payload, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}
		if err := s.vmMgr.ExecVMStream(r.Context(), vmID, req, send); err != nil {
			code := 1
			_ = send(vm.ExecStreamEvent{Error: err.Error(), ExitCode: &code})
		}

	case "public-ip":
		var req vm.UpdateVMConfigRequest
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		q := r.URL.Query()
		if val := q.Get("public_ip"); val != "" {
			b := strings.ToLower(val) == "on" || strings.ToLower(val) == "true" || val == "1"
			req.PublicIP = &b
			req.PublicIPEnabled = &b
		} else if val := q.Get("enabled"); val != "" {
			b := strings.ToLower(val) == "on" || strings.ToLower(val) == "true" || val == "1"
			req.PublicIP = &b
			req.PublicIPEnabled = &b
		}
		updated, err := s.vmMgr.UpdateVMConfig(ctx, vmID, req)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(updated)

	case "config":
		var req vm.UpdateVMConfigRequest
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		updated, err := s.vmMgr.UpdateVMConfig(ctx, vmID, req)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(updated)

	default:
		httpError(w, "unknown action: "+action, http.StatusBadRequest)
	}
}

func httpError(w http.ResponseWriter, message string, code int) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		customImgs, err := s.vmMgr.ListImages(ctx)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if customImgs == nil {
			customImgs = []*db.CustomImage{}
		}

		type ImageListItem struct {
			ID          string   `json:"id"`
			Name        string   `json:"name"`
			Description string   `json:"description,omitempty"`
			BaseImage   string   `json:"base_image,omitempty"`
			Packages    []string `json:"packages,omitempty"`
			SizeMB      int      `json:"size_mb"`
			Status      string   `json:"status"`
			Kind        string   `json:"kind"`
			CreatedAt   string   `json:"created_at,omitempty"`
		}

		var result []ImageListItem

		defaultRootfs := s.vmMgr.GetStorageManager().GetConfig().DefaultRootfs
		cleanBaseName := strings.TrimSuffix(defaultRootfs, ".ext4")
		result = append(result, ImageListItem{
			ID:          "img-base-ubuntu-24",
			Name:        cleanBaseName,
			Description: "Ubuntu 24.04 LTS (Noble Numbat) official MicroVM base image",
			BaseImage:   "ubuntu-24.04",
			Packages:    []string{"openssh-server", "sudo", "curl", "wget", "git", "python3", "nodejs", "npm", "jq"},
			SizeMB:      4096,
			Status:      "ready",
			Kind:        "base",
		})

		for _, av := range s.vmMgr.GetStorageManager().GetConfig().AvailableImages {
			if av.Name != cleanBaseName {
				result = append(result, ImageListItem{
					ID:          "img-base-" + av.Name,
					Name:        av.Name,
					Description: av.Description,
					Status:      "ready",
					Kind:        "base",
				})
			}
		}

		for _, c := range customImgs {
			createdStr := ""
			if !c.CreatedAt.IsZero() {
				createdStr = c.CreatedAt.Format(time.RFC3339)
			}
			result = append(result, ImageListItem{
				ID:          c.ID,
				Name:        c.Name,
				Description: c.Description,
				BaseImage:   c.BaseImage,
				Packages:    c.Packages,
				SizeMB:      c.SizeMB,
				Status:      string(c.Status),
				Kind:        "custom",
				CreatedAt:   createdStr,
			})
		}

		json.NewEncoder(w).Encode(result)

	case http.MethodPost:
		var req vm.BuildImageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		img, err := s.vmMgr.BuildImage(ctx, req)
		if err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(img)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleImageActions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/images/")
	nameOrID := strings.Trim(path, "/")
	if nameOrID == "" {
		httpError(w, "missing image name or id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		img, err := s.vmMgr.GetImage(ctx, nameOrID)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if img == nil {
			defaultRootfs := s.vmMgr.GetStorageManager().GetConfig().DefaultRootfs
			cleanBaseName := strings.TrimSuffix(defaultRootfs, ".ext4")
			if strings.EqualFold(nameOrID, cleanBaseName) || strings.EqualFold(nameOrID, "ubuntu-24") || strings.EqualFold(nameOrID, "ubuntu") {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":          "img-base-ubuntu-24",
					"name":        cleanBaseName,
					"description": "Ubuntu 24.04 LTS MicroVM base image",
					"status":      "ready",
					"kind":        "base",
				})
				return
			}
			httpError(w, "image not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(img)

	case http.MethodDelete:
		if err := s.vmMgr.DeleteImage(ctx, nameOrID); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": nameOrID})

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		tpls, err := s.vmMgr.ListTemplates(ctx)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if tpls == nil {
			tpls = []*db.Template{}
		}
		json.NewEncoder(w).Encode(tpls)

	case http.MethodPost:
		var req vm.CreateTemplateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		tpl, err := s.vmMgr.CreateTemplate(ctx, req)
		if err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(tpl)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleTemplateActions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/templates/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		httpError(w, "missing template name or id", http.StatusBadRequest)
		return
	}

	nameOrID := parts[0]

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			tpl, err := s.vmMgr.GetTemplate(ctx, nameOrID)
			if err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if tpl == nil {
				httpError(w, "template not found", http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(tpl)

		case http.MethodDelete:
			if err := s.vmMgr.DeleteTemplate(ctx, nameOrID); err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": nameOrID})

		default:
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	action := parts[1]
	if action == "launch" {
		if r.Method != http.MethodPost {
			httpError(w, "launch requires POST", http.StatusMethodNotAllowed)
			return
		}
		var req vm.CreateRequest
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		launchedVM, err := s.vmMgr.LaunchTemplate(ctx, nameOrID, req)
		if err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(launchedVM)
		return
	}

	httpError(w, "unknown action: "+action, http.StatusBadRequest)
}

type ClusterSnapshot struct {
	Type          string                  `json:"type"`
	Stats         *vm.StatsResult         `json:"stats,omitempty"`
	VMs           []*db.VM                `json:"vms"`
	Allocations   []*db.NetworkAllocation `json:"allocations"`
	NetworkConfig network.Config          `json:"network_config"`
	Config        config.GlobalConfig     `json:"config"`
	Queue         map[string]interface{}  `json:"queue"`
	Timestamp     time.Time               `json:"timestamp"`
}

func (s *Server) getClusterSnapshot(ctx context.Context) *ClusterSnapshot {
	stats, _ := s.vmMgr.GetStats(ctx)
	vms, _ := s.vmMgr.ListVMs(ctx)
	if vms == nil {
		vms = []*db.VM{}
	}
	allocs, _ := s.vmMgr.GetStore().GetNetworkAllocations(ctx)
	if allocs == nil {
		allocs = []*db.NetworkAllocation{}
	}
	netCfg := s.vmMgr.GetNetworkManager().GetConfig()

	cfg := s.cfg
	cfg.Resources = s.vmMgr.GetResourceLimits()
	cfg.Network = netCfg
	cfg.Defaults = s.vmMgr.GetDefaults()

	queue, _ := s.vmMgr.GetQueue(ctx)
	if queue == nil {
		queue = []*vm.QueueItem{}
	}
	queueData := map[string]interface{}{
		"queued_vms":      queue,
		"count":           len(queue),
		"running_vms":     0,
		"running_vcpus":   0,
		"running_mem_mb":  0,
		"resource_limits": s.vmMgr.GetResourceLimits(),
	}
	if stats != nil {
		queueData["running_vms"] = stats.RunningVMs
		queueData["running_vcpus"] = stats.RunningVCPUs
		queueData["running_mem_mb"] = stats.RunningMemoryMB
	}

	return &ClusterSnapshot{
		Type:          "snapshot",
		Stats:         stats,
		VMs:           vms,
		Allocations:   allocs,
		NetworkConfig: netCfg,
		Config:        cfg,
		Queue:         queueData,
		Timestamp:     time.Now().UTC(),
	}
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	opts := &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusInternalError, "server closed")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Initial snapshot
	snapshot := s.getClusterSnapshot(ctx)
	if data, err := json.Marshal(snapshot); err == nil {
		_ = conn.Write(ctx, websocket.MessageText, data)
	}

	// Incoming message loop
	go func() {
		defer cancel()
		for {
			msgType, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if msgType == websocket.MessageText {
				var req map[string]interface{}
				_ = json.Unmarshal(data, &req)
				snap := s.getClusterSnapshot(ctx)
				if respBytes, err := json.Marshal(snap); err == nil {
					_ = conn.Write(ctx, websocket.MessageText, respBytes)
				}
			}
		}
	}()

	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return
		case <-ticker.C:
			snap := s.getClusterSnapshot(ctx)
			data, err := json.Marshal(snap)
			if err != nil {
				continue
			}
			if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
				return
			}
		}
	}
}

type CreateKeyRequest struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

func (s *Server) handleKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		keys, err := s.vmMgr.GetStore().ListSSHKeys(r.Context(), "default")
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed to list keys: %v"}`, err), http.StatusInternalServerError)
			return
		}
		if keys == nil {
			keys = []*db.SSHKey{}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"keys": keys})

	case http.MethodPost:
		var req CreateKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request payload"}`, http.StatusBadRequest)
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		req.PublicKey = strings.TrimSpace(req.PublicKey)
		if req.Name == "" || req.PublicKey == "" {
			http.Error(w, `{"error":"name and public_key are required"}`, http.StatusBadRequest)
			return
		}

		parsedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(req.PublicKey))
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"invalid public key format: %v"}`, err), http.StatusBadRequest)
			return
		}

		fingerprint := ssh.FingerprintSHA256(parsedKey)
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		keyID := fmt.Sprintf("ssh_%s", hex.EncodeToString(b))

		key := &db.SSHKey{
			ID:          keyID,
			OwnerID:     "default",
			Name:        req.Name,
			PublicKey:   req.PublicKey,
			Fingerprint: fingerprint,
			CreatedAt:   time.Now().UTC(),
		}

		if err := s.vmMgr.GetStore().SaveSSHKey(r.Context(), key); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed to save key: %v"}`, err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(key)

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleKeyActions(w http.ResponseWriter, r *http.Request) {
	idOrName := strings.TrimPrefix(r.URL.Path, "/api/v1/keys/")
	idOrName = strings.TrimSpace(idOrName)
	if idOrName == "" {
		http.Error(w, `{"error":"key id or name required"}`, http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		key, err := s.vmMgr.GetStore().GetSSHKey(r.Context(), idOrName)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed to get key: %v"}`, err), http.StatusInternalServerError)
			return
		}
		if key == nil {
			http.Error(w, fmt.Sprintf(`{"error":"key not found: %s"}`, idOrName), http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(key)

	case http.MethodDelete:
		key, err := s.vmMgr.GetStore().GetSSHKey(r.Context(), idOrName)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed to query key: %v"}`, err), http.StatusInternalServerError)
			return
		}
		if key == nil {
			http.Error(w, fmt.Sprintf(`{"error":"key not found: %s"}`, idOrName), http.StatusNotFound)
			return
		}
		if err := s.vmMgr.GetStore().DeleteSSHKey(r.Context(), key.ID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"failed to delete key: %v"}`, err), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": key.ID})

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *Server) resolveSSHKey(keyPath, pubKey, keyName string) []ssh.AuthMethod {
	var methods []ssh.AuthMethod

	candidates := []string{}
	if keyPath != "" {
		candidates = append(candidates, keyPath)
	}
	if s.cfg.Images.SSHKeyPath != "" {
		candidates = append(candidates, s.cfg.Images.SSHKeyPath)
	}
	candidates = append(candidates, "/var/lib/compute/images/ssh/id_ed25519")

	if home, err := os.UserHomeDir(); err == nil {
		sshDir := filepath.Join(home, ".ssh")
		if keyName != "" {
			for _, ext := range []string{"", ".pem", ".id"} {
				candidates = append(candidates, filepath.Join(sshDir, keyName+ext))
			}
		}
		for _, name := range []string{"id_ed25519", "prox", "id_rsa", "id_ecdsa"} {
			candidates = append(candidates, filepath.Join(sshDir, name))
		}
	}

	seen := make(map[string]bool)
	var signers []ssh.Signer
	for _, cand := range candidates {
		if cand == "" || seen[cand] {
			continue
		}
		seen[cand] = true
		data, err := os.ReadFile(cand)
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			continue
		}
		signers = append(signers, signer)
	}
	if len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}

	return methods
}

func (s *Server) handleSSHDirect(w http.ResponseWriter, r *http.Request) {
	vmID := strings.TrimPrefix(r.URL.Path, "/api/v1/ssh/")
	vmID = strings.Trim(vmID, "/")
	if vmID == "" {
		httpError(w, "missing vm id", http.StatusBadRequest)
		return
	}
	s.handleVMSSH(w, r, vmID)
}

func (s *Server) handleVMSSH(w http.ResponseWriter, r *http.Request, vmID string) {
	v, err := s.vmMgr.GetVM(r.Context(), vmID)
	isWS := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	if err != nil || v == nil {
		if isWS {
			opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
			if conn, wsErr := websocket.Accept(w, r, opts); wsErr == nil {
				_ = conn.Write(r.Context(), websocket.MessageText, []byte("\r\n\x1b[31;1m[Compute Error]\x1b[0m VM not found: "+vmID+"\r\n"))
				_ = conn.Close(websocket.StatusNormalClosure, "vm not found")
			}
		} else {
			httpError(w, "vm not found", http.StatusNotFound)
		}
		return
	}

	if v.Status != "running" {
		if isWS {
			opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
			if conn, wsErr := websocket.Accept(w, r, opts); wsErr == nil {
				msg := fmt.Sprintf("\r\n\x1b[33;1m[Compute Notice]\x1b[0m VM '%s' is currently %s.\r\nPlease start the instance to open an SSH session.\r\n", v.Name, v.Status)
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(msg))
				_ = conn.Close(websocket.StatusNormalClosure, "vm not running")
			}
		} else {
			httpError(w, fmt.Sprintf("vm %s is not running (status: %s)", vmID, v.Status), http.StatusBadRequest)
		}
		return
	}

	if v.IPAddress == "" {
		if isWS {
			opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
			if conn, wsErr := websocket.Accept(w, r, opts); wsErr == nil {
				msg := fmt.Sprintf("\r\n\x1b[31;1m[Compute Error]\x1b[0m VM '%s' has no allocated IP address.\r\n", v.Name)
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(msg))
				_ = conn.Close(websocket.StatusNormalClosure, "no ip address")
			}
		} else {
			httpError(w, "vm has no allocated IP address", http.StatusBadRequest)
		}
		return
	}

	opts := &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	}
	wsConn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer wsConn.Close(websocket.StatusInternalError, "session closed")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var writeMu sync.Mutex
	safeWrite := func(typ websocket.MessageType, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return wsConn.Write(ctx, typ, data)
	}

	user := r.URL.Query().Get("user")
	if user == "" {
		user = "compute"
	}

	cmd := r.URL.Query().Get("cmd")
	if cmd == "" {
		cmd = r.URL.Query().Get("command")
	}

	ptyParam := r.URL.Query().Get("pty")
	usePTY := true
	if cmd != "" {
		usePTY = (ptyParam == "true" || ptyParam == "1")
	} else {
		usePTY = (ptyParam != "false" && ptyParam != "0")
	}

	cols := 80
	rows := 24
	if c := r.URL.Query().Get("cols"); c != "" {
		if val, convErr := strconv.Atoi(c); convErr == nil && val > 0 {
			cols = val
		}
	}
	if rw := r.URL.Query().Get("rows"); rw != "" {
		if val, convErr := strconv.Atoi(rw); convErr == nil && val > 0 {
			rows = val
		}
	}
	termType := r.URL.Query().Get("term")
	if termType == "" {
		termType = "xterm-256color"
	}

	authMethods := s.resolveSSHKey(v.SSHKeyPath, v.SSHPublicKey, v.SSHKeyName)
	if pwd := r.URL.Query().Get("password"); pwd != "" {
		authMethods = append(authMethods, ssh.Password(pwd))
	}

	if len(authMethods) == 0 {
		errMsg := "\r\n\x1b[31;1m[Compute SSH Error]\x1b[0m No usable SSH private key found on host to authenticate into VM.\r\n"
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}

	sshConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	dest := net.JoinHostPort(v.IPAddress, "22")
	sshClient, err := ssh.Dial("tcp", dest, sshConfig)
	if err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute SSH Connection Failed]\x1b[0m %v\r\n\r\n\x1b[33mEnsure the VM guest SSH service has finished initializing.\x1b[0m\r\n", err)
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}
	defer sshClient.Close()

	session, err := sshClient.NewSession()
	if err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute SSH Session Failed]\x1b[0m %v\r\n", err)
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}
	defer session.Close()

	if usePTY {
		modes := ssh.TerminalModes{
			ssh.ECHO:          1,
			ssh.TTY_OP_ISPEED: 14400,
			ssh.TTY_OP_OSPEED: 14400,
		}
		if err := session.RequestPty(termType, rows, cols, modes); err != nil {
			errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute PTY Allocation Failed]\x1b[0m %v\r\n", err)
			_ = safeWrite(websocket.MessageText, []byte(errMsg))
			return
		}
	}

	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return
	}
	defer stdinPipe.Close()

	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return
	}
	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return
	}

	if cmd != "" {
		err = session.Start(cmd)
	} else {
		err = session.Shell()
	}
	if err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute Exec Failed]\x1b[0m %v\r\n", err)
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}

	pipeOutput := func(rd io.Reader) {
		buf := make([]byte, 4096)
		for {
			n, rErr := rd.Read(buf)
			if n > 0 {
				if wErr := safeWrite(websocket.MessageBinary, buf[:n]); wErr != nil {
					cancel()
					return
				}
			}
			if rErr != nil {
				cancel()
				return
			}
		}
	}
	go pipeOutput(stdoutPipe)
	go pipeOutput(stderrPipe)

	// Inbound loop: WebSocket -> SSH stdin / resize
	go func() {
		defer cancel()
		for {
			msgType, data, rErr := wsConn.Read(ctx)
			if rErr != nil {
				return
			}
			if msgType == websocket.MessageText && len(data) > 0 && data[0] == '{' {
				var ctrl struct {
					Type string `json:"type"`
					Cols int    `json:"cols"`
					Rows int    `json:"rows"`
				}
				if jErr := json.Unmarshal(data, &ctrl); jErr == nil && ctrl.Type == "resize" {
					if usePTY && ctrl.Cols > 0 && ctrl.Rows > 0 {
						_ = session.WindowChange(ctrl.Rows, ctrl.Cols)
					}
					continue
				}
			}
			if _, wErr := stdinPipe.Write(data); wErr != nil {
				return
			}
		}
	}()

	waitErr := session.Wait()
	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*ssh.ExitError); ok {
			exitCode = exitErr.ExitStatus()
		} else {
			exitCode = 1
		}
	}
	exitMsg, _ := json.Marshal(map[string]interface{}{
		"type": "exit",
		"code": exitCode,
	})
	_ = safeWrite(websocket.MessageText, exitMsg)
	_ = wsConn.Close(websocket.StatusNormalClosure, fmt.Sprintf("exit %d", exitCode))
}

func (s *Server) handleVMTerminal(w http.ResponseWriter, r *http.Request, vmID string) {
	v, err := s.vmMgr.GetVM(r.Context(), vmID)
	if err != nil || v == nil {
		opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
		if conn, wsErr := websocket.Accept(w, r, opts); wsErr == nil {
			_ = conn.Write(r.Context(), websocket.MessageText, []byte("\r\n\x1b[31;1m[Compute Error]\x1b[0m VM not found: "+vmID+"\r\n"))
			_ = conn.Close(websocket.StatusNormalClosure, "vm not found")
		} else {
			httpError(w, "vm not found", http.StatusNotFound)
		}
		return
	}

	if v.Status != "running" {
		opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
		if conn, wsErr := websocket.Accept(w, r, opts); wsErr == nil {
			msg := fmt.Sprintf("\r\n\x1b[33;1m[Compute Notice]\x1b[0m VM '%s' is currently %s.\r\nPlease start the instance to open an SSH terminal session.\r\n", v.Name, v.Status)
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(msg))
			_ = conn.Close(websocket.StatusNormalClosure, "vm not running")
		} else {
			httpError(w, fmt.Sprintf("vm %s is not running (status: %s)", vmID, v.Status), http.StatusBadRequest)
		}
		return
	}

	if v.IPAddress == "" {
		opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
		if conn, wsErr := websocket.Accept(w, r, opts); wsErr == nil {
			msg := fmt.Sprintf("\r\n\x1b[31;1m[Compute Error]\x1b[0m VM '%s' has no allocated IP address.\r\n", v.Name)
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(msg))
			_ = conn.Close(websocket.StatusNormalClosure, "no ip address")
		} else {
			httpError(w, "vm has no allocated IP address", http.StatusBadRequest)
		}
		return
	}

	opts := &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	}
	wsConn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer wsConn.Close(websocket.StatusInternalError, "session closed")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var writeMu sync.Mutex
	safeWrite := func(typ websocket.MessageType, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return wsConn.Write(ctx, typ, data)
	}

	user := r.URL.Query().Get("user")
	if user == "" {
		user = "compute"
	}

	authMethods := s.resolveSSHKey(v.SSHKeyPath, v.SSHPublicKey, v.SSHKeyName)
	if pwd := r.URL.Query().Get("password"); pwd != "" {
		authMethods = append(authMethods, ssh.Password(pwd))
	}

	if len(authMethods) == 0 {
		errMsg := "\r\n\x1b[31;1m[Compute SSH Error]\x1b[0m No usable SSH private key found on host to authenticate into VM.\r\n"
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}

	sshConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	dest := net.JoinHostPort(v.IPAddress, "22")
	_ = safeWrite(websocket.MessageText, []byte(fmt.Sprintf("\x1b[36m[Compute]\x1b[0m Establishing SSH terminal session to %s@%s...\r\n", user, dest)))

	sshClient, err := ssh.Dial("tcp", dest, sshConfig)
	if err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute SSH Connection Failed]\x1b[0m %v\r\n\r\n\x1b[33mEnsure the VM guest SSH service has finished initializing.\x1b[0m\r\n", err)
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}
	defer sshClient.Close()

	session, err := sshClient.NewSession()
	if err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute SSH Session Failed]\x1b[0m %v\r\n", err)
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}
	defer session.Close()

	cols := 100
	rows := 30
	if c := r.URL.Query().Get("cols"); c != "" {
		if val, convErr := strconv.Atoi(c); convErr == nil && val > 0 {
			cols = val
		}
	}
	if rw := r.URL.Query().Get("rows"); rw != "" {
		if val, convErr := strconv.Atoi(rw); convErr == nil && val > 0 {
			rows = val
		}
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	termType := r.URL.Query().Get("term")
	if termType == "" {
		termType = "xterm-256color"
	}
	if err := session.RequestPty(termType, rows, cols, modes); err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute PTY Allocation Failed]\x1b[0m %v\r\n", err)
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}

	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return
	}
	defer stdinPipe.Close()

	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return
	}
	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return
	}

	if err := session.Shell(); err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31;1m[Compute Shell Exec Failed]\x1b[0m %v\r\n", err)
		_ = safeWrite(websocket.MessageText, []byte(errMsg))
		return
	}

	pipeOutput := func(rd io.Reader) {
		buf := make([]byte, 4096)
		for {
			n, rErr := rd.Read(buf)
			if n > 0 {
				if wErr := safeWrite(websocket.MessageBinary, buf[:n]); wErr != nil {
					cancel()
					return
				}
			}
			if rErr != nil {
				cancel()
				return
			}
		}
	}
	go pipeOutput(stdoutPipe)
	go pipeOutput(stderrPipe)

	// Inbound loop: WebSocket -> SSH stdin / resize
	go func() {
		defer cancel()
		for {
			msgType, data, rErr := wsConn.Read(ctx)
			if rErr != nil {
				return
			}
			if msgType == websocket.MessageText && len(data) > 0 && data[0] == "{"[0] {
				var ctrl struct {
					Type string `json:"type"`
					Cols int    `json:"cols"`
					Rows int    `json:"rows"`
				}
				if jErr := json.Unmarshal(data, &ctrl); jErr == nil && ctrl.Type == "resize" {
					if ctrl.Cols > 0 && ctrl.Rows > 0 {
						_ = session.WindowChange(ctrl.Rows, ctrl.Cols)
					}
					continue
				}
			}
			if _, wErr := stdinPipe.Write(data); wErr != nil {
				return
			}
		}
	}()

	_ = session.Wait()
	_ = safeWrite(websocket.MessageText, []byte("\r\n\x1b[33m[Session closed by remote host]\x1b[0m\r\n"))
	_ = wsConn.Close(websocket.StatusNormalClosure, "session exited")
}

// Worker and Job endpoints

func (s *Server) handleWorkerConnect(w http.ResponseWriter, r *http.Request) {
	opts := &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	_, msgBytes, err := conn.Read(ctx)
	if err != nil {
		return
	}
	var hello api.WorkerHello
	if err := json.Unmarshal(msgBytes, &hello); err != nil {
		return
	}

	if hello.Token != "" && s.vmMgr != nil && s.vmMgr.GetStore() != nil {
		_, _ = s.vmMgr.GetStore().ConsumeWorkerToken(ctx, hello.Token)
	}

	workerID := hello.ID
	if workerID == "" {
		workerID = hello.Name
	}
	if workerID == "" {
		workerID = fmt.Sprintf("nod_%s", uuid.NewString()[:8])
	}

	ack := api.WorkerHelloAck{
		WorkerMessage: api.WorkerMessage{
			Version:  1,
			Type:     "worker.hello_ack",
			ID:       uuid.NewString(),
			WorkerID: workerID,
		},
		Accepted:            true,
		WorkerID:            workerID,
		HeartbeatIntervalMS: 10000,
	}
	ackBytes, _ := json.Marshal(ack)
	if err := conn.Write(ctx, websocket.MessageText, ackBytes); err != nil {
		return
	}

	workerObj := scheduler.NewWorker(workerID, "default", hello.Capabilities, conn)
	if s.vmMgr != nil && s.vmMgr.GetStore() != nil {
		now := time.Now().UTC()
		capJSON, _ := json.Marshal(hello.Capabilities)
		_ = s.vmMgr.GetStore().SaveWorkerNode(ctx, &db.WorkerNode{
			ID:               workerID,
			Name:             workerID,
			PoolID:           "default",
			Role:             "worker",
			Status:           "ready",
			Arch:             hello.Architecture,
			AgentVersion:     hello.AgentVersion,
			CapabilitiesJSON: string(capJSON),
			LastHeartbeat:    &now,
			CreatedAt:        now,
			UpdatedAt:        now,
		})
	}

	var handler func(context.Context, *scheduler.Worker, api.WorkerMessage, json.RawMessage) error
	if s.scheduler != nil {
		handler = func(c context.Context, wrk *scheduler.Worker, env api.WorkerMessage, raw json.RawMessage) error {
			return s.scheduler.HandleWorkerMessage(c, wrk, env, []byte(raw))
		}
	}

	_ = s.workers.ServeStream(ctx, workerObj, handler)
}

func (s *Server) handleWorkerTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}

	var req api.WorkerTokenCreateRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	poolID := req.PoolID
	if poolID == "" {
		poolID = "default"
	}
	tokenStr := fmt.Sprintf("mft_%s", uuid.NewString()[:16])
	expiresAt := time.Now().UTC().Add(30 * time.Minute)

	token := &db.WorkerToken{
		Token:      tokenStr,
		PoolID:     poolID,
		LabelsJSON: "{}",
		ExpiresAt:  expiresAt,
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.vmMgr.GetStore().SaveWorkerToken(r.Context(), token); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	expUnix := expiresAt.Unix()
	_ = json.NewEncoder(w).Encode(api.WorkerTokenResponse{
		ID:        tokenStr,
		Name:      req.Name,
		Token:     tokenStr,
		Prefix:    tokenStr[:8],
		ExpiresAt: &expUnix,
	})
}

func (s *Server) handleWorkers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		json.NewEncoder(w).Encode([]*db.WorkerNode{})
		return
	}
	nodes, err := s.vmMgr.GetStore().ListWorkerNodes(r.Context())
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if nodes == nil {
		nodes = []*db.WorkerNode{}
	}
	json.NewEncoder(w).Encode(nodes)
}

func (s *Server) handleWorkerActions(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/workers/")
	parts := strings.Split(path, "/")
	id := parts[0]
	if id == "" {
		httpError(w, "worker id required", http.StatusBadRequest)
		return
	}

	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			node, err := s.vmMgr.GetStore().GetWorkerNode(r.Context(), id)
			if err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if node == nil {
				httpError(w, "worker not found", http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(node)
		case http.MethodDelete:
			if err := s.vmMgr.GetStore().DeleteWorkerNode(r.Context(), id); err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	action := parts[1]
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch action {
	case "cordon":
		if err := s.vmMgr.GetStore().UpdateWorkerNodeStatus(r.Context(), id, "cordoned"); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "cordoned"})
	case "drain":
		if err := s.vmMgr.GetStore().UpdateWorkerNodeStatus(r.Context(), id, "draining"); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "draining"})
	default:
		httpError(w, "unknown action", http.StatusNotFound)
	}
}

func (s *Server) handlePools(w http.ResponseWriter, r *http.Request) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		pools, err := s.vmMgr.GetStore().ListWorkerPools(r.Context())
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if pools == nil {
			pools = []*db.WorkerPool{}
		}
		json.NewEncoder(w).Encode(pools)
	case http.MethodPost:
		var req api.PoolCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		pool := &db.WorkerPool{
			ID:         uuid.NewString(),
			Name:       req.Name,
			LabelsJSON: "{}",
			PolicyJSON: "{}",
			CreatedAt:  time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
		}
		if err := s.vmMgr.GetStore().SaveWorkerPool(r.Context(), pool); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(pool)
	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePoolActions(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/pools/")
	if id == "" {
		httpError(w, "pool id required", http.StatusBadRequest)
		return
	}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		pool, err := s.vmMgr.GetStore().GetWorkerPool(r.Context(), id)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if pool == nil {
			httpError(w, "pool not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(pool)
	case http.MethodDelete:
		if err := s.vmMgr.GetStore().DeleteWorkerPool(r.Context(), id); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		jobs, err := s.vmMgr.GetStore().ListJobs(r.Context(), "")
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if jobs == nil {
			jobs = []*db.Job{}
		}
		json.NewEncoder(w).Encode(jobs)
	case http.MethodPost:
		var req api.JobCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		specJSON, _ := json.Marshal(req.Spec)
		jobID := fmt.Sprintf("job_%s", uuid.NewString()[:8])
		job := &db.Job{
			ID:        jobID,
			OwnerID:   "default",
			PoolID:    req.PoolID,
			Type:      req.Type,
			State:     "queued",
			Priority:  req.Priority,
			SpecJSON:  string(specJSON),
			QueuedAt:  time.Now().UTC(),
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		if job.Type == "" {
			job.Type = "command"
		}
		if err := s.vmMgr.GetStore().SaveJob(r.Context(), job); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if s.scheduler != nil {
			go func() {
				_, _ = s.scheduler.ScheduleNext(context.Background())
			}()
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(job)
	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleJobActions(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/jobs/")
	parts := strings.Split(path, "/")
	id := parts[0]
	if id == "" {
		httpError(w, "job id required", http.StatusBadRequest)
		return
	}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}

	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		job, err := s.vmMgr.GetStore().GetJob(r.Context(), id)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if job == nil {
			httpError(w, "job not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(job)
		return
	}

	action := parts[1]
	switch action {
	case "cancel":
		if r.Method != http.MethodPost {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_ = s.vmMgr.GetStore().UpdateJobState(r.Context(), id, "canceled", "")
		if s.jobRunner != nil {
			s.jobRunner.Cancel(id)
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "canceled"})
	case "logs":
		if r.Method != http.MethodGet {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		logs, err := s.vmMgr.GetStore().GetJobLogs(r.Context(), id)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if logs == nil {
			logs = []*db.JobLog{}
		}
		json.NewEncoder(w).Encode(logs)
	default:
		httpError(w, "unknown action", http.StatusNotFound)
	}
}
