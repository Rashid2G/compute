package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/config"
	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/master/auth"
	"github.com/Rashid2G/compute/internal/master/pki"
	"github.com/Rashid2G/compute/internal/network"
	"github.com/Rashid2G/compute/internal/storage"
	"github.com/Rashid2G/compute/internal/store"
	storeDB "github.com/Rashid2G/compute/internal/store/db"
	"github.com/Rashid2G/compute/internal/vm"
)

func newTestAuthServer(t *testing.T) (*httptest.Server, *auth.Manager) {
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
	withTestHostCapacity(&cfg)

	mk, err := pki.LoadOrCreateMasterKey(filepath.Join(root, "master.key"))
	if err != nil {
		t.Fatal(err)
	}

	metaStore, err := store.Open(cfg.Server.AuthDBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metaStore.Close() })

	queries := storeDB.New(metaStore.Writer())
	authMgr := auth.NewManager(queries, mk, 24*time.Hour, "admin-bootstrap-secret-999")

	serverObj := NewServer(vm.NewManager(vm.Config{}, dbStore, netMgr, storMgr), cfg)
	serverObj.SetAuthManager(authMgr)

	ts := httptest.NewServer(serverObj)
	t.Cleanup(ts.Close)

	return ts, authMgr
}

func TestAuth_FullLifecycleAndMultiTenancy(t *testing.T) {
	server, authMgr := newTestAuthServer(t)
	ctx := context.Background()

	// 1. Signup Alice and Bob
	aliceUser, err := authMgr.Signup(ctx, "alice@example.com", "alice", "Alice User", "PasswordAlice123!", "user")
	if err != nil {
		t.Fatalf("Alice signup failed: %v", err)
	}

	bobUser, err := authMgr.Signup(ctx, "bob@example.com", "bob", "Bob User", "PasswordBob123!", "user")
	if err != nil {
		t.Fatalf("Bob signup failed: %v", err)
	}

	// 2. Generate API Keys for Alice and Bob
	aliceToken, _, err := authMgr.KeyManager().GenerateAPIKey(ctx, aliceUser.ID, "alice-key", nil)
	if err != nil {
		t.Fatalf("Alice key gen failed: %v", err)
	}
	bobToken, _, err := authMgr.KeyManager().GenerateAPIKey(ctx, bobUser.ID, "bob-key", nil)
	if err != nil {
		t.Fatalf("Bob key gen failed: %v", err)
	}

	// 3. Test /api/v1/auth/me for Alice
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		var body bytes.Buffer
		if resp != nil {
			_, _ = body.ReadFrom(resp.Body)
		}
		t.Fatalf("Expected 200 OK from /api/v1/auth/me, got %v, body: %s", resp, body.String())
	}

	// 4. Test Limits endpoint
	reqLimits, _ := http.NewRequest("GET", server.URL+"/api/v1/limits", nil)
	reqLimits.Header.Set("Authorization", "Bearer "+aliceToken)
	respLimits, err := http.DefaultClient.Do(reqLimits)
	if err != nil || respLimits.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200 OK from /api/v1/limits, got %v", respLimits)
	}

	// 5. Alice creates a sandbox
	createPayload := []byte(`{"name":"sandbox-alice-1","image":"ubuntu-24","vcpus":1,"memory_mb":2048}`)
	reqCreate, _ := http.NewRequest("POST", server.URL+"/api/v1/sandboxes", bytes.NewBuffer(createPayload))
	reqCreate.Header.Set("Authorization", "Bearer "+aliceToken)
	reqCreate.Header.Set("Content-Type", "application/json")
	respCreate, err := http.DefaultClient.Do(reqCreate)
	if err != nil || respCreate.StatusCode != http.StatusCreated {
		t.Fatalf("Expected 201 Created from Alice sandbox create, got %v", respCreate)
	}

	// 6. Multi-tenant isolation: Bob lists sandboxes -> must NOT see Alice's sandbox
	reqBobList, _ := http.NewRequest("GET", server.URL+"/api/v1/sandboxes", nil)
	reqBobList.Header.Set("Authorization", "Bearer "+bobToken)
	respBobList, err := http.DefaultClient.Do(reqBobList)
	if err != nil || respBobList.StatusCode != http.StatusOK {
		t.Fatalf("Bob sandbox list failed: %v", respBobList)
	}
	var bobVMs []map[string]interface{}
	_ = json.NewDecoder(respBobList.Body).Decode(&bobVMs)
	if len(bobVMs) != 0 {
		t.Fatalf("Security failure: Bob saw %d sandboxes belonging to Alice!", len(bobVMs))
	}

	// 7. Multi-tenant isolation: Bob attempts to inspect Alice's sandbox -> must receive 404 Not Found
	reqBobInspect, _ := http.NewRequest("GET", server.URL+"/api/v1/sandboxes/sandbox-alice-1", nil)
	reqBobInspect.Header.Set("Authorization", "Bearer "+bobToken)
	respBobInspect, err := http.DefaultClient.Do(reqBobInspect)
	if err != nil || respBobInspect.StatusCode != http.StatusNotFound {
		t.Fatalf("Security failure: Bob inspecting Alice's sandbox expected 404 Not Found, got %d", respBobInspect.StatusCode)
	}

	// 8. Multi-tenant isolation: Bob attempts to delete Alice's sandbox -> must receive 404 Not Found
	reqBobDelete, _ := http.NewRequest("DELETE", server.URL+"/api/v1/sandboxes/sandbox-alice-1", nil)
	reqBobDelete.Header.Set("Authorization", "Bearer "+bobToken)
	respBobDelete, err := http.DefaultClient.Do(reqBobDelete)
	if err != nil || respBobDelete.StatusCode != http.StatusNotFound {
		t.Fatalf("Security failure: Bob deleting Alice's sandbox expected 404 Not Found, got %d", respBobDelete.StatusCode)
	}

	// 9. Alice lists sandboxes -> sees her sandbox
	reqAliceList, _ := http.NewRequest("GET", server.URL+"/api/v1/sandboxes", nil)
	reqAliceList.Header.Set("Authorization", "Bearer "+aliceToken)
	respAliceList, err := http.DefaultClient.Do(reqAliceList)
	if err != nil || respAliceList.StatusCode != http.StatusOK {
		t.Fatalf("Alice sandbox list failed: %v", respAliceList)
	}
	var aliceVMs []map[string]interface{}
	_ = json.NewDecoder(respAliceList.Body).Decode(&aliceVMs)
	if len(aliceVMs) != 1 {
		t.Fatalf("Expected Alice to see 1 sandbox, got %d", len(aliceVMs))
	}

	// 10. Admin using bootstrap secret inspects Alice's sandbox -> succeeds with 200 OK
	reqAdminInspect, _ := http.NewRequest("GET", server.URL+"/api/v1/sandboxes/sandbox-alice-1", nil)
	reqAdminInspect.Header.Set("Authorization", "Bearer admin-bootstrap-secret-999")
	respAdminInspect, err := http.DefaultClient.Do(reqAdminInspect)
	if err != nil || respAdminInspect.StatusCode != http.StatusOK {
		t.Fatalf("Admin inspect failed: %v", respAdminInspect)
	}
}
