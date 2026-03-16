package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// AdminAuth is a Gin middleware that authenticates requests using JWT Bearer
// tokens and additionally verifies that the user has an 'admin' or 'merchant' role.
func AdminAuth(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "Please authenticate",
			})
			return
		}

		tokenString := strings.TrimPrefix(authHeader, "Bearer ")

		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return []byte(cfg.JWTSecret), nil
		})
		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "Please authenticate",
			})
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "Please authenticate",
			})
			return
		}

		userIDStr, ok := claims["userId"].(string)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "Please authenticate",
			})
			return
		}

		userID, err := uuid.Parse(userIDStr)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "Please authenticate",
			})
			return
		}

		// Look up user in PostgreSQL
		var user models.User
		result := config.DB.First(&user, "id = ?", userID)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"success": false,
					"message": "Please authenticate",
				})
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "Please authenticate",
			})
			return
		}

		// Check account status
		if user.AccountStatus != models.AccountStatusActive {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": fmt.Sprintf("Account is %s. Please contact support.", user.AccountStatus),
			})
			return
		}

		// Check role - must be admin, super_admin, merchant, or support
		allowedRoles := []string{models.RoleAdmin, models.RoleSuperAdmin, models.RoleMerchant, models.RoleSupport}
		roleAllowed := false
		for _, role := range allowedRoles {
			if user.Role == role {
				roleAllowed = true
				break
			}
		}
		if !roleAllowed {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"success": false,
				"message": "Access denied. Admin or merchant only.",
			})
			return
		}

		// Set user and token in context
		c.Set("user", user)
		c.Set("token", tokenString)
		c.Next()
	}
}
