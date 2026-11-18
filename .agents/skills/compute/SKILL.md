---
name: compute
description: >
  Operate the Compute platform end to end: Firecracker microVM sandboxes, command/file/process
  control, snapshots, templates, custom images, persistent volumes, ingress and public previews,
  distributed jobs, GitHub CI runners, workers and pools, scheduling policy, tenants/RBAC/API keys,
  audit events, webhooks, and telemetry. Use this skill whenever a task mentions compute, computed,
  compute-worker, sandbox, microVM, Firecracker, ephemeral environment, isolated build/test,
  distributed job, CI runner, preview URL, or dogfooding Compute.
---

# Compute Platform Agent Skill

Compute (`on-demand-vms`) is a self-hosted Firecracker microVM platform. A control-plane
daemon (`computed`) exposes a CLI (`compute`), a REST/WebSocket API, an embedded web UI, and
TypeScript/Python SDKs. Agents can provision isolated Linux machines in seconds, run commands
and services inside them, snapshot and clone them, attach storage, publish HTTP endpoints, and
fan work out to remote worker nodes.

This skill covers every capability an agent can drive. Use it as the operating manual, then
open the reference files for exact flags, payloads, and SDK signatures.

---

## 0. Capability map and primitive selection

| Capability | CLI | HTTP API | SDK | Web UI |
| :--- | :--- | :--- | :--- | :--- |
| Sandbox lifecycle (create/start/stop/delete; restart is REST-only) | `compute sandbox ...` | `/api/v1/sandboxes` | `Sandbox.create/connect/kill` | Sandboxes |
| Bulk create/clone/action/delete | — | `/api/v1/sandboxes/bulk-*` | — | Sandboxes |
| Command execution, SSH | `compute sandbox exec/ssh` | `/exec`, `/exec-stream`, `/ssh` | `sandbox.commands.run` | Inspect / Terminal |
| Files, directories, processes | `compute file/cp/scp/process` | `/files`, `/processes` | `sandbox.files`, `sandbox.process` | Inspect |
| Live memory snapshot / resume | `save-state`, `resume` | `/save-state`, `/resume` | raw request | Snapshots |
| Disk templates (snapshot → new VM) | `compute template ...` | `/api/v1/templates` | raw request | Snapshots |
| Custom rootfs images | `compute image ...` | `/api/v1/images` | raw request | Snapshots |
| Persistent block volumes | — | `/api/v1/volumes` | raw request | Volumes |
| Ingress / public previews | `compute expose/ports/url` | `/expose`, `/network/port-forwards` | `sandbox.exposePort` | Access card |
| Distributed jobs | `compute job ...` | `/api/v1/jobs` | raw request | CI Jobs |
| GitHub-connected CI | — | `/api/v1/ci/repos`, `/ci/webhooks/github` | — | CI Jobs |
| Workers, pools, scheduling | `compute worker/pool` | `/api/v1/workers`, `/pools`, `/scheduler/policy` | — | Workers |
| Sandbox migration | — | `/sandboxes/:id/migrate`, `/pools/:id/migrate` | — | Workers |
| Auth, users, orgs, quotas | `compute auth ...` | `/auth/*`, `/users`, `/orgs`, `/limits` | — | Access / Settings |
| Machine API keys | `compute auth keys ...` | `/api/v1/auth/keys` | — | Access |
| Scoped access keys | — | `/api/v1/api-keys` | — | Access |
| SSH public keys | `compute keys ...` | `/api/v1/keys` | — | Access |
| Events, webhooks, OTel | — | `/events`, `/webhooks`, `/otel` | — | Settings |
| Queue, operations, tasks, stats | `compute doctor` (readiness) | `/queue`, `/operations`, `/tasks`, `/stats` | — | Dashboard |
| Config, tiers, DNS status | `compute version` | `/config`, `/tiers`, `/dns/status` | — | Settings |
| Embedded dashboard | `compute gui` | served on daemon port | — | — |

### Choose the right primitive

| Need | Use | Why |
| :--- | :--- | :--- |
| One-off build, test, lint, packaging | **Job** (`compute job run`) | Uploads a tar.gz, runs one command, streams logs, tears down automatically. |
| Multi-step work, debugging, background services, SSH | **Sandbox** | Persistent disk and process tree across many commands. |
| Live web app, API, WebSocket/SSE service | **Sandbox + expose** | Dedicated origin with auth, redirects, cookies, and WebSockets preserved. |
| Reusable starting state | **Template** or **Image** | Template clones a VM disk; image bakes packages into a rootfs. |
| Durable data beyond a sandbox | **Volume** | Attach/detach/expand/snapshot an ext4 or xfs block device. |
| Parallel capacity beyond one host | **Worker + pool + job** | Workers connect outbound; jobs are scheduled by pool/arch/resources. |
| Develop Compute itself | **Nested sandbox** | Full isolation from the host network, ports, and filesystem. |

### Operating principles for agents

1. **Verify readiness first.** Run `compute doctor` (or `GET /api/v1/ready`). A running daemon is
   not the same as a host that can boot microVMs.
2. **Prefer jobs for one-shot work.** They clean up after themselves and return an exit code.
3. **Prefer machine-readable output.** Use `--json` where supported; otherwise call the REST API.
4. **Always clean up.** Delete sandboxes, jobs, exposures, volumes, and templates you created
   unless the user asked to keep them. Use `compute sandbox delete '<prefix>-*'` for batches.
