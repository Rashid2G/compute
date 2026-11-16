package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/network"
)

func TestSanitizeServeSlug(t *testing.T) {
	cases := map[string]string{
		"quarry-3773":           "quarry-3773",
		"My App":                "my-app",
		"--web--8080--":         "web-8080",
		"@alice/web":            "alice-web",
		"":                      "serve",
		"UPPER_case.123":        "upper-case-123",
		strings.Repeat("a", 80): strings.Repeat("a", 48),
	}
	for in, want := range cases {
		if got := sanitizeServeSlug(in); got != want {
			t.Errorf("sanitizeServeSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestServePortModeAllocatesDedicatedListener verifies that port mode gives
// each exposure its own origin (host:port) and proxies straight to the guest.
func TestServePortModeAllocatesDedicatedListener(t *testing.T) {
	env := newMultiTenantServer(t)

	// Pick one free port for the serve range so the test cannot collide.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	servePort := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	env.server.cfg.Server.PublicURL = "http://compute.example.com"
	env.server.cfg.Server.ServePortStart = servePort
	env.server.cfg.Server.ServePortEnd = servePort
	env.server.cfg.Server.ServeBind = "127.0.0.1"
	t.Cleanup(func() { env.server.servePorts.close() })

	guestPort := startGuest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("port-mode:" + r.Host + r.URL.Path))
	}))

	mtSignup(t, env.ts, "portmode@test.local", "portmode", "password123!")
	token, userID := mtLogin(t, env.ts, "portmode", "password123!")
	vm := newRunningVM(t, env, "port-mode-app", "port-mode-app", userID)

	pf := exposeServe(t, env, token, vm.ID, guestPort, true)
	if pf.HostPort != servePort {
		t.Fatalf("host port = %d, want %d", pf.HostPort, servePort)
	}
	wantURL := fmt.Sprintf("http://compute.example.com:%d/", servePort)
	if pf.PublicURL != wantURL {
		t.Fatalf("public URL = %q, want %q", pf.PublicURL, wantURL)
	}

	res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/assets/app.js", servePort))
	if err != nil {
		t.Fatalf("serve listener request: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", res.StatusCode, body)
	}
	if want := fmt.Sprintf("port-mode:127.0.0.1:%d/assets/app.js", servePort); string(body) != want {
		t.Fatalf("body = %q, want %q", body, want)
	}

	// Removing the exposure closes its listener.
	req, _ := http.NewRequest(http.MethodDelete, env.ts.URL+"/api/v1/sandboxes/"+vm.ID+"/ports/"+strconv.Itoa(guestPort), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	del, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	del.Body.Close()
	if del.StatusCode != http.StatusOK {
		t.Fatalf("unexpose status = %d", del.StatusCode)
	}
	if _, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", servePort)); err == nil {
		t.Fatal("serve listener should be closed after unexpose")
	}
}

// TestServeAuthorizeMintsTokenForSessionUser covers the browser hop: the daemon
// session cookie (or bearer) is exchanged for a serve-scoped token.
func TestServeAuthorizeMintsTokenForSessionUser(t *testing.T) {
	env := newMultiTenantServer(t)
	guestPort := startGuest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("private app"))
	}))

	mtSignup(t, env.ts, "authz@test.local", "authz", "password123!")
	token, userID := mtLogin(t, env.ts, "authz", "password123!")
	vm := newRunningVM(t, env, "authz-app", "authz-app", userID)
	pf := exposeServe(t, env, token, vm.ID, guestPort, false)

	next := pf.PublicURL + "dashboard"
	req, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/api/v1/serve/authorize?next="+url.QueryEscape(next), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("authorize status = %d, want 302", res.StatusCode)
	}
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(loc.String(), pf.PublicURL) {
		t.Fatalf("redirect target = %q, want serve URL %q", loc.String(), pf.PublicURL)
	}
	if loc.Query().Get(network.ServeTokenQueryParam) == "" {
		t.Fatalf("redirect missing serve token: %q", loc.String())
	}

	// Unauthenticated callers are rejected by the auth middleware.
	res2, err := client.Get(env.ts.URL + "/api/v1/serve/authorize?next=" + url.QueryEscape(next))
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated authorize status = %d, want 401", res2.StatusCode)
	}

	// A foreign host is rejected even for an authenticated caller.
	res3, err := client.Do(func() *http.Request {
		r, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/api/v1/serve/authorize?next="+url.QueryEscape("https://evil.example/x"), nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}())
	if err != nil {
		t.Fatal(err)
	}
	res3.Body.Close()
	if res3.StatusCode != http.StatusBadRequest {
		t.Fatalf("foreign next status = %d, want 400", res3.StatusCode)
	}
}

// TestServeTokenEndpointReturnsTokenURL checks the CLI/SDK-facing endpoint.
func TestServeTokenEndpointReturnsTokenURL(t *testing.T) {
	env := newMultiTenantServer(t)
	guestPort := startGuest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	mtSignup(t, env.ts, "tok@test.local", "tok", "password123!")
	token, userID := mtLogin(t, env.ts, "tok", "password123!")
	vm := newRunningVM(t, env, "tok-app", "tok-app", userID)
	pf := exposeServe(t, env, token, vm.ID, guestPort, false)

	body, _ := json.Marshal(map[string]interface{}{"guest_port": guestPort})
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/v1/sandboxes/"+vm.ID+"/serve-token", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("serve-token status = %d: %s", res.StatusCode, raw)
	}
	var got struct {
		URL       string `json:"url"`
		PublicURL string `json:"public_url"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.PublicURL != pf.PublicURL {
		t.Fatalf("public_url = %q, want %q", got.PublicURL, pf.PublicURL)
	}
	parsed, _ := url.Parse(got.URL)
	if parsed == nil || parsed.Query().Get(network.ServeTokenQueryParam) == "" || got.ExpiresAt == "" {
		t.Fatalf("unexpected token response: %+v", got)
	}
}

// TestRestoreReopensPortModeListeners ensures exposures survive a restart.
func TestRestoreReopensPortModeListeners(t *testing.T) {
	env := newMultiTenantServer(t)
	guestPort := startGuest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("restored"))
	}))

	mtSignup(t, env.ts, "restore@test.local", "restore", "password123!")
	_, userID := mtLogin(t, env.ts, "restore", "password123!")
	vm := newRunningVM(t, env, "restore-app", "restore-app", userID)

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	servePort := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	env.server.cfg.Server.ServePortStart = servePort
	env.server.cfg.Server.ServePortEnd = servePort
	env.server.cfg.Server.ServeBind = "127.0.0.1"

	// Simulate a persisted port-mode exposure from a previous daemon run.
	_ = env.vmStore.SavePortForward(context.Background(), &db.PortForward{
		ID: "pf_restore", OwnerID: userID, VMID: vm.ID, VMName: vm.Name,
		HostPort: servePort, GuestPort: guestPort, Protocol: "tcp",
		Status: "active", Public: true,
	})
	env.server.servePorts.restore(context.Background())
	t.Cleanup(func() { env.server.servePorts.close() })

	res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", servePort))
	if err != nil {
		t.Fatalf("restored listener: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || string(body) != "restored" {
		t.Fatalf("restored serve status=%d body=%q", res.StatusCode, body)
	}
}
