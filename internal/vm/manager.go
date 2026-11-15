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
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/dns"
	"github.com/Rashid2G/on-demand-vms/internal/firecracker"
	"github.com/Rashid2G/on-demand-vms/internal/image"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	jailer "github.com/Rashid2G/on-demand-vms/internal/node/runtime/firecracker"
	"github.com/Rashid2G/on-demand-vms/internal/storage"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

type Config struct {
	FirecrackerBinary   string
	JailerBinary        string
	JailerEnabled       bool
	JailerBaseDir       string
	JailerUID           int
	JailerGID           int
	JailerNetNS         string
	JailerDevFallback   bool
	JailerUseNamespaces bool
	CgroupParent        string
	CgroupEnforce       bool
	CgroupDevOverride   bool
	Defaults            config.DefaultsConfig
	Resources           config.ResourceLimits
}

type Manager struct {
	cfg                   Config
	store                 *db.Store
	netMgr                *network.Manager
	storMgr               *storage.Manager
	imgBuilder            *image.Builder
	cgroupMgr             *CgroupManager
	cgroupEnforceExplicit bool
	defaults              config.DefaultsConfig
	resourceLimits        config.ResourceLimits
	runQueue              []string
	queueReasons          map[string]string
	queueTimes            map[string]time.Time
	queueMu               sync.Mutex
	processChecker        func(pid int) bool
	launcher              func(ctx context.Context, vm *db.VM) error
	guestSyncer           func(ctx context.Context, vm *db.VM) error
	mu                    sync.RWMutex
	vms                   map[string]*db.VM
	templates             map[string]*db.Template
	images                map[string]*db.CustomImage
	vmLocks               sync.Map // canonicalKey -> *sync.RWMutex
}

