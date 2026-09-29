package services

import (
	"context"
	"log"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// What happens to a finished change's fee: kept once verification was
// attempted, returned when it never was — an MCGP hold reversed, a stablecoin
// fee refunded to the proven wallet it came from.

// Fee decisions pinned on a change when its status alone cannot say.
const (
	nameChangeFeeKeep = "keep"
	// nameChangeFeeReturn is the user's own refund (a cancel, or an unused
	// change lapsing) — what the refund allowance counts.
	nameChangeFeeReturn = "return"
	// nameChangeFeeBounce is money that could not pay the change (short, or
	// too late) and goes back whatever the allowance says.
	nameChangeFeeBounce = "bounce"
)

// feeKept says what a finished change does with its fee. decided is false
// while the change is still open.
func feeKept(ch *models.UserNameChange) (keep, decided bool) {
	switch ch.FeeDecision {
	case nameChangeFeeKeep:
		return true, true
	case nameChangeFeeReturn, nameChangeFeeBounce:
		return false, true
	}
	switch ch.Status {
	case models.NameChangeStatusApproved, models.NameChangeStatusRejected:
		return true, true
	case models.NameChangeStatusLapsed:
		return ch.Attempts > 0, true
	case models.NameChangeStatusCancelled:
		return false, true
	}
	return false, false
}

// refundLimit is when the user may next have a fee returned, or nil when they
// may now: at most MaxRefundedCancels returns in any RefundWindow. It stops a
// cancel-and-reopen loop from spending treasury and operator gas at will.
func (s *NameChangeService) refundLimit(ctx context.Context, userID uuid.UUID) (*time.Time, error) {
	now := s.now()
	var rows []models.UserNameChange
	if err := s.db.WithContext(ctx).Select("completed_at").
		Where("user_id = ? AND fee_decision = ? AND pay_method <> ? AND completed_at > ?",
			userID, nameChangeFeeReturn, models.NameChangePayFree, now.Add(-s.cfg.RefundWindow)).
		Order("completed_at asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) < s.cfg.MaxRefundedCancels {
		return nil, nil
	}
	next := rows[len(rows)-s.cfg.MaxRefundedCancels].CompletedAt.Add(s.cfg.RefundWindow)
	return &next, nil
}

// resolveFee settles, reverses or refunds a finished change's fee. Every step
// is guarded on the fee status it read, so the request that finished the change
// and the sweep retrying it cannot both move the money. A failure is left for
// the sweep; it never undoes the change itself.
func (s *NameChangeService) resolveFee(ctx context.Context, ch *models.UserNameChange) {
	keep, decided := feeKept(ch)
	if !decided {
		return
	}
	switch ch.PayMethod {
	case models.NameChangePayMCGP:
		if ch.FeeStatus != models.NameChangeFeeHeld || s.debits == nil {
			return
		}
		resolve, target := s.debits.Reverse, models.NameChangeFeeReversed
		if keep {
			resolve, target = s.debits.Settle, models.NameChangeFeeSettled
		}
		pay, err := resolve(ctx, ch.ID)
		if err != nil {
			alertf("name-change-fee:"+ch.ID.String(),
				"[name-change] ALERT name_change_fee_unresolved_total=1 change=%s want=%s err=%v", ch.ID, target, err)
			return
		}
		s.setFee(ctx, ch.ID, []string{models.NameChangeFeeHeld}, target, pay.ResolveTxHash)
	case models.NameChangePayStablecoin:
		switch {
		case keep && ch.FeeStatus == models.NameChangeFeeReceived:
			s.setFee(ctx, ch.ID, []string{models.NameChangeFeeReceived}, models.NameChangeFeeSettled, "")
		case !keep && (ch.FeeStatus == models.NameChangeFeeReceived || ch.FeeStatus == models.NameChangeFeeRefundFailed):
			s.refund(ctx, ch)
		}
	}
}

// refund sends a stablecoin fee back to the proven wallet it came from — what
// was actually credited, not the quote. The send is claimed first
// (refund_sending), so of two callers only one ever broadcasts.
func (s *NameChangeService) refund(ctx context.Context, ch *models.UserNameChange) {
	if s.rail == nil {
		alertf("name-change-refund:"+ch.ID.String(), "[name-change] ALERT name_change_refund_unavailable_total=1 change=%s — no treasury to refund from", ch.ID)
		return
	}
	if !s.setFee(ctx, ch.ID, []string{models.NameChangeFeeReceived, models.NameChangeFeeRefundFailed}, models.NameChangeFeeRefundSending, "") {
		return
	}
	raw := ch.PayCredited
	if raw == "" {
		raw = ch.PayAmount
	}
	amount, ok := new(big.Int).SetString(raw, 10)
	if !ok || amount.Sign() <= 0 || ch.PayFrom == "" {
		s.setFee(ctx, ch.ID, []string{models.NameChangeFeeRefundSending}, models.NameChangeFeeRefundUncertain, "")
		log.Printf("[name-change] ALERT name_change_refund_unsendable_total=1 change=%s amount=%q — record the outcome by hand", ch.ID, raw)
		return
	}
	usd, _ := new(big.Float).Quo(new(big.Float).SetInt(s.feeMicrosOf(ch)), big.NewFloat(1e6)).Float64()
	txHash, attempted, err := s.rail.Refund(ctx, ch.PayChain, ch.PayToken, ch.PayFrom, amount, usd)
	switch {
	case err == nil:
		s.setFee(ctx, ch.ID, []string{models.NameChangeFeeRefundSending}, models.NameChangeFeeRefunded, txHash)
	case !attempted:
		// Provably never left: safe for the sweep to try again.
		s.setFee(ctx, ch.ID, []string{models.NameChangeFeeRefundSending}, models.NameChangeFeeRefundFailed, "")
		log.Printf("[name-change] change=%s refund not sent, will retry: %v", ch.ID, err)
	default:
		s.setFee(ctx, ch.ID, []string{models.NameChangeFeeRefundSending}, models.NameChangeFeeRefundUncertain, txHash)
		log.Printf("[name-change] ALERT name_change_refund_uncertain_total=1 change=%s chain=%s tx=%s — the refund may have left; check the chain, then record the outcome: %v",
			ch.ID, ch.PayChain, txHash, err)
	}
}

// feeMicrosOf is the change's frozen fee in USD micros (for the treasury caps).
func (s *NameChangeService) feeMicrosOf(ch *models.UserNameChange) *big.Int {
	if m, err := ParseFeeUSDMicros(ch.FeeUSD); err == nil {
		return m
	}
	return new(big.Int)
}

// setFee moves the fee status only from one of from — the guard every fee step
// relies on.
func (s *NameChangeService) setFee(ctx context.Context, id uuid.UUID, from []string, to, txHash string) bool {
	updates := map[string]any{"fee_status": to}
	if txHash != "" {
		updates["fee_tx_hash"] = txHash
	}
	res := s.db.WithContext(ctx).Model(&models.UserNameChange{}).Where("id = ? AND fee_status IN ?", id, from).Updates(updates)
	if res.Error != nil {
		log.Printf("[name-change] change=%s fee %v→%s not recorded: %v", id, from, to, res.Error)
		return false
	}
	return res.RowsAffected == 1
}
