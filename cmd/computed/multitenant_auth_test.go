package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/master/auth"
	"github.com/Rashid2G/on-demand-vms/internal/master/pki"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/storage"
	"github.com/Rashid2G/on-demand-vms/internal/store"
	storeDB "github.com/Rashid2G/on-demand-vms/internal/store/db"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
	"github.com/Rashid2G/on-demand-vms/pkg/api"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

const multiTenantAdminSecret = "admin-bootstrap-secret-999"

type multiTenantEnv struct {
	ts        *httptest.Server
	authMgr   *auth.Manager
	vmStore   *db.Store
	metaStore *store.Store
	vmMgr     *vm.Manager
	server    *Server
	root      string
}

func newMultiTenantServer(t *testing.T) *multiTenantEnv {
	t.Helper()
	root := t.TempDir()

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

	netMgr, err := network.NewManager(network.Config{SubnetCIDR: "192.168.50.0/24"}, testRunner{})
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfig()
	cfg.ConfigPath = "config/daemon.json"
	cfg.Jobs.LocalExecution = false
	cfg.Server.AuthEnabled = true
	cfg.Server.AuthDBPath = filepath.Join(root, "db", "auth.db")
	cfg.Server.AdminSecret = multiTenantAdminSecret

	mk, err := pki.LoadOrCreateMasterKey(filepath.Join(root, "master.key"))
	if err != nil {
		t.Fatal(err)
	}

	metaStore, err := store.Open(cfg.Server.AuthDBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metaStore.Close() })

	authMgr := auth.NewManager(storeDB.New(metaStore.Writer()), mk, 24*time.Hour, multiTenantAdminSecret)
	vmMgr := vm.NewManager(vm.Config{}, dbStore, netMgr, storMgr)
	serverObj := NewServer(vmMgr, cfg)
	serverObj.SetAuthManager(authMgr)

	ts := httptest.NewServer(serverObj)
	t.Cleanup(ts.Close)

	return &multiTenantEnv{
		ts:        ts,
		authMgr:   authMgr,
		vmStore:   dbStore,
		metaStore: metaStore,
		vmMgr:     vmMgr,
		server:    serverObj,
		root:      root,
	}
}

func mtRequest(t *testing.T, ts *httptest.Server, method, path, token string, body interface{}) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	data, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, data
}

func mtSignup(t *testing.T, ts *httptest.Server, email, username, password string) {
	t.Helper()
	resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/auth/signup", "", map[string]string{
		"email":        email,
		"username":     username,
		"display_name": username,
		"password":     password,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("signup %s failed: %d %s", username, resp.StatusCode, string(body))
	}
}

func mtLogin(t *testing.T, ts *httptest.Server, username, password string) (string, string) {
	t.Helper()
	resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": username,
		"password": password,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login %s failed: %d %s", username, resp.StatusCode, string(body))
	}
	var out struct {
		Token string `json:"token"`
		User  struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if out.Token == "" || out.User.ID == "" {
		t.Fatalf("login %s returned no token/user: %s", username, string(body))
	}
	return out.Token, out.User.ID
}

func mtCreateAPIKey(t *testing.T, ts *httptest.Server, sessionToken, name string) string {
	t.Helper()
	resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/auth/keys", sessionToken, map[string]interface{}{"name": name})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create api key failed: %d %s", resp.StatusCode, string(body))
	}
	var out struct {
		Token string          `json:"token"`
		Key   json.RawMessage `json:"key"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode api key response: %v", err)
	}
	if out.Token == "" {
		t.Fatalf("api key response missing token: %s", string(body))
	}
	if !strings.HasPrefix(out.Token, "cmp_live_") {
		t.Fatalf("expected cmp_live_ token, got %q", out.Token)
	}
	return out.Token
}

func mtCreateVM(t *testing.T, ts *httptest.Server, token string, payload map[string]interface{}) (*http.Response, db.VM) {
	t.Helper()
	resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/vms", token, payload)
	var out db.VM
	if resp.StatusCode == http.StatusCreated {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode vm response: %v", err)
		}
	}
	return resp, out
}

// mtAddSSHKey registers a freshly generated public key under the given name and
// returns the stored record.
func mtAddSSHKey(t *testing.T, ts *httptest.Server, token, name string) db.SSHKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatalf("generate ssh key: %v", err)
	}
	authorized, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("encode ssh key: %v", err)
	}
	publicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authorized)))

	resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/keys", token, map[string]interface{}{
		"name":       name,
		"public_key": publicKey,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create ssh key %q failed: %d (%s)", name, resp.StatusCode, string(body))
	}
	var key db.SSHKey
	if err := json.Unmarshal(body, &key); err != nil {
		t.Fatalf("decode ssh key response: %v", err)
	}
	if key.PublicKey != publicKey {
		t.Fatalf("ssh key %q stored with unexpected public key", name)
	}
	return key
}

func mtCreateVolume(t *testing.T, ts *httptest.Server, token, name string) db.Volume {
	t.Helper()
	resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/volumes", token, map[string]interface{}{
		"name":    name,
		"size_gb": 1,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create volume %q failed: %d (%s)", name, resp.StatusCode, string(body))
	}
	var vol db.Volume
	if err := json.Unmarshal(body, &vol); err != nil {
		t.Fatalf("decode volume response: %v", err)
	}
	return vol
}

func TestMultiTenant_AuthLifecycleAndIsolation(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	mtSignup(t, ts, "bob@example.com", "bob", "PasswordBob123!")

	// Cookie/session login + logout revocation.
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	loginBody, _ := json.Marshal(map[string]string{"username": "alice", "password": "PasswordAlice123!"})
	loginResp, err := client.Post(ts.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil || loginResp.StatusCode != http.StatusOK {
		t.Fatalf("cookie login failed: %v %v", err, loginResp)
	}
	loginResp.Body.Close()

	meResp, err := client.Get(ts.URL + "/api/v1/auth/me")
	if err != nil || meResp.StatusCode != http.StatusOK {
		t.Fatalf("cookie-authenticated /me failed: %v %v", err, meResp)
	}
	meResp.Body.Close()

	logoutReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/logout", nil)
	logoutResp, err := client.Do(logoutReq)
	if err != nil || logoutResp.StatusCode != http.StatusOK {
		t.Fatalf("logout failed: %v %v", err, logoutResp)
	}
	logoutResp.Body.Close()

	meResp2, err := client.Get(ts.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	meResp2.Body.Close()
	if meResp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected revoked cookie session to be unauthorized, got %d", meResp2.StatusCode)
	}

	aliceSession, aliceID := mtLogin(t, ts, "alice", "PasswordAlice123!")
	_, bobID := mtLogin(t, ts, "bob", "PasswordBob123!")
	aliceKey := mtCreateAPIKey(t, ts, aliceSession, "alice-cli")
	bobKey := mtCreateAPIKey(t, ts, mtMustSession(t, ts, "bob", "PasswordBob123!"), "bob-cli")

	// API key via Authorization header.
	resp, body := mtRequest(t, ts, http.MethodGet, "/api/v1/auth/me", aliceKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("API key auth failed: %d %s", resp.StatusCode, string(body))
	}

	// API key via X-API-Key header.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/me", nil)
	req.Header.Set("X-API-Key", aliceKey)
	xResp, err := http.DefaultClient.Do(req)
	if err != nil || xResp.StatusCode != http.StatusOK {
		t.Fatalf("X-API-Key auth failed: %v %v", err, xResp)
	}
	xResp.Body.Close()

	// Query-param token auth rejected on standard REST CRUD endpoints.
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/auth/me?token="+url.QueryEscape(aliceKey), "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("query token auth on REST endpoint succeeded: %d %s, want 401", resp.StatusCode, string(body))
	}

	// Query-param token auth rejected on REST endpoints even if resource name contains "ws" (adversarial regression).
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/vms/ws-production?token="+url.QueryEscape(aliceKey), "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("query token auth on REST /api/v1/vms/ws-production succeeded: %d %s, want 401", resp.StatusCode, string(body))
	}
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/sandboxes/ws-test?token="+url.QueryEscape(aliceKey), "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("query token auth on REST /api/v1/sandboxes/ws-test succeeded: %d %s, want 401", resp.StatusCode, string(body))
	}

	// Query-param WebSocket auth without custom headers.
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws?token=" + url.QueryEscape(aliceKey)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket query-token auth failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	_, wsData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("websocket read failed: %v", err)
	}
	var snapshot map[string]interface{}
	if err := json.Unmarshal(wsData, &snapshot); err != nil || snapshot["type"] != "snapshot" {
		t.Fatalf("unexpected websocket snapshot: %s", string(wsData))
	}

	// Alice creates a VM while trying to spoof the legacy "default" owner.
	createResp, aliceVM := mtCreateVM(t, ts, aliceKey, map[string]interface{}{
		"name":      "alice-vm",
		"tier":      "1vcpu",
		"memory_mb": 1024,
		"owner_id":  "default",
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("alice VM create failed: %d", createResp.StatusCode)
	}
	if aliceVM.OwnerID != aliceID {
		t.Fatalf("owner spoof not blocked: owner_id=%q want %q", aliceVM.OwnerID, aliceID)
	}

	// Bob cannot see or manipulate Alice's VM.
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/vms", bobKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob list failed: %d", resp.StatusCode)
	}
	var bobVMs []db.VM
	_ = json.Unmarshal(body, &bobVMs)
	if len(bobVMs) != 0 {
		t.Fatalf("bob saw %d VMs belonging to other tenants", len(bobVMs))
	}

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/vms/alice-vm"},
		{http.MethodDelete, "/api/v1/vms/alice-vm"},
		{http.MethodPost, "/api/v1/vms/alice-vm/exec"},
		{http.MethodPost, "/api/v1/vms/alice-vm/clone"},
		{http.MethodPost, "/api/v1/vms/alice-vm/run"},
		{http.MethodGet, "/api/v1/ssh/alice-vm"},
		{http.MethodGet, "/api/v1/vms/alice-vm/terminal"},
		{http.MethodGet, "/api/v1/vms/alice-vm/ssh"},
		{http.MethodGet, "/api/v1/network/inspect/alice-vm"},
	} {
		resp, body := mtRequest(t, ts, tc.method, tc.path, bobKey, map[string]interface{}{})
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("bob %s %s: expected 404, got %d (%s)", tc.method, tc.path, resp.StatusCode, string(body))
		}
	}

	// Bob cannot spoof Alice's owner on creation.
	spoofResp, bobVM := mtCreateVM(t, ts, bobKey, map[string]interface{}{
		"name":      "bob-vm",
		"tier":      "1vcpu",
		"memory_mb": 1024,
		"owner_id":  aliceID,
	})
	if spoofResp.StatusCode != http.StatusCreated {
		t.Fatalf("bob VM create failed: %d", spoofResp.StatusCode)
	}
	if bobVM.OwnerID != bobID {
		t.Fatalf("bob spoofed owner: got %q want %q", bobVM.OwnerID, bobID)
	}

	// Legacy "default"-owned resources are private to admins when auth is on.
	if err := env.vmStore.SaveVM(context.Background(), &db.VM{
		ID:         "default-vm",
		OwnerID:    "default",
		Name:       "default-vm",
		Status:     db.StatusStopped,
		VCPUs:      1,
		MemoryMB:   512,
		IPAddress:  "192.168.50.240",
		MACAddress: "02:fc:00:00:00:24",
		TAPDevice:  "tap-default",
		RootfsPath: filepath.Join(env.root, "rootfs.ext4"),
		KernelPath: filepath.Join(env.root, "kernel"),
		DiskPath:   filepath.Join(env.root, "disk"),
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{aliceKey, bobKey} {
		resp, _ := mtRequest(t, ts, http.MethodGet, "/api/v1/vms/default-vm", key, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("non-admin accessed default-owned VM: got %d", resp.StatusCode)
		}
	}
	resp, _ = mtRequest(t, ts, http.MethodGet, "/api/v1/vms/default-vm", multiTenantAdminSecret, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin should access default-owned VM, got %d", resp.StatusCode)
	}

	// Admin-only operational endpoints are forbidden for non-admins.
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/workers"},
		{http.MethodPost, "/api/v1/network/prune"},
		{http.MethodGet, "/api/v1/worker-tokens"},
		{http.MethodGet, "/api/v1/pools"},
	} {
		resp, body := mtRequest(t, ts, tc.method, tc.path, aliceKey, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("non-admin %s %s: expected 403, got %d (%s)", tc.method, tc.path, resp.StatusCode, string(body))
		}
	}

	// Live limits reflect real VM usage.
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/limits", aliceKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("limits failed: %d", resp.StatusCode)
	}
	var limits struct {
		Usage struct {
			Instances int `json:"instances"`
			VCPUs     int `json:"vcpus"`
			MemoryMB  int `json:"memory_mb"`
			DiskGB    int `json:"disk_gb"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &limits); err != nil {
		t.Fatalf("decode limits: %v", err)
	}
	if limits.Usage.Instances != 1 || limits.Usage.VCPUs != 1 || limits.Usage.MemoryMB != 1024 {
		t.Fatalf("unexpected live usage: %+v", limits.Usage)
	}

	// Trigger rootfs volume sync and confirm disk usage is reported.
	resp, _ = mtRequest(t, ts, http.MethodGet, "/api/v1/volumes", aliceKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("volumes list failed: %d", resp.StatusCode)
	}
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/limits", aliceKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("limits failed: %d", resp.StatusCode)
	}
	_ = json.Unmarshal(body, &limits)
	if limits.Usage.DiskGB <= 0 {
		t.Fatalf("expected non-zero disk usage after volume sync, got %+v", limits.Usage)
	}
}

