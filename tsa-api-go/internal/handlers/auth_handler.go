package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
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

// dummyBcryptHash is a pre-hashed cost-10 bcrypt string used to normalize
// response times when user lookup fails, mitigating timing-based user enumeration.
const dummyBcryptHash = "$2a$10$w8TfU0lXh2K5F2nK1i8L7.xPZ1N4rC1J5k9Q2o7X8z6W5y4V3u2T1"

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
	if len(validationErrors) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"errors":  validationErrors,
		})
		return
	}

	// Check for existing user. Email and username are always compared; the phone
	// number is optional at signup, so it only joins the lookup (and the conflict
	// message below) when the user supplied one — an empty phone number must not
	// match every other account that also skipped it.
	emailLower := strings.ToLower(req.Email)
	usernameLower := strings.ToLower(req.Username)

	var existingUser models.User
	query := config.DB.Where("email = ? OR username = ?", emailLower, usernameLower)
	if req.PhoneNumber != "" {
		query = query.Or("phone_number = ?", req.PhoneNumber)
	}
	err := query.First(&existingUser).Error
	if err == nil {
		var conflictErrors []gin.H
		if existingUser.Email == emailLower {
			conflictErrors = append(conflictErrors, gin.H{"field": "email", "message": "Email already registered"})
		}
		if existingUser.Username == usernameLower {
			conflictErrors = append(conflictErrors, gin.H{"field": "username", "message": "Username already taken"})
		}
		if req.PhoneNumber != "" && existingUser.PhoneNumber == req.PhoneNumber {
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

	// SP4 — global-pool new-user-fairness anchor. Seed the user's settlement
	// checkpoint to the cumulative index AT JOIN TIME so they share only
	// global-pool epochs settled AFTER they signed up (owed 0 for prior epochs).
	//
	// This read+insert MUST be one transaction: a plain SELECT of the index
	// followed by a SEPARATE INSERT is a TOCTOU race against
	// SettleGlobalPoolEpoch — an epoch folded between the two would exclude this
	// not-yet-inserted user from its denominator yet seed them a pre-fold
	// checkpoint, letting them claim a share of an epoch they were excluded from
	// (retroactive windfall + over-distribution). CreateUserWithGlobalCheckpoint
	// reads the index and inserts the user in one txn that locks the same pool row
	// the settler locks, so the two serialize and the seeded checkpoint always
	// matches the user's first eligible epoch's denominator boundary.
	//
	// Nil-safe: if the fee engine isn't wired, fall back to a plain insert (a NULL
	// checkpoint is treated as earn-from-origin, the pre-SP4 incumbent rule).
	var createErr error
	if h.FeeService != nil {
		createErr = h.FeeService.CreateUserWithGlobalCheckpoint(c.Request.Context(), &user)
	} else {
		createErr = config.DB.Create(&user).Error
	}
	if createErr != nil {
		log.Printf("Signup insert error: %v", createErr)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Internal server error",
		})
		return
	}

	// SP7 anti-sybil: when this signup used a referral code, run the cheap,
	// bounded velocity heuristic against the REFERRER. If their direct downline
	// grew past the configured threshold in the window, an advisory
	// RewardFlag{source:auto} is written for admin review (it gates the
	// referrer's MCGP claim until cleared — never blocks accrual or this signup).
	// Best-effort + async: a failed/disabled scan must never break account
	// creation. Captured by value (referredBy is a pointer into the local user).
	if referredBy != nil && h.EligibilityService != nil {
		referrerID := *referredBy
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := h.EligibilityService.RunVelocityScan(ctx, referrerID); err != nil {
				log.Printf("[sp7] velocity scan for referrer %s failed (advisory, ignored): %v", referrerID, err)
			}
		}()
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
	if err := config.DB.Create(&rt).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to store refresh token"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"message": "Registration successful. Please verify your email.",
		"data": gin.H{
			"userId":       user.ID.String(),
			"token":        token,
			"accessToken":  token,
			"refreshToken": rawRefresh,
			"nextStep":     "email_verification",
		},
	})
}

