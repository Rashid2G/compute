package network

import (
	"context"
	"strings"
	"testing"

	"github.com/Rashid2G/on-demand-vms/internal/db"
)

func TestAntiSpoofRuleGeneration(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{}
	cfg := DefaultNATConfig()
	mgr, err := NewManager(cfg, runner)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	isoCfg := IsolationConfig{
		TAPDevice:  "mftap-vm1",
		IPAddress:  "172.16.0.15",
		MACAddress: "06:00:ac:10:00:0f",
		OwnerID:    "tenant-alpha",
	}

	if err := mgr.ApplyAntiSpoofAndIsolation(ctx, isoCfg); err != nil {
		t.Fatalf("ApplyAntiSpoofAndIsolation failed: %v", err)
	}

	var hasBridgeIsolated bool
	var hasDHCPGuard bool
	var hasIPAntiSpoof bool
	var hasMACAntiSpoof bool
	var hasARPMACAntiSpoof bool
	var hasARPIPAntiSpoof bool
	var hasCrossTenantDropOut bool
	var hasCrossTenantDropIn bool

	for _, cmd := range runner.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "bridge link set dev mftap-vm1 isolated on") {
			hasBridgeIsolated = true
		}
		if strings.Contains(line, "iptables -I FORWARD 1 -i mftap-vm1 -p udp --sport 67 -j DROP") {
			hasDHCPGuard = true
		}
		if strings.Contains(line, "iptables -I FORWARD 1 -i mftap-vm1 ! -s 172.16.0.15 -j DROP") {
			hasIPAntiSpoof = true
		}
		if strings.Contains(line, "ebtables -I FORWARD 1 -i mftap-vm1 ! -s 06:00:ac:10:00:0f -j DROP") {
			hasMACAntiSpoof = true
		}
		if strings.Contains(line, "arptables -I FORWARD 1 -i mftap-vm1 ! --source-mac 06:00:ac:10:00:0f -j DROP") {
			hasARPMACAntiSpoof = true
		}
		if strings.Contains(line, "arptables -I FORWARD 1 -i mftap-vm1 ! --source-ip 172.16.0.15 -j DROP") {
			hasARPIPAntiSpoof = true
		}
		if strings.Contains(line, "iptables -A FORWARD -i mftap-vm1 -o mftap-+ -j DROP") {
			hasCrossTenantDropOut = true
		}
		if strings.Contains(line, "iptables -A FORWARD -i mftap-+ -o mftap-vm1 -j DROP") {
			hasCrossTenantDropIn = true
		}
	}

	if !hasBridgeIsolated {
		t.Errorf("expected bridge link isolated on for mftap-vm1")
	}
	if !hasDHCPGuard {
		t.Errorf("expected rogue DHCP server guard dropping udp sport 67")
	}
	if !hasIPAntiSpoof {
		t.Errorf("expected L3 IP anti-spoofing rule for 172.16.0.15")
	}
	if !hasMACAntiSpoof {
		t.Errorf("expected L2 MAC anti-spoofing rule for 06:00:ac:10:00:0f")
	}
	if !hasARPMACAntiSpoof {
		t.Errorf("expected ARP MAC anti-spoofing rule")
	}
	if !hasARPIPAntiSpoof {
		t.Errorf("expected ARP IP anti-spoofing rule")
	}
	if !hasCrossTenantDropOut || !hasCrossTenantDropIn {
		t.Errorf("expected cross-tenant lateral movement drop rules for mftap-vm1")
	}
}

