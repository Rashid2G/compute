package config

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/tailscale"
)

// Supported fixed VM tiers and limits (inspired by boat.dev)
const (
	Tier1VCPU = "1vcpu"
	Tier2VCPU = "2vcpu"
	Tier4VCPU = "4vcpu"

	DiskSize50GB  = 51200  // 50 GB in MB
	DiskSize100GB = 102400 // 100 GB in MB

	DefaultImageName  = "ubuntu-24"
	DefaultRootfsName = "ubuntu-24.04.ext4"
)

// VMTier defines a fixed specification tier for VMs
type VMTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	VCPUs       int    `json:"vcpus"`
	DefaultRAM  int    `json:"default_ram_mb"`
	DisksMB     []int  `json:"disks_mb"`
	Description string `json:"description"`
}

// GetSupportedTiers returns the fixed list of supported VM tiers
func GetSupportedTiers() []VMTier {
	return []VMTier{
		{
			ID:          "1vcpu",
			Name:        "1 vCPU (Micro)",
			VCPUs:       1,
			DefaultRAM:  2048,
			DisksMB:     []int{DiskSize50GB, DiskSize100GB},
			Description: "1 vCPU · 2 GiB RAM · 50 GB / 100 GB Disk",
		},
		{
			ID:          "2vcpu",
			Name:        "2 vCPU (Small)",
			VCPUs:       2,
			DefaultRAM:  4096,
			DisksMB:     []int{DiskSize50GB, DiskSize100GB},
			Description: "2 vCPU · 4 GiB RAM · 50 GB / 100 GB Disk",
		},
		{
			ID:          "4vcpu",
			Name:        "4 vCPU (Medium)",
			VCPUs:       4,
			DefaultRAM:  8192,
			DisksMB:     []int{DiskSize50GB, DiskSize100GB},
			Description: "4 vCPU · 8 GiB RAM · 50 GB / 100 GB Disk",
		},
	}
}

// ValidateAndNormalizeSpecs validates that vCPUs is in fixed tiers {1, 2, 4},
// sets default RAM if omitted, and ensures disk is limited to 50GB or 100GB.
func ValidateAndNormalizeSpecs(vcpus, memoryMB, diskMB int) (int, int, int, error) {
	// vCPUs check (fixed tiers 1, 2, 4)
	if vcpus <= 0 {
		vcpus = 1
	}
	if vcpus != 1 && vcpus != 2 && vcpus != 4 {
		return 0, 0, 0, fmt.Errorf("invalid vcpus '%d': compute only supports fixed tiers of 1vcpu, 2vcpu, or 4vcpu (inspired by boat.dev)", vcpus)
	}

	// RAM is optional: if omitted or 0, default according to tier
	if memoryMB <= 0 {
		switch vcpus {
		case 1:
			memoryMB = 2048
		case 2:
			memoryMB = 4096
		case 4:
			memoryMB = 8192
		}
	} else if memoryMB < 128 {
		return 0, 0, 0, fmt.Errorf("invalid memory '%d MB': minimum memory is 128 MB", memoryMB)
	}

	// Disk size is customizable (defaults to 50 GB if omitted, supports values in GB or MB)
	if diskMB <= 0 {
		diskMB = DiskSize50GB
	} else if diskMB < 512 {
		// Value was provided in GB (e.g. 10, 20, 50, 100)
		diskMB = diskMB * 1024
	}
	if diskMB < 512 {
		return 0, 0, 0, fmt.Errorf("invalid disk size '%d MB': minimum disk size is 512 MB", diskMB)
	}

	return vcpus, memoryMB, diskMB, nil
}

// ValidateImage validates that only Ubuntu 24 is accepted as a base image
func ValidateImage(image string) (string, error) {
	clean := strings.ToLower(strings.TrimSpace(image))
	if clean == "" || clean == "default" || clean == "ubuntu" || clean == "ubuntu-24" || clean == "ubuntu-24.04" || clean == "ubuntu-2404" {
		return DefaultImageName, nil
	}
	return "", fmt.Errorf("unsupported image '%s': only 'ubuntu-24' (Ubuntu 24.04 LTS) is supported; users can clone disks to run other VMs from their existing images", image)
}

