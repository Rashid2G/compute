package network

import (
	"context"
	"strings"
	"testing"

	"github.com/Rashid2G/compute/internal/db"
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

func TestAccountModeBridgeEbtablesAndRuleOrdering(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{}
	mgr, err := NewManager(DefaultNATConfig(), runner)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Pre-populate peer config to verify peer anti-spoofing re-pinning
	if err := mgr.ApplyAntiSpoofAndIsolation(ctx, IsolationConfig{
		TAPDevice:        "mftap-peer1",
		IPAddress:        "172.16.0.10",
		MACAddress:       "06:00:ac:10:00:0a",
		OwnerID:          "usr_alice",
		AllowSameAccount: true,
	}); err != nil {
		t.Fatalf("ApplyAntiSpoofAndIsolation peer1 failed: %v", err)
	}

	runner.commands = nil
	if err := mgr.ApplyAntiSpoofAndIsolation(ctx, IsolationConfig{
		TAPDevice:        "mftap-vm2",
		IPAddress:        "172.16.0.11",
		MACAddress:       "06:00:ac:10:00:0b",
		OwnerID:          "usr_alice",
		AllowSameAccount: true,
		AllowedPeerTAPs:  []string{"mftap-peer1"},
	}); err != nil {
		t.Fatalf("ApplyAntiSpoofAndIsolation vm2 failed: %v", err)
	}

	var hasBridgeIsoOff bool
	var hasEbDropOut, hasEbDropIn bool
	var hasEbAccOut, hasEbAccIn bool
	acceptIdx := -1
	antiSpoofIdx := -1
	rePinnedPeer := false

	for i, cmd := range runner.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "bridge link set dev mftap-vm2 isolated off") {
			hasBridgeIsoOff = true
		}
		if strings.Contains(line, "ebtables -A FORWARD -i mftap-vm2 -o mftap-+ -j DROP") {
			hasEbDropOut = true
		}
		if strings.Contains(line, "ebtables -A FORWARD -i mftap-+ -o mftap-vm2 -j DROP") {
			hasEbDropIn = true
		}
		if strings.Contains(line, "ebtables -I FORWARD 1 -i mftap-vm2 -o mftap-peer1 -j ACCEPT") {
			hasEbAccOut = true
			acceptIdx = i
		}
		if strings.Contains(line, "ebtables -I FORWARD 1 -i mftap-peer1 -o mftap-vm2 -j ACCEPT") {
			hasEbAccIn = true
			acceptIdx = i
		}
		if strings.Contains(line, "ebtables -I FORWARD 1 -i mftap-vm2 ! -s 06:00:ac:10:00:0b -j DROP") {
			antiSpoofIdx = i
		}
		if strings.Contains(line, "ebtables -I FORWARD 1 -i mftap-peer1 ! -s 06:00:ac:10:00:0a -j DROP") {
			rePinnedPeer = true
		}
	}

	if !hasBridgeIsoOff {
		t.Errorf("expected bridge isolated off in account mode")
	}
	if !hasEbDropOut || !hasEbDropIn {
		t.Errorf("expected ebtables FORWARD mftap-+ DROP rules")
	}
	if !hasEbAccOut || !hasEbAccIn {
		t.Errorf("expected ebtables bidirectional ACCEPT rules for same-account peer")
	}
	if acceptIdx < 0 || antiSpoofIdx <= acceptIdx {
		t.Errorf("expected anti-spoof -I FORWARD 1 (%d) to run after ACCEPT -I FORWARD 1 (%d) so anti-spoof stays at top", antiSpoofIdx, acceptIdx)
	}
	if !rePinnedPeer {
		t.Errorf("expected peer anti-spoof rule to be re-pinned after reciprocal ACCEPT")
	}
}

