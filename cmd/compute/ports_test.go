package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rashid2G/compute/internal/db"
)

func TestIngressURL(t *testing.T) {
	client := &DaemonClient{baseURL: "http://127.0.0.1:18080"}
	cases := map[string]string{
		"/preview/@alice/web/8080/": "http://127.0.0.1:18080/preview/@alice/web/8080/",
		"https://web.compute.dev/":  "https://web.compute.dev/",
		"":                          "",
		"relative/path":             "http://127.0.0.1:18080/relative/path",
	}
	for in, want := range cases {
		if got := ingressURL(client, in); got != want {
			t.Errorf("ingressURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitPortArgs(t *testing.T) {
	flags, positional := splitPortArgs([]string{"web", "8080", "--private", "--subdomain", "app"})
	if len(positional) != 2 || positional[0] != "web" || positional[1] != "8080" {
		t.Fatalf("positional = %v", positional)
	}
	if strings.Join(flags, " ") != "--private --subdomain app" {
		t.Fatalf("flags = %v", flags)
	}
}

func TestDaemonClientExposePort(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]interface{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sandboxes/vm-1/expose", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(&db.PortForward{
			ID:        "pf_1",
			VMID:      "vm-1",
			VMName:    "web",
			GuestPort: 8080,
			Public:    true,
			PublicURL: "/preview/@alice/web/8080/",
		})
	})
	mux.HandleFunc("/api/v1/sandboxes/vm-1/unexpose", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/v1/network/port-forwards", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("vm_id") != "vm-1" {
			t.Errorf("missing vm_id filter: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode([]*db.PortForward{{ID: "pf_1", VMID: "vm-1", GuestPort: 8080}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	client := NewDaemonClient(strings.TrimPrefix(ts.URL, "http://"))

	pf, err := client.ExposePort(context.Background(), "vm-1", 8080, true, "")
	if err != nil {
		t.Fatalf("ExposePort: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/sandboxes/vm-1/expose" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if gotBody["guest_port"].(float64) != 8080 || gotBody["public"] != true {
		t.Fatalf("body = %+v", gotBody)
	}
	if pf.PublicURL != "/preview/@alice/web/8080/" {
		t.Fatalf("public url = %q", pf.PublicURL)
	}

	pfs, err := client.ListPortForwards(context.Background(), "vm-1")
	if err != nil || len(pfs) != 1 {
		t.Fatalf("ListPortForwards = %+v, %v", pfs, err)
	}

	if err := client.UnexposePort(context.Background(), "vm-1", 8080); err != nil {
		t.Fatalf("UnexposePort: %v", err)
	}
	if gotBody["guest_port"].(float64) != 8080 {
		t.Fatalf("unexpose body = %+v", gotBody)
	}
}
