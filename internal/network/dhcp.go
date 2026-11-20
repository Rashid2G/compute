package network

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DHCP message opcodes (RFC 2131)
const (
	dhcpOpRequest = 1
	dhcpOpReply   = 2

	dhcpHTypeEthernet = 1
	dhcpHLenEthernet  = 6
)

// DHCP Options
const (
	optSubnetMask   = 1
	optRouter       = 3
	optDNS          = 6
	optRequestedIP  = 50
	optLeaseTime    = 51
	optMessageType  = 53
	optServerID     = 54
	optParamRequest = 55
	optClientID     = 61
	optEnd          = 255
	optPad          = 0
)

// DHCP Message Types
const (
	msgDiscover = 1
	msgOffer    = 2
	msgRequest  = 3
	msgDecline  = 4
	msgAck      = 5
	msgNak      = 6
	msgRelease  = 7
)

var dhcpMagicCookie = [4]byte{99, 130, 83, 99} // 0x63, 0x82, 0x53, 0x63

// DHCPLease represents an acquired IPv4 DHCP lease
type DHCPLease struct {
	IP            net.IP        `json:"ip"`
	Netmask       net.IPMask    `json:"netmask"`
	Gateway       net.IP        `json:"gateway"`
	DNS           []net.IP      `json:"dns"`
	ServerIP      net.IP        `json:"server_ip"`
	LeaseDuration time.Duration `json:"lease_duration"`
	MAC           string        `json:"mac"`
}

type dhcpOption struct {
	Tag  byte
	Data []byte
}

type dhcpPacket struct {
	Op          byte
	HType       byte
	HLen        byte
	Hops        byte
	XId         uint32
	Secs        uint16
	Flags       uint16
	CIAddr      net.IP
	YIAddr      net.IP
	SIAddr      net.IP
	GIAddr      net.IP
	CHAddr      net.HardwareAddr
	MagicCookie [4]byte
	Options     []dhcpOption
}

func (p *dhcpPacket) Encode() []byte {
	buf := make([]byte, 240)
	buf[0] = p.Op
	buf[1] = p.HType
	buf[2] = p.HLen
	buf[3] = p.Hops
	binary.BigEndian.PutUint32(buf[4:8], p.XId)
	binary.BigEndian.PutUint16(buf[8:10], p.Secs)
	binary.BigEndian.PutUint16(buf[10:12], p.Flags)

	if len(p.CIAddr) == 4 {
		copy(buf[12:16], p.CIAddr.To4())
	}
	if len(p.YIAddr) == 4 {
		copy(buf[16:20], p.YIAddr.To4())
	}
	if len(p.SIAddr) == 4 {
		copy(buf[20:24], p.SIAddr.To4())
	}
	if len(p.GIAddr) == 4 {
		copy(buf[24:28], p.GIAddr.To4())
	}

	copy(buf[28:44], p.CHAddr)
	copy(buf[236:240], dhcpMagicCookie[:])

	for _, opt := range p.Options {
		if opt.Tag == optEnd || opt.Tag == optPad {
			buf = append(buf, opt.Tag)
		} else {
			buf = append(buf, opt.Tag, byte(len(opt.Data)))
			buf = append(buf, opt.Data...)
		}
	}
	buf = append(buf, optEnd)

	// DHCP packet should be at least 300 bytes (RFC 2131)
	for len(buf) < 300 {
		buf = append(buf, optPad)
	}
	return buf
}

