package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Rashid2G/compute/internal/db"
)

func TestEnvironment_ValidateRepoSlug(t *testing.T) {
	// Valid repository slugs
	validCases := []struct {
		input     string
		wantOwner string
		wantName  string
	}{
		{"Rashid2G/compute", "Rashid2G", "compute"},
		{"Rashid2G/compute.git", "Rashid2G", "compute"},
		{"octocat/Hello-World", "octocat", "Hello-World"},
		{"user_123/repo.name_456", "user_123", "repo.name_456"},
		{"a/b", "a", "b"},
	}
	for _, tc := range validCases {
		owner, name, ok := validateRepoSlug(tc.input)
		if !ok {
			t.Fatalf("expected repo slug %q to be valid", tc.input)
		}
		if owner != tc.wantOwner || name != tc.wantName {
			t.Fatalf("repo slug %q parsed as %s/%s, want %s/%s", tc.input, owner, name, tc.wantOwner, tc.wantName)
		}
	}

	// Malicious and invalid repository slugs
	maliciousCases := []string{
		"owner/repo; id",
		"owner/$(whoami)",
		"owner/..",
		"../repo",
		"-o/repo",
		"owner/-repo",
		"-owner/repo",
		"owner/`whoami`",
		"owner/repo|id",
		"owner/repo&",
		"owner/repo/nested",
		"owner/repo.git; rm -rf /",
		"owner/repo\nwhoami",
		"owner/repo\twhoami",
		"owner repo",
		"owner/'repo'",
		"owner/\"repo\"",
		"owner/.",
		"./repo",
		"",
		"/",
		"onlyone",
	}
	for _, slug := range maliciousCases {
		_, _, ok := validateRepoSlug(slug)
		if ok {
			t.Fatalf("expected malicious slug %q to be rejected, but validateRepoSlug accepted it", slug)
		}
	}
}

