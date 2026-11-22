package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/network"
	"github.com/Rashid2G/compute/internal/storage"
)

func readDiskPrefix(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, n)
	nRead, _ := f.Read(buf)
	return string(buf[:nRead])
}

type testRunner struct {
	calls [][]string
	taps  []string
}

func (r *testRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	cmdStr := strings.Join(append([]string{name}, args...), " ")
	if strings.Contains(cmdStr, "ip -o link show") {
		taps := r.taps
		if len(taps) == 0 {
			taps = []string{"mftap-dead"}
		}
		var out strings.Builder
		for i, tap := range taps {
			fmt.Fprintf(&out, "%d: %s: <BROADCAST>\n", i+10, tap)
		}
		return []byte(out.String()), nil
	}
	return []byte("ok"), nil
}

func testManager(t *testing.T) (*Manager, *db.Store, *storage.Manager, *testRunner) {
	t.Helper()
	dir := t.TempDir()
	store, err := db.NewStore(filepath.Join(dir, "db", "vms.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	base, vms := filepath.Join(dir, "images"), filepath.Join(dir, "vms")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "ubuntu-24.04.ext4"), []byte("rootfs"), 0600); err != nil {
		t.Fatal(err)
	}
	stor, err := storage.NewManager(storage.Config{BaseImagesDir: base, VMsDir: vms, DefaultRootfs: "ubuntu-24.04.ext4", DefaultKernel: "vmlinux", SSHKeyPath: filepath.Join(base, "key")})
	if err != nil {
		t.Fatal(err)
	}
	if avail, err := stor.GetAvailableDiskSpaceMB(); err == nil && avail < 256 {
		t.Skipf("skipping test: insufficient free disk space on %s (%d MB < 256 MB)", dir, avail)
	}
	policy := db.DefaultSchedulingPolicy()
	policy.HostReservedDiskMB = 0
	_ = store.SaveSchedulingPolicy(context.Background(), policy)
	runner := &testRunner{}
	net, err := network.NewManager(network.Config{SubnetCIDR: "10.20.0.0/24"}, runner)
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(Config{}, store, net, stor), store, stor, runner
}

func TestCreateVMAllocatesAndPersists(t *testing.T) {
	ctx := context.Background()
	m, store, _, _ := testManager(t)

	vm, err := m.CreateVM(ctx, CreateRequest{Name: "custom", VCPUs: 2, MemoryMB: 1024})
	if err != nil {
		t.Fatalf("CreateVM error = %v", err)
	}
	if vm.ID == "" || vm.Name != "custom" || vm.VCPUs != 2 || vm.MemoryMB != 1024 {
		t.Fatalf("unexpected vm: %+v", vm)
	}
	if vm.IPAddress != "10.20.0.2" {
		t.Fatalf("expected IP 10.20.0.2, got %s", vm.IPAddress)
	}
	if !strings.HasPrefix(vm.TAPDevice, "mftap-") {
		t.Fatalf("unexpected tap device: %s", vm.TAPDevice)
	}
	loaded, err := store.GetVM(ctx, vm.ID)
	if err != nil || loaded == nil {
		t.Fatalf("GetVM error = %v, loaded = %+v", err, loaded)
	}
	if loaded.Status != db.StatusCreated {
		t.Fatalf("expected status created, got %s", loaded.Status)
	}
}

func TestCreateVMCustomIP(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := testManager(t)

	// Valid custom IP
	vm, err := m.CreateVM(ctx, CreateRequest{Name: "custom-ip", IPAddress: "10.20.0.50", MACAddress: "06:00:0a:14:00:32"})
	if err != nil {
		t.Fatalf("CreateVM custom IP error = %v", err)
	}
	if vm.IPAddress != "10.20.0.50" {
		t.Fatalf("expected IP 10.20.0.50, got %s", vm.IPAddress)
	}
	if vm.MACAddress != "06:00:0a:14:00:32" {
		t.Fatalf("expected MAC 06:00:0a:14:00:32, got %s", vm.MACAddress)
	}

	// Invalid custom IP (outside subnet)
	_, err = m.CreateVM(ctx, CreateRequest{Name: "bad-ip", IPAddress: "192.168.1.50"})
	if err == nil {
		t.Fatalf("expected error for IP outside subnet")
	}

	// Duplicate custom IP
	_, err = m.CreateVM(ctx, CreateRequest{Name: "dup-ip", IPAddress: "10.20.0.50"})
	if err == nil {
		t.Fatalf("expected error for duplicate IP")
	}
}

func TestGetAndListVMs(t *testing.T) {
	ctx := context.Background()
	m, store, _, _ := testManager(t)

	if vms, err := m.ListVMs(ctx); err != nil || len(vms) != 0 {
		t.Fatalf("ListVMs error = %v, len = %d", err, len(vms))
	}

	vm1, _ := m.CreateVM(ctx, CreateRequest{Name: "vm1"})
	vm2, _ := m.CreateVM(ctx, CreateRequest{Name: "vm2"})

	vms, err := m.ListVMs(ctx)
	if err != nil || len(vms) != 2 {
		t.Fatalf("ListVMs len = %d, error = %v", len(vms), err)
	}
	fetched, err := m.GetVM(ctx, vm1.ID)
	if err != nil || fetched == nil || fetched.ID != vm1.ID {
		t.Fatalf("GetVM fetched = %+v, error = %v", fetched, err)
	}
	_ = vm2
	_ = store
}

func TestDeleteVMCleansStorageAndDatabase(t *testing.T) {
	ctx := context.Background()
	m, store, stor, _ := testManager(t)

	vm, err := m.CreateVM(ctx, CreateRequest{Name: "to-delete"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(vm.DiskPath); err != nil {
		t.Fatalf("disk should exist: %v", err)
	}

	if err := m.DeleteVM(ctx, vm.ID); err != nil {
		t.Fatalf("DeleteVM error = %v", err)
	}

	deleted, err := store.GetVM(ctx, vm.ID)
	if err != nil || deleted != nil {
		t.Fatalf("VM should be removed from store, got %+v, err = %v", deleted, err)
	}
	if _, err := os.Stat(stor.GetVMDir(vm.ID)); !os.IsNotExist(err) {
		t.Fatalf("expected vm dir to be removed, err = %v", err)
	}
}

func TestFlushGuestFilesystemsSyncsRunningVMs(t *testing.T) {
	ctx := context.Background()
	m, store, _, _ := testManager(t)

	running := &db.VM{ID: "vm-flush", Name: "flush", Status: db.StatusRunning, IPAddress: "10.20.0.10", SSHKeyPath: "/tmp/key"}
	noIP := &db.VM{ID: "vm-noip", Name: "noip", Status: db.StatusRunning, SSHKeyPath: "/tmp/key"}
	stopped := &db.VM{ID: "vm-stopped-flush", Name: "stopped", Status: db.StatusStopped, IPAddress: "10.20.0.11", SSHKeyPath: "/tmp/key"}
	for _, vm := range []*db.VM{running, noIP, stopped} {
		if err := store.SaveVM(ctx, vm); err != nil {
			t.Fatal(err)
		}
	}

	var synced []string
	m.guestSyncer = func(_ context.Context, vm *db.VM) error {
		synced = append(synced, vm.ID)
		return nil
	}

	if got := m.FlushGuestFilesystems(ctx); got != 1 {
		t.Fatalf("expected 1 flushed guest, got %d (synced=%v)", got, synced)
	}
	if len(synced) != 1 || synced[0] != running.ID {
		t.Fatalf("expected only the running VM with an IP to be flushed, got %v", synced)
	}

	// A failing guest is logged but does not block the others.
	m.guestSyncer = func(_ context.Context, vm *db.VM) error {
		return fmt.Errorf("guest unreachable")
	}
	if got := m.FlushGuestFilesystems(ctx); got != 0 {
		t.Fatalf("expected 0 flushed guests when sync fails, got %d", got)
	}
}

func TestStopVMStoppedAndMissingProcess(t *testing.T) {
	ctx := context.Background()
	m, store, _, _ := testManager(t)

	t.Run("already_stopped", func(t *testing.T) {
		vm := &db.VM{ID: "vm-stopped", Status: db.StatusStopped, TAPDevice: "tap-stopped"}
		if err := store.SaveVM(ctx, vm); err != nil {
			t.Fatal(err)
		}
		if err := m.StopVM(ctx, vm.ID); err != nil {
			t.Fatalf("StopVM already stopped error = %v", err)
		}
	})

	t.Run("missing_process", func(t *testing.T) {
		vm := &db.VM{ID: "vm-missing-pid", Status: db.StatusRunning, PID: 9999999, TAPDevice: "tap-missing"}
		if err := store.SaveVM(ctx, vm); err != nil {
			t.Fatal(err)
		}
		if err := m.StopVM(ctx, vm.ID); err != nil {
			t.Fatalf("StopVM missing process error = %v", err)
		}
		loaded, _ := store.GetVM(ctx, vm.ID)
		if loaded.Status != db.StatusStopped || loaded.PID != 0 {
			t.Fatalf("unexpected state: %+v", loaded)
		}
	})
}

func TestSaveStateAndResumeValidateState(t *testing.T) {
	ctx := context.Background()
	m, store, _, _ := testManager(t)
	vm := &db.VM{ID: "vm-test", Name: "test", Status: db.StatusCreated, VCPUs: 1, MemoryMB: 512, TAPDevice: "tap", RootfsPath: "root", KernelPath: "kernel", DiskPath: "disk"}
	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveState(ctx, vm.ID); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("SaveState error = %v", err)
	}
	if err := m.ResumeVM(ctx, vm.ID); err == nil || !strings.Contains(err.Error(), "no snapshot") {
		t.Fatalf("ResumeVM without snapshot error = %v", err)
	}
	vm.SnapshotPath = filepath.Join(t.TempDir(), "missing.state")
	vm.MemFilePath = filepath.Join(t.TempDir(), "missing.mem")
	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatal(err)
	}
	if err := m.ResumeVM(ctx, vm.ID); err == nil || !strings.Contains(err.Error(), "snapshot file missing") {
		t.Fatalf("ResumeVM missing files error = %v", err)
	}
}

func TestReconcileAndPrune(t *testing.T) {
	ctx := context.Background()
	m, store, _, _ := testManager(t)
	// Dead running VM
	vmDead := &db.VM{ID: "vm-dead", Name: "dead", Status: db.StatusRunning, PID: 9999998, TAPDevice: "mftap-dead", IPAddress: "10.20.0.99"}
	if err := store.SaveVM(ctx, vmDead); err != nil {
		t.Fatal(err)
	}

	pruned, err := m.ReconcileAndPrune(ctx)
	if err != nil {
		t.Fatalf("ReconcileAndPrune error = %v", err)
	}
	if len(pruned) != 1 || pruned[0] != "mftap-dead" {
		t.Fatalf("expected pruned [mftap-dead], got %v", pruned)
	}

	loaded, err := store.GetVM(ctx, vmDead.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != db.StatusStopped || loaded.PID != 0 {
		t.Fatalf("expected dead VM to be marked stopped, got: %+v", loaded)
	}
}

// A sandbox that is still booting keeps status "created" in the database while
// RunVM has already created its TAP. ReconcileAndPrune runs every few seconds,
// so it must not delete that TAP: doing so made Firecracker fail with
// "Setting tap interface offload flags failed: File descriptor in bad state".
func TestReconcileAndPruneKeepsTAPForBootingVM(t *testing.T) {
	ctx := context.Background()
	m, store, _, runner := testManager(t)
	runner.taps = []string{"mftap-booting", "mftap-stale", "mftap-foreign"}

	booting := &db.VM{ID: "vm-booting", Name: "booting", Status: db.StatusCreated, TAPDevice: "mftap-booting"}
	if err := store.SaveVM(ctx, booting); err != nil {
		t.Fatal(err)
	}
	stale := &db.VM{ID: "vm-stale", Name: "stale", Status: db.StatusStopped, TAPDevice: "mftap-stale"}
	if err := store.SaveVM(ctx, stale); err != nil {
		t.Fatal(err)
	}

	pruned, err := m.ReconcileAndPrune(ctx)
	if err != nil {
		t.Fatalf("ReconcileAndPrune error = %v", err)
	}
	for _, tap := range pruned {
		if tap == "mftap-booting" {
			t.Fatalf("reconciler pruned the TAP of a booting VM: %v", pruned)
		}
		if tap == "mftap-foreign" {
			t.Fatalf("reconciler pruned a TAP it does not own (another deployment): %v", pruned)
		}
	}
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "del") && strings.Contains(joined, "mftap-booting") {
			t.Fatalf("reconciler removed the booting VM TAP: %s", joined)
		}
		if strings.Contains(joined, "del") && strings.Contains(joined, "mftap-foreign") {
			t.Fatalf("reconciler removed a foreign TAP: %s", joined)
		}
	}

	loaded, err := store.GetVM(ctx, booting.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != db.StatusCreated {
		t.Fatalf("booting VM status changed to %q", loaded.Status)
	}
}

func TestInspectNetworkAndStats(t *testing.T) {
	ctx := context.Background()
	m, store, _, _ := testManager(t)

	vm1 := &db.VM{ID: "vm-net-1", Name: "net-vm", Status: db.StatusCreated, VCPUs: 2, MemoryMB: 1024, IPAddress: "10.20.0.10", MACAddress: "06:00:0a:14:00:0a", TAPDevice: "mftap-1"}
	if err := store.SaveVM(ctx, vm1); err != nil {
		t.Fatal(err)
	}

	inspect, err := m.InspectNetwork(ctx, vm1.ID)
	if err != nil {
		t.Fatalf("InspectNetwork error = %v", err)
	}
	if inspect.VMID != vm1.ID || inspect.IPAddress != vm1.IPAddress || inspect.Reachable {
		t.Fatalf("unexpected inspect result: %+v", inspect)
	}

	stats, err := m.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats error = %v", err)
	}
	if stats.TotalVMs != 1 || stats.CreatedVMs != 1 || stats.TotalVCPUs != 2 || stats.TotalMemoryMB != 1024 || stats.AllocatedIPs != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	// Exec on non-running VM should fail
	_, err = m.ExecVM(ctx, vm1.ID, ExecRequest{Command: "echo hello"})
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("ExecVM on stopped VM expected error, got %v", err)
	}
}

