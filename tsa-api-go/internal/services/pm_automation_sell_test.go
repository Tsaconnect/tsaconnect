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
	return NewPMAutomationService(db, pmSvc, svc, signer, name, gw, "TSA Connect"), signer
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

// TestHandlePayoutStatus_MatchesByMerchantTxRef verifies the deterministic
// match: a payout_success carrying merchantTxRef = the trade UUID releases that
// trade, with NO reliance on nomba_payout_id (left empty here on purpose).
func TestHandlePayoutStatus_MatchesByMerchantTxRef(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	trade := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), "1500.00")
	// nomba_payout_id deliberately left empty — match must come from merchantTxRef.
	setReleasedEvent(esc, trade)

	gw := &fakeNombaGateway{}
	auto, signer := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	err := auto.HandlePayoutStatus(context.Background(), PayoutStatus{
		EventID: "evt_1", MerchantTxRef: trade.ID.String(), Status: "success",
	})
	if err != nil {
		t.Fatalf("HandlePayoutStatus: %v", err)
	}

	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status != models.P2PTradeStatusReleased {
		t.Errorf("status = %q, want released (matched by merchantTxRef)", persisted.Status)
	}
	if len(signer.calls) != 1 {
		t.Errorf("signer called %d times, want 1", len(signer.calls))
	}
}

// TestHandlePayoutStatus_FallsBackToPayoutID verifies that when merchantTxRef is
// empty, the legacy nomba_payout_id lookup still resolves and releases the trade.
func TestHandlePayoutStatus_FallsBackToPayoutID(t *testing.T) {
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

	// No merchantTxRef → falls back to nomba_payout_id.
	err := auto.HandlePayoutStatus(context.Background(), PayoutStatus{
		EventID: "evt_1", PayoutID: "po_1", Status: "success",
	})
	if err != nil {
		t.Fatalf("HandlePayoutStatus: %v", err)
	}

	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status != models.P2PTradeStatusReleased {
		t.Errorf("status = %q, want released (fallback to payout id)", persisted.Status)
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

// TestHandlePayoutStatus_EmptyPayoutID_NoOp verifies the empty-payout-id guard.
// Without the guard, First(&trade, "nomba_payout_id = ?", "") would match any
// trade whose nomba_payout_id is empty (the vast majority), then release it on
// Status=="success". The guard must make this a no-op before the DB is touched.
func TestHandlePayoutStatus_EmptyPayoutID_NoOp(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	// Seed a trade with an empty nomba_payout_id in a releasable state so that,
	// without the guard, the buggy query would match and release it.
	trade := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), "1500.00")
	// nomba_payout_id is deliberately left as "" (the default from the seeder).
	setReleasedEvent(esc, trade)

	gw := &fakeNombaGateway{}
	auto, signer := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	err := auto.HandlePayoutStatus(context.Background(), PayoutStatus{
		EventID: "evt_empty", PayoutID: "", Status: "success",
	})
	if err != nil {
		t.Fatalf("HandlePayoutStatus: %v (want nil — empty payout id is a no-op)", err)
	}

	// The seeded trade must NOT have been released.
	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status != models.P2PTradeStatusPendingPayment {
		t.Errorf("trade status = %q, want pending_payment (must not release on empty payout id)", persisted.Status)
	}
	if len(signer.calls) != 0 {
		t.Errorf("signer called %d times, want 0 (must not release on empty payout id)", len(signer.calls))
	}
}

// ─── Sub-account routing ────────────────────────────────────────────────────

// seedActivePMWithSubAccount inserts an active PM with a NombaSubAccountID set,
// so InitiateSellPayout can route the payout through the PM's sub-account.
func seedActivePMWithSubAccount(t *testing.T, db *gorm.DB, subAccountID, name string) *models.PaymentMerchant {
	t.Helper()
	pm := &models.PaymentMerchant{
		ID:                uuid.New(),
		UserID:            uuid.New(),
		Name:              name,
		Status:            models.PaymentMerchantStatusActive,
		NombaSubAccountID: subAccountID,
		NombaWalletID:     "wallet-" + subAccountID,
		PerTradeCapUSD:    0,
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed PM with sub-account: %v", err)
	}
	return pm
}

