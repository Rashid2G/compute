package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDaemonClientEnrollTailscaleHost(t *testing.T) {
	var receivedURL string
	var receivedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		receivedMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"dry_run":      true,
			"hostname":     "test-host",
			"current_tags": []string{"tag:compute-node"},
			"desired_tags": []string{"tag:compute-node", "tag:compute-master"},
			"message":      "preview only",
		})
	}))
	defer srv.Close()

	client := NewDaemonClient(srv.URL)

	// Preview mode (confirm = false)
	res, err := client.EnrollTailscaleHost(context.Background(), false)
	if err != nil {
		t.Fatalf("EnrollTailscaleHost failed: %v", err)
	}
	if receivedMethod != http.MethodPost || receivedURL != "/api/v1/tailscale/enroll" {
		t.Fatalf("unexpected request: %s %s", receivedMethod, receivedURL)
	}
	if res["hostname"] != "test-host" || res["dry_run"] != true {
		t.Fatalf("unexpected res: %+v", res)
	}

	// Confirm mode (confirm = true)
	_, err = client.EnrollTailscaleHost(context.Background(), true)
	if err != nil {
		t.Fatalf("EnrollTailscaleHost(confirm=true) failed: %v", err)
	}
	if receivedURL != "/api/v1/tailscale/enroll?confirm=true" {
		t.Fatalf("unexpected confirm URL: %s", receivedURL)
	}
}

func TestDaemonClientGetTailscaleStatusGateway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled":  true,
			"host_tag": "tag:compute-master",
			"service_gateway": map[string]interface{}{
				"enabled":        true,
				"target_address": "172.18.0.2",
				"services":       2,
			},
			"gateway_services": []map[string]interface{}{
				{
					"sandbox": "box1",
					"service": "compute-box1-abc",
					"vips":    []string{"100.64.1.1"},
					"ports":   []int{22, 80},
					"status":  "active",
				},
			},
		})
	}))
	defer srv.Close()

	client := NewDaemonClient(srv.URL)
	st, err := client.GetTailscaleStatus(context.Background())
	if err != nil {
		t.Fatalf("GetTailscaleStatus failed: %v", err)
	}
	if st.HostTag != "tag:compute-master" {
		t.Fatalf("HostTag = %q", st.HostTag)
	}
	if st.ServiceGateway == nil || !st.ServiceGateway.Enabled || st.ServiceGateway.TargetAddress != "172.18.0.2" {
		t.Fatalf("ServiceGateway = %+v", st.ServiceGateway)
	}
	if len(st.GatewayServices) != 1 || st.GatewayServices[0].Sandbox != "box1" {
		t.Fatalf("GatewayServices = %+v", st.GatewayServices)
	}
}

func TestTailscaleCLIHelpers(t *testing.T) {
	if got := intsCSV([]int{22, 80, 443}); got != "22,80,443" {
		t.Fatalf("intsCSV = %q", got)
	}
	if got := intsCSV(nil); got != "-" {
		t.Fatalf("intsCSV(nil) = %q", got)
	}

	tags := []interface{}{"tag:one", "tag:two"}
	if got := joinStringValues(tags); got != "tag:one, tag:two" {
		t.Fatalf("joinStringValues = %q", got)
	}
	if got := joinStringValues(nil); got != "-" {
		t.Fatalf("joinStringValues(nil) = %q", got)
	}
}
