package middleware

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestLogger returns a Gin middleware that logs the timestamp, HTTP method,
// path, status code, and latency for each request. The path only: a query
// string can carry a secret (the app's /ws/notifications?token=<JWT>) or
// personal data (the account number on a bank lookup).
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// Process request
		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		log.Printf("[%s] %s %s %d %v",
			start.Format(time.RFC3339),
			c.Request.Method,
			c.Request.URL.Path,
			status,
			latency,
		)
	}
}
