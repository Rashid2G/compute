package network

import (
	"os"
	"testing"
)

func TestNetwork_Naming(t *testing.T) {
	brName := BridgeName(4096)
	if brName != "mfb001000" {
		t.Errorf("expected mfb001000, got %s", brName)
	}

	tap1 := TAPName("ins_01JBX29M0A7")
	tap2 := TAPName("ins_01JBX29M0A7")
	if tap1 != tap2 {
		t.Errorf("expected deterministic TAP names, got %s and %s", tap1, tap2)
	}
	if len(tap1) != 10 { // mf + 8 hex chars = 10 chars
		t.Errorf("expected 10-char TAP name, got %d (%s)", len(tap1), tap1)
	}
}

func TestNetwork_NetlinkBridgeAndTAP(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("skipping Netlink TAP test requiring root/CAP_NET_ADMIN privileges")
	}

	bm := NewBridgeManager()
	bridgeName := "mfb_test_br"
	tapName := "mf_test_tap"

	// 1. Create bridge
	br, err := bm.EnsureVPCBridge(bridgeName, "10.99.0.1", 24, "02:6d:66:00:10:00")
	if err != nil {
		t.Fatalf("EnsureVPCBridge failed: %v", err)
	}
	defer bm.DeleteBridge(bridgeName)

	// 2. Create TAP
	tap, err := bm.CreateTAP(tapName, 100000, br)
	if err != nil {
		t.Fatalf("CreateTAP failed: %v", err)
	}
	defer bm.DeleteTAP(tapName)

	if tap.Attrs().Name != tapName {
		t.Errorf("expected TAP name %s, got %s", tapName, tap.Attrs().Name)
	}
}
