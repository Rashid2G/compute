package gui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

//go:embed assets
var assetsFS embed.FS

// NewHandler creates an http.Handler that serves the embedded web GUI assets
// and reverse-proxies /api/ requests to the running MicroForge daemon.
func NewHandler(daemonURL string) (http.Handler, error) {
	if daemonURL == "" {
		daemonURL = "http://127.0.0.1:18080"
	}
	if !strings.HasPrefix(daemonURL, "http://") && !strings.HasPrefix(daemonURL, "https://") {
		daemonURL = "http://" + daemonURL
	}

	targetURL, err := url.Parse(daemonURL)
	if err != nil {
		return nil, fmt.Errorf("invalid daemon URL %q: %w", daemonURL, err)
	}

	subFS, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return nil, fmt.Errorf("failed to extract embedded assets filesystem: %w", err)
	}

	fileServer := http.FileServer(http.FS(subFS))
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Custom error handler for proxy
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, proxyErr error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("daemon reverse proxy error: %v", proxyErr),
		})
	}

	mux := http.NewServeMux()

	// 1. API Reverse Proxy
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		r.Host = targetURL.Host
		proxy.ServeHTTP(w, r)
	})

	// 2. GUI Info Endpoint
	mux.HandleFunc("/api/gui/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"daemon_url": daemonURL,
			"version":    "1.0",
		})
	})

	// 3. Static File Server & SPA Fallback
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" || path == "index.html" {
			indexContent, readErr := fs.ReadFile(subFS, "index.html")
			if readErr != nil {
				http.Error(w, "index.html not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(indexContent)
			return
		}

		// Check if file exists in embedded assets
		if f, openErr := subFS.Open(path); openErr == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}

		// Fallback to index.html for SPA routes
		indexContent, readErr := fs.ReadFile(subFS, "index.html")
		if readErr == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(indexContent)
			return
		}
		http.NotFound(w, r)
	})

	return mux, nil
}

// GetAssetsFS returns the embedded filesystem
func GetAssetsFS() (fs.FS, error) {
	return fs.Sub(assetsFS, "assets")
}

// StartServer launches the HTTP GUI server and blocks until ctx is canceled
func StartServer(ctx context.Context, listenAddr, daemonURL string) error {
	handler, err := NewHandler(daemonURL)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("failed to bind GUI address %s: %w", listenAddr, err)
	}
	defer listener.Close()

	actualAddr := listener.Addr().String()

	fmt.Println("==================================================")
	fmt.Println("          MICROFORGE WEB GUI SERVER               ")
	fmt.Println("==================================================")
	fmt.Printf("Daemon Target: %s\n", daemonURL)
	fmt.Printf("GUI Server:    http://%s\n", actualAddr)
	if strings.HasPrefix(listenAddr, "0.0.0.0:") {
		port := strings.TrimPrefix(listenAddr, "0.0.0.0:")
		fmt.Printf("Local Access:  http://localhost:%s\n", port)
	}
	fmt.Println("Press Ctrl+C to terminate.")
	fmt.Println("==================================================")

	srv := &http.Server{
		Handler: handler,
	}

	errChan := make(chan error, 1)
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Println("[mforge-gui] Shutting down GUI server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		log.Println("[mforge-gui] GUI server stopped.")
		return nil
	case err := <-errChan:
		return err
	}
}

// Helper to check if daemon is reachable
func CheckDaemonReachable(daemonURL string, timeout time.Duration) bool {
	if !strings.HasPrefix(daemonURL, "http://") && !strings.HasPrefix(daemonURL, "https://") {
		daemonURL = "http://" + daemonURL
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(daemonURL + "/api/v1/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
