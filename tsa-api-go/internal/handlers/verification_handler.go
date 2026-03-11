package handlers

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// VerificationHandler holds dependencies for verification handlers.
type VerificationHandler struct {
	cfg *config.Config
}

// NewVerificationHandler creates a new VerificationHandler.
func NewVerificationHandler(cfg *config.Config) *VerificationHandler {
	return &VerificationHandler{cfg: cfg}
}

// SubmitForVerification handles POST /api/verification/submit (auth required).
func (h *VerificationHandler) SubmitForVerification(c *gin.Context) {
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
		log.Printf("SubmitForVerification error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to submit verification",
		})
		return
	}

	// Check minimum requirements: at least one document
	hasDocument := dbUser.Documents.DriversLicense.Front.URL != "" ||
		dbUser.Documents.NIN.Front.URL != "" ||
		dbUser.Documents.Passport.Photo.URL != ""

	if !hasDocument {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Please upload at least one ID document",
		})
		return
	}

	// Check facial images
	hasFacialImages := dbUser.FacialVerification.FaceFront.URL != "" &&
		dbUser.FacialVerification.FaceLeft.URL != "" &&
		dbUser.FacialVerification.FaceRight.URL != ""

	if !hasFacialImages {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Please complete facial verification",
		})
		return
	}

	// Update verification status
	now := time.Now()
	_, err = collection.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{
		"$set": bson.M{
			"verificationStatus":         models.VerificationStatusInReview,
			"submittedForVerificationAt": now,
			"updatedAt":                  now,
		},
	})
	if err != nil {
		log.Printf("SubmitForVerification update error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to submit verification",
		})
		return
	}

	// Create verification log
	logCollection := config.GetCollection("verificationlogs")
	_, err = logCollection.InsertOne(ctx, models.VerificationLog{
		ID:        primitive.NewObjectID(),
		UserID:    user.ID,
		Action:    models.VerificationActionSubmitted,
		Status:    models.VerificationLogStatusInReview,
		Notes:     "User submitted documents for verification",
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
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

// GetVerificationStatus handles GET /api/verification/status (auth required).
func (h *VerificationHandler) GetVerificationStatus(c *gin.Context) {
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
		log.Printf("GetVerificationStatus error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get verification status",
		})
		return
	}

	status := gin.H{
		"overall":     dbUser.VerificationStatus,
		"submittedAt": dbUser.SubmittedForVerificationAt,
		"notes":       dbUser.VerificationNotes,
		"documents": gin.H{
			"driversLicense": gin.H{
				"front": dbUser.Documents.DriversLicense.Front.Verified,
				"back":  dbUser.Documents.DriversLicense.Back.Verified,
			},
			"nin": gin.H{
				"front": dbUser.Documents.NIN.Front.Verified,
				"back":  dbUser.Documents.NIN.Back.Verified,
			},
			"passport": dbUser.Documents.Passport.Photo.Verified,
			"pvc":      dbUser.Documents.PVC.Card.Verified,
			"bvn":      dbUser.Documents.BVN.Verified,
		},
		"facial": gin.H{
			"verified":   dbUser.FacialVerification.Verified,
			"score":      dbUser.FacialVerification.VerificationScore,
			"verifiedAt": dbUser.FacialVerification.VerifiedAt,
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
func (h *VerificationHandler) VerifyBVN(c *gin.Context) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	_, err := collection.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{
		"$set": bson.M{
			"documents.bvn.number":     req.BVN,
			"documents.bvn.verified":   true,
			"documents.bvn.verifiedAt": now,
			"updatedAt":                now,
		},
	})
	if err != nil {
		log.Printf("VerifyBVN update error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "BVN verification service temporarily unavailable",
		})
		return
	}

	// Create verification log
	logCollection := config.GetCollection("verificationlogs")
	_, _ = logCollection.InsertOne(ctx, models.VerificationLog{
		ID:        primitive.NewObjectID(),
		UserID:    user.ID,
		Action:    models.VerificationActionBVNVerification,
		Status:    models.VerificationLogStatusVerified,
		Notes:     "BVN verified successfully",
		CreatedAt: now,
		UpdatedAt: now,
	})

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
func (h *VerificationHandler) VerifyDocument(c *gin.Context) {
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
func (h *VerificationHandler) VerifyFacialImages(c *gin.Context) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	// For a real implementation, images would be uploaded to Cloudinary first.
	// Here we use the provided URLs directly.
	updateData := bson.M{
		"facialVerification.faceFront.url":  req.Images[0],
		"facialVerification.faceLeft.url":   req.Images[1],
		"facialVerification.faceRight.url":  req.Images[2],
		"facialVerification.faceUp.url":     req.Images[3],
		"facialVerification.faceDown.url":   req.Images[4],
		"facialVerification.verified":       isMatch,
		"facialVerification.verificationScore": verificationScore,
		"updatedAt": now,
	}

	if isMatch {
		updateData["facialVerification.verifiedAt"] = now
	}

	_, err := collection.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{"$set": updateData})
	if err != nil {
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

	logCollection := config.GetCollection("verificationlogs")
	_, _ = logCollection.InsertOne(ctx, models.VerificationLog{
		ID:     primitive.NewObjectID(),
		UserID: user.ID,
		Action: models.VerificationActionFacialVerification,
		Status: logStatus,
		Notes:  message,
		Metadata: gin.H{
			"score":         verificationScore,
			"livenessScore": livenessScore,
		},
		CreatedAt: now,
		UpdatedAt: now,
	})

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
func (h *VerificationHandler) GetVerificationHistory(c *gin.Context) {
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

	logCollection := config.GetCollection("verificationlogs")

	opts := options.Find().
		SetSort(bson.M{"createdAt": -1}).
		SetLimit(50)

	cursor, err := logCollection.Find(ctx, bson.M{"userId": user.ID}, opts)
	if err != nil {
		log.Printf("GetVerificationHistory error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get verification history",
		})
		return
	}
	defer cursor.Close(ctx)

	var logs []models.VerificationLog
	if err := cursor.All(ctx, &logs); err != nil {
		log.Printf("GetVerificationHistory decode error: %v", err)
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
func (h *VerificationHandler) ResendVerificationEmail(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Unauthorized",
		})
		return
	}

	// In a real application, this would send a verification email.
	log.Printf("Verification email resent for user: %s", user.ID.Hex())

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Verification email sent successfully",
	})
}

