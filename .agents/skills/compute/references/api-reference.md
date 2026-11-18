# Compute HTTP and WebSocket API Reference

Base URL: `http://<daemon>:18080` (default `http://127.0.0.1:18080`). All paths below are
prefixed with `/api/v1` unless stated otherwise.

## Authentication and errors

```http
Authorization: Bearer <session-token-or-api-key>
X-API-Key: <api-key>
Content-Type: application/json
```

`/api/v1/health`, `/api/v1/ready`, `/api/v1/version`, `/api/v1/auth/status`,
`/api/v1/auth/login`, `/api/v1/auth/signup`, and public ingress routes do not require a key.
Everything else is authenticated and tenant-scoped.

Errors use `{"error": "message"}` with a 4xx/5xx status. Capacity/quota rejections return HTTP
`429` with a structured body:

```json
{
  "code": "RESOURCE_LIMIT_EXCEEDED",
  "resource": "cpu",
  "limit_type": "hard",
  "node_id": "nod_host_local",
  "requested": 8,
  "available": 2,
  "limit": 4,
  "error": "cpu hard limit exceeded",
  "suggestion": "stop a running sandbox or reduce the request"
}
```

Long-running create/clone/build/start calls may return `X-Compute-Operation-ID` and
`X-Compute-Boot-Warning` headers. Resolve the operation with `GET /operations/:id`.

---

## 1. System and diagnostics

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/health` | Liveness: `{status, version, name}`. |
| `GET` | `/ready` | Deep readiness: KVM, Firecracker, images, SSH key, DB, cgroups, bridge. `503` when critical checks fail. |
| `GET` | `/version` | Build info plus advertised `public_url`. |
| `GET` | `/stats` | Sandbox counts and resource totals. |
| `GET` | `/tiers` | Supported sandbox tiers. |
| `GET` | `/config` | Redacted effective daemon configuration (admin sees more). |
| `GET` | `/queue` | Queued VMs, running totals, pending backend ops, resource limits. |
| `GET` | `/limits` | Caller quota and current usage. |
| `GET` | `/dns/status` | DNS enabled/listen/zones/host IP/last error. |
| `GET` | `/admin/resources` | Host CPU/memory/disk/IO/network/system telemetry and capacity warnings (admin). |

---

## 2. Authentication, users, and organizations

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/auth/status` | `{auth_enabled, signup_enabled, admin_username}`. |
| `POST` | `/auth/signup` | `{email, username, display_name, password}`; only when signup is enabled or caller is admin. |
| `POST` | `/auth/login` | `{username, password}` → `{user, token, csrf}` and session cookie. |
| `POST` | `/auth/logout` | Clears the session. |
| `GET` | `/auth/me` | `{user, org_id, role, auth_type, scopes}`. |
| `GET` | `/auth/keys` | List the caller's machine API keys. |
| `POST` | `/auth/keys` | `{name, expires_in_seconds?}` → `{status, token, key}`. Token is shown once. |
| `DELETE` | `/auth/keys/:id` | Revoke a machine key. |
| `GET` | `/users` | List users (admin). |
| `POST` | `/users` | `{email, username, display_name, password, role}` (admin). |
| `GET` | `/users/:idOrUsername` | User details (admin). |
| `DELETE` | `/users/:idOrUsername` | Delete a user (admin; cannot delete self or bootstrap admin). |
| `GET` | `/orgs` | Organizations the caller belongs to. |
| `POST` | `/orgs` | `{name, slug?}`; creator becomes owner. |
| `GET` | `/orgs/:id` | Organization details (member or admin). |

Scoped access keys:

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/api-keys` | List access keys; secrets are never returned. |
| `POST` | `/api-keys` | `{name, role?, scope?, expires_at?}` → key with `cmp_live_...` token (once). |
| `GET` | `/api-keys/:id` | Get one key (secret redacted). |
| `DELETE` | `/api-keys/:id` | Delete a key. |

---

## 3. Sandboxes

Aliases: `/sandboxes` and `/vms` are equivalent everywhere, including sub-actions.

### Collection

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/sandboxes` | List accessible sandboxes. |
| `POST` | `/sandboxes` | Create a sandbox. |
| `POST` | `/sandboxes/bulk-create` | `{items: [CreateRequest...], async?}`. |
| `POST` | `/sandboxes/bulk-clone` | `{source_id, names[]}` or `{items: [{source_id, ...create}]}`. |
| `POST` | `/sandboxes/bulk-action` | `{ids: [], action, async?}`. |
| `POST` | `/sandboxes/bulk-delete` | `{ids: [], async?}`. |

