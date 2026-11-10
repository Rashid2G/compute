package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "compute-db-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	vm := &VM{
		ID:             "vm-test-1",
		Name:           "test-vm",
		Status:         StatusCreated,
		DesiredRunning: true,
		VCPUs:          1,
		MemoryMB:       512,
		IPAddress:      "172.16.0.2",
		MACAddress:     "06:00:ac:10:00:02",
		TAPDevice:      "mftap-test-1",
		RootfsPath:     "/tmp/rootfs.ext4",
		KernelPath:     "/tmp/vmlinux",
		DiskPath:       "/tmp/disk.ext4",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}

	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatalf("failed to save VM: %v", err)
	}

	got, err := store.GetVM(ctx, "vm-test-1")
	if err != nil {
		t.Fatalf("failed to get VM: %v", err)
	}
	if got == nil || got.ID != "vm-test-1" || got.Status != StatusCreated {
		t.Fatalf("unexpected VM retrieved: %+v", got)
	}
	if !got.DesiredRunning {
		t.Fatalf("expected desired_running to persist as true: %+v", got)
	}

	// Update status
	got.Status = StatusRunning
	got.DesiredRunning = false
	if err := store.SaveVM(ctx, got); err != nil {
		t.Fatalf("failed to update VM: %v", err)
	}

	updated, _ := store.GetVM(ctx, "vm-test-1")
	if updated.Status != StatusRunning {
		t.Fatalf("expected status running, got %s", updated.Status)
	}
	if updated.DesiredRunning {
		t.Fatalf("expected desired_running to update to false")
	}

	vms, err := store.ListVMs(ctx)
	if err != nil || len(vms) != 1 {
		t.Fatalf("expected 1 VM, got %d (err: %v)", len(vms), err)
	}

	ips, err := store.GetAllocatedIPs(ctx)
	if err != nil || !ips["172.16.0.2"] {
		t.Fatalf("expected IP 172.16.0.2 in allocated list: %v", ips)
	}

	if err := store.DeleteVM(ctx, "vm-test-1"); err != nil {
		t.Fatalf("failed to delete VM: %v", err)
	}

	deleted, _ := store.GetVM(ctx, "vm-test-1")
	if deleted != nil {
		t.Fatalf("expected nil after delete, got %+v", deleted)
	}
}

func TestStore_ImagesAndTemplates(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "compute-db-img-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// 1. Test CustomImage
	img := &CustomImage{
		ID:          "img-123",
		Name:        "python-dev",
		Description: "Python development image",
		BaseImage:   "ubuntu-24.04",
		Packages:    []string{"python3", "python3-pip", "git"},
		RootfsPath:  "/tmp/python-dev.ext4",
		KernelPath:  "/tmp/vmlinux",
		SizeMB:      2048,
		Status:      ImageStatusReady,
	}

	if err := store.SaveImage(ctx, img); err != nil {
		t.Fatalf("failed to save image: %v", err)
	}

	gotImg, err := store.GetImage(ctx, "python-dev")
	if err != nil || gotImg == nil {
		t.Fatalf("failed to get image by name: %v", err)
	}
	if gotImg.ID != "img-123" || len(gotImg.Packages) != 3 || gotImg.Packages[0] != "python3" {
		t.Fatalf("unexpected image retrieved: %+v", gotImg)
	}

	imgList, err := store.ListImages(ctx)
	if err != nil || len(imgList) != 1 {
		t.Fatalf("expected 1 image in list, got %d", len(imgList))
	}

	// 2. Test Template
	tpl := &Template{
		ID:          "tpl-456",
		Name:        "web-stack-tpl",
		Description: "Configured web stack template",
		SourceVMID:  "vm-src-1",
		DiskPath:    "/tmp/web-stack-tpl.ext4",
		KernelPath:  "/tmp/vmlinux",
		VCPUs:       2,
		MemoryMB:    4096,
		DiskMB:      51200,
	}

	if err := store.SaveTemplate(ctx, tpl); err != nil {
		t.Fatalf("failed to save template: %v", err)
	}

	gotTpl, err := store.GetTemplate(ctx, "web-stack-tpl")
	if err != nil || gotTpl == nil {
		t.Fatalf("failed to get template by name: %v", err)
	}
	if gotTpl.ID != "tpl-456" || gotTpl.VCPUs != 2 || gotTpl.SourceVMID != "vm-src-1" {
		t.Fatalf("unexpected template retrieved: %+v", gotTpl)
	}

	tplList, err := store.ListTemplates(ctx)
	if err != nil || len(tplList) != 1 {
		t.Fatalf("expected 1 template in list, got %d", len(tplList))
	}

	// Delete both
	if err := store.DeleteImage(ctx, "python-dev"); err != nil {
		t.Fatalf("failed to delete image: %v", err)
	}
	if err := store.DeleteTemplate(ctx, "web-stack-tpl"); err != nil {
		t.Fatalf("failed to delete template: %v", err)
	}

	delImg, _ := store.GetImage(ctx, "python-dev")
	if delImg != nil {
		t.Fatalf("expected nil after delete image")
	}
	delTpl, _ := store.GetTemplate(ctx, "web-stack-tpl")
	if delTpl != nil {
		t.Fatalf("expected nil after delete template")
	}
}