func TestIsolatedModeOverridesPeers(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{}
	mgr, err := NewManager(DefaultNATConfig(), runner)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	if err := mgr.ApplyAntiSpoofAndIsolation(ctx, IsolationConfig{
		TAPDevice:        "mftap-iso",
		IPAddress:        "172.16.0.20",
		MACAddress:       "06:00:ac:10:00:14",
		OwnerID:          "usr_alice",
		Isolated:         true,
		AllowSameAccount: true,
		AllowedPeerTAPs:  []string{"mftap-peer1"},
	}); err != nil {
		t.Fatalf("ApplyAntiSpoofAndIsolation failed: %v", err)
	}

	var hasBridgeIsoOn bool
	for _, cmd := range runner.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "bridge link set dev mftap-iso isolated on") {
			hasBridgeIsoOn = true
		}
		if strings.Contains(line, "-j ACCEPT") && !strings.Contains(line, " -D ") {
			t.Errorf("isolated sandbox must not install peer ACCEPT rule: %s", line)
		}
	}
	if !hasBridgeIsoOn {
		t.Errorf("expected bridge isolated on when Isolated=true")
	}
}

func TestRemoveTAPClearsReciprocalPeerRules(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{}
	mgr, err := NewManager(DefaultNATConfig(), runner)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	_ = mgr.ApplyAntiSpoofAndIsolation(ctx, IsolationConfig{
		TAPDevice:        "mftap-a",
		IPAddress:        "172.16.0.10",
		MACAddress:       "06:00:ac:10:00:0a",
		OwnerID:          "usr_alice",
		AllowSameAccount: true,
		AllowedPeerTAPs:  []string{"mftap-b"},
	})
	_ = mgr.ApplyAntiSpoofAndIsolation(ctx, IsolationConfig{
		TAPDevice:        "mftap-b",
		IPAddress:        "172.16.0.11",
		MACAddress:       "06:00:ac:10:00:0b",
		OwnerID:          "usr_alice",
		AllowSameAccount: true,
		AllowedPeerTAPs:  []string{"mftap-a"},
	})

	runner.commands = nil
	if err := mgr.RemoveTAP(ctx, "mftap-b"); err != nil {
		t.Fatalf("RemoveTAP failed: %v", err)
	}

	var delEbAToB, delIpAToB bool
	for _, cmd := range runner.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "ebtables -D FORWARD -i mftap-a -o mftap-b -j ACCEPT") {
			delEbAToB = true
		}
		if strings.Contains(line, "iptables -D FORWARD -i mftap-a -o mftap-b -j ACCEPT") {
			delIpAToB = true
		}
	}
	if !delEbAToB || !delIpAToB {
		t.Errorf("expected reciprocal ACCEPT rules on peer mftap-a to be cleaned up when mftap-b is removed")
	}
	if len(mgr.isolationConfigs["mftap-a"].AllowedPeerTAPs) != 0 {
		t.Errorf("expected mftap-b to be pruned from mftap-a AllowedPeerTAPs, got %v", mgr.isolationConfigs["mftap-a"].AllowedPeerTAPs)
	}
}

