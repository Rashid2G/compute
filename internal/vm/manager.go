package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/firecracker"
	"github.com/Rashid2G/on-demand-vms/internal/image"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/storage"
	"github.com/google/uuid"
)

type Config struct {
	FirecrackerBinary string
	Defaults          config.DefaultsConfig
	Resources         config.ResourceLimits
}

type Manager struct {
	cfg            Config
	store          *db.Store
	netMgr         *network.Manager
	storMgr        *storage.Manager
	imgBuilder     *image.Builder
	defaults       config.DefaultsConfig
	resourceLimits config.ResourceLimits
	runQueue       []string
	queueReasons   map[string]string
	queueTimes     map[string]time.Time
	processChecker func(pid int) bool
	launcher       func(ctx context.Context, vm *db.VM) error
	mu             sync.Mutex
}

func NewManager(cfg Config, store *db.Store, netMgr *network.Manager, storMgr *storage.Manager) *Manager {
	if cfg.FirecrackerBinary == "" {
		cfg.FirecrackerBinary = "/usr/local/bin/firecracker"
	}
	if cfg.Defaults.VCPUs <= 0 {
		cfg.Defaults.VCPUs = 1
	}
	if cfg.Defaults.MemoryMB <= 0 {
		cfg.Defaults.MemoryMB = 2048
	}
	if cfg.Defaults.DiskSizeMB <= 0 {
		cfg.Defaults.DiskSizeMB = config.DiskSize50GB
	}
	if cfg.Defaults.Image == "" {
		if storMgr != nil && storMgr.GetConfig().DefaultRootfs != "" {
			cfg.Defaults.Image = strings.TrimSuffix(storMgr.GetConfig().DefaultRootfs, ".ext4")
		} else {
			cfg.Defaults.Image = config.DefaultImageName
		}
	}

	var imgBuilder *image.Builder
	if store != nil && storMgr != nil {
		imgBuilder = image.NewBuilder(store, storMgr)
	}

	return &Manager{
		cfg:            cfg,
		store:          store,
		netMgr:         netMgr,
		storMgr:        storMgr,
		imgBuilder:     imgBuilder,
		defaults:       cfg.Defaults,
		resourceLimits: cfg.Resources,
		queueReasons:   make(map[string]string),
		queueTimes:     make(map[string]time.Time),
	}
}

// SetProcessChecker overrides process liveness checking (useful for testing)
func (m *Manager) SetProcessChecker(checker func(pid int) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.processChecker = checker
}

// SetLauncher overrides the process launcher (useful for testing)
func (m *Manager) SetLauncher(launcher func(ctx context.Context, vm *db.VM) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.launcher = launcher
}

func (m *Manager) isAlive(pid int) bool {
	if m.processChecker != nil {
		return m.processChecker(pid)
	}
	return isProcessAlive(pid)
}

// GetNetworkManager returns the underlying network manager
func (m *Manager) GetNetworkManager() *network.Manager {
	return m.netMgr
}

// GetStore returns the underlying database store
func (m *Manager) GetStore() *db.Store {
	return m.store
}

// volumesForVM returns data volumes attached to vm in a stable order.
func (m *Manager) volumesForVM(ctx context.Context, vm *db.VM) ([]*db.Volume, error) {
	all, err := m.store.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}
	var out []*db.Volume
	for _, vol := range all {
		if vol.Path == "" {
			continue
		}
		if vol.AttachedVM != vm.Name && vol.AttachedVM != vm.ID {
			continue
		}
		out = append(out, vol)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// GetStorageManager returns the underlying storage manager
func (m *Manager) GetStorageManager() *storage.Manager {
	return m.storMgr
}

// GetImageBuilder returns the underlying image builder
func (m *Manager) GetImageBuilder() *image.Builder {
	return m.imgBuilder
}

// SetImageBuilder overrides the image builder (useful for testing)
func (m *Manager) SetImageBuilder(b *image.Builder) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.imgBuilder = b
}

// GetDefaults returns default specs
func (m *Manager) GetDefaults() config.DefaultsConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.defaults
}

// GetResourceLimits returns active capacity resource limits
func (m *Manager) GetResourceLimits() config.ResourceLimits {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resourceLimits
}

// SetResourceLimits dynamically updates capacity limits
func (m *Manager) SetResourceLimits(limits config.ResourceLimits) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resourceLimits = limits
	m.processQueue(context.Background())
}

type CreateRequest struct {
	Name            string `json:"name"`
	OwnerID         string `json:"owner_id,omitempty"`
	Tier            string `json:"tier,omitempty"`       // "1vcpu", "2vcpu", "4vcpu" (or "micro", "small", "medium")
	VCPUs           int    `json:"vcpus"`                // 1, 2, or 4
	MemoryMB        int    `json:"memory_mb,omitempty"`  // Optional: default 2048, 4096, 8192
	DiskMB          int    `json:"disk_mb,omitempty"`    // 51200 (50GB) or 102400 (100GB)
	Image           string `json:"image,omitempty"`      // Custom image, template, or "ubuntu-24"
	Template        string `json:"template,omitempty"`   // VM Template name or ID to launch from
	CloneFrom       string `json:"clone_from,omitempty"` // VM ID to clone persistent disk from
	IPAddress       string `json:"ip_address,omitempty"`
	MACAddress      string `json:"mac_address,omitempty"`
	PublicIP        *bool  `json:"public_ip,omitempty"`
	PublicIPEnabled *bool  `json:"public_ip_enabled,omitempty"`
	DedicatedIP     *bool  `json:"dedicated_ip,omitempty"`
	DHCP            *bool  `json:"dhcp,omitempty"`
	SSHKeyID        string `json:"ssh_key_id,omitempty"`
	SSHPublicKey    string `json:"ssh_public_key,omitempty"`
}

