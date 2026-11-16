package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
		ID:                 "vm-test-1",
		Name:               "test-vm",
		Status:             StatusCreated,
		DesiredRunning:     true,
		VCPUs:              1,
		MemoryMB:           512,
		IPAddress:          "172.16.0.2",
		MACAddress:         "06:00:ac:10:00:02",
		TAPDevice:          "mftap-test-1",
		RootfsPath:         "/tmp/rootfs.ext4",
		KernelPath:         "/tmp/vmlinux",
		DiskPath:           "/tmp/disk.ext4",
		SSHPasswordEnabled: true,
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
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
	if !got.SSHPasswordEnabled {
		t.Fatalf("expected ssh_password_enabled to persist as true: %+v", got)
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
		ID:           "ssh-key-1",
		OwnerID:      "default",
		Name:         "laptop",
		PublicKey:    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey1 test@laptop",
		Fingerprint:  "SHA256:testfingerprint1",
		AllSandboxes: true,
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
	if gotByID.Name != "laptop" || gotByID.Fingerprint != "SHA256:testfingerprint1" || !gotByID.AllSandboxes {
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

func TestStore_SSHKeyDefault(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "compute-db-key-default-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	key1 := &SSHKey{ID: "ssh-key-1", OwnerID: "default", Name: "laptop", PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey1 test@laptop", Fingerprint: "SHA256:fp1"}
	key2 := &SSHKey{ID: "ssh-key-2", OwnerID: "default", Name: "workstation", PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey2 test@workstation", Fingerprint: "SHA256:fp2"}
	if err := store.SaveSSHKey(ctx, key1); err != nil {
		t.Fatalf("failed to save key1: %v", err)
	}
	if err := store.SaveSSHKey(ctx, key2); err != nil {
		t.Fatalf("failed to save key2: %v", err)
	}

	// No default until one is chosen.
	def, err := store.GetDefaultSSHKey(ctx, "default")
	if err != nil || def != nil {
		t.Fatalf("expected no default key, got %+v err=%v", def, err)
	}

	// Setting the default flags exactly one key per owner.
	if err := store.SetDefaultSSHKey(ctx, "default", key1.ID); err != nil {
		t.Fatalf("SetDefaultSSHKey failed: %v", err)
	}
	def, err = store.GetDefaultSSHKey(ctx, "default")
	if err != nil || def == nil || def.ID != key1.ID {
		t.Fatalf("expected key1 as default, got %+v err=%v", def, err)
	}
	got1, _ := store.GetSSHKey(ctx, key1.ID)
	if got1 == nil || !got1.IsDefault {
		t.Fatalf("expected key1 to carry is_default, got %+v", got1)
	}

	// Promoting key2 clears key1's flag.
	if err := store.SetDefaultSSHKey(ctx, "default", key2.ID); err != nil {
		t.Fatalf("SetDefaultSSHKey(key2) failed: %v", err)
	}
	got1, _ = store.GetSSHKey(ctx, key1.ID)
	got2, _ := store.GetSSHKey(ctx, key2.ID)
	if got1 == nil || got1.IsDefault {
		t.Fatalf("expected key1 to lose is_default, got %+v", got1)
	}
	if got2 == nil || !got2.IsDefault {
		t.Fatalf("expected key2 to carry is_default, got %+v", got2)
	}

	// Defaults are owner-scoped: another owner's key cannot be flagged, and
	// clearing only removes the flag from the exact key.
	if err := store.SetDefaultSSHKey(ctx, "other-owner", key2.ID); err == nil {
		t.Fatalf("expected error flagging a foreign owner's key as default")
	}
	if err := store.ClearDefaultSSHKey(ctx, "default", key2.ID); err != nil {
		t.Fatalf("ClearDefaultSSHKey failed: %v", err)
	}
	def, err = store.GetDefaultSSHKey(ctx, "default")
	if err != nil || def != nil {
		t.Fatalf("expected no default key after clearing, got %+v err=%v", def, err)
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

func TestStore_AuthorizeWorkerTokenReconnect(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	tok := &WorkerToken{
		Token:     "tok-reconnect",
		Name:      "worker-a",
		PoolID:    "default",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := store.SaveWorkerToken(ctx, tok); err != nil {
		t.Fatalf("SaveWorkerToken: %v", err)
	}

	// First enrollment succeeds and consumes the token.
	if err := store.AuthorizeWorkerToken(ctx, "tok-reconnect", "worker-a"); err != nil {
		t.Fatalf("first authorize: %v", err)
	}
	// The same named worker may reconnect after a restart.
	if err := store.AuthorizeWorkerToken(ctx, "tok-reconnect", "worker-a"); err != nil {
		t.Fatalf("reconnect with same name: %v", err)
	}
	// A different worker name is rejected.
	if err := store.AuthorizeWorkerToken(ctx, "tok-reconnect", "intruder"); err == nil {
		t.Fatal("expected different worker name to be rejected")
	}
	// Missing name is rejected once the token has been consumed.
	if err := store.AuthorizeWorkerToken(ctx, "tok-reconnect", ""); err == nil {
		t.Fatal("expected empty worker name to be rejected after consumption")
	}
	// Unknown token is rejected.
	if err := store.AuthorizeWorkerToken(ctx, "tok-missing", "worker-a"); err == nil {
		t.Fatal("expected unknown token to be rejected")
	}
	// Expired token is rejected.
	expired := &WorkerToken{
		Token:     "tok-expired",
		Name:      "worker-b",
		PoolID:    "default",
		ExpiresAt: time.Now().UTC().Add(-time.Hour),
	}
	if err := store.SaveWorkerToken(ctx, expired); err != nil {
		t.Fatalf("SaveWorkerToken expired: %v", err)
	}
	if err := store.AuthorizeWorkerToken(ctx, "tok-expired", "worker-b"); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestStore_AuthorizeWorkerTokenBindsUnnamedToken(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	// Tokens created without --name carry no binding. The first enrollment
	// names the token after the connecting worker so a restart can reconnect.
	unbound := &WorkerToken{Token: "tok-unbound", PoolID: "default", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := store.SaveWorkerToken(ctx, unbound); err != nil {
		t.Fatalf("SaveWorkerToken: %v", err)
	}
	if err := store.AuthorizeWorkerToken(ctx, "tok-unbound", "worker-x"); err != nil {
		t.Fatalf("first authorize: %v", err)
	}
	if err := store.AuthorizeWorkerToken(ctx, "tok-unbound", "worker-x"); err != nil {
		t.Fatalf("reconnect with same name: %v", err)
	}
	if err := store.AuthorizeWorkerToken(ctx, "tok-unbound", "worker-y"); err == nil {
		t.Fatal("expected a different worker to be rejected")
	}
	tokens, err := store.ListWorkerTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range tokens {
		if tok.Token == "tok-unbound" && tok.Name != "worker-x" {
			t.Fatalf("token not bound to first worker: %q", tok.Name)
		}
	}

	// The old placeholder name is treated as unbound so tokens minted by an
	// earlier release keep working after an upgrade.
	legacy := &WorkerToken{Token: "tok-legacy", Name: "worker-token", PoolID: "default", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := store.SaveWorkerToken(ctx, legacy); err != nil {
		t.Fatalf("SaveWorkerToken: %v", err)
	}
	if err := store.AuthorizeWorkerToken(ctx, "tok-legacy", "worker-z"); err != nil {
		t.Fatalf("legacy first authorize: %v", err)
	}
	if err := store.AuthorizeWorkerToken(ctx, "tok-legacy", "worker-z"); err != nil {
		t.Fatalf("legacy reconnect: %v", err)
	}
}

func TestStore_AuthorizeWorkerTokenReconnectAfterExpiry(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	tok := &WorkerToken{Token: "tok-bound", Name: "worker-a", PoolID: "default", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := store.SaveWorkerToken(ctx, tok); err != nil {
		t.Fatalf("SaveWorkerToken: %v", err)
	}
	if err := store.AuthorizeWorkerToken(ctx, "tok-bound", "worker-a"); err != nil {
		t.Fatalf("first authorize: %v", err)
	}

	// Enrollment tokens expire quickly; an already enrolled worker must still
	// be able to reconnect after a restart that happens later.
	if _, err := store.db.ExecContext(ctx, `UPDATE worker_tokens SET expires_at=? WHERE token=?`, time.Now().UTC().Add(-time.Hour), "tok-bound"); err != nil {
		t.Fatal(err)
	}
	if err := store.AuthorizeWorkerToken(ctx, "tok-bound", "worker-a"); err != nil {
		t.Fatalf("reconnect after expiry: %v", err)
	}
	if err := store.AuthorizeWorkerToken(ctx, "tok-bound", "worker-b"); err == nil {
		t.Fatal("expected a different worker to be rejected after expiry")
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

func TestStore_DeletionProtection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "compute-test-protection-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test-protection.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// 1. Create a protected VM
	vmProtected := &VM{
		ID:         "vm-prot-1",
		InternalID: "ins_prot_1",
		OwnerID:    "tenant-a",
		Name:       "doom",
		Status:     StatusCreated,
		VCPUs:      1,
		MemoryMB:   2048,
		Protected:  true,
	}
	if err := store.SaveVM(ctx, vmProtected); err != nil {
		t.Fatalf("SaveVM protected failed: %v", err)
	}

	// Verify it was persisted as protected
	got, err := store.GetVM(ctx, "vm-prot-1")
	if err != nil || got == nil {
		t.Fatalf("GetVM failed: %v", err)
	}
	if !got.Protected {
		t.Fatalf("expected VM to be protected")
	}

	// 2. Attempt to delete protected VM via DeleteVMByOwner - MUST FAIL
	err = store.DeleteVMByOwner(ctx, "tenant-a", "vm-prot-1")
	if err == nil {
		t.Fatalf("expected DeleteVMByOwner on protected VM to fail, but it succeeded")
	}
	if !strings.Contains(err.Error(), "protected against deletion") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// 3. Attempt to delete protected VM via DeleteVMByInternalID - MUST FAIL
	err = store.DeleteVMByInternalID(ctx, "ins_prot_1")
	if err == nil {
		t.Fatalf("expected DeleteVMByInternalID on protected VM to fail, but it succeeded")
	}
	if !strings.Contains(err.Error(), "protected against deletion") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// 4. Attempt direct SQL DELETE - MUST BE ABORTED BY TRIGGER
	_, rawErr := store.db.Exec("DELETE FROM vms WHERE id = 'vm-prot-1'")
	if rawErr == nil {
		t.Fatalf("expected raw SQL DELETE to be aborted by SQLite trigger, but succeeded")
	}
	if !strings.Contains(rawErr.Error(), "protected against deletion") {
		t.Fatalf("expected trigger abort error, got: %v", rawErr)
	}

	// Verify VM is still intact
	stillThere, _ := store.GetVM(ctx, "vm-prot-1")
	if stillThere == nil {
		t.Fatalf("protected VM was deleted despite protection!")
	}

	// 5. Unprotect and delete
	stillThere.Protected = false
	if err := store.SaveVM(ctx, stillThere); err != nil {
		t.Fatalf("SaveVM unprotect failed: %v", err)
	}
	if err := store.DeleteVM(ctx, "vm-prot-1"); err != nil {
		t.Fatalf("DeleteVM on unprotected VM failed: %v", err)
	}
	gone, _ := store.GetVM(ctx, "vm-prot-1")
	if gone != nil {
		t.Fatalf("expected VM to be deleted after unprotecting")
	}
}

func TestStore_VMTailnetFields(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "compute-db-tailnet-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	vm := &VM{
		ID:                "vm-ts-1",
		Name:              "ts-box",
		Status:            StatusRunning,
		IPAddress:         "172.16.0.9",
		TailscaleEnabled:  true,
		TailnetIP:         "100.64.7.7",
		TailnetDNS:        "compute-vm-ts-1.tail1234.ts.net",
		TailscaleDeviceID: "dev123",
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	}
	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatalf("failed to save VM: %v", err)
	}

	got, err := store.GetVM(ctx, "vm-ts-1")
	if err != nil || got == nil {
		t.Fatalf("failed to get VM: %v", err)
	}
	if !got.TailscaleEnabled || got.TailnetIP != "100.64.7.7" || got.TailnetDNS != "compute-vm-ts-1.tail1234.ts.net" || got.TailscaleDeviceID != "dev123" {
		t.Fatalf("tailnet fields lost on read: %+v", got)
	}

	// Update path must persist the fields too.
	got.TailnetIP = "100.64.7.8"
	got.TailscaleEnabled = false
	if err := store.SaveVM(ctx, got); err != nil {
		t.Fatalf("failed to update VM: %v", err)
	}
	updated, _ := store.GetVM(ctx, "vm-ts-1")
	if updated.TailnetIP != "100.64.7.8" || updated.TailscaleEnabled {
		t.Fatalf("tailnet update not persisted: %+v", updated)
	}

	// List path must expose them as well.
	all, err := store.ListVMs(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %v", err)
	}
	if all[0].TailnetIP != "100.64.7.8" {
		t.Fatalf("tailnet_ip missing in list: %+v", all[0])
	}
}

func TestStore_MigratesSSHPasswordEnabled(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	ctx := context.Background()

	vm := &VM{ID: "legacy-vm", Name: "legacy-vm", OwnerID: "default", Status: StatusStopped}
	if err := store.SaveVM(ctx, vm); err != nil {
		store.Close()
		t.Fatalf("failed to save VM: %v", err)
	}
	// Simulate a database created before ssh_password_enabled existed.
	if _, err := store.db.Exec("ALTER TABLE vms DROP COLUMN ssh_password_enabled"); err != nil {
		store.Close()
		t.Fatalf("failed to drop column: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}

	reopened, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to reopen store: %v", err)
	}
	defer reopened.Close()

	got, err := reopened.GetVM(ctx, "legacy-vm")
	if err != nil {
		t.Fatalf("GetVM after migration: %v", err)
	}
	if got == nil || got.SSHPasswordEnabled {
		t.Fatalf("migrated VM = %+v, want ssh_password_enabled=false", got)
	}

	all, err := reopened.ListVMs(ctx)
	if err != nil {
		t.Fatalf("ListVMs after migration: %v", err)
	}
	if len(all) != 1 || all[0].SSHPasswordEnabled {
		t.Fatalf("migrated list = %+v, want one VM with ssh_password_enabled=false", all)
	}
}

func TestStore_NetworkIsolationPersistenceAndMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "iso.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	ctx := context.Background()

	vmAcc := &VM{
		ID:               "vm-iso-default",
		InternalID:       "ins_iso_default",
		Name:             "vm-iso-default",
		OwnerID:          "usr_1",
		Status:           StatusRunning,
		IPAddress:        "172.16.0.21",
		MACAddress:       "06:00:ac:10:00:15",
		TAPDevice:        "mftap-21",
		NetworkIsolation: "", // should normalize to account
	}
	vmIso := &VM{
		ID:               "vm-iso-only",
		InternalID:       "ins_iso_only",
		Name:             "vm-iso-only",
		OwnerID:          "usr_1",
		Status:           StatusRunning,
		IPAddress:        "172.16.0.22",
		MACAddress:       "06:00:ac:10:00:16",
		TAPDevice:        "mftap-22",
		NetworkIsolation: NetworkIsolationIsolated,
	}
	if err := store.SaveVM(ctx, vmAcc); err != nil {
		t.Fatalf("SaveVM vmAcc: %v", err)
	}
	if err := store.SaveVM(ctx, vmIso); err != nil {
		t.Fatalf("SaveVM vmIso: %v", err)
	}

	gotAcc, err := store.GetVMByOwner(ctx, "usr_1", "vm-iso-default")
	if err != nil || gotAcc == nil || gotAcc.NetworkIsolation != NetworkIsolationAccount {
		t.Fatalf("expected default network_isolation=account, got %+v (err=%v)", gotAcc, err)
	}
	gotIso, err := store.GetVMByOwner(ctx, "usr_1", "vm-iso-only")
	if err != nil || gotIso == nil || gotIso.NetworkIsolation != NetworkIsolationIsolated {
		t.Fatalf("expected network_isolation=isolated, got %+v (err=%v)", gotIso, err)
	}

	alloc, err := store.GetNetworkAllocation(ctx, "mftap-22")
	if err != nil || alloc == nil || alloc.NetworkIsolation != NetworkIsolationIsolated {
		t.Fatalf("expected allocation network_isolation=isolated, got %+v (err=%v)", alloc, err)
	}
	allocs, err := store.GetNetworkAllocations(ctx)
	if err != nil || len(allocs) != 2 {
		t.Fatalf("expected 2 allocations, got %d (err=%v)", len(allocs), err)
	}

	// Simulate pre-migration DB lacking network_isolation column.
	if _, err := store.db.Exec("ALTER TABLE vms DROP COLUMN network_isolation"); err != nil {
		store.Close()
		t.Fatalf("drop network_isolation failed: %v", err)
	}
	_ = store.Close()

	reopened, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()

	listed, err := reopened.ListVMsByOwner(ctx, "usr_1")
	if err != nil || len(listed) != 2 {
		t.Fatalf("ListVMsByOwner after migration: %d (err=%v)", len(listed), err)
	}
	for _, v := range listed {
		if v.NetworkIsolation != NetworkIsolationAccount {
			t.Fatalf("expected migrated VM %s to default to account, got %q", v.ID, v.NetworkIsolation)
		}
	}
}

func TestStore_Milestone1ModelsAndPolicy(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "m1.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	// Default scheduling policy
	pol, err := store.GetSchedulingPolicy(ctx)
	if err != nil {
		t.Fatalf("GetSchedulingPolicy default: %v", err)
	}
	if pol.CPUSoftOvercommitRatio != 1.5 || pol.CPUHardOvercommitRatio != 2.0 || !pol.MemoryHardGuarantee || !pol.DiskHardEnforcement {
		t.Fatalf("unexpected default scheduling policy: %+v", pol)
	}

	// Custom scheduling policy round-trip
	pol.CPUSoftOvercommitRatio = 1.25
	pol.IdleThresholdSeconds = 300
	if err := store.SaveSchedulingPolicy(ctx, pol); err != nil {
		t.Fatalf("SaveSchedulingPolicy: %v", err)
	}
	gotPol, err := store.GetSchedulingPolicy(ctx)
	if err != nil {
		t.Fatalf("GetSchedulingPolicy reload: %v", err)
	}
	if gotPol.CPUSoftOvercommitRatio != 1.25 || gotPol.IdleThresholdSeconds != 300 {
		t.Fatalf("reloaded policy mismatch: %+v", gotPol)
	}

	// GetWorkerToken
	tok := &WorkerToken{
		Token:     "tok-pool-1",
		Name:      "worker-a",
		PoolID:    "pool-gpu",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := store.SaveWorkerToken(ctx, tok); err != nil {
		t.Fatalf("SaveWorkerToken: %v", err)
	}
	gotTok, err := store.GetWorkerToken(ctx, "tok-pool-1")
	if err != nil || gotTok == nil || gotTok.PoolID != "pool-gpu" {
		t.Fatalf("GetWorkerToken failed: %+v err=%v", gotTok, err)
	}

	// WorkerNode physical + allocated fields
	now := time.Now().UTC()
	wn := &WorkerNode{
		ID:              "nod_1",
		Name:            "worker-a",
		PoolID:          "pool-gpu",
		Role:            "worker",
		Status:          "ready",
		Arch:            "amd64",
		TotalVCPUs:      16,
		TotalMemMB:      32768,
		TotalDiskMB:     500000,
		AllocatedVCPUs:  14,
		AllocatedMemMB:  30720,
		AllocatedDiskMB: 450000,
		PhysicalVCPUs:   16,
		PhysicalMemMB:   32768,
		PhysicalDiskMB:  500000,
		LastHeartbeat:   &now,
	}
	if err := store.SaveWorkerNode(ctx, wn); err != nil {
		t.Fatalf("SaveWorkerNode: %v", err)
	}
	gotWN, err := store.GetWorkerNode(ctx, "nod_1")
	if err != nil || gotWN == nil {
		t.Fatalf("GetWorkerNode: %+v err=%v", gotWN, err)
	}
	if gotWN.AllocatedVCPUs != 14 || gotWN.AllocatedMemMB != 30720 || gotWN.AllocatedDiskMB != 450000 ||
		gotWN.PhysicalVCPUs != 16 || gotWN.PhysicalMemMB != 32768 || gotWN.PhysicalDiskMB != 500000 {
		t.Fatalf("WorkerNode capacity fields not persisted: %+v", gotWN)
	}
	wns, err := store.ListWorkerNodes(ctx)
	if err != nil || len(wns) != 1 || wns[0].AllocatedDiskMB != 450000 {
		t.Fatalf("ListWorkerNodes capacity mismatch: %+v err=%v", wns, err)
	}

	// VM placement & auto-hibernation fields + TouchVMActivity
	act := now.Add(-10 * time.Minute)
	vm := &VM{
		ID:              "vm-m1",
		Name:            "vm-m1",
		OwnerID:         "usr_1",
		Status:          StatusPaused,
		VCPUs:           2,
		MemoryMB:        1024,
		DiskMB:          10240,
		PoolID:          "pool-gpu",
		WorkerID:        "nod_1",
		LastActivityAt:  &act,
		AutoHibernated:  true,
		HibernateReason: "idle_memory_reclaim",
	}
	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatalf("SaveVM: %v", err)
	}
	gotVM, err := store.GetVMByOwner(ctx, "usr_1", "vm-m1")
	if err != nil || gotVM == nil {
		t.Fatalf("GetVMByOwner: %+v err=%v", gotVM, err)
	}
	if gotVM.PoolID != "pool-gpu" || gotVM.WorkerID != "nod_1" || !gotVM.AutoHibernated || gotVM.HibernateReason != "idle_memory_reclaim" || gotVM.LastActivityAt == nil {
		t.Fatalf("VM milestone 1 fields lost: %+v", gotVM)
	}
	if err := store.TouchVMActivity(ctx, "vm-m1"); err != nil {
		t.Fatalf("TouchVMActivity: %v", err)
	}
	touched, _ := store.GetVM(ctx, "vm-m1")
	if touched.LastActivityAt == nil || !touched.LastActivityAt.After(act) {
		t.Fatalf("expected LastActivityAt to advance after TouchVMActivity, got %v", touched.LastActivityAt)
	}
}

func TestStore_MigratesPoolWorkerAndHibernateColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-m1.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	if err := s.SaveVM(ctx, &VM{ID: "vm-old", Name: "vm-old", Status: StatusStopped}); err != nil {
		t.Fatalf("SaveVM: %v", err)
	}
	if err := s.SaveWorkerNode(ctx, &WorkerNode{ID: "nod-old", Name: "nod-old", PoolID: "default", Status: "ready"}); err != nil {
		t.Fatalf("SaveWorkerNode: %v", err)
	}
	for _, stmt := range []string{
		"ALTER TABLE vms DROP COLUMN pool_id",
		"ALTER TABLE vms DROP COLUMN worker_id",
		"ALTER TABLE vms DROP COLUMN last_activity_at",
		"ALTER TABLE vms DROP COLUMN auto_hibernated",
		"ALTER TABLE vms DROP COLUMN hibernate_reason",
		"ALTER TABLE worker_nodes DROP COLUMN allocated_vcpus",
		"ALTER TABLE worker_nodes DROP COLUMN allocated_mem_mb",
		"ALTER TABLE worker_nodes DROP COLUMN allocated_disk_mb",
		"ALTER TABLE worker_nodes DROP COLUMN physical_vcpus",
		"ALTER TABLE worker_nodes DROP COLUMN physical_mem_mb",
		"ALTER TABLE worker_nodes DROP COLUMN physical_disk_mb",
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("%s failed: %v", stmt, err)
		}
	}
	_ = s.Close()

	reopened, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen NewStore: %v", err)
	}
	defer reopened.Close()

	vmGot, err := reopened.GetVM(ctx, "vm-old")
	if err != nil || vmGot == nil {
		t.Fatalf("GetVM after migration: %+v err=%v", vmGot, err)
	}
	wnGot, err := reopened.GetWorkerNode(ctx, "nod-old")
	if err != nil || wnGot == nil {
		t.Fatalf("GetWorkerNode after migration: %+v err=%v", wnGot, err)
	}
}
