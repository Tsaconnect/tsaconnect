package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupProductModerationDB builds an in-memory SQLite DB with the products,
// users and categories tables. Raw DDL rather than AutoMigrate because the model
// carries Postgres-isms (gen_random_uuid(), jsonb).
func setupProductModerationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS products (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT,
			description TEXT,
			location TEXT,
			phone_number TEXT,
			email TEXT,
			company_name TEXT,
			price REAL,
			stock INTEGER,
			category_id TEXT,
			category_name TEXT,
			images TEXT,
			attributes TEXT,
			status TEXT DEFAULT 'active',
			type TEXT DEFAULT 'Product',
			is_featured INTEGER DEFAULT 0,
			views INTEGER DEFAULT 0,
			sales INTEGER DEFAULT 0,
			rating TEXT,
			metadata TEXT,
			shipping_residential REAL DEFAULT 0,
			shipping_same_city REAL DEFAULT 0,
			shipping_same_state REAL DEFAULT 0,
			shipping_same_country REAL DEFAULT 0,
			shipping_international REAL DEFAULT 0,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME, declared_given_names TEXT, declared_surname TEXT,
			deleted_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			name TEXT,
			username TEXT,
			email TEXT,
			password TEXT,
			role TEXT DEFAULT 'user',
			phone_number TEXT,
			country TEXT,
			state TEXT,
			city TEXT,
			address TEXT,
			profile_photo TEXT,
			referral_code TEXT,
			referred_by TEXT,
			didit_session_id TEXT,
			documents TEXT,
			facial_verification TEXT,
			verification_status TEXT DEFAULT 'pending',
			verification_notes TEXT,
			account_status TEXT DEFAULT 'active',
			last_login DATETIME,
			login_attempts INTEGER DEFAULT 0,
			lock_until DATETIME,
			wallet_address TEXT,
			seed_phrase_backed_up INTEGER DEFAULT 0,
			mute_notifications INTEGER DEFAULT 0,
			mute_email INTEGER DEFAULT 0,
			tp_balance REAL DEFAULT 0,
			cashback_balance REAL DEFAULT 0,
			last_settled_global_index INTEGER,
			email_verified INTEGER DEFAULT 0,
			deleted_at DATETIME,
			submitted_for_verification_at DATETIME,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME, declared_given_names TEXT, declared_surname TEXT,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS orders (
			id TEXT PRIMARY KEY,
			buyer_id TEXT NOT NULL,
			seller_id TEXT NOT NULL,
			product_id TEXT NOT NULL,
			status TEXT,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS categories (
			id TEXT PRIMARY KEY,
			title TEXT,
			product_count INTEGER DEFAULT 0,
			created_at DATETIME,
			updated_at DATETIME
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	config.DB = db
	return db
}

func seedProduct(t *testing.T, db *gorm.DB, ownerID uuid.UUID, status string) *models.Product {
	t.Helper()
	p := &models.Product{
		ID:        uuid.New(),
		UserID:    ownerID,
		Name:      "Test listing",
		Price:     100,
		Stock:     5,
		Status:    status,
		Type:      models.ProductTypeProduct,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("seed product: %v", err)
	}
	return p
}

func seedUser(t *testing.T, db *gorm.DB, role string) *models.User {
	t.Helper()
	u := &models.User{
		ID:       uuid.New(),
		Name:     "User",
		Username: "u_" + uuid.New().String()[:8],
		Email:    uuid.New().String()[:8] + "@example.com",
		Password: "x",
		Role:     role,
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

// modCtx builds a gin context with an optional JSON body, :id param and user.
func modCtx(t *testing.T, method, body string, productID string, user *models.User) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, _ := http.NewRequest(method, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	if productID != "" {
		c.Params = gin.Params{{Key: "id", Value: productID}}
	}
	if user != nil {
		c.Set("user", user)
	}
	return c, w
}

// modQueryCtx builds a gin context for a GET with a query string.
func modQueryCtx(t *testing.T, query string, user *models.User) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, _ := http.NewRequest(http.MethodGet, "/?"+query, nil)
	c.Request = req
	if user != nil {
		c.Set("user", user)
	}
	return c, w
}

func decodeData(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body struct {
		Success bool                   `json:"success"`
		Data    map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	return body.Data
}

func reload(t *testing.T, db *gorm.DB, id uuid.UUID) *models.Product {
	t.Helper()
	var p models.Product
	if err := db.Unscoped().First(&p, "id = ?", id).Error; err != nil {
		t.Fatalf("reload product: %v", err)
	}
	return &p
}

// --- Suspend ---------------------------------------------------------------

func TestSuspendProductRequiresReason(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	p := seedProduct(t, db, uuid.New(), models.ProductStatusActive)

	for _, body := range []string{`{}`, `{"reason":""}`, `{"reason":"   "}`} {
		c, w := modCtx(t, http.MethodPost, body, p.ID.String(), admin)
		h.SuspendProduct(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s: expected 400, got %d (%s)", body, w.Code, w.Body.String())
		}
	}
	if got := reload(t, db, p.ID).Status; got != models.ProductStatusActive {
		t.Fatalf("status changed despite rejected request: %s", got)
	}
}

func TestSuspendProductRecordsReasonAndAudit(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	p := seedProduct(t, db, uuid.New(), models.ProductStatusActive)

	c, w := modCtx(t, http.MethodPost, `{"reason":"Counterfeit branding"}`, p.ID.String(), admin)
	h.SuspendProduct(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}

	got := reload(t, db, p.ID)
	if got.Status != models.ProductStatusSuspended {
		t.Fatalf("status = %q, want suspended", got.Status)
	}
	if got.SuspensionReason != "Counterfeit branding" {
		t.Fatalf("reason = %q", got.SuspensionReason)
	}
	if got.SuspendedAt == nil {
		t.Fatal("suspendedAt not set")
	}
	if got.SuspendedBy == nil || *got.SuspendedBy != admin.ID {
		t.Fatalf("suspendedBy = %v, want %v", got.SuspendedBy, admin.ID)
	}
	if got.SuspendedFromStatus != models.ProductStatusActive {
		t.Fatalf("suspendedFromStatus = %q, want active", got.SuspendedFromStatus)
	}
}

// A listing the merchant had paused must come back paused, not published.
func TestUnsuspendRestoresPreviousStatus(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	p := seedProduct(t, db, uuid.New(), models.ProductStatusInactive)

	c, _ := modCtx(t, http.MethodPost, `{"reason":"spam"}`, p.ID.String(), admin)
	h.SuspendProduct(c)

	c2, w2 := modCtx(t, http.MethodPost, `{}`, p.ID.String(), admin)
	h.UnsuspendProduct(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w2.Code, w2.Body.String())
	}

	got := reload(t, db, p.ID)
	if got.Status != models.ProductStatusInactive {
		t.Fatalf("status = %q, want inactive (must not silently publish)", got.Status)
	}
	if got.SuspensionReason != "" || got.SuspendedAt != nil {
		t.Fatalf("suspension metadata not cleared: %+v", got)
	}
}

func TestUnsuspendRejectsProductThatIsNotSuspended(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	p := seedProduct(t, db, uuid.New(), models.ProductStatusActive)

	c, w := modCtx(t, http.MethodPost, `{}`, p.ID.String(), admin)
	h.UnsuspendProduct(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", w.Code, w.Body.String())
	}
}

// --- Merchant cannot escape a suspension -----------------------------------

func TestMerchantCannotSetSuspendedStatus(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	owner := seedUser(t, db, models.RoleMerchant)
	p := seedProduct(t, db, owner.ID, models.ProductStatusActive)

	c, w := modCtx(t, http.MethodPatch, `{"status":"suspended"}`, p.ID.String(), owner)
	h.UpdateStatus(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", w.Code, w.Body.String())
	}
	if got := reload(t, db, p.ID).Status; got != models.ProductStatusActive {
		t.Fatalf("status = %q, want active", got)
	}
}

func TestMerchantCannotUnsuspendOwnProduct(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	owner := seedUser(t, db, models.RoleMerchant)
	p := seedProduct(t, db, owner.ID, models.ProductStatusActive)

	c, _ := modCtx(t, http.MethodPost, `{"reason":"policy"}`, p.ID.String(), admin)
	h.SuspendProduct(c)

	c2, w2 := modCtx(t, http.MethodPatch, `{"status":"active"}`, p.ID.String(), owner)
	h.UpdateStatus(c2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w2.Code, w2.Body.String())
	}
	if got := reload(t, db, p.ID).Status; got != models.ProductStatusSuspended {
		t.Fatalf("merchant escaped suspension: status = %q", got)
	}
}

// --- Public visibility ------------------------------------------------------

func TestGetProductByIDHidesSuspendedFromStrangers(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	owner := seedUser(t, db, models.RoleMerchant)
	p := seedProduct(t, db, owner.ID, models.ProductStatusSuspended)

	// Anonymous caller
	c, w := modCtx(t, http.MethodGet, "", p.ID.String(), nil)
	h.GetProductByID(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("anonymous: expected 404, got %d (%s)", w.Code, w.Body.String())
	}

	// Some other logged-in user
	stranger := seedUser(t, db, models.RoleUser)
	c2, w2 := modCtx(t, http.MethodGet, "", p.ID.String(), stranger)
	h.GetProductByID(c2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("stranger: expected 404, got %d (%s)", w2.Code, w2.Body.String())
	}
}

func TestGetProductByIDAllowsOwnerAndAdminToSeeSuspended(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	owner := seedUser(t, db, models.RoleMerchant)
	admin := seedUser(t, db, models.RoleAdmin)
	p := seedProduct(t, db, owner.ID, models.ProductStatusSuspended)

	for name, u := range map[string]*models.User{"owner": owner, "admin": admin} {
		c, w := modCtx(t, http.MethodGet, "", p.ID.String(), u)
		h.GetProductByID(c)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d (%s)", name, w.Code, w.Body.String())
		}
	}
}

func TestGetProductByIDHidesSoftDeletedFromEveryone(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	owner := seedUser(t, db, models.RoleMerchant)
	admin := seedUser(t, db, models.RoleAdmin)
	p := seedProduct(t, db, owner.ID, models.ProductStatusActive)
	if err := db.Delete(&models.Product{}, "id = ?", p.ID).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	for name, u := range map[string]*models.User{"owner": owner, "admin": admin, "anon": nil} {
		c, w := modCtx(t, http.MethodGet, "", p.ID.String(), u)
		h.GetProductByID(c)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: expected 404 for deleted product, got %d", name, w.Code)
		}
	}
}

