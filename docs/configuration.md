# Configuration reference

Compute reads configuration from four layers, highest precedence last:

1. built-in defaults (`internal/config.DefaultConfig`),
2. an optional JSON config file (`--config`, `config/daemon.json`, or the image's `/etc/compute/daemon.json`),
3. `COMPUTE_*` environment variables (the installer writes these to `.env`),
4. command-line flags (`--listen`, `--db-path`, `--images-dir`, and so on).

Environment variables apply even when no JSON config file exists, so a Docker
Compose install needs no JSON editing. Malformed values fail fast and name the
offending variable. Boolean variables accept `true/false`, `1/0`, `yes/no`, and
`on/off`.

The default install mounts only the data volume; `./config` is **not** mounted.
Use `.env` for normal configuration, or add an override file that mounts a JSON
config explicitly.

## Server and authentication

| Variable | Default | Description |
| --- | --- | --- |
| `COMPUTE_LISTEN` | `127.0.0.1:18080` | Full listen address; wins over bind/port. |
| `COMPUTE_BIND_ADDRESS` | `127.0.0.1` | Bind host when `COMPUTE_LISTEN` is unset. |
| `COMPUTE_API_PORT` | `18080` | API/embedded-GUI port. |
| `COMPUTE_HEALTHCHECK_HOST` | `127.0.0.1` | Host the Compose healthcheck calls; set it to `COMPUTE_BIND_ADDRESS` when binding a specific non-loopback address. |
| `COMPUTE_DB_PATH` | `<data>/data/compute.db` | SQLite database path. |
| `COMPUTE_DATA_DIR` | `/var/lib/compute` | Compatibility root for DB, images, VMs, and the SSH key. Explicit path variables win. |
| `COMPUTE_FIRECRACKER_BIN` | `/usr/local/bin/firecracker` | Firecracker binary. |
| `COMPUTE_AUTH_ENABLED` | `true` | Require authentication. |
| `COMPUTE_SIGNUP_ENABLED` | `false` | Allow public self-registration via `POST /api/v1/auth/signup` and the login page. |
| `COMPUTE_ADMIN_USERNAME` | `admin` | Seeded administrator username. |
| `COMPUTE_ADMIN_EMAIL` | `admin@compute.local` | Seeded administrator email address. |
| `COMPUTE_ADMIN_PASSWORD` | `admin` in Compose | Initial/synced admin password. Required when auth is enabled unless `COMPUTE_ADMIN_SECRET` is set (clear with `COMPUTE_ADMIN_PASSWORD=` in `.env`); the daemon refuses to start with `admin` on non-loopback addresses. |
| `COMPUTE_ADMIN_SECRET` | — | Bootstrap admin secret alternative. |
| `COMPUTE_TLS_CERT_FILE` | — | PEM certificate for native HTTPS. Must be paired with the key. |
| `COMPUTE_TLS_KEY_FILE` | — | PEM private key for native HTTPS. |
| `COMPUTE_PUBLIC_URL` | — | Externally reachable daemon base URL (for example `https://compute.example.com`). Used in port-mode serve URLs and advertised on `/api/v1/version`. |
| `COMPUTE_SERVE_DOMAIN` | `localhost` for loopback binds | Enables subdomain serving: exposures become `https://<slug>.<serve-domain>/`. Requires a wildcard DNS record and wildcard certificate for public domains. |
| `COMPUTE_SERVE_BIND` | `127.0.0.1` | Bind address for port-mode serve listeners. Use `0.0.0.0` to accept public traffic directly. |
| `COMPUTE_SERVE_PORT_START` | `18400` | First port of the port-mode serve range. |
| `COMPUTE_SERVE_PORT_END` | `18500` | Last port of the port-mode serve range. |
| `COMPUTE_SERVE_TLS_CERT_FILE` | API TLS certificate | Optional PEM certificate for port-mode serve listeners. |
| `COMPUTE_SERVE_TLS_KEY_FILE` | API TLS key | Optional PEM private key for port-mode serve listeners. |
| `COMPUTE_SERVE_TOKEN_KEY` | derived from `COMPUTE_ADMIN_SECRET` | HMAC key for private-serve tokens. Empty without an admin secret uses a random per-process key. |
| `COMPUTE_EDGE_LISTEN` | `:80` when DNS is enabled | HTTP edge listener; `-` disables it. |
| `COMPUTE_CGROUP_ENFORCE` | `true` | Enforce per-VM cgroup limits. |
| `COMPUTE_JAILER_ENABLED` | `false` | Run Firecracker under jailer. |
| `COMPUTE_JAILER_BIN` | `/usr/local/bin/jailer` | Jailer binary. |
| `COMPUTE_JAILER_DIR` | `/srv/jailer` | Jailer base directory. |

Authentication is on by default. The daemon refuses to start when auth is
enabled and both `COMPUTE_ADMIN_PASSWORD` and `COMPUTE_ADMIN_SECRET` are empty,
or when `COMPUTE_ADMIN_PASSWORD=admin` is used on a non-loopback listen address
(`COMPUTE_BIND_ADDRESS=0.0.0.0` or any non-loopback IP). When binding a
specific non-loopback IP, also set `COMPUTE_HEALTHCHECK_HOST` to that IP so the
Docker Compose healthcheck probes the bound interface.

`computed` already serves the embedded Web Dashboard on `:18080` alongside the
REST and WebSocket (`/api/v1/ws`) APIs; the `compute-gui` container (`:8080`,
`gui` profile) is strictly optional and disabled by default.

### Default Quickstart Credentials and Accounts

After running `./scripts/install.sh` and `docker compose up -d`, the following default accounts and credentials are initialized:

- **Web UI & Control Plane**:
  - **URL**: `http://127.0.0.1:18080/` (or `http://localhost:18080/`)
  - **Default Username**: `admin` (`COMPUTE_ADMIN_USERNAME`)
  - **Default Password**: `admin` (`COMPUTE_ADMIN_PASSWORD`)
  - **Default Email**: `admin@compute.local` (`COMPUTE_ADMIN_EMAIL`)
  - **CLI Login**: `compute auth login --username admin --password admin`
- **MicroVM / Sandbox Guest**:
  - **Default User**: `compute` (UID `1000`) with passwordless `sudo` privileges
  - **Root User**: `root` (UID `0`, shadow password locked, accessible via `sudo su -`)
  - **Guest SSH Key**: Default Ed25519 key at `./data/images/ssh/id_ed25519` installed in guest authorized_keys
  - **Guest SSH Password Auth**: Disabled (`PasswordAuthentication no`) by default; opt-in per sandbox via `--ssh-password <password>` on create

## Network and sandboxes

| Variable | Default | Description |
| --- | --- | --- |
| `COMPUTE_NETWORK_MODE` | `nat` | `nat`, `bridge`, `nic`, or `wired`. |
| `COMPUTE_BRIDGE_NAME` | `mft3br0` | Sandbox bridge name. |
| `COMPUTE_BRIDGE_IP` | `172.16.0.1/16` | Bridge address. |
| `COMPUTE_SUBNET` | `172.16.0.0/16` | Sandbox subnet. |
| `COMPUTE_GATEWAY_IP` | `172.16.0.1` | Sandbox gateway. |
| `COMPUTE_PHYSICAL_NIC` | — | Uplink NIC for `nic`/`wired` modes. |
| `COMPUTE_NETWORK_BASE_DOMAIN` | — | Base domain used for sandbox networking. |
| `COMPUTE_LINKED_BRIDGES` | — | Comma-separated host bridges to veth-link to the sandbox bridge. |
| `COMPUTE_LINK_OTHER_BRIDGES` | `false` | Link every host bridge to the sandbox bridge. Legacy behavior; creates L2 loops on shared hosts. |
| `COMPUTE_DEFAULT_VCPUS` | `1` | Default sandbox vCPUs. |
| `COMPUTE_DEFAULT_MEMORY_MB` | `2048` | Default sandbox memory. |
| `COMPUTE_DEFAULT_DISK_MB` | `51200` | Default sandbox disk (50 GiB). |
| `COMPUTE_DEFAULT_IMAGE` | `ubuntu-24` | Default image profile. |
| `COMPUTE_MAX_RUNNING_VMS` | `0` | Capacity limit (0 = unlimited). |
| `COMPUTE_MAX_TOTAL_VCPUS` | `0` | Capacity limit. |
| `COMPUTE_MAX_TOTAL_MEMORY_MB` | `0` | Capacity limit. |
| `COMPUTE_MAX_TOTAL_DISK_MB` | `0` | Capacity limit. |
| `COMPUTE_MIN_FREE_DISK_MB` | `512` | Refuse new disks below this free space. |

`computed` must run in the host network namespace. In `nat` mode the host's
default route and NIC are untouched; the daemon adds masquerade and forwarding
rules for the sandbox subnet.

## Images and SSH key

| Variable | Default | Description |
| --- | --- | --- |
| `COMPUTE_IMAGES_DIR` | `<data>/images` | Base kernel/rootfs directory. |
| `COMPUTE_VMS_DIR` | `<data>/vms` | Sandbox disk directory. |
| `COMPUTE_DEFAULT_KERNEL` | `vmlinux` | Kernel filename under the images directory. |
| `COMPUTE_DEFAULT_ROOTFS` | `ubuntu-24.04.ext4` | Root filesystem filename. |
| `COMPUTE_SSH_KEY_PATH` | `<images>/ssh/id_ed25519` | Internal SSH private key used to reach guests. |
| `COMPUTE_IMAGES_MODE` | `auto` | `auto`, `download`, `prebuilt`, `build`, or `none`. |
| `COMPUTE_IMAGE_MANIFEST_URL` | — | HTTPS URL of the signed/checksummed image manifest. |
| `COMPUTE_IMAGE_ALLOW_HTTP` | `false` | Allow plain HTTP manifest/artifact URLs for local testing. |
| `COMPUTE_ROOTFS_SIZE_MB` | `2048` | Sparse ext4 rootfs image size in MB for `auto` bootstrap. |
| `COMPUTE_KERNEL_SRC` | — | Local uncompressed ELF kernel path for air-gapped `auto` bootstrap. |
| `COMPUTE_KERNEL_URL` | — | Custom kernel HTTPS URL (must pair with `COMPUTE_KERNEL_SHA256`). |
| `COMPUTE_KERNEL_SHA256` | — | Expected SHA-256 hex digest for `COMPUTE_KERNEL_URL` or `COMPUTE_KERNEL_SRC`. |

Host image bootstrap is performed by `./scripts/install.sh` (or `make build-images` / `make up`), which downloads and SHA-256-verifies the pinned Firecracker CI kernel (`vmlinux`), generates the internal Ed25519 SSH key (`./data/images/ssh/id_ed25519`), and builds the Ubuntu 24.04 rootfs (`ubuntu-24.04.ext4`) under `COMPUTE_HOST_DATA_DIR/images` (`./data/images`) before starting `computed`. Standalone CLI deployments can also run `computed init --images` (`COMPUTE_IMAGES_MODE=auto|download|prebuilt|build|none`).

## DNS

| Variable | Default | Description |
| --- | --- | --- |
| `COMPUTE_DNS_ENABLED` | `false` | Start the authoritative DNS responder. |
| `COMPUTE_DNS_LISTEN` | `:53` | DNS listen address (UDP and TCP). |
| `COMPUTE_DNS_BASE_DOMAIN` | — | Sandbox zone, e.g. `compute.example.com`. |
| `COMPUTE_DNS_EXTRA_BASE_DOMAINS` | — | Comma-separated additional zones. |
| `COMPUTE_DNS_HOST_ADDRESSES` | — | Comma-separated addresses answered for reserved names. |

Sandbox hostnames are `<sandbox>.<username>.<zone>`; reserved labels `api`,
`gui`, and `www` plus the zone apex route to the daemon.

## Tailscale & Reverse-Proxy Exposure

Run `tailscaled` directly on the host (or terminate TLS in Caddy, nginx, or
Traefik) and point clients or Tailscale Serve/Funnel at `127.0.0.1:18080`. When
`COMPUTE_DNS_ENABLED=true` and `COMPUTE_DNS_BASE_DOMAIN` are set, configure a
Tailscale split-DNS nameserver pointing at the host's tailnet IP so
`<sandbox>.<username>.<zone>` resolves across the tailnet (see
[dns-ssh.md](dns-ssh.md) and [domain-options.md](domain-options.md)).

## Workers

Workers connect **outbound** over WebSocket RPC/TCP tunnels to `computed` (zero
inbound ports required on worker hosts) and are configured through:

| Variable | Default | Description |
| --- | --- | --- |
| `COMPUTE_CONTROL_PLANE` | — | Control-plane URL (required). |
| `COMPUTE_WORKER_TOKEN` | — | Enrollment token (required unless local exec is enabled). |
| `COMPUTE_WORKER_NAME` | `compute-worker` | Stable name; the same name reconnects with the same token. |
| `COMPUTE_POOL_ID` | `default` | Scheduling pool (`--pool`); overridden when token specifies `pool_id`. |
| `COMPUTE_WORKER_STATE_DIR` | `/var/lib/compute-worker` | Worker-local database and VM state. |
| `COMPUTE_WORKER_MAX_VCPUS` | `0` (host total) | Maximum vCPUs allocated on this worker (`--max-vcpus`). |
| `COMPUTE_WORKER_MAX_MEMORY_MB` | `0` (host total) | Maximum RAM (MB) allocated on this worker (`--max-memory-mb`). |
| `COMPUTE_WORKER_MAX_DISK_MB` | `0` (host total) | Maximum disk (MB) allocated on this worker (`--max-disk-mb`). |
| `COMPUTE_WORKER_BOOT_TIMEOUT_SECONDS` | `90` | MicroVM boot timeout. |
| `COMPUTE_WORKER_ALLOW_LOCAL_EXEC` | `false` | Development-only host-shell execution. Never enable in production. |

Pool resolution on enrollment follows `token.pool_id` -> `--pool` /
`COMPUTE_POOL_ID` -> `default`. Per-node allocation limits (`allocated_vcpus`,
`allocated_mem_mb`, `allocated_disk_mb`), pool assignment, labels, and status
can also be adjusted live via `PATCH /api/v1/workers/:id`.

The worker keeps its own SQLite database and VM state under
`COMPUTE_WORKER_STATE_DIR`. Enrollment tokens are single-use and bind to
`--name` (or the first worker that enrolls); the same worker name may reconnect
after a restart even past the 30-minute enrollment expiry.

## Cluster Scheduling Policy & Resource Limits

Configured dynamically via `GET /api/v1/scheduler/policy` and `PUT /api/v1/scheduler/policy`:

| Field | Default | Description |
| --- | --- | --- |
| `strategy` | `least_loaded` | Placement algorithm: `least_loaded`, `round_robin`, `bin_pack`, or `spread`. |
| `cpu_weight` / `memory_weight` / `disk_weight` | `0.35` / `0.40` / `0.25` | Weighted headroom scoring factors for `least_loaded` and `bin_pack`. |
| `cpu_soft_overcommit_ratio` | `1.5` | Ratio where proportional cgroup CPU weight scaling begins. |
| `cpu_hard_overcommit_ratio` | `2.0` | Hard vCPU allocation ceiling; exceeding returns HTTP `429` `ResourceLimitError`. |
| `cpu_share_weight_base` | `100` | Base cgroup v2 CPU weight per vCPU before soft-overcommit scaling. |
| `memory_hard_guarantee` | `true` | Prevents RAM overcommit across running sandboxes. |
| `idle_ram_snapshot_enabled` | `true` | Automatically snapshots idle sandboxes to SSD (`auto_hibernated=true`) to free RAM. |
| `idle_threshold_seconds` | `900` | Inactivity duration before idle SSD auto-hibernation triggers. |
| `auto_reclaim_on_pressure` | `true` | Reclaims RAM from idle non-protected sandboxes when new boots need memory. |
| `host_reserved_vcpus` / `memory_mb` / `disk_mb` | `1` / `512` / `2048` | Host safety reserve subtracted from physical capacity. |
| `disk_hard_enforcement` | `true` | Hard-limit-only disk accounting; rejections return HTTP `429` `ResourceLimitError`. |

## Key Paths for Debugging and Investigation

Compute persists its operational state, microVM disks, hypervisor logs, credentials, and templates across a set of well-defined filesystem paths rooted by default at `COMPUTE_DATA_DIR` (`/var/lib/compute` in containers, `./data` on the host in standard Compose installs). When diagnosing startup failures, VM crashes, SSH connection errors, or state discrepancies, inspect the following locations:

### 1. MicroVM Workspaces (`/var/lib/compute/vms/<sandbox_id>/`)

Each microVM sandbox has a dedicated directory indexed by its sandbox ID (`ins_...`):

| File | Purpose | Investigation Guidance |
| --- | --- | --- |
| `firecracker.log` | Firecracker serial console and hypervisor log. | **Primary diagnostic file.** Contains kernel boot messages, driver init logs, and serial console output. Inspect this immediately if an instance fails to boot or if SSH dial fails with `connection refused`. |
| `firecracker.sock` | API control socket for the Firecracker instance. | Verifies the hypervisor process is running. If absent or unresponsive while `desired_running=true`, the Firecracker process crashed or was terminated. |
| `rootfs.ext4` | Copy-on-Write (COW) or sparse root disk for the sandbox. | Can be checked offline for filesystem corruption using `e2fsck -f rootfs.ext4` or inspected via read-only loop mount (`mount -o loop,ro rootfs.ext4 /mnt`). |
| `snapshot.state` | Serialized hypervisor CPU and device architecture state. | Inspected when debugging sandbox pause, resume, or idle auto-hibernation failures. |
| `snapshot.mem` | Guest RAM memory dump accompanying `snapshot.state`. | Full memory image corresponding to the saved execution state. |

### 2. Databases & Control-Plane State (`/var/lib/compute/data/` and `master/`)

| Path | Purpose | Investigation Guidance |
| --- | --- | --- |
| `/var/lib/compute/data/compute.db` | Primary SQLite database (`COMPUTE_DB_PATH`). | Stores sandbox metadata, status, IPAM assignments, user accounts, and API keys. Query with `sqlite3 compute.db "SELECT id, name, status, ip_address, error FROM vms;"` to inspect state. |
| `/var/lib/compute/master/state.db` | Cluster control-plane state database. | Worker node registry, pool topology, and distributed job records in multi-node setups. |
| `/var/lib/compute/master/auth.db` | Dedicated authentication database (`COMPUTE_AUTH_DB_PATH`). | User credentials, password hashes, and RBAC permissions. |
| `/var/lib/compute/master/audit.db` | Immutable cryptographic audit log. | Contains tamper-evident operation records. |
| `/var/lib/compute/master/master.key` | Root cryptographic key. | Permissions must remain `0600`. Required to decrypt stored secrets and sign tokens. |
| `/var/lib/compute/master/ca.crt` & `ca.key` | Internal mTLS Root CA. | Used for mutual TLS worker-control plane communication. |

### 3. Base Images & SSH Keys (`/var/lib/compute/images/`)

| Path | Purpose | Investigation Guidance |
| --- | --- | --- |
| `/var/lib/compute/images/vmlinux` | Bootable Linux kernel (`COMPUTE_DEFAULT_KERNEL`). | Must be an uncompressed ELF binary compatible with Firecracker. |
| `/var/lib/compute/images/ubuntu-24.04.ext4` | Golden rootfs template (`COMPUTE_DEFAULT_ROOTFS`). | Base filesystem cloned into new VM root disks. |
| `/var/lib/compute/images/ssh/id_ed25519` | Internal Ed25519 private key (`0600`). | Used by the daemon to establish terminal sessions, exec commands, and forward ports. Must have `0600` permissions. |
| `/var/lib/compute/images/ssh/id_ed25519.pub` | Internal SSH public key (`0644`). | Injected into `/home/compute/.ssh/authorized_keys` and `/root/.ssh/authorized_keys` inside the rootfs. |

### 4. Templates, Volumes & Scratch Staging

| Path | Purpose | Investigation Guidance |
| --- | --- | --- |
| `/var/lib/compute/templates/` | Sandbox template disks (`<template_id>.ext4`). | Base disks for instant template instantiation. |
| `/var/lib/compute/volumes/` | Persistent secondary block devices (`<volume_id>.ext4` / `.xfs`). | Attach/detach volume storage and snapshots (`<volume_id>.ext4.snap-<N>`). |
| `/var/lib/compute/sandboxes/` | Build staging and scratch workspace. | Temporary disk operations during custom image creation. |

### 5. Cgroups, Jailer & Host-Level Isolation

| Path | Purpose | Investigation Guidance |
| --- | --- | --- |
| `/sys/fs/cgroup/compute/<sandbox_id>/` | Cgroup v2 resource limits per microVM. | Check `memory.current` vs `memory.max` to detect guest OOM events; check `cpu.stat` for CPU throttling. |
| `/srv/jailer/firecracker/<sandbox_id>/root/` | Jailer chroot jail directory (`COMPUTE_JAILER_DIR`). | When `COMPUTE_JAILER_ENABLED=true`, Firecracker executes inside this restricted chroot. |

### 6. Client CLI & Multi-Node Workers

| Path | Purpose | Investigation Guidance |
| --- | --- | --- |
| `~/.compute/config.json` | Local user CLI credentials and tokens. | Stores API token from `compute auth login`. Clear or inspect if CLI returns unauthorized errors. |
| `/var/lib/compute-worker/` | Worker node state (`COMPUTE_WORKER_STATE_DIR`). | Contains `worker.db` and worker-managed sandbox disks under `vms/`. |
| `/tmp/compute-sandboxes/<job-id>/` | Ephemeral CI/distributed job checkouts. | Working directory where worker supervisor runs build tasks. |

## JSON config file

`config/daemon.json` contains neutral defaults; `config/daemon.example.json`
documents the advanced fields. The default Compose install does not mount
`./config`. To use a JSON file, add an override:

```yaml
# docker-compose.override.yml
services:
  computed:
    command: ["--config", "/etc/compute/daemon.json"]
    volumes:
      - ./config/daemon.json:/etc/compute/daemon.json:ro
```

Environment variables and CLI flags still win over file values.
