#!/usr/bin/env bash
# scripts/deploy.sh
# Automated build, upload, and deployment for on-demand-vms (compute-lab VM).
# Default deployment method: Docker Compose (fast, containerized, out-of-the-box).
# Legacy systemd deployment available via --systemd flag.

set -euo pipefail

TARGET_HOST="${COMPUTE_HOST:-${LAB_HOST:-${T3_RUNNER_HOST:-compute-lab}}}"
TARGET_DIR="${COMPUTE_DIR:-/home/rashid/on-demand-vms}"
BUILD_LOCAL=true
BUILD_UI=false
RESTART_ONLY=false
CHECK_STATUS_ONLY=false
DEPLOY_MODE="docker" # Default deployment method is docker compose

# ANSI colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BOLD='\033[1m'
NC='\033[0m' # No Color

usage() {
    cat <<EOF
Usage: $(basename "$0") [options] [target-host]

Deploy on-demand-vms changes to the remote VM (default: compute-lab),
upload updated sources/configs, launch services via Docker Compose, and verify health.

Options:
  --ui             Build Web GUI frontend before compiling Go binaries
  --skip-build     Skip building binaries locally, only sync and restart
  --restart-only   Only restart services on remote and verify health (no build/sync)
  --status         Check current status and recent logs of remote services
  --docker         Use Docker Compose deployment method (default)
  --systemd        Use legacy host systemd units deployment method
  --host <host>    Specify target SSH host (default: compute-lab)
  --dir <dir>      Specify target working directory (default: /home/rashid/on-demand-vms)
  -h, --help       Show this help message

Environment Variables:
  COMPUTE_HOST     Override default target SSH host (alias: LAB_HOST)
  COMPUTE_DIR      Override default target directory

Examples:
  ./scripts/deploy.sh             # Fast Docker Compose deployment to compute-lab
  ./scripts/deploy.sh --ui        # Build UI + deploy via Docker Compose
  ./scripts/deploy.sh --status    # Check remote container and service health
  ./scripts/deploy.sh --systemd   # Deploy via host systemd units
  make deploy                     # Standard workflow for developers
EOF
    exit 0
}

# Parse flags
while [[ $# -gt 0 ]]; do
    case "$1" in
        --ui)
            BUILD_UI=true
            shift
            ;;
        --skip-build)
            BUILD_LOCAL=false
            shift
            ;;
        --restart-only)
            RESTART_ONLY=true
            BUILD_LOCAL=false
            shift
            ;;
        --status)
            CHECK_STATUS_ONLY=true
            shift
            ;;
        --docker)
            DEPLOY_MODE="docker"
            shift
            ;;
        --systemd)
            DEPLOY_MODE="systemd"
            shift
            ;;
        --host)
            TARGET_HOST="$2"
            shift 2
            ;;
        --dir)
            TARGET_DIR="$2"
            shift 2
            ;;
        -h|--help)
            usage
            ;;
        *)
            TARGET_HOST="$1"
            shift
            ;;
    esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

check_ssh() {
    printf "${BLUE}==>${NC} ${BOLD}Checking SSH connectivity to %s...${NC}\n" "${TARGET_HOST}"
    if ! ssh -o BatchMode=yes -o ConnectTimeout=8 "${TARGET_HOST}" "true" 2>/dev/null; then
        printf "${RED}Error: Cannot reach '%s' via SSH.${NC}\n" "${TARGET_HOST}"
        printf "Ensure your ~/.ssh/config contains '%s' and your SSH keys are configured.\n" "${TARGET_HOST}"
        exit 1
    fi
    printf "${GREEN}✓${NC} SSH connection to %s confirmed.\n" "${TARGET_HOST}"
}

if [ "${CHECK_STATUS_ONLY}" = true ]; then
    check_ssh
    printf "\n${BLUE}==>${NC} ${BOLD}Checking remote status on %s...${NC}\n" "${TARGET_HOST}"
    ssh -o BatchMode=yes "${TARGET_HOST}" bash -c "'
        echo \"=== Docker Containers ===\"
        if command -v docker >/dev/null 2>&1; then
            sudo docker ps -a --filter \"name=compute\" --format \"table {{.Names}}\t{{.Status}}\t{{.Ports}}\" || true
        fi
        echo \"\"
        echo \"=== Systemd Units ===\"
        systemctl status computed.service compute-gui.service --no-pager 2>/dev/null || true
        echo \"\"
        echo \"=== API Health Check ===\"
        curl -s http://127.0.0.1:18080/api/v1/health || echo \"API Unreachable on :18080\"
        echo \"\"
        echo \"=== Web GUI Check ===\"
        curl -s -I http://127.0.0.1:8080 2>/dev/null | head -n 1 || echo \"GUI Unreachable on :8080\"
        echo \"\"
        echo \"=== Recent Container / Service Logs ===\"
        if sudo docker ps -q --filter \"name=computed\" | grep -q .; then
            echo \"--- Docker Logs: computed ---\"
            sudo docker logs --tail 15 computed || true
        else
            echo \"--- Systemd Journal: computed ---\"
            sudo journalctl -u computed.service -n 15 --no-pager 2>/dev/null || true
        fi
    '"
    exit 0
