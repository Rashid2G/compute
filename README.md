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

### 1. Install on a fresh host with Docker Compose

Requirements: Linux with Docker Engine 24+ (rootful), Compose v2.20+, and
`/dev/kvm`. See [docs/install.md](docs/install.md) for the full guide.

```bash
cp .env.example .env
chmod 600 .env
# set COMPUTE_ADMIN_PASSWORD (and COMPUTE_DOMAIN if you want DNS names)
./scripts/preflight.sh
sudo ./scripts/host-prepare.sh
docker compose up -d

# Liveness and readiness (readiness returns 503 with remediation when unready)
curl -fsS http://127.0.0.1:18080/api/v1/health
curl -fsS http://127.0.0.1:18080/api/v1/ready | python3 -m json.tool
./bin/compute doctor
```

The default install starts only the daemon (API plus embedded GUI on
`:18080`). Base images, the internal SSH key, DNS, a separate GUI container,
Tailscale, and multi-host workers are configured through `.env` and optional
profiles. Every variable is documented in
[docs/configuration.md](docs/configuration.md).

### 2. Enroll a Remote Worker Node (experimental, optional)

The single-host install runs sandboxes on the control plane and does not need a
worker. To add one:

```bash
# 1. On your machine with the compute CLI:
compute worker token create --name node-1

# 2. Put the printed token in .env as COMPUTE_WORKER_TOKEN, then:
docker compose --profile worker up -d
```

A worker with the same `COMPUTE_WORKER_NAME` and token reconnects safely after
a restart. Workers require their own `/dev/kvm`, host networking, and cgroups.

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

# Optional: allow SSH password login for compute/root (omit for public-key-only)
compute sandbox create --name pw-box --ssh-password 'choose-a-strong-password'

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

## Deployment & Testing on `compute-lab`
```bash
# Fast containerized deployment with Docker Compose (default)
make deploy

# Deploy including Web UI rebuild
make deploy-ui

# Check status of remote containers, services, and endpoints
make remote-status

# Deploy using legacy systemd host units
./scripts/deploy.sh --systemd
```

See [AGENTS.md](file:///home/rashid/Projects/Repos/on-demand-vms/AGENTS.md) for automated workflows and agent guidance.

A dedicated VM (`compute-lab`) serves the full stack on the LAN:

- Web GUI: `http://compute-lab:8080`
- Daemon API: `http://compute-lab:18080/api/v1/health`
- Remote CLI: `COMPUTE_DAEMON=compute-lab:18080 ./bin/compute sandbox list`
- SSH Access: `ssh compute-lab`

The `compute-lab` host alias and its address are configured in `~/.ssh/config`;
see [AGENTS.md](AGENTS.md) for deployment guidance.
