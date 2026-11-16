# Install Compute on a fresh Linux host

This is the supported installer path: a Linux host with Docker and `/dev/kvm`
runs the control plane (`computed`) and its embedded web UI directly with
`docker compose up -d`. **No `.env` file, no host `firecracker` binary, and no
manual image build commands are required.**

The default install runs **only the daemon** on the host. Sandboxes are
Firecracker microVMs created and supervised by that daemon (the Firecracker and
Jailer binaries are bundled inside the container image). Multi-host workers and
a separate GUI container are opt-in profiles.

## Host Dependencies

| Dependency | Details |
| --- | --- |
| OS | Linux, `x86_64` or `arm64` |
| Virtualization | KVM enabled (`/dev/kvm` present; nested virtualization enabled on VM hosts) |
| Kernel | cgroup v2 unified hierarchy (`/sys/fs/cgroup/cgroup.controllers`); `tun`, `bridge`, and `br_netfilter` modules (`vhost_net` optional) |
| Docker | Docker Engine 24+ (**rootful**) with the Compose v2 plugin (2.20+) |
| Firecracker | **Not required on host** — bundled inside the daemon/worker container image |
| Disk | 10 GB free minimum; base images plus VM disks need additional sparse/disk headroom |
| Memory | 4 GB recommended; sandboxes default to 2 GB RAM and queue when host memory is insufficient |
| Privileges | `compute-init` and `computed` run `privileged` with `network_mode: host` and `cgroup: host` |

Verify KVM support on your host (especially inside a cloud VM, Proxmox, or VMware guest):

```bash
ls -l /dev/kvm
grep -E 'vmx|svm' /proc/cpuinfo
```

## 1. Quickstart (`docker compose up -d`)

From a fresh clone:

```bash
docker compose up -d
```

What happens automatically:

1. Compose builds the container image from `Dockerfile.daemon` (includes `computed`, `compute`, `firecracker`, `jailer`, and rootfs tools).
2. `compute-init` runs once (`COMPUTE_IMAGES_MODE=auto`):
   - generates the internal SSH key at `./data/images/ssh/id_ed25519`
   - downloads and SHA-256-verifies the pinned Firecracker CI kernel (`./data/images/vmlinux`)
   - builds the Ubuntu 24.04 ext4 root filesystem (`./data/images/ubuntu-24.04.ext4`)
3. `computed` starts on `127.0.0.1:18080`, configures the `mft3br0` bridge, NAT/forwarding rules, and `net.ipv4.ip_forward=1`, and mounts the **embedded Web Dashboard** on the same `:18080` port.

Watch progress during the initial bootstrap:

```bash
docker compose logs -f compute-init computed
```

### Embedded Web UI vs. Optional `compute-gui`

> **Important**: `computed` on `:18080` **already serves** both the REST/WebSocket API and the full embedded Web Dashboard at `http://<host>:18080/`. You do **not** need `compute-gui` to use the Web UI.

`compute-gui` (`:8080`, enabled via `docker compose --profile gui up -d`) is strictly optional and disabled by default — use it only if you want a separate frontend proxy listener on a different port.

## 2. Preflight & `.env` Configuration (Required for Non-Loopback)

If a pre-check is desired before starting (for instance on a custom kernel):

```bash
./scripts/preflight.sh
# Optional if kernel modules need loading ahead of time:
sudo ./scripts/host-prepare.sh
```

Out of the box, no `.env` file is needed. The daemon binds to loopback
(`127.0.0.1:18080`) with default administrator credentials (`admin` / `admin`).
To customize settings or expose the API beyond loopback, create `.env`:

```bash
cp .env.example .env
chmod 600 .env
```

### Non-Loopback Bind Credentials (`COMPUTE_BIND_ADDRESS=0.0.0.0`)

> **Startup Trap — Non-Loopback Refusal**: If you set `COMPUTE_BIND_ADDRESS=0.0.0.0` (or any non-loopback IP) **without** overriding the default `COMPUTE_ADMIN_PASSWORD=admin` (or clearing `COMPUTE_ADMIN_PASSWORD=` and setting `COMPUTE_ADMIN_SECRET`), `computed` **refuses to start** with:
> `Refusing to start on non-loopback address ... with default admin password "admin"`.

Copy-paste `.env` snippet for remote/LAN/Tailscale access using a password:

```dotenv
COMPUTE_BIND_ADDRESS=0.0.0.0
COMPUTE_ADMIN_PASSWORD=replace-with-openssl-rand-base64-32
```

