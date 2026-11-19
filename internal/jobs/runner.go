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

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/Rashid2G/compute/pkg/api"
)

// DefaultWorkspace is where job commands run inside the sandbox when the job
// does not specify a working directory.
const (
	DefaultWorkspace = "/workspace"
	DefaultImageID   = "ubuntu-24"
)

// Runner executes jobs inside local microVMs. It implements
// scheduler.LocalExecutor.
type Runner struct {
	mgr           *vm.Manager
	store         *db.Store
	maxConcurrent int
	bootTimeout   time.Duration

	mu       sync.Mutex
	active   map[string]context.CancelFunc
	onUpdate func()
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

// SetOnUpdate registers a callback invoked when a job finishes or state changes.
func (r *Runner) SetOnUpdate(fn func()) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onUpdate = fn
}

// TryStart claims a queued job and runs it in the background. It reports false
// when the runner is at capacity so the scheduler retries on the next tick.
func (r *Runner) TryStart(ctx context.Context, job *db.Job, spec api.JobSpec) bool {
	if r == nil || r.mgr == nil || job == nil {
		return false
	}
	if len(spec.Command) == 0 {
		if job.Type == "image-build" || job.Type == "image.build" || spec.TargetTemplate != "" {
			spec.Command = buildImageConfigCommand(spec.Packages, spec.Script)
		} else {
			result, _ := json.Marshal(map[string]interface{}{"error": "job has no command"})
			_ = r.store.UpdateJobState(ctx, job.ID, "failed", string(result))
			return true
		}
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

	isImageBuild := job.Type == "image-build" || job.Type == "image.build" || spec.TargetTemplate != ""
	targetTemplate := strings.TrimSpace(spec.TargetTemplate)
	if targetTemplate == "" && isImageBuild {
		targetTemplate = strings.TrimSpace(job.RepoName)
	}

	if isImageBuild {
		if spec.VCPUs <= 0 {
			spec.VCPUs = 2
		}
		if spec.MemoryMB <= 0 {
			spec.MemoryMB = 2048
		}
		if spec.DiskMB <= 0 {
			spec.DiskMB = 10240
		}
		if len(spec.Command) == 0 {
			spec.Command = buildImageConfigCommand(spec.Packages, spec.Script)
		}
	}

	spec.ImageID = normalizeRunnerImageID(spec.ImageID)
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
	job.VMID = created.ID
	if job.WorkerID == "" {
		job.WorkerID = "local"
	}
	_ = r.store.UpdateJobExecution(ctx, job.ID, vmID, job.WorkerID)

	if err := r.mgr.RunVM(ctx, created.ID); err != nil {
		failure := "failed to start sandbox: " + err.Error()
		logs.append("stderr", "[compute] "+failure)
		r.finish(job.ID, "failed", map[string]interface{}{"error": failure, "sandbox_id": vmID, "vm_id": vmID, "worker_id": job.WorkerID})
		return
	}

	if err := r.waitReady(ctx, created.ID); err != nil {
		logs.append("stderr", "[compute] "+err.Error())
		r.finish(job.ID, "failed", map[string]interface{}{"error": err.Error(), "sandbox_id": vmID, "vm_id": vmID, "worker_id": job.WorkerID})
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
		r.finish(job.ID, "failed", map[string]interface{}{"error": failure, "sandbox_id": vmID, "vm_id": vmID, "worker_id": job.WorkerID})
		return
	}

	if spec.Files != "" {
		archive, err := base64.StdEncoding.DecodeString(spec.Files)
		if err != nil {
			failure := "invalid file archive: " + err.Error()
			logs.append("stderr", "[compute] "+failure)
			r.finish(job.ID, "failed", map[string]interface{}{"error": failure, "sandbox_id": vmID, "vm_id": vmID, "worker_id": job.WorkerID})
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
			r.finish(job.ID, "failed", map[string]interface{}{"error": failure, "sandbox_id": vmID, "vm_id": vmID, "worker_id": job.WorkerID})
			return
		}
	}

	// Copy spec env so we can inject the stored GitHub token at execution time
	// without ever persisting raw tokens inside jobs.spec_json.
	execSpec := spec
	execSpec.Env = make(map[string]string, len(spec.Env)+1)
	for k, v := range spec.Env {
		execSpec.Env[k] = v
	}
	if job.RepoID != "" && execSpec.Env["GITHUB_TOKEN"] == "" {
		if conn, err := r.store.GetGitHubConnection(ctx, job.RepoID); err == nil && conn != nil && conn.GitHubToken != "" {
			authorized := spec.AllowCrossOwner || conn.OwnerID == job.OwnerID || (conn.OwnerID == "default" && job.OwnerID == "default")
			if !authorized {
				for _, oid := range spec.AuthorizedOwnerIDs {
					if oid != "" && oid == conn.OwnerID {
						authorized = true
						break
					}
				}
			}
			if authorized {
				execSpec.Env["GITHUB_TOKEN"] = conn.GitHubToken
			}
		}
	}

	command := buildJobCommand(cwd, execSpec)
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
		"vm_id":       vmID,
		"worker_id":   job.WorkerID,
		"image":       spec.ImageID,
	}
	if job.Source != "" {
		result["source"] = job.Source
	}
	if job.RepoURL != "" {
		result["repo_url"] = job.RepoURL
	}
	if job.RepoName != "" {
		result["repo_name"] = job.RepoName
	}
	if job.Branch != "" {
		result["branch"] = job.Branch
	}
	if job.CommitSHA != "" {
		result["commit_sha"] = job.CommitSHA
	}
	state := "succeeded"
	if err != nil {
		state = "failed"
		result["error"] = err.Error()
	} else if exitCode != 0 {
		state = "failed"
		result["error"] = fmt.Sprintf("configuration command failed with exit code %d", exitCode)
	}

	if state == "succeeded" && isImageBuild && targetTemplate != "" {
		logs.append("stdout", fmt.Sprintf("[compute] image configuration complete. Creating template %q...", targetTemplate))
		// If template or custom image already exists for this owner (e.g. on rebuild), clean them up first so CreateTemplate replaces it cleanly
		if oldTpl, _ := r.mgr.GetStore().GetTemplateByOwner(ctx, job.OwnerID, targetTemplate); oldTpl != nil {
			_ = r.mgr.DeleteTemplate(ctx, oldTpl.ID)
		}
		if oldImg, _ := r.mgr.GetStore().GetImageByOwner(ctx, job.OwnerID, targetTemplate); oldImg != nil {
			_ = r.mgr.DeleteImage(ctx, oldImg.ID)
		}
		tpl, tplErr := r.mgr.CreateTemplate(ctx, vm.CreateTemplateRequest{
			VMID:        vmID,
			Name:        targetTemplate,
			Description: spec.TargetDescription,
			OwnerID:     job.OwnerID,
		})
		if tplErr != nil {
			state = "failed"
			result["error"] = "failed to create template from built image: " + tplErr.Error()
			logs.append("stderr", "[compute] "+result["error"].(string))
		} else {
			result["template_id"] = tpl.ID
			result["template_name"] = tpl.Name
			logs.append("stdout", fmt.Sprintf("[compute] template %s (%s) created successfully and ready to use!", tpl.Name, tpl.ID))

			// Also register custom image record so it is tracked across image endpoints
			customImg := &db.CustomImage{
				ID:          fmt.Sprintf("img-%s", tpl.ID),
				OwnerID:     job.OwnerID,
				Name:        tpl.Name,
				Description: tpl.Description,
				BaseImage:   spec.ImageID,
				Packages:    spec.Packages,
				RootfsPath:  tpl.DiskPath,
				KernelPath:  tpl.KernelPath,
				SizeMB:      tpl.DiskMB,
				Status:      db.ImageStatusReady,
				CreatedAt:   time.Now().UTC(),
				UpdatedAt:   time.Now().UTC(),
			}
			_ = r.store.SaveImage(ctx, customImg)
		}
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
	r.mu.Lock()
	onUpdate := r.onUpdate
	r.mu.Unlock()
	if onUpdate != nil {
		go onUpdate()
	}
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
		if !isValidEnvKey(k) {
			continue
		}
		parts = append(parts, fmt.Sprintf("export %s=%s;", k, shellQuote(v)))
	}
	parts = append(parts, fmt.Sprintf("cd %s && %s", shellQuote(cwd), shellJoinCommand(spec.Command)))
	return strings.Join(parts, " ")
}

