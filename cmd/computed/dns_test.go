package main

import (
	"net"
	"testing"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/db"
)

func TestSandboxAddresses(t *testing.T) {
	vm := &db.VM{
		IPAddress: "172.16.3.7",
	}
	ips := sandboxAddresses(vm)
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("172.16.3.7")) {
		t.Fatalf("want guest IP, got %v", ips)
	}

	ips = sandboxAddresses(&db.VM{IPAddress: "dhcp"})
	if len(ips) != 0 {
		t.Fatalf("dhcp placeholder must not answer, got %v", ips)
	}

	ips = sandboxAddresses(&db.VM{IPAddress: "172.16.3.9"})
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("172.16.3.9")) {
		t.Fatalf("guest-only: got %v", ips)
	}
}

func TestSandboxLabelForVM(t *testing.T) {
	cases := []struct {
		name, want string
	}{
		{"myvm", "myvm"},
		{"My VM!", "my-vm"},
		{"spaced_name", "spaced-name"},
	}
	for _, c := range cases {
		if got := sandboxLabelForVM(&db.VM{Name: c.name}); got != c.want {
			t.Errorf("sandboxLabelForVM(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestVMHostnameDefaultOwner(t *testing.T) {
	s := &Server{cfg: config.GlobalConfig{}}
	s.cfg.DNS.Enabled = true
	s.cfg.DNS.BaseDomain = "compute.tail1234.ts.net"

	if got := s.vmHostname(&db.VM{Name: "myvm", OwnerID: "default"}); got != "myvm.default.compute.tail1234.ts.net" {
		t.Fatalf("hostname = %q", got)
	}

	// DNS disabled → no hostname.
	s.cfg.DNS.Enabled = false
	if got := s.vmHostname(&db.VM{Name: "myvm", OwnerID: "default"}); got != "" {
		t.Fatalf("hostname with DNS disabled = %q", got)
	}

	// No zone configured → no hostname.
	s.cfg.DNS.Enabled = true
	s.cfg.DNS.BaseDomain = ""
	if got := s.vmHostname(&db.VM{Name: "myvm", OwnerID: "default"}); got != "" {
		t.Fatalf("hostname without zone = %q", got)
	}
}

func TestEdgeListenResolved(t *testing.T) {
	cfg := config.GlobalConfig{}
	if got := cfg.EdgeListenResolved(); got != "" {
		t.Fatalf("edge disabled without DNS: %q", got)
	}
	cfg.DNS.Enabled = true
	cfg.DNS.BaseDomain = "lab.example.com"
	if got := cfg.EdgeListenResolved(); got != ":80" {
		t.Fatalf("edge defaults to :80 with DNS: %q", got)
	}
	cfg.Server.EdgeListen = ":8080"
	if got := cfg.EdgeListenResolved(); got != ":8080" {
		t.Fatalf("explicit edge listen: %q", got)
	}
	cfg.Server.EdgeListen = "-"
	if got := cfg.EdgeListenResolved(); got != "" {
		t.Fatalf("explicitly disabled edge: %q", got)
	}
}
