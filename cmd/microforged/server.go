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
	s.mux.HandleFunc("/api/v1/vms", s.handleVMs)
	s.mux.HandleFunc("/api/v1/vms/", s.handleVMActions)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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

	default:
		httpError(w, "unknown action: "+action, http.StatusBadRequest)
	}
}

func httpError(w http.ResponseWriter, message string, code int) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
