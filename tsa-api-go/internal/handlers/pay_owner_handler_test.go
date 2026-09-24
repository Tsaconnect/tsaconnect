package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
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

type ownerTestBanks struct {
	name string
	err  error
}

func (b *ownerTestBanks) ListBanks(context.Context) ([]services.NombaBank, error) { return nil, nil }
func (b *ownerTestBanks) Resolve(_ context.Context, _, acct string) (services.NombaLookupResult, error) {
	return services.NombaLookupResult{AccountNumber: acct, AccountName: b.name}, b.err
}

// ownerTestChains lists USDC on Base, the one stablecoin these tests pay in.
type ownerTestChains struct{}

func (ownerTestChains) MainnetChainNameForID(id int64) (string, bool) { return "base", id == 8453 }
func (ownerTestChains) TokenAddress(chain, symbol string) string {
	if chain == "base" && symbol == "USDC" {
		return ownerUSDC
	}
	return ""
}

type ownerTestMailer struct{ message string }

func (m *ownerTestMailer) Send(_, _, _, _, message, _, _ string) error {
	m.message = message
	return nil
}

const (
	ownerUSDC   = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
	ownerWallet = "0x52908400098527886E0F7030069857D2E4169EE7"
)

type ownerRig struct {
	r      *gin.Engine
	db     *gorm.DB
	banks  *ownerTestBanks
	mail   *ownerTestMailer
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
	if err := db.AutoMigrate(&models.PayMerchant{}, &models.PayAuditEvent{}, &models.PaySettlementConfirmation{}); err != nil {
		t.Fatal(err)
	}
	// The columns the owner routes read and the lockout writes.
	if err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, name TEXT, email TEXT, verification_status TEXT,
		verified_legal_name TEXT, login_attempts INTEGER DEFAULT 0, lock_until DATETIME, updated_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	prev := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = prev })

	hashed, err := models.HashPassword("correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	owner := &models.User{ID: uuid.New(), Password: hashed}
	db.Exec(`INSERT INTO users (id, name, email, verification_status) VALUES (?, 'Ada', 'owner@skyair.ng', 'pending')`, owner.ID)
	m := seedHandlerMerchant(t, db)
	db.Model(m).Update("user_id", owner.ID)
	m.UserID = owner.ID

	banks := &ownerTestBanks{name: "SKY AIR LIMITED"}
	mail := &ownerTestMailer{}
	now := func() time.Time { return payHandlerNow }
	merchants := services.NewPayMerchantService(db, banks, ownerTestChains{}, []byte(strings.Repeat("k", 32)), now)
	merchants.SetMailer(mail)
	bus := events.NewBus()
	seen := collectEvents(bus)
	h := NewPayHandler(merchants, services.NewPayPaymentService(db, merchants, 100, "https://x", now), nil, false).WithEvents(bus)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	// The session's own copy of the user, as the auth middleware would load it.
	r.Use(func(c *gin.Context) {
		var u models.User
		db.Raw(`SELECT id, login_attempts, lock_until FROM users WHERE id = ?`, owner.ID).Scan(&u)
		u.Password = owner.Password
		c.Set("user", &u)
	})
	r.GET("/api/pay/merchant", h.OwnerMerchant)
	r.PUT("/api/pay/merchant/settlement", h.OwnerUpdateSettlement)
	r.POST("/api/pay/merchant/settlement/confirm", h.OwnerConfirmSettlement)
	return &ownerRig{r: r, db: db, banks: banks, mail: mail, owner: owner, m: m, events: seen}
}

type ownerResponse struct {
	status int
	raw    string
	Code   string `json:"code"`
	Data   struct {
		Merchant     *payOwnerMerchantView          `json:"merchant"`
		Confirmation *services.PaySettlementPending `json:"confirmation"`
	} `json:"data"`
}

func (o *ownerRig) do(method, path string, body map[string]any) *ownerResponse {
	w := payDo(o.r, method, path, body, nil)
	res := &ownerResponse{status: w.Code, raw: w.Body.String()}
	json.Unmarshal(w.Body.Bytes(), res)
	return res
}

func (o *ownerRig) bankChange(password, account string) *ownerResponse {
	return o.do(http.MethodPut, "/api/pay/merchant/settlement", map[string]any{
		"settlementMode": "fiat", "bankCode": "044", "accountNumber": account, "password": password,
	})
}

