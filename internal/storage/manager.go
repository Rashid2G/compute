package storage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// ImageProfile describes an available OS base image
type ImageProfile struct {
	Name        string `json:"name"`
	Kernel      string `json:"kernel"`
	Rootfs      string `json:"rootfs"`
	Description string `json:"description,omitempty"`
}

// Config defines storage directory configurations
type Config struct {
	BaseImagesDir   string         `json:"base_images_dir"` // e.g. /var/lib/compute/images
	TemplatesDir    string         `json:"templates_dir"`   // e.g. /var/lib/compute/templates
	VMsDir          string         `json:"vms_dir"`         // e.g. /var/lib/compute/vms
	DefaultKernel   string         `json:"default_kernel"`  // vmlinux
	DefaultRootfs   string         `json:"default_rootfs"`  // ubuntu-24.04.ext4
	SSHKeyPath      string         `json:"ssh_key_path"`    // /var/lib/compute/images/ssh/id_ed25519
	AvailableImages []ImageProfile `json:"available_images,omitempty"`
}

// Manager manages VM disk persistence, image resolution, disk space checks, and snapshots
type Manager struct {
	cfg Config
}

// NewManager creates a storage manager
func NewManager(cfg Config) (*Manager, error) {
	if cfg.BaseImagesDir == "" {
		cfg.BaseImagesDir = "/var/lib/compute/images"
	}
	if cfg.TemplatesDir == "" {
		cfg.TemplatesDir = filepath.Join(filepath.Dir(cfg.BaseImagesDir), "templates")
	}
	if cfg.VMsDir == "" {
		cfg.VMsDir = "/var/lib/compute/vms"
	}
	if cfg.DefaultKernel == "" {
		cfg.DefaultKernel = "vmlinux"
	}
	if cfg.DefaultRootfs == "" {
		cfg.DefaultRootfs = "ubuntu-24.04.ext4"
	}
	if cfg.SSHKeyPath == "" {
		cfg.SSHKeyPath = filepath.Join(cfg.BaseImagesDir, "ssh", "id_ed25519")
	}

	if err := os.MkdirAll(cfg.BaseImagesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base images dir: %w", err)
	}
	if err := os.MkdirAll(cfg.TemplatesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create templates dir: %w", err)
	}
	if err := os.MkdirAll(cfg.VMsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create vms dir: %w", err)
	}

	return &Manager{cfg: cfg}, nil
}

// GetConfig returns the storage configuration
func (m *Manager) GetConfig() Config {
	return m.cfg
}

// GetVMDir returns the dedicated directory path for a VM
func (m *Manager) GetVMDir(vmID string) string {
	return filepath.Join(m.cfg.VMsDir, vmID)
}

// GetTemplatesDir returns the templates directory
func (m *Manager) GetTemplatesDir() string {
	return m.cfg.TemplatesDir
}

