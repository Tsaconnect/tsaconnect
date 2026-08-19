package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
)

// diditHTTPClient bounds calls to the Didit API so a hung upstream can't pin
// request goroutines indefinitely.
var diditHTTPClient = &http.Client{Timeout: 15 * time.Second}

// diditSessionCreateResponse is the 201 body from POST /v3/session/.
type diditSessionCreateResponse struct {
	SessionID    string `json:"session_id"`
	SessionToken string `json:"session_token"`
	URL          string `json:"url"`
	Status       string `json:"status"`
	VendorData   string `json:"vendor_data"`
	Detail       string `json:"detail"`
}

// diditWebhookPayload is the V3 status.updated / data.updated envelope.
type diditWebhookPayload struct {
	EventID     string `json:"event_id"`
	WebhookType string `json:"webhook_type"`
	Timestamp   int64  `json:"timestamp"`
	SessionID   string `json:"session_id"`
	Status      string `json:"status"`
	VendorData  string `json:"vendor_data"`
}

// kycUnavailableMessage is what the client sees for every server-side failure in
// the session flow. The diagnosis goes to the log instead: a customer can act on
// neither our configuration nor Didit's uptime, and our vendor's name is not
// theirs to debug.
const kycUnavailableMessage = "Identity verification is temporarily unavailable. Please try again shortly."

// failKYCSession records why a session could not be created and returns the
// generic message. Every branch here is an operator problem, so the log line is
// the only place the cause is ever reported — none of them are user error.
func failKYCSession(c *gin.Context, code int, detail string) {
	log.Printf("[KYC] session failed: %s", detail)
	c.JSON(code, gin.H{"success": false, "message": kycUnavailableMessage})
}

// diditStatusResubmitted is the one non-terminal status that needs the user to
// come back and redo a step (Didit also emails them).
const diditStatusResubmitted = "Resubmitted"

// resumableDiditStatuses are the non-terminal states a user can still pick the
// same hosted session back up from. "In Review" is excluded — Didit is looking
// at it and there is nothing for the user to do.
var resumableDiditStatuses = map[string]bool{
	"":                     true, // created, no status event yet
	"Not Started":          true,
	"In Progress":          true,
	diditStatusResubmitted: true,
}

// latestSessionState returns the most recent Didit status and hosted-session URL
// recorded for sessionID. Metadata is filtered in Go rather than with `->>` so
// this works on both Postgres and the SQLite used by the tests.
func latestSessionState(userID uuid.UUID, sessionID string) (status, url string) {
	if sessionID == "" {
		return "", ""
	}
	var logs []models.VerificationLog
	if err := config.DB.Select("metadata").Where("user_id = ?", userID).
		Order("created_at DESC").Limit(100).Find(&logs).Error; err != nil {
		return "", ""
	}
	for _, l := range logs {
		var m map[string]string
		if json.Unmarshal(l.Metadata, &m) != nil || m["session_id"] != sessionID {
			continue
		}
		if status == "" {
			status = m["status"]
		}
		if url == "" {
			url = m["session_url"]
		}
		if status != "" && url != "" {
			break
		}
	}
	return status, url
}