func (o *ownerRig) stored(t *testing.T) models.PayMerchant {
	t.Helper()
	var m models.PayMerchant
	o.db.First(&m, "id = ?", o.m.ID)
	return m
}

func TestPayOwner_ChangesSettlementToAnAccountInTheBusinessNameAndIsTold(t *testing.T) {
	o := newOwnerRig(t)
	res := o.bankChange("correct-horse", "9876543210")
	if res.status != http.StatusOK || res.Data.Merchant == nil {
		t.Fatalf("status %d body %s", res.status, res.raw)
	}
	if v := res.Data.Merchant; v.AccountNumber != "******3210" || v.AccountName != "SKY AIR LIMITED" || strings.Contains(res.raw, "9876543210") {
		t.Fatalf("view %s: the account number must come back masked", res.raw)
	}
	if got := o.stored(t); got.AccountNumber != "9876543210" || got.BankCode != "044" {
		t.Fatalf("stored %+v", got)
	}
	seen := o.events()
	if len(seen) != 1 || seen[0].Type != events.SecurityPaySettlementChanged || seen[0].UserID != o.owner.ID ||
		!strings.Contains(seen[0].Message, "******3210") {
		t.Fatalf("events %+v: the owner must be told, on every channel, where the money now goes", seen)
	}
}

func TestPayOwner_WrongPasswordsCountTowardTheLoginLockout(t *testing.T) {
	o := newOwnerRig(t)
	if res := o.bankChange("", "9876543210"); res.status != http.StatusUnauthorized || res.Code != "password_incorrect" {
		t.Fatalf("empty password: status %d code %q", res.status, res.Code)
	}
	for i := 1; i < 5; i++ {
		o.bankChange("guess", "9876543210")
	}
	var attempts int
	o.db.Raw(`SELECT login_attempts FROM users WHERE id = ?`, o.owner.ID).Scan(&attempts)
	if attempts != 5 {
		t.Fatalf("login_attempts = %d: guesses here must reach the lockout", attempts)
	}
	if res := o.bankChange("correct-horse", "9876543210"); res.status != http.StatusLocked || res.Code != "account_locked" {
		t.Fatalf("the right password on a locked account: status %d code %q", res.status, res.Code)
	}
	if got := o.stored(t); got.AccountNumber != "0123456789" || len(o.events()) == 0 || o.events()[0].Type != events.SecurityAccountLocked {
		t.Fatalf("stored %+v events %+v", got, o.events())
	}
}

func TestPayOwner_AnAccountInSomeoneElsesNameIsRefused(t *testing.T) {
	o := newOwnerRig(t)
	o.banks.name = "SKY AIR CARGO LIMITED" // contains the business's words, but is not the business
	if res := o.bankChange("correct-horse", "9876543210"); res.status != http.StatusUnprocessableEntity || res.Code != "name_mismatch" {
		t.Fatalf("status %d code %q", res.status, res.Code)
	}
	if got := o.stored(t); got.AccountNumber != "0123456789" || len(o.events()) != 0 {
		t.Fatalf("stored %+v: a refused change landed or was announced", got)
	}
}

func TestPayOwner_TheBankProvidersOwnWordsAreNeverShown(t *testing.T) {
	o := newOwnerRig(t)
	o.banks.err = errors.New("nomba upstream: token eyJhbGciOi expired")
	res := o.bankChange("correct-horse", "9876543210")
	if res.status != http.StatusUnprocessableEntity || res.Code != "account_unverifiable" || strings.Contains(res.raw, "nomba") {
		t.Fatalf("status %d body %s", res.status, res.raw)
	}
}

