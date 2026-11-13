# 16 — Docker Compose + `.env` installer plan

Status: draft for review
Scope: make a fresh Linux host installable with `docker compose` plus a single
`.env` file, with no mandatory JSON editing and no mandatory host shell scripts
beyond Docker/KVM prerequisites.

## 1. Direct answer

**No. Today a user cannot install this stack with `docker compose` + `.env`.**

This is not a documentation or polish gap. The current `docker-compose.yml`
cannot start on a generic host, and even on the lab the daemon's VM networking
is isolated from the host network namespace.

The four hard blockers:

1. **The compose file binds host addresses that only exist on the lab.**
   `docker-compose.yml` publishes ports on `192.168.1.70` and `100.78.129.96`
   (lines 20–26). On any other host Docker fails with
   `bind: cannot assign requested address`, so `computed` never starts.
2. **The daemon runs in its own network namespace, so sandbox networking is
   isolated from the host.** There is no `network_mode: host`. The daemon
   creates `mft3br0`, TAPs, NAT rules, and DNS answers inside the container
   netns. Verified on `compute-lab`:
   - host has no `mftap-*` interfaces; the container has them;
   - `ping 172.16.0.3` from the host fails, from the container succeeds.
   Consequences: LAN clients cannot reach guest IPs, host-side port forwards
   never apply, and DNS answers that point at guest IPs are unusable from the
   host. Only the Tailscale Service gateway path happens to work because
   tailscaled dials the container IP.
3. **There is no first-run image bootstrap.** `data/images` (kernel + rootfs,
   ~2.9 GB) is gitignored, built by ad-hoc root scripts
   (`scripts/build-rootfs.sh`, `scripts/build-base-image.sh`) that are not
   invoked by compose, and no download/manifest path exists. A fresh install
   starts healthy, then fails when the first sandbox is created because
   `vmlinux` / `ubuntu-24.04.ext4` / the internal SSH key are missing.
4. **`.env` cannot configure the daemon.** `internal/config/config.go` only
   reads `COMPUTE_DATA_DIR` and three `COMPUTE_TAILSCALE_*` variables
   (lines 590–619). Everything else is a JSON file with lab-specific values
   baked into `config/daemon.json` and bind-mounted into the container. Worse,
   `ResolveConfig` returns `DefaultConfig()` without applying env overrides
   when no config file is found, so env-only mode does not work even for the
   four supported variables.

Additional correctness problems that must be fixed for "all services" to work:

- `compute-worker` never runs microVMs. `cmd/compute-worker/main.go` constructs
  `&worker.Client{Config: c}` with no supervisor, so
  `internal/worker/client.go` falls back to `LocalSandboxSupervisor`, which
  runs `exec.Command` directly on the worker host/container. The compose worker
  is privileged and KVM-capable but executes jobs unisolated.
- Worker enrollment tokens are consumed on **every** connection
  (`cmd/computed/server.go` → `ConsumeWorkerTokenAtomic`). With auth enabled,
  a worker cannot reconnect after a restart.
- `/api/v1/health` always returns `{"status":"ok"}` and does not check KVM,
  images, bridge, or DB writability.
- Auth is disabled by default (`config/daemon.json` has no `auth_enabled`), and
  the API/GUI are exposed without TLS.

## 2. Target installer contract

### Supported host

- Linux only, x86_64 or arm64.
- Docker Engine 24+ with Compose v2.20+ (the `cgroup: host` key requires a
  recent Compose).
- `/dev/kvm` present and read/write; nested virtualization enabled on cloud
  hosts.
- cgroup v2 unified hierarchy with `cpu`, `memory`, and `pids` controllers.
- Kernel modules `tun`, `bridge`, `br_netfilter`, `vhost_net` available.
- Rootful Docker. Rootless Docker and Docker Desktop are not supported.

### Target UX

```bash
git clone https://github.com/Rashid2G/on-demand-vms.git
cd on-demand-vms
cp .env.example .env
# edit COMPUTE_DOMAIN, COMPUTE_ADMIN_PASSWORD, optional Tailscale keys
docker compose up -d
curl -fsS http://127.0.0.1:18080/api/v1/health
```

Optional, profile-based:

