package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

var (
	ErrAgentNotReady = errors.New("compute-agent is not ready")
	ErrVsockConnect  = errors.New("failed to connect to microvm vsock socket")
	ErrHandshake     = errors.New("firecracker vsock handshake failed")
)

// Client coordinates host-side communication with compute-agent over Firecracker's virtio-vsock UDS socket.
type Client struct {
	udsPath   string
	vsockPort int
}

// NewClient creates a new host-side agent client.
func NewClient(udsPath string, vsockPort int) *Client {
	if vsockPort <= 0 {
		vsockPort = DefaultVsockPort
	}
	return &Client{
		udsPath:   udsPath,
		vsockPort: vsockPort,
	}
}

// UDSPath returns the path to the Unix Domain Socket exposed by Firecracker.
func (c *Client) UDSPath() string {
	return c.udsPath
}

// Dial connects to Firecracker's host vsock UDS and completes the official host-to-guest CONNECT handshake.
func (c *Client) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.udsPath)
	if err != nil {
		return nil, fmt.Errorf("%w (%s): %v", ErrVsockConnect, c.udsPath, err)
	}

	// Honor caller context deadline during the CONNECT handshake so a stalled
	// hypervisor socket cannot block ReadString indefinitely.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	// Step 1: Host sends "CONNECT <port>\n"
	cmd := fmt.Sprintf("CONNECT %d\n", c.vsockPort)
	if _, err := conn.Write([]byte(cmd)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: failed to write CONNECT command: %v", ErrHandshake, err)
	}

	// Step 2: Firecracker replies "OK <local_port>\n"
	reader := bufio.NewReader(conn)
	ack, err := reader.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: failed to read CONNECT response: %v", ErrHandshake, err)
	}

	ackTrim := strings.TrimSpace(ack)
	if !strings.HasPrefix(ackTrim, "OK") {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: server rejected connection with '%s'", ErrHandshake, ackTrim)
	}

	// Clear handshake deadline; callers (roundTrip / ExecStream) manage stream deadlines.
	_ = conn.SetDeadline(time.Time{})

	// The connection is now a transparent, full-duplex byte stream to compute-agent in guest!
	return conn, nil
}

// roundTrip opens a vsock connection, sends a JSON-framed Request, and reads the Response.
func (c *Client) roundTrip(ctx context.Context, req Request) (*Response, error) {
	conn, err := c.Dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// Apply deadline from context if present
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}
	reqBytes = append(reqBytes, '\n')

	if _, err := conn.Write(reqBytes); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	reader := bufio.NewReader(conn)
	resBytes, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var res Response
	if err := json.Unmarshal(resBytes, &res); err != nil {
		return nil, fmt.Errorf("failed to parse response JSON: %w (raw: %s)", err, string(resBytes))
	}

	if res.Status != StatusOK {
		if res.Error != "" {
			return &res, errors.New(res.Error)
		}
		return &res, fmt.Errorf("agent returned error status without message")
	}

	return &res, nil
}

// Ping performs a lightweight health probe to verify compute-agent is active.
func (c *Client) Ping(ctx context.Context) error {
	res, err := c.roundTrip(ctx, Request{Action: ActionPing})
	if err != nil {
		return err
	}
	if res.Status != StatusOK {
		return fmt.Errorf("%w: status=%s error=%s", ErrAgentNotReady, res.Status, res.Error)
	}
	return nil
}

// WaitReady polls until compute-agent responds to ping or the context expires.
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
			probeCtx, probeCancel := context.WithTimeout(ctx, 200*time.Millisecond)
			err := c.Ping(probeCtx)
			probeCancel()
			if err == nil {
				return nil
			}
		}
	}
}

// Exec executes a non-streaming command inside the microVM.
func (c *Client) Exec(ctx context.Context, req Request) (*Response, error) {
	req.Action = ActionExec
	return c.roundTrip(ctx, req)
}