5. **Protect anything long-lived.** `compute sandbox protect <name>` before handing a sandbox to
   another workflow.
6. **Never print secrets.** API keys, worker tokens, SSH passwords, webhook secrets, and GitHub
   tokens are shown once; keep them in environment variables or files, not logs.
7. **Respect tenancy.** Non-admin callers only see their own user/org resources. Do not attempt
   cross-tenant lookups by guessing names.
8. **Watch quotas and the queue.** A `201` create can still be queued; poll the sandbox or
   `/api/v1/queue` and report the queue reason instead of assuming failure.
9. **Treat the API as the contract.** CLI output is for humans; REST JSON is stable for scripts.
10. **Do not run untrusted code on the host.** Use a sandbox or job for anything risky.

---

## 0.5 Build, install, and run the platform

An agent that is asked to bring Compute up (or to develop it) needs these entry points. Skip
this section when a daemon is already reachable and healthy.

### From source

```bash
make build          # bin/compute, bin/computed, bin/compute-worker, bin/compute-bench
make vet
make test           # Go unit/integration tests
make test-web       # web typecheck + tests
```

The host must be Linux with KVM and the Firecracker binary. Bootstrap the kernel, rootfs, and
internal SSH key before the first boot:

```bash
./scripts/install.sh       # or: make setup
# Air-gapped/standalone alternative:
./bin/computed init --images --images-mode auto
```

### Docker Compose

```bash
cp .env.example .env       # optional; customize COMPUTE_* settings
make up                    # docker compose up -d; installs images if missing
make validate-compose
curl -fsS http://127.0.0.1:18080/api/v1/health
curl -fsS http://127.0.0.1:18080/api/v1/ready
```

The daemon container must use the host network namespace (`network_mode: host`,
`COMPUTE_HOST_NETWORK=1`) so sandbox bridges, TAPs, NAT rules, DNS, and ingress are visible to
host clients. The embedded UI is served on the daemon port; the optional `gui` profile publishes
a separate UI container on `:8080`.

### Standalone daemon

```bash
export COMPUTE_ADMIN_PASSWORD='change-me'   # required when auth is enabled
./bin/computed --listen 127.0.0.1:18080
```

Common `computed` flags: `--config <file>`, `--config-path`, `--listen`, `--db-path`,
`--images-dir`, `--vms-dir`, `--network-mode nat|bridge|nic|wired`, `--bridge`, `--bridge-ip`,
`--subnet`, `--physical-nic`, `--gateway-ip`, `--firecracker-bin`, `--jailer`, `--jailer-bin`,
`--jailer-dir`, `--cgroup-enforce`, `--version`.

Daemon subcommands:

```bash
./bin/computed check [--json]                 # readiness checks, no daemon needed
./bin/computed init [--data-dir <path>]       # master key, CA, state DB, audit DB
./bin/computed init --images                  # bootstrap kernel/rootfs/SSH key
./bin/computed admin user create --username <name> --email <email> --password <pw> [--role admin|user] [--data-dir <path>]
./bin/computed admin apikey create --username <name> --name <key-name> [--data-dir <path>]
./bin/computed admin audit verify [--data-dir <path>]
```

Configuration precedence: built-in defaults → JSON config file → `COMPUTE_*` environment →
command-line flags. Frequently used environment variables:

| Variable | Default | Purpose |
| :--- | :--- | :--- |
| `COMPUTE_LISTEN` | `127.0.0.1:18080` | Listen address (wins over bind/port). |
| `COMPUTE_DATA_DIR` | `/var/lib/compute` | Root for DB, images, VMs, keys. |
| `COMPUTE_DB_PATH` | `<data>/data/compute.db` | SQLite database. |
| `COMPUTE_IMAGES_DIR` | `<data>/images` | Kernel/rootfs images. |
| `COMPUTE_VMS_DIR` | `<data>/vms` | Sandbox disks. |
| `COMPUTE_DEFAULT_IMAGE` | `ubuntu-24` | Default base image. |
| `COMPUTE_DEFAULT_VCPUS` | `1` | Default vCPUs. |
| `COMPUTE_DEFAULT_MEMORY_MB` | `2048` | Default RAM. |
| `COMPUTE_DEFAULT_DISK_MB` | `51200` | Default disk. |
| `COMPUTE_NETWORK_MODE` | `nat` | `nat`, `bridge`, `nic`, or `wired`. |
| `COMPUTE_BRIDGE_NAME` / `COMPUTE_BRIDGE_IP` | `mft3br0` / `172.16.0.1/16` | Sandbox bridge. |
| `COMPUTE_SUBNET` | `172.16.0.0/16` | Sandbox subnet. |
| `COMPUTE_AUTH_ENABLED` | `true` | Require authentication. |
| `COMPUTE_SIGNUP_ENABLED` | `false` | Allow public self-registration. |
| `COMPUTE_ADMIN_USERNAME` / `COMPUTE_ADMIN_PASSWORD` | `admin` / required | Bootstrap administrator. |
| `COMPUTE_PUBLIC_URL` | — | Externally reachable base URL for serve links. |
| `COMPUTE_SERVE_DOMAIN` | `localhost` for loopback | Subdomain serve domain. |
| `COMPUTE_DNS_ENABLED` / `COMPUTE_DNS_BASE_DOMAIN` | `false` / — | Authoritative DNS. |
| `COMPUTE_MAX_RUNNING_VMS` | `0` (unlimited) | Capacity limit. |
| `COMPUTE_MAX_TOTAL_VCPUS` | `0` | Capacity limit. |
| `COMPUTE_MAX_TOTAL_MEMORY_MB` | `0` | Capacity limit. |
| `COMPUTE_MAX_TOTAL_DISK_MB` | `0` | Capacity limit. |
| `COMPUTE_CGROUP_ENFORCE` | `true` | Enforce per-VM cgroups. |
| `COMPUTE_JAILER_ENABLED` | `false` | Run Firecracker under jailer. |

