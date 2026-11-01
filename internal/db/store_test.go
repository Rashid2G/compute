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
		ID:         "vm-test-1",
		Name:       "test-vm",
		Status:     StatusCreated,
		VCPUs:      1,
		MemoryMB:   512,
		IPAddress:  "172.16.0.2",
		MACAddress: "06:00:ac:10:00:02",
		TAPDevice:  "mftap-test-1",
		RootfsPath: "/tmp/rootfs.ext4",
		KernelPath: "/tmp/vmlinux",
		DiskPath:   "/tmp/disk.ext4",
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
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

	// Update status
	got.Status = StatusRunning
	if err := store.SaveVM(ctx, got); err != nil {
		t.Fatalf("failed to update VM: %v", err)
	}

	updated, _ := store.GetVM(ctx, "vm-test-1")
	if updated.Status != StatusRunning {
		t.Fatalf("expected status running, got %s", updated.Status)
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
