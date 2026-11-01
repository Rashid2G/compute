# 13. Implementation Sequence

## Milestone 0: contracts and safety

Deliver:

- Architecture decision records.
- Threat model.
- Worker/job state diagrams.
- Protocol schemas.
- Error code catalog.
- Database migration design.
- Test strategy.

Dependencies: none.

Exit gate: protocol, authorization model, and cleanup semantics are approved.

## Milestone 1: worker foundation

Build:

- `cmd/compute-worker`.
- Worker config.
- Capability discovery.
- Local admission controller.
- Systemd installer/unit.
- Basic worker diagnostics.

Reuse:

- Firecracker runtime.
- Existing node/network/storage packages.

Exit gate: worker runs locally, reports capabilities, and refuses over-capacity
requests.

## Milestone 2: enrollment and worker registry

Build:

- Enrollment token API.
- CSR and certificate signing.
- Worker mTLS stream.
- Heartbeat persistence.
- Worker list/info/cordon/drain/revoke.
- Certificate rotation.

Exit gate: worker can enroll, reconnect, be cordoned, drained, and revoked.

## Milestone 3: job queue and scheduler

Build:

- Jobs, attempts, leases, reservations, and events migrations.
- Capability matcher.
- FIFO/fair queue.
- Assignment acknowledgement.
- Lease renewals and expiry.
- Startup reconciliation.

Exit gate: one job is assigned to exactly one compatible worker and recovers
from disconnects without false success.

## Milestone 4: ephemeral VM jobs

Build:

- Image cache protocol.
- VM preparation and readiness.
- Guest command execution.
- Log streaming.
- Cancellation and timeout.
- Artifact upload.
- Cleanup controller.

Exit gate: complete submit/run/observe/cancel/cleanup workflow works on a clean
worker.

## Milestone 5: CLI and GUI

Build:

- Worker CLI.
- Pool CLI.
- Job CLI.
- JSON/JSONL output.
- Worker dashboard.
- Queue and job detail views.
- Live logs and artifact download.

Exit gate: a user can operate the full workflow without database or SSH access.

## Milestone 6: CI integration

Build:

- GitHub App/webhook.
- Repository policy.
- Ephemeral runner lifecycle.
- GitHub status/log mapping.
- CI CLI and GUI.

Exit gate: a sample GitHub workflow executes on a fresh VM and always cleans up.

## Milestone 7: production hardening

Build:

- Metrics/tracing/alerts.
- Backup/restore.
- Signed worker upgrades.
- Object storage.
- PostgreSQL-compatible repositories.
- Rate limiting and abuse controls.
- Security and failure-injection test suites.

Exit gate: documented runbooks exist and failures are observable and
recoverable.

## Explicitly defer

- Persistent VM migration.
- Replicated volumes.
- Public URLs.
- GPU support.
- Hosted billing.
- SDK generation.
- Integrated agent conversations.
- Multi-region scheduling.

## Work breakdown by repository area

### `cmd/`

Add worker binary and CLI commands. Keep guest `compute-agent` separate.

### `internal/`

Add worker, scheduler, job, lease, image-cache, artifact, and reconciliation
packages. Extend auth, PKI, store, audit, and WebSocket packages.

### `pkg/api`

Add typed worker, pool, job, lease, event, artifact, and error contracts.

### `pkg/client`

Add typed services and streaming/reconnect support.

### `internal/store`

Add migrations, models, queries, indexes, and transaction helpers.

### `web/`

Add workers, queue, jobs, CI, logs, artifacts, and policy views.

### `scripts/`

Add worker install, service, upgrade, diagnostics, and local integration
fixtures.

### Tests

Add unit, protocol, integration, security, failure-injection, and end-to-end
tests before enabling untrusted workloads.
