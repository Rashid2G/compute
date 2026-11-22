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

// LegacyMigrationID is the marker key stored in legacy_migrations table.
const LegacyMigrationID = "compute_and_auth_v1"

// MigrateLegacyDatabases detects legacy compute.db and/or auth.db in dataDir,
// safely copying their records into unifiedStore in an idempotent transaction.
func MigrateLegacyDatabases(ctx context.Context, unifiedStore *Store, dataDir string) error {
	if unifiedStore == nil || dataDir == "" {
		return nil
	}

	// 1. Check idempotency: if already migrated, do nothing
	var migrated int
	err := unifiedStore.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM legacy_migrations WHERE id = ?", LegacyMigrationID).Scan(&migrated)
	if err == nil && migrated > 0 {
		return nil
	}

	// 2. Discover legacy database files
	computePath := findLegacyFile(dataDir, "compute.db")
	authPath := findLegacyFile(dataDir, "auth.db")

	// Ensure we don't copy from the unified store's own database file
	cleanUnified := filepath.Clean(unifiedStore.Path())
	if computePath != "" && filepath.Clean(computePath) == cleanUnified {
		computePath = ""
	}
	if authPath != "" && filepath.Clean(authPath) == cleanUnified {
		authPath = ""
	}

	if computePath == "" && authPath == "" {
		// Nothing to migrate; record marker so we don't re-scan needlessly
		_ = unifiedStore.WithTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO legacy_migrations (id, migrated_at, details_json) VALUES (?, CURRENT_TIMESTAMP, ?)",
				LegacyMigrationID, `{"status":"no_legacy_dbs_found"}`)
			return err
		})
		return nil
	}

	details := map[string]interface{}{
		"compute_db":  computePath,
		"auth_db":     authPath,
		"migrated_at": time.Now().UTC().Format(time.RFC3339),
	}

	// 3. Perform copy inside a single write transaction
	err = unifiedStore.WithTx(ctx, func(tx *sql.Tx) error {
		if authPath != "" {
			if err := copyLegacyDatabase(ctx, authPath, tx, []string{
				"users", "sessions", "api_keys", "organizations", "organization_members", "audit_log",
			}); err != nil {
				return fmt.Errorf("failed to copy legacy auth.db (%s): %w", authPath, err)
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
		}

		detailsBytes, _ := json.Marshal(details)
		_, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO legacy_migrations (id, migrated_at, details_json) VALUES (?, CURRENT_TIMESTAMP, ?)",
			LegacyMigrationID, string(detailsBytes))
		return err
	})

	if err != nil {
		return fmt.Errorf("legacy database migration failed: %w", err)
	}

	log.Printf("[store] Migrated legacy database(s) in %s to unified state.db", dataDir)
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

		rows, err := srcDB.QueryContext(ctx, fmt.Sprintf("SELECT * FROM %s", table))
		if err != nil {
			return fmt.Errorf("error querying table %s: %w", table, err)
		}

		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return err
		}

		placeholders := make([]string, len(cols))
		for i := range placeholders {
			placeholders[i] = "?"
		}
		insertQuery := fmt.Sprintf("INSERT OR IGNORE INTO %s (%s) VALUES (%s)",
			table, strings.Join(cols, ","), strings.Join(placeholders, ","))

		for rows.Next() {
			scanDest := make([]interface{}, len(cols))
			scanArgs := make([]interface{}, len(cols))
			for i := range scanDest {
				scanArgs[i] = &scanDest[i]
			}
			if err := rows.Scan(scanArgs...); err != nil {
				rows.Close()
				return err
			}
			if _, err := tx.ExecContext(ctx, insertQuery, scanDest...); err != nil {
				rows.Close()
				return fmt.Errorf("insert into %s failed: %w", table, err)
			}
		}
		rows.Close()
	}

	return nil
}