func TestImageBuildAndLaunchVM(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := testManager(t)

	// Mock builder to simulate building rootfs with packages
	builder := m.GetImageBuilder()
	builder.SetBuildFunc(func(ctx context.Context, req BuildImageRequest, outputPath string, sshPubKey string) error {
		return os.WriteFile(outputPath, []byte("custom-image-with-packages"), 0644)
	})

	// 1. Build image with packages
	imgReq := BuildImageRequest{
		Name:        "python-stack",
		Description: "Python stack image",
		BaseImage:   "ubuntu-24.04",
		Packages:    []string{"python3", "python3-pip", "curl"},
	}

	builtImg, err := m.BuildImage(ctx, imgReq)
	if err != nil {
		t.Fatalf("BuildImage failed: %v", err)
	}
	if builtImg.Status != db.ImageStatusReady || len(builtImg.Packages) != 3 {
		t.Fatalf("unexpected built image: %+v", builtImg)
	}

	// 2. List images
	imgs, err := m.ListImages(ctx)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("expected 1 image in list, got %d", len(imgs))
	}

	// 3. Launch a VM using this custom image
	vm, err := m.CreateVM(ctx, CreateRequest{
		Name:  "python-worker",
		Image: "python-stack",
		Tier:  "1vcpu",
	})
	if err != nil {
		t.Fatalf("CreateVM with custom image failed: %v", err)
	}

	data := readDiskPrefix(vm.DiskPath, 26)
	if data != "custom-image-with-packages" {
		t.Fatalf("expected VM disk to contain custom image rootfs, got: %s", string(data))
	}

	// 4. Delete image
	if err := m.DeleteImage(ctx, "python-stack"); err != nil {
		t.Fatalf("DeleteImage failed: %v", err)
	}
	imgsAfter, _ := m.ListImages(ctx)
	if len(imgsAfter) != 0 {
		t.Fatalf("expected 0 images after deletion")
	}
}

