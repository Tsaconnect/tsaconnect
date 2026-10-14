package services

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// redeliverOpenBudget bounds an operator re-delivery's open (no router
// failover waits on it, so it is not the 8 s PoolOpenCap).
const redeliverOpenBudget = 30 * time.Second

// RedeliverPoolBuy is F6 (plan C2 P12): a pool_slices_short park, every
// earlier slice terminal, priced live for the naira received. owed = V − R
// (R = proven releases); owed ≤ 0 resumes at once; otherwise generation n+1
// opens fresh slices for the owed units only, from LPs not already in the
// order (S41), and the order resumes only when all of them are proven.
//
// A converted order keeps its one conversion record (reference is unique per
// intent): the settle engine hands that same record off after re-delivery, so
// converted custody (recordsNamingDelivery excludes the record itself) never
// sees an older record of this order naming a release.
func (s *ITService) RedeliverPoolBuy(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error) {
	if note = strings.TrimSpace(note); note == "" {
		return nil, ErrITOperatorNoteRequired
	}
	d := s.poolDesk
	if !d.settleWired() || s.treasuryRates == nil {
		return nil, ErrPoolSettleUnwired
	}
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if !isPoolBuy(intent) || intent.Status != models.ITStatusBuyAwaitingRate ||
		!strings.HasPrefix(intent.FailureReason, models.ITFailurePoolSlicesShort) {
		return nil, fmt.Errorf("%w: only a TSA pool order parked %s can be re-delivered", ErrITBadState, models.ITFailurePoolSlicesShort)
	}
	var slices []models.P2PTrade
	if err := s.db.WithContext(ctx).Where("it_intent_id = ?", intentID).Order("created_at").Find(&slices).Error; err != nil {
		return nil, err
	}
	need := s.saleDepth()
	released, gen := new(big.Int), 1
	var releasedUnits []*big.Int // the proven releases: the slices her price is costed on
	exclude := map[uuid.UUID]bool{}
	for i := range slices {
		sl := &slices[i]
		if sl.Generation > gen {
			gen = sl.Generation
		}
		// S41 / §9.4 (review I-6): exclude only LPs holding a live or released
		// slice of the order — a cancelled one, or one resolved back to its LP,
		// leaves that LP eligible again.
		if sl.Status != models.P2PTradeStatusCancelled && !(sl.Status == models.P2PTradeStatusResolved && sl.DroppedAt != nil) {
			var ad models.P2PAd
			if err := s.db.WithContext(ctx).Select("id", "pm_id").First(&ad, "id = ?", sl.AdID).Error; err != nil {
				return nil, err
			}
			if ad.PMID != nil {
				exclude[*ad.PMID] = true
			}
		}
		switch {
		case sliceAtTreasury(sl.Status, sl.DroppedAt, sl.ReleaseTxHash, sl.CancelReason):
			ok, err := d.proveSliceRelease(ctx, sl, need)
			if err != nil || !ok || (sl.Status != models.P2PTradeStatusReleased && sl.Status != models.P2PTradeStatusResolved) {
				return nil, fmt.Errorf("%w: slice %s is not a proven release yet — wait for it", ErrITBadState, sl.ID)
			}
			released.Add(released, d.toCanonical(sl.TokenAmount))
			releasedUnits = append(releasedUnits, d.toCanonical(sl.TokenAmount))
		case sl.Status == models.P2PTradeStatusCancelled, sl.Status == models.P2PTradeStatusResolved:
		case sl.CancelReason == poolF22Returned:
			// Handed back to its LP by an operator: never at the treasury.
		case sl.Status == models.P2PTradeStatusReleased && sl.DroppedAt != nil:
			// Review I-1: dropped, then self-released by its LP — the LP's own
			// USDT (F22's operator exit), never R.
		default:
			return nil, fmt.Errorf("%w: slice %s is still %s — re-deliver once every earlier slice is released or cancelled", ErrITBadState, sl.ID, sl.Status)
		}
	}
	mid, err := s.treasuryRates.BuyRate(ctx, intent.Currency)
	if err != nil {
		return nil, err
	}
	paid, ok := new(big.Int).SetString(intent.ReceivedAmount, 10)
	if !ok || paid.Sign() <= 0 {
		return nil, fmt.Errorf("%w: unusable received amount %q", ErrITBadState, intent.ReceivedAmount)
	}
	// Costed on the slices her LPs are paid for: those already at the
	// treasury, plus the new generation — estimated here as one slice of
	// what is owed, then fitted to the actual allocation under the lock.
	p, err := s.poolQuoteAt(intent.Asset, intent.Currency, paid, mid, CurrentPoolSettings(), PayInCentral, false, releasedUnits)
	if err != nil {
		return nil, err
	}
	if est := new(big.Int).Sub(p.Quote.AssetUnits, released); est.Sign() > 0 {
		if p, err = s.poolQuoteAt(intent.Asset, intent.Currency, paid, mid, CurrentPoolSettings(), PayInCentral, false,
			append(append([]*big.Int(nil), releasedUnits...), est)); err != nil {
			return nil, err
		}
	}
	rec, err := s.ConversionFor(ctx, intent)
	if err != nil {
		return nil, err
	}
	// The C1 T11 deferral (review minor): a converted order's live V never
	// exceeds what its conversion record was opened for (N4).
	if err := s.convertedCeiling(ctx, intent, p.Quote.AssetUnits); err != nil {
		return nil, err
	}
	owed := new(big.Int).Sub(p.Quote.AssetUnits, released)
	meta := map[string]any{"live": p.Quote.AssetUnits.String(), "released": released.String(), "owed": owed.String()}
	seen := poolSliceStates(slices)
	if owed.Sign() <= 0 {
		return s.resumeRedelivered(ctx, adminID, intent, p, nil, seen, note, meta)
	}
	if !d.c.Gates.Open() || !s.poolReleaseGasOK(ctx, p.Settings.SliceCap, true) {
		return nil, ErrPoolClosed
	}
	if err := d.c.Nonces.Check(ctx); err != nil {
		return nil, err
	}
	gen++
	var fitted *PoolPrice
	var written []models.P2PTrade
	var closeAfter []models.P2PAd
	err = withAdvisoryLock(ctx, s.db, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		var locked models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "status").First(&locked, "id = ?", intentID).Error; err != nil {
			return err
		}
		if locked.Status != models.ITStatusBuyAwaitingRate {
			return fmt.Errorf("%w: the order moved", ErrITBadState)
		}
		// The owed units were decided on the slices read above: a concurrent
		// re-delivery (a new generation) or any slice change means that
		// decision is stale — never a second generation for the same owed units.
		if err := slicesUnchanged(tx, intentID, seen, nil); err != nil {
			return err
		}
		plan, cancelled, err := d.plan(ctx, tx, owed, p.Settings, exclude)
		closeAfter = cancelled
		if err != nil {
			return err
		}
		// A converted order's amount may shrink (still under its record) but
		// never grow past it (N4).
		fp, plan, more, err := fitPlanPrice(p, plan, planFit{fixed: releasedUnits, allowWorse: true, allowBetter: rec == nil,
			replan: func(u *big.Int) ([]PoolSlicePlan, []models.P2PAd, error) {
				return d.plan(ctx, tx, u, p.Settings, exclude)
			}})
		closeAfter = append(closeAfter, more...)
		if err != nil {
			return err
		}
		if err := tx.Create(d.snapshot(intentID, gen, fp, plan)).Error; err != nil {
			return err
		}
		written, err = d.writeSlices(tx, intentID, gen, fp, plan)
		if err == nil {
			fitted = fp
		}
		return err
	})
	d.closeCancelledAds(ctx, closeAfter)
	if err != nil {
		return nil, err
	}
	p = fitted
	owed = new(big.Int).Sub(p.Quote.AssetUnits, released)
	meta["live"], meta["owed"] = p.Quote.AssetUnits.String(), owed.String()
	octx, cancel := context.WithTimeout(ctx, redeliverOpenBudget)
	defer cancel()
	opens, _, feeBroken := d.openAndProve(octx, intent, p, written, exclude)
	if feeBroken || !allProven(opens, owed.String(), d) {
		// Review m-3: every slice this re-delivery opened, its re-picks
		// included; a failed one already re-picked was reclaimed (cancelled).
		var ids []uuid.UUID
		for _, o := range opens {
			if !o.repick {
				ids = append(ids, o.slice.ID)
			}
		}
		if dropped, err := d.dropSlices(s.db.WithContext(detached(ctx)), ids, d.now()); err != nil || len(dropped) != len(ids) {
			log.Printf("[liquidity] ALERT liquidity_pool_redeliver_drop_incomplete intent=%s gen=%d asked=%d dropped=%d — check the new slices by hand",
				intentID, gen, len(ids), len(dropped))
		}
		for _, o := range opens {
			if o.failed && !o.repick {
				d.reclaim(ctx, o)
			}
		}
		log.Printf("[liquidity] ALERT liquidity_pool_redeliver_failed intent=%s gen=%d — not resumed; the new slices go to the canceller", intentID, gen)
		return nil, fmt.Errorf("%w (re-delivery)", ErrPoolCannotFill)
	}
	meta["generation"] = gen
	return s.resumeRedelivered(ctx, adminID, intent, p, opens, seen, note, meta)
}

