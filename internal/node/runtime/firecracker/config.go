package firecracker

import (
	"encoding/json"
)

// VMConfig defines the single-shot Firecracker JSON configuration file schema.
type VMConfig struct {
	BootSource        BootSource        `json:"boot-source"`
	Drives            []Drive           `json:"drives"`
	MachineConfig     MachineConfig     `json:"machine-config"`
	NetworkInterfaces []NetworkInterface `json:"network-interfaces,omitempty"`
	Vsock             *VsockConfig      `json:"vsock,omitempty"`
	Balloon           *BalloonConfig    `json:"balloon,omitempty"`
}

type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
}

type Drive struct {
	DriveID      string       `json:"drive_id"`
	PathOnHost   string       `json:"path_on_host"`
	IsRootDevice bool         `json:"is_root_device"`
	IsReadOnly   bool         `json:"is_read_only"`
	RateLimiter  *RateLimiter `json:"rate_limiter,omitempty"`
}

type RateLimiter struct {
	Bandwidth *TokenBucket `json:"bandwidth,omitempty"`
	Ops       *TokenBucket `json:"ops,omitempty"`
}

type TokenBucket struct {
	Size       int64 `json:"size"`
	OneTimeBurst int64 `json:"one_time_burst,omitempty"`
	RefillTime int64 `json:"refill_time"` // milliseconds
}

type MachineConfig struct {
	VcpuCount  int  `json:"vcpu_count"`
	MemSizeMib int  `json:"mem_size_mib"`
	Smt        bool `json:"smt"`
}

type NetworkInterface struct {
	IfaceID     string `json:"iface_id"`
	GuestMac    string `json:"guest_mac"`
	HostDevName string `json:"host_dev_name"`
}

type VsockConfig struct {
	GuestCID uint32 `json:"guest_cid"`
	UdsPath  string `json:"uds_path"`
}

type BalloonConfig struct {
	AmountMib    int  `json:"amount_mib"`
	DeflateOnOOM bool `json:"deflate_on_oom"`
}

// BuildVMConfig constructs a complete, valid Firecracker configuration.
func BuildVMConfig(kernelPath, rootfsPath, bootArgs string, vcpus, memMB int, tapName, mac string, udsVsockPath string) (*VMConfig, error) {
	if bootArgs == "" {
		bootArgs = "console=ttyS0 reboot=k panic=1 pci=off nomodule 8250.nr_uarts=1 quiet root=/dev/vda rw"
	}

	cfg := &VMConfig{
		BootSource: BootSource{
			KernelImagePath: kernelPath,
			BootArgs:        bootArgs,
		},
		Drives: []Drive{
			{
				DriveID:      "rootfs",
				PathOnHost:   rootfsPath,
				IsRootDevice: true,
				IsReadOnly:   false,
			},
		},
		MachineConfig: MachineConfig{
			VcpuCount:  vcpus,
			MemSizeMib: memMB,
			Smt:        false,
		},
		Balloon: &BalloonConfig{
			AmountMib:    0,
			DeflateOnOOM: true,
		},
	}

	if tapName != "" && mac != "" {
		cfg.NetworkInterfaces = []NetworkInterface{
			{
				IfaceID:     "eth0",
				GuestMac:    mac,
				HostDevName: tapName,
			},
		}
	}

	if udsVsockPath != "" {
		cfg.Vsock = &VsockConfig{
			GuestCID: 3,
			UdsPath:  udsVsockPath,
		}
	}

	return cfg, nil
}

// ToJSON serializes the configuration to indented JSON.
func (c *VMConfig) ToJSON() ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}
