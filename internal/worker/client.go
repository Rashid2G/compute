package worker

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/Rashid2G/on-demand-vms/pkg/api"
	"github.com/coder/websocket"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Client struct {
	Config   Config
	mu       sync.Mutex
	conn     *websocket.Conn
	workerID string
	seq      int64
}

func (c *Client) send(ctx context.Context, v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return c.conn.Write(ctx, websocket.MessageText, b)
}
func (c *Client) Run(ctx context.Context) error {
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
	conn, _, e := websocket.Dial(ctx, u.String(), nil)
	if e != nil {
		return e
	}
	c.conn = conn
	defer conn.Close(websocket.StatusNormalClosure, "")
	hello := api.WorkerHello{WorkerMessage: api.WorkerMessage{Version: 1, Type: "worker.hello", ID: "hello"}, Token: c.Config.Token, Name: c.Config.Name, Architecture: runtimeArch(), Capabilities: DetectCapabilities()}
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
	go c.heartbeat(ctx)
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
			json.Unmarshal(b, &a)
			c.assignment(ctx, a)
		case "worker.cancel":
			var x api.WorkerCancel
			json.Unmarshal(b, &x)
			if cancel := activeCancel(x.AttemptID); cancel != nil {
				cancel()
			}
		}
	}
}

func runtimeArch() string { return runtime.GOARCH }
func (c *Client) heartbeat(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = c.send(ctx, api.WorkerHeartbeat{WorkerMessage: api.WorkerMessage{Version: 1, Type: "worker.heartbeat", WorkerID: c.workerID}, Status: "ready", Capabilities: DetectCapabilities()})
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
	_ = c.send(parent, api.WorkerAssignmentAck{WorkerMessage: api.WorkerMessage{Version: 1, Type: "worker.assignment_ack", WorkerID: c.workerID, JobID: a.JobID}, LeaseID: a.LeaseID, Accepted: true})
	go func() {
		ctx, cancel := context.WithCancel(parent)
		defer cancel()
		cancelMu.Lock()
		cancels[a.AttemptID] = cancel
		cancelMu.Unlock()
		defer func() { cancelMu.Lock(); delete(cancels, a.AttemptID); cancelMu.Unlock() }()
		if len(a.Spec.Command) == 0 {
			return
		}
		cmd := exec.CommandContext(ctx, a.Spec.Command[0], a.Spec.Command[1:]...)
		cmd.Dir = a.Spec.CWD
		if len(a.Spec.Env) > 0 {
			cmd.Env = os.Environ()
			for k, v := range a.Spec.Env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
		}
		if a.Spec.Files != "" {
			if err := extractFiles(a.Spec.Files, a.Spec.CWD); err != nil {
				_ = c.send(parent, api.WorkerWorkloadEvent{WorkerMessage: api.WorkerMessage{Version: 1, Type: "worker.workload_event", WorkerID: c.workerID, JobID: a.JobID}, AttemptID: a.AttemptID, Event: "job.failed", Data: map[string]interface{}{"exit_code": 1, "error": err.Error()}})
				return
			}
		}

		stdoutPipe, _ := cmd.StdoutPipe()
		stderrPipe, _ := cmd.StderrPipe()

		if err := cmd.Start(); err != nil {
			_ = c.send(parent, api.WorkerWorkloadEvent{
				WorkerMessage: api.WorkerMessage{Version: 1, Type: "worker.workload_event", WorkerID: c.workerID, JobID: a.JobID},
				AttemptID:     a.AttemptID,
				Event:         "job.failed",
				Data:          map[string]interface{}{"exit_code": 1, "error": err.Error()},
			})
			return
		}

		var seq int64
		var wg sync.WaitGroup
		streamOutput := func(r io.Reader, streamName string) {
			defer wg.Done()
			if r == nil {
				return
			}
			scanner := bufio.NewScanner(r)
			for scanner.Scan() {
				line := scanner.Text()
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
					Data:      line,
				})
			}
		}

		if stdoutPipe != nil {
			wg.Add(1)
			go streamOutput(stdoutPipe, "stdout")
		}
		if stderrPipe != nil {
			wg.Add(1)
			go streamOutput(stderrPipe, "stderr")
		}

		wg.Wait()
		err := cmd.Wait()

		event := "job.succeeded"
		code := 0
		if err != nil {
			event = "job.failed"
			code = 1
			if exitErr, ok := err.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			}
		}
		_ = c.send(parent, api.WorkerWorkloadEvent{
			WorkerMessage: api.WorkerMessage{Version: 1, Type: "worker.workload_event", WorkerID: c.workerID, JobID: a.JobID},
			AttemptID:     a.AttemptID,
			Event:         event,
			Data:          map[string]interface{}{"exit_code": code, "error": fmt.Sprint(err)},
		})
	}()
}

func extractFiles(encoded, cwd string) error {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	if cwd == "" {
		cwd = "."
	}
	if err = os.MkdirAll(cwd, 0755); err != nil {
		return err
	}
	gz, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	root, err := filepath.Abs(cwd)
	if err != nil {
		return err
	}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		dest := filepath.Join(root, filepath.Clean(h.Name))
		rel, e := filepath.Rel(root, dest)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("archive path escapes working directory")
		}
		switch h.Typeflag {
		case tar.TypeDir:
			e = os.MkdirAll(dest, os.FileMode(h.Mode))
		case tar.TypeReg, tar.TypeRegA:
			if e = os.MkdirAll(filepath.Dir(dest), 0755); e == nil {
				var f *os.File
				f, e = os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(h.Mode))
				if e == nil {
					_, e = io.Copy(f, tr)
					ce := f.Close()
					if e == nil {
						e = ce
					}
				}
			}
		default:
			continue
		}
		if e != nil {
			return e
		}
	}
	return nil
}
