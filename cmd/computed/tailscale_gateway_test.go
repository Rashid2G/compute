package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/db"
	servicegateway "github.com/Rashid2G/on-demand-vms/internal/gateway"
	"github.com/Rashid2G/on-demand-vms/internal/tailscale"
)

type dummyAdmin struct {
	services map[string]*tailscale.Service
}

func (d *dummyAdmin) EnsureService(ctx context.Context, name string, ports, tags []string) error {
	if d.services == nil {
		d.services = make(map[string]*tailscale.Service)
	}
	d.services[name] = &tailscale.Service{
		Name:  name,
		Addrs: []string{"100.64.42.1"},
		Ports: ports,
		Tags:  tags,
	}
	return nil
}

func (d *dummyAdmin) GetService(ctx context.Context, name string) (*tailscale.Service, error) {
	if d.services != nil && d.services[name] != nil {
		return d.services[name], nil
	}
	return &tailscale.Service{Name: name, Addrs: []string{"100.64.42.1"}}, nil
}

func (d *dummyAdmin) DeleteService(ctx context.Context, name string) error {
	delete(d.services, name)
	return nil
}

func (d *dummyAdmin) SetServiceHostApproval(ctx context.Context, name, deviceID string, approved bool) error {
	return nil
}

type dummyServe struct{}

func (d *dummyServe) AdvertiseTCP(ctx context.Context, service string, port int, target string) error {
	return nil
}

func (d *dummyServe) RemoveTCP(ctx context.Context, service string, port int) error {
	return nil
}

func (d *dummyServe) ClearService(ctx context.Context, service string) error {
	return nil
}

func (d *dummyServe) LocalDeviceID(ctx context.Context) (string, error) {
	return "node-test-123", nil
}

func TestGatewayServiceURLAndDNS(t *testing.T) {
	s := &Server{
		cfg: config.GlobalConfig{
			Tailscale: config.TailscaleConfig{
				ServiceGateway: &config.ServiceGatewayConfig{
					ServicePrefix: "compute",
				},
			},
		},
		tailnetDomain:       "tail1234.ts.net",
		tailnetDomainExpiry: time.Now().Add(time.Hour),
	}

	admin := &dummyAdmin{}
	serve := &dummyServe{}
	mgr := servicegateway.NewManager(servicegateway.Options{
		ServicePrefix: "compute",
		TargetHost:    "127.0.0.1",
	}, admin, serve)

	// Sync one spec so the manager state exists
	_ = mgr.Sync(context.Background(), []servicegateway.SandboxSpec{
		{
			ID:         "vm-12345678",
			Name:       "mybox",
			InternalID: "internal-abc",
			GuestIP:    "172.16.0.2",
			Running:    true,
			Ports:      []int{22, 80, 443},
		},
	})
	s.gateway = mgr

	vm := &db.VM{
		ID:         "vm-12345678",
		Name:       "mybox",
		InternalID: "internal-abc",
		IPAddress:  "172.16.0.2",
	}

	dns := s.gatewayServiceDNS(context.Background(), vm)
	if dns == "" {
		t.Fatal("expected non-empty gateway service DNS")
	}
	wantDNS := "compute-mybox-abc.tail1234.ts.net"
	if dns != wantDNS {
		t.Fatalf("got DNS %q, want %q", dns, wantDNS)
	}

	url80 := s.gatewayServiceURL(context.Background(), vm, 80)
	wantURL80 := "http://compute-mybox-abc.tail1234.ts.net:80"
	if url80 != wantURL80 {
		t.Fatalf("got URL %q, want %q", url80, wantURL80)
	}

	url443 := s.gatewayServiceURL(context.Background(), vm, 443)
	wantURL443 := "https://compute-mybox-abc.tail1234.ts.net:443"
	if url443 != wantURL443 {
		t.Fatalf("got URL %q, want %q", url443, wantURL443)
	}
}

