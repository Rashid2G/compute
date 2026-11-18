# Compute Workflows for Agents

Long-form recipes for common multi-step tasks. Each recipe assumes:

```bash
export COMPUTE_DAEMON="127.0.0.1:18080"
export COMPUTE_API_KEY="cmp_live_..."
```

Run `compute doctor` first. If readiness fails, stop and report the failing check and its
remediation instead of retrying blindly.

---

## 1. One-shot build or test job

```bash
compute job run \
  --pool default \
  --vcpus 4 --mem 4096 --timeout 900 \
  --files . \
  --cwd /workspace \
  -- make build test
```

The CLI prints `Job submitted: ID=...` and streams logs. On success it prints
`[Job completed successfully]` and exits 0. On failure it exits 1; capture the failing lines
from stderr and `compute job logs <id>`.

For asynchronous submission:

```bash
OUT=$(compute job run --follow=false --files . -- npm test)
ID=$(printf '%s\n' "$OUT" | sed -n 's/.*ID=\([^ ]*\).*/\1/p')
compute job info "$ID"
compute job logs "$ID"
```

---

## 2. Long-lived development sandbox

```bash
BOX="dev-$(date +%s)"
compute sandbox create --name "$BOX" --tier 2vcpu --memory 4096 --disk 20480 --start
compute sandbox exec "$BOX" -- bash -lc 'sudo apt-get update -y && sudo apt-get install -y git make'
compute scp -r . "$BOX:/workspace/repo"
compute sandbox exec "$BOX" -- bash -lc 'cd /workspace/repo && make build'
```

Checkpoint before risky work:

```bash
compute sandbox save-state "$BOX"
# ... experiment ...
compute sandbox resume "$BOX"
```

Cleanup:

```bash
compute sandbox delete "$BOX"
```

---

## 3. Live web application

```bash
BOX="web-$(date +%s)"
compute sandbox create --name "$BOX" --tier 2vcpu --memory 2048 --start
compute scp -r ./app "$BOX:/workspace/app"
compute sandbox process start "$BOX" --cwd /workspace/app -- npm start
compute sandbox exec "$BOX" -- bash -lc '
  for i in $(seq 1 30); do
    curl -fsS http://127.0.0.1:3000/ >/dev/null && exit 0
    sleep 1
  done
  exit 1
'
compute expose --json "$BOX" 3000
```

Use the returned `public_url` for verification. For a private service:

```bash
compute expose --private --json "$BOX" 3000
```

The response contains a short-lived URL; do not publish it.

---

## 4. Persist data across sandboxes with volumes

```bash
# Create a volume and capture its ID.
VOL=$(curl -fsS -X POST "http://${COMPUTE_DAEMON}/api/v1/volumes" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{"name":"shared-data","size_gb":20,"filesystem":"ext4"}' | jq -r .id)

# Attach to a stopped sandbox.
compute sandbox stop "$BOX"
curl -fsS -X POST "http://${COMPUTE_DAEMON}/api/v1/volumes/${VOL}/attach" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
  -H 'Content-Type: application/json' \
  -d "{\"vm_id\":\"${BOX}\"}"
compute sandbox start "$BOX"
```

Inside the guest the volume is mounted at `/mnt/data`. Detach before deleting:

```bash
compute sandbox stop "$BOX"
curl -fsS -X POST "http://${COMPUTE_DAEMON}/api/v1/volumes/${VOL}/detach" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}"
```

---

## 5. Repo CI with a GitHub webhook

1. Connect the repository once (repo owner or admin):

```bash
curl -fsS -X POST "http://${COMPUTE_DAEMON}/api/v1/ci/repos" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "my-repo",
    "repo_url": "https://github.com/org/repo",
    "default_branch": "main",
    "github_token": "'"${GITHUB_TOKEN}"'",
    "default_command": "make test",
    "vcpus": 2,
    "memory_mb": 2048,
    "timeout_sec": 900
  }'
```

The response includes `webhook_secret` once. Configure the GitHub webhook to
`https://<daemon>/api/v1/ci/webhooks/github` with that secret and push events.

2. Trigger manually when needed:

```bash
curl -fsS -X POST "http://${COMPUTE_DAEMON}/api/v1/ci/repos/<id>/trigger" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{"branch":"main"}'
```

3. Follow the resulting job with `compute job info` / `compute job logs`.

---

## 6. Distributed workers and pools

Control plane:

```bash
compute pool create batch
compute worker token create --pool batch --name worker-1
```

Worker host (outbound connection only):

```bash
compute-worker run \
  --control-plane "https://compute.example.com" \
  --token "${COMPUTE_WORKER_TOKEN}" \
  --pool batch \
  --name worker-1
```

Fan out shards:

```bash
for i in 1 2 3 4; do
  compute job run --pool batch --follow=false --files . -- ./run-shard.sh "$i"
done
compute job list
```

Check worker state with `compute worker list`; cordon before maintenance and drain before
shutdown.

---

## 7. Multi-agent coordination

- Give each agent a unique name prefix (`agent-a-`, `agent-b-`) so bulk cleanup cannot collide.
- Use `compute sandbox protect` on sandboxes that outlive a single agent run.
- Use jobs for untrusted work; they cannot mutate another agent's sandbox.
- For shared artifacts, use a volume or copy results out with `scp` before deleting.
- Do not share API keys between tenants; use one key per agent or automation.
- For parallel writes to the same volume, coordinate with the user; Compute does not provide
  distributed locks.

---

## 8. Dogfooding Compute inside Compute

```bash
BOX="compute-dogfood"
compute sandbox create --name "$BOX" --tier 4vcpu --memory 8192 --disk 40960 --start
compute scp -r . "$BOX:/workspace/on-demand-vms"
compute sandbox exec "$BOX" -- bash -lc 'cd /workspace/on-demand-vms && make build'
compute sandbox exec "$BOX" -- bash -lc 'cd /workspace/on-demand-vms && go test ./cmd/... ./internal/... ./pkg/...'
```

Start a nested daemon and reach it through ingress (not the host port):

```bash
compute sandbox exec "$BOX" -- bash -lc '
  cd /workspace/on-demand-vms &&
  nohup ./bin/computed -listen 127.0.0.1:18080 >/tmp/computed.log 2>&1 &
'
curl -fsS "http://${COMPUTE_DAEMON}/preview/${BOX}/18080/api/v1/health"
```

Nested sandboxes use the guest's own bridge and IP space, so they do not collide with the host.

---

## 9. Bulk lifecycle and cleanup

List first:

```bash
compute sandbox list
```

Then act on an explicit prefix:

```bash
compute sandbox stop 'agent-a-*'
compute sandbox delete 'agent-a-*'
```

For REST bulk requests, wait for the task:

```bash
curl -fsS -X POST "http://${COMPUTE_DAEMON}/api/v1/sandboxes/bulk-delete" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{"ids":["box-1","box-2"],"async":true}'

curl -fsS "http://${COMPUTE_DAEMON}/api/v1/tasks/<task-id>" \
  -H "Authorization: Bearer ${COMPUTE_API_KEY}"
```

Do not delete shared volumes, templates, or images without explicit user confirmation.

---

## 10. Capacity and cost control

- Prefer the smallest tier that passes the work (`1vcpu` for lint/docs, `2vcpu` for most builds).
- Set `--timeout` on jobs so a hung command cannot run forever.
- Stop sandboxes when idle; `save-state` frees CPU/RAM while preserving the process tree.
- Use templates to avoid reinstalling dependencies on every run.
- Check `GET /api/v1/queue` and `GET /api/v1/stats` before a large fan-out.
- Admin: tune `cpu_soft_overcommit_ratio`, `cpu_hard_overcommit_ratio`, and
  `idle_ram_snapshot_enabled` in the scheduling policy rather than oversubscribing blindly.

---

## 11. Handoff checklist

Before ending a task, report:

- sandboxes/jobs created and their IDs
- final state (running, stopped, succeeded, failed)
- preview URLs and whether they are public or private
- cleanup performed (or why resources were kept)
- any capacity/queue/readiness warnings
- commands that could not be verified (for example, no KVM host available)

Then delete or protect everything you created.
