package vm

import (
	"bytes"
	"context"
	"fmt"
	"net"
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

type Config struct {
	FirecrackerBinary string
}

type Manager struct {
	cfg     Config
	store   *db.Store
	netMgr  *network.Manager
	storMgr *storage.Manager
	mu      sync.Mutex
}

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

// GetNetworkManager returns the underlying network manager
func (m *Manager) GetNetworkManager() *network.Manager {
	return m.netMgr
}

// GetStore returns the underlying database store
func (m *Manager) GetStore() *db.Store {
	return m.store
}

type CreateRequest struct {
	Name       string `json:"name"`
	VCPUs      int    `json:"vcpus"`
	MemoryMB   int    `json:"memory_mb"`
	IPAddress  string `json:"ip_address,omitempty"`
	MACAddress string `json:"mac_address,omitempty"`
}

type ExecRequest struct {
	Command string `json:"command"`
	User    string `json:"user,omitempty"`
	Timeout int    `json:"timeout_seconds,omitempty"`
}

type ExecResponse struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
}

type NetworkInspectResult struct {
	VMID         string `json:"vm_id"`
	VMName       string `json:"vm_name"`
	Status       string `json:"status"`
	IPAddress    string `json:"ip_address"`
	MACAddress   string `json:"mac_address"`
	TAPDevice    string `json:"tap_device"`
	BridgeName   string `json:"bridge_name"`
	Gateway      string `json:"gateway"`
	Reachable    bool   `json:"reachable"`
	Connectivity string `json:"connectivity"`
}

type StatsResult struct {
	TotalVMs       int `json:"total_vms"`
	RunningVMs     int `json:"running_vms"`
	SnapshottedVMs int `json:"snapshotted_vms"`
	StoppedVMs     int `json:"stopped_vms"`
	CreatedVMs     int `json:"created_vms"`
	TotalVCPUs     int `json:"total_vcpus"`
	TotalMemoryMB  int `json:"total_memory_mb"`
	AllocatedIPs   int `json:"allocated_ips"`
}

