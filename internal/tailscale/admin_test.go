package tailscale

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient("tskey-api-test", "-")
	c.BaseURL = srv.URL
	c.HTTPClient = srv.Client()
	return c, srv
}

func TestCreateAuthKey(t *testing.T) {
	var gotBody keyRequest
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/api/v2/tailnet/-/keys") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer tskey-api-test" {
			t.Errorf("missing bearer auth: %q", auth)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AuthKey{ID: "k123456", Key: "tskey-auth-xyz", Ephemeral: true})
	}))

	key, err := c.CreateAuthKey(context.Background(), KeyOptions{
		Description:   "compute sandbox compute-a1b2c3d4",
		Reusable:      false,
		Ephemeral:     true,
		Preauthorized: true,
		ExpirySeconds: 3600,
		Tags:          []string{TagSandbox},
	})
	if err != nil {
		t.Fatalf("CreateAuthKey: %v", err)
	}
	if key.Key != "tskey-auth-xyz" || key.ID != "k123456" || !key.Ephemeral {
		t.Fatalf("unexpected key %+v", key)
	}
	dev := gotBody.Capabilities.Devices.Create
	if !dev.Ephemeral || !dev.Preauthorized || dev.Reusable {
		t.Errorf("expected ephemeral preauth non-reusable key, got %+v", dev)
	}
	if len(dev.Tags) != 1 || dev.Tags[0] != TagSandbox {
		t.Errorf("expected sandbox tag on key, got %v", dev.Tags)
	}
	if gotBody.ExpirySeconds != 3600 {
		t.Errorf("expected expiry 3600, got %d", gotBody.ExpirySeconds)
	}
}

func TestListDevicesAndFilter(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v2/tailnet/-/devices") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"devices": []Device{
				{ID: "1", Name: "compute-master.tail.ts.net", Hostname: "compute-master", Tags: []string{TagMaster}, Addresses: []string{"100.64.0.1"}},
				{ID: "2", Name: "laptop.tail.ts.net", Hostname: "laptop", User: "alice@example.com", Addresses: []string{"100.64.0.2"}},
				{ID: "3", Name: "compute-a1b2c3d4.tail.ts.net", Hostname: "compute-a1b2c3d4", Tags: []string{TagSandbox}, Addresses: []string{"100.64.0.3"}},
			},
		})
	}))

	devices, err := c.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devices) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(devices))
	}

	owned := FilterComputeDevices(devices, TagMaster, TagSandbox)
	if len(owned) != 2 {
		t.Fatalf("expected 2 compute devices, got %d: %+v", len(owned), owned)
	}
	if owned[0].ID != "1" || owned[1].ID != "3" {
		t.Errorf("unexpected filter result: %+v", owned)
	}
	if owned[1].TailscaleIP() != "100.64.0.3" {
		t.Errorf("TailscaleIP: %q", owned[1].TailscaleIP())
	}
	if owned[0].TailnetDomain() != "tail.ts.net" {
		t.Errorf("TailnetDomain: %q", owned[0].TailnetDomain())
	}
}

func TestDeleteDevice(t *testing.T) {
	deleted := false
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/api/v2/device/3") {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	if err := c.DeleteDevice(context.Background(), "3"); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}
	if !deleted {
		t.Error("delete request was not issued")
	}
}

func TestAPIErrorSurfacesStatus(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing scopes", http.StatusBadRequest)
	}))
	_, err := c.ListDevices(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "missing scopes") {
		t.Fatalf("expected descriptive error, got %v", err)
	}
}

func TestMissingAPIKeyFailsClosed(t *testing.T) {
	c := NewClient("", "-")
	if _, err := c.ListDevices(context.Background()); err == nil {
		t.Fatal("expected error when API key is missing")
	}
}

