package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupMerchantTestDB creates an in-memory SQLite database with the tables
// needed by MerchantRequestHandler tests.
func setupMerchantTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	sqls := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			username TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL UNIQUE,
			password TEXT NOT NULL,
			role TEXT DEFAULT 'user',
			phone_number TEXT,
			country TEXT, state TEXT, city TEXT, address TEXT,
			profile_photo TEXT, referral_code TEXT, referred_by TEXT,
			didit_session_id TEXT,
			verification_status TEXT DEFAULT 'pending',
			verification_notes TEXT, account_status TEXT DEFAULT 'active',
			last_login DATETIME, login_attempts INTEGER DEFAULT 0,
			lock_until DATETIME, wallet_address TEXT,
			seed_phrase_backed_up INTEGER DEFAULT 0,
			mute_notifications INTEGER DEFAULT 0,
			mute_email INTEGER DEFAULT 0,
			tp_balance REAL DEFAULT 0,
			cashback_balance REAL DEFAULT 0,
			last_settled_global_index INTEGER,
			email_verified INTEGER DEFAULT 0,
			deleted_at DATETIME, submitted_for_verification_at DATETIME,
			created_at DATETIME, updated_at DATETIME,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS merchant_requests (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			business_type TEXT NOT NULL,
			business_name TEXT NOT NULL,
			business_description TEXT,
			address TEXT NOT NULL,
			city TEXT NOT NULL,
			state TEXT NOT NULL,
			country TEXT NOT NULL,
			phone TEXT NOT NULL,
			registration_number TEXT,
			website TEXT,
			status TEXT NOT NULL DEFAULT 'pending',
			admin_note TEXT,
			reviewed_by TEXT,
			reviewed_at DATETIME,
			settlement_bank_code TEXT,
			settlement_account_number TEXT,
			settlement_account_name TEXT,
			maker_wallet_address TEXT,
			requested_per_trade_cap_usd REAL DEFAULT 0,
			requested_daily_cap_usd REAL DEFAULT 0,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS payment_merchants (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			kind TEXT NOT NULL DEFAULT 'merchant', pool_chain_id INTEGER,
			nomba_account_id TEXT,
			nomba_wallet_id TEXT,
			nomba_creds_ref TEXT,
			inbound_account_number TEXT,
			inbound_bank TEXT,
			nomba_account_ref TEXT,
			operator_wallet_address TEXT,
			nomba_sub_account_id TEXT,
			settlement_bank_code TEXT,
			settlement_account_number TEXT,
			settlement_account_name TEXT,
			available_ngn TEXT NOT NULL DEFAULT '0',
			reserved_ngn TEXT NOT NULL DEFAULT '0',
			provisioning_status TEXT NOT NULL DEFAULT 'pending',
			per_trade_cap_usd REAL NOT NULL DEFAULT 0,
			daily_cap_usd REAL NOT NULL DEFAULT 0,
			reviewed_by TEXT,
			reviewed_at DATETIME,
			review_note TEXT,
			created_at DATETIME,
			updated_at DATETIME,
			UNIQUE (user_id, kind)
		)`,
	}
	for _, s := range sqls {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("failed to create table: %v", err)
		}
	}

	if err := db.AutoMigrate(&models.PayMerchant{}); err != nil {
		t.Fatalf("failed to migrate pay_merchants: %v", err)
	}

	// Register a callback so that MerchantRequest gets a UUID before insert
	// (SQLite does not support gen_random_uuid()).
	db.Callback().Create().Before("gorm:create").Register("set_merchant_request_uuid", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "merchant_requests" {
			val := tx.Statement.ReflectValue
			if val.IsValid() && val.CanAddr() {
				idField := val.FieldByName("ID")
				if idField.IsValid() && idField.Interface() == uuid.Nil {
					idField.Set(reflect.ValueOf(uuid.New()))
				}
			}
		}
	})

	config.DB = db
	return db
}

func createMerchantTestUser(t *testing.T, db *gorm.DB, role string) *models.User {
	t.Helper()
	user := &models.User{
		ID:       uuid.New(),
		Name:     "Test User",
		Username: "user_" + uuid.New().String()[:8],
		Email:    "email_" + uuid.New().String()[:8] + "@example.com",
		Password: "$2a$10$fakehash",
		Role:     role,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	return user
}

// ginTestMode sets gin's global mode once: the race tests build contexts from
// several goroutines, and gin.SetMode is an unguarded global write.
var ginTestMode sync.Once

func newMerchantContext(method, path string, body interface{}, user *models.User) (*httptest.ResponseRecorder, *gin.Context) {
	ginTestMode.Do(func() { gin.SetMode(gin.TestMode) })
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var reqBody []byte
	if body != nil {
		reqBody, _ = json.Marshal(body)
	}
	c.Request, _ = http.NewRequest(method, path, bytes.NewBuffer(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	if user != nil {
		c.Set("user", user)
	}
	return w, c
}

func validMerchantInput() map[string]interface{} {
	return map[string]interface{}{
		"businessType":        "general_products",
		"businessName":        "Test Shop",
		"businessDescription": "A test shop",
		"address":             "123 Main St",
		"city":                "Lagos",
		"state":               "Lagos",
		"country":             "Nigeria",
		"phone":               "+2341234567890",
	}
}

// validPMSubmitInput returns a complete, valid p2p_merchant submit body
// (settlement bank/account/name + a valid non-zero maker wallet + caps).
func validPMSubmitInput() map[string]interface{} {
	return map[string]interface{}{
		"businessType":            "p2p_merchant",
		"businessName":            "Retry Corp",
		"businessDescription":     "A PM retry test",
		"address":                 "1 Retry St",
		"city":                    "Lagos",
		"state":                   "Lagos",
		"country":                 "Nigeria",
		"phone":                   "+2341234567890",
		"settlementBankCode":      "058",
		"settlementAccountNumber": "1234567890",
		"settlementAccountName":   "Retry Corp Ltd",
		"makerWalletAddress":      "0x52908400098527886E0F7030069857D2E4169EE7",
		"requestedPerTradeCapUsd": 500,
		"requestedDailyCapUsd":    10000,
	}
}

// ---------- Unauthenticated ----------

func TestSubmitMerchantRequest_Unauthenticated(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}

	w, c := newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), nil)
	h.SubmitMerchantRequest(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected %d, got %d: %s", http.StatusUnauthorized, w.Code, w.Body.String())
	}
}

func TestApproveMerchantRequest_Unauthenticated(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}

	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/fake/approve", nil, nil)
	h.ApproveMerchantRequest(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected %d, got %d: %s", http.StatusUnauthorized, w.Code, w.Body.String())
	}
}

func TestRejectMerchantRequest_Unauthenticated(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}

	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/fake/reject", nil, nil)
	h.RejectMerchantRequest(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected %d, got %d: %s", http.StatusUnauthorized, w.Code, w.Body.String())
	}
}

// ---------- Invalid UUID / Not Found ----------

func TestApproveMerchantRequest_InvalidUUID(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	admin := createMerchantTestUser(t, db, "admin")

	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/not-a-uuid/approve", nil, admin)
	c.Params = gin.Params{{Key: "id", Value: "not-a-uuid"}}
	h.ApproveMerchantRequest(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestRejectMerchantRequest_InvalidUUID(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	admin := createMerchantTestUser(t, db, "admin")

	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/not-a-uuid/reject",
		map[string]interface{}{"note": "test"}, admin)
	c.Params = gin.Params{{Key: "id", Value: "not-a-uuid"}}
	h.RejectMerchantRequest(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestApproveMerchantRequest_NotFound(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	admin := createMerchantTestUser(t, db, "admin")

	fakeID := uuid.New().String()
	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/"+fakeID+"/approve", nil, admin)
	c.Params = gin.Params{{Key: "id", Value: fakeID}}
	h.ApproveMerchantRequest(c)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected %d, got %d: %s", http.StatusNotFound, w.Code, w.Body.String())
	}
}

func TestRejectMerchantRequest_NotFound(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	admin := createMerchantTestUser(t, db, "admin")

	fakeID := uuid.New().String()
	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/"+fakeID+"/reject",
		map[string]interface{}{"note": "test"}, admin)
	c.Params = gin.Params{{Key: "id", Value: fakeID}}
	h.RejectMerchantRequest(c)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected %d, got %d: %s", http.StatusNotFound, w.Code, w.Body.String())
	}
}

// ---------- Submit Merchant Request ----------

func TestSubmitMerchantRequest_Success(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")

	w, c := newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), user)
	h.SubmitMerchantRequest(c)

	if w.Code != http.StatusCreated {
		t.Errorf("expected %d, got %d: %s", http.StatusCreated, w.Code, w.Body.String())
	}
}

func TestSubmitMerchantRequest_AlreadyMerchant(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "merchant")

	w, c := newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), user)
	h.SubmitMerchantRequest(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestSubmitMerchantRequest_DuplicatePending(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")

	// First submit
	w1, c1 := newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), user)
	h.SubmitMerchantRequest(c1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first submit failed: %d: %s", w1.Code, w1.Body.String())
	}

	// Second submit should fail
	w2, c2 := newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), user)
	h.SubmitMerchantRequest(c2)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w2.Code, w2.Body.String())
	}
}

func TestSubmitMerchantRequest_InvalidBusinessType(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")

	input := validMerchantInput()
	input["businessType"] = "invalid_type"

	w, c := newMerchantContext("POST", "/api/merchant-requests", input, user)
	h.SubmitMerchantRequest(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

// ---------- Get My Merchant Request ----------

func TestGetMyMerchantRequest_Found(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")

	// Create a request first
	w1, c1 := newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), user)
	h.SubmitMerchantRequest(c1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("submit failed: %d: %s", w1.Code, w1.Body.String())
	}

	// Fetch it
	w2, c2 := newMerchantContext("GET", "/api/merchant-requests/my-request", nil, user)
	h.GetMyMerchantRequest(c2)

	if w2.Code != http.StatusOK {
		t.Errorf("expected %d, got %d: %s", http.StatusOK, w2.Code, w2.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["data"] == nil {
		t.Error("expected data to be non-nil")
	}
}

func TestGetMyMerchantRequest_NotFound(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")

	w, c := newMerchantContext("GET", "/api/merchant-requests/my-request", nil, user)
	h.GetMyMerchantRequest(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["data"] != nil {
		t.Errorf("expected data to be nil, got %v", resp["data"])
	}
}

// ---------- Approve Merchant Request ----------

func TestApproveMerchantRequest_Success(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")
	admin := createMerchantTestUser(t, db, "admin")

	// Submit a request
	w1, c1 := newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), user)
	h.SubmitMerchantRequest(c1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("submit failed: %d: %s", w1.Code, w1.Body.String())
	}

	// Extract the request ID
	var submitResp map[string]interface{}
	json.Unmarshal(w1.Body.Bytes(), &submitResp)
	data := submitResp["data"].(map[string]interface{})
	reqID := data["id"].(string)

	// Approve
	w2, c2 := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/approve",
		map[string]interface{}{"note": "Approved"}, admin)
	c2.Params = gin.Params{{Key: "id", Value: reqID}}
	h.ApproveMerchantRequest(c2)

	if w2.Code != http.StatusOK {
		t.Errorf("expected %d, got %d: %s", http.StatusOK, w2.Code, w2.Body.String())
	}

	// Verify user role changed to merchant
	var updatedUser models.User
	if err := db.First(&updatedUser, "id = ?", user.ID).Error; err != nil {
		t.Fatalf("failed to reload user: %v", err)
	}
	if updatedUser.Role != models.RoleMerchant {
		t.Errorf("expected role %q, got %q", models.RoleMerchant, updatedUser.Role)
	}
}

func TestApproveMerchantRequest_NotPending(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")
	admin := createMerchantTestUser(t, db, "admin")

	// Submit and reject first
	w1, c1 := newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), user)
	h.SubmitMerchantRequest(c1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("submit failed: %d: %s", w1.Code, w1.Body.String())
	}

	var submitResp map[string]interface{}
	json.Unmarshal(w1.Body.Bytes(), &submitResp)
	data := submitResp["data"].(map[string]interface{})
	reqID := data["id"].(string)

	// Reject it
	wR, cR := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/reject",
		map[string]interface{}{"note": "Not qualified"}, admin)
	cR.Params = gin.Params{{Key: "id", Value: reqID}}
	h.RejectMerchantRequest(cR)
	if wR.Code != http.StatusOK {
		t.Fatalf("reject failed: %d: %s", wR.Code, wR.Body.String())
	}

	// Now try to approve the rejected request
	w2, c2 := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/approve",
		map[string]interface{}{"note": "Trying to approve"}, admin)
	c2.Params = gin.Params{{Key: "id", Value: reqID}}
	h.ApproveMerchantRequest(c2)

	if w2.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w2.Code, w2.Body.String())
	}
}

// ---------- Reject Merchant Request ----------

func TestRejectMerchantRequest_Success(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")
	admin := createMerchantTestUser(t, db, "admin")

	// Submit
	w1, c1 := newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), user)
	h.SubmitMerchantRequest(c1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("submit failed: %d: %s", w1.Code, w1.Body.String())
	}

	var submitResp map[string]interface{}
	json.Unmarshal(w1.Body.Bytes(), &submitResp)
	data := submitResp["data"].(map[string]interface{})
	reqID := data["id"].(string)

	// Reject with note
	w2, c2 := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/reject",
		map[string]interface{}{"note": "Missing documents"}, admin)
	c2.Params = gin.Params{{Key: "id", Value: reqID}}
	h.RejectMerchantRequest(c2)

	if w2.Code != http.StatusOK {
		t.Errorf("expected %d, got %d: %s", http.StatusOK, w2.Code, w2.Body.String())
	}

	// Verify user role is still "user" (rejection must NOT upgrade role)
	var updatedUser models.User
	if err := db.First(&updatedUser, "id = ?", user.ID).Error; err != nil {
		t.Fatalf("failed to reload user: %v", err)
	}
	if updatedUser.Role != "user" {
		t.Errorf("expected role %q after rejection, got %q", "user", updatedUser.Role)
	}
}

func TestRejectMerchantRequest_NoteRequired(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")
	admin := createMerchantTestUser(t, db, "admin")

	// Submit
	w1, c1 := newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), user)
	h.SubmitMerchantRequest(c1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("submit failed: %d: %s", w1.Code, w1.Body.String())
	}

	var submitResp map[string]interface{}
	json.Unmarshal(w1.Body.Bytes(), &submitResp)
	data := submitResp["data"].(map[string]interface{})
	reqID := data["id"].(string)

	// Reject without note
	w2, c2 := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/reject",
		map[string]interface{}{"note": ""}, admin)
	c2.Params = gin.Params{{Key: "id", Value: reqID}}
	h.RejectMerchantRequest(c2)

	if w2.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w2.Code, w2.Body.String())
	}
}

// ---------- Re-apply after rejection ----------

func TestSubmitMerchantRequest_ReapplyAfterRejection(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")
	admin := createMerchantTestUser(t, db, "admin")

	// Submit first request
	w1, c1 := newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), user)
	h.SubmitMerchantRequest(c1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first submit failed: %d: %s", w1.Code, w1.Body.String())
	}

	var submitResp map[string]interface{}
	json.Unmarshal(w1.Body.Bytes(), &submitResp)
	data := submitResp["data"].(map[string]interface{})
	reqID := data["id"].(string)

	// Reject it
	wR, cR := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/reject",
		map[string]interface{}{"note": "Incomplete info"}, admin)
	cR.Params = gin.Params{{Key: "id", Value: reqID}}
	h.RejectMerchantRequest(cR)
	if wR.Code != http.StatusOK {
		t.Fatalf("reject failed: %d: %s", wR.Code, wR.Body.String())
	}

	// Re-apply — should succeed since the old request is no longer pending
	w2, c2 := newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), user)
	h.SubmitMerchantRequest(c2)

	if w2.Code != http.StatusCreated {
		t.Errorf("expected %d, got %d: %s", http.StatusCreated, w2.Code, w2.Body.String())
	}
}

// ---------- List with status filter ----------

func TestListMerchantRequests_WithStatusFilter(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	admin := createMerchantTestUser(t, db, "admin")

	// Create 3 users with requests: 2 pending, 1 rejected
	for i := 0; i < 3; i++ {
		u := createMerchantTestUser(t, db, "user")
		ws, cs := newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), u)
		h.SubmitMerchantRequest(cs)
		if ws.Code != http.StatusCreated {
			t.Fatalf("submit %d failed: %d: %s", i, ws.Code, ws.Body.String())
		}

		// Reject the last one
		if i == 2 {
			var resp map[string]interface{}
			json.Unmarshal(ws.Body.Bytes(), &resp)
			data := resp["data"].(map[string]interface{})
			reqID := data["id"].(string)

			wR, cR := newMerchantContext("POST", "/", map[string]interface{}{"note": "Rejected"}, admin)
			cR.Params = gin.Params{{Key: "id", Value: reqID}}
			h.RejectMerchantRequest(cR)
			if wR.Code != http.StatusOK {
				t.Fatalf("reject failed: %d: %s", wR.Code, wR.Body.String())
			}
		}
	}

	// List with status=pending filter
	w, c := newMerchantContext("GET", "/api/admin/merchant-requests?status=pending", nil, admin)
	c.Request.URL.RawQuery = "status=pending"
	h.ListMerchantRequests(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	data := resp["data"].(map[string]interface{})
	requests := data["requests"].([]interface{})
	total := int(data["total"].(float64))

	if total != 2 {
		t.Errorf("expected total=2, got %d", total)
	}
	if len(requests) != 2 {
		t.Errorf("expected 2 pending requests, got %d", len(requests))
	}
}

// ── C1: provisioning retry re-entry test ─────────────────────────────────────

// handlerFakeProvisioner is a test double satisfying services.NombaProvisioner.
// Under the split onboarding model, merchant approval NEVER calls this — VA
// creation is deferred to merchant KYC activation — so vaCallCount must stay 0
// across every approval test.
type handlerFakeProvisioner struct {
	vaCallCount int
}

func (f *handlerFakeProvisioner) CreateVirtualAccount(_ context.Context, _, _, _, _ string) (string, error) {
	f.vaCallCount++
	return "0011223344", nil
}

// TestApproveMerchantRequest_PMApprovedNoVA verifies the split onboarding model:
// admin approval flips the request to approved and creates the payment_merchants
// row in the 'approved' state WITHOUT creating a virtual account (that is deferred
// to merchant KYC activation). The corporate fields captured at submit persist.
func TestApproveMerchantRequest_PMApprovedNoVA(t *testing.T) {
	db := setupMerchantTestDB(t)
	fake := &handlerFakeProvisioner{}
	svc := services.NewPMOnboardingService(db, fake)

	bus := newTestEventBus(t)
	h := NewMerchantRequestHandlerWithPM(db, bus, svc)

	user := createMerchantTestUser(t, db, "user")
	admin := createMerchantTestUser(t, db, "admin")

	// Submit a p2p_merchant request.
	submitInput := validPMSubmitInput()
	w1, c1 := newMerchantContext("POST", "/api/merchant-requests", submitInput, user)
	h.SubmitMerchantRequest(c1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("submit failed: %d: %s", w1.Code, w1.Body.String())
	}
	var submitResp map[string]interface{}
	json.Unmarshal(w1.Body.Bytes(), &submitResp)
	reqID := submitResp["data"].(map[string]interface{})["id"].(string)

	// Approve → 200.
	w2, c2 := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/approve",
		map[string]interface{}{"note": "Approved"}, admin)
	c2.Params = gin.Params{{Key: "id", Value: reqID}}
	h.ApproveMerchantRequest(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("approve: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}

	// Request is approved.
	var mr models.MerchantRequest
	if err := db.First(&mr, "id = ?", reqID).Error; err != nil {
		t.Fatalf("reload merchant request: %v", err)
	}
	if mr.Status != models.MerchantRequestStatusApproved {
		t.Errorf("request status = %q, want %q", mr.Status, models.MerchantRequestStatusApproved)
	}

	// PM row exists in 'approved' with NO virtual account yet.
	var pm models.PaymentMerchant
	if err := db.Where("user_id = ?", user.ID).First(&pm).Error; err != nil {
		t.Fatalf("load PM row: %v", err)
	}
	if pm.Status != models.PaymentMerchantStatusApproved {
		t.Errorf("PM status = %q, want approved", pm.Status)
	}
	if pm.NombaAccountRef != "" || pm.InboundAccountNumber != "" {
		t.Errorf("VA created during approval: ref=%q inbound=%q (should defer to activation)", pm.NombaAccountRef, pm.InboundAccountNumber)
	}
	if fake.vaCallCount != 0 {
		t.Errorf("CreateVirtualAccount called %d times during approval, want 0", fake.vaCallCount)
	}

	// Corporate fields captured at submit must persist onto the approved PM.
	if pm.OperatorWalletAddress == "" {
		t.Error("approved PM has empty OperatorWalletAddress — maker wallet not wired through")
	}
	if pm.SettlementBankCode == "" {
		t.Error("approved PM has empty SettlementBankCode — settlement bank not wired through")
	}
	if pm.SettlementAccountName == "" {
		t.Error("approved PM has empty SettlementAccountName — settlement account name not wired through")
	}
}

// ── I2: corporate-field validation at submit ─────────────────────────────────

// TestSubmitMerchantRequest_PMMissingSettlement: a p2p_merchant submit missing
// settlement fields is rejected (400) and no PM is provisioned/activated.
func TestSubmitMerchantRequest_PMMissingSettlement(t *testing.T) {
	db := setupMerchantTestDB(t)
	fake := &handlerFakeProvisioner{}
	svc := services.NewPMOnboardingService(db, fake)
	h := NewMerchantRequestHandlerWithPM(db, newTestEventBus(t), svc)
	user := createMerchantTestUser(t, db, "user")

	input := validPMSubmitInput()
	delete(input, "settlementAccountNumber")

	w, c := newMerchantContext("POST", "/api/merchant-requests", input, user)
	h.SubmitMerchantRequest(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}

	var count int64
	db.Model(&models.MerchantRequest{}).Count(&count)
	if count != 0 {
		t.Errorf("expected no merchant_request persisted, got %d", count)
	}
	db.Model(&models.PaymentMerchant{}).Count(&count)
	if count != 0 {
		t.Errorf("expected no payment_merchant persisted, got %d", count)
	}
}

// TestSubmitMerchantRequest_PMMissingMakerWallet: a p2p_merchant submit with no
// maker wallet is rejected (400).
func TestSubmitMerchantRequest_PMMissingMakerWallet(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")

	input := validPMSubmitInput()
	delete(input, "makerWalletAddress")

	w, c := newMerchantContext("POST", "/api/merchant-requests", input, user)
	h.SubmitMerchantRequest(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

// TestSubmitMerchantRequest_PMInvalidMakerWallet: a p2p_merchant submit with an
// invalid/zero maker wallet is rejected (400).
func TestSubmitMerchantRequest_PMInvalidMakerWallet(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}

	cases := []struct {
		name string
		addr string
	}{
		{"not-hex", "not-an-address"},
		{"zero", "0x0000000000000000000000000000000000000000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user := createMerchantTestUser(t, db, "user")
			input := validPMSubmitInput()
			input["makerWalletAddress"] = tc.addr

			w, c := newMerchantContext("POST", "/api/merchant-requests", input, user)
			h.SubmitMerchantRequest(c)

			if w.Code != http.StatusBadRequest {
				t.Errorf("addr %q: expected %d, got %d: %s", tc.addr, http.StatusBadRequest, w.Code, w.Body.String())
			}
		})
	}
}

// ── I1: PM approval gated to true admins ─────────────────────────────────────

// submitPMRequest is a helper: submits a valid p2p_merchant request as `user`
// and returns the created request ID.
func submitPMRequest(t *testing.T, h *MerchantRequestHandler, user *models.User) string {
	t.Helper()
	w, c := newMerchantContext("POST", "/api/merchant-requests", validPMSubmitInput(), user)
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("submit PM request failed: %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	return resp["data"].(map[string]interface{})["id"].(string)
}

// TestApproveMerchantRequest_PMNonAdminForbidden: a support/merchant caller
// cannot approve a p2p_merchant request — 403, no PM provisioned.
func TestApproveMerchantRequest_PMNonAdminForbidden(t *testing.T) {
	for _, role := range []string{"support", "merchant"} {
		t.Run(role, func(t *testing.T) {
			db := setupMerchantTestDB(t)
			fake := &handlerFakeProvisioner{}
			svc := services.NewPMOnboardingService(db, fake)
			h := NewMerchantRequestHandlerWithPM(db, newTestEventBus(t), svc)

			user := createMerchantTestUser(t, db, "user")
			reqID := submitPMRequest(t, h, user)

			caller := createMerchantTestUser(t, db, role)
			w, c := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/approve",
				map[string]interface{}{"note": "Approved"}, caller)
			c.Params = gin.Params{{Key: "id", Value: reqID}}
			h.ApproveMerchantRequest(c)

			if w.Code != http.StatusForbidden {
				t.Errorf("role %q: expected 403, got %d: %s", role, w.Code, w.Body.String())
			}

			// The request must stay pending — a forbidden caller must not
			// half-approve it (approved request with no provisioned PM).
			var mr models.MerchantRequest
			if err := db.First(&mr, "id = ?", reqID).Error; err != nil {
				t.Fatalf("role %q: load merchant request: %v", role, err)
			}
			if mr.Status != models.MerchantRequestStatusPending {
				t.Errorf("role %q: request status = %q, want %q", role, mr.Status, models.MerchantRequestStatusPending)
			}

			// No PM row should have been provisioned.
			var count int64
			db.Model(&models.PaymentMerchant{}).Count(&count)
			if count != 0 {
				t.Errorf("role %q: expected no PM provisioned, got %d", role, count)
			}
			if fake.vaCallCount != 0 {
				t.Errorf("role %q: provisioning was attempted (CreateVirtualAccount called %d times)", role, fake.vaCallCount)
			}
		})
	}
}

// TestApproveMerchantRequest_PMAdminSucceeds: a true admin can approve a
// p2p_merchant request; the PM lands in 'approved' (awaiting merchant KYC
// activation), with no VA provisioned during approval.
func TestApproveMerchantRequest_PMAdminSucceeds(t *testing.T) {
	db := setupMerchantTestDB(t)
	fake := &handlerFakeProvisioner{}
	svc := services.NewPMOnboardingService(db, fake)
	h := NewMerchantRequestHandlerWithPM(db, newTestEventBus(t), svc)

	user := createMerchantTestUser(t, db, "user")
	reqID := submitPMRequest(t, h, user)

	admin := createMerchantTestUser(t, db, "admin")
	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/approve",
		map[string]interface{}{"note": "Approved"}, admin)
	c.Params = gin.Params{{Key: "id", Value: reqID}}
	h.ApproveMerchantRequest(c)

	if w.Code != http.StatusOK {
		t.Errorf("admin approve: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var pm models.PaymentMerchant
	if err := db.Where("user_id = ?", user.ID).First(&pm).Error; err != nil {
		t.Fatalf("load PM row: %v", err)
	}
	if pm.Status != models.PaymentMerchantStatusApproved {
		t.Errorf("PM status = %q, want approved", pm.Status)
	}
	if fake.vaCallCount != 0 {
		t.Errorf("CreateVirtualAccount called %d times during approval, want 0", fake.vaCallCount)
	}
}

// TestApproveMerchantRequest_PMDormantNoHalfApprove: when the PM onboarding
// service is dormant (Nomba unconfigured → PMOnboarding == nil), approving a
// p2p_merchant request must fail cleanly with 503 and leave the request PENDING
// with the user's role unchanged. It must never commit a half-approval — an
// "approved" request with no active payment_merchants row — which would split-
// brain the UI (dashboard sees no active PM and shows "Upgrade", while the
// application screen sees an approved PM request and claims "You're a PM").
func TestApproveMerchantRequest_PMDormantNoHalfApprove(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := NewMerchantRequestHandlerWithPM(db, newTestEventBus(t), nil) // dormant: no Nomba

	user := createMerchantTestUser(t, db, "user")
	reqID := submitPMRequest(t, h, user)

	admin := createMerchantTestUser(t, db, "admin")
	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/approve",
		map[string]interface{}{"note": "Approved"}, admin)
	c.Params = gin.Params{{Key: "id", Value: reqID}}
	h.ApproveMerchantRequest(c)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}

	var mr models.MerchantRequest
	if err := db.First(&mr, "id = ?", reqID).Error; err != nil {
		t.Fatalf("load merchant request: %v", err)
	}
	if mr.Status != models.MerchantRequestStatusPending {
		t.Errorf("request status = %q, want %q (must not half-approve)", mr.Status, models.MerchantRequestStatusPending)
	}

	var u models.User
	if err := db.First(&u, "id = ?", user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if u.Role == models.RoleMerchant {
		t.Errorf("user role = %q, want unchanged (not upgraded to merchant)", u.Role)
	}

	var count int64
	db.Model(&models.PaymentMerchant{}).Count(&count)
	if count != 0 {
		t.Errorf("expected no PM row, got %d", count)
	}
}

// TestApproveMerchantRequest_ResumesStuckApprovedNoPMRow: an account that was
// approved while PM was dormant (request 'approved', NO payment_merchants row)
// must be resumable once a provider is configured — re-approving creates the PM
// row in 'approved' rather than 400-ing on "not pending". This unsticks the
// split-brain accounts created before the pre-flight gate existed.
func TestApproveMerchantRequest_ResumesStuckApprovedNoPMRow(t *testing.T) {
	db := setupMerchantTestDB(t)
	fake := &handlerFakeProvisioner{}
	svc := services.NewPMOnboardingService(db, fake)
	h := NewMerchantRequestHandlerWithPM(db, newTestEventBus(t), svc)

	// Seed the stuck state directly: an approved p2p_merchant request and NO
	// payment_merchants row (provisioning never ran under the old dormant path).
	user := createMerchantTestUser(t, db, "merchant")
	seedMerchantRequest(t, db, user.ID, "p2p_merchant", "approved")
	var req models.MerchantRequest
	if err := db.Where("user_id = ?", user.ID).First(&req).Error; err != nil {
		t.Fatalf("load seeded request: %v", err)
	}

	var count int64
	db.Model(&models.PaymentMerchant{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 0 {
		t.Fatalf("precondition: expected no PM row, got %d", count)
	}

	admin := createMerchantTestUser(t, db, "admin")
	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/"+req.ID.String()+"/approve",
		map[string]interface{}{"note": "Resume"}, admin)
	c.Params = gin.Params{{Key: "id", Value: req.ID.String()}}
	h.ApproveMerchantRequest(c)

	if w.Code != http.StatusOK {
		t.Fatalf("resume approve: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var pm models.PaymentMerchant
	if err := db.Where("user_id = ?", user.ID).First(&pm).Error; err != nil {
		t.Fatalf("load PM row after resume: %v", err)
	}
	if pm.Status != models.PaymentMerchantStatusApproved {
		t.Errorf("PM status = %q, want approved", pm.Status)
	}
	if fake.vaCallCount != 0 {
		t.Errorf("CreateVirtualAccount called %d times during approval, want 0", fake.vaCallCount)
	}
}

// newTestEventBus creates a started event bus for use in tests.
func newTestEventBus(t *testing.T) *events.Bus {
	t.Helper()
	bus := events.NewBus()
	t.Cleanup(bus.Stop)
	return bus
}

// ── Task 1: PM-upgrade carve-out tests ───────────────────────────────────────

// seedPaymentMerchant inserts a payment_merchants row for the given user with the
// specified status (and provisioning_status set to the same value for simplicity).
func seedPaymentMerchant(t *testing.T, db *gorm.DB, userID uuid.UUID, status string) {
	t.Helper()
	id := uuid.New()
	err := db.Exec(`INSERT INTO payment_merchants
		(id, user_id, name, status, provisioning_status, available_ngn, reserved_ngn,
		 per_trade_cap_usd, daily_cap_usd, created_at, updated_at)
		VALUES (?, ?, 'Test PM', ?, ?, '0', '0', 0, 0, datetime('now'), datetime('now'))`,
		id.String(), userID.String(), status, status).Error
	if err != nil {
		t.Fatalf("seedPaymentMerchant: %v", err)
	}
}

// seedMerchantRequest inserts a merchant_requests row directly.
func seedMerchantRequest(t *testing.T, db *gorm.DB, userID uuid.UUID, businessType, status string) {
	t.Helper()
	id := uuid.New()
	err := db.Exec(`INSERT INTO merchant_requests
		(id, user_id, business_type, business_name, address, city, state, country, phone, status, created_at, updated_at)
		VALUES (?, ?, ?, 'Seed Corp', '1 Seed St', 'Lagos', 'Lagos', 'Nigeria', '+2340000000000', ?, datetime('now'), datetime('now'))`,
		id.String(), userID.String(), businessType, status).Error
	if err != nil {
		t.Fatalf("seedMerchantRequest: %v", err)
	}
}

// TestSubmitMerchantRequest_MerchantUpgradeToPM: a role=merchant user with an
// existing approved general_products request can submit a p2p_merchant upgrade.
// The seeded stale approved row is load-bearing — without it the unscoped duplicate
// check would have passed anyway (false assurance), so we explicitly prove Critical-1.
func TestSubmitMerchantRequest_MerchantUpgradeToPM(t *testing.T) {
	db := setupMerchantTestDB(t)
	pmSvc := services.NewPMService(db)
	h := &MerchantRequestHandler{DB: db, PMService: pmSvc}

	user := createMerchantTestUser(t, db, models.RoleMerchant)

	// Seed the stale approved general_products request — this is what the old
	// unscoped duplicate check would have caught, proving Critical-1 is fixed.
	seedMerchantRequest(t, db, user.ID, "general_products", models.MerchantRequestStatusApproved)

	w, c := newMerchantContext("POST", "/api/merchant-requests", validPMSubmitInput(), user)
	h.SubmitMerchantRequest(c)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

// TestSubmitMerchantRequest_AlreadyActivePM: a user with an active payment_merchants
// row cannot submit another p2p_merchant application.
func TestSubmitMerchantRequest_AlreadyActivePM(t *testing.T) {
	db := setupMerchantTestDB(t)
	pmSvc := services.NewPMService(db)
	h := &MerchantRequestHandler{DB: db, PMService: pmSvc}

	user := createMerchantTestUser(t, db, models.RoleMerchant)
	seedPaymentMerchant(t, db, user.ID, models.PaymentMerchantStatusActive)

	w, c := newMerchantContext("POST", "/api/merchant-requests", validPMSubmitInput(), user)
	h.SubmitMerchantRequest(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "already a payment merchant") {
		t.Errorf("expected 'already a payment merchant' in body, got: %s", body)
	}
}

// TestSubmitMerchantRequest_FailedProvisioningResubmitBlocked: a merchant with a
// non-active payment_merchants row (failed provisioning) + a stuck approved
// p2p_merchant request cannot submit another PM application (Critical-2).
// Asserts exactly one payment_merchants row exists after the attempt.
func TestSubmitMerchantRequest_FailedProvisioningResubmitBlocked(t *testing.T) {
	db := setupMerchantTestDB(t)
	pmSvc := services.NewPMService(db)
	h := &MerchantRequestHandler{DB: db, PMService: pmSvc}

	user := createMerchantTestUser(t, db, models.RoleMerchant)

	// Seed a non-active PM row (failed provisioning).
	seedPaymentMerchant(t, db, user.ID, models.PaymentMerchantStatusPending)

	// Seed a stuck approved p2p_merchant request.
	seedMerchantRequest(t, db, user.ID, models.BusinessTypeP2PMerchant, models.MerchantRequestStatusApproved)

	w, c := newMerchantContext("POST", "/api/merchant-requests", validPMSubmitInput(), user)
	h.SubmitMerchantRequest(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// Critical-2: assert exactly one payment_merchants row (no new row created).
	// The schema's UNIQUE(user_id, kind) is a backstop, not the thing under test — the
	// real signal is that the type-scoped duplicate check returns 400 before the
	// submit ever reaches PM provisioning, so no second row is even attempted.
	var count int64
	db.Model(&models.PaymentMerchant{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 payment_merchants row, got %d", count)
	}
}

// ── Task 4: TSA Pay (payment_acceptance) onboarding ──────────────────────────

// fakePayProvisioner is a test double satisfying payMerchantProvisioner.
type fakePayProvisioner struct {
	db    *gorm.DB
	calls int
	err   error
}

// CreateFromRequest records a real pay_merchants row, so the retry test
// proves the handler reads the row, not the fake's memory.
func (f *fakePayProvisioner) CreateFromRequest(_ context.Context, req *models.MerchantRequest) (*models.PayMerchant, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	m := &models.PayMerchant{MerchantRequestID: req.ID, UserID: req.UserID, BusinessName: req.BusinessName,
		Status: models.PayMerchantActive, MinAmountKobo: 1, MaxAmountKobo: 2, WebhookSecretVersion: 1}
	return m, f.db.Create(m).Error
}

// NormalizeBusinessName is the real rule, with the default reserved words.
func (f *fakePayProvisioner) NormalizeBusinessName(name string) (string, error) {
	return (&services.PayMerchantService{}).NormalizeBusinessName(name)
}

func validPayAcceptanceInput() map[string]interface{} {
	in := validMerchantInput()
	in["businessType"] = "payment_acceptance"
	in["businessName"] = "Sky Air"
	in["website"] = "https://skyair.ng"
	return in
}

// verifyMerchantTestUser marks the user KYC-verified, as a TSA Pay applicant must be.
func verifyMerchantTestUser(t *testing.T, db *gorm.DB, user *models.User) {
	t.Helper()
	if err := db.Exec(`UPDATE users SET verification_status = ?, verified_legal_name = 'Ada Obi' WHERE id = ?`,
		models.VerificationStatusVerified, user.ID).Error; err != nil {
		t.Fatal(err)
	}
}

func submitPay(t *testing.T, h *MerchantRequestHandler, user *models.User) string {
	t.Helper()
	verifyMerchantTestUser(t, h.DB, user)
	w, c := newMerchantContext("POST", "/api/merchant-requests", validPayAcceptanceInput(), user)
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	var sub map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &sub)
	return sub["data"].(map[string]interface{})["id"].(string)
}

func approvePay(h *MerchantRequestHandler, admin *models.User, reqID string) *httptest.ResponseRecorder {
	w, c := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/approve", map[string]interface{}{"note": "ok"}, admin)
	c.Params = gin.Params{{Key: "id", Value: reqID}}
	h.ApproveMerchantRequest(c)
	return w
}

func TestSubmitMerchantRequest_PaymentAcceptanceWebsiteMustBeHTTPSWhenGiven(t *testing.T) {
	for _, site := range []string{"skyair.ng", "http://skyair.ng", "https://", "https://user:pw@skyair.ng",
		"https://skyair.ng,evil.example/", "https://skyair.ng;evil.example", "https://localhost", "https://203.0.113.5/", "https://[::1]/", "https://" + strings.Repeat("a", 250) + ".ng"} {
		db := setupMerchantTestDB(t)
		h := &MerchantRequestHandler{DB: db, PayMerchants: &fakePayProvisioner{db: db}}
		in := validPayAcceptanceInput()
		in["website"] = site
		user := createMerchantTestUser(t, db, "user")
		verifyMerchantTestUser(t, db, user)
		w, c := newMerchantContext("POST", "/api/merchant-requests", in, user)
		h.SubmitMerchantRequest(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("website %q: status %d", site, w.Code)
		}
	}
}

// A business with no website takes payments through payment links, so it may
// apply without one — and is approved with no website or return domain.
func TestSubmitMerchantRequest_PaymentAcceptanceWebsiteIsOptional(t *testing.T) {
	for _, site := range []any{"", "   ", nil} {
		db := setupMerchantTestDB(t)
		h := &MerchantRequestHandler{DB: db, PayMerchants: &fakePayProvisioner{db: db}}
		in := validPayAcceptanceInput()
		if site == nil {
			delete(in, "website")
		} else {
			in["website"] = site
		}
		user := createMerchantTestUser(t, db, "user")
		verifyMerchantTestUser(t, db, user)
		w, c := newMerchantContext("POST", "/api/merchant-requests", in, user)
		h.SubmitMerchantRequest(c)
		if w.Code != http.StatusCreated {
			t.Fatalf("website %v: status %d %s", site, w.Code, w.Body.String())
		}
		var stored models.MerchantRequest
		db.First(&stored, "user_id = ?", user.ID)
		if stored.Website != "" {
			t.Fatalf("website %v stored as %q", site, stored.Website)
		}
	}
}

func TestSubmitMerchantRequest_AMarketplaceMerchantMayApplyForPaymentsOnce(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db, PayMerchants: &fakePayProvisioner{db: db}}
	u := createMerchantTestUser(t, db, "merchant")
	submitPay(t, h, u)
	w, c := newMerchantContext("POST", "/api/merchant-requests", validPayAcceptanceInput(), u)
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusBadRequest && w.Code != http.StatusConflict {
		t.Fatalf("a second pending TSA Pay request: status %d", w.Code)
	}
}

// Reverse direction of TestSubmitMerchantRequest_AMarketplaceMerchantMayApplyForPaymentsOnce
// (whole-branch review Finding 3): the general, non-PM/non-Pay duplicate check
// must be scoped by business_type too, or an approved payment_acceptance
// request permanently blocks the same user from ever submitting a
// general_products/digital_products/service_provider request — a regression
// to the existing, live marketplace-merchant flow.
func TestSubmitMerchantRequest_ApprovedPaymentAcceptanceDoesNotBlockGeneralProducts(t *testing.T) {
	db := setupMerchantTestDB(t)
	prov := &fakePayProvisioner{db: db}
	h := &MerchantRequestHandler{DB: db, PayMerchants: prov}
	user := createMerchantTestUser(t, db, "user")
	admin := createMerchantTestUser(t, db, "admin")
	reqID := submitPay(t, h, user)
	if w := approvePay(h, admin, reqID); w.Code != http.StatusOK {
		t.Fatalf("approve payment_acceptance request: %d %s", w.Code, w.Body.String())
	}

	w, c := newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), user)
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("general_products submit after an approved payment_acceptance request: %d %s", w.Code, w.Body.String())
	}
}

func TestApproveMerchantRequest_PaymentAcceptanceProvisionsWithoutARoleFlip(t *testing.T) {
	db := setupMerchantTestDB(t)
	prov := &fakePayProvisioner{db: db}
	h := &MerchantRequestHandler{DB: db, PayMerchants: prov}
	user := createMerchantTestUser(t, db, "user")
	admin := createMerchantTestUser(t, db, "admin")
	reqID := submitPay(t, h, user)

	if w := approvePay(h, admin, reqID); w.Code != http.StatusOK || prov.calls != 1 {
		t.Fatalf("approve: %d calls %d: %s", w.Code, prov.calls, w.Body.String())
	}
	var u models.User
	db.First(&u, "id = ?", user.ID)
	if u.Role != "user" {
		t.Fatalf("role = %q; TSA Pay approval must not grant the admin-console merchant role", u.Role)
	}
	if w := approvePay(h, admin, reqID); w.Code == http.StatusOK || prov.calls != 1 {
		t.Fatalf("re-approving a provisioned request: %d calls %d", w.Code, prov.calls)
	}
}

func TestApproveMerchantRequest_PaymentAcceptanceRetriesAFailedProvisioning(t *testing.T) {
	db := setupMerchantTestDB(t)
	prov := &fakePayProvisioner{db: db, err: errors.New("boom")}
	h := &MerchantRequestHandler{DB: db, PayMerchants: prov}
	admin := createMerchantTestUser(t, db, "admin")
	reqID := submitPay(t, h, createMerchantTestUser(t, db, "user"))
	if w := approvePay(h, admin, reqID); w.Code != http.StatusInternalServerError {
		t.Fatalf("first approve: %d", w.Code)
	}
	prov.err = nil
	if w := approvePay(h, admin, reqID); w.Code != http.StatusOK || prov.calls != 2 {
		t.Fatalf("retry: %d calls %d", w.Code, prov.calls)
	}
}

// Approval makes a merchant live at once, so the applicant must still be
// verified when it happens — not only when they applied: a verification that
// lapsed in between (a name change's re-verification, say) must not take real
// money.
func TestApproveMerchantRequest_PaymentAcceptanceNeedsTheApplicantStillVerified(t *testing.T) {
	db := setupMerchantTestDB(t)
	prov := &fakePayProvisioner{db: db}
	h := &MerchantRequestHandler{DB: db, PayMerchants: prov}
	user := createMerchantTestUser(t, db, "user")
	admin := createMerchantTestUser(t, db, "admin")
	reqID := submitPay(t, h, user)
	if err := db.Exec(`UPDATE users SET verification_status = ? WHERE id = ?`, models.VerificationStatusPending, user.ID).Error; err != nil {
		t.Fatal(err)
	}

	w := approvePay(h, admin, reqID)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"applicant_not_verified"`) || prov.calls != 0 {
		t.Fatalf("approve an unverified applicant: %d calls %d: %s", w.Code, prov.calls, w.Body.String())
	}
	var req models.MerchantRequest
	db.First(&req, "id = ?", reqID)
	if req.Status != models.MerchantRequestStatusPending {
		t.Fatalf("status = %q; a refused approval must leave the request pending", req.Status)
	}

	verifyMerchantTestUser(t, db, user)
	if w := approvePay(h, admin, reqID); w.Code != http.StatusOK || prov.calls != 1 {
		t.Fatalf("approve once verified again: %d calls %d: %s", w.Code, prov.calls, w.Body.String())
	}
}

