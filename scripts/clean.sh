#!/usr/bin/env bash
# Clean script for Compute repository, build artifacts, and runtime state.
#
# Usage:
#   ./scripts/clean.sh [clean | clean-hard | clean-hard-all]
#   make clean | make clean-hard | make clean-hard-all
#
# Targets:
#   clean           Remove binaries and build artifacts (same as existing 'make clean')
#   clean-hard      Clean everything except rootfs and kernel base images (stops processes/VMs,
#                   removes databases, VM state, disks, logs, sockets, temp files, networks)
#   clean-hard-all  Clean everything including rootfs builds, kernels, SSH keys, and all data
#
# Options:
#   -h, --help      Show this help message
#   -y, --yes       Non-interactive mode (proceed without confirmation)
#   -f, --force     Force execution without warnings
set -euo pipefail

export PATH="${PATH}:/sbin:/usr/sbin:/usr/local/sbin:/usr/local/bin"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# Load .env when present so custom data/images paths are respected.
if [ -f .env ]; then
    set -a
    # shellcheck disable=SC1091
    . ./.env
    set +a
fi

DATA_DIR="${COMPUTE_HOST_DATA_DIR:-./data}"
IMAGES_DIR="${COMPUTE_IMAGES_DIR:-${DATA_DIR}/images}"
BRIDGE_NAME="${COMPUTE_BRIDGE_NAME:-mft3br0}"

SUDO=""
if [ "$(id -u)" -ne 0 ]; then
    if command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
    fi
fi

safe_rm() {
    for target in "$@"; do
        [ -e "${target}" ] || [ -L "${target}" ] || continue
        if rm -rf "${target}" 2>/dev/null; then
            :
        elif [ -n "${SUDO}" ]; then
            ${SUDO} rm -rf "${target}" 2>/dev/null || true
        fi
    done
}

show_help() {
    sed -n '2,17p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

TARGET=""
FORCE=0

while [ $# -gt 0 ]; do
    case "$1" in
        clean)
            TARGET="clean"
            shift
            ;;
        clean-hard|--hard|hard)
            TARGET="clean-hard"
            shift
            ;;
        clean-hard-all|--hard-all|--all|hard-all|all)
            TARGET="clean-hard-all"
            shift
            ;;
        -y|--yes|-f|--force)
            FORCE=1
            shift
            ;;
        -h|--help)
            show_help
            exit 0
            ;;
        *)
            echo "Error: Unknown argument '$1' (run ./scripts/clean.sh --help for usage)" >&2
            exit 2
            ;;
    esac
done

if [ -z "${TARGET}" ]; then
    TARGET="clean"
    echo "Notice: No clean target specified. Defaulting to 'clean'."
    echo "        Use 'clean-hard' or 'clean-hard-all' for deeper cleanup."
fi

clean_binaries_and_build() {
    echo "==> Cleaning binaries and build artifacts..."
    rm -rf bin/ dist/ coverage.out coverage*.out benchmark-results.json benchmark-*.json
    rm -f compute computed compute-worker compute-bench
    rm -f *.test
    rm -rf web/dist web/tsconfig.tsbuildinfo *.tsbuildinfo
    echo "  [OK] Binaries, test coverage, and build output removed."
}

stop_processes() {
    echo "==> Stopping running containers and processes..."
    if command -v docker >/dev/null 2>&1; then
        if [ -f docker-compose.yml ]; then
            echo "  Stopping Docker Compose stack..."
            if [ "${TARGET}" = "clean-hard-all" ]; then
                docker compose --profile gui --profile worker --profile dev down -v --remove-orphans --rmi local 2>/dev/null || true
            else
                docker compose --profile gui --profile worker --profile dev down -v --remove-orphans 2>/dev/null || true
            fi
        fi
    fi

    for proc in computed compute-worker firecracker jailer; do
        pids=$(pgrep -f "(^|/)${proc}(\s|$)" 2>/dev/null || true)
        if [ -n "${pids}" ]; then
            echo "  Stopping process ${proc} (PIDs: ${pids})..."
            kill ${pids} 2>/dev/null || true
            sleep 0.5
            for pid in ${pids}; do
                if kill -0 "${pid}" 2>/dev/null; then
                    kill -9 "${pid}" 2>/dev/null || true
                fi
            done
        fi
    done
    echo "  [OK] All Compute services and processes stopped."
}

