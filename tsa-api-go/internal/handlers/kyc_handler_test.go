package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ---------- verifyDiditWebhook (pure) ----------

func freshWebhookBody(status string) []byte {
	return []byte(fmt.Sprintf(
		`{"event_id":"e1","webhook_type":"status.updated","timestamp":%d,"session_id":"s1","status":"%s","vendor_data":"u1"}`,
		time.Now().Unix(), status))
}

func signRaw(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyDiditWebhookRawSignature(t *testing.T) {
	secret := "test-webhook-secret"
	body := freshWebhookBody("Approved")
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	if !verifyDiditWebhook(body, ts, signRaw(body, secret), "", secret) {
		t.Fatal("expected raw X-Signature to verify")
	}
	if verifyDiditWebhook(body, ts, "deadbeef", "", secret) {
		t.Fatal("expected bad raw signature to fail")
	}
}

func TestVerifyDiditWebhookSimpleSignature(t *testing.T) {
	secret := "test-webhook-secret"
	payload := diditWebhookPayload{
		EventID:     "e1",
		WebhookType: "status.updated",
		Timestamp:   time.Now().Unix(),
		SessionID:   "s1",
		Status:      "Approved",
		VendorData:  "u1",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	canonical := fmt.Sprintf("%d:%s:%s:%s",
		payload.Timestamp, payload.SessionID, payload.Status, payload.WebhookType)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonical))
	sig := hex.EncodeToString(mac.Sum(nil))

	if !verifyDiditWebhook(body, ts, "", sig, secret) {
		t.Fatal("expected X-Signature-Simple to verify")
	}
}

func TestVerifyDiditWebhookRejectsStaleTimestampHeader(t *testing.T) {
	secret := "test-webhook-secret"
	body := freshWebhookBody("Approved")
	stale := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)

	if verifyDiditWebhook(body, stale, signRaw(body, secret), "", secret) {
		t.Fatal("expected stale timestamp header to fail")
	}
}

// A replayed body carries its original (old) payload timestamp. A fresh
// X-Timestamp header must not resurrect it: freshness has to be checked
// against the signed payload, not just the unauthenticated header.
func TestVerifyDiditWebhookRejectsStalePayloadTimestamp(t *testing.T) {
	secret := "test-webhook-secret"
	old := time.Now().Add(-1 * time.Hour).Unix()
	body := []byte(fmt.Sprintf(
		`{"event_id":"e1","webhook_type":"status.updated","timestamp":%d,"session_id":"s1","status":"Approved","vendor_data":"u1"}`, old))
	freshHeader := strconv.FormatInt(time.Now().Unix(), 10)

	if verifyDiditWebhook(body, freshHeader, signRaw(body, secret), "", secret) {
		t.Fatal("expected replayed payload timestamp to fail despite fresh header")
	}
}

// ---------- KYCWebhook (handler) ----------

func setupKYCTestDB(t *testing.T, withLogsTable bool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}

	sqls := []string{
		`CREATE TABLE users (
			id TEXT PRIMARY KEY,
			name TEXT, username TEXT, email TEXT, password TEXT, role TEXT,
			phone_number TEXT, country TEXT, state TEXT, city TEXT, address TEXT,
			profile_photo TEXT, referral_code TEXT, referred_by TEXT, didit_session_id TEXT,
			verification_status TEXT DEFAULT 'pending', verification_notes TEXT,
			account_status TEXT DEFAULT 'active', last_login DATETIME,
			login_attempts INTEGER DEFAULT 0, lock_until DATETIME,
			wallet_address TEXT, seed_phrase_backed_up INTEGER DEFAULT 0,
			mute_notifications INTEGER DEFAULT 0, mute_email INTEGER DEFAULT 0,
			tp_balance REAL DEFAULT 0, cashback_balance REAL DEFAULT 0,
			last_settled_global_index INTEGER, email_verified INTEGER DEFAULT 0,
			deleted_at DATETIME, created_at DATETIME, updated_at DATETIME,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME
		)`,
	}
	if withLogsTable {
		sqls = append(sqls, `CREATE TABLE verification_logs (
			id TEXT PRIMARY KEY, user_id TEXT, action TEXT, status TEXT,
			notes TEXT, metadata TEXT, admin_id TEXT,
			created_at DATETIME, updated_at DATETIME
		)`)
	}
	for _, s := range sqls {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("failed to create table: %v", err)
		}
	}

	config.DB = db
	return db
}