func TestSandboxServiceNameAndHostname(t *testing.T) {
	if got := SandboxHostname("vm-a1b2c3d4e5f6"); got != "compute-vm-a1b2c3d4e5f6" {
		t.Errorf("SandboxHostname: %q", got)
	}
	if got := SandboxServiceName("VM_A1B2C3D4E5F6", 8080); got != "compute-vm-a1b2c3d4e5f6-8080" {
		t.Errorf("SandboxServiceName: %q", got)
	}
	if got := SandboxHostname("weird name!"); got != "compute-weird-name" {
		t.Errorf("sanitize fallback: %q", got)
	}
	if got := SandboxHostname(strings.Repeat("x", 60)); got != "compute-"+strings.Repeat("x", 32) {
		t.Errorf("long-id cap: %q", got)
	}
}

func TestRecommendedPolicy(t *testing.T) {
	policy := RecommendedPolicy(TagSandbox, TagNode, TagMaster)
	for _, want := range []string{"autoApprovers", `"tag:compute-sandbox": ["tag:compute-master", "tag:compute-sandbox"]`, `"tag:compute-master"`, `"autogroup:member"`} {
		if !strings.Contains(policy, want) {
			t.Errorf("policy missing %q:\n%s", want, policy)
		}
	}
	if strings.Contains(policy, "funnel") || strings.Contains(policy, "nodeAttrs") {
		t.Error("policy must not touch funnel/nodeAttrs")
	}
}

func TestNormalizeTag(t *testing.T) {
	cases := map[string]string{
		"tag:compute:sandbox": "tag:compute-sandbox",
		"tag:compute:master":  "tag:compute-master",
		"tag:compute:node":    "tag:compute-node",
		"tag:compute-sandbox": "tag:compute-sandbox",
		"tag:custom:a:b":      "tag:custom-a-b",
		"untagged":            "untagged",
	}
	for in, want := range cases {
		if got := NormalizeTag(in); got != want {
			t.Errorf("NormalizeTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProvisionerMockedRun(t *testing.T) {
	minted := 0
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/keys") {
			minted++
			_ = json.NewEncoder(w).Encode(AuthKey{ID: "k1", Key: "tskey-auth-k1", Ephemeral: true})
			return
		}
		t.Errorf("unexpected admin request %s %s", r.Method, r.URL.Path)
	}))

	p := &Provisioner{
		Client:      c,
		AutoInstall: true,
		SSHUser:     "root",
		BootTimeout: 2 * time.Second,
	}
	// Runner is exercised through the SSHRunner only with a real ssh binary;
	// here we validate the failure path when SSH is unreachable.
	_, err := p.Provision(context.Background(), "vm-a1b2c3d4", "127.0.0.1", "/nonexistent-key")
	if err == nil {
		t.Fatal("expected error provisioning against unreachable sandbox")
	}
	if minted != 1 {
		t.Errorf("expected exactly one key mint, got %d", minted)
	}
	if !strings.Contains(err.Error(), "SSH not ready") && !strings.Contains(err.Error(), "join failed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEnsureAndDeleteService(t *testing.T) {
	var putPath, deletePath string
	var putBody ServiceDefinition
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			putPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&putBody)
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			deletePath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	if err := c.EnsureService(context.Background(), "svc:compute-vm1-8080", []string{"tcp:443"}, []string{TagSandbox}); err != nil {
		t.Fatalf("EnsureService: %v", err)
	}
	if !strings.Contains(putPath, "compute-vm1-8080") {
		t.Errorf("unexpected PUT path %q", putPath)
	}
	if putBody.Name != "svc:compute-vm1-8080" || len(putBody.Ports) != 1 || putBody.Tags[0] != TagSandbox {
		t.Errorf("unexpected PUT body %+v", putBody)
	}
	if err := c.DeleteService(context.Background(), "svc:compute-vm1-8080"); err != nil {
		t.Fatalf("DeleteService: %v", err)
	}
	if !strings.Contains(deletePath, "compute-vm1-8080") {
		t.Errorf("unexpected DELETE path %q", deletePath)
	}
}

type serviceRunner struct {
	commands []string
}

func (r *serviceRunner) Run(ctx context.Context, host, cmd string) (string, string, int, error) {
	r.commands = append(r.commands, cmd)
	switch {
	case strings.Contains(cmd, "command -v tailscale"):
		return "/usr/bin/tailscale", "", 0, nil
	case strings.Contains(cmd, "tailscale serve --service="):
		return "", "", 0, nil
	default:
		return "", "", 0, nil
	}
}

