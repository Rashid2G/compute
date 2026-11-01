# 9. Workloads and CI

## Job types

### Command job

Input:

- Image/template.
- Resource requirements.
- Command and arguments.
- Working directory.
- Environment references.
- Secret references.
- Network policy.
- Timeout.
- Retry policy.
- Artifact declarations.

Output:

- State.
- Exit code or signal.
- Start/finish timestamps.
- Worker and attempt IDs.
- Bounded stdout/stderr.
- Artifact metadata.
- Failure code and message.

### Persistent instance

Input:

- Name.
- Image/template.
- Size.
- Disk and volume configuration.
- Network configuration.
- Restart and idle policy.

Output:

- Stable instance ID.
- Current state.
- Assigned worker.
- Guest address and connection metadata.
- Operation IDs for transitions.

### Long-running service

Add:

- Detached process ID.
- Restart policy.
- Health check.
- Explicit expiry or no-expiry setting.
- Log retention.
- Optional future port exposure.

## Cleanup contract

Every ephemeral job must have a cleanup controller that runs independently of
the client request. Cleanup must:

1. Cancel guest command.
2. Stop the VM.
3. Remove tap/network resources.
4. Remove temporary disks.
5. Remove secret files.
6. Upload final logs if possible.
7. Release worker reservations.
8. Mark the attempt cleaned or cleanup-failed.

Cleanup failure must be visible and retryable; never report successful cleanup
when resources remain.

## GitHub Actions integration

Recommended first approach: GitHub App plus webhook receiver.

Flow:

1. Organization installs the Compute GitHub App.
2. App receives workflow job events.
3. Compute validates installation, repository, actor, labels, and policy.
4. Compute queues a job.
5. Worker launches a clean VM.
6. Compute obtains a one-time runner registration token.
7. VM installs or starts the runner.
8. Runner executes the job.
9. Logs and status stream to Compute/GitHub.
10. Runner is removed.
11. VM and secrets are destroyed.

Support labels:

```text
self-hosted,compute
self-hosted,compute,linux-amd64
self-hosted,compute,large
self-hosted,compute,docker
```

Reject unknown labels rather than silently selecting a different resource size.

## CI controls

- Per-repository enable/disable.
- Public repository policy.
- Allowed actors and outside collaborators.
- Maximum runtime.
- Maximum parallel jobs.
- Debug retention duration.
- Template selection.
- Secret policy.
- Artifact retention.
- Retry on worker loss only, not on arbitrary test failure.

## Agent workloads

Start with generic jobs and event streams. Add an agent abstraction only when
the job primitive is stable:

- Conversation ID.
- Prompt and steer operations.
- Interrupt.
- Structured tool events.
- Session persistence.
- Provider/model metadata.
- Credential proxy.

This avoids coupling Compute to one vendor or harness.
