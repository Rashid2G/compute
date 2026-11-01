package firecracker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

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
}

type InstanceProcess struct {
	ID        string
	ChrootDir string
	Cmd       *exec.Cmd
	PID       int
	VsockUDS  string
	APISock   string
}

// LaunchJailer initializes the chroot jail, prepares files, and launches Firecracker inside Jailer.
func LaunchJailer(ctx context.Context, opts JailerOptions) (*InstanceProcess, error) {
	if opts.Firecracker == "" {
		opts.Firecracker = "/usr/local/bin/firecracker"
	}
	if opts.Jailer == "" {
		opts.Jailer = "/usr/local/bin/jailer"
	}
	if opts.ChrootBaseDir == "" {
		opts.ChrootBaseDir = "/tmp/compute/jail"
	}
	if opts.UID <= 0 {
		opts.UID = 100001
	}
	if opts.GID <= 0 {
		opts.GID = opts.UID
	}

	execName := filepath.Base(opts.Firecracker)
	chrootRoot := filepath.Join(opts.ChrootBaseDir, execName, opts.ID, "root")

	if err := os.MkdirAll(chrootRoot, 0755); err != nil {
		return nil, fmt.Errorf("failed to create chroot directory %s: %w", chrootRoot, err)
	}

	// Hardlink or copy kernel into chroot
	targetKernel := filepath.Join(chrootRoot, "vmlinux")
	_ = os.Remove(targetKernel)
	if err := os.Link(opts.KernelSrcPath, targetKernel); err != nil {
		// Fallback to copy if cross-device link
		if err := copyFile(opts.KernelSrcPath, targetKernel); err != nil {
			return nil, fmt.Errorf("failed to link/copy kernel: %w", err)
		}
	}
	_ = os.Chown(targetKernel, opts.UID, opts.GID)

	// Hardlink or copy rootfs into chroot
	targetRootfs := filepath.Join(chrootRoot, "rootfs.ext4")
	_ = os.Remove(targetRootfs)
	if err := os.Link(opts.RootfsSrcPath, targetRootfs); err != nil {
		if err := copyFile(opts.RootfsSrcPath, targetRootfs); err != nil {
			return nil, fmt.Errorf("failed to link/copy rootfs: %w", err)
		}
	}
	_ = os.Chown(targetRootfs, opts.UID, opts.GID)

	// UDS socket paths inside and outside chroot
	vsockUDSInChroot := "/v.sock"
	vsockUDSOnHost := filepath.Join(chrootRoot, "v.sock")
	apiSockOnHost := filepath.Join(chrootRoot, "run", "fc.sock")
	_ = os.MkdirAll(filepath.Join(chrootRoot, "run"), 0755)
	_ = os.Chown(filepath.Join(chrootRoot, "run"), opts.UID, opts.GID)

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
		return nil, fmt.Errorf("failed to build vm config: %w", err)
	}

	cfgBytes, err := cfg.ToJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal vm config: %w", err)
	}

	configPath := filepath.Join(chrootRoot, "config.json")
	if err := os.WriteFile(configPath, cfgBytes, 0644); err != nil {
		return nil, fmt.Errorf("failed to write config.json: %w", err)
	}
	_ = os.Chown(configPath, opts.UID, opts.GID)

	// Cgroup v2 limits
	cpuQuota := opts.VCPUs*100000 + 25000
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
		"--cgroup", "pids.max=128",
		"--new-pid-ns",
	}

	if opts.Daemonize {
		args = append(args, "--daemonize")
	}

	// Parameters passed to firecracker
	args = append(args, "--", "--config-file", "/config.json", "--api-sock", "/run/fc.sock")

	cmd := exec.CommandContext(ctx, opts.Jailer, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to spawn jailer: %w", err)
	}

	pid := cmd.Process.Pid

	return &InstanceProcess{
		ID:        opts.ID,
		ChrootDir: chrootRoot,
		Cmd:       cmd,
		PID:       pid,
		VsockUDS:  vsockUDSOnHost,
		APISock:   apiSockOnHost,
	}, nil
}

// Stop terminates the microVM instance gracefully or forcefully.
func (ip *InstanceProcess) Stop(timeout time.Duration) error {
	if ip.Cmd == nil || ip.Cmd.Process == nil {
		return nil
	}

	// Try SIGTERM first
	_ = ip.Cmd.Process.Signal(syscall.SIGTERM)

	done := make(chan error, 1)
	go func() {
		done <- ip.Cmd.Wait()
	}()

	select {
	case <-time.After(timeout):
		// Force kill
		_ = ip.Cmd.Process.Kill()
		return fmt.Errorf("process did not terminate within %v, killed with SIGKILL", timeout)
	case err := <-done:
		return err
	}
}

// Cleanup removes the chroot jail directory.
func (ip *InstanceProcess) Cleanup() error {
	return os.RemoveAll(filepath.Dir(ip.ChrootDir))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = out.ReadFrom(in)
	return err
}