func mtMustSession(t *testing.T, ts *httptest.Server, username, password string) string {
	t.Helper()
	token, _ := mtLogin(t, ts, username, password)
	return token
}

func TestMultiTenant_ResourceIsolation(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	mtSignup(t, ts, "bob@example.com", "bob", "PasswordBob123!")
	aliceSession, aliceID := mtLogin(t, ts, "alice", "PasswordAlice123!")
	bobSession, bobID := mtLogin(t, ts, "bob", "PasswordBob123!")
	aliceKey := mtCreateAPIKey(t, ts, aliceSession, "alice-cli")
	bobKey := mtCreateAPIKey(t, ts, bobSession, "bob-cli")

	createResp, _ := mtCreateVM(t, ts, aliceKey, map[string]interface{}{
		"name":      "alice-vm",
		"tier":      "1vcpu",
		"memory_mb": 1024,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("alice VM create failed: %d", createResp.StatusCode)
	}

	// ---- SSH keys ----
	resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/keys", aliceKey, map[string]string{
		"name":       "alice-key",
		"public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKVMQHURyGt9mXytbJpOeTM4z4jIIhwMQ+aeXq/vDwAj alice@client",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice key create failed: %d %s", resp.StatusCode, string(body))
	}
	var aliceSSHKey db.SSHKey
	_ = json.Unmarshal(body, &aliceSSHKey)
	if aliceSSHKey.OwnerID != aliceID {
		t.Fatalf("ssh key owner = %q, want %q", aliceSSHKey.OwnerID, aliceID)
	}
	if resp, _ := mtRequest(t, ts, http.MethodGet, "/api/v1/keys/"+aliceSSHKey.ID, bobKey, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob read alice ssh key: got %d", resp.StatusCode)
	}
	if resp, _ := mtRequest(t, ts, http.MethodDelete, "/api/v1/keys/"+aliceSSHKey.ID, bobKey, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob deleted alice ssh key: got %d", resp.StatusCode)
	}
	// Legacy default-owned key is invisible to non-admins.
	if err := env.vmStore.SaveSSHKey(ctx, &db.SSHKey{
		ID:          "ssh-legacy",
		OwnerID:     "default",
		Name:        "legacy-key",
		PublicKey:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKVMQHURyGt9mXytbJpOeTM4z4jIIhwMQ+aeXq/vDwAj legacy@client",
		Fingerprint: "SHA256:legacy",
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/keys", aliceKey, nil)
	var keyList struct {
		Keys []db.SSHKey `json:"keys"`
	}
	_ = json.Unmarshal(body, &keyList)
	for _, k := range keyList.Keys {
		if k.ID == "ssh-legacy" {
			t.Fatalf("non-admin saw legacy default ssh key")
		}
	}
	if resp, _ := mtRequest(t, ts, http.MethodGet, "/api/v1/keys/ssh-legacy", aliceKey, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("non-admin read legacy default ssh key: got %d", resp.StatusCode)
	}

	// ---- Volumes ----
	seedVolumes := []*db.Volume{
		{ID: "vol-alice", OwnerID: aliceID, Name: "alice-data", SizeGB: 10, Status: "available", Filesystem: "ext4"},
		{ID: "vol-bob", OwnerID: bobID, Name: "bob-data", SizeGB: 10, Status: "available", Filesystem: "ext4"},
		{ID: "vol-legacy", OwnerID: "default", Name: "legacy-data", SizeGB: 10, Status: "available", Filesystem: "ext4"},
	}
	for _, v := range seedVolumes {
		if err := env.vmStore.SaveVolume(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/volumes", aliceKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice volumes list failed: %d", resp.StatusCode)
	}
	var aliceVols []db.Volume
	_ = json.Unmarshal(body, &aliceVols)
	for _, v := range aliceVols {
		if v.ID == "vol-bob" || v.ID == "vol-legacy" {
			t.Fatalf("alice saw foreign volume %q", v.ID)
		}
	}
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/volumes/vol-alice"},
		{http.MethodDelete, "/api/v1/volumes/vol-alice"},
	} {
		resp, _ := mtRequest(t, ts, tc.method, tc.path, bobKey, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("bob %s %s: expected 404, got %d", tc.method, tc.path, resp.StatusCode)
		}
	}

	// ---- Jobs ----
	resp, body = mtRequest(t, ts, http.MethodPost, "/api/v1/jobs", aliceKey, map[string]interface{}{
		"type": "command",
		"spec": map[string]interface{}{"command": []string{"echo", "hi"}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice job create failed: %d %s", resp.StatusCode, string(body))
	}
	var aliceJob db.Job
	_ = json.Unmarshal(body, &aliceJob)
	if aliceJob.OwnerID != aliceID {
		t.Fatalf("job owner = %q, want %q", aliceJob.OwnerID, aliceID)
	}
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/jobs", bobKey, nil)
	var bobJobs []db.Job
	_ = json.Unmarshal(body, &bobJobs)
	if len(bobJobs) != 0 {
		t.Fatalf("bob saw %d foreign jobs", len(bobJobs))
	}
	if resp, _ := mtRequest(t, ts, http.MethodGet, "/api/v1/jobs/"+aliceJob.ID, bobKey, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob read alice job: got %d", resp.StatusCode)
	}
	if resp, _ := mtRequest(t, ts, http.MethodPost, "/api/v1/jobs/"+aliceJob.ID+"/cancel", bobKey, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob cancelled alice job: got %d", resp.StatusCode)
	}

	// ---- Port forwards ----
	resp, body = mtRequest(t, ts, http.MethodPost, "/api/v1/network/port-forwards", aliceKey, map[string]interface{}{
		"host_port":  31000,
		"vm_id":      "alice-vm",
		"guest_port": 8080,
		"protocol":   "tcp",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice port forward create failed: %d %s", resp.StatusCode, string(body))
	}
	var alicePF db.PortForward
	_ = json.Unmarshal(body, &alicePF)
	if alicePF.OwnerID != aliceID {
		t.Fatalf("port forward owner = %q, want %q", alicePF.OwnerID, aliceID)
	}
	resp, body = mtRequest(t, ts, http.MethodPost, "/api/v1/network/port-forwards", bobKey, map[string]interface{}{
		"host_port":  31001,
		"vm_id":      "alice-vm",
		"guest_port": 8080,
		"protocol":   "tcp",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob created port forward for alice VM: got %d %s", resp.StatusCode, string(body))
	}
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/network/port-forwards", bobKey, nil)
	var bobPFs []db.PortForward
	_ = json.Unmarshal(body, &bobPFs)
	if len(bobPFs) != 0 {
		t.Fatalf("bob saw %d foreign port forwards", len(bobPFs))
	}
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/network/port-forwards/" + alicePF.ID},
		{http.MethodDelete, "/api/v1/network/port-forwards/" + alicePF.ID},
	} {
		resp, _ := mtRequest(t, ts, tc.method, tc.path, bobKey, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("bob %s %s: expected 404, got %d", tc.method, tc.path, resp.StatusCode)
		}
	}

	// ---- Templates ----
	resp, body = mtRequest(t, ts, http.MethodPost, "/api/v1/templates", aliceKey, map[string]string{
		"name":  "alice-template",
		"vm_id": "alice-vm",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice template create failed: %d %s", resp.StatusCode, string(body))
	}
	var aliceTpl db.Template
	_ = json.Unmarshal(body, &aliceTpl)
	if aliceTpl.OwnerID != aliceID {
		t.Fatalf("template owner = %q, want %q", aliceTpl.OwnerID, aliceID)
	}
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/templates", bobKey, nil)
	var bobTpls []db.Template
	_ = json.Unmarshal(body, &bobTpls)
	if len(bobTpls) != 0 {
		t.Fatalf("bob saw %d foreign templates", len(bobTpls))
	}
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/templates/alice-template"},
		{http.MethodDelete, "/api/v1/templates/alice-template"},
		{http.MethodPost, "/api/v1/templates/alice-template/launch"},
	} {
		resp, _ := mtRequest(t, ts, tc.method, tc.path, bobKey, map[string]interface{}{})
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("bob %s %s: expected 404, got %d", tc.method, tc.path, resp.StatusCode)
		}
	}

	// ---- Custom images ----
	aliceImage := &db.CustomImage{
		ID:         "img-alice",
		OwnerID:    aliceID,
		Name:       "alice-image",
		BaseImage:  "ubuntu-24.04",
		Packages:   []string{"curl"},
		RootfsPath: filepath.Join(env.root, "alice-image.ext4"),
		KernelPath: filepath.Join(env.root, "kernel"),
		SizeMB:     1024,
		Status:     db.ImageStatusReady,
	}
	if err := env.vmStore.SaveImage(ctx, aliceImage); err != nil {
		t.Fatal(err)
	}
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/images", bobKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob images list failed: %d", resp.StatusCode)
	}
	var images []map[string]interface{}
	_ = json.Unmarshal(body, &images)
	sawBase := false
	for _, img := range images {
		if img["id"] == "img-alice" {
			t.Fatalf("bob saw alice's custom image")
		}
		if img["kind"] == "base" {
			sawBase = true
		}
	}
	if !sawBase {
		t.Fatalf("expected base images to remain publicly visible")
	}
	if resp, _ := mtRequest(t, ts, http.MethodGet, "/api/v1/images/img-alice", bobKey, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob read alice image: got %d", resp.StatusCode)
	}
	if resp, _ := mtRequest(t, ts, http.MethodDelete, "/api/v1/images/img-alice", bobKey, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob deleted alice image: got %d", resp.StatusCode)
	}
	if resp, _ := mtRequest(t, ts, http.MethodGet, "/api/v1/images/img-alice", aliceKey, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("alice could not read own image: got %d", resp.StatusCode)
	}
	if resp, _ := mtCreateVM(t, ts, bobKey, map[string]interface{}{
		"name":      "bob-from-alice-image",
		"tier":      "1vcpu",
		"memory_mb": 1024,
		"image":     "alice-image",
	}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob launched VM from alice's image: got %d", resp.StatusCode)
	}

	// ---- Network allocations ----
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/network/allocations", aliceKey, nil)
	var aliceAllocs []db.NetworkAllocation
	_ = json.Unmarshal(body, &aliceAllocs)
	sawAlice := false
	for _, a := range aliceAllocs {
		if a.VMName == "alice-vm" || a.VMID == "alice-vm" {
			sawAlice = true
		}
	}
	if !sawAlice {
		t.Fatalf("alice could not see her own network allocation")
	}
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/network/allocations", bobKey, nil)
	var bobAllocs []db.NetworkAllocation
	_ = json.Unmarshal(body, &bobAllocs)
	if len(bobAllocs) != 0 {
		t.Fatalf("bob saw %d foreign network allocations", len(bobAllocs))
	}
}

func TestMultiTenant_QuotaEnforcement(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	mtSignup(t, ts, "quota@example.com", "quota", "PasswordQuota123!")
	session, userID := mtLogin(t, ts, "quota", "PasswordQuota123!")
	apiKey := mtCreateAPIKey(t, ts, session, "quota-cli")

	setQuota := func(quotaJSON string) {
		if _, err := env.metaStore.Writer().ExecContext(ctx, `UPDATE users SET quota_json = ? WHERE id = ?`, quotaJSON, userID); err != nil {
			t.Fatalf("update quota: %v", err)
		}
	}

	setQuota(`{"instances":1,"running_instances":1,"vcpus":1,"memory_mb":1024,"disk_gb":50,"vpcs":1,"api_keys":10}`)
	resp, _ := mtCreateVM(t, ts, apiKey, map[string]interface{}{"name": "quota-vm-1", "tier": "1vcpu", "memory_mb": 1024})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first VM should fit quota, got %d", resp.StatusCode)
	}
	resp, _ = mtCreateVM(t, ts, apiKey, map[string]interface{}{"name": "quota-vm-2", "tier": "1vcpu", "memory_mb": 1024})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second VM should exceed instance quota, got %d", resp.StatusCode)
	}

	resp, body := mtRequest(t, ts, http.MethodGet, "/api/v1/limits", apiKey, nil)
	var limits struct {
		Usage struct {
			Instances int `json:"instances"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(body, &limits)
	if limits.Usage.Instances != 1 {
		t.Fatalf("expected usage.instances=1, got %d", limits.Usage.Instances)
	}

	// Concurrent-run quota: one VM already running, the next start must be refused.
	setQuota(`{"instances":10,"running_instances":1,"vcpus":16,"memory_mb":32768,"disk_gb":500,"vpcs":5,"api_keys":20}`)
	if err := env.vmStore.SaveVM(ctx, &db.VM{
		ID:         "already-running",
		OwnerID:    userID,
		Name:       "already-running",
		Status:     db.StatusRunning,
		VCPUs:      1,
		MemoryMB:   1024,
		IPAddress:  "192.168.50.241",
		MACAddress: "02:fc:00:00:00:25",
		TAPDevice:  "tap-running",
		RootfsPath: filepath.Join(env.root, "rootfs.ext4"),
		KernelPath: filepath.Join(env.root, "kernel"),
		DiskPath:   filepath.Join(env.root, "disk-running"),
	}); err != nil {
		t.Fatal(err)
	}
	resp, _ = mtCreateVM(t, ts, apiKey, map[string]interface{}{"name": "quota-vm-3", "tier": "1vcpu", "memory_mb": 1024})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("third VM create failed under generous quota: %d", resp.StatusCode)
	}
	resp, body = mtRequest(t, ts, http.MethodPost, "/api/v1/vms/quota-vm-3/run", apiKey, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected run quota rejection, got %d (%s)", resp.StatusCode, string(body))
	}
}

func TestMultiTenant_ScopedAccessKeysAuthenticate(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	mtSignup(t, ts, "bob@example.com", "bob", "PasswordBob123!")
	aliceSession, aliceID := mtLogin(t, ts, "alice", "PasswordAlice123!")
	bobSession, _ := mtLogin(t, ts, "bob", "PasswordBob123!")
	aliceKey := mtCreateAPIKey(t, ts, aliceSession, "alice-cli")
	bobKey := mtCreateAPIKey(t, ts, bobSession, "bob-cli")

	if resp, _ := mtCreateVM(t, ts, aliceKey, map[string]interface{}{"name": "alice-vm", "tier": "1vcpu", "memory_mb": 1024}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice VM create failed: %d", resp.StatusCode)
	}

	createScoped := func(userKey string) string {
		resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/api-keys", userKey, map[string]string{
			"name":  "scoped-key",
			"scope": "user",
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("scoped key create failed: %d %s", resp.StatusCode, string(body))
		}
		var out db.AccessAPIKey
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode scoped key: %v", err)
		}
		if !strings.HasPrefix(out.Token, "cmp_live_") {
			t.Fatalf("unexpected scoped token %q", out.Token)
		}
		return out.Token
	}

	aliceScoped := createScoped(aliceKey)
	bobScoped := createScoped(bobKey)

	resp, body := mtRequest(t, ts, http.MethodGet, "/api/v1/auth/me", aliceScoped, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scoped key auth failed: %d %s", resp.StatusCode, string(body))
	}
	var me struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	_ = json.Unmarshal(body, &me)
	if me.User.ID != aliceID {
		t.Fatalf("scoped key mapped to %q, want %q", me.User.ID, aliceID)
	}

	// A scoped key must retain its own tenant boundary.
	if resp, _ := mtRequest(t, ts, http.MethodGet, "/api/v1/vms/alice-vm", bobScoped, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob scoped key accessed alice VM: got %d", resp.StatusCode)
	}

	// Query-param fallback rejected on REST endpoints, but permitted on WebSocket routes.
	resp, _ = mtRequest(t, ts, http.MethodGet, "/api/v1/auth/me?api_key="+url.QueryEscape(aliceScoped), "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("scoped key query auth on REST endpoint succeeded: %d, want 401", resp.StatusCode)
	}

	wsScopedURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws?api_key=" + url.QueryEscape(aliceScoped)
	wsCtx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()
	scopedConn, _, err := websocket.Dial(wsCtx, wsScopedURL, nil)
	if err != nil {
		t.Fatalf("scoped key websocket query auth failed: %v", err)
	}
	defer scopedConn.Close(websocket.StatusNormalClosure, "")
}

func mtRequestWithHeaders(t *testing.T, ts *httptest.Server, method, path, token string, headers map[string]string, body interface{}) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	data, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, data
}

// TestMultiTenant_OrgHeaderSpoofingBlocked ensures X-Compute-Org cannot be used
// to cross tenant boundaries: a caller may only select an organization they are
// a confirmed member of.
func TestMultiTenant_OrgHeaderSpoofingBlocked(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	mtSignup(t, ts, "bob@example.com", "bob", "PasswordBob123!")
	aliceSession, aliceID := mtLogin(t, ts, "alice", "PasswordAlice123!")
	bobSession, bobID := mtLogin(t, ts, "bob", "PasswordBob123!")
	aliceKey := mtCreateAPIKey(t, ts, aliceSession, "alice-cli")
	bobKey := mtCreateAPIKey(t, ts, bobSession, "bob-cli")

	resp, _ := mtCreateVM(t, ts, aliceKey, map[string]interface{}{
		"name":      "alice-vm",
		"tier":      "1vcpu",
		"memory_mb": 1024,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice VM create failed: %d", resp.StatusCode)
	}

	aliceOrg := "org_" + strings.TrimPrefix(aliceID, "usr_")
	bobOrg := "org_" + strings.TrimPrefix(bobID, "usr_")

	// Bob claims Alice's organization: must not grant access.
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/vms/alice-vm"},
		{http.MethodDelete, "/api/v1/vms/alice-vm"},
		{http.MethodPost, "/api/v1/vms/alice-vm/run"},
		{http.MethodGet, "/api/v1/vms"},
	} {
		resp, body := mtRequestWithHeaders(t, ts, tc.method, tc.path, bobKey, map[string]string{"X-Compute-Org": aliceOrg}, nil)
		if tc.path == "/api/v1/vms" {
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("bob list with spoofed org: expected 200, got %d (%s)", resp.StatusCode, string(body))
			}
			var listed []db.VM
			_ = json.Unmarshal(body, &listed)
			if len(listed) != 0 {
				t.Fatalf("bob saw %d VMs with spoofed org header", len(listed))
			}
			continue
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("bob %s %s with spoofed org: expected 404, got %d (%s)", tc.method, tc.path, resp.StatusCode, string(body))
		}
	}

	// Bob's own organization remains valid.
	resp, body := mtRequestWithHeaders(t, ts, http.MethodGet, "/api/v1/vms", bobKey, map[string]string{"X-Compute-Org": bobOrg}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob list with own org failed: %d (%s)", resp.StatusCode, string(body))
	}

	// Alice still sees her own VM with her own organization.
	resp, body = mtRequestWithHeaders(t, ts, http.MethodGet, "/api/v1/vms/alice-vm", aliceKey, map[string]string{"X-Compute-Org": aliceOrg}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice lost access to her VM: %d (%s)", resp.StatusCode, string(body))
	}
}

// TestMultiTenant_DiskQuotaLiveEnforcement verifies disk usage is derived from
// live VM records immediately and that volume creation is quota-checked.
func TestMultiTenant_DiskQuotaLiveEnforcement(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	mtSignup(t, ts, "disk@example.com", "diskuser", "PasswordDisk123!")
	session, userID := mtLogin(t, ts, "diskuser", "PasswordDisk123!")
	apiKey := mtCreateAPIKey(t, ts, session, "disk-cli")

	if _, err := env.metaStore.Writer().ExecContext(ctx,
		`UPDATE users SET quota_json = ? WHERE id = ?`,
		`{"instances":10,"running_instances":10,"vcpus":32,"memory_mb":65536,"disk_gb":50,"vpcs":10,"api_keys":20}`,
		userID); err != nil {
		t.Fatalf("update quota: %v", err)
	}

	// A single default (50 GB) VM consumes the entire disk quota.
	resp, _ := mtCreateVM(t, ts, apiKey, map[string]interface{}{"name": "disk-vm-1", "tier": "1vcpu", "memory_mb": 1024})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first VM should fit disk quota, got %d", resp.StatusCode)
	}

	// Disk usage must be live immediately; no /volumes sync is required.
	resp, body := mtRequest(t, ts, http.MethodGet, "/api/v1/limits", apiKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("limits failed: %d", resp.StatusCode)
	}
	var limits struct {
		Usage struct {
			DiskGB int `json:"disk_gb"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &limits); err != nil {
		t.Fatalf("decode limits: %v", err)
	}
	if limits.Usage.DiskGB < 50 {
		t.Fatalf("disk usage not live after VM creation: %+v", limits.Usage)
	}

	// A second VM would exceed disk_gb.
	resp, _ = mtCreateVM(t, ts, apiKey, map[string]interface{}{"name": "disk-vm-2", "tier": "1vcpu", "memory_mb": 1024})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second VM should exceed disk quota, got %d", resp.StatusCode)
	}

	// Creating a data volume that would exceed disk_gb is rejected too.
	resp, body = mtRequest(t, ts, http.MethodPost, "/api/v1/volumes", apiKey, map[string]interface{}{
		"name":    "too-big",
		"size_gb": 10,
	})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("volume creation should exceed disk quota, got %d (%s)", resp.StatusCode, string(body))
	}

	// Raising the quota allows volume creation.
	if _, err := env.metaStore.Writer().ExecContext(ctx,
		`UPDATE users SET quota_json = ? WHERE id = ?`,
		`{"instances":10,"running_instances":10,"vcpus":32,"memory_mb":65536,"disk_gb":500,"vpcs":10,"api_keys":20}`,
		userID); err != nil {
		t.Fatalf("update quota: %v", err)
	}
	resp, body = mtRequest(t, ts, http.MethodPost, "/api/v1/volumes", apiKey, map[string]interface{}{
		"name":    "fits-now",
		"size_gb": 10,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("volume creation under higher quota failed: %d (%s)", resp.StatusCode, string(body))
	}
}