// StreamSession provides interactive control over an active streaming command.
type StreamSession struct {
	conn    net.Conn
	reader  *bufio.Reader
	writeMu sync.Mutex
	done    chan struct{}
}

// ExecStream starts a streaming command execution with bidirectional framing.
func (c *Client) ExecStream(ctx context.Context, req Request, onEvent func(StreamFrame) error) (*Response, error) {
	req.Action = ActionExecStream
	conn, err := c.Dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to encode stream request: %w", err)
	}
	reqBytes = append(reqBytes, '\n')

	if _, err := conn.Write(reqBytes); err != nil {
		return nil, fmt.Errorf("failed to send stream request: %w", err)
	}

	reader := bufio.NewReader(conn)
	var finalRes Response
	var finalExit int
	var durationMs int64
	hasExit := false

	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var frame StreamFrame
			if err := json.Unmarshal(line, &frame); err == nil {
				if onEvent != nil {
					if cbErr := onEvent(frame); cbErr != nil {
						return nil, cbErr
					}
				}
				if frame.Type == FrameExit {
					hasExit = true
					finalExit = frame.ExitCode
					finalRes.ExitCode = finalExit
					finalRes.Status = StatusOK
					if frame.Error != "" {
						finalRes.Error = frame.Error
					}
					break
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, readErr
		}
	}

	if !hasExit {
		finalRes.Status = StatusOK
		finalRes.ExitCode = 0
	}
	finalRes.DurationMS = durationMs
	return &finalRes, nil
}

// StartStreamSession establishes an interactive streaming session where frames can be read and sent dynamically.
func (c *Client) StartStreamSession(ctx context.Context, req Request) (*StreamSession, error) {
	req.Action = ActionExecStream
	conn, err := c.Dial(ctx)
	if err != nil {
		return nil, err
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	reqBytes = append(reqBytes, '\n')
	if _, err := conn.Write(reqBytes); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return &StreamSession{
		conn:   conn,
		reader: bufio.NewReader(conn),
		done:   make(chan struct{}),
	}, nil
}

// SendStdin sends a chunk of stdin data to the running streaming process.
func (s *StreamSession) SendStdin(data string) error {
	return s.sendFrame(StreamFrame{Type: FrameStdin, Data: data})
}

// SendResize updates the terminal dimensions of an active PTY session.
func (s *StreamSession) SendResize(rows, cols uint16) error {
	return s.sendFrame(StreamFrame{Type: FrameResize, Rows: rows, Cols: cols})
}

// SendSignal sends an OS signal (e.g. "SIGINT", "SIGTERM", "SIGKILL") to the streaming process.
func (s *StreamSession) SendSignal(signal string) error {
	return s.sendFrame(StreamFrame{Type: FrameSignal, Signal: signal})
}

// Close terminates the stream session connection.
func (s *StreamSession) Close() error {
	return s.conn.Close()
}

func (s *StreamSession) sendFrame(frame StreamFrame) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = s.conn.Write(data)
	return err
}

// ReadFrame reads the next frame from the stream session.
func (s *StreamSession) ReadFrame() (StreamFrame, error) {
	line, err := s.reader.ReadBytes('\n')
	if err != nil {
		return StreamFrame{}, err
	}
	var frame StreamFrame
	err = json.Unmarshal(line, &frame)
	return frame, err
}

// ReadFile reads the content of a file inside the guest microVM.
func (c *Client) ReadFile(ctx context.Context, path, format string) (*Response, error) {
	return c.roundTrip(ctx, Request{
		Action: ActionFileRead,
		Path:   path,
		Format: format,
	})
}

// WriteFile writes a payload to a file inside the guest microVM.
func (c *Client) WriteFile(ctx context.Context, path string, data []byte, mode uint32) error {
	b64 := base64.StdEncoding.EncodeToString(data)
	_, err := c.roundTrip(ctx, Request{
		Action:  ActionFileWrite,
		Path:    path,
		Content: b64,
		Format:  "base64",
		Mode:    mode,
	})
	return err
}

