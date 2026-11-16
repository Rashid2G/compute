package worker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Rashid2G/compute/internal/vm"
	"github.com/Rashid2G/compute/pkg/api"
)

// LogStreamFunc receives streamed lines from stdout/stderr.
type LogStreamFunc func(streamName, data string)

// WorkloadSupervisor handles the execution and isolation lifecycle of an assigned job.
type WorkloadSupervisor interface {
	Execute(ctx context.Context, a api.WorkerAssignment, streamLog LogStreamFunc) (exitCode int, err error)
}

// MicroVMSupervisor executes job workloads inside isolated Firecracker microVMs.
type MicroVMSupervisor struct {
	vmMgr       *vm.Manager
	bootTimeout time.Duration
}

// NewMicroVMSupervisor creates a supervisor that wraps workloads in ephemeral Firecracker microVMs.
func NewMicroVMSupervisor(vmMgr *vm.Manager, bootTimeout time.Duration) *MicroVMSupervisor {
	if bootTimeout <= 0 {
		bootTimeout = 60 * time.Second
	}
	return &MicroVMSupervisor{
		vmMgr:       vmMgr,
		bootTimeout: bootTimeout,
	}
}

// VMManager returns the underlying worker-local VM manager.
func (s *MicroVMSupervisor) VMManager() *vm.Manager {
	if s == nil {
		return nil
	}
	return s.vmMgr
}

func (s *MicroVMSupervisor) Execute(ctx context.Context, a api.WorkerAssignment, streamLog LogStreamFunc) (int, error) {
	if s.vmMgr == nil {
		return 1, fmt.Errorf("vm manager is nil")
	}

	vmName := fmt.Sprintf("job-%s", a.JobID)
	if len(vmName) > 20 {
		vmName = vmName[:20]
	}

	streamLog("stdout", fmt.Sprintf("[compute-worker] launching isolated Firecracker microVM for job %s", a.JobID))

	// Preserve the submitting job's owner so sandboxes, volumes, and quotas are
	// attributed to the tenant that submitted the job.
	ownerID := strings.TrimSpace(a.OwnerID)
	if ownerID == "" {
		ownerID = "worker-job"
	}

	// 1. Create VM
	created, err := s.vmMgr.CreateVM(ctx, vm.CreateRequest{
		Name:               vmName,
		OwnerID:            ownerID,
		Image:              a.Spec.ImageID,
		VCPUs:              a.Spec.VCPUs,
		MemoryMB:           a.Spec.MemoryMB,
		DiskMB:             a.Spec.DiskMB,
		AuthorizedOwnerIDs: a.Spec.AuthorizedOwnerIDs,
		AllowCrossOwner:    a.Spec.AllowCrossOwner,
	})
	if err != nil {
		return 1, fmt.Errorf("failed to create microVM: %w", err)
	}

	// Always clean up VM on exit
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.vmMgr.DeleteVM(cleanupCtx, created.ID)
	}()

	// 2. Start VM
	if err := s.vmMgr.RunVM(ctx, created.ID); err != nil {
		return 1, fmt.Errorf("failed to start microVM: %w", err)
	}

	// 3. Wait for VM ready
	readyCtx, readyCancel := context.WithTimeout(ctx, s.bootTimeout)
	defer readyCancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	ready := false
	for !ready {
		select {
		case <-readyCtx.Done():
			return 1, fmt.Errorf("microVM boot timed out after %v", s.bootTimeout)
		case <-ticker.C:
			res, err := s.vmMgr.ExecVM(readyCtx, created.ID, vm.ExecRequest{
				Command: "echo ready",
				User:    "compute",
				Timeout: 2,
			})
			if err == nil && res.ExitCode == 0 && strings.TrimSpace(res.Stdout) == "ready" {
				ready = true
			}
		}
	}

	// 4. Setup workspace directory
	cwd := strings.TrimSpace(a.Spec.CWD)
	if cwd == "" {
		cwd = "/workspace"
	}
	mkdirCmd := fmt.Sprintf("sudo mkdir -p %s && sudo chown compute:compute %s", shellQuoteSupervisor(cwd), shellQuoteSupervisor(cwd))
	if _, err := s.vmMgr.ExecVM(ctx, created.ID, vm.ExecRequest{Command: mkdirCmd, User: "compute", Timeout: 10}); err != nil {
		return 1, fmt.Errorf("failed to initialize workspace: %w", err)
	}

	// 5. Sync input files if provided
	if a.Spec.Files != "" {
		archive, err := base64.StdEncoding.DecodeString(a.Spec.Files)
		if err != nil {
			return 1, fmt.Errorf("invalid files archive: %w", err)
		}
		tarCmd := fmt.Sprintf("tar -xzf - -C %s", shellQuoteSupervisor(cwd))
		res, err := s.vmMgr.ExecVMInput(ctx, created.ID, vm.ExecRequest{Command: tarCmd, User: "compute", Timeout: 60}, bytes.NewReader(archive))
		if err != nil || res.ExitCode != 0 {
			return 1, fmt.Errorf("failed to extract input files: %w", err)
		}
	}

	// 6. Build command line
	cmdParts := a.Spec.Command
	if len(cmdParts) == 0 {
		return 0, nil
	}
	rawCmd := shellJoinSupervisor(cmdParts)
	envPrefix := ""
	for k, v := range a.Spec.Env {
		if !isValidEnvKeySupervisor(k) {
			continue
		}
		envPrefix += fmt.Sprintf("export %s=%s; ", k, shellQuoteSupervisor(v))
	}
	fullCmd := fmt.Sprintf("%scd %s && %s", envPrefix, shellQuoteSupervisor(cwd), rawCmd)

	streamLog("stdout", fmt.Sprintf("[compute-worker] sandbox ready; executing: %s", rawCmd))

	// 7. Stream execution live
	var exitCode int
	err = s.vmMgr.ExecVMStream(ctx, created.ID, vm.ExecRequest{
		Command: fullCmd,
		User:    "compute",
		Timeout: a.Spec.TimeoutSeconds,
	}, func(ev vm.ExecStreamEvent) error {
		if ev.Data != "" {
			streamLog(ev.Stream, ev.Data)
		}
		if ev.ExitCode != nil {
			exitCode = *ev.ExitCode
		}
		return nil
	})

	return exitCode, err
}

