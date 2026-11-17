package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rashid2G/compute/internal/network"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"COMPUTE_LISTEN", "COMPUTE_BIND_ADDRESS", "COMPUTE_API_PORT",
		"COMPUTE_DB_PATH", "COMPUTE_FIRECRACKER_BIN", "COMPUTE_AUTH_ENABLED",
		"COMPUTE_SIGNUP_ENABLED", "COMPUTE_ADMIN_SECRET", "COMPUTE_ADMIN_USERNAME",
		"COMPUTE_ADMIN_EMAIL", "COMPUTE_ADMIN_PASSWORD", "COMPUTE_EDGE_LISTEN",
		"COMPUTE_PUBLIC_URL",
		"COMPUTE_CGROUP_ENFORCE", "COMPUTE_JAILER_ENABLED", "COMPUTE_JAILER_BIN",
		"COMPUTE_JAILER_DIR", "COMPUTE_TLS_CERT_FILE", "COMPUTE_TLS_KEY_FILE",
		"COMPUTE_NETWORK_MODE", "COMPUTE_BRIDGE_NAME", "COMPUTE_BRIDGE_IP",
		"COMPUTE_SUBNET", "COMPUTE_GATEWAY_IP", "COMPUTE_PHYSICAL_NIC",
		"COMPUTE_NETWORK_BASE_DOMAIN", "COMPUTE_DEFAULT_VCPUS",
		"COMPUTE_DEFAULT_MEMORY_MB", "COMPUTE_DEFAULT_DISK_MB",
		"COMPUTE_DEFAULT_IMAGE", "COMPUTE_IMAGES_DIR", "COMPUTE_VMS_DIR",
		"COMPUTE_DEFAULT_KERNEL", "COMPUTE_DEFAULT_ROOTFS", "COMPUTE_SSH_KEY_PATH",
		"COMPUTE_MAX_RUNNING_VMS", "COMPUTE_MAX_TOTAL_VCPUS",
		"COMPUTE_MAX_TOTAL_MEMORY_MB", "COMPUTE_MAX_TOTAL_DISK_MB",
		"COMPUTE_MIN_FREE_DISK_MB", "COMPUTE_JOBS_LOCAL_EXECUTION",
		"COMPUTE_JOBS_MAX_CONCURRENT", "COMPUTE_JOBS_BOOT_TIMEOUT_SECONDS",
		"COMPUTE_DNS_ENABLED", "COMPUTE_DNS_LISTEN", "COMPUTE_DNS_BASE_DOMAIN",
		"COMPUTE_DNS_EXTRA_BASE_DOMAINS", "COMPUTE_DNS_HOST_ADDRESSES",
		"COMPUTE_TAILSCALE_ENABLED", "COMPUTE_TAILSCALE_API_KEY",
		"COMPUTE_TAILSCALE_TAILNET", "COMPUTE_TAILSCALE_NODE_TAG",
		"COMPUTE_TAILSCALE_SANDBOX_TAG", "COMPUTE_TAILSCALE_HOSTNAME_PREFIX",
		"COMPUTE_TAILSCALE_KEY_EXPIRY_SECONDS", "COMPUTE_TAILSCALE_DEFAULT_SANDBOXES",
		"COMPUTE_TAILSCALE_LEGACY_PER_SANDBOX_DEVICES",
		"COMPUTE_TAILSCALE_SERVICE_GATEWAY_ENABLED",
		"COMPUTE_TAILSCALE_SERVICE_GATEWAY_TARGET_ADDRESS",
		"COMPUTE_DATA_DIR",
	} {
		t.Setenv(key, "")
	}
}

func TestEnvOverridesApplyWithoutConfigFile(t *testing.T) {
	clearEnv(t)
	t.Chdir(t.TempDir())
	t.Setenv("COMPUTE_BIND_ADDRESS", "0.0.0.0")
	t.Setenv("COMPUTE_API_PORT", "19090")
	t.Setenv("COMPUTE_AUTH_ENABLED", "true")
	t.Setenv("COMPUTE_SIGNUP_ENABLED", "true")
	t.Setenv("COMPUTE_ADMIN_USERNAME", "rootadmin")
	t.Setenv("COMPUTE_ADMIN_EMAIL", "ops@compute.local")
	t.Setenv("COMPUTE_ADMIN_PASSWORD", "test-password")
	t.Setenv("COMPUTE_DEFAULT_VCPUS", "2")
	t.Setenv("COMPUTE_DEFAULT_MEMORY_MB", "4096")
	t.Setenv("COMPUTE_DNS_ENABLED", "yes")
	t.Setenv("COMPUTE_DNS_BASE_DOMAIN", "example.test")
	t.Setenv("COMPUTE_DNS_EXTRA_BASE_DOMAINS", "a.test, b.test")
	t.Setenv("COMPUTE_DNS_HOST_ADDRESSES", "10.0.0.1,10.0.0.2")

	cfg, path, err := ResolveConfig("")
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if path != "" {
		t.Fatalf("expected no config path, got %q", path)
	}
	if cfg.Server.ListenAddr != "0.0.0.0:19090" {
		t.Fatalf("listen = %q", cfg.Server.ListenAddr)
	}
	if !cfg.Server.AuthEnabled || !cfg.Server.SignupEnabled || cfg.Server.AdminUsername != "rootadmin" || cfg.Server.AdminEmail != "ops@compute.local" || cfg.Server.AdminPassword != "test-password" {
		t.Fatalf("auth env not applied: %+v", cfg.Server)
	}
	if cfg.Defaults.VCPUs != 2 || cfg.Defaults.MemoryMB != 4096 {
		t.Fatalf("defaults env not applied: %+v", cfg.Defaults)
	}
	if !cfg.DNS.Enabled || cfg.DNS.BaseDomain != "example.test" {
		t.Fatalf("dns env not applied: %+v", cfg.DNS)
	}
	if got := strings.Join(cfg.DNS.ExtraBaseDomains, "|"); got != "a.test|b.test" {
		t.Fatalf("extra domains = %q", got)
	}
	if got := strings.Join(cfg.DNS.HostAddresses, "|"); got != "10.0.0.1|10.0.0.2" {
		t.Fatalf("host addresses = %q", got)
	}
}