Full configuration is documented in `docs/configuration.md`. After changing daemon settings,
restart `computed` and re-run `compute doctor`.

---

## 1. Connect and authenticate

### Daemon address

| Setting | Meaning |
| :--- | :--- |
| `COMPUTE_DAEMON` | Primary address: `127.0.0.1:18080`, `https://compute.example.com`, or `unix:///run/compute.sock`. |
| `COMPUTE_HOST` | Fallback address. A bare host gets `:18080` appended. |
| `~/.compute/config.json` | CLI credentials written by `compute auth login` (mode `0600`). |
| `COMPUTE_API_KEY` | API key or session token; highest precedence for scripts. |

```bash
export COMPUTE_DAEMON="127.0.0.1:18080"
export COMPUTE_API_KEY="cmp_live_..."     # only when auth is enabled

compute doctor                            # readiness with remediation
compute version                           # client + daemon build info
curl -fsS "http://${COMPUTE_DAEMON}/api/v1/health"
```

The daemon also serves the embedded web UI at `/` on the same port. `compute gui` starts a
separate lightweight UI server only when you need one.

### Authentication model

- Auth is enabled by default. The daemon refuses to start without an admin credential.
- Requests accept `Authorization: Bearer <token>` or `X-API-Key: <key>`.
- Session tokens come from `POST /api/v1/auth/login`; machine API keys come from
  `POST /api/v1/auth/keys`; scoped control-plane keys come from `POST /api/v1/api-keys`.
- Roles are `admin` and `user`; org membership adds org-scoped visibility.

```bash
# Interactive or flag-based login. Stores a machine key in ~/.compute/config.json.
compute auth login --username admin --password "$COMPUTE_ADMIN_PASSWORD"

# Self-service registration (only when COMPUTE_SIGNUP_ENABLED=true).
compute auth signup --email dev@example.com --username dev --password 'strong-password'

compute auth whoami
compute auth keys list
compute auth keys create --name ci-agent
compute auth keys revoke <key-id>
compute auth logout
```

For CI, create a key once and pass it as `COMPUTE_API_KEY`. For a browser or short-lived
automation, use the login token. Never commit either.

---

## 2. Sandboxes (microVMs)

A sandbox is a Firecracker microVM with its own kernel, disk, network namespace, and process
tree. Names and IDs are interchangeable in most commands; shell-style patterns (`*`, `?`, `[]`)
work for batch lifecycle operations.

### Lifecycle

```bash
# Create (does not boot unless --start/--boot is given)
compute sandbox create \
  --name web-1 \
  --tier 2vcpu \
  --memory 4096 \
  --disk 20480 \
  --image ubuntu-24 \
  --start

compute sandbox list
compute sandbox info web-1
compute sandbox start web-1
compute sandbox stop web-1
compute sandbox delete web-1

# Batch operations (quote wildcards)
compute sandbox stop 'dev-*'
compute sandbox delete 'temp-*'
```

Create flags: `--name`, `--tier` (`1vcpu`/`2vcpu`/`4vcpu` or aliases), `--vcpus`, `--memory`
(MiB), `--disk` (MiB; use 20480 for 20 GiB), `--image`, `--template`, `--ssh-key <name|path>`,
`--no-ssh-key`, `--ssh-password` (`-` reads stdin), `--network-isolation account|isolated`,
`--isolated`, `--protected`, `--start`/`--boot`.

Statuses: `created`, `queued`, `running`, `paused`, `stopped`, `failed`. A sandbox that cannot
boot immediately may be admitted to the capacity queue; `compute sandbox info` prints the queue
reason and a hint.

### Snapshots and resume

```bash
compute sandbox save-state web-1     # alias: snapshot; status becomes paused
compute sandbox resume web-1         # restores memory and running processes
```

`save-state` is a live microVM snapshot (memory + CPU state), not a disk-only backup. Use a
template when you want a reusable disk image.

### Protection, configuration, and cloning

```bash
compute sandbox protect web-1
compute sandbox unprotect web-1
compute sandbox network-isolation web-1 isolated
compute sandbox network-isolation web-1 account
```

The CLI exposes the common lifecycle. The REST API additionally supports:

- `POST /api/v1/sandboxes/:id/restart`
- `POST /api/v1/sandboxes/:id/clone` with a create-style body (`name`, `vcpus`, `memory_mb`, ...)
- `PATCH /api/v1/sandboxes/:id` with `public_ip`, `protected`, `network_isolation`, or `isolated`
- `POST /api/v1/sandboxes/:id/public-ip` with `{"public_ip_enabled": true}`
- `POST /api/v1/sandboxes/:id/migrate` with `target_worker_id` or `target_pool_id`
- `POST /api/v1/sandboxes/:id/reconcile-ssh`
- `POST /api/v1/sandboxes/bulk-create`, `/bulk-clone`, `/bulk-action`, `/bulk-delete`

