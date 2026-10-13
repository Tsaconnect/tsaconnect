package services

import (
	"fmt"
	"log"
	"math/big"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// PoolPendingSlice is a slice that still backs its order: proven on-chain,
// pending, not dropped. Units are canonical.
type PoolPendingSlice struct {
	ID        uuid.UUID
	Units     *big.Int
	CreatedAt time.Time
}

// PoolCoverage is S18's answer for money priced live.
type PoolCoverage struct {
	Covered bool
	Owed    *big.Int    // live − released
	Keep    []uuid.UUID // the pending slices to release
	Drop    []uuid.UUID // the pending slices the order no longer needs
	Surplus *big.Int    // released + kept − live (≥ 0 when covered)
}

// EvaluatePoolCoverage is S18 (spec §12.3, review C1/I6), pure. Owed = live −
// released. Owed ≤ 0: nothing more is released. Otherwise the minimal subset of
// pending slices (fewest, then the smallest total, then the earliest) covering
// owed, kept only if its forced surplus ≤ max(minSlice, 2% of the subset).
// Plan C2's watcher re-runs it when a slice is cancelled under a paid order.
func EvaluatePoolCoverage(live, released *big.Int, pending []PoolPendingSlice, minSlice *big.Int) PoolCoverage {
	owed := new(big.Int).Sub(live, released)
	all := make([]uuid.UUID, len(pending))
	for i := range pending {
		all[i] = pending[i].ID
	}
	if owed.Sign() <= 0 {
		return PoolCoverage{Covered: true, Owed: owed, Drop: all, Surplus: new(big.Int).Neg(owed)}
	}
	idx, total, ok := minimalCoveringSubset(pending, owed)
	if !ok {
		return PoolCoverage{Owed: owed, Drop: all}
	}
	surplus := new(big.Int).Sub(total, owed)
	bound := new(big.Int).Quo(new(big.Int).Mul(total, big.NewInt(2)), big.NewInt(100))
	if minSlice.Cmp(bound) > 0 {
		bound = minSlice
	}
	if surplus.Cmp(bound) > 0 {
		return PoolCoverage{Owed: owed, Drop: all}
	}
	keep := map[int]bool{}
	cov := PoolCoverage{Covered: true, Owed: owed, Surplus: surplus}
	for _, i := range idx {
		keep[i] = true
		cov.Keep = append(cov.Keep, pending[i].ID)
	}
	for i := range pending {
		if !keep[i] {
			cov.Drop = append(cov.Drop, pending[i].ID)
		}
	}
	return cov
}

// minimalCoveringSubset is exhaustive over subsets: an order has at most a
// few slices per generation (pool_slice_cap ≤ 5), so this is tiny.
func minimalCoveringSubset(p []PoolPendingSlice, owed *big.Int) ([]int, *big.Int, bool) {
	order := make([]int, len(p))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return p[order[a]].CreatedAt.Before(p[order[b]].CreatedAt) })
	var best []int
	var bestTotal *big.Int
	n := len(p)
	if n > 15 {
		n = 15 // never reached: a guard against an unbounded loop
	}
	for mask := 1; mask < 1<<n; mask++ {
		var idx []int
		total := new(big.Int)
		for bit := 0; bit < n; bit++ {
			if mask&(1<<bit) != 0 {
				idx = append(idx, order[bit])
				total.Add(total, p[order[bit]].Units)
			}
		}
		if total.Cmp(owed) < 0 {
			continue
		}
		if best == nil || len(idx) < len(best) || (len(idx) == len(best) && total.Cmp(bestTotal) < 0) {
			best, bestTotal = idx, total
		}
	}
	return best, bestTotal, best != nil
}

// F22 markers on a slice's cancel_reason (review I-5): one exit per slice,
// taken under the slice's row lock by a CAS from the empty string.
const (
	poolF22Paid     = "pool_f22:paid"
	poolF22Returned = "pool_f22:returned"
)

