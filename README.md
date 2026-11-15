# Compute v1.0.0 — Self-Hosted Firecracker MicroVM Sandbox Platform

Compute is an open-source, self-hosted platform for launching and managing hardware-isolated **Firecracker microVM sandboxes** and **distributed compute jobs** on your own Linux hardware.

Launch sub-second microVMs, **replicate (clone)** fleets with custom resource overrides, **pause and resume** live memory/CPU state, capture reusable **snapshots and templates**, stream interactive terminals and live web previews, and dispatch ephemeral distributed jobs — all from a unified **Web Dashboard**, **`compute` CLI**, **REST/WebSocket API**, and **Python/TypeScript SDKs**.

- **Documentation Site**: [docs/index.html](docs/index.html) (published via GitHub Pages)
- **Configuration Reference**: [docs/configuration.md](docs/configuration.md)
- **Host Installation Guide**: [docs/install.md](docs/install.md)
- **Security Policy**: [SECURITY.md](SECURITY.md)
- **Contributing**: [CONTRIBUTING.md](CONTRIBUTING.md)

---

## Architecture Overview

- **`computed` (Control Plane & Embedded Web UI)**: Manages users, sessions, API keys, microVM lifecycle, snapshots/templates, network bridges/NAT/iptables, authoritative DNS, HTTP ingress preview proxy, and outbound worker relays.
- **`compute` (CLI & Standalone GUI)**: Ergonomic developer CLI (`compute sandbox`, `compute template`, `compute job`, `compute auth`, `compute keys`, `compute scp`) plus optional standalone GUI server (`compute gui`).
- **`compute-worker` (Distributed Worker Agent)**: Connects outbound over WebSocket/mTLS to `computed` with zero open inbound ports required on worker hosts, executing distributed jobs inside Firecracker microVMs.

---

## Quickstart (Docker Compose)

**Host dependencies** (the daemon container bundles Firecracker, Jailer, and image-building tools, so **you do NOT need `firecracker` installed on the host**):