func TestDefaultBridgeEgressFirewallAndHostIn(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{}
	cfg := DefaultNATConfig()
	mgr, err := NewManager(cfg, runner)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	if err := mgr.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork failed: %v", err)
	}

	var (
		hasEgressChainCreated bool
		hasEgressFlushed      bool
		hasEgressEstablished  bool
		hasEgressDropIMDS     bool
		hasEgressDrop10       bool
		hasEgressDrop172      bool
		hasEgressDrop192      bool
		hasEgressDrop100      bool
		hasEgressDrop127      bool
		hasEgressInternetAcc  bool
		hasForwardHookEgress  bool
		hasForwardDropIMDS    bool

		hasHostInChainCreated bool
		hasHostInFlushed      bool
		hasHostInEstablished  bool
		hasHostInDHCP67       bool
		hasHostInDHCP68       bool
		hasHostInDNSUDP       bool
		hasHostInDNSTCP       bool
		hasHostInICMP         bool
		hasHostInDropAll      bool
		hasInputHookHostIn    bool
	)

	for _, cmd := range runner.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "iptables -N COMPUTE_EGRESS") {
			hasEgressChainCreated = true
		}
		if strings.Contains(line, "iptables -F COMPUTE_EGRESS") {
			hasEgressFlushed = true
		}
		if strings.Contains(line, "iptables -A COMPUTE_EGRESS -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT") {
			hasEgressEstablished = true
		}
		if strings.Contains(line, "iptables -A COMPUTE_EGRESS -d 169.254.0.0/16 -j DROP") {
			hasEgressDropIMDS = true
		}
		if strings.Contains(line, "iptables -A COMPUTE_EGRESS -d 10.0.0.0/8 -j DROP") {
			hasEgressDrop10 = true
		}
		if strings.Contains(line, "iptables -A COMPUTE_EGRESS -d 172.16.0.0/12 -j DROP") {
			hasEgressDrop172 = true
		}
		if strings.Contains(line, "iptables -A COMPUTE_EGRESS -d 192.168.0.0/16 -j DROP") {
			hasEgressDrop192 = true
		}
		if strings.Contains(line, "iptables -A COMPUTE_EGRESS -d 100.64.0.0/10 -j DROP") {
			hasEgressDrop100 = true
		}
		if strings.Contains(line, "iptables -A COMPUTE_EGRESS -d 127.0.0.0/8 -j DROP") {
			hasEgressDrop127 = true
		}
		if strings.Contains(line, "iptables -A COMPUTE_EGRESS -j ACCEPT") {
			hasEgressInternetAcc = true
		}
		if strings.Contains(line, "iptables -I FORWARD 1 -i mft3br0 ! -o mft3br0 -j COMPUTE_EGRESS") {
			hasForwardHookEgress = true
		}
		if strings.Contains(line, "iptables -I FORWARD 1 -i mft3br0 -d 169.254.0.0/16 -j DROP") {
			hasForwardDropIMDS = true
		}

		if strings.Contains(line, "iptables -N COMPUTE_HOST_IN") {
			hasHostInChainCreated = true
		}
		if strings.Contains(line, "iptables -F COMPUTE_HOST_IN") {
			hasHostInFlushed = true
		}
		if strings.Contains(line, "iptables -A COMPUTE_HOST_IN -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT") {
			hasHostInEstablished = true
		}
		if strings.Contains(line, "iptables -A COMPUTE_HOST_IN -p udp --dport 67 -j ACCEPT") {
			hasHostInDHCP67 = true
		}
		if strings.Contains(line, "iptables -A COMPUTE_HOST_IN -p udp --dport 68 -j ACCEPT") {
			hasHostInDHCP68 = true
		}
		if strings.Contains(line, "iptables -A COMPUTE_HOST_IN -p udp --dport 53 -j ACCEPT") {
			hasHostInDNSUDP = true
		}
		if strings.Contains(line, "iptables -A COMPUTE_HOST_IN -p tcp --dport 53 -j ACCEPT") {
			hasHostInDNSTCP = true
		}
		if strings.Contains(line, "iptables -A COMPUTE_HOST_IN -p icmp -j ACCEPT") {
			hasHostInICMP = true
		}
		if strings.Contains(line, "iptables -A COMPUTE_HOST_IN -j DROP") {
			hasHostInDropAll = true
		}
		if strings.Contains(line, "iptables -I INPUT 1 -i mft3br0 -j COMPUTE_HOST_IN") {
			hasInputHookHostIn = true
		}
	}

	if !hasEgressChainCreated || !hasEgressFlushed {
		t.Errorf("expected COMPUTE_EGRESS chain creation and flush")
	}
	if !hasEgressEstablished {
		t.Errorf("expected COMPUTE_EGRESS to permit RELATED,ESTABLISHED")
	}
	if !hasEgressDropIMDS {
		t.Errorf("expected COMPUTE_EGRESS to block link-local IMDS 169.254.0.0/16")
	}
	if !hasEgressDrop10 || !hasEgressDrop172 || !hasEgressDrop192 {
		t.Errorf("expected COMPUTE_EGRESS to block RFC1918 private subnets (10/8, 172.16/12, 192.168/16)")
	}
	if !hasEgressDrop100 || !hasEgressDrop127 {
		t.Errorf("expected COMPUTE_EGRESS to block CGNAT/Tailscale and loopback")
	}
	if !hasEgressInternetAcc {
		t.Errorf("expected COMPUTE_EGRESS to permit remaining public internet egress")
	}
	if !hasForwardHookEgress {
		t.Errorf("expected FORWARD hook jumping -i mft3br0 ! -o mft3br0 to COMPUTE_EGRESS")
	}
	if !hasForwardDropIMDS {
		t.Errorf("expected FORWARD hook dropping -i mft3br0 -d 169.254.0.0/16")
	}

	if !hasHostInChainCreated || !hasHostInFlushed {
		t.Errorf("expected COMPUTE_HOST_IN chain creation and flush")
	}
	if !hasHostInEstablished {
		t.Errorf("expected COMPUTE_HOST_IN to permit RELATED,ESTABLISHED")
	}
	if !hasHostInDHCP67 || !hasHostInDHCP68 {
		t.Errorf("expected COMPUTE_HOST_IN to permit DHCP UDP 67/68")
	}
	if !hasHostInDNSUDP || !hasHostInDNSTCP {
		t.Errorf("expected COMPUTE_HOST_IN to permit DNS UDP/TCP 53")
	}
	if !hasHostInICMP {
		t.Errorf("expected COMPUTE_HOST_IN to permit ICMP to bridge gateway IP")
	}
	if !hasHostInDropAll {
		t.Errorf("expected COMPUTE_HOST_IN to drop remaining host packets")
	}
	if !hasInputHookHostIn {
		t.Errorf("expected INPUT hook jumping -i mft3br0 to COMPUTE_HOST_IN")
	}
}

