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

// ─── TestListBuyManualQueue ───────────────────────────────────────────────────

// TestListBuyManualQueue verifies the queue lists only unmatched PM buy trades.
// Seeds:
//   - tradeUnmatched: pending_payment, pooled, open_tx_hash set, nomba_inbound_event_id='' → APPEARS
//   - tradeMatched:   same but nomba_inbound_event_id='evt_1'                              → HIDDEN
//   - tradeNonPM:     pending_payment, pooled, open_tx_hash set, but ad has pm_id=NULL     → HIDDEN
func TestListBuyManualQueue(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	buyerA := uuid.New()
	buyerB := uuid.New()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)

	// PM sell ad (pm_id set)
	pmAd := seedPMSellAd(t, db, pm.ID, uuid.New())

	// tradeUnmatched: on PM ad, no inbound event → should appear in the queue.
	tradeUnmatched := seedBuyTradeOnAd(t, db, pmAd, buyerA, "1500.00")
	// The seeder does not set nomba_inbound_event_id, which defaults to "" in the
	// SQLite schema — no further update needed.

	// tradeMatched: on PM ad, inbound event set → should NOT appear.
	tradeMatched := seedBuyTradeOnAd(t, db, pmAd, buyerB, "2000.00")
	if err := db.Exec("UPDATE p2p_trades SET nomba_inbound_event_id = 'evt_1' WHERE id = ?", tradeMatched.ID).Error; err != nil {
		t.Fatalf("set nomba_inbound_event_id: %v", err)
	}

	// tradeNonPM: ad WITHOUT a pm_id → should NOT appear.
	nonPMAd := &models.P2PAd{
		ID: uuid.New(), SellerID: uuid.New(),
		Side: models.P2PAdSideSell, Token: "USDT", ChainID: 146,
		Amount: "1000000", Remaining: "1000000", Reserved: "0",
		FiatCurrency: "NGN", Rate: "1300", RateType: models.P2PAdRateTypeFixed,
		Status: models.P2PAdStatusActive, CreatedAt: now, UpdatedAt: now,
		// PMID is nil → non-PM ad
	}
	if err := db.Create(nonPMAd).Error; err != nil {
		t.Fatalf("seed non-PM ad: %v", err)
	}
	seedBuyTradeOnAd(t, db, nonPMAd, uuid.New(), "1300.00")

	// Build automation service with a stubbed name lookup.
	nameLookup := nameMap(map[uuid.UUID]string{
		buyerA: "ALICE WONDER",
		buyerB: "BOB SMITH",
	})
	auto, _ := newPMAutomationService(t, db, nil, svc, nameLookup)

	items, err := auto.ListBuyManualQueue(context.Background())
	if err != nil {
		t.Fatalf("ListBuyManualQueue: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item in manual queue, got %d: %+v", len(items), items)
	}

	item := items[0]
	if item.TradeID != tradeUnmatched.ID {
		t.Errorf("tradeId = %v, want %v", item.TradeID, tradeUnmatched.ID)
	}
	if item.AdID != pmAd.ID {
		t.Errorf("adId = %v, want %v", item.AdID, pmAd.ID)
	}
	if item.BuyerID != buyerA {
		t.Errorf("buyerId = %v, want %v", item.BuyerID, buyerA)
	}
	if item.BuyerName != "ALICE WONDER" {
		t.Errorf("buyerName = %q, want ALICE WONDER", item.BuyerName)
	}
	if item.Token != "USDT" {
		t.Errorf("token = %q, want USDT", item.Token)
	}
	if item.TokenAmount != tradeUnmatched.TokenAmount {
		t.Errorf("tokenAmount = %q, want %q", item.TokenAmount, tradeUnmatched.TokenAmount)
	}
	if item.FiatAmount != "1500.00" {
		t.Errorf("fiatAmount = %q, want 1500.00", item.FiatAmount)
	}
}