// TestMultiTenant_SecretsNotLeakedToNonAdmins verifies bootstrap secrets and
// infrastructure topology are redacted from config and websocket snapshots.
func TestMultiTenant_SecretsNotLeakedToNonAdmins(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	aliceSession, _ := mtLogin(t, ts, "alice", "PasswordAlice123!")
	aliceKey := mtCreateAPIKey(t, ts, aliceSession, "alice-cli")

	assertRedactedConfig := func(raw []byte, context string) {
		t.Helper()
		var payload map[string]interface{}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("%s: decode: %v", context, err)
		}
		serverCfg, _ := payload["server"].(map[string]interface{})
		for _, key := range []string{"admin_secret", "auth_db_path", "db_path"} {
			if v, ok := serverCfg[key]; ok {
				if s, _ := v.(string); s != "" {
					t.Fatalf("%s leaked server.%s", context, key)
				}
			}
		}
	}

	// /api/v1/config must not expose the admin bootstrap secret.
	resp, body := mtRequest(t, ts, http.MethodGet, "/api/v1/config", aliceKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("config failed: %d", resp.StatusCode)
	}
	assertRedactedConfig(body, "GET /api/v1/config")

	// Non-admin websocket snapshots must redact secrets and worker topology.
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/ws?token=" + url.QueryEscape(aliceKey)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	_, wsData, err := conn.Read(ctx)
	_ = conn.Close(websocket.StatusNormalClosure, "")
	if err != nil {
		t.Fatalf("websocket read failed: %v", err)
	}
	var snapshot struct {
		Config  json.RawMessage   `json:"config"`
		Workers []json.RawMessage `json:"workers"`
	}
	if err := json.Unmarshal(wsData, &snapshot); err != nil {
		t.Fatalf("decode websocket snapshot: %v", err)
	}
	assertRedactedConfig(snapshot.Config, "websocket snapshot")
	if len(snapshot.Workers) != 0 {
		t.Fatalf("non-admin websocket snapshot leaked %d workers", len(snapshot.Workers))
	}

	// Admins still receive the real configuration.
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/config", multiTenantAdminSecret, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin config failed: %d", resp.StatusCode)
	}
	var adminPayload map[string]interface{}
	_ = json.Unmarshal(body, &adminPayload)
	adminServer, _ := adminPayload["server"].(map[string]interface{})
	if secret, _ := adminServer["admin_secret"].(string); secret != multiTenantAdminSecret {
		t.Fatalf("admin did not receive admin secret (got %q)", secret)
	}
}

