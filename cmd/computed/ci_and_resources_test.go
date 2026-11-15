package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/pkg/api"
	"github.com/coder/websocket"
)

func TestAgentProfilesRemoved(t *testing.T) {
	env := newMultiTenantServer(t)

	for _, path := range []string{"/api/v1/agent-profiles", "/api/v1/agent-profiles/prof_123"} {
		resp, _ := mtRequest(t, env.ts, http.MethodGet, path, multiTenantAdminSecret, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404 for removed route %s, got %d", path, resp.StatusCode)
		}
	}
}

func TestJobVMsExcludedFromSandboxesVolumesAndSnapshot(t *testing.T) {
	env := newMultiTenantServer(t)
	ctx := context.Background()

	mtSignup(t, env.ts, "alice@example.com", "alice", "Password123!")
	aliceTok, aliceID := mtLogin(t, env.ts, "alice", "Password123!")

	// Save a regular sandbox VM, a job VM owned by alice, and a job VM owned by "jobs".
	regularVM := &db.VM{
		ID:        "vm-regular-1",
		OwnerID:   aliceID,
		Name:      "dev-sandbox",
		VCPUs:     2,
		MemoryMB:  2048,
		DiskMB:    51200,
		Status:    db.StatusRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	jobVMUser := &db.VM{
		ID:        "vm-job-user",
		OwnerID:   aliceID,
		Name:      "job-deadbeef",
		VCPUs:     2,
		MemoryMB:  2048,
		DiskMB:    51200,
		Status:    db.StatusRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	jobVMSystem := &db.VM{
		ID:        "vm-job-sys",
		OwnerID:   "jobs",
		Name:      "runner-ephemeral",
		VCPUs:     2,
		MemoryMB:  2048,
		DiskMB:    51200,
		Status:    db.StatusRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	for _, v := range []*db.VM{regularVM, jobVMUser, jobVMSystem} {
		if err := env.vmStore.SaveVM(ctx, v); err != nil {
			t.Fatalf("save vm %s: %v", v.Name, err)
		}
	}

	// Pre-seed a leaked job root volume in SQLite to verify GET /api/v1/volumes filters it too.
	_ = env.vmStore.SaveVolume(ctx, &db.Volume{
		ID:         "vol_root_job-deadbeef",
		OwnerID:    aliceID,
		Name:       "job-deadbeef-rootfs",
		Pool:       "default",
		SizeGB:     10,
		Status:     "attached",
		AttachedVM: "job-deadbeef",
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	})

	checkVMsList := func(token, label string) {
		for _, ep := range []string{"/api/v1/vms", "/api/v1/sandboxes"} {
			resp, body := mtRequest(t, env.ts, http.MethodGet, ep, token, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s GET %s status %d: %s", label, ep, resp.StatusCode, string(body))
			}
			var vms []*db.VM
			if err := json.Unmarshal(body, &vms); err != nil {
				t.Fatalf("decode %s: %v", ep, err)
			}
			foundRegular := false
			for _, v := range vms {
				if isJobVM(v) {
					t.Fatalf("%s GET %s leaked job VM %+v", label, ep, v)
				}
				if v.Name == "dev-sandbox" {
					foundRegular = true
				}
			}
			if !foundRegular {
				t.Fatalf("%s GET %s missing regular VM dev-sandbox", label, ep)
			}
		}
	}

	checkVMsList(aliceTok, "alice")
	checkVMsList(multiTenantAdminSecret, "admin")

	// Check Volumes API excludes job volumes for both alice and admin.
	for _, tc := range []struct {
		label string
		token string
	}{{"alice", aliceTok}, {"admin", multiTenantAdminSecret}} {
		resp, body := mtRequest(t, env.ts, http.MethodGet, "/api/v1/volumes", tc.token, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s GET /api/v1/volumes status %d: %s", tc.label, resp.StatusCode, string(body))
		}
		var vols []*db.Volume
		if err := json.Unmarshal(body, &vols); err != nil {
			t.Fatalf("decode volumes: %v", err)
		}
		for _, vol := range vols {
			if strings.HasPrefix(vol.Name, "job-") || strings.HasPrefix(vol.AttachedVM, "job-") || strings.Contains(vol.ID, "job-") {
				t.Fatalf("%s GET /api/v1/volumes leaked job volume %+v", tc.label, vol)
			}
		}
	}

	// Check WebSocket ClusterSnapshot excludes job VMs and job volumes.
	wsURL := "ws" + strings.TrimPrefix(env.ts.URL, "http") + "/api/v1/ws"
	wsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	headers := http.Header{"Authorization": []string{"Bearer " + multiTenantAdminSecret}}
	conn, _, err := websocket.Dial(wsCtx, wsURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	_, data, err := conn.Read(wsCtx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var snap ClusterSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("ws decode: %v", err)
	}
	for _, v := range snap.VMs {
		if isJobVM(v) {
			t.Fatalf("ClusterSnapshot leaked job VM: %+v", v)
		}
	}
	for _, vol := range snap.Volumes {
		if isJobVolume(vol) {
			t.Fatalf("ClusterSnapshot leaked job volume: %+v", vol)
		}
	}

	// Direct GET /api/v1/vms/{id} on a job VM must return 404.
	respJobVM, _ := mtRequest(t, env.ts, http.MethodGet, "/api/v1/vms/vm-job-user", multiTenantAdminSecret, nil)
	if respJobVM.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on GET /api/v1/vms/vm-job-user, got %d", respJobVM.StatusCode)
	}

	// User creation of VM or Volume with reserved "job-" prefix must return 400.
	respBadVM, _ := mtRequest(t, env.ts, http.MethodPost, "/api/v1/vms", aliceTok, map[string]interface{}{
		"name": "job-user-collide",
	})
	if respBadVM.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when creating VM with reserved job- prefix, got %d", respBadVM.StatusCode)
	}
	respBadVol, _ := mtRequest(t, env.ts, http.MethodPost, "/api/v1/volumes", aliceTok, map[string]interface{}{
		"name":    "job-vol-collide",
		"size_gb": 10,
	})
	if respBadVol.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when creating volume with reserved job- prefix, got %d", respBadVol.StatusCode)
	}
}

func TestCIReposCRUDTriggerWebhookAndIsolation(t *testing.T) {
	env := newMultiTenantServer(t)

	mtSignup(t, env.ts, "alice@example.com", "alice", "Password123!")
	aliceTok, _ := mtLogin(t, env.ts, "alice", "Password123!")

	mtSignup(t, env.ts, "bob@example.com", "bob", "Password123!")
	bobTok, _ := mtLogin(t, env.ts, "bob", "Password123!")

	const rawToken = "ghp_supersecret9876543210"
	const webhookSecret = "my-gh-webhook-secret"

	// 1. Connect GitHub repo as Alice.
	resp, body := mtRequest(t, env.ts, http.MethodPost, "/api/v1/ci/repos", aliceTok, map[string]interface{}{
		"repo_url":        "https://github.com/acme/webapp",
		"default_branch":  "main",
		"github_token":    rawToken,
		"webhook_secret":  webhookSecret,
		"default_command": "go test ./...",
		"vcpus":           2,
		"memory_mb":       2048,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/v1/ci/repos status %d: %s", resp.StatusCode, string(body))
	}
	if strings.Contains(string(body), rawToken) {
		t.Fatalf("POST /api/v1/ci/repos leaked raw github_token in JSON response: %s", string(body))
	}
	var created db.GitHubConnection
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode created repo: %v", err)
	}
	if !created.HasToken || created.TokenMasked == "" {
		t.Fatalf("expected has_token=true and token_masked populated, got %+v", created)
	}
	if !created.HasWebhookSecret || created.WebhookSecretMasked == "" {
		t.Fatalf("expected has_webhook_secret=true and webhook_secret_masked populated, got %+v", created)
	}
	if created.Name != "acme/webapp" {
		t.Fatalf("expected derived name acme/webapp, got %q", created.Name)
	}

	// 2. List repos as Alice and verify neither raw token nor raw webhook_secret is exposed on GET.
	resp, body = mtRequest(t, env.ts, http.MethodGet, "/api/v1/ci/repos", aliceTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/ci/repos status %d", resp.StatusCode)
	}
	if strings.Contains(string(body), rawToken) || strings.Contains(string(body), webhookSecret) {
		t.Fatalf("GET /api/v1/ci/repos leaked raw secret: %s", string(body))
	}
	var aliceRepos []*db.GitHubConnection
	_ = json.Unmarshal(body, &aliceRepos)
	if len(aliceRepos) != 1 || aliceRepos[0].ID != created.ID {
		t.Fatalf("unexpected repos list: %+v", aliceRepos)
	}

	resp, _ = mtRequest(t, env.ts, http.MethodGet, "/api/v1/ci/repos/"+created.ID, bobTok, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when Bob accesses Alice's repo, got %d", resp.StatusCode)
	}
	resp, _ = mtRequest(t, env.ts, http.MethodPost, "/api/v1/ci/repos/"+created.ID+"/trigger", bobTok, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when Bob triggers Alice's repo, got %d", resp.StatusCode)
	}
	// Cross-tenant POST /api/v1/jobs with Alice's repo_id by Bob must be rejected with 404.
	respSteal, _ := mtRequest(t, env.ts, http.MethodPost, "/api/v1/jobs", bobTok, map[string]interface{}{
		"type":    "command",
		"repo_id": created.ID,
		"spec": map[string]interface{}{
			"command": []string{"sh", "-c", "echo $GITHUB_TOKEN"},
		},
	})
	if respSteal.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when Bob submits job with Alice's repo_id, got %d", respSteal.StatusCode)
	}

	// 3. PATCH repo without overwriting existing token.
	resp, body = mtRequest(t, env.ts, http.MethodPatch, "/api/v1/ci/repos/"+created.ID, aliceTok, map[string]interface{}{
		"default_branch": "develop",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH /api/v1/ci/repos/{id} status %d: %s", resp.StatusCode, string(body))
	}
	stored, err := env.vmStore.GetGitHubConnection(context.Background(), created.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetGitHubConnection failed: %v", err)
	}
	if stored.GitHubToken != rawToken {
		t.Fatalf("expected stored GitHubToken preserved on PATCH, got %q", stored.GitHubToken)
	}
	if stored.DefaultBranch != "develop" {
		t.Fatalf("expected default_branch updated to develop, got %q", stored.DefaultBranch)
	}

	// 4. Manual trigger creates job with ubuntu-24 default, CI metadata, and no raw token in spec_json.
	resp, body = mtRequest(t, env.ts, http.MethodPost, "/api/v1/ci/repos/"+created.ID+"/trigger", aliceTok, map[string]interface{}{
		"branch":   "feature/ci",
		"image_id": "ubuntu-24.04",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/v1/ci/repos/{id}/trigger status %d: %s", resp.StatusCode, string(body))
	}
	var manualJob db.Job
	if err := json.Unmarshal(body, &manualJob); err != nil {
		t.Fatalf("decode triggered job: %v", err)
	}
	if manualJob.RepoID != created.ID || manualJob.RepoURL != "https://github.com/acme/webapp" || manualJob.Branch != "feature/ci" || manualJob.Source != "manual" {
		t.Fatalf("unexpected triggered job metadata: %+v", manualJob)
	}
	if strings.Contains(manualJob.SpecJSON, rawToken) {
		t.Fatalf("job SpecJSON must never embed raw GitHub token")
	}
	var manualSpec api.JobSpec
	_ = json.Unmarshal([]byte(manualJob.SpecJSON), &manualSpec)
	if manualSpec.ImageID != "ubuntu-24" {
		t.Fatalf("expected normalized image_id ubuntu-24, got %q", manualSpec.ImageID)
	}

	// 5. Connect the same repo as Bob with a different webhook secret; verify Alice's signed webhook still succeeds!
	respBobRepo, _ := mtRequest(t, env.ts, http.MethodPost, "/api/v1/ci/repos", bobTok, map[string]interface{}{
		"repo_url":        "https://github.com/acme/webapp",
		"default_branch":  "main",
		"webhook_secret":  "bob-different-secret",
		"default_command": "echo bob",
	})
	if respBobRepo.StatusCode != http.StatusCreated {
		t.Fatalf("expected Bob POST /api/v1/ci/repos 201, got %d", respBobRepo.StatusCode)
	}

	// GitHub Webhook: invalid signature -> 401; valid ping -> 200; valid push -> triggers Alice's job only.
	pingPayload := []byte(`{"zen":"Keep it logically awesome.","repository":{"full_name":"acme/webapp","clone_url":"https://github.com/acme/webapp.git"}}`)
	badReq, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/v1/ci/webhooks/github", bytes.NewReader(pingPayload))
	badReq.Header.Set("X-GitHub-Event", "ping")
	badReq.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 on invalid webhook signature, got %d", badResp.StatusCode)
	}

	sign := func(b []byte) string {
		mac := hmac.New(sha256.New, []byte(webhookSecret))
		_, _ = mac.Write(b)
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}

	pushPayload := []byte(`{"ref":"refs/heads/main","after":"abc1234567890","repository":{"full_name":"acme/webapp","clone_url":"https://github.com/acme/webapp.git"}}`)
	pushReq, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/v1/ci/webhooks/github", bytes.NewReader(pushPayload))
	pushReq.Header.Set("X-GitHub-Event", "push")
	pushReq.Header.Set("X-Hub-Signature-256", sign(pushPayload))
	pushResp, err := http.DefaultClient.Do(pushReq)
	if err != nil {
		t.Fatal(err)
	}
	defer pushResp.Body.Close()
	if pushResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 on GitHub push webhook, got %d", pushResp.StatusCode)
	}
	var whOut struct {
		Status string    `json:"status"`
		Count  int       `json:"count"`
		Jobs   []*db.Job `json:"jobs"`
	}
	if err := json.NewDecoder(pushResp.Body).Decode(&whOut); err != nil {
		t.Fatalf("decode webhook response: %v", err)
	}
	if whOut.Count != 1 || len(whOut.Jobs) != 1 {
		t.Fatalf("expected 1 webhook job (Alice only), got %+v", whOut)
	}
	whJob := whOut.Jobs[0]
	if whJob.Source != "github" || whJob.Branch != "main" || whJob.CommitSHA != "abc1234567890" || whJob.RepoID != created.ID {
		t.Fatalf("unexpected webhook job fields: %+v", whJob)
	}

	// 6. Direct POST /api/v1/jobs defaults source to "cli" and normalizes image_id "ubuntu-24.04" to "ubuntu-24".
	resp, body = mtRequest(t, env.ts, http.MethodPost, "/api/v1/jobs", aliceTok, map[string]interface{}{
		"type": "command",
		"spec": map[string]interface{}{
			"image_id": "ubuntu-24.04",
			"command":  []string{"echo", "hello"},
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/v1/jobs status %d: %s", resp.StatusCode, string(body))
	}
	var cliJob db.Job
	_ = json.Unmarshal(body, &cliJob)
	if cliJob.Source != "cli" {
		t.Fatalf("expected default job source cli, got %q", cliJob.Source)
	}
	var cliSpec api.JobSpec
	_ = json.Unmarshal([]byte(cliJob.SpecJSON), &cliSpec)
	if cliSpec.ImageID != "ubuntu-24" {
		t.Fatalf("expected default job spec image_id ubuntu-24, got %q", cliSpec.ImageID)
	}

	// 7. Delete repo
	resp, _ = mtRequest(t, env.ts, http.MethodDelete, "/api/v1/ci/repos/"+created.ID, aliceTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /api/v1/ci/repos/{id} status %d", resp.StatusCode)
	}
}

func TestImagesUbuntu24Metadata(t *testing.T) {
	env := newMultiTenantServer(t)

	for _, imgName := range []string{"ubuntu-24", "ubuntu-24.04", "img-base-ubuntu-24"} {
		resp, body := mtRequest(t, env.ts, http.MethodGet, "/api/v1/images/"+imgName, multiTenantAdminSecret, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/v1/images/%s status %d: %s", imgName, resp.StatusCode, string(body))
		}
	}

	resp, body := mtRequest(t, env.ts, http.MethodGet, "/api/v1/images", multiTenantAdminSecret, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/images status %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "iputils-ping") || !strings.Contains(string(body), `"name":"ubuntu-24"`) {
		t.Fatalf("expected base image ubuntu-24 with iputils-ping, got: %s", string(body))
	}

	respBuild, _ := mtRequest(t, env.ts, http.MethodPost, "/api/v1/images/build", multiTenantAdminSecret, map[string]interface{}{"name": "custom"})
	if respBuild.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on POST /api/v1/images/build, got %d", respBuild.StatusCode)
	}
}

func TestAdminResourcesEndpoint(t *testing.T) {
	env := newMultiTenantServer(t)
	ctx := context.Background()

	mtSignup(t, env.ts, "alice@example.com", "alice", "Password123!")
	aliceTok, aliceID := mtLogin(t, env.ts, "alice", "Password123!")

	// Non-admin must receive 403 Forbidden.
	resp, _ := mtRequest(t, env.ts, http.MethodGet, "/api/v1/admin/resources", aliceTok, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin on /api/v1/admin/resources, got %d", resp.StatusCode)
	}

	// Seed 1 regular sandbox VM, 1 job VM, 1 job, and 1 persistent volume.
	_ = env.vmStore.SaveVM(ctx, &db.VM{
		ID:        "vm-admin-res-1",
		OwnerID:   aliceID,
		Name:      "web-1",
		VCPUs:     2,
		MemoryMB:  2048,
		DiskMB:    51200,
		Status:    db.StatusRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	_ = env.vmStore.SaveVM(ctx, &db.VM{
		ID:        "vm-admin-res-job",
		OwnerID:   aliceID,
		Name:      "job-1234",
		VCPUs:     4,
		MemoryMB:  4096,
		DiskMB:    51200,
		Status:    db.StatusRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	_ = env.vmStore.SaveJob(ctx, &db.Job{
		ID:        "job_test1",
		OwnerID:   aliceID,
		Type:      "ci",
		State:     "running",
		VMID:      "vm-admin-res-job",
		SpecJSON:  `{"vcpus":4,"memory_mb":4096}`,
		QueuedAt:  time.Now().UTC(),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	_ = env.vmStore.SaveVolume(ctx, &db.Volume{
		ID:        "vol_data_1",
		OwnerID:   aliceID,
		Name:      "pg-data",
		Pool:      "default",
		SizeGB:    25,
		Status:    "attached",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})

	// Admin scrape #1 and #2 (second scrape also exercises rate calculation).
	resp, body := mtRequest(t, env.ts, http.MethodGet, "/api/v1/admin/resources", multiTenantAdminSecret, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for admin on /api/v1/admin/resources, got %d: %s", resp.StatusCode, string(body))
	}
	time.Sleep(30 * time.Millisecond)
	resp, body = mtRequest(t, env.ts, http.MethodGet, "/api/v1/admin/resources", multiTenantAdminSecret, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on second scrape, got %d: %s", resp.StatusCode, string(body))
	}

	var res adminResourcesResponse
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("decode admin resources: %v", err)
	}
	if res.CPU.Cores <= 0 {
		t.Fatalf("expected cpu.cores > 0, got %+v", res.CPU)
	}
	if res.Memory.TotalMB <= 0 {
		t.Fatalf("expected memory.total_mb > 0, got %+v", res.Memory)
	}
	if res.Disk.TotalGB <= 0 {
		t.Fatalf("expected disk.total_gb > 0, got %+v", res.Disk)
	}
	if res.System.Hostname == "" || res.System.UptimeSeconds <= 0 {
		t.Fatalf("expected populated system hostname and uptime, got %+v", res.System)
	}
	if res.Allocations.Sandboxes.Total != 1 || res.Allocations.Sandboxes.VCPUsAllocated != 2 {
		t.Fatalf("expected 1 non-job sandbox with 2 vCPUs allocated, got %+v", res.Allocations.Sandboxes)
	}
	if res.Allocations.CIJobs.Total != 1 || res.Allocations.CIJobs.Running != 1 || res.Allocations.CIJobs.VCPUsAllocated != 4 || res.Allocations.CIJobs.MemoryMBAllocated != 4096 {
		t.Fatalf("expected 1 running CI job with 4 vCPUs and 4096 MB allocated, got %+v", res.Allocations.CIJobs)
	}
	if res.Allocations.Volumes.Total != 1 || res.Allocations.AllocatedVolumeDiskGB != 25 {
		t.Fatalf("expected 1 persistent volume with 25 GB allocated, got %+v (allocated_volume_disk_gb=%v)", res.Allocations.Volumes, res.Allocations.AllocatedVolumeDiskGB)
	}
	if res.Allocations.AllocatedVCPUs != 6 || res.Allocations.JobVCPUs != 4 {
		t.Fatalf("expected total allocated_vcpus=6 (2 sandbox + 4 job), got allocated=%d job=%d", res.Allocations.AllocatedVCPUs, res.Allocations.JobVCPUs)
	}
	if res.CapacityWarnings == nil {
		t.Fatalf("expected non-nil capacity_warnings slice")
	}
}
