package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/network"
	"github.com/coder/websocket"
)

// exposeServe publishes a guest port through the API and returns the record.
func exposeServe(t *testing.T, env *multiTenantEnv, token, vmID string, guestPort int, public bool) db.PortForward {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"guest_port": guestPort, "public": public})
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/v1/sandboxes/"+vmID+"/expose", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("expose: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("expose status = %d: %s", res.StatusCode, raw)
	}
	var pf db.PortForward
	if err := json.NewDecoder(res.Body).Decode(&pf); err != nil {
		t.Fatal(err)
	}
	return pf
}

// serveRequest builds a request for an exposure host, which is how a browser
// reaches the sandbox in subdomain mode.
func serveRequest(t *testing.T, env *multiTenantEnv, slug, path string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, env.ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = slug + ".localhost"
	return req
}

func newRunningVM(t *testing.T, env *multiTenantEnv, id, name, ownerID string) *db.VM {
	t.Helper()
	v := &db.VM{ID: id, Name: name, OwnerID: ownerID, Status: db.StatusRunning, IPAddress: "127.0.0.1"}
	if err := env.vmStore.SaveVM(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	return v
}

func startGuest(t *testing.T, handler http.Handler) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func TestIngressProxy_ServeSubdomainRouting(t *testing.T) {
	env := newMultiTenantServer(t)
	guestPort := startGuest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("guest:" + r.Host + r.URL.Path))
	}))

	mtSignup(t, env.ts, "serve@test.local", "serveuser", "password123!")
	token, userID := mtLogin(t, env.ts, "serveuser", "password123!")
	vm := newRunningVM(t, env, "serve-app", "serve-app", userID)

	pf := exposeServe(t, env, token, vm.ID, guestPort, true)
	if pf.Subdomain == "" {
		t.Fatalf("exposure has no serve slug: %+v", pf)
	}
	if !strings.HasPrefix(pf.PublicURL, "http://"+pf.Subdomain+".localhost:") {
		t.Fatalf("unexpected serve URL %q", pf.PublicURL)
	}

	req := serveRequest(t, env, pf.Subdomain, "/assets/app.js")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", res.StatusCode, body)
	}
	// The app sees its own origin and the requested path, so absolute URLs and
	// client-side routing need no rewriting.
	if want := "guest:" + pf.Subdomain + ".localhost/assets/app.js"; string(body) != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

// A redirect issued by the app must stay on the serve origin. This is the
// regression that path-prefix serving could not solve.
func TestIngressProxy_ServeRedirectsStayOnOrigin(t *testing.T) {
	env := newMultiTenantServer(t)
	guestPort := startGuest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/go" {
			http.Redirect(w, r, "/login?next=%2Fdashboard", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))

	mtSignup(t, env.ts, "redir@test.local", "rediruser", "password123!")
	token, userID := mtLogin(t, env.ts, "rediruser", "password123!")
	vm := newRunningVM(t, env, "redir-app", "redir-app", userID)
	pf := exposeServe(t, env, token, vm.ID, guestPort, true)

	req := serveRequest(t, env, pf.Subdomain, "/go")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if got, want := res.Header.Get("Location"), "/login?next=%2Fdashboard"; got != want {
		t.Fatalf("Location = %q, want %q (must not be rewritten)", got, want)
	}
}

