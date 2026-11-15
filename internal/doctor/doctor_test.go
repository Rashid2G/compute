package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/image"
)

func writeFakeImages(t *testing.T, dir string) {
	t.Helper()
	kernel := make([]byte, 256)
	copy(kernel, "\x7fELF")
	if err := os.WriteFile(filepath.Join(dir, "vmlinux"), kernel, 0644); err != nil {
		t.Fatal(err)
	}
	rootfs := make([]byte, 4096)
	rootfs[1024+56] = 0x53
	rootfs[1024+57] = 0xEF
	if err := os.WriteFile(filepath.Join(dir, "ubuntu-24.04.ext4"), rootfs, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestImageChecks(t *testing.T) {
	dir := t.TempDir()
	writeFakeImages(t, dir)

	if c := checkKernel(dir, "vmlinux"); !c.OK {
		t.Fatalf("kernel check failed: %+v", c)
	}
	if c := checkRootfs(dir, "ubuntu-24.04.ext4"); !c.OK {
		t.Fatalf("rootfs check failed: %+v", c)
	}
	if c := checkKernel(dir, "missing"); c.OK || c.Remediation == "" {
		t.Fatalf("expected failed kernel check with remediation, got %+v", c)
	}

	if err := os.WriteFile(filepath.Join(dir, "bad.ext4"), []byte("nope"), 0644); err != nil {
		t.Fatal(err)
	}
	if c := checkRootfs(dir, "bad.ext4"); c.OK {
		t.Fatalf("expected failed rootfs check: %+v", c)
	}
}

func TestSSHKeyCheck(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "ssh", "id_ed25519")
	writeFakeImages(t, dir)
	if _, err := image.Bootstrap(context.Background(), image.BootstrapOptions{
		Mode: image.ModePrebuilt, ImagesDir: dir, SSHKeyPath: key, CurrentArch: "amd64",
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if c := checkSSHKey(key); !c.OK {
		t.Fatalf("ssh key check failed: %+v", c)
	}
	if err := os.Chmod(key, 0644); err != nil {
		t.Fatal(err)
	}
	if c := checkSSHKey(key); c.OK || c.Severity != Warning {
		t.Fatalf("expected permission warning, got %+v", c)
	}
	if c := checkSSHKey(filepath.Join(dir, "missing")); c.OK || c.Remediation == "" {
		t.Fatalf("expected missing key failure, got %+v", c)
	}
}

func TestDatabaseWritable(t *testing.T) {
	dir := t.TempDir()
	if c := checkDBWritable(filepath.Join(dir, "data", "compute.db")); !c.OK {
		t.Fatalf("db check failed: %+v", c)
	}
}

func TestReportAggregation(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.DBPath = filepath.Join(t.TempDir(), "db", "compute.db")
	cfg.Images.ImagesDir = t.TempDir()
	report := Preflight(cfg)
	if report.OK {
		t.Fatal("expected unready report for empty images dir")
	}
	found := false
	for _, c := range report.Failures() {
		if c.Name == "rootfs" || c.Name == "kernel" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected image failure in %+v", report.Failures())
	}
}

func TestHostNetworkCheck(t *testing.T) {
	t.Setenv("COMPUTE_HOST_NETWORK", "1")
	if c := checkHostNetwork(); !c.OK {
		t.Fatalf("host network check failed: %+v", c)
	}
}
