package image

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/storage"
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
