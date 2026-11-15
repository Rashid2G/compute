package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
	"github.com/Rashid2G/on-demand-vms/pkg/api"
	"github.com/google/uuid"
)

type ciRepoRequest struct {
	Name           string `json:"name"`
	RepoURL        string `json:"repo_url"`
	DefaultBranch  string `json:"default_branch"`
	GitHubToken    string `json:"github_token"`
	WebhookSecret  string `json:"webhook_secret"`
	DefaultCommand string `json:"default_command"`
	VCPUs          int    `json:"vcpus"`
	MemoryMB       int    `json:"memory_mb"`
	TimeoutSec     int    `json:"timeout_sec"`
	OwnerID        string `json:"owner_id"`
}

type ciRepoUpdateRequest struct {
	Name           *string `json:"name"`
	RepoURL        *string `json:"repo_url"`
	DefaultBranch  *string `json:"default_branch"`
	GitHubToken    *string `json:"github_token"`
	WebhookSecret  *string `json:"webhook_secret"`
	DefaultCommand *string `json:"default_command"`
	VCPUs          *int    `json:"vcpus"`
	MemoryMB       *int    `json:"memory_mb"`
	TimeoutSec     *int    `json:"timeout_sec"`
	Status         *string `json:"status"`
}

type ciTriggerRequest struct {
	Branch     string            `json:"branch"`
	CommitSHA  string            `json:"commit_sha"`
	Command    string            `json:"command"`
	Source     string            `json:"source"`
	ImageID    string            `json:"image_id"`
	PoolID     string            `json:"pool_id"`
	VCPUs      int               `json:"vcpus"`
	MemoryMB   int               `json:"memory_mb"`
	TimeoutSec int               `json:"timeout_sec"`
	Env        map[string]string `json:"env"`
}

func randomWebhookSecret() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "whsec_" + hex.EncodeToString(b)
}

func deriveRepoNameAndURL(name, repoURL string) (string, string) {
	name = strings.TrimSpace(name)
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" && name != "" {
		clean := strings.TrimPrefix(name, "/")
		clean = strings.TrimSuffix(clean, ".git")
		repoURL = "https://github.com/" + clean
	}
	if name == "" && repoURL != "" {
		clean := strings.TrimSuffix(repoURL, "/")
		clean = strings.TrimSuffix(clean, ".git")
		if idx := strings.Index(clean, "github.com/"); idx >= 0 {
			name = clean[idx+len("github.com/"):]
		} else if idx := strings.Index(clean, "github.com:"); idx >= 0 {
			name = clean[idx+len("github.com:"):]
		} else {
			parts := strings.Split(clean, "/")
			if len(parts) >= 2 {
				name = parts[len(parts)-2] + "/" + parts[len(parts)-1]
			} else {
				name = clean
			}
		}
	}
	return name, repoURL
}

func normalizeCIImageID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.EqualFold(trimmed, "ubuntu") || strings.EqualFold(trimmed, "default") || strings.EqualFold(trimmed, "img-base-ubuntu-24") {
		return config.DefaultImageName
	}
	if canonical, err := config.ValidateImage(trimmed); err == nil && canonical != "" {
		return canonical
	}
	return trimmed
}

func (s *Server) listAccessibleCIRepos(ctx context.Context, r *http.Request) ([]*db.GitHubConnection, error) {
	st := s.vmMgr.GetStore()
	_, isAdmin := s.currentOwnerID(r)
	if isAdmin {
		return st.ListGitHubConnections(ctx, "")
	}
	seen := make(map[string]bool)
	var out []*db.GitHubConnection
	for _, oid := range s.ownerIDsFor(r) {
		if oid == "" {
			continue
		}
		items, err := st.ListGitHubConnections(ctx, oid)
		if err != nil {
			return nil, err
		}
		for _, c := range items {
			if c == nil || seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			out = append(out, c)
		}
	}
	if out == nil {
		out = []*db.GitHubConnection{}
	}
	return out, nil
}

