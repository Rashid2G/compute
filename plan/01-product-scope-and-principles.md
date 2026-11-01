# 1. Product Scope and Principles

## Product statement

Compute will provide a control plane for compute contributed by users. A
user installs a small host agent, enrolls it into an organization, and makes
some or all of that machine's capacity available as a worker pool. Compute
then schedules isolated Firecracker workloads onto it.

The product must support:

- Ephemeral build and test jobs.
- Persistent development or service VMs.
- GitHub Actions and later other CI systems.
- Batch workloads and arbitrary commands inside a VM.
- AI-agent sessions and long-running processes.
- Multiple workers, pools, architectures, and labels.
- Hosts behind NAT or corporate firewalls.

## Non-goals for the first production release

Do not initially build:

- Live migration of running VMs.
- Cross-worker shared writable disks.
- GPU scheduling.
- Windows workers.
- Public preview URLs.
- Billing or marketplace functionality.
- Arbitrary host command execution.
- Automatic execution of untrusted workloads on a worker without explicit
  opt-in.
- A general Kubernetes replacement.

## Design principles

### Outbound-only connectivity

Workers initiate all connections. The control plane must never require
port-forwarding into a user's machine.

### Strong workload isolation

The worker host is more trusted than a guest VM. Workloads receive no host
filesystem, Docker socket, host network namespace, or privileged device unless
an explicit future capability grants it.

### Explicit ownership

Every worker, workload, image, artifact, secret, and event belongs to an
organization or user. Authorization must be checked at every API boundary.

### Durable state machines

Worker, job, VM, operation, and lease states are persisted. Reconnects,
restarts, retries, and partial failures must be recoverable.

### Safe defaults

- Short-lived enrollment tokens.
- Expiring scoped API keys.
- No secret inheritance from worker host to guest.
- Ephemeral jobs clean up by default.
- Public-repository CI disabled by default.
- Auto-stop enabled unless explicitly disabled.
- Resource ceilings enforced locally and centrally.

### Useful before feature-complete

The first useful product is a reliable ephemeral job runner. Persistent VMs,
snapshots, public URLs, integrated agents, billing, and SDKs follow only after
the job lifecycle is correct.

## Product vocabulary

| Term | Definition |
|---|---|
| Control plane | API, scheduler, database, image catalog, event service |
| Worker | Host process that launches Firecracker VMs |
| Worker pool | Workers sharing scheduling labels and policy |
| Workload | Any requested compute unit |
| Job | Finite workload with input and result |
| Instance | A VM with a persisted lifecycle |
| Lease | Time-limited assignment held by one worker |
| Attempt | One execution try for a job |
| Template | Versioned immutable image plus startup configuration |
| Artifact | Output file, log, report, or metadata produced by a workload |
