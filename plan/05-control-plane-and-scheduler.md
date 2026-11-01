# 5. Control Plane and Scheduler

## Worker registry

Implement a worker service that can:

- Create enrollment tokens.
- Accept enrollment CSRs.
- Register worker capabilities.
- Record heartbeats.
- Mark workers offline after missed heartbeats.
- Cordon, drain, revoke, and delete workers.
- List active workloads and reconcile capacity.
- Track agent and protocol versions.

The existing `nodes` table can be extended rather than replaced. `role=remote`
represents installed workers; `role=local` represents the local host.

## Enrollment protocol

1. Authenticated operator requests a token with pool, labels, and expiry.
2. API returns the token once.
3. Worker generates an Ed25519 or ECDSA private key.
4. Worker sends token, public key, host metadata, and CSR.
5. Control plane atomically consumes the token.
6. Control plane creates the node and signs a short-lived certificate.
7. Worker stores credentials and opens the control stream.
8. Control plane moves node to `ready` after capability validation.

The token must be one-use, hashed at rest, organization-scoped, and invalid
after expiry or revocation.

## Queue

Queue records must include:

- Job ID.
- Organization ID.
- Priority.
- Required capabilities.
- Requested resources.
- Enqueue time.
- Deadline.
- Retry policy.
- Idempotency key.
- Current attempt.

The queue must have deterministic ordering within the same priority and fair
organization selection so one tenant cannot starve others.

## Scheduling algorithm

1. Select queued jobs whose deadline has not expired.
2. Filter workers by organization/pool and worker state.
3. Filter by architecture, image, labels, and policy.
4. Filter by available resource reservation.
5. Prefer image-cache hits.
6. Prefer least-loaded compatible workers.
7. Atomically reserve worker capacity.
8. Create a lease and attempt.
9. Send assignment.
10. Wait for acceptance.
11. Release and requeue on rejection or timeout.

Do not schedule to `offline`, `cordoned`, `draining`, or `revoked` workers.

## Lease protocol

Lease fields:

- Lease ID.
- Job ID.
- Attempt ID.
- Worker ID.
- Created time.
- Expires time.
- Last renewal.
- Assignment state.

Recommended timings:

- Assignment acceptance timeout: 15 seconds.
- Lease duration: 60 seconds.
- Renewal interval: 20 seconds.
- Worker offline threshold: three missed heartbeats.
- Reconciliation grace period: configurable, default 2 minutes.

The lease holder must be the only worker allowed to report terminal state for
that attempt. Late messages must be recorded and ignored for state mutation.

## Reconciliation

On control-plane startup and periodically:

- Find expired leases.
- Query connected workers for active workload IDs.
- Mark unknown local workloads as orphaned.
- Reconcile reservations against worker reports.
- Requeue only retryable jobs.
- Mark non-retryable jobs as lost with an explicit reason.
- Clean stale temporary resources.

## Quotas

Apply limits at organization, user, pool, worker, and job levels:

- Concurrent VMs.
- Concurrent jobs.
- vCPUs.
- Memory.
- Disk.
- Daily runtime.
- Image download bandwidth.
- Artifact bytes.
- Log bytes.
- Job starts per minute/hour/day.

Return stable errors such as `quota_exceeded`, `no_compatible_worker`,
`worker_offline`, `capacity_reserved`, and `job_deadline_exceeded`.

## Local daemon compatibility

Treat the current local VM manager as an implementation of a worker backend.
First introduce an interface such as:

```go
type WorkerRuntime interface {
    Capabilities(ctx context.Context) (Capabilities, error)
    Start(ctx context.Context, spec WorkloadSpec) (WorkloadHandle, error)
    Stop(ctx context.Context, id string, force bool) error
    Exec(ctx context.Context, id string, req ExecRequest) (ExecResult, error)
    Destroy(ctx context.Context, id string) error
}
```

The local runtime and remote worker runtime should implement the same workload
contract.
