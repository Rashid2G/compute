-- +goose Up
-- ===== Identity =====
CREATE TABLE users (
  id             TEXT PRIMARY KEY,                 -- usr_...
  email          TEXT NOT NULL UNIQUE COLLATE NOCASE,
  username       TEXT NOT NULL UNIQUE,             -- DNS label, used in ingress hostnames
  display_name   TEXT NOT NULL DEFAULT '',
  password_hash  TEXT NOT NULL,                    -- argon2id PHC string
  role           TEXT NOT NULL CHECK (role IN ('admin','user')),
  status         TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  must_change_pw INTEGER NOT NULL DEFAULT 0,
  failed_logins  INTEGER NOT NULL DEFAULT 0,
  locked_until   INTEGER,
  quota_json     TEXT,                             -- NULL = platform default; {"instances":10,"vcpus":16,...}
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  revision       INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE sessions (                            -- browser sessions
  id           TEXT PRIMARY KEY,                   -- ses_...
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash   BLOB NOT NULL UNIQUE,               -- SHA-256(token)
  csrf_hash    BLOB NOT NULL,
  ip           TEXT NOT NULL,
  user_agent   TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL                    -- absolute expiry
);
CREATE INDEX idx_sessions_user ON sessions(user_id);

CREATE TABLE api_keys (
  id           TEXT PRIMARY KEY,                   -- key_...  (public part embedded in token)
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  secret_hash  BLOB NOT NULL,                      -- HMAC-SHA256(pepper, secret)
  prefix       TEXT NOT NULL,                      -- first 8 chars for display "mfk_01J9..."
  scopes       TEXT NOT NULL DEFAULT '["*"]',      -- reserved; v1 always full access
  expires_at   INTEGER,                            -- NULL = never
  last_used_at INTEGER,
  last_used_ip TEXT,
  created_at   INTEGER NOT NULL,
  revoked_at   INTEGER,
  UNIQUE (user_id, name)
);

-- ===== Cluster =====
CREATE TABLE nodes (
  id             TEXT PRIMARY KEY,                 -- nod_...
  name           TEXT NOT NULL UNIQUE,
  role           TEXT NOT NULL CHECK (role IN ('local','remote')),
  status         TEXT NOT NULL CHECK (status IN ('pending','ready','cordoned','draining','offline','removed')),
  underlay_ip    TEXT NOT NULL,
  arch           TEXT NOT NULL,                    -- amd64|arm64
  cert_serial    TEXT,                             -- current mTLS cert serial
  cert_not_after INTEGER,
  total_vcpus    INTEGER NOT NULL,
  total_mem_mb   INTEGER NOT NULL,
  total_disk_mb  INTEGER NOT NULL,
  overcommit_cpu REAL NOT NULL DEFAULT 1.0,
  overcommit_mem REAL NOT NULL DEFAULT 1.0,
  labels_json    TEXT NOT NULL DEFAULT '{}',
  agent_version  TEXT NOT NULL,
  fc_version     TEXT NOT NULL,
  last_heartbeat INTEGER,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);

CREATE TABLE node_join_tokens (
  id           TEXT PRIMARY KEY,                   -- jtk_...
  token_hash   BLOB NOT NULL UNIQUE,
  created_by   TEXT NOT NULL REFERENCES users(id),
  expires_at   INTEGER NOT NULL,                   -- default 1h
  used_at      INTEGER,
  used_by_node TEXT
);

-- ===== Catalog =====
CREATE TABLE sizes (                               -- replaces fixed v1 tiers; admin-managed
  id         TEXT PRIMARY KEY,                     -- 'micro','small','medium','large' (slug)
  vcpus      INTEGER NOT NULL CHECK (vcpus BETWEEN 1 AND 32),
  memory_mb  INTEGER NOT NULL CHECK (memory_mb BETWEEN 128 AND 262144),
  disk_mbps  INTEGER NOT NULL DEFAULT 100,         -- Firecracker drive rate limiter (bandwidth)
  disk_iops  INTEGER NOT NULL DEFAULT 5000,        -- Firecracker drive rate limiter (ops)
  net_mbps   INTEGER NOT NULL DEFAULT 500,         -- Firecracker NIC rate limiter
  sort_order INTEGER NOT NULL DEFAULT 0,
  enabled    INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE kernels (
  id         TEXT PRIMARY KEY,                     -- e.g. 'linux-6.1-fc-2026.10'
  arch       TEXT NOT NULL,
  sha256     TEXT NOT NULL,
  boot_args  TEXT NOT NULL,                        -- base args; platform appends ip=, etc.
  is_default INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE images (                              -- bootable images: VM base images AND cached OCI conversions
  id              TEXT PRIMARY KEY,                -- img_...
  owner_id        TEXT REFERENCES users(id) ON DELETE CASCADE, -- NULL = platform/public
  kind            TEXT NOT NULL CHECK (kind IN ('vm','oci')),
  name            TEXT NOT NULL,                   -- 'ubuntu-24.04' | 'docker.io/library/nginx:1.27'
  os_family       TEXT,                            -- ubuntu|debian|alpine|unknown
  arch            TEXT NOT NULL,
  kernel_id       TEXT REFERENCES kernels(id),
  rootfs_sha256   TEXT,                            -- content address in image store (vm kind)
  oci_digest      TEXT,                            -- sha256:... resolved manifest digest (oci kind)
  oci_config_json TEXT,                            -- entrypoint/cmd/env/workdir/user/exposed ports
  size_bytes      INTEGER NOT NULL DEFAULT 0,
  min_disk_mb     INTEGER NOT NULL DEFAULT 1024,
  status          TEXT NOT NULL CHECK (status IN ('importing','ready','failed','deprecated')),
  description     TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  UNIQUE (owner_id, kind, name)
);

CREATE TABLE image_node_cache (                    -- which node has which image materialized
  image_id  TEXT NOT NULL REFERENCES images(id) ON DELETE CASCADE,
  node_id   TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  status    TEXT NOT NULL CHECK (status IN ('pulling','ready','failed')),
  bytes     INTEGER NOT NULL DEFAULT 0,
  last_used INTEGER NOT NULL,
  PRIMARY KEY (image_id, node_id)
);

CREATE TABLE registry_credentials (                -- private OCI registries, per user
  id         TEXT PRIMARY KEY,                     -- reg_...
  owner_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  registry   TEXT NOT NULL,                        -- 'ghcr.io'
  username   TEXT NOT NULL,
  secret_enc BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE (owner_id, registry)
);

CREATE TABLE templates (                           -- "defined images for serverless VMs": image + size + idle + ports presets
  id          TEXT PRIMARY KEY,                    -- tpl_...
  owner_id    TEXT REFERENCES users(id) ON DELETE CASCADE, -- NULL = platform template
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  spec_json   TEXT NOT NULL,                       -- InstanceSpec subset (see 04 §2)
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  UNIQUE (owner_id, name)
);

-- ===== Networking =====
CREATE TABLE vpcs (
  id         TEXT PRIMARY KEY,                     -- vpc_...
  owner_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  cidr       TEXT NOT NULL,                        -- e.g. 10.20.0.0/24 ; /16../28
  vni        INTEGER NOT NULL UNIQUE,              -- VXLAN network id 4096..16777215
  is_default INTEGER NOT NULL DEFAULT 0,
  egress     TEXT NOT NULL DEFAULT 'nat' CHECK (egress IN ('nat','none')),
  created_at INTEGER NOT NULL,
  revision   INTEGER NOT NULL DEFAULT 1,
  UNIQUE (owner_id, name)
);

-- ===== Compute =====
CREATE TABLE instances (
  id               TEXT PRIMARY KEY,               -- ins_...
  owner_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name             TEXT NOT NULL,
  kind             TEXT NOT NULL CHECK (kind IN ('vm','container')),  -- container = OCI image
  image_id         TEXT NOT NULL REFERENCES images(id),
  template_id      TEXT REFERENCES templates(id) ON DELETE SET NULL,
  vcpus            INTEGER NOT NULL,
  memory_mb        INTEGER NOT NULL,
  node_id          TEXT REFERENCES nodes(id),      -- NULL until scheduled; fixed after (volumes are node-local)
  vpc_id           TEXT NOT NULL REFERENCES vpcs(id),
  desired_state    TEXT NOT NULL CHECK (desired_state IN ('running','stopped','suspended','deleted')),
  state            TEXT NOT NULL,                  -- pending, provisioning, starting, running, stopping, stopped, suspending, suspended, resuming, failed, deleting
  state_reason     TEXT NOT NULL DEFAULT '',
  idle_policy_json TEXT NOT NULL DEFAULT '{"mode":"none"}',
  ports_json       TEXT NOT NULL DEFAULT '[]',     -- [{"port":8080,"protocol":"http","public":true}]
  env_json         TEXT NOT NULL DEFAULT '{}',     -- non-secret env
  cmd_json         TEXT,                           -- override entrypoint/cmd (containers)
  restart_policy   TEXT NOT NULL DEFAULT 'no' CHECK (restart_policy IN ('no','on-failure','always')),
  last_active_at   INTEGER,                        -- for idle accounting
  started_at       INTEGER,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  revision         INTEGER NOT NULL DEFAULT 1,
  UNIQUE (owner_id, name)
);
CREATE INDEX idx_instances_owner_state ON instances(owner_id, state);
CREATE INDEX idx_instances_node ON instances(node_id);

CREATE TABLE ip_allocations (
  vpc_id      TEXT NOT NULL REFERENCES vpcs(id) ON DELETE CASCADE,
  ip          TEXT NOT NULL,
  instance_id TEXT UNIQUE REFERENCES instances(id) ON DELETE CASCADE,
  mac         TEXT NOT NULL UNIQUE,
  PRIMARY KEY (vpc_id, ip)
);

CREATE TABLE instance_secrets (                    -- secret env vars / files, injected via vsock only
  id          TEXT PRIMARY KEY,                    -- sec_...
  instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,                       -- ENV name or file path
  kind        TEXT NOT NULL CHECK (kind IN ('env','file')),
  value_enc   BLOB NOT NULL,
  UNIQUE (instance_id, name)
);

CREATE TABLE user_ssh_keys (                       -- injected into VM-image instances by guest agent
  id          TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  public_key  TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  UNIQUE (user_id, fingerprint)
);

-- ===== Storage =====
CREATE TABLE volumes (
  id               TEXT PRIMARY KEY,               -- vol_...
  owner_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name             TEXT NOT NULL,
  node_id          TEXT REFERENCES nodes(id),      -- NULL until first attach/creation placement
  size_mb          INTEGER NOT NULL,
  kind             TEXT NOT NULL CHECK (kind IN ('root','data')),
  source_image_id  TEXT REFERENCES images(id),     -- root volumes
  source_volume_id TEXT REFERENCES volumes(id),    -- clones
  instance_id      TEXT REFERENCES instances(id) ON DELETE SET NULL, -- current attachment
  device_index     INTEGER,                        -- 0 = root (vda), 1.. = vdb..
  read_only        INTEGER NOT NULL DEFAULT 0,
  status           TEXT NOT NULL CHECK (status IN ('creating','available','attached','deleting','failed')),
  used_mb          INTEGER NOT NULL DEFAULT 0,     -- reported by node (allocated blocks)
  created_at       INTEGER NOT NULL,
  UNIQUE (owner_id, name)
);
CREATE INDEX idx_volumes_instance ON volumes(instance_id);

CREATE TABLE snapshots (                           -- "Saved" memory+state snapshots (suspend) and user-created disk snapshots
  id          TEXT PRIMARY KEY,                    -- snp_...
  owner_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  instance_id TEXT REFERENCES instances(id) ON DELETE CASCADE,
  node_id     TEXT NOT NULL REFERENCES nodes(id),
  kind        TEXT NOT NULL CHECK (kind IN ('suspend','disk')),
  name        TEXT NOT NULL DEFAULT '',
  mem_bytes   INTEGER NOT NULL DEFAULT 0,
  disk_bytes  INTEGER NOT NULL DEFAULT 0,
  status      TEXT NOT NULL CHECK (status IN ('creating','ready','failed')),
  created_at  INTEGER NOT NULL
);

-- ===== Async operations =====
CREATE TABLE operations (
  id              TEXT PRIMARY KEY,                -- op_...
  owner_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind            TEXT NOT NULL,                   -- 'instance.start', 'image.import', ...
  target_id       TEXT NOT NULL,
  status          TEXT NOT NULL CHECK (status IN ('pending','running','succeeded','failed','canceled')),
  progress        INTEGER NOT NULL DEFAULT 0,      -- 0..100
  error_code      TEXT,
  error_msg       TEXT,
  trace_id        TEXT NOT NULL,
  idempotency_key TEXT,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  UNIQUE (owner_id, idempotency_key)
);
CREATE INDEX idx_operations_target ON operations(target_id);

CREATE TABLE settings (                            -- runtime platform settings (admin)
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- Seed default sizes
INSERT INTO sizes (id, vcpus, memory_mb, disk_mbps, disk_iops, net_mbps, sort_order, enabled) VALUES
  ('micro',  1,  512,  50,  2000,  200, 10, 1),
  ('small',  2, 2048, 100,  5000,  500, 20, 1),
  ('medium', 4, 4096, 200, 10000, 1000, 30, 1),
  ('large',  8, 8192, 400, 20000, 2000, 40, 1);

-- +goose Down
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS operations;
DROP TABLE IF EXISTS snapshots;
DROP TABLE IF EXISTS volumes;
DROP TABLE IF EXISTS user_ssh_keys;
DROP TABLE IF EXISTS instance_secrets;
DROP TABLE IF EXISTS ip_allocations;
DROP TABLE IF EXISTS instances;
DROP TABLE IF EXISTS vpcs;
DROP TABLE IF EXISTS templates;
DROP TABLE IF EXISTS registry_credentials;
DROP TABLE IF EXISTS image_node_cache;
DROP TABLE IF EXISTS images;
DROP TABLE IF EXISTS kernels;
DROP TABLE IF EXISTS sizes;
DROP TABLE IF EXISTS node_join_tokens;
DROP TABLE IF EXISTS nodes;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
