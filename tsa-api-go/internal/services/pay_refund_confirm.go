package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// payRefundConfirmBatch bounds one confirmer tick.
const payRefundConfirmBatch = 50

// payRefundUnconfirmedAfter is when a refund still unproven is ALERTed (once).
const payRefundUnconfirmedAfter = time.Hour

// Reasons a refund is parked in needs_review, in words the owner reads. A
// partial credit's reason is built from the amounts ("Credited 12.00 of 12.50").
const (
	payRefundReplacedReason   = "Replaced by another transaction from your wallet — support is checking whether it paid"
	payRefundUnexpectedReason = "The network showed something unexpected for this transaction — support is checking it"
)

// payRefundReview is why a refund was parked in needs_review. It picks the
// ALERT and the owner's notice; the reason words are stored on the row.
type payRefundReview int

const (
	payRefundReviewShort      payRefundReview = iota + 1 // credited less than the refund (even 0.00 of it)
	payRefundReviewReplaced                              // another transaction took its nonce
	payRefundReviewUnexpected                            // any other refuting reading
)

// alert is the ALERT name for parking a refund for review.
func (k payRefundReview) alert() string {
	switch k {
	case payRefundReviewShort:
		return "pay_refund_needs_review"
	case payRefundReviewReplaced:
		return "pay_refund_replaced"
	}
	return "pay_refund_unexpected_reading"
}

// Reasons a refund failed — proven to have paid nothing.
const (
	payRefundRevertedReason    = "The transaction failed on-chain"
	payRefundNothingPaidReason = "The transaction didn't pay the customer"
)

// ConfirmDue checks submitted refunds against the chain (the
// pay-refund-confirmer, every 30 s), the least recently checked first. Each
// refund's checked_at is stamped as it is looked at, so refunds on a chain that
// can't be read rotate to the back of the queue instead of hiding every newer
// refund behind them (review I5). A chain it can't read fails the tick — the
// worker's failure streak is the alert — and changes nothing else. Errors are
// returned redacted: the worker logs them as they are.
func (s *PayRefundService) ConfirmDue(ctx context.Context) error {
	s.pruneAlerted()
	var due []models.PayRefund
	if err := s.db.WithContext(ctx).Where("status = ?", models.PayRefundSubmitted).
		Order("checked_at NULLS FIRST, submitted_at").Limit(payRefundConfirmBatch).Find(&due).Error; err != nil {
		return errors.New(RedactAlertText(err.Error()))
	}
	var errs []error
	for i := range due {
		at := s.now()
		if err := s.db.WithContext(ctx).Model(&models.PayRefund{}).Where("id = ?", due[i].ID).
			UpdateColumn("checked_at", &at).Error; err != nil {
			errs = append(errs, errors.New(RedactAlertText(err.Error())))
			continue
		}
		if err := s.check(ctx, &due[i], []string{models.PayRefundSubmitted}, true); err != nil {
			msg := RedactAlertText(err.Error())
			log.Printf("[tsa-pay] refund %s: check: %s", due[i].ID, msg)
			errs = append(errs, fmt.Errorf("refund %s: %s", due[i].ID, msg))
		}
	}
	return errors.Join(errs...)
}

// Reevaluate re-reads the chain for a submitted or needs_review refund now (the
// super admin's look at needs_review). A full credit confirms it from either
// status; nothing else can move a needs_review refund — see check.
func (s *PayRefundService) Reevaluate(ctx context.Context, id uuid.UUID) (*models.PayRefund, error) {
	var r models.PayRefund
	if err := s.db.WithContext(ctx).First(&r, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPayRefundNotFound
		}
		return nil, err
	}
	if r.Status != models.PayRefundSubmitted && r.Status != models.PayRefundNeedsReview {
		return nil, ErrPayRefundState
	}
	if err := s.check(ctx, &r, []string{models.PayRefundSubmitted, models.PayRefundNeedsReview}, true); err != nil {
		return nil, err
	}
	return s.Get(ctx, r.MerchantID, r.ID)
}

