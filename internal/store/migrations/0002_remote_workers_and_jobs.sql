-- +goose Up
CREATE TABLE organizations (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, slug TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL DEFAULT 'active', quota_json TEXT,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE organization_members (
  organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('owner','admin','member','viewer')),
  created_at INTEGER NOT NULL, PRIMARY KEY (organization_id,user_id)
);
CREATE TABLE worker_pools (
  id TEXT PRIMARY KEY, organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name TEXT NOT NULL, labels_json TEXT NOT NULL DEFAULT '{}', policy_json TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, UNIQUE (organization_id,name)
);
INSERT INTO organizations(id,name,slug,status,created_at,updated_at) VALUES ('org_default','Default','default','active',unixepoch(),unixepoch());
INSERT INTO worker_pools(id,organization_id,name,created_at,updated_at) VALUES ('pool_default','org_default','default',unixepoch(),unixepoch());
INSERT INTO organization_members(organization_id,user_id,role,created_at) SELECT 'org_default',id,'owner',unixepoch() FROM users;
ALTER TABLE nodes ADD COLUMN organization_id TEXT REFERENCES organizations(id);
ALTER TABLE nodes ADD COLUMN pool_id TEXT REFERENCES worker_pools(id);
ALTER TABLE nodes ADD COLUMN capabilities_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE nodes ADD COLUMN used_vcpus INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN used_memory_mb INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN used_disk_mb INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN max_workloads INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN last_event_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN protocol_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE nodes ADD COLUMN revoked_at INTEGER;
ALTER TABLE nodes ADD COLUMN drain_started_at INTEGER;
ALTER TABLE nodes ADD COLUMN failure_count INTEGER NOT NULL DEFAULT 0;
UPDATE nodes SET organization_id='org_default', pool_id='pool_default';
CREATE INDEX idx_nodes_org_pool ON nodes(organization_id,pool_id);

CREATE TABLE node_certificates (
 id TEXT PRIMARY KEY, node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 serial TEXT NOT NULL UNIQUE, fingerprint TEXT NOT NULL, not_before INTEGER NOT NULL,
 not_after INTEGER NOT NULL, revoked_at INTEGER, created_at INTEGER NOT NULL
);
CREATE TABLE worker_heartbeats (
 id TEXT PRIMARY KEY, node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 received_at INTEGER NOT NULL, sequence INTEGER NOT NULL, status TEXT NOT NULL,
 capabilities_json TEXT NOT NULL DEFAULT '{}', metrics_json TEXT NOT NULL DEFAULT '{}', UNIQUE(node_id,sequence)
);
CREATE INDEX idx_worker_heartbeats_received ON worker_heartbeats(node_id,received_at DESC);
CREATE TABLE jobs (
 id TEXT PRIMARY KEY, organization_id TEXT NOT NULL REFERENCES organizations(id),
 owner_id TEXT NOT NULL REFERENCES users(id), pool_id TEXT REFERENCES worker_pools(id),
 type TEXT NOT NULL, state TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 0,
 spec_json TEXT NOT NULL, result_json TEXT, retry_policy_json TEXT NOT NULL DEFAULT '{}',
 idempotency_key TEXT, queued_at INTEGER, started_at INTEGER, finished_at INTEGER,
 deadline_at INTEGER, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_jobs_idempotency ON jobs(organization_id,idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX idx_jobs_state_priority ON jobs(state,priority DESC,queued_at);
CREATE TABLE job_attempts (
 id TEXT PRIMARY KEY, job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 node_id TEXT REFERENCES nodes(id), state TEXT NOT NULL, failure_code TEXT,
 failure_message TEXT, reserved_vcpus INTEGER NOT NULL DEFAULT 0, reserved_memory_mb INTEGER NOT NULL DEFAULT 0,
 reserved_disk_mb INTEGER NOT NULL DEFAULT 0, accepted_at INTEGER, started_at INTEGER, finished_at INTEGER,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE worker_leases (
 id TEXT PRIMARY KEY, job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 attempt_id TEXT NOT NULL REFERENCES job_attempts(id) ON DELETE CASCADE,
 node_id TEXT NOT NULL REFERENCES nodes(id), state TEXT NOT NULL, expires_at INTEGER NOT NULL,
 last_renewed_at INTEGER, created_at INTEGER NOT NULL, released_at INTEGER
);
ALTER TABLE job_attempts ADD COLUMN lease_id TEXT REFERENCES worker_leases(id);
CREATE TABLE job_events (
 id TEXT PRIMARY KEY, job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 attempt_id TEXT REFERENCES job_attempts(id) ON DELETE SET NULL, sequence INTEGER NOT NULL,
 type TEXT NOT NULL, data_json TEXT NOT NULL DEFAULT '{}', created_at INTEGER NOT NULL,
 UNIQUE(job_id,sequence)
);
CREATE TABLE job_logs (
 id TEXT PRIMARY KEY, job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 attempt_id TEXT REFERENCES job_attempts(id) ON DELETE SET NULL, stream TEXT NOT NULL,
 sequence INTEGER NOT NULL, data BLOB, truncated INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL
);
CREATE TABLE artifacts (
 id TEXT PRIMARY KEY, job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 attempt_id TEXT REFERENCES job_attempts(id) ON DELETE SET NULL, name TEXT NOT NULL,
 media_type TEXT NOT NULL DEFAULT 'application/octet-stream', size_bytes INTEGER NOT NULL DEFAULT 0,
 sha256 TEXT NOT NULL DEFAULT '', storage_key TEXT NOT NULL, status TEXT NOT NULL,
 created_at INTEGER NOT NULL, expires_at INTEGER
);

-- +goose Down
DROP TABLE IF EXISTS artifacts;
DROP TABLE IF EXISTS job_logs;
DROP TABLE IF EXISTS job_events;
DROP TABLE IF EXISTS worker_leases;
DROP TABLE IF EXISTS job_attempts;
DROP TABLE IF EXISTS jobs;
DROP TABLE IF EXISTS worker_heartbeats;
DROP TABLE IF EXISTS node_certificates;
DROP INDEX IF EXISTS idx_nodes_org_pool;
ALTER TABLE nodes DROP COLUMN failure_count;
ALTER TABLE nodes DROP COLUMN drain_started_at;
ALTER TABLE nodes DROP COLUMN revoked_at;
ALTER TABLE nodes DROP COLUMN protocol_version;
ALTER TABLE nodes DROP COLUMN last_event_seq;
ALTER TABLE nodes DROP COLUMN max_workloads;
ALTER TABLE nodes DROP COLUMN used_disk_mb;
ALTER TABLE nodes DROP COLUMN used_memory_mb;
ALTER TABLE nodes DROP COLUMN used_vcpus;
ALTER TABLE nodes DROP COLUMN capabilities_json;
ALTER TABLE nodes DROP COLUMN pool_id;
ALTER TABLE nodes DROP COLUMN organization_id;
DROP TABLE IF EXISTS worker_pools;
DROP TABLE IF EXISTS organization_members;
DROP TABLE IF EXISTS organizations;
