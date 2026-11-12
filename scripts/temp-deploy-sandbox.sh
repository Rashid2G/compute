#!/usr/bin/env bash
# scripts/temp-deploy-sandbox.sh
# Provisions and runs a temporary deployment sandbox on compute-lab for PR preview and testing.
# Uses the main deployment on compute-lab to serve an isolated microVM sandbox running the new changes.
# Prints the Tailscale URL so reviewers can verify changes live on the tailnet.
# Designed to be launched in the background without blocking PR creation.

set -euo pipefail

TARGET_HOST="${COMPUTE_HOST:-compute-lab}"
TARGET_DIR="${COMPUTE_DIR:-/home/rashid/on-demand-vms}"
CLI_BIN="${TARGET_DIR}/bin/compute"

# Determine default sandbox name from git branch or timestamp
GIT_BRANCH="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "preview")"
CLEAN_BRANCH="$(echo "${GIT_BRANCH}" | tr '/' '-' | tr '_' '-' | tr -cd '[:alnum:]-' | tr '[:upper:]' '[:lower:]')"
RAND_SUFFIX="$(head -c 3 /dev/urandom 2>/dev/null | xxd -p 2>/dev/null || echo "$$")"
DEFAULT_NAME="prv-${CLEAN_BRANCH:0:15}-${RAND_SUFFIX:0:4}"

SANDBOX_NAME="${1:-${DEFAULT_NAME}}"
GUEST_PORT="${2:-8080}"
VCPUS="${COMPUTE_VCPUS:-1}"
MEMORY_MB="${COMPUTE_MEMORY_MB:-512}"
SSH_PASSWORD="${COMPUTE_SSH_PASSWORD:-compute1234}"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }
success() { printf '\033[1;32m[✓]\033[0m %s\n' "$*"; }

usage() {
    cat <<EOF
Usage: $(basename "$0") [sandbox-name] [guest-port]

Spin up an isolated Firecracker microVM sandbox on ${TARGET_HOST} running the updated
code/version on a separate temporary deployment, and output its Tailscale URL.

Arguments:
  sandbox-name   Name for the preview sandbox (default: ${DEFAULT_NAME})
  guest-port     Port exposed by the preview service (default: 8080)

Environment:
  COMPUTE_HOST          Target SSH host (default: compute-lab)
  COMPUTE_VCPUS         Allocated vCPUs (default: 1)
  COMPUTE_MEMORY_MB     Allocated RAM in MB (default: 512)
  COMPUTE_SSH_PASSWORD  Guest SSH password for provisioning (default: compute1234)

Examples:
  ./scripts/temp-deploy-sandbox.sh
  ./scripts/temp-deploy-sandbox.sh my-preview-box 8080
  # Or run in background so it does not block:
  ./scripts/temp-deploy-sandbox.sh my-preview-box 8080 &
EOF
    exit 0
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
fi

log "Target host: ${TARGET_HOST}"
log "Sandbox name: ${SANDBOX_NAME}"
log "Guest port: ${GUEST_PORT} (vCPUs: ${VCPUS}, RAM: ${MEMORY_MB}MB)"

# 1. Provision sandbox on compute-lab
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

# 2. Wait for Tailnet IP and DNS to be assigned
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

# 3. Resolve Tailscale URLs
HOST_IP="$(ssh -o BatchMode=yes "${TARGET_HOST}" "tailscale ip -4 2>/dev/null || hostname -I | awk '{print \$1}'" | tr -d '\r')"

echo ""
success "Temporary deployment sandbox is running!"
echo "=================================================================="
echo "Sandbox Name:      ${SANDBOX_NAME}"
if [[ -n "${TAILNET_DNS}" && "${TAILNET_DNS}" != "—" ]]; then
    echo "Tailscale DNS URL: http://${TAILNET_DNS}:${GUEST_PORT}"
fi
if [[ -n "${TAILNET_IP}" && "${TAILNET_IP}" != "—" ]]; then
    echo "Tailscale IP URL:  http://${TAILNET_IP}:${GUEST_PORT}"
fi
if [[ -n "${HOST_IP}" ]]; then
    echo "Ingress Proxy URL: http://${HOST_IP}:18080/preview/${SANDBOX_NAME}/${GUEST_PORT}/"
fi
echo "SSH Command:       compute ssh ${SANDBOX_NAME}"
echo "=================================================================="
echo "Teardown command:  ssh ${TARGET_HOST} \"${CLI_BIN} sandbox stop '${SANDBOX_NAME}' && ${CLI_BIN} sandbox delete '${SANDBOX_NAME}'\""
echo ""