func TestStore_DistinctInternalIDsAndNameLookup(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "compute-db-tenant-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// 1. VM with name as external ID and distinct internal ID
	vmAlice := &VM{
		ID:         "web",
		Name:       "web",
		InternalID: "ins_alice_12345678",
		OwnerID:    "user-alice",
		Status:     StatusRunning,
		VCPUs:      2,
		MemoryMB:   2048,
		IPAddress:  "172.16.0.10",
		MACAddress: "06:00:ac:10:00:0a",
		TAPDevice:  "mftap-alice",
	}

	// 2. Another user also creates a VM with name "web", but has distinct internal ID
	vmBob := &VM{
		ID:         "web",
		Name:       "web",
		InternalID: "ins_bob_87654321",
		OwnerID:    "user-bob",
		Status:     StatusCreated,
		VCPUs:      4,
		MemoryMB:   4096,
		IPAddress:  "172.16.0.20",
		MACAddress: "06:00:ac:10:00:14",
		TAPDevice:  "mftap-bob",
	}

	if err := store.SaveVM(ctx, vmAlice); err != nil {
		t.Fatalf("failed to save Alice VM: %v", err)
	}
	if err := store.SaveVM(ctx, vmBob); err != nil {
		t.Fatalf("failed to save Bob VM: %v", err)
	}

	// Lookup by distinct internal ID
	aliceByInternal, err := store.GetVM(ctx, "ins_alice_12345678")
	if err != nil || aliceByInternal == nil || aliceByInternal.OwnerID != "user-alice" {
		t.Fatalf("failed to fetch Alice VM by internal ID: %+v", aliceByInternal)
	}
	bobByInternal, err := store.GetVM(ctx, "ins_bob_87654321")
	if err != nil || bobByInternal == nil || bobByInternal.OwnerID != "user-bob" {
		t.Fatalf("failed to fetch Bob VM by internal ID: %+v", bobByInternal)
	}

	// Lookup by name scoped to owner
	aliceByName, err := store.GetVMByOwner(ctx, "user-alice", "web")
	if err != nil || aliceByName == nil || aliceByName.InternalID != "ins_alice_12345678" {
		t.Fatalf("expected Alice VM when scoped to Alice: %+v", aliceByName)
	}
	bobByName, err := store.GetVMByOwner(ctx, "user-bob", "web")
	if err != nil || bobByName == nil || bobByName.InternalID != "ins_bob_87654321" {
		t.Fatalf("expected Bob VM when scoped to Bob: %+v", bobByName)
	}

	// Delete Alice VM should not delete Bob VM
	if err := store.DeleteVMByOwner(ctx, "user-alice", "web"); err != nil {
		t.Fatalf("failed to delete Alice VM: %v", err)
	}

	deletedAlice, _ := store.GetVMByOwner(ctx, "user-alice", "web")
	if deletedAlice != nil {
		t.Fatalf("expected Alice VM to be deleted")
	}

	stillBob, err := store.GetVMByOwner(ctx, "user-bob", "web")
	if err != nil || stillBob == nil || stillBob.InternalID != "ins_bob_87654321" {
		t.Fatalf("Bob VM should still exist after Alice VM deleted: %+v", stillBob)
	}
}

