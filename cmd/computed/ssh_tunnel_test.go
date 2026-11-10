package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/network"
	"github.com/Rashid2G/on-demand-vms/internal/storage"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

// TestSSH_ProxyCommand_RawStream tests raw stdio streaming through WebSocket to a mock TCP SSH listener.
func TestSSH_ProxyCommand_RawStream(t *testing.T) {
	// 1. Start mock TCP SSH listener
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock TCP listener: %v", err)
	}
	defer tcpListener.Close()

	tcpAddr := tcpListener.Addr().String()

	// Handle mock SSH server connection
	tcpDone := make(chan struct{})
	go func() {
		defer close(tcpDone)
		conn, aErr := tcpListener.Accept()
		if aErr != nil {
			return
		}
		defer conn.Close()

		// Read client banner
		buf := make([]byte, 1024)
		n, rErr := conn.Read(buf)
		if rErr != nil || n == 0 {
			return
		}
		clientBanner := string(buf[:n])
		if !strings.Contains(clientBanner, "SSH-2.0-ClientTest") {
			return
		}

		// Send server banner
		_, _ = conn.Write([]byte("SSH-2.0-MockServer_1.0\r\n"))

		// Echo back next message
		n, rErr = conn.Read(buf)
		if rErr != nil || n == 0 {
			return
		}
		_, _ = conn.Write(append([]byte("echo:"), buf[:n]...))
	}()

	// 2. Setup server with test VM whose IP points to the mock TCP server
	root := t.TempDir()
	store, err := db.NewStore(filepath.Join(root, "db", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	imagesDir := filepath.Join(root, "images")
	_ = os.MkdirAll(imagesDir, 0755)
	_ = os.WriteFile(filepath.Join(imagesDir, "rootfs.ext4"), []byte("test rootfs"), 0600)

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

	serverObj := NewServer(vm.NewManager(vm.Config{}, store, netMgr, storMgr), cfg)
	ts := httptest.NewServer(serverObj)
	t.Cleanup(ts.Close)

	// Save running VM record with IPAddress = mock TCP address
	vmRecord := &db.VM{
		ID:        "vm-raw-proxy-test",
		Name:      "raw-proxy-test",
		Status:    "running",
		IPAddress: tcpAddr,
		OwnerID:   "default",
	}
	if err := store.SaveVM(context.Background(), vmRecord); err != nil {
		t.Fatalf("failed to save VM record: %v", err)
	}

	// 3. Dial WebSocket with stream=raw and mode=stdio
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/vms/vm-raw-proxy-test/ssh?stream=raw&mode=stdio"
	wsCtx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()

	wsConn, _, err := websocket.Dial(wsCtx, wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial WebSocket: %v", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "test finish")

	// 4. Send client banner over WebSocket
	if err := wsConn.Write(wsCtx, websocket.MessageBinary, []byte("SSH-2.0-ClientTest\r\n")); err != nil {
		t.Fatalf("failed to write banner to WebSocket: %v", err)
	}

	// 5. Read server banner from WebSocket
	msgType, data, err := wsConn.Read(wsCtx)
	if err != nil {
		t.Fatalf("failed to read from WebSocket: %v", err)
	}
	if msgType != websocket.MessageBinary {
		t.Errorf("expected binary message type, got %v", msgType)
	}
	if !strings.Contains(string(data), "SSH-2.0-MockServer_1.0") {
		t.Errorf("unexpected server banner: %s", string(data))
	}

	// 6. Test payload echo
	payload := []byte("payload-data-over-websocket")
	if err := wsConn.Write(wsCtx, websocket.MessageBinary, payload); err != nil {
		t.Fatalf("failed to write payload to WebSocket: %v", err)
	}

	_, echoData, err := wsConn.Read(wsCtx)
	if err != nil {
		t.Fatalf("failed to read echo from WebSocket: %v", err)
	}
	expectedEcho := "echo:payload-data-over-websocket"
	if string(echoData) != expectedEcho {
		t.Errorf("expected %q, got %q", expectedEcho, string(echoData))
	}

	_ = wsConn.Close(websocket.StatusNormalClosure, "done")
	<-tcpDone
}

// TestSSH_MultiTenant_Rejection verifies unauthorized and cross-tenant callers are rejected (401/403/404).
func TestSSH_MultiTenant_Rejection(t *testing.T) {
	env := newMultiTenantServer(t)

	// Register Alice and Bob
	mtSignup(t, env.ts, "alice@test.local", "alice", "password-alice-123")
	aliceToken, aliceID := mtLogin(t, env.ts, "alice", "password-alice-123")
	aliceKey := mtCreateAPIKey(t, env.ts, aliceToken, "alice-key")

	mtSignup(t, env.ts, "bob@test.local", "bob", "password-bob-123")
	bobToken, _ := mtLogin(t, env.ts, "bob", "password-bob-123")
	bobKey := mtCreateAPIKey(t, env.ts, bobToken, "bob-key")

	// Create Alice's VM
	aliceVM := &db.VM{
		ID:        "alice-sandbox-01",
		Name:      "alice-vm",
		Status:    "running",
		IPAddress: "192.168.50.21",
		OwnerID:   aliceID,
	}
	if err := env.vmStore.SaveVM(context.Background(), aliceVM); err != nil {
		t.Fatalf("failed to save Alice's VM: %v", err)
	}

	// Subtest 1: Unauthenticated request (no token) -> 401 Unauthorized
	req, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/api/v1/vms/alice-sandbox-01/ssh", nil)
	resp, err := env.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for unauthenticated request, got %d", resp.StatusCode)
	}

	// Subtest 2: Cross-tenant HTTP request (Bob accessing Alice's VM) -> 404 Not Found
	resp, _ = mtRequest(t, env.ts, http.MethodGet, "/api/v1/vms/alice-sandbox-01/ssh", bobKey, nil)
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 404 or 403 for cross-tenant access, got %d", resp.StatusCode)
	}

	// Subtest 3: Cross-tenant WebSocket connection (Bob accessing Alice's VM) -> WebSocket rejection
	wsURL := "ws" + strings.TrimPrefix(env.ts.URL, "http") + "/api/v1/vms/alice-sandbox-01/ssh?api_key=" + url.QueryEscape(bobKey)
	wsCtx, wsCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer wsCancel()

	wsConn, _, wsErr := websocket.Dial(wsCtx, wsURL, nil)
	if wsErr == nil {
		// If upgrade was accepted, it must close immediately with error message
		_, msg, rErr := wsConn.Read(wsCtx)
		if rErr == nil && !strings.Contains(string(msg), "not found") && !strings.Contains(string(msg), "Error") {
			t.Errorf("expected error message or rejection for cross-tenant websocket, got: %s", string(msg))
		}
		_ = wsConn.Close(websocket.StatusNormalClosure, "test")
	}

	// Subtest 4: Alice accessing her own VM -> Allowed past auth/ownership checks
	resp, _ = mtRequest(t, env.ts, http.MethodGet, "/api/v1/vms/alice-sandbox-01/ssh", aliceKey, nil)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		t.Errorf("expected Alice to pass auth and ownership, got status %d", resp.StatusCode)
	}
}