// poolSliceState is what a re-delivery decision read of one slice.
func poolSliceState(sl *models.P2PTrade) string {
	return sl.Status + "|" + sl.ReleaseTxHash + "|" + sl.CancelReason + "|" + fmt.Sprint(sl.DroppedAt != nil)
}

func poolSliceStates(slices []models.P2PTrade) map[uuid.UUID]string {
	out := make(map[uuid.UUID]string, len(slices))
	for i := range slices {
		out[slices[i].ID] = poolSliceState(&slices[i])
	}
	return out
}

// slicesUnchanged refuses when the order's slices differ from what the
// decision read: a slice moved, or a slice exists that is neither one it
// read nor one this re-delivery opened (ours).
func slicesUnchanged(tx *gorm.DB, intentID uuid.UUID, seen map[uuid.UUID]string, ours map[uuid.UUID]bool) error {
	var now []models.P2PTrade
	if err := tx.Where("it_intent_id = ?", intentID).Find(&now).Error; err != nil {
		return err
	}
	found := 0
	for i := range now {
		st, read := seen[now[i].ID]
		switch {
		case read && st == poolSliceState(&now[i]):
			found++
		case read:
			return fmt.Errorf("%w: slice %s moved while the order was re-delivered — try again", ErrITBadState, now[i].ID)
		case !ours[now[i].ID]:
			return fmt.Errorf("%w: the order's slices changed while it was re-delivered — try again", ErrITBadState)
		}
	}
	if found != len(seen) {
		return fmt.Errorf("%w: the order's slices changed while it was re-delivered — try again", ErrITBadState)
	}
	return nil
}

