package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
)

type Server struct {
	vmMgr *vm.Manager
	mux   *http.ServeMux
}

func NewServer(vmMgr *vm.Manager) *Server {
	s := &Server{
		vmMgr: vmMgr,
		mux:   http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/api/v1/health", s.handleHealth)
	s.mux.HandleFunc("/api/v1/stats", s.handleStats)
	s.mux.HandleFunc("/api/v1/vms", s.handleVMs)
	s.mux.HandleFunc("/api/v1/vms/", s.handleVMActions)
	s.mux.HandleFunc("/api/v1/network", s.handleNetworkInfo)
	s.mux.HandleFunc("/api/v1/network/allocations", s.handleNetworkAllocations)
	s.mux.HandleFunc("/api/v1/network/inspect/", s.handleNetworkInspect)
	s.mux.HandleFunc("/api/v1/network/prune", s.handleNetworkPrune)
	s.mux.HandleFunc("/api/v1/network/config", s.handleNetworkConfig)
}

func (s *Server) SetGUIHandler(h http.Handler) {
	s.mux.Handle("/", h)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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

	switch action {
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

	default:
		httpError(w, "unknown action: "+action, http.StatusBadRequest)
	}
}

func httpError(w http.ResponseWriter, message string, code int) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
