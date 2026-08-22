package handlers

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

func seedOrder(t *testing.T, db *gorm.DB, buyerID, sellerID, productID uuid.UUID, status string) {
	t.Helper()
	err := db.Exec(
		`INSERT INTO orders (id, buyer_id, seller_id, product_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uuid.New().String(), buyerID.String(), sellerID.String(), productID.String(),
		status, time.Now(), time.Now(),
	).Error
	if err != nil {
		t.Fatalf("seed order: %v", err)
	}
}

func reloadModeratedUser(t *testing.T, db *gorm.DB, id uuid.UUID) *models.User {
	t.Helper()
	var u models.User
	if err := db.First(&u, "id = ?", id).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	return &u
}

// --- Suspension ------------------------------------------------------------

func TestSuspendUserRequiresReason(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	target := seedUser(t, db, models.RoleMerchant)

	for _, body := range []string{`{}`, `{"reason":""}`, `{"reason":"  "}`} {
		c, w := modCtx(t, http.MethodPost, body, target.ID.String(), admin)
		h.SuspendUser(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s: expected 400, got %d (%s)", body, w.Code, w.Body.String())
		}
	}
	if got := reloadModeratedUser(t, db, target.ID).AccountStatus; got != models.AccountStatusActive {
		t.Fatalf("status changed despite rejected request: %s", got)
	}
}

func TestSuspendUserRecordsReasonAndAudit(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	target := seedUser(t, db, models.RoleMerchant)

	c, w := modCtx(t, http.MethodPost, `{"reason":"Repeated counterfeit listings"}`, target.ID.String(), admin)
	h.SuspendUser(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}

	got := reloadModeratedUser(t, db, target.ID)
	if got.AccountStatus != models.AccountStatusSuspended {
		t.Fatalf("accountStatus = %q, want suspended", got.AccountStatus)
	}
	if got.SuspensionReason != "Repeated counterfeit listings" {
		t.Fatalf("reason = %q", got.SuspensionReason)
	}
	if got.SuspendedAt == nil || got.SuspendedBy == nil || *got.SuspendedBy != admin.ID {
		t.Fatalf("audit not recorded: %+v", got)
	}
	if got.SuspendedFromStatus != models.AccountStatusActive {
		t.Fatalf("suspendedFromStatus = %q", got.SuspendedFromStatus)
	}
}

func TestUnsuspendUserRestoresPreviousStatus(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	target := seedUser(t, db, models.RoleMerchant)
	db.Model(&models.User{}).Where("id = ?", target.ID).
		Update("account_status", models.AccountStatusInactive)

	c, _ := modCtx(t, http.MethodPost, `{"reason":"spam"}`, target.ID.String(), admin)
	h.SuspendUser(c)

	c2, w2 := modCtx(t, http.MethodPost, `{}`, target.ID.String(), admin)
	h.UnsuspendUser(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w2.Code, w2.Body.String())
	}

	got := reloadModeratedUser(t, db, target.ID)
	if got.AccountStatus != models.AccountStatusInactive {
		t.Fatalf("status = %q, want inactive (must not silently reactivate)", got.AccountStatus)
	}
	if got.SuspensionReason != "" || got.SuspendedAt != nil {
		t.Fatalf("suspension metadata not cleared: %+v", got)
	}
}

func TestSuspendUserRejectsSelfAndAdmins(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	otherAdmin := seedUser(t, db, models.RoleAdmin)

	c, w := modCtx(t, http.MethodPost, `{"reason":"oops"}`, admin.ID.String(), admin)
	h.SuspendUser(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("self-suspend: expected 400, got %d", w.Code)
	}

	c2, w2 := modCtx(t, http.MethodPost, `{"reason":"rivalry"}`, otherAdmin.ID.String(), admin)
	h.SuspendUser(c2)
	if w2.Code != http.StatusForbidden {
		t.Errorf("admin-suspend: expected 403, got %d", w2.Code)
	}
	if got := reloadModeratedUser(t, db, otherAdmin.ID).AccountStatus; got != models.AccountStatusActive {
		t.Fatalf("one admin locked out another: %s", got)
	}
}

// --- The cascade: a suspended seller's listings leave the marketplace -------

func TestSuspendingSellerHidesTheirListings(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	seller := seedUser(t, db, models.RoleMerchant)
	other := seedUser(t, db, models.RoleMerchant)

	theirs := seedProduct(t, db, seller.ID, models.ProductStatusActive)
	unaffected := seedProduct(t, db, other.ID, models.ProductStatusActive)

	// Visible while the seller is active.
	c, w := modQueryCtx(t, "", nil)
	h.GetMarketplaceProducts(c)
	if !bytes.Contains(w.Body.Bytes(), []byte(theirs.ID.String())) {
		t.Fatal("listing missing from the marketplace before suspension")
	}

	c2, _ := modCtx(t, http.MethodPost, `{"reason":"fraud"}`, seller.ID.String(), admin)
	h.SuspendUser(c2)

	// Gone from browse...
	c3, w3 := modQueryCtx(t, "", nil)
	h.GetMarketplaceProducts(c3)
	body := w3.Body.Bytes()
	if bytes.Contains(body, []byte(theirs.ID.String())) {
		t.Error("suspended seller's listing still in the marketplace")
	}
	if !bytes.Contains(body, []byte(unaffected.ID.String())) {
		t.Error("an unrelated seller's listing was hidden too")
	}

	// ...and from the detail endpoint, for anyone who is not the owner.
	stranger := seedUser(t, db, models.RoleUser)
	c4, w4 := modCtx(t, http.MethodGet, "", theirs.ID.String(), stranger)
	h.GetProductByID(c4)
	if w4.Code != http.StatusNotFound {
		t.Errorf("detail: expected 404, got %d", w4.Code)
	}

	// The product itself was never touched — that is what makes it reversible.
	if got := reload(t, db, theirs.ID).Status; got != models.ProductStatusActive {
		t.Fatalf("product status was mutated to %q; the cascade must not write to products", got)
	}
}

func TestReactivatingSellerRestoresTheirListings(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	seller := seedUser(t, db, models.RoleMerchant)
	theirs := seedProduct(t, db, seller.ID, models.ProductStatusActive)

	c, _ := modCtx(t, http.MethodPost, `{"reason":"fraud"}`, seller.ID.String(), admin)
	h.SuspendUser(c)
	c2, _ := modCtx(t, http.MethodPost, `{}`, seller.ID.String(), admin)
	h.UnsuspendUser(c2)

	c3, w3 := modQueryCtx(t, "", nil)
	h.GetMarketplaceProducts(c3)
	if !bytes.Contains(w3.Body.Bytes(), []byte(theirs.ID.String())) {
		t.Fatal("listing did not come back after reactivation")
	}
}

// A merchant's own inactive listing must stay hidden after their account is
// reactivated — the cascade must not resurrect anything they had paused.
func TestReactivatingSellerDoesNotPublishPausedListings(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	seller := seedUser(t, db, models.RoleMerchant)
	paused := seedProduct(t, db, seller.ID, models.ProductStatusInactive)

	c, _ := modCtx(t, http.MethodPost, `{"reason":"fraud"}`, seller.ID.String(), admin)
	h.SuspendUser(c)
	c2, _ := modCtx(t, http.MethodPost, `{}`, seller.ID.String(), admin)
	h.UnsuspendUser(c2)

	c3, w3 := modQueryCtx(t, "", nil)
	h.GetMarketplaceProducts(c3)
	if bytes.Contains(w3.Body.Bytes(), []byte(paused.ID.String())) {
		t.Fatal("a paused listing was published by reactivating the account")
	}
}

// --- In-flight orders ------------------------------------------------------

func TestSuspendUserReportsInFlightOrders(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	seller := seedUser(t, db, models.RoleMerchant)
	buyer := seedUser(t, db, models.RoleUser)
	product := seedProduct(t, db, seller.ID, models.ProductStatusActive)

	seedOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusEscrowed)
	seedOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusShipped)
	// Settled orders need no action from the seller and must not be counted.
	seedOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusCompleted)
	seedOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusCancelled)

	c, w := modCtx(t, http.MethodPost, `{"reason":"fraud"}`, seller.ID.String(), admin)
	h.SuspendUser(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}

	data := decodeData(t, w)
	if got := data["inFlightOrders"]; got != float64(2) {
		t.Fatalf("inFlightOrders = %v, want 2", got)
	}
}

func TestAdminGetUserReturnsStats(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	seller := seedUser(t, db, models.RoleMerchant)
	buyer := seedUser(t, db, models.RoleUser)

	active := seedProduct(t, db, seller.ID, models.ProductStatusActive)
	seedProduct(t, db, seller.ID, models.ProductStatusInactive)
	seedOrder(t, db, buyer.ID, seller.ID, active.ID, models.OrderStatusEscrowed)

	c, w := modCtx(t, http.MethodGet, "", seller.ID.String(), admin)
	h.AdminGetUser(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}

	stats, _ := decodeData(t, w)["stats"].(map[string]interface{})
	for field, want := range map[string]float64{
		"products": 2, "activeProducts": 1, "ordersAsSeller": 1, "inFlightOrders": 1,
	} {
		if stats[field] != want {
			t.Errorf("stats.%s = %v, want %v", field, stats[field], want)
		}
	}
}

func TestAdminUserEndpointsRequireAdmin(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	outsider := seedUser(t, db, models.RoleUser)
	target := seedUser(t, db, models.RoleMerchant)

	c, w := modCtx(t, http.MethodGet, "", target.ID.String(), outsider)
	h.AdminGetUser(c)
	if w.Code != http.StatusForbidden {
		t.Errorf("AdminGetUser: expected 403, got %d", w.Code)
	}
	c2, w2 := modCtx(t, http.MethodPost, `{"reason":"x"}`, target.ID.String(), outsider)
	h.SuspendUser(c2)
	if w2.Code != http.StatusForbidden {
		t.Errorf("SuspendUser: expected 403, got %d", w2.Code)
	}
	c3, w3 := modCtx(t, http.MethodPost, `{}`, target.ID.String(), outsider)
	h.UnsuspendUser(c3)
	if w3.Code != http.StatusForbidden {
		t.Errorf("UnsuspendUser: expected 403, got %d", w3.Code)
	}
	_ = config.DB
}

// --- Regressions from code review ------------------------------------------

// category.product_count counts only publicly visible listings, so suspending a
// seller has to trigger a recount. Without this the count stayed too high until
// some unrelated product in the same category happened to be edited.
func TestSuspendingSellerRefreshesCategoryCounts(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	seller := seedUser(t, db, models.RoleMerchant)

	categoryID := uuid.New()
	if err := db.Exec(
		`INSERT INTO categories (id, title, product_count, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		categoryID.String(), "Electronics", 0, time.Now(), time.Now(),
	).Error; err != nil {
		t.Fatalf("seed category: %v", err)
	}

	for i := 0; i < 3; i++ {
		p := seedProduct(t, db, seller.ID, models.ProductStatusActive)
		db.Model(&models.Product{}).Where("id = ?", p.ID).Update("category_id", categoryID)
	}
	updateCategoryProductCount(categoryID)

	countNow := func() int {
		var n int
		db.Raw(`SELECT product_count FROM categories WHERE id = ?`, categoryID.String()).Scan(&n)
		return n
	}
	if countNow() != 3 {
		t.Fatalf("baseline count = %d, want 3", countNow())
	}

	c, _ := modCtx(t, http.MethodPost, `{"reason":"fraud"}`, seller.ID.String(), admin)
	h.SuspendUser(c)
	if got := countNow(); got != 0 {
		t.Errorf("after suspension count = %d, want 0 — their listings left the catalogue", got)
	}

	c2, _ := modCtx(t, http.MethodPost, `{}`, seller.ID.String(), admin)
	h.UnsuspendUser(c2)
	if got := countNow(); got != 3 {
		t.Errorf("after reactivation count = %d, want 3", got)
	}
}

// The public category page shows a count beside the listing itself; the two
// must agree about suspended sellers.
func TestPublicCategoryCountExcludesSuspendedSellers(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	admin := seedUser(t, db, models.RoleAdmin)
	seller := seedUser(t, db, models.RoleMerchant)

	categoryID := uuid.New()
	if err := db.Exec(
		`INSERT INTO categories (id, title, product_count, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		categoryID.String(), "Electronics", 0, time.Now(), time.Now(),
	).Error; err != nil {
		t.Fatalf("seed category: %v", err)
	}
	p := seedProduct(t, db, seller.ID, models.ProductStatusActive)
	db.Model(&models.Product{}).Where("id = ?", p.ID).Update("category_id", categoryID)

	var visible int64
	models.VisibleToPublic(config.DB.Model(&models.Product{}).Where("category_id = ?", categoryID)).
		Count(&visible)
	if visible != 1 {
		t.Fatalf("baseline visible = %d, want 1", visible)
	}

	c, _ := modCtx(t, http.MethodPost, `{"reason":"fraud"}`, seller.ID.String(), admin)
	h.SuspendUser(c)

	models.VisibleToPublic(config.DB.Model(&models.Product{}).Where("category_id = ?", categoryID)).
		Count(&visible)
	if visible != 0 {
		t.Fatalf("public count = %d, want 0 — it must match what the listing shows", visible)
	}
}

// GetProductByID loads the seller for the visibility check, so the verification
// flag must come from that row rather than a second query for the same user.
func TestGetProductByIDReportsSellerVerified(t *testing.T) {
	db := setupProductModerationDB(t)
	h := &Handlers{}
	seller := seedUser(t, db, models.RoleMerchant)
	db.Model(&models.User{}).Where("id = ?", seller.ID).
		Update("verification_status", models.VerificationStatusVerified)
	p := seedProduct(t, db, seller.ID, models.ProductStatusActive)

	c, w := modCtx(t, http.MethodGet, "", p.ID.String(), nil)
	h.GetProductByID(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"sellerVerified":true`)) {
		t.Fatalf("sellerVerified not reported: %s", w.Body.String())
	}
}