// TestMultiTenant_TemplateViaImageFieldIsolated ensures the "image" field on VM
// creation cannot be used to reach another tenant's template. The manager
// resolves image names to templates when no custom image matches, so the HTTP
// layer must apply the same ownership check to both namespaces.
func TestMultiTenant_TemplateViaImageFieldIsolated(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	mtSignup(t, ts, "bob@example.com", "bob", "PasswordBob123!")
	aliceSession, aliceID := mtLogin(t, ts, "alice", "PasswordAlice123!")
	bobSession, _ := mtLogin(t, ts, "bob", "PasswordBob123!")
	aliceKey := mtCreateAPIKey(t, ts, aliceSession, "alice-cli")
	bobKey := mtCreateAPIKey(t, ts, bobSession, "bob-cli")

	if err := env.vmStore.SaveTemplate(ctx, &db.Template{
		ID:         "tpl-alice",
		OwnerID:    aliceID,
		Name:       "alice-tpl",
		DiskPath:   filepath.Join(env.root, "alice-tpl.ext4"),
		KernelPath: filepath.Join(env.root, "kernel"),
		VCPUs:      1,
		MemoryMB:   1024,
		DiskMB:     1024,
	}); err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"image", "template"} {
		resp, _ := mtCreateVM(t, ts, bobKey, map[string]interface{}{
			"name":      "bob-from-alice-" + field,
			"tier":      "1vcpu",
			"memory_mb": 1024,
			field:       "alice-tpl",
		})
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("bob launched from alice template via %s: expected 404, got %d", field, resp.StatusCode)
		}
	}

	// Alice may resolve her own template name (the VM build then fails because
	// the test fixture has no real template disk, but authorization must pass).
	resp, _ := mtCreateVM(t, ts, aliceKey, map[string]interface{}{
		"name":      "alice-from-own-template",
		"tier":      "1vcpu",
		"memory_mb": 1024,
		"image":     "alice-tpl",
	})
	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("alice was denied her own template via image field: %d", resp.StatusCode)
	}
}

// TestMultiTenant_LegacyDefaultJobsAndSSHKeysAreNotShared ensures authenticated
// tenants never inherit legacy "default"-owned jobs or SSH keys.
func TestMultiTenant_LegacyDefaultJobsAndSSHKeysAreNotShared(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	mtSignup(t, ts, "bob@example.com", "bob", "PasswordBob123!")
	aliceSession, _ := mtLogin(t, ts, "alice", "PasswordAlice123!")
	bobSession, bobID := mtLogin(t, ts, "bob", "PasswordBob123!")
	_ = mtCreateAPIKey(t, ts, aliceSession, "alice-cli")
	bobKey := mtCreateAPIKey(t, ts, bobSession, "bob-cli")

	// Jobs: a legacy default job must not appear in Bob's listing.
	if err := env.vmStore.SaveJob(ctx, &db.Job{
		ID: "job-default", OwnerID: "default", Type: "command", State: "queued",
		SpecJSON: `{"command":["echo","legacy"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.vmStore.SaveJob(ctx, &db.Job{
		ID: "job-bob", OwnerID: bobID, Type: "command", State: "queued",
		SpecJSON: `{"command":["echo","bob"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	resp, body := mtRequest(t, ts, http.MethodGet, "/api/v1/jobs", bobKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob jobs list failed: %d (%s)", resp.StatusCode, string(body))
	}
	var bobJobs []db.Job
	if err := json.Unmarshal(body, &bobJobs); err != nil {
		t.Fatalf("decode bob jobs: %v", err)
	}
	for _, job := range bobJobs {
		if job.ID == "job-default" || job.OwnerID == "default" {
			t.Fatalf("bob saw legacy default-owned job %q", job.ID)
		}
	}
	if len(bobJobs) != 1 || bobJobs[0].ID != "job-bob" {
		t.Fatalf("bob jobs = %+v, want only job-bob", bobJobs)
	}

	// SSH keys: a legacy default key cannot be attached by a tenant.
	if err := env.vmStore.SaveSSHKey(ctx, &db.SSHKey{
		ID:          "ssh-legacy",
		OwnerID:     "default",
		Name:        "legacy-key",
		PublicKey:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKVMQHURyGt9mXytbJpOeTM4z4jIIhwMQ+aeXq/vDwAj legacy@client",
		Fingerprint: "SHA256:legacy",
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	keyResp, _ := mtCreateVM(t, ts, bobKey, map[string]interface{}{
		"name":       "bob-with-legacy-key",
		"tier":       "1vcpu",
		"memory_mb":  1024,
		"ssh_key_id": "ssh-legacy",
	})
	if keyResp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob attached a legacy default-owned ssh key: expected 404, got %d", keyResp.StatusCode)
	}

	// The rejected VM must not exist.
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/vms", bobKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob vm list failed: %d", resp.StatusCode)
	}
	var bobVMs []db.VM
	_ = json.Unmarshal(body, &bobVMs)
	for _, v := range bobVMs {
		if v.Name == "bob-with-legacy-key" {
			t.Fatalf("rejected VM with legacy ssh key was still created")
		}
	}
}

// TestMultiTenant_PlatformEndpointsAdminOnly ensures infrastructure endpoints
// that expose secrets are not readable by ordinary tenants.
func TestMultiTenant_PlatformEndpointsAdminOnly(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	aliceSession, _ := mtLogin(t, ts, "alice", "PasswordAlice123!")
	aliceKey := mtCreateAPIKey(t, ts, aliceSession, "alice-cli")

	for _, path := range []string{
		"/api/v1/webhooks",
		"/api/v1/environments",
		"/api/v1/otel",
		"/api/v1/agent-profiles",
	} {
		resp, body := mtRequest(t, ts, http.MethodGet, path, aliceKey, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("non-admin GET %s: expected 403, got %d (%s)", path, resp.StatusCode, string(body))
		}
	}

	// Admins retain access.
	for _, path := range []string{
		"/api/v1/webhooks",
		"/api/v1/environments",
		"/api/v1/otel",
		"/api/v1/agent-profiles",
	} {
		resp, body := mtRequest(t, ts, http.MethodGet, path, multiTenantAdminSecret, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("admin GET %s: expected 200, got %d (%s)", path, resp.StatusCode, string(body))
		}
	}
}

// TestMultiTenant_AdminSecretRejectedViaQueryParam ensures the bootstrap admin
// secret is only accepted from headers, never from a URL.
func TestMultiTenant_AdminSecretRejectedViaQueryParam(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")

	resp, body := mtRequest(t, ts, http.MethodGet, "/api/v1/auth/me?token="+url.QueryEscape(multiTenantAdminSecret), "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("admin secret accepted from query parameter: got %d (%s)", resp.StatusCode, string(body))
	}

	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/webhooks?token="+url.QueryEscape(multiTenantAdminSecret), "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("admin secret accepted from query parameter on protected route: got %d (%s)", resp.StatusCode, string(body))
	}

	// The same secret still works from the Authorization header.
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/auth/me", multiTenantAdminSecret, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin secret rejected from header: got %d (%s)", resp.StatusCode, string(body))
	}
}

// TestMultiTenant_DuplicateNamesAreIsolated covers the collision cases that
// arise when two tenants are allowed to use the same VM name. Lookups, deletes,
// network allocations and job images must all stay within the caller's tenant.
func TestMultiTenant_DuplicateNamesAreIsolated(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	mtSignup(t, ts, "bob@example.com", "bob", "PasswordBob123!")
	aliceSession, aliceID := mtLogin(t, ts, "alice", "PasswordAlice123!")
	bobSession, bobID := mtLogin(t, ts, "bob", "PasswordBob123!")
	aliceKey := mtCreateAPIKey(t, ts, aliceSession, "alice-cli")
	bobKey := mtCreateAPIKey(t, ts, bobSession, "bob-cli")

	aliceResp, aliceVM := mtCreateVM(t, ts, aliceKey, map[string]interface{}{
		"name":      "shared-vm",
		"tier":      "1vcpu",
		"memory_mb": 1024,
	})
	if aliceResp.StatusCode != http.StatusCreated {
		t.Fatalf("alice duplicate-name VM create failed: %d", aliceResp.StatusCode)
	}
	bobResp, bobVM := mtCreateVM(t, ts, bobKey, map[string]interface{}{
		"name":      "shared-vm",
		"tier":      "1vcpu",
		"memory_mb": 1024,
	})
	if bobResp.StatusCode != http.StatusCreated {
		t.Fatalf("bob duplicate-name VM create failed: %d", bobResp.StatusCode)
	}
	if aliceVM.OwnerID != aliceID || bobVM.OwnerID != bobID {
		t.Fatalf("duplicate-name VMs have wrong owners: alice=%q bob=%q", aliceVM.OwnerID, bobVM.OwnerID)
	}

	// Network allocations must be tenant-scoped even though both VMs are named
	// "shared-vm". Match on the unique IP/MAC assigned to each VM.
	resp, body := mtRequest(t, ts, http.MethodGet, "/api/v1/network/allocations", aliceKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice allocations failed: %d (%s)", resp.StatusCode, string(body))
	}
	var aliceAllocs []db.NetworkAllocation
	if err := json.Unmarshal(body, &aliceAllocs); err != nil {
		t.Fatalf("decode alice allocations: %v", err)
	}
	sawAlice, sawBob := false, false
	for _, a := range aliceAllocs {
		if a.IPAddress == aliceVM.IPAddress && a.MACAddress == aliceVM.MACAddress {
			sawAlice = true
		}
		if a.IPAddress == bobVM.IPAddress || a.MACAddress == bobVM.MACAddress {
			sawBob = true
		}
	}
	if !sawAlice {
		t.Fatalf("alice allocation missing from her own listing: %+v", aliceAllocs)
	}
	if sawBob {
		t.Fatalf("alice saw bob's network allocation: %+v", aliceAllocs)
	}

	// Bob deleting his "shared-vm" must leave Alice's intact.
	resp, body = mtRequest(t, ts, http.MethodDelete, "/api/v1/vms/shared-vm", bobKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob delete of his shared-vm failed: %d (%s)", resp.StatusCode, string(body))
	}
	if resp, _ := mtRequest(t, ts, http.MethodGet, "/api/v1/vms/shared-vm", bobKey, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob's VM still visible after delete: %d", resp.StatusCode)
	}
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/vms/shared-vm", aliceKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice's shared-vm was destroyed by bob's delete: %d (%s)", resp.StatusCode, string(body))
	}
	var fetched db.VM
	if err := json.Unmarshal(body, &fetched); err != nil {
		t.Fatalf("decode alice VM: %v", err)
	}
	if fetched.OwnerID != aliceID {
		t.Fatalf("alice fetched a VM owned by %q", fetched.OwnerID)
	}
	if err := env.vmStore.DeleteVMByOwner(ctx, bobID, "shared-vm"); err != nil {
		t.Fatalf("cleanup bob VM: %v", err)
	}

	// Jobs must not be able to reference another tenant's custom image.
	aliceImage := &db.CustomImage{
		ID:         "img-alice-job",
		OwnerID:    aliceID,
		Name:       "alice-job-image",
		BaseImage:  "ubuntu-24.04",
		RootfsPath: filepath.Join(env.root, "alice-job-image.ext4"),
		KernelPath: filepath.Join(env.root, "kernel"),
		SizeMB:     1024,
		Status:     db.ImageStatusReady,
	}
	if err := env.vmStore.SaveImage(ctx, aliceImage); err != nil {
		t.Fatal(err)
	}
	resp, body = mtRequest(t, ts, http.MethodPost, "/api/v1/jobs", bobKey, map[string]interface{}{
		"type": "command",
		"spec": map[string]interface{}{
			"image_id": "img-alice-job",
			"command":  []string{"echo", "should-not-run"},
		},
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob created a job from alice's image: got %d (%s)", resp.StatusCode, string(body))
	}
	resp, body = mtRequest(t, ts, http.MethodPost, "/api/v1/jobs", aliceKey, map[string]interface{}{
		"type": "command",
		"spec": map[string]interface{}{
			"image_id": "img-alice-job",
			"command":  []string{"echo", "ok"},
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice could not create a job from her own image: %d (%s)", resp.StatusCode, string(body))
	}
}

// TestMultiTenant_VolumeExpandQuota verifies expanding a data volume enforces
// the owner's disk_gb quota.
func TestMultiTenant_VolumeExpandQuota(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	mtSignup(t, ts, "expand@example.com", "expanduser", "PasswordExpand123!")
	session, userID := mtLogin(t, ts, "expanduser", "PasswordExpand123!")
	apiKey := mtCreateAPIKey(t, ts, session, "expand-cli")

	if _, err := env.metaStore.Writer().ExecContext(ctx,
		`UPDATE users SET quota_json = ? WHERE id = ?`,
		`{"instances":10,"running_instances":10,"vcpus":32,"memory_mb":65536,"disk_gb":100,"vpcs":10,"api_keys":20}`,
		userID); err != nil {
		t.Fatalf("update quota: %v", err)
	}

	resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/volumes", apiKey, map[string]interface{}{
		"name":    "expand-vol",
		"size_gb": 60,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("volume create failed: %d (%s)", resp.StatusCode, string(body))
	}
	var vol db.Volume
	if err := json.Unmarshal(body, &vol); err != nil {
		t.Fatalf("decode volume: %v", err)
	}

	resp, body = mtRequest(t, ts, http.MethodPost, "/api/v1/volumes/"+vol.ID+"/expand", apiKey, map[string]interface{}{
		"add_gb": 50,
	})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("volume expand past quota: expected 429, got %d (%s)", resp.StatusCode, string(body))
	}

	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/volumes/"+vol.ID, apiKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("volume read failed: %d (%s)", resp.StatusCode, string(body))
	}
	var after db.Volume
	if err := json.Unmarshal(body, &after); err != nil {
		t.Fatalf("decode volume: %v", err)
	}
	if after.SizeGB != 60 {
		t.Fatalf("volume expanded despite quota denial: size_gb=%d", after.SizeGB)
	}
}

