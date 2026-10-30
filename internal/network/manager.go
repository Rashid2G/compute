package network

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
)

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
	ModeNAT    Mode = "nat"    // Virtual bridge behind virtual NAT (default)
	ModeBridge Mode = "bridge" // Virtual bridge (L2 only, no NAT)
	ModeNIC    Mode = "nic"    // Assigned to a real physical NIC via bridge
)

// Config defines network parameters
type Config struct {
	Mode        Mode   `json:"mode"`
	BridgeName  string `json:"bridge_name"`
	BridgeIP    string `json:"bridge_ip"`              // e.g. "172.16.0.1/16"
	SubnetCIDR  string `json:"subnet"`                 // e.g. "172.16.0.0/16"
	PhysicalNIC string `json:"physical_nic,omitempty"` // e.g. "eth0"
}

// DefaultNATConfig returns standard NAT network configuration
func DefaultNATConfig() Config {
	return Config{
		Mode:       ModeNAT,
		BridgeName: "mft3br0",
		BridgeIP:   "172.16.0.1/16",
		SubnetCIDR: "172.16.0.0/16",
	}
}

// DefaultBridgeConfig returns standalone virtual bridge configuration (no NAT masquerade)
func DefaultBridgeConfig() Config {
	return Config{
		Mode:       ModeBridge,
		BridgeName: "mft3br0",
		BridgeIP:   "172.16.0.1/16",
		SubnetCIDR: "172.16.0.0/16",
	}
}

// DefaultNICConfig returns physical NIC bridge configuration
func DefaultNICConfig(physicalNIC string) Config {
	if physicalNIC == "" {
		physicalNIC = "eth0"
	}
	return Config{
		Mode:        ModeNIC,
		BridgeName:  "mft3br0",
		BridgeIP:    "172.16.0.1/16",
		SubnetCIDR:  "172.16.0.0/16",
		PhysicalNIC: physicalNIC,
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
	Mode         Mode     `json:"mode"`
	BridgeName   string   `json:"bridge_name"`
	BridgeIP     string   `json:"bridge_ip"`
	SubnetCIDR   string   `json:"subnet"`
	GatewayIP    string   `json:"gateway_ip"`
	Netmask      string   `json:"netmask"`
	PhysicalNIC  string   `json:"physical_nic,omitempty"`
	BridgeActive bool     `json:"bridge_active"`
	NATEnabled   bool     `json:"nat_enabled"`
	ActiveTAPs   []string `json:"active_taps"`
	AllocatedIPs int      `json:"allocated_ips"`
}

// Manager handles bridge, tap, and iptables NAT configuration
type Manager struct {
	cfg    Config
	ipam   *IPAM
	runner Runner
	mu     sync.Mutex
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
		cfg.BridgeIP = "172.16.0.1/16"
	}
	if cfg.SubnetCIDR == "" {
		cfg.SubnetCIDR = "172.16.0.0/16"
	}

	ipam, err := NewIPAM(cfg.SubnetCIDR)
	if err != nil {
		return nil, err
	}

	return &Manager{
		cfg:    cfg,
		ipam:   ipam,
		runner: runner,
	}, nil
}

// GetConfig returns the active configuration
func (m *Manager) GetConfig() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// EnsureBridge is a backwards-compatible alias for EnsureNetwork
func (m *Manager) EnsureBridge(ctx context.Context) error {
	return m.EnsureNetwork(ctx)
}

// EnsureNetwork ensures the bridge, routing, NAT, or physical NIC enslavement is configured
func (m *Manager) EnsureNetwork(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. Check if bridge exists, create if missing
	out, err := m.runner.Run(ctx, "ip", "link", "show", m.cfg.BridgeName)
	if err != nil || !strings.Contains(string(out), m.cfg.BridgeName) {
		if _, err := m.runner.Run(ctx, "ip", "link", "add", m.cfg.BridgeName, "type", "bridge"); err != nil {
			return fmt.Errorf("failed to create bridge %s: %w", m.cfg.BridgeName, err)
		}
	}

	// 2. Assign IP to bridge if not already assigned
	if m.cfg.BridgeIP != "" {
		gwIP := m.GetGatewayIP()
		addrOut, _ := m.runner.Run(ctx, "ip", "-o", "-4", "addr", "show", "dev", m.cfg.BridgeName)
		if !strings.Contains(string(addrOut), gwIP) {
			_, _ = m.runner.Run(ctx, "ip", "addr", "add", m.cfg.BridgeIP, "dev", m.cfg.BridgeName)
		}
	}

	// 3. Bring bridge up
	if _, err := m.runner.Run(ctx, "ip", "link", "set", m.cfg.BridgeName, "up"); err != nil {
		return fmt.Errorf("failed to bring up bridge %s: %w", m.cfg.BridgeName, err)
	}

	// 4. Configure topology according to Mode
	switch m.cfg.Mode {
	case ModeNAT:
		// Enable IP forwarding
		m.runner.Run(ctx, "sysctl", "-w", "net.ipv4.ip_forward=1")
		m.setupNAT(ctx)

	case ModeBridge:
		// Standard virtual bridge, allow forwarding across bridge ports without NAT masquerading
		m.runner.Run(ctx, "sysctl", "-w", "net.ipv4.ip_forward=1")
		m.setupBridgeForward(ctx)

	case ModeNIC:
		// Attach physical NIC to bridge
		if m.cfg.PhysicalNIC != "" {
			_, _ = m.runner.Run(ctx, "ip", "link", "set", "dev", m.cfg.PhysicalNIC, "master", m.cfg.BridgeName)
			_, _ = m.runner.Run(ctx, "ip", "link", "set", "dev", m.cfg.PhysicalNIC, "up")
		}
		m.runner.Run(ctx, "sysctl", "-w", "net.ipv4.ip_forward=1")
		m.setupBridgeForward(ctx)
	}

	return nil
}

