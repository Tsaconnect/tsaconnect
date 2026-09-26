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
	name  string
	err   error
	banks []services.NombaBank
	calls int
}

func (b *ownerTestBanks) ListBanks(context.Context) ([]services.NombaBank, error) {
	return b.banks, nil
}
func (b *ownerTestBanks) Resolve(_ context.Context, _, acct string) (services.NombaLookupResult, error) {
	b.calls++
	return services.NombaLookupResult{AccountNumber: acct, AccountName: b.name}, b.err
}

// ownerTestChains lists USDC on Base, the one stablecoin these tests pay in.
type ownerTestChains struct{}

func (ownerTestChains) MainnetChainNameForID(id int64) (string, bool) { return "base", id == 8453 }
func (ownerTestChains) MainnetChainIDs() []int64                      { return []int64{8453} }
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
	r        *gin.Engine
	db       *gorm.DB
	banks    *ownerTestBanks
	mail     *ownerTestMailer
	owner    *models.User
	m        *models.PayMerchant
	payments *services.PayPaymentService
	events   func() []events.Event
}

// newOwnerRig mounts the owner routes for a signed-in owner of a naira-settled
// merchant. Her password is "correct-horse".
func newOwnerRig(t *testing.T) *ownerRig {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.PayMerchant{}, &models.PayAuditEvent{}, &models.PayOwnerAction{},
		&models.PayPayment{}, &models.PayWebhookDelivery{}); err != nil {
		t.Fatal(err)
	}
	// The columns the owner routes read and the lockout writes.
	if err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, name TEXT, email TEXT, verification_status TEXT,
		verified_legal_name TEXT, account_status TEXT DEFAULT 'active', login_attempts INTEGER DEFAULT 0, lock_until DATETIME, updated_at DATETIME)`).Error; err != nil {
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

	banks := &ownerTestBanks{name: "SKY AIR LIMITED", banks: []services.NombaBank{{Code: "044", Name: "Access Bank"}}}
	mail := &ownerTestMailer{}
	now := func() time.Time { return payHandlerNow }
	merchants := services.NewPayMerchantService(db, banks, ownerTestChains{}, []byte(strings.Repeat("k", 32)), now)
	merchants.SetMailer(mail)
	bus := events.NewBus()
	seen := collectEvents(bus)
	payments := services.NewPayPaymentService(db, merchants, 100, "https://x", now)
	h := NewPayHandler(merchants, payments, nil, false).WithEvents(bus)

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
	r.POST("/api/pay/merchant/actions/:id/confirm", h.OwnerConfirmAction)
	r.GET("/api/pay/merchant/payments", h.OwnerPayments)
	r.GET("/api/pay/merchant/payments/:id", h.OwnerPayment)
	r.POST("/api/pay/merchant/keys", h.OwnerRequestKeys)
	r.PUT("/api/pay/merchant/endpoints", h.OwnerRequestEndpoints)
	r.POST("/api/pay/merchant/webhook-secret", h.OwnerRequestWebhookSecret)
	r.GET("/api/pay/merchant/banks", h.OwnerBanks)
	r.GET("/api/pay/merchant/banks/resolve", h.OwnerResolveBank)
	r.GET("/api/pay/merchant/settlement-options", h.OwnerSettlementOptions)
	return &ownerRig{r: r, db: db, banks: banks, mail: mail, owner: owner, m: m, payments: payments, events: seen}
}

type ownerResponse struct {
	status int
	raw    string
	Code   string `json:"code"`
	Data   struct {
		Merchant     *payOwnerMerchantView           `json:"merchant"`
		Confirmation *services.PayOwnerActionPending `json:"confirmation"`
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

func TestPayOwner_ABankAccountWaitsForTheEmailedCodeAndIsToldOnlyThen(t *testing.T) {
	o := newOwnerRig(t)
	res := o.bankChange("correct-horse", "9876543210")
	if res.status != http.StatusAccepted || res.Data.Confirmation == nil || res.Data.Merchant != nil ||
		res.Data.Confirmation.Kind != models.PayOwnerActionSettlement || res.Data.Confirmation.SentTo != "o***@skyair.ng" {
		t.Fatalf("status %d body %s", res.status, res.raw)
	}
	if got := o.stored(t); got.AccountNumber != "0123456789" || len(o.events()) != 0 {
		t.Fatalf("stored %+v: the bank account was applied or announced before the code", got)
	}
	confirm := "/api/pay/merchant/actions/" + res.Data.Confirmation.ID.String() + "/confirm"
	if bad := o.do(http.MethodPost, confirm, map[string]any{"code": "abc"}); bad.status != http.StatusUnprocessableEntity || bad.Code != "code_invalid" {
		t.Fatalf("a wrong code: status %d code %q", bad.status, bad.Code)
	}
	if got := o.stored(t); got.AccountNumber != "0123456789" {
		t.Fatalf("stored %+v: a wrong code applied the bank account", got)
	}
	ok := o.confirm(t, res)
	if ok.status != http.StatusOK || ok.Data.Merchant == nil {
		t.Fatalf("status %d body %s", ok.status, ok.raw)
	}
	if v := ok.Data.Merchant; v.AccountNumber != "******3210" || v.AccountName != "SKY AIR LIMITED" || strings.Contains(ok.raw, "9876543210") {
		t.Fatalf("view %s: the account number must come back masked", ok.raw)
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
	if got := o.stored(t); got.AccountNumber != "0123456789" || len(o.events()) != 0 || o.mail.message != "" {
		t.Fatalf("stored %+v: a refused change landed, was announced or sent a code", got)
	}
}

func TestPayOwner_TheBankProvidersOwnWordsAreNeverShown(t *testing.T) {
	o := newOwnerRig(t)
	o.banks.err = errors.New("nomba upstream: token eyJhbGciOi expired")
	res := o.bankChange("correct-horse", "9876543210")
	if res.status != http.StatusUnprocessableEntity || res.Code != "account_unverifiable" || strings.Contains(res.raw, "nomba") {
		t.Fatalf("status %d body %s", res.status, res.raw)
	}
	if o.mail.message != "" {
		t.Fatal("an unverifiable account sent a code")
	}
}

func TestPayOwner_AWalletWaitsForTheEmailedCodeAndIsToldOnlyThen(t *testing.T) {
	o := newOwnerRig(t)
	res := o.do(http.MethodPut, "/api/pay/merchant/settlement", map[string]any{
		"settlementMode": "crypto", "chainId": 8453, "tokenAddress": ownerUSDC, "tokenSymbol": "SCAM", "walletAddress": ownerWallet,
		"password": "correct-horse",
	})
	if res.status != http.StatusAccepted || res.Data.Confirmation == nil || res.Data.Confirmation.SentTo != "o***@skyair.ng" ||
		res.Data.Confirmation.Kind != models.PayOwnerActionSettlement {
		t.Fatalf("status %d body %s", res.status, res.raw)
	}
	if got := o.stored(t); got.SettlementMode == models.PaySettlementCrypto || len(o.events()) != 0 {
		t.Fatalf("stored %+v: the wallet was applied or announced before the code", got)
	}
	code := regexp.MustCompile(`\b\d{6}\b`).FindString(o.mail.message)
	confirm := "/api/pay/merchant/actions/" + res.Data.Confirmation.ID.String() + "/confirm"
	if bad := o.do(http.MethodPost, confirm, map[string]any{"code": "abc"}); bad.status != http.StatusUnprocessableEntity || bad.Code != "code_invalid" {
		t.Fatalf("a wrong code: status %d code %q", bad.status, bad.Code)
	}
	ok := o.do(http.MethodPost, confirm, map[string]any{"code": code})
	if ok.status != http.StatusOK || ok.Data.Merchant == nil || ok.Data.Merchant.TokenSymbol != "USDC" || ok.Data.Merchant.WalletAddress != ownerWallet {
		t.Fatalf("status %d body %s: the symbol is the listing's, never the caller's", ok.status, ok.raw)
	}
	seen := o.events()
	if len(seen) != 1 || !strings.Contains(seen[0].Message, "0x5290…9EE7") {
		t.Fatalf("events %+v: the wallet must be named by its start and its end", seen)
	}
	if again := o.do(http.MethodPost, confirm, map[string]any{"code": code}); again.status != http.StatusGone || again.Code != "no_pending_change" {
		t.Fatalf("a used code: status %d code %q", again.status, again.Code)
	}
	if bad := o.do(http.MethodPost, "/api/pay/merchant/actions/not-a-uuid/confirm", map[string]any{"code": code}); bad.status != http.StatusGone {
		t.Fatalf("a malformed action id: status %d", bad.status)
	}
}

func TestPayOwner_OnlyTheSettlementCanChange(t *testing.T) {
	o := newOwnerRig(t)
	res := o.do(http.MethodPut, "/api/pay/merchant/settlement", map[string]any{
		"settlementMode": "fiat", "bankCode": "044", "accountNumber": "9876543210", "password": "correct-horse",
		"businessName": "Someone Else Ltd", "status": "active", "webhookUrl": "https://evil.example/hook",
		"returnDomains": []string{"evil.example"}, "minAmount": 1, "maxAmount": 1, "customerFeeSharePercent": 0,
		"liveEnabled": true,
	})
	if ok := o.confirm(t, res); ok.status != http.StatusOK {
		t.Fatalf("status %d body %s", ok.status, ok.raw)
	}
	got := o.stored(t)
	if got.AccountNumber != "9876543210" {
		t.Fatalf("stored %+v: the settlement itself did not change", got)
	}
	if got.BusinessName != o.m.BusinessName || got.WebhookURL != o.m.WebhookURL || got.ReturnDomains != o.m.ReturnDomains ||
		got.MinAmountKobo != o.m.MinAmountKobo || got.MaxAmountKobo != o.m.MaxAmountKobo || got.CustomerFeeSharePercent != o.m.CustomerFeeSharePercent {
		t.Fatalf("stored %+v: an owner changed more than where they are paid", got)
	}
	if got.LiveEnabled {
		t.Fatal("an owner switched their own merchant live")
	}
}

func TestPayOwner_ChangingToWhereTheyArePaidAlreadyIsNotAnnounced(t *testing.T) {
	o := newOwnerRig(t)
	o.banks.name = "SKY AIR LTD"
	res := o.do(http.MethodPut, "/api/pay/merchant/settlement", map[string]any{
		"settlementMode": "fiat", "bankCode": o.m.BankCode, "accountNumber": o.m.AccountNumber, "password": "correct-horse",
	})
	if res.status != http.StatusOK || len(o.events()) != 0 || o.stored(t).Version != o.m.Version || o.mail.message != "" {
		t.Fatalf("status %d events %d: a no-op must write, announce and email nothing", res.status, len(o.events()))
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

// code is the six digits in the last email the owner was sent.
func (o *ownerRig) code(t *testing.T) string {
	t.Helper()
	code := regexp.MustCompile(`\b\d{6}\b`).FindString(o.mail.message)
	if code == "" {
		t.Fatalf("no code in %q", o.mail.message)
	}
	return code
}

func (o *ownerRig) confirm(t *testing.T, res *ownerResponse) *ownerResponse {
	t.Helper()
	if res.status != http.StatusAccepted || res.Data.Confirmation == nil {
		t.Fatalf("request: status %d body %s", res.status, res.raw)
	}
	return o.do(http.MethodPost, "/api/pay/merchant/actions/"+res.Data.Confirmation.ID.String()+"/confirm", map[string]any{"code": o.code(t)})
}

func TestPayOwner_ListsOnlyTheirOwnPaymentsInTheModeAsked(t *testing.T) {
	o := newOwnerRig(t)
	ctx := context.Background()
	in := services.CreatePayPaymentInput{AmountKobo: 5_000_000, Currency: "NGN", Reference: "PNR-OWN", Description: "LOS-ABV"}
	mine, _, err := o.payments.Create(ctx, o.m, models.PayModeLive, in)
	if err != nil {
		t.Fatal(err)
	}
	test := in
	test.Reference = "PNR-TEST"
	if _, _, err := o.payments.Create(ctx, o.m, models.PayModeTest, test); err != nil {
		t.Fatal(err)
	}
	stranger := seedHandlerMerchant(t, o.db)
	if _, _, err := o.payments.Create(ctx, stranger, models.PayModeLive, in); err != nil {
		t.Fatal(err)
	}

	res := o.do(http.MethodGet, "/api/pay/merchant/payments?mode=live", nil)
	if res.status != http.StatusOK || !strings.Contains(res.raw, `"total":1`) || !strings.Contains(res.raw, mine.ID.String()) {
		t.Fatalf("status %d body %s", res.status, res.raw)
	}
	if res := o.do(http.MethodGet, "/api/pay/merchant/payments?mode=nope", nil); res.status != http.StatusBadRequest {
		t.Fatalf("unknown mode: %d", res.status)
	}
	const payload = `{"event":"payment.succeeded","secret-looking":"do-not-echo-8f3a"}`
	delivery := models.PayWebhookDelivery{PaymentID: mine.ID, MerchantID: o.m.ID, Mode: models.PayModeLive, Event: "payment.succeeded",
		Payload: payload, Attempts: 2, LastStatusCode: 500, LastError: "HTTP 500"}
	if err := o.db.Create(&delivery).Error; err != nil {
		t.Fatal(err)
	}
	detail := o.do(http.MethodGet, "/api/pay/merchant/payments/"+mine.ID.String()+"?mode=live", nil)
	if detail.status != http.StatusOK || !strings.Contains(detail.raw, `"id":"`+delivery.ID.String()+`"`) ||
		!strings.Contains(detail.raw, `"attempts":2`) || !strings.Contains(detail.raw, `"lastStatusCode":500`) {
		t.Fatalf("detail %d %s", detail.status, detail.raw)
	}
	for _, never := range []string{"do-not-echo-8f3a", `"payload"`, `"merchantId"`} {
		if strings.Contains(detail.raw, never) {
			t.Fatalf("detail leaked %s: %s", never, detail.raw)
		}
	}
	if res := o.do(http.MethodGet, "/api/pay/merchant/payments/"+mine.ID.String()+"?mode=test", nil); res.status != http.StatusNotFound {
		t.Fatalf("a live payment read as test: %d", res.status)
	}
	if res := o.do(http.MethodGet, "/api/pay/merchant/payments/not-an-id", nil); res.status != http.StatusNotFound {
		t.Fatalf("a malformed id: %d", res.status)
	}
}

func TestPayOwner_PaymentsEchoTheEffectivePage(t *testing.T) {
	o := newOwnerRig(t)
	for query, want := range map[string][2]string{
		"?limit=500":        {`"page":1`, `"limit":50`},
		"?limit=0&page=-3":  {`"page":1`, `"limit":20`},
		"?limit=abc&page=2": {`"page":2`, `"limit":20`},
		"":                  {`"page":1`, `"limit":20`},
	} {
		res := o.do(http.MethodGet, "/api/pay/merchant/payments"+query, nil)
		if res.status != http.StatusOK || !strings.Contains(res.raw, want[0]) || !strings.Contains(res.raw, want[1]) || !strings.Contains(res.raw, `"items":[]`) {
			t.Fatalf("%q: status %d body %s, want %v", query, res.status, res.raw, want)
		}
	}
}

func TestPayOwner_KeyRollNeedsThePasswordThenTheCodeAndIsAnnounced(t *testing.T) {
	o := newOwnerRig(t)
	if res := o.do(http.MethodPost, "/api/pay/merchant/keys", map[string]any{"mode": "live", "password": "guess"}); res.status != http.StatusUnauthorized || res.Code != "password_incorrect" {
		t.Fatalf("wrong password: %d %s", res.status, res.raw)
	}
	if o.mail.message != "" {
		t.Fatal("a code was sent for a wrong password")
	}
	res := o.do(http.MethodPost, "/api/pay/merchant/keys", map[string]any{"mode": "live", "password": "correct-horse"})
	if res.status != http.StatusAccepted || res.Data.Confirmation == nil || res.Data.Confirmation.Kind != "keys" {
		t.Fatalf("status %d body %s", res.status, res.raw)
	}
	ok := o.confirm(t, res)
	if ok.status != http.StatusOK || !strings.Contains(ok.raw, `"key":"tsapk_live_`) || !strings.Contains(ok.raw, `"mode":"live"`) {
		t.Fatalf("confirm %d %s", ok.status, ok.raw)
	}
	seen := o.events()
	if len(seen) != 1 || seen[0].Type != events.SecurityPayKeyRolled || seen[0].UserID != o.owner.ID || seen[0].Title != "Your live API key was rolled" {
		t.Fatalf("events %+v", seen)
	}
	if bad := o.do(http.MethodPost, "/api/pay/merchant/keys", map[string]any{"mode": "staging", "password": "correct-horse"}); bad.status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown mode: %d %s", bad.status, bad.raw)
	}
}

