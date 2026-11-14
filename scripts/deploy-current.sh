#!/usr/bin/env bash
# scripts/deploy-current.sh
#
# Deploy a preview sandbox for the CURRENT branch or worktree, straight from
# where you are working. No PR number required:
#
#   - If the branch has an open GitHub PR, the sandbox is named pr-<number>
#     (same naming as PR previews, so reviewers get a consistent URL).
#   - Otherwise the sandbox name is derived from the branch (or worktree):
#     feat/add-scp -> feat-add-scp.
#
# It wraps scripts/temp-deploy-sandbox.sh, which detects which services your
# changes touch (UI-only, daemon, worker) and deploys only those into an
# isolated microVM on the lab host, reusing the rest from the main dev
# deployment.
#
# Usage:
#   ./scripts/deploy-current.sh [port]            deploy a preview for HEAD
#   ./scripts/deploy-current.sh --stop [name]     tear the preview down
#   ./scripts/deploy-current.sh --name my-name    override the sandbox name
#   ./scripts/deploy-current.sh --status          show the sandbox status
#
# Environment:
#   COMPUTE_HOST   Target SSH host (default: compute-lab)
#   PREVIEW_BASE   Base ref for the change diff (default: origin/dev)

set -euo pipefail

TARGET_HOST="${COMPUTE_HOST:-compute-lab}"
CLI_BIN="${CLI_BIN:-/home/rashid/on-demand-vms/bin/compute}"
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
success() { printf '\033[1;32m[✓]\033[0m %s\n' "$*"; }
err() { printf '\033[1;31m[x]\033[0m %s\n' "$*" >&2; }

usage() {
    grep '^#   ' "${0}" | sed 's/^#   //'
    exit 0
}

MODE="deploy"
NAME_OVERRIDE=""
POSITIONAL=()
while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help) usage ;;
        --stop) MODE="stop"; shift ;;
        --status) MODE="status"; shift ;;
        --name) NAME_OVERRIDE="${2:?--name requires a value}"; shift 2 ;;
        --name=*) NAME_OVERRIDE="${1#*=}"; shift ;;
        *) POSITIONAL+=("$1"); shift ;;
    esac
done

# ---------------------------------------------------------------------------
# Derive the sandbox name from override, positional arg (stop/status), branch, or PR
# ---------------------------------------------------------------------------
if [[ -n "${NAME_OVERRIDE}" ]]; then
    SANDBOX_NAME="${NAME_OVERRIDE}"
elif [[ ( "${MODE}" == "stop" || "${MODE}" == "status" ) && ${#POSITIONAL[@]} -gt 0 ]]; then
    ARG_NAME="${POSITIONAL[0]}"
    if [[ "${ARG_NAME}" =~ ^[0-9]+$ ]]; then
        SANDBOX_NAME="pr-${ARG_NAME}"
    else
        SANDBOX_NAME="${ARG_NAME}"
    fi
else
    BRANCH="$(git -C "${REPO_ROOT}" rev-parse --abbrev-ref HEAD 2>/dev/null || echo detached)"
    if [[ "${BRANCH}" == "detached" ]]; then
        # Detached HEAD (e.g. inside a bare worktree checkout): fall back to
        # the worktree directory name.
        BRANCH="$(basename "$(pwd)" | sed 's/^on-demand-vms-//')"
    fi
    SANDBOX_NAME="${BRANCH}"
    # Prefer the PR number when one exists for this branch.
    PR_NUM="$(cd "${REPO_ROOT}" && gh pr view --json number -q .number 2>/dev/null || true)"
    if [[ "${PR_NUM}" =~ ^[0-9]+$ ]]; then
        SANDBOX_NAME="pr-${PR_NUM}"
    fi
fi
# Sanitize into a sandbox-safe DNS label: lowercase, [a-z0-9-], max 63.
SANDBOX_NAME="$(echo "${SANDBOX_NAME}" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/-/g; s/^-+|-+$//g' | cut -c1-63)"

if [[ -z "${SANDBOX_NAME}" ]]; then
    err "Could not derive a sandbox name (detached HEAD with no worktree name?). Use --name <name>."
    exit 1
fi

case "${MODE}" in
    stop)
        log "Stopping preview sandbox '${SANDBOX_NAME}' on ${TARGET_HOST}..."
        ssh -o BatchMode=yes "${TARGET_HOST}" "
            ${CLI_BIN} sandbox stop '${SANDBOX_NAME}' || true
            ${CLI_BIN} sandbox delete '${SANDBOX_NAME}' || true
            rm -rf '/tmp/${SANDBOX_NAME}-preview'
        "
        success "Preview '${SANDBOX_NAME}' removed."
        ;;
    status)
        ssh -o BatchMode=yes "${TARGET_HOST}" "${CLI_BIN} sandbox info '${SANDBOX_NAME}'"
        ;;
    deploy)
        log "Deploying preview sandbox '${SANDBOX_NAME}' from $(basename "${REPO_ROOT}") ($(git -C "${REPO_ROOT}" rev-parse --short HEAD 2>/dev/null || echo '?'))..."
        exec "${REPO_ROOT}/scripts/temp-deploy-sandbox.sh" "${SANDBOX_NAME}" ${POSITIONAL[@]+"${POSITIONAL[@]}"}
        ;;
esac
