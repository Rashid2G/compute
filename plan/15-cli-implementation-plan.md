# 15 — Boat-like `compute` CLI and WebSocket Implementation Plan

## 1. Objective

Give Compute a simple, scriptable sandbox CLI similar to Boat while preserving
Compute's self-hosted Firecracker and private-cluster scope.

The CLI must work for:

1. A developer operating one local daemon.
2. A team operating a remote Compute cluster.
3. CI jobs creating an ephemeral VM, running a command, collecting output, and
   deleting the VM.
4. An AI agent that needs deterministic JSON, safe retries, command execution,
   file transfer, and event streams.

The CLI is a client of the daemon. It must not access the database, Firecracker
socket, VM disk, or host network directly.

## 2. Target command tree

The new commands use `sandbox` as the user-facing resource name. `vm` is an
accepted alias. Existing flat commands remain compatibility aliases until a
later major release.

```text
compute [global flags] <command>
├── auth
│   ├── login
│   ├── logout
│   ├── whoami
│   └── status
├── sandbox (vm)
│   ├── create [--name --image --size --from --environment --env --setup-file]
│   ├── list [--all --state --label]
│   ├── info <id|name|current>
│   ├── start <id|name>
│   ├── stop <id|name> [--force]
│   ├── resume <id|name> [--size]
│   ├── restart <id|name>
│   ├── delete <id|name> [--yes]
│   ├── fork <id|name> [--name --size]
│   ├── exec <id|name> [--cwd --timeout --detach] -- <command>
│   ├── ssh <id|name> [--user --identity] [-- <command>]
│   ├── cp <source> <destination> [-r]
│   ├── logs <id|name> [--follow --since]
│   ├── events <id|name> [--follow]
│   ├── process <id|name>
│   ├── attach <job-id>
│   ├── forward <id|name> --remote <port> --local <port>
│   ├── host <id|name> <port> [--public]
│   └── prompt <id|name> [--provider] <task>
├── snapshot
│   ├── list
│   ├── create <sandbox> <name>
│   ├── info <name>
│   ├── delete <name>
│   └── restore <name> [--name --size --environment]
├── environment
│   ├── list
│   ├── create <name>
│   ├── info <name>
│   ├── delete <name>
│   ├── add-repo <environment> <repo> [--branch]
│   ├── set-env <environment> KEY=VALUE
│   ├── set-secret <environment> KEY [--file]
│   ├── set-setup-script <environment> <file>
│   └── set-safe-for-third-parties <environment> <true|false>
├── image
│   ├── list
│   ├── info <id|name>
│   ├── import <oci-reference>
│   ├── build <directory>
│   └── delete <id|name>
├── template
│   ├── list
│   ├── create
│   ├── info <name>
│   ├── launch <name>
│   └── delete <name>
├── operation
│   ├── list
│   ├── info <id>
│   ├── wait <id>
│   └── cancel <id>
├── api-key
│   ├── create
│   ├── list
│   ├── rotate <id>
│   └── revoke <id>
├── ci
│   ├── run [--from --size --timeout] -- <command>
│   └── status <job-id>
├── system
│   ├── info
│   ├── limits
│   └── capabilities
├── admin
│   ├── users
│   ├── nodes
│   ├── quotas
│   └── audit
└── completion <bash|zsh|fish|powershell>
```

### Compatibility aliases

| Existing command | New canonical command |
|---|---|
| `compute create` | `compute sandbox create` |
| `compute list`, `compute ps` | `compute sandbox list` |
| `compute status`, `compute stats` | `compute system info` or `sandbox list` |
| `compute run`, `compute start` | `compute sandbox start` |
| `compute stop` | `compute sandbox stop` |
| `compute resume` | `compute sandbox resume` |
| `compute clone` | `compute sandbox fork` |
| `compute save-state`, `compute snapshot` | `compute snapshot create` |
| `compute ssh` | `compute sandbox ssh` |
| `compute exec` | `compute sandbox exec` |
| `compute delete`, `compute rm` | `compute sandbox delete` |
| `compute image` | `compute image` |
| `compute template` | `compute template` |
| `compute key` | `compute api-key` or `ssh-key` |
| `compute network` | `compute system` and future `network` resources |

Aliases must call the same command implementation, not duplicate business logic.
Print a deprecation warning only when stderr is a terminal.

## 3. Global CLI contract

The root command must support:

```text
--profile <name>
--endpoint <url>
--api-key <token>              # discouraged; prefer stdin or environment
--output table|json|jsonl
--json                         # shorthand for --output json
--quiet
--verbose
--timeout <duration>
--no-color
--no-wait
--idem <key>
```

