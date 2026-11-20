package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Rashid2G/compute/internal/db"
)

// AllocationStore defines an interface for retrieving network allocation metadata.
type AllocationStore interface {
	GetNetworkAllocation(ctx context.Context, tapOrID string) (*db.NetworkAllocation, error)
}

// Runner executes system shell commands
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// RealRunner uses os/exec
type RealRunner struct{}

func (r *RealRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// Mode defines the network topology mode
type Mode string

const (
	ModeNAT        Mode = "nat"         // Virtual bridge behind virtual NAT (for Wi-Fi or isolated host)
	ModeBridge     Mode = "bridge"      // Virtual bridge (L2 only, direct to host NIC and router)
	ModeNIC        Mode = "nic"         // Assigned to a real physical NIC via bridge (dedicated IPs on host LAN)
	ModeWired      Mode = "wired"       // Alias for 'nic' mode: wired physical connection with dedicated IPs
	ModeNoInternet Mode = "no-internet" // Air-gapped sandbox network: drops all forwarded egress traffic off-bridge
	ModeAirgapped  Mode = "airgapped"   // Alias for 'no-internet' mode
)

// Config defines network parameters
type Config struct {
	Mode          Mode     `json:"mode"`
	BridgeName    string   `json:"bridge_name"`
	BridgeIP      string   `json:"bridge_ip"`                // e.g. "192.168.1.2/24"
	SubnetCIDR    string   `json:"subnet"`                   // e.g. "192.168.1.0/24"
	PhysicalNIC   string   `json:"physical_nic,omitempty"`   // e.g. "eth0"
	GatewayIP     string   `json:"gateway_ip,omitempty"`     // e.g. "192.168.1.1" (main router)
	LinkedBridges []string `json:"linked_bridges,omitempty"` // Explicit list of bridges to link
	BaseDomain    string   `json:"base_domain,omitempty"`    // e.g. "preview.localhost", "compute.internal"

	// LinkAllBridges links every bridge found on the host to this manager's
	// primary bridge. It is opt-in (COMPUTE_LINK_OTHER_BRIDGES=1) because a
	// shared host can already run other bridges (Docker, another Compute
	// daemon/worker, libvirt). Auto-linking those creates L2 loops and MAC
	// flaps that make unrelated sandboxes unreachable.
	LinkAllBridges bool `json:"link_all_bridges,omitempty"`

	// Egress firewall settings (ADR-002: Zero-Trust Sandbox Posture)
	EgressFirewallEnabled *bool    `json:"egress_firewall_enabled,omitempty"`
	EgressDenyCIDRs       []string `json:"egress_deny_cidrs,omitempty"`
	EgressAllowCIDRs      []string `json:"egress_allow_cidrs,omitempty"`
	BlockHostBridgePorts  *bool    `json:"block_host_bridge_ports,omitempty"`
}

var (
	// DefaultEgressDenyCIDRs defines destination IP ranges blocked by default for untrusted guests.
	// Includes Link-local/Cloud IMDS (169.254.0.0/16), RFC 1918 private subnets, CGNAT/Tailscale, and loopback.
	DefaultEgressDenyCIDRs = []string{
		"169.254.0.0/16",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"100.64.0.0/10",
		"127.0.0.0/8",
	}

	// DefaultIPv6EgressDenyCIDRs defines IPv6 equivalents blocked by default if ip6tables is available.
	DefaultIPv6EgressDenyCIDRs = []string{
		"fe80::/10", // Link-local
		"fc00::/7",  // Unique Local Address (ULA)
		"::1/128",   // Loopback
	}
)

// IsEgressFirewallEnabled returns true unless explicitly disabled.
func (c Config) IsEgressFirewallEnabled() bool {
	if c.EgressFirewallEnabled != nil {
		return *c.EgressFirewallEnabled
	}
	return true
}

// IsBlockHostBridgePorts returns true unless explicitly disabled.
func (c Config) IsBlockHostBridgePorts() bool {
	if c.BlockHostBridgePorts != nil {
		return *c.BlockHostBridgePorts
	}
	return true
}

// EffectiveEgressDenyCIDRs returns the configured deny CIDRs, falling back to DefaultEgressDenyCIDRs.
func (c Config) EffectiveEgressDenyCIDRs() []string {
	if len(c.EgressDenyCIDRs) > 0 {
		return c.EgressDenyCIDRs
	}
	return append([]string{}, DefaultEgressDenyCIDRs...)
}

func defaultEgressSettings() (*bool, *bool, []string) {
	ef := true
	bp := true
	deny := append([]string{}, DefaultEgressDenyCIDRs...)
	return &ef, &bp, deny
}

// DefaultNATConfig returns standard NAT network configuration
func DefaultNATConfig() Config {
	ef, bp, deny := defaultEgressSettings()
	return Config{
		Mode:                  ModeNAT,
		BridgeName:            "mft3br0",
		BridgeIP:              "172.16.0.1/16",
		SubnetCIDR:            "172.16.0.0/16",
		GatewayIP:             "172.16.0.1",
		EgressFirewallEnabled: ef,
		BlockHostBridgePorts:  bp,
		EgressDenyCIDRs:       deny,
	}
}

// DefaultNoInternetConfig returns standard airgapped bridge network configuration
func DefaultNoInternetConfig() Config {
	ef, bp, deny := defaultEgressSettings()
	return Config{
		Mode:                  ModeNoInternet,
		BridgeName:            "mft3br0",
		BridgeIP:              "172.16.0.1/16",
		SubnetCIDR:            "172.16.0.0/16",
		GatewayIP:             "172.16.0.1",
		EgressFirewallEnabled: ef,
		BlockHostBridgePorts:  bp,
		EgressDenyCIDRs:       deny,
	}
}

// DefaultBridgeConfig returns standalone virtual bridge configuration (L2 bridged, no NAT)
func DefaultBridgeConfig() Config {
	ef, bp, deny := defaultEgressSettings()
	return Config{
		Mode:                  ModeBridge,
		BridgeName:            "mft3br0",
		BridgeIP:              "172.16.0.1/16",
		SubnetCIDR:            "172.16.0.0/16",
		GatewayIP:             "172.16.0.1",
		EgressFirewallEnabled: ef,
		BlockHostBridgePorts:  bp,
		EgressDenyCIDRs:       deny,
	}
}

// DefaultNICConfig returns physical NIC bridge configuration
func DefaultNICConfig(physicalNIC string) Config {
	if physicalNIC == "" {
		physicalNIC = "eth0"
	}
	ef, bp, deny := defaultEgressSettings()
	return Config{
		Mode:                  ModeNIC,
		BridgeName:            "mft3br0",
		BridgeIP:              "192.168.1.2/24",
		SubnetCIDR:            "192.168.1.0/24",
		GatewayIP:             "192.168.1.1",
		PhysicalNIC:           physicalNIC,
		EgressFirewallEnabled: ef,
		BlockHostBridgePorts:  bp,
		EgressDenyCIDRs:       deny,
	}
}

// DefaultWiredConfig returns configuration for a wired host network giving each VM a dedicated LAN IP
func DefaultWiredConfig(physicalNIC, subnet, gateway string) Config {
	if physicalNIC == "" {
		physicalNIC = "eth0"
	}
	if subnet == "" {
		subnet = "192.168.1.0/24"
	}
	if gateway == "" {
		gateway = "192.168.1.1"
	}
	ef, bp, deny := defaultEgressSettings()
	return Config{
		Mode:                  ModeWired,
		BridgeName:            "mft3br0",
		BridgeIP:              "192.168.1.2/24",
		SubnetCIDR:            subnet,
		PhysicalNIC:           physicalNIC,
		GatewayIP:             gateway,
		EgressFirewallEnabled: ef,
		BlockHostBridgePorts:  bp,
		EgressDenyCIDRs:       deny,
	}
}

// LoadConfigFile loads a JSON configuration file if it exists
func LoadConfigFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("invalid json in network config %s: %w", path, err)
	}
	return cfg, nil
}

// NetworkInfo provides an active snapshot of network state
type NetworkInfo struct {
	Mode           Mode     `json:"mode"`
	BridgeName     string   `json:"bridge_name"`
	BridgeIP       string   `json:"bridge_ip"`
	SubnetCIDR     string   `json:"subnet"`
	GatewayIP      string   `json:"gateway_ip"`
	Netmask        string   `json:"netmask"`
	PhysicalNIC    string   `json:"physical_nic,omitempty"`
	BridgeActive   bool     `json:"bridge_active"`
	NATEnabled     bool     `json:"nat_enabled"`
	WiredDedicated bool     `json:"wired_dedicated"`
	ActiveTAPs     []string `json:"active_taps"`
	AllocatedIPs   int      `json:"allocated_ips"`
	LinkedBridges  []string `json:"linked_bridges,omitempty"`
}

// Manager handles bridge, tap, iptables isolation, and multi-bridge linking
type Manager struct {
	cfg              Config
	ipam             *IPAM
	runner           Runner
	store            AllocationStore
	isolationConfigs map[string]IsolationConfig
	mu               sync.RWMutex
	inFlightIPs      map[string]bool
	inFlightMu       sync.Mutex
}

