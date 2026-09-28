package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// orderMcgpPaymentsDDL mirrors models.OrderMcgpPayment for SQLite tests.
const orderMcgpPaymentsDDL = `CREATE TABLE IF NOT EXISTS order_mcgp_payments (
	id TEXT PRIMARY KEY, order_id TEXT UNIQUE, buyer_id TEXT, buyer_wallet TEXT,
	mcgp_amount TEXT, from_bought TEXT, debit_ref TEXT UNIQUE, status TEXT,
	debit_tx_hash TEXT, resolve_tx_hash TEXT, release_reason TEXT,
	seller_wallet TEXT, seller_amount TEXT, seller_credit_tx_hash TEXT,
	buyer_amount TEXT, collect_tx_hash TEXT, collect_settle_tx_hash TEXT,
	decided_at DATETIME, debited_at DATETIME, failure_reason TEXT,
	created_at DATETIME, updated_at DATETIME)`

// fakeSpendablePayer stands in for OrderSpendablePaymentService: it records
// what the handler asked for and applies the order outcome the way the real
// claim does (in a transaction), so the handler's branching is what is tested.
type fakeSpendablePayer struct {
	balance  *big.Int
	limitErr error
	fundErr  error
	// decided makes Release/Refund answer as for a payment another request
	// already decided.
	decided bool
	warmed  int

	funded   []services.OrderMcgpFundInput
	released []services.OrderMcgpReleasePlan
	refunded []uuid.UUID
}

func (f *fakeSpendablePayer) IsEnabled() bool                  { return true }
func (f *fakeSpendablePayer) Balance(string) (*big.Int, error) { return f.balance, nil }
func (f *fakeSpendablePayer) WarmLimits(context.Context)       { f.warmed++ }
func (f *fakeSpendablePayer) CheckCachedLimits(*big.Int) error { return f.limitErr }
func (f *fakeSpendablePayer) Fund(_ context.Context, in services.OrderMcgpFundInput) (*models.OrderMcgpPayment, error) {
	if f.fundErr != nil {
		return nil, f.fundErr
	}
	f.funded = append(f.funded, in)
	if err := config.DB.Model(&models.Order{}).Where("id = ?", in.OrderID).Updates(map[string]interface{}{
		"status": models.OrderStatusEscrowed, "payment_source": models.OrderPaymentSpendableMCGP,
	}).Error; err != nil {
		return nil, err
	}
	return &models.OrderMcgpPayment{OrderID: in.OrderID, Status: models.OrderMcgpHeld}, nil
}
func (f *fakeSpendablePayer) Release(_ context.Context, orderID uuid.UUID, plan services.OrderMcgpReleasePlan, apply func(tx *gorm.DB) error) (*models.OrderMcgpPayment, error) {
	if f.decided {
		return &models.OrderMcgpPayment{OrderID: orderID, Status: models.OrderMcgpReleased}, services.ErrOrderMcgpAlreadyDecided
	}
	if err := config.DB.Transaction(apply); err != nil {
		return nil, err
	}
	f.released = append(f.released, plan)
	return &models.OrderMcgpPayment{OrderID: orderID, Status: models.OrderMcgpReleasing}, nil
}
func (f *fakeSpendablePayer) Refund(_ context.Context, orderID uuid.UUID, apply func(tx *gorm.DB) error) (*models.OrderMcgpPayment, error) {
	if f.decided {
		return &models.OrderMcgpPayment{OrderID: orderID, Status: models.OrderMcgpReversed}, services.ErrOrderMcgpAlreadyDecided
	}
	if err := config.DB.Transaction(apply); err != nil {
		return nil, err
	}
	f.refunded = append(f.refunded, orderID)
	return &models.OrderMcgpPayment{OrderID: orderID, Status: models.OrderMcgpReversing}, nil
}

type spendableRig struct {
	db            *gorm.DB
	ch            *CheckoutHandler
	payer         *fakeSpendablePayer
	buyer, seller *models.User
	product       *models.Product
}

func newSpendableRig(t *testing.T) *spendableRig {
	t.Helper()
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	payer := &fakeSpendablePayer{balance: new(big.Int).Mul(big.NewInt(1000), big.NewInt(1e18))}
	ch.spendable = payer
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, buyer, "0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce")
	mustRecordWalletProof(t, db, buyer.ID, buyer.WalletAddress) // the operator only debits a proven wallet
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96")
	return &spendableRig{db: db, ch: ch, payer: payer, buyer: buyer, seller: seller,
		product: createTestProduct(t, db, seller.ID, 10.0)}
}