Or authenticate via static admin secret instead of a seeded password (you **must** clear `COMPUTE_ADMIN_PASSWORD=` to an empty value so `docker-compose.yml` does not inject the default `admin` password):

```dotenv
COMPUTE_BIND_ADDRESS=0.0.0.0
COMPUTE_ADMIN_PASSWORD=
COMPUTE_ADMIN_SECRET=replace-with-openssl-rand-base64-32
```

> **Healthcheck Host Note**: When binding a **specific non-loopback IP** (e.g. `COMPUTE_BIND_ADDRESS=10.0.0.5`) instead of `0.0.0.0`, also set `COMPUTE_HEALTHCHECK_HOST=10.0.0.5` in `.env` so the Docker Compose healthcheck curls the bound interface rather than `127.0.0.1`.

`.env` is git-ignored; never commit it. See [configuration.md](configuration.md) for every variable.

### Base Images (`COMPUTE_IMAGES_MODE=auto` vs. `build` / `prebuilt`)

`compute-init` bootstraps the kernel, root filesystem, and internal SSH key before the daemon starts. Select a mode with `COMPUTE_IMAGES_MODE`:

| Mode | Behavior |
| --- | --- |
| `auto` (default) | Automatically downloads and SHA-256-verifies `vmlinux`, builds `ubuntu-24.04.ext4` via chroot inside `compute-init`, and generates `./data/images/ssh/id_ed25519` (or fetches `COMPUTE_IMAGE_MANIFEST_URL` if set). Existing valid files are reused. |
| `download` | Fetches the checksummed manifest at `COMPUTE_IMAGE_MANIFEST_URL`, verifies SHA-256, and installs the artifacts. |
| `prebuilt` | **Requires** `vmlinux` and `ubuntu-24.04.ext4` to already exist under `./data/images` (`COMPUTE_HOST_DATA_DIR/images`) before starting. |
| `build` | Same as `prebuilt` — **requires** building `vmlinux` + `ubuntu-24.04.ext4` under `./data/images` first (`make build-images` or `scripts/build-kernel.sh` + `scripts/build-rootfs.sh`). |
| `none` | Skips validation; readiness reports the missing files. |

> **Startup Trap — Missing `vmlinux` in `build` / `prebuilt` Mode**: If you switch `COMPUTE_IMAGES_MODE` to `build` or `prebuilt` without building or placing `vmlinux` and `ubuntu-24.04.ext4` into `./data/images` first, `compute-init` / `computed` fails with `kernel is missing at .../vmlinux`.

To pre-build base images on the host (`make build-images`):

```bash
sudo ./scripts/build-kernel.sh ./data/images
sudo ./scripts/build-rootfs.sh ./data/images 2048
```

On an air-gapped host, supply a local uncompressed ELF kernel with
`COMPUTE_KERNEL_SRC=/path/to/vmlinux` (or `COMPUTE_KERNEL_URL` +
`COMPUTE_KERNEL_SHA256`), or pre-populate `./data/images/` and set
`COMPUTE_IMAGES_MODE=prebuilt`. The bootstrap is idempotent and safe to run on
every `docker compose up`.

## 3. Verify

```bash
curl -fsS http://127.0.0.1:18080/api/v1/health
curl -fsS http://127.0.0.1:18080/api/v1/ready | python3 -m json.tool
```

`/api/v1/health` is liveness only. `/api/v1/ready` returns `503` with a
per-check report and remediation when KVM, Firecracker, images, the SSH key,
the database, cgroups, or the sandbox bridge are not usable.

The same report is available through the CLI:

```bash
./bin/compute doctor
# or, from the daemon image:
docker compose exec computed /usr/local/bin/compute doctor
```

Open the embedded Web Dashboard at <http://127.0.0.1:18080/>.

## 4. First Sandbox

Point the CLI at the daemon and log in:

```bash
export COMPUTE_DAEMON=127.0.0.1:18080
./bin/compute auth login --username admin
./bin/compute sandbox create --name demo --image ubuntu-24
./bin/compute sandbox ssh demo -- uname -a
./bin/compute sandbox delete demo
```

If the host has less than ~2.5 GB of available memory, the default 2 GB
sandbox is placed in the admission queue instead of booting. `compute sandbox
info` prints the queue reason; create a smaller sandbox with `--memory 1024`
or free host memory and run `compute sandbox start demo`.

The host should show `mft3br0` and, while a sandbox runs, `mftap-*` devices.
Sandbox guests receive addresses from the configured NAT subnet.