// loginRequest defines the expected JSON body for login.
// Accepts either `identifier` (email or username), or legacy `email`, or `username`.
type loginRequest struct {
	Identifier string `json:"identifier"`
	Email      string `json:"email"`
	Username   string `json:"username"`
	Password   string `json:"password"`
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

	// Resolve the login identifier from any of the accepted fields
	identifier := strings.TrimSpace(req.Identifier)
	if identifier == "" {
		identifier = strings.TrimSpace(req.Email)
	}
	if identifier == "" {
		identifier = strings.TrimSpace(req.Username)
	}

	// Validate
	var validationErrors []gin.H
	if identifier == "" {
		validationErrors = append(validationErrors, gin.H{"field": "identifier", "message": "Email or username is required"})
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

	// Look up by email if the identifier looks like an email, otherwise by username.
	// Falls through to the opposite lookup so both formats work even if misclassified.
	var user models.User
	var err error
	lowered := strings.ToLower(identifier)
	if isValidEmail(identifier) {
		err = config.DB.Where("email = ?", lowered).First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = config.DB.Where("LOWER(username) = ?", lowered).First(&user).Error
		}
	} else {
		err = config.DB.Where("LOWER(username) = ?", lowered).First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = config.DB.Where("email = ?", lowered).First(&user).Error
		}
	}
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(req.Password))
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

	// A locked account is refused before the password is checked; a wrong
	// password counts toward the lockout.
	switch err := checkPasswordWithLockout(config.DB, h.EventBus, &user, req.Password); {
	case errors.Is(err, errAccountLocked):
		c.JSON(http.StatusLocked, gin.H{
			"success": false,
			"message": "Account is temporarily locked. Try again later.",
		})
		return
	case err != nil:
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Invalid credentials",
		})
		return
	}

	// Check account status — deleted or suspended accounts must not receive session tokens
	if user.AccountStatus == models.AccountStatusDeleted {
		// Check if there is a pending deletion request within the grace window
		var delReq models.AccountDeletionRequest
		if config.DB != nil {
			if err := config.DB.Where("user_id = ? AND status = ? AND purge_after > ?",
				user.ID, models.AccountDeletionStatusPending, time.Now()).
				Order("created_at DESC").First(&delReq).Error; err == nil {
				daysLeft := int(math.Ceil(time.Until(delReq.PurgeAfter).Hours() / 24))
				if daysLeft < 1 {
					daysLeft = 1
				}
				c.JSON(http.StatusForbidden, gin.H{
					"success": false,
					"message": fmt.Sprintf("Your account is scheduled for deletion on %s (%d days left). You can restore your account to continue.",
						delReq.PurgeAfter.Format("2 January 2006"), daysLeft),
					"code": "ACCOUNT_DELETED_GRACE_PERIOD",
					"data": gin.H{
						"canRestore":    true,
						"purgeAfter":    delReq.PurgeAfter,
						"graceDaysLeft": daysLeft,
					},
				})
				return
			}
		}

		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "This account has been deleted. Please contact support if you wish to restore it.",
			"code":    "ACCOUNT_DELETED",
		})
		return
	}
	if user.AccountStatus != models.AccountStatusActive {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": fmt.Sprintf("Account is %s. Please contact support.", user.AccountStatus),
			"code":    "ACCOUNT_NOT_ACTIVE",
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

	if h.EventBus != nil {
		h.EventBus.Publish(events.Event{
			Type:    events.SecurityLoginNewDevice,
			UserID:  user.ID,
			Title:   "New Login",
			Message: "A new login was detected on your account",
			Data: map[string]interface{}{
				"ip": c.ClientIP(),
			},
		})
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
	if err := config.DB.Create(&rt).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to store refresh token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Login successful",
		"data": gin.H{
			"userId":             user.ID.String(),
			"role":               user.Role,
			"token":              token,
			"accessToken":        token,
			"refreshToken":       rawRefresh,
			"name":               user.Name,
			"email":              user.Email,
			"verificationStatus": user.VerificationStatus,
			"accountStatus":      user.AccountStatus,
			"emailVerified":      user.EmailVerified,
		},
	})
}

