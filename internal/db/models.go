package db

import (
	"time"
)

// VMStatus represents the state of a VM
type VMStatus string

const (
	StatusCreated VMStatus = "created"
	StatusQueued  VMStatus = "queued"
	StatusRunning VMStatus = "running"
	StatusPaused  VMStatus = "paused"
	StatusStopped VMStatus = "stopped"
	StatusFailed  VMStatus = "failed"
)

// ImageStatus represents the build status of a custom image
type ImageStatus string

const (
	ImageStatusReady    ImageStatus = "ready"
	ImageStatusBuilding ImageStatus = "building"
	ImageStatusFailed   ImageStatus = "failed"
)

// VM represents a managed microVM
type VM struct {
	ID              string    `json:"id"`                    // External ID (identical to Name, or user-assigned ID)
	InternalID      string    `json:"internal_id,omitempty"` // Distinct internal ID in the global store
	OwnerID         string    `json:"owner_id,omitempty"`    // Owner / user ID for tenant isolation
	Name            string    `json:"name"`                  // VM Name (also serves as the external VM ID)
	Status          VMStatus  `json:"status"`
	VCPUs           int       `json:"vcpus"`
	MemoryMB        int       `json:"memory_mb"`
	IPAddress       string    `json:"ip_address"`
	PublicIP        string    `json:"public_ip"`
	PublicIPEnabled bool      `json:"public_ip_enabled"`
	MACAddress      string    `json:"mac_address"`
	TAPDevice       string    `json:"tap_device"`
	RootfsPath      string    `json:"rootfs_path"`
	KernelPath      string    `json:"kernel_path"`
	DiskPath        string    `json:"disk_path"`
	SnapshotPath    string    `json:"snapshot_path,omitempty"`
	MemFilePath     string    `json:"mem_file_path,omitempty"`
	SSHKeyPath      string    `json:"ssh_key_path,omitempty"`
	SSHKeyName      string    `json:"ssh_key_name,omitempty"`
	SSHPublicKey    string    `json:"ssh_public_key,omitempty"`
	PID             int       `json:"pid,omitempty"`
	SocketPath      string    `json:"socket_path,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// CustomImage represents a user-created OS image with pre-installed packages
type CustomImage struct {
	ID           string      `json:"id"`                    // External ID / Name
	InternalID   string      `json:"internal_id,omitempty"` // Distinct internal ID in global store
	OwnerID      string      `json:"owner_id,omitempty"`    // Owner / user ID
	Name         string      `json:"name"`
	Description  string      `json:"description,omitempty"`
	BaseImage    string      `json:"base_image"`
	Packages     []string    `json:"packages"`
	RootfsPath   string      `json:"rootfs_path"`
	KernelPath   string      `json:"kernel_path"`
	SizeMB       int         `json:"size_mb"`
	Status       ImageStatus `json:"status"`
	ErrorMessage string      `json:"error_message,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

// Template represents a reusable VM disk template created from a configured VM
type Template struct {
	ID          string    `json:"id"`                    // External ID / Name
	InternalID  string    `json:"internal_id,omitempty"` // Distinct internal ID in global store
	OwnerID     string    `json:"owner_id,omitempty"`    // Owner / user ID
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	SourceVMID  string    `json:"source_vm_id,omitempty"`
	DiskPath    string    `json:"disk_path"`
	KernelPath  string    `json:"kernel_path"`
	VCPUs       int       `json:"vcpus"`
	MemoryMB    int       `json:"memory_mb"`
	DiskMB      int       `json:"disk_mb"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// NetworkAllocation represents an assigned network lease
type NetworkAllocation struct {
	VMID            string    `json:"vm_id"`
	VMName          string    `json:"vm_name"`
	Status          VMStatus  `json:"status"`
	IPAddress       string    `json:"ip_address"`
	PublicIP        string    `json:"public_ip,omitempty"`
	PublicIPEnabled bool      `json:"public_ip_enabled"`
	MACAddress      string    `json:"mac_address"`
	TAPDevice       string    `json:"tap_device"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// SSHKey represents a stored user public SSH key
type SSHKey struct {
	ID          string    `json:"id"`
	OwnerID     string    `json:"owner_id"`
	Name        string    `json:"name"`
	PublicKey   string    `json:"public_key"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
}

// WorkerPool groups remote workers with shared placement policy.
type WorkerPool struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	LabelsJSON string    `json:"labels_json"`
	PolicyJSON string    `json:"policy_json"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
type WorkerToken struct {
	Token      string     `json:"token"`
	PoolID     string     `json:"pool_id"`
	LabelsJSON string     `json:"labels_json"`
	ExpiresAt  time.Time  `json:"expires_at"`
	UsedAt     *time.Time `json:"used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}
type WorkerNode struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	PoolID           string     `json:"pool_id"`
	Role             string     `json:"role"`
	Status           string     `json:"status"`
	UnderlayIP       string     `json:"underlay_ip"`
	Arch             string     `json:"arch"`
	TotalVCPUs       int        `json:"total_vcpus"`
	TotalMemMB       int        `json:"total_mem_mb"`
	TotalDiskMB      int        `json:"total_disk_mb"`
	LabelsJSON       string     `json:"labels_json"`
	CapabilitiesJSON string     `json:"capabilities_json"`
	AgentVersion     string     `json:"agent_version"`
	FCVersion        string     `json:"fc_version"`
	LastHeartbeat    *time.Time `json:"last_heartbeat,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}
type Job struct {
	ID         string     `json:"id"`
	OwnerID    string     `json:"owner_id"`
	PoolID     string     `json:"pool_id"`
	Type       string     `json:"type"`
	State      string     `json:"state"`
	Priority   int        `json:"priority"`
	SpecJSON   string     `json:"spec_json"`
	ResultJSON string     `json:"result_json"`
	QueuedAt   time.Time  `json:"queued_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	DeadlineAt *time.Time `json:"deadline_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}
type JobAttempt struct {
	ID        string    `json:"id"`
	JobID     string    `json:"job_id"`
	WorkerID  string    `json:"worker_id"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}
type WorkerLease struct {
	ID        string    `json:"id"`
	JobID     string    `json:"job_id"`
	AttemptID string    `json:"attempt_id"`
	WorkerID  string    `json:"worker_id"`
	ExpiresAt time.Time `json:"expires_at"`
	State     string    `json:"state"`
}
type JobLog struct {
	ID        string    `json:"id"`
	JobID     string    `json:"job_id"`
	AttemptID string    `json:"attempt_id"`
	Stream    string    `json:"stream"`
	Seq       int64     `json:"seq"`
	Data      string    `json:"data"`
	CreatedAt time.Time `json:"created_at"`
}