func TestMarketplaceExcludesSuspendedAndDeleted(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	owner := seedUser(t, db, models.RoleMerchant)
	visible := seedProduct(t, db, owner.ID, models.ProductStatusActive)
	suspended := seedProduct(t, db, owner.ID, models.ProductStatusSuspended)
	deleted := seedProduct(t, db, owner.ID, models.ProductStatusActive)
	db.Delete(&models.Product{}, "id = ?", deleted.ID)

	// Featured strip must apply the same rule.
	db.Model(&models.Product{}).Where("id IN ?", []uuid.UUID{suspended.ID, deleted.ID}).
		Update("is_featured", true)

	c, w := modQueryCtx(t, "", nil)
	h.GetMarketplaceProducts(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}

	data := decodeData(t, w)
	ids := map[string]bool{}
	for _, key := range []string{"products", "featuredProducts"} {
		list, _ := data[key].([]interface{})
		for _, item := range list {
			m, _ := item.(map[string]interface{})
			ids[m["id"].(string)] = true
		}
	}
	if !ids[visible.ID.String()] {
		t.Fatal("active product missing from marketplace")
	}
	if ids[suspended.ID.String()] {
		t.Fatal("suspended product leaked into marketplace")
	}
	if ids[deleted.ID.String()] {
		t.Fatal("soft-deleted product leaked into marketplace")
	}
}