func (r *spendableRig) order(t *testing.T, status, source string) *models.Order {
	t.Helper()
	o := createTestOrder(t, r.db, r.buyer.ID, r.seller.ID, r.product.ID, status, "MCGP")
	if source != "" {
		r.db.Model(&models.Order{}).Where("id = ?", o.ID).Update("payment_source", source)
		o.PaymentSource = source
	}
	return o
}

func (r *spendableRig) reload(t *testing.T, id uuid.UUID) models.Order {
	t.Helper()
	var o models.Order
	if err := r.db.First(&o, "id = ?", id).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	return o
}

// call runs h for user on order id with an optional JSON body.
func call(t *testing.T, h gin.HandlerFunc, user *models.User, id uuid.UUID, body map[string]interface{}) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)
	c.Params = gin.Params{{Key: "id", Value: id.String()}}
	raw, _ := json.Marshal(body)
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	h(c)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp
}

// ── fund ─────────────────────────────────────────────────────────────────

func TestPaySpendable_FundsTheOrderTotalFromSpendableMCGP(t *testing.T) {
	r := newSpendableRig(t)
	o := r.order(t, models.OrderStatusPendingPayment, "")

	w, _ := call(t, r.ch.PaySpendable, r.buyer, o.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(r.payer.funded) != 1 {
		t.Fatalf("funded %d times, want 1", len(r.payer.funded))
	}
	in := r.payer.funded[0]
	if in.Amount.String() != o.TotalAmount || in.BuyerWallet != r.buyer.WalletAddress {
		t.Errorf("fund = %s from %s, want the order total %s from the buyer", in.Amount, in.BuyerWallet, o.TotalAmount)
	}
	if got := r.reload(t, o.ID); got.Status != models.OrderStatusEscrowed || !got.IsSpendableMCGP() {
		t.Fatalf("order = %s/%q", got.Status, got.PaymentSource)
	}
}

func TestPaySpendable_OnlyTheBuyerAndOnlyMCGP(t *testing.T) {
	r := newSpendableRig(t)
	o := r.order(t, models.OrderStatusPendingPayment, "")
	if w, _ := call(t, r.ch.PaySpendable, r.seller, o.ID, nil); w.Code != http.StatusForbidden {
		t.Fatalf("seller: expected 403, got %d", w.Code)
	}
	usdc := createTestOrder(t, r.db, r.buyer.ID, r.seller.ID, r.product.ID, models.OrderStatusPendingPayment, "USDC")
	if w, _ := call(t, r.ch.PaySpendable, r.buyer, usdc.ID, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("usdc: expected 400, got %d", w.Code)
	}
	if len(r.payer.funded) != 0 {
		t.Fatal("nothing may be debited")
	}
}

func TestPaySpendable_ShortfallIsAClearRefusal(t *testing.T) {
	r := newSpendableRig(t)
	r.payer.fundErr = &services.SpendableShortfallError{Need: big.NewInt(16e17), Have: big.NewInt(5e17)}
	o := r.order(t, models.OrderStatusPendingPayment, "")
	w, resp := call(t, r.ch.PaySpendable, r.buyer, o.ID, nil)
	if w.Code != http.StatusBadRequest || resp["code"] != "insufficient_balance" ||
		!strings.Contains(w.Body.String(), "Spendable MCGP balance is 0.5 MCGP") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

// ── refusal of wallet (ERC-20) MCGP escrow funding ─────────────────────

func TestPrepareApprove_RefusesWalletMCGPForNewOrders(t *testing.T) {
	r := newSpendableRig(t)
	o := r.order(t, models.OrderStatusPendingPayment, "")
	for name, h := range map[string]gin.HandlerFunc{"approve": r.ch.PrepareApprove, "escrow": r.ch.PrepareEscrow} {
		w, resp := call(t, h, r.buyer, o.ID, nil)
		if w.Code != http.StatusConflict || resp["code"] != "mcgp_spendable_only" ||
			!strings.Contains(w.Body.String(), "Update the TSA Connect app") {
			t.Fatalf("%s: expected 409 mcgp_spendable_only, got %d %s", name, w.Code, w.Body.String())
		}
	}
}

func TestSubmitEscrow_RefusesASpendableOrder(t *testing.T) {
	r := newSpendableRig(t)
	o := r.order(t, models.OrderStatusPendingPayment, models.OrderPaymentSpendableMCGP)
	w, resp := call(t, r.ch.SubmitEscrow, r.buyer, o.ID, map[string]interface{}{"approveTxHash": "0x1", "escrowTxHash": "0x2"})
	if w.Code != http.StatusConflict || resp["code"] != "spendable_order" {
		t.Fatalf("expected 409 spendable_order, got %d %s", w.Code, w.Body.String())
	}
}

// Creating an MCGP order checks the Spendable MCGP balance, not the wallet.
func TestCreateOrderFromCart_MCGPChecksSpendableBalance(t *testing.T) {
	r := newSpendableRig(t)
	r.ch.otcService = fakeMCGPPrice{usd6: 250000}
	r.ch.walletBalances = fakeWalletBalances{balance: new(big.Int).Mul(big.NewInt(1_000_000), big.NewInt(1e18))} // plenty of wallet MCGP
	r.payer.balance = big.NewInt(1e18)                                                                           // 1 Spendable MCGP
	createTestCart(t, r.db, r.buyer.ID, []models.CartItem{
		{ID: uuid.New(), Product: r.product.ID, Seller: r.seller.ID, Quantity: 1, Price: 10.0, AddedAt: time.Now()},
	})
	w, _ := callOrderHandler(t, r.ch.CreateOrderFromCart, r.buyer, withToken("MCGP", nil))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Not enough Spendable MCGP") {
		t.Fatalf("expected a Spendable MCGP shortfall, got %d %s", w.Code, w.Body.String())
	}

	r.payer.balance = new(big.Int).Mul(big.NewInt(1000), big.NewInt(1e18))
	r.payer.limitErr = &services.SpendableLimitError{Max: big.NewInt(1e18)}
	w, _ = callOrderHandler(t, r.ch.CreateOrderFromCart, r.buyer, withToken("MCGP", nil))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "per-payment limit") {
		t.Fatalf("expected a cap refusal, got %d %s", w.Code, w.Body.String())
	}
	var n int64
	config.DB.Model(&models.Order{}).Where("buyer_id = ? AND token = 'MCGP'", r.buyer.ID).Count(&n)
	if n != 0 {
		t.Fatalf("a refused checkout must not create orders, got %d", n)
	}
}

