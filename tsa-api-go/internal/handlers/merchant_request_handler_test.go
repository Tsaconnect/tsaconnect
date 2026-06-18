package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

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
			persona_inquiry_id TEXT,
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
			created_at DATETIME, updated_at DATETIME
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
			user_id TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
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
			created_at DATETIME,
			updated_at DATETIME
		)`,
	}
	for _, s := range sqls {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("failed to create table: %v", err)
		}
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

func newMerchantContext(method, path string, body interface{}, user *models.User) (*httptest.ResponseRecorder, *gin.Context) {
	gin.SetMode(gin.TestMode)
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

// handlerFakeProvisioner is a test double that satisfies services.NombaProvisioner
// under the VA-only model. CreateVirtualAccount fails on the first call and
// succeeds on subsequent calls, so attempt-1 approval returns 5xx and leaves the
// request re-approvable.
type handlerFakeProvisioner struct {
	vaCallCount int
}

func (f *handlerFakeProvisioner) CreateVirtualAccount(_ context.Context, _, _ string) (string, error) {
	f.vaCallCount++
	if f.vaCallCount == 1 {
		return "", errors.New("VA create network error (attempt 1)")
	}
	return "0011223344", nil
}

// TestApproveMerchantRequest_PMProvisioningRetry exercises C1:
//   - The admin approves a p2p_merchant request.
//   - Attempt 1: VA-create fails → handler returns 5xx; request left in `approved`
//     state with a non-active payment_merchants row → re-approvable.
//   - Attempt 2: VA-create succeeds → 200; PM row is now active; sub-account was
//     created exactly once across both attempts.
func TestApproveMerchantRequest_PMProvisioningRetry(t *testing.T) {
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

	// ── Attempt 1: VA fails → 5xx ─────────────────────────────────────────────
	w2, c2 := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/approve",
		map[string]interface{}{"note": "Approved"}, admin)
	c2.Params = gin.Params{{Key: "id", Value: reqID}}
	h.ApproveMerchantRequest(c2)

	if w2.Code != http.StatusInternalServerError {
		t.Errorf("attempt 1: expected 500, got %d: %s", w2.Code, w2.Body.String())
	}

	// The merchant_request should now be approved (status flip already committed).
	var mr models.MerchantRequest
	if err := db.First(&mr, "id = ?", reqID).Error; err != nil {
		t.Fatalf("reload merchant request after attempt 1: %v", err)
	}
	if mr.Status != models.MerchantRequestStatusApproved {
		t.Errorf("after attempt 1: request status = %q, want %q", mr.Status, models.MerchantRequestStatusApproved)
	}

	// The payment_merchants row should exist but NOT be active.
	var pm models.PaymentMerchant
	if err := db.Where("user_id = ?", user.ID).First(&pm).Error; err != nil {
		t.Fatalf("load PM row after attempt 1: %v", err)
	}
	if pm.ProvisioningStatus == models.PMProvisioningActive {
		t.Error("attempt 1: PM row should NOT be active yet")
	}

	// VA-create attempted once so far (it failed).
	if fake.vaCallCount != 1 {
		t.Errorf("attempt 1: CreateVirtualAccount called %d times, want 1", fake.vaCallCount)
	}

	// ── Attempt 2: VA succeeds → 200 ─────────────────────────────────────────
	w3, c3 := newMerchantContext("POST", "/api/admin/merchant-requests/"+reqID+"/approve",
		map[string]interface{}{"note": "Approved retry"}, admin)
	c3.Params = gin.Params{{Key: "id", Value: reqID}}
	h.ApproveMerchantRequest(c3)

	if w3.Code != http.StatusOK {
		t.Errorf("attempt 2: expected 200, got %d: %s", w3.Code, w3.Body.String())
	}

	// PM row is now active.
	if err := db.Where("user_id = ?", user.ID).First(&pm).Error; err != nil {
		t.Fatalf("reload PM row after attempt 2: %v", err)
	}
	if pm.ProvisioningStatus != models.PMProvisioningActive {
		t.Errorf("attempt 2: provisioning_status = %q, want %q", pm.ProvisioningStatus, models.PMProvisioningActive)
	}
	if pm.Status != models.PaymentMerchantStatusActive {
		t.Errorf("attempt 2: PM status = %q, want %q", pm.Status, models.PaymentMerchantStatusActive)
	}

	// VA called twice (once failed, once succeeded) — the first attempt persisted
	// no accountRef, so attempt 2 retried the create exactly once.
	if fake.vaCallCount != 2 {
		t.Errorf("attempt 2: CreateVirtualAccount total calls = %d, want 2", fake.vaCallCount)
	}

	// C1: the activated PM must persist the corporate fields captured at submit.
	if pm.OperatorWalletAddress == "" {
		t.Error("activated PM has empty OperatorWalletAddress — maker wallet not wired through")
	}
	if pm.SettlementBankCode == "" {
		t.Error("activated PM has empty SettlementBankCode — settlement bank not wired through")
	}
	if pm.SettlementAccountName == "" {
		t.Error("activated PM has empty SettlementAccountName — settlement account name not wired through")
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
// p2p_merchant request and PM provisioning runs to active.
func TestApproveMerchantRequest_PMAdminSucceeds(t *testing.T) {
	db := setupMerchantTestDB(t)
	fake := &handlerFakeProvisioner{}
	// Make VA succeed on the first call for this happy-path test.
	fake.vaCallCount = 1
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
	if pm.ProvisioningStatus != models.PMProvisioningActive {
		t.Errorf("provisioning_status = %q, want %q", pm.ProvisioningStatus, models.PMProvisioningActive)
	}
}

// newTestEventBus creates a started event bus for use in tests.
func newTestEventBus(t *testing.T) *events.Bus {
	t.Helper()
	bus := events.NewBus()
	t.Cleanup(bus.Stop)
	return bus
}