func TestEgressAllowCIDRsEvaluatedBeforeDeny(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{}
	cfg := DefaultNATConfig()
	cfg.EgressAllowCIDRs = []string{"10.50.1.0/24", "172.16.200.0/24"}

	mgr, err := NewManager(cfg, runner)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	if err := mgr.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork failed: %v", err)
	}

	var allowIdx10, denyIdx10 int = -1, -1
	var allowIdx172, denyIdx172 int = -1, -1

	for idx, cmd := range runner.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "-A COMPUTE_EGRESS -d 10.50.1.0/24 -j ACCEPT") {
			allowIdx10 = idx
		}
		if strings.Contains(line, "-A COMPUTE_EGRESS -d 10.0.0.0/8 -j DROP") {
			denyIdx10 = idx
		}
		if strings.Contains(line, "-A COMPUTE_EGRESS -d 172.16.200.0/24 -j ACCEPT") {
			allowIdx172 = idx
		}
		if strings.Contains(line, "-A COMPUTE_EGRESS -d 172.16.0.0/12 -j DROP") {
			denyIdx172 = idx
		}
	}

	if allowIdx10 == -1 || denyIdx10 == -1 {
		t.Fatalf("missing allow or deny rule for 10.0.0.0 network: allow=%d, deny=%d", allowIdx10, denyIdx10)
	}
	if allowIdx10 >= denyIdx10 {
		t.Errorf("expected EgressAllowCIDR 10.50.1.0/24 (idx %d) to be evaluated before EgressDenyCIDR 10.0.0.0/8 (idx %d)", allowIdx10, denyIdx10)
	}

	if allowIdx172 == -1 || denyIdx172 == -1 {
		t.Fatalf("missing allow or deny rule for 172.16.0.0 network: allow=%d, deny=%d", allowIdx172, denyIdx172)
	}
	if allowIdx172 >= denyIdx172 {
		t.Errorf("expected EgressAllowCIDR 172.16.200.0/24 (idx %d) to be evaluated before EgressDenyCIDR 172.16.0.0/12 (idx %d)", allowIdx172, denyIdx172)
	}
}

