package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// SubmitForVerification handles POST /api/verification/submit (auth required).
func (h *Handlers) SubmitForVerification(c *gin.Context) {
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
		log.Printf("SubmitForVerification error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to submit verification",
		})
		return
	}

	docs := dbUser.GetDocuments()
	fv := dbUser.GetFacialVerification()

	// Check minimum requirements: at least one document
	hasDocument := docs.DriversLicense.Front.URL != "" ||
		docs.NIN.Front.URL != "" ||
		docs.Passport.Photo.URL != ""

	if !hasDocument {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Please upload at least one ID document",
		})
		return
	}

	// Check facial images
	hasFacialImages := fv.FaceFront.URL != "" &&
		fv.FaceLeft.URL != "" &&
		fv.FaceRight.URL != ""

	if !hasFacialImages {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Please complete facial verification",
		})
		return
	}

	// Update verification status
	now := time.Now()
	if err := config.DB.Model(&dbUser).Updates(map[string]interface{}{
		"verification_status":          models.VerificationStatusInReview,
		"submitted_for_verification_at": now,
		"updated_at":                   now,
	}).Error; err != nil {
		log.Printf("SubmitForVerification update error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to submit verification",
		})
		return
	}

	// Create verification log
	verificationLog := models.VerificationLog{
		ID:        uuid.New(),
		UserID:    user.ID,
		Action:    models.VerificationActionSubmitted,
		Status:    models.VerificationLogStatusInReview,
		Notes:     "User submitted documents for verification",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := config.DB.Create(&verificationLog).Error; err != nil {
		log.Printf("SubmitForVerification log error: %v", err)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Verification submitted successfully. Our team will review your documents within 24-48 hours.",
		"data": gin.H{
			"verificationStatus": models.VerificationStatusInReview,
			"estimatedTime":      "24-48 hours",
		},
	})
}

// GetVerificationStatusV handles GET /api/verification/status (auth required).
func (h *Handlers) GetVerificationStatusV(c *gin.Context) {
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
		"overall":     dbUser.VerificationStatus,
		"submittedAt": dbUser.SubmittedForVerificationAt,
		"notes":       dbUser.VerificationNotes,
		"documents": gin.H{
			"driversLicense": gin.H{
				"front": docs.DriversLicense.Front.Verified,
				"back":  docs.DriversLicense.Back.Verified,
			},
			"nin": gin.H{
				"front": docs.NIN.Front.Verified,
				"back":  docs.NIN.Back.Verified,
			},
			"passport": docs.Passport.Photo.Verified,
			"pvc":      docs.PVC.Card.Verified,
			"bvn":      docs.BVN.Verified,
		},
		"facial": gin.H{
			"verified":   fv.Verified,
			"score":      fv.VerificationScore,
			"verifiedAt": fv.VerifiedAt,
		},
		"nextSteps": getNextSteps(&dbUser),
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    status,
	})
}

// verifyBVNRequest defines the expected JSON body for BVN verification.
type verifyBVNRequest struct {
	BVN string `json:"bvn"`
}

// VerifyBVN handles POST /api/verification/bvn/verify (auth required).
func (h *Handlers) VerifyBVN(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	var req verifyBVNRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.BVN == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "BVN is required",
		})
		return
	}

	// Validate BVN format (11 digits)
	bvnRegex := regexp.MustCompile(`^\d{11}$`)
	if !bvnRegex.MatchString(req.BVN) {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "BVN must be 11 digits",
		})
		return
	}

	// Simulate BVN verification (90% success rate for demo)
	isVerified := rand.Float64() > 0.1

	if !isVerified {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "BVN verification failed. Please check the number and try again.",
		})
		return
	}

	now := time.Now()

	// Fetch current user to update documents JSONB
	var dbUser models.User
	if err := config.DB.First(&dbUser, "id = ?", user.ID).Error; err != nil {
		log.Printf("VerifyBVN fetch error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "BVN verification service temporarily unavailable",
		})
		return
	}

	docs := dbUser.GetDocuments()
	docs.BVN.Number = req.BVN
	docs.BVN.Verified = true
	docs.BVN.VerifiedAt = &now
	dbUser.SetDocuments(docs)

	if err := config.DB.Model(&dbUser).Updates(map[string]interface{}{
		"documents":  dbUser.Documents,
		"updated_at": now,
	}).Error; err != nil {
		log.Printf("VerifyBVN update error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "BVN verification service temporarily unavailable",
		})
		return
	}

	// Create verification log
	verificationLog := models.VerificationLog{
		ID:        uuid.New(),
		UserID:    user.ID,
		Action:    models.VerificationActionBVNVerification,
		Status:    models.VerificationLogStatusVerified,
		Notes:     "BVN verified successfully",
		CreatedAt: now,
		UpdatedAt: now,
	}
	_ = config.DB.Create(&verificationLog).Error

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "BVN verified successfully",
		"data": gin.H{
			"verified":   true,
			"verifiedAt": now,
		},
	})
}

