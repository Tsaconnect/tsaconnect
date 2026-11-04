package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// failingOrderer answers every create with err, as a provider timeout or 5xx
// would — the request left us, so an order may exist upstream.
type failingOrderer struct {
	stubPaycrestOrderer
	err error
}

func (f failingOrderer) CreateOnrampOrder(context.Context, PaycrestOnrampRequest) (*PaycrestOrder, error) {
	return nil, f.err
}
func (f failingOrderer) CreateOfframpOrder(context.Context, PaycrestOfframpRequest) (*PaycrestOrder, error) {
	return nil, f.err
}

// cancellingOrderer answers like Paycrest did on a live order, then cancels
// the caller's context — the request dying between Paycrest's answer and our
// row write, which used to lose the row.
type cancellingOrderer struct {
	*capturingOrderer
	cancel context.CancelFunc
}

func (c cancellingOrderer) CreateOnrampOrder(ctx context.Context, req PaycrestOnrampRequest) (*PaycrestOrder, error) {
	o, err := c.capturingOrderer.CreateOnrampOrder(ctx, req)
	c.cancel()
	return o, err
}

// newSeamService is newFeeTestService for any PaycrestOrderer: a real quote
// service over one base/USDC book at `rate`, so CreateBuy/CreateSell reach the
// provider call.
func newSeamService(t *testing.T, pc PaycrestOrderer, side, rate string) *PaycrestOrderService {
	t.Helper()
	base, _ := newPaycrestOrderTestService(t)
	book := sell(rate)
	if side == "buy" {
		book = buy(rate)
	}
	quotes := NewPaycrestQuoteService(
		&stubQuoter{market: 1370.15, rates: map[string]*PaycrestRates{"base|USDC": book}},
		[]string{"USDC"}, []string{"base"})
	svc := NewPaycrestOrderService(base.db, pc, quotes, base.dir, "0.5", "")
	if svc == nil {
		t.Fatal("NewPaycrestOrderService returned nil")
	}
	return svc
}

func seamBuyRequest(user uuid.UUID) PaycrestBuyRequest {
	return PaycrestBuyRequest{
		BuyerID: testBuyerID(),
		UserID:  user, WalletAddress: "0xabc", FiatCode: "NGN", FiatAmount: "800",
		ChainKey: "base", Token: "USDC",
		RefundBankCode: "044", RefundAccount: "0123456789", RefundAccountName: "Test User",
	}
}

// The ramp router must tell "the user must fix this" from a provider
// failure, so these are sentinels now — with today's wording unchanged.
func TestRampSeam_UserErrorsAreSentinelsWithUnchangedText(t *testing.T) {
	svc := newSeamService(t, stubPaycrestOrderer{}, "buy", "1390.64")
	ctx := context.Background()

	req := seamBuyRequest(uuid.New())
	req.WalletAddress = ""
	_, err := svc.CreateBuy(ctx, req)
	if !errors.Is(err, ErrBuyNeedsWallet) || err.Error() != "connect a wallet before buying" {
		t.Fatalf("no wallet: %v", err)
	}
	req = seamBuyRequest(uuid.New())
	req.RefundAccount = ""
	_, err = svc.CreateBuy(ctx, req)
	if !errors.Is(err, ErrBuyNeedsRefundAccount) || err.Error() != "a refund account is required in case the order fails" {
		t.Fatalf("no refund account: %v", err)
	}

	sreq := sellRequest()
	sreq.WalletAddress = ""
	_, err = svc.CreateSell(ctx, sreq)
	if !errors.Is(err, ErrSellNeedsWallet) || err.Error() != "connect a wallet before selling" {
		t.Fatalf("no wallet (sell): %v", err)
	}
	sreq = sellRequest()
	sreq.AccountNo = ""
	_, err = svc.CreateSell(ctx, sreq)
	if !errors.Is(err, ErrSellNeedsPayoutAccount) || err.Error() != "a payout bank account is required" {
		t.Fatalf("no payout account: %v", err)
	}
}