func TestDecorateVMHostnamesAndAddresses(t *testing.T) {
	s := &Server{
		cfg: config.GlobalConfig{
			Tailscale: config.TailscaleConfig{
				ServiceGateway: &config.ServiceGatewayConfig{
					ServicePrefix: "compute",
				},
			},
		},
		tailnetDomain:       "tail1234.ts.net",
		tailnetDomainExpiry: time.Now().Add(time.Hour),
	}

	admin := &dummyAdmin{}
	serve := &dummyServe{}
	mgr := servicegateway.NewManager(servicegateway.Options{
		ServicePrefix: "compute",
		TargetHost:    "127.0.0.1",
	}, admin, serve)

	_ = mgr.Sync(context.Background(), []servicegateway.SandboxSpec{
		{
			ID:         "vm-99",
			Name:       "worker-vm",
			InternalID: "worker-99",
			GuestIP:    "172.16.0.5",
			Running:    true,
			Ports:      []int{22},
		},
	})
	s.gateway = mgr

	vm := &db.VM{
		ID:         "vm-99",
		Name:       "worker-vm",
		InternalID: "worker-99",
		IPAddress:  "172.16.0.5",
	}

	s.decorateVMHostnames([]*db.VM{vm})
	if vm.TailnetIP != "100.64.42.1" {
		t.Fatalf("TailnetIP = %q, want 100.64.42.1", vm.TailnetIP)
	}
	if vm.TailnetDNS != "compute-worker-vm-99.tail1234.ts.net" {
		t.Fatalf("TailnetDNS = %q", vm.TailnetDNS)
	}

	addrs := s.sandboxAddresses(vm)
	if len(addrs) < 2 {
		t.Fatalf("expected at least 2 addresses, got %v", addrs)
	}
	if !addrs[0].Equal(net.ParseIP("100.64.42.1")) {
		t.Fatalf("expected VIP first, got %v", addrs[0])
	}
	if !addrs[1].Equal(net.ParseIP("172.16.0.5")) {
		t.Fatalf("expected guest IP second, got %v", addrs[1])
	}
}

func TestHandleTailscaleStatusResponse(t *testing.T) {
	s := &Server{
		cfg: config.GlobalConfig{
			Tailscale: config.TailscaleConfig{
				Enabled: true,
				ServiceGateway: &config.ServiceGatewayConfig{
					Enabled:       true,
					ServicePrefix: "compute",
					HostTag:       "tag:compute-master",
					ServiceTag:    "tag:compute-sandbox",
				},
			},
		},
	}

	admin := &dummyAdmin{}
	serve := &dummyServe{}
	mgr := servicegateway.NewManager(servicegateway.Options{
		ServicePrefix: "compute",
		TargetHost:    "127.0.0.1",
	}, admin, serve)

	_ = mgr.Sync(context.Background(), []servicegateway.SandboxSpec{
		{
			ID:         "vm-status-1",
			Name:       "statbox",
			InternalID: "stat-1",
			GuestIP:    "172.16.0.7",
			Running:    true,
			Ports:      []int{22, 80},
		},
	})
	s.gateway = mgr

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tailscale/status", nil)
	rr := httptest.NewRecorder()

	s.handleTailscaleStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}

	var resp tailscaleStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.HostTag != "tag:compute-master" {
		t.Fatalf("HostTag = %q", resp.HostTag)
	}
	if resp.ServiceGateway == nil || !resp.ServiceGateway.Enabled {
		t.Fatalf("ServiceGateway = %+v", resp.ServiceGateway)
	}
	if len(resp.GatewayServices) != 1 {
		t.Fatalf("GatewayServices count = %d, want 1", len(resp.GatewayServices))
	}
	if resp.GatewayServices[0].Sandbox != "statbox" {
		t.Fatalf("sandbox = %q", resp.GatewayServices[0].Sandbox)
	}
	if len(resp.GatewayServices[0].VIPs) == 0 || resp.GatewayServices[0].VIPs[0] != "100.64.42.1" {
		t.Fatalf("VIPs = %v", resp.GatewayServices[0].VIPs)
	}
}

func TestHandleTailscaleEnrollRequiresAdminAndAPIKey(t *testing.T) {
	s := &Server{
		cfg: config.GlobalConfig{
			Tailscale: config.TailscaleConfig{
				Enabled: true,
			},
		},
	}

	// 1. Non-POST
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tailscale/enroll", nil)
	rr := httptest.NewRecorder()
	s.handleTailscaleEnroll(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", rr.Code)
	}

	// 2. POST without API key
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tailscale/enroll", strings.NewReader(`{}`))
	rr = httptest.NewRecorder()
	s.handleTailscaleEnroll(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 when api key missing", rr.Code)
	}
}
