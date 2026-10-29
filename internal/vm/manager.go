package vm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/firecracker"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/storage"
	"github.com/google/uuid"
)

// Config holds top-level orchestrator configuration
type Config struct {
	FirecrackerBinary string `json:"firecracker_binary"` // /usr/local/bin/firecracker
}

// CreateRequest holds options for creating a VM
type CreateRequest struct {
	Name     string `json:"name"`
	VCPUs    int    `json:"vcpus"`
	MemoryMB int    `json:"memory_mb"`
}

// Manager orchestrates microVM lifecycles
type Manager struct {
	cfg     Config
	store   *db.Store
	netMgr  *network.Manager
	storMgr *storage.Manager
	mu      sync.Mutex
}

// NewManager creates a new VM Manager
func NewManager(cfg Config, store *db.Store, netMgr *network.Manager, storMgr *storage.Manager) *Manager {
	if cfg.FirecrackerBinary == "" {
		cfg.FirecrackerBinary = "/usr/local/bin/firecracker"
	}
	return &Manager{
		cfg:     cfg,
		store:   store,
		netMgr:  netMgr,
		storMgr: storMgr,
	}
}

// CreateVM provisions a new VM metadata, allocates IP/MAC, and initializes persistent disk
func (m *Manager) CreateVM(ctx context.Context, req CreateRequest) (*db.VM, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	uid := uuid.New().String()
	shortID := strings.ReplaceAll(uid, "-", "")[:8]
	vmID := fmt.Sprintf("vm-%s", shortID)

	name := req.Name
	if name == "" {
		name = vmID
	}
	vcpus := req.VCPUs
	if vcpus <= 0 {
		vcpus = 1
	}
	memoryMB := req.MemoryMB
	if memoryMB <= 0 {
		memoryMB = 512
	}

	// Allocate IP & MAC
	usedIPs, err := m.store.GetAllocatedIPs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch used IPs: %w", err)
	}

	ip, mac, err := m.netMgr.AllocateIP(usedIPs)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate IP: %w", err)
	}

	// TAP device name (kernel max 15 chars)
	tapDev := fmt.Sprintf("mftap-%s", shortID)

	// Initialize persistent disk
	diskPath, err := m.storMgr.InitVMDisk(vmID)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize VM disk: %w", err)
	}

	kernelPath := m.storMgr.GetBaseKernelPath()
	rootfsPath := m.storMgr.GetBaseRootfsPath()
	sshKeyPath := m.storMgr.GetSSHKeyPath()

	vm := &db.VM{
		ID:         vmID,
		Name:       name,
		Status:     db.StatusCreated,
		VCPUs:      vcpus,
		MemoryMB:   memoryMB,
		IPAddress:  ip,
		MACAddress: mac,
		TAPDevice:  tapDev,
		RootfsPath: rootfsPath,
		KernelPath: kernelPath,
		DiskPath:   diskPath,
		SSHKeyPath: sshKeyPath,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}

	if err := m.store.SaveVM(ctx, vm); err != nil {
		m.storMgr.CleanupVM(vmID)
		return nil, fmt.Errorf("failed to save VM in DB: %w", err)
	}

	return vm, nil
}

