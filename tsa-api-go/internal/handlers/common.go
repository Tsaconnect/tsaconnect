package handlers

import (
	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// getUserFromContext extracts the authenticated user from the Gin context.
func getUserFromContext(c *gin.Context) *models.User {
	user, exists := c.Get("user")
	if !exists {
		return nil
	}
	switch v := user.(type) {
	case *models.User:
		return v
	case models.User:
		return &v
	default:
		return nil
	}
}
