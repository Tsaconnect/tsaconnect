package services

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/gorm"
)

func expiringEvents(b *mockBus) int {
	n := 0
	for _, e := range b.published {
		if e.Type == events.LiquidityVoucherExpiring {
			n++
		}
	}
	return n
}

func (r *poolRig) stockAd(t *testing.T, lp *models.PaymentMerchant, max, remaining *big.Int, deadline time.Time) *models.P2PAd {
	ad := seedActiveAd(t, r.db, lp.UserID, poolTestChain, &lp.ID, "0", remaining.String())
	r.db.Model(&models.P2PAd{}).Where("id = ?", ad.ID).Updates(map[string]any{"amount": max.String(), "voucher_deadline": deadline})
	return ad
}

func adStatus(t *testing.T, db *gorm.DB, id uuid.UUID) string {
	t.Helper()
	var ad models.P2PAd
	if err := db.First(&ad, "id = ?", id).Error; err != nil {
		t.Fatalf("reload ad: %v", err)
	}
	return ad.Status
}

// Roadmap risk 6 (M12): the sweep measures each voucher against its OWN
// signed maxAmount, whatever pool_lp_max says today.
func TestVoucherRenewal_UsesVouchersOwnMaxAmount(t *testing.T) {
	r := newPoolRig(t)
	far := time.Date(2026, 12, 30, 0, 0, 0, 0, time.UTC)
	own := new(big.Int).Mul(usdt18(5000), big.NewInt(100)) // signed under a $5,000 max
	a := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	r.stockAd(t, a, own, usdt18(9000), far) // < 10,000: due by its own max
	b := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	r.stockAd(t, b, own, usdt18(15000), far) // ≥ 10,000: not due

	// Today's max moved both ways; neither changes the answer.
	db := setupFeeConfigTestDB(t)
	Fees = NewFeeConfigService(db)
	_ = Fees.ensurePoolSettingRows()
	db.Model(&models.FeeConfig{}).Where("feature = ?", models.FeaturePoolLPMax).Update("flat_amount_usd", 10000)
	_ = Fees.Load()

	notified, _, err := r.svc.SweepVoucherRenewals(context.Background())
	if err != nil || notified != 1 || expiringEvents(r.bus) != 1 || r.bus.published[len(r.bus.published)-1].UserID != a.UserID {
		t.Fatalf("notified=%d events=%d err=%v", notified, expiringEvents(r.bus), err)
	}

	// And down: at a $1,000 max (floor 2,000) b's 15,000 would be far from due
	// and a's 9,000 too; the answer must still be "a only" — no second notice
	// inside 20 h, and b never.
	db.Model(&models.FeeConfig{}).Where("feature = ?", models.FeaturePoolLPMax).Update("flat_amount_usd", 1000)
	_ = Fees.Load()
	c := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	r.stockAd(t, c, own, usdt18(9999), far) // due by its own max, not by today's
	if notified, _, err := r.svc.SweepVoucherRenewals(context.Background()); err != nil || notified != 1 ||
		r.bus.published[len(r.bus.published)-1].UserID != c.UserID {
		t.Fatalf("after max down: notified=%d err=%v", notified, err)
	}
}

func TestVoucherRenewal_NotifiesOncePer20Hours(t *testing.T) {
	r := newPoolRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	own := new(big.Int).Mul(usdt18(5000), big.NewInt(100))
	r.stockAd(t, lp, own, own, time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)) // 10 days left
	ctx := context.Background()
	r.svc.SweepVoucherRenewals(ctx)
	r.svc.SweepVoucherRenewals(ctx) // a restart ticks at once (scheduler.runWorker)
	if expiringEvents(r.bus) != 1 {
		t.Fatalf("events = %d, want 1 within 20h", expiringEvents(r.bus))
	}
	r.svc = r.svc.WithClock(fixedClock{t: time.Date(2026, 10, 9, 7, 59, 0, 0, time.UTC)}) // +19h59m
	r.svc.SweepVoucherRenewals(ctx)
	if expiringEvents(r.bus) != 1 {
		t.Fatalf("events = %d, want still 1 just inside 20h", expiringEvents(r.bus))
	}
	r.svc = r.svc.WithClock(fixedClock{t: time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)}) // +21h
	r.svc.SweepVoucherRenewals(ctx)
	if expiringEvents(r.bus) != 2 {
		t.Fatalf("events = %d, want a second notice after 20h", expiringEvents(r.bus))
	}
}