func (s *Server) handleCIRepos(w http.ResponseWriter, r *http.Request) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.callerIdentity(r); !ok {
			httpError(w, "authentication required", http.StatusUnauthorized)
			return
		}
		repos, err := s.listAccessibleCIRepos(ctx, r)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if repos == nil {
			repos = []*db.GitHubConnection{}
		}
		json.NewEncoder(w).Encode(repos)

	case http.MethodPost:
		ownerID, isAdmin, ok := s.callerIdentity(r)
		if !ok {
			httpError(w, "authentication required", http.StatusUnauthorized)
			return
		}
		var req ciRepoRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		name, repoURL := deriveRepoNameAndURL(req.Name, req.RepoURL)
		if name == "" && repoURL == "" {
			httpError(w, "repo_url or name is required", http.StatusBadRequest)
			return
		}
		if isAdmin && strings.TrimSpace(req.OwnerID) != "" {
			ownerID = strings.TrimSpace(req.OwnerID)
		}
		webhookSecret := strings.TrimSpace(req.WebhookSecret)
		if webhookSecret == "" {
			webhookSecret = randomWebhookSecret()
		}
		branch := strings.TrimSpace(req.DefaultBranch)
		if branch == "" {
			branch = "main"
		}
		cmd := strings.TrimSpace(req.DefaultCommand)
		if cmd == "" {
			cmd = "echo 'CI job completed'"
		}

		conn := &db.GitHubConnection{
			ID:             "gh_" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""),
			OwnerID:        ownerID,
			Name:           name,
			RepoURL:        repoURL,
			DefaultBranch:  branch,
			GitHubToken:    strings.TrimSpace(req.GitHubToken),
			WebhookSecret:  webhookSecret,
			DefaultCommand: cmd,
			VCPUs:          req.VCPUs,
			MemoryMB:       req.MemoryMB,
			TimeoutSec:     req.TimeoutSec,
			Status:         "connected",
			CreatedAt:      time.Now().UTC(),
		}
		if err := st.SaveGitHubConnection(ctx, conn); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.recordEvent("ci.repo.connected", getActor(r), conn.Name, "delivered", 15, map[string]interface{}{
			"id":       conn.ID,
			"repo_url": conn.RepoURL,
			"branch":   conn.DefaultBranch,
		})
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(struct {
			*db.GitHubConnection
			WebhookSecret string `json:"webhook_secret,omitempty"`
		}{
			GitHubConnection: conn,
			WebhookSecret:    webhookSecret,
		})

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleCIRepoActions(w http.ResponseWriter, r *http.Request) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	if _, _, ok := s.callerIdentity(r); !ok {
		httpError(w, "authentication required", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/ci/repos/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		httpError(w, "repo id required", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(parts[0])
	action := ""
	if len(parts) > 1 {
		action = strings.ToLower(strings.TrimSpace(parts[1]))
	}

	conn, err := st.GetGitHubConnection(ctx, id)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if conn == nil || !s.canAccessOwner(r, conn.OwnerID) {
		httpError(w, "connected repository not found", http.StatusNotFound)
		return
	}

	if action == "trigger" {
		if r.Method != http.MethodPost {
			httpError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req ciTriggerRequest
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
				httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
				return
			}
		}

		imageID := normalizeCIImageID(req.ImageID)
		vcpus := req.VCPUs
		if vcpus <= 0 {
			vcpus = conn.VCPUs
		}
		memMB := req.MemoryMB
		if memMB <= 0 {
			memMB = conn.MemoryMB
		}
		jobVCPUs, jobMemMB, jobDiskMB := s.estimateRequestResources(ctx, vm.CreateRequest{
			Image:    imageID,
			VCPUs:    vcpus,
			MemoryMB: memMB,
		})
		if err := s.enforceCreateQuota(ctx, r, conn.OwnerID, jobVCPUs, jobMemMB, jobDiskMB); err != nil {
			httpError(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		if err := s.enforceRunQuota(ctx, r, &db.VM{OwnerID: conn.OwnerID, VCPUs: jobVCPUs, MemoryMB: jobMemMB, DiskMB: jobDiskMB}); err != nil {
			httpError(w, err.Error(), http.StatusTooManyRequests)
			return
		}

		_, isAdmin := s.currentOwnerID(r)
		var authOwners []string
		if !isAdmin {
			authOwners = s.ownerIDsFor(r)
		}
		source := strings.TrimSpace(req.Source)
		if source == "" {
			source = "manual"
		}
		job, err := s.launchRepoCIJob(ctx, conn, req, isAdmin, authOwners, source)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("ci.repo.triggered", getActor(r), conn.Name, "delivered", 15, map[string]interface{}{
			"repo_id": conn.ID,
			"job_id":  job.ID,
			"branch":  job.Branch,
			"source":  job.Source,
		})
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(job)
		return
	}

	if action != "" {
		httpError(w, "unknown action", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(conn)

	case http.MethodPatch, http.MethodPut:
		var upd ciRepoUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&upd); err != nil {
			httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if upd.Name != nil {
			conn.Name = strings.TrimSpace(*upd.Name)
		}
		if upd.RepoURL != nil {
			conn.RepoURL = strings.TrimSpace(*upd.RepoURL)
		}
		if upd.DefaultBranch != nil && strings.TrimSpace(*upd.DefaultBranch) != "" {
			conn.DefaultBranch = strings.TrimSpace(*upd.DefaultBranch)
		}
		if upd.GitHubToken != nil && strings.TrimSpace(*upd.GitHubToken) != "" {
			conn.GitHubToken = strings.TrimSpace(*upd.GitHubToken)
		}
		if upd.WebhookSecret != nil && strings.TrimSpace(*upd.WebhookSecret) != "" {
			conn.WebhookSecret = strings.TrimSpace(*upd.WebhookSecret)
		}
		if upd.DefaultCommand != nil {
			conn.DefaultCommand = strings.TrimSpace(*upd.DefaultCommand)
		}
		if upd.VCPUs != nil && *upd.VCPUs > 0 {
			conn.VCPUs = *upd.VCPUs
		}
		if upd.MemoryMB != nil && *upd.MemoryMB > 0 {
			conn.MemoryMB = *upd.MemoryMB
		}
		if upd.TimeoutSec != nil && *upd.TimeoutSec > 0 {
			conn.TimeoutSec = *upd.TimeoutSec
		}
		if upd.Status != nil && strings.TrimSpace(*upd.Status) != "" {
			conn.Status = strings.TrimSpace(*upd.Status)
		}
		if err := st.SaveGitHubConnection(ctx, conn); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("ci.repo.updated", getActor(r), conn.Name, "delivered", 15, map[string]interface{}{"id": conn.ID})
		json.NewEncoder(w).Encode(conn)

	case http.MethodDelete:
		if err := st.DeleteGitHubConnection(ctx, conn.ID); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.recordEvent("ci.repo.deleted", getActor(r), conn.Name, "delivered", 15, map[string]interface{}{"id": conn.ID})
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": conn.ID})

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) launchRepoCIJob(
	ctx context.Context,
	conn *db.GitHubConnection,
	req ciTriggerRequest,
	allowCrossOwner bool,
	authorizedOwners []string,
	source string,
) (*db.Job, error) {
	st := s.vmMgr.GetStore()
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = conn.DefaultBranch
	}
	if branch == "" {
		branch = "main"
	}
	commitSHA := strings.TrimSpace(req.CommitSHA)
	userCmd := strings.TrimSpace(req.Command)
	if userCmd == "" {
		userCmd = conn.DefaultCommand
	}
	if userCmd == "" {
		userCmd = "echo 'CI job completed'"
	}
	imageID := normalizeCIImageID(req.ImageID)
	vcpus := req.VCPUs
	if vcpus <= 0 {
		vcpus = conn.VCPUs
	}
	if vcpus <= 0 {
		vcpus = 2
	}
	memMB := req.MemoryMB
	if memMB <= 0 {
		memMB = conn.MemoryMB
	}
	if memMB <= 0 {
		memMB = 2048
	}
	timeoutSec := req.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = conn.TimeoutSec
	}
	if timeoutSec <= 0 {
		timeoutSec = 600
	}

	// Clone and execute inside the ephemeral VM. The GitHub token itself is NOT
	// stored in SpecJSON; internal/jobs/runner.go injects GITHUB_TOKEN at execution
	// time from the repository connection stored in SQLite.
	script := fmt.Sprintf(`set -e
mkdir -p /workspace && cd /workspace
rm -rf repo
if [ -n "${GITHUB_TOKEN:-}" ] && echo "$REPO_URL" | grep -q '^https://'; then
  AUTH_HEADER="Authorization: Basic $(printf 'x-access-token:%%s' "$GITHUB_TOKEN" | base64 | tr -d '\n')"
  git -c "http.extraheader=$AUTH_HEADER" clone --branch "$CI_BRANCH" --depth 1 "$REPO_URL" repo
  if [ -n "${CI_COMMIT_SHA:-}" ]; then
    cd repo
    git -c "http.extraheader=$AUTH_HEADER" fetch --depth 1 origin "$CI_COMMIT_SHA"
    git checkout "$CI_COMMIT_SHA"
  else
    cd repo
  fi
else
  git clone --branch "$CI_BRANCH" --depth 1 "$REPO_URL" repo
  cd repo
  if [ -n "${CI_COMMIT_SHA:-}" ]; then
    git fetch --depth 1 origin "$CI_COMMIT_SHA"
    git checkout "$CI_COMMIT_SHA"
  fi
fi
%s`, userCmd)

	env := make(map[string]string, len(req.Env)+4)
	for k, v := range req.Env {
		env[k] = v
	}
	env["REPO_URL"] = conn.RepoURL
	env["CI_REPO_NAME"] = conn.Name
	env["CI_BRANCH"] = branch
	if commitSHA != "" {
		env["CI_COMMIT_SHA"] = commitSHA
	}

	spec := api.JobSpec{
		ImageID:            imageID,
		VCPUs:              vcpus,
		MemoryMB:           memMB,
		Command:            []string{"bash", "-lc", script},
		CWD:                "/workspace",
		Env:                env,
		TimeoutSeconds:     timeoutSec,
		RepoURL:            conn.RepoURL,
		RepoName:           conn.Name,
		Branch:             branch,
		CommitSHA:          commitSHA,
		Source:             source,
		AllowCrossOwner:    allowCrossOwner,
		AuthorizedOwnerIDs: authorizedOwners,
	}
	specJSON, _ := json.Marshal(spec)
	now := time.Now().UTC()
	job := &db.Job{
		ID:        fmt.Sprintf("job_%s", uuid.NewString()[:8]),
		OwnerID:   conn.OwnerID,
		PoolID:    req.PoolID,
		Type:      "ci",
		State:     "queued",
		Priority:  10,
		SpecJSON:  string(specJSON),
		RepoID:    conn.ID,
		RepoURL:   conn.RepoURL,
		RepoName:  conn.Name,
		Branch:    branch,
		CommitSHA: commitSHA,
		Source:    source,
		QueuedAt:  now,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.SaveJob(ctx, job); err != nil {
		return nil, err
	}
	_ = st.UpdateGitHubConnectionTriggered(ctx, conn.ID, now)

	if s.scheduler != nil {
		go func() {
			_, _ = s.scheduler.ScheduleNext(context.Background())
		}()
	} else if s.jobRunner != nil {
		go func() {
			s.jobRunner.TryStart(context.Background(), job, spec)
		}()
	}
	return job, nil
}

func verifyGitHubHMAC256(secret, signatureHeader string, body []byte) bool {
	if strings.TrimSpace(secret) == "" {
		return false
	}
	sig := strings.TrimSpace(signatureHeader)
	if !strings.HasPrefix(sig, "sha256=") {
		return false
	}
	gotHex := strings.TrimPrefix(sig, "sha256=")
	gotMAC, err := hex.DecodeString(gotHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	expectedMAC := mac.Sum(nil)
	return hmac.Equal(gotMAC, expectedMAC)
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	st := s.vmMgr.GetStore()

	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		httpError(w, "failed to read webhook payload", http.StatusBadRequest)
		return
	}

	var payload struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Action     string `json:"action"`
		Repository struct {
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
			HTMLURL  string `json:"html_url"`
		} `json:"repository"`
		HeadCommit struct {
			ID string `json:"id"`
		} `json:"head_commit"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			httpError(w, "invalid JSON payload: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	event := strings.ToLower(strings.TrimSpace(r.Header.Get("X-GitHub-Event")))
	sigHeader := r.Header.Get("X-Hub-Signature-256")

	repoURL := payload.Repository.CloneURL
	if repoURL == "" {
		repoURL = payload.Repository.HTMLURL
	}
	conns, err := st.FindGitHubConnectionsByRepo(ctx, repoURL, payload.Repository.FullName)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(conns) == 0 && payload.Repository.HTMLURL != "" && payload.Repository.HTMLURL != repoURL {
		conns, _ = st.FindGitHubConnectionsByRepo(ctx, payload.Repository.HTMLURL, payload.Repository.FullName)
	}

	// Verify HMAC signatures across matching connections BEFORE dispatching any
	// jobs so one tenant's non-matching secret never blocks another tenant or
	// leaves partially launched jobs on a 401.
	verifiedConns := make([]*db.GitHubConnection, 0, len(conns))
	for _, conn := range conns {
		if conn == nil {
			continue
		}
		if conn.Status != "" && conn.Status != "connected" && conn.Status != "active" {
			continue
		}
		if verifyGitHubHMAC256(conn.WebhookSecret, sigHeader, body) {
			verifiedConns = append(verifiedConns, conn)
		}
	}

	if len(conns) > 0 && len(verifiedConns) == 0 {
		httpError(w, "invalid webhook signature", http.StatusUnauthorized)
		return
	}

	if event == "ping" {
		json.NewEncoder(w).Encode(map[string]string{"status": "pong"})
		return
	}

	if len(verifiedConns) == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ignored",
			"reason": "no matching connected repository",
		})
		return
	}

	if event != "" && event != "push" && event != "workflow_job" && event != "workflow_dispatch" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ignored",
			"event":  event,
		})
		return
	}

	branch := strings.TrimPrefix(payload.Ref, "refs/heads/")
	commitSHA := strings.TrimSpace(payload.After)
	if commitSHA == "" || commitSHA == "0000000000000000000000000000000000000000" {
		commitSHA = strings.TrimSpace(payload.HeadCommit.ID)
	}

	triggered := make([]*db.Job, 0, len(verifiedConns))
	for _, conn := range verifiedConns {
		jobBranch := branch
		if jobBranch == "" || strings.HasPrefix(payload.Ref, "refs/tags/") {
			jobBranch = conn.DefaultBranch
		}
		allowCross := conn.OwnerID == "" || conn.OwnerID == "default"
		var authOwners []string
		if !allowCross {
			authOwners = []string{conn.OwnerID}
		}
		job, err := s.launchRepoCIJob(ctx, conn, ciTriggerRequest{
			Branch:    jobBranch,
			CommitSHA: commitSHA,
			Source:    "github",
		}, allowCross, authOwners, "github")
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		triggered = append(triggered, job)
		s.recordEvent("ci.webhook.github", "github-webhook", conn.Name, "delivered", 15, map[string]interface{}{
			"repo_id": conn.ID,
			"job_id":  job.ID,
			"branch":  job.Branch,
			"commit":  job.CommitSHA,
			"event":   event,
		})
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "triggered",
		"count":  len(triggered),
		"jobs":   triggered,
	})
}
