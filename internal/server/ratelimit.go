package server

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rateLimit struct {
	limit  int
	window time.Duration
}
type rateEntry struct {
	count int
	reset time.Time
}
type rateLimiter struct {
	mu             sync.Mutex
	clients        map[string]map[string]rateEntry
	now            func() time.Time
	lastCleanup    time.Time
	trustedProxies []netip.Prefix
}

// rateLimiter is intentionally process-local. Deployments with multiple API
// replicas must enforce a shared limit at their gateway or in a shared store.
func newRateLimiter(trustedProxies []netip.Prefix) *rateLimiter {
	return &rateLimiter{clients: make(map[string]map[string]rateEntry), now: time.Now, trustedProxies: trustedProxies}
}

func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, rule := "", rateLimit{}
		switch r.URL.Path {
		case "/v1/targets":
			if r.Method == http.MethodPost {
				key, rule = "create-target", rateLimit{20, time.Minute}
			}
		case "/v1/solve":
			key, rule = "solve", rateLimit{10, time.Minute}
		case "/v1/recipes":
			key, rule = "recipes", rateLimit{4, time.Minute}
		}
		client := l.clientIP(r)
		allowed, retryAfter := l.allow(client, "global", rateLimit{120, time.Minute})
		if allowed && key != "" {
			allowed, retryAfter = l.allow(client, key, rule)
		}
		if !allowed {
			seconds := int64((retryAfter + time.Second - 1) / time.Second)
			w.Header().Set("Retry-After", strconv.FormatInt(max(seconds, 1), 10))
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *rateLimiter) allow(client, key string, rule rateLimit) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	byRoute := l.clients[client]
	if byRoute == nil {
		if len(l.clients) >= 100_000 {
			if now.Sub(l.lastCleanup) >= time.Minute {
				for identity, entries := range l.clients {
					active := false
					for _, entry := range entries {
						active = active || now.Before(entry.reset)
					}
					if !active {
						delete(l.clients, identity)
					}
				}
				l.lastCleanup = now
			}
			if len(l.clients) >= 100_000 {
				return false, time.Minute
			}
		}
		byRoute = make(map[string]rateEntry)
		l.clients[client] = byRoute
	}
	entry := byRoute[key]
	if entry.reset.IsZero() || !now.Before(entry.reset) {
		entry = rateEntry{reset: now.Add(rule.window)}
	}
	if entry.count >= rule.limit {
		return false, entry.reset.Sub(now)
	}
	entry.count++
	byRoute[key] = entry
	return true, 0
}

func (l *rateLimiter) clientIP(r *http.Request) string {
	peer := parseIP(r.RemoteAddr)
	if !peer.IsValid() {
		return r.RemoteAddr
	}
	trusted := false
	for _, prefix := range l.trustedProxies {
		trusted = trusted || prefix.Contains(peer)
	}
	// Forwarding headers are accepted only from an explicitly configured proxy.
	// Walking backwards selects the nearest untrusted hop, so clients cannot
	// evade a limit by prepending a forged X-Forwarded-For value.
	if trusted {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			candidate, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
			if err == nil && !l.isTrustedProxy(candidate) {
				return candidate.String()
			}
		}
	}
	return peer.String()
}

func (l *rateLimiter) isTrustedProxy(address netip.Addr) bool {
	for _, prefix := range l.trustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func parseIP(address string) netip.Addr {
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	address = strings.Trim(address, "[]")
	parsed, _ := netip.ParseAddr(address)
	return parsed
}
