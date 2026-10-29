package vm

import (
	"context"
	"errors"
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
	return nil, errors.New("mock command")
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
	vm, err := m.CreateVM(ctx, CreateRequest{Name: "test", VCPUs: 2, MemoryMB: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(vm.ID, "vm-") || len(vm.ID) != 11 {
		t.Fatalf("unexpected generated ID %q", vm.ID)
	}
	if vm.IPAddress == "" || vm.MACAddress == "" {
		t.Fatalf("network allocation missing: %+v", vm)
	}
	if _, err := os.Stat(vm.DiskPath); err != nil {
		t.Fatalf("persistent disk not initialized: %v", err)
	}
	got, err := store.GetVM(ctx, vm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != vm.ID || got.DiskPath != vm.DiskPath {
		t.Fatalf("VM not persisted: %+v", got)
	}
}

func TestGetAndListVMs(t *testing.T) {
	ctx := context.Background()
	m, store, _, _ := testManager(t)
	if got, err := m.GetVM(ctx, "missing"); err != nil || got != nil {
		t.Fatalf("missing GetVM = %v, %v", got, err)
	}
	vm := &db.VM{ID: "vm-existing", Name: "existing", Status: db.StatusCreated, VCPUs: 1, MemoryMB: 512, IPAddress: "10.20.0.2", MACAddress: "02:00:00:00:00:02", TAPDevice: "tap", RootfsPath: "root", KernelPath: "kernel", DiskPath: "disk"}
	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatal(err)
	}
	got, err := m.GetVM(ctx, vm.ID)
	if err != nil || got == nil || got.Name != vm.Name {
		t.Fatalf("GetVM = %+v, %v", got, err)
	}
	list, err := m.ListVMs(ctx)
	if err != nil || len(list) != 1 || list[0].ID != vm.ID {
		t.Fatalf("ListVMs = %+v, %v", list, err)
	}
}

func TestDeleteVMCleansStorageAndDatabase(t *testing.T) {
	ctx := context.Background()
	m, store, stor, _ := testManager(t)
	vm, err := m.CreateVM(ctx, CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteVM(ctx, vm.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stor.GetVMDir(vm.ID)); !os.IsNotExist(err) {
		t.Fatalf("VM storage remains: %v", err)
	}
	got, err := store.GetVM(ctx, vm.ID)
	if err != nil || got != nil {
		t.Fatalf("DB record remains: %+v, %v", got, err)
	}
}

func TestStopVMStoppedAndMissingProcess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status db.VMStatus
		pid    int
	}{{"already stopped", db.StatusStopped, 0}, {"missing process", db.StatusRunning, 99999999}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m, store, _, runner := testManager(t)
			vm := &db.VM{ID: "vm-test", Name: "test", Status: tc.status, VCPUs: 1, MemoryMB: 512, TAPDevice: "tap-test", RootfsPath: "root", KernelPath: "kernel", DiskPath: "disk", PID: tc.pid}
			if err := store.SaveVM(ctx, vm); err != nil {
				t.Fatal(err)
			}
			if err := m.StopVM(ctx, vm.ID); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetVM(ctx, vm.ID)
			if err != nil || got.Status != db.StatusStopped || got.PID != 0 {
				t.Fatalf("unexpected stopped VM %+v (%v)", got, err)
			}
			if len(runner.calls) == 0 || !strings.Contains(strings.Join(runner.calls[len(runner.calls)-1], " "), "tap-test") {
				t.Fatalf("TAP cleanup not called: %v", runner.calls)
			}
		})
	}
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
