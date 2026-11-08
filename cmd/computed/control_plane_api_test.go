package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/pkg/api"
	"github.com/coder/websocket"
)

func TestControlPlane_AutoRegistration(t *testing.T) {
	server := newTestServer(t)

	// 1. Verify default pool exists
	resp, err := http.Get(server.URL + "/api/v1/pools")
	if err != nil {
		t.Fatalf("failed to list pools: %v", err)
	}
	defer resp.Body.Close()
	var pools []*db.WorkerPool
	if err := json.NewDecoder(resp.Body).Decode(&pools); err != nil {
		t.Fatalf("failed to decode pools: %v", err)
	}
	foundDefault := false
	for _, p := range pools {
		if p.ID == "default" {
			foundDefault = true
			break
		}
	}
	if !foundDefault {
		t.Fatalf("expected 'default' pool in worker_pools")
	}

	// 2. Verify local worker node is registered
	respW, err := http.Get(server.URL + "/api/v1/workers")
	if err != nil {
		t.Fatalf("failed to list workers: %v", err)
	}
	defer respW.Body.Close()
	var workers []*db.WorkerNode
	if err := json.NewDecoder(respW.Body).Decode(&workers); err != nil {
		t.Fatalf("failed to decode workers: %v", err)
	}
	var localNode *db.WorkerNode
	for _, w := range workers {
		if w.ID == "nod_host_local" {
			localNode = w
			break
		}
	}
	if localNode == nil {
		t.Fatalf("expected nod_host_local in worker_nodes")
	}
	if localNode.Status != "ready" || localNode.Role != "control-plane+worker" {
		t.Fatalf("unexpected localNode: %+v", localNode)
	}

	// 3. Verify base environment was auto-seeded
	respE, err := http.Get(server.URL + "/api/v1/environments")
	if err != nil {
		t.Fatalf("failed to list environments: %v", err)
	}
	defer respE.Body.Close()
	var envs []*db.EnvironmentProfile
	if err := json.NewDecoder(respE.Body).Decode(&envs); err != nil {
		t.Fatalf("failed to decode environments: %v", err)
	}
	if len(envs) == 0 || envs[0].Name != "base" || !envs[0].IsDefault {
		t.Fatalf("expected auto-seeded 'base' default environment, got: %+v", envs)
	}

	// 4. Verify system.daemon.started audit event was seeded
	respEvt, err := http.Get(server.URL + "/api/v1/events")
	if err != nil {
		t.Fatalf("failed to list events: %v", err)
	}
	defer respEvt.Body.Close()
	var evts []*db.AuditEvent
	if err := json.NewDecoder(respEvt.Body).Decode(&evts); err != nil {
		t.Fatalf("failed to decode events: %v", err)
	}
	if len(evts) == 0 {
		t.Fatalf("expected at least 1 audit event (daemon.started)")
	}
}

