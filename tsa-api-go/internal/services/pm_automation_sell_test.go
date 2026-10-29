package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	// onCall runs at the moment the gateway is asked to move money, so a test
	// can observe the rest of the world's state at exactly that instant —
	// which is how the P2P forward proves the ledger credit came first.
	onCall func()
}

func (f *fakeNombaGateway) InitiatePayout(_ context.Context, r PayoutRequest) (string, error) {
	f.calls = append(f.calls, r)
	if f.onCall != nil {
		f.onCall()
	}
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
	// Sell payouts re-read the trade first: by default it is open on-chain and, through the mock's
	// default releaser, releasable by the release key.
	if esc, err := svc.escrows.For(146); err == nil {
		if m, ok := esc.(*mockEscrow); ok && m.onChainTrade == nil {
			m.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending}
		}
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
	// Pre-merge review M-3: the expiry canceller refunds the deposit, so the
	// marker must never invite a hand payout (both would reach the user), and
	// it names no provider.
	r := strings.ToLower(persisted.CancelReason)
	if !strings.HasPrefix(persisted.CancelReason, pmPayoutFailedReason) || strings.Contains(r, "manual") ||
		strings.Contains(r, "nomba") || !strings.Contains(r, "refunded automatically") {
		t.Errorf("CancelReason = %q, want the failed prefix, the automatic refund, no manual route, no provider", persisted.CancelReason)
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

// TestInitiateSellPayout_PMTrade_ParentWithPMSender verifies the VA-only model:
// for a sell trade on a PM ad, InitiateSellPayout debits the parent account (no
// SubAccountID set) but stamps SenderName = pm.Name on the gateway request.
func TestInitiateSellPayout_PMTrade_ParentWithPMSender(t *testing.T) {
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
	// VA model: no sub-account routing — SubAccountID must stay empty (parent debit).
	if call.SubAccountID != "" {
		t.Errorf("SubAccountID = %q, want empty (VA model debits parent)", call.SubAccountID)
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

// Review C-1: once the expiry canceller has decided a trade, no payout may start.
func TestInitiateSellPayout_RefusesATradeTheCancellerDecided(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)
	trade := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), "1500.00")
	db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).Update("dropped_at", time.Now())
	gw := &fakeNombaGateway{payoutID: "po_1"}
	auto, _ := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))
	if _, err := auto.InitiateSellPayout(context.Background(), trade.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
	if len(gw.calls) != 0 {
		t.Fatalf("gateway called %d times on a decided trade", len(gw.calls))
	}
}

