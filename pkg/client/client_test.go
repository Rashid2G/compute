package client

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/master/auth"
	"github.com/Rashid2G/on-demand-vms/internal/master/pki"
	"github.com/Rashid2G/on-demand-vms/internal/master/ws"
	"github.com/Rashid2G/on-demand-vms/internal/store"
	"github.com/Rashid2G/on-demand-vms/internal/store/db"
)

func TestClient_DialAndCall(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.Open(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	defer st.Close()

	masterKey, _ := pki.NewMasterKeyFromBytes(make([]byte, 32))
	queries := db.New(st.Writer())
	km := auth.NewKeyManager(queries, masterKey)
	sm := auth.NewSessionManager(queries, 24*time.Hour)

	ctx := context.Background()
	user := &db.User{
		ID:           store.NewUserID(),
		Email:        "client@example.com",
		Username:     "clientuser",
		DisplayName:  "Client User",
		PasswordHash: "dummy",
		Role:         "admin",
		Status:       "active",
		CreatedAt:    db.NowMillis(),
		UpdatedAt:    db.NowMillis(),
		Revision:     1,
	}
	_ = queries.CreateUser(ctx, user)

	token, _, _ := km.GenerateAPIKey(ctx, user.ID, "client-key", nil)

	wsServer := ws.NewServer("http://example.com", km, sm)
	httpServer := httptest.NewServer(wsServer)
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	client, err := Dial(dialCtx, wsURL, token)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer client.Close()

	// Verify HelloMsg
	hello := client.Hello()
	if hello.Session.Username != "clientuser" {
		t.Errorf("expected username 'clientuser', got %s", hello.Session.Username)
	}

	// RPC Ping
	callCtx, callCancel := context.WithTimeout(ctx, 2*time.Second)
	defer callCancel()

	if err := client.Ping(callCtx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	// RPC Info
	var info map[string]string
	if err := client.Call(callCtx, "system.info", nil, &info); err != nil {
		t.Fatalf("Call system.info failed: %v", err)
	}
	if info["version"] != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %s", info["version"])
	}
}