// generateToken creates a JWT access token with userId and email claims, 1-hour
// expiry (refresh-token rotation extends the session; see the refresh flow).
func generateToken(userID uuid.UUID, email, secret string) (string, error) {
	claims := jwt.MapClaims{
		"userId": userID.String(),
		"email":  email,
		"exp":    time.Now().Add(1 * time.Hour).Unix(),
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

func generateRefreshToken() (raw string, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(b)
	h := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(h[:])
	return raw, hash, nil
}

func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// getUserFromContext is defined in common.go

// ReactivateAccount handles POST /api/auth/reactivate
// Allows a user whose account was soft-deleted to restore it within the 30-day grace period.
func (h *Handlers) ReactivateAccount(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Identifier == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Identifier and password are required",
		})
		return
	}

	lowered := strings.ToLower(strings.TrimSpace(req.Identifier))
	var user models.User
	var err error
	if strings.Contains(lowered, "@") {
		err = config.DB.Where("email = ?", lowered).First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = config.DB.Where("LOWER(username) = ?", lowered).First(&user).Error
		}
	} else {
		err = config.DB.Where("LOWER(username) = ?", lowered).First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = config.DB.Where("email = ?", lowered).First(&user).Error
		}
	}
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(req.Password))
		}
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Invalid credentials",
		})
		return
	}

	// 1-2. A locked account is refused; a wrong password counts toward the
	// lockout (protects against brute-force).
	switch err := checkPasswordWithLockout(config.DB, h.EventBus, &user, req.Password); {
	case errors.Is(err, errAccountLocked):
		c.JSON(http.StatusLocked, gin.H{
			"success": false,
			"message": "Account is temporarily locked. Try again later.",
		})
		return
	case err != nil:
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Invalid credentials",
		})
		return
	}

	// 3. Password verified: check if account is actually deleted
	if user.AccountStatus != models.AccountStatusDeleted {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Account is not deleted",
		})
		return
	}

	// 4. Verify there is an active pending deletion request within the grace window
	now := time.Now()
	var delReq models.AccountDeletionRequest
	if err := config.DB.Where("user_id = ? AND status = ? AND purge_after > ?",
		user.ID, models.AccountDeletionStatusPending, now).
		Order("created_at DESC").First(&delReq).Error; err != nil {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "The grace period for restoring this account has expired",
			"code":    "ACCOUNT_DELETED",
		})
		return
	}

	// 5. Restore account inside transaction with optimistic concurrency check
	var errStateConflict = errors.New("state_conflict")
	txErr := config.DB.Transaction(func(tx *gorm.DB) error {
		reqRes := tx.Model(&models.AccountDeletionRequest{}).
			Where("id = ? AND status = ?", delReq.ID, models.AccountDeletionStatusPending).
			Updates(map[string]interface{}{
				"status":      models.AccountDeletionStatusRestored,
				"admin_note":  "Self-service restored by user on login",
				"reviewed_at": now,
				"updated_at":  now,
			})
		if reqRes.Error != nil {
			return reqRes.Error
		}
		if reqRes.RowsAffected == 0 {
			return errStateConflict
		}

		userRes := tx.Model(&models.User{}).
			Where("id = ? AND account_status = ?", user.ID, models.AccountStatusDeleted).
			Updates(map[string]interface{}{
				"account_status": models.AccountStatusActive,
				"deleted_at":     nil,
				"updated_at":     now,
				"login_attempts": 0,
				"last_login":     now,
				"lock_until":     nil,
			})
		if userRes.Error != nil {
			return userRes.Error
		}
		if userRes.RowsAffected == 0 {
			return errStateConflict
		}
		return nil
	})
	if txErr != nil {
		if errors.Is(txErr, errStateConflict) {
			c.JSON(http.StatusConflict, gin.H{
				"success": false,
				"message": "Account deletion has already been processed or restored",
				"code":    "ACCOUNT_STATE_CONFLICT",
			})
			return
		}
		log.Printf("ReactivateAccount tx error for %s: %v", user.ID, txErr)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to restore account",
		})
		return
	}

	user.AccountStatus = models.AccountStatusActive
	user.DeletedAt = nil

	if h.EventBus != nil {
		h.EventBus.Publish(events.Event{
			Type:    events.AccountDeletionRestored,
			UserID:  user.ID,
			Title:   "Account Restored",
			Message: "Your TSA Connect account has been restored.",
			Data: map[string]interface{}{
				"requestId": delReq.ID.String(),
				"userId":    user.ID.String(),
			},
		})
	}

	// Generate JWT tokens and return standard login response
	token, err := generateToken(user.ID, user.Email, h.Config.JWTSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to generate token",
		})
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
	if err := config.DB.Create(&rt).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to store refresh token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Account restored successfully",
		"data": gin.H{
			"userId":             user.ID.String(),
			"role":               user.Role,
			"token":              token,
			"accessToken":        token,
			"refreshToken":       rawRefresh,
			"name":               user.Name,
			"email":              user.Email,
			"verificationStatus": user.VerificationStatus,
			"accountStatus":      user.AccountStatus,
			"emailVerified":      user.EmailVerified,
		},
	})
}
