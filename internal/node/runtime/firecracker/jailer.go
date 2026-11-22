package firecracker

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Rashid2G/compute/internal/storage"
)

// Linux namespace clone flags for isolation hardening
const (
	CloneNewPID    = 0x20000000 // syscall.CLONE_NEWPID
	CloneNewNet    = 0x40000000 // syscall.CLONE_NEWNET
	CloneNewNS     = 0x00020000 // syscall.CLONE_NEWNS
	CloneNewIPC    = 0x08000000 // syscall.CLONE_NEWIPC
	CloneNewUTS    = 0x04000000 // syscall.CLONE_NEWUTS
	CloneNewCgroup = 0x02000000 // syscall.CLONE_NEWCGROUP / unix.CLONE_NEWCGROUP
)

// Jailer UID/GID allocation range for per-VM deterministic isolation.
const (
	MinJailerUID = 100000
	MaxJailerUID = 165534
)

// AllocateJailerUIDGID deterministically derives a unique non-root UID/GID pair
// from a VM ID in the dedicated high range [100000, 165534].
func AllocateJailerUIDGID(vmID string) (int, int) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(vmID))
	poolSize := uint32(MaxJailerUID - MinJailerUID + 1)
	val := MinJailerUID + int(h.Sum32()%poolSize)
	return val, val
}

// SafeChown changes ownership of the given path to uid/gid. When running as root,
// errors are returned; when running unprivileged (e.g. in unit tests), permission
// errors (EPERM) are cleanly ignored.
func SafeChown(path string, uid, gid int) error {
	if path == "" {
		return nil
	}
	if err := os.Chown(path, uid, gid); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if os.Geteuid() != 0 && (errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM)) {
			return nil
		}
		if os.Geteuid() == 0 {
			return fmt.Errorf("failed to chown %s to %d:%d: %w", path, uid, gid, err)
		}
	}
	return nil
}

// UnmountFile safely unmounts a bind-mounted file if active.
func UnmountFile(target string) error {
	return unmountFile(target)
}

// ExtraDrive specifies additional volumes attached to the jailer instance.
type ExtraDrive struct {
	DriveID    string
	HostPath   string
	IsReadOnly bool
}

// JailerOptions defines parameters for launching Firecracker inside Jailer.
type JailerOptions struct {
	ID            string
	Firecracker   string
	Jailer        string
	ChrootBaseDir string
	UID           int
	GID           int
	VCPUs         int
	MemoryMB      int
	KernelSrcPath string
	RootfsSrcPath string
	BootArgs      string
	TapName       string
	MAC           string
	Daemonize     bool
	NetNS         string
	UseNamespaces bool
	ExtraDrives   []ExtraDrive

	IsResume        bool
	SnapshotSrcPath string
	MemFileSrcPath  string
}

// InstanceProcess represents a microVM process launched inside Jailer.
type InstanceProcess struct {
	ID               string
	ChrootDir        string
	Cmd              *exec.Cmd
	PID              int
	VsockUDS         string
	APISock          string
	SnapshotInChroot string
	MemFileInChroot  string
	MountedPaths     []string

	exitCh   chan struct{}
	waitErr  error
	waitOnce sync.Once
}

// Exited returns a channel that is closed when the jailer process terminates.
func (ip *InstanceProcess) Exited() <-chan struct{} {
	return ip.exitCh
}

// Wait waits for the jailer process to exit and returns its exit status.
// Can be called concurrently or repeatedly without double-Wait races.
func (ip *InstanceProcess) Wait() error {
	if ip == nil || ip.exitCh == nil {
		return nil
	}
	<-ip.exitCh
	return ip.waitErr
}