func TestIntraBridgeTrafficNotBrokenByRFC1918EgressDeny(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{}
	cfg := DefaultNATConfig()
	mgr, err := NewManager(cfg, runner)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	if err := mgr.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork failed: %v", err)
	}

	// 1. Verify egress deny rules apply strictly to traffic leaving the bridge (! -o mft3br0)
	var hasEgressOffBridgeHook bool
	for _, cmd := range runner.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "iptables -I FORWARD 1 -i mft3br0 ! -o mft3br0 -j COMPUTE_EGRESS") {
			hasEgressOffBridgeHook = true
		}
		if strings.Contains(line, "iptables -I FORWARD 1 -i mft3br0 -j COMPUTE_EGRESS") {
			t.Errorf("COMPUTE_EGRESS must not match on all -i mft3br0 indiscriminately without ! -o mft3br0: %s", line)
		}
	}
	if !hasEgressOffBridgeHook {
		t.Errorf("expected FORWARD hook with ! -o mft3br0 so intra-bridge traffic is not dropped by RFC1918 egress filter")
	}

	// 2. Apply intra-account isolation rules between two VMs on the bridge
	iso1 := IsolationConfig{
		TAPDevice:        "mftap-alpha",
		IPAddress:        "172.16.0.10",
		MACAddress:       "06:00:ac:10:00:0a",
		AllowedPeerTAPs:  []string{"mftap-beta"},
		AllowSameAccount: true,
	}
	if err := mgr.ApplyAntiSpoofAndIsolation(ctx, iso1); err != nil {
		t.Fatalf("ApplyAntiSpoofAndIsolation failed: %v", err)
	}

	var hasIntraAccountAccept bool
	for _, cmd := range runner.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "-i mftap-alpha -o mftap-beta -j ACCEPT") {
			hasIntraAccountAccept = true
		}
	}
	if !hasIntraAccountAccept {
		t.Errorf("expected intra-account accept rule to be installed in FORWARD")
	}
}

func TestNoInternetIsolationMode_BridgeAndPerVM(t *testing.T) {
	ctx := context.Background()

	// 1. Bridge-level ModeNoInternet
	runnerBridge := &mockRunner{}
	cfgNoInternet := DefaultNoInternetConfig()
	mgrBridge, err := NewManager(cfgNoInternet, runnerBridge)
	if err != nil {
		t.Fatalf("NewManager with ModeNoInternet failed: %v", err)
	}
	if err := mgrBridge.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork failed: %v", err)
	}

	var hasTailDropInEgress bool
	for _, cmd := range runnerBridge.commands {
		line := strings.Join(cmd, " ")
		if line == "iptables -A COMPUTE_EGRESS -j DROP" {
			hasTailDropInEgress = true
		}
		if line == "iptables -A COMPUTE_EGRESS -j ACCEPT" {
			t.Errorf("ModeNoInternet must not accept public internet in COMPUTE_EGRESS: %s", line)
		}
	}
	if !hasTailDropInEgress {
		t.Errorf("expected ModeNoInternet to terminate COMPUTE_EGRESS with -j DROP")
	}

	// 2. Per-VM no-internet isolation mode on standard NAT bridge
	runnerVM := &mockRunner{}
	mgrVM, err := NewManager(DefaultNATConfig(), runnerVM)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	isoAirgap := IsolationConfig{
		TAPDevice:     "mftap-airgapped",
		IPAddress:     "172.16.0.45",
		MACAddress:    "06:00:ac:10:00:2d",
		IsolationMode: "no-internet",
	}

	if err := mgrVM.SetupAntiSpoofAndIsolation(ctx, isoAirgap); err != nil {
		t.Fatalf("SetupAntiSpoofAndIsolation failed: %v", err)
	}

	var hasAirgapDrop, hasAirgapReturnAccept bool
	for _, cmd := range runnerVM.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "iptables -I FORWARD 1 -i mftap-airgapped ! -o mft3br0 -j DROP") {
			hasAirgapDrop = true
		}
		if strings.Contains(line, "iptables -I FORWARD 1 -i mftap-airgapped ! -o mft3br0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT") {
			hasAirgapReturnAccept = true
		}
	}
	if !hasAirgapDrop {
		t.Errorf("expected per-VM no-internet rule dropping outbound traffic off-bridge")
	}
	if !hasAirgapReturnAccept {
		t.Errorf("expected per-VM no-internet rule permitting RELATED,ESTABLISHED host management return traffic")
	}

	// 3. Clean teardown of per-VM no-internet rules
	runnerVM.commands = nil
	if err := mgrVM.RemoveAntiSpoofAndIsolation(ctx, isoAirgap); err != nil {
		t.Fatalf("RemoveAntiSpoofAndIsolation failed: %v", err)
	}

	var deletedAirgapDrop, deletedAirgapReturnAccept bool
	for _, cmd := range runnerVM.commands {
		line := strings.Join(cmd, " ")
		if strings.Contains(line, "iptables -D FORWARD -i mftap-airgapped ! -o mft3br0 -j DROP") {
			deletedAirgapDrop = true
		}
		if strings.Contains(line, "iptables -D FORWARD -i mftap-airgapped ! -o mft3br0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT") {
			deletedAirgapReturnAccept = true
		}
	}
	if !deletedAirgapDrop || !deletedAirgapReturnAccept {
		t.Errorf("expected clean teardown of per-VM no-internet rules")
	}
}

