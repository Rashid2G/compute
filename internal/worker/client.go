package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/Rashid2G/compute/pkg/api"
	"github.com/coder/websocket"
)

type Client struct {
	Config     Config
	Supervisor WorkloadSupervisor
	VMManager  *vm.Manager
	mu         sync.Mutex
	conn       *websocket.Conn
	workerID   string
	seq        int64

	tunMu   sync.Mutex
	tunnels map[string]*clientTunnel
}

func (c *Client) getVMManager() *vm.Manager {
	if c.VMManager != nil {
		return c.VMManager
	}
	if sup, ok := c.Supervisor.(*MicroVMSupervisor); ok {
		return sup.VMManager()
	}
	return nil
}

func (c *Client) send(ctx context.Context, v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return fmt.Errorf("worker connection not active")
	}
	c.seq++
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return c.conn.Write(ctx, websocket.MessageText, b)
}

func (c *Client) buildCapabilities() map[string]interface{} {
	caps := DetectCapabilities()
	if c.Config.MaxVCPUs > 0 {
		caps["max_vcpus"] = c.Config.MaxVCPUs
	}
	if c.Config.MaxMemoryMB > 0 {
		caps["max_memory_mb"] = c.Config.MaxMemoryMB
	}
	if c.Config.MaxDiskMB > 0 {
		caps["max_disk_mb"] = c.Config.MaxDiskMB
	}
	return caps
}

func (c *Client) buildMetrics(ctx context.Context) map[string]interface{} {
	caps := DetectCapabilities()
	metrics := map[string]interface{}{
		"used_vcpus":   0,
		"used_mem_mb":  0,
		"used_disk_mb": 0,
		"running_vms":  0,
	}
	if availMem, ok := caps["memory_available_mb"]; ok {
		metrics["memory_available_mb"] = availMem
	}
	if availDisk, ok := caps["disk_available_mb"]; ok {
		metrics["disk_available_mb"] = availDisk
	}
	mgr := c.getVMManager()
	if mgr == nil {
		return metrics
	}
	vms, err := mgr.ListVMs(ctx)
	if err != nil {
		return metrics
	}
	var usedVCPUs, usedMemMB, usedDiskMB, runningVMs int
	for _, v := range vms {
		if v == nil {
			continue
		}
		usedDiskMB += v.DiskMB
		if v.Status == db.StatusRunning {
			runningVMs++
			usedVCPUs += v.VCPUs
			usedMemMB += v.MemoryMB
		}
	}
	metrics["used_vcpus"] = usedVCPUs
	metrics["used_mem_mb"] = usedMemMB
	metrics["used_disk_mb"] = usedDiskMB
	metrics["running_vms"] = runningVMs
	return metrics
}