// sliceAtTreasury (plan C2 P1) is the ONE answer to "is this slice's USDT
// at the treasury for its order": R for S18 (claim, re-evaluation,
// re-delivery), the S37 refund's "zero slices" test, converted custody and
// the settle total. True for releasing, a broadcast release not yet resolved,
// and released or disputed/resolved while the slice still backs its order
// (F21 drops a slice it sends back to the LP, so a non-dropped dispute of a
// paid order goes to the treasury). Proof is separate (proveSliceRelease).
//
// A dropped slice its LP then self-released (F22, review I-1) is the LP's
// own USDT that happens to sit at the treasury: never hers, never R — its LP
// is paid or handed it back by an operator (F22). A dropped slice is never
// releasing: AutoRelease's flip needs dropped_at NULL and a drop refuses a
// releasing slice.
func sliceAtTreasury(status string, droppedAt *time.Time, releaseTxHash, cancelReason string) bool {
	if cancelReason == poolF22Returned {
		return false // re-review I-B: an operator handed this slice's USDT back to its LP
	}
	switch status {
	case models.P2PTradeStatusReleasing:
		return true
	case models.P2PTradeStatusReleased, models.P2PTradeStatusDisputed, models.P2PTradeStatusResolved:
		return droppedAt == nil
	case models.P2PTradeStatusPendingPayment, models.P2PTradeStatusPaid:
		// Review C-1: a recorded release hash means it was broadcast and is not
		// proven gone (Task 1 clears the hash when it is): it may land.
		return releaseTxHash != ""
	}
	return false
}

// orderSlices is an order's at-treasury units (R) and its pending slices,
// all generations, canonical (plan C2 P1: R counts releasing and F21's
// resolve-to-treasury too).
func (d *PoolDesk) orderSlices(tx *gorm.DB, intentID uuid.UUID) (*big.Int, []PoolPendingSlice, error) {
	sizes, pending, err := d.orderSliceUnits(tx, intentID)
	if err != nil {
		return nil, nil, err
	}
	released := new(big.Int)
	for _, u := range sizes {
		released.Add(released, u)
	}
	return released, pending, nil
}

// orderSliceUnits is orderSlices with each at-treasury slice's size: the
// slices an order's LPs are paid for, which a live re-price costs.
func (d *PoolDesk) orderSliceUnits(tx *gorm.DB, intentID uuid.UUID) ([]*big.Int, []PoolPendingSlice, error) {
	var rows []models.P2PTrade
	if err := tx.Where("it_intent_id = ?", intentID).Order("created_at").Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	var released []*big.Int
	var pending []PoolPendingSlice
	for _, r := range rows {
		units := d.toCanonical(r.TokenAmount)
		switch {
		case sliceAtTreasury(r.Status, r.DroppedAt, r.ReleaseTxHash, r.CancelReason):
			released = append(released, units)
		case (r.Status == models.P2PTradeStatusPendingPayment || r.Status == models.P2PTradeStatusPaid) && r.OpenedAt != nil && r.DroppedAt == nil:
			pending = append(pending, PoolPendingSlice{ID: r.ID, Units: units, CreatedAt: r.CreatedAt})
		}
	}
	return released, pending, nil
}

// errPoolSlicesMoved: a slice the caller decided to drop was released (or
// left its live state) between the caller's read and the drop. The decision
// was taken on a stale view, so the caller's transaction is rolled back and
// the order is decided again on the next pass.
var errPoolSlicesMoved = fmt.Errorf("%w: a slice of the order moved while it was decided (a release was broadcast) — decide again", ErrITBadState)

// dropSlices marks live slices as no longer backing their order (plan C1
// P1) and returns the ids it actually dropped. A slice whose release was
// broadcast (releasing, or a recorded hash) is at the treasury and is never
// dropped (plan C2 P1, review C-1), whatever a caller read before
// AutoRelease's flip landed — so a caller must compare what was dropped with
// what it asked for (dropAllSlices).
func (d *PoolDesk) dropSlices(tx *gorm.DB, ids []uuid.UUID, at time.Time) ([]uuid.UUID, error) {
	var dropped []uuid.UUID
	for _, id := range ids {
		res := tx.Model(&models.P2PTrade{}).
			Where("id = ? AND dropped_at IS NULL AND release_tx_hash = '' AND status IN ?", id,
				[]string{models.P2PTradeStatusPendingPayment, models.P2PTradeStatusPaid}).
			Update("dropped_at", at)
		if res.Error != nil {
			return dropped, res.Error
		}
		if res.RowsAffected == 1 {
			dropped = append(dropped, id)
		}
	}
	return dropped, nil
}

