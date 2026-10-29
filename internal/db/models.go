package db

import (
	"time"
)

// VMStatus represents the state of a VM
type VMStatus string

const (
	StatusCreated VMStatus = "created"
	StatusRunning VMStatus = "running"
	StatusPaused  VMStatus = "paused"
	StatusStopped VMStatus = "stopped"
	StatusFailed  VMStatus = "failed"
)

// VM represents a managed microVM
type VM struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Status       VMStatus  `json:"status"`
	VCPUs        int       `json:"vcpus"`
	MemoryMB     int       `json:"memory_mb"`
	IPAddress    string    `json:"ip_address"`
	MACAddress   string    `json:"mac_address"`
	TAPDevice    string    `json:"tap_device"`
	RootfsPath   string    `json:"rootfs_path"`
	KernelPath   string    `json:"kernel_path"`
	DiskPath     string    `json:"disk_path"`
	SnapshotPath string    `json:"snapshot_path,omitempty"`
	MemFilePath  string    `json:"mem_file_path,omitempty"`
	SSHKeyPath   string    `json:"ssh_key_path,omitempty"`
	PID          int       `json:"pid,omitempty"`
	SocketPath   string    `json:"socket_path,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