func NewManager(cfg Config, store *db.Store, netMgr *network.Manager, storMgr *storage.Manager) *Manager {
	if cfg.FirecrackerBinary == "" {
		cfg.FirecrackerBinary = "/usr/local/bin/firecracker"
	}
	if cfg.JailerBinary == "" {
		cfg.JailerBinary = "/usr/local/bin/jailer"
	}
	if cfg.JailerBaseDir == "" {
		cfg.JailerBaseDir = "/srv/jailer"
	}
	if cfg.JailerUID <= 0 {
		cfg.JailerUID = 1000
	}
	if cfg.JailerGID <= 0 {
		cfg.JailerGID = cfg.JailerUID
	}
	if cfg.CgroupParent == "" {
		cfg.CgroupParent = "/sys/fs/cgroup/compute"
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

	cgroupMgr := NewCgroupManager(cfg.CgroupParent)

	m := &Manager{
		cfg:            cfg,
		store:          store,
		netMgr:         netMgr,
		storMgr:        storMgr,
		imgBuilder:     imgBuilder,
		cgroupMgr:      cgroupMgr,
		defaults:       cfg.Defaults,
		resourceLimits: cfg.Resources,
		queueReasons:   make(map[string]string),
		queueTimes:     make(map[string]time.Time),
		vms:            make(map[string]*db.VM),
		templates:      make(map[string]*db.Template),
		images:         make(map[string]*db.CustomImage),
	}

	if netMgr != nil && store != nil {
		netMgr.SetStore(store)
	}

	if store != nil {
		if list, err := store.ListVMs(context.Background()); err == nil {
			for _, v := range list {
				m.vms[v.ID] = v
				if v.InternalID != "" {
					m.vms[v.InternalID] = v
				}
			}
		}
		if tpls, err := store.ListTemplates(context.Background()); err == nil {
			for _, t := range tpls {
				m.templates[t.ID] = t
				if t.InternalID != "" {
					m.templates[t.InternalID] = t
				}
			}
		}
		if imgs, err := store.ListImages(context.Background()); err == nil {
			for _, img := range imgs {
				m.images[img.ID] = img
				if img.InternalID != "" {
					m.images[img.InternalID] = img
				}
			}
		}
	}

	return m
}

func (m *Manager) getVMLock(vmID string) *sync.RWMutex {
	canonicalKey := vmID
	m.mu.RLock()
	v, ok := m.vms[vmID]
	m.mu.RUnlock()

	if (!ok || v == nil) && m.store != nil {
		if dbV, _ := m.store.GetVM(context.Background(), vmID); dbV != nil {
			m.mu.Lock()
			m.vms[dbV.ID] = dbV
			if dbV.InternalID != "" {
				m.vms[dbV.InternalID] = dbV
			}
			m.mu.Unlock()
			v = dbV
		}
	}

	if v != nil {
		if v.InternalID != "" {
			canonicalKey = v.InternalID
		} else if v.ID != "" {
			canonicalKey = v.ID
		}
	}

	val, _ := m.vmLocks.LoadOrStore(canonicalKey, &sync.RWMutex{})
	return val.(*sync.RWMutex)
}

func (m *Manager) saveVMRecord(ctx context.Context, vm *db.VM) error {
	if m.store != nil {
		if err := m.store.SaveVM(ctx, vm); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.vms[vm.ID] = vm
	if vm.InternalID != "" {
		m.vms[vm.InternalID] = vm
	}
	if vm.Name != "" {
		m.vms[vm.Name] = vm
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) deleteVMRecord(ctx context.Context, vmID string) error {
	var internalID, id, name string

	m.mu.RLock()
	if v, ok := m.vms[vmID]; ok && v != nil {
		internalID = v.InternalID
		id = v.ID
		name = v.Name
	}
	m.mu.RUnlock()

	if internalID == "" && m.store != nil {
		if v, _ := m.store.GetVM(ctx, vmID); v != nil {
			internalID = v.InternalID
			id = v.ID
			name = v.Name
		}
	}

	m.mu.Lock()
	delete(m.vms, vmID)
	if internalID != "" {
		delete(m.vms, internalID)
	}
	if id != "" {
		if cur, ok := m.vms[id]; ok && cur != nil && (cur.InternalID == internalID || cur.ID == id) {
			delete(m.vms, id)
		}
	}
	if name != "" {
		if cur, ok := m.vms[name]; ok && cur != nil && cur.InternalID == internalID {
			delete(m.vms, name)
		}
	}
	m.mu.Unlock()

	if m.store != nil {
		if internalID != "" {
			return m.store.DeleteVMByInternalID(ctx, internalID)
		}
		return m.store.DeleteVM(ctx, vmID)
	}
	return nil
}

func (m *Manager) getVMUnsafe(ctx context.Context, vmID string) (*db.VM, error) {
	if m.store != nil {
		v, err := m.store.GetVM(ctx, vmID)
		if err != nil {
			return nil, err
		}
		if v != nil {
			m.mu.Lock()
			m.vms[v.ID] = v
			if v.InternalID != "" {
				m.vms[v.InternalID] = v
			}
			if v.Name != "" {
				m.vms[v.Name] = v
			}
			m.mu.Unlock()
			return v, nil
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.vms[vmID]; ok && v != nil {
		return v, nil
	}
	return nil, nil
}

// CgroupManager returns the underlying cgroup manager
func (m *Manager) CgroupManager() *CgroupManager {
	return m.cgroupMgr
}

// SetCgroupEnforce enables or disables fail-closed cgroup enforcement
func (m *Manager) SetCgroupEnforce(enforce bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cgroupEnforceExplicit = true
	m.cfg.CgroupEnforce = enforce
	if !enforce {
		m.cfg.CgroupDevOverride = true
	} else {
		m.cfg.CgroupDevOverride = false
	}
}

func (m *Manager) isCgroupEnforced() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cgroupMgr == nil {
		return false
	}
	if m.cfg.CgroupDevOverride {
		return false
	}
	if m.cgroupEnforceExplicit {
		return m.cfg.CgroupEnforce
	}
	// Cgroup v2 setup and process assignment fail closed by default when cgroupMgr is active
	return true
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.defaults
}

// GetResourceLimits returns active capacity resource limits
func (m *Manager) GetResourceLimits() config.ResourceLimits {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.resourceLimits
}

// SetResourceLimits dynamically updates capacity limits
func (m *Manager) SetResourceLimits(limits config.ResourceLimits) {
	m.mu.Lock()
	m.resourceLimits = limits
	m.mu.Unlock()
	m.processQueue(context.Background())
}

type CreateRequest struct {
	Name             string `json:"name"`
	OwnerID          string `json:"owner_id,omitempty"`
	Tier             string `json:"tier,omitempty"`       // "1vcpu", "2vcpu", "4vcpu" (or "micro", "small", "medium")
	VCPUs            int    `json:"vcpus"`                // 1, 2, or 4
	MemoryMB         int    `json:"memory_mb,omitempty"`  // Optional: default 2048, 4096, 8192
	DiskMB           int    `json:"disk_mb,omitempty"`    // 51200 (50GB) or 102400 (100GB)
	DiskGB           int    `json:"disk_gb,omitempty"`    // Optional convenience override in GB
	Description      string `json:"description,omitempty"`
	AutoStart        bool   `json:"auto_start,omitempty"`
	Image            string `json:"image,omitempty"`      // Custom image, template, or "ubuntu-24"
	Template         string `json:"template,omitempty"`   // VM Template name or ID to launch from
	CloneFrom        string `json:"clone_from,omitempty"` // VM ID to clone persistent disk from
	IPAddress        string `json:"ip_address,omitempty"`
	MACAddress       string `json:"mac_address,omitempty"`
	PublicIP         *bool  `json:"public_ip,omitempty"`
	PublicIPEnabled  *bool  `json:"public_ip_enabled,omitempty"`
	DedicatedIP      *bool  `json:"dedicated_ip,omitempty"`
	NetworkIsolation string `json:"network_isolation,omitempty"` // "account" (default) or "isolated"
	Isolated         *bool  `json:"isolated,omitempty"`          // Convenience boolean alias: true => "isolated", false => "account"
	NetworkIsolated  *bool  `json:"network_isolated,omitempty"`  // Boolean alias for isolated
	DHCP             *bool  `json:"dhcp,omitempty"`
	SSHKeyID         string `json:"ssh_key_id,omitempty"`
	SSHPublicKey     string `json:"ssh_public_key,omitempty"`
	// SSHNoDefaultKey opts out of the caller's default account key. When a
	// creation request names no key explicitly and this flag is false, the
	// control plane injects the owner's default key automatically. Clients
	// set it when the user explicitly chose "no account key" for the sandbox.
	SSHNoDefaultKey bool `json:"ssh_no_default_key,omitempty"`
	// SSHPassword is an optional password for the guest's managed SSH accounts
	// (compute and root). It is applied to the disk image at creation time and
	// never persisted. When empty, password authentication is disabled and the
	// accounts stay locked, leaving the embedded public key as the only way in.
	SSHPassword string `json:"ssh_password,omitempty"`

	Protected *bool `json:"protected,omitempty"`

	// AuthorizedOwnerIDs lists every owner whose templates, images, clone
	// sources, and SSH keys the caller may use. It is populated by the control
	// plane after authentication and is never decoded from request JSON.
	AuthorizedOwnerIDs []string `json:"-"`
	// AllowCrossOwner lets a verified administrator use resources belonging to
	// any tenant. It is set only by the control plane, never by API clients.
	AllowCrossOwner bool `json:"-"`
}

func (r *CreateRequest) UnmarshalJSON(data []byte) error {
	type Alias CreateRequest
	aux := struct {
		RawPublicIP        interface{} `json:"public_ip,omitempty"`
		RawDedicatedIP     interface{} `json:"dedicated_ip,omitempty"`
		RawIsolated        interface{} `json:"isolated,omitempty"`
		RawIsolate         interface{} `json:"isolate,omitempty"`
		RawNetworkIsolated interface{} `json:"network_isolated,omitempty"`
		RawDHCP            interface{} `json:"dhcp,omitempty"`
		RawProtected       interface{} `json:"protected,omitempty"`
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
	if b := parseBool(aux.RawIsolated); b != nil {
		r.Isolated = b
	} else if b := parseBool(aux.RawIsolate); b != nil {
		r.Isolated = b
	} else if b := parseBool(aux.RawNetworkIsolated); b != nil {
		r.Isolated = b
		r.NetworkIsolated = b
	}
	if r.DiskMB <= 0 && r.DiskGB > 0 {
		r.DiskMB = r.DiskGB * 1024
	}
	if b := parseBool(aux.RawDHCP); b != nil {
		r.DHCP = b
	}
	if b := parseBool(aux.RawProtected); b != nil {
		r.Protected = b
	}
	return nil
}

type BuildImageRequest = image.BuildRequest

type CreateTemplateRequest struct {
	Name        string `json:"name"`
	OwnerID     string `json:"owner_id,omitempty"`
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
	VMID             string `json:"vm_id"`
	VMName           string `json:"vm_name"`
	Status           string `json:"status"`
	IPAddress        string `json:"ip_address"`
	PublicIP         string `json:"public_ip,omitempty"`
	PublicIPEnabled  bool   `json:"public_ip_enabled"`
	NetworkIsolation string `json:"network_isolation"`
	MACAddress       string `json:"mac_address"`
	TAPDevice        string `json:"tap_device"`
	BridgeName       string `json:"bridge_name"`
	Gateway          string `json:"gateway"`
	Reachable        bool   `json:"reachable"`
	Connectivity     string `json:"connectivity"`
}

type QueueItem struct {
	Position int       `json:"position"`
	VMID     string    `json:"vm_id"`
	VMName   string    `json:"vm_name"`
	OwnerID  string    `json:"-"` // used for tenant-scoped queue filtering
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

// isInternalServiceOwner reports whether ownerID is an internal job service
// identity rather than a tenant. The control plane authorizes the image before
// handing the job off, so these processes may consume tenant images.
func isInternalServiceOwner(ownerID string) bool {
	switch ownerID {
	case "jobs", "worker-job":
		return true
	}
	return false
}

// resourceOwnerAllowed reports whether req may use a resource owned by
// resourceOwner. Requests coming from the control plane always carry either an
// explicit authorized owner set or the administrator cross-owner flag, so the
// legacy "default" shared-resource fallback no longer applies to tenants.
func resourceOwnerAllowed(req *CreateRequest, fallbackOwner, resourceOwner string) bool {
	if req == nil || req.AllowCrossOwner {
		return true
	}
	if fallbackOwner == "default" || isInternalServiceOwner(fallbackOwner) {
		return true
	}
	if len(req.AuthorizedOwnerIDs) > 0 {
		if resourceOwner == "" {
			return false
		}
		for _, id := range req.AuthorizedOwnerIDs {
			if id != "" && id == resourceOwner {
				return true
			}
		}
		return false
	}
	// Direct (non-HTTP) callers keep the historical single-tenant behaviour.
	return resourceOwner == "" || resourceOwner == fallbackOwner
}

// lookupTemplateForRequest resolves a template while honouring the caller's
// authorized owners. Pinned internal IDs (tpl_...) resolve through the same
// owner-scoped query because it matches internal_id as well as name.
func (m *Manager) lookupTemplateForRequest(ctx context.Context, req *CreateRequest, identifier, fallbackOwner string) (*db.Template, error) {
	if req == nil || req.AllowCrossOwner || fallbackOwner == "default" || isInternalServiceOwner(fallbackOwner) {
		return m.store.GetTemplate(ctx, identifier)
	}
	owners := req.AuthorizedOwnerIDs
	if len(owners) == 0 {
		owners = []string{fallbackOwner}
	}
	for _, ownerID := range owners {
		if ownerID == "" {
			continue
		}
		tpl, err := m.store.GetTemplateByOwner(ctx, ownerID, identifier)
		if err != nil {
			return nil, err
		}
		if tpl != nil && resourceOwnerAllowed(req, fallbackOwner, tpl.OwnerID) {
			return tpl, nil
		}
	}
	if len(req.AuthorizedOwnerIDs) == 0 && fallbackOwner != "" {
		// Records created before ownership was introduced are treated as
		// legacy single-tenant assets for direct callers only.
		if global, _ := m.store.GetTemplate(ctx, identifier); global != nil && resourceOwnerAllowed(req, fallbackOwner, global.OwnerID) {
			return global, nil
		}
	}
	return nil, nil
}

// lookupImageForRequest resolves a custom image while honouring the caller's
// authorized owners.
func (m *Manager) lookupImageForRequest(ctx context.Context, req *CreateRequest, identifier, fallbackOwner string) (*db.CustomImage, error) {
	if req == nil || req.AllowCrossOwner || fallbackOwner == "default" || isInternalServiceOwner(fallbackOwner) {
		return m.store.GetImage(ctx, identifier)
	}
	owners := req.AuthorizedOwnerIDs
	if len(owners) == 0 {
		owners = []string{fallbackOwner}
	}
	for _, ownerID := range owners {
		if ownerID == "" {
			continue
		}
		img, err := m.store.GetImageByOwner(ctx, ownerID, identifier)
		if err != nil {
			return nil, err
		}
		if img != nil && resourceOwnerAllowed(req, fallbackOwner, img.OwnerID) {
			return img, nil
		}
	}
	if len(req.AuthorizedOwnerIDs) == 0 && fallbackOwner != "" {
		if global, _ := m.store.GetImage(ctx, identifier); global != nil && resourceOwnerAllowed(req, fallbackOwner, global.OwnerID) {
			return global, nil
		}
	}
	return nil, nil
}

// lookupSSHKeyForRequest resolves an SSH key while honouring the caller's
// authorized owners. SSH key names are only unique per owner, so a global
// name lookup would be able to select another tenant's key.
func (m *Manager) lookupSSHKeyForRequest(ctx context.Context, req *CreateRequest, identifier, fallbackOwner string) (*db.SSHKey, error) {
	if req == nil || req.AllowCrossOwner || fallbackOwner == "default" {
		return m.store.GetSSHKey(ctx, identifier)
	}
	owners := req.AuthorizedOwnerIDs
	if len(owners) == 0 {
		owners = []string{fallbackOwner}
	}
	for _, ownerID := range owners {
		if ownerID == "" {
			continue
		}
		key, err := m.store.GetSSHKeyByOwner(ctx, ownerID, identifier)
		if err != nil {
			return nil, err
		}
		if key != nil && resourceOwnerAllowed(req, fallbackOwner, key.OwnerID) {
			return key, nil
		}
	}
	if len(req.AuthorizedOwnerIDs) == 0 && fallbackOwner != "" {
		if global, _ := m.store.GetSSHKey(ctx, identifier); global != nil && resourceOwnerAllowed(req, fallbackOwner, global.OwnerID) {
			return global, nil
		}
	}
	return nil, nil
}

// isPlatformBaseImage reports whether imageName resolves to a platform-provided
// base image. Custom images must never shadow these names for other tenants.
func (m *Manager) isPlatformBaseImage(imageName string) bool {
	if m.storMgr == nil || strings.TrimSpace(imageName) == "" {
		return false
	}
	_, _, err := m.storMgr.ResolveImage(imageName)
	return err == nil
}

// IsPlatformBaseImage reports whether name refers to a platform-provided base
// image (for example "ubuntu-24.04") rather than a tenant-owned custom image or
// template. Control-plane handlers use it to keep foreign custom images from
// shadowing platform image names.
func (m *Manager) IsPlatformBaseImage(name string) bool {
	return m.isPlatformBaseImage(name)
}

// lookupVMForRequest resolves a clone source VM within the caller's authorized
// owners. Callers pin internal IDs so duplicate names cannot cross tenants.
func (m *Manager) lookupVMForRequest(ctx context.Context, req *CreateRequest, identifier, fallbackOwner string) (*db.VM, error) {
	if req == nil || req.AllowCrossOwner || fallbackOwner == "default" || isInternalServiceOwner(fallbackOwner) {
		return m.store.GetVM(ctx, identifier)
	}
	owners := req.AuthorizedOwnerIDs
	if len(owners) == 0 {
		owners = []string{fallbackOwner}
	}
	for _, ownerID := range owners {
		if ownerID == "" {
			continue
		}
		v, err := m.store.GetVMByOwner(ctx, ownerID, identifier)
		if err != nil {
			return nil, err
		}
		if v != nil && resourceOwnerAllowed(req, fallbackOwner, v.OwnerID) {
			return v, nil
		}
	}
	if len(req.AuthorizedOwnerIDs) == 0 && fallbackOwner != "" {
		if global, _ := m.store.GetVM(ctx, identifier); global != nil && resourceOwnerAllowed(req, fallbackOwner, global.OwnerID) {
			return global, nil
		}
	}
	return nil, nil
}

func (m *Manager) generateNickname(ctx context.Context, ownerID string) string {
	// Pick from the 100 curated random nicknames, ensuring it does not collide for this owner
	for i := 0; i < 50; i++ {
		nick := RandomNickname()
		if m.store != nil {
			if existing, _ := m.store.GetVMByOwner(ctx, ownerID, nick); existing == nil {
				return nick
			}
		} else {
			return nick
		}
	}
	uid := strings.ReplaceAll(uuid.New().String(), "-", "")
	return fmt.Sprintf("%s-%s", RandomNickname(), uid[:4])
}

// hasExtFilesystem reports whether path contains an ext2/3/4 superblock magic.
// It is used to decide whether a failure to apply the SSH access policy must
// abort sandbox creation: placeholder disks in tests and non-ext images cannot
// be configured, while a real guest disk must never be left with its old policy.
func hasExtFilesystem(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [2]byte
	if _, err := f.ReadAt(magic[:], 1080); err != nil {
		return false
	}
	// ext superblock magic 0xEF53, stored little-endian at superblock offset 0x38.
	return magic[0] == 0x53 && magic[1] == 0xEF
}

func validateSSHPassword(password string) error {
	if password == "" {
		return nil
	}
	if utf8.RuneCountInString(password) < 8 {
		return fmt.Errorf("invalid ssh password: must be at least 8 characters")
	}
	// bcrypt only considers the first 72 bytes, so reject longer inputs rather
	// than silently truncating them.
	if len(password) > 72 {
		return fmt.Errorf("invalid ssh password: must be at most 72 bytes")
	}
	if strings.ContainsAny(password, "\x00\r\n") {
		return fmt.Errorf("invalid ssh password: must not contain NUL or newline characters")
	}
	return nil
}

func resolveNetworkIsolation(mode string, isolated *bool) (string, error) {
	trimmed := strings.TrimSpace(mode)
	if trimmed != "" {
		norm, ok := db.NormalizeNetworkIsolation(trimmed)
		if !ok {
			return "", fmt.Errorf("invalid network_isolation %q: expected %q or %q", mode, db.NetworkIsolationAccount, db.NetworkIsolationIsolated)
		}
		if isolated != nil {
			want := db.NetworkIsolationAccount
			if *isolated {
				want = db.NetworkIsolationIsolated
			}
			if norm != want {
				return "", fmt.Errorf("conflicting network isolation settings: network_isolation=%q and isolated=%t", mode, *isolated)
			}
		}
		return norm, nil
	}
	if isolated != nil {
		if *isolated {
			return db.NetworkIsolationIsolated, nil
		}
		return db.NetworkIsolationAccount, nil
	}
	return db.NetworkIsolationAccount, nil
}

func sameAccountOwners(ownerA, ownerB string) bool {
	a := strings.TrimSpace(ownerA)
	b := strings.TrimSpace(ownerB)
	if a == "" {
		a = "default"
	}
	if b == "" {
		b = "default"
	}
	if a == b {
		return true
	}
	// Match a user account (usr_<id>) with its personal organization (org_<id>).
	if strings.HasPrefix(a, "usr_") && b == "org_"+strings.TrimPrefix(a, "usr_") {
		return true
	}
	if strings.HasPrefix(b, "usr_") && a == "org_"+strings.TrimPrefix(b, "usr_") {
		return true
	}
	return false
}

func (m *Manager) listVMsForIsolation(ctx context.Context) []*db.VM {
	if m.store != nil {
		if vms, err := m.store.ListVMs(ctx); err == nil {
			return vms
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := make(map[string]bool)
	var out []*db.VM
	for _, v := range m.vms {
		if v == nil {
			continue
		}
		key := v.InternalID
		if key == "" {
			key = v.ID
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	return out
}

func (m *Manager) buildIsolationConfigForVM(targetVM *db.VM, allVMs []*db.VM) network.IsolationConfig {
	isolated := db.IsNetworkIsolated(targetVM)
	cfg := network.IsolationConfig{
		TAPDevice:        targetVM.TAPDevice,
		IPAddress:        targetVM.IPAddress,
		MACAddress:       targetVM.MACAddress,
		OwnerID:          targetVM.OwnerID,
		Isolated:         isolated,
		AllowSameAccount: !isolated,
	}
	if isolated {
		return cfg
	}
	for _, peer := range allVMs {
		if peer == nil || peer.TAPDevice == "" || peer.TAPDevice == targetVM.TAPDevice {
			continue
		}
		if peer.InternalID != "" && targetVM.InternalID != "" {
			if peer.InternalID == targetVM.InternalID {
				continue
			}
		} else if peer.ID == targetVM.ID {
			continue
		}
		if peer.Status != db.StatusRunning {
			continue
		}
		if db.IsNetworkIsolated(peer) {
			continue
		}
		if sameAccountOwners(targetVM.OwnerID, peer.OwnerID) {
			cfg.AllowedPeerTAPs = append(cfg.AllowedPeerTAPs, peer.TAPDevice)
		}
	}
	return cfg
}

func (m *Manager) applyVMNetworkIsolation(ctx context.Context, targetVM *db.VM) {
	if m.netMgr == nil || targetVM == nil || targetVM.TAPDevice == "" {
		return
	}
	allVMs := m.listVMsForIsolation(ctx)
	isoCfg := m.buildIsolationConfigForVM(targetVM, allVMs)
	_ = m.netMgr.ApplyAntiSpoofAndIsolation(ctx, isoCfg)
}

func (m *Manager) reconcileAccountNetworkIsolation(ctx context.Context, ownerID string) {
	if m.netMgr == nil {
		return
	}
	allVMs := m.listVMsForIsolation(ctx)
	for _, v := range allVMs {
		if v == nil || v.TAPDevice == "" || v.Status != db.StatusRunning {
			continue
		}
		if !sameAccountOwners(v.OwnerID, ownerID) {
			continue
		}
		isoCfg := m.buildIsolationConfigForVM(v, allVMs)
		_ = m.netMgr.ApplyAntiSpoofAndIsolation(ctx, isoCfg)
	}
}

// CreateVM reserves networking, validates disk space and images, and registers a created VM
func (m *Manager) CreateVM(ctx context.Context, req CreateRequest) (*db.VM, error) {
	uid := uuid.New().String()
	cleanU := strings.ReplaceAll(uid, "-", "")
	internalID := fmt.Sprintf("ins_%s", cleanU[:16])

	ownerID := req.OwnerID
	if ownerID == "" {
		ownerID = "default"
	}
	if err := validateSSHPassword(req.SSHPassword); err != nil {
		return nil, err
	}
	isolatedFlag := req.Isolated
	if isolatedFlag == nil {
		isolatedFlag = req.NetworkIsolated
	}
	networkIsolation, err := resolveNetworkIsolation(req.NetworkIsolation, isolatedFlag)
	if err != nil {
		return nil, err
	}
	// Internal job service processes may consume images/templates that the
	// control plane already authorized for a tenant.
	imageLookupOwner := ownerID
	if isInternalServiceOwner(ownerID) {
		imageLookupOwner = "default"
	}

	var externalID, name string
	if strings.TrimSpace(req.Name) != "" {
		name = strings.TrimSpace(req.Name)
		// Sandbox names are exposed as DNS names
		// (<sandbox>.<username>.<base-domain>), so new names must be
		// valid DNS labels. Existing sandboxes keep working: their names
		// are sanitized for DNS purposes at lookup time.
		if !dns.ValidLabel(name) {
			return nil, fmt.Errorf("invalid sandbox name %q: names must be lowercase letters, digits, and dashes (a DNS label, max %d chars) so they can be used as <name>.<username>.<base-domain> hostnames", name, dns.MaxLabelLength)
		}
		externalID = name
		existing, _ := m.store.GetVMByOwner(ctx, ownerID, name)
		if existing != nil {
			return nil, fmt.Errorf("a VM named %q already exists for owner %q", name, ownerID)
		}
	} else {
		name = m.generateNickname(ctx, ownerID)
		externalID = name
	}

	// Check if launching from a saved template. The template must belong to one
	// of the caller's authorized owners (or the caller is an administrator).
	var tpl *db.Template
	if req.Template != "" {
		var tplErr error
		tpl, tplErr = m.lookupTemplateForRequest(ctx, &req, req.Template, imageLookupOwner)
		if tplErr != nil || tpl == nil || !resourceOwnerAllowed(&req, imageLookupOwner, tpl.OwnerID) {
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
			return nil, fmt.Errorf("disk space issue: invalid tier '%s': supported tiers are '1vcpu', '2vcpu', or '4vcpu'", req.Tier)
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
	if diskMB <= 0 && req.DiskGB > 0 {
		diskMB = req.DiskGB * 1024
	}
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
			return nil, fmt.Errorf("template disk not found at %s: %w", tpl.DiskPath, err)
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
		// Clone disk from existing VM. The source must be visible to the caller.
		srcVM, err := m.lookupVMForRequest(ctx, &req, req.CloneFrom, ownerID)
		if err != nil || srcVM == nil || !resourceOwnerAllowed(&req, ownerID, srcVM.OwnerID) {
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
		// Check custom image first. Images and templates are owner-scoped so a
		// same-named resource from another tenant can never be selected.
		customImg, imgErr := m.lookupImageForRequest(ctx, &req, imageName, imageLookupOwner)
		if imgErr != nil {
			return nil, fmt.Errorf("disk space issue: failed to resolve image '%s': %w", imageName, imgErr)
		}
		if customImg != nil && !resourceOwnerAllowed(&req, imageLookupOwner, customImg.OwnerID) {
			return nil, fmt.Errorf("disk space issue: custom image '%s' not found or not accessible", imageName)
		}
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
				return nil, fmt.Errorf("custom image rootfs not found at %s: %w", customImg.RootfsPath, err)
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
			namedTpl, tplErr := m.lookupTemplateForRequest(ctx, &req, imageName, imageLookupOwner)
			if tplErr != nil {
				return nil, fmt.Errorf("disk space issue: failed to resolve template '%s': %w", imageName, tplErr)
			}
			if namedTpl != nil && !resourceOwnerAllowed(&req, imageLookupOwner, namedTpl.OwnerID) {
				return nil, fmt.Errorf("template '%s' not found", imageName)
			}
			if namedTpl != nil {
				if _, err := os.Stat(namedTpl.DiskPath); err != nil {
					return nil, fmt.Errorf("template disk not found at %s: %w", namedTpl.DiskPath, err)
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
				// A same-named resource owned by someone else must not be
				// silently replaced by a base image when the caller explicitly
				// asked for that name. Platform base image names are exempt:
				// a foreign custom image must never shadow them.
				if req.Image != "" && !m.isPlatformBaseImage(imageName) {
					if foreignImg, _ := m.store.GetImage(ctx, imageName); foreignImg != nil {
						return nil, fmt.Errorf("custom image '%s' not found or not accessible", imageName)
					}
					if foreignTpl, _ := m.store.GetTemplate(ctx, imageName); foreignTpl != nil {
						return nil, fmt.Errorf("template '%s' not found", imageName)
					}
				}
				// Resolve base image (compute supports Ubuntu 24 base image)
				var err error
				kernelPath, rootfsPath, err = m.storMgr.ResolveImage(imageName)
				if err != nil {
					return nil, err
				}
				if _, err := os.Stat(rootfsPath); err != nil {
					return nil, fmt.Errorf("base rootfs image not found at %s: %w", rootfsPath, err)
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
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(embeddedPubKey)); err != nil {
			return nil, fmt.Errorf("invalid ssh_public_key: %w", err)
		}
		embeddedKeyName = "custom"
	} else if req.SSHKeyID != "" {
		key, err := m.lookupSSHKeyForRequest(ctx, &req, req.SSHKeyID, ownerID)
		if err != nil {
			return nil, fmt.Errorf("invalid requested ssh key: %w", err)
		}
		if key != nil {
			// SSH keys follow the same owner scoping as images and templates:
			// the caller may use a key owned by their user or active org.
			if !resourceOwnerAllowed(&req, imageLookupOwner, key.OwnerID) {
				return nil, fmt.Errorf("ssh key not found: %s", req.SSHKeyID)
			}
			embeddedPubKey = key.PublicKey
			embeddedKeyName = key.Name
		} else {
			return nil, fmt.Errorf("ssh key not found: %s", req.SSHKeyID)
		}
	}

	if diskPath != "" {
		accountKeys, keyErr := m.GlobalSSHKeys(ctx, ownerID)
		if keyErr != nil {
			m.storMgr.CleanupVM(internalID)
			return nil, fmt.Errorf("failed to resolve account-wide ssh keys: %w", keyErr)
		}
		err := m.storMgr.ConfigureSSHAccess(diskPath, storage.SSHAccess{
			PublicKey:  embeddedPubKey,
			PublicKeys: accountKeys,
			Password:   req.SSHPassword,
		})
		if err != nil {
			if req.SSHPassword != "" {
				m.storMgr.CleanupVM(internalID)
				return nil, fmt.Errorf("failed to configure ssh password: %w", err)
			}
			// A real guest disk must never keep its previous password policy
			// when the sandbox was created without a password. Placeholder or
			// non-ext images used by tests and some custom images are the only
			// case where the policy cannot be applied.
			if hasExtFilesystem(diskPath) {
				m.storMgr.CleanupVM(internalID)
				return nil, fmt.Errorf("failed to enforce public-key-only SSH access: %w", err)
			}
			log.Printf("[vm-manager] %s: failed to apply SSH access configuration (non-ext disk): %v", externalID, err)
		}
	} else if req.SSHPassword != "" {
		return nil, fmt.Errorf("invalid ssh password: sandbox disk is not available")
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

	protected := false
	if req.Protected != nil {
		protected = *req.Protected
	}

	vm := &db.VM{
		ID:                 externalID,
		InternalID:         internalID,
		OwnerID:            ownerID,
		Name:               name,
		Status:             db.StatusCreated,
		Protected:          protected,
		Description:        strings.TrimSpace(req.Description),
		VCPUs:              vcpus,
		MemoryMB:           memoryMB,
		DiskMB:             diskMB,
		IPAddress:          ip,
		PublicIP:           publicIP,
		PublicIPEnabled:    publicIPEnabled,
		NetworkIsolation:   networkIsolation,
		MACAddress:         mac,
		TAPDevice:          tapDev,
		TailscaleEnabled:   false,
		RootfsPath:         rootfsPath,
		KernelPath:         kernelPath,
		DiskPath:           diskPath,
		SSHKeyPath:         sshKeyPath,
		SSHKeyName:         embeddedKeyName,
		SSHPublicKey:       embeddedPubKey,
		SSHPasswordEnabled: req.SSHPassword != "",
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
	}

	if err := m.saveVMRecord(ctx, vm); err != nil {
		m.storMgr.CleanupVM(internalID)
		m.storMgr.CleanupVM(externalID)
		return nil, fmt.Errorf("failed to save VM in DB: %w", err)
	}

	return vm, nil
}

// CloneVM clones an existing VM's disk into a new VM with optional specification overrides
func (m *Manager) CloneVM(ctx context.Context, srcVMID string, req CreateRequest) (*db.VM, error) {
	srcLock := m.getVMLock(srcVMID)
	srcLock.RLock()
	defer srcLock.RUnlock()

	req.CloneFrom = srcVMID
	srcVM, _ := m.lookupVMForRequest(ctx, &req, srcVMID, req.OwnerID)
	if srcVM == nil {
		srcVM, _ = m.getVMUnsafe(ctx, srcVMID)
	}
	if srcVM != nil {
		if req.Tier == "" && req.VCPUs <= 0 && srcVM.VCPUs > 0 {
			req.VCPUs = srcVM.VCPUs
		}
		if req.MemoryMB <= 0 && srcVM.MemoryMB > 0 {
			req.MemoryMB = srcVM.MemoryMB
		}
		if req.DiskMB <= 0 && req.DiskGB <= 0 && srcVM.DiskMB > 0 {
			req.DiskMB = srcVM.DiskMB
		}
		if req.PublicIP == nil && req.PublicIPEnabled == nil && req.DedicatedIP == nil {
			req.PublicIP = &srcVM.PublicIPEnabled
			req.PublicIPEnabled = &srcVM.PublicIPEnabled
		}
		if strings.TrimSpace(req.NetworkIsolation) == "" && req.Isolated == nil && req.NetworkIsolated == nil {
			if norm, ok := db.NormalizeNetworkIsolation(srcVM.NetworkIsolation); ok {
				req.NetworkIsolation = norm
			}
		}
		if req.Protected == nil {
			req.Protected = &srcVM.Protected
		}
	}
	return m.CreateVM(ctx, req)
}

// RunVM starts an existing VM immediately if resources permit, or queues it if host capacity is reached
func (m *Manager) RunVM(ctx context.Context, vmID string) error {
	vmLock := m.getVMLock(vmID)
	vmLock.Lock()
	defer vmLock.Unlock()

	vm, err := m.getVMUnsafe(ctx, vmID)
	if err != nil || vm == nil {
		return fmt.Errorf("vm %s not found: %w", vmID, err)
	}

	vm.DesiredRunning = true
	if vm.Status == db.StatusRunning && m.isAlive(vm.PID) {
		return m.saveVMRecord(ctx, vm) // Already running; persist desired state
	}

	// Capacity check: if host resources are maxed out, queue the run
	canRun, reason := m.canRunVM(vm)
	if !canRun {
		vm.Status = db.StatusQueued
		m.enqueue(vm.ID, reason)
		if err := m.saveVMRecord(ctx, vm); err != nil {
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
	if vm.PID > 0 {
		go m.watchProcessExit(vm.ID, vm.PID, nil)
	}
	if err := m.saveVMRecord(ctx, vm); err != nil {
		return err
	}
	m.reconcileAccountNetworkIsolation(ctx, vm.OwnerID)
	m.maybeSyncSSHKeys(vm)
	return nil
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

	// Apply L2/L3 Anti-Spoofing and Bridge Port Isolation
	m.applyVMNetworkIsolation(ctx, vm)

	// 2. Setup Cgroups v2 resource limits
	if m.cgroupMgr != nil {
		limits := CgroupLimits{
			VCPUs:                vm.VCPUs,
			MemoryMB:             vm.MemoryMB,
			MemorySafetyBufferMB: 64,
			MemoryHighBufferMB:   32,
			PidsMax:              512,
		}
		if _, cgErr := m.cgroupMgr.Setup(vm.ID, limits); cgErr != nil {
			log.Printf("[vm-manager] Warning: cgroups v2 setup for vm %s: %v", vm.ID, cgErr)
			if m.isCgroupEnforced() {
				if m.netMgr != nil {
					_ = m.netMgr.RemoveAntiSpoofAndIsolation(ctx, network.IsolationConfig{TAPDevice: vm.TAPDevice})
					_ = m.netMgr.RemoveTAP(ctx, vm.TAPDevice)
				}
				return fmt.Errorf("failed to setup cgroup limits: %w", cgErr)
			}
		}
	}

	// 3. Setup paths
	vmDir := m.getVMStorageDir(vm)
	sockPath := filepath.Join(vmDir, "firecracker.sock")
	logPath := filepath.Join(vmDir, "firecracker.log")

	// Ensure cleanup on failure rollback
	var success bool
	var cmd *exec.Cmd
	var jailerProc *jailer.InstanceProcess
	defer func() {
		if !success {
			if jailerProc != nil {
				_ = jailerProc.Stop(2 * time.Second)
				_ = jailerProc.Cleanup()
			}
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			if m.cgroupMgr != nil {
				_ = m.cgroupMgr.Teardown(vm.ID)
			}
			if m.netMgr != nil {
				_ = m.netMgr.RemoveAntiSpoofAndIsolation(ctx, network.IsolationConfig{TAPDevice: vm.TAPDevice})
				_ = m.netMgr.RemoveTAP(ctx, vm.TAPDevice)
			}
			_ = os.Remove(sockPath)
		}
	}()

	// Resolve boot arguments
	gwIP := m.netMgr.GetGatewayIP()
	netmask := m.netMgr.GetNetmask()
	var bootArgs string
	if vm.IPAddress == "dhcp" || vm.IPAddress == "" {
		bootArgs = "console=ttyS0 reboot=k panic=1 pci=off ip=dhcp root=/dev/vda rw quiet"
	} else {
		bootArgs = fmt.Sprintf("console=ttyS0 reboot=k panic=1 pci=off ip=%s::%s:%s:%s:eth0:off root=/dev/vda rw quiet", vm.IPAddress, gwIP, netmask, vm.ID)
	}

	// If VM still has "dhcp" as IP, try resolving lease before booting Firecracker
	if vm.IPAddress == "dhcp" || vm.IPAddress == "" {
		if leasedIP, err := m.netMgr.ResolveGuestIPByMAC(ctx, vm.MACAddress); err == nil && leasedIP != "" {
			vm.IPAddress = leasedIP
			if vm.PublicIPEnabled {
				vm.PublicIP = leasedIP
			}
			_ = m.saveVMRecord(ctx, vm)
		}
	}

	// 4. Check if Jailer is enabled
	if m.cfg.JailerEnabled {
		if !jailer.IsJailerAvailable(m.cfg.JailerBinary) {
			if !m.cfg.JailerDevFallback {
				return fmt.Errorf("jailer is enabled but jailer binary is unavailable at %s (fail-closed)", m.cfg.JailerBinary)
			}
			log.Printf("[vm-manager] Warning: jailer enabled but unavailable, falling back to un-jailed launch (dev override)")
		} else {
			var extraDrives []jailer.ExtraDrive
			if vols, err := m.volumesForVM(ctx, vm); err == nil {
				for i, vol := range vols {
					if i >= 25 {
						break
					}
					extraDrives = append(extraDrives, jailer.ExtraDrive{
						DriveID:    fmt.Sprintf("data%d", i),
						HostPath:   vol.Path,
						IsReadOnly: false,
					})
					vol.DevicePath = fmt.Sprintf("/dev/vd%c", 'b'+rune(i))
					_ = m.store.SaveVolume(ctx, vol)
				}
			}

			jOpts := jailer.JailerOptions{
				ID:            sanitizeFirecrackerID(vm.ID),
				Firecracker:   m.cfg.FirecrackerBinary,
				Jailer:        m.cfg.JailerBinary,
				ChrootBaseDir: m.cfg.JailerBaseDir,
				UID:           m.cfg.JailerUID,
				GID:           m.cfg.JailerGID,
				VCPUs:         vm.VCPUs,
				MemoryMB:      vm.MemoryMB,
				KernelSrcPath: vm.KernelPath,
				RootfsSrcPath: vm.DiskPath,
				BootArgs:      bootArgs,
				TapName:       vm.TAPDevice,
				MAC:           vm.MACAddress,
				ExtraDrives:   extraDrives,
				UseNamespaces: os.Geteuid() == 0 || m.cfg.JailerUseNamespaces,
				NetNS:         m.cfg.JailerNetNS,
			}

			ip, jErr := jailer.LaunchJailer(ctx, jOpts)
			if jErr == nil {
				jailerProc = ip
				cmd = ip.Cmd
				sockPath = ip.APISock

				// Assign process to Cgroups v2
				if m.cgroupMgr != nil && ip.PID > 0 {
					if aErr := m.cgroupMgr.Assign(vm.ID, ip.PID); aErr != nil {
						log.Printf("[vm-manager] Warning: cgroups assign for jailed vm %s: %v", vm.ID, aErr)
						if m.isCgroupEnforced() {
							return fmt.Errorf("failed to assign jailed process to cgroup: %w", aErr)
						}
					}
				}

				// Wait for socket to become active
				if err := firecracker.WaitForSocket(sockPath, 5*time.Second); err != nil {
					return fmt.Errorf("jailer firecracker socket not ready at %s: %w", sockPath, err)
				}

				success = true
				vm.PID = ip.PID
				vm.SocketPath = sockPath
				_ = m.netMgr.ConfigureVMPublicIP(ctx, vm.IPAddress, vm.TAPDevice, vm.PublicIPEnabled)
				if vm.IPAddress == "dhcp" || vm.IPAddress == "" {
					go m.pollDHCPLease(vm.ID, vm.MACAddress, vm.PublicIPEnabled)
				}
				go m.watchProcessExit(vm.ID, vm.PID, cmd)
				return nil
			}

			if !m.cfg.JailerDevFallback {
				return fmt.Errorf("failed to launch inside jailer: %w (fail-closed)", jErr)
			}
			log.Printf("[vm-manager] Warning: failed to launch inside Jailer (%v), falling back to un-jailed launch (dev override)", jErr)
		}
	}

	// 5. Un-jailed Firecracker launch (fallback or dev mode)
	var err error
	cmd, err = firecracker.LaunchProcess(m.cfg.FirecrackerBinary, sockPath, sanitizeFirecrackerID(vm.ID), logPath)
	if err != nil {
		return fmt.Errorf("failed to launch firecracker process: %w", err)
	}

	// Assign process to Cgroups v2
	if m.cgroupMgr != nil && cmd.Process != nil {
		if aErr := m.cgroupMgr.Assign(vm.ID, cmd.Process.Pid); aErr != nil {
			log.Printf("[vm-manager] Warning: cgroups assign for vm %s: %v", vm.ID, aErr)
			if m.isCgroupEnforced() {
				return fmt.Errorf("failed to assign process to cgroup: %w", aErr)
			}
		}
	}

	// Wait for socket
	if err := firecracker.WaitForSocket(sockPath, 5*time.Second); err != nil {
		return fmt.Errorf("firecracker socket not ready: %w", err)
	}

	// 6. Configure via Firecracker API
	fcClient := firecracker.NewClient(sockPath)

	// Machine Config
	if err := fcClient.SetMachineConfig(ctx, firecracker.MachineConfig{
		VCPUCount:  vm.VCPUs,
		MemSizeMiB: vm.MemoryMB,
	}); err != nil {
		return fmt.Errorf("failed to set machine config: %w", err)
	}

	// Boot source with exact netmask and gateway
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
	go m.watchProcessExit(vm.ID, vm.PID, cmd)
	return nil
}

func (m *Manager) pollDHCPLease(vmID, mac string, publicIPEnabled bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	checkLease := func() bool {
		if leasedIP, err := m.netMgr.ResolveGuestIPByMAC(ctx, mac); err == nil && leasedIP != "" {
			vmLock := m.getVMLock(vmID)
			vmLock.Lock()
			vm, _ := m.getVMUnsafe(context.Background(), vmID)
			if vm != nil && (vm.IPAddress == "dhcp" || vm.IPAddress == "") {
				vm.IPAddress = leasedIP
				if publicIPEnabled {
					vm.PublicIP = leasedIP
				}
				_ = m.saveVMRecord(context.Background(), vm)
				_ = m.netMgr.ConfigureVMPublicIP(context.Background(), leasedIP, vm.TAPDevice, publicIPEnabled)
				m.reconcileAccountNetworkIsolation(context.Background(), vm.OwnerID)
			}
			vmLock.Unlock()
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
	if m.launcher == nil && os.Geteuid() == 0 {
		if available := hostMemoryAvailableMB(); available > 0 && candidate.MemoryMB+hostReserveMB > available {
			return false, fmt.Sprintf("insufficient host memory (requested %d MB + %d MB reserve, %d MB available)", candidate.MemoryMB, hostReserveMB, available)
		}
	}

	return true, ""
}

func (m *Manager) enqueue(vmID, reason string) {
	m.queueMu.Lock()
	defer m.queueMu.Unlock()
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
	m.queueMu.Lock()
	defer m.queueMu.Unlock()
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
	m.queueMu.Lock()
	if len(m.runQueue) == 0 {
		m.queueMu.Unlock()
		return
	}
	queuedIDs := append([]string(nil), m.runQueue...)
	m.queueMu.Unlock()

	var remaining []string
	for _, id := range queuedIDs {
		vmLock := m.getVMLock(id)
		vmLock.Lock()

		v, err := m.getVMUnsafe(ctx, id)
		if err != nil || v == nil || v.Status != db.StatusQueued {
			vmLock.Unlock()
			m.queueMu.Lock()
			delete(m.queueReasons, id)
			delete(m.queueTimes, id)
			m.queueMu.Unlock()
			continue
		}

		canRun, _ := m.canRunVM(v)
		if canRun {
			if err := m.startVMProcess(ctx, v); err == nil {
				v.Status = db.StatusRunning
				_ = m.saveVMRecord(ctx, v)
				m.reconcileAccountNetworkIsolation(ctx, v.OwnerID)
				vmLock.Unlock()
				m.queueMu.Lock()
				delete(m.queueReasons, id)
				delete(m.queueTimes, id)
				m.queueMu.Unlock()
				continue
			} else {
				v.Status = db.StatusFailed
				_ = m.saveVMRecord(ctx, v)
				vmLock.Unlock()
				m.queueMu.Lock()
				delete(m.queueReasons, id)
				delete(m.queueTimes, id)
				m.queueMu.Unlock()
				continue
			}
		}

		vmLock.Unlock()
		remaining = append(remaining, id)
	}

	m.queueMu.Lock()
	queuedSet := make(map[string]struct{}, len(queuedIDs))
	for _, id := range queuedIDs {
		queuedSet[id] = struct{}{}
	}
	remainingSet := make(map[string]struct{}, len(remaining))
	for _, id := range remaining {
		remainingSet[id] = struct{}{}
	}
	for _, id := range m.runQueue {
		if _, wasProcessed := queuedSet[id]; wasProcessed {
			continue
		}
		if _, alreadyRemaining := remainingSet[id]; alreadyRemaining {
			continue
		}
		remaining = append(remaining, id)
		remainingSet[id] = struct{}{}
	}
	m.runQueue = remaining
	m.queueMu.Unlock()
}

// RecoverQueue recovers queued VMs from database state and attempts to launch them
func (m *Manager) RecoverQueue(ctx context.Context) error {
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
	m.queueMu.Lock()
	defer m.queueMu.Unlock()

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
				OwnerID:  v.OwnerID,
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
	vmLock := m.getVMLock(vmID)
	vmLock.Lock()

	vm, err := m.getVMUnsafe(ctx, vmID)
	if err != nil || vm == nil {
		vmLock.Unlock()
		return fmt.Errorf("vm %s not found: %w", vmID, err)
	}

	if vm.Status != db.StatusRunning || !m.isAlive(vm.PID) {
		vmLock.Unlock()
		return fmt.Errorf("vm %s is not running (status: %s)", vmID, vm.Status)
	}

	fcClient := firecracker.NewClient(vm.SocketPath)

	// 1. Pause VM
	if err := fcClient.Pause(ctx); err != nil {
		vmLock.Unlock()
		return fmt.Errorf("failed to pause vm: %w", err)
	}

	// 2. Snapshot
	snapPath, memPath := m.storMgr.GetSnapshotPaths(m.getVMDirID(vm))
	os.Remove(snapPath)
	os.Remove(memPath)

	if err := fcClient.CreateSnapshot(ctx, snapPath, memPath); err != nil {
		fcClient.Resume(ctx)
		vmLock.Unlock()
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

	if err := m.saveVMRecord(ctx, vm); err != nil {
		vmLock.Unlock()
		return err
	}
	m.reconcileAccountNetworkIsolation(ctx, vm.OwnerID)

	vmLock.Unlock()
	m.processQueue(ctx)
	return nil
}

// ResumeVM restores a paused/snapshotted VM
func (m *Manager) ResumeVM(ctx context.Context, vmID string) error {
	vmLock := m.getVMLock(vmID)
	vmLock.Lock()
	defer vmLock.Unlock()

	vm, err := m.getVMUnsafe(ctx, vmID)
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
		return m.saveVMRecord(ctx, vm)
	}

	// 1. Ensure Network & TAP
	if err := m.netMgr.EnsureNetwork(ctx); err != nil {
		return fmt.Errorf("failed to ensure network: %w", err)
	}
	if err := m.netMgr.SetupTAP(ctx, vm.TAPDevice); err != nil {
		return fmt.Errorf("failed to setup TAP device: %w", err)
	}
	m.applyVMNetworkIsolation(ctx, vm)

	// 2. Setup Cgroups v2 resource limits
	if m.cgroupMgr != nil {
		limits := CgroupLimits{
			VCPUs:                vm.VCPUs,
			MemoryMB:             vm.MemoryMB,
			MemorySafetyBufferMB: 64,
			MemoryHighBufferMB:   32,
			PidsMax:              512,
		}
		if _, cgErr := m.cgroupMgr.Setup(vm.ID, limits); cgErr != nil {
			log.Printf("[vm-manager] Warning: cgroups v2 setup for resumed vm %s: %v", vm.ID, cgErr)
			if m.isCgroupEnforced() {
				_ = m.netMgr.RemoveTAP(ctx, vm.TAPDevice)
				return fmt.Errorf("failed to setup cgroup limits for resumed vm: %w", cgErr)
			}
		}
	}

	// 3. Launch new Firecracker process
	vmDir := m.getVMStorageDir(vm)
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return fmt.Errorf("failed to create vm directory %s: %w", vmDir, err)
	}
	sockPath := filepath.Join(vmDir, "firecracker.sock")
	logPath := filepath.Join(vmDir, "firecracker.log")

	var success bool
	var cmd *exec.Cmd
	var jailerProc *jailer.InstanceProcess
	snapPathToLoad := vm.SnapshotPath
	memPathToLoad := vm.MemFilePath

	defer func() {
		if !success {
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			if jailerProc != nil {
				_ = jailerProc.Cleanup()
			}
			if m.cgroupMgr != nil {
				_ = m.cgroupMgr.Teardown(vm.ID)
			}
			_ = m.netMgr.RemoveAntiSpoofAndIsolation(ctx, network.IsolationConfig{TAPDevice: vm.TAPDevice})
			_ = m.netMgr.RemoveTAP(ctx, vm.TAPDevice)
			_ = os.Remove(sockPath)
		}
	}()

	fcID := vm.InternalID
	if fcID == "" {
		fcID = vm.ID
	}

	if m.cfg.JailerEnabled {
		if !jailer.IsJailerAvailable(m.cfg.JailerBinary) {
			if !m.cfg.JailerDevFallback {
				return fmt.Errorf("jailer is enabled but jailer binary is unavailable at %s (fail-closed)", m.cfg.JailerBinary)
			}
			log.Printf("[vm-manager] Warning: jailer enabled but unavailable, falling back to un-jailed resume (dev override)")
		} else {
			var extraDrives []jailer.ExtraDrive
			if vols, err := m.volumesForVM(ctx, vm); err == nil {
				for i, vol := range vols {
					if i >= 25 {
						break
					}
					extraDrives = append(extraDrives, jailer.ExtraDrive{
						DriveID:    fmt.Sprintf("data%d", i),
						HostPath:   vol.Path,
						IsReadOnly: false,
					})
				}
			}

			jOpts := jailer.JailerOptions{
				ID:              sanitizeFirecrackerID(fcID),
				Firecracker:     m.cfg.FirecrackerBinary,
				Jailer:          m.cfg.JailerBinary,
				ChrootBaseDir:   m.cfg.JailerBaseDir,
				UID:             m.cfg.JailerUID,
				GID:             m.cfg.JailerGID,
				VCPUs:           vm.VCPUs,
				MemoryMB:        vm.MemoryMB,
				RootfsSrcPath:   vm.DiskPath,
				SnapshotSrcPath: vm.SnapshotPath,
				MemFileSrcPath:  vm.MemFilePath,
				IsResume:        true,
				TapName:         vm.TAPDevice,
				MAC:             vm.MACAddress,
				ExtraDrives:     extraDrives,
				UseNamespaces:   os.Geteuid() == 0 || m.cfg.JailerUseNamespaces,
				NetNS:           m.cfg.JailerNetNS,
			}

			ip, jErr := jailer.LaunchJailer(ctx, jOpts)
			if jErr == nil {
				jailerProc = ip
				cmd = ip.Cmd
				sockPath = ip.APISock
				snapPathToLoad = ip.SnapshotInChroot
				memPathToLoad = ip.MemFileInChroot
			} else {
				if !m.cfg.JailerDevFallback {
					return fmt.Errorf("failed to resume inside jailer: %w (fail-closed)", jErr)
				}
				log.Printf("[vm-manager] Warning: failed to resume inside Jailer (%v), falling back to un-jailed resume (dev override)", jErr)
			}
		}
	}

	if jailerProc == nil {
		var launchErr error
		cmd, launchErr = firecracker.LaunchProcess(m.cfg.FirecrackerBinary, sockPath, sanitizeFirecrackerID(fcID), logPath)
		if launchErr != nil {
			return fmt.Errorf("failed to launch firecracker process: %w", launchErr)
		}
	}

	// Assign resumed process to Cgroups v2
	if m.cgroupMgr != nil && cmd.Process != nil {
		if aErr := m.cgroupMgr.Assign(vm.ID, cmd.Process.Pid); aErr != nil {
			log.Printf("[vm-manager] Warning: cgroups assign for resumed vm %s: %v", vm.ID, aErr)
			if m.isCgroupEnforced() {
				return fmt.Errorf("failed to assign resumed process to cgroup: %w", aErr)
			}
		}
	}

	if err := firecracker.WaitForSocket(sockPath, 5*time.Second); err != nil {
		return fmt.Errorf("firecracker socket not ready: %w", err)
	}

	// 4. Load Snapshot with resume=true
	fcClient := firecracker.NewClient(sockPath)
	if err := fcClient.LoadSnapshot(ctx, snapPathToLoad, memPathToLoad, true); err != nil {
		return fmt.Errorf("failed to load snapshot: %w", err)
	}

	success = true
	m.dequeue(vm.ID)
	vm.Status = db.StatusRunning
	vm.PID = cmd.Process.Pid
	vm.SocketPath = sockPath
	vm.DesiredRunning = true
	_ = m.netMgr.ConfigureVMPublicIP(ctx, vm.IPAddress, vm.TAPDevice, vm.PublicIPEnabled)
	go m.watchProcessExit(vm.ID, vm.PID, cmd)
	if err := m.saveVMRecord(ctx, vm); err != nil {
		return err
	}
	m.reconcileAccountNetworkIsolation(ctx, vm.OwnerID)
	m.maybeSyncSSHKeys(vm)
	return nil
}

// StopVM gracefully shuts down a running VM, flushing caches, and cleaning runtime resources
func (m *Manager) StopVM(ctx context.Context, vmID string) error {
	log.Printf("[vm] StopVM invoked for %s", vmID)
	vmLock := m.getVMLock(vmID)
	vmLock.Lock()

	err := m.stopVMUnlocked(ctx, vmID)
	vmLock.Unlock()

	if err == nil {
		m.processQueue(ctx)
	}
	return err
}

func (m *Manager) stopVMUnlocked(ctx context.Context, vmID string) error {
	vm, err := m.getVMUnsafe(ctx, vmID)
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

	// Teardown Cgroups v2
	if m.cgroupMgr != nil {
		_ = m.cgroupMgr.Teardown(vm.ID)
		if vm.InternalID != "" {
			_ = m.cgroupMgr.Teardown(vm.InternalID)
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
	if err := m.saveVMRecord(ctx, vm); err != nil {
		return err
	}
	m.reconcileAccountNetworkIsolation(ctx, vm.OwnerID)
	return nil
}

// StopVMForOwner stops only the VM belonging to ownerID. It is used by the
// control plane so a tenant cannot stop another tenant's same-named VM.
func (m *Manager) StopVMForOwner(ctx context.Context, ownerID, vmID string) error {
	if ownerID == "" {
		return m.StopVM(ctx, vmID)
	}
	vm, err := m.store.GetVMByOwner(ctx, ownerID, vmID)
	if err != nil {
		return err
	}
	if vm == nil || vm.OwnerID != ownerID {
		return fmt.Errorf("vm %s not found", vmID)
	}
	key := vm.InternalID
	if key == "" {
		key = vm.ID
	}
	return m.StopVM(ctx, key)
}

// DeleteVMForOwner stops and deletes only the VM belonging to ownerID. The
// lookup is scoped to the owner so a same-named VM owned by another tenant is
// never touched.
func (m *Manager) DeleteVMForOwner(ctx context.Context, ownerID, vmID string) error {
	if ownerID == "" {
		return m.DeleteVM(ctx, vmID)
	}
	vm, err := m.store.GetVMByOwner(ctx, ownerID, vmID)
	if err != nil {
		return err
	}
	if vm == nil || vm.OwnerID != ownerID {
		return fmt.Errorf("vm %s not found", vmID)
	}
	if vm.Protected {
		displayName := vm.Name
		if displayName == "" {
			displayName = vmID
		}
		return fmt.Errorf("item %s is protected against deletion", displayName)
	}
	key := vm.InternalID
	if key == "" {
		key = vm.ID
	}
	return m.DeleteVM(ctx, key)
}

// DeleteVM stops the VM, cleans up disks, and deletes the record from DB
func (m *Manager) DeleteVM(ctx context.Context, vmID string) error {
	vmLock := m.getVMLock(vmID)
	vmLock.Lock()

	vm, _ := m.getVMUnsafe(ctx, vmID)
	if vm != nil && vm.Protected {
		displayName := vm.Name
		if displayName == "" {
			displayName = vmID
		}
		vmLock.Unlock()
		return fmt.Errorf("item %s is protected against deletion", displayName)
	}
	if vm != nil {
		_ = m.netMgr.ConfigureVMPublicIP(ctx, vm.IPAddress, vm.TAPDevice, false)
	}

	// Always stop using the exact record we resolved. Internal IDs are unique
	// across tenants, unlike names/IDs which can collide.
	stopKey := vmID
	if vm != nil {
		stopKey = vm.InternalID
		if stopKey == "" {
			stopKey = vm.ID
		}
	}
	_ = m.stopVMUnlocked(ctx, stopKey)

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

	// Teardown cgroup
	if m.cgroupMgr != nil {
		_ = m.cgroupMgr.Teardown(vmID)
		if vm != nil && vm.InternalID != "" {
			_ = m.cgroupMgr.Teardown(vm.InternalID)
		}
	}

	// Clean up chroot jail directory if jailer was used
	if vm != nil {
		_ = os.RemoveAll(filepath.Join(m.cfg.JailerBaseDir, "firecracker", sanitizeFirecrackerID(vm.ID)))
		if vm.InternalID != "" {
			_ = os.RemoveAll(filepath.Join(m.cfg.JailerBaseDir, "firecracker", sanitizeFirecrackerID(vm.InternalID)))
		}
	}

	var deleteErr error
	if vm != nil {
		if vm.InternalID != "" {
			_ = m.storMgr.CleanupVM(vm.InternalID)
			deleteErr = m.deleteVMRecord(ctx, vm.InternalID)
		} else {
			_ = m.storMgr.CleanupVM(vm.ID)
			deleteErr = m.deleteVMRecord(ctx, vm.ID)
		}
	} else {
		_ = m.storMgr.CleanupVM(vmID)
		deleteErr = m.deleteVMRecord(ctx, vmID)
	}

	vmLock.Unlock()
	if deleteErr != nil {
		return deleteErr
	}

	m.processQueue(ctx)
	return nil
}

// ReconcileAndPrune synchronizes database records with live system processes,
// cleans up stale state from dead microVMs, and removes orphaned TAP interfaces.
func (m *Manager) ReconcileAndPrune(ctx context.Context) ([]string, error) {
	log.Printf("[vm] ReconcileAndPrune invoked")
	m.mu.Lock()

	vms, err := m.store.ListVMs(ctx)
	if err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("failed to list VMs for reconciliation: %w", err)
	}

	activeTAPs := make(map[string]bool)
	knownTAPs := make(map[string]bool)
	reconcileOwners := make(map[string]bool)
	for _, v := range vms {
		if v.TAPDevice != "" {
			knownTAPs[v.TAPDevice] = true
		}
		switch v.Status {
		case db.StatusRunning:
			if !m.isAlive(v.PID) {
				log.Printf("[vm] reconcile marking %s stopped: pid=%d not alive", v.ID, v.PID)
				v.Status = db.StatusStopped
				v.PID = 0
				if cached, ok := m.vms[v.ID]; ok && cached != nil {
					cached.Status = db.StatusStopped
					cached.PID = 0
				}
				if v.InternalID != "" {
					if cached, ok := m.vms[v.InternalID]; ok && cached != nil {
						cached.Status = db.StatusStopped
						cached.PID = 0
					}
				}
				_ = m.store.SaveVM(ctx, v)
				_ = m.netMgr.RemoveTAP(ctx, v.TAPDevice)
				owner := v.OwnerID
				if owner == "" {
					owner = "default"
				}
				reconcileOwners[owner] = true
			} else {
				activeTAPs[v.TAPDevice] = true
			}
		case db.StatusCreated, db.StatusQueued:
			// A sandbox moves from "created" to "running" only after Firecracker
			// has booted. RunVM creates the TAP before that transition, so
			// pruning it here would delete the device out from under a booting
			// VM ("Setting tap interface offload flags failed: File descriptor
			// in bad state"). Keep its TAP until the VM starts or is removed.
			if v.TAPDevice != "" {
				activeTAPs[v.TAPDevice] = true
			}
		default:
			_ = m.netMgr.RemoveTAP(ctx, v.TAPDevice)
		}
	}

	pruned, err := m.netMgr.PruneOrphanTAPs(ctx, activeTAPs, knownTAPs)
	m.mu.Unlock()

	for owner := range reconcileOwners {
		m.reconcileAccountNetworkIsolation(ctx, owner)
	}
	m.processQueue(ctx)
	return pruned, err
}

// FlushGuestFilesystems asks every running sandbox to flush its dirty page
// cache to its disk. A container or PID-namespace restart kills Firecracker
// without giving the guest a chance to power off, so writes made since the
// guest's last writeback would be lost and the filesystem would be left
// unclean. Best-effort, time-bounded by ctx, and never stops the sandbox; it
// returns the number of guests that acknowledged the flush.
func (m *Manager) FlushGuestFilesystems(ctx context.Context) int {
	if m.store == nil {
		return 0
	}
	vms, err := m.store.ListVMs(ctx)
	if err != nil {
		log.Printf("[vm] guest flush skipped: %v", err)
		return 0
	}
	var wg sync.WaitGroup
	var flushed int32
	for _, vm := range vms {
		if vm == nil || vm.Status != db.StatusRunning || vm.IPAddress == "" {
			continue
		}
		wg.Add(1)
		go func(vm *db.VM) {
			defer wg.Done()
			if err := m.flushGuest(ctx, vm); err != nil {
				log.Printf("[vm] guest flush failed for %s: %v", vm.Name, err)
				return
			}
			atomic.AddInt32(&flushed, 1)
		}(vm)
	}
	wg.Wait()
	return int(atomic.LoadInt32(&flushed))
}

func (m *Manager) flushGuest(ctx context.Context, vm *db.VM) error {
	if m.guestSyncer != nil {
		return m.guestSyncer(ctx, vm)
	}
	flushCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=2",
	}
	if vm.SSHKeyPath != "" {
		args = append(args, "-i", vm.SSHKeyPath)
	}
	args = append(args, fmt.Sprintf("compute@%s", vm.IPAddress), "sync")
	return exec.CommandContext(flushCtx, "ssh", args...).Run()
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
		if vm.Status == db.StatusRunning && m.isAlive(vm.PID) {
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
	return m.InspectNetworkForOwner(ctx, vmID, "")
}

// InspectNetworkForOwner resolves the VM by internal ID and, when ownerID is
// non-empty, verifies it belongs to that owner. Control-plane callers pass the
// owner of the already-authorized VM so a duplicate name or external ID can
// never resolve to another tenant's record.
func (m *Manager) InspectNetworkForOwner(ctx context.Context, vmID, ownerID string) (*NetworkInspectResult, error) {
	var vm *db.VM
	var err error
	if ownerID == "" {
		vm, err = m.store.GetVM(ctx, vmID)
	} else {
		vm, err = m.store.GetVMByOwner(ctx, ownerID, vmID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get vm: %w", err)
	}
	if vm == nil || (ownerID != "" && vm.OwnerID != "" && vm.OwnerID != ownerID) {
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
	networkIsolation := db.NetworkIsolationAccount
	if db.IsNetworkIsolated(vm) {
		networkIsolation = db.NetworkIsolationIsolated
	}
	return &NetworkInspectResult{
		VMID:             vm.ID,
		VMName:           vm.Name,
		Status:           string(vm.Status),
		IPAddress:        vm.IPAddress,
		PublicIP:         publicIP,
		PublicIPEnabled:  vm.PublicIPEnabled,
		NetworkIsolation: networkIsolation,
		MACAddress:       vm.MACAddress,
		TAPDevice:        vm.TAPDevice,
		BridgeName:       bridgeName,
		Gateway:          gwIP,
		Reachable:        reachable,
		Connectivity:     connStatus,
	}, nil
}

// UpdateVMConfigRequest defines configuration options that can be updated on an existing VM
type UpdateVMConfigRequest struct {
	PublicIP         *bool   `json:"public_ip,omitempty"`
	PublicIPEnabled  *bool   `json:"public_ip_enabled,omitempty"`
	DedicatedIP      *bool   `json:"dedicated_ip,omitempty"`
	Protected        *bool   `json:"protected,omitempty"`
	NetworkIsolation *string `json:"network_isolation,omitempty"`
	Isolated         *bool   `json:"isolated,omitempty"`
}

func (r *UpdateVMConfigRequest) UnmarshalJSON(data []byte) error {
	type Alias UpdateVMConfigRequest
	aux := struct {
		RawPublicIP    interface{} `json:"public_ip,omitempty"`
		RawDedicatedIP interface{} `json:"dedicated_ip,omitempty"`
		RawProtected   interface{} `json:"protected,omitempty"`
		RawIsolated    interface{} `json:"isolated,omitempty"`
		RawIsolate     interface{} `json:"isolate,omitempty"`
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
	if b := parseBool(aux.RawProtected); b != nil {
		r.Protected = b
	}
	if b := parseBool(aux.RawIsolated); b != nil {
		r.Isolated = b
	} else if b := parseBool(aux.RawIsolate); b != nil {
		r.Isolated = b
	}
	return nil
}

// UpdateVMConfig updates configuration parameters on an existing VM (such as toggling dedicated public IP or network isolation)
func (m *Manager) UpdateVMConfig(ctx context.Context, vmID string, req UpdateVMConfigRequest) (*db.VM, error) {
	vmLock := m.getVMLock(vmID)
	vmLock.Lock()
	defer vmLock.Unlock()

	vm, err := m.getVMUnsafe(ctx, vmID)
	if err != nil || vm == nil {
		return nil, fmt.Errorf("vm %s not found: %w", vmID, err)
	}

	var nextNetworkIsolation string
	var isolationChanged bool
	if req.NetworkIsolation != nil || req.Isolated != nil {
		var modeStr string
		if req.NetworkIsolation != nil {
			modeStr = *req.NetworkIsolation
			if strings.TrimSpace(modeStr) == "" && req.Isolated == nil {
				return nil, fmt.Errorf("invalid network_isolation %q: expected %q or %q", modeStr, db.NetworkIsolationAccount, db.NetworkIsolationIsolated)
			}
		}
		resolved, err := resolveNetworkIsolation(modeStr, req.Isolated)
		if err != nil {
			return nil, err
		}
		nextNetworkIsolation = resolved
		currentIsolation := db.NetworkIsolationAccount
		if db.IsNetworkIsolated(vm) {
			currentIsolation = db.NetworkIsolationIsolated
		}
		if currentIsolation != nextNetworkIsolation || vm.NetworkIsolation != nextNetworkIsolation {
			isolationChanged = true
		}
	}

	var changed bool
	var enabled bool
	var publicIPChanged bool
	if req.PublicIP != nil {
		enabled = *req.PublicIP
		publicIPChanged = true
	} else if req.PublicIPEnabled != nil {
		enabled = *req.PublicIPEnabled
		publicIPChanged = true
	} else if req.DedicatedIP != nil {
		enabled = *req.DedicatedIP
		publicIPChanged = true
	}

	if publicIPChanged {
		changed = true
		vm.PublicIPEnabled = enabled
		if enabled {
			vm.PublicIP = vm.IPAddress // Dedicated IP on main network is considered as public IP
		} else {
			vm.PublicIP = ""
		}

		if vm.Status == db.StatusRunning {
			_ = m.netMgr.ConfigureVMPublicIP(ctx, vm.IPAddress, vm.TAPDevice, vm.PublicIPEnabled)
		}
	}

	if req.Protected != nil {
		vm.Protected = *req.Protected
		changed = true
	}

	if isolationChanged {
		vm.NetworkIsolation = nextNetworkIsolation
		changed = true
	}

	if changed {
		vm.UpdatedAt = time.Now().UTC()
		if err := m.saveVMRecord(ctx, vm); err != nil {
			return nil, fmt.Errorf("failed to save vm config: %w", err)
		}
		if isolationChanged && vm.Status == db.StatusRunning {
			m.reconcileAccountNetworkIsolation(ctx, vm.OwnerID)
		}
	}

	return vm, nil
}

// newSSHExec builds an SSH command for a running VM. A zero Timeout means no
// deadline; callers that want a default apply it before calling.
func (m *Manager) newSSHExec(ctx context.Context, vmID string, req ExecRequest) (*exec.Cmd, context.Context, context.CancelFunc, error) {
	vm, err := m.getVMUnsafe(ctx, vmID)
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
	command := `if [ -f "$HOME/.compute-env" ]; then set -a; . "$HOME/.compute-env"; set +a; elif [ -f "/home/compute/.compute-env" ]; then set -a; . "/home/compute/.compute-env"; set +a; fi; ` + req.Command
	sshArgs = append(sshArgs, fmt.Sprintf("%s@%s", user, vm.IPAddress), command)

	return exec.CommandContext(execCtx, "ssh", sshArgs...), execCtx, cancel, nil
}

// ExecVM executes a remote command inside a running VM via SSH, buffering the
// output until the command finishes.
func (m *Manager) ExecVM(ctx context.Context, vmID string, req ExecRequest) (*ExecResponse, error) {
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	defer vmLock.RUnlock()

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
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	defer vmLock.RUnlock()

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
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	defer vmLock.RUnlock()

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
	m.mu.RLock()
	if img, ok := m.images[nameOrID]; ok && img != nil {
		m.mu.RUnlock()
		return img, nil
	}
	m.mu.RUnlock()

	img, err := m.store.GetImage(ctx, nameOrID)
	if err == nil && img != nil {
		m.mu.Lock()
		m.images[img.ID] = img
		if img.InternalID != "" {
			m.images[img.InternalID] = img
		}
		m.mu.Unlock()
	}
	return img, err
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

	// Image artefacts are owner-unique; remove the record's own path instead of
	// deriving a filename from the (tenant-reusable) image name.
	if img.RootfsPath != "" {
		_ = os.Remove(img.RootfsPath)
	}
	uniqueID := img.InternalID
	if uniqueID == "" {
		uniqueID = img.ID
	}
	m.mu.Lock()
	delete(m.images, nameOrID)
	delete(m.images, uniqueID)
	m.mu.Unlock()

	return m.store.DeleteImage(ctx, uniqueID)
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

	// Name uniqueness is scoped to the owner so different tenants may reuse
	// image and template names without blocking or shadowing each other.
	existingTpl, _ := m.store.GetTemplateByOwner(ctx, req.OwnerID, cleanName)
	if existingTpl != nil {
		return nil, fmt.Errorf("template with name '%s' already exists", cleanName)
	}
	existingImg, _ := m.store.GetImageByOwner(ctx, req.OwnerID, cleanName)
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
	// Scope the on-disk template artefact by its unique ID: template names may
	// be reused by different tenants and must not overwrite each other.
	tplDiskPath, err := m.storMgr.CreateTemplateDiskFromVM(srcDirID, tplID)
	if err != nil && srcDirID != srcVM.ID {
		tplDiskPath, err = m.storMgr.CreateTemplateDiskFromVM(srcVM.ID, tplID)
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
		OwnerID:     req.OwnerID,
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

	m.mu.Lock()
	m.templates[tpl.ID] = tpl
	if tpl.InternalID != "" {
		m.templates[tpl.InternalID] = tpl
	}
	m.mu.Unlock()

	return tpl, nil
}

// ListTemplates returns all stored templates
func (m *Manager) ListTemplates(ctx context.Context) ([]*db.Template, error) {
	return m.store.ListTemplates(ctx)
}

// GetTemplate retrieves a template by ID or Name
func (m *Manager) GetTemplate(ctx context.Context, nameOrID string) (*db.Template, error) {
	m.mu.RLock()
	if t, ok := m.templates[nameOrID]; ok && t != nil {
		m.mu.RUnlock()
		return t, nil
	}
	m.mu.RUnlock()

	t, err := m.store.GetTemplate(ctx, nameOrID)
	if err == nil && t != nil {
		m.mu.Lock()
		m.templates[t.ID] = t
		if t.InternalID != "" {
			m.templates[t.InternalID] = t
		}
		m.mu.Unlock()
	}
	return t, err
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

	// Template disks are stored under the unique ID to avoid cross-tenant name
	// collisions; remove that path directly.
	if tpl.DiskPath != "" {
		_ = os.Remove(tpl.DiskPath)
	}
	uniqueID := tpl.InternalID
	if uniqueID == "" {
		uniqueID = tpl.ID
	}

	m.mu.Lock()
	delete(m.templates, nameOrID)
	delete(m.templates, uniqueID)
	m.mu.Unlock()

	return m.store.DeleteTemplate(ctx, uniqueID)
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

// GetVM retrieves VM details from in-memory registry or underlying store and reconciles liveness
func (m *Manager) GetVM(ctx context.Context, vmID string) (*db.VM, error) {
	var v *db.VM
	var err error
	if m.store != nil {
		v, err = m.store.GetVM(ctx, vmID)
		if err == nil && v != nil {
			m.mu.Lock()
			m.vms[v.ID] = v
			if v.InternalID != "" {
				m.vms[v.InternalID] = v
			}
			if v.Name != "" {
				m.vms[v.Name] = v
			}
			m.mu.Unlock()
		}
	} else {
		m.mu.RLock()
		if cached, ok := m.vms[vmID]; ok && cached != nil {
			v = cached
		}
		m.mu.RUnlock()
	}

	if v != nil && v.Status == db.StatusRunning && v.PID > 0 && !m.isAlive(v.PID) {
		m.handleProcessExit(v.ID, v.PID)
		// Reload updated state
		if m.store != nil {
			v, err = m.store.GetVM(ctx, vmID)
		}
	}

	return v, err
}

// ListVMs lists all VMs and reconciles running status against process liveness
func (m *Manager) ListVMs(ctx context.Context) ([]*db.VM, error) {
	vms, err := m.store.ListVMs(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range vms {
		if v.Status == db.StatusRunning && v.PID > 0 && !m.isAlive(v.PID) {
			m.handleProcessExit(v.ID, v.PID)
			v.Status = db.StatusStopped
			v.PID = 0
		}
	}
	return vms, nil
}

// watchProcessExit waits for a microVM process to terminate and reconciles state immediately
func (m *Manager) watchProcessExit(vmID string, pid int, cmd *exec.Cmd) {
	go func() {
		if cmd != nil {
			_ = cmd.Wait()
		} else if pid > 0 {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for range ticker.C {
				if !m.isAlive(pid) {
					break
				}
			}
		}
		m.handleProcessExit(vmID, pid)
	}()
}

// handleProcessExit handles a dead VM process by transitioning state to db.StatusStopped,
// cleaning runtime network artifacts, and triggering queue processing.
func (m *Manager) handleProcessExit(vmID string, pid int) {
	vmLock := m.getVMLock(vmID)
	vmLock.Lock()
	defer vmLock.Unlock()

	ctx := context.Background()
	vm, err := m.getVMUnsafe(ctx, vmID)
	if err != nil || vm == nil {
		return
	}

	if vm.Status == db.StatusRunning && ((pid > 0 && vm.PID == pid) || (vm.PID > 0 && !m.isAlive(vm.PID))) {
		log.Printf("[vm-manager] VM %s (pid %d) exited, marking stopped", vmID, vm.PID)
		vm.Status = db.StatusStopped
		vm.PID = 0
		vm.UpdatedAt = time.Now().UTC()
		_ = m.saveVMRecord(ctx, vm)
		if vm.TAPDevice != "" {
			_ = m.netMgr.RemoveTAP(ctx, vm.TAPDevice)
		}
		if vm.PublicIPEnabled && vm.IPAddress != "" {
			_ = m.netMgr.ConfigureVMPublicIP(ctx, vm.IPAddress, vm.TAPDevice, false)
		}
		m.reconcileAccountNetworkIsolation(ctx, vm.OwnerID)
		go m.processQueue(context.Background())
	}
}

// StartStatusMonitor starts a periodic ticker reconciling VM statuses against process liveness
func (m *Manager) StartStatusMonitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = m.ReconcileAndPrune(context.Background())
			}
		}
	}()
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
