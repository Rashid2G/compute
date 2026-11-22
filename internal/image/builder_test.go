package image

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/storage"
)

func TestBuilder_BuildImage(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "compute-img-bld-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := db.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	baseImages := filepath.Join(tmpDir, "images")
	vmsDir := filepath.Join(tmpDir, "vms")
	storMgr, err := storage.NewManager(storage.Config{
		BaseImagesDir: baseImages,
		VMsDir:        vmsDir,
	})
	if err != nil {
		t.Fatalf("failed to create storage manager: %v", err)
	}

	builder := NewBuilder(store, storMgr)

	// Mock build function that creates a dummy ext4 file
	var lastReq BuildRequest
	builder.SetBuildFunc(func(ctx context.Context, req BuildRequest, outputPath string, sshPubKey string) error {
		lastReq = req
		return os.WriteFile(outputPath, []byte("built-ext4-with-packages"), 0644)
	})

	ctx := context.Background()

	// 1. Successful build
	req := BuildRequest{
		Name:        "python-redis-stack",
		Description: "Python with Redis pre-installed",
		BaseImage:   "ubuntu-24.04",
		Packages:    []string{"python3", "python3-pip", "redis-server"},
		SizeMB:      2048,
	}

	img, err := builder.BuildImage(ctx, req)
	if err != nil {
		t.Fatalf("BuildImage failed: %v", err)
	}
	if img.Status != db.ImageStatusReady {
		t.Fatalf("expected status ready, got %s", img.Status)
	}
	if len(img.Packages) != 3 || img.Packages[2] != "redis-server" {
		t.Fatalf("unexpected packages: %v", img.Packages)
	}
	if lastReq.Name != "python-redis-stack" {
		t.Fatalf("unexpected build func call req: %+v", lastReq)
	}

	// Verify file was created
	data, err := os.ReadFile(img.RootfsPath)
	if err != nil || string(data) != "built-ext4-with-packages" {
		t.Fatalf("unexpected image content: %s", string(data))
	}

	// 2. Reject duplicate image name
	_, err = builder.BuildImage(ctx, req)
	if err == nil {
		t.Fatalf("expected error on duplicate image name, got nil")
	}

	// 2b. Reject any non-Ubuntu 24.04 base distribution
	for _, base := range []string{"debian", "alpine", "ubuntu-22.04", "fedora"} {
		_, err = builder.BuildImage(ctx, BuildRequest{Name: "other-os-" + base, BaseImage: base})
		if err == nil {
			t.Fatalf("expected error for unsupported base image %q, got nil", base)
		}
	}

	// 3. Failed build test
	builder.SetBuildFunc(func(ctx context.Context, req BuildRequest, outputPath string, sshPubKey string) error {
		return errors.New("apt failed to locate package nonexistent-pkg")
	})

	failedReq := BuildRequest{
		Name:     "bad-image",
		Packages: []string{"nonexistent-pkg"},
	}
	failedImg, err := builder.BuildImage(ctx, failedReq)
	if err == nil {
		t.Fatalf("expected error on failed build, got nil")
	}
	if failedImg == nil || failedImg.Status != db.ImageStatusFailed {
		t.Fatalf("expected image with failed status, got %+v", failedImg)
	}
	if failedImg.ErrorMessage == "" {
		t.Fatalf("expected error message in failed image")
	}
}

