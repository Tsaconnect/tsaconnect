package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type ownerTestBanks struct{ name string }

func (b *ownerTestBanks) ListBanks(context.Context) ([]services.NombaBank, error) { return nil, nil }
func (b *ownerTestBanks) Resolve(_ context.Context, _, acct string) (services.NombaLookupResult, error) {
	return services.NombaLookupResult{AccountNumber: acct, AccountName: b.name}, nil
}

type ownerRig struct {
	r      *gin.Engine
	db     *gorm.DB
	banks  *ownerTestBanks
	owner  *models.User
	m      *models.PayMerchant
	events func() []events.Event
}

// newOwnerRig mounts the owner routes for a signed-in owner of a naira-settled
// merchant. Her password is "correct-horse".
func newOwnerRig(t *testing.T) *ownerRig {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.PayMerchant{}, &models.PayAuditEvent{}); err != nil {
		t.Fatal(err)
	}
	// Only the column the name check reads.
	if err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, verified_legal_name TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	hashed, err := models.HashPassword("correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	owner := &models.User{ID: uuid.New(), Password: hashed}
	db.Exec(`INSERT INTO users (id, verified_legal_name) VALUES (?, '')`, owner.ID)
	m := seedHandlerMerchant(t, db)
	db.Model(m).Update("user_id", owner.ID)

	banks := &ownerTestBanks{name: "SKY AIR LIMITED"}
	now := func() time.Time { return payHandlerNow }
	merchants := services.NewPayMerchantService(db, banks, nil, []byte(strings.Repeat("k", 32)), now)
	bus := events.NewBus()
	seen := collectEvents(bus)
	h := NewPayHandler(merchants, services.NewPayPaymentService(db, merchants, 100, "https://x", now), nil, false).WithEvents(bus)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", owner) })
	r.GET("/api/pay/merchant", h.OwnerMerchant)
	r.PUT("/api/pay/merchant/settlement", h.OwnerUpdateSettlement)
	return &ownerRig{r: r, db: db, banks: banks, owner: owner, m: m, events: seen}
}

func (o *ownerRig) change(password, account string) *ownerResponse {
	w := payDo(o.r, http.MethodPut, "/api/pay/merchant/settlement", map[string]any{
		"settlementMode": "fiat", "bankCode": "044", "accountNumber": account, "password": password,
	}, nil)
	var body ownerResponse
	json.Unmarshal(w.Body.Bytes(), &body)
	body.status = w.Code
	return &body
}

type ownerResponse struct {
	status int
	Code   string          `json:"code"`
	Data   json.RawMessage `json:"data"`
}

func (o *ownerRig) stored(t *testing.T) models.PayMerchant {
	t.Helper()
	var m models.PayMerchant
	o.db.First(&m, "id = ?", o.m.ID)
	return m
}

func TestPayOwner_ChangesSettlementToAnAccountInTheBusinessNameAndIsTold(t *testing.T) {
	o := newOwnerRig(t)
	res := o.change("correct-horse", "9876543210")
	if res.status != http.StatusOK {
		t.Fatalf("status %d code %q", res.status, res.Code)
	}
	var v payOwnerMerchantView
	json.Unmarshal(res.Data, &v)
	if v.AccountNumber != "******3210" || v.AccountName != "SKY AIR LIMITED" || strings.Contains(string(res.Data), "9876543210") {
		t.Fatalf("view %s: the account number must come back masked", res.Data)
	}
	if got := o.stored(t); got.AccountNumber != "9876543210" || got.BankCode != "044" {
		t.Fatalf("stored %+v", got)
	}
	seen := o.events()
	if len(seen) != 1 || seen[0].Type != events.SecurityPaySettlementChanged || seen[0].UserID != o.owner.ID ||
		!strings.Contains(seen[0].Message, "ending 3210") {
		t.Fatalf("events %+v: the owner must be told, on every channel, where the money now goes", seen)
	}
}

func TestPayOwner_AWrongPasswordChangesNothing(t *testing.T) {
	o := newOwnerRig(t)
	for _, pw := range []string{"", "guess"} {
		if res := o.change(pw, "9876543210"); res.status != http.StatusUnauthorized || res.Code != "password_incorrect" {
			t.Fatalf("password %q: status %d code %q", pw, res.status, res.Code)
		}
	}
	if got := o.stored(t); got.AccountNumber != "0123456789" || len(o.events()) != 0 {
		t.Fatalf("stored %+v", got)
	}
}

func TestPayOwner_AnAccountInSomeoneElsesNameIsRefused(t *testing.T) {
	o := newOwnerRig(t)
	o.banks.name = "EMEKA NWOSU"
	if res := o.change("correct-horse", "9876543210"); res.status != http.StatusUnprocessableEntity || res.Code != "name_mismatch" {
		t.Fatalf("status %d code %q", res.status, res.Code)
	}
	if got := o.stored(t); got.AccountNumber != "0123456789" || len(o.events()) != 0 {
		t.Fatalf("stored %+v: a refused change landed or was announced", got)
	}
}

func TestPayOwner_ReadsOnlyTheirOwnMerchant(t *testing.T) {
	o := newOwnerRig(t)
	w := payDo(o.r, http.MethodGet, "/api/pay/merchant", nil, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accountNumber":"******6789"`) {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	o.db.Model(&models.PayMerchant{}).Where("id = ?", o.m.ID).Update("user_id", uuid.New())
	if w := payDo(o.r, http.MethodGet, "/api/pay/merchant", nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("someone else's merchant: status %d", w.Code)
	}
	if res := o.change("correct-horse", "9876543210"); res.status != http.StatusNotFound {
		t.Fatalf("changing someone else's merchant: status %d", res.status)
	}
}
