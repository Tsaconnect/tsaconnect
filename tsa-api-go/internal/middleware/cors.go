package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// CORS returns a Gin middleware that handles Cross-Origin Resource Sharing.
// It allows requests from the configured FrontendURL, AdminURL, and LandingURL
// with credentials. Each env value may contain a comma-separated list of
// origins, and trailing slashes are stripped to match the browser's Origin
// header (which never includes a path).
func CORS(cfg *config.Config) gin.HandlerFunc {
	allowedOrigins := map[string]bool{}
	for _, raw := range []string{cfg.FrontendURL, cfg.AdminURL, cfg.LandingURL} {
		for _, origin := range strings.Split(raw, ",") {
			origin = strings.TrimRight(strings.TrimSpace(origin), "/")
			if origin != "" {
				allowedOrigins[origin] = true
			}
		}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if allowedOrigins[origin] {
			c.Header("Access-Control-Allow-Origin", origin)
		}
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization, X-Requested-With")
		c.Header("Access-Control-Expose-Headers", "Content-Length")
		c.Header("Access-Control-Max-Age", "3600")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