// ListFiles lists directory entries inside the guest microVM.
func (c *Client) ListFiles(ctx context.Context, path string) ([]FileInfo, error) {
	res, err := c.roundTrip(ctx, Request{
		Action: ActionFileList,
		Path:   path,
	})
	if err != nil {
		return nil, err
	}
	return res.Entries, nil
}

// Mkdir creates a directory and any parent directories inside the guest microVM.
func (c *Client) Mkdir(ctx context.Context, path string, mode uint32) error {
	_, err := c.roundTrip(ctx, Request{
		Action: ActionFileMkdir,
		Path:   path,
		Mode:   mode,
	})
	return err
}

// RemoveFile deletes a file or directory inside the guest microVM.
func (c *Client) RemoveFile(ctx context.Context, path string, recursive bool) error {
	_, err := c.roundTrip(ctx, Request{
		Action:    ActionFileRemove,
		Path:      path,
		Recursive: recursive,
	})
	return err
}

// StatFile queries file metadata inside the guest microVM.
func (c *Client) StatFile(ctx context.Context, path string) (*FileInfo, error) {
	res, err := c.roundTrip(ctx, Request{
		Action: ActionFileStat,
		Path:   path,
	})
	if err != nil {
		return nil, err
	}
	return res.FileInfo, nil
}

// ProcList inspects running processes inside the guest microVM.
func (c *Client) ProcList(ctx context.Context) ([]Process, error) {
	res, err := c.roundTrip(ctx, Request{
		Action: ActionProcList,
	})
	if err != nil {
		return nil, err
	}
	return res.Processes, nil
}

// ProcStart launches a detached background process inside the guest microVM.
func (c *Client) ProcStart(ctx context.Context, cmd []string, env map[string]string, cwd string) (int, error) {
	res, err := c.roundTrip(ctx, Request{
		Action:  ActionProcStart,
		Cmd:     cmd,
		Env:     env,
		WorkDir: cwd,
	})
	if err != nil {
		return 0, err
	}
	return res.PID, nil
}

// ProcKill sends an OS signal to a process inside the guest microVM.
func (c *Client) ProcKill(ctx context.Context, pid int, signal string) error {
	if signal == "" {
		signal = "SIGTERM"
	}
	_, err := c.roundTrip(ctx, Request{
		Action: ActionProcKill,
		PID:    pid,
		Signal: signal,
	})
	return err
}

// VolumeMount formats (if needed) and mounts an attached block device inside the guest microVM.
func (c *Client) VolumeMount(ctx context.Context, device, mountPoint, fstype string, readOnly bool) error {
	_, err := c.roundTrip(ctx, Request{
		Action:     ActionVolumeMount,
		Device:     device,
		MountPoint: mountPoint,
		FSType:     fstype,
		ReadOnly:   readOnly,
	})
	return err
}

// VolumeUnmount unmounts a mounted path inside the guest microVM.
func (c *Client) VolumeUnmount(ctx context.Context, mountPoint string) error {
	_, err := c.roundTrip(ctx, Request{
		Action:     ActionVolumeUnmount,
		MountPoint: mountPoint,
	})
	return err
}

// Stats queries guest CPU, memory, disk, and load statistics.
func (c *Client) Stats(ctx context.Context) (*GuestStats, error) {
	res, err := c.roundTrip(ctx, Request{
		Action: ActionStats,
	})
	if err != nil {
		return nil, err
	}
	return res.Stats, nil
}

// ClockSync updates the guest system clock to align with the host's current nanoseconds.
func (c *Client) ClockSync(ctx context.Context, unixNano int64) error {
	if unixNano <= 0 {
		unixNano = time.Now().UnixNano()
	}
	_, err := c.roundTrip(ctx, Request{
		Action:   ActionClockSync,
		UnixNano: unixNano,
	})
	return err
}

// Shutdown initiates guest sync and graceful poweroff.
func (c *Client) Shutdown(ctx context.Context) error {
	_, err := c.roundTrip(ctx, Request{
		Action: ActionShutdown,
	})
	return err
}
