package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Rashid2G/compute/internal/master/auth"
	"github.com/Rashid2G/compute/internal/master/pki"
	"github.com/Rashid2G/compute/internal/store"
	"github.com/Rashid2G/compute/internal/store/db"
	"github.com/Rashid2G/compute/pkg/api"
)

func setupTestWSServer(t *testing.T) (*httptest.Server, *Server, *auth.KeyManager, *auth.SessionManager, *db.User, string, func()) {
	tmpDir := t.TempDir()
	st, err := store.Open(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	masterKey, _ := pki.NewMasterKeyFromBytes(make([]byte, 32))
	queries := db.New(st.Writer())
	km := auth.NewKeyManager(queries, masterKey)
	sm := auth.NewSessionManager(queries, 24*time.Hour)

	ctx := context.Background()
	user := &db.User{
		ID:           store.NewUserID(),
		Email:        "wsuser@example.com",
		Username:     "wsuser",
		DisplayName:  "WS User",
		PasswordHash: "dummy",
		Role:         "admin",
		Status:       "active",
		CreatedAt:    db.NowMillis(),
		UpdatedAt:    db.NowMillis(),
		Revision:     1,
	}
	_ = queries.CreateUser(ctx, user)

	token, _, _ := km.GenerateAPIKey(ctx, user.ID, "ws-key", nil)

	wsServer := NewServer("http://example.com", km, sm)
	httpServer := httptest.NewServer(wsServer)

	cleanup := func() {
		httpServer.Close()
		st.Close()
	}

	return httpServer, wsServer, km, sm, user, token, cleanup
}

func TestWS_ConnectWithAPIKeyAndPing(t *testing.T) {
	httpServer, _, _, _, _, token, cleanup := setupTestWSServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	header := make(http.Header)
	header.Set("Authorization", "Bearer "+token)

	opts := &websocket.DialOptions{
		Subprotocols: []string{api.Subprotocol},
		HTTPHeader:   header,
	}

	conn, resp, err := websocket.Dial(ctx, wsURL, opts)
	if err != nil {
		t.Fatalf("websocket.Dial failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101 Switching Protocols, got %d", resp.StatusCode)
	}

	// 1. Read Hello message
	msgType, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read hello: %v", err)
	}
	if msgType != websocket.MessageText {
		t.Fatalf("expected text message, got %v", msgType)
	}

	var hello api.HelloMsg
	if err := json.Unmarshal(data, &hello); err != nil {
		t.Fatalf("failed to unmarshal hello msg: %v", err)
	}
	if hello.Type != api.TypeHello || hello.Session.Username != "wsuser" {
		t.Errorf("unexpected hello message content: %+v", hello)
	}

	// 2. Send ping request
	req := api.Req{
		Type: api.TypeReq,
		ID:   "req-1",
		Op:   "system.ping",
	}
	reqBytes, _ := json.Marshal(req)
	if err := conn.Write(ctx, websocket.MessageText, reqBytes); err != nil {
		t.Fatalf("failed to send ping request: %v", err)
	}

	// 3. Read pong response
	_, resData, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}
	var res api.Res
	if err := json.Unmarshal(resData, &res); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if !res.OK || res.ID != "req-1" {
		t.Errorf("unexpected response: %+v", res)
	}
}

func TestWS_CSWSHProtection(t *testing.T) {
	httpServer, _, _, sm, user, _, cleanup := setupTestWSServer(t)
	defer cleanup()

	ctx := context.Background()
	rawCookie, _, _, err := sm.CreateSession(ctx, user.ID, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	// Attempt cookie auth with evil Origin header
	header := make(http.Header)
	header.Set("Cookie", "mf_session="+rawCookie)
	header.Set("Origin", "http://evil-attacker.com")

	opts := &websocket.DialOptions{
		Subprotocols: []string{api.Subprotocol},
		HTTPHeader:   header,
	}

	dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	_, resp, err := websocket.Dial(dialCtx, wsURL, opts)
	if err == nil {
		t.Fatal("expected connection to be rejected due to CSWSH origin mismatch, but succeeded")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden, got %d", resp.StatusCode)
	}
}

func TestWS_BinaryFrameCodec(t *testing.T) {
	payload := []byte("hello stream data 12345")
	encoded := EncodeBinaryFrame(42, ChannelStdout, payload)

	decoded, err := DecodeBinaryFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeBinaryFrame failed: %v", err)
	}
	if decoded.SID != 42 {
		t.Errorf("expected SID 42, got %d", decoded.SID)
	}
	if decoded.Channel != ChannelStdout {
		t.Errorf("expected channel %d, got %d", ChannelStdout, decoded.Channel)
	}
	if string(decoded.Payload) != string(payload) {
		t.Errorf("payload mismatch: %s vs %s", decoded.Payload, payload)
	}
}
