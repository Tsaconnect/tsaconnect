package services

import (
	"context"
	"log"
	"math/big"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// poolSettleBooks writes a pool order's books at settle, in the settling
// transaction: delivered units, the nominal take, the network fee her price
// carried (the gas spent, a cost) and the time. LPs are paid their spread in
// the vault itself (fill_spread_units), so no naira goes to them here.
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
	take := PoolTakeKobo(received, int64(o.PoolTakeBps))
	now := time.Now().UTC()
	return tx.Model(&models.PoolOrder{}).Where("intent_id = ? AND generation = ? AND settled_at IS NULL", o.IntentID, o.Generation).
		Updates(map[string]any{"delivered_units": intent.AssetAmount, "take_kobo": take.String(), "lp_total_kobo": "0",
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

// CompletePoolBooks writes each settled order's keep once its funding fee is
// known: the naira received, less the central account's funding fee and the
// network fee. That naira is what backs the PM's refill of the fill. Returns
// how many orders it completed.
func (s *ITService) CompletePoolBooks(ctx context.Context) (int, error) {
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

func (s *ITService) completeOnePoolBook(ctx context.Context, o *models.PoolOrder) (bool, error) {
	var intent models.ITIntent
	if err := s.db.WithContext(ctx).Select("id", "received_amount").First(&intent, "id = ?", o.IntentID).Error; err != nil {
		return false, err
	}
	received, _ := new(big.Int).SetString(intent.ReceivedAmount, 10)
	if received == nil {
		received = new(big.Int)
	}
	var funding int64
	s.db.WithContext(ctx).Model(&models.InboundCredit{}).Where("matched_claim_id = ? AND status = ?", o.IntentID, models.InboundCreditMatched).
		Select("COALESCE(SUM(fee_kobo), 0)").Scan(&funding)
	fund := big.NewInt(funding)
	if funding == 0 {
		fund = new(big.Int).Quo(new(big.Int).Mul(received, big.NewInt(int64(o.FundingBps))), big.NewInt(10_000))
		log.Printf("[liquidity] METRIC liquidity_pool_funding_estimated intent=%s bps=%d kobo=%s", o.IntentID, o.FundingBps, fund)
	}
	network, _ := new(big.Int).SetString(derefOr(o.NetworkFeeKobo, "0"), 10)
	keep := new(big.Int).Sub(received, fund)
	keep.Sub(keep, network) // the gas the treasury and operator spent is a cost, never kept
	res := s.db.WithContext(ctx).Model(&models.PoolOrder{}).
		Where("intent_id = ? AND generation = ? AND keep_kobo IS NULL", o.IntentID, o.Generation).
		Updates(map[string]any{"payout_fee_kobo": "0", "keep_kobo": keep.String()})
	if res.Error == nil && res.RowsAffected == 1 {
		log.Printf("[liquidity] METRIC liquidity_pool_books intent=%s keep_kobo=%s funding_kobo=%s", o.IntentID, keep, fund)
	}
	return res.RowsAffected == 1, res.Error
}

func derefOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}
