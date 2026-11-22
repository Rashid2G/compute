package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/image"
)

type serverMockVMRunner struct {
	createdReq  image.VMCreateRequest
	createdVM   *db.VM
	createErr   error
	ranVMID     string
	runErr      error
	stoppedVMID string
	stopErr     error
	deletedVMID string
	deleteErr   error
	execCalls   []image.VMExecRequest
	execResps   []*image.VMExecResponse
	getVM       *db.VM
}

func (m *serverMockVMRunner) CreateVM(ctx context.Context, req image.VMCreateRequest) (*db.VM, error) {
	m.createdReq = req
	if m.createErr != nil {
		return nil, m.createErr
	}
	if m.createdVM != nil {
		return m.createdVM, nil
	}
	return &db.VM{ID: "vm-builder-server-test", DiskPath: "/tmp/mock-server-disk.ext4"}, nil
}

func (m *serverMockVMRunner) RunVM(ctx context.Context, vmID string) error {
	m.ranVMID = vmID
	return m.runErr
}

func (m *serverMockVMRunner) StopVM(ctx context.Context, vmID string) error {
	m.stoppedVMID = vmID
	return m.stopErr
}

func (m *serverMockVMRunner) DeleteVM(ctx context.Context, vmID string) error {
	m.deletedVMID = vmID
	return m.deleteErr
}

func (m *serverMockVMRunner) ExecVM(ctx context.Context, vmID string, req image.VMExecRequest) (*image.VMExecResponse, error) {
	idx := len(m.execCalls)
	m.execCalls = append(m.execCalls, req)
	if idx < len(m.execResps) && m.execResps[idx] != nil {
		return m.execResps[idx], nil
	}
	return &image.VMExecResponse{Stdout: "", Stderr: "", ExitCode: 0}, nil
}

func (m *serverMockVMRunner) GetVM(ctx context.Context, vmID string) (*db.VM, error) {
	if m.getVM != nil {
		return m.getVM, nil
	}
	if m.createdVM != nil {
		return m.createdVM, nil
	}
	return &db.VM{ID: "vm-builder-server-test", DiskPath: "/tmp/mock-server-disk.ext4"}, nil
}

