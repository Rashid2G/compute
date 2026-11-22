package vm

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Rashid2G/compute/internal/agent"
	"github.com/Rashid2G/compute/internal/db"
)

// GetVMVsockPath resolves the host Unix Domain Socket path corresponding to the VM's virtio-vsock device.
func (m *Manager) GetVMVsockPath(vm *db.VM) string {
	if vm == nil {
		return ""
	}
	fcID := vm.InternalID
	if fcID == "" {
		fcID = vm.ID
	}

	// 1. When Jailer is enabled, Firecracker runs inside chrootRoot
	if m.cfg.JailerEnabled {
		if ip := m.getJailerProcess(vm.ID); ip != nil && ip.VsockUDS != "" {
			return ip.VsockUDS
		}
		execName := filepath.Base(m.cfg.FirecrackerBinary)
		chrootRoot := filepath.Clean(filepath.Join(m.cfg.JailerBaseDir, execName, sanitizeFirecrackerID(fcID), "root"))
		sockPath := filepath.Join(chrootRoot, "vsock.sock")
		if _, err := os.Stat(sockPath); err == nil {
			return sockPath
		}
		legacyPath := filepath.Join(chrootRoot, "v.sock")
		if _, err := os.Stat(legacyPath); err == nil {
			return legacyPath
		}
		return sockPath
	}

	// 2. When Jailer is disabled, Firecracker runs directly in host vmDir
	vmDir := m.getVMStorageDir(vm)
	sockPath := filepath.Join(vmDir, "vsock.sock")
	if _, err := os.Stat(sockPath); err == nil {
		return sockPath
	}
	legacyPath := filepath.Join(vmDir, "v.sock")
	if _, err := os.Stat(legacyPath); err == nil {
		return legacyPath
	}
	return sockPath
}

// SetAgentClient injects an agent client into Manager cache (primarily for tests).
func (m *Manager) SetAgentClient(vmID string, client *agent.Client) {
	m.agentMu.Lock()
	defer m.agentMu.Unlock()
	m.agentClients[vmID] = client
	m.agentLive[vmID] = client != nil
}

func (m *Manager) hasLiveAgent(vm *db.VM) bool {
	if vm == nil {
		return false
	}
	m.agentMu.RLock()
	defer m.agentMu.RUnlock()
	return m.agentLive[vm.ID] || (vm.InternalID != "" && m.agentLive[vm.InternalID])
}

// tryAgent probes whether compute-agent is reachable over Firecracker vsock with a short timeout.
func (m *Manager) tryAgent(ctx context.Context, vm *db.VM) (*agent.Client, bool) {
	if vm == nil {
		return nil, false
	}
	canonicalKey := vm.ID
	if canonicalKey == "" {
		canonicalKey = vm.InternalID
	}
	if canonicalKey == "" {
		return nil, false
	}

	m.agentMu.RLock()
	client, exists := m.agentClients[canonicalKey]
	live := m.agentLive[canonicalKey]
	m.agentMu.RUnlock()

	sockPath := m.GetVMVsockPath(vm)
	if sockPath == "" {
		return nil, false
	}

	if exists && client != nil && live {
		return client, true
	}

	// Verify socket file exists on host
	if _, err := os.Stat(sockPath); err != nil {
		return nil, false
	}

	client = agent.NewClient(sockPath, agent.DefaultVsockPort)

	// Fast probe with short timeout
	probeCtx, probeCancel := context.WithTimeout(ctx, 250*time.Millisecond)
	err := client.Ping(probeCtx)
	probeCancel()
	if err != nil {
		return nil, false
	}

	m.agentMu.Lock()
	m.agentClients[canonicalKey] = client
	m.agentLive[canonicalKey] = true
	if vm.InternalID != "" {
		m.agentClients[vm.InternalID] = client
		m.agentLive[vm.InternalID] = true
	}
	m.agentMu.Unlock()

	return client, true
}

func (m *Manager) invalidateAgent(vmID string) {
	m.agentMu.Lock()
	defer m.agentMu.Unlock()
	delete(m.agentClients, vmID)
	delete(m.agentLive, vmID)
}

