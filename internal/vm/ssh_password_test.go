package vm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSSHPassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  string
	}{
		{name: "empty disables password auth"},
		{name: "valid", password: "correct horse battery"},
		{name: "too short", password: "short", wantErr: "at least 8 characters"},
		{name: "too few multibyte characters", password: "密码密码", wantErr: "at least 8 characters"},
		{name: "too long", password: strings.Repeat("a", 73), wantErr: "at most 72 bytes"},
		{name: "newline", password: "longenough\nvalue", wantErr: "newline"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSSHPassword(tc.password)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSSHPassword(%q) = %v, want nil", tc.password, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateSSHPassword(%q) = %v, want error containing %q", tc.password, err, tc.wantErr)
			}
		})
	}
}

func TestHasExtFilesystem(t *testing.T) {
	dir := t.TempDir()

	plain := filepath.Join(dir, "plain.img")
	if err := os.WriteFile(plain, []byte("not an ext filesystem"), 0600); err != nil {
		t.Fatal(err)
	}
	if hasExtFilesystem(plain) {
		t.Fatal("plain file reported as ext filesystem")
	}

	ext := filepath.Join(dir, "disk.ext4")
	data := make([]byte, 2048)
	// ext superblock magic 0xEF53 at offset 1080 (little-endian).
	data[1080], data[1081] = 0x53, 0xEF
	if err := os.WriteFile(ext, data, 0600); err != nil {
		t.Fatal(err)
	}
	if !hasExtFilesystem(ext) {
		t.Fatal("ext filesystem magic not detected")
	}
}

func makeTestExt4Disk(t *testing.T, diskPath string) {
	t.Helper()
	if out, err := exec.Command("truncate", "-s", "32M", diskPath).CombinedOutput(); err != nil {
		t.Fatalf("truncate: %v: %s", err, out)
	}
	if out, err := exec.Command("mkfs.ext4", "-F", "-q", diskPath).CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v: %s", err, out)
	}
	writeGuestFileForTest(t, diskPath, "/etc/passwd", "root:x:0:0:root:/root:/bin/bash\ncompute:x:1000:1000::/home/compute:/bin/bash\n")
	writeGuestFileForTest(t, diskPath, "/etc/shadow", "root:$2a$10$old:19000:0:99999:7:::\ncompute:$2a$10$old:19000:0:99999:7:::\n")
	writeGuestFileForTest(t, diskPath, "/etc/ssh/sshd_config", "PasswordAuthentication yes\nPubkeyAuthentication yes\nUsePAM no\n")
	writeGuestFileForTest(t, diskPath, "/root/.ssh/authorized_keys", "ssh-ed25519 OLDKEY old@example\n")
}

func TestSSHPassword_OptInAuthAndLockOnCloneAndTemplate(t *testing.T) {
	for _, tool := range []string{"truncate", "mkfs.ext4", "debugfs"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
	}

	m, store, stor, _ := testManager(t)
	ctx := context.Background()

	// Make the base image a valid ext4 image
	baseImage := filepath.Join(stor.GetConfig().BaseImagesDir, "ubuntu-24.04.ext4")
	makeTestExt4Disk(t, baseImage)

	// 1. Opt-in SSH password auth: Create VM with SSHPassword set
	srcVM, err := m.CreateVM(ctx, CreateRequest{
		Name:        "src-with-pass",
		SSHPassword: "Password123!",
	})
	if err != nil {
		t.Fatalf("CreateVM with SSHPassword: %v", err)
	}
	if !srcVM.SSHPasswordEnabled {
		t.Fatalf("srcVM.SSHPasswordEnabled = false, want true")
	}

	// Verify in DB
	loadedSrc, err := store.GetVM(ctx, srcVM.ID)
	if err != nil {
		t.Fatalf("GetVM: %v", err)
	}
	if !loadedSrc.SSHPasswordEnabled {
		t.Fatalf("loadedSrc.SSHPasswordEnabled = false, want true")
	}

	// Verify disk has PasswordAuthentication yes and drop-in
	sshdSrc := readGuestFileForTest(t, srcVM.DiskPath, "/etc/ssh/sshd_config")
	if !strings.Contains(sshdSrc, "PasswordAuthentication yes") {
		t.Fatalf("srcVM sshd_config missing PasswordAuthentication yes: %s", sshdSrc)
	}

	// 2. Public-key-only rejection / default: Create VM without SSHPassword
	keyOnlyVM, err := m.CreateVM(ctx, CreateRequest{
		Name: "key-only-vm",
	})
	if err != nil {
		t.Fatalf("CreateVM without SSHPassword: %v", err)
	}
	if keyOnlyVM.SSHPasswordEnabled {
		t.Fatalf("keyOnlyVM.SSHPasswordEnabled = true, want false")
	}
	loadedKeyOnly, err := store.GetVM(ctx, keyOnlyVM.ID)
	if err != nil {
		t.Fatalf("GetVM: %v", err)
	}
	if loadedKeyOnly.SSHPasswordEnabled {
		t.Fatalf("loadedKeyOnly.SSHPasswordEnabled = true, want false")
	}

	// 3. Lock-on-clone: Clone from srcVM without specifying SSHPassword
	clonedVM, err := m.CloneVM(ctx, srcVM.ID, CreateRequest{
		Name: "cloned-vm",
	})
	if err != nil {
		t.Fatalf("CloneVM: %v", err)
	}
	if clonedVM.SSHPasswordEnabled {
		t.Fatalf("clonedVM.SSHPasswordEnabled = true, want false (lock-on-clone)")
	}
	loadedClone, err := store.GetVM(ctx, clonedVM.ID)
	if err != nil {
		t.Fatalf("GetVM: %v", err)
	}
	if loadedClone.SSHPasswordEnabled {
		t.Fatalf("loadedClone.SSHPasswordEnabled = true, want false")
	}

	// 4. Lock-on-template: Create template from srcVM
	tpl, err := m.CreateTemplate(ctx, CreateTemplateRequest{
		Name: "src-template",
		VMID: srcVM.ID,
	})
	if err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	if tpl.DiskPath == "" {
		t.Fatalf("tpl.DiskPath is empty")
	}

	// Launch VM from template without SSHPassword
	tplVM, err := m.CreateVM(ctx, CreateRequest{
		Name:     "vm-from-tpl",
		Template: tpl.Name,
	})
	if err != nil {
		t.Fatalf("CreateVM from template: %v", err)
	}
	if tplVM.SSHPasswordEnabled {
		t.Fatalf("tplVM.SSHPasswordEnabled = true, want false")
	}
}
