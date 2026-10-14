package services

import (
	"bytes"
	"context"
	"errors"
	"log"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fixedFresh struct{ at time.Time }

func (f fixedFresh) LastIngestOK() time.Time { return f.at }

// S24 / roadmap risk 9: the legacy scope runs with no pool at all.
func TestExpiry_LegacyScopeAlwaysOn(t *testing.T) {
	r := newPoolRig(t)
	sender := releaseSender()
	created := time.Now().Add(-61 * time.Minute)
	tr := legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-31*time.Minute))
	r.sonic.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending, CreatedAt: big.NewInt(created.Unix())}
	e := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: sender, Now: time.Now})
	tick, err := e.RunOnce(context.Background())
	if err != nil || tick.Legacy != 1 || len(sender.sendChains) != 1 || sender.sendChains[0] != 146 {
		t.Fatalf("tick=%+v err=%v sends=%v (trade %s)", tick, err, sender.sendChains, tr.ID)
	}
}

func TestExpiry_LegacyNeverTouchesPaidOrDisputed(t *testing.T) {
	r := newPoolRig(t)
	sender := releaseSender()
	old := time.Now().Add(-2 * time.Hour)
	legacyTrade(t, r, models.P2PTradeStatusPaid, old)
	legacyTrade(t, r, models.P2PTradeStatusDisputed, old)
	legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-10*time.Minute)) // told only 10 min ago
	r.sonic.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending, CreatedAt: big.NewInt(old.Unix())}
	tick, _ := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: sender}).RunOnce(context.Background())
	if tick.Legacy != 0 || len(sender.sendChains) != 0 {
		t.Fatalf("tick=%+v sends=%v", tick, sender.sendChains)
	}
}

func TestExpiry_AtMost20PerChainPerTick(t *testing.T) {
	r := newPoolRig(t)
	sender := releaseSender()
	for i := 0; i < 25; i++ {
		legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-31*time.Minute))
	}
	r.sonic.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending, CreatedAt: big.NewInt(time.Now().Add(-2 * time.Hour).Unix())}
	tick, _ := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: sender}).RunOnce(context.Background())
	if tick.Legacy != 20 || len(sender.sendChains) != 20 {
		t.Fatalf("tick=%+v sends=%d, want 20", tick, len(sender.sendChains))
	}
}

func legacyTrade(t *testing.T, r *poolRig, status string, notified time.Time) *models.P2PTrade {
	t.Helper()
	createSettlementForwardTable(t, r.db) // the canceller refuses a trade a credit has claimed
	ad := seedActiveAd(t, r.db, uuid.New(), 146, nil, "1500", "1000000")
	id := uuid.New()
	tr := &models.P2PTrade{ID: id, OnChainTradeID: HashIDHex(id), AdID: ad.ID, BuyerID: uuid.New(), SellerID: ad.SellerID, Token: "USDT", ChainID: 146,
		TokenAmount: "1000000", FiatAmount: "1500", FiatCurrency: "NGN", Rate: "1500", Status: status,
		OpenTxHash: "0xopened", ExpiresAt: notified.Add(-time.Minute), ExpiredNotifiedAt: &notified}
	if err := r.db.Create(tr).Error; err != nil {
		t.Fatal(err)
	}
	return tr
}

// pmLegacy is a merchant trade settled through the central account, 31 minutes
// past its expiry notice, Pending on-chain and past its 30-minute window.
func pmLegacy(t *testing.T) *fwdRig {
	t.Helper()
	r := newForwardRig(t, "1500.00")
	notified := time.Now().Add(-31 * time.Minute)
	r.db.Model(&models.P2PTrade{}).Where("id = ?", r.trade.ID).
		Updates(map[string]any{"expired_notified_at": notified, "expires_at": notified.Add(-time.Minute)})
	r.trade.ExpiredNotifiedAt = &notified
	r.esc.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending,
		CreatedAt: big.NewInt(time.Now().Add(-2 * time.Hour).Unix())}
	return r
}