func TestEnvironment_TenantIsolationAPI(t *testing.T) {
	env := newMultiTenantServer(t)

	// 1. Create two separate tenants
	mtSignup(t, env.ts, "alice@example.com", "alice", "Password123!")
	aliceToken, aliceID := mtLogin(t, env.ts, "alice", "Password123!")

	mtSignup(t, env.ts, "bob@example.com", "bob", "Password123!")
	bobToken, _ := mtLogin(t, env.ts, "bob", "Password123!")

	// 2. Alice creates a default environment with secrets
	aliceEnvReq := map[string]interface{}{
		"name":            "default",
		"is_default":      true,
		"secrets_enabled": true,
		"secrets":         "ALICE_SECRET=alice_super_secret_value",
	}
	respA, bodyA := mtRequest(t, env.ts, http.MethodPost, "/api/v1/environments", aliceToken, aliceEnvReq)
	if respA.StatusCode != http.StatusOK {
		t.Fatalf("Alice create environment failed: %d %s", respA.StatusCode, string(bodyA))
	}
	var aliceCreated db.EnvironmentProfile
	_ = json.Unmarshal(bodyA, &aliceCreated)
	if aliceCreated.OwnerID != aliceID {
		t.Fatalf("expected Alice environment owner_id %q, got %q", aliceID, aliceCreated.OwnerID)
	}

	// 3. Bob CANNOT read Alice's environment profile
	respBobGet, _ := mtRequest(t, env.ts, http.MethodGet, "/api/v1/environments/default", bobToken, nil)
	if respBobGet.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found when Bob tries to GET Alice's environment, got %d", respBobGet.StatusCode)
	}

	// 4. Bob CANNOT delete Alice's environment profile
	respBobDel, _ := mtRequest(t, env.ts, http.MethodDelete, "/api/v1/environments/default", bobToken, nil)
	if respBobDel.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found when Bob tries to DELETE Alice's environment, got %d", respBobDel.StatusCode)
	}

	// 5. Bob can create his OWN environment named "default" without colliding with Alice
	bobEnvReq := map[string]interface{}{
		"name":            "default",
		"is_default":      true,
		"secrets_enabled": true,
		"secrets":         "BOB_SECRET=bob_distinct_secret_value",
	}
	respBobCreate, bodyBobCreate := mtRequest(t, env.ts, http.MethodPost, "/api/v1/environments", bobToken, bobEnvReq)
	if respBobCreate.StatusCode != http.StatusOK {
		t.Fatalf("Bob create environment failed: %d %s", respBobCreate.StatusCode, string(bodyBobCreate))
	}

	// 6. Alice still reads her own environment untouched
	respAliceGet, bodyAliceGet := mtRequest(t, env.ts, http.MethodGet, "/api/v1/environments/default", aliceToken, nil)
	if respAliceGet.StatusCode != http.StatusOK {
		t.Fatalf("Alice GET environment failed: %d %s", respAliceGet.StatusCode, string(bodyAliceGet))
	}
	var aliceGot db.EnvironmentProfile
	_ = json.Unmarshal(bodyAliceGet, &aliceGot)
	if aliceGot.Secrets != "ALICE_SECRET=alice_super_secret_value" {
		t.Fatalf("Alice secrets corrupted or overwritten: %s", aliceGot.Secrets)
	}

	// 7. Bob reads his own environment
	respBobGet2, bodyBobGet2 := mtRequest(t, env.ts, http.MethodGet, "/api/v1/environments/default", bobToken, nil)
	if respBobGet2.StatusCode != http.StatusOK {
		t.Fatalf("Bob GET environment failed: %d %s", respBobGet2.StatusCode, string(bodyBobGet2))
	}
	var bobGot db.EnvironmentProfile
	_ = json.Unmarshal(bodyBobGet2, &bobGot)
	if bobGot.Secrets != "BOB_SECRET=bob_distinct_secret_value" {
		t.Fatalf("Bob secrets mismatch: %s", bobGot.Secrets)
	}

	// 8. Listing environments returns only caller's environments
	_, bodyAliceList := mtRequest(t, env.ts, http.MethodGet, "/api/v1/environments", aliceToken, nil)
	var aliceList []*db.EnvironmentProfile
	_ = json.Unmarshal(bodyAliceList, &aliceList)
	if len(aliceList) != 1 || aliceList[0].OwnerID != aliceID {
		t.Fatalf("Alice list leaked other tenant environments: %+v", aliceList)
	}
}

func TestEnvironment_ApplyEnvironmentTenantScoping(t *testing.T) {
	env := newMultiTenantServer(t)
	ctx := context.Background()

	// Alice has a default environment with secrets
	aliceEnv := &db.EnvironmentProfile{
		OwnerID:        "tenant-alice",
		Name:           "default",
		IsDefault:      true,
		SecretsEnabled: true,
		Secrets:        "ALICE_SECRET_KEY=ALICE_SECRET_VALUE",
	}
	if err := env.vmStore.SaveEnvironment(ctx, aliceEnv); err != nil {
		t.Fatalf("SaveEnvironment Alice: %v", err)
	}

	// Bob has no environment configured
	bobVM := &db.VM{
		ID:         "vm-bob-1",
		Name:       "bob-box",
		OwnerID:    "tenant-bob",
		RootfsPath: "test.ext4",
	}

	// Calling applyEnvironment for Bob's VM must NEVER select Alice's profile
	// (applyEnvironment checks ListEnvironmentsByOwner(ctx, v.OwnerID) which returns 0 rows)
	env.server.applyEnvironment(ctx, bobVM)

	// Post-boot check for Bob must return false
	if env.server.needsPostBootSSHSetup(ctx, bobVM) {
		t.Fatalf("needsPostBootSSHSetup returned true for Bob VM when only Alice had an environment!")
	}

	// Alice's VM post-boot check must return true
	aliceVM := &db.VM{
		ID:         "vm-alice-1",
		Name:       "alice-box",
		OwnerID:    "tenant-alice",
		RootfsPath: "test.ext4",
	}
	if !env.server.needsPostBootSSHSetup(ctx, aliceVM) {
		t.Fatalf("needsPostBootSSHSetup returned false for Alice VM!")
	}
}
