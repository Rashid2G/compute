package vm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/storage"
)

type testRunner struct{ calls [][]string }

func (r *testRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	cmdStr := strings.Join(append([]string{name}, args...), " ")
	if strings.Contains(cmdStr, "ip -o link show") {
		return []byte("1: lo: <LOOPBACK>\n10: mftap-dead: <BROADCAST>\n"), nil
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
	if err := os.WriteFile(filepath.Join(base, "rootfs.ext4"), []byte("rootfs"), 0600); err != nil {
		t.Fatal(err)
	}
	stor, err := storage.NewManager(storage.Config{BaseImagesDir: base, VMsDir: vms, DefaultRootfs: "rootfs.ext4", DefaultKernel: "vmlinux", SSHKeyPath: filepath.Join(base, "key")})
	if err != nil {
		t.Fatal(err)
	}
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
