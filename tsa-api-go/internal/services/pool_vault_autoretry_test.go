package services

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Review 2026-10-15: a paid order parked because the pool ran short (an LP
// withdrew before the company refilled) resumes on its own once liquidity
// returns; any other park still waits for an operator.

func TestPoolFillParkedOnLiquidity_Classifies(t *testing.T) {
	pre := models.ITFailurePoolFillRefused + ": "
	for reason, want := range map[string]bool{
		pre + vaultRefusedLiquidity:                                                             true,
		pre + vaultRefusedLiquidity + poolFillRepriceMark:                                       false,
		pre + "the pool's free collateral does not cover the amount owed":                       true,
		pre + "the pool's free collateral does not cover the amount owed" + poolFillRepriceMark: false,
		pre + "the vault refuses the fill: Insufficient liquidity":                              true,
		pre + "the vault refuses the fill: Insufficient liquidity" + poolFillRepriceMark:        false,
		pre + "the vault refuses the fill: EnforcedPause()":                                     false,
		pre + "the vault refuses the fill: Over fill window cap":                                false,
		pre + "the vault refuses the fill: Insufficient liquidity, too":                         false,
		pre + fillRefusedPerTx:                        false,
		pre + fillRefusedWindow:                       false,
		pre + fillRefusedSpread + poolFillRepriceMark: false,
		pre + "the fill 0xabc reverted":               false,
		pre + "the fill (1) does not match the amount owed (2): retry re-reserves it from the order": false,
		vaultRefusedLiquidity:                           false,
		"pool_fill_refused_x: " + vaultRefusedLiquidity: false,
	} {
		if got := poolFillParkedOnLiquidity(reason); got != want {
			t.Errorf("%q = %v, want %v", reason, got, want)
		}
	}
}

// Parked on the contract's "Insufficient liquidity": a tick while still short
// leaves it parked; after a refill the tick resumes it to delivering (audited
// as the system, no actor) and the settle step sends its fill.
func TestVaultAutoRetry_ResumesALiquidityParkOnceRefilled(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	ctx := context.Background()
	intent := r.openPaid(t, "5000")
	r.vault.mu.Lock()
	r.vault.balance = usdt18(1)
	r.vault.mu.Unlock()
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got := r.intent(t, intent.ID)
	if got.Status != models.ITStatusBuyAwaitingRate || got.FailureReason != models.ITFailurePoolFillRefused+": the vault refuses the fill: Insufficient liquidity" {
		t.Fatalf("status = %s %q, want the liquidity park", got.Status, got.FailureReason)
	}
	if n, err := r.it.AutoRetryPoolFills(ctx); err != nil || n != 0 {
		t.Fatalf("tick while short = %d %v, want 0", n, err)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusBuyAwaitingRate || got.FailureReason == "" {
		t.Fatalf("status = %s %q, want still parked", got.Status, got.FailureReason)
	}
	r.vault.mu.Lock()
	r.vault.balance = usdt18(100)
	r.vault.mu.Unlock()
	if n, err := r.it.AutoRetryPoolFills(ctx); err != nil || n != 1 {
		t.Fatalf("tick after refill = %d %v, want 1", n, err)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusDelivering || got.FailureReason != "" {
		t.Fatalf("status = %s %q, want delivering", got.Status, got.FailureReason)
	}
	var audits []models.ITIntentAuditLog
	if err := r.db.Where("intent_id = ? AND action = ?", intent.ID, models.ITAuditPoolFillRetried).Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].ActorID != nil || audits[0].Note != poolFillAutoRetryNote {
		t.Fatalf("audits = %+v, want one system retry", audits)
	}
	if err := r.advance(t, intent.ID); err != nil || r.order(t, intent.ID).FillTxHash == "" {
		t.Fatalf("advance after auto-retry: %v, want the fill sent", err)
	}
	// Nothing parked any more: the next tick is a no-op.
	if n, err := r.it.AutoRetryPoolFills(ctx); err != nil || n != 0 {
		t.Fatalf("idle tick = %d %v", n, err)
	}
}

// A claim park on the free-liquidity refusal (an LP's withdrawal went pending
// between open and payment) resumes too once it is paid.
func TestVaultAutoRetry_ResumesAClaimLiquidityPark(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	ctx := context.Background()
	older, err := r.openFor(t, r.userID, "5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	r.vault.setPending("0x00000000000000000000000000000000000000c1", usdt18(99)) // liquid 1
	if ok, err := r.it.claimAndPriceStableBuy(ctx, older, "sess_a", older.AmountMinor, time.Now(), false, PayInCentral); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if got := r.intent(t, older.ID); got.FailureReason != models.ITFailurePoolFillRefused+": "+vaultRefusedLiquidity {
		t.Fatalf("park = %q, want the free-liquidity refusal", got.FailureReason)
	}
	r.vault.setPending("0x00000000000000000000000000000000000000c1", new(big.Int))
	if n, err := r.it.AutoRetryPoolFills(ctx); err != nil || n != 1 {
		t.Fatalf("tick = %d %v, want 1", n, err)
	}
	if got := r.intent(t, older.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %s, want delivering", got.Status)
	}
}

// A fill-limit park is an operator's call: liquid or not, the tick leaves it.
func TestVaultAutoRetry_LeavesACapsPark(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	ctx := context.Background()
	intent, err := r.openFor(t, r.userID, "5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	r.vault.mu.Lock()
	r.vault.caps.MaxFillPerTx = usdt18(1)
	r.vault.mu.Unlock()
	if ok, err := r.it.claimAndPriceStableBuy(ctx, intent, "sess_c", intent.AmountMinor, time.Now(), false, PayInCentral); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	r.vault.mu.Lock()
	r.vault.caps.MaxFillPerTx = usdt18(10_000) // even once the limit would pass
	r.vault.mu.Unlock()
	if n, err := r.it.AutoRetryPoolFills(ctx); err != nil || n != 0 {
		t.Fatalf("tick = %d %v, want 0", n, err)
	}
	got := r.intent(t, intent.ID)
	if got.Status != models.ITStatusBuyAwaitingRate || !strings.HasSuffix(got.FailureReason, fillRefusedPerTx) {
		t.Fatalf("status = %s %q, want still the caps park", got.Status, got.FailureReason)
	}
}

// vaultRigLiquidityPark is an order paid and parked at claim on the
// free-liquidity refusal (an LP's withdrawal went pending first), then the
// pending withdrawal cleared again; reason overrides the park's text.
func (r *vaultRig) liquidityPark(t *testing.T, sess, reason string) *models.ITIntent {
	t.Helper()
	ctx := context.Background()
	intent, err := r.openFor(t, r.userID, "5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	r.vault.setPending("0x00000000000000000000000000000000000000c1", r.vault.balance) // liquid 0
	if ok, err := r.it.claimAndPriceStableBuy(ctx, intent, sess, intent.AmountMinor, time.Now(), false, PayInCentral); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	r.vault.setPending("0x00000000000000000000000000000000000000c1", new(big.Int))
	if got := r.intent(t, intent.ID); got.FailureReason != models.ITFailurePoolFillRefused+": "+vaultRefusedLiquidity {
		t.Fatalf("park = %q, want the free-liquidity refusal", got.FailureReason)
	}
	if reason != "" {
		if err := r.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("failure_reason", reason).Error; err != nil {
			t.Fatal(err)
		}
	}
	got := r.intent(t, intent.ID)
	return &got
}

// A park from before the no-collateral deploy (its reason still says "free
// collateral") is a liquidity park too, and resumes.
func TestVaultAutoRetry_ResumesAPreRenameCollateralPark(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	p := r.liquidityPark(t, "sess_legacy", models.ITFailurePoolFillRefused+": the pool's free collateral does not cover the amount owed")
	if n, err := r.it.AutoRetryPoolFills(context.Background()); err != nil || n != 1 {
		t.Fatalf("tick = %d %v, want 1", n, err)
	}
	if got := r.intent(t, p.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %s, want delivering", got.Status)
	}
}

// A park priced live at claim waits for a super admin: a retry would sell at
// a new rate, which is a person's decision.
func TestVaultAutoRetry_LeavesARepricedPark(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	reason := models.ITFailurePoolFillRefused + ": " + vaultRefusedLiquidity + poolFillRepriceMark
	p := r.liquidityPark(t, "sess_reprice", reason)
	if n, err := r.it.AutoRetryPoolFills(context.Background()); err != nil || n != 0 {
		t.Fatalf("tick = %d %v, want 0", n, err)
	}
	if got := r.intent(t, p.ID); got.Status != models.ITStatusBuyAwaitingRate || got.FailureReason != reason {
		t.Fatalf("status = %s %q, want still parked", got.Status, got.FailureReason)
	}
}

// The vault is read once per tick; parks the slack cannot cover are skipped
// without RetryPoolFill (no pool lock, no further vault read).
func TestVaultAutoRetry_ReadsTheVaultOnceAndSkipsWhatCannotFit(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	a := r.liquidityPark(t, "sess_a1", "")
	b := r.liquidityPark(t, "sess_b1", "")
	fill := bigFromDecimal(r.order(t, a.ID).FillUnits)
	both := new(big.Int).Add(fill, bigFromDecimal(r.order(t, b.ID).FillUnits))
	r.vault.mu.Lock()
	r.vault.balance = new(big.Int).Sub(both, big.NewInt(1)) // one unit short of both reservations
	r.vault.stateCalls = 0
	r.vault.mu.Unlock()
	if n, err := r.it.AutoRetryPoolFills(context.Background()); err != nil || n != 0 {
		t.Fatalf("tick = %d %v, want 0", n, err)
	}
	if r.vault.stateCalls != 1 {
		t.Fatalf("vault reads = %d, want 1 (no per-order retry)", r.vault.stateCalls)
	}
	r.vault.mu.Lock()
	r.vault.balance = both
	r.vault.stateCalls = 0
	r.vault.mu.Unlock()
	if n, err := r.it.AutoRetryPoolFills(context.Background()); err != nil || n != 2 {
		t.Fatalf("tick = %d %v, want both resumed", n, err)
	}
	if r.vault.stateCalls != 3 {
		t.Fatalf("vault reads = %d, want 1 + one per resumed retry", r.vault.stateCalls)
	}
}

// One order's own retry error (here its pool order row is gone) is not the
// job's failure: it is logged once an hour, the tick goes on and resumes the
// next order.
func TestVaultAutoRetry_AnOrdersErrorDoesNotFailTheTick(t *testing.T) {
	prev := reconcileAlerts
	reconcileAlerts = &alertLimiter{last: map[string]time.Time{}}
	t.Cleanup(func() { reconcileAlerts = prev })
	r := newVaultRig(t, 100, 25)
	broken := r.liquidityPark(t, "sess_broken", "")
	ok := r.liquidityPark(t, "sess_ok", "")
	if err := r.db.Where("intent_id = ?", broken.ID).Delete(&models.PoolOrder{}).Error; err != nil {
		t.Fatal(err)
	}
	logs := captureLog(t)
	if n, err := r.it.AutoRetryPoolFills(context.Background()); err != nil || n != 1 {
		t.Fatalf("tick = %d %v, want the other order resumed and no error", n, err)
	}
	if got := r.intent(t, ok.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %s, want delivering", got.Status)
	}
	if n, err := r.it.AutoRetryPoolFills(context.Background()); err != nil || n != 0 {
		t.Fatalf("second tick = %d %v", n, err)
	}
	if c := strings.Count(logs.String(), "auto-retry intent="+broken.ID.String()); c != 1 {
		t.Fatalf("logged %d times, want once:\n%s", c, logs.String())
	}
}
