package network

import (
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

func TestParseRouteOwnerScopedPath(t *testing.T) {
	p := NewIngressProxy(ProxyOptions{BaseDomains: []string{"boxes.example.com"}})

	r := httptest.NewRequest("GET", "http://daemon.local/preview/@alice/pr-42/8080/assets/app.js", nil)
	m := p.ParseRoute(r)
	if !m.IsMatch {
		t.Fatalf("owner-scoped preview path should match: %+v", m)
	}
	if m.Target != "pr-42" || m.Owner != "alice" || m.Port != 8080 {
		t.Fatalf("unexpected owner-scoped route: %+v", m)
	}
	if m.SubPath != "/assets/app.js" {
		t.Fatalf("subpath = %q, want /assets/app.js", m.SubPath)
	}
	if m.Prefix != "/preview/@alice/pr-42/8080" {
		t.Fatalf("prefix = %q, want /preview/@alice/pr-42/8080", m.Prefix)
	}

	// The legacy unscoped form keeps working and carries no owner.
	m = p.ParseRoute(httptest.NewRequest("GET", "http://daemon.local/preview/pr-42/8080/", nil))
	if !m.IsMatch || m.Owner != "" || m.Target != "pr-42" || m.Port != 8080 {
		t.Fatalf("unexpected unscoped route: %+v", m)
	}
	if m.Prefix != "/preview/pr-42/8080" {
		t.Fatalf("unscoped prefix = %q", m.Prefix)
	}

	// An empty owner segment is rejected rather than treated as a sandbox name.
	if m := p.ParseRoute(httptest.NewRequest("GET", "http://daemon.local/preview/@/pr-42/8080/", nil)); m.IsMatch {
		t.Fatalf("empty owner segment should not match: %+v", m)
	}
}

func TestParseRouteRefererFallbackForRootAssets(t *testing.T) {
	p := NewIngressProxy(ProxyOptions{BaseDomains: []string{"compute.tail1234.ts.net"}})

	r := httptest.NewRequest("GET", "https://compute.tail1234.ts.net/assets/chunk-abc.js", nil)
	r.Host = "compute.tail1234.ts.net"
	r.Header.Set("Referer", "https://compute.tail1234.ts.net/preview/web/8080/")
	m := p.ParseRoute(r)
	if !m.IsMatch || m.Target != "web" || m.Port != 8080 {
		t.Fatalf("root asset should route to the referer's sandbox: %+v", m)
	}
	if m.SubPath != "/assets/chunk-abc.js" {
		t.Fatalf("subpath = %q, want the requested path", m.SubPath)
	}
	if m.Prefix != "/preview/web/8080" {
		t.Fatalf("prefix = %q", m.Prefix)
	}

	// Owner-scoped preview URLs keep the owner so private lookups stay scoped.
	r = httptest.NewRequest("GET", "https://compute.tail1234.ts.net/api/auth/session", nil)
	r.Host = "compute.tail1234.ts.net"
	r.Header.Set("Referer", "https://compute.tail1234.ts.net/preview/@alice/web/8080/index.html")
	m = p.ParseRoute(r)
	if !m.IsMatch || m.Owner != "alice" || m.Target != "web" || m.Port != 8080 {
		t.Fatalf("owner-scoped referer route: %+v", m)
	}
	if m.SubPath != "/api/auth/session" {
		t.Fatalf("subpath = %q", m.SubPath)
	}

	// Referers from another host, or without a preview path, must not route.
	r = httptest.NewRequest("GET", "https://compute.tail1234.ts.net/assets/app.js", nil)
	r.Host = "compute.tail1234.ts.net"
	r.Header.Set("Referer", "https://evil.example/assets/app.js")
	if m := p.ParseRoute(r); m.IsMatch {
		t.Fatalf("cross-host referer must not route: %+v", m)
	}

	r = httptest.NewRequest("GET", "https://compute.tail1234.ts.net/api/v1/version", nil)
	r.Host = "compute.tail1234.ts.net"
	r.Header.Set("Referer", "https://compute.tail1234.ts.net/")
	if m := p.ParseRoute(r); m.IsMatch {
		t.Fatalf("non-preview referer must not route: %+v", m)
	}
}
