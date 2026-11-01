package pki

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestMasterKey_EncryptDecrypt(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "master.key")

	mk, err := LoadOrCreateMasterKey(keyPath)
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey failed: %v", err)
	}

	// Verify key file permissions are 0600
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("expected master.key to have 0600 permissions, got %o", perm)
	}

	// Test encrypt/decrypt round trip
	plaintext := []byte("secret_api_token_12345_!@#$%^&*()")
	ciphertext, err := mk.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Check format: version byte = 0x01
	if len(ciphertext) < 1+12+16 {
		t.Fatalf("ciphertext too short: %d", len(ciphertext))
	}
	if ciphertext[0] != version1 {
		t.Errorf("expected version 1, got %d", ciphertext[0])
	}

	decrypted, err := mk.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if !bytes.Equal(plaintext, decrypted) {
		t.Errorf("decrypted %s does not match plaintext %s", decrypted, plaintext)
	}

	// Test tampering resistance: flip one bit in ciphertext
	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	tampered[len(tampered)-1] ^= 0x01

	_, err = mk.Decrypt(tampered)
	if err == nil {
		t.Error("expected decryption to fail on tampered ciphertext, got nil")
	}

	// Verify reloading key returns identical key
	reloaded, err := LoadOrCreateMasterKey(keyPath)
	if err != nil {
		t.Fatalf("reloading master key failed: %v", err)
	}
	decrypted2, err := reloaded.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("reloaded key failed to decrypt: %v", err)
	}
	if !bytes.Equal(plaintext, decrypted2) {
		t.Errorf("reloaded decrypt mismatch")
	}
}

func TestMasterKey_HMAC(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	mk, err := NewMasterKeyFromBytes(key)
	if err != nil {
		t.Fatalf("NewMasterKeyFromBytes failed: %v", err)
	}

	data := []byte("important_string_to_hash")
	hmac1 := mk.HMAC(data)

	if !mk.VerifyHMAC(data, hmac1) {
		t.Error("VerifyHMAC returned false for valid HMAC")
	}

	tamperedHMAC := make([]byte, len(hmac1))
	copy(tamperedHMAC, hmac1)
	tamperedHMAC[0] ^= 0x01

	if mk.VerifyHMAC(data, tamperedHMAC) {
		t.Error("VerifyHMAC returned true for invalid HMAC")
	}
}

func TestCA_IssueAndTLSConfig(t *testing.T) {
	tmpDir := t.TempDir()

	ca, err := LoadOrCreateCA(tmpDir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA failed: %v", err)
	}

	// Verify CA key is 0600
	fi, err := os.Stat(filepath.Join(tmpDir, "ca.key"))
	if err != nil {
		t.Fatalf("stat ca.key failed: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("expected ca.key permissions 0600, got %o", perm)
	}

	// Issue node cert
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("10.10.0.5")}
	dnsNames := []string{"node-1.internal", "localhost"}
	certPEM, keyPEM, serial, err := ca.IssueCert("node-1", ips, dnsNames, 30)
	if err != nil {
		t.Fatalf("IssueCert failed: %v", err)
	}
	if serial == nil {
		t.Error("expected non-nil serial number")
	}

	// Generate TLS server config
	serverTLS, err := ca.TLSConfig(certPEM, keyPEM, true)
	if err != nil {
		t.Fatalf("TLSConfig server failed: %v", err)
	}
	if serverTLS.ClientCAs == nil {
		t.Error("expected serverTLS ClientCAs to be set")
	}

	// Generate TLS client config
	clientTLS, err := ca.TLSConfig(certPEM, keyPEM, false)
	if err != nil {
		t.Fatalf("TLSConfig client failed: %v", err)
	}
	if clientTLS.RootCAs == nil {
		t.Error("expected clientTLS RootCAs to be set")
	}
}
