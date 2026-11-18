# Compute Troubleshooting and Operations

## 1. Readiness first

```bash
compute doctor
```

or:

```bash
curl -sS http://127.0.0.1:18080/api/v1/ready | jq
```

The readiness report checks:

| Check | Critical? | Typical remediation |
| :--- | :--- | :--- |
| `os` | yes | Run on Linux. |
| `arch` | yes | Use amd64 or arm64. |
| `host_network` | yes | Run the daemon with `network_mode: host` and `COMPUTE_HOST_NETWORK=1` in containers. |
| `kvm` | yes | Load KVM, pass `/dev/kvm`, add the user to the `kvm` group or `chmod 666 /dev/kvm`. |
| `firecracker` | yes | Install Firecracker or set `COMPUTE_FIRECRACKER_BIN`. |
| `kernel` | yes | Run `./scripts/install.sh` or `make build-images`. |
| `rootfs` | yes | Run `./scripts/install.sh` or `make build-images`. |
| `ssh_key` | yes | Regenerate the internal key with the installer; ensure mode `0600`. |
| `images_dir` | yes | Create/mount the data directory. |
| `database` | yes | Make `COMPUTE_DB_PATH` writable. |
| `cgroups` | warning/critical | Enable cgroup v2 and delegation; do not disable enforcement in production. |
| `disk_space` | warning | Free space above `COMPUTE_MIN_FREE_DISK_MB`. |
| `jailer` | depends | Install jailer or disable `COMPUTE_JAILER_ENABLED`. |
| `bridge` | yes | Let the daemon create the bridge; check for name conflicts. |

If the daemon will not start, inspect its logs (`docker compose logs -f computed`,
`journalctl -u computed`, or the foreground process). Startup refuses to continue on critical
preflight failures unless `COMPUTE_ALLOW_UNREADY_STARTUP=true`.

---

## 2. Connection and authentication

| Symptom | Cause | Fix |
| :--- | :--- | :--- |
| `connection refused` | Wrong `COMPUTE_DAEMON`, daemon down, or wrong port. | `curl $COMPUTE_DAEMON/api/v1/health`; start `computed`; check `COMPUTE_LISTEN`. |
| `401 unauthorized` | No key, expired/revoked key, or private ingress without auth. | `export COMPUTE_API_KEY=...`; `compute auth login`; re-create a key. |
| `403 forbidden` | Non-admin calling an admin endpoint, or missing API-key scope. | Use an admin key; check scopes for SSH (`ssh:vms`). |
| `signup_disabled` | Public registration is off. | Admin creates the user or sets `COMPUTE_SIGNUP_ENABLED=true`. |
| Cookie login works, CLI does not | CLI uses a machine key, not the browser session. | `compute auth login` or export an API key. |
| `unix socket` connection fails | Socket path/permissions. | Use `COMPUTE_DAEMON=unix:///path`; ensure the daemon user can access it. |

---

## 3. Sandbox creation and lifecycle

| Symptom | Cause | Fix |
| :--- | :--- | :--- |
| Sandbox stuck `queued` | Host capacity or hard limits. | `compute sandbox info <name>` shows the queue reason; stop/delete idle sandboxes or request fewer vCPUs/RAM/disk. |
| `429` `RESOURCE_LIMIT_EXCEEDED` | CPU/RAM/disk hard limit or overcommit. | Free capacity; lower the request; admin can tune the scheduling policy. |
| `429` quota error | Tenant quota. | `GET /api/v1/limits`; delete unused resources or ask an admin. |
| `no IP addresses available` / pool exhausted | Stale allocations after a crash. | `compute network prune` (admin), then retry. |
| `sandbox is deletion protected` | Protection enabled. | `compute sandbox unprotect <name>` then delete. |
| Create succeeds but boot warns | Auto-start failed after the record was created. | Read `X-Compute-Boot-Warning`; inspect status and logs; `compute sandbox start <name>`. |
| `job-` prefix rejected | Reserved for CI. | Choose another name. |
| `start` fails with disk error | Host disk full or sparse file limit. | `compute doctor`, free disk, delete unused sandboxes/volumes/images. |
| Status `failed` after start | Guest boot failure, missing image/kernel, or network setup error. | Check daemon logs; verify readiness; retry with a known-good image. |

