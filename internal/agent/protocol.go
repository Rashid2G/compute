package agent

import (
	"hash/fnv"
	"time"
)

// Default vsock port for compute-agent
const DefaultVsockPort = 1024

// Protocol actions
const (
	ActionPing          = "ping"
	ActionReady         = "ready"
	ActionExec          = "exec"
	ActionExecStream    = "exec_stream"
	ActionFileRead      = "file_read"
	ActionFileWrite     = "file_write"
	ActionFileList      = "file_list"
	ActionFileMkdir     = "file_mkdir"
	ActionFileRemove    = "file_remove"
	ActionFileStat      = "file_stat"
	ActionProcList      = "proc_list"
	ActionProcStart     = "proc_start"
	ActionProcKill      = "proc_kill"
	ActionVolumeMount   = "volume_mount"
	ActionVolumeUnmount = "volume_unmount"
	ActionStats         = "stats"
	ActionClockSync     = "clock_sync"
	ActionShutdown      = "shutdown"
)

// Stream frame types
const (
	FrameStdin  = "stdin"
	FrameStdout = "stdout"
	FrameStderr = "stderr"
	FrameResize = "resize"
	FrameSignal = "signal"
	FrameExit   = "exit"
	FrameError  = "error"
)

// Status strings
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// Request defines the standard JSON payload sent to compute-agent over vsock.
type Request struct {
	Action  string `json:"action"`
	Version string `json:"version,omitempty"`

	// Exec options
	Cmd        []string          `json:"cmd,omitempty"`
	Command    string            `json:"command,omitempty"`
	User       string            `json:"user,omitempty"`
	WorkDir    string            `json:"workdir,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Stdin      string            `json:"stdin,omitempty"`
	TimeoutSec int               `json:"timeout_sec,omitempty"`

	// Streaming & PTY
	PTY  bool   `json:"pty,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
	Cols uint16 `json:"cols,omitempty"`

	// File operations
	Path      string `json:"path,omitempty"`
	Content   string `json:"content,omitempty"` // plain text or base64
	Format    string `json:"format,omitempty"`  // "text" or "base64"
	Mode      uint32 `json:"mode,omitempty"`
	Recursive bool   `json:"recursive,omitempty"`

	// Process operations
	PID    int    `json:"pid,omitempty"`
	Signal string `json:"signal,omitempty"`

	// Volume operations
	Device     string `json:"device,omitempty"`      // e.g. "/dev/vdb"
	MountPoint string `json:"mount_point,omitempty"` // e.g. "/mnt/data"
	FSType     string `json:"fstype,omitempty"`      // e.g. "ext4"
	ReadOnly   bool   `json:"read_only,omitempty"`

	// Clock sync
	UnixNano int64 `json:"unix_nano,omitempty"`
}

// Response defines the standard JSON payload returned by compute-agent over vsock.
type Response struct {
	Status  string `json:"status"` // "ok" or "error"
	Error   string `json:"error,omitempty"`
	Version string `json:"version,omitempty"`

	// Exec results
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms,omitempty"`

	// File results
	Size     int64      `json:"size,omitempty"`
	Content  string     `json:"content,omitempty"`
	Format   string     `json:"format,omitempty"`
	Entries  []FileInfo `json:"entries,omitempty"`
	FileInfo *FileInfo  `json:"file_info,omitempty"`

	// Process results
	Processes []Process `json:"processes,omitempty"`
	PID       int       `json:"pid,omitempty"`

	// Guest Telemetry / Stats
	Stats *GuestStats `json:"stats,omitempty"`

	// Clock sync
	CurrentUnixNano int64 `json:"current_unix_nano,omitempty"`
}

// StreamFrame represents a bidirectional streaming frame over an exec_stream vsock connection.
type StreamFrame struct {
	Type     string `json:"type"` // "stdin", "stdout", "stderr", "resize", "signal", "exit", "error"
	Data     string `json:"data,omitempty"`
	Rows     uint16 `json:"rows,omitempty"`
	Cols     uint16 `json:"cols,omitempty"`
	Signal   string `json:"signal,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

// FileInfo describes a guest file or directory entry.
type FileInfo struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	Mode    uint32    `json:"mode"`
	IsDir   bool      `json:"is_dir"`
	ModTime time.Time `json:"mod_time"`
}

// Process describes a guest process inspected from /proc.
type Process struct {
	PID     int     `json:"pid"`
	PPID    int     `json:"ppid"`
	User    string  `json:"user"`
	CPU     float64 `json:"cpu"`
	Memory  float64 `json:"memory"`
	Status  string  `json:"status"`
	Command string  `json:"command"`
	Args    string  `json:"args"`
}

// GuestStats contains real-time resource utilization telemetry from inside the guest.
type GuestStats struct {
	CPUPercent  float64 `json:"cpu_percent"`
	MemUsedMB   int64   `json:"mem_used_mb"`
	MemTotalMB  int64   `json:"mem_total_mb"`
	DiskUsedMB  int64   `json:"disk_used_mb"`
	DiskTotalMB int64   `json:"disk_total_mb"`
	LoadAvg1    float64 `json:"load_avg_1"`
	LoadAvg5    float64 `json:"load_avg_5"`
	LoadAvg15   float64 `json:"load_avg_15"`
	UptimeSec   int64   `json:"uptime_sec"`
}

// AllocateGuestCID deterministically derives a unique guest CID from the VM ID
// in the valid Firecracker vsock range [3, 2147483647], avoiding reserved CIDs
// (0: hypervisor, 1: loopback, 2: host).
func AllocateGuestCID(vmID string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(vmID))
	val := h.Sum32()
	// Range: [3, 2147483647], pool size = 2147483645
	return 3 + (val % 2147483645)
}