```bash
docker compose --profile gui up -d       # separate :8080 GUI container
docker compose --profile worker up -d    # multi-host worker
docker compose --profile tailscale up -d # host tailscaled integration
docker compose --profile dev up -d --build
```

No JSON edits. The JSON config remains supported for advanced/undocumented
settings via a standard Compose override file
(`docker-compose.override.yml`) or an explicit mount, but it is not part of the
default install.

### Default topology

- **Single host by default:** one `computed` container serves the API and the
  embedded GUI on `:18080`. The separate `compute-gui` container is optional
  (`gui` profile), because the daemon already serves the embedded UI.
- **Worker is optional** (`worker` profile) and only for additional hosts.
  A single-host install uses the daemon's local job execution.
- **DNS and HTTP edge are opt-in.** They require a domain and ports 53/80, so
  they must not be enabled by default on a generic host.
- **Tailscale is opt-in.** The current default (`enabled: true`) must become
  `false` unless credentials are supplied.

## 3. Gap analysis

| Area | Current state | Required state | Severity |
|---|---|---|---|
| Compose portability | Hardcoded `192.168.1.70` / `100.78.129.96` binds | No host-specific IPs; configurable bind address/ports; `network_mode: host` | P0 |
| VM networking | Daemon netns owns bridge/TAP/NAT | Daemon must own the host netns so VMs, DNS, and port forwards are reachable | P0 |
| Base images | Gitignored, hand-built, no bootstrap | Versioned, checksummed download; idempotent init; build fallback | P0 |
| SSH internal key | Generated only by `build-rootfs.sh` | Generated by init if missing; never baked into distributed images | P0 |
| Configuration | JSON with lab values; 4 env vars | Full `COMPUTE_*` env override layer; neutral default config; `.env.example` | P0 |
| Auth | Disabled by default; no TLS | Auth on by default; required admin secret/password; optional TLS | P1 |
| Worker | Runs commands directly; no images; one-time token | Real microVM supervisor; image/config plumbing; reconnect-safe enrollment | P1 |
| Health | Always `ok` | Liveness + readiness with component checks | P1 |
| Host prep | Dangerous `setup-host.sh` (enslaves default NIC even in NAT mode) | Preflight + idempotent host-prepare, compose-runnable | P1 |
| Ports | DNS/edge default on; 53/80 conflicts | Opt-in DNS/edge; configurable listen addresses | P1 |
| Images publishing | Build from source only | Published multi-arch images with tags/digests | P1 |
| Cleanup | iptables rules and bridge persist | Owned chains + `compute host cleanup` | P2 |
| Observability | Basic logs | Log rotation; readiness; doctor command | P2 |
| Docs | Lab-centric README | Install guide, env reference, troubleshooting, upgrade/backup | P1 |

## 4. Work plan

### Phase 0 — Freeze the installer contract (0.5 day)

- [ ] Decide: single-host default, worker optional, DNS/edge/tailscale opt-in.
- [ ] Create a tracking GitHub issue with the checklist below.
- [ ] Document supported/unsupported hosts in `docs/install.md`.

Acceptance: the contract is written down and referenced by all follow-up PRs.

### Phase 1 — Make `docker compose up` work on a generic host (P0)

#### 1.1 Compose rewrite (`docker-compose.yml`)

- [ ] Remove `version: '3.8'`; add `name: compute`.
- [ ] `computed`:
  - `network_mode: host` (required for bridge/TAP/NAT/DNS/edge).
  - Remove all `ports:` mappings (ignored in host mode).
  - Keep `privileged: true`, `cgroup: host`, `/sys/fs/cgroup`, `/dev/kvm`.
  - Add `ulimits.nofile: 65536`, `stop_grace_period: 30s`, log rotation.
  - `env_file: .env` plus required-variable guards
    (`${COMPUTE_ADMIN_PASSWORD:?set it in .env}`).
  - Data volume `${COMPUTE_HOST_DATA_DIR:-./data}:/var/lib/compute`.
  - No `./config` bind mount by default; use a neutral baked config.
  - Healthcheck calls a readiness endpoint.
- [ ] `compute-gui`: move to `profiles: [gui]`; host network; daemon target
  `http://127.0.0.1:18080`.
