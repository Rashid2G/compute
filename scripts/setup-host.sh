#!/usr/bin/env bash
set -euo pipefail

# Compute Host Readiness & Setup Script
# Configures host bridge networking, sysctl, firecracker prerequisites,
# directories, and validates virtualization readiness.

BRIDGE_NAME="${1:-mft3br0}"
BRIDGE_IP="${2:-172.16.0.1/16}"
SUBNET="${3:-172.16.0.0/16}"
GATEWAY="${4:-172.16.0.1}"
PHYSICAL_NIC="${5:-}"

# Check for dry-run or check-only mode
DRY_RUN="${DRY_RUN:-0}"

# Privilege handling (support root or sudo)
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
    if command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
    else
        echo "Error: this script requires root privileges or sudo." >&2
        exit 1
    fi
fi

echo "==================================================================="
echo "  Compute Host Environment & Readiness Verification"
echo "==================================================================="

# -------------------------------------------------------------------
# Preflight Checks
# -------------------------------------------------------------------
echo "==> Running Preflight System Checks..."

# 1. Linux Environment & Kernel Check
OS_NAME="$(uname -s)"
KERNEL_VER="$(uname -r)"
if [ "${OS_NAME}" != "Linux" ]; then
    echo "  [FAIL] Operating system is ${OS_NAME}. Compute requires Linux." >&2
    exit 1
fi
echo "  [OK] Operating System: Linux (kernel ${KERNEL_VER})"

# 2. KVM Support Check
if [ -e /dev/kvm ]; then
    if [ -r /dev/kvm ] && [ -w /dev/kvm ]; then
        echo "  [OK] Hardware Virtualization: /dev/kvm exists and is read/write accessible"
    else
        echo "  [WARN] /dev/kvm exists but requires elevated permissions or 'kvm' group membership"
        if [ -n "${SUDO}" ]; then
            $SUDO chmod 666 /dev/kvm 2>/dev/null || true
        fi
    fi
else
    echo "  [WARN] /dev/kvm device not found. Ensure nested virtualization is enabled on host."
fi

# 3. Cgroups v2 Check
if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
    echo "  [OK] Cgroups v2 unified hierarchy detected (/sys/fs/cgroup/cgroup.controllers)"
else
    echo "  [WARN] Cgroups v2 not detected. Memory and CPU limits may run in fallback mode."
fi

# 4. Tooling Availability Check
REQUIRED_TOOLS=("ip" "curl")
for tool in "${REQUIRED_TOOLS[@]}"; do
    if command -v "${tool}" >/dev/null 2>&1; then
        echo "  [OK] Tool available: ${tool}"
    else
        echo "  [FAIL] Required tool missing: ${tool}. Please install via package manager." >&2
        exit 1
    fi
done

# Check iptables / nftables
if command -v iptables >/dev/null 2>&1; then
    echo "  [OK] Firewall tool: iptables detected"
elif command -v nft >/dev/null 2>&1; then
    echo "  [OK] Firewall tool: nftables detected"
else
    echo "  [WARN] Neither iptables nor nftables found. Network isolation and NAT may require installation."
fi

# Check Firecracker and Jailer
if command -v firecracker >/dev/null 2>&1; then
    echo "  [OK] Firecracker binary found in PATH: $(command -v firecracker)"
elif [ -x /usr/local/bin/firecracker ]; then
    echo "  [OK] Firecracker binary found: /usr/local/bin/firecracker"
else
    echo "  [INFO] Firecracker not in PATH or /usr/local/bin."
    echo "         Download release: curl -L https://github.com/firecracker-microvm/firecracker/releases/latest/download/firecracker-v1.7.0-x86_64.tgz | tar -xz"
fi

if command -v jailer >/dev/null 2>&1; then
    echo "  [OK] Jailer binary found in PATH: $(command -v jailer)"
elif [ -x /usr/local/bin/jailer ]; then
    echo "  [OK] Jailer binary found: /usr/local/bin/jailer"
else
    echo "  [INFO] Jailer binary optional for root jail isolation."
fi

if [ "${DRY_RUN}" = "1" ] || [ "${CHECK_ONLY:-0}" = "1" ]; then
    echo ""
    echo "==> Dry run / Check-only mode complete."
    exit 0
fi

# -------------------------------------------------------------------
# Directory Initialization
# -------------------------------------------------------------------
echo ""
echo "==> Initializing Platform Directories..."
COMPUTE_DIRS=(
    "/var/lib/compute/images"
    "/var/lib/compute/sandboxes"
    "/var/lib/compute/ssh"
    "/etc/compute"
)

for dir in "${COMPUTE_DIRS[@]}"; do
    if [ ! -d "${dir}" ]; then
        $SUDO mkdir -p "${dir}"
        echo "  Created directory: ${dir}"
    else
        echo "  Directory exists: ${dir}"
    fi
done

# Enforce secure permissions on SSH directory
$SUDO chmod 700 /var/lib/compute/ssh 2>/dev/null || true

# -------------------------------------------------------------------
# Host Network Configuration
# -------------------------------------------------------------------
echo ""
echo "==> Configuring Host VM Bridge: ${BRIDGE_NAME} (${BRIDGE_IP}, Gateway: ${GATEWAY})..."

