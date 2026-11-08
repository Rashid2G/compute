#!/usr/bin/env bash
set -euo pipefail

BRIDGE_NAME="${1:-mft3br0}"
BRIDGE_IP="${2:-172.16.0.1/16}"
SUBNET="${3:-172.16.0.0/16}"
GATEWAY="${4:-172.16.0.1}"
PHYSICAL_NIC="${5:-}"

echo "==> Configuring Host VM Bridge: ${BRIDGE_NAME} (${BRIDGE_IP}, Gateway: ${GATEWAY})..."

# 1. Create main bridge if missing (tolerate a concurrent creator)
if ! ip link show "${BRIDGE_NAME}" >/dev/null 2>&1; then
    sudo ip link add "${BRIDGE_NAME}" type bridge 2>/dev/null || true
fi
if ! ip link show "${BRIDGE_NAME}" >/dev/null 2>&1; then
    echo "Error: bridge ${BRIDGE_NAME} could not be created" >&2
    exit 1
fi

# 2. Assign Bridge IP
sudo ip addr add "${BRIDGE_IP}" dev "${BRIDGE_NAME}" 2>/dev/null || true
sudo ip link set "${BRIDGE_NAME}" up

# 3. Detect and link physical NIC to the bridge
if [ -z "${PHYSICAL_NIC}" ]; then
    PHYSICAL_NIC=$(ip route show default | awk '/default/ {print $5}' | head -n1 || true)
fi

if [ "${PHYSICAL_NIC}" = "none" ]; then
    echo "==> Skipping physical NIC attachment (requested with 'none')."
elif [ -n "${PHYSICAL_NIC}" ] && [ "${PHYSICAL_NIC}" != "${BRIDGE_NAME}" ]; then
    echo "==> Linking physical NIC ${PHYSICAL_NIC} to bridge ${BRIDGE_NAME}..."
    sudo ip link set dev "${PHYSICAL_NIC}" master "${BRIDGE_NAME}" 2>/dev/null || true
    sudo ip link set dev "${PHYSICAL_NIC}" up 2>/dev/null || true
fi

# 4. Link all other bridges on the host VM to the main bridge via veth pairs
echo "==> Linking all other bridges to main bridge ${BRIDGE_NAME}..."
for br in $(ip -o link show type bridge | awk -F': ' '{print $2}' | cut -d'@' -f1); do
    if [ "${br}" != "${BRIDGE_NAME}" ]; then
        SAFE_NAME="${br:0:10}"
        VETH_M="vbm-${SAFE_NAME}"
        VETH_P="vbp-${SAFE_NAME}"
        echo "    Linking bridge ${br} <-> ${BRIDGE_NAME} via ${VETH_M}/${VETH_P}..."
        if ! ip link show "${VETH_M}" >/dev/null 2>&1; then
            sudo ip link add "${VETH_M}" type veth peer name "${VETH_P}" 2>/dev/null || true
        fi
        sudo ip link set dev "${VETH_M}" master "${BRIDGE_NAME}" 2>/dev/null || true
        sudo ip link set dev "${VETH_P}" master "${br}" 2>/dev/null || true
        sudo ip link set dev "${VETH_M}" up 2>/dev/null || true
        sudo ip link set dev "${VETH_P}" up 2>/dev/null || true
        sudo ip link set dev "${br}" up 2>/dev/null || true
    fi
done

# 5. Enable IPv4 forwarding and bridge bypass
echo "==> Enabling IPv4 forwarding..."
sudo sysctl -w net.ipv4.ip_forward=1 >/dev/null
sudo sysctl -w net.bridge.bridge-nf-call-iptables=0 2>/dev/null || true
sudo sysctl -w net.bridge.bridge-nf-call-arptables=0 2>/dev/null || true

# 6. Configure iptables forwarding rules for DHCP and bridge traffic
echo "==> Setting up bridge forwarding rules..."
if ! sudo iptables -C FORWARD -i "${BRIDGE_NAME}" -j ACCEPT 2>/dev/null; then
    sudo iptables -I FORWARD -i "${BRIDGE_NAME}" -j ACCEPT
fi
if ! sudo iptables -C FORWARD -o "${BRIDGE_NAME}" -j ACCEPT 2>/dev/null; then
    sudo iptables -I FORWARD -o "${BRIDGE_NAME}" -j ACCEPT
fi

# NAT fallback if outbound masquerade is needed
if ! sudo iptables -t nat -C POSTROUTING -s "${SUBNET}" ! -o "${BRIDGE_NAME}" -j MASQUERADE 2>/dev/null; then
    sudo iptables -t nat -A POSTROUTING -s "${SUBNET}" ! -o "${BRIDGE_NAME}" -j MASQUERADE 2>/dev/null || true
fi

echo "==> Host bridge ${BRIDGE_NAME} is ready and linked to router (${GATEWAY}) and all host bridges."
