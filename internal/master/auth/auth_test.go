package auth

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/Rashid2G/on-demand-vms/internal/master/auth/quota"
	"github.com/Rashid2G/on-demand-vms/internal/master/pki"
	"github.com/Rashid2G/on-demand-vms/internal/store"
	"github.com/Rashid2G/on-demand-vms/internal/store/db"
)

func TestAuth_Argon2idPasswordHashing(t *testing.T) {
	password := "SuperSecurePassword123!@#"
	phc, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	// Verify valid password
	if err := VerifyPassword(password, phc); err != nil {
		t.Errorf("VerifyPassword failed on valid password: %v", err)
	}

	// Verify wrong password
	if err := VerifyPassword("WrongPassword", phc); err == nil {
		t.Error("expected error for wrong password, got nil")
	}
}

func TestAuth_PasswordLockout(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.Open(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	queries := db.New(st.Writer())

	user := &db.User{
		ID:           store.NewUserID(),
		Email:        "lockout@example.com",
		Username:     "lockoutuser",
		DisplayName:  "Lockout User",
		PasswordHash: "dummy",
		Role:         "user",
		Status:       "active",
		CreatedAt:    db.NowMillis(),
		UpdatedAt:    db.NowMillis(),
		Revision:     1,
	}
	if err := queries.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	// 4 failed attempts should increment failures without lock
	for i := 1; i <= 4; i++ {
		err := CheckAndRecordLoginAttempt(ctx, queries, user, false)
		if err != ErrPasswordMismatch {
			t.Fatalf("attempt %d: expected ErrPasswordMismatch, got %v", i, err)
		}
		u, _ := queries.GetUserByID(ctx, user.ID)
		if u.FailedLogins != i {
			t.Errorf("expected %d failed logins, got %d", i, u.FailedLogins)
		}
		user = u
	}

	// 5th attempt must trigger account lockout
	err = CheckAndRecordLoginAttempt(ctx, queries, user, false)
	if err == nil || user.LockedUntil != nil {
		// Verify in DB
		u, _ := queries.GetUserByID(ctx, user.ID)
		if u.LockedUntil == nil {
			t.Fatal("expected user to be locked on 5th attempt, got nil LockedUntil")
		}
	}
}

func TestAuth_APIKeys(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.Open(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	defer st.Close()

	masterKey, _ := pki.NewMasterKeyFromBytes(make([]byte, 32))
	queries := db.New(st.Writer())
	km := NewKeyManager(queries, masterKey)
	ctx := context.Background()

	user := &db.User{
		ID:           store.NewUserID(),
		Email:        "apikey@example.com",
		Username:     "apikeyuser",
		DisplayName:  "API Key User",
		PasswordHash: "dummy",
		Role:         "user",
		Status:       "active",
		CreatedAt:    db.NowMillis(),
		UpdatedAt:    db.NowMillis(),
		Revision:     1,
	}
	_ = queries.CreateUser(ctx, user)

	// Generate key
	token, keyRecord, err := km.GenerateAPIKey(ctx, user.ID, "test-token", nil)
	if err != nil {
		t.Fatalf("GenerateAPIKey failed: %v", err)
	}

	// Authenticate valid key
	u, k, err := km.AuthenticateToken(ctx, token, "127.0.0.1")
	if err != nil {
		t.Fatalf("AuthenticateToken failed on valid key: %v", err)
	}
	if u.ID != user.ID || k.ID != keyRecord.ID {
		t.Errorf("mismatched user or key records")
	}

	// Revoke key
	if err := queries.RevokeAPIKey(ctx, keyRecord.ID, user.ID); err != nil {
		t.Fatalf("RevokeAPIKey failed: %v", err)
	}

	// Re-authenticate revoked key
	_, _, err = km.AuthenticateToken(ctx, token, "127.0.0.1")
	if err != ErrAPIKeyRevoked {
		t.Errorf("expected ErrAPIKeyRevoked, got %v", err)
	}
}

func TestAuth_QuotaEnforcement(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.Open(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	queries := db.New(st.Writer())

	user := &db.User{
		ID:           store.NewUserID(),
		Email:        "quota@example.com",
		Username:     "quotauser",
		Role:         "user",
		Status:       "active",
		QuotaJSON:    sql.NullString{String: `{"instances":2,"vcpus":4,"memory_mb":4096,"disk_gb":50,"vpcs":1,"api_keys":5}`, Valid: true},
		CreatedAt:    db.NowMillis(),
		UpdatedAt:    db.NowMillis(),
		Revision:     1,
	}
	_ = queries.CreateUser(ctx, user)

	// Within quota
	err = quota.CheckQuota(ctx, queries, user, 1, 2, 2048, 10240, 1, 1, "trace-1")
	if err != nil {
		t.Errorf("expected within quota check to succeed, got %v", err)
	}

	// Exceed instances
	err = quota.CheckQuota(ctx, queries, user, 3, 2, 2048, 10240, 1, 1, "trace-2")
	if err == nil {
		t.Error("expected quota_exceeded error for instances, got nil")
	}

	// Admin bypass
	adminUser := &db.User{
		ID:        store.NewUserID(),
		Username:  "adminuser",
		Role:      "admin",
		Status:    "active",
		CreatedAt: db.NowMillis(),
	}
	err = quota.CheckQuota(ctx, queries, adminUser, 100, 200, 204800, 1024000, 10, 10, "trace-3")
	if err != nil {
		t.Errorf("admin must bypass quota check, got %v", err)
	}
}
