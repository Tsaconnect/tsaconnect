package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
)

// CreateKYCSession generates a Smile ID verification session for the authenticated user.
func (h *Handlers) CreateKYCSession(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	if user.VerificationStatus == models.VerificationStatusVerified {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Already verified"})
		return
	}

	partnerID := h.Config.SmileIDPartnerID
	apiKey := h.Config.SmileIDAPIKey
	baseURL := h.Config.SmileIDBaseURL

	jobID := fmt.Sprintf("kyc-%s-%d", user.ID.String(), time.Now().Unix())

	payload := map[string]interface{}{
		"partner_id":         partnerID,
		"job_id":             jobID,
		"user_id":            user.ID.String(),
		"job_type":           6,
		"use_enrolled_image": false,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to create session"})
		return
	}

	req, err := http.NewRequest("POST", baseURL+"/v2/auth_smile", bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to create session"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to reach Smile ID"})
		return
	}
	defer resp.Body.Close()

	var smileResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&smileResp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Invalid response from Smile ID"})
		return
	}

	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "Smile ID returned an error", "details": smileResp})
		return
	}

	config.DB.Model(&user).Updates(map[string]interface{}{
		"smile_job_id":        jobID,
		"verification_status": models.VerificationStatusInReview,
	})

	metadata, _ := json.Marshal(map[string]string{"job_id": jobID})
	config.DB.Create(&models.VerificationLog{
		UserID:   user.ID,
		Action:   models.VerificationActionSmileIDInitiated,
		Status:   models.VerificationLogStatusInReview,
		Notes:    "Smile ID verification session created",
		Metadata: datatypes.JSON(metadata),
	})

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "KYC session created",
		"data": gin.H{
			"jobId":     jobID,
			"partnerId": partnerID,
			"session":   smileResp,
		},
	})
}

// KYCWebhook receives verification results from Smile ID.
func (h *Handlers) KYCWebhook(c *gin.Context) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Failed to read body"})
		return
	}

	secret := h.Config.SmileIDWebhookSecret
	if secret != "" {
		sig := c.GetHeader("X-Smileid-Signature")
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(bodyBytes)
		expected := hex.EncodeToString(mac.Sum(nil))
		if sig != expected {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid signature"})
			return
		}
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid JSON"})
		return
	}

	jobID, _ := payload["job_id"].(string)
	resultCode, _ := payload["result_code"].(string)
	resultText, _ := payload["result_text"].(string)

	if jobID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Missing job_id"})
		return
	}

	var user models.User
	if err := config.DB.Where("smile_job_id = ?", jobID).First(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "User not found for job"})
		return
	}

	var existingLog models.VerificationLog
	alreadyProcessed := config.DB.Where("user_id = ? AND metadata->>'job_id' = ? AND action IN ?",
		user.ID, jobID,
		[]string{models.VerificationActionSmileIDPassed, models.VerificationActionSmileIDFailed},
	).First(&existingLog).Error == nil

	if alreadyProcessed {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Already processed"})
		return
	}

	metadata, _ := json.Marshal(payload)

	passed := resultCode == "0810" || resultCode == "0811"

	if passed {
		now := time.Now()
		config.DB.Model(&user).Updates(map[string]interface{}{
			"verification_status": models.VerificationStatusVerified,
			"verification_notes":  resultText,
			"updated_at":          now,
		})
		config.DB.Create(&models.VerificationLog{
			UserID:   user.ID,
			Action:   models.VerificationActionSmileIDPassed,
			Status:   models.VerificationLogStatusVerified,
			Notes:    resultText,
			Metadata: datatypes.JSON(metadata),
		})
	} else {
		config.DB.Model(&user).Updates(map[string]interface{}{
			"verification_status": models.VerificationStatusRejected,
			"verification_notes":  resultText,
		})
		config.DB.Create(&models.VerificationLog{
			UserID:   user.ID,
			Action:   models.VerificationActionSmileIDFailed,
			Status:   models.VerificationLogStatusRejected,
			Notes:    resultText,
			Metadata: datatypes.JSON(metadata),
		})
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Webhook processed"})
}