func TestApproveMerchantRequest_PaymentAcceptanceNeedsAnAdminAndTheService(t *testing.T) {
	db := setupMerchantTestDB(t)
	// Submitted while TSA Pay was enabled, approved after it went dormant.
	reqID := submitPay(t, &MerchantRequestHandler{DB: db, PayMerchants: &fakePayProvisioner{db: db}}, createMerchantTestUser(t, db, "user"))
	h := &MerchantRequestHandler{DB: db}
	for role, want := range map[string]int{"support": http.StatusForbidden, "admin": http.StatusServiceUnavailable} {
		if w := approvePay(h, createMerchantTestUser(t, db, role), reqID); w.Code != want {
			t.Fatalf("%s: status %d, want %d", role, w.Code, want)
		}
	}
}

func TestSubmitMerchantRequest_PaymentAcceptanceRefusedWhileTSAPayIsDormant(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")
	w, c := newMerchantContext("POST", "/api/merchant-requests", validPayAcceptanceInput(), user)
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "TSA Pay isn't available yet") {
		t.Fatalf("dormant submit: %d %s", w.Code, w.Body.String())
	}
	var n int64
	db.Model(&models.MerchantRequest{}).Count(&n)
	if n != 0 {
		t.Fatalf("%d merchant_requests rows created while TSA Pay is dormant", n)
	}
	// Other business types are unaffected by TSA Pay being dormant.
	w, c = newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), user)
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("general_products submit: %d %s", w.Code, w.Body.String())
	}
}

