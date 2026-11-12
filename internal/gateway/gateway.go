// Package gateway exposes sandboxes that live on the daemon's local bridge as
// Tailscale Services.
//
// Only the machine running the daemon joins the tailnet. For every sandbox the
// gateway defines one Tailscale Service (a stable VIP plus MagicDNS name),
// allocates a local TCP listener that proxies to the guest, and configures
// `tailscale serve` so tailscaled forwards Service traffic to that listener.
// Creating or deleting a sandbox therefore creates or deletes a tailnet
// address without enrolling the sandbox itself.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/tailscale"
)

// AdminAPI is the subset of the Tailscale admin client the gateway needs.
type AdminAPI interface {
	EnsureService(ctx context.Context, name string, ports, tags []string) error
	GetService(ctx context.Context, name string) (*tailscale.Service, error)
	DeleteService(ctx context.Context, name string) error
	SetServiceHostApproval(ctx context.Context, name, deviceID string, approved bool) error
}

// ServeAPI configures Service endpoints on the local tailscaled.
type ServeAPI interface {
	AdvertiseTCP(ctx context.Context, service string, port int, target string) error
	RemoveTCP(ctx context.Context, service string, port int) error
	ClearService(ctx context.Context, service string) error
	LocalDeviceID(ctx context.Context) (string, error)
}

// SandboxSpec is the desired state for one sandbox.
type SandboxSpec struct {
	ID         string
	Name       string
	InternalID string
	GuestIP    string
	Running    bool
	// Ports are the guest TCP ports that should be reachable through the
	// Service. The gateway always includes SSH (22) unless it is excluded.
	Ports []int
}

// ServiceInfo is the observed state of one sandbox Service.
type ServiceInfo struct {
	VMID      string   `json:"vm_id"`
	Sandbox   string   `json:"sandbox"`
	Service   string   `json:"service"` // without the svc: prefix
	VIPs      []string `json:"vips,omitempty"`
	Ports     []int    `json:"ports,omitempty"`
	Status    string   `json:"status"`
	LastError string   `json:"last_error,omitempty"`
}

// Options configures the gateway manager.
type Options struct {
	ServicePrefix string
	ServiceTag    string
	TargetHost    string // address tailscaled dials; empty auto-detects
	ExcludePorts  map[int]bool
	IncludeSSH    bool
	AutoApprove   bool
	Logger        *log.Logger
}

// Manager reconciles sandbox Services and owns the local proxy listeners.
type Manager struct {
	opts  Options
	admin AdminAPI
	serve ServeAPI

	syncMu  sync.Mutex // serialises Sync calls
	mu      sync.RWMutex
	state   map[string]*sandboxState // keyed by VM ID
	target  string
	lastAt  time.Time
	lastErr string

	deviceMu  sync.Mutex
	deviceID  string
	deviceErr error
}

type sandboxState struct {
	spec       SandboxSpec
	service    string
	vips       []string
	proxies    map[int]*tcpProxy
	advertised map[int]string // guest port -> serve target
	approved   bool
	lastErr    string
}

// NewManager builds a gateway manager. TargetHost is resolved once so every
// Service endpoint uses the same reachable address.
func NewManager(opts Options, admin AdminAPI, serve ServeAPI) *Manager {
	if opts.ServicePrefix == "" {
		opts.ServicePrefix = "compute"
	}
	if opts.ServiceTag == "" {
		opts.ServiceTag = tailscale.TagSandbox
	}
	if opts.ExcludePorts == nil {
		opts.ExcludePorts = map[int]bool{}
	}
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	target := strings.TrimSpace(opts.TargetHost)
	if target == "" {
		target = detectTargetHost()
	}
	return &Manager{
		opts:   opts,
		admin:  admin,
		serve:  serve,
		state:  make(map[string]*sandboxState),
		target: target,
	}
}

// Target returns the address tailscaled dials to reach the local listeners.
func (m *Manager) Target() string { return m.target }