// RunVM starts an existing VM
func (m *Manager) RunVM(ctx context.Context, vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	vm, err := m.store.GetVM(ctx, vmID)
	if err != nil || vm == nil {
		return fmt.Errorf("vm %s not found: %w", vmID, err)
	}

	if vm.Status == db.StatusRunning && isProcessAlive(vm.PID) {
		return nil // Already running
	}

	// 1. Ensure Bridge & TAP
	if err := m.netMgr.EnsureBridge(ctx); err != nil {
		return fmt.Errorf("failed to ensure bridge: %w", err)
	}
	if err := m.netMgr.SetupTAP(ctx, vm.TAPDevice); err != nil {
		return fmt.Errorf("failed to setup TAP device: %w", err)
	}

	// 2. Setup paths
	vmDir := m.storMgr.GetVMDir(vmID)
	sockPath := filepath.Join(vmDir, "firecracker.sock")
	logPath := filepath.Join(vmDir, "firecracker.log")

	// 3. Launch Firecracker
	cmd, err := firecracker.LaunchProcess(m.cfg.FirecrackerBinary, sockPath, vmID, logPath)
	if err != nil {
		return fmt.Errorf("failed to launch firecracker process: %w", err)
	}

	// Wait for socket
	if err := firecracker.WaitForSocket(sockPath, 5*time.Second); err != nil {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return fmt.Errorf("firecracker socket not ready: %w", err)
	}

	// 4. Configure via API
	fcClient := firecracker.NewClient(sockPath)

	// Machine Config
	if err := fcClient.SetMachineConfig(ctx, firecracker.MachineConfig{
		VCPUCount:  vm.VCPUs,
		MemSizeMiB: vm.MemoryMB,
	}); err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("failed to set machine config: %w", err)
	}

	// Boot source
	gwIP := m.netMgr.GetGatewayIP()
	bootArgs := fmt.Sprintf("console=ttyS0 reboot=k panic=1 pci=off ip=%s::%s:255.255.0.0::eth0:off root=/dev/vda rw quiet", vm.IPAddress, gwIP)
	if err := fcClient.SetBootSource(ctx, firecracker.BootSource{
		KernelImagePath: vm.KernelPath,
		BootArgs:        bootArgs,
	}); err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("failed to set boot source: %w", err)
	}

	// Root Drive (Persistent Disk)
	if err := fcClient.AttachDrive(ctx, firecracker.Drive{
		DriveID:      "rootfs",
		PathOnHost:   vm.DiskPath,
		IsRootDevice: true,
		IsReadOnly:   false,
	}); err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("failed to attach root drive: %w", err)
	}

	// Network Interface
	if err := fcClient.AttachNetworkInterface(ctx, firecracker.NetworkInterface{
		IfaceID:     "eth0",
		GuestMAC:    vm.MACAddress,
		HostDevName: vm.TAPDevice,
	}); err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("failed to attach network interface: %w", err)
	}

	// Start Instance
	if err := fcClient.StartInstance(ctx); err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("failed to start instance: %w", err)
	}

	// Update DB record
	vm.Status = db.StatusRunning
	vm.PID = cmd.Process.Pid
	vm.SocketPath = sockPath
	return m.store.SaveVM(ctx, vm)
}

// SaveState pauses the running VM and creates a state + memory snapshot
func (m *Manager) SaveState(ctx context.Context, vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	vm, err := m.store.GetVM(ctx, vmID)
	if err != nil || vm == nil {
		return fmt.Errorf("vm %s not found: %w", vmID, err)
	}

	if vm.Status != db.StatusRunning || !isProcessAlive(vm.PID) {
		return fmt.Errorf("vm %s is not running (status: %s)", vmID, vm.Status)
	}

	fcClient := firecracker.NewClient(vm.SocketPath)

	// 1. Pause VM
	if err := fcClient.Pause(ctx); err != nil {
		return fmt.Errorf("failed to pause vm: %w", err)
	}

	// 2. Snapshot
	snapPath, memPath := m.storMgr.GetSnapshotPaths(vmID)
	os.Remove(snapPath)
	os.Remove(memPath)

	if err := fcClient.CreateSnapshot(ctx, snapPath, memPath); err != nil {
		fcClient.Resume(ctx)
		return fmt.Errorf("failed to create snapshot: %w", err)
	}

	// 3. Stop process
	if vm.PID > 0 {
		syscall.Kill(vm.PID, syscall.SIGKILL)
	}
	os.Remove(vm.SocketPath)

	vm.Status = db.StatusPaused
	vm.SnapshotPath = snapPath
	vm.MemFilePath = memPath
	vm.PID = 0

	return m.store.SaveVM(ctx, vm)
}

