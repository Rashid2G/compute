package audit

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Rashid2G/on-demand-vms/internal/master/pki"
	"github.com/Rashid2G/on-demand-vms/internal/store"
)

const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// Entry represents an immutable event in the audit trail.
type Entry struct {
	Seq        int64  `json:"seq"`
	ID         string `json:"id"`
	Timestamp  int64  `json:"timestamp"`
	TraceID    string `json:"trace_id"`
	ActorID    string `json:"actor_id"`
	ActorName  string `json:"actor_name"`
	ActorIP    string `json:"actor_ip"`
	Action     string `json:"action"`
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
	TargetName string `json:"target_name"`
	DiffJSON   string `json:"diff_json,omitempty"`
	Status     string `json:"status"` // 'success', 'denied', 'failed'
	ErrorMsg   string `json:"error_msg,omitempty"`
	PrevHash   string `json:"prev_hash"`
	EntryHash  string `json:"entry_hash"`
}

// Logger writes and verifies tamper-evident audit logs.
type Logger struct {
	db        *sql.DB
	masterKey *pki.MasterKey
	mu        sync.Mutex
	lastHash  string
}

// OpenLogger opens or initializes audit.db with the given MasterKey.
func OpenLogger(auditDBPath string, masterKey *pki.MasterKey) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(auditDBPath), 0700); err != nil {
		return nil, fmt.Errorf("failed to create directory for audit log: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", auditDBPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open audit.db: %w", err)
	}
	db.SetMaxOpenConns(1)

	schema := `
	CREATE TABLE IF NOT EXISTS audit_log (
		seq          INTEGER PRIMARY KEY AUTOINCREMENT,
		id           TEXT NOT NULL UNIQUE,
		timestamp    INTEGER NOT NULL,
		trace_id     TEXT NOT NULL,
		actor_id     TEXT NOT NULL,
		actor_name   TEXT NOT NULL,
		actor_ip     TEXT NOT NULL,
		action       TEXT NOT NULL,
		target_kind  TEXT NOT NULL,
		target_id    TEXT NOT NULL,
		target_name  TEXT NOT NULL,
		diff_json    TEXT,
		status       TEXT NOT NULL,
		error_msg    TEXT,
		prev_hash    TEXT NOT NULL,
		entry_hash   TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_log(actor_id, timestamp);
	CREATE INDEX IF NOT EXISTS idx_audit_target ON audit_log(target_kind, target_id);
	CREATE INDEX IF NOT EXISTS idx_audit_trace ON audit_log(trace_id);
	`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to initialize audit schema: %w", err)
	}

	// Fetch current head hash
	var lastHash string
	row := db.QueryRow("SELECT entry_hash FROM audit_log ORDER BY seq DESC LIMIT 1")
	if err := row.Scan(&lastHash); err != nil {
		if err == sql.ErrNoRows {
			lastHash = GenesisHash
		} else {
			_ = db.Close()
			return nil, fmt.Errorf("failed to read audit tail: %w", err)
		}
	}

	return &Logger{
		db:        db,
		masterKey: masterKey,
		lastHash:  lastHash,
	}, nil
}

func computePayload(seq, timestamp int64, traceID, actorID, action, targetKind, targetID, targetName, status, prevHash string) string {
	return fmt.Sprintf("%d|%d|%s|%s|%s|%s|%s|%s|%s|%s", seq, timestamp, traceID, actorID, action, targetKind, targetID, targetName, status, prevHash)
}

// Log records a state-mutating action into the audit trail with hash sealing.
func (l *Logger) Log(ctx context.Context, e *Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if e.ID == "" {
		e.ID = store.NewAuditID()
	}
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().UTC().UnixMilli()
	}
	e.PrevHash = l.lastHash

	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin audit transaction: %w", err)
	}
	defer tx.Rollback()

	// Insert row to obtain auto-incrementing seq
	res, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log (
			id, timestamp, trace_id, actor_id, actor_name, actor_ip, action,
			target_kind, target_id, target_name, diff_json, status, error_msg, prev_hash, entry_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '');
	`, e.ID, e.Timestamp, e.TraceID, e.ActorID, e.ActorName, e.ActorIP, e.Action,
		e.TargetKind, e.TargetID, e.TargetName, e.DiffJSON, e.Status, e.ErrorMsg, e.PrevHash)
	if err != nil {
		return fmt.Errorf("failed to insert audit record: %w", err)
	}

	seq, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get audit seq: %w", err)
	}
	e.Seq = seq

	// Compute HMAC entry hash
	payload := computePayload(e.Seq, e.Timestamp, e.TraceID, e.ActorID, e.Action, e.TargetKind, e.TargetID, e.TargetName, e.Status, e.PrevHash)
	hmacBytes := l.masterKey.HMAC([]byte(payload))
	e.EntryHash = hex.EncodeToString(hmacBytes)

	// Update record with entry_hash
	_, err = tx.ExecContext(ctx, "UPDATE audit_log SET entry_hash = ? WHERE seq = ?", e.EntryHash, e.Seq)
	if err != nil {
		return fmt.Errorf("failed to update audit entry hash: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit audit record: %w", err)
	}

	l.lastHash = e.EntryHash
	return nil
}

// VerifyChain scans the entire audit log from seq 1 to head, checking hash chain continuity.
func (l *Logger) VerifyChain(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	rows, err := l.db.QueryContext(ctx, `
		SELECT seq, id, timestamp, trace_id, actor_id, action, target_kind, target_id, target_name, status, prev_hash, entry_hash 
		FROM audit_log ORDER BY seq ASC
	`)
	if err != nil {
		return fmt.Errorf("failed to query audit log: %w", err)
	}
	defer rows.Close()

	expectedPrev := GenesisHash
	for rows.Next() {
		var seq, timestamp int64
		var id, traceID, actorID, action, targetKind, targetID, targetName, status, prevHash, entryHash string

		if err := rows.Scan(&seq, &id, &timestamp, &traceID, &actorID, &action, &targetKind, &targetID, &targetName, &status, &prevHash, &entryHash); err != nil {
			return fmt.Errorf("failed to scan audit entry: %w", err)
		}

		if prevHash != expectedPrev {
			return fmt.Errorf("chain broken at seq %d: prev_hash %s does not match expected %s", seq, prevHash, expectedPrev)
		}

		payload := computePayload(seq, timestamp, traceID, actorID, action, targetKind, targetID, targetName, status, prevHash)
		expectedHMAC := hex.EncodeToString(l.masterKey.HMAC([]byte(payload)))
		if !hmac.Equal([]byte(entryHash), []byte(expectedHMAC)) {
			return fmt.Errorf("tamper detected at seq %d: entry_hash %s does not match computed %s", seq, entryHash, expectedHMAC)
		}

		expectedPrev = entryHash
	}

	return rows.Err()
}

// Close closes the audit database.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.db.Close()
}