// An error from the create call itself means the request left us: marked
// ErrOrderRequestSent, still matching the provider's own error underneath.
func TestRampSeam_CreateCallErrorsAreMarkedSent(t *testing.T) {
	upstream := errors.New("paycrest: 502 bad gateway")

	buySvc := newSeamService(t, failingOrderer{err: upstream}, "buy", "1390.64")
	_, err := buySvc.CreateBuy(context.Background(), seamBuyRequest(seedVerifiedUser(t, buySvc.db)))
	if !errors.Is(err, ErrOrderRequestSent) || !errors.Is(err, upstream) {
		t.Fatalf("buy create error = %v, want ErrOrderRequestSent wrapping the upstream error", err)
	}

	sellSvc := newSeamService(t, failingOrderer{err: upstream}, "sell", "1360.05")
	_, err = sellSvc.CreateSell(context.Background(), sellRequest())
	if !errors.Is(err, ErrOrderRequestSent) || !errors.Is(err, upstream) {
		t.Fatalf("sell create error = %v, want ErrOrderRequestSent wrapping the upstream error", err)
	}
}

// A 400/422 from Paycrest means it refused the request: nothing was opened,
// so it is marked rejected — never "may exist".
func TestRampSeam_ProviderRejectionIsNotMarkedSent(t *testing.T) {
	rejected := &PaycrestRequestError{Status: 422}
	svc := newSeamService(t, failingOrderer{err: rejected}, "buy", "1390.64")
	_, err := svc.CreateBuy(context.Background(), seamBuyRequest(seedVerifiedUser(t, svc.db)))
	if !errors.Is(err, ErrPaycrestOrderRejected) || errors.Is(err, ErrOrderRequestSent) {
		t.Fatalf("422 = %v, want ErrPaycrestOrderRejected and not ErrOrderRequestSent", err)
	}
}

// Errors before the create call are NOT marked sent: nothing left us.
func TestRampSeam_PreCreateErrorsAreNotMarkedSent(t *testing.T) {
	svc := newSeamService(t, failingOrderer{err: errors.New("unreachable")}, "buy", "1390.64")
	req := seamBuyRequest(uuid.New())
	req.WalletAddress = ""
	_, err := svc.CreateBuy(context.Background(), req)
	if errors.Is(err, ErrOrderRequestSent) {
		t.Fatalf("a refusal before the provider call must not read as sent: %v", err)
	}
}

// Once Paycrest has answered with an order, our row is written even if the
// request context dies — otherwise the order exists upstream with no row.
func TestRampSeam_RowSurvivesARequestCancelledAfterPaycrestAnswered(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pc := cancellingOrderer{capturingOrderer: &capturingOrderer{rate: "1390.64", feePercent: "0.5"}, cancel: cancel}
	svc := newSeamService(t, pc, "buy", "1390.64")
	row, err := svc.CreateBuy(ctx, seamBuyRequest(seedVerifiedUser(t, svc.db)))
	if err != nil || row == nil {
		t.Fatalf("row=%v err=%v", row, err)
	}
	var n int64
	svc.db.Model(&models.PaycrestOrderRecord{}).Where("id = ?", row.ID).Count(&n)
	if n != 1 {
		t.Fatal("the order row was not persisted")
	}
}

func TestRampSeam_AssertNoOpenOrderKeepsItsOwnRefusal(t *testing.T) {
	svc := newSeamService(t, stubPaycrestOrderer{}, "buy", "1390.64")
	ctx := context.Background()
	user := uuid.New()

	if err := svc.AssertNoOpenOrder(ctx, user, models.PaycrestDirectionBuy); err != nil {
		t.Fatalf("empty: %v", err)
	}
	row := &models.PaycrestOrderRecord{
		UserID: user, Direction: models.PaycrestDirectionBuy,
		Status: models.PaycrestStatusAwaitingPayment, Reference: "PCB-seam-1",
	}
	if err := svc.db.Create(row).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.AssertNoOpenOrder(ctx, user, models.PaycrestDirectionBuy); !errors.Is(err, ErrPaycrestOrderOpen) {
		t.Fatalf("awaiting payment: %v", err)
	}
	if err := svc.AssertNoOpenOrder(ctx, user, models.PaycrestDirectionSell); err != nil {
		t.Fatalf("other direction: %v", err)
	}
}

func TestRampSeam_RequirePaycrestEnabledNormalisesTheCode(t *testing.T) {
	svc, registry := newPaycrestOrderTestService(t)
	svc.SetCurrencyRegistry(registry)
	if err := svc.RequirePaycrestEnabled(" ngn "); err != nil {
		t.Fatalf("NGN: %v", err)
	}
	if err := svc.RequirePaycrestEnabled(""); err != nil {
		t.Fatalf("empty defaults to NGN: %v", err)
	}
	if err := svc.RequirePaycrestEnabled("ghs"); !errors.Is(err, ErrPaycrestDisabled) {
		t.Fatalf("GHS (not paycrest): %v", err)
	}
}