- [ ] `compute-worker`: move to `profiles: [worker]`; host network; require
  `COMPUTE_WORKER_TOKEN` when auth is on; add `/sys/fs/cgroup` and shared image
  volume; remove dead `COMPUTE_WORKER_POOL` env (use `--pool`/`COMPUTE_POOL_ID`).
- [ ] Add `compute-init` one-shot service (image bootstrap, key generation) and
  make `computed` depend on `service_completed_successfully`.
- [ ] Add `host-prepare` profile service for sysctls/module checks, or document
  the equivalent `scripts/host-prepare.sh`.
- [ ] Add `dev` profile with `build:` for local development; default uses
  `image: ghcr.io/<org>/compute-daemon:${COMPUTE_VERSION}`.
- [ ] Remove `container_name` to avoid collisions between projects.

Acceptance: `docker compose config` succeeds with `.env.example`; no
host-specific IPs remain; `docker compose up -d` starts on a clean VM.

#### 1.2 Host networking correctness

- [ ] Switch `computed` and `compute-worker` to `network_mode: host`.
- [ ] Verify from the host: bridge, TAPs, guest ping, port forward, DNS A record.
- [ ] Add a startup check that warns if the daemon's `mft3br0` is not the host
  bridge (defensive check against accidental bridge mode).
- [ ] Document why host networking is mandatory.

Acceptance: `ip link` on the host shows `mft3br0` and `mftap-*`; a host-side
`ssh compute@<guest-ip>` works; a host port forward is reachable.

#### 1.3 Neutral default config

- [ ] Replace lab values in `config/daemon.json`:
  - `dns.base_domain`: empty (disabled unless set).
  - `dns.host_addresses`: empty.
  - `tailscale.enabled`: false.
  - `images.available`: only `ubuntu-24`; drop `app-server` unless the image is
    installed.
- [ ] Add `config/daemon.example.json` for advanced users.
- [ ] Do not bind-mount `./config` in compose by default.

Acceptance: a fresh clone has no lab-specific defaults.

### Phase 2 — Make `.env` the single configuration surface (P0/P1)

#### 2.1 Env override layer (`internal/config/env.go`)

- [ ] Add `ApplyEnvOverrides(cfg *GlobalConfig) error` with typed helpers
  (`string`, `bool`, `int`, `int64`, CSV, duration).
- [ ] Precedence: defaults → config file → env → CLI flags.
- [ ] Apply env in `ResolveConfig` even when no config file exists.
- [ ] Fail fast on malformed values with the variable name and expected type.
- [ ] Keep `COMPUTE_DATA_DIR` as a compatibility root, but let explicit path
  variables win.

Minimum variable set:

```text
COMPUTE_LISTEN, COMPUTE_BIND_ADDRESS, COMPUTE_API_PORT
COMPUTE_DB_PATH, COMPUTE_AUTH_ENABLED, COMPUTE_ADMIN_SECRET,
COMPUTE_ADMIN_PASSWORD, COMPUTE_EDGE_LISTEN, COMPUTE_CGROUP_ENFORCE,
COMPUTE_JAILER_ENABLED, COMPUTE_JAILER_BIN, COMPUTE_JAILER_DIR
COMPUTE_NETWORK_MODE, COMPUTE_BRIDGE_NAME, COMPUTE_BRIDGE_IP,
COMPUTE_SUBNET, COMPUTE_GATEWAY_IP, COMPUTE_PHYSICAL_NIC
COMPUTE_DEFAULT_VCPUS, COMPUTE_DEFAULT_MEMORY_MB,
COMPUTE_DEFAULT_DISK_MB, COMPUTE_DEFAULT_IMAGE
COMPUTE_IMAGES_DIR, COMPUTE_VMS_DIR, COMPUTE_DEFAULT_KERNEL,
COMPUTE_DEFAULT_ROOTFS, COMPUTE_SSH_KEY_PATH
COMPUTE_MAX_RUNNING_VMS, COMPUTE_MAX_TOTAL_VCPUS,
COMPUTE_MAX_TOTAL_MEMORY_MB, COMPUTE_MAX_TOTAL_DISK_MB,
COMPUTE_MIN_FREE_DISK_MB
COMPUTE_JOBS_LOCAL_EXECUTION, COMPUTE_JOBS_MAX_CONCURRENT,
COMPUTE_JOBS_BOOT_TIMEOUT_SECONDS
COMPUTE_DNS_ENABLED, COMPUTE_DNS_LISTEN, COMPUTE_DNS_BASE_DOMAIN,
COMPUTE_DNS_EXTRA_BASE_DOMAINS, COMPUTE_DNS_HOST_ADDRESSES
COMPUTE_TAILSCALE_ENABLED, COMPUTE_TAILSCALE_API_KEY,
COMPUTE_TAILSCALE_TAILNET, COMPUTE_TAILSCALE_NODE_TAG,
COMPUTE_TAILSCALE_SANDBOX_TAG, COMPUTE_TAILSCALE_HOSTNAME_PREFIX,
COMPUTE_TAILSCALE_SERVICE_GATEWAY_ENABLED,
COMPUTE_TAILSCALE_SERVICE_GATEWAY_TARGET_ADDRESS
```

