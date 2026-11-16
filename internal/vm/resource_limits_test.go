package vm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/config"
	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/network"
	"github.com/Rashid2G/compute/internal/storage"
	"github.com/Rashid2G/compute/pkg/api"
)

func newMilestone1TestManager(t *testing.T, limits config.ResourceLimits) (*Manager, *db.Store, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := db.NewStore(filepath.Join(dir, "db", "vms.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	base, vms := filepath.Join(dir, "images"), filepath.Join(dir, "vms")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "ubuntu-24.04.ext4"), []byte("rootfs"), 0o600); err != nil {
		t.Fatal(err)
	}
	stor, err := storage.NewManager(storage.Config{
		BaseImagesDir: base,
		VMsDir:        vms,
		TemplatesDir:  filepath.Join(dir, "templates"),
		DefaultRootfs: "ubuntu-24.04.ext4",
		DefaultKernel: "vmlinux",
		SSHKeyPath:    filepath.Join(base, "key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	netMgr, err := network.NewManager(network.Config{SubnetCIDR: "10.30.0.0/24"}, &testRunner{})
	if err != nil {
		t.Fatal(err)
	}
	cgRoot := filepath.Join(dir, "cgroup")
	m := NewManager(Config{
		CgroupParent: cgRoot,
		Resources:    limits,
	}, store, netMgr, stor)
	return m, store, cgRoot
}

func TestHardDiskLimits_CreateCloneTemplateSaveState(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newMilestone1TestManager(t, config.ResourceLimits{
		MaxTotalDiskMB:     1500,
		MinFreeDiskSpaceMB: 0,
	})

	// 1. CreateVM within limit (1024 MB <= 1500 MB)
	v1, err := m.CreateVM(ctx, CreateRequest{
		Name:     "disk-ok",
		VCPUs:    1,
		MemoryMB: 256,
		DiskMB:   1024,
	})
	if err != nil {
		t.Fatalf("CreateVM within limit failed: %v", err)
	}

	// 2. CreateVM exceeding MaxTotalDiskMB (1024 + 512 > 1500)
	_, err = m.CreateVM(ctx, CreateRequest{
		Name:     "disk-over",
		VCPUs:    1,
		MemoryMB: 256,
		DiskMB:   512,
	})
	var rle *api.ResourceLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("expected *api.ResourceLimitError on CreateVM, got %T: %v", err, err)
	}
	if rle.Code != api.ErrCodeResourceLimitExceeded || rle.Resource != "disk" || rle.LimitType != "hard" {
		t.Fatalf("unexpected ResourceLimitError: %+v", rle)
	}

	// 3. CloneVM exceeding MaxTotalDiskMB
	_, err = m.CloneVM(ctx, v1.ID, CreateRequest{
		Name: "disk-clone-over",
	})
	if !errors.As(err, &rle) || rle.Resource != "disk" {
		t.Fatalf("expected disk *api.ResourceLimitError on CloneVM, got %T: %v", err, err)
	}

	// 4. MinFreeDiskSpaceMB enforcement on SaveState and CreateTemplate
	m.SetResourceLimits(config.ResourceLimits{
		MaxTotalDiskMB:     0,
		MinFreeDiskSpaceMB: 999999999, // Guarantee physical reserve cannot be satisfied
	})

	v1.Status = db.StatusRunning
	v1.PID = 99991
	m.SetProcessChecker(func(pid int) bool { return pid == 99991 })
	m.SetSnapshotter(func(ctx context.Context, vm *db.VM, snapPath, memPath string) error {
		return nil
	})
	if err := m.saveVMRecord(ctx, v1); err != nil {
		t.Fatal(err)
	}

	err = m.SaveState(ctx, v1.ID)
	if !errors.As(err, &rle) || rle.Resource != "disk" || rle.LimitType != "hard" {
		t.Fatalf("expected disk *api.ResourceLimitError on SaveState, got %T: %v", err, err)
	}

	_, err = m.CreateTemplate(ctx, CreateTemplateRequest{
		Name: "tpl-over-reserve",
		VMID: v1.ID,
	})
	if !errors.As(err, &rle) || rle.Resource != "disk" || rle.LimitType != "hard" {
		t.Fatalf("expected disk *api.ResourceLimitError on CreateTemplate, got %T: %v", err, err)
	}
}

func TestCPUOvercommit_SoftAndHardLimits(t *testing.T) {
	ctx := context.Background()
	m, store, cgRoot := newMilestone1TestManager(t, config.ResourceLimits{
		MaxTotalVCPUs:      2,
		MaxTotalMemoryMB:   8192,
		MinFreeDiskSpaceMB: 0,
	})

	if err := store.SaveSchedulingPolicy(ctx, db.ClusterSchedulingPolicy{
		CPUSoftOvercommitRatio: 1.0,
		CPUHardOvercommitRatio: 2.0,
		CPUShareWeightBase:     100,
		MemoryHardGuarantee:    true,
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	alivePIDs := map[int]bool{}
	nextPID := 40000
	m.SetProcessChecker(func(pid int) bool {
		mu.Lock()
		defer mu.Unlock()
		return alivePIDs[pid]
	})
	m.SetLauncher(func(ctx context.Context, vm *db.VM) error {
		mu.Lock()
		nextPID++
		pid := nextPID
		alivePIDs[pid] = true
		mu.Unlock()
		vm.PID = pid
		vm.Status = db.StatusRunning
		vm.DesiredRunning = true
		return nil
	})

	createAndRun := func(name string, vcpus int) (*db.VM, error) {
		v, err := m.CreateVM(ctx, CreateRequest{
			Name:     name,
			VCPUs:    vcpus,
			MemoryMB: 256,
			DiskMB:   256,
		})
		if err != nil {
			return nil, err
		}
		return v, m.RunVM(ctx, v.ID)
	}

	// VM1: 2/2 base vCPUs -> weight = 100
	vm1, err := m.CreateVM(ctx, CreateRequest{
		Name:     "cpu-vm-1",
		VCPUs:    2,
		MemoryMB: 256,
		DiskMB:   256,
	})
	if err != nil {
		t.Fatalf("create vm1 failed: %v", err)
	}
	w1, q1, err := m.evaluatePlacement(ctx, vm1, true)
	if err != nil || q1 != "" || w1 != 100 {
		t.Fatalf("expected vm1 evaluatePlacement weight=100, got weight=%d queue=%q err=%v", w1, q1, err)
	}
	if err := m.RunVM(ctx, vm1.ID); err != nil {
		t.Fatalf("vm1 RunVM failed: %v", err)
	}

	// VM2: +2 vCPUs = 4/2 (2.0x hard ceiling, soft overcommit active) -> weight = (100*2)/4 = 50
	vm2, err := m.CreateVM(ctx, CreateRequest{
		Name:     "cpu-vm-2",
		VCPUs:    2,
		MemoryMB: 256,
		DiskMB:   256,
	})
	if err != nil {
		t.Fatalf("create vm2 failed: %v", err)
	}
	w2, q2, err := m.evaluatePlacement(ctx, vm2, true)
	if err != nil || q2 != "" || w2 != 50 {
		t.Fatalf("expected vm2 evaluatePlacement weight=50, got weight=%d queue=%q err=%v", w2, q2, err)
	}
	if _, err := m.cgroupMgr.Setup(vm2.ID, CgroupLimits{VCPUs: vm2.VCPUs, CPUWeight: w2, MemoryMB: vm2.MemoryMB}); err != nil {
		t.Fatalf("cgroup Setup failed: %v", err)
	}
	w2Bytes, err := os.ReadFile(filepath.Join(cgRoot, vm2.ID, "cpu.weight"))
	if err != nil {
		t.Fatalf("failed reading vm2 cpu.weight: %v", err)
	}
	if strings.TrimSpace(string(w2Bytes)) != "50" {
		t.Fatalf("expected vm2 cpu.weight=50 under 2.0x overcommit, got %q", strings.TrimSpace(string(w2Bytes)))
	}
	if err := m.RunVM(ctx, vm2.ID); err != nil {
		t.Fatalf("vm2 RunVM within 2.0x hard overcommit failed: %v", err)
	}

	// VM3: +1 vCPU = 5 > 4 hard limit -> rejected with *api.ResourceLimitError
	_, err = createAndRun("cpu-vm-3", 1)
	var rle *api.ResourceLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("expected *api.ResourceLimitError for hard CPU limit, got %T: %v", err, err)
	}
	if rle.Resource != "cpu" || rle.LimitType != "hard" || rle.Limit != 4 {
		t.Fatalf("unexpected CPU ResourceLimitError: %+v", rle)
	}
}

func TestMemoryHardGuarantee_AndIdleRAMReclaim(t *testing.T) {
	ctx := context.Background()
	m, store, _ := newMilestone1TestManager(t, config.ResourceLimits{
		MaxTotalVCPUs:      16,
		MaxTotalMemoryMB:   1024,
		MinFreeDiskSpaceMB: 0,
	})

	if err := store.SaveSchedulingPolicy(ctx, db.ClusterSchedulingPolicy{
		CPUSoftOvercommitRatio: 1.0,
		CPUHardOvercommitRatio: 2.0,
		CPUShareWeightBase:     100,
		MemoryHardGuarantee:    true,
		IdleRAMSnapshotEnabled: true,
		IdleThresholdSeconds:   60,
		AutoReclaimOnPressure:  true,
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	alivePIDs := map[int]bool{}
	nextPID := 50000
	m.SetProcessChecker(func(pid int) bool {
		mu.Lock()
		defer mu.Unlock()
		return alivePIDs[pid]
	})
	m.SetLauncher(func(ctx context.Context, vm *db.VM) error {
		mu.Lock()
		nextPID++
		pid := nextPID
		alivePIDs[pid] = true
		mu.Unlock()
		vm.PID = pid
		vm.Status = db.StatusRunning
		vm.DesiredRunning = true
		return nil
	})
	m.SetSnapshotter(func(ctx context.Context, vm *db.VM, snapPath, memPath string) error {
		mu.Lock()
		delete(alivePIDs, vm.PID)
		mu.Unlock()
		if err := os.WriteFile(snapPath, []byte("snap"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(memPath, []byte("mem"), 0o600)
	})

	makeRunning := func(id, name string, memMB int, protected bool, idleAgo time.Duration) *db.VM {
		v, err := m.CreateVM(ctx, CreateRequest{
			Name:      name,
			VCPUs:     1,
			MemoryMB:  memMB,
			DiskMB:    256,
			Protected: &protected,
		})
		if err != nil {
			t.Fatalf("CreateVM(%s) failed: %v", name, err)
		}
		if id != "" && id != v.ID {
			// Also test job-* ID exemption by saving under explicit ID
			_ = m.deleteVMRecord(ctx, v.ID)
			v.ID = id
			v.InternalID = id
			if err := m.saveVMRecord(ctx, v); err != nil {
				t.Fatalf("saveVMRecord(%s) failed: %v", id, err)
			}
		}
		if err := m.RunVM(ctx, v.ID); err != nil {
			t.Fatalf("RunVM(%s) failed: %v", name, err)
		}
		loaded, _ := m.GetVM(ctx, v.ID)
		past := time.Now().UTC().Add(-idleAgo)
		loaded.LastActivityAt = &past
		loaded.UpdatedAt = past
		if err := m.saveVMRecord(ctx, loaded); err != nil {
			t.Fatal(err)
		}
		return loaded
	}

	// Fill 1024 MB total:
	// - vmIdle (512 MB, idle 180s -> eligible for reclaim)
	// - vmProtected (256 MB, Protected=true, idle 180s -> MUST be skipped)
	// - vmJob (256 MB, ID="job-ci-1", idle 180s -> MUST be skipped)
	vmIdle := makeRunning("", "idle-reclaimable", 512, false, 180*time.Second)
	vmProtected := makeRunning("", "protected-vm", 256, true, 180*time.Second)
	vmJob := makeRunning("job-ci-1", "job-ci-1", 256, false, 180*time.Second)

	// Start vmNew (512 MB): triggers RAM pressure reclaim, auto-hibernating vmIdle to SSD
	vmNew, err := m.CreateVM(ctx, CreateRequest{
		Name:     "new-vm",
		VCPUs:    1,
		MemoryMB: 512,
		DiskMB:   256,
	})
	if err != nil {
		t.Fatalf("CreateVM(new-vm) failed: %v", err)
	}
	if err := m.RunVM(ctx, vmNew.ID); err != nil {
		t.Fatalf("RunVM(new-vm) should have succeeded after reclaiming idle VM: %v", err)
	}

	reclaimedCheck, _ := m.GetVM(ctx, vmIdle.ID)
	if reclaimedCheck.Status != db.StatusPaused || !reclaimedCheck.AutoHibernated || reclaimedCheck.HibernateReason != "idle_memory_reclaim" {
		t.Fatalf("expected vmIdle to be auto-hibernated, got status=%s auto=%v reason=%q",
			reclaimedCheck.Status, reclaimedCheck.AutoHibernated, reclaimedCheck.HibernateReason)
	}

	protCheck, _ := m.GetVM(ctx, vmProtected.ID)
	if protCheck.Status != db.StatusRunning {
		t.Fatalf("expected protected VM to remain running, got %s", protCheck.Status)
	}
	jobCheck, _ := m.GetVM(ctx, vmJob.ID)
	if jobCheck.Status != db.StatusRunning {
		t.Fatalf("expected job-* VM to remain running, got %s", jobCheck.Status)
	}

	// Verify TouchVMActivity updates last_activity_at on running VM
	beforeTouch := time.Now().UTC().Add(-time.Second)
	m.TouchVMActivity(ctx, vmNew.ID)
	touched, _ := m.GetVM(ctx, vmNew.ID)
	if touched.LastActivityAt == nil || touched.LastActivityAt.Before(beforeTouch) {
		t.Fatalf("expected TouchVMActivity to update LastActivityAt, got %v", touched.LastActivityAt)
	}

	// Now 1024 MB is held by vmProtected(256) + vmJob(256) + vmNew(512 active), none reclaimable.
	// Attempting another 512 MB VM must fail with hard memory ResourceLimitError.
	vmOver, err := m.CreateVM(ctx, CreateRequest{
		Name:     "mem-over",
		VCPUs:    1,
		MemoryMB: 512,
		DiskMB:   256,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = m.RunVM(ctx, vmOver.ID)
	var rle *api.ResourceLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("expected *api.ResourceLimitError for memory guarantee, got %T: %v", err, err)
	}
	if rle.Resource != "memory" || rle.LimitType != "hard" || rle.Limit != 1024 {
		t.Fatalf("unexpected memory ResourceLimitError: %+v", rle)
	}
}

func TestAdmissionTOCTOU_ConcurrentRunVMAndCreateVM(t *testing.T) {
	t.Run("cpu_hard_overcommit_race", func(t *testing.T) {
		ctx := context.Background()
		m, store, _ := newMilestone1TestManager(t, config.ResourceLimits{
			MaxTotalVCPUs:      1,
			MaxTotalMemoryMB:   8192,
			MinFreeDiskSpaceMB: 0,
		})
		if err := store.SaveSchedulingPolicy(ctx, db.ClusterSchedulingPolicy{
			CPUSoftOvercommitRatio: 1.0,
			CPUHardOvercommitRatio: 2.0, // hard limit = 2 vCPUs
			CPUShareWeightBase:     100,
			MemoryHardGuarantee:    true,
		}); err != nil {
			t.Fatal(err)
		}

		var mu sync.Mutex
		alivePIDs := map[int]bool{}
		nextPID := 70000
		m.SetProcessChecker(func(pid int) bool {
			mu.Lock()
			defer mu.Unlock()
			return alivePIDs[pid]
		})
		m.SetLauncher(func(ctx context.Context, v *db.VM) error {
			time.Sleep(15 * time.Millisecond)
			mu.Lock()
			nextPID++
			pid := nextPID
			alivePIDs[pid] = true
			mu.Unlock()
			v.PID = pid
			v.Status = db.StatusRunning
			v.DesiredRunning = true
			return nil
		})

		v1, err := m.CreateVM(ctx, CreateRequest{Name: "cpu-race-1", VCPUs: 2, MemoryMB: 256, DiskMB: 256})
		if err != nil {
			t.Fatal(err)
		}
		v2, err := m.CreateVM(ctx, CreateRequest{Name: "cpu-race-2", VCPUs: 2, MemoryMB: 256, DiskMB: 256})
		if err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, id := range []string{v1.ID, v2.ID} {
			wg.Add(1)
			go func(vmID string) {
				defer wg.Done()
				<-start
				errs <- m.RunVM(ctx, vmID)
			}(id)
		}
		close(start)
		wg.Wait()
		close(errs)

		var successes, limitErrors int
		for err := range errs {
			if err == nil {
				successes++
				continue
			}
			var rle *api.ResourceLimitError
			if errors.As(err, &rle) && rle.Resource == "cpu" && rle.LimitType == "hard" {
				limitErrors++
				continue
			}
			t.Fatalf("unexpected RunVM error: %v", err)
		}
		if successes != 1 || limitErrors != 1 {
			t.Fatalf("expected exactly 1 success and 1 CPU hard limit error, got successes=%d limitErrors=%d", successes, limitErrors)
		}
	})

	t.Run("memory_hard_guarantee_race", func(t *testing.T) {
		ctx := context.Background()
		m, store, _ := newMilestone1TestManager(t, config.ResourceLimits{
			MaxTotalVCPUs:      16,
			MaxTotalMemoryMB:   512,
			MinFreeDiskSpaceMB: 0,
		})
		if err := store.SaveSchedulingPolicy(ctx, db.ClusterSchedulingPolicy{
			CPUSoftOvercommitRatio: 1.0,
			CPUHardOvercommitRatio: 2.0,
			CPUShareWeightBase:     100,
			MemoryHardGuarantee:    true,
			AutoReclaimOnPressure:  false,
		}); err != nil {
			t.Fatal(err)
		}

		var mu sync.Mutex
		alivePIDs := map[int]bool{}
		nextPID := 71000
		m.SetProcessChecker(func(pid int) bool {
			mu.Lock()
			defer mu.Unlock()
			return alivePIDs[pid]
		})
		m.SetLauncher(func(ctx context.Context, v *db.VM) error {
			time.Sleep(15 * time.Millisecond)
			mu.Lock()
			nextPID++
			pid := nextPID
			alivePIDs[pid] = true
			mu.Unlock()
			v.PID = pid
			v.Status = db.StatusRunning
			v.DesiredRunning = true
			return nil
		})

		v1, err := m.CreateVM(ctx, CreateRequest{Name: "mem-race-1", VCPUs: 1, MemoryMB: 512, DiskMB: 256})
		if err != nil {
			t.Fatal(err)
		}
		v2, err := m.CreateVM(ctx, CreateRequest{Name: "mem-race-2", VCPUs: 1, MemoryMB: 512, DiskMB: 256})
		if err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, id := range []string{v1.ID, v2.ID} {
			wg.Add(1)
			go func(vmID string) {
				defer wg.Done()
				<-start
				errs <- m.RunVM(ctx, vmID)
			}(id)
		}
		close(start)
		wg.Wait()
		close(errs)

		var successes, limitErrors int
		for err := range errs {
			if err == nil {
				successes++
				continue
			}
			var rle *api.ResourceLimitError
			if errors.As(err, &rle) && rle.Resource == "memory" && rle.LimitType == "hard" {
				limitErrors++
				continue
			}
			t.Fatalf("unexpected RunVM error: %v", err)
		}
		if successes != 1 || limitErrors != 1 {
			t.Fatalf("expected exactly 1 success and 1 memory hard limit error, got successes=%d limitErrors=%d", successes, limitErrors)
		}
	})

	t.Run("concurrent_create_disk_limit_race", func(t *testing.T) {
		ctx := context.Background()
		m, _, _ := newMilestone1TestManager(t, config.ResourceLimits{
			MaxTotalDiskMB:     1500,
			MinFreeDiskSpaceMB: 0,
		})

		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for i, name := range []string{"disk-race-1", "disk-race-2"} {
			wg.Add(1)
			go func(idx int, vmName string) {
				defer wg.Done()
				<-start
				_, err := m.CreateVM(ctx, CreateRequest{
					Name:     vmName,
					VCPUs:    1,
					MemoryMB: 256,
					DiskMB:   1024,
				})
				errs <- err
			}(i, name)
		}
		close(start)
		wg.Wait()
		close(errs)

		var successes, limitErrors int
		for err := range errs {
			if err == nil {
				successes++
				continue
			}
			var rle *api.ResourceLimitError
			if errors.As(err, &rle) && rle.Resource == "disk" && rle.LimitType == "hard" {
				limitErrors++
				continue
			}
			t.Fatalf("unexpected CreateVM error: %v", err)
		}
		if successes != 1 || limitErrors != 1 {
			t.Fatalf("expected exactly 1 CreateVM success and 1 disk hard limit error, got successes=%d limitErrors=%d", successes, limitErrors)
		}
	})
}

func TestTemplateAndBuildImageDiskLimits(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newMilestone1TestManager(t, config.ResourceLimits{
		MaxTotalDiskMB:     1500,
		MinFreeDiskSpaceMB: 0,
	})
	m.GetImageBuilder().SetBuildFunc(func(ctx context.Context, req BuildImageRequest, outputPath string, sshPubKey string) error {
		return os.WriteFile(outputPath, []byte("custom-img"), 0o600)
	})

	v1, err := m.CreateVM(ctx, CreateRequest{
		Name:     "tpl-src",
		VCPUs:    1,
		MemoryMB: 256,
		DiskMB:   1024,
	})
	if err != nil {
		t.Fatal(err)
	}

	// CreateTemplate needs 1024 MB logical (1024 + 1024 > 1500) -> rejected
	_, err = m.CreateTemplate(ctx, CreateTemplateRequest{
		Name: "tpl-over",
		VMID: v1.ID,
	})
	var rle *api.ResourceLimitError
	if !errors.As(err, &rle) || rle.Resource != "disk" || rle.LimitType != "hard" {
		t.Fatalf("expected disk *api.ResourceLimitError on CreateTemplate, got %T: %v", err, err)
	}

	// Raise limit to 2048 MB: CreateTemplate succeeds, then template counts toward allocated logical disk
	m.SetResourceLimits(config.ResourceLimits{
		MaxTotalDiskMB:     2048,
		MinFreeDiskSpaceMB: 0,
	})
	tpl, err := m.CreateTemplate(ctx, CreateTemplateRequest{
		Name: "tpl-fit",
		VMID: v1.ID,
	})
	if err != nil {
		t.Fatalf("CreateTemplate within 2048 MB failed: %v", err)
	}
	if tpl.DiskMB != 1024 {
		t.Fatalf("expected template DiskMB=1024, got %d", tpl.DiskMB)
	}

	// Now 1024 (v1) + 1024 (tpl) = 2048 MB allocated; creating a 512 MB VM must fail
	_, err = m.CreateVM(ctx, CreateRequest{
		Name:     "after-tpl-over",
		VCPUs:    1,
		MemoryMB: 256,
		DiskMB:   512,
	})
	if !errors.As(err, &rle) || rle.Resource != "disk" {
		t.Fatalf("expected template usage to count against MaxTotalDiskMB, got %T: %v", err, err)
	}

	// BuildImage default size (2048 MB) exceeds remaining limit -> rejected
	_, err = m.BuildImage(ctx, BuildImageRequest{
		Name: "img-over",
	})
	if !errors.As(err, &rle) || rle.Resource != "disk" || rle.LimitType != "hard" {
		t.Fatalf("expected disk *api.ResourceLimitError on BuildImage, got %T: %v", err, err)
	}
}

func TestMigration_PathTraversalRejected(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newMilestone1TestManager(t, config.ResourceLimits{
		MaxTotalDiskMB: 4096,
	})

	badIDs := []string{
		"",
		".",
		"..",
		"../escape",
		"../../etc/passwd",
		"vm/nested",
		`vm\nested`,
		"a..b",
	}
	for _, bad := range badIDs {
		if _, err := m.ImportVMArtifactChunk(ctx, api.WorkerVMImportDiskRequest{
			VMID:      bad,
			Kind:      "disk",
			Offset:    0,
			TotalSize: 4,
			Data:      "AAAA",
		}); err == nil {
			t.Fatalf("expected ImportVMArtifactChunk to reject VMID %q", bad)
		}
		if _, err := m.ExportVMArtifactChunk(ctx, bad, "disk", 0, 1024); err == nil {
			t.Fatalf("expected ExportVMArtifactChunk to reject VMID %q", bad)
		}
	}

	// Valid VMID with traversal in embedded VM metadata InternalID/ID must also be rejected.
	if _, err := m.ImportVMArtifactChunk(ctx, api.WorkerVMImportDiskRequest{
		VMID:      "safe-vm-1",
		Kind:      "disk",
		Offset:    0,
		TotalSize: 4,
		VM:        []byte(`{"id":"safe-vm-1","internal_id":"../../outside"}`),
	}); err == nil {
		t.Fatalf("expected ImportVMArtifactChunk to reject traversal in VM.InternalID")
	}

	// Verify DiskPath outside VMsDir is rejected by ArtifactPath/ExportVMArtifactChunk.
	outsideFile := filepath.Join(t.TempDir(), "secret.ext4")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.saveVMRecord(ctx, &db.VM{
		ID:         "vm-escape",
		InternalID: "vm-escape",
		Name:       "vm-escape",
		OwnerID:    "default",
		DiskPath:   outsideFile,
		Status:     db.StatusStopped,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ExportVMArtifactChunk(ctx, "vm-escape", "disk", 0, 1024); err == nil {
		t.Fatalf("expected ExportVMArtifactChunk to reject DiskPath outside VMsDir")
	}
}