// CreateVM reserves networking, initializes storage, and registers a created VM
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

	// Fetch current IP allocations
	usedIPs, err := m.store.GetAllocatedIPs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch used IPs: %w", err)
	}

	var ip, mac string
	if req.IPAddress != "" {
		if err := m.netMgr.ValidateIP(req.IPAddress, usedIPs); err != nil {
			return nil, fmt.Errorf("invalid requested IP: %w", err)
		}
		ip = req.IPAddress
		if req.MACAddress != "" {
			mac = req.MACAddress
		} else {
			mac = network.GenerateMAC(net.ParseIP(ip))
		}
	} else {
		ip, mac, err = m.netMgr.AllocateIP(usedIPs)
		if err != nil {
			return nil, fmt.Errorf("failed to allocate IP: %w", err)
		}
		if req.MACAddress != "" {
			mac = req.MACAddress
		}
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
	if err := m.netMgr.EnsureNetwork(ctx); err != nil {
		return fmt.Errorf("failed to ensure network: %w", err)
	}
	if err := m.netMgr.SetupTAP(ctx, vm.TAPDevice); err != nil {
		return fmt.Errorf("failed to setup TAP device: %w", err)
	}

	// 2. Setup paths
	vmDir := m.storMgr.GetVMDir(vmID)
	sockPath := filepath.Join(vmDir, "firecracker.sock")
	logPath := filepath.Join(vmDir, "firecracker.log")

	// Ensure cleanup on failure rollback
	var success bool
	var cmd *exec.Cmd
	defer func() {
		if !success {
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = m.netMgr.RemoveTAP(ctx, vm.TAPDevice)
			_ = os.Remove(sockPath)
		}
	}()

	// 3. Launch Firecracker
	cmd, err = firecracker.LaunchProcess(m.cfg.FirecrackerBinary, sockPath, vmID, logPath)
	if err != nil {
		return fmt.Errorf("failed to launch firecracker process: %w", err)
	}

	// Wait for socket
	if err := firecracker.WaitForSocket(sockPath, 5*time.Second); err != nil {
		return fmt.Errorf("firecracker socket not ready: %w", err)
	}

	// 4. Configure via API
	fcClient := firecracker.NewClient(sockPath)

	// Machine Config
	if err := fcClient.SetMachineConfig(ctx, firecracker.MachineConfig{
		VCPUCount:  vm.VCPUs,
		MemSizeMiB: vm.MemoryMB,
	}); err != nil {
		return fmt.Errorf("failed to set machine config: %w", err)
	}

	// Boot source with exact netmask and gateway
	gwIP := m.netMgr.GetGatewayIP()
	netmask := m.netMgr.GetNetmask()
	bootArgs := fmt.Sprintf("console=ttyS0 reboot=k panic=1 pci=off ip=%s::%s:%s:%s:eth0:off root=/dev/vda rw quiet", vm.IPAddress, gwIP, netmask, vm.ID)
	if err := fcClient.SetBootSource(ctx, firecracker.BootSource{
		KernelImagePath: vm.KernelPath,
		BootArgs:        bootArgs,
	}); err != nil {
		return fmt.Errorf("failed to set boot source: %w", err)
	}

	// Root Drive (Persistent Disk)
	if err := fcClient.AttachDrive(ctx, firecracker.Drive{
		DriveID:      "rootfs",
		PathOnHost:   vm.DiskPath,
		IsRootDevice: true,
		IsReadOnly:   false,
	}); err != nil {
		return fmt.Errorf("failed to attach root drive: %w", err)
	}

	// Network Interface
	if err := fcClient.AttachNetworkInterface(ctx, firecracker.NetworkInterface{
		IfaceID:     "eth0",
		GuestMAC:    vm.MACAddress,
		HostDevName: vm.TAPDevice,
	}); err != nil {
		return fmt.Errorf("failed to attach network interface: %w", err)
	}

	// Start Instance
	if err := fcClient.StartInstance(ctx); err != nil {
		return fmt.Errorf("failed to start instance: %w", err)
	}

	// Update DB record
	success = true
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
	_ = m.netMgr.RemoveTAP(ctx, vm.TAPDevice)

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

	// 1. Ensure Network & TAP
	if err := m.netMgr.EnsureNetwork(ctx); err != nil {
		return fmt.Errorf("failed to ensure network: %w", err)
	}
	if err := m.netMgr.SetupTAP(ctx, vm.TAPDevice); err != nil {
		return fmt.Errorf("failed to setup TAP device: %w", err)
	}

	// 2. Launch new Firecracker process
	vmDir := m.storMgr.GetVMDir(vmID)
	sockPath := filepath.Join(vmDir, "firecracker.sock")
	logPath := filepath.Join(vmDir, "firecracker.log")

	var success bool
	var cmd *exec.Cmd
	defer func() {
		if !success {
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = m.netMgr.RemoveTAP(ctx, vm.TAPDevice)
			_ = os.Remove(sockPath)
		}
	}()

	cmd, err = firecracker.LaunchProcess(m.cfg.FirecrackerBinary, sockPath, vmID, logPath)
	if err != nil {
		return fmt.Errorf("failed to launch firecracker process: %w", err)
	}

	if err := firecracker.WaitForSocket(sockPath, 5*time.Second); err != nil {
		return fmt.Errorf("firecracker socket not ready: %w", err)
	}

	// 3. Load Snapshot with resume=true
	fcClient := firecracker.NewClient(sockPath)
	if err := fcClient.LoadSnapshot(ctx, vm.SnapshotPath, vm.MemFilePath, true); err != nil {
		return fmt.Errorf("failed to load snapshot: %w", err)
	}

	success = true
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
		_ = m.netMgr.RemoveTAP(ctx, vm.TAPDevice)
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
				fmt.Sprintf("mforge@%s", vm.IPAddress),
				"sudo sync; sudo poweroff",
			)
			if err := sshCmd.Run(); err != nil {
				fallbackCmd := exec.CommandContext(syncCtx, "ssh",
					"-i", vm.SSHKeyPath,
					"-o", "StrictHostKeyChecking=no",
					"-o", "UserKnownHostsFile=/dev/null",
					"-o", "ConnectTimeout=1",
					fmt.Sprintf("root@%s", vm.IPAddress),
					"sync; poweroff",
				)
				_ = fallbackCmd.Run()
			}
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

	// Ensure TAP device is removed from host
	vm, _ := m.store.GetVM(ctx, vmID)
	if vm != nil && vm.TAPDevice != "" {
		_ = m.netMgr.RemoveTAP(ctx, vm.TAPDevice)
	}

	_ = m.storMgr.CleanupVM(vmID)
	return m.store.DeleteVM(ctx, vmID)
}

// ReconcileAndPrune synchronizes database records with live system processes,
// cleans up stale state from dead microVMs, and removes orphaned TAP interfaces.
func (m *Manager) ReconcileAndPrune(ctx context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	vms, err := m.store.ListVMs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs for reconciliation: %w", err)
	}

	activeTAPs := make(map[string]bool)
	for _, v := range vms {
		if v.Status == db.StatusRunning {
			if !isProcessAlive(v.PID) {
				v.Status = db.StatusStopped
				v.PID = 0
				_ = m.store.SaveVM(ctx, v)
				_ = m.netMgr.RemoveTAP(ctx, v.TAPDevice)
			} else {
				activeTAPs[v.TAPDevice] = true
			}
		} else {
			// Created/stopped VMs do not hold active TAP devices
			_ = m.netMgr.RemoveTAP(ctx, v.TAPDevice)
		}
	}

	return m.netMgr.PruneOrphanTAPs(ctx, activeTAPs)
}

