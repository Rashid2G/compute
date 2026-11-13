#!/usr/bin/env bash
# scripts/reconcile-ssh.sh
# Reconcile SSH accounts and unlock legacy sandboxes where shadow passwords start with '!' or '*'.
# This allows OpenSSH 9.6 with UsePAM=no to permit public-key authentication without recreating sandboxes.

set -euo pipefail

DAEMON_URL="${COMPUTE_DAEMON:-http://127.0.0.1:18080}"
TARGET="${1:-}"

echo "==> Compute SSH Reconciliation Tool"

if [ -n "${TARGET}" ]; then
  echo "==> Reconciling SSH access for sandbox: ${TARGET}"
  if command -v compute >/dev/null 2>&1; then
    compute keys reconcile "${TARGET}"
  else
    curl -sS -f -X POST "${DAEMON_URL}/api/v1/sandboxes/${TARGET}/reconcile-ssh"
  fi
  echo "==> Reconciled sandbox ${TARGET}"
  exit 0
fi

echo "==> Reconciling all sandboxes across daemon..."
if command -v compute >/dev/null 2>&1; then
  compute keys reconcile --all
else
  curl -sS -f -X POST "${DAEMON_URL}/api/v1/keys/reconcile"
fi

echo "==> SSH reconciliation complete."