// NewManager creates a network manager instance
func NewManager(cfg Config, runner Runner) (*Manager, error) {
	if runner == nil {
		runner = &RealRunner{}
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeNAT
	}
	if cfg.BridgeName == "" {
		cfg.BridgeName = "mft3br0"
	}
	if cfg.BridgeIP == "" {
		cfg.BridgeIP = "192.168.1.2/24"
	}
	if cfg.SubnetCIDR == "" {
		cfg.SubnetCIDR = "192.168.1.0/24"
	}
	if cfg.GatewayIP == "" {
		cfg.GatewayIP = "192.168.1.1"
	}

	if cfg.EgressFirewallEnabled == nil {
		t := true
		cfg.EgressFirewallEnabled = &t
	}
	if cfg.BlockHostBridgePorts == nil {
		t := true
		cfg.BlockHostBridgePorts = &t
	}
	if cfg.EgressDenyCIDRs == nil && *cfg.EgressFirewallEnabled {
		cfg.EgressDenyCIDRs = append([]string{}, DefaultEgressDenyCIDRs...)
	}

	for _, cidr := range cfg.EgressDenyCIDRs {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return nil, fmt.Errorf("invalid egress deny cidr %q: %w", cidr, err)
		}
	}
	for _, cidr := range cfg.EgressAllowCIDRs {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return nil, fmt.Errorf("invalid egress allow cidr %q: %w", cidr, err)
		}
	}

	ipam, err := NewIPAM(cfg.SubnetCIDR)
	if err != nil {
		return nil, err
	}

	return &Manager{
		cfg:              cfg,
		ipam:             ipam,
		runner:           runner,
		isolationConfigs: make(map[string]IsolationConfig),
		inFlightIPs:      make(map[string]bool),
	}, nil
}

// SetStore configures the allocation store for network allocation lookups during teardown.
func (m *Manager) SetStore(store AllocationStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store = store
}

// GetConfig returns the active configuration
func (m *Manager) GetConfig() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// EnsureBridge is a backwards-compatible alias for EnsureNetwork
func (m *Manager) EnsureBridge(ctx context.Context) error {
	return m.EnsureNetwork(ctx)
}

// EnsureNetwork ensures the main host bridge, physical NIC enslavement, and linked bridges are configured
func (m *Manager) EnsureNetwork(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. Check if main bridge exists, create if missing
	out, err := m.runner.Run(ctx, "ip", "link", "show", m.cfg.BridgeName)
	if err != nil || !strings.Contains(string(out), m.cfg.BridgeName) {
		if _, err := m.runner.Run(ctx, "ip", "link", "add", m.cfg.BridgeName, "type", "bridge"); err != nil {
			return fmt.Errorf("failed to create bridge %s: %w", m.cfg.BridgeName, err)
		}
	}

	// 2. Assign IP to bridge if not already assigned
	if m.cfg.BridgeIP != "" {
		gwIP := m.getGatewayIPLocked()
		addrOut, _ := m.runner.Run(ctx, "ip", "-o", "-4", "addr", "show", "dev", m.cfg.BridgeName)
		if !strings.Contains(string(addrOut), gwIP) {
			_, _ = m.runner.Run(ctx, "ip", "addr", "add", m.cfg.BridgeIP, "dev", m.cfg.BridgeName)
		}
	}

	// 3. Bring bridge up
	if _, err := m.runner.Run(ctx, "ip", "link", "set", m.cfg.BridgeName, "up"); err != nil {
		return fmt.Errorf("failed to bring up bridge %s: %w", m.cfg.BridgeName, err)
	}

	// 4. Attach physical NIC to bridge if specified or auto-detect primary NIC
	nic := m.cfg.PhysicalNIC
	if nic == "" && (m.cfg.Mode == ModeNIC || m.cfg.Mode == ModeWired) {
		if defNIC, err := m.detectDefaultInterface(ctx); err == nil && defNIC != "" && defNIC != m.cfg.BridgeName {
			nic = defNIC
		}
	}
	if nic != "" && nic != m.cfg.BridgeName {
		_, _ = m.runner.Run(ctx, "ip", "link", "set", "dev", nic, "master", m.cfg.BridgeName)
		_, _ = m.runner.Run(ctx, "ip", "link", "set", "dev", nic, "up")
	}

	// 5. Link explicitly configured bridges to this main bridge. Automatic
	// linking of every host bridge is opt-in only; see linkOtherBridgesLocked.
	_, _ = m.linkOtherBridgesLocked(ctx)

	// 6. Enable IPv4 forwarding and bridge netfilter settings
	m.runner.Run(ctx, "sysctl", "-w", "net.ipv4.ip_forward=1")
	_, _ = m.runner.Run(ctx, "sysctl", "-w", "net.bridge.bridge-nf-call-iptables=0")
	_, _ = m.runner.Run(ctx, "sysctl", "-w", "net.bridge.bridge-nf-call-arptables=0")

	// 7. Configure forwarding rules
	m.setupBridgeForward(ctx)
	if m.cfg.Mode == ModeNAT {
		m.setupNAT(ctx)
	}
	if m.cfg.IsEgressFirewallEnabled() {
		m.setupEgressFirewall(ctx)
	} else {
		m.teardownEgressFirewall(ctx)
	}

	return nil
}

// detectDefaultInterface finds the interface associated with the default IPv4 route
func (m *Manager) detectDefaultInterface(ctx context.Context) (string, error) {
	out, err := m.runner.Run(ctx, "ip", "route", "show", "default")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", fmt.Errorf("no default interface found")
}

// ListOtherBridges returns all bridge interface names on the host except the primary bridge
func (m *Manager) ListOtherBridges(ctx context.Context) ([]string, error) {
	out, err := m.runner.Run(ctx, "ip", "-o", "link", "show", "type", "bridge")
	if err != nil {
		return nil, err
	}
	var bridges []string
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			name := strings.TrimSuffix(fields[1], ":")
			if idx := strings.Index(name, "@"); idx != -1 {
				name = name[:idx]
			}
			if name != "" && name != m.cfg.BridgeName {
				bridges = append(bridges, name)
			}
		}
	}
	return bridges, nil
}

// linkOtherBridgesLocked links explicitly configured bridges to the primary
// bridge via veth pairs so they share L2 access to the host NIC and the main
// router's DHCP. It deliberately does not sweep every bridge on the host: a
// shared host may run Docker bridges, other Compute daemons/workers, or
// libvirt bridges, and linking those creates loops and MAC flaps. Set
// LinkAllBridges (COMPUTE_LINK_OTHER_BRIDGES=1) to opt into the old behavior.
func (m *Manager) linkOtherBridgesLocked(ctx context.Context) ([]string, error) {
	var otherBridges []string
	if m.cfg.LinkAllBridges {
		otherBridges, _ = m.ListOtherBridges(ctx)
	}

	// Also include any explicitly configured linked bridges
	for _, lb := range m.cfg.LinkedBridges {
		if lb != "" && lb != m.cfg.BridgeName {
			var found bool
			for _, b := range otherBridges {
				if b == lb {
					found = true
					break
				}
			}
			if !found {
				otherBridges = append(otherBridges, lb)
			}
		}
	}

	var linked []string
	for _, br := range otherBridges {
		safeName := br
		if len(safeName) > 10 {
			safeName = safeName[:10]
		}
		vethMain := fmt.Sprintf("vbm-%s", safeName)
		vethPeer := fmt.Sprintf("vbp-%s", safeName)

		// Check if veth pair already exists
		_, errCheck := m.runner.Run(ctx, "ip", "link", "show", vethMain)
		if errCheck != nil {
			// Create veth pair
			if _, err := m.runner.Run(ctx, "ip", "link", "add", "dev", vethMain, "type", "veth", "peer", "name", vethPeer); err != nil {
				continue
			}
		}

		// Enslave vethMain to primary bridge
		_, _ = m.runner.Run(ctx, "ip", "link", "set", "dev", vethMain, "master", m.cfg.BridgeName)
		// Enslave vethPeer to other bridge
		_, _ = m.runner.Run(ctx, "ip", "link", "set", "dev", vethPeer, "master", br)

		// Bring both endpoints up
		_, _ = m.runner.Run(ctx, "ip", "link", "set", "dev", vethMain, "up")
		_, _ = m.runner.Run(ctx, "ip", "link", "set", "dev", vethPeer, "up")

		// Ensure the other bridge itself is up
		_, _ = m.runner.Run(ctx, "ip", "link", "set", "dev", br, "up")

		linked = append(linked, br)
	}

	return linked, nil
}

// LinkOtherBridges is the thread-safe exported version of linkOtherBridgesLocked
func (m *Manager) LinkOtherBridges(ctx context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.linkOtherBridgesLocked(ctx)
}

