package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/Rashid2G/compute/internal/store/db"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStore_OpenAndMigrations(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	// Verify table existence
	tables := []string{
		"users", "sessions", "api_keys", "nodes", "node_join_tokens",
		"sizes", "kernels", "images", "image_node_cache", "registry_credentials",
		"templates", "vpcs", "instances", "ip_allocations", "instance_secrets",
		"user_ssh_keys", "volumes", "snapshots", "operations", "settings",
	}

	for _, table := range tables {
		var name string
		err := s.Reader().QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %s was not created: %v", table, err)
		}
	}

	// Verify default sizes were seeded
	var count int
	err = s.Reader().QueryRow("SELECT COUNT(*) FROM sizes").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query sizes count: %v", err)
	}
	if count < 4 {
		t.Errorf("expected at least 4 default sizes, got %d", count)
	}
}

func TestStore_TransactionsAndRollback(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Successful commit
	userID := NewUserID()
	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO users (id, email, username, display_name, password_hash, role, status, created_at, updated_at)
			VALUES (?, 'test@example.com', 'testuser', 'Test User', 'hash', 'user', 'active', 1000, 1000)
		`, userID)
		return err
	})
	if err != nil {
		t.Fatalf("WithTx commit failed: %v", err)
	}

	// Verify user exists via Reader
	var username string
	err = s.Reader().QueryRow("SELECT username FROM users WHERE id=?", userID).Scan(&username)
	if err != nil {
		t.Fatalf("user not found: %v", err)
	}
	if username != "testuser" {
		t.Errorf("expected username 'testuser', got %q", username)
	}

	// Transaction rollback
	errExpected := errors.New("aborted transaction")
	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO users (id, email, username, display_name, password_hash, role, status, created_at, updated_at)
			VALUES (?, 'fail@example.com', 'failuser', 'Fail User', 'hash', 'user', 'active', 2000, 2000)
		`, NewUserID())
		if err != nil {
			return err
		}
		return errExpected
	})
	if !errors.Is(err, errExpected) {
		t.Fatalf("expected errExpected, got %v", err)
	}

	// Verify user was not saved
	var failCount int
	_ = s.Reader().QueryRow("SELECT COUNT(*) FROM users WHERE username='failuser'").Scan(&failCount)
	if failCount != 0 {
		t.Errorf("expected failuser to not exist after rollback, got count %d", failCount)
	}
}

func TestStore_ConcurrentReadsAndWrites(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	var wg sync.WaitGroup

	// Concurrently write settings
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = s.WithTx(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", "k", "v")
				return err
			})
		}(i)
	}

	// Concurrently read settings
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var val string
			_ = s.Reader().QueryRow("SELECT value FROM settings WHERE key='k'").Scan(&val)
		}()
	}

	wg.Wait()
}

func TestStore_BackupOnline(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	backupPath := filepath.Join(tmpDir, "backup.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	_ = s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO settings (key, value) VALUES ('version', '2.0.0')")
		return err
	})

	if err := s.Backup(ctx, backupPath); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	// Verify backup DB opens and has content
	backupStore, err := Open(backupPath, OpenOptions{SkipMigrations: true})
	if err != nil {
		t.Fatalf("failed to open backup store: %v", err)
	}
	defer backupStore.Close()

	var val string
	err = backupStore.Reader().QueryRow("SELECT value FROM settings WHERE key='version'").Scan(&val)
	if err != nil {
		t.Fatalf("failed to query setting from backup: %v", err)
	}
	if val != "2.0.0" {
		t.Errorf("expected version '2.0.0', got %q", val)
	}
}

