package network

import (
	"context"
	"net"
	"testing"
)

type mockRunner struct {
	commands [][]string
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := append([]string{name}, args...)
	m.commands = append(m.commands, cmd)
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
}

func TestNetworkManager(t *testing.T) {
	runner := &mockRunner{}
	cfg := Config{
		BridgeName: "testbr0",
		BridgeIP:   "172.16.0.1/16",
		SubnetCIDR: "172.16.0.0/16",
	}

	mgr, err := NewManager(cfg, runner)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()
	if err := mgr.EnsureBridge(ctx); err != nil {
		t.Fatalf("EnsureBridge failed: %v", err)
	}

	if err := mgr.SetupTAP(ctx, "testtap0"); err != nil {
		t.Fatalf("SetupTAP failed: %v", err)
	}

	if err := mgr.RemoveTAP(ctx, "testtap0"); err != nil {
		t.Fatalf("RemoveTAP failed: %v", err)
	}

	if mgr.GetGatewayIP() != "172.16.0.1" {
		t.Fatalf("expected gateway 172.16.0.1, got %s", mgr.GetGatewayIP())
	}
}
