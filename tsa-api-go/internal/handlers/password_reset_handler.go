package handlers

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

type forgotPasswordRequest struct {
	Email string `json:"email"`
}

type resetPasswordRequest struct {
	Email       string `json:"email"`
	Code        string `json:"code"`
	NewPassword string `json:"newPassword"`
}

// ForgotPassword handles POST /api/auth/forgot-password (public, no auth).
// Looks up the user by email and sends a password-reset OTP.
func (h *Handlers) ForgotPassword(c *gin.Context) {
	var req forgotPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Email is required"})
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))

	var user models.User
	if err := config.DB.Where("email = ?", email).First(&user).Error; err != nil {
		// Don't reveal whether the email exists
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "If an account with that email exists, a reset code has been sent.",
		})
		return
	}

	// Cooldown: prevent spam
	var recent models.PasswordReset
	err := config.DB.Where("user_id = ? AND used = false", user.ID).
		Order("created_at DESC").First(&recent).Error
	if err == nil && time.Since(recent.CreatedAt) < otpCooldown {
		remaining := otpCooldown - time.Since(recent.CreatedAt)
		c.JSON(http.StatusTooManyRequests, gin.H{
			"success":           false,
			"message":           "Please wait before requesting a new code",
			"retryAfterSeconds": int(remaining.Seconds()),
		})
		return
	}

	// Rate limit: max 5 per hour
	var count int64
	oneHourAgo := time.Now().Add(-1 * time.Hour)
	if err := config.DB.Model(&models.PasswordReset{}).
		Where("user_id = ? AND created_at > ?", user.ID, oneHourAgo).
		Count(&count).Error; err != nil {
		log.Printf("Password reset rate limit count error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Internal server error"})
		return
	}
	if count >= int64(otpMaxResends) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"success": false,
			"message": "Too many attempts. Please try again later.",
		})
		return
	}

	// Invalidate previous codes
	if err := config.DB.Model(&models.PasswordReset{}).
		Where("user_id = ? AND used = false", user.ID).
		Update("expires_at", time.Now()).Error; err != nil {
		log.Printf("Failed to invalidate previous reset codes for user %s: %v", user.ID, err)
	}

	// Generate and hash OTP
	code, err := generateOTP()
	if err != nil {
		log.Printf("Password reset OTP generation error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Internal server error"})
		return
	}

	hashedCode, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		log.Printf("Password reset OTP hash error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Internal server error"})
		return
	}

	resetRecord := models.PasswordReset{
		ID:        uuid.New(),
		UserID:    user.ID,
		Email:     user.Email,
		Code:      string(hashedCode),
		ExpiresAt: time.Now().Add(otpExpiry),
		CreatedAt: time.Now(),
	}

	if err := config.DB.Create(&resetRecord).Error; err != nil {
		log.Printf("Password reset save error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Internal server error"})
		return
	}

	if err := sendOTPEmail(h.EmailService, user.Email, user.Name, code); err != nil {
		log.Printf("Password reset email error for user %s: %v", user.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Unable to send reset code. Please try again later."})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "If an account with that email exists, a reset code has been sent.",
	})
}

// ResetPassword handles POST /api/auth/reset-password (public, no auth).
// Verifies the OTP code and updates the user's password.
func (h *Handlers) ResetPassword(c *gin.Context) {
	var req resetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid request body"})
		return
	}

	if req.Email == "" || req.Code == "" || req.NewPassword == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Email, code, and new password are required"})
		return
	}

	// Validate password strength
	if len(req.NewPassword) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Password must be at least 8 characters"})
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))

	var user models.User
	if err := config.DB.Where("email = ?", email).First(&user).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid email or code"})
		return
	}

	// Find the latest unused, non-expired reset code for this user
	var resetRecord models.PasswordReset
	err := config.DB.Where("user_id = ? AND used = false AND expires_at > ?", user.ID, time.Now()).
		Order("created_at DESC").First(&resetRecord).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "No valid reset code found. Please request a new one."})
			return
		}
		log.Printf("Reset code lookup error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Internal server error"})
		return
	}

	if resetRecord.Attempts >= otpMaxAttempts {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Too many failed attempts. Please request a new code."})
		return
	}

	// Verify the OTP
	if err := bcrypt.CompareHashAndPassword([]byte(resetRecord.Code), []byte(req.Code)); err != nil {
		config.DB.Model(&resetRecord).Update("attempts", resetRecord.Attempts+1)
		remaining := otpMaxAttempts - resetRecord.Attempts - 1
		msg := "Invalid code"
		if remaining <= 2 && remaining > 0 {
			msg = fmt.Sprintf("Invalid code. %d attempts remaining.", remaining)
		} else if remaining <= 0 {
			msg = "Too many failed attempts. Please request a new code."
		}
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": msg})
		return
	}

	// Hash the new password
	hashedPassword, err := models.HashPassword(req.NewPassword)
	if err != nil {
		log.Printf("Password hash error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Internal server error"})
		return
	}

	// Update password and mark code as used in a single transaction
	txErr := config.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
			"password":       hashedPassword,
			"login_attempts": 0,
			"lock_until":     nil,
			"updated_at":     time.Now(),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&resetRecord).Update("used", true).Error
	})
	if txErr != nil {
		log.Printf("Password reset transaction error for user %s: %v", user.ID, txErr)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to update password. Please try again."})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Password has been reset successfully. You can now log in.",
	})
}
