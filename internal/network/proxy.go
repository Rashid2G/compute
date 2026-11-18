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
	BaseDomains  []string // legacy DNS zones for "<sandbox>.<user>.<zone>" routing
	ServeDomains []string // serve domains for "<slug>.<serve-domain>" routing
	Resolver     TargetResolver
	Auth         AuthValidator
	DialTarget   DialTargetFunc
	Transport    http.RoundTripper
	Timeout      time.Duration

	// ServeResolver resolves published exposures by slug (subdomain mode) or
	// by exposure ID (port mode).
	ServeResolver ServeResolver
	// ServeTokenKey signs/verifies short-lived private-serve tokens. Empty
	// disables token auth (private serves then require a bearer token).
	ServeTokenKey []byte
	// AuthorizeBaseURL is the daemon base URL browsers are sent to when a
	// private serve needs authentication (e.g. "https://compute.example.com").
	AuthorizeBaseURL string
}

// Exposure describes one published guest port and its access rules.
type Exposure struct {
	ID        string
	OwnerID   string
	Slug      string
	GuestPort int
	Public    bool
}

// ServeResolver resolves exposures for subdomain and port based serving.
type ServeResolver interface {
	ResolveServeSlug(ctx context.Context, slug string) (*TargetInfo, *Exposure, error)
	ResolveServeExposure(ctx context.Context, id string) (*TargetInfo, *Exposure, error)
}

// IngressProxy is a high-performance unified reverse proxy supporting
// hostname-based serving, public vs. private authorization, HTTP/1.1, HTTP/2,
// WebSockets, and SSE.
type IngressProxy struct {
	baseDomains      []string
	serveDomains     []string
	resolver         TargetResolver
	serveResolver    ServeResolver
	auth             AuthValidator
	dialTarget       DialTargetFunc
	transport        http.RoundTripper
	serveTokenKey    []byte
	authorizeBaseURL string
	mu               sync.RWMutex
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
		baseDomains:      domains,
		serveDomains:     opts.ServeDomains,
		resolver:         opts.Resolver,
		serveResolver:    opts.ServeResolver,
		auth:             opts.Auth,
		dialTarget:       opts.DialTarget,
		transport:        tr,
		serveTokenKey:    opts.ServeTokenKey,
		authorizeBaseURL: strings.TrimRight(strings.TrimSpace(opts.AuthorizeBaseURL), "/"),
	}
}

// RouteMatch represents parsed routing details for an incoming request.
type RouteMatch struct {
	Target   string    // VM ID or VM Name
	Port     int       // Upstream guest port
	Owner    string    // Username DNS label for "<sandbox>.<username>.<zone>" routes
	SubPath  string    // Path forwarded upstream
	Exposure *Exposure // Non-nil for published serve routes
	IsMatch  bool
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

	// 1. Published serve subdomains: <slug>.<serve-domain>. Every exposure has
	// its own origin, so the app sees "/" as its root and no URL rewriting is
	// needed.
	for _, domain := range p.serveDomains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			continue
		}
		suffix := "." + domain
		if !strings.HasSuffix(host, suffix) {
			continue
		}
		slug := strings.TrimSuffix(host, suffix)
		if slug == "" || strings.Contains(slug, ".") || networkReservedLabels[strings.ToLower(slug)] {
			continue
		}
		if p.serveResolver == nil {
			continue
		}
		target, exp, err := p.serveResolver.ResolveServeSlug(r.Context(), slug)
		if err != nil || target == nil || exp == nil {
			continue
		}
		return RouteMatch{
			Target:   target.Name,
			Port:     exp.GuestPort,
			SubPath:  r.URL.Path,
			Exposure: exp,
			IsMatch:  true,
		}
	}

	// 2. Legacy DNS zones: "<port>-<vm>.<zone>", "<vm>.<username>.<zone>", ...
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

	// 3. Path-based preview routes:
	//   /preview/<target>/<port>[/...]
	//   /api/v1/sandboxes/<target>/preview/<port>[/...]
	//   /api/v1/vms/<target>/preview/<port>[/...]
	if r.URL != nil {
		if m, ok := parsePreviewPath(r.URL.Path); ok {
			return m
		}
	}

	return RouteMatch{IsMatch: false}
}

