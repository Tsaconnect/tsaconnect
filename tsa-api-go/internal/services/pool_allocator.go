package services

import (
	"context"
	"errors"
	"log"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ErrPoolCannotFill: no allocation of free stock covers the order within the
// slice cap (§9.3). Router failover; copy "That amount can't be filled right
// now. Try a smaller amount." (§19).
var ErrPoolCannotFill = errors.New("liquidity pool: that amount cannot be filled right now")

// poolCandidate is one eligible LP with its free stock (canonical) and weight.
type poolCandidate struct {
	lp       models.PaymentMerchant
	ad       models.P2PAd
	free     *big.Int
	deficit  *big.Rat
	lastFill *time.Time
}

// PoolSlicePlan is one slice of an allocation: an LP, its stock ad, and the
// canonical units the slice locks.
type PoolSlicePlan struct {
	LP    models.PaymentMerchant
	Ad    models.P2PAd
	Units *big.Int
}

// deficitFirst orders candidates for a whole order (§9.3 step 2): the largest
// deficit, then the earliest last fill (never-filled first), then the lowest id.
func deficitFirst(a, b poolCandidate) bool {
	if c := a.deficit.Cmp(b.deficit); c != 0 {
		return c > 0
	}
	switch {
	case a.lastFill == nil && b.lastFill != nil:
		return true
	case a.lastFill != nil && b.lastFill == nil:
		return false
	case a.lastFill != nil && !a.lastFill.Equal(*b.lastFill):
		return a.lastFill.Before(*b.lastFill)
	}
	return a.lp.ID.String() < b.lp.ID.String()
}

// allocatePool is §9.3 (S13), pure: the whole order to one LP when any covers
// it, else the fewest slices largest-free-first, never leaving a tail below
// minSlice, within sliceCap.
func allocatePool(cands []poolCandidate, units *big.Int, sliceCap int, minSlice *big.Int) ([]PoolSlicePlan, error) {
	if len(cands) == 0 || sliceCap < 1 || units.Cmp(minSlice) < 0 {
		return nil, ErrPoolCannotFill
	}
	var whole []poolCandidate
	for _, c := range cands {
		if c.free.Cmp(units) >= 0 {
			whole = append(whole, c)
		}
	}
	if len(whole) > 0 {
		sort.SliceStable(whole, func(i, j int) bool { return deficitFirst(whole[i], whole[j]) })
		return []PoolSlicePlan{{LP: whole[0].lp, Ad: whole[0].ad, Units: new(big.Int).Set(units)}}, nil
	}
	sorted := append([]poolCandidate(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if c := sorted[i].free.Cmp(sorted[j].free); c != 0 {
			return c > 0
		}
		return deficitFirst(sorted[i], sorted[j])
	})
	rem := new(big.Int).Set(units)
	var out []PoolSlicePlan
	for _, c := range sorted {
		if len(out) == sliceCap {
			break
		}
		take := new(big.Int).Set(c.free)
		if take.Cmp(rem) > 0 {
			take.Set(rem)
		}
		if tail := new(big.Int).Sub(rem, take); tail.Sign() > 0 && tail.Cmp(minSlice) < 0 {
			take.Sub(rem, minSlice)
			if take.Cmp(minSlice) < 0 {
				continue
			}
		}
		out = append(out, PoolSlicePlan{LP: c.lp, Ad: c.ad, Units: take})
		rem.Sub(rem, take)
		if rem.Sign() == 0 {
			break
		}
	}
	if rem.Sign() > 0 {
		return nil, ErrPoolCannotFill
	}
	return out, nil
}

// sumTopN is the free stock of the n largest candidates.
func sumTopN(cs []poolCandidate, n int) *big.Int {
	frees := make([]*big.Int, len(cs))
	for i := range cs {
		frees[i] = cs[i].free
	}
	sort.Slice(frees, func(i, j int) bool { return frees[i].Cmp(frees[j]) > 0 })
	sum := new(big.Int)
	for i := 0; i < len(frees) && i < n; i++ {
		sum.Add(sum, frees[i])
	}
	return sum
}

// lpUnits is a per-LP canonical sum read from slices.
type lpUnits struct {
	PMID        uuid.UUID
	TokenAmount string
	ReleasedAt  *time.Time
}

func (d *PoolDesk) sliceSums(ctx context.Context, db *gorm.DB, where string, args ...any) ([]lpUnits, error) {
	var rows []lpUnits
	err := db.WithContext(ctx).Table("p2p_trades").
		Select("p2p_ads.pm_id AS pm_id, p2p_trades.token_amount AS token_amount, p2p_trades.released_at AS released_at").
		Joins("JOIN p2p_ads ON p2p_ads.id = p2p_trades.ad_id").
		Where("p2p_trades.it_intent_id IS NOT NULL AND p2p_trades.chain_id = ?", d.c.Token.ChainID).
		Where(where, args...).Scan(&rows).Error
	return rows, err
}

func (d *PoolDesk) toCanonical(chainUnits string) *big.Int {
	v, ok := new(big.Int).SetString(strings.TrimSpace(chainUnits), 10)
	if !ok {
		return new(big.Int)
	}
	c, err := ChainToCanonicalUnits(v, d.c.Token.Decimals)
	if err != nil {
		return new(big.Int)
	}
	return c
}

// candidates is the eligible set E with free stock and weights (§9.1, §9.3
// step 1). exclude drops LPs (already in the order, S41); only, when non-nil,
// restricts the set (the fresh pass over the chosen LPs, M11). fresh reads
// balances from the chain instead of the 30 s cache.
func (d *PoolDesk) candidates(ctx context.Context, db *gorm.DB, set PoolSettings, exclude, only map[uuid.UUID]bool, fresh bool) ([]poolCandidate, error) {
	now := d.now()
	var lps []models.PaymentMerchant
	if err := db.WithContext(ctx).Where("kind = ? AND status = ? AND pool_chain_id = ?",
		models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, d.c.Token.ChainID).Find(&lps).Error; err != nil {
		return nil, err
	}
	unconf, err := d.sliceSums(ctx, db, "p2p_trades.status = ? AND p2p_trades.opened_at IS NULL AND p2p_trades.created_at > ?",
		models.P2PTradeStatusPendingPayment, now.Add(-poolUnconfirmedWindow))
	if err != nil {
		return nil, err
	}
	released, err := d.sliceSums(ctx, db, "p2p_trades.status = ?", models.P2PTradeStatusReleased)
	if err != nil {
		return nil, err
	}
	unconfirmed := map[uuid.UUID]*big.Int{}
	for _, u := range unconf {
		if unconfirmed[u.PMID] == nil {
			unconfirmed[u.PMID] = new(big.Int)
		}
		unconfirmed[u.PMID].Add(unconfirmed[u.PMID], d.toCanonical(u.TokenAmount))
	}
	fills, lastFill, totalFills := map[uuid.UUID]*big.Int{}, map[uuid.UUID]time.Time{}, new(big.Int)
	for _, r := range released {
		if r.ReleasedAt == nil {
			continue
		}
		if r.ReleasedAt.After(lastFill[r.PMID]) {
			lastFill[r.PMID] = *r.ReleasedAt
		}
		if r.ReleasedAt.After(now.Add(-poolFillWindow)) {
			v := d.toCanonical(r.TokenAmount)
			if fills[r.PMID] == nil {
				fills[r.PMID] = new(big.Int)
			}
			fills[r.PMID].Add(fills[r.PMID], v)
			totalFills.Add(totalFills, v)
		}
	}
	// Phase 1, database only, on the caller's handle (review C2: inside the
	// pool lock that is the lock's transaction, never s.db).
	type eligible struct {
		lp models.PaymentMerchant
		ad *models.P2PAd
	}
	var elig []eligible
	for i := range lps {
		lp := lps[i]
		if exclude[lp.ID] || (only != nil && !only[lp.ID]) {
			continue
		}
		ad, err := d.c.Trades.latestStockAdIn(ctx, db, &lp, d.c.Token.ChainID, d.c.Token.Symbol)
		if err != nil {
			return nil, err // a failed read inside the lock is the transaction's failure, not a skipped LP
		}
		if ad == nil || ad.Status != models.P2PAdStatusActive || ad.VoucherSignature == "" ||
			ad.VoucherDeadline == nil || !ad.VoucherDeadline.After(now.Add(poolVoucherMargin)) {
			continue // Plan B M10: the latest ad, active, signed, not expiring
		}
		elig = append(elig, eligible{lp: lp, ad: ad})
	}
	// Phase 2, chain only. A fresh pass reads every chosen LP at once under ONE
	// 3 s deadline (review M8): the pool lock is held, so serial reads would
	// cost 3 s per LP against the 8 s open cap.
	positions := make([]*LPPosition, len(elig))
	rctx, cancel := ctx, context.CancelFunc(func() {})
	if fresh {
		rctx, cancel = context.WithTimeout(ctx, poolFreshReadTimeout)
	}
	var wg sync.WaitGroup
	for i := range elig {
		read := func(i int) {
			pos, err := d.c.Stock.Position(rctx, &elig[i].lp, fresh)
			if err != nil {
				log.Printf("[liquidity] lp=%s balance unreadable, skipped: %s", elig[i].lp.ID, RedactAlertText(err.Error()))
				return
			}
			positions[i] = pos
		}
		if !fresh {
			read(i) // the 30 s cache: no chain round trip to overlap
			continue
		}
		wg.Add(1)
		go func(i int) { defer wg.Done(); read(i) }(i)
	}
	wg.Wait()
	cancel()
	var out []poolCandidate
	for i, e := range elig {
		lp, ad, pos := e.lp, e.ad, positions[i]
		if pos == nil {
			continue
		}
		avail, locked, stake := d.toCanonical(pos.Available.String()), d.toCanonical(pos.Locked.String()), d.toCanonical(pos.Stake.String())
		unc := unconfirmed[lp.ID]
		if unc == nil {
			unc = new(big.Int)
		}
		free := new(big.Int).Sub(avail, unc)
		if v := new(big.Int).Sub(new(big.Int).Sub(stake, locked), unc); v.Cmp(free) < 0 {
			free = v
		}
		if r := d.toCanonical(ad.Remaining); r.Cmp(free) < 0 {
			free = r
		}
		if free.Cmp(set.MinSliceUnits) < 0 {
			continue
		}
		c := poolCandidate{lp: lp, ad: *ad, free: free}
		if t, ok := lastFill[lp.ID]; ok {
			c.lastFill = &t
		}
		out = append(out, c)
	}
	total := new(big.Int)
	for _, c := range out {
		total.Add(total, c.free)
	}
	for i := range out {
		stock := new(big.Rat).SetFrac(out[i].free, total)
		fill := new(big.Rat)
		if totalFills.Sign() > 0 && fills[out[i].lp.ID] != nil {
			fill.SetFrac(fills[out[i].lp.ID], totalFills)
		}
		out[i].deficit = stock.Sub(stock, fill)
	}
	return out, nil
}

// plan is §9.1's allocation order (M11): allocate on cached stock, read the
// chosen LPs fresh (balance and voucher, all at once under one 3 s deadline),
// re-allocate once on the fresh values, else ErrPoolCannotFill. Every read
// runs on db, the lock's transaction (review C2). A voucher cancelled on-chain
// is never allocated and is handed back so the caller closes its ad after the
// lock commits (F23): closeStockAd writes on s.db, which must not run inside
// the lock's transaction.
func (d *PoolDesk) plan(ctx context.Context, db *gorm.DB, units *big.Int, set PoolSettings, exclude map[uuid.UUID]bool) ([]PoolSlicePlan, []models.P2PAd, error) {
	cached, err := d.candidates(ctx, db, set, exclude, nil, false)
	if err != nil {
		return nil, nil, err
	}
	first, err := allocatePool(cached, units, set.SliceCap, set.MinSliceUnits)
	if err != nil {
		return nil, nil, err
	}
	chosen := map[uuid.UUID]bool{}
	cancelledCh := make(chan uuid.UUID, len(first))
	var vwg sync.WaitGroup
	for _, p := range first {
		chosen[p.LP.ID] = true
		vwg.Add(1)
		go func(lp uuid.UUID, ad models.P2PAd) { // overlaps the fresh balance reads (M8)
			defer vwg.Done()
			if d.voucherCancelled(ctx, ad) {
				cancelledCh <- lp
			}
		}(p.LP.ID, p.Ad)
	}
	fresh, err := d.candidates(ctx, db, set, exclude, chosen, true)
	vwg.Wait()
	close(cancelledCh)
	if err != nil {
		return nil, nil, err
	}
	voided := map[uuid.UUID]bool{}
	for lp := range cancelledCh {
		voided[lp] = true
	}
	var closeAfter []models.P2PAd
	freshFree := map[uuid.UUID]*big.Int{}
	for _, c := range fresh {
		if voided[c.lp.ID] {
			log.Printf("[liquidity] ALERT liquidity_pool_voucher_cancelled lp=%s ad=%s", c.lp.ID, c.ad.ID)
			closeAfter = append(closeAfter, c.ad)
			continue
		}
		freshFree[c.lp.ID] = c.free
	}
	ok := true
	for _, p := range first {
		if f := freshFree[p.LP.ID]; f == nil || f.Cmp(p.Units) < 0 {
			ok = false
		}
	}
	if ok {
		return first, closeAfter, nil
	}
	// One re-allocation: the chosen LPs at their fresh values, the rest cached.
	var again []poolCandidate
	for _, c := range cached {
		if !chosen[c.lp.ID] {
			again = append(again, c)
		}
	}
	for _, c := range fresh {
		if f := freshFree[c.lp.ID]; f != nil {
			c.free = f
			again = append(again, c)
		}
	}
	plans, err := allocatePool(again, units, set.SliceCap, set.MinSliceUnits)
	return plans, closeAfter, err
}

// closeCancelledAds closes stock ads whose voucher plan found cancelled (F23).
// Called after the pool lock's transaction has committed, never inside it.
func (d *PoolDesk) closeCancelledAds(ctx context.Context, ads []models.P2PAd) {
	for i := range ads {
		if _, err := d.c.Trades.closeStockAd(ctx, &ads[i], "voucher_cancelled"); err != nil {
			log.Printf("[liquidity] ad=%s cancelled voucher not closed: %s", ads[i].ID, RedactAlertText(err.Error()))
		}
	}
}

// voucherCancelled reads the ad's voucher on-chain within 3 s; an unreadable
// read is treated as not cancelled (the open's prepare reverts on a cancelled
// voucher anyway, F23).
func (d *PoolDesk) voucherCancelled(ctx context.Context, ad models.P2PAd) bool {
	h, err := decodeHex32(ad.VoucherHash)
	if err != nil {
		// Fails closed — the ad is closed as if cancelled — but never silently
		// (review M-15): a stored hash that does not decode is a data defect.
		log.Printf("[liquidity] ALERT liquidity_pool_voucher_undecodable ad=%s — treated as cancelled; the ad will be closed", ad.ID)
		return true
	}
	type res struct{ cancelled bool }
	ch := make(chan res, 1)
	go func() { c, _ := d.c.Escrow.ReadVoucherCancelled(h); ch <- res{c} }()
	rctx, cancel := context.WithTimeout(ctx, poolFreshReadTimeout)
	defer cancel()
	select {
	case r := <-ch:
		return r.cancelled
	case <-rctx.Done():
		return false
	}
}