// check reads r's transfer and moves r the way the chain says (D24 as amended
// by the plans review, C3 and I4, and preflight P2/P3).
//
// confirmFrom is the statuses a proven full credit may confirm r from:
// ConfirmDue and Submit pass [submitted], Reevaluate [submitted, needs_review].
// resend lets a transfer the chain doesn't know be sent again; Submit passes
// false when its own broadcast was just refused, leaving that to the confirmer.
// Every other move starts from submitted only:
//   - failed: only on facts that cannot un-happen — a receipt that reverted,
//     at least MinConf deep, or a transfer at least MinConf deep that credited
//     nothing to to_address — so the cap may safely release a failed amount;
//   - needs_review: a partial credit; a nonce another transaction took while
//     ours stays unknown ("replaced": that one may itself have paid); or any
//     other reading that refutes the transfer (another recipient, another
//     signer) — none of those prove the customer unpaid.
//
// A needs_review refund has already been seen to move money, or may have; a
// later reading that says otherwise (a lagging or pruned node, a reorg read) is
// a contradiction for a person — it stays needs_review with an ALERT and its
// amount stays held against the cap. Not mined, not deep, or not readable: r
// stays as it is (an unreadable chain is returned, redacted, wrapping
// ErrPayRefundChainUnreadable).
//
// The server decoded and recorded the transaction itself (D23), so no
// NotBefore applies: when it was mined says nothing about whether it is this
// refund. Verify counts only Transfer logs emitted by r's token contract.
func (s *PayRefundService) check(ctx context.Context, r *models.PayRefund, confirmFrom []string, resend bool) error {
	if r.TxHash == nil || *r.TxHash == "" || s.network == nil || s.verifier == nil {
		return nil
	}
	chain, ok := s.network.MainnetChainNameForID(r.ChainID)
	if !ok {
		return fmt.Errorf("%w: chain %d has no client", ErrPayRefundChainUnreadable, r.ChainID)
	}
	p, err := s.verifier.Verify(ctx, chain, *r.TxHash, r.TokenAddress, r.FromAddress, r.ToAddress)
	out := ProveStableTransfer(p, err, StableTransferWant{From: r.FromAddress, MinConf: s.minConf, RevertNeedsDepth: true})
	switch out.Verdict {
	case StableTransferProven:
		return s.settleProven(ctx, chain, r, confirmFrom, p.Credited)
	case StableTransferRefuted:
		switch {
		case out.Reason == StableReasonReverted && errors.Is(err, ErrTransferReverted):
			return s.markFailed(ctx, r, payRefundRevertedReason)
		case out.Reason == StableReasonNothingMoved:
			return s.markFailed(ctx, r, payRefundNothingPaidReason)
		}
		reading := refundReadingName(out.Reason, err)
		if err != nil {
			reading += ": " + RedactAlertText(err.Error())
		}
		log.Printf("[tsa-pay] refund %s: unexpected reading of %s on %s: %s", r.ID, *r.TxHash, chain, reading)
		return s.markNeedsReview(ctx, r, 0, payRefundUnexpectedReason, payRefundReviewUnexpected)
	}
	if out.Reason != StableReasonUnreadable {
		return nil // mined, not deep enough yet
	}
	known, _, err := s.verifier.TransactionKnown(ctx, chain, *r.TxHash)
	if err != nil {
		return fmt.Errorf("%w: read %s on %s: %s", ErrPayRefundChainUnreadable, *r.TxHash, chain, RedactAlertText(err.Error()))
	}
	if known {
		s.alertUnconfirmed(r) // pending in the mempool, or mined and the receipt lags
		return nil
	}
	return s.unknownToChain(ctx, chain, r, resend)
}

// refundReadingName names a refuting reading for the log.
func refundReadingName(reason StableTransferReason, err error) string {
	switch {
	case errors.Is(err, ErrTransferWrongRecipient):
		return "wrong recipient"
	case reason == StableReasonWrongSender:
		return "signed by another wallet"
	case reason == StableReasonTooEarly:
		return "mined too early"
	}
	return fmt.Sprintf("refuted (reason %d)", reason)
}

