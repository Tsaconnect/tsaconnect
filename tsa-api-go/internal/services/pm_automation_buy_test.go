package services

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ─── Pure matcher (no DB) ────────────────────────────────────────────────────

func TestMatchBuyInbound(t *testing.T) {
	idA := uuid.New()
	idB := uuid.New()

	cases := []struct {
		name       string
		cands      []buyCandidate
		sender     string
		amountNGN  string
		wantID     uuid.UUID
		wantOK     bool
	}{
		{
			name:      "exact single name+amount match",
			cands:     []buyCandidate{{TradeID: idA, BuyerName: "JOHN DOE", FiatAmount: "1500.00"}},
			sender:    "JOHN DOE",
			amountNGN: "1500.00",
			wantID:    idA,
			wantOK:    true,
		},
		{
			name:      "name mismatch",
			cands:     []buyCandidate{{TradeID: idA, BuyerName: "JOHN DOE", FiatAmount: "1500.00"}},
			sender:    "JANE SMITH",
			amountNGN: "1500.00",
			wantOK:    false,
		},
		{
			name:      "amount mismatch",
			cands:     []buyCandidate{{TradeID: idA, BuyerName: "JOHN DOE", FiatAmount: "1500.00"}},
			sender:    "JOHN DOE",
			amountNGN: "1600.00",
			wantOK:    false,
		},
		{
			name: "two candidates same amount, one name matches",
			cands: []buyCandidate{
				{TradeID: idA, BuyerName: "JOHN DOE", FiatAmount: "1500.00"},
				{TradeID: idB, BuyerName: "JANE SMITH", FiatAmount: "1500.00"},
			},
			sender:    "JOHN DOE",
			amountNGN: "1500.00",
			wantID:    idA,
			wantOK:    true,
		},
		{
			name: "two candidates both name+amount match -> ambiguous",
			cands: []buyCandidate{
				{TradeID: idA, BuyerName: "JOHN DOE", FiatAmount: "1500.00"},
				{TradeID: idB, BuyerName: "JOHN DOE", FiatAmount: "1500.00"},
			},
			sender:    "JOHN DOE",
			amountNGN: "1500.00",
			wantOK:    false,
		},
		{
			name:      "empty candidates",
			cands:     nil,
			sender:    "JOHN DOE",
			amountNGN: "1500.00",
			wantOK:    false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, ok := matchBuyInbound(c.cands, c.sender, c.amountNGN)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && id != c.wantID {
				t.Fatalf("id = %v, want %v", id, c.wantID)
			}
		})
	}
}