// CreateKYCSession creates a Didit verification session for the authenticated user.
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

	apiKey := h.Config.DiditAPIKey
	workflowID := h.Config.DiditWorkflowID
	baseURL := strings.TrimRight(h.Config.DiditBaseURL, "/")

	if apiKey == "" || workflowID == "" {
		failKYCSession(c, http.StatusServiceUnavailable,
			"DIDIT_API_KEY or DIDIT_WORKFLOW_ID is unset")
		return
	}

	// Hand back the session already in flight instead of minting a new one: a
	// fresh session abandons any resubmission Didit asked for, is billed again,
	// and orphans the old session's events as stale (see the staleSession guard
	// in KYCWebhook). Kept behind the config check so a dormant integration
	// still reports itself as dormant.
	if status, url := latestSessionState(user.ID, user.DiditSessionID); url != "" && resumableDiditStatuses[status] {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "Resuming verification session",
			"data": gin.H{
				"sessionId":  user.DiditSessionID,
				"sessionUrl": url,
				"resumed":    true,
				"inquiryId":  user.DiditSessionID,
				"inquiryUrl": url,
			},
		})
		return
	}

	payload := map[string]interface{}{
		"workflow_id": workflowID,
		"vendor_data": user.ID.String(),
	}
	if callback := h.Config.DiditCallbackURL; callback != "" {
		payload["callback"] = callback
	}

	body, err := json.Marshal(payload)
	if err != nil {
		failKYCSession(c, http.StatusInternalServerError,
			fmt.Sprintf("failed to marshal session payload: %v", err))
		return
	}

	req, err := http.NewRequest("POST", baseURL+"/session/", bytes.NewReader(body))
	if err != nil {
		failKYCSession(c, http.StatusInternalServerError,
			fmt.Sprintf("failed to build Didit request: %v", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)

	resp, err := diditHTTPClient.Do(req)
	if err != nil {
		failKYCSession(c, http.StatusInternalServerError,
			fmt.Sprintf("Didit request failed: %v", err))
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		failKYCSession(c, http.StatusInternalServerError,
			fmt.Sprintf("failed to read Didit response: %v", err))
		return
	}

	var diditResp diditSessionCreateResponse
	if err := json.Unmarshal(respBody, &diditResp); err != nil {
		failKYCSession(c, http.StatusInternalServerError,
			fmt.Sprintf("failed to decode Didit response: %v", err))
		return
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		// diditResp.Detail is Didit's own wording for operators; it is logged
		// with the body rather than forwarded to the user.
		failKYCSession(c, http.StatusBadGateway,
			fmt.Sprintf("Didit API error: status=%d detail=%q body=%s",
				resp.StatusCode, diditResp.Detail, string(respBody)))
		return
	}

	if diditResp.SessionID == "" || diditResp.URL == "" {
		failKYCSession(c, http.StatusBadGateway,
			fmt.Sprintf("Didit response missing session fields: %s", string(respBody)))
		return
	}

	if err := config.DB.Model(&user).Updates(map[string]interface{}{
		"didit_session_id":    diditResp.SessionID,
		"verification_status": models.VerificationStatusInReview,
	}).Error; err != nil {
		failKYCSession(c, http.StatusInternalServerError,
			fmt.Sprintf("failed to persist session %s for user %s: %v", diditResp.SessionID, user.ID, err))
		return
	}

	// session_url is what lets a later call resume this session rather than
	// start a paid replacement.
	metadata, _ := json.Marshal(map[string]string{
		"session_id":  diditResp.SessionID,
		"session_url": diditResp.URL,
	})
	if err := config.DB.Create(&models.VerificationLog{
		UserID:   user.ID,
		Action:   models.VerificationActionDiditInitiated,
		Status:   models.VerificationLogStatusInReview,
		Notes:    "Didit verification session created",
		Metadata: datatypes.JSON(metadata),
	}).Error; err != nil {
		log.Printf("[KYC] failed to write verification log for user %s: %v", user.ID, err)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "KYC session created",
		"data": gin.H{
			"sessionId":  diditResp.SessionID,
			"sessionUrl": diditResp.URL,
			// Backward-compatible aliases for older app builds.
			"inquiryId":  diditResp.SessionID,
			"inquiryUrl": diditResp.URL,
		},
	})
}

