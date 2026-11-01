#!/usr/bin/env bash
set -euo pipefail

COMPUTE_BIN="${1:-./bin/compute}"

echo "=========================================================="
echo "  Firecracker On-Demand VM Orchestrator End-to-End Demo  "
echo "=========================================================="

echo
echo "[Step 1] Creating a new Firecracker on-demand VM..."
CREATE_OUT=$("${COMPUTE_BIN}" sandbox create --name demo-runner --vcpus 1 --memory 512)
echo "${CREATE_OUT}"

VM_ID=$(echo "${CREATE_OUT}" | grep -o 'ID=vm-[a-zA-Z0-9]*' | cut -d= -f2)
echo "==> Assigned VM ID: ${VM_ID}"

echo
echo "[Step 2] Listing VMs in SQLite database..."
"${COMPUTE_BIN}" sandbox list

echo
echo "[Step 3] Launching Firecracker microVM with isolated TAP network..."
"${COMPUTE_BIN}" sandbox start "${VM_ID}"

echo
echo "[Step 4] Checking VM status (running)..."
"${COMPUTE_BIN}" sandbox list

echo
echo "[Step 5] Executing isolated code over SSH (non-root compute user & serverless runtimes)..."
"${COMPUTE_BIN}" sandbox ssh "${VM_ID}" -- 'whoami && hostname && node -e "console.log(\"Node.js serverless compute working:\", 2 + 2)"'
"${COMPUTE_BIN}" sandbox ssh "${VM_ID}" -- 'python3 -c "import sys; print(\"Python serverless compute working:\", sys.version.split()[0])"'
"${COMPUTE_BIN}" sandbox ssh "${VM_ID}" -- 'sudo id && echo "Compute Job Finished at $(date)" > /home/compute/job_output.txt && cat /home/compute/job_output.txt'

echo
echo "[Step 6] Saving VM execution state and RAM snapshot..."
"${COMPUTE_BIN}" sandbox save-state "${VM_ID}"

echo
echo "[Step 7] Checking VM status (paused)..."
"${COMPUTE_BIN}" sandbox list

echo
echo "[Step 8] Resuming VM from snapshot state..."
"${COMPUTE_BIN}" sandbox resume "${VM_ID}"

echo
echo "[Step 9] Verifying memory and filesystem persistence after resume..."
"${COMPUTE_BIN}" sandbox ssh "${VM_ID}" -- 'cat /home/compute/job_output.txt && uptime'

echo
echo "[Step 10] Stopping the microVM gracefully..."
"${COMPUTE_BIN}" sandbox stop "${VM_ID}"

echo
echo "[Step 11] Checking VM status (stopped)..."
"${COMPUTE_BIN}" sandbox list

echo
echo "[Step 12] Deleting VM and freeing network/storage resources..."
"${COMPUTE_BIN}" sandbox delete "${VM_ID}"
