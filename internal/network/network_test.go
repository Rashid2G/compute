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

func TestWiredDedicatedIP(t *testing.T) {
	ctx := context.Background()

	runnerWired := &mockRunner{}
	cfgWired := DefaultWiredConfig("enp3s0", "192.168.1.0/24", "192.168.1.1")
	mgrWired, err := NewManager(cfgWired, runnerWired)
	if err != nil {
		t.Fatalf("failed to create Wired manager: %v", err)
	}

	if mgrWired.GetGatewayIP() != "192.168.1.1" {
		t.Fatalf("expected custom gateway 192.168.1.1, got %s", mgrWired.GetGatewayIP())
	}

	if err := mgrWired.EnsureNetwork(ctx); err != nil {
		t.Fatalf("Wired EnsureNetwork failed: %v", err)
	}

	var enslaved bool
	for _, cmd := range runnerWired.commands {
		str := strings.Join(cmd, " ")
		if strings.Contains(str, "ip link set dev enp3s0 master") {
			enslaved = true
			break
		}
	}
	if !enslaved {
		t.Fatalf("expected physical NIC enp3s0 to be enslaved to bridge")
	}

	info, err := mgrWired.GetNetworkInfo(ctx, []string{"mftap-1"}, 1)
	if err != nil {
		t.Fatalf("GetNetworkInfo error: %v", err)
	}
	if !info.WiredDedicated {
		t.Fatalf("expected WiredDedicated to be true")
	}
	if info.GatewayIP != "192.168.1.1" {
		t.Fatalf("expected GatewayIP 192.168.1.1, got %s", info.GatewayIP)
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

func TestLinkOtherBridgesAndDHCP(t *testing.T) {
	runner := &mockRunner{
		output: map[string]string{
			"link show type bridge": "1: br0: <BROADCAST>\n2: virbr0: <BROADCAST>\n3: mft3br0: <BROADCAST>\n",
			"route show default":    "default via 192.168.1.1 dev eth0 proto dhcp src 192.168.1.50\n",
			"ip neigh show":         "192.168.1.120 dev mft3br0 lladdr 06:00:c0:a8:01:78 REACHABLE\n",
		},
	}
	cfg := DefaultNATConfig()
	mgr, err := NewManager(cfg, runner)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()
	if err := mgr.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork failed: %v", err)
	}

	linked, err := mgr.LinkOtherBridges(ctx)
	if err != nil {
		t.Fatalf("LinkOtherBridges error: %v", err)
	}
	if len(linked) != 2 {
		t.Fatalf("expected 2 linked bridges (br0, virbr0), got %v", linked)
	}

	ip, err := mgr.ResolveGuestIPByMAC(ctx, "06:00:c0:a8:01:78")
	if err != nil {
		t.Fatalf("ResolveGuestIPByMAC failed: %v", err)
	}
	if ip != "192.168.1.120" {
		t.Fatalf("expected IP 192.168.1.120, got %s", ip)
	}

	if mgr.GetGatewayIP() != "172.16.0.1" {
		t.Fatalf("expected gateway 172.16.0.1, got %s", mgr.GetGatewayIP())
	}
}

func TestConflictDetectionAndNeighborUsed(t *testing.T) {
	runner := &mockRunner{
		output: map[string]string{
			"ip neigh show": "192.168.1.3 dev eth0 lladdr 00:11:22:33:44:55 REACHABLE\n192.168.1.4 dev eth0 lladdr 00:aa:bb:cc:dd:ee STALE\n",
		},
	}
	cfg := DefaultWiredConfig("eth0", "192.168.1.0/24", "192.168.1.1")
	mgr, err := NewManager(cfg, runner)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// 192.168.1.3 and 192.168.1.4 are active devices on the physical network.
	// AllocateIP should automatically skip them!
	used := map[string]bool{}
	allocatedIP, _, err := mgr.AllocateIP(used)
	if err != nil {
		t.Fatalf("AllocateIP failed: %v", err)
	}

	// Offset 2 is 192.168.1.2. If 192.168.1.2 was used, it would try .3 (skipped), .4 (skipped), .5.
	// Since 192.168.1.2 is bridge IP, allocatedIP should be .5 or .2 if .2 wasn't used.
	if allocatedIP == "192.168.1.3" || allocatedIP == "192.168.1.4" {
		t.Fatalf("allocated IP %s collided with active network device in neighbor table!", allocatedIP)
	}

	// ValidateIP should reject 192.168.1.3
	ctx := context.Background()
	if !mgr.IsIPInUse(ctx, "192.168.1.3", "") {
		t.Fatalf("expected 192.168.1.3 to be reported as in use")
	}
	if err := mgr.ValidateIP("192.168.1.3", used); err == nil {
		t.Fatalf("expected ValidateIP to reject 192.168.1.3 due to network conflict")
	}
}

func TestResolveGuestIPDHCPFallback(t *testing.T) {
	runner := &mockRunner{
		output: map[string]string{
			"link show": "1: eth0: <BROADCAST>",
			"udhcpc":    "DHCP_IP=192.168.1.99\nDHCP_MASK=255.255.255.0\nDHCP_ROUTER=192.168.1.1\nDHCP_SERVERID=192.168.1.1\nDHCP_LEASE=7200\n",
		},
	}
	cfg := DefaultWiredConfig("eth0", "192.168.1.0/24", "192.168.1.1")
	mgr, err := NewManager(cfg, runner)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	ctx := context.Background()
	ip, err := mgr.ResolveGuestIPByMAC(ctx, "06:00:55:66:77:88")
	if err != nil {
		t.Fatalf("ResolveGuestIPByMAC failed: %v", err)
	}
	if ip != "192.168.1.99" {
		t.Fatalf("expected leased IP 192.168.1.99, got %s", ip)
	}
}