func TestRootfsPackagesAndPingConfig(t *testing.T) {
	requiredPkgs := []string{
		"iputils-ping", "bind9-dnsutils", "netcat-openbsd", "traceroute", "iproute2",
		"net-tools", "curl", "wget", "less", "nano", "vim-tiny", "htop", "lsof",
		"psmisc", "file", "unzip", "zip", "tar", "gzip", "xz-utils", "rsync",
		"openssh-server", "openssh-client", "jq", "git", "ca-certificates", "sudo",
		"procps", "util-linux", "bash", "coreutils", "findutils", "grep", "sed",
		"gawk", "hostname", "locales", "libcap2-bin",
	}

	dockerfile := buildDockerfile(BuildRequest{Name: "test-img", Packages: []string{"redis-server"}})
	for _, pkg := range requiredPkgs {
		if !strings.Contains(dockerfile, pkg) {
			t.Fatalf("builder Dockerfile missing required package %q", pkg)
		}
	}
	if !strings.Contains(dockerfile, "redis-server") {
		t.Fatal("builder Dockerfile missing custom package redis-server")
	}
	if !strings.Contains(dockerfile, "chmod u+s /bin/ping") || !strings.Contains(dockerfile, "cap_net_raw+ep") {
		t.Fatal("builder Dockerfile missing SUID/capability configuration for ping")
	}
	if !strings.Contains(dockerfile, "net.ipv4.ping_group_range = 0 2147483647") {
		t.Fatal("builder Dockerfile missing sysctl ping_group_range configuration")
	}

	initScript := defaultInitScript()
	if !strings.Contains(initScript, "ping_group_range") || !strings.Contains(initScript, "0 2147483647") {
		t.Fatal("builder /sbin/init missing ping_group_range configuration")
	}
	if !strings.Contains(initScript, "/sbin/getty") || !strings.Contains(initScript, "/sbin/agetty") {
		t.Fatal("builder /sbin/init should support both /sbin/getty and /sbin/agetty")
	}

	repoRoot := filepath.Join("..", "..")
	for _, rel := range []string{
		"scripts/build-rootfs-chroot.sh",
		"scripts/build-rootfs.sh",
		"scripts/build-base-image.sh",
	} {
		contentBytes, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("failed to read %s: %v", rel, err)
		}
		content := string(contentBytes)
		for _, pkg := range requiredPkgs {
			if !strings.Contains(content, pkg) {
				t.Fatalf("%s missing required package %q", rel, pkg)
			}
		}
		if !strings.Contains(content, "ping_group_range") {
			t.Fatalf("%s missing ping_group_range configuration", rel)
		}
		if !strings.Contains(content, "chmod u+s /bin/ping") {
			t.Fatalf("%s missing chmod u+s /bin/ping", rel)
		}
	}
}

func TestBuilder_CustomBuildCommands(t *testing.T) {
	req := BuildRequest{
		Name:     "node-custom",
		Packages: []string{"nodejs", "npm"},
		Command:  "npm install -g pnpm && pnpm --version",
		Commands: []string{"echo build-stage-1 > /etc/stage"},
	}
	dockerfile := buildDockerfile(req)
	if !strings.Contains(dockerfile, "npm install -g pnpm") {
		t.Fatalf("expected custom command in dockerfile, got: %s", dockerfile)
	}
	if !strings.Contains(dockerfile, "echo build-stage-1") {
		t.Fatalf("expected custom commands array in dockerfile, got: %s", dockerfile)
	}
}

type mockVMRunner struct {
	createdReq  VMCreateRequest
	createdVM   *db.VM
	createErr   error
	ranVMID     string
	runErr      error
	stoppedVMID string
	stopErr     error
	deletedVMID string
	deleteErr   error
	execCalls   []VMExecRequest
	execResps   []*VMExecResponse
	execErrs    []error
	getVM       *db.VM
	getVMErr    error
}

func (m *mockVMRunner) CreateVM(ctx context.Context, req VMCreateRequest) (*db.VM, error) {
	m.createdReq = req
	if m.createErr != nil {
		return nil, m.createErr
	}
	if m.createdVM != nil {
		return m.createdVM, nil
	}
	return &db.VM{ID: "vm-test-builder", DiskPath: "/tmp/mock-disk.ext4"}, nil
}

func (m *mockVMRunner) RunVM(ctx context.Context, vmID string) error {
	m.ranVMID = vmID
	return m.runErr
}

func (m *mockVMRunner) StopVM(ctx context.Context, vmID string) error {
	m.stoppedVMID = vmID
	return m.stopErr
}

