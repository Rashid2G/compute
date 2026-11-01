package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Rashid2G/on-demand-vms/internal/network"
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

	// Disks limited to 50GB or 100GB
	if diskMB <= 0 {
		diskMB = DiskSize50GB
	} else if diskMB == 50 || diskMB == 51200 || diskMB == 50000 {
		diskMB = DiskSize50GB
	} else if diskMB == 100 || diskMB == 102400 || diskMB == 100000 {
		diskMB = DiskSize100GB
	} else {
		return 0, 0, 0, fmt.Errorf("invalid disk size '%d MB': disks are limited to 50gb (%d MB) or 100gb (%d MB)", diskMB, DiskSize50GB, DiskSize100GB)
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

// ServerConfig holds daemon server settings
type ServerConfig struct {
	ListenAddr     string `json:"listen"`
	DBPath         string `json:"db_path"`
	FirecrackerBin string `json:"firecracker_bin"`
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
}

// DefaultConfig returns standard out-of-the-box configuration
func DefaultConfig() GlobalConfig {
	return GlobalConfig{
		Server: ServerConfig{
			ListenAddr:     "0.0.0.0:18080",
			DBPath:         "/var/lib/compute/data/compute.db",
			FirecrackerBin: "/usr/local/bin/firecracker",
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