func TestPayOwner_AWalletWaitsForTheEmailedCodeAndIsToldOnlyThen(t *testing.T) {
	o := newOwnerRig(t)
	res := o.do(http.MethodPut, "/api/pay/merchant/settlement", map[string]any{
		"settlementMode": "crypto", "chainId": 8453, "tokenAddress": ownerUSDC, "tokenSymbol": "SCAM", "walletAddress": ownerWallet,
		"password": "correct-horse",
	})
	if res.status != http.StatusAccepted || res.Data.Confirmation == nil || res.Data.Confirmation.SentTo != "o***@skyair.ng" {
		t.Fatalf("status %d body %s", res.status, res.raw)
	}
	if got := o.stored(t); got.SettlementMode == models.PaySettlementCrypto || len(o.events()) != 0 {
		t.Fatalf("stored %+v: the wallet was applied or announced before the code", got)
	}
	code := regexp.MustCompile(`\b\d{6}\b`).FindString(o.mail.message)
	if bad := o.do(http.MethodPost, "/api/pay/merchant/settlement/confirm", map[string]any{"code": "abc"}); bad.status != http.StatusUnprocessableEntity || bad.Code != "code_invalid" {
		t.Fatalf("a wrong code: status %d code %q", bad.status, bad.Code)
	}
	ok := o.do(http.MethodPost, "/api/pay/merchant/settlement/confirm", map[string]any{"code": code})
	if ok.status != http.StatusOK || ok.Data.Merchant == nil || ok.Data.Merchant.TokenSymbol != "USDC" || ok.Data.Merchant.WalletAddress != ownerWallet {
		t.Fatalf("status %d body %s: the symbol is the listing's, never the caller's", ok.status, ok.raw)
	}
	seen := o.events()
	if len(seen) != 1 || !strings.Contains(seen[0].Message, "0x5290…9EE7") {
		t.Fatalf("events %+v: the wallet must be named by its start and its end", seen)
	}
}

func TestPayOwner_OnlyTheSettlementCanChange(t *testing.T) {
	o := newOwnerRig(t)
	res := o.do(http.MethodPut, "/api/pay/merchant/settlement", map[string]any{
		"settlementMode": "fiat", "bankCode": "044", "accountNumber": "9876543210", "password": "correct-horse",
		"businessName": "Someone Else Ltd", "status": "active", "webhookUrl": "https://evil.example/hook",
		"returnDomains": []string{"evil.example"}, "minAmount": 1, "maxAmount": 1, "customerFeeSharePercent": 0,
	})
	if res.status != http.StatusOK {
		t.Fatalf("status %d body %s", res.status, res.raw)
	}
	got := o.stored(t)
	if got.BusinessName != o.m.BusinessName || got.WebhookURL != o.m.WebhookURL || got.ReturnDomains != o.m.ReturnDomains ||
		got.MinAmountKobo != o.m.MinAmountKobo || got.MaxAmountKobo != o.m.MaxAmountKobo || got.CustomerFeeSharePercent != o.m.CustomerFeeSharePercent {
		t.Fatalf("stored %+v: an owner changed more than where they are paid", got)
	}
}

func TestPayOwner_ChangingToWhereTheyArePaidAlreadyIsNotAnnounced(t *testing.T) {
	o := newOwnerRig(t)
	o.banks.name = "SKY AIR LTD"
	res := o.do(http.MethodPut, "/api/pay/merchant/settlement", map[string]any{
		"settlementMode": "fiat", "bankCode": o.m.BankCode, "accountNumber": o.m.AccountNumber, "password": "correct-horse",
	})
	if res.status != http.StatusOK || len(o.events()) != 0 || o.stored(t).Version != o.m.Version {
		t.Fatalf("status %d events %d", res.status, len(o.events()))
	}
}

func TestPayOwner_ReadsOnlyTheirOwnMerchant(t *testing.T) {
	o := newOwnerRig(t)
	res := o.do(http.MethodGet, "/api/pay/merchant", nil)
	if res.status != http.StatusOK || !strings.Contains(res.raw, `"accountNumber":"******6789"`) || !strings.Contains(res.raw, `"version":`) {
		t.Fatalf("status %d body %s", res.status, res.raw)
	}
	o.db.Model(&models.PayMerchant{}).Where("id = ?", o.m.ID).Update("user_id", uuid.New())
	if res := o.do(http.MethodGet, "/api/pay/merchant", nil); res.status != http.StatusNotFound || res.Code != "no_merchant" {
		t.Fatalf("someone else's merchant: status %d code %q", res.status, res.Code)
	}
	if res := o.bankChange("correct-horse", "9876543210"); res.status != http.StatusNotFound {
		t.Fatalf("changing someone else's merchant: status %d", res.status)
	}
}
