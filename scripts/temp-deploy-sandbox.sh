#!/usr/bin/env bash
# scripts/temp-deploy-sandbox.sh
# Provisions a temporary preview sandbox on compute-lab for a PR and deploys ONLY
# the services changed by that PR into the guest microVM. Unchanged services are
# reused from the main compute-lab deployment so the sandbox stays light:
#
#   - Web UI / GUI server (compute binary, embedded UI): always runs in the guest;
#     it is the front door of the preview. Its /api/* proxy targets the lab daemon
#     (http://172.16.0.1:18080) unless the daemon itself changed in this PR.
#   - computed daemon (API :18080): runs in the guest ONLY when daemon-side code
#     changed; otherwise the compute-lab daemon is reused (offloaded).
#   - compute-worker: runs in the guest only when worker code changed (needs a
#     control-plane token via COMPUTE_WORKER_TOKEN to actually register).
#
# This way a UI-only PR runs the new UI against the shared lab API, a daemon PR
# runs the new daemon plus the matching UI, and both are testable end-to-end
# through a single URL.
#
# Prints the Tailscale URL so reviewers can verify changes live on the tailnet.
# Designed to be launched in the background without blocking PR creation.

set -euo pipefail

TARGET_HOST="${COMPUTE_HOST:-compute-lab}"
TARGET_DIR="${COMPUTE_DIR:-/home/rashid/on-demand-vms}"
CLI_BIN="${TARGET_DIR}/bin/compute"
GUEST_USER="${COMPUTE_GUEST_USER:-compute}"
GUEST_PORT="${2:-8080}"
PREVIEW_DIR="${PREVIEW_DIR:-/home/${GUEST_USER}/preview}"
BASE_REF="${PREVIEW_BASE:-origin/dev}"
PREVIEW_DAEMON_CANDIDATES="${PREVIEW_DAEMON_CANDIDATES:-http://172.16.0.1:18080 http://100.78.129.96:18080}"

# Determine PR number (from argument, PR env, or gh pr view)
PR_ARG="${1:-${PR:-}}"

if [[ -z "${PR_ARG}" ]]; then
    # Attempt auto-detection from gh pr view
    DETECTED_PR="$(gh pr view --json number -q .number 2>/dev/null || true)"
    if [[ -n "${DETECTED_PR}" && "${DETECTED_PR}" =~ ^[0-9]+$ ]]; then
        PR_ARG="${DETECTED_PR}"
    fi
fi

if [[ -z "${PR_ARG}" ]]; then
    # Attempt extraction from git branch name (e.g., fix/25-feature or pr-25)
    BRANCH_NUM="$(git rev-parse --abbrev-ref HEAD 2>/dev/null | grep -oE '[0-9]+' | head -n1 || true)"
    if [[ -n "${BRANCH_NUM}" ]]; then
        PR_ARG="${BRANCH_NUM}"
    fi
fi

if [[ -z "${PR_ARG}" ]]; then
    echo "Error: Each preview deployment must contain the PR number." >&2
    echo "Usage: $(basename "$0") <pr-number> [guest-port]" >&2
    echo "Example: $(basename "$0") 25 8080" >&2
    exit 1
fi

# Ensure sandbox name follows pr-<PR_NUMBER> naming
if [[ "${PR_ARG}" =~ ^pr- ]]; then
    SANDBOX_NAME="${PR_ARG}"
else
    SANDBOX_NAME="pr-${PR_ARG}"
fi
STAGE_DIR="/tmp/${SANDBOX_NAME}-preview"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }
success() { printf '\033[1;32m[✓]\033[0m %s\n' "$*"; }

