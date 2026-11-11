package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/dns"
	"github.com/Rashid2G/on-demand-vms/internal/master/auth"
	storedb "github.com/Rashid2G/on-demand-vms/internal/store/db"
	"github.com/Rashid2G/on-demand-vms/internal/tailscale"
)

// dnsRuntime tracks the DNS responder lifecycle and state for /api/v1/dns/status.
type dnsRuntime struct {
	mu      sync.Mutex
	enabled bool
	listen  string
	zones   []string
	hostIP  string
	lastErr string
	server  *dns.Server
}

// startDNS launches the authoritative DNS responder for the configured base
// domains. Failures are logged and reflected in the status endpoint but never
// take the daemon down.
func (s *Server) startDNS() {
	zones := s.cfg.DNSBaseDomains()
	listen := strings.TrimSpace(s.cfg.DNS.Listen)
	if listen == "" {
		listen = ":53"
	}
	rt := &dnsRuntime{enabled: s.cfg.DNS.Enabled, listen: listen, zones: zones}
	s.dns = rt
	if !s.cfg.DNS.Enabled || len(zones) == 0 {
		return
	}

	rt.server = dns.NewServer(zones, s)
	// Warm the host address cache so first queries are fast.
	go s.dnsHostAddresses()

	errCh := make(chan error, 2)
	go func() { errCh <- rt.server.ListenAndServeUDP(listen) }()
	go func() { errCh <- rt.server.ListenAndServeTCP(listen) }()

	go func() {
		err := <-errCh
		rt.mu.Lock()
		rt.lastErr = err.Error()
		rt.mu.Unlock()
		log.Printf("[computed] dns responder on %s stopped: %v", listen, err)
	}()
	log.Printf("[computed] dns responder listening on %s for zones %v (sandbox names: <sandbox>.<username>.<zone>)", listen, zones)
}

// ResolveA implements dns.Resolver for names inside the configured zones.
//
// Zone layout:
//
//	<zone>                      → the host itself (GUI/API entry point)
//	api.<zone>, gui.<zone>, …   → the host itself (reserved service labels)
//	<sandbox>.<username>.<zone> → the sandbox's own addresses (tailnet IP
//	                              first, guest IP second) so SSH connects
//	                              directly to the sandbox
func (s *Server) ResolveA(ctx context.Context, qname string) ([]net.IP, bool) {
	for _, zone := range s.dns.zones {
		labels, ok := dns.SplitZoneLabels(qname, zone)
		if !ok {
			continue
		}
		// Zone apex or a reserved service label: the host itself.
		if len(labels) == 0 || (len(labels) == 1 && dns.IsReservedServiceLabel(labels[0])) {
			ips, ok := s.dnsHostAddresses()
			if !ok {
				return nil, false
			}
			return ips, true
		}
		if len(labels) < 2 {
			return nil, false
		}
		sandbox, username, ok := dns.ParseSandboxName(labels)
		if !ok {
			return nil, false
		}
		return s.dnsSandboxIPs(ctx, sandbox, username)
	}
	return nil, false
}

// dnsSandboxIPs resolves "<sandbox>.<username>" to the sandbox's routable
// addresses. When authentication is disabled every caller is the legacy
// "default" tenant and the username label is accepted but ignored.
func (s *Server) dnsSandboxIPs(ctx context.Context, sandbox, username string) ([]net.IP, bool) {
	var ownerIDs []string
	if s.authEnabled() {
		user, ok := s.dnsUserForLabel(username)
		if !ok {
			return nil, false
		}
		ownerIDs = []string{user.ID, auth.PersonalOrgID(user.ID)}
	} else {
		ownerIDs = []string{"default"}
	}

	for _, vm := range s.listVMsForOwnersCached(ctx, ownerIDs) {
		if sandboxLabelForVM(vm) != sandbox {
			continue
		}
		return sandboxAddresses(vm), true
	}
	return nil, false
}

// sandboxLabelForVM returns the DNS label used for a VM inside its owner's
// zone. Legacy names that predate DNS-safe validation are sanitized the same
// way here and at hostname display time, so old sandboxes stay reachable.
func sandboxLabelForVM(v *db.VM) string {
	if name := strings.TrimSpace(v.Name); dns.ValidLabel(strings.ToLower(name)) {
		return strings.ToLower(name)
	}
	return dns.SanitizeLabel(v.Name, dns.MaxLabelLength)
}

