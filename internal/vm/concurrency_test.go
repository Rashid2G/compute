package vm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/storage"
)

type safeRunner struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *safeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{name}, args...))
	cmdStr := strings.Join(append([]string{name}, args...), " ")
	if strings.Contains(cmdStr, "ip -o link show") {
		return []byte("1: lo: <LOOPBACK>\n"), nil
	}
	return []byte("ok"), nil
}

func testConcurrentManager(t *testing.T) (*Manager, *db.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := db.NewStore(filepath.Join(dir, "db", "vms.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	base, vms := filepath.Join(dir, "images"), filepath.Join(dir, "vms")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "ubuntu-24.04.ext4"), []byte("concurrent-rootfs-content"), 0600); err != nil {
		t.Fatal(err)
	}

	stor, err := storage.NewManager(storage.Config{
		BaseImagesDir: base,
		VMsDir:        vms,
		DefaultRootfs: "ubuntu-24.04.ext4",
		DefaultKernel: "vmlinux",
		SSHKeyPath:    filepath.Join(base, "key"),
	})
	if err != nil {
		t.Fatal(err)
	}

	runner := &safeRunner{}
	net, err := network.NewManager(network.Config{SubnetCIDR: "10.50.0.0/16"}, runner)
	if err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		CgroupParent:  filepath.Join(dir, "cgroup"),
		JailerBaseDir: filepath.Join(dir, "srv-jailer"),
	}

	m := NewManager(cfg, store, net, stor)
	return m, store
}

// TestConcurrentHighDensityOperations_50VMs tests that 50+ concurrent VM operations
// (creation, registry lookup, inspect, update, deletion) run race-free without deadlocks
// or lock contention bottlenecks.
func TestConcurrentHighDensityOperations_50VMs(t *testing.T) {
	const numConcurrentVMs = 55
	m, _ := testConcurrentManager(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	errCh := make(chan error, numConcurrentVMs*3)
	createdVMs := make([]*db.VM, numConcurrentVMs)
	var createdLock sync.Mutex

	startBarrier := make(chan struct{})

	// 1. Concurrently create 55 VMs across different tenants
	for i := 0; i < numConcurrentVMs; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-startBarrier

			ownerID := fmt.Sprintf("tenant-%d", idx)
			vmName := fmt.Sprintf("bench-vm-%d", idx)

			vm, err := m.CreateVM(ctx, CreateRequest{
				Name:     vmName,
				OwnerID:  ownerID,
				Tier:     "1vcpu",
				VCPUs:    1,
				MemoryMB: 256,
				DiskMB:   51200,
			})
			if err != nil {
				errCh <- fmt.Errorf("worker %d: CreateVM failed: %w", idx, err)
				return
			}

			createdLock.Lock()
			createdVMs[idx] = vm
			createdLock.Unlock()

			// Concurrently inspect network
			_, err = m.InspectNetworkForOwner(ctx, vm.ID, ownerID)
			if err != nil {
				errCh <- fmt.Errorf("worker %d: InspectNetwork failed: %w", idx, err)
				return
			}

			// Concurrently get VM
			got, err := m.GetVM(ctx, vm.ID)
			if err != nil || got == nil {
				errCh <- fmt.Errorf("worker %d: GetVM returned nil or err: %v", idx, err)
				return
			}

			// Concurrently update config
			pubEnabled := true
			_, err = m.UpdateVMConfig(ctx, vm.ID, UpdateVMConfigRequest{
				PublicIPEnabled: &pubEnabled,
			})
			if err != nil {
				errCh <- fmt.Errorf("worker %d: UpdateVMConfig failed: %w", idx, err)
				return
			}
		}(i)
	}

	close(startBarrier)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}

	// Verify all 55 VMs exist
	allVMs, err := m.ListVMs(ctx)
	if err != nil {
		t.Fatalf("ListVMs failed: %v", err)
	}
	if len(allVMs) != numConcurrentVMs {
		t.Fatalf("expected %d VMs, got %d", numConcurrentVMs, len(allVMs))
	}

	// 2. Concurrently delete all 55 VMs
	var delWg sync.WaitGroup
	delErrCh := make(chan error, numConcurrentVMs)

	for i := 0; i < numConcurrentVMs; i++ {
		vm := createdVMs[i]
		if vm == nil {
			continue
		}
		delWg.Add(1)
		go func(v *db.VM) {
			defer delWg.Done()
			if err := m.DeleteVM(ctx, v.ID); err != nil {
				delErrCh <- fmt.Errorf("DeleteVM %s failed: %w", v.ID, err)
			}
		}(vm)
	}

	delWg.Wait()
	close(delErrCh)

	for err := range delErrCh {
		t.Fatal(err)
	}

	// Verify all VMs deleted
	remainingVMs, err := m.ListVMs(ctx)
	if err != nil {
		t.Fatalf("ListVMs failed after delete: %v", err)
	}
	if len(remainingVMs) != 0 {
		t.Fatalf("expected 0 VMs after deletion, got %d", len(remainingVMs))
	}
}

