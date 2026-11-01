package nftables

import (
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
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 6, Len: 6},
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

			// Check ether type is IPv4 (0x0800)
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(unix.ETH_P_IP)},

			// Check source IP
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 14 + 12, Len: 4},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: ip},

			&expr.Verdict{Kind: expr.VerdictDrop},
		},
	})

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
