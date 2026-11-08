# Compute Platform — Web UI Specification & Architecture Guide (`ui.md`)

This document defines the complete product specification, design system, view-by-view functional requirements, real-time streaming ergonomics, and implementation roadmap for the **Compute Web UI**.

It serves as the definitive reference for frontend engineers, UI/UX designers, and backend developers building and extending the management dashboard for Compute's on-demand Firecracker microVMs and distributed user-contributed worker fleet.

---

## Table of Contents

1. [Executive Vision & Core Principles](#1-executive-vision--core-principles)
2. [Target Personas & Primary Use Cases](#2-target-personas--primary-use-cases)
3. [Design System & UI Foundations](#3-design-system--ui-foundations)
4. [Global Shell & Navigation Architecture](#4-global-shell--navigation-architecture)
5. [Complete Screen & View Specifications](#5-complete-screen--view-specifications)
   - 5.1 [Fleet Overview Dashboard (`/overview`)](#51-fleet-overview-dashboard-overview)
   - 5.2 [MicroVM Instances Management (`/instances`)](#52-microvm-instances-management-instances)
   - 5.3 [MicroVM Instance Deep Dive (`/instances/:id`)](#53-microvm-instance-deep-dive-instancesid)
   - 5.4 [Interactive SSH & Serial Web Terminal (`/console` & `/terminal/:id`)](#54-interactive-ssh--serial-web-terminal-console--terminalid)
   - 5.5 [Distributed Worker Nodes (`/workers`)](#55-distributed-worker-nodes-workers)
   - 5.6 [Worker Node Deep Dive (`/workers/:id`)](#56-worker-node-deep-dive-workersid)
   - 5.7 [Workload Queue & Scheduler (`/queue`)](#57-workload-queue--scheduler-queue)
   - 5.8 [Batch & CI Jobs Runner (`/jobs` & `/jobs/:id`)](#58-batch--ci-jobs-runner-jobs--jobsid)
   - 5.9 [Images, Kernels & Rootfs Catalog (`/images`)](#59-images-kernels--rootfs-catalog-images)
   - 5.10 [VM Templates & Snapshots (`/templates`)](#510-vm-templates--snapshots-templates)
   - 5.11 [Network Topology & Firewall Rules (`/network`)](#511-network-topology--firewall-rules-network)
   - 5.12 [Persistent Storage & Disks (`/storage`)](#512-persistent-storage--disks-storage)
   - 5.13 [Access Control & Credentials (`/access`)](#513-access-control--credentials-access)
   - 5.14 [Audit Logs & Events (`/events`)](#514-audit-logs--events-events)
   - 5.15 [Cluster Settings & Daemon Config (`/settings`)](#515-cluster-settings--daemon-config-settings)
   - 5.16 [Interactive API Explorer (`/api`)](#516-interactive-api-explorer-api)
6. [Global Modals, Dialogs & Drawers](#6-global-modals-dialogs--drawers)
7. [Real-Time Streaming, WebSockets & Event Protocols](#7-real-time-streaming-websockets--event-protocols)
8. [Error Handling, Telemetry & Operator UX](#8-error-handling-telemetry--operator-ux)
9. [Component Architecture & State Management](#9-component-architecture--state-management)
10. [Implementation Checklist & Sequence](#10-implementation-checklist--sequence)

---

## 1. Executive Vision & Core Principles

The Compute Web UI is an enterprise-grade, high-density web console designed to make lightweight Firecracker virtualization feel instantaneous, transparent, and effortlessly controllable. It bridges two computing models:
1. **Local/Single-Host MicroVM Engine**: Sub-second launching, direct TAP bridge control, persistent disk attaching, and memory snapshotting.
2. **Distributed Remote Worker Fleet**: Scheduling workloads across customer-contributed Linux servers operating behind NAT and firewalls.

### Core UX Principles

1. **Sub-Second Feedback Loop**: State changes, terminal connections, and metrics should reflect instantly. When an instance launches, the UI should transition seamlessly from queued $\rightarrow$ starting $\rightarrow$ running without page refreshes.
2. **Terminal-First Ergonomics**: Instant access to xterm.js terminals (popout windows or embedded drawers), quick cURL commands, and SSH copy helpers across every resource.
3. **High-Density, Information-Rich Layouts**: Minimal decorative whitespace; prioritize tabular data, monospace network coordinates, hardware utilization progress bars, and status indicators.
4. **Resilient Failure Transparency**: When a VM fails to boot or a worker disconnects, never display a generic error. Provide the machine exit code, jailer/Firecracker kernel console logs, trace ID, and actionable operator guidance.
5. **Safe Destructive Actions**: Enforce explicit confirmation typing for deleting active instances, wiping persistent volumes, draining worker pools, or revoking enrollment credentials.

---

## 2. Target Personas & Primary Use Cases

| Persona | Core Focus | Key Screens & Interactions |
|---|---|---|
| **Platform Operator / SRE** | Cluster health, capacity saturation, worker lifecycle, network bridges, resource quotas. | `/overview`, `/workers`, `/network`, `/settings`, cordon/drain operations. |
| **DevOps / CI Engineer** | Build pipeline performance, job throughput, runner tokens, ephemeral VM test sandboxes. | `/jobs`, `/queue`, `/images`, GitHub Actions integration, log artifacts. |
| **AI Agent Developer** | Ephemeral microVM sandboxes for LLM coding agents, remote command execution, fast snapshot restores. | `/instances`, `/console`, `/templates`, `/api`, execution timeout controls. |
| **Host Worker Provider** | Contributing spare Linux capacity to the organization pool, monitoring host CPU/RAM stress. | `/workers` enrollment token flow, worker host utilization gauges. |

---

## 3. Design System & UI Foundations

The frontend is built upon a modern, accessible, dark-mode native component system.

### 3.1 Tech Stack
- **Framework**: React 18+ with TypeScript (strict mode).
- **Styling**: Tailwind CSS with CSS Variables for theme tokens.
- **Component Primitives**: Radix UI (accessible primitives for dialogs, dropdowns, tooltips, sheets, tabs).
- **Icons**: Lucide React.
- **Terminal Emulator**: xterm.js with `@xterm/addon-fit`, `@xterm/addon-web-links`, and `@xterm/addon-canvas`.
- **Notifications**: Sonner (stackable toast notifications with action buttons).
- **Data Visualization**: Recharts or SVG sparklines for live CPU/RAM/IO utilization.

### 3.2 Color Tokens & Status Semantics

| Semantic State | Light Theme Token | Dark Theme Token | UI Representation |
|---|---|---|---|
| **Running / Ready** | `bg-emerald-50 text-emerald-700 border-emerald-200` | `bg-emerald-500/10 text-emerald-400 border-emerald-500/20` | Pulsing green dot, active badge |
| **Queued / Pending** | `bg-amber-50 text-amber-700 border-amber-200` | `bg-amber-500/10 text-amber-400 border-amber-500/20` | Amber clock indicator |
| **Paused / Snapshot** | `bg-blue-50 text-blue-700 border-blue-200` | `bg-blue-500/10 text-blue-400 border-blue-500/20` | Blue disk/camera icon |
| **Stopped** | `bg-slate-100 text-slate-700 border-slate-200` | `bg-zinc-800 text-zinc-300 border-zinc-700` | Muted neutral badge |
| **Failed / Error** | `bg-rose-50 text-rose-700 border-rose-200` | `bg-rose-500/10 text-rose-400 border-rose-500/20` | Red triangle alert badge |
| **Cordoned / Draining** | `bg-purple-50 text-purple-700 border-purple-200` | `bg-purple-500/10 text-purple-400 border-purple-500/20` | Purple pause icon |

### 3.3 Typography & Formatting
- **Body & UI**: Inter or system UI font family.
- **Monospace Coordinates**: JetBrains Mono or Fira Code for IP addresses, MACs, Git commit SHAs, UUIDs, TAP devices, and CLI snippets.
- **Tabular Numerics**: `font-variant-numeric: tabular-nums;` on all counts, memory allocations, timestamps, and percentages to prevent layout jitter.

---

## 4. Global Shell & Navigation Architecture

```
+----------------------------------------------------------------------------------------------------+
|  [Compute Logo]  Org: default-team v   Search (Cmd+K)       [Health: 99.8%] [Theme] [User / Keys] |
+------------------+---------------------------------------------------------------------------------+
|  NAVIGATION      |  PAGE HEADER: Breadcrumb / Title / Quick Actions                                 |
|  * Overview      +---------------------------------------------------------------------------------+
|  * Instances     |                                                                                 |
|  * Workers [New] |  PAGE CONTENT BODY                                                              |
|  * Queue         |  (Cards, Tables, Graphs, Skeletons, Streaming Terminals)                         |
|  * Jobs          |                                                                                 |
|  * Images        |                                                                                 |
|  * Templates     |                                                                                 |
|  * Network       |                                                                                 |
|  * Console       |                                                                                 |
|  * Storage       |                                                                                 |
|  * Access        |                                                                                 |
|  * Settings      |                                                                                 |
|  * API Explorer  |                                                                                 |
+------------------+---------------------------------------------------------------------------------+
|  FOOTER STATUS: Daemon: v0.4.2 | Control Plane: Connected (12ms) | Active Worker Nodes: 4/4        |
+----------------------------------------------------------------------------------------------------+
```

### 4.1 Header Bar
- **Organization / Workspace Switcher**: Multi-tenant selector dropdown.
- **Global Command Palette (`Cmd+K` / `Ctrl+K`)**: Rapid fuzzy finder to instantly jump to any VM by name/ID, worker node, image, or trigger actions ("Create VM", "Enroll Worker", "Clear Failed Jobs").
- **Daemon & Connection Health Pill**: Live ping status with latency tooltip. Green when active, yellow on reconnecting backoff, red on disconnect.
- **Global Action Buttons**:
  - `+ New Instance`: Launches the Instance Creation Wizard.
  - `Enroll Worker`: Opens the Worker Setup Modal.
  - `SSH Keys`: Opens the user SSH Key Injection manager.
  - `Theme Toggle`: Dark/Light theme toggle.

### 4.2 Sidebar Navigation
- Primary categories grouped into logical sections:
  1. **Compute Fleet**: Overview (`/overview`), Instances (`/instances`), Workers (`/workers`).
  2. **Workloads & CI**: Queue (`/queue`), Jobs (`/jobs`), Console (`/console`).
  3. **Infrastructure**: Images & Rootfs (`/images`), Templates (`/templates`), Storage & Snapshots (`/storage`), Networking (`/network`).
  4. **Administration**: Access & Keys (`/access`), Audit Events (`/events`), Cluster Settings (`/settings`), API Explorer (`/api`).
- **Badge Indicators**: Dynamic count badges on Queue (pending count) and Instances (active count).
- **Collapse Mode**: Icon-only sidebar for narrow displays with tooltip descriptions.

---

## 5. Complete Screen & View Specifications

### 5.1 Fleet Overview Dashboard (`/overview`)

#### Header & Metrics
- **Cluster Stats Bar**:
  - Total VMs (Running, Queued, Snapshotted, Stopped, Failed).
  - CPU Allocation: Used vCPUs vs Total Host/Cluster Capacity ($N / M$ vCPUs, percentage fill bar).
  - Memory Allocation: Consumed RAM vs Total Available RAM (e.g., $18.4\text{ GB} / 32\text{ GB}$, percentage fill bar).
  - Disk Pool Capacity: Ephemeral + Persistent disk allocation and free host storage.
  - Active Worker Nodes: Connected count vs total enrolled.

#### Main Panels
1. **Recent Instances Table**:
   - Quick glance at top 5 running or recently modified microVMs.
   - Inline actions: Open Terminal, Inspect, Stop, Restart.
2. **Scheduling Queue Health**:
   - Status of the scheduler queue (jobs waiting, average wait time, worker compatibility status).
3. **Network & Subnet Utilization**:
   - Subnet CIDR (e.g., `172.20.0.0/16`), Bridge name (`fc-br0`), Allocated IP count, Gateway status.
4. **System Events & Audit Stream**:
   - Live chronological feed of recent events: VM booted, snapshot taken, worker enrolled, job exited.

---

### 5.2 MicroVM Instances Management (`/instances`)

The core hub for managing Firecracker microVMs.

```
+----------------------------------------------------------------------------------------------------+
| Instances (8 Total)                   [Search instances...] [Filter: All v]   [+ Launch Instance]  |
+----------------------------------------------------------------------------------------------------+
| [x] Name / ID         Status    vCPU  RAM     IP Address       Uptime     Actions                  |
+----------------------------------------------------------------------------------------------------+
| [ ] agent-run-41a     RUNNING   2     2048MB  172.20.0.14      14m 22s    [SSH] [Exec] [Snapshot] [:]|
| [ ] dev-workspace-1   STOPPED   4     4096MB  172.20.0.18      -          [Start] [Inspect] [Delete] |
| [ ] ci-test-worker-9  QUEUED    2     1024MB  (allocating)     -          [Cancel] [Queue Info]     |
| [ ] base-alpine-snap  PAUSED    1     512MB   172.20.0.9       -          [Resume] [Fork] [Delete]  |
+----------------------------------------------------------------------------------------------------+
| Selected (1): [Start] [Stop] [Pause] [Delete Selected]                                            |
+----------------------------------------------------------------------------------------------------+
```

#### View Features
- **View Modes**: Switchable between **Dense Table** view and **Visual Card Grid** view.
- **Search & Filters**:
  - Fuzzy text search across VM name, UUID, IP address, MAC, or assigned worker.
  - Status filter pills: `All`, `Running`, `Queued`, `Paused (Snapshots)`, `Stopped`, `Failed`.
  - Tag / Owner filters.
- **Batch Operations**: Multi-select checkboxes to bulk Start, Stop, or Delete instances.
- **Data Columns**:
  1. **Name & UUID**: Display name, truncated UUID with quick-copy icon, creation timestamp.
  2. **Status**: Pulsing color badge with micro-state (`booting`, `ready`, `stopping`, `failed`).
  3. **Specs**: vCPUs, Allocated RAM, Disk Size.
  4. **Network Coordinates**: Assigned IP (clickable to copy), Public IP badge (if enabled), TAP device name (`fc-tap-X`).
  5. **Worker Node**: Which physical host / worker is executing the microVM.
  6. **Uptime / Lease Time**: Dynamic countdown or elapsed duration.
  7. **Quick Action Group**:
     - `SSH`: Launches interactive browser terminal.
     - `Exec`: Opens one-off command runner dialog.
     - `Power`: Start / Stop / Restart toggle.
     - `More Actions (...)`: Inspect JSON, Take Snapshot, Fork Instance, Toggle Public IP, Delete.

---

### 5.3 MicroVM Instance Deep Dive (`/instances/:id`)

Comprehensive multi-tab management screen for a single microVM.

#### Header Section
- Instance Name with inline rename option.
- Current Status Badge + Heartbeat/Uptime timer.
- Primary Action Bar: `[Start]`, `[Stop]`, `[Reboot]`, `[Take Snapshot]`, `[Open Web Terminal]`, `[Delete]`.

#### Tabs Architecture
1. **Overview & Metrics**:
   - Hardware Specifications Card: vCPUs, RAM, Rootfs path, Kernel binary path, Firecracker PID, Socket path (`/tmp/firecracker-<id>.sock`).
   - Live Utilization Sparklines: CPU usage percentage, Memory consumption, Network I/O (RX/TX bytes/sec), Disk I/O.
   - SSH Connection Assistant: Copyable SSH command (`ssh -i ~/.compute/id_rsa compute@172.20.0.X`), dedicated port forward info.
2. **Interactive Terminal (`Console`)**:
   - Full-width xterm.js terminal connected directly over SSH or Firecracker serial vsock console.
   - Screen resizing, clipboard paste support, reconnect button, download session log.
3. **Execution Jobs (`Exec History`)**:
   - Table of commands executed inside this VM via the API or agent harness.
   - Columns: Command string, User, Exit code (colored green for 0, red for non-zero), Duration, Timestamp.
   - Click to inspect full captured stdout and stderr.
4. **Networking & Firewalls**:
   - Assigned IPv4, MAC address, Host TAP interface, Bridge parent.
   - Public IP toggle switch: Instantly updates host `iptables` FORWARD rules to route public ingress/egress.
   - Port forwarding rules table: Host port $\rightarrow$ Guest port mapping.
5. **Storage & Disks**:
   - Rootfs image details (read-only vs copy-on-write overlay).
   - Attached persistent volumes: Mount point, volume ID, size, IOPS limit.
   - Create disk snapshot button.
6. **Snapshots & Memory State**:
   - List of RAM snapshots: Date taken, memory dump file size (`mem_file`), snapshot state path.
   - Actions: Restore from snapshot (reverts VM state in $<100\text{ms}$), Export snapshot, Delete.
7. **Logs & Diagnostics**:
   - Live stream of Firecracker process logs (`firecracker.log`) and guest serial output.
   - Search filter for kernel panics, OOM killer messages, DHCP negotiation logs.

---

### 5.4 Interactive SSH & Serial Web Terminal (`/console` & `/terminal/:id`)

A dedicated, latency-optimized terminal interface designed for debugging and developer workspaces.

#### Features
- **Terminal Engine**: Full xterm.js terminal with WebGL/Canvas acceleration.
- **Instance Selector Dropdown**: Switch between active running instances without leaving the terminal page.
- **Popout Window Mode**: One-click button to detach the terminal into an independent, chromeless browser window for multi-monitor setups.
- **Connection Diagnostics**:
  - Connection protocol pill: `SSH over WebSocket` or `Serial Console (TTY)`.
  - Real-time latency ping (e.g., `4ms`).
- **Terminal Controls Bar**:
  - Clear screen, Send `Ctrl+C`, Send `Ctrl+D`, Send `SIGKILL`.
  - Reset Terminal session.
  - Font size adjuster (`12px`, `14px`, `16px`).
  - Copy all buffer / Download terminal log as `.txt`.

---

### 5.5 Distributed Worker Nodes (`/workers`)

The management panel for the user-contributed computing fleet.

#### Fleet Overview Cards
- **Online / Healthy Workers**: e.g., `12 Active / 14 Enrolled`.
- **Aggregated Fleet Capacity**: Total cores, total RAM, total storage across all distributed machines.
- **Worker Pools**: Pool groups (e.g., `default`, `gpu-builders`, `arm64-nodes`, `isolated-ci`).

#### Action Bar
- `Enroll New Worker`: Opens the enrollment token generation modal with a one-click `curl` install snippet.
- `Pool Filter`: Filter by pool name.
- `Architecture Filter`: `x86_64 / amd64`, `aarch64 / arm64`.

#### Workers Table Columns
1. **Worker Name & Host ID**: Machine hostname, unique worker ID, enrollment date.
2. **Status**:
   - `Ready` (Active heartbeat $<15\text{s}$).
   - `Degraded` (Missed heartbeat, high resource pressure).
   - `Offline` (Disconnected).
   - `Cordoned` (Marked unschedulable).
   - `Draining` (Waiting for running jobs to finish before shutdown).
3. **Pool & Labels**: Key-value label badges (e.g., `pool: linux-builders`, `arch: amd64`, `disk: nvme`, `kernel: 6.8.0`).
4. **Agent Version**: `compute-worker v0.3.1` (with update available badge if out of date).
5. **Capacity & Pressure Gauges**:
   - CPU: e.g., $12 / 16$ cores used ($75\%$).
   - RAM: e.g., $24 / 32\text{ GB}$ used ($75\%$).
   - Disk: Free space in worker spool directory.
6. **Active Workloads**: Number of currently running microVMs or jobs on this machine.
7. **Certificate / mTLS Expiry**: Days remaining on worker client certificate.
8. **Actions Menu**:
   - **Cordon**: Prevent new jobs from scheduling on this node.
   - **Uncordon**: Re-enable scheduling.
   - **Drain**: Gracefully migrate or wait for jobs to finish, then disconnect.
   - **Reconcile**: Trigger manual state synchronization with control plane.
   - **Revoke**: Invalidate worker certificate and remove from cluster.

---

### 5.6 Worker Node Deep Dive (`/workers/:id`)

Detailed hardware, host OS, and workload telemetry for an individual worker machine.

#### Panels
- **Host System Info**: Linux distribution, Kernel version, KVM availability (`/dev/kvm` read/write verified), Jailer UID/GID isolation, Public IP / ISP, Outbound latency to control plane.
- **Worker Configuration**: Heartbeat interval, maximum concurrent VMs, storage paths, allowed CIDRs.
- **Running Workloads on Node**: Table of all microVMs currently assigned to this node with their CPU/RAM footprint.
- **Worker Agent Logs**: Live streaming connection log showing WebSocket heartbeat frames, job assignments, rootfs downloads, and teardown confirmations.

---

### 5.7 Workload Queue & Scheduler (`/queue`)

Provides complete transparency into the scheduling engine, explaining *why* a workload is queued and when it will run.

#### Queue Metrics
- Pending jobs count, Average time in queue, Capacity fulfillment rate.

#### Queued Items Table
- **Position in Queue**: `#1`, `#2`, etc.
- **Workload / Job ID**: Clickable to job details.
- **Submitted By / Organization**: User or automated API key.
- **Resource Requirements**: Required vCPUs, RAM, Architecture (`amd64`/`arm64`), required labels (e.g., `docker=true`, `disk: fast`).
- **Wait Duration**: Time spent in queue (e.g., `2m 14s`).
- **Scheduling Diagnostics / Reason**:
  - Clear, human-readable scheduling explanation:
    - *"Waiting for 4 vCPU in pool 'linux-builders' (0/3 workers have capacity)"*
    - *"No online worker matches requirements: arch=arm64"*
    - *"Tenant quota reached (max 5 running VMs)"*
- **Action Controls**:
  - `Prioritize (Bump)`: Operator override to adjust priority.
  - `Cancel Job`: Abort queuing and clean up.

---

### 5.8 Batch & CI Jobs Runner (`/jobs` & `/jobs/:id`)

The execution tracker for ephemeral CI jobs, batch scripts, and AI agent workloads.

#### Jobs List View (`/jobs`)
- Filter by status: `Running`, `Passed`, `Failed`, `Cancelled`.
- Filter by source: `GitHub Actions`, `Manual CLI`, `API Automation`.
- Columns: Job ID, Image / Base, Command, Triggered By, Target Worker, Runtime, Status, Completed At.

#### Job Detail View (`/jobs/:id`)
- **Execution Timeline**:
  - Visual stage progression: `Queued` $\rightarrow$ `Scheduled (worker-02)` $\rightarrow$ `Image Pull` $\rightarrow$ `VM Boot (180ms)` $\rightarrow$ `Executing Command` $\rightarrow$ `Collecting Artifacts` $\rightarrow$ `VM Teardown`.
- **Exit Status Header**: Big badge showing `Exit Code: 0 (Success)` or `Exit Code: 137 (OOM Killed)` with duration.
- **Live Streaming Log Terminal**:
  - Real-time ANSI-color log viewer.
  - Toggle between Combined, `stdout` only, or `stderr` only.
  - Autoscroll toggle, line numbers, search within logs.
- **Job Artifacts Download**:
  - Table of files generated during execution and uploaded to storage.
  - File name, size, SHA256 checksum, direct download button.
- **Environment & Injected Secrets**:
  - Non-sensitive environment variables displayed; secrets masked (`********`).

---

### 5.9 Images, Kernels & Rootfs Catalog (`/images`)

Manage base guest operating system images, custom Firecracker kernels, and specialized rootfs builds.

#### Available Images
- **Standard Pre-Baked Images**:
  - Alpine Linux Minimal (Instant boot, $<50\text{MB}$ rootfs, optimized for fast tasks).
  - Ubuntu 22.04 / 24.04 LTS (Pre-installed with Docker, Python 3, Node.js, Go).
  - Debian 12 Slim.
- **Kernel Catalog**:
  - Custom stripped Firecracker Linux kernels (`vmlinux-5.10`, `vmlinux-6.1`) with microVM optimizations.

#### Custom Image Builder Interface
- **Interactive Builder Form**:
  - Base Image selector (Alpine / Ubuntu / Debian).
  - Package Checklist or text area for `apt` / `apk` packages (e.g., `build-essential git curl nodejs`).
  - Target Disk Size slider ($1\text{ GB} - 50\text{ GB}$).
  - Startup Script / Provisioning commands.
  - `Build Rootfs` button: Displays live build progress logs and compiles into a bootable ext4 rootfs image.

---

### 5.10 VM Templates & Snapshots (`/templates`)

Accelerate workflow provisioning with pre-configured templates and gold-master snapshots.

#### Features
- **Template Library**: Cards showing template name, source VM, disk size, pre-installed tools, default vCPU/RAM.
- **One-Click Launch**: "Launch Template" dialog pre-filling all hardware defaults.
- **Create Template from Running VM**: Takes an active microVM, flushes the filesystem, detaches the persistent disk copy, and saves it as a reusable template.

---

### 5.11 Network Topology & Firewall Rules (`/network`)

Visualize and control virtual networking, bridge interfaces, TAP devices, and host routing.

```
+----------------------------------------------------------------------------------------------------+
| Network Topology (Bridge: fc-br0 | CIDR: 172.20.0.0/16 | Gateway: 172.20.0.1)                     |
+----------------------------------------------------------------------------------------------------+
| BRIDGE CONFIG       TAP ALLOCATIONS TABLE                                                          |
| Mode: NAT / Bridge  [Search IPs...]                                                                |
| Interface: fc-br0   VM Name          Guest IP      Public IP       TAP Device  MAC Address  Status |
| Subnet: /16         agent-run-41a    172.20.0.14   198.51.100.22   fc-tap-0    06:00:ac:14  ACTIVE |
| IP Forwarding: ON   dev-workspace-1  172.20.0.18   -               fc-tap-1    06:00:ac:18  ACTIVE |
| DNS: 8.8.8.8        ci-test-9        172.20.0.25   -               fc-tap-2    06:00:ac:19  IDLE   |
+----------------------------------------------------------------------------------------------------+
| FIREWALL & ISOLATION RULES (iptables)                                                              |
| [x] Masquerade Outbound Traffic (NAT)                                                              |
| [x] Inter-VM Isolation (Prevent guest-to-guest communication across bridge)                         |
| [x] Restrict Host LAN Access (Block 192.168.0.0/16 from microVMs)                                  |
+----------------------------------------------------------------------------------------------------+
```

#### Capabilities
- **Bridge Configuration Overview**: Subnet range, host bridge IP, physical NIC uplink (`eth0` / `enp3s0`), IPv4 forwarding status.
- **TAP Allocations & Leases Table**: Real-time DHCP / static IP table displaying every active TAP device, assigned microVM, MAC address, and ping reachability check.
- **Isolation Rules Controls**:
  - Toggle inter-VM network isolation.
  - Toggle host network shielding (blocking microVMs from querying host private subnets or cloud metadata endpoints `169.254.169.254`).
  - Toggle dedicated Public IP routing per instance.

---

### 5.12 Persistent Storage & Disks (`/storage`)

Manage persistent ext4 volume storage detached from microVM lifecycles.

#### Capabilities
- **Disks Table**: Volume ID, Name, Size (GB), Attached VM (if any), Storage Path on host, Filesystem format (`ext4`).
- **Disk Creation Wizard**: Name, initial size, blank disk vs pre-seeded from image.
- **Disk Resizing**: Safely expand disk volume without data loss.
- **Detach / Attach Controls**: Attach an existing disk to a new microVM on boot.

---

### 5.13 Access Control & Credentials (`/access`)

Security and authorization center.

#### Tabs
1. **API Keys**:
   - Create scoped API tokens with expirations (`30 days`, `90 days`, `never`).
   - Scopes: `vms:read`, `vms:write`, `workers:enroll`, `jobs:run`, `admin`.
   - Secret key reveal modal (displayed once with copy button).
   - Revocation and last-used timestamp tracking.
2. **User SSH Keys**:
   - List of authorized public keys injected automatically into newly provisioned microVMs.
   - Shows key title, fingerprint (SHA256), date added.
   - Add new SSH Key modal (paste OpenSSH public key).
3. **Worker Enrollment Tokens**:
   - Active tokens used to enroll remote worker daemons.
   - Pool assignment, remaining uses, expiration countdown.

---

### 5.14 Audit Logs & Events (`/events`)

System-wide activity log for security compliance and post-incident reviews.

#### Capabilities
- Filterable stream of events: `vm.create`, `vm.start`, `vm.stop`, `vm.delete`, `worker.enrolled`, `worker.cordoned`, `exec.run`, `auth.failed`.
- Detailed event metadata inspector: Requesting user/token, source IP, trace ID, payload parameters, before/after state diffs.
- Export as JSONL / CSV.

---

### 5.15 Cluster Settings & Daemon Config (`/settings`)

Global system configurations and capacity limits.

#### Sections
- **Resource Ceilings**:
  - Maximum concurrent running VMs.
  - Maximum total vCPUs host limit.
  - Maximum allocated RAM limit (MB).
  - Minimum free host disk space threshold (stops new VMs if free disk falls below e.g., $5\text{ GB}$).
- **Paths & Binaries**:
  - Firecracker binary path (`/usr/local/bin/firecracker`).
  - Jailer binary path (`/usr/local/bin/jailer`).
  - Default Kernel image (`/var/lib/compute/vmlinux`).
  - Default Rootfs image (`/var/lib/compute/rootfs.ext4`).
- **Default VM Sizing**:
  - Default vCPUs (1, 2, 4).
  - Default RAM (512MB, 1024MB, 2048MB).
  - Default Disk size.
- **Worker Policy**:
  - Allowed worker pools, default worker job timeout, auto-cordon on missed heartbeats.

---

### 5.16 Interactive API Explorer (`/api`)

Built-in developer documentation and sandbox.

#### Capabilities
- Live API documentation for all REST endpoints (`/v1/vms`, `/v1/workers`, `/v1/jobs`, `/v1/stats`).
- Copyable cURL commands, Python SDK snippets, and TypeScript SDK snippets for every endpoint.
- "Try It Now" interactive request runner with token auto-injection and formatted JSON response viewer.

---

## 6. Global Modals, Dialogs & Drawers

### 6.1 Create Instance Wizard (`CreateVMDialog`)
- **Mode Toggle**: `Simple (Presets)` vs `Advanced (Custom Hardware)`.
- **Instance Name**: Auto-generated friendly slug (e.g., `swift-puma-14`) with custom override.
- **Hardware Sizing**:
  - Tier Presets: Nano (1 vCPU, 512MB RAM), Standard (2 vCPU, 2048MB RAM), Compute (4 vCPU, 4096MB RAM), Custom.
  - Interactive sliders for vCPU, RAM (MB), and Disk Size (MB).
  - Real-time Host Capacity Bar: Shows how much host CPU/RAM will remain after provisioning.
- **Base Image & Boot Source**:
  - Select from Image Catalog, existing Snapshot, or Custom rootfs file path.
- **Network & IP Configuration**:
  - Bridge selection, Static IP request (optional), Public IP toggle.
- **SSH Key & Cloud-Init**:
  - Select user SSH key from registry or paste ad-hoc key.
  - Optional Startup Bash Script / cloud-init payload.

### 6.2 Worker Enrollment Modal (`EnrollWorkerModal`)
- **Step 1: Pool & Options**: Choose target pool (`default`), TTL (`30m`), and optional host labels (`region=us-east`, `arch=amd64`).
- **Step 2: Generate One-Time Command**:
  - Displays copyable single-line bash command:
    ```bash
    curl -fsSL https://compute.internal/install.sh | sudo sh -s -- --token compute_wtk_8f92a1... --pool default
    ```
- **Step 3: Live Handshake Indicator**:
  - Modal listens via WebSocket for worker connection.
  - When the worker connects, UI automatically displays a green checkmark, machine specs, and links to `/workers/:id`.

### 6.3 Command Execution Dialog (`ExecVMDialog`)
- Run an arbitrary command inside a running microVM without opening a full terminal.
- Input fields: Command string (`ls -la /var/log`), User (`compute` or `root`), Timeout seconds (default 30s).
- Execution Output Panel: Real-time spinner $\rightarrow$ Formatted tabs for `stdout`, `stderr`, and `Exit Code`.
- Copy output button.

### 6.4 Delete Confirmation Dialog (`DeleteVMDialog`)
- Warns of irreversible data loss.
- Displays whether attached persistent disks will be preserved or deleted.
- Requires typing the instance name or pressing explicit confirmation button to prevent accidental clicks.

---

## 7. Real-Time Streaming, WebSockets & Event Protocols

The Web UI utilizes bidirectional WebSockets and Server-Sent Events (SSE) to deliver real-time responsiveness without resource-intensive HTTP polling.

```
+---------------------------------------------------------------------------------+
|                               WEB UI CLIENT                                     |
+---------------------------------------------------------------------------------+
        ^                                  ^                             ^
        | 1. SSE Stream:                   | 2. WebSocket:               | 3. WebSocket:
        | `/v1/events/stream`              | `/v1/vms/:id/terminal`      | `/v1/jobs/:id/logs`
        v                                  v                             v
+------------------+             +-------------------+         +------------------+
| Cluster State &  |             | Full Duplex PTY   |         | ANSI Log Stream  |
| VM Status Events |             | (xterm.js binary) |         | (stdout/stderr)  |
+------------------+             +-------------------+         +------------------+
```

### 7.1 Real-Time Channels
1. **Cluster Events Stream (`/v1/events/stream` - SSE)**:
   - Delivers real-time state transitions (`vm.status_change`, `worker.heartbeat`, `queue.updated`).
   - UI automatically updates instance badges and stats counters without full page reloads.
2. **Interactive Terminal WebSocket (`/v1/vms/:id/terminal` - WS)**:
   - Binary/UTF-8 stream forwarding stdin/stdout between xterm.js and the guest SSH or serial PTY.
   - Handles terminal resize events (`{ type: "resize", cols: 120, rows: 34 }`).
3. **Job Log Streaming (`/v1/jobs/:id/logs` - WS / Chunked HTTP)**:
   - Live stream of job execution stdout and stderr frames with microsecond timestamps.

---

## 8. Error Handling, Telemetry & Operator UX

The UI enforces strict structured error reporting to eliminate mystery failures.

### 8.1 Error Presentation Schema
Every API error surfaces a structured card containing:
- **Error Title & Code**: e.g., `no_compatible_worker`, `capacity_exceeded`, `firecracker_boot_timeout`.
- **Human Message**: *"No ready worker matches requirements: arch=amd64, vcpu>=4."*
- **Remediation Suggestion**: *"Add a new worker to pool 'default' or decrease requested vCPUs."*
- **Trace ID**: Unique request tracking ID (`trace-8f921bc4`) with copy button.
- **Operator Action**: Retry button with exponential backoff indicator if safe to retry.

---

## 9. Component Architecture & State Management

### 9.1 Directory Structure
```text
web/
├── index.html
├── src/
│   ├── App.tsx                     # Main layout shell, router & global state
│   ├── components/
│   │   ├── create-vm-dialog.tsx    # Instance creation wizard
│   │   ├── enroll-worker-dialog.tsx# Worker token & enrollment modal
│   │   ├── exec-vm-dialog.tsx      # Remote command execution dialog
│   │   ├── inspect-vm-dialog.tsx   # JSON inspection sheet
│   │   ├── delete-vm-dialog.tsx    # Safe delete modal
│   │   ├── ssh-keys-dialog.tsx     # SSH key manager dialog
│   │   ├── ssh-terminal.tsx        # xterm.js terminal component
│   │   ├── status-badge.tsx        # Color-coded status badges
│   │   ├── stat-card.tsx           # Dashboard metric cards
│   │   ├── empty-state.tsx         # Reusable empty state view
│   │   ├── copy-button.tsx         # Click-to-copy helper
│   │   ├── layout/
│   │   │   ├── sidebar.tsx         # Collapsible navigation
│   │   │   ├── topbar.tsx          # Header with breadcrumbs & search
│   │   │   └── footer-status.tsx   # Connection health footer
│   │   └── ui/                     # Radix & Tailwind design primitives
│   ├── hooks/
│   │   ├── use-cluster.ts          # Central data hook with SSE / polling sync
│   │   ├── use-terminal.ts         # xterm.js lifecycle & socket manager
│   │   ├── use-worker-fleet.ts     # Distributed worker state & actions
│   │   └── use-job-stream.ts       # Live job log stream manager
│   ├── lib/
│   │   ├── api.ts                  # Typed REST API client
│   │   ├── format.ts               # Byte, memory, date & time formatters
│   │   ├── navigation.ts           # Route definitions & metadata
│   │   ├── terminal.ts             # Terminal options & popout helpers
│   │   └── types.ts                # TypeScript interfaces & types
│   └── pages/
│       ├── dashboard.tsx           # Fleet overview (/overview)
│       ├── instances.tsx           # MicroVM instances list (/instances)
│       ├── instance-detail.tsx     # Deep VM management (/instances/:id)
│       ├── workers.tsx             # Worker fleet overview (/workers)
│       ├── worker-detail.tsx       # Worker host drilldown (/workers/:id)
│       ├── queue.tsx               # Workload scheduler queue (/queue)
│       ├── jobs.tsx                # Batch & CI jobs runner (/jobs)
│       ├── console.tsx             # Dedicated interactive console (/console)
│       ├── images-templates.tsx    # Images & Templates catalog (/images)
│       ├── network.tsx             # Network topology & leases (/network)
│       ├── storage.tsx             # Persistent volumes & disks (/storage)
│       ├── access.tsx              # API keys & SSH keys (/access)
│       ├── settings.tsx            # Cluster & daemon configuration (/settings)
│       └── api.tsx                 # Interactive API documentation (/api)
```

---

## 10. Implementation Checklist & Sequence

To build out and complete the Web UI as specified, follow this phased implementation sequence:

### Phase 1: MicroVM Core Hardening (Single-Host Excellence)
- [x] Responsive layout shell, theme toggle, and navigation sidebar.
- [x] Fleet Overview dashboard with resource gauges and quick instances table.
- [x] Instances list with status filters, search, and lifecycle action buttons.
- [x] Basic Create Instance dialog with hardware tier presets.
- [x] xterm.js embedded terminal and popout window over SSH WebSocket.
- [x] Network table with TAP devices, bridge info, and public IP toggle.
- [ ] Dedicated Instance Detail view (`/instances/:id`) with multi-tab hardware, metrics, and diagnostics.
- [ ] One-click disk snapshotting and snapshot-restore lifecycle in UI.
- [ ] In-browser rootfs package builder dialog.

### Phase 2: Distributed Worker Fleet Management
- [ ] Worker fleet view (`/workers`) with capacity gauges, online/offline state, and pool tags.
- [ ] Worker enrollment modal with one-line `curl` command generator and live connection listener.
- [ ] Worker node action menu: Cordon, Uncordon, Drain, Reconcile, Revoke.
- [ ] Worker drilldown screen (`/workers/:id`) showing hosted microVMs and agent connection logs.

### Phase 3: Workload Queue & CI/Batch Job Runner
- [ ] Workload Queue view (`/queue`) with scheduling reason diagnostic alerts and priority bumper.
- [ ] Jobs list and detail views (`/jobs` & `/jobs/:id`) tracking ephemeral CI tasks.
- [ ] Live ANSI log streamer with stdout/stderr filtering and auto-scroll.
- [ ] Artifacts download table with checksum verification.

### Phase 4: Enterprise Security, Storage & Observability
- [ ] Persistent Volume manager view (`/storage`) with volume attach/detach and resizing.
- [ ] Access control view (`/access`) with scoped API token generation and revocation.
- [ ] Audit log event stream (`/events`) with trace ID filtering and JSONL export.
- [ ] Global command palette (`Cmd+K`) for instant navigation and resource search.