func TestApprovalMessage_TSAPayHasItsOwnWording(t *testing.T) {
	const want = "Your TSA Pay application is approved. Sign in at https://app.tsaconnectworld.com with your TSA Connect account" +
		" to create API keys, set your webhook and payouts, and start taking payments."
	if got := approvalMessage(models.BusinessTypePaymentAcceptance, "https://app.tsaconnectworld.com"); got != want {
		t.Fatalf("payment_acceptance message = %q", got)
	}
	if got := approvalMessage(models.BusinessTypePaymentAcceptance, ""); !strings.Contains(got, "Sign in at the TSA Connect web app with your TSA Connect account") {
		t.Fatalf("payment_acceptance message with no web app origin = %q", got)
	}
	for _, origin := range []string{"", "https://app.tsaconnectworld.com"} {
		if got := approvalMessage(models.BusinessTypeGeneralProducts, origin); got != "Your merchant application has been approved. You can now list products." {
			t.Fatalf("general message = %q", got)
		}
	}
}

func TestApproveMerchantRequest_PaymentAcceptanceNotificationDoesNotPromiseProducts(t *testing.T) {
	db := setupMerchantTestDB(t)
	bus := newTestEventBus(t)
	msgs := make(chan string, 1)
	bus.Subscribe(events.MerchantApproved, func(e events.Event) { msgs <- e.Message })
	h := &MerchantRequestHandler{DB: db, EventBus: bus, PayMerchants: &fakePayProvisioner{db: db}}
	reqID := submitPay(t, h, createMerchantTestUser(t, db, "user"))
	if w := approvePay(h, createMerchantTestUser(t, db, "admin"), reqID); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	select {
	case m := <-msgs:
		if strings.Contains(m, "list products") || !strings.Contains(m, "TSA Pay") {
			t.Fatalf("notification = %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no MerchantApproved event")
	}
}

func TestSubmitMerchantRequest_PaymentAcceptanceNeedsAVerifiedIdentity(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db, PayMerchants: &fakePayProvisioner{db: db}}
	user := createMerchantTestUser(t, db, "user")

	w, c := newMerchantContext("POST", "/api/merchant-requests", validPayAcceptanceInput(), user)
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"kyc_verification_required"`) ||
		!strings.Contains(w.Body.String(), "Verify your identity before applying for TSA Pay") {
		t.Fatalf("unverified applicant: %d %s", w.Code, w.Body.String())
	}
	var n int64
	db.Model(&models.MerchantRequest{}).Count(&n)
	if n != 0 {
		t.Fatalf("%d merchant_requests rows created for an unverified applicant", n)
	}

	// Other business types never needed verification to apply — neither a
	// marketplace merchant nor a Payment Merchant.
	w, c = newMerchantContext("POST", "/api/merchant-requests", validMerchantInput(), user)
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("general_products submit by an unverified user: %d %s", w.Code, w.Body.String())
	}
	w, c = newMerchantContext("POST", "/api/merchant-requests", validPMSubmitInput(), user)
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("p2p_merchant submit by an unverified user: %d %s", w.Code, w.Body.String())
	}

	verifyMerchantTestUser(t, db, user)
	w, c = newMerchantContext("POST", "/api/merchant-requests", validPayAcceptanceInput(), user)
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("verified applicant: %d %s", w.Code, w.Body.String())
	}
}

// A TSA Pay application is held to the business-name rule when submitted — the
// name stored tidied — and again when approved, for requests filed before it.
func TestMerchantRequest_PaymentAcceptanceBusinessNameRule(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db, PayMerchants: &fakePayProvisioner{db: db}}
	user := createMerchantTestUser(t, db, "user")
	verifyMerchantTestUser(t, db, user)
	for name, bad := range map[string]string{
		"a reserved word, dotted":      "T.S.A-Pay Payments",
		"a reserved word, in Cyrillic": "Suppоrt Desk Ltd", // Cyrillic о
		"a reserved word, full-width":  "ＴＳＡ Ｃｏｎｎｅｃｔ",
		"a hidden character":           "Sky\u200bAir",
	} {
		in := validPayAcceptanceInput()
		in["businessName"] = bad
		w, c := newMerchantContext("POST", "/api/merchant-requests", in, user)
		h.SubmitMerchantRequest(c)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d %s", name, w.Code, w.Body.String())
		}
	}
	in := validPayAcceptanceInput()
	in["businessName"] = "  Sky   Air  "
	w, c := newMerchantContext("POST", "/api/merchant-requests", in, user)
	h.SubmitMerchantRequest(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("a fine name: %d %s", w.Code, w.Body.String())
	}
	var stored models.MerchantRequest
	db.First(&stored, "user_id = ?", user.ID)
	if stored.BusinessName != "Sky Air" {
		t.Fatalf("stored %q", stored.BusinessName)
	}

	// Filed before the rule: approval refuses it, and nothing is provisioned.
	db.Model(&stored).Update("business_name", "TSA Pay Refunds")
	prov := h.PayMerchants.(*fakePayProvisioner)
	admin := createMerchantTestUser(t, db, "admin")
	if w := approvePay(h, admin, stored.ID.String()); w.Code != http.StatusUnprocessableEntity || prov.calls != 0 {
		t.Fatalf("approving a reserved name: %d calls=%d %s", w.Code, prov.calls, w.Body.String())
	}
	db.First(&stored, "id = ?", stored.ID)
	if stored.Status != models.MerchantRequestStatusPending {
		t.Fatalf("a refused approval changed the request: %s", stored.Status)
	}
}
