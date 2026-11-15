package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeKernel() []byte {
	b := make([]byte, 256)
	copy(b, "\x7fELF")
	return b
}

func fakeRootfs() []byte {
	b := make([]byte, 4096)
	b[1024+56] = 0x53
	b[1024+57] = 0xEF
	return b
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestBootstrapDownloadAndIdempotency(t *testing.T) {
	kernel := fakeKernel()
	rootfs := fakeRootfs()
	var requests, artifactRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/manifest.json" {
			artifactRequests++
		}
		switch r.URL.Path {
		case "/manifest.json":
			_ = json.NewEncoder(w).Encode(Manifest{
				SchemaVersion: 1,
				Version:       "test-1",
				Arch:          "amd64",
				Kernel:        ManifestArtifact{URL: "http://" + r.Host + "/vmlinux", SHA256: sum(kernel), Size: int64(len(kernel))},
				Rootfs:        ManifestArtifact{URL: "http://" + r.Host + "/rootfs.ext4", SHA256: sum(rootfs), Size: int64(len(rootfs))},
			})
		case "/vmlinux":
			_, _ = w.Write(kernel)
		case "/rootfs.ext4":
			_, _ = w.Write(rootfs)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	opts := BootstrapOptions{
		Mode:        ModeDownload,
		ManifestURL: srv.URL + "/manifest.json",
		ImagesDir:   dir,
		CurrentArch: "amd64",
	}
	res, err := Bootstrap(context.Background(), opts)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if len(res.Downloaded) != 2 {
		t.Fatalf("downloaded = %v", res.Downloaded)
	}
	if !res.GeneratedKey {
		t.Fatal("expected generated SSH key")
	}
	info, err := os.Stat(filepath.Join(dir, "ssh", "id_ed25519"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("key mode = %o", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dir, ".installed.json")); err != nil {
		t.Fatalf("state file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vmlinux.part")); !os.IsNotExist(err) {
		t.Fatalf("partial download left behind: %v", err)
	}

	before := requests
	beforeArtifacts := artifactRequests
	res2, err := Bootstrap(context.Background(), opts)
	if err != nil {
		t.Fatalf("second Bootstrap: %v", err)
	}
	if len(res2.Downloaded) != 0 {
		t.Fatalf("second run downloaded %v", res2.Downloaded)
	}
	if res2.GeneratedKey {
		t.Fatal("second run regenerated key")
	}
	if artifactRequests != beforeArtifacts {
		t.Fatalf("second run re-downloaded %d artifacts", artifactRequests-beforeArtifacts)
	}
	if requests != before+1 {
		t.Fatalf("second run made %d HTTP requests (expected only the manifest)", requests-before)
	}
}

func TestBootstrapChecksumMismatch(t *testing.T) {
	kernel := fakeKernel()
	rootfs := fakeRootfs()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_ = json.NewEncoder(w).Encode(Manifest{
				SchemaVersion: 1,
				Version:       "bad",
				Arch:          "amd64",
				Kernel:        ManifestArtifact{URL: "http://" + r.Host + "/vmlinux", SHA256: strings.Repeat("0", 64)},
				Rootfs:        ManifestArtifact{URL: "http://" + r.Host + "/rootfs.ext4", SHA256: sum(rootfs)},
			})
		case "/vmlinux":
			_, _ = w.Write(kernel)
		case "/rootfs.ext4":
			_, _ = w.Write(rootfs)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	_, err := Bootstrap(context.Background(), BootstrapOptions{
		Mode:        ModeDownload,
		ManifestURL: srv.URL + "/manifest.json",
		ImagesDir:   dir,
		CurrentArch: "amd64",
	})
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("expected checksum error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "vmlinux")); !os.IsNotExist(statErr) {
		t.Fatalf("bad artifact was installed: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "vmlinux.part")); !os.IsNotExist(statErr) {
		t.Fatalf("partial file left behind: %v", statErr)
	}
}

func TestBootstrapDownloadRequiresManifestURL(t *testing.T) {
	_, err := Bootstrap(context.Background(), BootstrapOptions{Mode: ModeDownload, ImagesDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "COMPUTE_IMAGE_MANIFEST_URL") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBootstrapPrebuiltMissingGivesRemediation(t *testing.T) {
	_, err := Bootstrap(context.Background(), BootstrapOptions{Mode: ModeBuild, ImagesDir: t.TempDir(), CurrentArch: "amd64"})
	if err == nil || !strings.Contains(err.Error(), "scripts/build-rootfs.sh") {
		t.Fatalf("expected build remediation, got %v", err)
	}
}

func TestBootstrapRejectsNonExt4Rootfs(t *testing.T) {
	dir := t.TempDir()
	kernel := fakeKernel()
	if err := os.WriteFile(filepath.Join(dir, "vmlinux"), kernel, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ubuntu-24.04.ext4"), []byte("not an ext4 image"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Bootstrap(context.Background(), BootstrapOptions{Mode: ModePrebuilt, ImagesDir: dir, CurrentArch: "amd64"})
	if err == nil || !strings.Contains(err.Error(), "ext4") {
		t.Fatalf("expected ext4 validation error, got %v", err)
	}
}

func TestManifestArchAliases(t *testing.T) {
	for _, tc := range []struct{ manifest, host string }{
		{"x86_64", "amd64"}, {"amd64", "amd64"}, {"aarch64", "arm64"}, {"arm64", "arm64"},
	} {
		if !archMatches(tc.manifest, tc.host) {
			t.Errorf("archMatches(%q,%q)=false", tc.manifest, tc.host)
		}
	}
	if archMatches("amd64", "arm64") {
		t.Error("amd64 should not match arm64")
	}
}

func TestManifestChecksumValidation(t *testing.T) {
	for _, bad := range []string{"", "abc", strings.Repeat("z", 64)} {
		if err := validateChecksum(bad); err == nil {
			t.Errorf("checksum %q accepted", bad)
		}
	}
	if err := validateChecksum(sum([]byte("ok"))); err != nil {
		t.Errorf("valid checksum rejected: %v", err)
	}
}

func TestLoadState(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadState(dir); !os.IsNotExist(err) {
		t.Fatalf("expected not exist, got %v", err)
	}
	if err := writeState(dir, State{SchemaVersion: 1, Mode: ModeDownload, Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "v1" || st.Mode != ModeDownload {
		t.Fatalf("state = %+v", st)
	}
	_ = fmt.Sprintf("%v", st)
}

func TestBootstrapAutoInstallsKernelRootfsAndIsIdempotent(t *testing.T) {
	kernel := fakeKernel()
	rootfs := fakeRootfs()
	var kernelRequests, rootfsBuilds int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/vmlinux" {
			kernelRequests++
			_, _ = w.Write(kernel)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	dir := t.TempDir()
	opts := BootstrapOptions{
		ImagesDir:    dir,
		KernelURL:    srv.URL + "/vmlinux",
		KernelSHA256: sum(kernel),
		CurrentArch:  "amd64",
		BuildRootfs: func(ctx context.Context, o BootstrapOptions, dstPath string) error {
			rootfsBuilds++
			return os.WriteFile(dstPath, rootfs, 0644)
		},
	}

	res, err := Bootstrap(context.Background(), opts)
	if err != nil {
		t.Fatalf("Bootstrap(auto): %v", err)
	}
	if res.State.Mode != ModeAuto {
		t.Fatalf("mode = %q, want %q", res.State.Mode, ModeAuto)
	}
	if len(res.Downloaded) != 2 || kernelRequests != 1 || rootfsBuilds != 1 {
		t.Fatalf("downloaded=%v kernelRequests=%d rootfsBuilds=%d", res.Downloaded, kernelRequests, rootfsBuilds)
	}
	if !res.GeneratedKey {
		t.Fatal("expected generated SSH key")
	}
	aliasTarget, err := os.Readlink(filepath.Join(dir, "ubuntu-24.ext4"))
	if err != nil || aliasTarget != "ubuntu-24.04.ext4" {
		t.Fatalf("alias symlink = %q, err=%v", aliasTarget, err)
	}

	res2, err := Bootstrap(context.Background(), opts)
	if err != nil {
		t.Fatalf("second Bootstrap(auto): %v", err)
	}
	if len(res2.Downloaded) != 0 || kernelRequests != 1 || rootfsBuilds != 1 {
		t.Fatalf("second run was not idempotent: downloaded=%v kernelRequests=%d rootfsBuilds=%d", res2.Downloaded, kernelRequests, rootfsBuilds)
	}
	if res2.GeneratedKey {
		t.Fatal("second run regenerated SSH key")
	}
}

func TestBootstrapAutoKernelSrcAndValidation(t *testing.T) {
	kernel := fakeKernel()
	srcDir := t.TempDir()
	srcKernel := filepath.Join(srcDir, "vmlinux-custom")
	if err := os.WriteFile(srcKernel, kernel, 0644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	res, err := Bootstrap(context.Background(), BootstrapOptions{
		Mode:         ModeAuto,
		ImagesDir:    dir,
		KernelSrc:    srcKernel,
		KernelSHA256: sum(kernel),
		CurrentArch:  "amd64",
		BuildRootfs: func(ctx context.Context, o BootstrapOptions, dstPath string) error {
			return os.WriteFile(dstPath, fakeRootfs(), 0644)
		},
	})
	if err != nil {
		t.Fatalf("Bootstrap(auto, KernelSrc): %v", err)
	}
	if len(res.Downloaded) != 2 {
		t.Fatalf("downloaded = %v", res.Downloaded)
	}

	// Custom KernelURL requires KernelSHA256.
	_, err = Bootstrap(context.Background(), BootstrapOptions{
		Mode:        ModeAuto,
		ImagesDir:   t.TempDir(),
		KernelURL:   "https://example.com/vmlinux",
		CurrentArch: "amd64",
		BuildRootfs: func(ctx context.Context, o BootstrapOptions, dstPath string) error {
			return os.WriteFile(dstPath, fakeRootfs(), 0644)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "COMPUTE_KERNEL_SHA256") {
		t.Fatalf("expected COMPUTE_KERNEL_SHA256 error, got %v", err)
	}
}
