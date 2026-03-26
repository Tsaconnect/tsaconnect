package handlers

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

const (
	otpExpiry      = 15 * time.Minute
	otpCooldown    = 60 * time.Second
	otpMaxAttempts = 5
	otpMaxResends  = 5
)

// generateOTP creates a cryptographically random 6-digit code.
func generateOTP() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// sendOTPEmail sends the OTP code via the email service.
func sendOTPEmail(emailService *services.EmailService, toEmail, toName, code string) error {
	return emailService.SendOTP(toEmail, toName, code)
}

// SendOTP handles POST /api/auth/send-otp (auth required).
func (h *Handlers) SendOTP(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	if user.EmailVerified {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Email is already verified"})
		return
	}

	// Check cooldown
	var recent models.EmailVerification
	err := config.DB.Where("user_id = ? AND verified = false", user.ID).
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

	// Check max resends per hour
	var count int64
	oneHourAgo := time.Now().Add(-1 * time.Hour)
	config.DB.Model(&models.EmailVerification{}).
		Where("user_id = ? AND created_at > ?", user.ID, oneHourAgo).
		Count(&count)
	if count >= int64(otpMaxResends) {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"success": false,
			"message": "Too many attempts. Please try again later.",
		})
		return
	}

	// Invalidate previous codes
	config.DB.Model(&models.EmailVerification{}).
		Where("user_id = ? AND verified = false", user.ID).
		Update("expires_at", time.Now())

	// Generate and hash OTP
	code, err := generateOTP()
	if err != nil {
		log.Printf("OTP generation error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Internal server error"})
		return
	}

	hashedCode, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		log.Printf("OTP hash error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Internal server error"})
		return
	}

	verification := models.EmailVerification{
		ID:        uuid.New(),
		UserID:    user.ID,
		Email:     user.Email,
		Code:      string(hashedCode),
		ExpiresAt: time.Now().Add(otpExpiry),
		CreatedAt: time.Now(),
	}

	if err := config.DB.Create(&verification).Error; err != nil {
		log.Printf("OTP save error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Internal server error"})
		return
	}

	if err := sendOTPEmail(h.EmailService, user.Email, user.Name, code); err != nil {
		log.Printf("OTP email error: %v", err)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Verification code sent to your email",
	})
}

type verifyOTPRequest struct {
	Code string `json:"code"`
}

// VerifyOTP handles POST /api/auth/verify-otp (auth required).
func (h *Handlers) VerifyOTP(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	if user.EmailVerified {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Email is already verified"})
		return
	}

	var req verifyOTPRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Verification code is required"})
		return
	}

	var verification models.EmailVerification
	err := config.DB.Where("user_id = ? AND verified = false AND expires_at > ?", user.ID, time.Now()).
		Order("created_at DESC").First(&verification).Error
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "No valid verification code found. Please request a new one."})
		return
	}

	if verification.Attempts >= otpMaxAttempts {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Too many failed attempts. Please request a new code."})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(verification.Code), []byte(req.Code)); err != nil {
		config.DB.Model(&verification).Update("attempts", verification.Attempts+1)
		remaining := otpMaxAttempts - verification.Attempts - 1
		msg := "Invalid verification code"
		if remaining <= 2 && remaining > 0 {
			msg = fmt.Sprintf("Invalid code. %d attempts remaining.", remaining)
		} else if remaining <= 0 {
			msg = "Too many failed attempts. Please request a new code."
		}
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": msg})
		return
	}

	config.DB.Model(&verification).Update("verified", true)
	config.DB.Model(&models.User{}).Where("id = ?", user.ID).Update("email_verified", true)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Email verified successfully",
	})
}

// ResendOTP handles POST /api/auth/resend-otp (auth required).
func (h *Handlers) ResendOTP(c *gin.Context) {
	h.SendOTP(c)
}
