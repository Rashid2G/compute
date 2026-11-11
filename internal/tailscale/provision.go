package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

// Runner executes a command inside a guest sandbox over the SSH control
// channel. vm.Manager wires this to the standard ssh binary using the VM's
// embedded key.
type Runner interface {
	// Run executes cmd on host as the configured user and returns stdout/stderr.
	Run(ctx context.Context, host, cmd string) (stdout string, stderr string, exitCode int, err error)
}

// ProvisionResult reports what the provisioning step accomplished.
type ProvisionResult struct {
	// TailnetIP is the sandbox's 100.x.y.z tailnet address.
	TailnetIP string
	// DNSName is the sandbox's MagicDNS name (compute-<id>.<tailnet>.ts.net).
	DNSName string
	// DeviceID is the device ID assigned by the Tailscale coordination server.
	DeviceID string
	// Ephemeral is always true for sandbox keys today.
	Ephemeral bool
	// Installed is true when the tailscale binary was installed in the guest.
	Installed bool
}

// Provisioner joins sandboxes to the tailnet as ephemeral, tagged devices.
type Provisioner struct {
	Client *Client // admin API, used to mint sandbox auth keys

	SandboxTag    string // default TagSandbox
	KeyExpirySecs int    // default 90 days
	AutoInstall   bool   // install the tailscale binary in the guest when missing
	SSHUser       string // default "root"
	BootTimeout   time.Duration
	InstallQuiet  bool
	Logger        *log.Logger
}

// keyStatus mirrors the Self object of `tailscale status --json` inside the guest.
type keyStatus struct {
	Self struct {
		ID           string   `json:"ID"`
		HostName     string   `json:"HostName"`
		DNSName      string   `json:"DNSName"`
		TailscaleIPs []string `json:"TailscaleIPs"`
	} `json:"Self"`
}

const defaultProvisionTimeout = 3 * time.Minute

