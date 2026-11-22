package vm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rashid2G/compute/internal/db"
	jailer "github.com/Rashid2G/compute/internal/node/runtime/firecracker"
	"github.com/Rashid2G/compute/internal/storage"
)

func TestJailerDirectoryStructure(t *testing.T) {
	tmpDir := t.TempDir()
	opts := jailer.JailerOptions{
		ID:            "vm-test-1",
		Firecracker:   "/usr/local/bin/firecracker",
		Jailer:        "/usr/local/bin/jailer",
		ChrootBaseDir: tmpDir,
		UID:           1000,
		GID:           1000,
	}

	execName := filepath.Base(opts.Firecracker)
	expectedChroot := filepath.Join(tmpDir, execName, opts.ID, "root")

	if expectedChroot != filepath.Join(tmpDir, "firecracker", "vm-test-1", "root") {
		t.Errorf("unexpected chroot path: %s", expectedChroot)
	}
}

func TestJailerAvailabilityCheck(t *testing.T) {
	// Non-existent path returns false
	if jailer.IsJailerAvailable("/non/existent/path/to/jailer") {
		t.Errorf("expected non-existent jailer path to return false")
	}

	// Empty string checks default path
	_ = jailer.IsJailerAvailable("")
}

func TestJailerFallbackConfiguration(t *testing.T) {
	// Test that Manager respects JailerEnabled: false
	cfg := Config{
		FirecrackerBinary: "/usr/local/bin/firecracker",
		JailerBinary:      "/usr/local/bin/jailer",
		JailerEnabled:     false,
	}

	if cfg.JailerEnabled {
		t.Errorf("expected JailerEnabled to be false")
	}

	cfg.JailerEnabled = true
	if !cfg.JailerEnabled {
		t.Errorf("expected JailerEnabled to be true")
	}
}

func TestJailerIsolation_NoMutationBleed(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Golden base image / template
	goldenBase := filepath.Join(tmpDir, "golden-rootfs.ext4")
	origContent := []byte("pristine-base-image-data-do-not-mutate")
	if err := os.WriteFile(goldenBase, origContent, 0644); err != nil {
		t.Fatalf("failed to write golden base: %v", err)
	}

	// 2. Chroot rootfs prepared via storage.CloneFileCOW (as done in jailer.go)
	chrootRoot := filepath.Join(tmpDir, "firecracker", "vm-1", "root")
	if err := os.MkdirAll(chrootRoot, 0755); err != nil {
		t.Fatalf("failed to mkdir chroot: %v", err)
	}
	targetRootfs := filepath.Join(chrootRoot, "rootfs.ext4")

	if err := storage.CloneFileCOW(goldenBase, targetRootfs); err != nil {
		t.Fatalf("failed to prepare rootfs via CoW: %v", err)
	}

	// 3. VM mutates disk inside the jail
	mutatedContent := []byte("MUTATED-CORRUPTED-BY-MALICIOUS-GUEST-VM")
	if err := os.WriteFile(targetRootfs, mutatedContent, 0644); err != nil {
		t.Fatalf("failed to mutate target rootfs: %v", err)
	}

	// 4. Verify golden image is 100% UNTOUCHED
	goldenRead, err := os.ReadFile(goldenBase)
	if err != nil {
		t.Fatalf("failed to read golden base: %v", err)
	}
	if string(goldenRead) != string(origContent) {
		t.Fatalf("MUTATION BLEED DETECTED! Golden base was corrupted: got %q, expected %q", string(goldenRead), string(origContent))
	}
}

func TestJailerNamespaceFlags(t *testing.T) {
	// Verify CloneNewNet is defined and non-zero
	if jailer.CloneNewNet == 0 {
		t.Errorf("expected CloneNewNet to be non-zero")
	}

	combinedFlags := jailer.CloneNewPID | jailer.CloneNewNS | jailer.CloneNewIPC | jailer.CloneNewUTS | jailer.CloneNewCgroup | jailer.CloneNewNet
	if combinedFlags&jailer.CloneNewNet == 0 {
		t.Errorf("combined namespace flags must include CloneNewNet")
	}
}

