package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/Rashid2G/compute/internal/db"
)

const webhookTimeout = 10 * time.Second

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func isDisallowedIP(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return true
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	// CGNAT 100.64.0.0/10
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	if cgnat.Contains(addr) {
		return true
	}
	// 0.0.0.0/8
	zeroNet := netip.MustParsePrefix("0.0.0.0/8")
	if zeroNet.Contains(addr) {
		return true
	}
	return false
}

// validateWebhookURL verifies the scheme and destination host.
func (s *Server) validateWebhookURL(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid webhook url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported webhook url scheme %q: only http and https are allowed", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("webhook url missing host")
	}
	if s == nil || !s.allowPrivateWebhooks {
		if nip, err := netip.ParseAddr(host); err == nil {
			if isDisallowedIP(nip) {
				return fmt.Errorf("forbidden webhook destination IP %s", host)
			}
		}
	}
	return nil
}

// validateWebhookURL package-level function for standalone validation (always strict).
func validateWebhookURL(rawURL string) error {
	return (*Server)(nil).validateWebhookURL(rawURL)
}

// newWebhookHTTPClient creates a standalone HTTP client with dial-time SSRF protection (always strict).
func newWebhookHTTPClient() *http.Client {
	return (*Server)(nil).newWebhookHTTPClient()
}

// newWebhookHTTPClient creates an HTTP client with dial-time SSRF protection and redirect safety.
func (s *Server) newWebhookHTTPClient() *http.Client {
	dialer := &net.Dialer{
		Timeout:   webhookTimeout,
		KeepAlive: 30 * time.Second,
	}

	allowPrivate := s != nil && s.allowPrivateWebhooks

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("invalid address %q: %w", addr, err)
			}

			// If host is already an IP, check directly.
			if nip, err := netip.ParseAddr(host); err == nil {
				if !allowPrivate && isDisallowedIP(nip) {
					return nil, fmt.Errorf("webhook destination %s is a forbidden IP", host)
				}
				return dialer.DialContext(ctx, network, addr)
			}

			// Otherwise resolve DNS and check EVERY resolved IP.
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("dns lookup failed for %s: %w", host, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("no IP address found for %s", host)
			}

			var targetIP net.IP
			for _, ip := range ips {
				nip, ok := netip.AddrFromSlice(ip)
				if !allowPrivate && (!ok || isDisallowedIP(nip)) {
					return nil, fmt.Errorf("webhook destination %s resolves to forbidden IP %s", host, ip.String())
				}
				if targetIP == nil {
					targetIP = ip
				}
			}

			// Connect directly to the verified target IP to prevent DNS rebinding.
			targetAddr := net.JoinHostPort(targetIP.String(), port)
			return dialer.DialContext(ctx, network, targetAddr)
		},
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   webhookTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			scheme := strings.ToLower(req.URL.Scheme)
			if scheme != "http" && scheme != "https" {
				return fmt.Errorf("forbidden redirect scheme %q", req.URL.Scheme)
			}
			host := req.URL.Hostname()
			if !allowPrivate {
				if nip, err := netip.ParseAddr(host); err == nil {
					if isDisallowedIP(nip) {
						return fmt.Errorf("redirect to forbidden IP: %s", host)
					}
				}
			}
			return nil
		},
	}
}

// deliverWebhooks fans an audit event out to every active webhook subscribed to
// that event. Delivery happens asynchronously so API latency is unaffected.
func (s *Server) deliverWebhooks(evt *db.AuditEvent) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil || evt == nil {
		return
	}
	hooks, err := s.vmMgr.GetStore().ListWebhooks(context.Background())
	if err != nil {
		return
	}
	for _, hook := range hooks {
		if hook == nil || !hook.Active || !webhookSubscribed(hook.Events, evt.Event) {
			continue
		}
		go func(hook *db.WebhookEndpoint) {
			ctx, cancel := context.WithTimeout(context.Background(), webhookTimeout)
			defer cancel()
			_, _ = s.deliverWebhook(ctx, hook, evt.Event, evt)
		}(hook)
	}
}

func webhookSubscribed(events []string, event string) bool {
	for _, candidate := range events {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == event {
			return true
		}
		// Allow prefix subscriptions such as "sandbox.*".
		if strings.HasSuffix(candidate, ".*") && strings.HasPrefix(event, strings.TrimSuffix(candidate, "*")) {
			return true
		}
	}
	return false
}

// recordWebhookResult safely updates delivery statistics on the stored webhook record.
func (s *Server) recordWebhookResult(hookID string, code int, delivered bool, deliveryErr error) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil || hookID == "" {
		return
	}
	s.webhookStatsMu.Lock()
	defer s.webhookStatsMu.Unlock()

	latest, err := s.vmMgr.GetStore().GetWebhook(context.Background(), hookID)
	if err != nil || latest == nil {
		return
	}

	latest.DeliveriesCount++
	latest.UpdatedAt = time.Now().UTC()
	if delivered {
		latest.LastDelivery = "just now"
		latest.SuccessRate = "100%"
	} else if deliveryErr != nil {
		latest.LastDelivery = "failed"
		latest.SuccessRate = "0%"
	} else {
		latest.LastDelivery = fmt.Sprintf("failed (%d)", code)
		latest.SuccessRate = "0%"
	}

	_ = s.vmMgr.GetStore().SaveWebhook(context.Background(), latest)
}

// deliverWebhook performs one signed HTTP delivery and updates delivery stats.
func (s *Server) deliverWebhook(ctx context.Context, hook *db.WebhookEndpoint, event string, payload interface{}) (int, error) {
	if hook == nil {
		return 0, errors.New("nil webhook")
	}
	hookID := hook.ID
	hookURL := hook.URL
	hookSecret := hook.Secret

	if err := s.validateWebhookURL(hookURL); err != nil {
		s.recordWebhookResult(hookID, 0, false, err)
		return 0, err
	}

	body, err := json.Marshal(map[string]interface{}{
		"id":          "evt_" + hex.EncodeToString(randomBytes(6)),
		"event":       event,
		"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
		"data":        payload,
		"api_version": "v1",
	})
	if err != nil {
		s.recordWebhookResult(hookID, 0, false, err)
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hookURL, bytes.NewReader(body))
	if err != nil {
		s.recordWebhookResult(hookID, 0, false, err)
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Compute-Event", event)
	if hookSecret != "" {
		mac := hmac.New(sha256.New, []byte(hookSecret))
		_, _ = mac.Write(body)
		req.Header.Set("X-Compute-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	client := s.newWebhookHTTPClient()
	resp, err := client.Do(req)
	code := 0
	delivered := false
	if err == nil {
		code = resp.StatusCode
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		delivered = code >= 200 && code < 300
	}

	s.recordWebhookResult(hookID, code, delivered, err)

	if err != nil {
		return code, err
	}
	if !delivered {
		return code, fmt.Errorf("endpoint returned HTTP %d", code)
	}
	return code, nil
}
