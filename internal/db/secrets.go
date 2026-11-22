package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rashid2G/compute/internal/master/pki"
)

const (
	SecretPrefixV1   = "enc:v1:"
	HashPrefixSHA256 = "sha256:"
)

// HashToken computes a prefixed SHA-256 hex digest for tokens at rest.
// If the token already has the sha256: prefix, it is returned unmodified.
func HashToken(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, HashPrefixSHA256) {
		return raw
	}
	sum := sha256.Sum256([]byte(raw))
	return HashPrefixSHA256 + hex.EncodeToString(sum[:])
}

// CompareTokenConstantTime securely compares a stored token (which may be hashed
// or legacy plaintext) against a candidate token presented by a client.
func CompareTokenConstantTime(stored, candidate string) bool {
	if stored == "" || candidate == "" {
		return false
	}
	candidateHash := HashToken(candidate)
	if strings.HasPrefix(stored, HashPrefixSHA256) {
		return subtle.ConstantTimeCompare([]byte(stored), []byte(candidateHash)) == 1
	}
	// Fallback for legacy plaintext rows before migration
	matchPlain := subtle.ConstantTimeCompare([]byte(stored), []byte(candidate)) == 1
	matchHash := subtle.ConstantTimeCompare([]byte(stored), []byte(candidateHash)) == 1
	return matchPlain || matchHash
}

func isMemoryDB(dbPath string) bool {
	return dbPath == ":memory:" || strings.Contains(dbPath, "mode=memory") || strings.Contains(dbPath, ":memory:")
}

// resolveMasterKey loads or creates the 32-byte master key for AES-256-GCM envelope encryption.
func resolveMasterKey(dbPath string) (*pki.MasterKey, error) {
	if envKey := strings.TrimSpace(os.Getenv("COMPUTE_MASTER_KEY")); envKey != "" {
		if len(envKey) == 64 {
			if b, err := hex.DecodeString(envKey); err == nil && len(b) == 32 {
				return pki.NewMasterKeyFromBytes(b)
			}
		}
		if b, err := base64.StdEncoding.DecodeString(envKey); err == nil && len(b) == 32 {
			return pki.NewMasterKeyFromBytes(b)
		}
		if len(envKey) == 32 {
			return pki.NewMasterKeyFromBytes([]byte(envKey))
		}
		h := sha256.Sum256([]byte(envKey))
		return pki.NewMasterKeyFromBytes(h[:])
	}

	if isMemoryDB(dbPath) {
		var ephem [32]byte
		if _, err := io.ReadFull(rand.Reader, ephem[:]); err != nil {
			return nil, fmt.Errorf("failed to generate in-memory master key: %w", err)
		}
		return pki.NewMasterKeyFromBytes(ephem[:])
	}

	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create db directory for master key: %w", err)
	}
	_ = os.Chmod(dir, 0700)
	keyPath := filepath.Join(dir, "master.key")
	mk, err := pki.LoadOrCreateMasterKey(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load or create master key: %w", err)
	}
	_ = os.Chmod(keyPath, 0600)
	return mk, nil
}

// SetMasterKey overrides the store's master key (primarily for testing).
func (s *Store) SetMasterKey(mk *pki.MasterKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.masterKey = mk
}

// MasterKey returns the active master key instance.
func (s *Store) MasterKey() *pki.MasterKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.masterKey
}

// encryptSecret encrypts a plaintext string with AES-256-GCM envelope encryption.
// If the string is already encrypted with enc:v1: or is empty, it returns as is.
func (s *Store) encryptSecret(plain string) (string, error) {
	if plain == "" || strings.HasPrefix(plain, SecretPrefixV1) {
		return plain, nil
	}
	if s.masterKey == nil {
		return "", fmt.Errorf("master key is not initialized")
	}
	encrypted, err := s.masterKey.Encrypt([]byte(plain))
	if err != nil {
		return "", fmt.Errorf("encryption failed: %w", err)
	}
	return SecretPrefixV1 + base64.StdEncoding.EncodeToString(encrypted), nil
}

// decryptSecret decrypts an enc:v1: prefixed ciphertext string.
// If the string is not encrypted, it is returned as is for transparent compatibility.
func (s *Store) decryptSecret(cipherStr string) (string, error) {
	if cipherStr == "" || !strings.HasPrefix(cipherStr, SecretPrefixV1) {
		return cipherStr, nil
	}
	if s.masterKey == nil {
		return "", fmt.Errorf("master key is not initialized")
	}
	rawB64 := strings.TrimPrefix(cipherStr, SecretPrefixV1)
	data, err := base64.StdEncoding.DecodeString(rawB64)
	if err != nil {
		return "", fmt.Errorf("failed to decode encrypted secret: %w", err)
	}
	plain, err := s.masterKey.Decrypt(data)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt secret: %w", err)
	}
	return string(plain), nil
}