// TestListBuyManualQueue_SurfacesPaidOrphan verifies the paid-orphan recovery:
// a PM buy trade left at `paid` with no release (AutoRelease failed after
// SystemMarkPaid) is invisible to the timeout watchdog and reconciler, so the
// manual queue must surface it. A `paid` trade that already released
// (release_tx_hash set) must NOT appear.
func TestListBuyManualQueue_SurfacesPaidOrphan(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	pmAd := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerOrphan := uuid.New()
	buyerReleased := uuid.New()

	// paidOrphan: paid, empty release_tx_hash, empty nomba_inbound_event_id → APPEARS.
	paidOrphan := seedBuyTradeOnAd(t, db, pmAd, buyerOrphan, "1500.00")
	if err := db.Exec("UPDATE p2p_trades SET status = ? WHERE id = ?",
		models.P2PTradeStatusPaid, paidOrphan.ID).Error; err != nil {
		t.Fatalf("set paid: %v", err)
	}

	// paidReleased: paid but release_tx_hash set → HIDDEN (already released).
	paidReleased := seedBuyTradeOnAd(t, db, pmAd, buyerReleased, "2500.00")
	if err := db.Exec("UPDATE p2p_trades SET status = ?, release_tx_hash = '0xdone' WHERE id = ?",
		models.P2PTradeStatusPaid, paidReleased.ID).Error; err != nil {
		t.Fatalf("set released: %v", err)
	}

	auto, _ := newPMAutomationService(t, db, nil, svc, nameMap(map[uuid.UUID]string{
		buyerOrphan:   "ORPHAN OPS",
		buyerReleased: "DONE DEAL",
	}))

	items, err := auto.ListBuyManualQueue(context.Background())
	if err != nil {
		t.Fatalf("ListBuyManualQueue: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item (the paid orphan), got %d: %+v", len(items), items)
	}
	if items[0].TradeID != paidOrphan.ID {
		t.Errorf("tradeId = %v, want paid orphan %v", items[0].TradeID, paidOrphan.ID)
	}
}

// TestManualRelease_RecoversPaidOrphan verifies an operator can settle a
// surfaced `paid` orphan via ManualRelease: SystemMarkPaid returns
// ErrInvalidTransition (already paid, tolerated), AutoRelease accepts `paid`
// as an entry state, and the trade settles to released with the signer called
// exactly once.
func TestManualRelease_RecoversPaidOrphan(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")
	if err := db.Exec("UPDATE p2p_trades SET status = ? WHERE id = ?",
		models.P2PTradeStatusPaid, trade.ID).Error; err != nil {
		t.Fatalf("set paid: %v", err)
	}

	setReleasedEvent(esc, trade)
	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{buyerID: "ALICE"}))

	got, err := auto.ManualRelease(context.Background(), trade.ID, uuid.New())
	if err != nil {
		t.Fatalf("ManualRelease on paid orphan: %v", err)
	}
	if got.Status != models.P2PTradeStatusReleased {
		t.Errorf("status = %q, want released", got.Status)
	}
	if len(signer.calls) != 1 {
		t.Errorf("signer called %d times, want 1", len(signer.calls))
	}
}

// TestListBuyManualQueue_NameLookupError verifies that a name-lookup failure
// results in an empty BuyerName (skip-name-on-error) rather than an error,
// and the trade still appears in the queue.
func TestListBuyManualQueue_NameLookupError(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerID := uuid.New()
	seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")

	// Name lookup always errors.
	errLookup := PMNameLookup(func(_ context.Context, _ uuid.UUID) (string, error) {
		return "", errNameLookupFailed
	})
	auto, _ := newPMAutomationService(t, db, nil, svc, errLookup)

	items, err := auto.ListBuyManualQueue(context.Background())
	if err != nil {
		t.Fatalf("ListBuyManualQueue returned error on name-lookup failure (want skip): %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item even with name-lookup error, got %d", len(items))
	}
	if items[0].BuyerName != "" {
		t.Errorf("buyerName = %q, want empty (skip-name-on-error)", items[0].BuyerName)
	}
}

