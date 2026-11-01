# 12. Testing and Acceptance

## Unit tests

Add tests for:

- Enrollment token hashing, expiry, one-use, and organization binding.
- Certificate validation and revocation.
- Worker state transitions.
- Capability matching.
- Resource reservation and release.
- Queue fairness.
- Lease creation, renewal, expiry, and duplicate rejection.
- Job state transitions.
- Idempotent create/cancel/retry behavior.
- Secret authorization and redaction.
- Log/output limits.
- Artifact checksums and size limits.
- CI repository/actor policy.

## Protocol tests

Test:

- Malformed messages.
- Unknown message types.
- Maximum frame sizes.
- Sequence gaps.
- Duplicate messages.
- Replay attempts.
- Out-of-order acknowledgements.
- Stream reconnect and resume.
- Heartbeat timeout.
- Certificate expiry.
- Control-plane backpressure.

## Worker integration tests

On a Linux host capable of running Firecracker:

1. Install worker.
2. Enroll it.
3. Connect it to a test control plane.
4. Submit a job.
5. Verify VM creation.
6. Verify command and exit status.
7. Stream logs.
8. Upload an artifact.
9. Cancel a running job.
10. Kill worker and verify lease expiry.
11. Restart worker and reconcile.
12. Verify cleanup and reservation release.

## Security tests

Verify that:

- Guest cannot read worker private keys.
- Guest cannot access host filesystem.
- Guest cannot access host Docker socket.
- Guest cannot access another VM's disk.
- Guest cannot use another job's secrets.
- Worker cannot act for another organization.
- Revoked workers cannot receive assignments.
- Public repository policy is enforced.
- Secrets are absent from logs, snapshots, and artifacts.
- Resource limits cannot be bypassed through API fields.

Use a Firecracker escape test plan and keep it separate from ordinary unit
tests. A successful test suite is not a substitute for kernel/runtime review.

## Failure-injection tests

Inject:

- Database restart during assignment.
- Worker disconnect before acceptance.
- Worker disconnect during execution.
- Control-plane restart during execution.
- Image download interruption.
- Guest boot timeout.
- Guest agent crash.
- Artifact upload failure.
- Duplicate terminal events.
- Clock skew within configured bounds.
- Full disk and full log quota.

## Acceptance criteria

The MVP is accepted when:

- A clean supported Linux machine can enroll using a one-time token.
- No inbound worker port is required.
- Worker mTLS reconnects after control-plane restart.
- Scheduler never exceeds worker resource limits.
- A job cannot be assigned to two workers simultaneously.
- Job logs are live, bounded, and replayable.
- Cancellation reaches the guest and cleanup completes.
- Worker loss is explicit and retry behavior follows policy.
- Artifacts have verified checksums.
- Revocation stops new assignments.
- Secrets are unavailable after cleanup.
- Existing local VM operations continue to work.
- `make test` and targeted integration tests pass.

## Performance targets

Initial targets for a single control plane:

- 100 connected workers.
- 1,000 active workloads.
- 100 job submissions per second sustained.
- Scheduler decision under 250 ms at 100 workers.
- Heartbeat processing without blocking job APIs.
- Log backpressure without unbounded memory growth.

These are engineering targets, not a promise of hosted scale until measured.