// ReadFile reads file contents from inside the VM, preferring vsock compute-agent and falling back to SSH.
func (m *Manager) ReadFile(ctx context.Context, vmID string, path, format string) (*agent.Response, error) {
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	vm, _ := m.getVMUnsafe(ctx, vmID)
	vmLock.RUnlock()

	if vm != nil {
		if client, ok := m.tryAgent(ctx, vm); ok {
			return client.ReadFile(ctx, path, format)
		}
	}

	// SSH Fallback
	readCmd := fmt.Sprintf("cat %q", path)
	if strings.ToLower(format) == "base64" {
		readCmd = fmt.Sprintf("base64 %q", path)
	}
	res, err := m.ExecVM(ctx, vmID, ExecRequest{
		Command: readCmd,
		Timeout: 30,
	})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("read failed: %s", res.Stderr)
	}

	content := res.Stdout
	if strings.ToLower(format) != "base64" {
		content = strings.TrimRight(content, "\r\n")
	}

	return &agent.Response{
		Status:  agent.StatusOK,
		Content: content,
		Format:  format,
		Size:    int64(len(res.Stdout)),
	}, nil
}

// WriteFile writes data to a file inside the VM, preferring vsock compute-agent and falling back to SSH.
func (m *Manager) WriteFile(ctx context.Context, vmID string, path string, data []byte, mode uint32) error {
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	vm, _ := m.getVMUnsafe(ctx, vmID)
	vmLock.RUnlock()

	if vm != nil {
		if client, ok := m.tryAgent(ctx, vm); ok {
			return client.WriteFile(ctx, path, data, mode)
		}
	}

	// SSH Fallback via ExecVMInput
	perm := "0644"
	if mode > 0 {
		perm = fmt.Sprintf("%#o", mode)
	}
	writeCmd := fmt.Sprintf("sh -c 'mkdir -p \"$(dirname \"$1\")\" && cat > \"$1\" && chmod %s \"$1\"' _ %q", perm, path)
	res, err := m.ExecVMInput(ctx, vmID, ExecRequest{
		Command: writeCmd,
		Timeout: 30,
	}, bytes.NewReader(data))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("write failed: %s", res.Stderr)
	}
	return nil
}

// ListFiles lists directory entries inside the VM, preferring vsock compute-agent and falling back to SSH.
func (m *Manager) ListFiles(ctx context.Context, vmID string, path string) ([]agent.FileInfo, error) {
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	vm, _ := m.getVMUnsafe(ctx, vmID)
	vmLock.RUnlock()

	if vm != nil {
		if client, ok := m.tryAgent(ctx, vm); ok {
			return client.ListFiles(ctx, path)
		}
	}

	// SSH Fallback
	listCmd := fmt.Sprintf("ls -la %q", path)
	res, err := m.ExecVM(ctx, vmID, ExecRequest{
		Command: listCmd,
		Timeout: 10,
	})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("list failed: %s", res.Stderr)
	}

	var results []agent.FileInfo
	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	for _, l := range lines {
		fields := strings.Fields(l)
		if len(fields) < 9 || fields[0] == "total" {
			continue
		}
		name := strings.Join(fields[8:], " ")
		size, _ := strconv.ParseInt(fields[4], 10, 64)
		isDir := strings.HasPrefix(fields[0], "d")
		results = append(results, agent.FileInfo{
			Name:  name,
			Path:  filepath.Join(path, name),
			Size:  size,
			IsDir: isDir,
		})
	}
	return results, nil
}

// Mkdir creates a directory inside the VM, preferring vsock compute-agent and falling back to SSH.
func (m *Manager) Mkdir(ctx context.Context, vmID string, path string, mode uint32) error {
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	vm, _ := m.getVMUnsafe(ctx, vmID)
	vmLock.RUnlock()

	if vm != nil {
		if client, ok := m.tryAgent(ctx, vm); ok {
			return client.Mkdir(ctx, path, mode)
		}
	}

	// SSH Fallback
	mkdirCmd := fmt.Sprintf("mkdir -p %q", path)
	res, err := m.ExecVM(ctx, vmID, ExecRequest{
		Command: mkdirCmd,
		Timeout: 10,
	})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("mkdir failed: %s", res.Stderr)
	}
	return nil
}