func TestControlPlane_Volumes(t *testing.T) {
	server := newTestServer(t)

	// 1. List volumes (triggers sync)
	resp, err := http.Get(server.URL + "/api/v1/volumes")
	if err != nil {
		t.Fatalf("GET /volumes failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /volumes status: %d", resp.StatusCode)
	}

	// 2. Create volume
	body, _ := json.Marshal(map[string]interface{}{
		"name":       "data-volume-1",
		"pool":       "default",
		"size_gb":    25,
		"filesystem": "ext4",
	})
	respPost, err := http.Post(server.URL+"/api/v1/volumes", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /volumes failed: %v", err)
	}
	defer respPost.Body.Close()
	if respPost.StatusCode != http.StatusCreated {
		t.Fatalf("POST /volumes status: %d", respPost.StatusCode)
	}
	var createdVol db.Volume
	if err := json.NewDecoder(respPost.Body).Decode(&createdVol); err != nil {
		t.Fatalf("decode volume failed: %v", err)
	}
	if createdVol.Name != "data-volume-1" || createdVol.SizeGB != 25 || createdVol.Status != "available" {
		t.Fatalf("unexpected created volume: %+v", createdVol)
	}

	volID := createdVol.ID

	// 3. Create a sandbox to attach the volume to.
	vmBody, _ := json.Marshal(map[string]interface{}{
		"name":      "test-vm-42",
		"vcpus":     1,
		"memory_mb": 512,
	})
	respVM, err := http.Post(server.URL+"/api/v1/vms", "application/json", bytes.NewReader(vmBody))
	if err != nil {
		t.Fatalf("create vm failed: %v", err)
	}
	defer respVM.Body.Close()
	if respVM.StatusCode != http.StatusCreated {
		t.Fatalf("POST /vms status: %d", respVM.StatusCode)
	}

	// 4. Attach volume
	attachBody, _ := json.Marshal(map[string]string{"vm_id": "test-vm-42"})
	respAttach, err := http.Post(server.URL+"/api/v1/volumes/"+volID+"/attach", "application/json", bytes.NewReader(attachBody))
	if err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	defer respAttach.Body.Close()
	var attachedVol db.Volume
	_ = json.NewDecoder(respAttach.Body).Decode(&attachedVol)
	if attachedVol.Status != "attached" || attachedVol.AttachedVM != "test-vm-42" {
		t.Fatalf("expected attached volume, got %+v", attachedVol)
	}

	// 4. Expand volume
	expandBody, _ := json.Marshal(map[string]int{"add_gb": 15})
	respExpand, err := http.Post(server.URL+"/api/v1/volumes/"+volID+"/expand", "application/json", bytes.NewReader(expandBody))
	if err != nil {
		t.Fatalf("expand failed: %v", err)
	}
	defer respExpand.Body.Close()
	var expandedVol db.Volume
	_ = json.NewDecoder(respExpand.Body).Decode(&expandedVol)
	if expandedVol.SizeGB != 40 {
		t.Fatalf("expected size 40, got %d", expandedVol.SizeGB)
	}

	// 5. Snapshot volume
	respSnap, err := http.Post(server.URL+"/api/v1/volumes/"+volID+"/snapshot", "application/json", nil)
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	defer respSnap.Body.Close()
	var snapVol db.Volume
	_ = json.NewDecoder(respSnap.Body).Decode(&snapVol)
	if snapVol.Snapshots != 1 {
		t.Fatalf("expected 1 snapshot, got %d", snapVol.Snapshots)
	}

	// 6. Detach volume
	respDetach, err := http.Post(server.URL+"/api/v1/volumes/"+volID+"/detach", "application/json", nil)
	if err != nil {
		t.Fatalf("detach failed: %v", err)
	}
	defer respDetach.Body.Close()
	var detachedVol db.Volume
	_ = json.NewDecoder(respDetach.Body).Decode(&detachedVol)
	if detachedVol.Status != "available" || detachedVol.AttachedVM != "" {
		t.Fatalf("expected available volume, got %+v", detachedVol)
	}

	// 7. Delete volume
	reqDel, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/volumes/"+volID, nil)
	respDel, err := http.DefaultClient.Do(reqDel)
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	defer respDel.Body.Close()
	if respDel.StatusCode != http.StatusOK {
		t.Fatalf("delete status: %d", respDel.StatusCode)
	}
}