func TestTemplateFromVMAndLaunch(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := testManager(t)

	// 1. Create a VM to modify
	vm1, err := m.CreateVM(ctx, CreateRequest{
		Name: "dev-box",
		Tier: "2vcpu",
	})
	if err != nil {
		t.Fatalf("CreateVM failed: %v", err)
	}

	// Simulate user shell modification directly on the VM's disk
	modifiedContent := "custom-shell-installed-tools-and-files"
	if err := os.WriteFile(vm1.DiskPath, []byte(modifiedContent), 0644); err != nil {
		t.Fatalf("failed to modify VM disk: %v", err)
	}

	// 2. Create a template from the modified VM disk
	tpl, err := m.CreateTemplate(ctx, CreateTemplateRequest{
		Name:        "golang-dev-template",
		Description: "Ubuntu configured with Go toolchain and custom scripts",
		VMID:        vm1.ID,
	})
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}
	if tpl.Name != "golang-dev-template" || tpl.SourceVMID != vm1.ID || tpl.VCPUs != 2 {
		t.Fatalf("unexpected template details: %+v", tpl)
	}

	// Check template disk content
	tplData, err := os.ReadFile(tpl.DiskPath)
	if err != nil || string(tplData) != modifiedContent {
		t.Fatalf("expected template disk to contain shell modifications, got: %s", string(tplData))
	}

	// 3. List templates
	tpls, err := m.ListTemplates(ctx)
	if err != nil || len(tpls) != 1 {
		t.Fatalf("expected 1 template in list, got %d", len(tpls))
	}

	// 4. Create new VM from template via CreateRequest.Template
	vm2, err := m.CreateVM(ctx, CreateRequest{
		Name:     "worker-from-tpl",
		Template: "golang-dev-template",
	})
	if err != nil {
		t.Fatalf("CreateVM from template failed: %v", err)
	}
	// Verify it inherited vCPUs from template
	if vm2.VCPUs != 2 {
		t.Fatalf("expected vcpus=2 from template, got %d", vm2.VCPUs)
	}
	vm2Data := readDiskPrefix(vm2.DiskPath, len(modifiedContent))
	if vm2Data != modifiedContent {
		t.Fatalf("expected cloned VM disk to contain template modifications, got: %s", string(vm2Data))
	}

	// 5. Also test launching via --image <template_name>
	vm3, err := m.CreateVM(ctx, CreateRequest{
		Name:  "worker-via-image-flag",
		Image: "golang-dev-template",
		Tier:  "1vcpu",
	})
	if err != nil {
		t.Fatalf("CreateVM with image=template failed: %v", err)
	}
	vm3Data := readDiskPrefix(vm3.DiskPath, len(modifiedContent))
	if vm3Data != modifiedContent {
		t.Fatalf("expected VM from template image to contain modifications, got: %s", string(vm3Data))
	}

	// 6. Delete template
	if err := m.DeleteTemplate(ctx, "golang-dev-template"); err != nil {
		t.Fatalf("DeleteTemplate failed: %v", err)
	}
	tplsAfter, _ := m.ListTemplates(ctx)
	if len(tplsAfter) != 0 {
		t.Fatalf("expected 0 templates after deletion")
	}
	if _, err := os.Stat(tpl.DiskPath); !os.IsNotExist(err) {
		t.Fatalf("expected template disk to be removed from disk")
	}
}

