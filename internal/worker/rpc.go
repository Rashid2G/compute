package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/Rashid2G/compute/pkg/api"
	"github.com/google/uuid"
)

type clientTunnel struct {
	id   string
	conn net.Conn

	writeMu   sync.Mutex
	writeCh   chan []byte
	writeDone bool

	closeOnce sync.Once
	closedCh  chan struct{}
}

func newClientTunnel(id string, conn net.Conn) *clientTunnel {
	ct := &clientTunnel{
		id:       id,
		conn:     conn,
		writeCh:  make(chan []byte, 128),
		closedCh: make(chan struct{}),
	}
	go ct.writeLoop()
	return ct
}

func (ct *clientTunnel) enqueueWrite(data []byte) {
	if len(data) == 0 {
		return
	}
	ct.writeMu.Lock()
	if ct.writeDone {
		ct.writeMu.Unlock()
		return
	}
	ch := ct.writeCh
	ct.writeMu.Unlock()

	select {
	case ch <- data:
	case <-ct.closedCh:
	}
}

func (ct *clientTunnel) markRemoteEOF() {
	ct.writeMu.Lock()
	if !ct.writeDone {
		ct.writeDone = true
		close(ct.writeCh)
	}
	ct.writeMu.Unlock()
}

func (ct *clientTunnel) writeLoop() {
	for data := range ct.writeCh {
		if len(data) == 0 {
			continue
		}
		if _, err := ct.conn.Write(data); err != nil {
			ct.close()
			return
		}
	}
	if cw, ok := ct.conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	} else {
		ct.close()
	}
}

func (ct *clientTunnel) close() {
	ct.closeOnce.Do(func() {
		close(ct.closedCh)
		ct.markRemoteEOF()
		_ = ct.conn.Close()
	})
}

func (c *Client) closeAllTunnels() {
	c.tunMu.Lock()
	defer c.tunMu.Unlock()
	for id, ct := range c.tunnels {
		ct.close()
		delete(c.tunnels, id)
	}
}

func (c *Client) handleTunnelOpen(ctx context.Context, open api.WorkerTunnelOpen) {
	if open.TunnelID == "" || open.TargetAddr == "" {
		return
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", open.TargetAddr)
	if err != nil {
		_ = c.send(ctx, api.WorkerTunnelClose{
			WorkerMessage: api.WorkerMessage{
				Version:  1,
				Type:     "worker.tunnel.close",
				ID:       uuid.NewString(),
				WorkerID: c.workerID,
			},
			TunnelID: open.TunnelID,
			Error:    err.Error(),
		})
		return
	}

	ct := newClientTunnel(open.TunnelID, conn)
	c.tunMu.Lock()
	if c.tunnels == nil {
		c.tunnels = make(map[string]*clientTunnel)
	}
	c.tunnels[open.TunnelID] = ct
	c.tunMu.Unlock()

	removeTunnel := func() {
		c.tunMu.Lock()
		delete(c.tunnels, open.TunnelID)
		c.tunMu.Unlock()
	}

	// Send ready acknowledgement frame before streaming bytes.
	if err := c.send(ctx, api.WorkerTunnelData{
		WorkerMessage: api.WorkerMessage{
			Version:  1,
			Type:     "worker.tunnel.data",
			ID:       uuid.NewString(),
			WorkerID: c.workerID,
		},
		TunnelID: open.TunnelID,
	}); err != nil {
		removeTunnel()
		ct.close()
		return
	}

	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			frame := api.WorkerTunnelData{
				WorkerMessage: api.WorkerMessage{
					Version:  1,
					Type:     "worker.tunnel.data",
					ID:       uuid.NewString(),
					WorkerID: c.workerID,
				},
				TunnelID: open.TunnelID,
				Data:     base64.StdEncoding.EncodeToString(buf[:n]),
			}
			if sendErr := c.send(ctx, frame); sendErr != nil {
				removeTunnel()
				ct.close()
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				_ = c.send(ctx, api.WorkerTunnelData{
					WorkerMessage: api.WorkerMessage{
						Version:  1,
						Type:     "worker.tunnel.data",
						ID:       uuid.NewString(),
						WorkerID: c.workerID,
					},
					TunnelID: open.TunnelID,
					EOF:      true,
				})
			} else {
				_ = c.send(ctx, api.WorkerTunnelClose{
					WorkerMessage: api.WorkerMessage{
						Version:  1,
						Type:     "worker.tunnel.close",
						ID:       uuid.NewString(),
						WorkerID: c.workerID,
					},
					TunnelID: open.TunnelID,
					Error:    err.Error(),
				})
			}
			removeTunnel()
			ct.close()
			return
		}
	}
}

