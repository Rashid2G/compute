package agent

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupMockVsock launches a Unix domain socket server that simulates Firecracker's host vsock proxy.
func setupMockVsock(t *testing.T, srv *Server) (string, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "vsock-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	sockPath := filepath.Join(dir, "vsock.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("failed to listen on mock vsock socket: %v", err)
	}

	stopCh := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-stopCh:
					return
				default:
					return
				}
			}

			go func(c net.Conn) {
				// Firecracker handshake simulation:
				// 1. Read "CONNECT <port>\n"
				reader := bufio.NewReader(c)
				req, err := reader.ReadString('\n')
				if err != nil {
					c.Close()
					return
				}

				if !strings.HasPrefix(strings.TrimSpace(req), "CONNECT") {
					_, _ = c.Write([]byte("ERR invalid command\n"))
					c.Close()
					return
				}

				// 2. Respond "OK 10000\n"
				if _, err := c.Write([]byte("OK 10000\n")); err != nil {
					c.Close()
					return
				}

				// 3. Transparently pass connection to compute-agent server
				srv.HandleConn(c)
			}(conn)
		}
	}()

	cleanup := func() {
		close(stopCh)
		ln.Close()
		os.RemoveAll(dir)
	}

	return sockPath, cleanup
}

func TestAgentClientPingAndReady(t *testing.T) {
	srv := NewServer()
	sockPath, cleanup := setupMockVsock(t, srv)
	defer cleanup()

	client := NewClient(sockPath, DefaultVsockPort)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	if err := client.WaitReady(ctx, 10*time.Millisecond); err != nil {
		t.Fatalf("WaitReady failed: %v", err)
	}
}