// TestMultiTenant_DuplicateVMNameNetworkInspectIsolation verifies that
// inspecting a VM by a name shared across tenants never returns another
// tenant's network metadata.
func TestMultiTenant_DuplicateVMNameNetworkInspectIsolation(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	mtSignup(t, ts, "bob@example.com", "bob", "PasswordBob123!")
	aliceSession, _ := mtLogin(t, ts, "alice", "PasswordAlice123!")
	bobSession, _ := mtLogin(t, ts, "bob", "PasswordBob123!")
	aliceKey := mtCreateAPIKey(t, ts, aliceSession, "alice-cli")
	bobKey := mtCreateAPIKey(t, ts, bobSession, "bob-cli")

	aliceResp, aliceVM := mtCreateVM(t, ts, aliceKey, map[string]interface{}{
		"name": "shared-vm", "tier": "1vcpu", "memory_mb": 1024,
	})
	if aliceResp.StatusCode != http.StatusCreated {
		t.Fatalf("alice VM create failed: %d", aliceResp.StatusCode)
	}
	bobResp, bobVM := mtCreateVM(t, ts, bobKey, map[string]interface{}{
		"name": "shared-vm", "tier": "1vcpu", "memory_mb": 1024,
	})
	if bobResp.StatusCode != http.StatusCreated {
		t.Fatalf("bob VM create failed: %d", bobResp.StatusCode)
	}

	inspect := func(token, name string) (int, vm.NetworkInspectResult) {
		resp, body := mtRequest(t, ts, http.MethodGet, "/api/v1/network/inspect/"+name, token, nil)
		var out vm.NetworkInspectResult
		if resp.StatusCode == http.StatusOK {
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatalf("decode inspect response: %v", err)
			}
		}
		return resp.StatusCode, out
	}

	// Alice's inspect must describe Alice's VM even though Bob's identically
	// named record was created later (and is therefore newer in the global
	// lookup ordering).
	status, aliceInspect := inspect(aliceKey, "shared-vm")
	if status != http.StatusOK {
		t.Fatalf("alice inspect failed: %d", status)
	}
	if aliceInspect.IPAddress != aliceVM.IPAddress || aliceInspect.MACAddress != aliceVM.MACAddress {
		t.Fatalf("alice inspect leaked bob's network metadata: got ip=%q mac=%q want ip=%q mac=%q",
			aliceInspect.IPAddress, aliceInspect.MACAddress, aliceVM.IPAddress, aliceVM.MACAddress)
	}

	status, bobInspect := inspect(bobKey, "shared-vm")
	if status != http.StatusOK {
		t.Fatalf("bob inspect failed: %d", status)
	}
	if bobInspect.IPAddress != bobVM.IPAddress || bobInspect.MACAddress != bobVM.MACAddress {
		t.Fatalf("bob inspect returned wrong VM: got ip=%q mac=%q want ip=%q mac=%q",
			bobInspect.IPAddress, bobInspect.MACAddress, bobVM.IPAddress, bobVM.MACAddress)
	}

	// Pinning another tenant's unique internal ID must not work either.
	status, _ = inspect(bobKey, aliceVM.InternalID)
	if status != http.StatusNotFound {
		t.Fatalf("bob inspected alice's internal VM id: got %d", status)
	}
}

// TestMultiTenant_JobsQuotaEnforced ensures job submission respects the same
// per-tenant creation and running quotas as direct VM creation.
func TestMultiTenant_JobsQuotaEnforced(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	mtSignup(t, ts, "jobs@example.com", "jobsuser", "PasswordJobs123!")
	session, userID := mtLogin(t, ts, "jobsuser", "PasswordJobs123!")
	apiKey := mtCreateAPIKey(t, ts, session, "jobs-cli")

	setQuota := func(quotaJSON string) {
		if _, err := env.metaStore.Writer().ExecContext(ctx, `UPDATE users SET quota_json = ? WHERE id = ?`, quotaJSON, userID); err != nil {
			t.Fatalf("update quota: %v", err)
		}
	}

	setQuota(`{"instances":1,"running_instances":10,"vcpus":32,"memory_mb":65536,"disk_gb":500,"vpcs":10,"api_keys":20}`)
	if resp, _ := mtCreateVM(t, ts, apiKey, map[string]interface{}{"name": "quota-holder", "tier": "1vcpu", "memory_mb": 1024}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("quota holder VM create failed: %d", resp.StatusCode)
	}

	jobPayload := map[string]interface{}{
		"type": "command",
		"spec": map[string]interface{}{
			"image_id": "rootfs",
			"command":  []string{"echo", "quota"},
		},
	}
	resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/jobs", apiKey, jobPayload)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("job past instance quota: expected 429, got %d (%s)", resp.StatusCode, string(body))
	}

	// Raising the quota allows the job to be accepted.
	setQuota(`{"instances":5,"running_instances":10,"vcpus":32,"memory_mb":65536,"disk_gb":500,"vpcs":10,"api_keys":20}`)
	resp, body = mtRequest(t, ts, http.MethodPost, "/api/v1/jobs", apiKey, jobPayload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("job under raised quota failed: %d (%s)", resp.StatusCode, string(body))
	}
}

// TestMultiTenant_LegacyDefaultResourcesInaccessible ensures authenticated
// tenants cannot use images or templates owned by the legacy "default" owner,
// while administrators retain access.
func TestMultiTenant_LegacyDefaultResourcesInaccessible(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	aliceSession, _ := mtLogin(t, ts, "alice", "PasswordAlice123!")
	aliceKey := mtCreateAPIKey(t, ts, aliceSession, "alice-cli")

	imgPath := filepath.Join(env.root, "legacy-default.ext4")
	if err := os.WriteFile(imgPath, []byte("legacy image rootfs"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := env.vmStore.SaveImage(ctx, &db.CustomImage{
		ID:         "img-legacy-default",
		OwnerID:    "default",
		Name:       "legacy-default-image",
		BaseImage:  "ubuntu-24.04",
		RootfsPath: imgPath,
		KernelPath: filepath.Join(env.root, "kernel"),
		SizeMB:     1024,
		Status:     db.ImageStatusReady,
	}); err != nil {
		t.Fatal(err)
	}
	tplPath := filepath.Join(env.root, "legacy-default-tpl.ext4")
	if err := os.WriteFile(tplPath, []byte("legacy template disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := env.vmStore.SaveTemplate(ctx, &db.Template{
		ID:         "tpl-legacy-default",
		OwnerID:    "default",
		Name:       "legacy-default-template",
		DiskPath:   tplPath,
		KernelPath: filepath.Join(env.root, "kernel"),
		VCPUs:      1,
		MemoryMB:   1024,
		DiskMB:     1024,
	}); err != nil {
		t.Fatal(err)
	}

	// Direct reads and launches must be invisible to a tenant.
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/images/img-legacy-default"},
		{http.MethodGet, "/api/v1/images/legacy-default-image"},
		{http.MethodGet, "/api/v1/templates/tpl-legacy-default"},
		{http.MethodGet, "/api/v1/templates/legacy-default-template"},
		{http.MethodPost, "/api/v1/templates/tpl-legacy-default/launch"},
		{http.MethodDelete, "/api/v1/images/img-legacy-default"},
		{http.MethodDelete, "/api/v1/templates/tpl-legacy-default"},
	} {
		resp, _ := mtRequest(t, ts, tc.method, tc.path, aliceKey, map[string]interface{}{})
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("alice %s %s: expected 404, got %d", tc.method, tc.path, resp.StatusCode)
		}
	}

	// Neither the image nor the template may be used to launch a sandbox.
	if resp, _ := mtCreateVM(t, ts, aliceKey, map[string]interface{}{
		"name": "alice-from-legacy-image", "tier": "1vcpu", "memory_mb": 1024,
		"image": "legacy-default-image",
	}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("alice launched from legacy default image: got %d", resp.StatusCode)
	}
	if resp, _ := mtCreateVM(t, ts, aliceKey, map[string]interface{}{
		"name": "alice-from-legacy-template", "tier": "1vcpu", "memory_mb": 1024,
		"template": "legacy-default-template",
	}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("alice launched from legacy default template: got %d", resp.StatusCode)
	}

	// Administrators may still read and use legacy resources.
	if resp, _ := mtRequest(t, ts, http.MethodGet, "/api/v1/images/img-legacy-default", multiTenantAdminSecret, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin read legacy image: got %d", resp.StatusCode)
	}
	if resp, _ := mtRequest(t, ts, http.MethodGet, "/api/v1/templates/tpl-legacy-default", multiTenantAdminSecret, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin read legacy template: got %d", resp.StatusCode)
	}
}

