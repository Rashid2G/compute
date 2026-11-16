package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// Store handles persistence of VM metadata
type Store struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewStore opens or creates the SQLite database at dbPath
func NewStore(dbPath string) (*Store, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	store := &Store{db: db}
	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return store, nil
}

// Close closes the database handle
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

// SaveWorkerPool persists a pool.
func (s *Store) SaveWorkerPool(ctx context.Context, p *WorkerPool) error {
	now := time.Now().UTC()
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	_, e := s.db.ExecContext(ctx, `INSERT INTO worker_pools VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,labels_json=excluded.labels_json,policy_json=excluded.policy_json,updated_at=excluded.updated_at`, p.ID, p.Name, p.LabelsJSON, p.PolicyJSON, p.CreatedAt, p.UpdatedAt)
	return e
}
func (s *Store) ListWorkerPools(ctx context.Context) ([]*WorkerPool, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT id,name,labels_json,policy_json,created_at,updated_at FROM worker_pools ORDER BY name`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []*WorkerPool{}
	for rows.Next() {
		p := new(WorkerPool)
		if e = rows.Scan(&p.ID, &p.Name, &p.LabelsJSON, &p.PolicyJSON, &p.CreatedAt, &p.UpdatedAt); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) SaveWorkerToken(ctx context.Context, t *WorkerToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	_, e := s.db.ExecContext(ctx, `INSERT INTO worker_tokens(token,name,pool_id,labels_json,expires_at,used_at,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(token) DO UPDATE SET name=excluded.name,pool_id=excluded.pool_id,labels_json=excluded.labels_json,expires_at=excluded.expires_at,used_at=excluded.used_at`, t.Token, t.Name, t.PoolID, t.LabelsJSON, t.ExpiresAt, t.UsedAt, t.CreatedAt)
	return e
}
func (s *Store) ConsumeWorkerToken(ctx context.Context, token string) (*WorkerToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	t := new(WorkerToken)
	e = tx.QueryRowContext(ctx, `SELECT token,name,pool_id,labels_json,expires_at,used_at,created_at FROM worker_tokens WHERE token=?`, token).Scan(&t.Token, &t.Name, &t.PoolID, &t.LabelsJSON, &t.ExpiresAt, &t.UsedAt, &t.CreatedAt)
	if e != nil {
		return nil, e
	}
	if t.UsedAt != nil || time.Now().After(t.ExpiresAt) {
		return nil, fmt.Errorf("token invalid or expired")
	}
	now := time.Now().UTC()
	if _, e = tx.ExecContext(ctx, `UPDATE worker_tokens SET used_at=? WHERE token=? AND used_at IS NULL`, now, token); e != nil {
		return nil, e
	}
	return t, tx.Commit()
}

// ConsumeWorkerTokenAtomic atomically marks a worker token as consumed if it is valid, unconsumed, and not expired.
func (s *Store) ConsumeWorkerTokenAtomic(ctx context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `UPDATE worker_tokens SET used_at = CURRENT_TIMESTAMP WHERE token = ? AND used_at IS NULL AND (expires_at > CURRENT_TIMESTAMP OR datetime(expires_at) > datetime('now'))`, token)
	if err != nil {
		return err
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return fmt.Errorf("worker token already consumed or expired")
	}
	return nil
}

// legacyUnboundTokenName is the placeholder name written by older token
// creation paths when the caller did not choose a name. It is treated as
// unbound so those tokens can still bind to their first worker.
const legacyUnboundTokenName = "worker-token"

// AuthorizeWorkerToken validates a worker enrollment token and allows a
// worker with the same name to reconnect with it. The first connection marks
// the token as used and binds it to the connecting worker name; subsequent
// connections are accepted only when the X-Worker-Name header matches that
// bound name. This keeps single-use enrollment while making worker restarts
// reconnect-safe, including after the enrollment token's expiry (expiry gates
// enrollment, not an already enrolled worker's identity).
func (s *Store) AuthorizeWorkerToken(ctx context.Context, token, workerName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	t := new(WorkerToken)
	err = tx.QueryRowContext(ctx, `SELECT token,name,pool_id,labels_json,expires_at,used_at,created_at FROM worker_tokens WHERE token=?`, token).
		Scan(&t.Token, &t.Name, &t.PoolID, &t.LabelsJSON, &t.ExpiresAt, &t.UsedAt, &t.CreatedAt)
	if err != nil {
		return fmt.Errorf("invalid worker token")
	}
	boundName := strings.TrimSpace(t.Name)
	if boundName == legacyUnboundTokenName {
		boundName = ""
	}
	workerName = strings.TrimSpace(workerName)

	if t.UsedAt != nil {
		// Reconnect: allow only the worker this token was bound to. An
		// unbound token (created without --name, or by an older release)
		// binds to the first worker that reconnects so restarts keep
		// working without inventing a new identity.
		if boundName == "" {
			if workerName == "" {
				return fmt.Errorf("worker token already consumed; worker name required to reconnect")
			}
			if _, err := tx.ExecContext(ctx, `UPDATE worker_tokens SET name=? WHERE token=?`, workerName, token); err != nil {
				return err
			}
			return tx.Commit()
		}
		if workerName == "" || !strings.EqualFold(boundName, workerName) {
			return fmt.Errorf("worker token already consumed by another worker")
		}
		return nil
	}
	if time.Now().After(t.ExpiresAt) {
		return fmt.Errorf("worker token expired")
	}
	bindName := boundName
	if bindName == "" {
		bindName = workerName
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `UPDATE worker_tokens SET used_at=?, name=? WHERE token=? AND used_at IS NULL`, now, bindName, token)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("worker token already consumed by another worker")
	}
	return tx.Commit()
}

// GetWorkerToken retrieves an enrollment token by token value.
func (s *Store) GetWorkerToken(ctx context.Context, token string) (*WorkerToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := new(WorkerToken)
	err := s.db.QueryRowContext(ctx, `SELECT token,name,pool_id,labels_json,expires_at,used_at,created_at FROM worker_tokens WHERE token=?`, token).
		Scan(&t.Token, &t.Name, &t.PoolID, &t.LabelsJSON, &t.ExpiresAt, &t.UsedAt, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ListWorkerTokens returns all enrollment tokens.
func (s *Store) ListWorkerTokens(ctx context.Context) ([]*WorkerToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, e := s.db.QueryContext(ctx, `SELECT token,name,pool_id,labels_json,expires_at,used_at,created_at FROM worker_tokens ORDER BY created_at DESC`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []*WorkerToken{}
	for rows.Next() {
		t := new(WorkerToken)
		if e = rows.Scan(&t.Token, &t.Name, &t.PoolID, &t.LabelsJSON, &t.ExpiresAt, &t.UsedAt, &t.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) DeleteWorkerToken(ctx context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, e := s.db.ExecContext(ctx, `DELETE FROM worker_tokens WHERE token=?`, token)
	return e
}
func (s *Store) SaveJob(ctx context.Context, j *Job) error {
	now := time.Now().UTC()
	if j.ID == "" {
		j.ID = uuid.NewString()
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now
	}
	if j.QueuedAt.IsZero() {
		j.QueuedAt = now
	}
	if j.Source == "" {
		j.Source = "cli"
	}
	j.UpdatedAt = now
	_, e := s.db.ExecContext(ctx, `INSERT INTO jobs (
		id,owner_id,pool_id,type,state,priority,spec_json,result_json,
		repo_id,repo_url,repo_name,branch,commit_sha,source,vm_id,worker_id,
		queued_at,started_at,finished_at,deadline_at,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET
		state=excluded.state,
		result_json=excluded.result_json,
		repo_id=excluded.repo_id,
		repo_url=excluded.repo_url,
		repo_name=excluded.repo_name,
		branch=excluded.branch,
		commit_sha=excluded.commit_sha,
		source=excluded.source,
		vm_id=excluded.vm_id,
		worker_id=excluded.worker_id,
		started_at=excluded.started_at,
		finished_at=excluded.finished_at,
		updated_at=excluded.updated_at`,
		j.ID, j.OwnerID, j.PoolID, j.Type, j.State, j.Priority, j.SpecJSON, j.ResultJSON,
		j.RepoID, j.RepoURL, j.RepoName, j.Branch, j.CommitSHA, j.Source, j.VMID, j.WorkerID,
		j.QueuedAt, j.StartedAt, j.FinishedAt, j.DeadlineAt, j.CreatedAt, j.UpdatedAt)
	return e
}
func (s *Store) ListQueuedJobs(ctx context.Context) ([]*Job, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT id,owner_id,pool_id,type,state,priority,spec_json,result_json,repo_id,repo_url,repo_name,branch,commit_sha,source,vm_id,worker_id,queued_at,started_at,finished_at,deadline_at,created_at,updated_at FROM jobs WHERE state='queued' ORDER BY priority DESC,queued_at`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []*Job{}
	for rows.Next() {
		j := new(Job)
		if e = rows.Scan(&j.ID, &j.OwnerID, &j.PoolID, &j.Type, &j.State, &j.Priority, &j.SpecJSON, &j.ResultJSON, &j.RepoID, &j.RepoURL, &j.RepoName, &j.Branch, &j.CommitSHA, &j.Source, &j.VMID, &j.WorkerID, &j.QueuedAt, &j.StartedAt, &j.FinishedAt, &j.DeadlineAt, &j.CreatedAt, &j.UpdatedAt); e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS vms (
		id TEXT NOT NULL,
		internal_id TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL DEFAULT 'default',
		name TEXT NOT NULL,
		status TEXT NOT NULL,
		desired_running INTEGER NOT NULL DEFAULT 0,
		vcpus INTEGER NOT NULL,
		memory_mb INTEGER NOT NULL,
		disk_mb INTEGER NOT NULL DEFAULT 0,
		ip_address TEXT NOT NULL,
		public_ip TEXT NOT NULL DEFAULT '',
		public_ip_enabled INTEGER NOT NULL DEFAULT 1,
		network_isolation TEXT NOT NULL DEFAULT 'account',
		mac_address TEXT NOT NULL,
		tap_device TEXT NOT NULL,
		tailscale_enabled INTEGER NOT NULL DEFAULT 0,
		tailnet_ip TEXT NOT NULL DEFAULT '',
		tailnet_dns TEXT NOT NULL DEFAULT '',
		tailscale_device_id TEXT NOT NULL DEFAULT '',
		rootfs_path TEXT NOT NULL,
		kernel_path TEXT NOT NULL,
		disk_path TEXT NOT NULL,
		snapshot_path TEXT DEFAULT '',
		mem_file_path TEXT DEFAULT '',
		ssh_key_path TEXT DEFAULT '',
		ssh_key_name TEXT DEFAULT '',
		ssh_public_key TEXT DEFAULT '',
		ssh_password_enabled INTEGER NOT NULL DEFAULT 0,
		pid INTEGER DEFAULT 0,
		socket_path TEXT DEFAULT '',
		protected INTEGER NOT NULL DEFAULT 0,
		description TEXT NOT NULL DEFAULT '',
		pool_id TEXT NOT NULL DEFAULT '',
		worker_id TEXT NOT NULL DEFAULT '',
		last_activity_at TIMESTAMP,
		auto_hibernated INTEGER NOT NULL DEFAULT 0,
		hibernate_reason TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);

	CREATE TABLE IF NOT EXISTS images (
		id TEXT NOT NULL,
		internal_id TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL DEFAULT 'default',
		name TEXT NOT NULL,
		description TEXT DEFAULT '',
		base_image TEXT NOT NULL,
		packages TEXT NOT NULL,
		rootfs_path TEXT NOT NULL,
		kernel_path TEXT NOT NULL,
		size_mb INTEGER NOT NULL,
		status TEXT NOT NULL,
		error_message TEXT DEFAULT '',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);

	CREATE TABLE IF NOT EXISTS templates (
		id TEXT NOT NULL,
		internal_id TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL DEFAULT 'default',
		name TEXT NOT NULL,
		description TEXT DEFAULT '',
		source_vm_id TEXT DEFAULT '',
		disk_path TEXT NOT NULL,
		kernel_path TEXT NOT NULL,
		vcpus INTEGER NOT NULL,
		memory_mb INTEGER NOT NULL,
		disk_mb INTEGER NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);

	CREATE TABLE IF NOT EXISTS ssh_keys (
		id TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL DEFAULT 'default',
		name TEXT NOT NULL,
		public_key TEXT NOT NULL,
		fingerprint TEXT NOT NULL,
		all_sandboxes INTEGER NOT NULL DEFAULT 0,
		is_default INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL
	);
	CREATE TABLE IF NOT EXISTS worker_pools(id TEXT PRIMARY KEY,name TEXT NOT NULL,labels_json TEXT NOT NULL DEFAULT '{}',policy_json TEXT NOT NULL DEFAULT '{}',created_at TIMESTAMP NOT NULL,updated_at TIMESTAMP NOT NULL);
	CREATE TABLE IF NOT EXISTS worker_tokens(token TEXT PRIMARY KEY,name TEXT NOT NULL DEFAULT '',pool_id TEXT NOT NULL,labels_json TEXT NOT NULL DEFAULT '{}',expires_at TIMESTAMP NOT NULL,used_at TIMESTAMP,created_at TIMESTAMP NOT NULL);
	CREATE TABLE IF NOT EXISTS worker_nodes(id TEXT PRIMARY KEY,name TEXT NOT NULL,pool_id TEXT NOT NULL,role TEXT NOT NULL DEFAULT 'worker',status TEXT NOT NULL,underlay_ip TEXT NOT NULL DEFAULT '',arch TEXT NOT NULL DEFAULT '',total_vcpus INTEGER NOT NULL DEFAULT 0,total_mem_mb INTEGER NOT NULL DEFAULT 0,total_disk_mb INTEGER NOT NULL DEFAULT 0,allocated_vcpus INTEGER NOT NULL DEFAULT 0,allocated_mem_mb INTEGER NOT NULL DEFAULT 0,allocated_disk_mb INTEGER NOT NULL DEFAULT 0,physical_vcpus INTEGER NOT NULL DEFAULT 0,physical_mem_mb INTEGER NOT NULL DEFAULT 0,physical_disk_mb INTEGER NOT NULL DEFAULT 0,labels_json TEXT NOT NULL DEFAULT '{}',capabilities_json TEXT NOT NULL DEFAULT '{}',agent_version TEXT NOT NULL DEFAULT '',fc_version TEXT NOT NULL DEFAULT '',last_heartbeat TIMESTAMP,created_at TIMESTAMP NOT NULL,updated_at TIMESTAMP NOT NULL);
	CREATE TABLE IF NOT EXISTS cluster_settings(key TEXT PRIMARY KEY,value_json TEXT NOT NULL,updated_at TIMESTAMP NOT NULL);
	CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,owner_id TEXT NOT NULL DEFAULT '',pool_id TEXT NOT NULL DEFAULT '',type TEXT NOT NULL,state TEXT NOT NULL,priority INTEGER NOT NULL DEFAULT 0,spec_json TEXT NOT NULL,result_json TEXT NOT NULL DEFAULT '',repo_id TEXT NOT NULL DEFAULT '',repo_url TEXT NOT NULL DEFAULT '',repo_name TEXT NOT NULL DEFAULT '',branch TEXT NOT NULL DEFAULT '',commit_sha TEXT NOT NULL DEFAULT '',source TEXT NOT NULL DEFAULT 'cli',vm_id TEXT NOT NULL DEFAULT '',worker_id TEXT NOT NULL DEFAULT '',queued_at TIMESTAMP NOT NULL,started_at TIMESTAMP,finished_at TIMESTAMP,deadline_at TIMESTAMP,created_at TIMESTAMP NOT NULL,updated_at TIMESTAMP NOT NULL);
	CREATE TABLE IF NOT EXISTS github_connections (
		id TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL DEFAULT 'default',
		name TEXT NOT NULL,
		repo_url TEXT NOT NULL,
		default_branch TEXT NOT NULL DEFAULT 'main',
		github_token TEXT NOT NULL DEFAULT '',
		webhook_secret TEXT NOT NULL DEFAULT '',
		default_command TEXT NOT NULL DEFAULT '',
		vcpus INTEGER NOT NULL DEFAULT 2,
		memory_mb INTEGER NOT NULL DEFAULT 2048,
		timeout_sec INTEGER NOT NULL DEFAULT 600,
		status TEXT NOT NULL DEFAULT 'connected',
		last_triggered_at TIMESTAMP,
		created_at TIMESTAMP NOT NULL
	);
	CREATE TABLE IF NOT EXISTS job_attempts(id TEXT PRIMARY KEY,job_id TEXT NOT NULL,worker_id TEXT NOT NULL,state TEXT NOT NULL,created_at TIMESTAMP NOT NULL);
	CREATE TABLE IF NOT EXISTS worker_leases(id TEXT PRIMARY KEY,job_id TEXT NOT NULL,attempt_id TEXT NOT NULL,worker_id TEXT NOT NULL,expires_at TIMESTAMP NOT NULL,state TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS job_logs(id TEXT PRIMARY KEY,job_id TEXT NOT NULL,attempt_id TEXT NOT NULL,stream TEXT NOT NULL,seq INTEGER NOT NULL,data TEXT NOT NULL,created_at TIMESTAMP NOT NULL);

	CREATE TABLE IF NOT EXISTS volumes (
		id TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL DEFAULT 'default',
		name TEXT NOT NULL,
		pool TEXT NOT NULL DEFAULT 'default',
		size_gb INTEGER NOT NULL,
		used_gb REAL NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'available',
		attached_vm TEXT NOT NULL DEFAULT '',
		device_path TEXT NOT NULL DEFAULT '',
		mount_point TEXT NOT NULL DEFAULT '',
		filesystem TEXT NOT NULL DEFAULT 'ext4',
		iops INTEGER NOT NULL DEFAULT 0,
		throughput TEXT NOT NULL DEFAULT '',
		snapshots INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);

	CREATE TABLE IF NOT EXISTS webhooks (
		id TEXT PRIMARY KEY,
		url TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		secret TEXT NOT NULL DEFAULT '',
		events TEXT NOT NULL DEFAULT '[]',
		active INTEGER NOT NULL DEFAULT 1,
		last_delivery TEXT NOT NULL DEFAULT '',
		success_rate TEXT NOT NULL DEFAULT '100%',
		deliveries_count INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);

	CREATE TABLE IF NOT EXISTS otel_config (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		enabled INTEGER NOT NULL DEFAULT 0,
		endpoint TEXT NOT NULL DEFAULT '',
		protocol TEXT NOT NULL DEFAULT 'http/protobuf',
		auth_header TEXT NOT NULL DEFAULT '',
		service_name TEXT NOT NULL DEFAULT 'on-demand-vms',
		sample_rate TEXT NOT NULL DEFAULT '1.0',
		export_interval TEXT NOT NULL DEFAULT '10s',
		metrics_enabled INTEGER NOT NULL DEFAULT 1,
		traces_enabled INTEGER NOT NULL DEFAULT 1,
		logs_enabled INTEGER NOT NULL DEFAULT 1,
		spans_exported INTEGER NOT NULL DEFAULT 0,
		last_export_at TEXT NOT NULL DEFAULT ''
	);

	CREATE TABLE IF NOT EXISTS audit_events (
		id TEXT PRIMARY KEY,
		timestamp TIMESTAMP NOT NULL,
		event TEXT NOT NULL,
		actor TEXT NOT NULL,
		target TEXT NOT NULL,
		status TEXT NOT NULL,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		payload TEXT NOT NULL DEFAULT '{}'
	);

	CREATE TABLE IF NOT EXISTS access_api_keys (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		prefix TEXT NOT NULL,
		token TEXT NOT NULL DEFAULT '',
		scope TEXT NOT NULL DEFAULT '',
		last_used TEXT NOT NULL DEFAULT 'Never',
		expires_at TEXT NOT NULL DEFAULT '',
		created_by TEXT NOT NULL DEFAULT 'admin',
		created_at TIMESTAMP NOT NULL
	);

	CREATE TABLE IF NOT EXISTS agent_profiles (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		provider TEXT NOT NULL,
		upstream_url TEXT NOT NULL DEFAULT '',
		inject_header TEXT NOT NULL DEFAULT '',
		secret_ref TEXT NOT NULL DEFAULT '',
		sandboxes_linked INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'active',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);

	CREATE TABLE IF NOT EXISTS environments (
		name TEXT PRIMARY KEY,
		is_default INTEGER NOT NULL DEFAULT 0,
		safe_for_third_party INTEGER NOT NULL DEFAULT 0,
		github_repos_enabled INTEGER NOT NULL DEFAULT 0,
		github_repos TEXT NOT NULL DEFAULT '[]',
		secrets_enabled INTEGER NOT NULL DEFAULT 0,
		secrets TEXT NOT NULL DEFAULT '',
		boat_creds_enabled INTEGER NOT NULL DEFAULT 0,
		agent_creds_enabled INTEGER NOT NULL DEFAULT 0,
		credential_profile TEXT NOT NULL DEFAULT '',
		version INTEGER NOT NULL DEFAULT 1,
		updated_at TIMESTAMP NOT NULL
	);

	CREATE TABLE IF NOT EXISTS port_forwards (
		id TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL DEFAULT 'default',
		host_port INTEGER NOT NULL,
		vm_id TEXT NOT NULL,
		vm_name TEXT NOT NULL DEFAULT '',
		guest_port INTEGER NOT NULL,
		protocol TEXT NOT NULL DEFAULT 'tcp',
		subdomain TEXT NOT NULL DEFAULT '',
		public_url TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'active',
		public INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL
	);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	_, _ = s.db.Exec("ALTER TABLE vms ADD COLUMN ssh_key_name TEXT DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE vms ADD COLUMN ssh_public_key TEXT DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE volumes ADD COLUMN path TEXT NOT NULL DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE vms ADD COLUMN desired_running INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE vms ADD COLUMN disk_mb INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE worker_tokens ADD COLUMN name TEXT DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE ssh_keys ADD COLUMN all_sandboxes INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE ssh_keys ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE vms ADD COLUMN description TEXT NOT NULL DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE jobs ADD COLUMN repo_id TEXT NOT NULL DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE jobs ADD COLUMN repo_url TEXT NOT NULL DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE jobs ADD COLUMN repo_name TEXT NOT NULL DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE jobs ADD COLUMN branch TEXT NOT NULL DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE jobs ADD COLUMN commit_sha TEXT NOT NULL DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE jobs ADD COLUMN source TEXT NOT NULL DEFAULT 'cli'")
	_, _ = s.db.Exec("ALTER TABLE jobs ADD COLUMN vm_id TEXT NOT NULL DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE jobs ADD COLUMN worker_id TEXT NOT NULL DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE vms ADD COLUMN pool_id TEXT NOT NULL DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE vms ADD COLUMN worker_id TEXT NOT NULL DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE vms ADD COLUMN last_activity_at TIMESTAMP")
	_, _ = s.db.Exec("ALTER TABLE vms ADD COLUMN auto_hibernated INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE vms ADD COLUMN hibernate_reason TEXT NOT NULL DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE worker_nodes ADD COLUMN allocated_vcpus INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE worker_nodes ADD COLUMN allocated_mem_mb INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE worker_nodes ADD COLUMN allocated_disk_mb INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE worker_nodes ADD COLUMN physical_vcpus INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE worker_nodes ADD COLUMN physical_mem_mb INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE worker_nodes ADD COLUMN physical_disk_mb INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE worker_nodes ADD COLUMN used_vcpus INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE worker_nodes ADD COLUMN used_mem_mb INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE worker_nodes ADD COLUMN used_disk_mb INTEGER NOT NULL DEFAULT 0")
	_, _ = s.db.Exec("ALTER TABLE worker_nodes ADD COLUMN running_vms INTEGER NOT NULL DEFAULT 0")

	// Migrate existing tables to ensure internal_id, owner_id, public_ip, public_ip_enabled, network_isolation, and protected columns exist
	_ = s.migrateTableColumns("vms", []string{"internal_id", "owner_id", "public_ip", "public_ip_enabled", "network_isolation", "protected", "tailscale_enabled", "tailnet_ip", "tailnet_dns", "tailscale_device_id", "ssh_password_enabled"})
	_ = s.migrateTableColumns("images", []string{"internal_id", "owner_id"})
	_ = s.migrateTableColumns("templates", []string{"internal_id", "owner_id"})
	_ = s.migrateTableColumns("volumes", []string{"owner_id"})
	_ = s.migrateTableColumns("port_forwards", []string{"owner_id", "public"})

	// Migrate idx_port_forwards_host_port to a partial unique index (WHERE host_port > 0)
	// so ingress-only port exposures (host_port = 0) do not collide on host_port.
	var pfIdxSQL string
	_ = s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_port_forwards_host_port'`).Scan(&pfIdxSQL)
	if pfIdxSQL != "" && !strings.Contains(strings.ToLower(pfIdxSQL), "where") {
		_, _ = s.db.Exec(`DROP INDEX IF EXISTS idx_port_forwards_host_port`)
	}

	indexes := `
	CREATE INDEX IF NOT EXISTS idx_vms_internal_id ON vms(internal_id);
	CREATE INDEX IF NOT EXISTS idx_vms_id ON vms(id);
	CREATE INDEX IF NOT EXISTS idx_vms_name ON vms(name);
	CREATE INDEX IF NOT EXISTS idx_vms_owner ON vms(owner_id);
	CREATE INDEX IF NOT EXISTS idx_vms_owner_name ON vms(owner_id, name);
	CREATE INDEX IF NOT EXISTS idx_vms_status ON vms(status);
	CREATE INDEX IF NOT EXISTS idx_vms_ip ON vms(ip_address);
	CREATE INDEX IF NOT EXISTS idx_vms_public_ip ON vms(public_ip);
	CREATE INDEX IF NOT EXISTS idx_vms_protected ON vms(protected);

	CREATE TRIGGER IF NOT EXISTS trg_prevent_protected_vm_delete
	BEFORE DELETE ON vms
	FOR EACH ROW
	WHEN OLD.protected = 1
	BEGIN
		SELECT RAISE(ABORT, 'item is protected against deletion');
	END;

	CREATE INDEX IF NOT EXISTS idx_images_internal_id ON images(internal_id);
	CREATE INDEX IF NOT EXISTS idx_images_id ON images(id);
	CREATE INDEX IF NOT EXISTS idx_images_name ON images(name);
	CREATE INDEX IF NOT EXISTS idx_images_owner ON images(owner_id);

	CREATE INDEX IF NOT EXISTS idx_templates_internal_id ON templates(internal_id);
	CREATE INDEX IF NOT EXISTS idx_templates_id ON templates(id);
	CREATE INDEX IF NOT EXISTS idx_templates_name ON templates(name);
	CREATE INDEX IF NOT EXISTS idx_templates_owner ON templates(owner_id);
	CREATE INDEX IF NOT EXISTS idx_jobs_queue ON jobs(state,priority,queued_at);
	CREATE INDEX IF NOT EXISTS idx_github_connections_owner ON github_connections(owner_id);
	CREATE INDEX IF NOT EXISTS idx_github_connections_repo_url ON github_connections(repo_url);
	CREATE INDEX IF NOT EXISTS idx_workers_pool_status ON worker_nodes(pool_id,status);
	CREATE INDEX IF NOT EXISTS idx_job_logs_job_seq ON job_logs(job_id,seq);
	CREATE INDEX IF NOT EXISTS idx_volumes_status ON volumes(status);
	CREATE INDEX IF NOT EXISTS idx_volumes_owner ON volumes(owner_id);
	CREATE INDEX IF NOT EXISTS idx_audit_events_ts ON audit_events(timestamp DESC);
	CREATE INDEX IF NOT EXISTS idx_port_forwards_vm ON port_forwards(vm_id);
	CREATE INDEX IF NOT EXISTS idx_port_forwards_owner ON port_forwards(owner_id);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_port_forwards_host_port ON port_forwards(host_port) WHERE host_port > 0;
	`
	_, err := s.db.Exec(indexes)
	return err
}

func (s *Store) migrateTableColumns(tableName string, cols []string) error {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s);", tableName))
	if err != nil {
		return err
	}
	existing := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err == nil {
			existing[name] = true
		}
	}
	rows.Close()

	for _, col := range cols {
		if !existing[col] {
			if col == "owner_id" {
				_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s TEXT NOT NULL DEFAULT 'default';", tableName, col))
			} else if col == "network_isolation" {
				_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s TEXT NOT NULL DEFAULT 'account';", tableName, col))
			} else if col == "public_ip_enabled" {
				_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s INTEGER NOT NULL DEFAULT 1;", tableName, col))
			} else if col == "protected" {
				_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s INTEGER NOT NULL DEFAULT 0;", tableName, col))
			} else if col == "public" {
				_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s INTEGER NOT NULL DEFAULT 0;", tableName, col))
			} else if col == "tailscale_enabled" || col == "ssh_password_enabled" {
				_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s INTEGER NOT NULL DEFAULT 0;", tableName, col))
			} else if col == "tailnet_ip" || col == "tailnet_dns" || col == "tailscale_device_id" {
				// Address columns start empty; never backfill from the id.
				_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s TEXT NOT NULL DEFAULT '';", tableName, col))
			} else if col == "public_ip" {
				_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s TEXT NOT NULL DEFAULT '';", tableName, col))
				_, _ = s.db.Exec(fmt.Sprintf("UPDATE %s SET %s = ip_address WHERE %s = '' OR %s IS NULL;", tableName, col, col, col))
			} else {
				_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s TEXT NOT NULL DEFAULT '';", tableName, col))
				_, _ = s.db.Exec(fmt.Sprintf("UPDATE %s SET %s = id WHERE %s = '' OR %s IS NULL;", tableName, col, col, col))
			}
		}
	}
	return nil
}

// SaveVM inserts or updates a VM record
func (s *Store) SaveVM(ctx context.Context, vm *VM) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if vm.CreatedAt.IsZero() {
		vm.CreatedAt = now
	}
	vm.UpdatedAt = now

	if vm.InternalID == "" {
		if vm.ID != "" && strings.HasPrefix(vm.ID, "ins_") {
			vm.InternalID = vm.ID
		} else {
			uid := uuid.New().String()
			cleanU := strings.ReplaceAll(uid, "-", "")
			vm.InternalID = fmt.Sprintf("ins_%s", cleanU[:16])
		}
	}
	if vm.ID == "" {
		if vm.Name != "" {
			vm.ID = vm.Name
		} else {
			vm.ID = vm.InternalID
		}
	}
	if vm.Name == "" {
		vm.Name = vm.ID
	}
	if vm.OwnerID == "" {
		vm.OwnerID = "default"
	}

	pubEnabledInt := 0
	if vm.PublicIPEnabled {
		pubEnabledInt = 1
	}
	netIsolation, ok := NormalizeNetworkIsolation(vm.NetworkIsolation)
	if !ok {
		netIsolation = NetworkIsolationAccount
	}
	vm.NetworkIsolation = netIsolation

	updateQuery := `
	UPDATE vms SET
		id = ?, name = ?, status = ?, desired_running = ?, vcpus = ?, memory_mb = ?, disk_mb = ?,
		ip_address = ?, public_ip = ?, public_ip_enabled = ?, network_isolation = ?,
		mac_address = ?, tap_device = ?, tailscale_enabled = ?, tailnet_ip = ?, tailnet_dns = ?, tailscale_device_id = ?,
		rootfs_path = ?,
		kernel_path = ?, disk_path = ?, snapshot_path = ?, mem_file_path = ?,
		ssh_key_path = ?, ssh_key_name = ?, ssh_public_key = ?, ssh_password_enabled = ?, pid = ?, socket_path = ?, protected = ?, description = ?,
		pool_id = ?, worker_id = ?, last_activity_at = ?, auto_hibernated = ?, hibernate_reason = ?, owner_id = ?, updated_at = ?
	WHERE internal_id = ? OR (owner_id = ? AND (id = ? OR name = ?));
	`
	res, err := s.db.ExecContext(ctx, updateQuery,
		vm.ID, vm.Name, string(vm.Status), boolInt(vm.DesiredRunning), vm.VCPUs, vm.MemoryMB, vm.DiskMB,
		vm.IPAddress, vm.PublicIP, pubEnabledInt, netIsolation,
		vm.MACAddress, vm.TAPDevice, boolInt(vm.TailscaleEnabled), vm.TailnetIP, vm.TailnetDNS, vm.TailscaleDeviceID,
		vm.RootfsPath,
		vm.KernelPath, vm.DiskPath, vm.SnapshotPath, vm.MemFilePath,
		vm.SSHKeyPath, vm.SSHKeyName, vm.SSHPublicKey, boolInt(vm.SSHPasswordEnabled), vm.PID, vm.SocketPath, boolInt(vm.Protected), vm.Description,
		vm.PoolID, vm.WorkerID, vm.LastActivityAt, boolInt(vm.AutoHibernated), vm.HibernateReason, vm.OwnerID, vm.UpdatedAt,
		vm.InternalID, vm.OwnerID, vm.ID, vm.Name,
	)
	if err != nil {
		return err
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected > 0 {
		return nil
	}

	insertQuery := `
	INSERT INTO vms (
		id, internal_id, owner_id, name, status, desired_running, vcpus, memory_mb, disk_mb,
		ip_address, public_ip, public_ip_enabled, network_isolation, mac_address, tap_device,
		tailscale_enabled, tailnet_ip, tailnet_dns, tailscale_device_id,
		rootfs_path, kernel_path,
		disk_path, snapshot_path, mem_file_path, ssh_key_path, ssh_key_name, ssh_public_key, ssh_password_enabled,
		pid, socket_path, protected, description, pool_id, worker_id, last_activity_at, auto_hibernated, hibernate_reason, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err = s.db.ExecContext(ctx, insertQuery,
		vm.ID, vm.InternalID, vm.OwnerID, vm.Name, string(vm.Status), boolInt(vm.DesiredRunning), vm.VCPUs, vm.MemoryMB, vm.DiskMB,
		vm.IPAddress, vm.PublicIP, pubEnabledInt, netIsolation, vm.MACAddress, vm.TAPDevice,
		boolInt(vm.TailscaleEnabled), vm.TailnetIP, vm.TailnetDNS, vm.TailscaleDeviceID,
		vm.RootfsPath,
		vm.KernelPath, vm.DiskPath, vm.SnapshotPath, vm.MemFilePath,
		vm.SSHKeyPath, vm.SSHKeyName, vm.SSHPublicKey, boolInt(vm.SSHPasswordEnabled), vm.PID, vm.SocketPath, boolInt(vm.Protected), vm.Description,
		vm.PoolID, vm.WorkerID, vm.LastActivityAt, boolInt(vm.AutoHibernated), vm.HibernateReason, vm.CreatedAt, vm.UpdatedAt,
	)
	return err
}

// TouchVMActivity updates last_activity_at for an active VM.
func (s *Store) TouchVMActivity(ctx context.Context, idOrInternalID string) error {
	if strings.TrimSpace(idOrInternalID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE vms SET last_activity_at = ?, updated_at = ? WHERE internal_id = ? OR id = ? OR name = ?`, now, now, idOrInternalID, idOrInternalID, idOrInternalID)
	return err
}