func TestCIDRValidationInNewManager(t *testing.T) {
	runner := &mockRunner{}

	// Valid CIDRs
	validCfg := DefaultNATConfig()
	validCfg.EgressDenyCIDRs = []string{"10.0.0.0/8", "192.168.1.0/24"}
	validCfg.EgressAllowCIDRs = []string{"1.1.1.1/32"}
	if _, err := NewManager(validCfg, runner); err != nil {
		t.Fatalf("expected valid CIDRs to succeed, got: %v", err)
	}

	// Invalid deny CIDR
	badDenyCfg := DefaultNATConfig()
	badDenyCfg.EgressDenyCIDRs = []string{"not-a-cidr"}
	if _, err := NewManager(badDenyCfg, runner); err == nil {
		t.Fatalf("expected error on invalid deny CIDR")
	}

	// Invalid allow CIDR
	badAllowCfg := DefaultNATConfig()
	badAllowCfg.EgressAllowCIDRs = []string{"999.999.999.999/24"}
	if _, err := NewManager(badAllowCfg, runner); err == nil {
		t.Fatalf("expected error on invalid allow CIDR")
	}
}

func TestIPv6EgressFirewallAndHostInRules(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{}
	mgr, err := NewManager(DefaultNATConfig(), runner)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if err := mgr.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork failed: %v", err)
	}

	required := []string{
		"ip6tables -N COMPUTE_EGRESS",
		"ip6tables -A COMPUTE_EGRESS -d fe80::/10 -j DROP",
		"ip6tables -A COMPUTE_EGRESS -d fc00::/7 -j DROP",
		"ip6tables -A COMPUTE_EGRESS -d ::1/128 -j DROP",
		"ip6tables -I FORWARD 1 -i mft3br0 ! -o mft3br0 -j COMPUTE_EGRESS",
		"ip6tables -A COMPUTE_HOST_IN -p udp --dport 546 -j ACCEPT",
		"ip6tables -A COMPUTE_HOST_IN -p udp --dport 547 -j ACCEPT",
		"ip6tables -A COMPUTE_HOST_IN -p udp --dport 53 -j ACCEPT",
		"ip6tables -A COMPUTE_HOST_IN -p tcp --dport 53 -j ACCEPT",
		"ip6tables -A COMPUTE_HOST_IN -p ipv6-icmp -j ACCEPT",
		"ip6tables -A COMPUTE_HOST_IN -j DROP",
		"ip6tables -I INPUT 1 -i mft3br0 -j COMPUTE_HOST_IN",
	}
	seen := make(map[string]bool)
	for _, cmd := range runner.commands {
		seen[strings.Join(cmd, " ")] = true
	}
	for _, want := range required {
		if !seen[want] {
			t.Errorf("expected IPv6 firewall rule %q to be executed", want)
		}
	}
}