// settleProven: credited ≥ the refund at the token's decimals confirms it from
// one of confirmFrom (ALERT when more); credited but short parks a submitted
// refund for a person (ALERT).
func (s *PayRefundService) settleProven(ctx context.Context, chain string, r *models.PayRefund, confirmFrom []string, credited *big.Int) error {
	decimals, err := s.network.TokenDecimals(chain, r.TokenAddress)
	if err != nil {
		return fmt.Errorf("%w: %s decimals on %s: %s", ErrPayRefundChainUnreadable, r.TokenSymbol, chain, RedactAlertText(err.Error()))
	}
	want := stableForUSDMicros(big.NewInt(r.AmountMicroUSD), decimals)
	creditedMicro, err := usdMicrosFloor(credited, decimals)
	if err != nil {
		if s.alertOnce(r.ID, "pay_refund_credit_unreadable") {
			log.Printf("[tsa-pay] ALERT pay_refund_credit_unreadable=1 refund=%s payment=%s tx=%s err=%q", r.ID, r.PaymentID, *r.TxHash, err)
		}
		return fmt.Errorf("credited by %s: %w", *r.TxHash, err)
	}
	if credited.Cmp(want) < 0 {
		why := "Credited " + FormatMicroUSD(creditedMicro) + " of " + FormatMicroUSD(r.AmountMicroUSD)
		return s.markNeedsReview(ctx, r, creditedMicro, why, payRefundReviewShort)
	}
	if credited.Cmp(want) > 0 {
		log.Printf("[tsa-pay] ALERT pay_refund_over_credited=1 refund=%s payment=%s credited=%s want=%s tx=%s",
			r.ID, r.PaymentID, credited, want, *r.TxHash)
	}
	_, err = s.claimConfirmed(ctx, r, confirmFrom, creditedMicro)
	return err
}

// unknownToChain: the node has never seen the transaction. If a transaction
// with its nonce is already deep and, asked again, the chain still doesn't
// know ours, ours can never mine — but the one that took its place may have
// paid the customer (the same key in another wallet app re-sent or sped it
// up), or the node may have pruned our transaction's index. So it is parked
// in needs_review, never failed (review I4). Otherwise, when resend, send it
// again (idempotent — the same signed bytes), recording any refusal on the row.
func (s *PayRefundService) unknownToChain(ctx context.Context, chain string, r *models.PayRefund, resend bool) error {
	tx, signer, err := blockchain.DecodeSignedTx(r.RawTx)
	if err != nil {
		if s.alertOnce(r.ID, "pay_refund_raw_tx_unreadable") {
			log.Printf("[tsa-pay] ALERT pay_refund_raw_tx_unreadable=1 refund=%s err=%q", r.ID, err)
		}
		return nil
	}
	nonce, err := s.network.ConfirmedNonce(ctx, chain, signer, s.minConf)
	if err != nil {
		return fmt.Errorf("%w: nonce of %s on %s: %s", ErrPayRefundChainUnreadable, signer.Hex(), chain, RedactAlertText(err.Error()))
	}
	if nonce > tx.Nonce() {
		known, _, err := s.verifier.TransactionKnown(ctx, chain, *r.TxHash)
		if err != nil {
			return fmt.Errorf("%w: re-read %s on %s: %s", ErrPayRefundChainUnreadable, *r.TxHash, chain, RedactAlertText(err.Error()))
		}
		if known {
			return nil // ours is the one that mined; the next look proves it
		}
		return s.markNeedsReview(ctx, r, 0, payRefundReplacedReason, payRefundReviewReplaced)
	}
	if resend {
		s.broadcast(ctx, chain, r, tx)
	}
	s.alertUnconfirmed(r)
	return nil
}

// markFailed closes a submitted r as proven unpaid and tells the owner they may
// ask again. Only submitted can fail (review C3): a needs_review refund the
// chain now reads as unpaid is a contradiction — see alertContradicted.
func (s *PayRefundService) markFailed(ctx context.Context, r *models.PayRefund, why string) error {
	if r.Status != models.PayRefundSubmitted {
		s.alertContradicted(r, why)
		return nil
	}
	res := s.db.WithContext(ctx).Model(&models.PayRefund{}).Where("id = ? AND status = ?", r.ID, models.PayRefundSubmitted).
		Updates(map[string]any{"status": models.PayRefundFailed, "failure_reason": why})
	if res.Error != nil || res.RowsAffected != 1 {
		return res.Error
	}
	r.Status, r.FailureReason = models.PayRefundFailed, why
	log.Printf("[tsa-pay] refund %s failed: %s (tx %s)", r.ID, why, *r.TxHash)
	s.announce(ctx, r, events.PayRefundFailed)
	return nil
}

