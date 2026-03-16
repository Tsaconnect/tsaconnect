package handlers

import (
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// GetProfile handles GET /api/users/profile (auth required).
func (h *Handlers) GetProfile(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	var dbUser models.User
	err := config.DB.First(&dbUser, "id = ?", user.ID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
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
func (h *Handlers) UpdateProfile(c *gin.Context) {
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

	updates := map[string]interface{}{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.PhoneNumber != "" {
		updates["phone_number"] = req.PhoneNumber
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

	updates["updated_at"] = time.Now()

	var updatedUser models.User
	if err := config.DB.First(&updatedUser, "id = ?", user.ID).Error; err != nil {
		log.Printf("UpdateProfile error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to update profile",
		})
		return
	}

	if err := config.DB.Model(&updatedUser).Updates(updates).Error; err != nil {
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
func (h *Handlers) GetVerificationStatus(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	var dbUser models.User
	err := config.DB.First(&dbUser, "id = ?", user.ID).Error
	if err != nil {
		log.Printf("GetVerificationStatus error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get verification status",
		})
		return
	}

	docs := dbUser.GetDocuments()
	fv := dbUser.GetFacialVerification()

	status := gin.H{
		"overall": dbUser.VerificationStatus,
		"documents": gin.H{
			"driversLicense": docs.DriversLicense.Front.Verified,
			"nin":            docs.NIN.Front.Verified,
			"passport":       docs.Passport.Photo.Verified,
			"pvc":            docs.PVC.Card.Verified,
			"bvn":            docs.BVN.Verified,
		},
		"facial":    fv.Verified,
		"completed": dbUser.VerificationStatus == models.VerificationStatusVerified,
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    status,
	})
}

// GetDocuments handles GET /api/users/documents (auth required).
func (h *Handlers) GetDocuments(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	var dbUser models.User
	err := config.DB.First(&dbUser, "id = ?", user.ID).Error
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
		"data":    dbUser.GetDocuments(),
	})
}

// uploadProfilePhotoRequest defines the expected JSON body for profile photo upload.
type uploadProfilePhotoRequest struct {
	Image string `json:"image"`
}

// UploadProfilePhoto handles POST /api/users/profile-photo (auth required).
func (h *Handlers) UploadProfilePhoto(c *gin.Context) {
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

	var updatedUser models.User
	if err := config.DB.First(&updatedUser, "id = ?", user.ID).Error; err != nil {
		log.Printf("UploadProfilePhoto error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to upload profile photo",
		})
		return
	}

	updatedUser.SetProfilePhoto(&models.ProfilePhoto{URL: req.Image})

	if err := config.DB.Model(&updatedUser).Updates(map[string]interface{}{
		"profile_photo": updatedUser.ProfilePhoto,
		"updated_at":    time.Now(),
	}).Error; err != nil {
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
		"data":    updatedUser.GetProfilePhoto(),
	})
}

// changePasswordRequest defines the expected JSON body for changing password.
type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// ChangePassword handles PUT /api/users/change-password (auth required).
func (h *Handlers) ChangePassword(c *gin.Context) {
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

	if err := config.DB.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
		"password":   hashedPassword,
		"updated_at": time.Now(),
	}).Error; err != nil {
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
func (h *Handlers) DeleteAccount(c *gin.Context) {
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

	now := time.Now()
	if err := config.DB.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
		"account_status": models.AccountStatusDeleted,
		"deleted_at":     now,
		"updated_at":     now,
	}).Error; err != nil {
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
func (h *Handlers) GetReferralStats(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	type referralUser struct {
		Name               string    `json:"name"`
		Email              string    `json:"email"`
		CreatedAt          time.Time `json:"createdAt"`
		VerificationStatus string    `json:"verificationStatus"`
	}

	var referrals []referralUser
	if err := config.DB.Model(&models.User{}).
		Select("name, email, created_at, verification_status").
		Where("referred_by = ?", user.ID).
		Order("created_at DESC").
		Find(&referrals).Error; err != nil {
		log.Printf("GetReferralStats error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get referral stats",
		})
		return
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
func (h *Handlers) CheckUsernameAvailability(c *gin.Context) {
	username := c.Param("username")

	if username == "" || len(username) < 3 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Username must be at least 3 characters",
		})
		return
	}

	var existingUser models.User
	err := config.DB.Where("username = ?", strings.ToLower(username)).First(&existingUser).Error

	available := errors.Is(err, gorm.ErrRecordNotFound)

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
func (h *Handlers) CheckEmailAvailability(c *gin.Context) {
	email := c.Param("email")

	if email == "" || !isValidEmail(email) {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid email address",
		})
		return
	}

	var existingUser models.User
	err := config.DB.Where("email = ?", strings.ToLower(email)).First(&existingUser).Error

	available := errors.Is(err, gorm.ErrRecordNotFound)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"available": available,
		},
	})
}

// GetUserByID handles GET /api/users/:id (admin only).
func (h *Handlers) GetUserByID(c *gin.Context) {
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

	userID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid user ID",
		})
		return
	}

	var dbUser models.User
	err = config.DB.First(&dbUser, "id = ?", userID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
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
func (h *Handlers) GetAllUsers(c *gin.Context) {
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

	db := config.DB.Model(&models.User{})

	if status != "" {
		db = db.Where("account_status = ?", status)
	}
	if verificationStatus != "" {
		db = db.Where("verification_status = ?", verificationStatus)
	}
	if search != "" {
		pat := "%" + search + "%"
		db = db.Where("name ILIKE ? OR email ILIKE ? OR username ILIKE ?", pat, pat, pat)
	}

	// Count total records
	var total int64
	if err := db.Count(&total).Error; err != nil {
		log.Printf("GetAllUsers count error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get users",
		})
		return
	}

	// Find users
	skip := (page - 1) * limit
	var users []models.User
	if err := db.Offset(skip).Limit(limit).Order("created_at DESC").Find(&users).Error; err != nil {
		log.Printf("GetAllUsers find error: %v", err)
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
				"page":       page,
				"limit":      limit,
				"total":      total,
				"totalPages": pages,
			},
		},
	})
}

// UpdateUserRole handles PATCH /api/users/:id/role - super admin only.
func (h *Handlers) UpdateUserRole(c *gin.Context) {
	caller := getUserFromContext(c)
	if caller == nil || caller.Role != models.RoleSuperAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Only super admins can change roles")
		return
	}

	userID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid user ID")
		return
	}

	var body struct {
		Role string `json:"role" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Role is required")
		return
	}

	validRoles := map[string]bool{
		models.RoleUser:       true,
		models.RoleAdmin:      true,
		models.RoleSuperAdmin: true,
		models.RoleMerchant:   true,
		models.RoleSupport:    true,
	}
	if !validRoles[body.Role] {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid role")
		return
	}

	var user models.User
	if err := config.DB.First(&user, "id = ?", userID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "User not found")
		return
	}

	if err := config.DB.Model(&user).Update("role", body.Role).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update role")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Role updated successfully", gin.H{
		"userId": user.ID,
		"role":   body.Role,
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
