package gateway

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/Rashid2G/on-demand-vms/internal/tailscale"
)

type fakeAdmin struct {
	mu       sync.Mutex
	services map[string]*tailscale.Service
	approved map[string]string
	nextVIP  int
}

func newFakeAdmin() *fakeAdmin {
	return &fakeAdmin{services: map[string]*tailscale.Service{}, approved: map[string]string{}}
}

func (f *fakeAdmin) EnsureService(_ context.Context, name string, ports, tags []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.TrimPrefix(name, "svc:")
	svc, ok := f.services[key]
	if !ok {
		f.nextVIP++
		svc = &tailscale.Service{Name: key, Addrs: []string{fmt.Sprintf("100.64.0.%d", f.nextVIP)}}
		f.services[key] = svc
	}
	svc.Ports = append([]string(nil), ports...)
	svc.Tags = append([]string(nil), tags...)
	return nil
}

func (f *fakeAdmin) GetService(_ context.Context, name string) (*tailscale.Service, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.TrimPrefix(name, "svc:")
	svc, ok := f.services[key]
	if !ok {
		return nil, fmt.Errorf("404 not found")
	}
	cp := *svc
	cp.Addrs = append([]string(nil), svc.Addrs...)
	return &cp, nil
}

func (f *fakeAdmin) DeleteService(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.services, strings.TrimPrefix(name, "svc:"))
	return nil
}

func (f *fakeAdmin) SetServiceHostApproval(_ context.Context, name, deviceID string, approved bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if approved {
		f.approved[strings.TrimPrefix(name, "svc:")] = deviceID
	}
	return nil
}

type fakeServe struct {
	mu         sync.Mutex
	advertised map[string]map[int]string
	cleared    []string
	deviceID   string
}

func newFakeServe() *fakeServe {
	return &fakeServe{advertised: map[string]map[int]string{}, deviceID: "n123"}
}

func (f *fakeServe) AdvertiseTCP(_ context.Context, service string, port int, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.TrimPrefix(service, "svc:")
	if f.advertised[key] == nil {
		f.advertised[key] = map[int]string{}
	}
	f.advertised[key][port] = target
	return nil
}

func (f *fakeServe) RemoveTCP(_ context.Context, service string, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.advertised[strings.TrimPrefix(service, "svc:")]; m != nil {
		delete(m, port)
	}
	return nil
}

func (f *fakeServe) ClearService(_ context.Context, service string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.TrimPrefix(service, "svc:")
	f.cleared = append(f.cleared, key)
	delete(f.advertised, key)
	return nil
}

func (f *fakeServe) LocalDeviceID(context.Context) (string, error) { return f.deviceID, nil }