func TestVoucherRenewal_ClosesExpiredAdWithoutLiveTrades(t *testing.T) {
	r := newPoolRig(t)
	own := new(big.Int).Mul(usdt18(5000), big.NewInt(100))
	past := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	idle := r.stockAd(t, seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet), own, own, past)
	busyLP := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	busy := r.stockAd(t, busyLP, own, own, past)
	r.db.Exec(`INSERT INTO p2p_trades (id, ad_id, buyer_id, seller_id, token, chain_id, token_amount, fiat_amount, status, expires_at)
		VALUES (?, ?, ?, ?, 'USDT', 97, '1', '1', 'pending_payment', ?)`, uuid.New(), busy.ID, uuid.New(), busyLP.UserID, time.Now())
	_, closed, err := r.svc.SweepVoucherRenewals(context.Background())
	var a, b models.P2PAd
	r.db.First(&a, "id = ?", idle.ID)
	r.db.First(&b, "id = ?", busy.ID)
	if err != nil || closed != 1 || a.Status != models.P2PAdStatusCancelled || b.Status != models.P2PAdStatusActive {
		t.Fatalf("closed=%d idle=%s busy=%s err=%v", closed, a.Status, b.Status, err)
	}
	// An expired ad is retired, never told to renew.
	if expiringEvents(r.bus) != 0 {
		t.Fatalf("expired ads got %d renewal notices", expiringEvents(r.bus))
	}
	var audit models.P2PAuditLog
	if err := r.db.Where("ad_id = ? AND action = ?", idle.ID, models.P2PAuditAdClosed).First(&audit).Error; err != nil ||
		!strings.Contains(string(audit.Metadata), `"expired"`) {
		t.Fatalf("ad.closed audit: %v %s", err, audit.Metadata)
	}
}

// Task 7 note / spec §8.4 step 6: a superseded stock ad kept for its live trade
// is retired by the sweep once that trade ends, and it is never the subject of
// a renewal notice (the LP already renewed).
func TestVoucherRenewal_RetiresSupersededAdOnceItsTradesEnd(t *testing.T) {
	r := newPoolRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	own := new(big.Int).Mul(usdt18(5000), big.NewInt(100))
	old := r.stockAd(t, lp, own, own, time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)) // due
	r.db.Model(&models.P2PAd{}).Where("id = ?", old.ID).Update("created_at", time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC))
	fresh := r.stockAd(t, lp, own, own, time.Date(2027, 1, 6, 12, 0, 0, 0, time.UTC)) // the renewal, not due
	seedTradeOn(t, r.db, old.ID, models.P2PTradeStatusPaid)
	ctx := context.Background()

	notified, closed, err := r.svc.SweepVoucherRenewals(ctx)
	if err != nil || notified != 0 || closed != 0 || adStatus(t, r.db, old.ID) != models.P2PAdStatusActive {
		t.Fatalf("live trade: notified=%d closed=%d old=%s err=%v", notified, closed, adStatus(t, r.db, old.ID), err)
	}
	r.db.Model(&models.P2PTrade{}).Where("ad_id = ?", old.ID).Update("status", models.P2PTradeStatusReleased)
	notified, closed, err = r.svc.SweepVoucherRenewals(ctx)
	if err != nil || notified != 0 || closed != 1 ||
		adStatus(t, r.db, old.ID) != models.P2PAdStatusCancelled || adStatus(t, r.db, fresh.ID) != models.P2PAdStatusActive {
		t.Fatalf("ended: notified=%d closed=%d old=%s fresh=%s err=%v", notified, closed,
			adStatus(t, r.db, old.ID), adStatus(t, r.db, fresh.ID), err)
	}
	if expiringEvents(r.bus) != 0 {
		t.Fatalf("a superseded ad was told to renew: %d", expiringEvents(r.bus))
	}
}

