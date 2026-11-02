package middleware

import (
	"log"
	"strings"
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
			loggedPath(c),
			status,
			latency,
		)
	}
}

// loggedPath is the request path, except on a route that carries a credential
// in its path (a ":secret" segment, e.g. a vendor callback that cannot send a
// header): there it is the route template, so the credential never reaches the
// log sink. Other paths stay literal — an order id in a path is useful to see.
func loggedPath(c *gin.Context) string {
	if route := c.FullPath(); strings.Contains(route, "/:secret") {
		return route
	}
	return c.Request.URL.Path
}