func TestVMOpsByNameAndDistinctInternalIDs(t *testing.T) {
	ctx := context.Background()
	m, store, _, _ := testManager(t)
	m.SetLauncher(func(ctx context.Context, vm *db.VM) error {
		vm.PID = 43210
		return nil
	})
	m.SetProcessChecker(func(pid int) bool {
		return pid == 43210
	})

	// 1. Create VM with explicit name: external ID should be the name, internal ID should be distinct
	vm, err := m.CreateVM(ctx, CreateRequest{
		Name:     "web-prod",
		Tier:     "2vcpu",
		MemoryMB: 512,
	})
	if err != nil {
		t.Fatalf("CreateVM failed: %v", err)
	}

	if vm.Name != "web-prod" {
		t.Fatalf("expected vm.Name to be web-prod, got %q", vm.Name)
	}
	if vm.ID != "web-prod" {
		t.Fatalf("expected vm.ID (external ID) to be web-prod, got %q", vm.ID)
	}
	if vm.InternalID == "" {
		t.Fatalf("expected non-empty distinct internal ID")
	}
	if vm.InternalID == vm.ID {
		t.Fatalf("expected external ID to be different from internal ID, got internal=%q, external=%q", vm.InternalID, vm.ID)
	}

	// 2. Perform operations using NAME only
	// Get by name
	byName, err := m.GetVM(ctx, "web-prod")
	if err != nil || byName == nil || byName.ID != "web-prod" {
		t.Fatalf("GetVM by name failed: %+v, err=%v", byName, err)
	}

	// Get by internal ID
	byInt, err := m.GetVM(ctx, vm.InternalID)
	if err != nil || byInt == nil || byInt.ID != "web-prod" {
		t.Fatalf("GetVM by internal ID failed: %+v, err=%v", byInt, err)
	}

	// Run using name only
	if err := m.RunVM(ctx, "web-prod"); err != nil {
		t.Fatalf("RunVM by name failed: %v", err)
	}
	runningVM, _ := m.GetVM(ctx, "web-prod")
	if runningVM.Status != db.StatusRunning {
		t.Fatalf("expected running status, got %s", runningVM.Status)
	}

	// Inspect network using name only
	inspectRes, err := m.InspectNetwork(ctx, "web-prod")
	if err != nil || inspectRes == nil || inspectRes.VMName != "web-prod" {
		t.Fatalf("InspectNetwork by name failed: %+v, err=%v", inspectRes, err)
	}

	// Stop using name only
	if err := m.StopVM(ctx, "web-prod"); err != nil {
		t.Fatalf("StopVM by name failed: %v", err)
	}
	stoppedVM, _ := m.GetVM(ctx, "web-prod")
	if stoppedVM.Status != db.StatusStopped {
		t.Fatalf("expected stopped status, got %s", stoppedVM.Status)
	}

	// Re-start using internal ID
	if err := m.RunVM(ctx, vm.InternalID); err != nil {
		t.Fatalf("RunVM by internal ID failed: %v", err)
	}

	// Stop using name only
	if err := m.StopVM(ctx, "web-prod"); err != nil {
		t.Fatalf("StopVM by name failed: %v", err)
	}

	// Delete using name only
	if err := m.DeleteVM(ctx, "web-prod"); err != nil {
		t.Fatalf("DeleteVM by name failed: %v", err)
	}
	deletedVM, _ := m.GetVM(ctx, "web-prod")
	if deletedVM != nil {
		t.Fatalf("expected VM to be deleted, found %+v", deletedVM)
	}

	// 3. Multi-tenant isolation: distinct users can have the same VM name
	user1VM, err := m.CreateVM(ctx, CreateRequest{
		Name:    "worker",
		OwnerID: "tenant-1",
		Tier:    "1vcpu",
	})
	if err != nil {
		t.Fatalf("failed to create tenant-1 worker: %v", err)
	}

	user2VM, err := m.CreateVM(ctx, CreateRequest{
		Name:    "worker",
		OwnerID: "tenant-2",
		Tier:    "1vcpu",
	})
	if err != nil {
		t.Fatalf("failed to create tenant-2 worker: %v", err)
	}

	if user1VM.InternalID == user2VM.InternalID {
		t.Fatalf("distinct users should have distinct internal IDs in global store: %q vs %q", user1VM.InternalID, user2VM.InternalID)
	}

	// Verify lookups scoped by owner in store
	t1Got, err := store.GetVMByOwner(ctx, "tenant-1", "worker")
	if err != nil || t1Got.InternalID != user1VM.InternalID {
		t.Fatalf("expected tenant-1 worker, got %+v", t1Got)
	}
	t2Got, err := store.GetVMByOwner(ctx, "tenant-2", "worker")
	if err != nil || t2Got.InternalID != user2VM.InternalID {
		t.Fatalf("expected tenant-2 worker, got %+v", t2Got)
	}
}

