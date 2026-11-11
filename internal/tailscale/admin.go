// Package tailscale implements the Tailscale ("tailnet") integration for Compute.
//
// It provides:
//
//   - an admin API v2 client for minting tagged/ephemeral auth keys, inspecting
//     and cleaning up Compute-owned devices, and reading Tailscale Services;
//   - local detection of the host's Tailscale client (via the tailscale CLI);
//   - sandbox provisioning over the existing SSH control channel, which joins
//     each sandbox to the tailnet as an ephemeral, tagged device;
//   - generation of a scoped policy-file snippet the operator can review and
//     apply manually.
//
// Everything Compute creates is tagged (tag:compute:*) and named with the
// "compute-" prefix. This package never modifies the operator's policy file,
// routes, DNS settings, or any device that is not tagged as Compute-owned.
package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultAPIBaseURL is the Tailscale admin API base URL.
const DefaultAPIBaseURL = "https://api.tailscale.com"

// Canonical tags used by Compute. All Compute-owned devices carry exactly one
// of these (plus the operator may add more), and cleanup logic filters on them.
const (
	TagMaster  = "tag:compute:master"
	TagNode    = "tag:compute:node"
	TagSandbox = "tag:compute:sandbox"
)

// HostnamePrefix is prepended to every hostname Compute assigns on the tailnet.
const HostnamePrefix = "compute"

// Client talks to the Tailscale admin API v2.
type Client struct {
	BaseURL    string
	APIKey     string
	Tailnet    string // "-" selects the API key's default tailnet
	HTTPClient *http.Client
}

