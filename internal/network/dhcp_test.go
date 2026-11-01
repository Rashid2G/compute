package network

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestDHCPPacketEncodeDecode(t *testing.T) {
	mac, err := net.ParseMAC("06:00:11:22:33:44")
	if err != nil {
		t.Fatalf("failed to parse MAC: %v", err)
	}

	disc := BuildDiscover(0x12345678, mac)
	packet, err := parseDHCPPacket(disc)
	if err != nil {
		t.Fatalf("failed to parse generated discover packet: %v", err)
	}

	if packet.XId != 0x12345678 {
		t.Fatalf("expected XId 0x12345678, got 0x%x", packet.XId)
	}
	if packet.GetMessageType() != msgDiscover {
		t.Fatalf("expected message type %d (Discover), got %d", msgDiscover, packet.GetMessageType())
	}
	if packet.CHAddr.String() != mac.String() {
		t.Fatalf("expected CHAddr %s, got %s", mac.String(), packet.CHAddr.String())
	}

	req := BuildRequest(0x87654321, mac, net.ParseIP("192.168.1.55"), net.ParseIP("192.168.1.1"))
	reqPacket, err := parseDHCPPacket(req)
	if err != nil {
		t.Fatalf("failed to parse generated request packet: %v", err)
	}
	if reqPacket.XId != 0x87654321 {
		t.Fatalf("expected XId 0x87654321, got 0x%x", reqPacket.XId)
	}
	if reqPacket.GetMessageType() != msgRequest {
		t.Fatalf("expected message type %d (Request), got %d", msgRequest, reqPacket.GetMessageType())
	}
}

func TestDHCPParseEnvOutput(t *testing.T) {
	output := `
udhcpc: started, v1.36.1
udhcpc: broadcasting discover
udhcpc: broadcasting select for 192.168.1.77, server 192.168.1.1
DHCP_IP=192.168.1.77
DHCP_MASK=255.255.255.0
DHCP_ROUTER=192.168.1.1
DHCP_DNS=1.1.1.1 8.8.8.8
DHCP_SERVERID=192.168.1.1
DHCP_LEASE=86400
udhcpc: lease of 192.168.1.77 obtained from 192.168.1.1, lease time 86400
`
	lease, err := parseDHCPEnvOutput(output, "06:00:11:22:33:44")
	if err != nil {
		t.Fatalf("parseDHCPEnvOutput failed: %v", err)
	}

	if lease.IP.String() != "192.168.1.77" {
		t.Fatalf("expected IP 192.168.1.77, got %s", lease.IP.String())
	}
	if lease.Gateway.String() != "192.168.1.1" {
		t.Fatalf("expected Gateway 192.168.1.1, got %s", lease.Gateway.String())
	}
	if lease.ServerIP.String() != "192.168.1.1" {
		t.Fatalf("expected ServerIP 192.168.1.1, got %s", lease.ServerIP.String())
	}
	if len(lease.DNS) != 2 || lease.DNS[0].String() != "1.1.1.1" {
		t.Fatalf("unexpected DNS servers: %v", lease.DNS)
	}
	if lease.LeaseDuration != 86400*time.Second {
		t.Fatalf("expected lease duration 86400s, got %v", lease.LeaseDuration)
	}
}

func TestAcquireDHCPLeaseMockRunner(t *testing.T) {
	mockOut := map[string]string{
		"ip link show": "1: eth0: <BROADCAST,MULTICAST,UP> mtu 1500",
		"udhcpc": "DHCP_IP=192.168.1.88\nDHCP_MASK=255.255.255.0\nDHCP_ROUTER=192.168.1.1\nDHCP_SERVERID=192.168.1.1\nDHCP_LEASE=3600\n",
	}
	runner := &mockRunner{output: mockOut}
	ctx := context.Background()

	lease, err := AcquireDHCPLease(ctx, runner, "eth0", "06:00:11:22:33:44")
	if err != nil {
		t.Fatalf("AcquireDHCPLease failed: %v", err)
	}

	if lease.IP.String() != "192.168.1.88" {
		t.Fatalf("expected leased IP 192.168.1.88, got %s", lease.IP.String())
	}
	if lease.Gateway.String() != "192.168.1.1" {
		t.Fatalf("expected Gateway 192.168.1.1, got %s", lease.Gateway.String())
	}
}