func TestVMDedicatedPublicIPOnOff(t *testing.T) {
	ctx := context.Background()
	m, store, _, runner := testManager(t)

	// 1. Create VM with public IP ON by default
	vmDefault, err := m.CreateVM(ctx, CreateRequest{Name: "vm-public-default", VCPUs: 1, MemoryMB: 512})
	if err != nil {
		t.Fatalf("failed to create default VM: %v", err)
	}
	if !vmDefault.PublicIPEnabled {
		t.Fatalf("expected public IP to be enabled by default")
	}
	if vmDefault.PublicIP != vmDefault.IPAddress || vmDefault.PublicIP == "" {
		t.Fatalf("expected public IP to match dedicated IP %s, got %s", vmDefault.IPAddress, vmDefault.PublicIP)
	}

	// 2. Create VM with public IP defined as OFF
	fals := false
	vmPrivate, err := m.CreateVM(ctx, CreateRequest{
		Name:     "vm-private",
		VCPUs:    1,
		MemoryMB: 512,
		PublicIP: &fals,
	})
	if err != nil {
		t.Fatalf("failed to create private VM: %v", err)
	}
	if vmPrivate.PublicIPEnabled {
		t.Fatalf("expected public IP to be disabled")
	}
	if vmPrivate.PublicIP != "" {
		t.Fatalf("expected public IP to be empty when disabled, got %s", vmPrivate.PublicIP)
	}
	if vmPrivate.IPAddress == "" {
		t.Fatalf("expected dedicated IP to still be assigned for internal TAP/management")
	}

	// 3. Test UpdateVMConfig: Turn public IP ON for private VM
	trueVal := true
	updated, err := m.UpdateVMConfig(ctx, vmPrivate.ID, UpdateVMConfigRequest{PublicIP: &trueVal})
	if err != nil {
		t.Fatalf("failed to update VM config to ON: %v", err)
	}
	if !updated.PublicIPEnabled {
		t.Fatalf("expected PublicIPEnabled to be true after toggle")
	}
	if updated.PublicIP != updated.IPAddress {
		t.Fatalf("expected PublicIP to equal %s, got %s", updated.IPAddress, updated.PublicIP)
	}

	// Verify persistence in store
	persisted, err := store.GetVM(ctx, vmPrivate.ID)
	if err != nil || persisted == nil {
		t.Fatalf("failed to retrieve persisted VM: %v", err)
	}
	if !persisted.PublicIPEnabled || persisted.PublicIP != persisted.IPAddress {
		t.Fatalf("persisted VM mismatch: enabled=%v publicIP=%s", persisted.PublicIPEnabled, persisted.PublicIP)
	}

	// 4. Test UpdateVMConfig: Turn public IP OFF
	falseVal := false
	updatedOff, err := m.UpdateVMConfig(ctx, vmPrivate.ID, UpdateVMConfigRequest{PublicIP: &falseVal})
	if err != nil {
		t.Fatalf("failed to update VM config to OFF: %v", err)
	}
	if updatedOff.PublicIPEnabled {
		t.Fatalf("expected PublicIPEnabled to be false after toggle off")
	}
	if updatedOff.PublicIP != "" {
		t.Fatalf("expected PublicIP to be empty after toggle off, got %s", updatedOff.PublicIP)
	}

	// 5. Test JSON string unmarshaling ("on" / "off")
	var req CreateRequest
	if err := json.Unmarshal([]byte(`{"name":"json-vm","public_ip":"off"}`), &req); err != nil {
		t.Fatalf("failed to unmarshal JSON with public_ip off: %v", err)
	}
	if req.PublicIP == nil || *req.PublicIP != false {
		t.Fatalf("expected unmarshaled public_ip to be false")
	}

	var reqOn CreateRequest
	if err := json.Unmarshal([]byte(`{"name":"json-vm-on","public_ip":"on"}`), &reqOn); err != nil {
		t.Fatalf("failed to unmarshal JSON with public_ip on: %v", err)
	}
	if reqOn.PublicIP == nil || *reqOn.PublicIP != true {
		t.Fatalf("expected unmarshaled public_ip to be true")
	}

	_ = runner
}

