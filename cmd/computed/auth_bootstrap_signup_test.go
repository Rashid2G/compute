package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/storage"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
)

func newAuthBootstrappedServer(t *testing.T, root string, mutate func(*config.GlobalConfig)) *Server {
	t.Helper()
	dbStore, err := db.NewStore(filepath.Join(root, "db", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbStore.Close() })

	imagesDir := filepath.Join(root, "images")
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imagesDir, "rootfs.ext4"), []byte("test rootfs"), 0o600); err != nil {
		t.Fatal(err)
	}

	storMgr, err := storage.NewManager(storage.Config{
		BaseImagesDir: imagesDir,
		VMsDir:        filepath.Join(root, "vms"),
		DefaultRootfs: "rootfs.ext4",
	})
	if err != nil {
		t.Fatal(err)
	}

	netMgr, err := network.NewManager(network.Config{SubnetCIDR: "192.168.55.0/24"}, testRunner{})
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfig()
	cfg.ConfigPath = "config/daemon.json"
	cfg.Jobs.LocalExecution = false
	cfg.Server.AuthEnabled = true
	cfg.Server.AuthDBPath = filepath.Join(root, "db", "auth.db")
	cfg.Server.AdminSecret = "test-admin-secret-xyz"
	if mutate != nil {
		mutate(&cfg)
	}

	vmMgr := vm.NewManager(vm.Config{}, dbStore, netMgr, storMgr)
	srv := NewServer(vmMgr, cfg)
	t.Cleanup(func() {
		if srv.metaStore != nil {
			_ = srv.metaStore.Close()
		}
	})
	return srv
}

