package db

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestStore_PermissionsAndMasterKey(t *testing.T) {
	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "nested", "computed")
	dbPath := filepath.Join(dbDir, "compute.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer store.Close()

	// 1. Directory permissions: 0700
	dirInfo, err := os.Stat(dbDir)
	if err != nil {
		t.Fatalf("Stat dbDir failed: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0700 {
		t.Fatalf("expected dir mode 0700, got %#o", perm)
	}

	// 2. Database file permissions: 0600
	fileInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Stat dbPath failed: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0600 {
		t.Fatalf("expected db file mode 0600, got %#o", perm)
	}

	// 3. Master key permissions: 0600
	keyPath := filepath.Join(dbDir, "master.key")
	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Stat master.key failed: %v", err)
	}
	if perm := keyInfo.Mode().Perm(); perm != 0600 {
		t.Fatalf("expected master.key mode 0600, got %#o", perm)
	}
}

func TestStore_SecretsEncryptionAndMigration(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "compute.db")
	ctx := context.Background()

	rawGHToken := "ghp_RAW_GITHUB_SECRET_TOKEN_12345"
	rawWebhookSecret := "whsec_RAW_WEBHOOK_SECRET_TOKEN_67890"
	rawWHSecret := "whk_RAW_SIGNING_SECRET_TOKEN_ABCDEF"
	rawEnvSecret := "SUPER_SECRET_ENV_VAR=SUPER_SECRET_VALUE_XYZ"

	// Step 1: Initialize database with legacy schema and insert plaintext secrets
	{
		initStore, err := NewStore(dbPath)
		if err != nil {
			t.Fatalf("failed to init store: %v", err)
		}
		// Manually insert raw plaintext values bypassing encryptSecret to simulate legacy unencrypted rows
		_, err = initStore.db.ExecContext(ctx, `
			INSERT INTO github_connections (id, owner_id, name, repo_url, github_token, webhook_secret, created_at)
			VALUES ('gh_legacy', 'default', 'test-repo', 'https://github.com/test/repo', ?, ?, CURRENT_TIMESTAMP)
		`, rawGHToken, rawWebhookSecret)
		if err != nil {
			t.Fatalf("insert raw github connection failed: %v", err)
		}

		_, err = initStore.db.ExecContext(ctx, `
			INSERT INTO webhooks (id, url, secret, created_at, updated_at)
			VALUES ('whk_legacy', 'https://example.com/webhook', ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		`, rawWHSecret)
		if err != nil {
			t.Fatalf("insert raw webhook failed: %v", err)
		}

		_, err = initStore.db.ExecContext(ctx, `
			INSERT INTO environments (owner_id, name, secrets, updated_at)
			VALUES ('default', 'legacy_env', ?, CURRENT_TIMESTAMP)
		`, rawEnvSecret)
		if err != nil {
			t.Fatalf("insert raw environment failed: %v", err)
		}

		// Force WAL flush so secrets are written to disk
		_, _ = initStore.db.Exec("PRAGMA wal_checkpoint(TRUNCATE);")
		_ = initStore.Close()
	}

	// Verify the raw plaintext strings WERE present in raw SQLite database bytes before migration
	dbBytesBefore, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read db before migration: %v", err)
	}
	if !bytes.Contains(dbBytesBefore, []byte(rawGHToken)) {
		t.Fatalf("expected raw token in db bytes before migration")
	}

	// Step 2: Reopen with NewStore - should transparently migrate all plaintext secrets to enc:v1:
	reopenedStore, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store failed: %v", err)
	}
	defer reopenedStore.Close()

	// Verify directly in DB rows that stored values now start with enc:v1:
	var dbGHToken, dbGHSecret, dbWHSecret, dbEnvSecret string
	err = reopenedStore.db.QueryRowContext(ctx, `SELECT github_token, webhook_secret FROM github_connections WHERE id = 'gh_legacy'`).Scan(&dbGHToken, &dbGHSecret)
	if err != nil {
		t.Fatalf("query migrated github connection failed: %v", err)
	}
	if !strings.HasPrefix(dbGHToken, SecretPrefixV1) {
		t.Fatalf("expected enc:v1: prefix on github_token, got: %s", dbGHToken)
	}
	if !strings.HasPrefix(dbGHSecret, SecretPrefixV1) {
		t.Fatalf("expected enc:v1: prefix on webhook_secret, got: %s", dbGHSecret)
	}

	err = reopenedStore.db.QueryRowContext(ctx, `SELECT secret FROM webhooks WHERE id = 'whk_legacy'`).Scan(&dbWHSecret)
	if err != nil {
		t.Fatalf("query migrated webhook failed: %v", err)
	}
	if !strings.HasPrefix(dbWHSecret, SecretPrefixV1) {
		t.Fatalf("expected enc:v1: prefix on webhook secret, got: %s", dbWHSecret)
	}

	err = reopenedStore.db.QueryRowContext(ctx, `SELECT secrets FROM environments WHERE name = 'legacy_env'`).Scan(&dbEnvSecret)
	if err != nil {
		t.Fatalf("query migrated environment failed: %v", err)
	}
	if !strings.HasPrefix(dbEnvSecret, SecretPrefixV1) {
		t.Fatalf("expected enc:v1: prefix on environment secrets, got: %s", dbEnvSecret)
	}

	// Step 3: Verify Store getters transparently decrypt back to original plaintext
	gh, err := reopenedStore.GetGitHubConnection(ctx, "gh_legacy")
	if err != nil || gh == nil {
		t.Fatalf("GetGitHubConnection failed: %v", err)
	}
	if gh.GitHubToken != rawGHToken {
		t.Fatalf("GetGitHubConnection token mismatch: got %q, want %q", gh.GitHubToken, rawGHToken)
	}
	if gh.WebhookSecret != rawWebhookSecret {
		t.Fatalf("GetGitHubConnection webhook_secret mismatch: got %q, want %q", gh.WebhookSecret, rawWebhookSecret)
	}

	wh, err := reopenedStore.GetWebhook(ctx, "whk_legacy")
	if err != nil || wh == nil {
		t.Fatalf("GetWebhook failed: %v", err)
	}
	if wh.Secret != rawWHSecret {
		t.Fatalf("GetWebhook secret mismatch: got %q, want %q", wh.Secret, rawWHSecret)
	}

	env, err := reopenedStore.GetEnvironmentByOwner(ctx, "default", "legacy_env")
	if err != nil || env == nil {
		t.Fatalf("GetEnvironmentByOwner failed: %v", err)
	}
	if env.Secrets != rawEnvSecret {
		t.Fatalf("GetEnvironmentByOwner secrets mismatch: got %q, want %q", env.Secrets, rawEnvSecret)
	}

	// Step 4: Checkpoint WAL and vacuum, then verify raw secret bytes do NOT appear in SQLite file bytes
	_, _ = reopenedStore.db.Exec("PRAGMA wal_checkpoint(TRUNCATE);")
	_, _ = reopenedStore.db.Exec("VACUUM;")
	_ = reopenedStore.Close()

	checkNoPlaintextInFiles := func(path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		for _, secret := range []string{rawGHToken, rawWebhookSecret, rawWHSecret, rawEnvSecret} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatalf("raw secret %q leaked in SQLite database file %s!", secret, path)
			}
		}
	}
	checkNoPlaintextInFiles(dbPath)
	checkNoPlaintextInFiles(dbPath + "-wal")
}