func TestPublishServiceRunsServeAndBuildsURL(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/services/"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/devices"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"devices": []Device{{Name: "compute-master.tail1234.ts.net"}},
			})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	runner := &serviceRunner{}
	p := &Provisioner{
		Client:     c,
		SandboxTag: TagSandbox,
		NewRunner:  func(string) Runner { return runner },
	}
	res, err := p.PublishService(context.Background(), "vm-a1b2c3d4", "172.16.0.9", "/key", 8080)
	if err != nil {
		t.Fatalf("PublishService: %v", err)
	}
	if res.Name != "compute-vm-a1b2c3d4-8080" {
		t.Errorf("name = %q", res.Name)
	}
	if res.DNSName != "compute-vm-a1b2c3d4-8080.tail1234.ts.net" {
		t.Errorf("dns = %q", res.DNSName)
	}
	if res.URL != "https://"+res.DNSName {
		t.Errorf("url = %q", res.URL)
	}
	var found bool
	for _, cmd := range runner.commands {
		if strings.Contains(cmd, "tailscale serve --service=") &&
			strings.Contains(cmd, "svc:compute-vm-a1b2c3d4-8080") &&
			strings.Contains(cmd, "--https=443") &&
			strings.Contains(cmd, "http://127.0.0.1:8080") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected serve command, got %v", runner.commands)
	}
}

func TestProvisionerDeleteDevice_Direct(t *testing.T) {
	var deletedDevice string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2/device/"):
			deletedDevice = strings.TrimPrefix(r.URL.Path, "/api/v2/device/")
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/services"):
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []Service{}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	p := &Provisioner{Client: c, SandboxTag: TagSandbox}
	if err := p.DeleteDevice(context.Background(), "dev-12345", "vm-test", "100.64.0.5"); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}
	if deletedDevice != "dev-12345" {
		t.Errorf("expected device dev-12345 to be deleted, got %q", deletedDevice)
	}
}

func TestProvisionerDeleteDevice_404Ignored(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2/device/"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/services"):
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []Service{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	p := &Provisioner{Client: c, SandboxTag: TagSandbox}
	if err := p.DeleteDevice(context.Background(), "dev-already-gone", "vm-test", ""); err != nil {
		t.Fatalf("expected 404 to be treated as success, got: %v", err)
	}
}

func TestProvisionerDeleteDevice_FallbackLookup(t *testing.T) {
	var deletedDevice string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/devices"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"devices": []Device{
					{
						ID:        "node-target-99",
						Hostname:  SandboxHostname("vm-fallback"),
						Tags:      []string{TagSandbox},
						Addresses: []string{"100.64.9.9"},
					},
					{
						ID:        "node-other-1",
						Hostname:  "unrelated",
						Tags:      []string{"tag:other"},
						Addresses: []string{"100.64.1.1"},
					},
				},
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2/device/"):
			deletedDevice = strings.TrimPrefix(r.URL.Path, "/api/v2/device/")
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/services"):
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []Service{}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	p := &Provisioner{Client: c, SandboxTag: TagSandbox}
	// Pass empty deviceID so it exercises fallback resolution by vmID / hostname
	if err := p.DeleteDevice(context.Background(), "", "vm-fallback", "100.64.9.9"); err != nil {
		t.Fatalf("DeleteDevice fallback: %v", err)
	}
	if deletedDevice != "node-target-99" {
		t.Errorf("expected node-target-99 deleted via fallback, got %q", deletedDevice)
	}
}