func (f *fakeServe) ports(service string) []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.advertised[strings.TrimPrefix(service, "svc:")]
	out := make([]int, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

func TestServiceName(t *testing.T) {
	a := ServiceName("compute", "Dev Box", "ins_0123456789abcdef")
	b := ServiceName("compute", "dev-box", "ins_0123456789abcdef")
	if a != b {
		t.Fatalf("service name is not stable: %q != %q", a, b)
	}
	if !strings.HasPrefix(a, "compute-dev-box-") {
		t.Fatalf("unexpected name %q", a)
	}
	if len(a) > 63 {
		t.Fatalf("name too long: %d", len(a))
	}
	c := ServiceName("compute", "dev-box", "ins_fedcba9876543210")
	if a == c {
		t.Fatalf("different sandboxes produced the same service name: %q", a)
	}
	long := ServiceName("compute", strings.Repeat("a", 100), "ins_0123456789abcdef")
	if len(long) > 63 {
		t.Fatalf("long name not truncated: %d", len(long))
	}
}

func TestSyncCreatesAndRemovesService(t *testing.T) {
	admin := newFakeAdmin()
	serve := newFakeServe()
	mgr := NewManager(Options{
		ServicePrefix: "compute",
		ServiceTag:    "tag:compute-sandbox",
		TargetHost:    "127.0.0.1",
		IncludeSSH:    true,
		AutoApprove:   true,
	}, admin, serve)

	spec := SandboxSpec{
		ID:         "dev-box",
		Name:       "dev-box",
		InternalID: "ins_0123456789abcdef",
		GuestIP:    "172.16.0.42",
		Running:    true,
		Ports:      []int{80, 3000},
	}
	if err := mgr.Sync(context.Background(), []SandboxSpec{spec}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	service := ServiceName("compute", "dev-box", "ins_0123456789abcdef")
	if !strings.HasPrefix(service, "compute-") {
		t.Fatalf("unexpected service name %q", service)
	}
	svc := admin.services[service]
	if svc == nil {
		t.Fatalf("service %q not created", service)
	}
	wantPorts := []string{"tcp:22", "tcp:80", "tcp:3000"}
	if strings.Join(svc.Ports, ",") != strings.Join(wantPorts, ",") {
		t.Fatalf("service ports = %v, want %v", svc.Ports, wantPorts)
	}
	if got := serve.ports(service); len(got) != 3 {
		t.Fatalf("advertised ports = %v, want 3", got)
	}
	if vips := mgr.VIPs("dev-box"); len(vips) != 1 || !strings.HasPrefix(vips[0], "100.") {
		t.Fatalf("VIPs = %v", vips)
	}
	if admin.approved[service] != "n123" {
		t.Fatalf("service host was not auto-approved: %v", admin.approved)
	}

	// Stopping the sandbox removes the Service and its endpoints.
	spec.Running = false
	if err := mgr.Sync(context.Background(), []SandboxSpec{spec}); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if _, ok := admin.services[service]; ok {
		t.Fatalf("service %q still exists after stop", service)
	}
	if len(serve.ports(service)) != 0 {
		t.Fatalf("serve endpoints still configured: %v", serve.ports(service))
	}
	if vips := mgr.VIPs("dev-box"); len(vips) != 0 {
		t.Fatalf("VIPs still cached after stop: %v", vips)
	}
	if !contains(serve.cleared, service) {
		t.Fatalf("service %q was not cleared", service)
	}
}

func TestSyncUpdatesPorts(t *testing.T) {
	admin := newFakeAdmin()
	serve := newFakeServe()
	mgr := NewManager(Options{TargetHost: "127.0.0.1", IncludeSSH: true}, admin, serve)

	spec := SandboxSpec{ID: "box", Name: "box", InternalID: "ins_aaaa111122223333", GuestIP: "172.16.0.9", Running: true, Ports: []int{80, 8080}}
	if err := mgr.Sync(context.Background(), []SandboxSpec{spec}); err != nil {
		t.Fatal(err)
	}
	service := ServiceName("", "box", "ins_aaaa111122223333")
	if got := serve.ports(service); len(got) != 3 {
		t.Fatalf("initial ports = %v", got)
	}

	spec.Ports = []int{80}
	if err := mgr.Sync(context.Background(), []SandboxSpec{spec}); err != nil {
		t.Fatal(err)
	}
	svc := admin.services[service]
	if strings.Join(svc.Ports, ",") != "tcp:22,tcp:80" {
		t.Fatalf("updated service ports = %v", svc.Ports)
	}
	if got := serve.ports(service); len(got) != 2 {
		t.Fatalf("updated serve ports = %v", got)
	}
}

func TestSyncSkipsStoppedAndInvalid(t *testing.T) {
	admin := newFakeAdmin()
	serve := newFakeServe()
	mgr := NewManager(Options{TargetHost: "127.0.0.1"}, admin, serve)
	for _, spec := range []SandboxSpec{
		{ID: "stopped", Name: "stopped", InternalID: "ins_1", GuestIP: "172.16.0.4"},
		{ID: "dhcp", Name: "dhcp", InternalID: "ins_2", GuestIP: "dhcp", Running: true},
	} {
		if err := mgr.Sync(context.Background(), []SandboxSpec{spec}); err != nil {
			t.Fatalf("sync %s: %v", spec.ID, err)
		}
	}
	if len(admin.services) != 0 {
		t.Fatalf("unexpected services: %v", admin.services)
	}
}

func TestSnapshotStatus(t *testing.T) {
	admin := newFakeAdmin()
	serve := newFakeServe()
	mgr := NewManager(Options{TargetHost: "127.0.0.1", IncludeSSH: true}, admin, serve)
	if err := mgr.Sync(context.Background(), []SandboxSpec{{
		ID: "box", Name: "box", InternalID: "ins_aaaa111122223333",
		GuestIP: "172.16.0.9", Running: true, Ports: []int{80},
	}}); err != nil {
		t.Fatal(err)
	}
	snaps := mgr.Snapshots()
	if len(snaps) != 1 || snaps[0].Status != "active" || len(snaps[0].VIPs) == 0 {
		t.Fatalf("snapshot = %+v", snaps)
	}
	st := mgr.Status()
	if !st.Enabled || st.Services != 1 || st.TargetAddress != "127.0.0.1" {
		t.Fatalf("status = %+v", st)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
