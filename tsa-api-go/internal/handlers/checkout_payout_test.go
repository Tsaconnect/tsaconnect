package handlers

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

const proofTreasury = "0x00000000000000000000000000000000000000e5"

// fakeSellerCalls stands in for the treasury: a call mines the matching event
// on the fake chain, as the real one would.
type fakeSellerCalls struct {
	chain  *services.FakeEscrowChain
	escrow string
	err    error
	calls  []string
	hash   string
}

func (f *fakeSellerCalls) do(fn string, event string, key [32]byte) (string, error) {
	f.calls = append(f.calls, fn)
	if f.err != nil {
		return "", f.err
	}
	f.hash = "0xtreasury" + fn
	f.chain.MineEvent(f.hash, f.escrow, event, key, nil)
	return f.hash, nil
}

func (f *fakeSellerCalls) MarkDelivered(_ context.Context, _ uuid.UUID, key [32]byte, _ string) (string, error) {
	return f.do("markDelivered", "OrderDelivered", key)
}

func (f *fakeSellerCalls) CancelOrder(_ context.Context, _ uuid.UUID, key [32]byte, _ string) (string, error) {
	return f.do("cancelOrder", "OrderRefunded", key)
}

// withPayout makes the fixture's order a payout order: the treasury is its
// on-chain seller and a seller_payouts row carries it.
func (f *proofFixture) withPayout(t *testing.T) *fakeSellerCalls {
	t.Helper()
	f.db.Model(f.order).Update("escrow_seller_wallet", proofTreasury)
	f.order.EscrowSellerWallet = proofTreasury
	if err := f.db.Exec(`INSERT INTO seller_payouts (id, marketplace_order_id, seller_user_id, status) VALUES (?, ?, ?, ?)`,
		uuid.NewString(), f.order.ID.String(), f.seller.ID.String(), models.SellerPayoutAwaitingRelease).Error; err != nil {
		t.Fatal(err)
	}
	calls := &fakeSellerCalls{chain: f.chain, escrow: f.escrow()}
	f.ch.WithSellerCalls(calls)
	return calls
}

func TestPayoutOrder_TheTreasuryMarksDeliveryForTheSeller(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusShipped)
	calls := f.withPayout(t)
	f.holdOrder(func(o *services.OnChainOrder) { o.Seller = common.HexToAddress(proofTreasury) })

	// The seller has nothing to sign.
	w := f.call(f.ch.PrepareDeliver, f.seller, gin.H{})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "delivery_server_signed") {
		t.Fatalf("prepare-deliver: %d %s", w.Code, w.Body)
	}
	if len(calls.calls) != 0 {
		t.Fatal("preparing must not send anything")
	}

	w = f.call(f.ch.MarkDelivered, f.seller, gin.H{"deliveryProofUrl": "https://example.com/p.jpg"})
	if w.Code != http.StatusOK {
		t.Fatalf("deliver: %d %s", w.Code, w.Body)
	}
	if got := f.status(t); got != models.OrderStatusDelivered || len(calls.calls) != 1 || calls.calls[0] != "markDelivered" {
		t.Fatalf("status %q, treasury calls %v; want delivered after one markDelivered", got, calls.calls)
	}
}

