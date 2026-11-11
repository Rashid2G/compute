---
name: compute
description: >
  Manage isolated Firecracker microVM sandboxes, distributed compute jobs, and ephemeral per-pull-request preview
  deployments across any repository using the compute platform. Grounded in the native compute CLI and REST APIs.
  Triggers when the user mentions "compute", "sandbox", "microVM", "Firecracker", "isolated build", "distributed job",
  "preview deployment", "ephemeral environment", "per-PR preview", "dogfood compute", or needs isolated, safe code execution.
---

# Compute Platform Skill

A unified, project-agnostic operational skill for utilizing the `compute` platform (`on-demand-vms`). It empowers AI agents to provision lightweight, isolated Firecracker microVM sandboxes on demand, run ephemeral distributed build/test jobs with file synchronization, host live per-pull-request preview deployments with automatic ingress routing, and recursively dogfood/develop `compute` itself without host collisions.

---

## Architecture & Execution Model Decision Matrix

`compute` provides two core operational primitives. Choose the right primitive for the task:

| Requirement | Primitive | Command / Interface | Typical Use Cases |
| :--- | :--- | :--- | :--- |
| **Ephemeral Build / Test / Lint** | **Job (`compute job`)** | `compute job run --files ... -- <cmd>` | CI/CD steps, compiling binaries, unit tests, linters, static analysis, artifact packaging. No persistent VM lifecycle needed. |
| **Interactive / Long-Running / Multi-Step** | **Sandbox (`compute sandbox`)** | `compute sandbox create` & `exec` | Multi-step debugging, iterative compilation, background processes, SSH sessions, state checkpoints (`save-state`/`resume`). |
| **Live Web App / PR Preview** | **Sandbox + Ingress** | `compute sandbox create` + Ingress Proxy | Per-branch/PR live previews, public demos, webhook testing, full-stack dev environments with HTTP/WebSocket/SSE routing. |
| **Dogfooding / Recursion** | **Isolated Sandbox** | `compute sandbox create` (nested KVM/daemon) | Developing `computed`, testing CLI features, running feature test suites inside a guest microVM without host conflicts. |

```
                       +-----------------------------------+
                       |        AI Agent / Caller          |
                       +-----------------+-----------------+
                                         |
                +------------------------+------------------------+
                |                                                 |
                v                                                 v
   [Ephemeral One-Off Task?]                           [Stateful / Service / Preview?]
                |                                                 |
                v                                                 v
       +-----------------+                              +-------------------+
       |   compute job   |                              |  compute sandbox  |
       |  (tar.gz sync,  |                              | (dedicated disk,  |
       |  run, stream,   |                              |  exec, live HTTP/ |
       |  auto-teardown) |                              |  WS preview, IP)  |
       +-----------------+                              +---------+---------+
                                                                  |
                                              +-------------------+-------------------+
                                              |                                       |
                                              v                                       v
                                    [Interactive / Debug]                   [PR Preview Deployment]
                                    `compute sandbox exec`                 Ingress Reverse Proxy:
                                    `compute sandbox ssh`                  - Subdomain: `<port>-<vm>.<domain>`
                                    `save-state` / `resume`                - Path: `/preview/<vm>/<port>/`
```

---

## 1. Environment Discovery & Authentication

Before executing workloads, verify the daemon connection and configure authentication credentials.

### Discovery & Health Checks
The `compute` CLI and client connect to the control plane daemon determined by environment variables:
- `COMPUTE_DAEMON` (or `COMPUTE_HOST`): Host address and port (default: `127.0.0.1:18080`).
- Remote deployment example: `export COMPUTE_DAEMON="192.168.1.70:18080"` (for `compute-lab`).
- Unix socket example: `export COMPUTE_DAEMON="unix:///var/run/computed.sock"`.

**Verify Daemon Health:**
```bash
# Direct HTTP health probe
curl -sSf "http://${COMPUTE_DAEMON:-127.0.0.1:18080}/api/v1/health"
# Expected response: {"status":"ok","time":"..."}

# CLI version check
compute version
# Or query client-only if daemon is offline:
compute version --client
```

### Authentication Credentials
The platform implements multi-tenant authentication with RBAC and API keys:
1. **API Key via Environment**: `export COMPUTE_API_KEY="cmp_live_..."` (highest precedence in scripts/CI).
2. **Local User Config**: `~/.compute/config.json` (auto-populated by `compute auth login`).
3. **Session Tokens**: Bearer tokens passed via HTTP `Authorization: Bearer <token>` or `X-API-Key: <key>`.