// errNameLookupFailed is a sentinel used in TestListBuyManualQueue_NameLookupError.
var errNameLookupFailed = errSentinel("name lookup failed")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// ─── TestManualRelease_Releases ───────────────────────────────────────────────

// TestManualRelease_Releases verifies that ManualRelease flips a pending
// PM buy trade to released and calls the signer exactly once.
func TestManualRelease_Releases(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")

	// Wire the fake escrow money-invariant so confirmReleaseCore is satisfied.
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	fee := big.NewInt(500)
	toBuyer := new(big.Int).Sub(tokenAmt, fee)
	esc.releasedEvent = &P2PTradeReleasedEvent{
		TradeID:       HashID(trade.ID),
		Buyer:         common.HexToAddress("0x000000000000000000000000000000000000beef"),
		AmountToBuyer: toBuyer,
		FeeToSystem:   fee,
	}

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{buyerID: "ALICE"}))
	adminID := uuid.New()

	got, err := auto.ManualRelease(context.Background(), trade.ID, adminID)
	if err != nil {
		t.Fatalf("ManualRelease: %v", err)
	}
	if got.Status != models.P2PTradeStatusReleased {
		t.Errorf("status = %q, want released", got.Status)
	}
	if len(signer.calls) != 1 {
		t.Errorf("signer called %d times, want 1", len(signer.calls))
	}

	// Verify persisted state.
	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status != models.P2PTradeStatusReleased {
		t.Errorf("persisted status = %q, want released", persisted.Status)
	}
	// ProofImageURL carries the manual ref (SystemMarkPaid stores the ref there).
	if persisted.ProofImageURL != "manual:"+adminID.String() {
		t.Errorf("proofImageURL = %q, want manual:%s", persisted.ProofImageURL, adminID)
	}
}

// TestManualRelease_AlreadyPaid_ToleratessTransition verifies that if the
// trade is already paid (SystemMarkPaid returns ErrInvalidTransition), the
// release still proceeds rather than returning an error.
func TestManualRelease_AlreadyPaid_ToleratesTransition(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")

	// Advance the trade to paid manually so SystemMarkPaid will get ErrInvalidTransition.
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "pre:paid"); err != nil {
		t.Fatalf("pre-advance to paid: %v", err)
	}

	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	fee := big.NewInt(500)
	toBuyer := new(big.Int).Sub(tokenAmt, fee)
	esc.releasedEvent = &P2PTradeReleasedEvent{
		TradeID:       HashID(trade.ID),
		Buyer:         common.HexToAddress("0x000000000000000000000000000000000000beef"),
		AmountToBuyer: toBuyer,
		FeeToSystem:   fee,
	}

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{buyerID: "ALICE"}))
	adminID := uuid.New()

	got, err := auto.ManualRelease(context.Background(), trade.ID, adminID)
	if err != nil {
		t.Fatalf("ManualRelease on already-paid trade: %v (want nil — tolerates ErrInvalidTransition)", err)
	}
	if got.Status != models.P2PTradeStatusReleased {
		t.Errorf("status = %q, want released", got.Status)
	}
	if len(signer.calls) != 1 {
		t.Errorf("signer called %d times, want 1", len(signer.calls))
	}
}

// TestManualRelease_TradeNotFound verifies that ManualRelease propagates
// ErrTradeNotFound (from SystemMarkPaid) when the trade doesn't exist.
func TestManualRelease_TradeNotFound(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	auto, _ := newPMAutomationService(t, db, esc, svc, nameMap(nil))

	_, err := auto.ManualRelease(context.Background(), uuid.New(), uuid.New())
	if err == nil {
		t.Fatal("expected error for non-existent trade, got nil")
	}
}
