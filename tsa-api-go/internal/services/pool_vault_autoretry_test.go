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
		pre + vaultRefusedLiquidity:                                                                  true,
		pre + vaultRefusedLiquidity + poolFillRepriceMark:                                            true,
		pre + "the vault refuses the fill: Insufficient liquidity":                                   true,
		pre + "the vault refuses the fill: EnforcedPause()":                                          false,
		pre + "the vault refuses the fill: Over fill window cap":                                     false,
		pre + "the vault refuses the fill: Insufficient liquidity, too":                              false,
		pre + fillRefusedPerTx:                                                                       false,
		pre + fillRefusedWindow:                                                                      false,
		pre + fillRefusedSpread + poolFillRepriceMark:                                                false,
		pre + "the fill 0xabc reverted":                                                              false,
		pre + "the fill (1) does not match the amount owed (2): retry re-reserves it from the order": false,
		vaultRefusedLiquidity:                                                                        false,
		"pool_fill_refused_x: " + vaultRefusedLiquidity:                                              false,
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