func (c *Client) handleTunnelData(frame api.WorkerTunnelData) {
	c.tunMu.Lock()
	ct := c.tunnels[frame.TunnelID]
	c.tunMu.Unlock()
	if ct == nil {
		return
	}
	if frame.Data != "" {
		data, err := base64.StdEncoding.DecodeString(frame.Data)
		if err == nil && len(data) > 0 {
			ct.enqueueWrite(data)
		}
	}
	if frame.EOF {
		ct.markRemoteEOF()
	}
}

func (c *Client) handleTunnelClose(frame api.WorkerTunnelClose) {
	c.tunMu.Lock()
	ct := c.tunnels[frame.TunnelID]
	delete(c.tunnels, frame.TunnelID)
	c.tunMu.Unlock()
	if ct != nil {
		ct.close()
	}
}

func (c *Client) handleRPC(ctx context.Context, req api.WorkerRPCRequest) {
	resObj, err := c.dispatchRPC(ctx, req)
	resp := api.WorkerRPCResponse{
		WorkerMessage: api.WorkerMessage{
			Version:  1,
			Type:     "worker.rpc.resp",
			ID:       uuid.NewString(),
			WorkerID: c.workerID,
		},
		RequestID: req.RequestID,
	}
	if err != nil {
		resp.Error = err.Error()
		var rle *api.ResourceLimitError
		if errors.As(err, &rle) {
			resp.Code = rle.Code
			if b, merr := json.Marshal(rle); merr == nil {
				resp.Result = b
			}
		}
	} else if resObj != nil {
		b, merr := json.Marshal(resObj)
		if merr != nil {
			resp.Error = fmt.Sprintf("marshal rpc result: %v", merr)
		} else {
			resp.Result = b
		}
	}
	_ = c.send(ctx, resp)
}