func TestMCGPCheckout_UnavailableWithoutTheOperator(t *testing.T) {
	r := newSpendableRig(t)
	r.ch.spendable = nil
	createTestCart(t, r.db, r.buyer.ID, []models.CartItem{
		{ID: uuid.New(), Product: r.product.ID, Seller: r.seller.ID, Quantity: 1, Price: 10.0, AddedAt: time.Now()},
	})
	w, _ := callOrderHandler(t, r.ch.CreateOrderFromCart, r.buyer, withToken("MCGP", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d %s", w.Code, w.Body.String())
	}
}

// ── release ──────────────────────────────────────────────────────────────

func TestConfirmReceipt_ReleasesTheHoldToTheSeller(t *testing.T) {
	r := newSpendableRig(t)
	o := r.order(t, models.OrderStatusDelivered, models.OrderPaymentSpendableMCGP)

	w, _ := call(t, r.ch.ConfirmReceipt, r.buyer, o.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", w.Code, w.Body.String())
	}
	if len(r.payer.released) != 1 {
		t.Fatalf("released %d times", len(r.payer.released))
	}
	plan := r.payer.released[0]
	// product + shipping; the platform fee stays with the platform (as
	// ProductEscrow's _releaseToSeller). MCGP orders carry no fee in practice.
	if plan.SellerAmount.String() != "15000000" || plan.SellerWallet != r.seller.WalletAddress || plan.BuyerAmount != nil {
		t.Fatalf("plan = %+v", plan)
	}
	if got := r.reload(t, o.ID); got.Status != models.OrderStatusCompleted || got.BuyerConfirmedAt == nil {
		t.Fatalf("order = %s", got.Status)
	}
	// The escrow confirm is refused for it.
	if w, resp := call(t, r.ch.PrepareConfirm, r.buyer, o.ID, nil); w.Code != http.StatusConflict || resp["code"] != "spendable_order" {
		t.Fatalf("prepare-confirm: %d %s", w.Code, w.Body.String())
	}
}

func TestConfirmReceipt_RefusesDisputedAndEscrowOrders(t *testing.T) {
	r := newSpendableRig(t)
	o := r.order(t, models.OrderStatusDelivered, models.OrderPaymentSpendableMCGP)
	r.db.Model(&models.Order{}).Where("id = ?", o.ID).Update("disputed_at", time.Now())
	if w, _ := call(t, r.ch.ConfirmReceipt, r.buyer, o.ID, nil); w.Code != http.StatusConflict {
		t.Fatalf("disputed: %d", w.Code)
	}
	legacy := r.order(t, models.OrderStatusDelivered, "")
	if w, resp := call(t, r.ch.ConfirmReceipt, r.buyer, legacy.ID, nil); w.Code != http.StatusConflict || resp["code"] != "escrow_order" {
		t.Fatalf("escrow order: %d %s", w.Code, w.Body.String())
	}
	if len(r.payer.released) != 0 {
		t.Fatal("nothing may be released")
	}
}

func TestAdminResolve_SpendableReleaseOrRefundNeedsSuperAdmin(t *testing.T) {
	r := newSpendableRig(t)
	admin := createTestUserWithLocation(t, r.db, "Lagos", "Lagos", "Nigeria")
	admin.Role = models.RoleAdmin
	r.db.Save(admin)

	o := r.order(t, models.OrderStatusRefundRequested, models.OrderPaymentSpendableMCGP)
	if w, _ := call(t, r.ch.AdminResolveDispute, admin, o.ID, map[string]interface{}{"refundBuyer": false}); w.Code != http.StatusForbidden {
		t.Fatalf("plain admin: expected 403, got %d", w.Code)
	}

	admin.Role = models.RoleSuperAdmin
	w, resp := call(t, r.ch.AdminResolveDispute, admin, o.ID, map[string]interface{}{"refundBuyer": false})
	if w.Code != http.StatusOK || len(r.payer.released) != 1 {
		t.Fatalf("release: %d %s", w.Code, w.Body.String())
	}
	if data := resp["data"].(map[string]interface{}); data["resolved"] != true || data["resolveTx"] != nil {
		t.Fatalf("a spendable resolve returns no tx to sign: %v", data)
	}
	if got := r.reload(t, o.ID); got.Status != models.OrderStatusCompleted {
		t.Fatalf("order = %s", got.Status)
	}

	d := r.order(t, models.OrderStatusDelivered, models.OrderPaymentSpendableMCGP)
	r.db.Model(&models.Order{}).Where("id = ?", d.ID).Updates(map[string]interface{}{"disputed_at": time.Now(), "dispute_reason": "never arrived"})
	if w, _ := call(t, r.ch.AdminResolveDispute, admin, d.ID, map[string]interface{}{"refundBuyer": true}); w.Code != http.StatusOK {
		t.Fatalf("refund: %d %s", w.Code, w.Body.String())
	}
	got := r.reload(t, d.ID)
	if got.Status != models.OrderStatusRefunded || got.DisputedAt != nil || len(r.payer.refunded) != 1 {
		t.Fatalf("order = %s disputed=%v refunds=%d", got.Status, got.DisputedAt, len(r.payer.refunded))
	}
	if w, resp := call(t, r.ch.SubmitAdminResolve, admin, d.ID, map[string]interface{}{"txHash": "0x1"}); w.Code != http.StatusConflict || resp["code"] != "spendable_order" {
		t.Fatalf("submit-resolve: %d %s", w.Code, w.Body.String())
	}
}

// ── refund ───────────────────────────────────────────────────────────────

func TestRequestRefund_EscrowedSpendableRefundsImmediately(t *testing.T) {
	r := newSpendableRig(t)
	o := r.order(t, models.OrderStatusEscrowed, models.OrderPaymentSpendableMCGP)
	w, resp := call(t, r.ch.RequestRefund, r.buyer, o.ID, nil)
	if w.Code != http.StatusOK || len(r.payer.refunded) != 1 {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if resp["data"].(map[string]interface{})["refundTx"] != nil {
		t.Fatal("no tx to sign for a Spendable MCGP refund")
	}
	if got := r.reload(t, o.ID); got.Status != models.OrderStatusRefunded {
		t.Fatalf("order = %s", got.Status)
	}
	var stock int
	r.db.Model(&models.Product{}).Select("stock").Where("id = ?", r.product.ID).Scan(&stock)
	if stock != r.product.Stock+1 {
		t.Fatalf("stock = %d, want restored to %d", stock, r.product.Stock+1)
	}
}

func TestSellerCancelAndApprovedCancel_RefundTheHold(t *testing.T) {
	r := newSpendableRig(t)
	o := r.order(t, models.OrderStatusEscrowed, models.OrderPaymentSpendableMCGP)
	w, resp := call(t, r.ch.CancelOrder, r.seller, o.ID, nil)
	if w.Code != http.StatusOK || resp["data"].(map[string]interface{})["cancelTx"] != nil {
		t.Fatalf("seller cancel: %d %s", w.Code, w.Body.String())
	}
	if got := r.reload(t, o.ID); got.Status != models.OrderStatusCancelled {
		t.Fatalf("order = %s", got.Status)
	}

	req := r.order(t, models.OrderStatusCancelRequested, models.OrderPaymentSpendableMCGP)
	w, resp = call(t, r.ch.ApproveCancelOrder, r.seller, req.ID, nil)
	if w.Code != http.StatusOK || resp["data"].(map[string]interface{})["cancelled"] != true {
		t.Fatalf("approve cancel: %d %s", w.Code, w.Body.String())
	}
	if got := r.reload(t, req.ID); got.Status != models.OrderStatusCancelled || !got.MerchantApprovedCancel {
		t.Fatalf("order = %s approved=%v", got.Status, got.MerchantApprovedCancel)
	}
	if len(r.payer.refunded) != 2 {
		t.Fatalf("refunds = %d, want 2", len(r.payer.refunded))
	}
}

// ── legacy wallet-MCGP escrow orders keep their on-chain lifecycle ──────

func TestLegacyEscrowMCGPOrder_KeepsItsOnChainLifecycle(t *testing.T) {
	r := newSpendableRig(t)
	escrowed := r.order(t, models.OrderStatusEscrowed, "")

	w, resp := call(t, r.ch.RequestRefund, r.buyer, escrowed.ID, nil)
	if w.Code != http.StatusOK || resp["data"].(map[string]interface{})["refundTx"] == nil {
		t.Fatalf("legacy refund must still return a tx to sign: %d %s", w.Code, w.Body.String())
	}
	delivered := r.order(t, models.OrderStatusDelivered, "")
	w, resp = call(t, r.ch.PrepareConfirm, r.buyer, delivered.ID, nil)
	if w.Code != http.StatusOK || resp["data"].(map[string]interface{})["confirmTx"] == nil {
		t.Fatalf("legacy confirm must still return a tx: %d %s", w.Code, w.Body.String())
	}
	w, resp = call(t, r.ch.CancelOrder, r.seller, escrowed.ID, nil)
	if w.Code != http.StatusOK || resp["data"].(map[string]interface{})["cancelTx"] == nil {
		t.Fatalf("legacy seller cancel must still return a tx: %d %s", w.Code, w.Body.String())
	}
	if len(r.payer.funded)+len(r.payer.released)+len(r.payer.refunded) != 0 {
		t.Fatal("a legacy escrow order must never touch Spendable MCGP")
	}
	if got := r.reload(t, escrowed.ID); got.Status != models.OrderStatusEscrowed {
		t.Fatalf("legacy order status changed off-chain: %s", got.Status)
	}
}

// ── review fixes ─────────────────────────────────────────────────────────

// A seller with no wallet could never be paid: the buyer's MCGP is not taken.
func TestPaySpendable_RefusesASellerWithNoWallet(t *testing.T) {
	// Empty and malformed alike: Release requires a valid address, so pay
	// time must too.
	for _, wallet := range []string{"", "0x1234", "not-an-address"} {
		r := newSpendableRig(t)
		setUserWallet(t, r.db, r.seller, wallet)
		o := r.order(t, models.OrderStatusPendingPayment, "")
		w, resp := call(t, r.ch.PaySpendable, r.buyer, o.ID, nil)
		if w.Code != http.StatusBadRequest || resp["code"] != "seller_no_wallet" {
			t.Fatalf("wallet %q: expected 400 seller_no_wallet, got %d %s", wallet, w.Code, w.Body.String())
		}
		if len(r.payer.funded) != 0 {
			t.Fatal("nothing may be debited")
		}
	}
}

// Without the operator every spendable money path answers 503, never panics.
func TestSpendableOrderPaths_WithoutTheOperatorAnswer503(t *testing.T) {
	r := newSpendableRig(t)
	r.ch.spendable = nil
	admin := createTestUserWithLocation(t, r.db, "Lagos", "Lagos", "Nigeria")
	admin.Role = models.RoleSuperAdmin
	r.db.Save(admin)

	cases := []struct {
		name   string
		h      gin.HandlerFunc
		user   *models.User
		status string
		body   map[string]interface{}
	}{
		{"confirm", r.ch.ConfirmReceipt, r.buyer, models.OrderStatusDelivered, nil},
		{"refund", r.ch.RequestRefund, r.buyer, models.OrderStatusEscrowed, nil},
		{"seller cancel", r.ch.CancelOrder, r.seller, models.OrderStatusEscrowed, nil},
		{"approve cancel", r.ch.ApproveCancelOrder, r.seller, models.OrderStatusCancelRequested, nil},
		{"admin resolve", r.ch.AdminResolveDispute, admin, models.OrderStatusRefundRequested, map[string]interface{}{"refundBuyer": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := r.order(t, tc.status, models.OrderPaymentSpendableMCGP)
			w, resp := call(t, tc.h, tc.user, o.ID, tc.body)
			if w.Code != http.StatusServiceUnavailable || resp["code"] != "mcgp_unavailable" {
				t.Fatalf("expected 503 mcgp_unavailable, got %d %s", w.Code, w.Body.String())
			}
			if got := r.reload(t, o.ID); got.Status != tc.status {
				t.Fatalf("order moved to %s without the payment moving", got.Status)
			}
		})
	}
}

// A payment another request already decided is echoed, not re-run: no
// second fee settlement, no second notice.
func TestConfirmReceipt_AlreadyDecidedEchoesTheOrder(t *testing.T) {
	r := newSpendableRig(t)
	r.payer.decided = true
	o := r.order(t, models.OrderStatusDelivered, models.OrderPaymentSpendableMCGP)

	w, resp := call(t, r.ch.ConfirmReceipt, r.buyer, o.ID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	data := resp["data"].(map[string]interface{})
	if data["alreadyDecided"] != true || data["status"] != models.OrderStatusDelivered {
		t.Fatalf("data = %v", data)
	}
	if got := r.reload(t, o.ID); got.TPDistributedAt != nil {
		t.Fatal("fee settlement must not run for a decision another request made")
	}

	refund := r.order(t, models.OrderStatusEscrowed, models.OrderPaymentSpendableMCGP)
	w, resp = call(t, r.ch.RequestRefund, r.buyer, refund.ID, nil)
	if w.Code != http.StatusOK || resp["data"].(map[string]interface{})["alreadyDecided"] != true {
		t.Fatalf("refund: %d %s", w.Code, w.Body.String())
	}
}

func TestPaySpendable_MapsRetryWalletAndEscrowErrors(t *testing.T) {
	cases := map[error]struct {
		code   string
		status int
	}{
		services.ErrOrderMcgpRetry:          {"payment_retry", http.StatusConflict},
		services.ErrOrderMcgpWalletChanged:  {"wallet_changed", http.StatusConflict},
		services.ErrOrderMcgpEscrowUnknown:  {"escrow_unknown", http.StatusServiceUnavailable},
		services.ErrOrderMcgpPending:        {"payment_pending", http.StatusConflict},
		services.ErrOrderNotPayable:         {"order_not_payable", http.StatusConflict},
		services.ErrOrderMcgpSellerWallet:   {"seller_no_wallet", http.StatusConflict},
		services.ErrOrderMcgpPayoutBelowMin: {"payout_below_minimum", http.StatusConflict},
	}
	for err, want := range cases {
		r := newSpendableRig(t)
		r.payer.fundErr = err
		o := r.order(t, models.OrderStatusPendingPayment, "")
		w, resp := call(t, r.ch.PaySpendable, r.buyer, o.ID, nil)
		if w.Code != want.status || resp["code"] != want.code {
			t.Errorf("%v: got %d %v, want %d %s", err, w.Code, resp["code"], want.status, want.code)
		}
	}
}