// TestMultiTenant_OrgOwnedResourcesUsable verifies resources owned by the
// caller's active organization are usable for VM creation and launch.
func TestMultiTenant_OrgOwnedResourcesUsable(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	mtSignup(t, ts, "bob@example.com", "bob", "PasswordBob123!")
	aliceSession, aliceID := mtLogin(t, ts, "alice", "PasswordAlice123!")
	bobSession, _ := mtLogin(t, ts, "bob", "PasswordBob123!")
	aliceKey := mtCreateAPIKey(t, ts, aliceSession, "alice-cli")
	bobKey := mtCreateAPIKey(t, ts, bobSession, "bob-cli")

	orgID := "org_" + strings.TrimPrefix(aliceID, "usr_")

	orgImagePath := filepath.Join(env.root, "org-image.ext4")
	if err := os.WriteFile(orgImagePath, []byte("org image rootfs"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := env.vmStore.SaveImage(ctx, &db.CustomImage{
		ID:         "img-org-owned",
		OwnerID:    orgID,
		Name:       "org-owned-image",
		BaseImage:  "ubuntu-24.04",
		RootfsPath: orgImagePath,
		KernelPath: filepath.Join(env.root, "kernel"),
		SizeMB:     1024,
		Status:     db.ImageStatusReady,
	}); err != nil {
		t.Fatal(err)
	}
	orgTplPath := filepath.Join(env.root, "org-tpl.ext4")
	if err := os.WriteFile(orgTplPath, []byte("org template disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := env.vmStore.SaveTemplate(ctx, &db.Template{
		ID:         "tpl-org-owned",
		OwnerID:    orgID,
		Name:       "org-owned-template",
		DiskPath:   orgTplPath,
		KernelPath: filepath.Join(env.root, "kernel"),
		VCPUs:      1,
		MemoryMB:   1024,
		DiskMB:     51200,
	}); err != nil {
		t.Fatal(err)
	}

	// Org members can use the org image.
	resp, _ := mtCreateVM(t, ts, aliceKey, map[string]interface{}{
		"name": "alice-from-org-image", "tier": "1vcpu", "memory_mb": 1024,
		"image": "org-owned-image",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice could not use org image: %d", resp.StatusCode)
	}

	// Org members can launch from the org template.
	resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/vms", aliceKey, map[string]interface{}{
		"name": "alice-from-org-template", "tier": "1vcpu", "memory_mb": 1024,
		"template": "org-owned-template",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice could not launch org template: %d (%s)", resp.StatusCode, string(body))
	}

	// The template is also resolvable through the image field.
	resp, _ = mtCreateVM(t, ts, aliceKey, map[string]interface{}{
		"name": "alice-org-template-as-image", "tier": "1vcpu", "memory_mb": 1024,
		"image": "org-owned-template",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice could not use org template via image field: %d", resp.StatusCode)
	}

	// A user outside the organization must not see or use them.
	if resp, _ := mtRequest(t, ts, http.MethodGet, "/api/v1/images/img-org-owned", bobKey, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob read org image: got %d", resp.StatusCode)
	}
	if resp, _ := mtCreateVM(t, ts, bobKey, map[string]interface{}{
		"name": "bob-from-org-image", "tier": "1vcpu", "memory_mb": 1024,
		"image": "org-owned-image",
	}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob launched from org image: got %d", resp.StatusCode)
	}
}

// TestMultiTenant_AdminCrossTenantCloneAndLaunch verifies administrators are
// not blocked by owner re-scoping when cloning or launching another tenant's
// resources.
func TestMultiTenant_AdminCrossTenantCloneAndLaunch(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	aliceSession, aliceID := mtLogin(t, ts, "alice", "PasswordAlice123!")
	aliceKey := mtCreateAPIKey(t, ts, aliceSession, "alice-cli")

	aliceResp, aliceVM := mtCreateVM(t, ts, aliceKey, map[string]interface{}{
		"name": "shared-vm", "tier": "1vcpu", "memory_mb": 1024,
	})
	if aliceResp.StatusCode != http.StatusCreated {
		t.Fatalf("alice VM create failed: %d", aliceResp.StatusCode)
	}

	// Admin clones Alice's VM by name.
	resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/vms/shared-vm/clone", multiTenantAdminSecret, map[string]interface{}{
		"name": "admin-clone-of-alice",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin cross-tenant clone failed: %d (%s)", resp.StatusCode, string(body))
	}
	var cloned db.VM
	if err := json.Unmarshal(body, &cloned); err != nil {
		t.Fatalf("decode clone: %v", err)
	}
	if cloned.OwnerID == aliceID {
		t.Fatalf("admin clone unexpectedly assigned to alice")
	}

	// Admin creates a template from Alice's VM and launches it for themselves.
	resp, body = mtRequest(t, ts, http.MethodPost, "/api/v1/templates", multiTenantAdminSecret, map[string]interface{}{
		"name":  "admin-template-from-alice",
		"vm_id": aliceVM.Name,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin template creation from alice VM failed: %d (%s)", resp.StatusCode, string(body))
	}
	resp, body = mtRequest(t, ts, http.MethodPost, "/api/v1/vms", multiTenantAdminSecret, map[string]interface{}{
		"name":      "admin-from-alice-template",
		"tier":      "1vcpu",
		"memory_mb": 1024,
		"template":  "admin-template-from-alice",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin launch from alice template failed: %d (%s)", resp.StatusCode, string(body))
	}

	// The explicit launch endpoint must not report the template as missing.
	resp, _ = mtRequest(t, ts, http.MethodPost, "/api/v1/templates/admin-template-from-alice/launch", multiTenantAdminSecret, map[string]interface{}{
		"name": "admin-launch-endpoint",
	})
	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("admin template launch reported not found")
	}
}

// TestMultiTenant_DuplicateVolumeNamesIsolated ensures two tenants can each own
// a volume named "shared" and that deleting one never affects the other.
func TestMultiTenant_DuplicateVolumeNamesIsolated(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	mtSignup(t, ts, "bob@example.com", "bob", "PasswordBob123!")
	aliceSession, aliceID := mtLogin(t, ts, "alice", "PasswordAlice123!")
	bobSession, bobID := mtLogin(t, ts, "bob", "PasswordBob123!")
	aliceKey := mtCreateAPIKey(t, ts, aliceSession, "alice-cli")
	bobKey := mtCreateAPIKey(t, ts, bobSession, "bob-cli")

	aliceVol := mtCreateVolume(t, ts, aliceKey, "shared")
	bobVol := mtCreateVolume(t, ts, bobKey, "shared")
	if aliceVol.ID == bobVol.ID {
		t.Fatalf("duplicate volume names unexpectedly produced the same ID: %s", aliceVol.ID)
	}
	if aliceVol.OwnerID != aliceID || bobVol.OwnerID != bobID {
		t.Fatalf("volumes have wrong owners: alice=%q bob=%q", aliceVol.OwnerID, bobVol.OwnerID)
	}

	// Bob deleting his "shared" volume must leave Alice's intact.
	resp, body := mtRequest(t, ts, http.MethodDelete, "/api/v1/volumes/shared", bobKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob delete of his shared volume failed: %d (%s)", resp.StatusCode, string(body))
	}
	if stored, err := env.vmStore.GetVolume(ctx, aliceVol.ID); err != nil || stored == nil {
		t.Fatalf("alice volume lost after bob's delete: %v", err)
	}
	if stored, err := env.vmStore.GetVolume(ctx, bobVol.ID); err != nil || stored != nil {
		t.Fatalf("bob volume still present after delete: %v", err)
	}

	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/volumes/shared", aliceKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice could not read her shared volume: %d (%s)", resp.StatusCode, string(body))
	}
	var fetched db.Volume
	if err := json.Unmarshal(body, &fetched); err != nil {
		t.Fatalf("decode alice volume: %v", err)
	}
	if fetched.ID != aliceVol.ID || fetched.OwnerID != aliceID {
		t.Fatalf("alice resolved the wrong shared volume: id=%q owner=%q", fetched.ID, fetched.OwnerID)
	}
	if resp, _ := mtRequest(t, ts, http.MethodGet, "/api/v1/volumes/shared", bobKey, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob still sees a volume after deleting his own: %d", resp.StatusCode)
	}

	// Alice can still delete her own volume by the shared name.
	resp, body = mtRequest(t, ts, http.MethodDelete, "/api/v1/volumes/shared", aliceKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice delete of her shared volume failed: %d (%s)", resp.StatusCode, string(body))
	}
	if stored, err := env.vmStore.GetVolume(ctx, aliceVol.ID); err != nil || stored != nil {
		t.Fatalf("alice volume still present after her own delete: %v", err)
	}
}

// TestMultiTenant_DuplicateSSHKeyNamesIsolated ensures duplicate SSH key names
// across tenants resolve to the caller's own key when creating sandboxes.
func TestMultiTenant_DuplicateSSHKeyNamesIsolated(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	mtSignup(t, ts, "bob@example.com", "bob", "PasswordBob123!")
	aliceSession, _ := mtLogin(t, ts, "alice", "PasswordAlice123!")
	bobSession, _ := mtLogin(t, ts, "bob", "PasswordBob123!")
	aliceKey := mtCreateAPIKey(t, ts, aliceSession, "alice-cli")
	bobKey := mtCreateAPIKey(t, ts, bobSession, "bob-cli")

	aliceSSHKey := mtAddSSHKey(t, ts, aliceKey, "my-key")
	bobSSHKey := mtAddSSHKey(t, ts, bobKey, "my-key")
	if aliceSSHKey.ID == bobSSHKey.ID {
		t.Fatalf("duplicate ssh key names unexpectedly produced the same ID: %s", aliceSSHKey.ID)
	}

	resp, aliceVM := mtCreateVM(t, ts, aliceKey, map[string]interface{}{
		"name": "alice-ssh-vm", "tier": "1vcpu", "memory_mb": 1024,
		"ssh_key_id": "my-key",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice could not attach her my-key: %d", resp.StatusCode)
	}
	resp, bobVM := mtCreateVM(t, ts, bobKey, map[string]interface{}{
		"name": "bob-ssh-vm", "tier": "1vcpu", "memory_mb": 1024,
		"ssh_key_id": "my-key",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bob could not attach his my-key: %d", resp.StatusCode)
	}
	if aliceVM.SSHPublicKey != aliceSSHKey.PublicKey {
		t.Fatalf("alice VM embedded a foreign ssh key")
	}
	if bobVM.SSHPublicKey != bobSSHKey.PublicKey {
		t.Fatalf("bob VM embedded a foreign ssh key")
	}
}

// TestMultiTenant_BaseImageSquattingBlocked ensures a tenant-owned custom image
// cannot shadow a platform base image name for other tenants.
func TestMultiTenant_BaseImageSquattingBlocked(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	mtSignup(t, ts, "alice@example.com", "alice", "PasswordAlice123!")
	mtSignup(t, ts, "bob@example.com", "bob", "PasswordBob123!")
	aliceSession, _ := mtLogin(t, ts, "alice", "PasswordAlice123!")
	_, bobID := mtLogin(t, ts, "bob", "PasswordBob123!")
	aliceKey := mtCreateAPIKey(t, ts, aliceSession, "alice-cli")

	bobImagePath := filepath.Join(env.root, "bob-ubuntu.ext4")
	if err := os.WriteFile(bobImagePath, []byte("bob custom rootfs"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := env.vmStore.SaveImage(ctx, &db.CustomImage{
		ID:         "img-bob-ubuntu",
		OwnerID:    bobID,
		Name:       "ubuntu-24.04",
		BaseImage:  "ubuntu-24.04",
		RootfsPath: bobImagePath,
		KernelPath: filepath.Join(env.root, "bob-kernel"),
		SizeMB:     1024,
		Status:     db.ImageStatusReady,
	}); err != nil {
		t.Fatalf("save bob squatting image: %v", err)
	}

	resp, aliceVM := mtCreateVM(t, ts, aliceKey, map[string]interface{}{
		"name": "alice-base-image-vm", "tier": "1vcpu", "memory_mb": 1024,
		"image": "ubuntu-24.04",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice could not use platform base image after bob squatting: %d", resp.StatusCode)
	}
	if aliceVM.RootfsPath == bobImagePath {
		t.Fatalf("alice VM used bob's squatting custom image")
	}

	// The same shadowing protection applies to job submissions.
	resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/jobs", aliceKey, map[string]interface{}{
		"type": "command",
		"spec": map[string]interface{}{
			"image_id": "ubuntu-24.04",
			"command":  []string{"echo", "base-image"},
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice job with platform base image was blocked: %d (%s)", resp.StatusCode, string(body))
	}
}

// TestMultiTenant_AuthFailsClosedWhenUninitialized ensures a configured but
// uninitialized auth backend rejects requests instead of falling back to the
// legacy default/admin identity.
func TestMultiTenant_AuthFailsClosedWhenUninitialized(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts

	// Simulate an auth backend initialization failure for an auth-enabled daemon.
	env.server.SetAuthManager(nil)

	for _, tc := range []struct {
		method string
		path   string
		token  string
	}{
		{http.MethodGet, "/api/v1/vms", ""},
		{http.MethodGet, "/api/v1/vms", multiTenantAdminSecret},
		{http.MethodPost, "/api/v1/volumes", ""},
		{http.MethodGet, "/api/v1/auth/me", multiTenantAdminSecret},
	} {
		resp, body := mtRequest(t, ts, tc.method, tc.path, tc.token, nil)
		if resp.StatusCode != http.StatusServiceUnavailable && resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s with uninitialized auth: expected 503/401, got %d (%s)", tc.method, tc.path, resp.StatusCode, string(body))
		}
	}

	// Public auth endpoints also fail closed rather than panicking.
	resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": "alice", "password": "PasswordAlice123!",
	})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("login with uninitialized auth: expected 503, got %d (%s)", resp.StatusCode, string(body))
	}
}

func TestAdminPassword_NoPredictableFallback(t *testing.T) {
	root := t.TempDir()
	dbStore, err := db.NewStore(filepath.Join(root, "db", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbStore.Close()

	imagesDir := filepath.Join(root, "images")
	_ = os.MkdirAll(imagesDir, 0o755)
	_ = os.WriteFile(filepath.Join(imagesDir, "rootfs.ext4"), []byte("test rootfs"), 0o600)

	storMgr, err := storage.NewManager(storage.Config{
		BaseImagesDir: imagesDir,
		VMsDir:        filepath.Join(root, "vms"),
		DefaultRootfs: "rootfs.ext4",
	})
	if err != nil {
		t.Fatal(err)
	}

	netMgr, err := network.NewManager(network.Config{SubnetCIDR: "192.168.50.0/24"}, testRunner{})
	if err != nil {
		t.Fatal(err)
	}

	vmMgr := vm.NewManager(vm.Config{}, dbStore, netMgr, storMgr)

	cfg := config.DefaultConfig()
	cfg.ConfigPath = "config/daemon.json"
	cfg.Jobs.LocalExecution = false
	cfg.Server.AuthEnabled = true
	cfg.Server.AuthDBPath = filepath.Join(root, "db", "auth.db")
	cfg.Server.AdminPassword = "" // Explicitly empty
	_ = os.Unsetenv("COMPUTE_ADMIN_PASSWORD")

	serverObj := NewServer(vmMgr, cfg)
	ts := httptest.NewServer(serverObj)
	defer ts.Close()

	// 1. Attempt login with the predictable fallback password "compute-admin-2026!"
	loginBody, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "compute-admin-2026!",
	})
	resp, err := http.Post(ts.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("POST /api/v1/auth/login failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for compute-admin-2026!, got %d", resp.StatusCode)
	}

	// 2. Verify that when cfg.Server.AdminPassword is explicitly provided, it is accepted
	root2 := t.TempDir()
	dbStore2, _ := db.NewStore(filepath.Join(root2, "db", "test.sqlite"))
	defer dbStore2.Close()
	vmMgr2 := vm.NewManager(vm.Config{}, dbStore2, netMgr, storMgr)
	cfg2 := config.DefaultConfig()
	cfg2.ConfigPath = "config/daemon.json"
	cfg2.Jobs.LocalExecution = false
	cfg2.Server.AuthEnabled = true
	cfg2.Server.AuthDBPath = filepath.Join(root2, "db", "auth.db")
	cfg2.Server.AdminPassword = "CustomSecurePassword987!"

	serverObj2 := NewServer(vmMgr2, cfg2)
	ts2 := httptest.NewServer(serverObj2)
	defer ts2.Close()

	loginBody2, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "CustomSecurePassword987!",
	})
	resp2, err := http.Post(ts2.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(loginBody2))
	if err != nil {
		t.Fatalf("POST /api/v1/auth/login failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for custom admin password, got %d", resp2.StatusCode)
	}
}

