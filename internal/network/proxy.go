package network

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
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
	// ResolveTargetForOwner resolves a sandbox within a specific owner's
	// namespace (the "<sandbox>.<username>.<zone>" wildcard form). The owner
	// is the username DNS label.
	ResolveTargetForOwner(ctx context.Context, idOrName, ownerLabel string) (*TargetInfo, error)
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
	Owner   string // Username DNS label for "<sandbox>.<username>.<zone>" routes
	SubPath string // Rewritten path to forward upstream
	// Prefix is the URL path prefix the request was matched under, for
	// path-based preview routes (e.g. "/preview/pr-42/8080"). Empty for
	// subdomain routes and the /api/v1/*/preview forms, which are already
	// API-scoped and need no HTML rewriting.
	Prefix  string
	IsMatch bool
}

// ParseRoute extracts target microVM ID/name, port, and subpath from either subdomain or URL path.
//
// Subdomain formats supported (for a base domain like sandbox.example.com):
//
//	<port>-<vm_id_or_name>.<domain>        e.g. 8080-myvm.sandbox.example.com
//	<vm_name>-<port>.<domain>              e.g. myvm-8080.sandbox.example.com
//	<vm>.<username>.<domain>               e.g. myvm.alice.sandbox.example.com (guest port 80)
//	<port>-<vm>.<username>.<domain>        e.g. 8080-myvm.alice.sandbox.example.com
//	<vm>-<port>.<username>.<domain>        e.g. myvm-8080.alice.sandbox.example.com
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
			if m, ok := parseSubdomain(subdomain, r.URL.Path); ok {
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

// parseSubdomain parses the labels preceding a base domain. A single label is
// the legacy "<port>-<vm>" / "<vm>-<port>" form; two labels are the tenant
// wildcard form "<vm>[.<port>].<username>" (guest port 80 when no port is
// given).
func parseSubdomain(subdomain, rawPath string) (RouteMatch, bool) {
	if subdomain == "" {
		return RouteMatch{}, false
	}
	// Apex and reserved host-service labels are served by the daemon/edge, not
	// by sandbox ingress.
	if len(strings.Split(subdomain, ".")) == 1 && networkReservedLabels[strings.ToLower(subdomain)] {
		return RouteMatch{}, false
	}

	labels := strings.Split(subdomain, ".")
	if len(labels) == 2 && labels[0] != "" && labels[1] != "" {
		head := labels[0]
		owner := strings.ToLower(labels[1])
		if m, ok := parseHeadLabel(head, rawPath); ok {
			m.Owner = owner
			return m, true
		}
		// Plain "<vm>.<username>" → guest port 80.
		if m, ok := plainRoute(head, 80, rawPath); ok {
			m.Owner = owner
			return m, true
		}
		return RouteMatch{}, false
	}
	return parseSubdomainPrefix(subdomain, rawPath)
}

// networkReservedLabels are zone-root labels that belong to the host itself
// (GUI, API) and never resolve to a sandbox.
var networkReservedLabels = map[string]bool{"api": true, "gui": true, "www": true}

// parseHeadLabel parses the sandbox-side label, supporting the port-prefixed
// and port-suffixed variants ("<port>-<vm>", "<vm>-<port>") and falling back
// to the plain name when the label parses as neither.
func parseHeadLabel(head, rawPath string) (RouteMatch, bool) {
	dashIdx := strings.Index(head, "-")
	if dashIdx > 0 {
		portCandidate, err := strconv.Atoi(head[:dashIdx])
		if err == nil && portCandidate > 0 && portCandidate <= 65535 {
			target := head[dashIdx+1:]
			if target != "" {
				return portRoute(target, portCandidate, rawPath), true
			}
		}
	}
	lastDashIdx := strings.LastIndex(head, "-")
	if lastDashIdx > 0 && lastDashIdx < len(head)-1 {
		portCandidate, err := strconv.Atoi(head[lastDashIdx+1:])
		if err == nil && portCandidate > 0 && portCandidate <= 65535 {
			target := head[:lastDashIdx]
			if target != "" {
				return portRoute(target, portCandidate, rawPath), true
			}
		}
	}
	return RouteMatch{}, false
}

func portRoute(target string, port int, rawPath string) RouteMatch {
	subPath := rawPath
	if subPath == "" {
		subPath = "/"
	}
	return RouteMatch{Target: target, Port: port, SubPath: subPath, IsMatch: true}
}

func plainRoute(target string, port int, rawPath string) (RouteMatch, bool) {
	if target == "" {
		return RouteMatch{}, false
	}
	return portRoute(target, port, rawPath), true
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
	match := RouteMatch{
		Target:  target,
		Port:    port,
		SubPath: subPath,
		IsMatch: true,
	}
	// Public /preview/<target>/<port> URLs serve full web apps under a path
	// prefix; the proxy rewrites their HTML so absolute asset/API paths keep
	// working (see rewriteHTMLForPrefix).
	if strings.HasPrefix(path, "/preview/") {
		match.Prefix = "/preview/" + target + "/" + strconv.Itoa(port)
	}
	return match, true
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
	var targetInfo *TargetInfo
	var err error
	if match.Owner != "" {
		targetInfo, err = p.resolver.ResolveTargetForOwner(ctx, match.Target, match.Owner)
	} else {
		targetInfo, err = p.resolver.ResolveTarget(ctx, match.Target)
	}
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
	if match.Prefix != "" && previewPrefixIsValid(match.Prefix) {
		w = newHTMLPrefixWriter(w, match.Prefix+"/")
	}
	p.serveHTTP(w, r, targetInfo.IPAddress, match.Port, match.SubPath)
	if hw, ok := w.(*htmlPrefixWriter); ok {
		hw.finalize()
	}
}

// previewPrefixIsValid guards against untrusted URL segments being embedded in
// rewritten HTML. Real sandbox names are DNS labels; anything else is left
// un-rewritten rather than injected into a document.
func previewPrefixIsValid(prefix string) bool {
	for _, part := range strings.Split(strings.Trim(prefix, "/"), "/") {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			c := part[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return false
			}
		}
	}
	return true
}

