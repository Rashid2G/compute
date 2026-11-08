package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"time"
)

// Client handles communication with Firecracker process API over Unix domain socket
type Client struct {
	socketPath string
	httpClient *http.Client
}

// NewClient creates an HTTP client connected to Firecracker's Unix domain socket
func NewClient(socketPath string) *Client {
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: 60 * time.Second,
	}

	return &Client{
		socketPath: socketPath,
		httpClient: httpClient,
	}
}

func (c *Client) request(ctx context.Context, method, endpoint string, body interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	// In unix socket http requests, host is ignored but must be present
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+endpoint, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("firecracker api request failed (%s %s): %w", method, endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("firecracker api error (%s %s, status %d): %s", method, endpoint, resp.StatusCode, string(respBody))
}

// SetMachineConfig configures vCPUs and memory
func (c *Client) SetMachineConfig(ctx context.Context, cfg MachineConfig) error {
	return c.request(ctx, http.MethodPut, "/machine-config", cfg)
}

// SetBootSource sets the kernel image and boot arguments
func (c *Client) SetBootSource(ctx context.Context, bs BootSource) error {
	return c.request(ctx, http.MethodPut, "/boot-source", bs)
}

// AttachDrive attaches a drive (e.g. rootfs)
func (c *Client) AttachDrive(ctx context.Context, drive Drive) error {
	return c.request(ctx, http.MethodPut, "/drives/"+drive.DriveID, drive)
}

// AttachNetworkInterface attaches a network TAP interface
func (c *Client) AttachNetworkInterface(ctx context.Context, iface NetworkInterface) error {
	return c.request(ctx, http.MethodPut, "/network-interfaces/"+iface.IfaceID, iface)
}

// StartInstance sends the InstanceStart action
func (c *Client) StartInstance(ctx context.Context) error {
	return c.request(ctx, http.MethodPut, "/actions", Action{ActionType: "InstanceStart"})
}

// Pause pauses the microVM execution
func (c *Client) Pause(ctx context.Context) error {
	return c.request(ctx, http.MethodPatch, "/vm", VMState{State: "Paused"})
}

// Resume resumes a paused microVM
func (c *Client) Resume(ctx context.Context) error {
	return c.request(ctx, http.MethodPatch, "/vm", VMState{State: "Resumed"})
}

// CreateSnapshot creates a snapshot of the microVM state and memory
func (c *Client) CreateSnapshot(ctx context.Context, snapshotPath, memFilePath string) error {
	params := SnapshotCreateParams{
		SnapshotType: "Full",
		SnapshotPath: snapshotPath,
		MemFilePath:  memFilePath,
	}
	return c.request(ctx, http.MethodPut, "/snapshot/create", params)
}

// LoadSnapshot loads a microVM from snapshot files
func (c *Client) LoadSnapshot(ctx context.Context, snapshotPath, memFilePath string, resume bool) error {
	params := SnapshotLoadParams{
		SnapshotPath: snapshotPath,
		MemFilePath:  memFilePath,
		ResumeVM:     resume,
	}
	return c.request(ctx, http.MethodPut, "/snapshot/load", params)
}

// SendCtrlAltDel sends Ctrl+Alt+Del for graceful reboot/shutdown
func (c *Client) SendCtrlAltDel(ctx context.Context) error {
	return c.request(ctx, http.MethodPut, "/actions", Action{ActionType: "SendCtrlAltDel"})
}

// WaitForSocket polls until the Firecracker Unix socket exists and accepts connections
func WaitForSocket(socketPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for socket %s", socketPath)
}

// LaunchProcess spawns the Firecracker background process
func LaunchProcess(binaryPath, socketPath, vmID, logPath string) (*exec.Cmd, error) {
	// Clean stale socket
	os.Remove(socketPath)

	args := []string{
		"--api-sock", socketPath,
		"--id", vmID,
	}

	cmd := exec.Command(binaryPath, args...)

	if logPath != "" {
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			cmd.Stdout = logFile
			cmd.Stderr = logFile
		}
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start firecracker: %w", err)
	}

	// Reap the child when it exits so stopped microVMs do not accumulate as
	// zombie processes under the daemon.
	go func() { _ = cmd.Wait() }()

	return cmd, nil
}