- [ ] Add unit tests for precedence, type errors, CSV parsing, and empty-file
  behavior (`internal/config/config_test.go`).

Acceptance: `computed` can be fully configured with env vars and no JSON file.

#### 2.2 `.env.example`

- [ ] Add a tracked `.env.example` with comments and safe defaults:
  - `COMPUTE_VERSION=...`, `COMPUTE_HOST_DATA_DIR=./data`
  - `COMPUTE_BIND_ADDRESS=127.0.0.1` for local-only, `0.0.0.0` for remote
  - `COMPUTE_DOMAIN=` (empty disables DNS/edge)
  - `COMPUTE_AUTH_ENABLED=true`, `COMPUTE_ADMIN_PASSWORD=`
  - `COMPUTE_TAILSCALE_ENABLED=false`
  - image bootstrap mode/URL
  - optional worker token/name
- [ ] Keep `.env` untracked and mode 600; document `chmod 600 .env`.
- [ ] Add `.env.example` to docs and CI.

Acceptance: copying `.env.example` and setting two values is enough to start.

### Phase 3 — First-run bootstrap and preflight (P0)

#### 3.1 Image bootstrap

- [ ] Add `compute image pull` (or `computed init --images`):
  - read a versioned manifest (`arch`, `kernel_url`, `rootfs_url`, `sha256`);
  - download to `*.part`, verify checksum, atomically rename;
  - create `images/ssh/id_ed25519` (0600) and `.pub` if missing;
  - validate the kernel is ELF and the rootfs is ext4;
  - write `images/.installed.json` for idempotency/status.
- [ ] Add `compute-init` compose service running the pull, then exit.
- [ ] Add `COMPUTE_IMAGES_MODE=download|prebuilt|build|none` and
  `COMPUTE_IMAGE_MANIFEST_URL`.
- [ ] Publish official artifacts (kernel + rootfs + manifest) as GitHub Release
  assets; add checksums and multi-arch builds.
- [ ] Add `scripts/build-kernel.sh` (pinned source/config) because no
  reproducible kernel build exists today.
- [ ] Stop baking host SSH public keys into distributed rootfs images; rely on
  per-VM `ConfigureSSHAccess` injection.
- [ ] Add a startup readiness check for kernel, rootfs, and SSH key with a clear
  error instead of failing at first sandbox creation.

Acceptance: from an empty `./data`, `docker compose up -d` downloads/verifies
images, generates the key, and `compute sandbox create demo` works.

#### 3.2 Preflight

- [ ] Add `scripts/preflight.sh` (also runnable as a compose one-shot service):
  - Linux, arch, Docker/Compose versions;
  - `/dev/kvm` exists/rw and a KVM create-VM probe;
  - cgroup v2 controllers;
  - modules/sysctls (`tun`, `bridge`, `br_netfilter`, `vhost_net`,
    `net.ipv4.ip_forward`);
  - port availability for the configured bind address (53/80/18080/8080);
  - free disk space;
  - SELinux/AppArmor notes.
- [ ] Add `scripts/host-prepare.sh` (idempotent) for the irreducible host bits:
  load modules, set sysctls, optionally create the bridge. It must **not**
  enslave the default NIC in NAT mode (current `setup-host.sh` does).
