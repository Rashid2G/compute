package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rashid2G/compute/internal/db"
)

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
	if err := env.vmStore.SavePortForward(context.Background(), &db.PortForward{
		ID: "pf_idx", OwnerID: userAID, VMID: vmA.ID, VMName: vmA.Name,
		GuestPort: 8080, Protocol: "tcp", Subdomain: "pr-77-8080",
		PublicURL: "https://pr-77-8080.serves.example.com/", Status: "active", Public: true,
	}); err != nil {
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
		`href="https://pr-77-8080.serves.example.com/"`,
		"pr-78", // listed even when stopped (non-link)
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
	}}, "http://127.0.0.1:8080/", nil, nil)
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

	resAPI, err := http.Get(env.ts.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	bodyAPI, _ := io.ReadAll(resAPI.Body)
	resAPI.Body.Close()
	if resAPI.StatusCode != http.StatusOK || !strings.Contains(string(bodyAPI), "ok") {
		t.Errorf("expected healthy API with mounted GUI, got %d: %s", resAPI.StatusCode, string(bodyAPI))
	}
}

func TestHandleRootPreviewModeServesGUI(t *testing.T) {
	t.Setenv("COMPUTE_DEPLOYMENTS_INDEX", "false")
	env := newMultiTenantServer(t)

	guiMux := http.NewServeMux()
	guiMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("PR_WEB_UI"))
	})
	env.server.SetGUIHandler(guiMux)

	resRoot, err := http.Get(env.ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	bodyRoot, _ := io.ReadAll(resRoot.Body)
	resRoot.Body.Close()
	if resRoot.StatusCode != http.StatusOK || string(bodyRoot) != "PR_WEB_UI" {
		t.Fatalf("expected GUI at / when COMPUTE_DEPLOYMENTS_INDEX=false, got %d: %s", resRoot.StatusCode, string(bodyRoot))
	}
}

// TestDeploymentsIndexListsDockerPreviews verifies that docker preview
// manifests (data/previews/*.json) are listed on the homepage with their
// direct URLs.
func TestDeploymentsIndexListsDockerPreviews(t *testing.T) {
	env := newMultiTenantServer(t)

	manifest := `{"name":"pr-99","url":"http://192.0.2.70:19037/","port":19037,"branch":"feat/x","commit":"abc1234","created":"2026-11-14T00:00:00Z"}`
	tmpRoot := t.TempDir()
	env.server.cfg.Server.DBPath = filepath.Join(tmpRoot, "db", "compute.db")
	previewsDir := previewsDirFor(env.server.cfg.Server.DBPath)
	if err := os.MkdirAll(previewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previewsDir, "pr-99.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	// Malformed manifests must be ignored gracefully.
	if err := os.WriteFile(filepath.Join(previewsDir, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := http.Get(env.ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	out := string(body)
	for _, want := range []string{
		`href="http://192.0.2.70:19037/"`,
		"pr-99",
		"feat/x @ abc1234",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected homepage to contain %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "broken") {
		t.Errorf("malformed manifest must not appear, got:\n%s", out)
	}
}
