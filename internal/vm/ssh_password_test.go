package vm

import (
	"os"
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
