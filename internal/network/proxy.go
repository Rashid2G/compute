package network

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TargetInfo holds resolution details for a microVM needed by the reverse proxy.
type TargetInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	OwnerID   string `json:"owner_id"`
	Status    string `json:"status"`
	IPAddress string `json:"ip_address"`
}

// TargetResolver resolves a VM ID or name to its target information and port exposure.
type TargetResolver interface {
	ResolveTarget(ctx context.Context, idOrName string) (*TargetInfo, error)
	IsPortPublic(ctx context.Context, idOrName string, port int) bool
}

// AuthValidator validates whether an incoming HTTP request is authorized to access the given owner.
type AuthValidator func(r *http.Request, ownerID string) bool

// ProxyOptions configures the IngressProxy.
type ProxyOptions struct {
	BaseDomains []string // e.g. "preview.localhost", "compute.internal", "localhost"
	Resolver    TargetResolver
	Auth        AuthValidator
	Transport   http.RoundTripper
	Timeout     time.Duration
}

// IngressProxy is a high-performance unified reverse proxy supporting path-based routing,
// wildcard subdomains, public vs. private authorization, HTTP/1.1, HTTP/2, WebSockets, and SSE.
type IngressProxy struct {
	baseDomains []string
	resolver    TargetResolver
	auth        AuthValidator
	transport   http.RoundTripper
	mu          sync.RWMutex
}

// NewIngressProxy creates a new unified ingress reverse proxy instance.
func NewIngressProxy(opts ProxyOptions) *IngressProxy {
	domains := opts.BaseDomains
	if len(domains) == 0 {
		domains = []string{"preview.localhost", "compute.internal", "compute.dev", "localhost"}
	}
	tr := opts.Transport
	if tr == nil {
		tr = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		}
	}
	return &IngressProxy{
		baseDomains: domains,
		resolver:    opts.Resolver,
		auth:        opts.Auth,
		transport:   tr,
	}
}

// RouteMatch represents parsed routing details for an incoming request.
type RouteMatch struct {
	Target  string // VM ID or VM Name
	Port    int    // Upstream guest port
	SubPath string // Rewritten path to forward upstream
	IsMatch bool
}

// ParseRoute extracts target microVM ID/name, port, and subpath from either subdomain or URL path.
func (p *IngressProxy) ParseRoute(r *http.Request) RouteMatch {
	if r == nil {
		return RouteMatch{}
	}

	// 1. Check Subdomain-Based Routing
	// Formats supported:
	//   <port>-<vm_id_or_name>.<domain> (e.g. 8080-myvm.preview.localhost)
	//   <vm_name>-<port>.<domain>       (e.g. myvm-8080.preview.localhost)
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSpace(host))

	for _, domain := range p.baseDomains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			continue
		}
		suffix := "." + domain
		if strings.HasSuffix(host, suffix) {
			subdomain := strings.TrimSuffix(host, suffix)
			if m, ok := parseSubdomainPrefix(subdomain, r.URL.Path); ok {
				return m
			}
		}
	}

	// 2. Check Path-Based Routing
	// Patterns supported:
	//   /preview/<target>/<port>/*
	//   /api/v1/sandboxes/<target>/preview/<port>/*
	//   /api/v1/vms/<target>/preview/<port>/*
	path := r.URL.Path
	if m, ok := parsePathRoute(path); ok {
		return m
	}

	return RouteMatch{IsMatch: false}
}