# 1. Create main bridge if missing (tolerate a concurrent creator)
if ! ip link show "${BRIDGE_NAME}" >/dev/null 2>&1; then
    $SUDO ip link add "${BRIDGE_NAME}" type bridge 2>/dev/null || true
fi
if ! ip link show "${BRIDGE_NAME}" >/dev/null 2>&1; then
    echo "Error: bridge ${BRIDGE_NAME} could not be created" >&2
    exit 1
fi

# 2. Assign Bridge IP
$SUDO ip addr add "${BRIDGE_IP}" dev "${BRIDGE_NAME}" 2>/dev/null || true
$SUDO ip link set "${BRIDGE_NAME}" up

# 3. Detect and link physical NIC to the bridge
if [ -z "${PHYSICAL_NIC}" ]; then
    PHYSICAL_NIC=$(ip route show default | awk '/default/ {print $5}' | head -n1 || true)
fi

if [ "${PHYSICAL_NIC}" = "none" ]; then
    echo "==> Skipping physical NIC attachment (requested with 'none')."
elif [ -n "${PHYSICAL_NIC}" ] && [ "${PHYSICAL_NIC}" != "${BRIDGE_NAME}" ]; then
    echo "==> Linking physical NIC ${PHYSICAL_NIC} to bridge ${BRIDGE_NAME}..."
    $SUDO ip link set dev "${PHYSICAL_NIC}" master "${BRIDGE_NAME}" 2>/dev/null || true
    $SUDO ip link set dev "${PHYSICAL_NIC}" up 2>/dev/null || true
fi

# 4. Link all other bridges on the host VM to the main bridge via veth pairs
for br in $(ip -o link show type bridge 2>/dev/null | awk -F': ' '{print $2}' | cut -d'@' -f1 || true); do
    if [ -n "${br}" ] && [ "${br}" != "${BRIDGE_NAME}" ]; then
        SAFE_NAME="${br:0:10}"
        VETH_M="vbm-${SAFE_NAME}"
        VETH_P="vbp-${SAFE_NAME}"
        if ! ip link show "${VETH_M}" >/dev/null 2>&1; then
            $SUDO ip link add "${VETH_M}" type veth peer name "${VETH_P}" 2>/dev/null || true
        fi
        $SUDO ip link set dev "${VETH_M}" master "${BRIDGE_NAME}" 2>/dev/null || true
        $SUDO ip link set dev "${VETH_P}" master "${br}" 2>/dev/null || true
        $SUDO ip link set dev "${VETH_M}" up 2>/dev/null || true
        $SUDO ip link set dev "${VETH_P}" up 2>/dev/null || true
        $SUDO ip link set dev "${br}" up 2>/dev/null || true
    fi
done

# 5. Enable IPv4 forwarding and bridge bypass
echo "==> Enabling IPv4 forwarding & netfilter bypass..."
$SUDO sysctl -w net.ipv4.ip_forward=1 >/dev/null
$SUDO sysctl -w net.bridge.bridge-nf-call-iptables=0 2>/dev/null || true
$SUDO sysctl -w net.bridge.bridge-nf-call-arptables=0 2>/dev/null || true

# 6. Configure iptables forwarding rules and NAT masquerade
if command -v iptables >/dev/null 2>&1; then
    echo "==> Setting up bridge forwarding and NAT masquerade rules..."
    if ! $SUDO iptables -C FORWARD -i "${BRIDGE_NAME}" -j ACCEPT 2>/dev/null; then
        $SUDO iptables -I FORWARD -i "${BRIDGE_NAME}" -j ACCEPT
    fi
    if ! $SUDO iptables -C FORWARD -o "${BRIDGE_NAME}" -j ACCEPT 2>/dev/null; then
        $SUDO iptables -I FORWARD -o "${BRIDGE_NAME}" -j ACCEPT
    fi
    if ! $SUDO iptables -t nat -C POSTROUTING -s "${SUBNET}" ! -o "${BRIDGE_NAME}" -j MASQUERADE 2>/dev/null; then
        $SUDO iptables -t nat -A POSTROUTING -s "${SUBNET}" ! -o "${BRIDGE_NAME}" -j MASQUERADE 2>/dev/null || true
    fi
fi

echo ""
echo "==================================================================="
echo "  Compute Host Readiness Complete!"
echo "==================================================================="
echo "  Bridge:           ${BRIDGE_NAME} (${BRIDGE_IP}, Gateway: ${GATEWAY})"
echo "  Data Directories: /var/lib/compute/images, /var/lib/compute/sandboxes, /var/lib/compute/ssh"
echo "  Configuration:    /etc/compute"
echo ""
echo "  Next Steps:"
echo "    1. Compile & run daemon:  make dev (or: ./bin/computed)"
echo "    2. Launch a sandbox:      ./bin/compute sandbox create my-vm"
echo "    3. Connect with SSH:      ./bin/compute ssh my-vm"
echo "    4. ProxyCommand SSH:      ssh -o ProxyCommand=\"./bin/compute ssh --proxy %h\" compute@my-vm"
echo "==================================================================="
