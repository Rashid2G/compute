// Package jobs provides a local job executor used when no worker node is
// connected to the control plane. Each job runs inside a fresh Firecracker
// sandbox created from the job's requested image.
package jobs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
	"github.com/Rashid2G/on-demand-vms/pkg/api"
)

// DefaultWorkspace is where job commands run inside the sandbox when the job
// does not specify a working directory.
const DefaultWorkspace = "/workspace"

// Runner executes jobs inside local microVMs. It implements
// scheduler.LocalExecutor.
type Runner struct {
	mgr           *vm.Manager
	store         *db.Store
	maxConcurrent int
	bootTimeout   time.Duration

	mu     sync.Mutex
	active map[string]context.CancelFunc
}

// NewRunner creates a local job runner. maxConcurrent <= 0 defaults to 2 and
// bootTimeout <= 0 defaults to 90 seconds.
func NewRunner(mgr *vm.Manager, maxConcurrent int, bootTimeout time.Duration) *Runner {
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	if bootTimeout <= 0 {
		bootTimeout = 90 * time.Second
	}
	return &Runner{
		mgr:           mgr,
		store:         mgr.GetStore(),
		maxConcurrent: maxConcurrent,
		bootTimeout:   bootTimeout,
		active:        make(map[string]context.CancelFunc),
	}
}