// TestSSH_ScopedKey_Enforcement verifies scoped API key with read:vms is rejected with 403 Forbidden, while key with ssh:vms is allowed.
func TestSSH_ScopedKey_Enforcement(t *testing.T) {
	env := newMultiTenantServer(t)

	mtSignup(t, env.ts, "scopeduser@test.local", "scopeduser", "password-scoped-123")
	userToken, userID := mtLogin(t, env.ts, "scopeduser", "password-scoped-123")

	// Create user VM
	userVM := &db.VM{
		ID:        "scoped-vm-01",
		Name:      "scoped-vm",
		Status:    "running",
		IPAddress: "192.168.50.33",
		OwnerID:   userID,
	}
	if err := env.vmStore.SaveVM(context.Background(), userVM); err != nil {
		t.Fatalf("failed to save VM: %v", err)
	}

	createKeyWithScope := func(scopeName string) string {
		payload := map[string]interface{}{
			"name":  "test-key-" + scopeName,
			"scope": scopeName,
		}
		resp, body := mtRequest(t, env.ts, http.MethodPost, "/api/v1/api-keys", userToken, payload)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("failed to create API key with scope %s: status %d body %s", scopeName, resp.StatusCode, string(body))
		}
		var outKey db.AccessAPIKey
		if err := json.Unmarshal(body, &outKey); err != nil {
			t.Fatalf("decode key: %v", err)
		}
		if outKey.Token == "" {
			t.Fatalf("key has empty token: %s", string(body))
		}
		return outKey.Token
	}

	// 1. Narrow scope: read:vms MUST be rejected with 403 Forbidden
	readKey := createKeyWithScope("read:vms")
	resp, body := mtRequest(t, env.ts, http.MethodGet, "/api/v1/vms/scoped-vm-01/ssh", readKey, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for read:vms key on SSH endpoint, got %d (body: %s)", resp.StatusCode, string(body))
	}

	// 2. Direct SSH endpoint /api/v1/ssh/{id} with read:vms MUST also be rejected with 403
	resp, body = mtRequest(t, env.ts, http.MethodGet, "/api/v1/ssh/scoped-vm-01", readKey, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for read:vms key on /api/v1/ssh/, got %d (body: %s)", resp.StatusCode, string(body))
	}

	// 3. Narrow scope: write:vms MUST also be rejected with 403 Forbidden
	writeKey := createKeyWithScope("write:vms")
	resp, body = mtRequest(t, env.ts, http.MethodGet, "/api/v1/vms/scoped-vm-01/ssh", writeKey, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for write:vms key on SSH endpoint, got %d (body: %s)", resp.StatusCode, string(body))
	}

	// 4. Scope: ssh:vms MUST be permitted
	sshKey := createKeyWithScope("ssh:vms")
	resp, body = mtRequest(t, env.ts, http.MethodGet, "/api/v1/vms/scoped-vm-01/ssh", sshKey, nil)
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("expected ssh:vms key to be permitted, got 403 Forbidden (body: %s)", string(body))
	}

	// 5. Full wildcard scope: * MUST be permitted
	wildcardKey := createKeyWithScope("*")
	resp, body = mtRequest(t, env.ts, http.MethodGet, "/api/v1/vms/scoped-vm-01/ssh", wildcardKey, nil)
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("expected * wildcard key to be permitted, got 403 Forbidden (body: %s)", string(body))
	}
}