// The status query param must not be a way to browse unpublished listings.
func TestCategoryListingRejectsNonPublicStatusParam(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	owner := seedUser(t, db, models.RoleMerchant)
	hidden := seedProduct(t, db, owner.ID, models.ProductStatusSuspended)
	inactive := seedProduct(t, db, owner.ID, models.ProductStatusInactive)

	for _, status := range []string{"suspended", "inactive", "pending_review"} {
		c, w := modQueryCtx(t, "status="+url.QueryEscape(status), nil)
		h.GetProductsByCategory(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%s: expected 200, got %d", status, w.Code)
		}
		body := w.Body.String()
		if bytes.Contains([]byte(body), []byte(hidden.ID.String())) {
			t.Fatalf("status=%s leaked the suspended product", status)
		}
		if bytes.Contains([]byte(body), []byte(inactive.ID.String())) {
			t.Fatalf("status=%s leaked the inactive product", status)
		}
	}
}

// --- Soft delete / restore --------------------------------------------------

func TestAdminDeleteIsSoftAndHidesFromOwner(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	owner := seedUser(t, db, models.RoleMerchant)
	p := seedProduct(t, db, owner.ID, models.ProductStatusActive)

	c, w := modCtx(t, http.MethodDelete, "", p.ID.String(), admin)
	h.AdminDeleteProduct(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}

	// Row survives...
	var count int64
	db.Unscoped().Model(&models.Product{}).Where("id = ?", p.ID).Count(&count)
	if count != 1 {
		t.Fatal("row was hard-deleted; order history would dangle")
	}
	// ...but is invisible to normal queries, including the owner's own list.
	c2, w2 := modQueryCtx(t, "", owner)
	h.GetUserProducts(c2)
	if bytes.Contains(w2.Body.Bytes(), []byte(p.ID.String())) {
		t.Fatal("deleted product still visible in owner's listings")
	}
}

