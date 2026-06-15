package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// fakeNombaGateway records InitiatePayout calls and returns a canned id/error.
type fakeNombaGateway struct {
	payoutID string
	err      error
	calls    []PayoutRequest
}

func (f *fakeNombaGateway) InitiatePayout(_ context.Context, r PayoutRequest) (string, error) {
	f.calls = append(f.calls, r)
	if f.err != nil {
		return "", f.err
	}
	return f.payoutID, nil
}

// seedSellTradeOnAd inserts a pending_payment, deposited-by-taker (funding=1),
// on-chain-opened sell trade with a verified payout destination. This is the
// shape InitiateSellPayout/HandlePayoutStatus operate on.
func seedSellTradeOnAd(t *testing.T, db *gorm.DB, buyerID, sellerID uuid.UUID, fiat string) *models.P2PTrade {
	t.Helper()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	id := uuid.New()
	trade := &models.P2PTrade{
		ID: id, AdID: uuid.New(), SellerID: sellerID, BuyerID: buyerID,
		Token: "USDT", ChainID: 146, Funding: models.P2PTradeFundingDepositedByTaker,
		TokenAmount: "1000000", FiatAmount: fiat, FiatCurrency: "NGN", Rate: "1300",
		PaymentMethodID: uuid.New(), Status: models.P2PTradeStatusPendingPayment,
		OnChainTradeID: HashIDHex(id), OpenTxHash: "0xopen",
		PayoutAccountNumber: "0123456789", PayoutBank: "044", PayoutAccountName: "JOHN DOE",
		ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(trade).Error; err != nil {
		t.Fatalf("seed sell trade: %v", err)
	}
	return trade
}

// newPMAutomationServiceWithGateway builds the automation service with an
// injectable Nomba gateway (the buy-flow harness passes a no-op gateway).
func newPMAutomationServiceWithGateway(t *testing.T, db *gorm.DB, svc *P2PTradeService, gw NombaGateway, name PMNameLookup) (*PMAutomationService, *fakeReleaseSigner) {
	t.Helper()
	signer := &fakeReleaseSigner{
		addr: "0x1111111111111111111111111111111111111111",
		hash: "0xrelease",
	}
	pmSvc := NewPMService(db)
	return NewPMAutomationService(db, pmSvc, svc, signer, name, gw), signer
}

// ─── InitiateSellPayout ──────────────────────────────────────────────────────

func TestInitiateSellPayout_HappyPath(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)

	buyerID := uuid.New()
	trade := seedSellTradeOnAd(t, db, buyerID, uuid.New(), "1500.00")

	gw := &fakeNombaGateway{payoutID: "po_1"}
	auto, _ := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	got, err := auto.InitiateSellPayout(context.Background(), trade.ID)
	if err != nil {
		t.Fatalf("InitiateSellPayout: %v", err)
	}
	if got != "po_1" {
		t.Errorf("payoutID = %q, want po_1", got)
	}

	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.NombaPayoutID != "po_1" {
		t.Errorf("nomba_payout_id = %q, want po_1", persisted.NombaPayoutID)
	}

	if len(gw.calls) != 1 {
		t.Fatalf("gateway called %d times, want 1", len(gw.calls))
	}
	call := gw.calls[0]
	if call.AccountNumber != "0123456789" {
		t.Errorf("call.AccountNumber = %q, want 0123456789", call.AccountNumber)
	}
	if call.BankCode != "044" {
		t.Errorf("call.BankCode = %q, want 044", call.BankCode)
	}
	if call.AmountNGN != "1500.00" {
		t.Errorf("call.AmountNGN = %q, want 1500.00", call.AmountNGN)
	}
	if call.Reference != trade.ID.String() {
		t.Errorf("call.Reference = %q, want %q", call.Reference, trade.ID.String())
	}
}

func TestInitiateSellPayout_AlreadyInitiated_NoDoublePay(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)

	trade := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), "1500.00")
	if err := db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).
		Update("nomba_payout_id", "po_x").Error; err != nil {
		t.Fatalf("preset payout id: %v", err)
	}

	gw := &fakeNombaGateway{payoutID: "po_new"}
	auto, _ := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	got, err := auto.InitiateSellPayout(context.Background(), trade.ID)
	if err != nil {
		t.Fatalf("InitiateSellPayout: %v", err)
	}
	if got != "po_x" {
		t.Errorf("payoutID = %q, want po_x (existing, no re-init)", got)
	}
	if len(gw.calls) != 0 {
		t.Errorf("gateway called %d times, want 0 (must not double-pay)", len(gw.calls))
	}
}

