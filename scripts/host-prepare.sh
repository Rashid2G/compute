#!/usr/bin/env bash
# Idempotent host preparation for Compute.
#
# Loads the kernel modules and sysctls required by Firecracker sandboxes. It
# deliberately does NOT enslave the default network interface: NAT mode keeps
# the host's normal connectivity and the daemon adds masquerade rules for the
# sandbox subnet.
set -euo pipefail

BRIDGE_NAME="${COMPUTE_BRIDGE_NAME:-mft3br0}"
BRIDGE_IP="${COMPUTE_BRIDGE_IP:-172.16.0.1/16}"
CREATE_BRIDGE="${COMPUTE_CREATE_BRIDGE:-0}"

SUDO=""
if [ "$(id -u)" -ne 0 ]; then
    if command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
    else
        echo "Error: root privileges or sudo are required." >&2
        exit 1
    fi
fi

echo "==> Loading kernel modules"
for module in tun bridge br_netfilter vhost_net nf_nat; do
    if [ -d "/sys/module/${module}" ]; then
        echo "  already loaded: ${module}"
        continue
    fi
    if command -v modprobe >/dev/null 2>&1 && $SUDO modprobe "${module}" 2>/dev/null; then
        echo "  loaded: ${module}"
    else
        echo "  [WARN] could not load ${module}; install linux-modules-extra or enable it in the kernel" >&2
    fi
done

echo "==> Applying sysctls"
$SUDO sysctl -w net.ipv4.ip_forward=1 >/dev/null
if [ -d /sys/module/br_netfilter ]; then
    $SUDO sysctl -w net.bridge.bridge-nf-call-iptables=0 >/dev/null 2>&1 || true
    $SUDO sysctl -w net.bridge.bridge-nf-call-arptables=0 >/dev/null 2>&1 || true
fi

if [ "${CREATE_BRIDGE}" = "1" ]; then
    echo "==> Ensuring bridge ${BRIDGE_NAME} (${BRIDGE_IP})"
    if ! ip link show "${BRIDGE_NAME}" >/dev/null 2>&1; then
        $SUDO ip link add "${BRIDGE_NAME}" type bridge
    fi
    if ! ip -o -4 addr show dev "${BRIDGE_NAME}" | grep -q "${BRIDGE_IP%%/*}"; then
        $SUDO ip addr add "${BRIDGE_IP}" dev "${BRIDGE_NAME}"
    fi
    $SUDO ip link set "${BRIDGE_NAME}" up
    echo "  bridge ready (NIC left untouched)"
else
    echo "==> Bridge ${BRIDGE_NAME} is created by computed at startup (COMPUTE_CREATE_BRIDGE=1 to pre-create)"
fi

echo "==> Host preparation complete."