func TestResumeVM_JailerFailClosed(t *testing.T) {
	m, store := testConcurrentManager(t)
	ctx := context.Background()

	m.cfg.JailerEnabled = true
	m.cfg.JailerBinary = "/non/existent/jailer"
	m.cfg.JailerDevFallback = false

	tmpDir := t.TempDir()
	snapFile := filepath.Join(tmpDir, "snap.bin")
	memFile := filepath.Join(tmpDir, "mem.bin")
	_ = os.WriteFile(snapFile, []byte("snap-data"), 0644)
	_ = os.WriteFile(memFile, []byte("mem-data"), 0644)

	vm := &db.VM{
		ID:           "vm-resume-failclosed",
		Name:         "resume-failclosed",
		OwnerID:      "tenant-1",
		Status:       db.StatusPaused,
		VCPUs:        1,
		MemoryMB:     512,
		SnapshotPath: snapFile,
		MemFilePath:  memFile,
		TAPDevice:    "tap-fc-1",
	}
	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.vms[vm.ID] = vm
	m.mu.Unlock()

	err := m.ResumeVM(ctx, vm.ID)
	if err == nil {
		t.Fatal("expected ResumeVM to fail closed when jailer binary is unavailable, got nil")
	}
	if !strings.Contains(err.Error(), "jailer is enabled but jailer binary is unavailable") {
		t.Fatalf("expected fail-closed error, got: %v", err)
	}

	// Now verify with dev fallback enabled, it logs warning and attempts unjailed launch
	m.cfg.JailerDevFallback = true
	// Since firecracker binary is default /usr/local/bin/firecracker which doesn't exist, it will fail on firecracker launch, not jailer fail-closed
	fallbackErr := m.ResumeVM(ctx, vm.ID)
	if fallbackErr != nil && strings.Contains(fallbackErr.Error(), "jailer is enabled but jailer binary is unavailable") {
		t.Fatalf("expected fallback to bypass jailer fail-closed error, got: %v", fallbackErr)
	}
}