A queued sandbox is not a failed sandbox. Report the queue reason and either wait, free capacity,
or ask the user to reduce the request.

---

## 4. Command execution, files, and SSH

| Symptom | Cause | Fix |
| :--- | :--- | :--- |
| `exec` hangs | Long-running command without a timeout. | Add `--timeout`; for daemons use `process start`. |
| `exec` returns 127 | Command not installed in the guest. | Install it or use an image that includes it. |
| File read returns base64-looking text | Binary file or explicit `base64` format. | Use `--format base64` and decode locally. |
| `cp`/`scp` fails | Sandbox stopped, wrong path syntax, or directory without `-r`. | Start the sandbox; use `<sandbox>:<path>`; add `-r` for directories. |
| SSH `Permission denied` | Key not injected or account locked. | `compute keys list`; `compute keys apply <key>`; `compute keys reconcile <sandbox>`. |
| SSH says sandbox not running | Target stopped/paused. | `compute sandbox start` or `resume`. |
| SSH host key warnings | Each guest is a fresh host key. | Expected for ephemeral sandboxes; use the CLI tunnel rather than direct SSH. |

---

## 5. Ingress and previews

| Symptom | Cause | Fix |
| :--- | :--- | :--- |
| Preview `404` | No exposure or wrong sandbox/port. | `compute ports <box>`; `compute expose <box> <port>`. |
| Preview `401` | Private exposure. | Use a session, `--public`, or a serve token. |
| Preview works at `/` but assets 404 | App assumes root and you used a path prefix. | Use the dedicated serve origin (subdomain/port mode) from `compute expose --json`. |
| Redirects go to the wrong host | App uses absolute URLs. | Use a dedicated origin, not path-prefix routing. |
| WebSocket fails | Proxy route or app mismatch. | Verify the exposure is active; the proxy supports WS/SSE. |
| Host port unreachable in Docker | Port not published at container start. | Use `/preview/...` or a serve origin; or publish the port in Compose. |
| `sandbox exposure not found` | Exposure deleted or sandbox stopped/deleted. | Re-expose after starting the sandbox. |
| DNS name does not resolve | DNS disabled or zone not delegated. | Enable `COMPUTE_DNS_ENABLED`, set the base domain, delegate the zone; check `/api/v1/dns/status`. |

---

## 6. Jobs and CI

| Symptom | Cause | Fix |
| :--- | :--- | :--- |
| Job stays `queued` | No worker matches pool/arch/resources. | `compute worker list`; check pool, arch, and free capacity; create a token/worker. |
| Job `failed` immediately | Bad image, command, or working directory. | `compute job logs <id>`; verify `--image`, `--cwd`, and the command. |
| Job `lost` | Worker disconnected mid-run. | Resubmit; inspect worker heartbeat/status. |
| Job timeout | Command exceeded `--timeout`. | Increase it or optimize the command. |
| `--files` upload too large | Request body limit. | Exclude build artifacts/VCS data; upload a smaller tree or clone in the guest. |
| CI webhook creates no job | Bad secret/signature, wrong URL, or repo disconnected. | Check the GitHub delivery log; re-copy the webhook secret; verify `/api/v1/ci/repos`. |
| CI job uses the wrong command | `default_command` or trigger override. | Update the connection or pass `command` in the trigger body. |

---

## 7. Workers, pools, and scheduling