func (m *mockVMRunner) DeleteVM(ctx context.Context, vmID string) error {
	m.deletedVMID = vmID
	return m.deleteErr
}

func (m *mockVMRunner) ExecVM(ctx context.Context, vmID string, req VMExecRequest) (*VMExecResponse, error) {
	idx := len(m.execCalls)
	m.execCalls = append(m.execCalls, req)
	if idx < len(m.execErrs) && m.execErrs[idx] != nil {
		return nil, m.execErrs[idx]
	}
	if idx < len(m.execResps) && m.execResps[idx] != nil {
		return m.execResps[idx], nil
	}
	return &VMExecResponse{Stdout: "", Stderr: "", ExitCode: 0}, nil
}

func (m *mockVMRunner) GetVM(ctx context.Context, vmID string) (*db.VM, error) {
	if m.getVMErr != nil {
		return nil, m.getVMErr
	}
	if m.getVM != nil {
		return m.getVM, nil
	}
	if m.createdVM != nil {
		return m.createdVM, nil
	}
	return &db.VM{ID: "vm-test-builder", DiskPath: "/tmp/mock-disk.ext4"}, nil
}

func TestBuilder_FailsClosedWithoutRunner(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := db.NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	storMgr, err := storage.NewManager(storage.Config{
		BaseImagesDir: filepath.Join(tmpDir, "images"),
		VMsDir:        filepath.Join(tmpDir, "vms"),
	})
	if err != nil {
		t.Fatal(err)
	}

	builder := NewBuilder(store, storMgr)
	// Do NOT set a VM runner and do NOT set BuildFunc. Default must fail closed!
	ctx := context.Background()
	req := BuildRequest{
		Name:     "secure-img",
		Packages: []string{"curl"},
	}

	img, err := builder.BuildImage(ctx, req)
	if err == nil {
		t.Fatal("expected build to fail when no microVM runner is configured, but got nil error")
	}
	if !strings.Contains(err.Error(), "no microVM runner configured") {
		t.Fatalf("expected error mentioning no microVM runner configured, got: %v", err)
	}
	if img == nil || img.Status != db.ImageStatusFailed {
		t.Fatalf("expected image with failed status, got: %+v", img)
	}
}

