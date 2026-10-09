package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

const gasNeedWallet = "0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A"

// gasNeedFixture: a Sonic (146) service whose USDT is listed, one sell ad by
// seller, and helpers to seed trades on it. The clock is newService's
// 2026-05-23 12:00 UTC.
type gasNeedFixture struct {
	svc           *P2PTradeService
	esc           *mockEscrow
	db            *gorm.DB
	seller, buyer uuid.UUID
	adID          uuid.UUID
	now           time.Time
}

func newGasNeedFixture(t *testing.T) *gasNeedFixture {
	t.Helper()
	db := setupTradeDB(t)
	esc := newMockEscrow()
	svc := NewP2PTradeService(db, NewP2PEscrowRegistryFromEntries(map[int64]P2PRegistryEntry{
		146: {Escrow: esc, ChainKey: "sonic", Assets: []string{"USDT"}},
	}, "1"), &mockRates{}, &mockBus{}, 146)
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	svc = svc.WithClock(fixedClock{t: now})
	svc.SetTokenResolver(func(_ int64, symbol string) string {
		if symbol == "USDT" {
			return "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
		}
		return ""
	})
	f := &gasNeedFixture{svc: svc, esc: esc, db: db, seller: seedVerifiedUser(t, db), buyer: seedVerifiedUser(t, db), now: now}
	f.adID = f.seedAd(t, f.seller, models.P2PAdSideSell, models.P2PAdStatusActive, 146)
	return f
}

func (f *gasNeedFixture) seedAd(t *testing.T, seller uuid.UUID, side, status string, chainID int64) uuid.UUID {
	t.Helper()
	ad := &models.P2PAd{
		ID: uuid.New(), SellerID: seller, Side: side, Token: "USDT", ChainID: chainID,
		Amount: "100", Remaining: "100", Reserved: "0", FiatCurrency: "NGN", Rate: "1500", RateType: "fixed",
		PaymentMethodIDs: stringifyUUIDs([]uuid.UUID{uuid.New()}), Status: status,
	}
	if err := f.db.Create(ad).Error; err != nil {
		t.Fatalf("seed ad: %v", err)
	}
	return ad.ID
}

func (f *gasNeedFixture) seedTrade(t *testing.T, status string, expiresIn time.Duration, chainID int64) uuid.UUID {
	t.Helper()
	id := uuid.New()
	tr := &models.P2PTrade{
		ID: id, OnChainTradeID: fmt.Sprintf("0x%x", id[:]), AdID: f.adID, BuyerID: f.buyer, SellerID: f.seller, Token: "USDT", ChainID: chainID,
		TokenAmount: "10", FiatAmount: "15000", FiatCurrency: "NGN", Rate: "1500", PaymentMethodID: uuid.New(),
		Status: status, ExpiresAt: f.now.Add(expiresIn), CreatedAt: f.now, UpdatedAt: f.now,
	}
	if err := f.db.Create(tr).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
	return tr.ID
}

func (f *gasNeedFixture) check(caller uuid.UUID, req P2PGasRequest) error {
	return f.svc.RequireP2PGasNeed(context.Background(), &models.User{ID: caller, WalletAddress: gasNeedWallet}, gasNeedWallet, req)
}

func wantGasNeed(t *testing.T, name string, err, want error) {
	t.Helper()
	if want == nil && err != nil || want != nil && !errors.Is(err, want) {
		t.Fatalf("%s: err = %v, want %v", name, err, want)
	}
}

func TestP2PGasNeed_TradeActions(t *testing.T) {
	f := newGasNeedFixture(t)
	stranger := uuid.New()
	pending := f.seedTrade(t, models.P2PTradeStatusPendingPayment, 30*time.Minute, 146)
	expired := f.seedTrade(t, models.P2PTradeStatusPendingPayment, -time.Minute, 146)
	paid := f.seedTrade(t, models.P2PTradeStatusPaid, 30*time.Minute, 146)
	releasing := f.seedTrade(t, models.P2PTradeStatusReleasing, 30*time.Minute, 146)
	released := f.seedTrade(t, models.P2PTradeStatusReleased, 30*time.Minute, 146)
	bsc := f.seedTrade(t, models.P2PTradeStatusPendingPayment, 30*time.Minute, 56)

	for _, tc := range []struct {
		name   string
		caller uuid.UUID
		action P2PGasAction
		trade  uuid.UUID
		want   error
	}{
		{"mark_paid buyer pending", f.buyer, P2PGasMarkPaid, pending, nil},
		{"mark_paid seller", f.seller, P2PGasMarkPaid, pending, ErrP2PGasNotNeeded},
		{"mark_paid already paid", f.buyer, P2PGasMarkPaid, paid, ErrP2PGasNotNeeded},
		{"release seller paid", f.seller, P2PGasRelease, paid, nil},
		{"release seller early", f.seller, P2PGasRelease, pending, nil},
		{"release seller retry", f.seller, P2PGasRelease, releasing, nil},
		{"release buyer", f.buyer, P2PGasRelease, paid, ErrP2PGasNotNeeded},
		{"release released", f.seller, P2PGasRelease, released, ErrP2PGasNotNeeded},
		{"cancel expired buyer", f.buyer, P2PGasCancelTrade, expired, nil},
		{"cancel expired seller", f.seller, P2PGasCancelTrade, expired, nil},
		{"cancel before expiry", f.buyer, P2PGasCancelTrade, pending, ErrP2PGasNotNeeded},
		{"cancel stranger", stranger, P2PGasCancelTrade, expired, ErrP2PGasNotNeeded},
		{"dispute buyer paid", f.buyer, P2PGasDispute, paid, nil},
		{"dispute seller pending", f.seller, P2PGasDispute, pending, nil},
		{"dispute stranger", stranger, P2PGasDispute, paid, ErrP2PGasNotNeeded},
		{"dispute released", f.buyer, P2PGasDispute, released, ErrP2PGasNotNeeded},
		{"not on sonic", f.buyer, P2PGasMarkPaid, bsc, ErrP2PGasNotNeeded},
		{"missing tradeId", f.buyer, P2PGasMarkPaid, uuid.Nil, ErrInvalidParams},
		{"unknown tradeId", f.buyer, P2PGasMarkPaid, uuid.New(), ErrInvalidParams},
	} {
		wantGasNeed(t, tc.name, f.check(tc.caller, P2PGasRequest{Action: tc.action, TradeID: tc.trade}), tc.want)
	}
}

