package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// LegacyMigrationID is retained for backwards compatibility with earlier monolithic markers.
	LegacyMigrationID = "compute_and_auth_v1"
	// LegacyMigrationAuthID tracks migration of legacy auth.db independently.
	LegacyMigrationAuthID = "legacy_auth_v1"
	// LegacyMigrationComputeID tracks migration of legacy compute.db independently.
	LegacyMigrationComputeID = "legacy_compute_v1"
)

func hasMigrationMarker(ctx context.Context, unifiedStore *Store, id string) bool {
	var migrated int
	err := unifiedStore.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM legacy_migrations WHERE id = ?", id).Scan(&migrated)
	return err == nil && migrated > 0
}

// MigrateLegacyDatabases detects legacy compute.db and/or auth.db in dataDir,
// safely copying their records into unifiedStore in an idempotent transaction.
// Each legacy database is tracked with its own completion marker so migrating one
// file does not prevent migrating the other if it is restored or mounted later.
func MigrateLegacyDatabases(ctx context.Context, unifiedStore *Store, dataDir string) error {
	if unifiedStore == nil || dataDir == "" {
		return nil
	}

	authMigrated := hasMigrationMarker(ctx, unifiedStore, LegacyMigrationAuthID)
	computeMigrated := hasMigrationMarker(ctx, unifiedStore, LegacyMigrationComputeID)
	if authMigrated && computeMigrated {
		return nil
	}

	// Discover unmigrated legacy database files.
	var computePath, authPath string
	if !computeMigrated {
		computePath = findLegacyFile(dataDir, "compute.db")
	}
	if !authMigrated {
		authPath = findLegacyFile(dataDir, "auth.db")
	}

	// Ensure we don't copy from the unified store's own database file.
	cleanUnified := filepath.Clean(unifiedStore.Path())
	if computePath != "" && filepath.Clean(computePath) == cleanUnified {
		computePath = ""
	}
	if authPath != "" && filepath.Clean(authPath) == cleanUnified {
		authPath = ""
	}

	if computePath == "" && authPath == "" {
		return nil
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)

	// Perform copy inside a single write transaction.
	err := unifiedStore.WithTx(ctx, func(tx *sql.Tx) error {
		if authPath != "" {
			if err := copyLegacyDatabase(ctx, authPath, tx, []string{
				"users", "sessions", "api_keys", "organizations", "organization_members", "audit_log",
			}); err != nil {
				return fmt.Errorf("failed to copy legacy auth.db (%s): %w", authPath, err)
			}
			authDetails, _ := json.Marshal(map[string]interface{}{
				"auth_db":     authPath,
				"migrated_at": nowStr,
			})
			if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO legacy_migrations (id, migrated_at, details_json) VALUES (?, CURRENT_TIMESTAMP, ?)",
				LegacyMigrationAuthID, string(authDetails)); err != nil {
				return err
			}
		}

		if computePath != "" {
			if err := copyLegacyDatabase(ctx, computePath, tx, []string{
				"vms", "images", "templates", "volumes", "ssh_keys", "worker_pools", "worker_tokens",
				"worker_nodes", "cluster_settings", "jobs", "job_attempts", "worker_leases", "job_logs",
				"webhooks", "otel_config", "audit_events", "operations", "task_records", "access_api_keys",
				"agent_profiles", "environments", "port_forwards", "github_connections",
			}); err != nil {
				return fmt.Errorf("failed to copy legacy compute.db (%s): %w", computePath, err)
			}
			computeDetails, _ := json.Marshal(map[string]interface{}{
				"compute_db":  computePath,
				"migrated_at": nowStr,
			})
			if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO legacy_migrations (id, migrated_at, details_json) VALUES (?, CURRENT_TIMESTAMP, ?)",
				LegacyMigrationComputeID, string(computeDetails)); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("legacy database migration failed: %w", err)
	}

	log.Printf("[store] Migrated legacy database(s) in %s to unified state.db (auth=%q compute=%q)", dataDir, authPath, computePath)
	return nil
}

func findLegacyFile(baseDir, name string) string {
	candidates := []string{
		filepath.Join(baseDir, name),
		filepath.Join(baseDir, "data", name),
		filepath.Join(baseDir, "master", name),
		filepath.Join(filepath.Dir(baseDir), name),
		filepath.Join(filepath.Dir(baseDir), "data", name),
		filepath.Join(filepath.Dir(baseDir), "master", name),
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}

func tableColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var (
			cid       int
			name      string
			colType   string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		cols[strings.ToLower(name)] = true
	}
	return cols, rows.Err()
}

func copyLegacyDatabase(ctx context.Context, srcPath string, tx *sql.Tx, tables []string) error {
	srcDB, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&_pragma=query_only(ON)&_pragma=busy_timeout(5000)", srcPath))
	if err != nil {
		return err
	}
	defer srcDB.Close()

	for _, table := range tables {
		var exists string
		err := srcDB.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&exists)
		if err != nil || exists == "" {
			continue
		}

		dstCols, err := tableColumns(ctx, tx, table)
		if err != nil || len(dstCols) == 0 {
			continue
		}

		rows, err := srcDB.QueryContext(ctx, fmt.Sprintf("SELECT * FROM %s", table))
		if err != nil {
			return fmt.Errorf("error querying table %s: %w", table, err)
		}

		srcCols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return err
		}

		// Copy only columns present in the destination unified schema.
		var sharedCols []string
		var sharedIdx []int
		for i, col := range srcCols {
			if dstCols[strings.ToLower(col)] {
				sharedCols = append(sharedCols, col)
				sharedIdx = append(sharedIdx, i)
			}
		}
		if len(sharedCols) == 0 {
			rows.Close()
			continue
		}

		placeholders := make([]string, len(sharedCols))
		for i := range placeholders {
			placeholders[i] = "?"
		}
		insertQuery := fmt.Sprintf("INSERT OR IGNORE INTO %s (%s) VALUES (%s)",
			table, strings.Join(sharedCols, ","), strings.Join(placeholders, ","))

		for rows.Next() {
			scanDest := make([]interface{}, len(srcCols))
			scanArgs := make([]interface{}, len(srcCols))
			for i := range scanDest {
				scanArgs[i] = &scanDest[i]
			}
			if err := rows.Scan(scanArgs...); err != nil {
				rows.Close()
				return err
			}
			insertArgs := make([]interface{}, len(sharedIdx))
			for i, idx := range sharedIdx {
				insertArgs[i] = scanDest[idx]
			}
			if _, err := tx.ExecContext(ctx, insertQuery, insertArgs...); err != nil {
				rows.Close()
				return fmt.Errorf("insert into %s failed: %w", table, err)
			}
		}
		rows.Close()
	}

	return nil
}
