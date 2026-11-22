package firecracker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func createFakeJailerScript(t *testing.T, dir string) string {
	t.Helper()
	fakeJailer := filepath.Join(dir, "fake-jailer.sh")
	script := `#!/bin/sh
trap 'exit 0' TERM INT QUIT
while true; do
    sleep 1 &
    wait $!
done
`
	if err := os.WriteFile(fakeJailer, []byte(script), 0755); err != nil {
		t.Fatalf("failed to create fake jailer script: %v", err)
	}
	return fakeJailer
}

func TestAllocateJailerUIDGID(t *testing.T) {
	// 1. Verify range [100000, 165534] and UID == GID
	for _, id := range []string{"vm-1", "vm-2", "sandbox-alpha", "sandbox-beta", "tenant-100-vm-1"} {
		uid, gid := AllocateJailerUIDGID(id)
		if uid < MinJailerUID || uid > MaxJailerUID {
			t.Errorf("AllocateJailerUIDGID(%q) uid %d outside range [%d, %d]", id, uid, MinJailerUID, MaxJailerUID)
		}
		if gid != uid {
			t.Errorf("AllocateJailerUIDGID(%q) gid %d != uid %d", id, gid, uid)
		}
	}

	// 2. Determinism: same ID yields same UID/GID across calls
	uid1, gid1 := AllocateJailerUIDGID("vm-deterministic-test")
	uid2, gid2 := AllocateJailerUIDGID("vm-deterministic-test")
	if uid1 != uid2 || gid1 != gid2 {
		t.Errorf("AllocateJailerUIDGID is not deterministic: (%d, %d) vs (%d, %d)", uid1, gid1, uid2, gid2)
	}

	// 3. Distinct IDs receive distinct deterministic pairs (not shared 1000:1000)
	seen := make(map[int]string)
	distinctCount := 0
	totalCount := 100
	for i := 0; i < totalCount; i++ {
		vmID := fmt.Sprintf("vm-instance-%04d", i)
		uid, _ := AllocateJailerUIDGID(vmID)
		if uid == 1000 {
			t.Errorf("AllocateJailerUIDGID assigned legacy shared 1000 UID to %s", vmID)
		}
		if _, exists := seen[uid]; !exists {
			distinctCount++
			seen[uid] = vmID
		}
	}
	// With 100 VMs in a 65535 pool, distinct count should be at least 95
	if distinctCount < 95 {
		t.Errorf("unexpectedly high collision rate in AllocateJailerUIDGID: only %d distinct UIDs out of %d", distinctCount, totalCount)
	}
}

