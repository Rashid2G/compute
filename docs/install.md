# Install Compute on a fresh Linux host

This is the supported installer path: a Linux host with Docker and `/dev/kvm`
runs the control plane (`computed`) and its embedded web UI from Docker
Compose, configured entirely through `.env`.

The default install runs **only the daemon** on the host. Sandboxes are
Firecracker microVMs created and supervised by that daemon. Multi-host workers,
a separate GUI container, and Tailscale integration are opt-in profiles.

## Requirements

| Requirement | Details |
| --- | --- |
| OS | Linux, x86_64 or arm64 |
| Kernel | cgroup v2 unified hierarchy; KVM enabled (`/dev/kvm` read/write) |
| Docker | Docker Engine 24+ (rootful) with the Compose v2 plugin (2.20+) |
| Disk | 10 GB free minimum; base images plus VM disks need considerably more |
| Network | Host networking; the daemon creates the `mft3br0` bridge, TAP devices and NAT rules |
| Privileges | The daemon container is `privileged` with `cgroup: host` and `/dev/kvm` |

Nested virtualization (a VM host such as Proxmox, VMware, or a cloud instance)
must have nested KVM enabled. Check with:

```bash
ls -l /dev/kvm
grep -E 'vmx|svm' /proc/cpuinfo
```

## 1. Preflight

From the repository checkout:

```bash
./scripts/preflight.sh
```

The script is read-only and checks the OS/architecture, Docker and Compose
versions, rootful Docker, `/dev/kvm`, cgroup v2, kernel modules and sysctls,
port availability, and disk space. It prints a fix for every failure.

Apply the host settings it recommends (idempotent, does not touch your default
network interface):

```bash
sudo ./scripts/host-prepare.sh
```

## 2. Configure

```bash
cp .env.example .env
chmod 600 .env
```

Set at least the admin credential:

```dotenv
COMPUTE_ADMIN_PASSWORD=replace-with-a-long-random-password
# or a bootstrap secret instead of a password:
# COMPUTE_ADMIN_SECRET=...
```

Generate a password with `openssl rand -base64 32`. `.env` is git-ignored;
never commit it.

The API binds `127.0.0.1:18080` by default. Set
`COMPUTE_BIND_ADDRESS=0.0.0.0` (or a specific host address) to expose it, and
keep authentication enabled. See [configuration.md](configuration.md) for every
variable.

### Base images

`compute-init` bootstraps the kernel, root filesystem, and internal SSH key
before the daemon starts. Select a mode with `COMPUTE_IMAGES_MODE`:

| Mode | Behavior |
| --- | --- |
| `download` | Fetches the checksummed manifest at `COMPUTE_IMAGE_MANIFEST_URL`, verifies SHA-256, and installs the artifacts. |
| `prebuilt` | Validates kernel/rootfs files already present under `COMPUTE_HOST_DATA_DIR/images`. |
| `build` | Same as `prebuilt`, with messages pointing at the build scripts. This is the current default until release artifacts are published. |
| `none` | Skips validation; readiness reports the missing files. |

To build images locally when no release manifest exists:

```bash
sudo ./scripts/build-rootfs.sh ./data/images
```

Then keep `COMPUTE_IMAGES_MODE=build` (or `prebuilt`) and start Compose. The
bootstrap is idempotent and safe to run on every `docker compose up`.

> **Current limitation:** no signed image manifest is published from this
> repository yet. A completely fresh host cannot bootstrap images from the
> network; `COMPUTE_IMAGES_MODE=download` needs a manifest URL you publish
> yourself (for example a GitHub Release with checksums). The default
> `.env.example` therefore uses `build` and expects images under
> `COMPUTE_HOST_DATA_DIR/images`.

## 3. Start

```bash
docker compose up -d
```

`compute-init` runs once (`service_completed_successfully`), then `computed`
starts. Watch the logs:

```bash
docker compose logs -f compute-init computed
```

