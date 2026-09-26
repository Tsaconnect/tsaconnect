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

// WebTokenClaim is set (true) on every access token minted for a web session.
// It follows the token wherever it is sent: a web token presented as a bearer
// header is still a web session, and never reaches a staff power.
const WebTokenClaim = "web"

func isWebToken(claims jwt.MapClaims) bool {
	web, _ := claims[WebTokenClaim].(bool)
	return web
}

// Auth is a Gin middleware that authenticates requests using a JWT: the app's
// Bearer header, or — when the web app is on — the web session's access cookie.
func Auth(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		var tokenString string
		fromCookie := false
		if authHeader := c.GetHeader("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
		} else if cfg.WebAuthEnabled() {
			// The web app's session. The app always sends a bearer header, so
			// its behaviour is unchanged. A second access cookie (one a sibling
			// site planted with a Domain attribute) is refused as no session.
			if v, ok := SingleWebCookie(c, WebAccessCookie); ok {
				tokenString, fromCookie = v, true
			}
		}
		if tokenString == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "Please authenticate",
			})
			return
		}
		if fromCookie {
			if !WebRequestFromApp(c, cfg.WebAppOrigin) {
				abortOrigin(c)
				return
			}
			if !safeMethod(c.Request.Method) && c.GetHeader(WebCSRFHeader) != "1" {
				abortCSRF(c)
				return
			}
		}

		claims, userID, ok := parseAccessToken(cfg.JWTSecret, tokenString)
		if !ok {
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

		// Set user and token in context
		c.Set("user", user)
		c.Set("token", tokenString)
		if fromCookie || isWebToken(claims) {
			c.Set(webSessionKey, true)
		}
		c.Next()
	}
}

// parseAccessToken verifies an access JWT (HS256 with secret) and returns its
// claims and user id. ok is false for anything that is not a valid token.
func parseAccessToken(secret, tokenString string) (jwt.MapClaims, uuid.UUID, bool) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		return nil, uuid.Nil, false
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, uuid.Nil, false
	}
	userIDStr, ok := claims["userId"].(string)
	if !ok {
		return nil, uuid.Nil, false
	}
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return nil, uuid.Nil, false
	}
	return claims, userID, true
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}
