# Compute CLI Reference

The `compute` binary is both the user CLI and the GUI launcher. It talks to `computed` over
HTTP/WebSocket. Run `compute help` or `compute <group> --help` for the built-in text.

## Global environment

| Variable | Default | Description |
| :--- | :--- | :--- |
| `COMPUTE_DAEMON` | `127.0.0.1:18080` | Daemon address. Accepts `host:port`, `http(s)://...`, or `unix:///path`. |
| `COMPUTE_HOST` | — | Fallback address. A bare host gets `:18080` appended. |
| `COMPUTE_API_KEY` | — | API key or session token. Overrides `~/.compute/config.json`. |
| `HOME` | — | `~/.compute/config.json` stores CLI credentials (mode `0600`). |

Global output conventions: tables are for humans, `--json` (where available) is stable for
scripts. Errors go to stderr and exit non-zero.

---

## 1. Connection and diagnostics

### `compute version`

```bash
compute version            # client + daemon, build, commit, date
compute version --client   # client only, no network
compute version --short    # version string only
compute version --json     # JSON
compute version --daemon 10.0.0.5:18080
```

Flags: `--client`/`-c`, `--short`/`-s`, `--json`/`-j`, `--daemon <addr>`.

### `compute doctor` / `compute check`

Calls `GET /api/v1/ready` and prints each readiness check with severity, detail, and remediation.
Exits non-zero when a critical check fails. Use this before provisioning on a new host.

### `compute gui` / `compute web` / `compute ui`

```bash
compute gui --port 8080 --addr 127.0.0.1 --daemon 127.0.0.1:18080
```

Starts the standalone web UI server. The daemon already serves the same UI on its own port.

### `compute completion`

```bash
compute completion bash|zsh|fish
compute completion install     # writes ~/.bash_completion.d/compute
```

---

## 2. Authentication (`compute auth`)

```bash
compute auth login [--username <user-or-email>] [--password <password>]
compute auth signup --email <email> --username <username> --password <password> [--name <display-name>]
compute auth whoami
compute auth keys [list]
compute auth keys create [--name <name>]
compute auth keys revoke <key-id>
compute auth logout
```

- `login` prompts for missing fields, authenticates, creates a long-lived machine API key, and
  saves it to `~/.compute/config.json`.
- `signup` only succeeds when the daemon has `COMPUTE_SIGNUP_ENABLED=true` (or the caller is an
  admin).
- `whoami` prints user ID, email, username, display name, system role, org ID, and org role.
- `auth keys create` prints the secret once; `revoke` deletes it.
- `logout` removes the local config file.

REST equivalents: `POST /api/v1/auth/login`, `/auth/signup`, `GET /auth/me`,
`GET|POST /auth/keys`, `DELETE /auth/keys/:id`, `POST /auth/logout`.

---

## 3. Sandboxes (`compute sandbox`, alias `compute vm`)

### `sandbox list`

```bash
compute sandbox list
```

Lists sandboxes with name/ID, status, IP, resources, and URL/SSH hints.

### `sandbox create`

```bash
compute sandbox create [flags]
```

| Flag | Description |
| :--- | :--- |
| `--name <name>` | Human-readable name. |
| `--tier <tier>` | `1vcpu`, `2vcpu`, `4vcpu` (aliases such as `micro`/`small`/`medium` are accepted by the API). |
| `--vcpus <n>` | Explicit vCPU count (1, 2, or 4). |
| `--memory <MiB>` | RAM size. |
| `--disk <MiB>` | Disk size in MiB (`disk_mb`). Use `20480` for 20 GiB. |
| `--image <name>` | Base/custom image name. |
| `--template <name\|id>` | Launch from a saved disk template. |
| `--ssh-key <name\|path>` | Select a saved key or a public-key file. |
| `--no-ssh-key` | Do not embed the account default key. |
| `--ssh-password <pw>` | Password for `compute` and `root` (min 8 chars; `-` reads stdin). |
| `--network-isolation account\|isolated` | Cross-sandbox visibility. |
| `--isolated` | Shorthand for `--network-isolation isolated`. |
| `--protected` | Enable deletion protection. |
| `--start`, `--boot` | Boot immediately after creation. |

### Lifecycle

```bash
compute sandbox start <target>...        # alias: run
compute sandbox stop <target>...
compute sandbox save-state <target>      # alias: snapshot
compute sandbox resume <target>
compute sandbox delete <target>...       # alias: rm
compute sandbox protect <target>
compute sandbox unprotect <target>
compute sandbox network-isolation <target> <account|isolated>
compute sandbox info <target>            # aliases: inspect, show
```

Targets may be names, IDs, internal IDs, or shell-style patterns (`'dev-*'`, `'temp-?'`).
Patterns are expanded against all accessible sandboxes; quote them to avoid shell expansion.

### Discovery and access

```bash
compute sandbox images
compute sandbox templates
compute sandbox url [--json] [--port <n>] [-u <user>] [-v] <target>
compute sandbox expose [--public|--private] [--subdomain <name>] [--json] <target> <guest-port>
compute sandbox ports [--json] <target>
compute sandbox unexpose <target> <guest-port>
```

