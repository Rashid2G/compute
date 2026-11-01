# 6. Worker Protocol and Public API

## Protocol requirements

The existing `compute.v1` WebSocket protocol can be extended, but worker
connections need a distinct authenticated role and message namespace.

Every message must contain:

- Protocol version.
- Message type.
- Message ID.
- Worker or job ID where relevant.
- Monotonic sequence number for streams.
- Trace ID.
- Optional idempotency key.

Messages must have:

- Maximum size.
- Schema validation.
- Unknown-field policy.
- Explicit acknowledgement.
- Retry behavior.

## Worker stream messages

```text
worker.hello
worker.hello_ack
worker.heartbeat
worker.heartbeat_ack
worker.capabilities
worker.assignment
worker.assignment_ack
worker.assignment_reject
worker.workload_event
worker.log_chunk
worker.artifact_begin
worker.artifact_chunk
worker.artifact_end
worker.cancel
worker.cancel_ack
worker.drain
worker.reconcile
worker.reconcile_response
worker.error
```

Example assignment:

```json
{
  "type": "worker.assignment",
  "id": "msg_01",
  "seq": 44,
  "trace_id": "trc_01",
  "lease_id": "lea_01",
  "attempt_id": "att_01",
  "job_id": "job_01",
  "spec": {
    "image_id": "img_01",
    "architecture": "amd64",
    "vcpus": 2,
    "memory_mb": 4096,
    "disk_mb": 20480,
    "command": ["bash", "-lc", "npm test"],
    "cwd": "/workspace",
    "timeout_seconds": 3600,
    "network_policy": "egress"
  }
}
```

Never send plaintext secret values in control messages. Send a short-lived
secret delivery reference that the worker exchanges only after assignment
acceptance, or use an encrypted payload whose key is scoped to the workload.

## Event semantics

Events must be append-only and replayable:

```text
job.queued
job.scheduled
job.accepted
job.provisioning
job.ready
job.started
job.output
job.artifact
job.canceled
job.timed_out
job.succeeded
job.failed
job.lost
job.cleaned
```

Clients can request events after a sequence number. The server must return a
gap error if retention no longer covers the requested sequence.

## Public REST API

### Workers

```text
POST   /api/v1/worker-tokens
GET    /api/v1/workers
GET    /api/v1/workers/{id}
POST   /api/v1/workers/{id}/cordon
POST   /api/v1/workers/{id}/drain
POST   /api/v1/workers/{id}/rotate-certificate
POST   /api/v1/workers/{id}/reconcile
DELETE /api/v1/workers/{id}
```

### Pools

```text
GET    /api/v1/pools
POST   /api/v1/pools
GET    /api/v1/pools/{id}
PATCH  /api/v1/pools/{id}
DELETE /api/v1/pools/{id}
```

### Jobs

```text
POST   /api/v1/jobs
GET    /api/v1/jobs
GET    /api/v1/jobs/{id}
POST   /api/v1/jobs/{id}/cancel
POST   /api/v1/jobs/{id}/retry
GET    /api/v1/jobs/{id}/events
GET    /api/v1/jobs/{id}/logs
GET    /api/v1/jobs/{id}/artifacts
```

### CI

```text
POST   /api/v1/ci/github/connect
GET    /api/v1/ci/github/repos
GET    /api/v1/ci/jobs
GET    /api/v1/ci/jobs/{id}
GET    /api/v1/ci/jobs/{id}/logs
```

## API rules

- Require `Authorization: Bearer`.
- Require `Idempotency-Key` on create/cancel/retry operations.
- Return stable machine-readable error codes.
- Include `trace_id` in every response.
- Enforce maximum request body and output sizes.
- Use `Retry-After` for retryable 429/503 responses.
- Use `409` for state conflicts.
- Never return secret values after the creation response.

## SDK and CLI contract

The typed `pkg/client` must be the only client implementation used by the CLI.
The CLI must support table, JSON, and JSONL output. Streaming output goes to
JSONL when stdout is not interactive.