func parseDHCPPacket(data []byte) (*dhcpPacket, error) {
	if len(data) < 240 {
		return nil, fmt.Errorf("dhcp packet too short: %d bytes", len(data))
	}
	if !bytes.Equal(data[236:240], dhcpMagicCookie[:]) {
		return nil, fmt.Errorf("invalid dhcp magic cookie")
	}

	p := &dhcpPacket{
		Op:     data[0],
		HType:  data[1],
		HLen:   data[2],
		Hops:   data[3],
		XId:    binary.BigEndian.Uint32(data[4:8]),
		Secs:   binary.BigEndian.Uint16(data[8:10]),
		Flags:  binary.BigEndian.Uint16(data[10:12]),
		CIAddr: net.IPv4(data[12], data[13], data[14], data[15]),
		YIAddr: net.IPv4(data[16], data[17], data[18], data[19]),
		SIAddr: net.IPv4(data[20], data[21], data[22], data[23]),
		GIAddr: net.IPv4(data[24], data[25], data[26], data[27]),
	}

	hlen := int(p.HLen)
	if hlen > 16 {
		hlen = 16
	}
	if hlen > 0 {
		p.CHAddr = make(net.HardwareAddr, hlen)
		copy(p.CHAddr, data[28:28+hlen])
	}
	copy(p.MagicCookie[:], data[236:240])

	idx := 240
	for idx < len(data) {
		tag := data[idx]
		if tag == optEnd {
			break
		}
		if tag == optPad {
			idx++
			continue
		}
		if idx+1 >= len(data) {
			break
		}
		optLen := int(data[idx+1])
		idx += 2
		if idx+optLen > len(data) {
			break
		}
		optData := make([]byte, optLen)
		copy(optData, data[idx:idx+optLen])
		p.Options = append(p.Options, dhcpOption{Tag: tag, Data: optData})
		idx += optLen
	}

	return p, nil
}

func (p *dhcpPacket) GetOption(tag byte) []byte {
	for _, opt := range p.Options {
		if opt.Tag == tag {
			return opt.Data
		}
	}
	return nil
}

func (p *dhcpPacket) GetMessageType() byte {
	data := p.GetOption(optMessageType)
	if len(data) > 0 {
		return data[0]
	}
	return 0
}

func (p *dhcpPacket) ToLease(mac string) *DHCPLease {
	lease := &DHCPLease{
		IP:  p.YIAddr,
		MAC: mac,
	}

	// Subnet Mask
	if maskBytes := p.GetOption(optSubnetMask); len(maskBytes) == 4 {
		lease.Netmask = net.IPv4Mask(maskBytes[0], maskBytes[1], maskBytes[2], maskBytes[3])
	} else {
		lease.Netmask = net.CIDRMask(24, 32)
	}

	// Gateway / Router
	if routerBytes := p.GetOption(optRouter); len(routerBytes) >= 4 {
		lease.Gateway = net.IPv4(routerBytes[0], routerBytes[1], routerBytes[2], routerBytes[3])
	}

	// Server ID
	if serverBytes := p.GetOption(optServerID); len(serverBytes) == 4 {
		lease.ServerIP = net.IPv4(serverBytes[0], serverBytes[1], serverBytes[2], serverBytes[3])
	}

	// DNS
	if dnsBytes := p.GetOption(optDNS); len(dnsBytes) >= 4 {
		for i := 0; i+4 <= len(dnsBytes); i += 4 {
			lease.DNS = append(lease.DNS, net.IPv4(dnsBytes[i], dnsBytes[i+1], dnsBytes[i+2], dnsBytes[i+3]))
		}
	}

	// Lease Time
	if leaseBytes := p.GetOption(optLeaseTime); len(leaseBytes) == 4 {
		secs := binary.BigEndian.Uint32(leaseBytes)
		lease.LeaseDuration = time.Duration(secs) * time.Second
	} else {
		lease.LeaseDuration = 24 * time.Hour
	}

	return lease
}

// BuildDiscover creates a standard DHCPDISCOVER packet
func BuildDiscover(xid uint32, hwAddr net.HardwareAddr) []byte {
	p := dhcpPacket{
		Op:     dhcpOpRequest,
		HType:  dhcpHTypeEthernet,
		HLen:   byte(len(hwAddr)),
		XId:    xid,
		Flags:  0x8000, // Broadcast
		CHAddr: hwAddr,
		Options: []dhcpOption{
			{Tag: optMessageType, Data: []byte{msgDiscover}},
			{Tag: optClientID, Data: append([]byte{1}, hwAddr...)},
			{Tag: optParamRequest, Data: []byte{optSubnetMask, optRouter, optDNS, 15, 28}},
		},
	}
	return p.Encode()
}

