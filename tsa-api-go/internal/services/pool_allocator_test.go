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
)

func cand(id string, freeUnits int64, deficit float64, lastFill *time.Time) poolCandidate {
	return poolCandidate{lp: models.PaymentMerchant{ID: uuid.MustParse(id)}, free: big.NewInt(freeUnits),
		deficit: new(big.Rat).SetFloat64(deficit), lastFill: lastFill}
}

func unitsOf(p []PoolSlicePlan) []int64 {
	out := make([]int64, len(p))
	for i := range p {
		out[i] = p[i].Units.Int64()
	}
	return out
}

// S13: the whole order to one LP, the one whose fills lag its stock most.
func TestPoolAllocator_WholeOrderByDeficit(t *testing.T) {
	a := cand("00000000-0000-0000-0000-00000000000a", 50_000_000, 0.10, nil)
	b := cand("00000000-0000-0000-0000-00000000000b", 50_000_000, 0.30, nil)
	c := cand("00000000-0000-0000-0000-00000000000c", 4_000_000, 0.90, nil) // biggest deficit, cannot cover
	p, err := allocatePool([]poolCandidate{a, b, c}, big.NewInt(5_000_000), 3, big.NewInt(2_000_000))
	if err != nil || len(p) != 1 || p[0].LP.ID != b.lp.ID || p[0].Units.Int64() != 5_000_000 {
		t.Fatalf("plan = %+v %v, want all 5 USDT to B", p, err)
	}
}

func TestPoolAllocator_TieLeastRecentlyFilledThenID(t *testing.T) {
	recent, old := time.Now(), time.Now().Add(-48*time.Hour)
	a := cand("00000000-0000-0000-0000-00000000000a", 10_000_000, 0.2, &recent)
	b := cand("00000000-0000-0000-0000-00000000000b", 10_000_000, 0.2, &old)
	c := cand("00000000-0000-0000-0000-00000000000c", 10_000_000, 0.2, nil) // never filled: first
	if p, _ := allocatePool([]poolCandidate{a, b, c}, big.NewInt(3_000_000), 3, big.NewInt(2_000_000)); p[0].LP.ID != c.lp.ID {
		t.Fatalf("never-filled LP not first: %v", p[0].LP.ID)
	}
	if p, _ := allocatePool([]poolCandidate{a, b}, big.NewInt(3_000_000), 3, big.NewInt(2_000_000)); p[0].LP.ID != b.lp.ID {
		t.Fatalf("least recently filled not first: %v", p[0].LP.ID)
	}
	d := cand("00000000-0000-0000-0000-00000000000d", 10_000_000, 0.2, &recent)
	if p, _ := allocatePool([]poolCandidate{d, a}, big.NewInt(3_000_000), 3, big.NewInt(2_000_000)); p[0].LP.ID != a.lp.ID {
		t.Fatalf("lowest id not the last tie-break: %v", p[0].LP.ID)
	}
}

// Spec §10.2: free 60 / 45 / 20, U = 72.419593 → A 60.000000, B 12.419593.
func TestPoolAllocator_SplitFewestSlicesLargestFirst(t *testing.T) {
	a := cand("00000000-0000-0000-0000-00000000000a", 60_000_000, 0, nil)
	b := cand("00000000-0000-0000-0000-00000000000b", 45_000_000, 0, nil)
	c := cand("00000000-0000-0000-0000-00000000000c", 20_000_000, 0, nil)
	p, err := allocatePool([]poolCandidate{c, b, a}, big.NewInt(72_419_593), 3, big.NewInt(2_000_000))
	if err != nil || len(p) != 2 || p[0].LP.ID != a.lp.ID || p[1].LP.ID != b.lp.ID ||
		p[0].Units.Int64() != 60_000_000 || p[1].Units.Int64() != 12_419_593 {
		t.Fatalf("plan = %v %v", unitsOf(p), err)
	}
}

func TestPoolAllocator_TailNeverBelowMinSlice(t *testing.T) {
	a := cand("00000000-0000-0000-0000-00000000000a", 10_000_000, 0, nil)
	b := cand("00000000-0000-0000-0000-00000000000b", 9_500_000, 0, nil)
	p, err := allocatePool([]poolCandidate{a, b}, big.NewInt(11_000_000), 3, big.NewInt(2_000_000))
	if err != nil || len(p) != 2 || p[0].Units.Int64() != 9_000_000 || p[1].Units.Int64() != 2_000_000 {
		t.Fatalf("plan = %v %v, want 9 + 2 (never a 1-USDT tail)", unitsOf(p), err)
	}
}

