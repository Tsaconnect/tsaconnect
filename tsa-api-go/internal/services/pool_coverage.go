package services

import (
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

// orderSlices is an order's released units (R) and its pending slices, all
// generations, canonical. C1 counts `released` only; plan C2 narrows R to
// releases proven 5 deep and adds F21's resolve-to-treasury.
func (d *PoolDesk) orderSlices(tx *gorm.DB, intentID uuid.UUID) (*big.Int, []PoolPendingSlice, error) {
	var rows []models.P2PTrade
	if err := tx.Where("it_intent_id = ?", intentID).Order("created_at").Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	released := new(big.Int)
	var pending []PoolPendingSlice
	for _, r := range rows {
		units := d.toCanonical(r.TokenAmount)
		switch {
		case r.Status == models.P2PTradeStatusReleased:
			released.Add(released, units)
		case r.Status == models.P2PTradeStatusPendingPayment && r.OpenedAt != nil && r.DroppedAt == nil:
			pending = append(pending, PoolPendingSlice{ID: r.ID, Units: units, CreatedAt: r.CreatedAt})
		}
	}
	return released, pending, nil
}

// dropSlices marks slices as no longer backing their order (plan C1 P1).
func (d *PoolDesk) dropSlices(tx *gorm.DB, ids []uuid.UUID, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	return tx.Model(&models.P2PTrade{}).Where("id IN ? AND dropped_at IS NULL", ids).Update("dropped_at", at).Error
}
