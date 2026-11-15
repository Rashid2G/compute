package vm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/db"
)

func TestGlobalSSHKeysAndStoppedSandboxSync(t *testing.T) {
	for _, tool := range []string{"truncate", "mkfs.ext4", "debugfs"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
	}

	m, store, stor, _ := testManager(t)
	ctx := context.Background()

	// Deterministic internal automation key.
	sshDir := filepath.Join(stor.GetConfig().BaseImagesDir, "ssh")
	if err := os.MkdirAll(sshDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519.pub"), []byte("ssh-ed25519 INTERNAL internal@daemon\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	disk := filepath.Join(dir, "guest.ext4")
	if out, err := exec.Command("truncate", "-s", "32M", disk).CombinedOutput(); err != nil {
		t.Fatalf("truncate: %v: %s", err, out)
	}
	if out, err := exec.Command("mkfs.ext4", "-F", "-q", disk).CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v: %s", err, out)
	}
	writeGuestFileForTest(t, disk, "/etc/shadow", "root:$2a$10$existing:19000:0:99999:7:::\ncompute:$2a$10$existing:19000:0:99999:7:::\n")
	writeGuestFileForTest(t, disk, "/etc/ssh/sshd_config", "PasswordAuthentication yes\n")

	vm := &db.VM{
		ID:           "stopped-vm",
		InternalID:   "ins_stopped",
		OwnerID:      "default",
		Name:         "stopped-vm",
		Status:       db.StatusStopped,
		VCPUs:        1,
		MemoryMB:     512,
		DiskMB:       32,
		DiskPath:     disk,
		SSHPublicKey: "ssh-ed25519 PERSANDBOX per@example",
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := store.SaveVM(ctx, vm); err != nil {
		t.Fatal(err)
	}

	if err := store.SaveSSHKey(ctx, &db.SSHKey{
		ID: "ssh_global", OwnerID: "default", Name: "global",
		PublicKey: "ssh-ed25519 GLOBAL global@example", Fingerprint: "SHA256:global", AllSandboxes: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSSHKey(ctx, &db.SSHKey{
		ID: "ssh_other", OwnerID: "default", Name: "other",
		PublicKey: "ssh-ed25519 OTHER other@example", Fingerprint: "SHA256:other",
	}); err != nil {
		t.Fatal(err)
	}

	globals, err := m.GlobalSSHKeys(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(globals) != 1 || !strings.Contains(globals[0], "GLOBAL") {
		t.Fatalf("GlobalSSHKeys = %v, want only the account-wide key", globals)
	}

	if err := m.SyncSSHKeysForVM(ctx, vm.ID); err != nil {
		t.Fatalf("SyncSSHKeysForVM: %v", err)
	}

	for _, path := range []string{"/root/.ssh/authorized_keys", "/home/compute/.ssh/authorized_keys"} {
		keys := readGuestFileForTest(t, disk, path)
		for _, want := range []string{"INTERNAL", "GLOBAL", "PERSANDBOX"} {
			if !strings.Contains(keys, want) {
				t.Fatalf("%s missing %s:\n%s", path, want, keys)
			}
		}
		if strings.Contains(keys, "OTHER") {
			t.Fatalf("%s includes a key that is not account-wide:\n%s", path, keys)
		}
	}
	shadow := readGuestFileForTest(t, disk, "/etc/shadow")
	if !strings.Contains(shadow, "root:$2a$10$existing") {
		t.Fatalf("password policy changed during key sync:\n%s", shadow)
	}

	// Disabling the account-wide key removes it from the sandbox on the next
	// reconciliation pass.
	key, _ := store.GetSSHKey(ctx, "ssh_global")
	key.AllSandboxes = false
	if err := store.SaveSSHKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := m.SyncSSHKeysForVM(ctx, vm.ID); err != nil {
		t.Fatal(err)
	}
	keys := readGuestFileForTest(t, disk, "/root/.ssh/authorized_keys")
	if strings.Contains(keys, "GLOBAL") {
		t.Fatalf("disabled account-wide key was not removed:\n%s", keys)
	}
	if !strings.Contains(keys, "PERSANDBOX") || !strings.Contains(keys, "INTERNAL") {
		t.Fatalf("per-sandbox or internal key was removed:\n%s", keys)
	}
}

func writeGuestFileForTest(t *testing.T, disk, path, content string) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "content")
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(path)
	var mkdir strings.Builder
	current := ""
	for _, part := range strings.Split(strings.Trim(parent, "/"), "/") {
		if part == "" {
			continue
		}
		current += "/" + part
		mkdir.WriteString("mkdir " + current + "\n")
	}
	script := mkdir.String() + "rm " + path + "\nwrite " + tmp + " " + path + "\n"
	scriptFile := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(scriptFile, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("debugfs", "-w", "-f", scriptFile, disk).CombinedOutput(); err != nil {
		t.Fatalf("debugfs write %s: %v: %s", path, err, out)
	}
}

func readGuestFileForTest(t *testing.T, disk, path string) string {
	t.Helper()
	out, err := exec.Command("debugfs", "-R", "cat "+path, disk).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs read %s: %v: %s", path, err, out)
	}
	return string(out)
}