func (m *Manager) setupNAT(ctx context.Context) {
	if m.cfg.Mode == ModeNoInternet || m.cfg.Mode == ModeAirgapped {
		return
	}
	// Add MASQUERADE rule if not present
	out, _ := m.runner.Run(ctx, "iptables", "-t", "nat", "-C", "POSTROUTING", "-s", m.cfg.SubnetCIDR, "!", "-o", m.cfg.BridgeName, "-j", "MASQUERADE")
	if len(out) != 0 || out == nil {
		m.runner.Run(ctx, "iptables", "-t", "nat", "-A", "POSTROUTING", "-s", m.cfg.SubnetCIDR, "!", "-o", m.cfg.BridgeName, "-j", "MASQUERADE")
	}

	// Add FORWARD rules
	if !m.cfg.IsEgressFirewallEnabled() {
		m.runner.Run(ctx, "iptables", "-I", "FORWARD", "-i", m.cfg.BridgeName, "-j", "ACCEPT")
	}
	m.runner.Run(ctx, "iptables", "-I", "FORWARD", "-o", m.cfg.BridgeName, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")
}

func (m *Manager) setupBridgeForward(ctx context.Context) {
	// Add FORWARD rule for internal bridge forwarding and DHCP
	if !m.cfg.IsEgressFirewallEnabled() {
		out, _ := m.runner.Run(ctx, "iptables", "-C", "FORWARD", "-i", m.cfg.BridgeName, "-j", "ACCEPT")
		if len(out) != 0 || out == nil {
			m.runner.Run(ctx, "iptables", "-I", "FORWARD", "-i", m.cfg.BridgeName, "-j", "ACCEPT")
		}
	}
	out2, _ := m.runner.Run(ctx, "iptables", "-C", "FORWARD", "-o", m.cfg.BridgeName, "-j", "ACCEPT")
	if len(out2) != 0 || out2 == nil {
		m.runner.Run(ctx, "iptables", "-I", "FORWARD", "-o", m.cfg.BridgeName, "-j", "ACCEPT")
	}
}

func (m *Manager) setupEgressFirewall(ctx context.Context) {
	// 1. Ensure custom chains exist in filter table
	_ = m.ensureChain(ctx, "iptables", "COMPUTE_EGRESS")
	_ = m.ensureChain(ctx, "iptables", "COMPUTE_HOST_IN")

	// 2. Remove any legacy broad accept rule from FORWARD
	m.runner.Run(ctx, "iptables", "-D", "FORWARD", "-i", m.cfg.BridgeName, "-j", "ACCEPT")

	// 3. Flush and populate COMPUTE_EGRESS rules (evaluated for -i <BridgeName> ! -o <BridgeName>)
	m.runner.Run(ctx, "iptables", "-F", "COMPUTE_EGRESS")

	// Rule 1: Always allow RELATED,ESTABLISHED responses
	m.runner.Run(ctx, "iptables", "-A", "COMPUTE_EGRESS", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")

	// Rule 2: Allow explicitly configured EgressAllowCIDRs before deny rules
	for _, cidr := range m.cfg.EgressAllowCIDRs {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		ip, _, err := net.ParseCIDR(cidr)
		if err == nil && ip.To4() != nil {
			m.runner.Run(ctx, "iptables", "-A", "COMPUTE_EGRESS", "-d", cidr, "-j", "ACCEPT")
		}
	}

	// Rule 3: Block link-local / Cloud IMDS (169.254.0.0/16) and all EgressDenyCIDRs
	m.runner.Run(ctx, "iptables", "-A", "COMPUTE_EGRESS", "-d", "169.254.0.0/16", "-j", "DROP")

	denyCIDRs := m.cfg.EffectiveEgressDenyCIDRs()
	for _, cidr := range denyCIDRs {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" || cidr == "169.254.0.0/16" {
			continue
		}
		ip, _, err := net.ParseCIDR(cidr)
		if err == nil && ip.To4() != nil {
			m.runner.Run(ctx, "iptables", "-A", "COMPUTE_EGRESS", "-d", cidr, "-j", "DROP")
		}
	}

	// Rule 4: Allow remaining public internet egress unless no-internet mode is active
	if m.cfg.Mode == ModeNoInternet || m.cfg.Mode == ModeAirgapped {
		m.runner.Run(ctx, "iptables", "-A", "COMPUTE_EGRESS", "-j", "DROP")
	} else {
		m.runner.Run(ctx, "iptables", "-A", "COMPUTE_EGRESS", "-j", "ACCEPT")
	}

	// 4. Hook COMPUTE_EGRESS into FORWARD for packets leaving the bridge
	m.runner.Run(ctx, "iptables", "-D", "FORWARD", "-i", m.cfg.BridgeName, "-d", "169.254.0.0/16", "-j", "DROP")
	m.runner.Run(ctx, "iptables", "-I", "FORWARD", "1", "-i", m.cfg.BridgeName, "-d", "169.254.0.0/16", "-j", "DROP")

	m.runner.Run(ctx, "iptables", "-D", "FORWARD", "-i", m.cfg.BridgeName, "!", "-o", m.cfg.BridgeName, "-j", "COMPUTE_EGRESS")
	m.runner.Run(ctx, "iptables", "-I", "FORWARD", "1", "-i", m.cfg.BridgeName, "!", "-o", m.cfg.BridgeName, "-j", "COMPUTE_EGRESS")

	// 5. Host port protection: COMPUTE_HOST_IN hooked into INPUT for -i <BridgeName>
	if m.cfg.IsBlockHostBridgePorts() {
		m.runner.Run(ctx, "iptables", "-F", "COMPUTE_HOST_IN")
		m.runner.Run(ctx, "iptables", "-A", "COMPUTE_HOST_IN", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
		m.runner.Run(ctx, "iptables", "-A", "COMPUTE_HOST_IN", "-p", "udp", "--dport", "67", "-j", "ACCEPT")
		m.runner.Run(ctx, "iptables", "-A", "COMPUTE_HOST_IN", "-p", "udp", "--dport", "68", "-j", "ACCEPT")
		m.runner.Run(ctx, "iptables", "-A", "COMPUTE_HOST_IN", "-p", "udp", "--dport", "53", "-j", "ACCEPT")
		m.runner.Run(ctx, "iptables", "-A", "COMPUTE_HOST_IN", "-p", "tcp", "--dport", "53", "-j", "ACCEPT")
		m.runner.Run(ctx, "iptables", "-A", "COMPUTE_HOST_IN", "-p", "icmp", "-j", "ACCEPT")
		m.runner.Run(ctx, "iptables", "-A", "COMPUTE_HOST_IN", "-j", "DROP")

		m.runner.Run(ctx, "iptables", "-D", "INPUT", "-i", m.cfg.BridgeName, "-j", "COMPUTE_HOST_IN")
		m.runner.Run(ctx, "iptables", "-I", "INPUT", "1", "-i", m.cfg.BridgeName, "-j", "COMPUTE_HOST_IN")
	} else {
		m.runner.Run(ctx, "iptables", "-D", "INPUT", "-i", m.cfg.BridgeName, "-j", "COMPUTE_HOST_IN")
		m.runner.Run(ctx, "iptables", "-F", "COMPUTE_HOST_IN")
		m.runner.Run(ctx, "iptables", "-X", "COMPUTE_HOST_IN")
	}

	// 6. Setup IPv6 equivalents if ip6tables is available
	m.setupIPv6EgressFirewall(ctx)
}

func (m *Manager) setupIPv6EgressFirewall(ctx context.Context) {
	if m.ensureChain(ctx, "ip6tables", "COMPUTE_EGRESS") != nil ||
		m.ensureChain(ctx, "ip6tables", "COMPUTE_HOST_IN") != nil {
		return
	}

	m.runner.Run(ctx, "ip6tables", "-D", "FORWARD", "-i", m.cfg.BridgeName, "-j", "ACCEPT")
	m.runner.Run(ctx, "ip6tables", "-F", "COMPUTE_EGRESS")
	m.runner.Run(ctx, "ip6tables", "-A", "COMPUTE_EGRESS", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")

	for _, cidr := range m.cfg.EgressAllowCIDRs {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		ip, _, err := net.ParseCIDR(cidr)
		if err == nil && ip.To4() == nil {
			m.runner.Run(ctx, "ip6tables", "-A", "COMPUTE_EGRESS", "-d", cidr, "-j", "ACCEPT")
		}
	}

	for _, cidr := range DefaultIPv6EgressDenyCIDRs {
		m.runner.Run(ctx, "ip6tables", "-A", "COMPUTE_EGRESS", "-d", cidr, "-j", "DROP")
	}
	for _, cidr := range m.cfg.EgressDenyCIDRs {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		ip, _, err := net.ParseCIDR(cidr)
		if err == nil && ip.To4() == nil {
			m.runner.Run(ctx, "ip6tables", "-A", "COMPUTE_EGRESS", "-d", cidr, "-j", "DROP")
		}
	}

	if m.cfg.Mode == ModeNoInternet || m.cfg.Mode == ModeAirgapped {
		m.runner.Run(ctx, "ip6tables", "-A", "COMPUTE_EGRESS", "-j", "DROP")
	} else {
		m.runner.Run(ctx, "ip6tables", "-A", "COMPUTE_EGRESS", "-j", "ACCEPT")
	}

	m.runner.Run(ctx, "ip6tables", "-D", "FORWARD", "-i", m.cfg.BridgeName, "!", "-o", m.cfg.BridgeName, "-j", "COMPUTE_EGRESS")
	m.runner.Run(ctx, "ip6tables", "-I", "FORWARD", "1", "-i", m.cfg.BridgeName, "!", "-o", m.cfg.BridgeName, "-j", "COMPUTE_EGRESS")

	if m.cfg.IsBlockHostBridgePorts() {
		m.runner.Run(ctx, "ip6tables", "-F", "COMPUTE_HOST_IN")
		m.runner.Run(ctx, "ip6tables", "-A", "COMPUTE_HOST_IN", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
		m.runner.Run(ctx, "ip6tables", "-A", "COMPUTE_HOST_IN", "-p", "udp", "--dport", "546", "-j", "ACCEPT")
		m.runner.Run(ctx, "ip6tables", "-A", "COMPUTE_HOST_IN", "-p", "udp", "--dport", "547", "-j", "ACCEPT")
		m.runner.Run(ctx, "ip6tables", "-A", "COMPUTE_HOST_IN", "-p", "udp", "--dport", "53", "-j", "ACCEPT")
		m.runner.Run(ctx, "ip6tables", "-A", "COMPUTE_HOST_IN", "-p", "tcp", "--dport", "53", "-j", "ACCEPT")
		m.runner.Run(ctx, "ip6tables", "-A", "COMPUTE_HOST_IN", "-p", "ipv6-icmp", "-j", "ACCEPT")
		m.runner.Run(ctx, "ip6tables", "-A", "COMPUTE_HOST_IN", "-j", "DROP")

		m.runner.Run(ctx, "ip6tables", "-D", "INPUT", "-i", m.cfg.BridgeName, "-j", "COMPUTE_HOST_IN")
		m.runner.Run(ctx, "ip6tables", "-I", "INPUT", "1", "-i", m.cfg.BridgeName, "-j", "COMPUTE_HOST_IN")
	} else {
		m.runner.Run(ctx, "ip6tables", "-D", "INPUT", "-i", m.cfg.BridgeName, "-j", "COMPUTE_HOST_IN")
		m.runner.Run(ctx, "ip6tables", "-F", "COMPUTE_HOST_IN")
		m.runner.Run(ctx, "ip6tables", "-X", "COMPUTE_HOST_IN")
	}
}

func (m *Manager) teardownEgressFirewall(ctx context.Context) {
	m.runner.Run(ctx, "iptables", "-D", "FORWARD", "-i", m.cfg.BridgeName, "!", "-o", m.cfg.BridgeName, "-j", "COMPUTE_EGRESS")
	m.runner.Run(ctx, "iptables", "-D", "FORWARD", "-i", m.cfg.BridgeName, "-d", "169.254.0.0/16", "-j", "DROP")
	m.runner.Run(ctx, "iptables", "-D", "INPUT", "-i", m.cfg.BridgeName, "-j", "COMPUTE_HOST_IN")

	m.runner.Run(ctx, "iptables", "-F", "COMPUTE_EGRESS")
	m.runner.Run(ctx, "iptables", "-X", "COMPUTE_EGRESS")
	m.runner.Run(ctx, "iptables", "-F", "COMPUTE_HOST_IN")
	m.runner.Run(ctx, "iptables", "-X", "COMPUTE_HOST_IN")

	m.runner.Run(ctx, "ip6tables", "-D", "FORWARD", "-i", m.cfg.BridgeName, "!", "-o", m.cfg.BridgeName, "-j", "COMPUTE_EGRESS")
	m.runner.Run(ctx, "ip6tables", "-D", "INPUT", "-i", m.cfg.BridgeName, "-j", "COMPUTE_HOST_IN")
	m.runner.Run(ctx, "ip6tables", "-F", "COMPUTE_EGRESS")
	m.runner.Run(ctx, "ip6tables", "-X", "COMPUTE_EGRESS")
	m.runner.Run(ctx, "ip6tables", "-F", "COMPUTE_HOST_IN")
	m.runner.Run(ctx, "ip6tables", "-X", "COMPUTE_HOST_IN")
}

// AddPortForward installs host DNAT rules that expose a guest port on the host.
// The rules are idempotent: existing entries are detected with iptables -C.
func (m *Manager) AddPortForward(ctx context.Context, protocol string, hostPort int, guestIP string, guestPort int) error {
	proto := strings.ToLower(strings.TrimSpace(protocol))
	if proto == "" {
		proto = "tcp"
	}
	if proto != "tcp" && proto != "udp" {
		return fmt.Errorf("unsupported protocol %q: use tcp or udp", protocol)
	}
	if hostPort <= 0 || hostPort > 65535 || guestPort <= 0 || guestPort > 65535 {
		return fmt.Errorf("host_port and guest_port must be between 1 and 65535")
	}
	ip := net.ParseIP(guestIP)
	if ip == nil {
		return fmt.Errorf("invalid guest IP %q", guestIP)
	}

	hostPortStr := strconv.Itoa(hostPort)
	guestPortStr := strconv.Itoa(guestPort)
	destination := net.JoinHostPort(guestIP, guestPortStr)

	ensure := func(args ...string) error {
		if _, err := m.runner.Run(ctx, "iptables", args...); err != nil {
			full := append([]string{"iptables"}, args...)
			return fmt.Errorf("%s: %w", strings.Join(full, " "), err)
		}
		return nil
	}
	exists := func(args ...string) bool {
		_, err := m.runner.Run(ctx, "iptables", args...)
		return err == nil
	}

	natRule := []string{"-t", "nat", "-C", "PREROUTING", "-p", proto, "--dport", hostPortStr, "-j", "DNAT", "--to-destination", destination}
	if !exists(natRule...) {
		if err := ensure("-t", "nat", "-A", "PREROUTING", "-p", proto, "--dport", hostPortStr, "-j", "DNAT", "--to-destination", destination); err != nil {
			return err
		}
	}

	// Host-local traffic (curl localhost) traverses OUTPUT rather than PREROUTING.
	outRule := []string{"-t", "nat", "-C", "OUTPUT", "-p", proto, "--dport", hostPortStr, "-j", "DNAT", "--to-destination", destination}
	if !exists(outRule...) {
		if err := ensure("-t", "nat", "-A", "OUTPUT", "-p", proto, "--dport", hostPortStr, "-j", "DNAT", "--to-destination", destination); err != nil {
			return err
		}
	}

	fwdRule := []string{"-C", "FORWARD", "-p", proto, "-d", guestIP, "--dport", guestPortStr, "-j", "ACCEPT"}
	if !exists(fwdRule...) {
		if err := ensure("-A", "FORWARD", "-p", proto, "-d", guestIP, "--dport", guestPortStr, "-j", "ACCEPT"); err != nil {
			return err
		}
	}
	return nil
}

// RemovePortForward removes the DNAT/FORWARD rules installed by AddPortForward.
// Missing rules are ignored so the call is safe to repeat.
func (m *Manager) RemovePortForward(ctx context.Context, protocol string, hostPort int, guestIP string, guestPort int) error {
	proto := strings.ToLower(strings.TrimSpace(protocol))
	if proto == "" {
		proto = "tcp"
	}
	if proto != "tcp" && proto != "udp" {
		return fmt.Errorf("unsupported protocol %q: use tcp or udp", protocol)
	}
	if hostPort <= 0 || guestPort <= 0 || net.ParseIP(guestIP) == nil {
		return nil
	}
	hostPortStr := strconv.Itoa(hostPort)
	guestPortStr := strconv.Itoa(guestPort)
	destination := net.JoinHostPort(guestIP, guestPortStr)

	m.runner.Run(ctx, "iptables", "-t", "nat", "-D", "PREROUTING", "-p", proto, "--dport", hostPortStr, "-j", "DNAT", "--to-destination", destination)
	m.runner.Run(ctx, "iptables", "-t", "nat", "-D", "OUTPUT", "-p", proto, "--dport", hostPortStr, "-j", "DNAT", "--to-destination", destination)
	m.runner.Run(ctx, "iptables", "-D", "FORWARD", "-p", proto, "-d", guestIP, "--dport", guestPortStr, "-j", "ACCEPT")
	return nil
}

// SetupTAP creates a tap interface, attaches it to the bridge, and brings it up
func (m *Manager) SetupTAP(ctx context.Context, tapName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if already exists
	_, err := m.runner.Run(ctx, "ip", "link", "show", tapName)
	if err == nil {
		// Exists, ensure attached to bridge and brought up
		m.runner.Run(ctx, "ip", "link", "set", "dev", tapName, "master", m.cfg.BridgeName)
		m.runner.Run(ctx, "ip", "link", "set", "dev", tapName, "up")
		return nil
	}

	// Create TAP
	if out, err := m.runner.Run(ctx, "ip", "tuntap", "add", "dev", tapName, "mode", "tap"); err != nil {
		return fmt.Errorf("failed to create tap %s (%s): %w", tapName, string(out), err)
	}

	// Attach to bridge
	if out, err := m.runner.Run(ctx, "ip", "link", "set", "dev", tapName, "master", m.cfg.BridgeName); err != nil {
		m.runner.Run(ctx, "ip", "link", "del", "dev", tapName)
		return fmt.Errorf("failed to attach tap %s to bridge %s (%s): %w", tapName, m.cfg.BridgeName, string(out), err)
	}

	// Bring TAP up
	if out, err := m.runner.Run(ctx, "ip", "link", "set", "dev", tapName, "up"); err != nil {
		m.runner.Run(ctx, "ip", "link", "del", "dev", tapName)
		return fmt.Errorf("failed to bring up tap %s (%s): %w", tapName, string(out), err)
	}

	return nil
}

// IsolationConfig specifies network parameters for TAP anti-spoofing and bridge isolation.
type IsolationConfig struct {
	TAPDevice        string   `json:"tap_device"`
	IPAddress        string   `json:"ip_address"`
	MACAddress       string   `json:"mac_address"`
	OwnerID          string   `json:"owner_id,omitempty"`
	Isolated         bool     `json:"isolated,omitempty"`
	AllowSameAccount bool     `json:"allow_same_account,omitempty"`
	AllowedPeerTAPs  []string `json:"allowed_peer_taps,omitempty"`
	AllowedPeerIPs   []string `json:"allowed_peer_ips,omitempty"`
	IsolationMode    string   `json:"isolation_mode,omitempty"`
}

func isPortIsolated(cfg IsolationConfig) bool {
	if isNoInternet(cfg) {
		return true
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.IsolationMode))
	if mode == "strict" || mode == "isolated" {
		return true
	}
	return cfg.Isolated || (!cfg.AllowSameAccount && len(cfg.AllowedPeerTAPs) == 0)
}

func isNoInternet(cfg IsolationConfig) bool {
	mode := strings.ToLower(strings.TrimSpace(cfg.IsolationMode))
	return mode == "no-internet" || mode == "no_internet" || mode == "airgapped" || mode == "air-gapped"
}

var (
	arpLimitationOnce sync.Once
	ebLimitationOnce  sync.Once
)

// ensureChain creates a base chain for an xtables family when it does not
// exist yet. "Chain already exists" is success. A missing binary or a kernel
// that cannot create the chain is reported to the caller so anti-spoofing can
// degrade explicitly instead of silently.
func (m *Manager) ensureChain(ctx context.Context, binary, chain string) error {
	out, err := m.runner.Run(ctx, binary, "-N", chain)
	if err == nil {
		return nil
	}
	msg := strings.ToLower(string(out) + " " + err.Error())
	if strings.Contains(msg, "already exists") {
		return nil
	}
	return fmt.Errorf("%s -N %s: %w (%s)", binary, chain, err, strings.TrimSpace(string(out)))
}

// isBenignTeardownError reports whether a failed delete/cleanup command means
// the rule or device is already absent.
func isBenignTeardownError(out []byte, err error) bool {
	if err == nil {
		return true
	}
	msg := strings.ToLower(string(out) + " " + err.Error())
	for _, marker := range []string{
		"bad rule",
		"does a matching rule exist",
		"no chain/target/match",
		"no chain",
		"not exist",
		"cannot find device",
		"cannot find bridge device",
		"no such file or directory",
		"executable file not found",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// ApplyAntiSpoofAndIsolation installs L2 MAC anti-spoofing, L3 IP anti-spoofing, DHCP server
// impersonation blocking, and bridge port isolation for a microVM TAP interface.
func (m *Manager) ApplyAntiSpoofAndIsolation(ctx context.Context, cfg IsolationConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if cfg.TAPDevice == "" {
		return nil
	}

	// Clean any previous rules for this TAP first to prevent duplication
	_ = m.removeAntiSpoofAndIsolationLocked(ctx, cfg)

	portIsolated := isPortIsolated(cfg)
	var peerTAPs []string
	if portIsolated {
		cfg.AllowedPeerTAPs = nil
	} else {
		seenPeers := make(map[string]bool, len(cfg.AllowedPeerTAPs))
		for _, peerTAP := range cfg.AllowedPeerTAPs {
			if peerTAP != "" && peerTAP != cfg.TAPDevice && !seenPeers[peerTAP] {
				seenPeers[peerTAP] = true
				peerTAPs = append(peerTAPs, peerTAP)
			}
		}
		cfg.AllowedPeerTAPs = peerTAPs
	}

	if m.isolationConfigs == nil {
		m.isolationConfigs = make(map[string]IsolationConfig)
	}
	m.isolationConfigs[cfg.TAPDevice] = cfg

	var errs []error
	runSetup := func(name string, args ...string) {
		out, err := m.runner.Run(ctx, name, args...)
		if err != nil {
			log.Printf("[ERROR] ApplyAntiSpoofAndIsolation: %s %s failed: %v (output: %s)", name, strings.Join(args, " "), err, string(out))
			errs = append(errs, fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err))
		}
	}

	// nft-backed arptables/ebtables do not auto-create their base chains, so a
	// fresh host fails inserts with "No chain/target/match by that name". Create
	// the chains we need first. Legacy xtables implementations accept the same
	// commands; "Chain already exists" is expected and harmless.
	arpAvailable := m.ensureChain(ctx, "arptables", "FORWARD") == nil
	if !arpAvailable {
		arpLimitationOnce.Do(func() {
			log.Printf("[WARN] ApplyAntiSpoofAndIsolation: ARP anti-spoof rules unavailable on this host; ARP spoofing is not isolated (L2/L3 filtering continues)")
		})
	}
	ebAvailable := m.ensureChain(ctx, "ebtables", "FORWARD") == nil &&
		m.ensureChain(ctx, "ebtables", "INPUT") == nil
	if !ebAvailable {
		ebLimitationOnce.Do(func() {
			log.Printf("[WARN] ApplyAntiSpoofAndIsolation: ebtables MAC anti-spoof rules unavailable on this host; MAC spoofing is not isolated")
		})
	}

	// 1. Kernel Bridge Port Isolation:
	// Isolated sandboxes keep isolated=on so the bridge never forwards L2 frames
	// between them and any other TAP port. Non-isolated same-account sandboxes
	// use isolated=off so ebtables FORWARD controls exact peer-to-peer L2 access.
	if portIsolated {
		runSetup("bridge", "link", "set", "dev", cfg.TAPDevice, "isolated", "on")
	} else {
		runSetup("bridge", "link", "set", "dev", cfg.TAPDevice, "isolated", "off")
	}

	// 2. Default lateral isolation DROP rules at the tail of FORWARD (both L3 iptables and L2 ebtables):
	// bridge-nf-call-iptables is disabled on the bridge so L2 traffic between TAPs is governed by ebtables FORWARD.
	runSetup("iptables", "-A", "FORWARD", "-i", cfg.TAPDevice, "-o", "mftap-+", "-j", "DROP")
	runSetup("iptables", "-A", "FORWARD", "-i", "mftap-+", "-o", cfg.TAPDevice, "-j", "DROP")
	if ebAvailable {
		runSetup("ebtables", "-A", "FORWARD", "-i", cfg.TAPDevice, "-o", "mftap-+", "-j", "DROP")
		runSetup("ebtables", "-A", "FORWARD", "-i", "mftap-+", "-o", cfg.TAPDevice, "-j", "DROP")
	}

	// 3. Intra-account Lateral Communication:
	// Insert peer ACCEPT rules BEFORE anti-spoof rules so step 4 places anti-spoof
	// rules above ACCEPT rules in FORWARD.
	if !portIsolated {
		for _, peerTAP := range cfg.AllowedPeerTAPs {
			runSetup("iptables", "-I", "FORWARD", "1", "-i", cfg.TAPDevice, "-o", peerTAP, "-j", "ACCEPT")
			runSetup("iptables", "-I", "FORWARD", "1", "-i", peerTAP, "-o", cfg.TAPDevice, "-j", "ACCEPT")
			if ebAvailable {
				runSetup("ebtables", "-I", "FORWARD", "1", "-i", cfg.TAPDevice, "-o", peerTAP, "-j", "ACCEPT")
				runSetup("ebtables", "-I", "FORWARD", "1", "-i", peerTAP, "-o", cfg.TAPDevice, "-j", "ACCEPT")
			}
		}
	}

	// 4. Anti-spoofing & DHCP Server Impersonation Guards (inserted at position 1 after ACCEPTs):
	installAntiSpoof := func(target IsolationConfig) {
		// Rogue DHCP server blocking (UDP src port 67)
		runSetup("iptables", "-I", "FORWARD", "1", "-i", target.TAPDevice, "-p", "udp", "--sport", "67", "-j", "DROP")

		// L3 IP anti-spoofing
		if target.IPAddress != "" && target.IPAddress != "dhcp" {
			runSetup("iptables", "-I", "FORWARD", "1", "-i", target.TAPDevice, "!", "-s", target.IPAddress, "-j", "DROP")
		}

		// L2 MAC & ARP anti-spoofing
		if target.MACAddress != "" && ebAvailable {
			runSetup("ebtables", "-I", "FORWARD", "1", "-i", target.TAPDevice, "!", "-s", target.MACAddress, "-j", "DROP")
			runSetup("ebtables", "-I", "INPUT", "1", "-i", target.TAPDevice, "!", "-s", target.MACAddress, "-j", "DROP")
		}
		if target.MACAddress != "" && arpAvailable {
			runSetup("arptables", "-I", "FORWARD", "1", "-i", target.TAPDevice, "!", "--source-mac", target.MACAddress, "-j", "DROP")
		}
		if target.IPAddress != "" && target.IPAddress != "dhcp" && arpAvailable {
			runSetup("arptables", "-I", "FORWARD", "1", "-i", target.TAPDevice, "!", "--source-ip", target.IPAddress, "-j", "DROP")
		}
	}
	installAntiSpoof(cfg)

	// Keep existing peers' anti-spoof rules ahead of the newly inserted -i <peer> -o <tap> ACCEPT rules.
	if !portIsolated && len(cfg.AllowedPeerTAPs) > 0 {
		deleteAntiSpoof := func(target IsolationConfig) {
			runIgnore := func(name string, args ...string) {
				_, _ = m.runner.Run(ctx, name, args...)
			}
			runIgnore("iptables", "-D", "FORWARD", "-i", target.TAPDevice, "-p", "udp", "--sport", "67", "-j", "DROP")
			if target.IPAddress != "" && target.IPAddress != "dhcp" {
				runIgnore("iptables", "-D", "FORWARD", "-i", target.TAPDevice, "!", "-s", target.IPAddress, "-j", "DROP")
			}
			if target.MACAddress != "" && ebAvailable {
				runIgnore("ebtables", "-D", "FORWARD", "-i", target.TAPDevice, "!", "-s", target.MACAddress, "-j", "DROP")
				runIgnore("ebtables", "-D", "INPUT", "-i", target.TAPDevice, "!", "-s", target.MACAddress, "-j", "DROP")
			}
			if target.MACAddress != "" && arpAvailable {
				runIgnore("arptables", "-D", "FORWARD", "-i", target.TAPDevice, "!", "--source-mac", target.MACAddress, "-j", "DROP")
			}
			if target.IPAddress != "" && target.IPAddress != "dhcp" && arpAvailable {
				runIgnore("arptables", "-D", "FORWARD", "-i", target.TAPDevice, "!", "--source-ip", target.IPAddress, "-j", "DROP")
			}
		}
		for _, peerTAP := range cfg.AllowedPeerTAPs {
			if peerCfg, ok := m.isolationConfigs[peerTAP]; ok {
				deleteAntiSpoof(peerCfg)
				installAntiSpoof(peerCfg)
			}
		}
	}

	// 5. Per-VM no-internet / airgapped mode: drop all outbound traffic off-bridge while permitting management return traffic.
	// Any previous per-VM no-internet rules were already torn down unconditionally by removeAntiSpoofAndIsolationLocked above.
	if isNoInternet(cfg) {
		runSetup("iptables", "-I", "FORWARD", "1", "-i", cfg.TAPDevice, "!", "-o", m.cfg.BridgeName, "-j", "DROP")
		runSetup("iptables", "-I", "FORWARD", "1", "-i", cfg.TAPDevice, "!", "-o", m.cfg.BridgeName, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
		// Best-effort IPv6 equivalent (hosts without ip6tables degrade cleanly).
		_, _ = m.runner.Run(ctx, "ip6tables", "-I", "FORWARD", "1", "-i", cfg.TAPDevice, "!", "-o", m.cfg.BridgeName, "-j", "DROP")
		_, _ = m.runner.Run(ctx, "ip6tables", "-I", "FORWARD", "1", "-i", cfg.TAPDevice, "!", "-o", m.cfg.BridgeName, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	}

	return errors.Join(errs...)
}

// SetupAntiSpoofAndIsolation is an alias for ApplyAntiSpoofAndIsolation
func (m *Manager) SetupAntiSpoofAndIsolation(ctx context.Context, cfg IsolationConfig) error {
	return m.ApplyAntiSpoofAndIsolation(ctx, cfg)
}

// ApplyIsolation is an alias for ApplyAntiSpoofAndIsolation
func (m *Manager) ApplyIsolation(ctx context.Context, cfg IsolationConfig) error {
	return m.ApplyAntiSpoofAndIsolation(ctx, cfg)
}

// RemoveAntiSpoofAndIsolation cleans up all firewall rules, anti-spoofing guards, and bridge
// isolation settings associated with a microVM TAP interface.
func (m *Manager) RemoveAntiSpoofAndIsolation(ctx context.Context, cfg IsolationConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.removeAntiSpoofAndIsolationLocked(ctx, cfg)
}

// RemoveIsolation is an alias for RemoveAntiSpoofAndIsolation
func (m *Manager) RemoveIsolation(ctx context.Context, cfg IsolationConfig) error {
	return m.RemoveAntiSpoofAndIsolation(ctx, cfg)
}

func (m *Manager) removeAntiSpoofAndIsolationLocked(ctx context.Context, cfg IsolationConfig) error {
	tapName := cfg.TAPDevice
	if tapName == "" {
		return nil
	}

	targetCfg := cfg
	if storedCfg, ok := m.isolationConfigs[tapName]; ok {
		targetCfg = storedCfg
		delete(m.isolationConfigs, tapName)
	} else if (targetCfg.IPAddress == "" || targetCfg.MACAddress == "") && m.store != nil {
		if alloc, err := m.store.GetNetworkAllocation(ctx, tapName); err == nil && alloc != nil {
			if targetCfg.IPAddress == "" {
				targetCfg.IPAddress = alloc.IPAddress
			}
			if targetCfg.MACAddress == "" {
				targetCfg.MACAddress = alloc.MACAddress
			}
		} else if err != nil {
			log.Printf("[ERROR] removeAntiSpoofAndIsolation: GetNetworkAllocation failed for %s: %v", tapName, err)
		}
	}

	// Collect all peers referenced either by targetCfg, cfg, or other TAPs' cached configs.
	peerSet := make(map[string]bool)
	var peersToClean []string
	addPeer := func(p string) {
		if p != "" && p != tapName && !peerSet[p] {
			peerSet[p] = true
			peersToClean = append(peersToClean, p)
		}
	}
	for _, p := range targetCfg.AllowedPeerTAPs {
		addPeer(p)
	}
	for _, p := range cfg.AllowedPeerTAPs {
		addPeer(p)
	}
	for otherTAP, otherCfg := range m.isolationConfigs {
		if otherTAP == tapName {
			continue
		}
		var filtered []string
		found := false
		for _, p := range otherCfg.AllowedPeerTAPs {
			if p == tapName {
				found = true
				continue
			}
			filtered = append(filtered, p)
		}
		if found {
			addPeer(otherTAP)
			otherCfg.AllowedPeerTAPs = filtered
			m.isolationConfigs[otherTAP] = otherCfg
		}
	}

	var errs []error
	runTeardown := func(name string, args ...string) {
		out, err := m.runner.Run(ctx, name, args...)
		if err == nil {
			return
		}
		// Teardown is best-effort and idempotent: a rule or device that is
		// already gone is a success, not an error. Only unexpected failures
		// (e.g. permission denied) are reported.
		if isBenignTeardownError(out, err) {
			return
		}
		log.Printf("[WARN] removeAntiSpoofAndIsolation: %s %s failed: %v (output: %s)", name, strings.Join(args, " "), err, string(out))
		errs = append(errs, fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err))
	}

	// Delete bridge isolation drop rules (iptables + ebtables)
	runTeardown("iptables", "-D", "FORWARD", "-i", targetCfg.TAPDevice, "-o", "mftap-+", "-j", "DROP")
	runTeardown("iptables", "-D", "FORWARD", "-i", "mftap-+", "-o", targetCfg.TAPDevice, "-j", "DROP")
	runTeardown("ebtables", "-D", "FORWARD", "-i", targetCfg.TAPDevice, "-o", "mftap-+", "-j", "DROP")
	runTeardown("ebtables", "-D", "FORWARD", "-i", "mftap-+", "-o", targetCfg.TAPDevice, "-j", "DROP")

	// Delete intra-account accept rules (iptables + ebtables)
	for _, peerTAP := range peersToClean {
		runTeardown("iptables", "-D", "FORWARD", "-i", targetCfg.TAPDevice, "-o", peerTAP, "-j", "ACCEPT")
		runTeardown("iptables", "-D", "FORWARD", "-i", peerTAP, "-o", targetCfg.TAPDevice, "-j", "ACCEPT")
		runTeardown("ebtables", "-D", "FORWARD", "-i", targetCfg.TAPDevice, "-o", peerTAP, "-j", "ACCEPT")
		runTeardown("ebtables", "-D", "FORWARD", "-i", peerTAP, "-o", targetCfg.TAPDevice, "-j", "ACCEPT")
	}

	// Delete DHCP guard
	runTeardown("iptables", "-D", "FORWARD", "-i", targetCfg.TAPDevice, "-p", "udp", "--sport", "67", "-j", "DROP")

	// Delete IP anti-spoof
	if targetCfg.IPAddress != "" && targetCfg.IPAddress != "dhcp" {
		runTeardown("iptables", "-D", "FORWARD", "-i", targetCfg.TAPDevice, "!", "-s", targetCfg.IPAddress, "-j", "DROP")
	}

	// Delete ARP and MAC anti-spoof
	if targetCfg.MACAddress != "" {
		runTeardown("ebtables", "-D", "FORWARD", "-i", targetCfg.TAPDevice, "!", "-s", targetCfg.MACAddress, "-j", "DROP")
		runTeardown("ebtables", "-D", "INPUT", "-i", targetCfg.TAPDevice, "!", "-s", targetCfg.MACAddress, "-j", "DROP")
		runTeardown("arptables", "-D", "FORWARD", "-i", targetCfg.TAPDevice, "!", "--source-mac", targetCfg.MACAddress, "-j", "DROP")
	}
	if targetCfg.IPAddress != "" && targetCfg.IPAddress != "dhcp" {
		runTeardown("arptables", "-D", "FORWARD", "-i", targetCfg.TAPDevice, "!", "--source-ip", targetCfg.IPAddress, "-j", "DROP")
	}

	// Delete per-VM no-internet / airgapped rules unconditionally so mode transitions
	// (no-internet -> account/isolated) and post-restart deletions (when in-memory
	// isolationConfigs is empty) never leak rules in FORWARD.
	runTeardown("iptables", "-D", "FORWARD", "-i", targetCfg.TAPDevice, "!", "-o", m.cfg.BridgeName, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	runTeardown("iptables", "-D", "FORWARD", "-i", targetCfg.TAPDevice, "!", "-o", m.cfg.BridgeName, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	runTeardown("iptables", "-D", "FORWARD", "-i", targetCfg.TAPDevice, "!", "-o", m.cfg.BridgeName, "-j", "DROP")
	runTeardown("ip6tables", "-D", "FORWARD", "-i", targetCfg.TAPDevice, "!", "-o", m.cfg.BridgeName, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	runTeardown("ip6tables", "-D", "FORWARD", "-i", targetCfg.TAPDevice, "!", "-o", m.cfg.BridgeName, "-j", "DROP")

	// Reset bridge port isolation if link is still present
	runTeardown("bridge", "link", "set", "dev", targetCfg.TAPDevice, "isolated", "off")

	return errors.Join(errs...)
}

// RemoveTAP deletes a TAP interface and tears down associated firewall isolation rules
func (m *Manager) RemoveTAP(ctx context.Context, tapName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if tapName == "" {
		return nil
	}
	isoErr := m.removeAntiSpoofAndIsolationLocked(ctx, IsolationConfig{TAPDevice: tapName})
	out, linkErr := m.runner.Run(ctx, "ip", "link", "del", "dev", tapName)
	if linkErr != nil && !isBenignTeardownError(out, linkErr) {
		log.Printf("[WARN] RemoveTAP: ip link del dev %s failed: %v (output: %s)", tapName, linkErr, string(out))
	} else if linkErr != nil {
		linkErr = nil
	}
	return errors.Join(isoErr, linkErr)
}

// ConfigureVMPublicIP manages public network access for a dedicated VM IP/TAP.
// When enabled=true, any drop isolation rules are cleared and ingress/egress FORWARD ACCEPT rules are placed so external traffic can reach the VM's dedicated public IP.
// When enabled=false, isolation rules are placed to block external ingress and egress while preserving host bridge access.
func (m *Manager) ConfigureVMPublicIP(ctx context.Context, ip string, tapDev string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ip == "" && tapDev == "" {
		return nil
	}

	runIPCmd := func(name string, args ...string) {
		out, err := m.runner.Run(ctx, name, args...)
		if err != nil && !isBenignTeardownError(out, err) {
			log.Printf("[WARN] ConfigureVMPublicIP: %s %s failed: %v (output: %s)", name, strings.Join(args, " "), err, string(out))
		}
	}

	// Always clear existing DROP and ACCEPT rules first to avoid duplicate rules
	if ip != "" && ip != "dhcp" {
		runIPCmd("iptables", "-D", "FORWARD", "-s", ip, "!", "-o", m.cfg.BridgeName, "-j", "DROP")
		runIPCmd("iptables", "-D", "FORWARD", "-d", ip, "!", "-i", m.cfg.BridgeName, "-j", "DROP")
		runIPCmd("iptables", "-D", "FORWARD", "-d", ip, "-o", m.cfg.BridgeName, "-j", "ACCEPT")
		runIPCmd("iptables", "-D", "FORWARD", "-s", ip, "-i", m.cfg.BridgeName, "-j", "ACCEPT")
	}
	if tapDev != "" && m.cfg.PhysicalNIC != "" {
		runIPCmd("iptables", "-D", "FORWARD", "-i", tapDev, "-o", m.cfg.PhysicalNIC, "-j", "DROP")
		runIPCmd("iptables", "-D", "FORWARD", "-i", m.cfg.PhysicalNIC, "-o", tapDev, "-j", "DROP")
		runIPCmd("iptables", "-D", "FORWARD", "-i", tapDev, "-o", m.cfg.PhysicalNIC, "-j", "ACCEPT")
		runIPCmd("iptables", "-D", "FORWARD", "-i", m.cfg.PhysicalNIC, "-o", tapDev, "-j", "ACCEPT")
	}

	if enabled {
		// When public IP is turned ON, install FORWARD rules allowing external ingress and egress for the VM
		if ip != "" && ip != "dhcp" {
			runIPCmd("iptables", "-I", "FORWARD", "1", "-d", ip, "-o", m.cfg.BridgeName, "-j", "ACCEPT")
			runIPCmd("iptables", "-I", "FORWARD", "1", "-s", ip, "-i", m.cfg.BridgeName, "-j", "ACCEPT")
		}
		if tapDev != "" && m.cfg.PhysicalNIC != "" {
			runIPCmd("iptables", "-I", "FORWARD", "1", "-i", tapDev, "-o", m.cfg.PhysicalNIC, "-j", "ACCEPT")
			runIPCmd("iptables", "-I", "FORWARD", "1", "-i", m.cfg.PhysicalNIC, "-o", tapDev, "-j", "ACCEPT")
		}
	} else {
		// If public IP is turned OFF, install DROP rules blocking external/public forwarding
		if ip != "" && ip != "dhcp" {
			runIPCmd("iptables", "-I", "FORWARD", "1", "-s", ip, "!", "-o", m.cfg.BridgeName, "-j", "DROP")
			runIPCmd("iptables", "-I", "FORWARD", "1", "-d", ip, "!", "-i", m.cfg.BridgeName, "-j", "DROP")
		}
		if tapDev != "" && m.cfg.PhysicalNIC != "" {
			runIPCmd("iptables", "-I", "FORWARD", "1", "-i", tapDev, "-o", m.cfg.PhysicalNIC, "-j", "DROP")
			runIPCmd("iptables", "-I", "FORWARD", "1", "-i", m.cfg.PhysicalNIC, "-o", tapDev, "-j", "DROP")
		}
	}

	return nil
}

// getDHCPInterface returns the network interface used to reach the main router DHCP server
func (m *Manager) getDHCPInterface(ctx context.Context) string {
	if m.cfg.PhysicalNIC != "" {
		return m.cfg.PhysicalNIC
	}
	if defNIC, err := m.detectDefaultInterface(ctx); err == nil && defNIC != "" && defNIC != m.cfg.BridgeName {
		return defNIC
	}
	return m.cfg.BridgeName
}

// AcquireDHCPLease requests an IP lease from the DHCP server on the active network interface and validates it
func (m *Manager) AcquireDHCPLease(ctx context.Context, mac string) (*DHCPLease, error) {
	iface := m.getDHCPInterface(ctx)
	lease, err := AcquireDHCPLease(ctx, m.runner, iface, mac)
	if err != nil {
		return nil, err
	}
	if lease != nil && lease.IP != nil {
		// Verify offered IP is not already in use by another device on the network
		if m.IsIPInUse(ctx, lease.IP.String(), mac) {
			_ = m.ReleaseDHCPLease(ctx, mac, lease.IP.String())
			return nil, fmt.Errorf("dhcp server offered ip %s which is already in use by another device on the network", lease.IP.String())
		}
	}
	return lease, nil
}

// ReleaseDHCPLease releases an assigned DHCP lease back to the network DHCP pool
func (m *Manager) ReleaseDHCPLease(ctx context.Context, mac string, ipStr string) error {
	if ipStr == "" || ipStr == "dhcp" || mac == "" {
		return nil
	}
	hwAddr, err := net.ParseMAC(mac)
	if err != nil {
		return err
	}
	clientIP := net.ParseIP(ipStr)
	if clientIP == nil {
		return nil
	}
	gw := net.ParseIP(m.GetGatewayIP())
	if gw == nil {
		gw = net.IPv4bcast
	}
	_ = BuildRelease(0x12345678, hwAddr, clientIP, gw)
	return nil
}

// GetHostIPs returns all IPv4 addresses currently assigned to host network interfaces
func (m *Manager) GetHostIPs() map[string]bool {
	ips := make(map[string]bool)
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ip4 := ipnet.IP.To4(); ip4 != nil {
					ips[ip4.String()] = true
				}
			}
		}
	}
	return ips
}

// GetNeighborIPs parses the host ARP and neighbor tables to collect all active IP addresses on the network
func (m *Manager) GetNeighborIPs(ctx context.Context) map[string]string {
	res := make(map[string]string)
	if out, err := m.runner.Run(ctx, "ip", "neigh", "show"); err == nil {
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				ip := fields[0]
				for i, f := range fields {
					if f == "lladdr" && i+1 < len(fields) {
						state := fields[len(fields)-1]
						if state != "FAILED" {
							res[ip] = strings.ToLower(fields[i+1])
						}
					}
				}
			}
		}
	}
	if arpData, err := os.ReadFile("/proc/net/arp"); err == nil {
		lines := strings.Split(string(arpData), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) >= 4 && fields[0] != "IP" {
				ip := fields[0]
				mac := strings.ToLower(fields[3])
				if mac != "00:00:00:00:00:00" && mac != "" {
					res[ip] = mac
				}
			}
		}
	}
	return res
}

// IsIPInUse checks if an IP address is already used by the host, default gateway, or another device on the network
func (m *Manager) IsIPInUse(ctx context.Context, ipStr string, allowedMAC string) bool {
	if ipStr == "" {
		return false
	}
	cleanAllowed := strings.ToLower(strings.TrimSpace(allowedMAC))

	// 1. Host local IPs
	hostIPs := m.GetHostIPs()
	if hostIPs[ipStr] {
		return true
	}

	// 2. Gateway IP
	if ipStr == m.GetGatewayIP() {
		return true
	}

	// 3. Check ARP / Neighbor table
	neigh := m.GetNeighborIPs(ctx)
	if existingMAC, ok := neigh[ipStr]; ok {
		if cleanAllowed == "" || existingMAC != cleanAllowed {
			return true
		}
	}

	// 4. Active probing via busybox arping, arping, or ping
	nic := m.getDHCPInterface(ctx)
	probeCtx, probeCancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer probeCancel()
	if nic != "" {
		out, err := m.runner.Run(probeCtx, "busybox", "arping", "-c", "1", "-w", "1", "-I", nic, ipStr)
		if err != nil {
			out, err = m.runner.Run(probeCtx, "arping", "-c", "1", "-w", "1", "-I", nic, ipStr)
		}
		if err == nil {
			outStr := string(out)
			if strings.Contains(outStr, "reply from") || strings.Contains(outStr, "response(s)") || strings.Contains(outStr, "bytes from") {
				return true
			}
		}
		return false
	}

	// Fallback to ping only if no physical/bridged NIC was present
	out, err := m.runner.Run(probeCtx, "ping", "-c", "1", "-W", "1", ipStr)
	if err == nil && strings.Contains(string(out), "1 received") {
		return true
	}

	return false
}

// ResolveGuestIPByMAC scans the neighbor/ARP table, DHCP lease files, and queries the router DHCP server to resolve the dynamic IP assigned to a guest MAC
func (m *Manager) ResolveGuestIPByMAC(ctx context.Context, mac string) (string, error) {
	if mac == "" {
		return "", fmt.Errorf("empty mac address")
	}
	cleanMAC := strings.ToLower(strings.TrimSpace(mac))

	// 1. Scan host neighbor table
	out, err := m.runner.Run(ctx, "ip", "neigh", "show")
	if err == nil {
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			lower := strings.ToLower(line)
			if strings.Contains(lower, cleanMAC) {
				fields := strings.Fields(line)
				if len(fields) >= 1 {
					candidateIP := fields[0]
					if parsed := net.ParseIP(candidateIP); parsed != nil && parsed.To4() != nil {
						if !m.GetHostIPs()[candidateIP] {
							return candidateIP, nil
						}
					}
				}
			}
		}
	}

	// 2. Fallback to /proc/net/arp if available
	if arpData, err := os.ReadFile("/proc/net/arp"); err == nil {
		lines := strings.Split(string(arpData), "\n")
		for _, line := range lines {
			lower := strings.ToLower(line)
			if strings.Contains(lower, cleanMAC) {
				fields := strings.Fields(line)
				if len(fields) >= 1 {
					candidateIP := fields[0]
					if parsed := net.ParseIP(candidateIP); parsed != nil && parsed.To4() != nil {
						if !m.GetHostIPs()[candidateIP] {
							return candidateIP, nil
						}
					}
				}
			}
		}
	}

	// 3. Check known DHCP lease files
	leaseFiles := []string{
		"/var/lib/misc/dnsmasq.leases",
		"/var/lib/dhcp/dhclient.leases",
		"/var/lib/dhcpcd/dhcpcd.leases",
	}
	for _, f := range leaseFiles {
		if data, err := os.ReadFile(f); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.Contains(strings.ToLower(line), cleanMAC) {
					fields := strings.Fields(line)
					for _, fld := range fields {
						if parsed := net.ParseIP(fld); parsed != nil && parsed.To4() != nil {
							if !m.GetHostIPs()[fld] {
								return fld, nil
							}
						}
					}
				}
			}
		}
	}

	// 4. Try active DHCP lease query to the router for this MAC
	if lease, err := m.AcquireDHCPLease(ctx, cleanMAC); err == nil && lease != nil && lease.IP != nil {
		ipCand := lease.IP.String()
		if !m.GetHostIPs()[ipCand] {
			return ipCand, nil
		}
	}

	return "", fmt.Errorf("ip not found in arp/neighbor cache or dhcp for mac %s", mac)
}

