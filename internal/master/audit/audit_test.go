package audit

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Rashid2G/compute/internal/master/pki"
)

func TestAuditLogger_HashChainAndVerification(t *testing.T) {
	tmpDir := t.TempDir()
	auditDBPath := filepath.Join(tmpDir, "audit.db")

	masterKey, err := pki.NewMasterKeyFromBytes(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewMasterKeyFromBytes failed: %v", err)
	}

	logger, err := OpenLogger(auditDBPath, masterKey)
	if err != nil {
		t.Fatalf("OpenLogger failed: %v", err)
	}
	defer logger.Close()

	ctx := context.Background()

	// 1. Record 5 actions
	for i := 1; i <= 5; i++ {
		err := logger.Log(ctx, &Entry{
			TraceID:    "4bf92f3577b34da6a3ce929d0e0e4736",
			ActorID:    "usr_01J9X2",
			ActorName:  "admin",
			ActorIP:    "127.0.0.1",
			Action:     "instance.create",
			TargetKind: "instance",
			TargetID:   "ins_01J9X2",
			TargetName: "vm-1",
			Status:     "success",
		})
		if err != nil {
			t.Fatalf("failed to log entry %d: %v", i, err)
		}
	}

	// 2. Verify clean chain
	if err := logger.VerifyChain(ctx); err != nil {
		t.Fatalf("VerifyChain failed on clean log: %v", err)
	}

	// 3. Simulate tampering: modify an audit entry directly in the database
	_, err = logger.db.Exec("UPDATE audit_log SET target_name = 'hacked-vm' WHERE seq = 3")
	if err != nil {
		t.Fatalf("failed to execute tamper update: %v", err)
	}

	// 4. Assert VerifyChain detects tampering
	err = logger.VerifyChain(ctx)
	if err == nil {
		t.Fatal("expected VerifyChain to fail on tampered audit log, but got nil")
	}
}