// Money rule: a credit that claimed the trade first wins; the canceller refuses
// it even while the trade still reads pending_payment (the release has not run).
func TestExpiry_LegacyRefusesATradeACreditAlreadyClaimed(t *testing.T) {
	r := pmLegacy(t)
	if _, _, err := r.src.claimAndCredit(context.Background(), r.trade, r.pm, "999000000077"); err != nil {
		t.Fatal(err)
	}
	sender := releaseSender()
	tick, err := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.src.trades, Signer: sender}).RunOnce(context.Background())
	if err != nil || tick.Legacy != 0 || len(sender.sendChains) != 0 {
		t.Fatalf("tick=%+v err=%v sends=%v, want no cancel of a credited trade", tick, err, sender.sendChains)
	}
}

// Money rule: once the canceller has decided, no payment path can take the
// trade — the credit, the operator's mark-paid, the buyer's mark-paid and the
// release all refuse, and the matcher no longer offers it.
func TestExpiry_LegacyDecidedTradeTakesNoPayment(t *testing.T) {
	r := pmLegacy(t)
	ctx := context.Background()
	sender := releaseSender()
	tick, err := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.src.trades, Signer: sender}).RunOnce(ctx)
	if err != nil || tick.Legacy != 1 || len(sender.sendChains) != 1 {
		t.Fatalf("tick=%+v err=%v sends=%v, want the expired trade cancelled", tick, err, sender.sendChains)
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", r.trade.ID)
	if got.DroppedAt == nil {
		t.Fatal("the canceller's decision was not recorded on the trade")
	}
	if err := r.src.Accept(ctx, r.trade.ID, p2pInboundCredit("999000000078", "1500.00")); err == nil {
		t.Fatal("a late credit settled a trade the canceller is cancelling")
	}
	var fwd int64
	r.db.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", r.trade.ID).Count(&fwd)
	if fwd != 0 || !ngnEqual(r.availableNGN(t), "0") {
		t.Fatalf("forwards=%d available=%s, want nothing credited", fwd, r.availableNGN(t))
	}
	if claims, _ := r.src.OpenClaims(ctx, "NGN"); len(claims) != 0 {
		t.Fatalf("claims = %v, want the decided trade withheld from the matcher", claims)
	}
	if _, err := r.src.trades.SystemMarkPaid(ctx, r.trade.ID, "manual:op"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("operator mark-paid: %v", err)
	}
	if _, _, err := r.src.trades.PrepareMarkPaid(ctx, r.trade.ID, r.trade.BuyerID, testPayerAddr, "https://res.cloudinary.com/tsa/image/upload/proof.png"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("buyer mark-paid: %v", err)
	}
	if _, err := r.src.trades.AutoRelease(ctx, r.trade.ID, sender); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("release: %v", err)
	}
}

// Review C-1: a sell trade whose payout has started (or succeeded) is never
// cancelled — cancelByTimeout would refund the deposit after the naira left.
// A payout recorded as failed is the one exception: nothing left, so refund.
// A matched inbound is money moved too.
func TestExpiry_LegacyNeverCancelsATradeWithAPayoutInFlight(t *testing.T) {
	for _, c := range []struct {
		name   string
		set    map[string]any
		cancel bool
	}{
		{"payout in flight", map[string]any{"nomba_payout_id": "po_1"}, false},
		{"payout started, id lost", map[string]any{"payout_started_at": time.Now().Add(-40 * time.Minute)}, false},
		{"payout failed", map[string]any{"nomba_payout_id": "po_1", "cancel_reason": pmPayoutFailedReason + " (payout=po_1); deposit will be refunded automatically"}, true},
		{"inbound matched", map[string]any{"nomba_inbound_event_id": "evt_1"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newPoolRig(t)
			sender := releaseSender()
			tr := legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-31*time.Minute))
			r.db.Model(&models.P2PTrade{}).Where("id = ?", tr.ID).Updates(c.set)
			r.sonic.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending, CreatedAt: big.NewInt(time.Now().Add(-2 * time.Hour).Unix())}
			tick, err := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: sender}).RunOnce(context.Background())
			var got models.P2PTrade
			r.db.First(&got, "id = ?", tr.ID)
			if err != nil || (tick.Legacy == 1) != c.cancel || (len(sender.sendChains) == 1) != c.cancel || (got.DroppedAt != nil) != c.cancel {
				t.Fatalf("tick=%+v err=%v sends=%v dropped=%v, want cancel=%v", tick, err, sender.sendChains, got.DroppedAt, c.cancel)
			}
		})
	}
}

