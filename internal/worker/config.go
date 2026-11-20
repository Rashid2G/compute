package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	ControlPlaneURL string `json:"control_plane"`
	WorkerID        string `json:"worker_id,omitempty"`
	Name            string `json:"name"`
	PoolID          string `json:"pool_id,omitempty"`
	Token           string `json:"token,omitempty"`
	StateDir        string `json:"state_dir,omitempty"`
	MaxVCPUs        int    `json:"max_vcpus,omitempty"`
	MaxMemoryMB     int    `json:"max_memory_mb,omitempty"`
	MaxDiskMB       int    `json:"max_disk_mb,omitempty"`

	// Host runtime. A worker executes jobs inside its own Firecracker VMs, so
	// it needs the same image, network and cgroup configuration as a daemon.
	FirecrackerBin        string   `json:"firecracker_bin,omitempty"`
	CgroupEnforce         bool     `json:"cgroup_enforce,omitempty"`
	ImagesDir             string   `json:"images_dir,omitempty"`
	VMsDir                string   `json:"vms_dir,omitempty"`
	SSHKeyPath            string   `json:"ssh_key_path,omitempty"`
	DefaultKernel         string   `json:"default_kernel,omitempty"`
	DefaultRootfs         string   `json:"default_rootfs,omitempty"`
	NetworkMode           string   `json:"network_mode,omitempty"`
	BridgeName            string   `json:"bridge_name,omitempty"`
	BridgeIP              string   `json:"bridge_ip,omitempty"`
	SubnetCIDR            string   `json:"subnet,omitempty"`
	GatewayIP             string   `json:"gateway_ip,omitempty"`
	EgressFirewallEnabled *bool    `json:"egress_firewall_enabled,omitempty"`
	EgressDenyCIDRs       []string `json:"egress_deny_cidrs,omitempty"`
	EgressAllowCIDRs      []string `json:"egress_allow_cidrs,omitempty"`
	BlockHostBridgePorts  *bool    `json:"block_host_bridge_ports,omitempty"`
	BootTimeout           int      `json:"boot_timeout_seconds,omitempty"`
	// AllowLocalExec runs assignments with the host shell instead of a microVM.
	// This is a development-only escape hatch and must never be used on a
	// shared or production worker.
	AllowLocalExec bool `json:"allow_local_exec,omitempty"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	if path != "" {
		b, e := os.ReadFile(path)
		if e != nil {
			return c, e
		}
		if strings.EqualFold(strings.TrimPrefix(strings.ToLower(path), "."), "toml") || strings.HasSuffix(strings.ToLower(path), ".toml") {
			parseTOML(b, &c)
		} else if e = json.Unmarshal(b, &c); e != nil {
			return c, e
		}
	}
	env := func(key string, dst *string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	env("COMPUTE_CONTROL_PLANE", &c.ControlPlaneURL)
	env("COMPUTE_WORKER_ID", &c.WorkerID)
	env("COMPUTE_WORKER_NAME", &c.Name)
	env("COMPUTE_POOL_ID", &c.PoolID)
	env("COMPUTE_WORKER_TOKEN", &c.Token)
	env("COMPUTE_WORKER_STATE_DIR", &c.StateDir)
	env("COMPUTE_FIRECRACKER_BIN", &c.FirecrackerBin)
	env("COMPUTE_IMAGES_DIR", &c.ImagesDir)
	env("COMPUTE_VMS_DIR", &c.VMsDir)
	env("COMPUTE_SSH_KEY_PATH", &c.SSHKeyPath)
	env("COMPUTE_DEFAULT_KERNEL", &c.DefaultKernel)
	env("COMPUTE_DEFAULT_ROOTFS", &c.DefaultRootfs)
	env("COMPUTE_NETWORK_MODE", &c.NetworkMode)
	env("COMPUTE_BRIDGE_NAME", &c.BridgeName)
	env("COMPUTE_BRIDGE_IP", &c.BridgeIP)
	env("COMPUTE_SUBNET", &c.SubnetCIDR)
	env("COMPUTE_GATEWAY_IP", &c.GatewayIP)
	if v := strings.TrimSpace(os.Getenv("COMPUTE_NETWORK_EGRESS_FIREWALL")); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			val := true
			c.EgressFirewallEnabled = &val
		case "0", "false", "no", "off":
			val := false
			c.EgressFirewallEnabled = &val
		}
	}
	if v := strings.TrimSpace(os.Getenv("COMPUTE_NETWORK_BLOCK_HOST_BRIDGE_PORTS")); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			val := true
			c.BlockHostBridgePorts = &val
		case "0", "false", "no", "off":
			val := false
			c.BlockHostBridgePorts = &val
		}
	}
	if v := strings.TrimSpace(os.Getenv("COMPUTE_NETWORK_EGRESS_DENY_CIDRS")); v != "" {
		var out []string
		for _, item := range strings.Split(v, ",") {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		c.EgressDenyCIDRs = out
	}
	if v := strings.TrimSpace(os.Getenv("COMPUTE_NETWORK_EGRESS_ALLOW_CIDRS")); v != "" {
		var out []string
		for _, item := range strings.Split(v, ",") {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		c.EgressAllowCIDRs = out
	}
	if v := strings.TrimSpace(os.Getenv("COMPUTE_WORKER_BOOT_TIMEOUT_SECONDS")); v != "" {
		var secs int
		if _, err := fmt.Sscanf(v, "%d", &secs); err != nil {
			return c, fmt.Errorf("invalid COMPUTE_WORKER_BOOT_TIMEOUT_SECONDS=%q: expected an integer", v)
		}
		c.BootTimeout = secs
	}
	if v := strings.TrimSpace(os.Getenv("COMPUTE_WORKER_MAX_VCPUS")); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return c, fmt.Errorf("invalid COMPUTE_WORKER_MAX_VCPUS=%q: expected an integer", v)
		}
		c.MaxVCPUs = n
	}
	if v := strings.TrimSpace(os.Getenv("COMPUTE_WORKER_MAX_MEMORY_MB")); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return c, fmt.Errorf("invalid COMPUTE_WORKER_MAX_MEMORY_MB=%q: expected an integer", v)
		}
		c.MaxMemoryMB = n
	}
	if v := strings.TrimSpace(os.Getenv("COMPUTE_WORKER_MAX_DISK_MB")); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return c, fmt.Errorf("invalid COMPUTE_WORKER_MAX_DISK_MB=%q: expected an integer", v)
		}
		c.MaxDiskMB = n
	}
	if v := strings.TrimSpace(os.Getenv("COMPUTE_WORKER_ALLOW_LOCAL_EXEC")); v != "" {
		c.AllowLocalExec = envBool(v)
	}
	if v := strings.TrimSpace(os.Getenv("COMPUTE_CGROUP_ENFORCE")); v != "" {
		c.CgroupEnforce = envBool(v)
	}
	if c.Name == "" {
		if hn, err := os.Hostname(); err == nil && strings.TrimSpace(hn) != "" {
			c.Name = strings.TrimSpace(hn)
		} else {
			c.Name = "compute-worker"
		}
	}
	if c.StateDir == "" {
		c.StateDir = "/var/lib/compute-worker"
	}
	if c.ImagesDir == "" {
		c.ImagesDir = "/var/lib/compute/images"
	}
	if c.VMsDir == "" {
		c.VMsDir = filepath.Join(c.StateDir, "vms")
	}
	if c.DefaultKernel == "" {
		c.DefaultKernel = "vmlinux"
	}
	if c.DefaultRootfs == "" {
		c.DefaultRootfs = "ubuntu-24.04.ext4"
	}
	if c.BootTimeout <= 0 {
		c.BootTimeout = 90
	}
	if c.ControlPlaneURL == "" {
		return c, errors.New("control plane URL is required")
	}
	return c, nil
}

func envBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
func parseTOML(b []byte, c *Config) {
	for _, line := range strings.Split(string(b), "\n") {
		p := strings.SplitN(strings.TrimSpace(strings.SplitN(line, "#", 2)[0]), "=", 2)
		if len(p) != 2 {
			continue
		}
		k, v := strings.TrimSpace(p[0]), strings.Trim(strings.TrimSpace(p[1]), "\"'")
		switch k {
		case "control_plane", "control_plane_url":
			c.ControlPlaneURL = v
		case "worker_id":
			c.WorkerID = v
		case "name":
			c.Name = v
		case "pool_id":
			c.PoolID = v
		case "token":
			c.Token = v
		case "state_dir":
			c.StateDir = v
		case "max_vcpus":
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				c.MaxVCPUs = n
			}
		case "max_memory_mb":
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				c.MaxMemoryMB = n
			}
		case "max_disk_mb":
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				c.MaxDiskMB = n
			}
		}
	}
}