Create body (all fields optional unless noted):

```json
{
  "name": "web-1",
  "description": "demo",
  "tier": "2vcpu",
  "vcpus": 2,
  "memory_mb": 4096,
  "disk_mb": 20480,
  "disk_gb": 20,
  "image": "ubuntu-24",
  "template": "web-base",
  "clone_from": "sandbox-id",
  "pool_id": "default",
  "worker_id": "nod_remote_1",
  "ip_address": "172.16.0.50",
  "mac_address": "aa:bb:cc:dd:ee:ff",
  "public_ip": false,
  "public_ip_enabled": false,
  "dedicated_ip": false,
  "network_isolation": "account",
  "isolated": false,
  "auto_start": true,
  "start": true,
  "protected": false,
  "ssh_key_id": "ssh_ab12cd34",
  "ssh_public_key": "ssh-ed25519 AAAA...",
  "ssh_no_default_key": false,
  "ssh_password": "optional-min-8-chars"
}
```

`tier` and explicit `vcpus`/`memory_mb` may be combined; explicit values win. `start` is an alias
for `auto_start`. `isolated` is an alias for `network_isolation: "isolated"`. A `job-` name
prefix is rejected because it is reserved for CI.

### Individual sandbox

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/sandboxes/:id` | Get one sandbox (name, ID, or internal ID). |
| `PATCH`/`PUT` | `/sandboxes/:id` | Update `public_ip`, `protected`, `network_isolation`, or `isolated`. |
| `DELETE` | `/sandboxes/:id` | Delete; `400` when protected. |
| `POST` | `/sandboxes/:id/run` or `/start` | Boot. |
| `POST` | `/sandboxes/:id/stop` | Stop and persist the disk. |
| `POST` | `/sandboxes/:id/restart` | Restart. |
| `POST` | `/sandboxes/:id/save-state` or `/snapshot` | Live snapshot; status becomes `paused`. |
| `POST` | `/sandboxes/:id/resume` | Resume from snapshot. |
| `POST` | `/sandboxes/:id/clone` | Clone with a create-style body. |
| `POST` | `/sandboxes/:id/migrate` | `{target_worker_id?, target_pool_id?, force?}`. |
| `POST` | `/sandboxes/:id/protect` | Enable deletion protection. |
| `POST` | `/sandboxes/:id/unprotect` | Disable protection. |
| `POST` | `/sandboxes/:id/network-isolation` | `{network_isolation: "account"|"isolated"}` or `{isolated: true}`. |
| `POST` | `/sandboxes/:id/public-ip` | `{public_ip_enabled: true}`. |
| `POST` | `/sandboxes/:id/config` | Update config (same fields as PATCH). |
| `POST` | `/sandboxes/:id/reconcile-ssh` | Re-apply keys and unlock accounts. |

### Execution

| Method | Path | Description |
| :--- | :--- | :--- |
| `POST` | `/sandboxes/:id/exec` | `{command, user?, timeout_seconds?}` → `{stdout, stderr, exit_code, duration_ms}`. |
| `POST` | `/sandboxes/:id/exec-stream` | Same body; SSE `data:` events with `stream`, `data`, `exit_code`, `duration_ms`, `error`. |
| `GET` | `/sandboxes/:id/ssh` | WebSocket SSH tunnel (`Upgrade: websocket`). Query: `user`, `password`, `cols`, `rows`, `term`, `pty`, `cmd`, `stream=raw`, `mode=stdio`. |
| `GET` | `/sandboxes/:id/terminal` | WebSocket terminal for the web UI (same query conventions). |
| `GET` | `/ssh/:id` | Alias for the SSH tunnel. |

### Files

| Method | Path | Description |
| :--- | :--- | :--- |
| `POST` | `/sandboxes/:id/files` or `/files/write` | `{path, content, format: "text"|"base64"}`. Creates parents. |
| `GET` | `/sandboxes/:id/files` or `/files/read?path=` | Read a file; add `format=base64` for binary. |
| `GET` | `/sandboxes/:id/files/list?path=` | List entries `{name, path, is_dir, size}`. Default path `/workspace`. |
| `POST` | `/sandboxes/:id/files/mkdir` | `{path}`. |
| `DELETE` | `/sandboxes/:id/files?path=` | Remove file or directory tree. |
| `POST` | `/sandboxes/:id/files/remove` | `{path}` alternative. |

### Processes

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/sandboxes/:id/processes` | `{processes: [{pid, ppid, user, cpu, memory, status, command, args}]}`. |
| `POST` | `/sandboxes/:id/processes` | `{command, cwd?, env?}` → `{status, pid, command}`. |
| `POST` | `/sandboxes/:id/processes/kill` | `{pid, signal}` (default `SIGTERM`). |

