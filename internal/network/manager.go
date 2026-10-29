package network

import (
	"context"
	"fmt"
	"net"
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

// Config defines network parameters
type Config struct {
	BridgeName string `json:"bridge_name"`
	BridgeIP   string `json:"bridge_ip"` // e.g. "172.16.0.1/16"
	SubnetCIDR string `json:"subnet"`    // e.g. "172.16.0.0/16"
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

// EnsureBridge checks if bridge exists; if not, creates and configures it
func (m *Manager) EnsureBridge(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if bridge exists
	out, err := m.runner.Run(ctx, "ip", "link", "show", m.cfg.BridgeName)
	if err != nil || !strings.Contains(string(out), m.cfg.BridgeName) {
		// Create bridge
		if _, err := m.runner.Run(ctx, "ip", "link", "add", m.cfg.BridgeName, "type", "bridge"); err != nil {
			return fmt.Errorf("failed to create bridge %s: %w", m.cfg.BridgeName, err)
		}
	}

	// Assign IP to bridge if not set
	m.runner.Run(ctx, "ip", "addr", "add", m.cfg.BridgeIP, "dev", m.cfg.BridgeName)

	// Bring bridge up
	if _, err := m.runner.Run(ctx, "ip", "link", "set", m.cfg.BridgeName, "up"); err != nil {
		return fmt.Errorf("failed to bring up bridge %s: %w", m.cfg.BridgeName, err)
	}

	// Enable IP forwarding
	m.runner.Run(ctx, "sysctl", "-w", "net.ipv4.ip_forward=1")

	// Setup NAT rules
	m.setupNAT(ctx)

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

// SetupTAP creates a tap interface, attaches it to the bridge, and brings it up
func (m *Manager) SetupTAP(ctx context.Context, tapName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if already exists
	_, err := m.runner.Run(ctx, "ip", "link", "show", tapName)
	if err == nil {
		// Exists, make sure it is attached to bridge and up
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

	_, err := m.runner.Run(ctx, "ip", "link", "del", "dev", tapName)
	return err
}

// AllocateIP delegates to IPAM
func (m *Manager) AllocateIP(usedIPs map[string]bool) (string, string, error) {
	return m.ipam.AllocateNextIP(usedIPs)
}

// GetGatewayIP returns the bridge IPv4 address without subnet mask
func (m *Manager) GetGatewayIP() string {
	ip, _, err := net.ParseCIDR(m.cfg.BridgeIP)
	if err != nil {
		return "172.16.0.1"
	}
	return ip.String()
}
