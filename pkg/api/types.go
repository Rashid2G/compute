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
