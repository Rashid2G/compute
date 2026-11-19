package api

import "encoding/json"

// WorkerMessage is the common envelope for worker stream frames.
type WorkerMessage struct {
	Version        int    `json:"protocol_version"`
	Type           string `json:"type"`
	ID             string `json:"id"`
	Seq            int64  `json:"seq,omitempty"`
	TraceID        string `json:"trace_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	WorkerID       string `json:"worker_id,omitempty"`
	JobID          string `json:"job_id,omitempty"`
}

type WorkerHello struct {
	WorkerMessage
	Token        string                 `json:"token,omitempty"`
	Name         string                 `json:"name"`
	PoolID       string                 `json:"pool_id,omitempty"`
	Architecture string                 `json:"architecture"`
	AgentVersion string                 `json:"agent_version,omitempty"`
	Capabilities map[string]interface{} `json:"capabilities,omitempty"`
}
type WorkerHelloAck struct {
	WorkerMessage
	Accepted            bool   `json:"accepted"`
	WorkerID            string `json:"worker_id"`
	PoolID              string `json:"pool_id,omitempty"`
	HeartbeatIntervalMS int    `json:"heartbeat_interval_ms"`
}
type WorkerHeartbeat struct {
	WorkerMessage
	Status       string                 `json:"status"`
	Capabilities map[string]interface{} `json:"capabilities,omitempty"`
	Metrics      map[string]interface{} `json:"metrics,omitempty"`
}
type WorkerHeartbeatAck struct {
	WorkerMessage
	ServerTime      int64 `json:"server_time"`
	NextHeartbeatMS int   `json:"next_heartbeat_ms,omitempty"`
}
type WorkerCapabilities struct {
	WorkerMessage
	Capabilities map[string]interface{} `json:"capabilities"`
}
type WorkerAssignment struct {
	WorkerMessage
	LeaseID   string  `json:"lease_id"`
	AttemptID string  `json:"attempt_id"`
	OwnerID   string  `json:"owner_id,omitempty"`
	Spec      JobSpec `json:"spec"`
}
type JobSpec struct {
	ImageID        string            `json:"image_id"`
	Architecture   string            `json:"architecture"`
	VCPUs          int               `json:"vcpus"`
	MemoryMB       int               `json:"memory_mb"`
	DiskMB         int               `json:"disk_mb"`
	Command        []string          `json:"command,omitempty"`
	CWD            string            `json:"cwd,omitempty"`
	Files          string            `json:"files,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	NetworkPolicy  string            `json:"network_policy,omitempty"`
	RepoURL        string            `json:"repo_url,omitempty"`
	RepoName       string            `json:"repo_name,omitempty"`
	Branch         string            `json:"branch,omitempty"`
	CommitSHA      string            `json:"commit_sha,omitempty"`
	Source         string            `json:"source,omitempty"`

	// AuthorizedOwnerIDs and AllowCrossOwner are populated by the control plane
	// after authenticating the job submission. They are never trusted directly
	// from client input and let the worker consume org-owned images.
	AuthorizedOwnerIDs []string `json:"authorized_owner_ids,omitempty"`
	AllowCrossOwner    bool     `json:"allow_cross_owner,omitempty"`
}
type WorkerAssignmentAck struct {
	WorkerMessage
	LeaseID  string `json:"lease_id"`
	Accepted bool   `json:"accepted"`
}
type WorkerAssignmentReject struct {
	WorkerMessage
	LeaseID string `json:"lease_id"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}
type WorkerWorkloadEvent struct {
	WorkerMessage
	AttemptID string                 `json:"attempt_id"`
	Event     string                 `json:"event"`
	Data      map[string]interface{} `json:"data,omitempty"`
}
type WorkerLogChunk struct {
	WorkerMessage
	AttemptID string `json:"attempt_id"`
	Stream    string `json:"stream"`
	Data      string `json:"data"`
	Truncated bool   `json:"truncated,omitempty"`
}
type WorkerArtifactBegin struct {
	WorkerMessage
	AttemptID  string `json:"attempt_id"`
	ArtifactID string `json:"artifact_id"`
	Name       string `json:"name"`
	MediaType  string `json:"media_type,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
	SHA256     string `json:"sha256"`
}
type WorkerArtifactChunk struct {
	WorkerMessage
	ArtifactID string `json:"artifact_id"`
	Offset     int64  `json:"offset"`
	Data       string `json:"data"`
}
type WorkerArtifactEnd struct {
	WorkerMessage
	ArtifactID string `json:"artifact_id"`
	SizeBytes  int64  `json:"size_bytes"`
	SHA256     string `json:"sha256"`
}
type WorkerCancel struct {
	WorkerMessage
	LeaseID   string `json:"lease_id"`
	AttemptID string `json:"attempt_id"`
	Reason    string `json:"reason,omitempty"`
}
type WorkerCancelAck struct {
	WorkerMessage
	LeaseID  string `json:"lease_id"`
	Accepted bool   `json:"accepted"`
}
type WorkerDrain struct {
	WorkerMessage
	Deadline int64  `json:"deadline,omitempty"`
	Reason   string `json:"reason,omitempty"`
}
type WorkerReconcile struct {
	WorkerMessage
	ActiveLeases []string `json:"active_leases"`
}
type WorkerReconcileResponse struct {
	WorkerMessage
	Leases []WorkerLeaseState `json:"leases"`
}
type WorkerLeaseState struct {
	LeaseID string `json:"lease_id"`
	JobID   string `json:"job_id"`
	State   string `json:"state"`
}
type WorkerError struct {
	WorkerMessage
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

// WorkerRPCRequest invokes a synchronous RPC method on a connected worker.
type WorkerRPCRequest struct {
	WorkerMessage
	RequestID string          `json:"request_id"`
	Method    string          `json:"method"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// WorkerRPCResponse returns the result or error for a WorkerRPCRequest.
type WorkerRPCResponse struct {
	WorkerMessage
	RequestID string          `json:"request_id"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	Code      string          `json:"code,omitempty"`
}

// WorkerTunnelOpen asks a worker to dial targetAddr on its host/bridge network.
type WorkerTunnelOpen struct {
	WorkerMessage
	TunnelID   string `json:"tunnel_id"`
	TargetAddr string `json:"target_addr"`
}

// WorkerTunnelData carries base64-encoded TCP stream bytes over the worker WebSocket.
type WorkerTunnelData struct {
	WorkerMessage
	TunnelID string `json:"tunnel_id"`
	Data     string `json:"data,omitempty"`
	EOF      bool   `json:"eof,omitempty"`
}

// WorkerTunnelClose closes a tunneled TCP stream.
type WorkerTunnelClose struct {
	WorkerMessage
	TunnelID string `json:"tunnel_id"`
	Error    string `json:"error,omitempty"`
}

// WorkerVMCreateRequest carries sandbox creation fields plus resolved control-plane SSH keys.
type WorkerVMCreateRequest struct {
	Name              string   `json:"name"`
	OwnerID           string   `json:"owner_id,omitempty"`
	Tier              string   `json:"tier,omitempty"`
	VCPUs             int      `json:"vcpus"`
	MemoryMB          int      `json:"memory_mb,omitempty"`
	DiskMB            int      `json:"disk_mb,omitempty"`
	DiskGB            int      `json:"disk_gb,omitempty"`
	Description       string   `json:"description,omitempty"`
	AutoStart         bool     `json:"auto_start,omitempty"`
	Image             string   `json:"image,omitempty"`
	Template          string   `json:"template,omitempty"`
	IPAddress         string   `json:"ip_address,omitempty"`
	MACAddress        string   `json:"mac_address,omitempty"`
	PublicIP          *bool    `json:"public_ip,omitempty"`
	PublicIPEnabled   *bool    `json:"public_ip_enabled,omitempty"`
	DedicatedIP       *bool    `json:"dedicated_ip,omitempty"`
	NetworkIsolation  string   `json:"network_isolation,omitempty"`
	Isolated          *bool    `json:"isolated,omitempty"`
	NetworkIsolated   *bool    `json:"network_isolated,omitempty"`
	DHCP              *bool    `json:"dhcp,omitempty"`
	SSHPublicKey      string   `json:"ssh_public_key,omitempty"`
	AuthorizedSSHKeys []string `json:"authorized_ssh_keys,omitempty"`
	SSHPassword       string   `json:"ssh_password,omitempty"`
	Protected         *bool    `json:"protected,omitempty"`
	StartupScript     string   `json:"startup_script,omitempty"`
	Command           string   `json:"command,omitempty"`
	BuildCommand      string   `json:"build_command,omitempty"`
	ExecUser          string   `json:"exec_user,omitempty"`
	PoolID            string   `json:"pool_id,omitempty"`
	WorkerID          string   `json:"worker_id,omitempty"`
}

// WorkerVMIDRequest identifies a sandbox for lifecycle RPCs.
type WorkerVMIDRequest struct {
	VMID string `json:"vm_id"`
}

// WorkerVMExecRequest executes a command inside a sandbox on a worker.
type WorkerVMExecRequest struct {
	VMID           string `json:"vm_id"`
	Command        string `json:"command"`
	User           string `json:"user,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	StdinBase64    string `json:"stdin_base64,omitempty"`
}

// WorkerVMFilesRequest runs a filesystem operation inside a sandbox on a worker.
type WorkerVMFilesRequest struct {
	VMID    string `json:"vm_id"`
	Action  string `json:"action"` // "read", "write", "list", "mkdir", "remove"
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Format  string `json:"format,omitempty"`
}

// WorkerVMProcessesRequest inspects or controls guest processes on a worker.
type WorkerVMProcessesRequest struct {
	VMID    string            `json:"vm_id"`
	Action  string            `json:"action"` // "list", "start", "kill"
	Command string            `json:"command,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	PID     int               `json:"pid,omitempty"`
	Signal  string            `json:"signal,omitempty"`
}

// WorkerVMSSHSyncRequest reconciles authorized_keys for a sandbox on a worker.
type WorkerVMSSHSyncRequest struct {
	VMID       string   `json:"vm_id"`
	PublicKeys []string `json:"public_keys"`
}

// WorkerVMExportDiskRequest requests a gzipped chunk of a VM disk or snapshot file.
type WorkerVMExportDiskRequest struct {
	VMID   string `json:"vm_id"`
	Kind   string `json:"kind,omitempty"` // "disk" (default), "snapshot", "mem"
	Offset int64  `json:"offset"`
	Length int    `json:"length"`
}

// WorkerVMExportDiskResponse returns a base64-encoded gzipped chunk of a VM disk/snapshot.
type WorkerVMExportDiskResponse struct {
	Offset    int64  `json:"offset"`
	RawLength int    `json:"raw_length"`
	TotalSize int64  `json:"total_size"`
	Data      string `json:"data"`
	EOF       bool   `json:"eof"`
	Gzipped   bool   `json:"gzipped"`
}

// WorkerVMImportDiskRequest streams a disk/snapshot chunk to a target worker and optionally registers the VM.
type WorkerVMImportDiskRequest struct {
	VMID      string          `json:"vm_id"`
	Kind      string          `json:"kind,omitempty"` // "disk" (default), "snapshot", "mem"
	Offset    int64           `json:"offset"`
	TotalSize int64           `json:"total_size"`
	Data      string          `json:"data"`
	Gzipped   bool            `json:"gzipped"`
	Finalize  bool            `json:"finalize,omitempty"`
	VM        json.RawMessage `json:"vm,omitempty"`
}
