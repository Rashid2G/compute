#!/usr/bin/env bash
# scripts/preview-docker.sh
#
# Deploy a PR or branch preview as its own Docker Compose stack on the SAME
# Docker daemon and host as the dev deployment (compute-lab). No microVM
# sandbox is created inside the dev deployment; the preview runs the PR's own
# `computed` binary (which serves the PR's Web UI at "/" plus its daemon API)
# in a container configured so multiple stacks coexist:
#
#   - unique host port          (19000-19099, stable per name, collision-checked)
#   - unique bridge + subnet    (mft3brp<slot>, 172.<100+slot>.0.0/16)
#   - own data dir              (~/on-demand-vms/preview-data/<name>/)
#   - shared rootfs image cache (~/on-demand-vms/data/images)
#   - DNS responder and HTTP edge disabled (no :53 / :80 conflicts)
#   - same auth credentials as the dev deployment (lab .env)
#
# Each deploy writes a manifest to ~/on-demand-vms/data/previews/<name>.json
# which the daemon's deployments homepage (http://compute-lab:18080/) lists so
# reviewers can pick any deployment.
#
# Usage:
#   ./scripts/preview-docker.sh <name>              deploy/refresh the preview
#   ./scripts/preview-docker.sh <name> --stop       tear it down
#   ./scripts/preview-docker.sh <name> --status     show stack status
#   ./scripts/preview-docker.sh <name> --logs       follow the daemon logs
#
# Environment:
#   COMPUTE_HOST          Target SSH host (default: compute-lab)
#   COMPUTE_DIR           Deployed checkout on the target (default: /home/rashid/on-demand-vms)
#   PREVIEW_BASE          Git base ref for the change diff (default: origin/dev)
#   COMPUTE_WORKER_TOKEN  Start the PR's worker in the preview when set

set -euo pipefail

TARGET_HOST="${COMPUTE_HOST:-compute-lab}"
TARGET_DIR="${COMPUTE_DIR:-/home/rashid/on-demand-vms}"
PREVIEW_DATA_ROOT="${TARGET_DIR}/preview-data"
MANIFEST_DIR="${TARGET_DIR}/data/previews"
PREVIEW_BASE="${PREVIEW_BASE:-origin/dev}"

NAME="${1:-}"
shift || true
ACTION="deploy"
for arg in "$@"; do
    case "${arg}" in
        --stop) ACTION="stop" ;;
        --status) ACTION="status" ;;
        --logs) ACTION="logs" ;;
    esac
done

if [[ -z "${NAME}" ]]; then
    echo "Usage: $(basename "$0") <name> [--stop|--status|--logs]" >&2
    exit 1
fi
NAME="$(echo "${NAME}" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/-/g; s/^-+|-+$//g' | cut -c1-63)"
PROJECT="preview-${NAME}"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*" >&2; }
success() { printf '\033[1;32m[✓]\033[0m %s\n' "$*"; }

stop_stack() {
    log "Stopping preview stack '${PROJECT}' on ${TARGET_HOST}..."
    ssh -o BatchMode=yes "${TARGET_HOST}" "
        if [ -f '${PREVIEW_DATA_ROOT}/${NAME}/compose.yml' ]; then
            sudo docker compose -p '${PROJECT}' -f '${PREVIEW_DATA_ROOT}/${NAME}/compose.yml' down --remove-orphans 2>/dev/null || true
        fi
        sudo docker rm -f '${PROJECT}-computed-1' '${PROJECT}-compute-worker-1' 2>/dev/null || true
        sudo rm -rf '${PREVIEW_DATA_ROOT}/${NAME}'
        rm -f '${MANIFEST_DIR}/${NAME}.json'
    "
    success "Preview '${NAME}' removed."
}

show_status() {
    ssh -o BatchMode=yes "${TARGET_HOST}" "
        if [ -f '${MANIFEST_DIR}/${NAME}.json' ]; then cat '${MANIFEST_DIR}/${NAME}.json'; echo; fi
        sudo docker ps -a --filter name='${PROJECT}' --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}' 2>/dev/null || true
    "
    exit 0
}