func TestProvisionerDeleteDevice_CleansServices(t *testing.T) {
	var deletedServices []string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2/device/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/services"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"services": []Service{
					{Name: SandboxServiceName("vm-svc", 8080)},
					{Name: SandboxServiceName("vm-svc", 3000)},
					{Name: "unrelated-service"},
				},
			})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/services/"):
			svc := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			deletedServices = append(deletedServices, svc)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	p := &Provisioner{Client: c, SandboxTag: TagSandbox}
	if err := p.DeleteDevice(context.Background(), "dev-svc-vm", "vm-svc", ""); err != nil {
		t.Fatalf("DeleteDevice with services: %v", err)
	}
	if len(deletedServices) == 0 {
		t.Fatal("expected published services to be deleted")
	}
	found8080 := false
	found3000 := false
	for _, s := range deletedServices {
		if strings.Contains(s, "8080") {
			found8080 = true
		}
		if strings.Contains(s, "3000") {
			found3000 = true
		}
		if strings.Contains(s, "unrelated") {
			t.Errorf("unrelated service was deleted: %s", s)
		}
	}
	if !found8080 || !found3000 {
		t.Errorf("expected both ports 8080 and 3000 services deleted, got %v", deletedServices)
	}
}

func TestListAndGetServiceVIPs(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v2/tailnet/-/services") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"vipServices": []Service{
					{Name: "svc:compute-a", Addrs: []string{"100.64.1.1", "fd7a::1"}, Ports: []string{"tcp:22"}},
					{Name: "svc:compute-b", Addrs: []string{"100.64.1.2"}, Ports: []string{"tcp:80"}},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/api/v2/tailnet/-/services/svc:compute-a") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(Service{Name: "svc:compute-a", Addrs: []string{"100.64.1.1"}, Ports: []string{"tcp:22"}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))

	services, err := c.ListServices(context.Background())
	if err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	if len(services) != 2 || services[0].VIP4() != "100.64.1.1" {
		t.Fatalf("services = %+v", services)
	}
	svc, err := c.GetService(context.Background(), "svc:compute-a")
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}
	if svc.VIP4() != "100.64.1.1" {
		t.Fatalf("VIP = %q", svc.VIP4())
	}
}

func TestSetServiceHostApproval(t *testing.T) {
	var got map[string]bool
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/api/v2/tailnet/-/services/svc:compute-a/device/n123/approved") {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	if err := c.SetServiceHostApproval(context.Background(), "svc:compute-a", "n123", true); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if !got["approved"] {
		t.Fatalf("body = %v", got)
	}
}

func TestEnsureServiceNormalizesName(t *testing.T) {
	var putPath, approvalPath string
	var putBody ServiceDefinition
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			putPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&putBody)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost:
			approvalPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	if err := c.EnsureService(context.Background(), "compute-dev-abc", []string{"tcp:22"}, []string{TagSandbox}); err != nil {
		t.Fatalf("EnsureService: %v", err)
	}
	if !strings.Contains(putPath, "/services/svc:compute-dev-abc") {
		t.Errorf("bare name not normalised in path: %q", putPath)
	}
	if putBody.Name != "svc:compute-dev-abc" {
		t.Errorf("body name = %q", putBody.Name)
	}
	if err := c.SetServiceHostApproval(context.Background(), "compute-dev-abc", "n123", true); err != nil {
		t.Fatalf("SetServiceHostApproval: %v", err)
	}
	if !strings.Contains(approvalPath, "/services/svc:compute-dev-abc/device/n123/approved") {
		t.Errorf("approval path = %q", approvalPath)
	}
}

func TestFilterComputeServices(t *testing.T) {
	in := []Service{
		{Name: "svc:compute-dev-box-abc", Tags: []string{TagSandbox}},
		{Name: "svc:cockpit", Tags: []string{"tag:server"}},
		{Name: "svc:custom", Tags: []string{TagSandbox}},
	}
	got := FilterComputeServices(in, TagSandbox, "compute-")
	if len(got) != 2 || got[0].Name != "svc:compute-dev-box-abc" || got[1].Name != "svc:custom" {
		t.Fatalf("filtered = %+v", got)
	}
}

func TestProvisionerDeleteDevice_NilClientNoOp(t *testing.T) {
	var p *Provisioner
	if err := p.DeleteDevice(context.Background(), "dev", "vm", "100.64.0.1"); err != nil {
		t.Fatalf("expected nil from nil provisioner, got %v", err)
	}
	p2 := &Provisioner{Client: nil}
	if err := p2.DeleteDevice(context.Background(), "dev", "vm", "100.64.0.1"); err != nil {
		t.Fatalf("expected nil from provisioner without client, got %v", err)
	}
}
