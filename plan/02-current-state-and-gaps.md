# 2. Current State and Gaps

## Reusable repository capabilities

The repository already contains these foundations:

| Area | Evidence | Status |
|---|---|---|
| Firecracker runtime | `internal/node/runtime/firecracker`, `internal/firecracker` | Existing |
| VM lifecycle | `internal/vm/manager.go` | Existing; needs remote orchestration |
| Guest command agent | `cmd/compute-agent/main.go` | Existing; this is the guest agent |
| REST daemon | `cmd/computed/server.go` | Existing; needs control-plane APIs |
| WebSocket RPC | `internal/master/ws`, `pkg/api/types.go` | Existing; needs worker protocol |
| Auth and API keys | `internal/master/auth`, `api_keys` table | Existing; needs scopes and worker identity |
| PKI | `internal/master/pki` | Existing; needs worker certificate lifecycle |
| Nodes table | `nodes` in `0001_init.sql` | Partially prepared |
| Join tokens | `node_join_tokens` in `0001_init.sql` | Partially prepared |
| Images/templates | `images`, `templates`, image packages | Existing; needs distribution and cache protocol |
| Persistent state | SQLite store and migrations | Existing; needs job/lease tables |
| Async operations | `operations` table | Existing; needs attempts and reconciliation |
| Audit logging | `internal/master/audit` | Existing; expand to worker/job actions |
| CLI | `cmd/compute` | Existing; extend with worker/job/CI commands |
| Deployment | `scripts/deploy-t3-runner.sh` | Existing for the local deployment target |

## What is missing

### Host worker

Build a separate `compute-worker` binary. The existing `compute-agent` runs
inside a VM and must remain a guest component.

Missing behavior:

- Host configuration and service installation.
- Enrollment and certificate generation.
- Outbound control stream.
- Capability discovery.
- Local capacity accounting.
- Image cache.
- Workload supervisor.
- Worker drain and shutdown.
- Local security hardening.
- Upgrade and certificate rotation.

### Control-plane worker management

Missing behavior:

- Enrollment-token API.
- Worker registration.
- Heartbeat processing.
- Worker state transitions.
- Worker revocation.
- Capacity reconciliation.
- Worker pools and labels.
- Worker event persistence.

### Scheduler

Missing behavior:

- Job queue.
- Capability matching.
- Atomic resource reservations.
- Leases and expiry.
- Assignment acknowledgement.
- Fairness and quotas.
- Requeue/lost-workload policy.
- Scheduler reconciliation after restart.

### Workload jobs

Missing behavior:

- Job request/response types.
- Job state machine.
- Attempt tracking.
- Command and artifact contracts.
- Live logs and event replay.
- Cancellation.
- Timeout enforcement.
- Cleanup guarantees.
- Retry policy.

### Security

Missing behavior:

- Scoped organization authorization for workers.
- Short-lived enrollment tokens with one-use enforcement.
- Worker mTLS authentication.
- Certificate revocation and rotation.
- Per-job secret injection.
- Host-side resource and capability enforcement.
- Public repository CI policy.
- Security audit events.

### CI integration

Missing behavior:

- GitHub App or webhook receiver.
- Workflow job validation.
- Ephemeral runner registration.
- Runner token cleanup.
- Job status mapping.
- Repository allowlists.
- Debug retention policy.

### User surfaces

Missing behavior:

- `compute worker` commands.
- `compute job` commands.
- `compute pool` commands.
- CI setup and status commands.
- Worker and queue GUI views.
- Live job logs and artifacts.
- Clear capacity and failure explanations.

### Operations

Missing behavior:

- Worker/job metrics.
- Alerts and health checks.
- Image distribution strategy.
- Object storage for large artifacts.
- Control-plane backup and restore.
- Version compatibility policy.
- Upgrade/rollback mechanism.

## Important implementation constraint

The current daemon appears primarily designed to manage one local host. The
remote-worker feature must introduce a control-plane layer without making the
existing local mode unusable. Treat the local host as a worker with a special
`local` role and route it through the same scheduler interfaces where practical.