Bulk endpoints accept up to the daemon's per-request limit and return `task_id` when async.
Resolve the final result with `GET /api/v1/tasks/:task_id`; resolve individual long-running
operations with `GET /api/v1/operations/:id`. Create/clone responses may include
`X-Compute-Operation-ID` and `X-Compute-Boot-Warning` headers.

### Quotas and capacity

`GET /api/v1/limits` returns the caller's quota and current usage. `GET /api/v1/queue` returns
queued sandboxes, running totals, and resource limits. A create that exceeds a hard limit returns
HTTP `429` with a structured resource-limit error (resource, requested, available, limit).
Admins can raise limits through the environment or admin API; ordinary callers cannot.

---

## 3. Commands, files, processes, and SSH

### Execute commands

```bash
# Stream output live (default)
compute sandbox exec web-1 -- uname -a

# Buffer and print JSON
compute sandbox exec --json --timeout 120 web-1 -- sh -c 'npm test'

# Run as root (passwordless sudo is available to the compute user)
compute sandbox exec --user root web-1 -- apt-get update -y
```

REST: `POST /api/v1/sandboxes/:id/exec` with
`{"command": "...", "user": "compute", "timeout_seconds": 60}` returns
`{stdout, stderr, exit_code, duration_ms}`. `POST .../exec-stream` returns SSE events with
`stream`, `data`, `exit_code`, and `duration_ms` fields.

### Files and directories

```bash
compute sandbox file read web-1 /etc/os-release
compute sandbox file write web-1 /workspace/app.py --file ./app.py
echo 'hello' | compute sandbox file write web-1 /workspace/hello.txt -
compute sandbox file list web-1 /workspace
compute sandbox file mkdir web-1 /workspace/data
compute sandbox file remove web-1 /workspace/old

compute sandbox cp ./app.py web-1:/workspace/app.py
compute sandbox cp web-1:/workspace/result.json ./result.json
compute scp -r ./src web-1:/workspace/src
compute scp -r web-1:/workspace/logs ./
```

`file write` creates parent directories. `cp`/`scp` run as the sandbox's default `compute` user;
`scp` requires `-r` for directory sources and accepts multiple sources.

REST: `POST .../files/write` `{path, content, format}` (`text` or `base64`),
`GET .../files/read?path=...`, `GET .../files/list?path=...`,
`POST .../files/mkdir`, `DELETE .../files?path=...` (or `POST .../files/remove`).

### Background processes

```bash
compute sandbox process list web-1
compute sandbox process start web-1 --cwd /workspace --env NODE_ENV=production -- npm start
compute sandbox process kill web-1 1234 --signal SIGTERM
```

REST: `GET .../processes`, `POST .../processes` `{command, cwd, env}`, and
`POST .../processes/kill` `{pid, signal}`. Process listing includes PID, PPID, user, CPU%,
memory%, status, command, and args.

### SSH

```bash
compute sandbox ssh web-1
compute sandbox ssh compute@web-1 -- 'systemctl status nginx'
compute sandbox ssh -p "$SSH_PASSWORD" web-1
compute sandbox ssh --proxy web-1        # raw stdio proxy for tools
```

SSH is tunneled over WebSocket through the daemon; no inbound port is required on the guest.
`-t`/`-T` force or disable PTY allocation. One-off commands avoid a PTY automatically.

---

## 4. Networking, ingress, and public previews

Every exposure gets its own origin so applications that use absolute paths, redirects, cookies,
or WebSockets work unchanged.

### Expose a guest port

```bash
compute expose web-1 3000                 # public by default
compute expose --private web-1 3000       # requires a tenant session/serve token
compute expose --subdomain my-app web-1 3000
compute expose --json web-1 3000
compute ports web-1
compute unexpose web-1 3000
```

REST: `POST /api/v1/sandboxes/:id/expose` with
`{"guest_port": 3000, "public": true, "subdomain": "optional"}`. Re-exposing the same guest port
updates the existing exposure in place and keeps its origin stable. List with
`GET /api/v1/sandboxes/:id/ports`, remove with `DELETE /api/v1/sandboxes/:id/ports/:port`.
Private exposures can mint a short-lived URL with
`POST /api/v1/sandboxes/:id/serve-token` `{"guest_port": 3000}`.

### Address forms

| Form | Pattern | Notes |
| :--- | :--- | :--- |
| Path-based (universal) | `http://<daemon>/preview/<sandbox>/<port>/` | Works in Docker and lab VMs with only the daemon port published. |
| API-prefixed path | `http://<daemon>/api/v1/sandboxes/<sandbox>/preview/<port>/` | Same routing, useful when the plain path is reserved. |
| Legacy subdomain | `http://<port>-<sandbox>.<zone>/` or `http://<sandbox>-<port>.<zone>/` | Requires wildcard DNS for the base domain. |
| Tenant subdomain | `http://<sandbox>.<username>.<zone>/` | Guest port 80; requires DNS and auth. |
| Dedicated serve origin | `https://<slug>.<serve-domain>/` or `https://<host>:<serve-port>/` | Assigned by `expose`; best for apps with absolute URLs. |

`compute url web-1` prints the best URL (or the SSH target when no URL resolves). `compute url
--json --port 3000 web-1` prints the full access model.

### Port forwards and public IPs

`POST /api/v1/network/port-forwards` registers a host-port DNAT rule:

```json
{
  "vm_id": "web-1",
  "guest_port": 3000,
  "host_port": 33000,
  "protocol": "tcp",
  "public": true,
  "subdomain": "optional"
}
```