func TestPayOwner_EndpointsChangeIsValidatedThenGuarded(t *testing.T) {
	o := newOwnerRig(t)
	bad := o.do(http.MethodPut, "/api/pay/merchant/endpoints", map[string]any{"webhookUrl": "http://10.0.0.1/x", "password": "correct-horse"})
	if bad.status != http.StatusUnprocessableEntity || o.mail.message != "" {
		t.Fatalf("private URL: %d %s", bad.status, bad.raw)
	}
	res := o.do(http.MethodPut, "/api/pay/merchant/endpoints", map[string]any{
		"webhookUrl": "https://api.skyair.ng/tsa/webhook", "returnDomains": []string{"skyair.ng"}, "password": "correct-horse"})
	if res.status != http.StatusAccepted || res.Data.Confirmation.Kind != models.PayOwnerActionEndpoints {
		t.Fatalf("status %d %s", res.status, res.raw)
	}
	if o.stored(t).WebhookURL != "" {
		t.Fatal("the webhook changed before the code")
	}
	ok := o.confirm(t, res)
	if ok.status != http.StatusOK || ok.Data.Merchant == nil || ok.Data.Merchant.WebhookURL != "https://api.skyair.ng/tsa/webhook" {
		t.Fatalf("confirm %d %s", ok.status, ok.raw)
	}
	seen := o.events()
	if len(seen) != 1 || seen[0].Type != events.SecurityPayEndpointsChanged ||
		!strings.HasPrefix(seen[0].Message, "Live webhooks: https://api.skyair.ng/…; test webhooks: none.") || strings.Contains(seen[0].Message, "/tsa/webhook") {
		t.Fatalf("events %+v: the alert names each mode's host, never the path a token can live in", seen)
	}

	test := o.confirm(t, o.do(http.MethodPut, "/api/pay/merchant/endpoints", map[string]any{
		"webhookUrl": "https://api.skyair.ng/tsa/webhook", "testWebhookUrl": "https://staging.skyair.ng/hook?token=abc",
		"returnDomains": []string{"skyair.ng"}, "password": "correct-horse"}))
	if test.status != http.StatusOK {
		t.Fatalf("test webhook confirm %d %s", test.status, test.raw)
	}
	seen = o.events()
	if len(seen) != 2 || !strings.HasPrefix(seen[1].Message, "Live webhooks: https://api.skyair.ng/…; test webhooks: https://staging.skyair.ng/….") ||
		strings.Contains(seen[1].Message, "token=abc") {
		t.Fatalf("events %+v: a test-webhook change must be named too", seen)
	}
}