// seedSellTradeOnPMAd inserts a deposited, pending_payment sell trade whose ad
// belongs to the given PM. This is the shape InitiateSellPayout needs for the
// sub-account routing path.
func seedSellTradeOnPMAd(t *testing.T, db *gorm.DB, pmID, buyerID, sellerID uuid.UUID, fiat string) *models.P2PTrade {
	t.Helper()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	ad := &models.P2PAd{
		ID: uuid.New(), PMID: &pmID, SellerID: sellerID,
		Side: models.P2PAdSideBuy, Token: "USDT", ChainID: 146,
		Amount: "1000000", Remaining: "1000000", Reserved: "0",
		FiatCurrency: "NGN", Rate: "1300", RateType: models.P2PAdRateTypeFixed,
		Status: models.P2PAdStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(ad).Error; err != nil {
		t.Fatalf("seed PM buy ad for sell trade: %v", err)
	}
	id := uuid.New()
	trade := &models.P2PTrade{
		ID: id, AdID: ad.ID, SellerID: sellerID, BuyerID: buyerID,
		Token: "USDT", ChainID: 146, Funding: models.P2PTradeFundingDepositedByTaker,
		TokenAmount: "1000000", FiatAmount: fiat, FiatCurrency: "NGN", Rate: "1300",
		PaymentMethodID: uuid.New(), Status: models.P2PTradeStatusPendingPayment,
		OnChainTradeID: HashIDHex(id), OpenTxHash: "0xopen",
		PayoutAccountNumber: "0123456789", PayoutBank: "044", PayoutAccountName: "JOHN DOE",
		ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(trade).Error; err != nil {
		t.Fatalf("seed sell trade on PM ad: %v", err)
	}
	return trade
}

// TestInitiateSellPayout_PMTrade_UsesSubAccountID verifies that for a sell trade
// on a PM ad, InitiateSellPayout resolves the PM, sets SubAccountID =
// pm.NombaSubAccountID, and sets SenderName = pm.Name on the gateway request.
func TestInitiateSellPayout_PMTrade_UsesSubAccountID(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)

	pm := seedActivePMWithSubAccount(t, db, "sub-acct-pm-42", "Acme Merchants Ltd")
	trade := seedSellTradeOnPMAd(t, db, pm.ID, uuid.New(), uuid.New(), "2000.00")

	gw := &fakeNombaGateway{payoutID: "po_pm_1"}
	auto, _ := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	got, err := auto.InitiateSellPayout(context.Background(), trade.ID)
	if err != nil {
		t.Fatalf("InitiateSellPayout (PM trade): %v", err)
	}
	if got != "po_pm_1" {
		t.Errorf("payoutID = %q, want po_pm_1", got)
	}
	if len(gw.calls) != 1 {
		t.Fatalf("gateway called %d times, want 1", len(gw.calls))
	}
	call := gw.calls[0]
	// SubAccountID must match the PM's sub-account id.
	if call.SubAccountID != "sub-acct-pm-42" {
		t.Errorf("SubAccountID = %q, want sub-acct-pm-42", call.SubAccountID)
	}
	// SenderName must be the PM's name, not the service-level senderName.
	if call.SenderName != "Acme Merchants Ltd" {
		t.Errorf("SenderName = %q, want Acme Merchants Ltd (pm.Name)", call.SenderName)
	}
	// AccountName is the trade's recipient name — unchanged.
	if call.AccountName != "JOHN DOE" {
		t.Errorf("AccountName = %q, want JOHN DOE", call.AccountName)
	}
}

// TestInitiateSellPayout_NonPMTrade_NoSubAccountID verifies that for a
// non-PM sell trade (no ad.pm_id), InitiateSellPayout leaves SubAccountID
// empty (parent Nomba path) and uses the service-level SenderName.
func TestInitiateSellPayout_NonPMTrade_NoSubAccountID(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)

	// seedSellTradeOnAd creates a trade with NO ad.pm_id (ad is not linked to a PM).
	trade := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), "1800.00")

	gw := &fakeNombaGateway{payoutID: "po_nonpm_1"}
	auto, _ := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	got, err := auto.InitiateSellPayout(context.Background(), trade.ID)
	if err != nil {
		t.Fatalf("InitiateSellPayout (non-PM trade): %v", err)
	}
	if got != "po_nonpm_1" {
		t.Errorf("payoutID = %q, want po_nonpm_1", got)
	}
	if len(gw.calls) != 1 {
		t.Fatalf("gateway called %d times, want 1", len(gw.calls))
	}
	call := gw.calls[0]
	// Non-PM: SubAccountID must be empty → parent Nomba path.
	if call.SubAccountID != "" {
		t.Errorf("SubAccountID = %q, want empty (non-PM parent path)", call.SubAccountID)
	}
	// SenderName falls through to the service-level senderName ("TSA Connect").
	if call.SenderName != "TSA Connect" {
		t.Errorf("SenderName = %q, want TSA Connect (service-level, non-PM path)", call.SenderName)
	}
}

// TestInitiateSellPayout_EmptyGatewayID_Errors verifies that a gateway returning
// ("", nil) is treated as an error and does NOT poison the trade's nomba_payout_id.
func TestInitiateSellPayout_EmptyGatewayID_Errors(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)

	trade := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), "1500.00")

	// Gateway returns ("", nil) — simulates a buggy/empty response with no error.
	gw := &fakeNombaGateway{payoutID: ""}
	auto, _ := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	_, err := auto.InitiateSellPayout(context.Background(), trade.ID)
	if err == nil {
		t.Fatal("InitiateSellPayout: want error for empty gateway payout id, got nil")
	}

	// The trade's nomba_payout_id must remain empty — no poisoning.
	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.NombaPayoutID != "" {
		t.Errorf("nomba_payout_id = %q, want empty (must not persist empty payout id)", persisted.NombaPayoutID)
	}
}
