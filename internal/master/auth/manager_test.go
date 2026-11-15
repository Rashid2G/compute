package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func TestManager_EnsureAdminAndSecretResolution(t *testing.T) {
	mgr, st, _ := setupTestManager(t)
	defer st.Close()

	ctx := context.Background()
	mgr.SetAdminIdentity("superadmin", "super@compute.local")

	// 1. Seed initial admin
	admin, err := mgr.EnsureAdmin(ctx, "super@compute.local", "superadmin", "Super Admin", "InitialPass123!")
	if err != nil {
		t.Fatalf("EnsureAdmin create failed: %v", err)
	}
	if admin.Username != "superadmin" || admin.Email != "super@compute.local" || admin.Role != "admin" || admin.Status != "active" {
		t.Fatalf("unexpected seeded admin: %+v", admin)
	}

	// Verify admin_secret resolves to the seeded DB user & org
	req := httptest.NewRequest("GET", "/api/v1/vms", nil)
	req.Header.Set("Authorization", "Bearer test_admin_bootstrap_secret_123")
	authCtx, err := mgr.AuthenticateRequest(req)
	if err != nil {
		t.Fatalf("AuthenticateRequest with admin_secret failed: %v", err)
	}
	if authCtx.User.ID != admin.ID || authCtx.User.Username != "superadmin" || authCtx.OrgID == "" {
		t.Errorf("expected admin_secret to resolve seeded admin %q with org, got user=%+v org=%q", admin.ID, authCtx.User, authCtx.OrgID)
	}

	// 2. Trigger failed logins lockout on the admin user
	for i := 0; i < 6; i++ {
		_, _, _, _ = mgr.Login(ctx, "superadmin", "WrongPassword", "127.0.0.1", "test")
	}
	if _, _, _, err := mgr.Login(ctx, "superadmin", "InitialPass123!", "127.0.0.1", "test"); err == nil {
		t.Fatal("expected locked out admin login to fail before EnsureAdmin sync")
	}

	// 3. Re-run EnsureAdmin with rotated password; should update hash and clear lockout
	synced, err := mgr.EnsureAdmin(ctx, "super@compute.local", "superadmin", "Super Admin", "RotatedPass456!")
	if err != nil {
		t.Fatalf("EnsureAdmin sync failed: %v", err)
	}
	if synced.ID != admin.ID {
		t.Fatalf("expected EnsureAdmin to update existing admin %q, got %q", admin.ID, synced.ID)
	}
	if _, _, _, err := mgr.Login(ctx, "superadmin", "InitialPass123!", "127.0.0.1", "test"); err == nil {
		t.Fatal("expected old admin password to be rejected after EnsureAdmin sync")
	}
	loggedIn, _, _, err := mgr.Login(ctx, "superadmin", "RotatedPass456!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("expected rotated admin password login to succeed after EnsureAdmin sync, got: %v", err)
	}
	if loggedIn.ID != admin.ID {
		t.Errorf("expected user ID %q, got %q", admin.ID, loggedIn.ID)
	}
}