// verifyDocumentRequest defines the expected JSON body for document verification.
type verifyDocumentRequest struct {
	ImageURL     string `json:"imageUrl"`
	DocumentType string `json:"documentType"`
}

// VerifyDocument handles POST /api/verification/document/verify (auth required).
func (h *Handlers) VerifyDocument(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	var req verifyDocumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid request body",
		})
		return
	}

	if req.ImageURL == "" || req.DocumentType == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Image URL and document type are required",
		})
		return
	}

	// Simulate document verification (80% valid)
	isValid := rand.Float64() > 0.2
	confidence := rand.Float64()*0.3 + 0.7
	if !isValid {
		confidence = rand.Float64() * 0.3
	}

	var extractedData interface{}
	if isValid {
		extractedData = generateMockDocumentData(req.DocumentType)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Document verification completed",
		"data": gin.H{
			"documentType":  req.DocumentType,
			"verified":      isValid,
			"confidence":    confidence,
			"extractedData": extractedData,
		},
	})
}

// verifyFacialRequest defines the expected JSON body for facial verification.
type verifyFacialRequest struct {
	Images []string `json:"images"`
}

// VerifyFacialImages handles POST /api/verification/facial/verify (auth required).
func (h *Handlers) VerifyFacialImages(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	var req verifyFacialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid request body",
		})
		return
	}

	if len(req.Images) < 5 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "At least 5 facial images are required",
		})
		return
	}

	// Simulate facial recognition
	verificationScore := rand.Float64()*0.2 + 0.8 // 80-100%
	isMatch := verificationScore > 0.85
	livenessScore := rand.Float64()*0.2 + 0.8

	var message string
	if isMatch {
		message = "Facial verification successful"
	} else {
		message = "Facial verification failed. Please retake images."
	}

	now := time.Now()

	// Fetch current user to update facial verification JSONB
	var dbUser models.User
	if err := config.DB.First(&dbUser, "id = ?", user.ID).Error; err != nil {
		log.Printf("VerifyFacialImages fetch error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Facial verification service error",
		})
		return
	}

	fv := dbUser.GetFacialVerification()
	fv.FaceFront.URL = req.Images[0]
	fv.FaceLeft.URL = req.Images[1]
	fv.FaceRight.URL = req.Images[2]
	fv.FaceUp.URL = req.Images[3]
	fv.FaceDown.URL = req.Images[4]
	fv.Verified = isMatch
	fv.VerificationScore = verificationScore
	if isMatch {
		fv.VerifiedAt = &now
	}
	dbUser.SetFacialVerification(fv)

	if err := config.DB.Model(&dbUser).Updates(map[string]interface{}{
		"facial_verification": dbUser.FacialVerification,
		"updated_at":          now,
	}).Error; err != nil {
		log.Printf("VerifyFacialImages update error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Facial verification service error",
		})
		return
	}

	// Create verification log
	var logStatus string
	if isMatch {
		logStatus = models.VerificationLogStatusVerified
	} else {
		logStatus = models.VerificationLogStatusFailed
	}

	metadata, _ := json.Marshal(gin.H{
		"score":         verificationScore,
		"livenessScore": livenessScore,
	})

	verificationLog := models.VerificationLog{
		ID:        uuid.New(),
		UserID:    user.ID,
		Action:    models.VerificationActionFacialVerification,
		Status:    logStatus,
		Notes:     message,
		Metadata:  datatypes.JSON(metadata),
		CreatedAt: now,
		UpdatedAt: now,
	}
	_ = config.DB.Create(&verificationLog).Error

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": message,
		"data": gin.H{
			"verified":          isMatch,
			"verificationScore": verificationScore,
			"livenessScore":     livenessScore,
			"anglesVerified":    len(req.Images),
			"message":           message,
		},
	})
}

// GetVerificationHistory handles GET /api/verification/history (auth required).
func (h *Handlers) GetVerificationHistory(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	var logs []models.VerificationLog
	if err := config.DB.Where("user_id = ?", user.ID).
		Order("created_at DESC").
		Limit(50).
		Find(&logs).Error; err != nil {
		log.Printf("GetVerificationHistory error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get verification history",
		})
		return
	}

	if logs == nil {
		logs = []models.VerificationLog{}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    logs,
	})
}