## 5. Multi-Node Workers, Load Balancing & Live Migration

Single-host installs run sandboxes and jobs locally on `nod_host_local` without needing remote workers. To scale across multiple Linux KVM hosts, enroll `compute-worker` nodes over outbound WebSockets.

### Worker Enrollment & Outbound Tunnel

Workers connect **outbound** via WebSocket RPC and multiplexed TCP tunnels to the control plane — **zero inbound firewall ports are needed on worker hosts**. Control-plane SSH, file sync, exec, process control, and migration disk streaming tunnel back through that connection.

1. Mint an enrollment token on the control plane:

   ```bash
   compute worker token create --name worker-eu-1 --pool gpu-pool
   ```

   **Pool precedence**: When a worker enrolls, its pool resolves in order: `token.pool_id` (if set on the token) -> `--pool` / `COMPUTE_POOL_ID` -> `default`. Missing pools are auto-created on enrollment.

2. Start `compute-worker` on the remote KVM host (binary or Docker Compose `--profile worker`):

   ```bash
   # Standalone binary
   compute-worker run \
     --control-plane http://<control-plane-host>:18080 \
     --token <printed-token> \
     --name worker-eu-1 \
     --pool gpu-pool \
     --max-vcpus 16 --max-memory-mb 32768 --max-disk-mb 204800
   ```

   Or with Docker Compose on a worker host:

   ```bash
   # Put COMPUTE_CONTROL_PLANE, COMPUTE_WORKER_TOKEN, COMPUTE_WORKER_NAME, and COMPUTE_POOL_ID in .env
   docker compose --profile worker up -d
   ```

   `--name` pins the token to that worker name (without `--name`, the token binds to the first worker that enrolls). The worker may reconnect with the same name and token after restarts, even past the 30-minute enrollment window.

### Node Limits & Configurable Load Balancer

- **Per-node resource allocation limits** (`PATCH /api/v1/workers/:id`): Dynamically cap or adjust `allocated_vcpus`, `allocated_mem_mb`, `allocated_disk_mb`, `pool_id`, `labels`, and `status` (`ready`, `cordoned`, `draining`, `offline`). Updates to `nod_host_local` apply immediately to local admission.
- **Load balancer strategies** (`GET/PUT /api/v1/scheduler/policy`):
  - `least_loaded` *(default)* — scores weighted CPU (`0.35`), RAM (`0.40`), and Disk (`0.25`) headroom.
  - `round_robin` — rotates deterministically across eligible ready nodes.
  - `bin_pack` — packs onto the fullest eligible node first to keep other nodes free.
  - `spread` — chooses the node with the fewest running VMs (tie-breaking on lowest used RAM).
- **Resource accounting & overcommit**:
  - **CPU (soft + hard overcommit)**: `cpu_soft_overcommit_ratio` (default `1.5`) and `cpu_hard_overcommit_ratio` (default `2.0`). Up to the soft ratio, VMs get full cgroup CPU weight (`cpu_share_weight_base=100`); between soft and hard, weights scale proportionally; requests exceeding the hard ceiling are rejected.
  - **RAM (hard guarantee + idle SSD auto-snapshotting)**: `memory_hard_guarantee` (default `true`) prevents RAM overcommit. When `idle_ram_snapshot_enabled=true` and `idle_threshold_seconds` (default `900`) elapses — or on memory pressure when `auto_reclaim_on_pressure=true` — idle non-protected sandboxes are automatically checkpointed to SSD (`status=paused`, `auto_hibernated=true`) to reclaim RAM.
  - **Disk (hard-limit-only)**: `disk_hard_enforcement` (default `true`) enforces `allocated_disk_mb`, `host_reserved_disk_mb` (`2048` MB default), and physical free space. Any disk, RAM, or hard CPU limit violation returns **HTTP `429 Too Many Requests`** with a structured JSON `ResourceLimitError` (`code: "RESOURCE_LIMIT_EXCEEDED"`, `resource`, `limit_type`, `node_id`, `requested`, `available`, `limit`, `suggestion`).

### Live Sandbox & Pool Migration, Bulk Ops & Real-Time UI Sync

