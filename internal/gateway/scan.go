package gateway

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/tailscale"
)

// Scanner discovers TCP ports listening inside a sandbox.
type Scanner interface {
	ListenPorts(ctx context.Context, guestIP, sshKeyPath string) ([]int, error)
}

// SSHScanner scans a guest over the daemon's existing SSH control channel.
type SSHScanner struct {
	User    string
	Timeout time.Duration
	// ExposeLoopback also publishes listeners bound to 127.0.0.1/::1.
	ExposeLoopback bool
}

// NewSSHScanner builds a scanner using the given guest account.
func NewSSHScanner(user string) *SSHScanner {
	if strings.TrimSpace(user) == "" {
		user = "compute"
	}
	return &SSHScanner{User: user, Timeout: 20 * time.Second}
}

// ListenPorts runs `ss` (falling back to `netstat`) inside the guest and returns
// wildcard or guest-bound TCP ports. Loopback-only listeners are excluded:
// they are intentionally local to the sandbox.
func (s *SSHScanner) ListenPorts(ctx context.Context, guestIP, sshKeyPath string) ([]int, error) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	runner := &tailscale.SSHRunner{KeyPath: sshKeyPath, User: s.User, Timeout: timeout}
	out, stderr, code, err := runner.Run(ctx, guestIP, "ss -ltnH 2>/dev/null || netstat -ltn 2>/dev/null")
	if err != nil || code != 0 {
		return nil, fmt.Errorf("scan listening ports on %s (exit %d): %s: %w", guestIP, code, strings.TrimSpace(stderr), err)
	}
	ports := ParseListeningPortsWith(out, guestIP, s.ExposeLoopback)
	if len(ports) == 0 {
		return nil, fmt.Errorf("no TCP listeners found on %s", guestIP)
	}
	return ports, nil
}

// ParseListeningPorts extracts exposed TCP ports from `ss -ltnH` or
// `netstat -ltn` output, excluding loopback-only listeners.
func ParseListeningPorts(raw, guestIP string) []int {
	return ParseListeningPortsWith(raw, guestIP, false)
}

// ParseListeningPortsWith is ParseListeningPorts with an explicit loopback
// policy. A listener is exposed when it is bound to a wildcard address, the
// guest's own address, or 0.0.0.0/[::]; loopback-only listeners are included
// only when exposeLoopback is true.
func ParseListeningPortsWith(raw, guestIP string, exposeLoopback bool) []int {
	set := make(map[int]bool)
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// ss -ltnH:  LISTEN 0 4096 0.0.0.0:22 0.0.0.0:*
		// netstat:   tcp 0 0 0.0.0.0:22 0.0.0.0:* LISTEN
		for _, field := range fields {
			host, port, ok := splitHostPort(field)
			if !ok || !isExposableHost(host, guestIP, exposeLoopback) {
				continue
			}
			set[port] = true
			break
		}
	}
	out := make([]int, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

func splitHostPort(field string) (string, int, bool) {
	idx := strings.LastIndex(field, ":")
	if idx < 0 || idx == len(field)-1 {
		return "", 0, false
	}
	host := strings.Trim(field[:idx], "[]")
	port, err := strconv.Atoi(field[idx+1:])
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, false
	}
	return host, port, true
}

func isExposableHost(host, guestIP string, exposeLoopback bool) bool {
	host = strings.Trim(host, "[]")
	switch host {
	case "", "*", "0.0.0.0", "::", "[::]":
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return exposeLoopback
	}
	if guestIP != "" && ip.Equal(net.ParseIP(guestIP)) {
		return true
	}
	return ip.IsUnspecified()
}
