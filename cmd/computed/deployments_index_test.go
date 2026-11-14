package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/Rashid2G/on-demand-vms/internal/db"
)

// TestIngressProxy_HTMLRewriteUnderPreviewPrefix verifies that HTML responses
// served under /preview/<target>/<port>/ are rewritten for the path prefix:
// absolute asset URLs are rewritten, a <base href> is injected, and the
// runtime patch script is added. JSON responses pass through untouched.
func TestIngressProxy_HTMLRewriteUnderPreviewPrefix(t *testing.T) {
	env := newMultiTenantServer(t)

	guestListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer guestListener.Close()
	guestPort := guestListener.Addr().(*net.TCPAddr).Port

	guestMux := http.NewServeMux()
	guestMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>App</title>` +
			`<script type="module" crossorigin src="/app.js"></script>` +
			`<link rel="stylesheet" crossorigin href="/app.css">` +
			`</head><body><div id="root"></div></body></html>`))
	})
	guestMux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	guestServer := &http.Server{Handler: guestMux}
	go func() { _ = guestServer.Serve(guestListener) }()
	defer guestServer.Close()

	mtSignup(t, env.ts, "owner_html@test.local", "ownerhtml", "password123!")
	token, userAID := mtLogin(t, env.ts, "ownerhtml", "password123!")
	vmA := &db.VM{
		ID:        "vm-html",
		Name:      "vm-html",
		OwnerID:   userAID,
		Status:    db.StatusRunning,
		IPAddress: "127.0.0.1",
	}
	if err := env.vmStore.SaveVM(context.Background(), vmA); err != nil {
		t.Fatal(err)
	}

	// HTML response must be rewritten.
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/preview/vm-html/%d/", env.ts.URL, guestPort), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	out := string(body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.StatusCode, out)
	}
	for _, want := range []string{
		fmt.Sprintf(`<base href="/preview/vm-html/%d/">`, guestPort),
		fmt.Sprintf(`src="/preview/vm-html/%d/app.js"`, guestPort),
		fmt.Sprintf(`href="/preview/vm-html/%d/app.css"`, guestPort),
		fmt.Sprintf(`P="/preview/vm-html/%d/"`, guestPort),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in rewritten HTML, got:\n%s", want, out)
		}
	}

	// Non-HTML (JSON) response must pass through unchanged.
	req2, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/preview/vm-html/%d/api/v1/health", env.ts.URL, guestPort), nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(res2.Body)
	res2.Body.Close()
	if string(body2) != `{"status":"ok"}` {
		t.Fatalf("JSON response should be untouched, got: %s", string(body2))
	}
}

// TestDeploymentsIndexHomepage verifies the landing page at the daemon root:
// it lists running sandboxes with preview links, links the main GUI, 404s
// unknown paths, and does not require authentication.
func TestDeploymentsIndexHomepage(t *testing.T) {
	env := newMultiTenantServer(t)

	mtSignup(t, env.ts, "owner_idx@test.local", "owneridx", "password123!")
	_, userAID := mtLogin(t, env.ts, "owneridx", "password123!")
	vmA := &db.VM{
		ID:        "vm-idx",
		Name:      "pr-77",
		OwnerID:   userAID,
		Status:    db.StatusRunning,
		IPAddress: "172.16.0.9",
	}
	if err := env.vmStore.SaveVM(context.Background(), vmA); err != nil {
		t.Fatal(err)
	}
	stopped := &db.VM{
		ID:        "vm-stopped",
		Name:      "pr-78",
		OwnerID:   userAID,
		Status:    db.StatusStopped,
		IPAddress: "172.16.0.10",
	}
	if err := env.vmStore.SaveVM(context.Background(), stopped); err != nil {
		t.Fatal(err)
	}

	// No auth header: the homepage is a public landing page.
	res, err := http.Get(env.ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	out := string(body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 at /, got %d", res.StatusCode)
	}
	if res.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("unexpected content type: %s", res.Header.Get("Content-Type"))
	}
	for _, want := range []string{
		"Deployments",
		`href="/preview/pr-77/8080/"`,
		"pr-78", // listed even when stopped (non-link)
		":8080/",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected homepage to contain %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, `href="/preview/pr-78/8080/"`) {
		t.Errorf("stopped sandbox must not be a preview link, got:\n%s", out)
	}

	// Unknown paths must still 404.
	res2, err := http.Get(env.ts.URL + "/definitely-not-here")
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown path, got %d", res2.StatusCode)
	}

	// Unknown API path must not be swallowed by the homepage handler.
	res3, err := http.Get(env.ts.URL + "/api/v1/definitely-not-here")
	if err != nil {
		t.Fatal(err)
	}
	res3.Body.Close()
	if res3.StatusCode == http.StatusOK {
		t.Errorf("unknown API path unexpectedly succeeded")
	}
}

func TestRenderDeploymentsPageEscapesNames(t *testing.T) {
	out := renderDeploymentsPage([]*db.VM{{
		Name:    `<script>alert(1)</script>`,
		OwnerID: `o<"w"ner`,
		Status:  db.StatusRunning,
	}}, "http://127.0.0.1:8080/")
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Errorf("sandbox name must be HTML-escaped, got:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected escaped name in output:\n%s", out)
	}
}

func TestSetGUIHandlerDoesNotConflictWithRoot(t *testing.T) {
	env := newMultiTenantServer(t)

	guiMux := http.NewServeMux()
	guiMux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("console.log('gui');"))
	})
	env.server.SetGUIHandler(guiMux)

	resRoot, err := http.Get(env.ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	bodyRoot, _ := io.ReadAll(resRoot.Body)
	resRoot.Body.Close()
	if resRoot.StatusCode != http.StatusOK || !strings.Contains(string(bodyRoot), "Deployments") {
		t.Fatalf("expected deployments homepage at /, got %d: %s", resRoot.StatusCode, string(bodyRoot))
	}

	resAsset, err := http.Get(env.ts.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	bodyAsset, _ := io.ReadAll(resAsset.Body)
	resAsset.Body.Close()
	if resAsset.StatusCode != http.StatusOK || string(bodyAsset) != "console.log('gui');" {
		t.Fatalf("expected guiHandler to serve /app.js, got %d: %s", resAsset.StatusCode, string(bodyAsset))
	}
}