func TestControlPlane_Webhooks(t *testing.T) {
	server := newTestServer(t)

	// A local receiver verifies that deliveries actually leave the daemon.
	var deliveries atomic.Int32
	var signature atomic.Value
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deliveries.Add(1)
		signature.Store(r.Header.Get("X-Compute-Signature"))
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	// 1. Create webhook
	body, _ := json.Marshal(map[string]interface{}{
		"url":         receiver.URL,
		"description": "Production Alerting",
		"events":      []string{"sandbox.started", "sandbox.stopped"},
	})
	respPost, err := http.Post(server.URL+"/api/v1/webhooks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /webhooks failed: %v", err)
	}
	defer respPost.Body.Close()
	if respPost.StatusCode != http.StatusCreated {
		t.Fatalf("POST status: %d", respPost.StatusCode)
	}
	var hook db.WebhookEndpoint
	_ = json.NewDecoder(respPost.Body).Decode(&hook)
	if hook.URL != receiver.URL || len(hook.Events) != 2 || hook.Secret == "" {
		t.Fatalf("unexpected hook: %+v", hook)
	}

	whID := hook.ID

	// 2. Patch webhook
	newDesc := "Updated Description"
	patchBody, _ := json.Marshal(map[string]interface{}{
		"description": newDesc,
	})
	reqPatch, _ := http.NewRequest(http.MethodPatch, server.URL+"/api/v1/webhooks/"+whID, bytes.NewReader(patchBody))
	respPatch, err := http.DefaultClient.Do(reqPatch)
	if err != nil {
		t.Fatalf("PATCH failed: %v", err)
	}
	defer respPatch.Body.Close()
	var patchedHook db.WebhookEndpoint
	_ = json.NewDecoder(respPatch.Body).Decode(&patchedHook)
	if patchedHook.Description != newDesc {
		t.Fatalf("expected description %q, got %q", newDesc, patchedHook.Description)
	}

	// 3. Test webhook
	respTest, err := http.Post(server.URL+"/api/v1/webhooks/"+whID+"/test", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /test failed: %v", err)
	}
	defer respTest.Body.Close()
	var testRes map[string]interface{}
	_ = json.NewDecoder(respTest.Body).Decode(&testRes)
	if testRes["status"] != "ok" || int(testRes["code"].(float64)) != 200 {
		t.Fatalf("unexpected test res: %+v", testRes)
	}
	if deliveries.Load() != 1 {
		t.Fatalf("expected 1 webhook delivery, got %d", deliveries.Load())
	}
	if sig, _ := signature.Load().(string); !strings.HasPrefix(sig, "sha256=") {
		t.Fatalf("expected HMAC signature, got %q", sig)
	}

	// 4. Delete webhook
	reqDel, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/webhooks/"+whID, nil)
	respDel, err := http.DefaultClient.Do(reqDel)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	defer respDel.Body.Close()
	if respDel.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status: %d", respDel.StatusCode)
	}
}