func parsePreviewPath(rawPath string) (RouteMatch, bool) {
	if strings.HasPrefix(rawPath, "/preview/") {
		rest := strings.TrimPrefix(rawPath, "/preview/")
		return splitTargetPortSubpath(rest)
	}
	for _, apiPrefix := range []string{"/api/v1/sandboxes/", "/api/v1/vms/"} {
		if strings.HasPrefix(rawPath, apiPrefix) {
			rest := strings.TrimPrefix(rawPath, apiPrefix)
			slashIdx := strings.IndexByte(rest, '/')
			if slashIdx <= 0 {
				continue
			}
			target := rest[:slashIdx]
			afterTarget := rest[slashIdx:]
			if !strings.HasPrefix(afterTarget, "/preview/") {
				continue
			}
			portAndRest := strings.TrimPrefix(afterTarget, "/preview/")
			return splitPortSubpath(target, portAndRest)
		}
	}
	return RouteMatch{}, false
}

func splitTargetPortSubpath(rest string) (RouteMatch, bool) {
	slashIdx := strings.IndexByte(rest, '/')
	if slashIdx <= 0 {
		return RouteMatch{}, false
	}
	target := rest[:slashIdx]
	portAndRest := rest[slashIdx+1:]
	if strings.HasPrefix(target, "@") && len(target) > 1 {
		owner := strings.ToLower(target[1:])
		if m, ok := splitTargetPortSubpath(portAndRest); ok {
			m.Owner = owner
			return m, true
		}
		return RouteMatch{}, false
	}
	return splitPortSubpath(target, portAndRest)
}

