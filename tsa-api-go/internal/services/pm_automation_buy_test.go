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
		name      string
		cands     []buyCandidate
		sender    string
		amountNGN string
		wantID    uuid.UUID
		wantOK    bool
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
		id                       TEXT PRIMARY KEY,
		user_id                  TEXT NOT NULL,
		name                     TEXT NOT NULL,
		status                   TEXT NOT NULL DEFAULT 'pending',
		nomba_account_id         TEXT,
		nomba_wallet_id          TEXT,
		nomba_creds_ref          TEXT,
		inbound_account_number   TEXT,
		inbound_bank             TEXT,
		nomba_account_ref        TEXT,
		operator_wallet_address  TEXT,
		nomba_sub_account_id     TEXT,
		settlement_bank_code     TEXT,
		settlement_account_number TEXT,
		settlement_account_name  TEXT,
		available_ngn            TEXT NOT NULL DEFAULT '0',
		reserved_ngn             TEXT NOT NULL DEFAULT '0',
		provisioning_status      TEXT NOT NULL DEFAULT 'pending',
		per_trade_cap_usd        REAL NOT NULL DEFAULT 0,
		daily_cap_usd            REAL NOT NULL DEFAULT 0,
		reviewed_by              TEXT,
		reviewed_at              DATETIME,
		review_note              TEXT,
		created_at               DATETIME,
		updated_at               DATETIME
	)`).Error; err != nil {
		t.Fatalf("create payment_merchants: %v", err)
	}

	// Per-currency balances. Every ledger operation now writes both this and the
	// legacy columns in one transaction, so omitting it here would roll back the
	// legacy write too — the dual-write working as intended, but confusing to
	// debug. The CHECK mirrors chk_pm_balances_ledger in config/database.go.
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS pm_balances (
		pm_id      TEXT NOT NULL,
		currency   TEXT NOT NULL,
		available  TEXT NOT NULL DEFAULT '0',
		reserved   TEXT NOT NULL DEFAULT '0',
		created_at DATETIME,
		updated_at DATETIME,
		PRIMARY KEY (pm_id, currency),
		CHECK (CAST(available AS NUMERIC) >= 0
		   AND CAST(reserved  AS NUMERIC) >= 0
		   AND CAST(reserved  AS NUMERIC) <= CAST(available AS NUMERIC))
	)`).Error; err != nil {
		t.Fatalf("create pm_balances: %v", err)
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
		// Derive unique non-blank ids from the inbound acct. NombaAccountRef is
		// the ONLY inbound match key under the VA-only model (resolveInboundPM);
		// the walletId fallback was removed in 17f761f. NombaWalletID is still
		// seeded so tests that assert walletId is *ignored* have a value to pass.
		NombaAccountRef: "ref-" + inboundAcct,
		NombaWalletID:   "wallet-" + inboundAcct,
		PerTradeCapUSD:  perTradeCapUSD,
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
	return NewPMAutomationService(db, pmSvc, svc, signer, name, &fakeNombaGateway{}, "TSA Connect"), signer
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
		Status:            "settled",
		AccountRef:        pm.NombaAccountRef,
		WalletID:          pm.NombaWalletID,
		SenderAccountName: "JOHN DOE",
		AmountNGN:         "1500.00",
		EventID:           "evt_1",
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

// TestHandleInbound_KeysByWalletId verifies the buy flow resolves the PM by the
// Nomba walletId (data.merchant.walletId), NOT by a destination account number.
// The inbound carries only WalletID (DestinationAccount left blank) and still
// releases; an inbound with an unknown walletId is a no-op even if the (now
// ignored) DestinationAccount would have matched.
func TestHandleInbound_KeysByWalletId(t *testing.T) {
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

	// Match by walletId, with DestinationAccount deliberately blank.
	if err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:            "settled",
		AccountRef:        pm.NombaAccountRef,
		WalletID:          pm.NombaWalletID,
		SenderAccountName: "JOHN DOE",
		AmountNGN:         "1500.00",
		EventID:           "evt_wallet",
	}); err != nil {
		t.Fatalf("HandleInbound (by walletId): %v", err)
	}
	var released models.P2PTrade
	db.First(&released, "id = ?", trade.ID)
	if released.Status != models.P2PTradeStatusReleased {
		t.Fatalf("status = %q, want released (keyed by walletId)", released.Status)
	}
	if len(signer.calls) != 1 {
		t.Fatalf("signer called %d times, want 1", len(signer.calls))
	}

	// An unknown walletId is a no-op even though DestinationAccount matches the PM.
	trade2 := seedBuyTradeOnAd(t, db, ad, buyerID, "2500.00")
	if err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:             "settled",
		WalletID:           "wallet-unknown",
		DestinationAccount: pm.InboundAccountNumber,
		SenderAccountName:  "JOHN DOE",
		AmountNGN:          "2500.00",
		EventID:            "evt_unknown_wallet",
	}); err != nil {
		t.Fatalf("HandleInbound (unknown walletId): %v", err)
	}
	var stuck models.P2PTrade
	db.First(&stuck, "id = ?", trade2.ID)
	if stuck.Status != models.P2PTradeStatusPendingPayment {
		t.Fatalf("status = %q, want pending_payment (unknown walletId must no-op)", stuck.Status)
	}
	if len(signer.calls) != 1 {
		t.Fatalf("signer called %d times after unknown-wallet inbound, want still 1", len(signer.calls))
	}
}