func createKYCTestUser(t *testing.T, db *gorm.DB, status string) *models.User {
	t.Helper()
	user := &models.User{
		ID:                 uuid.New(),
		Name:               "KYC User",
		Username:           "kyc_" + uuid.New().String()[:8],
		Email:              "kyc_" + uuid.New().String()[:8] + "@example.com",
		Password:           "x",
		VerificationStatus: status,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	return user
}

func postKYCWebhook(h *Handlers, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("POST", "/api/kyc/webhook", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		c.Request.Header.Set(k, v)
	}
	h.KYCWebhook(c)
	return w
}

func webhookBodyFor(user *models.User, status string) []byte {
	return webhookBodyForSession(user, status, "sess-1")
}

func webhookBodyForSession(user *models.User, status, sessionID string) []byte {
	return []byte(fmt.Sprintf(
		`{"event_id":"","webhook_type":"status.updated","timestamp":%d,"session_id":"%s","status":"%s","vendor_data":"%s"}`,
		time.Now().Unix(), sessionID, status, user.ID.String()))
}

func signedHeaders(body []byte, secret string) map[string]string {
	return map[string]string{
		"X-Timestamp": strconv.FormatInt(time.Now().Unix(), 10),
		"X-Signature": signRaw(body, secret),
	}
}

func reloadUser(t *testing.T, db *gorm.DB, id uuid.UUID) models.User {
	t.Helper()
	var u models.User
	if err := db.First(&u, "id = ?", id).Error; err != nil {
		t.Fatalf("failed to reload user: %v", err)
	}
	return u
}

func TestKYCWebhookFailsClosedWhenSecretMissing(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusInReview)
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditWebhookSecret: ""}}

	w := postKYCWebhook(h, webhookBodyFor(user, "Approved"), nil)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, user.ID).VerificationStatus; got != models.VerificationStatusInReview {
		t.Fatalf("user status changed to %q despite rejected webhook", got)
	}
}

func TestKYCWebhookDormantModeDoesNotProcess(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, "pending")
	h := &Handlers{Config: &config.Config{DiditAPIKey: "", DiditWebhookSecret: ""}}

	w := postKYCWebhook(h, webhookBodyFor(user, "Approved"), nil)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 ack, got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, user.ID).VerificationStatus; got != "pending" {
		t.Fatalf("dormant webhook must not process events; user status = %q", got)
	}
}

func TestKYCWebhookApprovesUser(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusInReview)
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditWebhookSecret: secret}}

	body := webhookBodyFor(user, "Approved")
	w := postKYCWebhook(h, body, signedHeaders(body, secret))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got := reloadUser(t, db, user.ID)
	if got.VerificationStatus != models.VerificationStatusVerified {
		t.Fatalf("expected verified, got %q", got.VerificationStatus)
	}
	if got.DiditSessionID != "sess-1" {
		t.Fatalf("expected didit_session_id sess-1, got %q", got.DiditSessionID)
	}
}

func TestKYCWebhookDoesNotDowngradeVerifiedUser(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusVerified)
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditWebhookSecret: secret}}

	body := webhookBodyFor(user, "In Review")
	w := postKYCWebhook(h, body, signedHeaders(body, secret))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, user.ID).VerificationStatus; got != models.VerificationStatusVerified {
		t.Fatalf("verified user downgraded to %q by late non-terminal event", got)
	}
}