func TestIngressProxy_PublicVsPrivateAuth(t *testing.T) {
	env := newMultiTenantServer(t)
	guestPort := startGuest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret guest app"))
	}))

	mtSignup(t, env.ts, "alice@test.local", "alice", "password123!")
	tokenA, userAID := mtLogin(t, env.ts, "alice", "password123!")
	mtSignup(t, env.ts, "bob@test.local", "bob", "password123!")
	tokenB, _ := mtLogin(t, env.ts, "bob", "password123!")
	vm := newRunningVM(t, env, "alice-app", "alice-app", userAID)

	pf := exposeServe(t, env, tokenA, vm.ID, guestPort, false)

	// 1. No credentials -> 401 for API clients.
	req := serveRequest(t, env, pf.Subdomain, "/")
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", res.StatusCode)
	}

	// 2. A different tenant cannot open it.
	req = serveRequest(t, env, pf.Subdomain, "/")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenB)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cross-tenant status = %d, want 401", res.StatusCode)
	}

	// 3. The owner can open it with a bearer token.
	req = serveRequest(t, env, pf.Subdomain, "/")
	req.Header.Set("Authorization", "Bearer "+tokenA)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "secret guest app") {
		t.Fatalf("owner status = %d body=%s", res.StatusCode, body)
	}

	// 4. Browser navigation bounces through the daemon to mint a token.
	req = serveRequest(t, env, pf.Subdomain, "/")
	req.Header.Set("Accept", "text/html")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("browser redirect status = %d, want 302", res.StatusCode)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	if loc == nil || loc.Path != "/api/v1/serve/authorize" || loc.Query().Get("next") == "" {
		t.Fatalf("unexpected authorize redirect %q", res.Header.Get("Location"))
	}

	// 5. The serve-token API returns a URL that exchanges into a cookie.
	tokBody, _ := json.Marshal(map[string]interface{}{"guest_port": guestPort})
	tokReq, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/v1/sandboxes/"+vm.ID+"/serve-token", bytes.NewReader(tokBody))
	tokReq.Header.Set("Authorization", "Bearer "+tokenA)
	tokRes, err := http.DefaultClient.Do(tokReq)
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		URL       string `json:"url"`
		ExpiresAt string `json:"expires_at"`
	}
	_ = json.NewDecoder(tokRes.Body).Decode(&tok)
	tokRes.Body.Close()
	if tok.URL == "" || tok.ExpiresAt == "" {
		t.Fatalf("serve-token response = %+v", tok)
	}
	parsed, err := url.Parse(tok.URL)
	if err != nil {
		t.Fatal(err)
	}
	token := parsed.Query().Get(network.ServeTokenQueryParam)
	if token == "" {
		t.Fatalf("serve token missing from %q", tok.URL)
	}

	req = serveRequest(t, env, pf.Subdomain, "/?"+network.ServeTokenQueryParam+"="+url.QueryEscape(token))
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/" {
		t.Fatalf("token exchange status=%d location=%q", res.StatusCode, res.Header.Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == network.ServeCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("no %s cookie set: %v", network.ServeCookieName, res.Header.Values("Set-Cookie"))
	}

	req = serveRequest(t, env, pf.Subdomain, "/")
	req.AddCookie(cookie)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "secret guest app") {
		t.Fatalf("cookie auth status=%d body=%s", res.StatusCode, body)
	}
}

func TestIngressProxy_ServeSlugsAreTenantIsolated(t *testing.T) {
	env := newMultiTenantServer(t)
	guestPort := startGuest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tenant app"))
	}))

	mtSignup(t, env.ts, "carol@test.local", "carol", "password123!")
	carolToken, carolID := mtLogin(t, env.ts, "carol", "password123!")
	mtSignup(t, env.ts, "dave@test.local", "dave", "password123!")
	_, daveID := mtLogin(t, env.ts, "dave", "password123!")

	// Both tenants use the same sandbox name; each exposure gets its own slug.
	carolVM := newRunningVM(t, env, "vm-carol-web", "web", carolID)
	daveVM := newRunningVM(t, env, "vm-dave-web", "web", daveID)

	carolPF := exposeServe(t, env, carolToken, carolVM.ID, guestPort, true)
	// Dave's exposure is created by an admin-style direct store write so the
	// test does not need his token for the API call.
	if err := env.vmStore.SavePortForward(context.Background(), &db.PortForward{
		ID: "pf_dave", OwnerID: daveID, VMID: daveVM.ID, VMName: daveVM.Name,
		GuestPort: guestPort, Protocol: "tcp", Subdomain: "web-" + fmt.Sprint(guestPort) + "-dave",
		Status: "active", Public: false,
	}); err != nil {
		t.Fatal(err)
	}

	if carolPF.Subdomain == "web-"+fmt.Sprint(guestPort)+"-dave" {
		t.Fatalf("slugs collided: %q", carolPF.Subdomain)
	}

	// Carol's public exposure is reachable; Dave's private one is not.
	res, err := http.DefaultClient.Do(serveRequest(t, env, carolPF.Subdomain, "/"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("carol public serve status = %d", res.StatusCode)
	}

	req := serveRequest(t, env, "web-"+fmt.Sprint(guestPort)+"-dave", "/")
	req.Header.Set("Accept", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("dave private serve status = %d, want 401", res.StatusCode)
	}
}

