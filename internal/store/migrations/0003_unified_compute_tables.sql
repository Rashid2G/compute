-- +goose NO TRANSACTION
-- +goose Up
-- Migration 0003: Unify compute and auth tables onto single state.db

PRAGMA foreign_keys = OFF;
BEGIN TRANSACTION;

-- ===== 1. Recreate Colliding Catalog & Storage Tables to Support Both Schemas =====

-- images
CREATE TABLE images_v2 (
  id              TEXT PRIMARY KEY,
  internal_id     TEXT NOT NULL DEFAULT '',
  owner_id        TEXT DEFAULT 'default',
  kind            TEXT NOT NULL DEFAULT 'vm' CHECK (kind IN ('vm','oci')),
  name            TEXT NOT NULL,
  os_family       TEXT DEFAULT 'unknown',
  arch            TEXT NOT NULL DEFAULT 'amd64',
  kernel_id       TEXT DEFAULT '',
  rootfs_sha256   TEXT DEFAULT '',
  oci_digest      TEXT DEFAULT '',
  oci_config_json TEXT DEFAULT '',
  size_bytes      INTEGER NOT NULL DEFAULT 0,
  min_disk_mb     INTEGER NOT NULL DEFAULT 1024,
  status          TEXT NOT NULL DEFAULT 'ready',
  description     TEXT NOT NULL DEFAULT '',
  base_image      TEXT NOT NULL DEFAULT '',
  packages        TEXT NOT NULL DEFAULT '[]',
  rootfs_path     TEXT NOT NULL DEFAULT '',
  kernel_path     TEXT NOT NULL DEFAULT '',
  size_mb         INTEGER NOT NULL DEFAULT 0,
  error_message   TEXT DEFAULT '',
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO images_v2 (
  id, owner_id, kind, name, os_family, arch, kernel_id, rootfs_sha256,
  oci_digest, oci_config_json, size_bytes, min_disk_mb, status, description, created_at
)
SELECT
  id, owner_id, kind, name, os_family, arch, kernel_id, rootfs_sha256,
  oci_digest, oci_config_json, size_bytes, min_disk_mb, status, description, created_at
FROM images;

DROP TABLE images;
ALTER TABLE images_v2 RENAME TO images;

-- templates
CREATE TABLE templates_v2 (
  id           TEXT PRIMARY KEY,
  internal_id  TEXT NOT NULL DEFAULT '',
  owner_id     TEXT DEFAULT 'default',
  name         TEXT NOT NULL,
  description  TEXT NOT NULL DEFAULT '',
  spec_json    TEXT NOT NULL DEFAULT '{}',
  source_vm_id TEXT DEFAULT '',
  disk_path    TEXT NOT NULL DEFAULT '',
  kernel_path  TEXT NOT NULL DEFAULT '',
  vcpus        INTEGER NOT NULL DEFAULT 1,
  memory_mb    INTEGER NOT NULL DEFAULT 512,
  disk_mb      INTEGER NOT NULL DEFAULT 1024,
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO templates_v2 (
  id, owner_id, name, description, spec_json, created_at, updated_at
)
SELECT
  id, owner_id, name, description, spec_json, created_at, updated_at
FROM templates;

DROP TABLE templates;
ALTER TABLE templates_v2 RENAME TO templates;

-- volumes
CREATE TABLE volumes_v2 (
  id               TEXT PRIMARY KEY,
  owner_id         TEXT DEFAULT 'default',
  name             TEXT NOT NULL,
  node_id          TEXT,
  size_mb          INTEGER NOT NULL DEFAULT 0,
  kind             TEXT NOT NULL DEFAULT 'data' CHECK (kind IN ('root','data')),
  source_image_id  TEXT,
  source_volume_id TEXT,
  instance_id      TEXT,
  device_index     INTEGER,
  read_only        INTEGER NOT NULL DEFAULT 0,
  status           TEXT NOT NULL DEFAULT 'available',
  used_mb          INTEGER NOT NULL DEFAULT 0,
  pool             TEXT NOT NULL DEFAULT 'default',
  size_gb          INTEGER NOT NULL DEFAULT 0,
  used_gb          REAL NOT NULL DEFAULT 0,
  attached_vm      TEXT NOT NULL DEFAULT '',
  device_path      TEXT NOT NULL DEFAULT '',
  mount_point      TEXT NOT NULL DEFAULT '',
  filesystem       TEXT NOT NULL DEFAULT 'ext4',
  path             TEXT NOT NULL DEFAULT '',
  iops             INTEGER NOT NULL DEFAULT 0,
  throughput       TEXT NOT NULL DEFAULT '',
  snapshots        INTEGER NOT NULL DEFAULT 0,
  created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO volumes_v2 (
  id, owner_id, name, node_id, size_mb, kind, source_image_id, source_volume_id,
  instance_id, device_index, read_only, status, used_mb, created_at
)
SELECT
  id, owner_id, name, node_id, size_mb, kind, source_image_id, source_volume_id,
  instance_id, device_index, read_only, status, used_mb, created_at
FROM volumes;

DROP TABLE volumes;
ALTER TABLE volumes_v2 RENAME TO volumes;

-- operations
CREATE TABLE operations_v2 (
  id              TEXT PRIMARY KEY,
  owner_id        TEXT NOT NULL DEFAULT 'default',
  kind            TEXT NOT NULL,
  target_id       TEXT NOT NULL DEFAULT '',
  target_name     TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT 'queued',
  progress        INTEGER NOT NULL DEFAULT 0,
  error_code      TEXT,
  error_msg       TEXT NOT NULL DEFAULT '',
  trace_id        TEXT NOT NULL DEFAULT '',
  idempotency_key TEXT,
  request_json    TEXT NOT NULL DEFAULT '{}',
  result_json     TEXT NOT NULL DEFAULT '',
  task_id         TEXT NOT NULL DEFAULT '',
  actor           TEXT NOT NULL DEFAULT '',
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  started_at      TIMESTAMP,
  updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  finished_at     TIMESTAMP
);

INSERT INTO operations_v2 (
  id, owner_id, kind, target_id, status, progress, error_code, error_msg,
  trace_id, idempotency_key, created_at, updated_at
)
SELECT
  id, owner_id, kind, target_id, status, progress, error_code, COALESCE(error_msg, ''),
  trace_id, idempotency_key, created_at, updated_at
FROM operations;

DROP TABLE operations;
ALTER TABLE operations_v2 RENAME TO operations;

-- worker_pools
CREATE TABLE worker_pools_v2 (
  id              TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL DEFAULT 'org_default',
  name            TEXT NOT NULL,
  labels_json     TEXT NOT NULL DEFAULT '{}',
  policy_json     TEXT NOT NULL DEFAULT '{}',
  created_at      TIMESTAMP NOT NULL,
  updated_at      TIMESTAMP NOT NULL
);

INSERT INTO worker_pools_v2 (
  id, organization_id, name, labels_json, policy_json, created_at, updated_at
)
SELECT
  id, organization_id, name, labels_json, policy_json,
  CASE WHEN typeof(created_at) = 'integer' THEN datetime(created_at, 'unixepoch') ELSE created_at END,
  CASE WHEN typeof(updated_at) = 'integer' THEN datetime(updated_at, 'unixepoch') ELSE updated_at END
FROM worker_pools;

DROP TABLE worker_pools;
ALTER TABLE worker_pools_v2 RENAME TO worker_pools;

-- jobs
CREATE TABLE jobs_v2 (
  id                TEXT PRIMARY KEY,
  organization_id   TEXT DEFAULT 'org_default',
  owner_id          TEXT NOT NULL DEFAULT '',
  pool_id           TEXT DEFAULT '',
  type              TEXT NOT NULL,
  state             TEXT NOT NULL,
  priority          INTEGER NOT NULL DEFAULT 0,
  spec_json         TEXT NOT NULL,
  result_json       TEXT NOT NULL DEFAULT '',
  retry_policy_json TEXT NOT NULL DEFAULT '{}',
  idempotency_key   TEXT,
  repo_id           TEXT NOT NULL DEFAULT '',
  repo_url          TEXT NOT NULL DEFAULT '',
  repo_name         TEXT NOT NULL DEFAULT '',
  branch            TEXT NOT NULL DEFAULT '',
  commit_sha        TEXT NOT NULL DEFAULT '',
  source            TEXT NOT NULL DEFAULT 'cli',
  vm_id             TEXT NOT NULL DEFAULT '',
  worker_id         TEXT NOT NULL DEFAULT '',
  queued_at         TIMESTAMP NOT NULL,
  started_at        TIMESTAMP,
  finished_at       TIMESTAMP,
  deadline_at       TIMESTAMP,
  created_at        TIMESTAMP NOT NULL,
  updated_at        TIMESTAMP NOT NULL
);

INSERT INTO jobs_v2 (
  id, organization_id, owner_id, pool_id, type, state, priority, spec_json,
  result_json, retry_policy_json, idempotency_key, queued_at, started_at,
  finished_at, deadline_at, created_at, updated_at
)
SELECT
  id, organization_id, owner_id, pool_id, type, state, priority, spec_json,
  COALESCE(result_json, ''), retry_policy_json, idempotency_key,
  COALESCE(
    CASE WHEN typeof(queued_at) = 'integer' THEN datetime(queued_at, 'unixepoch') ELSE queued_at END,
    CASE WHEN typeof(created_at) = 'integer' THEN datetime(created_at, 'unixepoch') ELSE created_at END
  ),
  CASE WHEN typeof(started_at) = 'integer' THEN datetime(started_at, 'unixepoch') ELSE started_at END,
  CASE WHEN typeof(finished_at) = 'integer' THEN datetime(finished_at, 'unixepoch') ELSE finished_at END,
  CASE WHEN typeof(deadline_at) = 'integer' THEN datetime(deadline_at, 'unixepoch') ELSE deadline_at END,
  CASE WHEN typeof(created_at) = 'integer' THEN datetime(created_at, 'unixepoch') ELSE created_at END,
  CASE WHEN typeof(updated_at) = 'integer' THEN datetime(updated_at, 'unixepoch') ELSE updated_at END
FROM jobs;

DROP TABLE jobs;
ALTER TABLE jobs_v2 RENAME TO jobs;

-- job_attempts
CREATE TABLE job_attempts_v2 (
  id                  TEXT PRIMARY KEY,
  job_id              TEXT NOT NULL,
  node_id             TEXT,
  worker_id           TEXT NOT NULL DEFAULT '',
  state               TEXT NOT NULL,
  failure_code        TEXT,
  failure_message     TEXT,
  reserved_vcpus      INTEGER NOT NULL DEFAULT 0,
  reserved_memory_mb  INTEGER NOT NULL DEFAULT 0,
  reserved_disk_mb    INTEGER NOT NULL DEFAULT 0,
  accepted_at         INTEGER,
  started_at          INTEGER,
  finished_at         INTEGER,
  created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO job_attempts_v2 (
  id, job_id, node_id, state, failure_code, failure_message, reserved_vcpus,
  reserved_memory_mb, reserved_disk_mb, accepted_at, started_at, finished_at,
  created_at, updated_at
)
SELECT
  id, job_id, node_id, state, failure_code, failure_message, reserved_vcpus,
  reserved_memory_mb, reserved_disk_mb, accepted_at, started_at, finished_at,
  created_at, updated_at
FROM job_attempts;

DROP TABLE job_attempts;
ALTER TABLE job_attempts_v2 RENAME TO job_attempts;

-- worker_leases
CREATE TABLE worker_leases_v2 (
  id         TEXT PRIMARY KEY,
  job_id     TEXT NOT NULL,
  attempt_id TEXT NOT NULL,
  node_id    TEXT DEFAULT '',
  worker_id  TEXT NOT NULL DEFAULT '',
  state      TEXT NOT NULL,
  expires_at TIMESTAMP NOT NULL
);

INSERT INTO worker_leases_v2 (
  id, job_id, attempt_id, node_id, state, expires_at
)
SELECT
  id, job_id, attempt_id, node_id, state, expires_at
FROM worker_leases;

DROP TABLE worker_leases;
ALTER TABLE worker_leases_v2 RENAME TO worker_leases;

-- job_logs
CREATE TABLE job_logs_v2 (
  id         TEXT PRIMARY KEY,
  job_id     TEXT NOT NULL,
  attempt_id TEXT NOT NULL DEFAULT '',
  stream     TEXT NOT NULL DEFAULT 'stdout',
  sequence   INTEGER NOT NULL DEFAULT 0,
  seq        INTEGER NOT NULL DEFAULT 0,
  data       TEXT NOT NULL DEFAULT '',
  truncated  INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO job_logs_v2 (
  id, job_id, attempt_id, stream, sequence, seq, data, truncated, created_at
)
SELECT
  id, job_id, COALESCE(attempt_id, ''), stream, sequence, sequence, COALESCE(data, ''), truncated, created_at
FROM job_logs;

DROP TABLE job_logs;
ALTER TABLE job_logs_v2 RENAME TO job_logs;

-- ===== 2. Create Compute Control-Plane Tables =====

CREATE TABLE IF NOT EXISTS vms (
  id                   TEXT NOT NULL,
  internal_id          TEXT NOT NULL DEFAULT '',
  owner_id             TEXT NOT NULL DEFAULT 'default',
  name                 TEXT NOT NULL,
  status               TEXT NOT NULL,
  desired_running      INTEGER NOT NULL DEFAULT 0,
  vcpus                INTEGER NOT NULL,
  memory_mb            INTEGER NOT NULL,
  disk_mb              INTEGER NOT NULL DEFAULT 0,
  ip_address           TEXT NOT NULL,
  public_ip            TEXT NOT NULL DEFAULT '',
  public_ip_enabled    INTEGER NOT NULL DEFAULT 1,
  network_isolation    TEXT NOT NULL DEFAULT 'account',
  mac_address          TEXT NOT NULL,
  tap_device           TEXT NOT NULL,
  tailscale_enabled    INTEGER NOT NULL DEFAULT 0,
  tailnet_ip           TEXT NOT NULL DEFAULT '',
  tailnet_dns          TEXT NOT NULL DEFAULT '',
  tailscale_device_id  TEXT NOT NULL DEFAULT '',
  rootfs_path          TEXT NOT NULL,
  kernel_path          TEXT NOT NULL,
  disk_path            TEXT NOT NULL,
  snapshot_path        TEXT DEFAULT '',
  mem_file_path        TEXT DEFAULT '',
  ssh_key_path         TEXT DEFAULT '',
  ssh_key_name         TEXT DEFAULT '',
  ssh_public_key       TEXT DEFAULT '',
  ssh_password_enabled INTEGER NOT NULL DEFAULT 0,
  pid                  INTEGER DEFAULT 0,
  socket_path          TEXT DEFAULT '',
  protected            INTEGER NOT NULL DEFAULT 0,
  description          TEXT NOT NULL DEFAULT '',
  startup_script       TEXT NOT NULL DEFAULT '',
  pool_id              TEXT NOT NULL DEFAULT '',
  worker_id            TEXT NOT NULL DEFAULT '',
  last_activity_at     TIMESTAMP,
  auto_hibernated      INTEGER NOT NULL DEFAULT 0,
  hibernate_reason     TEXT NOT NULL DEFAULT '',
  created_at           TIMESTAMP NOT NULL,
  updated_at           TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS ssh_keys (
  id            TEXT PRIMARY KEY,
  owner_id      TEXT NOT NULL DEFAULT 'default',
  name          TEXT NOT NULL,
  public_key    TEXT NOT NULL,
  fingerprint   TEXT NOT NULL,
  all_sandboxes INTEGER NOT NULL DEFAULT 0,
  is_default    INTEGER NOT NULL DEFAULT 0,
  created_at    TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS worker_tokens (
  token       TEXT PRIMARY KEY,
  name        TEXT NOT NULL DEFAULT '',
  pool_id     TEXT NOT NULL,
  labels_json TEXT NOT NULL DEFAULT '{}',
  expires_at  TIMESTAMP NOT NULL,
  used_at     TIMESTAMP,
  created_at  TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS worker_nodes (
  id                TEXT PRIMARY KEY,
  name              TEXT NOT NULL,
  pool_id           TEXT NOT NULL,
  role              TEXT NOT NULL DEFAULT 'worker',
  status            TEXT NOT NULL,
  underlay_ip       TEXT NOT NULL DEFAULT '',
  arch              TEXT NOT NULL DEFAULT '',
  total_vcpus       INTEGER NOT NULL DEFAULT 0,
  total_mem_mb      INTEGER NOT NULL DEFAULT 0,
  total_disk_mb     INTEGER NOT NULL DEFAULT 0,
  allocated_vcpus   INTEGER NOT NULL DEFAULT 0,
  allocated_mem_mb  INTEGER NOT NULL DEFAULT 0,
  allocated_disk_mb INTEGER NOT NULL DEFAULT 0,
  physical_vcpus    INTEGER NOT NULL DEFAULT 0,
  physical_mem_mb   INTEGER NOT NULL DEFAULT 0,
  physical_disk_mb  INTEGER NOT NULL DEFAULT 0,
  used_vcpus        INTEGER NOT NULL DEFAULT 0,
  used_mem_mb       INTEGER NOT NULL DEFAULT 0,
  used_disk_mb      INTEGER NOT NULL DEFAULT 0,
  running_vms       INTEGER NOT NULL DEFAULT 0,
  labels_json       TEXT NOT NULL DEFAULT '{}',
  capabilities_json TEXT NOT NULL DEFAULT '{}',
  agent_version     TEXT NOT NULL DEFAULT '',
  fc_version        TEXT NOT NULL DEFAULT '',
  last_heartbeat    TIMESTAMP,
  created_at        TIMESTAMP NOT NULL,
  updated_at        TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS cluster_settings (
  key        TEXT PRIMARY KEY,
  value_json TEXT NOT NULL,
  updated_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS github_connections (
  id                TEXT PRIMARY KEY,
  owner_id          TEXT NOT NULL DEFAULT 'default',
  name              TEXT NOT NULL,
  repo_url          TEXT NOT NULL,
  default_branch    TEXT NOT NULL DEFAULT 'main',
  github_token      TEXT NOT NULL DEFAULT '',
  webhook_secret    TEXT NOT NULL DEFAULT '',
  default_command   TEXT NOT NULL DEFAULT '',
  vcpus             INTEGER NOT NULL DEFAULT 2,
  memory_mb         INTEGER NOT NULL DEFAULT 2048,
  timeout_sec       INTEGER NOT NULL DEFAULT 600,
  status            TEXT NOT NULL DEFAULT 'connected',
  last_triggered_at TIMESTAMP,
  created_at        TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS webhooks (
  id               TEXT PRIMARY KEY,
  url              TEXT NOT NULL,
  description      TEXT NOT NULL DEFAULT '',
  secret           TEXT NOT NULL DEFAULT '',
  events           TEXT NOT NULL DEFAULT '[]',
  active           INTEGER NOT NULL DEFAULT 1,
  last_delivery    TEXT NOT NULL DEFAULT '',
  success_rate     TEXT NOT NULL DEFAULT '100%',
  deliveries_count INTEGER NOT NULL DEFAULT 0,
  created_at       TIMESTAMP NOT NULL,
  updated_at       TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS otel_config (
  id              INTEGER PRIMARY KEY CHECK (id = 1),
  enabled         INTEGER NOT NULL DEFAULT 0,
  endpoint        TEXT NOT NULL DEFAULT '',
  protocol        TEXT NOT NULL DEFAULT 'http/protobuf',
  auth_header     TEXT NOT NULL DEFAULT '',
  service_name    TEXT NOT NULL DEFAULT 'on-demand-vms',
  sample_rate     TEXT NOT NULL DEFAULT '1.0',
  export_interval TEXT NOT NULL DEFAULT '10s',
  metrics_enabled INTEGER NOT NULL DEFAULT 1,
  traces_enabled  INTEGER NOT NULL DEFAULT 1,
  logs_enabled    INTEGER NOT NULL DEFAULT 1,
  spans_exported  INTEGER NOT NULL DEFAULT 0,
  last_export_at  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS audit_events (
  id          TEXT PRIMARY KEY,
  timestamp   TIMESTAMP NOT NULL,
  event       TEXT NOT NULL,
  actor       TEXT NOT NULL,
  target      TEXT NOT NULL,
  status      TEXT NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  payload     TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS task_records (
  id             TEXT PRIMARY KEY,
  owner_id       TEXT NOT NULL DEFAULT 'default',
  action         TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL DEFAULT 'queued',
  requested      INTEGER NOT NULL DEFAULT 0,
  queued         INTEGER NOT NULL DEFAULT 0,
  succeeded_json TEXT NOT NULL DEFAULT '[]',
  failed_json    TEXT NOT NULL DEFAULT '[]',
  warnings_json  TEXT NOT NULL DEFAULT '[]',
  created_at     TIMESTAMP NOT NULL,
  updated_at     TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS access_api_keys (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  prefix     TEXT NOT NULL,
  token      TEXT NOT NULL DEFAULT '',
  scope      TEXT NOT NULL DEFAULT '',
  last_used  TEXT NOT NULL DEFAULT 'Never',
  expires_at TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL DEFAULT 'admin',
  created_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_profiles (
  id               TEXT PRIMARY KEY,
  name             TEXT NOT NULL,
  provider         TEXT NOT NULL,
  upstream_url     TEXT NOT NULL DEFAULT '',
  inject_header    TEXT NOT NULL DEFAULT '',
  secret_ref       TEXT NOT NULL DEFAULT '',
  sandboxes_linked INTEGER NOT NULL DEFAULT 0,
  status           TEXT NOT NULL DEFAULT 'active',
  created_at       TIMESTAMP NOT NULL,
  updated_at       TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS environments (
  owner_id             TEXT NOT NULL DEFAULT '',
  name                 TEXT NOT NULL,
  is_default           INTEGER NOT NULL DEFAULT 0,
  safe_for_third_party INTEGER NOT NULL DEFAULT 0,
  github_repos_enabled INTEGER NOT NULL DEFAULT 0,
  github_repos         TEXT NOT NULL DEFAULT '[]',
  secrets_enabled      INTEGER NOT NULL DEFAULT 0,
  secrets              TEXT NOT NULL DEFAULT '',
  boat_creds_enabled   INTEGER NOT NULL DEFAULT 0,
  agent_creds_enabled  INTEGER NOT NULL DEFAULT 0,
  credential_profile   TEXT NOT NULL DEFAULT '',
  version              INTEGER NOT NULL DEFAULT 1,
  updated_at           TIMESTAMP NOT NULL,
  PRIMARY KEY (owner_id, name)
);

CREATE TABLE IF NOT EXISTS port_forwards (
  id         TEXT PRIMARY KEY,
  owner_id   TEXT NOT NULL DEFAULT 'default',
  host_port  INTEGER NOT NULL,
  vm_id      TEXT NOT NULL,
  vm_name    TEXT NOT NULL DEFAULT '',
  guest_port INTEGER NOT NULL,
  protocol   TEXT NOT NULL DEFAULT 'tcp',
  subdomain  TEXT NOT NULL DEFAULT '',
  public_url TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'active',
  public     INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS legacy_migrations (
  id           TEXT PRIMARY KEY,
  migrated_at  TIMESTAMP NOT NULL,
  details_json TEXT NOT NULL DEFAULT '{}'
);

-- ===== 3. Indexes and Triggers =====

CREATE INDEX IF NOT EXISTS idx_vms_internal_id ON vms(internal_id);
CREATE INDEX IF NOT EXISTS idx_vms_id ON vms(id);
CREATE INDEX IF NOT EXISTS idx_vms_name ON vms(name);
CREATE INDEX IF NOT EXISTS idx_vms_owner ON vms(owner_id);
CREATE INDEX IF NOT EXISTS idx_vms_owner_name ON vms(owner_id, name);
CREATE INDEX IF NOT EXISTS idx_vms_status ON vms(status);
CREATE INDEX IF NOT EXISTS idx_vms_ip ON vms(ip_address);
CREATE INDEX IF NOT EXISTS idx_vms_public_ip ON vms(public_ip);
CREATE INDEX IF NOT EXISTS idx_vms_protected ON vms(protected);

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS trg_prevent_protected_vm_delete
BEFORE DELETE ON vms
FOR EACH ROW
WHEN OLD.protected = 1
BEGIN
  SELECT RAISE(ABORT, 'item is protected against deletion');
END;
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS idx_images_internal_id ON images(internal_id);
CREATE INDEX IF NOT EXISTS idx_images_id ON images(id);
CREATE INDEX IF NOT EXISTS idx_images_name ON images(name);
CREATE INDEX IF NOT EXISTS idx_images_owner ON images(owner_id);

CREATE INDEX IF NOT EXISTS idx_templates_internal_id ON templates(internal_id);
CREATE INDEX IF NOT EXISTS idx_templates_id ON templates(id);
CREATE INDEX IF NOT EXISTS idx_templates_name ON templates(name);
CREATE INDEX IF NOT EXISTS idx_templates_owner ON templates(owner_id);

CREATE INDEX IF NOT EXISTS idx_volumes_instance ON volumes(instance_id);
CREATE INDEX IF NOT EXISTS idx_operations_target ON operations(target_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_idempotency ON jobs(organization_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_jobs_state_priority ON jobs(state, priority DESC, queued_at);

CREATE UNIQUE INDEX IF NOT EXISTS idx_port_forwards_host_port ON port_forwards(host_port) WHERE host_port > 0;

COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;
BEGIN TRANSACTION;
DROP TABLE IF EXISTS legacy_migrations;
DROP TABLE IF EXISTS port_forwards;
DROP TABLE IF EXISTS environments;
DROP TABLE IF EXISTS agent_profiles;
DROP TABLE IF EXISTS access_api_keys;
DROP TABLE IF EXISTS task_records;
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS otel_config;
DROP TABLE IF EXISTS webhooks;
DROP TABLE IF EXISTS github_connections;
DROP TABLE IF EXISTS cluster_settings;
DROP TABLE IF EXISTS worker_nodes;
DROP TABLE IF EXISTS worker_tokens;
DROP TABLE IF EXISTS ssh_keys;
DROP TRIGGER IF EXISTS trg_prevent_protected_vm_delete;
DROP TABLE IF EXISTS vms;
COMMIT;
PRAGMA foreign_keys = ON;
