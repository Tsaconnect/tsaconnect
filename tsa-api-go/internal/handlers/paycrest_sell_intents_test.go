package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// paycrest_sell_intents_test.go — Task 13: the sale routes.
//
// The service owns every decision about money; what is pinned here is the two
// things only the handler can get wrong — WHOSE wallet the USDC is promised
// to, and whether a "still swapping" reads to the app as a wait rather than a
// failure.

type stubSellIntents struct {
	last     services.CreateSellIntentInput
	lastUser *models.User
	intent   *models.RampSellIntent
	quote    *services.SellSwapQuote
	err      error
	open     *models.RampSellIntent
	// resumable answers Resumable when set; otherwise it answers with open.
	resumable *models.RampSellIntent
	// got answers Get; getErr overrides it.
	got    *models.RampSellIntent
	getErr error
	// lastUseWallet is the flag OpenOrder was last called with.
	lastUseWallet bool
	// order is what OpenOrder answers when there is no error.
	order *models.PaycrestOrderRecord
}

func (s *stubSellIntents) Get(context.Context, uuid.UUID, uuid.UUID) (*models.RampSellIntent, error) {
	return s.got, s.getErr
}
func (s *stubSellIntents) CountToppedUpSince(context.Context, uuid.UUID, time.Time) (int64, error) {
	return 0, nil
}
func (s *stubSellIntents) CountUnusedSince(context.Context, uuid.UUID, time.Time) (int64, error) {
	return 0, nil
}
func (s *stubSellIntents) MarkToppedUp(context.Context, uuid.UUID, time.Time) (bool, error) {
	return true, nil
}
func (s *stubSellIntents) ClearToppedUp(context.Context, uuid.UUID) error { return nil }
func (s *stubSellIntents) RecordTopUp(context.Context, uuid.UUID, string, string) error {
	return nil
}

func (s *stubSellIntents) Create(_ context.Context, user *models.User, in services.CreateSellIntentInput) (*models.RampSellIntent, *services.SellSwapQuote, error) {
	s.last, s.lastUser = in, user
	if s.err != nil {
		return nil, nil, s.err
	}
	return s.intent, s.quote, nil
}

func (s *stubSellIntents) RecordSwap(context.Context, uuid.UUID, uuid.UUID, string) error {
	return s.err
}

func (s *stubSellIntents) OpenOrder(_ context.Context, _ *models.User, _ uuid.UUID, useWalletBalance bool) (*models.PaycrestOrderRecord, error) {
	s.lastUseWallet = useWalletBalance
	return s.order, s.err
}

func (s *stubSellIntents) Abandon(context.Context, uuid.UUID, uuid.UUID) error { return s.err }

func (s *stubSellIntents) Open(context.Context, uuid.UUID) (*models.RampSellIntent, error) {
	return s.open, nil
}

func (s *stubSellIntents) Resumable(context.Context, uuid.UUID) (*models.RampSellIntent, error) {
	if s.resumable != nil {
		return s.resumable, nil
	}
	return s.open, nil
}

func postSellIntent(t *testing.T, h *PaycrestHandler, user *models.User, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/paycrest/sell-intents", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", user)
	h.CreateSellIntent(c)
	return w
}

func sellIntentBody() map[string]any {
	return map[string]any{
		"sourceChainId": 56, "sourceToken": "0x0000000000000000000000000000000000000000",
		"sourceAmount": "16000000000000000", "sourceAddress": sellSwapUserWallet,
		"bankCode": "044", "accountNumber": "0123456789", "currency": "NGN",
	}
}