func TestStore_TokenHashingAndMigration(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "compute.db")
	ctx := context.Background()

	rawAPIKeyToken := "cmp_live_RAW_SECRET_KEY_11112222"
	rawWorkerToken := "mft_RAW_SECRET_WORKER_33334444"

	// 1. Manually insert legacy plaintext tokens
	{
		initStore, err := NewStore(dbPath)
		if err != nil {
			t.Fatalf("failed to init store: %v", err)
		}
		_, err = initStore.db.ExecContext(ctx, `
			INSERT INTO access_api_keys (id, name, prefix, token, scope, created_at)
			VALUES ('key_legacy', 'legacy key', 'cmp_live_RAW_', ?, 'admin', CURRENT_TIMESTAMP)
		`, rawAPIKeyToken)
		if err != nil {
			t.Fatalf("insert raw api key failed: %v", err)
		}

		_, err = initStore.db.ExecContext(ctx, `
			INSERT INTO worker_tokens (token, name, pool_id, expires_at, created_at)
			VALUES (?, 'legacy-worker', 'default', datetime('now', '+1 hour'), CURRENT_TIMESTAMP)
		`, rawWorkerToken)
		if err != nil {
			t.Fatalf("insert raw worker token failed: %v", err)
		}

		_, _ = initStore.db.Exec("PRAGMA wal_checkpoint(TRUNCATE);")
		_ = initStore.Close()
	}

	// 2. Reopen store - startup migration must hash with sha256:
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store failed: %v", err)
	}
	defer store.Close()

	// Verify rows in DB contain sha256: prefix
	var storedKeyToken, storedWorkerToken string
	err = store.db.QueryRowContext(ctx, `SELECT token FROM access_api_keys WHERE id = 'key_legacy'`).Scan(&storedKeyToken)
	if err != nil {
		t.Fatalf("query api key failed: %v", err)
	}
	if !strings.HasPrefix(storedKeyToken, HashPrefixSHA256) {
		t.Fatalf("expected sha256: prefix in access_api_keys, got: %s", storedKeyToken)
	}

	err = store.db.QueryRowContext(ctx, `SELECT token FROM worker_tokens WHERE name = 'legacy-worker'`).Scan(&storedWorkerToken)
	if err != nil {
		t.Fatalf("query worker token failed: %v", err)
	}
	if !strings.HasPrefix(storedWorkerToken, HashPrefixSHA256) {
		t.Fatalf("expected sha256: prefix in worker_tokens, got: %s", storedWorkerToken)
	}

	// 3. Validation with original raw token succeeds
	key, err := store.ValidateAccessAPIKey(ctx, rawAPIKeyToken)
	if err != nil || key == nil || key.ID != "key_legacy" {
		t.Fatalf("ValidateAccessAPIKey with valid token failed: %v", err)
	}

	// Validation with invalid token fails
	if _, err := store.ValidateAccessAPIKey(ctx, "invalid-token"); err == nil {
		t.Fatal("expected ValidateAccessAPIKey with invalid token to fail")
	}

	// Worker token authorization with original raw token succeeds
	if err := store.AuthorizeWorkerToken(ctx, rawWorkerToken, "legacy-worker"); err != nil {
		t.Fatalf("AuthorizeWorkerToken with valid token failed: %v", err)
	}

	// Invalid worker token fails
	if err := store.AuthorizeWorkerToken(ctx, "invalid-worker-token", "legacy-worker"); err == nil {
		t.Fatal("expected AuthorizeWorkerToken with invalid token to fail")
	}

	// 4. Verify newly created tokens are also hashed on creation
	newRawKey := "cmp_live_NEW_KEY_99998888"
	err = store.SaveAccessAPIKey(ctx, &AccessAPIKey{
		ID:        "key_new",
		Name:      "new key",
		Token:     newRawKey,
		Scope:     "admin",
		CreatedBy: "admin",
	})
	if err != nil {
		t.Fatalf("SaveAccessAPIKey failed: %v", err)
	}
	var newStoredKeyToken string
	_ = store.db.QueryRowContext(ctx, `SELECT token FROM access_api_keys WHERE id = 'key_new'`).Scan(&newStoredKeyToken)
	if !strings.HasPrefix(newStoredKeyToken, HashPrefixSHA256) {
		t.Fatalf("new key not hashed: %s", newStoredKeyToken)
	}

	newRawWorkerTok := "mft_NEW_WORKER_77776666"
	err = store.SaveWorkerToken(ctx, &WorkerToken{
		Token:     newRawWorkerTok,
		Name:      "new-worker",
		PoolID:    "default",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("SaveWorkerToken failed: %v", err)
	}
	var newStoredWorkerTok string
	_ = store.db.QueryRowContext(ctx, `SELECT token FROM worker_tokens WHERE name = 'new-worker'`).Scan(&newStoredWorkerTok)
	if !strings.HasPrefix(newStoredWorkerTok, HashPrefixSHA256) {
		t.Fatalf("new worker token not hashed: %s", newStoredWorkerTok)
	}

	// 5. Verify raw tokens are NOT in raw DB file bytes
	_, _ = store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE);")
	_, _ = store.db.Exec("VACUUM;")
	_ = store.Close()

	checkNoRawToken := func(path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		for _, tok := range []string{rawAPIKeyToken, rawWorkerToken, newRawKey, newRawWorkerTok} {
			if bytes.Contains(data, []byte(tok)) {
				t.Fatalf("raw token %q leaked in database file %s!", tok, path)
			}
		}
	}
	checkNoRawToken(dbPath)
	checkNoRawToken(dbPath + "-wal")
}