func TestWorkerWebSocket_TokenValidation(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	// Create valid worker token
	validTok := &db.WorkerToken{
		Token:     "tok-valid-12345",
		Name:      "valid-worker",
		PoolID:    "default",
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		CreatedAt: time.Now().UTC(),
	}
	if err := env.vmStore.SaveWorkerToken(ctx, validTok); err != nil {
		t.Fatalf("SaveWorkerToken validTok: %v", err)
	}

	// Create already-used worker token
	usedTime := time.Now().UTC().Add(-1 * time.Hour)
	usedTok := &db.WorkerToken{
		Token:     "tok-used-99999",
		Name:      "used-worker",
		PoolID:    "default",
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		UsedAt:    &usedTime,
		CreatedAt: time.Now().UTC(),
	}
	if err := env.vmStore.SaveWorkerToken(ctx, usedTok); err != nil {
		t.Fatalf("SaveWorkerToken usedTok: %v", err)
	}

	baseWS := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/worker/connect"

	// 1. Missing token -> 401 Unauthorized
	dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, resp1, err := websocket.Dial(dialCtx, baseWS, nil)
	if err == nil {
		t.Fatalf("expected websocket.Dial without token to fail")
	}
	if resp1 == nil || resp1.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for missing token, got %v", resp1)
	}

	// 2. Invalid token in X-Worker-Token header -> 401 Unauthorized
	dialCtx2, cancel2 := context.WithTimeout(ctx, 3*time.Second)
	defer cancel2()
	optsInvalid := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"X-Worker-Token": []string{"tok-bogus-random"},
		},
	}
	_, resp2, err := websocket.Dial(dialCtx2, baseWS, optsInvalid)
	if err == nil {
		t.Fatalf("expected websocket.Dial with bogus token to fail")
	}
	if resp2 == nil || resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for bogus token, got %v", resp2)
	}

	// 3. Revoked / used token in query parameter -> 401 Unauthorized
	dialCtx3, cancel3 := context.WithTimeout(ctx, 3*time.Second)
	defer cancel3()
	_, resp3, err := websocket.Dial(dialCtx3, baseWS+"?token=tok-used-99999", nil)
	if err == nil {
		t.Fatalf("expected websocket.Dial with used token to fail")
	}
	if resp3 == nil || resp3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for used token, got %v", resp3)
	}

	// 4. Valid token in X-Worker-Token -> connection accepted and handshake succeeds
	dialCtx4, cancel4 := context.WithTimeout(ctx, 5*time.Second)
	defer cancel4()
	optsValid := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"X-Worker-Token": []string{"tok-valid-12345"},
		},
	}
	conn, resp4, err := websocket.Dial(dialCtx4, baseWS, optsValid)
	if err != nil {
		t.Fatalf("expected websocket.Dial with valid token to succeed, got %v (resp: %v)", err, resp4)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Complete handshake
	hello := api.WorkerHello{
		WorkerMessage: api.WorkerMessage{
			Version: 1,
			Type:    "worker.hello",
			ID:      "hello-worker-1",
		},
		Name:         "nod_test_worker_1",
		Token:        "tok-valid-12345",
		Architecture: "amd64",
	}
	helloBytes, _ := json.Marshal(hello)
	if err := conn.Write(dialCtx4, websocket.MessageText, helloBytes); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	_, ackBytes, err := conn.Read(dialCtx4)
	if err != nil {
		t.Fatalf("read hello ack: %v", err)
	}
	var ack api.WorkerHelloAck
	if err := json.Unmarshal(ackBytes, &ack); err != nil {
		t.Fatalf("unmarshal ack: %v", err)
	}
	if !ack.Accepted {
		t.Fatalf("expected worker handshake accepted")
	}

	// Verify token is now consumed (cannot be re-used)
	dialCtx5, cancel5 := context.WithTimeout(ctx, 3*time.Second)
	defer cancel5()
	_, resp5, err := websocket.Dial(dialCtx5, baseWS, optsValid)
	if err == nil {
		t.Fatalf("expected re-use of consumed worker token to fail")
	}
	if resp5 == nil || resp5.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for consumed token, got %v", resp5)
	}
}

func TestMultiTenant_AdminScopedKeyRestrictedScopes(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	// 1. Sign up an admin user and create a session
	adminUser, err := env.authMgr.Signup(ctx, "admin-scoped@example.com", "admin-user", "Admin User", "AdminSecret123!", "admin")
	if err != nil {
		t.Fatalf("signup admin: %v", err)
	}
	if adminUser.Role != "admin" {
		t.Fatalf("expected admin user role, got %s", adminUser.Role)
	}

	adminSession, _ := mtLogin(t, ts, "admin-user", "AdminSecret123!")

	// 2. Admin creates a VM so there is something to read
	vmResp, createdVM := mtCreateVM(t, ts, adminSession, map[string]interface{}{
		"name":      "admin-vm-target",
		"tier":      "1vcpu",
		"memory_mb": 1024,
	})
	if vmResp.StatusCode != http.StatusCreated {
		t.Fatalf("admin create VM failed: %d (%+v)", vmResp.StatusCode, createdVM)
	}

	// 3. Admin creates a scoped API key with narrow scope "read:vms"
	resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/api-keys", adminSession, map[string]string{
		"name":  "narrow-admin-key",
		"scope": "read:vms",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create scoped key failed: %d %s", resp.StatusCode, string(body))
	}
	var outKey db.AccessAPIKey
	if err := json.Unmarshal(body, &outKey); err != nil {
		t.Fatalf("unmarshal key: %v", err)
	}
	narrowToken := outKey.Token

	// 4. Narrow key CAN perform GET /api/v1/vms
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/vms", narrowToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for GET /api/v1/vms with read:vms key, got %d (%s)", resp.StatusCode, string(body))
	}

	// 5. Narrow key CANNOT perform write operations on VMs (e.g. POST /api/v1/vms)
	failResp, _ := mtCreateVM(t, ts, narrowToken, map[string]interface{}{
		"name":      "should-fail-vm",
		"tier":      "1vcpu",
		"memory_mb": 1024,
	})
	if failResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for POST /api/v1/vms with read:vms key, got %d", failResp.StatusCode)
	}

	// 6. Narrow key CANNOT perform write operations (DELETE /api/v1/vms/admin-vm-target)
	resp, body = mtRequest(t, ts, http.MethodDelete, "/api/v1/vms/admin-vm-target", narrowToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for DELETE /api/v1/vms with read:vms key, got %d (%s)", resp.StatusCode, string(body))
	}

	// 7. Narrow key CANNOT access admin endpoints: /api/v1/webhooks
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/webhooks", narrowToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for GET /api/v1/webhooks with read:vms key, got %d (%s)", resp.StatusCode, string(body))
	}
	resp, body = mtRequest(t, ts, http.MethodPost, "/api/v1/webhooks", narrowToken, map[string]interface{}{
		"url": "https://example.com/webhook",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for POST /api/v1/webhooks with read:vms key, got %d (%s)", resp.StatusCode, string(body))
	}

	// 8. Narrow key CANNOT access admin endpoints: /api/v1/workers and /api/v1/worker-tokens
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/workers", narrowToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for GET /api/v1/workers with read:vms key, got %d (%s)", resp.StatusCode, string(body))
	}
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/worker-tokens", narrowToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for GET /api/v1/worker-tokens with read:vms key, got %d (%s)", resp.StatusCode, string(body))
	}

	// 9. Verify that an admin-created key with "admin" scope CAN access admin endpoints and write
	resp, body = mtRequest(t, ts, http.MethodPost, "/api/v1/api-keys", adminSession, map[string]string{
		"name":  "full-admin-key",
		"scope": "admin",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create full admin key: %d %s", resp.StatusCode, string(body))
	}
	var fullKey db.AccessAPIKey
	_ = json.Unmarshal(body, &fullKey)

	resp, _ = mtRequest(t, ts, http.MethodGet, "/api/v1/webhooks", fullKey.Token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for GET /api/v1/webhooks with admin scope, got %d", resp.StatusCode)
	}
	adminVMResp, _ := mtCreateVM(t, ts, fullKey.Token, map[string]interface{}{
		"name":      "admin-full-vm",
		"tier":      "1vcpu",
		"memory_mb": 1024,
	})
	if adminVMResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created for POST /api/v1/vms with admin scope, got %d", adminVMResp.StatusCode)
	}
}