func isValidEnvKey(k string) bool {
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

func normalizeRunnerImageID(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "default", "ubuntu", "ubuntu-24", "ubuntu-24.04", "ubuntu-2404", "img-base-ubuntu-24":
		return DefaultImageID
	default:
		return strings.TrimSpace(raw)
	}
}

func shellJoinCommand(cmd []string) string {
	if len(cmd) == 0 {
		return "true"
	}
	if len(cmd) == 1 {
		return cmd[0]
	}
	quoted := make([]string, 0, len(cmd))
	for _, arg := range cmd {
		if isSafeShellWord(arg) {
			quoted = append(quoted, arg)
		} else {
			quoted = append(quoted, shellQuote(arg))
		}
	}
	return strings.Join(quoted, " ")
}

func isSafeShellWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '=' || r == '+' {
			continue
		}
		return false
	}
	return true
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

func buildImageConfigCommand(packages []string, script string) []string {
	var sb strings.Builder
	sb.WriteString("set -euo pipefail\n")
	sb.WriteString("export DEBIAN_FRONTEND=noninteractive\n")

	cleanPackages := make([]string, 0, len(packages))
	for _, p := range packages {
		for _, part := range strings.Split(p, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				cleanPackages = append(cleanPackages, part)
			}
		}
	}

	if len(cleanPackages) > 0 {
		sb.WriteString("echo \"==> [1/3] Updating package repositories...\"\n")
		sb.WriteString("sudo -E apt-get update -y\n")
		sb.WriteString(fmt.Sprintf("echo \"==> [2/3] Installing pre-configured packages: %s...\"\n", strings.Join(cleanPackages, " ")))
		pkgArgs := make([]string, len(cleanPackages))
		for i, p := range cleanPackages {
			pkgArgs[i] = shellQuote(p)
		}
		sb.WriteString(fmt.Sprintf("sudo -E apt-get install -y --no-install-recommends %s\n", strings.Join(pkgArgs, " ")))
	} else {
		sb.WriteString("echo \"==> [1/2] Updating package repositories...\"\n")
		sb.WriteString("sudo -E apt-get update -y\n")
	}

	trimmedScript := strings.TrimSpace(script)
	if trimmedScript != "" {
		sb.WriteString("echo \"==> [3/3] Running custom configuration commands...\"\n")
		sb.WriteString(trimmedScript)
		sb.WriteString("\n")
	}

	sb.WriteString("echo \"==> Flushing filesystem buffers...\"\n")
	sb.WriteString("sync\n")
	sb.WriteString("echo \"==> Image configuration completed successfully.\"\n")

	return []string{"bash", "-c", sb.String()}
}