func splitPortSubpath(target, portAndRest string) (RouteMatch, bool) {
	if target == "" || portAndRest == "" {
		return RouteMatch{}, false
	}
	portStr := portAndRest
	subPath := "/"
	if slashIdx := strings.IndexByte(portAndRest, '/'); slashIdx >= 0 {
		portStr = portAndRest[:slashIdx]
		subPath = portAndRest[slashIdx:]
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return RouteMatch{}, false
	}
	return RouteMatch{
		Target:  target,
		Port:    port,
		SubPath: subPath,
		IsMatch: true,
	}, true
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

// ShouldHandle reports whether the request targets a sandbox exposure host.
func (p *IngressProxy) ShouldHandle(r *http.Request) bool {
	match := p.ParseRoute(r)
	return match.IsMatch
}

// ServeHTTP handles hostname routing, authentication, and reverse-proxying.
func (p *IngressProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	match := p.ParseRoute(r)
	if !match.IsMatch {
		http.Error(w, "unknown host: no sandbox exposure matches this URL", http.StatusNotFound)
		return
	}

	ctx := r.Context()
	if match.Exposure != nil {
		if p.serveResolver == nil {
			http.Error(w, "serve resolver not configured", http.StatusInternalServerError)
			return
		}
		target, exp, err := p.serveResolver.ResolveServeSlug(ctx, match.Exposure.Slug)
		if err != nil || target == nil || exp == nil {
			http.Error(w, "sandbox exposure not found", http.StatusNotFound)
			return
		}
		p.serveResolved(w, r, target, exp)
		return
	}

	if p.resolver == nil {
		http.Error(w, "ingress proxy resolver not configured", http.StatusInternalServerError)
		return
	}
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

	// Legacy DNS routing has no exposure record; derive access from the
	// port-forward table.
	isPublic := false
	if ownerChecker, ok := p.resolver.(OwnerPortChecker); ok {
		isPublic = ownerChecker.IsPortPublicForOwner(ctx, targetInfo.OwnerID, targetInfo.ID, match.Port) ||
			ownerChecker.IsPortPublicForOwner(ctx, targetInfo.OwnerID, targetInfo.Name, match.Port)
	} else {
		isPublic = p.resolver.IsPortPublic(ctx, targetInfo.ID, match.Port) ||
			p.resolver.IsPortPublic(ctx, targetInfo.Name, match.Port)
	}
	exp := &Exposure{
		ID:        fmt.Sprintf("preview:%s:%d", targetInfo.ID, match.Port),
		OwnerID:   targetInfo.OwnerID,
		GuestPort: match.Port,
		Public:    isPublic,
	}
	// Authorize against the public request path first: rewriting r.URL.Path to
	// the subpath below would drop "/preview/<target>/<port>/" from the
	// authorize redirect's "next" URL.
	if !exp.Public && !p.authorizePrivateServe(w, r, targetInfo, exp) {
		return
	}
	if match.SubPath != "" && r.URL != nil && r.URL.Path != match.SubPath {
		r = r.Clone(ctx)
		r.URL.Path = match.SubPath
		r.URL.RawPath = ""
	}
	p.serveResolved(w, r, targetInfo, exp)
}

// ServeExposure proxies a port-mode request bound to one exposure ID.
func (p *IngressProxy) ServeExposure(w http.ResponseWriter, r *http.Request, exposureID string) {
	if p.serveResolver == nil {
		http.Error(w, "serve resolver not configured", http.StatusInternalServerError)
		return
	}
	target, exp, err := p.serveResolver.ResolveServeExposure(r.Context(), exposureID)
	if err != nil || target == nil || exp == nil {
		http.Error(w, "sandbox exposure not found", http.StatusNotFound)
		return
	}
	p.serveResolved(w, r, target, exp)
}

// serveResolved applies access control and proxies the request to the sandbox.
// The upstream sees the public Host and the request path unchanged, so apps
// that build absolute URLs behave as if they were served from their own root.
func (p *IngressProxy) serveResolved(w http.ResponseWriter, r *http.Request, target *TargetInfo, exp *Exposure) {
	if target == nil || exp == nil {
		http.Error(w, "sandbox exposure not found", http.StatusNotFound)
		return
	}
	if target.Status != "running" {
		http.Error(w, fmt.Sprintf("sandbox %s is not running (status: %s)", target.Name, target.Status), http.StatusServiceUnavailable)
		return
	}
	if target.IPAddress == "" || target.IPAddress == "dhcp" {
		http.Error(w, fmt.Sprintf("sandbox %s has no allocated IP address", target.Name), http.StatusServiceUnavailable)
		return
	}
	if !exp.Public && !p.authorizePrivateServe(w, r, target, exp) {
		return
	}

	r = r.WithContext(context.WithValue(r.Context(), targetInfoCtxKey{}, target))
	if isWebSocketUpgrade(r) {
		p.serveWebSocket(w, r, target, exp.GuestPort, r.URL.Path)
		return
	}
	p.serveHTTP(w, r, target, exp.GuestPort, r.URL.Path)
}

// authorizePrivateServe admits bearer/session credentials, a signed serve
// token (query or cookie), or bounces browser navigations through the daemon so
// it can mint a token from the user's existing session.
func (p *IngressProxy) authorizePrivateServe(w http.ResponseWriter, r *http.Request, target *TargetInfo, exp *Exposure) bool {
	if p.auth != nil && p.auth(r, target.OwnerID) {
		return true
	}
	if token := serveTokenFromRequest(r); token != "" && len(p.serveTokenKey) > 0 {
		if id, owner, err := VerifyServeToken(p.serveTokenKey, token); err == nil && id == exp.ID && (owner == "" || owner == target.OwnerID) {
			if r.URL.Query().Get(ServeTokenQueryParam) != "" {
				p.setServeCookie(w, r, token)
				redirectServeClean(w, r)
				return false
			}
			return true
		}
	}
	if base := p.authorizeBaseURLForRequest(r); base != "" && acceptsHTML(r.Header.Get("Accept")) {
		next := serveRequestURL(r)
		http.Redirect(w, r, base+"/api/v1/serve/authorize?next="+url.QueryEscape(next), http.StatusFound)
		return false
	}
	http.Error(w, "unauthorized: private sandbox exposure requires authentication", http.StatusUnauthorized)
	return false
}

// authorizeBaseURLForRequest returns the daemon base URL browsers should use to
// authenticate. When the configured base URL points at loopback but the request
// arrived through a public host (e.g. Tailscale Serve forwarding
// "compute-lab.tailb1be99.ts.net" to 127.0.0.1:18080), the request's own scheme
// and host are used so remote browsers are not redirected to loopback.
func (p *IngressProxy) authorizeBaseURLForRequest(r *http.Request) string {
	base := p.authorizeBaseURL
	if base == "" || r == nil {
		return base
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || !isLoopbackHost(u.Hostname()) {
		return base
	}
	host := requestForwardedHost(r)
	if host == "" || isLoopbackHost(stripHostPort(host)) {
		return base
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	u.Scheme = scheme
	u.Host = normalizeRequestHost(scheme, host)
	return strings.TrimRight(u.String(), "/")
}

// requestForwardedHost returns the externally visible host, preferring
// X-Forwarded-Host over the request's Host header.
func requestForwardedHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); fwd != "" {
		if idx := strings.IndexByte(fwd, ','); idx >= 0 {
			fwd = strings.TrimSpace(fwd[:idx])
		}
		return fwd
	}
	return strings.TrimSpace(r.Host)
}

func stripHostPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.Trim(host, "[]")
}