// DNSConfig configures the built-in authoritative DNS responder that gives
// every sandbox a stable hostname of the form
//
//	<sandbox>.<username>.<base-domain>
//
// SSH connections carry no hostname, so the wildcard dispatch for SSH happens
// at the DNS layer: each sandbox name resolves to the sandbox's own address
// (tailnet IP first, guest IP second) and the network does the routing. The
// zone works for any base domain — Tailscale split DNS, any DNS provider's
// delegation, or a LAN resolver — as long as queries for the zone reach the
// daemon. Queries outside the zone are forwarded upstream.
type DNSConfig struct {
	// Enabled turns the responder on.
	Enabled bool `json:"enabled,omitempty"`
	// Listen is the UDP/TCP listen address (default ":53").
	Listen string `json:"listen,omitempty"`
	// BaseDomain is the primary zone, e.g. "compute-lab.tail1234.ts.net".
	// Empty falls back to network.base_domain.
	BaseDomain string `json:"base_domain,omitempty"`
	// ExtraBaseDomains optionally serves additional zones.
	ExtraBaseDomains []string `json:"extra_base_domains,omitempty"`
	// HostAddresses overrides the addresses the zone apex and reserved
	// service labels (api/gui/www) resolve to — the host running the GUI/API.
	// Useful when the daemon runs in a container where auto-detection (the
	// outbound route, or the host's tailnet IP) yields a non-routable address.
	// Empty falls back to the host's tailnet IP, else the primary outbound IP.
	HostAddresses []string `json:"host_addresses,omitempty"`
}

// ServerConfig holds daemon server settings
type ServerConfig struct {
	ListenAddr     string `json:"listen"`
	DBPath         string `json:"db_path"`
	FirecrackerBin string `json:"firecracker_bin"`
	AuthEnabled    bool   `json:"auth_enabled"`
	AuthDBPath     string `json:"auth_db_path,omitempty"`
	AdminSecret    string `json:"admin_secret,omitempty"`
	AdminPassword  string `json:"admin_password,omitempty"`
	JailerEnabled  bool   `json:"jailer_enabled,omitempty"`
	JailerBinary   string `json:"jailer_binary,omitempty"`
	JailerBaseDir  string `json:"jailer_base_dir,omitempty"`
	CgroupEnforce  bool   `json:"cgroup_enforce,omitempty"`
	// EdgeListen optionally starts an HTTP edge listener (default ":80" when
	// DNS is enabled) that dispatches by Host header: the reserved "api" and
	// apex/gui/www subdomains of the base domain are served by the daemon
	// itself, every other subdomain goes to the sandbox ingress wildcard.
	// Set to "" (or "-") to disable.
	EdgeListen string `json:"edge_listen,omitempty"`
}

// DefaultsConfig holds default microVM specifications
type DefaultsConfig struct {
	VCPUs      int    `json:"vcpus"`
	MemoryMB   int    `json:"memory_mb"`
	DiskSizeMB int    `json:"disk_size_mb"`
	Image      string `json:"image"`
}

// AvailableImage defines a bootable base image
type AvailableImage struct {
	Name        string `json:"name"`
	Kernel      string `json:"kernel"`
	Rootfs      string `json:"rootfs"`
	Description string `json:"description,omitempty"`
}

// ImagesConfig holds storage and base image settings
type ImagesConfig struct {
	ImagesDir     string           `json:"images_dir"`
	VMsDir        string           `json:"vms_dir"`
	DefaultKernel string           `json:"default_kernel"`
	DefaultRootfs string           `json:"default_rootfs"`
	SSHKeyPath    string           `json:"ssh_key_path,omitempty"`
	Available     []AvailableImage `json:"available,omitempty"`
}

