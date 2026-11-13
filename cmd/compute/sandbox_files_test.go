package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSplitRemoteRef(t *testing.T) {
	tests := []struct {
		input       string
		wantSandbox string
		wantPath    string
		wantRemote  bool
	}{
		{"mybox:/workspace/app.py", "mybox", "/workspace/app.py", true},
		{"ins_12345:/etc/hosts", "ins_12345", "/etc/hosts", true},
		{"./local/path.txt", "", "./local/path.txt", false},
		{"./logs/2026-11-13:00.log", "", "./logs/2026-11-13:00.log", false},
		{"/var/log:1/syslog", "", "/var/log:1/syslog", false},
		{"relative/file.go", "", "relative/file.go", false},
		{"/var/log/syslog", "", "/var/log/syslog", false},
		{"C:\\Windows\\System32", "", "C:\\Windows\\System32", false},
		{"c:/users/test", "", "c:/users/test", false},
	}

	for _, tc := range tests {
		sb, p, isRemote := splitRemoteRef(tc.input)
		if isRemote != tc.wantRemote {
			t.Errorf("splitRemoteRef(%q) isRemote = %v, want %v", tc.input, isRemote, tc.wantRemote)
		}
		if sb != tc.wantSandbox {
			t.Errorf("splitRemoteRef(%q) sandbox = %q, want %q", tc.input, sb, tc.wantSandbox)
		}
		if p != tc.wantPath {
			t.Errorf("splitRemoteRef(%q) path = %q, want %q", tc.input, p, tc.wantPath)
		}
	}
}

func TestSplitFlagAndPositionalArgs(t *testing.T) {
	valueFlags := map[string]bool{
		"format":  true,
		"content": true,
		"file":    true,
	}

	// Case 1: space-separated flags after positional
	args1 := []string{"mybox", "/workspace/app.py", "--format", "base64"}
	flags1, pos1 := splitFlagAndPositionalArgs(args1, valueFlags)
	if len(flags1) != 2 || flags1[0] != "--format" || flags1[1] != "base64" {
		t.Errorf("unexpected flags1: %v", flags1)
	}
	if len(pos1) != 2 || pos1[0] != "mybox" || pos1[1] != "/workspace/app.py" {
		t.Errorf("unexpected pos1: %v", pos1)
	}

	// Case 2: flags before positional with =
	args2 := []string{"--format=text", "--content=hello", "mybox", "/path.txt"}
	flags2, pos2 := splitFlagAndPositionalArgs(args2, valueFlags)
	if len(flags2) != 2 || flags2[0] != "--format=text" || flags2[1] != "--content=hello" {
		t.Errorf("unexpected flags2: %v", flags2)
	}
	if len(pos2) != 2 || pos2[0] != "mybox" || pos2[1] != "/path.txt" {
		t.Errorf("unexpected pos2: %v", pos2)
	}

	// Case 3: space-separated content with empty string and dash
	args3 := []string{"mybox", "/path.txt", "--content", ""}
	flags3, pos3 := splitFlagAndPositionalArgs(args3, valueFlags)
	if len(flags3) != 2 || flags3[0] != "--content" || flags3[1] != "" {
		t.Errorf("unexpected flags3: %v", flags3)
	}
	if len(pos3) != 2 || pos3[0] != "mybox" || pos3[1] != "/path.txt" {
		t.Errorf("unexpected pos3: %v", pos3)
	}
}

