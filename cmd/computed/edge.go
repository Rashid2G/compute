package main

import (
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/Rashid2G/compute/internal/config"
)

// startEdge launches the optional HTTP edge listener (default :80 while the
// DNS responder is enabled). It is the network router for HTTP: it reads the
// Host header and decides where the request goes:
//
//   - the zone apex and the reserved "api"/"gui"/"www" subdomains are served
//     by the daemon itself (GUI + API),
//   - every other subdomain of the base domain goes to the sandbox ingress
//     wildcard ("<vm>[-<port>][.<username>]" routing).
//
// When the edge is disabled, the GUI/API/ingress keep working exactly as
// before through whatever serves the daemon (direct port, Tailscale serve, a
// reverse proxy, ...).
func startEdge(s *Server, cfg config.GlobalConfig) {
	addr := cfg.EdgeListenResolved()
	if addr == "" {
		return
	}
	if strings.HasPrefix(cfg.Server.ListenAddr, "unix://") {
		log.Printf("[computed] edge listener disabled: unix-socket daemons have no HTTP backend to route to")
		return
	}
	_, port, err := net.SplitHostPort(cfg.Server.ListenAddr)
	if err != nil {
		log.Printf("[computed] edge listener disabled: cannot parse listen address %q", cfg.Server.ListenAddr)
		return
	}
	backendScheme := "http"
	if strings.TrimSpace(cfg.Server.TLSCertFile) != "" {
		backendScheme = "https"
	}
	backendHost := "127.0.0.1"
	if lh := strings.TrimSpace(cfg.Server.ListenHost()); lh != "" && lh != "0.0.0.0" && lh != "::" {
		backendHost = lh
	}
	backend := &url.URL{Scheme: backendScheme, Host: net.JoinHostPort(backendHost, port)}

	edgeZones := cfg.DNSBaseDomains()
	if s.dns != nil && len(s.dns.zones) > 0 {
		edgeZones = s.dns.zones
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.TrimSuffix(host, ".")

		for _, zone := range edgeZones {
			if host == zone || isReservedHost(host, zone) {
				proxyLocal(w, r, backend)
				return
			}
		}
		if s.ingressProxy != nil && s.ingressProxy.ShouldHandle(r) {
			s.ingressProxy.ServeHTTP(w, r)
			return
		}
		// Bare-IP and unknown-host traffic falls through to the daemon.
		proxyLocal(w, r, backend)
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("[computed] edge listener on %s routing base domain %v (apex + reserved 'api' to the daemon, wildcard subdomains to sandboxes)", addr, edgeZones)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[computed] edge listener on %s stopped: %v (HTTP GUI/API keep working on %s)", addr, err, cfg.Server.ListenAddr)
		}
	}()
}

// isReservedHost reports whether host is "<reserved>.<zone>".
func isReservedHost(host, zone string) bool {
	suffix := "." + zone
	if !strings.HasSuffix(host, suffix) {
		return false
	}
	label := strings.TrimSuffix(host, suffix)
	switch strings.ToLower(label) {
	case "api", "gui", "www":
		return true
	}
	return false
}

func proxyLocal(w http.ResponseWriter, r *http.Request, backend *url.URL) {
	proxy := httputil.NewSingleHostReverseProxy(backend)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("[computed] edge: backend error: %v", err)
		http.Error(w, "daemon backend unavailable", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}
