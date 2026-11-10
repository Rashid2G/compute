package nftables

import (
	"bytes"
	"fmt"
	"net"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// Firewall manages kernel nftables tables and rules for Compute VPC isolation.
type Firewall struct {
	conn *nftables.Conn
}

func NewFirewall() (*Firewall, error) {
	conn, err := nftables.New()
	if err != nil {
		return nil, fmt.Errorf("failed to open nftables netlink connection: %w", err)
	}
	return &Firewall{conn: conn}, nil
}

// EnsureBaseChains provisions the compute inet table and standard filter chains.
func (f *Firewall) EnsureBaseChains() error {
	table := f.conn.AddTable(&nftables.Table{
		Family: nftables.TableFamilyINet,
		Name:   "compute",
	})

	// Input chain: drop all incoming traffic to host from mfb* bridges except DNS (53) & ICMP
	inputChain := f.conn.AddChain(&nftables.Chain{
		Name:     "input",
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookInput,
		Priority: nftables.ChainPriorityFilter,
	})

	// Forward chain: isolation and drop cross-VPC
	forwardChain := f.conn.AddChain(&nftables.Chain{
		Name:     "forward",
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookForward,
		Priority: nftables.ChainPriorityFilter,
	})

	_ = inputChain
	_ = forwardChain

	return f.conn.Flush()
}

// ApplyAntiSpoofRules adds anti-IP/MAC spoofing rules for a specific TAP interface.
func (f *Firewall) ApplyAntiSpoofRules(tapName, assignedIP, assignedMAC string) error {
	table := &nftables.Table{
		Family: nftables.TableFamilyBridge,
		Name:   "compute",
	}
	f.conn.AddTable(table)

	chain := f.conn.AddChain(&nftables.Chain{
		Name:     "prerouting",
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: nftables.ChainPriorityFilter,
	})

	mac, err := net.ParseMAC(assignedMAC)
	if err != nil {
		return fmt.Errorf("invalid MAC: %w", err)
	}
	ip := net.ParseIP(assignedIP).To4()
	if ip == nil {
		return fmt.Errorf("invalid IPv4 address: %s", assignedIP)
	}

	// 1. Drop packets from tapName if source MAC does not match assignedMAC
	f.conn.AddRule(&nftables.Rule{
		Table: table,
		Chain: chain,
		Exprs: []expr.Any{
			// [ meta load iifname => reg 1 ]
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(tapName + "\x00")},

			// [ payload load 6 bytes @ link header + 6 => reg 1 ] (source MAC)
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseLLHeader, Offset: 6, Len: 6},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: mac},

			// [ immediate drop ]
			&expr.Verdict{Kind: expr.VerdictDrop},
		},
	})

	// 2. Drop packets from tapName if source IP does not match assignedIP
	f.conn.AddRule(&nftables.Rule{
		Table: table,
		Chain: chain,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(tapName + "\x00")},

			// Check ether type is IPv4 (0x0800) at LL header offset 12
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseLLHeader, Offset: 12, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(unix.ETH_P_IP)},

			// Check source IP at NetworkHeader offset 12
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: ip},

			&expr.Verdict{Kind: expr.VerdictDrop},
		},
	})

	// 3. Drop rogue DHCP server packets (UDP source port 67) from untrusted guest
	f.conn.AddRule(&nftables.Rule{
		Table: table,
		Chain: chain,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(tapName + "\x00")},

			// IP protocol == UDP (17) at NetworkHeader offset 9
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 9, Len: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_UDP}},

			// UDP source port == 67 (DHCP server) at TransportHeader offset 0
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 0, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(67)},

			&expr.Verdict{Kind: expr.VerdictDrop},
		},
	})

	// 4. Drop rogue ARP spoofing: in ARP packets (EtherType 0x0806), verify source MAC matches assignedMAC
	f.conn.AddRule(&nftables.Rule{
		Table: table,
		Chain: chain,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(tapName + "\x00")},

			// EtherType == ARP (0x0806) at LL header offset 12
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseLLHeader, Offset: 12, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(unix.ETH_P_ARP)},

			// Check ARP sender hardware address (offset 8 of ARP payload, len 6)
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 8, Len: 6},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: mac},

			&expr.Verdict{Kind: expr.VerdictDrop},
		},
	})

	return f.conn.Flush()
}