// Stop terminates the microVM instance gracefully or forcefully.
// It signals the process/process-group and coordinates cleanly without double-Wait races.
func (ip *InstanceProcess) Stop(timeout time.Duration) error {
	if ip == nil || ip.Cmd == nil || ip.Cmd.Process == nil {
		return nil
	}

	select {
	case <-ip.exitCh:
		return nil
	default:
	}

	// Try SIGTERM on the process group
	_ = signalProcess(ip, syscall.SIGTERM)

	select {
	case <-ip.exitCh:
		return nil
	case <-time.After(timeout):
		// Force kill with SIGKILL on the process group
		_ = signalProcess(ip, syscall.SIGKILL)
		select {
		case <-ip.exitCh:
			return fmt.Errorf("process did not terminate within %v, killed with SIGKILL", timeout)
		case <-time.After(500 * time.Millisecond):
			return fmt.Errorf("process did not terminate within %v, killed with SIGKILL", timeout)
		}
	}
}

// Cleanup unmounts any active bind mounts and removes the chroot jail directory.
func (ip *InstanceProcess) Cleanup() error {
	if ip == nil {
		return nil
	}
	var firstErr error
	for i := len(ip.MountedPaths) - 1; i >= 0; i-- {
		p := ip.MountedPaths[i]
		if err := unmountFile(p); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	ip.MountedPaths = nil

	if ip.ChrootDir != "" {
		entries, _ := os.ReadDir(ip.ChrootDir)
		for _, e := range entries {
			_ = os.Remove(filepath.Join(ip.ChrootDir, e.Name()))
		}
		if err := os.RemoveAll(filepath.Dir(ip.ChrootDir)); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// linkOrBindMount creates a hardlink from src to dst. If linking fails with EXDEV
// (cross-device), it falls back on Linux to bind-mounting src onto dst.
// Mounted destination paths are appended to mountedPaths so they can be cleanly unmounted on Stop/Cleanup.
func linkOrBindMount(src, dst string, mountedPaths *[]string) error {
	_ = os.Remove(dst)
	err := os.Link(src, dst)
	if err == nil {
		return nil
	}

	var linkErr *os.LinkError
	isEXDEV := errors.Is(err, syscall.EXDEV)
	if !isEXDEV && errors.As(err, &linkErr) {
		if errors.Is(linkErr.Err, syscall.EXDEV) {
			isEXDEV = true
		}
	}

	if isEXDEV {
		if mountErr := bindMountFile(src, dst); mountErr != nil {
			return fmt.Errorf("failed cross-device bind mount %s -> %s: %w", src, dst, mountErr)
		}
		*mountedPaths = append(*mountedPaths, dst)
		return nil
	}

	return fmt.Errorf("failed to hardlink %s -> %s: %w", src, dst, err)
}

// IsJailerAvailable checks if the jailer binary exists and is executable.
func IsJailerAvailable(jailerPath string) bool {
	if jailerPath == "" {
		jailerPath = "/usr/local/bin/jailer"
	}
	if _, err := exec.LookPath(jailerPath); err == nil {
		return true
	}
	if fi, err := os.Stat(jailerPath); err == nil && (fi.Mode()&0111 != 0) {
		return true
	}
	return false
}

// LaunchJailer initializes the chroot jail, prepares files, and launches Firecracker inside Jailer.
func LaunchJailer(ctx context.Context, opts JailerOptions) (*InstanceProcess, error) {
	// Validate opts.ID on raw input to prevent path traversal
	if opts.ID == "" || strings.ContainsAny(opts.ID, "/\\") || strings.Contains(opts.ID, "..") {
		return nil, fmt.Errorf("invalid jailer id: %s", opts.ID)
	}
	cleanID := filepath.Clean(opts.ID)
	if cleanID == "." || cleanID == ".." {
		return nil, fmt.Errorf("invalid jailer id: %s", opts.ID)
	}

	// Validate each drive.DriveID on raw input
	for _, drive := range opts.ExtraDrives {
		if drive.DriveID == "" || strings.ContainsAny(drive.DriveID, "/\\") || strings.Contains(drive.DriveID, "..") {
			return nil, fmt.Errorf("invalid drive id: %s", drive.DriveID)
		}
		cleanDriveID := filepath.Clean(drive.DriveID)
		if cleanDriveID == "." || cleanDriveID == ".." {
			return nil, fmt.Errorf("invalid drive id: %s", drive.DriveID)
		}
	}

	// Fail closed if namespace isolation was requested but process does not run as root
	if opts.UseNamespaces && os.Geteuid() != 0 {
		return nil, fmt.Errorf("root privileges (or CAP_SYS_ADMIN) are required for namespace isolation")
	}

	if opts.Firecracker == "" {
		opts.Firecracker = "/usr/local/bin/firecracker"
	}
	if opts.Jailer == "" {
		opts.Jailer = "/usr/local/bin/jailer"
	}
	if opts.ChrootBaseDir == "" {
		opts.ChrootBaseDir = "/srv/jailer"
	}
	if opts.UID <= 0 {
		opts.UID, opts.GID = AllocateJailerUIDGID(opts.ID)
	}
	if opts.GID <= 0 {
		opts.GID = opts.UID
	}

	cleanBaseDir := filepath.Clean(opts.ChrootBaseDir)
	execName := filepath.Base(opts.Firecracker)
	chrootRoot := filepath.Clean(filepath.Join(cleanBaseDir, execName, opts.ID, "root"))

	// Canonical path check inside ChrootBaseDir
	rel, err := filepath.Rel(cleanBaseDir, chrootRoot)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return nil, fmt.Errorf("jailer chroot path escapes base directory: %s", chrootRoot)
	}

	if err := os.MkdirAll(chrootRoot, 0755); err != nil {
		return nil, fmt.Errorf("failed to create chroot directory %s: %w", chrootRoot, err)
	}
	_ = SafeChown(chrootRoot, opts.UID, opts.GID)

	var mountedPaths []string
	unmountAll := func() {
		for i := len(mountedPaths) - 1; i >= 0; i-- {
			_ = unmountFile(mountedPaths[i])
		}
		mountedPaths = nil
	}

	if opts.IsResume {
		// Link or bind-mount rootfs into chroot if provided so writes persist to opts.RootfsSrcPath
		if opts.RootfsSrcPath != "" {
			targetRootfs := filepath.Join(chrootRoot, "rootfs.ext4")
			if err := linkOrBindMount(opts.RootfsSrcPath, targetRootfs, &mountedPaths); err != nil {
				unmountAll()
				return nil, fmt.Errorf("failed to link rootfs: %w", err)
			}
			_ = SafeChown(targetRootfs, opts.UID, opts.GID)
		}

		// Copy snapshot into chroot
		targetSnap := filepath.Join(chrootRoot, "snapshot")
		_ = os.Remove(targetSnap)
		if opts.SnapshotSrcPath != "" {
			if err := storage.CloneFileCOW(opts.SnapshotSrcPath, targetSnap); err != nil {
				unmountAll()
				return nil, fmt.Errorf("failed to copy snapshot: %w", err)
			}
			_ = SafeChown(targetSnap, opts.UID, opts.GID)
		}

		// Copy mem file into chroot
		targetMem := filepath.Join(chrootRoot, "mem")
		_ = os.Remove(targetMem)
		if opts.MemFileSrcPath != "" {
			if err := storage.CloneFileCOW(opts.MemFileSrcPath, targetMem); err != nil {
				unmountAll()
				return nil, fmt.Errorf("failed to copy mem file: %w", err)
			}
			_ = SafeChown(targetMem, opts.UID, opts.GID)
		}

		// Prepare attached extra data volumes via direct link/bind-mount
		for _, ed := range opts.ExtraDrives {
			targetDrive := filepath.Join(chrootRoot, ed.DriveID+".ext4")
			if err := linkOrBindMount(ed.HostPath, targetDrive, &mountedPaths); err != nil {
				unmountAll()
				return nil, fmt.Errorf("failed to link extra drive %s: %w", ed.DriveID, err)
			}
			_ = SafeChown(targetDrive, opts.UID, opts.GID)
		}

		// UDS socket paths inside and outside chroot
		vsockUDSOnHost := filepath.Join(chrootRoot, "v.sock")
		apiSockOnHost := filepath.Join(chrootRoot, "run", "fc.sock")
		_ = os.MkdirAll(filepath.Join(chrootRoot, "run"), 0755)
		_ = SafeChown(filepath.Join(chrootRoot, "run"), opts.UID, opts.GID)

		// Cgroup v2 limits
		cpuQuota := opts.VCPUs * 100000
		memMaxBytes := int64(opts.MemoryMB+64) * 1024 * 1024

		args := []string{
			"--id", opts.ID,
			"--exec-file", opts.Firecracker,
			"--uid", fmt.Sprintf("%d", opts.UID),
			"--gid", fmt.Sprintf("%d", opts.GID),
			"--chroot-base-dir", opts.ChrootBaseDir,
			"--cgroup-version", "2",
			"--cgroup", fmt.Sprintf("cpu.max=%d 100000", cpuQuota),
			"--cgroup", fmt.Sprintf("memory.max=%d", memMaxBytes),
			"--cgroup", "pids.max=512",
			"--new-pid-ns",
		}

		if opts.NetNS != "" {
			args = append(args, "--netns", opts.NetNS)
		}

		if opts.Daemonize {
			args = append(args, "--daemonize")
		}

		// Parameters passed to firecracker for snapshot resume
		args = append(args, "--", "--api-sock", "/run/fc.sock")

		// Check context cancellation before spawning to avoid race
		if err := ctx.Err(); err != nil {
			unmountAll()
			return nil, err
		}

		// Detach jailer lifecycle from caller request/startup ctx:
		// use exec.Command with Setpgid: true so request completion never kills running microVMs
		cmd := exec.Command(opts.Jailer, args...)
		cmd.SysProcAttr = newSysProcAttr(opts.UseNamespaces, opts.NetNS)

		if err := cmd.Start(); err != nil {
			unmountAll()
			return nil, fmt.Errorf("failed to spawn jailer for resume: %w", err)
		}

		pid := cmd.Process.Pid

		ip := &InstanceProcess{
			ID:               opts.ID,
			ChrootDir:        chrootRoot,
			Cmd:              cmd,
			PID:              pid,
			VsockUDS:         vsockUDSOnHost,
			APISock:          apiSockOnHost,
			SnapshotInChroot: "/snapshot",
			MemFileInChroot:  "/mem",
			MountedPaths:     mountedPaths,
			exitCh:           make(chan struct{}),
		}
		go func() {
			if ip.Cmd != nil {
				ip.waitErr = ip.Cmd.Wait()
			}
			close(ip.exitCh)
		}()

		return ip, nil
	}

	// Copy kernel into chroot (never hardlink golden kernel image to avoid chown mutation bleed)
	targetKernel := filepath.Join(chrootRoot, "vmlinux")
	_ = os.Remove(targetKernel)
	if err := storage.CloneFileCOW(opts.KernelSrcPath, targetKernel); err != nil {
		unmountAll()
		return nil, fmt.Errorf("failed to copy kernel: %w", err)
	}
	_ = SafeChown(targetKernel, opts.UID, opts.GID)

	// Link or bind-mount rootfs into chroot so writes persist directly to opts.RootfsSrcPath
	targetRootfs := filepath.Join(chrootRoot, "rootfs.ext4")
	if err := linkOrBindMount(opts.RootfsSrcPath, targetRootfs, &mountedPaths); err != nil {
		unmountAll()
		return nil, fmt.Errorf("failed to link rootfs: %w", err)
	}
	_ = SafeChown(targetRootfs, opts.UID, opts.GID)

	// UDS socket paths inside and outside chroot
	vsockUDSInChroot := "/v.sock"
	vsockUDSOnHost := filepath.Join(chrootRoot, "v.sock")
	apiSockOnHost := filepath.Join(chrootRoot, "run", "fc.sock")
	_ = os.MkdirAll(filepath.Join(chrootRoot, "run"), 0755)
	_ = SafeChown(filepath.Join(chrootRoot, "run"), opts.UID, opts.GID)

	// Generate config.json inside chroot
	cfg, err := BuildVMConfig(
		"/vmlinux",
		"/rootfs.ext4",
		opts.BootArgs,
		opts.VCPUs,
		opts.MemoryMB,
		opts.TapName,
		opts.MAC,
		vsockUDSInChroot,
	)
	if err != nil {
		unmountAll()
		return nil, fmt.Errorf("failed to build vm config: %w", err)
	}

	// Prepare attached extra data volumes via direct link/bind-mount
	for _, ed := range opts.ExtraDrives {
		targetDrive := filepath.Join(chrootRoot, ed.DriveID+".ext4")
		if err := linkOrBindMount(ed.HostPath, targetDrive, &mountedPaths); err != nil {
			unmountAll()
			return nil, fmt.Errorf("failed to link extra drive %s: %w", ed.DriveID, err)
		}
		_ = SafeChown(targetDrive, opts.UID, opts.GID)
		cfg.Drives = append(cfg.Drives, Drive{
			DriveID:      ed.DriveID,
			PathOnHost:   "/" + ed.DriveID + ".ext4",
			IsRootDevice: false,
			IsReadOnly:   ed.IsReadOnly,
		})
	}

	cfgBytes, err := cfg.ToJSON()
	if err != nil {
		unmountAll()
		return nil, fmt.Errorf("failed to marshal vm config: %w", err)
	}

	configPath := filepath.Join(chrootRoot, "config.json")
	if err := os.WriteFile(configPath, cfgBytes, 0644); err != nil {
		unmountAll()
		return nil, fmt.Errorf("failed to write config.json: %w", err)
	}
	_ = SafeChown(configPath, opts.UID, opts.GID)

	// Cgroup v2 limits
	cpuQuota := opts.VCPUs * 100000
	memMaxBytes := int64(opts.MemoryMB+64) * 1024 * 1024

	args := []string{
		"--id", opts.ID,
		"--exec-file", opts.Firecracker,
		"--uid", fmt.Sprintf("%d", opts.UID),
		"--gid", fmt.Sprintf("%d", opts.GID),
		"--chroot-base-dir", opts.ChrootBaseDir,
		"--cgroup-version", "2",
		"--cgroup", fmt.Sprintf("cpu.max=%d 100000", cpuQuota),
		"--cgroup", fmt.Sprintf("memory.max=%d", memMaxBytes),
		"--cgroup", "pids.max=512",
		"--new-pid-ns",
	}

	if opts.NetNS != "" {
		args = append(args, "--netns", opts.NetNS)
	}

	if opts.Daemonize {
		args = append(args, "--daemonize")
	}

	// Parameters passed to firecracker
	args = append(args, "--", "--config-file", "/config.json", "--api-sock", "/run/fc.sock")

	// Check context cancellation before spawning to avoid race
	if err := ctx.Err(); err != nil {
		unmountAll()
		return nil, err
	}

	// Detach jailer lifecycle from caller request/startup ctx:
	// use exec.Command with Setpgid: true so request completion never kills running microVMs
	cmd := exec.Command(opts.Jailer, args...)
	cmd.SysProcAttr = newSysProcAttr(opts.UseNamespaces, opts.NetNS)

	if err := cmd.Start(); err != nil {
		unmountAll()
		return nil, fmt.Errorf("failed to spawn jailer: %w", err)
	}

	pid := cmd.Process.Pid

	ip := &InstanceProcess{
		ID:           opts.ID,
		ChrootDir:    chrootRoot,
		Cmd:          cmd,
		PID:          pid,
		VsockUDS:     vsockUDSOnHost,
		APISock:      apiSockOnHost,
		MountedPaths: mountedPaths,
		exitCh:       make(chan struct{}),
	}
	go func() {
		if ip.Cmd != nil {
			ip.waitErr = ip.Cmd.Wait()
		}
		close(ip.exitCh)
	}()

	return ip, nil
}
