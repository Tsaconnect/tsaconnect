package handlers

import (
	"net/http"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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

	tokenHash := hashToken(req.RefreshToken)
	var stored models.RefreshToken
	result := config.DB.Where("token_hash = ? AND revoked = false AND expires_at > ?", tokenHash, time.Now()).First(&stored)
	if result.Error != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid or expired refresh token"})
		return
	}

	var user models.User
	if err := config.DB.First(&user, "id = ?", stored.UserID).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "User not found"})
		return
	}
	if user.AccountStatus != models.AccountStatusActive {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Account is not active"})
		return
	}

	accessToken, err := generateToken(user.ID, user.Email, h.Config.JWTSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to generate token"})
		return
	}

	rawRefresh, hashedRefresh, err := generateRefreshToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to generate refresh token"})
		return
	}
	rt := models.RefreshToken{
		UserID:    user.ID,
		TokenHash: hashedRefresh,
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}

	err = config.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&stored).Update("revoked", true).Error; err != nil {
			return err
		}
		return tx.Create(&rt).Error
	})
	if err != nil {
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
