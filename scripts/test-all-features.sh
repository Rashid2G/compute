#!/usr/bin/env bash
set -euo pipefail

MFORGE_BIN="${1:-./bin/mforge}"
DAEMON_ADDR="${2:-127.0.0.1:18080}"

export MFORGE_DAEMON="${DAEMON_ADDR}"

echo "=================================================================="
echo "    COMPREHENSIVE FIRECRACKER ON-DEMAND VM FEATURE TEST SUITE     "
echo "=================================================================="

# Helper function for assertions
assert_eq() {
    local expected="$1"
    local actual="$2"
    local msg="$3"
    if [ "${expected}" != "${actual}" ]; then
        echo "[-] ASSERTION FAILED: ${msg}"
        echo "    Expected: '${expected}'"
        echo "    Actual:   '${actual}'"
        exit 1
    fi
    echo "[+] PASS: ${msg}"
}

assert_contains() {
    local haystack="$1"
    local needle="$2"
    local msg="$3"
    if [[ "${haystack}" != *"${needle}"* ]]; then
        echo "[-] ASSERTION FAILED: ${msg}"
        echo "    String '${needle}' not found in output:"
        echo "${haystack}"
        exit 1
    fi
    echo "[+] PASS: ${msg}"
}

# -------------------------------------------------------------------------
# Test 1: Daemon Health Check
# -------------------------------------------------------------------------
echo
echo "--> [Test 1] Testing Daemon Health Endpoint..."
HEALTH_RESP=$(curl -s "http://${DAEMON_ADDR}/api/v1/health")
assert_contains "${HEALTH_RESP}" '"status":"ok"' "Daemon reports healthy status"

# -------------------------------------------------------------------------
# Test 2: VM Provisioning with Custom Specs
# -------------------------------------------------------------------------
echo
echo "--> [Test 2] Provisioning VM with custom specs (2 vCPUs, 512MB RAM)..."
CREATE_OUT=$("${MFORGE_BIN}" create --name feat-test-runner --vcpus 2 --memory 512)
echo "${CREATE_OUT}"
assert_contains "${CREATE_OUT}" "VM Created" "CLI reports VM created"

VM_ID=$(echo "${CREATE_OUT}" | grep -o 'ID=vm-[a-zA-Z0-9]*' | cut -d= -f2)
VM_IP=$(echo "${CREATE_OUT}" | grep -o 'IP=[0-9.]*' | cut -d= -f2)
echo "    Allocated VM ID: ${VM_ID}, IP: ${VM_IP}"

# Verify initial status in list
LIST_OUT=$("${MFORGE_BIN}" list)
assert_contains "${LIST_OUT}" "${VM_ID}" "VM appears in list"
assert_contains "${LIST_OUT}" "created" "VM initial status is 'created'"

# -------------------------------------------------------------------------
# Test 3: MicroVM Launch & TAP Network Attachment
# -------------------------------------------------------------------------
echo
echo "--> [Test 3] Launching Firecracker microVM..."
RUN_OUT=$("${MFORGE_BIN}" run "${VM_ID}")
echo "${RUN_OUT}"
assert_contains "${RUN_OUT}" "VM Started" "CLI reports VM started"
assert_contains "${RUN_OUT}" "Status=running" "Status is updated to running"

VM_PID=$(echo "${RUN_OUT}" | grep -o 'PID=[0-9]*' | cut -d= -f2)
if [ -z "${VM_PID}" ] || [ "${VM_PID}" -le 0 ]; then
    echo "[-] FAILED: Invalid PID: ${VM_PID}"
    exit 1
fi
echo "[+] PASS: Firecracker process running with PID ${VM_PID}"

# Ping VM from host
echo "    Testing ICMP reachability to guest IP ${VM_IP}..."
ping -c 2 "${VM_IP}" >/dev/null 2>&1
echo "[+] PASS: Host successfully pinged guest IP ${VM_IP}"

# -------------------------------------------------------------------------
# Test 4: SSH Connectivity, Non-Root User, Passwordless Sudo & Serverless Tools
# -------------------------------------------------------------------------
echo
echo "--> [Test 4] Testing SSH connectivity and non-root execution..."
SSH_OUT=$("${MFORGE_BIN}" ssh "${VM_ID}" -- 'uname -s && nproc')
echo "${SSH_OUT}"
assert_contains "${SSH_OUT}" "Linux" "Guest OS is Linux"
assert_contains "${SSH_OUT}" "2" "Guest detects 2 configured vCPUs"

echo "    Verifying default non-root user 'mforge'..."
USER_OUT=$("${MFORGE_BIN}" ssh "${VM_ID}" -- 'whoami')
assert_eq "mforge" "${USER_OUT}" "Default SSH user is non-root mforge"

echo "    Verifying passwordless sudo execution..."
SUDO_OUT=$("${MFORGE_BIN}" ssh "${VM_ID}" -- 'sudo whoami')
assert_eq "root" "${SUDO_OUT}" "mforge has passwordless sudo access"

echo "    Verifying guest hostname configuration..."
HOST_OUT=$("${MFORGE_BIN}" ssh "${VM_ID}" -- 'hostname')
assert_eq "${VM_ID}" "${HOST_OUT}" "Guest hostname reflects VM ID"

