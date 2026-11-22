package vm

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/agent"
	"github.com/Rashid2G/compute/internal/db"
)

func TestDeterministicCIDAllocation(t *testing.T) {
	cid1 := agent.AllocateGuestCID("vm-abc-123")
	cid2 := agent.AllocateGuestCID("vm-abc-123")
	if cid1 != cid2 {
		t.Fatalf("CID allocation is not deterministic: %d vs %d", cid1, cid2)
	}

	if cid1 < 3 {
		t.Fatalf("CID allocation produced reserved CID: %d (< 3)", cid1)
	}

	cidOther := agent.AllocateGuestCID("vm-def-456")
	if cidOther < 3 {
		t.Fatalf("CID allocation produced reserved CID: %d (< 3)", cidOther)
	}
	if cidOther == cid1 {
		t.Logf("Warning: hash collision for different VM IDs (%d)", cid1)
	}
}

func TestGetVMVsockPath(t *testing.T) {
	m, _, _, _ := testManager(t)

	// Nil VM
	if got := m.GetVMVsockPath(nil); got != "" {
		t.Fatalf("expected empty vsock path for nil VM, got %s", got)
	}

	vm := &db.VM{
		ID:         "test-vm",
		InternalID: "ins_test_123",
	}

	// Default non-jailed path
	expectedNonJailed := filepath.Join(m.getVMStorageDir(vm), "vsock.sock")
	got := m.GetVMVsockPath(vm)
	if got != expectedNonJailed {
		t.Fatalf("expected vsock path %s, got %s", expectedNonJailed, got)
	}

	// Jailed path simulation: if JailerEnabled and socket exists on host
	m.cfg.JailerEnabled = true
	m.cfg.JailerBaseDir = t.TempDir()
	m.cfg.FirecrackerBinary = "/usr/bin/firecracker"
	jailerSock := filepath.Join(m.cfg.JailerBaseDir, "firecracker", sanitizeFirecrackerID(vm.InternalID), "root", "vsock.sock")
	if err := os.MkdirAll(filepath.Dir(jailerSock), 0755); err == nil {
		if f, err := os.Create(jailerSock); err == nil {
			f.Close()
			defer os.RemoveAll(filepath.Dir(jailerSock))

			gotJailed := m.GetVMVsockPath(vm)
			if gotJailed != jailerSock {
				t.Fatalf("expected jailed vsock path %s, got %s", jailerSock, gotJailed)
			}
		}
	}
}

func setupMockVsockServer(t *testing.T) (string, *agent.Server, func()) {
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

	srv := agent.NewServer()
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
				if _, err := c.Write([]byte("OK 10000\n")); err != nil {
					c.Close()
					return
				}
				srv.HandleConn(c)
			}(conn)
		}
	}()

	cleanup := func() {
		close(stopCh)
		ln.Close()
		os.RemoveAll(dir)
	}

	return sockPath, srv, cleanup
}

func TestManagerVsockAgentOperations(t *testing.T) {
	m, store, _, _ := testManager(t)
	ctx := context.Background()

	sockPath, _, cleanup := setupMockVsockServer(t)
	defer cleanup()

	client := agent.NewClient(sockPath, agent.DefaultVsockPort)
	if err := client.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	vm := &db.VM{
		ID:         "vsock-test-vm",
		InternalID: "ins_vsock_test",
		OwnerID:    "default",
		Status:     db.StatusRunning,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatalf("SaveVM: %v", err)
	}

	// Register the agent client directly
	m.SetAgentClient(vm.ID, client)

	// 1. ExecVM via vsock agent
	execRes, err := m.ExecVM(ctx, vm.ID, ExecRequest{
		Command: "echo 'hello from agent'",
	})
	if err != nil {
		t.Fatalf("ExecVM failed: %v", err)
	}
	if !strings.Contains(execRes.Stdout, "hello from agent") {
		t.Fatalf("ExecVM unexpected stdout: %q", execRes.Stdout)
	}
	if execRes.ExitCode != 0 {
		t.Fatalf("ExecVM exit code = %d, want 0", execRes.ExitCode)
	}

	// 2. ExecVMStream via vsock agent
	var streamOut bytes.Buffer
	err = m.ExecVMStream(ctx, vm.ID, ExecRequest{
		Command: "echo 'stream hello'",
	}, func(event ExecStreamEvent) error {
		if event.Stream == "stdout" {
			streamOut.WriteString(event.Data)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ExecVMStream failed: %v", err)
	}
	if !strings.Contains(streamOut.String(), "stream hello") {
		t.Fatalf("ExecVMStream unexpected stdout: %q", streamOut.String())
	}

	// 3. File Operations: Write, Read, Stat, List, Mkdir, Remove
	testDir := filepath.Join(t.TempDir(), "agent-files")
	testFile := filepath.Join(testDir, "test.txt")

	if err := m.Mkdir(ctx, vm.ID, testDir, 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	content := []byte("vsock agent file content")
	if err := m.WriteFile(ctx, vm.ID, testFile, content, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	readBack, err := m.ReadFile(ctx, vm.ID, testFile, "utf-8")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if readBack.Content != string(content) {
		t.Fatalf("ReadFile got %q, want %q", readBack.Content, string(content))
	}

	stat, err := m.StatFile(ctx, vm.ID, testFile)
	if err != nil {
		t.Fatalf("StatFile: %v", err)
	}
	if stat.Size != int64(len(content)) {
		t.Fatalf("StatFile size = %d, want %d", stat.Size, len(content))
	}

	files, err := m.ListFiles(ctx, vm.ID, testDir)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 || files[0].Name != "test.txt" {
		t.Fatalf("ListFiles unexpected results: %+v", files)
	}

	if err := m.RemoveFile(ctx, vm.ID, testFile, false); err != nil {
		t.Fatalf("RemoveFile: %v", err)
	}

	// 4. Process Operations
	pid, err := m.StartProcess(ctx, vm.ID, []string{"sleep", "10"}, nil, "/tmp")
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("StartProcess returned invalid PID: %d", pid)
	}

	procs, err := m.ListProcesses(ctx, vm.ID)
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	if len(procs) == 0 {
		t.Fatalf("ListProcesses returned empty list")
	}

	if err := m.KillProcess(ctx, vm.ID, pid, "SIGKILL"); err != nil {
		t.Fatalf("KillProcess: %v", err)
	}

	// 5. Guest stats & Clock sync
	stats, err := m.GetGuestStats(ctx, vm.ID)
	if err != nil {
		t.Fatalf("GetGuestStats: %v", err)
	}
	if stats == nil {
		t.Fatalf("GetGuestStats returned nil stats")
	}

	// Clock sync
	if err := m.SyncClock(ctx, vm.ID, time.Now().UnixNano()); err != nil {
		t.Logf("SyncClock completed (err: %v)", err)
	}
}

func TestManagerFallbackToSSHWhenVsockAbsent(t *testing.T) {
	m, store, _, _ := testManager(t)
	ctx := context.Background()

	vm := &db.VM{
		ID:         "no-vsock-vm",
		InternalID: "ins_no_vsock",
		OwnerID:    "default",
		Status:     db.StatusRunning,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatalf("SaveVM: %v", err)
	}

	// With no vsock socket and no registered agent client, ExecVM falls back to SSH.
	// Since no SSH daemon is running for this VM, it returns an error rather than panicking.
	_, err := m.ExecVM(ctx, vm.ID, ExecRequest{Command: "whoami"})
	if err == nil {
		t.Fatal("expected error from fallback SSH when VM has no SSH daemon, got nil")
	}
}
