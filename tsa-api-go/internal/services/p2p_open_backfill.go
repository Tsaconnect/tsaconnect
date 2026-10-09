package services

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// openTxReconciled is the open_tx_hash marker written when the open was found
// on chain but its confirm-open never reached us (app killed after broadcast).
// It is not a tx hash: nothing may look up a receipt or build an explorer link
// from it. Same convention as release_tx_hash = "reconciled".
const openTxReconciled = "reconciled"

// openDeadlineFromChain is the contract's cancelByTimeout deadline:
// on-chain createdAt + paymentWindow. False when createdAt is unknown.
func openDeadlineFromChain(onchain *OnChainP2PTrade) (time.Time, bool) {
	if onchain == nil || onchain.CreatedAt == nil || onchain.CreatedAt.Sign() <= 0 {
		return time.Time{}, false
	}
	return time.Unix(onchain.CreatedAt.Int64(), 0).UTC().Add(p2pPaymentWindow), true
}

// openRecordUpdates is the column set written when a trade's on-chain open is
// recorded — by ConfirmOpenTrade (real tx hash) and by the backfill (marker).
// The off-chain deadline is re-anchored to the on-chain createdAt because
// expires_at was set at intent time and drifts early if the open mined late,
// which would nudge a timeout-cancel the contract reverts. onchain may be nil
// (best-effort read failed): the intent-time deadline is then kept.
func (s *P2PTradeService) openRecordUpdates(openTxHash string, onchain *OnChainP2PTrade) map[string]any {
	updates := map[string]any{"open_tx_hash": openTxHash, "updated_at": s.clock.Now()}
	if deadline, ok := openDeadlineFromChain(onchain); ok {
		updates["expires_at"] = deadline
	}
	return updates
}

// recordOpenFromChain records a trade's open from the escrow's view when the
// confirm-open was lost. It refuses an on-chain trade whose amount or funding
// disagrees with the row — that is not the open we prepared. Reports true only
// when this call wrote the open; false when one was already recorded (t is
// refreshed from the row in that case).
func (s *P2PTradeService) recordOpenFromChain(ctx context.Context, t *models.P2PTrade, onchain *OnChainP2PTrade) (bool, error) {
	expected, ok := new(big.Int).SetString(t.TokenAmount, 10)
	if !ok || onchain.Amount == nil || onchain.Amount.Cmp(expected) != 0 || onchain.Funding != t.Funding {
		log.Printf("[p2p] ALERT p2p_open_backfill_mismatch_total=1 trade=%s chainAmount=%v dbAmount=%s chainFunding=%d dbFunding=%d",
			t.ID, onchain.Amount, t.TokenAmount, onchain.Funding, t.Funding)
		return false, fmt.Errorf("%w: on-chain trade does not match this trade's amount or funding", ErrInvalidTransition)
	}
	updates := s.openRecordUpdates(openTxReconciled, onchain)
	res := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("id = ? AND (open_tx_hash = '' OR open_tx_hash IS NULL)", t.ID).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		// A confirm-open (or another backfill) recorded it between our read and here.
		var current models.P2PTrade
		if err := s.db.WithContext(ctx).Select("open_tx_hash").First(&current, "id = ?", t.ID).Error; err != nil {
			return false, err
		}
		t.OpenTxHash = current.OpenTxHash
		return false, nil
	}
	s.audit(ctx, models.P2PAuditReconcilerDrift, nil, &t.AdID, &t.ID, map[string]any{
		"reason": "open on chain, confirm lost",
	})
	t.OpenTxHash = openTxReconciled
	if deadline, ok := updates["expires_at"].(time.Time); ok {
		t.ExpiresAt = deadline
	}
	t.UpdatedAt = updates["updated_at"].(time.Time)
	return true, nil
}

// openRecorded reports whether the trade's escrow is known to be funded,
// backfilling a lost confirm-open from chain when it is.
func (s *P2PTradeService) openRecorded(ctx context.Context, t *models.P2PTrade, escrow P2PEscrowAdapter) (bool, error) {
	if strings.TrimSpace(t.OpenTxHash) != "" {
		return true, nil
	}
	onchain, err := escrow.ReadTrade(HashID(t.ID))
	if err != nil {
		return false, fmt.Errorf("%w: read on-chain trade: %v", ErrChainUnavailable, err)
	}
	if !onchain.Exists {
		return false, nil
	}
	if _, err := s.recordOpenFromChain(ctx, t, onchain); err != nil {
		return false, err
	}
	return strings.TrimSpace(t.OpenTxHash) != "", nil
}

// requireOpenOnChain is the one gate for "the escrow is funded" on the
// payer's mark-paid and on raising a dispute: a never-opened trade has nothing
// escrowed, so flipping it to paid or disputed would leave a state the
// reconciler can't clear. SystemMarkPaid (PM) keeps its own inline gate.
func (s *P2PTradeService) requireOpenOnChain(ctx context.Context, t *models.P2PTrade, escrow P2PEscrowAdapter) error {
	opened, err := s.openRecorded(ctx, t, escrow)
	if err != nil {
		return err
	}
	if !opened {
		return fmt.Errorf("%w: trade not yet opened on-chain", ErrInvalidTransition)
	}
	return nil
}
