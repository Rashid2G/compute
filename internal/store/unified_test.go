package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/store"
	storeDB "github.com/Rashid2G/compute/internal/store/db"
	"github.com/Rashid2G/compute/internal/store/migrations"
)

// TestUnifiedStore_ConcurrentStressNoSQLiteBusy verifies that 20 concurrent goroutines
// performing heavy mixed reads and writes against the unified database experience zero SQLITE_BUSY errors.
func TestUnifiedStore_ConcurrentStressNoSQLiteBusy(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	computeStore, err := db.NewStoreFromUnified(s, tmpDir)
	if err != nil {
		t.Fatalf("NewStoreFromUnified failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	const concurrency = 20
	const iterationsPerWorker = 30

	var (
		wg          sync.WaitGroup
		busyErrors  int64
		otherErrors int64
	)

	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		workerID := i
		go func() {
			defer wg.Done()
			for j := 0; j < iterationsPerWorker; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}

				if j%2 == 0 {
					// Write operation: Save a VM or worker pool
					vm := &db.VM{
						ID:         fmt.Sprintf("stress-vm-%d-%d", workerID, j),
						Name:       fmt.Sprintf("vm-%d-%d", workerID, j),
						OwnerID:    fmt.Sprintf("tenant-%d", workerID%5),
						Status:     db.StatusRunning,
						VCPUs:      2,
						MemoryMB:   2048,
						DiskMB:     10240,
						IPAddress:  fmt.Sprintf("10.0.%d.%d", workerID, j+1),
						MACAddress: fmt.Sprintf("02:00:00:00:%02x:%02x", workerID, j),
						TAPDevice:  fmt.Sprintf("tap-%d-%d", workerID, j),
					}
					if err := computeStore.SaveVM(ctx, vm); err != nil {
						if strings.Contains(strings.ToLower(err.Error()), "busy") ||
							strings.Contains(strings.ToLower(err.Error()), "locked") {
							atomic.AddInt64(&busyErrors, 1)
						} else {
							atomic.AddInt64(&otherErrors, 1)
						}
					}
				} else {
					// Read operation: List VMs or query through reader
					owner := fmt.Sprintf("tenant-%d", workerID%5)
					vms, err := computeStore.ListVMsByOwner(ctx, owner)
					if err != nil {
						if strings.Contains(strings.ToLower(err.Error()), "busy") ||
							strings.Contains(strings.ToLower(err.Error()), "locked") {
							atomic.AddInt64(&busyErrors, 1)
						} else {
							atomic.AddInt64(&otherErrors, 1)
						}
					}
					_ = vms
				}
			}
		}()
	}

	wg.Wait()

	if busyErrors > 0 {
		t.Fatalf("expected 0 SQLITE_BUSY errors under concurrent load, got %d", busyErrors)
	}
	if otherErrors > 0 {
		t.Fatalf("unexpected non-busy errors during stress test: %d", otherErrors)
	}
}

