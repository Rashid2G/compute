package storage

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestUpdateShadowUsers(t *testing.T) {
	shadow := []byte("root:oldroot:19000:0:99999:7:::\ncompute:oldcompute:19000:0:99999:7:::\ndaemon:*:19000:0:99999:7:::\n")

	locked, matched := updateShadowUsers(shadow, []string{"compute", "root"}, "")
	if matched != 2 {
		t.Fatalf("matched = %d, want 2", matched)
	}
	if !bytes.Contains(locked, []byte("root:!:19000")) || !bytes.Contains(locked, []byte("compute:!:19000")) {
		t.Fatalf("accounts were not locked:\n%s", locked)
	}
	if !bytes.Contains(locked, []byte("daemon:*:19000")) {
		t.Fatalf("unmanaged account was modified:\n%s", locked)
	}

	hashed, matched := updateShadowUsers(shadow, []string{"compute"}, "$2a$10$example")
	if matched != 1 {
		t.Fatalf("matched = %d, want 1", matched)
	}
	if !bytes.Contains(hashed, []byte("compute:$2a$10$example:19000")) {
		t.Fatalf("password was not applied:\n%s", hashed)
	}
	if !bytes.Contains(hashed, []byte("root:oldroot:19000")) {
		t.Fatalf("root account was unexpectedly modified:\n%s", hashed)
	}
}

func TestUpdateSSHDConfig(t *testing.T) {
	config := []byte(strings.Join([]string{
		"Include /etc/ssh/sshd_config.d/*.conf",
		"#PasswordAuthentication yes",
		"PasswordAuthentication yes",
		"PubkeyAuthentication yes",
		"Match User restricted",
		"    PasswordAuthentication yes",
		"",
	}, "\n"))

	disabled := string(updateSSHDConfig(config, false))
	if !strings.Contains(disabled, "\nPasswordAuthentication no\n") {
		t.Fatalf("global PasswordAuthentication not disabled:\n%s", disabled)
	}
	if !strings.Contains(disabled, "    PasswordAuthentication yes") {
		t.Fatalf("Match block must keep its own PasswordAuthentication:\n%s", disabled)
	}
	if strings.Contains(disabled, "\nPasswordAuthentication yes\n") {
		t.Fatalf("global PasswordAuthentication still enabled:\n%s", disabled)
	}
	if !strings.Contains(disabled, "KbdInteractiveAuthentication no") {
		t.Fatalf("keyboard-interactive auth not disabled:\n%s", disabled)
	}

	missing := []byte("Port 22\nMatch User restricted\n    PasswordAuthentication yes\n")
	updated := string(updateSSHDConfig(missing, true))
	if !strings.Contains(updated, "PasswordAuthentication yes\nKbdInteractiveAuthentication no") {
		t.Fatalf("missing directives were not inserted before Match:\n%s", updated)
	}
	if strings.Index(updated, "PasswordAuthentication yes") > strings.Index(updated, "Match User") {
		t.Fatalf("directives inserted inside Match block:\n%s", updated)
	}
}