// Sync reconciles the tailnet with the given sandboxes. It is safe to call
// repeatedly; only differences are applied. Errors are recorded per sandbox
// and never abort the whole reconcile.
func (m *Manager) Sync(ctx context.Context, specs []SandboxSpec) error {
	if m == nil {
		return nil
	}
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	desired := make(map[string]SandboxSpec, len(specs))
	for _, spec := range specs {
		if strings.TrimSpace(spec.ID) == "" {
			continue
		}
		desired[spec.ID] = spec
	}

	var errs []error
	seen := make(map[string]bool, len(desired))
	for id, spec := range desired {
		seen[id] = true
		if err := m.syncOne(ctx, spec); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			m.setError(id, err)
		}
	}
	// Remove Services for sandboxes that no longer exist or stopped running.
	m.mu.RLock()
	var stale []string
	for id := range m.state {
		if !seen[id] {
			stale = append(stale, id)
		}
	}
	m.mu.RUnlock()
	for _, id := range stale {
		if err := m.remove(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("%s: cleanup: %w", id, err))
		}
	}

	m.mu.Lock()
	m.lastAt = time.Now()
	if len(errs) > 0 {
		m.lastErr = strings.Join(errorStrings(errs), "; ")
	} else {
		m.lastErr = ""
	}
	m.mu.Unlock()
	return errors.Join(errs...)
}

func (m *Manager) syncOne(ctx context.Context, spec SandboxSpec) error {
	ports := m.desiredPorts(spec)
	if !spec.Running || !validGuestIP(spec.GuestIP) || len(ports) == 0 {
		return m.remove(ctx, spec.ID)
	}

	service := ServiceName(m.opts.ServicePrefix, spec.Name, spec.InternalID)
	if service == "" {
		return fmt.Errorf("cannot derive a service name")
	}

	m.mu.RLock()
	prev := m.state[spec.ID]
	var prevService string
	var prevPorts map[int]string
	if prev != nil {
		prevService = prev.service
		prevPorts = clonePortMap(prev.advertised)
	}
	m.mu.RUnlock()

	// If the deterministic name changed (rename), remove the old Service.
	if prevService != "" && prevService != service {
		if err := m.teardown(ctx, spec.ID); err != nil {
			m.opts.Logger.Printf("[gateway] %s: remove old service %s: %v", spec.ID, prevService, err)
		}
		prev = nil
		prevPorts = nil
	}

	// If this is the first time we see the Service (new sandbox or daemon
	// restart), reset any stale serve endpoints left from a previous process.
	if prev == nil && prevService == "" {
		if err := m.serve.ClearService(ctx, service); err != nil {
			m.opts.Logger.Printf("[gateway] %s: reset stale serve config for svc:%s: %v", spec.ID, service, err)
		}
	}

	// Drop endpoints that are no longer desired before shrinking the Service.
	for port := range prevPorts {
		if !containsPort(ports, port) {
			if err := m.serve.RemoveTCP(ctx, service, port); err != nil {
				m.opts.Logger.Printf("[gateway] %s: remove tcp:%d: %v", spec.ID, port, err)
			}
		}
	}

	portStrings := make([]string, 0, len(ports))
	for _, p := range ports {
		portStrings = append(portStrings, fmt.Sprintf("tcp:%d", p))
	}
	if err := m.admin.EnsureService(ctx, service, portStrings, []string{m.opts.ServiceTag}); err != nil {
		return fmt.Errorf("ensure service svc:%s: %w", service, err)
	}

	vips := []string(nil)
	if svc, err := m.admin.GetService(ctx, service); err == nil && svc != nil {
		vips = svc.Addrs
	} else if err != nil {
		m.opts.Logger.Printf("[gateway] %s: read back service svc:%s: %v", spec.ID, service, err)
	}

	// Create/refresh one local proxy per guest port.
	proxies := make(map[int]*tcpProxy, len(ports))
	advertised := make(map[int]string, len(ports))
	created := make(map[int]*tcpProxy)
	var cleanup sync.Once
	cleanupNew := func() {
		cleanup.Do(func() {
			for _, px := range created {
				px.close()
			}
		})
	}
	for _, port := range ports {
		px, isNew, err := m.ensureProxy(spec.ID, port, spec.GuestIP, port)
		if err != nil {
			cleanupNew()
			return fmt.Errorf("listen for guest port %d: %w", port, err)
		}
		if isNew {
			created[port] = px
		}
		proxies[port] = px
		target := net.JoinHostPort(m.target, fmt.Sprint(px.port))
		if prevPorts[port] != target {
			if err := m.serve.AdvertiseTCP(ctx, service, port, target); err != nil {
				cleanupNew()
				return fmt.Errorf("advertise tcp:%d: %w", port, err)
			}
		}
		advertised[port] = target
	}

	approved := prev != nil && prev.approved
	if m.opts.AutoApprove && !approved {
		if id := m.localDeviceID(ctx); id != "" {
			if err := m.admin.SetServiceHostApproval(ctx, service, id, true); err != nil {
				m.opts.Logger.Printf("[gateway] %s: auto-approve service host failed (apply the policy snippet or approve in the console): %v", spec.ID, err)
			} else {
				approved = true
			}
		}
	}

	m.mu.Lock()
	old := m.state[spec.ID]
	m.state[spec.ID] = &sandboxState{
		spec:       spec,
		service:    service,
		vips:       vips,
		proxies:    proxies,
		advertised: advertised,
		approved:   approved,
	}
	m.mu.Unlock()

	// Close listeners that are no longer used.
	if old != nil {
		for port, px := range old.proxies {
			if _, keep := proxies[port]; !keep {
				px.close()
			}
		}
	}
	return nil
}