// ListHostTAPs returns all TAP devices currently present on the host matching "mftap-"
func (m *Manager) ListHostTAPs(ctx context.Context) ([]string, error) {
	out, err := m.runner.Run(ctx, "ip", "-o", "link", "show")
	if err != nil {
		return nil, err
	}
	var taps []string
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			name := strings.TrimSuffix(fields[1], ":")
			if idx := strings.Index(name, "@"); idx != -1 {
				name = name[:idx]
			}
			if strings.HasPrefix(name, "mftap-") {
				taps = append(taps, name)
			}
		}
	}
	return taps, nil
}

// PruneOrphanTAPs deletes host TAP devices that are known to this manager but
// are no longer active. knownTAPs is the set of TAP names recorded on this
// store's VM records. Hosts may run more than one Compute deployment (a control
// plane plus a worker, or several installations), and each one creates
// "mftap-*" devices; blindly deleting every "mftap-*" device that is not in
// this manager's active set tears down another deployment's running sandboxes.
func (m *Manager) PruneOrphanTAPs(ctx context.Context, activeTAPs, knownTAPs map[string]bool) ([]string, error) {
	hostTAPs, err := m.ListHostTAPs(ctx)
	if err != nil {
		return nil, err
	}
	var pruned []string
	for _, tap := range hostTAPs {
		if activeTAPs[tap] || !knownTAPs[tap] {
			continue
		}
		m.RemoveAntiSpoofAndIsolation(ctx, IsolationConfig{TAPDevice: tap})
		if _, err := m.runner.Run(ctx, "ip", "link", "del", "dev", tap); err == nil {
			pruned = append(pruned, tap)
		}
	}
	return pruned, nil
}

