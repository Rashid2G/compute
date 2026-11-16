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

The bootstrap (`computed init --images`, run by the `compute-init` service)
defaults to `auto`: when `COMPUTE_IMAGE_MANIFEST_URL` is unset, it downloads
and SHA-256-verifies the pinned Firecracker CI kernel (`vmlinux`), builds the
Ubuntu 24.04 rootfs (`ubuntu-24.04.ext4`) via chroot inside the container,
generates the internal Ed25519 SSH key, and records `.installed.json` for
idempotency.

Using `COMPUTE_IMAGES_MODE=build` or `prebuilt` **requires** `vmlinux` and
`ubuntu-24.04.ext4` to already be built or placed under
`COMPUTE_HOST_DATA_DIR/images` (`./data/images`) before starting (`make
build-images` or `scripts/build-kernel.sh` + `scripts/build-rootfs.sh`);
otherwise `compute-init` / `computed` fails with a missing `vmlinux` error.

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
