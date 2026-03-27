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

// CreateKYCSession creates a Persona inquiry for the authenticated user.
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

	apiKey := h.Config.PersonaAPIKey
	templateID := h.Config.PersonaTemplateID
	baseURL := h.Config.PersonaBaseURL

	payload := map[string]interface{}{
		"data": map[string]interface{}{
			"attributes": map[string]interface{}{
				"inquiry-template-id": templateID,
				"reference-id":        user.ID.String(),
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to create session"})
		return
	}

	req, err := http.NewRequest("POST", baseURL+"/inquiries", bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to create session"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Persona-Version", "2023-01-05")
	req.Header.Set("Key-Inflection", "kebab")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to reach Persona"})
		return
	}
	defer resp.Body.Close()

	var personaResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&personaResp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Invalid response from Persona"})
		return
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "Persona returned an error", "details": personaResp})
		return
	}

	// Extract inquiry ID and URL from Persona response
	data, _ := personaResp["data"].(map[string]interface{})
	inquiryID, _ := data["id"].(string)
	attrs, _ := data["attributes"].(map[string]interface{})

	// Build the hosted inquiry URL
	inquiryURL := fmt.Sprintf("https://withpersona.com/verify?inquiry-id=%s", inquiryID)
	if env, ok := attrs["environment"].(string); ok && env == "sandbox" {
		inquiryURL += "&sandbox=true"
	}

	config.DB.Model(&user).Updates(map[string]interface{}{
		"persona_inquiry_id":  inquiryID,
		"verification_status": models.VerificationStatusInReview,
	})

	metadata, _ := json.Marshal(map[string]string{"inquiry_id": inquiryID})
	config.DB.Create(&models.VerificationLog{
		UserID:   user.ID,
		Action:   models.VerificationActionPersonaInitiated,
		Status:   models.VerificationLogStatusInReview,
		Notes:    "Persona verification inquiry created",
		Metadata: datatypes.JSON(metadata),
	})

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "KYC session created",
		"data": gin.H{
			"inquiryId":  inquiryID,
			"inquiryUrl": inquiryURL,
		},
	})
}

// KYCWebhook receives verification results from Persona.
func (h *Handlers) KYCWebhook(c *gin.Context) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Failed to read body"})
		return
	}

	secret := h.Config.PersonaWebhookSecret
	if secret != "" {
		sig := c.GetHeader("Persona-Signature")
		// Persona signature format: "t=<timestamp>,v1=<signature>"
		parts := parsePersonaSignature(sig)
		if parts.timestamp == "" || parts.signature == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid signature format"})
			return
		}
		// Compute HMAC-SHA256 over "timestamp.body"
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(parts.timestamp + "."))
		mac.Write(bodyBytes)
		expected := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(parts.signature), []byte(expected)) {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid signature"})
			return
		}
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid JSON"})
		return
	}

	// Extract event name and inquiry data from Persona webhook payload
	data, _ := payload["data"].(map[string]interface{})
	attrs, _ := data["attributes"].(map[string]interface{})
	eventName, _ := attrs["name"].(string)

	// Get the inquiry from the included array
	included, _ := payload["included"].([]interface{})
	var inquiryID string
	var referenceID string
	for _, item := range included {
		obj, _ := item.(map[string]interface{})
		if objType, _ := obj["type"].(string); objType == "inquiry" {
			inquiryID, _ = obj["id"].(string)
			objAttrs, _ := obj["attributes"].(map[string]interface{})
			referenceID, _ = objAttrs["reference-id"].(string)
			break
		}
	}

	if inquiryID == "" || referenceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Missing inquiry data"})
		return
	}

	var user models.User
	if err := config.DB.Where("persona_inquiry_id = ? OR id = ?", inquiryID, referenceID).First(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "User not found for inquiry"})
		return
	}

	// Check for duplicate processing
	var existingLog models.VerificationLog
	alreadyProcessed := config.DB.Where("user_id = ? AND metadata->>'inquiry_id' = ? AND action IN ?",
		user.ID, inquiryID,
		[]string{models.VerificationActionPersonaPassed, models.VerificationActionPersonaFailed},
	).First(&existingLog).Error == nil

	if alreadyProcessed {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Already processed"})
		return
	}

	metadata, _ := json.Marshal(payload)

	// inquiry.completed = passed, inquiry.failed/inquiry.expired = failed
	passed := eventName == "inquiry.completed"

	if passed {
		now := time.Now()
		config.DB.Model(&user).Updates(map[string]interface{}{
			"verification_status": models.VerificationStatusVerified,
			"verification_notes":  "Persona verification completed",
			"updated_at":          now,
		})
		config.DB.Create(&models.VerificationLog{
			UserID:   user.ID,
			Action:   models.VerificationActionPersonaPassed,
			Status:   models.VerificationLogStatusVerified,
			Notes:    "Persona verification completed",
			Metadata: datatypes.JSON(metadata),
		})
	} else {
		config.DB.Model(&user).Updates(map[string]interface{}{
			"verification_status": models.VerificationStatusRejected,
			"verification_notes":  fmt.Sprintf("Persona verification event: %s", eventName),
		})
		config.DB.Create(&models.VerificationLog{
			UserID:   user.ID,
			Action:   models.VerificationActionPersonaFailed,
			Status:   models.VerificationLogStatusRejected,
			Notes:    fmt.Sprintf("Persona verification event: %s", eventName),
			Metadata: datatypes.JSON(metadata),
		})
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Webhook processed"})
}

// personaSignatureParts holds parsed Persona webhook signature components.
type personaSignatureParts struct {
	timestamp string
	signature string
}

// parsePersonaSignature parses the "t=<ts>,v1=<sig>" format from Persona-Signature header.
func parsePersonaSignature(header string) personaSignatureParts {
	var parts personaSignatureParts
	for _, segment := range bytes.Split([]byte(header), []byte(",")) {
		kv := bytes.SplitN(segment, []byte("="), 2)
		if len(kv) != 2 {
			continue
		}
		key := string(bytes.TrimSpace(kv[0]))
		val := string(bytes.TrimSpace(kv[1]))
		switch key {
		case "t":
			parts.timestamp = val
		case "v1":
			parts.signature = val
		}
	}
	return parts
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
			"personaInquiryId":   user.PersonaInquiryID,
			"lastAction":         latestLog.Action,
			"lastActionAt":       latestLog.CreatedAt,
		},
	})
}

// AdminOverrideKYC allows an admin to approve a user whose verification failed.
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
	query.Select("id, name, email, phone_number, verification_status, verification_notes, persona_inquiry_id, created_at, updated_at, profile_photo").
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