func TestImageBuild_MicroVMIsolation_NonAdminTenantScoping(t *testing.T) {
	env := newMultiTenantServer(t)

	// Create non-admin tenant user alice
	mtSignup(t, env.ts, "alice@example.com", "alice", "AliceSecretPass123!")
	aliceToken, aliceID := mtLogin(t, env.ts, "alice", "AliceSecretPass123!")

	// Mock disk for VM runner
	mockVMDir := filepath.Join(env.root, "mock-vm-alice")
	_ = os.MkdirAll(mockVMDir, 0755)
	mockDisk := filepath.Join(mockVMDir, "rootfs.ext4")
	_ = os.WriteFile(mockDisk, []byte("alice-built-image-content"), 0644)

	mockRunner := &serverMockVMRunner{
		createdVM: &db.VM{ID: "vm-builder-alice", DiskPath: mockDisk},
		getVM:     &db.VM{ID: "vm-builder-alice", DiskPath: mockDisk},
		execResps: []*image.VMExecResponse{
			{Stdout: "", Stderr: "", ExitCode: 0},                            // wait ready
			{Stdout: "built packages successfully", Stderr: "", ExitCode: 0}, // build script
		},
	}

	builder := env.vmMgr.GetImageBuilder()
	builder.SetVMRunner(mockRunner)

	// 1. Non-admin alice attempts to build an image, trying to spoof owner_id as "victim-user"
	buildReq := map[string]interface{}{
		"name":        "alice-custom-toolset",
		"owner_id":    "victim-user", // Should be ignored and forced to aliceID
		"description": "Alice custom toolset",
		"base_image":  "ubuntu-24.04",
		"packages":    []string{"curl", "jq", "git"},
		"script":      "echo alice-build > /etc/alice-build",
		"size_mb":     2048,
	}

	resp, body := mtRequest(t, env.ts, http.MethodPost, "/api/v1/images", aliceToken, buildReq)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", resp.StatusCode, string(body))
	}

	var createdImg db.CustomImage
	if err := json.Unmarshal(body, &createdImg); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify tenant scoping: owner is aliceID, NOT victim-user
	if createdImg.OwnerID != aliceID {
		t.Fatalf("expected image owner %s, got %s (tenant scoping violated!)", aliceID, createdImg.OwnerID)
	}
	if mockRunner.createdReq.OwnerID != aliceID {
		t.Fatalf("expected builder VM owner %s, got %s", aliceID, mockRunner.createdReq.OwnerID)
	}
	if mockRunner.createdReq.Image != "ubuntu-24.04" {
		t.Fatalf("expected base image ubuntu-24.04, got %s", mockRunner.createdReq.Image)
	}

	// Verify lifecycle operations occurred on the VM runner
	if mockRunner.ranVMID != "vm-builder-alice" {
		t.Fatalf("expected RunVM called for vm-builder-alice, got %s", mockRunner.ranVMID)
	}
	if mockRunner.stoppedVMID != "vm-builder-alice" {
		t.Fatalf("expected StopVM called for vm-builder-alice, got %s", mockRunner.stoppedVMID)
	}
	if mockRunner.deletedVMID != "vm-builder-alice" {
		t.Fatalf("expected DeleteVM called for vm-builder-alice, got %s", mockRunner.deletedVMID)
	}

	// Verify output rootfs was captured
	capturedData, err := os.ReadFile(createdImg.RootfsPath)
	if err != nil || string(capturedData) != "alice-built-image-content" {
		t.Fatalf("unexpected captured rootfs data: %v, %s", err, string(capturedData))
	}
}

func TestImageBuild_RejectionOfDockerfileAndMaliciousInput(t *testing.T) {
	env := newMultiTenantServer(t)

	mtSignup(t, env.ts, "bob@example.com", "bob", "BobSecretPass123!")
	bobToken, _ := mtLogin(t, env.ts, "bob", "BobSecretPass123!")

	// 1. Raw Dockerfile build must be rejected with 400 Bad Request
	dockerReq := map[string]interface{}{
		"name":       "docker-exploit",
		"dockerfile": "FROM ubuntu:24.04\nRUN id",
	}
	resp, body := mtRequest(t, env.ts, http.MethodPost, "/api/v1/images", bobToken, dockerReq)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for Dockerfile, got %d: %s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "Dockerfile builds are disabled") && !strings.Contains(string(body), "raw Dockerfile builds are disabled") {
		t.Fatalf("expected error mentioning Dockerfile builds are disabled, got: %s", string(body))
	}

	// 2. Malicious package injection must be rejected with 400 Bad Request
	maliciousPackages := []string{
		"; rm -rf /",
		"--option",
		"$(whoami)",
		"`id`",
		"pkg&reboot",
		"",
	}
	for _, badPkg := range maliciousPackages {
		badReq := map[string]interface{}{
			"name":     "bad-pkg-img",
			"packages": []string{"curl", badPkg},
		}
		resp, body := mtRequest(t, env.ts, http.MethodPost, "/api/v1/images", bobToken, badReq)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for package %q, got %d: %s", badPkg, resp.StatusCode, string(body))
		}
	}

	// 3. Path traversal image names must be rejected with 400 Bad Request
	traversalNames := []string{
		"..",
		".",
		"../traversal",
		"sub/name",
		"-leadingdash",
	}
	for _, badName := range traversalNames {
		badReq := map[string]interface{}{
			"name":     badName,
			"packages": []string{"curl"},
		}
		resp, body := mtRequest(t, env.ts, http.MethodPost, "/api/v1/images", bobToken, badReq)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for image name %q, got %d: %s", badName, resp.StatusCode, string(body))
		}
	}
}