## 4. Verify

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

## 5. First sandbox

Point the CLI at the daemon and log in:

```bash
export COMPUTE_DAEMON=127.0.0.1:18080
./bin/compute auth login --username admin
./bin/compute sandbox create --name demo --image ubuntu-24
./bin/compute sandbox ssh demo -- uname -a
./bin/compute sandbox delete demo
```

The host should show `mft3br0` and, while a sandbox runs, `mftap-*` devices.
Sandbox guests receive addresses from the configured NAT subnet.

## Optional profiles

Enable a profile with `--profile <name>`; profiles are additive.

| Profile | Service | Purpose |
| --- | --- | --- |
| `gui` | `compute-gui` | Separate GUI container on `COMPUTE_GUI_PORT` (default 8080). The daemon already serves the embedded UI. |
| `worker` | `compute-worker` | Experimental multi-host worker. Requires `COMPUTE_WORKER_TOKEN` and a shared image directory. |
| `tailscale` | `compute-tailscale` | Host tailscaled for tailnet Service exposure. Most hosts should use their existing tailscaled instead. |
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

The worker must share the base images (`COMPUTE_HOST_DATA_DIR/images`) and have
its own `/dev/kvm`, `cgroup: host`, and privileged access. It refuses to start
without a token. `COMPUTE_WORKER_ALLOW_LOCAL_EXEC=true` is a development-only
escape hatch that runs commands on the host shell; never enable it in
production.

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

```bash
git pull
# bump COMPUTE_VERSION in .env to the new release tag
docker compose pull
docker compose up -d
```

The data volume (`COMPUTE_HOST_DATA_DIR`, default `./data`) and database are
preserved. Running microVMs are re-adopted when the daemon restarts because the
daemon process is replaced without killing Firecracker and reconciles by PID.

If you run the legacy systemd units instead of Compose, add
`COMPUTE_ADMIN_PASSWORD` (or `COMPUTE_ADMIN_SECRET`) to the daemon's
`EnvironmentFile` before upgrading: authentication is now enabled by default
and `computed` refuses to start without a credential.

## Backup

Back up the data directory while the daemon is stopped or quiesced:

```bash
docker compose stop computed
tar -C ./data -czf compute-backup-$(date +%F).tar.gz .
docker compose start computed
```

The archive contains the SQLite database, base images, VM disks, and the
internal SSH key. Store it encrypted; the SSH key grants access to sandboxes.

## Uninstall

```bash
docker compose --profile gui --profile worker --profile tailscale down
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
| `compute-init` fails with `COMPUTE_IMAGE_MANIFEST_URL is not set` | No release manifest is published yet. Build images with `scripts/build-rootfs.sh` and keep `COMPUTE_IMAGES_MODE=build`, or set a manifest URL. |
| Readiness fails with `kernel`/`rootfs` | Check `COMPUTE_HOST_DATA_DIR/images`, re-run `docker compose up compute-init`, and confirm the image files are valid ELF/ext4. |
| Readiness fails with `kvm` | Enable virtualization/nested KVM on the host and ensure `/dev/kvm` is passed through (`chmod 666 /dev/kvm` on the host if needed). |
| `host_network` readiness failure | `computed` must keep `network_mode: host`; bridge-mode networking hides the bridge/TAPs from the host. |
| `bridge` readiness failure | The daemon creates `mft3br0`; check `docker compose logs computed` for NET_ADMIN or permission errors, and run `scripts/host-prepare.sh`. |
| Port 18080/80/53 already in use | Change `COMPUTE_API_PORT`, or stop the conflicting service (systemd-resolved for 53). |
| `docker compose` complains about `COMPUTE_VERSION` | Set `COMPUTE_VERSION` in `.env`; it is intentionally required to pin upgrades. |
| Login fails right after install | Set `COMPUTE_ADMIN_PASSWORD` in `.env` and restart `computed`; authentication never falls back to a predictable password. |