func TestStore_SSHKeys(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "compute-db-key-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	key1 := &SSHKey{
		ID:          "ssh-key-1",
		OwnerID:     "default",
		Name:        "laptop",
		PublicKey:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey1 test@laptop",
		Fingerprint: "SHA256:testfingerprint1",
	}

	key2 := &SSHKey{
		ID:          "ssh-key-2",
		OwnerID:     "default",
		Name:        "workstation",
		PublicKey:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey2 test@workstation",
		Fingerprint: "SHA256:testfingerprint2",
	}

	if err := store.SaveSSHKey(ctx, key1); err != nil {
		t.Fatalf("failed to save key1: %v", err)
	}
	if err := store.SaveSSHKey(ctx, key2); err != nil {
		t.Fatalf("failed to save key2: %v", err)
	}

	// Lookup by ID
	gotByID, err := store.GetSSHKey(ctx, "ssh-key-1")
	if err != nil || gotByID == nil {
		t.Fatalf("failed to get key by ID: %v", err)
	}
	if gotByID.Name != "laptop" || gotByID.Fingerprint != "SHA256:testfingerprint1" {
		t.Fatalf("unexpected key by ID: %+v", gotByID)
	}

	// Lookup by Name
	gotByName, err := store.GetSSHKey(ctx, "workstation")
	if err != nil || gotByName == nil {
		t.Fatalf("failed to get key by Name: %v", err)
	}
	if gotByName.ID != "ssh-key-2" {
		t.Fatalf("unexpected key by Name: %+v", gotByName)
	}

	// List keys
	list, err := store.ListSSHKeys(ctx, "default")
	if err != nil || len(list) != 2 {
		t.Fatalf("expected 2 keys, got %d (err: %v)", len(list), err)
	}

	// Delete key
	if err := store.DeleteSSHKey(ctx, "laptop"); err != nil {
		t.Fatalf("failed to delete key by Name: %v", err)
	}

	delKey, _ := store.GetSSHKey(ctx, "ssh-key-1")
	if delKey != nil {
		t.Fatalf("expected key to be deleted")
	}

	listAfter, err := store.ListSSHKeys(ctx, "default")
	if err != nil || len(listAfter) != 1 {
		t.Fatalf("expected 1 key after deletion, got %d", len(listAfter))
	}
}

func TestStore_FailStaleJobs(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	for _, j := range []*Job{
		{ID: "job-running", State: "running"},
		{ID: "job-assigned", State: "assigned"},
		{ID: "job-queued", State: "queued"},
	} {
		if err := store.SaveJob(ctx, j); err != nil {
			t.Fatalf("failed to save job %s: %v", j.ID, err)
		}
	}

	n, err := store.FailStaleJobs(ctx)
	if err != nil {
		t.Fatalf("FailStaleJobs failed: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 interrupted jobs, got %d", n)
	}
	if j, _ := store.GetJob(ctx, "job-queued"); j == nil || j.State != "queued" {
		t.Fatalf("queued job should be untouched, got %+v", j)
	}
	if j, _ := store.GetJob(ctx, "job-running"); j == nil || j.State != "failed" {
		t.Fatalf("running job should be failed, got %+v", j)
	}
}

