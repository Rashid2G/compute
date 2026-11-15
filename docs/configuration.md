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
| `COMPUTE_ADMIN_PASSWORD` | `admin` in Compose | Initial/synced admin password. Required when auth is enabled unless a secret is set; replace `admin` before exposing beyond loopback. |
| `COMPUTE_ADMIN_SECRET` | — | Bootstrap admin secret alternative. |
| `COMPUTE_TLS_CERT_FILE` | — | PEM certificate for native HTTPS. Must be paired with the key. |
| `COMPUTE_TLS_KEY_FILE` | — | PEM private key for native HTTPS. |
| `COMPUTE_EDGE_LISTEN` | `:80` when DNS is enabled | HTTP edge listener; `-` disables it. |
| `COMPUTE_CGROUP_ENFORCE` | `true` | Enforce per-VM cgroup limits. |
| `COMPUTE_JAILER_ENABLED` | `false` | Run Firecracker under jailer. |
| `COMPUTE_JAILER_BIN` | `/usr/local/bin/jailer` | Jailer binary. |
| `COMPUTE_JAILER_DIR` | `/srv/jailer` | Jailer base directory. |

Authentication is on by default. The daemon refuses to start when auth is
enabled and both `COMPUTE_ADMIN_PASSWORD` and `COMPUTE_ADMIN_SECRET` are empty.
It never logs a generated password; if no credential is configured for an
existing install, a one-time credential may be written to
`<master>/admin-password` with mode `0600`.

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
and SHA-256-verifies the pinned Firecracker CI kernel, builds the Ubuntu 24.04
rootfs via chroot inside the container, generates the internal Ed25519 SSH key,
and records `.installed.json` for idempotency. Downloads are written to `*.part`
and atomically renamed only after verification succeeds.

If you prefer building base images on the host or air-gapped media, run
`make build-images` (or `scripts/build-kernel.sh` + `scripts/build-rootfs.sh`)
and set `COMPUTE_IMAGES_MODE=build` or `prebuilt`.

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

## Tailscale

| Variable | Default | Description |
| --- | --- | --- |
| `COMPUTE_TAILSCALE_ENABLED` | `false` | Enable tailnet integration. |
| `COMPUTE_TAILSCALE_API_KEY` | — | Tailscale admin API key (never commit). |
| `COMPUTE_TAILSCALE_TAILNET` | `-` | Tailnet (`-` = the key's default). |
| `COMPUTE_TAILSCALE_NODE_TAG` | — | Tag for the control-plane device. |
| `COMPUTE_TAILSCALE_SANDBOX_TAG` | — | Tag for sandbox devices. |
| `COMPUTE_TAILSCALE_HOSTNAME_PREFIX` | `compute` | Hostname prefix. |
| `COMPUTE_TAILSCALE_KEY_EXPIRY_SECONDS` | 90 days | Minted key lifetime. |
| `COMPUTE_TAILSCALE_DEFAULT_SANDBOXES` | `false` | Give new sandboxes tailnet addressing. |
| `COMPUTE_TAILSCALE_LEGACY_PER_SANDBOX_DEVICES` | `false` | Legacy per-sandbox device mode. |
| `COMPUTE_TAILSCALE_SERVICE_GATEWAY_ENABLED` | `false` | Single-host Service gateway mode. |
| `COMPUTE_TAILSCALE_SERVICE_GATEWAY_HOST_TAG` | — | Host tag for the gateway. |
| `COMPUTE_TAILSCALE_SERVICE_GATEWAY_SERVICE_TAG` | — | Service tag for exposure. |
| `COMPUTE_TAILSCALE_SERVICE_GATEWAY_TARGET_ADDRESS` | — | Optional gateway target address. |

The `tailscale` Compose profile runs a containerized `tailscaled`; set
`COMPUTE_TAILSCALE_AUTHKEY` and `COMPUTE_TAILSCALE_HOSTNAME`. If the host
already runs tailscaled, skip the profile and use the host socket instead.

## Workers

Workers are configured through the same environment plus:

| Variable | Default | Description |
| --- | --- | --- |
| `COMPUTE_CONTROL_PLANE` | — | Control-plane URL (required). |
| `COMPUTE_WORKER_TOKEN` | — | Enrollment token (required unless local exec is enabled). |
| `COMPUTE_WORKER_NAME` | `compute-worker` | Stable name; the same name reconnects with the same token. |
| `COMPUTE_POOL_ID` | `default` | Scheduling pool. |
| `COMPUTE_WORKER_STATE_DIR` | `/var/lib/compute-worker` | Worker-local database and VM state. |
| `COMPUTE_WORKER_BOOT_TIMEOUT_SECONDS` | `90` | MicroVM boot timeout. |
| `COMPUTE_WORKER_ALLOW_LOCAL_EXEC` | `false` | Development-only host-shell execution. Never enable in production. |

The worker keeps its own SQLite database and VM state under
`COMPUTE_WORKER_STATE_DIR`. Because the worker runs Firecracker inside its own
container/host, restarting the worker process or container terminates its
in-flight microVMs; the control plane then re-schedules unfinished assignments.
Enrollment tokens are single-use. A token created with `--name` is bound to that
worker name, while a token created without `--name` binds to the first worker
that enrolls. In both cases the same worker name may reconnect after a restart,
including after the token's enrollment expiry; a different name is rejected.
Use `compute worker token create --name <worker-name>` (or set a stable
`COMPUTE_WORKER_NAME`) for multi-worker deployments.

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
