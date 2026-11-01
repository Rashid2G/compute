package api

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
	Architecture string                 `json:"architecture"`
	AgentVersion string                 `json:"agent_version,omitempty"`
	Capabilities map[string]interface{} `json:"capabilities,omitempty"`
}
type WorkerHelloAck struct {
	WorkerMessage
	Accepted            bool   `json:"accepted"`
	WorkerID            string `json:"worker_id"`
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