// RemoveFile deletes a file or directory inside the VM, preferring vsock compute-agent and falling back to SSH.
func (m *Manager) RemoveFile(ctx context.Context, vmID string, path string, recursive bool) error {
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	vm, _ := m.getVMUnsafe(ctx, vmID)
	vmLock.RUnlock()

	if vm != nil {
		if client, ok := m.tryAgent(ctx, vm); ok {
			return client.RemoveFile(ctx, path, recursive)
		}
	}

	// SSH Fallback
	rmCmd := fmt.Sprintf("rm -f %q", path)
	if recursive {
		rmCmd = fmt.Sprintf("rm -rf %q", path)
	}
	res, err := m.ExecVM(ctx, vmID, ExecRequest{
		Command: rmCmd,
		Timeout: 10,
	})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("remove failed: %s", res.Stderr)
	}
	return nil
}

// StatFile queries file metadata inside the VM, preferring vsock compute-agent and falling back to SSH.
func (m *Manager) StatFile(ctx context.Context, vmID string, path string) (*agent.FileInfo, error) {
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	vm, _ := m.getVMUnsafe(ctx, vmID)
	vmLock.RUnlock()

	if vm != nil {
		if client, ok := m.tryAgent(ctx, vm); ok {
			return client.StatFile(ctx, path)
		}
	}

	// SSH Fallback
	statCmd := fmt.Sprintf("stat -c '%%s %%f' %q", path)
	res, err := m.ExecVM(ctx, vmID, ExecRequest{
		Command: statCmd,
		Timeout: 10,
	})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("stat failed: %s", res.Stderr)
	}
	fields := strings.Fields(strings.TrimSpace(res.Stdout))
	size := int64(0)
	if len(fields) > 0 {
		size, _ = strconv.ParseInt(fields[0], 10, 64)
	}
	return &agent.FileInfo{
		Name: filepath.Base(path),
		Path: path,
		Size: size,
	}, nil
}

// ListProcesses lists processes inside the VM, preferring vsock compute-agent and falling back to SSH.
func (m *Manager) ListProcesses(ctx context.Context, vmID string) ([]agent.Process, error) {
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	vm, _ := m.getVMUnsafe(ctx, vmID)
	vmLock.RUnlock()

	if vm != nil {
		if client, ok := m.tryAgent(ctx, vm); ok {
			return client.ProcList(ctx)
		}
	}

	// SSH Fallback
	psCmd := "ps -eo pid,ppid,user,%cpu,%mem,stat,comm,args --no-headers"
	res, err := m.ExecVM(ctx, vmID, ExecRequest{
		Command: psCmd,
		Timeout: 10,
	})
	if err != nil {
		return nil, err
	}

	var procs []agent.Process
	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	for _, line := range lines {
		fields := strings.Fields(strings.TrimSpace(line))
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
		procs = append(procs, agent.Process{
			PID:     pid,
			PPID:    ppid,
			User:    fields[2],
			CPU:     cpu,
			Memory:  mem,
			Status:  fields[5],
			Command: fields[6],
			Args:    args,
		})
	}
	return procs, nil
}

// StartProcess launches a detached background process inside the VM.
func (m *Manager) StartProcess(ctx context.Context, vmID string, cmd []string, env map[string]string, cwd string) (int, error) {
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	vm, _ := m.getVMUnsafe(ctx, vmID)
	vmLock.RUnlock()

	if vm != nil {
		if client, ok := m.tryAgent(ctx, vm); ok {
			return client.ProcStart(ctx, cmd, env, cwd)
		}
	}

	// SSH Fallback
	envPrefix := ""
	for k, val := range env {
		envPrefix += fmt.Sprintf("%s=%q ", k, val)
	}
	cdPrefix := ""
	if cwd != "" {
		cdPrefix = fmt.Sprintf("cd %q && ", cwd)
	}

	fullCommand := strings.Join(cmd, " ")
	shellCmd := fmt.Sprintf("%s%snohup %s > /tmp/bg-proc.log 2>&1 & echo $!", cdPrefix, envPrefix, fullCommand)
	escaped := strings.ReplaceAll(shellCmd, "'", `'\''`)
	startCmd := fmt.Sprintf("sh -c '%s'", escaped)

	res, err := m.ExecVM(ctx, vmID, ExecRequest{
		Command: startCmd,
		Timeout: 10,
	})
	if err != nil {
		return 0, err
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(res.Stdout))
	return pid, nil
}

