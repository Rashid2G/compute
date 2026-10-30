# MicroForge: High-Performance On-Demand MicroVM Orchestrator

MicroForge is an on-demand microVM provisioning platform powered by AWS Firecracker and Linux KVM. It features sub-second cold starts, dynamic networking with multiple topology modes (virtual NAT, virtual bridge, and physical NIC enslavement), persistent storage, memory + CPU snapshot pausing/resuming, and an agent-ready non-root SSH environment with Node.js and Python 3 runtimes.

---

## Key Features

- **MicroVM Orchestration**: Fast microVM lifecycle management (`create`, `run`, `stop`, `save-state`, `resume`, `delete`).
- **Flexible Network Topologies**:
  - **Virtual NAT Mode (`nat`)**: Dedicated bridge (`mft3br0`) behind virtual NAT with iptables masquerading and IP forwarding.
  - **Virtual Bridge Mode (`bridge`)**: Standard layer-2 virtual bridge for isolated or user-managed routing without NAT.
  - **Physical NIC Mode (`nic`)**: Direct physical network attachment enslaving a real host NIC (`eth0`, `enp...`).
- **Zero Orphan Guarantee**: Automatic host TAP interface reconciliation and prune command to remove leftover devices and reclaim IPs.
- **Agent & User Friendly SSH**:
  - OpenSSH server with non-root user `mforge` and passwordless `sudo`.
  - Full pseudo-terminal (`/dev/pts`) support for interactive terminal sessions.
  - Preloaded serverless tools: Node.js, Python 3, bash, git, curl, jq, iproute2, and bind-tools.
- **RAM & CPU Snapshots**: Save and resume running microVM memory states in milliseconds.
- **Web GUI & Remote Console**: Functional dark-mode Web UI with live monitoring, provisioning, lifecycle management, and in-browser SSH remote execution console (`mforge gui`).
- **RESTful API & CLI**: Complete HTTP API and intuitive `mforge` CLI.
- **Performance Benchmarking Suite**: Automated latency, throughput, and regression tracking (`mforge-bench`).

---

## Network Architecture & Modes

MicroForge supports three distinct network modes configured via JSON config files or daemon flags:

| Mode | Topology | Best For |
|---|---|---|
| `nat` *(Default)* | Virtual bridge (`mft3br0`) + iptables NAT masquerade + IP forwarding | VMs needing outbound Internet access behind private subnet (`172.16.0.0/16`). |
| `bridge` | Layer-2 Linux bridge without NAT masquerading | Private inter-VM networks or host networks where routing/DHCP is managed externally. |
| `nic` | Enslaves a real physical host interface (e.g. `eth0`) into the bridge | VMs directly accessible on the host's physical LAN. |

### Network Config Files

Default configurations are located in `config/`:
- `config/network.nat.json`: Virtual bridge with virtual NAT.
- `config/network.bridge.json`: Virtual bridge without NAT.
- `config/network.nic.json`: Enslaved real NIC mode.
- `config/network.json`: Active configuration loaded automatically by the daemon.

---

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

Generate the standard serverless Alpine rootfs with Node.js, Python 3, OpenSSH, devpts, and non-root user:

```bash
./scripts/build-rootfs.sh /var/lib/microforge/images 1024
```

### 3. Start the Daemon

```bash
# Default mode (NAT via config/network.json)
sudo ./bin/microforged -listen 0.0.0.0:18080

# Or specify a network config file directly
sudo ./bin/microforged -network-config config/network.nat.json

# Or override via flags (e.g. bridge mode or physical NIC)
sudo ./bin/microforged -network-mode nic -physical-nic eth0
```

### 4. Web GUI (Browser Interface)

MicroForge includes an embedded, zero-dependency Web UI for monitoring and managing microVMs, network topologies, and remote execution.

Launch the GUI server with the CLI:

```bash
# Launch GUI on default port 8080 (connects to daemon on 127.0.0.1:18080)
./bin/mforge gui

# Or customize listen address, port, and daemon endpoint:
./bin/mforge gui --addr 0.0.0.0 --port 8080 --daemon 127.0.0.1:18080
```

Once running, navigate to `http://localhost:8080` (or `http://<host-ip>:8080`) in your browser to access:
- **Dashboard**: Live cluster metrics, resource allocation (vCPUs/RAM), network bridge & NAT state, and auto-refresh intervals.
- **MicroVM Management**: Searchable, filterable instance table with full lifecycle actions (Run, Stop, Snapshot, Resume, Delete, Inspect).
- **Web Exec Console**: Direct remote execution inside any running microVM via SSH with preset commands and output terminal.
- **Network Management**: Live network topology card, active IP/MAC lease allocations, TAP interface list, and one-click orphan TAP pruning.

*Note: The `microforged` daemon also serves the Web GUI directly on its listen port (e.g. `http://<daemon-ip>:18080/`).*

### 5. CLI Network Management

MicroForge CLI provides network management commands:

```bash
# Check current network mode, bridge status, NAT, and active TAPs
./bin/mforge network status

# List all active IP leases, MACs, and TAP devices
./bin/mforge network list

# Inspect network details and host connectivity for a VM
./bin/mforge network inspect <vm_id>

# Clean up any orphaned TAP interfaces or stale resources
./bin/mforge network prune
```

### 6. VM Lifecycle Management

```bash
# Provision a microVM with automatic IP allocation
./bin/mforge create --name runner-1 --vcpus 1 --memory 512

# Provision with a static IP and custom MAC
./bin/mforge create --name runner-static --ip 172.16.0.50 --mac 06:00:ac:10:00:32

# Start the microVM
./bin/mforge run <vm_id>

# List running VMs
./bin/mforge list

# Interactive SSH shell (full PTY allocation, colored prompt)
./bin/mforge ssh <vm_id>

# Execute remote commands directly via SSH
./bin/mforge ssh <vm_id> -- node -e 'console.log("Hello from Node.js in microVM!")'
./bin/mforge ssh <vm_id> -- python3 -c 'print("Hello from Python 3 in microVM!")'
./bin/mforge ssh <vm_id> -- sudo whoami

# Save execution state (RAM + CPU state)
./bin/mforge save-state <vm_id>

# Resume microVM instantly
./bin/mforge resume <vm_id>

# Stop microVM (automatically frees TAP device)
./bin/mforge stop <vm_id>

# Delete microVM (frees IP address and cleans persistent disks)
./bin/mforge delete <vm_id>
```

### 7. Run Performance Benchmarks

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

### 8. Run Automated Feature Tests

```bash
./scripts/demo.sh
./scripts/test-all-features.sh ./bin/mforge 127.0.0.1:18080
```