// ResumeVM restores a paused/snapshotted VM
func (m *Manager) ResumeVM(ctx context.Context, vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	vm, err := m.store.GetVM(ctx, vmID)
	if err != nil || vm == nil {
		return fmt.Errorf("vm %s not found: %w", vmID, err)
	}

	if vm.SnapshotPath == "" || vm.MemFilePath == "" {
		return fmt.Errorf("vm %s has no snapshot state saved", vmID)
	}
	if _, err := os.Stat(vm.SnapshotPath); err != nil {
		return fmt.Errorf("snapshot file missing at %s: %w", vm.SnapshotPath, err)
	}
	if _, err := os.Stat(vm.MemFilePath); err != nil {
		return fmt.Errorf("memory dump missing at %s: %w", vm.MemFilePath, err)
	}

	// 1. Ensure Bridge & TAP
	if err := m.netMgr.EnsureBridge(ctx); err != nil {
		return fmt.Errorf("failed to ensure bridge: %w", err)
	}
	if err := m.netMgr.SetupTAP(ctx, vm.TAPDevice); err != nil {
		return fmt.Errorf("failed to setup TAP device: %w", err)
	}

	// 2. Launch new Firecracker process
	vmDir := m.storMgr.GetVMDir(vmID)
	sockPath := filepath.Join(vmDir, "firecracker.sock")
	logPath := filepath.Join(vmDir, "firecracker.log")

	cmd, err := firecracker.LaunchProcess(m.cfg.FirecrackerBinary, sockPath, vmID, logPath)
	if err != nil {
		return fmt.Errorf("failed to launch firecracker process: %w", err)
	}

	if err := firecracker.WaitForSocket(sockPath, 5*time.Second); err != nil {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return fmt.Errorf("firecracker socket not ready: %w", err)
	}

	// 3. Load Snapshot with resume=true
	fcClient := firecracker.NewClient(sockPath)
	if err := fcClient.LoadSnapshot(ctx, vm.SnapshotPath, vm.MemFilePath, true); err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("failed to load snapshot: %w", err)
	}

	vm.Status = db.StatusRunning
	vm.PID = cmd.Process.Pid
	vm.SocketPath = sockPath
	return m.store.SaveVM(ctx, vm)
}

// StopVM gracefully shuts down a running VM, flushing caches, and cleaning runtime resources
func (m *Manager) StopVM(ctx context.Context, vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	vm, err := m.store.GetVM(ctx, vmID)
	if err != nil || vm == nil {
		return fmt.Errorf("vm %s not found: %w", vmID, err)
	}

	if vm.Status == db.StatusStopped && !isProcessAlive(vm.PID) {
		return nil
	}

	if vm.PID > 0 && isProcessAlive(vm.PID) {
		// 1. Try graceful sync over SSH if accessible
		if vm.IPAddress != "" && vm.SSHKeyPath != "" {
			syncCtx, syncCancel := context.WithTimeout(ctx, 2*time.Second)
			sshCmd := exec.CommandContext(syncCtx, "ssh",
				"-i", vm.SSHKeyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ConnectTimeout=1",
				fmt.Sprintf("root@%s", vm.IPAddress),
				"sync; poweroff",
			)
			_ = sshCmd.Run()
			syncCancel()
		}

		// Wait briefly for guest poweroff
		for i := 0; i < 5; i++ {
			if !isProcessAlive(vm.PID) {
				break
			}
			time.Sleep(150 * time.Millisecond)
		}

		// 2. If still alive, attempt SendCtrlAltDel
		if isProcessAlive(vm.PID) && vm.SocketPath != "" {
			fcClient := firecracker.NewClient(vm.SocketPath)
			_ = fcClient.SendCtrlAltDel(ctx)
			time.Sleep(300 * time.Millisecond)
		}

		// 3. If still alive, SIGTERM then SIGKILL
		if isProcessAlive(vm.PID) {
			syscall.Kill(vm.PID, syscall.SIGTERM)
			time.Sleep(200 * time.Millisecond)
			if isProcessAlive(vm.PID) {
				syscall.Kill(vm.PID, syscall.SIGKILL)
			}
		}
	}

	// Clean up TAP interface and socket
	_ = m.netMgr.RemoveTAP(ctx, vm.TAPDevice)
	if vm.SocketPath != "" {
		_ = os.Remove(vm.SocketPath)
	}

	vm.Status = db.StatusStopped
	vm.PID = 0
	return m.store.SaveVM(ctx, vm)
}

// DeleteVM stops the VM, cleans up disks, and deletes the record from DB
func (m *Manager) DeleteVM(ctx context.Context, vmID string) error {
	_ = m.StopVM(ctx, vmID)

	m.mu.Lock()
	defer m.mu.Unlock()

	_ = m.storMgr.CleanupVM(vmID)
	return m.store.DeleteVM(ctx, vmID)
}

// GetVM retrieves VM details
func (m *Manager) GetVM(ctx context.Context, vmID string) (*db.VM, error) {
	return m.store.GetVM(ctx, vmID)
}

// ListVMs lists all VMs
func (m *Manager) ListVMs(ctx context.Context) ([]*db.VM, error) {
	return m.store.ListVMs(ctx)
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil
}