// ValidateAccessAPIKey validates a bearer API token against stored SHA-256 hashes
// using constant-time comparison.
func (s *Store) ValidateAccessAPIKey(ctx context.Context, token string) (*AccessAPIKey, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("invalid access key")
	}
	key, err := s.GetAccessAPIKeyByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, fmt.Errorf("invalid access key")
	}
	return key, nil
}

// migrateSecretsAndTokens scans existing rows on store startup and:
// 1. Transparently encrypts plaintext secrets in github_connections, webhooks, and environments.
// 2. Hashes legacy plaintext tokens in access_api_keys and worker_tokens with sha256:.
func (s *Store) migrateSecretsAndTokens(ctx context.Context) error {
	// 1. github_connections
	ghRows, err := s.reader().QueryContext(ctx, `SELECT id, github_token, webhook_secret FROM github_connections WHERE (github_token != '' AND github_token NOT LIKE 'enc:v1:%') OR (webhook_secret != '' AND webhook_secret NOT LIKE 'enc:v1:%')`)
	if err == nil {
		type ghEntry struct{ id, tok, sec string }
		var entries []ghEntry
		for ghRows.Next() {
			var e ghEntry
			if err := ghRows.Scan(&e.id, &e.tok, &e.sec); err == nil {
				entries = append(entries, e)
			}
		}
		ghRows.Close()
		for _, e := range entries {
			encTok, _ := s.encryptSecret(e.tok)
			encSec, _ := s.encryptSecret(e.sec)
			_, _ = s.db.ExecContext(ctx, `UPDATE github_connections SET github_token = ?, webhook_secret = ? WHERE id = ?`, encTok, encSec, e.id)
		}
	}

	// 2. webhooks
	whRows, err := s.reader().QueryContext(ctx, `SELECT id, secret FROM webhooks WHERE secret != '' AND secret NOT LIKE 'enc:v1:%'`)
	if err == nil {
		type whEntry struct{ id, sec string }
		var entries []whEntry
		for whRows.Next() {
			var e whEntry
			if err := whRows.Scan(&e.id, &e.sec); err == nil && e.sec != "" {
				entries = append(entries, e)
			}
		}
		whRows.Close()
		for _, e := range entries {
			encSec, _ := s.encryptSecret(e.sec)
			_, _ = s.db.ExecContext(ctx, `UPDATE webhooks SET secret = ? WHERE id = ?`, encSec, e.id)
		}
	}

	// 3. environments
	envRows, err := s.reader().QueryContext(ctx, `SELECT rowid, secrets FROM environments WHERE secrets != '' AND secrets NOT LIKE 'enc:v1:%'`)
	if err == nil {
		type envEntry struct {
			rowid int64
			sec   string
		}
		var entries []envEntry
		for envRows.Next() {
			var e envEntry
			if err := envRows.Scan(&e.rowid, &e.sec); err == nil && e.sec != "" {
				entries = append(entries, e)
			}
		}
		envRows.Close()
		for _, e := range entries {
			encSec, _ := s.encryptSecret(e.sec)
			_, _ = s.db.ExecContext(ctx, `UPDATE environments SET secrets = ? WHERE rowid = ?`, encSec, e.rowid)
		}
	}

	// 4. access_api_keys
	keyRows, err := s.reader().QueryContext(ctx, `SELECT id, token FROM access_api_keys WHERE token != '' AND token NOT LIKE 'sha256:%'`)
	if err == nil {
		type keyEntry struct{ id, tok string }
		var entries []keyEntry
		for keyRows.Next() {
			var e keyEntry
			if err := keyRows.Scan(&e.id, &e.tok); err == nil && e.tok != "" {
				entries = append(entries, e)
			}
		}
		keyRows.Close()
		for _, e := range entries {
			hashed := HashToken(e.tok)
			if hashed != e.tok {
				_, _ = s.db.ExecContext(ctx, `UPDATE access_api_keys SET token = ? WHERE id = ?`, hashed, e.id)
			}
		}
	}

	// 5. worker_tokens
	tokRows, err := s.reader().QueryContext(ctx, `SELECT token FROM worker_tokens WHERE token != '' AND token NOT LIKE 'sha256:%'`)
	if err == nil {
		var legacyTokens []string
		for tokRows.Next() {
			var t string
			if err := tokRows.Scan(&t); err == nil && t != "" {
				legacyTokens = append(legacyTokens, t)
			}
		}
		tokRows.Close()
		for _, oldTok := range legacyTokens {
			newTok := HashToken(oldTok)
			if newTok != oldTok {
				_, _ = s.db.ExecContext(ctx, `UPDATE worker_tokens SET token = ? WHERE token = ?`, newTok, oldTok)
			}
		}
	}

	return nil
}