// TestUnifiedStore_CreateVMWithQuotaCheck_ACID verifies that concurrent goroutines racing
// to create VMs against a tight tenant quota serialize cleanly inside SQLite writer transactions,
// allowing exactly the quota limit through with zero overruns.
func TestUnifiedStore_CreateVMWithQuotaCheck_ACID(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	computeStore, err := db.NewStoreFromUnified(s, tmpDir)
	if err != nil {
		t.Fatalf("NewStoreFromUnified failed: %v", err)
	}

	ctx := context.Background()
	const maxInstances = 5
	const concurrency = 20
	const targetOwner = "tenant-acid-test"

	quota := db.QuotaLimits{
		Instances: maxInstances,
		VCPUs:     maxInstances * 2,
		MemoryMB:  maxInstances * 2048,
		DiskGB:    500,
	}

	var (
		wg            sync.WaitGroup
		successCount  int64
		quotaErrCount int64
		otherErrCount int64
	)

	startGate := make(chan struct{})
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		idx := i
		go func() {
			defer wg.Done()
			<-startGate // Synchronize start for maximum concurrency race

			vm := &db.VM{
				ID:         fmt.Sprintf("acid-vm-%d", idx),
				Name:       fmt.Sprintf("acid-vm-%d", idx),
				OwnerID:    targetOwner,
				Status:     db.StatusCreated,
				VCPUs:      2,
				MemoryMB:   2048,
				DiskMB:     10240,
				IPAddress:  fmt.Sprintf("10.1.%d.1", idx),
				MACAddress: fmt.Sprintf("02:00:00:01:00:%02x", idx),
				TAPDevice:  fmt.Sprintf("tap-acid-%d", idx),
			}

			err := computeStore.CreateVMWithQuotaCheck(ctx, vm, quota)
			if err == nil {
				atomic.AddInt64(&successCount, 1)
			} else {
				var qErr *db.ErrQuotaExceeded
				if errors.As(err, &qErr) {
					atomic.AddInt64(&quotaErrCount, 1)
				} else {
					atomic.AddInt64(&otherErrCount, 1)
				}
			}
		}()
	}

	// Release all racers concurrently
	close(startGate)
	wg.Wait()

	if otherErrCount > 0 {
		t.Fatalf("unexpected non-quota errors during ACID race: %d", otherErrCount)
	}
	if successCount != int64(maxInstances) {
		t.Fatalf("expected exactly %d successful creations, got %d", maxInstances, successCount)
	}
	if quotaErrCount != int64(concurrency-maxInstances) {
		t.Fatalf("expected exactly %d quota-exceeded rejections, got %d", concurrency-maxInstances, quotaErrCount)
	}

	// Verify database state: COUNT(*) must be exactly maxInstances
	var rowCount int
	err = s.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM vms WHERE owner_id = ?", targetOwner).Scan(&rowCount)
	if err != nil {
		t.Fatalf("failed to query vms count: %v", err)
	}
	if rowCount != maxInstances {
		t.Fatalf("database has %d rows for tenant, expected exact quota %d (overrun detected!)", rowCount, maxInstances)
	}
}

