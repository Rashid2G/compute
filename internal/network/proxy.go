package network

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
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
	WorkerID  string `json:"worker_id,omitempty"`
}

type targetInfoCtxKey struct{}

// TargetResolver resolves a VM ID or name to its target information and port exposure.
type TargetResolver interface {
	ResolveTarget(ctx context.Context, idOrName string) (*TargetInfo, error)
	// ResolveTargetForOwner resolves a sandbox within a specific owner's
	// namespace (the "<sandbox>.<username>.<zone>" wildcard form). The owner
	// is the username DNS label.
	ResolveTargetForOwner(ctx context.Context, idOrName, ownerLabel string) (*TargetInfo, error)
	IsPortPublic(ctx context.Context, idOrName string, port int) bool
}

// OwnerPortChecker is an optional extension of TargetResolver that scopes public
// port exposure checks to the resolved VM's OwnerID, preventing cross-tenant
// collisions when two tenants use the same sandbox name.
type OwnerPortChecker interface {
	IsPortPublicForOwner(ctx context.Context, ownerID, idOrName string, port int) bool
}

// AuthValidator validates whether an incoming HTTP request is authorized to access the given owner.
type AuthValidator func(r *http.Request, ownerID string) bool

// DialTargetFunc dials a backend sandbox address, optionally tunneling through a remote worker.
type DialTargetFunc func(ctx context.Context, target *TargetInfo, network, addr string) (net.Conn, error)

// ProxyOptions configures the IngressProxy.
type ProxyOptions struct {
	BaseDomains []string // e.g. "preview.localhost", "compute.internal", "localhost"
	Resolver    TargetResolver
	Auth        AuthValidator
	DialTarget  DialTargetFunc
	Transport   http.RoundTripper
	Timeout     time.Duration
}

// IngressProxy is a high-performance unified reverse proxy supporting path-based routing,
// wildcard subdomains, public vs. private authorization, HTTP/1.1, HTTP/2, WebSockets, and SSE.
type IngressProxy struct {
	baseDomains []string
	resolver    TargetResolver
	auth        AuthValidator
	dialTarget  DialTargetFunc
	transport   http.RoundTripper
	mu          sync.RWMutex
}

// NewIngressProxy creates a new unified ingress reverse proxy instance.
func NewIngressProxy(opts ProxyOptions) *IngressProxy {
	domains := opts.BaseDomains
	if len(domains) == 0 {
		domains = []string{"preview.localhost", "compute.internal", "compute.dev", "localhost"}
	}
	baseDialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	tr := opts.Transport
	if tr == nil {
		tr = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if opts.DialTarget != nil {
					if target, ok := ctx.Value(targetInfoCtxKey{}).(*TargetInfo); ok && target != nil && target.WorkerID != "" && target.WorkerID != "nod_host_local" {
						return opts.DialTarget(ctx, target, network, addr)
					}
				}
				return baseDialer.DialContext(ctx, network, addr)
			},
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
		dialTarget:  opts.DialTarget,
		transport:   tr,
	}
}

