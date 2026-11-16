package api

import "encoding/json"

// Message Type constants ('t' field in JSON frames)
const (
	TypeReq    = "req"
	TypeRes    = "res"
	TypeEvt    = "evt"
	TypeStream = "stream"
	TypeHello  = "hello"
	TypeBye    = "bye"
)

// Subprotocol constant
const Subprotocol = "compute.v1"

// Req represents a client-to-server RPC request.
type Req struct {
	Type   string          `json:"t"` // "req"
	ID     string          `json:"id"`
	Op     string          `json:"op"`
	Params json.RawMessage `json:"params,omitempty"`
	Trace  string          `json:"trace,omitempty"`
	Idem   string          `json:"idem,omitempty"`
}

// Res represents a server-to-client RPC response.
type Res struct {
	Type    string          `json:"t"` // "res"
	ID      string          `json:"id"`
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
	TraceID string          `json:"trace_id,omitempty"`
}

// Evt represents a server-to-client subscription event.
type Evt struct {
	Type  string      `json:"t"` // "evt"
	Sub   string      `json:"sub"`
	Topic string      `json:"topic"`
	Seq   int64       `json:"seq"`
	Data  interface{} `json:"data"`
}

// StreamMsg represents a stream control message (open, close, resize, credit, exit).
type StreamMsg struct {
	Type   string      `json:"t"` // "stream"
	SID    uint32      `json:"sid"`
	Action string      `json:"action"` // open, close, resize, credit, exit
	Data   interface{} `json:"data,omitempty"`
}

// HelloMsg is the first frame sent by server upon WebSocket connect.
type HelloMsg struct {
	Type          string       `json:"t"` // "hello"
	ServerVersion string       `json:"server_version"`
	Session       SessionInfo  `json:"session"`
	Limits        ServerLimits `json:"limits"`
	HeartbeatMS   int          `json:"heartbeat_ms"`
}

type SessionInfo struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Auth     string `json:"auth"` // "cookie" or "api_key"
	KeyID    string `json:"key_id,omitempty"`
}

type ServerLimits struct {
	MaxMessageBytes int `json:"max_message_bytes"`
	MaxInflight     int `json:"max_inflight"`
}

// ByeMsg is sent by the server before closing an invalid/revoked connection.
type ByeMsg struct {
	Type   string `json:"t"` // "bye"
	Code   int    `json:"code"`
	Reason string `json:"reason"`
}

// ===== Domain Payloads =====

type Port struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"` // "http" or "tcp"
	Public   bool   `json:"public"`
	Auth     string `json:"auth,omitempty"` // "none" or "api_key"
}

type VolumeMount struct {
	Volume   string `json:"volume"`
	SizeMB   int    `json:"size_mb"`
	Mount    string `json:"mount"`
	ReadOnly bool   `json:"read_only"`
}

type IdlePolicy struct {
	Mode         string `json:"mode"`                    // "none", "suspend", "stop"
	AfterSeconds int    `json:"after_seconds,omitempty"` // default 300
	WakeOn       string `json:"wake_on,omitempty"`       // "http" or "none"
	CPUThreshold int    `json:"cpu_threshold_percent,omitempty"`
}

type InstanceCreate struct {
	Name               string            `json:"name"`
	Kind               string            `json:"kind"` // "vm" or "container"
	Image              string            `json:"image"`
	Template           string            `json:"template,omitempty"`
	Size               string            `json:"size,omitempty"`
	VPC                string            `json:"vpc,omitempty"`
	IP                 string            `json:"ip,omitempty"`
	DiskMB             int               `json:"disk_mb,omitempty"`
	Volumes            []VolumeMount     `json:"volumes,omitempty"`
	Ports              []Port            `json:"ports,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	Secrets            map[string]string `json:"secrets,omitempty"`
	Cmd                []string          `json:"cmd,omitempty"`
	Entrypoint         []string          `json:"entrypoint,omitempty"`
	User               string            `json:"user,omitempty"`
	Workdir            string            `json:"workdir,omitempty"`
	Idle               IdlePolicy        `json:"idle,omitempty"`
	Restart            string            `json:"restart,omitempty"`
	Start              *bool             `json:"start,omitempty"`
	RegistryCredential string            `json:"registry_credential,omitempty"`
	PoolID             string            `json:"pool_id,omitempty"`
	WorkerID           string            `json:"worker_id,omitempty"`
}

type APIKeyCreateReq struct {
	Name      string `json:"name"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
}