// Review I-1: the legacy scope is a rolling 24-hour window on the expiry
// notice; an older backlog is an ops step, never swept by a deploy.
func TestExpiry_LegacyScopeIsARolling24hWindow(t *testing.T) {
	r := newPoolRig(t)
	sender := releaseSender()
	legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-25*time.Hour))
	r.sonic.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending, CreatedAt: big.NewInt(time.Now().Add(-26 * time.Hour).Unix())}
	tick, err := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: sender}).RunOnce(context.Background())
	if err != nil || tick.Legacy != 0 || len(sender.sendChains) != 0 || r.sonic.readTradeCalls != 0 {
		t.Fatalf("tick=%+v sends=%v reads=%d, want a trade notified 25h ago left alone", tick, sender.sendChains, r.sonic.readTradeCalls)
	}
}

// Review I-1: a legacy trade the escrow does not know (opened on a retired
// escrow) is read once, then skipped — never 500 reads a minute.
func TestExpiry_LegacySkipsTradesTheEscrowDoesNotKnow(t *testing.T) {
	r := newPoolRig(t)
	legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-31*time.Minute))
	r.sonic.onChainTrade = &OnChainP2PTrade{Exists: false}
	ec := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: releaseSender()})
	ec.RunOnce(context.Background())
	ec.RunOnce(context.Background())
	if r.sonic.readTradeCalls != 1 {
		t.Fatalf("reads = %d over two ticks, want 1", r.sonic.readTradeCalls)
	}
}

// Review I-1 / M-11: a read error is surfaced (logged and counted as failed)
// and counts against the chain's budget, so an RPC outage costs at most
// PerChain reads a tick.
func TestExpiry_ReadErrorsAreSurfacedAndBudgeted(t *testing.T) {
	r := newPoolRig(t)
	for i := 0; i < 25; i++ {
		legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-31*time.Minute))
	}
	r.sonic.readTradeErr = errors.New("rpc down")
	tick, err := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: releaseSender()}).RunOnce(context.Background())
	if err != nil || tick.Failed != expiryPerChainDefault || r.sonic.readTradeCalls != expiryPerChainDefault {
		t.Fatalf("tick=%+v err=%v reads=%d, want %d failed reads surfaced", tick, err, r.sonic.readTradeCalls, expiryPerChainDefault)
	}
}

// Review M-7: a cancel that provably never broadcast takes its decision back,
// so the trade's payment paths are open again until a later tick succeeds.
func TestExpiry_LegacyUnbroadcastCancelClearsTheDecision(t *testing.T) {
	r := newPoolRig(t)
	sender := releaseSender()
	sender.err = notBroadcast(errors.New("insufficient funds for gas"))
	tr := legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-31*time.Minute))
	r.sonic.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending, CreatedAt: big.NewInt(time.Now().Add(-2 * time.Hour).Unix())}
	ec := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: sender})
	tick, _ := ec.RunOnce(context.Background())
	var got models.P2PTrade
	r.db.First(&got, "id = ?", tr.ID)
	if tick.Failed != 1 || got.DroppedAt != nil {
		t.Fatalf("tick=%+v dropped=%v, want the failed cancel's decision cleared", tick, got.DroppedAt)
	}
	sender.err = nil // the next tick retries at once: nothing was sent, so no back-off
	if tick, _ := ec.RunOnce(context.Background()); tick.Legacy != 1 {
		t.Fatalf("retry tick=%+v, want the cancel sent", tick)
	}
}

// Review M-9: a statement stale for 30 minutes defers every cancel; that is
// an alert, not only a per-minute metric.
func TestExpiry_SustainedDeferralAlerts(t *testing.T) {
	r := newPoolRig(t)
	now := time.Now()
	ec := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: releaseSender(),
		Fresh: fixedFresh{at: now.Add(-time.Hour)}, Now: func() time.Time { return now }})
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	ec.RunOnce(context.Background())
	now = now.Add(29 * time.Minute)
	ec.RunOnce(context.Background())
	if strings.Contains(buf.String(), "ALERT p2p_expiry_deferred_sustained") {
		t.Fatal("alerted before 30 minutes deferred")
	}
	now = now.Add(time.Minute)
	ec.RunOnce(context.Background())
	if !strings.Contains(buf.String(), "ALERT p2p_expiry_deferred_sustained") {
		t.Fatalf("no sustained-deferral alert:\n%s", buf.String())
	}
}

