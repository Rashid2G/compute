# On-Demand Firecracker VMs (`microforged` & `mforge`)

A lightweight software suite designed to manage isolated code execution in ephemeral and persistent Firecracker microVMs on demand.

## Features

- **CLI & Daemon Architecture**: Clean separation between a client CLI (`mforge`) and a background daemon (`microforged`).
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

### 2. Host Setup

Configure the host virtual bridge (`mft3br0`) and IP forwarding:

```bash
sudo ./scripts/setup-host.sh
```

Generate the base Alpine rootfs with OpenSSH and OpenRC:

```bash
sudo ./scripts/build-rootfs.sh /var/lib/microforge/images 512
```

### 3. Start the Daemon

```bash
sudo ./bin/microforged -listen 0.0.0.0:18080
```

### 4. Use the CLI

```bash
# Provision a new microVM
./bin/mforge create --name runner-1 --vcpus 1 --memory 256

# Start the microVM
./bin/mforge run <vm_id>

# List running VMs
./bin/mforge list

# Execute code or start an interactive shell via SSH
./bin/mforge ssh <vm_id> -- uname -a

# Save execution state (RAM + CPU state)
./bin/mforge save-state <vm_id>

# Resume microVM instantly
./bin/mforge resume <vm_id>

# Stop microVM
./bin/mforge stop <vm_id>

# Delete microVM
./bin/mforge delete <vm_id>
```

### 5. Run the Automated End-to-End Demo

```bash
./scripts/demo.sh
```