// remove tears down one sandbox's local listeners, serve endpoints, and Service.
func (m *Manager) remove(ctx context.Context, vmID string) error {
	return m.teardown(ctx, vmID)
}

func (m *Manager) teardown(ctx context.Context, vmID string) error {
	m.mu.Lock()
	st := m.state[vmID]
	if st != nil {
		delete(m.state, vmID)
	}
	m.mu.Unlock()
	if st == nil {
		return nil
	}

	var errs []error
	for _, px := range st.proxies {
		px.close()
	}
	if m.serve != nil && st.service != "" {
		if err := m.serve.ClearService(ctx, st.service); err != nil {
			errs = append(errs, err)
		}
	}
	if m.admin != nil && st.service != "" {
		if err := m.admin.DeleteService(ctx, st.service); err != nil {
			// A Service that is already gone is not an error.
			if !strings.Contains(strings.ToLower(err.Error()), "404") &&
				!strings.Contains(strings.ToLower(err.Error()), "not found") {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Close stops forwarding and clears serve endpoints while keeping Service
// definitions (and therefore VIPs) intact for the next daemon start.
func (m *Manager) Close(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.RLock()
	states := make([]*sandboxState, 0, len(m.state))
	for _, st := range m.state {
		states = append(states, st)
	}
	m.mu.RUnlock()
	for _, st := range states {
		for _, px := range st.proxies {
			px.close()
		}
		if m.serve != nil && st.service != "" {
			_ = m.serve.ClearService(ctx, st.service)
		}
	}
}

// VIPs returns the Service VIPs for a sandbox (empty when none is allocated).
func (m *Manager) VIPs(vmID string) []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if st := m.state[vmID]; st != nil {
		return append([]string(nil), st.vips...)
	}
	for _, st := range m.state {
		if st.spec.InternalID == vmID || st.spec.Name == vmID {
			return append([]string(nil), st.vips...)
		}
	}
	return nil
}

// ServiceNameFor returns the active Service name for a sandbox, or "".
func (m *Manager) ServiceNameFor(vmID string) string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if st := m.state[vmID]; st != nil {
		return st.service
	}
	for _, st := range m.state {
		if st.spec.InternalID == vmID || st.spec.Name == vmID {
			return st.service
		}
	}
	return ""
}

// Snapshots returns the current state of every managed Service.
func (m *Manager) Snapshots() []ServiceInfo {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ServiceInfo, 0, len(m.state))
	for _, st := range m.state {
		info := ServiceInfo{
			VMID:      st.spec.ID,
			Sandbox:   st.spec.Name,
			Service:   st.service,
			VIPs:      append([]string(nil), st.vips...),
			Status:    "active",
			LastError: st.lastErr,
		}
		for p := range st.advertised {
			info.Ports = append(info.Ports, p)
		}
		sort.Ints(info.Ports)
		if len(info.VIPs) == 0 {
			info.Status = "pending"
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sandbox < out[j].Sandbox })
	return out
}

// Status reports manager-level state.
type Status struct {
	Enabled       bool   `json:"enabled"`
	TargetAddress string `json:"target_address"`
	LastSync      string `json:"last_sync,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	Services      int    `json:"services"`
}

// Status returns a summary for the API.
func (m *Manager) Status() Status {
	if m == nil {
		return Status{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := Status{Enabled: true, TargetAddress: m.target, LastError: m.lastErr, Services: len(m.state)}
	if !m.lastAt.IsZero() {
		st.LastSync = m.lastAt.UTC().Format(time.RFC3339)
	}
	return st
}

func (m *Manager) ensureProxy(vmID string, guestPort int, guestIP string, port int) (*tcpProxy, bool, error) {
	key := vmID
	m.mu.RLock()
	var existing *tcpProxy
	if st := m.state[key]; st != nil {
		existing = st.proxies[guestPort]
	}
	m.mu.RUnlock()
	if existing != nil && existing.guest == net.JoinHostPort(guestIP, fmt.Sprint(port)) {
		return existing, false, nil
	}
	if existing != nil {
		existing.close()
	}
	px, err := startProxy(net.JoinHostPort(guestIP, fmt.Sprint(port)), m.target)
	if err != nil {
		return nil, false, err
	}
	return px, true, nil
}

func (m *Manager) desiredPorts(spec SandboxSpec) []int {
	set := make(map[int]bool, len(spec.Ports)+1)
	for _, p := range spec.Ports {
		if p > 0 && p <= 65535 && !m.opts.ExcludePorts[p] {
			set[p] = true
		}
	}
	if m.opts.IncludeSSH && !m.opts.ExcludePorts[22] {
		set[22] = true
	}
	out := make([]int, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

func (m *Manager) localDeviceID(ctx context.Context) string {
	m.deviceMu.Lock()
	defer m.deviceMu.Unlock()
	if m.deviceID != "" {
		return m.deviceID
	}
	if m.serve == nil {
		return ""
	}
	id, err := m.serve.LocalDeviceID(ctx)
	if err != nil {
		m.deviceErr = err
		return ""
	}
	m.deviceID = id
	m.deviceErr = nil
	return id
}

func (m *Manager) setError(vmID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.state[vmID]; st != nil {
		st.lastErr = err.Error()
	}
}

// ServiceName builds a deterministic, DNS-safe Service name. The short
// internal ID suffix keeps names unique across users and renames.
func ServiceName(prefix, sandbox, internalID string) string {
	if prefix == "" {
		prefix = "compute"
	}
	base := sanitizeLabel(prefix)
	if base == "" {
		base = "compute"
	}
	name := sanitizeLabel(sandbox)
	if name == "" {
		name = "sandbox"
	}
	suffix := shortID(internalID)
	if suffix == "" {
		suffix = shortID(sandbox)
	}
	// DNS label limit is 63 characters; leave room for "prefix-name-suffix".
	maxName := 63 - len(base) - len(suffix) - 2
	if maxName < 1 {
		maxName = 1
	}
	if len(name) > maxName {
		name = strings.Trim(name[:maxName], "-")
	}
	out := strings.Trim(base+"-"+name+"-"+suffix, "-")
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	return out
}

func sanitizeLabel(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
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
	return strings.Trim(b.String(), "-")
}

func shortID(id string) string {
	clean := sanitizeLabel(id)
	if clean == "" {
		return ""
	}
	// Prefer the trailing hex from "ins_<hex>" style IDs.
	parts := strings.Split(clean, "-")
	last := parts[len(parts)-1]
	if len(last) > 10 {
		last = last[len(last)-10:]
	}
	if last == "" {
		return "x"
	}
	return last
}

func validGuestIP(ip string) bool {
	v := net.ParseIP(strings.TrimSpace(ip))
	return v != nil && v.To4() != nil && !v.IsLoopback() && !v.IsUnspecified()
}

func containsPort(ports []int, port int) bool {
	for _, p := range ports {
		if p == port {
			return true
		}
	}
	return false
}

func clonePortMap(in map[int]string) map[int]string {
	out := make(map[int]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func errorStrings(errs []error) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		if e != nil {
			out = append(out, e.Error())
		}
	}
	return out
}

// detectTargetHost finds an address the host's tailscaled can dial. In the
// container deployment this is the container's eth0 address; on a bare host it
// is the primary LAN address.
func detectTargetHost() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		ip := ipnet.IP
		if ip.IsLinkLocalUnicast() {
			continue
		}
		return ip.String()
	}
	return "127.0.0.1"
}