func (c *Client) dispatchRPC(ctx context.Context, req api.WorkerRPCRequest) (interface{}, error) {
	mgr := c.getVMManager()
	if mgr == nil {
		return nil, fmt.Errorf("worker vm manager is not initialized")
	}

	switch req.Method {
	case "vm.create":
		var payload api.WorkerVMCreateRequest
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid vm.create payload: %w", err)
		}
		createReq := vm.CreateRequest{
			Name:              payload.Name,
			OwnerID:           payload.OwnerID,
			Tier:              payload.Tier,
			VCPUs:             payload.VCPUs,
			MemoryMB:          payload.MemoryMB,
			DiskMB:            payload.DiskMB,
			DiskGB:            payload.DiskGB,
			Description:       payload.Description,
			AutoStart:         payload.AutoStart,
			Image:             payload.Image,
			Template:          payload.Template,
			IPAddress:         payload.IPAddress,
			MACAddress:        payload.MACAddress,
			PublicIP:          payload.PublicIP,
			PublicIPEnabled:   payload.PublicIPEnabled,
			DedicatedIP:       payload.DedicatedIP,
			NetworkIsolation:  payload.NetworkIsolation,
			Isolated:          payload.Isolated,
			NetworkIsolated:   payload.NetworkIsolated,
			DHCP:              payload.DHCP,
			SSHPublicKey:      payload.SSHPublicKey,
			AuthorizedSSHKeys: payload.AuthorizedSSHKeys,
			SSHPassword:       payload.SSHPassword,
			Protected:         payload.Protected,
			StartupScript:     payload.StartupScript,
			Command:           payload.Command,
			BuildCommand:      payload.BuildCommand,
			ExecUser:          payload.ExecUser,
			PoolID:            payload.PoolID,
			// On the worker itself, leave WorkerID local ("nod_host_local").
		}
		created, err := mgr.CreateVM(ctx, createReq)
		if err != nil {
			return nil, err
		}
		if payload.AutoStart {
			if runErr := mgr.RunVM(ctx, created.ID); runErr != nil {
				_ = mgr.DeleteVM(context.Background(), created.ID)
				return nil, runErr
			}
			if updated, gerr := mgr.GetVM(ctx, created.ID); gerr == nil && updated != nil {
				created = updated
			}
		}
		return created, nil

	case "vm.start":
		var payload api.WorkerVMIDRequest
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid vm.start payload: %w", err)
		}
		if err := mgr.RunVM(ctx, payload.VMID); err != nil {
			return nil, err
		}
		return mgr.GetVM(ctx, payload.VMID)

	case "vm.stop":
		var payload api.WorkerVMIDRequest
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid vm.stop payload: %w", err)
		}
		if err := mgr.StopVM(ctx, payload.VMID); err != nil {
			return nil, err
		}
		return mgr.GetVM(ctx, payload.VMID)

	case "vm.delete":
		var payload api.WorkerVMIDRequest
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid vm.delete payload: %w", err)
		}
		if err := mgr.DeleteVM(ctx, payload.VMID); err != nil {
			return nil, err
		}
		return map[string]interface{}{"deleted": true, "vm_id": payload.VMID}, nil

	case "vm.save_state":
		var payload api.WorkerVMIDRequest
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid vm.save_state payload: %w", err)
		}
		if err := mgr.SaveState(ctx, payload.VMID); err != nil {
			return nil, err
		}
		return mgr.GetVM(ctx, payload.VMID)

	case "vm.resume":
		var payload api.WorkerVMIDRequest
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid vm.resume payload: %w", err)
		}
		if err := mgr.ResumeVM(ctx, payload.VMID); err != nil {
			return nil, err
		}
		return mgr.GetVM(ctx, payload.VMID)

	case "vm.status":
		var payload api.WorkerVMIDRequest
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid vm.status payload: %w", err)
		}
		v, err := mgr.GetVM(ctx, payload.VMID)
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nil, fmt.Errorf("vm %s not found", payload.VMID)
		}
		return v, nil

	case "vm.exec":
		var payload api.WorkerVMExecRequest
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid vm.exec payload: %w", err)
		}
		execReq := vm.ExecRequest{
			Command: payload.Command,
			User:    payload.User,
			Timeout: payload.TimeoutSeconds,
		}
		if payload.StdinBase64 != "" {
			raw, err := base64.StdEncoding.DecodeString(payload.StdinBase64)
			if err != nil {
				return nil, fmt.Errorf("invalid stdin_base64: %w", err)
			}
			return mgr.ExecVMInput(ctx, payload.VMID, execReq, bytes.NewReader(raw))
		}
		return mgr.ExecVM(ctx, payload.VMID, execReq)

	case "vm.files":
		var payload api.WorkerVMFilesRequest
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid vm.files payload: %w", err)
		}
		return c.handleVMFilesRPC(ctx, mgr, payload)

	case "vm.processes":
		var payload api.WorkerVMProcessesRequest
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid vm.processes payload: %w", err)
		}
		return c.handleVMProcessesRPC(ctx, mgr, payload)

	case "vm.ssh_sync":
		var payload api.WorkerVMSSHSyncRequest
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid vm.ssh_sync payload: %w", err)
		}
		if err := mgr.SyncExplicitSSHKeysForVM(ctx, payload.VMID, payload.PublicKeys); err != nil {
			return nil, err
		}
		return map[string]interface{}{"status": "synced", "vm_id": payload.VMID}, nil

	case "vm.export_disk":
		var payload api.WorkerVMExportDiskRequest
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid vm.export_disk payload: %w", err)
		}
		return mgr.ExportVMArtifactChunk(ctx, payload.VMID, payload.Kind, payload.Offset, payload.Length)

	case "vm.import_disk":
		var payload api.WorkerVMImportDiskRequest
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid vm.import_disk payload: %w", err)
		}
		return mgr.ImportVMArtifactChunk(ctx, payload)

	default:
		return nil, fmt.Errorf("unsupported rpc method %q", req.Method)
	}
}

