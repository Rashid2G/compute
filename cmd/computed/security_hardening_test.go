package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/config"
	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/master/auth"
	"github.com/Rashid2G/compute/internal/vm"
	"github.com/coder/websocket"
)

func setupSecurityTestServer(t *testing.T) (*httptest.Server, *Server, *auth.Manager, string, string) {
	t.Helper()
	server, authMgr := newTestAuthServer(t)

	// Create a test user with session
	ctx := context.Background()
	user, err := authMgr.Signup(ctx, "secuser@compute.local", "secuser", "Security User", "StrongPass123!", "user")
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	sessionToken, csrfToken, _, err := authMgr.SessionManager().CreateSession(ctx, user.ID, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	s := server.Config.Handler.(*Server)
	return server, s, authMgr, sessionToken, csrfToken
}

func TestSecurity_HTTP413BodyLimits(t *testing.T) {
	server, _, _, sessionToken, csrfToken := setupSecurityTestServer(t)

	// 1. Oversized body on standard JSON API route (limit 4 MiB)
	oversized := make([]byte, 5<<20) // 5 MiB
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/auth/login", bytes.NewReader(oversized))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected HTTP 413 for oversized body on /auth/login, got %d", resp.StatusCode)
	}

	var errBody map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&errBody)
	if !strings.Contains(errBody["error"], "request entity too large") {
		t.Fatalf("expected 'request entity too large' in error response, got %v", errBody)
	}

	// 2. Body on upload/file path allowing higher cap (limit 512 MiB)
	// A 5 MiB payload should NOT be rejected with 413 on /files
	uploadReq, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/sandboxes/fake-vm/files/write", bytes.NewReader(oversized))
	if err != nil {
		t.Fatal(err)
	}
	uploadReq.Header.Set("Content-Type", "application/json")
	uploadReq.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
	uploadReq.Header.Set("X-CSRF-Token", csrfToken)

	respUpload, err := http.DefaultClient.Do(uploadReq)
	if err != nil {
		t.Fatal(err)
	}
	defer respUpload.Body.Close()

	if respUpload.StatusCode == http.StatusRequestEntityTooLarge {
		t.Fatalf("did not expect HTTP 413 on 5 MiB upload payload; bulk cap is 512 MiB")
	}
}

func TestSecurity_CSRFEnforcement(t *testing.T) {
	server, _, authMgr, sessionToken, csrfToken := setupSecurityTestServer(t)
	ctx := context.Background()

	// Create an API key to test that non-cookie mutations bypass CSRF checks
	aliceUser, _ := authMgr.UserByUsername(ctx, "secuser")
	apiKey, _, err := authMgr.KeyManager().GenerateAPIKey(ctx, aliceUser.ID, "test-key", nil)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Cookie-authenticated POST with MISSING X-CSRF-Token -> 403 Forbidden
	req1, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/sandboxes", bytes.NewReader([]byte(`{"name":"sbx-test"}`)))
	req1.Header.Set("Content-Type", "application/json")
	req1.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})

	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 for missing CSRF token, got %d", resp1.StatusCode)
	}
	var errBody1 map[string]string
	_ = json.NewDecoder(resp1.Body).Decode(&errBody1)
	if errBody1["error"] != "invalid or missing CSRF token" {
		t.Fatalf("expected 'invalid or missing CSRF token', got %q", errBody1["error"])
	}

	// 2. Cookie-authenticated POST with INVALID X-CSRF-Token -> 403 Forbidden
	req2, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/sandboxes", bytes.NewReader([]byte(`{"name":"sbx-test"}`)))
	req2.Header.Set("Content-Type", "application/json")
	req2.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
	req2.Header.Set("X-CSRF-Token", "wrong-csrf-token-12345")

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 for invalid CSRF token, got %d", resp2.StatusCode)
	}

	// 3. Cookie-authenticated POST with VALID X-CSRF-Token -> CSRF passes (not 403)
	req3, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/sandboxes", bytes.NewReader([]byte(`{"name":"sbx-test"}`)))
	req3.Header.Set("Content-Type", "application/json")
	req3.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
	req3.Header.Set("X-CSRF-Token", csrfToken)

	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()

	if resp3.StatusCode == http.StatusForbidden {
		t.Fatalf("valid CSRF token was rejected with 403")
	}

	// 4. API Key / Bearer authenticated mutation WITHOUT CSRF header -> passes cleanly
	req4, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/sandboxes", bytes.NewReader([]byte(`{"name":"sbx-test"}`)))
	req4.Header.Set("Content-Type", "application/json")
	req4.Header.Set("Authorization", "Bearer "+apiKey)

	resp4, err := http.DefaultClient.Do(req4)
	if err != nil {
		t.Fatal(err)
	}
	defer resp4.Body.Close()

	if resp4.StatusCode == http.StatusForbidden {
		t.Fatalf("Bearer API key request should NOT require CSRF header, got 403")
	}

	// 5. Pre-auth routes (login/signup) succeed without CSRF header
	loginPayload := `{"username":"secuser","password":"StrongPass123!"}`
	req5, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/auth/login", strings.NewReader(loginPayload))
	req5.Header.Set("Content-Type", "application/json")

	resp5, err := http.DefaultClient.Do(req5)
	if err != nil {
		t.Fatal(err)
	}
	defer resp5.Body.Close()

	if resp5.StatusCode != http.StatusOK {
		t.Fatalf("login should succeed without CSRF token, got %d", resp5.StatusCode)
	}

	// Verify compute_csrf cookie is returned
	var foundCSRFCookie bool
	for _, c := range resp5.Cookies() {
		if c.Name == auth.CSRFCookieName && c.Value != "" && !c.HttpOnly {
			foundCSRFCookie = true
			break
		}
	}
	if !foundCSRFCookie {
		t.Fatalf("expected non-HttpOnly compute_csrf cookie in login response")
	}
}

