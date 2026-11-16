package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/coder/websocket"
)

func TestIngressProxy_PathRouting(t *testing.T) {
	env := newMultiTenantServer(t)

	// Spin up a mock guest service simulating an app running inside a microVM
	guestListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer guestListener.Close()

	guestPort := guestListener.Addr().(*net.TCPAddr).Port
	guestMux := http.NewServeMux()
	guestMux.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"hello from microvm","path":"` + r.URL.Path + `"}`))
	})
	guestMux.HandleFunc("/preview/subpath", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok-subpath:" + r.URL.Query().Get("q")))
	})
	guestServer := &http.Server{Handler: guestMux}
	go guestServer.Serve(guestListener)
	defer guestServer.Close()

	// Create user and running VM
	mtSignup(t, env.ts, "owner_a@test.local", "ownera", "password123!")
	tokenA, userAID := mtLogin(t, env.ts, "ownera", "password123!")
	vmA := &db.VM{
		ID:        "vm-alpha",
		Name:      "vm-alpha",
		OwnerID:   userAID,
		Status:    db.StatusRunning,
		IPAddress: "127.0.0.1",
	}
	if err := env.vmStore.SaveVM(context.Background(), vmA); err != nil {
		t.Fatal(err)
	}

	// 1. Path-based: /preview/<target>/<port>/api/hello
	req1, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/preview/%s/%d/api/hello", env.ts.URL, vmA.Name, guestPort), nil)
	req1.Header.Set("Authorization", "Bearer "+tokenA)
	res1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(res1.Body)
	res1.Body.Close()
	if res1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /preview path routing, got %d: %s", res1.StatusCode, string(body1))
	}
	if !strings.Contains(string(body1), "hello from microvm") {
		t.Fatalf("expected response from guest service, got: %s", string(body1))
	}

	// 2. Path-based: /api/v1/sandboxes/<target>/preview/<port>/preview/subpath?q=fast
	req2, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v1/sandboxes/%s/preview/%d/preview/subpath?q=fast", env.ts.URL, vmA.Name, guestPort), nil)
	req2.Header.Set("Authorization", "Bearer "+tokenA)
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(res2.Body)
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/v1/sandboxes preview path routing, got %d: %s", res2.StatusCode, string(body2))
	}
	if string(body2) != "ok-subpath:fast" {
		t.Fatalf("expected 'ok-subpath:fast', got: %s", string(body2))
	}

	// 3. Path-based: /api/v1/vms/<target>/preview/<port>/api/hello
	req3, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v1/vms/%s/preview/%d/api/hello", env.ts.URL, vmA.Name, guestPort), nil)
	req3.Header.Set("Authorization", "Bearer "+tokenA)
	res3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	body3, _ := io.ReadAll(res3.Body)
	res3.Body.Close()
	if res3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/v1/vms preview path routing, got %d: %s", res3.StatusCode, string(body3))
	}
	if !strings.Contains(string(body3), "hello from microvm") {
		t.Fatalf("expected response from guest service, got: %s", string(body3))
	}
}

func TestIngressProxy_PreviewHTMLSurvivesCompressedUpstream(t *testing.T) {
	env := newMultiTenantServer(t)

	guestListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer guestListener.Close()
	guestPort := guestListener.Addr().(*net.TCPAddr).Port

	const page = `<!doctype html><html><head><script src="/app.js"></script></head><body>compressed app</body></html>`
	guestMux := http.NewServeMux()
	guestMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Compress whenever the client negotiated it, and force gzip on
		// ?force=gzip to emulate an upstream that ignores identity.
		if r.URL.Query().Get("force") == "gzip" || strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			_, _ = zw.Write([]byte(page))
			_ = zw.Close()
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
			_, _ = w.Write(buf.Bytes())
			return
		}
		_, _ = w.Write([]byte(page))
	})
	guestServer := &http.Server{Handler: guestMux}
	go guestServer.Serve(guestListener)
	defer guestServer.Close()

	mtSignup(t, env.ts, "preview@test.local", "previewer", "password123!")
	token, userID := mtLogin(t, env.ts, "previewer", "password123!")
	vm := &db.VM{
		ID:        "preview-app",
		Name:      "preview-app",
		OwnerID:   userID,
		Status:    db.StatusRunning,
		IPAddress: "127.0.0.1",
	}
	if err := env.vmStore.SaveVM(context.Background(), vm); err != nil {
		t.Fatal(err)
	}

	fetch := func(url string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		// Browser-like negotiation: the proxy must not hand back compressed
		// bytes as unencoded HTML.
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return res, body
	}

	assertRewrittenHTML := func(name string, res *http.Response, body []byte) {
		t.Helper()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", name, res.StatusCode, body)
		}
		if enc := res.Header.Get("Content-Encoding"); enc != "" {
			t.Errorf("%s: Content-Encoding = %q, want none", name, enc)
		}
		if !bytes.HasPrefix(body, []byte("<!doctype html>")) {
			t.Fatalf("%s: body is not HTML (first bytes %x)", name, body[:min(len(body), 16)])
		}
		if !strings.Contains(string(body), `<base href="/preview/preview-app/`+strconv.Itoa(guestPort)+`/">`) {
			t.Errorf("%s: missing injected base href:\n%s", name, body)
		}
		if !strings.Contains(string(body), `src="/preview/preview-app/`+strconv.Itoa(guestPort)+`/app.js"`) {
			t.Errorf("%s: asset path not rewritten:\n%s", name, body)
		}
		if got, want := res.Header.Get("Content-Length"), strconv.Itoa(len(body)); got != want {
			t.Errorf("%s: Content-Length = %q, want %q", name, got, want)
		}
	}

	base := fmt.Sprintf("%s/preview/%s/%d/", env.ts.URL, vm.Name, guestPort)
	res, body := fetch(base)
	assertRewrittenHTML("identity upstream", res, body)

	// Upstream compresses regardless of the negotiated identity encoding; the
	// proxy must decode before rewriting.
	res, body = fetch(base + "?force=gzip")
	assertRewrittenHTML("gzip upstream", res, body)
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

func TestIngressProxy_PublicVsPrivateAuth(t *testing.T) {
	env := newMultiTenantServer(t)

	guestListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer guestListener.Close()
	guestPort := guestListener.Addr().(*net.TCPAddr).Port

	guestMux := http.NewServeMux()
	guestMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("secret guest app"))
	})
	guestServer := &http.Server{Handler: guestMux}
	go guestServer.Serve(guestListener)
	defer guestServer.Close()

	mtSignup(t, env.ts, "alice@test.local", "alice", "password123!")
	tokenA, userAID := mtLogin(t, env.ts, "alice", "password123!")
	mtSignup(t, env.ts, "bob@test.local", "bob", "password123!")
	tokenB, _ := mtLogin(t, env.ts, "bob", "password123!")

	vmA := &db.VM{
		ID:        "alice-app",
		Name:      "alice-app",
		OwnerID:   userAID,
		Status:    db.StatusRunning,
		IPAddress: "127.0.0.1",
	}
	if err := env.vmStore.SaveVM(context.Background(), vmA); err != nil {
		t.Fatal(err)
	}

	previewURL := fmt.Sprintf("%s/preview/%s/%d/", env.ts.URL, vmA.Name, guestPort)

	// 1. Unauthenticated request to private VM -> 401 Unauthorized
	reqUnauth, _ := http.NewRequest(http.MethodGet, previewURL, nil)
	resUnauth, err := http.DefaultClient.Do(reqUnauth)
	if err != nil {
		t.Fatal(err)
	}
	resUnauth.Body.Close()
	if resUnauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for unauthenticated access to private preview, got %d", resUnauth.StatusCode)
	}

	// 2. Unauthorized caller (Bob accessing Alice's private preview) -> 401 Unauthorized
	reqBob, _ := http.NewRequest(http.MethodGet, previewURL, nil)
	reqBob.Header.Set("Authorization", "Bearer "+tokenB)
	resBob, err := http.DefaultClient.Do(reqBob)
	if err != nil {
		t.Fatal(err)
	}
	resBob.Body.Close()
	if resBob.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for non-owner access to private preview, got %d", resBob.StatusCode)
	}

	// 3. Authorized caller (Alice accessing Alice's private preview) -> 200 OK
	reqAlice, _ := http.NewRequest(http.MethodGet, previewURL, nil)
	reqAlice.Header.Set("Authorization", "Bearer "+tokenA)
	resAlice, err := http.DefaultClient.Do(reqAlice)
	if err != nil {
		t.Fatal(err)
	}
	bodyAlice, _ := io.ReadAll(resAlice.Body)
	resAlice.Body.Close()
	if resAlice.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for owner access, got %d", resAlice.StatusCode)
	}
	if string(bodyAlice) != "secret guest app" {
		t.Fatalf("unexpected content: %s", string(bodyAlice))
	}

	// 4. Expose as Public via PortForward record
	publicPF := &db.PortForward{
		ID:        "pf_public_test",
		OwnerID:   userAID,
		HostPort:  19080,
		VMID:      vmA.ID,
		VMName:    vmA.Name,
		GuestPort: guestPort,
		Protocol:  "tcp",
		Public:    true,
		Status:    "active",
	}
	if err := env.vmStore.SavePortForward(context.Background(), publicPF); err != nil {
		t.Fatal(err)
	}

	// Now unauthenticated request to the public port must succeed with 200 OK!
	reqPublic, _ := http.NewRequest(http.MethodGet, previewURL, nil)
	resPublic, err := http.DefaultClient.Do(reqPublic)
	if err != nil {
		t.Fatal(err)
	}
	bodyPublic, _ := io.ReadAll(resPublic.Body)
	resPublic.Body.Close()
	if resPublic.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for public preview without auth, got %d: %s", resPublic.StatusCode, string(bodyPublic))
	}
	if string(bodyPublic) != "secret guest app" {
		t.Fatalf("unexpected body from public preview: %s", string(bodyPublic))
	}

	// 5. Untrusted client attempts authentication bypass via ?public=true or header X-Public-Preview: true on private preview
	privatePort := guestPort + 100 // port not registered as public
	privateURL := fmt.Sprintf("%s/preview/%s/%d/", env.ts.URL, vmA.Name, privatePort)

	reqQueryBypass, _ := http.NewRequest(http.MethodGet, privateURL+"?public=true", nil)
	resQueryBypass, err := http.DefaultClient.Do(reqQueryBypass)
	if err != nil {
		t.Fatal(err)
	}
	resQueryBypass.Body.Close()
	if resQueryBypass.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized when attempting auth bypass with ?public=true, got %d", resQueryBypass.StatusCode)
	}

	reqHeaderBypass, _ := http.NewRequest(http.MethodGet, privateURL, nil)
	reqHeaderBypass.Header.Set("X-Public-Preview", "true")
	resHeaderBypass, err := http.DefaultClient.Do(reqHeaderBypass)
	if err != nil {
		t.Fatal(err)
	}
	resHeaderBypass.Body.Close()
	if resHeaderBypass.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized when attempting auth bypass with X-Public-Preview header, got %d", resHeaderBypass.StatusCode)
	}
}

func TestIngressProxy_OwnerScopedPathIsolation(t *testing.T) {
	env := newMultiTenantServer(t)

	guestListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer guestListener.Close()
	guestPort := guestListener.Addr().(*net.TCPAddr).Port

	guestMux := http.NewServeMux()
	guestMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("tenant app"))
	})
	guestServer := &http.Server{Handler: guestMux}
	go guestServer.Serve(guestListener)
	defer guestServer.Close()

	mtSignup(t, env.ts, "carol@test.local", "carol", "password123!")
	_, carolID := mtLogin(t, env.ts, "carol", "password123!")
	mtSignup(t, env.ts, "dave@test.local", "dave", "password123!")
	_, daveID := mtLogin(t, env.ts, "dave", "password123!")

	// Both tenants use the same sandbox name. Only Carol's is public.
	carolVM := &db.VM{ID: "vm-carol-web", Name: "web", OwnerID: carolID, Status: db.StatusRunning, IPAddress: "127.0.0.1"}
	daveVM := &db.VM{ID: "vm-dave-web", Name: "web", OwnerID: daveID, Status: db.StatusRunning, IPAddress: "127.0.0.1"}
	for _, v := range []*db.VM{carolVM, daveVM} {
		if err := env.vmStore.SaveVM(context.Background(), v); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.vmStore.SavePortForward(context.Background(), &db.PortForward{
		ID:        "pf_carol_public",
		OwnerID:   carolID,
		VMID:      carolVM.ID,
		VMName:    carolVM.Name,
		GuestPort: guestPort,
		Protocol:  "tcp",
		Public:    true,
		Status:    "active",
	}); err != nil {
		t.Fatal(err)
	}

	carolURL := fmt.Sprintf("%s/preview/@carol/web/%d/", env.ts.URL, guestPort)
	daveURL := fmt.Sprintf("%s/preview/@dave/web/%d/", env.ts.URL, guestPort)

	// Public exposure applies only to Carol's sandbox.
	resCarols, err := http.Get(carolURL)
	if err != nil {
		t.Fatal(err)
	}
	resCarols.Body.Close()
	if resCarols.StatusCode != http.StatusOK {
		t.Fatalf("carol's public owner-scoped preview: got %d, want 200", resCarols.StatusCode)
	}

	// Dave's same-named sandbox stays private despite Carol's public rule.
	resDaves, err := http.Get(daveURL)
	if err != nil {
		t.Fatal(err)
	}
	resDaves.Body.Close()
	if resDaves.StatusCode != http.StatusUnauthorized {
		t.Fatalf("dave's private owner-scoped preview: got %d, want 401", resDaves.StatusCode)
	}
}

func TestIngressProxy_WebSocketUpgrade(t *testing.T) {
	env := newMultiTenantServer(t)

	// Upstream server handling WebSockets
	guestListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer guestListener.Close()
	guestPort := guestListener.Addr().(*net.TCPAddr).Port

	guestMux := http.NewServeMux()
	guestMux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
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
			reply := "echo: " + string(msg)
			if err := conn.Write(ctx, typ, []byte(reply)); err != nil {
				return
			}
		}
	})
	guestServer := &http.Server{Handler: guestMux}
	go guestServer.Serve(guestListener)
	defer guestServer.Close()

	mtSignup(t, env.ts, "ws_owner@test.local", "wsowner", "password123!")
	tokenA, userAID := mtLogin(t, env.ts, "wsowner", "password123!")
	vmA := &db.VM{
		ID:        "ws-sandbox",
		Name:      "ws-sandbox",
		OwnerID:   userAID,
		Status:    db.StatusRunning,
		IPAddress: "127.0.0.1",
	}
	if err := env.vmStore.SaveVM(context.Background(), vmA); err != nil {
		t.Fatal(err)
	}

	// Connect to WebSocket endpoint through preview reverse proxy
	wsURL := fmt.Sprintf("ws://%s/preview/%s/%d/ws", env.ts.Listener.Addr().String(), vmA.Name, guestPort)
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()

	wsConn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + tokenA},
		},
	})
	if err != nil {
		t.Fatalf("failed to dial websocket through preview proxy: %v", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "")

	// Send message
	if err := wsConn.Write(dialCtx, websocket.MessageText, []byte("ping-reverse-proxy")); err != nil {
		t.Fatalf("failed to write websocket message: %v", err)
	}

	// Read response
	_, replyBytes, err := wsConn.Read(dialCtx)
	if err != nil {
		t.Fatalf("failed to read websocket response: %v", err)
	}
	if string(replyBytes) != "echo: ping-reverse-proxy" {
		t.Fatalf("expected 'echo: ping-reverse-proxy', got: %s", string(replyBytes))
	}
}

func TestIngressProxy_ServerSentEventsStreaming(t *testing.T) {
	env := newMultiTenantServer(t)

	guestListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer guestListener.Close()
	guestPort := guestListener.Addr().(*net.TCPAddr).Port

	guestMux := http.NewServeMux()
	guestMux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		for i := 1; i <= 3; i++ {
			_, _ = fmt.Fprintf(w, "data: event-%d\n\n", i)
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	})
	guestServer := &http.Server{Handler: guestMux}
	go guestServer.Serve(guestListener)
	defer guestServer.Close()

	mtSignup(t, env.ts, "sse_owner@test.local", "sseowner", "password123!")
	tokenA, userAID := mtLogin(t, env.ts, "sseowner", "password123!")
	vmA := &db.VM{
		ID:        "sse-sandbox",
		Name:      "sse-sandbox",
		OwnerID:   userAID,
		Status:    db.StatusRunning,
		IPAddress: "127.0.0.1",
	}
	if err := env.vmStore.SaveVM(context.Background(), vmA); err != nil {
		t.Fatal(err)
	}

	sseURL := fmt.Sprintf("%s/preview/%s/%d/sse", env.ts.URL, vmA.Name, guestPort)
	req, _ := http.NewRequest(http.MethodGet, sseURL, nil)
	req.Header.Set("Authorization", "Bearer "+tokenA)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from SSE endpoint, got %d", res.StatusCode)
	}

	reader := bufio.NewReader(res.Body)
	var events []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			events = append(events, line)
		}
		if len(events) == 3 {
			break
		}
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 streamed SSE events, got %d (%v)", len(events), events)
	}
	if events[0] != "data: event-1" || events[1] != "data: event-2" || events[2] != "data: event-3" {
		t.Fatalf("unexpected events received: %v", events)
	}
}

func TestPortForwarding_ScopingAndConflictDetection(t *testing.T) {
	env := newMultiTenantServer(t)

	mtSignup(t, env.ts, "pf_alice@test.local", "pfalice", "password123!")
	tokenA, userAID := mtLogin(t, env.ts, "pfalice", "password123!")
	mtSignup(t, env.ts, "pf_bob@test.local", "pfbob", "password123!")
	tokenB, userBID := mtLogin(t, env.ts, "pfbob", "password123!")

	vmA := &db.VM{
		ID:        "alice-pf-vm",
		Name:      "alice-pf-vm",
		OwnerID:   userAID,
		Status:    db.StatusRunning,
		IPAddress: "192.168.50.20",
	}
	_ = env.vmStore.SaveVM(context.Background(), vmA)

	vmB := &db.VM{
		ID:        "bob-pf-vm",
		Name:      "bob-pf-vm",
		OwnerID:   userBID,
		Status:    db.StatusRunning,
		IPAddress: "192.168.50.21",
	}
	_ = env.vmStore.SaveVM(context.Background(), vmB)

	// Pick an unallocated high port
	testPort := 28492

	// 1. Tenant A creates port forward on testPort -> succeeds (201 Created)
	pfPayloadA := map[string]interface{}{
		"host_port":  testPort,
		"vm_id":      vmA.ID,
		"guest_port": 8080,
		"protocol":   "tcp",
		"public":     true,
	}
	res1, body1 := mtRequest(t, env.ts, http.MethodPost, "/api/v1/network/port-forwards", tokenA, pfPayloadA)
	if res1.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created for Tenant A port forward, got %d: %s", res1.StatusCode, string(body1))
	}
	var createdPF db.PortForward
	_ = json.Unmarshal(body1, &createdPF)
	if createdPF.HostPort != testPort || !createdPF.Public || createdPF.OwnerID != userAID {
		t.Fatalf("created port forward fields incorrect: %+v", createdPF)
	}

	// 2. Tenant B attempts to create port forward on the SAME host port -> 409 Conflict
	pfPayloadB := map[string]interface{}{
		"host_port":  testPort,
		"vm_id":      vmB.ID,
		"guest_port": 9090,
		"protocol":   "tcp",
	}
	res2, body2 := mtRequest(t, env.ts, http.MethodPost, "/api/v1/network/port-forwards", tokenB, pfPayloadB)
	if res2.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for duplicate host port allocation, got %d: %s", res2.StatusCode, string(body2))
	}

	// 3. Non-admin attempts to bind privileged port (< 1024) -> 403 Forbidden
	privPayload := map[string]interface{}{
		"host_port":  80,
		"vm_id":      vmA.ID,
		"guest_port": 80,
	}
	resPriv, _ := mtRequest(t, env.ts, http.MethodPost, "/api/v1/network/port-forwards", tokenA, privPayload)
	if resPriv.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for privileged port allocation by non-admin, got %d", resPriv.StatusCode)
	}

	// 4. Tenant B tries to GET Tenant A's port forward -> 404 Not Found
	resGetB, _ := mtRequest(t, env.ts, http.MethodGet, "/api/v1/network/port-forwards/"+createdPF.ID, tokenB, nil)
	if resGetB.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found when Tenant B accesses Tenant A's port forward, got %d", resGetB.StatusCode)
	}

	// 5. Tenant B tries to DELETE Tenant A's port forward -> 404 Not Found
	resDelB, _ := mtRequest(t, env.ts, http.MethodDelete, "/api/v1/network/port-forwards/"+createdPF.ID, tokenB, nil)
	if resDelB.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found when Tenant B deletes Tenant A's port forward, got %d", resDelB.StatusCode)
	}

	// 6. Tenant A views their port forward -> 200 OK
	resGetA, bodyGetA := mtRequest(t, env.ts, http.MethodGet, "/api/v1/network/port-forwards/"+createdPF.ID, tokenA, nil)
	if resGetA.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK when Tenant A views their port forward, got %d: %s", resGetA.StatusCode, string(bodyGetA))
	}

	// 7. Tenant A deletes their port forward -> 200 OK
	resDelA, _ := mtRequest(t, env.ts, http.MethodDelete, "/api/v1/network/port-forwards/"+createdPF.ID, tokenA, nil)
	if resDelA.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK when Tenant A deletes their port forward, got %d", resDelA.StatusCode)
	}

	// 8. Now Tenant B can allocate that host port since it was released
	res3, body3 := mtRequest(t, env.ts, http.MethodPost, "/api/v1/network/port-forwards", tokenB, pfPayloadB)
	if res3.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created after previous port forward was deleted, got %d: %s", res3.StatusCode, string(body3))
	}
}

func TestPortForwarding_ConcurrentRaceReturnsConflict(t *testing.T) {
	env := newMultiTenantServer(t)

	mtSignup(t, env.ts, "pf_race1@test.local", "pfrace1", "password123!")
	token1, user1ID := mtLogin(t, env.ts, "pfrace1", "password123!")
	mtSignup(t, env.ts, "pf_race2@test.local", "pfrace2", "password123!")
	token2, user2ID := mtLogin(t, env.ts, "pfrace2", "password123!")

	vm1 := &db.VM{
		ID:        "race-vm-1",
		Name:      "race-vm-1",
		OwnerID:   user1ID,
		Status:    db.StatusRunning,
		IPAddress: "192.168.50.31",
	}
	_ = env.vmStore.SaveVM(context.Background(), vm1)

	vm2 := &db.VM{
		ID:        "race-vm-2",
		Name:      "race-vm-2",
		OwnerID:   user2ID,
		Status:    db.StatusRunning,
		IPAddress: "192.168.50.32",
	}
	_ = env.vmStore.SaveVM(context.Background(), vm2)

	racePort := 28555
	numAttempts := 8
	var wg sync.WaitGroup
	statusCodeChan := make(chan int, numAttempts)

	for i := 0; i < numAttempts; i++ {
		wg.Add(1)
		tok := token1
		vmid := vm1.ID
		if i%2 == 1 {
			tok = token2
			vmid = vm2.ID
		}
		go func(token, vmID string) {
			defer wg.Done()
			payload := map[string]interface{}{
				"host_port":  racePort,
				"vm_id":      vmID,
				"guest_port": 8080,
				"protocol":   "tcp",
			}
			res, _ := mtRequest(t, env.ts, http.MethodPost, "/api/v1/network/port-forwards", token, payload)
			statusCodeChan <- res.StatusCode
		}(tok, vmid)
	}

	wg.Wait()
	close(statusCodeChan)

	var createdCount int
	var conflictCount int
	for code := range statusCodeChan {
		if code == http.StatusCreated {
			createdCount++
		} else if code == http.StatusConflict {
			conflictCount++
		} else {
			t.Errorf("unexpected status code during concurrent race: %d", code)
		}
	}

	if createdCount != 1 {
		t.Fatalf("expected exactly 1 successful creation (201 Created), got %d", createdCount)
	}
	if conflictCount != numAttempts-1 {
		t.Fatalf("expected %d conflicts (409 Conflict), got %d", numAttempts-1, conflictCount)
	}
}