// BuildRequest creates a standard DHCPREQUEST packet
func BuildRequest(xid uint32, hwAddr net.HardwareAddr, requestedIP, serverID net.IP) []byte {
	opts := []dhcpOption{
		{Tag: optMessageType, Data: []byte{msgRequest}},
		{Tag: optClientID, Data: append([]byte{1}, hwAddr...)},
		{Tag: optRequestedIP, Data: requestedIP.To4()},
		{Tag: optServerID, Data: serverID.To4()},
		{Tag: optParamRequest, Data: []byte{optSubnetMask, optRouter, optDNS, 15, 28}},
	}
	p := dhcpPacket{
		Op:      dhcpOpRequest,
		HType:   dhcpHTypeEthernet,
		HLen:    byte(len(hwAddr)),
		XId:     xid,
		Flags:   0x8000,
		CHAddr:  hwAddr,
		Options: opts,
	}
	return p.Encode()
}

// BuildRelease creates a standard DHCPRELEASE packet
func BuildRelease(xid uint32, hwAddr net.HardwareAddr, clientIP, serverID net.IP) []byte {
	opts := []dhcpOption{
		{Tag: optMessageType, Data: []byte{msgRelease}},
		{Tag: optClientID, Data: append([]byte{1}, hwAddr...)},
		{Tag: optServerID, Data: serverID.To4()},
	}
	p := dhcpPacket{
		Op:      dhcpOpRequest,
		HType:   dhcpHTypeEthernet,
		HLen:    byte(len(hwAddr)),
		XId:     xid,
		CIAddr:  clientIP,
		CHAddr:  hwAddr,
		Options: opts,
	}
	return p.Encode()
}

// AcquireDHCPLease attempts to negotiate and obtain a valid dynamic IP address from the DHCP server on the specified NIC or bridge.
func AcquireDHCPLease(ctx context.Context, runner Runner, nic string, macStr string) (*DHCPLease, error) {
	if macStr == "" {
		return nil, fmt.Errorf("empty mac address for dhcp request")
	}
	hwAddr, err := net.ParseMAC(macStr)
	if err != nil {
		return nil, fmt.Errorf("invalid mac address %s: %w", macStr, err)
	}

	// 1. Try via system DHCP client on macvlan or tap device (proven fast and highly compatible)
	if lease, err := acquireViaSystemClient(ctx, runner, nic, hwAddr); err == nil && lease != nil {
		return lease, nil
	}

	// 2. Fall back to direct Go socket DHCP client
	if lease, err := acquireViaRawSocket(ctx, nic, hwAddr); err == nil && lease != nil {
		return lease, nil
	}

	return nil, fmt.Errorf("no dhcp lease received from dhcp server for mac %s on interface %s", macStr, nic)
}

