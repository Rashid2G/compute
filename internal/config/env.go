package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/network"
)

// ApplyEnvOverrides applies COMPUTE_* environment variables to cfg.
//
// Precedence is: built-in defaults -> JSON config file -> environment ->
// command-line flags. Callers apply CLI flags after this function. Environment
// overrides are applied even when no config file exists, so `computed` can be
// configured entirely from a `.env`-style environment.
//
// COMPUTE_DATA_DIR is kept as a compatibility root: it relocates the default
// database, image, VM and SSH-key paths. Explicit path variables always win.
// Malformed values fail fast and name the offending variable.
func ApplyEnvOverrides(cfg *GlobalConfig) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	env := envReader{}

	// Compatibility root first, so explicit path variables below can win.
	if dataDir := strings.TrimSpace(os.Getenv("COMPUTE_DATA_DIR")); dataDir != "" {
		if cfg.Server.DBPath == "" || cfg.Server.DBPath == "/var/lib/compute/data/compute.db" || strings.HasPrefix(cfg.Server.DBPath, "/home/") {
			cfg.Server.DBPath = filepath.Join(dataDir, "data", "compute.db")
		}
		if cfg.Images.ImagesDir == "" || cfg.Images.ImagesDir == "/var/lib/compute/images" || strings.HasPrefix(cfg.Images.ImagesDir, "/home/") {
			cfg.Images.ImagesDir = filepath.Join(dataDir, "images")
		}
		if cfg.Images.VMsDir == "" || cfg.Images.VMsDir == "/var/lib/compute/vms" || strings.HasPrefix(cfg.Images.VMsDir, "/home/") {
			cfg.Images.VMsDir = filepath.Join(dataDir, "vms")
		}
		if cfg.Images.SSHKeyPath == "" || cfg.Images.SSHKeyPath == "/var/lib/compute/images/ssh/id_ed25519" || strings.HasPrefix(cfg.Images.SSHKeyPath, "/home/") {
			cfg.Images.SSHKeyPath = filepath.Join(dataDir, "images", "ssh", "id_ed25519")
		}
	}

	// Server / API.
	listen := env.str("COMPUTE_LISTEN")
	bind := env.str("COMPUTE_BIND_ADDRESS")
	port := env.intValue("COMPUTE_API_PORT")
	if listen != "" {
		cfg.Server.ListenAddr = listen
	} else if bind != "" || port != 0 {
		host := bind
		if host == "" {
			host = "127.0.0.1"
		}
		if port == 0 {
			port = 18080
		}
		cfg.Server.ListenAddr = net.JoinHostPort(host, strconv.Itoa(port))
	}
	env.strInto("COMPUTE_DB_PATH", &cfg.Server.DBPath)
	env.strInto("COMPUTE_FIRECRACKER_BIN", &cfg.Server.FirecrackerBin)
	env.boolInto("COMPUTE_AUTH_ENABLED", &cfg.Server.AuthEnabled)
	env.strInto("COMPUTE_ADMIN_SECRET", &cfg.Server.AdminSecret)
	env.strInto("COMPUTE_ADMIN_PASSWORD", &cfg.Server.AdminPassword)
	env.strInto("COMPUTE_EDGE_LISTEN", &cfg.Server.EdgeListen)
	env.boolInto("COMPUTE_CGROUP_ENFORCE", &cfg.Server.CgroupEnforce)
	env.boolInto("COMPUTE_JAILER_ENABLED", &cfg.Server.JailerEnabled)
	env.strInto("COMPUTE_JAILER_BIN", &cfg.Server.JailerBinary)
	env.strInto("COMPUTE_JAILER_DIR", &cfg.Server.JailerBaseDir)
	env.strInto("COMPUTE_TLS_CERT_FILE", &cfg.Server.TLSCertFile)
	env.strInto("COMPUTE_TLS_KEY_FILE", &cfg.Server.TLSKeyFile)

	// Network.
	if v := env.str("COMPUTE_NETWORK_MODE"); v != "" {
		cfg.Network.Mode = network.Mode(strings.ToLower(v))
	}
	env.strInto("COMPUTE_BRIDGE_NAME", &cfg.Network.BridgeName)
	env.strInto("COMPUTE_BRIDGE_IP", &cfg.Network.BridgeIP)
	env.strInto("COMPUTE_SUBNET", &cfg.Network.SubnetCIDR)
	env.strInto("COMPUTE_GATEWAY_IP", &cfg.Network.GatewayIP)
	env.strInto("COMPUTE_PHYSICAL_NIC", &cfg.Network.PhysicalNIC)
	env.strInto("COMPUTE_NETWORK_BASE_DOMAIN", &cfg.Network.BaseDomain)
	env.boolInto("COMPUTE_LINK_OTHER_BRIDGES", &cfg.Network.LinkAllBridges)
	env.csvInto("COMPUTE_LINKED_BRIDGES", &cfg.Network.LinkedBridges)

	// Default VM specs.
	env.intInto("COMPUTE_DEFAULT_VCPUS", &cfg.Defaults.VCPUs)
	env.intInto("COMPUTE_DEFAULT_MEMORY_MB", &cfg.Defaults.MemoryMB)
	env.intInto("COMPUTE_DEFAULT_DISK_MB", &cfg.Defaults.DiskSizeMB)
	env.strInto("COMPUTE_DEFAULT_IMAGE", &cfg.Defaults.Image)

	// Images.
	env.strInto("COMPUTE_IMAGES_DIR", &cfg.Images.ImagesDir)
	env.strInto("COMPUTE_VMS_DIR", &cfg.Images.VMsDir)
	env.strInto("COMPUTE_DEFAULT_KERNEL", &cfg.Images.DefaultKernel)
	env.strInto("COMPUTE_DEFAULT_ROOTFS", &cfg.Images.DefaultRootfs)
	env.strInto("COMPUTE_SSH_KEY_PATH", &cfg.Images.SSHKeyPath)

	// Capacity.
	env.intInto("COMPUTE_MAX_RUNNING_VMS", &cfg.Resources.MaxRunningVMs)
	env.intInto("COMPUTE_MAX_TOTAL_VCPUS", &cfg.Resources.MaxTotalVCPUs)
	env.intInto("COMPUTE_MAX_TOTAL_MEMORY_MB", &cfg.Resources.MaxTotalMemoryMB)
	env.int64Into("COMPUTE_MAX_TOTAL_DISK_MB", &cfg.Resources.MaxTotalDiskMB)
	env.int64Into("COMPUTE_MIN_FREE_DISK_MB", &cfg.Resources.MinFreeDiskSpaceMB)

	// Jobs.
	env.boolInto("COMPUTE_JOBS_LOCAL_EXECUTION", &cfg.Jobs.LocalExecution)
	env.intInto("COMPUTE_JOBS_MAX_CONCURRENT", &cfg.Jobs.MaxConcurrent)
	env.durationSecondsInto("COMPUTE_JOBS_BOOT_TIMEOUT_SECONDS", &cfg.Jobs.BootTimeoutSeconds)

	// DNS.
	env.boolInto("COMPUTE_DNS_ENABLED", &cfg.DNS.Enabled)
	env.strInto("COMPUTE_DNS_LISTEN", &cfg.DNS.Listen)
	env.strInto("COMPUTE_DNS_BASE_DOMAIN", &cfg.DNS.BaseDomain)
	env.csvInto("COMPUTE_DNS_EXTRA_BASE_DOMAINS", &cfg.DNS.ExtraBaseDomains)
	env.csvInto("COMPUTE_DNS_HOST_ADDRESSES", &cfg.DNS.HostAddresses)

	// Tailscale.
	env.boolInto("COMPUTE_TAILSCALE_ENABLED", &cfg.Tailscale.Enabled)
	env.strInto("COMPUTE_TAILSCALE_API_KEY", &cfg.Tailscale.APIKey)
	env.strInto("COMPUTE_TAILSCALE_TAILNET", &cfg.Tailscale.Tailnet)
	env.strInto("COMPUTE_TAILSCALE_NODE_TAG", &cfg.Tailscale.NodeTag)
	env.strInto("COMPUTE_TAILSCALE_SANDBOX_TAG", &cfg.Tailscale.SandboxTag)
	env.strInto("COMPUTE_TAILSCALE_HOSTNAME_PREFIX", &cfg.Tailscale.HostnamePrefix)
	env.intInto("COMPUTE_TAILSCALE_KEY_EXPIRY_SECONDS", &cfg.Tailscale.KeyExpirySeconds)
	env.boolInto("COMPUTE_TAILSCALE_DEFAULT_SANDBOXES", &cfg.Tailscale.DefaultSandboxes)
	env.boolInto("COMPUTE_TAILSCALE_LEGACY_PER_SANDBOX_DEVICES", &cfg.Tailscale.LegacyPerSandboxDevices)
	env.strInto("COMPUTE_TAILSCALE_SERVICE_GATEWAY_TARGET_ADDRESS", gatewayTarget(cfg))

	if gateway := env.boolValue("COMPUTE_TAILSCALE_SERVICE_GATEWAY_ENABLED"); gateway != nil {
		if cfg.Tailscale.ServiceGateway == nil {
			cfg.Tailscale.ServiceGateway = &ServiceGatewayConfig{}
		}
		cfg.Tailscale.ServiceGateway.Enabled = *gateway
	}
	if v := env.str("COMPUTE_TAILSCALE_SERVICE_GATEWAY_HOST_TAG"); v != "" {
		if cfg.Tailscale.ServiceGateway == nil {
			cfg.Tailscale.ServiceGateway = &ServiceGatewayConfig{}
		}
		cfg.Tailscale.ServiceGateway.HostTag = v
	}
	if v := env.str("COMPUTE_TAILSCALE_SERVICE_GATEWAY_SERVICE_TAG"); v != "" {
		if cfg.Tailscale.ServiceGateway == nil {
			cfg.Tailscale.ServiceGateway = &ServiceGatewayConfig{}
		}
		cfg.Tailscale.ServiceGateway.ServiceTag = v
	}

	return env.err()
}