- Linux (`x86_64` or `arm64`) with `/dev/kvm` and cgroup v2
- Rootful Docker Engine 24+ and Docker Compose v2.20+ (<https://docs.docker.com/engine/install/>)
- Standard kernel networking support (`tun`, `bridge`, `br_netfilter`; `vhost_net` optional)
- Outbound HTTPS on first run (to install the pinned Firecracker kernel and build the Ubuntu 24.04 rootfs automatically inside `compute-init`)

1. **Start the stack** (no `.env` or manual image build required):

   ```bash
   docker compose up -d
   ```

   On first startup, `compute-init` automatically downloads and SHA-256-verifies the Firecracker Linux kernel (`vmlinux`), builds the Ubuntu 24.04 ext4 rootfs (`ubuntu-24.04.ext4`) inside the container, and generates the internal Ed25519 SSH key under `./data/images`. Subsequent restarts reuse those artifacts and boot in seconds.

2. **Verify health & readiness**:

   ```bash
   curl -fsS http://127.0.0.1:18080/api/v1/health
   curl -fsS http://127.0.0.1:18080/api/v1/ready
   ```

   Open the Web Dashboard at `http://127.0.0.1:18080/` and sign in with the default loopback credentials (`admin` / `admin`). The optional `gui` profile adds a second listener on `:8080`; it is disabled by default.

3. **Optional: host check & `.env` customization**

   To audit host readiness ahead of time or customize ports, bind address, DNS, or credentials:

   ```bash
   ./scripts/preflight.sh
   cp .env.example .env && chmod 600 .env
   ```

   > **Security note**: Before setting `COMPUTE_BIND_ADDRESS=0.0.0.0` (or any non-loopback address), set a strong `COMPUTE_ADMIN_PASSWORD` / `COMPUTE_ADMIN_SECRET` in `.env` and keep `COMPUTE_SIGNUP_ENABLED=false`. See [docs/install.md](docs/install.md) for production hardening.

---

## Pre-Built Binaries (GitHub Releases)

Pre-compiled, standalone release binaries for Linux (`amd64` and `arm64`) with SHA-256 checksums are published with every tagged release on [GitHub Releases](https://github.com/Rashid2G/compute/releases).

### Direct Download Links

| Component | Architecture | Latest Download URL |
| :--- | :--- | :--- |
| **`compute` CLI** | `linux/amd64` | [`compute_1.0.0_linux_amd64.tar.gz`](https://github.com/Rashid2G/compute/releases/latest/download/compute_1.0.0_linux_amd64.tar.gz) |
| **`compute` CLI** | `linux/arm64` | [`compute_1.0.0_linux_arm64.tar.gz`](https://github.com/Rashid2G/compute/releases/latest/download/compute_1.0.0_linux_arm64.tar.gz) |
| **`computed` Daemon** | `linux/amd64` | [`computed_1.0.0_linux_amd64.tar.gz`](https://github.com/Rashid2G/compute/releases/latest/download/computed_1.0.0_linux_amd64.tar.gz) |
| **`computed` Daemon** | `linux/arm64` | [`computed_1.0.0_linux_arm64.tar.gz`](https://github.com/Rashid2G/compute/releases/latest/download/computed_1.0.0_linux_arm64.tar.gz) |
| **Full Suite** | `linux/amd64` | [`compute-suite_1.0.0_linux_amd64.tar.gz`](https://github.com/Rashid2G/compute/releases/latest/download/compute-suite_1.0.0_linux_amd64.tar.gz) |
| **Full Suite** | `linux/arm64` | [`compute-suite_1.0.0_linux_arm64.tar.gz`](https://github.com/Rashid2G/compute/releases/latest/download/compute-suite_1.0.0_linux_arm64.tar.gz) |

### Quick CLI Install

```bash
# Download and install compute CLI to /usr/local/bin
curl -fsSL https://github.com/Rashid2G/compute/releases/latest/download/compute_1.0.0_linux_amd64.tar.gz | sudo tar -xz -C /usr/local/bin
compute --version
```

---

## Building from Source

Requires Go 1.24+ and Node.js 20+ (or Bun):

```bash
# Build Web Dashboard assets embedded into computed & compute gui
cd web && npm ci && cd ..
make build-ui

# Build compute, computed, compute-worker, and compute-bench into ./bin/
make build

# Run tests and static checks
make vet
make test
make test-web
```

See `make help` for all build, validation, image, and systemd targets.

---

## Core Workflows

### 1. Authentication & User Provisioning

- **`.env` Admin Bootstrap**: On startup, `computed` seeds and syncs the administrator account defined by `COMPUTE_ADMIN_USERNAME`, `COMPUTE_ADMIN_EMAIL`, and `COMPUTE_ADMIN_PASSWORD` (or `COMPUTE_ADMIN_SECRET`).
- **Closed Public Signup by Default**: `COMPUTE_SIGNUP_ENABLED=false` blocks unauthenticated public self-registration. The Web Login page adapts dynamically via `GET /api/v1/auth/status`, hiding or disabling public signup when closed.
- **Admin User Provisioning**: Administrators can create and manage users from **Access & Users** in the Web Dashboard (or `POST /api/v1/users`) and issue scoped `cmp_...` API keys for CLI/SDK automation.

```bash
# Authenticate CLI against your Compute daemon
export COMPUTE_DAEMON=127.0.0.1:18080
./bin/compute auth login --username admin
./bin/compute auth whoami
./bin/compute auth keys create --name ci-pipeline
```

### 2. Interactive Sandboxes & Live Previews

```bash
# Create and boot a persistent microVM sandbox
compute sandbox create --name dev-box --tier 2vcpu --image ubuntu-24 --start

# Open an interactive shell or execute commands with live streaming
compute sandbox ssh dev-box
compute sandbox exec dev-box -- uname -a

# Copy files and directories
compute scp -r ./app dev-box:/workspace/

# Discover HTTP preview URL or SSH target
compute url --port 3000 dev-box
```

### 3. Replicating (Cloning), Pausing, and Snapshotting Sandboxes

Compute provides full microVM lifecycle controls from the **Web Dashboard**, **CLI**, and **REST API**:

- **Replicate (Clone) Sandboxes**: Duplicate any existing sandbox into 1–5 replicas with independent overrides for **vCPUs** (`1, 2, 4, 8, 16`), **RAM**, **Disk**, **Network Isolation** (`account` vs `isolated`), and **Auto-Start** via the **Replicate** dialog in the Web UI or:

  ```bash
  curl -X POST http://127.0.0.1:18080/api/v1/vms/<sandbox-id>/clone \
    -H "Authorization: Bearer $COMPUTE_API_KEY" \
    -H "Content-Type: application/json" \
    -d '{"name":"dev-box-clone-1","vcpus":4,"memory_mb":4096,"network_isolated":true,"auto_start":true}'
  ```

- **Pause & Resume Live Memory/CPU State**: Freeze a running Firecracker microVM to disk (`PAUSED (STATE SAVED)`) and resume it instantaneously with memory and running processes intact:

  ```bash
  compute sandbox save-state dev-box
  compute sandbox resume dev-box
  ```

- **Snapshots & Reusable Templates**: Capture a point-in-time disk snapshot from any sandbox and launch new sandboxes from it via the **Images & Templates** page or CLI:

  ```bash
  compute template create dev-golden --vm dev-box --desc "Pre-configured toolchain"
  compute template launch dev-golden --name worker-1 --tier 2vcpu
  ```

### 4. Distributed Jobs

Run isolated ephemeral jobs across enrolled worker pools (or locally on the control plane when `jobs.local_execution` is enabled):

```bash
# Enroll a remote worker node
compute worker token create --name worker-1

# Dispatch a job with automatic workspace sync and live log streaming
compute job run --image ubuntu-24 --files ./src --cwd /workspace -- python3 test.py
compute job list
compute job logs <job_id>
```

---

## Documentation

Browse the complete interactive documentation in [`docs/index.html`](docs/index.html) (or on GitHub Pages when enabled for your fork), covering:

- Self-hosting, Docker Compose & bare-metal systemd setup
- Full `.env` & authentication reference
- Sandbox lifecycle: Replicate, Pause/Resume, Snapshots & Templates
- Distributed jobs & worker enrollment
- Complete `compute` CLI, REST API, and Python/TypeScript SDK references

---

## License

Compute is open-source software licensed under the [MIT License](LICENSE).