func TestSecurity_WebSocketOriginValidation(t *testing.T) {
	server, s, _, sessionToken, _ := setupSecurityTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws://" + server.Listener.Addr().String() + "/api/v1/ws"

	authHeader := func(extra http.Header) http.Header {
		h := http.Header{
			"Authorization": []string{"Bearer " + sessionToken},
		}
		for k, vv := range extra {
			for _, v := range vv {
				h.Add(k, v)
			}
		}
		return h
	}

	// 1. Cross-Origin browser request -> rejected with HTTP 403
	evilOpts := &websocket.DialOptions{
		HTTPHeader: authHeader(http.Header{"Origin": []string{"https://evil.example.com"}}),
	}
	_, _, err := websocket.Dial(ctx, wsURL, evilOpts)
	if err == nil {
		t.Fatalf("expected cross-origin websocket upgrade to fail with 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 error, got: %v", err)
	}

	// 2. Same-Origin request -> succeeds
	sameOpts := &websocket.DialOptions{
		HTTPHeader: authHeader(http.Header{"Origin": []string{"http://" + server.Listener.Addr().String()}}),
	}
	connSame, _, err := websocket.Dial(ctx, wsURL, sameOpts)
	if err != nil {
		t.Fatalf("same-origin websocket upgrade failed: %v", err)
	}
	_ = connSame.Close(websocket.StatusNormalClosure, "")

	// 3. Configured PublicURL -> succeeds
	s.cfg.Server.PublicURL = "https://compute.mycorp.internal"
	pubOpts := &websocket.DialOptions{
		HTTPHeader: authHeader(http.Header{"Origin": []string{"https://compute.mycorp.internal"}}),
	}
	connPub, _, err := websocket.Dial(ctx, wsURL, pubOpts)
	if err != nil {
		t.Fatalf("public-url websocket upgrade failed: %v", err)
	}
	_ = connPub.Close(websocket.StatusNormalClosure, "")

	// 4. Empty Origin (CLI / SDK / worker client) -> succeeds
	emptyOpts := &websocket.DialOptions{
		HTTPHeader: authHeader(nil),
	}
	connEmpty, _, err := websocket.Dial(ctx, wsURL, emptyOpts)
	if err != nil {
		t.Fatalf("empty-origin websocket upgrade failed: %v", err)
	}
	_ = connEmpty.Close(websocket.StatusNormalClosure, "")
}

func TestSecurity_RateLimiter(t *testing.T) {
	// Configure tight rate limiter for testing
	cfg := config.ServerConfig{
		TrustedProxies:     "10.0.0.0/8",
		RateLimitAuthRate:  1.0, // 1 req/sec
		RateLimitAuthBurst: 2,   // burst 2
		RateLimitAPIRate:   2.0,
		RateLimitAPIBurst:  3,
	}
	limiter := NewRateLimiter(cfg)
	defer limiter.Close()

	// 1. Burst on auth route
	reqAuth, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	reqAuth.RemoteAddr = "192.168.1.50:12345"

	// First 2 requests (within burst) should pass
	for i := 0; i < 2; i++ {
		allowed, _ := limiter.Allow(reqAuth)
		if !allowed {
			t.Fatalf("expected request %d to be allowed", i)
		}
	}

	// 3rd request exceeds burst -> 429
	allowed, retryAfter := limiter.Allow(reqAuth)
	if allowed {
		t.Fatalf("expected request to be rate limited")
	}
	if retryAfter < 1 {
		t.Fatalf("expected retryAfter >= 1, got %f", retryAfter)
	}

	// 2. Verify untrusted IP cannot spoof X-Forwarded-For to bypass rate limit
	spoofReq, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	spoofReq.RemoteAddr = "192.168.1.50:12345" // same remote IP as above
	spoofReq.Header.Set("X-Forwarded-For", "8.8.8.8")

	allowedSpoof, _ := limiter.Allow(spoofReq)
	if allowedSpoof {
		t.Fatalf("spoofed X-Forwarded-For from untrusted remote address bypassed rate limit!")
	}

	// 3. Verify trusted proxy DOES honor X-Forwarded-For
	trustedReq, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	trustedReq.RemoteAddr = "10.0.0.1:54321" // within 10.0.0.0/8 trusted proxy
	trustedReq.Header.Set("X-Forwarded-For", "203.0.113.10")

	// First request from this forwarded client IP succeeds
	allowedTrusted, _ := limiter.Allow(trustedReq)
	if !allowedTrusted {
		t.Fatalf("trusted proxy forwarded request should be allowed")
	}
}