usage() {
    cat <<EOF
Usage: $(basename "$0") <pr-number> [guest-port]

Spin up an isolated Firecracker microVM sandbox on ${TARGET_HOST} (running on the
main deployment deployed from the dev branch) for the PR preview, deploy ONLY the
services changed by the PR into the guest, and output its Tailscale URL.

Service selection (diff against ${BASE_REF}):
  web/** or internal/gui/** or cmd/compute/** -> new GUI server in guest (always the front door)
  cmd/computed/**, internal/**, pkg/**, go.mod -> new computed daemon in guest
  cmd/compute-worker/**                       -> new compute-worker in guest
  docs/scripts/sdk-only changes               -> no service rebuild; GUI front door only

Unchanged services are reused from the compute-lab main deployment.

Arguments:
  <pr-number>    GitHub Pull Request number (sandbox will be named pr-<pr-number>)
  [guest-port]   Port exposed by the preview GUI server (default: 8080)

Environment:
  COMPUTE_HOST                Target SSH host (default: compute-lab)
  PREVIEW_BASE                Git base ref for the change diff (default: origin/dev)
  PREVIEW_DAEMON_CANDIDATES   Daemon URLs probed from the guest (default: host bridge + tailnet IP)
  COMPUTE_WORKER_TOKEN        Control-plane token to start a changed worker
  COMPUTE_VCPUS               Allocated vCPUs (default: 1)
  COMPUTE_MEMORY_MB           Allocated RAM in MB (default: 512, or 1024 when the daemon is included)
  COMPUTE_SSH_PASSWORD        Guest SSH password for provisioning (default: compute1234)
  COMPUTE_GUEST_SSH_KEY       Daemon SSH private key used to copy files into the guest
                              (default: /var/lib/compute/images/ssh/id_ed25519)

Examples:
  ./scripts/temp-deploy-sandbox.sh 25 8080
  # Or run in background so it does not block PR creation:
  ./scripts/temp-deploy-sandbox.sh 25 8080 &
EOF
    exit 0
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
fi

cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"

# ---------------------------------------------------------------------------
# 1. Detect which services changed vs the base ref
# ---------------------------------------------------------------------------
log "Detecting changed services (base: ${BASE_REF})..."

BASE_COMMIT=""
if git rev-parse --verify --quiet "${BASE_REF}^{commit}" >/dev/null 2>&1; then
    BASE_COMMIT="$(git merge-base "${BASE_REF}" HEAD)"
elif git rev-parse --verify --quiet "dev^{commit}" >/dev/null 2>&1; then
    warn "Base ref '${BASE_REF}' not found; falling back to 'dev'."
    BASE_COMMIT="$(git merge-base dev HEAD)"
else
    warn "Cannot resolve base ref '${BASE_REF}' (run 'git fetch origin'); deploying all services to be safe."
fi

NEED_GUI=0 NEED_DAEMON=0 NEED_WORKER=0 WEB_CHANGED=0
if [[ -n "${BASE_COMMIT}" ]]; then
    CHANGED_FILES="$(
        {
            git diff --name-only "${BASE_COMMIT}" HEAD
            git diff --name-only HEAD
            git ls-files --others --exclude-standard
        } | sort -u
    )"
    if [[ -z "${CHANGED_FILES}" ]]; then
        warn "No file changes detected vs ${BASE_REF}; previewing the branch GUI front door only."
        NEED_GUI=1
    fi
    while IFS= read -r f; do
        [[ -z "${f}" ]] && continue
        case "${f}" in
            web/*)                 WEB_CHANGED=1; NEED_GUI=1 ;;
            internal/gui/*)        NEED_GUI=1 ;;
            cmd/compute/*)         NEED_GUI=1 ;;
            cmd/compute-worker/*)  NEED_WORKER=1 ;;
            cmd/computed/*|internal/*|pkg/*|go.mod|go.sum|docker-compose.yml|config/*)
                                   NEED_DAEMON=1 ;;
            *.go)                  NEED_DAEMON=1 ;;
            *)                     : ;;  # docs, scripts, sdk, .agents, Makefile: no runtime change
        esac
    done <<< "${CHANGED_FILES}"
else
    NEED_GUI=1 NEED_DAEMON=1
fi

# A new daemon in the guest needs its matching GUI; a worker needs a GUI front
# door for a testable preview URL.
if [[ "${NEED_DAEMON}" -eq 1 || "${NEED_WORKER}" -eq 1 ]]; then
    NEED_GUI=1
fi

SERVICES_NEW=""
SERVICES_REUSED=""
if [[ "${NEED_GUI}" -eq 1 ]]; then
    SERVICES_NEW="web UI + GUI server (new build)"
fi
if [[ "${NEED_DAEMON}" -eq 1 ]]; then
    SERVICES_NEW="${SERVICES_NEW}; computed daemon (new build, guest-local :18080)"
else
    SERVICES_REUSED="computed daemon (compute-lab, proxied by the guest GUI)"
fi
if [[ "${NEED_WORKER}" -eq 1 ]]; then
    SERVICES_NEW="${SERVICES_NEW}; compute-worker (new build)"
else
    SERVICES_REUSED="${SERVICES_REUSED:+${SERVICES_REUSED}; }compute-worker (compute-lab)"
fi

log "Services deployed NEW into sandbox: ${SERVICES_NEW}"
log "Services REUSED from compute-lab:  ${SERVICES_REUSED:-none (full stack in guest)}"

# ---------------------------------------------------------------------------
# 2. Build the required linux binaries (and web assets if the UI changed)
# ---------------------------------------------------------------------------
BUILD_DIR="bin/preview"
mkdir -p "${BUILD_DIR}"

if [[ "${WEB_CHANGED}" -eq 1 ]]; then
    log "Building web UI assets (web/ changed)..."
    if ! (cd web && { [[ -d node_modules ]] || npm ci --silent; } && npm run build); then
        warn "Web UI build failed; falling back to committed assets in internal/gui/assets (UI may be stale)."
    fi
fi

build_bin() {
    local name="$1" dir="$2"
    log "Building ${name} (linux/amd64)..."
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "${BUILD_DIR}/${name}" "./${dir}"
}

if [[ "${NEED_GUI}" -eq 1 ]]; then
    build_bin compute cmd/compute
fi
if [[ "${NEED_DAEMON}" -eq 1 ]]; then
    build_bin computed cmd/computed
fi
if [[ "${NEED_WORKER}" -eq 1 ]]; then
    build_bin compute-worker cmd/compute-worker
fi

# Guest daemon configuration (only used when the daemon runs inside the sandbox)
if [[ "${NEED_DAEMON}" -eq 1 ]]; then
    cat > "${BUILD_DIR}/daemon.json" <<'JSON'
{
  "server": {
    "listen": "0.0.0.0:18080",
    "db_path": "/var/lib/compute/data/compute.db"
  },
  "network": {
    "mode": "nat",
    "bridge_name": "compute0",
    "bridge_ip": "10.20.0.1/16",
    "subnet": "10.20.0.0/16",
    "gateway_ip": "10.20.0.1"
  },
  "images": {
    "images_dir": "/var/lib/compute/images",
    "vms_dir": "/var/lib/compute/vms",
    "ssh_key_path": "/var/lib/compute/images/ssh/id_ed25519"
  },
  "resources": {
    "max_running_vms": 0
  },
  "jobs": {
    "local_execution": false
  },
  "tailscale": {
    "enabled": false
  }
}
JSON
fi

VCPUS="${COMPUTE_VCPUS:-1}"
if [[ -z "${COMPUTE_MEMORY_MB:-}" ]]; then
    if [[ "${NEED_DAEMON}" -eq 1 ]]; then
        MEMORY_MB=1024
    else
        MEMORY_MB=512
    fi
else
    MEMORY_MB="${COMPUTE_MEMORY_MB}"
fi
SSH_PASSWORD="${COMPUTE_SSH_PASSWORD:-compute1234}"

log "Target host: ${TARGET_HOST}"
log "Sandbox name: ${SANDBOX_NAME}"
log "Guest port: ${GUEST_PORT} (vCPUS: ${VCPUS}, RAM: ${MEMORY_MB}MB)"

# ---------------------------------------------------------------------------
# 3. Provision the sandbox on compute-lab (main dev deployment)
# ---------------------------------------------------------------------------
log "Provisioning microVM sandbox '${SANDBOX_NAME}' on ${TARGET_HOST}..."
ssh -o BatchMode=yes "${TARGET_HOST}" "
    # If sandbox exists, stop and delete to ensure clean temp state
    if ${CLI_BIN} sandbox info '${SANDBOX_NAME}' >/dev/null 2>&1; then
        echo 'Recreating existing sandbox ${SANDBOX_NAME}...'
        ${CLI_BIN} sandbox stop '${SANDBOX_NAME}' >/dev/null 2>&1 || true
        ${CLI_BIN} sandbox delete '${SANDBOX_NAME}' >/dev/null 2>&1 || true
    fi
    ${CLI_BIN} sandbox create --name '${SANDBOX_NAME}' --vcpus ${VCPUS} --memory ${MEMORY_MB} --ssh-password '${SSH_PASSWORD}'
    ${CLI_BIN} sandbox start '${SANDBOX_NAME}'
"

# 4. Wait for Tailnet IP and DNS to be assigned
log "Waiting for Tailscale provisioning (this takes ~20-30s)..."
TAILNET_IP=""
TAILNET_DNS=""
for i in $(seq 1 25); do
    INFO_OUT="$(ssh -o BatchMode=yes "${TARGET_HOST}" "${CLI_BIN} sandbox info '${SANDBOX_NAME}'" 2>/dev/null || true)"
    TAILNET_IP="$(echo "${INFO_OUT}" | awk '/Tailnet IP:/ {print $3}' | tr -d '\r')"
    TAILNET_DNS="$(echo "${INFO_OUT}" | awk '/Tailnet DNS:/ {print $3}' | tr -d '\r')"

    if [[ -n "${TAILNET_IP}" && "${TAILNET_IP}" != "—" && "${TAILNET_IP}" != "" ]]; then
        break
    fi
    sleep 2
done

if [[ -z "${TAILNET_IP}" || "${TAILNET_IP}" == "—" ]]; then
    echo "Error: sandbox '${SANDBOX_NAME}' never received a Tailnet IP; cannot install services." >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# 5. Locate the daemon SSH key on the lab host (used to provision guests)
# ---------------------------------------------------------------------------
GUEST_SSH_KEY="${COMPUTE_GUEST_SSH_KEY:-}"
if [[ -z "${GUEST_SSH_KEY}" ]]; then
    GUEST_SSH_KEY="$(ssh -o BatchMode=yes "${TARGET_HOST}" "
        for cand in /var/lib/compute/images/ssh/id_ed25519 '${TARGET_DIR}/data/images/ssh/id_ed25519'; do
            if sudo -n test -f \"\$cand\" 2>/dev/null || test -f \"\$cand\" 2>/dev/null; then
                echo \"\$cand\"; break
            fi
        done
    " | tr -d '\r' | head -n1)"
fi
if [[ -z "${GUEST_SSH_KEY}" ]]; then
    echo "Error: could not locate the daemon guest SSH key on ${TARGET_HOST} (set COMPUTE_GUEST_SSH_KEY)." >&2
    exit 1
fi

GUEST_SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=10 -i ${GUEST_SSH_KEY}"

# Wait for guest SSH to accept connections
log "Waiting for guest SSH on ${GUEST_USER}@${TAILNET_IP}..."
GUEST_READY=0
for i in $(seq 1 30); do
    if ssh -o BatchMode=yes "${TARGET_HOST}" "sudo -n ssh ${GUEST_SSH_OPTS} ${GUEST_USER}@${TAILNET_IP} true 2>/dev/null" >/dev/null 2>&1; then
        GUEST_READY=1
        break
    fi
    sleep 2
done
if [[ "${GUEST_READY}" -ne 1 ]]; then
    echo "Error: guest SSH never became reachable at ${GUEST_USER}@${TAILNET_IP}." >&2
    exit 1
fi
success "Guest SSH reachable."

# ---------------------------------------------------------------------------
# 6. Stage and install the new service binaries into the guest
# ---------------------------------------------------------------------------
log "Staging binaries on ${TARGET_HOST}..."
ssh -o BatchMode=yes "${TARGET_HOST}" "rm -rf '${STAGE_DIR}' && mkdir -p '${STAGE_DIR}'"
scp -o BatchMode=yes ${BUILD_DIR}/* "${TARGET_HOST}:${STAGE_DIR}/" >/dev/null

log "Installing services into guest ${SANDBOX_NAME} (${TAILNET_IP})..."
ssh -o BatchMode=yes "${TARGET_HOST}" "
    sudo -n ssh ${GUEST_SSH_OPTS} ${GUEST_USER}@${TAILNET_IP} 'mkdir -p ${PREVIEW_DIR}/bin ${PREVIEW_DIR}/logs'
    sudo -n scp ${GUEST_SSH_OPTS} ${STAGE_DIR}/* ${GUEST_USER}@${TAILNET_IP}:${PREVIEW_DIR}/bin/ >/dev/null
"

# ---------------------------------------------------------------------------
# 7. Start the preview services in the guest
# ---------------------------------------------------------------------------
log "Starting preview services in guest..."

# Build the in-guest startup script locally (avoid remote quoting issues),
# copy it in, and execute it.
START_SCRIPT="$(mktemp /tmp/pr-preview-start-XXXXXX.sh)"
cat > "${START_SCRIPT}" <<EOF
#!/usr/bin/env bash
set -uo pipefail
PREVIEW_DIR="${PREVIEW_DIR}"
GUEST_PORT="${GUEST_PORT}"
DEPLOY_DAEMON="${NEED_DAEMON}"
DEPLOY_WORKER="${NEED_WORKER}"
DAEMON_CANDIDATES="${PREVIEW_DAEMON_CANDIDATES}"
WORKER_TOKEN="\${COMPUTE_WORKER_TOKEN:-}"
WORKER_CONTROL_PLANE="\${COMPUTE_WORKER_CONTROL_PLANE:-}"
mkdir -p "\${PREVIEW_DIR}/logs"

# Stop any previous preview processes (idempotent re-runs)
pkill -f "\${PREVIEW_DIR}/bin/compute gui" 2>/dev/null || true
pkill -f "\${PREVIEW_DIR}/bin/computed" 2>/dev/null || true
pkill -f "\${PREVIEW_DIR}/bin/compute-worker" 2>/dev/null || true
sleep 1

if [ "\${DEPLOY_DAEMON}" = "1" ]; then
    # New daemon runs guest-local; the GUI proxies to it over loopback.
    sudo -n mkdir -p /etc/compute /var/lib/compute/data /var/lib/compute/images /var/lib/compute/vms /var/log/compute
    sudo -n cp "\${PREVIEW_DIR}/bin/daemon.json" /etc/compute/daemon.json
    sudo -n bash -c "nohup '\${PREVIEW_DIR}/bin/computed' -config /etc/compute/daemon.json >/var/log/compute/computed.log 2>&1 & disown"
    DAEMON_URL="http://127.0.0.1:18080"
    for i in \$(seq 1 45); do
        curl -sf --connect-timeout 2 "\${DAEMON_URL}/api/v1/health" >/dev/null 2>&1 && break
        sleep 1
    done
else
    # Daemon unchanged: reuse the compute-lab daemon. Probe candidates from
    # inside the guest so the URL works in this network.
    DAEMON_URL=""
    for cand in \${DAEMON_CANDIDATES}; do
        if curl -sf --connect-timeout 3 "\${cand}/api/v1/health" >/dev/null 2>&1; then
            DAEMON_URL="\${cand}"
            break
        fi
    done
    if [ -z "\${DAEMON_URL}" ]; then
        DAEMON_URL="http://172.16.0.1:18080"
        echo "WARNING: no daemon candidate reachable from guest; defaulting to \${DAEMON_URL}" >&2
    fi
fi

# GUI server is the preview front door (embedded web UI + /api reverse proxy)
nohup "\${PREVIEW_DIR}/bin/compute" gui --addr 0.0.0.0 --port "\${GUEST_PORT}" --daemon "\${DAEMON_URL}" >"\${PREVIEW_DIR}/logs/gui.log" 2>&1 & disown

if [ "\${DEPLOY_WORKER}" = "1" ]; then
    if [ -n "\${WORKER_TOKEN}" ]; then
        CP="\${WORKER_CONTROL_PLANE:-\${DAEMON_URL}}"
        nohup "\${PREVIEW_DIR}/bin/compute-worker" run --control-plane "\${CP}" --token "\${WORKER_TOKEN}" --name pr-worker --pool default >"\${PREVIEW_DIR}/logs/worker.log" 2>&1 & disown
    else
        echo "NOTE: worker binary staged at \${PREVIEW_DIR}/bin/compute-worker; set COMPUTE_WORKER_TOKEN to register it." >&2
    fi
fi

echo "DAEMON_URL=\${DAEMON_URL}"
EOF

scp -o BatchMode=yes "${START_SCRIPT}" "${TARGET_HOST}:${STAGE_DIR}/start.sh" >/dev/null
ssh -o BatchMode=yes "${TARGET_HOST}" "
    sudo -n scp ${GUEST_SSH_OPTS} ${STAGE_DIR}/start.sh ${GUEST_USER}@${TAILNET_IP}:${PREVIEW_DIR}/start.sh >/dev/null
    sudo -n ssh ${GUEST_SSH_OPTS} ${GUEST_USER}@${TAILNET_IP} 'chmod +x ${PREVIEW_DIR}/start.sh && ${PREVIEW_DIR}/start.sh'
"
rm -f "${START_SCRIPT}"

# ---------------------------------------------------------------------------
# 8. Health checks (GUI front door, and guest-local daemon when deployed)
# ---------------------------------------------------------------------------
log "Running health checks..."
GUI_OK=0
for i in $(seq 1 30); do
    CODE="$(ssh -o BatchMode=yes "${TARGET_HOST}" "curl -s -o /dev/null -w '%{http_code}' --connect-timeout 3 http://${TAILNET_IP}:${GUEST_PORT}/api/v1/health 2>/dev/null || true" | tr -d '\r')"
    if [[ "${CODE}" == "200" ]]; then
        GUI_OK=1
        break
    fi
    sleep 2
done
if [[ "${GUI_OK}" -ne 1 ]]; then
    warn "GUI health check did not return 200 in time; inspect logs: ssh compute-lab \"sudo -n ssh -i ${GUEST_SSH_KEY} ${GUEST_USER}@${TAILNET_IP} 'tail -n 40 ${PREVIEW_DIR}/logs/gui.log'\""
else
    success "GUI front door healthy on port ${GUEST_PORT}."
fi

if [[ "${NEED_DAEMON}" -eq 1 ]]; then
    DAEMON_OK=0
    for i in $(seq 1 30); do
        CODE="$(ssh -o BatchMode=yes "${TARGET_HOST}" "curl -s -o /dev/null -w '%{http_code}' --connect-timeout 3 http://${TAILNET_IP}:18080/api/v1/health 2>/dev/null || true" | tr -d '\r')"
        if [[ "${CODE}" == "200" ]]; then
            DAEMON_OK=1
            break
        fi
        sleep 2
    done
    if [[ "${DAEMON_OK}" -ne 1 ]]; then
        warn "Guest-local daemon health check did not return 200 in time; inspect logs: ssh compute-lab \"sudo -n ssh -i ${GUEST_SSH_KEY} ${GUEST_USER}@${TAILNET_IP} 'sudo tail -n 40 /var/log/compute/computed.log'\""
    else
        success "Guest-local daemon healthy on port 18080."
    fi
fi

# ---------------------------------------------------------------------------
# 9. Resolve Tailscale URLs and print the summary
# ---------------------------------------------------------------------------
HOST_IP="$(ssh -o BatchMode=yes "${TARGET_HOST}" "tailscale ip -4 2>/dev/null || hostname -I | awk '{print \$1}'" | tr -d '\r')"

echo ""
success "Temporary preview deployment is running!"
echo "=================================================================="
echo "Sandbox Name:      ${SANDBOX_NAME}"
echo "Services (NEW):    ${SERVICES_NEW}"
echo "Services (REUSED): ${SERVICES_REUSED:-none}"
if [[ -n "${TAILNET_DNS}" && "${TAILNET_DNS}" != "—" ]]; then
    echo "Tailscale DNS URL: http://${TAILNET_DNS}:${GUEST_PORT}"
fi
echo "Tailscale IP URL:  http://${TAILNET_IP}:${GUEST_PORT}"
if [[ -n "${HOST_IP}" ]]; then
    echo "Ingress Proxy URL: http://${HOST_IP}:18080/preview/${SANDBOX_NAME}/${GUEST_PORT}/"
fi
echo "SSH Command:       compute ssh ${SANDBOX_NAME}"
echo "=================================================================="
echo "Teardown command:  ssh ${TARGET_HOST} \"${CLI_BIN} sandbox stop '${SANDBOX_NAME}' && ${CLI_BIN} sandbox delete '${SANDBOX_NAME}' && rm -rf '${STAGE_DIR}'\""
echo ""
