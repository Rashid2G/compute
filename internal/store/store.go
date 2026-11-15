package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"github.com/Rashid2G/compute/internal/store/migrations"
)

// Store provides access to SQLite with a dedicated single-writer pool and multi-connection reader pool.
type Store struct {
	writerDB *sql.DB
	readerDB *sql.DB
	dbPath   string
	closed   bool
	mu       sync.Mutex
	stopCh   chan struct{}
}

// OpenOptions configures database connection options.
type OpenOptions struct {
	SkipMigrations bool
	ReadOnlyOnly   bool
}

// Open initializes the SQLite store at the given database file path, applying WAL pragmas and running migrations.
func Open(dbPath string, opts ...OpenOptions) (*Store, error) {
	var opt OpenOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create directory for database: %w", err)
	}

	// Ensure secure file permissions (0600)
	if f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600); err == nil {
		_ = f.Chmod(0600)
		_ = f.Close()
	}

	buildDSN := func(readOnly bool) string {
		v := url.Values{}
		v.Add("_pragma", "busy_timeout(5000)")
		v.Add("_pragma", "foreign_keys(ON)")
		v.Add("_pragma", "temp_store(MEMORY)")
		v.Add("_pragma", "cache_size(-65536)")
		v.Add("_pragma", "mmap_size(268435456)")
		if readOnly {
			v.Add("_pragma", "query_only(ON)")
			return fmt.Sprintf("file:%s?mode=ro&%s", dbPath, v.Encode())
		}
		v.Add("_pragma", "journal_mode(WAL)")
		v.Add("_pragma", "synchronous(NORMAL)")
		return fmt.Sprintf("file:%s?%s", dbPath, v.Encode())
	}

	writerDSN := buildDSN(false)
	writerDB, err := sql.Open("sqlite", writerDSN)
	if err != nil {
		return nil, fmt.Errorf("failed to open writer database: %w", err)
	}

	// Single writer connection pool
	writerDB.SetMaxOpenConns(1)
	writerDB.SetMaxIdleConns(1)
	writerDB.SetConnMaxLifetime(0)

	// Execute initial pragmas on writer
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
		"PRAGMA temp_store=MEMORY;",
		"PRAGMA cache_size=-65536;",
		"PRAGMA mmap_size=268435456;",
	}
	for _, p := range pragmas {
		if _, err := writerDB.Exec(p); err != nil {
			_ = writerDB.Close()
			return nil, fmt.Errorf("failed to apply pragma %q: %w", p, err)
		}
	}

	// Run migrations on writer before opening reader pool
	if !opt.SkipMigrations {
		goose.SetBaseFS(migrations.FS)
		if err := goose.SetDialect("sqlite3"); err != nil {
			_ = writerDB.Close()
			return nil, fmt.Errorf("failed to set goose dialect: %w", err)
		}
		if err := goose.Up(writerDB, "."); err != nil {
			_ = writerDB.Close()
			return nil, fmt.Errorf("failed to run migrations: %w", err)
		}
	}

	// Reader pool
	readerDSN := buildDSN(true)
	readerDB, err := sql.Open("sqlite", readerDSN)
	if err != nil {
		_ = writerDB.Close()
		return nil, fmt.Errorf("failed to open reader database: %w", err)
	}

	maxReaders := runtime.NumCPU() * 2
	if maxReaders < 4 {
		maxReaders = 4
	}
	readerDB.SetMaxOpenConns(maxReaders)
	readerDB.SetMaxIdleConns(maxReaders)
	readerDB.SetConnMaxLifetime(0)

	// Execute read-only pragmas
	readPragmas := []string{
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
		"PRAGMA temp_store=MEMORY;",
		"PRAGMA cache_size=-65536;",
		"PRAGMA mmap_size=268435456;",
		"PRAGMA query_only=ON;",
	}
	for _, p := range readPragmas {
		if _, err := readerDB.Exec(p); err != nil {
			_ = readerDB.Close()
			_ = writerDB.Close()
			return nil, fmt.Errorf("failed to apply reader pragma %q: %w", p, err)
		}
	}

	s := &Store{
		writerDB: writerDB,
		readerDB: readerDB,
		dbPath:   dbPath,
		stopCh:   make(chan struct{}),
	}

	// Periodic maintenance: wal_checkpoint(TRUNCATE) hourly, PRAGMA optimize every 6h
	go s.maintenanceLoop()

	return s, nil
}

// Writer returns the dedicated single-writer *sql.DB handle.
func (s *Store) Writer() *sql.DB {
	return s.writerDB
}

// Reader returns the reader pool *sql.DB handle.
func (s *Store) Reader() *sql.DB {
	return s.readerDB
}

// WithTx executes the callback function inside an immediate writer transaction.
func (s *Store) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("store is closed")
	}

	tx, err := s.writerDB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// Backup creates an online backup of the database to destPath using VACUUM INTO.
func (s *Store) Backup(ctx context.Context, destPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("store is closed")
	}

	if err := os.Remove(destPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to clean target backup path: %w", err)
	}

	// VACUUM INTO produces an atomic, consistent snapshot without locking reads
	query := fmt.Sprintf("VACUUM INTO '%s'", destPath)
	_, err := s.writerDB.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to vacuum into %s: %w", destPath, err)
	}
	return nil
}

// Checkpoint executes a WAL checkpoint.
func (s *Store) Checkpoint(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("store is closed")
	}
	_, err := s.writerDB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE);")
	return err
}

// Optimize executes PRAGMA optimize.
func (s *Store) Optimize(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("store is closed")
	}
	_, err := s.writerDB.ExecContext(ctx, "PRAGMA optimize;")
	return err
}

func (s *Store) maintenanceLoop() {
	hourlyTicker := time.NewTicker(1 * time.Hour)
	sixHourTicker := time.NewTicker(6 * time.Hour)
	defer hourlyTicker.Stop()
	defer sixHourTicker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-hourlyTicker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = s.Checkpoint(ctx)
			cancel()
		case <-sixHourTicker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = s.Optimize(ctx)
			cancel()
		}
	}
}

// Close gracefully closes the store, optimizing tables and terminating the pools.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.stopCh)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = s.writerDB.ExecContext(ctx, "PRAGMA optimize;")
	cancel()

	var errReader, errWriter error
	if s.readerDB != nil {
		errReader = s.readerDB.Close()
	}
	if s.writerDB != nil {
		errWriter = s.writerDB.Close()
	}

	if errReader != nil {
		return errReader
	}
	return errWriter
}