// sandboxAddresses returns the addresses a sandbox name resolves to. The
// tailnet address comes first: it is reachable from every tailnet device
// without subnet routes, while the guest IP requires routing (tailnet subnet
// route or LAN route) but works for on-net clients. Unroutable first
// addresses fail fast with "network unreachable", letting OpenSSH fall
// through to the next address.
func sandboxAddresses(v *db.VM) []net.IP {
	var out []net.IP
	if v.TailnetIP != "" {
		if ip := net.ParseIP(v.TailnetIP); ip != nil && ip.To4() != nil {
			out = append(out, ip)
		}
	}
	if v.IPAddress != "" && v.IPAddress != "dhcp" {
		if ip := net.ParseIP(v.IPAddress); ip != nil && ip.To4() != nil {
			out = append(out, ip)
		}
	}
	return out
}

// dnsUserForLabel maps a username DNS label back to a user account. Exact
// usernames win; otherwise the sanitized-username index is used so legacy
// usernames remain addressable.
func (s *Server) dnsUserForLabel(label string) (*storedb.User, bool) {
	if label == "" {
		return nil, false
	}

	s.userIndexMu.RLock()
	index := s.userIndex
	expiry := s.userIndexExpiry
	s.userIndexMu.RUnlock()

	if index == nil || time.Now().After(expiry) {
		users, err := s.authMgr.Users(context.Background())
		if err != nil {
			log.Printf("[computed] dns: user index refresh failed: %v", err)
			return nil, false
		}
		index = make(map[string]*storedb.User, len(users))
		for _, u := range users {
			if u == nil || u.Username == "" {
				continue
			}
			if dns.ValidLabel(u.Username) {
				index[strings.ToLower(u.Username)] = u
			}
			index[dns.SanitizeLabel(u.Username, dns.MaxLabelLength)] = u
		}
		s.userIndexMu.Lock()
		s.userIndex = index
		s.userIndexExpiry = time.Now().Add(30 * time.Second)
		s.userIndexMu.Unlock()
	}

	if user, ok := index[strings.ToLower(label)]; ok && user != nil {
		return user, true
	}
	// Fall back to a direct lookup (covers usernames written since the index
	// was cached, including email-style identifiers).
	if user, err := s.authMgr.UserByUsername(context.Background(), label); err == nil && user != nil {
		if dns.ValidLabel(user.Username) {
			return user, true
		}
		if dns.SanitizeLabel(user.Username, dns.MaxLabelLength) == strings.ToLower(label) {
			return user, true
		}
	}
	return nil, false
}

// listVMsForOwnersCached memoizes per-owner VM listings for a few seconds so
// DNS query bursts do not hammer SQLite.
func (s *Server) listVMsForOwnersCached(ctx context.Context, ownerIDs []string) []*db.VM {
	key := strings.Join(ownerIDs, "|")

	s.vmListMu.RLock()
	if entry, ok := s.vmListCache[key]; ok && time.Now().Before(entry.expiry) {
		vms := entry.vms
		s.vmListMu.RUnlock()
		return vms
	}
	s.vmListMu.RUnlock()

	vms := s.listVMsForOwners(ctx, ownerIDs)
	s.vmListMu.Lock()
	s.vmListCache[key] = vmListEntry{vms: vms, expiry: time.Now().Add(2 * time.Second)}
	s.vmListMu.Unlock()
	return vms
}

type vmListEntry struct {
	vms    []*db.VM
	expiry time.Time
}