func TestStore_ForeignKeysEnforced(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Inserting an API key with non-existent user_id must fail due to foreign keys
	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO api_keys (id, user_id, name, secret_hash, prefix, created_at)
			VALUES (?, 'usr_nonexistent', 'testkey', X'001122', 'mfk_01J9', 1000)
		`, NewAPIKeyID())
		return err
	})

	if err == nil {
		t.Fatal("expected foreign key violation error, got nil")
	}
}

func TestID_GenerationAndValidation(t *testing.T) {
	uid := NewUserID()
	if !ValidateID(uid, PrefixUser) {
		t.Errorf("expected valid user ID, got %s", uid)
	}
	if ValidateID(uid, PrefixInstance) {
		t.Errorf("user ID %s should not validate as instance ID", uid)
	}

	insID := NewInstanceID()
	if !ValidateID(insID, PrefixInstance) {
		t.Errorf("expected valid instance ID, got %s", insID)
	}
}

func TestStore_FilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	fi, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	// Check permission is 0600 (-rw-------)
	perm := fi.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected file permissions 0600, got %o", perm)
	}
}

func TestStore_ResourceDistinctIDsAndTenantIsolation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_res.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	q := db.New(st.Writer())

	// Create 2 users
	u1 := &db.User{
		ID:           NewUserID(),
		Email:        "u1@example.com",
		Username:     "u1",
		PasswordHash: "hash1",
		Role:         "user",
		Status:       "active",
		CreatedAt:    db.NowMillis(),
		UpdatedAt:    db.NowMillis(),
	}
	u2 := &db.User{
		ID:           NewUserID(),
		Email:        "u2@example.com",
		Username:     "u2",
		PasswordHash: "hash2",
		Role:         "user",
		Status:       "active",
		CreatedAt:    db.NowMillis(),
		UpdatedAt:    db.NowMillis(),
	}
	if err := q.CreateUser(ctx, u1); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateUser(ctx, u2); err != nil {
		t.Fatal(err)
	}

	// 1. VPCs with same name for both users, distinct internal IDs
	vpc1ID := NewVPCID()
	vpc2ID := NewVPCID()
	if vpc1ID == vpc2ID {
		t.Fatal("expected distinct VPC IDs")
	}
	v1 := &db.VPC{ID: vpc1ID, OwnerID: u1.ID, Name: "default", CIDR: "10.0.0.0/24", VNI: 5001, Egress: "nat", CreatedAt: db.NowMillis()}
	v2 := &db.VPC{ID: vpc2ID, OwnerID: u2.ID, Name: "default", CIDR: "10.1.0.0/24", VNI: 5002, Egress: "nat", CreatedAt: db.NowMillis()}
	if err := q.CreateVPC(ctx, v1); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateVPC(ctx, v2); err != nil {
		t.Fatal(err)
	}

	// Lookup by distinct ID
	gotV1, err := q.GetVPC(ctx, u1.ID, vpc1ID)
	if err != nil || gotV1.ID != vpc1ID {
		t.Fatalf("failed to get VPC by distinct ID: %+v, err: %v", gotV1, err)
	}
	// Lookup by name scoped to owner
	gotV1ByName, err := q.GetVPC(ctx, u1.ID, "default")
	if err != nil || gotV1ByName.ID != vpc1ID {
		t.Fatalf("expected u1 VPC, got: %+v", gotV1ByName)
	}
	gotV2ByName, err := q.GetVPC(ctx, u2.ID, "default")
	if err != nil || gotV2ByName.ID != vpc2ID {
		t.Fatalf("expected u2 VPC, got: %+v", gotV2ByName)
	}

	// 2. Images with distinct IDs
	img1ID := NewImageID()
	img1 := &db.Image{ID: img1ID, OwnerID: &u1.ID, Kind: "vm", Name: "custom-ubuntu", Arch: "amd64", Status: "ready", CreatedAt: db.NowMillis()}
	if err := q.CreateImage(ctx, img1); err != nil {
		t.Fatal(err)
	}
	gotImg, err := q.GetImage(ctx, u1.ID, "custom-ubuntu")
	if err != nil || gotImg.ID != img1ID {
		t.Fatalf("failed to get image by name: %+v", gotImg)
	}

	// 3. Instances (VMs) with same name for both users, distinct internal IDs
	ins1ID := NewInstanceID()
	ins2ID := NewInstanceID()
	i1 := &db.Instance{
		ID: ins1ID, OwnerID: u1.ID, Name: "web-server", Kind: "vm", ImageID: img1ID,
		VCPUs: 2, MemoryMB: 2048, VPCID: vpc1ID, DesiredState: "running", State: "running", RestartPolicy: "no",
		CreatedAt: db.NowMillis(), UpdatedAt: db.NowMillis(),
	}
	i2 := &db.Instance{
		ID: ins2ID, OwnerID: u2.ID, Name: "web-server", Kind: "vm", ImageID: img1ID,
		VCPUs: 4, MemoryMB: 4096, VPCID: vpc2ID, DesiredState: "stopped", State: "stopped", RestartPolicy: "no",
		CreatedAt: db.NowMillis(), UpdatedAt: db.NowMillis(),
	}
	if err := q.CreateInstance(ctx, i1); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateInstance(ctx, i2); err != nil {
		t.Fatal(err)
	}

	// Lookup instance by name only
	gotI1ByName, err := q.GetInstance(ctx, u1.ID, "web-server")
	if err != nil || gotI1ByName.ID != ins1ID || gotI1ByName.VCPUs != 2 {
		t.Fatalf("expected u1 instance by name, got: %+v", gotI1ByName)
	}
	gotI2ByName, err := q.GetInstance(ctx, u2.ID, "web-server")
	if err != nil || gotI2ByName.ID != ins2ID || gotI2ByName.VCPUs != 4 {
		t.Fatalf("expected u2 instance by name, got: %+v", gotI2ByName)
	}

	// Lookup instance by distinct internal ID
	gotI1ByID, err := q.GetInstance(ctx, u1.ID, ins1ID)
	if err != nil || gotI1ByID.ID != ins1ID {
		t.Fatalf("expected instance by ID, got: %+v", gotI1ByID)
	}

	// 4. Volumes with same name for both users, distinct internal IDs
	vol1ID := NewVolumeID()
	vol2ID := NewVolumeID()
	vl1 := &db.Volume{ID: vol1ID, OwnerID: u1.ID, Name: "data-disk", SizeMB: 51200, Kind: "data", Status: "available", CreatedAt: db.NowMillis()}
	vl2 := &db.Volume{ID: vol2ID, OwnerID: u2.ID, Name: "data-disk", SizeMB: 102400, Kind: "data", Status: "available", CreatedAt: db.NowMillis()}
	if err := q.CreateVolume(ctx, vl1); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateVolume(ctx, vl2); err != nil {
		t.Fatal(err)
	}

	gotVl1, err := q.GetVolume(ctx, u1.ID, "data-disk")
	if err != nil || gotVl1.ID != vol1ID || gotVl1.SizeMB != 51200 {
		t.Fatalf("expected u1 volume, got: %+v", gotVl1)
	}
	gotVl2, err := q.GetVolume(ctx, u2.ID, "data-disk")
	if err != nil || gotVl2.ID != vol2ID || gotVl2.SizeMB != 102400 {
		t.Fatalf("expected u2 volume, got: %+v", gotVl2)
	}

	// Deleting u1 volume does not delete u2 volume
	if err := q.DeleteVolume(ctx, u1.ID, "data-disk"); err != nil {
		t.Fatal(err)
	}
	stillVl2, err := q.GetVolume(ctx, u2.ID, "data-disk")
	if err != nil || stillVl2.ID != vol2ID {
		t.Fatalf("u2 volume should still exist: %+v", stillVl2)
	}
}