func TestStore_NewEntities(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	// 1. WorkerTokens
	tok := &WorkerToken{
		Token:     "tok-1",
		Name:      "test-token",
		PoolID:    "default",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := store.SaveWorkerToken(ctx, tok); err != nil {
		t.Fatalf("SaveWorkerToken failed: %v", err)
	}
	tokens, err := store.ListWorkerTokens(ctx)
	if err != nil || len(tokens) != 1 || tokens[0].Name != "test-token" {
		t.Fatalf("unexpected tokens: %+v (err: %v)", tokens, err)
	}
	if err := store.DeleteWorkerToken(ctx, "tok-1"); err != nil {
		t.Fatalf("DeleteWorkerToken failed: %v", err)
	}
	tokens, _ = store.ListWorkerTokens(ctx)
	if len(tokens) != 0 {
		t.Fatalf("expected 0 tokens after delete")
	}

	// 2. Volumes
	vol := &Volume{
		ID:         "vol-1",
		Name:       "data-vol",
		Pool:       "default",
		SizeGB:     50,
		UsedGB:     12.5,
		Status:     "available",
		Filesystem: "ext4",
		IOPS:       3000,
		Throughput: "250 MB/s",
		Snapshots:  1,
	}
	if err := store.SaveVolume(ctx, vol); err != nil {
		t.Fatalf("SaveVolume failed: %v", err)
	}
	vGot, err := store.GetVolume(ctx, "vol-1")
	if err != nil || vGot == nil || vGot.Name != "data-vol" || vGot.SizeGB != 50 {
		t.Fatalf("unexpected volume: %+v", vGot)
	}
	vols, _ := store.ListVolumes(ctx)
	if len(vols) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(vols))
	}
	if err := store.DeleteVolume(ctx, "vol-1"); err != nil {
		t.Fatalf("DeleteVolume failed: %v", err)
	}

	// 3. Webhooks
	wh := &WebhookEndpoint{
		ID:          "whk-1",
		URL:         "https://example.com/hook",
		Description: "test webhook",
		Secret:      "whsec_test",
		Events:      []string{"sandbox.started", "sandbox.stopped"},
		Active:      true,
		SuccessRate: "100%",
	}
	if err := store.SaveWebhook(ctx, wh); err != nil {
		t.Fatalf("SaveWebhook failed: %v", err)
	}
	whGot, err := store.GetWebhook(ctx, "whk-1")
	if err != nil || whGot == nil || len(whGot.Events) != 2 || !whGot.Active {
		t.Fatalf("unexpected webhook: %+v", whGot)
	}
	whs, _ := store.ListWebhooks(ctx)
	if len(whs) != 1 {
		t.Fatalf("expected 1 webhook, got %d", len(whs))
	}
	if err := store.DeleteWebhook(ctx, "whk-1"); err != nil {
		t.Fatalf("DeleteWebhook failed: %v", err)
	}

	// 4. OTelConfig
	otelCfg, err := store.GetOTelConfig(ctx)
	if err != nil || otelCfg == nil || otelCfg.ServiceName != "on-demand-vms" {
		t.Fatalf("unexpected default otel cfg: %+v", otelCfg)
	}
	otelCfg.Enabled = true
	otelCfg.Endpoint = "http://otel.collector:4318"
	otelCfg.SpansExported = 100
	if err := store.SaveOTelConfig(ctx, otelCfg); err != nil {
		t.Fatalf("SaveOTelConfig failed: %v", err)
	}
	otelUpdated, _ := store.GetOTelConfig(ctx)
	if !otelUpdated.Enabled || otelUpdated.Endpoint != "http://otel.collector:4318" || otelUpdated.SpansExported != 100 {
		t.Fatalf("unexpected updated otel config: %+v", otelUpdated)
	}

	// 5. AuditEvents
	evt := &AuditEvent{
		ID:         "evt-1",
		Event:      "sandbox.started",
		Actor:      "admin",
		Target:     "vm-1",
		Status:     "delivered",
		DurationMs: 45,
		Payload:    `{"test":true}`,
	}
	if err := store.SaveAuditEvent(ctx, evt); err != nil {
		t.Fatalf("SaveAuditEvent failed: %v", err)
	}
	evts, err := store.ListAuditEvents(ctx, 10)
	if err != nil || len(evts) != 1 || evts[0].Event != "sandbox.started" {
		t.Fatalf("unexpected audit events: %+v", evts)
	}

	// 6. AccessAPIKeys
	apiKey := &AccessAPIKey{
		ID:        "key-1",
		Name:      "test-api-key",
		Prefix:    "bsh_live_1234",
		Token:     "bsh_live_1234567890",
		Scope:     "admin",
		CreatedBy: "admin",
	}
	if err := store.SaveAccessAPIKey(ctx, apiKey); err != nil {
		t.Fatalf("SaveAccessAPIKey failed: %v", err)
	}
	keyGot, err := store.GetAccessAPIKey(ctx, "key-1")
	if err != nil || keyGot == nil || keyGot.Name != "test-api-key" {
		t.Fatalf("unexpected api key: %+v", keyGot)
	}
	keys, _ := store.ListAccessAPIKeys(ctx)
	if len(keys) != 1 {
		t.Fatalf("expected 1 api key, got %d", len(keys))
	}
	if err := store.DeleteAccessAPIKey(ctx, "key-1"); err != nil {
		t.Fatalf("DeleteAccessAPIKey failed: %v", err)
	}

	// 7. AgentProfiles
	prof := &AgentProfile{
		ID:          "prof-1",
		Name:        "Claude Proxy",
		Provider:    "Anthropic",
		UpstreamURL: "https://api.anthropic.com/v1",
		Status:      "active",
	}
	if err := store.SaveAgentProfile(ctx, prof); err != nil {
		t.Fatalf("SaveAgentProfile failed: %v", err)
	}
	pGot, err := store.GetAgentProfile(ctx, "prof-1")
	if err != nil || pGot == nil || pGot.Name != "Claude Proxy" {
		t.Fatalf("unexpected agent profile: %+v", pGot)
	}
	profs, _ := store.ListAgentProfiles(ctx)
	if len(profs) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profs))
	}
	if err := store.DeleteAgentProfile(ctx, "prof-1"); err != nil {
		t.Fatalf("DeleteAgentProfile failed: %v", err)
	}

	// 8. Environments
	env := &EnvironmentProfile{
		Name:               "base",
		IsDefault:          true,
		SafeForThirdParty:  false,
		GitHubReposEnabled: true,
		GitHubRepos:        []string{"owner/repo1"},
		SecretsEnabled:     true,
		Secrets:            "KEY=VALUE",
		BoatCredsEnabled:   true,
		AgentCredsEnabled:  true,
		CredentialProfile:  "Owner's default",
		Version:            1,
	}
	if err := store.SaveEnvironment(ctx, env); err != nil {
		t.Fatalf("SaveEnvironment failed: %v", err)
	}
	envGot, err := store.GetEnvironment(ctx, "base")
	if err != nil || envGot == nil || !envGot.IsDefault || len(envGot.GitHubRepos) != 1 {
		t.Fatalf("unexpected environment: %+v", envGot)
	}
	envs, _ := store.ListEnvironments(ctx)
	if len(envs) != 1 {
		t.Fatalf("expected 1 environment, got %d", len(envs))
	}
	if err := store.DeleteEnvironment(ctx, "base"); err != nil {
		t.Fatalf("DeleteEnvironment failed: %v", err)
	}

	// 9. PortForwards
	pf := &PortForward{
		ID:        "pf-1",
		HostPort:  8080,
		VMID:      "vm-1",
		VMName:    "my-vm",
		GuestPort: 80,
		Protocol:  "tcp",
		Subdomain: "myvm",
		PublicURL: "https://myvm.compute.dev",
		Status:    "active",
	}
	if err := store.SavePortForward(ctx, pf); err != nil {
		t.Fatalf("SavePortForward failed: %v", err)
	}
	pfGot, err := store.GetPortForward(ctx, "pf-1")
	if err != nil || pfGot == nil || pfGot.HostPort != 8080 {
		t.Fatalf("unexpected port forward: %+v", pfGot)
	}
	pfs, _ := store.ListPortForwards(ctx)
	if len(pfs) != 1 {
		t.Fatalf("expected 1 port forward, got %d", len(pfs))
	}
	if err := store.DeletePortForward(ctx, "pf-1"); err != nil {
		t.Fatalf("DeletePortForward failed: %v", err)
	}
}