func (r *CreateRequest) UnmarshalJSON(data []byte) error {
	type Alias CreateRequest
	aux := struct {
		RawPublicIP    interface{} `json:"public_ip,omitempty"`
		RawDedicatedIP interface{} `json:"dedicated_ip,omitempty"`
		RawDHCP        interface{} `json:"dhcp,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(r),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	parseBool := func(v interface{}) *bool {
		if v == nil {
			return nil
		}
		switch val := v.(type) {
		case bool:
			return &val
		case string:
			lower := strings.ToLower(strings.TrimSpace(val))
			if lower == "true" || lower == "1" || lower == "on" || lower == "yes" || lower == "enabled" {
				b := true
				return &b
			}
			if lower == "false" || lower == "0" || lower == "off" || lower == "no" || lower == "disabled" {
				b := false
				return &b
			}
		}
		return nil
	}
	if b := parseBool(aux.RawPublicIP); b != nil {
		r.PublicIP = b
		r.PublicIPEnabled = b
	}
	if b := parseBool(aux.RawDedicatedIP); b != nil {
		r.DedicatedIP = b
		if r.PublicIP == nil {
			r.PublicIP = b
			r.PublicIPEnabled = b
		}
	}
	if b := parseBool(aux.RawDHCP); b != nil {
		r.DHCP = b
	}
	return nil
}

type BuildImageRequest = image.BuildRequest

type CreateTemplateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	VMID        string `json:"vm_id"`
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

// ExecStreamEvent is one chunk of streamed command output, or the final result
// when ExitCode is set.
type ExecStreamEvent struct {
	Stream     string `json:"stream,omitempty"`
	Data       string `json:"data,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

type NetworkInspectResult struct {
	VMID            string `json:"vm_id"`
	VMName          string `json:"vm_name"`
	Status          string `json:"status"`
	IPAddress       string `json:"ip_address"`
	PublicIP        string `json:"public_ip,omitempty"`
	PublicIPEnabled bool   `json:"public_ip_enabled"`
	MACAddress      string `json:"mac_address"`
	TAPDevice       string `json:"tap_device"`
	BridgeName      string `json:"bridge_name"`
	Gateway         string `json:"gateway"`
	Reachable       bool   `json:"reachable"`
	Connectivity    string `json:"connectivity"`
}

type QueueItem struct {
	Position int       `json:"position"`
	VMID     string    `json:"vm_id"`
	VMName   string    `json:"vm_name"`
	VCPUs    int       `json:"vcpus"`
	MemoryMB int       `json:"memory_mb"`
	QueuedAt time.Time `json:"queued_at"`
	Reason   string    `json:"reason"`
}

type StatsResult struct {
	TotalVMs              int                    `json:"total_vms"`
	RunningVMs            int                    `json:"running_vms"`
	QueuedVMs             int                    `json:"queued_vms"`
	SnapshottedVMs        int                    `json:"snapshotted_vms"`
	StoppedVMs            int                    `json:"stopped_vms"`
	CreatedVMs            int                    `json:"created_vms"`
	TotalVCPUs            int                    `json:"total_vcpus"`
	TotalMemoryMB         int                    `json:"total_memory_mb"`
	RunningVCPUs          int                    `json:"running_vcpus"`
	RunningMemoryMB       int                    `json:"running_memory_mb"`
	AllocatedIPs          int                    `json:"allocated_ips"`
	HostCPUs              int                    `json:"host_cpus"`
	HostMemoryAvailableMB int                    `json:"host_memory_available_mb"`
	ResourceLimits        *config.ResourceLimits `json:"resource_limits,omitempty"`
}

// hostMemoryAvailableMB reports the memory the kernel estimates is available for
// new workloads. It returns 0 when the value cannot be determined.
func hostMemoryAvailableMB() int {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "MemAvailable:" {
			continue
		}
		kb, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0
		}
		return kb / 1024
	}
	return 0
}

// NetManager exposes the network manager so API handlers can program host
// networking primitives (port forwarding, TAP inspection, ...).
func (m *Manager) NetManager() *network.Manager {
	return m.netMgr
}