func TestControlPlane_OTel(t *testing.T) {
	server := newTestServer(t)

	var traces atomic.Int32
	var lastPath atomic.Value
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" {
			traces.Add(1)
			lastPath.Store(r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	// 1. GET initial otel config
	resp, err := http.Get(server.URL + "/api/v1/otel")
	if err != nil {
		t.Fatalf("GET /otel failed: %v", err)
	}
	defer resp.Body.Close()
	var cfg db.OTelConfig
	_ = json.NewDecoder(resp.Body).Decode(&cfg)
	if cfg.ServiceName != "on-demand-vms" {
		t.Fatalf("unexpected default service name: %s", cfg.ServiceName)
	}

	// 2. PUT update otel config
	cfg.Enabled = true
	cfg.Endpoint = collector.URL
	cfg.ServiceName = "custom-compute"
	putBody, _ := json.Marshal(cfg)
	reqPut, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/otel", bytes.NewReader(putBody))
	respPut, err := http.DefaultClient.Do(reqPut)
	if err != nil {
		t.Fatalf("PUT /otel failed: %v", err)
	}
	defer respPut.Body.Close()
	var updatedCfg db.OTelConfig
	_ = json.NewDecoder(respPut.Body).Decode(&updatedCfg)
	if !updatedCfg.Enabled || updatedCfg.Endpoint != collector.URL {
		t.Fatalf("unexpected updated cfg: %+v", updatedCfg)
	}

	// 3. POST test export
	respTest, err := http.Post(server.URL+"/api/v1/otel/test", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /otel/test failed: %v", err)
	}
	defer respTest.Body.Close()
	var testCfg db.OTelConfig
	_ = json.NewDecoder(respTest.Body).Decode(&testCfg)
	if testCfg.SpansExported <= 0 || testCfg.LastExportAt == "Never" {
		t.Fatalf("expected spans exported, got: %+v", testCfg)
	}
	if traces.Load() != 1 {
		t.Fatalf("expected one OTLP export, got %d", traces.Load())
	}
	if path, _ := lastPath.Load().(string); path != "/v1/traces" {
		t.Fatalf("unexpected OTLP path %q", path)
	}
}

func TestControlPlane_EventsAndSSE(t *testing.T) {
	server := newTestServer(t)

	// 1. Connect SSE client
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reqSSE, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/events/stream", nil)
	respSSE, err := http.DefaultClient.Do(reqSSE)
	if err != nil {
		t.Fatalf("failed to open SSE stream: %v", err)
	}
	defer respSSE.Body.Close()

	reader := bufio.NewReader(respSSE.Body)
	// Read initial : connected
	firstLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read from SSE: %v", err)
	}
	if !strings.Contains(firstLine, "connected") {
		t.Fatalf("expected initial connected comment, got %q", firstLine)
	}

	// 2. Create custom event via POST /api/v1/events
	postBody, _ := json.Marshal(map[string]interface{}{
		"event":       "custom.probe.ping",
		"actor":       "tester",
		"target":      "cluster",
		"status":      "delivered",
		"duration_ms": 12,
		"payload":     map[string]string{"foo": "bar"},
	})
	respPost, err := http.Post(server.URL+"/api/v1/events", "application/json", bytes.NewReader(postBody))
	if err != nil {
		t.Fatalf("POST /events failed: %v", err)
	}
	defer respPost.Body.Close()
	if respPost.StatusCode != http.StatusCreated {
		t.Fatalf("POST /events status: %d", respPost.StatusCode)
	}

	// 3. Receive event over SSE
	var eventData string
	for i := 0; i < 5; i++ {
		line, rErr := reader.ReadString('\n')
		if rErr != nil {
			t.Fatalf("read SSE error: %v", rErr)
		}
		if strings.HasPrefix(line, "data: ") {
			eventData = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	if eventData == "" {
		t.Fatalf("did not receive data line over SSE")
	}
	var evt db.AuditEvent
	if err := json.Unmarshal([]byte(eventData), &evt); err != nil {
		t.Fatalf("failed to decode SSE event: %v", err)
	}
	if evt.Event != "custom.probe.ping" {
		t.Fatalf("expected event custom.probe.ping, got %s", evt.Event)
	}
}

func TestControlPlane_AccessAPIKeys(t *testing.T) {
	server := newTestServer(t)

	// 1. Create API key
	postBody, _ := json.Marshal(map[string]string{
		"name":  "CI Runner Key",
		"scope": "vms:write,ci:runner",
	})
	resp, err := http.Post(server.URL+"/api/v1/api-keys", "application/json", bytes.NewReader(postBody))
	if err != nil {
		t.Fatalf("POST /api-keys failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api-keys status: %d", resp.StatusCode)
	}
	var key db.AccessAPIKey
	_ = json.NewDecoder(resp.Body).Decode(&key)
	if !strings.HasPrefix(key.Token, "cmp_live_") || key.Name != "CI Runner Key" {
		t.Fatalf("unexpected API key: %+v", key)
	}

	// 2. List API keys
	respList, err := http.Get(server.URL + "/api/v1/api-keys")
	if err != nil {
		t.Fatalf("GET /api-keys failed: %v", err)
	}
	defer respList.Body.Close()
	var keys []*db.AccessAPIKey
	_ = json.NewDecoder(respList.Body).Decode(&keys)
	if len(keys) != 1 {
		t.Fatalf("expected 1 API key, got %d", len(keys))
	}

	// 3. Delete API key
	reqDel, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/api-keys/"+key.ID, nil)
	respDel, err := http.DefaultClient.Do(reqDel)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	defer respDel.Body.Close()
	if respDel.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status: %d", respDel.StatusCode)
	}
}

func TestControlPlane_AgentProfiles(t *testing.T) {
	server := newTestServer(t)

	// 1. Create agent profile
	body, _ := json.Marshal(map[string]string{
		"name":          "Anthropic Proxy",
		"provider":      "Anthropic",
		"upstream_url":  "https://api.anthropic.com/v1",
		"inject_header": "x-api-key: secret",
		"secret_ref":    "sec_anthropic_prod",
	})
	resp, err := http.Post(server.URL+"/api/v1/agent-profiles", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /agent-profiles failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var prof db.AgentProfile
	_ = json.NewDecoder(resp.Body).Decode(&prof)
	if prof.Name != "Anthropic Proxy" || prof.Status != "active" {
		t.Fatalf("unexpected profile: %+v", prof)
	}

	// 2. Patch agent profile
	newURL := "https://api.anthropic.com/v2"
	patchBody, _ := json.Marshal(map[string]string{
		"upstream_url": newURL,
	})
	reqPatch, _ := http.NewRequest(http.MethodPatch, server.URL+"/api/v1/agent-profiles/"+prof.ID, bytes.NewReader(patchBody))
	respPatch, err := http.DefaultClient.Do(reqPatch)
	if err != nil {
		t.Fatalf("PATCH failed: %v", err)
	}
	defer respPatch.Body.Close()
	var patched db.AgentProfile
	_ = json.NewDecoder(respPatch.Body).Decode(&patched)
	if patched.UpstreamURL != newURL {
		t.Fatalf("expected updated upstream_url %q, got %q", newURL, patched.UpstreamURL)
	}

	// 3. Delete agent profile
	reqDel, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/agent-profiles/"+prof.ID, nil)
	respDel, err := http.DefaultClient.Do(reqDel)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	defer respDel.Body.Close()
	if respDel.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status: %d", respDel.StatusCode)
	}
}

func TestControlPlane_Environments(t *testing.T) {
	server := newTestServer(t)

	// 1. GET list - auto-seeds base
	resp, err := http.Get(server.URL + "/api/v1/environments")
	if err != nil {
		t.Fatalf("GET /environments failed: %v", err)
	}
	defer resp.Body.Close()
	var envs []*db.EnvironmentProfile
	_ = json.NewDecoder(resp.Body).Decode(&envs)
	if len(envs) != 1 || envs[0].Name != "base" || !envs[0].IsDefault {
		t.Fatalf("expected base default environment, got: %+v", envs)
	}

	// 2. Create second environment with is_default: true
	body, _ := json.Marshal(map[string]interface{}{
		"name":               "production",
		"is_default":         true,
		"secrets_enabled":    true,
		"secrets":            "ENV=prod",
		"credential_profile": "Prod Default",
		"version":            1,
	})
	respPost, err := http.Post(server.URL+"/api/v1/environments", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /environments failed: %v", err)
	}
	defer respPost.Body.Close()
	var prodEnv db.EnvironmentProfile
	_ = json.NewDecoder(respPost.Body).Decode(&prodEnv)
	if prodEnv.Name != "production" || !prodEnv.IsDefault {
		t.Fatalf("unexpected prod env: %+v", prodEnv)
	}

	// 3. Verify base is no longer default
	respList, _ := http.Get(server.URL + "/api/v1/environments")
	defer respList.Body.Close()
	var allEnvs []*db.EnvironmentProfile
	_ = json.NewDecoder(respList.Body).Decode(&allEnvs)
	for _, e := range allEnvs {
		if e.Name == "base" && e.IsDefault {
			t.Fatalf("base environment should not be default after setting production as default")
		}
	}

	// 4. Delete production environment
	reqDel, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/environments/production", nil)
	respDel, err := http.DefaultClient.Do(reqDel)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	defer respDel.Body.Close()
	if respDel.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status: %d", respDel.StatusCode)
	}
}

