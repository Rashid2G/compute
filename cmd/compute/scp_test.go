package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
)

func TestParseScpArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantSrcs  []string
		wantDst   string
		wantRec   bool
		wantError bool
	}{
		{name: "push single file", args: []string{"./file", "trigger:/home/"}, wantSrcs: []string{"./file"}, wantDst: "trigger:/home/"},
		{name: "recursive flag short", args: []string{"-r", "./dir", "box:/tmp"}, wantSrcs: []string{"./dir"}, wantDst: "box:/tmp", wantRec: true},
		{name: "recursive flag long", args: []string{"./dir", "--recursive", "box:/tmp"}, wantSrcs: []string{"./dir"}, wantDst: "box:/tmp", wantRec: true},
		{name: "multiple sources", args: []string{"a.txt", "b.txt", "box:/tmp/"}, wantSrcs: []string{"a.txt", "b.txt"}, wantDst: "box:/tmp/"},
		{name: "missing dst", args: []string{"a.txt"}, wantError: true},
		{name: "no args", args: []string{}, wantError: true},
		{name: "unknown flag", args: []string{"-P", "22", "a.txt", "box:/tmp"}, wantError: true},
		{name: "dash ends flags", args: []string{"--", "-r", "weird-file", "box:/tmp"}, wantSrcs: []string{"-r", "weird-file"}, wantDst: "box:/tmp"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srcs, dst, recursive, err := parseScpArgs(tc.args)
			if tc.wantError {
				if err == nil {
					t.Fatalf("expected error for args %v", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(srcs, "|") != strings.Join(tc.wantSrcs, "|") {
				t.Errorf("srcs = %v, want %v", srcs, tc.wantSrcs)
			}
			if dst != tc.wantDst {
				t.Errorf("dst = %q, want %q", dst, tc.wantDst)
			}
			if recursive != tc.wantRec {
				t.Errorf("recursive = %v, want %v", recursive, tc.wantRec)
			}
		})
	}
}

func TestStripUserPrefix(t *testing.T) {
	if got := stripUserPrefix("user@trigger"); got != "trigger" {
		t.Errorf("stripUserPrefix(user@trigger) = %q, want trigger", got)
	}
	if got := stripUserPrefix("trigger"); got != "trigger" {
		t.Errorf("stripUserPrefix(trigger) = %q, want trigger", got)
	}
}

// scpMockDaemon builds a mock daemon server handling the endpoints scp uses.
// execFn receives decoded exec requests so tests can respond per command.
func scpMockDaemon(t *testing.T, vms map[string]*db.VM, execFn func(vmID string, req vm.ExecRequest) vm.ExecResponse, writeFn func(path, content, format string) error, reads map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/vms/") && r.URL.Path != "/api/v1/vms":
			ref := strings.TrimPrefix(r.URL.Path, "/api/v1/vms/")
			v, ok := vms[ref]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(v)

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec"):
			vmID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/vms/"), "/exec")
			var req vm.ExecRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(execFn(vmID, req))

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/files/write"):
			sandboxID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/sandboxes/"), "/files/write")
			if _, ok := vms[sandboxID]; !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			var req struct {
				Path    string `json:"path"`
				Content string `json:"content"`
				Format  string `json:"format"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := writeFn(req.Path, req.Content, req.Format); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "path": req.Path})

		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/files/read"):
			sandboxID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/sandboxes/"), "/files/read")
			if _, ok := vms[sandboxID]; !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			p := r.URL.Query().Get("path")
			content, ok := reads[sandboxID+":"+p]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(SandboxFileReadResult{
				Path:    p,
				Content: content,
				Format:  "base64",
				Size:    len(content),
			})

		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
}

func scpTestVM(id, name string) *db.VM {
	return &db.VM{ID: id, Name: name, Status: db.StatusRunning}
}

func mustWriteLocalTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunScpPushFile(t *testing.T) {
	localDir := t.TempDir()
	localFile := filepath.Join(localDir, "hello.txt")
	if err := os.WriteFile(localFile, []byte("hello scp"), 0o644); err != nil {
		t.Fatal(err)
	}

	var writtenPath, writtenData string
	sb := scpTestVM("sb-1", "trigger")
	ts := scpMockDaemon(t,
		map[string]*db.VM{"trigger": sb, "sb-1": sb},
		func(vmID string, req vm.ExecRequest) vm.ExecResponse {
			t.Errorf("unexpected exec call: %s", req.Command)
			return vm.ExecResponse{}
		},
		func(path, content, format string) error {
			writtenPath = path
			if format != "base64" {
				return fmt.Errorf("expected base64 format, got %q", format)
			}
			raw, err := base64.StdEncoding.DecodeString(content)
			if err != nil {
				return err
			}
			writtenData = string(raw)
			return nil
		},
		nil)
	defer ts.Close()

	if err := runScp(context.Background(), NewDaemonClient(ts.URL), []string{localFile}, "trigger:/home/", false); err != nil {
		t.Fatalf("runScp failed: %v", err)
	}
	if writtenPath != "/home/hello.txt" {
		t.Errorf("written path = %q, want /home/hello.txt", writtenPath)
	}
	if writtenData != "hello scp" {
		t.Errorf("written data = %q, want %q", writtenData, "hello scp")
	}
}

func TestRunScpPushFileIntoExistingRemoteDir(t *testing.T) {
	localDir := t.TempDir()
	localFile := filepath.Join(localDir, "hello.txt")
	if err := os.WriteFile(localFile, []byte("hello scp"), 0o644); err != nil {
		t.Fatal(err)
	}

	var writtenPath string
	sb := scpTestVM("sb-1", "trigger")
	ts := scpMockDaemon(t,
		map[string]*db.VM{"trigger": sb, "sb-1": sb},
		func(vmID string, req vm.ExecRequest) vm.ExecResponse {
			// /home/data exists as a directory in the sandbox.
			return vm.ExecResponse{ExitCode: 0}
		},
		func(path, content, format string) error {
			writtenPath = path
			return nil
		},
		nil)
	defer ts.Close()

	if err := runScp(context.Background(), NewDaemonClient(ts.URL), []string{localFile}, "trigger:/home/data", false); err != nil {
		t.Fatalf("runScp failed: %v", err)
	}
	if writtenPath != "/home/data/hello.txt" {
		t.Errorf("written path = %q, want /home/data/hello.txt", writtenPath)
	}
}

func TestRunScpPushDirRecursive(t *testing.T) {
	localDir := t.TempDir()
	mustWriteLocalTree(t, localDir, map[string]string{
		"data/a.txt":       "alpha",
		"data/nested/b.md": "beta",
	})
	srcDir := filepath.Join(localDir, "data")

	var stagedTar string
	var execCommands []string
	sb := scpTestVM("sb-1", "trigger")
	ts := scpMockDaemon(t,
		map[string]*db.VM{"trigger": sb, "sb-1": sb},
		func(vmID string, req vm.ExecRequest) vm.ExecResponse {
			execCommands = append(execCommands, req.Command)
			// /dest does not exist remotely: test -d fails, extraction succeeds.
			if strings.Contains(req.Command, "test -d") {
				return vm.ExecResponse{ExitCode: 1}
			}
			return vm.ExecResponse{ExitCode: 0}
		},
		func(path, content, format string) error {
			if !strings.HasPrefix(path, "/tmp/.compute-scp-") {
				return fmt.Errorf("unexpected staging path %q", path)
			}
			raw, err := base64.StdEncoding.DecodeString(content)
			if err != nil {
				return err
			}
			stagedTar = string(raw)
			return nil
		},
		nil)
	defer ts.Close()

	// Destination does not exist remotely (test -d fails), so the destination
	// itself becomes the copy of the source directory.
	if err := runScp(context.Background(), NewDaemonClient(ts.URL), []string{srcDir}, "trigger:/dest", true); err != nil {
		t.Fatalf("runScp failed: %v", err)
	}

	if len(execCommands) < 2 {
		t.Fatalf("expected test -d and extract exec calls, got %v", execCommands)
	}
	if !strings.Contains(execCommands[0], `test -d "/dest"`) {
		t.Errorf("first exec = %q, want test -d check", execCommands[0])
	}
	extractCmd := execCommands[len(execCommands)-1]
	if !strings.Contains(extractCmd, `tar -xzf`) || !strings.Contains(extractCmd, `mkdir -p "/dest"`) {
		t.Errorf("extract exec = %q, want mkdir+tar extraction into /dest", extractCmd)
	}

	// Verify the staged tar.gz holds the source tree contents.
	gz, err := gzip.NewReader(strings.NewReader(stagedTar))
	if err != nil {
		t.Fatalf("staged archive unreadable: %v", err)
	}
	defer gz.Close()
	seen := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Typeflag == tar.TypeReg {
			var buf bytes.Buffer
			if _, err := io.Copy(&buf, tr); err != nil && err != io.EOF {
				t.Fatalf("reading tar member: %v", err)
			}
			seen[h.Name] = buf.String()
		}
	}
	if seen["a.txt"] != "alpha" || seen["nested/b.md"] != "beta" {
		t.Errorf("archive contents = %v, want a.txt/nested entries", seen)
	}
}

func TestRunScpPullFile(t *testing.T) {
	localDir := t.TempDir()

	b64 := base64.StdEncoding.EncodeToString([]byte("remote content"))
	sb := scpTestVM("sb-1", "trigger")
	ts := scpMockDaemon(t,
		map[string]*db.VM{"trigger": sb, "sb-1": sb},
		func(vmID string, req vm.ExecRequest) vm.ExecResponse {
			// Source path check: file is not a directory.
			if strings.Contains(req.Command, "test -d") {
				return vm.ExecResponse{ExitCode: 1}
			}
			t.Errorf("unexpected exec call: %s", req.Command)
			return vm.ExecResponse{}
		},
		nil,
		map[string]string{"sb-1:/home/hello.txt": b64})
	defer ts.Close()

	if err := runScp(context.Background(), NewDaemonClient(ts.URL), []string{"trigger:/home/hello.txt"}, localDir, false); err != nil {
		t.Fatalf("runScp failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(localDir, "hello.txt"))
	if err != nil {
		t.Fatalf("pulled file missing: %v", err)
	}
	if string(data) != "remote content" {
		t.Errorf("pulled data = %q, want %q", data, "remote content")
	}
}

func TestRunScpPullDirRecursive(t *testing.T) {
	localDst := t.TempDir()

	// Build the "remote" tree locally, then serve it as a base64 tar.gz the
	// same way the guest's tar+base64 command would.
	remoteTree := t.TempDir()
	mustWriteLocalTree(t, remoteTree, map[string]string{
		"a.txt":    "alpha",
		"nested/b": "beta",
	})

	var tarBuf bytes.Buffer
	gw := gzip.NewWriter(&tarBuf)
	tw := tar.NewWriter(gw)
	mustAddToTar := func(rel, content string) {
		if err := tw.WriteHeader(&tar.Header{Name: rel, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	mustAddToTar("a.txt", "alpha")
	mustAddToTar("nested/b", "beta")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	remoteB64 := base64.StdEncoding.EncodeToString(tarBuf.Bytes())

	ts := scpMockDaemon(t,
		map[string]*db.VM{"trigger": scpTestVM("sb-1", "trigger")},
		func(vmID string, req vm.ExecRequest) vm.ExecResponse {
			switch {
			case strings.Contains(req.Command, "test -d"):
				return vm.ExecResponse{ExitCode: 0} // source is a directory
			case strings.Contains(req.Command, "tar -czf") && strings.Contains(req.Command, "base64"):
				return vm.ExecResponse{ExitCode: 0, Stdout: remoteB64}
			default:
				t.Errorf("unexpected exec call: %s", req.Command)
				return vm.ExecResponse{}
			}
		},
		nil, nil)
	defer ts.Close()

	// Pulling a directory into an existing local dir places it under its own name.
	if err := runScp(context.Background(), NewDaemonClient(ts.URL), []string{"trigger:/home/data"}, localDst, true); err != nil {
		t.Fatalf("runScp failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(localDst, "data", "a.txt"))
	if err != nil {
		t.Fatalf("expected extracted file at %s: %v", filepath.Join(localDst, "data", "a.txt"), err)
	}
	if string(data) != "alpha" {
		t.Errorf("extracted content = %q, want %q", data, "alpha")
	}
	data, err = os.ReadFile(filepath.Join(localDst, "data", "nested", "b"))
	if err != nil {
		t.Fatalf("expected nested extracted file: %v", err)
	}
	if string(data) != "beta" {
		t.Errorf("extracted content = %q, want %q", data, "beta")
	}
}

func TestRunScpRemoteDirPullRequiresRecursive(t *testing.T) {
	ts := scpMockDaemon(t,
		map[string]*db.VM{"trigger": scpTestVM("sb-1", "trigger")},
		func(vmID string, req vm.ExecRequest) vm.ExecResponse {
			return vm.ExecResponse{ExitCode: 0} // test -d succeeds: it is a directory
		},
		nil, nil)
	defer ts.Close()

	err := runScp(context.Background(), NewDaemonClient(ts.URL), []string{"trigger:/home/logs"}, t.TempDir(), false)
	if err == nil || !strings.Contains(err.Error(), "use -r") {
		t.Fatalf("expected -r hint error, got: %v", err)
	}
}

func TestRunScpRemoteToRemoteFile(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("payload"))

	var writtenPath, writtenData string
	srcSandbox := scpTestVM("sb-1", "box-1")
	dstSandbox := scpTestVM("sb-2", "trigger")
	ts := scpMockDaemon(t,
		map[string]*db.VM{"box-1": srcSandbox, "sb-1": srcSandbox, "trigger": dstSandbox, "sb-2": dstSandbox},
		func(vmID string, req vm.ExecRequest) vm.ExecResponse {
			if strings.Contains(req.Command, "test -d") {
				return vm.ExecResponse{ExitCode: 1}
			}
			t.Errorf("unexpected exec call: %s", req.Command)
			return vm.ExecResponse{}
		},
		func(path, content, format string) error {
			writtenPath = path
			raw, err := base64.StdEncoding.DecodeString(content)
			if err != nil {
				return err
			}
			writtenData = string(raw)
			return nil
		},
		map[string]string{"sb-1:/workspace/data.bin": b64})
	defer ts.Close()

	if err := runScp(context.Background(), NewDaemonClient(ts.URL), []string{"box-1:/workspace/data.bin"}, "trigger:/home/data.bin", false); err != nil {
		t.Fatalf("runScp failed: %v", err)
	}
	if writtenPath != "/home/data.bin" {
		t.Errorf("written path = %q, want /home/data.bin", writtenPath)
	}
	if writtenData != "payload" {
		t.Errorf("written data = %q, want %q", writtenData, "payload")
	}
}

func TestRunScpLocalDirWithoutRecursiveFails(t *testing.T) {
	localDir := t.TempDir()
	ts := scpMockDaemon(t,
		map[string]*db.VM{"trigger": scpTestVM("sb-1", "trigger")},
		func(vmID string, req vm.ExecRequest) vm.ExecResponse { return vm.ExecResponse{} },
		nil, nil)
	defer ts.Close()

	err := runScp(context.Background(), NewDaemonClient(ts.URL), []string{localDir}, "trigger:/home/", false)
	if err == nil || !strings.Contains(err.Error(), "use -r") {
		t.Fatalf("expected -r hint error, got: %v", err)
	}
}

func TestRunScpMultipleSourcesRequireDirDst(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.txt")
	f2 := filepath.Join(dir, "b.txt")
	for _, f := range []string{f1, f2} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	notADir := filepath.Join(dir, "flat.txt")
	if err := os.WriteFile(notADir, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	ts := scpMockDaemon(t,
		map[string]*db.VM{"trigger": scpTestVM("sb-1", "trigger")},
		func(vmID string, req vm.ExecRequest) vm.ExecResponse { return vm.ExecResponse{ExitCode: 1} },
		nil, nil)
	defer ts.Close()

	err := runScp(context.Background(), NewDaemonClient(ts.URL), []string{f1, f2}, "trigger:"+notADir, false)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected not-a-directory error, got: %v", err)
	}
}

func TestExtractTarGzBase64RejectsUnsafePaths(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{Name: "../evil.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("no")); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gw.Close()

	dest := t.TempDir()
	err := extractTarGzBase64(base64.StdEncoding.EncodeToString(buf.Bytes()), dest)
	if err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("expected unsafe path error, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "..", "evil.txt")); err == nil {
		t.Fatalf("file escaped destination")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "evil.txt")); err == nil {
		t.Fatalf("file escaped destination")
	}
}

func TestScpTmpPathUnique(t *testing.T) {
	a, b := scpTmpPath("tgz"), scpTmpPath("tgz")
	if a == b {
		t.Fatalf("tmp paths not unique: %s", a)
	}
	if !strings.HasPrefix(a, "/tmp/.compute-scp-") || !strings.HasSuffix(a, ".tgz") {
		t.Errorf("unexpected tmp path format: %q", a)
	}
}
