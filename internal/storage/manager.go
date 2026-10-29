package storage

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Config defines storage directory configurations
type Config struct {
	BaseImagesDir string `json:"base_images_dir"` // e.g. /var/lib/microforge/images
	VMsDir        string `json:"vms_dir"`         // e.g. /var/lib/microforge/vms
	DefaultKernel string `json:"default_kernel"`  // vmlinux
	DefaultRootfs string `json:"default_rootfs"`  // alpine-3.20.ext4
	SSHKeyPath    string `json:"ssh_key_path"`    // /var/lib/microforge/images/ssh/id_ed25519
}

// Manager manages VM disk persistence and snapshots
type Manager struct {
	cfg Config
}

// NewManager creates a storage manager
func NewManager(cfg Config) (*Manager, error) {
	if cfg.BaseImagesDir == "" {
		cfg.BaseImagesDir = "/var/lib/microforge/images"
	}
	if cfg.VMsDir == "" {
		cfg.VMsDir = "/var/lib/microforge/vms"
	}
	if cfg.DefaultKernel == "" {
		cfg.DefaultKernel = "vmlinux"
	}
	if cfg.DefaultRootfs == "" {
		cfg.DefaultRootfs = "alpine-3.20.ext4"
	}
	if cfg.SSHKeyPath == "" {
		cfg.SSHKeyPath = filepath.Join(cfg.BaseImagesDir, "ssh", "id_ed25519")
	}

	if err := os.MkdirAll(cfg.BaseImagesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base images dir: %w", err)
	}
	if err := os.MkdirAll(cfg.VMsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create vms dir: %w", err)
	}

	return &Manager{cfg: cfg}, nil
}

// GetVMDir returns the dedicated directory path for a VM
func (m *Manager) GetVMDir(vmID string) string {
	return filepath.Join(m.cfg.VMsDir, vmID)
}

// GetBaseKernelPath returns the path to the base kernel
func (m *Manager) GetBaseKernelPath() string {
	return filepath.Join(m.cfg.BaseImagesDir, m.cfg.DefaultKernel)
}

// GetBaseRootfsPath returns the path to the base rootfs image
func (m *Manager) GetBaseRootfsPath() string {
	return filepath.Join(m.cfg.BaseImagesDir, m.cfg.DefaultRootfs)
}

// GetSSHKeyPath returns the path to the SSH private key
func (m *Manager) GetSSHKeyPath() string {
	return m.cfg.SSHKeyPath
}

// InitVMDisk creates the VM directory and creates a persistent copy-on-write / sparse disk for the VM
func (m *Manager) InitVMDisk(vmID string) (diskPath string, err error) {
	vmDir := m.GetVMDir(vmID)
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create vm dir: %w", err)
	}

	diskPath = filepath.Join(vmDir, "rootfs.ext4")
	baseRootfs := m.GetBaseRootfsPath()

	// Check if already initialized
	if _, err := os.Stat(diskPath); err == nil {
		return diskPath, nil
	}

	// Verify base image exists
	if _, err := os.Stat(baseRootfs); err != nil {
		return "", fmt.Errorf("base rootfs not found at %s: %w", baseRootfs, err)
	}

	// Copy using cp --sparse=always if available, otherwise sparse file copy
	cmd := exec.Command("cp", "--sparse=always", baseRootfs, diskPath)
	if err := cmd.Run(); err != nil {
		// Fallback to Go copy
		if err := copySparseFile(baseRootfs, diskPath); err != nil {
			return "", fmt.Errorf("failed to copy base rootfs: %w", err)
		}
	}

	return diskPath, nil
}

// GetSnapshotPaths returns the paths where VM state and memory dumps will be saved
func (m *Manager) GetSnapshotPaths(vmID string) (snapshotPath, memPath string) {
	vmDir := m.GetVMDir(vmID)
	return filepath.Join(vmDir, "snapshot.state"), filepath.Join(vmDir, "snapshot.mem")
}

// CleanupVM removes the VM workspace directory and all disks/snapshots
func (m *Manager) CleanupVM(vmID string) error {
	vmDir := m.GetVMDir(vmID)
	return os.RemoveAll(vmDir)
}

func copySparseFile(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()

	_, err = io.Copy(destination, source)
	return err
}
