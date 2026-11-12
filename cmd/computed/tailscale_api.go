package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	servicegateway "github.com/Rashid2G/on-demand-vms/internal/gateway"
	"github.com/Rashid2G/on-demand-vms/internal/tailscale"
)

// tailscaleAdminClient returns an admin API client when the integration is
// configured, or nil. Callers must nil-check it.
func (s *Server) tailscaleAdminClient() *tailscale.Client {
	if s == nil || !s.cfg.Tailscale.Enabled || s.cfg.Tailscale.APIKey == "" {
		return nil
	}
	return tailscale.NewClient(s.cfg.Tailscale.APIKey, s.cfg.Tailscale.Tailnet)
}

// tailscaleStatusResponse is the payload of GET /api/v1/tailscale/status.
type tailscaleStatusResponse struct {
	Enabled               bool                         `json:"enabled"`
	APIKeyConfigured      bool                         `json:"api_key_configured"`
	Tailnet               string                       `json:"tailnet,omitempty"`
	NodeTag               string                       `json:"node_tag,omitempty"`
	SandboxTag            string                       `json:"sandbox_tag,omitempty"`
	DefaultSandboxes      bool                         `json:"default_sandboxes"`
	Host                  *tailscale.LocalStatus       `json:"host,omitempty"`
	WorkerControlPlaneURL string                       `json:"worker_control_plane_url,omitempty"`
	RecommendedPolicy     string                       `json:"recommended_policy,omitempty"`
	Sandboxes             []tailscaleSandbox           `json:"sandboxes,omitempty"`
	Services              []tailscale.Service          `json:"services,omitempty"`
	HostTag               string                       `json:"host_tag,omitempty"`
	ServiceGateway        *servicegateway.Status       `json:"service_gateway,omitempty"`
	GatewayServices       []servicegateway.ServiceInfo `json:"gateway_services,omitempty"`
}

// tailscaleSandbox summarizes a sandbox's tailnet identity.
type tailscaleSandbox struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	OwnerID    string `json:"owner_id,omitempty"`
	Status     string `json:"status"`
	TailnetIP  string `json:"tailnet_ip,omitempty"`
	TailnetDNS string `json:"tailnet_dns,omitempty"`
	DeviceID   string `json:"tailscale_device_id,omitempty"`
}

// handleTailscaleStatus reports the local host state, configuration and a
// summary of all tailnet-connected sandboxes. Read-only; it never mutates the
// tailnet.
func (s *Server) handleTailscaleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	cfg := s.cfg.Tailscale

	resp := tailscaleStatusResponse{
		Enabled:          cfg.Enabled,
		APIKeyConfigured: cfg.APIKey != "",
		Tailnet:          cfg.Tailnet,
		NodeTag:          cfg.ResolvedNodeTag(),
		SandboxTag:       cfg.ResolvedSandboxTag(),
		DefaultSandboxes: cfg.DefaultSandboxes,
		HostTag:          cfg.ResolvedHostTag(),
	}
	if s.gateway != nil {
		st := s.gateway.Status()
		resp.ServiceGateway = &st
		resp.GatewayServices = s.gateway.Snapshots()
	}

	// Local host detection never requires the admin API.
	if host, err := tailscale.DetectLocal(ctx); err == nil && host != nil {
		resp.Host = host
		if host.Running {
			resp.WorkerControlPlaneURL = host.WorkerControlPlaneHint(portFromAddr(s.cfg.Server.ListenAddr))
		}
	}

	if client := s.tailscaleAdminClient(); client != nil {
		if devices, err := client.ListDevices(ctx); err == nil {
			owned := tailscale.FilterComputeDevices(devices, cfg.ResolvedSandboxTag())
			for _, d := range owned {
				resp.Sandboxes = append(resp.Sandboxes, tailscaleSandbox{
					ID: d.Hostname, Name: d.Name, Status: "device",
					TailnetIP: d.TailscaleIP(), TailnetDNS: d.Name, DeviceID: d.ID,
				})
			}
			if svc, err := client.ListServices(ctx); err == nil {
				resp.Services = tailscale.FilterComputeServices(svc, cfg.ResolvedServiceTag(), cfg.GatewayServicePrefix()+"-")
			}
		}
		resp.RecommendedPolicy = tailscale.RecommendedPolicy(cfg.ResolvedSandboxTag(), tailscale.TagNode, cfg.ResolvedNodeTag())
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleTailscalePolicy prints the recommended policy-file snippet. Like all
// tailscale endpoints this is strictly read-only: the operator reviews the
// snippet and applies it in the Tailscale admin console.
func (s *Server) handleTailscalePolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := s.cfg.Tailscale
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"policy": tailscale.RecommendedPolicy(cfg.ResolvedSandboxTag(), tailscale.TagNode, cfg.ResolvedNodeTag()),
	})
}