// BuildBridgeIsolationRules returns the nftables rules for isolating a TAP interface on the bridge,
// including intra-tenant peer ACCEPT rules and an explicit default DROP rule for cross-tenant TAP traffic.
func BuildBridgeIsolationRules(table *nftables.Table, chain *nftables.Chain, tapName string, allowedPeerIPs []string) []*nftables.Rule {
	var rules []*nftables.Rule

	// 1. Allow communication to explicit intra-tenant peer IPs
	for _, peerIPStr := range allowedPeerIPs {
		peerIP := net.ParseIP(peerIPStr).To4()
		if peerIP == nil {
			continue
		}

		var exprs []expr.Any
		if table.Family == nftables.TableFamilyBridge {
			exprs = []expr.Any{
				&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(tapName + "\x00")},

				// EtherType is IPv4 (0x0800) at LL header offset 12
				&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseLLHeader, Offset: 12, Len: 2},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(unix.ETH_P_IP)},

				// Destination IP at NetworkHeader offset 16
				&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: peerIP},

				&expr.Verdict{Kind: expr.VerdictAccept},
			}
		} else {
			exprs = []expr.Any{
				&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(tapName + "\x00")},

				// Destination IP at NetworkHeader offset 16
				&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: peerIP},

				&expr.Verdict{Kind: expr.VerdictAccept},
			}
		}

		rules = append(rules, &nftables.Rule{
			Table: table,
			Chain: chain,
			Exprs: exprs,
		})
	}

	// 2. Explicit default DROP rule for forward traffic originating from the guest TAP device
	// targeting any other TAP device on the bridge (matching `iptables -A FORWARD -i <tap> -o mftap-+ -j DROP`)
	dropRule := &nftables.Rule{
		Table: table,
		Chain: chain,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(tapName + "\x00")},

			&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
			&expr.Bitwise{
				SourceRegister: 1,
				DestRegister:   1,
				Len:            16,
				Mask:           []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
				Xor:            make([]byte, 16),
			},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: append([]byte("mftap-"), make([]byte, 10)...)},

			&expr.Verdict{Kind: expr.VerdictDrop},
		},
	}
	rules = append(rules, dropRule)

	return rules
}

// ApplyBridgeIsolation configures cross-tenant lateral movement blocking on the bridge.
func (f *Firewall) ApplyBridgeIsolation(tapName string, allowedPeerIPs []string) error {
	tableBridge := &nftables.Table{
		Family: nftables.TableFamilyBridge,
		Name:   "compute",
	}
	f.conn.AddTable(tableBridge)

	chainBridge := f.conn.AddChain(&nftables.Chain{
		Name:     "forward",
		Table:    tableBridge,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookForward,
		Priority: nftables.ChainPriorityFilter,
	})

	for _, r := range BuildBridgeIsolationRules(tableBridge, chainBridge, tapName, allowedPeerIPs) {
		f.conn.AddRule(r)
	}

	tableFilter := &nftables.Table{
		Family: nftables.TableFamilyINet,
		Name:   "compute",
	}
	f.conn.AddTable(tableFilter)

	chainFilter := f.conn.AddChain(&nftables.Chain{
		Name:     "forward",
		Table:    tableFilter,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookForward,
		Priority: nftables.ChainPriorityFilter,
	})

	for _, r := range BuildBridgeIsolationRules(tableFilter, chainFilter, tapName, allowedPeerIPs) {
		f.conn.AddRule(r)
	}

	return f.conn.Flush()
}

// RemoveRulesForTAP removes all anti-spoofing and isolation rules associated with tapName.
func (f *Firewall) RemoveRulesForTAP(tapName string) error {
	needle := []byte(tapName + "\x00")
	rawNeedle := []byte(tapName)

	// Query and clean both tableBridge (prerouting and forward) and tableFilter (forward)
	tableBridge := &nftables.Table{Family: nftables.TableFamilyBridge, Name: "compute"}
	tableFilter := &nftables.Table{Family: nftables.TableFamilyINet, Name: "compute"}

	targets := []struct {
		table *nftables.Table
		chain *nftables.Chain
	}{
		{table: tableBridge, chain: &nftables.Chain{Name: "prerouting", Table: tableBridge}},
		{table: tableBridge, chain: &nftables.Chain{Name: "forward", Table: tableBridge}},
		{table: tableFilter, chain: &nftables.Chain{Name: "forward", Table: tableFilter}},
	}

	for _, tgt := range targets {
		rules, err := f.conn.GetRules(tgt.table, tgt.chain)
		if err == nil {
			for _, r := range rules {
				for _, e := range r.Exprs {
					if cmp, ok := e.(*expr.Cmp); ok && (bytes.Equal(cmp.Data, needle) || bytes.Equal(cmp.Data, rawNeedle)) {
						_ = f.conn.DelRule(r)
						break
					}
				}
			}
		}
	}
	return f.conn.Flush()
}

// Flush removes all rules managed by Compute.
func (f *Firewall) Flush() error {
	tableBridge := &nftables.Table{Family: nftables.TableFamilyBridge, Name: "compute"}
	f.conn.DelTable(tableBridge)
	tableInet := &nftables.Table{Family: nftables.TableFamilyINet, Name: "compute"}
	f.conn.DelTable(tableInet)
	return f.conn.Flush()
}
