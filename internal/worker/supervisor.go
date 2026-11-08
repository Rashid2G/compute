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

	"github.com/Rashid2G/on-demand-vms/internal/vm"
	"github.com/Rashid2G/on-demand-vms/pkg/api"
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

func (s *MicroVMSupervisor) Execute(ctx context.Context, a api.WorkerAssignment, streamLog LogStreamFunc) (int, error) {
	if s.vmMgr == nil {
		return 1, fmt.Errorf("vm manager is nil")
	}

	vmName := fmt.Sprintf("job-%s", a.JobID)
	if len(vmName) > 20 {
		vmName = vmName[:20]
	}

	streamLog("stdout", fmt.Sprintf("[compute-worker] launching isolated Firecracker microVM for job %s", a.JobID))

	// 1. Create VM
	created, err := s.vmMgr.CreateVM(ctx, vm.CreateRequest{
		Name:     vmName,
		OwnerID:  "worker-job",
		Image:    a.Spec.ImageID,
		VCPUs:    a.Spec.VCPUs,
		MemoryMB: a.Spec.MemoryMB,
		DiskMB:   a.Spec.DiskMB,
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
	mkdirCmd := fmt.Sprintf("sudo mkdir -p %s && sudo chown compute:compute %s", cwd, cwd)
	if _, err := s.vmMgr.ExecVM(ctx, created.ID, vm.ExecRequest{Command: mkdirCmd, User: "compute", Timeout: 10}); err != nil {
		return 1, fmt.Errorf("failed to initialize workspace: %w", err)
	}

	// 5. Sync input files if provided
	if a.Spec.Files != "" {
		archive, err := base64.StdEncoding.DecodeString(a.Spec.Files)
		if err != nil {
			return 1, fmt.Errorf("invalid files archive: %w", err)
		}
		tarCmd := fmt.Sprintf("tar -xzf - -C %s", cwd)
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
	rawCmd := strings.Join(cmdParts, " ")
	fullCmd := fmt.Sprintf("cd %s && %s", cwd, rawCmd)

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
	scanPipe := func(r io.Reader, streamName string) {
		defer wg.Done()
		if r == nil {
			return
		}
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			streamLog(streamName, scanner.Text())
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
