package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// seedOpenedPMTrade inserts a pending_payment trade whose on-chain open has
// landed (OpenTxHash set) on the configured test chain (146).
func seedOpenedPMTrade(t *testing.T, svc *P2PTradeService) *models.P2PTrade {
	t.Helper()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	trade := &models.P2PTrade{
		ID: uuid.New(), AdID: uuid.New(), SellerID: uuid.New(), BuyerID: uuid.New(),
		Token: "USDT", ChainID: 146, Funding: models.P2PTradeFundingPooled,
		TokenAmount: "1000000", FiatAmount: "1300", FiatCurrency: "NGN", Rate: "1300",
		PaymentMethodID: uuid.New(), Status: models.P2PTradeStatusPendingPayment,
		OpenTxHash: "0xopen",
		ExpiresAt:  now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	if err := svc.db.Create(trade).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
	return trade
}

// TestSystemMarkPaid_FlipsPendingToPaid verifies the PM-automation mark-paid
// path: a verified Nomba inbound flips pending_payment → paid with the ref
// recorded as provenance, and a second call is rejected (no double-flip).
func TestSystemMarkPaid_FlipsPendingToPaid(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)

	got, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt_1")
	if err != nil {
		t.Fatalf("SystemMarkPaid: %v", err)
	}
	if got.Status != models.P2PTradeStatusPaid {
		t.Errorf("status = %q, want paid", got.Status)
	}
	if got.ProofImageURL != "nomba:evt_1" {
		t.Errorf("proofImageURL = %q, want nomba:evt_1", got.ProofImageURL)
	}

	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status != models.P2PTradeStatusPaid {
		t.Errorf("persisted status = %q, want paid", persisted.Status)
	}

	// Second call: trade is no longer pending_payment → ErrInvalidTransition.
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt_1"); err == nil {
		t.Fatalf("expected ErrInvalidTransition on re-mark, got nil")
	}
}

// TestAutoRelease_SystemPath verifies the admin-EOA release path end-to-end on
// a paid PM trade: the unsigned adminRelease tx is signed+broadcast through the
// ReleaseSigner exactly once, the on-chain event is verified, and the trade is
// settled to released with the broadcast tx hash recorded.
func TestAutoRelease_SystemPath(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)
	// Advance it to paid (the AutoRelease precondition for a system release).
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt_1"); err != nil {
		t.Fatalf("SystemMarkPaid: %v", err)
	}

	// Money-invariant: AmountToBuyer + FeeToSystem must equal TokenAmount.
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	fee := big.NewInt(500)
	toBuyer := new(big.Int).Sub(tokenAmt, fee)
	esc.releasedEvent = &P2PTradeReleasedEvent{
		TradeID:       HashID(trade.ID),
		Buyer:         common.HexToAddress("0x000000000000000000000000000000000000beef"),
		AmountToBuyer: toBuyer,
		FeeToSystem:   fee,
	}

	signer := &fakeReleaseSigner{
		addr: "0x1111111111111111111111111111111111111111",
		hash: "0xrelease",
	}

	got, err := svc.AutoRelease(context.Background(), trade.ID, signer)
	if err != nil {
		t.Fatalf("AutoRelease: %v", err)
	}
	if got.Status != models.P2PTradeStatusReleased {
		t.Errorf("status = %q, want released", got.Status)
	}
	if got.ReleaseTxHash != "0xrelease" {
		t.Errorf("releaseTxHash = %q, want 0xrelease", got.ReleaseTxHash)
	}
	if len(signer.calls) != 1 {
		t.Errorf("signer called %d times, want exactly 1", len(signer.calls))
	}

	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status != models.P2PTradeStatusReleased {
		t.Errorf("persisted status = %q, want released", persisted.Status)
	}
	if persisted.ReleaseTxHash != "0xrelease" {
		t.Errorf("persisted releaseTxHash = %q, want 0xrelease", persisted.ReleaseTxHash)
	}
}