func TestAgentClientExec(t *testing.T) {
	srv := NewServer()
	sockPath, cleanup := setupMockVsock(t, srv)
	defer cleanup()

	client := NewClient(sockPath, DefaultVsockPort)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Basic echo
	res, err := client.Exec(ctx, Request{
		Cmd: []string{"echo", "hello vsock agent"},
	})
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "hello vsock agent" {
		t.Errorf("unexpected stdout: %q", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Errorf("unexpected exit code: %d", res.ExitCode)
	}

	// 2. Exec with Env and Stdin
	res, err = client.Exec(ctx, Request{
		Cmd:   []string{"sh", "-c", "echo $TEST_VAR; cat"},
		Env:   map[string]string{"TEST_VAR": "compute-test-env"},
		Stdin: "piped-stdin-content",
	})
	if err != nil {
		t.Fatalf("Exec with env/stdin failed: %v", err)
	}
	if !strings.Contains(res.Stdout, "compute-test-env") {
		t.Errorf("missing env var in stdout: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "piped-stdin-content") {
		t.Errorf("missing stdin in stdout: %q", res.Stdout)
	}

	// 3. Exec with non-zero exit code
	res, err = client.Exec(ctx, Request{
		Cmd: []string{"sh", "-c", "exit 42"},
	})
	if err != nil {
		t.Fatalf("Exec exit code test error: %v", err)
	}
	if res.ExitCode != 42 {
		t.Errorf("expected exit code 42, got %d", res.ExitCode)
	}
}

func TestAgentClientExecStream(t *testing.T) {
	srv := NewServer()
	sockPath, cleanup := setupMockVsock(t, srv)
	defer cleanup()

	client := NewClient(sockPath, DefaultVsockPort)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var chunks []string
	finalRes, err := client.ExecStream(ctx, Request{
		Cmd: []string{"sh", "-c", "echo 'stream chunk 1'; echo 'stream chunk 2'"},
	}, func(f StreamFrame) error {
		if f.Type == FrameStdout {
			chunks = append(chunks, f.Data)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ExecStream failed: %v", err)
	}
	if finalRes.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", finalRes.ExitCode)
	}
	joined := strings.Join(chunks, "")
	if !strings.Contains(joined, "stream chunk 1") || !strings.Contains(joined, "stream chunk 2") {
		t.Errorf("streaming output missing chunks: %q", joined)
	}
}

func TestAgentClientFileOps(t *testing.T) {
	srv := NewServer()
	sockPath, cleanup := setupMockVsock(t, srv)
	defer cleanup()

	client := NewClient(sockPath, DefaultVsockPort)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "sub", "test.txt")

	// 1. Mkdir
	if err := client.Mkdir(ctx, filepath.Join(tempDir, "sub"), 0755); err != nil {
		t.Fatalf("Mkdir failed: %v", err)
	}

	// 2. WriteFile
	payload := []byte("hello from vsock file write!")
	if err := client.WriteFile(ctx, testFile, payload, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// 3. StatFile
	stat, err := client.StatFile(ctx, testFile)
	if err != nil {
		t.Fatalf("StatFile failed: %v", err)
	}
	if stat.Size != int64(len(payload)) {
		t.Errorf("expected size %d, got %d", len(payload), stat.Size)
	}
	if stat.IsDir {
		t.Errorf("expected file not dir")
	}

	// 4. ReadFile (text & base64)
	textRes, err := client.ReadFile(ctx, testFile, "text")
	if err != nil {
		t.Fatalf("ReadFile text failed: %v", err)
	}
	if textRes.Content != string(payload) {
		t.Errorf("ReadFile content mismatch: got %q, want %q", textRes.Content, string(payload))
	}

	b64Res, err := client.ReadFile(ctx, testFile, "base64")
	if err != nil {
		t.Fatalf("ReadFile base64 failed: %v", err)
	}
	if b64Res.Format != "base64" {
		t.Errorf("expected format base64, got %s", b64Res.Format)
	}

	// 5. ListFiles
	entries, err := client.ListFiles(ctx, filepath.Join(tempDir, "sub"))
	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "test.txt" {
		t.Errorf("unexpected list entries: %+v", entries)
	}

	// 6. RemoveFile
	if err := client.RemoveFile(ctx, testFile, false); err != nil {
		t.Fatalf("RemoveFile failed: %v", err)
	}
	_, err = client.StatFile(ctx, testFile)
	if err == nil {
		t.Errorf("expected error after remove, got nil")
	}
}

func TestAgentClientProcOps(t *testing.T) {
	srv := NewServer()
	sockPath, cleanup := setupMockVsock(t, srv)
	defer cleanup()

	client := NewClient(sockPath, DefaultVsockPort)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. List processes
	procs, err := client.ProcList(ctx)
	if err != nil {
		t.Fatalf("ProcList failed: %v", err)
	}
	if len(procs) == 0 {
		t.Errorf("expected non-empty process list")
	}

	// 2. Start process
	pid, err := client.ProcStart(ctx, []string{"sleep", "10"}, nil, "")
	if err != nil {
		t.Fatalf("ProcStart failed: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("invalid started pid: %d", pid)
	}

	// 3. Kill process
	if err := client.ProcKill(ctx, pid, "SIGKILL"); err != nil {
		t.Fatalf("ProcKill failed: %v", err)
	}
}

func TestAgentClientStatsClockShutdown(t *testing.T) {
	shutdownCh := make(chan struct{}, 1)
	srv := NewServer()
	srv.OnShutdown = func() {
		select {
		case shutdownCh <- struct{}{}:
		default:
		}
	}
	sockPath, cleanup := setupMockVsock(t, srv)
	defer cleanup()

	client := NewClient(sockPath, DefaultVsockPort)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Stats
	stats, err := client.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if stats == nil {
		t.Fatalf("stats returned nil")
	}

	// 2. ClockSync
	// Setting clock may fail if unprivileged in tests; verify roundtrip doesn't crash
	_ = client.ClockSync(ctx, time.Now().UnixNano())

	// 3. Shutdown
	if err := client.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
	select {
	case <-shutdownCh:
	case <-time.After(2 * time.Second):
		t.Errorf("expected shutdown hook to be invoked")
	}
}

func TestAllocateGuestCID(t *testing.T) {
	tests := []struct {
		vmID string
	}{
		{"vm-1"},
		{"sandbox-prod-001"},
		{"sb-6bcdff8"},
		{"very-long-sandbox-name-uuid-12345678-abcdef"},
	}

	for _, tc := range tests {
		cid := AllocateGuestCID(tc.vmID)
		if cid < 3 {
			t.Errorf("AllocateGuestCID(%s) = %d, must be >= 3", tc.vmID, cid)
		}
		// Deterministic
		cid2 := AllocateGuestCID(tc.vmID)
		if cid != cid2 {
			t.Errorf("AllocateGuestCID not deterministic for %s: %d != %d", tc.vmID, cid, cid2)
		}
	}

	// Verify two different VMs get different CIDs (collision resistance in high range)
	cidA := AllocateGuestCID("vm-alpha")
	cidB := AllocateGuestCID("vm-beta")
	if cidA == cidB {
		t.Errorf("unexpected collision between vm-alpha (%d) and vm-beta (%d)", cidA, cidB)
	}
}