func TestPoolAllocator_RefusesBeyondSliceCap(t *testing.T) {
	var cs []poolCandidate
	for i := 0; i < 4; i++ {
		cs = append(cs, cand(uuid.NewString(), 3_000_000, 0, nil))
	}
	if _, err := allocatePool(cs, big.NewInt(10_000_000), 3, big.NewInt(2_000_000)); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("4 slices needed under a cap of 3: %v", err)
	}
	if p, err := allocatePool(cs, big.NewInt(9_000_000), 3, big.NewInt(2_000_000)); err != nil || len(p) != 3 {
		t.Fatalf("3 slices: %v %v", unitsOf(p), err)
	}
}

// S13 fairness: whole orders by deficit converge fill share to stock share.
func TestPoolAllocator_ConvergesToStockShare(t *testing.T) {
	free := []int64{3_000_000_000, 1_500_000_000, 500_000_000}
	ids := []string{"00000000-0000-0000-0000-00000000000a", "00000000-0000-0000-0000-00000000000b", "00000000-0000-0000-0000-00000000000c"}
	fills := make([]int64, 3)
	var total int64
	for n := 0; n < 1000; n++ {
		size := int64(3+n%47) * 1_000_000
		var cs []poolCandidate
		for i := range free {
			fill := 0.0
			if total > 0 {
				fill = float64(fills[i]) / float64(total)
			}
			cs = append(cs, cand(ids[i], free[i], float64(free[i])/5_000_000_000-fill, nil))
		}
		p, err := allocatePool(cs, big.NewInt(size), 3, big.NewInt(2_000_000))
		if err != nil {
			t.Fatal(err)
		}
		for i := range ids {
			if p[0].LP.ID.String() == ids[i] {
				fills[i] += size
			}
		}
		total += size
	}
	for i := range free {
		got, want := float64(fills[i])/float64(total), float64(free[i])/5_000_000_000
		if got < want-0.05 || got > want+0.05 {
			t.Fatalf("LP %d fill share %.3f, stock share %.3f (want within 5%%)", i, got, want)
		}
	}
}

// §9.1 eligibility through the desk: paused LP, expiring voucher, stake above
// the max, unconfirmed reservations, and Plan B M10 (LatestStockAd + active).
func TestPoolAllocator_SkipsPausedExpiredVoucherAboveMax(t *testing.T) {
	r := newDeskRig(t)
	ctx := context.Background()
	paused, _ := r.addLP(t, 100)
	r.db.Model(paused).Update("status", models.PaymentMerchantStatusPaused)
	_, expiring := r.addLP(t, 100)
	r.db.Model(expiring).Update("voucher_deadline", time.Now().Add(60*time.Minute)) // < window + 10 min
	big1, _ := r.addLP(t, 6_000)                                                    // above the $5,000 max
	cs, err := r.desk.candidates(ctx, r.db, specPoolSettings(), nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].lp.ID != big1.ID || cs[0].free.Cmp(canonical(5_000)) != 0 {
		t.Fatalf("candidates = %+v, want only the big LP, free capped at 5,000", cs)
	}
}

func TestPoolAllocator_StakeCappedAtLPMax(t *testing.T) {
	r := newDeskRig(t)
	lp, _ := r.addLP(t, 0)
	r.esc.setBalance(lp.OperatorWalletAddress, usdt18(4_000), usdt18(2_000)) // held 6,000, 2,000 locked
	cs, _ := r.desk.candidates(context.Background(), r.db, specPoolSettings(), nil, nil, true)
	if len(cs) != 1 || cs[0].free.Cmp(canonical(3_000)) != 0 {
		t.Fatalf("free = %v, want min(avail 4,000, stake 5,000 − locked 2,000) = 3,000", cs)
	}
}

func TestPoolAllocator_UnconfirmedSlicesReduceFree(t *testing.T) {
	r := newDeskRig(t)
	_, ad := r.addLP(t, 100)
	seedPoolSlice(t, r.db, sliceSeed{Intent: uuid.New(), Ad: ad, Units: usdt18(30)})                                                     // reserved, not yet proven
	seedPoolSlice(t, r.db, sliceSeed{Intent: uuid.New(), Ad: ad, Units: usdt18(10), CreatedAt: time.Now().UTC().Add(-20 * time.Minute)}) // stale: on-chain decides (UTC: SQLite compares times as text)
	cs, _ := r.desk.candidates(context.Background(), r.db, specPoolSettings(), nil, nil, true)
	if len(cs) != 1 || cs[0].free.Cmp(canonical(70)) != 0 {
		t.Fatalf("free = %v, want 100 − 30 unconfirmed", cs)
	}
}

