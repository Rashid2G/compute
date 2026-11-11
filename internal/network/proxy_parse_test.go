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
	p := NewIngressProxy(ProxyOptions{BaseDomains: []string{"compute-lab.tail1234.ts.net"}})

	// Plain "<vm>.<username>.<zone>" → guest port 80.
	m := p.ParseRoute(ingressReq(t, "myvm.alice.compute-lab.tail1234.ts.net"))
	if !m.IsMatch || m.Target != "myvm" || m.Owner != "alice" || m.Port != 80 {
		t.Fatalf("owner wildcard: %+v", m)
	}

	// Port-prefixed and port-suffixed variants keep the owner label.
	m = p.ParseRoute(ingressReq(t, "8080-myvm.alice.compute-lab.tail1234.ts.net"))
	if !m.IsMatch || m.Target != "myvm" || m.Owner != "alice" || m.Port != 8080 {
		t.Fatalf("port-prefixed owner wildcard: %+v", m)
	}

	m = p.ParseRoute(ingressReq(t, "myvm-3000.alice.compute-lab.tail1234.ts.net"))
	if !m.IsMatch || m.Target != "myvm" || m.Owner != "alice" || m.Port != 3000 {
		t.Fatalf("port-suffixed owner wildcard: %+v", m)
	}

	// Apex and reserved host-service labels never become sandbox routes.
	for _, host := range []string{
		"compute-lab.tail1234.ts.net",
		"api.compute-lab.tail1234.ts.net",
		"gui.compute-lab.tail1234.ts.net",
		"www.compute-lab.tail1234.ts.net",
	} {
		if m := p.ParseRoute(ingressReq(t, host)); m.IsMatch {
			t.Fatalf("reserved host %q should not match: %+v", host, m)
		}
	}

	// Deeper nesting is not a sandbox route.
	if m := p.ParseRoute(ingressReq(t, "a.b.alice.compute-lab.tail1234.ts.net")); m.IsMatch {
		t.Fatalf("deep nesting should not match: %+v", m)
	}
}