// TestSSH_AutoGenerateHostKey verifies host key auto-generation when none exists.
func TestSSH_AutoGenerateHostKey(t *testing.T) {
	tempDir := t.TempDir()
	keyPath := filepath.Join(tempDir, "images", "ssh", "id_ed25519")

	cfg := config.DefaultConfig()
	cfg.Images.SSHKeyPath = keyPath
	cfg.Images.ImagesDir = filepath.Join(tempDir, "images")

	store, err := db.NewStore(filepath.Join(tempDir, "db", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	storMgr, err := storage.NewManager(storage.Config{
		BaseImagesDir: filepath.Join(tempDir, "images"),
		VMsDir:        filepath.Join(tempDir, "vms"),
		SSHKeyPath:    keyPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	netMgr, err := network.NewManager(network.Config{SubnetCIDR: "192.168.50.0/24"}, testRunner{})
	if err != nil {
		t.Fatal(err)
	}

	server := NewServer(vm.NewManager(vm.Config{}, store, netMgr, storMgr), cfg)

	// Ensure key file does not exist initially
	if _, err := os.Stat(keyPath); err == nil {
		t.Fatalf("key file should not exist initially at %s", keyPath)
	}

	// Resolve SSH key - should trigger auto-generation of master host Ed25519 key
	methods := server.resolveSSHKey("", "", "")
	if len(methods) == 0 {
		t.Fatalf("expected non-empty auth methods after auto-generation")
	}

	// Verify key file was created with 0600 permissions
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("expected key file to be created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected file mode 0600, got %o", perm)
	}

	// Verify public key was created
	pubBytes, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatalf("expected public key file to be created: %v", err)
	}
	if !strings.HasPrefix(string(pubBytes), "ssh-ed25519") {
		t.Errorf("expected Ed25519 public key format, got: %s", string(pubBytes))
	}

	// Parse private key to verify it is valid OpenSSH Ed25519
	privBytes, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("failed to read private key: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(privBytes)
	if err != nil {
		t.Fatalf("failed to parse generated private key: %v", err)
	}
	if signer.PublicKey().Type() != "ssh-ed25519" {
		t.Errorf("expected ssh-ed25519 key type, got: %s", signer.PublicKey().Type())
	}
}

// TestSetupHostScript tests script syntax and dry-run execution.
func TestSetupHostScript(t *testing.T) {
	// Find scripts/setup-host.sh
	scriptPath, err := filepath.Abs("../../scripts/setup-host.sh")
	if err != nil || !fileExists(scriptPath) {
		// Try from repository root
		scriptPath, _ = filepath.Abs("scripts/setup-host.sh")
	}
	if !fileExists(scriptPath) {
		t.Skipf("setup-host.sh not found at %s", scriptPath)
	}

	// 1. bash -n syntax validation
	syntaxCmd := exec.Command("bash", "-n", scriptPath)
	out, err := syntaxCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n failed for %s: %v (output: %s)", scriptPath, err, string(out))
	}

	// 2. DRY_RUN=1 execution
	dryRunCmd := exec.Command("bash", scriptPath)
	dryRunCmd.Env = append(os.Environ(), "DRY_RUN=1")
	out, err = dryRunCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("DRY_RUN execution failed: %v (output: %s)", err, string(out))
	}

	outStr := string(out)
	if !strings.Contains(outStr, "Preflight System Checks") {
		t.Errorf("expected output to contain preflight checks, got: %s", outStr)
	}
	if !strings.Contains(outStr, "Dry run / Check-only mode complete") {
		t.Errorf("expected output to complete dry-run, got: %s", outStr)
	}
}

// TestIPAM_50PlusConcurrency tests allocating 50+ concurrent IPs without collisions.
func TestIPAM_50PlusConcurrency(t *testing.T) {
	ipam, err := network.NewIPAM("172.16.0.0/16")
	if err != nil {
		t.Fatalf("failed to create IPAM: %v", err)
	}

	const concurrentCount = 60
	type allocResult struct {
		ip  string
		mac string
		err error
	}

	var mu sync.Mutex
	used := make(map[string]bool)
	results := make([]allocResult, concurrentCount)

	var wg sync.WaitGroup
	wg.Add(concurrentCount)

	for i := 0; i < concurrentCount; i++ {
		go func(idx int) {
			defer wg.Done()
			mu.Lock()
			ip, mac, aErr := ipam.AllocateNextIP(used)
			if aErr == nil {
				used[ip] = true
			}
			mu.Unlock()
			results[idx] = allocResult{ip: ip, mac: mac, err: aErr}
		}(i)
	}

	wg.Wait()

	seenIPs := make(map[string]bool)
	seenMACs := make(map[string]bool)

	for i, res := range results {
		if res.err != nil {
			t.Fatalf("allocation %d failed: %v", i, res.err)
		}
		if seenIPs[res.ip] {
			t.Errorf("duplicate IP allocated: %s", res.ip)
		}
		seenIPs[res.ip] = true

		if seenMACs[res.mac] {
			t.Errorf("duplicate MAC allocated: %s", res.mac)
		}
		seenMACs[res.mac] = true
	}

	if len(seenIPs) != concurrentCount {
		t.Errorf("expected %d unique IPs, got %d", concurrentCount, len(seenIPs))
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