// The sale is opened for the account's OWN wallet. A device that asks for the
// USDC to land somewhere another account holds is refused outright — a sale
// delivered to a stranger's wallet cannot be undone.
func TestCreateSellIntent_SellsToTheReconciledWallet(t *testing.T) {
	db := newCallerWalletTestDB(t)
	user := &models.User{ID: uuid.New(), Username: "seller", WalletAddress: sellSwapUserWallet,
		AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, user)
	insertTestUser(t, db, &models.User{ID: uuid.New(), Username: "someone-else",
		WalletAddress: sellSwapOtherWallet, AccountStatus: models.AccountStatusActive})

	stub := &stubSellIntents{
		intent: &models.RampSellIntent{ID: uuid.New()},
		quote:  &services.SellSwapQuote{LiFiQuote: json.RawMessage(`{"ok":true}`)},
	}
	h := (&PaycrestHandler{}).WithSellIntents(stub)

	if w := postSellIntent(t, h, user, sellIntentBody()); w.Code != http.StatusCreated {
		t.Fatalf("code = %d, body %s", w.Code, w.Body.String())
	}
	if stub.lastUser == nil || stub.lastUser.WalletAddress != sellSwapUserWallet {
		t.Fatalf("sale opened for wallet %+v", stub.lastUser)
	}

	body := sellIntentBody()
	body["walletAddress"] = sellSwapOtherWallet
	if w := postSellIntent(t, h, user, body); w.Code != http.StatusConflict {
		t.Fatalf("claiming another account's wallet: code = %d", w.Code)
	}
}

// The app asks for a source-chain top-up exactly when the server priced one,
// so the Create answer has to carry that verdict — both ways.
func TestCreateSellIntent_SaysWhetherAGasTopUpWasPriced(t *testing.T) {
	db := newCallerWalletTestDB(t)
	user := &models.User{ID: uuid.New(), Username: "seller", WalletAddress: sellSwapUserWallet,
		AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, user)
	for _, priced := range []bool{true, false} {
		stub := &stubSellIntents{
			intent: &models.RampSellIntent{ID: uuid.New()},
			quote:  &services.SellSwapQuote{LiFiQuote: json.RawMessage(`{}`), GasTopUp: priced, TopUpWei: "4320"},
		}
		w := postSellIntent(t, (&PaycrestHandler{}).WithSellIntents(stub), user, sellIntentBody())
		var out struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusCreated {
			t.Fatalf("code = %d err = %v body = %s", w.Code, err, w.Body.String())
		}
		if got, ok := out.Data["gasTopUp"].(bool); !ok || got != priced {
			t.Fatalf("gasTopUp = %v (present %v), want %v", out.Data["gasTopUp"], ok, priced)
		}
		if strings.Contains(w.Body.String(), "4320") {
			t.Fatal("the wei figure is the server's; it never leaves it")
		}
	}
}

// GET /sell-intents/open answers with what "Finish your sale" can resume —
// including an ordered sale still awaiting its deposit, which Open() alone
// (the one-open-sale rule's view) deliberately does not report.
func TestOpenSellIntent_ReturnsTheResumableSale(t *testing.T) {
	ordered := &models.RampSellIntent{ID: uuid.New(), Status: models.SellIntentOrdered}
	h := (&PaycrestHandler{}).WithSellIntents(&stubSellIntents{resumable: ordered})
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/paycrest/sell-intents/open", nil)
	c.Set("user", &models.User{ID: uuid.New(), WalletAddress: sellSwapUserWallet})
	h.OpenSellIntent(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ordered.ID.String()) {
		t.Fatalf("code = %d body = %s; want the ordered sale", w.Code, w.Body.String())
	}
}