// dropAllSlices drops every id or refuses with errPoolSlicesMoved: a
// decision (park, cover, refund) that read a slice as live must never stand
// once that slice turned out to be releasing — the caller's transaction rolls
// back and the order is decided again.
func (d *PoolDesk) dropAllSlices(tx *gorm.DB, ids []uuid.UUID, at time.Time) error {
	dropped, err := d.dropSlices(tx, ids, at)
	if err != nil {
		return err
	}
	if len(dropped) != len(ids) {
		log.Printf("[liquidity] ALERT liquidity_pool_drop_raced asked=%d dropped=%d — a slice moved under the decision; not applied", len(ids), len(dropped))
		return errPoolSlicesMoved
	}
	return nil
}

// poolSettleCostRounds bounds poolSettlePrice's search; an order has at most
// pool_slice_cap pending slices, so a consistent keep set is found in one or
// two rounds.
const poolSettleCostRounds = 4

// poolSettlePrice is S18's live price for money that arrived outside the
// honour window, costed only on the slices that will be paid to an LP: the
// ones already at the treasury and the pending ones coverage keeps (never a
// dropped, cancelled or F22-returned slice). Coverage picks the keep set from
// the price and the price depends on the keep set, so it starts from every
// pending slice (the dearest price, the smallest amount owed) and re-prices
// on the kept set until coverage keeps exactly the set it was priced on. An
// uncovered answer, or no consistent set within poolSettleCostRounds, is
// returned as not covered: the caller parks pool_slices_short for an operator.
func (s *ITService) poolSettlePrice(asset, currency string, paid *big.Int, mid *big.Rat, ch PayInChannel,
	released []*big.Int, pending []PoolPendingSlice) (*PoolPrice, PoolCoverage, error) {
	releasedSum := sumUnits(released)
	kept := map[uuid.UUID]bool{}
	for _, sl := range pending {
		kept[sl.ID] = true
	}
	var p *PoolPrice
	var cov PoolCoverage
	for round := 0; round < poolSettleCostRounds; round++ {
		sizes := append([]*big.Int(nil), released...)
		for _, sl := range pending {
			if kept[sl.ID] {
				sizes = append(sizes, sl.Units)
			}
		}
		var err error
		if p, err = s.poolQuoteAt(asset, currency, paid, mid, CurrentPoolSettings(), ch, false, sizes); err != nil {
			return nil, PoolCoverage{}, err
		}
		cov = EvaluatePoolCoverage(p.Quote.AssetUnits, releasedSum, pending, p.Settings.MinSliceUnits)
		if !cov.Covered {
			return p, cov, nil
		}
		next := map[uuid.UUID]bool{}
		for _, id := range cov.Keep {
			next[id] = true
		}
		if sameIDSet(kept, next) {
			return p, cov, nil
		}
		kept = next
	}
	log.Printf("[liquidity] ALERT liquidity_pool_settle_costs_unsettled owed=%s — no keep set prices consistently; parked for an operator", cov.Owed)
	return p, PoolCoverage{Owed: cov.Owed, Drop: allPendingIDs(pending)}, nil
}

func sameIDSet(a, b map[uuid.UUID]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if !b[id] {
			return false
		}
	}
	return true
}

func allPendingIDs(pending []PoolPendingSlice) []uuid.UUID {
	out := make([]uuid.UUID, len(pending))
	for i := range pending {
		out[i] = pending[i].ID
	}
	return out
}

func sumUnits(units []*big.Int) *big.Int {
	out := new(big.Int)
	for _, u := range units {
		out.Add(out, u)
	}
	return out
}