// TestConcurrentReadWriteIsolation verifies that high read frequency across
// registry queries (ListVMs, GetVM) does not block or deadlock with concurrent writes.
func TestConcurrentReadWriteIsolation(t *testing.T) {
	m, _ := testConcurrentManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var stopReaders int32
	var readCount int64
	var readWg sync.WaitGroup

	// Start 10 background readers reading as fast as possible
	for r := 0; r < 10; r++ {
		readWg.Add(1)
		go func() {
			defer readWg.Done()
			for atomic.LoadInt32(&stopReaders) == 0 {
				_, _ = m.ListVMs(ctx)
				_, _ = m.GetVM(ctx, "nonexistent")
				atomic.AddInt64(&readCount, 2)
				time.Sleep(1 * time.Millisecond)
			}
		}()
	}

	// Concurrently create and delete 20 VMs while readers run
	var writerWg sync.WaitGroup
	writerErrCh := make(chan error, 20)

	for w := 0; w < 20; w++ {
		writerWg.Add(1)
		go func(idx int) {
			defer writerWg.Done()
			name := fmt.Sprintf("rw-vm-%d", idx)
			vm, err := m.CreateVM(ctx, CreateRequest{
				Name:     name,
				OwnerID:  "tenant-rw",
				VCPUs:    1,
				MemoryMB: 256,
			})
			if err != nil {
				writerErrCh <- fmt.Errorf("writer %d CreateVM failed: %w", idx, err)
				return
			}

			// Small sleep to ensure overlap
			time.Sleep(5 * time.Millisecond)

			if err := m.DeleteVM(ctx, vm.ID); err != nil {
				writerErrCh <- fmt.Errorf("writer %d DeleteVM failed: %w", idx, err)
			}
		}(w)
	}

	writerWg.Wait()
	close(writerErrCh)
	atomic.StoreInt32(&stopReaders, 1)
	readWg.Wait()

	for err := range writerErrCh {
		t.Fatal(err)
	}

	if atomic.LoadInt64(&readCount) == 0 {
		t.Fatalf("expected non-zero read count during concurrent test")
	}
}

func TestProcessQueue_LocksVMProperly(t *testing.T) {
	m, store := testConcurrentManager(t)
	ctx := context.Background()

	vmID := "queued-vm-lock-test"
	vm := &db.VM{
		ID:       vmID,
		Name:     "queued-lock-test",
		OwnerID:  "tenant-1",
		Status:   db.StatusQueued,
		VCPUs:    1,
		MemoryMB: 256,
	}
	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatal(err)
	}

	m.enqueue(vmID, "waiting for resources")

	var lockHeldDuringLaunch bool
	var launcherCalled bool
	m.SetLauncher(func(ctx context.Context, v *db.VM) error {
		launcherCalled = true
		vmLock := m.getVMLock(v.ID)
		canAcquire := vmLock.TryLock()
		if canAcquire {
			vmLock.Unlock()
			lockHeldDuringLaunch = false
		} else {
			lockHeldDuringLaunch = true
		}
		return nil
	})

	m.processQueue(ctx)

	if !lockHeldDuringLaunch {
		t.Fatalf("expected processQueue to hold vmLock (launcherCalled=%v), but lock was not held!", launcherCalled)
	}

	// Verify that after processQueue finishes, lock was released
	vmLock := m.getVMLock(vmID)
	if !vmLock.TryLock() {
		t.Fatal("expected vmLock to be unlocked after processQueue finished")
	}
	vmLock.Unlock()

	// Verify VM status transitioned to running
	loaded, err := m.GetVM(ctx, vmID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != db.StatusRunning {
		t.Fatalf("expected VM status to be running, got %s", loaded.Status)
	}
}

