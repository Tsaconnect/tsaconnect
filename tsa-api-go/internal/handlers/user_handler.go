package handlers

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// UserHandler holds dependencies for user management handlers.
type UserHandler struct {
	cfg *config.Config
}

// NewUserHandler creates a new UserHandler.
func NewUserHandler(cfg *config.Config) *UserHandler {
	return &UserHandler{cfg: cfg}
}

// GetProfile handles GET /api/users/profile (auth required).
func (h *UserHandler) GetProfile(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	var dbUser models.User
	err := collection.FindOne(ctx, bson.M{"_id": user.ID}).Decode(&dbUser)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"message": "User not found",
			})
			return
		}
		log.Printf("GetProfile error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to fetch profile",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    dbUser,
	})
}

// updateProfileRequest defines allowed fields for profile update.
type updateProfileRequest struct {
	Name        string `json:"name"`
	PhoneNumber string `json:"phoneNumber"`
	Address     string `json:"address"`
	State       string `json:"state"`
	City        string `json:"city"`
}

// UpdateProfile handles PUT /api/users/profile (auth required).
func (h *UserHandler) UpdateProfile(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	var req updateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid request body",
		})
		return
	}

	updates := bson.M{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.PhoneNumber != "" {
		updates["phoneNumber"] = req.PhoneNumber
	}
	if req.Address != "" {
		updates["address"] = req.Address
	}
	if req.State != "" {
		updates["state"] = req.State
	}
	if req.City != "" {
		updates["city"] = req.City
	}

	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "No fields to update",
		})
		return
	}

	updates["updatedAt"] = time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	var updatedUser models.User
	err := collection.FindOneAndUpdate(
		ctx,
		bson.M{"_id": user.ID},
		bson.M{"$set": updates},
		opts,
	).Decode(&updatedUser)
	if err != nil {
		log.Printf("UpdateProfile error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to update profile",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Profile updated successfully",
		"data":    updatedUser,
	})
}

// GetVerificationStatus handles GET /api/users/verification-status (auth required).
func (h *UserHandler) GetVerificationStatus(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	var dbUser models.User
	err := collection.FindOne(ctx, bson.M{"_id": user.ID}).Decode(&dbUser)
	if err != nil {
		log.Printf("GetVerificationStatus error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get verification status",
		})
		return
	}

	status := gin.H{
		"overall": dbUser.VerificationStatus,
		"documents": gin.H{
			"driversLicense": dbUser.Documents.DriversLicense.Front.Verified,
			"nin":            dbUser.Documents.NIN.Front.Verified,
			"passport":       dbUser.Documents.Passport.Photo.Verified,
			"pvc":            dbUser.Documents.PVC.Card.Verified,
			"bvn":            dbUser.Documents.BVN.Verified,
		},
		"facial":    dbUser.FacialVerification.Verified,
		"completed": dbUser.VerificationStatus == models.VerificationStatusVerified,
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    status,
	})
}

// GetDocuments handles GET /api/users/documents (auth required).
func (h *UserHandler) GetDocuments(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	var dbUser models.User
	err := collection.FindOne(ctx, bson.M{"_id": user.ID}).Decode(&dbUser)
	if err != nil {
		log.Printf("GetDocuments error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get documents",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    dbUser.Documents,
	})
}

// uploadProfilePhotoRequest defines the expected JSON body for profile photo upload.
type uploadProfilePhotoRequest struct {
	Image string `json:"image"`
}

// UploadProfilePhoto handles POST /api/users/profile-photo (auth required).
func (h *UserHandler) UploadProfilePhoto(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	var req uploadProfilePhotoRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Image == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "No image provided",
		})
		return
	}

	// In a full implementation, upload to Cloudinary here.
	// For now, store the URL directly.
	profilePhoto := models.ProfilePhoto{
		URL: req.Image,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	var updatedUser models.User
	err := collection.FindOneAndUpdate(
		ctx,
		bson.M{"_id": user.ID},
		bson.M{"$set": bson.M{
			"profilePhoto": profilePhoto,
			"updatedAt":    time.Now(),
		}},
		opts,
	).Decode(&updatedUser)
	if err != nil {
		log.Printf("UploadProfilePhoto error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to upload profile photo",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Profile photo updated successfully",
		"data":    updatedUser.ProfilePhoto,
	})
}

// changePasswordRequest defines the expected JSON body for changing password.
type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// ChangePassword handles PUT /api/users/change-password (auth required).
func (h *UserHandler) ChangePassword(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid request body",
		})
		return
	}

	if req.CurrentPassword == "" || req.NewPassword == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Current password and new password are required",
		})
		return
	}

	// Verify current password
	if err := user.ComparePassword(req.CurrentPassword); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Current password is incorrect",
		})
		return
	}

	// Validate new password strength
	if pwdErrors := validatePassword(req.NewPassword); len(pwdErrors) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "New password is too weak",
			"errors":  pwdErrors,
		})
		return
	}

	// Hash the new password
	hashedPassword, err := models.HashPassword(req.NewPassword)
	if err != nil {
		log.Printf("ChangePassword hash error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to change password",
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	_, err = collection.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{
		"$set": bson.M{
			"password":  hashedPassword,
			"updatedAt": time.Now(),
		},
	})
	if err != nil {
		log.Printf("ChangePassword update error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to change password",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Password changed successfully",
	})
}

// deleteAccountRequest defines the expected JSON body for account deletion.
type deleteAccountRequest struct {
	Password string `json:"password"`
}