### Ports and previews

| Method | Path | Description |
| :--- | :--- | :--- |
| `POST` | `/sandboxes/:id/expose` | `{guest_port, host_port?, protocol?, subdomain?, public?}`. Public defaults to true for this route. |
| `GET` | `/sandboxes/:id/ports` | List this sandbox's exposures. |
| `POST` | `/sandboxes/:id/unexpose` | `{guest_port}` or `{port}`. |
| `DELETE` | `/sandboxes/:id/ports/:port` | Remove an exposure. |
| `POST` | `/sandboxes/:id/serve-token` | `{guest_port}` → short-lived private URL/token. |

Ingress routes (no `/api/v1` prefix):

```
/preview/<sandbox>/<port>/<subpath>
/api/v1/sandboxes/<sandbox>/preview/<port>/<subpath>
/api/v1/vms/<sandbox>/preview/<port>/<subpath>
<port>-<sandbox>.<base-domain>/<subpath>
<sandbox>-<port>.<base-domain>/<subpath>
<sandbox>.<username>.<zone>/<subpath>          # guest port 80
```

The proxy forwards HTTP, WebSocket, SSE, and streaming bodies. Public exposures skip tenant auth;
private exposures require a session, bearer token, or serve token.

---

## 4. Templates and images

### Templates

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/templates` | List accessible templates. |
| `POST` | `/templates` | `{vm_id, name, description?}`; snapshots the source disk. |
| `GET` | `/templates/:idOrName` | Details. |
| `DELETE` | `/templates/:idOrName` | Delete. |
| `POST` | `/templates/:idOrName/launch` | Create-and-start body with optional `name`, `vcpus`, `memory_mb`, `disk_mb`, `network_isolation`, `isolated`, `auto_start`. |

### Images

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/images` | List base and custom images with `kind` (`base`/`custom`). |
| `POST` | `/images` | Build: `{name, description?, base_image?, packages?, dockerfile?, script?, size_mb?}`. |
| `GET` | `/images/:idOrName` | Details. |
| `DELETE` | `/images/:idOrName` | Delete a custom image. |

Builds are long-running operations; poll `/operations/:id` or re-read the image until
`status` is `ready` or `failed`.

---

## 5. Volumes

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/volumes` | List volumes (including synthetic `vol_root_*` rootfs volumes). |
| `POST` | `/volumes` | `{name, size_gb|size_mb, pool?, filesystem?: "ext4"|"xfs", attached_vm?, mount_point?}`. |
| `GET` | `/volumes/:id` | Details. |
| `DELETE` | `/volumes/:id` | Delete. |
| `POST` | `/volumes/:id/attach` | `{vm_id}`; sandbox must be stopped/paused. |
| `POST` | `/volumes/:id/detach` | Detach. |
| `POST` | `/volumes/:id/expand` | `{add_gb}`. |
| `POST` | `/volumes/:id/snapshot` | Create a volume snapshot. |

`409 Conflict` means the sandbox is running or paused. Attached volumes are mounted in the guest
after boot (default `/mnt/data`).

---

## 6. Jobs and CI

### Jobs

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/jobs` | List accessible jobs. |
| `POST` | `/jobs` | Create a job. |
| `GET` | `/jobs/:id` | Job state, spec, result, timestamps. |
| `GET` | `/jobs/:id/logs` | Log records `{stream, seq, data, ...}`. |
| `POST` | `/jobs/:id/cancel` | Cancel queued/running work. |

Create body:

```json
{
  "type": "command",
  "pool_id": "default",
  "priority": 10,
  "source": "cli",
  "repo_id": "gh_ab12cd34",
  "repo_url": "https://github.com/org/repo",
  "repo_name": "repo",
  "branch": "main",
  "commit_sha": "abc123",
  "spec": {
    "image_id": "ubuntu-24",
    "architecture": "amd64",
    "vcpus": 2,
    "memory_mb": 2048,
    "disk_mb": 10240,
    "command": ["make", "test"],
    "cwd": "/workspace",
    "files": "<base64 tar.gz>",
    "env": {"CI": "true"},
    "timeout_seconds": 900,
    "network_policy": "default",
    "repo_url": "...",
    "repo_name": "...",
    "branch": "main",
    "commit_sha": "...",
    "source": "cli"
  }
}
```