clean_network() {
    echo "==> Cleaning TAP network devices and sandbox bridge..."
    if command -v ip >/dev/null 2>&1; then
        for tap in $(ip -o link show 2>/dev/null | awk -F': ' '{print $2}' | grep -E '^mftap-|^fc-tap' || true); do
            echo "  Deleting TAP device: ${tap}"
            if ! ip link delete "${tap}" 2>/dev/null; then
                if [ -n "${SUDO}" ]; then
                    ${SUDO} ip link delete "${tap}" 2>/dev/null || true
                fi
            fi
        done

        if ip link show "${BRIDGE_NAME}" >/dev/null 2>&1; then
            echo "  Removing bridge: ${BRIDGE_NAME}"
            if ! (ip link set "${BRIDGE_NAME}" down && ip link delete "${BRIDGE_NAME}" type bridge) 2>/dev/null; then
                if [ -n "${SUDO}" ]; then
                    ${SUDO} ip link set "${BRIDGE_NAME}" down 2>/dev/null || true
                    ${SUDO} ip link delete "${BRIDGE_NAME}" type bridge 2>/dev/null || true
                fi
            fi
        fi
    fi

    if command -v iptables >/dev/null 2>&1; then
        IPT_CMD=""
        if [ "$(id -u)" -eq 0 ]; then
            IPT_CMD="iptables"
        elif [ -n "${SUDO}" ]; then
            IPT_CMD="${SUDO} iptables"
        fi

        if [ -n "${IPT_CMD}" ]; then
            while ${IPT_CMD} -D FORWARD -i "${BRIDGE_NAME}" -j ACCEPT 2>/dev/null; do :; done
            while ${IPT_CMD} -D FORWARD -o "${BRIDGE_NAME}" -j ACCEPT 2>/dev/null; do :; done
            SUBNET="${COMPUTE_SUBNET:-172.16.0.0/16}"
            while ${IPT_CMD} -t nat -D POSTROUTING -s "${SUBNET}" ! -o "${BRIDGE_NAME}" -j MASQUERADE 2>/dev/null; do :; done
        fi
    fi
    echo "  [OK] Network devices and firewall rules cleaned."
}

clean_runtime_state() {
    echo "==> Cleaning runtime VM state, databases, disks, sockets, and logs..."
    safe_rm vms/ snapshots/ disks/ srv-jailer/
    safe_rm *.sock *.pid *.tmp *.log
    safe_rm *.db *.db-shm *.db-wal

    if [ -d "${DATA_DIR}" ]; then
        safe_rm "${DATA_DIR}/vms"
        safe_rm "${DATA_DIR}/snapshots"
        safe_rm "${DATA_DIR}/disks"
        safe_rm "${DATA_DIR}/data"
        safe_rm "${DATA_DIR}/logs"
        safe_rm "${DATA_DIR}/cache"
        safe_rm "${DATA_DIR}"/*.db "${DATA_DIR}"/*.db-shm "${DATA_DIR}"/*.db-wal
    fi

    # Clean partial build artifacts inside images directory if any
    if [ -d "${IMAGES_DIR}" ]; then
        safe_rm "${IMAGES_DIR}"/.*.part.* "${IMAGES_DIR}"/*.part "${IMAGES_DIR}"/*.tmp
    fi

    # Clean temporary directories created in /tmp
    echo "==> Cleaning temporary build and test directories in /tmp..."
    safe_rm /tmp/compute-* /tmp/fc-res-* /tmp/j-res-* /tmp/compute-sandboxes 2>/dev/null || true

    # Clean Compute cgroups if present
    if [ -d "/sys/fs/cgroup/compute" ]; then
        if ! rmdir /sys/fs/cgroup/compute 2>/dev/null; then
            if [ -n "${SUDO}" ]; then
                ${SUDO} rmdir /sys/fs/cgroup/compute 2>/dev/null || true
            fi
        fi
    fi

    echo "  [OK] Runtime state, databases, disks, and temporary files cleaned."
}

echo "==================================================================="
echo "  Compute Project Cleanup: [${TARGET}]"
echo "==================================================================="

case "${TARGET}" in
    clean)
        clean_binaries_and_build
        ;;

    clean-hard)
        stop_processes
        clean_network
        clean_binaries_and_build
        clean_runtime_state
        echo ""
        echo "==> [PRESERVED] Base images & rootfs preserved in ${IMAGES_DIR}:"
        if [ -d "${IMAGES_DIR}" ]; then
            ls -lh "${IMAGES_DIR}" 2>/dev/null || true
        else
            echo "  (No base images directory currently present)"
        fi
        ;;

    clean-hard-all)
        stop_processes
        clean_network
        clean_binaries_and_build
        clean_runtime_state
        echo "==> Cleaning base images, rootfs builds, kernels, and SSH keys..."
        safe_rm "${IMAGES_DIR}"
        safe_rm "${DATA_DIR}"
        echo "  [OK] Everything cleaned including base images, rootfs, and data directory."
        ;;
esac

echo "==================================================================="
echo "  Cleanup [${TARGET}] complete!"
echo "==================================================================="