// TestMigrateLegacyDatabases_Idempotent verifies automatic migration of legacy compute.db and auth.db
// into state.db, checking permissions, secret preservation, and idempotency on repeated runs.
func TestMigrateLegacyDatabases_Idempotent(t *testing.T) {
	dataDir := t.TempDir()

	// 1. Setup legacy auth.db
	authDBPath := filepath.Join(dataDir, "master", "auth.db")
	if err := os.MkdirAll(filepath.Dir(authDBPath), 0700); err != nil {
		t.Fatal(err)
	}
	legacyAuthStore, err := store.Open(authDBPath)
	if err != nil {
		t.Fatalf("failed to create legacy auth.db: %v", err)
	}
	queries := storeDB.New(legacyAuthStore.Writer())
	ctx := context.Background()

	now := time.Now().UTC().Unix()
	err = queries.CreateUser(ctx, &storeDB.User{
		ID:           "usr_legacy_1",
		Email:        "legacy1@compute.local",
		Username:     "legacy1",
		DisplayName:  "Legacy One",
		PasswordHash: "argon2id$testhash",
		Role:         "user",
		Status:       "active",
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		t.Fatalf("failed to seed legacy user: %v", err)
	}
	_ = legacyAuthStore.Close()

	// 2. Setup legacy compute.db
	computeDBPath := filepath.Join(dataDir, "data", "compute.db")
	if err := os.MkdirAll(filepath.Dir(computeDBPath), 0700); err != nil {
		t.Fatal(err)
	}
	legacyComputeStore, err := db.NewStore(computeDBPath)
	if err != nil {
		t.Fatalf("failed to create legacy compute.db: %v", err)
	}
	legacyVM := &db.VM{
		ID:         "vm-legacy-1",
		Name:       "vm-legacy-1",
		OwnerID:    "usr_legacy_1",
		Status:     db.StatusStopped,
		VCPUs:      2,
		MemoryMB:   2048,
		IPAddress:  "172.16.0.50",
		MACAddress: "02:00:ac:10:00:32",
		TAPDevice:  "mftap-leg1",
	}
	if err := legacyComputeStore.SaveVM(ctx, legacyVM); err != nil {
		t.Fatalf("failed to seed legacy VM: %v", err)
	}
	_ = legacyComputeStore.Close()

	// 3. Open fresh state.db in dataDir
	stateDBPath := filepath.Join(dataDir, "state.db")
	unifiedStore, err := store.Open(stateDBPath)
	if err != nil {
		t.Fatalf("failed to open unified state.db: %v", err)
	}
	defer unifiedStore.Close()

	// 4. Run MigrateLegacyDatabases
	if err := store.MigrateLegacyDatabases(ctx, unifiedStore, dataDir); err != nil {
		t.Fatalf("MigrateLegacyDatabases failed: %v", err)
	}

	// 5. Verify records migrated to state.db
	var userCount int
	if err := unifiedStore.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE id = 'usr_legacy_1'").Scan(&userCount); err != nil || userCount != 1 {
		t.Fatalf("expected migrated user usr_legacy_1, got count=%d, err=%v", userCount, err)
	}

	var vmCount int
	if err := unifiedStore.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM vms WHERE id = 'vm-legacy-1'").Scan(&vmCount); err != nil || vmCount != 1 {
		t.Fatalf("expected migrated vm vm-legacy-1, got count=%d, err=%v", vmCount, err)
	}

	// 6. Verify filesystem permissions on state.db (0600) and directory (0700)
	dirInfo, err := os.Stat(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0700 {
		t.Errorf("expected dir permissions 0700, got %04o", perm)
	}

	fileInfo, err := os.Stat(stateDBPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0600 {
		t.Errorf("expected state.db file permissions 0600, got %04o", perm)
	}

	// 7. Verify legacy files remain intact on disk
	if _, err := os.Stat(authDBPath); err != nil {
		t.Errorf("legacy auth.db missing after migration: %v", err)
	}
	if _, err := os.Stat(computeDBPath); err != nil {
		t.Errorf("legacy compute.db missing after migration: %v", err)
	}

	// 8. Run MigrateLegacyDatabases a second time -> assert 100% idempotent
	if err := store.MigrateLegacyDatabases(ctx, unifiedStore, dataDir); err != nil {
		t.Fatalf("second MigrateLegacyDatabases call failed: %v", err)
	}

	if err := unifiedStore.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE id = 'usr_legacy_1'").Scan(&userCount); err != nil || userCount != 1 {
		t.Fatalf("second run duplicate detected: user count = %d", userCount)
	}
	if err := unifiedStore.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM vms WHERE id = 'vm-legacy-1'").Scan(&vmCount); err != nil || vmCount != 1 {
		t.Fatalf("second run duplicate detected: vm count = %d", vmCount)
	}
}

// TestUnifiedAPIKeysAndAudit verifies API key constant-time verification and audit logging
// on the unified store.
func TestUnifiedAPIKeysAndAudit(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	computeStore, err := db.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer computeStore.Close()

	ctx := context.Background()

	// 1. Save and query an Access API Key
	rawToken := "csk_live_unified_test_secret_token_12345"
	key := &db.AccessAPIKey{
		ID:        "key_test_1",
		Name:      "test-key",
		Prefix:    "csk_live",
		Token:     rawToken,
		Scope:     "read,write",
		CreatedBy: "admin",
	}

	if err := computeStore.SaveAccessAPIKey(ctx, key); err != nil {
		t.Fatalf("SaveAccessAPIKey failed: %v", err)
	}

	// Query by token and verify constant-time match
	retrieved, err := computeStore.GetAccessAPIKeyByToken(ctx, rawToken)
	if err != nil || retrieved == nil {
		t.Fatalf("GetAccessAPIKeyByToken failed: %v", err)
	}
	if !db.CompareTokenConstantTime(retrieved.Token, rawToken) {
		t.Fatalf("constant time token comparison failed for retrieved key")
	}

	// 2. Record and query Audit Events
	event := &db.AuditEvent{
		ID:         "audit_evt_1",
		Timestamp:  time.Now().UTC(),
		Event:      "sandbox.created",
		Actor:      "admin",
		Target:     "vm-1",
		Status:     "success",
		DurationMs: 42,
		Payload:    `{"vcpus":2,"memory_mb":2048}`,
	}

	if err := computeStore.SaveAuditEvent(ctx, event); err != nil {
		t.Fatalf("SaveAuditEvent failed: %v", err)
	}

	events, err := computeStore.ListAuditEvents(ctx, 10)
	if err != nil {
		t.Fatalf("ListAuditEvents failed: %v", err)
	}
	if len(events) != 1 || events[0].ID != "audit_evt_1" {
		t.Fatalf("expected audit event audit_evt_1, got %+v", events)
	}
}

