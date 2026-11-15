#!/usr/bin/env bash
# Install a Firecracker-compatible uncompressed Linux kernel (vmlinux).
#
# Firecracker boots an uncompressed ELF kernel, so a distribution bzImage or
# vmlinuz cannot be used directly. This script downloads the pinned, official
# Firecracker CI build for the host architecture, verifies its SHA-256, and
# installs it atomically as <images_dir>/vmlinux.
#
# Usage:
#   sudo ./scripts/build-kernel.sh [images_dir]
#
# Environment overrides (for air-gapped hosts or custom kernels):
#   COMPUTE_KERNEL_SRC=/path/to/vmlinux   Copy a local kernel instead of downloading.
#   COMPUTE_KERNEL_URL=https://...        Download a custom kernel (requires SHA256).
#   COMPUTE_KERNEL_SHA256=<hex>           Expected SHA-256 for COMPUTE_KERNEL_URL.
set -euo pipefail

DEST_DIR="${1:-/var/lib/compute/images}"
OUT="${DEST_DIR}/vmlinux"

# Keep this in lockstep with the Firecracker release pinned in Dockerfile.daemon
# (v1.17.0). The CI artifact directory is immutable, so both the URL and the
# checksum stay valid for existing installs.
CI_TAG="v1.15"
KERNEL_VERSION="6.1.155"

case "$(uname -m)" in
    x86_64 | amd64)
        CI_ARCH="x86_64"
        DEFAULT_URL="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/${CI_TAG}/x86_64/vmlinux-${KERNEL_VERSION}"
        DEFAULT_SHA256="e20e46d0c36c55c0d1014eb20576171b3f3d922260d9f792017aeff53af3d4f2"
        ;;
    aarch64 | arm64)
        CI_ARCH="aarch64"
        DEFAULT_URL="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/${CI_TAG}/aarch64/vmlinux-${KERNEL_VERSION}"
        DEFAULT_SHA256="e3544b10603acbf3db492cb52e000d22ba202cb4b63b9add027565683e11c591"
        ;;
    *)
        echo "unsupported architecture: $(uname -m) (expected x86_64 or aarch64)" >&2
        exit 2
        ;;
esac

URL="${COMPUTE_KERNEL_URL:-${DEFAULT_URL}}"
SHA256="${COMPUTE_KERNEL_SHA256:-${DEFAULT_SHA256}}"

if [ -n "${COMPUTE_KERNEL_URL:-}" ] && [ -z "${COMPUTE_KERNEL_SHA256:-}" ]; then
    echo "COMPUTE_KERNEL_URL requires COMPUTE_KERNEL_SHA256 so the download can be verified" >&2
    exit 2
fi
if [ -n "${COMPUTE_KERNEL_SHA256:-}" ] && [ -z "${COMPUTE_KERNEL_URL:-}" ] && [ -z "${COMPUTE_KERNEL_SRC:-}" ]; then
    echo "COMPUTE_KERNEL_SHA256 requires COMPUTE_KERNEL_URL or COMPUTE_KERNEL_SRC" >&2
    exit 2
fi

require() {
    if ! command -v "$1" >/dev/null 2>&1; then
        echo "missing required command '$1': install it and re-run (or use COMPUTE_IMAGES_MODE=download)" >&2
        exit 1
    fi
}
require head
require install
require mktemp
require sha256sum

is_elf() {
    [ "$(head -c 4 "$1" 2>/dev/null || true)" = "$(printf '\177ELF')" ]
}

file_sha256() {
    sha256sum "$1" | awk '{print $1}'
}

mkdir -p "${DEST_DIR}"

if [ -n "${COMPUTE_KERNEL_SRC:-}" ]; then
    echo "==> Installing kernel from ${COMPUTE_KERNEL_SRC}..."
    if [ ! -f "${COMPUTE_KERNEL_SRC}" ]; then
        echo "COMPUTE_KERNEL_SRC does not exist: ${COMPUTE_KERNEL_SRC}" >&2
        exit 1
    fi
    tmp="$(mktemp "${DEST_DIR}/.vmlinux.XXXXXX")"
    trap 'rm -f "${tmp:-}"' EXIT
    install -m 0644 "${COMPUTE_KERNEL_SRC}" "${tmp}"
    if [ -n "${COMPUTE_KERNEL_SHA256:-}" ] && [ "$(file_sha256 "${tmp}")" != "${COMPUTE_KERNEL_SHA256}" ]; then
        echo "kernel checksum mismatch: ${COMPUTE_KERNEL_SRC} does not match COMPUTE_KERNEL_SHA256" >&2
        exit 1
    fi
else
    require curl
    if [ -f "${OUT}" ] && is_elf "${OUT}" && [ "$(file_sha256 "${OUT}")" = "${SHA256}" ]; then
        echo "==> Kernel already present and verified: ${OUT}"
        echo "    version ${KERNEL_VERSION} (${CI_ARCH})"
        exit 0
    fi
    echo "==> Downloading Firecracker kernel ${KERNEL_VERSION} (${CI_ARCH})..."
    tmp="$(mktemp "${DEST_DIR}/.vmlinux.XXXXXX")"
    trap 'rm -f "${tmp:-}"' EXIT
    curl --fail --location --retry 3 --retry-delay 2 --connect-timeout 20 \
        --output "${tmp}" "${URL}"
    got="$(file_sha256 "${tmp}")"
    if [ "${got}" != "${SHA256}" ]; then
        echo "kernel checksum mismatch for ${URL}" >&2
        echo "  expected ${SHA256}" >&2
        echo "  got      ${got}" >&2
        exit 1
    fi
fi

if ! is_elf "${tmp}"; then
    echo "downloaded kernel is not an ELF image: ${tmp}" >&2
    exit 1
fi

install -m 0644 "${tmp}" "${OUT}"
rm -f "${tmp}"
trap - EXIT

echo "==> Kernel installed: ${OUT}"
echo "    version ${KERNEL_VERSION} (${CI_ARCH}), sha256 ${SHA256}"
