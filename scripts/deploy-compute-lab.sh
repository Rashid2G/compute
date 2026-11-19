#!/usr/bin/env bash
# Deploy the current worktree/checkout to compute-lab.
#
# Syncs current codebase changes to the compute-lab deployment host,
# rebuilds the daemon container, updates the host CLI binary, and
# verifies daemon readiness and health.
#
# Usage:
#   ./scripts/deploy-compute-lab.sh [options]
#   make deploy-lab
#
# Options:
#   --host <ssh-host>       Remote SSH host/alias (default: compute-lab or $COMPUTE_LAB_HOST)
#   --remote-dir <path>     Remote project directory (default: ~/compute or $COMPUTE_LAB_DIR)
#   --skip-build            Skip container rebuild (only sync files)
#   --skip-cli              Skip updating host /usr/local/bin/compute
#   --dry-run               Show what would be transferred without making changes
#   -h, --help              Show this help message

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# Configuration with defaults
REMOTE_HOST="${COMPUTE_LAB_HOST:-compute-lab}"
REMOTE_DIR="${COMPUTE_LAB_DIR:-~/compute}"
SKIP_BUILD=0
SKIP_CLI=0
DRY_RUN=0

# Parse arguments
while [ $# -gt 0 ]; do
    case "$1" in
        --host)
            REMOTE_HOST="$2"
            shift 2
            ;;
        --remote-dir)
            REMOTE_DIR="$2"
            shift 2
            ;;
        --skip-build)
            SKIP_BUILD=1
            shift
            ;;
        --skip-cli)
            SKIP_CLI=1
            shift
            ;;
        --dry-run)
            DRY_RUN=1
            shift
            ;;
        -h|--help)
            sed -n '2,17p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *)
            echo "Unknown option: $1 (run with --help for usage)" >&2
            exit 1
            ;;
    esac
done

# Colors
C_RESET="\033[0m"
C_BOLD="\033[1m"
C_BLUE="\033[34m"
C_GREEN="\033[32m"
C_YELLOW="\033[33m"
C_RED="\033[31m"
C_CYAN="\033[36m"

say() {
    echo -e "${C_BLUE}${C_BOLD}==>${C_RESET} ${C_BOLD}$*${C_RESET}"
}

say_ok() {
    echo -e "${C_GREEN}${C_BOLD}[OK]${C_RESET} $*"
}

say_warn() {
    echo -e "${C_YELLOW}${C_BOLD}[WARN]${C_RESET} $*"
}

say_fail() {
    echo -e "${C_RED}${C_BOLD}[ERROR]${C_RESET} $*" >&2
    exit 1
}

echo "==================================================================="
echo -e "${C_CYAN}${C_BOLD}  Compute Lab — Worktree Deployment${C_RESET}"
echo "==================================================================="
echo "  Source Worktree: ${ROOT}"
echo "  Target Host:     ${REMOTE_HOST}"
echo "  Target Dir:      ${REMOTE_DIR}"
if [ "${DRY_RUN}" -eq 1 ]; then
    echo -e "  Mode:            ${C_YELLOW}DRY RUN${C_RESET}"
fi
echo "==================================================================="

# 1. Verify SSH connectivity
say "[1/5] Checking connection to ${REMOTE_HOST}..."
if ! ssh -o BatchMode=yes -o ConnectTimeout=5 "${REMOTE_HOST}" true 2>/dev/null; then
    say_fail "Cannot connect to ${REMOTE_HOST} over SSH. Check ~/.ssh/config and host reachability."
fi
say_ok "SSH connection to ${REMOTE_HOST} established."

# 2. Sync worktree files via rsync
say "[2/5] Syncing worktree files to ${REMOTE_HOST}:${REMOTE_DIR}..."
RSYNC_FLAGS=(
    -avz
    --delete
    --exclude='.git'
    --exclude='data/'
    --exclude='bin/'
    --exclude='dist/'
    --exclude='coverage.out'
    --exclude='*.tmp'
    --exclude='node_modules/'
    --exclude='web/node_modules/'
    --exclude='web/dist/'
    --exclude='.vscode/'
)

if [ "${DRY_RUN}" -eq 1 ]; then
    RSYNC_FLAGS+=(--dry-run)
fi

rsync "${RSYNC_FLAGS[@]}" "${ROOT}/" "${REMOTE_HOST}:${REMOTE_DIR}/"
say_ok "Worktree synced successfully."

if [ "${DRY_RUN}" -eq 1 ]; then
    say "Dry run complete. No remote commands executed."
    exit 0
fi

# 3. Rebuild and restart computed daemon container
if [ "${SKIP_BUILD}" -eq 0 ]; then
    say "[3/5] Rebuilding and restarting computed container on ${REMOTE_HOST}..."
    ssh "${REMOTE_HOST}" "cd ${REMOTE_DIR} && docker compose up -d --build computed"
    say_ok "Container rebuilt and started."
else
    say "[3/5] Skipping container rebuild (--skip-build requested)."
fi

# 4. Update host CLI binary from the container
if [ "${SKIP_CLI}" -eq 0 ] && [ "${SKIP_BUILD}" -eq 0 ]; then
    say "[4/5] Updating host /usr/local/bin/compute CLI binary..."
    ssh "${REMOTE_HOST}" "
        cd ${REMOTE_DIR} &&
        docker compose cp computed:/usr/local/bin/compute /tmp/compute-bin &&
        sudo mv /tmp/compute-bin /usr/local/bin/compute &&
        sudo chmod +x /usr/local/bin/compute
    "
    say_ok "Host CLI binary updated."
else
    say "[4/5] Skipping CLI binary update."
fi

# 5. Wait for readiness and run diagnostics
say "[5/5] Verifying daemon readiness on ${REMOTE_HOST}..."

WAIT_MAX=60
READY=0
for ((i=1; i<=WAIT_MAX; i++)); do
    if ssh "${REMOTE_HOST}" "curl -fsS http://127.0.0.1:18080/api/v1/ready >/dev/null 2>&1"; then
        READY=1
        break
    fi
    sleep 1
done

if [ "${READY}" -ne 1 ]; then
    say_fail "Daemon did not report ready within ${WAIT_MAX} seconds. Check logs with: ssh ${REMOTE_HOST} 'docker compose -f ${REMOTE_DIR}/docker-compose.yml logs computed'"
fi
say_ok "Daemon is ready!"

# Fetch remote host IP and version info
REMOTE_IP="$(ssh "${REMOTE_HOST}" "hostname -I 2>/dev/null | awk '{print \$1}'" || echo "${REMOTE_HOST}")"
VERSION_INFO="$(ssh "${REMOTE_HOST}" "compute version 2>/dev/null" || echo "Compute Daemon")"

echo ""
echo "==================================================================="
echo -e "${C_GREEN}${C_BOLD}  Deployment to ${REMOTE_HOST} Succeeded!${C_RESET}"
echo "==================================================================="
echo "  Dashboard URL:   http://${REMOTE_IP}:18080/"
echo "  REST API:        http://${REMOTE_IP}:18080/api/v1"
echo "  CLI Access:      ssh ${REMOTE_HOST} 'compute sandbox list'"
echo "  Daemon Version:  ${VERSION_INFO}"
echo "==================================================================="
