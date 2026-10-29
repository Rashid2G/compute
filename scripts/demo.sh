#!/usr/bin/env bash
set -euo pipefail

MFORGE_BIN="${1:-./bin/mforge}"

echo "=========================================================="
echo "  Firecracker On-Demand VM Orchestrator End-to-End Demo  "
echo "=========================================================="

echo
echo "[Step 1] Creating a new Firecracker on-demand VM..."
CREATE_OUT=$("${MFORGE_BIN}" create --name demo-runner --vcpus 1 --memory 256)
echo "${CREATE_OUT}"

VM_ID=$(echo "${CREATE_OUT}" | grep -o 'ID=vm-[a-zA-Z0-9]*' | cut -d= -f2)
echo "==> Assigned VM ID: ${VM_ID}"

echo
echo "[Step 2] Listing VMs in SQLite database..."
"${MFORGE_BIN}" list

echo
echo "[Step 3] Launching Firecracker microVM with isolated TAP network..."
"${MFORGE_BIN}" run "${VM_ID}"

echo
echo "[Step 4] Checking VM status (running)..."
"${MFORGE_BIN}" list

echo
echo "[Step 5] Executing isolated code over SSH..."
"${MFORGE_BIN}" ssh "${VM_ID}" -- 'uname -a && echo "Compute Job Finished at $(date)" > /root/job_output.txt && cat /root/job_output.txt'

echo
echo "[Step 6] Saving VM execution state and RAM snapshot..."
"${MFORGE_BIN}" save-state "${VM_ID}"

echo
echo "[Step 7] Checking VM status (paused)..."
"${MFORGE_BIN}" list

echo
echo "[Step 8] Resuming VM from snapshot state..."
"${MFORGE_BIN}" resume "${VM_ID}"

echo
echo "[Step 9] Verifying memory and filesystem persistence after resume..."
"${MFORGE_BIN}" ssh "${VM_ID}" -- 'cat /root/job_output.txt && uptime'

echo
echo "[Step 10] Stopping the microVM gracefully..."
"${MFORGE_BIN}" stop "${VM_ID}"

echo
echo "[Step 11] Checking VM status (stopped)..."
"${MFORGE_BIN}" list

echo
echo "[Step 12] Deleting VM and freeing network/storage resources..."
"${MFORGE_BIN}" delete "${VM_ID}"

echo
echo "[Step 13] Final VM list:"
"${MFORGE_BIN}" list

echo
echo "=========================================================="
echo "  End-to-End Demo Completed Successfully!                 "
echo "=========================================================="
