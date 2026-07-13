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

type rateLimiterStore struct {
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
		s.entries.Range(func(key, value interface{}) bool {
			entry := value.(*rateLimitEntry)
			if now.After(entry.expiresAt) {
				s.entries.Delete(key)
			}
			return true
		})
	}
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
		now := time.Now()

		val, loaded := store.entries.Load(key)
		if !loaded {
			// First request for this key
			store.entries.Store(key, &rateLimitEntry{
				count:     1,
				expiresAt: now.Add(window),
			})
			c.Next()
			return
		}

		entry := val.(*rateLimitEntry)

		// If the window has expired, reset the counter
		if now.After(entry.expiresAt) {
			store.entries.Store(key, &rateLimitEntry{
				count:     1,
				expiresAt: now.Add(window),
			})
			c.Next()
			return
		}

		// Increment the counter
		entry.count++

		if entry.count > maxRequests {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"success": false,
				"message": "Too many requests, please try again later.",
			})
			return
		}

		c.Next()
	}
}
