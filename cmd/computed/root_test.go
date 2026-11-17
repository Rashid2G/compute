package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestRootServesDashboardGUI verifies that the daemon root serves the web GUI
// instead of a deployment listing page.
func TestRootServesDashboardGUI(t *testing.T) {
	env := newMultiTenantServer(t)

	guiMux := http.NewServeMux()
	guiMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("PR_WEB_UI"))
	})
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
	if resRoot.StatusCode != http.StatusOK || string(bodyRoot) != "PR_WEB_UI" {
		t.Fatalf("expected GUI dashboard at /, got %d: %s", resRoot.StatusCode, string(bodyRoot))
	}
	if strings.Contains(string(bodyRoot), "Deployments") {
		t.Fatalf("root must not serve the deployments listing page, got: %s", string(bodyRoot))
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

// TestRootWithoutGUIHandlerReturnsNotFound verifies that an unconfigured GUI
// does not fall back to a deployment listing at the daemon root.
func TestRootWithoutGUIHandlerReturnsNotFound(t *testing.T) {
	env := newMultiTenantServer(t)

	for _, path := range []string{"/", "/deployments"} {
		res, err := http.Get(env.ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404 at %s without a GUI handler, got %d: %s", path, res.StatusCode, string(body))
		}
	}
}
