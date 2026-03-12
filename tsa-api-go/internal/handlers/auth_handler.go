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
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
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
		var referrer models.User
		err := config.DB.Where("referral_code = ? AND account_status = ?", req.ReferralCode, models.AccountStatusActive).First(&referrer).Error
		if err == nil {
			referredBy = &referrer.ID
		}
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
		"message": "Registration successful. Please proceed to identity verification.",
		"data": gin.H{
			"userId":   user.ID.String(),
			"token":    token,
			"nextStep": "identity_verification",
		},
	})
}

// identityRequest defines the expected JSON body for identity document updates.
type identityRequest struct {
	DriversLicenseFront string `json:"driversLicenseFront"`
	DriversLicenseBack  string `json:"driversLicenseBack"`
	NINFront            string `json:"ninFront"`
	NINBack             string `json:"ninBack"`
	PassportPhoto       string `json:"passportPhoto"`
	PVCCard             string `json:"pvcCard"`
	BVN                 string `json:"bvn"`
}

// UpdateIdentity handles POST /api/auth/identity (auth required).
func (h *Handlers) UpdateIdentity(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	var req identityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid request body",
		})
		return
	}

	// Fetch current user to get existing documents
	var dbUser models.User
	if err := config.DB.First(&dbUser, "id = ?", user.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"message": "User not found",
			})
			return
		}
		log.Printf("UpdateIdentity error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Internal server error",
		})
		return
	}

	docs := dbUser.GetDocuments()
	hasUpdate := false

	if req.DriversLicenseFront != "" {
		docs.DriversLicense.Front.URL = req.DriversLicenseFront
		docs.DriversLicense.Front.Verified = false
		hasUpdate = true
	}
	if req.DriversLicenseBack != "" {
		docs.DriversLicense.Back.URL = req.DriversLicenseBack
		docs.DriversLicense.Back.Verified = false
		hasUpdate = true
	}
	if req.NINFront != "" {
		docs.NIN.Front.URL = req.NINFront
		docs.NIN.Front.Verified = false
		hasUpdate = true
	}
	if req.NINBack != "" {
		docs.NIN.Back.URL = req.NINBack
		docs.NIN.Back.Verified = false
		hasUpdate = true
	}
	if req.PassportPhoto != "" {
		docs.Passport.Photo.URL = req.PassportPhoto
		docs.Passport.Photo.Verified = false
		hasUpdate = true
	}
	if req.PVCCard != "" {
		docs.PVC.Card.URL = req.PVCCard
		docs.PVC.Card.Verified = false
		hasUpdate = true
	}
	if req.BVN != "" {
		docs.BVN.Number = req.BVN
		docs.BVN.Verified = false
		hasUpdate = true
	}

	if !hasUpdate {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "No documents provided",
		})
		return
	}

	dbUser.SetDocuments(docs)
	dbUser.UpdatedAt = time.Now()

	if err := config.DB.Model(&dbUser).Updates(map[string]interface{}{
		"documents":  dbUser.Documents,
		"updated_at": dbUser.UpdatedAt,
	}).Error; err != nil {
		log.Printf("UpdateIdentity error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Internal server error",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Identity documents updated successfully",
		"data": gin.H{
			"userId":    user.ID.String(),
			"nextStep":  "facial_verification",
			"documents": dbUser.GetDocuments(),
		},
	})
}

// facialRequest defines the expected JSON body for facial verification updates.
type facialRequest struct {
	FaceFront string `json:"faceFront"`
	FaceLeft  string `json:"faceLeft"`
	FaceRight string `json:"faceRight"`
	FaceUp    string `json:"faceUp"`
	FaceDown  string `json:"faceDown"`
}

// UpdateFacial handles POST /api/auth/facial (auth required).
func (h *Handlers) UpdateFacial(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	var req facialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid request body",
		})
		return
	}

	// Fetch current user to get existing facial verification data
	var dbUser models.User
	if err := config.DB.First(&dbUser, "id = ?", user.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"message": "User not found",
			})
			return
		}
		log.Printf("UpdateFacial error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Internal server error",
		})
		return
	}

	fv := dbUser.GetFacialVerification()

	if req.FaceFront != "" {
		fv.FaceFront.URL = req.FaceFront
	}
	if req.FaceLeft != "" {
		fv.FaceLeft.URL = req.FaceLeft
	}
	if req.FaceRight != "" {
		fv.FaceRight.URL = req.FaceRight
	}
	if req.FaceUp != "" {
		fv.FaceUp.URL = req.FaceUp
	}
	if req.FaceDown != "" {
		fv.FaceDown.URL = req.FaceDown
	}

	dbUser.SetFacialVerification(fv)
	now := time.Now()

	if err := config.DB.Model(&dbUser).Updates(map[string]interface{}{
		"facial_verification": dbUser.FacialVerification,
		"verification_status": models.VerificationStatusInReview,
		"updated_at":          now,
	}).Error; err != nil {
		log.Printf("UpdateFacial error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Internal server error",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Facial verification images uploaded successfully. Verification is in progress.",
		"data": gin.H{
			"userId":             user.ID.String(),
			"verificationStatus": models.VerificationStatusInReview,
			"estimatedTime":      "24-48 hours",
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