// GetVM retrieves a single VM by internal ID, external ID, or Name
func (s *Store) GetVM(ctx context.Context, idOrName string) (*VM, error) {
	return s.GetVMByOwner(ctx, "", idOrName)
}

// GetVMByOwner retrieves a single VM for a specific owner by internal ID, external ID, Name, IPAddress, or PublicIP
func (s *Store) GetVMByOwner(ctx context.Context, ownerID, idOrName string) (*VM, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, internal_id, owner_id, name, status, desired_running, vcpus, memory_mb, disk_mb, ip_address, public_ip, public_ip_enabled, network_isolation, mac_address, tap_device, tailscale_enabled, tailnet_ip, tailnet_dns, tailscale_device_id,
	       rootfs_path, kernel_path, disk_path, snapshot_path, mem_file_path, ssh_key_path, ssh_key_name, ssh_public_key, ssh_password_enabled,
	       pid, socket_path, protected, description, pool_id, worker_id, last_activity_at, auto_hibernated, hibernate_reason, created_at, updated_at
	FROM vms
	WHERE (internal_id = ? OR id = ? OR name = ? OR ip_address = ? OR public_ip = ?)
	  AND (? = '' OR owner_id = ? OR owner_id = '')
	ORDER BY CASE
		WHEN internal_id = ? THEN 1
		WHEN id = ? THEN 2
		WHEN name = ? THEN 3
		WHEN public_ip = ? AND status = 'running' THEN 4
		WHEN ip_address = ? AND status = 'running' THEN 5
		WHEN public_ip = ? THEN 6
		WHEN ip_address = ? THEN 7
		ELSE 8
	END, updated_at DESC
	LIMIT 1;
	`

	row := s.db.QueryRowContext(ctx, query, idOrName, idOrName, idOrName, idOrName, idOrName, ownerID, ownerID, idOrName, idOrName, idOrName, idOrName, idOrName, idOrName, idOrName)
	vm := &VM{}
	var status string
	var pubEnabledInt int
	var desiredRunning int
	var protectedInt int
	var tsEnabledInt int
	var sshPasswordEnabledInt int
	var autoHibernatedInt int
	err := row.Scan(
		&vm.ID, &vm.InternalID, &vm.OwnerID, &vm.Name, &status, &desiredRunning, &vm.VCPUs, &vm.MemoryMB, &vm.DiskMB,
		&vm.IPAddress, &vm.PublicIP, &pubEnabledInt, &vm.NetworkIsolation, &vm.MACAddress, &vm.TAPDevice,
		&tsEnabledInt, &vm.TailnetIP, &vm.TailnetDNS, &vm.TailscaleDeviceID, &vm.RootfsPath,
		&vm.KernelPath, &vm.DiskPath, &vm.SnapshotPath, &vm.MemFilePath,
		&vm.SSHKeyPath, &vm.SSHKeyName, &vm.SSHPublicKey, &sshPasswordEnabledInt, &vm.PID, &vm.SocketPath, &protectedInt, &vm.Description,
		&vm.PoolID, &vm.WorkerID, &vm.LastActivityAt, &autoHibernatedInt, &vm.HibernateReason, &vm.CreatedAt, &vm.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	vm.Status = VMStatus(status)
	vm.DesiredRunning = desiredRunning != 0
	vm.PublicIPEnabled = pubEnabledInt != 0
	if norm, ok := NormalizeNetworkIsolation(vm.NetworkIsolation); ok {
		vm.NetworkIsolation = norm
	} else {
		vm.NetworkIsolation = NetworkIsolationAccount
	}
	vm.Protected = protectedInt != 0
	vm.TailscaleEnabled = tsEnabledInt != 0
	vm.SSHPasswordEnabled = sshPasswordEnabledInt != 0
	vm.AutoHibernated = autoHibernatedInt != 0
	if vm.PublicIPEnabled && vm.PublicIP == "" && vm.IPAddress != "" {
		vm.PublicIP = vm.IPAddress
	}
	return vm, nil
}

// ListVMs returns all stored VMs
func (s *Store) ListVMs(ctx context.Context) ([]*VM, error) {
	return s.ListVMsByOwner(ctx, "")
}

// ListVMsByOwner returns all stored VMs for a given owner
func (s *Store) ListVMsByOwner(ctx context.Context, ownerID string) ([]*VM, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, internal_id, owner_id, name, status, desired_running, vcpus, memory_mb, disk_mb, ip_address, public_ip, public_ip_enabled, network_isolation, mac_address, tap_device, tailscale_enabled, tailnet_ip, tailnet_dns, tailscale_device_id,
	       rootfs_path, kernel_path, disk_path, snapshot_path, mem_file_path, ssh_key_path, ssh_key_name, ssh_public_key, ssh_password_enabled,
	       pid, socket_path, protected, description, pool_id, worker_id, last_activity_at, auto_hibernated, hibernate_reason, created_at, updated_at
	FROM vms
	WHERE (? = '' OR owner_id = ?)
	ORDER BY created_at DESC;
	`

	rows, err := s.db.QueryContext(ctx, query, ownerID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vms []*VM
	for rows.Next() {
		vm := &VM{}
		var status string
		var pubEnabledInt int
		var desiredRunning int
		var protectedInt int
		var tsEnabledInt int
		var sshPasswordEnabledInt int
		var autoHibernatedInt int
		err := rows.Scan(
			&vm.ID, &vm.InternalID, &vm.OwnerID, &vm.Name, &status, &desiredRunning, &vm.VCPUs, &vm.MemoryMB, &vm.DiskMB,
			&vm.IPAddress, &vm.PublicIP, &pubEnabledInt, &vm.NetworkIsolation, &vm.MACAddress, &vm.TAPDevice,
			&tsEnabledInt, &vm.TailnetIP, &vm.TailnetDNS, &vm.TailscaleDeviceID, &vm.RootfsPath,
			&vm.KernelPath, &vm.DiskPath, &vm.SnapshotPath, &vm.MemFilePath,
			&vm.SSHKeyPath, &vm.SSHKeyName, &vm.SSHPublicKey, &sshPasswordEnabledInt, &vm.PID, &vm.SocketPath, &protectedInt, &vm.Description,
			&vm.PoolID, &vm.WorkerID, &vm.LastActivityAt, &autoHibernatedInt, &vm.HibernateReason, &vm.CreatedAt, &vm.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		vm.Status = VMStatus(status)
		vm.DesiredRunning = desiredRunning != 0
		vm.PublicIPEnabled = pubEnabledInt != 0
		if norm, ok := NormalizeNetworkIsolation(vm.NetworkIsolation); ok {
			vm.NetworkIsolation = norm
		} else {
			vm.NetworkIsolation = NetworkIsolationAccount
		}
		vm.Protected = protectedInt != 0
		vm.TailscaleEnabled = tsEnabledInt != 0
		vm.SSHPasswordEnabled = sshPasswordEnabledInt != 0
		vm.AutoHibernated = autoHibernatedInt != 0
		if vm.PublicIPEnabled && vm.PublicIP == "" && vm.IPAddress != "" {
			vm.PublicIP = vm.IPAddress
		}
		vms = append(vms, vm)
	}
	return vms, rows.Err()
}

// DeleteVM removes a VM record by internal ID, external ID, or Name
func (s *Store) DeleteVM(ctx context.Context, idOrName string) error {
	return s.DeleteVMByOwner(ctx, "", idOrName)
}

// DeleteVMByOwner removes a VM record for a specific owner by internal ID, external ID, or Name.
// If the VM has deletion protection enabled, it is never removable.
func (s *Store) DeleteVMByOwner(ctx context.Context, ownerID, idOrName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var name string
	var protected int
	checkQuery := `SELECT name, protected FROM vms WHERE (internal_id = ? OR id = ? OR name = ?) AND (? = '' OR owner_id = ?) LIMIT 1;`
	if err := s.db.QueryRowContext(ctx, checkQuery, idOrName, idOrName, idOrName, ownerID, ownerID).Scan(&name, &protected); err == nil && protected != 0 {
		displayName := name
		if displayName == "" {
			displayName = idOrName
		}
		return fmt.Errorf("item %s is protected against deletion", displayName)
	}

	query := `DELETE FROM vms WHERE (internal_id = ? OR id = ? OR name = ?) AND (? = '' OR owner_id = ?);`
	_, err := s.db.ExecContext(ctx, query, idOrName, idOrName, idOrName, ownerID, ownerID)
	return err
}

// DeleteVMByInternalID removes exactly one VM row by its globally unique internal ID.
// If the VM has deletion protection enabled, it is never removable.
func (s *Store) DeleteVMByInternalID(ctx context.Context, internalID string) error {
	if strings.TrimSpace(internalID) == "" {
		return fmt.Errorf("internal vm id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var name string
	var protected int
	checkQuery := `SELECT name, protected FROM vms WHERE internal_id = ? LIMIT 1;`
	if err := s.db.QueryRowContext(ctx, checkQuery, internalID).Scan(&name, &protected); err == nil && protected != 0 {
		displayName := name
		if displayName == "" {
			displayName = internalID
		}
		return fmt.Errorf("item %s is protected against deletion", displayName)
	}

	_, err := s.db.ExecContext(ctx, `DELETE FROM vms WHERE internal_id = ?;`, internalID)
	return err
}

// GetAllocatedIPs returns a set of all currently assigned IPs
func (s *Store) GetAllocatedIPs(ctx context.Context) (map[string]bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, "SELECT ip_address FROM vms WHERE ip_address != ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	allocated := make(map[string]bool)
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err == nil && ip != "" {
			allocated[ip] = true
		}
	}
	return allocated, rows.Err()
}

// GetNetworkAllocations returns structured allocation information for all VMs with an IP
func (s *Store) GetNetworkAllocations(ctx context.Context) ([]*NetworkAllocation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, name, owner_id, status, ip_address, public_ip, public_ip_enabled, network_isolation, mac_address, tap_device, updated_at
	FROM vms WHERE ip_address != '' ORDER BY ip_address ASC;
	`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var allocs []*NetworkAllocation
	for rows.Next() {
		var a NetworkAllocation
		var status string
		var pubEnabledInt int
		if err := rows.Scan(&a.VMID, &a.VMName, &a.OwnerID, &status, &a.IPAddress, &a.PublicIP, &pubEnabledInt, &a.NetworkIsolation, &a.MACAddress, &a.TAPDevice, &a.UpdatedAt); err == nil {
			a.Status = VMStatus(status)
			a.PublicIPEnabled = pubEnabledInt != 0
			if norm, ok := NormalizeNetworkIsolation(a.NetworkIsolation); ok {
				a.NetworkIsolation = norm
			} else {
				a.NetworkIsolation = NetworkIsolationAccount
			}
			if a.PublicIPEnabled && a.PublicIP == "" && a.IPAddress != "" {
				a.PublicIP = a.IPAddress
			}
			allocs = append(allocs, &a)
		}
	}
	return allocs, rows.Err()
}

// GetNetworkAllocation returns structured allocation information for a specific VM by TAP device, ID, or name
func (s *Store) GetNetworkAllocation(ctx context.Context, tapOrID string) (*NetworkAllocation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, name, owner_id, status, ip_address, public_ip, public_ip_enabled, network_isolation, mac_address, tap_device, updated_at
	FROM vms WHERE tap_device = ? OR id = ? OR name = ? LIMIT 1;
	`
	row := s.db.QueryRowContext(ctx, query, tapOrID, tapOrID, tapOrID)
	var a NetworkAllocation
	var status string
	var pubEnabledInt int
	err := row.Scan(&a.VMID, &a.VMName, &a.OwnerID, &status, &a.IPAddress, &a.PublicIP, &pubEnabledInt, &a.NetworkIsolation, &a.MACAddress, &a.TAPDevice, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.Status = VMStatus(status)
	a.PublicIPEnabled = pubEnabledInt != 0
	if norm, ok := NormalizeNetworkIsolation(a.NetworkIsolation); ok {
		a.NetworkIsolation = norm
	} else {
		a.NetworkIsolation = NetworkIsolationAccount
	}
	if a.PublicIPEnabled && a.PublicIP == "" && a.IPAddress != "" {
		a.PublicIP = a.IPAddress
	}
	return &a, nil
}

// SaveImage inserts or updates a CustomImage record
func (s *Store) SaveImage(ctx context.Context, img *CustomImage) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if img.CreatedAt.IsZero() {
		img.CreatedAt = now
	}
	img.UpdatedAt = now

	if img.InternalID == "" {
		cleanU := strings.ReplaceAll(uuid.New().String(), "-", "")
		img.InternalID = fmt.Sprintf("img_%s", cleanU[:16])
	}
	if img.ID == "" {
		if img.Name != "" {
			img.ID = img.Name
		} else {
			img.ID = img.InternalID
		}
	}
	if img.Name == "" {
		img.Name = img.ID
	}
	if img.OwnerID == "" {
		img.OwnerID = "default"
	}

	pkgsJSON, err := json.Marshal(img.Packages)
	if err != nil {
		pkgsJSON = []byte("[]")
	}

	updateQuery := `
	UPDATE images SET
		id = ?, name = ?, description = ?, base_image = ?, packages = ?,
		rootfs_path = ?, kernel_path = ?, size_mb = ?, status = ?,
		error_message = ?, owner_id = ?, updated_at = ?
	WHERE internal_id = ? OR (owner_id = ? AND (id = ? OR name = ?));
	`
	res, err := s.db.ExecContext(ctx, updateQuery,
		img.ID, img.Name, img.Description, img.BaseImage, string(pkgsJSON),
		img.RootfsPath, img.KernelPath, img.SizeMB, string(img.Status),
		img.ErrorMessage, img.OwnerID, img.UpdatedAt,
		img.InternalID, img.OwnerID, img.ID, img.Name,
	)
	if err != nil {
		return err
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected > 0 {
		return nil
	}

	insertQuery := `
	INSERT INTO images (
		id, internal_id, owner_id, name, description, base_image, packages,
		rootfs_path, kernel_path, size_mb, status, error_message, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err = s.db.ExecContext(ctx, insertQuery,
		img.ID, img.InternalID, img.OwnerID, img.Name, img.Description, img.BaseImage, string(pkgsJSON),
		img.RootfsPath, img.KernelPath, img.SizeMB, string(img.Status),
		img.ErrorMessage, img.CreatedAt, img.UpdatedAt,
	)
	return err
}

// GetImage retrieves a custom image by internal ID, external ID, or Name
func (s *Store) GetImage(ctx context.Context, idOrName string) (*CustomImage, error) {
	return s.GetImageByOwner(ctx, "", idOrName)
}

// GetImageByOwner retrieves a custom image scoped to an owner. Duplicate image
// names are allowed across tenants, so owner-scoped lookups are required.
func (s *Store) GetImageByOwner(ctx context.Context, ownerID, idOrName string) (*CustomImage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, internal_id, owner_id, name, description, base_image, packages, rootfs_path, kernel_path,
	       size_mb, status, error_message, created_at, updated_at
	FROM images
	WHERE (internal_id = ? OR id = ? OR name = ?)
	  AND (? = '' OR owner_id = ? OR owner_id = '')
	ORDER BY CASE WHEN internal_id = ? THEN 1 WHEN id = ? THEN 2 ELSE 3 END
	LIMIT 1;
	`

	row := s.db.QueryRowContext(ctx, query, idOrName, idOrName, idOrName, ownerID, ownerID, idOrName, idOrName)
	img := &CustomImage{}
	var pkgsStr, statusStr string
	err := row.Scan(
		&img.ID, &img.InternalID, &img.OwnerID, &img.Name, &img.Description, &img.BaseImage, &pkgsStr,
		&img.RootfsPath, &img.KernelPath, &img.SizeMB, &statusStr,
		&img.ErrorMessage, &img.CreatedAt, &img.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	img.Status = ImageStatus(statusStr)
	_ = json.Unmarshal([]byte(pkgsStr), &img.Packages)
	if img.Packages == nil {
		img.Packages = []string{}
	}
	return img, nil
}

// ListImages returns all custom images
func (s *Store) ListImages(ctx context.Context) ([]*CustomImage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, internal_id, owner_id, name, description, base_image, packages, rootfs_path, kernel_path,
	       size_mb, status, error_message, created_at, updated_at
	FROM images ORDER BY created_at DESC;
	`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var images []*CustomImage
	for rows.Next() {
		img := &CustomImage{}
		var pkgsStr, statusStr string
		err := rows.Scan(
			&img.ID, &img.InternalID, &img.OwnerID, &img.Name, &img.Description, &img.BaseImage, &pkgsStr,
			&img.RootfsPath, &img.KernelPath, &img.SizeMB, &statusStr,
			&img.ErrorMessage, &img.CreatedAt, &img.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		img.Status = ImageStatus(statusStr)
		_ = json.Unmarshal([]byte(pkgsStr), &img.Packages)
		if img.Packages == nil {
			img.Packages = []string{}
		}
		images = append(images, img)
	}
	return images, rows.Err()
}

// DeleteImage removes an image record by internal ID, external ID, or Name
func (s *Store) DeleteImage(ctx context.Context, idOrName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, "DELETE FROM images WHERE internal_id = ? OR id = ? OR name = ?", idOrName, idOrName, idOrName)
	return err
}

// SaveTemplate inserts or updates a Template record
func (s *Store) SaveTemplate(ctx context.Context, tpl *Template) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if tpl.CreatedAt.IsZero() {
		tpl.CreatedAt = now
	}
	tpl.UpdatedAt = now

	if tpl.InternalID == "" {
		cleanU := strings.ReplaceAll(uuid.New().String(), "-", "")
		tpl.InternalID = fmt.Sprintf("tpl_%s", cleanU[:16])
	}
	if tpl.ID == "" {
		if tpl.Name != "" {
			tpl.ID = tpl.Name
		} else {
			tpl.ID = tpl.InternalID
		}
	}
	if tpl.Name == "" {
		tpl.Name = tpl.ID
	}
	if tpl.OwnerID == "" {
		tpl.OwnerID = "default"
	}

	updateQuery := `
	UPDATE templates SET
		id = ?, name = ?, description = ?, source_vm_id = ?, disk_path = ?,
		kernel_path = ?, vcpus = ?, memory_mb = ?, disk_mb = ?, owner_id = ?, updated_at = ?
	WHERE internal_id = ? OR (owner_id = ? AND (id = ? OR name = ?));
	`
	res, err := s.db.ExecContext(ctx, updateQuery,
		tpl.ID, tpl.Name, tpl.Description, tpl.SourceVMID, tpl.DiskPath,
		tpl.KernelPath, tpl.VCPUs, tpl.MemoryMB, tpl.DiskMB, tpl.OwnerID, tpl.UpdatedAt,
		tpl.InternalID, tpl.OwnerID, tpl.ID, tpl.Name,
	)
	if err != nil {
		return err
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected > 0 {
		return nil
	}

	insertQuery := `
	INSERT INTO templates (
		id, internal_id, owner_id, name, description, source_vm_id, disk_path, kernel_path,
		vcpus, memory_mb, disk_mb, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err = s.db.ExecContext(ctx, insertQuery,
		tpl.ID, tpl.InternalID, tpl.OwnerID, tpl.Name, tpl.Description, tpl.SourceVMID, tpl.DiskPath, tpl.KernelPath,
		tpl.VCPUs, tpl.MemoryMB, tpl.DiskMB, tpl.CreatedAt, tpl.UpdatedAt,
	)
	return err
}

// GetTemplate retrieves a template by internal ID, external ID, or Name
func (s *Store) GetTemplate(ctx context.Context, idOrName string) (*Template, error) {
	return s.GetTemplateByOwner(ctx, "", idOrName)
}

// GetTemplateByOwner retrieves a template scoped to an owner. Duplicate
// template names are allowed across tenants.
func (s *Store) GetTemplateByOwner(ctx context.Context, ownerID, idOrName string) (*Template, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, internal_id, owner_id, name, description, source_vm_id, disk_path, kernel_path,
	       vcpus, memory_mb, disk_mb, created_at, updated_at
	FROM templates
	WHERE (internal_id = ? OR id = ? OR name = ?)
	  AND (? = '' OR owner_id = ? OR owner_id = '')
	ORDER BY CASE WHEN internal_id = ? THEN 1 WHEN id = ? THEN 2 ELSE 3 END
	LIMIT 1;
	`

	row := s.db.QueryRowContext(ctx, query, idOrName, idOrName, idOrName, ownerID, ownerID, idOrName, idOrName)
	tpl := &Template{}
	err := row.Scan(
		&tpl.ID, &tpl.InternalID, &tpl.OwnerID, &tpl.Name, &tpl.Description, &tpl.SourceVMID, &tpl.DiskPath,
		&tpl.KernelPath, &tpl.VCPUs, &tpl.MemoryMB, &tpl.DiskMB, &tpl.CreatedAt, &tpl.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return tpl, nil
}

// ListTemplates returns all templates
func (s *Store) ListTemplates(ctx context.Context) ([]*Template, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, internal_id, owner_id, name, description, source_vm_id, disk_path, kernel_path,
	       vcpus, memory_mb, disk_mb, created_at, updated_at
	FROM templates ORDER BY created_at DESC;
	`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var templates []*Template
	for rows.Next() {
		tpl := &Template{}
		err := rows.Scan(
			&tpl.ID, &tpl.InternalID, &tpl.OwnerID, &tpl.Name, &tpl.Description, &tpl.SourceVMID, &tpl.DiskPath,
			&tpl.KernelPath, &tpl.VCPUs, &tpl.MemoryMB, &tpl.DiskMB, &tpl.CreatedAt, &tpl.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		templates = append(templates, tpl)
	}
	return templates, rows.Err()
}

// DeleteTemplate removes a template record by internal ID, external ID, or Name
func (s *Store) DeleteTemplate(ctx context.Context, idOrName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, "DELETE FROM templates WHERE internal_id = ? OR id = ? OR name = ?", idOrName, idOrName, idOrName)
	return err
}

// SaveSSHKey saves or updates an SSH public key
func (s *Store) SaveSSHKey(ctx context.Context, key *SSHKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if key.OwnerID == "" {
		key.OwnerID = "default"
	}
	if key.CreatedAt.IsZero() {
		key.CreatedAt = time.Now().UTC()
	}

	query := `
	INSERT INTO ssh_keys (id, owner_id, name, public_key, fingerprint, all_sandboxes, is_default, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		public_key = excluded.public_key,
		fingerprint = excluded.fingerprint,
		all_sandboxes = excluded.all_sandboxes,
		is_default = excluded.is_default;
	`
	_, err := s.db.ExecContext(ctx, query,
		key.ID, key.OwnerID, key.Name, key.PublicKey, key.Fingerprint, boolInt(key.AllSandboxes), boolInt(key.IsDefault), key.CreatedAt,
	)
	return err
}

// ListSSHKeys returns all stored SSH public keys for an owner
func (s *Store) ListSSHKeys(ctx context.Context, ownerID string) ([]*SSHKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, owner_id, name, public_key, fingerprint, all_sandboxes, is_default, created_at
	FROM ssh_keys
	WHERE (? = "" OR owner_id = ?)
	ORDER BY created_at DESC;
	`
	rows, err := s.db.QueryContext(ctx, query, ownerID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []*SSHKey
	for rows.Next() {
		k := &SSHKey{}
		var allSandboxes, isDefault int
		if err := rows.Scan(&k.ID, &k.OwnerID, &k.Name, &k.PublicKey, &k.Fingerprint, &allSandboxes, &isDefault, &k.CreatedAt); err != nil {
			return nil, err
		}
		k.AllSandboxes = allSandboxes != 0
		k.IsDefault = isDefault != 0
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// GetSSHKey retrieves an SSH key by ID or Name
func (s *Store) GetSSHKey(ctx context.Context, idOrName string) (*SSHKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, owner_id, name, public_key, fingerprint, all_sandboxes, is_default, created_at
	FROM ssh_keys
	WHERE id = ? OR name = ?
	ORDER BY CASE WHEN id = ? THEN 1 ELSE 2 END
	LIMIT 1;
	`
	row := s.db.QueryRowContext(ctx, query, idOrName, idOrName, idOrName)
	k := &SSHKey{}
	var allSandboxes, isDefault int
	err := row.Scan(&k.ID, &k.OwnerID, &k.Name, &k.PublicKey, &k.Fingerprint, &allSandboxes, &isDefault, &k.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	k.AllSandboxes = allSandboxes != 0
	k.IsDefault = isDefault != 0
	return k, nil
}

// GetSSHKeyByOwner retrieves an SSH key belonging to ownerID by ID or name. An
// empty ownerID keeps the global (administrator or legacy) lookup. Owner
// scoping prevents duplicate key names across tenants from colliding.
func (s *Store) GetSSHKeyByOwner(ctx context.Context, ownerID, idOrName string) (*SSHKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, owner_id, name, public_key, fingerprint, all_sandboxes, is_default, created_at
	FROM ssh_keys
	WHERE (id = ? OR name = ?) AND (? = '' OR owner_id = ?)
	ORDER BY CASE WHEN id = ? THEN 1 ELSE 2 END, created_at DESC
	LIMIT 1;
	`
	row := s.db.QueryRowContext(ctx, query, idOrName, idOrName, ownerID, ownerID, idOrName)
	k := &SSHKey{}
	var allSandboxes, isDefault int
	err := row.Scan(&k.ID, &k.OwnerID, &k.Name, &k.PublicKey, &k.Fingerprint, &allSandboxes, &isDefault, &k.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	k.AllSandboxes = allSandboxes != 0
	k.IsDefault = isDefault != 0
	return k, nil
}

// GetDefaultSSHKey returns the key flagged as ownerID's default, or nil when
// the owner has not chosen one. The default key is embedded into every new
// sandbox the owner creates unless the request picks another key explicitly.
func (s *Store) GetDefaultSSHKey(ctx context.Context, ownerID string) (*SSHKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, owner_id, name, public_key, fingerprint, all_sandboxes, is_default, created_at
	FROM ssh_keys
	WHERE owner_id = ? AND is_default = 1
	ORDER BY created_at DESC
	LIMIT 1;
	`
	row := s.db.QueryRowContext(ctx, query, ownerID)
	k := &SSHKey{}
	var allSandboxes, isDefault int
	err := row.Scan(&k.ID, &k.OwnerID, &k.Name, &k.PublicKey, &k.Fingerprint, &allSandboxes, &isDefault, &k.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	k.AllSandboxes = allSandboxes != 0
	k.IsDefault = isDefault != 0
	return k, nil
}

// SetDefaultSSHKey flags keyID as ownerID's single default key, clearing the
// flag from every other key the owner holds. The key must belong to ownerID.
func (s *Store) SetDefaultSSHKey(ctx context.Context, ownerID, keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "UPDATE ssh_keys SET is_default = 0 WHERE owner_id = ?", ownerID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, "UPDATE ssh_keys SET is_default = 1 WHERE id = ? AND owner_id = ?", keyID, ownerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("ssh key %s not found for owner %s", keyID, ownerID)
	}
	return tx.Commit()
}

// ClearDefaultSSHKey removes the default flag from keyID when it is ownerID's
// default key. Other keys are left untouched.
func (s *Store) ClearDefaultSSHKey(ctx context.Context, ownerID, keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, "UPDATE ssh_keys SET is_default = 0 WHERE id = ? AND owner_id = ?", keyID, ownerID)
	return err
}

// DeleteSSHKey removes an SSH key by ID or Name
func (s *Store) DeleteSSHKey(ctx context.Context, idOrName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `DELETE FROM ssh_keys WHERE id = ? OR name = ?;`
	_, err := s.db.ExecContext(ctx, query, idOrName, idOrName)
	return err
}

// GetWorkerPool retrieves a pool by ID
func (s *Store) GetWorkerPool(ctx context.Context, id string) (*WorkerPool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := new(WorkerPool)
	err := s.db.QueryRowContext(ctx, `SELECT id, name, labels_json, policy_json, created_at, updated_at FROM worker_pools WHERE id = ?`, id).Scan(
		&p.ID, &p.Name, &p.LabelsJSON, &p.PolicyJSON, &p.CreatedAt, &p.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// DeleteWorkerPool deletes a worker pool
func (s *Store) DeleteWorkerPool(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM worker_pools WHERE id = ?`, id)
	return err
}

// SaveWorkerNode inserts or updates a worker node
func (s *Store) SaveWorkerNode(ctx context.Context, n *WorkerNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	n.UpdatedAt = now
	query := `INSERT INTO worker_nodes (
		id, name, pool_id, role, status, underlay_ip, arch,
		total_vcpus, total_mem_mb, total_disk_mb,
		allocated_vcpus, allocated_mem_mb, allocated_disk_mb,
		physical_vcpus, physical_mem_mb, physical_disk_mb,
		used_vcpus, used_mem_mb, used_disk_mb, running_vms,
		labels_json, capabilities_json,
		agent_version, fc_version, last_heartbeat, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		pool_id = excluded.pool_id,
		role = excluded.role,
		status = excluded.status,
		underlay_ip = excluded.underlay_ip,
		arch = excluded.arch,
		total_vcpus = excluded.total_vcpus,
		total_mem_mb = excluded.total_mem_mb,
		total_disk_mb = excluded.total_disk_mb,
		allocated_vcpus = excluded.allocated_vcpus,
		allocated_mem_mb = excluded.allocated_mem_mb,
		allocated_disk_mb = excluded.allocated_disk_mb,
		physical_vcpus = excluded.physical_vcpus,
		physical_mem_mb = excluded.physical_mem_mb,
		physical_disk_mb = excluded.physical_disk_mb,
		used_vcpus = excluded.used_vcpus,
		used_mem_mb = excluded.used_mem_mb,
		used_disk_mb = excluded.used_disk_mb,
		running_vms = excluded.running_vms,
		labels_json = excluded.labels_json,
		capabilities_json = excluded.capabilities_json,
		agent_version = excluded.agent_version,
		fc_version = excluded.fc_version,
		last_heartbeat = excluded.last_heartbeat,
		updated_at = excluded.updated_at`
	_, err := s.db.ExecContext(ctx, query,
		n.ID, n.Name, n.PoolID, n.Role, n.Status, n.UnderlayIP, n.Arch,
		n.TotalVCPUs, n.TotalMemMB, n.TotalDiskMB,
		n.AllocatedVCPUs, n.AllocatedMemMB, n.AllocatedDiskMB,
		n.PhysicalVCPUs, n.PhysicalMemMB, n.PhysicalDiskMB,
		n.UsedVCPUs, n.UsedMemMB, n.UsedDiskMB, n.RunningVMs,
		n.LabelsJSON, n.CapabilitiesJSON,
		n.AgentVersion, n.FCVersion, n.LastHeartbeat, n.CreatedAt, n.UpdatedAt,
	)
	return err
}

// GetWorkerNode retrieves a worker node by ID
func (s *Store) GetWorkerNode(ctx context.Context, id string) (*WorkerNode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := new(WorkerNode)
	err := s.db.QueryRowContext(ctx, `SELECT id, name, pool_id, role, status, underlay_ip, arch, total_vcpus, total_mem_mb, total_disk_mb, allocated_vcpus, allocated_mem_mb, allocated_disk_mb, physical_vcpus, physical_mem_mb, physical_disk_mb, used_vcpus, used_mem_mb, used_disk_mb, running_vms, labels_json, capabilities_json, agent_version, fc_version, last_heartbeat, created_at, updated_at FROM worker_nodes WHERE id = ? OR name = ? LIMIT 1`, id, id).Scan(
		&n.ID, &n.Name, &n.PoolID, &n.Role, &n.Status, &n.UnderlayIP, &n.Arch,
		&n.TotalVCPUs, &n.TotalMemMB, &n.TotalDiskMB,
		&n.AllocatedVCPUs, &n.AllocatedMemMB, &n.AllocatedDiskMB,
		&n.PhysicalVCPUs, &n.PhysicalMemMB, &n.PhysicalDiskMB,
		&n.UsedVCPUs, &n.UsedMemMB, &n.UsedDiskMB, &n.RunningVMs,
		&n.LabelsJSON, &n.CapabilitiesJSON,
		&n.AgentVersion, &n.FCVersion, &n.LastHeartbeat, &n.CreatedAt, &n.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return n, nil
}

// ListWorkerNodes returns all worker nodes
func (s *Store) ListWorkerNodes(ctx context.Context) ([]*WorkerNode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, pool_id, role, status, underlay_ip, arch, total_vcpus, total_mem_mb, total_disk_mb, allocated_vcpus, allocated_mem_mb, allocated_disk_mb, physical_vcpus, physical_mem_mb, physical_disk_mb, used_vcpus, used_mem_mb, used_disk_mb, running_vms, labels_json, capabilities_json, agent_version, fc_version, last_heartbeat, created_at, updated_at FROM worker_nodes ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WorkerNode
	for rows.Next() {
		n := new(WorkerNode)
		if err := rows.Scan(
			&n.ID, &n.Name, &n.PoolID, &n.Role, &n.Status, &n.UnderlayIP, &n.Arch,
			&n.TotalVCPUs, &n.TotalMemMB, &n.TotalDiskMB,
			&n.AllocatedVCPUs, &n.AllocatedMemMB, &n.AllocatedDiskMB,
			&n.PhysicalVCPUs, &n.PhysicalMemMB, &n.PhysicalDiskMB,
			&n.UsedVCPUs, &n.UsedMemMB, &n.UsedDiskMB, &n.RunningVMs,
			&n.LabelsJSON, &n.CapabilitiesJSON,
			&n.AgentVersion, &n.FCVersion, &n.LastHeartbeat, &n.CreatedAt, &n.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UpdateWorkerNodeStatus updates worker status
func (s *Store) UpdateWorkerNodeStatus(ctx context.Context, id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE worker_nodes SET status = ?, updated_at = ? WHERE id = ? OR name = ?`, status, time.Now().UTC(), id, id)
	return err
}

// DeleteWorkerNode removes a worker node
func (s *Store) DeleteWorkerNode(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM worker_nodes WHERE id = ? OR name = ?`, id, id)
	return err
}

// GetJob retrieves a job by ID
func (s *Store) GetJob(ctx context.Context, id string) (*Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j := new(Job)
	err := s.db.QueryRowContext(ctx, `SELECT id, owner_id, pool_id, type, state, priority, spec_json, result_json, repo_id, repo_url, repo_name, branch, commit_sha, source, vm_id, worker_id, queued_at, started_at, finished_at, deadline_at, created_at, updated_at FROM jobs WHERE id = ?`, id).Scan(
		&j.ID, &j.OwnerID, &j.PoolID, &j.Type, &j.State, &j.Priority, &j.SpecJSON, &j.ResultJSON, &j.RepoID, &j.RepoURL, &j.RepoName, &j.Branch, &j.CommitSHA, &j.Source, &j.VMID, &j.WorkerID, &j.QueuedAt, &j.StartedAt, &j.FinishedAt, &j.DeadlineAt, &j.CreatedAt, &j.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return j, nil
}

// ListJobs retrieves jobs, optionally filtered by owner
func (s *Store) ListJobs(ctx context.Context, ownerID string) ([]*Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	const selectCols = `SELECT id, owner_id, pool_id, type, state, priority, spec_json, result_json, repo_id, repo_url, repo_name, branch, commit_sha, source, vm_id, worker_id, queued_at, started_at, finished_at, deadline_at, created_at, updated_at FROM jobs`
	var (
		rows *sql.Rows
		err  error
	)
	if ownerID == "" {
		rows, err = s.db.QueryContext(ctx, selectCols+` ORDER BY queued_at DESC`)
	} else {
		// A specific owner sees strictly their own jobs; the legacy "default"
		// owner is not shared with authenticated tenants.
		rows, err = s.db.QueryContext(ctx, selectCols+` WHERE owner_id = ? ORDER BY queued_at DESC`, ownerID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j := new(Job)
		if err := rows.Scan(&j.ID, &j.OwnerID, &j.PoolID, &j.Type, &j.State, &j.Priority, &j.SpecJSON, &j.ResultJSON, &j.RepoID, &j.RepoURL, &j.RepoName, &j.Branch, &j.CommitSHA, &j.Source, &j.VMID, &j.WorkerID, &j.QueuedAt, &j.StartedAt, &j.FinishedAt, &j.DeadlineAt, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// UpdateJobExecution updates the ephemeral sandbox VM ID and worker ID assigned to a job.
func (s *Store) UpdateJobExecution(ctx context.Context, id, vmID, workerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET vm_id = CASE WHEN ? != '' THEN ? ELSE vm_id END, worker_id = CASE WHEN ? != '' THEN ? ELSE worker_id END, updated_at = ? WHERE id = ?`,
		vmID, vmID, workerID, workerID, now, id)
	return err
}

// UpdateJobState updates state, result, and completion times
func (s *Store) UpdateJobState(ctx context.Context, id, state, resultJSON string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if state == "running" {
		_, err := s.db.ExecContext(ctx, `UPDATE jobs SET state = ?, started_at = coalesce(started_at, ?), updated_at = ? WHERE id = ?`, state, now, now, id)
		return err
	}
	if state == "succeeded" || state == "failed" || state == "canceled" || state == "lost" {
		_, err := s.db.ExecContext(ctx, `UPDATE jobs SET state = ?, result_json = ?, finished_at = coalesce(finished_at, ?), updated_at = ? WHERE id = ?`, state, resultJSON, now, now, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET state = ?, updated_at = ? WHERE id = ?`, state, now, id)
	return err
}

// FailStaleJobs marks jobs left in a running state by a previous daemon
// process as failed, so they do not stay stuck forever after a restart.
func (s *Store) FailStaleJobs(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE jobs SET state = 'failed', result_json = ?, finished_at = coalesce(finished_at, ?), updated_at = ? WHERE state IN ('running', 'assigned')`,
		`{"error":"daemon restarted before the job finished"}`, now, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SaveJobAttempt records an attempt
func (s *Store) SaveJobAttempt(ctx context.Context, a *JobAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO job_attempts (id, job_id, worker_id, state, created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET state = excluded.state`, a.ID, a.JobID, a.WorkerID, a.State, a.CreatedAt)
	return err
}

// SaveWorkerLease records a lease
func (s *Store) SaveWorkerLease(ctx context.Context, l *WorkerLease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.ID == "" {
		l.ID = uuid.NewString()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO worker_leases (id, job_id, attempt_id, worker_id, expires_at, state) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET expires_at = excluded.expires_at, state = excluded.state`, l.ID, l.JobID, l.AttemptID, l.WorkerID, l.ExpiresAt, l.State)
	return err
}

// AppendJobLog appends a log entry
func (s *Store) AppendJobLog(ctx context.Context, l *JobLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.ID == "" {
		l.ID = uuid.NewString()
	}
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO job_logs (id, job_id, attempt_id, stream, seq, data, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, l.ID, l.JobID, l.AttemptID, l.Stream, l.Seq, l.Data, l.CreatedAt)
	return err
}

// GetJobLogs retrieves all logs for a job
func (s *Store) GetJobLogs(ctx context.Context, jobID string) ([]*JobLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, job_id, attempt_id, stream, seq, data, created_at FROM job_logs WHERE job_id = ? ORDER BY seq ASC, created_at ASC`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*JobLog
	for rows.Next() {
		l := new(JobLog)
		if err := rows.Scan(&l.ID, &l.JobID, &l.AttemptID, &l.Stream, &l.Seq, &l.Data, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SaveVolume inserts or updates a volume
func (s *Store) SaveVolume(ctx context.Context, v *Volume) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if v.ID == "" {
		v.ID = "vol_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	if v.Pool == "" {
		v.Pool = "default"
	}
	if v.Status == "" {
		v.Status = "available"
	}
	if v.Filesystem == "" {
		v.Filesystem = "ext4"
	}
	if v.OwnerID == "" {
		v.OwnerID = "default"
	}
	query := `INSERT INTO volumes (
		id, owner_id, name, pool, size_gb, used_gb, status, attached_vm,
		device_path, mount_point, filesystem, path, iops, throughput, snapshots, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		owner_id = excluded.owner_id,
		name = excluded.name,
		pool = excluded.pool,
		size_gb = excluded.size_gb,
		used_gb = excluded.used_gb,
		status = excluded.status,
		attached_vm = excluded.attached_vm,
		device_path = excluded.device_path,
		mount_point = excluded.mount_point,
		filesystem = excluded.filesystem,
		path = excluded.path,
		iops = excluded.iops,
		throughput = excluded.throughput,
		snapshots = excluded.snapshots,
		updated_at = excluded.updated_at`
	_, err := s.db.ExecContext(ctx, query,
		v.ID, v.OwnerID, v.Name, v.Pool, v.SizeGB, v.UsedGB, v.Status, v.AttachedVM,
		v.DevicePath, v.MountPoint, v.Filesystem, v.Path, v.IOPS, v.Throughput, v.Snapshots, v.CreatedAt, v.UpdatedAt,
	)
	return err
}

// GetVolume retrieves a volume by ID or name using the legacy global lookup.
// Callers serving tenant requests should prefer GetVolumeByOwner so duplicate
// names across owners cannot resolve to another tenant's volume.
func (s *Store) GetVolume(ctx context.Context, id string) (*Volume, error) {
	return s.GetVolumeByOwner(ctx, "", id)
}

// GetVolumeByOwner retrieves a volume belonging to ownerID by internal ID,
// external ID, or name. An empty ownerID keeps the global (administrator or
// legacy) lookup. Volume IDs are globally unique, but the owner predicate still
// applies to ID lookups so a caller cannot reach a foreign volume by ID.
func (s *Store) GetVolumeByOwner(ctx context.Context, ownerID, idOrName string) (*Volume, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := new(Volume)
	err := s.db.QueryRowContext(ctx, `SELECT id, owner_id, name, pool, size_gb, used_gb, status, attached_vm, device_path, mount_point, filesystem, path, iops, throughput, snapshots, created_at, updated_at FROM volumes WHERE (id = ? OR name = ?) AND (? = '' OR owner_id = ?) ORDER BY CASE WHEN id = ? THEN 1 ELSE 2 END LIMIT 1`, idOrName, idOrName, ownerID, ownerID, idOrName).Scan(
		&v.ID, &v.OwnerID, &v.Name, &v.Pool, &v.SizeGB, &v.UsedGB, &v.Status, &v.AttachedVM,
		&v.DevicePath, &v.MountPoint, &v.Filesystem, &v.Path, &v.IOPS, &v.Throughput, &v.Snapshots, &v.CreatedAt, &v.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// ListVolumes returns all persistent volumes
func (s *Store) ListVolumes(ctx context.Context) ([]*Volume, error) {
	return s.ListVolumesByOwner(ctx, "")
}

// ListVolumesByOwner returns volumes owned by ownerID, or every volume when ownerID is empty.
func (s *Store) ListVolumesByOwner(ctx context.Context, ownerID string) ([]*Volume, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_id, name, pool, size_gb, used_gb, status, attached_vm, device_path, mount_point, filesystem, path, iops, throughput, snapshots, created_at, updated_at FROM volumes WHERE (? = '' OR owner_id = ?) ORDER BY created_at DESC`, ownerID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Volume
	for rows.Next() {
		v := new(Volume)
		if err := rows.Scan(
			&v.ID, &v.OwnerID, &v.Name, &v.Pool, &v.SizeGB, &v.UsedGB, &v.Status, &v.AttachedVM,
			&v.DevicePath, &v.MountPoint, &v.Filesystem, &v.Path, &v.IOPS, &v.Throughput, &v.Snapshots, &v.CreatedAt, &v.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteVolume deletes a volume by its exact ID. Names are not globally
// unique, so name-based deletion would be able to cross tenant boundaries.
func (s *Store) DeleteVolume(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM volumes WHERE id = ?`, id)
	return err
}

// DeleteVolumeByID deletes a volume by its exact ID, optionally constrained to
// ownerID. Passing an empty ownerID performs an administrator/legacy delete.
func (s *Store) DeleteVolumeByID(ctx context.Context, id, ownerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ownerID == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM volumes WHERE id = ?`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM volumes WHERE id = ? AND owner_id = ?`, id, ownerID)
	return err
}

// SaveWebhook inserts or updates a webhook endpoint
func (s *Store) SaveWebhook(ctx context.Context, w *WebhookEndpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if w.ID == "" {
		w.ID = "whk_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = now
	}
	w.UpdatedAt = now
	if w.Events == nil {
		w.Events = []string{}
	}
	eventsJSON, _ := json.Marshal(w.Events)
	query := `INSERT INTO webhooks (
		id, url, description, secret, events, active, last_delivery, success_rate, deliveries_count, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		url = excluded.url,
		description = excluded.description,
		secret = excluded.secret,
		events = excluded.events,
		active = excluded.active,
		last_delivery = excluded.last_delivery,
		success_rate = excluded.success_rate,
		deliveries_count = excluded.deliveries_count,
		updated_at = excluded.updated_at`
	activeInt := 0
	if w.Active {
		activeInt = 1
	}
	_, err := s.db.ExecContext(ctx, query,
		w.ID, w.URL, w.Description, w.Secret, string(eventsJSON), activeInt, w.LastDelivery, w.SuccessRate, w.DeliveriesCount, w.CreatedAt, w.UpdatedAt,
	)
	return err
}

// GetWebhook retrieves a webhook by ID
func (s *Store) GetWebhook(ctx context.Context, id string) (*WebhookEndpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w := new(WebhookEndpoint)
	var eventsJSON string
	var activeInt int
	err := s.db.QueryRowContext(ctx, `SELECT id, url, description, secret, events, active, last_delivery, success_rate, deliveries_count, created_at, updated_at FROM webhooks WHERE id = ? LIMIT 1`, id).Scan(
		&w.ID, &w.URL, &w.Description, &w.Secret, &eventsJSON, &activeInt, &w.LastDelivery, &w.SuccessRate, &w.DeliveriesCount, &w.CreatedAt, &w.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w.Active = activeInt == 1
	_ = json.Unmarshal([]byte(eventsJSON), &w.Events)
	if w.Events == nil {
		w.Events = []string{}
	}
	return w, nil
}

// ListWebhooks returns all registered webhooks
func (s *Store) ListWebhooks(ctx context.Context) ([]*WebhookEndpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, url, description, secret, events, active, last_delivery, success_rate, deliveries_count, created_at, updated_at FROM webhooks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WebhookEndpoint
	for rows.Next() {
		w := new(WebhookEndpoint)
		var eventsJSON string
		var activeInt int
		if err := rows.Scan(
			&w.ID, &w.URL, &w.Description, &w.Secret, &eventsJSON, &activeInt, &w.LastDelivery, &w.SuccessRate, &w.DeliveriesCount, &w.CreatedAt, &w.UpdatedAt,
		); err != nil {
			return nil, err
		}
		w.Active = activeInt == 1
		_ = json.Unmarshal([]byte(eventsJSON), &w.Events)
		if w.Events == nil {
			w.Events = []string{}
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// DeleteWebhook removes a webhook by ID
func (s *Store) DeleteWebhook(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	return err
}

// GetOTelConfig retrieves the global OpenTelemetry configuration
func (s *Store) GetOTelConfig(ctx context.Context) (*OTelConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := new(OTelConfig)
	var enabledInt, metricsInt, tracesInt, logsInt int
	err := s.db.QueryRowContext(ctx, `SELECT enabled, endpoint, protocol, auth_header, service_name, sample_rate, export_interval, metrics_enabled, traces_enabled, logs_enabled, spans_exported, last_export_at FROM otel_config WHERE id = 1 LIMIT 1`).Scan(
		&enabledInt, &c.Endpoint, &c.Protocol, &c.AuthHeader, &c.ServiceName, &c.SampleRate, &c.ExportInterval, &metricsInt, &tracesInt, &logsInt, &c.SpansExported, &c.LastExportAt,
	)
	if err == sql.ErrNoRows {
		return &OTelConfig{
			Enabled:        false,
			Endpoint:       "http://localhost:4318",
			Protocol:       "http/protobuf",
			ServiceName:    "on-demand-vms",
			SampleRate:     "1.0",
			ExportInterval: "10s",
			MetricsEnabled: true,
			TracesEnabled:  true,
			LogsEnabled:    true,
			SpansExported:  0,
			LastExportAt:   "Never",
		}, nil
	}
	if err != nil {
		return nil, err
	}
	c.Enabled = enabledInt == 1
	c.MetricsEnabled = metricsInt == 1
	c.TracesEnabled = tracesInt == 1
	c.LogsEnabled = logsInt == 1
	return c, nil
}

// SaveOTelConfig saves or updates the singleton OpenTelemetry configuration
func (s *Store) SaveOTelConfig(ctx context.Context, c *OTelConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	enabledInt, metricsInt, tracesInt, logsInt := 0, 0, 0, 0
	if c.Enabled {
		enabledInt = 1
	}
	if c.MetricsEnabled {
		metricsInt = 1
	}
	if c.TracesEnabled {
		tracesInt = 1
	}
	if c.LogsEnabled {
		logsInt = 1
	}
	query := `INSERT INTO otel_config (
		id, enabled, endpoint, protocol, auth_header, service_name, sample_rate,
		export_interval, metrics_enabled, traces_enabled, logs_enabled, spans_exported, last_export_at
	) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		enabled = excluded.enabled,
		endpoint = excluded.endpoint,
		protocol = excluded.protocol,
		auth_header = excluded.auth_header,
		service_name = excluded.service_name,
		sample_rate = excluded.sample_rate,
		export_interval = excluded.export_interval,
		metrics_enabled = excluded.metrics_enabled,
		traces_enabled = excluded.traces_enabled,
		logs_enabled = excluded.logs_enabled,
		spans_exported = excluded.spans_exported,
		last_export_at = excluded.last_export_at`
	_, err := s.db.ExecContext(ctx, query,
		enabledInt, c.Endpoint, c.Protocol, c.AuthHeader, c.ServiceName, c.SampleRate,
		c.ExportInterval, metricsInt, tracesInt, logsInt, c.SpansExported, c.LastExportAt,
	)
	return err
}

// SaveAuditEvent records an auditable event
func (s *Store) SaveAuditEvent(ctx context.Context, e *AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.ID == "" {
		e.ID = "evt_" + strings.ReplaceAll(uuid.NewString()[:12], "-", "")
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	if e.Payload == "" {
		e.Payload = "{}"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_events (
		id, timestamp, event, actor, target, status, duration_ms, payload
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Timestamp, e.Event, e.Actor, e.Target, e.Status, e.DurationMs, e.Payload,
	)
	return err
}

// ListAuditEvents retrieves audit events ordered by timestamp descending
func (s *Store) ListAuditEvents(ctx context.Context, limit int) ([]*AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, timestamp, event, actor, target, status, duration_ms, payload FROM audit_events ORDER BY timestamp DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AuditEvent
	for rows.Next() {
		e := new(AuditEvent)
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Event, &e.Actor, &e.Target, &e.Status, &e.DurationMs, &e.Payload); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SaveAccessAPIKey persists a scoped API access key
func (s *Store) SaveAccessAPIKey(ctx context.Context, k *AccessAPIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k.ID == "" {
		k.ID = "key_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = time.Now().UTC()
	}
	if k.LastUsed == "" {
		k.LastUsed = "Never"
	}
	if k.CreatedBy == "" {
		k.CreatedBy = "admin"
	}
	query := `INSERT INTO access_api_keys (
		id, name, prefix, token, scope, last_used, expires_at, created_by, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		prefix = excluded.prefix,
		token = excluded.token,
		scope = excluded.scope,
		last_used = excluded.last_used,
		expires_at = excluded.expires_at,
		created_by = excluded.created_by`
	_, err := s.db.ExecContext(ctx, query,
		k.ID, k.Name, k.Prefix, k.Token, k.Scope, k.LastUsed, k.ExpiresAt, k.CreatedBy, k.CreatedAt,
	)
	return err
}

// GetAccessAPIKey retrieves an API access key by ID
func (s *Store) GetAccessAPIKey(ctx context.Context, id string) (*AccessAPIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := new(AccessAPIKey)
	err := s.db.QueryRowContext(ctx, `SELECT id, name, prefix, token, scope, last_used, expires_at, created_by, created_at FROM access_api_keys WHERE id = ? LIMIT 1`, id).Scan(
		&k.ID, &k.Name, &k.Prefix, &k.Token, &k.Scope, &k.LastUsed, &k.ExpiresAt, &k.CreatedBy, &k.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return k, nil
}

// GetAccessAPIKeyByToken retrieves an API access key by its full secret token
func (s *Store) GetAccessAPIKeyByToken(ctx context.Context, token string) (*AccessAPIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := new(AccessAPIKey)
	err := s.db.QueryRowContext(ctx, `SELECT id, name, prefix, token, scope, last_used, expires_at, created_by, created_at FROM access_api_keys WHERE token = ? LIMIT 1`, token).Scan(
		&k.ID, &k.Name, &k.Prefix, &k.Token, &k.Scope, &k.LastUsed, &k.ExpiresAt, &k.CreatedBy, &k.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return k, nil
}

// ListAccessAPIKeysByCreator lists API access keys created by a specific actor.
func (s *Store) ListAccessAPIKeysByCreator(ctx context.Context, createdBy string) ([]*AccessAPIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, prefix, token, scope, last_used, expires_at, created_by, created_at FROM access_api_keys WHERE created_by = ? ORDER BY created_at DESC`, createdBy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AccessAPIKey
	for rows.Next() {
		k := new(AccessAPIKey)
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.Token, &k.Scope, &k.LastUsed, &k.ExpiresAt, &k.CreatedBy, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ListAccessAPIKeys lists all API access keys
func (s *Store) ListAccessAPIKeys(ctx context.Context) ([]*AccessAPIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, prefix, token, scope, last_used, expires_at, created_by, created_at FROM access_api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AccessAPIKey
	for rows.Next() {
		k := new(AccessAPIKey)
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.Token, &k.Scope, &k.LastUsed, &k.ExpiresAt, &k.CreatedBy, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteAccessAPIKey deletes an API key by ID
func (s *Store) DeleteAccessAPIKey(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM access_api_keys WHERE id = ?`, id)
	return err
}

// SaveEnvironment persists an environment profile
func (s *Store) SaveEnvironment(ctx context.Context, e *EnvironmentProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.UpdatedAt = time.Now().UTC()
	if e.Version == 0 {
		e.Version = 1
	}
	if e.GitHubRepos == nil {
		e.GitHubRepos = []string{}
	}
	reposJSON, _ := json.Marshal(e.GitHubRepos)
	if e.IsDefault {
		_, _ = s.db.ExecContext(ctx, `UPDATE environments SET is_default = 0 WHERE name != ?`, e.Name)
	}
	isDefInt, safeInt, ghInt, secInt, boatInt, agInt := 0, 0, 0, 0, 0, 0
	if e.IsDefault {
		isDefInt = 1
	}
	if e.SafeForThirdParty {
		safeInt = 1
	}
	if e.GitHubReposEnabled {
		ghInt = 1
	}
	if e.SecretsEnabled {
		secInt = 1
	}
	if e.ComputeCredsEnabled || e.BoatCredsEnabled {
		boatInt = 1
	}
	if e.AgentCredsEnabled {
		agInt = 1
	}

	query := `INSERT INTO environments (
		name, is_default, safe_for_third_party, github_repos_enabled, github_repos,
		secrets_enabled, secrets, boat_creds_enabled, agent_creds_enabled, credential_profile, version, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(name) DO UPDATE SET
		is_default = excluded.is_default,
		safe_for_third_party = excluded.safe_for_third_party,
		github_repos_enabled = excluded.github_repos_enabled,
		github_repos = excluded.github_repos,
		secrets_enabled = excluded.secrets_enabled,
		secrets = excluded.secrets,
		boat_creds_enabled = excluded.boat_creds_enabled,
		agent_creds_enabled = excluded.agent_creds_enabled,
		credential_profile = excluded.credential_profile,
		version = excluded.version,
		updated_at = excluded.updated_at`
	_, err := s.db.ExecContext(ctx, query,
		e.Name, isDefInt, safeInt, ghInt, string(reposJSON),
		secInt, e.Secrets, boatInt, agInt, e.CredentialProfile, e.Version, e.UpdatedAt,
	)
	return err
}

// GetEnvironment retrieves an environment profile by name
func (s *Store) GetEnvironment(ctx context.Context, name string) (*EnvironmentProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := new(EnvironmentProfile)
	var reposJSON string
	var isDefInt, safeInt, ghInt, secInt, boatInt, agInt int
	err := s.db.QueryRowContext(ctx, `SELECT name, is_default, safe_for_third_party, github_repos_enabled, github_repos, secrets_enabled, secrets, boat_creds_enabled, agent_creds_enabled, credential_profile, version, updated_at FROM environments WHERE name = ? LIMIT 1`, name).Scan(
		&e.Name, &isDefInt, &safeInt, &ghInt, &reposJSON, &secInt, &e.Secrets, &boatInt, &agInt, &e.CredentialProfile, &e.Version, &e.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.IsDefault = isDefInt == 1
	e.SafeForThirdParty = safeInt == 1
	e.GitHubReposEnabled = ghInt == 1
	e.SecretsEnabled = secInt == 1
	e.ComputeCredsEnabled = boatInt == 1
	e.BoatCredsEnabled = boatInt == 1
	e.AgentCredsEnabled = agInt == 1
	_ = json.Unmarshal([]byte(reposJSON), &e.GitHubRepos)
	if e.GitHubRepos == nil {
		e.GitHubRepos = []string{}
	}
	return e, nil
}

// ListEnvironments returns all environment profiles
func (s *Store) ListEnvironments(ctx context.Context) ([]*EnvironmentProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT name, is_default, safe_for_third_party, github_repos_enabled, github_repos, secrets_enabled, secrets, boat_creds_enabled, agent_creds_enabled, credential_profile, version, updated_at FROM environments ORDER BY is_default DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*EnvironmentProfile
	for rows.Next() {
		e := new(EnvironmentProfile)
		var reposJSON string
		var isDefInt, safeInt, ghInt, secInt, boatInt, agInt int
		if err := rows.Scan(
			&e.Name, &isDefInt, &safeInt, &ghInt, &reposJSON, &secInt, &e.Secrets, &boatInt, &agInt, &e.CredentialProfile, &e.Version, &e.UpdatedAt,
		); err != nil {
			return nil, err
		}
		e.IsDefault = isDefInt == 1
		e.SafeForThirdParty = safeInt == 1
		e.GitHubReposEnabled = ghInt == 1
		e.SecretsEnabled = secInt == 1
		e.ComputeCredsEnabled = boatInt == 1
		e.BoatCredsEnabled = boatInt == 1
		e.AgentCredsEnabled = agInt == 1
		_ = json.Unmarshal([]byte(reposJSON), &e.GitHubRepos)
		if e.GitHubRepos == nil {
			e.GitHubRepos = []string{}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteEnvironment deletes an environment profile by name
func (s *Store) DeleteEnvironment(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM environments WHERE name = ?`, name)
	return err
}

// SavePortForward persists a port forwarding record
func (s *Store) SavePortForward(ctx context.Context, pf *PortForward) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pf.ID == "" {
		pf.ID = "pf_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	}
	if pf.CreatedAt.IsZero() {
		pf.CreatedAt = time.Now().UTC()
	}
	if pf.Protocol == "" {
		pf.Protocol = "tcp"
	}
	if pf.Status == "" {
		pf.Status = "active"
	}
	if pf.OwnerID == "" {
		pf.OwnerID = "default"
	}
	publicInt := 0
	if pf.Public {
		publicInt = 1
	}
	query := `INSERT INTO port_forwards (
		id, owner_id, host_port, vm_id, vm_name, guest_port, protocol, subdomain, public_url, status, public, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		owner_id = excluded.owner_id,
		host_port = excluded.host_port,
		vm_id = excluded.vm_id,
		vm_name = excluded.vm_name,
		guest_port = excluded.guest_port,
		protocol = excluded.protocol,
		subdomain = excluded.subdomain,
		public_url = excluded.public_url,
		status = excluded.status,
		public = excluded.public`
	_, err := s.db.ExecContext(ctx, query,
		pf.ID, pf.OwnerID, pf.HostPort, pf.VMID, pf.VMName, pf.GuestPort, pf.Protocol, pf.Subdomain, pf.PublicURL, pf.Status, publicInt, pf.CreatedAt,
	)
	return err
}

// GetPortForward retrieves a port forward by ID
// GetPortForwardBySubdomain resolves an exposure by its serve slug. Slugs are
// assigned globally unique at exposure time, so this is safe across tenants.
func (s *Store) GetPortForwardBySubdomain(ctx context.Context, subdomain string) (*PortForward, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	subdomain = strings.ToLower(strings.TrimSpace(subdomain))
	if subdomain == "" {
		return nil, nil
	}
	pf := new(PortForward)
	var publicInt int
	err := s.db.QueryRowContext(ctx, `SELECT id, owner_id, host_port, vm_id, vm_name, guest_port, protocol, subdomain, public_url, status, public, created_at FROM port_forwards WHERE lower(subdomain) = ? LIMIT 1`, subdomain).Scan(
		&pf.ID, &pf.OwnerID, &pf.HostPort, &pf.VMID, &pf.VMName, &pf.GuestPort, &pf.Protocol, &pf.Subdomain, &pf.PublicURL, &pf.Status, &publicInt, &pf.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pf.Public = publicInt == 1
	return pf, nil
}

func (s *Store) GetPortForward(ctx context.Context, id string) (*PortForward, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pf := new(PortForward)
	var publicInt int
	err := s.db.QueryRowContext(ctx, `SELECT id, owner_id, host_port, vm_id, vm_name, guest_port, protocol, subdomain, public_url, status, public, created_at FROM port_forwards WHERE id = ? LIMIT 1`, id).Scan(
		&pf.ID, &pf.OwnerID, &pf.HostPort, &pf.VMID, &pf.VMName, &pf.GuestPort, &pf.Protocol, &pf.Subdomain, &pf.PublicURL, &pf.Status, &publicInt, &pf.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pf.Public = publicInt == 1
	return pf, nil
}

// ListPortForwards returns all port forwarding mappings
func (s *Store) ListPortForwards(ctx context.Context) ([]*PortForward, error) {
	return s.ListPortForwardsByOwner(ctx, "")
}

// ListPortForwardsByOwner returns port forwards owned by ownerID, or every mapping when ownerID is empty.
func (s *Store) ListPortForwardsByOwner(ctx context.Context, ownerID string) ([]*PortForward, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_id, host_port, vm_id, vm_name, guest_port, protocol, subdomain, public_url, status, public, created_at FROM port_forwards WHERE (? = '' OR owner_id = ?) ORDER BY created_at DESC`, ownerID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PortForward
	for rows.Next() {
		pf := new(PortForward)
		var publicInt int
		if err := rows.Scan(
			&pf.ID, &pf.OwnerID, &pf.HostPort, &pf.VMID, &pf.VMName, &pf.GuestPort, &pf.Protocol, &pf.Subdomain, &pf.PublicURL, &pf.Status, &publicInt, &pf.CreatedAt,
		); err != nil {
			return nil, err
		}
		pf.Public = publicInt == 1
		out = append(out, pf)
	}
	return out, rows.Err()
}

// IsPortForwardPublic checks whether a given guest port on a VM is exposed as public.
func (s *Store) IsPortForwardPublic(ctx context.Context, vmIDOrName string, guestPort int) bool {
	return s.IsPortForwardPublicForOwner(ctx, "", vmIDOrName, guestPort)
}

// IsPortForwardPublicForOwner checks whether a given guest port on a VM owned by ownerID
// (or any owner when ownerID is empty) is exposed as public.
func (s *Store) IsPortForwardPublicForOwner(ctx context.Context, ownerID, vmIDOrName string, guestPort int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var count int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM port_forwards WHERE (? = '' OR owner_id = ?) AND (vm_id = ? OR vm_name = ?) AND guest_port = ? AND public = 1`, ownerID, ownerID, vmIDOrName, vmIDOrName, guestPort).Scan(&count)
	return count > 0
}

// ListPortForwardsForVM returns all port forwarding/exposure mappings for a VM owned by ownerID.
func (s *Store) ListPortForwardsForVM(ctx context.Context, ownerID, vmID, vmName string) ([]*PortForward, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_id, host_port, vm_id, vm_name, guest_port, protocol, subdomain, public_url, status, public, created_at FROM port_forwards WHERE (? = '' OR owner_id = ?) AND (vm_id = ? OR (? != '' AND vm_name = ?)) ORDER BY guest_port ASC, created_at DESC`, ownerID, ownerID, vmID, vmName, vmName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PortForward
	for rows.Next() {
		pf := new(PortForward)
		var publicInt int
		if err := rows.Scan(
			&pf.ID, &pf.OwnerID, &pf.HostPort, &pf.VMID, &pf.VMName, &pf.GuestPort, &pf.Protocol, &pf.Subdomain, &pf.PublicURL, &pf.Status, &publicInt, &pf.CreatedAt,
		); err != nil {
			return nil, err
		}
		pf.Public = publicInt == 1
		out = append(out, pf)
	}
	return out, rows.Err()
}

// DeletePortForward deletes a port forward mapping by ID
func (s *Store) DeletePortForward(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM port_forwards WHERE id = ?`, id)
	return err
}

// ===== GitHub Connections (CI Repositories) =====

// SaveGitHubConnection inserts or updates a connected GitHub repository.
func (s *Store) SaveGitHubConnection(ctx context.Context, c *GitHubConnection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == "" {
		c.ID = "gh_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	}
	if c.OwnerID == "" {
		c.OwnerID = "default"
	}
	if c.DefaultBranch == "" {
		c.DefaultBranch = "main"
	}
	if c.VCPUs <= 0 {
		c.VCPUs = 2
	}
	if c.MemoryMB <= 0 {
		c.MemoryMB = 2048
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 600
	}
	if c.Status == "" {
		c.Status = "connected"
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	query := `INSERT INTO github_connections (
		id, owner_id, name, repo_url, default_branch, github_token, webhook_secret,
		default_command, vcpus, memory_mb, timeout_sec, status, last_triggered_at, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		repo_url = excluded.repo_url,
		default_branch = excluded.default_branch,
		github_token = CASE WHEN excluded.github_token != '' THEN excluded.github_token ELSE github_connections.github_token END,
		webhook_secret = CASE WHEN excluded.webhook_secret != '' THEN excluded.webhook_secret ELSE github_connections.webhook_secret END,
		default_command = excluded.default_command,
		vcpus = excluded.vcpus,
		memory_mb = excluded.memory_mb,
		timeout_sec = excluded.timeout_sec,
		status = excluded.status,
		last_triggered_at = COALESCE(excluded.last_triggered_at, github_connections.last_triggered_at)`
	_, err := s.db.ExecContext(ctx, query,
		c.ID, c.OwnerID, c.Name, c.RepoURL, c.DefaultBranch, c.GitHubToken, c.WebhookSecret,
		c.DefaultCommand, c.VCPUs, c.MemoryMB, c.TimeoutSec, c.Status, c.LastTriggeredAt, c.CreatedAt,
	)
	if err == nil {
		c.PopulateMask()
	}
	return err
}

// GetGitHubConnection retrieves a GitHub connection by ID.
func (s *Store) GetGitHubConnection(ctx context.Context, id string) (*GitHubConnection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := new(GitHubConnection)
	err := s.db.QueryRowContext(ctx, `SELECT id, owner_id, name, repo_url, default_branch, github_token, webhook_secret, default_command, vcpus, memory_mb, timeout_sec, status, last_triggered_at, created_at FROM github_connections WHERE id = ? LIMIT 1`, id).Scan(
		&c.ID, &c.OwnerID, &c.Name, &c.RepoURL, &c.DefaultBranch, &c.GitHubToken, &c.WebhookSecret,
		&c.DefaultCommand, &c.VCPUs, &c.MemoryMB, &c.TimeoutSec, &c.Status, &c.LastTriggeredAt, &c.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.PopulateMask()
	return c, nil
}

// ListGitHubConnections returns connected GitHub repositories, optionally filtered by ownerID.
func (s *Store) ListGitHubConnections(ctx context.Context, ownerID string) ([]*GitHubConnection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	const selectCols = `SELECT id, owner_id, name, repo_url, default_branch, github_token, webhook_secret, default_command, vcpus, memory_mb, timeout_sec, status, last_triggered_at, created_at FROM github_connections`
	var (
		rows *sql.Rows
		err  error
	)
	if ownerID == "" {
		rows, err = s.db.QueryContext(ctx, selectCols+` ORDER BY created_at DESC`)
	} else {
		rows, err = s.db.QueryContext(ctx, selectCols+` WHERE owner_id = ? ORDER BY created_at DESC`, ownerID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*GitHubConnection{}
	for rows.Next() {
		c := new(GitHubConnection)
		if err := rows.Scan(
			&c.ID, &c.OwnerID, &c.Name, &c.RepoURL, &c.DefaultBranch, &c.GitHubToken, &c.WebhookSecret,
			&c.DefaultCommand, &c.VCPUs, &c.MemoryMB, &c.TimeoutSec, &c.Status, &c.LastTriggeredAt, &c.CreatedAt,
		); err != nil {
			return nil, err
		}
		c.PopulateMask()
		out = append(out, c)
	}
	return out, rows.Err()
}

// FindGitHubConnectionsByRepo finds connected repositories matching either repoURL or fullName.
func (s *Store) FindGitHubConnectionsByRepo(ctx context.Context, repoURL, fullName string) ([]*GitHubConnection, error) {
	all, err := s.ListGitHubConnections(ctx, "")
	if err != nil {
		return nil, err
	}
	norm := func(u string) string {
		u = strings.TrimSpace(strings.ToLower(u))
		u = strings.TrimSuffix(u, ".git")
		u = strings.TrimSuffix(u, "/")
		return u
	}
	wantURL := norm(repoURL)
	wantName := norm(fullName)
	var matched []*GitHubConnection
	for _, c := range all {
		if c == nil {
			continue
		}
		if (wantURL != "" && norm(c.RepoURL) == wantURL) ||
			(wantName != "" && (norm(c.Name) == wantName || strings.HasSuffix(norm(c.RepoURL), "/"+wantName))) {
			matched = append(matched, c)
		}
	}
	return matched, nil
}

// UpdateGitHubConnectionTriggered updates last_triggered_at on a GitHub connection.
func (s *Store) UpdateGitHubConnectionTriggered(ctx context.Context, id string, ts time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE github_connections SET last_triggered_at = ? WHERE id = ?`, ts.UTC(), id)
	return err
}

// DeleteGitHubConnection deletes a connected GitHub repository by ID.
func (s *Store) DeleteGitHubConnection(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM github_connections WHERE id = ?`, id)
	return err
}

const schedulingPolicySettingKey = "scheduling_policy"

// GetSchedulingPolicy loads the cluster-wide scheduling policy, falling back to DefaultSchedulingPolicy.
func (s *Store) GetSchedulingPolicy(ctx context.Context) (ClusterSchedulingPolicy, error) {
	policy := DefaultSchedulingPolicy()
	s.mu.RLock()
	defer s.mu.RUnlock()

	var valueJSON string
	err := s.db.QueryRowContext(ctx, `SELECT value_json FROM cluster_settings WHERE key = ? LIMIT 1`, schedulingPolicySettingKey).Scan(&valueJSON)
	if err == sql.ErrNoRows {
		return policy, nil
	}
	if err != nil {
		return policy, err
	}
	if strings.TrimSpace(valueJSON) != "" {
		if err := json.Unmarshal([]byte(valueJSON), &policy); err != nil {
			return DefaultSchedulingPolicy(), err
		}
	}
	return policy, nil
}

// SaveSchedulingPolicy persists the cluster-wide scheduling policy.
func (s *Store) SaveSchedulingPolicy(ctx context.Context, policy ClusterSchedulingPolicy) error {
	data, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO cluster_settings (key, value_json, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			value_json = excluded.value_json,
			updated_at = excluded.updated_at
	`, schedulingPolicySettingKey, string(data), now)
	return err
}