func TestClientFileOperations(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sandboxes/sb-1/files/write":
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req["path"] != "/workspace/hello.txt" {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "size": len(req["content"])})

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sandboxes/sb-1/files/mkdir":
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req["path"] != "/workspace/newdir" {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "path": req["path"]})

		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sandboxes/sb-1/files/read":
			p := r.URL.Query().Get("path")
			if p != "/workspace/hello.txt" {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(SandboxFileReadResult{
				Path:    p,
				Content: "Hello world from sandbox",
				Format:  "text",
				Size:    24,
			})

		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sandboxes/sb-1/files/list":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"entries": []SandboxFileEntry{
					{Name: "app.py", Path: "/workspace/app.py", IsDir: false, Size: 1024},
					{Name: "src", Path: "/workspace/src", IsDir: true, Size: 4096},
				},
			})

		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/sandboxes/sb-1/files":
			p := r.URL.Query().Get("path")
			if p != "/workspace/hello.txt" {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "path": p})

		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client := NewDaemonClient(ts.URL)
	ctx := context.Background()

	// 1. WriteFile
	if err := client.WriteFile(ctx, "sb-1", "/workspace/hello.txt", "hello", "text"); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// 2. MkdirFile
	if err := client.MkdirFile(ctx, "sb-1", "/workspace/newdir"); err != nil {
		t.Fatalf("MkdirFile failed: %v", err)
	}

	// 3. ReadFileDetails
	res, err := client.ReadFileDetails(ctx, "sb-1", "/workspace/hello.txt", "text")
	if err != nil {
		t.Fatalf("ReadFileDetails failed: %v", err)
	}
	if res.Content != "Hello world from sandbox" || res.Size != 24 {
		t.Errorf("unexpected read result: %+v", res)
	}

	// 4. ListFilesDetails
	entries, err := client.ListFilesDetails(ctx, "sb-1", "/workspace")
	if err != nil {
		t.Fatalf("ListFilesDetails failed: %v", err)
	}
	if len(entries) != 2 || entries[0].Name != "app.py" || !entries[1].IsDir {
		t.Errorf("unexpected list entries: %+v", entries)
	}

	// 5. RemoveFile
	if err := client.RemoveFile(ctx, "sb-1", "/workspace/hello.txt"); err != nil {
		t.Fatalf("RemoveFile failed: %v", err)
	}
}

func TestClientProcessOperations(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sandboxes/sb-1/processes":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"processes": []SandboxProcessInfo{
					{PID: 1, PPID: 0, User: "root", CPU: 0.1, Memory: 0.2, Status: "Ss", Command: "init", Args: "init"},
					{PID: 42, PPID: 1, User: "compute", CPU: 2.5, Memory: 5.0, Status: "Sl", Command: "python3", Args: "python3 app.py"},
				},
			})

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sandboxes/sb-1/processes":
			var req struct {
				Command string            `json:"command"`
				Cwd     string            `json:"cwd"`
				Env     map[string]string `json:"env"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.Command != "npm start" || req.Cwd != "/app" || req.Env["PORT"] != "3000" {
				http.Error(w, "bad start payload", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "started",
				"pid":     54321,
				"command": req.Command,
			})

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sandboxes/sb-1/processes/kill":
			var req struct {
				PID    int    `json:"pid"`
				Signal string `json:"signal"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.PID != 54321 || req.Signal != "SIGTERM" {
				http.Error(w, "bad kill payload", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "killed",
				"pid":    req.PID,
				"signal": req.Signal,
			})

		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client := NewDaemonClient(ts.URL)
	ctx := context.Background()

	// 1. ListProcessesDetails
	procs, err := client.ListProcessesDetails(ctx, "sb-1")
	if err != nil {
		t.Fatalf("ListProcessesDetails failed: %v", err)
	}
	if len(procs) != 2 || procs[1].PID != 42 || procs[1].User != "compute" {
		t.Errorf("unexpected process list: %+v", procs)
	}

	// 2. StartProcess
	pid, err := client.StartProcess(ctx, "sb-1", "npm start", "/app", map[string]string{"PORT": "3000"})
	if err != nil {
		t.Fatalf("StartProcess failed: %v", err)
	}
	if pid != 54321 {
		t.Errorf("expected pid 54321, got %d", pid)
	}

	// 3. KillProcess
	if err := client.KillProcess(ctx, "sb-1", 54321, "SIGTERM"); err != nil {
		t.Fatalf("KillProcess failed: %v", err)
	}
}
