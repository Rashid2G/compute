package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/network"
)

// ===== Sandbox serving: one origin per exposure =====
//
// Every published guest port is reachable at its own origin so the app sees
// "/" as its root: redirects, history.pushState, cookies, service workers and
// absolute asset URLs all behave normally. Two transports provide that origin:
//
//   - subdomain mode: "<slug>.<serve-domain>" (wildcard DNS + wildcard TLS).
//     Loopback binds default to "<slug>.localhost:<api-port>", which browsers
//     resolve to loopback with no DNS setup.
//   - port mode: one daemon listener per exposure, "<host>:<port>". Used for
//     bare IPs, tailnets and deployments without a wildcard domain.

// serveSlugFor builds a DNS-safe, globally unique serve label for an exposure.
func (s *Server) ensureServeSlug(ctx context.Context, st *db.Store, v *db.VM, guestPort int, requested string) (string, error) {
	base := ""
	if strings.TrimSpace(requested) != "" {
		base = sanitizeServeSlug(requested)
	}
	if base == "" {
		base = sanitizeServeSlug(fmt.Sprintf("%s-%d", v.Name, guestPort))
	}
	candidate := base
	for i := 0; i < 8; i++ {
		pf, err := st.GetPortForwardBySubdomain(ctx, candidate)
		if err != nil {
			return "", err
		}
		if pf == nil || (pf.VMID == v.ID && pf.GuestPort == guestPort) {
			return candidate, nil
		}
		candidate = base + "-" + randomLabelSuffix()
	}
	return "", fmt.Errorf("could not allocate a unique serve name for %q", base)
}

func sanitizeServeSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 48 {
		out = strings.Trim(out[:48], "-")
	}
	if out == "" {
		out = "serve"
	}
	return out
}

func randomLabelSuffix() string {
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return "x1"
	}
	return hex.EncodeToString(buf)
}

// serveURLFor renders the absolute URL for an exposure.
func (s *Server) serveURLFor(slug string, hostPort int) string {
	scheme := s.cfg.ServeScheme()
	cert, key := s.serveTLSPaths()
	hasTLS := cert != "" && key != ""
	if hasTLS {
		scheme = "https"
	}
	if s.cfg.ServeMode() == "subdomain" {
		domain := s.cfg.ServeDomainResolved()
		host := slug + "." + domain
		if isLocalServeDomain(domain) {
			if !hasTLS && s.cfg.Server.ServeTLSCertFile == "" && s.cfg.Server.ServeTLSKeyFile == "" && !strings.HasPrefix(s.cfg.Server.NormalizedPublicURL(), "https://") {
				scheme = "http"
			}
			if port := s.apiListenPort(); port != "" && port != "80" && port != "443" {
				host = net.JoinHostPort(host, port)
			}
		} else if s.cfg.Server.NormalizedPublicURL() == "" {
			scheme = "https"
		}
		return scheme + "://" + host + "/"
	}

	host := s.cfg.Server.PublicURLHost()
	if host == "" {
		host = s.cfg.Server.ListenHost()
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(hostPort)) + "/"
}

func isLocalServeDomain(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	return domain == "localhost" || strings.HasSuffix(domain, ".localhost")
}