// markNeedsReview parks a submitted refund for a person — credited only part,
// replaced (credited 0), or read some other unexpected way (credited 0) — ALERTs
// review's name and tells the owner. Its amount stays held against the cap. A
// refund already in needs_review stays as it is (see alertContradicted).
func (s *PayRefundService) markNeedsReview(ctx context.Context, r *models.PayRefund, creditedMicro int64, why string, review payRefundReview) error {
	if r.Status != models.PayRefundSubmitted {
		s.alertContradicted(r, why)
		return nil
	}
	res := s.db.WithContext(ctx).Model(&models.PayRefund{}).Where("id = ? AND status = ?", r.ID, models.PayRefundSubmitted).
		Updates(map[string]any{"status": models.PayRefundNeedsReview, "credited_micro_usd": creditedMicro, "failure_reason": why})
	if res.Error != nil || res.RowsAffected != 1 {
		return res.Error
	}
	r.Status, r.CreditedMicroUSD, r.FailureReason = models.PayRefundNeedsReview, creditedMicro, why
	log.Printf("[tsa-pay] ALERT %s=1 refund=%s payment=%s credited=%s amount=%s chain=%d tx=%s",
		review.alert(), r.ID, r.PaymentID, FormatMicroUSD(creditedMicro), FormatMicroUSD(r.AmountMicroUSD), r.ChainID, *r.TxHash)
	s.announceReview(ctx, r, review)
	return nil
}

// alertContradicted: a re-check of a needs_review refund read something other
// than a full credit. It already moved money, or may have, so nothing changes —
// its amount stays held and the owner is not told again — and support is
// ALERTed with the new reading. The very reading it was parked on is no
// contradiction, except "replaced" (preflight P8): whether the replacing
// transaction paid is still open, so each look says so again.
func (s *PayRefundService) alertContradicted(r *models.PayRefund, reading string) {
	if r.Status == models.PayRefundNeedsReview && reading == r.FailureReason && reading != payRefundReplacedReason {
		return
	}
	log.Printf("[tsa-pay] ALERT pay_refund_review_contradicted=1 refund=%s payment=%s status=%s reason=%q reading=%q tx=%s",
		r.ID, r.PaymentID, r.Status, r.FailureReason, reading, *r.TxHash)
}

// alertUnconfirmed ALERTs once per process for a refund still unproven an
// hour after it was sent. It is never failed for that.
func (s *PayRefundService) alertUnconfirmed(r *models.PayRefund) {
	if r.SubmittedAt == nil || s.now().Sub(*r.SubmittedAt) < payRefundUnconfirmedAfter {
		return
	}
	if !s.alertOnce(r.ID, "pay_refund_unconfirmed") {
		return
	}
	log.Printf("[tsa-pay] ALERT pay_refund_unconfirmed=1 refund=%s payment=%s chain=%d tx=%s submitted_at=%s last_broadcast_error=%q",
		r.ID, r.PaymentID, r.ChainID, *r.TxHash, r.SubmittedAt.Format(time.RFC3339), r.LastBroadcastError)
}

// payRefundAlertKey is one ALERT about one refund.
type payRefundAlertKey struct {
	refund uuid.UUID
	alert  string
}

// alertOnce reports whether this process has not yet raised alert for the
// refund, and marks it raised.
func (s *PayRefundService) alertOnce(refund uuid.UUID, alert string) bool {
	_, seen := s.alerted.LoadOrStore(payRefundAlertKey{refund, alert}, s.now())
	return !seen
}

// pruneAlerted drops alertOnce's memory of anything older than payAlertedTTL,
// so the map a long-lived process holds does not grow forever. An ALERT about
// a refund still stuck past the TTL is raised again, on purpose.
func (s *PayRefundService) pruneAlerted() {
	cutoff := s.now().Add(-payAlertedTTL)
	s.alerted.Range(func(key, value any) bool {
		if at, ok := value.(time.Time); !ok || at.Before(cutoff) {
			s.alerted.Delete(key)
		}
		return true
	})
}