// TryStart claims a queued job and runs it in the background. It reports false
// when the runner is at capacity so the scheduler retries on the next tick.
func (r *Runner) TryStart(ctx context.Context, job *db.Job, spec api.JobSpec) bool {
	if r == nil || r.mgr == nil || job == nil {
		return false
	}
	if len(spec.Command) == 0 {
		result, _ := json.Marshal(map[string]interface{}{"error": "job has no command"})
		_ = r.store.UpdateJobState(ctx, job.ID, "failed", string(result))
		return true
	}

	r.mu.Lock()
	if len(r.active) >= r.maxConcurrent {
		r.mu.Unlock()
		return false
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.active[job.ID] = cancel
	r.mu.Unlock()

	if err := r.store.UpdateJobState(runCtx, job.ID, "running", ""); err != nil {
		r.release(job.ID)
		return false
	}
	go r.run(runCtx, job, spec)
	return true
}

// Cancel stops a running job, if this runner owns it.
func (r *Runner) Cancel(jobID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	cancel := r.active[jobID]
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *Runner) release(jobID string) {
	r.mu.Lock()
	delete(r.active, jobID)
	r.mu.Unlock()
}

func (r *Runner) run(ctx context.Context, job *db.Job, spec api.JobSpec) {
	defer r.release(job.ID)

	logs := &jobLogger{store: r.store, jobID: job.ID}
	var (
		vmID     string
		exitCode int
		started  = time.Now()
	)
	defer func() {
		if vmID == "" {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := r.mgr.DeleteVM(cleanupCtx, vmID); err != nil {
			log.Printf("[jobs] failed to clean up sandbox %s for %s: %v", vmID, job.ID, err)
		}
	}()

	logs.append("stdout", fmt.Sprintf("[compute] creating sandbox from image %q", spec.ImageID))
	ownerID := job.OwnerID
	if ownerID == "" {
		ownerID = "jobs"
	}
	created, err := r.mgr.CreateVM(ctx, vm.CreateRequest{
		Name:               jobSandboxName(job.ID),
		OwnerID:            ownerID,
		Image:              spec.ImageID,
		VCPUs:              spec.VCPUs,
		MemoryMB:           spec.MemoryMB,
		DiskMB:             spec.DiskMB,
		AuthorizedOwnerIDs: spec.AuthorizedOwnerIDs,
		AllowCrossOwner:    spec.AllowCrossOwner,
	})
	if err != nil {
		failure := "failed to create sandbox: " + err.Error()
		logs.append("stderr", "[compute] "+failure)
		r.finish(job.ID, "failed", map[string]interface{}{"error": failure})
		return
	}
	vmID = created.ID

	if err := r.mgr.RunVM(ctx, created.ID); err != nil {
		failure := "failed to start sandbox: " + err.Error()
		logs.append("stderr", "[compute] "+failure)
		r.finish(job.ID, "failed", map[string]interface{}{"error": failure, "sandbox_id": vmID})
		return
	}

	if err := r.waitReady(ctx, created.ID); err != nil {
		logs.append("stderr", "[compute] "+err.Error())
		r.finish(job.ID, "failed", map[string]interface{}{"error": err.Error(), "sandbox_id": vmID})
		return
	}
	logs.append("stdout", "[compute] sandbox ready, running command")

	cwd := strings.TrimSpace(spec.CWD)
	if cwd == "" {
		cwd = DefaultWorkspace
	}
	setup := fmt.Sprintf("sudo mkdir -p %s && sudo chown compute:compute %s", shellQuote(cwd), shellQuote(cwd))
	if res, err := r.mgr.ExecVM(ctx, vmID, vm.ExecRequest{Command: setup, User: "compute", Timeout: 30}); err != nil || res.ExitCode != 0 {
		failure := "failed to prepare working directory " + cwd
		if err != nil {
			failure += ": " + err.Error()
		} else if res.Stderr != "" {
			failure += ": " + strings.TrimSpace(res.Stderr)
		}
		logs.append("stderr", "[compute] "+failure)
		r.finish(job.ID, "failed", map[string]interface{}{"error": failure, "sandbox_id": vmID})
		return
	}

	if spec.Files != "" {
		archive, err := base64.StdEncoding.DecodeString(spec.Files)
		if err != nil {
			failure := "invalid file archive: " + err.Error()
			logs.append("stderr", "[compute] "+failure)
			r.finish(job.ID, "failed", map[string]interface{}{"error": failure, "sandbox_id": vmID})
			return
		}
		cmd := fmt.Sprintf("tar -xzf - -C %s", shellQuote(cwd))
		res, err := r.mgr.ExecVMInput(ctx, vmID, vm.ExecRequest{Command: cmd, User: "compute", Timeout: 120}, bytes.NewReader(archive))
		if err != nil || res.ExitCode != 0 {
			failure := "failed to sync job files"
			if err != nil {
				failure += ": " + err.Error()
			} else if res.Stderr != "" {
				failure += ": " + strings.TrimSpace(res.Stderr)
			}
			logs.append("stderr", "[compute] "+failure)
			r.finish(job.ID, "failed", map[string]interface{}{"error": failure, "sandbox_id": vmID})
			return
		}
	}

	command := buildJobCommand(cwd, spec)
	pending := map[string]string{}
	flush := func(stream string) {
		if rest := pending[stream]; rest != "" {
			pending[stream] = ""
			logs.append(stream, strings.TrimSuffix(rest, "\r"))
		}
	}

	err = r.mgr.ExecVMStream(ctx, vmID, vm.ExecRequest{
		Command: command,
		User:    "compute",
		Timeout: spec.TimeoutSeconds,
	}, func(ev vm.ExecStreamEvent) error {
		if ev.Data != "" {
			pending[ev.Stream] += ev.Data
			for {
				idx := strings.IndexByte(pending[ev.Stream], '\n')
				if idx < 0 {
					break
				}
				line := pending[ev.Stream][:idx]
				pending[ev.Stream] = pending[ev.Stream][idx+1:]
				logs.append(ev.Stream, strings.TrimSuffix(line, "\r"))
			}
		}
		if ev.Error != "" {
			logs.append("stderr", "[compute] "+ev.Error)
		}
		if ev.ExitCode != nil {
			exitCode = *ev.ExitCode
		}
		return nil
	})
	flush("stdout")
	flush("stderr")

	result := map[string]interface{}{
		"exit_code":   exitCode,
		"duration_ms": time.Since(started).Milliseconds(),
		"sandbox_id":  vmID,
		"image":       spec.ImageID,
	}
	state := "succeeded"
	if err != nil {
		state = "failed"
		result["error"] = err.Error()
	} else if exitCode != 0 {
		state = "failed"
	}
	logs.append("stdout", fmt.Sprintf("[compute] job finished with exit code %d", exitCode))
	r.finish(job.ID, state, result)
}

// finish writes the final state unless the job was explicitly canceled.
func (r *Runner) finish(jobID, state string, result map[string]interface{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if current, err := r.store.GetJob(ctx, jobID); err == nil && current != nil && current.State == "canceled" {
		return
	}
	payload, _ := json.Marshal(result)
	_ = r.store.UpdateJobState(ctx, jobID, state, string(payload))
}

func (r *Runner) waitReady(ctx context.Context, vmID string) error {
	deadline := time.Now().Add(r.bootTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("job canceled while waiting for sandbox: %w", err)
		}
		res, err := r.mgr.ExecVM(ctx, vmID, vm.ExecRequest{Command: "true", User: "compute", Timeout: 5})
		if err == nil && res.ExitCode == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sandbox did not become reachable within %s", r.bootTimeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("job canceled while waiting for sandbox: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

type jobLogger struct {
	store *db.Store
	jobID string
	seq   int64
}

func (l *jobLogger) append(stream, data string) {
	_ = l.store.AppendJobLog(context.Background(), &db.JobLog{
		JobID:  l.jobID,
		Stream: stream,
		Seq:    atomic.AddInt64(&l.seq, 1),
		Data:   data,
	})
}

func buildJobCommand(cwd string, spec api.JobSpec) string {
	parts := make([]string, 0, len(spec.Env)+1)
	for k, v := range spec.Env {
		parts = append(parts, fmt.Sprintf("export %s=%s;", k, shellQuote(v)))
	}
	parts = append(parts, fmt.Sprintf("cd %s && %s", shellQuote(cwd), strings.Join(spec.Command, " ")))
	return strings.Join(parts, " ")
}

func jobSandboxName(jobID string) string {
	clean := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, jobID)
	return "job-" + clean
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
