package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newPayoutAdminFixture(t *testing.T) (*SellerPayoutAdminHandler, *gorm.DB, *models.SellerPayoutOrder, *models.User) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.SellerPayoutOrder{}, &models.SellerPayoutSetting{}, &models.RampLeg{}); err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE p2p_audit_logs (id TEXT PRIMARY KEY, actor_id TEXT, trade_id TEXT, ad_id TEXT, action TEXT, metadata TEXT, created_at DATETIME)`)
	db.Exec(`CREATE TABLE orders (id TEXT PRIMARY KEY, status TEXT, release_tx_hash TEXT, escrow_contract_address TEXT, updated_at DATETIME)`)
	p := &models.SellerPayoutOrder{
		ID: uuid.New(), MarketplaceOrderID: uuid.New(), SellerUserID: uuid.New(), Currency: "NGN",
		InstitutionCode: "058", InstitutionName: "GTBank", AccountNumber: "0123456789", AccountName: "ADA OBI",
		ChainKey: "sonic", Token: "USDC", ExpectedAmount: "100000000", FiatTargetKobo: 1,
		Status: models.SellerPayoutNeedsReview, ParkedFrom: models.SellerPayoutReceived, RetrySafe: true,
		FailureReason: "cannot start the bridge: kill switch",
	}
	if err := db.Create(p).Error; err != nil {
		t.Fatal(err)
	}
	orch := services.NewSellerPayoutOrchestrator(db, nil, &services.GormRampLegStore{DB: db}, nil, nil, nil, nil, nil, "", 0, 0, nil)
	h := &SellerPayoutAdminHandler{Admin: services.NewSellerPayoutAdmin(db, orch), Switch: services.NewSellerPayoutSwitch(db)}
	return h, db, p, &models.User{ID: uuid.New(), Role: models.RoleSuperAdmin}
}

func callPayoutAdmin(t *testing.T, fn gin.HandlerFunc, admin *models.User, id uuid.UUID, query string, body any) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", admin)
	if id != uuid.Nil {
		c.Params = gin.Params{{Key: "id", Value: id.String()}}
	}
	raw, _ := json.Marshal(body)
	c.Request, _ = http.NewRequest("POST", "/?"+query, bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	fn(c)
	return w
}

func TestSellerPayoutAdminHandler_QueueAndDetail(t *testing.T) {
	h, _, p, admin := newPayoutAdminFixture(t)
	w := callPayoutAdmin(t, h.List, admin, uuid.Nil, "status=needs_review", nil)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(p.ID.String())) {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if w = callPayoutAdmin(t, h.List, admin, uuid.Nil, "status=paid", nil); bytes.Contains(w.Body.Bytes(), []byte(p.ID.String())) {
		t.Fatal("the status filter must apply")
	}
	if w = callPayoutAdmin(t, h.Get, admin, p.ID, "", nil); w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("cannot start the bridge")) {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	if w = callPayoutAdmin(t, h.Get, admin, uuid.New(), "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", w.Code)
	}
}

func TestSellerPayoutAdminHandler_Actions(t *testing.T) {
	h, db, p, admin := newPayoutAdminFixture(t)

	if w := callPayoutAdmin(t, h.Retry, admin, p.ID, "", gin.H{"note": ""}); w.Code != http.StatusBadRequest {
		t.Fatalf("no note: %d", w.Code)
	}
	if w := callPayoutAdmin(t, h.Retry, admin, p.ID, "", gin.H{"note": "lane funded"}); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body)
	}
	var got models.SellerPayoutOrder
	db.First(&got, "id = ?", p.ID)
	if got.Status != models.SellerPayoutReceived {
		t.Fatalf("status %q", got.Status)
	}
	// Not parked any more: a second retry is a conflict.
	if w := callPayoutAdmin(t, h.Retry, admin, p.ID, "", gin.H{"note": "again"}); w.Code != http.StatusConflict {
		t.Fatalf("retry of a moving payout: %d", w.Code)
	}

	// An unsafe stop needs force.
	db.Model(&got).Updates(map[string]any{"status": models.SellerPayoutNeedsReview, "parked_from": models.SellerPayoutDepositing, "retry_safe": false})
	w := callPayoutAdmin(t, h.Retry, admin, p.ID, "", gin.H{"note": "looked"})
	if w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte("retry_unsafe")) {
		t.Fatalf("unsafe retry: %d %s", w.Code, w.Body)
	}
	if w := callPayoutAdmin(t, h.MarkPaid, admin, p.ID, "", gin.H{"note": "by hand", "reference": "NIP-9"}); w.Code != http.StatusOK {
		t.Fatalf("mark paid: %d %s", w.Code, w.Body)
	}
	db.First(&got, "id = ?", p.ID)
	if got.Status != models.SellerPayoutPaid {
		t.Fatalf("status %q", got.Status)
	}
}

func TestSellerPayoutAdminHandler_Switch(t *testing.T) {
	h, db, _, admin := newPayoutAdminFixture(t)
	w := callPayoutAdmin(t, h.GetSwitch, admin, uuid.Nil, "", nil)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"enabled":true`)) {
		t.Fatalf("default: %d %s", w.Code, w.Body)
	}
	if w = callPayoutAdmin(t, h.SetSwitch, admin, uuid.Nil, "", gin.H{"enabled": false}); w.Code != http.StatusBadRequest {
		t.Fatalf("a switch change needs a note: %d", w.Code)
	}
	if w = callPayoutAdmin(t, h.SetSwitch, admin, uuid.Nil, "", gin.H{"note": "x"}); w.Code != http.StatusBadRequest {
		t.Fatalf("a switch change needs a value: %d", w.Code)
	}
	if w = callPayoutAdmin(t, h.SetSwitch, admin, uuid.Nil, "", gin.H{"enabled": false, "note": "Paycrest down"}); w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	var row models.SellerPayoutSetting
	db.First(&row, "key = ?", models.SellerPayoutSettingKey)
	if row.Enabled || row.Note != "Paycrest down" {
		t.Fatalf("row = %+v", row)
	}
	if w = callPayoutAdmin(t, h.GetSwitch, admin, uuid.Nil, "", nil); !bytes.Contains(w.Body.Bytes(), []byte(`"enabled":false`)) {
		t.Fatalf("after: %s", w.Body)
	}
}