func TestConfigureSSHAccessDebugfs(t *testing.T) {
	for _, tool := range []string{"truncate", "mkfs.ext4", "debugfs"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
	}

	dir := t.TempDir()
	disk := filepath.Join(dir, "rootfs.ext4")
	if out, err := exec.Command("truncate", "-s", "32M", disk).CombinedOutput(); err != nil {
		t.Fatalf("truncate: %v: %s", err, out)
	}
	if out, err := exec.Command("mkfs.ext4", "-F", "-q", disk).CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v: %s", err, out)
	}

	writeGuestFile(t, disk, "/etc/passwd", "root:x:0:0:root:/root:/bin/bash\ncompute:x:1000:1000::/home/compute:/bin/bash\n")
	writeGuestFile(t, disk, "/etc/shadow", "root:oldroot:19000:0:99999:7:::\ncompute:oldcompute:19000:0:99999:7:::\ndaemon:*:19000:0:99999:7:::\n")
	writeGuestFile(t, disk, "/etc/ssh/sshd_config", "#PasswordAuthentication yes\nPasswordAuthentication yes\nPubkeyAuthentication yes\nUsePAM no\n")
	writeGuestFile(t, disk, "/root/.ssh/authorized_keys", "ssh-ed25519 OLDKEY old@example\n")

	m, err := NewManager(Config{BaseImagesDir: dir, VMsDir: filepath.Join(dir, "vms"), TemplatesDir: filepath.Join(dir, "templates"), SSHKeyPath: filepath.Join(dir, "missing-key")})
	if err != nil {
		t.Fatal(err)
	}

	const password = "hunter2-with-salt"
	if err := m.ConfigureSSHAccess(disk, SSHAccess{
		PublicKey: "ssh-ed25519 NEWKEY new@example",
		Password:  password,
	}); err != nil {
		t.Fatalf("ConfigureSSHAccess: %v", err)
	}

	shadow := readGuestFile(t, disk, "/etc/shadow")
	computeHash := shadowField(t, shadow, "compute", 1)
	if computeHash == "!" || computeHash == "oldcompute" {
		t.Fatalf("compute password was not replaced: %q", computeHash)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(computeHash), []byte(password)); err != nil {
		t.Fatalf("compute password hash does not match: %v", err)
	}
	if got := shadowField(t, shadow, "root", 1); got == "oldroot" || got == "!" {
		t.Fatalf("root password was not replaced: %q", got)
	}
	if got := shadowField(t, shadow, "daemon", 1); got != "*" {
		t.Fatalf("unmanaged account changed: %q", got)
	}

	keys := readGuestFile(t, disk, "/root/.ssh/authorized_keys")
	if !strings.Contains(keys, "NEWKEY") || strings.Contains(keys, "OLDKEY") {
		t.Fatalf("authorized_keys were not replaced:\n%s", keys)
	}
	if got := readGuestFile(t, disk, "/home/compute/.ssh/authorized_keys"); !strings.Contains(got, "NEWKEY") {
		t.Fatalf("compute authorized_keys missing new key:\n%s", got)
	}

	sshd := readGuestFile(t, disk, "/etc/ssh/sshd_config")
	if !strings.Contains(sshd, "PasswordAuthentication yes") {
		t.Fatalf("password auth not enabled:\n%s", sshd)
	}
	if !strings.Contains(sshd, "KbdInteractiveAuthentication no") {
		t.Fatalf("keyboard-interactive auth not disabled:\n%s", sshd)
	}
	dropIn := readGuestFile(t, disk, sshAccessDropInPath)
	if !strings.Contains(dropIn, "PasswordAuthentication yes") {
		t.Fatalf("drop-in does not enable password auth:\n%s", dropIn)
	}

	// Re-applying without a password must disable password auth and install an
	// unguessable placeholder password. The accounts must NOT be locked: OpenSSH
	// 9.6 with UsePAM=no rejects locked accounts even for public-key auth.
	if err := m.ConfigureSSHAccess(disk, SSHAccess{PublicKey: "ssh-ed25519 NEWKEY new@example"}); err != nil {
		t.Fatalf("ConfigureSSHAccess (passwordless): %v", err)
	}
	shadow = readGuestFile(t, disk, "/etc/shadow")
	for _, user := range []string{"compute", "root"} {
		got := shadowField(t, shadow, user, 1)
		if got == "!" || got == "" || got == "oldroot" || got == "oldcompute" {
			t.Fatalf("%s account is locked or empty: %q", user, got)
		}
		if !strings.HasPrefix(got, "$2") {
			t.Fatalf("%s shadow entry is not a bcrypt hash: %q", user, got)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(got), []byte("")); err == nil {
			t.Fatalf("%s accepts an empty password", user)
		}
	}
	sshd = readGuestFile(t, disk, "/etc/ssh/sshd_config")
	if !strings.Contains(sshd, "PasswordAuthentication no") {
		t.Fatalf("password auth not disabled:\n%s", sshd)
	}
}