fi

# Step 1: Build locally (if requested/enabled)
if [ "${BUILD_LOCAL}" = true ]; then
    if [ "${BUILD_UI}" = true ]; then
        printf "${BLUE}==>${NC} ${BOLD}Building Web UI frontend...${NC}\n"
        (cd "${REPO_ROOT}/web" && (npm run build || bun run build))
    fi

    VERSION="${VERSION:-2.0.0}"
    RELEASE_NAME="${RELEASE_NAME:-Vulcan}"
    GIT_COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo "dev")"
    BUILD_DATE="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
    PKG_VERSION="github.com/Rashid2G/on-demand-vms/pkg/version"
    LDFLAGS="-s -w -X ${PKG_VERSION}.Version=${VERSION} -X ${PKG_VERSION}.ReleaseName=${RELEASE_NAME} -X ${PKG_VERSION}.GitCommit=${GIT_COMMIT} -X ${PKG_VERSION}.BuildDate=${BUILD_DATE}"

    printf "${BLUE}==>${NC} ${BOLD}Compiling Go binaries for linux/amd64...${NC}\n"
    mkdir -p "${REPO_ROOT}/bin"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="${LDFLAGS}" -o "${REPO_ROOT}/bin/compute" ./cmd/compute
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="${LDFLAGS}" -o "${REPO_ROOT}/bin/computed" ./cmd/computed
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="${LDFLAGS}" -o "${REPO_ROOT}/bin/compute-worker" ./cmd/compute-worker
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="${LDFLAGS}" -o "${REPO_ROOT}/bin/compute-bench" ./cmd/compute-bench
    printf "${GREEN}✓${NC} Successfully built binaries in bin/\n"
fi

# Check connectivity
check_ssh

# Step 2: Upload changes
if [ "${RESTART_ONLY}" = false ]; then
    printf "${BLUE}==>${NC} ${BOLD}Ensuring remote directories exist on %s...${NC}\n" "${TARGET_HOST}"
    ssh -o BatchMode=yes "${TARGET_HOST}" "mkdir -p '${TARGET_DIR}'"

    printf "${BLUE}==>${NC} ${BOLD}Synchronizing files to %s:%s...${NC}\n" "${TARGET_HOST}" "${TARGET_DIR}"
    if command -v rsync >/dev/null 2>&1; then
        rsync -avz --delete-after \
            --exclude='.git' \
            --exclude='node_modules' \
            --exclude='.cache' \
            --exclude='data/' \
            --exclude='coverage.out' \
            --exclude='benchmark-results.json' \
            --exclude='tmp/' \
            "${REPO_ROOT}/" "${TARGET_HOST}:${TARGET_DIR}/"
    else
        printf "${YELLOW}rsync not found locally, falling back to scp...${NC}\n"
        scp -r "${REPO_ROOT}/bin" "${REPO_ROOT}/config" "${REPO_ROOT}/scripts" "${REPO_ROOT}/docker-compose.yml" "${REPO_ROOT}/Dockerfile.daemon" "${REPO_ROOT}/Dockerfile.worker" "${TARGET_HOST}:${TARGET_DIR}/"
    fi

    # Ensure executable permissions on remote binaries and scripts
    ssh -o BatchMode=yes "${TARGET_HOST}" "chmod +x '${TARGET_DIR}'/bin/* '${TARGET_DIR}'/scripts/* 2>/dev/null || true"
    printf "${GREEN}✓${NC} Files synchronized successfully.\n"
fi