func TestStore_ConsumeWorkerTokenAtomic(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	// 1. Valid token
	tok := &WorkerToken{
		Token:     "tok-atomic-1",
		Name:      "test-worker-1",
		PoolID:    "default",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
		CreatedAt: time.Now().UTC(),
	}
	if err := store.SaveWorkerToken(ctx, tok); err != nil {
		t.Fatalf("SaveWorkerToken failed: %v", err)
	}

	// First consumption must succeed
	if err := store.ConsumeWorkerTokenAtomic(ctx, "tok-atomic-1"); err != nil {
		t.Fatalf("first ConsumeWorkerTokenAtomic failed: %v", err)
	}

	// Second consumption on already-used token must fail
	if err := store.ConsumeWorkerTokenAtomic(ctx, "tok-atomic-1"); err == nil {
		t.Fatalf("expected second ConsumeWorkerTokenAtomic to fail, but succeeded")
	}

	// 2. Expired token
	expiredTok := &WorkerToken{
		Token:     "tok-atomic-expired",
		Name:      "expired-worker",
		PoolID:    "default",
		ExpiresAt: time.Now().UTC().Add(-time.Hour),
		CreatedAt: time.Now().UTC().Add(-2 * time.Hour),
	}
	if err := store.SaveWorkerToken(ctx, expiredTok); err != nil {
		t.Fatalf("SaveWorkerToken expired failed: %v", err)
	}

	if err := store.ConsumeWorkerTokenAtomic(ctx, "tok-atomic-expired"); err == nil {
		t.Fatalf("expected ConsumeWorkerTokenAtomic on expired token to fail, but succeeded")
	}

	// 3. Non-existent token
	if err := store.ConsumeWorkerTokenAtomic(ctx, "tok-non-existent"); err == nil {
		t.Fatalf("expected ConsumeWorkerTokenAtomic on non-existent token to fail, but succeeded")
	}
}
