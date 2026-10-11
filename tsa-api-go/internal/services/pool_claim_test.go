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
)

// §12.3: the P2P claim source never offers or settles a slice (its ad has a
// pm_id, so the old filter matched it).
func TestP2PClaimSource_SkipsPoolSlices(t *testing.T) {
	r := newForwardRig(t, "1500")
	intent := uuid.New()
	r.db.Model(&models.P2PTrade{}).Where("id = ?", r.trade.ID).Update("it_intent_id", intent)
	claims, err := r.src.OpenClaims(context.Background(), "NGN")
	if err != nil || len(claims) != 0 {
		t.Fatalf("claims = %v %v, want the slice skipped", claims, err)
	}
	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("sess_slice", "1500")); !errors.Is(err, ErrTradeNotFound) {
		t.Fatalf("Accept on a slice = %v", err)
	}
	if !ngnEqual(r.availableNGN(t), "0") {
		t.Fatalf("merchant credited %s from a slice", r.availableNGN(t))
	}
}

// S11 / §12.3: an opening pool order is never a claim.
func TestPoolOpen_OpeningIsNotAClaim(t *testing.T) {
	r := newPoolOpenRig(t)
	opening := seedProviderIntent(t, r.it, r.userID, models.ITProviderPool, models.ITStatusOpening)
	src := NewITStableClaimSource(r.it)
	claims, err := src.OpenClaims(context.Background(), "NGN")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range claims {
		if c.ID == opening.ID {
			t.Fatal("an opening pool order was offered as a claim")
		}
	}
	credit := &models.InboundCredit{ID: uuid.New(), ProviderRef: "sess_open", Currency: "NGN", AmountKobo: 1_000_000, GrossKobo: 1_000_000}
	if err := src.Accept(context.Background(), opening.ID, credit); !errors.Is(err, ErrITBadState) {
		t.Fatalf("manual bind to an opening order = %v", err)
	}
}

func pend(id string, units int64, minute int) PoolPendingSlice {
	return PoolPendingSlice{ID: uuid.MustParse(id), Units: big.NewInt(units), CreatedAt: time.Date(2026, 10, 9, 12, minute, 0, 0, time.UTC)}
}

// S18 / review I6: the fewest slices, then the smallest total, and a forced
// surplus no larger than max(min slice, 2% of the subset).
func TestPoolLate_MinimalSubsetSurplusBound(t *testing.T) {
	a := pend("00000000-0000-0000-0000-00000000000a", 60_000_000, 0)
	b := pend("00000000-0000-0000-0000-00000000000b", 12_419_593, 1)
	pending := []PoolPendingSlice{a, b}
	min := big.NewInt(2_000_000)
	zero := new(big.Int)
	for _, tc := range []struct {
		live    int64
		covered bool
		keep    []uuid.UUID
		surplus int64
	}{
		{72_419_593, true, []uuid.UUID{a.ID, b.ID}, 0},
		{71_000_000, true, []uuid.UUID{a.ID, b.ID}, 1_419_593}, // ≤ $2
		{70_000_000, false, nil, 0},                            // 2.419593 > max($2, 1.448)
		{60_500_000, false, nil, 0},                            // A alone is short; A+B forces 11.9
		{59_000_000, true, []uuid.UUID{a.ID}, 1_000_000},       // A alone, B dropped
		{12_000_000, true, []uuid.UUID{b.ID}, 419_593},         // B (smaller total) over A
	} {
		got := EvaluatePoolCoverage(big.NewInt(tc.live), zero, pending, min)
		if got.Covered != tc.covered || (tc.covered && (len(got.Keep) != len(tc.keep) || got.Surplus.Int64() != tc.surplus)) {
			t.Fatalf("live %d: %+v, want covered=%v keep=%v surplus=%d", tc.live, got, tc.covered, tc.keep, tc.surplus)
		}
		for i := range tc.keep {
			if got.Keep[i] != tc.keep[i] {
				t.Fatalf("live %d: keep %v, want %v", tc.live, got.Keep, tc.keep)
			}
		}
	}
	// Already released covers it: nothing more is released, every pending slice drops.
	got := EvaluatePoolCoverage(big.NewInt(50_000_000), big.NewInt(60_000_000), pending, min)
	if !got.Covered || len(got.Keep) != 0 || len(got.Drop) != 2 || got.Surplus.Int64() != 10_000_000 {
		t.Fatalf("over-released = %+v", got)
	}
}