// TestMigration0003_PopulatedV2DatabaseWithForeignKeys verifies that upgrading a populated
// schema-v2 database containing active foreign-key references succeeds cleanly without
// FOREIGN KEY constraint failures and leaves foreign_keys enabled afterward.
func TestMigration0003_PopulatedV2DatabaseWithForeignKeys(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	// 1. Open without running migrations and migrate only up to version 2.
	sV2, err := store.Open(dbPath, store.OpenOptions{SkipMigrations: true})
	if err != nil {
		t.Fatalf("Open(SkipMigrations) failed: %v", err)
	}
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		_ = sV2.Close()
		t.Fatalf("SetDialect failed: %v", err)
	}
	if err := goose.UpTo(sV2.Writer(), ".", 2); err != nil {
		_ = sV2.Close()
		t.Fatalf("goose.UpTo(2) failed: %v", err)
	}

	now := time.Now().UTC().Unix()
	seedStmts := []string{
		fmt.Sprintf(`INSERT INTO users (id, email, username, password_hash, role, created_at, updated_at)
			VALUES ('usr_fk', 'fk@compute.local', 'fkuser', 'hash', 'user', %d, %d)`, now, now),
		fmt.Sprintf(`INSERT INTO nodes (id, name, role, status, underlay_ip, arch, total_vcpus, total_mem_mb, total_disk_mb, agent_version, fc_version, created_at, updated_at, organization_id, pool_id)
			VALUES ('nod_fk', 'node-fk', 'remote', 'ready', '10.0.0.2', 'amd64', 4, 8192, 50000, '1.0', '1.17', %d, %d, 'org_default', 'pool_default')`, now, now),
		`INSERT INTO kernels (id, arch, sha256, boot_args, is_default)
			VALUES ('kern_fk', 'amd64', 'abc123', 'console=ttyS0', 1)`,
		fmt.Sprintf(`INSERT INTO images (id, owner_id, kind, name, os_family, arch, kernel_id, status, created_at)
			VALUES ('img_fk', 'usr_fk', 'vm', 'ubuntu-fk', 'ubuntu', 'amd64', 'kern_fk', 'ready', %d)`, now),
		fmt.Sprintf(`INSERT INTO image_node_cache (image_id, node_id, status, bytes, last_used)
			VALUES ('img_fk', 'nod_fk', 'ready', 1024, %d)`, now),
		fmt.Sprintf(`INSERT INTO templates (id, owner_id, name, spec_json, created_at, updated_at)
			VALUES ('tpl_fk', 'usr_fk', 'tpl-fk', '{}', %d, %d)`, now, now),
		fmt.Sprintf(`INSERT INTO vpcs (id, owner_id, name, cidr, vni, created_at)
			VALUES ('vpc_fk', 'usr_fk', 'vpc-fk', '10.20.0.0/24', 5000, %d)`, now),
		fmt.Sprintf(`INSERT INTO instances (id, owner_id, name, kind, image_id, template_id, vcpus, memory_mb, node_id, vpc_id, desired_state, state, created_at, updated_at)
			VALUES ('ins_fk', 'usr_fk', 'inst-fk', 'vm', 'img_fk', 'tpl_fk', 2, 1024, 'nod_fk', 'vpc_fk', 'running', 'running', %d, %d)`, now, now),
		fmt.Sprintf(`INSERT INTO volumes (id, owner_id, name, node_id, size_mb, kind, source_image_id, instance_id, status, created_at)
			VALUES ('vol_fk', 'usr_fk', 'vol-fk', 'nod_fk', 2048, 'root', 'img_fk', 'ins_fk', 'attached', %d)`, now),
		fmt.Sprintf(`INSERT INTO jobs (id, organization_id, owner_id, pool_id, type, state, spec_json, queued_at, created_at, updated_at)
			VALUES ('job_fk', 'org_default', 'usr_fk', 'pool_default', 'exec', 'running', '{}', %d, %d, %d)`, now, now, now),
		fmt.Sprintf(`INSERT INTO job_attempts (id, job_id, node_id, state, created_at, updated_at)
			VALUES ('att_fk', 'job_fk', 'nod_fk', 'running', %d, %d)`, now, now),
		fmt.Sprintf(`INSERT INTO worker_leases (id, job_id, attempt_id, node_id, state, expires_at, created_at)
			VALUES ('lease_fk', 'job_fk', 'att_fk', 'nod_fk', 'active', %d, %d)`, now+60, now),
		fmt.Sprintf(`INSERT INTO job_events (id, job_id, attempt_id, sequence, type, created_at)
			VALUES ('evt_fk', 'job_fk', 'att_fk', 1, 'started', %d)`, now),
		fmt.Sprintf(`INSERT INTO job_logs (id, job_id, attempt_id, stream, sequence, data, created_at)
			VALUES ('log_fk', 'job_fk', 'att_fk', 'stdout', 1, 'hello', %d)`, now),
		fmt.Sprintf(`INSERT INTO artifacts (id, job_id, attempt_id, name, storage_key, status, created_at)
			VALUES ('art_fk', 'job_fk', 'att_fk', 'out.txt', 'k1', 'ready', %d)`, now),
		fmt.Sprintf(`INSERT INTO operations (id, owner_id, kind, target_id, status, trace_id, created_at, updated_at)
			VALUES ('op_fk', 'usr_fk', 'instance.start', 'ins_fk', 'running', 'trc_1', %d, %d)`, now, now),
	}
	for _, stmt := range seedStmts {
		if _, err := sV2.Writer().Exec(stmt); err != nil {
			_ = sV2.Close()
			t.Fatalf("seed statement failed (%s): %v", stmt, err)
		}
	}
	if err := sV2.Close(); err != nil {
		t.Fatalf("closing v2 store failed: %v", err)
	}

	// 2. Re-open with normal store.Open, which executes migration 0003.
	sV3, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open upgrading populated v2 DB through migration 0003 failed: %v", err)
	}
	defer sV3.Close()

	ctx := context.Background()
	var fkEnabled int
	if err := sV3.Writer().QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fkEnabled); err != nil || fkEnabled != 1 {
		t.Fatalf("expected PRAGMA foreign_keys=1 after migration 0003, got %d (err=%v)", fkEnabled, err)
	}

	fkRows, err := sV3.Writer().QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("PRAGMA foreign_key_check failed: %v", err)
	}
	defer fkRows.Close()
	if fkRows.Next() {
		t.Fatalf("expected zero foreign key violations after migration 0003")
	}

	// 3. Verify seeded records survived table recreations intact.
	checks := []struct {
		table string
		id    string
	}{
		{"images", "img_fk"},
		{"templates", "tpl_fk"},
		{"volumes", "vol_fk"},
		{"worker_pools", "pool_default"},
		{"jobs", "job_fk"},
		{"job_attempts", "att_fk"},
		{"worker_leases", "lease_fk"},
		{"job_logs", "log_fk"},
	}
	for _, c := range checks {
		var cnt int
		if err := sV3.Reader().QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = ?", c.table), c.id).Scan(&cnt); err != nil || cnt != 1 {
			t.Fatalf("expected row %q preserved in %s, got count=%d err=%v", c.id, c.table, cnt, err)
		}
	}
}