echo "    Verifying standard serverless runtimes and utilities..."
NODE_OUT=$("${MFORGE_BIN}" ssh "${VM_ID}" -- 'node -v')
assert_contains "${NODE_OUT}" "v20" "Node.js runtime is installed"

PY_OUT=$("${MFORGE_BIN}" ssh "${VM_ID}" -- 'python3 --version')
assert_contains "${PY_OUT}" "Python 3" "Python 3 runtime is installed"

JQ_OUT=$("${MFORGE_BIN}" ssh "${VM_ID}" -- 'jq --version')
assert_contains "${JQ_OUT}" "jq-" "jq JSON processor is installed"

GIT_OUT=$("${MFORGE_BIN}" ssh "${VM_ID}" -- 'git --version')
assert_contains "${GIT_OUT}" "git version" "git is installed"

CURL_OUT=$("${MFORGE_BIN}" ssh "${VM_ID}" -- 'curl --version')
assert_contains "${CURL_OUT}" "curl" "curl is installed"

# -------------------------------------------------------------------------
# Test 5: Outbound Network & Virtual NAT Access
# -------------------------------------------------------------------------
echo
echo "--> [Test 5] Testing guest outbound internet access over virtual NAT..."
NAT_OUT=$("${MFORGE_BIN}" ssh "${VM_ID}" -- 'sudo ping -c 2 1.1.1.1')
assert_contains "${NAT_OUT}" "0% packet loss" "Guest reached public internet (1.1.1.1) via NAT"

# -------------------------------------------------------------------------
# Test 6: Disk Persistence Across Stop & Restart
# -------------------------------------------------------------------------
echo
echo "--> [Test 6] Testing disk write persistence..."
UNIQUE_TOKEN="test-token-$(date +%s)-$RANDOM"
"${MFORGE_BIN}" ssh "${VM_ID}" -- "echo '${UNIQUE_TOKEN}' > /home/mforge/persist_token.txt && sync"
echo "    Wrote token to /home/mforge/persist_token.txt: ${UNIQUE_TOKEN}"

echo "    Stopping VM..."
STOP_OUT=$("${MFORGE_BIN}" stop "${VM_ID}")
assert_contains "${STOP_OUT}" "Status=stopped" "VM stopped successfully"

echo "    Restarting VM from persistent disk..."
"${MFORGE_BIN}" run "${VM_ID}"
sleep 1

READ_TOKEN=$("${MFORGE_BIN}" ssh "${VM_ID}" -- 'cat /home/mforge/persist_token.txt')
assert_eq "${UNIQUE_TOKEN}" "${READ_TOKEN}" "Persisted disk preserved data across reboot cycle"

# -------------------------------------------------------------------------
# Test 7: Save State (MicroVM Snapshot & Memory Dump)
# -------------------------------------------------------------------------
echo
echo "--> [Test 7] Testing microVM state snapshot..."
# Write an in-memory marker
"${MFORGE_BIN}" ssh "${VM_ID}" -- "echo 'in-memory-state' > /home/mforge/mem_marker.txt"

SAVE_OUT=$("${MFORGE_BIN}" save-state "${VM_ID}")
echo "${SAVE_OUT}"
assert_contains "${SAVE_OUT}" "Status=paused" "Status is updated to paused"
assert_contains "${SAVE_OUT}" "Snapshot=" "Snapshot file created"

PAUSED_LIST=$("${MFORGE_BIN}" list)
assert_contains "${PAUSED_LIST}" "paused" "VM listed as paused"

# -------------------------------------------------------------------------
# Test 8: Instant Resume from State
# -------------------------------------------------------------------------
echo
echo "--> [Test 8] Resuming microVM from snapshot..."
RESUME_OUT=$("${MFORGE_BIN}" resume "${VM_ID}")
echo "${RESUME_OUT}"
assert_contains "${RESUME_OUT}" "Status=running" "VM resumed to running status"

# Verify in-memory state remained intact
MEM_READ=$("${MFORGE_BIN}" ssh "${VM_ID}" -- 'cat /home/mforge/mem_marker.txt')
assert_eq "in-memory-state" "${MEM_READ}" "In-memory state preserved across snapshot and resume"

# -------------------------------------------------------------------------
# Test 9: Graceful Stop & Teardown
# -------------------------------------------------------------------------
echo
echo "--> [Test 9] Graceful stop and resource cleanup..."
"${MFORGE_BIN}" stop "${VM_ID}"

DEL_OUT=$("${MFORGE_BIN}" delete "${VM_ID}")
assert_contains "${DEL_OUT}" "VM Deleted" "VM deletion succeeded"

FINAL_LIST=$("${MFORGE_BIN}" list)
if [[ "${FINAL_LIST}" == *"${VM_ID}"* ]]; then
    echo "[-] FAILED: VM ID ${VM_ID} still present in database after delete!"
    exit 1
fi
echo "[+] PASS: VM record completely removed from database"

echo
echo "=================================================================="
echo "    ALL 9 FEATURE INTEGRATION TESTS PASSED WITH 100% SUCCESS!     "
echo "=================================================================="