// KYCWebhook receives verification results from Didit.
func (h *Handlers) KYCWebhook(c *gin.Context) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Failed to read body"})
		return
	}

	secret := h.Config.DiditWebhookSecret
	if secret == "" {
		// Fail closed once Didit is configured; when KYC is fully dormant,
		// acknowledge without processing — never act on unsigned payloads.
		if h.Config.DiditAPIKey != "" {
			log.Printf("[KYC] DIDIT_WEBHOOK_SECRET is unset; rejecting webhook")
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Webhook secret not configured"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "KYC not configured; event ignored"})
		return
	} else if !verifyDiditWebhook(bodyBytes, c.GetHeader("X-Timestamp"),
		c.GetHeader("X-Signature"), c.GetHeader("X-Signature-Simple"), secret) {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid signature"})
		return
	}

	var payload diditWebhookPayload
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid JSON"})
		return
	}

	// Only session status changes drive verification_status updates.
	if payload.WebhookType != "status.updated" {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Ignored event type"})
		return
	}

	if payload.SessionID == "" && payload.VendorData == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Missing session data"})
		return
	}

	var user models.User
	query := config.DB
	switch {
	case payload.SessionID != "" && payload.VendorData != "":
		query = query.Where("didit_session_id = ? OR id = ?", payload.SessionID, payload.VendorData)
	case payload.SessionID != "":
		query = query.Where("didit_session_id = ?", payload.SessionID)
	default:
		query = query.Where("id = ?", payload.VendorData)
	}
	if err := query.First(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "User not found for session"})
		return
	}

	// Idempotency: same Didit event_id already processed.
	if payload.EventID != "" {
		var existingLog models.VerificationLog
		alreadyProcessed := config.DB.Where(
			"user_id = ? AND metadata->>'event_id' = ? AND action IN ?",
			user.ID, payload.EventID,
			[]string{models.VerificationActionDiditPassed, models.VerificationActionDiditFailed, models.VerificationActionDiditInitiated},
		).First(&existingLog).Error == nil
		if alreadyProcessed {
			c.JSON(http.StatusOK, gin.H{"success": true, "message": "Already processed"})
			return
		}
	}

	metadataMap := map[string]string{
		"session_id":   payload.SessionID,
		"event_id":     payload.EventID,
		"status":       payload.Status,
		"webhook_type": payload.WebhookType,
	}
	metadata, _ := json.Marshal(metadataMap)

	status := strings.TrimSpace(payload.Status)
	var updates map[string]interface{}
	logEntry := models.VerificationLog{UserID: user.ID, Metadata: datatypes.JSON(metadata)}

	// A user can start a new session (the app allows retrying), so events may
	// arrive from sessions that are no longer current — Expired fires days
	// later. Approval stays monotonic (processed from any session the user
	// completed); everything else from a non-current session is ignored so a
	// stale terminal event can't revoke or disturb the current state.
	staleSession := payload.SessionID != "" && user.DiditSessionID != "" &&
		payload.SessionID != user.DiditSessionID

	switch status {
	case "Approved":
		updates = map[string]interface{}{
			"verification_status": models.VerificationStatusVerified,
			"verification_notes":  "Didit verification approved",
			"didit_session_id":    payload.SessionID,
			"updated_at":          time.Now(),
		}
		logEntry.Action = models.VerificationActionDiditPassed
		logEntry.Status = models.VerificationLogStatusVerified
		logEntry.Notes = "Didit verification approved"

	case "Declined", "Abandoned", "Expired", "Kyc Expired":
		if staleSession {
			c.JSON(http.StatusOK, gin.H{"success": true, "message": "Ignored event from non-current session"})
			return
		}
		updates = map[string]interface{}{
			"verification_status": models.VerificationStatusRejected,
			"verification_notes":  fmt.Sprintf("Didit verification status: %s", status),
			"didit_session_id":    payload.SessionID,
		}
		logEntry.Action = models.VerificationActionDiditFailed
		logEntry.Status = models.VerificationLogStatusRejected
		logEntry.Notes = fmt.Sprintf("Didit verification status: %s", status)

	case "In Review", "In Progress", "Resubmitted", "Awaiting User", "Not Started":
		if staleSession {
			c.JSON(http.StatusOK, gin.H{"success": true, "message": "Ignored event from non-current session"})
			return
		}
		// A late or redelivered non-terminal event must not downgrade a user
		// who is already verified (verified gates P2P + reward eligibility).
		if user.VerificationStatus == models.VerificationStatusVerified {
			c.JSON(http.StatusOK, gin.H{"success": true, "message": "Ignored non-terminal status for verified user"})
			return
		}
		updates = map[string]interface{}{
			"verification_status": models.VerificationStatusInReview,
			"didit_session_id":    payload.SessionID,
		}
		logEntry.Action = models.VerificationActionDiditInitiated
		logEntry.Status = models.VerificationLogStatusInReview
		logEntry.Notes = fmt.Sprintf("Didit status: %s", status)

	default:
		log.Printf("[KYC] Unhandled Didit status=%q session=%s", status, payload.SessionID)
	}

	if updates != nil {
		// Return 500 on persistence failures so Didit redelivers the event.
		if err := config.DB.Model(&user).Updates(updates).Error; err != nil {
			log.Printf("[KYC] failed to update user %s for status %q: %v", user.ID, status, err)
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to persist verification status"})
			return
		}
		if err := config.DB.Create(&logEntry).Error; err != nil {
			log.Printf("[KYC] failed to write verification log for user %s: %v", user.ID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to record verification event"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Webhook processed"})
}

// verifyDiditWebhook validates Didit HMAC signatures.
// Prefers X-Signature (raw body), falls back to X-Signature-Simple (envelope fields).
func verifyDiditWebhook(body []byte, timestampHeader, sigRaw, sigSimple, secret string) bool {
	if timestampHeader == "" {
		return false
	}
	ts, err := strconv.ParseInt(timestampHeader, 10, 64)
	if err != nil {
		return false
	}
	now := time.Now().Unix()
	if diff := now - ts; diff > 300 || diff < -300 {
		return false
	}

	// The header is not covered by the raw-body HMAC, so also require the
	// signed payload's own timestamp to be fresh — otherwise a captured body
	// could be replayed under a fresh X-Timestamp header.
	var envelope diditWebhookPayload
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false
	}
	if envelope.Timestamp != 0 {
		if diff := now - envelope.Timestamp; diff > 300 || diff < -300 {
			return false
		}
	}

	if sigRaw != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		expected := hex.EncodeToString(mac.Sum(nil))
		if hmac.Equal([]byte(strings.ToLower(sigRaw)), []byte(expected)) {
			return true
		}
	}

	if sigSimple != "" {
		canonical := fmt.Sprintf("%d:%s:%s:%s",
			envelope.Timestamp, envelope.SessionID, envelope.Status, envelope.WebhookType)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(canonical))
		expected := hex.EncodeToString(mac.Sum(nil))
		if hmac.Equal([]byte(strings.ToLower(sigSimple)), []byte(expected)) {
			return true
		}
	}

	return false
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

	// Several Didit statuses collapse into in_review, but they mean opposite
	// things to the user: "In Review" is wait, "Resubmitted" is act. Surface the
	// raw status so the client can tell them apart.
	diditStatus, sessionURL := latestSessionState(user.ID, user.DiditSessionID)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"verificationStatus": user.VerificationStatus,
			"verificationNotes":  user.VerificationNotes,
			"diditSessionId":     user.DiditSessionID,
			"diditStatus":        diditStatus,
			"sessionUrl":         sessionURL,
			"actionRequired":     diditStatus == diditStatusResubmitted,
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
	query.Select("id, name, email, phone_number, verification_status, verification_notes, didit_session_id, created_at, updated_at, profile_photo").
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
