package tailscale

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ServeRunner configures Tailscale Service endpoints on the local host through
// the `tailscale serve` CLI. The daemon runs in a container in the compose
// deployment, so it talks to the host's tailscaled over the mounted control
// socket; tailscaled (not the daemon) accepts Service traffic and dials the
// target address.
type ServeRunner struct {
	Binary  string
	Socket  string
	Timeout time.Duration
}

// NewServeRunner builds a runner with defaults.
func NewServeRunner(binary, socket string) *ServeRunner {
	if strings.TrimSpace(binary) == "" {
		binary = "tailscale"
	}
	return &ServeRunner{
		Binary:  binary,
		Socket:  strings.TrimSpace(socket),
		Timeout: 30 * time.Second,
	}
}

// AdvertiseTCP maps a Service port to a raw TCP target. The target must be
// reachable from the host's tailscaled process (for the compose deployment
// that is the daemon container's IP, not 127.0.0.1).
func (r *ServeRunner) AdvertiseTCP(ctx context.Context, service string, port int, target string) error {
	if service == "" || port <= 0 || port > 65535 {
		return fmt.Errorf("tailscale: invalid service %q or port %d", service, port)
	}
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("tailscale: empty serve target")
	}
	args := []string{
		"serve",
		"--service=" + normalizeServiceName(service),
		fmt.Sprintf("--tcp=%d", port),
		"--yes",
		"tcp://" + target,
	}
	_, stderr, code, err := r.run(ctx, args...)
	if err != nil || code != 0 {
		return fmt.Errorf("tailscale serve --tcp=%d for %s failed (exit %d): %s: %w",
			port, service, code, strings.TrimSpace(stderr), err)
	}
	return nil
}

// RemoveTCP removes one advertised TCP endpoint from a Service.
func (r *ServeRunner) RemoveTCP(ctx context.Context, service string, port int) error {
	args := []string{
		"serve",
		"--service=" + normalizeServiceName(service),
		fmt.Sprintf("--tcp=%d", port),
		"--yes",
		"off",
	}
	_, stderr, code, err := r.run(ctx, args...)
	if err != nil || code != 0 {
		// Removing an endpoint that was never advertised is not an error for
		// reconciliation.
		msg := strings.ToLower(stderr)
		if strings.Contains(msg, "does not exist") || strings.Contains(msg, "not found") ||
			strings.Contains(msg, "no config") {
			return nil
		}
		return fmt.Errorf("tailscale serve remove tcp:%d for %s failed (exit %d): %s: %w",
			port, service, code, strings.TrimSpace(stderr), err)
	}
	return nil
}

// ClearService removes every endpoint configured for a Service.
func (r *ServeRunner) ClearService(ctx context.Context, service string) error {
	_, stderr, code, err := r.run(ctx, "serve", "clear", normalizeServiceName(service))
	if err != nil || code != 0 {
		// Clearing an unknown service is not an error for reconciliation.
		if strings.Contains(strings.ToLower(stderr), "not found") ||
			strings.Contains(strings.ToLower(stderr), "no config") {
			return nil
		}
		return fmt.Errorf("tailscale serve clear %s failed (exit %d): %s: %w",
			service, code, strings.TrimSpace(stderr), err)
	}
	return nil
}

// LocalStatus inspects the host's tailscaled through the configured socket.
// Unlike DetectLocal it honours a non-default binary or socket path, which the
// container deployment needs.
func (r *ServeRunner) LocalStatus(ctx context.Context) (*LocalStatus, error) {
	out, stderr, code, err := r.run(ctx, "status", "--json")
	if err != nil || code != 0 {
		return nil, fmt.Errorf("tailscale status failed (exit %d): %s: %w", code, strings.TrimSpace(stderr), err)
	}
	var parsed statusOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("tailscale: parse status output: %w", err)
	}
	st := &LocalStatus{
		Installed:   true,
		Hostname:    parsed.Self.HostName,
		DNSName:     strings.TrimSuffix(parsed.Self.DNSName, "."),
		TailnetName: parsed.CurrentTailnet.Name,
		Tags:        parsed.Self.Tags,
		DeviceID:    parsed.Self.ID,
	}
	st.Running = parsed.BackendState == "Running" || len(parsed.Self.TailscaleIPs) > 0
	for _, ip := range parsed.Self.TailscaleIPs {
		if strings.HasPrefix(ip, "100.") {
			st.TailnetIP = ip
			break
		}
	}
	return st, nil
}

// LocalDeviceID returns this host's tailnet device ID (needed to approve the
// host as a Service endpoint).
func (r *ServeRunner) LocalDeviceID(ctx context.Context) (string, error) {
	args := []string{"status", "--json"}
	out, stderr, code, err := r.run(ctx, args...)
	if err != nil || code != 0 {
		return "", fmt.Errorf("tailscale status failed (exit %d): %s: %w", code, strings.TrimSpace(stderr), err)
	}
	id := parseSelfID(out)
	if id == "" {
		return "", fmt.Errorf("tailscale status returned no Self.ID")
	}
	return id, nil
}

func (r *ServeRunner) run(ctx context.Context, args ...string) (string, string, int, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	all := args
	if r.Socket != "" {
		all = append([]string{"--socket=" + r.Socket}, args...)
	}
	cmd := exec.CommandContext(ctx, r.Binary, all...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	return stdout.String(), stderr.String(), code, err
}

func normalizeServiceName(name string) string {
	if strings.HasPrefix(name, "svc:") {
		return name
	}
	return "svc:" + name
}

// parseSelfID extracts Self.ID from `tailscale status --json`.
func parseSelfID(raw string) string {
	var out struct {
		Self struct {
			ID string `json:"ID"`
		} `json:"Self"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return ""
	}
	return out.Self.ID
}