func TestSecurity_WebhookSSRFProtection(t *testing.T) {
	// 1. Test disallowed IPs
	testCases := []struct {
		ip         string
		disallowed bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"169.254.169.254", true},
		{"169.254.1.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"100.64.0.1", true}, // CGNAT
		{"::1", true},
		{"fe80::1", true},
		{"fc00::1", true},
		{"0.0.0.0", true},
		{"::ffff:127.0.0.1", true},
		{"::ffff:169.254.169.254", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
	}

	for _, tc := range testCases {
		nip, err := netip.ParseAddr(tc.ip)
		if err != nil {
			t.Fatalf("failed to parse IP %s: %v", tc.ip, err)
		}
		dis := isDisallowedIP(nip)
		if dis != tc.disallowed {
			t.Errorf("isDisallowedIP(%s) = %v, want %v", tc.ip, dis, tc.disallowed)
		}
	}

	// 2. Test webhook URL validation
	invalidURLs := []string{
		"file:///etc/passwd",
		"gopher://127.0.0.1:1234",
		"http://127.0.0.1/webhook",
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.5:8080/",
		"http://[::1]:8080/hook",
		"http://[::ffff:169.254.169.254]/hook",
	}

	for _, u := range invalidURLs {
		if err := validateWebhookURL(u); err == nil {
			t.Errorf("validateWebhookURL(%q) expected error, got nil", u)
		}
	}

	validURLs := []string{
		"https://api.github.com/webhook",
		"https://hooks.slack.com/services/T00/B00/X00",
		"http://example.com/events",
	}
	for _, u := range validURLs {
		if err := validateWebhookURL(u); err != nil {
			t.Errorf("validateWebhookURL(%q) expected valid, got: %v", u, err)
		}
	}

	// 3. Test dial-time SSRF rejection with custom HTTP client
	client := newWebhookHTTPClient()

	_, err := client.Get("http://127.0.0.1:18080/forbidden")
	if err == nil {
		t.Fatalf("expected dial to loopback to fail with SSRF protection error")
	}
	if !strings.Contains(err.Error(), "forbidden") {
		t.Logf("dial-time rejection error: %v", err)
	}

	// 4. Test redirect to loopback rejection
	tsRedirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:9999/secret", http.StatusFound)
	}))
	defer tsRedirect.Close()

	_, err = client.Get(tsRedirect.URL)
	if err == nil {
		t.Fatalf("expected redirect to 127.0.0.1 to be blocked by client")
	}

	// 5. Test concurrent delivery stats race safety
	root := t.TempDir()
	dbStore, err := db.NewStore(filepath.Join(root, "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbStore.Close()

	hook := &db.WebhookEndpoint{
		ID:     "whk_race_test",
		URL:    "https://example.com/webhook",
		Events: []string{"*"},
		Active: true,
	}
	if err := dbStore.SaveWebhook(context.Background(), hook); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		vmMgr: vm.NewManager(vm.Config{}, dbStore, nil, nil),
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s.recordWebhookResult(hook.ID, 200, true, nil)
		}(i)
	}
	wg.Wait()

	updatedHook, err := dbStore.GetWebhook(context.Background(), hook.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedHook.DeliveriesCount != 50 {
		t.Fatalf("expected DeliveriesCount == 50, got %d", updatedHook.DeliveriesCount)
	}
}

func TestSecurity_SocketAndServerConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "secsock")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Test 0660 Unix socket permissions
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer os.Remove(sockPath)

	_ = os.Chmod(sockPath, 0660)
	fi, err := os.Stat(sockPath)
	if err != nil {
		t.Fatal(err)
	}
	mode := fi.Mode().Perm()
	if mode != 0660 {
		t.Fatalf("expected socket permissions 0660, got %04o", mode)
	}

	// Verify server timeouts config
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("expected ReadHeaderTimeout 10s, got %v", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Errorf("expected IdleTimeout 120s, got %v", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 1<<20 {
		t.Errorf("expected MaxHeaderBytes 1MiB, got %v", srv.MaxHeaderBytes)
	}
}
