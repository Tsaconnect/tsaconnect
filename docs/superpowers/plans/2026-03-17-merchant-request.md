# Merchant Request + Admin Approval Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the "Become a Merchant" flow — users submit merchant applications from the mobile app, admins review/approve/reject in the admin panel, and approved users get their role upgraded to `merchant`.

**Architecture:** New `MerchantRequest` model + `MerchantRequestHandler` (custom struct pattern like `CheckoutHandler`). Admin panel gets a new page with DataTable + approve/reject actions. Mobile app gets a new screen accessed from AppServices index 2.

**Tech Stack:** Go/Gin/GORM (backend), React/Vite/Shadcn/TanStack Query (admin), Expo React Native (mobile)

**Spec:** `docs/superpowers/specs/2026-03-17-merchant-request-design.md`

---

## File Structure

### Backend (tsa-api-go)

| Action | File | Responsibility |
|--------|------|---------------|
| Create | `internal/models/merchant_request.go` | MerchantRequest model + constants |
| Modify | `internal/config/database.go` | Add MerchantRequest to AutoMigrate |
| Create | `internal/handlers/merchant_request_handler.go` | Handler struct + 5 endpoint methods |
| Create | `internal/handlers/merchant_request_handler_test.go` | Unit tests (SQLite in-memory) |
| Modify | `internal/routes/routes.go` | Register merchant request routes |
| Modify | `cmd/server/main.go` | Instantiate handler, pass to SetupRoutes |

### Admin Panel (tsa-admin)

| Action | File | Responsibility |
|--------|------|---------------|
| Create | `src/api/merchant-requests.ts` | API client functions |
| Create | `src/pages/merchant-requests/index.tsx` | List page with DataTable + approve/reject |
| Modify | `src/types/index.ts` | MerchantRequest interface |
| Modify | `src/lib/permissions.ts` | Add merchant_requests permissions |
| Modify | `src/lib/constants.ts` | Add status color/label maps |
| Modify | `src/components/layout/sidebar.tsx` | Add nav item to Approvals group |
| Modify | `src/app.tsx` | Register route |

### Mobile App (tsa-app)

| Action | File | Responsibility |
|--------|------|---------------|
| Create | `app/merchants/merchant-request.tsx` | Application form + status display screen |
| Modify | `components/appservices/AppServices.tsx` | Wire up index 2 button |
| Modify | `components/services/api.ts` | Add API functions |

---

## Task 1: Backend — Model + AutoMigrate

**Files:**
- Create: `tsa-api-go/internal/models/merchant_request.go`
- Modify: `tsa-api-go/internal/config/database.go:25-42`

- [ ] **Step 1: Create the MerchantRequest model**

Create `internal/models/merchant_request.go`:

```go
package models

import (
	"time"

	"github.com/google/uuid"
)

const (
	MerchantRequestStatusPending  = "pending"
	MerchantRequestStatusApproved = "approved"
	MerchantRequestStatusRejected = "rejected"
)

const (
	BusinessTypeGeneralProducts = "general_products"
	BusinessTypeDigitalProducts = "digital_products"
	BusinessTypeP2PMerchant     = "p2p_merchant"
	BusinessTypeServiceProvider = "service_provider"
)

// ValidBusinessTypes is used for request validation.
var ValidBusinessTypes = []string{
	BusinessTypeGeneralProducts,
	BusinessTypeDigitalProducts,
	BusinessTypeP2PMerchant,
	BusinessTypeServiceProvider,
}

type MerchantRequest struct {
	ID                  uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID              uuid.UUID  `gorm:"type:uuid;index;not null" json:"userId"`
	BusinessType        string     `gorm:"not null" json:"businessType"`
	BusinessName        string     `gorm:"not null" json:"businessName"`
	BusinessDescription string     `gorm:"type:text" json:"businessDescription"`
	Address             string     `gorm:"not null" json:"address"`
	City                string     `gorm:"not null" json:"city"`
	State               string     `gorm:"not null" json:"state"`
	Country             string     `gorm:"not null" json:"country"`
	Phone               string     `gorm:"not null" json:"phone"`
	RegistrationNumber  string     `json:"registrationNumber,omitempty"`
	Status              string     `gorm:"not null;default:'pending';index" json:"status"`
	AdminNote           string     `json:"adminNote,omitempty"`
	ReviewedBy          *uuid.UUID `gorm:"type:uuid" json:"reviewedBy,omitempty"`
	ReviewedAt          *time.Time `json:"reviewedAt,omitempty"`
	User                User       `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Reviewer            *User      `gorm:"foreignKey:ReviewedBy" json:"reviewer,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

func (MerchantRequest) TableName() string {
	return "merchant_requests"
}
```

- [ ] **Step 2: Add to AutoMigrate**

In `internal/config/database.go`, add `&models.MerchantRequest{}` to the AutoMigrate call:

```go
func AutoMigrate() error {
	return DB.AutoMigrate(
		&models.User{},
		&models.Asset{},
		&models.Product{},
		&models.Cart{},
		&models.Category{},
		&models.Portfolio{},
		&models.Transaction{},
		&models.Wallet{},
		&models.WalletTransaction{},
		&models.SupportedToken{},
		&models.VerificationLog{},
		&models.Order{},
		&models.Deposit{},
		&models.ServiceContactPayment{},
		&models.MerchantRequest{},
	)
}
```

- [ ] **Step 3: Verify it compiles**

Run: `cd tsa-api-go && go build ./...`
Expected: Clean build, no errors.

- [ ] **Step 4: Commit**

```bash
git add internal/models/merchant_request.go internal/config/database.go
git commit -m "feat: add MerchantRequest model and AutoMigrate"
```

---

## Task 2: Backend — Handler with Submit + GetMyRequest endpoints

**Files:**
- Create: `tsa-api-go/internal/handlers/merchant_request_handler.go`

- [ ] **Step 1: Create the handler struct and request types**

Create `internal/handlers/merchant_request_handler.go`:

```go
package handlers

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// MerchantRequestHandler handles merchant request endpoints.
type MerchantRequestHandler struct {
	DB *gorm.DB
}

// NewMerchantRequestHandler creates a new MerchantRequestHandler.
func NewMerchantRequestHandler(db *gorm.DB) *MerchantRequestHandler {
	return &MerchantRequestHandler{DB: db}
}

type submitMerchantRequestInput struct {
	BusinessType        string `json:"businessType" binding:"required"`
	BusinessName        string `json:"businessName" binding:"required"`
	BusinessDescription string `json:"businessDescription"`
	Address             string `json:"address" binding:"required"`
	City                string `json:"city" binding:"required"`
	State               string `json:"state" binding:"required"`
	Country             string `json:"country" binding:"required"`
	Phone               string `json:"phone" binding:"required"`
	RegistrationNumber  string `json:"registrationNumber"`
}

type adminNoteInput struct {
	Note string `json:"note"`
}
```

