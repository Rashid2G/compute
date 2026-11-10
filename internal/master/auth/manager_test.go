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

func TestManager_QueryParameterRestrictions(t *testing.T) {
	mgr, st, _ := setupTestManager(t)
	defer st.Close()

	ctx := context.Background()
	user, err := mgr.Signup(ctx, "charlie@example.com", "charlie", "Charlie Brown", "SecretPass123!", "user")
	if err != nil {
		t.Fatalf("Signup failed: %v", err)
	}

	token, _, err := mgr.KeyManager().GenerateAPIKey(ctx, user.ID, "query-test-key", nil)
	if err != nil {
		t.Fatalf("GenerateAPIKey failed: %v", err)
	}

	// 1. REST CRUD endpoint with ?token= must be REJECTED (401 / error), even with "ws" substrings
	restEndpoints := []string{
		"/api/v1/sandboxes?token=" + token,
		"/api/v1/sandboxes?api_key=" + token,
		"/api/v1/vms/vm-123?token=" + token,
		"/api/v1/volumes?api_key=" + token,
		"/api/v1/vms/ws-production?token=" + token,
		"/api/v1/vms/ws-test?token=" + token,
		"/api/v1/sandboxes/ws-production?token=" + token,
		"/api/v1/sandboxes/ws-test?api_key=" + token,
		"/api/v1/sandboxes/ws?token=" + token,
		"/api/v1/images/ws-image?token=" + token,
		"/api/v1/templates/ws-tpl?token=" + token,
		"/api/v1/jobs/ws-job?token=" + token,
		"/api/v1/config?token=" + token,
	}
	for _, ep := range restEndpoints {
		req := httptest.NewRequest("GET", ep, nil)
		_, err := mgr.AuthenticateRequest(req)
		if err == nil {
			t.Errorf("expected AuthenticateRequest on REST route %q to be rejected, but it succeeded", ep)
		}
	}

	// 2. Allowed streaming / preview endpoints with ?token= or ?api_key= must SUCCEED
	allowedEndpoints := []string{
		"/api/v1/ws?token=" + token,
		"/api/v1/ws/live?api_key=" + token,
		"/api/v1/sandboxes/vm-1/exec?token=" + token,
		"/preview/vm-1/proxy?token=" + token,
		"/api/v1/events?api_key=" + token,
		"/api/v1/events/stream?token=" + token,
		"/api/v1/vms/vm-1/logs?token=" + token,
	}
	for _, ep := range allowedEndpoints {
		req := httptest.NewRequest("GET", ep, nil)
		authCtx, err := mgr.AuthenticateRequest(req)
		if err != nil {
			t.Errorf("expected AuthenticateRequest on streaming route %q to succeed, got: %v", ep, err)
		} else if authCtx.User.ID != user.ID {
			t.Errorf("expected user %s, got %s", user.ID, authCtx.User.ID)
		}
	}

	// 3. Request with Upgrade: websocket header on REST endpoints MUST BE REJECTED
	wsReq := httptest.NewRequest("GET", "/api/v1/vms?token="+token, nil)
	wsReq.Header.Set("Upgrade", "websocket")
	if _, err := mgr.AuthenticateRequest(wsReq); err == nil {
		t.Errorf("expected AuthenticateRequest with Upgrade: websocket on /api/v1/vms to be rejected, but it succeeded")
	}

	// 4. Request with Accept: text/event-stream header on REST endpoints MUST BE REJECTED
	sseReq := httptest.NewRequest("GET", "/api/v1/vms?api_key="+token, nil)
	sseReq.Header.Set("Accept", "text/event-stream")
	if _, err := mgr.AuthenticateRequest(sseReq); err == nil {
		t.Errorf("expected AuthenticateRequest with Accept: text/event-stream on /api/v1/vms to be rejected, but it succeeded")
	}

	// 5. Generic unapproved route with spoofed headers MUST ALSO BE REJECTED
	genericReq := httptest.NewRequest("GET", "/any/custom/route?token="+token, nil)
	genericReq.Header.Set("Upgrade", "websocket")
	if _, err := mgr.AuthenticateRequest(genericReq); err == nil {
		t.Errorf("expected AuthenticateRequest on generic route with Upgrade header to be rejected, but it succeeded")
	}
}