func TestBridgeIsolationIntraTenant(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{}
	cfg := DefaultNATConfig()
	mgr, err := NewManager(cfg, runner)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	isoCfg := IsolationConfig{
		TAPDevice:       "mftap-vm1",
		IPAddress:       "172.16.0.15",
		MACAddress:      "06:00:ac:10:00:0f",
		OwnerID:         "org-team-a",
		AllowedPeerTAPs: []string{"mftap-vm2", "mftap-vm3"},
	}

	if err := mgr.ApplyAntiSpoofAndIsolation(ctx, isoCfg); err != nil {
		t.Fatalf("ApplyAntiSpoofAndIsolation failed: %v", err)
	}

	var hasAcceptVM2Out, hasAcceptVM2In bool
	var hasAcceptVM3Out, hasAcceptVM3In bool
	for _, cmd := range runner.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "-i mftap-vm1 -o mftap-vm2 -j ACCEPT") {
			hasAcceptVM2Out = true
		}
		if strings.Contains(line, "-i mftap-vm2 -o mftap-vm1 -j ACCEPT") {
			hasAcceptVM2In = true
		}
		if strings.Contains(line, "-i mftap-vm1 -o mftap-vm3 -j ACCEPT") {
			hasAcceptVM3Out = true
		}
		if strings.Contains(line, "-i mftap-vm3 -o mftap-vm1 -j ACCEPT") {
			hasAcceptVM3In = true
		}
	}

	if !hasAcceptVM2Out || !hasAcceptVM2In {
		t.Errorf("expected bi-directional intra-tenant ACCEPT rules between mftap-vm1 and mftap-vm2")
	}
	if !hasAcceptVM3Out || !hasAcceptVM3In {
		t.Errorf("expected bi-directional intra-tenant ACCEPT rules between mftap-vm1 and mftap-vm3")
	}
}

func TestIsolationCleanupAndTeardown(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{}
	cfg := DefaultNATConfig()
	mgr, err := NewManager(cfg, runner)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	isoCfg := IsolationConfig{
		TAPDevice:       "mftap-vm1",
		IPAddress:       "172.16.0.15",
		MACAddress:      "06:00:ac:10:00:0f",
		OwnerID:         "tenant-b",
		AllowedPeerTAPs: []string{"mftap-vm2"},
	}

	runner.commands = nil
	if err := mgr.RemoveAntiSpoofAndIsolation(ctx, isoCfg); err != nil {
		t.Fatalf("RemoveAntiSpoofAndIsolation failed: %v", err)
	}

	var deletedCrossTenantDrop bool
	var deletedIntraTenantAccept bool
	var deletedDHCPGuard bool
	var deletedIPAntiSpoof bool
	var deletedMACAntiSpoof bool
	var resetBridgeIsolation bool

	for _, cmd := range runner.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "iptables -D FORWARD -i mftap-vm1 -o mftap-+ -j DROP") {
			deletedCrossTenantDrop = true
		}
		if strings.Contains(line, "iptables -D FORWARD -i mftap-vm1 -o mftap-vm2 -j ACCEPT") {
			deletedIntraTenantAccept = true
		}
		if strings.Contains(line, "iptables -D FORWARD -i mftap-vm1 -p udp --sport 67 -j DROP") {
			deletedDHCPGuard = true
		}
		if strings.Contains(line, "iptables -D FORWARD -i mftap-vm1 ! -s 172.16.0.15 -j DROP") {
			deletedIPAntiSpoof = true
		}
		if strings.Contains(line, "ebtables -D FORWARD -i mftap-vm1 ! -s 06:00:ac:10:00:0f -j DROP") {
			deletedMACAntiSpoof = true
		}
		if strings.Contains(line, "bridge link set dev mftap-vm1 isolated off") {
			resetBridgeIsolation = true
		}
	}

	if !deletedCrossTenantDrop {
		t.Errorf("expected deletion of cross-tenant drop rules")
	}
	if !deletedIntraTenantAccept {
		t.Errorf("expected deletion of intra-tenant accept rules")
	}
	if !deletedDHCPGuard {
		t.Errorf("expected deletion of DHCP guard rule")
	}
	if !deletedIPAntiSpoof {
		t.Errorf("expected deletion of IP anti-spoof rule")
	}
	if !deletedMACAntiSpoof {
		t.Errorf("expected deletion of MAC anti-spoof rule")
	}
	if !resetBridgeIsolation {
		t.Errorf("expected bridge link isolated set to off")
	}
}