// A paused (or otherwise inactive) LP cannot renew (openableFor), so it is not
// told to; its expired ad is still retired. A merchant's ordinary ad is never
// touched.
func TestVoucherRenewal_OnlyActiveLPsNotifiedAndNonLPAdsUntouched(t *testing.T) {
	r := newPoolRig(t)
	own := new(big.Int).Mul(usdt18(5000), big.NewInt(100))
	paused := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusPaused, lpWallet)
	r.stockAd(t, paused, own, own, time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)) // due
	pausedExpired := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusPaused, lpWallet)
	gone := r.stockAd(t, pausedExpired, own, own, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	merchant := seedPMRow(t, r.db, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusActive, lpWallet)
	ordinary := r.stockAd(t, merchant, own, own, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))

	notified, closed, err := r.svc.SweepVoucherRenewals(context.Background())
	if err != nil || notified != 0 || closed != 1 || expiringEvents(r.bus) != 0 {
		t.Fatalf("notified=%d closed=%d events=%d err=%v", notified, closed, expiringEvents(r.bus), err)
	}
	if adStatus(t, r.db, gone.ID) != models.P2PAdStatusCancelled || adStatus(t, r.db, ordinary.ID) != models.P2PAdStatusActive {
		t.Fatalf("paused LP expired=%s merchant ad=%s", adStatus(t, r.db, gone.ID), adStatus(t, r.db, ordinary.ID))
	}
}