// resumeRedelivered moves the park back to payment_received at the live
// price, stamps the new generation's slices opened, and audits — under the
// pool lock and the intent's row lock, on the same slices the decision read.
func (s *ITService) resumeRedelivered(ctx context.Context, adminID uuid.UUID, intent *models.ITIntent, p *PoolPrice,
	opens []*sliceOpen, seen map[uuid.UUID]string, note string, meta map[string]any) (*models.ITIntent, error) {
	d := s.poolDesk
	err := withAdvisoryLock(ctx, s.db, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		var locked models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "status").First(&locked, "id = ?", intent.ID).Error; err != nil {
			return err
		}
		ours := map[uuid.UUID]bool{}
		for _, o := range opens {
			ours[o.slice.ID] = true
		}
		if err := slicesUnchanged(tx, intent.ID, seen, ours); err != nil {
			return err
		}
		cols := map[string]any{"status": models.ITStatusPaymentReceived, "repriced": true, "failure_reason": ""}
		setQuoteColumns(cols, p.Quote.AssetUnits, p.Quote.RatePerUnitMinor, p.Quote.DisplayRateMinor, p.Quote.Fees)
		res := tx.Model(&models.ITIntent{}).Where("id = ? AND status = ?", intent.ID, models.ITStatusBuyAwaitingRate).Updates(cols)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("%w: the order moved while it was re-delivered", ErrITBadState)
		}
		var proven []uuid.UUID
		for _, o := range opens {
			if o.proven && !o.failed {
				proven = append(proven, o.slice.ID)
			}
		}
		if len(proven) > 0 {
			if err := tx.Model(&models.P2PTrade{}).Where("id IN ? AND opened_at IS NULL", proven).Update("opened_at", time.Now().UTC()).Error; err != nil {
				return err
			}
		}
		return auditIntent(tx, intent.ID, adminID, models.ITAuditPoolRedelivered, note, meta)
	})
	if err != nil {
		return nil, err
	}
	log.Printf("[liquidity] METRIC liquidity_pool_redelivered intent=%s meta=%v", intent.ID, meta)
	return s.getIntent(ctx, intent.ID)
}