// Plan B M10: only the LP's LatestStockAd, and it must be active.
func TestPoolAllocator_UsesLatestStockAdAndActiveLP(t *testing.T) {
	r := newDeskRig(t)
	lp, older := r.addLP(t, 100)
	newer := seedActiveAd(t, r.db, lp.UserID, poolTestChain, &lp.ID, "0", usdt18(500_000).String())
	r.db.Model(newer).Updates(map[string]any{"status": models.P2PAdStatusPaused, "created_at": time.Now().Add(time.Minute)})
	cs, _ := r.desk.candidates(context.Background(), r.db, specPoolSettings(), nil, nil, false)
	if len(cs) != 0 {
		t.Fatalf("an older active ad (%s) was used while the latest is paused: %+v", older.ID, cs)
	}
}

// M11: the fresh read of the chosen LP beats a stale 30 s cache.
func TestPoolAllocator_FreshReadBeatsStaleCache(t *testing.T) {
	r := newDeskRig(t)
	ctx := context.Background()
	lp, _ := r.addLP(t, 100)
	if _, err := r.desk.candidates(ctx, r.db, specPoolSettings(), nil, nil, false); err != nil { // warm the cache
		t.Fatal(err)
	}
	r.esc.setBalance(lp.OperatorWalletAddress, usdt18(10), new(big.Int)) // the LP withdrew
	if _, _, err := r.desk.plan(ctx, r.db, canonical(50), specPoolSettings(), nil); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("plan on a stale 100 = %v, want refused after the fresh read shows 10", err)
	}
	if p, _, err := r.desk.plan(ctx, r.db, canonical(8), specPoolSettings(), nil); err != nil || p[0].Units.Cmp(canonical(8)) != 0 {
		t.Fatalf("8 of a fresh 10 = %v %v", p, err)
	}
}

// F23: a voucher cancelled on-chain closes the stock ad and is never allocated.
func TestAllocator_SkipsCancelledVoucher(t *testing.T) {
	r := newDeskRig(t)
	ctx := context.Background()
	_, gone := r.addLP(t, 100)
	other, _ := r.addLP(t, 60) // less stock: the cancelled LP is chosen first, then read fresh
	h, err := decodeHex32(gone.VoucherHash)
	if err != nil {
		t.Fatal(err)
	}
	r.esc.cancelled[h] = true
	// Review C2: the whole allocation runs inside the pool lock's transaction,
	// as insertOrder runs it. On SQLite :memory: any read on s.db instead of tx
	// would open a second, empty database and skip every LP.
	var p []PoolSlicePlan
	var closeAfter []models.P2PAd
	err = withAdvisoryLock(ctx, r.db, poolLockKey("bsc", "USDT"), "test", func(tx *gorm.DB) error {
		var err error
		p, closeAfter, err = r.desk.plan(ctx, tx, canonical(50), specPoolSettings(), nil)
		return err
	})
	if err != nil || len(p) != 1 || p[0].LP.ID != other.ID {
		t.Fatalf("plan = %+v %v, want the other LP", p, err)
	}
	if len(closeAfter) != 1 || closeAfter[0].ID != gone.ID {
		t.Fatalf("cancelled ads handed back = %+v, want the cancelled one", closeAfter)
	}
	r.desk.closeCancelledAds(ctx, closeAfter) // after the lock, on the service's own handle
	var ad models.P2PAd
	r.db.First(&ad, "id = ?", gone.ID)
	if ad.Status != models.P2PAdStatusCancelled {
		t.Fatalf("cancelled voucher's ad is %s, want closed (F23)", ad.Status)
	}
}

// S41: an LP already in the order is never allocated again.
func TestPoolAllocator_ExcludesLPsAlreadyInTheOrder(t *testing.T) {
	r := newDeskRig(t)
	in, _ := r.addLP(t, 100)
	out, _ := r.addLP(t, 100)
	p, _, err := r.desk.plan(context.Background(), r.db, canonical(20), specPoolSettings(), map[uuid.UUID]bool{in.ID: true})
	if err != nil || p[0].LP.ID != out.ID {
		t.Fatalf("plan = %+v %v", p, err)
	}
}

// Review M-15: an undecodable voucher hash fails closed (the ad reads as
// cancelled and is closed), and says so in an alert.
func TestAllocator_UndecodableVoucherAlerts(t *testing.T) {
	r := newDeskRig(t)
	_, ad := r.addLP(t, 100)
	ad.VoucherHash = "0xnot-a-hash"
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if !r.desk.voucherCancelled(context.Background(), *ad) {
		t.Fatal("an undecodable voucher must read as cancelled (fail closed)")
	}
	if !strings.Contains(buf.String(), "ALERT liquidity_pool_voucher_undecodable ad="+ad.ID.String()) {
		t.Fatalf("no alert:\n%s", buf.String())
	}
}