// TailscaleConfig controls the tailnet integration on the control-plane host.
// When enabled, the daemon can assign tailnet addresses to sandboxes and
// report its own MagicDNS identity. Every device Compute joins is tagged
// (tag:compute-*) and named with the "compute-" prefix; the operator's
// existing devices, routes and policy file are never modified.
type TailscaleConfig struct {
	// Enabled turns the integration on.
	Enabled bool `json:"enabled,omitempty"`
	// APIKey is a Tailscale admin API access token, required to mint
	// sandbox auth keys and manage cleanup. Prefer the
	// COMPUTE_TAILSCALE_API_KEY environment variable over committing it here.
	APIKey string `json:"api_key,omitempty"`
	// Tailnet selects the tailnet ("-" = the API key's default).
	Tailnet string `json:"tailnet,omitempty"`
	// NodeTag is the tag applied to the control plane device itself.
	NodeTag string `json:"node_tag,omitempty"`
	// SandboxTag is the tag applied to sandbox auth keys/devices.
	SandboxTag string `json:"sandbox_tag,omitempty"`
	// HostnamePrefix prepends to every hostname Compute assigns.
	HostnamePrefix string `json:"hostname_prefix,omitempty"`
	// KeyExpirySeconds is the lifetime of minted sandbox keys (default 90 days).
	KeyExpirySeconds int `json:"key_expiry_seconds,omitempty"`
	// AutoInstallGuest installs the tailscale binary inside sandboxes that
	// lack it (via the SSH control channel). Default true.
	AutoInstallGuest *bool `json:"auto_install_guest,omitempty"`
	// DefaultSandboxes gives every new sandbox tailnet addressing unless the
	// request explicitly opts out. Deprecated: superseded by ServiceGateway.
	DefaultSandboxes bool `json:"default_sandboxes,omitempty"`
	// ServiceGateway turns the API host into the single tailnet Service host.
	// Sandboxes stay on the local bridge; the daemon creates one Tailscale
	// Service (VIP + MagicDNS name) per sandbox and proxies its ports.
	ServiceGateway *ServiceGatewayConfig `json:"service_gateway,omitempty"`
	// LegacyPerSandboxDevices keeps the old behaviour of joining each sandbox
	// to the tailnet as its own ephemeral device. Off by default; the
	// ServiceGateway replaces it.
	LegacyPerSandboxDevices bool `json:"legacy_per_sandbox_devices,omitempty"`
}

// ServiceGatewayConfig controls the "single tailnet Service host" model: only
// the machine running the daemon joins the tailnet, sandboxes stay on the
// local bridge, and the daemon creates/deletes a Tailscale Service per
// sandbox for stable VIP addresses and MagicDNS names.
type ServiceGatewayConfig struct {
	// Enabled turns the gateway on. Requires tailscale.enabled and an API key.
	Enabled bool `json:"enabled,omitempty"`
	// HostTag is the tag the API host must carry to act as a Service host.
	// Default tag:compute-master.
	HostTag string `json:"host_tag,omitempty"`
	// ServiceTag is applied to every created Service. Default tag:compute-sandbox.
	ServiceTag string `json:"service_tag,omitempty"`
	// AutoApprove approves this daemon's Service host advertisements through
	// the admin API so no manual console click is needed. Default true.
	AutoApprove *bool `json:"auto_approve,omitempty"`
	// ScanIntervalSeconds controls how often guest listening ports are
	// re-scanned and Services reconciled. Default 60.
	ScanIntervalSeconds int `json:"scan_interval_seconds,omitempty"`
	// ExposeLoopback also exposes listeners bound to 127.0.0.1/::1. Off by
	// default because those are usually meant to stay inside the sandbox.
	ExposeLoopback bool `json:"expose_loopback,omitempty"`
	// ExcludePorts are never exposed (for example 25 to avoid mail abuse).
	ExcludePorts []int `json:"exclude_ports,omitempty"`
	// TargetAddress is the address tailscaled (running on the host) should dial
	// to reach the daemon's local gateway listeners. Empty auto-detects a
	// non-loopback IPv4 address of this process (the container IP in the
	// compose deployment).
	TargetAddress string `json:"target_address,omitempty"`
	// TailscaleBinary is the CLI used for `tailscale serve`. Default "tailscale".
	TailscaleBinary string `json:"tailscale_binary,omitempty"`
	// TailscaleSocket is the tailscaled control socket. Default
	// /var/run/tailscale/tailscaled.sock.
	TailscaleSocket string `json:"tailscale_socket,omitempty"`
	// SSHUser is the guest account used to scan listening ports. Default "compute".
	SSHUser string `json:"ssh_user,omitempty"`
	// ServicePrefix prefixes generated Service names. Default "compute".
	ServicePrefix string `json:"service_prefix,omitempty"`
}

