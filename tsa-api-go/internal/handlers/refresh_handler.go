package handlers

import (
	"errors"
	"log"
	"net/http"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"

	"github.com/gin-gonic/gin"
)

type refreshRequest struct {
	RefreshToken string `json:"refreshToken" binding:"required"`
}

func (h *Handlers) RefreshToken(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "refreshToken is required"})
		return
	}

	user, accessToken, rawRefresh, err := h.rotateRefresh(req.RefreshToken)
	switch {
	case errors.Is(err, errRefreshInvalid):
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid or expired refresh token"})
		return
	case errors.Is(err, errRefreshNoUser):
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "User not found"})
		return
	case errors.Is(err, errRefreshInactive):
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Account is not active"})
		return
	case err != nil:
		log.Printf("RefreshToken: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to rotate refresh token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Token refreshed",
		"data": gin.H{
			"accessToken":  accessToken,
			"refreshToken": rawRefresh,
			"userId":       user.ID.String(),
			"email":        user.Email,
			"role":         user.Role,
		},
	})
}

type revokeRequest struct {
	RefreshToken string `json:"refreshToken"`
}

func (h *Handlers) RevokeToken(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Authentication required"})
		return
	}

	var req revokeRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.RefreshToken == "" {
		config.DB.Model(&models.RefreshToken{}).Where("user_id = ? AND revoked = false", user.ID).Update("revoked", true)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "All sessions revoked"})
		return
	}

	tokenHash := hashToken(req.RefreshToken)
	result := config.DB.Model(&models.RefreshToken{}).
		Where("token_hash = ? AND user_id = ? AND revoked = false", tokenHash, user.ID).
		Update("revoked", true)
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Token not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Token revoked"})
}
