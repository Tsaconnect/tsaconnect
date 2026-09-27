package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	cleanupInterval = 5 * time.Minute
)

type rateLimitEntry struct {
	count     int
	expiresAt time.Time
}

// rateLimiterStore holds one counter per key. mu guards the map and every
// entry in it, so each request's read-reset-or-increment of its entry is one
// step: concurrent requests on one key would otherwise race on the count (and,
// on a first request, both could start the window at 1).
type rateLimiterStore struct {
	mu      sync.Mutex
	entries map[string]*rateLimitEntry
}

func newRateLimiterStore() *rateLimiterStore {
	store := &rateLimiterStore{entries: map[string]*rateLimitEntry{}}
	go store.cleanup()
	return store
}

// cleanup periodically removes expired entries from the rate limiter store.
func (s *rateLimiterStore) cleanup() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		s.mu.Lock()
		for key, entry := range s.entries {
			if now.After(entry.expiresAt) {
				delete(s.entries, key)
			}
		}
		s.mu.Unlock()
	}
}

// allow counts one request for key and reports whether it is within
// maxRequests for the current window, starting a new window when there is none
// or the last has ended.
func (s *rateLimiterStore) allow(key string, maxRequests int, window time.Duration, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.entries[key]; ok && !now.After(entry.expiresAt) {
		entry.count++
		return entry.count <= maxRequests
	}
	s.entries[key] = &rateLimitEntry{count: 1, expiresAt: now.Add(window)}
	return true
}

// RateLimiter returns a Gin middleware that limits requests per IP address.
// It allows up to maxRequests requests per the specified window duration.
// Default recommendation: 100 requests per 15 minutes.
func RateLimiter(maxRequests int, window time.Duration) gin.HandlerFunc {
	return RateLimiterBy(maxRequests, window, ClientKey)
}

// ClientKey is the client's address as a rate-limit key: an IPv4 address as it
// is, an IPv6 one as its /64. One IPv6 host is usually handed a whole /64 and
// can pick any address in it, so keying on the full address would give it
// billions of fresh budgets. Every per-address limit keys on this.
func ClientKey(c *gin.Context) string {
	raw := c.ClientIP()
	ip := net.ParseIP(raw)
	if ip == nil || ip.To4() != nil {
		return raw
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// RateLimiterBy is RateLimiter with a caller-supplied bucket key. Use it to
// throttle per authenticated user (e.g. to protect a shared upstream quota that
// a single account could exhaust from one IP) rather than per IP. keyFn should
// return a stable, non-empty key; an empty key falls back to the client IP so a
// missing key never disables the limit.
func RateLimiterBy(maxRequests int, window time.Duration, keyFn func(*gin.Context) string) gin.HandlerFunc {
	store := newRateLimiterStore()

	return func(c *gin.Context) {
		key := keyFn(c)
		if key == "" {
			key = ClientKey(c)
		}
		if !store.allow(key, maxRequests, window, time.Now()) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"success": false,
				"message": "Too many requests, please try again later.",
			})
			return
		}

		c.Next()
	}
}