// ResolvedSandboxTag returns the sandbox tag with the Compute default applied.
func (c TailscaleConfig) ResolvedSandboxTag() string {
	if c.SandboxTag != "" {
		return tailscale.NormalizeTag(c.SandboxTag)
	}
	return tailscale.TagSandbox
}

// ResolvedNodeTag returns the control-plane tag with the Compute default applied.
func (c TailscaleConfig) ResolvedNodeTag() string {
	if c.NodeTag != "" {
		return tailscale.NormalizeTag(c.NodeTag)
	}
	return tailscale.TagMaster
}

// AutoInstallResolved defaults to true when unset.
func (c TailscaleConfig) AutoInstallResolved() bool {
	if c.AutoInstallGuest != nil {
		return *c.AutoInstallGuest
	}
	return true
}

// ServiceGatewayEnabled reports whether the single-host Service gateway is on.
func (c TailscaleConfig) ServiceGatewayEnabled() bool {
	return c.Enabled && c.ServiceGateway != nil && c.ServiceGateway.Enabled
}

// ResolvedHostTag returns the Service-host tag with the Compute default applied.
func (c TailscaleConfig) ResolvedHostTag() string {
	if c.ServiceGateway != nil && c.ServiceGateway.HostTag != "" {
		return tailscale.NormalizeTag(c.ServiceGateway.HostTag)
	}
	return c.ResolvedNodeTag()
}

// ResolvedServiceTag returns the tag applied to created Services.
func (c TailscaleConfig) ResolvedServiceTag() string {
	if c.ServiceGateway != nil && c.ServiceGateway.ServiceTag != "" {
		return tailscale.NormalizeTag(c.ServiceGateway.ServiceTag)
	}
	return c.ResolvedSandboxTag()
}

// AutoApproveResolved defaults to true when unset.
func (c TailscaleConfig) AutoApproveResolved() bool {
	if c.ServiceGateway != nil && c.ServiceGateway.AutoApprove != nil {
		return *c.ServiceGateway.AutoApprove
	}
	return true
}

// GatewayScanInterval returns the reconcile interval (default 30s).
func (c TailscaleConfig) GatewayScanInterval() time.Duration {
	if c.ServiceGateway != nil && c.ServiceGateway.ScanIntervalSeconds > 0 {
		return time.Duration(c.ServiceGateway.ScanIntervalSeconds) * time.Second
	}
	return 30 * time.Second
}

// GatewayExcludePorts returns the configured exclusion set.
func (c TailscaleConfig) GatewayExcludePorts() map[int]bool {
	out := make(map[int]bool)
	if c.ServiceGateway == nil {
		return out
	}
	for _, p := range c.ServiceGateway.ExcludePorts {
		if p > 0 && p <= 65535 {
			out[p] = true
		}
	}
	return out
}

// GatewayServicePrefix returns the Service name prefix.
func (c TailscaleConfig) GatewayServicePrefix() string {
	if c.ServiceGateway != nil && strings.TrimSpace(c.ServiceGateway.ServicePrefix) != "" {
		return strings.TrimSpace(c.ServiceGateway.ServicePrefix)
	}
	return "compute"
}

// GatewayTailscaleBinary returns the tailscale CLI path.
func (c TailscaleConfig) GatewayTailscaleBinary() string {
	if c.ServiceGateway != nil && strings.TrimSpace(c.ServiceGateway.TailscaleBinary) != "" {
		return strings.TrimSpace(c.ServiceGateway.TailscaleBinary)
	}
	return "tailscale"
}

// GatewayTailscaleSocket returns the tailscaled control socket path.
func (c TailscaleConfig) GatewayTailscaleSocket() string {
	if c.ServiceGateway != nil && strings.TrimSpace(c.ServiceGateway.TailscaleSocket) != "" {
		return strings.TrimSpace(c.ServiceGateway.TailscaleSocket)
	}
	return "/var/run/tailscale/tailscaled.sock"
}