// handleTailscaleCleanup removes stale Compute-owned sandbox devices from the
// tailnet. A device is stale when it is tagged tag:compute-sandbox and has not
// been seen for the given threshold. With dry_run=true (default) nothing is
// deleted. Only devices tagged with the Compute sandbox tag are ever eligible;
// the operator's own devices can never be touched by this endpoint.
func (s *Server) handleTailscaleCleanup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAdminAccess(w, r) {
		return
	}
	client := s.tailscaleAdminClient()
	if client == nil {
		httpError(w, "tailscale integration is not configured (api key missing)", http.StatusBadRequest)
		return
	}

	var body struct {
		MaxIdleSecs int `json:"max_idle_secs"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body)
	}
	maxIdle := time.Duration(body.MaxIdleSecs) * time.Second
	if maxIdle <= 0 {
		maxIdle = 30 * time.Minute
	}
	// Deletion requires an explicit confirm; otherwise this is a dry run.
	dryRun := r.URL.Query().Get("confirm") != "true"

	cfg := s.cfg.Tailscale
	ctx := r.Context()
	devices, err := client.ListDevices(ctx)
	if err != nil {
		httpError(w, "tailscale api: "+err.Error(), http.StatusBadGateway)
		return
	}

	type removed struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		LastSeen string `json:"last_seen"`
	}
	var stale []removed
	var deleted []string
	cutoff := time.Now().Add(-maxIdle)
	for _, d := range tailscale.FilterComputeDevices(devices, cfg.ResolvedSandboxTag()) {
		seen := d.LastSeen
		if seen.IsZero() {
			seen = d.Created
		}
		if seen.After(cutoff) {
			continue
		}
		stale = append(stale, removed{ID: d.ID, Name: d.Name, LastSeen: seen.Format(time.RFC3339)})
		if !dryRun {
			if err := client.DeleteDevice(ctx, d.ID); err != nil {
				httpError(w, "tailscale api delete: "+err.Error(), http.StatusBadGateway)
				return
			}
			deleted = append(deleted, d.Name)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"dry_run": dryRun,
		"stale":   stale,
		"deleted": deleted,
	})
}

// handleTailscaleEnroll tags the daemon host as the Compute Service host.
// Tailscale only accepts tagged devices as Service hosts. The operation is
// explicit: without confirm=true it reports what would change and does nothing.
func (s *Server) handleTailscaleEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireAdminAccess(w, r) {
		return
	}
	client := s.tailscaleAdminClient()
	if client == nil {
		httpError(w, "tailscale integration is not configured (api key missing)", http.StatusBadRequest)
		return
	}

	var body struct {
		Confirm bool `json:"confirm"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body)
	}
	confirm := body.Confirm || r.URL.Query().Get("confirm") == "true"

	runner := tailscale.NewServeRunner(s.cfg.Tailscale.GatewayTailscaleBinary(), s.cfg.Tailscale.GatewayTailscaleSocket())
	host, err := runner.LocalStatus(r.Context())
	if err != nil || host == nil {
		httpError(w, "cannot read the host's tailscale status (install the tailscale CLI on the daemon host and mount its control socket): "+errString(err), http.StatusBadRequest)
		return
	}
	if !host.Running {
		httpError(w, "the host is not connected to a tailnet; run 'tailscale up' on the host first", http.StatusBadRequest)
		return
	}
	if host.DeviceID == "" {
		httpError(w, "cannot determine the host's tailnet device ID", http.StatusBadRequest)
		return
	}

	want := s.cfg.Tailscale.ResolvedHostTag()
	tags := mergeTags(host.Tags, want)
	already := containsString(host.Tags, want)
	resp := map[string]interface{}{
		"dry_run":        true,
		"device_id":      host.DeviceID,
		"hostname":       host.Hostname,
		"current_tags":   host.Tags,
		"desired_tags":   tags,
		"already_tagged": already,
	}
	if already {
		resp["dry_run"] = false
		resp["message"] = "host already carries " + want
	} else if confirm {
		if err := client.SetDeviceTags(r.Context(), host.DeviceID, tags); err != nil {
			httpError(w, "tailscale api: "+err.Error(), http.StatusBadGateway)
			return
		}
		resp["dry_run"] = false
		resp["message"] = "tagged host as " + want
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func mergeTags(current []string, extra string) []string {
	seen := make(map[string]bool, len(current)+1)
	out := make([]string, 0, len(current)+1)
	for _, t := range current {
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if extra != "" && !seen[extra] {
		out = append(out, extra)
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func errString(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

// portFromAddr extracts the numeric port from a listen address.
func portFromAddr(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		p := strings.TrimSuffix(addr[i+1:], "/")
		if p != "" && p != "unix" {
			return p
		}
	}
	return "18080"
}

// workerUnderlayIP extracts the node's real network address from its
// capability map, preferring the tailnet IP when the node joined one.
func workerUnderlayIP(caps map[string]interface{}) string {
	if caps != nil {
		if v, ok := caps["tailscale_ip"].(string); ok && strings.HasPrefix(v, "100.") {
			return v
		}
	}
	return "127.0.0.1"
}