func TestBuilder_MicroVMLifecycle_Success(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := db.NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	storMgr, err := storage.NewManager(storage.Config{
		BaseImagesDir: filepath.Join(tmpDir, "images"),
		VMsDir:        filepath.Join(tmpDir, "vms"),
	})
	if err != nil {
		t.Fatal(err)
	}

	mockDiskDir := filepath.Join(tmpDir, "mock-vm")
	_ = os.MkdirAll(mockDiskDir, 0755)
	mockDiskPath := filepath.Join(mockDiskDir, "rootfs.ext4")
	if err := os.WriteFile(mockDiskPath, []byte("clean-guest-installed-rootfs"), 0644); err != nil {
		t.Fatal(err)
	}

	mockRunner := &mockVMRunner{
		createdVM: &db.VM{ID: "vm-builder-xyz", DiskPath: mockDiskPath},
		getVM:     &db.VM{ID: "vm-builder-xyz", DiskPath: mockDiskPath},
		execResps: []*VMExecResponse{
			{Stdout: "", Stderr: "", ExitCode: 0},                                  // wait ready "true"
			{Stdout: "packages installed successfully\n", Stderr: "", ExitCode: 0}, // build script
		},
	}

	builder := NewBuilder(store, storMgr)
	builder.SetVMRunner(mockRunner)

	ctx := context.Background()
	req := BuildRequest{
		Name:      "python-microvm",
		OwnerID:   "user-123",
		BaseImage: "ubuntu-24.04",
		Packages:  []string{"python3", "python3-pip"},
		Script:    "pip3 install fastapi",
		Command:   "echo complete > /etc/build-info",
		Commands:  []string{"mkdir -p /app"},
		SizeMB:    4096,
	}

	img, err := builder.BuildImage(ctx, req)
	if err != nil {
		t.Fatalf("BuildImage failed: %v", err)
	}

	if img.Status != db.ImageStatusReady {
		t.Fatalf("expected status ready, got %s", img.Status)
	}
	if img.OwnerID != "user-123" {
		t.Fatalf("expected owner user-123, got %s", img.OwnerID)
	}

	// 1. Verify CreateVM parameters
	if mockRunner.createdReq.OwnerID != "user-123" {
		t.Fatalf("expected VM owner user-123, got %s", mockRunner.createdReq.OwnerID)
	}
	if mockRunner.createdReq.Image != "ubuntu-24.04" {
		t.Fatalf("expected base image ubuntu-24.04, got %s", mockRunner.createdReq.Image)
	}
	if mockRunner.createdReq.DiskMB != 4096 {
		t.Fatalf("expected disk 4096, got %d", mockRunner.createdReq.DiskMB)
	}
	if mockRunner.createdReq.VCPUs != 2 || mockRunner.createdReq.MemoryMB != 2048 {
		t.Fatalf("expected 2 VCPUs and 2048 MB, got %d and %d", mockRunner.createdReq.VCPUs, mockRunner.createdReq.MemoryMB)
	}

	// 2. Verify RunVM was invoked
	if mockRunner.ranVMID != "vm-builder-xyz" {
		t.Fatalf("expected RunVM called for vm-builder-xyz, got %s", mockRunner.ranVMID)
	}

	// 3. Verify ExecVM build script contents
	if len(mockRunner.execCalls) < 2 {
		t.Fatalf("expected at least 2 exec calls, got %d", len(mockRunner.execCalls))
	}
	buildCmd := mockRunner.execCalls[1].Command
	if !strings.Contains(buildCmd, "apt-get update -y") || !strings.Contains(buildCmd, "apt-get install -y --no-install-recommends 'python3' 'python3-pip'") {
		t.Fatalf("missing apt-get packages in build script: %s", buildCmd)
	}
	if !strings.Contains(buildCmd, "pip3 install fastapi") {
		t.Fatalf("missing custom script in build script: %s", buildCmd)
	}
	if !strings.Contains(buildCmd, "echo complete > /etc/build-info") {
		t.Fatalf("missing custom command in build script: %s", buildCmd)
	}
	if !strings.Contains(buildCmd, "mkdir -p /app") {
		t.Fatalf("missing custom commands in build script: %s", buildCmd)
	}
	// Verify transient cleanup inside guest
	if !strings.Contains(buildCmd, "apt-get clean") || !strings.Contains(buildCmd, "sync") {
		t.Fatalf("missing transient state cleanup in build script: %s", buildCmd)
	}

	// 4. Verify StopVM was called
	if mockRunner.stoppedVMID != "vm-builder-xyz" {
		t.Fatalf("expected StopVM called for vm-builder-xyz, got %s", mockRunner.stoppedVMID)
	}

	// 5. Verify captured output file
	data, err := os.ReadFile(img.RootfsPath)
	if err != nil || string(data) != "clean-guest-installed-rootfs" {
		t.Fatalf("unexpected captured rootfs content: %v, %s", err, string(data))
	}

	// 6. Verify DeleteVM was called for cleanup
	if mockRunner.deletedVMID != "vm-builder-xyz" {
		t.Fatalf("expected DeleteVM called for cleanup, got %s", mockRunner.deletedVMID)
	}
}