Environment overrides:

```text
COMPUTE_PROFILE
COMPUTE_ENDPOINT
COMPUTE_API_KEY
COMPUTE_OUTPUT
COMPUTE_NO_COLOR
COMPUTE_NO_UPDATE
```

Rules:

- TTY output defaults to tables; redirected output defaults to JSON.
- Streaming commands use JSON Lines, one event per line.
- Progress, warnings, and diagnostics go to stderr.
- Command exit status must be deterministic and preserve guest command exit
  status for `sandbox exec`.
- Secrets never appear in logs, JSON responses, process arguments, or error
  messages.
- `current` is a local interactive alias only. Scripts must use an explicit ID.
- Every mutating request gets an idempotency key unless the caller supplies one.

Suggested local configuration:

```toml
default_profile = "local"

[profiles.local]
endpoint = "https://127.0.0.1:8443"
api_key = "mfk_..."
insecure_skip_verify = true
output = "table"
```

## 4. CLI code changes

### 4.1 Replace the hand-written dispatcher

Edit or replace:

- `cmd/compute/main.go`
- `cmd/compute/client.go`
- `cmd/compute/completion.go`

Add:

```text
cmd/compute/root.go
cmd/compute/commands/auth.go
cmd/compute/commands/sandbox.go
cmd/compute/commands/snapshot.go
cmd/compute/commands/environment.go
cmd/compute/commands/image.go
cmd/compute/commands/template.go
cmd/compute/commands/operation.go
cmd/compute/commands/apikey.go
cmd/compute/commands/ci.go
cmd/compute/commands/system.go
cmd/compute/commands/admin.go
cmd/compute/output.go
cmd/compute/config.go
cmd/compute/aliases.go
cmd/compute/resolve.go
```

Use Cobra or the repository's selected command framework consistently. Each
command should only parse flags, call `pkg/client`, and render the result.
Resource lifecycle, authentication, retries, and protocol handling belong below
the command layer.

### 4.2 Introduce a typed client SDK

`pkg/client` becomes the only client implementation used by the CLI and tests.
It should provide typed services:

```go
type Client struct {
    System       SystemService
    Sandboxes    SandboxService
    Snapshots    SnapshotService
    Environments EnvironmentService
    Images       ImageService
    Templates    TemplateService
    Operations   OperationService
    APIKeys      APIKeyService
    Events       EventService
}
```

The old `cmd/compute/DaemonClient` should become a compatibility adapter during
the migration and then be removed. Do not add new endpoint methods to it.

Required SDK behavior:

- Context-aware deadlines and cancellation.
- Typed request and response structs from `pkg/api`.
- Typed errors with stable error codes.
- Idempotency headers/fields for mutating operations.
- Automatic reconnect for subscriptions and streams.
- Safe concurrent calls.
- Binary stream access as `io.Reader`/`io.Writer`.
- Operation wait helpers with bounded server-side waits.

## 5. WebSocket protocol changes

The existing protocol in `pkg/api` and `internal/master/ws` is the correct
direction, but it needs to become complete enough for every CLI command.

### 5.1 Request and response envelope

Keep the existing fields and enforce validation:

```json
{
  "t": "req",
  "id": "req-123",
  "op": "sandboxes.create",
  "idem": "idem-123",
  "trace": "trace-123",
  "params": {}
}
```

Add or verify:

- Maximum ID length and non-empty IDs.
- Supported operation registry.
- Per-connection in-flight request limit.
- Request deadline propagation.
- Idempotency replay for mutations.
- No raw internal errors in responses.
- Consistent error code and details shape.

### 5.2 Operation registry

Add a registry in `pkg/api/methods.go` containing:

```go
type MethodSpec struct {
    Name       string
    Mutating   bool
    Auth       AuthScope
    Audit      bool
    RateClass  string
    Params     func() any
    Result     func() any
}
```

Every operation must have an explicit auth scope, validator, audit decision,
rate class, and typed result. `compute api schema` should serialize this registry
for automation and AI tool calling.

Recommended operation names:

```text
system.info
system.capabilities
me.get
auth.session

sandbox.list
sandbox.get
sandbox.create
sandbox.start
sandbox.stop
sandbox.resume
sandbox.restart
sandbox.delete
sandbox.fork
sandbox.exec
sandbox.process
sandbox.logs
sandbox.events
sandbox.files.upload
sandbox.files.download
sandbox.forward.create
sandbox.forward.delete
sandbox.host.create
sandbox.host.list
sandbox.host.delete
sandbox.prompt

snapshot.list
snapshot.get
snapshot.create
snapshot.delete
snapshot.restore

environment.list
environment.get
environment.create
environment.update
environment.delete
environment.repo.add
environment.secret.set
environment.secret.delete
environment.setup.set

image.list
image.get
image.import
image.build
image.delete

template.list
template.get
template.create
template.update
template.delete
template.launch

operation.list
operation.get
operation.wait
operation.cancel

events.subscribe
events.unsubscribe
apikey.list
apikey.create
apikey.rotate
apikey.revoke
audit.list
```

### 5.3 Events and subscriptions

Implement `events.subscribe` and `events.unsubscribe` in
`internal/master/ws`. Events must be authorized per user and include:

```text
sandbox.created
sandbox.updated
sandbox.deleted
sandbox.ready
sandbox.failed
sandbox.archived
operation.updated
snapshot.created
snapshot.ready
command.started
command.output
command.finished
environment.updated
image.imported
host.created
host.deleted
subscription.overflow
```

Each connection needs:

- Subscription IDs.
- Monotonic sequence numbers.
- Topic filters and optional resource filters.
- Bounded outbound queues.
- Overflow notification.
- Re-fetch-on-reconnect behavior.
- No event leakage across tenants.

### 5.4 Binary streams

Complete `internal/master/ws/frame.go` and the currently stubbed binary path in
`internal/master/ws/server.go`.

Use the existing frame layout:

```text
sid: uint32 big-endian
channel: uint8
payload: bytes
```

Channels:

```text
0 stdin
1 stdout
2 stderr
3 file data
4 agent/event data
```

Implement stream lifecycle actions:

```text
open
credit
resize
close
exit
```

Every stream needs a 256 KiB flow-control window, output limits, cancellation,
and an exit result containing exit code, signal, duration, and truncation status.

## 6. WebSocket server and daemon changes

### 6.1 Files to edit or add

Primary surfaces:

- `internal/master/ws/server.go`
- `internal/master/ws/frame.go`
- `internal/master/ws/client.go` (new connection state and stream manager)
- `internal/master/ws/subscriptions.go`
- `internal/master/ws/dispatch.go`
- `internal/master/ws/operations.go`
- `internal/master/ws/authorization.go`
- `internal/master/ws/audit.go`
- `cmd/microforged/server.go`
- `cmd/microforged/commands.go`
- `pkg/api/*.go`
- `internal/master/auth/*`
- `internal/master/audit/*`
- `internal/store/*`

Register resource handlers from service packages instead of putting VM logic
directly in the WebSocket transport.

Recommended layering:

```text
WebSocket transport
  -> method registry / validation
  -> authorization / quota / audit
  -> application service
  -> operation manager
  -> VM, storage, network, image and guest-agent services
```

### 6.2 Authentication and authorization

The WebSocket handshake must support:

- `Authorization: Bearer mfk_...` for CLI and CI.
- Session cookie plus strict Origin checking for the Web UI.
- Credential revocation while a socket is open.
- Per-key action scopes.
- Per-user ownership filtering.
- Admin-only node, user, quota, and audit methods.

API-key creation must return the secret only once. The CLI must support
`--key-stdin`-style login so secrets do not appear in process listings.

### 6.3 Async operation manager

Add an operation manager backed by the store:

```text
operations
idempotency_keys
command_jobs
event_outbox
```

Long-running calls return an operation ID. The operation manager must persist
state across daemon restarts and publish updates over WebSocket.

Required states:

```text
pending
running
succeeded
failed
cancelled
expired
```

The VM manager must stop holding global locks across disk copies, guest
shutdown, image import, or snapshot work. Use per-resource locks and durable
operations.

### 6.4 Guest agent integration

Use the planned `cmd/compute-agent` and vsock path for:

- `sandbox exec`
- PTY SSH-like sessions
- File upload/download
- Process listing
- Detached command attach
- Shutdown/reboot
- Readiness and health

The daemon should not accept arbitrary guest usernames or expose host SSH
paths. Validate command size, environment size, working directory, timeout,
output bytes, and signal behavior.

## 7. Data model changes

Extend the store migrations with:

```text
environments
environment_repositories
environment_variables
environment_secrets
environment_setup_scripts
environment_versions
snapshots
snapshot_versions
operations
idempotency_keys
command_jobs
command_output
port_forwards
public_hosts
api_key_scopes
audit_events
```

Sandbox/instance records need:

