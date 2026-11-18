#!/usr/bin/env bash
# End-to-end host installation and image bootstrap script for Compute.
#
# Prepares a fresh Linux host (kernel modules, sysctls, preflight checks) and
# builds/verifies the Firecracker kernel (vmlinux), Ubuntu 24.04 ext4 rootfs
# (ubuntu-24.04.ext4), and internal Ed25519 SSH key under ./data/images so
# `docker compose up -d` starts `computed` immediately without an init container.
#
# Usage:
#   ./scripts/install.sh [options]
#
# Options:
#   --images-only         Only bootstrap kernel, rootfs, and SSH key (skip preflight & host-prepare)
#   --skip-preflight      Skip read-only preflight checks
#   --skip-host-prepare   Skip loading kernel modules and sysctl settings
#   --force               Force rebuild of the Ubuntu 24.04 rootfs image even if present
#   --up                  Start the Docker Compose stack (`docker compose up -d`) after setup
#   -h, --help            Show this help message
set -euo pipefail

export PATH="${PATH}:/sbin:/usr/sbin:/usr/local/sbin:/usr/local/bin"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

IMAGES_ONLY=0
SKIP_PREFLIGHT=0
SKIP_HOST_PREPARE=0
FORCE_REBUILD="${FORCE_REBUILD:-0}"
START_STACK=0

while [ $# -gt 0 ]; do
    case "$1" in
        --images-only)
            IMAGES_ONLY=1
            shift
            ;;
        --skip-preflight)
            SKIP_PREFLIGHT=1
            shift
            ;;
        --skip-host-prepare)
            SKIP_HOST_PREPARE=1
            shift
            ;;
        --force)
            FORCE_REBUILD=1
            shift
            ;;
        --up)
            START_STACK=1
            shift
            ;;
        -h|--help)
            sed -n '2,18p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *)
            echo "Unknown option: $1 (run ./scripts/install.sh --help for usage)" >&2
            exit 2
            ;;
    esac
done

# Load .env when present so custom paths and image settings are respected.
if [ -f .env ]; then
    set -a
    # shellcheck disable=SC1091
    . ./.env
    set +a
fi

DATA_DIR="${COMPUTE_HOST_DATA_DIR:-./data}"
IMAGES_DIR="${COMPUTE_IMAGES_DIR:-${DATA_DIR}/images}"
ROOTFS_SIZE_MB="${COMPUTE_ROOTFS_SIZE_MB:-2048}"

echo "==================================================================="
echo "  Compute Host Installation & Base Image Bootstrap"
echo "==================================================================="
echo "  Data directory:   ${DATA_DIR}"
echo "  Images directory: ${IMAGES_DIR}"
echo "  Rootfs size:      ${ROOTFS_SIZE_MB} MB"
echo "==================================================================="

if [ "${IMAGES_ONLY}" -eq 0 ] && [ "${SKIP_HOST_PREPARE}" -eq 0 ]; then
    echo ""
    echo "==> [1/4] Preparing host kernel modules and sysctls..."
    ./scripts/host-prepare.sh
else
    echo ""
    echo "==> [1/4] Skipping host kernel module/sysctl preparation."
fi

if [ "${IMAGES_ONLY}" -eq 0 ] && [ "${SKIP_PREFLIGHT}" -eq 0 ]; then
    echo ""
    echo "==> [2/4] Running host preflight checks..."
    ./scripts/preflight.sh
else
    echo ""
    echo "==> [2/4] Skipping preflight checks."
fi

echo ""
echo "==> [3/4] Installing Firecracker kernel (${IMAGES_DIR}/vmlinux)..."
mkdir -p "${DATA_DIR}" "${IMAGES_DIR}/ssh" "${DATA_DIR}/vms" 2>/dev/null || sudo mkdir -p "${DATA_DIR}" "${IMAGES_DIR}/ssh" "${DATA_DIR}/vms"
if [ "$(id -u)" -ne 0 ] && { [ ! -w "${IMAGES_DIR}" ] || [ ! -w "${IMAGES_DIR}/ssh" ]; }; then
    sudo chown -R "$(id -u):$(id -g)" "${IMAGES_DIR}" 2>/dev/null || true
fi
chmod 700 "${IMAGES_DIR}/ssh" 2>/dev/null || true

COMPUTE_KERNEL_SRC="${COMPUTE_KERNEL_SRC:-}" \
COMPUTE_KERNEL_URL="${COMPUTE_KERNEL_URL:-}" \
COMPUTE_KERNEL_SHA256="${COMPUTE_KERNEL_SHA256:-}" \
./scripts/build-kernel.sh "${IMAGES_DIR}"

echo ""
echo "==> [4/4] Building Ubuntu 24.04 rootfs & internal SSH key (${IMAGES_DIR}/ubuntu-24.04.ext4)..."
FORCE_REBUILD="${FORCE_REBUILD}" ./scripts/build-rootfs.sh "${IMAGES_DIR}" "${ROOTFS_SIZE_MB}"

echo ""
echo "==================================================================="
echo "  Compute Installation Complete!"
echo "==================================================================="
echo "  Kernel:   ${IMAGES_DIR}/vmlinux"
echo "  Rootfs:   ${IMAGES_DIR}/ubuntu-24.04.ext4"
echo "  SSH Key:  ${IMAGES_DIR}/ssh/id_ed25519"
echo "==================================================================="

if [ "${START_STACK}" -eq 1 ]; then
    echo ""
    echo "==> Starting Compute stack via Docker Compose..."
    docker compose up -d
    echo "==> Compute daemon started. Verify readiness with:"
    echo "    curl -fsS http://127.0.0.1:${COMPUTE_API_PORT:-18080}/api/v1/ready | python3 -m json.tool"
else
    echo ""
    echo "Next steps:"
    echo "  1. Start the daemon:     docker compose up -d   (or: make up)"
    echo "  2. Check readiness:      curl -fsS http://127.0.0.1:${COMPUTE_API_PORT:-18080}/api/v1/ready | python3 -m json.tool"
    echo "  3. Open Web Dashboard:   http://127.0.0.1:${COMPUTE_API_PORT:-18080}/"
fi
