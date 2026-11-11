package vm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rashid2G/on-demand-vms/internal/db"
)

func TestFormatCPUMax(t *testing.T) {
	tests := []struct {
		vcpus    int
		expected string
	}{
		{vcpus: 1, expected: "100000 100000"},
		{vcpus: 2, expected: "200000 100000"},
		{vcpus: 4, expected: "400000 100000"},
		{vcpus: 0, expected: "max 100000"},
		{vcpus: -1, expected: "max 100000"},
	}

	for _, tt := range tests {
		got := FormatCPUMax(tt.vcpus)
		if got != tt.expected {
			t.Errorf("FormatCPUMax(%d) = %q; want %q", tt.vcpus, got, tt.expected)
		}
	}
}

func TestFormatMemoryMax(t *testing.T) {
	// 2048 MB + 64 MB overhead = 2112 MB = 2214592512 bytes
	expected2GB := fmt.Sprintf("%d", int64(2048+64)*1024*1024)
	got2GB := FormatMemoryMax(2048, 64)
	if got2GB != expected2GB {
		t.Errorf("FormatMemoryMax(2048, 64) = %q; want %q", got2GB, expected2GB)
	}

	// 4096 MB + 64 MB overhead = 4160 MB = 4362076160 bytes
	expected4GB := fmt.Sprintf("%d", int64(4096+64)*1024*1024)
	got4GB := FormatMemoryMax(4096, 64)
	if got4GB != expected4GB {
		t.Errorf("FormatMemoryMax(4096, 64) = %q; want %q", got4GB, expected4GB)
	}

	if gotZero := FormatMemoryMax(0, 64); gotZero != "max" {
		t.Errorf("FormatMemoryMax(0, 64) = %q; want 'max'", gotZero)
	}
}

func TestFormatPidsMax(t *testing.T) {
	if got := FormatPidsMax(256); got != "256" {
		t.Errorf("FormatPidsMax(256) = %q; want '256'", got)
	}
	if got := FormatPidsMax(0); got != "512" {
		t.Errorf("FormatPidsMax(0) = %q; want '512'", got)
	}
}

func TestCgroupManager_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	cgroupRoot := filepath.Join(tmpDir, "cgroup", "compute")
	cm := NewCgroupManager(cgroupRoot)

	vmID := "vm-test-123"
	limits := CgroupLimits{
		VCPUs:                2,
		MemoryMB:             2048,
		MemorySafetyBufferMB: 64,
		MemoryHighBufferMB:   32,
		PidsMax:              512,
		IOMax:                "8:0 rbps=10485760",
	}

	cgPath, err := cm.Setup(vmID, limits)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Verify directory exists
	if _, err := os.Stat(cgPath); err != nil {
		t.Fatalf("cgroup dir does not exist: %v", err)
	}

	// Verify cpu.max content
	cpuData, err := os.ReadFile(filepath.Join(cgPath, "cpu.max"))
	if err != nil {
		t.Fatalf("failed to read cpu.max: %v", err)
	}
	if strings.TrimSpace(string(cpuData)) != "200000 100000" {
		t.Errorf("unexpected cpu.max content: %s", string(cpuData))
	}

	// Verify memory.max content
	memData, err := os.ReadFile(filepath.Join(cgPath, "memory.max"))
	if err != nil {
		t.Fatalf("failed to read memory.max: %v", err)
	}
	expectedMem := fmt.Sprintf("%d", int64(2048+64)*1024*1024)
	if strings.TrimSpace(string(memData)) != expectedMem {
		t.Errorf("unexpected memory.max: %s; want %s", string(memData), expectedMem)
	}

	// Verify pids.max content
	pidsData, err := os.ReadFile(filepath.Join(cgPath, "pids.max"))
	if err != nil {
		t.Fatalf("failed to read pids.max: %v", err)
	}
	if strings.TrimSpace(string(pidsData)) != "512" {
		t.Errorf("unexpected pids.max: %s", string(pidsData))
	}

	// Verify io.max content
	ioData, err := os.ReadFile(filepath.Join(cgPath, "io.max"))
	if err != nil {
		t.Fatalf("failed to read io.max: %v", err)
	}
	if strings.TrimSpace(string(ioData)) != "8:0 rbps=10485760" {
		t.Errorf("unexpected io.max: %s", string(ioData))
	}

	// Assign PID
	testPID := 12345
	if err := cm.Assign(vmID, testPID); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	procsData, err := os.ReadFile(filepath.Join(cgPath, "cgroup.procs"))
	if err != nil {
		t.Fatalf("failed to read cgroup.procs: %v", err)
	}
	if strings.TrimSpace(string(procsData)) != "12345" {
		t.Errorf("unexpected cgroup.procs: %s", string(procsData))
	}

	// Write mock stats and verify GetStats
	_ = os.WriteFile(filepath.Join(cgPath, "memory.current"), []byte("104857600\n"), 0644)
	_ = os.WriteFile(filepath.Join(cgPath, "pids.current"), []byte("4\n"), 0644)
	_ = os.WriteFile(filepath.Join(cgPath, "cpu.stat"), []byte("usage_usec 5000000\nuser_usec 4000000\n"), 0644)

	stats, err := cm.GetStats(vmID)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if stats.MemoryCurrent != 104857600 {
		t.Errorf("expected MemoryCurrent 104857600, got %d", stats.MemoryCurrent)
	}
	if stats.PidsCurrent != 4 {
		t.Errorf("expected PidsCurrent 4, got %d", stats.PidsCurrent)
	}
	if stats.CPUTimeUsec != 5000000 {
		t.Errorf("expected CPUTimeUsec 5000000, got %d", stats.CPUTimeUsec)
	}

	// Teardown
	if err := cm.Teardown(vmID); err != nil {
		t.Fatalf("Teardown failed: %v", err)
	}
	if _, err := os.Stat(cgPath); !os.IsNotExist(err) {
		t.Errorf("expected cgroup dir %s to be removed", cgPath)
	}
}

