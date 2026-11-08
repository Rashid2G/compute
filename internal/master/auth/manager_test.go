package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/master/auth"
	"github.com/Rashid2G/on-demand-vms/internal/master/pki"
	"github.com/Rashid2G/on-demand-vms/internal/store"
	"github.com/Rashid2G/on-demand-vms/internal/store/db"
)

func setupTestManager(t *testing.T) (*auth.Manager, *store.Store, *pki.MasterKey) {
	t.Helper()
	tmpDir := t.TempDir()

	mk, err := pki.LoadOrCreateMasterKey(filepath.Join(tmpDir, "master.key"))
	if err != nil {
		t.Fatalf("failed to create master key: %v", err)
	}

	st, err := store.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}

	queries := db.New(st.Writer())
	mgr := auth.NewManager(queries, mk, 24*time.Hour, "test_admin_bootstrap_secret_123")
	return mgr, st, mk
}

func TestManager_SignupAndLogin(t *testing.T) {
	mgr, st, _ := setupTestManager(t)
	defer st.Close()

	ctx := context.Background()

	// 1. Signup
	user, err := mgr.Signup(ctx, "alice@example.com", "alice", "Alice Wonder", "SecretPass123!", "user")
	if err != nil {
		t.Fatalf("Signup failed: %v", err)
	}
	if user.Username != "alice" || user.Role != "user" {
		t.Errorf("Unexpected user values: %+v", user)
	}

	// 2. Login with correct password
	loggedInUser, sessionToken, csrfToken, err := mgr.Login(ctx, "alice", "SecretPass123!", "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if loggedInUser.ID != user.ID || sessionToken == "" || csrfToken == "" {
		t.Errorf("Unexpected login response: user=%v, token=%s", loggedInUser, sessionToken)
	}

	// 3. Login with incorrect password
	_, _, _, err = mgr.Login(ctx, "alice", "WrongPassword", "127.0.0.1", "test-agent")
	if err == nil {
		t.Fatal("Expected error for wrong password, got nil")
	}

	// 4. Authenticate via Cookie
	req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
	authCtx, err := mgr.AuthenticateRequest(req)
	if err != nil {
		t.Fatalf("AuthenticateRequest with cookie failed: %v", err)
	}
	if authCtx.User.ID != user.ID || authCtx.AuthType != "session" {
		t.Errorf("Unexpected AuthContext: %+v", authCtx)
	}
}

func TestManager_APIKeyAuthentication(t *testing.T) {
	mgr, st, _ := setupTestManager(t)
	defer st.Close()

	ctx := context.Background()

	user, err := mgr.Signup(ctx, "bob@example.com", "bob", "Bob Dylan", "SecretPass123!", "admin")
	if err != nil {
		t.Fatalf("Signup failed: %v", err)
	}

	// Generate API key
	token, keyRecord, err := mgr.KeyManager().GenerateAPIKey(ctx, user.ID, "ci-token", nil)
	if err != nil {
		t.Fatalf("GenerateAPIKey failed: %v", err)
	}

	// Authenticate via Bearer header
	req := httptest.NewRequest("GET", "/api/v1/sandboxes", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	authCtx, err := mgr.AuthenticateRequest(req)
	if err != nil {
		t.Fatalf("AuthenticateRequest via Bearer failed: %v", err)
	}
	if authCtx.User.ID != user.ID || authCtx.Role != "admin" || authCtx.APIKey.ID != keyRecord.ID {
		t.Errorf("Unexpected AuthContext: %+v", authCtx)
	}
}

func TestManager_MiddlewareProtection(t *testing.T) {
	mgr, st, _ := setupTestManager(t)
	defer st.Close()

	protectedHandler := mgr.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		authCtx := auth.GetAuth(r.Context())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello " + authCtx.User.Username))
	})

	// 1. Request with no auth -> 401
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/sandboxes", nil)
	protectedHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. Request with Admin Bootstrap Secret -> 200
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/api/v1/sandboxes", nil)
	req2.Header.Set("Authorization", "Bearer test_admin_bootstrap_secret_123")
	protectedHandler(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("Expected 200 with bootstrap secret, got %d: %s", rec2.Code, rec2.Body.String())
	}
}
