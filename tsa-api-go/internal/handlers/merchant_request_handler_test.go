package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
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
			documents TEXT, facial_verification TEXT,
			verification_status TEXT DEFAULT 'pending',
			verification_notes TEXT, account_status TEXT DEFAULT 'active',
			last_login DATETIME, login_attempts INTEGER DEFAULT 0,
			lock_until DATETIME, wallet_address TEXT,
			seed_phrase_backed_up INTEGER DEFAULT 0,
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
