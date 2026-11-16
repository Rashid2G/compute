package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rashid2G/compute/internal/db"
	"github.com/Rashid2G/compute/internal/master/auth"
	"github.com/coder/websocket"
)

func TestClusterSnapshotExtendedAndRBAC(t *testing.T) {
	env := newMultiTenantServer(t)
	ctx := context.Background()

	// Create two non-admin tenants: alice and bob
	mtSignup(t, env.ts, "alice@example.com", "alice", "SecretPass!1")
	mtSignup(t, env.ts, "bob@example.com", "bob", "SecretPass!2")
	_, aliceID := mtLogin(t, env.ts, "alice", "SecretPass!1")
	_, bobID := mtLogin(t, env.ts, "bob", "SecretPass!2")

	// Seed pool, worker token, custom images, templates, and port forwards across tenants.
	if err := env.vmStore.SaveWorkerPool(ctx, &db.WorkerPool{
		ID:         "gpu-pool",
		Name:       "gpu-pool",
		LabelsJSON: "{}",
		PolicyJSON: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	rawToken := "mft_supersecrettoken1234"
	if err := env.vmStore.SaveWorkerToken(ctx, &db.WorkerToken{
		Token:      rawToken,
		Name:       "enroll-1",
		PoolID:     "gpu-pool",
		LabelsJSON: "{}",
		ExpiresAt:  time.Now().UTC().Add(time.Hour),
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.vmStore.SaveImage(ctx, &db.CustomImage{
		ID:        "img-alice",
		OwnerID:   aliceID,
		Name:      "alice-img",
		BaseImage: "ubuntu-24.04",
		Status:    db.ImageStatusReady,
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.vmStore.SaveImage(ctx, &db.CustomImage{
		ID:        "img-bob",
		OwnerID:   bobID,
		Name:      "bob-img",
		BaseImage: "ubuntu-24.04",
		Status:    db.ImageStatusReady,
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.vmStore.SaveTemplate(ctx, &db.Template{
		ID:       "tpl-alice",
		OwnerID:  aliceID,
		Name:     "alice-tpl",
		VCPUs:    2,
		MemoryMB: 1024,
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.vmStore.SaveTemplate(ctx, &db.Template{
		ID:       "tpl-bob",
		OwnerID:  bobID,
		Name:     "bob-tpl",
		VCPUs:    2,
		MemoryMB: 1024,
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.vmStore.SavePortForward(ctx, &db.PortForward{
		ID:        "pf-alice",
		OwnerID:   aliceID,
		HostPort:  19001,
		VMID:      "vm-alice",
		VMName:    "vm-alice",
		GuestPort: 80,
		Protocol:  "tcp",
		Status:    "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.vmStore.SavePortForward(ctx, &db.PortForward{
		ID:        "pf-bob",
		OwnerID:   bobID,
		HostPort:  19002,
		VMID:      "vm-bob",
		VMName:    "vm-bob",
		GuestPort: 80,
		Protocol:  "tcp",
		Status:    "pending",
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Admin snapshot includes all extended admin + global slices, with worker_tokens masked.
	adminSnap := env.server.getClusterSnapshot(ctx, &auth.AuthContext{
		AuthType: "admin_secret",
		Role:     "admin",
	})
	if adminSnap.Pools == nil || len(*adminSnap.Pools) < 2 {
		t.Fatalf("expected admin snapshot pools >= 2, got %+v", adminSnap.Pools)
	}
	if adminSnap.WorkerTokens == nil || len(*adminSnap.WorkerTokens) != 1 {
		t.Fatalf("expected 1 worker token in admin snapshot, got %+v", adminSnap.WorkerTokens)
	}
	tok := (*adminSnap.WorkerTokens)[0]
	if tok.Token == rawToken || strings.Contains(tok.Token, "supersecrettoken1234") {
		t.Fatalf("raw worker token leaked in WS snapshot: %q", tok.Token)
	}
	if !strings.HasPrefix(tok.ID, "tok_") {
		t.Fatalf("expected hashed tok_ ID for safe revocation, got %q", tok.ID)
	}
	if adminSnap.SchedulingPolicy == nil || adminSnap.SchedulingPolicy.Strategy == "" {
		t.Fatalf("expected scheduling_policy in admin snapshot, got %+v", adminSnap.SchedulingPolicy)
	}
	if adminSnap.Resources == nil || adminSnap.Resources.CPU.Cores <= 0 {
		t.Fatalf("expected resources in admin snapshot, got %+v", adminSnap.Resources)
	}
	if len(adminSnap.Templates) != 2 {
		t.Fatalf("expected 2 templates for admin, got %d", len(adminSnap.Templates))
	}
	if len(adminSnap.Images) != 3 {
		t.Fatalf("expected 3 images (base + 2 custom) for admin, got %d", len(adminSnap.Images))
	}
	if len(adminSnap.PortForwards) != 2 {
		t.Fatalf("expected 2 port forwards for admin, got %d", len(adminSnap.PortForwards))
	}

	// Verify deleting a worker token via its masked tok_ ID works.
	delResp, delBody := mtRequest(t, env.ts, http.MethodDelete, "/api/v1/worker-tokens/"+tok.ID, multiTenantAdminSecret, nil)
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE masked worker token failed: %d %s", delResp.StatusCode, string(delBody))
	}
	remainingTokens, err := env.vmStore.ListWorkerTokens(ctx)
	if err != nil || len(remainingTokens) != 0 {
		t.Fatalf("expected 0 remaining tokens after masked delete, got %d (err=%v)", len(remainingTokens), err)
	}

	// 2. Non-admin WS snapshot omits admin-only fields and isolates tenant slices.
	aliceToken, _ := mtLogin(t, env.ts, "alice", "SecretPass!1")
	wsURL := "ws" + strings.TrimPrefix(env.ts.URL, "http") + "/api/v1/ws?token=" + aliceToken
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, wsURL, nil)
	if err != nil {
		t.Fatalf("alice websocket dial failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	_, rawFrame, err := conn.Read(dialCtx)
	if err != nil {
		t.Fatalf("alice ws initial read failed: %v", err)
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(rawFrame, &rawMap); err != nil {
		t.Fatalf("decode raw ws frame: %v", err)
	}
	for _, forbiddenKey := range []string{"pools", "worker_tokens", "scheduling_policy", "resources"} {
		if _, exists := rawMap[forbiddenKey]; exists {
			t.Fatalf("non-admin WS snapshot must omit %q key, got frame: %s", forbiddenKey, string(rawFrame))
		}
	}

	var decoded struct {
		Workers      []*db.WorkerNode  `json:"workers"`
		Templates    []*db.Template    `json:"templates"`
		Images       []ImageListItem   `json:"images"`
		PortForwards []*db.PortForward `json:"port_forwards"`
	}
	if err := json.Unmarshal(rawFrame, &decoded); err != nil {
		t.Fatalf("decode alice snapshot: %v", err)
	}
	if len(decoded.Workers) != 0 {
		t.Fatalf("expected 0 workers for non-admin, got %d", len(decoded.Workers))
	}
	if len(decoded.Templates) != 1 || decoded.Templates[0].ID != "tpl-alice" {
		t.Fatalf("expected only alice template, got %+v", decoded.Templates)
	}
	if len(decoded.Images) != 2 {
		t.Fatalf("expected base + alice image (2), got %+v", decoded.Images)
	}
	for _, img := range decoded.Images {
		if img.ID == "img-bob" {
			t.Fatalf("bob private image leaked to alice WS snapshot")
		}
	}
	if len(decoded.PortForwards) != 1 || decoded.PortForwards[0].ID != "pf-alice" {
		t.Fatalf("expected only alice port forward, got %+v", decoded.PortForwards)
	}
}

func TestWSSingleWriterConcurrentBroadcast(t *testing.T) {
	env := newMultiTenantServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(env.ts.URL, "http") + "/api/v1/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": {"Bearer " + multiTenantAdminSecret},
		},
	})
	if err != nil {
		t.Fatalf("admin websocket dial failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Read initial snapshot
	_, initBytes, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("initial ws read failed: %v", err)
	}
	var initSnap struct {
		Type             string                      `json:"type"`
		Pools            *[]*db.WorkerPool           `json:"pools"`
		SchedulingPolicy *db.ClusterSchedulingPolicy `json:"scheduling_policy"`
		Resources        *adminResourcesResponse     `json:"resources"`
		Images           []ImageListItem             `json:"images"`
	}
	if err := json.Unmarshal(initBytes, &initSnap); err != nil {
		t.Fatalf("unmarshal initial snapshot: %v", err)
	}
	if initSnap.Type != "snapshot" || initSnap.Pools == nil || initSnap.SchedulingPolicy == nil || initSnap.Resources == nil || len(initSnap.Images) == 0 {
		t.Fatalf("incomplete admin WS snapshot: %+v", initSnap)
	}

	// Hammer broadcastClusterUpdate and client refresh concurrently to verify zero conn.Write races.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				env.server.broadcastClusterUpdate()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 10; j++ {
			_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"refresh"}`))
		}
	}()

	// Drain at least one triggered update and confirm valid JSON.
	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()
	_, frame, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("reading triggered ws snapshot failed: %v", err)
	}
	var nextSnap struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(frame, &nextSnap); err != nil || nextSnap.Type != "snapshot" {
		t.Fatalf("expected valid snapshot frame, got err=%v frame=%s", err, string(frame))
	}

	wg.Wait()
}