func TestPayOwner_WebhookSecretRevealAndRollAreAnnouncedByName(t *testing.T) {
	o := newOwnerRig(t)
	reveal := o.confirm(t, o.do(http.MethodPost, "/api/pay/merchant/webhook-secret", map[string]any{"action": "reveal", "password": "correct-horse"}))
	if reveal.status != http.StatusOK || !strings.Contains(reveal.raw, `"webhookSecretLive":"`) || !strings.Contains(reveal.raw, `"secretAction":"reveal"`) {
		t.Fatalf("reveal %d %s", reveal.status, reveal.raw)
	}
	roll := o.confirm(t, o.do(http.MethodPost, "/api/pay/merchant/webhook-secret", map[string]any{"action": "roll", "password": "correct-horse"}))
	if roll.status != http.StatusOK || !strings.Contains(roll.raw, `"webhookSecretTest":"`) {
		t.Fatalf("roll %d %s", roll.status, roll.raw)
	}
	seen := o.events()
	if len(seen) != 2 || seen[0].Type != events.SecurityPayWebhookSecret ||
		seen[0].Title != "Your webhook signing secret was revealed" || seen[1].Title != "Your webhook signing secret was rolled" {
		t.Fatalf("events %+v", seen)
	}
}

func TestPayOwner_GuardedRequestsNeedASessionAndAnActiveMerchant(t *testing.T) {
	o := newOwnerRig(t)
	h := NewPayHandler(services.NewPayMerchantService(o.db, nil, nil, []byte(strings.Repeat("k", 32)), time.Now), o.payments, nil, false)
	bare := gin.New()
	bare.POST("/keys", h.OwnerRequestKeys)
	bare.PUT("/endpoints", h.OwnerRequestEndpoints)
	bare.POST("/webhook-secret", h.OwnerRequestWebhookSecret)
	for _, rt := range []struct{ method, path string }{{http.MethodPost, "/keys"}, {http.MethodPut, "/endpoints"}, {http.MethodPost, "/webhook-secret"}} {
		if w := payDo(bare, rt.method, rt.path, map[string]any{"password": "correct-horse"}, nil); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s without a user: %d", rt.path, w.Code)
		}
	}
	if w := payDo(o.r, http.MethodPost, "/api/pay/merchant/keys", "not an object", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("a malformed body: %d", w.Code)
	}
	o.db.Model(&models.PayMerchant{}).Where("id = ?", o.m.ID).Update("status", models.PayMerchantSuspended)
	res := o.do(http.MethodPost, "/api/pay/merchant/keys", map[string]any{"mode": "live", "password": "correct-horse"})
	if res.status != http.StatusForbidden || res.Code != "merchant_suspended" || !strings.Contains(res.raw, "Your merchant account is suspended — contact support") {
		t.Fatalf("suspended: %d %s", res.status, res.raw)
	}
}