// DeleteAccount handles DELETE /api/users/account (auth required, soft delete).
func (h *UserHandler) DeleteAccount(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	var req deleteAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Password is required to delete account",
		})
		return
	}

	// Verify password
	if err := user.ComparePassword(req.Password); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Incorrect password",
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	now := time.Now()
	_, err := collection.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{
		"$set": bson.M{
			"accountStatus": models.AccountStatusDeleted,
			"deletedAt":     now,
			"updatedAt":     now,
		},
	})
	if err != nil {
		log.Printf("DeleteAccount error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to delete account",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Account deleted successfully",
	})
}

// GetReferralStats handles GET /api/users/referral-stats (auth required).
func (h *UserHandler) GetReferralStats(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	opts := options.Find().SetSort(bson.M{"createdAt": -1})
	cursor, err := collection.Find(ctx, bson.M{"referredBy": user.ID}, opts)
	if err != nil {
		log.Printf("GetReferralStats error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get referral stats",
		})
		return
	}
	defer cursor.Close(ctx)

	type referralUser struct {
		Name               string    `bson:"name" json:"name"`
		Email              string    `bson:"email" json:"email"`
		CreatedAt          time.Time `bson:"createdAt" json:"createdAt"`
		VerificationStatus string    `bson:"verificationStatus" json:"verificationStatus"`
	}

	var referrals []referralUser
	for cursor.Next(ctx) {
		var u referralUser
		if err := cursor.Decode(&u); err != nil {
			continue
		}
		referrals = append(referrals, u)
	}

	if referrals == nil {
		referrals = []referralUser{}
	}

	verifiedCount := 0
	pendingCount := 0
	for _, r := range referrals {
		if r.VerificationStatus == models.VerificationStatusVerified {
			verifiedCount++
		}
		if r.VerificationStatus == models.VerificationStatusPending || r.VerificationStatus == models.VerificationStatusInReview {
			pendingCount++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"totalReferrals":    len(referrals),
			"verifiedReferrals": verifiedCount,
			"pendingReferrals":  pendingCount,
			"referrals":         referrals,
		},
	})
}

// CheckUsernameAvailability handles GET /api/users/check-username/:username.
func (h *UserHandler) CheckUsernameAvailability(c *gin.Context) {
	username := c.Param("username")

	if username == "" || len(username) < 3 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Username must be at least 3 characters",
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	var existingUser models.User
	err := collection.FindOne(ctx, bson.M{"username": strings.ToLower(username)}).Decode(&existingUser)

	available := err == mongo.ErrNoDocuments

	var suggestions []string
	if !available {
		suggestions = generateUsernameSuggestions(username)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"available":   available,
			"suggestions": suggestions,
		},
	})
}

// CheckEmailAvailability handles GET /api/users/check-email/:email.
func (h *UserHandler) CheckEmailAvailability(c *gin.Context) {
	email := c.Param("email")

	if email == "" || !isValidEmail(email) {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid email address",
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	var existingUser models.User
	err := collection.FindOne(ctx, bson.M{"email": strings.ToLower(email)}).Decode(&existingUser)

	available := err == mongo.ErrNoDocuments

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"available": available,
		},
	})
}

// GetUserByID handles GET /api/users/:userId (admin only).
func (h *UserHandler) GetUserByID(c *gin.Context) {
	currentUser := getUserFromContext(c)
	if currentUser == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	if currentUser.Role != models.RoleAdmin && currentUser.Role != models.RoleSuperAdmin {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "Access denied",
		})
		return
	}

	userID, err := primitive.ObjectIDFromHex(c.Param("userId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid user ID",
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	var dbUser models.User
	err = collection.FindOne(ctx, bson.M{"_id": userID}).Decode(&dbUser)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"message": "User not found",
			})
			return
		}
		log.Printf("GetUserByID error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get user",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    dbUser,
	})
}

// GetAllUsers handles GET /api/users/ (admin only, with pagination and filters).
func (h *UserHandler) GetAllUsers(c *gin.Context) {
	currentUser := getUserFromContext(c)
	if currentUser == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	if currentUser.Role != models.RoleAdmin && currentUser.Role != models.RoleSuperAdmin {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "Access denied",
		})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	status := c.Query("status")
	verificationStatus := c.Query("verificationStatus")
	search := c.Query("search")

	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}

	filter := bson.M{}
	if status != "" {
		filter["accountStatus"] = status
	}
	if verificationStatus != "" {
		filter["verificationStatus"] = verificationStatus
	}
	if search != "" {
		filter["$or"] = bson.A{
			bson.M{"name": bson.M{"$regex": search, "$options": "i"}},
			bson.M{"email": bson.M{"$regex": search, "$options": "i"}},
			bson.M{"username": bson.M{"$regex": search, "$options": "i"}},
		}
	}

	skip := int64((page - 1) * limit)
	limitInt64 := int64(limit)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	// Count total documents
	total, err := collection.CountDocuments(ctx, filter)
	if err != nil {
		log.Printf("GetAllUsers count error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get users",
		})
		return
	}

	// Find users
	opts := options.Find().
		SetSort(bson.M{"createdAt": -1}).
		SetSkip(skip).
		SetLimit(limitInt64)

	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		log.Printf("GetAllUsers find error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get users",
		})
		return
	}
	defer cursor.Close(ctx)

	var users []models.User
	if err := cursor.All(ctx, &users); err != nil {
		log.Printf("GetAllUsers decode error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get users",
		})
		return
	}

	if users == nil {
		users = []models.User{}
	}

	pages := int(math.Ceil(float64(total) / float64(limit)))

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"users": users,
			"pagination": gin.H{
				"page":  page,
				"limit": limit,
				"total": total,
				"pages": pages,
			},
		},
	})
}

// generateUsernameSuggestions generates alternative username suggestions.
func generateUsernameSuggestions(username string) []string {
	suggestions := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		suggestions = append(suggestions, fmt.Sprintf("%s%d", username, rand.Intn(1000)))
	}
	return suggestions
}