func TestStore_TenantScopedEnvironments(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(filepath.Join(tmpDir, "compute.db"))
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	envA := &EnvironmentProfile{
		OwnerID:        "tenant-a",
		Name:           "default",
		IsDefault:      true,
		SecretsEnabled: true,
		Secrets:        "SECRET_KEY=SECRET_A",
	}
	if err := store.SaveEnvironment(ctx, envA); err != nil {
		t.Fatalf("SaveEnvironment A failed: %v", err)
	}

	// Tenant B can create an environment with the EXACT same name "default" without collision
	envB := &EnvironmentProfile{
		OwnerID:        "tenant-b",
		Name:           "default",
		IsDefault:      true,
		SecretsEnabled: true,
		Secrets:        "SECRET_KEY=SECRET_B",
	}
	if err := store.SaveEnvironment(ctx, envB); err != nil {
		t.Fatalf("SaveEnvironment B failed: %v", err)
	}

	// Lookup by owner returns tenant's own environment
	gotA, err := store.GetEnvironmentByOwner(ctx, "tenant-a", "default")
	if err != nil || gotA == nil {
		t.Fatalf("GetEnvironmentByOwner A failed: %v", err)
	}
	if gotA.Secrets != "SECRET_KEY=SECRET_A" {
		t.Fatalf("Tenant A received wrong secrets: %s", gotA.Secrets)
	}

	gotB, err := store.GetEnvironmentByOwner(ctx, "tenant-b", "default")
	if err != nil || gotB == nil {
		t.Fatalf("GetEnvironmentByOwner B failed: %v", err)
	}
	if gotB.Secrets != "SECRET_KEY=SECRET_B" {
		t.Fatalf("Tenant B received wrong secrets: %s", gotB.Secrets)
	}

	// ListEnvironmentsByOwner is scoped
	listA, err := store.ListEnvironmentsByOwner(ctx, "tenant-a")
	if err != nil || len(listA) != 1 || listA[0].OwnerID != "tenant-a" {
		t.Fatalf("ListEnvironmentsByOwner A mismatch: %+v", listA)
	}

	listB, err := store.ListEnvironmentsByOwner(ctx, "tenant-b")
	if err != nil || len(listB) != 1 || listB[0].OwnerID != "tenant-b" {
		t.Fatalf("ListEnvironmentsByOwner B mismatch: %+v", listB)
	}

	// Deleting Tenant A's environment does not affect Tenant B
	if err := store.DeleteEnvironmentByOwner(ctx, "tenant-a", "default"); err != nil {
		t.Fatalf("DeleteEnvironmentByOwner A failed: %v", err)
	}
	deletedA, err := store.GetEnvironmentByOwner(ctx, "tenant-a", "default")
	if err != nil || deletedA != nil {
		t.Fatalf("expected Tenant A environment to be deleted, got: %+v", deletedA)
	}
	stillB, err := store.GetEnvironmentByOwner(ctx, "tenant-b", "default")
	if err != nil || stillB == nil || stillB.Secrets != "SECRET_KEY=SECRET_B" {
		t.Fatalf("Tenant B environment was unexpectedly modified or deleted: %+v", stillB)
	}
}