func TestPayOwner_ViewShowsKeysAndEndpointsButNeverASecret(t *testing.T) {
	o := newOwnerRig(t)
	res := o.do(http.MethodGet, "/api/pay/merchant", nil)
	for _, want := range []string{`"hasLiveKey":false`, `"hasTestKey":false`, `"webhookUrl":""`, `"testWebhookUrl":""`, `"returnDomains":["skyair.ng"]`} {
		if !strings.Contains(res.raw, want) {
			t.Fatalf("view %s missing %s", res.raw, want)
		}
	}
	for _, never := range []string{"keyHash", "KeyHash", "key_hash", "webhookSecret", "webhook_secret"} {
		if strings.Contains(res.raw, never) {
			t.Fatalf("view %s leaked %s", res.raw, never)
		}
	}
	o.db.Model(&models.PayMerchant{}).Where("id = ?", o.m.ID).Update("return_domains", "")
	if res := o.do(http.MethodGet, "/api/pay/merchant", nil); !strings.Contains(res.raw, `"returnDomains":[]`) {
		t.Fatalf("no return domains must read as [], not null: %s", res.raw)
	}
}

func TestPayOwner_BankDirectory(t *testing.T) {
	o := newOwnerRig(t)
	if res := o.do(http.MethodGet, "/api/pay/merchant/banks", nil); res.status != http.StatusOK || !strings.Contains(res.raw, `"data":[{"code":"044","name":"Access Bank"}]`) {
		t.Fatalf("banks %d %s", res.status, res.raw)
	}
	o.banks.banks = nil
	if res := o.do(http.MethodGet, "/api/pay/merchant/banks", nil); res.status != http.StatusOK || !strings.Contains(res.raw, `"data":[]`) {
		t.Fatalf("an empty directory must read as [], not null: %d %s", res.status, res.raw)
	}
	res := o.do(http.MethodGet, "/api/pay/merchant/banks/resolve?bankCode=044&accountNumber=1023456789", nil)
	if res.status != http.StatusOK || !strings.Contains(res.raw, `"accountName":"SKY AIR LIMITED"`) || !strings.Contains(res.raw, `"accountNumber":"1023456789"`) {
		t.Fatalf("resolve %d %s", res.status, res.raw)
	}
	for _, q := range []string{"bankCode=044&accountNumber=102345678", "bankCode=044&accountNumber=10234567ab", "accountNumber=1023456789"} {
		if res := o.do(http.MethodGet, "/api/pay/merchant/banks/resolve?"+q, nil); res.status != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, res.status)
		}
	}
	o.banks.err = services.ErrNombaAccountNotFound
	if res := o.do(http.MethodGet, "/api/pay/merchant/banks/resolve?bankCode=044&accountNumber=1023456789", nil); res.status != http.StatusNotFound {
		t.Fatalf("not found: %d", res.status)
	}
	o.banks.err = errors.New("nomba upstream: token eyJhbGciOi expired")
	if res := o.do(http.MethodGet, "/api/pay/merchant/banks/resolve?bankCode=044&accountNumber=1023456789", nil); res.status != http.StatusBadGateway || strings.Contains(res.raw, "nomba") {
		t.Fatalf("directory error: %d %s", res.status, res.raw)
	}

	h := NewPayHandler(services.NewPayMerchantService(o.db, nil, nil, []byte(strings.Repeat("k", 32)), time.Now), o.payments, nil, false)
	dormant := gin.New()
	dormant.Use(func(c *gin.Context) { c.Set("user", o.owner) })
	dormant.GET("/banks", h.OwnerBanks)
	dormant.GET("/resolve", h.OwnerResolveBank)
	for _, path := range []string{"/banks", "/resolve?bankCode=044&accountNumber=1023456789"} {
		if w := payDo(dormant, http.MethodGet, path, nil, nil); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"banks_unavailable"`) {
			t.Fatalf("%s with no directory: %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestPayOwner_SettlementOptionsAreTheListedStablecoins(t *testing.T) {
	o := newOwnerRig(t)
	res := o.do(http.MethodGet, "/api/pay/merchant/settlement-options", nil)
	want := `"data":[{"chainId":8453,"chain":"base","symbol":"USDC","tokenAddress":"` + ownerUSDC + `"}]`
	if res.status != http.StatusOK || !strings.Contains(res.raw, want) {
		t.Fatalf("options %d %s", res.status, res.raw)
	}
}

// The owner routes are the owner's: a signed-in user with no merchant cannot use
// them to look up whose a bank account is, or to spend the provider's quota.
func TestPayOwner_NoMerchantNoBankLookups(t *testing.T) {
	o := newOwnerRig(t)
	o.db.Model(&models.PayMerchant{}).Where("id = ?", o.m.ID).Update("user_id", uuid.New())
	for _, path := range []string{"/api/pay/merchant/banks/resolve?bankCode=044&accountNumber=1023456789", "/api/pay/merchant/banks",
		"/api/pay/merchant/settlement-options", "/api/pay/merchant/payments"} {
		if res := o.do(http.MethodGet, path, nil); res.status != http.StatusNotFound || res.Code != "no_merchant" {
			t.Fatalf("%s without a merchant: %d %s", path, res.status, res.raw)
		}
	}
	if o.banks.calls != 0 {
		t.Fatalf("the bank directory was asked %d times for a user with no merchant", o.banks.calls)
	}
}

func TestPayOwner_ABusyActionIsAConflictToRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/", func(c *gin.Context) { payOwnerFail(c, services.ErrPayActionBusy) })
	w := payDo(r, http.MethodGet, "/", nil, nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"action_busy"`) ||
		!strings.Contains(w.Body.String(), "Another change is being requested — try again") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