// TestMigrateLegacyDatabases_StaggeredArrival verifies that running migration when no legacy DBs
// exist or when only one legacy DB is present does not prevent migrating the other DB later.
func TestMigrateLegacyDatabases_StaggeredArrival(t *testing.T) {
	dataDir := t.TempDir()
	ctx := context.Background()

	stateDBPath := filepath.Join(dataDir, "data", "state.db")
	unifiedStore, err := store.Open(stateDBPath)
	if err != nil {
		t.Fatalf("failed to open unified state.db: %v", err)
	}
	defer unifiedStore.Close()

	// 1. Initial run with no legacy files should succeed and leave no tombstone markers.
	if err := store.MigrateLegacyDatabases(ctx, unifiedStore, filepath.Dir(stateDBPath)); err != nil {
		t.Fatalf("initial empty MigrateLegacyDatabases failed: %v", err)
	}
	var markerCount int
	if err := unifiedStore.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM legacy_migrations").Scan(&markerCount); err != nil || markerCount != 0 {
		t.Fatalf("expected 0 migration markers when no legacy files exist, got %d (err=%v)", markerCount, err)
	}

	// 2. Place only legacy auth.db and run migration.
	authDBPath := filepath.Join(dataDir, "master", "auth.db")
	if err := os.MkdirAll(filepath.Dir(authDBPath), 0700); err != nil {
		t.Fatal(err)
	}
	legacyAuth, err := store.Open(authDBPath)
	if err != nil {
		t.Fatalf("failed to create legacy auth.db: %v", err)
	}
	now := time.Now().UTC().Unix()
	if err := storeDB.New(legacyAuth.Writer()).CreateUser(ctx, &storeDB.User{
		ID:           "usr_staggered",
		Email:        "staggered@compute.local",
		Username:     "staggered",
		PasswordHash: "hash",
		Role:         "user",
		Status:       "active",
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("failed to seed staggered user: %v", err)
	}
	_ = legacyAuth.Close()

	if err := store.MigrateLegacyDatabases(ctx, unifiedStore, filepath.Dir(stateDBPath)); err != nil {
		t.Fatalf("auth-only MigrateLegacyDatabases failed: %v", err)
	}
	var userCount int
	if err := unifiedStore.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE id = 'usr_staggered'").Scan(&userCount); err != nil || userCount != 1 {
		t.Fatalf("expected migrated user usr_staggered, got count=%d err=%v", userCount, err)
	}

	// 3. Later restore legacy compute.db and run migration again -> compute.db must still migrate!
	computeDBPath := filepath.Join(dataDir, "data", "compute.db")
	legacyCompute, err := db.NewStore(computeDBPath)
	if err != nil {
		t.Fatalf("failed to create legacy compute.db: %v", err)
	}
	if err := legacyCompute.SaveVM(ctx, &db.VM{
		ID:         "vm-staggered",
		Name:       "vm-staggered",
		OwnerID:    "usr_staggered",
		Status:     db.StatusStopped,
		VCPUs:      1,
		MemoryMB:   512,
		IPAddress:  "172.16.0.60",
		MACAddress: "02:00:ac:10:00:3c",
		TAPDevice:  "mftap-stag",
	}); err != nil {
		t.Fatalf("failed to seed staggered VM: %v", err)
	}
	_ = legacyCompute.Close()

	if err := store.MigrateLegacyDatabases(ctx, unifiedStore, filepath.Dir(stateDBPath)); err != nil {
		t.Fatalf("compute-arrival MigrateLegacyDatabases failed: %v", err)
	}
	var vmCount int
	if err := unifiedStore.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM vms WHERE id = 'vm-staggered'").Scan(&vmCount); err != nil || vmCount != 1 {
		t.Fatalf("expected migrated vm vm-staggered after staggered arrival, got count=%d err=%v", vmCount, err)
	}
}