func (c *Client) handleVMFilesRPC(ctx context.Context, mgr *vm.Manager, payload api.WorkerVMFilesRequest) (interface{}, error) {
	v, err := mgr.GetVM(ctx, payload.VMID)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, fmt.Errorf("sandbox not found")
	}
	if v.Status != db.StatusRunning {
		return nil, fmt.Errorf("sandbox is not running")
	}

	switch strings.ToLower(strings.TrimSpace(payload.Action)) {
	case "write":
		if payload.Path == "" {
			return nil, fmt.Errorf("missing path")
		}
		var data []byte
		if strings.EqualFold(payload.Format, "base64") {
			decoded, err := base64.StdEncoding.DecodeString(payload.Content)
			if err != nil {
				return nil, fmt.Errorf("invalid base64 content: %w", err)
			}
			data = decoded
		} else {
			data = []byte(payload.Content)
		}
		writeCmd := fmt.Sprintf("sh -c 'mkdir -p \"$(dirname \"$1\")\" && cat > \"$1\"' _ %q", payload.Path)
		res, err := mgr.ExecVMInput(ctx, payload.VMID, vm.ExecRequest{Command: writeCmd, Timeout: 30}, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("write failed: %s", res.Stderr)
		}
		return map[string]interface{}{"status": "success", "path": payload.Path, "size": len(data)}, nil

	case "mkdir":
		if payload.Path == "" {
			return nil, fmt.Errorf("missing path")
		}
		res, err := mgr.ExecVM(ctx, payload.VMID, vm.ExecRequest{Command: fmt.Sprintf("mkdir -p %q", payload.Path), Timeout: 10})
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("mkdir failed: %s", res.Stderr)
		}
		return map[string]interface{}{"status": "success", "path": payload.Path}, nil

	case "remove":
		if payload.Path == "" {
			return nil, fmt.Errorf("missing path")
		}
		res, err := mgr.ExecVM(ctx, payload.VMID, vm.ExecRequest{Command: fmt.Sprintf("rm -rf %q", payload.Path), Timeout: 15})
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("remove failed: %s", res.Stderr)
		}
		return map[string]interface{}{"status": "success", "path": payload.Path}, nil

	case "list":
		path := payload.Path
		if path == "" {
			path = "/workspace"
		}
		pyCmd := fmt.Sprintf(`python3 -c "import os, sys, json; p = sys.argv[1]; res = []; [res.append({'name': e, 'path': os.path.join(p, e), 'is_dir': os.path.isdir(os.path.join(p, e)), 'size': os.path.getsize(os.path.join(p, e)) if not os.path.islink(os.path.join(p, e)) and os.path.exists(os.path.join(p, e)) else 0}) for e in sorted(os.listdir(p))]; print(json.dumps(res))" %q`, path)
		res, err := mgr.ExecVM(ctx, payload.VMID, vm.ExecRequest{Command: pyCmd, Timeout: 15})
		if err == nil && res.ExitCode == 0 {
			var entries []map[string]interface{}
			if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &entries); jsonErr == nil {
				return map[string]interface{}{"path": path, "entries": entries}, nil
			}
		}
		fbRes, fbErr := mgr.ExecVM(ctx, payload.VMID, vm.ExecRequest{Command: fmt.Sprintf("ls -1p %q", path), Timeout: 10})
		if fbErr != nil || fbRes.ExitCode != 0 {
			errMsg := ""
			if res != nil {
				errMsg = res.Stderr
			}
			return nil, fmt.Errorf("failed to list directory: %s", errMsg)
		}
		var entries []map[string]interface{}
		for _, line := range strings.Split(strings.TrimSpace(fbRes.Stdout), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			isDir := strings.HasSuffix(line, "/")
			name := strings.TrimSuffix(line, "/")
			entries = append(entries, map[string]interface{}{
				"name":   name,
				"path":   path + "/" + name,
				"is_dir": isDir,
				"size":   0,
			})
		}
		return map[string]interface{}{"path": path, "entries": entries}, nil

	case "read":
		if payload.Path == "" {
			return nil, fmt.Errorf("missing path")
		}
		res, err := mgr.ExecVM(ctx, payload.VMID, vm.ExecRequest{Command: fmt.Sprintf("base64 %q", payload.Path), Timeout: 20})
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("file read failed: %s", res.Stderr)
		}
		rawB64 := strings.ReplaceAll(strings.ReplaceAll(res.Stdout, "\n", ""), "\r", "")
		decoded, err := base64.StdEncoding.DecodeString(rawB64)
		if err != nil {
			return nil, fmt.Errorf("failed to decode file data: %w", err)
		}
		format := "text"
		contentStr := string(decoded)
		if strings.EqualFold(payload.Format, "base64") || !utf8.Valid(decoded) {
			format = "base64"
			contentStr = rawB64
		}
		return map[string]interface{}{
			"path":    payload.Path,
			"content": contentStr,
			"format":  format,
			"size":    len(decoded),
		}, nil

	default:
		return nil, fmt.Errorf("unsupported vm.files action %q", payload.Action)
	}
}

