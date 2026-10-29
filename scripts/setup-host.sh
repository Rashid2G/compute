#!/usr/bin/env bash
set -euo pipefail

BRIDGE_NAME="${1:-mft3br0}"
BRIDGE_IP="${2:-172.16.0.1/16}"
SUBNET="${3:-172.16.0.0/16}"

echo "==> Configuring Host Virtual Bridge: ${BRIDGE_NAME} (${BRIDGE_IP})..."

if ! ip link show "${BRIDGE_NAME}" >/dev/null 2>&1; then
    sudo ip link add "${BRIDGE_NAME}" type bridge
fi

sudo ip addr add "${BRIDGE_IP}" dev "${BRIDGE_NAME}" 2>/dev/null || true
sudo ip link set "${BRIDGE_NAME}" up

echo "==> Enabling IPv4 forwarding..."
sudo sysctl -w net.ipv4.ip_forward=1 >/dev/null

echo "==> Setting up NAT / Masquerade rules..."
# Default egress interface
DEFAULT_IFACE=$(ip route show default | awk '/default/ {print $5}' | head -n1)

# Check and add MASQUERADE
if ! sudo iptables -t nat -C POSTROUTING -s "${SUBNET}" ! -o "${BRIDGE_NAME}" -j MASQUERADE 2>/dev/null; then
    sudo iptables -t nat -A POSTROUTING -s "${SUBNET}" ! -o "${BRIDGE_NAME}" -j MASQUERADE
fi

# Check and add FORWARD rules
if ! sudo iptables -C FORWARD -i "${BRIDGE_NAME}" -j ACCEPT 2>/dev/null; then
    sudo iptables -I FORWARD -i "${BRIDGE_NAME}" -j ACCEPT
fi
if ! sudo iptables -C FORWARD -o "${BRIDGE_NAME}" -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null; then
    sudo iptables -I FORWARD -o "${BRIDGE_NAME}" -m state --state RELATED,ESTABLISHED -j ACCEPT
fi

echo "==> Virtual bridge ${BRIDGE_NAME} is ready with NAT enabled via ${DEFAULT_IFACE}."