// Re-review N-2: a trade whose payout may be in flight alerts once, then at
// most hourly — not on every tick for a day.
func TestExpiry_PayoutInFlightAlertIsHourly(t *testing.T) {
	r := newPoolRig(t)
	tr := legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-31*time.Minute))
	r.db.Model(&models.P2PTrade{}).Where("id = ?", tr.ID).Update("payout_started_at", time.Now().Add(-40*time.Minute))
	r.sonic.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending, CreatedAt: big.NewInt(time.Now().Add(-2 * time.Hour).Unix())}
	now := time.Now()
	ec := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: releaseSender(), Now: func() time.Time { return now }})
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	count := func() int { return strings.Count(buf.String(), "ALERT p2p_expiry_payout_in_flight") }
	ec.RunOnce(context.Background())
	now = now.Add(time.Minute)
	ec.RunOnce(context.Background())
	if count() != 1 {
		t.Fatalf("alerts after two ticks = %d, want 1", count())
	}
	now = now.Add(time.Hour)
	ec.RunOnce(context.Background())
	if count() != 2 {
		t.Fatalf("alerts an hour later = %d, want 2", count())
	}
}

// Re-review N-2 backstop: a payout with no outcome that has aged out of the
// legacy window is never silent — a daily sweep alerts it.
func TestExpiry_PayoutWithNoOutcomePastTheWindowAlertsDaily(t *testing.T) {
	r := newPoolRig(t)
	if err := r.db.AutoMigrate(&models.JobRunMarker{}); err != nil {
		t.Fatal(err)
	}
	old := legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-30*time.Hour))
	r.db.Model(&models.P2PTrade{}).Where("id = ?", old.ID).Update("payout_started_at", time.Now().Add(-31*time.Hour))
	failed := legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-30*time.Hour))
	r.db.Model(&models.P2PTrade{}).Where("id = ?", failed.ID).Updates(map[string]any{
		"payout_started_at": time.Now().Add(-31 * time.Hour), "nomba_payout_id": "po_f", "cancel_reason": pmPayoutFailedReason + " (x)"})
	now := time.Now()
	ec := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: releaseSender(), Now: func() time.Time { return now }})
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	count := func() int { return strings.Count(buf.String(), "ALERT p2p_payout_outcome_unresolved") }
	ec.RunOnce(context.Background())
	if count() != 1 || !strings.Contains(buf.String(), "trade="+old.ID.String()) || strings.Contains(buf.String(), "trade="+failed.ID.String()) {
		t.Fatalf("first sweep:\n%s", buf.String())
	}
	now = now.Add(time.Hour)
	ec.RunOnce(context.Background())
	if count() != 1 {
		t.Fatalf("swept again within the day: %d", count())
	}
	now = now.Add(24 * time.Hour)
	ec.RunOnce(context.Background())
	if count() != 2 {
		t.Fatalf("no daily repeat: %d", count())
	}
}

// Pre-merge review M-4: the daily unresolved-payout sweep is claimed in the
// database (job_run_markers), so a deploy or restart does not re-alert every
// historical row; the day's schedule carries over to the new process.
func TestExpiry_PayoutUnresolvedAlertDoesNotRefireOnRestart(t *testing.T) {
	r := newPoolRig(t)
	if err := r.db.AutoMigrate(&models.JobRunMarker{}); err != nil {
		t.Fatal(err)
	}
	old := legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-30*time.Hour))
	r.db.Model(&models.P2PTrade{}).Where("id = ?", old.ID).Update("payout_started_at", time.Now().Add(-31*time.Hour))
	now := time.Now()
	boot := func() *ExpiryCanceller {
		return NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: releaseSender(), Now: func() time.Time { return now }})
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	count := func() int { return strings.Count(buf.String(), "ALERT p2p_payout_outcome_unresolved") }
	boot().RunOnce(context.Background())
	if count() != 1 {
		t.Fatalf("first boot: %d alerts", count())
	}
	now = now.Add(10 * time.Minute)
	boot().RunOnce(context.Background()) // a restart
	if count() != 1 {
		t.Fatalf("a restart re-fired the daily alert: %d", count())
	}
	now = now.Add(24 * time.Hour)
	boot().RunOnce(context.Background())
	if count() != 2 {
		t.Fatalf("no alert a day later: %d", count())
	}
}