**Auth Commands:**
```bash
# Check current authenticated user and organization
compute auth whoami

# Authenticate with credentials (stores machine API key in ~/.compute/config.json)
compute auth login --username <username-or-email> --password <secret-password>

# Create a long-lived machine API key for CI/CD or agent automation
compute auth keys create --name "ci-agent-token"

# List and inspect existing API keys
compute auth keys list

# Revoke an API key
compute auth keys revoke <key-id>

# Log out (clears local token)
compute auth logout
```

---

## 2. Isolated Builds & Sandboxes

### Pattern A: Ephemeral Job Execution (`compute job`)
For one-off builds, test suites, or compilation tasks that should execute in total isolation without leaving persistent VM residue.

```bash
compute job run [flags] -- <command...>
```

#### Core Flags
- `--files <path>`: Local file or directory path. Automatically packaged into a gzip-compressed tar archive (`tar.gz` in base64), transferred to the sandbox, and extracted in `--cwd`.
- `--cwd <path>`: Execution working directory inside the guest microVM (default: `/workspace`).
- `--image <name>`: Rootfs image name (default: daemon default, e.g., Ubuntu base with Node 20, Python 3, Git).
- `--vcpus <n>`: Allocated vCPU cores (default: 2, options: 1, 2, 4).
- `--mem <MiB>`: Memory size in MiB (default: 2048).
- `--timeout <seconds>`: Timeout in seconds (default: 0 = no timeout).
- `--follow`: Stream stdout/stderr in real time (default: `true`). Set to `false` for asynchronous execution.

#### Complete Job Example
```bash
# Package the current repository, build Go binaries, and run tests inside an isolated sandbox
compute job run \
  --vcpus 4 \
  --mem 4096 \
  --timeout 600 \
  --files . \
  --cwd /workspace \
  -- make build test
```

#### Async Job Management
```bash
# Submit job asynchronously
JOB_OUT=$(compute job run --follow=false --files . -- npm test)
JOB_ID=$(echo "${JOB_OUT}" | grep -o 'ID=[a-zA-Z0-9_-]*' | cut -d= -f2)

# Inspect job state (queued, running, succeeded, failed, canceled)
compute job info "${JOB_ID}"

# Stream logs
compute job logs "${JOB_ID}"

# Cancel job if hung
compute job cancel "${JOB_ID}"
```

---

### Pattern B: Stateful Sandboxes (`compute sandbox`)
Use when you need a persistent filesystem across multiple commands, daemon processes, interactive debugging, or snapshotting.

#### 1. Lifecycle Operations
```bash
# Provision a sandbox with custom specs
compute sandbox create \
  --name "build-runner-01" \
  --tier "2vcpu" \
  --memory 2048 \
  --disk 10240

# Start the microVM (spawns Firecracker process, attaches TAP network, boots guest)
compute sandbox start build-runner-01

# Inspect status and assigned private IP
compute sandbox info build-runner-01

# Execute commands inside the guest (non-root 'compute' user with passwordless sudo)
compute sandbox exec build-runner-01 -- uname -a

# Execute with JSON buffered output and custom timeout
compute sandbox exec --json --timeout 30 build-runner-01 -- git status

# Run as root user
compute sandbox exec --user root build-runner-01 -- apt-get update -y

# Interactive SSH terminal over WebSocket
compute sandbox ssh build-runner-01

# Stop the sandbox (persists rootfs disk)
compute sandbox stop build-runner-01

# Restart sandbox with data intact
compute sandbox start build-runner-01

# Permanently destroy sandbox and storage
compute sandbox delete build-runner-01
```

#### 2. Checkpoints: Instant Snapshot & Resume
Freeze a running sandbox with its full in-memory process tree and resume it instantaneously:
```bash
# Save memory and CPU register state to disk (transitions status to 'paused')
compute sandbox save-state build-runner-01

# Resume execution exactly where it paused (transitions status back to 'running')
compute sandbox resume build-runner-01
```

