package network

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func ingressReq(t *testing.T, host string) *http.Request {
	t.Helper()
	r := httptest.NewRequest("GET", "http://"+host+"/", nil)
	r.Host = host
	return r
}

func TestParseRouteSingleLabelForms(t *testing.T) {
	p := NewIngressProxy(ProxyOptions{BaseDomains: []string{"boxes.example.com"}})

	m := p.ParseRoute(ingressReq(t, "8080-myvm.boxes.example.com"))
	if !m.IsMatch || m.Target != "myvm" || m.Port != 8080 || m.Owner != "" {
		t.Fatalf("port-prefixed single label: %+v", m)
	}

	m = p.ParseRoute(ingressReq(t, "myvm-3000.boxes.example.com"))
	if !m.IsMatch || m.Target != "myvm" || m.Port != 3000 || m.Owner != "" {
		t.Fatalf("port-suffixed single label: %+v", m)
	}

	// A plain single label is not a sandbox route (no port specified).
	m = p.ParseRoute(ingressReq(t, "myvm.boxes.example.com"))
	if m.IsMatch {
		t.Fatalf("plain single label should not match: %+v", m)
	}
}

func TestParseRouteOwnerWildcardForms(t *testing.T) {
	p := NewIngressProxy(ProxyOptions{BaseDomains: []string{"compute.tail1234.ts.net"}})

	// Plain "<vm>.<username>.<zone>" → guest port 80.
	m := p.ParseRoute(ingressReq(t, "myvm.alice.compute.tail1234.ts.net"))
	if !m.IsMatch || m.Target != "myvm" || m.Owner != "alice" || m.Port != 80 {
		t.Fatalf("owner wildcard: %+v", m)
	}

	// Port-prefixed and port-suffixed variants keep the owner label.
	m = p.ParseRoute(ingressReq(t, "8080-myvm.alice.compute.tail1234.ts.net"))
	if !m.IsMatch || m.Target != "myvm" || m.Owner != "alice" || m.Port != 8080 {
		t.Fatalf("port-prefixed owner wildcard: %+v", m)
	}

	m = p.ParseRoute(ingressReq(t, "myvm-3000.alice.compute.tail1234.ts.net"))
	if !m.IsMatch || m.Target != "myvm" || m.Owner != "alice" || m.Port != 3000 {
		t.Fatalf("port-suffixed owner wildcard: %+v", m)
	}

	// Apex and reserved host-service labels never become sandbox routes.
	for _, host := range []string{
		"compute.tail1234.ts.net",
		"api.compute.tail1234.ts.net",
		"gui.compute.tail1234.ts.net",
		"www.compute.tail1234.ts.net",
	} {
		if m := p.ParseRoute(ingressReq(t, host)); m.IsMatch {
			t.Fatalf("reserved host %q should not match: %+v", host, m)
		}
	}

	// Deeper nesting is not a sandbox route.
	if m := p.ParseRoute(ingressReq(t, "a.b.alice.compute.tail1234.ts.net")); m.IsMatch {
		t.Fatalf("deep nesting should not match: %+v", m)
	}
}

type fakeServeResolver struct {
	slug   string
	target *TargetInfo
	port   int
}

func (f fakeServeResolver) ResolveTarget(ctx context.Context, idOrName string) (*TargetInfo, error) {
	return f.target, nil
}
func (f fakeServeResolver) ResolveTargetForOwner(ctx context.Context, idOrName, ownerLabel string) (*TargetInfo, error) {
	return f.target, nil
}
func (f fakeServeResolver) IsPortPublic(ctx context.Context, idOrName string, port int) bool {
	return true
}
func (f fakeServeResolver) ResolveServeSlug(ctx context.Context, slug string) (*TargetInfo, *Exposure, error) {
	if slug != f.slug {
		return nil, nil, fmt.Errorf("not found")
	}
	return f.target, &Exposure{ID: "pf_1", OwnerID: f.target.OwnerID, Slug: slug, GuestPort: f.port, Public: true}, nil
}
func (f fakeServeResolver) ResolveServeExposure(ctx context.Context, id string) (*TargetInfo, *Exposure, error) {
	if id != "pf_1" {
		return nil, nil, fmt.Errorf("not found")
	}
	return f.target, &Exposure{ID: "pf_1", OwnerID: f.target.OwnerID, Slug: f.slug, GuestPort: f.port, Public: true}, nil
}

func TestParseRouteServeSubdomain(t *testing.T) {
	resolver := fakeServeResolver{
		slug:   "quarry-3773",
		target: &TargetInfo{ID: "vm_1", Name: "quarry", OwnerID: "user_1", Status: "running", IPAddress: "172.16.0.2"},
		port:   3773,
	}
	p := NewIngressProxy(ProxyOptions{
		BaseDomains:   []string{"compute.tail1234.ts.net"},
		ServeDomains:  []string{"serves.example.com"},
		ServeResolver: resolver,
	})

	r := httptest.NewRequest("GET", "https://quarry-3773.serves.example.com/assets/chunk.js", nil)
	r.Host = "quarry-3773.serves.example.com"
	m := p.ParseRoute(r)
	if !m.IsMatch || m.Target != "quarry" || m.Port != 3773 || m.Exposure == nil {
		t.Fatalf("serve subdomain should match: %+v", m)
	}
	if m.SubPath != "/assets/chunk.js" {
		t.Fatalf("subpath = %q", m.SubPath)
	}

	// Reserved labels, nested labels and unknown slugs are not serve routes.
	for _, host := range []string{
		"api.serves.example.com",
		"a.b.serves.example.com",
		"unknown.serves.example.com",
	} {
		r := httptest.NewRequest("GET", "https://"+host+"/", nil)
		r.Host = host
		if m := p.ParseRoute(r); m.IsMatch {
			t.Fatalf("host %q should not match: %+v", host, m)
		}
	}
}