`host_port` is optional for serve-origin exposure but required for a direct host-port mapping.
In containers, a host port is only reachable if it was published at container start; prefer the
`/preview/...` path or a serve origin. Update visibility with
`PATCH /api/v1/network/port-forwards/:id` `{"public": false}` and remove with `DELETE`.

`POST /api/v1/sandboxes/:id/public-ip` with `{"public_ip_enabled": true}` requests a dedicated
public IP when the host network mode supports it.

### Network isolation and diagnostics

- `account` (default): sandboxes can reach other sandboxes in the same account.
- `isolated`: the sandbox can reach only itself and egress via NAT.

```bash
compute sandbox network-isolation web-1 isolated
compute network status
compute network list
compute network inspect web-1
compute network prune
```

REST adds `GET /api/v1/network/config`, `GET /api/v1/network/allocations`, and
`POST /api/v1/network/prune`. `prune` removes orphaned TAP devices, stale leases, and dangling
allocations after a crash; it is admin-only.

### DNS

When `COMPUTE_DNS_ENABLED=true` and a base domain is configured, sandbox hostnames are
`<sandbox>.<username>.<zone>`; `api`, `gui`, `www`, and the zone apex route to the daemon.
Check runtime state with `GET /api/v1/dns/status`.

---

## 5. Templates, images, and volumes

### Templates (disk snapshots)

```bash
compute template list
compute template create --vm web-1 --name web-base --desc 'Node 20 + deps'
compute template inspect web-base
compute template launch web-base --name web-2 --tier 2vcpu
compute template delete web-base
```

REST: `POST /api/v1/templates` `{vm_id, name, description}`,
`POST /api/v1/templates/:id/launch` with create-style overrides, `GET`, `DELETE`.
Launching a template provisions and starts a new sandbox from the saved disk.

### Custom images

```bash
compute image list
compute image build --name node20 --base ubuntu-24 --packages nodejs,npm,git --size 4096 --desc 'Node 20'
compute image inspect node20
compute image delete node20
```

REST: `POST /api/v1/images` with
`{name, description, base_image, packages, dockerfile, script, size_mb}`. Builds run as a
long-running operation; poll `GET /api/v1/operations/:id` or `GET /api/v1/images/:name`.
Use images when every sandbox should start with the same packages; use templates when you want
an exact disk state.

### Volumes

Volumes are persistent block devices independent of a sandbox lifecycle. They are managed over
REST (and the Volumes page), not the CLI.

```bash
curl -sS -X POST "http://${COMPUTE_DAEMON}/api/v1/volumes" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{"name":"data","size_gb":20,"filesystem":"ext4"}'

curl -sS -X POST ".../api/v1/volumes/<id>/attach" \
  -H 'Content-Type: application/json' -d '{"vm_id":"web-1"}'
curl -sS -X POST ".../api/v1/volumes/<id>/detach"
curl -sS -X POST ".../api/v1/volumes/<id>/expand" \
  -H 'Content-Type: application/json' -d '{"add_gb":10}'
curl -sS -X POST ".../api/v1/volumes/<id>/snapshot"
curl -sS -X DELETE ".../api/v1/volumes/<id>"
```

Attach requires the sandbox to be stopped or paused; the guest mounts the device at
`/mnt/data` by default after boot. Supported filesystems are `ext4` and `xfs`. A sandbox's
rootfs also appears as a synthetic `vol_root_*` volume.

---

## 6. Distributed jobs and CI

### Ephemeral jobs

```bash
compute job run --files . --cwd /workspace --timeout 900 -- make test
compute job run --follow=false --files . -- npm test
compute job list
compute job info <job-id>
compute job logs <job-id>
compute job cancel <job-id>
```

Run flags: `--image`, `--pool` (default `default`), `--arch` (`amd64`/`arm64`), `--vcpus`,
`--mem` (MiB), `--files` (tar.gz + base64 upload), `--cwd` (default `/workspace`), `--timeout`
(seconds, 0 = unlimited), `--follow` (default true). Exit code is 0 on success, 1 on failure,
130 on cancel.

Jobs run on a connected worker, or in a local microVM on the daemon host when no worker is
available (`jobs.local_execution`). The job sandbox is ephemeral and removed after the run.

REST: `POST /api/v1/jobs` with `{type, pool_id, priority, source, spec}` where `spec` is a
`JobSpec` (`image_id`, `architecture`, `vcpus`, `memory_mb`, `disk_mb`, `command` array, `cwd`,
`files` base64 tar.gz, `env`, `timeout_seconds`, `network_policy`, repo fields).
`GET /api/v1/jobs`, `GET /api/v1/jobs/:id`, `GET /api/v1/jobs/:id/logs`,
`POST /api/v1/jobs/:id/cancel`. Job states include `queued`, `assigned`, `running`, `succeeded`,
`failed`, `canceled`, and `lost`.

### GitHub-connected CI

```bash
# Connect a repository (REST; the UI has the same form)
curl -sS -X POST "http://${COMPUTE_DAEMON}/api/v1/ci/repos" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "my-repo",
    "repo_url": "https://github.com/org/repo",
    "default_branch": "main",
    "github_token": "ghp_...",
    "default_command": "make test",
    "vcpus": 2,
    "memory_mb": 2048,
    "timeout_sec": 900
  }'

curl -sS -X POST ".../api/v1/ci/repos/<id>/trigger" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{"branch":"main","command":"make test"}'
```