func (c *Client) Run(ctx context.Context) error {
	backoff := time.Second
	attempt := 0
	for {
		if attempt > 0 {
			log.Printf("[worker] attempting reconnect to %s", c.Config.ControlPlaneURL)
		}
		attempt++
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			log.Printf("[worker] connection lost: %v (reconnecting in %s)", err, backoff)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

// runOnce establishes one control-plane session. The enrollment token is
// single-use, but the daemon accepts the same worker name reconnecting with
// it, so a restarted worker re-enrolls instead of failing permanently.
func (c *Client) runOnce(ctx context.Context) error {
	u, e := url.Parse(c.Config.ControlPlaneURL)
	if e != nil {
		return e
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/worker/connect"
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else if u.Scheme == "http" {
		u.Scheme = "ws"
	}
	var dialOpts *websocket.DialOptions
	if c.Config.Token != "" {
		headers := http.Header{
			"X-Worker-Token": []string{c.Config.Token},
		}
		if c.Config.Name != "" {
			headers.Set("X-Worker-Name", c.Config.Name)
		}
		dialOpts = &websocket.DialOptions{HTTPHeader: headers}
	}
	log.Printf("[worker] connecting to control plane %s (name=%s pool=%s)", u.String(), c.Config.Name, c.Config.PoolID)
	conn, _, e := websocket.Dial(ctx, u.String(), dialOpts)
	if e != nil {
		return e
	}
	conn.SetReadLimit(16 << 20)
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.closeAllTunnels()
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()
	workerMsgID := c.Config.Name
	if workerMsgID == "" {
		workerMsgID = "hello"
	}
	hello := api.WorkerHello{
		WorkerMessage: api.WorkerMessage{Version: 1, Type: "worker.hello", ID: workerMsgID},
		Token:         c.Config.Token,
		Name:          c.Config.Name,
		PoolID:        c.Config.PoolID,
		Architecture:  runtimeArch(),
		Capabilities:  c.buildCapabilities(),
	}
	if e = c.send(ctx, hello); e != nil {
		return e
	}
	_, b, e := conn.Read(ctx)
	if e != nil {
		return e
	}
	var ack api.WorkerHelloAck
	if e = json.Unmarshal(b, &ack); e != nil {
		return e
	}
	if !ack.Accepted {
		return fmt.Errorf("worker handshake rejected")
	}
	c.workerID = ack.WorkerID
	log.Printf("[worker] enrolled id=%s pool=%s", ack.WorkerID, ack.PoolID)

	connCtx, cancelConn := context.WithCancel(ctx)
	defer cancelConn()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		c.heartbeat(connCtx)
	}()
	defer func() { cancelConn(); <-heartbeatDone }()

	for {
		_, b, e = conn.Read(ctx)
		if e != nil {
			return e
		}
		var head api.WorkerMessage
		if json.Unmarshal(b, &head) != nil {
			continue
		}
		switch head.Type {
		case "worker.assignment":
			var a api.WorkerAssignment
			_ = json.Unmarshal(b, &a)
			c.assignment(ctx, a)
		case "worker.cancel":
			var x api.WorkerCancel
			_ = json.Unmarshal(b, &x)
			if cancel := activeCancel(x.AttemptID); cancel != nil {
				cancel()
			}
		case "worker.rpc.req":
			var req api.WorkerRPCRequest
			if err := json.Unmarshal(b, &req); err == nil {
				go c.handleRPC(connCtx, req)
			}
		case "worker.tunnel.open":
			var open api.WorkerTunnelOpen
			if err := json.Unmarshal(b, &open); err == nil {
				go c.handleTunnelOpen(connCtx, open)
			}
		case "worker.tunnel.data":
			var frame api.WorkerTunnelData
			if err := json.Unmarshal(b, &frame); err == nil {
				c.handleTunnelData(frame)
			}
		case "worker.tunnel.close":
			var frame api.WorkerTunnelClose
			if err := json.Unmarshal(b, &frame); err == nil {
				c.handleTunnelClose(frame)
			}
		}
	}
}

func runtimeArch() string { return runtime.GOARCH }
func (c *Client) heartbeat(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	first := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.send(ctx, api.WorkerHeartbeat{
				WorkerMessage: api.WorkerMessage{Version: 1, Type: "worker.heartbeat", WorkerID: c.workerID},
				Status:        "ready",
				Capabilities:  c.buildCapabilities(),
				Metrics:       c.buildMetrics(ctx),
			}); err == nil && first {
				log.Printf("[worker] first heartbeat sent worker_id=%s", c.workerID)
				first = false
			}
		}
	}
}

var cancels = map[string]context.CancelFunc{}
var cancelMu sync.Mutex

func activeCancel(id string) context.CancelFunc {
	cancelMu.Lock()
	defer cancelMu.Unlock()
	return cancels[id]
}
func (c *Client) assignment(parent context.Context, a api.WorkerAssignment) {
	_ = c.send(parent, api.WorkerAssignmentAck{
		WorkerMessage: api.WorkerMessage{Version: 1, Type: "worker.assignment_ack", WorkerID: c.workerID, JobID: a.JobID},
		LeaseID:       a.LeaseID,
		Accepted:      true,
	})

	supervisor := c.Supervisor
	if supervisor == nil {
		supervisor = NewLocalSandboxSupervisor(c.Config.StateDir)
	}

	go func() {
		ctx, cancel := context.WithCancel(parent)
		defer cancel()
		cancelMu.Lock()
		cancels[a.AttemptID] = cancel
		cancelMu.Unlock()
		defer func() { cancelMu.Lock(); delete(cancels, a.AttemptID); cancelMu.Unlock() }()

		var seq int64
		streamLog := func(streamName, data string) {
			atomic.AddInt64(&seq, 1)
			_ = c.send(parent, api.WorkerLogChunk{
				WorkerMessage: api.WorkerMessage{
					Version:  1,
					Type:     "worker.log_chunk",
					WorkerID: c.workerID,
					JobID:    a.JobID,
					Seq:      seq,
				},
				AttemptID: a.AttemptID,
				Stream:    streamName,
				Data:      data,
			})
		}

		exitCode, err := supervisor.Execute(ctx, a, streamLog)

		event := "job.succeeded"
		if err != nil || exitCode != 0 {
			event = "job.failed"
		}
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}

		_ = c.send(parent, api.WorkerWorkloadEvent{
			WorkerMessage: api.WorkerMessage{
				Version:  1,
				Type:     "worker.workload_event",
				WorkerID: c.workerID,
				JobID:    a.JobID,
			},
			AttemptID: a.AttemptID,
			Event:     event,
			Data: map[string]interface{}{
				"exit_code": exitCode,
				"error":     errMsg,
			},
		})
	}()
}