// GetPendingVerifications handles GET /api/verification/admin/pending (admin only).
func (h *VerificationHandler) GetPendingVerifications(c *gin.Context) {
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

	skip := int64((page - 1) * limit)
	limitInt64 := int64(limit)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")
	filter := bson.M{"verificationStatus": models.VerificationStatusInReview}

	// Count total
	total, err := collection.CountDocuments(ctx, filter)
	if err != nil {
		log.Printf("GetPendingVerifications count error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get pending verifications",
		})
		return
	}

	// Find users
	opts := options.Find().
		SetSort(bson.M{"submittedForVerificationAt": 1}).
		SetSkip(skip).
		SetLimit(limitInt64)

	cursor, err := collection.Find(ctx, filter, opts)
	if err != nil {
		log.Printf("GetPendingVerifications find error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get pending verifications",
		})
		return
	}
	defer cursor.Close(ctx)

	var users []models.User
	if err := cursor.All(ctx, &users); err != nil {
		log.Printf("GetPendingVerifications decode error: %v", err)
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

// GetUserVerificationDetails handles GET /api/verification/admin/:userId (admin only).
func (h *VerificationHandler) GetUserVerificationDetails(c *gin.Context) {
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

// ApproveVerification handles POST /api/verification/admin/:userId/approve (admin only).
func (h *VerificationHandler) ApproveVerification(c *gin.Context) {
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

	updateData := bson.M{
		"verificationStatus":                      models.VerificationStatusVerified,
		"verificationNotes":                        notes,
		"documents.driversLicense.front.verified":  true,
		"documents.driversLicense.back.verified":   true,
		"documents.nin.front.verified":             true,
		"documents.nin.back.verified":              true,
		"documents.passport.photo.verified":        true,
		"documents.pvc.card.verified":              true,
		"documents.bvn.verified":                   true,
		"documents.bvn.verifiedAt":                 now,
		"facialVerification.verified":              true,
		"facialVerification.verifiedAt":            now,
		"updatedAt":                                now,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	_, err = collection.UpdateOne(ctx, bson.M{"_id": userID}, bson.M{"$set": updateData})
	if err != nil {
		log.Printf("ApproveVerification error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to approve verification",
		})
		return
	}

	// Create verification log
	logCollection := config.GetCollection("verificationlogs")
	adminID := currentUser.ID
	_, _ = logCollection.InsertOne(ctx, models.VerificationLog{
		ID:        primitive.NewObjectID(),
		UserID:    userID,
		Action:    models.VerificationActionAdminApproval,
		Status:    models.VerificationLogStatusVerified,
		Notes:     notes,
		AdminID:   &adminID,
		CreatedAt: now,
		UpdatedAt: now,
	})

	log.Printf("Verification approved for user %s by admin %s", userID.Hex(), currentUser.ID.Hex())

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

// RejectVerification handles POST /api/verification/admin/:userId/reject (admin only).
func (h *VerificationHandler) RejectVerification(c *gin.Context) {
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

	var req rejectVerificationRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Rejection reason is required",
		})
		return
	}

	now := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	_, err = collection.UpdateOne(ctx, bson.M{"_id": userID}, bson.M{
		"$set": bson.M{
			"verificationStatus": models.VerificationStatusRejected,
			"verificationNotes":  req.Reason,
			"updatedAt":          now,
		},
	})
	if err != nil {
		log.Printf("RejectVerification error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to reject verification",
		})
		return
	}

	// Create verification log
	logCollection := config.GetCollection("verificationlogs")
	adminID := currentUser.ID
	_, _ = logCollection.InsertOne(ctx, models.VerificationLog{
		ID:        primitive.NewObjectID(),
		UserID:    userID,
		Action:    models.VerificationActionAdminRejection,
		Status:    models.VerificationLogStatusRejected,
		Notes:     req.Reason,
		AdminID:   &adminID,
		CreatedAt: now,
		UpdatedAt: now,
	})

	log.Printf("Verification rejected for user %s by admin %s. Reason: %s", userID.Hex(), currentUser.ID.Hex(), req.Reason)

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

