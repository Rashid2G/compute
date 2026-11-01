package network

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
)

// IPAM manages IP and MAC address allocation for VMs
type IPAM struct {
	subnet *net.IPNet
	baseIP net.IP
	mu     sync.Mutex
}

// NewIPAM creates an IPAM instance for the given CIDR (e.g. "192.168.1.0/24")
func NewIPAM(cidr string) (*IPAM, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet CIDR %s: %w", cidr, err)
	}

	return &IPAM{
		subnet: ipnet,
		baseIP: ipnet.IP,
	}, nil
}

// AllocateNextIP finds the lowest unused IPv4 in the subnet, starting at offset 2 (offset 1 is gateway)
func (ipam *IPAM) AllocateNextIP(usedIPs map[string]bool) (string, string, error) {
	ipam.mu.Lock()
	defer ipam.mu.Unlock()

	mask := binary.BigEndian.Uint32(ipam.subnet.Mask)
	start := binary.BigEndian.Uint32(ipam.subnet.IP.To4())
	broadcast := (start & mask) | (^mask)

	// Host offset starts at 2 (.1 is typically bridge/gateway)
	for cur := start + 2; cur < broadcast; cur++ {
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, cur)
		ipStr := ip.String()

		if !usedIPs[ipStr] {
			mac := GenerateMAC(ip)
			return ipStr, mac, nil
		}
	}

	return "", "", fmt.Errorf("no available IP addresses in subnet %s", ipam.subnet.String())
}

// ValidateIP checks whether an IP address is valid, belongs to the subnet, and is not already used or reserved
func (ipam *IPAM) ValidateIP(ipStr string, usedIPs map[string]bool) error {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return fmt.Errorf("invalid IP address format: %s", ipStr)
	}
	ipv4 := ip.To4()
	if ipv4 == nil {
		return fmt.Errorf("only IPv4 addresses are currently supported: %s", ipStr)
	}
	if !ipam.subnet.Contains(ipv4) {
		return fmt.Errorf("IP address %s is outside subnet %s", ipStr, ipam.subnet.String())
	}

	mask := binary.BigEndian.Uint32(ipam.subnet.Mask)
	start := binary.BigEndian.Uint32(ipam.subnet.IP.To4())
	broadcast := (start & mask) | (^mask)
	cur := binary.BigEndian.Uint32(ipv4)

	// Offset 0 is network address, offset 1 is bridge/gateway address, broadcast is broadcast address
	if cur == start {
		return fmt.Errorf("IP address %s is the network address and cannot be assigned", ipStr)
	}
	if cur == start+1 {
		return fmt.Errorf("IP address %s is reserved for the gateway", ipStr)
	}
	if cur >= broadcast {
		return fmt.Errorf("IP address %s is the broadcast address and cannot be assigned", ipStr)
	}
	if usedIPs != nil && usedIPs[ipStr] {
		return fmt.Errorf("IP address %s is already allocated", ipStr)
	}

	return nil
}

// GetNetmask returns the dotted decimal netmask (e.g. "255.255.255.0")
func (ipam *IPAM) GetNetmask() string {
	mask := ipam.subnet.Mask
	if len(mask) == 4 {
		return fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])
	}
	return "255.255.255.0"
}

// GetSubnetCIDR returns the subnet CIDR representation
func (ipam *IPAM) GetSubnetCIDR() string {
	return ipam.subnet.String()
}

// GenerateMAC creates a locally administered unicast MAC address from an IPv4 address or random entropy
// Prefix: 06:00: (02 bit set for locally administered, 01 bit clear for unicast)
func GenerateMAC(ip net.IP) string {
	ipv4 := ip.To4()
	if ipv4 == nil {
		buf := make([]byte, 4)
		_, _ = rand.Read(buf)
		return fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", buf[0], buf[1], buf[2], buf[3])
	}
	return fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", ipv4[0], ipv4[1], ipv4[2], ipv4[3])
}
