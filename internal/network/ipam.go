package network

import (
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

// NewIPAM creates an IPAM instance for the given CIDR (e.g. "172.16.0.0/16")
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

// GenerateMAC creates a locally administered unicast MAC address from an IPv4 address
// Prefix: 06:00: (02 bit set for locally administered, 01 bit clear for unicast)
func GenerateMAC(ip net.IP) string {
	ipv4 := ip.To4()
	if ipv4 == nil {
		return "06:00:ac:10:00:02"
	}
	return fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", ipv4[0], ipv4[1], ipv4[2], ipv4[3])
}