- [ ] Fix `setup-host.sh` inconsistencies: `/var/lib/compute/ssh` vs
  `/var/lib/compute/images/ssh`; unconditional NIC enslavement; missing key
  generation.
- [ ] Add `compute doctor` / `computed --check` that prints the same checks and
  remediation.

Acceptance: a failed prerequisite produces one actionable message, not a
half-started stack.

### Phase 4 — Security and worker correctness (P1)

#### 4.1 Auth and secrets

- [ ] Default `COMPUTE_AUTH_ENABLED=true` in `.env.example`; require
  `COMPUTE_ADMIN_PASSWORD` or `COMPUTE_ADMIN_SECRET` at startup.
- [ ] Bind the API to `127.0.0.1` by default; `0.0.0.0` only when explicitly set.
- [ ] Do not log the generated admin password in normal operation; if no
  password is supplied, write a one-time credential file in the data dir
  (0600) and point to it.
- [ ] Add optional TLS (`COMPUTE_TLS_CERT_FILE`, `COMPUTE_TLS_KEY_FILE`) or
  document a reverse proxy/Tailscale requirement.
- [ ] Document `.env` handling and secret rotation.

#### 4.2 Worker

- [ ] Wire `MicroVMSupervisor` in `cmd/compute-worker` with a real
  `vm.Manager` (storage, network, firecracker, cgroup config).
- [ ] Add worker config fields for images/vms dirs, SSH key, bridge/subnet,
  and cgroup enforcement.
- [ ] Give the worker its own image volume or image-pull step.
- [ ] Fix enrollment so a worker can reconnect: two-phase token → per-worker
  credential stored in `COMPUTE_WORKER_STATE_DIR`, or allow token reuse bound
  to the same worker ID. Add reconnect tests.
- [ ] Add a worker healthcheck and backoff/reconnect behavior.
- [ ] If the worker is not ready for the default install, keep it out of the
  default profile and label it experimental in docs.

#### 4.3 Health and readiness

- [ ] Split `/api/v1/health` (liveness) and `/api/v1/ready` (readiness).
- [ ] Readiness checks: DB writable, firecracker executable, `/dev/kvm`,
  kernel/rootfs/SSH key present, bridge up, optional DNS/Tailscale status.
- [ ] Compose healthcheck uses readiness; `compute doctor` renders the checks.

#### 4.4 Cleanup and operations

- [ ] Put iptables rules in owned chains (`COMPUTE-FORWARD`, `COMPUTE-NAT`) with
  idempotent jumps; add `compute host cleanup`.
- [ ] Add log rotation and resource guidance in compose.
- [ ] Document backup/restore (data dir + DB) and upgrade/rollback
  (pin `COMPUTE_VERSION`, migrations are forward-only).
- [ ] Document uninstall: `docker compose down`, keep or delete the data
  volume, run `compute host cleanup`.

### Phase 5 — Docs, CI, and evidence (P1)

- [ ] `docs/install.md`: prerequisites, quickstart, `.env` reference, profiles,
  verification, networking, port conflicts, Tailscale/DNS, multi-host worker,
  upgrade/backup/uninstall, troubleshooting.
- [ ] Update `README.md` quickstart to the new flow.
- [ ] Add `docs/configuration.md` generated from the env reference.
- [ ] CI:
  - `docker compose config` with `.env.example`;
  - grep guard against lab IPs in compose;
  - config env precedence unit tests;
  - worker reconnect test;
  - image manifest/checksum test;
  - KVM smoke test on a self-hosted runner: up → health → create/delete
    sandbox → down.
- [ ] Add a shellcheck job for installer scripts.

## 5. Concrete file changes