// Re-review N-4: the canceller asks for the day's claim every minute, so an
// already-claimed day is a quiet no-op — never a duplicate-key error in the
// log each tick.
func TestClaimRunAt_AlreadyClaimedIsQuiet(t *testing.T) {
	r := newPoolRig(t)
	if err := r.db.AutoMigrate(&models.JobRunMarker{}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	db := r.db.Session(&gorm.Session{Logger: logger.New(log.New(&buf, "", 0), logger.Config{LogLevel: logger.Warn})})
	now := time.Now()
	ctx := context.Background()
	if !claimRunAt(ctx, db, "n4-test", time.Hour, now) {
		t.Fatal("the first claim must run")
	}
	// Another instance created the row first: the create path conflicts.
	if claimRunAt(ctx, db, "n4-test", time.Hour, now.Add(time.Minute)) || claimRunAt(ctx, db, "n4-test", time.Hour, now.Add(2*time.Minute)) {
		t.Fatal("a claimed run ran again inside its interval")
	}
	if !claimRunAt(ctx, db, "n4-test", time.Hour, now.Add(time.Hour+time.Second)) {
		t.Fatal("no claim after the interval")
	}
	if strings.Contains(strings.ToLower(buf.String()), "unique") || strings.Contains(strings.ToLower(buf.String()), "duplicate") {
		t.Fatalf("an already-claimed run logged an error:\n%s", buf.String())
	}
}

// Pre-merge review M-2: O-C1-2 lets ops refund (not bind) a payment that
// arrives for a trade the canceller decided. The canceller now names such a
// held or unmatched credit when it cancels the trade, so ops do not learn of
// it only from a failed assign. A credit of another amount is not named.
func TestExpiry_LegacyCancelAlertsAMatchingHeldCredit(t *testing.T) {
	r := pmLegacy(t)
	createInboundCreditsTable(t, r.db)
	held := p2pInboundCredit("999000000090", "1500.00")
	held.Status, held.HoldReasonCode = models.InboundCreditHeld, "trade_not_within_merchant_cap"
	other := p2pInboundCredit("999000000091", "1499.00")
	for _, c := range []*models.InboundCredit{held, other} {
		c.TransactionDate, c.SeenAt = time.Now(), time.Now()
		if err := r.db.Create(c).Error; err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	tick, err := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.src.trades, Signer: releaseSender()}).RunOnce(context.Background())
	if err != nil || tick.Legacy != 1 {
		t.Fatalf("tick=%+v err=%v, want the trade cancelled", tick, err)
	}
	out := buf.String()
	if strings.Count(out, "ALERT p2p_expiry_cancelled_with_held_credit") != 1 ||
		!strings.Contains(out, "trade="+r.trade.ID.String()) || !strings.Contains(out, "credit="+held.ID.String()) ||
		strings.Contains(out, other.ID.String()) || strings.Contains(out, "JOHN DOE") {
		t.Fatalf("want one alert linking the trade to the held credit, no name:\n%s", out)
	}
}

// Review M10: an unmined cancelByTimeout is not re-sent every minute.
func TestExpiry_DoesNotResendWhileTheCancelIsUnmined(t *testing.T) {
	r := newPoolRig(t)
	sender := releaseSender()
	created := time.Now().Add(-61 * time.Minute)
	legacyTrade(t, r, models.P2PTradeStatusPendingPayment, time.Now().Add(-31*time.Minute))
	r.sonic.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending, CreatedAt: big.NewInt(created.Unix())}
	now := time.Now()
	ec := NewExpiryCanceller(ExpiryCancellerConfig{Trades: r.svc, Signer: sender, Now: func() time.Time { return now }})
	ctx := context.Background()
	ec.RunOnce(ctx)
	now = now.Add(time.Minute)
	ec.RunOnce(ctx)
	if len(sender.sendChains) != 1 {
		t.Fatalf("sends = %d within the back-off, want 1", len(sender.sendChains))
	}
	now = now.Add(expiryResendAfter)
	ec.RunOnce(ctx)
	if len(sender.sendChains) != 2 {
		t.Fatalf("sends = %d after the back-off, want a second try", len(sender.sendChains))
	}
}