// gatewayTarget returns a stable address for the optional gateway target.
func gatewayTarget(cfg *GlobalConfig) *string {
	if cfg.Tailscale.ServiceGateway == nil {
		cfg.Tailscale.ServiceGateway = &ServiceGatewayConfig{}
	}
	return &cfg.Tailscale.ServiceGateway.TargetAddress
}

type envReader struct {
	errs []error
}

func (e *envReader) err() error {
	return errors.Join(e.errs...)
}

func (e *envReader) fail(name, expected, value string) {
	e.errs = append(e.errs, fmt.Errorf("invalid %s=%q: expected %s", name, value, expected))
}

// str returns a trimmed non-empty value, or "" when unset/empty.
func (e *envReader) str(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

func (e *envReader) strInto(name string, dst *string) {
	if v, ok := os.LookupEnv(name); ok {
		v = strings.TrimSpace(v)
		if v != "" {
			*dst = v
		}
	}
}

func (e *envReader) boolInto(name string, dst *bool) {
	if v := e.boolValue(name); v != nil {
		*dst = *v
	}
}

func (e *envReader) boolValue(name string) *bool {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		v := true
		return &v
	case "0", "false", "no", "off":
		v := false
		return &v
	default:
		e.fail(name, "a boolean (true/false, 1/0, yes/no, on/off)", raw)
		return nil
	}
}

func (e *envReader) intInto(name string, dst *int) {
	if v := e.intValue(name); v != 0 {
		*dst = v
	}
}

func (e *envReader) intValue(name string) int {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return 0
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		e.fail(name, "an integer", raw)
		return 0
	}
	return v
}

func (e *envReader) int64Into(name string, dst *int64) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		e.fail(name, "an integer", raw)
		return
	}
	*dst = v
}

func (e *envReader) durationSecondsInto(name string, dst *int) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		e.fail(name, "a non-negative number of seconds", raw)
		return
	}
	*dst = v
}

func (e *envReader) csvInto(name string, dst *[]string) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		*dst = nil
		return
	}
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	*dst = out
}

// ParseDurationEnv parses a duration environment variable, returning def when
// unset. It is exported for callers outside config that need consistent errors.
func ParseDurationEnv(name string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return def, fmt.Errorf("invalid %s=%q: expected a duration such as 30s or 2m", name, raw)
	}
	return v, nil
}