// acquireViaSystemClient creates a temporary macvlan interface with the VM's MAC address and requests a DHCP lease
func acquireViaSystemClient(ctx context.Context, runner Runner, parentIface string, hwAddr net.HardwareAddr) (*DHCPLease, error) {
	if parentIface == "" {
		parentIface = "eth0"
	}

	// Check if parent interface exists
	if _, err := runner.Run(ctx, "ip", "link", "show", parentIface); err != nil {
		return nil, fmt.Errorf("interface %s not found: %w", parentIface, err)
	}

	randBytes := make([]byte, 3)
	_, _ = rand.Read(randBytes)
	tmpDev := fmt.Sprintf("mfdhcp-%x", randBytes)

	// Create macvlan device
	if out, err := runner.Run(ctx, "ip", "link", "add", "link", parentIface, "name", tmpDev, "type", "macvlan"); err != nil {
		return nil, fmt.Errorf("failed to create macvlan on %s (%s): %w", parentIface, string(out), err)
	}
	defer func() {
		_, _ = runner.Run(context.Background(), "ip", "link", "del", "dev", tmpDev)
	}()

	// Set MAC address and bring interface up
	if out, err := runner.Run(ctx, "ip", "link", "set", "dev", tmpDev, "address", hwAddr.String()); err != nil {
		return nil, fmt.Errorf("failed to set mac %s on %s (%s): %w", hwAddr.String(), tmpDev, string(out), err)
	}
	if out, err := runner.Run(ctx, "ip", "link", "set", "dev", tmpDev, "up"); err != nil {
		return nil, fmt.Errorf("failed to bring up %s (%s): %w", tmpDev, string(out), err)
	}

	// Helper script to extract udhcpc environment variables
	scriptContent := `#!/bin/sh
if [ "$1" = "bound" ] || [ "$1" = "renew" ]; then
    echo "DHCP_IP=$ip"
    echo "DHCP_MASK=$subnet"
    echo "DHCP_ROUTER=$router"
    echo "DHCP_DNS=$dns"
    echo "DHCP_SERVERID=$serverid"
    echo "DHCP_LEASE=$lease"
fi
`
	tmpScript := filepath.Join(os.TempDir(), fmt.Sprintf("udhcpc-%s.sh", tmpDev))
	if err := os.WriteFile(tmpScript, []byte(scriptContent), 0755); err == nil {
		defer os.Remove(tmpScript)
	} else {
		tmpScript = "/bin/true"
	}

	// Try udhcpc (busybox or standalone) with ARP validation (-a500) to confirm address is not in use before accepting
	out, err := runner.Run(ctx, "busybox", "udhcpc", "-i", tmpDev, "-n", "-q", "-a500", "-T", "3", "-t", "3", "-s", tmpScript)
	if err != nil {
		out, err = runner.Run(ctx, "udhcpc", "-i", tmpDev, "-n", "-q", "-a500", "-T", "3", "-t", "3", "-s", tmpScript)
	}
	if err != nil {
		// Fallback without -a in case -a is unsupported by legacy udhcpc versions
		out, err = runner.Run(ctx, "busybox", "udhcpc", "-i", tmpDev, "-n", "-q", "-T", "3", "-t", "3", "-s", tmpScript)
		if err != nil {
			out, err = runner.Run(ctx, "udhcpc", "-i", tmpDev, "-n", "-q", "-T", "3", "-t", "3", "-s", tmpScript)
		}
	}

	outStr := string(out)
	if strings.Contains(outStr, "DHCP_IP=") {
		return parseDHCPEnvOutput(outStr, hwAddr.String())
	}

	// Try parsing inline lease output if script output was suppressed (e.g. "lease of 192.168.1.32 obtained from 192.168.1.1, lease time 100000")
	if strings.Contains(outStr, "obtained from") {
		fields := strings.Fields(outStr)
		for i, f := range fields {
			if f == "lease" && i+2 < len(fields) && fields[i+1] == "of" {
				ipCand := strings.Trim(fields[i+2], ",;")
				if parsed := net.ParseIP(ipCand); parsed != nil && parsed.To4() != nil {
					var srv net.IP
					if i+4 < len(fields) && fields[i+3] == "obtained" && fields[i+4] == "from" && i+5 < len(fields) {
						srv = net.ParseIP(strings.Trim(fields[i+5], ",;"))
					}
					var leaseDur = 24 * time.Hour
					for j := i; j < len(fields)-2; j++ {
						if fields[j] == "lease" && fields[j+1] == "time" {
							if d, err := strconv.Atoi(strings.Trim(fields[j+2], ",;")); err == nil && d > 0 {
								leaseDur = time.Duration(d) * time.Second
							}
							break
						}
					}
					return &DHCPLease{
						IP:            parsed,
						Netmask:       net.CIDRMask(24, 32),
						Gateway:       srv,
						ServerIP:      srv,
						LeaseDuration: leaseDur,
						MAC:           hwAddr.String(),
					}, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("system dhcp client failed to obtain lease: %s (%v)", outStr, err)
}

func parseDHCPEnvOutput(output string, mac string) (*DHCPLease, error) {
	lines := strings.Split(output, "\n")
	lease := &DHCPLease{
		MAC: mac,
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, val := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		switch key {
		case "DHCP_IP":
			lease.IP = net.ParseIP(val)
		case "DHCP_MASK":
			if maskIP := net.ParseIP(val); maskIP != nil && maskIP.To4() != nil {
				m := maskIP.To4()
				lease.Netmask = net.IPv4Mask(m[0], m[1], m[2], m[3])
			}
		case "DHCP_ROUTER":
			firstGW := strings.Fields(val)
			if len(firstGW) > 0 {
				lease.Gateway = net.ParseIP(firstGW[0])
			}
		case "DHCP_SERVERID":
			lease.ServerIP = net.ParseIP(val)
		case "DHCP_DNS":
			for _, d := range strings.Fields(val) {
				if parsed := net.ParseIP(d); parsed != nil {
					lease.DNS = append(lease.DNS, parsed)
				}
			}
		case "DHCP_LEASE":
			if secs, err := strconv.ParseInt(val, 10, 64); err == nil && secs > 0 {
				lease.LeaseDuration = time.Duration(secs) * time.Second
			}
		}
	}

	if lease.IP == nil || lease.IP.To4() == nil {
		return nil, fmt.Errorf("missing valid ip in dhcp output")
	}
	if lease.Netmask == nil {
		lease.Netmask = net.CIDRMask(24, 32)
	}
	if lease.Gateway == nil {
		lease.Gateway = lease.ServerIP
	}
	if lease.LeaseDuration == 0 {
		lease.LeaseDuration = 24 * time.Hour
	}

	return lease, nil
}

// acquireViaRawSocket implements pure-Go DHCP client fallback
func acquireViaRawSocket(ctx context.Context, nic string, hwAddr net.HardwareAddr) (*DHCPLease, error) {
	if nic == "" {
		nic = "eth0"
	}
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				configureDHCPRawConn(fd, nic)
			})
		},
	}

	packetConn, err := lc.ListenPacket(ctx, "udp4", "0.0.0.0:68")
	if err != nil {
		return nil, err
	}
	defer packetConn.Close()

	var xid uint32
	xidBytes := make([]byte, 4)
	_, _ = rand.Read(xidBytes)
	xid = binary.BigEndian.Uint32(xidBytes)

	disc := BuildDiscover(xid, hwAddr)
	dst := &net.UDPAddr{IP: net.IPv4bcast, Port: 67}

	_ = packetConn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := packetConn.WriteTo(disc, dst); err != nil {
		return nil, err
	}

	buf := make([]byte, 1500)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		_ = packetConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := packetConn.ReadFrom(buf)
		if err != nil {
			return nil, err
		}

		packet, err := parseDHCPPacket(buf[:n])
		if err != nil || packet.XId != xid {
			continue
		}

		if packet.GetMessageType() == msgOffer {
			// Send Request
			offeredIP := packet.YIAddr
			serverID := packet.SIAddr
			if s := packet.GetOption(optServerID); len(s) == 4 {
				serverID = net.IPv4(s[0], s[1], s[2], s[3])
			}

			req := BuildRequest(xid, hwAddr, offeredIP, serverID)
			_, _ = packetConn.WriteTo(req, dst)

			// Wait for ACK
			for {
				_ = packetConn.SetReadDeadline(time.Now().Add(2 * time.Second))
				n2, _, err := packetConn.ReadFrom(buf)
				if err != nil {
					break
				}
				p2, err := parseDHCPPacket(buf[:n2])
				if err != nil || p2.XId != xid {
					continue
				}
				if p2.GetMessageType() == msgAck {
					return p2.ToLease(hwAddr.String()), nil
				}
			}
		}
	}
}