// A sale still swapping is a WAIT. Answering 500 (or anything the app reads
// as broken) would send her back to start a second swap for money she has
// already sent.
func TestSellIntentRoutes_MapTheServiceSentinels(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"still swapping", services.ErrSwapNotLanded, http.StatusConflict},
		{"not her swap", services.ErrSwapMismatch, http.StatusConflict},
		{"wrong step", services.ErrSellIntentState, http.StatusConflict},
		{"already selling", services.ErrSellIntentOpen, http.StatusConflict},
		{"no such sale", services.ErrSellIntentNotFound, http.StatusNotFound},
		{"payout being opened", services.ErrSellIntentOrdering, http.StatusConflict},
		{"swap failed", fmt.Errorf("%w (FAILED)", services.ErrSwapFailed), http.StatusConflict},
		{"a direct sell is live", services.ErrPaycrestOrderOpen, http.StatusConflict},
		{"too costly", services.ErrSellRouteTooCostly, http.StatusUnprocessableEntity},
		{"unavailable", services.ErrSellUnavailable, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := &models.User{ID: uuid.New(), WalletAddress: sellSwapUserWallet}
			h := (&PaycrestHandler{}).WithSellIntents(&stubSellIntents{err: tc.err})
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/paycrest/sell-intents/x/order", nil)
			c.Params = gin.Params{{Key: "id", Value: uuid.NewString()}}
			c.Set("user", user)
			h.OpenSellIntentOrder(c)
			if w.Code != tc.want {
				t.Fatalf("code = %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// The app decides wait-versus-terminal on the response's code, not its copy:
// every outcome it acts on carries one, and the message stays what it was for
// an app build that still reads the text.
func TestSellIntentRoutes_NameEachOutcomeWithACode(t *testing.T) {
	ended := func(status, sub string) error { return &services.SwapEndedShortError{Status: status, Substatus: sub} }
	for _, tc := range []struct {
		name, code, message string
		err                 error
	}{
		{"still swapping", "swap_pending", "Still swapping — try again in a moment", services.ErrSwapNotLanded},
		{"payout being opened", "payout_opening", "Opening your payout — try again in a moment", services.ErrSellIntentOrdering},
		{"failed", "swap_failed", "your swap didn't complete (FAILED) — nothing was sold; check your wallet",
			ended(services.LiFiStatusFailed, "")},
		{"failed with a qualifier", "swap_failed",
			"your swap didn't complete (FAILED/NOT_PROCESSABLE_REFUND_NEEDED) — nothing was sold; check your wallet",
			ended(services.LiFiStatusFailed, "NOT_PROCESSABLE_REFUND_NEEDED")},
		{"refunded", "swap_refunded", "your swap didn't complete (DONE/REFUNDED) — nothing was sold; check your wallet",
			ended(services.LiFiStatusDone, services.LiFiSubstatusRefunded)},
		{"partial", "swap_partial", "your swap didn't complete (DONE/PARTIAL) — nothing was sold; check your wallet",
			ended(services.LiFiStatusDone, services.LiFiSubstatusPartial)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := &models.User{ID: uuid.New(), WalletAddress: sellSwapUserWallet}
			h := (&PaycrestHandler{}).WithSellIntents(&stubSellIntents{err: tc.err})
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/paycrest/sell-intents/x/order", nil)
			c.Params = gin.Params{{Key: "id", Value: uuid.NewString()}}
			c.Set("user", user)
			h.OpenSellIntentOrder(c)
			var body struct{ Code, Message string }
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusConflict || body.Code != tc.code || body.Message != tc.message {
				t.Fatalf("%d %+v, want 409 code %q message %q", w.Code, body, tc.code, tc.message)
			}
		})
	}
}

// Without RAMP_CONVERSION_SELL nothing is wired, and the routes are not even
// registered — a handler reached anyway must not pretend to have a sale.
func TestSellIntentRoutes_DormantWithoutTheService(t *testing.T) {
	user := &models.User{ID: uuid.New(), WalletAddress: sellSwapUserWallet}
	h := &PaycrestHandler{}
	if h.SellIntentsEnabled() {
		t.Fatal("sales enabled with no service")
	}
	if w := postSellIntent(t, h, user, sellIntentBody()); w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}

// ─── Task 14: gas top-up for a sale ────────────────────────────────────────

func postSellIntentTopUp(t *testing.T, h *PaycrestHandler, id string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/paycrest/sell-intents/"+id+"/gas-topup", nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Set("user", &models.User{ID: uuid.New(), WalletAddress: sellSwapUserWallet})
	h.SellIntentGasTopUp(c)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

// The handler loads HER sale and hands the service its own store: it never
// decides an amount or a cap itself.
func TestSellIntentGasTopUp_TopsUpHerOwnSale(t *testing.T) {
	in := &models.RampSellIntent{ID: uuid.New(), Status: models.SellIntentSwapping}
	sales := &stubSellIntents{got: in}
	port := &fakeSponsorPort{enabled: true, topUpOut: services.GasTopUpResult{TxHash: "0xgas", AmountWei: "123"}}
	h := (&PaycrestHandler{}).WithSellIntents(sales).WithSponsorship(port)
	w, out := postSellIntentTopUp(t, h, in.ID.String())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if port.intentTopUpIntent != in || port.intentTopUpStore != services.IntentTopUpStore(sales) {
		t.Fatalf("port got intent %v store %v; want the loaded sale and the sale service", port.intentTopUpIntent, port.intentTopUpStore)
	}
	if out["data"].(map[string]any)["txHash"] != "0xgas" {
		t.Fatalf("data = %v", out["data"])
	}
}

func TestSellIntentGasTopUp_MapsErrors(t *testing.T) {
	cases := []struct {
		name   string
		getErr error
		topErr error
		want   int
	}{
		{"someone else's sale", services.ErrSellIntentNotFound, nil, http.StatusNotFound},
		{"past its swap", nil, services.ErrSellIntentState, http.StatusConflict},
		{"daily cap", nil, services.ErrTopUpDailyCap, http.StatusTooManyRequests},
		{"unused cap", nil, services.ErrTopUpUnusedCap, http.StatusTooManyRequests},
		{"no KYC", nil, services.ErrKYCRequired, http.StatusForbidden},
		{"no lane", nil, services.ErrSponsorNotAvailable, http.StatusConflict},
		{"too expensive", nil, services.ErrTopUpTooExpensive, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sales := &stubSellIntents{got: &models.RampSellIntent{ID: uuid.New()}, getErr: tc.getErr}
			port := &fakeSponsorPort{enabled: true, err: tc.topErr}
			h := (&PaycrestHandler{}).WithSellIntents(sales).WithSponsorship(port)
			w, _ := postSellIntentTopUp(t, h, uuid.NewString())
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, tc.want, w.Body.String())
			}
			if tc.getErr != nil && port.intentTopUpCalls != 0 {
				t.Fatal("a sale that could not be loaded must never reach the top-up")
			}
		})
	}
}