// KillProcess sends an OS signal to a process inside the VM.
func (m *Manager) KillProcess(ctx context.Context, vmID string, pid int, signal string) error {
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	vm, _ := m.getVMUnsafe(ctx, vmID)
	vmLock.RUnlock()

	if vm != nil {
		if client, ok := m.tryAgent(ctx, vm); ok {
			return client.ProcKill(ctx, pid, signal)
		}
	}

	// SSH Fallback
	if signal == "" {
		signal = "SIGTERM"
	}
	killCmd := fmt.Sprintf("kill -s %s %d", signal, pid)
	res, err := m.ExecVM(ctx, vmID, ExecRequest{
		Command: killCmd,
		Timeout: 10,
	})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("kill failed: %s", res.Stderr)
	}
	return nil
}

// MountVolume formats (if unformatted) and mounts an attached block device inside the VM.
func (m *Manager) MountVolume(ctx context.Context, vmID string, device, mountPoint, fstype string, readOnly bool) error {
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	vm, _ := m.getVMUnsafe(ctx, vmID)
	vmLock.RUnlock()

	if vm != nil {
		if client, ok := m.tryAgent(ctx, vm); ok {
			return client.VolumeMount(ctx, device, mountPoint, fstype, readOnly)
		}
	}

	// SSH Fallback
	if fstype == "" {
		fstype = "ext4"
	}
	roFlag := ""
	if readOnly {
		roFlag = "-o ro"
	}
	mountCmd := fmt.Sprintf("sudo mkdir -p %q && sudo mount %s -t %s %s %q", mountPoint, roFlag, fstype, device, mountPoint)
	res, err := m.ExecVM(ctx, vmID, ExecRequest{
		Command: mountCmd,
		Timeout: 15,
	})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("mount failed: %s", res.Stderr)
	}
	return nil
}

// UnmountVolume unmounts a mounted path inside the VM.
func (m *Manager) UnmountVolume(ctx context.Context, vmID string, mountPoint string) error {
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	vm, _ := m.getVMUnsafe(ctx, vmID)
	vmLock.RUnlock()

	if vm != nil {
		if client, ok := m.tryAgent(ctx, vm); ok {
			return client.VolumeUnmount(ctx, mountPoint)
		}
	}

	// SSH Fallback
	umountCmd := fmt.Sprintf("sudo umount %q", mountPoint)
	res, err := m.ExecVM(ctx, vmID, ExecRequest{
		Command: umountCmd,
		Timeout: 10,
	})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("unmount failed: %s", res.Stderr)
	}
	return nil
}

// GetGuestStats queries guest CPU, memory, disk, and load averages.
func (m *Manager) GetGuestStats(ctx context.Context, vmID string) (*agent.GuestStats, error) {
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	vm, _ := m.getVMUnsafe(ctx, vmID)
	vmLock.RUnlock()

	if vm != nil {
		if client, ok := m.tryAgent(ctx, vm); ok {
			return client.Stats(ctx)
		}
	}

	// SSH Fallback: parse meminfo & uptime
	res, err := m.ExecVM(ctx, vmID, ExecRequest{
		Command: "cat /proc/meminfo /proc/uptime",
		Timeout: 5,
	})
	if err != nil {
		return nil, err
	}

	stats := &agent.GuestStats{}
	lines := strings.Split(res.Stdout, "\n")
	for _, l := range lines {
		fields := strings.Fields(l)
		if len(fields) >= 2 {
			key := strings.TrimSuffix(fields[0], ":")
			val, _ := strconv.ParseInt(fields[1], 10, 64)
			switch key {
			case "MemTotal":
				stats.MemTotalMB = val / 1024
			case "MemFree":
				stats.MemUsedMB = stats.MemTotalMB - (val / 1024)
			}
		}
	}
	return stats, nil
}

// SyncClock synchronizes the guest system clock after snapshot resume or drift.
func (m *Manager) SyncClock(ctx context.Context, vmID string, unixNano int64) error {
	vmLock := m.getVMLock(vmID)
	vmLock.RLock()
	vm, _ := m.getVMUnsafe(ctx, vmID)
	vmLock.RUnlock()

	if vm != nil {
		if client, ok := m.tryAgent(ctx, vm); ok {
			return client.ClockSync(ctx, unixNano)
		}
	}

	// SSH Fallback: date -s @<seconds>
	sec := unixNano / 1e9
	syncCmd := fmt.Sprintf("sudo date -s @%d", sec)
	_, err := m.ExecVM(ctx, vmID, ExecRequest{
		Command: syncCmd,
		Timeout: 5,
	})
	return err
}