| Symptom | Cause | Fix |
| :--- | :--- | :--- |
| Worker never appears | Cannot reach control plane, bad token, or token expired. | Check worker logs; mint a fresh token; verify `COMPUTE_CONTROL_PLANE`. |
| Worker reconnects but is `offline` | Heartbeat missed. | `POST /api/v1/workers/:id/reconcile`; check network/proxy timeouts. |
| No placements on a worker | Cordoned/draining or pool mismatch. | `POST /api/v1/workers/:id/uncordon`; verify `pool_id`. |
| Drain does not finish | Running jobs cannot be evicted. | Cancel jobs or wait; check worker logs. |
| Pool delete fails | Workers/sandboxes still reference it. | Migrate them first with `POST /api/v1/pools/:id/migrate`. |
| Migration fails | Target lacks capacity or sandbox is running without `force`. | Free capacity; use `force` only when acceptable. |
| Scheduling rejects policy | Invalid ratio/weight/reserve. | `cpu_hard >= cpu_soft >= 1.0`; weights/reserves non-negative. |

---

## 8. Images, templates, and volumes

| Symptom | Cause | Fix |
| :--- | :--- | :--- |
| Image build `failed` | Package install error, bad base, or insufficient disk. | Read the operation/image error; retry with a valid base and larger `size_mb`. |
| Image build stuck `building` | Daemon crashed mid-build. | Restart reconciles completed rootfs; otherwise delete and rebuild. |
| Template create fails | Source sandbox missing or disk error. | Verify the source; free disk; retry. |
| Template launch fails | Template disk missing or quota exceeded. | Check `/api/v1/limits` and daemon logs. |
| Volume attach `409` | Sandbox is running/paused. | Stop it, attach, start it. |
| Volume not mounted in guest | Post-boot setup failed. | Check daemon logs; verify the volume is attached and the guest booted. |
| Volume expand fails | Filesystem/tooling error. | Check daemon logs; ensure the volume is not in use. |
| `no space left on device` | Host or guest disk full. | Free host disk; expand the volume; delete unused images/templates. |

---

## 9. Operations, tasks, and audit

- A create/start/clone/build may return before completion. Use the `X-Compute-Operation-ID`
  header or list `GET /api/v1/operations` to follow it.
- Bulk requests return a `task_id`; `GET /api/v1/tasks/:id` shows `succeeded`, `failed`, and
  `warnings`. `partial` means some items succeeded.
- Audit events are at `GET /api/v1/events` (last 100) and stream at `/events/stream`.
- If an operation is stuck `running`, check daemon/worker logs and the queue. Restarting the
  daemon reconciles persisted state; do not delete database rows manually.

---

## 10. Safe cleanup

```bash
compute sandbox list
compute sandbox stop 'agent-*'
compute sandbox delete 'agent-*'
compute network prune
```

Before deleting:

- confirm the prefix matches only your resources;
- detach volumes and delete exposures;
- keep templates/images the user asked to preserve;
- do not delete protected sandboxes without unprotecting;
- do not delete shared pools/workers while jobs are running.

If cleanup cannot be completed, report exactly what remains and why.

---

## 11. Logs and evidence

| Component | Where to look |
| :--- | :--- |
| Daemon (foreground) | stdout/stderr of `computed`. |
| Docker Compose | `docker compose logs -f computed` / `compute-worker`. |
| systemd | `journalctl -u computed -u compute-worker`. |
| Readiness | `compute doctor` or `GET /api/v1/ready`. |
| Capacity | `GET /api/v1/stats`, `GET /api/v1/queue`, `GET /api/v1/admin/resources`. |
| Operations | `GET /api/v1/operations/:id`. |
| Audit | `GET /api/v1/events`, `GET /api/v1/events/stream`. |
| Jobs | `compute job logs <id>`. |
| Nested daemon | `/tmp/computed.log` inside the dogfood sandbox. |

When reporting a failure, include the command, the full error, the sandbox/job/operation ID, the
daemon readiness summary, and whether the host has `/dev/kvm`.
