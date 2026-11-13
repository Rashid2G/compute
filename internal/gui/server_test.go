package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGUIHandlerStaticAssets(t *testing.T) {
	handler, err := NewHandler("http://127.0.0.1:18080")
	if err != nil {
		t.Fatalf("NewHandler error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	// 1. Root / should return HTML
	resp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("GET / content-type = %s, want text/html", ct)
	}

	// 2. /app.js should return javascript
	respJS, err := http.Get(server.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer respJS.Body.Close()
	if respJS.StatusCode != http.StatusOK {
		t.Fatalf("GET /app.js status = %d, want %d", respJS.StatusCode, http.StatusOK)
	}

	// 3. /app.css should return css
	respCSS, err := http.Get(server.URL + "/app.css")
	if err != nil {
		t.Fatal(err)
	}
	defer respCSS.Body.Close()
	if respCSS.StatusCode != http.StatusOK {
		t.Fatalf("GET /app.css status = %d, want %d", respCSS.StatusCode, http.StatusOK)
	}

	// 4. SPA routing fallback for any nested frontend route
	respRoute, err := http.Get(server.URL + "/vms")
	if err != nil {
		t.Fatal(err)
	}
	defer respRoute.Body.Close()
	if respRoute.StatusCode != http.StatusOK {
		t.Fatalf("GET /vms status = %d, want %d", respRoute.StatusCode, http.StatusOK)
	}
	ctRoute := respRoute.Header.Get("Content-Type")
	if !strings.Contains(ctRoute, "text/html") {
		t.Fatalf("GET /vms content-type = %s, want text/html", ctRoute)
	}

	// 5. Embedded font / sub-assets should be served directly
	entries, err := assetsFS.ReadDir("assets/assets")
	if err == nil && len(entries) > 0 {
		fontName := entries[0].Name()
		respFont, err := http.Get(server.URL + "/assets/" + fontName)
		if err != nil {
			t.Fatal(err)
		}
		defer respFont.Body.Close()
		if respFont.StatusCode != http.StatusOK {
			t.Fatalf("GET /assets/%s status = %d, want %d", fontName, respFont.StatusCode, http.StatusOK)
		}
		ctFont := respFont.Header.Get("Content-Type")
		if strings.Contains(ctFont, "text/html") {
			t.Fatalf("GET /assets/%s served HTML fallback instead of binary asset", fontName)
		}
	}

	// 6. /api/gui/info should return JSON
	respInfo, err := http.Get(server.URL + "/api/gui/info")
	if err != nil {
		t.Fatal(err)
	}
	defer respInfo.Body.Close()
	if respInfo.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/gui/info status = %d, want %d", respInfo.StatusCode, http.StatusOK)
	}
}

func TestGUIHandlerProxyToDaemon(t *testing.T) {
	// Mock Daemon
	mockDaemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/health" {
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "mock": "true"})
			return
		}
		http.NotFound(w, r)
	}))
	defer mockDaemon.Close()

	// GUI Handler pointing to mockDaemon
	handler, err := NewHandler(mockDaemon.URL)
	if err != nil {
		t.Fatalf("NewHandler error = %v", err)
	}

	guiServer := httptest.NewServer(handler)
	defer guiServer.Close()

	resp, err := http.Get(guiServer.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxied request status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var data map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	if data["mock"] != "true" {
		t.Fatalf("unexpected proxy payload: %v", data)
	}
}

func TestCheckDaemonReachable(t *testing.T) {
	// 1. Unreachable
	if CheckDaemonReachable("http://127.0.0.1:54321", 50*time.Millisecond) {
		t.Fatal("expected 127.0.0.1:54321 to be unreachable")
	}

	// 2. Reachable
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	if !CheckDaemonReachable(ts.URL, 500*time.Millisecond) {
		t.Fatalf("expected test server %s to be reachable", ts.URL)
	}
}

func TestGUIHandlerProxyFallbackAuth(t *testing.T) {
	var capturedAuth string
	mockDaemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer mockDaemon.Close()

	t.Setenv("COMPUTE_ADMIN_SECRET", "test-secret-123")

	handler, err := NewHandler(mockDaemon.URL)
	if err != nil {
		t.Fatalf("NewHandler error = %v", err)
	}

	guiServer := httptest.NewServer(handler)
	defer guiServer.Close()

	// 1. Unauthenticated request receives fallback secret
	capturedAuth = ""
	resp1, err := http.Get(guiServer.URL + "/api/v1/vms")
	if err != nil {
		t.Fatal(err)
	}
	defer resp1.Body.Close()
	if capturedAuth != "Bearer test-secret-123" {
		t.Errorf("expected fallback auth header 'Bearer test-secret-123', got %q", capturedAuth)
	}

	// 2. Request with explicit Authorization header preserves caller's header
	capturedAuth = ""
	req, _ := http.NewRequest("GET", guiServer.URL+"/api/v1/vms", nil)
	req.Header.Set("Authorization", "Bearer user-token-456")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if capturedAuth != "Bearer user-token-456" {
		t.Errorf("expected caller auth header 'Bearer user-token-456', got %q", capturedAuth)
	}
}