// Provision joins the sandbox reachable at vmIP over SSH (key at sshKeyPath)
// to the tailnet. It is safe to call from a goroutine after VM start; the VM
// runs normally even when provisioning fails (the error is reported to the
// caller for status/logging only).
func (p *Provisioner) Provision(ctx context.Context, vmID, vmIP, sshKeyPath string) (*ProvisionResult, error) {
	if p.Client == nil {
		return nil, fmt.Errorf("tailscale: provisioner has no admin API client")
	}
	if vmIP == "" || vmIP == "dhcp" {
		return nil, fmt.Errorf("tailscale: sandbox %s has no routable IP yet", vmID)
	}
	runner := p.runner(sshKeyPath)
	timeout := p.BootTimeout
	if timeout <= 0 {
		timeout = defaultProvisionTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sandboxTag := p.SandboxTag
	if sandboxTag == "" {
		sandboxTag = TagSandbox
	}

	// 1. Mint a scoped, ephemeral, preauthorized key. The key is delivered only
	// over the existing SSH channel and grants membership for this one sandbox.
	key, err := p.Client.CreateAuthKey(ctx, KeyOptions{
		Description:   "compute sandbox " + SandboxHostname(vmID),
		Reusable:      false,
		Ephemeral:     true,
		Preauthorized: true,
		ExpirySeconds: p.keyExpiry(),
		Tags:          []string{sandboxTag},
	})
	if err != nil {
		return nil, fmt.Errorf("tailscale: mint sandbox auth key: %w", err)
	}

	// 2. Wait for the guest's SSH daemon to accept connections.
	if err := waitSSHReady(ctx, runner, vmIP); err != nil {
		return nil, err
	}

	// 3. Ensure the tailscale binary exists (optionally installing it).
	installed := false
	if _, _, code, err := runner.Run(ctx, vmIP, "command -v tailscale"); err != nil || code != 0 {
		if !p.AutoInstall {
			return nil, fmt.Errorf("tailscale binary not found in sandbox %s; rebuild the image with TAILSCALE=1 or enable tailscale.auto_install_guest", vmID)
		}
		if _, _, code, err := runner.Run(ctx, vmIP, "curl -fsSL https://tailscale.com/install.sh | sh"); err != nil || code != 0 {
			return nil, fmt.Errorf("tailscale: install in sandbox %s failed (exit %d)", vmID, code)
		}
		installed = true
	}

	// 4. Join the tailnet as an ephemeral tagged device.
	hostname := SandboxHostname(vmID)
	upCmd := fmt.Sprintf(
		"tailscale up --authkey=%s --hostname=%s --advertise-tags=%s --accept-dns=false",
		shellQuote(key.Key), shellQuote(hostname), shellQuote(sandboxTag),
	)
	if out, stderr, code, err := runner.Run(ctx, vmIP, upCmd); err != nil || code != 0 {
		return nil, fmt.Errorf("tailscale: sandbox %s join failed (exit %d): %s", vmID, code, strings.TrimSpace(stderr+" "+out))
	}

	// 5. Read back the assigned tailnet identity.
	out, _, code, err := runner.Run(ctx, vmIP, "tailscale ip -4 -1")
	if err != nil || code != 0 {
		return nil, fmt.Errorf("tailscale: sandbox %s ip read failed (exit %d)", vmID, code)
	}
	ip := strings.TrimSpace(out)
	if !strings.HasPrefix(ip, "100.") {
		return nil, fmt.Errorf("tailscale: sandbox %s reported unexpected address %q", vmID, ip)
	}

	statusOut, _, _, err := runner.Run(ctx, vmIP, "tailscale status --json")
	dnsName := hostname + "." + p.clientTailnetDomain()
	deviceID := ""
	if err == nil && statusOut != "" {
		var parsed keyStatus
		if json.Unmarshal([]byte(statusOut), &parsed) == nil {
			deviceID = parsed.Self.ID
			if parsed.Self.DNSName != "" {
				dnsName = strings.TrimSuffix(parsed.Self.DNSName, ".")
			}
		}
	}

	return &ProvisionResult{
		TailnetIP: ip,
		DNSName:   dnsName,
		DeviceID:  deviceID,
		Ephemeral: true,
		Installed: installed,
	}, nil
}

// Teardown logs the sandbox out of the tailnet. With ephemeral keys the node
// is removed by Tailscale shortly after this, keeping the device list clean.
// Best-effort: errors are ignored.
func (p *Provisioner) Teardown(ctx context.Context, vmIP, sshKeyPath string) {
	if vmIP == "" || vmIP == "dhcp" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, _, _, _ = p.runner(sshKeyPath).Run(ctx, vmIP, "tailscale logout >/dev/null 2>&1 || true")
}

func (p *Provisioner) keyExpiry() int {
	if p.KeyExpirySecs > 0 {
		return p.KeyExpirySecs
	}
	return 90 * 24 * 3600
}

func (p *Provisioner) clientTailnetDomain() string {
	if p.Client == nil {
		return ""
	}
	devices, err := p.Client.ListDevices(context.Background())
	if err != nil || len(devices) == 0 {
		return ""
	}
	return devices[0].TailnetDomain()
}

func (p *Provisioner) runner(keyPath string) Runner {
	return &SSHRunner{KeyPath: keyPath, User: p.sshUser(), Timeout: 60 * time.Second}
}

func (p *Provisioner) sshUser() string {
	if p.SSHUser != "" {
		return p.SSHUser
	}
	return "root"
}

func waitSSHReady(ctx context.Context, runner Runner, host string) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultProvisionTimeout)
	}
	for {
		if _, _, code, err := runner.Run(ctx, host, "true"); err == nil && code == 0 {
			return nil
		}
		if time.Now().After(deadline.Add(-2 * time.Second)) {
			return fmt.Errorf("tailscale: sandbox %s SSH not ready before timeout", host)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("tailscale: waiting for sandbox %s SSH: %w", host, ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// SSHRunner implements Runner by shelling out to the ssh binary on the daemon
// host using the VM's embedded key. It reuses the trust relationship that
// already exists between the control plane and its sandboxes.
type SSHRunner struct {
	KeyPath string
	User    string
	Port    int
	Timeout time.Duration
}

func (r *SSHRunner) Run(ctx context.Context, host, cmd string) (string, string, int, error) {
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	user := r.User
	if user == "" {
		user = "root"
	}
	addr := user + "@" + host
	if r.Port > 0 && r.Port != 22 {
		addr = fmt.Sprintf("[%s]:%d", host, r.Port)
	}
	args := []string{
		"-i", r.KeyPath,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=5",
	}
	var out, errOut bytes.Buffer
	c := exec.CommandContext(ctx, "ssh", append(args, addr, "--", cmd)...)
	c.Stdout = &out
	c.Stderr = &errOut
	err := c.Run()
	exit := 0
	if err != nil {
		exit = 1
		if ee, ok := err.(interface{ ExitCode() int }); ok {
			exit = ee.ExitCode()
		}
	}
	return out.String(), errOut.String(), exit, err
}