### Execution and SSH

```bash
compute sandbox exec [--no-stream] [--json] [--timeout <sec>] [--user <user>] <target> -- <command...>
compute sandbox ssh [-u <user>] [-p <password>] [-t|-T] [--proxy] [user@]<target> [-- <command...>]
```

`--json` implies `--no-stream` and prints `{exit_code, stdout, stderr, duration_ms}`.
`--timeout 0` means no timeout while streaming. `--proxy` exposes raw stdio over the SSH
WebSocket for external tools.

### Files and directories

```bash
compute sandbox file read <target> <path> [--format text|base64]
compute sandbox file write <target> <path> [--content <data> | --file <local-path> | -]
compute sandbox file list <target> [path]
compute sandbox file mkdir <target> <path>
compute sandbox file remove <target> <path>
```

### Copy

```bash
compute sandbox cp <src> <dst>
compute cp <src> <dst>
compute scp [-r] <src> [<src>...] <dst>
```

Remote paths use `<sandbox>:<path>`. `cp` copies one path in either direction; `scp` supports
multiple sources and requires `-r`/`-R`/`--recursive` for directories. A trailing slash on a
remote directory source copies its contents.

### Processes

```bash
compute sandbox process list <target>
compute sandbox process start <target> [--cwd <path>] [--env KEY=VAL]... [--] <command...>
compute sandbox process kill <target> <pid> [--signal SIGTERM|SIGKILL]
```

`process start` launches a detached background process and prints its PID.

### Lifecycle notes

- `created` → `start` → `running`; `stop` persists the disk; `start` reuses it.
- `save-state` pauses the VM and stores memory/CPU state; `resume` restores it.
- A `queued` sandbox is waiting for capacity, not failed.
- `delete` removes the VM and its rootfs; protection blocks it.
- `job-` is a reserved name prefix for ephemeral CI sandboxes.

---

## 4. Jobs (`compute job`, alias `compute jobs`)

```bash
compute job run [flags] -- <command...>
compute job list
compute job info <id>
compute job logs <id>
compute job cancel <id>
```

### `job run` flags

| Flag | Default | Description |
| :--- | :--- | :--- |
| `--image <name>` | daemon default | Base/custom image. |
| `--pool <pool>` | `default` | Target worker pool. |
| `--arch <arch>` | host arch | `amd64` or `arm64` (`x86_64`/`x64`/`aarch64` normalized). |
| `--vcpus <n>` | `2` | Required vCPUs. |
| `--mem <MiB>` | `2048` | Required memory. |
| `--files <path>` | — | Directory/file tree packaged as base64 tar.gz and extracted in `--cwd`. |
| `--cwd <path>` | `/workspace` | Working directory in the job sandbox. |
| `--timeout <sec>` | `0` | Job timeout (0 = unlimited). |
| `--follow` | `true` | Stream logs until completion. Set `--follow=false` for async submission. |

Exit codes: `0` succeeded, `1` failed/submission error, `130` canceled.

Jobs run on a connected worker or locally on the daemon host when no worker is available.
`job list` shows recent jobs; `job logs` prints stored stdout/stderr; `job info` prints state,
timestamps, and result JSON.

REST: `POST /api/v1/jobs`, `GET /api/v1/jobs`, `GET /api/v1/jobs/:id`,
`GET /api/v1/jobs/:id/logs`, `POST /api/v1/jobs/:id/cancel`.

---

## 5. Workers and pools

### `compute worker`

```bash
compute worker list
compute worker token create [--pool <pool>] [--name <worker-name>]
compute worker info <worker-id>
compute worker cordon <worker-id>
compute worker drain <worker-id>
compute worker delete <worker-id>
```

`token create` prints a one-time enrollment token and a ready-to-run `compute-worker` command.
Tokens expire after 30 minutes; an unnamed token binds to the first worker that enrolls and is
accepted again when that same worker restarts. `--name` pins the token to one worker name.

`cordon` stops new placements; `drain` evicts running jobs and cordons. Uncordon, reconcile, and
limit/pool/label updates are REST-only (`POST /api/v1/workers/:id/uncordon`,
`POST /api/v1/workers/:id/reconcile`, `PATCH /api/v1/workers/:id`).

### `compute pool`

```bash
compute pool list
compute pool create <name>
compute pool delete <id>
```

Pool migration (`POST /api/v1/pools/:id/migrate`) is REST-only.

### Worker process

```bash
compute-worker run --control-plane <url> --token <token> --pool <pool> --name <name>
```

Worker configuration: `COMPUTE_CONTROL_PLANE`, `COMPUTE_WORKER_TOKEN`, `COMPUTE_WORKER_NAME`,
`COMPUTE_POOL_ID`, `COMPUTE_WORKER_STATE_DIR`, `COMPUTE_WORKER_MAX_VCPUS`,
`COMPUTE_WORKER_MAX_MEMORY_MB`, `COMPUTE_WORKER_MAX_DISK_MB`,
`COMPUTE_WORKER_BOOT_TIMEOUT_SECONDS`, `COMPUTE_WORKER_ALLOW_LOCAL_EXEC` (development only).