- [ ] **Step 2: Implement SubmitMerchantRequest**

Append to `merchant_request_handler.go`:

```go
// SubmitMerchantRequest handles POST /api/merchant-requests
func (h *MerchantRequestHandler) SubmitMerchantRequest(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	// Already a merchant
	if user.Role == models.RoleMerchant {
		utils.ErrorResponse(c, http.StatusBadRequest, "You are already a merchant")
		return
	}

	var input submitMerchantRequestInput
	if err := c.ShouldBindJSON(&input); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body", err.Error())
		return
	}

	// Validate business type
	if !slices.Contains(models.ValidBusinessTypes, input.BusinessType) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid business type")
		return
	}

	// Check for existing pending request
	var existing models.MerchantRequest
	err := h.DB.Where("user_id = ? AND status = ?", user.ID, models.MerchantRequestStatusPending).First(&existing).Error
	if err == nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "You already have a pending merchant request")
		return
	}

	req := models.MerchantRequest{
		UserID:              user.ID,
		BusinessType:        input.BusinessType,
		BusinessName:        input.BusinessName,
		BusinessDescription: input.BusinessDescription,
		Address:             input.Address,
		City:                input.City,
		State:               input.State,
		Country:             input.Country,
		Phone:               input.Phone,
		RegistrationNumber:  input.RegistrationNumber,
		Status:              models.MerchantRequestStatusPending,
	}

	if err := h.DB.Create(&req).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create merchant request")
		return
	}

	utils.SuccessResponse(c, http.StatusCreated, "Merchant request submitted successfully", req)
}
```

- [ ] **Step 3: Implement GetMyMerchantRequest**

Append to `merchant_request_handler.go`:

```go
// GetMyMerchantRequest handles GET /api/merchant-requests/my-request
func (h *MerchantRequestHandler) GetMyMerchantRequest(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	var req models.MerchantRequest
	err := h.DB.Where("user_id = ?", user.ID).Order("created_at DESC").First(&req).Error
	if err != nil {
		utils.SuccessResponse(c, http.StatusOK, "Merchant request retrieved", nil)
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Merchant request retrieved", req)
}
```

- [ ] **Step 4: Verify it compiles**

Run: `cd tsa-api-go && go build ./...`
Expected: Clean build.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/merchant_request_handler.go
git commit -m "feat: add MerchantRequestHandler with submit and get-my-request"
```

---

## Task 3: Backend — Admin endpoints (List, Approve, Reject)

**Files:**
- Modify: `tsa-api-go/internal/handlers/merchant_request_handler.go`

- [ ] **Step 1: Implement ListMerchantRequests**

Append to `merchant_request_handler.go`:

```go
// ListMerchantRequests handles GET /api/admin/merchant-requests
func (h *MerchantRequestHandler) ListMerchantRequests(c *gin.Context) {
	status := c.Query("status")
	page := 1
	limit := 20

	if p := c.Query("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}
	if l := c.Query("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}

	query := h.DB.Model(&models.MerchantRequest{})
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	query.Count(&total)

	var requests []models.MerchantRequest
	query.Preload("User").Preload("Reviewer").
		Order("created_at DESC").
		Offset((page - 1) * limit).
		Limit(limit).
		Find(&requests)

	utils.SuccessResponse(c, http.StatusOK, "Merchant requests retrieved", gin.H{
		"requests": requests,
		"total":    total,
		"page":     page,
		"limit":    limit,
	})
}
```

`strconv` is already included in the imports from Step 1 of Task 2.

- [ ] **Step 2: Implement ApproveMerchantRequest**

Append to `merchant_request_handler.go`:

```go
// ApproveMerchantRequest handles POST /api/admin/merchant-requests/:id/approve
func (h *MerchantRequestHandler) ApproveMerchantRequest(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request ID")
		return
	}

	var input adminNoteInput
	c.ShouldBindJSON(&input) // note is optional for approval

	var req models.MerchantRequest
	if err := h.DB.First(&req, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Merchant request not found")
		return
	}

	if req.Status != models.MerchantRequestStatusPending {
		utils.ErrorResponse(c, http.StatusBadRequest, "Request is not in pending status")
		return
	}

	now := time.Now()

	// Use a transaction: approve request + upgrade user role
	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&req).Updates(map[string]interface{}{
			"status":      models.MerchantRequestStatusApproved,
			"reviewed_by": admin.ID,
			"reviewed_at": now,
			"admin_note":  input.Note,
		}).Error; err != nil {
			return err
		}

		if err := tx.Model(&models.User{}).Where("id = ?", req.UserID).
			Update("role", models.RoleMerchant).Error; err != nil {
			return err
		}

		return nil
	})

	if txErr != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to approve merchant request")
		return
	}

	// Reload with updated fields
	h.DB.Preload("User").First(&req, "id = ?", id)

	utils.SuccessResponse(c, http.StatusOK, "Merchant request approved", req)
}
```

- [ ] **Step 3: Implement RejectMerchantRequest**

Append to `merchant_request_handler.go`:

```go
// RejectMerchantRequest handles POST /api/admin/merchant-requests/:id/reject
func (h *MerchantRequestHandler) RejectMerchantRequest(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request ID")
		return
	}

	var input adminNoteInput
	if err := c.ShouldBindJSON(&input); err != nil || input.Note == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Note is required for rejection")
		return
	}

	var req models.MerchantRequest
	if err := h.DB.First(&req, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Merchant request not found")
		return
	}

	if req.Status != models.MerchantRequestStatusPending {
		utils.ErrorResponse(c, http.StatusBadRequest, "Request is not in pending status")
		return
	}

	now := time.Now()
	if err := h.DB.Model(&req).Updates(map[string]interface{}{
		"status":      models.MerchantRequestStatusRejected,
		"reviewed_by": admin.ID,
		"reviewed_at": now,
		"admin_note":  input.Note,
	}).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to reject merchant request")
		return
	}

	// Reload with updated fields
	h.DB.Preload("User").First(&req, "id = ?", id)

	utils.SuccessResponse(c, http.StatusOK, "Merchant request rejected", req)
}
```

- [ ] **Step 4: Verify it compiles**

Run: `cd tsa-api-go && go build ./...`
Expected: Clean build.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/merchant_request_handler.go
git commit -m "feat: add admin list/approve/reject merchant request endpoints"
```