func TestLaunchJailer_SurvivesCallerContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	fakeJailer := createFakeJailerScript(t, tmpDir)

	kernelPath := filepath.Join(tmpDir, "vmlinux")
	rootfsPath := filepath.Join(tmpDir, "rootfs.ext4")
	if err := os.WriteFile(kernelPath, []byte("fake-kernel"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootfsPath, []byte("fake-rootfs"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	opts := JailerOptions{
		ID:            "vm-ctx-test",
		Firecracker:   "/usr/local/bin/firecracker",
		Jailer:        fakeJailer,
		ChrootBaseDir: tmpDir,
		KernelSrcPath: kernelPath,
		RootfsSrcPath: rootfsPath,
		BootArgs:      "console=ttyS0",
		VCPUs:         1,
		MemoryMB:      128,
	}

	ip, err := LaunchJailer(ctx, opts)
	if err != nil {
		t.Fatalf("LaunchJailer failed: %v", err)
	}
	defer func() {
		_ = ip.Stop(2 * time.Second)
		_ = ip.Cleanup()
	}()

	// Immediately cancel caller ctx
	cancel()

	// Wait briefly to confirm SIGKILL was NOT sent to running process
	time.Sleep(150 * time.Millisecond)

	// Process must still be alive
	if err := syscall.Kill(ip.PID, 0); err != nil {
		t.Fatalf("jailed process died after caller ctx was canceled: %v", err)
	}

	// Verify Stop cleanly terminates the process
	if err := ip.Stop(2 * time.Second); err != nil {
		t.Fatalf("ip.Stop failed: %v", err)
	}

	// Verify process is now dead
	time.Sleep(50 * time.Millisecond)
	if err := syscall.Kill(ip.PID, 0); err == nil {
		t.Fatal("jailed process should be terminated after ip.Stop, but is still alive")
	}
}

func TestLaunchJailer_RootfsAndExtraDrivePersistence_Hardlink(t *testing.T) {
	tmpDir := t.TempDir()
	fakeJailer := createFakeJailerScript(t, tmpDir)

	kernelPath := filepath.Join(tmpDir, "vmlinux")
	hostRootfsPath := filepath.Join(tmpDir, "vm-disk.ext4")
	hostVolumePath := filepath.Join(tmpDir, "vol-data0.ext4")

	origKernelContent := []byte("original-golden-kernel-bytes")
	origRootfsContent := []byte("initial-rootfs-content-v1")
	origVolumeContent := []byte("initial-volume-content-v1")

	if err := os.WriteFile(kernelPath, origKernelContent, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostRootfsPath, origRootfsContent, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostVolumePath, origVolumeContent, 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	opts := JailerOptions{
		ID:            "vm-persist-test",
		Firecracker:   "/usr/local/bin/firecracker",
		Jailer:        fakeJailer,
		ChrootBaseDir: tmpDir,
		KernelSrcPath: kernelPath,
		RootfsSrcPath: hostRootfsPath,
		BootArgs:      "console=ttyS0",
		VCPUs:         1,
		MemoryMB:      128,
		ExtraDrives: []ExtraDrive{
			{
				DriveID:    "data0",
				HostPath:   hostVolumePath,
				IsReadOnly: false,
			},
		},
	}

	ip, err := LaunchJailer(ctx, opts)
	if err != nil {
		t.Fatalf("LaunchJailer failed: %v", err)
	}

	chrootRootfs := filepath.Join(ip.ChrootDir, "rootfs.ext4")
	chrootData0 := filepath.Join(ip.ChrootDir, "data0.ext4")
	chrootKernel := filepath.Join(ip.ChrootDir, "vmlinux")

	// 1. Verify read-only vmlinux is NOT hardlinked (copied, different inode, host nlink == 1)
	hostKernelFi, err := os.Stat(kernelPath)
	if err != nil {
		t.Fatal(err)
	}
	chrootKernelFi, err := os.Stat(chrootKernel)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(hostKernelFi, chrootKernelFi) {
		t.Fatal("vmlinux MUST NOT be hardlinked into chroot! Golden kernel must be isolated to avoid chown mutation bleed")
	}

	// 2. Verify rootfs and volume ARE hardlinked (same inode, os.SameFile is true)
	hostRootfsFi, err := os.Stat(hostRootfsPath)
	if err != nil {
		t.Fatal(err)
	}
	chrootRootfsFi, err := os.Stat(chrootRootfs)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(hostRootfsFi, chrootRootfsFi) {
		t.Fatal("rootfs.ext4 in chroot must be hardlinked/bind-mounted to hostRootfsPath to prevent silent data loss")
	}

	hostVolFi, err := os.Stat(hostVolumePath)
	if err != nil {
		t.Fatal(err)
	}
	chrootVolFi, err := os.Stat(chrootData0)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(hostVolFi, chrootVolFi) {
		t.Fatal("data0.ext4 in chroot must be hardlinked/bind-mounted to hostVolumePath to prevent silent data loss")
	}

	// 3. Write data through chroot entries (simulating guest disk writes)
	guestRootfsUpdate := []byte("updated-rootfs-written-by-guest-microvm")
	guestVolumeUpdate := []byte("updated-volume-written-by-guest-microvm")
	if err := os.WriteFile(chrootRootfs, guestRootfsUpdate, 0644); err != nil {
		t.Fatalf("failed to write to chroot rootfs: %v", err)
	}
	if err := os.WriteFile(chrootData0, guestVolumeUpdate, 0644); err != nil {
		t.Fatalf("failed to write to chroot data0: %v", err)
	}

	// 4. Verify host files immediately reflect writes
	hostRootfsRead, err := os.ReadFile(hostRootfsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(hostRootfsRead) != string(guestRootfsUpdate) {
		t.Fatalf("expected host rootfs to reflect guest writes immediately: got %q, want %q", string(hostRootfsRead), string(guestRootfsUpdate))
	}

	hostVolRead, err := os.ReadFile(hostVolumePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(hostVolRead) != string(guestVolumeUpdate) {
		t.Fatalf("expected host volume to reflect guest writes immediately: got %q, want %q", string(hostVolRead), string(guestVolumeUpdate))
	}

	// 5. Stop and Cleanup()
	if err := ip.Stop(2 * time.Second); err != nil {
		t.Fatalf("ip.Stop failed: %v", err)
	}
	if err := ip.Cleanup(); err != nil {
		t.Fatalf("ip.Cleanup failed: %v", err)
	}

	// 6. Verify host files STILL intact after Cleanup()
	hostRootfsAfterCleanup, err := os.ReadFile(hostRootfsPath)
	if err != nil {
		t.Fatalf("host rootfs missing after Cleanup(): %v", err)
	}
	if string(hostRootfsAfterCleanup) != string(guestRootfsUpdate) {
		t.Fatalf("host rootfs lost data after Cleanup(): got %q, want %q", string(hostRootfsAfterCleanup), string(guestRootfsUpdate))
	}

	hostVolAfterCleanup, err := os.ReadFile(hostVolumePath)
	if err != nil {
		t.Fatalf("host volume missing after Cleanup(): %v", err)
	}
	if string(hostVolAfterCleanup) != string(guestVolumeUpdate) {
		t.Fatalf("host volume lost data after Cleanup(): got %q, want %q", string(hostVolAfterCleanup), string(guestVolumeUpdate))
	}

	// 7. Restart VM inside Jailer with the same host files
	ip2, err := LaunchJailer(ctx, opts)
	if err != nil {
		t.Fatalf("LaunchJailer restart failed: %v", err)
	}
	defer func() {
		_ = ip2.Stop(2 * time.Second)
		_ = ip2.Cleanup()
	}()

	chrootRootfs2 := filepath.Join(ip2.ChrootDir, "rootfs.ext4")
	chrootData02 := filepath.Join(ip2.ChrootDir, "data0.ext4")

	restartedRootfsRead, err := os.ReadFile(chrootRootfs2)
	if err != nil {
		t.Fatal(err)
	}
	if string(restartedRootfsRead) != string(guestRootfsUpdate) {
		t.Fatalf("restarted chroot rootfs did not retain guest writes: got %q, want %q", string(restartedRootfsRead), string(guestRootfsUpdate))
	}

	restartedVolRead, err := os.ReadFile(chrootData02)
	if err != nil {
		t.Fatal(err)
	}
	if string(restartedVolRead) != string(guestVolumeUpdate) {
		t.Fatalf("restarted chroot volume did not retain guest writes: got %q, want %q", string(restartedVolRead), string(guestVolumeUpdate))
	}
}

func TestLaunchJailer_Stop_NoDoubleWaitRace(t *testing.T) {
	tmpDir := t.TempDir()
	fakeJailer := createFakeJailerScript(t, tmpDir)

	kernelPath := filepath.Join(tmpDir, "vmlinux")
	rootfsPath := filepath.Join(tmpDir, "rootfs.ext4")
	_ = os.WriteFile(kernelPath, []byte("fake-kernel"), 0644)
	_ = os.WriteFile(rootfsPath, []byte("fake-rootfs"), 0644)

	ctx := context.Background()
	opts := JailerOptions{
		ID:            "vm-stop-race",
		Firecracker:   "/usr/local/bin/firecracker",
		Jailer:        fakeJailer,
		ChrootBaseDir: tmpDir,
		KernelSrcPath: kernelPath,
		RootfsSrcPath: rootfsPath,
		BootArgs:      "console=ttyS0",
		VCPUs:         1,
		MemoryMB:      128,
	}

	ip, err := LaunchJailer(ctx, opts)
	if err != nil {
		t.Fatalf("LaunchJailer failed: %v", err)
	}
	defer func() { _ = ip.Cleanup() }()

	// Spawn multiple concurrent callers calling Stop() and Wait()
	var wg sync.WaitGroup
	errCh := make(chan error, 10)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ip.Stop(2 * time.Second); err != nil {
				errCh <- fmt.Errorf("Stop failed: %w", err)
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ip.Wait(); err != nil {
				if strings.Contains(err.Error(), "already called") {
					errCh <- fmt.Errorf("double Wait race detected: %w", err)
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Errorf("unexpected error during concurrent Stop/Wait: %v", err)
		}
	}
}

func TestLaunchJailer_PreSpawnContextCanceled(t *testing.T) {
	tmpDir := t.TempDir()
	fakeJailer := createFakeJailerScript(t, tmpDir)

	kernelPath := filepath.Join(tmpDir, "vmlinux")
	rootfsPath := filepath.Join(tmpDir, "rootfs.ext4")
	_ = os.WriteFile(kernelPath, []byte("fake-kernel"), 0644)
	_ = os.WriteFile(rootfsPath, []byte("fake-rootfs"), 0644)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-canceled

	opts := JailerOptions{
		ID:            "vm-precancel",
		Firecracker:   "/usr/local/bin/firecracker",
		Jailer:        fakeJailer,
		ChrootBaseDir: tmpDir,
		KernelSrcPath: kernelPath,
		RootfsSrcPath: rootfsPath,
		BootArgs:      "console=ttyS0",
		VCPUs:         1,
		MemoryMB:      128,
	}

	_, err := LaunchJailer(ctx, opts)
	if err == nil {
		t.Fatal("expected LaunchJailer to return error for pre-canceled ctx, got nil")
	}
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}