func TestVMDHCPMode(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := testManager(t)

	dhcpTrue := true
	vmDHCP, err := m.CreateVM(ctx, CreateRequest{
		Name: "vm-dhcp-test",
		DHCP: &dhcpTrue,
	})
	if err != nil {
		t.Fatalf("failed to create DHCP VM: %v", err)
	}
	if vmDHCP.IPAddress != "dhcp" {
		t.Fatalf("expected VM IP to be 'dhcp', got %s", vmDHCP.IPAddress)
	}
	if !vmDHCP.PublicIPEnabled {
		t.Fatalf("expected public IP enabled by default")
	}
	if vmDHCP.MACAddress == "" {
		t.Fatalf("expected MAC address to be generated for DHCP VM")
	}
}

type dhcpMockRunner struct {
	testRunner
}

func (r *dhcpMockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmdStr := strings.Join(append([]string{name}, args...), " ")
	if strings.Contains(cmdStr, "ip link show") {
		return []byte("1: eth0: <BROADCAST>"), nil
	}
	if strings.Contains(cmdStr, "udhcpc") {
		return []byte("DHCP_IP=192.168.1.155\nDHCP_MASK=255.255.255.0\nDHCP_ROUTER=192.168.1.1\nDHCP_LEASE=86400\n"), nil
	}
	return r.testRunner.Run(ctx, name, args...)
}