// GetTemplateDiskPath returns the path to a template disk
func (m *Manager) GetTemplateDiskPath(templateID string) string {
	return filepath.Join(m.cfg.TemplatesDir, templateID+".ext4")
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

// ResolveImage resolves a requested image name to its corresponding kernel and rootfs absolute paths.
// Compute supports Ubuntu 24 ("ubuntu-24", "ubuntu-24.04", "ubuntu", "default", or empty),
// pre-configured available images, custom created images in BaseImagesDir, or saved templates in TemplatesDir.
func (m *Manager) ResolveImage(imageName string) (kernelPath, rootfsPath string, err error) {
	cleanName := strings.ToLower(strings.TrimSpace(imageName))
	defaultRootfsName := strings.TrimSuffix(m.cfg.DefaultRootfs, ".ext4")

	if cleanName == "" || cleanName == "default" || cleanName == "ubuntu" || cleanName == "ubuntu-24" || cleanName == "ubuntu-24.04" || cleanName == "ubuntu-2404" || cleanName == strings.ToLower(m.cfg.DefaultRootfs) || cleanName == strings.ToLower(defaultRootfsName) {
		k := m.GetBaseKernelPath()
		r := m.GetBaseRootfsPath()
		return k, r, nil
	}

	// Search in configured available images (in case test suite provided explicit profiles)
	for _, img := range m.cfg.AvailableImages {
		if strings.EqualFold(img.Name, imageName) {
			k := img.Kernel
			if !filepath.IsAbs(k) {
				k = filepath.Join(m.cfg.BaseImagesDir, k)
			}
			r := img.Rootfs
			if !filepath.IsAbs(r) {
				r = filepath.Join(m.cfg.BaseImagesDir, r)
			}
			return k, r, nil
		}
	}

	// Check if this matches a direct existing file in BaseImagesDir
	candidates := []string{
		imageName,
		imageName + ".ext4",
		imageName + ".img",
	}
	for _, c := range candidates {
		fullPath := filepath.Join(m.cfg.BaseImagesDir, c)
		if _, statErr := os.Stat(fullPath); statErr == nil {
			return m.GetBaseKernelPath(), fullPath, nil
		}
	}

	// Check if this matches a file in TemplatesDir
	if m.cfg.TemplatesDir != "" {
		for _, c := range candidates {
			fullPath := filepath.Join(m.cfg.TemplatesDir, c)
			if _, statErr := os.Stat(fullPath); statErr == nil {
				return m.GetBaseKernelPath(), fullPath, nil
			}
		}
	}

	return "", "", fmt.Errorf("unsupported image '%s': only 'ubuntu-24' (Ubuntu 24.04 LTS), custom images, or saved templates are supported", imageName)
}

// InitVMDisk creates the VM directory and creates a persistent copy-on-write / sparse disk for the VM using default rootfs
func (m *Manager) InitVMDisk(vmID string) (diskPath string, err error) {
	return m.InitVMDiskFromImageWithResize(vmID, m.GetBaseRootfsPath(), 0)
}

// InitVMDiskFromImage initializes a persistent disk for the VM from a specific base rootfs path
func (m *Manager) InitVMDiskFromImage(vmID string, baseRootfs string) (diskPath string, err error) {
	return m.InitVMDiskFromImageWithResize(vmID, baseRootfs, 0)
}

// InitVMDiskFromImageWithResize initializes a persistent disk for the VM from a source rootfs path
// and resizes it sparsely to the target diskMB (e.g. 50GB or 100GB).
func (m *Manager) InitVMDiskFromImageWithResize(vmID string, baseRootfs string, diskMB int) (diskPath string, err error) {
	if baseRootfs == "" {
		baseRootfs = m.GetBaseRootfsPath()
	}

	vmDir := m.GetVMDir(vmID)
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create vm dir: %w", err)
	}

	diskPath = filepath.Join(vmDir, "rootfs.ext4")

	// Check if already initialized
	if _, err := os.Stat(diskPath); err == nil {
		return diskPath, nil
	}

	// Verify base image exists
	if _, err := os.Stat(baseRootfs); err != nil {
		return "", fmt.Errorf("base rootfs not found at %s: %w", baseRootfs, err)
	}

	// Fast Copy-on-Write (Reflink / FICLONE) with fallback to sparse copy
	if err := CloneFileCOW(baseRootfs, diskPath); err != nil {
		return "", fmt.Errorf("failed to copy base rootfs: %w", err)
	}

	// Resize sparse file and ext4 filesystem if target size is specified and differs.
	if diskMB > 0 {
		targetBytes := int64(diskMB) * 1024 * 1024
		if info, statErr := os.Stat(diskPath); statErr != nil || info.Size() != targetBytes {
			_ = resizeDiskSparse(diskPath, diskMB)
		}
	}

	return diskPath, nil
}

// CloneVMDisk clones the disk of an existing VM into a new VM's directory as a sparse file
// and optionally resizes it to the target diskMB.
func (m *Manager) CloneVMDisk(srcVMID, dstVMID string, diskMB int) (string, error) {
	srcDiskPath := filepath.Join(m.GetVMDir(srcVMID), "rootfs.ext4")
	if _, err := os.Stat(srcDiskPath); err != nil {
		return "", fmt.Errorf("source VM '%s' disk not found at %s: %w", srcVMID, srcDiskPath, err)
	}

	return m.InitVMDiskFromImageWithResize(dstVMID, srcDiskPath, diskMB)
}

// CreateTemplateDiskFromPath clones a source disk file into a new template image in TemplatesDir
func (m *Manager) CreateTemplateDiskFromPath(srcDiskPath, templateID string) (string, error) {
	if _, err := os.Stat(srcDiskPath); err != nil {
		return "", fmt.Errorf("source disk not found at %s: %w", srcDiskPath, err)
	}

	if err := os.MkdirAll(m.cfg.TemplatesDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create templates directory: %w", err)
	}

	dstPath := filepath.Join(m.cfg.TemplatesDir, templateID+".ext4")
	if err := CloneFileCOW(srcDiskPath, dstPath); err != nil {
		return "", fmt.Errorf("failed to clone disk for template: %w", err)
	}

	return dstPath, nil
}

