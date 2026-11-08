# Compute v2: Distributed On-Demand MicroVM Platform

Compute is a distributed compute platform for orchestrating lightning-fast Firecracker microVMs across customer-owned host machines, private clusters, and local developer environments.

---

## Key Features

1. **Boat.dev-Style Developer CLI (`compute`)**
   - Manage sandboxes with clean subcommands: `compute sandbox create`, `compute sandbox list`, `compute sandbox ssh`, `compute sandbox exec`.
   - Run distributed tasks with one command: `compute job run --image <img|env> --files <dir> -- <command>`.
   - Real-time live log streaming and automated workspace synchronization.

2. **Central Daemon Relay (`computed`)**
   - Central control plane managing user state, worker registry, quotas, and scheduling.
   - Workers establish **outbound-only** WebSocket/mTLS streams (`/api/v1/worker/connect`), traversing NATs and corporate firewalls with **zero open inbound ports**.
   - Multiplexes job dispatch, log streaming, and relayed SSH/terminal tunnels into worker microVMs.

3. **Autonomous Worker Client (`compute-worker`)**
   - Lightweight host agent running on any Linux machine with `/dev/kvm`.
   - Local admission control and capability reporting (vCPUs, RAM, disk, arch).
   - Unpacks synced file archives directly into the workspace before executing job commands.

4. **Self-Hosting with Docker & Docker Compose**
   - Full cluster deployable with `docker compose up -d`.
   - Production-ready multi-stage Dockerfiles for daemon (`Dockerfile.daemon`) and worker client (`Dockerfile.worker`).

---

## Quickstart

### 1. Launch Central Daemon & Worker via Docker Compose

```bash
# Start both computed and compute-worker
docker compose up -d

# Check cluster health
curl -f http://localhost:18080/api/v1/health
```

### 2. Enroll a Remote Worker Node

```bash
# 1. On your machine with compute CLI:
compute worker token create --pool default

# Output will provide the one-time enrollment token and run command:
# Run on the worker node:
compute-worker run --control-plane http://<daemon-ip>:18080 --token <token> --name node-1
```

### 3. Run Compute Tasks (Agent Workflow)

```bash
# Kick off a task with input files and live log follow:
compute job run --image alpine --files ./src --cwd /workspace -- python3 test.py

# List jobs
compute job list

# Check job logs
compute job logs <job_id>
```

Jobs are dispatched to connected worker nodes. When no worker is connected,
the daemon falls back to running the job in a local Firecracker sandbox on the
control-plane host (`jobs.local_execution` in the daemon config), so
`compute job run` never leaves submissions stuck in the queue.

### 4. Interactive Sandboxes (Boat.dev Style)

```bash
# Create and start a persistent sandbox
compute sandbox create --name dev-box --tier 2vcpu --image alpine
compute sandbox start dev-box

# Interactive terminal
compute sandbox ssh dev-box

# Execute command inside sandbox (stdout/stderr stream live)
compute sandbox exec dev-box -- uname -a

# Buffer the output and print it when the command finishes
compute sandbox exec --no-stream dev-box -- uname -a

# Delete sandbox
compute sandbox delete dev-box
```

---

## Deployment & Testing on `t3-runner`
```bash
# Standard deploy (compiles Go binaries, syncs files, restarts services)
make deploy

# Deploy including Web UI rebuild
make deploy-ui

# Check status and logs on remote VM
make remote-status
```

See [AGENTS.md](file:///home/rashid/Projects/Repos/on-demand-vms/AGENTS.md) for automated workflows and agent guidance.

## Local Lab Deployment (`compute-lab`)

A dedicated VM serves the full stack on the LAN:

- Web GUI: http://192.168.1.70:8080
- Daemon API: http://192.168.1.70:18080/api/v1/health
- Remote CLI: `COMPUTE_DAEMON=192.168.1.70:18080 ./bin/compute sandbox list`

Deploy with `make deploy-lab` (or `make deploy-lab-ui` when the frontend changed) and
check health with `make remote-status-lab`.
