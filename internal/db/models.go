package db

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/Rashid2G/compute/pkg/api"
)

// Network isolation modes control whether a sandbox can reach other non-isolated
// sandboxes in the same user account or is completely isolated on its own.
const (
	NetworkIsolationAccount  = "account"
	NetworkIsolationIsolated = "isolated"
)

// NormalizeNetworkIsolation canonicalizes a sandbox network isolation mode.
// Empty string defaults to NetworkIsolationAccount. Returns ("", false) for
// unrecognized non-empty values.
func NormalizeNetworkIsolation(mode string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", NetworkIsolationAccount, "shared", "all", "same-account", "same_account", "peers":
		return NetworkIsolationAccount, true
	case NetworkIsolationIsolated, "self", "private", "none", "standalone":
		return NetworkIsolationIsolated, true
	default:
		return "", false
	}
}

// IsNetworkIsolated reports whether the VM is configured to be isolated on its own.
func IsNetworkIsolated(vm *VM) bool {
	if vm == nil {
		return false
	}
	mode, ok := NormalizeNetworkIsolation(vm.NetworkIsolation)
	if !ok {
		return false
	}
	return mode == NetworkIsolationIsolated
}

// VMStatus represents the state of a VM
type VMStatus string

const (
	StatusCreated              VMStatus = "created"
	StatusQueued               VMStatus = "queued"
	StatusBooting              VMStatus = "booting"
	StatusRunningStartupScript VMStatus = "running_startup_script"
	StatusRunningBuildScript   VMStatus = "running_build_script"
	StatusRunning              VMStatus = "running"
	StatusPaused               VMStatus = "paused"
	StatusStopped              VMStatus = "stopped"
	StatusFailed               VMStatus = "failed"
)

// IsActiveRunning reports whether the VM is in an active running or startup/build execution state.
func (s VMStatus) IsActiveRunning() bool {
	return s == StatusRunning || s == StatusRunningStartupScript || s == StatusRunningBuildScript
}

// ImageStatus represents the build status of a custom image
type ImageStatus string

const (
	ImageStatusReady    ImageStatus = "ready"
	ImageStatusBuilding ImageStatus = "building"
	ImageStatusFailed   ImageStatus = "failed"
)