func TestResumeVM_AssignsCgroups(t *testing.T) {
	shortTmpDir, err := os.MkdirTemp("/tmp", "fc-res-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(shortTmpDir)

	dbPath := filepath.Join(shortTmpDir, "vms.sqlite")
	store, err := db.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	base, vms := filepath.Join(shortTmpDir, "img"), filepath.Join(shortTmpDir, "v")
	_ = os.MkdirAll(base, 0755)
	_ = os.MkdirAll(vms, 0755)
	_ = os.WriteFile(filepath.Join(base, "ubuntu-24.04.ext4"), []byte("data"), 0600)

	stor, err := storage.NewManager(storage.Config{
		BaseImagesDir: base,
		VMsDir:        vms,
		DefaultRootfs: "ubuntu-24.04.ext4",
		DefaultKernel: "vmlinux",
		SSHKeyPath:    filepath.Join(base, "key"),
	})
	if err != nil {
		t.Fatal(err)
	}

	runner := &safeRunner{}
	net, err := network.NewManager(network.Config{SubnetCIDR: "10.50.0.0/16"}, runner)
	if err != nil {
		t.Fatal(err)
	}

	mockFCPath := filepath.Join(shortTmpDir, "mock_fc.py")
	script := `#!/usr/bin/env python3
import sys, os, socket

sock_path = None
for i, arg in enumerate(sys.argv):
    if arg == "--api-sock" and i + 1 < len(sys.argv):
        sock_path = sys.argv[i + 1]

if not sock_path:
    sys.exit(1)

os.makedirs(os.path.dirname(sock_path), exist_ok=True)
try:
    os.remove(sock_path)
except OSError:
    pass

s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.bind(sock_path)
s.listen(10)

while True:
    try:
        conn, _ = s.accept()
        data = conn.recv(2048)
        if data:
            conn.sendall(b"HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n")
        conn.close()
    except Exception:
        break
`
	if err := os.WriteFile(mockFCPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		FirecrackerBinary: mockFCPath,
		CgroupParent:      filepath.Join(shortTmpDir, "cg"),
		JailerBaseDir:     filepath.Join(shortTmpDir, "srv"),
	}

	m := NewManager(cfg, store, net, stor)
	ctx := context.Background()

	// Create snapshot state files
	snapFile := filepath.Join(shortTmpDir, "vm.snap")
	memFile := filepath.Join(shortTmpDir, "vm.mem")
	_ = os.WriteFile(snapFile, []byte("mock-snap"), 0644)
	_ = os.WriteFile(memFile, []byte("mock-mem"), 0644)

	vmID := "vm-res-cg"
	vm := &db.VM{
		ID:           vmID,
		Name:         "res-cg-vm",
		OwnerID:      "tenant-cg",
		Status:       db.StatusPaused,
		VCPUs:        2,
		MemoryMB:     512,
		SnapshotPath: snapFile,
		MemFilePath:  memFile,
		TAPDevice:    "tap-res-cg",
	}
	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.vms[vmID] = vm
	m.mu.Unlock()

	// Resume VM
	if err := m.ResumeVM(ctx, vmID); err != nil {
		t.Fatalf("ResumeVM failed: %v", err)
	}

	defer func() {
		_ = m.StopVM(ctx, vmID)
	}()

	// Verify VM is running with non-zero PID
	loaded, err := m.GetVM(ctx, vmID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != db.StatusRunning || loaded.PID <= 0 {
		t.Fatalf("expected running VM with valid PID, got status=%s pid=%d", loaded.Status, loaded.PID)
	}

	// Verify PID was assigned to cgroup.procs
	cgProcsPath := filepath.Join(m.cgroupMgr.BasePath(), vmID, "cgroup.procs")
	procsData, err := os.ReadFile(cgProcsPath)
	if err != nil {
		t.Fatalf("failed to read cgroup.procs for resumed vm: %v", err)
	}

	expectedPIDStr := fmt.Sprintf("%d", loaded.PID)
	if !strings.Contains(string(procsData), expectedPIDStr) {
		t.Fatalf("cgroup.procs does not contain resumed PID %s; got %q", expectedPIDStr, string(procsData))
	}
}

// TestNoDeadlock_SetResourceLimits_ReconcileAndPrune_WithRunQueue verifies that
// SetResourceLimits and ReconcileAndPrune do not enter self-deadlock when items
// reside in m.runQueue.
func TestNoDeadlock_SetResourceLimits_ReconcileAndPrune_WithRunQueue(t *testing.T) {
	m, store := testConcurrentManager(t)
	ctx := context.Background()

	// Intercept startVMProcess via custom launcher
	m.SetLauncher(func(ctx context.Context, vm *db.VM) error {
		return nil
	})

	vm1 := &db.VM{
		ID:       "vm-deadlock-1",
		Name:     "queued-1",
		OwnerID:  "tenant-1",
		Status:   db.StatusQueued,
		VCPUs:    1,
		MemoryMB: 512,
	}
	if err := store.SaveVM(ctx, vm1); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	m.vms[vm1.ID] = vm1
	m.mu.Unlock()

	m.queueMu.Lock()
	m.runQueue = append(m.runQueue, vm1.ID)
	m.queueReasons[vm1.ID] = "capacity"
	m.queueMu.Unlock()

	// 1. Verify SetResourceLimits completes without deadlocking
	doneCh := make(chan struct{})
	go func() {
		m.SetResourceLimits(m.resourceLimits)
		close(doneCh)
	}()

	select {
	case <-doneCh:
		// Success without deadlock
	case <-time.After(3 * time.Second):
		t.Fatal("FATAL: SetResourceLimits entered self-deadlock while processing runQueue!")
	}

	// 2. Re-queue and verify ReconcileAndPrune completes without deadlocking
	m.queueMu.Lock()
	m.runQueue = append(m.runQueue, vm1.ID)
	m.queueReasons[vm1.ID] = "capacity"
	m.queueMu.Unlock()

	doneCh2 := make(chan struct{})
	go func() {
		_, _ = m.ReconcileAndPrune(ctx)
		close(doneCh2)
	}()

	select {
	case <-doneCh2:
		// Success without deadlock
	case <-time.After(3 * time.Second):
		t.Fatal("FATAL: ReconcileAndPrune entered self-deadlock while processing runQueue!")
	}
}