func TestPayoutOrder_DeliveryIsProvenBeforeTheStatusMoves(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusShipped)
	calls := f.withPayout(t)
	f.holdOrder(nil)

	// A call that mined the WRONG event proves nothing about delivery.
	calls.chain = f.chain
	f.ch.WithSellerCalls(&wrongEventCaller{f: calls})
	w := f.call(f.ch.MarkDelivered, f.seller, gin.H{"deliveryProofUrl": "https://example.com/p.jpg"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if got := f.status(t); got != models.OrderStatusShipped {
		t.Fatalf("status %q", got)
	}
}

type wrongEventCaller struct{ f *fakeSellerCalls }

func (w *wrongEventCaller) MarkDelivered(_ context.Context, _ uuid.UUID, key [32]byte, _ string) (string, error) {
	return w.f.do("markDelivered", "OrderRefunded", key)
}
func (w *wrongEventCaller) CancelOrder(_ context.Context, _ uuid.UUID, key [32]byte, _ string) (string, error) {
	return w.f.do("cancelOrder", "OrderReleased", key)
}

func TestPayoutOrder_ATreasuryThatCannotSendLeavesTheOrderAlone(t *testing.T) {
	for name, err := range map[string]error{
		"still confirming": services.ErrSellerCallUncertain,
		"killed":           services.ErrTreasuryKilled,
	} {
		t.Run(name, func(t *testing.T) {
			f := newProofFixture(t, models.OrderStatusShipped)
			calls := f.withPayout(t)
			calls.err = err
			f.holdOrder(nil)
			w := f.call(f.ch.MarkDelivered, f.seller, gin.H{"deliveryProofUrl": "https://example.com/p.jpg"})
			want := http.StatusServiceUnavailable
			if errors.Is(err, services.ErrSellerCallUncertain) {
				want = http.StatusConflict
			}
			if w.Code != want {
				t.Fatalf("got %d %s", w.Code, w.Body)
			}
			if got := f.status(t); got != models.OrderStatusShipped {
				t.Fatalf("status %q", got)
			}
		})
	}
}

func TestPayoutOrder_WithoutTheTreasuryCallerNothingIsSigned(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusShipped)
	f.withPayout(t)
	f.ch.sellerCalls = nil
	f.holdOrder(nil)
	if w := f.call(f.ch.MarkDelivered, f.seller, gin.H{"deliveryProofUrl": "https://example.com/p.jpg"}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
}

func TestPayoutOrder_TheTreasuryCancelsWhenTheSellerApproves(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusCancelRequested)
	calls := f.withPayout(t)
	f.holdOrder(nil)
	w := f.call(f.ch.ApproveCancelOrder, f.seller, gin.H{})
	if w.Code != http.StatusOK {
		t.Fatalf("approve-cancel: %d %s", w.Code, w.Body)
	}
	if got := f.status(t); got != models.OrderStatusCancelled || len(calls.calls) != 1 || calls.calls[0] != "cancelOrder" {
		t.Fatalf("status %q, treasury calls %v", got, calls.calls)
	}
}

func TestPayoutOrder_ASellerCancelOfAPaidOrderGoesThroughTheTreasury(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusEscrowed)
	calls := f.withPayout(t)
	f.holdOrder(nil)
	w := f.call(f.ch.CancelOrder, f.seller, gin.H{})
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	if got := f.status(t); got != models.OrderStatusCancelled || len(calls.calls) != 1 {
		t.Fatalf("status %q, treasury calls %v", got, calls.calls)
	}
}

func TestEscrowSellerFor_UsesTheRecordedWalletForPayoutOrders(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusPendingPayment)
	ctx := context.Background()
	if got, err := escrowSellerFor(ctx, f.db, f.order, proofSellerWallet); err != nil || got != proofSellerWallet {
		t.Fatalf("ordinary order: %q, %v", got, err)
	}
	f.withPayout(t)
	if got, err := escrowSellerFor(ctx, f.db, f.order, proofSellerWallet); err != nil || got != proofTreasury {
		t.Fatalf("payout order: %q, %v; want the treasury", got, err)
	}
}

// fakePlanner prices every line the same.
type fakePlanner struct {
	plan *services.PayoutPlan
	err  error
	nets []*big.Int
}

func (p *fakePlanner) Plan(_ context.Context, _ uuid.UUID, _ string, net *big.Int) (*services.PayoutPlan, error) {
	p.nets = append(p.nets, net)
	return p.plan, p.err
}
func (p *fakePlanner) Persist(*gorm.DB, *models.Order, string, *services.PayoutPlan, *big.Int) error {
	return nil
}
func (p *fakePlanner) TreasuryAddress() string { return proofTreasury }

func TestPlanLinePayout(t *testing.T) {
	newLine := func() *pricedCartLine {
		return &pricedCartLine{productAmount: big.NewInt(98_000000), shippingAmount: big.NewInt(2_000000), seller: models.User{ID: uuid.New()}}
	}
	t.Run("the offramp costs ride on the shipping amount, off the fee base", func(t *testing.T) {
		ch := &CheckoutHandler{}
		p := &fakePlanner{plan: &services.PayoutPlan{GrossUp: big.NewInt(906138), FiatTargetKobo: 1}}
		ch.WithPayouts(p)
		line := newLine()
		if perr := ch.planLinePayout(context.Background(), line, "USDC"); perr != nil {
			t.Fatal(perr)
		}
		if line.shippingAmount.String() != "2906138" || line.productAmount.String() != "98000000" || line.payout == nil {
			t.Fatalf("line = product %s shipping %s", line.productAmount, line.shippingAmount)
		}
		if p.nets[0].String() != "100000000" {
			t.Fatalf("priced on %s; want product + shipping", p.nets[0])
		}
	})
	t.Run("no plan leaves the line alone", func(t *testing.T) {
		ch := (&CheckoutHandler{}).WithPayouts(&fakePlanner{})
		line := newLine()
		if perr := ch.planLinePayout(context.Background(), line, "USDC"); perr != nil || line.payout != nil || line.shippingAmount.String() != "2000000" {
			t.Fatalf("line = %+v, %v", line, perr)
		}
	})
	t.Run("no planner leaves the line alone", func(t *testing.T) {
		line := newLine()
		if perr := (&CheckoutHandler{}).planLinePayout(context.Background(), line, "USDC"); perr != nil || line.payout != nil {
			t.Fatalf("line = %+v, %v", line, perr)
		}
	})
	t.Run("an unreadable method fails the quote instead of paying the wallet", func(t *testing.T) {
		ch := (&CheckoutHandler{}).WithPayouts(&fakePlanner{err: errors.New("db down")})
		if perr := ch.planLinePayout(context.Background(), newLine(), "USDC"); perr == nil {
			t.Fatal("must fail")
		}
	})
}

