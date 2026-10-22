package handlers

import (
	"context"
	"errors"
	"math/big"
	"net/http"
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

func (f *fakeSellerCalls) do(fn string, event string, orderID uuid.UUID) (string, error) {
	f.calls = append(f.calls, fn)
	if f.err != nil {
		return "", f.err
	}
	f.hash = "0xtreasury" + fn
	f.chain.MineEvent(f.hash, f.escrow, event, services.GenerateOrderID(orderID), nil)
	return f.hash, nil
}

func (f *fakeSellerCalls) MarkDelivered(_ context.Context, id uuid.UUID, _ string) (string, error) {
	return f.do("markDelivered", "DeliveryMarked", id)
}

func (f *fakeSellerCalls) CancelOrder(_ context.Context, id uuid.UUID, _ string) (string, error) {
	return f.do("cancelOrder", "OrderCancelled", id)
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

func (w *wrongEventCaller) MarkDelivered(_ context.Context, id uuid.UUID, _ string) (string, error) {
	return w.f.do("markDelivered", "OrderCancelled", id)
}
func (w *wrongEventCaller) CancelOrder(_ context.Context, id uuid.UUID, _ string) (string, error) {
	return w.f.do("cancelOrder", "ReceiptConfirmed", id)
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