// CreateTemplateDiskFromVM clones the disk of an existing VM into a new template image in TemplatesDir
func (m *Manager) CreateTemplateDiskFromVM(srcVMID, templateID string) (string, error) {
	srcDiskPath := filepath.Join(m.GetVMDir(srcVMID), "rootfs.ext4")
	return m.CreateTemplateDiskFromPath(srcDiskPath, templateID)
}

// DeleteTemplateFile deletes the template disk image file
func (m *Manager) DeleteTemplateFile(templateID string) error {
	_ = os.Remove(filepath.Join(m.cfg.TemplatesDir, templateID+".ext4"))
	_ = os.Remove(filepath.Join(m.cfg.TemplatesDir, templateID))
	return nil
}

// DeleteImageFile deletes an image file from BaseImagesDir
func (m *Manager) DeleteImageFile(imageName string) error {
	_ = os.Remove(filepath.Join(m.cfg.BaseImagesDir, imageName+".ext4"))
	_ = os.Remove(filepath.Join(m.cfg.BaseImagesDir, imageName))
	return nil
}

// isExt4Image checks for the ext2/3/4 superblock magic (0xEF53 at offset 1080).
func isExt4Image(diskPath string) bool {
	f, err := os.Open(diskPath)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [2]byte
	if _, err := f.ReadAt(magic[:], 1024+0x38); err != nil {
		return false
	}
	return magic[0] == 0x53 && magic[1] == 0xef
}

func resizeDiskSparse(diskPath string, diskMB int) error {
	targetBytes := int64(diskMB) * 1024 * 1024
	if err := os.Truncate(diskPath, targetBytes); err != nil {
		return err
	}
	if !isExt4Image(diskPath) {
		return nil
	}
	// e2fsck check and resize2fs on ext4
	_ = exec.Command(fsTool("e2fsck"), "-pf", diskPath).Run()
	_ = exec.Command(fsTool("resize2fs"), "-f", diskPath).Run()
	return nil
}

// CheckHostDiskSpace checks if the host filesystem hosting VMsDir has sufficient headroom.
// If requiredMB > 0, it verifies that at least requiredMB is available.
// If requiredMB <= 0, it defaults to requiring 512 MB operational headroom.
func (m *Manager) CheckHostDiskSpace(requiredMB int64) error {
	freeMB, err := m.GetAvailableDiskSpaceMB()
	if err != nil {
		return nil // Cannot query filesystem stat, do not block creation
	}

	neededMB := int64(512)
	if requiredMB > 0 {
		neededMB = requiredMB
	}

	if freeMB < neededMB {
		return fmt.Errorf("insufficient host disk space (%d MB available, %d MB minimum headroom required)", freeMB, neededMB)
	}
	return nil
}

// GetAvailableDiskSpaceMB queries the filesystem for free available space in MB
func (m *Manager) GetAvailableDiskSpaceMB() (int64, error) {
	checkPath := m.cfg.VMsDir
	var stat syscall.Statfs_t
	if err := syscall.Statfs(checkPath, &stat); err != nil {
		// If VMsDir doesn't exist yet, check its parent
		parent := filepath.Dir(checkPath)
		if err2 := syscall.Statfs(parent, &stat); err2 != nil {
			return 0, err2
		}
	}

	freeBytes := stat.Bavail * uint64(stat.Bsize)
	freeMB := int64(freeBytes / (1024 * 1024))
	return freeMB, nil
}

// GetTotalVMsDiskUsageMB calculates the total disk space currently occupied by VM disks in VMsDir
func (m *Manager) GetTotalVMsDiskUsageMB() (int64, error) {
	entries, err := os.ReadDir(m.cfg.VMsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	var totalBytes int64
	for _, entry := range entries {
		if entry.IsDir() {
			vmDirPath := filepath.Join(m.cfg.VMsDir, entry.Name())
			_ = filepath.Walk(vmDirPath, func(_ string, info os.FileInfo, walkErr error) error {
				if walkErr == nil && info != nil && !info.IsDir() {
					totalBytes += info.Size()
				}
				return nil
			})
		}
	}

	return totalBytes / (1024 * 1024), nil
}

// GetVolumesDir returns the directory that holds attachable data volume images.
func (m *Manager) GetVolumesDir() string {
	return filepath.Join(filepath.Dir(m.cfg.VMsDir), "volumes")
}

func volumeFileName(id, filesystem string) string {
	ext := "ext4"
	if strings.EqualFold(filesystem, "xfs") {
		ext = "xfs"
	}
	return filepath.Base(id) + "." + ext
}

// fsTool resolves a filesystem utility, which commonly lives in /sbin rather
// than in the daemon's inherited PATH.
func fsTool(name string) string {
	for _, candidate := range []string{name, "/sbin/" + name, "/usr/sbin/" + name} {
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate
		}
	}
	return name
}