// expiredSellTrade is a deposited sell trade 31 minutes past its expiry notice,
// with a gateway whose id write the test can make fail (the DB lost after the
// money moved).
func expiredSellTrade(t *testing.T) (*gorm.DB, *P2PTradeService, *models.P2PTrade, *fakeNombaGateway, *PMAutomationService, *bool) {
	t.Helper()
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	createSettlementForwardTable(t, db)
	svc, _, _ := newService(t, db)
	trade := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), "1500.00")
	notified := time.Now().Add(-31 * time.Minute)
	db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).Update("expired_notified_at", notified)
	trade.ExpiredNotifiedAt = &notified
	failIDWrite := new(bool)
	if err := db.Callback().Update().Before("gorm:update").Register("test:lose_payout_id", func(tx *gorm.DB) {
		if m, ok := tx.Statement.Dest.(map[string]any); ok && *failIDWrite {
			if _, ok := m["nomba_payout_id"]; ok {
				tx.AddError(errors.New("connection reset"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	gw := &fakeNombaGateway{payoutID: "po_lost"}
	auto, _ := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))
	return db, svc, trade, gw, auto, failIDWrite
}

// Review C-1 (nonce-before-broadcast for naira): the gateway sent the money,
// then the payout id never reached the DB. The durable "started" marker,
// committed before the gateway was asked, still refuses the canceller.
func TestInitiateSellPayout_LostIDWriteStillBlocksTheCanceller(t *testing.T) {
	_, svc, trade, gw, auto, failIDWrite := expiredSellTrade(t)
	*failIDWrite = true
	if _, err := auto.InitiateSellPayout(context.Background(), trade.ID); err == nil {
		t.Fatal("the id write failed but the payout reported success")
	}
	if len(gw.calls) != 1 {
		t.Fatalf("gateway called %d times, want 1", len(gw.calls))
	}
	if ok, _, err := svc.decideLegacyCancelLeg(context.Background(), trade, time.Now()); ok || err != nil {
		t.Fatalf("decide = %v %v: the canceller would refund a trade whose naira left", ok, err)
	}
}

// ...and a retry never asks the gateway for a second payout: the outcome is
// the webhook's to record (it matches on the trade-id reference).
func TestInitiateSellPayout_RetryAfterLostIDSendsNoSecondPayout(t *testing.T) {
	_, _, trade, gw, auto, failIDWrite := expiredSellTrade(t)
	*failIDWrite = true
	_, _ = auto.InitiateSellPayout(context.Background(), trade.ID)
	*failIDWrite = false
	if _, err := auto.InitiateSellPayout(context.Background(), trade.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("retry = %v, want ErrInvalidTransition (payout already started)", err)
	}
	if len(gw.calls) != 1 {
		t.Fatalf("gateway called %d times, want 1 — a second payout", len(gw.calls))
	}
}

// A provider refusal moved no money: the started marker comes off, and the
// operator may try again.
func TestInitiateSellPayout_ProviderRefusalAllowsARetry(t *testing.T) {
	_, _, trade, gw, auto, _ := expiredSellTrade(t)
	gw.err = &ProviderRejectedError{Err: errors.New("insufficient balance")}
	if _, err := auto.InitiateSellPayout(context.Background(), trade.ID); err == nil {
		t.Fatal("a refused payout reported success")
	}
	gw.err = nil
	if id, err := auto.InitiateSellPayout(context.Background(), trade.ID); err != nil || id != "po_lost" || len(gw.calls) != 2 {
		t.Fatalf("retry = %q %v calls=%d, want the payout sent", id, err, len(gw.calls))
	}
}

// The success webhook records a payout id the initiating call lost, and settles.
func TestHandlePayoutStatus_SuccessRecordsALostPayoutID(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)
	trade := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), "1500.00")
	db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).Update("payout_started_at", time.Now())
	setReleasedEvent(esc, trade)
	auto, _ := newPMAutomationServiceWithGateway(t, db, svc, &fakeNombaGateway{}, nameMap(nil))
	if err := auto.HandlePayoutStatus(context.Background(), PayoutStatus{
		EventID: "evt_lost", MerchantTxRef: trade.ID.String(), PayoutID: "po_from_webhook", Status: "success",
	}); err != nil {
		t.Fatal(err)
	}
	var got models.P2PTrade
	db.First(&got, "id = ?", trade.ID)
	if got.NombaPayoutID != "po_from_webhook" || got.Status != models.P2PTradeStatusReleased {
		t.Fatalf("payout id %q status %s, want the webhook's id recorded and released", got.NombaPayoutID, got.Status)
	}
}

// Re-review N-1: a timeout or 5xx says nothing about whether the transfer
// exists. The marker stays: no retry reaches the gateway, and the canceller
// refuses the refund.
func TestInitiateSellPayout_UnknownOutcomeKeepsTheMarker(t *testing.T) {
	for _, gwErr := range []error{context.DeadlineExceeded, errors.New("flutterwave direct-transfer: HTTP 502")} {
		_, svc, trade, gw, auto, _ := expiredSellTrade(t)
		gw.err = gwErr
		if _, err := auto.InitiateSellPayout(context.Background(), trade.ID); err == nil {
			t.Fatalf("%v: reported success", gwErr)
		}
		gw.err = nil
		if _, err := auto.InitiateSellPayout(context.Background(), trade.ID); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("%v: retry = %v, want ErrInvalidTransition", gwErr, err)
		}
		if len(gw.calls) != 1 {
			t.Fatalf("%v: gateway called %d times, want 1", gwErr, len(gw.calls))
		}
		if ok, _, err := svc.decideLegacyCancelLeg(context.Background(), trade, time.Now()); ok || err != nil {
			t.Fatalf("%v: decide = %v %v, want the refund refused", gwErr, ok, err)
		}
	}
}

// Re-review N-4: the rule the IT sell payout uses, shared — a refusal proves
// no transfer only on a reference's first attempt. On a retry it can mean
// "that reference already exists", Replayed or not.
func TestProviderRefusalIsFinal(t *testing.T) {
	refused := &ProviderRejectedError{Err: errors.New("HTTP 400")}
	replayed := &ProviderRejectedError{Err: errors.New("HTTP 400"), Replayed: true}
	for _, c := range []struct {
		err   error
		first bool
		want  bool
	}{
		{refused, true, true},
		{fmt.Errorf("wrapped: %w", refused), true, true},
		{refused, false, false},
		{replayed, false, false},
		{errors.New("timeout"), true, false},
	} {
		if got := providerRefusalIsFinal(c.err, c.first); got != c.want {
			t.Errorf("providerRefusalIsFinal(%v, first=%v) = %v, want %v", c.err, c.first, got, c.want)
		}
	}
}