// RouteMatch represents parsed routing details for an incoming request.
type RouteMatch struct {
	Target  string // VM ID or VM Name
	Port    int    // Upstream guest port
	Owner   string // Username DNS label for "<sandbox>.<username>.<zone>" or "/preview/@<username>/<sandbox>/<port>" routes
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
	//   /preview/@<owner>/<target>/<port>/*
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

	// Support optional owner-scoped path form: /preview/@<owner>/<target>/<port>/*
	ownerLabel := ""
	rawOwnerSegment := ""
	if strings.HasPrefix(trimmed, "@") {
		slashIdx := strings.Index(trimmed, "/")
		if slashIdx <= 1 {
			return RouteMatch{}, false
		}
		rawOwnerSegment = trimmed[:slashIdx]
		ownerLabel = strings.ToLower(strings.TrimPrefix(rawOwnerSegment, "@"))
		trimmed = trimmed[slashIdx+1:]
	}

	parts := strings.SplitN(trimmed, "/", 3)
	if len(parts) < 2 {
		return RouteMatch{}, false
	}
	target := parts[0]
	if target == "" {
		return RouteMatch{}, false
	}
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
		Owner:   ownerLabel,
		SubPath: subPath,
		IsMatch: true,
	}
	// Public /preview/<target>/<port> URLs serve full web apps under a path
	// prefix; the proxy rewrites their HTML so absolute asset/API paths keep
	// working (see rewriteHTMLForPrefix).
	if strings.HasPrefix(path, "/preview/") {
		if rawOwnerSegment != "" {
			match.Prefix = "/preview/" + rawOwnerSegment + "/" + target + "/" + strconv.Itoa(port)
		} else {
			match.Prefix = "/preview/" + target + "/" + strconv.Itoa(port)
		}
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

	// Determine Public vs. Private exposure (scoped to targetInfo.OwnerID when supported)
	isPublic := false
	if ownerChecker, ok := p.resolver.(OwnerPortChecker); ok {
		isPublic = ownerChecker.IsPortPublicForOwner(ctx, targetInfo.OwnerID, targetInfo.ID, match.Port) ||
			ownerChecker.IsPortPublicForOwner(ctx, targetInfo.OwnerID, targetInfo.Name, match.Port)
	} else {
		isPublic = p.resolver.IsPortPublic(ctx, targetInfo.ID, match.Port) ||
			p.resolver.IsPortPublic(ctx, targetInfo.Name, match.Port)
	}

	if !isPublic {
		// Private (default): enforce tenant authentication and verify caller owns the VM
		if p.auth != nil && !p.auth(r, targetInfo.OwnerID) {
			http.Error(w, "unauthorized: private sandbox preview requires authentication", http.StatusUnauthorized)
			return
		}
	}

	r = r.WithContext(context.WithValue(r.Context(), targetInfoCtxKey{}, targetInfo))

	// Handle WebSocket Upgrade
	if isWebSocketUpgrade(r) {
		p.serveWebSocket(w, r, targetInfo, match.Port, match.SubPath)
		return
	}

	// Handle standard HTTP/1.1, HTTP/2, and Server-Sent Events (SSE)
	prefix := ""
	if match.Prefix != "" && previewPrefixIsValid(match.Prefix) {
		prefix = match.Prefix
		w = newHTMLPrefixWriter(w, match.Prefix+"/")
	}
	p.serveHTTP(w, r, targetInfo, match.Port, match.SubPath, prefix)
	if hw, ok := w.(*htmlPrefixWriter); ok {
		hw.finalize()
	}
}