func TestBuilder_MicroVMLifecycle_FailureCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := db.NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	storMgr, err := storage.NewManager(storage.Config{
		BaseImagesDir: filepath.Join(tmpDir, "images"),
		VMsDir:        filepath.Join(tmpDir, "vms"),
	})
	if err != nil {
		t.Fatal(err)
	}

	mockDiskDir := filepath.Join(tmpDir, "mock-vm")
	_ = os.MkdirAll(mockDiskDir, 0755)
	mockDiskPath := filepath.Join(mockDiskDir, "rootfs.ext4")
	_ = os.WriteFile(mockDiskPath, []byte("broken-rootfs"), 0644)

	mockRunner := &mockVMRunner{
		createdVM: &db.VM{ID: "vm-builder-fail", DiskPath: mockDiskPath},
		getVM:     &db.VM{ID: "vm-builder-fail", DiskPath: mockDiskPath},
		execResps: []*VMExecResponse{
			{Stdout: "", Stderr: "", ExitCode: 0},                                     // wait ready
			{Stdout: "", Stderr: "E: Unable to locate package badpkg", ExitCode: 100}, // build fails!
		},
	}

	builder := NewBuilder(store, storMgr)
	builder.SetVMRunner(mockRunner)

	ctx := context.Background()
	req := BuildRequest{
		Name:     "failing-img",
		Packages: []string{"badpkg"},
	}

	img, err := builder.BuildImage(ctx, req)
	if err == nil {
		t.Fatal("expected build failure, got nil error")
	}
	if !strings.Contains(err.Error(), "Unable to locate package badpkg") {
		t.Fatalf("expected error mentioning failure detail, got: %v", err)
	}

	// Verify image marked failed in DB
	if img == nil || img.Status != db.ImageStatusFailed {
		t.Fatalf("expected failed image record, got %+v", img)
	}

	// Verify DeleteVM was STILL called despite failure (guaranteed defer cleanup)
	if mockRunner.deletedVMID != "vm-builder-fail" {
		t.Fatalf("expected DeleteVM cleanup to be invoked on failure, got %s", mockRunner.deletedVMID)
	}

	// Verify output image does not exist
	if _, err := os.Stat(img.RootfsPath); err == nil {
		t.Fatalf("expected failed rootfs to be cleaned up, but it exists at %s", img.RootfsPath)
	}
}

func TestBuilder_InputValidation_PackagesAndSecurity(t *testing.T) {
	// 1. Package validation tests
	validPackages := []string{
		"python3",
		"python3-pip",
		"redis-server",
		"libssl3",
		"ca-certificates",
		"build-essential",
		"g++",
		"pkg.name_123+4",
	}
	for _, pkg := range validPackages {
		if err := ValidatePackageName(pkg); err != nil {
			t.Errorf("expected package %q to be valid, got: %v", pkg, err)
		}
	}

	invalidPackages := []string{
		"; rm -rf /",
		"--option",
		"-f",
		"$(whoami)",
		"`id`",
		"pkg&echo",
		"pkg|cat",
		"pkg>file",
		"pkg<file",
		"pkg newline\n",
		"pkg space",
		"",
		"   ",
	}
	for _, pkg := range invalidPackages {
		if err := ValidatePackageName(pkg); err == nil {
			t.Errorf("expected package %q to be invalid, but passed validation", pkg)
		}
	}

	// 2. ValidateBuildRequest tests
	// 2a. Reject Dockerfile
	err := ValidateBuildRequest(BuildRequest{
		Name:       "docker-test",
		Dockerfile: "FROM ubuntu:24.04\nRUN id",
	})
	if err == nil || !strings.Contains(err.Error(), "raw Dockerfile builds are disabled") {
		t.Fatalf("expected Dockerfile to be rejected for security isolation, got: %v", err)
	}

	// 2b. Reject invalid package in request
	err = ValidateBuildRequest(BuildRequest{
		Name:     "bad-pkg-req",
		Packages: []string{"curl", "; rm -rf /"},
	})
	if err == nil {
		t.Fatal("expected request with malicious package to be rejected, got nil error")
	}

	// 2c. Reject path traversal in image name
	for _, badName := range []string{".", "..", "../bad", "/root", "a/b", "name..ext", "-dashstart"} {
		err = ValidateBuildRequest(BuildRequest{
			Name: badName,
		})
		if err == nil {
			t.Errorf("expected traversal/invalid name %q to be rejected, got nil", badName)
		}
	}
}
