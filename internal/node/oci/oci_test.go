package oci

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRootfsBuilder_BuildExt4(t *testing.T) {
	if _, err := lookSystemBinary("mkfs.ext4"); err != nil {
		t.Skipf("mkfs.ext4 not available: %v", err)
	}
	tmpDir := t.TempDir()
	rootDir := filepath.Join(tmpDir, "rootfs")
	_ = os.MkdirAll(filepath.Join(rootDir, "bin"), 0755)
	_ = os.WriteFile(filepath.Join(rootDir, "bin", "hello"), []byte("#!/bin/sh\necho hello\n"), 0755)

	// Mock agent binary
	mockAgent := filepath.Join(tmpDir, "mock-agent")
	_ = os.WriteFile(mockAgent, []byte("#!/bin/sh\necho agent\n"), 0755)

	builder := NewRootfsBuilder(mockAgent)
	outputPath := filepath.Join(tmpDir, "output.ext4")

	ctx := context.Background()
	err := builder.BuildExt4(ctx, rootDir, outputPath, 32)
	if err != nil {
		t.Fatalf("BuildExt4 failed: %v", err)
	}

	fi, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("failed to stat output ext4: %v", err)
	}

	// 32MB = 33554432 bytes
	if fi.Size() != 32*1024*1024 {
		t.Errorf("expected size 32MB (%d bytes), got %d", 32*1024*1024, fi.Size())
	}
}
