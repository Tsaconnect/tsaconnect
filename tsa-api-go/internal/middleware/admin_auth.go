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
// A web session's token is refused as no token at all, however it is sent.
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
		if !ok || isWebToken(claims) {
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

// RequireP2PAdmin restricts a route to true admin roles (admin / super_admin).
//
// AdminAuth above admits merchants and support — they need it for inventory,
// support tickets, etc. P2P dispute resolution moves user funds and writes
// to the audit ledger, so it needs the tighter gate. Apply this AFTER
// AdminAuth has populated the user. A web session is refused as unauthenticated.
func RequireP2PAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		val, ok := c.Get("user")
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false, "message": "Please authenticate",
			})
			return
		}
		user, ok := val.(models.User)
		if !ok {
			if p, pok := val.(*models.User); pok {
				user = *p
			} else {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"success": false, "message": "Please authenticate",
				})
				return
			}
		}
		if IsWebSession(c) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false, "message": "Please authenticate",
			})
			return
		}
		if user.Role != models.RoleAdmin && user.Role != models.RoleSuperAdmin {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"success": false, "message": "P2P admin role required",
			})
			return
		}
		c.Next()
	}
}

// RequireSuperAdmin gates an action to super_admin only. Used for operations a
// plain admin must not perform even though they can view the surrounding page —
// e.g. IT order money-movements (reverse/settle/credit/reprice), where the
// admin UI shows the list to any admin but restricts the action buttons.
// Layer AFTER an auth middleware that sets the "user" context value. A web
// session is refused as unauthenticated.
func RequireSuperAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		val, ok := c.Get("user")
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false, "message": "Please authenticate",
			})
			return
		}
		user, ok := val.(models.User)
		if !ok {
			if p, pok := val.(*models.User); pok {
				user = *p
			} else {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"success": false, "message": "Please authenticate",
				})
				return
			}
		}
		if IsWebSession(c) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false, "message": "Please authenticate",
			})
			return
		}
		if user.Role != models.RoleSuperAdmin {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"success": false, "message": "Super admin role required",
			})
			return
		}
		c.Next()
	}
}
