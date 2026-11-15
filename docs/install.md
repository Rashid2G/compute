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
3. `computed` starts on `127.0.0.1:18080`, configures the `mft3br0` bridge, NAT/forwarding rules, and `net.ipv4.ip_forward=1`, and mounts the embedded Web Dashboard.

Watch progress during the initial bootstrap:

```bash
docker compose logs -f compute-init computed
```

## 2. Optional Preflight & Configuration (`.env`)

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

For production or non-loopback deployments (`COMPUTE_BIND_ADDRESS=0.0.0.0`),
always override the default credentials with strong random values
(`openssl rand -base64 32`) — the daemon refuses to start with password
`admin` on a non-loopback address:

```dotenv
COMPUTE_BIND_ADDRESS=0.0.0.0
COMPUTE_ADMIN_PASSWORD=replace-with-a-long-random-password
# or a bootstrap secret instead of a password (leave COMPUTE_ADMIN_PASSWORD= empty):
# COMPUTE_ADMIN_PASSWORD=
# COMPUTE_ADMIN_SECRET=...
```

`.env` is git-ignored; never commit it. See [configuration.md](configuration.md)
for every variable.

### Base images & offline/custom options

`compute-init` bootstraps the kernel, root filesystem, and internal SSH key
before the daemon starts. Select a mode with `COMPUTE_IMAGES_MODE`:

| Mode | Behavior |
| --- | --- |
| `auto` (default) | If `COMPUTE_IMAGE_MANIFEST_URL` is set, behaves like `download`; otherwise installs the pinned Firecracker CI `vmlinux`, builds `ubuntu-24.04.ext4` via chroot inside `compute-init`, and creates the internal SSH key. Existing valid files are reused. |
| `download` | Fetches the checksummed manifest at `COMPUTE_IMAGE_MANIFEST_URL`, verifies SHA-256, and installs the artifacts. |
| `prebuilt` | Validates kernel/rootfs files already present under `COMPUTE_HOST_DATA_DIR/images`. |
| `build` | Same as `prebuilt`, with remediation messages pointing at the manual build scripts. |
| `none` | Skips validation; readiness reports the missing files. |

Optional host-side scripts remain available if you prefer building images on the host (`make build-images`):

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

Open the embedded GUI at <http://127.0.0.1:18080/>.

## 4. First sandbox

Point the CLI at the daemon and log in:

```bash
export COMPUTE_DAEMON=127.0.0.1:18080
./bin/compute auth login --username admin
./bin/compute sandbox create --name demo --image ubuntu-24
./bin/compute sandbox ssh demo -- uname -a
./bin/compute sandbox delete demo

If the host has less than ~2.5 GB of available memory, the default 2 GB
sandbox is placed in the admission queue instead of booting. `compute sandbox
info` now prints the queue reason; create a smaller sandbox with
`--memory 1024` or free host memory and run `compute sandbox start demo`.
```

The host should show `mft3br0` and, while a sandbox runs, `mftap-*` devices.
Sandbox guests receive addresses from the configured NAT subnet.

## Optional profiles

Enable a profile with `--profile <name>`; profiles are additive.

| Profile | Service | Purpose |
| --- | --- | --- |
| `gui` | `compute-gui` | Separate GUI container on `COMPUTE_GUI_PORT` (default 8080). The daemon already serves the embedded UI. |
| `worker` | `compute-worker` | Experimental multi-host worker. Requires `COMPUTE_WORKER_TOKEN` and a shared image directory. |
| `dev` | Reserved for local development overrides. |

```bash
docker compose --profile gui up -d
```

### Worker (experimental)

The default single-host install does not need a worker: the control plane runs
sandboxes locally. A worker executes assigned jobs in its own Firecracker VMs
and reconnects to the control plane with the same enrollment token and name
after a restart.

```bash
./bin/compute worker token create --name docker-worker
# put the printed token in .env as COMPUTE_WORKER_TOKEN
docker compose --profile worker up -d
```

`--name` pins the token to that worker name. Without it, the token binds to the
first worker that enrolls. Either way the worker may reconnect with the same
name and token after a restart, including after the 30-minute enrollment
expiry; a different name is rejected. Set `COMPUTE_WORKER_NAME` to the same
value on the worker so the identity stays stable.

The worker must share the base images (`COMPUTE_HOST_DATA_DIR/images`) and have
its own `/dev/kvm`, `cgroup: host`, and privileged access. It refuses to start
without a token. `COMPUTE_WORKER_ALLOW_LOCAL_EXEC=true` is a development-only
escape hatch that runs commands on the host shell; never enable it in
production.

The `worker` profile is intended for a separate host. Running it on the same
host as `computed` makes both deployments share `/dev/kvm`, the host sandbox
bridge/subnet, and capacity; use that only for smoke tests.

### DNS and HTTP edge

Set `COMPUTE_DOMAIN` and `COMPUTE_DNS_ENABLED=true` to run the authoritative DNS
responder (port 53) and HTTP edge (port 80), for example
`<sandbox>.<username>.<domain>`. Port 53 is frequently owned by
systemd-resolved; delegate the zone or disable DNS if you cannot free it.

### TLS

Leave `COMPUTE_TLS_CERT_FILE`/`COMPUTE_TLS_KEY_FILE` empty and terminate TLS in
a reverse proxy (Caddy, nginx, Traefik) or Tailscale, or set both paths and
mount the certificate files into the container to serve HTTPS directly.

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
| `compute-init` fails in `auto` mode | Ensure outbound HTTPS is reachable from `compute-init` (or set `COMPUTE_KERNEL_SRC` / `COMPUTE_IMAGE_MANIFEST_URL` / `COMPUTE_IMAGES_MODE=prebuilt`), then re-run `docker compose up -d`. |
| `compute-init` fails with `kernel is missing at .../vmlinux` | Use the default `COMPUTE_IMAGES_MODE=auto` so `compute-init` installs it automatically, or run `sudo ./scripts/build-kernel.sh ./data/images`. |
| Readiness fails with `kernel`/`rootfs` | Check `COMPUTE_HOST_DATA_DIR/images`, re-run `docker compose up compute-init`, and confirm the image files are valid ELF/ext4. |
| Readiness fails with `kvm` | Enable virtualization/nested KVM on the host and ensure `/dev/kvm` is passed through (`chmod 666 /dev/kvm` on the host if needed). |
| `host_network` readiness failure | `computed` must keep `network_mode: host` and set `COMPUTE_HOST_NETWORK=1`; bridge-mode networking hides the bridge/TAPs from the host. `docker run` users need the same flag. |
| `bridge` readiness failure | The daemon creates `mft3br0`; check `docker compose logs computed` for NET_ADMIN or permission errors, and run `scripts/host-prepare.sh`. |
| Port 18080/80/53 already in use | Change `COMPUTE_API_PORT` in `.env`, or stop the conflicting service (systemd-resolved for 53). |
| Login fails right after install | Default loopback credentials are `admin` / `admin`; override `COMPUTE_ADMIN_PASSWORD` in `.env` and restart `computed` when needed. |