`GET /api/v1/ci/repos`, `PATCH /api/v1/ci/repos/:id`, `DELETE /api/v1/ci/repos/:id`.
GitHub push events are delivered to `POST /api/v1/ci/webhooks/github`; configure the webhook
secret shown when the repo was connected and subscribe to push events. Triggered runs create
normal jobs, so use `compute job info/logs` or the CI Jobs page to follow them.

---

## 7. Workers, pools, scheduling, and migration

Workers are remote KVM hosts that connect **outbound** to the control plane over WebSocket; no
inbound ports are needed on the worker.

```bash
# Control plane: mint a one-time enrollment token (30 min, binds on first use or to --name)
compute worker token create --pool default --name gpu-1

# Worker host:
compute-worker run \
  --control-plane https://compute.example.com \
  --token "$COMPUTE_WORKER_TOKEN" \
  --pool default \
  --name gpu-1

compute worker list
compute worker info <worker-id>
compute worker cordon <worker-id>     # stop new placements
compute worker drain <worker-id>      # evict running jobs and cordon
compute worker delete <worker-id>
```

Pools group workers for job placement:

```bash
compute pool list
compute pool create gpu
compute pool delete <pool-id>
```

REST adds `POST /api/v1/workers/:id/uncordon`, `POST /api/v1/workers/:id/reconcile`,
`POST /api/v1/workers/reconcile`, `PATCH /api/v1/workers/:id` (pool, allocated vCPU/RAM/disk,
labels, status), and `POST /api/v1/pools/:id/migrate` to move workers and/or sandboxes to
another pool. Sandboxes can be migrated individually with
`POST /api/v1/sandboxes/:id/migrate`.

Scheduling is cluster-wide:

```bash
curl -sS "http://${COMPUTE_DAEMON}/api/v1/scheduler/policy" -H "Authorization: Bearer $COMPUTE_API_KEY"
curl -sS -X PUT "http://${COMPUTE_DAEMON}/api/v1/scheduler/policy" \
  -H "Authorization: Bearer $COMPUTE_API_KEY" -H 'Content-Type: application/json' \
  -d '{"strategy":"least_loaded","cpu_soft_overcommit_ratio":1.5,"cpu_hard_overcommit_ratio":2.0}'
```

Strategies: `least_loaded`, `round_robin`, `bin_pack`, `spread`. The policy also controls CPU,
memory, and disk weights, hard/soft overcommit, host reserves, idle-RAM snapshotting, and
auto-reclaim. Invalid values return `400`; overcommit/disk violations return `429`.

Worker environment: `COMPUTE_CONTROL_PLANE`, `COMPUTE_WORKER_TOKEN`, `COMPUTE_WORKER_NAME`,
`COMPUTE_POOL_ID`, `COMPUTE_WORKER_STATE_DIR`, `COMPUTE_WORKER_MAX_VCPUS`,
`COMPUTE_WORKER_MAX_MEMORY_MB`, `COMPUTE_WORKER_MAX_DISK_MB`, `COMPUTE_WORKER_BOOT_TIMEOUT_SECONDS`.

---

## 8. Platform administration and observability

Most admin endpoints require the `admin` role. Non-admins see only their own resources.

### Users, orgs, and quotas

```bash
curl -sS "http://${COMPUTE_DAEMON}/api/v1/users" -H "Authorization: Bearer $ADMIN_TOKEN"
curl -sS -X POST "http://${COMPUTE_DAEMON}/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"email":"dev@example.com","username":"dev","password":"...","role":"user"}'
curl -sS "http://${COMPUTE_DAEMON}/api/v1/limits" -H "Authorization: Bearer $TOKEN"
```

`GET|POST /api/v1/orgs`, `GET /api/v1/orgs/:id`; `GET /api/v1/users`,
`GET|DELETE /api/v1/users/:idOrUsername`. The bootstrap administrator and your own account
cannot be deleted.

### SSH keys

```bash
compute keys list
compute keys add --name laptop --key-file ~/.ssh/id_ed25519.pub --all
compute keys update laptop --name workstation --all
compute keys apply laptop
compute keys show laptop
compute keys delete laptop
```

REST: `GET|POST /api/v1/keys`, `GET|PATCH|DELETE /api/v1/keys/:idOrName`,
`POST /api/v1/keys/:idOrName/apply`, `POST /api/v1/keys/reconcile`. The first saved key becomes
the account default; `--all` embeds it in every current and future sandbox. Keys are pushed to
running sandboxes over SSH and injected offline into stopped ones.

### API keys

Two kinds exist:

- **Machine keys** for a user: `POST /api/v1/auth/keys` (what `compute auth login` creates).
- **Scoped access keys** for the control plane: `POST /api/v1/api-keys` with
  `{name, role, scope, expires_at}`. The secret is returned once as `cmp_live_...`.

List keys with `GET /api/v1/auth/keys` or `GET /api/v1/api-keys`; revoke with `DELETE`.

### Events, webhooks, and OpenTelemetry

```bash
# Recent audit events (non-admins see only their own actor)
curl -sS "http://${COMPUTE_DAEMON}/api/v1/events" -H "Authorization: Bearer $TOKEN"

# Live event stream (SSE)
curl -N "http://${COMPUTE_DAEMON}/api/v1/events/stream" -H "Authorization: Bearer $TOKEN"
```

Webhooks (admin) subscribe to event names or prefixes such as `sandbox.*`:

```bash
curl -sS -X POST "http://${COMPUTE_DAEMON}/api/v1/webhooks" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com/hook","events":["sandbox.*","ci.job.completed"]}'
```

Deliveries are HMAC-SHA256 signed in `X-Compute-Signature` with the webhook secret; the event
name is in `X-Compute-Event`. `POST /api/v1/webhooks/:id/test` sends a test delivery.

OTel config lives at `GET|PUT /api/v1/otel` (`enabled`, `endpoint`, `protocol`, `auth_header`,
`service_name`, `sample_rate`, `export_interval`, `metrics_enabled`, `traces_enabled`,
`logs_enabled`); `POST /api/v1/otel/test` sends a test span.

### Environments and host resources

`GET|POST /api/v1/environments` manages named environment profiles (default profile, safe for
third parties, GitHub repos, secrets, credential profile). `GET /api/v1/admin/resources`
returns host CPU, memory, disk, disk I/O, network, system, and allocation telemetry plus capacity
warnings. `GET /api/v1/config` returns the redacted daemon configuration; `GET /api/v1/stats`
returns sandbox counts and resource totals; `GET /api/v1/tiers` returns supported sizes.

### Operations, tasks, and the queue

- `GET /api/v1/operations?status=&kind=&task_id=&limit=` lists durable operations.
- `GET /api/v1/operations/:id` returns one operation with progress and result.
- `GET /api/v1/tasks` and `GET /api/v1/tasks/:id` resolve asynchronous bulk requests.
- `GET /api/v1/queue` shows queued sandboxes and capacity state.

---

## 9. SDKs and embedded UI

### TypeScript / JavaScript

```ts
import { Sandbox } from '@compute/sdk';

const sandbox = await Sandbox.create({ template: 'ubuntu-24', tier: '2vcpu', name: 'agent-box' });
const result = await sandbox.commands.run('node -v');
await sandbox.files.write('/workspace/app.js', 'console.log("hi")');
const files = await sandbox.files.list('/workspace');
const pid = await sandbox.process.start('node /workspace/app.js', { cwd: '/workspace' });
const { url } = await sandbox.exposePort(3000, { public: true });
await sandbox.kill();

// Reconnect later
const again = Sandbox.connect(sandbox.id);
```

`Sandbox.create` creates and boots; `Sandbox.connect` attaches to an existing sandbox.
`commands.run`, `files.read/write/list/mkdir/remove`, `process.list/start/kill`,
`exposePort`, `setNetworkIsolation`, and `kill` are available.

### Python

```python
from compute import Sandbox

with Sandbox.create(template="ubuntu-24", tier="2vcpu", name="agent-box") as sandbox:
    result = sandbox.commands.run("python3 --version")
    sandbox.files.write("/workspace/app.py", "print('hi')")
    pid = sandbox.process.start("python3 /workspace/app.py", cwd="/workspace")
    preview = sandbox.expose_port(8080, public=True)
    print(preview["url"])
# __exit__ kills the sandbox
```

`Sandbox.connect(id)` attaches; `commands.run` raises `CommandExecutionError` on non-zero exit.
`files`, `process`, `expose_port`, `set_network_isolation`, `kill`, and `close` mirror the TS SDK.

Both SDKs read `COMPUTE_HOST`/`COMPUTE_DAEMON` and `COMPUTE_API_KEY` when options are omitted.

### Embedded web UI

```bash
compute gui --port 8080 --addr 127.0.0.1 --daemon 127.0.0.1:18080
```

The daemon already serves the same UI on its own port (`/`). The standalone `gui` command is
useful when you want the UI on a different address. UI pages cover sandboxes, snapshots/images,
volumes, CI jobs, workers, host resources, and settings (access, environment, network, events).

---

## 10. Agent playbooks

### A. Isolated build and test

```bash
compute doctor
compute job run --files . --cwd /workspace --timeout 900 -- make build test
```

Report the job ID, final state, and failing log lines. Do not leave a sandbox behind.

### B. Interactive debugging session

```bash
BOX="debug-$(date +%s)"
compute sandbox create --name "$BOX" --tier 2vcpu --memory 4096 --start
compute sandbox exec "$BOX" -- bash -lc 'cd /workspace && ./repro.sh'
compute sandbox file read "$BOX" /var/log/app.log
compute sandbox save-state "$BOX"          # optional checkpoint
compute sandbox delete "$BOX"
```

### C. Deploy a live web service

```bash
BOX="web-$(date +%s)"
compute sandbox create --name "$BOX" --tier 2vcpu --memory 2048 --start
compute scp -r ./app "$BOX:/workspace/app"
compute sandbox process start "$BOX" --cwd /workspace/app -- npm start
compute sandbox exec "$BOX" -- bash -lc 'for i in $(seq 1 30); do curl -fsS http://127.0.0.1:3000/ >/dev/null && exit 0; sleep 1; done; exit 1'
compute expose --json "$BOX" 3000
```

Use the returned `public_url` (or `/preview/<box>/3000/`) for verification. Keep the sandbox
running only as long as the user needs it; `unexpose` and delete when done.

### D. CI-style repository job

```bash
compute job run \
  --pool default \
  --arch amd64 \
  --vcpus 2 --mem 4096 --timeout 1200 \
  --files . \
  --cwd /workspace \
  -- bash -lc 'git status --short && make test'
```

For GitHub-triggered runs, connect the repo once and let the webhook create jobs; monitor with
`compute job logs`.

### E. Parallel batch with workers