func (m *Manager) getVMStorageDir(vm *db.VM) string {
	if vm.DiskPath != "" {
		return filepath.Dir(vm.DiskPath)
	}
	if vm.InternalID != "" && m.storMgr != nil {
		p := m.storMgr.GetVMDir(vm.InternalID)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if m.storMgr != nil {
		return m.storMgr.GetVMDir(vm.ID)
	}
	return ""
}

func (m *Manager) getVMDirID(vm *db.VM) string {
	if vm.InternalID != "" && m.storMgr != nil {
		p := m.storMgr.GetVMDir(vm.InternalID)
		if _, err := os.Stat(p); err == nil {
			return vm.InternalID
		}
	}
	return vm.ID
}

// CreateVM reserves networking, validates disk space and images, and registers a created VM
func (m *Manager) CreateVM(ctx context.Context, req CreateRequest) (*db.VM, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	uid := uuid.New().String()
	cleanU := strings.ReplaceAll(uid, "-", "")
	internalID := fmt.Sprintf("ins_%s", cleanU[:16])

	var externalID, name string
	if strings.TrimSpace(req.Name) != "" {
		name = strings.TrimSpace(req.Name)
		externalID = name
	} else {
		shortID := cleanU[:8]
		externalID = fmt.Sprintf("vm-%s", shortID)
		name = externalID
	}

	ownerID := req.OwnerID
	if ownerID == "" {
		ownerID = "default"
	}

	existing, _ := m.store.GetVMByOwner(ctx, ownerID, name)
	if existing != nil {
		return nil, fmt.Errorf("a VM named %q already exists for owner %q", name, ownerID)
	}

	// Check if launching from a saved template
	var tpl *db.Template
	if req.Template != "" {
		var tplErr error
		tpl, tplErr = m.store.GetTemplate(ctx, req.Template)
		if tplErr != nil || tpl == nil {
			return nil, fmt.Errorf("disk space issue: template '%s' not found", req.Template)
		}
		if req.Tier == "" && req.VCPUs <= 0 && tpl.VCPUs > 0 {
			req.VCPUs = tpl.VCPUs
		}
		if req.MemoryMB <= 0 && tpl.MemoryMB > 0 {
			req.MemoryMB = tpl.MemoryMB
		}
		if req.DiskMB <= 0 && tpl.DiskMB > 0 {
			req.DiskMB = tpl.DiskMB
		}
	}

	// Map tier string if specified (e.g. "1vcpu", "2vcpu", "4vcpu", "micro", "small", "medium")
	if req.Tier != "" && req.VCPUs <= 0 {
		tier := strings.ToLower(strings.TrimSpace(req.Tier))
		switch tier {
		case "1vcpu", "1", "micro", "nano":
			req.VCPUs = 1
			if req.MemoryMB <= 0 {
				req.MemoryMB = 2048
			}
		case "2vcpu", "2", "small", "standard":
			req.VCPUs = 2
			if req.MemoryMB <= 0 {
				req.MemoryMB = 4096
			}
		case "4vcpu", "4", "medium", "compute", "default":
			req.VCPUs = 4
			if req.MemoryMB <= 0 {
				req.MemoryMB = 8192
			}
		default:
			return nil, fmt.Errorf("disk space issue: invalid tier '%s': supported tiers are '1vcpu', '2vcpu', or '4vcpu' (inspired by boat.dev)", req.Tier)
		}
	}

	// Apply default specs if not specified
	vcpus := req.VCPUs
	if vcpus <= 0 {
		vcpus = m.defaults.VCPUs
		if vcpus <= 0 {
			vcpus = 1
		}
	}
	memoryMB := req.MemoryMB
	diskMB := req.DiskMB
	if diskMB <= 0 {
		diskMB = m.defaults.DiskSizeMB
		if diskMB <= 0 {
			diskMB = config.DiskSize50GB
		}
	}

	// Validate and normalize fixed tiers (1, 2, 4 vCPUs), optional RAM, and 50GB/100GB disks
	var normErr error
	vcpus, memoryMB, diskMB, normErr = config.ValidateAndNormalizeSpecs(vcpus, memoryMB, diskMB)
	if normErr != nil {
		return nil, fmt.Errorf("disk space issue: %w", normErr)
	}

	imageName := req.Image
	if imageName == "" {
		imageName = m.defaults.Image
		if imageName == "" {
			if m.storMgr != nil && m.storMgr.GetConfig().DefaultRootfs != "" {
				imageName = strings.TrimSuffix(m.storMgr.GetConfig().DefaultRootfs, ".ext4")
			} else {
				imageName = config.DefaultImageName
			}
		}
	}

	// 1. Check disk space constraints on creation
	minFreeMB := m.resourceLimits.MinFreeDiskSpaceMB
	if minFreeMB > 0 {
		if err := m.storMgr.CheckHostDiskSpace(minFreeMB); err != nil {
			return nil, fmt.Errorf("disk space issue: %w", err)
		}
	}

	if m.resourceLimits.MaxTotalDiskMB > 0 {
		usedMB, err := m.storMgr.GetTotalVMsDiskUsageMB()
		if err == nil && usedMB+int64(diskMB) > m.resourceLimits.MaxTotalDiskMB {
			return nil, fmt.Errorf("disk space issue: max total disk space limit exceeded (%d MB used + %d MB requested > %d MB limit)", usedMB, diskMB, m.resourceLimits.MaxTotalDiskMB)
		}
	}

	var diskPath string
	var rootfsPath, kernelPath string

	if req.Template != "" && tpl != nil {
		// Launch from template disk
		if _, err := os.Stat(tpl.DiskPath); err != nil {
			return nil, fmt.Errorf("disk space issue: template disk not found at %s: %w", tpl.DiskPath, err)
		}
		var err error
		diskPath, err = m.storMgr.InitVMDiskFromImageWithResize(internalID, tpl.DiskPath, diskMB)
		if err != nil {
			return nil, fmt.Errorf("disk space issue: failed to initialize VM disk from template '%s': %w", req.Template, err)
		}
		rootfsPath = tpl.DiskPath
		kernelPath = tpl.KernelPath
		if kernelPath == "" {
			kernelPath = m.storMgr.GetBaseKernelPath()
		}
	} else if req.CloneFrom != "" {
		// Clone disk from existing VM
		srcVM, err := m.store.GetVM(ctx, req.CloneFrom)
		if err != nil || srcVM == nil {
			return nil, fmt.Errorf("disk space issue: clone source VM '%s' not found", req.CloneFrom)
		}
		srcDirID := m.getVMDirID(srcVM)
		diskPath, err = m.storMgr.CloneVMDisk(srcDirID, internalID, diskMB)
		if err != nil && srcDirID != srcVM.ID {
			diskPath, err = m.storMgr.CloneVMDisk(srcVM.ID, internalID, diskMB)
		}
		if err != nil {
			return nil, fmt.Errorf("disk space issue: failed to clone disk from VM '%s': %w", req.CloneFrom, err)
		}
		rootfsPath = srcVM.RootfsPath
		kernelPath = srcVM.KernelPath
		if kernelPath == "" {
			kernelPath = m.storMgr.GetBaseKernelPath()
		}
	} else {
		// Check custom image first
		customImg, _ := m.store.GetImage(ctx, imageName)
		if customImg != nil {
			if customImg.Status == db.ImageStatusBuilding {
				// A daemon restart during a build can leave the record in the
				// building state after the rootfs was produced.
				if _, statErr := os.Stat(customImg.RootfsPath); statErr == nil {
					customImg.Status = db.ImageStatusReady
					customImg.UpdatedAt = time.Now().UTC()
					_ = m.store.SaveImage(ctx, customImg)
				}
			}
			if customImg.Status != db.ImageStatusReady {
				return nil, fmt.Errorf("disk space issue: custom image '%s' is not ready (status: %s)", imageName, customImg.Status)
			}
			if _, err := os.Stat(customImg.RootfsPath); err != nil {
				return nil, fmt.Errorf("disk space issue: custom image rootfs not found at %s: %w", customImg.RootfsPath, err)
			}
			var err error
			diskPath, err = m.storMgr.InitVMDiskFromImageWithResize(internalID, customImg.RootfsPath, diskMB)
			if err != nil {
				return nil, fmt.Errorf("disk space issue: failed to initialize VM disk from custom image '%s': %w", imageName, err)
			}
			rootfsPath = customImg.RootfsPath
			kernelPath = customImg.KernelPath
			if kernelPath == "" {
				kernelPath = m.storMgr.GetBaseKernelPath()
			}
		} else {
			// Check if imageName refers to a template
			namedTpl, _ := m.store.GetTemplate(ctx, imageName)
			if namedTpl != nil {
				if _, err := os.Stat(namedTpl.DiskPath); err != nil {
					return nil, fmt.Errorf("disk space issue: template disk not found at %s: %w", namedTpl.DiskPath, err)
				}
				var err error
				diskPath, err = m.storMgr.InitVMDiskFromImageWithResize(internalID, namedTpl.DiskPath, diskMB)
				if err != nil {
					return nil, fmt.Errorf("disk space issue: failed to initialize VM disk from template '%s': %w", imageName, err)
				}
				rootfsPath = namedTpl.DiskPath
				kernelPath = namedTpl.KernelPath
				if kernelPath == "" {
					kernelPath = m.storMgr.GetBaseKernelPath()
				}
			} else {
				// Resolve base image (compute supports Ubuntu 24 base image)
				var err error
				kernelPath, rootfsPath, err = m.storMgr.ResolveImage(imageName)
				if err != nil {
					return nil, fmt.Errorf("disk space issue: %w", err)
				}
				if _, err := os.Stat(rootfsPath); err != nil {
					return nil, fmt.Errorf("disk space issue: base rootfs image not found at %s: %w", rootfsPath, err)
				}

				// Initialize persistent disk from base image with sparse resize to 50GB / 100GB
				diskPath, err = m.storMgr.InitVMDiskFromImageWithResize(internalID, rootfsPath, diskMB)
				if err != nil {
					return nil, fmt.Errorf("disk space issue: failed to initialize VM disk: %w", err)
				}
			}
		}
	}

	// Resolve and embed chosen SSH public key
	var embeddedPubKey string
	var embeddedKeyName string
	if req.SSHPublicKey != "" {
		embeddedPubKey = strings.TrimSpace(req.SSHPublicKey)
		embeddedKeyName = "custom"
	} else if req.SSHKeyID != "" {
		key, err := m.store.GetSSHKey(ctx, req.SSHKeyID)
		if err != nil {
			return nil, fmt.Errorf("invalid requested ssh key: %w", err)
		}
		if key != nil {
			embeddedPubKey = key.PublicKey
			embeddedKeyName = key.Name
		} else {
			return nil, fmt.Errorf("ssh key not found: %s", req.SSHKeyID)
		}
	}

	if embeddedPubKey != "" && diskPath != "" {
		_ = m.storMgr.EmbedSSHKey(diskPath, embeddedPubKey)
	}

	// 2. Fetch current IP allocations
	usedIPs, err := m.store.GetAllocatedIPs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch used IPs: %w", err)
	}
	gw := m.netMgr.GetGatewayIP()
	if gw != "" {
		usedIPs[gw] = true
	}
	cfg := m.netMgr.GetConfig()
	if cfg.BridgeIP != "" {
		if bIP, _, err := net.ParseCIDR(cfg.BridgeIP); err == nil {
			usedIPs[bIP.String()] = true
		}
	}

	isDHCP := (req.DHCP != nil && *req.DHCP) || strings.EqualFold(req.IPAddress, "dhcp")
	isWiredNIC := (cfg.Mode == network.ModeNIC || cfg.Mode == network.ModeWired)
	if !isDHCP && req.IPAddress == "" && isWiredNIC && (req.DHCP == nil || *req.DHCP) {
		isDHCP = true
	}

	var ip, mac string
	if isDHCP {
		if req.MACAddress != "" {
			mac = req.MACAddress
		} else {
			mac = network.GenerateMAC(nil)
		}
		// Attempt to acquire an authoritative lease from the network DHCP server
		if lease, err := m.netMgr.AcquireDHCPLease(ctx, mac); err == nil && lease != nil && lease.IP != nil {
			ip = lease.IP.String()
		} else {
			ip = "dhcp"
		}
	} else if req.IPAddress != "" {
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
	tapDev := fmt.Sprintf("mftap-%s", cleanU[:8])

	sshKeyPath := m.storMgr.GetSSHKeyPath()

	// Determine public IP setting (default is on: each VM takes a dedicated IP on the main network considered as public IP)
	publicIPEnabled := true
	if req.PublicIP != nil {
		publicIPEnabled = *req.PublicIP
	} else if req.PublicIPEnabled != nil {
		publicIPEnabled = *req.PublicIPEnabled
	} else if req.DedicatedIP != nil {
		publicIPEnabled = *req.DedicatedIP
	}

	var publicIP string
	if publicIPEnabled {
		publicIP = ip // Dedicated IP on main network is considered as the public IP
	}

	vm := &db.VM{
		ID:              externalID,
		InternalID:      internalID,
		OwnerID:         ownerID,
		Name:            name,
		Status:          db.StatusCreated,
		VCPUs:           vcpus,
		MemoryMB:        memoryMB,
		IPAddress:       ip,
		PublicIP:        publicIP,
		PublicIPEnabled: publicIPEnabled,
		MACAddress:      mac,
		TAPDevice:       tapDev,
		RootfsPath:      rootfsPath,
		KernelPath:      kernelPath,
		DiskPath:        diskPath,
		SSHKeyPath:      sshKeyPath,
		SSHKeyName:      embeddedKeyName,
		SSHPublicKey:    embeddedPubKey,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}

	if err := m.store.SaveVM(ctx, vm); err != nil {
		m.storMgr.CleanupVM(internalID)
		m.storMgr.CleanupVM(externalID)
		return nil, fmt.Errorf("failed to save VM in DB: %w", err)
	}

	return vm, nil
}

// CloneVM clones an existing VM's disk into a new VM with optional specification overrides
func (m *Manager) CloneVM(ctx context.Context, srcVMID string, req CreateRequest) (*db.VM, error) {
	req.CloneFrom = srcVMID
	if req.PublicIP == nil && req.PublicIPEnabled == nil && req.DedicatedIP == nil {
		if srcVM, _ := m.store.GetVM(ctx, srcVMID); srcVM != nil {
			req.PublicIP = &srcVM.PublicIPEnabled
			req.PublicIPEnabled = &srcVM.PublicIPEnabled
		}
	}
	return m.CreateVM(ctx, req)
}

// RunVM starts an existing VM immediately if resources permit, or queues it if host capacity is reached
func (m *Manager) RunVM(ctx context.Context, vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	vm, err := m.store.GetVM(ctx, vmID)
	if err != nil || vm == nil {
		return fmt.Errorf("vm %s not found: %w", vmID, err)
	}

	vm.DesiredRunning = true
	if vm.Status == db.StatusRunning && m.isAlive(vm.PID) {
		return m.store.SaveVM(ctx, vm) // Already running; persist desired state
	}

	// Capacity check: if host resources are maxed out, queue the run
	canRun, reason := m.canRunVM(vm)
	if !canRun {
		vm.Status = db.StatusQueued
		m.enqueue(vm.ID, reason)
		if err := m.store.SaveVM(ctx, vm); err != nil {
			return fmt.Errorf("failed to save queued VM state: %w", err)
		}
		return nil
	}

	// Launch VM process
	if err := m.startVMProcess(ctx, vm); err != nil {
		return err
	}

	m.dequeue(vm.ID)
	vm.Status = db.StatusRunning
	return m.store.SaveVM(ctx, vm)
}

// startVMProcess executes the Firecracker binary, attaches TAP, and boots the microVM
func (m *Manager) startVMProcess(ctx context.Context, vm *db.VM) error {
	if m.launcher != nil {
		return m.launcher(ctx, vm)
	}

	// 1. Ensure Network & TAP
	if err := m.netMgr.EnsureNetwork(ctx); err != nil {
		return fmt.Errorf("failed to ensure network: %w", err)
	}
	if err := m.netMgr.SetupTAP(ctx, vm.TAPDevice); err != nil {
		return fmt.Errorf("failed to setup TAP device: %w", err)
	}

	// 2. Setup paths
	vmDir := m.getVMStorageDir(vm)
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
	var err error
	cmd, err = firecracker.LaunchProcess(m.cfg.FirecrackerBinary, sockPath, sanitizeFirecrackerID(vm.ID), logPath)
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

	// If VM still has "dhcp" as IP, try resolving lease before booting Firecracker
	if vm.IPAddress == "dhcp" || vm.IPAddress == "" {
		if leasedIP, err := m.netMgr.ResolveGuestIPByMAC(ctx, vm.MACAddress); err == nil && leasedIP != "" {
			vm.IPAddress = leasedIP
			if vm.PublicIPEnabled {
				vm.PublicIP = leasedIP
			}
			_ = m.store.SaveVM(ctx, vm)
		}
	}

	// Boot source with exact netmask and gateway
	gwIP := m.netMgr.GetGatewayIP()
	netmask := m.netMgr.GetNetmask()
	var bootArgs string
	if vm.IPAddress == "dhcp" || vm.IPAddress == "" {
		bootArgs = "console=ttyS0 reboot=k panic=1 pci=off ip=dhcp root=/dev/vda rw quiet"
	} else {
		bootArgs = fmt.Sprintf("console=ttyS0 reboot=k panic=1 pci=off ip=%s::%s:%s:%s:eth0:off root=/dev/vda rw quiet", vm.IPAddress, gwIP, netmask, vm.ID)
	}
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

	// Attached data volumes (vdb, vdc, ...)
	if vols, err := m.volumesForVM(ctx, vm); err == nil {
		for i, vol := range vols {
			if i >= 25 {
				break
			}
			if err := fcClient.AttachDrive(ctx, firecracker.Drive{
				DriveID:      fmt.Sprintf("data%d", i),
				PathOnHost:   vol.Path,
				IsRootDevice: false,
				IsReadOnly:   false,
			}); err != nil {
				return fmt.Errorf("failed to attach volume %s: %w", vol.Name, err)
			}
			vol.DevicePath = fmt.Sprintf("/dev/vd%c", 'b'+rune(i))
			_ = m.store.SaveVolume(ctx, vol)
		}
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

	success = true
	vm.PID = cmd.Process.Pid
	vm.SocketPath = sockPath
	_ = m.netMgr.ConfigureVMPublicIP(ctx, vm.IPAddress, vm.TAPDevice, vm.PublicIPEnabled)
	if vm.IPAddress == "dhcp" || vm.IPAddress == "" {
		go m.pollDHCPLease(vm.ID, vm.MACAddress, vm.PublicIPEnabled)
	}
	return nil
}

func (m *Manager) pollDHCPLease(vmID, mac string, publicIPEnabled bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	checkLease := func() bool {
		if leasedIP, err := m.netMgr.ResolveGuestIPByMAC(ctx, mac); err == nil && leasedIP != "" {
			m.mu.Lock()
			vm, _ := m.store.GetVM(context.Background(), vmID)
			if vm != nil && (vm.IPAddress == "dhcp" || vm.IPAddress == "") {
				vm.IPAddress = leasedIP
				if publicIPEnabled {
					vm.PublicIP = leasedIP
				}
				_ = m.store.SaveVM(context.Background(), vm)
				_ = m.netMgr.ConfigureVMPublicIP(context.Background(), leasedIP, vm.TAPDevice, publicIPEnabled)
			}
			m.mu.Unlock()
			return true
		}
		return false
	}

	if checkLease() {
		return
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if checkLease() {
				return
			}
		}
	}
}

// canRunVM checks if the candidate VM can run without violating resource limits
func (m *Manager) canRunVM(candidate *db.VM) (bool, string) {
	vms, err := m.store.ListVMs(context.Background())
	if err != nil {
		return true, ""
	}

	var runningCount int
	var runningVCPUs int
	var runningMemoryMB int

	for _, v := range vms {
		if v.ID == candidate.ID {
			continue
		}
		if v.Status == db.StatusRunning && m.isAlive(v.PID) {
			runningCount++
			runningVCPUs += v.VCPUs
			runningMemoryMB += v.MemoryMB
		}
	}

	if m.resourceLimits.MaxRunningVMs > 0 && runningCount+1 > m.resourceLimits.MaxRunningVMs {
		return false, fmt.Sprintf("max running VMs limit reached (%d/%d)", runningCount, m.resourceLimits.MaxRunningVMs)
	}
	if m.resourceLimits.MaxTotalVCPUs > 0 && runningVCPUs+candidate.VCPUs > m.resourceLimits.MaxTotalVCPUs {
		return false, fmt.Sprintf("max total vCPUs limit reached (%d + %d > %d)", runningVCPUs, candidate.VCPUs, m.resourceLimits.MaxTotalVCPUs)
	}
	if m.resourceLimits.MaxTotalMemoryMB > 0 && runningMemoryMB+candidate.MemoryMB > m.resourceLimits.MaxTotalMemoryMB {
		return false, fmt.Sprintf("max total memory limit reached (%d MB + %d MB > %d MB)", runningMemoryMB, candidate.MemoryMB, m.resourceLimits.MaxTotalMemoryMB)
	}

	// Physical host admission control. The configured limits above are optional
	// policy caps; this guard prevents the daemon from launching a microVM that
	// would overcommit the machine's actually available RAM and trigger the OOM
	// killer. Reserve headroom for the daemon, page cache, and host services.
	const hostReserveMB = 256
	if available := hostMemoryAvailableMB(); available > 0 && candidate.MemoryMB+hostReserveMB > available {
		return false, fmt.Sprintf("insufficient host memory (requested %d MB + %d MB reserve, %d MB available)", candidate.MemoryMB, hostReserveMB, available)
	}

	return true, ""
}

func (m *Manager) enqueue(vmID, reason string) {
	for _, id := range m.runQueue {
		if id == vmID {
			m.queueReasons[vmID] = reason
			return
		}
	}
	m.runQueue = append(m.runQueue, vmID)
	m.queueReasons[vmID] = reason
	m.queueTimes[vmID] = time.Now().UTC()
}

func (m *Manager) dequeue(vmID string) {
	var next []string
	for _, id := range m.runQueue {
		if id != vmID {
			next = append(next, id)
		}
	}
	m.runQueue = next
	delete(m.queueReasons, vmID)
	delete(m.queueTimes, vmID)
}

// processQueue iterates over queued VMs and launches any that now fit within host capacity
func (m *Manager) processQueue(ctx context.Context) {
	if len(m.runQueue) == 0 {
		return
	}

	var remaining []string
	for _, id := range m.runQueue {
		v, err := m.store.GetVM(ctx, id)
		if err != nil || v == nil || v.Status != db.StatusQueued {
			delete(m.queueReasons, id)
			delete(m.queueTimes, id)
			continue
		}

		canRun, _ := m.canRunVM(v)
		if canRun {
			if err := m.startVMProcess(ctx, v); err == nil {
				v.Status = db.StatusRunning
				_ = m.store.SaveVM(ctx, v)
				delete(m.queueReasons, id)
				delete(m.queueTimes, id)
				continue
			} else {
				v.Status = db.StatusFailed
				_ = m.store.SaveVM(ctx, v)
				delete(m.queueReasons, id)
				delete(m.queueTimes, id)
				continue
			}
		}

		remaining = append(remaining, id)
	}
	m.runQueue = remaining
}

// RecoverQueue recovers queued VMs from database state and attempts to launch them
func (m *Manager) RecoverQueue(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	vms, err := m.store.ListVMs(ctx)
	if err != nil {
		return err
	}
	for _, v := range vms {
		if v.Status == db.StatusQueued {
			m.enqueue(v.ID, "restored from database")
		}
	}
	m.processQueue(ctx)
	return nil
}

// GetQueue returns the list of currently queued runs in order
func (m *Manager) GetQueue(ctx context.Context) ([]*QueueItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var items []*QueueItem
	pos := 1
	for _, id := range m.runQueue {
		v, err := m.store.GetVM(ctx, id)
		if err == nil && v != nil && v.Status == db.StatusQueued {
			qTime := m.queueTimes[id]
			if qTime.IsZero() {
				qTime = v.UpdatedAt
			}
			reason := m.queueReasons[id]
			if reason == "" {
				reason = "waiting for host resources"
			}
			items = append(items, &QueueItem{
				Position: pos,
				VMID:     v.ID,
				VMName:   v.Name,
				VCPUs:    v.VCPUs,
				MemoryMB: v.MemoryMB,
				QueuedAt: qTime,
				Reason:   reason,
			})
			pos++
		}
	}
	if items == nil {
		items = []*QueueItem{}
	}
	return items, nil
}

// SaveState pauses the running VM and creates a state + memory snapshot
func (m *Manager) SaveState(ctx context.Context, vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	vm, err := m.store.GetVM(ctx, vmID)
	if err != nil || vm == nil {
		return fmt.Errorf("vm %s not found: %w", vmID, err)
	}

	if vm.Status != db.StatusRunning || !m.isAlive(vm.PID) {
		return fmt.Errorf("vm %s is not running (status: %s)", vmID, vm.Status)
	}

	fcClient := firecracker.NewClient(vm.SocketPath)

	// 1. Pause VM
	if err := fcClient.Pause(ctx); err != nil {
		return fmt.Errorf("failed to pause vm: %w", err)
	}

	// 2. Snapshot
	snapPath, memPath := m.storMgr.GetSnapshotPaths(m.getVMDirID(vm))
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
	vm.DesiredRunning = false
	vm.SnapshotPath = snapPath
	vm.MemFilePath = memPath
	vm.PID = 0

	if err := m.store.SaveVM(ctx, vm); err != nil {
		return err
	}

	m.processQueue(ctx)
	return nil
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

	// Capacity check for resume
	canRun, reason := m.canRunVM(vm)
	if !canRun {
		vm.Status = db.StatusQueued
		m.enqueue(vm.ID, reason)
		return m.store.SaveVM(ctx, vm)
	}

	// 1. Ensure Network & TAP
	if err := m.netMgr.EnsureNetwork(ctx); err != nil {
		return fmt.Errorf("failed to ensure network: %w", err)
	}
	if err := m.netMgr.SetupTAP(ctx, vm.TAPDevice); err != nil {
		return fmt.Errorf("failed to setup TAP device: %w", err)
	}

	// 2. Launch new Firecracker process
	vmDir := m.getVMStorageDir(vm)
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

	fcID := vm.InternalID
	if fcID == "" {
		fcID = vm.ID
	}
	cmd, err = firecracker.LaunchProcess(m.cfg.FirecrackerBinary, sockPath, sanitizeFirecrackerID(fcID), logPath)
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
	m.dequeue(vm.ID)
	vm.Status = db.StatusRunning
	vm.DesiredRunning = true
	vm.PID = cmd.Process.Pid
	vm.SocketPath = sockPath
	_ = m.netMgr.ConfigureVMPublicIP(ctx, vm.IPAddress, vm.TAPDevice, vm.PublicIPEnabled)
	return m.store.SaveVM(ctx, vm)
}

// StopVM gracefully shuts down a running VM, flushing caches, and cleaning runtime resources
func (m *Manager) StopVM(ctx context.Context, vmID string) error {
	log.Printf("[vm] StopVM invoked for %s", vmID)
	m.mu.Lock()
	defer m.mu.Unlock()

	vm, err := m.store.GetVM(ctx, vmID)
	if err != nil || vm == nil {
		return fmt.Errorf("vm %s not found: %w", vmID, err)
	}

	if vm.Status == db.StatusStopped && !m.isAlive(vm.PID) {
		_ = m.netMgr.RemoveTAP(ctx, vm.TAPDevice)
		return nil
	}

	if vm.PID > 0 && m.isAlive(vm.PID) {
		// 1. Try graceful sync over SSH if accessible
		if vm.IPAddress != "" && vm.SSHKeyPath != "" {
			syncCtx, syncCancel := context.WithTimeout(ctx, 2*time.Second)
			sshCmd := exec.CommandContext(syncCtx, "ssh",
				"-i", vm.SSHKeyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ConnectTimeout=1",
				fmt.Sprintf("compute@%s", vm.IPAddress),
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
			if !m.isAlive(vm.PID) {
				break
			}
			time.Sleep(150 * time.Millisecond)
		}

		// 2. If still alive, attempt SendCtrlAltDel
		if m.isAlive(vm.PID) && vm.SocketPath != "" {
			fcClient := firecracker.NewClient(vm.SocketPath)
			_ = fcClient.SendCtrlAltDel(ctx)
			time.Sleep(300 * time.Millisecond)
		}

		// 3. If still alive, SIGTERM then SIGKILL
		if m.isAlive(vm.PID) {
			syscall.Kill(vm.PID, syscall.SIGTERM)
			time.Sleep(200 * time.Millisecond)
			if m.isAlive(vm.PID) {
				syscall.Kill(vm.PID, syscall.SIGKILL)
			}
		}
	}

	// Clean up TAP interface, public IP isolation rules, and socket
	_ = m.netMgr.RemoveTAP(ctx, vm.TAPDevice)
	_ = m.netMgr.ConfigureVMPublicIP(ctx, vm.IPAddress, vm.TAPDevice, false)
	if vm.SocketPath != "" {
		_ = os.Remove(vm.SocketPath)
	}

	vm.Status = db.StatusStopped
	vm.DesiredRunning = false
	vm.PID = 0
	if err := m.store.SaveVM(ctx, vm); err != nil {
		return err
	}

	// Wake up queued VMs if capacity is now available
	m.processQueue(ctx)
	return nil
}

// DeleteVM stops the VM, cleans up disks, and deletes the record from DB
func (m *Manager) DeleteVM(ctx context.Context, vmID string) error {
	vm, _ := m.store.GetVM(ctx, vmID)
	if vm != nil {
		_ = m.netMgr.ConfigureVMPublicIP(ctx, vm.IPAddress, vm.TAPDevice, false)
	}

	_ = m.StopVM(ctx, vmID)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.dequeue(vmID)
	if vm != nil {
		m.dequeue(vm.ID)
		m.dequeue(vm.InternalID)
		m.dequeue(vm.Name)
	}

	// Ensure TAP device is removed from host and DHCP lease released
	if vm != nil && vm.TAPDevice != "" {
		_ = m.netMgr.RemoveTAP(ctx, vm.TAPDevice)
	}
	if vm != nil && vm.IPAddress != "" && vm.IPAddress != "dhcp" {
		_ = m.netMgr.ReleaseDHCPLease(ctx, vm.MACAddress, vm.IPAddress)
	}

	if vm != nil {
		if vm.InternalID != "" {
			_ = m.storMgr.CleanupVM(vm.InternalID)
		}
		_ = m.storMgr.CleanupVM(vm.ID)
		_ = m.storMgr.CleanupVM(vm.Name)
		if err := m.store.DeleteVM(ctx, vm.InternalID); err != nil {
			_ = m.store.DeleteVM(ctx, vm.ID)
			return err
		}
		_ = m.store.DeleteVM(ctx, vm.ID)
	} else {
		_ = m.storMgr.CleanupVM(vmID)
		if err := m.store.DeleteVM(ctx, vmID); err != nil {
			return err
		}
	}

	m.processQueue(ctx)
	return nil
}

// ReconcileAndPrune synchronizes database records with live system processes,
// cleans up stale state from dead microVMs, and removes orphaned TAP interfaces.
func (m *Manager) ReconcileAndPrune(ctx context.Context) ([]string, error) {
	log.Printf("[vm] ReconcileAndPrune invoked")
	m.mu.Lock()
	defer m.mu.Unlock()

	vms, err := m.store.ListVMs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs for reconciliation: %w", err)
	}

	activeTAPs := make(map[string]bool)
	for _, v := range vms {
		if v.Status == db.StatusRunning {
			if !m.isAlive(v.PID) {
				log.Printf("[vm] reconcile marking %s stopped: pid=%d not alive", v.ID, v.PID)
				v.Status = db.StatusStopped
				v.PID = 0
				_ = m.store.SaveVM(ctx, v)
				_ = m.netMgr.RemoveTAP(ctx, v.TAPDevice)
			} else {
				activeTAPs[v.TAPDevice] = true
			}
		} else {
			_ = m.netMgr.RemoveTAP(ctx, v.TAPDevice)
		}
	}

	pruned, err := m.netMgr.PruneOrphanTAPs(ctx, activeTAPs)
	m.processQueue(ctx)
	return pruned, err
}

// StartDesiredVMs restarts sandboxes that were running before the daemon (or
// host) went down. Paused snapshots stay paused and are resumed explicitly.
func (m *Manager) StartDesiredVMs(ctx context.Context) {
	vms, err := m.store.ListVMs(ctx)
	if err != nil {
		log.Printf("[vm] desired-state startup: %v", err)
		return
	}
	for _, vm := range vms {
		if !vm.DesiredRunning {
			continue
		}
		if vm.Status == db.StatusRunning {
			continue
		}
		var startErr error
		if vm.Status == db.StatusPaused {
			startErr = m.ResumeVM(ctx, vm.ID)
		} else {
			startErr = m.RunVM(ctx, vm.ID)
		}
		if startErr != nil {
			log.Printf("[vm] could not restore %s (%s): %v", vm.Name, vm.Status, startErr)
			continue
		}
		log.Printf("[vm] restored sandbox %s after control-plane startup", vm.Name)
	}
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
		if v.Status == db.StatusRunning && m.isAlive(v.PID) {
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
	gwIP := "192.168.1.1"
	if netInfo != nil {
		bridgeName = netInfo.BridgeName
		gwIP = netInfo.GatewayIP
	}

	reachable := false
	connStatus := "Not reachable (VM is not running)"
	if vm.Status == db.StatusRunning {
		if vm.IPAddress == "dhcp" || vm.IPAddress == "" {
			connStatus = "Dynamic IP awaiting DHCP lease confirmation"
		} else {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:22", vm.IPAddress), 1*time.Second)
			if err == nil {
				conn.Close()
				reachable = true
				connStatus = "Reachable (SSH port 22 OPEN)"
			} else {
				connStatus = fmt.Sprintf("Unreachable (SSH port 22 closed or unresponsive: %v)", err)
			}
		}
	}

	publicIP := vm.PublicIP
	if vm.PublicIPEnabled && publicIP == "" && vm.IPAddress != "" {
		publicIP = vm.IPAddress
	}
	return &NetworkInspectResult{
		VMID:            vm.ID,
		VMName:          vm.Name,
		Status:          string(vm.Status),
		IPAddress:       vm.IPAddress,
		PublicIP:        publicIP,
		PublicIPEnabled: vm.PublicIPEnabled,
		MACAddress:      vm.MACAddress,
		TAPDevice:       vm.TAPDevice,
		BridgeName:      bridgeName,
		Gateway:         gwIP,
		Reachable:       reachable,
		Connectivity:    connStatus,
	}, nil
}

// UpdateVMConfigRequest defines configuration options that can be updated on an existing VM
type UpdateVMConfigRequest struct {
	PublicIP        *bool `json:"public_ip,omitempty"`
	PublicIPEnabled *bool `json:"public_ip_enabled,omitempty"`
	DedicatedIP     *bool `json:"dedicated_ip,omitempty"`
}

func (r *UpdateVMConfigRequest) UnmarshalJSON(data []byte) error {
	type Alias UpdateVMConfigRequest
	aux := struct {
		RawPublicIP    interface{} `json:"public_ip,omitempty"`
		RawDedicatedIP interface{} `json:"dedicated_ip,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(r),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	parseBool := func(v interface{}) *bool {
		if v == nil {
			return nil
		}
		switch val := v.(type) {
		case bool:
			return &val
		case string:
			lower := strings.ToLower(strings.TrimSpace(val))
			if lower == "true" || lower == "1" || lower == "on" || lower == "yes" || lower == "enabled" {
				b := true
				return &b
			}
			if lower == "false" || lower == "0" || lower == "off" || lower == "no" || lower == "disabled" {
				b := false
				return &b
			}
		}
		return nil
	}
	if b := parseBool(aux.RawPublicIP); b != nil {
		r.PublicIP = b
		r.PublicIPEnabled = b
	}
	if b := parseBool(aux.RawDedicatedIP); b != nil {
		r.DedicatedIP = b
		if r.PublicIP == nil {
			r.PublicIP = b
			r.PublicIPEnabled = b
		}
	}
	return nil
}

// UpdateVMConfig updates configuration parameters on an existing VM (such as toggling dedicated public IP on or off)
func (m *Manager) UpdateVMConfig(ctx context.Context, vmID string, req UpdateVMConfigRequest) (*db.VM, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	vm, err := m.store.GetVM(ctx, vmID)
	if err != nil || vm == nil {
		return nil, fmt.Errorf("vm %s not found: %w", vmID, err)
	}

	var changed bool
	var enabled bool
	if req.PublicIP != nil {
		enabled = *req.PublicIP
		changed = true
	} else if req.PublicIPEnabled != nil {
		enabled = *req.PublicIPEnabled
		changed = true
	} else if req.DedicatedIP != nil {
		enabled = *req.DedicatedIP
		changed = true
	}

	if changed {
		vm.PublicIPEnabled = enabled
		if enabled {
			vm.PublicIP = vm.IPAddress // Dedicated IP on main network is considered as public IP
		} else {
			vm.PublicIP = ""
		}

		if vm.Status == db.StatusRunning {
			_ = m.netMgr.ConfigureVMPublicIP(ctx, vm.IPAddress, vm.TAPDevice, vm.PublicIPEnabled)
		}
		if err := m.store.SaveVM(ctx, vm); err != nil {
			return nil, fmt.Errorf("failed to save vm config: %w", err)
		}
	}

	return vm, nil
}

// newSSHExec builds an SSH command for a running VM. A zero Timeout means no
// deadline; callers that want a default apply it before calling.
func (m *Manager) newSSHExec(ctx context.Context, vmID string, req ExecRequest) (*exec.Cmd, context.Context, context.CancelFunc, error) {
	vm, err := m.store.GetVM(ctx, vmID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get vm: %w", err)
	}
	if vm == nil {
		return nil, nil, nil, fmt.Errorf("vm not found: %s", vmID)
	}
	if vm.Status != db.StatusRunning {
		return nil, nil, nil, fmt.Errorf("vm %s is not running (status: %s)", vmID, vm.Status)
	}
	if vm.IPAddress == "" {
		return nil, nil, nil, fmt.Errorf("vm %s has no allocated IP address", vmID)
	}

	user := req.User
	if user == "" {
		user = "compute"
	}

	var execCtx context.Context
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(req.Timeout)*time.Second)
	} else {
		execCtx, cancel = context.WithCancel(ctx)
	}

	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=5",
	}
	if vm.SSHKeyPath != "" {
		sshArgs = append(sshArgs, "-i", vm.SSHKeyPath)
	}
	command := req.Command
	if user != "root" {
		// Make environment-profile secrets available to every non-root command
		// (exec, jobs, setup scripts) the same way they are to interactive shells.
		command = `if [ -f "$HOME/.compute-env" ]; then set -a; . "$HOME/.compute-env"; set +a; fi; ` + command
	}
	sshArgs = append(sshArgs, fmt.Sprintf("%s@%s", user, vm.IPAddress), command)

	return exec.CommandContext(execCtx, "ssh", sshArgs...), execCtx, cancel, nil
}

// ExecVM executes a remote command inside a running VM via SSH, buffering the
// output until the command finishes.
func (m *Manager) ExecVM(ctx context.Context, vmID string, req ExecRequest) (*ExecResponse, error) {
	if req.Timeout <= 0 {
		req.Timeout = 30
	}
	cmd, _, cancel, err := m.newSSHExec(ctx, vmID, req)
	if err != nil {
		return nil, err
	}
	defer cancel()

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

// ExecVMInput executes a remote command inside a running VM while piping stdin
// to it. Output is buffered until the command finishes.
func (m *Manager) ExecVMInput(ctx context.Context, vmID string, req ExecRequest, stdin io.Reader) (*ExecResponse, error) {
	if req.Timeout <= 0 {
		req.Timeout = 60
	}
	cmd, _, cancel, err := m.newSSHExec(ctx, vmID, req)
	if err != nil {
		return nil, err
	}
	defer cancel()

	cmd.Stdin = stdin
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

// ExecVMStream executes a remote command inside a running VM and emits output
// chunks as they arrive. A zero Timeout means the command may run indefinitely.
// The final event always carries the exit code (and an error, when relevant).
func (m *Manager) ExecVMStream(ctx context.Context, vmID string, req ExecRequest, emit func(ExecStreamEvent) error) error {
	cmd, execCtx, cancel, err := m.newSSHExec(ctx, vmID, req)
	if err != nil {
		return err
	}
	defer cancel()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to open stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to open stderr pipe: %w", err)
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("command execution error: %w", err)
	}

	var (
		wg       sync.WaitGroup
		emitMu   sync.Mutex
		emitErr  error
		streamFn = func(r io.Reader, name string) {
			defer wg.Done()
			buf := make([]byte, 32*1024)
			for {
				n, readErr := r.Read(buf)
				if n > 0 {
					emitMu.Lock()
					if emitErr == nil {
						if cbErr := emit(ExecStreamEvent{Stream: name, Data: string(buf[:n])}); cbErr != nil {
							emitErr = cbErr
							cancel()
						}
					}
					emitMu.Unlock()
				}
				if readErr != nil {
					return
				}
			}
		}
	)
	wg.Add(2)
	go streamFn(stdout, "stdout")
	go streamFn(stderr, "stderr")
	wg.Wait()
	runErr := cmd.Wait()

	if emitErr != nil {
		return emitErr
	}

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	event := ExecStreamEvent{
		ExitCode:   &exitCode,
		DurationMs: time.Since(start).Milliseconds(),
	}
	if execCtx.Err() == context.DeadlineExceeded {
		event.Error = "command timed out"
	}
	return emit(event)
}

// BuildImage requests the image builder to compile and register a custom image with pre-installed packages
func (m *Manager) BuildImage(ctx context.Context, req image.BuildRequest) (*db.CustomImage, error) {
	if m.imgBuilder == nil {
		return nil, fmt.Errorf("image builder is not initialized")
	}
	return m.imgBuilder.BuildImage(ctx, req)
}

// ListImages lists all custom images registered in the system. Images left in
// the "building" state by a daemon crash are reconciled against the filesystem
// so a completed rootfs becomes usable immediately.
func (m *Manager) ListImages(ctx context.Context) ([]*db.CustomImage, error) {
	imgs, err := m.store.ListImages(ctx)
	if err != nil {
		return nil, err
	}
	for _, img := range imgs {
		if img.Status != db.ImageStatusBuilding {
			continue
		}
		if _, statErr := os.Stat(img.RootfsPath); statErr == nil {
			img.Status = db.ImageStatusReady
			img.UpdatedAt = time.Now().UTC()
			_ = m.store.SaveImage(ctx, img)
			continue
		}
		if time.Since(img.UpdatedAt) > 15*time.Minute {
			img.Status = db.ImageStatusFailed
			img.UpdatedAt = time.Now().UTC()
			_ = m.store.SaveImage(ctx, img)
		}
	}
	return imgs, nil
}

// GetImage retrieves a custom image by ID or Name
func (m *Manager) GetImage(ctx context.Context, nameOrID string) (*db.CustomImage, error) {
	return m.store.GetImage(ctx, nameOrID)
}

// DeleteImage deletes a custom image and cleans up its rootfs file
func (m *Manager) DeleteImage(ctx context.Context, nameOrID string) error {
	img, err := m.store.GetImage(ctx, nameOrID)
	if err != nil {
		return err
	}
	if img == nil {
		return fmt.Errorf("image '%s' not found", nameOrID)
	}

	_ = m.storMgr.DeleteImageFile(img.Name)
	if img.RootfsPath != "" {
		_ = os.Remove(img.RootfsPath)
	}
	return m.store.DeleteImage(ctx, img.ID)
}

// CreateTemplate creates a reusable template from an existing VM's disk
func (m *Manager) CreateTemplate(ctx context.Context, req CreateTemplateRequest) (*db.Template, error) {
	cleanName := strings.TrimSpace(req.Name)
	if cleanName == "" {
		return nil, fmt.Errorf("template name cannot be empty")
	}

	for _, r := range cleanName {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return nil, fmt.Errorf("invalid template name '%s': must contain only letters, numbers, dashes, underscores, and dots", cleanName)
		}
	}

	existingTpl, _ := m.store.GetTemplate(ctx, cleanName)
	if existingTpl != nil {
		return nil, fmt.Errorf("template with name '%s' already exists", cleanName)
	}
	existingImg, _ := m.store.GetImage(ctx, cleanName)
	if existingImg != nil {
		return nil, fmt.Errorf("image with name '%s' already exists", cleanName)
	}

	srcVM, err := m.store.GetVM(ctx, req.VMID)
	if err != nil || srcVM == nil {
		return nil, fmt.Errorf("source VM '%s' not found", req.VMID)
	}

	// If VM is running, flush filesystem caches inside the guest so all shell modifications are safely committed to disk
	if srcVM.Status == db.StatusRunning && srcVM.IPAddress != "" {
		syncCtx, syncCancel := context.WithTimeout(ctx, 3*time.Second)
		_, _ = m.ExecVM(syncCtx, srcVM.ID, ExecRequest{Command: "sync", User: "root", Timeout: 3})
		syncCancel()
	}

	uid := uuid.New().String()
	shortID := strings.ReplaceAll(uid, "-", "")[:8]
	tplID := fmt.Sprintf("tpl-%s", shortID)

	srcDirID := m.getVMDirID(srcVM)
	tplDiskPath, err := m.storMgr.CreateTemplateDiskFromVM(srcDirID, cleanName)
	if err != nil && srcDirID != srcVM.ID {
		tplDiskPath, err = m.storMgr.CreateTemplateDiskFromVM(srcVM.ID, cleanName)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to clone VM disk for template: %w", err)
	}

	kernelPath := srcVM.KernelPath
	if kernelPath == "" {
		kernelPath = m.storMgr.GetBaseKernelPath()
	}

	diskMB := config.DiskSize50GB
	if stat, err := os.Stat(tplDiskPath); err == nil && stat.Size() > 0 {
		diskMB = int(stat.Size() / (1024 * 1024))
	}

	tpl := &db.Template{
		ID:          tplID,
		Name:        cleanName,
		Description: req.Description,
		SourceVMID:  srcVM.ID,
		DiskPath:    tplDiskPath,
		KernelPath:  kernelPath,
		VCPUs:       srcVM.VCPUs,
		MemoryMB:    srcVM.MemoryMB,
		DiskMB:      diskMB,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	if err := m.store.SaveTemplate(ctx, tpl); err != nil {
		_ = os.Remove(tplDiskPath)
		return nil, fmt.Errorf("failed to save template record: %w", err)
	}

	return tpl, nil
}

// ListTemplates returns all stored templates
func (m *Manager) ListTemplates(ctx context.Context) ([]*db.Template, error) {
	return m.store.ListTemplates(ctx)
}

// GetTemplate retrieves a template by ID or Name
func (m *Manager) GetTemplate(ctx context.Context, nameOrID string) (*db.Template, error) {
	return m.store.GetTemplate(ctx, nameOrID)
}

// DeleteTemplate removes a template and deletes its disk image
func (m *Manager) DeleteTemplate(ctx context.Context, nameOrID string) error {
	tpl, err := m.store.GetTemplate(ctx, nameOrID)
	if err != nil {
		return err
	}
	if tpl == nil {
		return fmt.Errorf("template '%s' not found", nameOrID)
	}

	_ = m.storMgr.DeleteTemplateFile(tpl.Name)
	_ = m.storMgr.DeleteTemplateFile(tpl.ID)
	if tpl.DiskPath != "" {
		_ = os.Remove(tpl.DiskPath)
	}
	return m.store.DeleteTemplate(ctx, tpl.ID)
}

// LaunchTemplate provisions and starts a new VM from a template
func (m *Manager) LaunchTemplate(ctx context.Context, templateNameOrID string, req CreateRequest) (*db.VM, error) {
	req.Template = templateNameOrID
	created, err := m.CreateVM(ctx, req)
	if err != nil {
		return nil, err
	}

	if err := m.RunVM(ctx, created.ID); err != nil {
		return created, fmt.Errorf("VM created but failed to start: %w", err)
	}

	v, _ := m.GetVM(ctx, created.ID)
	if v != nil {
		return v, nil
	}
	return created, nil
}

// GetStats returns summary metrics for the orchestrator
func (m *Manager) GetStats(ctx context.Context) (*StatsResult, error) {
	vms, err := m.store.ListVMs(ctx)
	if err != nil {
		return nil, err
	}
	stats := &StatsResult{
		TotalVMs:              len(vms),
		HostCPUs:              runtime.NumCPU(),
		HostMemoryAvailableMB: hostMemoryAvailableMB(),
		ResourceLimits:        &m.resourceLimits,
	}
	for _, v := range vms {
		stats.TotalVCPUs += v.VCPUs
		stats.TotalMemoryMB += v.MemoryMB
		switch v.Status {
		case db.StatusRunning:
			stats.RunningVMs++
			stats.RunningVCPUs += v.VCPUs
			stats.RunningMemoryMB += v.MemoryMB
		case db.StatusQueued:
			stats.QueuedVMs++
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

// sanitizeFirecrackerID returns an instance ID accepted by Firecracker
// (alphanumerics and hyphens only, maximum 64 characters).
func sanitizeFirecrackerID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" {
		out = "vm"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
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
