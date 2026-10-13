package services

import (
	"context"
	"log"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// poolSettleBooks is §12.7 at settle (P9), in the settling transaction: the
// current generation's delivered units, nominal take, LP total, the network
// fee her price carried (the treasury's gas for the order, a cost) and time.
func (s *ITService) poolSettleBooks(tx *gorm.DB, intent *models.ITIntent) error {
	var o models.PoolOrder
	if err := tx.Where("intent_id = ?", intent.ID).Order("generation DESC").First(&o).Error; err != nil {
		log.Printf("[liquidity] ALERT liquidity_pool_books_unrecorded intent=%s — no snapshot: %s", intent.ID, RedactAlertText(err.Error()))
		return nil // the delivery stands; the books are an operator's (never roll a paid delivery back over them)
	}
	received, ok := new(big.Int).SetString(intent.ReceivedAmount, 10)
	if !ok {
		received = new(big.Int)
	}
	var slices []models.P2PTrade
	if err := tx.Where("it_intent_id = ?", intent.ID).Find(&slices).Error; err != nil {
		return err
	}
	lp := new(big.Int)
	for _, sl := range slices {
		if sliceAtTreasury(sl.Status, sl.DroppedAt, sl.ReleaseTxHash, sl.CancelReason) {
			k, _ := ngnToKobo(sl.FiatAmount)
			lp.Add(lp, big.NewInt(k))
		}
	}
	take := PoolTakeKobo(received, int64(o.PoolTakeBps))
	now := time.Now().UTC()
	return tx.Model(&models.PoolOrder{}).Where("intent_id = ? AND generation = ? AND settled_at IS NULL", o.IntentID, o.Generation).
		Updates(map[string]any{"delivered_units": intent.AssetAmount, "take_kobo": take.String(), "lp_total_kobo": lp.String(),
			"network_fee_kobo": poolNetworkFeeKobo(intent).String(), "settled_at": now}).Error
}

// poolNetworkFeeKobo is the naira of the network fee line her price carried
// (zero for an order priced before it was a line).
func poolNetworkFeeKobo(intent *models.ITIntent) *big.Int {
	out := new(big.Int)
	for _, f := range StableBuyFees(intent) {
		if f.Kind == FeeKindNetwork {
			if v, ok := new(big.Int).SetString(f.NgnMinor, 10); ok {
				out.Add(out, v)
			}
		}
	}
	return out
}

// CompletePoolBooks is the books' second half (S47): once every forward of a
// settled order's at-treasury slices is settled with its actual fee, write
// payout_fee_kobo and keep_kobo, once. It also back-fills missing fees and
// tells LPs about delayed payouts. Returns how many orders it completed.
func (s *ITService) CompletePoolBooks(ctx context.Context) (int, error) {
	s.notePoolPayoutsDelayed(ctx)
	s.backfillPoolForwards(ctx)
	var open []models.PoolOrder
	if err := s.db.WithContext(ctx).Where("settled_at IS NOT NULL AND keep_kobo IS NULL").
		Order("settled_at ASC").Limit(200).Find(&open).Error; err != nil {
		return 0, err
	}
	done := 0
	for i := range open {
		ok, err := s.completeOnePoolBook(ctx, &open[i])
		if err != nil {
			log.Printf("[liquidity] books intent=%s: %s", open[i].IntentID, RedactAlertText(err.Error()))
		}
		if ok {
			done++
		}
	}
	return done, nil
}

// poolForwardMissingAfter: a slice at the treasury with no forward this long
// is an LP nobody is paying (review I-3).
const poolForwardMissingAfter = 30 * time.Minute

// backfillPoolForwards (review I-3): every proven slice of an order that holds
// her naira — delivering, parked or already settled — gets its forward, even
// if the settle tick that proved it failed to write one; and any at-treasury
// slice still without a forward after poolForwardMissingAfter alerts (dropped
// ones included, so nothing a refusal left behind is silent).
func (s *ITService) backfillPoolForwards(ctx context.Context) {
	d := s.poolDesk
	if !d.settleWired() {
		return
	}
	var rows []models.P2PTrade
	if err := s.db.WithContext(ctx).Table("p2p_trades").Select("p2p_trades.*").
		Joins("JOIN it_intents ON it_intents.id = p2p_trades.it_intent_id").
		Where("p2p_trades.it_intent_id IS NOT NULL AND COALESCE(p2p_trades.cancel_reason, '') <> ? AND NOT EXISTS (SELECT 1 FROM p2p_settlement_forwards f WHERE f.trade_id = p2p_trades.id)", poolF22Returned).
		Where("p2p_trades.status IN ? AND it_intents.status IN ?",
			[]string{models.P2PTradeStatusReleased, models.P2PTradeStatusResolved},
			[]string{models.ITStatusPaymentReceived, models.ITStatusDelivering, models.ITStatusSettled, models.ITStatusBuyAwaitingRate}).
		Order("p2p_trades.updated_at ASC").Limit(100).Find(&rows).Error; err != nil {
		log.Printf("[liquidity] back-fill read: %s", RedactAlertText(err.Error()))
		return
	}
	need := s.saleDepth()
	for i := range rows {
		sl := &rows[i]
		if sl.DroppedAt == nil {
			if ok, _ := d.proveSliceRelease(ctx, sl, need); ok {
				if _, err := s.forwardSlice(ctx, d, sl, poolForwardRef(sl.ID), false); err == nil {
					continue
				}
			}
		}
		if time.Since(sl.UpdatedAt) > poolForwardMissingAfter &&
			claimRunAt(ctx, s.db, "pool_forward_missing:"+sl.ID.String(), time.Hour, time.Now()) {
			log.Printf("[liquidity] ALERT liquidity_pool_forward_missing slice=%s intent=%s dropped=%v — the LP's USDT is at the treasury and no naira was sent",
				sl.ID, *sl.ITIntentID, sl.DroppedAt != nil)
		}
	}
}

func (s *ITService) completeOnePoolBook(ctx context.Context, o *models.PoolOrder) (bool, error) {
	var slices []models.P2PTrade
	if err := s.db.WithContext(ctx).Where("it_intent_id = ?", o.IntentID).Find(&slices).Error; err != nil {
		return false, err
	}
	var ids []uuid.UUID
	for _, sl := range slices {
		if sliceAtTreasury(sl.Status, sl.DroppedAt, sl.ReleaseTxHash, sl.CancelReason) {
			ids = append(ids, sl.ID)
		}
	}
	var fws []models.P2PSettlementForward
	if err := s.db.WithContext(ctx).Where("trade_id IN ?", ids).Find(&fws).Error; err != nil {
		return false, err
	}
	if len(fws) != len(ids) {
		return false, nil
	}
	fees := new(big.Int)
	for i := range fws {
		if fws[i].Status != models.P2PForwardSettled {
			return false, nil
		}
		if fws[i].FeeKobo == nil && s.poolForwards != nil && s.poolForwards.forwarder != nil {
			_ = s.poolForwards.forwarder.RecordFee(ctx, fws[i].ID)
			s.db.WithContext(ctx).First(&fws[i], "id = ?", fws[i].ID)
		}
		if fws[i].FeeKobo == nil {
			if fws[i].SettledAt != nil && time.Since(*fws[i].SettledAt) > time.Hour &&
				claimRunAt(ctx, s.db, "pool_fee_unread:"+fws[i].ID.String(), 24*time.Hour, time.Now()) {
				log.Printf("[liquidity] ALERT liquidity_pool_payout_fee_unread forward=%s intent=%s — keep not booked (Task 17 probes the fee shape)", fws[i].ID, o.IntentID)
			}
			return false, nil
		}
		f, _ := new(big.Int).SetString(*fws[i].FeeKobo, 10)
		fees.Add(fees, f)
	}
	var intent models.ITIntent
	if err := s.db.WithContext(ctx).Select("id", "received_amount").First(&intent, "id = ?", o.IntentID).Error; err != nil {
		return false, err
	}
	received, _ := new(big.Int).SetString(intent.ReceivedAmount, 10)
	var funding int64
	s.db.WithContext(ctx).Model(&models.InboundCredit{}).Where("matched_claim_id = ? AND status = ?", o.IntentID, models.InboundCreditMatched).
		Select("COALESCE(SUM(fee_kobo), 0)").Scan(&funding)
	fund := big.NewInt(funding)
	if funding == 0 {
		fund = new(big.Int).Quo(new(big.Int).Mul(received, big.NewInt(int64(o.FundingBps))), big.NewInt(10_000))
		log.Printf("[liquidity] METRIC liquidity_pool_funding_estimated intent=%s bps=%d kobo=%s", o.IntentID, o.FundingBps, fund)
	}
	lp, _ := new(big.Int).SetString(derefOr(o.LPTotalKobo, "0"), 10)
	network, _ := new(big.Int).SetString(derefOr(o.NetworkFeeKobo, "0"), 10)
	keep := new(big.Int).Sub(received, fund)
	keep.Sub(keep, lp).Sub(keep, fees).Sub(keep, network) // the gas the treasury spent is a cost, never kept
	res := s.db.WithContext(ctx).Model(&models.PoolOrder{}).
		Where("intent_id = ? AND generation = ? AND keep_kobo IS NULL", o.IntentID, o.Generation).
		Updates(map[string]any{"payout_fee_kobo": fees.String(), "keep_kobo": keep.String()})
	if res.Error == nil && res.RowsAffected == 1 {
		log.Printf("[liquidity] METRIC liquidity_pool_books intent=%s keep_kobo=%s lp_kobo=%s fees_kobo=%s funding_kobo=%s", o.IntentID, keep, lp, fees, fund)
	}
	return res.RowsAffected == 1, res.Error
}

func derefOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}
