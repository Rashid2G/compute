package nftables

import (
	"os"
	"testing"
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