# Step 3: Launch / Restart services using selected deployment method
if [ "${DEPLOY_MODE}" = "docker" ]; then
    printf "${BLUE}==>${NC} ${BOLD}Deploying via Docker Compose on %s...${NC}\n" "${TARGET_HOST}"

    # Ensure Docker and Docker Compose are available on remote host
    ssh -o BatchMode=yes "${TARGET_HOST}" bash -c "'
        if ! command -v docker >/dev/null 2>&1; then
            echo \"Docker is required for Docker Compose deployment. Installing docker.io...\"
            sudo apt-get update && sudo apt-get install -y docker.io docker-compose
        elif ! command -v docker-compose >/dev/null 2>&1 && ! docker compose version >/dev/null 2>&1; then
            echo \"Installing docker-compose...\"
            sudo apt-get update && sudo apt-get install -y docker-compose || true
        fi
        # Stop conflicting systemd services if running
        sudo systemctl stop computed.service compute-gui.service 2>/dev/null || true
        sudo systemctl disable computed.service compute-gui.service 2>/dev/null || true
    '"

    printf "${BLUE}==>${NC} ${BOLD}Starting containers with Docker Compose...${NC}\n"
    ssh -o BatchMode=yes "${TARGET_HOST}" bash -c "'
        cd \"${TARGET_DIR}\"
        mkdir -p data
        if docker compose version >/dev/null 2>&1; then
            sudo docker compose up -d --build
        else
            sudo docker-compose up -d --build
        fi
    '"
    printf "${GREEN}✓${NC} Docker Compose deployment launched.\n"

else
    # Legacy systemd deployment
    printf "${BLUE}==>${NC} ${BOLD}Deploying via Systemd Units on %s...${NC}\n" "${TARGET_HOST}"
    ssh -o BatchMode=yes "${TARGET_HOST}" "sudo install -m 0644 '${TARGET_DIR}/config/systemd/computed.service' /etc/systemd/system/computed.service && sudo install -m 0644 '${TARGET_DIR}/config/systemd/compute-gui.service' /etc/systemd/system/compute-gui.service && sudo install -m 0644 '${TARGET_DIR}/config/systemd/compute-net.service' /etc/systemd/system/compute-net.service && sudo systemctl daemon-reload && sudo systemctl enable computed.service compute-gui.service compute-net.service >/dev/null"
    ssh -o BatchMode=yes "${TARGET_HOST}" "sudo systemctl start compute-net.service"
    ssh -o BatchMode=yes "${TARGET_HOST}" "sudo systemctl restart computed.service compute-gui.service"
    printf "${GREEN}✓${NC} Systemd services restarted.\n"
fi

# Step 4: Health Verification
printf "${BLUE}==>${NC} ${BOLD}Verifying service health (up to 20s)...${NC}\n"
HEALTHY=false
for i in $(seq 1 20); do
    STATUS_OUT=$(ssh -o BatchMode=yes "${TARGET_HOST}" bash -c "'
        DAEMON_OK=false
        GUI_OK=false
        if curl -s -f http://127.0.0.1:18080/api/v1/health >/dev/null 2>&1; then
            DAEMON_OK=true
        fi
        if curl -s -f -I http://127.0.0.1:8080 >/dev/null 2>&1; then
            GUI_OK=true
        fi
        echo \"daemon:\$DAEMON_OK gui:\$GUI_OK\"
    '" 2>/dev/null || echo "daemon:false gui:false")

    if [[ "$STATUS_OUT" =~ "daemon:true gui:true" ]] || [[ "$STATUS_OUT" =~ "daemon:true" ]]; then
        HEALTHY=true
        break
    fi
    sleep 1
done

if [ "$HEALTHY" = true ]; then
    printf "${GREEN}✓ All services are ACTIVE and HEALTHY!${NC}\n\n"

    # Fetch remote host IP for convenient links
    REMOTE_IP=$(ssh -o BatchMode=yes "${TARGET_HOST}" "hostname -I 2>/dev/null | awk '{print \$1}'" || true)
    if [ -z "${REMOTE_IP}" ]; then
        REMOTE_IP="${TARGET_HOST}"
    fi

    printf "${BOLD}Service Endpoints:${NC}\n"
    printf "  • Compute Daemon API: ${BLUE}http://%s:18080/api/v1/health${NC}\n" "${REMOTE_IP}"
    printf "  • Compute Web GUI:    ${BLUE}http://%s:8080${NC}\n" "${REMOTE_IP}"
    printf "  • Deployment Method:  ${BOLD}%s${NC}\n" "${DEPLOY_MODE}"
    printf "  • Target Working Dir: ${TARGET_DIR}\n"
    printf "\n${GREEN}Deployment complete! Your changes are live on %s.${NC}\n" "${TARGET_HOST}"
else
    printf "${RED}Warning: Health check did not pass within 20 seconds.${NC}\n"
    printf "Checking logs from remote VM:\n"
    if [ "${DEPLOY_MODE}" = "docker" ]; then
        ssh -o BatchMode=yes "${TARGET_HOST}" "cd '${TARGET_DIR}' && (sudo docker compose logs --tail 25 || sudo docker-compose logs --tail 25)"
    else
        ssh -o BatchMode=yes "${TARGET_HOST}" "sudo journalctl -u computed.service -u compute-gui.service -n 25 --no-pager"
    fi
    exit 1
fi
