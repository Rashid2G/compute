package nftables

import (
	"bytes"
	"net"
	"os"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
)

func TestFirewall_EnsureAndAntiSpoof(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("skipping nftables test requiring root/CAP_NET_ADMIN privileges")
	}

	fw, err := NewFirewall()
	if err != nil {
		t.Fatalf("NewFirewall failed: %v", err)
	}
	defer fw.Flush()

	if err := fw.EnsureBaseChains(); err != nil {
		t.Fatalf("EnsureBaseChains failed: %v", err)
	}

	err = fw.ApplyAntiSpoofRules("mf_test0", "10.64.0.2", "06:00:ac:10:00:02")
	if err != nil {
		t.Fatalf("ApplyAntiSpoofRules failed: %v", err)
	}
}

func TestApplyBridgeIsolation_RuleGeneration(t *testing.T) {
	tapName := "mftap-alpha"
	peers := []string{"10.64.0.5", "10.64.0.6"}

	tables := []*nftables.Table{
		{Family: nftables.TableFamilyBridge, Name: "compute"},
		{Family: nftables.TableFamilyINet, Name: "compute"},
	}

	for _, tbl := range tables {
		chain := &nftables.Chain{Name: "forward", Table: tbl}
		rules := BuildBridgeIsolationRules(tbl, chain, tapName, peers)

		// Expect 2 peer ACCEPT rules + 1 default DROP rule = 3 rules
		if len(rules) != 3 {
			t.Fatalf("[%v] expected 3 rules, got %d", tbl.Family, len(rules))
		}

		// Verify peer ACCEPT rules
		for i, peerIP := range peers {
			r := rules[i]
			var hasIIF, hasPeerIP, hasAccept bool
			for _, exp := range r.Exprs {
				if cmp, ok := exp.(*expr.Cmp); ok {
					if string(cmp.Data) == tapName+"\x00" {
						hasIIF = true
					}
					if len(cmp.Data) == 4 && net.IP(cmp.Data).String() == peerIP {
						hasPeerIP = true
					}
				}
				if v, ok := exp.(*expr.Verdict); ok && v.Kind == expr.VerdictAccept {
					hasAccept = true
				}
			}
			if !hasIIF || !hasPeerIP || !hasAccept {
				t.Errorf("[%v] peer rule %d mismatch: iif=%v, peer=%v, accept=%v", tbl.Family, i, hasIIF, hasPeerIP, hasAccept)
			}
		}

		// Verify explicit default DROP rule for cross-tenant TAP traffic
		dropRule := rules[2]
		var hasIIF, hasBitwise, hasOIF, hasDrop bool
		for _, exp := range dropRule.Exprs {
			if bitwise, ok := exp.(*expr.Bitwise); ok {
				if bitwise.Len == 16 && bytes.Equal(bitwise.Mask, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}) {
					hasBitwise = true
				}
			}
			if cmp, ok := exp.(*expr.Cmp); ok {
				if string(cmp.Data) == tapName+"\x00" {
					hasIIF = true
				}
				if bytes.Equal(cmp.Data, append([]byte("mftap-"), make([]byte, 10)...)) {
					hasOIF = true
				}
			}
			if v, ok := exp.(*expr.Verdict); ok && v.Kind == expr.VerdictDrop {
				hasDrop = true
			}
		}
		if !hasIIF || !hasBitwise || !hasOIF || !hasDrop {
			t.Errorf("[%v] default DROP rule missing expected expressions: iif=%v, bitwise=%v, oif=%v, drop=%v", tbl.Family, hasIIF, hasBitwise, hasOIF, hasDrop)
		}
	}
}

func TestFirewall_BridgeIsolationIntegration(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("skipping nftables test requiring root/CAP_NET_ADMIN privileges")
	}

	fw, err := NewFirewall()
	if err != nil {
		t.Fatalf("NewFirewall failed: %v", err)
	}
	defer fw.Flush()

	tap := "mftap-itg"
	if err := fw.ApplyBridgeIsolation(tap, []string{"10.64.0.10"}); err != nil {
		t.Fatalf("ApplyBridgeIsolation failed: %v", err)
	}

	if err := fw.RemoveRulesForTAP(tap); err != nil {
		t.Fatalf("RemoveRulesForTAP failed: %v", err)
	}
}