func TestAdminRestoreBringsProductBack(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	owner := seedUser(t, db, models.RoleMerchant)
	p := seedProduct(t, db, owner.ID, models.ProductStatusActive)

	c, _ := modCtx(t, http.MethodDelete, "", p.ID.String(), admin)
	h.AdminDeleteProduct(c)

	c2, w2 := modCtx(t, http.MethodPost, "", p.ID.String(), admin)
	h.RestoreProduct(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w2.Code, w2.Body.String())
	}

	var found models.Product
	if err := db.First(&found, "id = ?", p.ID).Error; err != nil {
		t.Fatalf("product not restored: %v", err)
	}
}

// Cloudinary images must survive a soft delete — a restore has to bring the
// listing back intact.
func TestAdminDeleteKeepsImages(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	p := seedProduct(t, db, uuid.New(), models.ProductStatusActive)
	p.SetImages([]models.ProductImage{{ID: uuid.New(), URL: "https://cdn/x.jpg", PublicID: "products/x"}})
	db.Save(p)

	c, _ := modCtx(t, http.MethodDelete, "", p.ID.String(), admin)
	h.AdminDeleteProduct(c)

	got := reload(t, db, p.ID)
	if len(got.GetImages()) != 1 {
		t.Fatal("images were dropped on soft delete")
	}
}

// --- Admin listing ----------------------------------------------------------

func TestAdminListShowsAllStatusesAndHonoursIncludeDeleted(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	owner := seedUser(t, db, models.RoleMerchant)
	active := seedProduct(t, db, owner.ID, models.ProductStatusActive)
	suspended := seedProduct(t, db, owner.ID, models.ProductStatusSuspended)
	pending := seedProduct(t, db, owner.ID, models.ProductStatusPendingReview)
	deleted := seedProduct(t, db, owner.ID, models.ProductStatusActive)
	db.Delete(&models.Product{}, "id = ?", deleted.ID)

	c, w := modQueryCtx(t, "", admin)
	h.AdminListProducts(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	body := w.Body.Bytes()
	for name, id := range map[string]uuid.UUID{"active": active.ID, "suspended": suspended.ID, "pending": pending.ID} {
		if !bytes.Contains(body, []byte(id.String())) {
			t.Fatalf("admin list missing %s product", name)
		}
	}
	if bytes.Contains(body, []byte(deleted.ID.String())) {
		t.Fatal("deleted product shown without includeDeleted")
	}

	c2, w2 := modQueryCtx(t, "includeDeleted=true", admin)
	h.AdminListProducts(c2)
	if !bytes.Contains(w2.Body.Bytes(), []byte(deleted.ID.String())) {
		t.Fatal("includeDeleted=true did not surface the deleted product")
	}
}

func TestAdminListFiltersByStatus(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	owner := seedUser(t, db, models.RoleMerchant)
	active := seedProduct(t, db, owner.ID, models.ProductStatusActive)
	suspended := seedProduct(t, db, owner.ID, models.ProductStatusSuspended)

	c, w := modQueryCtx(t, "status=suspended", admin)
	h.AdminListProducts(c)
	body := w.Body.Bytes()
	if !bytes.Contains(body, []byte(suspended.ID.String())) {
		t.Fatal("status=suspended did not return the suspended product")
	}
	if bytes.Contains(body, []byte(active.ID.String())) {
		t.Fatal("status=suspended returned an active product")
	}
}

func TestAdminGetProductIncludesSuspensionDetailAndSeller(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	owner := seedUser(t, db, models.RoleMerchant)
	p := seedProduct(t, db, owner.ID, models.ProductStatusActive)

	c, _ := modCtx(t, http.MethodPost, `{"reason":"Prohibited item"}`, p.ID.String(), admin)
	h.SuspendProduct(c)

	c2, w2 := modCtx(t, http.MethodGet, "", p.ID.String(), admin)
	h.AdminGetProduct(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w2.Code, w2.Body.String())
	}
	data := decodeData(t, w2)
	product, _ := data["product"].(map[string]interface{})
	if product["suspensionReason"] != "Prohibited item" {
		t.Fatalf("suspensionReason missing: %v", data)
	}
	seller, _ := data["seller"].(map[string]interface{})
	if seller == nil || seller["id"] != owner.ID.String() {
		t.Fatalf("seller not included: %v", data)
	}
}

