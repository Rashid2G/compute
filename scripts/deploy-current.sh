#!/usr/bin/env bash
# scripts/deploy-current.sh
#
# Deploy a preview for the CURRENT branch or worktree, straight from where you
# are working. No PR number required:
#
#   - If the branch has an open GitHub PR, the deployment is named pr-<number>
#     (same naming as PR previews, so reviewers get a consistent URL).
#   - Otherwise the name is derived from the branch (or worktree):
#     feat/add-scp -> feat-add-scp.
#
# Runs the branch's own `computed` binary (PR Web UI + daemon API) as its own
# Docker Compose stack on the compute-lab VM's Docker daemon (never inside the
# dev deployment). See scripts/preview-docker.sh.
#
# Usage:
#   ./scripts/deploy-current.sh [name|pr]       deploy a preview for HEAD (or PR/name)
#   ./scripts/deploy-current.sh --stop [name]   tear the preview down
#   ./scripts/deploy-current.sh --name my-name  override the deployment name
#   ./scripts/deploy-current.sh --status [name] show the deployment status
#   ./scripts/deploy-current.sh --logs [name]   follow the preview daemon logs
#
# Environment:
#   COMPUTE_HOST    Target SSH host (default: compute-lab)
#   PREVIEW_BASE    Base ref for the change diff (default: origin/dev)

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
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
        --logs) MODE="logs"; shift ;;
        --name) NAME_OVERRIDE="${2:?--name requires a value}"; shift 2 ;;
        --name=*) NAME_OVERRIDE="${1#*=}"; shift ;;
        *) POSITIONAL+=("$1"); shift ;;
    esac
done

# ---------------------------------------------------------------------------
# Derive the deployment name from override, positional arg, branch, or PR
# ---------------------------------------------------------------------------
if [[ -n "${NAME_OVERRIDE}" ]]; then
    SANDBOX_NAME="${NAME_OVERRIDE}"
elif [[ ${#POSITIONAL[@]} -gt 0 ]]; then
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
# Sanitize into a Docker/DNS-safe label: lowercase, [a-z0-9-], max 63.
SANDBOX_NAME="$(echo "${SANDBOX_NAME}" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/-/g; s/^-+|-+$//g' | cut -c1-63)"

if [[ -z "${SANDBOX_NAME}" ]]; then
    err "Could not derive a deployment name (detached HEAD with no worktree name?). Use --name <name>."
    exit 1
fi

case "${MODE}" in
    stop)
        exec "${REPO_ROOT}/scripts/preview-docker.sh" "${SANDBOX_NAME}" --stop
        ;;
    status)
        exec "${REPO_ROOT}/scripts/preview-docker.sh" "${SANDBOX_NAME}" --status
        ;;
    logs)
        exec "${REPO_ROOT}/scripts/preview-docker.sh" "${SANDBOX_NAME}" --logs
        ;;
    deploy)
        log "Deploying Docker preview '${SANDBOX_NAME}' from $(basename "${REPO_ROOT}") ($(git -C "${REPO_ROOT}" rev-parse --short HEAD 2>/dev/null || echo '?'))..."
        exec "${REPO_ROOT}/scripts/preview-docker.sh" "${SANDBOX_NAME}"
        ;;
esac