func TestCleanTeardownOnRemoveTAPAndPrune(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{
		output: map[string]string{
			"link show": "1: lo: <LOOPBACK>\n100: mftap-orphan1: <BROADCAST>\n",
		},
	}
	cfg := DefaultNATConfig()
	mgr, err := NewManager(cfg, runner)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// RemoveTAP should clean up isolation before deleting link
	runner.commands = nil
	if err := mgr.RemoveTAP(ctx, "mftap-active"); err != nil {
		t.Fatalf("RemoveTAP failed: %v", err)
	}
	var tapRemoved, rulesCleaned bool
	for _, cmd := range runner.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "ip link del dev mftap-active") {
			tapRemoved = true
		}
		if strings.Contains(line, "iptables -D FORWARD -i mftap-active -o mftap-+ -j DROP") {
			rulesCleaned = true
		}
	}
	if !tapRemoved || !rulesCleaned {
		t.Errorf("expected RemoveTAP to clean rules and delete device")
	}

	// PruneOrphanTAPs should clean up rules for orphan TAPs
	runner.commands = nil
	activeTAPs := map[string]bool{}
	knownTAPs := map[string]bool{"mftap-orphan1": true}
	pruned, err := mgr.PruneOrphanTAPs(ctx, activeTAPs, knownTAPs)
	if err != nil {
		t.Fatalf("PruneOrphanTAPs failed: %v", err)
	}
	if len(pruned) != 1 || pruned[0] != "mftap-orphan1" {
		t.Fatalf("expected mftap-orphan1 to be pruned, got %v", pruned)
	}
	var orphanRulesCleaned bool
	for _, cmd := range runner.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "iptables -D FORWARD -i mftap-orphan1 -o mftap-+ -j DROP") {
			orphanRulesCleaned = true
		}
	}
	if !orphanRulesCleaned {
		t.Errorf("expected PruneOrphanTAPs to clean up isolation rules for orphan tap")
	}
}

type testAllocStore struct {
	allocs map[string]*db.NetworkAllocation
}

func (s *testAllocStore) GetNetworkAllocation(ctx context.Context, tapOrID string) (*db.NetworkAllocation, error) {
	return s.allocs[tapOrID], nil
}

func TestRemoveTAP_DeletesAllRulesAndHandlesStoreFallback(t *testing.T) {
	ctx := context.Background()

	// Case 1: In-memory isolation config from ApplyAntiSpoofAndIsolation
	runner1 := &mockRunner{}
	mgr1, err := NewManager(DefaultNATConfig(), runner1)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	isoCfg := IsolationConfig{
		TAPDevice:       "mftap-vm99",
		IPAddress:       "172.16.0.99",
		MACAddress:      "06:00:ac:10:00:99",
		AllowedPeerTAPs: []string{"mftap-peerA"},
	}
	if err := mgr1.ApplyAntiSpoofAndIsolation(ctx, isoCfg); err != nil {
		t.Fatalf("ApplyAntiSpoofAndIsolation failed: %v", err)
	}

	runner1.commands = nil
	// Calling RemoveTAP passing only tapName
	if err := mgr1.RemoveTAP(ctx, "mftap-vm99"); err != nil {
		t.Fatalf("RemoveTAP failed: %v", err)
	}

	var hasIPDel, hasMACDel, hasARPMACDel, hasARPIPDel, hasDHCPDel, hasPeerDel, hasDropDel, hasBridgeDel, hasLinkDel bool
	for _, cmd := range runner1.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "iptables -D FORWARD -i mftap-vm99 ! -s 172.16.0.99 -j DROP") {
			hasIPDel = true
		}
		if strings.Contains(line, "ebtables -D FORWARD -i mftap-vm99 ! -s 06:00:ac:10:00:99 -j DROP") {
			hasMACDel = true
		}
		if strings.Contains(line, "arptables -D FORWARD -i mftap-vm99 ! --source-mac 06:00:ac:10:00:99 -j DROP") {
			hasARPMACDel = true
		}
		if strings.Contains(line, "arptables -D FORWARD -i mftap-vm99 ! --source-ip 172.16.0.99 -j DROP") {
			hasARPIPDel = true
		}
		if strings.Contains(line, "iptables -D FORWARD -i mftap-vm99 -p udp --sport 67 -j DROP") {
			hasDHCPDel = true
		}
		if strings.Contains(line, "iptables -D FORWARD -i mftap-vm99 -o mftap-peerA -j ACCEPT") {
			hasPeerDel = true
		}
		if strings.Contains(line, "iptables -D FORWARD -i mftap-vm99 -o mftap-+ -j DROP") {
			hasDropDel = true
		}
		if strings.Contains(line, "bridge link set dev mftap-vm99 isolated off") {
			hasBridgeDel = true
		}
		if strings.Contains(line, "ip link del dev mftap-vm99") {
			hasLinkDel = true
		}
	}

	if !hasIPDel {
		t.Errorf("expected IP anti-spoof rule deletion in RemoveTAP")
	}
	if !hasMACDel {
		t.Errorf("expected MAC anti-spoof rule deletion in RemoveTAP")
	}
	if !hasARPMACDel {
		t.Errorf("expected ARP MAC anti-spoof rule deletion in RemoveTAP")
	}
	if !hasARPIPDel {
		t.Errorf("expected ARP IP anti-spoof rule deletion in RemoveTAP")
	}
	if !hasDHCPDel {
		t.Errorf("expected DHCP guard rule deletion in RemoveTAP")
	}
	if !hasPeerDel {
		t.Errorf("expected peer accept rule deletion in RemoveTAP")
	}
	if !hasDropDel {
		t.Errorf("expected cross-tenant drop rule deletion in RemoveTAP")
	}
	if !hasBridgeDel {
		t.Errorf("expected bridge isolation reset in RemoveTAP")
	}
	if !hasLinkDel {
		t.Errorf("expected link del in RemoveTAP")
	}

	// Case 2: Config not in memory (e.g. daemon restart / orphan pruning) -> fallback to store
	runner2 := &mockRunner{}
	mgr2, err := NewManager(DefaultNATConfig(), runner2)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	mgr2.SetStore(&testAllocStore{
		allocs: map[string]*db.NetworkAllocation{
			"mftap-reboot": {
				TAPDevice:  "mftap-reboot",
				IPAddress:  "172.16.0.88",
				MACAddress: "06:00:ac:10:00:88",
			},
		},
	})

	runner2.commands = nil
	if err := mgr2.RemoveTAP(ctx, "mftap-reboot"); err != nil {
		t.Fatalf("RemoveTAP with store fallback failed: %v", err)
	}

	var hasFallbackIPDel, hasFallbackMACDel bool
	for _, cmd := range runner2.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "iptables -D FORWARD -i mftap-reboot ! -s 172.16.0.88 -j DROP") {
			hasFallbackIPDel = true
		}
		if strings.Contains(line, "ebtables -D FORWARD -i mftap-reboot ! -s 06:00:ac:10:00:88 -j DROP") {
			hasFallbackMACDel = true
		}
	}
	if !hasFallbackIPDel {
		t.Errorf("expected fallback IP anti-spoof deletion via store lookup")
	}
	if !hasFallbackMACDel {
		t.Errorf("expected fallback MAC anti-spoof deletion via store lookup")
	}
}