// GetKYCStatus returns the current KYC verification status for the authenticated user.
func (h *Handlers) GetKYCStatus(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	var latestLog models.VerificationLog
	config.DB.Where("user_id = ?", user.ID).Order("created_at DESC").First(&latestLog)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"verificationStatus": user.VerificationStatus,
			"verificationNotes":  user.VerificationNotes,
			"smileJobId":         user.SmileJobID,
			"lastAction":         latestLog.Action,
			"lastActionAt":       latestLog.CreatedAt,
		},
	})
}

// AdminOverrideKYC allows an admin to approve a user whose Smile ID verification failed.
func (h *Handlers) AdminOverrideKYC(c *gin.Context) {
	adminUser := getUserFromContext(c)
	if adminUser == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	userID := c.Param("id")

	var user models.User
	if err := config.DB.Where("id = ?", userID).First(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "User not found"})
		return
	}

	var req struct {
		Notes string `json:"notes"`
	}
	c.ShouldBindJSON(&req)

	now := time.Now()
	config.DB.Model(&user).Updates(map[string]interface{}{
		"verification_status": models.VerificationStatusVerified,
		"verification_notes":  req.Notes,
		"updated_at":          now,
	})

	metadata, _ := json.Marshal(map[string]string{
		"admin_id":   adminUser.ID.String(),
		"admin_name": adminUser.Name,
		"reason":     req.Notes,
	})
	config.DB.Create(&models.VerificationLog{
		UserID:   user.ID,
		Action:   models.VerificationActionAdminOverride,
		Status:   models.VerificationLogStatusVerified,
		Notes:    req.Notes,
		Metadata: datatypes.JSON(metadata),
		AdminID:  &adminUser.ID,
	})

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "User verification overridden to approved",
	})
}

// AdminRejectKYC allows an admin to reject a user's verification.
func (h *Handlers) AdminRejectKYC(c *gin.Context) {
	adminUser := getUserFromContext(c)
	if adminUser == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	userID := c.Param("id")

	var user models.User
	if err := config.DB.Where("id = ?", userID).First(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "User not found"})
		return
	}

	var req struct {
		Reason string `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Reason is required"})
		return
	}

	config.DB.Model(&user).Updates(map[string]interface{}{
		"verification_status": models.VerificationStatusRejected,
		"verification_notes":  req.Reason,
	})

	metadata, _ := json.Marshal(map[string]string{
		"admin_id":   adminUser.ID.String(),
		"admin_name": adminUser.Name,
		"reason":     req.Reason,
	})
	config.DB.Create(&models.VerificationLog{
		UserID:   user.ID,
		Action:   models.VerificationActionAdminRejection,
		Status:   models.VerificationLogStatusRejected,
		Notes:    req.Reason,
		Metadata: datatypes.JSON(metadata),
		AdminID:  &adminUser.ID,
	})

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "User verification rejected",
	})
}

// GetAllVerifications returns paginated verification records for admin dashboard.
func (h *Handlers) GetAllVerifications(c *gin.Context) {
	status := c.DefaultQuery("status", "")
	page := 1
	limit := 20

	if p := c.Query("page"); p != "" {
		fmt.Sscanf(p, "%d", &page)
	}
	if l := c.Query("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}

	offset := (page - 1) * limit

	query := config.DB.Model(&models.User{})
	if status != "" {
		query = query.Where("verification_status = ?", status)
	} else {
		query = query.Where("verification_status != ?", models.VerificationStatusPending)
	}

	var total int64
	query.Count(&total)

	var users []models.User
	query.Select("id, name, email, phone_number, verification_status, verification_notes, smile_job_id, created_at, updated_at, profile_photo").
		Order("updated_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&users)

	type UserWithLog struct {
		models.User
		LastAction   string    `json:"lastAction"`
		LastActionAt time.Time `json:"lastActionAt"`
	}

	results := make([]UserWithLog, len(users))
	for i, u := range users {
		results[i] = UserWithLog{User: u}
		var log models.VerificationLog
		if config.DB.Where("user_id = ?", u.ID).Order("created_at DESC").First(&log).Error == nil {
			results[i].LastAction = log.Action
			results[i].LastActionAt = log.CreatedAt
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    results,
		"meta": gin.H{
			"total": total,
			"page":  page,
			"limit": limit,
		},
	})
}