func TestCreateKYCSessionReturns500WhenDBWriteFails(t *testing.T) {
	// Didit API succeeds but the local DB write fails: the client must NOT
	// get a session URL for a session we failed to record.
	didit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"session_id":"sess-9","session_token":"tok","url":"https://verify.didit.me/x","status":"Not Started","vendor_data":"u1"}`)
	}))
	defer didit.Close()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	config.DB = db // no users table → the session-id update fails

	h := &Handlers{Config: &config.Config{
		DiditAPIKey: "key", DiditWorkflowID: "wf", DiditBaseURL: didit.URL,
	}}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", &models.User{ID: uuid.New(), VerificationStatus: "pending"})
	c.Request, _ = http.NewRequest("POST", "/api/kyc/session", nil)

	h.CreateKYCSession(c)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when session cannot be persisted, got %d: %s", w.Code, w.Body.String())
	}
}

// A user can abandon one session and verify through a newer one; the stale
// session's terminal event (Expired fires days later) must not revoke the
// verification earned by the current session.
func TestKYCWebhookIgnoresStaleSessionTerminalEvent(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusVerified)
	db.Model(user).Update("didit_session_id", "sess-current")
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditWebhookSecret: secret}}

	body := webhookBodyForSession(user, "Abandoned", "sess-old")
	w := postKYCWebhook(h, body, signedHeaders(body, secret))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got := reloadUser(t, db, user.ID)
	if got.VerificationStatus != models.VerificationStatusVerified {
		t.Fatalf("stale session event revoked verification: status = %q", got.VerificationStatus)
	}
	if got.DiditSessionID != "sess-current" {
		t.Fatalf("stale session overwrote current session id: %q", got.DiditSessionID)
	}
}

func TestKYCWebhookIgnoresStaleSessionForInReviewUser(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusInReview)
	db.Model(user).Update("didit_session_id", "sess-current")
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditWebhookSecret: secret}}

	body := webhookBodyForSession(user, "Declined", "sess-old")
	w := postKYCWebhook(h, body, signedHeaders(body, secret))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, user.ID).VerificationStatus; got != models.VerificationStatusInReview {
		t.Fatalf("stale session event changed status to %q", got)
	}
}

// Approval is monotonic: a delayed Approved from a superseded session still
// verifies the user (they completed that flow; a newer session doesn't undo it).
func TestKYCWebhookApprovesFromSupersededSession(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusInReview)
	db.Model(user).Update("didit_session_id", "sess-current")
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditWebhookSecret: secret}}

	body := webhookBodyForSession(user, "Approved", "sess-old")
	w := postKYCWebhook(h, body, signedHeaders(body, secret))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, user.ID).VerificationStatus; got != models.VerificationStatusVerified {
		t.Fatalf("expected verified via superseded session approval, got %q", got)
	}
}

func TestKYCWebhookReturns500WhenLogWriteFails(t *testing.T) {
	db := setupKYCTestDB(t, false) // no verification_logs table → log insert fails
	user := createKYCTestUser(t, db, models.VerificationStatusInReview)
	secret := "hook-secret"
	h := &Handlers{Config: &config.Config{DiditAPIKey: "key", DiditWebhookSecret: secret}}

	body := webhookBodyFor(user, "Approved")
	w := postKYCWebhook(h, body, signedHeaders(body, secret))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 so Didit retries, got %d: %s", w.Code, w.Body.String())
	}
}

// logWithMetadata inserts a verification log carrying the given metadata keys,
// mirroring what CreateKYCSession and KYCWebhook write.
func logWithMetadata(t *testing.T, db *gorm.DB, userID uuid.UUID, at time.Time, meta map[string]string) {
	t.Helper()
	raw, _ := json.Marshal(meta)
	if err := db.Create(&models.VerificationLog{
		UserID:    userID,
		Action:    models.VerificationActionDiditInitiated,
		Status:    models.VerificationLogStatusInReview,
		Metadata:  datatypes.JSON(raw),
		CreatedAt: at,
		UpdatedAt: at,
	}).Error; err != nil {
		t.Fatalf("failed to write verification log: %v", err)
	}
}

func createKYCSessionFor(h *Handlers, user *models.User) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)
	c.Request, _ = http.NewRequest("POST", "/api/kyc/session", nil)
	h.CreateKYCSession(c)
	return w
}

// Pressing "continue" mid-session must reuse the hosted session, not mint a new
// one: a replacement abandons any resubmission Didit asked for and is billed again.
func TestCreateKYCSessionResumesInFlightSession(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusInReview)
	user.DiditSessionID = "sess-live"
	db.Save(user)

	logWithMetadata(t, db, user.ID, time.Now().Add(-time.Hour), map[string]string{
		"session_id": "sess-live", "session_url": "https://verify.didit.me/live",
	})
	logWithMetadata(t, db, user.ID, time.Now(), map[string]string{
		"session_id": "sess-live", "status": "Resubmitted",
	})

	didit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Didit was called; the in-flight session should have been resumed")
	}))
	defer didit.Close()

	h := &Handlers{Config: &config.Config{
		DiditAPIKey: "key", DiditWorkflowID: "wf", DiditBaseURL: didit.URL,
	}}
	w := createKYCSessionFor(h, user)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			SessionURL string `json:"sessionUrl"`
			Resumed    bool   `json:"resumed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response: %v", err)
	}
	if resp.Data.SessionURL != "https://verify.didit.me/live" || !resp.Data.Resumed {
		t.Fatalf("expected the stored session URL to be resumed, got %+v", resp.Data)
	}
}