func lateClaim(t *testing.T, r *poolOpenRig, intent *models.ITIntent, naira int64) (bool, error) {
	t.Helper()
	r.it.SetTreasuryMarketRate(&fixedTreasuryRate{rate: poolMid})
	late := intent.RateExpiresAt.Add(time.Minute) // after the honour window: priced live (D33)
	return r.it.claimAndPriceStableBuy(context.Background(), intent, "sess_late_"+intent.ID.String()[:6],
		big.NewInt(naira*100).String(), late, false, PayInCentral)
}

// S18 covered: claimed at the live price, the forced surplus booked.
func TestPoolLate_CoveredClaimsLiveRecordsSurplus(t *testing.T) {
	r := newPoolOpenRig(t)
	threeLPs(t, r)
	intent, _, err := r.open(t, "100000")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := lateClaim(t, r, intent, 99_000) // live net 71.695397 of the 72.419593 locked
	got := r.intent(t, intent.ID)
	if !ok || err != nil || got.Status != models.ITStatusPaymentReceived || !got.Repriced || got.AssetAmount != "71695397" {
		t.Fatalf("claim = %v %v, intent = %+v", ok, err, got)
	}
	var po models.PoolOrder
	r.db.First(&po, "intent_id = ? AND generation = 1", intent.ID)
	if po.SurplusUnits != "724196" {
		t.Fatalf("surplus = %s, want 0.724196", po.SurplusUnits)
	}
	for _, sl := range r.slicesOf(t, intent.ID) {
		if sl.DroppedAt != nil {
			t.Fatalf("slice %s dropped though both back the order", sl.ID)
		}
	}
}

// S18 not covered: park pool_slices_short and free every pending slice.
func TestPoolLate_UncoveredParks(t *testing.T) {
	r := newPoolOpenRig(t)
	threeLPs(t, r)
	intent, _, err := r.open(t, "100000")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := lateClaim(t, r, intent, 120_000) // live ≈ 86.9 > 72.42 locked
	got := r.intent(t, intent.ID)
	if !ok || err != nil || got.Status != models.ITStatusBuyAwaitingRate || !strings.HasPrefix(got.FailureReason, models.ITFailurePoolSlicesShort) {
		t.Fatalf("claim = %v %v, intent = %+v", ok, err, got)
	}
	for _, sl := range r.slicesOf(t, intent.ID) {
		if sl.DroppedAt == nil {
			t.Fatalf("slice %s still backs a parked order", sl.ID)
		}
	}
}

// The honoured path is today's: exact naira in the window, slices untouched.
func TestPoolHonoured_ClaimsWithoutTouchingSlices(t *testing.T) {
	r := newPoolOpenRig(t)
	threeLPs(t, r)
	intent, _, err := r.open(t, "100000")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := r.it.claimAndPriceStableBuy(context.Background(), intent, "sess_ok", intent.AmountMinor, time.Now(), false, PayInCentral)
	got := r.intent(t, intent.ID)
	if !ok || err != nil || got.Status != models.ITStatusPaymentReceived || got.Repriced || got.AssetAmount != "72419593" {
		t.Fatalf("honoured = %v %v %+v", ok, err, got)
	}
}

// S18 covered by a subset: the slice the order no longer needs is dropped for
// the canceller, the kept one is not, and the surplus is the kept total − live.
func TestPoolLate_CoveredBySubsetDropsTheRest(t *testing.T) {
	r := newPoolOpenRig(t)
	threeLPs(t, r)
	intent, _, err := r.open(t, "100000")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := lateClaim(t, r, intent, 82_500) // live ≈ 59.75: slice A (60) alone
	got := r.intent(t, intent.ID)
	if !ok || err != nil || got.Status != models.ITStatusPaymentReceived || !got.Repriced {
		t.Fatalf("claim = %v %v, intent = %+v", ok, err, got)
	}
	live, _ := new(big.Int).SetString(got.AssetAmount, 10)
	var po models.PoolOrder
	r.db.First(&po, "intent_id = ? AND generation = 1", intent.ID)
	if want := new(big.Int).Sub(big.NewInt(60_000_000), live).String(); po.SurplusUnits != want {
		t.Fatalf("surplus = %s, want %s (live %s)", po.SurplusUnits, want, live)
	}
	for _, sl := range r.slicesOf(t, intent.ID) {
		kept := sl.TokenAmount == "60000000000000000000"
		if kept == (sl.DroppedAt != nil) {
			t.Fatalf("slice %s (%s) dropped=%v, want only the unselected slice dropped", sl.ID, sl.TokenAmount, sl.DroppedAt != nil)
		}
	}
}