func isLocalServeHost(host string) bool {
	if isLocalServeDomain(host) {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

func (s *Server) apiListenPort() string {
	addr := strings.TrimPrefix(strings.TrimSpace(s.cfg.Server.ListenAddr), "tcp://")
	if strings.HasPrefix(addr, "unix://") {
		return ""
	}
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return ""
}

// prepareServe assigns the origin for an exposure: a unique slug in subdomain
// mode, or a dedicated listener port in port mode, and stores the absolute URL.
// The record must already be persisted so port-mode requests can resolve it.
func (s *Server) prepareServe(ctx context.Context, st *db.Store, pf *db.PortForward, v *db.VM, requestedSlug string) error {
	if pf == nil || v == nil {
		return fmt.Errorf("serve target missing")
	}
	if s.cfg.ServeMode() == "port" {
		if s.servePorts == nil {
			return fmt.Errorf("serve port registry not initialized")
		}
		port, err := s.servePorts.ensure(pf)
		if err != nil {
			return err
		}
		pf.HostPort = port
		pf.Subdomain = ""
		pf.PublicURL = s.serveURLFor("", port)
		return nil
	}

	slug := strings.TrimSpace(pf.Subdomain)
	if requestedSlug != "" || slug == "" {
		allocated, err := s.ensureServeSlug(ctx, st, v, pf.GuestPort, requestedSlug)
		if err != nil {
			return err
		}
		slug = allocated
	}
	pf.Subdomain = slug
	// An explicitly requested host port also opens a dedicated listener in
	// subdomain mode, so callers can reach the exposure by "<host>:<port>".
	if pf.HostPort > 0 && s.servePorts != nil {
		port, err := s.servePorts.ensure(pf)
		if err != nil {
			return err
		}
		pf.HostPort = port
		pf.PublicURL = s.serveURLFor(slug, port)
		return nil
	}
	pf.HostPort = 0
	pf.PublicURL = s.serveURLFor(slug, 0)
	return nil
}

// releaseServe stops any listener or allocation owned by an exposure.
func (s *Server) releaseServe(pf *db.PortForward) {
	if pf == nil || s.servePorts == nil {
		return
	}
	s.servePorts.release(pf.ID)
}

// ===== Resolvers =====

func (s *Server) ResolveServeSlug(ctx context.Context, slug string) (*network.TargetInfo, *network.Exposure, error) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		return nil, nil, fmt.Errorf("store not available")
	}
	pf, err := s.vmMgr.GetStore().GetPortForwardBySubdomain(ctx, slug)
	if err != nil {
		return nil, nil, err
	}
	if pf == nil {
		return nil, nil, fmt.Errorf("serve %q not found", slug)
	}
	return s.resolveServeTarget(ctx, pf)
}

func (s *Server) ResolveServeExposure(ctx context.Context, id string) (*network.TargetInfo, *network.Exposure, error) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		return nil, nil, fmt.Errorf("store not available")
	}
	pf, err := s.vmMgr.GetStore().GetPortForward(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if pf == nil {
		return nil, nil, fmt.Errorf("serve %q not found", id)
	}
	return s.resolveServeTarget(ctx, pf)
}

func (s *Server) resolveServeTarget(ctx context.Context, pf *db.PortForward) (*network.TargetInfo, *network.Exposure, error) {
	st := s.vmMgr.GetStore()
	v, err := st.GetVMByOwner(ctx, pf.OwnerID, pf.VMID)
	if err != nil || v == nil {
		v, _ = st.GetVM(ctx, pf.VMID)
	}
	if v == nil && pf.VMName != "" {
		v, _ = st.GetVM(ctx, pf.VMName)
	}
	if v == nil {
		return nil, nil, fmt.Errorf("sandbox for exposure %q not found", pf.ID)
	}
	return &network.TargetInfo{
		ID:        v.ID,
		Name:      v.Name,
		OwnerID:   v.OwnerID,
		Status:    string(v.Status),
		IPAddress: v.IPAddress,
		WorkerID:  v.WorkerID,
	}, &network.Exposure{
		ID:        pf.ID,
		OwnerID:   pf.OwnerID,
		Slug:      pf.Subdomain,
		GuestPort: pf.GuestPort,
		Public:    pf.Public,
	}, nil
}

// ===== Private-serve authentication =====

// handleServeAuthorize is the daemon-side hop for private serves. The browser
// already has a daemon session; this mints a short-lived exposure token and
// bounces back to the serve origin, which stores it as a cookie.
func (s *Server) handleServeAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	next := strings.TrimSpace(r.URL.Query().Get("next"))
	target, err := url.Parse(next)
	if err != nil || target.Host == "" {
		httpError(w, "next must be an absolute serve URL", http.StatusBadRequest)
		return
	}
	if !s.isServeHost(target.Hostname()) && !strings.EqualFold(target.Hostname(), requestHostname(r)) {
		httpError(w, "next is not a serve URL of this daemon", http.StatusBadRequest)
		return
	}
	pf, err := s.serveExposureForURL(r.Context(), target)
	if err != nil || pf == nil {
		httpError(w, "sandbox exposure not found", http.StatusNotFound)
		return
	}
	if !s.canAccessOwner(r, pf.OwnerID) {
		httpError(w, "forbidden", http.StatusForbidden)
		return
	}
	if !pf.Public {
		token := network.SignServeToken(s.serveTokenKey, pf.ID, pf.OwnerID, network.ServeTokenTTL)
		q := target.Query()
		q.Set(network.ServeTokenQueryParam, token)
		target.RawQuery = q.Encode()
	}
	http.Redirect(w, r, target.String(), http.StatusFound)
}