func TestAuthStatusEndpoint_PublicAndReflectsConfig(t *testing.T) {
	root := t.TempDir()
	srv := newAuthBootstrappedServer(t, root, func(cfg *config.GlobalConfig) {
		cfg.Server.AuthEnabled = true
		cfg.Server.SignupEnabled = false
		cfg.Server.AdminUsername = "opsroot"
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, raw := mtRequest(t, ts, "GET", "/api/v1/auth/status", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on /api/v1/auth/status, got %d: %s", resp.StatusCode, string(raw))
	}
	var payload struct {
		AuthEnabled   bool   `json:"auth_enabled"`
		SignupEnabled bool   `json:"signup_enabled"`
		AdminUsername string `json:"admin_username"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode /auth/status: %v", err)
	}
	if !payload.AuthEnabled || payload.SignupEnabled || payload.AdminUsername != "opsroot" {
		t.Fatalf("expected auth_enabled=true, signup_enabled=false, admin_username=opsroot; got %+v", payload)
	}
}

func TestSignupDisabledByDefault_BlocksPublicAndAllowsAdmin(t *testing.T) {
	root := t.TempDir()
	srv := newAuthBootstrappedServer(t, root, func(cfg *config.GlobalConfig) {
		cfg.Server.SignupEnabled = false
		cfg.Server.AdminUsername = "opsadmin"
		cfg.Server.AdminEmail = "opsadmin@compute.local"
		cfg.Server.AdminPassword = "OpsPassword123!"
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// 1. Unauthenticated signup rejected with 403 signup_disabled
	resp, raw := mtRequest(t, ts, "POST", "/api/v1/auth/signup", "", map[string]string{
		"email":    "public@example.com",
		"username": "publicuser",
		"password": "StrongPassword123!",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 when signup_enabled=false, got %d: %s", resp.StatusCode, string(raw))
	}
	if !strings.Contains(string(raw), "signup_disabled") {
		t.Fatalf("expected signup_disabled error code, got: %s", string(raw))
	}

	// 2. Admin login succeeds using configured AdminUsername & AdminPassword
	loginResp, loginRaw := mtRequest(t, ts, "POST", "/api/v1/auth/login", "", map[string]string{
		"username": "opsadmin",
		"password": "OpsPassword123!",
	})
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("admin login failed (%d): %s", loginResp.StatusCode, string(loginRaw))
	}
	var loginPayload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(loginRaw, &loginPayload); err != nil || loginPayload.Token == "" {
		t.Fatalf("missing admin token in login response: %s", string(loginRaw))
	}

	// 3. Authenticated admin can create users via POST /api/v1/users even when signup_enabled=false
	createResp, createRaw := mtRequest(t, ts, "POST", "/api/v1/users", loginPayload.Token, map[string]string{
		"email":        "dev1@example.com",
		"username":     "dev1",
		"display_name": "Dev One",
		"password":     "StrongPassword123!",
		"role":         "user",
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("admin POST /api/v1/users failed (%d): %s", createResp.StatusCode, string(createRaw))
	}

	// 4. Authenticated admin can also call /api/v1/auth/signup to provision a user
	adminSignupResp, adminSignupRaw := mtRequest(t, ts, "POST", "/api/v1/auth/signup", loginPayload.Token, map[string]string{
		"email":    "dev2@example.com",
		"username": "dev2",
		"password": "StrongPassword123!",
	})
	if adminSignupResp.StatusCode != http.StatusCreated {
		t.Fatalf("admin POST /api/v1/auth/signup failed (%d): %s", adminSignupResp.StatusCode, string(adminSignupRaw))
	}

	// 5. Admin can list users via GET /api/v1/users and get single user via GET /api/v1/users/{username}
	listResp, listRaw := mtRequest(t, ts, "GET", "/api/v1/users", loginPayload.Token, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("admin GET /api/v1/users failed (%d): %s", listResp.StatusCode, string(listRaw))
	}
	if !strings.Contains(string(listRaw), `"dev1"`) || !strings.Contains(string(listRaw), `"opsadmin"`) {
		t.Fatalf("expected listed users to contain opsadmin and dev1, got: %s", string(listRaw))
	}

	getResp, getRaw := mtRequest(t, ts, "GET", "/api/v1/users/dev1", loginPayload.Token, nil)
	if getResp.StatusCode != http.StatusOK || !strings.Contains(string(getRaw), `"dev1@example.com"`) {
		t.Fatalf("admin GET /api/v1/users/dev1 failed (%d): %s", getResp.StatusCode, string(getRaw))
	}

	// 6. Non-admin user is forbidden from listing or creating users
	devToken, _ := mtLogin(t, ts, "dev1", "StrongPassword123!")
	nonAdminList, _ := mtRequest(t, ts, "GET", "/api/v1/users", devToken, nil)
	if nonAdminList.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin GET /api/v1/users, got %d", nonAdminList.StatusCode)
	}
	nonAdminCreate, _ := mtRequest(t, ts, "POST", "/api/v1/users", devToken, map[string]string{
		"email":    "dev3@example.com",
		"username": "dev3",
		"password": "StrongPassword123!",
	})
	if nonAdminCreate.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin POST /api/v1/users, got %d", nonAdminCreate.StatusCode)
	}
}

func TestSignupEnabled_IssuesSessionCookieAndToken(t *testing.T) {
	root := t.TempDir()
	srv := newAuthBootstrappedServer(t, root, func(cfg *config.GlobalConfig) {
		cfg.Server.SignupEnabled = true
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, raw := mtRequest(t, ts, "POST", "/api/v1/auth/signup", "", map[string]string{
		"email":        "selfreg@example.com",
		"username":     "selfreg",
		"display_name": "Self Registered",
		"password":     "SelfRegPass123!",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 on public signup, got %d: %s", resp.StatusCode, string(raw))
	}

	var body struct {
		Token string `json:"token"`
		CSRF  string `json:"csrf"`
		User  struct {
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode signup response: %v", err)
	}
	if body.Token == "" || body.CSRF == "" || body.User.Username != "selfreg" || body.User.Role != "user" {
		t.Fatalf("expected token, csrf, and user in signup response, got: %s", string(raw))
	}

	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "compute_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatal("expected compute_session cookie to be set on public signup")
	}

	meReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/auth/me", nil)
	meReq.AddCookie(sessionCookie)
	meResp, err := http.DefaultClient.Do(meReq)
	if err != nil {
		t.Fatalf("GET /api/v1/auth/me with signup cookie: %v", err)
	}
	defer meResp.Body.Close()
	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("expected /auth/me to succeed with signup session cookie, got %d", meResp.StatusCode)
	}
}

func TestAdminBootstrapSyncsPasswordOnRestart(t *testing.T) {
	root := t.TempDir()

	// First boot with initial admin password
	srv1 := newAuthBootstrappedServer(t, root, func(cfg *config.GlobalConfig) {
		cfg.Server.AdminUsername = "rootops"
		cfg.Server.AdminEmail = "rootops@compute.local"
		cfg.Server.AdminPassword = "FirstPassword123!"
	})
	ts1 := httptest.NewServer(srv1)
	_, _ = mtLogin(t, ts1, "rootops", "FirstPassword123!")
	ts1.Close()
	if srv1.metaStore != nil {
		_ = srv1.metaStore.Close()
		srv1.metaStore = nil
	}

	// Second boot against same DB with updated COMPUTE_ADMIN_PASSWORD
	srv2 := newAuthBootstrappedServer(t, root, func(cfg *config.GlobalConfig) {
		cfg.Server.AdminUsername = "rootops"
		cfg.Server.AdminEmail = "rootops@compute.local"
		cfg.Server.AdminPassword = "UpdatedPassword456!"
	})
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()

	oldResp, _ := mtRequest(t, ts2, "POST", "/api/v1/auth/login", "", map[string]string{
		"username": "rootops",
		"password": "FirstPassword123!",
	})
	if oldResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected old password to be rejected after restart sync, got %d", oldResp.StatusCode)
	}

	newToken, _ := mtLogin(t, ts2, "rootops", "UpdatedPassword456!")
	if newToken == "" {
		t.Fatal("expected login with updated admin password to succeed")
	}
}

func TestDeleteUserEndpoint_SuccessAndGuards(t *testing.T) {
	root := t.TempDir()
	srv := newAuthBootstrappedServer(t, root, func(cfg *config.GlobalConfig) {
		cfg.Server.SignupEnabled = false
		cfg.Server.AdminUsername = "opsadmin"
		cfg.Server.AdminEmail = "opsadmin@compute.local"
		cfg.Server.AdminPassword = "OpsPassword123!"
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	adminToken, adminID := mtLogin(t, ts, "opsadmin", "OpsPassword123!")

	// Create two regular users and a second admin
	createUser := func(username, email, role string) string {
		resp, raw := mtRequest(t, ts, http.MethodPost, "/api/v1/users", adminToken, map[string]string{
			"email":    email,
			"username": username,
			"password": "StrongPassword123!",
			"role":     role,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create user %s failed (%d): %s", username, resp.StatusCode, string(raw))
		}
		var out struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		}
		if err := json.Unmarshal(raw, &out); err != nil || out.User.ID == "" {
			t.Fatalf("decode created user %s: %v (%s)", username, err, string(raw))
		}
		return out.User.ID
	}

	dev1ID := createUser("dev1", "dev1@example.com", "user")
	_ = createUser("dev2", "dev2@example.com", "user")
	secAdminID := createUser("secadmin", "secadmin@example.com", "admin")

	// dev1 logs in and creates an API key
	dev1Token, _ := mtLogin(t, ts, "dev1", "StrongPassword123!")
	keyResp, keyRaw := mtRequest(t, ts, http.MethodPost, "/api/v1/auth/keys", dev1Token, map[string]string{
		"name": "dev1-cli",
	})
	if keyResp.StatusCode != http.StatusCreated {
		t.Fatalf("create dev1 API key failed (%d): %s", keyResp.StatusCode, string(keyRaw))
	}
	var keyBody struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(keyRaw, &keyBody); err != nil || keyBody.Token == "" {
		t.Fatalf("decode dev1 API key: %v (%s)", err, string(keyRaw))
	}

	// Non-admin cannot delete users
	nonAdminDel, _ := mtRequest(t, ts, http.MethodDelete, "/api/v1/users/"+dev1ID, dev1Token, nil)
	if nonAdminDel.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin DELETE /api/v1/users/{id}, got %d", nonAdminDel.StatusCode)
	}

	// Admin cannot delete themselves
	secAdminToken, _ := mtLogin(t, ts, "secadmin", "StrongPassword123!")
	selfDel, selfRaw := mtRequest(t, ts, http.MethodDelete, "/api/v1/users/"+secAdminID, secAdminToken, nil)
	if selfDel.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on self-deletion, got %d: %s", selfDel.StatusCode, string(selfRaw))
	}

	// Another admin cannot delete the primary configured bootstrap admin
	bootDelByID, bootRaw := mtRequest(t, ts, http.MethodDelete, "/api/v1/users/"+adminID, secAdminToken, nil)
	if bootDelByID.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 deleting primary bootstrap admin by ID, got %d: %s", bootDelByID.StatusCode, string(bootRaw))
	}
	bootDelByName, _ := mtRequest(t, ts, http.MethodDelete, "/api/v1/users/opsadmin", secAdminToken, nil)
	if bootDelByName.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 deleting primary bootstrap admin by username, got %d", bootDelByName.StatusCode)
	}

	// Delete dev1 by ID -> 200 OK, user and tokens revoked
	delResp, delRaw := mtRequest(t, ts, http.MethodDelete, "/api/v1/users/"+dev1ID, adminToken, nil)
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 deleting dev1 by ID, got %d: %s", delResp.StatusCode, string(delRaw))
	}
	getDeleted, _ := mtRequest(t, ts, http.MethodGet, "/api/v1/users/"+dev1ID, adminToken, nil)
	if getDeleted.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after deleting dev1, got %d", getDeleted.StatusCode)
	}
	meWithDeletedSession, _ := mtRequest(t, ts, http.MethodGet, "/api/v1/auth/me", dev1Token, nil)
	if meWithDeletedSession.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for deleted user's session token, got %d", meWithDeletedSession.StatusCode)
	}
	meWithDeletedKey, _ := mtRequest(t, ts, http.MethodGet, "/api/v1/auth/me", keyBody.Token, nil)
	if meWithDeletedKey.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for deleted user's API key, got %d", meWithDeletedKey.StatusCode)
	}

	// Delete dev2 by username -> 200 OK
	delByName, delByNameRaw := mtRequest(t, ts, http.MethodDelete, "/api/v1/users/dev2", adminToken, nil)
	if delByName.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 deleting dev2 by username, got %d: %s", delByName.StatusCode, string(delByNameRaw))
	}

	// Deleting nonexistent user -> 404
	delMissing, _ := mtRequest(t, ts, http.MethodDelete, "/api/v1/users/does-not-exist", adminToken, nil)
	if delMissing.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 deleting nonexistent user, got %d", delMissing.StatusCode)
	}
}

func TestAdminBootstrapRenameInPlaceOnRestart(t *testing.T) {
	root := t.TempDir()

	// Boot 1: default admin / AdminPass123!
	srv1 := newAuthBootstrappedServer(t, root, func(cfg *config.GlobalConfig) {
		cfg.Server.AdminUsername = "admin"
		cfg.Server.AdminEmail = "admin@compute.local"
		cfg.Server.AdminPassword = "AdminPass123!"
	})
	ts1 := httptest.NewServer(srv1)
	_, initialID := mtLogin(t, ts1, "admin", "AdminPass123!")
	ts1.Close()
	if srv1.metaStore != nil {
		_ = srv1.metaStore.Close()
		srv1.metaStore = nil
	}

	// Boot 2: renamed COMPUTE_ADMIN_USERNAME=newops and rotated COMPUTE_ADMIN_PASSWORD=NewPass123!
	srv2 := newAuthBootstrappedServer(t, root, func(cfg *config.GlobalConfig) {
		cfg.Server.AdminUsername = "newops"
		cfg.Server.AdminEmail = "newops@compute.local"
		cfg.Server.AdminPassword = "NewPass123!"
	})
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()

	// Old admin/AdminPass123! must be rejected
	oldResp, _ := mtRequest(t, ts2, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": "admin",
		"password": "AdminPass123!",
	})
	if oldResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for old admin credentials after rename, got %d", oldResp.StatusCode)
	}

	// New newops/NewPass123! logs in with the preserved User.ID
	newToken, newID := mtLogin(t, ts2, "newops", "NewPass123!")
	if newID != initialID {
		t.Fatalf("expected preserved User.ID %q after rename, got %q", initialID, newID)
	}

	// Only 1 user exists (no stale admin left behind)
	listResp, listRaw := mtRequest(t, ts2, http.MethodGet, "/api/v1/users", newToken, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/users failed: %d", listResp.StatusCode)
	}
	var users []map[string]interface{}
	if err := json.Unmarshal(listRaw, &users); err != nil {
		t.Fatalf("decode users list: %v", err)
	}
	if len(users) != 1 || users[0]["username"] != "newops" {
		t.Fatalf("expected single renamed admin user 'newops', got: %s", string(listRaw))
	}
}

func TestSafeConfig_RedactsSecretsEvenForAdmin(t *testing.T) {
	root := t.TempDir()
	srv := newAuthBootstrappedServer(t, root, func(cfg *config.GlobalConfig) {
		cfg.Server.AdminUsername = "opsadmin"
		cfg.Server.AdminEmail = "opsadmin@compute.local"
		cfg.Server.AdminPassword = "TopSecretPassword123!"
		cfg.Server.AdminSecret = "top-secret-bearer-token-xyz"
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Check direct safeConfig(true) and safeConfig(false)
	for _, isAdmin := range []bool{false, true} {
		sc := srv.safeConfig(isAdmin)
		if sc.Server.AdminSecret != "" || sc.Server.AdminPassword != "" {
			t.Fatalf("safeConfig(%v) leaked secrets: secret=%q password=%q", isAdmin, sc.Server.AdminSecret, sc.Server.AdminPassword)
		}
	}

	// Check HTTP GET /api/v1/config as authenticated admin
	adminToken, _ := mtLogin(t, ts, "opsadmin", "TopSecretPassword123!")
	resp, raw := mtRequest(t, ts, http.MethodGet, "/api/v1/config", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/config as admin failed (%d): %s", resp.StatusCode, string(raw))
	}
	var body struct {
		Server map[string]interface{} `json:"server"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode /api/v1/config: %v", err)
	}
	if v, _ := body.Server["admin_secret"].(string); v != "" {
		t.Fatalf("GET /api/v1/config leaked server.admin_secret to admin: %q", v)
	}
	if v, _ := body.Server["admin_password"].(string); v != "" {
		t.Fatalf("GET /api/v1/config leaked server.admin_password to admin: %q", v)
	}
}

