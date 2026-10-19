package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/datatypes"
	"gorm.io/gorm"
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

// diditIDVerification is one feature-report item from a session decision. V3
// workflows are graphs that can run the same feature more than once, so every
// feature block comes back as an array of these, each carrying its own status.
type diditIDVerification struct {
	Status    string `json:"status"`
	FullName  string `json:"full_name"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	// Read only by a name change's same-person check (fetchDiditIdentity):
	// compared across two sessions, never logged, and the document number
	// never stored.
	DateOfBirth    string `json:"date_of_birth"`
	DocumentNumber string `json:"document_number"`
	IssuingState   string `json:"issuing_state"`
}

// diditLivenessCheck is one liveness feature item. Matches are the earlier
// sessions Didit's face search found the same face in (workflows with face
// search / duplicate detection on; empty otherwise).
type diditLivenessCheck struct {
	Status  string `json:"status"`
	Matches []struct {
		SessionID  string  `json:"session_id"`
		Similarity float64 `json:"similarity_percentage"`
	} `json:"matches"`
}

// diditDecisionResponse is GET /v3/session/{id}/decision/. Only the fields a
// decision here is made on are modelled — nothing else in the identity report
// is ours to keep.
type diditDecisionResponse struct {
	Status           string                `json:"status"`
	IDVerifications  []diditIDVerification `json:"id_verifications"`
	NFCVerifications []diditIDVerification `json:"nfc_verifications"`
	LivenessChecks   []diditLivenessCheck  `json:"liveness_checks"`
}

// errDiditNameUnresolved means Didit has no usable name for this session and
// re-asking will not change that: the session is gone (the Aug 2026 outage left
// a handful of those behind) or the workflow withholds name attributes.
var errDiditNameUnresolved = errors.New("didit: no verified name for session")

// fetchDiditVerifiedName reads the document name off a session's decision.
//
// The name is pulled instead of read from the webhook body because V3 only
// guarantees it in the decision report, and because the X-Signature-Simple
// fallback signs the envelope alone — trusting a payload field would let
// unsigned data decide who a bank transfer belongs to. Didit documents this
// webhook-then-fetch order as the recommended pattern.
//
// ctx is the caller's, not the background: the backfill makes one of these
// calls per user in a loop, and without it an operator who walked away — or a
// proxy that gave up on the request — left the run spending provider calls
// nobody would ever see the result of.
func fetchDiditVerifiedName(ctx context.Context, baseURL, apiKey, sessionID string) (string, error) {
	decision, err := fetchDiditDecision(ctx, baseURL, apiKey, sessionID)
	if err != nil {
		return "", err
	}
	if item := approvedIdentity(decision); item != nil {
		return item.name(), nil
	}
	return "", errDiditNameUnresolved
}

// fetchDiditDecision reads one session's decision report. errDiditNameUnresolved
// means Didit no longer holds the session.
func fetchDiditDecision(ctx context.Context, baseURL, apiKey, sessionID string) (*diditDecisionResponse, error) {
	if sessionID == "" {
		return nil, errDiditNameUnresolved
	}

	endpoint := strings.TrimRight(baseURL, "/") + "/session/" + url.PathEscape(sessionID) + "/decision/"
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", apiKey)

	resp, err := diditHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errDiditNameUnresolved
	}
	if resp.StatusCode != http.StatusOK {
		// The body is the identity report, and error strings get logged, so the
		// status code is the most that may go into one.
		return nil, fmt.Errorf("didit decision lookup returned status %d", resp.StatusCode)
	}

	var decision diditDecisionResponse
	if err := json.NewDecoder(resp.Body).Decode(&decision); err != nil {
		return nil, errors.New("didit decision response was not valid JSON")
	}
	return &decision, nil
}

// approvedIdentity is the first document feature item that passed and carries
// a name. NFC is checked second: when a workflow reads the passport chip that
// name is the more authoritative one, but most workflows never run it.
func approvedIdentity(decision *diditDecisionResponse) *diditIDVerification {
	for _, block := range [][]diditIDVerification{decision.IDVerifications, decision.NFCVerifications} {
		if item := firstApprovedItem(block); item != nil {
			return item
		}
	}
	return nil
}

// errDiditNoDocument means Didit holds the session but the user never submitted
// an identity document in it (Not Started, Expired before upload), so there is
// no name to read and nothing for an admin to approve.
var errDiditNoDocument = errors.New("didit: session has no submitted document")

// errDiditDocumentUnreadable means the user did submit a document but Didit
// read no name off it — the NIN slip, mostly. The name is on the image in the
// Didit console, so an admin can read it and type it in.
var errDiditDocumentUnreadable = errors.New("didit: submitted document has no readable name")

// fetchDiditDocumentName reads the document name for an admin who is approving
// the session by override: a passed check first, else any check that read a
// name — Declined or In Review is exactly what an override approves, so the
// admin's decision vouches for that reading. Never use it where no admin has
// approved: there the unpassed reading is only the scan's guess
// (fetchDiditVerifiedName).
func fetchDiditDocumentName(ctx context.Context, baseURL, apiKey, sessionID string) (string, error) {
	decision, err := fetchDiditDecision(ctx, baseURL, apiKey, sessionID)
	if err != nil {
		return "", err
	}
	if item := approvedIdentity(decision); item != nil {
		return item.name(), nil
	}
	submitted := false
	for _, block := range [][]diditIDVerification{decision.IDVerifications, decision.NFCVerifications} {
		for i := range block {
			if name := block[i].name(); name != "" {
				return name, nil
			}
			submitted = submitted || block[i].submitted()
		}
	}
	if submitted {
		return "", errDiditDocumentUnreadable
	}
	return "", errDiditNoDocument
}

// fetchDiditIdentity is what a session's decision says about the person, for a
// name change's same-person check: the approved document's name, date of
// birth, document number and issuing country, and the earlier sessions Didit's
// face search matched. errDiditNameUnresolved: the session is gone or passed
// no document.
func fetchDiditIdentity(ctx context.Context, baseURL, apiKey, sessionID string) (*services.NameChangeIdentity, error) {
	decision, err := fetchDiditDecision(ctx, baseURL, apiKey, sessionID)
	if err != nil {
		return nil, err
	}
	item := approvedIdentity(decision)
	if item == nil {
		return nil, errDiditNameUnresolved
	}
	id := &services.NameChangeIdentity{Name: item.name(), DateOfBirth: item.DateOfBirth,
		DocumentNumber: item.DocumentNumber, IssuingCountry: item.IssuingState}
	for _, check := range decision.LivenessChecks {
		for _, m := range check.Matches {
			id.FaceMatches = append(id.FaceMatches, services.NameChangeFaceMatch{SessionID: m.SessionID, Similarity: m.Similarity})
		}
	}
	return id, nil
}

// firstApprovedItem returns the first feature item that passed and names
// someone. A declined item still carries whatever the scan guessed before it
// was rejected, which is not a name to attribute money with.
func firstApprovedItem(items []diditIDVerification) *diditIDVerification {
	for i := range items {
		item := &items[i]
		// Didit publishes "Approved" everywhere but "APPROVED" in one example,
		// so the comparison is case-insensitive; an item with no status at all
		// is taken at face value.
		if item.Status != "" && !strings.EqualFold(item.Status, "Approved") {
			continue
		}
		if item.name() != "" {
			return item
		}
	}
	return nil
}

// name is the item's full name, or its first and last names joined — or ""
// when what the scan read is not a name at all (see usableLegalName).
func (item *diditIDVerification) name() string {
	name := item.FullName
	if strings.TrimSpace(name) == "" {
		name = item.FirstName + " " + item.LastName
	}
	name = strings.Join(strings.Fields(name), " ")
	if !usableLegalName(name) {
		return ""
	}
	return name
}

// submitted reports whether the user got as far as handing Didit this
// document: the check reached a verdict, whatever it was.
func (item *diditIDVerification) submitted() bool {
	switch strings.ToLower(strings.TrimSpace(item.Status)) {
	case "approved", "declined", "in review":
		return true
	}
	return false
}

// documentLabelWord matches the words printed on an ID as field labels, and
// the placeholders a scan returns when a field is empty.
var documentLabelWord = regexp.MustCompile(`^(surname\w*|first|middle|last|given|other|names?|none|null|unknown|n/?a)$`)

// usableLegalName refuses a "name" made only of field labels or placeholders.
// Didit's scan of the Nigerian NIN slip reads the printed "Surname" label as
// the name ("Surnamen Surnamen") and every other field as "none"; storing that
// would make every bank transfer the user sends fail the name match.
func usableLegalName(name string) bool {
	words := strings.Fields(strings.ToLower(name))
	for _, w := range words {
		if !documentLabelWord.MatchString(strings.Trim(w, ".,:;-")) {
			return true
		}
	}
	return false
}

// verifiedNameUpdates returns the column updates recording a user's Didit legal
// name, or nil when it cannot be had. Every failure yields nil rather than an
// error on purpose: an approval must not be lost because a second Didit call
// went wrong, and a missing name is repairable by AdminBackfillVerifiedNames
// while a missing approval is not.
func (h *Handlers) verifiedNameUpdates(ctx context.Context, userID uuid.UUID, sessionID string) map[string]interface{} {
	if h.Config == nil || h.Config.DiditAPIKey == "" || sessionID == "" {
		return nil
	}
	name, err := fetchDiditVerifiedName(ctx, h.Config.DiditBaseURL, h.Config.DiditAPIKey, sessionID)
	if err != nil {
		if !errors.Is(err, errDiditNameUnresolved) {
			log.Printf("[KYC] verified-name lookup failed for user %s session %s: %v", userID, sessionID, err)
		}
		return nil
	}
	now := time.Now()
	return map[string]interface{}{
		"verified_legal_name":    name,
		"verified_legal_name_at": now,
	}
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

// The two Didit webhook types this integration acts on.
const (
	diditWebhookStatusUpdated = "status.updated"
	diditWebhookDataUpdated   = "data.updated"
)

// diditStatusResubmitted is the one non-terminal status that needs the user to
// come back and redo a step (Didit also emails them).
const diditStatusResubmitted = "Resubmitted"

// diditStatusInReview is Didit's own reviewer looking at the session.
const diditStatusInReview = "In Review"

// kycInReviewCode is the refusal to start a session while one is in review.
const kycInReviewCode = "kyc_in_review"

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

// declaredName is the name the user says is on their ID: a claim to check,
// never a verified name.
type declaredName struct {
	GivenNames string
	Surname    string
}

func (d declaredName) full() string { return d.GivenNames + " " + d.Surname }

// declaredNamePart allows letters (any script), marks, spaces and the
// punctuation names carry: hyphen, apostrophe, full stop.
var declaredNamePart = regexp.MustCompile(`^[\p{L}\p{M}' .-]{1,80}$`)

// parseDeclaredName validates the name typed before the Didit flow. Both
// parts empty is an app build without the name step (nil, no refusal).
func parseDeclaredName(givenNames, surname string) (*declaredName, *overrideRefusal) {
	givenNames = strings.Join(strings.Fields(givenNames), " ")
	surname = strings.Join(strings.Fields(surname), " ")
	if givenNames == "" && surname == "" {
		return nil, nil
	}
	if givenNames == "" || surname == "" {
		return nil, &overrideRefusal{http.StatusBadRequest, "legal_name_incomplete",
			"Enter both your first name and your surname exactly as they appear on your ID.", nil}
	}
	if !declaredNamePart.MatchString(givenNames) || !declaredNamePart.MatchString(surname) {
		return nil, &overrideRefusal{http.StatusBadRequest, "legal_name_invalid",
			"Use only letters, spaces, hyphens and apostrophes — exactly as your name appears on your ID.", nil}
	}
	d := &declaredName{GivenNames: givenNames, Surname: surname}
	if !usableLegalName(d.full()) {
		return nil, &overrideRefusal{http.StatusBadRequest, "legal_name_invalid",
			"Enter your actual name as printed on your ID, not the field labels.", nil}
	}
	return d, nil
}

// diditSessionError is why a Didit session could not be bought: the status to
// answer with and the operator-facing detail for the log (see failKYCSession).
type diditSessionError struct {
	status int
	detail string
}

// createDiditSession buys one hosted verification session. vendorData is what
// Didit echoes on every webhook for it: the user id for first-time KYC, a
// name-change marker for a re-verification.
//
// expected, when set, is the name the user declared: Didit fuzzy-matches it
// against the document and raises a warning on the decision when they differ.
func createDiditSession(ctx context.Context, baseURL, apiKey, workflowID, callback, vendorData string, expected *declaredName) (*diditSessionCreateResponse, *diditSessionError) {
	payload := map[string]interface{}{
		"workflow_id": workflowID,
		"vendor_data": vendorData,
	}
	if callback != "" {
		payload["callback"] = callback
	}
	if expected != nil {
		payload["expected_details"] = map[string]string{
			"first_name": expected.GivenNames,
			"last_name":  expected.Surname,
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &diditSessionError{http.StatusInternalServerError,
			fmt.Sprintf("failed to marshal session payload: %v", err)}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/session/", bytes.NewReader(body))
	if err != nil {
		return nil, &diditSessionError{http.StatusInternalServerError,
			fmt.Sprintf("failed to build Didit request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)

	resp, err := diditHTTPClient.Do(req)
	if err != nil {
		return nil, &diditSessionError{http.StatusInternalServerError,
			fmt.Sprintf("Didit request failed: %v", err)}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &diditSessionError{http.StatusInternalServerError,
			fmt.Sprintf("failed to read Didit response: %v", err)}
	}

	var diditResp diditSessionCreateResponse
	if err := json.Unmarshal(respBody, &diditResp); err != nil {
		return nil, &diditSessionError{http.StatusInternalServerError,
			fmt.Sprintf("failed to decode Didit response: %v", err)}
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		// diditResp.Detail is Didit's own wording for operators; it is logged
		// with the body rather than forwarded to the user.
		return nil, &diditSessionError{http.StatusBadGateway,
			fmt.Sprintf("Didit API error: status=%d detail=%q body=%s",
				resp.StatusCode, diditResp.Detail, string(respBody))}
	}

	if diditResp.SessionID == "" || diditResp.URL == "" {
		return nil, &diditSessionError{http.StatusBadGateway,
			fmt.Sprintf("Didit response missing session fields: %s", string(respBody))}
	}
	return &diditResp, nil
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

	// The name on the user's ID, typed before the Didit flow. Optional so app
	// builds that predate the name step keep working.
	var req struct {
		GivenNames string `json:"givenNames"`
		Surname    string `json:"surname"`
	}
	_ = c.ShouldBindJSON(&req) // an empty body is an older app
	declared, refusal := parseDeclaredName(req.GivenNames, req.Surname)
	if refusal != nil {
		c.JSON(refusal.status, gin.H{"success": false, "code": refusal.code, "message": refusal.message})
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
	status, url := latestSessionState(user.ID, user.DiditSessionID)
	// A session under review has nothing left for the user to do. A new one
	// is billed again, lands in the same review queue, and Didit's face search
	// flags it as a duplicate of the first — which is how one person ended up
	// with three sessions in review. The reviewer's decision arrives by webhook.
	if status == diditStatusInReview {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"code":    kycInReviewCode,
			"message": "Your verification is being reviewed. There is nothing more to submit — we'll update your status as soon as the review is done.",
		})
		return
	}
	if url != "" && resumableDiditStatuses[status] {
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

	diditResp, serr := createDiditSession(context.Background(), baseURL, apiKey, workflowID, h.Config.DiditCallbackURL, user.ID.String(), declared)
	if serr != nil {
		failKYCSession(c, serr.status, serr.detail)
		return
	}

	sessionUpdates := map[string]interface{}{
		"didit_session_id":    diditResp.SessionID,
		"verification_status": models.VerificationStatusInReview,
	}
	// Recorded with the session it was sent to Didit with, so the claim on
	// the row is always the one Didit compared the document against.
	if declared != nil {
		sessionUpdates["declared_given_names"] = declared.GivenNames
		sessionUpdates["declared_surname"] = declared.Surname
	}
	if err := config.DB.Model(&user).Updates(sessionUpdates).Error; err != nil {
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
		// 82 consecutive rejections in Aug 2026 produced no log line at all,
		// which is how a dead KYC funnel went unnoticed for 16 days. Record
		// enough to separate a secret mismatch from a missing or stale header,
		// without ever logging the payload.
		log.Printf("[KYC] webhook rejected: invalid signature (timestamp=%q raw_sig=%t simple_sig=%t body_bytes=%d) — check DIDIT_WEBHOOK_SECRET matches the sending destination's signing key",
			c.GetHeader("X-Timestamp"), c.GetHeader("X-Signature") != "",
			c.GetHeader("X-Signature-Simple") != "", len(bodyBytes))
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Invalid signature"})
		return
	}

	var payload diditWebhookPayload
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid JSON"})
		return
	}

	// status.updated drives verification_status. data.updated says a reviewer
	// edited the session's data — no decision change, but possibly a corrected
	// legal name, which is what inbound transfers are matched against.
	if payload.WebhookType != diditWebhookStatusUpdated && payload.WebhookType != diditWebhookDataUpdated {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Ignored event type"})
		return
	}

	if payload.SessionID == "" && payload.VendorData == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Missing session data"})
		return
	}

	// A re-verification for a name change is not first-time KYC: its result
	// must never reach the approval path below, which would write the new
	// document's name straight onto a verified account.
	if h.handleNameChangeWebhook(c, payload) {
		return
	}
	// A superseded name-change session is no longer any change's current one,
	// but it is still a re-verification — never first-time KYC's to approve.
	if isChange, err := h.NameChanges.IsNameChangeSession(c.Request.Context(), payload.SessionID); err != nil {
		log.Printf("[KYC] name-change session lookup failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to look up session"})
		return
	} else if isChange {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Ignored event for a past name-change session"})
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
			[]string{models.VerificationActionDiditPassed, models.VerificationActionDiditFailed,
				models.VerificationActionDiditInitiated, models.VerificationActionDiditNameCaptured},
		).First(&existingLog).Error == nil
		if alreadyProcessed {
			c.JSON(http.StatusOK, gin.H{"success": true, "message": "Already processed"})
			return
		}
	}

	if payload.WebhookType == diditWebhookDataUpdated {
		h.handleDiditDataUpdated(c, &user, payload)
		return
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
		// The document name is the only name on this record that was checked
		// against anything, and inbound bank transfers are attributed against
		// it. Folded into the same update so the name and the approval land
		// together; a lookup that fails leaves it for the backfill. A name
		// that came from a later name change is left alone: this session
		// describes the old document (see NameChangeService.NameSetByChange).
		if !h.NameChanges.NameSetByChange(c.Request.Context(), user.ID) {
			for column, value := range h.verifiedNameUpdates(c.Request.Context(), user.ID, sessionForName(payload, &user)) {
				updates[column] = value
			}
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

// sessionForName picks the session whose decision to read: the one the event is
// about, falling back to the user's current session for events that omit it.
func sessionForName(payload diditWebhookPayload, user *models.User) string {
	if payload.SessionID != "" {
		return payload.SessionID
	}
	return user.DiditSessionID
}

// handleDiditDataUpdated re-reads the legal name after a reviewer edits a
// session's data — the one way a stored name can become wrong without any
// status changing. It never touches verification_status: data.updated says the
// report changed, not that the decision did.
func (h *Handlers) handleDiditDataUpdated(c *gin.Context, user *models.User, payload diditWebhookPayload) {
	// A name only means something once the session it came from passed.
	if user.VerificationStatus != models.VerificationStatusVerified {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Ignored data update for unverified user"})
		return
	}
	// Same stale-session rule as the status path: an edit to a superseded
	// session must not overwrite the name the current one produced.
	if payload.SessionID != "" && user.DiditSessionID != "" && payload.SessionID != user.DiditSessionID {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Ignored event from non-current session"})
		return
	}

	// A reviewer's edit of the original session must not undo a name the user
	// has since changed with a newer document.
	if h.NameChanges.NameSetByChange(c.Request.Context(), user.ID) {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "Name kept: set by a later name change"})
		return
	}

	updates := h.verifiedNameUpdates(c.Request.Context(), user.ID, sessionForName(payload, user))
	if updates == nil {
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "No verified name available"})
		return
	}

	if err := config.DB.Model(user).Updates(updates).Error; err != nil {
		log.Printf("[KYC] failed to store verified name for user %s: %v", user.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to persist verified name"})
		return
	}

	metadata, _ := json.Marshal(map[string]string{
		"session_id":   payload.SessionID,
		"event_id":     payload.EventID,
		"webhook_type": payload.WebhookType,
		"source":       diditWebhookDataUpdated,
	})
	if err := config.DB.Create(&models.VerificationLog{
		UserID:   user.ID,
		Action:   models.VerificationActionDiditNameCaptured,
		Status:   models.VerificationLogStatusVerified,
		Notes:    "Didit legal name refreshed after a data update",
		Metadata: datatypes.JSON(metadata),
	}).Error; err != nil {
		log.Printf("[KYC] failed to write verification log for user %s: %v", user.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to record verification event"})
		return
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
			// The user's own document name, shown so they know which bank
			// account a transfer has to come from: payments into the central
			// account are attributed by matching the sender's account name
			// against this exact string.
			"verifiedLegalName": user.VerifiedLegalName,
			// What the user typed before the Didit flow, to pre-fill a retry.
			"declaredGivenNames": user.DeclaredGivenNames,
			"declaredSurname":    user.DeclaredSurname,
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
		Notes     string `json:"notes"`
		LegalName string `json:"legalName"`
	}
	c.ShouldBindJSON(&req)

	name, source, refusal := h.overrideLegalName(c.Request.Context(), &user, req.LegalName)
	if refusal != nil {
		body := gin.H{"success": false, "message": refusal.message, "code": refusal.code}
		if refusal.data != nil {
			body["data"] = refusal.data
		}
		c.JSON(refusal.status, body)
		return
	}

	now := time.Now()
	updates := map[string]interface{}{
		"verification_status": models.VerificationStatusVerified,
		"verification_notes":  req.Notes,
		"updated_at":          now,
	}
	write := config.DB.Model(&models.User{}).Where("id = ?", user.ID)
	if source != legalNameSourceExisting {
		updates["verified_legal_name"] = name
		updates["verified_legal_name_at"] = now
		// Fill the name only if it is still what we read: a webhook or name
		// change that stored one since must not be overwritten. What we read is
		// empty, or a label-only scan that overrideLegalName treats as empty.
		write = write.Where("verified_legal_name IS NULL OR verified_legal_name = '' OR verified_legal_name = ?",
			user.VerifiedLegalName)
	}
	res := write.Updates(updates)
	if res.Error != nil {
		log.Printf("[KYC] override failed to store approval for user %s: %v", user.ID, res.Error)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to approve verification"})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusConflict, gin.H{
			"success": false, "message": "this user's legal name changed while approving — reload and try again"})
		return
	}

	// The name itself never goes in the log, only where it came from.
	metadata, _ := json.Marshal(map[string]string{
		"admin_id":          adminUser.ID.String(),
		"admin_name":        adminUser.Name,
		"reason":            req.Notes,
		"legal_name_source": source,
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

// Where an override's legal name came from, recorded on its verification log.
const (
	legalNameSourceAdmin    = "admin"
	legalNameSourceDidit    = "didit"
	legalNameSourceExisting = "existing"
	// The admin read the name off a document image Didit could not read.
	legalNameSourceAdminFromDocument = "admin_document_unreadable"
	// As above, and what the admin read matches what the user declared.
	legalNameSourceAdminConfirmedDeclared = "admin_confirmed_declared"
)

// overrideRefusal is why an override was not applied, as the admin sees it.
type overrideRefusal struct {
	status        int
	code, message string
	// data goes back with the refusal: the user's declared name, for the
	// console to offer as the admin's starting point.
	data gin.H
}

var noDocumentRefusal = &overrideRefusal{http.StatusConflict, "kyc_no_document",
	"this user never submitted an ID document in Didit — ask them to finish verification in the app first", nil}

// overrideLegalName settles the legal name an admin approval stores, or the
// refusal to show the admin.
//
// An approval without a name is worse than no approval: the money-in gate
// refuses a verified user with no name (requireVerifiedForMoneyIn), so the user
// sees "verified" and is still turned away, with nothing they can do about it.
//
// The name comes from the user's Didit document, so nobody copies it by hand.
// An override of a session with no document at all is refused: there is no
// identity to approve, and the user has to finish verification first — so is a
// user who never opened a session. A typed name is only the fallback for a
// session Didit no longer holds (or no Didit at all) — never while Didit is unreachable, or an outage would skip the
// document check. A stored name is kept, and is never replaced by a different
// one: changing a verified name is the name-change review's job.
//
// A document Didit holds but could not read a name off (the NIN slip) takes
// the admin's typed name, read off the document image in the Didit console. A
// stored name that is only field labels ("Surnamen Surnamen") counts as none.
func (h *Handlers) overrideLegalName(ctx context.Context, user *models.User, typed string) (string, string, *overrideRefusal) {
	typed = strings.Join(strings.Fields(typed), " ")
	if typed != "" && !usableLegalName(typed) {
		return "", "", &overrideRefusal{http.StatusBadRequest, "legal_name_invalid",
			"that is a field label, not a name — enter the person's name exactly as printed on their ID", nil}
	}
	stored := strings.TrimSpace(user.VerifiedLegalName)
	if !usableLegalName(stored) {
		stored = ""
	}

	if stored != "" {
		if typed != "" && !strings.EqualFold(stored, typed) {
			return "", "", &overrideRefusal{http.StatusBadRequest, "legal_name_conflict",
				"this user already has a different verified legal name — change it through a name-change review", nil}
		}
		return stored, legalNameSourceExisting, nil
	}

	diditOn := h.Config != nil && h.Config.DiditAPIKey != ""
	if diditOn && user.DiditSessionID == "" {
		return "", "", noDocumentRefusal
	}
	if diditOn {
		name, err := fetchDiditDocumentName(ctx, h.Config.DiditBaseURL, h.Config.DiditAPIKey, user.DiditSessionID)
		switch {
		case err == nil:
			return name, legalNameSourceDidit, nil
		case errors.Is(err, errDiditNoDocument):
			return "", "", noDocumentRefusal
		case errors.Is(err, errDiditDocumentUnreadable):
			if typed == "" {
				return "", "", &overrideRefusal{http.StatusBadRequest, "legal_name_unreadable",
					"Didit could not read the name on this user's ID — open their session in Didit, read the name off the document image and enter it exactly as printed",
					declaredNameData(user)}
			}
			if declared := declaredFullName(user); declared != "" && strings.EqualFold(typed, declared) {
				return typed, legalNameSourceAdminConfirmedDeclared, nil
			}
			return typed, legalNameSourceAdminFromDocument, nil
		case !errors.Is(err, errDiditNameUnresolved):
			log.Printf("[KYC] override document lookup failed for user %s: %v", user.ID, err)
			return "", "", &overrideRefusal{http.StatusServiceUnavailable, "kyc_provider_unavailable",
				"could not reach the verification provider to read the user's document — try again shortly", nil}
		}
		// Didit no longer holds the session: only a typed name is left.
	}

	if typed != "" {
		return typed, legalNameSourceAdmin, nil
	}
	return "", "", &overrideRefusal{http.StatusBadRequest, "legal_name_required",
		"Didit holds no document for this user — enter their legal name exactly as it appears on their ID",
		declaredNameData(user)}
}

// declaredFullName is the name the user declared before the Didit flow, or "".
func declaredFullName(user *models.User) string {
	if user.DeclaredGivenNames == "" || user.DeclaredSurname == "" {
		return ""
	}
	return declaredName{user.DeclaredGivenNames, user.DeclaredSurname}.full()
}

// declaredNameData offers the declared name to the admin who has to type one —
// as a starting point to check against the document, never as the answer.
func declaredNameData(user *models.User) gin.H {
	if name := declaredFullName(user); name != "" {
		return gin.H{"declaredLegalName": name}
	}
	return nil
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

// One backfill run is bounded so an admin can watch it finish and re-run it,
// rather than firing thousands of provider calls behind a single request.
//
// The ceiling is what a request can actually reach the end of: the lookups are
// sequential and each one can burn the Didit client's 15s timeout, so a cap in
// the hundreds is hours of work behind a response the operator's browser and
// every proxy between gave up on long ago — the counts that say how much is
// left are then never seen by anybody. The endpoint is built to be re-run a
// page at a time, so a low cap costs a second click and nothing else.
const nameBackfillRunLimit = 50

// AdminBackfillVerifiedNames reads the Didit legal name for users who passed
// verification before the name was stored.
//
// An admin-triggered pass rather than a boot-time job: it spends provider calls,
// the population it repairs is finite and shrinking, and an operator watching
// the counts is what tells us how many of the Aug 2026 outage's stranded
// sessions are gone for good.
func (h *Handlers) AdminBackfillVerifiedNames(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}
	if h.Config == nil || h.Config.DiditAPIKey == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false, "message": "Didit is not configured; cannot read verified names"})
		return
	}

	var req struct {
		Limit int `json:"limit"`
	}
	c.ShouldBindJSON(&req)
	limit := req.Limit
	if limit <= 0 || limit > nameBackfillRunLimit {
		limit = nameBackfillRunLimit
	}

	pending := func() *gorm.DB { return pendingLegalNames(config.DB) }

	var remaining int64
	pending().Count(&remaining)

	var users []models.User
	if err := pending().Order("updated_at ASC").Limit(limit).Find(&users).Error; err != nil {
		log.Printf("[KYC] backfill failed to list users: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to list users"})
		return
	}

	result := backfillVerifiedNames(c.Request.Context(), h.Config, users, &admin.ID)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Verified name backfill complete",
		"data": gin.H{
			"scanned":           result.Scanned,
			"updated":           result.Updated,
			"unresolved":        len(result.UnresolvedUserIDs),
			"failed":            result.Failed,
			"remaining":         remaining - int64(result.Updated),
			"unresolvedUserIds": result.UnresolvedUserIDs,
			"noDocumentUserIds": result.NoDocumentUserIDs,
		},
	})
}