// AllocateIP delegates to IPAM with comprehensive conflict detection against host IPs, neighbor devices, and active network devices
func (m *Manager) AllocateIP(usedIPs map[string]bool) (string, string, error) {
	mergedUsed := make(map[string]bool)
	for k, v := range usedIPs {
		mergedUsed[k] = v
	}

	// Also mark currently in-flight allocated IPs as used so concurrent requests do not collide
	m.inFlightMu.Lock()
	for ip := range m.inFlightIPs {
		mergedUsed[ip] = true
	}
	m.inFlightMu.Unlock()

	// Mark host IPs as used
	for hIP := range m.GetHostIPs() {
		mergedUsed[hIP] = true
	}

	// Mark gateway as used
	if gw := m.GetGatewayIP(); gw != "" {
		mergedUsed[gw] = true
	}

	// Mark all known neighbor IPs on network as used
	for nIP := range m.GetNeighborIPs(context.Background()) {
		mergedUsed[nIP] = true
	}

	// Iterate through candidates and verify each is not in use on the network
	for attempts := 0; attempts < 256; attempts++ {
		candidateIP, mac, err := m.ipam.AllocateNextIP(mergedUsed)
		if err != nil {
			return "", "", err
		}

		// Optimistically reserve in-flight so parallel allocators immediately try another IP
		m.inFlightMu.Lock()
		if m.inFlightIPs[candidateIP] {
			m.inFlightMu.Unlock()
			mergedUsed[candidateIP] = true
			continue
		}
		m.inFlightIPs[candidateIP] = true
		m.inFlightMu.Unlock()

		// Double-check active reachability/conflict without holding global manager lock
		if m.IsIPInUse(context.Background(), candidateIP, mac) {
			m.inFlightMu.Lock()
			delete(m.inFlightIPs, candidateIP)
			m.inFlightMu.Unlock()

			mergedUsed[candidateIP] = true
			continue
		}

		// Automatically release in-flight reservation after 30 seconds
		// allowing DB persistence to record it in usedIPs permanently
		go func(reservedIP string) {
			time.Sleep(30 * time.Second)
			m.inFlightMu.Lock()
			delete(m.inFlightIPs, reservedIP)
			m.inFlightMu.Unlock()
		}(candidateIP)

		return candidateIP, mac, nil
	}

	m.mu.RLock()
	sub := m.cfg.SubnetCIDR
	m.mu.RUnlock()
	return "", "", fmt.Errorf("no available unconflicted IP addresses in subnet %s", sub)
}

