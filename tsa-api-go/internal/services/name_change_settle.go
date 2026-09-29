package services

import (
	"context"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// What happens to a finished change's fee: kept once verification was
// attempted, returned when it never was — an MCGP hold reversed, a stablecoin
// fee refunded to the proven, linked wallet it came from.

// Fee decisions pinned on a change when its status alone cannot say.
const (
	nameChangeFeeKeep = "keep"
	// nameChangeFeeReturn is the user's own refund (a cancel, or an unused
	// change lapsing) — what the refund allowance counts.
	nameChangeFeeReturn = "return"
	// nameChangeFeeBounce is money that could not pay the change (short, or
	// too late) and goes back. It counts toward the refund allowance too: a
	// deliberately short or late payment must not be a way round it.
	nameChangeFeeBounce = "bounce"
	// nameChangeFeeKeepAll is a bounce that is not sent back — below the
	// minimum refund, or past the refund allowance — kept whole (no excess
	// refund either) and left to support.
	nameChangeFeeKeepAll = "keep_all"
)

// feeKept says what a finished change does with its fee. decided is false
// while the change is still open.
func feeKept(ch *models.UserNameChange) (keep, decided bool) {
	switch ch.FeeDecision {
	case nameChangeFeeKeep, nameChangeFeeKeepAll:
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
// may now: at most MaxRefundedCancels returns — their own, or payments bounced
// back — in any RefundWindow. It stops a cancel-and-reopen loop, or a string of
// deliberately short payments, from spending treasury and operator gas at will.
func (s *NameChangeService) refundLimit(ctx context.Context, userID uuid.UUID) (*time.Time, error) {
	now := s.now()
	var rows []models.UserNameChange
	if err := s.db.WithContext(ctx).Select("completed_at").
		Where("user_id = ? AND fee_decision IN ? AND pay_method <> ? AND completed_at > ?",
			userID, []string{nameChangeFeeReturn, nameChangeFeeBounce}, models.NameChangePayFree, now.Add(-s.cfg.RefundWindow)).
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
		if ch.FeeStatus != models.NameChangeFeeReceived && ch.FeeStatus != models.NameChangeFeeRefundFailed {
			return
		}
		if !keep {
			s.refund(ctx, ch, stableCredited(ch))
			return
		}
		// Kept — but money paid over the fee was never the fee.
		excess := s.stableExcess(ch)
		switch {
		case excess == nil:
		case excess.Cmp(s.minRefund(ch)) < 0:
			log.Printf("[name-change] change=%s overpaid by %s base units of %s on %s, below the minimum refund; kept — for support",
				ch.ID, excess, ch.PayToken, ch.PayChain)
		default:
			s.refund(ctx, ch, excess)
			return
		}
		s.setFee(ctx, ch.ID, []string{models.NameChangeFeeReceived}, models.NameChangeFeeSettled, "")
	}
}

// stableCredited is what a stablecoin fee actually moved into the treasury
// (base units): what a returned fee sends back, which is not always the quote.
func stableCredited(ch *models.UserNameChange) *big.Int {
	raw := ch.PayCredited
	if raw == "" {
		raw = ch.PayAmount
	}
	n, ok := new(big.Int).SetString(strings.TrimSpace(raw), 10)
	if !ok {
		return nil
	}
	return n
}

// stableExcess is what a kept stablecoin fee was overpaid by, or nil: nothing
// over, unknown amounts, or a bounce kept whole.
func (s *NameChangeService) stableExcess(ch *models.UserNameChange) *big.Int {
	if ch.FeeDecision == nameChangeFeeKeepAll {
		return nil
	}
	credited := stableCredited(ch)
	fee, ok := new(big.Int).SetString(strings.TrimSpace(ch.PayAmount), 10)
	if credited == nil || !ok {
		return nil
	}
	if excess := new(big.Int).Sub(credited, fee); excess.Sign() > 0 {
		return excess
	}
	return nil
}

// minRefund is MinRefundUSD in the change's stablecoin base units.
func (s *NameChangeService) minRefund(ch *models.UserNameChange) *big.Int {
	return stableForUSDMicros(s.minRefundMicros, ch.PayDecimals)
}

// refundBackoff is how long after the attempt-th failed send the sweep waits
// before the next: base, doubling, capped.
func refundBackoff(attempt int) time.Duration {
	d := nameChangeRefundRetryBase
	for i := 1; i < attempt && d < nameChangeRefundRetryMax; i++ {
		d *= 2
	}
	return min(d, nameChangeRefundRetryMax)
}

// refund sends amount (base units) of a stablecoin fee back to the proven
// wallet it came from: all that was credited when the fee is returned, the
// overpaid part when it is kept. The send is claimed first (refund_sending,
// with the amount), so of two callers only one ever broadcasts.
func (s *NameChangeService) refund(ctx context.Context, ch *models.UserNameChange, amount *big.Int) {
	if s.rail == nil {
		alertf("name-change-refund:"+ch.ID.String(), "[name-change] ALERT name_change_refund_unavailable_total=1 change=%s — no treasury to refund from", ch.ID)
		return
	}
	recorded := ""
	if amount != nil {
		recorded = amount.String()
	}
	if !s.setFeeWith(ctx, ch.ID, []string{models.NameChangeFeeReceived, models.NameChangeFeeRefundFailed}, models.NameChangeFeeRefundSending, "",
		map[string]any{"refund_amount": recorded}) {
		return
	}
	if amount == nil || amount.Sign() <= 0 || ch.PayFrom == "" {
		s.setFee(ctx, ch.ID, []string{models.NameChangeFeeRefundSending}, models.NameChangeFeeRefundUncertain, "")
		log.Printf("[name-change] ALERT name_change_refund_unsendable_total=1 change=%s amount=%q — record the outcome by hand", ch.ID, recorded)
		return
	}
	// The treasury's value caps read USD: a $1 stablecoin's amount is its value.
	usd, _ := new(big.Float).Quo(new(big.Float).SetInt(amount),
		new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(ch.PayDecimals)), nil))).Float64()
	txHash, attempted, err := s.rail.Refund(ctx, ch.PayChain, ch.PayToken, ch.PayFrom, amount, usd)
	switch {
	case err == nil:
		s.setFee(ctx, ch.ID, []string{models.NameChangeFeeRefundSending}, models.NameChangeFeeRefunded, txHash)
	case !attempted:
		// Provably never left: safe for the sweep to try again — later each time.
		attempt := ch.RefundAttempts + 1
		retryAt := s.now().Add(refundBackoff(attempt))
		s.setFeeWith(ctx, ch.ID, []string{models.NameChangeFeeRefundSending}, models.NameChangeFeeRefundFailed, "",
			map[string]any{"refund_attempts": attempt, "refund_retry_at": &retryAt})
		log.Printf("[name-change] change=%s refund not sent (attempt %d), retrying from %s: %v", ch.ID, attempt, retryAt.UTC().Format(time.RFC3339), err)
	default:
		s.setFee(ctx, ch.ID, []string{models.NameChangeFeeRefundSending}, models.NameChangeFeeRefundUncertain, txHash)
		log.Printf("[name-change] ALERT name_change_refund_uncertain_total=1 change=%s chain=%s tx=%s — the refund may have left; check the chain, then record the outcome: %v",
			ch.ID, ch.PayChain, txHash, err)
	}
}

// setFee moves the fee status only from one of from — the guard every fee step
// relies on.
func (s *NameChangeService) setFee(ctx context.Context, id uuid.UUID, from []string, to, txHash string) bool {
	return s.setFeeWith(ctx, id, from, to, txHash, nil)
}

// setFeeWith is setFee recording further columns with the move.
func (s *NameChangeService) setFeeWith(ctx context.Context, id uuid.UUID, from []string, to, txHash string, extra map[string]any) bool {
	updates := map[string]any{"fee_status": to}
	if txHash != "" {
		updates["fee_tx_hash"] = txHash
	}
	for k, v := range extra {
		updates[k] = v
	}
	res := s.db.WithContext(ctx).Model(&models.UserNameChange{}).Where("id = ? AND fee_status IN ?", id, from).Updates(updates)
	if res.Error != nil {
		log.Printf("[name-change] change=%s fee %v→%s not recorded: %v", id, from, to, res.Error)
		return false
	}
	return res.RowsAffected == 1
}