// NameBackfillResult counts one pass of the legal-name backfill.
type NameBackfillResult struct {
	Scanned           int
	Updated           int
	Failed            int
	UnresolvedUserIDs []string
	// NoDocumentUserIDs were approved by admin override with no document ever
	// submitted to Didit.
	NoDocumentUserIDs []string
}

// adminOverriddenUsers is the subset of users an admin approved by override.
// A failed read just means nobody is treated as overridden: the stricter read.
func adminOverriddenUsers(users []models.User) map[uuid.UUID]bool {
	ids := make([]uuid.UUID, len(users))
	for i := range users {
		ids[i] = users[i].ID
	}
	var hits []uuid.UUID
	if err := config.DB.Model(&models.VerificationLog{}).
		Where("action = ? AND user_id IN ?", models.VerificationActionAdminOverride, ids).
		Distinct().Pluck("user_id", &hits).Error; err != nil {
		log.Printf("[KYC] backfill could not read override history: %v", err)
	}
	out := make(map[uuid.UUID]bool, len(hits))
	for _, id := range hits {
		out[id] = true
	}
	return out
}

// pendingLegalNames selects the users a backfill repairs: ones the platform
// already treats as verified but holds no legal name for. An unverified user has
// no decision worth reading, and a named one is already done.
func pendingLegalNames(db *gorm.DB) *gorm.DB {
	return db.Model(&models.User{}).Where(
		"verification_status = ? AND (verified_legal_name IS NULL OR verified_legal_name = '')",
		models.VerificationStatusVerified)
}