func TestIngressProxy_SubdomainRouting(t *testing.T) {
	env := newMultiTenantServer(t)

	guestListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer guestListener.Close()

	guestPort := guestListener.Addr().(*net.TCPAddr).Port
	guestMux := http.NewServeMux()
	guestMux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("dashboard-ok:" + r.Header.Get("X-Forwarded-Host")))
	})
	guestServer := &http.Server{Handler: guestMux}
	go guestServer.Serve(guestListener)
	defer guestServer.Close()

	mtSignup(t, env.ts, "owner_sub@test.local", "ownersub", "password123!")
	tokenA, userAID := mtLogin(t, env.ts, "ownersub", "password123!")
	vmA := &db.VM{
		ID:        "web-sandbox-1",
		Name:      "web-sandbox-1",
		OwnerID:   userAID,
		Status:    db.StatusRunning,
		IPAddress: "127.0.0.1",
	}
	if err := env.vmStore.SaveVM(context.Background(), vmA); err != nil {
		t.Fatal(err)
	}

	// Form 1: <port>-<target>.<baseDomain> e.g. "8080-web-sandbox-1.preview.localhost"
	req1, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/dashboard", nil)
	req1.Host = fmt.Sprintf("%d-%s.preview.localhost", guestPort, vmA.Name)
	req1.Header.Set("Authorization", "Bearer "+tokenA)
	res1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(res1.Body)
	res1.Body.Close()
	if res1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for <port>-<target> subdomain, got %d: %s", res1.StatusCode, string(body1))
	}
	if !strings.HasPrefix(string(body1), "dashboard-ok:") {
		t.Fatalf("expected dashboard response from guest, got: %s", string(body1))
	}

	// Form 2: <target>-<port>.<baseDomain> e.g. "web-sandbox-1-8080.preview.localhost"
	req2, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/dashboard", nil)
	req2.Host = fmt.Sprintf("%s-%d.compute.internal", vmA.Name, guestPort)
	req2.Header.Set("Authorization", "Bearer "+tokenA)
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(res2.Body)
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for <target>-<port> subdomain, got %d: %s", res2.StatusCode, string(body2))
	}
	if !strings.HasPrefix(string(body2), "dashboard-ok:") {
		t.Fatalf("expected dashboard response from guest, got: %s", string(body2))
	}
}

func TestIngressProxy_WebSocketUpgrade(t *testing.T) {
	env := newMultiTenantServer(t)

	guestPort := startGuest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		for {
			typ, msg, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if err := conn.Write(ctx, typ, []byte("echo: "+string(msg))); err != nil {
				return
			}
		}
	}))

	mtSignup(t, env.ts, "ws_owner@test.local", "wsowner", "password123!")
	tokenA, userAID := mtLogin(t, env.ts, "wsowner", "password123!")
	vmA := newRunningVM(t, env, "ws-sandbox", "ws-sandbox", userAID)
	pf := exposeServe(t, env, tokenA, vmA.ID, guestPort, true)

	wsURL := fmt.Sprintf("ws://%s/ws", env.ts.Listener.Addr().String())
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()

	wsConn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		Host: pf.Subdomain + ".localhost",
	})
	if err != nil {
		t.Fatalf("failed to dial websocket through serve proxy: %v", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "")

	if err := wsConn.Write(dialCtx, websocket.MessageText, []byte("ping-serve")); err != nil {
		t.Fatalf("failed to write websocket message: %v", err)
	}
	_, replyBytes, err := wsConn.Read(dialCtx)
	if err != nil {
		t.Fatalf("failed to read websocket response: %v", err)
	}
	if string(replyBytes) != "echo: ping-serve" {
		t.Fatalf("expected 'echo: ping-serve', got: %s", string(replyBytes))
	}
}

func TestIngressProxy_ServerSentEventsStreaming(t *testing.T) {
	env := newMultiTenantServer(t)

	guestPort := startGuest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		for i := 1; i <= 3; i++ {
			_, _ = fmt.Fprintf(w, "data: event-%d\n\n", i)
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))

	mtSignup(t, env.ts, "sse@test.local", "sseuser", "password123!")
	token, userID := mtLogin(t, env.ts, "sseuser", "password123!")
	vm := newRunningVM(t, env, "sse-app", "sse-app", userID)
	pf := exposeServe(t, env, token, vm.ID, guestPort, true)

	req := serveRequest(t, env, pf.Subdomain, "/events")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	scanner := bufio.NewScanner(res.Body)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	joined := strings.Join(lines, "\n")
	for i := 1; i <= 3; i++ {
		if !strings.Contains(joined, fmt.Sprintf("data: event-%d", i)) {
			t.Fatalf("missing event %d in stream:\n%s", i, joined)
		}
	}
}