func isValidEnvKeySupervisor(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		if i == 0 {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_') {
				return false
			}
			continue
		}
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

func shellQuoteSupervisor(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellJoinSupervisor(cmd []string) string {
	if len(cmd) == 0 {
		return "true"
	}
	if len(cmd) == 1 {
		return cmd[0]
	}
	out := make([]string, 0, len(cmd))
	for _, arg := range cmd {
		safe := arg != ""
		for _, r := range arg {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
				r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '=' || r == '+' {
				continue
			}
			safe = false
			break
		}
		if safe {
			out = append(out, arg)
		} else {
			out = append(out, shellQuoteSupervisor(arg))
		}
	}
	return strings.Join(out, " ")
}

// LocalSandboxSupervisor provides sandbox execution for test environments without KVM.
type LocalSandboxSupervisor struct {
	baseDir string
}

func NewLocalSandboxSupervisor(baseDir string) *LocalSandboxSupervisor {
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "compute-sandboxes")
	}
	return &LocalSandboxSupervisor{baseDir: baseDir}
}

func (s *LocalSandboxSupervisor) Execute(ctx context.Context, a api.WorkerAssignment, streamLog LogStreamFunc) (int, error) {
	if len(a.Spec.Command) == 0 {
		return 0, nil
	}

	sandboxDir := filepath.Join(s.baseDir, a.JobID)
	_ = os.MkdirAll(sandboxDir, 0755)
	defer os.RemoveAll(sandboxDir)

	cwd := sandboxDir
	if a.Spec.CWD != "" {
		cwd = filepath.Join(sandboxDir, filepath.Clean(a.Spec.CWD))
		_ = os.MkdirAll(cwd, 0755)
	}

	if a.Spec.Files != "" {
		if err := extractFiles(a.Spec.Files, cwd); err != nil {
			return 1, fmt.Errorf("failed to extract files: %w", err)
		}
	}

	cmd := exec.CommandContext(ctx, a.Spec.Command[0], a.Spec.Command[1:]...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	for k, v := range a.Spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return 1, err
	}

	var wg sync.WaitGroup
	var logMu sync.Mutex
	scanPipe := func(r io.Reader, streamName string) {
		defer wg.Done()
		if r == nil {
			return
		}
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			logMu.Lock()
			streamLog(streamName, line)
			logMu.Unlock()
		}
	}

	if stdoutPipe != nil {
		wg.Add(1)
		go scanPipe(stdoutPipe, "stdout")
	}
	if stderrPipe != nil {
		wg.Add(1)
		go scanPipe(stderrPipe, "stderr")
	}

	wg.Wait()
	err := cmd.Wait()

	exitCode := 0
	if err != nil {
		exitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	return exitCode, err
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
					_, _ = io.Copy(f, tr)
					_ = f.Close()
				}
			}
		}
		if e != nil {
			return e
		}
	}
	return nil
}