- **Sandbox migration** (`POST /api/v1/vms/:id/migrate`): Moves a sandbox (including chunked gzipped disk + snapshot/memory state for paused VMs) to `target_worker_id` or `target_pool_id` (pass `"force": true` to stop, migrate, and restart a running sandbox; rolls back on error).
- **Pool migration** (`POST /api/v1/pools/:id/migrate`): Moves workers (`migrate_workers`) and/or capacity-preflighted sandboxes (`migrate_sandboxes`) out of a pool.
- **Bulk operations**: `POST /api/v1/vms/bulk-delete` (`{"ids": [...]}`) and `POST /api/v1/vms/bulk-action` (`{"ids": [...], "action": "start|stop|save-state|snapshot|resume|delete"}`) execute across up to 4 concurrent workers per request.
- **Real-time `/api/v1/ws` UI sync**: The embedded Web Dashboard maintains a persistent RBAC-scoped WebSocket connection to `/api/v1/ws`, receiving immediate state snapshots on connect and cluster mutations.

## Optional Profiles

Enable a profile with `--profile <name>`; profiles are additive.

| Profile | Service | Purpose |
| --- | --- | --- |
| `gui` | `compute-gui` | Optional separate GUI proxy on `COMPUTE_GUI_PORT` (default 8080). `computed` on `:18080` already serves the embedded UI. |
| `worker` | `compute-worker` | Multi-host worker agent. Requires `COMPUTE_WORKER_TOKEN` and base images under `COMPUTE_HOST_DATA_DIR/images`. |
| `dev` | `compute-dev` | Reserved for local development overrides. |

### DNS and HTTP Edge

Set `COMPUTE_DOMAIN` and `COMPUTE_DNS_ENABLED=true` to run the authoritative DNS
responder (port 53) and HTTP edge (port 80), for example
`<sandbox>.<username>.<domain>`. Port 53 is frequently owned by
systemd-resolved; delegate the zone or disable DNS if you cannot free it.

### TLS

Leave `COMPUTE_TLS_CERT_FILE`/`COMPUTE_TLS_KEY_FILE` empty and terminate TLS in
a reverse proxy (Caddy, nginx, Traefik) or Tailscale, or set both paths and
mount the certificate files into the container to serve HTTPS directly.

## Standalone Pre-Built Binaries