func TestControlPlane_PortForwards(t *testing.T) {
	server := newTestServer(t)

	// 1. Create port forward
	body, _ := json.Marshal(map[string]interface{}{
		"host_port":  9080,
		"vm_id":      "vm-web-1",
		"guest_port": 80,
		"protocol":   "tcp",
		"subdomain":  "web1",
	})
	resp, err := http.Post(server.URL+"/api/v1/network/port-forwards", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /port-forwards failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var pf db.PortForward
	_ = json.NewDecoder(resp.Body).Decode(&pf)
	if pf.HostPort != 9080 || pf.PublicURL != "https://web1.compute.dev" {
		t.Fatalf("unexpected port forward: %+v", pf)
	}

	// 2. List port forwards
	respList, err := http.Get(server.URL + "/api/v1/network/port-forwards")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer respList.Body.Close()
	var pfs []*db.PortForward
	_ = json.NewDecoder(respList.Body).Decode(&pfs)
	if len(pfs) != 1 {
		t.Fatalf("expected 1 port forward, got %d", len(pfs))
	}

	// 3. Delete port forward
	reqDel, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/network/port-forwards/"+pf.ID, nil)
	respDel, err := http.DefaultClient.Do(reqDel)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	defer respDel.Body.Close()
	if respDel.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status: %d", respDel.StatusCode)
	}
}

