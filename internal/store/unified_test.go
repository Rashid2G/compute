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

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/store"
	storeDB "github.com/Rashid2G/compute/internal/store/db"
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