If you prefer to run the `compute` CLI or `computed` daemon directly without building from source or running inside Docker, download standalone pre-compiled binaries from [GitHub Releases](https://github.com/Rashid2G/compute/releases).

Every release publishes static Linux binaries for both `x86_64` (`amd64`) and `aarch64` (`arm64`), along with a cryptographic `checksums.txt` file.

### Direct Download Links

| Component | Linux `amd64` (`x86_64`) | Linux `arm64` (`aarch64`) |
| :--- | :--- | :--- |
| **`compute` CLI** | [Download](https://github.com/Rashid2G/compute/releases/latest/download/compute_1.0.0_linux_amd64.tar.gz) | [Download](https://github.com/Rashid2G/compute/releases/latest/download/compute_1.0.0_linux_arm64.tar.gz) |
| **`computed` Daemon** | [Download](https://github.com/Rashid2G/compute/releases/latest/download/computed_1.0.0_linux_amd64.tar.gz) | [Download](https://github.com/Rashid2G/compute/releases/latest/download/computed_1.0.0_linux_arm64.tar.gz) |
| **`compute-worker`** | [Download](https://github.com/Rashid2G/compute/releases/latest/download/compute-worker_1.0.0_linux_amd64.tar.gz) | [Download](https://github.com/Rashid2G/compute/releases/latest/download/compute-worker_1.0.0_linux_arm64.tar.gz) |
| **Complete Suite** | [Download](https://github.com/Rashid2G/compute/releases/latest/download/compute-suite_1.0.0_linux_amd64.tar.gz) | [Download](https://github.com/Rashid2G/compute/releases/latest/download/compute-suite_1.0.0_linux_arm64.tar.gz) |

### Install the `compute` CLI

```bash
# Download and unpack CLI to /usr/local/bin
curl -fsSL https://github.com/Rashid2G/compute/releases/latest/download/compute_1.0.0_linux_amd64.tar.gz | sudo tar -xz -C /usr/local/bin
compute --version
```

### Verifying Checksums

```bash
curl -fsSLO https://github.com/Rashid2G/compute/releases/latest/download/checksums.txt
curl -fsSLO https://github.com/Rashid2G/compute/releases/latest/download/compute_1.0.0_linux_amd64.tar.gz
sha256sum --check --ignore-missing checksums.txt
```

## Upgrade

Source installs build the image locally:

```bash
git pull
docker compose build
docker compose up -d
```

If you use published images, bump `COMPUTE_VERSION` in `.env` to the new
release tag, then run `docker compose pull` before `docker compose up -d`.

The data volume (`COMPUTE_HOST_DATA_DIR`, default `./data`) and database are
preserved. A Docker restart replaces the container's PID namespace, so the
Firecracker processes die with the daemon: on startup the control plane marks
those sandboxes `stopped` and restarts the ones with `desired_running=true`.
Before exiting, the daemon asks running guests to `sync` their disks so writes
are not lost and the filesystems stay consistent. Expect running sandboxes to
reboot once; their disks and data survive. (The legacy systemd install keeps the
daemon process separate from Firecracker, so there it re-adopts running VMs by
PID instead.)

If you run the legacy systemd units instead of Compose, add
`COMPUTE_ADMIN_PASSWORD` (or `COMPUTE_ADMIN_SECRET`) to the daemon's
`EnvironmentFile` before upgrading: authentication is now enabled by default
and `computed` refuses to start without a credential.

## Backup

Stop the daemon and sandboxes first so the SQLite database and VM disks are
consistent, then archive the data directory. The daemon writes as root inside
the container (database, `images/ssh` private key, VM disks), so the archive
command needs `sudo`. VM disks are sparse 50 GB files: `--sparse` keeps the
archive small instead of copying tens of gigabytes of holes.

```bash
./bin/compute sandbox stop '*' || true   # quoted: the CLI expands the glob
docker compose stop computed
sudo tar --sparse -C ./data -czf compute-backup-$(date +%F).tar.gz .
docker compose start computed
```

The archive contains the SQLite database, base images, VM disks, and the
internal SSH key. Store it encrypted; the SSH key grants access to sandboxes.
On a typical host, a full archive of a small install is roughly 500 MB and takes
several minutes, mostly gzip on the multi-gigabyte images.

If you only need the control-plane state (no VM disks), back up
`data/data/compute.db` and `data/images/ssh/` instead.

## Uninstall

```bash
docker compose --profile gui --profile worker down
# remove containers, network state and volumes created by this project
docker compose down -v
# optionally remove the host data directory (irreversible)
sudo rm -rf ./data
```

The host prep changes (`sysctl net.ipv4.ip_forward=1`, loaded modules, the
`mft3br0` bridge) are not reverted automatically.

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| Daemon refuses startup on `0.0.0.0` | Set a non-default `COMPUTE_ADMIN_PASSWORD` in `.env`, or set `COMPUTE_ADMIN_PASSWORD=` (empty) with `COMPUTE_ADMIN_SECRET=...`. |
| `compute-init` fails in `auto` mode | Ensure outbound HTTPS is reachable from `compute-init` (or set `COMPUTE_KERNEL_SRC` / `COMPUTE_IMAGE_MANIFEST_URL` / `COMPUTE_IMAGES_MODE=prebuilt`), then re-run `docker compose up -d`. |
| `compute-init` fails with `kernel is missing at .../vmlinux` | Use the default `COMPUTE_IMAGES_MODE=auto` so `compute-init` installs it automatically, or run `make build-images` (`scripts/build-kernel.sh` + `scripts/build-rootfs.sh`) before using `build` or `prebuilt`. |
| Readiness fails with `kernel`/`rootfs` | Check `COMPUTE_HOST_DATA_DIR/images`, re-run `docker compose up compute-init`, and confirm the image files are valid ELF/ext4. |
| Readiness fails with `kvm` | Enable virtualization/nested KVM on the host and ensure `/dev/kvm` is passed through (`chmod 666 /dev/kvm` on the host if needed). |
| `host_network` readiness failure | `computed` must keep `network_mode: host` and set `COMPUTE_HOST_NETWORK=1`; bridge-mode networking hides the bridge/TAPs from the host. `docker run` users need the same flag. |
| `bridge` readiness failure | The daemon creates `mft3br0`; check `docker compose logs computed` for NET_ADMIN or permission errors, and run `scripts/host-prepare.sh`. |
| Healthcheck fails with specific `COMPUTE_BIND_ADDRESS` | Set `COMPUTE_HEALTHCHECK_HOST` in `.env` to match `COMPUTE_BIND_ADDRESS`. |
| Port 18080/80/53 already in use | Change `COMPUTE_API_PORT` in `.env`, or stop the conflicting service (systemd-resolved for 53). |
| Login fails right after install | Default loopback credentials are `admin` / `admin`; override `COMPUTE_ADMIN_PASSWORD` in `.env` and restart `computed` when needed. |