---

## 6. Images (`compute image`)

```bash
compute image list
compute image build --name <name> [--base <image>] [--packages <a,b>] [--size <MiB>] [--desc <text>]
compute image inspect <name|id>
compute image delete <name|id>
```

`build` creates a custom rootfs from a base image and optional packages. The REST body also
accepts `dockerfile` and `script` for custom provisioning. Builds are long-running operations;
poll `GET /api/v1/operations/:id` or `GET /api/v1/images/:name`.

---

## 7. Templates (`compute template`)

```bash
compute template list
compute template create [<name>] --vm <name|id> [--name <name>] [--desc <text>]
compute template launch <name|id> [--name <vm-name>] [--tier <tier>] [--network-isolation <mode>] [--isolated]
compute template inspect <name|id>
compute template delete <name|id>
```

A template is a reusable disk snapshot taken from an existing sandbox. `launch` provisions and
starts a new sandbox, applying any overrides.

---

## 8. SSH keys (`compute keys`, aliases `key`, `ssh-keys`)

```bash
compute keys list
compute keys add --name <name> [--key-file <path>|-] [--all] [--default]
compute keys update <name|id> [--name <name>] [--key-file <path>|-] [--all|--no-all] [--default|--no-default]
compute keys apply <name|id>
compute keys set-default <name|id>
compute keys reconcile [<sandbox>|--all]
compute keys show <name|id>
compute keys delete <name|id>...
```

The first saved key becomes the account default. `--all` embeds the key into every current and
future sandbox; `apply` promotes an existing key to account-wide and syncs it. `reconcile`
repairs SSH access and unlocks accounts across one sandbox or the whole account.

---

## 9. Networking (`compute network`)

```bash
compute network status                 # aliases: info, show
compute network list                   # alias: ls
compute network inspect <target>
compute network prune
```

`status` prints mode, bridge, subnet, gateway, NAT state, allocation count, and active TAPs.
`list` prints IP/MAC/TAP/public-IP allocations. `inspect` checks a sandbox's IP, TAP, bridge,
gateway, and reachability. `prune` removes orphaned TAPs, stale leases, and dangling allocations
(admin-only).

REST adds `GET /api/v1/network/config`, `GET /api/v1/network/allocations`,
`GET /api/v1/network/inspect/:id`, `POST /api/v1/network/prune`, and the port-forward API.

### Port exposure shortcuts

```bash
compute expose [--public|--private] [--subdomain <name>] [--json] <target> <guest-port>
compute ports [--json] <target>
compute unexpose <target> <guest-port>
compute url [--json] [--port <n>] [-u <user>] [-v] <target>
```

`expose` publishes at a dedicated origin; `ports` lists exposures; `unexpose` removes one.
`url` prints the best URL, or the SSH target when no URL resolves.

---

## 10. Help and aliases

```bash
compute help
compute help sandbox
compute sandbox --help
```

Aliases: `vm` = `sandbox`, `jobs` = `job`, `workers` = `worker`, `pools` = `pool`,
`images` = `image`, `templates` = `template`, `key`/`ssh-keys` = `keys`, `ps` = `process`,
`files` = `file`, `net` = `network`, `web`/`ui` = `gui`, `check` = `doctor`,
`autocomplete` = `completion`.

---

## 11. Daemon and worker binaries

`computed` is the control plane; it is not driven through `compute`.

```bash
computed [--config <file>] [--listen <addr>] [--db-path <path>] [--images-dir <dir>] ...
computed check [--json]
computed init [--data-dir <path>] [--images] [--images-mode auto|download|prebuilt|build|none]
computed admin user create --username <name> --email <email> --password <pw> [--role admin|user] [--data-dir <path>]
computed admin apikey create --username <name> --name <key-name> [--data-dir <path>]
computed admin audit verify [--data-dir <path>]
computed version
```

`compute-worker` joins a control plane:

```bash
compute-worker run \
  --control-plane <url> \
  --token <enrollment-token> \
  [--name <worker-name>] [--pool <pool>] \
  [--max-vcpus <n>] [--max-memory-mb <n>] [--max-disk-mb <n>] \
  [--config <file>]
compute-worker version
```

Build and verification targets: `make build`, `make vet`, `make test`, `make test-web`,
`make test-features` (requires KVM + a running daemon), `make validate-compose`.

---

## 12. Exit codes and scripting

| Code | Meaning |
| :--- | :--- |
| `0` | Success. |
| `1` | Command failed, job failed, readiness failed, or API error. |
| `130` | Interrupted or job canceled. |

For scripts, prefer `--json` where available and the REST API for anything the CLI does not
expose (volumes, bulk operations, CI repos, webhooks, OTel, operations/tasks, scheduler policy,
worker limits, migrations, user/org administration).
