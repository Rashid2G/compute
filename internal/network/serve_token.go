package network

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// ServeTokenQueryParam carries a one-time private-serve token in the URL.
	ServeTokenQueryParam = "__serve_token"
	// ServeCookieName stores the exchanged token on the serve origin.
	ServeCookieName = "compute_serve"
	// ServeTokenTTL bounds how long a private-serve token or cookie is valid.
	ServeTokenTTL = 12 * time.Hour
)

// ErrInvalidServeToken reports a malformed, expired, or forged serve token.
var ErrInvalidServeToken = errors.New("invalid serve token")

// SignServeToken returns an HMAC-signed token granting access to one exposure.
// The token is scoped to the exposure and its owner, so it cannot be replayed
// against a different sandbox.
func SignServeToken(key []byte, exposureID, ownerID string, ttl time.Duration) string {
	if ttl <= 0 {
		ttl = ServeTokenTTL
	}
	return signServeTokenExpiring(key, exposureID, ownerID, time.Now().Add(ttl))
}

func signServeTokenExpiring(key []byte, exposureID, ownerID string, exp time.Time) string {
	payload := strings.Join([]string{"v1", exposureID, ownerID, strconv.FormatInt(exp.UTC().Unix(), 10)}, "|")
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyServeToken validates a token and returns its exposure and owner scope.
func VerifyServeToken(key []byte, token string) (exposureID, ownerID string, err error) {
	if len(key) == 0 || token == "" {
		return "", "", ErrInvalidServeToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", "", ErrInvalidServeToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", ErrInvalidServeToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", ErrInvalidServeToken
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", "", ErrInvalidServeToken
	}
	fields := strings.Split(string(payload), "|")
	if len(fields) != 4 || fields[0] != "v1" {
		return "", "", ErrInvalidServeToken
	}
	exp, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil || time.Now().UTC().Unix() > exp {
		return "", "", ErrInvalidServeToken
	}
	return fields[1], fields[2], nil
}

// serveTokenFromRequest reads a token from the query string or the serve cookie.
func serveTokenFromRequest(r *http.Request) string {
	if v := strings.TrimSpace(r.URL.Query().Get(ServeTokenQueryParam)); v != "" {
		return v
	}
	if c, err := r.Cookie(ServeCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

// ServeTokenKey derives the signing key for private-serve tokens. An explicit
// key wins; otherwise the admin secret is expanded with HMAC; otherwise a
// random per-process key is used (tokens then expire on daemon restart).
func ServeTokenKey(explicit, adminSecret string) []byte {
	if key := strings.TrimSpace(explicit); key != "" {
		return []byte(key)
	}
	if secret := strings.TrimSpace(adminSecret); secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte("compute-serve-tokens"))
		return mac.Sum(nil)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return []byte("compute-serve-fallback-key")
	}
	return buf
}