#### 3. High-Speed File & Process API (Direct REST / SDK)
For agents operating programmatically without shell spawning:
```bash
# Write file to sandbox
curl -s -X POST "http://${COMPUTE_DAEMON:-127.0.0.1:18080}/api/v1/sandboxes/<sandbox-id>/files" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"path": "/home/compute/app.json", "content": "{\"status\":\"ok\"}", "format": "text"}'

# Read file from sandbox
curl -s "http://${COMPUTE_DAEMON:-127.0.0.1:18080}/api/v1/sandboxes/<sandbox-id>/files?path=/home/compute/app.json"

# Launch background daemon process
curl -s -X POST "http://${COMPUTE_DAEMON:-127.0.0.1:18080}/api/v1/sandboxes/<sandbox-id>/processes" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"command": "python3 -m http.server 8000", "cwd": "/home/compute"}'

# List active processes inside guest
curl -s "http://${COMPUTE_DAEMON:-127.0.0.1:18080}/api/v1/sandboxes/<sandbox-id>/processes" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}"

# Terminate process by PID
curl -s -X POST "http://${COMPUTE_DAEMON:-127.0.0.1:18080}/api/v1/sandboxes/<sandbox-id>/processes/kill" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"pid": 1234, "signal": "SIGTERM"}'
```

---

## 3. Per-Pull-Request Ephemeral Deployments

Every pull request or branch can be given an isolated, addressable live deployment with bidirectional HTTP/WebSocket reverse proxy routing.

### Ingress & Routing Mechanism
`computed` contains a built-in reverse proxy (`IngressProxy`) supporting HTTP/1.1, HTTP/2, WebSockets, and SSE. It resolves incoming requests to target guest IPs and ports using two schemes:

1. **Path-Based Routing (Works out-of-the-box on any host without DNS changes):**
   ```
   http://<daemon-host>:<port>/preview/<sandbox-name-or-id>/<guest-port>/
   http://<daemon-host>:<port>/api/v1/sandboxes/<sandbox-name-or-id>/preview/<guest-port>/
   ```
   *Example:* `http://127.0.0.1:18080/preview/pr-42-web/3000/`

2. **Subdomain-Based Routing (When wildcard DNS or `/etc/hosts` points to ingress):**
   ```
   http://<guest-port>-<sandbox-name-or-id>.<base-domain>:<port>/
   http://<sandbox-name-or-id>-<guest-port>.<base-domain>:<port>/
   ```
   *Supported default base domains:* `preview.localhost`, `compute.internal`, `compute.dev`, `localhost`.
   *Example:* `http://3000-pr-42-web.preview.localhost:18080/`

### Public vs. Private Previews
- **Private Preview (Default):** The ingress proxy requires an `Authorization: Bearer <token>` or session cookie matching the VM owner. Unauthenticated external requests receive `401 Unauthorized`.
- **Public Preview:** Expose the guest port publicly so external reviewers, clients, or automated test runners can access it without credentials:
  ```bash
  # Register public port forward rule via control-plane API
  curl -s -X POST "http://${COMPUTE_DAEMON:-127.0.0.1:18080}/api/v1/network/port-forwards" \
    -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{
      "vm_id": "pr-42-web",
      "guest_port": 3000,
      "host_port": 33000,
      "protocol": "tcp",
      "public": true
    }'
  ```

---

### Step-by-Step PR Preview Lifecycle Workflow

```
[PR Opened / Updated]
        |
        v
1. Provision Sandbox: `compute sandbox create --name pr-<n>-<slug>`
2. Boot Sandbox:      `compute sandbox start pr-<n>-<slug>`
3. Deploy Code:       `compute sandbox exec ... -- <build & start server>`
4. Mark Port Public:  `POST /api/v1/network/port-forwards` (`public: true`)
5. Post URL to PR:    `http://<daemon-host>:18080/preview/pr-<n>-<slug>/<port>/`
        |
   [PR Closed]
        v
6. Teardown:          `compute sandbox stop ... && compute sandbox delete ...`
7. Prune Network:     `compute network prune`
```

#### Step 1: Provision & Boot
```bash
PR_NUM=42
REPO_SLUG="web-client"
BOX_NAME="pr-${PR_NUM}-${REPO_SLUG}"

# Create sandbox with 2 vCPUs and 2GB RAM
compute sandbox create --name "${BOX_NAME}" --vcpus 2 --memory 2048
compute sandbox start "${BOX_NAME}"
```

#### Step 2: Deploy Project Code
Sync files and run the build inside the sandbox:
```bash
# Clone the PR branch inside the guest microVM
compute sandbox exec "${BOX_NAME}" -- bash -c "
  git clone --depth 1 -b my-feature-branch https://github.com/my-org/my-repo.git /app && \
  cd /app && \
  npm install && \
  npm run build