func parseSubdomainPrefix(subdomain, rawPath string) (RouteMatch, bool) {
	if subdomain == "" {
		return RouteMatch{}, false
	}

	// Pattern A: <port>-<target> (e.g. 8080-myvm or 3000-ins_abcdef)
	dashIdx := strings.Index(subdomain, "-")
	if dashIdx > 0 {
		portCandidate, err := strconv.Atoi(subdomain[:dashIdx])
		if err == nil && portCandidate > 0 && portCandidate <= 65535 {
			target := subdomain[dashIdx+1:]
			if target != "" {
				subPath := rawPath
				if subPath == "" {
					subPath = "/"
				}
				return RouteMatch{
					Target:  target,
					Port:    portCandidate,
					SubPath: subPath,
					IsMatch: true,
				}, true
			}
		}
	}

	// Pattern B: <target>-<port> (e.g. myvm-8080 or my-sandbox-3000)
	lastDashIdx := strings.LastIndex(subdomain, "-")
	if lastDashIdx > 0 && lastDashIdx < len(subdomain)-1 {
		portCandidate, err := strconv.Atoi(subdomain[lastDashIdx+1:])
		if err == nil && portCandidate > 0 && portCandidate <= 65535 {
			target := subdomain[:lastDashIdx]
			if target != "" {
				subPath := rawPath
				if subPath == "" {
					subPath = "/"
				}
				return RouteMatch{
					Target:  target,
					Port:    portCandidate,
					SubPath: subPath,
					IsMatch: true,
				}, true
			}
		}
	}

	return RouteMatch{}, false
}

func parsePathRoute(path string) (RouteMatch, bool) {
	var trimmed string
	switch {
	case strings.HasPrefix(path, "/preview/"):
		trimmed = strings.TrimPrefix(path, "/preview/")
	case strings.HasPrefix(path, "/api/v1/sandboxes/"):
		after := strings.TrimPrefix(path, "/api/v1/sandboxes/")
		idx := strings.Index(after, "/preview/")
		if idx > 0 {
			target := after[:idx]
			rest := after[idx+len("/preview/"):]
			parts := strings.SplitN(rest, "/", 2)
			if len(parts) >= 1 {
				port, err := strconv.Atoi(parts[0])
				if err == nil && port > 0 && port <= 65535 {
					subPath := "/"
					if len(parts) == 2 && parts[1] != "" {
						subPath = "/" + parts[1]
					}
					return RouteMatch{
						Target:  target,
						Port:    port,
						SubPath: subPath,
						IsMatch: true,
					}, true
				}
			}
		}
		return RouteMatch{}, false
	case strings.HasPrefix(path, "/api/v1/vms/"):
		after := strings.TrimPrefix(path, "/api/v1/vms/")
		idx := strings.Index(after, "/preview/")
		if idx > 0 {
			target := after[:idx]
			rest := after[idx+len("/preview/"):]
			parts := strings.SplitN(rest, "/", 2)
			if len(parts) >= 1 {
				port, err := strconv.Atoi(parts[0])
				if err == nil && port > 0 && port <= 65535 {
					subPath := "/"
					if len(parts) == 2 && parts[1] != "" {
						subPath = "/" + parts[1]
					}
					return RouteMatch{
						Target:  target,
						Port:    port,
						SubPath: subPath,
						IsMatch: true,
					}, true
				}
			}
		}
		return RouteMatch{}, false
	default:
		return RouteMatch{}, false
	}

	parts := strings.SplitN(trimmed, "/", 3)
	if len(parts) < 2 {
		return RouteMatch{}, false
	}
	target := parts[0]
	port, err := strconv.Atoi(parts[1])
	if err != nil || port <= 0 || port > 65535 {
		return RouteMatch{}, false
	}
	subPath := "/"
	if len(parts) == 3 && parts[2] != "" {
		subPath = "/" + parts[2]
	}
	return RouteMatch{
		Target:  target,
		Port:    port,
		SubPath: subPath,
		IsMatch: true,
	}, true
}

// ShouldHandle returns whether the request matches any ingress subdomain or preview path.
func (p *IngressProxy) ShouldHandle(r *http.Request) bool {
	match := p.ParseRoute(r)
	return match.IsMatch
}