// TestHandleInbound_MatchesByAccountRef verifies the VA-only model: a corporate
// PM is resolved by its NombaAccountRef when the inbound carries an AccountRef,
// and the matched trade is released.
func TestHandleInbound_MatchesByAccountRef(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	// Seed an active corporate PM with an accountRef (no walletId match here).
	pm := &models.PaymentMerchant{
		ID:                   uuid.New(),
		UserID:               uuid.New(),
		Name:                 "Acme Corp",
		Status:               models.PaymentMerchantStatusActive,
		InboundAccountNumber: "9012345678",
		NombaAccountRef:      "pm-acme-ref",
		PerTradeCapUSD:       0,
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed PM: %v", err)
	}
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")
	setReleasedEvent(esc, trade)

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{
		buyerID: "JOHN DOE",
	}))

	// Inbound carries AccountRef (VA model), no WalletID.
	if err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:            "settled",
		AccountRef:        "pm-acme-ref",
		SenderAccountName: "JOHN DOE",
		AmountNGN:         "1500.00",
		EventID:           "evt_va_1",
	}); err != nil {
		t.Fatalf("HandleInbound (by accountRef): %v", err)
	}
	var released models.P2PTrade
	db.First(&released, "id = ?", trade.ID)
	if released.Status != models.P2PTradeStatusReleased {
		t.Fatalf("status = %q, want released (keyed by accountRef)", released.Status)
	}
	if len(signer.calls) != 1 {
		t.Fatalf("signer called %d times, want 1", len(signer.calls))
	}

	// An unknown accountRef with no walletId is a no-op.
	trade2 := seedBuyTradeOnAd(t, db, ad, buyerID, "2500.00")
	if err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:            "settled",
		AccountRef:        "pm-unknown-ref",
		SenderAccountName: "JOHN DOE",
		AmountNGN:         "2500.00",
		EventID:           "evt_va_unknown",
	}); err != nil {
		t.Fatalf("HandleInbound (unknown accountRef): %v", err)
	}
	var stuck models.P2PTrade
	db.First(&stuck, "id = ?", trade2.ID)
	if stuck.Status != models.P2PTradeStatusPendingPayment {
		t.Fatalf("status = %q, want pending_payment (unknown accountRef must no-op)", stuck.Status)
	}
}

// TestHandleInbound_WalletIDFallbackStillWorks was removed: it asserted that an
// empty AccountRef falls back to walletId resolution, a path deliberately
// deleted in 17f761f (accountRef-only). The inverted contract — an empty
// AccountRef is a no-op — is covered by TestHandleInbound_EmptyAccountRef_IsIgnored
// in pm_automation_inbound_test.go, so re-adding it here would only duplicate it.

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
		Status:            "pending",
		AccountRef:        pm.NombaAccountRef,
		WalletID:          pm.NombaWalletID,
		SenderAccountName: "JOHN DOE",
		AmountNGN:         "1500.00",
		EventID:           "evt_1",
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
		Status:            "settled",
		AccountRef:        pm.NombaAccountRef,
		WalletID:          pm.NombaWalletID,
		SenderAccountName: "JANE SMITH",
		AmountNGN:         "1500.00",
		EventID:           "evt_1",
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
		Status:            "settled",
		AccountRef:        pm.NombaAccountRef,
		WalletID:          pm.NombaWalletID,
		SenderAccountName: "JOHN DOE",
		AmountNGN:         "1500.00",
		EventID:           "evt_1",
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

// TestHandleInbound_UnparseableTokenAmount_NoRelease verifies the cap-bypass
// guard: a trade whose TokenAmount cannot be parsed as a base-10 integer (e.g.
// a decimal like "1.5") must NOT be auto-released. "1.5" inserts cleanly into
// SQLite's TEXT column but big.Int.SetString("1.5", 10) returns ok=false
// because base-10 parsing rejects the decimal point.
func TestHandleInbound_UnparseableTokenAmount_NoRelease(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")

	// Overwrite token_amount with a decimal value that inserts fine (TEXT column
	// in SQLite) but fails big.Int.SetString(_, 10). "1.5" is the canonical
	// example: valid as a number but not as a base-10 integer string.
	if err := db.Exec("UPDATE p2p_trades SET token_amount = '1.5' WHERE id = ?", trade.ID).Error; err != nil {
		t.Fatalf("overwrite token_amount: %v", err)
	}
	trade.TokenAmount = "1.5"
	// We do NOT call setReleasedEvent — if the signer were called it would fail
	// the money-invariant check anyway, making the test stricter.

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{
		buyerID: "JOHN DOE",
	}))

	err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:            "settled",
		AccountRef:        pm.NombaAccountRef,
		WalletID:          pm.NombaWalletID,
		SenderAccountName: "JOHN DOE",
		AmountNGN:         "1500.00",
		EventID:           "evt_bad_amount",
	})
	// Route-to-manual is a deliberate no-op: no error returned.
	if err != nil {
		t.Fatalf("HandleInbound: %v (want nil — unparseable amount routes to manual, not hard error)", err)
	}

	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status != models.P2PTradeStatusPendingPayment {
		t.Errorf("status = %q, want pending_payment (must not release when USD unknown)", persisted.Status)
	}
	if len(signer.calls) != 0 {
		t.Errorf("signer called %d times, want 0 (must not sign when USD unknown)", len(signer.calls))
	}
}
