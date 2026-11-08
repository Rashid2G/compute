package db

import (
	"database/sql"
	"time"
)

// User represents a platform tenant or administrator.
type User struct {
	ID           string         `json:"id"`
	Email        string         `json:"email"`
	Username     string         `json:"username"`
	DisplayName  string         `json:"display_name"`
	PasswordHash string         `json:"-"`
	Role         string         `json:"role"`
	Status       string         `json:"status"`
	MustChangePW bool           `json:"must_change_pw"`
	FailedLogins int            `json:"failed_logins"`
	LockedUntil  *int64         `json:"locked_until,omitempty"`
	QuotaJSON    sql.NullString `json:"quota_json,omitempty"`
	CreatedAt    int64          `json:"created_at"`
	UpdatedAt    int64          `json:"updated_at"`
	Revision     int            `json:"revision"`
}

// Session represents an authenticated browser session.
type Session struct {
	ID         string `json:"id"`
	UserID     string `json:"user_id"`
	TokenHash  []byte `json:"-"`
	CSRFHash   []byte `json:"-"`
	IP         string `json:"ip"`
	UserAgent  string `json:"user_agent"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	ExpiresAt  int64  `json:"expires_at"`
}

// APIKey represents a machine token for CLI or AI agents.
type APIKey struct {
	ID         string  `json:"id"`
	UserID     string  `json:"user_id"`
	Name       string  `json:"name"`
	SecretHash []byte  `json:"-"`
	Prefix     string  `json:"prefix"`
	Scopes     string  `json:"scopes"`
	ExpiresAt  *int64  `json:"expires_at,omitempty"`
	LastUsedAt *int64  `json:"last_used_at,omitempty"`
	LastUsedIP *string `json:"last_used_ip,omitempty"`
	CreatedAt  int64   `json:"created_at"`
	RevokedAt  *int64  `json:"revoked_at,omitempty"`
}

// Instance represents a Firecracker microVM instance.
type Instance struct {
	ID             string  `json:"id"`
	OwnerID        string  `json:"owner_id"`
	Name           string  `json:"name"`
	Kind           string  `json:"kind"` // 'vm' or 'container'
	ImageID        string  `json:"image_id"`
	TemplateID     *string `json:"template_id,omitempty"`
	VCPUs          int     `json:"vcpus"`
	MemoryMB       int     `json:"memory_mb"`
	NodeID         *string `json:"node_id,omitempty"`
	VPCID          string  `json:"vpc_id"`
	DesiredState   string  `json:"desired_state"`
	State          string  `json:"state"`
	StateReason    string  `json:"state_reason"`
	IdlePolicyJSON string  `json:"idle_policy_json"`
	PortsJSON      string  `json:"ports_json"`
	EnvJSON        string  `json:"env_json"`
	CmdJSON        *string `json:"cmd_json,omitempty"`
	RestartPolicy  string  `json:"restart_policy"`
	LastActiveAt   *int64  `json:"last_active_at,omitempty"`
	StartedAt      *int64  `json:"started_at,omitempty"`
	CreatedAt      int64   `json:"created_at"`
	UpdatedAt      int64   `json:"updated_at"`
	Revision       int     `json:"revision"`
}

// Volume represents a disk volume.
type Volume struct {
	ID             string  `json:"id"`
	OwnerID        string  `json:"owner_id"`
	Name           string  `json:"name"`
	NodeID         *string `json:"node_id,omitempty"`
	SizeMB         int     `json:"size_mb"`
	Kind           string  `json:"kind"` // 'root' or 'data'
	SourceImageID  *string `json:"source_image_id,omitempty"`
	SourceVolumeID *string `json:"source_volume_id,omitempty"`
	InstanceID     *string `json:"instance_id,omitempty"`
	DeviceIndex    *int    `json:"device_index,omitempty"`
	ReadOnly       bool    `json:"read_only"`
	Status         string  `json:"status"`
	UsedMB         int     `json:"used_mb"`
	CreatedAt      int64   `json:"created_at"`
}

// VPC represents an isolated virtual private cloud overlay.
type VPC struct {
	ID        string `json:"id"`
	OwnerID   string `json:"owner_id"`
	Name      string `json:"name"`
	CIDR      string `json:"cidr"`
	VNI       int    `json:"vni"`
	IsDefault bool   `json:"is_default"`
	Egress    string `json:"egress"` // 'nat' or 'none'
	CreatedAt int64  `json:"created_at"`
	Revision  int    `json:"revision"`
}

// Size represents an instance resource tier.
type Size struct {
	ID        string `json:"id"`
	VCPUs     int    `json:"vcpus"`
	MemoryMB  int    `json:"memory_mb"`
	DiskMBps  int    `json:"disk_mbps"`
	DiskIOPS  int    `json:"disk_iops"`
	NetMBps   int    `json:"net_mbps"`
	SortOrder int    `json:"sort_order"`
	Enabled   bool   `json:"enabled"`
}

// Operation represents an asynchronous long-running task.
type Operation struct {
	ID             string  `json:"id"`
	OwnerID        string  `json:"owner_id"`
	Kind           string  `json:"kind"`
	TargetID       string  `json:"target_id"`
	Status         string  `json:"status"` // pending, running, succeeded, failed, canceled
	Progress       int     `json:"progress"`
	ErrorCode      *string `json:"error_code,omitempty"`
	ErrorMsg       *string `json:"error_msg,omitempty"`
	TraceID        string  `json:"trace_id"`
	IdempotencyKey *string `json:"idempotency_key,omitempty"`
	CreatedAt      int64   `json:"created_at"`
	UpdatedAt      int64   `json:"updated_at"`
}

// Image represents a bootable OS image or cached OCI container image.
type Image struct {
	ID            string  `json:"id"`
	OwnerID       *string `json:"owner_id,omitempty"`
	Kind          string  `json:"kind"` // vm or oci
	Name          string  `json:"name"`
	OSFamily      *string `json:"os_family,omitempty"`
	Arch          string  `json:"arch"`
	KernelID      *string `json:"kernel_id,omitempty"`
	RootfsSHA256  *string `json:"rootfs_sha256,omitempty"`
	OCIDigest     *string `json:"oci_digest,omitempty"`
	OCIConfigJSON *string `json:"oci_config_json,omitempty"`
	SizeBytes     int64   `json:"size_bytes"`
	MinDiskMB     int     `json:"min_disk_mb"`
	Status        string  `json:"status"`
	Description   string  `json:"description"`
	CreatedAt     int64   `json:"created_at"`
}

// Template represents a defined image specification for VMs.
type Template struct {
	ID          string  `json:"id"`
	OwnerID     *string `json:"owner_id,omitempty"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	SpecJSON    string  `json:"spec_json"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
}

// Organization represents a multi-user tenant workspace.
type Organization struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Slug      string  `json:"slug"`
	Status    string  `json:"status"`
	QuotaJSON *string `json:"quota_json,omitempty"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

// OrganizationMember represents a user's membership and role in an organization.
type OrganizationMember struct {
	OrganizationID string `json:"organization_id"`
	UserID         string `json:"user_id"`
	Role           string `json:"role"` // 'owner', 'admin', 'member', 'viewer'
	CreatedAt      int64  `json:"created_at"`
}

func NowMillis() int64 {
	return time.Now().UTC().UnixMilli()
}
