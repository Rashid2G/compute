package main

import (
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Rashid2G/compute/internal/config"
)

type tokenBucket struct {
	tokens     float64
	lastUpdate time.Time
}

func (b *tokenBucket) allow(rate float64, burst int, now time.Time) (bool, float64) {
	if b.lastUpdate.IsZero() {
		b.tokens = float64(burst)
		b.lastUpdate = now
	} else {
		elapsed := now.Sub(b.lastUpdate).Seconds()
		if elapsed > 0 {
			b.tokens = math.Min(float64(burst), b.tokens+elapsed*rate)
			b.lastUpdate = now
		}
	}

	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true, 0
	}

	missing := 1.0 - b.tokens
	retryAfter := math.Ceil(missing / rate)
	if retryAfter < 1 {
		retryAfter = 1
	}
	return false, retryAfter
}

// RateLimiter manages per-IP token buckets for auth and general API routes.
type RateLimiter struct {
	enabled      bool
	trustedCIDRs []*net.IPNet

	authRate  float64
	authBurst int
	apiRate   float64
	apiBurst  int

	mu          sync.Mutex
	authBuckets map[string]*tokenBucket
	apiBuckets  map[string]*tokenBucket

	stopCh chan struct{}
}

// NewRateLimiter creates a new RateLimiter configured according to cfg.
func NewRateLimiter(cfg config.ServerConfig) *RateLimiter {
	enabled := cfg.IsRateLimitEnabled()

	var trusted []*net.IPNet
	if raw := strings.TrimSpace(cfg.TrustedProxies); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if !strings.Contains(part, "/") {
				if ip := net.ParseIP(part); ip != nil {
					if ip.To4() != nil {
						part = part + "/32"
					} else {
						part = part + "/128"
					}
				}
			}
			if _, ipNet, err := net.ParseCIDR(part); err == nil && ipNet != nil {
				trusted = append(trusted, ipNet)
			}
		}
	}

	authRate := cfg.RateLimitAuthRate
	if authRate <= 0 {
		authRate = 20.0 / 60.0 // 20 req/min
	}
	authBurst := cfg.RateLimitAuthBurst
	if authBurst <= 0 {
		authBurst = 10
	}

	apiRate := cfg.RateLimitAPIRate
	if apiRate <= 0 {
		apiRate = 600.0 / 60.0 // 600 req/min
	}
	apiBurst := cfg.RateLimitAPIBurst
	if apiBurst <= 0 {
		apiBurst = 120
	}

	limiter := &RateLimiter{
		enabled:      enabled,
		trustedCIDRs: trusted,
		authRate:     authRate,
		authBurst:    authBurst,
		apiRate:      apiRate,
		apiBurst:     apiBurst,
		authBuckets:  make(map[string]*tokenBucket),
		apiBuckets:   make(map[string]*tokenBucket),
		stopCh:       make(chan struct{}),
	}

	if enabled {
		go limiter.cleanupLoop()
	}

	return limiter
}

// Close stops the background eviction worker.
func (l *RateLimiter) Close() {
	if l == nil || l.stopCh == nil {
		return
	}
	select {
	case <-l.stopCh:
	default:
		close(l.stopCh)
	}
}

func (l *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case now := <-ticker.C:
			l.evictStale(now)
		}
	}
}

func (l *RateLimiter) evictStale(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	const maxIdle = 10 * time.Minute
	for ip, b := range l.authBuckets {
		if now.Sub(b.lastUpdate) > maxIdle {
			delete(l.authBuckets, ip)
		}
	}
	for ip, b := range l.apiBuckets {
		if now.Sub(b.lastUpdate) > maxIdle {
			delete(l.apiBuckets, ip)
		}
	}

	// Memory cap under active IP spray attack
	const maxEntries = 50000
	if len(l.authBuckets) > maxEntries {
		l.authBuckets = make(map[string]*tokenBucket)
	}
	if len(l.apiBuckets) > maxEntries {
		l.apiBuckets = make(map[string]*tokenBucket)
	}
}

// IsTrustedProxy checks whether remoteAddr belongs to a configured trusted proxy CIDR.
func (l *RateLimiter) IsTrustedProxy(remoteAddr string) bool {
	if l == nil || len(l.trustedCIDRs) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false
	}
	for _, cidr := range l.trustedCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// ExtractClientIP resolves the caller IP address, respecting X-Forwarded-For and
// X-Real-IP ONLY when the immediate TCP remote address is a trusted proxy.
func (l *RateLimiter) ExtractClientIP(r *http.Request) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = strings.TrimSpace(r.RemoteAddr)
	}

	if l != nil && l.IsTrustedProxy(r.RemoteAddr) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if len(parts) > 0 {
				candidate := strings.TrimSpace(parts[0])
				if ip := net.ParseIP(candidate); ip != nil {
					return candidate
				}
			}
		}
		if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
			if ip := net.ParseIP(xri); ip != nil {
				return xri
			}
		}
	}

	return remoteHost
}

// isExcludedRoute checks routes that must never be rate limited.
func isExcludedRoute(path string) bool {
	path = strings.ToLower(path)
	if path == "/api/v1/health" || path == "/api/v1/ready" || path == "/api/v1/metrics" {
		return true
	}
	if path == "/api/v1/worker/connect" {
		return true
	}
	if strings.HasPrefix(path, "/api/v1/workers/") && strings.HasSuffix(path, "/heartbeat") {
		return true
	}
	return false
}

func isAuthRoute(path string) bool {
	p := strings.ToLower(path)
	return p == "/api/v1/auth/login" || p == "/api/v1/auth/signup" || p == "/api/v1/auth/setup"
}

// Allow reports whether a request is permitted and, if throttled, the retry duration in seconds.
func (l *RateLimiter) Allow(r *http.Request) (allowed bool, retryAfter float64) {
	if l == nil || !l.enabled {
		return true, 0
	}

	path := r.URL.Path
	if !strings.HasPrefix(path, "/api/v1/") || isExcludedRoute(path) {
		return true, 0
	}

	clientIP := l.ExtractClientIP(r)
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if isAuthRoute(path) {
		b, ok := l.authBuckets[clientIP]
		if !ok {
			b = &tokenBucket{}
			l.authBuckets[clientIP] = b
		}
		return b.allow(l.authRate, l.authBurst, now)
	}

	b, ok := l.apiBuckets[clientIP]
	if !ok {
		b = &tokenBucket{}
		l.apiBuckets[clientIP] = b
	}
	return b.allow(l.apiRate, l.apiBurst, now)
}
