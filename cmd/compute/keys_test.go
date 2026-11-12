package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKVMQHURyGt9mXytbJpOeTM4z4jIIhwMQ+aeXq/vDwAj test@client"

func TestValidatePublicKey(t *testing.T) {
	if err := validatePublicKey(testPublicKey); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	privateKey := "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----"
	if err := validatePublicKey(privateKey); err == nil {
		t.Fatal("expected private key to be rejected")
	}
	if err := validatePublicKey("not-a-key"); err == nil {
		t.Fatal("expected garbage to be rejected")
	}
}

func TestReadPublicKeyFromFile(t *testing.T) {
	dir := t.TempDir()
	pub := filepath.Join(dir, "id_ed25519.pub")
	if err := os.WriteFile(pub, []byte(testPublicKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPublicKey(nil, pub)
	if err != nil {
		t.Fatalf("readPublicKey: %v", err)
	}
	if got != testPublicKey {
		t.Fatalf("readPublicKey = %q, want %q", got, testPublicKey)
	}

	priv := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(priv, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPublicKey(nil, priv); err == nil {
		t.Fatal("expected private key file to be rejected")
	}
}

func TestReadPublicKeyLiteral(t *testing.T) {
	got, err := readPublicKey([]string{testPublicKey}, "")
	if err != nil {
		t.Fatalf("readPublicKey literal: %v", err)
	}
	if got != testPublicKey {
		t.Fatalf("readPublicKey literal = %q, want %q", got, testPublicKey)
	}
}

func TestReorderKeyFlags(t *testing.T) {
	valueFlags := map[string]bool{"name": true, "key-file": true}
	got := reorderKeyFlags(
		[]string{"laptop", "--no-all", "--name", "new-name", "--key-file", "/tmp/k.pub"},
		valueFlags,
	)
	want := []string{"--no-all", "--name", "new-name", "--key-file", "/tmp/k.pub", "laptop"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reorderKeyFlags = %v, want %v", got, want)
	}

	// A flag value that looks like a positional must stay attached to its flag.
	got = reorderKeyFlags([]string{"--name", "laptop", "target"}, valueFlags)
	want = []string{"--name", "laptop", "target"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reorderKeyFlags = %v, want %v", got, want)
	}
}

func TestLooksLikePublicKey(t *testing.T) {
	for _, key := range []string{"ssh-ed25519 AAAA", "ecdsa-sha2-nistp256 AAAA", "sk-ssh-ed25519 AAAA"} {
		if !looksLikePublicKey(key) {
			t.Fatalf("expected %q to look like a public key", key)
		}
	}
	if looksLikePublicKey("/tmp/id_ed25519.pub") {
		t.Fatal("path should not look like a public key")
	}
	if looksLikePublicKey(strings.Repeat("x", 3)) {
		t.Fatal("garbage should not look like a public key")
	}
}