// ResendVerificationEmail handles POST /api/verification/resend-email (auth required).
func (h *Handlers) ResendVerificationEmail(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	// In a real application, this would send a verification email.
	log.Printf("Verification email resent for user: %s", user.ID.String())

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Verification email sent successfully",
	})
}

// GetPendingVerifications handles GET /api/verification/admin/pending (admin only).
func (h *Handlers) GetPendingVerifications(c *gin.Context) {
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
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}

	db := config.DB.Model(&models.User{}).Where("verification_status = ?", models.VerificationStatusInReview)

	// Count total
	var total int64
	if err := db.Count(&total).Error; err != nil {
		log.Printf("GetPendingVerifications count error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get pending verifications",
		})
		return
	}

	// Find users
	skip := (page - 1) * limit
	var users []models.User
	if err := db.Offset(skip).Limit(limit).Order("submitted_for_verification_at ASC").Find(&users).Error; err != nil {
		log.Printf("GetPendingVerifications find error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get pending verifications",
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

// GetUserVerificationDetails handles GET /api/verification/admin/:id (admin only).
func (h *Handlers) GetUserVerificationDetails(c *gin.Context) {
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
		log.Printf("GetUserVerificationDetails error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get verification details",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    dbUser,
	})
}

// approveVerificationRequest defines the expected JSON body for approval.
type approveVerificationRequest struct {
	Notes string `json:"notes"`
}

// ApproveVerification handles POST /api/verification/admin/:id/approve (admin only).
func (h *Handlers) ApproveVerification(c *gin.Context) {
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

	var req approveVerificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Notes are optional
		req.Notes = ""
	}

	notes := req.Notes
	if notes == "" {
		notes = "Verification approved"
	}

	now := time.Now()

	// Fetch user to update JSONB fields
	var dbUser models.User
	if err := config.DB.First(&dbUser, "id = ?", userID).Error; err != nil {
		log.Printf("ApproveVerification fetch error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to approve verification",
		})
		return
	}

	// Update documents - mark all as verified
	docs := dbUser.GetDocuments()
	docs.DriversLicense.Front.Verified = true
	docs.DriversLicense.Back.Verified = true
	docs.NIN.Front.Verified = true
	docs.NIN.Back.Verified = true
	docs.Passport.Photo.Verified = true
	docs.PVC.Card.Verified = true
	docs.BVN.Verified = true
	docs.BVN.VerifiedAt = &now
	dbUser.SetDocuments(docs)

	// Update facial verification
	fv := dbUser.GetFacialVerification()
	fv.Verified = true
	fv.VerifiedAt = &now
	dbUser.SetFacialVerification(fv)

	if err := config.DB.Model(&dbUser).Updates(map[string]interface{}{
		"verification_status": models.VerificationStatusVerified,
		"verification_notes":  notes,
		"documents":           dbUser.Documents,
		"facial_verification": dbUser.FacialVerification,
		"updated_at":          now,
	}).Error; err != nil {
		log.Printf("ApproveVerification error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to approve verification",
		})
		return
	}

	// Create verification log
	adminID := currentUser.ID
	verificationLog := models.VerificationLog{
		ID:        uuid.New(),
		UserID:    userID,
		Action:    models.VerificationActionAdminApproval,
		Status:    models.VerificationLogStatusVerified,
		Notes:     notes,
		AdminID:   &adminID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	_ = config.DB.Create(&verificationLog).Error

	log.Printf("Verification approved for user %s by admin %s", userID.String(), currentUser.ID.String())

	h.EventBus.Publish(events.Event{
		Type:    events.VerificationApproved,
		UserID:  userID,
		Title:   "Verification Approved",
		Message: "Your identity verification has been approved",
		Data: map[string]interface{}{
			"verificationStatus": models.VerificationStatusVerified,
			"notes":              notes,
		},
	})

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Verification approved successfully",
		"data": gin.H{
			"verificationStatus": models.VerificationStatusVerified,
		},
	})
}

// rejectVerificationRequest defines the expected JSON body for rejection.
type rejectVerificationRequest struct {
	Reason string `json:"reason"`
}