// BackfillAllVerifiedNames is the one-time server-side run of the backfill
// (`tsa-api -backfill-legal-names`): every pending user in one pass, with no
// request timeout to outlive, so no admin page is needed for it.
func BackfillAllVerifiedNames(ctx context.Context, cfg *config.Config) (NameBackfillResult, error) {
	if cfg == nil || cfg.DiditAPIKey == "" {
		return NameBackfillResult{}, errors.New("Didit is not configured (DIDIT_API_KEY); cannot read verified names")
	}
	var users []models.User
	if err := pendingLegalNames(config.DB).Order("updated_at ASC").Find(&users).Error; err != nil {
		return NameBackfillResult{}, fmt.Errorf("list users: %w", err)
	}
	return backfillVerifiedNames(ctx, cfg, users, nil), nil
}

// backfillVerifiedNames reads and stores the Didit legal name for each user.
// adminID is nil for the server-side run.
func backfillVerifiedNames(ctx context.Context, cfg *config.Config, users []models.User, adminID *uuid.UUID) NameBackfillResult {
	result := NameBackfillResult{Scanned: len(users), UnresolvedUserIDs: make([]string, 0, len(users))}
	overridden := adminOverriddenUsers(users)

	for i := range users {
		user := users[i]
		// An admin who overrode the session approved its document, so the
		// reading of an unpassed check is theirs to vouch for; anyone else's
		// name must come from a check Didit passed.
		read := fetchDiditVerifiedName
		if overridden[user.ID] {
			read = fetchDiditDocumentName
		}
		name, err := read(ctx, cfg.DiditBaseURL, cfg.DiditAPIKey, user.DiditSessionID)
		switch {
		case errors.Is(err, errDiditNoDocument):
			// Approved by override without ever submitting a document: there
			// is no name anywhere, and the user has to verify for real.
			result.NoDocumentUserIDs = append(result.NoDocumentUserIDs, user.ID.String())
			continue
		case errors.Is(err, errDiditNameUnresolved), errors.Is(err, errDiditDocumentUnreadable):
			// Sessions Didit no longer holds, documents it read no name off
			// (an admin override with a typed name repairs those), and users
			// verified by admin override who never had one. Reported so they
			// can be chased by hand — a run must not stop on them.
			result.UnresolvedUserIDs = append(result.UnresolvedUserIDs, user.ID.String())
			continue
		case err != nil:
			result.Failed++
			log.Printf("[KYC] backfill lookup failed for user %s: %v", user.ID, err)
			continue
		}

		now := time.Now()
		if err := config.DB.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
			"verified_legal_name":    name,
			"verified_legal_name_at": now,
		}).Error; err != nil {
			result.Failed++
			log.Printf("[KYC] backfill failed to store name for user %s: %v", user.ID, err)
			continue
		}
		result.Updated++

		meta := map[string]string{"session_id": user.DiditSessionID, "source": "backfill"}
		if adminID != nil {
			meta["admin_id"] = adminID.String()
		} else {
			meta["source"] = "backfill-cli"
		}
		metadata, _ := json.Marshal(meta)
		if err := config.DB.Create(&models.VerificationLog{
			UserID:   user.ID,
			Action:   models.VerificationActionDiditNameCaptured,
			Status:   models.VerificationLogStatusVerified,
			Notes:    "Didit legal name backfilled",
			Metadata: datatypes.JSON(metadata),
			AdminID:  adminID,
		}).Error; err != nil {
			log.Printf("[KYC] backfill failed to write verification log for user %s: %v", user.ID, err)
		}
	}
	return result
}

