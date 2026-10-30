package network

import (
	"context"
	"net"
	"strings"
	"testing"
)

type mockRunner struct {
	commands [][]string
	output   map[string]string
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := append([]string{name}, args...)
	m.commands = append(m.commands, cmd)

	fullCmd := strings.Join(cmd, " ")
	for k, v := range m.output {
		if strings.Contains(fullCmd, k) {
			return []byte(v), nil
		}
	}
	return []byte("ok"), nil
}

func TestIPAM(t *testing.T) {
	ipam, err := NewIPAM("172.16.0.0/16")
	if err != nil {
		t.Fatalf("failed to create IPAM: %v", err)
	}

	used := map[string]bool{
		"172.16.0.2": true,
	}

	ip, mac, err := ipam.AllocateNextIP(used)
	if err != nil {
		t.Fatalf("allocation failed: %v", err)
	}

	if ip != "172.16.0.3" {
		t.Fatalf("expected 172.16.0.3, got %s", ip)
	}

	expectedMAC := GenerateMAC(net.ParseIP("172.16.0.3"))
	if mac != expectedMAC {
		t.Fatalf("expected mac %s, got %s", expectedMAC, mac)
	}

	if ipam.GetNetmask() != "255.255.0.0" {
		t.Fatalf("expected 255.255.0.0, got %s", ipam.GetNetmask())
	}

	// Validation tests
	if err := ipam.ValidateIP("172.16.0.5", used); err != nil {
		t.Fatalf("expected valid IP, got: %v", err)
	}
	if err := ipam.ValidateIP("172.16.0.2", used); err == nil {
		t.Fatalf("expected error for already allocated IP")
	}
	if err := ipam.ValidateIP("172.16.0.1", used); err == nil {
		t.Fatalf("expected error for gateway IP")
	}
	if err := ipam.ValidateIP("192.168.1.1", used); err == nil {
		t.Fatalf("expected error for out of subnet IP")
	}
}

func TestNetworkManagerModes(t *testing.T) {
	ctx := context.Background()

	// 1. NAT Mode
	runnerNAT := &mockRunner{}
	cfgNAT := DefaultNATConfig()
	mgrNAT, err := NewManager(cfgNAT, runnerNAT)
	if err != nil {
		t.Fatalf("failed to create NAT manager: %v", err)
	}
	if err := mgrNAT.EnsureNetwork(ctx); err != nil {
		t.Fatalf("NAT EnsureNetwork failed: %v", err)
	}

	// 2. Bridge Mode
	runnerBridge := &mockRunner{}
	cfgBridge := DefaultBridgeConfig()
	mgrBridge, err := NewManager(cfgBridge, runnerBridge)
	if err != nil {
		t.Fatalf("failed to create Bridge manager: %v", err)
	}
	if err := mgrBridge.EnsureNetwork(ctx); err != nil {
		t.Fatalf("Bridge EnsureNetwork failed: %v", err)
	}

	// 3. NIC Mode
	runnerNIC := &mockRunner{}
	cfgNIC := DefaultNICConfig("eth0")
	mgrNIC, err := NewManager(cfgNIC, runnerNIC)
	if err != nil {
		t.Fatalf("failed to create NIC manager: %v", err)
	}
	if err := mgrNIC.EnsureNetwork(ctx); err != nil {
		t.Fatalf("NIC EnsureNetwork failed: %v", err)
	}

	// Check that physical NIC was enslaved
	var enslaved bool
	for _, cmd := range runnerNIC.commands {
		str := strings.Join(cmd, " ")
		if strings.Contains(str, "ip link set dev eth0 master") {
			enslaved = true
			break
		}
	}
	if !enslaved {
		t.Fatalf("expected physical NIC eth0 to be enslaved to bridge")
	}
}

func TestNetworkManagerTAPAndPrune(t *testing.T) {
	runner := &mockRunner{
		output: map[string]string{
			"link show": "1: lo: <LOOPBACK>\n150: mftap-active: <BROADCAST>\n151: mftap-orphan: <BROADCAST>\n",
		},
	}
	cfg := DefaultNATConfig()
	mgr, err := NewManager(cfg, runner)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()
	if err := mgr.SetupTAP(ctx, "mftap-test"); err != nil {
		t.Fatalf("SetupTAP failed: %v", err)
	}
	if err := mgr.RemoveTAP(ctx, "mftap-test"); err != nil {
		t.Fatalf("RemoveTAP failed: %v", err)
	}

	activeTAPs := map[string]bool{
		"mftap-active": true,
	}
	pruned, err := mgr.PruneOrphanTAPs(ctx, activeTAPs)
	if err != nil {
		t.Fatalf("PruneOrphanTAPs failed: %v", err)
	}
	if len(pruned) != 1 || pruned[0] != "mftap-orphan" {
		t.Fatalf("expected pruned [mftap-orphan], got %v", pruned)
	}
}