"

# Start application server in the background (e.g. listening on port 3000)
compute sandbox exec "${BOX_NAME}" -- bash -c "
  cd /app && nohup npm start > /var/log/app.log 2>&1 &
"
```

#### Step 3: Verify Application Health
```bash
# Poll local port inside sandbox until listening
compute sandbox exec "${BOX_NAME}" -- bash -c "
  for i in \$(seq 1 30); do
    if curl -s http://127.0.0.1:3000 >/dev/null; then
      echo 'App is ready'; exit 0
    fi
    sleep 1
  done
  echo 'App failed to start' && cat /var/log/app.log && exit 1
"
```

#### Step 4: Expose & Publish Preview URL
Make the preview publicly accessible:
```bash
curl -s -X POST "http://${COMPUTE_DAEMON:-127.0.0.1:18080}/api/v1/network/port-forwards" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
  -H "Content-Type: application/json" \
  -d "{\"vm_id\": \"${BOX_NAME}\", \"guest_port\": 3000, \"public\": true}"

# Resolve the addressable preview URL
DAEMON_HOST="${COMPUTE_DAEMON:-127.0.0.1:18080}"
PREVIEW_URL="http://${DAEMON_HOST}/preview/${BOX_NAME}/3000/"

echo "Preview Deployment ready at: ${PREVIEW_URL}"
# Post comment to GitHub PR:
# gh pr comment "${PR_NUM}" --body "Deployed preview: ${PREVIEW_URL}"
```

#### Step 5: Update Deployment on Subsequent Commits
When new commits arrive on the PR branch, update in place without re-provisioning:
```bash
compute sandbox exec "${BOX_NAME}" -- bash -c "
  cd /app && \
  git fetch origin && \
  git checkout FETCH_HEAD && \
  npm run build && \
  pkill -f 'npm start' || true && \
  nohup npm start > /var/log/app.log 2>&1 &
"
```

#### Step 6: Teardown on PR Merge / Close
When the pull request is closed or merged, reclaim host resources:
```bash
compute sandbox stop "${BOX_NAME}"
compute sandbox delete "${BOX_NAME}"
compute network prune
```

---

## 4. Lifecycle, Protection & Cleanup

### Accidental Deletion Protection
Prevent critical dev environments or long-running sandboxes from being accidentally deleted:
```bash
# Enable deletion protection
compute sandbox protect <name-or-id>

# Attempting delete will fail:
# compute sandbox delete <name-or-id> -> Error: sandbox is deletion protected

# Unprotect when ready for decommission
compute sandbox unprotect <name-or-id>
compute sandbox delete <name-or-id>
```

### Network Pruning & Stale Allocations
If host crashes or abrupt container exits leave orphaned network TAP devices or dangling routing entries:
```bash
# Inspect all network allocations and interface states
compute network list
compute network inspect <sandbox-name-or-id>

# Prune all orphaned TAP devices, stale leases, and unused interfaces
compute network prune
```

### Batch Operations with Shell-Style Pattern Matching
Clean up groups of ephemeral sandboxes safely by quoting wildcards:
```bash
# Stop all PR preview environments
compute sandbox stop 'pr-*'

# Delete all completed feature test sandboxes
compute sandbox delete 'feat-test-*'

# Stop all sandboxes matching a prefix
compute sandbox stop 'dev-*'
```

---

## 5. Recursion & Dogfooding (`compute` within `compute`)

When developing `compute` itself (`on-demand-vms`), running build, test, and feature verification tasks inside a `compute` sandbox eliminates host contamination and prevents port/network collisions.

### Isolation Guarantees
1. **Network Space**: Each guest microVM receives an isolated private IP on a dedicated bridge/TAP network (e.g. `172.16.0.2`). Guest port `18080` does NOT collide with host port `18080`.
2. **Filesystem Space**: The guest operates on its own dedicated ext4 rootfs disk image.
3. **Process Space**: Firecracker runs in the guest's own kernel space with isolated cgroups.

### Dogfooding Procedure

```bash
# 1. Provision a sandbox with adequate resources for Go & web compilation
compute sandbox create --name "compute-dogfood" --vcpus 4 --memory 4096 --disk 20480
compute sandbox start compute-dogfood