func TestControlPlane_WorkerActionsAndTokens(t *testing.T) {
	server := newTestServer(t)

	// 1. Cordon worker
	respCordon, err := http.Post(server.URL+"/api/v1/workers/nod_host_local/cordon", "application/json", nil)
	if err != nil {
		t.Fatalf("cordon failed: %v", err)
	}
	defer respCordon.Body.Close()
	var resMap map[string]string
	_ = json.NewDecoder(respCordon.Body).Decode(&resMap)
	if resMap["status"] != "cordoned" {
		t.Fatalf("expected cordoned status, got %+v", resMap)
	}

	// 2. Drain worker
	respDrain, _ := http.Post(server.URL+"/api/v1/workers/nod_host_local/drain", "application/json", nil)
	defer respDrain.Body.Close()
	_ = json.NewDecoder(respDrain.Body).Decode(&resMap)
	if resMap["status"] != "draining" {
		t.Fatalf("expected draining status, got %+v", resMap)
	}

	// 3. Uncordon worker
	respUncordon, _ := http.Post(server.URL+"/api/v1/workers/nod_host_local/uncordon", "application/json", nil)
	defer respUncordon.Body.Close()
	_ = json.NewDecoder(respUncordon.Body).Decode(&resMap)
	if resMap["status"] != "ready" {
		t.Fatalf("expected ready status, got %+v", resMap)
	}

	// 4. Reconcile all workers
	respRecAll, err := http.Post(server.URL+"/api/v1/workers/reconcile", "application/json", nil)
	if err != nil {
		t.Fatalf("reconcile all failed: %v", err)
	}
	defer respRecAll.Body.Close()
	var recAllRes map[string]interface{}
	_ = json.NewDecoder(respRecAll.Body).Decode(&recAllRes)
	if recAllRes["status"] != "reconciled" || int(recAllRes["count"].(float64)) <= 0 {
		t.Fatalf("unexpected reconcile all res: %+v", recAllRes)
	}

	// 5. Worker Tokens CRUD
	tokenBody, _ := json.Marshal(map[string]string{
		"name":    "ci-enroll-token",
		"pool_id": "default",
	})
	respTok, err := http.Post(server.URL+"/api/v1/worker-tokens", "application/json", bytes.NewReader(tokenBody))
	if err != nil {
		t.Fatalf("POST worker token failed: %v", err)
	}
	defer respTok.Body.Close()
	var tokResp api.WorkerTokenResponse
	_ = json.NewDecoder(respTok.Body).Decode(&tokResp)
	if tokResp.Name != "ci-enroll-token" || tokResp.PoolID != "default" {
		t.Fatalf("unexpected token resp: %+v", tokResp)
	}

	// 6. List worker tokens
	respListTok, err := http.Get(server.URL + "/api/v1/worker-tokens")
	if err != nil {
		t.Fatalf("GET worker tokens failed: %v", err)
	}
	defer respListTok.Body.Close()
	var tokList []api.WorkerTokenResponse
	_ = json.NewDecoder(respListTok.Body).Decode(&tokList)
	if len(tokList) != 1 || tokList[0].Token != tokResp.Token {
		t.Fatalf("expected 1 token in list, got %+v", tokList)
	}

	// 7. Delete worker token
	reqDelTok, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/worker-tokens/"+tokResp.Token, nil)
	respDelTok, err := http.DefaultClient.Do(reqDelTok)
	if err != nil {
		t.Fatalf("DELETE worker token failed: %v", err)
	}
	defer respDelTok.Body.Close()
	if respDelTok.StatusCode != http.StatusOK {
		t.Fatalf("DELETE worker token status: %d", respDelTok.StatusCode)
	}
}

func TestControlPlane_ClusterSnapshot(t *testing.T) {
	server := newTestServer(t)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("WebSocket read failed: %v", err)
	}

	var snap ClusterSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("failed to decode snapshot: %v", err)
	}

	if snap.Type != "snapshot" {
		t.Fatalf("expected snapshot type, got %s", snap.Type)
	}
	if snap.Workers == nil || snap.Jobs == nil || snap.Volumes == nil || snap.Events == nil {
		t.Fatalf("expected snapshot to include Workers, Jobs, Volumes, and Events slices; got: workers=%v, jobs=%v, volumes=%v, events=%v",
			snap.Workers, snap.Jobs, snap.Volumes, snap.Events)
	}
}