func TestBlockHostBridgePortsFalseDetachesChain(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{}
	cfg := DefaultNATConfig()
	disabled := false
	cfg.BlockHostBridgePorts = &disabled
	mgr, err := NewManager(cfg, runner)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if err := mgr.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork failed: %v", err)
	}

	var detachedV4, detachedV6 bool
	for _, cmd := range runner.commands {
		line := strings.Join(cmd, " ")
		if line == "iptables -D INPUT -i mft3br0 -j COMPUTE_HOST_IN" {
			detachedV4 = true
		}
		if line == "ip6tables -D INPUT -i mft3br0 -j COMPUTE_HOST_IN" {
			detachedV6 = true
		}
		if line == "iptables -I INPUT 1 -i mft3br0 -j COMPUTE_HOST_IN" ||
			line == "ip6tables -I INPUT 1 -i mft3br0 -j COMPUTE_HOST_IN" {
			t.Errorf("did not expect COMPUTE_HOST_IN hook when BlockHostBridgePorts=false: %s", line)
		}
	}
	if !detachedV4 || !detachedV6 {
		t.Errorf("expected COMPUTE_HOST_IN to be detached from INPUT for IPv4 (%v) and IPv6 (%v)", detachedV4, detachedV6)
	}
}

func TestNoInternetModeTransitionAndPostRestartTeardown(t *testing.T) {
	ctx := context.Background()
	runner := &mockRunner{}
	mgr, err := NewManager(DefaultNATConfig(), runner)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	// 1. Mode transition from no-internet -> account must tear down no-internet DROP rule
	// even if in-memory isolationConfigs was cleared (e.g. after daemon restart).
	runner.commands = nil
	err = mgr.ApplyAntiSpoofAndIsolation(ctx, IsolationConfig{
		TAPDevice:        "mftap-trans",
		IPAddress:        "172.16.0.50",
		MACAddress:       "06:00:ac:10:00:32",
		IsolationMode:    "account",
		AllowSameAccount: true,
	})
	if err != nil {
		t.Fatalf("ApplyAntiSpoofAndIsolation failed: %v", err)
	}

	var toreDownV4Drop, toreDownV6Drop, insertedDrop bool
	for _, cmd := range runner.commands {
		line := strings.Join(cmd, " ")
		if line == "iptables -D FORWARD -i mftap-trans ! -o mft3br0 -j DROP" {
			toreDownV4Drop = true
		}
		if line == "ip6tables -D FORWARD -i mftap-trans ! -o mft3br0 -j DROP" {
			toreDownV6Drop = true
		}
		if strings.Contains(line, "-I FORWARD 1 -i mftap-trans ! -o mft3br0 -j DROP") {
			insertedDrop = true
		}
	}
	if !toreDownV4Drop || !toreDownV6Drop {
		t.Errorf("expected unconditional teardown of per-VM no-internet DROP rules on mode transition (v4=%v, v6=%v)", toreDownV4Drop, toreDownV6Drop)
	}
	if insertedDrop {
		t.Errorf("did not expect no-internet DROP rule to be inserted for account mode")
	}

	// 2. Post-restart RemoveTAP (empty isolationConfigs & empty IsolationMode) must still tear down no-internet rules.
	runner.commands = nil
	mgr.isolationConfigs = make(map[string]IsolationConfig)
	if err := mgr.RemoveTAP(ctx, "mftap-orphan"); err != nil {
		t.Fatalf("RemoveTAP failed: %v", err)
	}
	var cleanedOrphanDrop bool
	for _, cmd := range runner.commands {
		if strings.Join(cmd, " ") == "iptables -D FORWARD -i mftap-orphan ! -o mft3br0 -j DROP" {
			cleanedOrphanDrop = true
		}
	}
	if !cleanedOrphanDrop {
		t.Errorf("expected RemoveTAP after restart to unconditionally tear down per-VM no-internet rules")
	}

	// 3. isPortIsolated must match all isNoInternet aliases.
	for _, mode := range []string{"no-internet", "no_internet", "airgapped", "air-gapped"} {
		if !isPortIsolated(IsolationConfig{IsolationMode: mode, AllowSameAccount: true, AllowedPeerTAPs: []string{"mftap-peer"}}) {
			t.Errorf("expected isPortIsolated to return true for mode %q", mode)
		}
	}
}
