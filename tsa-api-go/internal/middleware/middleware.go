package middleware

import (
	"github.com/gin-gonic/gin"
)

// SecurityHeaders adds helmet-like security headers to responses.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("Content-Security-Policy", "default-src 'self'")
		c.Next()
	}
}

// NoStore keeps a response out of every cache, the browser's and any in
// between: it carries a signed-in user's own data or sets their session. Vary
// makes a cache that ignores no-store still key it per session and per origin.
func NoStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Vary", "Cookie, Origin")
		c.Next()
	}
}
