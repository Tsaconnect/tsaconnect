package handlers

import (
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// signupRequest defines the expected JSON body for signup.
type signupRequest struct {
	Name            string `json:"name"`
	Username        string `json:"username"`
	Email           string `json:"email"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirmPassword"`
	PhoneNumber     string `json:"phoneNumber"`
	Country         string `json:"country"`
	State           string `json:"state"`
	City            string `json:"city"`
	Address         string `json:"address"`
	ReferralCode    string `json:"referralCode"`
	ProfilePhoto    string `json:"profilePhoto"`
}

// Signup handles POST /api/auth/signup.
func (h *Handlers) Signup(c *gin.Context) {
	var req signupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid request body",
		})
		return
	}

	// Validate required fields
	var validationErrors []gin.H
	req.Name = strings.TrimSpace(req.Name)
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.TrimSpace(req.Email)
	req.PhoneNumber = strings.TrimSpace(req.PhoneNumber)
	req.Country = strings.TrimSpace(req.Country)
	req.Address = strings.TrimSpace(req.Address)

	if req.Name == "" {
		validationErrors = append(validationErrors, gin.H{"field": "name", "message": "Name is required"})
	}
	if req.Username == "" {
		validationErrors = append(validationErrors, gin.H{"field": "username", "message": "Username is required"})
	} else if !regexp.MustCompile(`^[a-zA-Z0-9_]{3,20}$`).MatchString(req.Username) {
		validationErrors = append(validationErrors, gin.H{"field": "username", "message": "Username must be 3-20 characters, letters, numbers, or underscores only"})
	}
	if req.Email == "" || !isValidEmail(req.Email) {
		validationErrors = append(validationErrors, gin.H{"field": "email", "message": "Please enter a valid email"})
	}
	if pwdErrors := validatePassword(req.Password); len(pwdErrors) > 0 {
		for _, e := range pwdErrors {
			validationErrors = append(validationErrors, gin.H{"field": "password", "message": e})
		}
	}
	if req.ConfirmPassword != "" && req.ConfirmPassword != req.Password {
		validationErrors = append(validationErrors, gin.H{"field": "confirmPassword", "message": "Passwords do not match"})
	}
	if req.PhoneNumber == "" {
		validationErrors = append(validationErrors, gin.H{"field": "phoneNumber", "message": "Phone number is required"})
	}

	if len(validationErrors) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"errors":  validationErrors,
		})
		return
	}

	// Check for existing user (email, username, phoneNumber)
	emailLower := strings.ToLower(req.Email)
	usernameLower := strings.ToLower(req.Username)

	var existingUser models.User
	err := config.DB.Where("email = ? OR username = ? OR phone_number = ?", emailLower, usernameLower, req.PhoneNumber).First(&existingUser).Error
	if err == nil {
		var conflictErrors []gin.H
		if existingUser.Email == emailLower {
			conflictErrors = append(conflictErrors, gin.H{"field": "email", "message": "Email already registered"})
		}
		if existingUser.Username == usernameLower {
			conflictErrors = append(conflictErrors, gin.H{"field": "username", "message": "Username already taken"})
		}
		if existingUser.PhoneNumber == req.PhoneNumber {
			conflictErrors = append(conflictErrors, gin.H{"field": "phoneNumber", "message": "Phone number already registered"})
		}
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"errors":  conflictErrors,
		})
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("Signup DB error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Internal server error",
		})
		return
	}

	// Handle referral code
	var referredBy *uuid.UUID
	if req.ReferralCode != "" {
		referralCode := strings.ToLower(strings.TrimSpace(req.ReferralCode))
		var referrer models.User
		err := config.DB.Where("referral_code = ? AND account_status = ?", referralCode, models.AccountStatusActive).First(&referrer).Error
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"message": "Invalid referral code. Please check and try again.",
			})
			return
		}
		referredBy = &referrer.ID
	}

	// Hash password
	hashedPassword, err := models.HashPassword(req.Password)
	if err != nil {
		log.Printf("Password hash error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Internal server error",
		})
		return
	}

	// Build user record
	now := time.Now()
	user := models.User{
		ID:                 uuid.New(),
		Name:               req.Name,
		Username:           usernameLower,
		Email:              emailLower,
		Password:           hashedPassword,
		Role:               models.RoleUser,
		PhoneNumber:        req.PhoneNumber,
		Country:            req.Country,
		State:              req.State,
		City:               req.City,
		Address:            req.Address,
		ReferralCode:       usernameLower,
		ReferredBy:         referredBy,
		VerificationStatus: models.VerificationStatusPending,
		AccountStatus:      models.AccountStatusActive,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	if req.ProfilePhoto != "" {
		user.SetProfilePhoto(&models.ProfilePhoto{URL: req.ProfilePhoto})
	}

	if err := config.DB.Create(&user).Error; err != nil {
		log.Printf("Signup insert error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Internal server error",
		})
		return
	}

	// Send email verification OTP
	go func() {
		code, err := generateOTP()
		if err != nil {
			log.Printf("Signup OTP generation error: %v", err)
			return
		}
		hashedCode, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
		if err != nil {
			log.Printf("Signup OTP hash error: %v", err)
			return
		}
		verification := models.EmailVerification{
			ID:        uuid.New(),
			UserID:    user.ID,
			Email:     user.Email,
			Code:      string(hashedCode),
			ExpiresAt: time.Now().Add(15 * time.Minute),
			CreatedAt: time.Now(),
		}
		if err := config.DB.Create(&verification).Error; err != nil {
			log.Printf("Signup OTP save error: %v", err)
			return
		}
		if err := sendOTPEmail(h.EmailService, user.Email, user.Name, code); err != nil {
			log.Printf("Signup OTP email error: %v", err)
		}
	}()

	// Generate JWT token
	token, err := generateToken(user.ID, user.Email, h.Config.JWTSecret)
	if err != nil {
		log.Printf("Token generation error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Internal server error",
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"message": "Registration successful. Please verify your email.",
		"data": gin.H{
			"userId":   user.ID.String(),
			"token":    token,
			"nextStep": "email_verification",
		},
	})
}