func TestWorkerWebSocket_ConcurrentAtomicTokenRace(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	tokenStr := "tok-race-concurrent-" + uuid.NewString()[:8]
	tok := &db.WorkerToken{
		Token:     tokenStr,
		Name:      "race-worker",
		PoolID:    "default",
		ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
		CreatedAt: time.Now().UTC(),
	}
	if err := env.vmStore.SaveWorkerToken(ctx, tok); err != nil {
		t.Fatalf("SaveWorkerToken: %v", err)
	}

	baseWS := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/worker/connect"

	concurrency := 4
	var wg sync.WaitGroup
	start := make(chan struct{})

	type result struct {
		statusCode int
		success    bool
		err        error
	}
	results := make([]result, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start

			dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			opts := &websocket.DialOptions{
				HTTPHeader: http.Header{
					"X-Worker-Token": []string{tokenStr},
				},
			}
			conn, resp, err := websocket.Dial(dialCtx, baseWS, opts)
			if err != nil {
				sc := 0
				if resp != nil {
					sc = resp.StatusCode
				}
				results[idx] = result{statusCode: sc, success: false, err: err}
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "")

			// Complete handshake for the winning connection
			hello := api.WorkerHello{
				WorkerMessage: api.WorkerMessage{
					Version: 1,
					Type:    "worker.hello",
					ID:      fmt.Sprintf("hello-race-worker-%d", idx),
				},
				Name:         fmt.Sprintf("nod_race_worker_%d", idx),
				Token:        tokenStr,
				Architecture: "amd64",
			}
			helloBytes, _ := json.Marshal(hello)
			_ = conn.Write(dialCtx, websocket.MessageText, helloBytes)
			var ack api.WorkerHelloAck
			_, ackBytes, readErr := conn.Read(dialCtx)
			if readErr == nil {
				_ = json.Unmarshal(ackBytes, &ack)
			}
			results[idx] = result{statusCode: http.StatusSwitchingProtocols, success: true}
		}(i)
	}

	close(start)
	wg.Wait()

	successCount := 0
	rejectedCount := 0
	for _, res := range results {
		if res.success {
			successCount++
		} else if res.statusCode == http.StatusUnauthorized {
			rejectedCount++
		}
	}

	if successCount != 1 {
		t.Fatalf("expected exactly 1 worker connection to succeed, got %d (results: %+v)", successCount, results)
	}
	if rejectedCount != concurrency-1 {
		t.Fatalf("expected exactly %d worker connections rejected with 401, got %d (results: %+v)", concurrency-1, rejectedCount, results)
	}
}

func TestMultiTenant_InactiveDisabledUserKeyInvalidation(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts
	ctx := context.Background()

	// 1. Sign up user and log in
	mtSignup(t, ts, "charlie@example.com", "charlie", "CharliePassword123!")
	sessionToken, _ := mtLogin(t, ts, "charlie", "CharliePassword123!")

	// 2. Create a VM for Charlie so there is an SSH target
	vmResp, createdVM := mtCreateVM(t, ts, sessionToken, map[string]interface{}{
		"name":      "charlie-vm-1",
		"tier":      "1vcpu",
		"memory_mb": 1024,
	})
	if vmResp.StatusCode != http.StatusCreated {
		t.Fatalf("create VM failed: %d (%+v)", vmResp.StatusCode, createdVM)
	}

	// 3. Create scoped access API key via POST /api/v1/api-keys
	createScopedKey := func(name, scope string) string {
		resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/api-keys", sessionToken, map[string]string{
			"name":  name,
			"scope": scope,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create scoped key failed: %d %s", resp.StatusCode, string(body))
		}
		var outKey db.AccessAPIKey
		if err := json.Unmarshal(body, &outKey); err != nil {
			t.Fatalf("unmarshal key: %v", err)
		}
		return outKey.Token
	}

	scopedKey := createScopedKey("charlie-key", "*")

	// 4. Verify key works while user status is active
	resp, body := mtRequest(t, ts, http.MethodGet, "/api/v1/vms", scopedKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for active user key on /api/v1/vms, got %d (%s)", resp.StatusCode, string(body))
	}

	// 5. Deactivate user: status = "disabled"
	if _, err := env.metaStore.Writer().ExecContext(ctx, "UPDATE users SET status = ? WHERE username = ?", "disabled", "charlie"); err != nil {
		t.Fatalf("failed to update user status to disabled: %v", err)
	}

	// 6. Verify scoped API key is rejected with 401 across API endpoints
	apiEndpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/vms"},
		{http.MethodGet, "/api/v1/vms/charlie-vm-1"},
		{http.MethodGet, "/api/v1/auth/me"},
		{http.MethodGet, "/api/v1/volumes"},
		{http.MethodGet, "/api/v1/images"},
		{http.MethodGet, "/api/v1/api-keys"},
	}
	for _, ep := range apiEndpoints {
		resp, body := mtRequest(t, ts, ep.method, ep.path, scopedKey, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized for disabled user key on %s %s, got %d (%s)", ep.method, ep.path, resp.StatusCode, string(body))
		}
	}

	// 7. Verify SSH endpoints reject disabled user's key with 401 Unauthorized
	sshEndpoints := []string{
		"/api/v1/vms/charlie-vm-1/ssh",
		"/api/v1/ssh/charlie-vm-1",
	}
	for _, path := range sshEndpoints {
		resp, body := mtRequest(t, ts, http.MethodGet, path, scopedKey, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized for disabled user key on SSH endpoint %s, got %d (%s)", path, resp.StatusCode, string(body))
		}
	}

	// Also verify WebSocket dial to SSH endpoint is rejected with 401 Unauthorized
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/vms/charlie-vm-1/ssh?api_key=" + url.QueryEscape(scopedKey)
	wsCtx, wsCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer wsCancel()
	_, wsResp, wsErr := websocket.Dial(wsCtx, wsURL, nil)
	if wsErr == nil {
		t.Errorf("expected websocket dial to fail for disabled user key")
	} else if wsResp != nil && wsResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized on websocket dial for disabled user, got %d", wsResp.StatusCode)
	}

	// 8. Test authContextForAccessKey rejection on "suspended" and "inactive" statuses directly
	for _, inactiveStatus := range []string{"disabled", "suspended", "inactive"} {
		inactiveUser := &storeDB.User{
			ID:       "usr_inactive_test",
			Username: "inactive_user",
			Status:   inactiveStatus,
			Role:     "user",
		}
		dummyKey := &db.AccessAPIKey{
			ID:        "key_inactive_test",
			Scope:     "*",
			Role:      "user",
			ExpiresAt: time.Now().AddDate(0, 0, 90).Format(time.RFC3339),
		}
		authCtx := env.server.authContextForAccessKey(inactiveUser, dummyKey, "127.0.0.1")
		if authCtx != nil {
			t.Errorf("expected authContextForAccessKey to return nil for user status %q, got %+v", inactiveStatus, authCtx)
		}
	}

	// 9. Test admin user deactivation blocks admin scoped keys
	adminUser, err := env.authMgr.Signup(ctx, "admin-deact@example.com", "admin-deact", "Admin Deact", "AdminPassword123!", "admin")
	if err != nil {
		t.Fatalf("signup admin: %v", err)
	}
	_ = adminUser
	adminSession, _ := mtLogin(t, ts, "admin-deact", "AdminPassword123!")
	adminResp, adminKeyBody := mtRequest(t, ts, http.MethodPost, "/api/v1/api-keys", adminSession, map[string]string{
		"name":  "admin-key",
		"scope": "admin",
	})
	if adminResp.StatusCode != http.StatusCreated {
		t.Fatalf("create admin api key failed: %d", adminResp.StatusCode)
	}
	var adminKey db.AccessAPIKey
	_ = json.Unmarshal(adminKeyBody, &adminKey)

	// Verify admin key works initially
	resp, _ = mtRequest(t, ts, http.MethodGet, "/api/v1/config", adminKey.Token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected admin key to access /api/v1/config, got %d", resp.StatusCode)
	}

	// Deactivate admin
	if _, err := env.metaStore.Writer().ExecContext(ctx, "UPDATE users SET status = ? WHERE username = ?", "disabled", "admin-deact"); err != nil {
		t.Fatalf("failed to update admin status to disabled: %v", err)
	}

	// Admin key MUST now be rejected with 401
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/config", adminKey.Token, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for disabled admin key, got %d (%s)", resp.StatusCode, string(body))
	}
}

func TestMultiTenant_APIKeyConcreteExpiration(t *testing.T) {
	env := newMultiTenantServer(t)
	ts := env.ts

	// 1. Sign up user and log in
	mtSignup(t, ts, "dave@example.com", "dave", "DavePassword123!")
	sessionToken, _ := mtLogin(t, ts, "dave", "DavePassword123!")

	// Helper to create access key with given expires_at payload
	createKey := func(expiresAt string) db.AccessAPIKey {
		payload := map[string]string{
			"name":  "key-" + expiresAt,
			"scope": "*",
		}
		if expiresAt != "" {
			payload["expires_at"] = expiresAt
		}
		resp, body := mtRequest(t, ts, http.MethodPost, "/api/v1/api-keys", sessionToken, payload)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create access key failed: %d (%s)", resp.StatusCode, string(body))
		}
		var k db.AccessAPIKey
		if err := json.Unmarshal(body, &k); err != nil {
			t.Fatalf("unmarshal key: %v", err)
		}
		return k
	}

	// 2. Default expiration (empty string in payload)
	defaultKey := createKey("")
	if defaultKey.ExpiresAt == "in 90 days" {
		t.Fatalf("default access key retained literal 'in 90 days' instead of concrete RFC3339 timestamp")
	}
	defaultExp, err := time.Parse(time.RFC3339, defaultKey.ExpiresAt)
	if err != nil {
		t.Fatalf("default key expires_at is not valid RFC3339: %q (%v)", defaultKey.ExpiresAt, err)
	}
	expectedDefault := time.Now().UTC().AddDate(0, 0, 90)
	if diff := defaultExp.Sub(expectedDefault); diff > 24*time.Hour || diff < -24*time.Hour {
		t.Errorf("expected default expiration around 90 days, got %v (diff %v)", defaultExp, diff)
	}
	// Active key works
	resp, body := mtRequest(t, ts, http.MethodGet, "/api/v1/vms", defaultKey.Token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for valid default key, got %d (%s)", resp.StatusCode, string(body))
	}

	// 3. Literal "in 90 days" passed explicitly in payload
	in90Key := createKey("in 90 days")
	if in90Key.ExpiresAt == "in 90 days" {
		t.Fatalf("access key with 'in 90 days' retained literal string instead of concrete timestamp")
	}
	if _, err := time.Parse(time.RFC3339, in90Key.ExpiresAt); err != nil {
		t.Fatalf("key with 'in 90 days' expires_at is not valid RFC3339: %q (%v)", in90Key.ExpiresAt, err)
	}

	// 4. Relative duration "30d"
	key30d := createKey("30d")
	exp30d, err := time.Parse(time.RFC3339, key30d.ExpiresAt)
	if err != nil {
		t.Fatalf("30d key expires_at is not valid RFC3339: %q (%v)", key30d.ExpiresAt, err)
	}
	expected30d := time.Now().UTC().AddDate(0, 0, 30)
	if diff := exp30d.Sub(expected30d); diff > 24*time.Hour || diff < -24*time.Hour {
		t.Errorf("expected 30d expiration around 30 days, got %v", exp30d)
	}

	// 5. Relative duration "24h"
	key24h := createKey("24h")
	exp24h, err := time.Parse(time.RFC3339, key24h.ExpiresAt)
	if err != nil {
		t.Fatalf("24h key expires_at is not valid RFC3339: %q (%v)", key24h.ExpiresAt, err)
	}
	expected24h := time.Now().UTC().Add(24 * time.Hour)
	if diff := exp24h.Sub(expected24h); diff > time.Hour || diff < -time.Hour {
		t.Errorf("expected 24h expiration around 24 hours, got %v", exp24h)
	}

	// 6. Expired key via RFC3339 timestamp in past
	pastTS := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	expiredKey := createKey(pastTS)
	if expiredKey.ExpiresAt != pastTS {
		t.Errorf("expected past RFC3339 timestamp preserved, got %q", expiredKey.ExpiresAt)
	}
	// Expired key MUST be rejected with 401 Unauthorized
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/vms", expiredKey.Token, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for expired key, got %d (%s)", resp.StatusCode, string(body))
	}

	// 7. Expired key via negative relative duration ("-1h")
	expiredRelKey := createKey("-1h")
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/vms", expiredRelKey.Token, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for key created with -1h, got %d (%s)", resp.StatusCode, string(body))
	}

	// 8. Key directly inserted into store with past date ("2020-01-01")
	rawExpiredKey := &db.AccessAPIKey{
		ID:        "key_raw_expired_01",
		Name:      "raw-expired",
		Role:      "user",
		Prefix:    "cmp_live_rawexp...",
		Token:     "cmp_live_raw_expired_token_test_1234",
		Scope:     "*",
		LastUsed:  "Never",
		ExpiresAt: "2020-01-01",
		CreatedBy: "dave",
		CreatedAt: time.Now().UTC().AddDate(-5, 0, 0),
	}
	if err := env.vmStore.SaveAccessAPIKey(context.Background(), rawExpiredKey); err != nil {
		t.Fatalf("save raw expired key: %v", err)
	}
	resp, body = mtRequest(t, ts, http.MethodGet, "/api/v1/vms", rawExpiredKey.Token, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for 2020-01-01 key, got %d (%s)", resp.StatusCode, string(body))
	}
}