// The detail tells the merchant which TSA Connect account paid — a name only,
// never the payer's email or account id, and nothing once the account is deleted.
func TestPayOwner_PaymentDetailNamesThePayerButNotTheirContact(t *testing.T) {
	o := newOwnerRig(t)
	ctx := context.Background()
	in := services.CreatePayPaymentInput{AmountKobo: 5_000_000, Currency: "NGN", Reference: "PNR-PAYER", Description: "LOS-ABV"}
	p, _, err := o.payments.Create(ctx, o.m, models.PayModeLive, in)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if err := o.db.Exec(q, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	detail := func() string {
		t.Helper()
		res := o.do(http.MethodGet, "/api/pay/merchant/payments/"+p.ID.String()+"?mode=live", nil)
		if res.status != http.StatusOK {
			t.Fatalf("detail %d %s", res.status, res.raw)
		}
		return res.raw
	}
	if raw := detail(); !strings.Contains(raw, `"payer":null`) {
		t.Fatalf("nobody has paid yet, want payer null: %s", raw)
	}

	payer := uuid.New()
	exec(`INSERT INTO users (id, name, email, verification_status, account_status) VALUES (?, 'Tolu', 'tolu.private@gmail.com', 'pending', 'active')`, payer)
	exec(`UPDATE pay_payments SET payer_user_id = ?, status = ? WHERE id = ?`, payer, models.PayStatusProcessing, p.ID)
	raw := detail()
	if !strings.Contains(raw, `"payer":{"name":"Tolu"}`) {
		t.Fatalf("unverified payer: %s", raw)
	}
	if strings.Contains(raw, "tolu.private") || strings.Contains(raw, payer.String()) {
		t.Fatalf("the payer's email or account id leaked: %s", raw)
	}

	exec(`UPDATE users SET verification_status = 'verified', verified_legal_name = 'TOLUWANI ADEYEMI' WHERE id = ?`, payer)
	if raw := detail(); !strings.Contains(raw, `"payer":{"name":"TOLUWANI ADEYEMI"}`) {
		t.Fatalf("a verified payer shows the name on their ID: %s", raw)
	}

	// Deletion (as the handler does it): status deleted, profile anonymised —
	// the legal name stays in the row for KYC but must never be served.
	exec(`UPDATE users SET account_status = 'deleted', name = 'Deleted User', email = 'deleted@x' WHERE id = ?`, payer)
	raw = detail()
	if !strings.Contains(raw, `"payer":{"name":"Deleted account"}`) || strings.Contains(raw, "TOLUWANI") {
		t.Fatalf("a deleted payer's ID name reached the merchant: %s", raw)
	}
}

func TestPayOwner_ViewSaysWhetherLivePaymentsAreOn(t *testing.T) {
	o := newOwnerRig(t)
	if res := o.do(http.MethodGet, "/api/pay/merchant", nil); !strings.Contains(res.raw, `"liveEnabled":false`) {
		t.Fatalf("before the switch: %s", res.raw)
	}
	o.db.Model(&models.PayMerchant{}).Where("id = ?", o.m.ID).Update("live_enabled", true)
	if res := o.do(http.MethodGet, "/api/pay/merchant", nil); !strings.Contains(res.raw, `"liveEnabled":true`) {
		t.Fatalf("after the switch: %s", res.raw)
	}
}

// Only a super admin moves the live switch: an owner's endpoints change that
// also carries liveEnabled applies the endpoints and leaves the switch as it was,
// either way round.
func TestPayOwner_AnEndpointsChangeCannotMoveTheLiveSwitch(t *testing.T) {
	for _, was := range []bool{false, true} {
		o := newOwnerRig(t)
		o.db.Model(&models.PayMerchant{}).Where("id = ?", o.m.ID).Update("live_enabled", was)
		ok := o.confirm(t, o.do(http.MethodPut, "/api/pay/merchant/endpoints", map[string]any{
			"webhookUrl": "https://api.skyair.ng/tsa/webhook", "returnDomains": []string{"skyair.ng"},
			"password": "correct-horse", "liveEnabled": !was}))
		if ok.status != http.StatusOK {
			t.Fatalf("was %v: confirm %d %s", was, ok.status, ok.raw)
		}
		got := o.stored(t)
		if got.WebhookURL != "https://api.skyair.ng/tsa/webhook" {
			t.Fatalf("was %v: the endpoints change itself did not apply: %+v", was, got)
		}
		if got.LiveEnabled != was {
			t.Fatalf("was %v: an owner's endpoints change moved the live switch to %v", was, got.LiveEnabled)
		}
	}
}