// loginRequest defines the expected JSON body for login.
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Login handles POST /api/auth/login.
func (h *Handlers) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid request body",
		})
		return
	}

	// Validate
	var validationErrors []gin.H
	if req.Email == "" || !isValidEmail(req.Email) {
		validationErrors = append(validationErrors, gin.H{"field": "email", "message": "Please enter a valid email"})
	}
	if req.Password == "" {
		validationErrors = append(validationErrors, gin.H{"field": "password", "message": "Password is required"})
	}
	if len(validationErrors) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"errors":  validationErrors,
		})
		return
	}

	var user models.User
	err := config.DB.Where("email = ?", strings.ToLower(req.Email)).First(&user).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "Invalid credentials",
			})
			return
		}
		log.Printf("Login DB error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Internal server error",
		})
		return
	}

	// Check if account is locked
	if user.IsLocked() {
		c.JSON(http.StatusLocked, gin.H{
			"success": false,
			"message": "Account is temporarily locked. Try again later.",
		})
		return
	}

	// Compare password
	if err := user.ComparePassword(req.Password); err != nil {
		// Increment login attempts
		attempts, lockUntil := user.IncrementLoginAttempts()
		updates := map[string]interface{}{
			"login_attempts": attempts,
			"updated_at":     time.Now(),
		}
		if lockUntil != nil {
			updates["lock_until"] = lockUntil
			h.EventBus.Publish(events.Event{
				Type:    events.SecurityAccountLocked,
				UserID:  user.ID,
				Title:   "Account Locked",
				Message: "Your account has been temporarily locked due to multiple failed login attempts",
				Data: map[string]interface{}{
					"attempts": attempts,
				},
			})
		}
		config.DB.Model(&user).Updates(updates)

		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Invalid credentials",
		})
		return
	}

	// Reset login attempts on success
	now := time.Now()
	config.DB.Model(&user).Updates(map[string]interface{}{
		"login_attempts": 0,
		"last_login":     now,
		"updated_at":     now,
		"lock_until":     nil,
	})

	// Generate JWT token
	token, err := generateToken(user.ID, user.Email, h.Config.JWTSecret)
	if err != nil {
		log.Printf("Token generation error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Internal server error",
		})
		return
	}

	h.EventBus.Publish(events.Event{
		Type:    events.SecurityLoginNewDevice,
		UserID:  user.ID,
		Title:   "New Login",
		Message: "A new login was detected on your account",
		Data: map[string]interface{}{
			"ip": c.ClientIP(),
		},
	})

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Login successful",
		"data": gin.H{
			"userId":             user.ID.String(),
			"role":               user.Role,
			"token":              token,
			"name":               user.Name,
			"email":              user.Email,
			"verificationStatus": user.VerificationStatus,
			"accountStatus":      user.AccountStatus,
			"emailVerified":      user.EmailVerified,
		},
	})
}

// generateToken creates a JWT token with userId and email claims, 7-day expiry.
func generateToken(userID uuid.UUID, email, secret string) (string, error) {
	claims := jwt.MapClaims{
		"userId": userID.String(),
		"email":  email,
		"exp":    time.Now().Add(7 * 24 * time.Hour).Unix(),
		"iat":    time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// validatePassword checks password strength and returns a list of error messages.
func validatePassword(password string) []string {
	var errs []string
	if len(password) < 8 {
		errs = append(errs, "Password must be at least 8 characters")
	}
	hasUpper := false
	hasLower := false
	hasDigit := false
	for _, ch := range password {
		if unicode.IsUpper(ch) {
			hasUpper = true
		}
		if unicode.IsLower(ch) {
			hasLower = true
		}
		if unicode.IsDigit(ch) {
			hasDigit = true
		}
	}
	if !hasUpper {
		errs = append(errs, "Password must contain at least one uppercase letter")
	}
	if !hasLower {
		errs = append(errs, "Password must contain at least one lowercase letter")
	}
	if !hasDigit {
		errs = append(errs, "Password must contain at least one number")
	}
	return errs
}

// isValidEmail validates an email address format.
func isValidEmail(email string) bool {
	re := regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	return re.MatchString(email)
}

// getUserFromContext is defined in common.go