// GetNetworkInfo collects runtime state and returns a summary
func (m *Manager) GetNetworkInfo(ctx context.Context) (*network.NetworkInfo, error) {
	vms, err := m.store.ListVMs(ctx)
	if err != nil {
		return nil, err
	}

	var activeTAPs []string
	var allocatedCount int
	for _, v := range vms {
		if v.IPAddress != "" {
			allocatedCount++
		}
		if v.Status == db.StatusRunning && isProcessAlive(v.PID) {
			activeTAPs = append(activeTAPs, v.TAPDevice)
		}
	}

	return m.netMgr.GetNetworkInfo(ctx, activeTAPs, allocatedCount)
}

// InspectNetwork checks the live network connectivity and configuration for a VM
func (m *Manager) InspectNetwork(ctx context.Context, vmID string) (*NetworkInspectResult, error) {
	vm, err := m.store.GetVM(ctx, vmID)
	if err != nil {
		return nil, fmt.Errorf("failed to get vm: %w", err)
	}
	if vm == nil {
		return nil, fmt.Errorf("vm not found: %s", vmID)
	}

	netInfo, _ := m.GetNetworkInfo(ctx)
	bridgeName := "mft3br0"
	gwIP := "172.16.0.1"
	if netInfo != nil {
		bridgeName = netInfo.BridgeName
		gwIP = netInfo.GatewayIP
	}

	reachable := false
	connStatus := "Not reachable (VM is not running)"
	if vm.Status == db.StatusRunning && vm.IPAddress != "" {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:22", vm.IPAddress), 1*time.Second)
		if err == nil {
			conn.Close()
			reachable = true
			connStatus = "Reachable (SSH port 22 OPEN)"
		} else {
			connStatus = fmt.Sprintf("Unreachable (SSH port 22 closed or unresponsive: %v)", err)
		}
	}

	return &NetworkInspectResult{
		VMID:         vm.ID,
		VMName:       vm.Name,
		Status:       string(vm.Status),
		IPAddress:    vm.IPAddress,
		MACAddress:   vm.MACAddress,
		TAPDevice:    vm.TAPDevice,
		BridgeName:   bridgeName,
		Gateway:      gwIP,
		Reachable:    reachable,
		Connectivity: connStatus,
	}, nil
}

// ExecVM executes a remote command inside a running VM via SSH
func (m *Manager) ExecVM(ctx context.Context, vmID string, req ExecRequest) (*ExecResponse, error) {
	vm, err := m.store.GetVM(ctx, vmID)
	if err != nil {
		return nil, fmt.Errorf("failed to get vm: %w", err)
	}
	if vm == nil {
		return nil, fmt.Errorf("vm not found: %s", vmID)
	}
	if vm.Status != db.StatusRunning {
		return nil, fmt.Errorf("vm %s is not running (status: %s)", vmID, vm.Status)
	}
	if vm.IPAddress == "" {
		return nil, fmt.Errorf("vm %s has no allocated IP address", vmID)
	}

	user := req.User
	if user == "" {
		user = "mforge"
	}
	timeoutSec := req.Timeout
	if timeoutSec <= 0 {
		timeoutSec = 30
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=5",
	}
	if vm.SSHKeyPath != "" {
		sshArgs = append(sshArgs, "-i", vm.SSHKeyPath)
	}
	sshArgs = append(sshArgs, fmt.Sprintf("%s@%s", user, vm.IPAddress), req.Command)

	cmd := exec.CommandContext(execCtx, "ssh", sshArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	durationMs := time.Since(start).Milliseconds()

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("command execution error: %w", runErr)
		}
	}

	return &ExecResponse{
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		ExitCode:   exitCode,
		DurationMs: durationMs,
	}, nil
}

// GetStats returns summary metrics for the orchestrator
func (m *Manager) GetStats(ctx context.Context) (*StatsResult, error) {
	vms, err := m.store.ListVMs(ctx)
	if err != nil {
		return nil, err
	}
	stats := &StatsResult{
		TotalVMs: len(vms),
	}
	for _, v := range vms {
		stats.TotalVCPUs += v.VCPUs
		stats.TotalMemoryMB += v.MemoryMB
		switch v.Status {
		case db.StatusRunning:
			stats.RunningVMs++
		case db.StatusPaused:
			stats.SnapshottedVMs++
		case db.StatusStopped:
			stats.StoppedVMs++
		case db.StatusCreated:
			stats.CreatedVMs++
		}
		if v.IPAddress != "" {
			stats.AllocatedIPs++
		}
	}
	return stats, nil
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