func TestEnvOverridesWinOverConfigFile(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.json")
	content := `{
		"server": {"listen": "127.0.0.1:1111", "auth_enabled": false},
		"network": {"mode": "nat", "bridge_name": "br-file"},
		"defaults": {"vcpus": 1},
		"dns": {"enabled": true, "base_domain": "file.test"}
	}`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMPUTE_LISTEN", "127.0.0.1:2222")
	t.Setenv("COMPUTE_NETWORK_MODE", "bridge")
	t.Setenv("COMPUTE_BRIDGE_NAME", "br-env")
	t.Setenv("COMPUTE_DEFAULT_VCPUS", "4")
	t.Setenv("COMPUTE_DNS_ENABLED", "false")
	t.Setenv("COMPUTE_PUBLIC_URL", "https://compute-lab.tailb1be99.ts.net/")

	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if cfg.Server.ListenAddr != "127.0.0.1:2222" {
		t.Fatalf("listen = %q", cfg.Server.ListenAddr)
	}
	if cfg.Network.Mode != network.ModeBridge || cfg.Network.BridgeName != "br-env" {
		t.Fatalf("network = %+v", cfg.Network)
	}
	if cfg.Defaults.VCPUs != 4 {
		t.Fatalf("vcpus = %d", cfg.Defaults.VCPUs)
	}
	if cfg.DNS.Enabled {
		t.Fatalf("expected DNS disabled by env")
	}
	if got := cfg.Server.NormalizedPublicURL(); got != "https://compute-lab.tailb1be99.ts.net" {
		t.Fatalf("public url = %q", got)
	}
	if cfg.DNS.BaseDomain != "file.test" {
		t.Fatalf("base domain should retain file value, got %q", cfg.DNS.BaseDomain)
	}
}

func TestEnvOverridesRejectMalformedValues(t *testing.T) {
	cases := []struct{ key, value, want string }{
		{"COMPUTE_AUTH_ENABLED", "maybe", "COMPUTE_AUTH_ENABLED"},
		{"COMPUTE_API_PORT", "eighty", "COMPUTE_API_PORT"},
		{"COMPUTE_DEFAULT_VCPUS", "many", "COMPUTE_DEFAULT_VCPUS"},
		{"COMPUTE_MIN_FREE_DISK_MB", "1.5", "COMPUTE_MIN_FREE_DISK_MB"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			clearEnv(t)
			cfg := DefaultConfig()
			t.Setenv(tc.key, tc.value)
			err := ApplyEnvOverrides(&cfg)
			if err == nil {
				t.Fatalf("expected error for %s=%s", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %s", err, tc.want)
			}
		})
	}
}

func TestDataDirCompatibilityRoot(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	cfg := DefaultConfig()
	t.Setenv("COMPUTE_DATA_DIR", dir)
	if err := ApplyEnvOverrides(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.DBPath != filepath.Join(dir, "data", "compute.db") {
		t.Fatalf("db path = %q", cfg.Server.DBPath)
	}
	if cfg.Images.ImagesDir != filepath.Join(dir, "images") {
		t.Fatalf("images dir = %q", cfg.Images.ImagesDir)
	}
	if cfg.Images.VMsDir != filepath.Join(dir, "vms") {
		t.Fatalf("vms dir = %q", cfg.Images.VMsDir)
	}
	if cfg.Images.SSHKeyPath != filepath.Join(dir, "images", "ssh", "id_ed25519") {
		t.Fatalf("ssh key = %q", cfg.Images.SSHKeyPath)
	}

	// Explicit path variables win over the compatibility root.
	t.Setenv("COMPUTE_IMAGES_DIR", "/srv/images")
	if err := ApplyEnvOverrides(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Images.ImagesDir != "/srv/images" {
		t.Fatalf("explicit images dir lost: %q", cfg.Images.ImagesDir)
	}
}