func TestNgnEqual(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1500.00", "1500", true},
		{"1500.00", "1500.0", true},
		{"1500", "1500.00", true},
		{"1500.50", "1500.00", false},
		{"1500.00", "150.00", false},
		{"1500.5", "1500.50", true},
		{"1500.00", "1500.000", true}, // extra fractional zeros truncate to 2dp
		{"abc", "1500", false},
		{"1500", "xyz", false},
		{"", "1500", false},
	}
	for _, c := range cases {
		if got := ngnEqual(c.a, c.b); got != c.want {
			t.Errorf("ngnEqual(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// ─── Integration harness ─────────────────────────────────────────────────────

// createPaymentMerchantsTable adds the payment_merchants table to the shared
// trade-service SQLite DB (setupTradeDB doesn't include it). Mirrors the DDL in
// pm_service_test.go's newSQLiteForPM.
func createPaymentMerchantsTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS payment_merchants (
		id                    TEXT PRIMARY KEY,
		user_id               TEXT NOT NULL,
		name                  TEXT NOT NULL,
		status                TEXT NOT NULL DEFAULT 'pending',
		nomba_account_id      TEXT,
		nomba_creds_ref       TEXT,
		inbound_account_number TEXT,
		inbound_bank          TEXT,
		operator_wallet_address TEXT,
		per_trade_cap_usd     REAL NOT NULL DEFAULT 0,
		daily_cap_usd         REAL NOT NULL DEFAULT 0,
		created_at            DATETIME,
		updated_at            DATETIME
	)`).Error; err != nil {
		t.Fatalf("create payment_merchants: %v", err)
	}
}

// seedActivePM inserts an active PaymentMerchant bound to the given inbound
// account, with the supplied per-trade USD cap (0 = unlimited).
func seedActivePM(t *testing.T, db *gorm.DB, inboundAcct string, perTradeCapUSD float64) *models.PaymentMerchant {
	t.Helper()
	pm := &models.PaymentMerchant{
		ID:                   uuid.New(),
		UserID:               uuid.New(),
		Name:                 "TSA",
		Status:               models.PaymentMerchantStatusActive,
		InboundAccountNumber: inboundAcct,
		InboundBank:          "Nomba",
		PerTradeCapUSD:       perTradeCapUSD,
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed PM: %v", err)
	}
	return pm
}

// seedPMSellAd inserts an active PM sell-ad (pm_id set, side=sell) on chain 146.
func seedPMSellAd(t *testing.T, db *gorm.DB, pmID, sellerID uuid.UUID) *models.P2PAd {
	t.Helper()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	ad := &models.P2PAd{
		ID: uuid.New(), PMID: &pmID, SellerID: sellerID,
		Side: models.P2PAdSideSell, Token: "USDT", ChainID: 146,
		Amount: "1000000", Remaining: "1000000", Reserved: "0",
		FiatCurrency: "NGN", Rate: "1300", RateType: models.P2PAdRateTypeFixed,
		Status: models.P2PAdStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(ad).Error; err != nil {
		t.Fatalf("seed PM sell ad: %v", err)
	}
	return ad
}

// seedBuyTradeOnAd inserts a pending_payment, pooled (funding 0), on-chain-open
// buy trade on the given ad with a specific buyer + fiat amount.
func seedBuyTradeOnAd(t *testing.T, db *gorm.DB, ad *models.P2PAd, buyerID uuid.UUID, fiat string) *models.P2PTrade {
	t.Helper()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	id := uuid.New()
	trade := &models.P2PTrade{
		ID: id, AdID: ad.ID, SellerID: ad.SellerID, BuyerID: buyerID,
		Token: "USDT", ChainID: 146, Funding: models.P2PTradeFundingPooled,
		TokenAmount: "1000000", FiatAmount: fiat, FiatCurrency: "NGN", Rate: "1300",
		PaymentMethodID: uuid.New(), Status: models.P2PTradeStatusPendingPayment,
		OnChainTradeID: HashIDHex(id), OpenTxHash: "0xopen",
		ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(trade).Error; err != nil {
		t.Fatalf("seed buy trade: %v", err)
	}
	return trade
}

// setReleasedEvent wires the fake escrow's TradeReleased receipt so the
// money-invariant in confirmReleaseCore (AmountToBuyer+FeeToSystem==TokenAmount)
// is satisfied for the given trade.
func setReleasedEvent(esc *mockEscrow, trade *models.P2PTrade) {
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	fee := big.NewInt(500)
	toBuyer := new(big.Int).Sub(tokenAmt, fee)
	esc.releasedEvent = &P2PTradeReleasedEvent{
		TradeID:       HashID(trade.ID),
		Buyer:         common.HexToAddress("0x000000000000000000000000000000000000beef"),
		AmountToBuyer: toBuyer,
		FeeToSystem:   fee,
	}
}

// nameMap builds a PMNameLookup backed by a static buyerID→name map.
func nameMap(m map[uuid.UUID]string) PMNameLookup {
	return func(_ context.Context, id uuid.UUID) (string, error) {
		return m[id], nil
	}
}

func newPMAutomationService(t *testing.T, db *gorm.DB, esc *mockEscrow, svc *P2PTradeService, name PMNameLookup) (*PMAutomationService, *fakeReleaseSigner) {
	t.Helper()
	signer := &fakeReleaseSigner{
		addr: "0x1111111111111111111111111111111111111111",
		hash: "0xrelease",
	}
	pmSvc := NewPMService(db)
	return NewPMAutomationService(db, pmSvc, svc, signer, name), signer
}

// ─── Integration tests ───────────────────────────────────────────────────────

func TestHandleInbound_HappyPath(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")
	setReleasedEvent(esc, trade)

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{
		buyerID: "JOHN DOE",
	}))

	err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:             "settled",
		DestinationAccount: pm.InboundAccountNumber,
		SenderAccountName:  "JOHN DOE",
		AmountNGN:          "1500.00",
		EventID:            "evt_1",
	})
	if err != nil {
		t.Fatalf("HandleInbound: %v", err)
	}

	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status != models.P2PTradeStatusReleased {
		t.Errorf("status = %q, want released", persisted.Status)
	}
	if persisted.NombaInboundEventID != "evt_1" {
		t.Errorf("nombaInboundEventID = %q, want evt_1", persisted.NombaInboundEventID)
	}
	if len(signer.calls) != 1 {
		t.Errorf("signer called %d times, want 1", len(signer.calls))
	}
}

func TestHandleInbound_PendingStatus_NoOp(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")
	setReleasedEvent(esc, trade)

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{
		buyerID: "JOHN DOE",
	}))

	err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:             "pending",
		DestinationAccount: pm.InboundAccountNumber,
		SenderAccountName:  "JOHN DOE",
		AmountNGN:          "1500.00",
		EventID:            "evt_1",
	})
	if err != nil {
		t.Fatalf("HandleInbound: %v", err)
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

func TestHandleInbound_NameMismatch_NoOp(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")
	setReleasedEvent(esc, trade)

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{
		buyerID: "JOHN DOE",
	}))

	err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:             "settled",
		DestinationAccount: pm.InboundAccountNumber,
		SenderAccountName:  "JANE SMITH",
		AmountNGN:          "1500.00",
		EventID:            "evt_1",
	})
	if err != nil {
		t.Fatalf("HandleInbound: %v", err)
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

func TestHandleInbound_Ambiguous_NoOp(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerA := uuid.New()
	buyerB := uuid.New()
	tradeA := seedBuyTradeOnAd(t, db, ad, buyerA, "1500.00")
	tradeB := seedBuyTradeOnAd(t, db, ad, buyerB, "1500.00")
	setReleasedEvent(esc, tradeA)

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{
		buyerA: "JOHN DOE",
		buyerB: "JOHN DOE",
	}))

	err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:             "settled",
		DestinationAccount: pm.InboundAccountNumber,
		SenderAccountName:  "JOHN DOE",
		AmountNGN:          "1500.00",
		EventID:            "evt_1",
	})
	if err != nil {
		t.Fatalf("HandleInbound: %v", err)
	}

	for _, id := range []uuid.UUID{tradeA.ID, tradeB.ID} {
		var persisted models.P2PTrade
		db.First(&persisted, "id = ?", id)
		if persisted.Status != models.P2PTradeStatusPendingPayment {
			t.Errorf("trade %v status = %q, want pending_payment (no-op)", id, persisted.Status)
		}
	}
	if len(signer.calls) != 0 {
		t.Errorf("signer called %d times, want 0", len(signer.calls))
	}
}