---

## Task 4: Backend — Route Registration + Handler Wiring

**Files:**
- Modify: `tsa-api-go/internal/routes/routes.go:14`
- Modify: `tsa-api-go/cmd/server/main.go:68-101`

- [ ] **Step 1: Update SetupRoutes signature and add routes**

In `internal/routes/routes.go`:

1. Add `mrh *handlers.MerchantRequestHandler` parameter to `SetupRoutes` (line 14). Change the variadic `serviceContactHandlers` to a regular pointer parameter since we now have multiple optional handlers:

```go
func SetupRoutes(router *gin.Engine, cfg *config.Config, h *handlers.Handlers, ch *handlers.CheckoutHandler, mrh *handlers.MerchantRequestHandler, sch *handlers.ServiceContactHandler) {
```

Also update the variadic unpacking at lines 15-18. Replace:
```go
var sch *handlers.ServiceContactHandler
if len(serviceContactHandlers) > 0 {
    sch = serviceContactHandlers[0]
}
```
With nothing — `sch` is now a direct parameter.

2. Add merchant request routes before the service contact block (before line 202):

```go
	// Merchant request routes (authenticated user)
	if mrh != nil {
		merchantReqGroup := api.Group("/merchant-requests")
		merchantReqGroup.Use(auth)
		{
			merchantReqGroup.POST("", mrh.SubmitMerchantRequest)
			merchantReqGroup.GET("/my-request", mrh.GetMyMerchantRequest)
		}

		// Admin merchant request routes
		adminMerchantReqGroup := api.Group("/admin/merchant-requests")
		adminMerchantReqGroup.Use(adminAuth)
		{
			adminMerchantReqGroup.GET("", mrh.ListMerchantRequests)
			adminMerchantReqGroup.POST("/:id/approve", mrh.ApproveMerchantRequest)
			adminMerchantReqGroup.POST("/:id/reject", mrh.RejectMerchantRequest)
		}
	}
```

- [ ] **Step 2: Instantiate handler in main.go**

In `cmd/server/main.go`, after the existing handler initialization (after line 71):

```go
mrh := handlers.NewMerchantRequestHandler(config.DB)
```

Update the `SetupRoutes` call (line 101) to pass `mrh`. The `sch` is no longer variadic:

```go
routes.SetupRoutes(router, cfg, h, ch, mrh, sch)
```

- [ ] **Step 3: Verify it compiles**

Run: `cd tsa-api-go && go build ./...`
Expected: Clean build.

- [ ] **Step 4: Commit**

```bash
git add internal/routes/routes.go cmd/server/main.go
git commit -m "feat: wire up merchant request routes and handler"
```

---

## Task 5: Backend — Tests

**Files:**
- Create: `tsa-api-go/internal/handlers/merchant_request_handler_test.go`

- [ ] **Step 1: Write test setup and helper**

Create `internal/handlers/merchant_request_handler_test.go`:

```go
package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

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
			country TEXT,
			state TEXT,
			city TEXT,
			address TEXT,
			profile_photo TEXT,
			referral_code TEXT,
			referred_by TEXT,
			documents TEXT,
			facial_verification TEXT,
			verification_status TEXT DEFAULT 'pending',
			verification_notes TEXT,
			account_status TEXT DEFAULT 'active',
			last_login DATETIME,
			login_attempts INTEGER DEFAULT 0,
			lock_until DATETIME,
			wallet_address TEXT UNIQUE,
			seed_phrase_backed_up INTEGER DEFAULT 0,
			deleted_at DATETIME,
			submitted_for_verification_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME
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

	config.DB = db
	return db
}

func createMerchantTestUser(t *testing.T, db *gorm.DB, role string) *models.User {
	t.Helper()
	user := &models.User{
		ID:       uuid.New(),
		Name:     "Test User",
		Username: "testuser_" + uuid.New().String()[:8],
		Email:    "test_" + uuid.New().String()[:8] + "@example.com",
		Password: "$2a$10$fakehash",
		Role:     role,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	return user
}

func newMerchantReqContext(method, path string, body interface{}, user *models.User) (*httptest.ResponseRecorder, *gin.Context) {
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
```

- [ ] **Step 2: Write submit request tests**

Append to the test file:

```go
func TestSubmitMerchantRequest_Success(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")

	input := map[string]string{
		"businessType":    "general_products",
		"businessName":    "Test Shop",
		"address":         "123 Main St",
		"city":            "Lagos",
		"state":           "Lagos",
		"country":         "Nigeria",
		"phone":           "+2348012345678",
	}

	w, c := newMerchantReqContext("POST", "/api/merchant-requests", input, user)
	h.SubmitMerchantRequest(c)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["success"] != true {
		t.Fatalf("expected success true, got %v", resp["success"])
	}
}

func TestSubmitMerchantRequest_AlreadyMerchant(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "merchant")

	input := map[string]string{
		"businessType": "general_products",
		"businessName": "Test Shop",
		"address":      "123 Main St",
		"city":         "Lagos",
		"state":        "Lagos",
		"country":      "Nigeria",
		"phone":        "+2348012345678",
	}

	w, c := newMerchantReqContext("POST", "/api/merchant-requests", input, user)
	h.SubmitMerchantRequest(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitMerchantRequest_DuplicatePending(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")

	// Create an existing pending request
	existing := models.MerchantRequest{
		ID:           uuid.New(),
		UserID:       user.ID,
		BusinessType: "general_products",
		BusinessName: "Existing Shop",
		Address:      "456 Other St",
		City:         "Lagos",
		State:        "Lagos",
		Country:      "Nigeria",
		Phone:        "+2348099999999",
		Status:       models.MerchantRequestStatusPending,
	}
	db.Create(&existing)

	input := map[string]string{
		"businessType": "digital_products",
		"businessName": "New Shop",
		"address":      "789 New St",
		"city":         "Abuja",
		"state":        "FCT",
		"country":      "Nigeria",
		"phone":        "+2348012345678",
	}

	w, c := newMerchantReqContext("POST", "/api/merchant-requests", input, user)
	h.SubmitMerchantRequest(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitMerchantRequest_InvalidBusinessType(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")

	input := map[string]string{
		"businessType": "invalid_type",
		"businessName": "Test Shop",
		"address":      "123 Main St",
		"city":         "Lagos",
		"state":        "Lagos",
		"country":      "Nigeria",
		"phone":        "+2348012345678",
	}

	w, c := newMerchantReqContext("POST", "/api/merchant-requests", input, user)
	h.SubmitMerchantRequest(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 3: Write get-my-request test**

```go
func TestGetMyMerchantRequest_Found(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")

	req := models.MerchantRequest{
		ID:           uuid.New(),
		UserID:       user.ID,
		BusinessType: "general_products",
		BusinessName: "Test Shop",
		Address:      "123 Main St",
		City:         "Lagos",
		State:        "Lagos",
		Country:      "Nigeria",
		Phone:        "+2348012345678",
		Status:       models.MerchantRequestStatusPending,
	}
	db.Create(&req)

	w, c := newMerchantReqContext("GET", "/api/merchant-requests/my-request", nil, user)
	h.GetMyMerchantRequest(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetMyMerchantRequest_NotFound(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")

	w, c := newMerchantReqContext("GET", "/api/merchant-requests/my-request", nil, user)
	h.GetMyMerchantRequest(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["data"] != nil {
		t.Fatalf("expected nil data, got %v", resp["data"])
	}
}
```

- [ ] **Step 4: Write admin approve test (verify role change)**

```go
func TestApproveMerchantRequest_Success(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")
	admin := createMerchantTestUser(t, db, "admin")

	req := models.MerchantRequest{
		ID:           uuid.New(),
		UserID:       user.ID,
		BusinessType: "general_products",
		BusinessName: "Test Shop",
		Address:      "123 Main St",
		City:         "Lagos",
		State:        "Lagos",
		Country:      "Nigeria",
		Phone:        "+2348012345678",
		Status:       models.MerchantRequestStatusPending,
	}
	db.Create(&req)

	input := map[string]string{"note": "Looks good"}
	w, c := newMerchantReqContext("POST", "/api/admin/merchant-requests/"+req.ID.String()+"/approve", input, admin)
	c.Params = gin.Params{{Key: "id", Value: req.ID.String()}}
	h.ApproveMerchantRequest(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify user role was updated
	var updatedUser models.User
	db.First(&updatedUser, "id = ?", user.ID)
	if updatedUser.Role != models.RoleMerchant {
		t.Fatalf("expected role 'merchant', got '%s'", updatedUser.Role)
	}
}

func TestApproveMerchantRequest_NotPending(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")
	admin := createMerchantTestUser(t, db, "admin")

	req := models.MerchantRequest{
		ID:           uuid.New(),
		UserID:       user.ID,
		BusinessType: "general_products",
		BusinessName: "Test Shop",
		Address:      "123 Main St",
		City:         "Lagos",
		State:        "Lagos",
		Country:      "Nigeria",
		Phone:        "+2348012345678",
		Status:       models.MerchantRequestStatusRejected,
	}
	db.Create(&req)

	w, c := newMerchantReqContext("POST", "/api/admin/merchant-requests/"+req.ID.String()+"/approve", nil, admin)
	c.Params = gin.Params{{Key: "id", Value: req.ID.String()}}
	h.ApproveMerchantRequest(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 5: Write admin reject tests**

```go
func TestRejectMerchantRequest_Success(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")
	admin := createMerchantTestUser(t, db, "admin")

	req := models.MerchantRequest{
		ID:           uuid.New(),
		UserID:       user.ID,
		BusinessType: "general_products",
		BusinessName: "Test Shop",
		Address:      "123 Main St",
		City:         "Lagos",
		State:        "Lagos",
		Country:      "Nigeria",
		Phone:        "+2348012345678",
		Status:       models.MerchantRequestStatusPending,
	}
	db.Create(&req)

	input := map[string]string{"note": "Incomplete information"}
	w, c := newMerchantReqContext("POST", "/api/admin/merchant-requests/"+req.ID.String()+"/reject", input, admin)
	c.Params = gin.Params{{Key: "id", Value: req.ID.String()}}
	h.RejectMerchantRequest(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRejectMerchantRequest_NoteRequired(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")
	admin := createMerchantTestUser(t, db, "admin")

	req := models.MerchantRequest{
		ID:           uuid.New(),
		UserID:       user.ID,
		BusinessType: "general_products",
		BusinessName: "Test Shop",
		Address:      "123 Main St",
		City:         "Lagos",
		State:        "Lagos",
		Country:      "Nigeria",
		Phone:        "+2348012345678",
		Status:       models.MerchantRequestStatusPending,
	}
	db.Create(&req)

	input := map[string]string{"note": ""}
	w, c := newMerchantReqContext("POST", "/api/admin/merchant-requests/"+req.ID.String()+"/reject", input, admin)
	c.Params = gin.Params{{Key: "id", Value: req.ID.String()}}
	h.RejectMerchantRequest(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListMerchantRequests_WithStatusFilter(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")
	admin := createMerchantTestUser(t, db, "admin")

	// Create requests with different statuses
	for _, status := range []string{"pending", "approved", "rejected"} {
		db.Create(&models.MerchantRequest{
			ID: uuid.New(), UserID: user.ID,
			BusinessType: "general_products", BusinessName: "Shop " + status,
			Address: "123 St", City: "Lagos", State: "Lagos", Country: "Nigeria", Phone: "+234",
			Status: status,
		})
	}

	w, c := newMerchantReqContext("GET", "/api/admin/merchant-requests?status=pending", nil, admin)
	c.Request.URL.RawQuery = "status=pending"
	h.ListMerchantRequests(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	requests := data["requests"].([]interface{})
	if len(requests) != 1 {
		t.Fatalf("expected 1 pending request, got %d", len(requests))
	}
}

func TestSubmitMerchantRequest_ReapplyAfterRejection(t *testing.T) {
	db := setupMerchantTestDB(t)
	h := &MerchantRequestHandler{DB: db}
	user := createMerchantTestUser(t, db, "user")

	// Create a rejected request
	rejected := models.MerchantRequest{
		ID:           uuid.New(),
		UserID:       user.ID,
		BusinessType: "general_products",
		BusinessName: "Old Shop",
		Address:      "123 Main St",
		City:         "Lagos",
		State:        "Lagos",
		Country:      "Nigeria",
		Phone:        "+2348012345678",
		Status:       models.MerchantRequestStatusRejected,
	}
	db.Create(&rejected)

	// Re-apply should succeed
	input := map[string]string{
		"businessType": "digital_products",
		"businessName": "New Shop",
		"address":      "456 New St",
		"city":         "Abuja",
		"state":        "FCT",
		"country":      "Nigeria",
		"phone":        "+2348012345678",
	}

	w, c := newMerchantReqContext("POST", "/api/merchant-requests", input, user)
	h.SubmitMerchantRequest(c)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 6: Run tests**

Run: `cd tsa-api-go && go test ./internal/handlers/ -run TestMerchant -v`
Expected: All tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/handlers/merchant_request_handler_test.go
git commit -m "test: add merchant request handler unit tests"
```

---

## Task 6: Admin Panel — Types, API Client, Permissions, Constants

**Files:**
- Modify: `tsa-admin/src/types/index.ts`
- Create: `tsa-admin/src/api/merchant-requests.ts`
- Modify: `tsa-admin/src/lib/permissions.ts`
- Modify: `tsa-admin/src/lib/constants.ts`

- [ ] **Step 1: Add MerchantRequest type**

In `tsa-admin/src/types/index.ts`, add after the `Deposit` interface:

```typescript
export type MerchantRequestStatus = 'pending' | 'approved' | 'rejected';

export interface MerchantRequest {
  id: string;
  userId: string;
  businessType: string;
  businessName: string;
  businessDescription?: string;
  address: string;
  city: string;
  state: string;
  country: string;
  phone: string;
  registrationNumber?: string;
  status: MerchantRequestStatus;
  adminNote?: string;
  reviewedBy?: string;
  reviewedAt?: string;
  user?: User;
  reviewer?: User;
  createdAt: string;
  updatedAt: string;
}
```

- [ ] **Step 2: Create API client**

Create `tsa-admin/src/api/merchant-requests.ts`:

```typescript
import client from './client';
import type { ApiResponse, MerchantRequest, PaginationMeta } from '@/types';

export async function getMerchantRequests(params: { page?: number; limit?: number; status?: string }) {
  const { data } = await client.get<ApiResponse<{ requests: MerchantRequest[]; total: number; page: number; limit: number }>>(
    '/admin/merchant-requests',
    { params }
  );
  return data;
}

export async function approveMerchantRequest(id: string, note?: string) {
  const { data } = await client.post<ApiResponse<MerchantRequest>>(
    `/admin/merchant-requests/${id}/approve`,
    { note }
  );
  return data;
}

export async function rejectMerchantRequest(id: string, note: string) {
  const { data } = await client.post<ApiResponse<MerchantRequest>>(
    `/admin/merchant-requests/${id}/reject`,
    { note }
  );
  return data;
}
```

- [ ] **Step 3: Add permissions**

In `tsa-admin/src/lib/permissions.ts`:

Add to `Permission` type (after `'settings.view'`):
```typescript
  | 'merchant_requests.view'
  | 'merchant_requests.approve';
```

Add to `ROLE_PERMISSIONS`:
- `super_admin` array: `'merchant_requests.view', 'merchant_requests.approve'`
- `admin` array: `'merchant_requests.view', 'merchant_requests.approve'`
- `support` array: `'merchant_requests.view'`

Add to `canAccessRoute` routePermissions:
```typescript
'/merchant-requests': 'merchant_requests.view',
```

- [ ] **Step 4: Add status constants**

In `tsa-admin/src/lib/constants.ts`, add:

```typescript
export const MERCHANT_REQUEST_STATUS_LABELS: Record<string, string> = {
  pending: 'Pending',
  approved: 'Approved',
  rejected: 'Rejected',
};

export const MERCHANT_REQUEST_STATUS_COLORS: Record<string, string> = {
  pending: 'bg-amber-100 text-amber-700',
  approved: 'bg-green-100 text-green-700',
  rejected: 'bg-red-100 text-red-700',
};

export const BUSINESS_TYPE_LABELS: Record<string, string> = {
  general_products: 'General Products',
  digital_products: 'Digital Products',
  p2p_merchant: 'P2P Merchant',
  service_provider: 'Service Provider',
};
```

- [ ] **Step 5: Commit**

```bash
cd tsa-admin
git add src/types/index.ts src/api/merchant-requests.ts src/lib/permissions.ts src/lib/constants.ts
git commit -m "feat(admin): add merchant request types, API client, and permissions"
```

---

## Task 7: Admin Panel — Merchant Requests Page + Routing + Sidebar

**Files:**
- Create: `tsa-admin/src/pages/merchant-requests/index.tsx`
- Modify: `tsa-admin/src/components/layout/sidebar.tsx`
- Modify: `tsa-admin/src/app.tsx`

- [ ] **Step 1: Create the Merchant Requests page**

Create `tsa-admin/src/pages/merchant-requests/index.tsx`:

```tsx
import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { getMerchantRequests, approveMerchantRequest, rejectMerchantRequest } from '@/api/merchant-requests';
import { DataTable } from '@/components/shared/data-table';
import { PageHeader } from '@/components/shared/page-header';
import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { StatusBadge } from '@/components/shared/status-badge';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog';
import { Textarea } from '@/components/ui/textarea';
import { MERCHANT_REQUEST_STATUS_COLORS, MERCHANT_REQUEST_STATUS_LABELS, BUSINESS_TYPE_LABELS } from '@/lib/constants';
import { CheckCircle2, XCircle, Eye } from 'lucide-react';
import { toast } from 'sonner';
import { usePermission } from '@/hooks/use-permission';
import type { ColumnDef } from '@tanstack/react-table';
import type { MerchantRequest } from '@/types';

export default function MerchantRequestsPage() {
  const [page, setPage] = useState(1);
  const [statusFilter, setStatusFilter] = useState('');
  const [selected, setSelected] = useState<MerchantRequest | null>(null);
  const [rejectNote, setRejectNote] = useState('');
  const [approveNote, setApproveNote] = useState('');
  const [showDetail, setShowDetail] = useState(false);
  const [showReject, setShowReject] = useState(false);
  const [showApprove, setShowApprove] = useState(false);
  const queryClient = useQueryClient();
  const canApprove = usePermission('merchant_requests.approve');

  const { data, isLoading } = useQuery({
    queryKey: ['merchant-requests', page, statusFilter],
    queryFn: () => getMerchantRequests({ page, limit: 20, status: statusFilter || undefined }),
  });

  const approveMutation = useMutation({
    mutationFn: ({ id, note }: { id: string; note?: string }) => approveMerchantRequest(id, note),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['merchant-requests'] });
      toast.success('Merchant request approved');
      setShowApprove(false);
      setApproveNote('');
    },
    onError: () => toast.error('Failed to approve request'),
  });

  const rejectMutation = useMutation({
    mutationFn: ({ id, note }: { id: string; note: string }) => rejectMerchantRequest(id, note),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['merchant-requests'] });
      toast.success('Merchant request rejected');
      setShowReject(false);
      setRejectNote('');
    },
    onError: () => toast.error('Failed to reject request'),
  });

  const requests = data?.data?.requests ?? [];
  const total = data?.data?.total ?? 0;
  const totalPages = Math.ceil(total / 20);

  const columns: ColumnDef<MerchantRequest>[] = [
    { accessorKey: 'businessName', header: 'Business Name' },
    {
      accessorKey: 'businessType',
      header: 'Type',
      cell: ({ row }) => BUSINESS_TYPE_LABELS[row.original.businessType] || row.original.businessType,
    },
    {
      id: 'applicant',
      header: 'Applicant',
      cell: ({ row }) => (
        <div>
          <p className="font-medium">{row.original.user?.name}</p>
          <p className="text-xs text-slate-500">{row.original.user?.email}</p>
        </div>
      ),
    },
    {
      accessorKey: 'status',
      header: 'Status',
      cell: ({ row }) => (
        <StatusBadge
          status={row.original.status}
          colorMap={MERCHANT_REQUEST_STATUS_COLORS}
          labelMap={MERCHANT_REQUEST_STATUS_LABELS}
        />
      ),
    },
    {
      accessorKey: 'createdAt',
      header: 'Submitted',
      cell: ({ row }) => new Date(row.original.createdAt).toLocaleDateString(),
    },
    {
      id: 'actions',
      header: 'Actions',
      cell: ({ row }) => (
        <div className="flex gap-1">
          <Button size="sm" variant="ghost" onClick={() => { setSelected(row.original); setShowDetail(true); }}>
            <Eye className="h-4 w-4" />
          </Button>
          {canApprove && row.original.status === 'pending' && (
            <>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => { setSelected(row.original); setShowApprove(true); }}
              >
                <CheckCircle2 className="h-4 w-4 text-green-600" />
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => { setSelected(row.original); setShowReject(true); }}
              >
                <XCircle className="h-4 w-4 text-red-600" />
              </Button>
            </>
          )}
        </div>
      ),
    },
  ];

  return (
    <div>
      <PageHeader title="Merchant Requests" description="Review and manage merchant applications" />
      <div className="mb-4">
        <Select value={statusFilter} onValueChange={(v) => { setStatusFilter(v === 'all' ? '' : v); setPage(1); }}>
          <SelectTrigger className="w-40"><SelectValue placeholder="All statuses" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All statuses</SelectItem>
            <SelectItem value="pending">Pending</SelectItem>
            <SelectItem value="approved">Approved</SelectItem>
            <SelectItem value="rejected">Rejected</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <DataTable columns={columns} data={requests} page={page} totalPages={totalPages} onPageChange={setPage} isLoading={isLoading} />

      {/* Detail Dialog */}
      <Dialog open={showDetail} onOpenChange={setShowDetail}>
        <DialogContent className="max-w-lg">
          <DialogHeader><DialogTitle>Merchant Application</DialogTitle></DialogHeader>
          {selected && (
            <div className="space-y-3 text-sm">
              <div className="grid grid-cols-2 gap-2">
                <div><span className="text-slate-500">Business Name:</span><p className="font-medium">{selected.businessName}</p></div>
                <div><span className="text-slate-500">Type:</span><p>{BUSINESS_TYPE_LABELS[selected.businessType]}</p></div>
                <div><span className="text-slate-500">Phone:</span><p>{selected.phone}</p></div>
                <div><span className="text-slate-500">Registration #:</span><p>{selected.registrationNumber || 'N/A'}</p></div>
                <div className="col-span-2"><span className="text-slate-500">Address:</span><p>{selected.address}, {selected.city}, {selected.state}, {selected.country}</p></div>
                {selected.businessDescription && (
                  <div className="col-span-2"><span className="text-slate-500">Description:</span><p>{selected.businessDescription}</p></div>
                )}
              </div>
              <div className="border-t pt-2">
                <span className="text-slate-500">Applicant:</span>
                <p className="font-medium">{selected.user?.name} ({selected.user?.email})</p>
                <p className="text-xs text-slate-400">Verification: {selected.user?.verificationStatus}</p>
              </div>
              {selected.adminNote && (
                <div className="border-t pt-2">
                  <span className="text-slate-500">Admin Note:</span>
                  <p>{selected.adminNote}</p>
                </div>
              )}
            </div>
          )}
        </DialogContent>
      </Dialog>

      {/* Approve Dialog */}
      <Dialog open={showApprove} onOpenChange={setShowApprove}>
        <DialogContent>
          <DialogHeader><DialogTitle>Approve Merchant Request</DialogTitle></DialogHeader>
          <p className="text-sm text-slate-600">Approve <strong>{selected?.businessName}</strong>? This will upgrade the user to merchant role.</p>
          <Textarea placeholder="Optional note..." value={approveNote} onChange={(e) => setApproveNote(e.target.value)} />
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowApprove(false)}>Cancel</Button>
            <Button
              onClick={() => selected && approveMutation.mutate({ id: selected.id, note: approveNote || undefined })}
              disabled={approveMutation.isPending}
            >
              Approve
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Reject Dialog */}
      <Dialog open={showReject} onOpenChange={setShowReject}>
        <DialogContent>
          <DialogHeader><DialogTitle>Reject Merchant Request</DialogTitle></DialogHeader>
          <p className="text-sm text-slate-600">Reject <strong>{selected?.businessName}</strong>?</p>
          <Textarea placeholder="Reason for rejection (required)..." value={rejectNote} onChange={(e) => setRejectNote(e.target.value)} />
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowReject(false)}>Cancel</Button>
            <Button
              variant="destructive"
              onClick={() => selected && rejectMutation.mutate({ id: selected.id, note: rejectNote })}
              disabled={rejectMutation.isPending || !rejectNote.trim()}
            >
              Reject
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
```

- [ ] **Step 2: Add sidebar nav item**

In `tsa-admin/src/components/layout/sidebar.tsx`, add to the `Approvals` group items array (after Verifications):

```typescript
{ path: '/merchant-requests', label: 'Merchant Requests', icon: Store },
```

Add `Store` to the lucide-react import.

- [ ] **Step 3: Register route in app.tsx**

In `tsa-admin/src/app.tsx`:

Add import:
```typescript
import MerchantRequestsPage from '@/pages/merchant-requests/index';
```

Add route inside the protected layout (after the verifications route):
```tsx
<Route path="merchant-requests" element={<MerchantRequestsPage />} />
```

- [ ] **Step 4: Verify admin panel builds**

Run: `cd tsa-admin && npm run build`
Expected: Clean build, no TypeScript errors.

- [ ] **Step 5: Commit**

```bash
cd tsa-admin
git add src/pages/merchant-requests/index.tsx src/components/layout/sidebar.tsx src/app.tsx
git commit -m "feat(admin): add merchant requests page with approve/reject"
```

---

## Task 8: Mobile App — Merchant Request Screen

**Files:**
- Create: `tsa-app/app/merchants/merchant-request.tsx`
- Modify: `tsa-app/components/services/api.ts`
- Modify: `tsa-app/components/appservices/AppServices.tsx`

- [ ] **Step 1: Add API functions to api.ts**

In `tsa-app/components/services/api.ts`, add these functions:

```typescript
export async function getMyMerchantRequest(): Promise<ApiResponse> {
  try {
    const token = await AsyncStorage.getItem('token');
    const response = await fetch(`${API_BASE_URL}/merchant-requests/my-request`, {
      headers: {
        'Authorization': `Bearer ${token}`,
        'Content-Type': 'application/json',
      },
    });
    return response.json();
  } catch (error: any) {
    return { success: false, message: error.message || 'Failed to fetch merchant request' };
  }
}

export async function submitMerchantRequest(data: {
  businessType: string;
  businessName: string;
  businessDescription?: string;
  address: string;
  city: string;
  state: string;
  country: string;
  phone: string;
  registrationNumber?: string;
}): Promise<ApiResponse> {
  try {
    const token = await AsyncStorage.getItem('token');
    const response = await fetch(`${API_BASE_URL}/merchant-requests`, {
      method: 'POST',
      headers: {
        'Authorization': `Bearer ${token}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify(data),
    });
    return response.json();
  } catch (error: any) {
    return { success: false, message: error.message || 'Failed to submit merchant request' };
  }
}
```

- [ ] **Step 2: Create the merchant request screen**

Create `tsa-app/app/merchants/merchant-request.tsx`:

```tsx
import React, { useState, useEffect } from 'react';
import {
  View,
  Text,
  TextInput,
  TouchableOpacity,
  ScrollView,
  StyleSheet,
  ActivityIndicator,
  Alert,
} from 'react-native';
import { Stack, router } from 'expo-router';
import { COLORS } from '../../constants';
import { useAuth } from '../../AuthContext/AuthContext';
import { getMyMerchantRequest, submitMerchantRequest } from '../../components/services/api';

const BUSINESS_TYPES = [
  { label: 'General Products', value: 'general_products' },
  { label: 'Digital Products', value: 'digital_products' },
  { label: 'P2P Merchant', value: 'p2p_merchant' },
  { label: 'Service Provider', value: 'service_provider' },
];

export default function MerchantRequestScreen() {
  const { user } = useAuth();
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [existingRequest, setExistingRequest] = useState<any>(null);

  const [businessType, setBusinessType] = useState('general_products');
  const [businessName, setBusinessName] = useState('');
  const [businessDescription, setBusinessDescription] = useState('');
  const [address, setAddress] = useState('');
  const [city, setCity] = useState('');
  const [state, setState] = useState('');
  const [country, setCountry] = useState('');
  const [phone, setPhone] = useState(user?.phoneNumber || '');
  const [registrationNumber, setRegistrationNumber] = useState('');

  useEffect(() => {
    fetchExistingRequest();
  }, []);

  const fetchExistingRequest = async () => {
    setLoading(true);
    const res = await getMyMerchantRequest();
    if (res.success && res.data) {
      setExistingRequest(res.data);
    }
    setLoading(false);
  };

  const handleSubmit = async () => {
    if (!businessName || !address || !city || !state || !country || !phone) {
      Alert.alert('Error', 'Please fill in all required fields');
      return;
    }

    setSubmitting(true);
    const res = await submitMerchantRequest({
      businessType,
      businessName,
      businessDescription: businessDescription || undefined,
      address,
      city,
      state,
      country,
      phone,
      registrationNumber: registrationNumber || undefined,
    });

    setSubmitting(false);
    if (res.success) {
      Alert.alert('Success', 'Your merchant application has been submitted!');
      setExistingRequest(res.data);
    } else {
      Alert.alert('Error', res.message || 'Failed to submit application');
    }
  };

  if (loading) {
    return (
      <View style={styles.center}>
        <Stack.Screen options={{ title: 'Merchant Application' }} />
        <ActivityIndicator size="large" color={COLORS.primary} />
      </View>
    );
  }

  // Status card for existing request
  if (existingRequest) {
    const { status } = existingRequest;

    if (status === 'pending') {
      return (
        <View style={styles.container}>
          <Stack.Screen options={{ title: 'Merchant Application' }} />
          <View style={styles.statusCard}>
            <Text style={styles.statusEmoji}>&#9203;</Text>
            <Text style={styles.statusTitle}>Application Under Review</Text>
            <Text style={styles.statusText}>
              Your merchant application for "{existingRequest.businessName}" is being reviewed. We'll update your status soon.
            </Text>
          </View>
        </View>
      );
    }

    if (status === 'approved') {
      return (
        <View style={styles.container}>
          <Stack.Screen options={{ title: 'Merchant Application' }} />
          <View style={[styles.statusCard, { borderColor: '#22c55e' }]}>
            <Text style={styles.statusEmoji}>&#9989;</Text>
            <Text style={styles.statusTitle}>You're a Merchant!</Text>
            <Text style={styles.statusText}>
              Your application has been approved. You now have access to the merchant portal.
            </Text>
            <TouchableOpacity
              style={[styles.submitButton, { backgroundColor: '#22c55e' }]}
              onPress={() => router.push('/merchants/dashboard')}
            >
              <Text style={styles.submitButtonText}>Go to Merchant Dashboard</Text>
            </TouchableOpacity>
          </View>
        </View>
      );
    }

    if (status === 'rejected') {
      return (
        <View style={styles.container}>
          <Stack.Screen options={{ title: 'Merchant Application' }} />
          <View style={[styles.statusCard, { borderColor: '#ef4444' }]}>
            <Text style={styles.statusEmoji}>&#10060;</Text>
            <Text style={styles.statusTitle}>Application Rejected</Text>
            <Text style={styles.statusText}>
              Reason: {existingRequest.adminNote || 'No reason provided'}
            </Text>
            <TouchableOpacity
              style={styles.submitButton}
              onPress={() => {
                setExistingRequest(null);
                setBusinessName('');
                setBusinessDescription('');
                setAddress('');
                setCity('');
                setState('');
                setCountry('');
                setRegistrationNumber('');
              }}
            >
              <Text style={styles.submitButtonText}>Apply Again</Text>
            </TouchableOpacity>
          </View>
        </View>
      );
    }
  }

  // Application form
  return (
    <ScrollView style={styles.container}>
      <Stack.Screen options={{ title: 'Merchant Application' }} />
      <Text style={styles.heading}>Become a Merchant</Text>
      <Text style={styles.subheading}>Fill in your business details to apply</Text>

      <Text style={styles.label}>Business Type *</Text>
      <View style={styles.pickerRow}>
        {BUSINESS_TYPES.map((bt) => (
          <TouchableOpacity
            key={bt.value}
            style={[styles.pickerOption, businessType === bt.value && styles.pickerOptionSelected]}
            onPress={() => setBusinessType(bt.value)}
          >
            <Text style={[styles.pickerText, businessType === bt.value && styles.pickerTextSelected]}>
              {bt.label}
            </Text>
          </TouchableOpacity>
        ))}
      </View>

      <Text style={styles.label}>Business Name *</Text>
      <TextInput style={styles.input} value={businessName} onChangeText={setBusinessName} placeholder="e.g. Ojay Electronics" />

      <Text style={styles.label}>Business Description</Text>
      <TextInput style={[styles.input, { height: 80 }]} value={businessDescription} onChangeText={setBusinessDescription} placeholder="Describe your business..." multiline />

      <Text style={styles.label}>Address *</Text>
      <TextInput style={styles.input} value={address} onChangeText={setAddress} placeholder="Street address" />

      <Text style={styles.label}>City *</Text>
      <TextInput style={styles.input} value={city} onChangeText={setCity} placeholder="City" />

      <Text style={styles.label}>State *</Text>
      <TextInput style={styles.input} value={state} onChangeText={setState} placeholder="State" />

      <Text style={styles.label}>Country *</Text>
      <TextInput style={styles.input} value={country} onChangeText={setCountry} placeholder="Country" />

      <Text style={styles.label}>Phone *</Text>
      <TextInput style={styles.input} value={phone} onChangeText={setPhone} placeholder="+234..." keyboardType="phone-pad" />

      <Text style={styles.label}>Registration Number (optional)</Text>
      <TextInput style={styles.input} value={registrationNumber} onChangeText={setRegistrationNumber} placeholder="e.g. RC-123456" />

      <TouchableOpacity style={styles.submitButton} onPress={handleSubmit} disabled={submitting}>
        {submitting ? (
          <ActivityIndicator color="#fff" />
        ) : (
          <Text style={styles.submitButtonText}>Submit Application</Text>
        )}
      </TouchableOpacity>

      <View style={{ height: 40 }} />
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#f5f5f5', padding: 16 },
  center: { flex: 1, justifyContent: 'center', alignItems: 'center' },
  heading: { fontSize: 22, fontWeight: '700', color: '#1e293b', marginBottom: 4 },
  subheading: { fontSize: 14, color: '#64748b', marginBottom: 20 },
  label: { fontSize: 13, fontWeight: '600', color: '#475569', marginTop: 12, marginBottom: 4 },
  input: {
    backgroundColor: '#fff',
    borderWidth: 1,
    borderColor: '#e2e8f0',
    borderRadius: 8,
    padding: 12,
    fontSize: 14,
    color: '#1e293b',
  },
  pickerRow: { flexDirection: 'row', flexWrap: 'wrap', gap: 8 },
  pickerOption: {
    paddingHorizontal: 12,
    paddingVertical: 8,
    borderRadius: 8,
    borderWidth: 1,
    borderColor: '#e2e8f0',
    backgroundColor: '#fff',
  },
  pickerOptionSelected: { borderColor: '#9b795fff', backgroundColor: '#fef3c7' },
  pickerText: { fontSize: 13, color: '#64748b' },
  pickerTextSelected: { color: '#9b795fff', fontWeight: '600' },
  submitButton: {
    backgroundColor: '#9b795fff',
    borderRadius: 8,
    padding: 14,
    alignItems: 'center',
    marginTop: 20,
  },
  submitButtonText: { color: '#fff', fontSize: 16, fontWeight: '600' },
  statusCard: {
    backgroundColor: '#fff',
    borderRadius: 12,
    padding: 24,
    alignItems: 'center',
    borderWidth: 1,
    borderColor: '#f59e0b',
    marginTop: 40,
  },
  statusEmoji: { fontSize: 48, marginBottom: 12 },
  statusTitle: { fontSize: 20, fontWeight: '700', color: '#1e293b', marginBottom: 8 },
  statusText: { fontSize: 14, color: '#64748b', textAlign: 'center', lineHeight: 20 },
});
```

- [ ] **Step 3: Wire up AppServices button**

In `tsa-app/components/appservices/AppServices.tsx`, update the `onPress` handler. Find the `else if (index === 5)` block (around line 69-71) and add a case for index 2 after it, before the final `else` block with the alert:

After this block (line 69-71):
```typescript
              }else if (index === 5) {
                //@ts-ignore
                router.push("/sellp2p");
              }
```

Add the index 2 case:
```typescript
              else if (index === 2) {
                //@ts-ignore
                router.push("/merchants/merchant-request");
              }
```

The final `else` block with `alert("Service coming soon")` stays as the fallback after this new block.

- [ ] **Step 4: Hide merchant-request from drawer nav**

In `tsa-app/app/merchants/_layout.tsx`, add a Drawer.Screen entry to hide it from the drawer navigation:

```tsx
<Drawer.Screen
    name="merchant-request"
    options={{
        drawerLabel: 'Merchant Application',
        title: 'Merchant Application',
        drawerItemStyle: { display: 'none' },
        drawerIcon: ({ color, size }) => <Ionicons name="document-text-outline" size={size} color={color} />,
    }}
/>
```

- [ ] **Step 5: Commit**

```bash
cd tsa-app
git add app/merchants/merchant-request.tsx components/services/api.ts components/appservices/AppServices.tsx app/merchants/_layout.tsx
git commit -m "feat(app): add merchant request screen and wire up AppServices button"
```

---

## Task 9: Final Verification

- [ ] **Step 1: Run backend tests**

Run: `cd tsa-api-go && go test ./... -v -count=1`
Expected: All tests PASS.

- [ ] **Step 2: Build backend**

Run: `cd tsa-api-go && go build ./cmd/server`
Expected: Clean build.

- [ ] **Step 3: Build admin panel**

Run: `cd tsa-admin && npm run build`
Expected: Clean build.

- [ ] **Step 4: Verify mobile app compiles**

Run: `cd tsa-app && npx expo export --platform web`
Expected: No TypeScript or bundler errors.