// RejectVerification handles POST /api/verification/admin/:id/reject (admin only).
func (h *Handlers) RejectVerification(c *gin.Context) {
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

	var req rejectVerificationRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Rejection reason is required",
		})
		return
	}

	now := time.Now()

	if err := config.DB.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]interface{}{
		"verification_status": models.VerificationStatusRejected,
		"verification_notes":  req.Reason,
		"updated_at":          now,
	}).Error; err != nil {
		log.Printf("RejectVerification error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to reject verification",
		})
		return
	}

	// Create verification log
	adminID := currentUser.ID
	verificationLog := models.VerificationLog{
		ID:        uuid.New(),
		UserID:    userID,
		Action:    models.VerificationActionAdminRejection,
		Status:    models.VerificationLogStatusRejected,
		Notes:     req.Reason,
		AdminID:   &adminID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	_ = config.DB.Create(&verificationLog).Error

	log.Printf("Verification rejected for user %s by admin %s. Reason: %s", userID.String(), currentUser.ID.String(), req.Reason)

	h.EventBus.Publish(events.Event{
		Type:    events.VerificationRejected,
		UserID:  userID,
		Title:   "Verification Rejected",
		Message: fmt.Sprintf("Your identity verification has been rejected: %s", req.Reason),
		Data: map[string]interface{}{
			"verificationStatus": models.VerificationStatusRejected,
			"reason":             req.Reason,
		},
	})

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Verification rejected",
		"data": gin.H{
			"verificationStatus": models.VerificationStatusRejected,
		},
	})
}

// requestMoreInfoRequest defines the expected JSON body for requesting more info.
type requestMoreInfoRequest struct {
	RequestedInfo string `json:"requestedInfo"`
}

// RequestMoreInfo handles POST /api/verification/admin/:id/request-more-info (admin only).
func (h *Handlers) RequestMoreInfo(c *gin.Context) {
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

	var req requestMoreInfoRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.RequestedInfo == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Requested information is required",
		})
		return
	}

	now := time.Now()

	if err := config.DB.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]interface{}{
		"verification_notes": fmt.Sprintf("Additional information requested: %s", req.RequestedInfo),
		"updated_at":         now,
	}).Error; err != nil {
		log.Printf("RequestMoreInfo error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to request more information",
		})
		return
	}

	// Create verification log
	adminID := currentUser.ID
	verificationLog := models.VerificationLog{
		ID:        uuid.New(),
		UserID:    userID,
		Action:    models.VerificationActionInfoRequested,
		Status:    models.VerificationLogStatusInReview,
		Notes:     fmt.Sprintf("Admin requested additional information: %s", req.RequestedInfo),
		AdminID:   &adminID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	_ = config.DB.Create(&verificationLog).Error

	log.Printf("Info requested for user %s by admin %s", userID.String(), currentUser.ID.String())

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Information request sent to user",
	})
}

// getNextSteps determines the next verification steps for a user.
func getNextSteps(user *models.User) []string {
	var steps []string

	if user.VerificationStatus == models.VerificationStatusPending {
		steps = append(steps, "Submit your documents for verification")
	} else if user.VerificationStatus == models.VerificationStatusRejected {
		steps = append(steps, "Review the rejection reason and resubmit documents")
	}

	docs := user.GetDocuments()
	if !docs.BVN.Verified {
		steps = append(steps, "Verify your BVN")
	}

	fv := user.GetFacialVerification()
	if !fv.Verified {
		steps = append(steps, "Complete facial verification")
	}

	return steps
}

// generateMockDocumentData generates simulated document verification data.
func generateMockDocumentData(documentType string) gin.H {
	data := gin.H{
		"documentType":   documentType,
		"timestamp":      time.Now(),
		"processingTime": rand.Float64()*2 + 1,
	}

	switch documentType {
	case "drivers_license":
		data["licenseNumber"] = fmt.Sprintf("DL%d", rand.Intn(1000000))
		expiry := time.Now().Add(365 * 24 * time.Hour)
		data["expiryDate"] = expiry
		data["stateOfIssue"] = "Lagos"
	case "nin":
		data["ninNumber"] = fmt.Sprintf("%d", rand.Intn(10000000000))
		data["dateOfBirth"] = time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)
	case "passport":
		data["passportNumber"] = fmt.Sprintf("A%d", rand.Intn(10000000))
		expiry := time.Now().Add(3650 * 24 * time.Hour)
		data["expiryDate"] = expiry
		data["nationality"] = "Nigerian"
	case "pvc":
		data["voterId"] = fmt.Sprintf("PVC%d", rand.Intn(1000000))
		data["state"] = "Lagos"
		data["lga"] = "Ikeja"
	}

	return data
}