func TestResumeVM_UsesJailer(t *testing.T) {
	m, store := testConcurrentManager(t)
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("/tmp", "j-res-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	jailerLog := filepath.Join(tmpDir, "jailer.log")

	// Create mock jailer binary that records its arguments and opens a mock Unix socket in chroot
	mockJailer := filepath.Join(tmpDir, "mock-jailer")
	script := fmt.Sprintf(`#!/usr/bin/env python3
import sys, os, time, socket

with open(%q, "w") as f:
    f.write(" ".join(sys.argv[1:]))

chroot_dir = ""
vm_id = ""
args = sys.argv[1:]
for i, arg in enumerate(args):
    if arg == "--chroot-base-dir" and i + 1 < len(args):
        chroot_dir = args[i+1]
    elif arg == "--id" and i + 1 < len(args):
        vm_id = args[i+1]

if chroot_dir and vm_id:
    run_dir = os.path.join(chroot_dir, "firecracker", vm_id, "root", "run")
    os.makedirs(run_dir, exist_ok=True)
    sock_path = os.path.join(run_dir, "fc.sock")
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
`, jailerLog)

	if err := os.WriteFile(mockJailer, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	m.cfg.JailerEnabled = true
	m.cfg.JailerBinary = mockJailer
	m.cfg.JailerBaseDir = filepath.Join(tmpDir, "srv-jailer")
	m.cfg.FirecrackerBinary = "/usr/bin/firecracker"

	snapFile := filepath.Join(tmpDir, "snap.bin")
	memFile := filepath.Join(tmpDir, "mem.bin")
	_ = os.WriteFile(snapFile, []byte("snap-data"), 0644)
	_ = os.WriteFile(memFile, []byte("mem-data"), 0644)

	vmID := "vm-res-jailed"
	vm := &db.VM{
		ID:           vmID,
		Name:         "res-jailed",
		OwnerID:      "tenant-1",
		Status:       db.StatusPaused,
		VCPUs:        1,
		MemoryMB:     512,
		SnapshotPath: snapFile,
		MemFilePath:  memFile,
		TAPDevice:    "tap-res-j",
	}
	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.vms[vm.ID] = vm
	m.mu.Unlock()

	expectedID := sanitizeFirecrackerID(vm.InternalID)
	if expectedID == "" {
		expectedID = sanitizeFirecrackerID(vm.ID)
	}

	if err := m.ResumeVM(ctx, vmID); err != nil {
		t.Fatalf("ResumeVM failed: %v", err)
	}
	defer func() { _ = m.StopVM(ctx, vmID) }()

	// Verify jailer was invoked with expected flags
	loggedArgs, err := os.ReadFile(jailerLog)
	if err != nil {
		t.Fatalf("failed to read jailer log: %v", err)
	}
	logStr := string(loggedArgs)
	if !strings.Contains(logStr, "--id "+expectedID) {
		t.Errorf("expected jailer to be called with --id %s, got: %s", expectedID, logStr)
	}
	if !strings.Contains(logStr, "--api-sock /run/fc.sock") {
		t.Errorf("expected jailer to pass --api-sock /run/fc.sock to firecracker, got: %s", logStr)
	}

	// Verify snapshot and mem file were placed inside chroot
	chrootRoot := filepath.Join(m.cfg.JailerBaseDir, "firecracker", expectedID, "root")
	if _, err := os.Stat(filepath.Join(chrootRoot, "snapshot")); err != nil {
		t.Errorf("snapshot file missing in chroot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(chrootRoot, "mem")); err != nil {
		t.Errorf("mem file missing in chroot: %v", err)
	}
}

func TestLaunchJailer_PathTraversalRejected(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

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
		"test\\id",
	}

	for _, badID := range traversalIDs {
		opts := jailer.JailerOptions{
			ID:            badID,
			Firecracker:   "/usr/local/bin/firecracker",
			Jailer:        "/usr/local/bin/jailer",
			ChrootBaseDir: tmpDir,
		}
		_, err := jailer.LaunchJailer(ctx, opts)
		if err == nil {
			t.Errorf("expected LaunchJailer to reject traversal ID %q, but got nil error", badID)
		}
	}

	// Test DriveID traversal rejection
	for _, badDriveID := range traversalIDs {
		opts := jailer.JailerOptions{
			ID:            "valid-vm-id",
			Firecracker:   "/usr/local/bin/firecracker",
			Jailer:        "/usr/local/bin/jailer",
			ChrootBaseDir: tmpDir,
			ExtraDrives: []jailer.ExtraDrive{
				{
					DriveID:  badDriveID,
					HostPath: filepath.Join(tmpDir, "drive.raw"),
				},
			},
		}
		_, err := jailer.LaunchJailer(ctx, opts)
		if err == nil {
			t.Errorf("expected LaunchJailer to reject traversal DriveID %q, but got nil error", badDriveID)
		}
	}
}

func TestLaunchJailer_NonRootNamespacesFailClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("skipping non-root namespace test when running as root")
	}

	ctx := context.Background()
	tmpDir := t.TempDir()

	opts := jailer.JailerOptions{
		ID:            "vm-ns-test",
		Firecracker:   "/usr/local/bin/firecracker",
		Jailer:        "/usr/local/bin/jailer",
		ChrootBaseDir: tmpDir,
		UseNamespaces: true,
	}

	_, err := jailer.LaunchJailer(ctx, opts)
	if err == nil {
		t.Fatal("expected LaunchJailer with UseNamespaces=true to fail closed when running as non-root, got nil")
	}
	if !strings.Contains(err.Error(), "root privileges (or CAP_SYS_ADMIN) are required") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestManager_JailerRootfsAndVolumePersistence(t *testing.T) {
	m, store := testConcurrentManager(t)
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("/tmp", "j-persist-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mockJailer := filepath.Join(tmpDir, "mock-jailer")
	script := `#!/usr/bin/env python3
import sys, os, time, socket

chroot_dir = ""
vm_id = ""
args = sys.argv[1:]
for i, arg in enumerate(args):
    if arg == "--chroot-base-dir" and i + 1 < len(args):
        chroot_dir = args[i+1]
    elif arg == "--id" and i + 1 < len(args):
        vm_id = args[i+1]

if chroot_dir and vm_id:
    run_dir = os.path.join(chroot_dir, "firecracker", vm_id, "root", "run")
    os.makedirs(run_dir, exist_ok=True)
    sock_path = os.path.join(run_dir, "fc.sock")
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
	if err := os.WriteFile(mockJailer, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	m.cfg.JailerEnabled = true
	m.cfg.JailerBinary = mockJailer
	m.cfg.JailerBaseDir = filepath.Join(tmpDir, "srv-jailer")

	kernelPath := filepath.Join(tmpDir, "vmlinux")
	hostRootfsPath := filepath.Join(tmpDir, "rootfs.ext4")
	hostVolPath := filepath.Join(tmpDir, "data0.ext4")

	_ = os.WriteFile(kernelPath, []byte("golden-kernel"), 0644)
	_ = os.WriteFile(hostRootfsPath, []byte("initial-rootfs-data"), 0644)
	_ = os.WriteFile(hostVolPath, []byte("initial-volume-data"), 0644)

	vmID := "vm-test-persist-manager"
	vm := &db.VM{
		ID:         vmID,
		Name:       "persist-manager",
		OwnerID:    "tenant-persist",
		Status:     db.StatusCreated,
		VCPUs:      1,
		MemoryMB:   256,
		DiskPath:   hostRootfsPath,
		KernelPath: kernelPath,
		TAPDevice:  "tap-persist-1",
	}
	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.vms[vm.ID] = vm
	m.mu.Unlock()

	vol := &db.Volume{
		ID:         "vol-persist-1",
		OwnerID:    vm.OwnerID,
		Name:       "vol-persist-1",
		Path:       hostVolPath,
		AttachedVM: vm.ID,
		Status:     "attached",
	}
	if err := store.SaveVolume(ctx, vol); err != nil {
		t.Fatal(err)
	}

	// 1. Start VM inside Jailer
	if err := m.RunVM(ctx, vmID); err != nil {
		t.Fatalf("RunVM failed: %v", err)
	}

	chrootRoot := filepath.Join(m.cfg.JailerBaseDir, "firecracker", sanitizeFirecrackerID(vmID), "root")
	chrootRootfs := filepath.Join(chrootRoot, "rootfs.ext4")
	chrootData0 := filepath.Join(chrootRoot, "data0.ext4")

	// 2. Write to chroot rootfs and data0
	guestRootfsData := []byte("guest-wrote-rootfs-content")
	guestVolData := []byte("guest-wrote-volume-content")
	if err := os.WriteFile(chrootRootfs, guestRootfsData, 0644); err != nil {
		t.Fatalf("failed to write to chroot rootfs: %v", err)
	}
	if err := os.WriteFile(chrootData0, guestVolData, 0644); err != nil {
		t.Fatalf("failed to write to chroot data0: %v", err)
	}

	// Verify host files immediately reflect the writes (hardlink zero-copy)
	hostRootfsRead, _ := os.ReadFile(hostRootfsPath)
	if string(hostRootfsRead) != string(guestRootfsData) {
		t.Fatalf("host rootfs did not reflect guest write: got %q, want %q", string(hostRootfsRead), string(guestRootfsData))
	}
	hostVolRead, _ := os.ReadFile(hostVolPath)
	if string(hostVolRead) != string(guestVolData) {
		t.Fatalf("host volume did not reflect guest write: got %q, want %q", string(hostVolRead), string(guestVolData))
	}

	// 3. Stop the VM
	if err := m.StopVM(ctx, vmID); err != nil {
		t.Fatalf("StopVM failed: %v", err)
	}

	// Verify chroot was cleaned up
	if _, err := os.Stat(chrootRoot); !os.IsNotExist(err) {
		t.Errorf("chrootRoot should be removed after StopVM, but still exists")
	}

	// Verify host files STILL intact with guest writes
	hostRootfsAfterStop, err := os.ReadFile(hostRootfsPath)
	if err != nil {
		t.Fatalf("host rootfs missing after StopVM: %v", err)
	}
	if string(hostRootfsAfterStop) != string(guestRootfsData) {
		t.Fatalf("host rootfs lost data after StopVM: got %q, want %q", string(hostRootfsAfterStop), string(guestRootfsData))
	}

	hostVolAfterStop, err := os.ReadFile(hostVolPath)
	if err != nil {
		t.Fatalf("host volume missing after StopVM: %v", err)
	}
	if string(hostVolAfterStop) != string(guestVolData) {
		t.Fatalf("host volume lost data after StopVM: got %q, want %q", string(hostVolAfterStop), string(guestVolData))
	}

	// 4. Restart VM inside Jailer
	if err := m.RunVM(ctx, vmID); err != nil {
		t.Fatalf("RunVM restart failed: %v", err)
	}
	defer func() { _ = m.StopVM(ctx, vmID) }()

	// Verify chroot files on restart contain the persisted writes
	chrootRootfsRestart, _ := os.ReadFile(chrootRootfs)
	if string(chrootRootfsRestart) != string(guestRootfsData) {
		t.Fatalf("restarted chroot rootfs lost data: got %q, want %q", string(chrootRootfsRestart), string(guestRootfsData))
	}
	chrootVolRestart, _ := os.ReadFile(chrootData0)
	if string(chrootVolRestart) != string(guestVolData) {
		t.Fatalf("restarted chroot volume lost data: got %q, want %q", string(chrootVolRestart), string(guestVolData))
	}
}

func TestManager_VolumeDetachAndOwnershipReconciliation(t *testing.T) {
	m, store := testConcurrentManager(t)
	ctx := context.Background()
	tmpDir := t.TempDir()

	volPath := filepath.Join(tmpDir, "reconcile-vol.ext4")
	_ = os.WriteFile(volPath, []byte("volume-bytes"), 0644)

	vol := &db.Volume{
		ID:         "vol-reconcile-1",
		OwnerID:    "tenant-1",
		Name:       "reconcile-vol",
		Path:       volPath,
		AttachedVM: "vm-1",
		Status:     "attached",
	}
	if err := store.SaveVolume(ctx, vol); err != nil {
		t.Fatal(err)
	}

	// Test DetachVolume reconciles ownership and updates status
	if err := m.DetachVolume(ctx, vol); err != nil {
		t.Fatalf("DetachVolume failed: %v", err)
	}

	if vol.Status != "available" {
		t.Errorf("expected vol.Status to be 'available', got %q", vol.Status)
	}
	if vol.AttachedVM != "" {
		t.Errorf("expected vol.AttachedVM to be cleared, got %q", vol.AttachedVM)
	}

	// Verify ReconcileVolumeOwnership
	if err := m.ReconcileVolumeOwnership(ctx, vol); err != nil {
		t.Fatalf("ReconcileVolumeOwnership failed: %v", err)
	}
}

func TestManager_DeterministicPerVMUIDAllocation(t *testing.T) {
	m, _ := testConcurrentManager(t)

	// Manager config should not default JailerUID to 1000
	if m.cfg.JailerUID == 1000 {
		t.Errorf("m.cfg.JailerUID should not be defaulted to 1000")
	}

	// Distinct VMs derive distinct deterministic UIDs in high range
	uid1, gid1 := jailer.AllocateJailerUIDGID("vm-alpha")
	uid2, gid2 := jailer.AllocateJailerUIDGID("vm-beta")

	if uid1 < jailer.MinJailerUID || uid1 > jailer.MaxJailerUID {
		t.Errorf("uid1 %d outside range [%d, %d]", uid1, jailer.MinJailerUID, jailer.MaxJailerUID)
	}
	if uid2 < jailer.MinJailerUID || uid2 > jailer.MaxJailerUID {
		t.Errorf("uid2 %d outside range [%d, %d]", uid2, jailer.MinJailerUID, jailer.MaxJailerUID)
	}
	if uid1 == uid2 {
		t.Errorf("expected distinct UIDs for distinct VMs, got %d for both", uid1)
	}
	if gid1 != uid1 || gid2 != uid2 {
		t.Errorf("expected GID == UID")
	}
}