func TestP2PGasNeed_AdActions(t *testing.T) {
	f := newGasNeedFixture(t)
	paused := f.seedAd(t, f.seller, models.P2PAdSideSell, models.P2PAdStatusPaused, 146)
	cancelled := f.seedAd(t, f.seller, models.P2PAdSideSell, models.P2PAdStatusCancelled, 146)
	buyAd := f.seedAd(t, f.seller, models.P2PAdSideBuy, models.P2PAdStatusActive, 146)
	withTrade := f.seedAd(t, f.seller, models.P2PAdSideSell, models.P2PAdStatusActive, 146)
	f.adID = withTrade
	f.seedTrade(t, models.P2PTradeStatusPaid, 30*time.Minute, 146)

	for _, tc := range []struct {
		name   string
		caller uuid.UUID
		action P2PGasAction
		ad     uuid.UUID
		want   error
	}{
		{"open paused ad", f.buyer, P2PGasOpenTrade, paused, ErrP2PGasNotNeeded},
		{"open active ad", f.buyer, P2PGasOpenTrade, buyAd, nil},
		{"open own ad", f.seller, P2PGasOpenTrade, buyAd, ErrP2PGasNotNeeded},
		{"open cancelled ad", f.buyer, P2PGasOpenTrade, cancelled, ErrP2PGasNotNeeded},
		{"open missing adId", f.buyer, P2PGasOpenTrade, uuid.Nil, ErrInvalidParams},
		{"cancel own paused sell ad", f.seller, P2PGasCancelAd, paused, nil},
		{"cancel someone else's ad", f.buyer, P2PGasCancelAd, paused, ErrP2PGasNotNeeded},
		{"cancel cancelled ad", f.seller, P2PGasCancelAd, cancelled, ErrP2PGasNotNeeded},
		{"cancel buy ad (DB-only)", f.seller, P2PGasCancelAd, buyAd, ErrP2PGasNotNeeded},
		{"cancel ad with open trade", f.seller, P2PGasCancelAd, withTrade, ErrP2PGasNotNeeded},
		{"cancel unknown ad", f.seller, P2PGasCancelAd, uuid.New(), ErrInvalidParams},
	} {
		wantGasNeed(t, tc.name, f.check(tc.caller, P2PGasRequest{Action: tc.action, AdID: tc.ad}), tc.want)
	}
}

func TestP2PGasNeed_PoolActions(t *testing.T) {
	f := newGasNeedFixture(t)
	wantGasNeed(t, "approve", f.check(f.buyer, P2PGasRequest{Action: P2PGasApprove}), nil)
	wantGasNeed(t, "deposit", f.check(f.buyer, P2PGasRequest{Action: P2PGasDeposit}), nil)

	f.esc.balance = OnChainBalance{Available: big.NewInt(0), Locked: big.NewInt(5)}
	wantGasNeed(t, "withdraw with nothing available", f.check(f.seller, P2PGasRequest{Action: P2PGasWithdraw}), ErrP2PGasNotNeeded)
	f.esc.balance = OnChainBalance{Available: big.NewInt(1), Locked: big.NewInt(0)}
	wantGasNeed(t, "withdraw with a balance", f.check(f.seller, P2PGasRequest{Action: P2PGasWithdraw}), nil)
	f.esc.balanceErr = errors.New("rpc down")
	if err := f.check(f.seller, P2PGasRequest{Action: P2PGasWithdraw}); err == nil ||
		errors.Is(err, ErrP2PGasNotNeeded) || errors.Is(err, ErrInvalidParams) {
		t.Fatalf("a failed balance read must surface as ours, got %v", err)
	}
}