func TestCgroupManager_EnablesRequiredControllers(t *testing.T) {
	tmpDir := t.TempDir()
	cgroupRoot := filepath.Join(tmpDir, "cgroup", "compute")
	if err := os.MkdirAll(cgroupRoot, 0755); err != nil {
		t.Fatalf("failed to create cgroup root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cgroupRoot, "cgroup.controllers"), []byte("cpuset cpu io memory pids\n"), 0644); err != nil {
		t.Fatalf("failed to write cgroup.controllers: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cgroupRoot, "cgroup.subtree_control"), []byte("\n"), 0644); err != nil {
		t.Fatalf("failed to write cgroup.subtree_control: %v", err)
	}

	cm := NewCgroupManager(cgroupRoot)
	_, err := cm.Setup("vm-controllers", CgroupLimits{
		VCPUs:    2,
		MemoryMB: 512,
		IOMax:    "8:0 rbps=1048576",
	})
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(cgroupRoot, "cgroup.subtree_control"))
	if err != nil {
		t.Fatalf("failed to read cgroup.subtree_control: %v", err)
	}
	got := strings.Fields(string(data))
	want := map[string]bool{"+cpu": true, "+memory": true, "+pids": true, "+io": true}
	if len(got) != len(want) {
		t.Fatalf("unexpected subtree_control %q; want %v", string(data), want)
	}
	for _, controller := range got {
		if !want[controller] {
			t.Errorf("unexpected controller %q in subtree_control %q", controller, string(data))
		}
	}
}

func TestCgroupManager_SkipsUnavailableControllers(t *testing.T) {
	tmpDir := t.TempDir()
	cgroupRoot := filepath.Join(tmpDir, "cgroup", "compute")
	if err := os.MkdirAll(cgroupRoot, 0755); err != nil {
		t.Fatalf("failed to create cgroup root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cgroupRoot, "cgroup.controllers"), []byte("cpu memory pids\n"), 0644); err != nil {
		t.Fatalf("failed to write cgroup.controllers: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cgroupRoot, "cgroup.subtree_control"), []byte("\n"), 0644); err != nil {
		t.Fatalf("failed to write cgroup.subtree_control: %v", err)
	}

	cm := NewCgroupManager(cgroupRoot)
	if _, err := cm.Setup("vm-no-io", CgroupLimits{
		VCPUs:    1,
		MemoryMB: 256,
		IOMax:    "8:0 rbps=1048576",
	}); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(cgroupRoot, "cgroup.subtree_control"))
	if err != nil {
		t.Fatalf("failed to read cgroup.subtree_control: %v", err)
	}
	if strings.Contains(string(data), "+io") {
		t.Errorf("subtree_control %q must not enable an unavailable controller", string(data))
	}
}

func TestIsCgroupV2Supported(t *testing.T) {
	// Function must execute safely without panicking
	_ = IsCgroupV2Supported()
}

func TestCgroupManager_PathTraversalRejected(t *testing.T) {
	tmpDir := t.TempDir()
	cgroupRoot := filepath.Join(tmpDir, "cgroup", "compute")
	cm := NewCgroupManager(cgroupRoot)

	traversalIDs := []string{
		"../escape",
		"../../etc/passwd",
		"../../../root",
		"/escape",
		"sub/dir",
		"a/../b",
		"foo/../bar",
		"..",
		".",
		"",
	}

	limits := CgroupLimits{
		VCPUs:    1,
		MemoryMB: 512,
	}

	for _, badID := range traversalIDs {
		// 1. VMPath must error
		if _, err := cm.VMPath(badID); err == nil {
			t.Errorf("VMPath(%q) should fail with path traversal error, got nil", badID)
		}

		// 2. Setup must error
		if _, err := cm.Setup(badID, limits); err == nil {
			t.Errorf("Setup(%q) should fail with path traversal error, got nil", badID)
		}

		// 3. Assign must error
		if err := cm.Assign(badID, 1234); err == nil {
			t.Errorf("Assign(%q) should fail with path traversal error, got nil", badID)
		}

		// 4. Teardown must error
		if err := cm.Teardown(badID); err == nil {
			t.Errorf("Teardown(%q) should fail with path traversal error, got nil", badID)
		}

		// 5. GetStats must error
		if _, err := cm.GetStats(badID); err == nil {
			t.Errorf("GetStats(%q) should fail with path traversal error, got nil", badID)
		}
	}
}

func TestCgroupManager_SymlinkRejected(t *testing.T) {
	tmpDir := t.TempDir()
	cgroupRoot := filepath.Join(tmpDir, "cgroup", "compute")
	if err := os.MkdirAll(cgroupRoot, 0755); err != nil {
		t.Fatalf("failed to mkdir cgroup root: %v", err)
	}

	cm := NewCgroupManager(cgroupRoot)

	// Target directory outside cgroup hierarchy
	outsideDir := filepath.Join(tmpDir, "target-escape")
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatalf("failed to mkdir outside dir: %v", err)
	}

	// Create symlink inside cgroup root pointing to outside directory
	symlinkID := "symlink-vm"
	symlinkPath := filepath.Join(cgroupRoot, symlinkID)
	if err := os.Symlink(outsideDir, symlinkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	// 1. VMPath must reject symlink
	if _, err := cm.VMPath(symlinkID); err == nil {
		t.Errorf("expected VMPath to reject symlink %q, got nil", symlinkID)
	}

	// 2. Setup must reject symlink
	limits := CgroupLimits{
		VCPUs:    1,
		MemoryMB: 512,
	}
	if _, err := cm.Setup(symlinkID, limits); err == nil {
		t.Errorf("expected Setup to reject symlink %q, got nil", symlinkID)
	}

	// 3. Assign must reject symlink
	if err := cm.Assign(symlinkID, 1234); err == nil {
		t.Errorf("expected Assign to reject symlink %q, got nil", symlinkID)
	}

	// 4. Teardown must reject symlink
	if err := cm.Teardown(symlinkID); err == nil {
		t.Errorf("expected Teardown to reject symlink %q, got nil", symlinkID)
	}

	// 5. GetStats must reject symlink
	if _, err := cm.GetStats(symlinkID); err == nil {
		t.Errorf("expected GetStats to reject symlink %q, got nil", symlinkID)
	}
}

func TestCgroupManager_FailClosedLimitHandling(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a read-only root directory where subdirectories/files cannot be written
	cgroupRoot := filepath.Join(tmpDir, "readonly-cgroup")
	if err := os.MkdirAll(cgroupRoot, 0555); err != nil {
		t.Fatalf("failed to create readonly dir: %v", err)
	}
	defer os.Chmod(cgroupRoot, 0755) // for cleanup

	cm := NewCgroupManager(cgroupRoot)
	limits := CgroupLimits{
		VCPUs:    2,
		MemoryMB: 1024,
	}

	// Setup should fail concretely because it cannot create the VM cgroup directory
	_, err := cm.Setup("test-vm-fail", limits)
	if err == nil {
		t.Fatal("expected Setup to fail concretely on unwritable cgroup directory, got nil")
	}
}

func TestVMManager_CgroupSetupFailClosedByDefault(t *testing.T) {
	m, store := testConcurrentManager(t)
	ctx := context.Background()

	// 1. Verify that when cgroupMgr != nil, isCgroupEnforced() is true by default without explicit setting
	if !m.isCgroupEnforced() {
		t.Fatal("expected isCgroupEnforced() to be true by default when cgroupMgr is active")
	}

	// 2. Point cgroupMgr to an unwritable path so Setup is guaranteed to fail
	tmpDir := t.TempDir()
	readOnlyCgroup := filepath.Join(tmpDir, "readonly-cg")
	if err := os.MkdirAll(readOnlyCgroup, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(readOnlyCgroup, 0755)

	m.cgroupMgr = NewCgroupManager(readOnlyCgroup)

	vm := &db.VM{
		ID:        "vm-cgroup-failclosed",
		Name:      "cgroup-failclosed",
		OwnerID:   "tenant-1",
		Status:    db.StatusPaused,
		VCPUs:     1,
		MemoryMB:  512,
		TAPDevice: "tap-cgroup-fc",
	}
	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.vms[vm.ID] = vm
	m.mu.Unlock()

	// startVMProcess must fail closed
	err := m.startVMProcess(ctx, vm)
	if err == nil {
		t.Fatal("expected startVMProcess to fail closed when cgroup setup fails, got nil error")
	}
	if !strings.Contains(err.Error(), "failed to setup cgroup limits") {
		t.Fatalf("expected 'failed to setup cgroup limits' error, got: %v", err)
	}

	// 3. With explicit dev mode override (SetCgroupEnforce(false)), failure does not block execution at cgroup step
	m.SetCgroupEnforce(false)
	if m.isCgroupEnforced() {
		t.Fatal("expected isCgroupEnforced() to be false after SetCgroupEnforce(false)")
	}
}