func TestConfigureAuthorizedKeysPreservesPasswordPolicy(t *testing.T) {
	for _, tool := range []string{"truncate", "mkfs.ext4", "debugfs"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
	}

	dir := t.TempDir()
	disk := filepath.Join(dir, "rootfs.ext4")
	if out, err := exec.Command("truncate", "-s", "32M", disk).CombinedOutput(); err != nil {
		t.Fatalf("truncate: %v: %s", err, out)
	}
	if out, err := exec.Command("mkfs.ext4", "-F", "-q", disk).CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v: %s", err, out)
	}

	writeGuestFile(t, disk, "/etc/shadow", "root:$2a$10$existing:19000:0:99999:7:::\ncompute:$2a$10$existing:19000:0:99999:7:::\n")
	writeGuestFile(t, disk, "/etc/ssh/sshd_config", "PasswordAuthentication yes\n")
	writeGuestFile(t, disk, "/root/.ssh/authorized_keys", "ssh-ed25519 OLDKEY old@example\n")

	m, err := NewManager(Config{BaseImagesDir: dir, VMsDir: filepath.Join(dir, "vms"), TemplatesDir: filepath.Join(dir, "templates"), SSHKeyPath: filepath.Join(dir, "missing-key")})
	if err != nil {
		t.Fatal(err)
	}

	content := m.AuthorizedKeys("ssh-ed25519 NEWKEY1 one@example", "ssh-ed25519 NEWKEY2 two@example", "ssh-ed25519 NEWKEY1 one@example")
	if strings.Count(content, "NEWKEY1") != 1 || !strings.Contains(content, "NEWKEY2") {
		t.Fatalf("AuthorizedKeys did not dedupe keys:\n%s", content)
	}

	if err := m.ConfigureAuthorizedKeys(disk, "ssh-ed25519 NEWKEY1 one@example", "ssh-ed25519 NEWKEY2 two@example"); err != nil {
		t.Fatalf("ConfigureAuthorizedKeys: %v", err)
	}
	for _, path := range []string{"/root/.ssh/authorized_keys", "/home/compute/.ssh/authorized_keys"} {
		keys := readGuestFile(t, disk, path)
		if !strings.Contains(keys, "NEWKEY1") || !strings.Contains(keys, "NEWKEY2") || strings.Contains(keys, "OLDKEY") {
			t.Fatalf("%s was not replaced:\n%s", path, keys)
		}
	}
	shadow := readGuestFile(t, disk, "/etc/shadow")
	if !strings.Contains(shadow, "root:$2a$10$existing") || !strings.Contains(shadow, "compute:$2a$10$existing") {
		t.Fatalf("password policy changed while updating keys:\n%s", shadow)
	}
	sshd := readGuestFile(t, disk, "/etc/ssh/sshd_config")
	if !strings.Contains(sshd, "PasswordAuthentication yes") {
		t.Fatalf("sshd policy changed while updating keys:\n%s", sshd)
	}
}

func writeGuestFile(t *testing.T, disk, path, content string) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "content")
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(path)
	var mkdir strings.Builder
	current := ""
	for _, part := range strings.Split(strings.Trim(parent, "/"), "/") {
		current += "/" + part
		fmt.Fprintf(&mkdir, "mkdir %s\n", current)
	}
	script := mkdir.String() + fmt.Sprintf("rm %s\nwrite %s %s\nset_inode_field %s mode 0100600\n", path, tmp, path, path)
	scriptFile := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(scriptFile, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("debugfs", "-w", "-f", scriptFile, disk).CombinedOutput(); err != nil {
		t.Fatalf("debugfs write %s: %v: %s", path, err, out)
	}
}

func readGuestFile(t *testing.T, disk, path string) string {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "dump")
	if out, err := exec.Command("debugfs", "-R", fmt.Sprintf("dump %s %s", path, tmp), disk).CombinedOutput(); err != nil {
		t.Fatalf("debugfs dump %s: %v: %s", path, err, out)
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("read dumped %s: %v", path, err)
	}
	return string(data)
}

func shadowField(t *testing.T, shadow, user string, index int) string {
	t.Helper()
	for _, line := range strings.Split(shadow, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > index && fields[0] == user {
			return fields[index]
		}
	}
	t.Fatalf("user %q not found in shadow:\n%s", user, shadow)
	return ""
}
