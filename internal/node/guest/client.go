package guest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

var (
	ErrGuestNotReady = errors.New("guest agent is not ready")
	ErrVsockConnect  = errors.New("failed to connect to guest vsock")
)

type Client struct {
	udsPath   string
	vsockPort int
}

type ExecRequest struct {
	Cmd   []string          `json:"cmd"`
	Env   map[string]string `json:"env,omitempty"`
	Stdin string            `json:"stdin,omitempty"`
}

type ExecResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
}

type StatsResult struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemUsedMB  int64   `json:"mem_used_mb"`
	MemTotalMB int64   `json:"mem_total_mb"`
	UptimeSec  int64   `json:"uptime_sec"`
}

func NewClient(udsPath string, vsockPort int) *Client {
	if vsockPort <= 0 {
		vsockPort = 1024
	}
	return &Client{
		udsPath:   udsPath,
		vsockPort: vsockPort,
	}
}

// dial establishes a connection to the guest agent via Firecracker's UDS vsock proxy.
func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.udsPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVsockConnect, err)
	}

	// Handshake: write CONNECT <port>\n
	connectCmd := fmt.Sprintf("CONNECT %d\n", c.vsockPort)
	if _, err := conn.Write([]byte(connectCmd)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to send CONNECT to firecracker vsock UDS: %w", err)
	}

	reader := bufio.NewReader(conn)
	ack, err := reader.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to read CONNECT ack from firecracker vsock: %w", err)
	}

	if !strings.HasPrefix(ack, "OK") {
		conn.Close()
		return nil, fmt.Errorf("firecracker vsock handshake rejected: %s", strings.TrimSpace(ack))
	}

	return conn, nil
}

// Ping checks if guest agent is reachable.
func (c *Client) Ping(ctx context.Context) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	req := map[string]string{"action": "ping"}
	data, _ := json.Marshal(req)
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return err
	}

	reader := bufio.NewReader(conn)
	resBytes, err := reader.ReadBytes('\n')
	if err != nil {
		return err
	}

	var res map[string]interface{}
	if err := json.Unmarshal(resBytes, &res); err != nil {
		return err
	}

	if res["status"] != "ok" {
		return fmt.Errorf("ping returned non-ok status: %v", res)
	}
	return nil
}

// WaitReady polls until guest agent responds to ping or context expires.
func (c *Client) WaitReady(ctx context.Context, pollInterval time.Duration) error {
	if pollInterval <= 0 {
		pollInterval = 50 * time.Millisecond
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := c.Ping(ctx); err == nil {
				return nil
			}
		}
	}
}

// Exec executes a command inside the guest microVM over vsock.
func (c *Client) Exec(ctx context.Context, req ExecRequest) (*ExecResult, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	payload := map[string]interface{}{
		"action": "exec",
		"cmd":    req.Cmd,
		"env":    req.Env,
		"stdin":  req.Stdin,
	}
	data, _ := json.Marshal(payload)
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("failed to send exec request: %w", err)
	}

	reader := bufio.NewReader(conn)
	resBytes, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read exec response: %w", err)
	}

	var res struct {
		Status     string `json:"status"`
		Error      string `json:"error"`
		Stdout     string `json:"stdout"`
		Stderr     string `json:"stderr"`
		ExitCode   int    `json:"exit_code"`
		DurationMS int64  `json:"duration_ms"`
	}
	if err := json.Unmarshal(resBytes, &res); err != nil {
		return nil, fmt.Errorf("failed to parse exec response: %w", err)
	}

	if res.Status != "ok" && res.Error != "" {
		return nil, errors.New(res.Error)
	}

	return &ExecResult{
		Stdout:     res.Stdout,
		Stderr:     res.Stderr,
		ExitCode:   res.ExitCode,
		DurationMS: res.DurationMS,
	}, nil
}

// Shutdown requests graceful poweroff of the guest.
func (c *Client) Shutdown(ctx context.Context) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	req := map[string]string{"action": "shutdown"}
	data, _ := json.Marshal(req)
	data = append(data, '\n')
	_, err = conn.Write(data)
	return err
}