// dnsHostAddresses returns the addresses the host itself should answer with
// for the zone apex and reserved service labels. Explicit dns.host_addresses
// win; otherwise the tailnet IP (the address the GUI/API hostnames are
// advertised on) is preferred, then the primary outbound address. Cached for
// a minute.
func (s *Server) dnsHostAddresses() ([]net.IP, bool) {
	s.dnsHostMu.RLock()
	if s.dnsHostIPs != nil && time.Now().Before(s.dnsHostExpiry) {
		ips := s.dnsHostIPs
		s.dnsHostMu.RUnlock()
		return ips, true
	}
	s.dnsHostMu.RUnlock()

	var ips []net.IP
	for _, a := range s.cfg.DNS.HostAddresses {
		if ip := net.ParseIP(strings.TrimSpace(a)); ip != nil && ip.To4() != nil {
			ips = append(ips, ip)
		}
	}
	source := "config"
	if len(ips) == 0 {
		if ip, via := detectHostAddress(); ip != nil {
			ips = []net.IP{ip}
			source = via
		}
	}
	if len(ips) == 0 {
		return nil, false
	}

	s.dnsHostMu.Lock()
	s.dnsHostIPs = ips
	s.dnsHostExpiry = time.Now().Add(time.Minute)
	s.dnsHostMu.Unlock()

	strAddrs := make([]string, 0, len(ips))
	for _, ip := range ips {
		strAddrs = append(strAddrs, ip.String())
	}
	if rt := s.dns; rt != nil {
		rt.mu.Lock()
		rt.hostIP = strings.Join(strAddrs, ",")
		rt.mu.Unlock()
	}
	log.Printf("[computed] dns: host addresses %v (via %s)", strAddrs, source)
	return ips, true
}

func detectHostAddress() (net.IP, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if st, err := tailscale.DetectLocal(ctx); err == nil && st != nil && st.Running && st.TailnetIP != "" {
		if ip := net.ParseIP(st.TailnetIP); ip != nil {
			return ip, "tailscale"
		}
	}
	// Primary outbound address: dial a public address and inspect the local side.
	c, err := net.DialTimeout("udp", "8.8.8.8:53", 2*time.Second)
	if err == nil {
		defer c.Close()
		if addr, ok := c.LocalAddr().(*net.UDPAddr); ok {
			return addr.IP, "outbound-route"
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, ""
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP, "interface"
		}
	}
	return nil, ""
}

// vmHostname returns the fully qualified sandbox hostname for API responses,
// or "" when DNS naming is not configured or the owner is unknown.
func (s *Server) vmHostname(v *db.VM) string {
	zone := s.cfg.PrimaryDNSBaseDomain()
	if zone == "" || !s.cfg.DNS.Enabled || v == nil {
		return ""
	}
	if !s.authEnabled() || v.OwnerID == "" || v.OwnerID == "default" {
		return sandboxLabelForVM(v) + ".default." + zone
	}
	if strings.HasPrefix(v.OwnerID, "org_") {
		// Organization-owned resources have no single username label.
		return ""
	}
	user, err := s.authMgr.UserByID(context.Background(), v.OwnerID)
	if err != nil || user == nil || user.Username == "" {
		return ""
	}
	label := user.Username
	if !dns.ValidLabel(label) {
		label = dns.SanitizeLabel(label, dns.MaxLabelLength)
	}
	return sandboxLabelForVM(v) + "." + label + "." + zone
}

// decorateVMHostnames fills the transient Hostname field on API responses.
func (s *Server) decorateVMHostnames(vms []*db.VM) {
	zone := s.cfg.PrimaryDNSBaseDomain()
	if zone == "" || !s.cfg.DNS.Enabled || len(vms) == 0 {
		return
	}
	for _, v := range vms {
		v.Hostname = s.vmHostname(v)
	}
}

// encodeVM writes a single VM response with the transient hostname populated.
func (s *Server) encodeVM(w http.ResponseWriter, v *db.VM) {
	if v != nil {
		s.decorateVMHostnames([]*db.VM{v})
	}
	json.NewEncoder(w).Encode(v)
}

// handleDNSStatus reports the DNS responder state.
func (s *Server) handleDNSStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rt := s.dns
	if rt == nil {
		json.NewEncoder(w).Encode(map[string]any{"enabled": false})
		return
	}
	rt.mu.Lock()
	state := map[string]any{
		"enabled":    rt.enabled,
		"listen":     rt.listen,
		"zones":      rt.zones,
		"host_ip":    rt.hostIP,
		"last_error": rt.lastErr,
	}
	rt.mu.Unlock()
	if state["host_ip"] == "" {
		if ips, ok := s.dnsHostAddresses(); ok && len(ips) > 0 {
			state["host_ip"] = ips[0].String()
		}
	}
	json.NewEncoder(w).Encode(state)
}
