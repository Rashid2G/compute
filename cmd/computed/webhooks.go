package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		if !hook.Active || !webhookSubscribed(hook.Events, evt.Event) {
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

// deliverWebhook performs one signed HTTP delivery and updates delivery stats.
func (s *Server) deliverWebhook(ctx context.Context, hook *db.WebhookEndpoint, event string, payload interface{}) (int, error) {
	body, err := json.Marshal(map[string]interface{}{
		"id":          "evt_" + hex.EncodeToString(randomBytes(6)),
		"event":       event,
		"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
		"data":        payload,
		"api_version": "v1",
	})
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Compute-Event", event)
	if hook.Secret != "" {
		mac := hmac.New(sha256.New, []byte(hook.Secret))
		_, _ = mac.Write(body)
		req.Header.Set("X-Compute-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	client := &http.Client{Timeout: webhookTimeout}
	resp, err := client.Do(req)
	code := 0
	delivered := false
	if err != nil {
		hook.LastDelivery = "failed"
	} else {
		code = resp.StatusCode
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		delivered = code >= 200 && code < 300
		if delivered {
			hook.LastDelivery = "just now"
			hook.SuccessRate = "100%"
		} else {
			hook.LastDelivery = fmt.Sprintf("failed (%d)", code)
			hook.SuccessRate = "0%"
		}
	}
	if !delivered && hook.LastDelivery == "failed" {
		hook.SuccessRate = "0%"
	}
	hook.DeliveriesCount++
	hook.UpdatedAt = time.Now().UTC()
	if s.vmMgr != nil && s.vmMgr.GetStore() != nil {
		_ = s.vmMgr.GetStore().SaveWebhook(context.Background(), hook)
	}
	if err != nil {
		return code, err
	}
	if !delivered {
		return code, fmt.Errorf("endpoint returned HTTP %d", code)
	}
	return code, nil
}
