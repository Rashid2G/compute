package image

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