func TestInitiateSellPayout_BadState(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)

	trade := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), "1500.00")
	// Make it a non-deposited (pooled) trade — wrong funding for the sell flow.
	if err := db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).
		Update("funding", models.P2PTradeFundingPooled).Error; err != nil {
		t.Fatalf("set funding: %v", err)
	}

	gw := &fakeNombaGateway{payoutID: "po_1"}
	auto, _ := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	_, err := auto.InitiateSellPayout(context.Background(), trade.ID)
	if err == nil {
		t.Fatal("InitiateSellPayout: want error, got nil")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("err = %v, want ErrInvalidTransition", err)
	}
	if len(gw.calls) != 0 {
		t.Errorf("gateway called %d times, want 0", len(gw.calls))
	}
}

// ─── HandlePayoutStatus ──────────────────────────────────────────────────────

func TestHandlePayoutStatus_Success_Releases(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	trade := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), "1500.00")
	if err := db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).
		Update("nomba_payout_id", "po_1").Error; err != nil {
		t.Fatalf("preset payout id: %v", err)
	}
	setReleasedEvent(esc, trade)

	gw := &fakeNombaGateway{}
	auto, signer := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	err := auto.HandlePayoutStatus(context.Background(), PayoutStatus{
		EventID: "evt_1", PayoutID: "po_1", Status: "success",
	})
	if err != nil {
		t.Fatalf("HandlePayoutStatus: %v", err)
	}

	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status != models.P2PTradeStatusReleased {
		t.Errorf("status = %q, want released", persisted.Status)
	}
	if len(signer.calls) != 1 {
		t.Errorf("signer called %d times, want 1", len(signer.calls))
	}
}

func TestHandlePayoutStatus_Failed_NoRelease(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	trade := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), "1500.00")
	if err := db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).
		Update("nomba_payout_id", "po_1").Error; err != nil {
		t.Fatalf("preset payout id: %v", err)
	}
	setReleasedEvent(esc, trade)

	gw := &fakeNombaGateway{}
	auto, signer := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	err := auto.HandlePayoutStatus(context.Background(), PayoutStatus{
		EventID: "evt_1", PayoutID: "po_1", Status: "failed",
	})
	if err != nil {
		t.Fatalf("HandlePayoutStatus: %v", err)
	}

	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status != models.P2PTradeStatusPendingPayment {
		t.Errorf("status = %q, want pending_payment (must not release on failed)", persisted.Status)
	}
	if len(signer.calls) != 0 {
		t.Errorf("signer called %d times, want 0 (must not release on failed)", len(signer.calls))
	}
	// A failure marker is recorded so the manual queue / operator can see it.
	if persisted.CancelReason == "" {
		t.Errorf("CancelReason empty, want a payout-failure marker")
	}
}

func TestHandlePayoutStatus_Pending_NoOp(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	trade := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), "1500.00")
	if err := db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).
		Update("nomba_payout_id", "po_1").Error; err != nil {
		t.Fatalf("preset payout id: %v", err)
	}
	setReleasedEvent(esc, trade)

	gw := &fakeNombaGateway{}
	auto, signer := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	err := auto.HandlePayoutStatus(context.Background(), PayoutStatus{
		EventID: "evt_1", PayoutID: "po_1", Status: "pending",
	})
	if err != nil {
		t.Fatalf("HandlePayoutStatus: %v", err)
	}

	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status != models.P2PTradeStatusPendingPayment {
		t.Errorf("status = %q, want pending_payment (no-op)", persisted.Status)
	}
	if len(signer.calls) != 0 {
		t.Errorf("signer called %d times, want 0", len(signer.calls))
	}
}

func TestHandlePayoutStatus_UnknownPayout_NoOp(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)

	gw := &fakeNombaGateway{}
	auto, signer := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	err := auto.HandlePayoutStatus(context.Background(), PayoutStatus{
		EventID: "evt_1", PayoutID: "po_does_not_exist", Status: "success",
	})
	if err != nil {
		t.Fatalf("HandlePayoutStatus: %v (want nil — unknown payout is a no-op)", err)
	}
	if len(signer.calls) != 0 {
		t.Errorf("signer called %d times, want 0", len(signer.calls))
	}
}