func TestPortForwarding_TenantScoping(t *testing.T) {
	env := newMultiTenantServer(t)

	mtSignup(t, env.ts, "pf_alice@test.local", "pfalice", "password123!")
	tokenA, userAID := mtLogin(t, env.ts, "pfalice", "password123!")
	mtSignup(t, env.ts, "pf_bob@test.local", "pfbob", "password123!")
	tokenB, userBID := mtLogin(t, env.ts, "pfbob", "password123!")

	vmA := newRunningVM(t, env, "alice-pf-vm", "alice-pf-vm", userAID)
	vmB := newRunningVM(t, env, "bob-pf-vm", "bob-pf-vm", userBID)

	// 1. Tenant A publishes a port; it gets its own origin, not a host port.
	res1, body1 := mtRequest(t, env.ts, http.MethodPost, "/api/v1/network/port-forwards", tokenA, map[string]interface{}{
		"vm_id": vmA.ID, "guest_port": 8080, "protocol": "tcp", "public": true,
	})
	if res1.StatusCode != http.StatusCreated {
		t.Fatalf("tenant A expose: %d %s", res1.StatusCode, body1)
	}
	var created db.PortForward
	_ = json.Unmarshal(body1, &created)
	if created.HostPort != 0 || !created.Public || created.OwnerID != userAID {
		t.Fatalf("created exposure fields incorrect: %+v", created)
	}
	if created.Subdomain == "" || !strings.HasPrefix(created.PublicURL, "http://"+created.Subdomain+".localhost:") {
		t.Fatalf("created exposure has no origin: %+v", created)
	}

	// 2. Tenant B can publish the same guest port on a different sandbox; the
	//    origins are distinct and do not collide.
	res2, body2 := mtRequest(t, env.ts, http.MethodPost, "/api/v1/network/port-forwards", tokenB, map[string]interface{}{
		"vm_id": vmB.ID, "guest_port": 8080, "protocol": "tcp",
	})
	if res2.StatusCode != http.StatusCreated {
		t.Fatalf("tenant B expose: %d %s", res2.StatusCode, body2)
	}
	var createdB db.PortForward
	_ = json.Unmarshal(body2, &createdB)
	if createdB.Subdomain == "" || createdB.Subdomain == created.Subdomain {
		t.Fatalf("exposures must get distinct slugs: %q vs %q", created.Subdomain, createdB.Subdomain)
	}

	// 3. Cross-tenant reads and deletes are denied.
	resGetB, _ := mtRequest(t, env.ts, http.MethodGet, "/api/v1/network/port-forwards/"+created.ID, tokenB, nil)
	if resGetB.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant GET = %d, want 404", resGetB.StatusCode)
	}
	resDelB, _ := mtRequest(t, env.ts, http.MethodDelete, "/api/v1/network/port-forwards/"+created.ID, tokenB, nil)
	if resDelB.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant DELETE = %d, want 404", resDelB.StatusCode)
	}

	// 4. The owner can read and delete it.
	resGetA, bodyGetA := mtRequest(t, env.ts, http.MethodGet, "/api/v1/network/port-forwards/"+created.ID, tokenA, nil)
	if resGetA.StatusCode != http.StatusOK {
		t.Fatalf("owner GET = %d: %s", resGetA.StatusCode, bodyGetA)
	}
	resDelA, _ := mtRequest(t, env.ts, http.MethodDelete, "/api/v1/network/port-forwards/"+created.ID, tokenA, nil)
	if resDelA.StatusCode != http.StatusOK {
		t.Fatalf("owner DELETE = %d", resDelA.StatusCode)
	}
}

func TestPortForwarding_ConcurrentExposeIsIdempotent(t *testing.T) {
	env := newMultiTenantServer(t)

	mtSignup(t, env.ts, "pf_race1@test.local", "pfrace1", "password123!")
	token1, user1ID := mtLogin(t, env.ts, "pfrace1", "password123!")
	vm1 := newRunningVM(t, env, "race-vm-1", "race-vm-1", user1ID)

	const workers = 8
	var wg sync.WaitGroup
	statuses := make(chan int, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, _ := mtRequest(t, env.ts, http.MethodPost, "/api/v1/network/port-forwards", token1, map[string]interface{}{
				"vm_id": vm1.ID, "guest_port": 8080, "protocol": "tcp", "public": true,
			})
			statuses <- res.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	for code := range statuses {
		if code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("concurrent expose status = %d", code)
		}
	}

	pfs, err := env.vmStore.ListPortForwardsForVM(context.Background(), user1ID, vm1.ID, vm1.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(pfs) != 1 {
		t.Fatalf("expected exactly one exposure after concurrent requests, got %d", len(pfs))
	}
	if pfs[0].Subdomain == "" || pfs[0].PublicURL == "" {
		t.Fatalf("exposure lost its origin: %+v", pfs[0])
	}
}