// handleServeToken returns a ready-to-open URL for one exposed guest port. It
// lets the CLI and SDKs hand out private links without embedding long-lived
// credentials in URLs.
func (s *Server) handleServeToken(w http.ResponseWriter, r *http.Request, v *db.VM) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil || v == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	guestPort := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("port")); raw != "" {
		guestPort, _ = strconv.Atoi(raw)
	}
	if guestPort == 0 && r.Body != nil {
		var req struct {
			Port      int `json:"port"`
			GuestPort int `json:"guest_port"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.GuestPort > 0 {
			guestPort = req.GuestPort
		} else if req.Port > 0 {
			guestPort = req.Port
		}
	}
	pfs, err := s.vmMgr.GetStore().ListPortForwardsForVM(r.Context(), v.OwnerID, v.ID, v.Name)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, pf := range pfs {
		if pf == nil || pf.GuestPort != guestPort {
			continue
		}
		serveURL := pf.PublicURL
		expires := ""
		if serveURL == "" {
			serveURL = s.serveURLFor(pf.Subdomain, pf.HostPort)
		}
		if !pf.Public {
			token := network.SignServeToken(s.serveTokenKey, pf.ID, pf.OwnerID, network.ServeTokenTTL)
			u, perr := url.Parse(serveURL)
			if perr == nil {
				q := u.Query()
				q.Set(network.ServeTokenQueryParam, token)
				u.RawQuery = q.Encode()
				serveURL = u.String()
			}
			expires = time.Now().Add(network.ServeTokenTTL).UTC().Format(time.RFC3339)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"url":        serveURL,
			"public_url": pf.PublicURL,
			"guest_port": pf.GuestPort,
			"public":     pf.Public,
			"expires_at": expires,
		})
		return
	}
	httpError(w, fmt.Sprintf("port %d is not exposed on sandbox %s", guestPort, v.Name), http.StatusNotFound)
}

// serveExposureForURL maps an absolute serve URL back to its exposure record.
func (s *Server) serveExposureForURL(ctx context.Context, u *url.URL) (*db.PortForward, error) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil || u == nil {
		return nil, fmt.Errorf("store not available")
	}
	st := s.vmMgr.GetStore()
	if s.cfg.ServeMode() == "subdomain" {
		domain := s.cfg.ServeDomainResolved()
		host := strings.ToLower(u.Hostname())
		if strings.HasSuffix(host, "."+domain) {
			slug := strings.TrimSuffix(host, "."+domain)
			if slug != "" && !strings.Contains(slug, ".") {
				pf, err := st.GetPortForwardBySubdomain(ctx, slug)
				if err == nil && pf != nil {
					return pf, nil
				}
			}
		}
		return s.servePreviewExposureForPath(ctx, st, u)
	}
	port, err := strconv.Atoi(u.Port())
	if err == nil && port > 0 {
		pfs, lerr := st.ListPortForwards(ctx)
		if lerr != nil {
			return nil, lerr
		}
		for _, pf := range pfs {
			if pf != nil && pf.HostPort == port {
				return pf, nil
			}
		}
	}
	return s.servePreviewExposureForPath(ctx, st, u)
}

// servePreviewExposureForPath resolves a path-based preview URL
// ("https://host/preview/<sandbox>/<port>/") to an exposure. The ID matches the
// synthetic exposure ID the ingress proxy derives for path routes so minted
// serve tokens verify correctly.
func (s *Server) servePreviewExposureForPath(ctx context.Context, st *db.Store, u *url.URL) (*db.PortForward, error) {
	if s.ingressProxy == nil || u == nil {
		return nil, fmt.Errorf("not a serve host")
	}
	match := s.ingressProxy.ParseRoute(&http.Request{Host: u.Host, URL: u})
	if !match.IsMatch || match.Target == "" || match.Port <= 0 {
		return nil, fmt.Errorf("not a serve host")
	}
	var target *network.TargetInfo
	var err error
	if match.Owner != "" {
		target, err = s.ResolveTargetForOwner(ctx, match.Target, match.Owner)
	} else {
		target, err = s.ResolveTarget(ctx, match.Target)
	}
	if err != nil || target == nil {
		return nil, fmt.Errorf("sandbox %q not found", match.Target)
	}
	pf := &db.PortForward{
		ID:        fmt.Sprintf("preview:%s:%d", target.ID, match.Port),
		OwnerID:   target.OwnerID,
		VMID:      target.ID,
		VMName:    target.Name,
		GuestPort: match.Port,
		Public:    false,
	}
	if pfs, lerr := st.ListPortForwardsForVM(ctx, target.OwnerID, target.ID, target.Name); lerr == nil {
		for _, existing := range pfs {
			if existing == nil || existing.GuestPort != match.Port {
				continue
			}
			pf.Public = existing.Public
			pf.HostPort = existing.HostPort
			pf.Subdomain = existing.Subdomain
			pf.PublicURL = existing.PublicURL
			break
		}
	}
	return pf, nil
}

// requestHostname returns the externally visible request hostname, preferring
// X-Forwarded-Host (set by reverse proxies such as Tailscale Serve).
func requestHostname(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if idx := strings.IndexByte(host, ','); idx >= 0 {
		host = strings.TrimSpace(host[:idx])
	}
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}

func (s *Server) isServeHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if s.cfg.ServeMode() == "subdomain" {
		domain := s.cfg.ServeDomainResolved()
		return host == domain || strings.HasSuffix(host, "."+domain)
	}
	if pub := strings.ToLower(s.cfg.Server.PublicURLHost()); pub != "" && host == pub {
		return true
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	listen := strings.ToLower(s.cfg.Server.ListenHost())
	return listen != "" && host == listen
}

// authorizeBaseURL is where browsers are sent to authenticate private serves.
func (s *Server) authorizeBaseURL() string {
	if u := s.cfg.Server.NormalizedPublicURL(); u != "" {
		return u
	}
	host := s.cfg.Server.ListenHost()
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	scheme := "http"
	if strings.TrimSpace(s.cfg.Server.TLSCertFile) != "" {
		scheme = "https"
	}
	addr := host
	if port := s.apiListenPort(); port != "" {
		addr = net.JoinHostPort(host, port)
	}
	return scheme + "://" + addr
}

// ===== Port-mode listeners =====

type servePortRegistry struct {
	mu      sync.Mutex
	server  *Server
	entries map[string]*servePortEntry
}

type servePortEntry struct {
	port int
	srv  *http.Server
	ln   net.Listener
}

func newServePortRegistry(s *Server) *servePortRegistry {
	return &servePortRegistry{server: s, entries: make(map[string]*servePortEntry)}
}

// ensure starts (or reuses) the listener dedicated to one exposure and returns
// its port. The exposure record must already exist in the store.
func (r *servePortRegistry) ensure(pf *db.PortForward) (int, error) {
	if pf == nil || pf.ID == "" {
		return 0, fmt.Errorf("exposure id required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.entries[pf.ID]; e != nil {
		if pf.HostPort <= 0 || e.port == pf.HostPort {
			return e.port, nil
		}
		// The explicitly requested port changed: drop the old listener and
		// reopen on the new port below.
		delete(r.entries, pf.ID)
		_ = e.srv.Close()
		_ = e.ln.Close()
	}

	start, end := r.server.cfg.ServePortRange()
	bind := strings.TrimSpace(r.server.cfg.Server.ServeBind)
	if bind == "" {
		if lh := strings.TrimSpace(r.server.cfg.Server.ListenHost()); lh != "" && !isLocalServeHost(lh) {
			bind = lh
		} else {
			bind = "127.0.0.1"
		}
	}
	// Honor an explicitly requested host port before scanning the range.
	if pf.HostPort > 0 && !r.portTakenLocked(pf.HostPort) {
		if port, err := r.listenLocked(pf, bind, pf.HostPort); err == nil {
			return port, nil
		}
	}
	for port := start; port <= end; port++ {
		if r.portTakenLocked(port) {
			continue
		}
		if port, err := r.listenLocked(pf, bind, port); err == nil {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free serve port in %d-%d (increase COMPUTE_SERVE_PORT_END)", start, end)
}

// listenLocked starts the exposure listener on one port. Callers hold r.mu.
func (r *servePortRegistry) listenLocked(pf *db.PortForward, bind string, port int) (int, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(port)))
	if err != nil {
		return 0, err
	}
	id := pf.ID
	handler := http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
		if r.server.ingressProxy == nil {
			http.Error(w, "ingress proxy not initialized", http.StatusInternalServerError)
			return
		}
		r.server.ingressProxy.ServeExposure(w, rq, id)
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	// WebSocket upgrades require HTTP/1.1 hijacking; disable HTTP/2 on
	// serve listeners so browsers do not negotiate extended CONNECT.
	srv.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))
	r.entries[id] = &servePortEntry{port: port, srv: srv, ln: ln}
	cert, key := r.server.serveTLSPaths()
	go func() {
		var err error
		if cert != "" && key != "" {
			err = srv.ServeTLS(ln, cert, key)
		} else {
			err = srv.Serve(ln)
		}
		if err != nil && err != http.ErrServerClosed {
			log.Printf("[computed] serve listener :%d for %s stopped: %v", port, id, err)
		}
	}()
	return port, nil
}

func (r *servePortRegistry) portTakenLocked(port int) bool {
	for _, e := range r.entries {
		if e.port == port {
			return true
		}
	}
	return false
}

func (r *servePortRegistry) release(id string) {
	if id == "" {
		return
	}
	r.mu.Lock()
	entry := r.entries[id]
	delete(r.entries, id)
	r.mu.Unlock()
	if entry != nil {
		_ = entry.srv.Close()
		_ = entry.ln.Close()
	}
}

// restore reopens listeners for persisted port-mode exposures after a restart.
func (r *servePortRegistry) restore(ctx context.Context) {
	if r.server.vmMgr == nil || r.server.vmMgr.GetStore() == nil {
		return
	}
	pfs, err := r.server.vmMgr.GetStore().ListPortForwards(ctx)
	if err != nil {
		return
	}
	for _, pf := range pfs {
		// Only records that own a listener port belong here: port-mode
		// exposures, or subdomain exposures with an explicit host port.
		if pf == nil || pf.HostPort <= 0 {
			continue
		}
		port, err := r.ensure(pf)
		if err != nil {
			log.Printf("[computed] could not restore serve listener for %s: %v", pf.ID, err)
			continue
		}
		wantURL := r.server.serveURLFor(pf.Subdomain, port)
		if port != pf.HostPort || pf.PublicURL != wantURL {
			pf.HostPort = port
			pf.PublicURL = wantURL
			if err := r.server.vmMgr.GetStore().SavePortForward(ctx, pf); err != nil {
				log.Printf("[computed] could not update restored serve %s: %v", pf.ID, err)
			}
		}
	}
}

func (r *servePortRegistry) close() {
	r.mu.Lock()
	entries := r.entries
	r.entries = make(map[string]*servePortEntry)
	r.mu.Unlock()
	for _, e := range entries {
		_ = e.srv.Close()
		_ = e.ln.Close()
	}
}

func (s *Server) serveTLSPaths() (string, string) {
	cert := strings.TrimSpace(s.cfg.Server.ServeTLSCertFile)
	key := strings.TrimSpace(s.cfg.Server.ServeTLSKeyFile)
	if cert == "" {
		cert = strings.TrimSpace(s.cfg.Server.TLSCertFile)
	}
	if key == "" {
		key = strings.TrimSpace(s.cfg.Server.TLSKeyFile)
	}
	return cert, key
}