// GatewaySSHUser returns the guest account used for port scans.
func (c TailscaleConfig) GatewaySSHUser() string {
	if c.ServiceGateway != nil && strings.TrimSpace(c.ServiceGateway.SSHUser) != "" {
		return strings.TrimSpace(c.ServiceGateway.SSHUser)
	}
	return "compute"
}

// GatewayTargetAddress returns the explicit target address, or "" for auto.
func (c TailscaleConfig) GatewayTargetAddress() string {
	if c.ServiceGateway != nil {
		return strings.TrimSpace(c.ServiceGateway.TargetAddress)
	}
	return ""
}

// JobsConfig controls local job execution on the control-plane host.
type JobsConfig struct {
	// LocalExecution runs queued jobs in local microVMs when no worker node is
	// connected, so jobs submitted from the CLI do not wait forever.
	LocalExecution bool `json:"local_execution"`
	// MaxConcurrent limits how many local job sandboxes may run at once.
	MaxConcurrent int `json:"max_concurrent,omitempty"`
	// BootTimeoutSeconds is how long to wait for a job sandbox to accept SSH.
	BootTimeoutSeconds int `json:"boot_timeout_seconds,omitempty"`
}

// ResourceLimits defines maximum capacity constraints for the host
type ResourceLimits struct {
	MaxRunningVMs      int   `json:"max_running_vms"`        // Max concurrent running VMs (0 = unlimited)
	MaxTotalVCPUs      int   `json:"max_total_vcpus"`        // Max total vCPUs across running VMs (0 = unlimited)
	MaxTotalMemoryMB   int   `json:"max_total_memory_mb"`    // Max total RAM (MB) across running VMs (0 = unlimited)
	MaxTotalDiskMB     int64 `json:"max_total_disk_mb"`      // Max total disk allocation for all VMs (0 = unlimited)
	MinFreeDiskSpaceMB int64 `json:"min_free_disk_space_mb"` // Minimum free disk required on host (MB) on creation
}

// GlobalConfig is the master configuration for the Compute daemon
type GlobalConfig struct {
	ConfigPath string          `json:"config_path,omitempty"`
	Server     ServerConfig    `json:"server"`
	Network    network.Config  `json:"network"`
	Defaults   DefaultsConfig  `json:"defaults"`
	Images     ImagesConfig    `json:"images"`
	Resources  ResourceLimits  `json:"resources"`
	Jobs       JobsConfig      `json:"jobs"`
	Tailscale  TailscaleConfig `json:"tailscale,omitempty"`
	DNS        DNSConfig       `json:"dns,omitempty"`
}

// Config is an alias for GlobalConfig for backward compatibility and package consistency.
type Config = GlobalConfig

// DNSBaseDomains returns the effective DNS zones in priority order (primary
// first, longest-suffix matching is applied by the DNS server). An empty
// result means DNS naming is not configured.
func (c GlobalConfig) DNSBaseDomains() []string {
	var zones []string
	primary := c.DNS.BaseDomain
	if primary == "" {
		primary = c.Network.BaseDomain
	}
	if primary = strings.ToLower(strings.TrimSpace(primary)); primary != "" {
		zones = append(zones, primary)
	}
	for _, z := range c.DNS.ExtraBaseDomains {
		if z = strings.ToLower(strings.TrimSpace(z)); z != "" && z != primary {
			zones = append(zones, z)
		}
	}
	return zones
}

// PrimaryDNSBaseDomain returns the first configured DNS zone ("" when none).
func (c GlobalConfig) PrimaryDNSBaseDomain() string {
	zones := c.DNSBaseDomains()
	if len(zones) == 0 {
		return ""
	}
	return zones[0]
}

// EdgeListenResolved returns the edge listener address, or "" when the edge
// should not start. Default ":80" while DNS is enabled; "-" explicitly
// disables.
func (c GlobalConfig) EdgeListenResolved() string {
	v := strings.TrimSpace(c.Server.EdgeListen)
	if v == "-" {
		return ""
	}
	if v != "" {
		return v
	}
	if c.DNS.Enabled && c.PrimaryDNSBaseDomain() != "" {
		return ":80"
	}
	return ""
}