// Dormant unless BOTH the sale service and sponsorship are wired.
func TestSellIntentGasTopUp_DormantUnlessSellAndSponsorshipAreOn(t *testing.T) {
	port := &fakeSponsorPort{enabled: true}
	for name, h := range map[string]*PaycrestHandler{
		"no sell":         (&PaycrestHandler{}).WithSponsorship(port),
		"no sponsorship":  (&PaycrestHandler{}).WithSellIntents(&stubSellIntents{got: &models.RampSellIntent{}}),
		"sponsorship off": (&PaycrestHandler{}).WithSellIntents(&stubSellIntents{got: &models.RampSellIntent{}}).WithSponsorship(&fakeSponsorPort{}),
	} {
		if h.SellIntentGasTopUpEnabled() {
			t.Fatalf("%s: enabled", name)
		}
		if w, _ := postSellIntentTopUp(t, h, uuid.NewString()); w.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", name, w.Code)
		}
	}
	if port.intentTopUpCalls != 0 {
		t.Fatal("a dormant route must not top up")
	}
	on := (&PaycrestHandler{}).WithSellIntents(&stubSellIntents{}).WithSponsorship(port)
	if !on.SellIntentGasTopUpEnabled() {
		t.Fatal("both on: want enabled")
	}
}

// openOrderRequest drives POST /sell-intents/:id/order with an optional body.
func openOrderRequest(t *testing.T, stub *stubSellIntents, body string) *httptest.ResponseRecorder {
	t.Helper()
	user := &models.User{ID: uuid.New(), WalletAddress: sellSwapUserWallet}
	h := (&PaycrestHandler{}).WithSellIntents(stub)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/paycrest/sell-intents/x/order", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: uuid.NewString()}}
	c.Set("user", user)
	h.OpenSellIntentOrder(c)
	return w
}

