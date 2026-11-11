# Compute CLI Reference

Complete reference for the `compute` command-line interface.

## Global Options & Environment Variables

| Variable / Option | Default | Description |
| :--- | :--- | :--- |
| `COMPUTE_DAEMON` | `127.0.0.1:18080` | URL or host:port of the control-plane daemon (`computed`). Supports HTTP (`127.0.0.1:18080`), HTTPS (`https://compute.dev`), or Unix socket (`unix:///path/to/socket`). |
| `COMPUTE_HOST` | - | Secondary fallback environment variable for daemon address. |
| `COMPUTE_API_KEY` | - | Authentication API key or session token. If unset, reads from `~/.compute/config.json`. |
| `COMPUTE_DATA_DIR`| `/var/lib/compute`| Daemon data directory for storage, kernels, and DB. |

---

## Command Reference

### 1. Authentication (`compute auth`)

```bash
compute auth <subcommand> [flags]
```

- `compute auth login [--username <user> --password <pass>]`
  Authenticate with user credentials. Interactively prompts if flags are omitted. Generates a machine API key (`cli-<username>`) and saves credentials in `~/.compute/config.json`.
- `compute auth signup --email <email> --username <user> --password <pass> [--name <display-name>]`
  Create a new user account on the daemon.
- `compute auth whoami`
  Display currently authenticated user ID, email, username, display name, system role, organization ID, and organization role.
- `compute auth keys [list]`
  List active API keys with key ID, name, prefix, and creation timestamp.
- `compute auth keys create --name <name> [--expires <days>]`
  Generate a new long-lived API key. Prints the secret key once.
- `compute auth keys revoke <id>`
  Revoke and permanently delete an API key.
- `compute auth logout`
  Clear local API key and user profile from `~/.compute/config.json`.

---

### 2. Distributed Jobs (`compute job`)

```bash
compute job run [flags] -- <command...>
compute job list
compute job info <id>
compute job logs <id>
compute job cancel <id>
```

#### Flags for `compute job run`:
- `--files <path>`: Local directory or file to package into a `.tar.gz` archive (base64) and unpack inside the sandbox before execution.
- `--cwd <path>`: Working directory inside the sandbox (default: `/workspace`).
- `--image <name>`: Rootfs image name (default: daemon default image).
- `--pool <pool>`: Worker pool ID or name (default: `default`).
- `--vcpus <n>`: vCPU count required (default: 2).
- `--mem <MiB>`: Memory required in MiB (default: 2048).
- `--timeout <seconds>`: Maximum execution duration in seconds (0 = unlimited).
- `--follow`: Stream output logs in real-time (default: `true`). If set to `false`, outputs job ID immediately and exits.

#### Exit Codes:
- `0`: Job state `succeeded`.
- `1`: Job state `failed` or submission error.
- `130`: Job state `canceled`.

---

### 3. Sandboxes & MicroVMs (`compute sandbox` / `compute vm`)

```bash
compute sandbox <subcommand> [flags]
```

#### Subcommands:
- `create`
  - `--name <name>`: Human-readable name.
  - `--tier <tier>`: Preset tier (`1vcpu`, `2vcpu`, `4vcpu`).
  - `--vcpus <n>`: Explicit vCPU count (1, 2, or 4).
  - `--memory <mb>`: Explicit RAM size in MiB.
  - `--disk <size>`: Disk size in MiB or GiB.
  - `--image <name>`: Base rootfs image name.
  - `--template <id|name>`: Launch from a disk template.
  - `--ssh-key <id|name|path>`: Pre-authorize SSH public key.
  - `--protected`: Enable deletion protection.
- `start <name|id|pattern>...` (alias: `run`)
  Launch one or more microVMs. Spawns Firecracker, attaches TAP device, binds veth, and boots Linux guest. Supports shell wildcards (e.g. `'pr-*'`).
- `stop <name|id|pattern>...`
  Gracefully stop running microVMs, unbind network, and persist rootfs.
- `save-state <name|id>` (alias: `snapshot`)
  Pause guest execution, dump microVM memory and CPU registers to disk snapshot. Sandbox state transitions to `paused`.
- `resume <name|id>`
  Instant hot-resume from snapshot. Memory and running processes are fully preserved.
- `delete <name|id|pattern>...` (alias: `rm`)
  Permanently destroy sandboxes and rootfs storage. Blocked if protection is enabled.
- `protect <name|id>`
  Enable deletion protection.
- `unprotect <name|id>`
  Disable deletion protection.
- `info <name|id>` (aliases: `inspect`, `show`)
  Show detailed metadata: ID, Name, Status, IP, MAC, TAP device, vCPUs, Memory, Disk size, Creation time.
- `exec [flags] [user@]<name|id> -- <command...>`
  Execute a command inside the running sandbox.
  - `--no-stream`: Buffer output until command finishes.
  - `--json`: Output result as JSON `{exit_code, stdout, stderr, duration_ms}`.
  - `--timeout <seconds>`: Command timeout.
  - `--user <username>`: Guest user to execute as (default: `compute`, with passwordless sudo).
- `ssh [user@]<name|id> [<command...>]`
  Interactive terminal session or one-off command over WebSocket tunnel.

---

### 4. Networking (`compute network`)

```bash
compute network <subcommand> [flags]
```

- `status` (aliases: `info`, `show`)
  Display network configuration: bridge interface name, bridge IP, subnet CIDR, gateway, netmask, NAT masquerading status, allocated IPs count, and active TAP devices.
- `list` (alias: `ls`)
  List all active IP allocations with VM ID, Name, Status, IP Address, Public IP, MAC, and TAP device name.
- `inspect <name|id>`
  Inspect network connectivity, bridge routing, IP assignment, and ICMP reachability to a specific sandbox.
- `prune`
  Scan host network namespace and remove orphaned TAP interfaces, stale veth pairs, and dangling database IP allocations.

---

### 5. Images & Templates

```bash
compute image list
compute image inspect <name|id>
compute image build --name <name> --source <dir|dockerfile>
compute image delete <name|id>

compute template list
compute template create --name <name> --sandbox <sandbox-id>
compute template inspect <name|id>
compute template launch <name|id> [--name <new-sandbox-name>]
compute template delete <name|id>
```

---

### 6. Web Dashboard GUI (`compute gui` / `compute ui`)

```bash
compute gui [--port 8080] [--addr 0.0.0.0] [--daemon 127.0.0.1:18080]
```
Launches the embedded React/Vite dashboard, connecting to the specified daemon address.
