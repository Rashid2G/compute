package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

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

func (s *Store) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS vms (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		status TEXT NOT NULL,
		vcpus INTEGER NOT NULL,
		memory_mb INTEGER NOT NULL,
		ip_address TEXT NOT NULL,
		mac_address TEXT NOT NULL,
		tap_device TEXT NOT NULL,
		rootfs_path TEXT NOT NULL,
		kernel_path TEXT NOT NULL,
		disk_path TEXT NOT NULL,
		snapshot_path TEXT DEFAULT '',
		mem_file_path TEXT DEFAULT '',
		ssh_key_path TEXT DEFAULT '',
		pid INTEGER DEFAULT 0,
		socket_path TEXT DEFAULT '',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_vms_status ON vms(status);
	CREATE INDEX IF NOT EXISTS idx_vms_ip ON vms(ip_address);
	`
	_, err := s.db.Exec(schema)
	return err
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

	query := `
	INSERT INTO vms (
		id, name, status, vcpus, memory_mb, ip_address, mac_address, tap_device,
		rootfs_path, kernel_path, disk_path, snapshot_path, mem_file_path, ssh_key_path,
		pid, socket_path, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		status = excluded.status,
		vcpus = excluded.vcpus,
		memory_mb = excluded.memory_mb,
		ip_address = excluded.ip_address,
		mac_address = excluded.mac_address,
		tap_device = excluded.tap_device,
		rootfs_path = excluded.rootfs_path,
		kernel_path = excluded.kernel_path,
		disk_path = excluded.disk_path,
		snapshot_path = excluded.snapshot_path,
		mem_file_path = excluded.mem_file_path,
		ssh_key_path = excluded.ssh_key_path,
		pid = excluded.pid,
		socket_path = excluded.socket_path,
		updated_at = excluded.updated_at;
	`

	_, err := s.db.ExecContext(ctx, query,
		vm.ID, vm.Name, string(vm.Status), vm.VCPUs, vm.MemoryMB,
		vm.IPAddress, vm.MACAddress, vm.TAPDevice, vm.RootfsPath,
		vm.KernelPath, vm.DiskPath, vm.SnapshotPath, vm.MemFilePath,
		vm.SSHKeyPath, vm.PID, vm.SocketPath, vm.CreatedAt, vm.UpdatedAt,
	)
	return err
}

// GetVM retrieves a single VM by ID
func (s *Store) GetVM(ctx context.Context, id string) (*VM, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, name, status, vcpus, memory_mb, ip_address, mac_address, tap_device,
	       rootfs_path, kernel_path, disk_path, snapshot_path, mem_file_path, ssh_key_path,
	       pid, socket_path, created_at, updated_at
	FROM vms WHERE id = ?;
	`

	row := s.db.QueryRowContext(ctx, query, id)
	vm := &VM{}
	var status string
	err := row.Scan(
		&vm.ID, &vm.Name, &status, &vm.VCPUs, &vm.MemoryMB,
		&vm.IPAddress, &vm.MACAddress, &vm.TAPDevice, &vm.RootfsPath,
		&vm.KernelPath, &vm.DiskPath, &vm.SnapshotPath, &vm.MemFilePath,
		&vm.SSHKeyPath, &vm.PID, &vm.SocketPath, &vm.CreatedAt, &vm.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	vm.Status = VMStatus(status)
	return vm, nil
}

// ListVMs returns all stored VMs
func (s *Store) ListVMs(ctx context.Context) ([]*VM, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, name, status, vcpus, memory_mb, ip_address, mac_address, tap_device,
	       rootfs_path, kernel_path, disk_path, snapshot_path, mem_file_path, ssh_key_path,
	       pid, socket_path, created_at, updated_at
	FROM vms ORDER BY created_at DESC;
	`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vms []*VM
	for rows.Next() {
		vm := &VM{}
		var status string
		err := rows.Scan(
			&vm.ID, &vm.Name, &status, &vm.VCPUs, &vm.MemoryMB,
			&vm.IPAddress, &vm.MACAddress, &vm.TAPDevice, &vm.RootfsPath,
			&vm.KernelPath, &vm.DiskPath, &vm.SnapshotPath, &vm.MemFilePath,
			&vm.SSHKeyPath, &vm.PID, &vm.SocketPath, &vm.CreatedAt, &vm.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		vm.Status = VMStatus(status)
		vms = append(vms, vm)
	}
	return vms, rows.Err()
}

// DeleteVM removes a VM record by ID
func (s *Store) DeleteVM(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, "DELETE FROM vms WHERE id = ?", id)
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