// TestAutoRelease_RollsBackOnBroadcastError verifies the optimistic-lock rollback:
// when SignAndBroadcast fails AFTER AutoRelease flips paid → releasing, the trade
// is reverted to its prior status (paid) rather than stranded in `releasing` for
// the reconciler's ~10-min stale window.
func TestAutoRelease_RollsBackOnBroadcastError(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt_1"); err != nil {
		t.Fatalf("SystemMarkPaid: %v", err)
	}

	signer := &fakeReleaseSigner{
		addr: "0x1111111111111111111111111111111111111111",
		err:  errors.New("rpc down"),
	}

	if _, err := svc.AutoRelease(context.Background(), trade.ID, signer); err == nil {
		t.Fatalf("expected AutoRelease error on broadcast failure, got nil")
	}

	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status != models.P2PTradeStatusPaid {
		t.Errorf("status = %q, want paid (rolled back, not stranded in releasing)", persisted.Status)
	}
}

// TestAutoRelease_RejectsBadMoneyInvariant verifies the money-invariant guard in
// confirmReleaseCore: if the on-chain TradeReleased event's AmountToBuyer +
// FeeToSystem does not equal the locked TokenAmount, AutoRelease refuses to mark
// the trade released. (Verify succeeds, so the trade is left in `releasing`; the
// reconciler reconciles it against on-chain truth — we only assert it is NOT
// prematurely declared released.)
func TestAutoRelease_RejectsBadMoneyInvariant(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt_1"); err != nil {
		t.Fatalf("SystemMarkPaid: %v", err)
	}

	// TokenAmount is "1000000"; emit 1 + 1 = 2, which violates the invariant.
	esc.releasedEvent = &P2PTradeReleasedEvent{
		TradeID:       HashID(trade.ID),
		Buyer:         common.HexToAddress("0x000000000000000000000000000000000000beef"),
		AmountToBuyer: big.NewInt(1),
		FeeToSystem:   big.NewInt(1),
	}

	signer := &fakeReleaseSigner{
		addr: "0x1111111111111111111111111111111111111111",
		hash: "0xrelease",
	}

	if _, err := svc.AutoRelease(context.Background(), trade.ID, signer); err == nil {
		t.Fatalf("expected AutoRelease error on money-invariant violation, got nil")
	}

	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status == models.P2PTradeStatusReleased {
		t.Errorf("status = %q, must NOT be released on invariant violation", persisted.Status)
	}
}

// TestAutoRelease_FailsClosedOnUnparseableTokenAmount verifies the fail-closed
// money-invariant guard: when a trade's TokenAmount cannot be parsed as a
// big.Int, confirmReleaseCore must NOT skip the invariant and confirm the
// release. AutoRelease must error and leave the trade un-released (it stays in
// `releasing` for the reconciler) regardless of the on-chain event.
func TestAutoRelease_FailsClosedOnUnparseableTokenAmount(t *testing.T) {
	for _, amt := range []string{"1.5", ""} {
		amt := amt
		t.Run("amount="+amt, func(t *testing.T) {
			db := setupTradeDB(t)
			svc, esc, _ := newService(t, db)
			trade := seedOpenedPMTrade(t, svc)
			// Corrupt the token amount to an unparseable value after seeding.
			if err := db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).
				Update("token_amount", amt).Error; err != nil {
				t.Fatalf("set token_amount: %v", err)
			}
			if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt_1"); err != nil {
				t.Fatalf("SystemMarkPaid: %v", err)
			}

			// Fake escrow returns any well-formed event — it must still be blocked.
			esc.releasedEvent = &P2PTradeReleasedEvent{
				TradeID:       HashID(trade.ID),
				Buyer:         common.HexToAddress("0x000000000000000000000000000000000000beef"),
				AmountToBuyer: big.NewInt(999500),
				FeeToSystem:   big.NewInt(500),
			}
			signer := &fakeReleaseSigner{
				addr: "0x1111111111111111111111111111111111111111",
				hash: "0xrelease",
			}

			if _, err := svc.AutoRelease(context.Background(), trade.ID, signer); err == nil {
				t.Fatalf("expected AutoRelease error on unparseable token amount %q, got nil", amt)
			}

			var persisted models.P2PTrade
			db.First(&persisted, "id = ?", trade.ID)
			if persisted.Status == models.P2PTradeStatusReleased {
				t.Errorf("status = %q, must NOT be released on unparseable amount", persisted.Status)
			}
		})
	}
}