func TestManager_EnsureAdmin_RenameAndPasswordRotationInPlace(t *testing.T) {
	mgr, st, _ := setupTestManager(t)
	defer st.Close()

	ctx := context.Background()
	queries := db.New(st.Reader())

	// 1. Initial boot with default admin identity
	initial, err := mgr.EnsureAdmin(ctx, "admin@compute.local", "admin", "Administrator", "AdminPass123!")
	if err != nil {
		t.Fatalf("initial EnsureAdmin failed: %v", err)
	}
	if _, _, _, err := mgr.Login(ctx, "admin", "AdminPass123!", "127.0.0.1", "test"); err != nil {
		t.Fatalf("initial admin login failed: %v", err)
	}

	// 2. Restart with renamed username, email, and rotated password
	renamed, err := mgr.EnsureAdmin(ctx, "newops@compute.local", "newops", "New Ops", "NewPass123!")
	if err != nil {
		t.Fatalf("renamed EnsureAdmin failed: %v", err)
	}
	if renamed.ID != initial.ID {
		t.Fatalf("expected in-place rename preserving ID %q, got %q", initial.ID, renamed.ID)
	}
	if renamed.Username != "newops" || renamed.Email != "newops@compute.local" {
		t.Fatalf("unexpected renamed fields: %+v", renamed)
	}

	// Personal org slug/name should be updated in-place as well
	org, err := queries.GetOrganizationByID(ctx, auth.PersonalOrgID(initial.ID))
	if err != nil {
		t.Fatalf("GetOrganizationByID failed: %v", err)
	}
	if org.Slug != "newops" || org.Name != "newops's Workspace" {
		t.Fatalf("expected personal org updated to newops, got slug=%q name=%q", org.Slug, org.Name)
	}

	// Old credentials must no longer log in
	if _, _, _, err := mgr.Login(ctx, "admin", "AdminPass123!", "127.0.0.1", "test"); err == nil {
		t.Fatal("expected old admin username/password login to fail after rename")
	}

	// New credentials must log in with the preserved User.ID
	loggedIn, _, _, err := mgr.Login(ctx, "newops", "NewPass123!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("expected newops login to succeed: %v", err)
	}
	if loggedIn.ID != initial.ID {
		t.Fatalf("expected preserved ID %q, got %q", initial.ID, loggedIn.ID)
	}

	// Only a single admin user should exist
	users, err := mgr.Users(ctx)
	if err != nil {
		t.Fatalf("Users failed: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("expected exactly 1 user after rename, got %d", len(users))
	}

	// 3. Subsequent rename from non-default "newops" to "ops2" also updates sole admin in-place
	renamed2, err := mgr.EnsureAdmin(ctx, "ops2@compute.local", "ops2", "Ops Two", "OpsTwoPass789!")
	if err != nil {
		t.Fatalf("second EnsureAdmin rename failed: %v", err)
	}
	if renamed2.ID != initial.ID || renamed2.Username != "ops2" {
		t.Fatalf("expected second rename to preserve ID %q and set username ops2, got %+v", initial.ID, renamed2)
	}
	if _, _, _, err := mgr.Login(ctx, "newops", "NewPass123!", "127.0.0.1", "test"); err == nil {
		t.Fatal("expected previous newops login to fail after second rename")
	}
	if _, _, _, err := mgr.Login(ctx, "ops2", "OpsTwoPass789!", "127.0.0.1", "test"); err != nil {
		t.Fatalf("expected ops2 login to succeed: %v", err)
	}
}

func TestManager_DeleteUser(t *testing.T) {
	mgr, st, _ := setupTestManager(t)
	defer st.Close()

	ctx := context.Background()
	admin, err := mgr.EnsureAdmin(ctx, "admin@compute.local", "admin", "Administrator", "AdminPass123!")
	if err != nil {
		t.Fatalf("EnsureAdmin failed: %v", err)
	}

	// Cannot delete primary bootstrap admin
	if err := mgr.DeleteUser(ctx, admin.ID); err == nil {
		t.Fatal("expected DeleteUser on primary bootstrap admin to fail")
	}

	// Create regular user with session + API key
	user, err := mgr.Signup(ctx, "temp@example.com", "tempuser", "Temp User", "TempPass123!", "user")
	if err != nil {
		t.Fatalf("Signup failed: %v", err)
	}
	_, sessToken, _, err := mgr.Login(ctx, "tempuser", "TempPass123!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	apiToken, _, err := mgr.KeyManager().GenerateAPIKey(ctx, user.ID, "temp-key", nil)
	if err != nil {
		t.Fatalf("GenerateAPIKey failed: %v", err)
	}

	// Delete user
	if err := mgr.DeleteUser(ctx, user.ID); err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}

	if got, err := mgr.UserByID(ctx, user.ID); err != nil || got != nil {
		t.Fatalf("expected user to be deleted, got user=%v err=%v", got, err)
	}

	// Session and API key must no longer authenticate
	sessReq := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	sessReq.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessToken})
	if _, err := mgr.AuthenticateRequest(sessReq); err == nil {
		t.Fatal("expected deleted user's session cookie to be rejected")
	}

	keyReq := httptest.NewRequest("GET", "/api/v1/vms", nil)
	keyReq.Header.Set("Authorization", "Bearer "+apiToken)
	if _, err := mgr.AuthenticateRequest(keyReq); err == nil {
		t.Fatal("expected deleted user's API key to be rejected")
	}
}

func TestManager_KeyPrefixAndLegacyCompatibility(t *testing.T) {
	mgr, st, _ := setupTestManager(t)
	defer st.Close()

	if auth.DefaultKeyPrefix != "cmp_" {
		t.Fatalf("expected DefaultKeyPrefix to be cmp_, got %q", auth.DefaultKeyPrefix)
	}

	ctx := context.Background()
	user, err := mgr.Signup(ctx, "keys@compute.local", "keyuser", "Key User", "StrongPass123!", "user")
	if err != nil {
		t.Fatalf("Signup failed: %v", err)
	}

	token, rec, err := mgr.KeyManager().GenerateAPIKey(ctx, user.ID, "compute-cli", nil)
	if err != nil {
		t.Fatalf("GenerateAPIKey failed: %v", err)
	}
	if !strings.HasPrefix(token, "cmp_") {
		t.Fatalf("expected generated key to start with cmp_, got %q", token)
	}
	if !strings.HasPrefix(rec.Prefix, "cmp_") {
		t.Fatalf("expected stored key prefix to start with cmp_, got %q", rec.Prefix)
	}

	// Authenticate with standard cmp_ token, cmp_live_ alias, and legacy mfk_ alias.
	variants := []string{
		token,
		"cmp_live_" + strings.TrimPrefix(token, "cmp_"),
		"mfk_" + strings.TrimPrefix(token, "cmp_"),
	}
	for _, tok := range variants {
		req := httptest.NewRequest("GET", "/api/v1/vms", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		authCtx, err := mgr.AuthenticateRequest(req)
		if err != nil {
			t.Fatalf("expected token %q to authenticate, got error: %v", tok[:12], err)
		}
		if authCtx.User.ID != user.ID {
			t.Fatalf("expected user %q, got %q", user.ID, authCtx.User.ID)
		}
	}
}

func TestManager_EnsureAdminInvalidatesStaleSessions(t *testing.T) {
	mgr, st, _ := setupTestManager(t)
	defer st.Close()

	ctx := context.Background()
	admin, err := mgr.EnsureAdmin(ctx, "admin@compute.local", "admin", "Admin", "InitialPass123!")
	if err != nil {
		t.Fatalf("EnsureAdmin failed: %v", err)
	}

	_, sessToken, _, err := mgr.Login(ctx, "admin", "InitialPass123!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessToken})
	if _, err := mgr.AuthenticateRequest(req); err != nil {
		t.Fatalf("expected initial session to authenticate, got: %v", err)
	}

	// Rotate password in-place via EnsureAdmin.
	if _, err := mgr.EnsureAdmin(ctx, "admin@compute.local", "admin", "Admin", "RotatedPass456!"); err != nil {
		t.Fatalf("EnsureAdmin rotation failed: %v", err)
	}

	if _, err := mgr.AuthenticateRequest(req); err == nil {
		t.Fatalf("expected stale session cookie for %s to be invalidated after password rotation", admin.ID)
	}
}
