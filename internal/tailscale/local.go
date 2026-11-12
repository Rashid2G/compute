package tailscale

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultTailscaleBinary is the local tailscale CLI used for detection.
const DefaultTailscaleBinary = "tailscale"

// LocalStatus describes the state of the tailscale client on the local host.
type LocalStatus struct {
	Installed   bool     `json:"installed"`
	Running     bool     `json:"running"`
	Hostname    string   `json:"hostname"`   // device hostname
	DNSName     string   `json:"dns_name"`   // full MagicDNS name, e.g. compute-master.tail1234.ts.net
	TailnetIP   string   `json:"tailnet_ip"` // IPv4 tailnet address (100.x.y.z)
	TailnetName string   `json:"tailnet_name"`
	Tags        []string `json:"tags"`
	Version     string   `json:"version"`
	DeviceID    string   `json:"device_id,omitempty"` // tailnet device ID (n123...)
}

// TailnetDomain returns the MagicDNS domain of the local device.
func (s *LocalStatus) TailnetDomain() string {
	parts := strings.SplitN(strings.TrimSuffix(s.DNSName, "."), ".", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return ""
}

// selfStatus mirrors the "Self" object of `tailscale status --json`.
type selfStatus struct {
	ID           string   `json:"ID"`
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	Online       bool     `json:"Online"`
	Tags         []string `json:"Tags"`
}

type statusOutput struct {
	Version        string     `json:"Version"`
	Self           selfStatus `json:"Self"`
	CurrentTailnet struct {
		Name string `json:"Name"`
	} `json:"CurrentTailnet"`
	BackendState string `json:"BackendState"`
}

// DetectLocal inspects the local tailscale client using the tailscale CLI.
// It returns a status with Installed=false when the CLI is missing, and
// Running=false when tailscaled is not connected to a tailnet.
func DetectLocal(ctx context.Context) (*LocalStatus, error) {
	return DetectLocalWith(ctx, DefaultTailscaleBinary, 5*time.Second)
}

// DetectLocalWith is the injectable variant used by tests.
func DetectLocalWith(ctx context.Context, binary string, timeout time.Duration) (*LocalStatus, error) {
	st := &LocalStatus{}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil {
		if isNotFound(err) {
			return st, nil // not installed; not an error for callers
		}
		return st, nil // treat any other failure as "present but unusable"
	}
	st.Installed = true
	st.Version = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])

	out, err = exec.CommandContext(ctx, binary, "status", "--json").Output()
	if err != nil {
		return st, nil
	}
	var parsed statusOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return st, fmt.Errorf("tailscale: parse status output: %w", err)
	}
	if parsed.BackendState == "NeedsLogin" || parsed.BackendState == "NoState" ||
		parsed.BackendState == "Starting" && len(parsed.Self.TailscaleIPs) == 0 {
		return st, nil
	}
	st.Running = parsed.BackendState == "Running" || len(parsed.Self.TailscaleIPs) > 0
	st.Hostname = parsed.Self.HostName
	st.DNSName = strings.TrimSuffix(parsed.Self.DNSName, ".")
	st.TailnetName = parsed.CurrentTailnet.Name
	st.Tags = parsed.Self.Tags
	st.DeviceID = parsed.Self.ID
	for _, ip := range parsed.Self.TailscaleIPs {
		if strings.HasPrefix(ip, "100.") {
			st.TailnetIP = ip
			break
		}
	}
	return st, nil
}

// WorkerControlPlaneHint returns the URL workers should use to reach a daemon
// listening on port listenPort of this host, based on the local tailnet name.
// It returns "" when the host is not on a tailnet.
func (s *LocalStatus) WorkerControlPlaneHint(listenPort string) string {
	if !s.Running || s.DNSName == "" {
		return ""
	}
	return fmt.Sprintf("http://%s:%s", s.DNSName, listenPort)
}

func isNotFound(err error) bool {
	if _, ok := err.(*exec.ExitError); ok {
		return false
	}
	return strings.Contains(err.Error(), "executable file not found") ||
		strings.Contains(err.Error(), "no such file or directory")
}