// rewriteHTMLForPrefix makes an upstream document work under a path prefix:
//   - absolute src=/href= attributes are rewritten to the prefix,
//   - a <base href> is injected after <head> so relative URLs resolve,
//   - a small runtime patch redirects fetch/XHR/EventSource/WebSocket calls
//     that use absolute paths (e.g. "/api/v1/...") to the prefix.
//
// The rewrite runs on already-decoded response bodies (the transport handles
// gzip), so no Content-Encoding negotiation is involved.
func rewriteHTMLForPrefix(body []byte, prefix string) []byte {
	html := string(body)
	base := strings.TrimSuffix(prefix, "/")
	attrRe := regexp.MustCompile(`(?i)\b(src|href)\s*=\s*"(/[^/"][^"]*)"`)
	html = attrRe.ReplaceAllString(html, `$1="`+base+`$2"`)
	attrSingleRe := regexp.MustCompile(`(?i)\b(src|href)\s*=\s*'(/[^/'][^']*)'`)
	html = attrSingleRe.ReplaceAllString(html, `$1='`+base+`$2'`)

	inject := `<base href="` + prefix + `">` + previewRuntimePatch(prefix)
	lower := strings.ToLower(html)
	if idx := strings.Index(lower, "<head"); idx >= 0 {
		if gt := strings.Index(html[idx:], ">"); gt >= 0 {
			at := idx + gt + 1
			return []byte(html[:at] + inject + html[at:])
		}
	}
	return []byte(inject + html)
}

// previewRuntimePatch returns the inline script that patches browser APIs to
// honor the preview prefix for runtime-issued requests (SPA code typically
// fetches "/api/v1/..." with absolute paths that bypass <base>).
func previewRuntimePatch(prefix string) string {
	return `<script>(function(){
var P="` + prefix + `";
function A(u){return typeof u==="string"&&u.charAt(0)==="/"&&u.charAt(1)!=="/";}
var F=window.fetch;
if(F)window.fetch=function(){if(A(arguments[0]))arguments[0]=P+arguments[0].slice(1);return F.apply(this,arguments);};
var O=XMLHttpRequest&&XMLHttpRequest.prototype.open;
if(O)XMLHttpRequest.prototype.open=function(){if(A(arguments[1]))arguments[1]=P+arguments[1].slice(1);return O.apply(this,arguments);};
var E=window.EventSource;
if(E){window.EventSource=function(u){if(A(u))u=P+u.slice(1);return new E(u);};window.EventSource.prototype=E.prototype;}
var W=window.WebSocket;
if(W){window.WebSocket=function(u,p){if(A(u))u=(location.protocol==="https:"?"wss://":"ws://")+location.host+P+u.slice(1);return p!==undefined?new W(u,p):new W(u);};window.WebSocket.prototype=W.prototype;window.WebSocket.CONNECTING=W.CONNECTING;window.WebSocket.OPEN=W.OPEN;window.WebSocket.CLOSING=W.CLOSING;window.WebSocket.CLOSED=W.CLOSED;}
})();</script>`
}

// htmlPrefixWriter buffers HTML responses so they can be rewritten for the
// preview path prefix. Non-HTML responses (including SSE streams) pass
// through untouched.
type htmlPrefixWriter struct {
	w           http.ResponseWriter
	prefix      string // ends with "/"
	wroteHeader bool
	buffering   bool
	status      int
	buf         bytes.Buffer
	finalized   bool
}

func newHTMLPrefixWriter(w http.ResponseWriter, prefix string) *htmlPrefixWriter {
	return &htmlPrefixWriter{w: w, prefix: prefix}
}

func (h *htmlPrefixWriter) Header() http.Header { return h.w.Header() }

func (h *htmlPrefixWriter) WriteHeader(code int) {
	if h.wroteHeader {
		return
	}
	h.wroteHeader = true
	h.status = code
	if strings.Contains(h.w.Header().Get("Content-Type"), "text/html") {
		h.buffering = true
		return
	}
	h.w.WriteHeader(code)
}

func (h *htmlPrefixWriter) Write(p []byte) (int, error) {
	if !h.wroteHeader {
		h.WriteHeader(http.StatusOK)
	}
	if h.buffering {
		if h.finalized {
			return h.w.Write(p)
		}
		return h.buf.Write(p)
	}
	return h.w.Write(p)
}

// Flush is a no-op while an HTML response is being buffered; the full body is
// written by finalize once the upstream handler returns. Reverse proxying
// (FlushInterval -1) flushes immediately after WriteHeader, which would
// otherwise cut the buffered rewrite short. Non-HTML responses flush through.
func (h *htmlPrefixWriter) Flush() {
	if !h.buffering || h.finalized {
		if f, ok := h.w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func (h *htmlPrefixWriter) finalize() {
	if !h.buffering || h.finalized {
		return
	}
	h.finalized = true
	rewritten := rewriteHTMLForPrefix(h.buf.Bytes(), h.prefix)
	header := h.w.Header()
	header.Del("Content-Encoding") // upstream body is decoded; no encoding to declare
	header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	h.w.WriteHeader(h.status)
	_, _ = h.w.Write(rewritten)
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