# 2. Sync local on-demand-vms source code into guest workspace
# Using job or direct rsync/git:
compute sandbox exec compute-dogfood -- bash -c "
  sudo apt-get update -y && \
  sudo apt-get install -y golang make git nodejs npm && \
  git clone https://github.com/Rashid2G/on-demand-vms.git /home/compute/on-demand-vms
"

# 3. Build compute binaries inside the sandbox
compute sandbox exec compute-dogfood -- bash -c "
  cd /home/compute/on-demand-vms && \
  make build
"

# 4. Run unit and subsystem test suites
compute sandbox exec compute-dogfood -- bash -c "
  cd /home/compute/on-demand-vms && \
  go test -v ./pkg/... ./internal/network/... ./cmd/compute/...
"

# 5. Start a nested computed daemon on guest port 18080
compute sandbox exec compute-dogfood -- bash -c "
  cd /home/compute/on-demand-vms && \
  nohup ./bin/computed --listen 0.0.0.0:18080 > /tmp/nested-computed.log 2>&1 &
"

# 6. Access nested daemon or GUI from host via Ingress Proxy
# Host preview route: http://127.0.0.1:18080/preview/compute-dogfood/18080/api/v1/health
curl -s "http://${COMPUTE_DAEMON:-127.0.0.1:18080}/preview/compute-dogfood/18080/api/v1/health"

# 7. Teardown dogfood sandbox when tests finish
compute sandbox stop compute-dogfood
compute sandbox delete compute-dogfood
```

---

## 6. Command Cheat Sheet & Troubleshooting

### CLI Quick Reference
| Task | Command |
| :--- | :--- |
| **Verify status** | `compute version` & `compute auth whoami` |
| **Run build job** | `compute job run --files . -- <build-cmd>` |
| **List jobs** | `compute job list` |
| **Inspect job** | `compute job info <id>` |
| **Create sandbox** | `compute sandbox create --name <name> --tier 2vcpu --memory 2048` |
| **Boot sandbox** | `compute sandbox start <name>` |
| **Run command** | `compute sandbox exec <name> -- <cmd>` |
| **Run root command** | `compute sandbox exec --user root <name> -- <cmd>` |
| **SSH terminal** | `compute sandbox ssh <name>` |
| **Checkpoint state** | `compute sandbox save-state <name>` |
| **Restore state** | `compute sandbox resume <name>` |
| **Protect sandbox** | `compute sandbox protect <name>` |
| **Clean network** | `compute network prune` |
| **Delete sandbox** | `compute sandbox delete <name>` |

### Common Failure Modes & Solutions

| Error / Symptom | Root Cause | Remediation |
| :--- | :--- | :--- |
| `connection to daemon failed: dial tcp 127.0.0.1:18080: connect: connection refused` | `computed` daemon is not running or running on a different port/host. | Check `COMPUTE_DAEMON` env variable. Start daemon with `sudo ./bin/computed` or verify remote host IP (`192.168.1.70:18080`). |
| `unauthorized: private sandbox preview requires authentication` | Attempting to access preview URL without API key or auth token. | Pass `Authorization: Bearer <token>` header, or register public port forward via `POST /api/v1/network/port-forwards` with `"public": true`. |
| `sandbox is deletion protected` | Protection flag is enabled on sandbox. | Run `compute sandbox unprotect <name>` before calling delete. |
| `failed to allocate IP: IP pool exhausted` | Stale or unreleased network allocations from aborted microVMs. | Run `compute network prune` to clean orphaned leases and TAP devices. |
| `sandbox not running (status: stopped)` | Commands routed to a stopped or paused sandbox. | Run `compute sandbox start <name>` (or `compute sandbox resume <name>`) before executing commands or accessing preview URLs. |
| `job timeout exceeded` | Build or test command ran longer than specified `--timeout`. | Increase `--timeout <sec>` or investigate stalled build commands in job logs (`compute job logs <id>`). |

---

## 7. Additional References
Detailed references are available in the `references/` directory:
- [CLI Reference](./references/cli-reference.md): Full flag specifications and command semantics.
- [API Reference](./references/api-reference.md): REST endpoints, WebSocket contracts, and JSON payload definitions.
- [Preview Recipes](./references/preview-recipes.md): End-to-end setup recipes for Node.js, Go, Python, and static site PR previews.