Job states: `queued`, `assigned`, `running`, `succeeded`, `failed`, `canceled`, `lost`.
The control plane overwrites `authorized_owner_ids` and `allow_cross_owner`; client values are
ignored.

### GitHub-connected repositories

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/ci/repos` | List connections. |
| `POST` | `/ci/repos` | `{name?, repo_url, default_branch?, github_token?, webhook_secret?, default_command?, vcpus?, memory_mb?, timeout_sec?}`. Response includes the generated `webhook_secret` once. |
| `GET` | `/ci/repos/:id` | Details. |
| `PATCH`/`PUT` | `/ci/repos/:id` | Update any connection field. |
| `DELETE` | `/ci/repos/:id` | Delete. |
| `POST` | `/ci/repos/:id/trigger` | `{branch?, commit_sha?, command?, source?, image_id?, pool_id?, vcpus?, memory_mb?, timeout_sec?, env?}` → job. |
| `POST` | `/ci/webhooks/github` | GitHub push webhook (HMAC-verified with the connection secret). |

---

## 7. Workers, pools, and scheduling

### Workers

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/workers` | List workers (admin). |
| `GET` | `/workers/:id` | Details (admin). |
| `PATCH`/`PUT` | `/workers/:id` | Update `pool_id`, `allocated_vcpus`, `allocated_mem_mb`, `allocated_disk_mb`, `labels`, `status` (admin). |
| `DELETE` | `/workers/:id` | Remove (admin). |
| `POST` | `/workers/:id/cordon` | Stop new placements. |
| `POST` | `/workers/:id/uncordon` | Resume placements. |
| `POST` | `/workers/:id/drain` | Evict and cordon. |
| `POST` | `/workers/:id/reconcile` | Refresh heartbeat/status. |
| `POST` | `/workers/reconcile` | Reconcile all workers. |

Worker tokens:

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/worker-tokens` | List enrollment tokens (admin). |
| `POST` | `/worker-tokens` | `{name?, pool_id?, expires_at?}` → token (30-minute default). |
| `DELETE` | `/worker-tokens/:id` | Delete/revoke. |

Worker transport: `GET /worker/connect` upgrades to a WebSocket and speaks the
`compute.v1` worker protocol (hello, heartbeat, assignment, logs, artifacts, cancel, drain,
reconcile, RPC, tunnel frames).

### Pools

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/pools` | List pools (admin). |
| `POST` | `/pools` | `{name, labels?, policy?}`. |
| `GET` | `/pools/:id` | Details. |
| `DELETE` | `/pools/:id` | Delete. |
| `POST` | `/pools/:id/migrate` | `{target_pool_id?, target_worker_id?, migrate_workers?, migrate_sandboxes?, force?}`. |

### Scheduling policy

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/scheduler/policy` | Current cluster policy (admin). |
| `PUT`/`PATCH` | `/scheduler/policy` | Update policy. |

Fields: `strategy` (`least_loaded`, `round_robin`, `bin_pack`, `spread`), `cpu_weight`,
`memory_weight`, `disk_weight`, `cpu_soft_overcommit_ratio`, `cpu_hard_overcommit_ratio`,
`cpu_share_weight_base`, `memory_hard_guarantee`, `idle_ram_snapshot_enabled`,
`idle_threshold_seconds`, `auto_reclaim_on_pressure`, `host_reserved_vcpus`,
`host_reserved_memory_mb`, `host_reserved_disk_mb`, `disk_hard_enforcement`.

Validation: `cpu_soft_overcommit_ratio >= 1.0`; `cpu_hard_overcommit_ratio >= soft`; weights and
reserves must be non-negative.

---

## 8. Networking and port forwards

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/network` | Bridge/subnet/gateway/NAT/allocation summary. |
| `GET` | `/network/config` | Effective network configuration. |
| `GET` | `/network/allocations` | IP/MAC/TAP/public-IP allocations. |
| `GET` | `/network/inspect/:id` | Per-sandbox connectivity details. |
| `POST` | `/network/prune` | Remove orphaned TAPs/leases (admin). |
| `GET` | `/network/port-forwards?vm_id=` | List exposures. |
| `POST` | `/network/port-forwards` | Create/update an exposure. |
| `GET` | `/network/port-forwards/:id` | Details. |
| `PATCH` | `/network/port-forwards/:id` | `{public: bool}`. |
| `DELETE` | `/network/port-forwards/:id` | Remove. |