show_logs() {
    exec ssh -o BatchMode=yes "${TARGET_HOST}" "cd '${TARGET_DIR}' && sudo docker compose -p '${PROJECT}' -f '${PREVIEW_DATA_ROOT}/${NAME}/compose.yml' logs -f computed"
}

case "${ACTION}" in
    stop) stop_stack; exit 0 ;;
    status) show_status ;;
    logs) show_logs ;;
esac

# ---------------------------------------------------------------------------
# 1. Detect which services changed vs the base ref
# ---------------------------------------------------------------------------
cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"

BASE_COMMIT=""
if git rev-parse --verify --quiet "${PREVIEW_BASE}^{commit}" >/dev/null 2>&1; then
    BASE_COMMIT="$(git merge-base "${PREVIEW_BASE}" HEAD)"
fi

NEED_DAEMON=0 NEED_WORKER=0 WEB_CHANGED=0
if [[ -n "${BASE_COMMIT}" ]]; then
    CHANGED_FILES="$(
        {
            git diff --name-only "${BASE_COMMIT}" HEAD
            git diff --name-only HEAD
            git ls-files --others --exclude-standard
        } | sort -u
    )"
    while IFS= read -r f; do
        [[ -z "${f}" ]] && continue
        case "${f}" in
            web/*|internal/gui/*|cmd/compute/*)      WEB_CHANGED=1 ;;
            cmd/compute-worker/*)                    NEED_WORKER=1 ;;
            cmd/computed/*|internal/*|pkg/*|go.mod|go.sum|docker-compose.yml|config/*|*.go)
                                                     NEED_DAEMON=1 ;;
        esac
    done <<< "${CHANGED_FILES}"
fi
if [[ ! -f "internal/gui/assets/index.html" ]] || [[ ! -f "internal/gui/assets/app.js" ]]; then
    WEB_CHANGED=1
fi
# The preview front door is the PR's computed binary (embedded GUI + API);
# always rebuild it so the preview reflects the branch.
NEED_DAEMON=1

log "Preview '${NAME}': daemon build required; worker: ${NEED_WORKER}; web rebuild: ${WEB_CHANGED}"

# ---------------------------------------------------------------------------
# 2. Build the PR's linux binaries (embedded GUI included)
# ---------------------------------------------------------------------------
BUILD_DIR="bin/preview/${NAME}"
mkdir -p "${BUILD_DIR}"

if [[ "${WEB_CHANGED}" -eq 1 ]]; then
    log "Building web UI assets (web/ changed)..."
    if command -v npm >/dev/null 2>&1; then
        (cd web && { [[ -x node_modules/.bin/tsc ]] || npm ci --silent; } && npm run build)
    elif command -v bun >/dev/null 2>&1; then
        (cd web && { [[ -d node_modules ]] || bun install --frozen-lockfile; } && bun run build)
    else
        warn "Neither npm nor bun is installed; falling back to committed assets in internal/gui/assets."
    fi
fi

build_bin() {
    local name="$1" dir="$2"
    log "Building ${name} (linux/amd64)..."
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "${BUILD_DIR}/${name}" "./${dir}"
}

build_bin computed cmd/computed
if [[ "${NEED_WORKER}" -eq 1 ]]; then
    build_bin compute-worker cmd/compute-worker
fi

# ---------------------------------------------------------------------------
# 3. Pick a stable, free slot: port 19000-19099, bridge mft3brp<slot>,
#    subnet 172.<100+slot>.0.0/16
# ---------------------------------------------------------------------------
USED_PORTS_FILE="$(mktemp)"
OTHER_CLAIMED_PORTS_FILE="$(mktemp)"
trap 'rm -f "${USED_PORTS_FILE}" "${OTHER_CLAIMED_PORTS_FILE}"' EXIT

EXISTING_PORT="$(ssh -o BatchMode=yes "${TARGET_HOST}" "
    if [ -f '${MANIFEST_DIR}/${NAME}.json' ]; then
        grep -oE '\"port\"[[:space:]]*:[[:space:]]*[0-9]+' '${MANIFEST_DIR}/${NAME}.json' | grep -oE '[0-9]+' | head -n1 || true
    fi
" | tr -d '\r')"

ssh -o BatchMode=yes "${TARGET_HOST}" "cat /proc/net/tcp{,6} 2>/dev/null | awk 'NR>1 {print \$2}' | cut -d: -f2 | sort -u" > "${USED_PORTS_FILE}" || true
ssh -o BatchMode=yes "${TARGET_HOST}" "
    for f in '${MANIFEST_DIR}'/*.json; do
        [ -f \"\$f\" ] || continue
        [ \"\$(basename \"\$f\")\" = '${NAME}.json' ] && continue
        grep -oE '\"port\"[[:space:]]*:[[:space:]]*[0-9]+' \"\$f\" | grep -oE '[0-9]+' || true
    done
" | tr -d '\r' > "${OTHER_CLAIMED_PORTS_FILE}" || true

SLOT=""
PORT=""
SUBNET=""

if [[ "${EXISTING_PORT}" =~ ^[0-9]+$ ]] && (( EXISTING_PORT >= 19000 && EXISTING_PORT <= 19099 )); then
    if ! grep -qx "${EXISTING_PORT}" "${OTHER_CLAIMED_PORTS_FILE}" 2>/dev/null; then
        SLOT=$(( EXISTING_PORT - 19000 ))
        PORT="${EXISTING_PORT}"
        SUBNET="172.$((100 + SLOT)).0.0/16"
    fi
fi

if [[ -z "${PORT}" ]]; then
    HASH=$(( $(echo "${NAME}" | cksum | awk '{print $1}') % 100 ))
    for i in $(seq 0 99); do
        slot=$(( (HASH + i) % 100 ))
        port=$(( 19000 + slot ))
        if grep -qi "$(printf '%04X' "${port}")" "${USED_PORTS_FILE}" 2>/dev/null; then
            continue
        fi
        if grep -qx "${port}" "${OTHER_CLAIMED_PORTS_FILE}" 2>/dev/null; then
            continue
        fi
        SLOT="${slot}"
        PORT="${port}"
        SUBNET="172.$((100 + slot)).0.0/16"
        break
    done
fi
if [[ -z "${PORT}" ]]; then
    echo "Error: no free preview slot (19000-19099 all in use)." >&2
    exit 1
fi
log "Slot ${SLOT}: port ${PORT}, bridge mft3brp${SLOT}, subnet ${SUBNET} (gateway ${SUBNET%0.0/16}0.1)"

# ---------------------------------------------------------------------------
# 4. Stage the binary + compose file on the lab host and start the stack
# ---------------------------------------------------------------------------
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
BRANCH="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"

log "Staging preview binaries on ${TARGET_HOST}..."
# Pre-create every bind-mount source; a missing host dir leaves the
# container's mount target broken (mkdir inside fails with ENOENT).
ssh -o BatchMode=yes "${TARGET_HOST}" "mkdir -p '${PREVIEW_DATA_ROOT}/${NAME}/bin' '${PREVIEW_DATA_ROOT}/${NAME}/data' '${PREVIEW_DATA_ROOT}/${NAME}/vms' '${MANIFEST_DIR}'"
scp -o BatchMode=yes "${BUILD_DIR}/computed" "${TARGET_HOST}:${PREVIEW_DATA_ROOT}/${NAME}/bin/" >/dev/null
if [[ "${NEED_WORKER}" -eq 1 && -f "${BUILD_DIR}/compute-worker" ]]; then
    scp -o BatchMode=yes "${BUILD_DIR}/compute-worker" "${TARGET_HOST}:${PREVIEW_DATA_ROOT}/${NAME}/bin/" >/dev/null
fi

DAEMON_IMAGE="$(ssh -o BatchMode=yes "${TARGET_HOST}" "set -a; . '${TARGET_DIR}/.env' 2>/dev/null; set +a; echo \${COMPUTE_DAEMON_IMAGE:-ghcr.io/rashid2g/compute-daemon}:\${COMPUTE_VERSION:-2.0.0}" | tr -d '\r')"
WORKER_IMAGE="$(ssh -o BatchMode=yes "${TARGET_HOST}" "set -a; . '${TARGET_DIR}/.env' 2>/dev/null; set +a; echo \${COMPUTE_WORKER_IMAGE:-ghcr.io/rashid2g/compute-worker}:\${COMPUTE_VERSION:-2.0.0}" | tr -d '\r')"

log "Writing compose file for '${PROJECT}' (image: ${DAEMON_IMAGE})..."
ssh -o BatchMode=yes "${TARGET_HOST}" "cat > '${PREVIEW_DATA_ROOT}/${NAME}/compose.yml'" <<EOF
name: ${PROJECT}

x-logging: &default-logging
  driver: json-file
  options:
    max-size: "10m"
    max-file: "3"

services:
  computed:
    image: ${DAEMON_IMAGE}
    restart: unless-stopped
    # Host networking matches the dev deployment: the sandbox bridge, TAP
    # devices and NAT live on the host; this stack's daemon uses its own
    # bridge (mft3brp${SLOT}) and subnet (${SUBNET}) so stacks do not collide.
    network_mode: host
    privileged: true
    cgroup: host
    stop_grace_period: 30s
    logging: *default-logging
    ulimits:
      nofile: 65536
    env_file:
      - ${TARGET_DIR}/.env
    environment:
      COMPUTE_HOST_NETWORK: "1"
      COMPUTE_BIND_ADDRESS: 0.0.0.0
      COMPUTE_API_PORT: "${PORT}"
      COMPUTE_DATA_DIR: /var/lib/compute-preview
      COMPUTE_DB_PATH: /var/lib/compute-preview/data/compute.db
      COMPUTE_IMAGES_DIR: /var/lib/compute-preview/images
      COMPUTE_VMS_DIR: /var/lib/compute-preview/vms
      COMPUTE_SSH_KEY_PATH: /var/lib/compute-preview/images/ssh/id_ed25519
      COMPUTE_BRIDGE_NAME: mft3brp${SLOT}
      COMPUTE_BRIDGE_IP: ${SUBNET%.0.0/16}.0.1/16
      COMPUTE_SUBNET: ${SUBNET}
      COMPUTE_GATEWAY_IP: ${SUBNET%.0.0/16}.0.1
      # Serve the PR's embedded Web UI directly at "/" on preview ports;
      # the main dev daemon (:18080) owns the Deployments homepage.
      COMPUTE_DEPLOYMENTS_INDEX: "false"
      # No DNS responder (:53) and no HTTP edge (:80) in preview stacks;
      # the dev daemon owns both. Previews are plain host:port deployments.
      COMPUTE_DNS_ENABLED: "false"
      COMPUTE_EDGE_LISTEN: "-"
      COMPUTE_DEFAULT_KERNEL: \${COMPUTE_DEFAULT_KERNEL:-vmlinux}
      COMPUTE_DEFAULT_ROOTFS: \${COMPUTE_DEFAULT_ROOTFS:-ubuntu-24.04.ext4}
    volumes:
      # The PR's binary over the image's daemon binary.
      - ${PREVIEW_DATA_ROOT}/${NAME}/bin/computed:/usr/local/bin/computed
      # Own data dir (db, per-PR guests); shared rootfs image cache.
      - ${PREVIEW_DATA_ROOT}/${NAME}/data:/var/lib/compute-preview/data
      - ${PREVIEW_DATA_ROOT}/${NAME}/vms:/var/lib/compute-preview/vms
      - ${TARGET_DIR}/data/images:/var/lib/compute-preview/images
      - /sys/fs/cgroup:/sys/fs/cgroup:rw
      - /dev/kvm:/dev/kvm
    devices:
      - /dev/kvm:/dev/kvm
    healthcheck:
      test: ["CMD", "curl", "-fsS", "http://127.0.0.1:${PORT}/api/v1/ready"]
      interval: 10s
      timeout: 5s
      retries: 6
      start_period: 20s
EOF

if [[ "${NEED_WORKER}" -eq 1 && -f "${BUILD_DIR}/compute-worker" ]]; then
    if [[ -n "${COMPUTE_WORKER_TOKEN:-}" ]]; then
        ssh -o BatchMode=yes "${TARGET_HOST}" "cat >> '${PREVIEW_DATA_ROOT}/${NAME}/compose.yml'" <<EOF

  compute-worker:
    image: ${WORKER_IMAGE}
    restart: unless-stopped
    network_mode: host
    privileged: true
    cgroup: host
    stop_grace_period: 30s
    logging: *default-logging
    env_file:
      - ${TARGET_DIR}/.env
    environment:
      COMPUTE_CONTROL_PLANE: http://127.0.0.1:${PORT}
      COMPUTE_WORKER_TOKEN: ${COMPUTE_WORKER_TOKEN}
      COMPUTE_WORKER_NAME: ${NAME}-worker
      COMPUTE_POOL_ID: default
      COMPUTE_WORKER_STATE_DIR: /var/lib/compute-worker
      COMPUTE_IMAGES_DIR: /var/lib/compute-preview/images
      COMPUTE_VMS_DIR: /var/lib/compute-worker/vms
      COMPUTE_SSH_KEY_PATH: /var/lib/compute-preview/images/ssh/id_ed25519
      COMPUTE_BRIDGE_NAME: mft3brp${SLOT}
    command: ["run"]
    volumes:
      - ${PREVIEW_DATA_ROOT}/${NAME}/bin/compute-worker:/usr/local/bin/compute-worker
      - ${TARGET_DIR}/data/images:/var/lib/compute-preview/images:ro
      - /sys/fs/cgroup:/sys/fs/cgroup:rw
      - /dev/kvm:/dev/kvm
    devices:
      - /dev/kvm:/dev/kvm
EOF
    else
        warn "compute-worker changed in this PR but COMPUTE_WORKER_TOKEN is not set; worker binary staged only."
    fi
fi

log "Starting preview stack '${PROJECT}'..."
ssh -o BatchMode=yes "${TARGET_HOST}" "cd '${TARGET_DIR}' && sudo docker compose -p '${PROJECT}' -f '${PREVIEW_DATA_ROOT}/${NAME}/compose.yml' up -d --wait" || {
    warn "Stack did not become healthy; logs:"
    ssh -o BatchMode=yes "${TARGET_HOST}" "cd '${TARGET_DIR}' && sudo docker compose -p '${PROJECT}' -f '${PREVIEW_DATA_ROOT}/${NAME}/compose.yml' logs --tail 30 computed 2>&1" || true
    exit 1
}

# ---------------------------------------------------------------------------
# 5. Health check + manifest for the deployments homepage
# ---------------------------------------------------------------------------
log "Health-checking http://127.0.0.1:${PORT}/api/v1/health..."
HEALTH=""
for _ in $(seq 1 30); do
    HEALTH="$(ssh -o BatchMode=yes "${TARGET_HOST}" "curl -sf --connect-timeout 2 http://127.0.0.1:${PORT}/api/v1/health 2>/dev/null || true")" && [[ -n "${HEALTH}" ]] && break
    sleep 2
done
if [[ -z "${HEALTH}" ]]; then
    warn "Preview daemon did not report healthy; logs:"
    ssh -o BatchMode=yes "${TARGET_HOST}" "cd '${TARGET_DIR}' && sudo docker compose -p '${PROJECT}' -f '${PREVIEW_DATA_ROOT}/${NAME}/compose.yml' logs --tail 30 computed 2>&1" || true
    exit 1
fi

HOST_IP="$(ssh -o BatchMode=yes "${TARGET_HOST}" "hostname -I | awk '{print \$1}'" | tr -d '\r' | awk '{print $1}')"
PREVIEW_URL="http://${HOST_IP}:${PORT}/"

ssh -o BatchMode=yes "${TARGET_HOST}" "cat > '${MANIFEST_DIR}/${NAME}.json'" <<EOF
{
  "name": "${NAME}",
  "url": "${PREVIEW_URL}",
  "port": ${PORT},
  "branch": "${BRANCH}",
  "commit": "${COMMIT}",
  "created": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

success "Preview deployment '${NAME}' is running!"
echo "=================================================================="
echo "Preview URL:       ${PREVIEW_URL}   (PR Web UI + daemon API)"
echo "Deployments Home:  http://${HOST_IP}:18080/"
echo "Compose project:   ${PROJECT}"
echo "Branch/commit:     ${BRANCH} @ ${COMMIT}"
echo "=================================================================="
echo "Logs:      ssh ${TARGET_HOST} 'sudo docker logs -f ${PROJECT}-computed-1'"
echo "Teardown:  make preview-stop PR=${NAME}   (or: ./scripts/preview-docker.sh ${NAME} --stop)"
echo ""