// realPlanner prices every order the same and records the payout row for real.
type realPlanner struct{ gross *big.Int }

func (p *realPlanner) Plan(context.Context, uuid.UUID, string, *big.Int) (*services.PayoutPlan, error) {
	return &services.PayoutPlan{
		Method:  &models.SellerPayoutMethod{Currency: "NGN", InstitutionCode: "058", InstitutionName: "GTBank", AccountNumber: "0123456789", AccountName: "ADA OBI"},
		GrossUp: p.gross, FiatTargetKobo: 1_500_000,
	}, nil
}
func (p *realPlanner) Persist(tx *gorm.DB, order *models.Order, wallet string, plan *services.PayoutPlan, expected *big.Int) error {
	return (&services.SellerPayoutPlanner{}).Persist(tx, order, wallet, plan, expected)
}
func (p *realPlanner) TreasuryAddress() string { return proofTreasury }

func TestCreateServiceOrder_APayoutRidesOnTheShippingAmount(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	ch.WithPayouts(&realPlanner{gross: big.NewInt(906138)})
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, proofSellerWallet)
	cat := createTypedServiceCategory(t, db, models.ServiceTypeOrder)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 0)
	svc.ShippingSameCity = 4
	db.Save(svc)
	item := addMenuItem(t, db, svc.ID, "Combo", 15, true)

	w := postServiceOrder(t, ch, buyer, map[string]interface{}{
		"productId":      svc.ID.String(),
		"menuSelections": []map[string]interface{}{{"itemId": item.ID.String(), "quantity": 2}},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	order := firstOrder(t, w)
	if order["shippingAmount"] != "4906138" || order["payoutFee"] != "906138" {
		t.Fatalf("shipping %v, payoutFee %v; want the seller's 4 USDC plus the 0.906138 payout fee", order["shippingAmount"], order["payoutFee"])
	}
	id, _ := uuid.Parse(order["id"].(string))
	row, err := services.PayoutForOrder(context.Background(), db, id)
	if err != nil || row == nil {
		t.Fatalf("payout row = %+v, %v", row, err)
	}
	// What the escrow will credit the treasury: product + shipping (with the payout fee) less the
	// merchant fee OrderEscrow takes from the seller at release.
	want := new(big.Int).Sub(new(big.Int).Add(bigOf(t, order["productAmount"]), bigOf(t, order["shippingAmount"])), bigOf(t, order["platformFee"]))
	if row.ExpectedAmount != want.String() || row.SellerWallet != proofSellerWallet || row.AccountNumber != "0123456789" {
		t.Fatalf("payout = %+v", row)
	}
	var stored models.Order
	db.First(&stored, "id = ?", id)
	if stored.EscrowSellerWallet != proofTreasury {
		t.Fatalf("recorded seller wallet = %q; want the treasury", stored.EscrowSellerWallet)
	}
}

// The treasury signs these for the seller, so nobody else may ask: not the
// buyer, not a stranger.
func TestPayoutOrder_OnlyTheSellerCanHaveTheTreasuryCancelOrDeliver(t *testing.T) {
	stranger := func(f *proofFixture) *models.User {
		u := createTestUserWithLocation(t, f.db, "Lagos", "Lagos", "Nigeria")
		return u
	}
	cases := map[string]struct {
		status string
		call   func(*proofFixture) func(*gin.Context)
		body   gin.H
	}{
		"cancel an escrowed order":       {models.OrderStatusEscrowed, func(f *proofFixture) func(*gin.Context) { return f.ch.CancelOrder }, gin.H{}},
		"approve a cancel request":       {models.OrderStatusCancelRequested, func(f *proofFixture) func(*gin.Context) { return f.ch.ApproveCancelOrder }, gin.H{}},
		"mark a shipped order delivered": {models.OrderStatusShipped, func(f *proofFixture) func(*gin.Context) { return f.ch.MarkDelivered }, gin.H{"deliveryProofUrl": "https://example.com/p.jpg"}},
	}
	for name, c := range cases {
		for who, pick := range map[string]func(*proofFixture) *models.User{
			"the buyer":  func(f *proofFixture) *models.User { return f.buyer },
			"a stranger": stranger,
		} {
			t.Run(name+" as "+who, func(t *testing.T) {
				f := newProofFixture(t, c.status)
				calls := f.withPayout(t)
				f.holdOrder(nil)
				w := f.call(c.call(f), pick(f), c.body)
				if w.Code != http.StatusForbidden {
					t.Fatalf("got %d %s; want 403", w.Code, w.Body)
				}
				if len(calls.calls) != 0 {
					t.Fatalf("the treasury was made to send %v for someone who is not the seller", calls.calls)
				}
				if got := f.status(t); got != c.status {
					t.Fatalf("status moved to %q", got)
				}
			})
		}
	}
}

func TestOrderDetail_OnlyTheSellerSeesThePayoutAndNoInternals(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusEscrowed)
	f.withPayout(t)
	f.db.Exec(`UPDATE seller_payouts SET account_number = '0123456789', institution_name = 'GTBank', fiat_target_kobo = 1770588,
		status = ?, failure_reason = 'secret internals' WHERE marketplace_order_id = ?`, models.SellerPayoutDepositing, f.order.ID.String())

	w := f.call(f.ch.GetOrderDetail, f.seller, gin.H{})
	if w.Code != http.StatusOK {
		t.Fatalf("seller: %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{`"stage":"sending"`, `"amountNgn":"17705.88"`, `"bank":"GTBank"`, `"accountLast4":"6789"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, "secret internals") || strings.Contains(body, "0123456789") {
		t.Fatalf("the seller's view leaks internals or the full account number: %s", body)
	}
	if w = f.call(f.ch.GetOrderDetail, f.buyer, gin.H{}); strings.Contains(w.Body.String(), `"payout"`) {
		t.Fatalf("the buyer must not see the seller's payout: %s", w.Body)
	}
}

// A fee-rate change re-prices a pending order at approve time; the payout it carries must expect
// the figure the escrow will now release (product + shipping less the merchant fee).
func TestRepricePayout_MovesTheExpectedAmountAndTheNaira(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusPendingPayment)
	f.withPayout(t)
	f.db.Exec(`UPDATE seller_payouts SET expected_amount = '100000000', fiat_target_kobo = 15000000, status = ? WHERE marketplace_order_id = ?`,
		models.SellerPayoutAwaitingRelease, f.order.ID.String())

	// The merchant fee fell by 1 USDC at the new rate: the release is 101.
	if err := repricePayout(f.db, f.order, big.NewInt(101_000000)); err != nil {
		t.Fatal(err)
	}
	row, err := services.PayoutForOrder(context.Background(), f.db, f.order.ID)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	if row.ExpectedAmount != "101000000" || row.FiatTargetKobo != 15150000 {
		t.Fatalf("expected %s / %d kobo; want 101 USDC and the naira in proportion", row.ExpectedAmount, row.FiatTargetKobo)
	}
	// An ordinary order has no payout to move.
	g := newProofFixture(t, models.OrderStatusPendingPayment)
	if err := repricePayout(g.db, g.order, big.NewInt(2)); err != nil {
		t.Fatalf("ordinary order: %v", err)
	}
}

// Two requests for the same proven cancel restore the unit once.
func TestFinishSellerCancel_RestoresStockOnce(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusCancelRequested)
	var before models.Product
	f.db.First(&before, "id = ?", f.order.ProductID)

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest("POST", "/", nil)
		stale := *f.order // each request saw the order as cancel_requested
		f.ch.finishSellerCancel(c, &stale, "0xcancel")
		if w.Code != http.StatusOK {
			t.Fatalf("call %d: %d %s", i, w.Code, w.Body)
		}
	}
	var after models.Product
	f.db.First(&after, "id = ?", f.order.ProductID)
	if after.Stock != before.Stock+f.order.Quantity {
		t.Fatalf("stock %d -> %d; want exactly one unit back", before.Stock, after.Stock)
	}
}

func bigOf(t *testing.T, v interface{}) *big.Int {
	t.Helper()
	x, ok := new(big.Int).SetString(fmt.Sprint(v), 10)
	if !ok {
		t.Fatalf("not a number: %v", v)
	}
	return x
}
