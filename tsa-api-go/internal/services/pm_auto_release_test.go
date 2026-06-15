package services

import (
	"context"
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