func (m *Manager) setupNAT(ctx context.Context) {
	// Add MASQUERADE rule if not present
	out, _ := m.runner.Run(ctx, "iptables", "-t", "nat", "-C", "POSTROUTING", "-s", m.cfg.SubnetCIDR, "!", "-o", m.cfg.BridgeName, "-j", "MASQUERADE")
	if len(out) != 0 || out == nil {
		m.runner.Run(ctx, "iptables", "-t", "nat", "-A", "POSTROUTING", "-s", m.cfg.SubnetCIDR, "!", "-o", m.cfg.BridgeName, "-j", "MASQUERADE")
	}

	// Add FORWARD rules
	m.runner.Run(ctx, "iptables", "-I", "FORWARD", "-i", m.cfg.BridgeName, "-j", "ACCEPT")
	m.runner.Run(ctx, "iptables", "-I", "FORWARD", "-o", m.cfg.BridgeName, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")
}

func (m *Manager) setupBridgeForward(ctx context.Context) {
	// Add FORWARD rule for internal bridge forwarding
	out, _ := m.runner.Run(ctx, "iptables", "-C", "FORWARD", "-i", m.cfg.BridgeName, "-o", m.cfg.BridgeName, "-j", "ACCEPT")
	if len(out) != 0 || out == nil {
		m.runner.Run(ctx, "iptables", "-I", "FORWARD", "-i", m.cfg.BridgeName, "-o", m.cfg.BridgeName, "-j", "ACCEPT")
	}
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

// RemoveTAP deletes a TAP interface
func (m *Manager) RemoveTAP(ctx context.Context, tapName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if tapName == "" {
		return nil
	}
	_, err := m.runner.Run(ctx, "ip", "link", "del", "dev", tapName)
	return err
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

// PruneOrphanTAPs deletes any TAP devices matching "mftap-" that are not in the activeTAPs map
func (m *Manager) PruneOrphanTAPs(ctx context.Context, activeTAPs map[string]bool) ([]string, error) {
	hostTAPs, err := m.ListHostTAPs(ctx)
	if err != nil {
		return nil, err
	}
	var pruned []string
	for _, tap := range hostTAPs {
		if !activeTAPs[tap] {
			if _, err := m.runner.Run(ctx, "ip", "link", "del", "dev", tap); err == nil {
				pruned = append(pruned, tap)
			}
		}
	}
	return pruned, nil
}

// AllocateIP delegates to IPAM
func (m *Manager) AllocateIP(usedIPs map[string]bool) (string, string, error) {
	return m.ipam.AllocateNextIP(usedIPs)
}

// ValidateIP validates if a user-supplied IP is valid and available
func (m *Manager) ValidateIP(ip string, usedIPs map[string]bool) error {
	return m.ipam.ValidateIP(ip, usedIPs)
}

// GetGatewayIP returns the bridge IPv4 address without subnet mask
func (m *Manager) GetGatewayIP() string {
	ip, _, err := net.ParseCIDR(m.cfg.BridgeIP)
	if err != nil {
		return "172.16.0.1"
	}
	return ip.String()
}

// GetNetmask returns the IPv4 netmask string (e.g. "255.255.0.0")
func (m *Manager) GetNetmask() string {
	return m.ipam.GetNetmask()
}

// GetNetworkInfo collects runtime state and returns a summary
func (m *Manager) GetNetworkInfo(ctx context.Context, activeTAPs []string, allocatedIPsCount int) (*NetworkInfo, error) {
	out, err := m.runner.Run(ctx, "ip", "link", "show", m.cfg.BridgeName)
	bridgeActive := err == nil && strings.Contains(string(out), "state UP")

	return &NetworkInfo{
		Mode:         m.cfg.Mode,
		BridgeName:   m.cfg.BridgeName,
		BridgeIP:     m.cfg.BridgeIP,
		SubnetCIDR:   m.cfg.SubnetCIDR,
		GatewayIP:    m.GetGatewayIP(),
		Netmask:      m.GetNetmask(),
		PhysicalNIC:  m.cfg.PhysicalNIC,
		BridgeActive: bridgeActive,
		NATEnabled:   m.cfg.Mode == ModeNAT,
		ActiveTAPs:   activeTAPs,
		AllocatedIPs: allocatedIPsCount,
	}, nil
}