// VM represents a managed microVM
type VM struct {
	ID                 string     `json:"id"`                    // External ID (identical to Name, or user-assigned ID)
	InternalID         string     `json:"internal_id,omitempty"` // Distinct internal ID in the global store
	OwnerID            string     `json:"owner_id,omitempty"`    // Owner / user ID for tenant isolation
	Name               string     `json:"name"`                  // VM Name (also serves as the external VM ID)
	Status             VMStatus   `json:"status"`
	DesiredRunning     bool       `json:"desired_running"`
	VCPUs              int        `json:"vcpus"`
	MemoryMB           int        `json:"memory_mb"`
	DiskMB             int        `json:"disk_mb"`
	IPAddress          string     `json:"ip_address"`
	PublicIP           string     `json:"public_ip"`
	PublicIPEnabled    bool       `json:"public_ip_enabled"`
	NetworkIsolation   string     `json:"network_isolation"`
	MACAddress         string     `json:"mac_address"`
	TAPDevice          string     `json:"tap_device"`
	TailscaleEnabled   bool       `json:"tailscale_enabled"`
	TailnetIP          string     `json:"tailnet_ip"`
	TailnetDNS         string     `json:"tailnet_dns,omitempty"`
	TailscaleDeviceID  string     `json:"tailscale_device_id,omitempty"`
	RootfsPath         string     `json:"rootfs_path"`
	KernelPath         string     `json:"kernel_path"`
	DiskPath           string     `json:"disk_path"`
	SnapshotPath       string     `json:"snapshot_path,omitempty"`
	MemFilePath        string     `json:"mem_file_path,omitempty"`
	SSHKeyPath         string     `json:"ssh_key_path,omitempty"`
	SSHKeyName         string     `json:"ssh_key_name,omitempty"`
	SSHPublicKey       string     `json:"ssh_public_key,omitempty"`
	SSHPasswordEnabled bool       `json:"ssh_password_enabled"`
	PID                int        `json:"pid,omitempty"`
	SocketPath         string     `json:"socket_path,omitempty"`
	Protected          bool       `json:"protected"`
	Description        string     `json:"description,omitempty"`
	StartupScript      string     `json:"startup_script,omitempty"`
	PoolID             string     `json:"pool_id,omitempty"`
	WorkerID           string     `json:"worker_id,omitempty"`
	LastActivityAt     *time.Time `json:"last_activity_at,omitempty"`
	AutoHibernated     bool       `json:"auto_hibernated,omitempty"`
	HibernateReason    string     `json:"hibernate_reason,omitempty"`
	// Hostname is transient and response-only: the DNS-safe fully qualified
	// sandbox name "<sandbox>.<username>.<base-domain>" populated by the API
	// layer when DNS naming is configured. It is never persisted.
	Hostname  string    `json:"hostname,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
	VMID             string    `json:"vm_id"`
	VMName           string    `json:"vm_name"`
	OwnerID          string    `json:"-"`
	Status           VMStatus  `json:"status"`
	IPAddress        string    `json:"ip_address"`
	PublicIP         string    `json:"public_ip,omitempty"`
	PublicIPEnabled  bool      `json:"public_ip_enabled"`
	NetworkIsolation string    `json:"network_isolation"`
	MACAddress       string    `json:"mac_address"`
	TAPDevice        string    `json:"tap_device"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// SSHKey represents a stored user public SSH key
type SSHKey struct {
	ID          string `json:"id"`
	OwnerID     string `json:"owner_id"`
	Name        string `json:"name"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	// AllSandboxes marks the key as account-wide: it is embedded into every
	// sandbox owned by the key's owner, including existing and future ones.
	AllSandboxes bool `json:"all_sandboxes"`
	// IsDefault marks the key that is embedded automatically into a new
	// sandbox when its creation request does not choose a key. The creator
	// can override the choice per sandbox. At most one key per owner carries
	// the flag.
	IsDefault bool      `json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
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
	Name       string     `json:"name"`
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
	AllocatedVCPUs   int        `json:"allocated_vcpus"`
	AllocatedMemMB   int        `json:"allocated_mem_mb"`
	AllocatedDiskMB  int        `json:"allocated_disk_mb"`
	PhysicalVCPUs    int        `json:"physical_vcpus"`
	PhysicalMemMB    int        `json:"physical_mem_mb"`
	PhysicalDiskMB   int        `json:"physical_disk_mb"`
	UsedVCPUs        int        `json:"used_vcpus"`
	UsedMemMB        int        `json:"used_mem_mb"`
	UsedDiskMB       int        `json:"used_disk_mb"`
	RunningVMs       int        `json:"running_vms"`
	AvailableMemMB   int        `json:"available_mem_mb"`
	AvailableDiskMB  int        `json:"available_disk_mb"`
	LabelsJSON       string     `json:"labels_json"`
	CapabilitiesJSON string     `json:"capabilities_json"`
	AgentVersion     string     `json:"agent_version"`
	FCVersion        string     `json:"fc_version"`
	LastHeartbeat    *time.Time `json:"last_heartbeat,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// ClusterSchedulingPolicy governs cluster-wide CPU, RAM, and disk scheduling limits.
type ClusterSchedulingPolicy = api.ClusterSchedulingPolicy

// DefaultSchedulingPolicy returns the safe default cluster scheduling policy.
func DefaultSchedulingPolicy() ClusterSchedulingPolicy {
	return api.DefaultSchedulingPolicy()
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
	RepoID     string     `json:"repo_id,omitempty"`
	RepoURL    string     `json:"repo_url,omitempty"`
	RepoName   string     `json:"repo_name,omitempty"`
	Branch     string     `json:"branch,omitempty"`
	CommitSHA  string     `json:"commit_sha,omitempty"`
	Source     string     `json:"source,omitempty"`
	VMID       string     `json:"vm_id,omitempty"`
	WorkerID   string     `json:"worker_id,omitempty"`
	QueuedAt   time.Time  `json:"queued_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	DeadlineAt *time.Time `json:"deadline_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// GitHubConnection represents a connected GitHub account/repository persisted
// in SQLite for launching ephemeral CI/CD jobs.
type GitHubConnection struct {
	ID                  string     `json:"id"`
	OwnerID             string     `json:"owner_id"`
	Name                string     `json:"name"`
	RepoURL             string     `json:"repo_url"`
	DefaultBranch       string     `json:"default_branch"`
	GitHubToken         string     `json:"-"`
	TokenMasked         string     `json:"token_masked,omitempty"`
	HasToken            bool       `json:"has_token"`
	WebhookSecret       string     `json:"-"`
	WebhookSecretMasked string     `json:"webhook_secret_masked,omitempty"`
	HasWebhookSecret    bool       `json:"has_webhook_secret"`
	DefaultCommand      string     `json:"default_command"`
	VCPUs               int        `json:"vcpus"`
	MemoryMB            int        `json:"memory_mb"`
	TimeoutSec          int        `json:"timeout_sec"`
	Status              string     `json:"status"`
	LastTriggeredAt     *time.Time `json:"last_triggered_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
}

func maskSecretString(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	if len(s) <= 8 {
		return "****", true
	}
	return s[:4] + "..." + s[len(s)-4:], true
}

// PopulateMask populates HasToken/TokenMasked and HasWebhookSecret/WebhookSecretMasked
// without exposing raw secrets in JSON responses.
func (c *GitHubConnection) PopulateMask() {
	if c == nil {
		return
	}
	c.TokenMasked, c.HasToken = maskSecretString(c.GitHubToken)
	c.WebhookSecretMasked, c.HasWebhookSecret = maskSecretString(c.WebhookSecret)
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

// Volume represents a persistent block storage volume
type Volume struct {
	ID         string    `json:"id"`
	OwnerID    string    `json:"owner_id,omitempty"` // Owner / user ID for tenant isolation
	Name       string    `json:"name"`
	Pool       string    `json:"pool"`
	SizeGB     int       `json:"size_gb"`
	UsedGB     float64   `json:"used_gb"`
	Status     string    `json:"status"` // "attached" | "available" | "snapshotting"
	AttachedVM string    `json:"attached_vm"`
	DevicePath string    `json:"device_path"`
	MountPoint string    `json:"mount_point"`
	Filesystem string    `json:"filesystem"`
	Path       string    `json:"path,omitempty"`
	IOPS       int       `json:"iops"`
	Throughput string    `json:"throughput"`
	Snapshots  int       `json:"snapshots"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// WebhookEndpoint represents a destination for outbound event notifications
type WebhookEndpoint struct {
	ID              string    `json:"id"`
	URL             string    `json:"url"`
	Description     string    `json:"description"`
	Secret          string    `json:"secret"`
	Events          []string  `json:"events"` // stored as JSON in SQLite
	Active          bool      `json:"active"`
	LastDelivery    string    `json:"last_delivery"`
	SuccessRate     string    `json:"success_rate"`
	DeliveriesCount int       `json:"deliveries_count"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// OTelConfig represents OpenTelemetry exporter configuration
type OTelConfig struct {
	Enabled        bool   `json:"enabled"`
	Endpoint       string `json:"endpoint"`
	Protocol       string `json:"protocol"`
	AuthHeader     string `json:"auth_header"`
	ServiceName    string `json:"service_name"`
	SampleRate     string `json:"sample_rate"`
	ExportInterval string `json:"export_interval"`
	MetricsEnabled bool   `json:"metrics_enabled"`
	TracesEnabled  bool   `json:"traces_enabled"`
	LogsEnabled    bool   `json:"logs_enabled"`
	SpansExported  int64  `json:"spans_exported"`
	LastExportAt   string `json:"last_export_at"`
}

// AuditEvent represents an auditable control-plane event
type AuditEvent struct {
	ID         string    `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	Event      string    `json:"event"`
	Actor      string    `json:"actor"`
	Target     string    `json:"target"`
	Status     string    `json:"status"` // "delivered" | "recorded" | "failed" | "retrying"
	DurationMs int       `json:"duration_ms"`
	Payload    string    `json:"payload"`
}

// AccessAPIKey represents a persistent scoped API access token
type AccessAPIKey struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Role      string    `json:"role,omitempty"`
	Prefix    string    `json:"prefix"`
	Token     string    `json:"token,omitempty"`
	Scope     string    `json:"scope"`
	LastUsed  string    `json:"last_used"`
	ExpiresAt string    `json:"expires_at"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// EnvironmentProfile represents reusable environment configurations
type EnvironmentProfile struct {
	OwnerID             string    `json:"owner_id"`
	Name                string    `json:"name"`
	IsDefault           bool      `json:"is_default"`
	SafeForThirdParty   bool      `json:"safe_for_third_parties"`
	GitHubReposEnabled  bool      `json:"github_repos_enabled"`
	GitHubRepos         []string  `json:"github_repos"`
	SecretsEnabled      bool      `json:"secrets_enabled"`
	Secrets             string    `json:"secrets"`
	ComputeCredsEnabled bool      `json:"compute_credentials_enabled"`
	BoatCredsEnabled    bool      `json:"boat_credentials_enabled,omitempty"`
	AgentCredsEnabled   bool      `json:"agent_credentials_enabled"`
	CredentialProfile   string    `json:"credential_profile"`
	Version             int       `json:"version"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// UnmarshalJSON accepts both compute_credentials_enabled and legacy boat_credentials_enabled.
func (e *EnvironmentProfile) UnmarshalJSON(data []byte) error {
	type Alias EnvironmentProfile
	aux := struct {
		*Alias
		HasComputeCreds *bool `json:"compute_credentials_enabled"`
		HasBoatCreds    *bool `json:"boat_credentials_enabled"`
	}{
		Alias: (*Alias)(e),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.HasComputeCreds != nil {
		e.ComputeCredsEnabled = *aux.HasComputeCreds
		e.BoatCredsEnabled = *aux.HasComputeCreds
	} else if aux.HasBoatCreds != nil {
		e.ComputeCredsEnabled = *aux.HasBoatCreds
		e.BoatCredsEnabled = *aux.HasBoatCreds
	}
	return nil
}

// PortForward represents a port forwarding mapping
type PortForward struct {
	ID        string    `json:"id"`
	OwnerID   string    `json:"owner_id,omitempty"` // Owner / user ID for tenant isolation
	HostPort  int       `json:"host_port"`
	VMID      string    `json:"vm_id"`
	VMName    string    `json:"vm_name"`
	GuestPort int       `json:"guest_port"`
	Protocol  string    `json:"protocol"`
	Subdomain string    `json:"subdomain"`
	PublicURL string    `json:"public_url"`
	Status    string    `json:"status"`
	Public    bool      `json:"public"`
	CreatedAt time.Time `json:"created_at"`
}
