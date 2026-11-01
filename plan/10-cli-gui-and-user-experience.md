# 10. CLI, GUI, and User Experience

## Installation journey

The happy path should be:

```bash
compute login
compute worker token create --pool default --ttl 30m
# copy the generated one-time command to another Linux machine
curl -fsSL https://get.compute.dev/worker | sh
compute-worker enroll --token ...
sudo systemctl enable --now compute-worker
compute worker list
```

The enrollment output must state:

- Worker ID.
- Organization.
- Pool.
- Connected/disconnected state.
- Host labels.
- Capacity.
- Certificate expiry.
- How to revoke the worker.

## CLI commands

Add:

```text
compute worker token create
compute worker token list
compute worker list
compute worker info <id>
compute worker cordon <id>
compute worker drain <id>
compute worker revoke <id>
compute worker reconcile <id>

compute pool list
compute pool create
compute pool update
compute pool delete

compute job run --image ... -- <command>
compute job list
compute job info <id>
compute job logs <id> [--follow]
compute job events <id> [--follow]
compute job cancel <id>
compute job retry <id>
compute job artifact <id> <name>

compute ci connect github
compute ci repos
compute ci jobs
compute ci job <id>
compute ci logs <id> [--follow]
```

The existing CLI plan in `15-cli-implementation-plan.md` defines global output,
profile, timeout, idempotency, and compatibility rules. Apply those rules to
worker and job commands.

## Output behavior

- Interactive terminal: readable tables and progress.
- Redirected stdout: JSON or JSONL.
- Logs: stdout for guest output, stderr for CLI diagnostics.
- Exit code: guest exit code for `job run` where available.
- Never print tokens, private keys, secret values, or signed URLs.
- Explain capacity failures with compatible worker counts and retry guidance.

## GUI views

### Workers

Show:

- Online/offline state.
- Pool and labels.
- Agent version.
- Architecture.
- Total/used/free resources.
- Heartbeat age.
- Active workloads.
- Certificate expiry.
- Cordon/drain/revoke actions.

### Queue

Show:

- Queued jobs.
- Queue age.
- Required capabilities.
- Scheduling reason.
- Estimated compatible worker count.
- Quota or policy rejection.

### Job detail

Show:

- State timeline.
- Attempt history.
- Worker.
- Resource request.
- Live logs.
- Events.
- Exit status.
- Artifacts.
- Cleanup status.
- Retry/cancel controls.

### CI

Show:

- Connected repositories.
- Policy state.
- Running and recent jobs.
- Wait/runtime.
- Failure reason.
- Debug-retention deadline.

## Error UX

Every error needs:

- Human-readable message.
- Stable code.
- Trace ID.
- Whether retry is safe.
- Retry time if known.
- Suggested operator action.

Examples:

```text
no_compatible_worker:
  No ready worker matches amd64 + 4 vCPU + docker=true.
  Add a worker to pool linux-builders or change the job requirements.
```
