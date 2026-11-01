# 11. Operations, Observability, and Upgrades

## Metrics

Control plane:

- API request count, latency, and error rate.
- Queue depth by organization/pool/priority.
- Scheduling latency.
- Assignment acceptance latency.
- Lease expiry count.
- Job success/failure/lost/cleanup-failed counts.
- Event and log ingestion rate.

Worker:

- Connection state and reconnect count.
- Heartbeat age.
- CPU, memory, disk, and network usage.
- VM boot latency.
- Image cache hit/miss.
- Active workload count.
- VM launch failures.
- Guest-agent errors.
- Artifact upload failures.

## Logs and tracing

Every request, worker message, job attempt, and cleanup operation carries a
trace ID. Logs must include resource IDs but not secrets.

Provide:

- Structured JSON logs.
- Correlation from API request to job attempt to worker process.
- Worker-side local log rotation.
- Bounded job logs.
- Event sequence numbers for replay.

## Health endpoints

Control plane:

```text
/api/v1/health
/api/v1/ready
/api/v1/metrics
```

Worker diagnostics:

```text
compute-worker status
compute-worker diagnose
compute-worker capabilities
```

Readiness must fail when the database, certificate, or required Firecracker
capability is unavailable.

## Backups and retention

Back up:

- Database.
- Image metadata.
- Template metadata.
- Certificate revocation state.
- Job metadata and audit logs.

Store large artifacts and images in object storage when SQLite becomes
operationally unsuitable.

Define retention separately for:

- Job metadata.
- Logs.
- Artifacts.
- Audit events.
- Stopped VM disks.
- Saved templates.

## Version compatibility

Worker reports agent version and protocol version. Control plane maintains:

- Minimum supported worker version.
- Maximum supported protocol version.
- Upgrade-required status.
- Feature flags by protocol capability.

Protocol changes must be additive where possible. Negotiate features during
`worker.hello`.

## Upgrade process

1. Publish signed worker binary and checksum.
2. Mark compatible versions in the control plane.
3. Cordon workers requiring upgrade.
4. Drain active workloads.
5. Upgrade and restart service.
6. Verify capability report and health.
7. Uncordon only after validation.

Provide rollback to the previous binary and preserve the worker certificate.

## Incident handling

Document procedures for:

- Compromised worker certificate.
- Leaked API key.
- Worker host compromise.
- VM cleanup failure.
- Control-plane database corruption.
- Image integrity mismatch.
- Stuck lease.
- Runaway job.
- Public CI abuse.

Every incident procedure must include immediate containment, evidence
preservation, recovery, and credential rotation.