// CreateDataVolume allocates a sparse, formatted volume image for a sandbox to
// attach as a secondary block device.
func (m *Manager) CreateDataVolume(id string, sizeGB int, filesystem string) (string, error) {
	if sizeGB <= 0 {
		return "", fmt.Errorf("volume size must be positive")
	}
	dir := m.GetVolumesDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create volumes dir: %w", err)
	}
	path := filepath.Join(dir, volumeFileName(id, filesystem))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	if err := f.Truncate(int64(sizeGB) * 1024 * 1024 * 1024); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if strings.EqualFold(filesystem, "xfs") {
		_, err = exec.Command(fsTool("mkfs.xfs"), "-f", "-q", path).CombinedOutput()
	} else {
		_, err = exec.Command(fsTool("mkfs.ext4"), "-F", "-q", path).CombinedOutput()
	}
	if err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("format volume: %w", err)
	}
	return path, nil
}

// ExpandDataVolume grows a sparse volume image and resizes its filesystem.
func (m *Manager) ExpandDataVolume(path string, newSizeGB int, filesystem string) error {
	if path == "" {
		return fmt.Errorf("volume has no backing image")
	}
	if newSizeGB <= 0 {
		return fmt.Errorf("new size must be positive")
	}
	if strings.EqualFold(filesystem, "xfs") {
		// Offline XFS growth is not supported; xfs_growfs needs the fs mounted.
		return fmt.Errorf("xfs volumes cannot be expanded offline; recreate or expand from inside the guest")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	oldSize := info.Size()
	if err := os.Truncate(path, int64(newSizeGB)*1024*1024*1024); err != nil {
		return err
	}
	// A sandbox that was just powered off can leave the journal dirty; repair
	// first so resize2fs accepts the filesystem. e2fsck exit 1 means "errors
	// corrected", which is a successful repair for our purposes.
	fsckCmd := exec.Command(fsTool("e2fsck"), "-f", "-y", path)
	if out, err := fsckCmd.CombinedOutput(); err != nil {
		corrected := false
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			corrected = true
		}
		if !corrected {
			_ = os.Truncate(path, oldSize)
			return fmt.Errorf("e2fsck: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}
	if out, err := exec.Command(fsTool("resize2fs"), path).CombinedOutput(); err != nil {
		_ = os.Truncate(path, oldSize)
		return fmt.Errorf("resize2fs: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// SnapshotDataVolume clones a volume image with a reflink when the filesystem
// supports it and falls back to a sparse copy otherwise.
func (m *Manager) SnapshotDataVolume(path string, index int) (string, error) {
	if path == "" {
		return "", fmt.Errorf("volume has no backing image")
	}
	dest := fmt.Sprintf("%s.snap-%d", path, index)
	_ = os.Remove(dest)
	if out, err := exec.Command("cp", "--reflink=auto", "--sparse=always", path, dest).CombinedOutput(); err != nil {
		return "", fmt.Errorf("snapshot volume: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return dest, nil
}

// DeleteDataVolume removes a volume image and any snapshots taken from it.
func (m *Manager) DeleteDataVolume(path string) error {
	if path == "" {
		return nil
	}
	if matches, err := filepath.Glob(path + ".snap-*"); err == nil {
		for _, snapshot := range matches {
			_ = os.Remove(snapshot)
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
	return CloneFileCOW(src, dst)
}

// EmbedSSHKey injects an SSH public key into the target ext4 disk image and
// disables password authentication. It writes to /root/.ssh/authorized_keys and
// /home/compute/.ssh/authorized_keys.
func (m *Manager) EmbedSSHKey(diskPath string, pubKey string) error {
	return m.ConfigureSSHAccess(diskPath, SSHAccess{PublicKey: pubKey})
}
