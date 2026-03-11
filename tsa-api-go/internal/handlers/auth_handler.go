package handlers

import (
	"context"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// AuthHandler holds dependencies for authentication handlers.
type AuthHandler struct {
	cfg *config.Config
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(cfg *config.Config) *AuthHandler {
	return &AuthHandler{cfg: cfg}
}

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
func (h *AuthHandler) Signup(c *gin.Context) {
	var req signupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid request body",
		})
		return
	}

	// Validate required fields
	var errors []gin.H
	req.Name = strings.TrimSpace(req.Name)
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.TrimSpace(req.Email)
	req.PhoneNumber = strings.TrimSpace(req.PhoneNumber)
	req.Country = strings.TrimSpace(req.Country)
	req.Address = strings.TrimSpace(req.Address)

	if req.Name == "" {
		errors = append(errors, gin.H{"field": "name", "message": "Name is required"})
	}
	if req.Username == "" {
		errors = append(errors, gin.H{"field": "username", "message": "Username is required"})
	}
	if req.Email == "" || !isValidEmail(req.Email) {
		errors = append(errors, gin.H{"field": "email", "message": "Please enter a valid email"})
	}
	if pwdErrors := validatePassword(req.Password); len(pwdErrors) > 0 {
		for _, e := range pwdErrors {
			errors = append(errors, gin.H{"field": "password", "message": e})
		}
	}
	if req.ConfirmPassword != req.Password {
		errors = append(errors, gin.H{"field": "confirmPassword", "message": "Passwords do not match"})
	}
	if req.PhoneNumber == "" {
		errors = append(errors, gin.H{"field": "phoneNumber", "message": "Phone number is required"})
	}
	if req.Country == "" {
		errors = append(errors, gin.H{"field": "country", "message": "Country is required"})
	}
	if req.Address == "" {
		errors = append(errors, gin.H{"field": "address", "message": "Address is required"})
	}

	if len(errors) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"errors":  errors,
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	// Check for existing user (email, username, phoneNumber)
	emailLower := strings.ToLower(req.Email)
	usernameLower := strings.ToLower(req.Username)

	filter := bson.M{
		"$or": bson.A{
			bson.M{"email": emailLower},
			bson.M{"username": usernameLower},
			bson.M{"phoneNumber": req.PhoneNumber},
		},
	}

	var existingUser models.User
	err := collection.FindOne(ctx, filter).Decode(&existingUser)
	if err == nil {
		// User exists - determine which field(s) conflict
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
	} else if err != mongo.ErrNoDocuments {
		log.Printf("Signup DB error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Internal server error",
		})
		return
	}

	// Handle referral code
	var referredBy *primitive.ObjectID
	if req.ReferralCode != "" {
		var referrer models.User
		err := collection.FindOne(ctx, bson.M{
			"referralCode":  req.ReferralCode,
			"accountStatus": models.AccountStatusActive,
		}).Decode(&referrer)
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

	// Build user document
	now := time.Now()
	user := models.User{
		ID:                 primitive.NewObjectID(),
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
		user.ProfilePhoto = &models.ProfilePhoto{URL: req.ProfilePhoto}
	}

	_, err = collection.InsertOne(ctx, user)
	if err != nil {
		log.Printf("Signup insert error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Internal server error",
		})
		return
	}

	// Generate JWT token
	token, err := generateToken(user.ID, user.Email, h.cfg.JWTSecret)
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
			"userId":   user.ID.Hex(),
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
func (h *AuthHandler) UpdateIdentity(c *gin.Context) {
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

	updateData := bson.M{}
	if req.DriversLicenseFront != "" {
		updateData["documents.driversLicense.front.url"] = req.DriversLicenseFront
		updateData["documents.driversLicense.front.verified"] = false
	}
	if req.DriversLicenseBack != "" {
		updateData["documents.driversLicense.back.url"] = req.DriversLicenseBack
		updateData["documents.driversLicense.back.verified"] = false
	}
	if req.NINFront != "" {
		updateData["documents.nin.front.url"] = req.NINFront
		updateData["documents.nin.front.verified"] = false
	}
	if req.NINBack != "" {
		updateData["documents.nin.back.url"] = req.NINBack
		updateData["documents.nin.back.verified"] = false
	}
	if req.PassportPhoto != "" {
		updateData["documents.passport.photo.url"] = req.PassportPhoto
		updateData["documents.passport.photo.verified"] = false
	}
	if req.PVCCard != "" {
		updateData["documents.pvc.card.url"] = req.PVCCard
		updateData["documents.pvc.card.verified"] = false
	}
	if req.BVN != "" {
		updateData["documents.bvn.number"] = req.BVN
		updateData["documents.bvn.verified"] = false
	}

	if len(updateData) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "No documents provided",
		})
		return
	}

	updateData["updatedAt"] = time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	result := collection.FindOneAndUpdate(
		ctx,
		bson.M{"_id": user.ID},
		bson.M{"$set": updateData},
		nil,
	)
	if result.Err() != nil {
		if result.Err() == mongo.ErrNoDocuments {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"message": "User not found",
			})
			return
		}
		log.Printf("UpdateIdentity error: %v", result.Err())
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Internal server error",
		})
		return
	}

	// Fetch updated user to return documents
	var updatedUser models.User
	err := collection.FindOne(ctx, bson.M{"_id": user.ID}).Decode(&updatedUser)
	if err != nil {
		log.Printf("UpdateIdentity fetch error: %v", err)
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
			"userId":    user.ID.Hex(),
			"nextStep":  "facial_verification",
			"documents": updatedUser.Documents,
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
func (h *AuthHandler) UpdateFacial(c *gin.Context) {
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

	updateData := bson.M{}
	if req.FaceFront != "" {
		updateData["facialVerification.faceFront.url"] = req.FaceFront
	}
	if req.FaceLeft != "" {
		updateData["facialVerification.faceLeft.url"] = req.FaceLeft
	}
	if req.FaceRight != "" {
		updateData["facialVerification.faceRight.url"] = req.FaceRight
	}
	if req.FaceUp != "" {
		updateData["facialVerification.faceUp.url"] = req.FaceUp
	}
	if req.FaceDown != "" {
		updateData["facialVerification.faceDown.url"] = req.FaceDown
	}

	// Set verification status to in_review
	updateData["verificationStatus"] = models.VerificationStatusInReview
	updateData["updatedAt"] = time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	result := collection.FindOneAndUpdate(
		ctx,
		bson.M{"_id": user.ID},
		bson.M{"$set": updateData},
		nil,
	)
	if result.Err() != nil {
		if result.Err() == mongo.ErrNoDocuments {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"message": "User not found",
			})
			return
		}
		log.Printf("UpdateFacial error: %v", result.Err())
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
			"userId":             user.ID.Hex(),
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
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid request body",
		})
		return
	}

	// Validate
	var errors []gin.H
	if req.Email == "" || !isValidEmail(req.Email) {
		errors = append(errors, gin.H{"field": "email", "message": "Please enter a valid email"})
	}
	if req.Password == "" {
		errors = append(errors, gin.H{"field": "password", "message": "Password is required"})
	}
	if len(errors) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"errors":  errors,
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	var user models.User
	err := collection.FindOne(ctx, bson.M{"email": strings.ToLower(req.Email)}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
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
		update := bson.M{
			"$set": bson.M{
				"loginAttempts": attempts,
				"updatedAt":     time.Now(),
			},
		}
		if lockUntil != nil {
			update["$set"].(bson.M)["lockUntil"] = lockUntil
		}
		_, _ = collection.UpdateOne(ctx, bson.M{"_id": user.ID}, update)

		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Invalid credentials",
		})
		return
	}

	// Reset login attempts on success
	now := time.Now()
	_, _ = collection.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{
		"$set":   bson.M{"loginAttempts": 0, "lastLogin": now, "updatedAt": now},
		"$unset": bson.M{"lockUntil": ""},
	})

	// Generate JWT token
	token, err := generateToken(user.ID, user.Email, h.cfg.JWTSecret)
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
			"userId":             user.ID.Hex(),
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
func generateToken(userID primitive.ObjectID, email, secret string) (string, error) {
	claims := jwt.MapClaims{
		"userId": userID.Hex(),
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
