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

	// 4. /static/bootstrap.min.css should return bootstrap css
	respBS, err := http.Get(server.URL + "/static/bootstrap.min.css")
	if err != nil {
		t.Fatal(err)
	}
	defer respBS.Body.Close()
	if respBS.StatusCode != http.StatusOK {
		t.Fatalf("GET /static/bootstrap.min.css status = %d, want %d", respBS.StatusCode, http.StatusOK)
	}

	// 5. /api/gui/info should return JSON
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

	// Proxy request to /api/v1/health through GUI server
	resp, err := http.Get(guiServer.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxied /api/v1/health status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var data map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	if data["mock"] != "true" || data["status"] != "ok" {
		t.Fatalf("unexpected proxied data: %+v", data)
	}
}

func TestCheckDaemonReachable(t *testing.T) {
	mockDaemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer mockDaemon.Close()

	if !CheckDaemonReachable(mockDaemon.URL, 1*time.Second) {
		t.Fatal("expected mockDaemon to be reachable")
	}

	if CheckDaemonReachable("http://127.0.0.1:54321", 50*time.Millisecond) {
		t.Fatal("expected non-existent port to be unreachable")
	}
}
