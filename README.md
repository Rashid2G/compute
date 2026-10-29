# On-Demand Firecracker VMs (`microforged` & `mforge`)

A lightweight software suite designed to manage isolated code execution in ephemeral and persistent Firecracker microVMs on demand.

## Features

- **CLI & Daemon Architecture**: Clean separation between a client CLI (`mforge`) and a background daemon (`microforged`).
- **Standard Serverless Image**:
  - Pre-baked standard Alpine image containing essential serverless runtimes:
    - **Runtimes**: Node.js 20 (`node`, `npm`), Python 3.12 (`python3`, `py3-pip`), Bash
    - **Serverless Utilities**: `jq`, `curl`, `wget`, `git`, `tar`, `gzip`, `unzip`, `zip`, `procps` (`ps`, `top`), `socat`, `netcat`, `bind-tools` (`dig`), `ca-certificates`
- **Security & Non-Root Execution**:
  - MicroVMs run as a dedicated non-root user `mforge` (UID 1000).
  - Out-of-the-box passwordless `sudo` (`mforge ALL=(ALL) NOPASSWD: ALL`).
- **User-Friendly SSH Experience**:
  - Interactive pseudo-terminal allocation (`-t`).
  - Formatted, colored shell prompt displaying username, guest hostname, and current directory (`mforge@vm-xxxx:~$ `).
  - Clean login welcome banner (MOTD) displaying VM status and pre-installed runtimes.
  - Flexibility: defaults to `mforge` user, supports `mforge ssh [user@]<vm_id> [-- <cmd>]`.
- **Comprehensive Performance Benchmarking**:
  - Dedicated benchmark tool (`bin/mforge-bench`) and automated script (`scripts/benchmark.sh`).
  - Benchmarks API latency (GET `/health`, GET `/api/v1/vms`, concurrent load).
  - Benchmarks full VM lifecycle: Cold boot time, Time to First SSH (cold start latency), remote SSH execution, Node.js and Python serverless function latency, RAM snapshotting (`save-state`), instant restore (`resume`), warm start latency, and teardown.
  - Automated regression detection (`--compare baseline.json`): identifies improvements and regressions against previous runs.
- **Full MicroVM Lifecycle**:
  - `create`: Provisions VM metadata, allocates IP/MAC, and creates persistent sparse rootfs disk.
  - `run` / `start`: Launches Firecracker with isolated TAP device, attaches drives, and boots the guest Linux kernel.
  - `save-state`: Pauses the running VM, dumps microVM device states and guest RAM into snapshot files.
  - `resume`: Restores a running VM from saved snapshot files in milliseconds.
  - `stop`: Gracefully powers down microVMs, flushing disk caches, and cleaning up runtime resources.
  - `ssh`: Directly connects to running microVMs over SSH or runs isolated remote commands.
  - `list` / `ps`: Tabular status overview of all managed microVMs.
  - `delete` / `rm`: Cleans up VM workspace, disk images, and removes DB records.
- **SQLite Persistence**: Complete state machine, IP assignments, MACs, storage paths, and PIDs persisted in SQLite with WAL mode.
- **Virtual Network & NAT**:
  - Simulated NIC via virtio-net over per-VM TAP interfaces (`mftap-<id>`).
  - Host bridge (`mft3br0`) acting as gateway (`172.16.0.1/16`).
  - IPAM engine allocating deterministic guest IPs and locally administered unicast MACs.
  - Outbound internet access provided via host `iptables` NAT masquerade.
- **Persisted Disks**:
  - Copy-on-write sparse disk initialized from base Alpine rootfs for each VM.
  - Changes made by code execution persist across stop and restart cycles.

## Architecture

```
                 +-----------------------------+
                 |       mforge (CLI)          |
                 +--------------+--------------+
                                | REST API (HTTP or Unix Domain Socket)
                 +--------------v--------------+
                 |    microforged (Daemon)     |
                 +--+-----------+-----------+--+
                    |           |           |
       +------------v--+  +-----v-----+  +--v-------------+
       | SQLite Store  |  |  Storage  |  | Network / IPAM |
       | (modernc)     |  | (Disks &  |  |  (Bridge, TAP, |
       |               |  | Snapshots)|  |   NAT iptables)|
       +---------------+  +-----------+  +----------------+
                                |
                    +-----------v-------------+
                    |  Firecracker v1.9.0     |
                    |  - vCPU / Memory        |
                    |  - Kernel vmlinux       |
                    |  - Rootfs ext4 drive    |
                    |  - virtio-net / eth0    |
                    +-------------------------+
```

## Quick Start

### 1. Build Binaries

```bash
make build
```

The compiled binaries will be placed in `./bin/`:
- `./bin/microforged`: Daemon
- `./bin/mforge`: CLI tool
- `./bin/mforge-bench`: Performance benchmark suite

### 2. Host Setup

Configure the host virtual bridge (`mft3br0`) and IP forwarding:

```bash
sudo ./scripts/setup-host.sh
```

Generate the standard serverless Alpine rootfs with Node.js, Python 3, OpenSSH, and non-root user:

```bash
./scripts/build-rootfs.sh /var/lib/microforge/images 1024
```

### 3. Start the Daemon

```bash
sudo ./bin/microforged -listen 0.0.0.0:18080
```

### 4. Use the CLI

```bash
# Provision a new microVM
./bin/mforge create --name runner-1 --vcpus 1 --memory 512

# Start the microVM
./bin/mforge run <vm_id>

# List running VMs
./bin/mforge list

# Interactive SSH (opens colored bash shell as mforge user with passwordless sudo)
./bin/mforge ssh <vm_id>

# Execute serverless code directly via SSH
./bin/mforge ssh <vm_id> -- node -e 'console.log("Hello from Node.js in microVM!")'
./bin/mforge ssh <vm_id> -- python3 -c 'print("Hello from Python 3 in microVM!")'
./bin/mforge ssh <vm_id> -- sudo whoami

# Save execution state (RAM + CPU state)
./bin/mforge save-state <vm_id>

# Resume microVM instantly
./bin/mforge resume <vm_id>

# Stop microVM
./bin/mforge stop <vm_id>

# Delete microVM
./bin/mforge delete <vm_id>
```

### 5. Run Performance Benchmarks

Run the automated performance benchmark suite:

```bash
make bench
```

Or run directly with comparison against a saved baseline to detect regressions:

```bash
# 1. Establish baseline
./bin/mforge-bench --daemon 127.0.0.1:18080 --output baseline.json

# 2. After making changes, compare against baseline:
./bin/mforge-bench --daemon 127.0.0.1:18080 --compare baseline.json
```

Run unit-level micro-benchmarks:

```bash
make bench-unit
```

### 6. Run the Automated End-to-End Demo & Feature Tests

```bash
./scripts/demo.sh
./scripts/test-all-features.sh ./bin/mforge 127.0.0.1:18080
```
