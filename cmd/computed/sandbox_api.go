package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
)

// FileEntry represents an entry in a directory listing.
type FileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// FileReadResponse represents the response when reading a file.
type FileReadResponse struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Format  string `json:"format"` // "text" or "base64"
	Size    int    `json:"size"`
}

// FileWriteRequest represents the payload for writing a file.
type FileWriteRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Format  string `json:"format,omitempty"` // "text" or "base64"
}

// ProcessInfo represents a running process inside the sandbox.
type ProcessInfo struct {
	PID     int     `json:"pid"`
	PPID    int     `json:"ppid"`
	User    string  `json:"user"`
	CPU     float64 `json:"cpu"`
	Memory  float64 `json:"memory"`
	Status  string  `json:"status"`
	Command string  `json:"command"`
	Args    string  `json:"args"`
}

// ProcessStartRequest represents a request to launch a background process.
type ProcessStartRequest struct {
	Command string            `json:"command"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// ProcessKillRequest represents a request to signal or terminate a process.
type ProcessKillRequest struct {
	PID    int    `json:"pid"`
	Signal string `json:"signal,omitempty"` // Default "SIGTERM"
}

// handleVMFiles manages sandbox filesystem operations (read, write, list, mkdir, remove).
func (s *Server) handleVMFiles(w http.ResponseWriter, r *http.Request, vmID string, subParts []string) {
	ctx := r.Context()
	v, err := s.vmMgr.GetVM(ctx, vmID)
	if err != nil || v == nil {
		httpError(w, "sandbox not found", http.StatusNotFound)
		return
	}
	if !s.canAccessOwner(r, v.OwnerID) {
		httpError(w, "sandbox not found", http.StatusNotFound)
		return
	}
	if v.Status != db.StatusRunning {
		httpError(w, "sandbox is not running", http.StatusConflict)
		return
	}

	subAction := ""
	if len(subParts) > 0 {
		subAction = subParts[0]
	}

	switch {
	// Write file: POST /api/v1/sandboxes/:id/files or POST /api/v1/sandboxes/:id/files/write
	case (r.Method == http.MethodPost && (subAction == "" || subAction == "write")):
		var req FileWriteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Path == "" {
			httpError(w, "missing path", http.StatusBadRequest)
			return
		}

		var data []byte
		if strings.ToLower(req.Format) == "base64" {
			decoded, err := base64.StdEncoding.DecodeString(req.Content)
			if err != nil {
				httpError(w, "invalid base64 content: "+err.Error(), http.StatusBadRequest)
				return
			}
			data = decoded
		} else {
			data = []byte(req.Content)
		}

		// Use ExecVMInput to stream data into file cleanly via stdin
		writeCmd := fmt.Sprintf("sh -c 'mkdir -p \"$(dirname \"$1\")\" && cat > \"$1\"' _ %q", req.Path)
		res, err := s.vmMgr.ExecVMInput(ctx, vmID, vm.ExecRequest{
			Command: writeCmd,
			Timeout: 30,
		}, bytes.NewReader(data))
		if err != nil {
			httpError(w, "failed to write file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if res.ExitCode != 0 {
			httpError(w, "write failed: "+res.Stderr, http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "success",
			"path":   req.Path,
			"size":   len(data),
		})

	// Make directory: POST /api/v1/sandboxes/:id/files/mkdir
	case (r.Method == http.MethodPost && subAction == "mkdir"):
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			httpError(w, "missing or invalid path", http.StatusBadRequest)
			return
		}

		mkdirCmd := fmt.Sprintf("mkdir -p %q", req.Path)
		res, err := s.vmMgr.ExecVM(ctx, vmID, vm.ExecRequest{
			Command: mkdirCmd,
			Timeout: 10,
		})
		if err != nil {
			httpError(w, "failed to create directory: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if res.ExitCode != 0 {
			httpError(w, "mkdir failed: "+res.Stderr, http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "success",
			"path":   req.Path,
		})

	// Delete file or dir: DELETE /api/v1/sandboxes/:id/files or POST /api/v1/sandboxes/:id/files/remove
	case (r.Method == http.MethodDelete || (r.Method == http.MethodPost && subAction == "remove")):
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

		rmCmd := fmt.Sprintf("rm -rf %q", path)
		res, err := s.vmMgr.ExecVM(ctx, vmID, vm.ExecRequest{
			Command: rmCmd,
			Timeout: 15,
		})
		if err != nil {
			httpError(w, "failed to delete: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if res.ExitCode != 0 {
			httpError(w, "remove failed: "+res.Stderr, http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "success",
			"path":   path,
		})

	// List directory: GET /api/v1/sandboxes/:id/files/list or GET /api/v1/sandboxes/:id/files?action=list
	case (r.Method == http.MethodGet && (subAction == "list" || r.URL.Query().Get("action") == "list")):
		path := r.URL.Query().Get("path")
		if path == "" {
			path = "/workspace"
		}

		pyCmd := fmt.Sprintf(`python3 -c "import os, sys, json; p = sys.argv[1]; res = []; [res.append({'name': e, 'path': os.path.join(p, e), 'is_dir': os.path.isdir(os.path.join(p, e)), 'size': os.path.getsize(os.path.join(p, e)) if not os.path.islink(os.path.join(p, e)) and os.path.exists(os.path.join(p, e)) else 0}) for e in sorted(os.listdir(p))]; print(json.dumps(res))" %q`, path)
		res, err := s.vmMgr.ExecVM(ctx, vmID, vm.ExecRequest{
			Command: pyCmd,
			Timeout: 15,
		})
		if err == nil && res.ExitCode == 0 {
			var entries []FileEntry
			if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &entries); jsonErr == nil {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"path":    path,
					"entries": entries,
				})
				return
			}
		}

		// Fallback listing using ls
		fallbackCmd := fmt.Sprintf("ls -1p %q", path)
		fbRes, fbErr := s.vmMgr.ExecVM(ctx, vmID, vm.ExecRequest{Command: fallbackCmd, Timeout: 10})
		if fbErr != nil || fbRes.ExitCode != 0 {
			httpError(w, "failed to list directory: "+res.Stderr, http.StatusBadRequest)
			return
		}
		var entries []FileEntry
		for _, line := range strings.Split(strings.TrimSpace(fbRes.Stdout), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			isDir := strings.HasSuffix(line, "/")
			name := strings.TrimSuffix(line, "/")
			entries = append(entries, FileEntry{
				Name:  name,
				Path:  path + "/" + name,
				IsDir: isDir,
				Size:  0,
			})
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"path":    path,
			"entries": entries,
		})

	// Read file: GET /api/v1/sandboxes/:id/files or GET /api/v1/sandboxes/:id/files/read
	case r.Method == http.MethodGet:
		path := r.URL.Query().Get("path")
		if path == "" {
			httpError(w, "missing path query parameter", http.StatusBadRequest)
			return
		}
		reqFormat := strings.ToLower(r.URL.Query().Get("format"))

		readCmd := fmt.Sprintf("base64 %q", path)
		res, err := s.vmMgr.ExecVM(ctx, vmID, vm.ExecRequest{
			Command: readCmd,
			Timeout: 20,
		})
		if err != nil {
			httpError(w, "failed to read file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if res.ExitCode != 0 {
			httpError(w, "file read failed: "+res.Stderr, http.StatusNotFound)
			return
		}

		rawB64 := strings.ReplaceAll(res.Stdout, "\n", "")
		rawB64 = strings.ReplaceAll(rawB64, "\r", "")
		decoded, err := base64.StdEncoding.DecodeString(rawB64)
		if err != nil {
			httpError(w, "failed to decode file data", http.StatusInternalServerError)
			return
		}

		format := "text"
		contentStr := string(decoded)
		if reqFormat == "base64" || !utf8.Valid(decoded) {
			format = "base64"
			contentStr = rawB64
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(FileReadResponse{
			Path:    path,
			Content: contentStr,
			Format:  format,
			Size:    len(decoded),
		})

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleVMProcesses manages sandbox process inspection and execution.
func (s *Server) handleVMProcesses(w http.ResponseWriter, r *http.Request, vmID string, subParts []string) {
	ctx := r.Context()
	v, err := s.vmMgr.GetVM(ctx, vmID)
	if err != nil || v == nil {
		httpError(w, "sandbox not found", http.StatusNotFound)
		return
	}
	if !s.canAccessOwner(r, v.OwnerID) {
		httpError(w, "sandbox not found", http.StatusNotFound)
		return
	}
	if v.Status != db.StatusRunning {
		httpError(w, "sandbox is not running", http.StatusConflict)
		return
	}

	subAction := ""
	if len(subParts) > 0 {
		subAction = subParts[0]
	}

	switch {
	// List processes: GET /api/v1/sandboxes/:id/processes
	case r.Method == http.MethodGet:
		psCmd := "ps -eo pid,ppid,user,%cpu,%mem,stat,comm,args --no-headers"
		res, err := s.vmMgr.ExecVM(ctx, vmID, vm.ExecRequest{
			Command: psCmd,
			Timeout: 10,
		})
		if err != nil {
			httpError(w, "failed to inspect processes: "+err.Error(), http.StatusInternalServerError)
			return
		}

		var processes []ProcessInfo
		lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 7 {
				continue
			}
			pid, _ := strconv.Atoi(fields[0])
			ppid, _ := strconv.Atoi(fields[1])
			cpu, _ := strconv.ParseFloat(fields[3], 64)
			mem, _ := strconv.ParseFloat(fields[4], 64)
			args := strings.Join(fields[7:], " ")
			if args == "" {
				args = fields[6]
			}
			processes = append(processes, ProcessInfo{
				PID:     pid,
				PPID:    ppid,
				User:    fields[2],
				CPU:     cpu,
				Memory:  mem,
				Status:  fields[5],
				Command: fields[6],
				Args:    args,
			})
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"processes": processes,
		})

	// Kill process: POST /api/v1/sandboxes/:id/processes/kill
	case r.Method == http.MethodPost && subAction == "kill":
		var req ProcessKillRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PID <= 0 {
			httpError(w, "invalid process kill request", http.StatusBadRequest)
			return
		}
		sig := req.Signal
		if sig == "" {
			sig = "SIGTERM"
		}
		killCmd := fmt.Sprintf("kill -s %s %d", sig, req.PID)
		res, err := s.vmMgr.ExecVM(ctx, vmID, vm.ExecRequest{
			Command: killCmd,
			Timeout: 10,
		})
		if err != nil {
			httpError(w, "failed to send signal: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if res.ExitCode != 0 {
			httpError(w, "kill failed: "+res.Stderr, http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "killed",
			"pid":    req.PID,
			"signal": sig,
		})

	// Start background process: POST /api/v1/sandboxes/:id/processes
	case r.Method == http.MethodPost && subAction == "":
		var req ProcessStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Command) == "" {
			httpError(w, "missing command", http.StatusBadRequest)
			return
		}

		envPrefix := ""
		for k, val := range req.Env {
			envPrefix += fmt.Sprintf("%s=%q ", k, val)
		}
		cdPrefix := ""
		if req.Cwd != "" {
			cdPrefix = fmt.Sprintf("cd %q && ", req.Cwd)
		}

		startCmd := fmt.Sprintf("sh -c '%s%snohup %s > /tmp/bg-proc.log 2>&1 & echo $!'", cdPrefix, envPrefix, req.Command)
		res, err := s.vmMgr.ExecVM(ctx, vmID, vm.ExecRequest{
			Command: startCmd,
			Timeout: 10,
		})
		if err != nil {
			httpError(w, "failed to start process: "+err.Error(), http.StatusInternalServerError)
			return
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(res.Stdout))

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "started",
			"pid":     pid,
			"command": req.Command,
		})

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePreviewProxy handles reverse-proxying HTTP/WebSocket traffic to microVM ports via unified IngressProxy.
// URL pattern: /preview/:sandbox_id/:port/*
func (s *Server) handlePreviewProxy(w http.ResponseWriter, r *http.Request) {
	if s.ingressProxy != nil {
		s.ingressProxy.ServeHTTP(w, r)
		return
	}
	http.Error(w, "ingress proxy not initialized", http.StatusInternalServerError)
}