// ValidateIP validates if a user-supplied IP is valid and available
func (m *Manager) ValidateIP(ip string, usedIPs map[string]bool) error {
	if err := m.ipam.ValidateIP(ip, usedIPs); err != nil {
		return err
	}
	if m.GetHostIPs()[ip] {
		return fmt.Errorf("IP address %s is currently used by the host system", ip)
	}
	if m.IsIPInUse(context.Background(), ip, "") {
		return fmt.Errorf("IP address %s is already in use by another device on the network", ip)
	}
	return nil
}

func (m *Manager) getGatewayIPLocked() string {
	if m.cfg.GatewayIP != "" {
		return m.cfg.GatewayIP
	}
	ip, _, err := net.ParseCIDR(m.cfg.BridgeIP)
	if err != nil {
		return "172.16.0.1"
	}
	return ip.String()
}

// GetGatewayIP returns the configured gateway IPv4 address, or extracts it from bridge IP
func (m *Manager) GetGatewayIP() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.getGatewayIPLocked()
}

// GetNetmask returns the netmask of the configured bridge/subnet
func (m *Manager) GetNetmask() string {
	return m.ipam.GetNetmask()
}

// GetInfo returns a complete snapshot of current network topology and state
func (m *Manager) GetInfo(ctx context.Context, allocatedCount int, activeTAPs []string) NetworkInfo {
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()

	var bridgeActive bool
	out, err := m.runner.Run(ctx, "ip", "link", "show", cfg.BridgeName)
	if err == nil && strings.Contains(string(out), "UP") {
		bridgeActive = true
	}

	info := NetworkInfo{
		Mode:           cfg.Mode,
		BridgeName:     cfg.BridgeName,
		BridgeIP:       cfg.BridgeIP,
		SubnetCIDR:     cfg.SubnetCIDR,
		GatewayIP:      m.GetGatewayIP(),
		Netmask:        m.GetNetmask(),
		PhysicalNIC:    cfg.PhysicalNIC,
		BridgeActive:   bridgeActive,
		NATEnabled:     cfg.Mode == ModeNAT,
		WiredDedicated: cfg.Mode == ModeNIC || cfg.Mode == ModeWired,
		ActiveTAPs:     activeTAPs,
		AllocatedIPs:   allocatedCount,
		LinkedBridges:  cfg.LinkedBridges,
	}

	return info
}

// GetNetworkInfo returns network info with the signature expected by callers
func (m *Manager) GetNetworkInfo(ctx context.Context, activeTAPs []string, allocatedCount int) (*NetworkInfo, error) {
	info := m.GetInfo(ctx, allocatedCount, activeTAPs)
	return &info, nil
}