// failingTeardownRunner simulates an nft-backed host where the base chains
// exist but every delete reports that no matching rule is present.
type failingTeardownRunner struct {
	commands [][]string
}

func (r *failingTeardownRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.commands = append(r.commands, append([]string{name}, args...))
	if name == "iptables" && len(args) > 0 && args[0] == "-D" {
		return []byte("iptables: Bad rule (does a matching rule exist in that chain?)."), errTestRuleAbsent
	}
	if name == "ebtables" || name == "arptables" {
		if len(args) > 0 && args[0] == "-D" {
			return []byte("Bad rule (does a matching rule exist in that chain?)"), errTestRuleAbsent
		}
	}
	return []byte("ok"), nil
}

var errTestRuleAbsent = &testError{"exit status 1"}

type testError struct{ s string }

func (e *testError) Error() string { return e.s }

func TestAntiSpoofCreatesBaseChains(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{}
	mgr, err := NewManager(DefaultNATConfig(), runner)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	if err := mgr.ApplyAntiSpoofAndIsolation(ctx, IsolationConfig{
		TAPDevice:  "mftap-vm1",
		IPAddress:  "172.16.0.15",
		MACAddress: "06:00:ac:10:00:0f",
	}); err != nil {
		t.Fatalf("ApplyAntiSpoofAndIsolation failed: %v", err)
	}
	want := []string{
		"arptables -N FORWARD",
		"ebtables -N FORWARD",
		"ebtables -N INPUT",
	}
	for _, w := range want {
		found := false
		for _, cmd := range runner.commands {
			if strings.Join(cmd, " ") == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected chain setup command %q", w)
		}
	}
}

func TestRemoveAntiSpoofIgnoresAbsentRules(t *testing.T) {
	ctx := context.Background()
	runner := &failingTeardownRunner{}
	mgr, err := NewManager(DefaultNATConfig(), runner)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	cfg := IsolationConfig{
		TAPDevice:  "mftap-vm1",
		IPAddress:  "172.16.0.15",
		MACAddress: "06:00:ac:10:00:0f",
	}
	if err := mgr.RemoveAntiSpoofAndIsolation(ctx, cfg); err != nil {
		t.Fatalf("teardown of already-absent rules must be a no-op, got %v", err)
	}
}