func TestAdminGetProductFindsSoftDeleted(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	p := seedProduct(t, db, uuid.New(), models.ProductStatusActive)
	db.Delete(&models.Product{}, "id = ?", p.ID)

	c, w := modCtx(t, http.MethodGet, "", p.ID.String(), admin)
	h.AdminGetProduct(c)
	if w.Code != http.StatusOK {
		t.Fatalf("admin cannot open a deleted product to restore it: %d", w.Code)
	}
	product, _ := decodeData(t, w)["product"].(map[string]interface{})
	if product["isDeleted"] != true {
		t.Fatalf("product not flagged as deleted: %v", product["isDeleted"])
	}
}

// --- Regressions from code review ------------------------------------------

// Adding gorm.DeletedAt turned the merchant's own delete into a soft delete, but
// it was still destroying the Cloudinary images first — so an admin could
// "restore" a listing whose images were permanently gone.
func TestMerchantDeleteIsSoftAndKeepsImages(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	owner := seedUser(t, db, models.RoleMerchant)
	p := seedProduct(t, db, owner.ID, models.ProductStatusActive)
	p.SetImages([]models.ProductImage{{ID: uuid.New(), URL: "https://cdn/x.jpg", PublicID: "products/x"}})
	db.Save(p)

	c, w := modCtx(t, http.MethodDelete, "", p.ID.String(), owner)
	h.DeleteProduct(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}

	got := reload(t, db, p.ID)
	if !got.DeletedAt.Valid {
		t.Fatal("merchant delete did not soft-delete the row")
	}
	if len(got.GetImages()) != 1 {
		t.Fatal("images dropped, so an admin restore would return a product with dead image URLs")
	}
}

// Deleting a suspended listing would hide it from the admin's default view while
// the moderation decision is still open.
func TestMerchantCannotDeleteSuspendedProduct(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	owner := seedUser(t, db, models.RoleMerchant)
	p := seedProduct(t, db, owner.ID, models.ProductStatusActive)

	c, _ := modCtx(t, http.MethodPost, `{"reason":"policy"}`, p.ID.String(), admin)
	h.SuspendProduct(c)

	c2, w2 := modCtx(t, http.MethodDelete, "", p.ID.String(), owner)
	h.DeleteProduct(c2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w2.Code, w2.Body.String())
	}
	if reload(t, db, p.ID).DeletedAt.Valid {
		t.Fatal("merchant deleted a suspended listing out of the admin's view")
	}
}

// includeDeleted widens the query to deleted AND live rows, so every row must
// say which it is. Without the flag the console offered Restore on live
// products, which the API rejects with 400.
func TestAdminListFlagsDeletedRowsIndividually(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	owner := seedUser(t, db, models.RoleMerchant)
	live := seedProduct(t, db, owner.ID, models.ProductStatusActive)
	gone := seedProduct(t, db, owner.ID, models.ProductStatusActive)
	db.Delete(&models.Product{}, "id = ?", gone.ID)

	c, w := modQueryCtx(t, "includeDeleted=true", admin)
	h.AdminListProducts(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	flags := map[string]bool{}
	data := decodeData(t, w)
	list, _ := data["products"].([]interface{})
	for _, item := range list {
		m, _ := item.(map[string]interface{})
		flags[m["id"].(string)], _ = m["isDeleted"].(bool)
	}

	if len(list) != 2 {
		t.Fatalf("expected both rows, got %d", len(list))
	}
	if flags[gone.ID.String()] != true {
		t.Error("deleted row not flagged isDeleted")
	}
	if flags[live.ID.String()] != false {
		t.Error("live row flagged as deleted — console would offer Restore on it")
	}
}

func TestAdminReadsRequireAdminRole(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	outsider := seedUser(t, db, models.RoleUser)
	p := seedProduct(t, db, uuid.New(), models.ProductStatusActive)

	c, w := modQueryCtx(t, "", outsider)
	h.AdminListProducts(c)
	if w.Code != http.StatusForbidden {
		t.Errorf("AdminListProducts: expected 403, got %d", w.Code)
	}

	c2, w2 := modCtx(t, http.MethodGet, "", p.ID.String(), outsider)
	h.AdminGetProduct(c2)
	if w2.Code != http.StatusForbidden {
		t.Errorf("AdminGetProduct: expected 403, got %d", w2.Code)
	}
}