// normalizeRequestHost drops a port that is redundant for the given scheme.
func normalizeRequestHost(scheme, host string) string {
	h, port, err := net.SplitHostPort(host)
	if err != nil {
		return host
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		if strings.Contains(h, ":") {
			return "[" + h + "]"
		}
		return h
	}
	return host
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(stripHostPort(host)))
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func (p *IngressProxy) setServeCookie(w http.ResponseWriter, r *http.Request, token string) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     ServeCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   int(ServeTokenTTL.Seconds()),
	})
}

// serveRequestURL reconstructs the externally visible request URL.
func serveRequestURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); fwd != "" {
		host = fwd
	}
	return scheme + "://" + host + r.URL.RequestURI()
}

// redirectServeClean removes the one-time token from the address bar after it
// has been exchanged for a cookie.
func redirectServeClean(w http.ResponseWriter, r *http.Request) {
	u := *r.URL
	q := u.Query()
	q.Del(ServeTokenQueryParam)
	u.RawQuery = q.Encode()
	target := u.Path
	if target == "" {
		target = "/"
	}
	if u.RawQuery != "" {
		target += "?" + u.RawQuery
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func acceptsHTML(accept string) bool {
	accept = strings.ToLower(accept)
	if strings.TrimSpace(accept) == "" {
		return false
	}
	return strings.Contains(accept, "text/html") || strings.Contains(accept, "*/*")
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

	// Forward the public Host (as with HTTP requests) and the upstream path.
	outReq := r.Clone(r.Context())
	outReq.URL.Path = subPath
	outReq.Host = r.Host
	outReq.Header.Set("X-Forwarded-Host", r.Host)

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
func (p *IngressProxy) serveHTTP(w http.ResponseWriter, r *http.Request, target *TargetInfo, port int, subPath string) {
	targetURL, err := url.Parse(fmt.Sprintf("http://%s:%d", target.IPAddress, port))
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
		if subPath != "" {
			req.URL.Path = subPath
		}
		// Preserve the public Host: apps that build absolute URLs or redirects
		// from it then emit the serve origin, which is exactly where the client
		// is. X-Forwarded-* carries the original values for apps that need them.
		if target.WorkerID != "" && target.WorkerID != "nod_host_local" {
			req.Close = true
		}
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
