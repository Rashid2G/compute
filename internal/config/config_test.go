package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Rashid2G/on-demand-vms/internal/network"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Server.ListenAddr != "127.0.0.1:18080" {
		t.Fatalf("expected 127.0.0.1:18080, got %s", cfg.Server.ListenAddr)
	}
	if cfg.Network.Mode != network.ModeNAT {
		t.Fatalf("expected mode nat, got %s", cfg.Network.Mode)
	}
	if cfg.Defaults.VCPUs != 1 || cfg.Defaults.MemoryMB != 2048 {
		t.Fatalf("unexpected defaults: %+v", cfg.Defaults)
	}
	if cfg.Defaults.DiskSizeMB != DiskSize50GB {
		t.Fatalf("expected %d disk, got %d", DiskSize50GB, cfg.Defaults.DiskSizeMB)
	}
	if cfg.Defaults.Image != "ubuntu-24" {
		t.Fatalf("expected image ubuntu-24, got %s", cfg.Defaults.Image)
	}
	if cfg.Resources.MinFreeDiskSpaceMB != 512 {
		t.Fatalf("expected 512MB min free, got %d", cfg.Resources.MinFreeDiskSpaceMB)
	}
	if !cfg.Server.CgroupEnforce {
		t.Fatalf("expected CgroupEnforce to be true by default")
	}
	if cfg.Server.JailerEnabled {
		t.Fatalf("expected JailerEnabled to be false by default")
	}
	if cfg.Server.JailerBinary != "/usr/local/bin/jailer" {
		t.Fatalf("expected default JailerBinary /usr/local/bin/jailer, got %s", cfg.Server.JailerBinary)
	}
	if cfg.Server.JailerBaseDir != "/srv/jailer" {
		t.Fatalf("expected default JailerBaseDir /srv/jailer, got %s", cfg.Server.JailerBaseDir)
	}
	if cfg.Server.AdminUsername != "admin" {
		t.Fatalf("expected default AdminUsername admin, got %s", cfg.Server.AdminUsername)
	}
	if cfg.Server.AdminEmail != "admin@compute.local" {
		t.Fatalf("expected default AdminEmail admin@compute.local, got %s", cfg.Server.AdminEmail)
	}
	if cfg.Server.SignupEnabled {
		t.Fatalf("expected SignupEnabled to be false by default")
	}
}

func TestTiersAndValidation(t *testing.T) {
	tiers := GetSupportedTiers()
	if len(tiers) != 3 {
		t.Fatalf("expected 3 fixed tiers, got %d", len(tiers))
	}

	// Valid tiers: 1, 2, 4, 8, 16
	v, m, d, err := ValidateAndNormalizeSpecs(1, 0, 0)
	if err != nil || v != 1 || m != 2048 || d != DiskSize50GB {
		t.Fatalf("tier 1 normalization failed: v=%d m=%d d=%d err=%v", v, m, d, err)
	}

	v, m, d, err = ValidateAndNormalizeSpecs(2, 0, 0)
	if err != nil || v != 2 || m != 4096 || d != DiskSize50GB {
		t.Fatalf("tier 2 normalization failed: v=%d m=%d d=%d err=%v", v, m, d, err)
	}

	v, m, d, err = ValidateAndNormalizeSpecs(4, 0, 102400)
	if err != nil || v != 4 || m != 8192 || d != DiskSize100GB {
		t.Fatalf("tier 4 normalization failed: v=%d m=%d d=%d err=%v", v, m, d, err)
	}

	v, m, d, err = ValidateAndNormalizeSpecs(8, 0, 0)
	if err != nil || v != 8 || m != 16384 || d != DiskSize50GB {
		t.Fatalf("tier 8 normalization failed: v=%d m=%d d=%d err=%v", v, m, d, err)
	}

	v, m, d, err = ValidateAndNormalizeSpecs(16, 0, 0)
	if err != nil || v != 16 || m != 32768 || d != DiskSize50GB {
		t.Fatalf("tier 16 normalization failed: v=%d m=%d d=%d err=%v", v, m, d, err)
	}

	// 100GB via 100
	v, m, d, err = ValidateAndNormalizeSpecs(2, 2048, 100)
	if err != nil || v != 2 || m != 2048 || d != DiskSize100GB {
		t.Fatalf("100GB normalization failed: v=%d m=%d d=%d err=%v", v, m, d, err)
	}

	// Invalid vCPUs
	_, _, _, err = ValidateAndNormalizeSpecs(3, 0, 0)
	if err == nil {
		t.Fatalf("expected error for 3 vcpus, got nil")
	}

	_, _, _, err = ValidateAndNormalizeSpecs(32, 0, 0)
	if err == nil {
		t.Fatalf("expected error for 32 vcpus, got nil")
	}

	// Custom disk sizes: 2048 MB, 20 GB, 100 GB
	v, m, d, err = ValidateAndNormalizeSpecs(1, 0, 2048)
	if err != nil || v != 1 || m != 2048 || d != 2048 {
		t.Fatalf("custom 2048 MB disk normalization failed: v=%d m=%d d=%d err=%v", v, m, d, err)
	}

	v, m, d, err = ValidateAndNormalizeSpecs(2, 0, 20)
	if err != nil || v != 2 || m != 4096 || d != 20480 {
		t.Fatalf("custom 20 GB disk normalization failed: v=%d m=%d d=%d err=%v", v, m, d, err)
	}

	// Invalid disk size (< 512 MB or negative)
	_, _, _, err = ValidateAndNormalizeSpecs(1, 0, -10)
	// negative defaults to DiskSize50GB
	if err != nil {
		t.Fatalf("expected negative disk to default, got err: %v", err)
	}

	// ValidateImage
	img, err := ValidateImage("ubuntu-24")
	if err != nil || img != "ubuntu-24" {
		t.Fatalf("expected valid ubuntu-24, got %s, err: %v", img, err)
	}
	img, err = ValidateImage("ubuntu-24.04")
	if err != nil || img != "ubuntu-24" {
		t.Fatalf("expected valid ubuntu-24, got %s, err: %v", img, err)
	}
	_, err = ValidateImage("alpine")
	if err == nil {
		t.Fatalf("expected error for alpine, got nil")
	}
}

func TestLoadGlobalConfigFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "daemon.json")

	content := `{
		"server": {
			"listen": "127.0.0.1:19090",
			"db_path": "/tmp/test.db",
			"firecracker_bin": "/usr/bin/firecracker"
		},
		"network": {
			"mode": "wired",
			"bridge_name": "br-wired",
			"physical_nic": "eth1",
			"subnet": "192.168.10.0/24",
			"gateway_ip": "192.168.10.1"
		},
		"defaults": {
			"vcpus": 4,
			"memory_mb": 2048,
			"disk_size_mb": 51200,
			"image": "ubuntu-24"
		},
		"resources": {
			"max_running_vms": 3,
			"max_total_vcpus": 8,
			"max_total_memory_mb": 8192,
			"max_total_disk_mb": 20480,
			"min_free_disk_space_mb": 1024
		}
	}`

	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfigFile(configPath)
	if err != nil {
		t.Fatalf("LoadConfigFile error = %v", err)
	}

	if cfg.ConfigPath != configPath {
		t.Fatalf("expected ConfigPath %s, got %s", configPath, cfg.ConfigPath)
	}
	if cfg.Server.ListenAddr != "127.0.0.1:19090" {
		t.Fatalf("expected listen addr 127.0.0.1:19090, got %s", cfg.Server.ListenAddr)
	}
	if cfg.Network.Mode != network.ModeWired {
		t.Fatalf("expected mode wired, got %s", cfg.Network.Mode)
	}
	if cfg.Network.GatewayIP != "192.168.10.1" {
		t.Fatalf("expected gateway 192.168.10.1, got %s", cfg.Network.GatewayIP)
	}
	if cfg.Defaults.VCPUs != 4 || cfg.Defaults.MemoryMB != 2048 || cfg.Defaults.Image != "ubuntu-24" {
		t.Fatalf("unexpected defaults: %+v", cfg.Defaults)
	}
	if cfg.Resources.MaxRunningVMs != 3 || cfg.Resources.MaxTotalVCPUs != 8 {
		t.Fatalf("unexpected resources: %+v", cfg.Resources)
	}
}

func TestLoadLegacyNetworkConfig(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "network.json")

	content := `{
		"mode": "nat",
		"bridge_name": "mft3br0",
		"bridge_ip": "172.16.0.1/16",
		"subnet": "172.16.0.0/16"
	}`

	if err := os.WriteFile(legacyPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfigFile(legacyPath)
	if err != nil {
		t.Fatalf("LoadConfigFile legacy error = %v", err)
	}

	if cfg.ConfigPath != legacyPath {
		t.Fatalf("expected ConfigPath %s, got %s", legacyPath, cfg.ConfigPath)
	}
	if cfg.Network.Mode != network.ModeNAT {
		t.Fatalf("expected mode nat, got %s", cfg.Network.Mode)
	}
	if cfg.Network.BridgeName != "mft3br0" {
		t.Fatalf("expected bridge mft3br0, got %s", cfg.Network.BridgeName)
	}
	if cfg.Network.BridgeIP != "172.16.0.1/16" {
		t.Fatalf("expected bridge ip 172.16.0.1/16, got %s", cfg.Network.BridgeIP)
	}
	if cfg.Network.SubnetCIDR != "172.16.0.0/16" {
		t.Fatalf("expected subnet 172.16.0.0/16, got %s", cfg.Network.SubnetCIDR)
	}
}

func TestResolveConfigPath(t *testing.T) {
	dir := t.TempDir()
	customPath := filepath.Join(dir, "custom.json")
	if err := os.WriteFile(customPath, []byte(`{"server":{"listen":"127.0.0.1:12345"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, resolvedPath, err := ResolveConfig(customPath)
	if err != nil {
		t.Fatalf("ResolveConfig error = %v", err)
	}
	if resolvedPath != customPath {
		t.Fatalf("expected resolvedPath %s, got %s", customPath, resolvedPath)
	}
	if cfg.ConfigPath != customPath {
		t.Fatalf("expected cfg.ConfigPath %s, got %s", customPath, cfg.ConfigPath)
	}
}
