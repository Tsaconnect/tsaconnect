package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrITOperatorNoteRequired: an operator's money decision needs a note — it is
// the only record of what was actually done.
var ErrITOperatorNoteRequired = errors.New("a note is required — it is the only record of what was actually done")

// auditIntent appends an operator action to it_intent_audit_logs, on tx.
func auditIntent(tx *gorm.DB, intentID, adminID uuid.UUID, action, note string, meta map[string]any) error {
	raw, _ := json.Marshal(meta)
	actor := adminID
	return tx.Create(&models.ITIntentAuditLog{
		IntentID: intentID, ActorID: &actor, Action: action,
		Note: truncateRunes(note, 512), Metadata: datatypes.JSON(raw),
	}).Error
}

// repriceTreasuryParked prices a parked company-wallet buy's received naira
// live and moves it back to payment_received — only if free stock covers it,
// under the lock (spec §5.2). waiveMax is the operator's "deliver anyway"
// (§13 Q9). onApplied runs in the same transaction after the write (audit).
func (s *ITService) repriceTreasuryParked(ctx context.Context, intent *models.ITIntent, waiveMax bool, onApplied func(tx *gorm.DB) error) (*models.ITIntent, error) {
	if !s.HasCompanyWallet() {
		return nil, ErrTreasuryUnavailable
	}
	if intent.Status != models.ITStatusBuyAwaitingRate {
		return nil, ErrITBadState
	}
	paid, ok := new(big.Int).SetString(strings.TrimSpace(intent.ReceivedAmount), 10)
	if !ok || paid.Sign() <= 0 {
		return nil, fmt.Errorf("%w: no received amount to price", ErrITBadState)
	}
	ch, err := s.paidThrough(ctx, intent)
	if err != nil {
		return nil, err
	}
	q, err := s.quoteTreasuryLive(ctx, intent.Asset, paid, intent.Currency, !waiveMax, ch)
	if err != nil {
		return nil, err
	}
	if err := s.convertedCeiling(ctx, intent, q.AssetUnits); err != nil {
		return nil, err
	}
	applied := false
	chain := strings.ToLower(intent.DestChain)
	err = s.withStockLock(ctx, chain, intent.Asset, func(tx *gorm.DB) error {
		// Deliver-anyway and a re-price lead to a send: the same gate as every
		// other second-payment path.
		reason, err := s.secondPaymentGate(ctx, tx, intent.ID, uuid.Nil)
		if err != nil {
			return err
		}
		if reason != "" {
			return fmt.Errorf("%w: %s", ErrITBadState, reason)
		}
		free, _, err := s.freeTreasuryStock(ctx, tx, chain, intent.Asset, intent.ID, true)
		if err != nil {
			return err
		}
		if free.Cmp(q.AssetUnits) < 0 {
			return fmt.Errorf("%w: needs %s, %s free", ErrTreasuryStockShort,
				FormatStableUnits(q.AssetUnits.String()), FormatStableUnits(free.String()))
		}
		// The daily cap is the buyer's limit, not the order's: "deliver
		// anyway" waives only the per-order maximum.
		if err := s.checkTreasuryDailyCap(ctx, tx, intent.UserID, q.AssetUnits, intent.ID); err != nil {
			return err
		}
		cols := map[string]any{"status": models.ITStatusPaymentReceived, "repriced": true, "failure_reason": ""}
		if waiveMax && q.AssetUnits.Cmp(s.treasuryLimits().maxUnits) > 0 {
			cols["max_waived"] = true // §13 Q9: a waiver that happened lives on the order too
		}
		setQuoteColumns(cols, q.AssetUnits, q.RatePerUnitMinor, q.DisplayRateMinor, q.Fees)
		res := tx.Model(&models.ITIntent{}).
			Where("id = ? AND status = ?", intent.ID, models.ITStatusBuyAwaitingRate).Updates(cols)
		if res.Error != nil {
			return res.Error
		}
		applied = res.RowsAffected == 1
		if applied && onApplied != nil {
			return onApplied(tx)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !applied {
		return nil, ErrITBadState // someone moved it between our read and our write
	}
	log.Printf("[it stable buy] METRIC ramp_treasury_repriced intent=%s operator=true waive_max=%v units=%s",
		intent.ID, waiveMax, q.AssetUnits)
	s.notifyRateUpdated(intent, q.AssetUnits)
	return s.getIntent(ctx, intent.ID)
}

// DeliverParkedTreasuryBuy is the operator's "deliver anyway" for a parked
// company-wallet buy — usually an overpayment above the maximum (§13 Q9):
// re-priced live with the maximum waived, only when free stock covers it.
func (s *ITService) DeliverParkedTreasuryBuy(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return nil, ErrITOperatorNoteRequired
	}
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if intent.Direction != models.ITDirectionBuy || !isCompanyWalletBuy(intent) {
		return nil, ErrITBadState
	}
	if rec, err := s.ConversionFor(ctx, intent); err != nil {
		return nil, err
	} else if rec != nil {
		return nil, ErrConvertedNoWaiver // N4: refund is the only exit
	}
	return s.repriceTreasuryParked(ctx, intent, true, func(tx *gorm.DB) error {
		return auditIntent(tx, intent.ID, adminID, models.ITAuditStableBuyDeliveredAnyway, note, map[string]any{
			"receivedAmount": intent.ReceivedAmount, "parkedReason": intent.FailureReason,
		})
	})
}

// SliceLeftovers counts what the retired slice pool (spec 2026-10-08) can
// have left behind; nothing in this build settles any of it. Boot refuses
// while any count is non-zero (cmd/server/slice_leftovers.go).
type SliceLeftovers struct {
	Slices, Intents, Forwards int64
}

// Any reports whether a leftover exists.
func (l SliceLeftovers) Any() bool { return l.Slices > 0 || l.Intents > 0 || l.Forwards > 0 }

// sliceEraOpenIntentStatuses are the pool order states in which a slice-era
// order still holds or owes money (slices locked, naira in, delivery owed).
var sliceEraOpenIntentStatuses = []string{
	models.ITStatusOpening, models.ITStatusPendingPayment, models.ITStatusPaymentReceived,
	models.ITStatusAwaitingFunds, models.ITStatusDelivering, models.ITStatusBuyAwaitingRate,
}

// CountSliceLeftovers counts (a) non-terminal slices, (b) live pool orders
// the slice path opened — a vault order always has pool_orders.fill_units
// written at open, a slice-era one never does — and (c) LP naira forwards
// ("pool:" automatic, "pool-f22:" operator) not yet landed.
func CountSliceLeftovers(ctx context.Context, db *gorm.DB) (SliceLeftovers, error) {
	var l SliceLeftovers
	db = db.WithContext(ctx)
	if err := db.Model(&models.P2PTrade{}).
		Where("it_intent_id IS NOT NULL AND status NOT IN ?",
			[]string{models.P2PTradeStatusReleased, models.P2PTradeStatusCancelled, models.P2PTradeStatusResolved}).
		Count(&l.Slices).Error; err != nil {
		return l, err
	}
	if err := db.Model(&models.ITIntent{}).
		Where("provider IN ?", poolProviders()).
		Where("status IN ?", sliceEraOpenIntentStatuses).
		Where("NOT EXISTS (SELECT 1 FROM pool_orders po WHERE po.intent_id = it_intents.id AND po.fill_units IS NOT NULL)").
		Count(&l.Intents).Error; err != nil {
		return l, err
	}
	if err := db.Model(&models.P2PSettlementForward{}).
		Where("(provider_ref LIKE 'pool:%' OR provider_ref LIKE 'pool-f22:%') AND status IN ?",
			[]string{models.P2PForwardPending, models.P2PForwardSubmitted}).
		Count(&l.Forwards).Error; err != nil {
		return l, err
	}
	return l, nil
}

// poolRefundableWithoutDesk: a TSA pool buy may be refunded in naira with no
// pool desk only while it is paid and undelivered, no vault fill is recorded
// for it, and every slice the retired slice pool opened for it is cancelled
// (a slice in any other state may have released LP crypto to the treasury,
// or still can — that order is settled by hand, never refunded here).
func poolRefundableWithoutDesk(tx *gorm.DB, intent *models.ITIntent) error {
	if intent.Status != models.ITStatusPaymentReceived && intent.Status != models.ITStatusBuyAwaitingRate {
		return fmt.Errorf("%w: a TSA pool buy is refundable only while paid and undelivered (status %s)", ErrITBadState, intent.Status)
	}
	var filled int64
	if err := tx.Model(&models.PoolOrder{}).
		Where("intent_id = ? AND (fill_tx_hash <> '' OR filled_at IS NOT NULL)", intent.ID).
		Count(&filled).Error; err != nil {
		return err
	}
	if filled > 0 {
		return fmt.Errorf("%w: this order's vault fill is recorded — it must be delivered, never refunded", ErrITBadState)
	}
	var slices []models.P2PTrade
	if err := tx.Select("id", "status").Where("it_intent_id = ?", intent.ID).Find(&slices).Error; err != nil {
		return err
	}
	var live []string
	for _, sl := range slices {
		if sl.Status != models.P2PTradeStatusCancelled {
			live = append(live, sl.ID.String()+"="+sl.Status)
		}
	}
	if len(live) > 0 {
		return fmt.Errorf("%w: a TSA pool buy with a slice not cancelled (%s) is never refunded without the pool desk — settle its slices by hand first",
			ErrITBadState, strings.Join(live, ", "))
	}
	return nil
}

// MarkStableBuyRefunded records that an operator returned a buy's naira (spec
// §5.2), for either provider: terminal `refunded`, with an audit row, in one
// transaction (Decision D56). It closes a parked buy, and — review C2 — a
// company-wallet buy stuck in `delivering` with no live send (a kill switch,
// gas, a refused broadcast). The intent row is locked first, as the send path
// locks it before reserving a sale, so a refund and a send never both happen.
func (s *ITService) MarkStableBuyRefunded(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return nil, ErrITOperatorNoteRequired
	}
	if pre, err := s.getIntent(ctx, intentID); err == nil && isPoolBuy(pre) && s.poolDesk != nil {
		return s.refundPoolBuy(ctx, adminID, intentID, note) // S37, under the pool lock (plan C2 P11)
	}
	var provider string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var intent models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&intent, "id = ?", intentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrStableIntentNotFound
			}
			return err
		}
		// P5: an unknown provider (ErrUnknownITProvider) has zero terms, so it
		// gets neither the company wallet's gate nor Flutterwave's hashless
		// branch — only its park (buy_awaiting_rate) is refundable. Safe: every
		// provider-specific path refused it, so nothing was sent or converted.
		terms, termsErr := providerTerms(&intent)
		if termsErr != nil && !errors.Is(termsErr, ErrUnknownITProvider) {
			return termsErr
		}
		provider = models.ITIntentProvider(&intent) // the audit row names the raw value
		if terms.pool {
			// With a pool desk the refund is refundPoolBuy's, under the pool
			// lock. Without one (vault unconfigured, or a slice-era order left
			// over) nothing can fill or deliver it any more, so the operator's
			// naira refund is its only exit — allowed only while provably no
			// crypto moved for it.
			if s.poolDesk != nil {
				return fmt.Errorf("%w: a TSA pool buy is refunded only through its own path", ErrITBadState)
			}
			if err := poolRefundableWithoutDesk(tx, &intent); err != nil {
				return err
			}
			return s.writePoolRefund(ctx, tx, &intent, adminID, models.ITAuditStableBuyRefunded, note, "an operator", map[string]any{
				"receivedAmount": intent.ReceivedAmount, "provider": provider, "fromStatus": intent.Status,
				"parkedReason": intent.FailureReason, "poolDesk": false})
		}
		refundable := []string{models.ITStatusBuyAwaitingRate}
		if terms.companyWallet {
			// Review N1 / C2: never refund while any sale can still pay her —
			// the one gate every second-payment path shares.
			reason, err := s.secondPaymentGate(ctx, tx, intentID, uuid.Nil)
			if err != nil {
				return err
			}
			if reason != "" {
				return fmt.Errorf("%w: %s", ErrITBadState, reason)
			}
			if intent.Status == models.ITStatusDelivering {
				refundable = append(refundable, models.ITStatusDelivering)
			}
		}
		// Ledger ruling M5: a Flutterwave converted buy parked without a hash
		// is settled, yet nothing proves Flutterwave delivered. Its operator
		// exit is a naira refund, closing the park in this transaction under
		// a compare-and-set, so an attach-hash and a refund never both win.
		// Any other settled buy stays refused.
		hashlessPark := false
		if terms.flutterwaveFulfils && intent.Status == models.ITStatusSettled {
			rec, err := conversionRecordOn(ctx, tx, intentID)
			if err != nil {
				return err
			}
			if isHashlessPark(rec) {
				hashlessPark = true
				refundable = append(refundable, models.ITStatusSettled)
			}
		}
		res := tx.Model(&models.ITIntent{}).
			Where("id = ? AND direction = ? AND status IN ? AND asset IN ?", intentID, models.ITDirectionBuy,
				refundable, []string{models.ITAssetUSDC, models.ITAssetUSDT}).
			Updates(map[string]any{"status": models.ITStatusRefunded, "failure_reason": "refunded by an operator"})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("%w: only a parked buy, or a company-wallet delivery with no send, can be marked refunded (status %s)",
				ErrITBadState, intent.Status)
		}
		if hashlessPark {
			closed := tx.Model(&models.PaycrestOrderRecord{}).
				Where("reference = ? AND status = ? AND holder = ? AND tx_hash = ?", ITConversionRef(intentID),
					models.PaycrestStatusRecoveryPending, models.HolderFlutterwave, "").
				Updates(map[string]any{"status": models.PaycrestStatusCancelled, "failure_reason": "refunded in naira by an operator"})
			if closed.Error != nil {
				return closed.Error
			}
			if closed.RowsAffected != 1 {
				return fmt.Errorf("%w: the conversion record changed while the refund was recorded", ErrITBadState)
			}
		}
		// A converted buy's record closes with it: the naira went back, no
		// conversion will ever run (spec §15.4).
		if err := markConversion(ctx, tx, intentID,
			[]string{models.PaycrestStatusAwaitingPayment, models.PaycrestStatusPending},
			map[string]any{"status": models.PaycrestStatusCancelled, "failure_reason": "refunded in naira by an operator"}); err != nil {
			return err
		}
		return auditIntent(tx, intentID, adminID, models.ITAuditStableBuyRefunded, note, map[string]any{
			"receivedAmount": intent.ReceivedAmount, "provider": provider, "fromStatus": intent.Status,
			"parkedReason": intent.FailureReason,
		})
	})
	if err != nil {
		return nil, err
	}
	log.Printf("[it stable buy] METRIC ramp_it_refunded intent=%s provider=%s admin=%s", intentID, provider, adminID)
	return s.getIntent(ctx, intentID)
}
