# 3. Target Architecture

## Components

```text
CLI / GUI / CI integration
          |
          v
  microforged control plane
  ├─ authentication and authorization
  ├─ worker registry
  ├─ scheduler and queue
  ├─ image/template catalog
  ├─ job/VM lifecycle API
  ├─ event and log service
  └─ SQLite initially, PostgreSQL later
          |
          | outbound mTLS streams
          v
  compute-worker on customer hosts
  ├─ capability discovery
  ├─ local admission control
  ├─ image cache
  ├─ workload supervisor
  ├─ Firecracker manager
  ├─ guest-agent bridge
  └─ metrics/log uploader
          |
          v
  Firecracker microVMs
  └─ compute-agent over vsock
```

## Process boundaries

### Control plane

Responsible for desired state, scheduling, authorization, and durable history.
It must not assume a worker is available merely because the database says it is
healthy.

### Worker

Responsible for actual host enforcement and VM execution. It must reject
assignments that exceed local limits even if the control plane sends an invalid
request.

### Guest

Responsible for command execution and guest-local reporting. It must not be
trusted with worker credentials or control-plane administration.

## Deployment modes

### Single-host mode

`microforged` and `compute-worker` run on the same machine. This preserves the
current local deployment path while exercising the remote protocol.

### Private control plane

The control plane runs on an operator server. Workers connect outbound from
developer, lab, office, or edge machines.

### Hosted control plane

Future deployment where many organizations use a managed control plane. This
requires PostgreSQL, object storage, rate limiting, tenant isolation, and
billing, but the worker protocol should remain the same.

## Data flow: ephemeral job

1. Client authenticates and submits a job with an idempotency key.
2. Control plane validates organization quota and job requirements.
3. Scheduler selects a compatible worker.
4. Control plane creates an attempt and worker lease.
5. Worker accepts the assignment and reserves local resources.
6. Worker ensures the image is present and verified.
7. Worker creates a copy-on-write disk and launches Firecracker.
8. Worker waits for guest readiness.
9. Worker injects non-secret environment and scoped secrets.
10. Worker executes the command through the guest agent.
11. Worker streams events and bounded logs.
12. Worker uploads artifacts.
13. Worker stops and deletes the VM.
14. Control plane records final status and releases the reservation.

## Failure data flow

Every step must produce a durable state transition. If the worker disconnects,
the lease expires only after a grace period. The scheduler then marks the
attempt lost or retries it according to the job's explicit retry policy.

Never silently convert a lost worker into a successful job.