```text
name
state
size
environment_id
snapshot_id
auto_stop_at
setup_status
setup_error
labels
created_by
updated_at
```

Never return host disk paths, Firecracker socket paths, PIDs, private keys, or
internal network implementation details to CLI users.

## 8. Feature implementation order

### Phase A — CLI foundation

- Introduce command tree and compatibility aliases.
- Add profiles and output renderer.
- Finish typed `pkg/client`.
- Add shell completion.
- Add protocol error-to-exit-code mapping.

Acceptance:

```bash
compute --json sandbox list
compute sandbox info <id>
compute completion bash
```

### Phase B — Lifecycle parity

- Create, list, info, start, stop, resume, restart, delete, fork.
- Durable operations and idempotency.
- Named snapshots and restore.
- `current` alias for interactive use.

Acceptance: repeated create/stop/resume/fork requests do not create duplicate
resources when retried with the same idempotency key.

### Phase C — Remote work

- Streaming exec.
- PTY shell.
- Logs and events.
- Detached commands and attach.
- File copy.
- Guest-agent readiness and health.

Acceptance: large output is flow-controlled, cancellation works, and guest
exit codes reach the local process unchanged.

### Phase D — Environments and hosting

- Repository checkout.
- Setup scripts and status.
- Secret references and safe-for-third-parties.
- Port forwarding.
- Token-gated public hosts.

Acceptance: a new sandbox can be created from an environment and snapshot
without exposing the creator's credentials.

### Phase E — CI and agents

- `compute ci run`.
- Scoped CI keys.
- Guaranteed cleanup.
- Artifact and log collection.
- `sandbox prompt` provider interface and event stream.

Acceptance: a failed guest command fails the CI job, while cleanup still runs.

### Phase F — Administration and hardening

- API-key rotation/revocation.
- User, quota, node, and audit commands.
- Rate limiting, quotas, audit verification.
- Reconnect and chaos tests.

## 9. Testing plan

Add tests at each layer:

```text
cmd/compute/*_test.go
pkg/client/*_test.go
internal/master/ws/*_test.go
internal/master/auth/*_test.go
internal/master/audit/*_test.go
internal/store/*_test.go
test/e2e/cli_lifecycle_test.go
test/e2e/cli_exec_stream_test.go
test/e2e/cli_snapshot_test.go
test/e2e/cli_environment_test.go
test/e2e/cli_ci_test.go
```

Required cases:

- Human table, JSON, and JSON Lines output.
- Pipe detection and no ANSI output in machine mode.
- Invalid arguments and stable exit statuses.
- API-key authentication and revocation.
- Tenant isolation and admin authorization.
- Request replay with idempotency keys.
- Reconnect during operation and event streaming.
- Stream flow control and output truncation.
- SIGINT cancellation and guest signal forwarding.
- Snapshot consistency and restore failures.
- Setup script timeout and failure reporting.
- File path traversal prevention.
- Public host authorization and token expiry.
- Cleanup after success, failure, timeout, and client disconnect.
- Daemon restart with pending operations.

Update the existing unit and feature scripts only after the new command
behavior is covered by Go end-to-end tests. Run `make test`, then the focused
CLI/WebSocket tests, then the full integration suite.

## 10. Documentation and migration

Add:

```text
docs/cli/quickstart.md
docs/cli/reference.md
docs/cli/ci.md
docs/cli/agent-automation.md
docs/cli/migration-from-flat-commands.md
```

The plan documents must include copy-paste examples for local use, remote
profiles, CI, JSON parsing, snapshots, environments, and port forwarding.

Migration requirements:

1. Keep flat commands for one release.
2. Print deprecation warnings only for TTY users.
3. Keep output stable for existing scripts where possible.
4. Provide `compute migrate-config` for old endpoint settings.
5. Do not migrate insecure plaintext credentials automatically.
6. Document that old direct SSH behavior is replaced by the guest agent where
   the hardened runtime is enabled.

## 11. Definition of done

The Boat-like CLI work is complete when:

- All P0 commands work against the local and remote daemon.
- CLI and Web UI share the typed WebSocket protocol.
- `--json` and JSON Lines output are stable and documented.
- Mutating operations are authenticated, authorized, audited, quota checked,
  idempotent, and recoverable after daemon restart.
- Exec, PTY, logs, events, and file transfer use bounded binary streams.
- Snapshot, environment, and sandbox semantics are documented and tested.
- CI can create, execute, collect results, and clean up an ephemeral sandbox.
- Existing flat commands remain functional through the compatibility period.
- Unit, integration, security, and end-to-end tests pass.