// previewPrefixIsValid guards against untrusted URL segments being embedded in
// rewritten HTML. Real sandbox names are DNS labels; anything else is left
// un-rewritten rather than injected into a document.
func previewPrefixIsValid(prefix string) bool {
	for _, part := range strings.Split(strings.Trim(prefix, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for i := 0; i < len(part); i++ {
			c := part[i]
			if i == 0 && c == '@' && len(part) > 1 {
				continue
			}
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
	attrRe := regexp.MustCompile(`(?i)\b(src|href|action)\s*=\s*"(/[^/"][^"]*)"`)
	html = attrRe.ReplaceAllStringFunc(html, func(m string) string {
		sub := attrRe.FindStringSubmatch(m)
		if len(sub) == 3 && (sub[2] == base || strings.HasPrefix(sub[2], base+"/")) {
			return m
		}
		return sub[1] + `="` + base + sub[2] + `"`
	})
	attrSingleRe := regexp.MustCompile(`(?i)\b(src|href|action)\s*=\s*'(/[^/'][^']*)'`)
	html = attrSingleRe.ReplaceAllStringFunc(html, func(m string) string {
		sub := attrSingleRe.FindStringSubmatch(m)
		if len(sub) == 3 && (sub[2] == base || strings.HasPrefix(sub[2], base+"/")) {
			return m
		}
		return sub[1] + `='` + base + sub[2] + `'`
	})

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
var B=P.slice(0,-1);
function A(u){return typeof u==="string"&&u.charAt(0)==="/"&&u.charAt(1)!=="/"&&u!==B&&u.indexOf(P)!==0;}
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

// acceptsRewritableHTML reports whether a client request may receive an HTML
// document that the proxy will rewrite. Browsers send "text/html" for
// navigations and curl/axios send "*/*"; both are safe to ask upstream for an
// uncompressed body.
func acceptsRewritableHTML(accept string) bool {
	accept = strings.ToLower(accept)
	if strings.TrimSpace(accept) == "" {
		return true
	}
	return strings.Contains(accept, "text/html") || strings.Contains(accept, "*/*")
}

// maxRewritableHTMLBody bounds decompression of an upstream HTML body so a
// misbehaving sandbox cannot exhaust daemon memory through the rewrite path.
const maxRewritableHTMLBody = 16 << 20

// decodeHTMLBody returns the decoded document for a supported Content-Encoding.
// Unsupported encodings return an error so the caller can forward the upstream
// bytes untouched instead of corrupting them.
func decodeHTMLBody(body []byte, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return body, nil
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		return readRewritableHTML(zr)
	case "deflate":
		if zr, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			decoded, readErr := readRewritableHTML(zr)
			zr.Close()
			if readErr == nil {
				return decoded, nil
			}
		}
		// Some servers send raw DEFLATE without the zlib wrapper.
		fr := flate.NewReader(bytes.NewReader(body))
		defer fr.Close()
		return readRewritableHTML(fr)
	default:
		return nil, fmt.Errorf("unsupported content encoding %q", encoding)
	}
}

func readRewritableHTML(r io.Reader) ([]byte, error) {
	out, err := io.ReadAll(io.LimitReader(r, maxRewritableHTMLBody+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxRewritableHTMLBody {
		return nil, fmt.Errorf("decoded HTML exceeds %d bytes", maxRewritableHTMLBody)
	}
	return out, nil
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
	if !strings.Contains(h.w.Header().Get("Content-Type"), "text/html") {
		h.w.WriteHeader(code)
		return
	}
	// Buffer only bodies this writer can decode and rewrite. Anything else
	// (e.g. "br" or a multi-encoding list) is forwarded untouched so the
	// client can decode it.
	if !rewritableContentEncoding(h.w.Header().Get("Content-Encoding")) {
		h.w.WriteHeader(code)
		return
	}
	h.buffering = true
}

func rewritableContentEncoding(encoding string) bool {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity", "gzip", "x-gzip", "deflate":
		return true
	}
	return false
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
	header := h.w.Header()
	body := h.buf.Bytes()
	encoding := header.Get("Content-Encoding")
	decoded, err := decodeHTMLBody(body, encoding)
	if err != nil {
		// The body cannot be decoded safely; forward it untouched (including
		// Content-Encoding) rather than emitting bytes the client cannot read.
		header.Set("Content-Length", strconv.Itoa(len(body)))
		h.w.WriteHeader(h.status)
		_, _ = h.w.Write(body)
		return
	}
	rewritten := rewriteHTMLForPrefix(decoded, h.prefix)
	header.Del("Content-Encoding") // body is decoded; no encoding to declare
	header.Del("Content-Length")
	header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	h.w.WriteHeader(h.status)
	_, _ = h.w.Write(rewritten)
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// serveWebSocket performs bidirectional raw TCP tunneling / hijacking for WebSocket traffic.
func (p *IngressProxy) serveWebSocket(w http.ResponseWriter, r *http.Request, target *TargetInfo, port int, subPath string) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket hijacking not supported by server", http.StatusInternalServerError)
		return
	}

	targetAddr := net.JoinHostPort(target.IPAddress, strconv.Itoa(port))
	var backendConn net.Conn
	var err error
	if p.dialTarget != nil && target.WorkerID != "" && target.WorkerID != "nod_host_local" {
		backendConn, err = p.dialTarget(r.Context(), target, "tcp", targetAddr)
	} else {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		backendConn, err = dialer.DialContext(r.Context(), "tcp", targetAddr)
	}
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

// rewriteRedirectLocation keeps root-relative upstream redirects within the
// preview path prefix (e.g. "Location: /login" -> "Location: /preview/myvm/8080/login").
func rewriteRedirectLocation(loc, prefix string) string {
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" || loc == "" {
		return loc
	}
	if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") {
		if loc == prefix || strings.HasPrefix(loc, prefix+"/") {
			return loc
		}
		return prefix + loc
	}
	return loc
}

// serveHTTP proxies HTTP/1.1 and HTTP/2 requests with immediate unbuffered flush for streaming/SSE.
func (p *IngressProxy) serveHTTP(w http.ResponseWriter, r *http.Request, target *TargetInfo, port int, subPath, prefix string) {
	targetIP := target.IPAddress
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
		if target.WorkerID != "" && target.WorkerID != "nod_host_local" {
			req.Close = true
		}
		req.Header.Set("X-Forwarded-Host", r.Host)
		if prefix != "" {
			req.Header.Set("X-Forwarded-Prefix", prefix)
			// HTML responses under a preview prefix are buffered and rewritten by
			// htmlPrefixWriter, which needs the decoded document. Forwarding the
			// browser's Accept-Encoding lets the upstream compress the body; the
			// writer would then rewrite compressed bytes and strip
			// Content-Encoding, handing the browser an unreadable payload. Ask
			// for identity on document requests and leave asset requests (JS,
			// CSS, images) compressed so they still stream through efficiently.
			if acceptsRewritableHTML(r.Header.Get("Accept")) {
				req.Header.Set("Accept-Encoding", "identity")
			}
		}
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

	if prefix != "" {
		proxy.ModifyResponse = func(resp *http.Response) error {
			if loc := resp.Header.Get("Location"); loc != "" {
				resp.Header.Set("Location", rewriteRedirectLocation(loc, prefix))
			}
			return nil
		}
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		http.Error(w, "bad gateway: upstream service unreachable: "+err.Error(), http.StatusBadGateway)
	}

	proxy.ServeHTTP(w, r)
}