```bash
compute pool create batch
compute worker token create --pool batch
# start workers on remote KVM hosts with the token
for i in 1 2 3; do
  compute job run --pool batch --follow=false --files . -- ./run-shard.sh "$i" &
done
wait
compute job list
```

### F. Dogfooding Compute itself

```bash
BOX="compute-dogfood"
compute sandbox create --name "$BOX" --tier 4vcpu --memory 8192 --disk 40960 --start
compute scp -r . "$BOX:/workspace/on-demand-vms"
compute sandbox exec "$BOX" -- bash -lc 'cd /workspace/on-demand-vms && make build'
compute sandbox exec "$BOX" -- bash -lc 'cd /workspace/on-demand-vms && go test ./cmd/... ./internal/... ./pkg/...'
# Nested daemon is reachable through the ingress proxy, not the host port:
# http://<daemon>/preview/compute-dogfood/18080/api/v1/health
compute sandbox delete "$BOX"
```

### G. Bulk cleanup

```bash
compute sandbox list
compute sandbox stop 'agent-*'
compute sandbox delete 'agent-*'
compute network prune
```

For REST-created bulk work, wait for the task and verify `succeeded`/`failed` before deleting
shared resources.

---

## 11. Safety, limits, and cleanup

- **Do not expose secrets.** Use `--ssh-password -` or environment variables; redact tokens in
  logs. Webhook/GitHub/worker secrets are returned once.
- **Protect shared resources.** Use `protect` on sandboxes that other agents or users depend on.
- **Avoid reserved names.** The `job-` prefix is reserved for ephemeral CI sandboxes.
- **Mind the disk.** Every sandbox consumes a sparse rootfs; `--disk` is MiB. Volumes and
  images also consume host disk. Check `compute doctor` and `/api/v1/admin/resources`.
- **Mind the queue.** A queued sandbox has not failed. Report the queue reason and either wait,
  free capacity, or request a smaller size.
- **Clean up exposures** before deleting a sandbox; stale port forwards are removed on stop/delete
  but explicit `unexpose` is clearer.
- **Batch deletes are destructive.** List matches first and quote wildcards.
- **Respect tenant boundaries.** Never use admin credentials for routine work.

---

## 12. Troubleshooting

| Symptom | Likely cause | Fix |
| :--- | :--- | :--- |
| `connection refused` / `cannot reach the Compute daemon` | Daemon not running or wrong address. | Check `COMPUTE_DAEMON`; start `computed`; run `compute doctor`. |
| `401 unauthorized` | Missing/expired key, or private preview without auth. | Set `COMPUTE_API_KEY`; `compute auth login`; use `--public` exposure or a serve token. |
| `403 forbidden` on admin endpoints | Caller is not an admin. | Use an admin key or ask the operator. |
| `429` with resource details | Quota or host capacity exceeded. | Stop/delete sandboxes, lower `--vcpus`/`--memory`, or raise limits. |
| Sandbox stuck `queued` | Host cannot fit the request. | Free capacity or request a smaller size; inspect `/api/v1/queue`. |
| `sandbox is deletion protected` | Protection enabled. | `compute sandbox unprotect <name>` then delete. |
| `sandbox not running` | Stopped/paused target. | `compute sandbox start` or `resume`. |
| `IP pool exhausted` | Stale allocations/TAPs after a crash. | `compute network prune` (admin). |
| Preview `404` | Wrong sandbox/port or exposure missing. | `compute ports <box>`; `compute expose <box> <port>`. |
| Preview `401` | Private exposure. | Use a tenant session, `compute expose --public`, or a serve token. |
| Preview loads but assets 404 | App assumes `/` root and you used a path prefix. | Use the dedicated serve origin from `compute expose` (subdomain/port mode). |
| `exec` hangs | Long-running command with no timeout. | Set `--timeout`; use `process start` for daemons. |
| SSH fails | Sandbox not running or key not injected. | Start it; `compute keys apply <key>`; check `compute keys list`. |
| Job stays `queued` | No worker with matching pool/arch/resources. | `compute worker list`; check pool/arch; create a token/worker. |
| Job `lost` | Worker disconnected during the run. | Resubmit; inspect worker status and logs. |
| Image build `failed` | Bad packages/base or insufficient disk. | Inspect `GET /api/v1/images/:name`; check `compute doctor`; retry with a larger `--size`. |
| Volume attach `409` | Sandbox is running/paused. | Stop the sandbox, attach, start it again. |
| `signup_disabled` | Public registration is off. | Admin must create the user or enable `COMPUTE_SIGNUP_ENABLED`. |
| Readiness `503` | Missing KVM, Firecracker, images, SSH key, cgroups, or bridge. | Follow the remediation text from `compute doctor`. |

For a failing daemon, `GET /api/v1/ready` returns the same structured checks with remediation.
`GET /api/v1/config` shows the effective redacted configuration; `GET /api/v1/stats` and
`GET /api/v1/queue` show live capacity.

---

## 13. References

- [CLI reference](./references/cli-reference.md) — every command, flag, alias, and exit code.
- [HTTP/WebSocket API reference](./references/api-reference.md) — every endpoint, payload, and
  response shape, including ingress and streaming.
- [SDK reference](./references/sdk-reference.md) — TypeScript and Python classes and methods.
- [Workflows](./references/workflows.md) — longer end-to-end recipes and multi-tenant patterns.
- [Troubleshooting and operations](./references/troubleshooting.md) — readiness, capacity,
  worker recovery, and safe cleanup.
