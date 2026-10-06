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
	q, err := s.quoteTreasuryLive(ctx, intent.Asset, paid, intent.Currency, !waiveMax)
	if err != nil {
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
	if intent.Direction != models.ITDirectionBuy || models.ITIntentProvider(intent) != models.ITProviderTreasury {
		return nil, ErrITBadState
	}
	return s.repriceTreasuryParked(ctx, intent, true, func(tx *gorm.DB) error {
		return auditIntent(tx, intent.ID, adminID, models.ITAuditStableBuyDeliveredAnyway, note, map[string]any{
			"receivedAmount": intent.ReceivedAmount, "parkedReason": intent.FailureReason,
		})
	})
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
	var provider string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var intent models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&intent, "id = ?", intentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrStableIntentNotFound
			}
			return err
		}
		provider = models.ITIntentProvider(&intent)
		refundable := []string{models.ITStatusBuyAwaitingRate}
		if provider == models.ITProviderTreasury {
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