| File | Change |
|---|---|
| `docker-compose.yml` | Rewrite: host network, profiles, env guards, init service, no hardcoded IPs, no default worker/GUI |
| `.env.example` | New: documented installer surface |
| `config/daemon.json` | Neutral defaults; DNS/Tailscale off; only installable images |
| `config/daemon.example.json` | New: advanced reference |
| `internal/config/config.go` | Apply env in all resolve paths |
| `internal/config/env.go` | New: typed env override layer |
| `internal/config/config_test.go` | Precedence/type/empty-file tests |
| `cmd/computed/main.go` | Startup validation; init/check subcommands; host-net warning |
| `cmd/computed/server.go` | Readiness endpoint; auth defaults; worker enrollment |
| `cmd/compute/main.go` | `image pull`, `doctor`, `host cleanup` commands |
| `cmd/compute-worker/main.go` | Wire `MicroVMSupervisor`; config plumbing |
| `internal/worker/config.go` | Images/network/state/cgroup fields |
| `internal/db/store.go` | Worker credential/reconnect semantics |
| `internal/network/manager.go` | Owned iptables chains; cleanup |
| `internal/image/` | Manifest download/verify/bootstrap |
| `scripts/preflight.sh` | New |
| `scripts/host-prepare.sh` | New |
| `scripts/setup-host.sh` | Fix NIC/key/dir bugs |
| `scripts/build-kernel.sh` | New reproducible kernel build |
| `scripts/build-rootfs.sh` | Do not bake host keys |
| `Dockerfile.daemon` | Add init/health tooling; pin base images; multi-arch |
| `.github/workflows/` | Compose validation, image release, KVM smoke |
| `docs/install.md`, `docs/configuration.md` | New |

## 6. Suggested PR sequence

1. `fix/compose-portability` — compose rewrite, host network, neutral config.
2. `feat/config-env-overrides` — `COMPUTE_*` layer + `.env.example` + tests.
3. `feat/image-bootstrap` — manifest download, key generation, init service.
4. `feat/installer-preflight` — preflight/host-prepare/doctor + docs.
5. `fix/worker-supervisor-enrollment` — real microVM supervisor + reconnect.
6. `feat/health-readiness` — readiness checks + compose healthcheck.
7. `ci/compose-smoke` — config guard + KVM smoke test.

Each PR targets `dev` and references the tracking issue. Keep the lab working
by setting the new variables in the lab's untracked `.env` (bind address,
domain, host addresses) before the compose rewrite lands.

## 7. Acceptance tests

Minimum installer acceptance:

```bash
# fresh Ubuntu 24.04 VM with Docker + /dev/kvm
git clone <repo> && cd on-demand-vms
cp .env.example .env
# set COMPUTE_DOMAIN and COMPUTE_ADMIN_PASSWORD
docker compose up -d
curl -fsS http://127.0.0.1:18080/api/v1/health
docker compose exec computed compute doctor
docker compose exec computed compute image list
docker compose exec computed compute sandbox create demo
docker compose exec computed compute sandbox ssh demo -- uname -a
docker compose exec computed compute sandbox delete demo
```

Pass criteria:

- no JSON edits, no host-specific IPs, no manual image build;
- host sees `mft3br0` and `mftap-*`; host can reach the guest;
- readiness reports all components green;
- restarting the stack preserves the DB and re-adopts running VMs;
- `docker compose down` + `compute host cleanup` leaves no bridge/iptables
  residue;
- optional profiles (`gui`, `worker`, `tailscale`) do not affect the default
  install.

## 8. Decisions needed

1. **Worker scope:** implement the real microVM supervisor now, or mark the
   worker experimental and keep it out of the default install? Recommendation:
   keep it out of the default install and implement it in a follow-up, because
   the daemon already runs local VMs.
2. **Image distribution:** GitHub Releases vs OCI registry vs both.
   Recommendation: GitHub Releases with a signed checksum manifest; add an OCI
   mirror later.
3. **TLS:** terminate in the daemon or require a reverse proxy/Tailscale?
   Recommendation: bind loopback by default, document Tailscale/reverse proxy,
   add native TLS in a follow-up.
4. **GUI container:** keep the separate `:8080` container or rely on the
   daemon's embedded GUI? Recommendation: optional `gui` profile, daemon is the
   default.
5. **Host prep:** compose-run `host-prepare` service vs documented script.
   Recommendation: provide both; the compose profile is the primary path.

## 9. Out of scope

- Rootless Docker and Docker Desktop.
- cgroup v1 hosts.
- Non-Linux hosts.
- HA/replicated control plane.
- Automatic TLS certificate issuance.
- GPU passthrough.
- Public multi-tenant hardening beyond the auth defaults above.