// A pay intent that landed short asks her before spending her other USDC: the
// 409 carries a machine code and exactly how much more it needs.
func TestOpenSellIntentOrder_AShortfallIsAskedNotSpent(t *testing.T) {
	stub := &stubSellIntents{err: &services.SellShortfallError{MissingBaseUnits: big.NewInt(23_525000)}}
	w := openOrderRequest(t, stub, "")
	var body struct {
		Code        string `json:"code"`
		MissingUSDC string `json:"missingUsdc"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusConflict || body.Code != "pay_shortfall" || body.MissingUSDC != "23.525" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestOpenSellIntentOrder_ARateThatMovedIsARetryableConflict(t *testing.T) {
	w := openOrderRequest(t, &stubSellIntents{err: services.ErrSellRateMoved}, "")
	var body struct{ Code string }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusConflict || body.Code != "rate_moved" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestOpenSellIntentOrder_TheWalletBalanceConsentDefaultsToNo(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
		code       int
	}{
		{"no body", "", false, http.StatusCreated},
		{"empty object", "{}", false, http.StatusCreated},
		{"explicit no", `{"useWalletBalance":false}`, false, http.StatusCreated},
		{"consent", `{"useWalletBalance":true}`, true, http.StatusCreated},
		{"malformed", `{"useWalletBalance":`, false, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubSellIntents{}
			w := openOrderRequest(t, stub, tc.body)
			if w.Code != tc.code || stub.lastUseWallet != tc.want {
				t.Fatalf("code %d (want %d), flag %v (want %v): %s", w.Code, tc.code, stub.lastUseWallet, tc.want, w.Body.String())
			}
		})
	}
}

// The pay-only fields belong to the pay checkout, which sets them itself from
// a payment it owns. A client that sends any of them must not be able to point
// her own sale at a wallet, an exact naira payout or a fee budget of her
// choosing — the body has no such fields, so they arrive at the service empty.
func TestCreateSellIntent_TheAppCannotSetAnyPayOnlyField(t *testing.T) {
	db := newCallerWalletTestDB(t)
	user := &models.User{ID: uuid.New(), Username: "seller", WalletAddress: sellSwapUserWallet,
		AccountStatus: models.AccountStatusActive}
	insertTestUser(t, db, user)
	stub := &stubSellIntents{
		intent: &models.RampSellIntent{ID: uuid.New()},
		quote:  &services.SellSwapQuote{LiFiQuote: json.RawMessage(`{"ok":true}`)},
	}
	h := (&PaycrestHandler{}).WithSellIntents(stub)

	body := sellIntentBody()
	for k, v := range map[string]any{
		"payPaymentId": uuid.New().String(), "payoutFiat": "50000.00", "toAmount": "1",
		"destChainId": 10, "destTokenAddress": "0xToken", "destAddress": "0xAttacker", "destAmount": "1", "budgetBps": 5000,
		"PayPaymentID": uuid.New().String(), "DestAddress": "0xAttacker", "BudgetBps": 5000,
	} {
		body[k] = v
	}
	if w := postSellIntent(t, h, user, body); w.Code != http.StatusCreated {
		t.Fatalf("code = %d, body %s", w.Code, w.Body.String())
	}
	got := stub.last
	if got.PayPaymentID != nil || got.PayoutFiat != "" || got.ToAmount != "" ||
		got.DestChainID != 0 || got.DestTokenAddress != "" || got.DestAddress != "" || got.DestAmount != "" || got.BudgetBps != 0 {
		t.Fatalf("a client reached a pay-only field: %+v", got)
	}
}