// No market rate is transient on the central path: nothing is written, the
// order stays payable and every slice still backs it; the poller retries.
func TestPoolLate_NoMidWritesNothing(t *testing.T) {
	r := newPoolOpenRig(t)
	threeLPs(t, r)
	intent, _, err := r.open(t, "100000")
	if err != nil {
		t.Fatal(err)
	}
	r.it.SetTreasuryMarketRate(&fixedTreasuryRate{err: errors.New("rate feed down")})
	late := intent.RateExpiresAt.Add(time.Minute)
	ok, err := r.it.claimAndPriceStableBuy(context.Background(), intent, "sess_nomid", big.NewInt(99_000_00).String(), late, false, PayInCentral)
	got := r.intent(t, intent.ID)
	if ok || err == nil || got.Status != models.ITStatusPendingPayment || got.PaymentRef != "" {
		t.Fatalf("claim = %v %v, intent = %+v, want an error and nothing written", ok, err, got)
	}
	for _, sl := range r.slicesOf(t, intent.ID) {
		if sl.DroppedAt != nil {
			t.Fatalf("slice %s dropped on a transient outage", sl.ID)
		}
	}
}

// Review C-2: an honoured claim decides under the pool lock with the intent
// row locked, over the slices that still back the order. A slice the
// canceller or F21 already dropped does not back it: the order parks
// pool_slices_short instead of being paid against stock that is going back.
func TestPoolHonoured_ParksWhenASliceWasDropped(t *testing.T) {
	for _, how := range []string{"canceller", "f21"} {
		t.Run(how, func(t *testing.T) {
			r, intent, sl, _, _ := openOne(t)
			ctx := context.Background()
			switch how {
			case "canceller":
				r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Update("dropped_at", time.Now())
			case "f21":
				r.chain.setTrade(HashID(sl.ID), OnChainP2PStatusDisputed)
				if _, err := r.trades.ReconcileTrade(ctx, sl.ID); err != nil {
					t.Fatal(err)
				}
				if len(r.chain.resolves) != 1 || r.chain.resolves[0] {
					t.Fatalf("resolves = %v, want the unpaid slice back to its LP", r.chain.resolves)
				}
			}
			ok, err := r.it.claimAndPriceStableBuy(ctx, intent, "sess_honour_"+how, intent.AmountMinor, time.Now(), false, PayInCentral)
			got := r.intent(t, intent.ID)
			if !ok || err != nil || got.Status != models.ITStatusBuyAwaitingRate || !strings.HasPrefix(got.FailureReason, models.ITFailurePoolSlicesShort) {
				t.Fatalf("honoured claim on a dropped slice = %v %v, intent %s/%q; want parked %s", ok, err, got.Status, got.FailureReason, models.ITFailurePoolSlicesShort)
			}
		})
	}
}

// Review M-13: a claim whose pool-order snapshot is missing still stands (the
// money is hers), but the unbooked surplus is alerted, never silent.
func TestPoolLate_MissingSnapshotAlertsTheSurplus(t *testing.T) {
	r := newPoolOpenRig(t)
	threeLPs(t, r)
	intent, _, err := r.open(t, "100000")
	if err != nil {
		t.Fatal(err)
	}
	r.db.Where("intent_id = ?", intent.ID).Delete(&models.PoolOrder{})
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if ok, err := lateClaim(t, r, intent, 99_000); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if !strings.Contains(buf.String(), "ALERT liquidity_pool_surplus_unrecorded") {
		t.Fatalf("no surplus alert:\n%s", buf.String())
	}
}