// UserWithLog is one row of the admin verification queue.
type UserWithLog struct {
	models.User
	// VerifiedLegalName is `json:"-"` on the model so it cannot leak through
	// any incidental marshal. The admin queue opts in by name: it is the exact
	// string a held payment's sender name is judged against, so an admin
	// adjudicating one has to be able to see it.
	VerifiedLegalName string    `json:"verifiedLegalName,omitempty"`
	LastAction        string    `json:"lastAction"`
	LastActionAt      time.Time `json:"lastActionAt"`
}

// MarshalJSON merges the row's own fields into the embedded user's payload.
// Without it the User's promoted MarshalJSON wins outright and every field
// declared here is silently dropped from the response.
func (u UserWithLog) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(u.User)
	if err != nil {
		return nil, err
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal(base, &row); err != nil {
		return nil, err
	}
	extra := map[string]interface{}{
		"lastAction":   u.LastAction,
		"lastActionAt": u.LastActionAt,
		// False for no name and for a label-only scan ("Surnamen Surnamen"):
		// either way the user is refused at pay-in until an admin repairs it.
		"legalNameUsable": usableLegalName(u.VerifiedLegalName),
	}
	if u.VerifiedLegalName != "" {
		extra["verifiedLegalName"] = u.VerifiedLegalName
	}
	for key, value := range extra {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		row[key] = raw
	}
	return json.Marshal(row)
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
	query.Select("id, name, email, phone_number, verification_status, verification_notes, didit_session_id, verified_legal_name, created_at, updated_at, profile_photo").
		Order("updated_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&users)

	results := make([]UserWithLog, len(users))
	for i, u := range users {
		results[i] = UserWithLog{User: u, VerifiedLegalName: u.VerifiedLegalName}
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
