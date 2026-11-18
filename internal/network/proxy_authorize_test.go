package network

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthorizeBaseURLForRequestPrefersForwardedPublicHost(t *testing.T) {
	p := NewIngressProxy(ProxyOptions{AuthorizeBaseURL: "http://127.0.0.1:18080"})
	req := httptest.NewRequest(http.MethodGet, "http://preview.localhost/preview/vm1/8080/", nil)
	req.Host = "127.0.0.1:18080"
	req.Header.Set("X-Forwarded-Host", "compute-lab.tailb1be99.ts.net")
	req.Header.Set("X-Forwarded-Proto", "https")

	if got, want := p.authorizeBaseURLForRequest(req), "https://compute-lab.tailb1be99.ts.net"; got != want {
		t.Fatalf("authorizeBaseURLForRequest = %q, want %q", got, want)
	}
}

func TestAuthorizeBaseURLForRequestKeepsLoopbackForLocalRequests(t *testing.T) {
	p := NewIngressProxy(ProxyOptions{AuthorizeBaseURL: "http://127.0.0.1:18080"})
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18080/preview/vm1/8080/", nil)

	if got, want := p.authorizeBaseURLForRequest(req), "http://127.0.0.1:18080"; got != want {
		t.Fatalf("authorizeBaseURLForRequest = %q, want %q", got, want)
	}
}

func TestAuthorizeBaseURLForRequestKeepsExplicitPublicBase(t *testing.T) {
	p := NewIngressProxy(ProxyOptions{AuthorizeBaseURL: "https://compute.example.com"})
	req := httptest.NewRequest(http.MethodGet, "http://preview.localhost/preview/vm1/8080/", nil)
	req.Host = "preview.localhost"

	if got, want := p.authorizeBaseURLForRequest(req), "https://compute.example.com"; got != want {
		t.Fatalf("authorizeBaseURLForRequest = %q, want %q", got, want)
	}
}