type APIKeyCreateResp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"` // mfk_<key_id>_<secret> (Returned once!)
	Prefix    string `json:"prefix"`
	CreatedAt int64  `json:"created_at"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	User     UserInfo `json:"user"`
	CSRF     string   `json:"csrf_token"`
	MustWait int      `json:"must_wait_seconds,omitempty"`
}

type UserInfo struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// Job REST payloads.
type JobCreateRequest struct {
	Type        string                 `json:"type"`
	PoolID      string                 `json:"pool_id,omitempty"`
	Priority    int                    `json:"priority,omitempty"`
	Source      string                 `json:"source,omitempty"`
	RepoID      string                 `json:"repo_id,omitempty"`
	RepoURL     string                 `json:"repo_url,omitempty"`
	RepoName    string                 `json:"repo_name,omitempty"`
	Branch      string                 `json:"branch,omitempty"`
	CommitSHA   string                 `json:"commit_sha,omitempty"`
	Spec        JobSpec                `json:"spec"`
	RetryPolicy map[string]interface{} `json:"retry_policy,omitempty"`
	DeadlineAt  *int64                 `json:"deadline_at,omitempty"`
}
type JobResponse struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	OwnerID        string          `json:"owner_id"`
	PoolID         string          `json:"pool_id,omitempty"`
	Type           string          `json:"type"`
	State          string          `json:"state"`
	Priority       int             `json:"priority"`
	Spec           json.RawMessage `json:"spec"`
	Result         json.RawMessage `json:"result,omitempty"`
	QueuedAt       *int64          `json:"queued_at,omitempty"`
	StartedAt      *int64          `json:"started_at,omitempty"`
	FinishedAt     *int64          `json:"finished_at,omitempty"`
	CreatedAt      int64           `json:"created_at"`
	UpdatedAt      int64           `json:"updated_at"`
}
type JobListResponse struct {
	Jobs       []JobResponse `json:"jobs"`
	NextCursor string        `json:"next_cursor,omitempty"`
}
type JobLogsRequest struct {
	AfterSequence int64  `json:"after_sequence,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Stream        string `json:"stream,omitempty"`
}
type JobArtifactsResponse struct {
	Artifacts []ArtifactResponse `json:"artifacts"`
}
type ArtifactResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
}
type WorkerTokenCreateRequest struct {
	Name      string `json:"name"`
	PoolID    string `json:"pool_id,omitempty"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
}
type WorkerTokenResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"`
	Prefix    string `json:"prefix"`
	PoolID    string `json:"pool_id,omitempty"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
	CreatedAt int64  `json:"created_at"`
}
type PoolCreateRequest struct {
	Name   string                 `json:"name"`
	Labels map[string]string      `json:"labels,omitempty"`
	Policy map[string]interface{} `json:"policy,omitempty"`
}
type PoolResponse struct {
	ID             string                 `json:"id"`
	OrganizationID string                 `json:"organization_id"`
	Name           string                 `json:"name"`
	Labels         map[string]string      `json:"labels"`
	Policy         map[string]interface{} `json:"policy"`
	CreatedAt      int64                  `json:"created_at"`
	UpdatedAt      int64                  `json:"updated_at"`
}

type AuthStatusResponse struct {
	AuthEnabled   bool   `json:"auth_enabled"`
	SignupEnabled bool   `json:"signup_enabled"`
	AdminUsername string `json:"admin_username,omitempty"`
}

// ClusterSchedulingPolicy governs cluster-wide CPU, RAM, and disk scheduling limits.
type ClusterSchedulingPolicy struct {
	Strategy               string  `json:"strategy"` // "least_loaded", "round_robin", "bin_pack", "spread"
	CPUWeight              float64 `json:"cpu_weight"`
	MemoryWeight           float64 `json:"memory_weight"`
	DiskWeight             float64 `json:"disk_weight"`
	CPUSoftOvercommitRatio float64 `json:"cpu_soft_overcommit_ratio"`
	CPUHardOvercommitRatio float64 `json:"cpu_hard_overcommit_ratio"`
	CPUShareWeightBase     int     `json:"cpu_share_weight_base"`
	MemoryHardGuarantee    bool    `json:"memory_hard_guarantee"`
	IdleRAMSnapshotEnabled bool    `json:"idle_ram_snapshot_enabled"`
	IdleThresholdSeconds   int     `json:"idle_threshold_seconds"`
	AutoReclaimOnPressure  bool    `json:"auto_reclaim_on_pressure"`
	HostReservedVCPUs      int     `json:"host_reserved_vcpus"`
	HostReservedMemoryMB   int     `json:"host_reserved_memory_mb"`
	HostReservedDiskMB     int     `json:"host_reserved_disk_mb"`
	DiskHardEnforcement    bool    `json:"disk_hard_enforcement"`
}

// DefaultSchedulingPolicy returns the safe default cluster scheduling policy.
func DefaultSchedulingPolicy() ClusterSchedulingPolicy {
	return ClusterSchedulingPolicy{
		Strategy:               "least_loaded",
		CPUWeight:              0.35,
		MemoryWeight:           0.40,
		DiskWeight:             0.25,
		CPUSoftOvercommitRatio: 1.5,
		CPUHardOvercommitRatio: 2.0,
		CPUShareWeightBase:     100,
		MemoryHardGuarantee:    true,
		IdleRAMSnapshotEnabled: true,
		IdleThresholdSeconds:   900,
		AutoReclaimOnPressure:  true,
		HostReservedVCPUs:      1,
		HostReservedMemoryMB:   512,
		HostReservedDiskMB:     2048,
		DiskHardEnforcement:    true,
	}
}

// UpdateWorkerRequest updates mutable worker node pool, allocation limits, status, and labels.
type UpdateWorkerRequest struct {
	PoolID          *string            `json:"pool_id,omitempty"`
	AllocatedVCPUs  *int               `json:"allocated_vcpus,omitempty"`
	AllocatedMemMB  *int               `json:"allocated_mem_mb,omitempty"`
	AllocatedDiskMB *int               `json:"allocated_disk_mb,omitempty"`
	Labels          *map[string]string `json:"labels,omitempty"`
	Status          *string            `json:"status,omitempty"`
}

// MigrateVMRequest migrates a sandbox to a target worker or pool.
type MigrateVMRequest struct {
	TargetWorkerID string `json:"target_worker_id,omitempty"`
	TargetPoolID   string `json:"target_pool_id,omitempty"`
	Force          bool   `json:"force,omitempty"`
}

// MigratePoolRequest moves workers and/or sandboxes out of a pool.
type MigratePoolRequest struct {
	TargetPoolID     string `json:"target_pool_id,omitempty"`
	TargetWorkerID   string `json:"target_worker_id,omitempty"`
	MigrateWorkers   bool   `json:"migrate_workers,omitempty"`
	MigrateSandboxes bool   `json:"migrate_sandboxes,omitempty"`
	Force            bool   `json:"force,omitempty"`
}

// PoolMigrationResponse summarizes a pool migration operation.
type PoolMigrationResponse struct {
	SourcePoolID      string   `json:"source_pool_id"`
	TargetPoolID      string   `json:"target_pool_id"`
	WorkersMoved      int      `json:"workers_moved"`
	WorkersFailed     int      `json:"workers_failed"`
	SandboxesMigrated int      `json:"sandboxes_migrated"`
	SandboxesFailed   int      `json:"sandboxes_failed"`
	MovedWorkerIDs    []string `json:"moved_worker_ids"`
	MigratedVMIDs     []string `json:"migrated_vm_ids"`
	Errors            []string `json:"errors,omitempty"`
}

// BulkVMsRequest executes an action or deletion across multiple sandboxes.
type BulkVMsRequest struct {
	IDs    []string `json:"ids"`
	VMIDs  []string `json:"vm_ids,omitempty"`
	Action string   `json:"action,omitempty"` // "start", "stop", "save-state", "snapshot", "resume", "restart", "protect", "unprotect", "delete"
	Async  bool     `json:"async,omitempty"`
}

// BulkVMItemError reports a single failure in a bulk operation.
type BulkVMItemError struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// BulkVMsResponse summarizes the outcome of a bulk VM operation.
type BulkVMsResponse struct {
	Action    string            `json:"action,omitempty"`
	Status    string            `json:"status,omitempty"`
	TaskID    string            `json:"task_id,omitempty"`
	Requested int               `json:"requested"`
	Queued    int               `json:"queued,omitempty"`
	Succeeded []string          `json:"succeeded"`
	Failed    []BulkVMItemError `json:"failed"`
}
