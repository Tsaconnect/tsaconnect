package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// CORS returns a Gin middleware that handles Cross-Origin Resource Sharing.
// It allows requests from the configured FrontendURL with credentials.
func CORS(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", cfg.FrontendURL)
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization, X-Requested-With")
		c.Header("Access-Control-Expose-Headers", "Content-Length")
		c.Header("Access-Control-Max-Age", "86400")

		// Handle preflight requests
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
