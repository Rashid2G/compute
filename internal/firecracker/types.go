package firecracker

// MachineConfig defines vCPU and memory allocation
type MachineConfig struct {
	VCPUCount  int  `json:"vcpu_count"`
	MemSizeMiB int  `json:"mem_size_mib"`
	Smt        bool `json:"smt,omitempty"`
}

// BootSource defines kernel and boot parameters
type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
}

// Drive defines disk storage for Firecracker
type Drive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

// NetworkInterface configures guest NIC connected to a host TAP device
type NetworkInterface struct {
	IfaceID     string `json:"iface_id"`
	GuestMAC    string `json:"guest_mac"`
	HostDevName string `json:"host_dev_name"`
}

// Action defines instance actions like InstanceStart
type Action struct {
	ActionType string `json:"action_type"`
}

// VMState defines pause/resume state
type VMState struct {
	State string `json:"state"` // "Paused" or "Resumed"
}

// SnapshotCreateParams parameters for creating a full snapshot
type SnapshotCreateParams struct {
	SnapshotType string `json:"snapshot_type"` // "Full" or "Diff"
	SnapshotPath string `json:"snapshot_path"`
	MemFilePath  string `json:"mem_file_path"`
}

// SnapshotLoadParams parameters for loading a snapshot
type SnapshotLoadParams struct {
	SnapshotPath        string `json:"snapshot_path"`
	MemFilePath         string `json:"mem_file_path"`
	EnableDiffSnapshots bool   `json:"enable_diff_snapshots,omitempty"`
	ResumeVM            bool   `json:"resume_vm"`
}