// "In Review" means Didit is deciding — there is nothing to resume, so a user
// asking to verify again gets a fresh session.
func TestCreateKYCSessionStartsFreshWhenNotResumable(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusInReview)
	user.DiditSessionID = "sess-old"
	db.Save(user)

	logWithMetadata(t, db, user.ID, time.Now().Add(-time.Hour), map[string]string{
		"session_id": "sess-old", "session_url": "https://verify.didit.me/old",
	})
	logWithMetadata(t, db, user.ID, time.Now(), map[string]string{
		"session_id": "sess-old", "status": "In Review",
	})

	didit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"session_id":"sess-new","url":"https://verify.didit.me/new","status":"Not Started"}`)
	}))
	defer didit.Close()

	h := &Handlers{Config: &config.Config{
		DiditAPIKey: "key", DiditWorkflowID: "wf", DiditBaseURL: didit.URL,
	}}
	w := createKYCSessionFor(h, user)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadUser(t, db, user.ID).DiditSessionID; got != "sess-new" {
		t.Fatalf("expected a new session to be persisted, got %q", got)
	}
}

// in_review covers both "we are reviewing" and "we need something from you";
// the client can only tell them apart if the raw Didit status is surfaced.
func TestGetKYCStatusSurfacesResubmitted(t *testing.T) {
	db := setupKYCTestDB(t, true)
	user := createKYCTestUser(t, db, models.VerificationStatusInReview)
	user.DiditSessionID = "sess-live"
	db.Save(user)

	logWithMetadata(t, db, user.ID, time.Now().Add(-time.Hour), map[string]string{
		"session_id": "sess-live", "session_url": "https://verify.didit.me/live",
	})
	logWithMetadata(t, db, user.ID, time.Now(), map[string]string{
		"session_id": "sess-live", "status": "Resubmitted",
	})

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)
	c.Request, _ = http.NewRequest("GET", "/api/kyc/status", nil)

	(&Handlers{Config: &config.Config{}}).GetKYCStatus(c)

	var resp struct {
		Data struct {
			DiditStatus    string `json:"diditStatus"`
			SessionURL     string `json:"sessionUrl"`
			ActionRequired bool   `json:"actionRequired"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response: %v", err)
	}
	if resp.Data.DiditStatus != "Resubmitted" || !resp.Data.ActionRequired {
		t.Fatalf("expected Resubmitted to be surfaced as action-required, got %+v", resp.Data)
	}
	if resp.Data.SessionURL != "https://verify.didit.me/live" {
		t.Fatalf("expected the resumable session URL, got %q", resp.Data.SessionURL)
	}
}

// Every server-side failure in the session flow is an operator problem, and the
// app puts response.message straight into an alert — so the user gets one
// generic line and the cause goes to the log, not to them.
func TestCreateKYCSessionHidesOperatorDetailFromUser(t *testing.T) {
	cases := []struct {
		name   string
		config *config.Config
		didit  func(w http.ResponseWriter, r *http.Request)
		want   int
	}{
		{
			name:   "unconfigured",
			config: &config.Config{},
			want:   http.StatusServiceUnavailable,
		},
		{
			name:   "didit rejects the request",
			config: &config.Config{DiditAPIKey: "key", DiditWorkflowID: "wf"},
			didit: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"detail":"Invalid API key for application 123"}`)
			},
			want: http.StatusBadGateway,
		},
		{
			name:   "didit returns an incomplete session",
			config: &config.Config{DiditAPIKey: "key", DiditWorkflowID: "wf"},
			didit: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, `{"session_id":"","url":""}`)
			},
			want: http.StatusBadGateway,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupKYCTestDB(t, true)
			user := createKYCTestUser(t, db, models.VerificationStatusPending)

			cfg := *tc.config
			if tc.didit != nil {
				srv := httptest.NewServer(http.HandlerFunc(tc.didit))
				defer srv.Close()
				cfg.DiditBaseURL = srv.URL
			}

			w := createKYCSessionFor(&Handlers{Config: &cfg}, user)

			if w.Code != tc.want {
				t.Fatalf("expected %d, got %d: %s", tc.want, w.Code, w.Body.String())
			}
			var resp struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("bad response: %v", err)
			}
			if resp.Message != kycUnavailableMessage {
				t.Fatalf("user-facing message leaked detail: %q", resp.Message)
			}
		})
	}
}
