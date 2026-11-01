package pki

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var (
	ErrInvalidCiphertext = errors.New("invalid ciphertext format or version")
	ErrDecryptionFailed  = errors.New("decryption failed (authentication tag mismatch)")
)

const (
	version1     byte = 0x01
	nonceSize         = 12
	masterKeyLen      = 32
)

// MasterKey manages symmetric AES-256-GCM encryption and HMAC peppering.
type MasterKey struct {
	key [32]byte
}

// LoadOrCreateMasterKey reads the 32-byte key from keyPath or creates a new one with 0600 permissions.
func LoadOrCreateMasterKey(keyPath string) (*MasterKey, error) {
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return nil, fmt.Errorf("failed to create directory for master key: %w", err)
	}

	data, err := os.ReadFile(keyPath)
	if err == nil {
		if len(data) != masterKeyLen {
			return nil, fmt.Errorf("invalid master key length %d, expected %d", len(data), masterKeyLen)
		}
		var mk MasterKey
		copy(mk.key[:], data)
		return &mk, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read master key: %w", err)
	}

	// Generate 32 cryptographically secure random bytes
	var newKey [32]byte
	if _, err := io.ReadFull(rand.Reader, newKey[:]); err != nil {
		return nil, fmt.Errorf("failed to generate random master key: %w", err)
	}

	// Write with 0600 permissions
	if err := os.WriteFile(keyPath, newKey[:], 0600); err != nil {
		return nil, fmt.Errorf("failed to persist master key: %w", err)
	}

	return &MasterKey{key: newKey}, nil
}

// NewMasterKeyFromBytes initializes a master key directly from 32 bytes (for in-memory or test usage).
func NewMasterKeyFromBytes(b []byte) (*MasterKey, error) {
	if len(b) != masterKeyLen {
		return nil, fmt.Errorf("master key must be exactly 32 bytes, got %d", len(b))
	}
	var mk MasterKey
	copy(mk.key[:], b)
	return &mk, nil
}

// Encrypt encrypts plaintext using AES-256-GCM with a 12-byte random nonce.
// Output format: [1 byte version (0x01)] [12 bytes nonce] [ciphertext + 16 bytes auth tag]
func (m *MasterKey) Encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(m.key[:])
	if err != nil {
		return nil, fmt.Errorf("failed to initialize AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize GCM: %w", err)
	}

	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate random nonce: %w", err)
	}

	// Additional authenticated data is the 1-byte version
	aad := []byte{version1}
	sealed := gcm.Seal(nil, nonce, plaintext, aad)

	result := make([]byte, 1+nonceSize+len(sealed))
	result[0] = version1
	copy(result[1:1+nonceSize], nonce)
	copy(result[1+nonceSize:], sealed)

	return result, nil
}

// Decrypt authenticates and decrypts ciphertext formatted as [version(1)] [nonce(12)] [ciphertext + tag(16)].
func (m *MasterKey) Decrypt(data []byte) ([]byte, error) {
	if len(data) < 1+nonceSize+16 {
		return nil, ErrInvalidCiphertext
	}

	if data[0] != version1 {
		return nil, ErrInvalidCiphertext
	}

	nonce := data[1 : 1+nonceSize]
	ciphertext := data[1+nonceSize:]

	block, err := aes.NewCipher(m.key[:])
	if err != nil {
		return nil, fmt.Errorf("failed to initialize AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize GCM: %w", err)
	}

	aad := []byte{version1}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, ErrDecryptionFailed
	}

	return plaintext, nil
}

// HMAC computes HMAC-SHA256 of data using the master key as pepper.
func (m *MasterKey) HMAC(data []byte) []byte {
	h := hmac.New(sha256.New, m.key[:])
	h.Write(data)
	return h.Sum(nil)
}

// VerifyHMAC performs constant-time verification of computed HMAC against expected.
func (m *MasterKey) VerifyHMAC(data, expectedHMAC []byte) bool {
	actual := m.HMAC(data)
	return subtle.ConstantTimeCompare(actual, expectedHMAC) == 1
}

// Bytes returns a copy of the raw 32-byte key.
func (m *MasterKey) Bytes() []byte {
	out := make([]byte, 32)
	copy(out, m.key[:])
	return out
}