// NewClient builds a Tailscale admin API client.
func NewClient(apiKey, tailnet string) *Client {
	if tailnet == "" {
		tailnet = "-"
	}
	return &Client{
		BaseURL:    DefaultAPIBaseURL,
		APIKey:     apiKey,
		Tailnet:    tailnet,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Device is a machine in the tailnet.
type Device struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"` // full MagicDNS name, e.g. compute-master.tailnet.ts.net
	Hostname    string    `json:"hostname"`
	OS          string    `json:"os"`
	User        string    `json:"user"`
	Tags        []string  `json:"tags"`
	Addresses   []string  `json:"addresses"` // tailnet IPs (100.x, plus ULA)
	Created     time.Time `json:"created"`
	Expires     time.Time `json:"expires"`
	LastSeen    time.Time `json:"lastSeen"`
	IsEphemeral bool      `json:"isEphemeral"`
}

// TailscaleIP returns the IPv4 tailnet address (100.x.y.z), or "".
func (d *Device) TailscaleIP() string {
	for _, a := range d.Addresses {
		if strings.HasPrefix(a, "100.") {
			return a
		}
	}
	return ""
}

// TailnetDomain extracts the MagicDNS domain from a device's full name,
// e.g. "compute-master.tail1234.ts.net" -> "tail1234.ts.net".
func (d *Device) TailnetDomain() string {
	parts := strings.SplitN(strings.TrimSuffix(d.Name, "."), ".", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return ""
}

// keyRequest is the body of POST /tailnet/{tailnet}/keys.
type keyRequest struct {
	Description   string   `json:"description,omitempty"`
	Reusable      bool     `json:"reusable"`
	Ephemeral     bool     `json:"ephemeral"`
	Preauthorized bool     `json:"preauthorized"`
	ExpirySeconds int      `json:"expirySeconds,omitempty"`
	Tags          []string `json:"tags,omitempty"`
}

// AuthKey describes a key created via the admin API.
type AuthKey struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Created   time.Time `json:"created"`
	Expires   time.Time `json:"expires"`
	Ephemeral bool      `json:"ephemeral"`
	Reusable  bool      `json:"reusable"`
	Tags      []string  `json:"tags,omitempty"`
}

// KeyOptions controls the shape of a minted auth key.
type KeyOptions struct {
	Description   string
	Reusable      bool
	Ephemeral     bool
	Preauthorized bool
	ExpirySeconds int
	Tags          []string
}

// CreateAuthKey mints a new auth key.
func (c *Client) CreateAuthKey(ctx context.Context, opts KeyOptions) (*AuthKey, error) {
	body, err := json.Marshal(keyRequest{
		Description:   opts.Description,
		Reusable:      opts.Reusable,
		Ephemeral:     opts.Ephemeral,
		Preauthorized: opts.Preauthorized,
		ExpirySeconds: opts.ExpirySeconds,
		Tags:          opts.Tags,
	})
	if err != nil {
		return nil, err
	}
	var out AuthKey
	if err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/v2/tailnet/%s/keys", url.PathEscape(c.Tailnet)),
		bytes.NewReader(body), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListDevices returns all devices in the tailnet.
func (c *Client) ListDevices(ctx context.Context) ([]Device, error) {
	var out struct {
		Devices []Device `json:"devices"`
	}
	if err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v2/tailnet/%s/devices", url.PathEscape(c.Tailnet)),
		nil, &out); err != nil {
		return nil, err
	}
	return out.Devices, nil
}

// GetDevice fetches a single device by ID.
func (c *Client) GetDevice(ctx context.Context, deviceID string) (*Device, error) {
	var out Device
	if err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v2/device/%s", url.PathEscape(deviceID)), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteDevice removes a device from the tailnet. Callers must ensure the
// device is Compute-owned (tagged) before invoking this.
func (c *Client) DeleteDevice(ctx context.Context, deviceID string) error {
	return c.do(ctx, http.MethodDelete,
		fmt.Sprintf("/api/v2/device/%s", url.PathEscape(deviceID)), nil, nil)
}

// SetDeviceTags replaces a device's tags.
func (c *Client) SetDeviceTags(ctx context.Context, deviceID string, tags []string) error {
	body, err := json.Marshal(map[string][]string{"tags": tags})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/v2/device/%s/tags", url.PathEscape(deviceID)),
		bytes.NewReader(body), nil)
}

// Service is a Tailscale Service published on the tailnet.
type Service struct {
	Name        string   `json:"name"` // without the "svc:" prefix
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// ListServices returns the Tailscale Services defined for the tailnet.
func (c *Client) ListServices(ctx context.Context) ([]Service, error) {
	var out struct {
		Services []Service `json:"services"`
	}
	if err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v2/tailnet/%s/services", url.PathEscape(c.Tailnet)),
		nil, &out); err != nil {
		return nil, err
	}
	return out.Services, nil
}

// do performs an authenticated API request and decodes the JSON response.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out interface{}) error {
	if c.APIKey == "" {
		return fmt.Errorf("tailscale: missing API key")
	}
	u := strings.TrimSuffix(c.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("tailscale api %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return fmt.Errorf("tailscale api %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("tailscale api %s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

// FilterComputeDevices returns only devices owned by Compute: tagged with one
// of the given tags AND named with the compute- prefix.
func FilterComputeDevices(devices []Device, tags ...string) []Device {
	tagSet := make(map[string]bool, len(tags))
	for _, t := range tags {
		tagSet[t] = true
	}
	var out []Device
	for _, d := range devices {
		if !strings.HasPrefix(d.Hostname, HostnamePrefix+"-") && !strings.HasPrefix(d.Name, HostnamePrefix+"-") {
			continue
		}
		for _, t := range d.Tags {
			if tagSet[t] {
				out = append(out, d)
				break
			}
		}
	}
	return out
}

// SandboxDeviceID returns a deterministic MagicDNS-safe service name for a
// sandbox port exposure, e.g. "compute-vm-a1b2c3d4-8080".
func SandboxServiceName(vmID string, port int) string {
	return fmt.Sprintf("%s-%d", SandboxHostname(vmID), port)
}

// SandboxHostname returns the deterministic hostname for a sandbox device,
// e.g. "compute-vm-a1b2c3d4". MagicDNS labels allow up to 63 characters; we
// cap at 32 to stay well inside every tool's comfort zone.
func SandboxHostname(vmID string) string {
	id := sanitizeHostname(vmID)
	if len(id) > 32 {
		id = strings.Trim(id[:32], "-")
	}
	return HostnamePrefix + "-" + id
}

// sanitizeHostname keeps only lowercase alphanumeric characters and dashes.
func sanitizeHostname(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.' || r == ' ':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "sandbox"
	}
	return out
}
