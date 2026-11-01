# 7. Data Model and Migrations

## Existing tables to extend

Add organization ownership and indexes to users, nodes, images, templates,
instances, volumes, snapshots, operations, and API keys. Preserve existing
single-user behavior by creating a default organization during migration.

Extend `nodes` with:

- `organization_id`
- `pool_id`
- `labels_json`
- `capabilities_json`
- `used_vcpus`
- `used_memory_mb`
- `used_disk_mb`
- `max_workloads`
- `last_event_seq`
- `protocol_version`
- `revoked_at`
- `drain_started_at`
- `failure_count`

## New tables

### organizations

```text
id, name, slug, status, quota_json, created_at, updated_at
```

### organization_members

```text
organization_id, user_id, role, created_at
```

Roles: `owner`, `admin`, `member`, `viewer`.

### worker_pools

```text
id, organization_id, name, labels_json, policy_json, created_at, updated_at
```

### node_certificates

```text
id, node_id, serial, fingerprint, not_before, not_after, revoked_at, created_at
```

### worker_heartbeats

Retain a bounded history:

```text
id, node_id, received_at, sequence, status, capabilities_json, metrics_json
```

### jobs

```text
id, organization_id, owner_id, pool_id, type, state, priority,
spec_json, result_json, retry_policy_json, idempotency_key,
queued_at, started_at, finished_at, deadline_at, created_at, updated_at
```

Unique `(organization_id, idempotency_key)` when the key is non-null.

### job_attempts

```text
id, job_id, node_id, lease_id, state, failure_code, failure_message,
reserved_vcpus, reserved_memory_mb, reserved_disk_mb,
accepted_at, started_at, finished_at, created_at, updated_at
```

### worker_leases

```text
id, job_id, attempt_id, node_id, state, expires_at,
last_renewed_at, created_at, released_at
```

### job_events

```text
id, job_id, attempt_id, sequence, type, data_json, created_at
```

Unique `(job_id, sequence)`.

### job_logs

```text
id, job_id, attempt_id, stream, sequence, data, truncated, created_at
```

Store large logs outside SQLite when necessary and retain only metadata plus an
object key.

### artifacts

```text
id, job_id, attempt_id, name, media_type, size_bytes, sha256,
storage_key, status, created_at, expires_at
```

### ci_integrations and ci_jobs

Store provider installation IDs, repository allowlists, webhook secrets
(encrypted), workflow/job IDs, runner registration metadata, and status
mapping. Never store reusable runner tokens.

## Migration rules

1. Use a new goose migration for every schema change.
2. Add nullable columns before code begins writing them.
3. Backfill default organization and memberships.
4. Add indexes after large backfills where practical.
5. Keep down migrations safe for development databases.
6. Test migrations on an existing production-shaped database copy.
7. Do not place secret plaintext in migration fixtures.

## SQLite to PostgreSQL preparation

Avoid:

- SQLite-only locking assumptions in scheduler code.
- Holding transactions while waiting on workers.
- In-memory queue state as the source of truth.
- Unbounded JSON blobs for query-critical fields.

Use repository interfaces for workers, jobs, leases, events, and reservations so
PostgreSQL can be added without changing API handlers.