func (c *Client) handleVMProcessesRPC(ctx context.Context, mgr *vm.Manager, payload api.WorkerVMProcessesRequest) (interface{}, error) {
	v, err := mgr.GetVM(ctx, payload.VMID)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, fmt.Errorf("sandbox not found")
	}
	if v.Status != db.StatusRunning {
		return nil, fmt.Errorf("sandbox is not running")
	}

	switch strings.ToLower(strings.TrimSpace(payload.Action)) {
	case "", "list":
		res, err := mgr.ExecVM(ctx, payload.VMID, vm.ExecRequest{
			Command: "ps -eo pid,ppid,user,%cpu,%mem,stat,comm,args --no-headers",
			Timeout: 10,
		})
		if err != nil {
			return nil, err
		}
		var processes []map[string]interface{}
		for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 7 {
				continue
			}
			pid, _ := strconv.Atoi(fields[0])
			ppid, _ := strconv.Atoi(fields[1])
			cpu, _ := strconv.ParseFloat(fields[3], 64)
			mem, _ := strconv.ParseFloat(fields[4], 64)
			args := strings.Join(fields[7:], " ")
			if args == "" {
				args = fields[6]
			}
			processes = append(processes, map[string]interface{}{
				"pid":     pid,
				"ppid":    ppid,
				"user":    fields[2],
				"cpu":     cpu,
				"memory":  mem,
				"status":  fields[5],
				"command": fields[6],
				"args":    args,
			})
		}
		return map[string]interface{}{"processes": processes}, nil

	case "kill":
		if payload.PID <= 0 {
			return nil, fmt.Errorf("invalid pid")
		}
		sig := payload.Signal
		if sig == "" {
			sig = "SIGTERM"
		}
		res, err := mgr.ExecVM(ctx, payload.VMID, vm.ExecRequest{
			Command: fmt.Sprintf("kill -s %s %d", sig, payload.PID),
			Timeout: 10,
		})
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("kill failed: %s", res.Stderr)
		}
		return map[string]interface{}{"status": "killed", "pid": payload.PID, "signal": sig}, nil

	case "start":
		if strings.TrimSpace(payload.Command) == "" {
			return nil, fmt.Errorf("missing command")
		}
		envPrefix := ""
		for k, val := range payload.Env {
			envPrefix += fmt.Sprintf("%s=%q ", k, val)
		}
		cdPrefix := ""
		if payload.Cwd != "" {
			cdPrefix = fmt.Sprintf("cd %q && ", payload.Cwd)
		}
		bgCmd := fmt.Sprintf("sh -c '%s%s nohup sh -c %q >/tmp/proc.log 2>&1 </dev/null & echo $!'", cdPrefix, envPrefix, payload.Command)
		res, err := mgr.ExecVM(ctx, payload.VMID, vm.ExecRequest{Command: bgCmd, Timeout: 10})
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("failed to start process: %s", res.Stderr)
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(res.Stdout))
		return map[string]interface{}{"status": "started", "pid": pid, "command": payload.Command}, nil

	default:
		return nil, fmt.Errorf("unsupported vm.processes action %q", payload.Action)
	}
}