// DefaultConfig returns standard out-of-the-box configuration
func DefaultConfig() GlobalConfig {
	return GlobalConfig{
		Server: ServerConfig{
			ListenAddr:     "0.0.0.0:18080",
			DBPath:         "/var/lib/compute/data/compute.db",
			FirecrackerBin: "/usr/local/bin/firecracker",
			JailerEnabled:  false,
			JailerBinary:   "/usr/local/bin/jailer",
			JailerBaseDir:  "/srv/jailer",
			CgroupEnforce:  true,
		},
		Network: network.DefaultNATConfig(),
		Defaults: DefaultsConfig{
			VCPUs:      1,
			MemoryMB:   2048,
			DiskSizeMB: DiskSize50GB,
			Image:      DefaultImageName,
		},
		Images: ImagesConfig{
			ImagesDir:     "/var/lib/compute/images",
			VMsDir:        "/var/lib/compute/vms",
			DefaultKernel: "vmlinux",
			DefaultRootfs: DefaultRootfsName,
			SSHKeyPath:    "/var/lib/compute/images/ssh/id_ed25519",
			Available: []AvailableImage{
				{
					Name:        DefaultImageName,
					Kernel:      "vmlinux",
					Rootfs:      DefaultRootfsName,
					Description: "Standard Ubuntu 24.04 LTS (Noble Numbat) with Node.js, Python 3, and SSH",
				},
			},
		},
		Resources: ResourceLimits{
			MaxRunningVMs:      0,
			MaxTotalVCPUs:      0,
			MaxTotalMemoryMB:   0,
			MaxTotalDiskMB:     0,
			MinFreeDiskSpaceMB: 512,
		},
		Jobs: JobsConfig{
			LocalExecution:     true,
			MaxConcurrent:      2,
			BootTimeoutSeconds: 90,
		},
	}
}