Create body: `{vm_id|vm_name, guest_port, host_port?, protocol?, subdomain?, public?}`.
`guest_port` is required (1–65535). Re-posting the same sandbox/guest-port/protocol updates the
existing exposure and preserves its origin. `public` defaults to false here (unlike the
`/sandboxes/:id/expose` shortcut, which defaults to true).

---

## 9. SSH keys

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/keys` | `{keys: [...]}` for the caller. |
| `POST` | `/keys` | `{name, public_key, all_sandboxes?, is_default?}`. First key becomes default. |
| `GET` | `/keys/:idOrName` | Details. |
| `PATCH`/`PUT` | `/keys/:idOrName` | `{name?, public_key?, all_sandboxes?, is_default?}`. |
| `DELETE` | `/keys/:idOrName` | Delete. |
| `POST` | `/keys/:idOrName/apply` | Mark account-wide and sync to all sandboxes. |
| `POST` | `/keys/reconcile` | Reconcile keys/accounts for the caller's sandboxes. |

Responses for `apply`/`reconcile` include `{key, sync: {applied: [...], failed: [{vm_id, name, error}]}}`.

---

## 10. Events, webhooks, telemetry, environments

### Audit events

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/events` | Last 100 audit events (non-admins see their own actor only). |
| `POST` | `/events` | Record a custom event `{event, actor?, target?, status?, duration_ms?, payload?}`. |
| `GET` | `/events/stream` | SSE stream of live events. |

### Webhooks (admin)

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/webhooks` | List endpoints. |
| `POST` | `/webhooks` | `{url, description?, secret?, events[]}`. Defaults to sandbox/snapshot/CI events; `*` and `prefix.*` subscriptions work. |
| `GET` | `/webhooks/:id` | Details. |
| `PATCH`/`PUT` | `/webhooks/:id` | Update URL/description/secret/events/active. |
| `DELETE` | `/webhooks/:id` | Delete. |
| `POST` | `/webhooks/:id/test` | Send a test delivery. |

Delivery headers: `X-Compute-Event`, `X-Compute-Signature: sha256=<hmac>`, `Content-Type:
application/json`. Body: `{id, event, timestamp, data, api_version}`.

### OpenTelemetry (admin)

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/otel` | Current config. |
| `PUT`/`POST` | `/otel` | Save config: `enabled`, `endpoint`, `protocol`, `auth_header`, `service_name`, `sample_rate`, `export_interval`, `metrics_enabled`, `traces_enabled`, `logs_enabled`. |
| `POST` | `/otel/test` | Send a test OTLP trace; requires enabled + endpoint. |

### Environment profiles (admin)

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/environments` | List profiles; seeds a `base` default when empty. |
| `POST` | `/environments` | Save a profile (`name` required). |
| `GET` | `/environments/:name` | Details. |
| `PUT`/`POST` | `/environments/:name` | Upsert. |
| `DELETE` | `/environments/:name` | Delete. |

---

## 11. Operations, tasks, and WebSocket

### Operations

Durable records for creates, deletes, starts, stops, snapshots, clones, image builds, and volume
operations. Statuses: `queued`, `running`, `succeeded`, `failed`, `canceled`.

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/operations?status=&kind=&task_id=&limit=` | List newest first. |
| `GET` | `/operations/:id` | One operation. |

### Tasks

Asynchronous bulk requests return `task_id`. Task statuses: `queued`, `running`, `completed`,
`partial`, `failed`.

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/tasks` | List tasks. |
| `GET` | `/tasks/:id` | Final/ongoing outcome with `succeeded`, `failed`, `warnings`, `requested`, `queued`, `completed`. |

### Cluster WebSocket

`GET /api/v1/ws` (optional `?token=...`) upgrades to a WebSocket. The server immediately sends
a JSON cluster snapshot and then re-sends on changes and every 2 seconds:

```json
{
  "type": "snapshot",
  "stats": {}, "vms": [], "allocations": [], "network_config": {},
  "config": {}, "queue": {}, "workers": [], "pools": [], "worker_tokens": [],
  "scheduling_policy": {}, "resources": {}, "templates": [], "images": [],
  "port_forwards": [], "jobs": [], "volumes": [], "events": [],
  "timestamp": "2026-11-18T12:00:00Z"
}
```

Sending any text frame requests a fresh snapshot. Non-admin clients receive only their own
resources. This socket is for live UI state, not for commands.

---

## 12. Ingress authorization

Private exposures redirect browsers to `/api/v1/serve/authorize?next=<url>`. After a successful
tenant session it redirects back to the requested origin. Programmatic clients can send
`Authorization: Bearer <token>` directly or mint a serve token with
`POST /sandboxes/:id/serve-token`.