func TestVMDHCPModeWithLease(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := db.NewStore(filepath.Join(dir, "db", "vms.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	base, vms := filepath.Join(dir, "images"), filepath.Join(dir, "vms")
	_ = os.MkdirAll(base, 0755)
	_ = os.MkdirAll(vms, 0755)
	_ = os.WriteFile(filepath.Join(base, "vmlinux"), []byte("k"), 0644)
	_ = os.WriteFile(filepath.Join(base, "ubuntu-24.04.ext4"), []byte("r"), 0644)

	runner := &dhcpMockRunner{}
	netMgr, err := network.NewManager(network.DefaultWiredConfig("eth0", "192.168.1.0/24", "192.168.1.1"), runner)
	if err != nil {
		t.Fatal(err)
	}
	storMgr, err := storage.NewManager(storage.Config{
		BaseImagesDir: base,
		VMsDir:        vms,
		AvailableImages: []storage.ImageProfile{
			{Name: "default", Kernel: "vmlinux", Rootfs: "ubuntu-24.04.ext4"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	m := NewManager(Config{FirecrackerBinary: "firecracker"}, store, netMgr, storMgr)

	dhcpTrue := true
	vmDHCP, err := m.CreateVM(ctx, CreateRequest{
		Name: "vm-dhcp-leased",
		DHCP: &dhcpTrue,
	})
	if err != nil {
		t.Fatalf("failed to create DHCP VM with lease: %v", err)
	}
	if vmDHCP.IPAddress != "192.168.1.155" {
		t.Fatalf("expected VM IP to be leased 192.168.1.155, got %s", vmDHCP.IPAddress)
	}
	if vmDHCP.PublicIP != "192.168.1.155" {
		t.Fatalf("expected VM Public IP to be leased 192.168.1.155, got %s", vmDHCP.PublicIP)
	}
	if !vmDHCP.PublicIPEnabled {
		t.Fatalf("expected public IP enabled")
	}
}

func TestVMDeletionProtection(t *testing.T) {
	ctx := context.Background()
	m, store, _, _ := testManager(t)

	prot := true
	vm, err := m.CreateVM(ctx, CreateRequest{
		Name:      "protected-vm",
		Protected: &prot,
	})
	if err != nil {
		t.Fatalf("CreateVM failed: %v", err)
	}
	if !vm.Protected {
		t.Fatalf("expected vm.Protected to be true")
	}

	// Attempt deletion via DeleteVM - must fail
	err = m.DeleteVM(ctx, vm.ID)
	if err == nil || !strings.Contains(err.Error(), "protected against deletion") {
		t.Fatalf("expected deletion to fail due to protection, got: %v", err)
	}

	// Attempt deletion via DeleteVMForOwner - must fail
	err = m.DeleteVMForOwner(ctx, vm.OwnerID, vm.ID)
	if err == nil || !strings.Contains(err.Error(), "protected against deletion") {
		t.Fatalf("expected DeleteVMForOwner to fail due to protection, got: %v", err)
	}

	// Verify VM still exists in DB
	loaded, err := store.GetVM(ctx, vm.ID)
	if err != nil || loaded == nil {
		t.Fatalf("expected VM to still exist in store, got err=%v", err)
	}

	// Unprotect VM via UpdateVMConfig
	unprot := false
	updated, err := m.UpdateVMConfig(ctx, vm.ID, UpdateVMConfigRequest{
		Protected: &unprot,
	})
	if err != nil {
		t.Fatalf("UpdateVMConfig failed: %v", err)
	}
	if updated.Protected {
		t.Fatalf("expected updated.Protected to be false")
	}

	// DeleteVM should now succeed
	if err := m.DeleteVM(ctx, vm.ID); err != nil {
		t.Fatalf("DeleteVM failed after unprotecting: %v", err)
	}

	// Verify VM is deleted
	deleted, err := store.GetVM(ctx, vm.ID)
	if err != nil || deleted != nil {
		t.Fatalf("expected VM to be deleted from store")
	}
}

func TestVMNicknameAssignment(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := testManager(t)

	// Create VM with empty name
	vm1, err := m.CreateVM(ctx, CreateRequest{Name: ""})
	if err != nil {
		t.Fatalf("CreateVM failed: %v", err)
	}
	if vm1.Name == "" || vm1.ID == "" {
		t.Fatalf("expected non-empty name and ID")
	}

	// Create another VM with empty name
	vm2, err := m.CreateVM(ctx, CreateRequest{Name: ""})
	if err != nil {
		t.Fatalf("CreateVM failed: %v", err)
	}
	if vm2.Name == "" || vm2.Name == vm1.Name {
		t.Fatalf("expected distinct nickname, got vm1=%s, vm2=%s", vm1.Name, vm2.Name)
	}
}

func TestVMStatusOutageRecovery(t *testing.T) {
	ctx := context.Background()
	m, store, _, _ := testManager(t)

	// Set process checker so pid 54321 is not alive
	m.SetProcessChecker(func(pid int) bool {
		return pid == 12345 // only 12345 is alive
	})

	vm := &db.VM{
		ID:             "outage-vm",
		Name:           "outage-vm",
		OwnerID:        "default",
		Status:         db.StatusRunning,
		PID:            54321, // dead process
		DesiredRunning: true,
	}
	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatal(err)
	}

	// GetVM should detect dead process and reconcile to StatusStopped
	reconciled, err := m.GetVM(ctx, vm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Status != db.StatusStopped || reconciled.PID != 0 {
		t.Fatalf("expected StatusStopped with PID 0, got status=%s pid=%d", reconciled.Status, reconciled.PID)
	}

	// Mock launcher to restart VM
	var restarted bool
	m.SetLauncher(func(ctx context.Context, v *db.VM) error {
		restarted = true
		v.PID = 12345
		return nil
	})

	// StartDesiredVMs simulates daemon restart outage recovery
	m.StartDesiredVMs(ctx)

	if !restarted {
		t.Fatalf("expected StartDesiredVMs to restart desired running VM after outage")
	}
}

func TestVMNetworkIsolationModesAndAccountReconciliation(t *testing.T) {
	ctx := context.Background()
	m, store, _, runner := testManager(t)

	vmA, err := m.CreateVM(ctx, CreateRequest{Name: "alice-a", OwnerID: "usr_alice"})
	if err != nil {
		t.Fatalf("create vmA: %v", err)
	}
	if vmA.NetworkIsolation != db.NetworkIsolationAccount {
		t.Fatalf("expected default network_isolation=account, got %q", vmA.NetworkIsolation)
	}

	vmB, err := m.CreateVM(ctx, CreateRequest{Name: "alice-b", OwnerID: "usr_alice", NetworkIsolation: "account"})
	if err != nil {
		t.Fatalf("create vmB: %v", err)
	}

	vmC, err := m.CreateVM(ctx, CreateRequest{Name: "alice-org", OwnerID: "org_alice"})
	if err != nil {
		t.Fatalf("create vmC (personal org): %v", err)
	}

	isoTrue := true
	vmD, err := m.CreateVM(ctx, CreateRequest{Name: "alice-iso", OwnerID: "usr_alice", Isolated: &isoTrue})
	if err != nil {
		t.Fatalf("create vmD (isolated): %v", err)
	}
	if vmD.NetworkIsolation != db.NetworkIsolationIsolated {
		t.Fatalf("expected vmD network_isolation=isolated, got %q", vmD.NetworkIsolation)
	}

	vmOther, err := m.CreateVM(ctx, CreateRequest{Name: "bob-a", OwnerID: "usr_bob"})
	if err != nil {
		t.Fatalf("create vmOther: %v", err)
	}

	// Conflicting or invalid values must fail.
	if _, err := m.CreateVM(ctx, CreateRequest{Name: "bad-mode", OwnerID: "usr_alice", NetworkIsolation: "public"}); err == nil {
		t.Fatalf("expected invalid network_isolation error")
	}
	if _, err := m.CreateVM(ctx, CreateRequest{Name: "conflict-mode", OwnerID: "usr_alice", NetworkIsolation: "account", Isolated: &isoTrue}); err == nil {
		t.Fatalf("expected conflicting network_isolation error")
	}

	// Mark all VMs running so reconciliation builds peer allowlists.
	for _, v := range []*db.VM{vmA, vmB, vmC, vmD, vmOther} {
		v.Status = db.StatusRunning
		if err := store.SaveVM(ctx, v); err != nil {
			t.Fatalf("save running vm %s: %v", v.Name, err)
		}
	}

	runner.calls = nil
	m.reconcileAccountNetworkIsolation(ctx, "usr_alice")

	var aAllowsB, aAllowsC, aAllowsD, aAllowsOther bool
	var dIsolatedOn, dHasAccept bool
	for _, call := range runner.calls {
		line := strings.Join(call, " ")
		if strings.Contains(line, fmt.Sprintf("-i %s -o %s -j ACCEPT", vmA.TAPDevice, vmB.TAPDevice)) {
			aAllowsB = true
		}
		if strings.Contains(line, fmt.Sprintf("-i %s -o %s -j ACCEPT", vmA.TAPDevice, vmC.TAPDevice)) {
			aAllowsC = true
		}
		if strings.Contains(line, fmt.Sprintf("-i %s -o %s -j ACCEPT", vmA.TAPDevice, vmD.TAPDevice)) {
			aAllowsD = true
		}
		if strings.Contains(line, fmt.Sprintf("-i %s -o %s -j ACCEPT", vmA.TAPDevice, vmOther.TAPDevice)) {
			aAllowsOther = true
		}
		if strings.Contains(line, fmt.Sprintf("bridge link set dev %s isolated on", vmD.TAPDevice)) {
			dIsolatedOn = true
		}
		if strings.Contains(line, fmt.Sprintf("-i %s", vmD.TAPDevice)) && strings.Contains(line, "-j ACCEPT") && !strings.Contains(line, " -D ") {
			dHasAccept = true
		}
	}
	if !aAllowsB || !aAllowsC {
		t.Errorf("expected vmA to allow vmB (%v) and personal-org vmC (%v)", aAllowsB, aAllowsC)
	}
	if aAllowsD || aAllowsOther {
		t.Errorf("vmA must not allow isolated vmD (%v) or other-tenant vmOther (%v)", aAllowsD, aAllowsOther)
	}
	if !dIsolatedOn || dHasAccept {
		t.Errorf("expected isolated vmD to have bridge isolated on (%v) and no ACCEPT rules (%v)", dIsolatedOn, dHasAccept)
	}

	// Switch vmB to isolated live via UpdateVMConfig.
	runner.calls = nil
	isoMode := db.NetworkIsolationIsolated
	updatedB, err := m.UpdateVMConfig(ctx, vmB.InternalID, UpdateVMConfigRequest{NetworkIsolation: &isoMode})
	if err != nil {
		t.Fatalf("UpdateVMConfig vmB -> isolated: %v", err)
	}
	if updatedB.NetworkIsolation != db.NetworkIsolationIsolated {
		t.Fatalf("expected vmB isolated after update, got %q", updatedB.NetworkIsolation)
	}
	var bIsolatedOn, aStillAllowsB, aStillAllowsC bool
	for _, call := range runner.calls {
		line := strings.Join(call, " ")
		if strings.Contains(line, fmt.Sprintf("bridge link set dev %s isolated on", vmB.TAPDevice)) {
			bIsolatedOn = true
		}
		if strings.Contains(line, fmt.Sprintf("-i %s -o %s -j ACCEPT", vmA.TAPDevice, vmB.TAPDevice)) && !strings.Contains(line, " -D ") {
			aStillAllowsB = true
		}
		if strings.Contains(line, fmt.Sprintf("-i %s -o %s -j ACCEPT", vmA.TAPDevice, vmC.TAPDevice)) && !strings.Contains(line, " -D ") {
			aStillAllowsC = true
		}
	}
	if !bIsolatedOn || aStillAllowsB || !aStillAllowsC {
		t.Errorf("unexpected rules after live isolation of vmB: bIsolatedOn=%v aAllowsB=%v aAllowsC=%v", bIsolatedOn, aStillAllowsB, aStillAllowsC)
	}

	// Clone inherits source VM network isolation when omitted.
	cloneOfD, err := m.CloneVM(ctx, vmD.InternalID, CreateRequest{Name: "alice-iso-clone", OwnerID: "usr_alice"})
	if err != nil {
		t.Fatalf("CloneVM vmD: %v", err)
	}
	if cloneOfD.NetworkIsolation != db.NetworkIsolationIsolated {
		t.Fatalf("expected cloned VM to inherit isolated mode, got %q", cloneOfD.NetworkIsolation)
	}
}