// LoadConfigFile loads and parses a JSON config file.
// It handles full GlobalConfig JSON, as well as legacy network-only JSON gracefully.
func LoadConfigFile(path string) (GlobalConfig, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}

	// First try unmarshaling as full GlobalConfig
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return cfg, fmt.Errorf("invalid json in config %s: %w", path, err)
	}

	// Check if this is a legacy network config (contains "bridge_name" or "subnet" directly at top level)
	_, hasBridge := raw["bridge_name"]
	_, hasSubnet := raw["subnet"]
	_, hasMode := raw["mode"]
	_, hasNetworkObj := raw["network"]

	if (hasBridge || hasSubnet || hasMode) && !hasNetworkObj {
		// Legacy network-only JSON
		var netCfg network.Config
		if err := json.Unmarshal(data, &netCfg); err != nil {
			return cfg, fmt.Errorf("invalid legacy network config %s: %w", path, err)
		}
		cfg.Network = netCfg
		cfg.ConfigPath = path
		return cfg, nil
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("failed to parse global config %s: %w", path, err)
	}

	// Set resolved config path
	cfg.ConfigPath = path

	// Normalize defaults if omitted in custom config
	if cfg.Server.ListenAddr == "" {
		cfg.Server.ListenAddr = "0.0.0.0:18080"
	}
	if cfg.Server.DBPath == "" {
		cfg.Server.DBPath = "/var/lib/compute/data/compute.db"
	}
	if cfg.Server.FirecrackerBin == "" {
		cfg.Server.FirecrackerBin = "/usr/local/bin/firecracker"
	}
	if cfg.Defaults.VCPUs <= 0 {
		cfg.Defaults.VCPUs = 1
	}
	if cfg.Defaults.MemoryMB <= 0 {
		cfg.Defaults.MemoryMB = 2048
	}
	if cfg.Defaults.DiskSizeMB <= 0 {
		cfg.Defaults.DiskSizeMB = DiskSize50GB
	}
	if cfg.Defaults.Image == "" {
		cfg.Defaults.Image = DefaultImageName
	}
	if cfg.Images.ImagesDir == "" {
		cfg.Images.ImagesDir = "/var/lib/compute/images"
	}
	if cfg.Images.VMsDir == "" {
		cfg.Images.VMsDir = "/var/lib/compute/vms"
	}
	if cfg.Images.DefaultKernel == "" {
		cfg.Images.DefaultKernel = "vmlinux"
	}
	if cfg.Images.DefaultRootfs == "" {
		cfg.Images.DefaultRootfs = DefaultRootfsName
	}

	// Apply COMPUTE_DATA_DIR environment variable if specified
	if dataDir := os.Getenv("COMPUTE_DATA_DIR"); dataDir != "" {
		if cfg.Server.DBPath == "/var/lib/compute/data/compute.db" || strings.HasPrefix(cfg.Server.DBPath, "/home/") {
			cfg.Server.DBPath = filepath.Join(dataDir, "data", "compute.db")
		}
		if cfg.Images.ImagesDir == "/var/lib/compute/images" || strings.HasPrefix(cfg.Images.ImagesDir, "/home/") {
			cfg.Images.ImagesDir = filepath.Join(dataDir, "images")
		}
		if cfg.Images.VMsDir == "/var/lib/compute/vms" || strings.HasPrefix(cfg.Images.VMsDir, "/home/") {
			cfg.Images.VMsDir = filepath.Join(dataDir, "vms")
		}
		if cfg.Images.SSHKeyPath == "/var/lib/compute/images/ssh/id_ed25519" || strings.HasPrefix(cfg.Images.SSHKeyPath, "/home/") {
			cfg.Images.SSHKeyPath = filepath.Join(dataDir, "images", "ssh", "id_ed25519")
		}
	}

	// Apply Tailscale environment overrides
	if v := os.Getenv("COMPUTE_TAILSCALE_ENABLED"); v != "" {
		cfg.Tailscale.Enabled = v == "true" || v == "1" || v == "on" || v == "yes"
	}
	if v := os.Getenv("COMPUTE_TAILSCALE_API_KEY"); v != "" {
		cfg.Tailscale.APIKey = v
	}
	if v := os.Getenv("COMPUTE_TAILSCALE_TAILNET"); v != "" {
		cfg.Tailscale.Tailnet = v
	}
	if cfg.Tailscale.Enabled && cfg.Tailscale.APIKey == "" && os.Getenv("COMPUTE_TAILSCALE_API_KEY") == "" {
		// Enabling the integration without credentials still works for local
		// status reporting; sandbox provisioning reports the missing key.
	}

	// Auto-locate Firecracker and Jailer if configured path doesn't exist
	if _, err := os.Stat(cfg.Server.FirecrackerBin); err != nil {
		if path, err := exec.LookPath("firecracker"); err == nil {
			cfg.Server.FirecrackerBin = path
		}
	}
	if cfg.Server.JailerBinary != "" {
		if _, err := os.Stat(cfg.Server.JailerBinary); err != nil {
			if path, err := exec.LookPath("jailer"); err == nil {
				cfg.Server.JailerBinary = path
			}
		}
	}

	return cfg, nil
}

// ResolveConfig finds and loads the configuration file according to standard precedence:
// 1. Explicit path passed via flag
// 2. ./config/daemon.json
// 3. ./config/daemon.nat.json
// 4. ./config/network.json (legacy)
// 5. /etc/compute/config.json
// 6. Default fallback
func ResolveConfig(explicitPath string) (GlobalConfig, string, error) {
	if explicitPath != "" {
		cfg, err := LoadConfigFile(explicitPath)
		if err == nil {
			cfg.ConfigPath = explicitPath
		}
		return cfg, explicitPath, err
	}

	candidatePaths := []string{
		"config/daemon.json",
		"config/daemon.nat.json",
		"config/compute.json",
		"config/network.json",
		"/etc/compute/config.json",
	}

	for _, p := range candidatePaths {
		if _, err := os.Stat(p); err == nil {
			cfg, err := LoadConfigFile(p)
			if err == nil {
				cfg.ConfigPath = p
				return cfg, p, nil
			}
		}
	}

	return DefaultConfig(), "", nil
}