// RequestMoreInfo handles POST /api/verification/admin/:userId/request-more-info (admin only).
func (h *VerificationHandler) RequestMoreInfo(c *gin.Context) {
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

	var req requestMoreInfoRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.RequestedInfo == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Requested information is required",
		})
		return
	}

	now := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := config.GetCollection("users")

	_, err = collection.UpdateOne(ctx, bson.M{"_id": userID}, bson.M{
		"$set": bson.M{
			"verificationNotes": fmt.Sprintf("Additional information requested: %s", req.RequestedInfo),
			"updatedAt":         now,
		},
	})
	if err != nil {
		log.Printf("RequestMoreInfo error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to request more information",
		})
		return
	}

	// Create verification log
	logCollection := config.GetCollection("verificationlogs")
	adminID := currentUser.ID
	_, _ = logCollection.InsertOne(ctx, models.VerificationLog{
		ID:        primitive.NewObjectID(),
		UserID:    userID,
		Action:    models.VerificationActionInfoRequested,
		Status:    models.VerificationLogStatusInReview,
		Notes:     fmt.Sprintf("Admin requested additional information: %s", req.RequestedInfo),
		AdminID:   &adminID,
		CreatedAt: now,
		UpdatedAt: now,
	})

	log.Printf("Info requested for user %s by admin %s", userID.Hex(), currentUser.ID.Hex())

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

	if !user.Documents.BVN.Verified {
		steps = append(steps, "Verify your BVN")
	}

	if !user.FacialVerification.Verified {
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
