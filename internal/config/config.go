package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/Rashid2G/compute/internal/network"
)

// Supported fixed VM tiers and limits
const (
	Tier1VCPU  = "1vcpu"
	Tier2VCPU  = "2vcpu"
	Tier4VCPU  = "4vcpu"
	Tier8VCPU  = "8vcpu"
	Tier16VCPU = "16vcpu"

	MaxVCPUsPerVM = 16

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

// ValidateAndNormalizeSpecs validates that vCPUs is in supported tiers {1, 2, 4, 8, 16}
// (up to MaxVCPUsPerVM), sets default RAM if omitted, and normalizes disk size.
func ValidateAndNormalizeSpecs(vcpus, memoryMB, diskMB int) (int, int, int, error) {
	// vCPUs check (supported tiers 1, 2, 4, 8, 16 up to MaxVCPUsPerVM)
	if vcpus <= 0 {
		vcpus = 1
	}
	if vcpus > MaxVCPUsPerVM || (vcpus != 1 && vcpus != 2 && vcpus != 4 && vcpus != 8 && vcpus != 16) {
		return 0, 0, 0, fmt.Errorf("invalid vcpus '%d': compute supports 1, 2, 4, 8, or 16 vcpus (max %d)", vcpus, MaxVCPUsPerVM)
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
		case 8:
			memoryMB = 16384
		case 16:
			memoryMB = 32768
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
	// BaseDomain is the primary zone, e.g. "compute.example.internal".
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
	SignupEnabled  bool   `json:"signup_enabled"`
	AuthDBPath     string `json:"auth_db_path,omitempty"`
	AdminSecret    string `json:"admin_secret,omitempty"`
	AdminUsername  string `json:"admin_username,omitempty"`
	AdminEmail     string `json:"admin_email,omitempty"`
	AdminPassword  string `json:"admin_password,omitempty"`
	JailerEnabled  bool   `json:"jailer_enabled,omitempty"`
	JailerBinary   string `json:"jailer_binary,omitempty"`
	JailerBaseDir  string `json:"jailer_base_dir,omitempty"`
	CgroupEnforce  bool   `json:"cgroup_enforce,omitempty"`
	// PublicURL is the externally reachable base URL of this daemon, for
	// example "https://compute-lab.tailb1be99.ts.net". Clients use it to print
	// absolute serve links instead of the address they connected through
	// (which may be a loopback or container-internal endpoint). Empty means
	// "resolve links against the daemon address the client used".
	PublicURL string `json:"public_url,omitempty"`
	// ServeDomain selects hostname-based serving: every exposure gets its own
	// origin at "<slug>.<serve_domain>". Requires a wildcard DNS record and a
	// wildcard certificate for the domain. When empty, the daemon uses
	// "localhost" for loopback binds and port-based serving otherwise.
	ServeDomain string `json:"serve_domain,omitempty"`
	// ServeBind is the listen address for port-based serving (one listener per
	// exposure). Defaults to 127.0.0.1; set 0.0.0.0 to accept public traffic.
	ServeBind string `json:"serve_bind,omitempty"`
	// ServePortStart/ServePortEnd bound the port-based serve range.
	ServePortStart int `json:"serve_port_start,omitempty"`
	ServePortEnd   int `json:"serve_port_end,omitempty"`
	// ServeTLSCertFile/ServeTLSKeyFile enable HTTPS on port-based serve
	// listeners. Empty falls back to the API listener's TLS files.
	ServeTLSCertFile string `json:"serve_tls_cert_file,omitempty"`
	ServeTLSKeyFile  string `json:"serve_tls_key_file,omitempty"`
	// ServeTokenKey signs short-lived tokens for private serve access. When
	// empty it is derived from AdminSecret, else generated per process.
	ServeTokenKey string `json:"serve_token_key,omitempty"`
	// EdgeListen optionally starts an HTTP edge listener (default ":80" when
	// DNS is enabled) that dispatches by Host header: the reserved "api" and
	// apex/gui/www subdomains of the base domain are served by the daemon
	// itself, every other subdomain goes to the sandbox ingress wildcard.
	// Set to "" (or "-") to disable.
	EdgeListen string `json:"edge_listen,omitempty"`
	// TLSCertFile and TLSKeyFile optionally enable native HTTPS on the API
	// listener. Leave empty to terminate TLS in a reverse proxy or Tailscale.
	TLSCertFile string `json:"tls_cert_file,omitempty"`
	TLSKeyFile  string `json:"tls_key_file,omitempty"`
	// TrustedProxies is a comma-separated list of CIDRs from which proxy headers
	// (X-Forwarded-For, X-Real-IP, X-Forwarded-Host) are trusted.
	TrustedProxies string `json:"trusted_proxies,omitempty"`
	// Rate limiting configuration.
	RateLimitEnabled   *bool   `json:"rate_limit_enabled,omitempty"`
	RateLimitAuthRate  float64 `json:"rate_limit_auth_rate,omitempty"`
	RateLimitAuthBurst int     `json:"rate_limit_auth_burst,omitempty"`
	RateLimitAPIRate   float64 `json:"rate_limit_api_rate,omitempty"`
	RateLimitAPIBurst  int     `json:"rate_limit_api_burst,omitempty"`
}

// IsRateLimitEnabled reports whether token-bucket rate limiting is active.
func (c ServerConfig) IsRateLimitEnabled() bool {
	if c.RateLimitEnabled != nil {
		return *c.RateLimitEnabled
	}
	return true
}

// NormalizedPublicURL returns the configured externally reachable base URL
// without a trailing slash. Values without a scheme are assumed to be HTTPS
// (the common case for a reverse proxy or tailnet name).
func (c ServerConfig) NormalizedPublicURL() string {
	u := strings.TrimSpace(c.PublicURL)
	if u == "" {
		return ""
	}
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	return strings.TrimRight(u, "/")
}

// PublicURLHost returns the host (no scheme or path) of the configured public
// URL, or "" when it is unset.
func (c ServerConfig) PublicURLHost() string {
	u := c.NormalizedPublicURL()
	if u == "" {
		return ""
	}
	rest := u
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if h, _, err := net.SplitHostPort(rest); err == nil {
		return h
	}
	return rest
}

// ListenHost returns the host part of the API listen address.
func (c ServerConfig) ListenHost() string {
	addr := strings.TrimPrefix(strings.TrimSpace(c.ListenAddr), "tcp://")
	if strings.HasPrefix(addr, "unix://") || addr == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// ServeMode reports how sandbox exposures are addressed: "subdomain" when a
// serve domain (explicit or localhost) is available, otherwise "port".
func (c GlobalConfig) ServeMode() string {
	if strings.TrimSpace(c.Server.ServeDomain) != "" {
		return "subdomain"
	}
	if host := c.Server.PublicURLHost(); host != "" && !isLoopbackHost(host) {
		return "port"
	}
	if isLoopbackHost(c.Server.ListenHost()) {
		return "subdomain"
	}
	return "port"
}

// ServeDomainResolved returns the subdomain suffix used in subdomain mode.
// Explicit configuration wins; loopback binds default to "localhost".
func (c GlobalConfig) ServeDomainResolved() string {
	if d := strings.Trim(strings.TrimSpace(c.Server.ServeDomain), "."); d != "" {
		return strings.ToLower(d)
	}
	if c.ServeMode() == "subdomain" {
		return "localhost"
	}
	return ""
}

// ServePortRange returns the inclusive port range for port-based serving.
func (c GlobalConfig) ServePortRange() (int, int) {
	start := c.Server.ServePortStart
	if start <= 0 {
		start = 18400
	}
	end := c.Server.ServePortEnd
	if end <= 0 {
		end = start + 100
	}
	if end < start {
		end = start
	}
	if start > 65535 {
		start = 65535
	}
	if end > 65535 {
		end = 65535
	}
	return start, end
}

// ServeScheme returns the scheme used for generated serve URLs. Port-mode
// listeners speak plain HTTP unless serve TLS is configured; subdomain mode
// assumes TLS in front (wildcard certificate or reverse proxy).
func (c GlobalConfig) ServeScheme() string {
	if strings.TrimSpace(c.Server.ServeTLSCertFile) != "" || strings.TrimSpace(c.Server.ServeTLSKeyFile) != "" {
		return "https"
	}
	if c.ServeMode() == "subdomain" {
		if strings.HasPrefix(c.Server.NormalizedPublicURL(), "https://") {
			return "https"
		}
		if domain := c.ServeDomainResolved(); domain != "" && !isLoopbackHost(domain) {
			return "https"
		}
	}
	return "http"
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
	ConfigPath string         `json:"config_path,omitempty"`
	Server     ServerConfig   `json:"server"`
	Network    network.Config `json:"network"`
	Defaults   DefaultsConfig `json:"defaults"`
	Images     ImagesConfig   `json:"images"`
	Resources  ResourceLimits `json:"resources"`
	Jobs       JobsConfig     `json:"jobs"`
	DNS        DNSConfig      `json:"dns,omitempty"`
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
			// Bind loopback by default: a fresh install must not expose the
			// API/GUI to the network until the operator sets COMPUTE_BIND_ADDRESS.
			ListenAddr:     "127.0.0.1:18080",
			DBPath:         "/var/lib/compute/data/compute.db",
			FirecrackerBin: "/usr/local/bin/firecracker",
			SignupEnabled:  false,
			AdminUsername:  "admin",
			AdminEmail:     "admin@compute.local",
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
		if err := ApplyEnvOverrides(&cfg); err != nil {
			return cfg, err
		}
		return cfg, nil
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("failed to parse global config %s: %w", path, err)
	}

	// Set resolved config path
	cfg.ConfigPath = path

	// Normalize defaults if omitted in custom config
	if cfg.Server.ListenAddr == "" {
		cfg.Server.ListenAddr = DefaultConfig().Server.ListenAddr
	}
	if cfg.Server.DBPath == "" {
		cfg.Server.DBPath = "/var/lib/compute/data/compute.db"
	}
	if cfg.Server.FirecrackerBin == "" {
		cfg.Server.FirecrackerBin = "/usr/local/bin/firecracker"
	}
	if strings.TrimSpace(cfg.Server.AdminUsername) == "" {
		cfg.Server.AdminUsername = "admin"
	}
	if strings.TrimSpace(cfg.Server.AdminEmail) == "" {
		cfg.Server.AdminEmail = "admin@compute.local"
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

	// Environment overrides are applied for every resolve path, including the
	// no-config-file fallback, so `docker compose` can configure the daemon
	// entirely from .env variables.
	if err := ApplyEnvOverrides(&cfg); err != nil {
		return cfg, err
	}

	for _, cidr := range cfg.Network.EgressDenyCIDRs {
		cidr = strings.TrimSpace(cidr)
		if cidr != "" {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return cfg, fmt.Errorf("invalid network.egress_deny_cidrs %q in config %s: %w", cidr, path, err)
			}
		}
	}
	for _, cidr := range cfg.Network.EgressAllowCIDRs {
		cidr = strings.TrimSpace(cidr)
		if cidr != "" {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return cfg, fmt.Errorf("invalid network.egress_allow_cidrs %q in config %s: %w", cidr, path, err)
			}
		}
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
		"/etc/compute/daemon.json",
	}

	for _, p := range candidatePaths {
		if _, err := os.Stat(p); err == nil {
			cfg, err := LoadConfigFile(p)
			if err != nil {
				return cfg, p, err
			}
			cfg.ConfigPath = p
			return cfg, p, nil
		}
	}

	cfg := DefaultConfig()
	if err := ApplyEnvOverrides(&cfg); err != nil {
		return cfg, "", err
	}
	return cfg, "", nil
}
