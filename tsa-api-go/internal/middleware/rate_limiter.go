package middleware

import (
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

// rateLimiterStore holds one counter per key. mu makes each request's
// read-reset-or-increment of its entry one step, and covers the cleanup's reads
// of the same entries: concurrent requests on one key would otherwise race on
// the count (and on a first request, both could start the window at 1).
type rateLimiterStore struct {
	mu      sync.Mutex
	entries sync.Map
}

func newRateLimiterStore() *rateLimiterStore {
	store := &rateLimiterStore{}
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
		s.entries.Range(func(key, value interface{}) bool {
			entry := value.(*rateLimitEntry)
			if now.After(entry.expiresAt) {
				s.entries.Delete(key)
			}
			return true
		})
		s.mu.Unlock()
	}
}

// allow counts one request for key and reports whether it is within
// maxRequests for the current window, starting a new window when there is none
// or the last has ended.
func (s *rateLimiterStore) allow(key string, maxRequests int, window time.Duration, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if val, ok := s.entries.Load(key); ok {
		entry := val.(*rateLimitEntry)
		if !now.After(entry.expiresAt) {
			entry.count++
			return entry.count <= maxRequests
		}
	}
	s.entries.Store(key, &rateLimitEntry{count: 1, expiresAt: now.Add(window)})
	return true
}

// RateLimiter returns a Gin middleware that limits requests per IP address.
// It allows up to maxRequests requests per the specified window duration.
// Default recommendation: 100 requests per 15 minutes.
func RateLimiter(maxRequests int, window time.Duration) gin.HandlerFunc {
	return RateLimiterBy(maxRequests, window, func(c *gin.Context) string { return c.ClientIP() })
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
			key = c.ClientIP()
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
