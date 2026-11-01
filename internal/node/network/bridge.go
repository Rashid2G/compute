package network

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// BridgeManager manages Linux bridges and TAP interfaces for VPCs using Netlink.
type BridgeManager struct{}

func NewBridgeManager() *BridgeManager {
	return &BridgeManager{}
}

// BridgeName returns the standard Linux bridge interface name for a given VNI.
func BridgeName(vni int) string {
	return fmt.Sprintf("mfb%06x", vni)
}

// TAPName returns the deterministic 10-char TAP device name for an instance.
func TAPName(instanceID string) string {
	h := sha256.Sum256([]byte(instanceID))
	return fmt.Sprintf("mf%s", hex.EncodeToString(h[:4]))
}

// EnsureVPCBridge provisions or reuses a per-VPC Linux bridge.
func (bm *BridgeManager) EnsureVPCBridge(bridgeName string, gatewayIP string, prefixLen int, gatewayMAC string) (*netlink.Bridge, error) {
	link, err := netlink.LinkByName(bridgeName)
	if err == nil {
		if br, ok := link.(*netlink.Bridge); ok {
			return br, nil
		}
		return nil, fmt.Errorf("interface %s exists but is not a bridge", bridgeName)
	}

	hwAddr, err := net.ParseMAC(gatewayMAC)
	if err != nil {
		hwAddr = net.HardwareAddr{0x02, 0x6d, 0x66, 0x00, 0x10, 0x00}
	}

	br := &netlink.Bridge{
		LinkAttrs: netlink.LinkAttrs{
			Name:         bridgeName,
			HardwareAddr: hwAddr,
		},
	}

	if err := netlink.LinkAdd(br); err != nil {
		return nil, fmt.Errorf("failed to create bridge %s: %w", bridgeName, err)
	}

	ipNet := &net.IPNet{
		IP:   net.ParseIP(gatewayIP),
		Mask: net.CIDRMask(prefixLen, 32),
	}
	addr := &netlink.Addr{IPNet: ipNet}
	if err := netlink.AddrAdd(br, addr); err != nil {
		_ = netlink.LinkDel(br)
		return nil, fmt.Errorf("failed to assign gateway IP %s to bridge: %w", gatewayIP, err)
	}

	if err := netlink.LinkSetUp(br); err != nil {
		_ = netlink.LinkDel(br)
		return nil, fmt.Errorf("failed to bring bridge up: %w", err)
	}

	return br, nil
}

// CreateTAP creates a TAP interface, sets its owner UID, and enslaves it to the VPC bridge.
func (bm *BridgeManager) CreateTAP(tapName string, ownerUID int, bridge *netlink.Bridge) (*netlink.Tuntap, error) {
	// If existing TAP, clean up first
	if link, err := netlink.LinkByName(tapName); err == nil {
		_ = netlink.LinkDel(link)
	}

	tap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{
			Name:   tapName,
			TxQLen: 1000,
		},
		Mode:  netlink.TUNTAP_MODE_TAP,
		Owner: uint32(ownerUID),
	}

	if err := netlink.LinkAdd(tap); err != nil {
		return nil, fmt.Errorf("failed to create TAP device %s: %w", tapName, err)
	}

	if bridge != nil {
		if err := netlink.LinkSetMaster(tap, bridge); err != nil {
			_ = netlink.LinkDel(tap)
			return nil, fmt.Errorf("failed to enslave TAP %s to bridge %s: %w", tapName, bridge.Name, err)
		}
	}

	if err := netlink.LinkSetUp(tap); err != nil {
		_ = netlink.LinkDel(tap)
		return nil, fmt.Errorf("failed to bring TAP %s up: %w", tapName, err)
	}

	return tap, nil
}

// DeleteTAP deletes the specified TAP interface.
func (bm *BridgeManager) DeleteTAP(tapName string) error {
	link, err := netlink.LinkByName(tapName)
	if err != nil {
		return nil // Already gone
	}
	return netlink.LinkDel(link)
}

// DeleteBridge deletes the specified bridge interface.
func (bm *BridgeManager) DeleteBridge(bridgeName string) error {
	link, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return nil
	}
	return netlink.LinkDel(link)
}