// The notice is recorded as an ad.renewal_due audit row on the service clock,
// and its copy names no provider and promises no return (spec §19).
func TestVoucherRenewal_NoticeAuditAndCopy(t *testing.T) {
	r := newPoolRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	own := new(big.Int).Mul(usdt18(5000), big.NewInt(100))
	ad := r.stockAd(t, lp, own, own, time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
	if _, _, err := r.svc.SweepVoucherRenewals(context.Background()); err != nil {
		t.Fatal(err)
	}
	var row models.P2PAuditLog
	if err := r.db.Where("ad_id = ? AND action = ?", ad.ID, models.P2PAuditAdRenewalDue).First(&row).Error; err != nil {
		t.Fatalf("audit: %v", err)
	}
	if !row.CreatedAt.Equal(time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("audit stamped %s, want the service clock", row.CreatedAt)
	}
	ev := r.bus.published[len(r.bus.published)-1]
	if ev.Type != events.LiquidityVoucherExpiring || ev.UserID != lp.UserID || ev.Data["adId"] != ad.ID ||
		!strings.Contains(ev.Message, "18 October 2026") {
		t.Fatalf("event = %+v", ev)
	}
	copy := strings.ToLower(ev.Title + " " + ev.Message)
	for _, banned := range []string{"interest", "apr", "earn", "paycrest", "flutterwave", "bsc", "binance"} {
		if strings.Contains(copy, banned) {
			t.Fatalf("copy contains %q: %s", banned, copy)
		}
	}
}

// A voucher lapsing silently drops the LP out of the pool, so the notice goes
// out by email and push as well as in-app (spec §8.4 step 6).
func TestLiquidityVoucherExpiring_EmailAndPush(t *testing.T) {
	if !emailEvents[events.LiquidityVoucherExpiring] || !pushWorthy(events.LiquidityVoucherExpiring) {
		t.Fatal("liquidity.voucher_expiring must email and push")
	}
}

// A sweep that cannot read or record must surface to the scheduler (the job is
// registered with AlertOnFailureStreak) and must not notify unrecorded.
func TestVoucherRenewal_FailureSurfacesWithoutNotifying(t *testing.T) {
	r := newPoolRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	own := new(big.Int).Mul(usdt18(5000), big.NewInt(100))
	r.stockAd(t, lp, own, own, time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
	if err := r.db.Exec(`DROP TABLE p2p_audit_logs`).Error; err != nil {
		t.Fatal(err)
	}
	notified, _, err := r.svc.SweepVoucherRenewals(context.Background())
	if err == nil || notified != 0 || expiringEvents(r.bus) != 0 {
		t.Fatalf("notified=%d events=%d err=%v", notified, expiringEvents(r.bus), err)
	}
}

// closeStockAd re-checks for a live trade in the same statement, so a trade
// opened after the sweep counted none still keeps its ad live.
func TestCloseStockAd_NeverClosesAnAdWithALiveTrade(t *testing.T) {
	r := newPoolRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	own := new(big.Int).Mul(usdt18(5000), big.NewInt(100))
	ad := r.stockAd(t, lp, own, own, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	seedTradeOn(t, r.db, ad.ID, models.P2PTradeStatusPendingPayment)
	closed, err := r.svc.closeStockAd(context.Background(), ad, "expired")
	if err != nil || closed || adStatus(t, r.db, ad.ID) != models.P2PAdStatusActive {
		t.Fatalf("closed=%v status=%s err=%v", closed, adStatus(t, r.db, ad.ID), err)
	}
}

// The sweep's queries on PostgreSQL (the guarded close's correlated NOT EXISTS
// and the spacing count). Skips without DATABASE_URL.
func TestVoucherRenewal_SweepOnPostgres(t *testing.T) {
	db := testutil.TempPostgres(t, "liq_renewal_test_",
		&models.PaymentMerchant{}, &models.P2PAd{}, &models.P2PTrade{}, &models.P2PAuditLog{})
	reg, _, _ := poolTestRegistry()
	bus := &mockBus{}
	svc := NewP2PTradeService(db, reg, &mockRates{}, bus, 146).
		WithClock(fixedClock{t: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)})
	svc.SetPMService(NewPMService(db))
	r := &poolRig{db: db, svc: svc, bus: bus}
	own := new(big.Int).Mul(usdt18(5000), big.NewInt(100))
	due := r.stockAd(t, seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet),
		own, own, time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
	idle := r.stockAd(t, seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000d2"),
		own, own, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	busyLP := seedPMRow(t, db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000d3")
	busy := r.stockAd(t, busyLP, own, own, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err := db.Create(&models.P2PTrade{ID: uuid.New(), AdID: busy.ID, BuyerID: uuid.New(), SellerID: busyLP.UserID,
		Token: "USDT", ChainID: poolTestChain, TokenAmount: "1", FiatAmount: "1", Rate: "1",
		Status: models.P2PTradeStatusPendingPayment, ExpiresAt: time.Now()}).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		notified, closed, err := svc.SweepVoucherRenewals(ctx)
		if err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
		if i == 0 && (notified != 1 || closed != 1) || i == 1 && (notified != 0 || closed != 0) {
			t.Fatalf("sweep %d: notified=%d closed=%d", i, notified, closed)
		}
	}
	if adStatus(t, db, idle.ID) != models.P2PAdStatusCancelled || adStatus(t, db, busy.ID) != models.P2PAdStatusActive ||
		adStatus(t, db, due.ID) != models.P2PAdStatusActive {
		t.Fatal("wrong ads closed on PostgreSQL")
	}
}

// The deadline itself retires the ad; a minute before it, the ad is still the
// LP's current one and is told to renew.
func TestVoucherRenewal_ExpiresAtTheDeadline(t *testing.T) {
	r := newPoolRig(t)
	own := new(big.Int).Mul(usdt18(5000), big.NewInt(100))
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) // the rig's clock
	at := r.stockAd(t, seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet), own, own, now)
	before := r.stockAd(t, seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet), own, own, now.Add(time.Minute))
	notified, closed, err := r.svc.SweepVoucherRenewals(context.Background())
	if err != nil || closed != 1 || notified != 1 ||
		adStatus(t, r.db, at.ID) != models.P2PAdStatusCancelled || adStatus(t, r.db, before.ID) != models.P2PAdStatusActive {
		t.Fatalf("notified=%d closed=%d at=%s before=%s err=%v", notified, closed,
			adStatus(t, r.db, at.ID), adStatus(t, r.db, before.ID), err)
	}
}