// ServeHTTP handles routing, authentication, and reverse-proxying of the incoming request.
func (p *IngressProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	match := p.ParseRoute(r)
	if !match.IsMatch {
		http.Error(w, "invalid preview url format or unmatched route", http.StatusBadRequest)
		return
	}

	if p.resolver == nil {
		http.Error(w, "ingress proxy resolver not configured", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	targetInfo, err := p.resolver.ResolveTarget(ctx, match.Target)
	if err != nil || targetInfo == nil {
		http.Error(w, fmt.Sprintf("sandbox %q not found", match.Target), http.StatusNotFound)
		return
	}

	if targetInfo.Status != "running" {
		http.Error(w, fmt.Sprintf("sandbox %s is not running (status: %s)", match.Target, targetInfo.Status), http.StatusServiceUnavailable)
		return
	}

	if targetInfo.IPAddress == "" || targetInfo.IPAddress == "dhcp" {
		http.Error(w, fmt.Sprintf("sandbox %s has no allocated IP address", match.Target), http.StatusServiceUnavailable)
		return
	}

	// Determine Public vs. Private exposure
	isPublic := p.resolver.IsPortPublic(ctx, targetInfo.ID, match.Port) ||
		p.resolver.IsPortPublic(ctx, targetInfo.Name, match.Port)

	if !isPublic {
		// Private (default): enforce tenant authentication and verify caller owns the VM
		if p.auth != nil && !p.auth(r, targetInfo.OwnerID) {
			http.Error(w, "unauthorized: private sandbox preview requires authentication", http.StatusUnauthorized)
			return
		}
	}

	// Handle WebSocket Upgrade
	if isWebSocketUpgrade(r) {
		p.serveWebSocket(w, r, targetInfo.IPAddress, match.Port, match.SubPath)
		return
	}

	// Handle standard HTTP/1.1, HTTP/2, and Server-Sent Events (SSE)
	p.serveHTTP(w, r, targetInfo.IPAddress, match.Port, match.SubPath)
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// serveWebSocket performs bidirectional raw TCP tunneling / hijacking for WebSocket traffic.
func (p *IngressProxy) serveWebSocket(w http.ResponseWriter, r *http.Request, targetIP string, port int, subPath string) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket hijacking not supported by server", http.StatusInternalServerError)
		return
	}

	targetAddr := net.JoinHostPort(targetIP, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	backendConn, err := dialer.DialContext(r.Context(), "tcp", targetAddr)
	if err != nil {
		http.Error(w, "failed to connect to upstream websocket service: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer backendConn.Close()

	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		http.Error(w, "failed to hijack client connection: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	// Rewrite request URL path and target Host for upstream
	outReq := r.Clone(r.Context())
	outReq.URL.Path = subPath
	outReq.Host = targetAddr

	// Forward client handshake to backend
	if err := outReq.Write(backendConn); err != nil {
		return
	}

	// Full-duplex bidirectional streaming
	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(backendConn, clientBuf)
		errc <- err
	}()
	go func() {
		_, err := io.Copy(clientConn, backendConn)
		errc <- err
	}()

	<-errc
}

// serveHTTP proxies HTTP/1.1 and HTTP/2 requests with immediate unbuffered flush for streaming/SSE.
func (p *IngressProxy) serveHTTP(w http.ResponseWriter, r *http.Request, targetIP string, port int, subPath string) {
	targetURL, err := url.Parse(fmt.Sprintf("http://%s:%d", targetIP, port))
	if err != nil {
		http.Error(w, "failed to parse upstream url: "+err.Error(), http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = p.transport
	// FlushInterval = -1 guarantees immediate unbuffered flushes for SSE / streaming responses
	proxy.FlushInterval = -1

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = subPath
		req.Host = fmt.Sprintf("%s:%d", targetIP, port)
		req.Header.Set("X-Forwarded-Host", r.Host)
		if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			req.Header.Set("X-Forwarded-For", clientIP)
		} else if r.RemoteAddr != "" {
			req.Header.Set("X-Forwarded-For", r.RemoteAddr)
		}
		if r.TLS != nil {
			req.Header.Set("X-Forwarded-Proto", "https")
		} else {
			req.Header.Set("X-Forwarded-Proto", "http")
		}
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		http.Error(w, "bad gateway: upstream service unreachable: "+err.Error(), http.StatusBadGateway)
	}

	proxy.ServeHTTP(w, r)
}
