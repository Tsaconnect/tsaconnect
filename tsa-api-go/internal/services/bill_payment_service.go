package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// BillPaymentService owns the MCGP leg of a utility-bill purchase, and nothing
// else. It does not know what a bill is, which provider vends it, or whether the
// thing bought was airtime or a meter token — the caller does the off-chain work
// between Debit and Settle/Reverse.
//
// WHY THE OPERATOR PATH RATHER THAN A USER-SIGNED SPEND.
//
// A bill can fail to vend, so the payment needs a reversal. debitSpendable →
// settleDebit / reverseDebit is a three-state machine that maps exactly onto that
// lifecycle, and a reversal is a pure ledger operation: no second transfer, no
// collector key, no gas beyond the operator's own transaction. A user-signed
// `spend` has no reversal at all — refunding a failed vend would mean sending
// MCGP back in a fresh transaction with its own failure modes. It is also
// operator-signed end to end, so the user needs no gas and signs nothing, which
// removes the blind-signing exposure rather than mitigating it.
//
// Dormant-by-default: with no operator signer wired, IsEnabled() is false and
// every call returns ErrBillPayDisabled.
var (
	ErrBillPayDisabled     = errors.New("MCGP bill payments are not configured")
	ErrBillPayNotFound     = errors.New("bill payment not found")
	ErrBillPayBadState     = errors.New("bill payment is not in a resolvable state")
	ErrBillPayInsufficient = errors.New("not enough spendable MCGP for this bill")
)

// billPaySwap is the slice of *MCGPSwapService this service needs, declared on
// the consumer side so the orchestration is testable without a chain.
type billPaySwap interface {
	OperatorDebitSpendable(ctx context.Context, user string, amount, maxFromBought *big.Int, debitRef [32]byte) (string, error)
	OperatorSettleDebit(ctx context.Context, debitRef [32]byte) (string, error)
	OperatorReverseDebit(ctx context.Context, debitRef [32]byte) (string, error)
	VerifyOperatorEvent(eventName, txHash string, user common.Address, ref [32]byte) (*big.Int, error)
	PremiumBalance(user string) (*big.Int, error)
	DebitFromBought(debitRef [32]byte) (*big.Int, error)
	DebitRecord(ctx context.Context, debitRef [32]byte) (DebitRecord, error)
	HasOperator() bool
}

// billDebitAbandonAfter is how long a `debiting` payment with no on-chain debit
// is given to mine before it is marked failed. A debit that lands later is
// still adopted (AdoptLateDebit) and resolved by the reconciler.
const billDebitAbandonAfter = 30 * time.Minute

type BillPaymentService struct {
	db   *gorm.DB
	swap billPaySwap
}

func NewBillPaymentService(db *gorm.DB, swap billPaySwap) *BillPaymentService {
	return &BillPaymentService{db: db, swap: swap}
}

func (s *BillPaymentService) IsEnabled() bool {
	return s != nil && s.db != nil && s.swap != nil && s.swap.HasOperator()
}

// billDebitRef derives the on-chain idempotency key from the ORDER id, not a
// random value and not the payment row id.
//
// The chain rejects a duplicate ref, so deriving it from the order is what makes
// a retried request safe: two attempts at the same order produce the same ref and
// the second reverts instead of debiting twice. A per-attempt ref would make the
// on-chain guard useless, which is the same defect the peer-pay path had.
func billDebitRef(orderID uuid.UUID) [32]byte {
	h := crypto.Keccak256Hash([]byte("mcgp-bill:" + orderID.String()))
	var out [32]byte
	copy(out[:], h.Bytes())
	return out
}

// DebitInput is one bill's payment leg.
type DebitInput struct {
	UserID        uuid.UUID
	OrderID       uuid.UUID
	WalletAddress string
	McgpAmount    *big.Int // 18dp base units
	NgnKobo       *big.Int // what the bill costs
	RateKobo      *big.Int // ₦-per-MCGP used for the quote
	// AllowBought caps how much may come out of the user's self-exitable
	// balance. Bills pass the full amount — paying a bill with MCGP you bought is
	// the point — but it is passed explicitly because the contract makes reaching
	// bought premium opt-in per call.
	AllowBought *big.Int
}

// Debit moves the user's premium into pendingSettle and records the payment.
//
// Idempotent per order: an existing row for the order is returned as-is rather
// than debiting again.
func (s *BillPaymentService) Debit(ctx context.Context, in DebitInput) (*models.BillPayment, error) {
	if !s.IsEnabled() {
		return nil, ErrBillPayDisabled
	}
	if in.McgpAmount == nil || in.McgpAmount.Sign() <= 0 {
		return nil, fmt.Errorf("bill payment: amount must be > 0")
	}
	wallet := strings.TrimSpace(in.WalletAddress)
	if wallet == "" {
		return nil, fmt.Errorf("bill payment: user has no wallet")
	}

	// Return an existing attempt untouched. Checked before anything is
	// broadcast, so a retry can never open a second debit. A FAILED attempt is
	// not success and must never be replayed as one — returning it nil-error
	// would let the caller treat an unpaid order as paid (free vend). The ref
	// is derived from the order id, so the safe retry path is a new order.
	var existing models.BillPayment
	err := s.db.WithContext(ctx).Where("order_id = ?", in.OrderID).First(&existing).Error
	if err == nil {
		if existing.Status == models.BillPayStatusFailed {
			return nil, ErrBillPayBadState
		}
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Fail early with a clear message rather than letting the chain revert.
	balance, err := s.swap.PremiumBalance(wallet)
	if err != nil {
		return nil, fmt.Errorf("bill payment: read balance: %w", err)
	}
	if balance.Cmp(in.McgpAmount) < 0 {
		return nil, ErrBillPayInsufficient
	}

	allowBought := in.AllowBought
	if allowBought == nil {
		allowBought = new(big.Int)
	}

	ref := billDebitRef(in.OrderID)
	pay := &models.BillPayment{
		ID:            uuid.New(),
		UserID:        in.UserID,
		OrderID:       in.OrderID,
		WalletAddress: wallet,
		McgpAmount:    in.McgpAmount.String(),
		NgnKobo:       bigOrZero(in.NgnKobo),
		RateKobo:      bigOrZero(in.RateKobo),
		DebitRef:      common.BytesToHash(ref[:]).Hex(),
		Status:        models.BillPayStatusDebiting,
	}
	// Persist BEFORE broadcasting. If the process dies mid-flight the row is the
	// only evidence a debit may exist on-chain; without it a reconciler has
	// nothing to look for.
	if err := s.db.WithContext(ctx).Create(pay).Error; err != nil {
		return nil, err
	}

	txHash, err := s.swap.OperatorDebitSpendable(ctx, wallet, in.McgpAmount, allowBought, ref)
	if err != nil {
		if errors.Is(err, ErrTxNotBroadcast) {
			s.fail(ctx, pay, "debit broadcast: "+err.Error())
			return nil, err
		}
		// The send failed or timed out after the transaction may have left:
		// only chain state can say whether the debit exists (ResolveDebiting).
		s.setStatus(ctx, pay, models.BillPayStatusDebiting, "debit broadcast unknown: "+err.Error())
		return nil, err
	}
	// Keep the hash before verifying — if verification never returns it is the
	// operator's pointer to the transaction.
	pay.TxHash = txHash
	s.setStatus(ctx, pay, models.BillPayStatusDebiting, "")
	if _, err := s.swap.VerifyOperatorEvent("PremiumDebitPending", txHash, common.HexToAddress(wallet), ref); err != nil {
		// Broadcast but unverified: the debit may still be on-chain, so this is
		// left for the reconciler rather than marked failed.
		s.setStatus(ctx, pay, models.BillPayStatusDebiting, "debit verify: "+err.Error())
		return nil, err
	}

	// Record how much came from the self-exitable bucket. Best-effort: the debit
	// already succeeded, and a failed read must not fail the payment.
	if fromBought, ferr := s.swap.DebitFromBought(ref); ferr == nil {
		pay.FromBought = fromBought.String()
	}
	pay.TxHash = txHash
	pay.Status = models.BillPayStatusDebited
	if err := s.db.WithContext(ctx).Save(pay).Error; err != nil {
		return nil, err
	}
	return pay, nil
}

// Settle finalises a payment whose vend succeeded, freeing the backing.
func (s *BillPaymentService) Settle(ctx context.Context, orderID uuid.UUID) (*models.BillPayment, error) {
	return s.resolve(ctx, orderID, true)
}

// Reverse restores the user after a vend failed. The contract returns each
// bucket exactly as drawn, so a bill that fails cannot downgrade bought premium
// to spend-only.
func (s *BillPaymentService) Reverse(ctx context.Context, orderID uuid.UUID) (*models.BillPayment, error) {
	return s.resolve(ctx, orderID, false)
}

func (s *BillPaymentService) resolve(ctx context.Context, orderID uuid.UUID, settle bool) (*models.BillPayment, error) {
	if !s.IsEnabled() {
		return nil, ErrBillPayDisabled
	}
	var pay models.BillPayment
	if err := s.db.WithContext(ctx).Where("order_id = ?", orderID).First(&pay).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBillPayNotFound
		}
		return nil, err
	}

	target := models.BillPayStatusReversed
	if settle {
		target = models.BillPayStatusSettled
	}
	if pay.Status == target {
		return &pay, nil // idempotent
	}
	if pay.Status != models.BillPayStatusDebited {
		return nil, ErrBillPayBadState
	}

	ref := billDebitRef(orderID)
	var txHash string
	var err error
	event := "PremiumDebitReversed"
	if settle {
		txHash, err = s.swap.OperatorSettleDebit(ctx, ref)
		event = "PremiumDebitSettled"
	} else {
		txHash, err = s.swap.OperatorReverseDebit(ctx, ref)
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.swap.VerifyOperatorEvent(event, txHash, common.HexToAddress(pay.WalletAddress), ref); err != nil {
		return nil, err
	}

	pay.ResolveTxHash = txHash
	pay.Status = target
	if err := s.db.WithContext(ctx).Save(&pay).Error; err != nil {
		return nil, err
	}
	return &pay, nil
}

// StuckPayments lists rows that never reached a terminal state — a debit that
// broadcast but was never confirmed, or a vend that never reported back — and
// have not moved for olderThan. The MCGP is not lost: it is on-chain in
// pendingSettle (or not debited at all), waiting for a settle, a reverse or a
// chain check. Surfacing them is what stops that becoming permanent.
func (s *BillPaymentService) StuckPayments(ctx context.Context, olderThan time.Duration, limit int) ([]models.BillPayment, error) {
	if !s.IsEnabled() {
		return nil, ErrBillPayDisabled
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []models.BillPayment
	err := s.db.WithContext(ctx).
		Where("status IN ? AND updated_at <= ?",
			[]string{models.BillPayStatusDebiting, models.BillPayStatusDebited}, time.Now().Add(-olderThan)).
		Order("created_at asc").
		Limit(limit).
		Find(&out).Error
	return out, err
}

// ResolveDebiting settles a `debiting` payment's uncertainty from the swap
// contract's debit record for the order's ref, and returns the payment's
// status afterwards plus whether THIS call made the transition:
//
//	debit Pending for this wallet+amount → debited
//	no debit, and the row is older than billDebitAbandonAfter → failed
//	anything else (too fresh, resolved, mismatched, read error) → unchanged
//
// Only positive chain evidence moves the row, and only the caller that won
// the guarded transition may act on it (vend, fail the order): a concurrent
// caller sees won=false. A payment not in `debiting` is returned as-is.
func (s *BillPaymentService) ResolveDebiting(ctx context.Context, orderID uuid.UUID) (string, bool, error) {
	if !s.IsEnabled() {
		return "", false, ErrBillPayDisabled
	}
	pay, err := s.paymentForOrder(ctx, orderID)
	if err != nil {
		return "", false, err
	}
	if pay.Status != models.BillPayStatusDebiting {
		return pay.Status, false, nil
	}
	ref := billDebitRef(orderID)
	rec, err := s.swap.DebitRecord(ctx, ref)
	if err != nil {
		return pay.Status, false, fmt.Errorf("bill payment: read debit: %w", err)
	}
	switch rec.State {
	case DebitStatePending:
		if !billDebitMatches(rec, pay) {
			alertf("bill-mismatch:"+orderID.String(),
				"[bills] ALERT bill_debit_mismatch order=%s on-chain user=%s amount=%s payment wallet=%s amount=%s",
				orderID, rec.User.Hex(), rec.Amount, pay.WalletAddress, pay.McgpAmount)
			return pay.Status, false, nil
		}
		updates := map[string]any{"status": models.BillPayStatusDebited, "failure_reason": ""}
		if fromBought, ferr := s.swap.DebitFromBought(ref); ferr == nil {
			updates["from_bought"] = fromBought.String()
		}
		moved, err := s.transition(ctx, pay.ID, models.BillPayStatusDebiting, updates)
		if err != nil || !moved {
			return s.currentStatus(ctx, orderID, pay.Status), false, err
		}
		log.Printf("[bills] payment order=%s debit confirmed on-chain → debited", orderID)
		return models.BillPayStatusDebited, true, nil
	case DebitStateNone:
		if time.Since(pay.UpdatedAt) < billDebitAbandonAfter {
			return pay.Status, false, nil // a broadcast may still mine
		}
		reason := truncateReason("debit never landed on-chain (was: " + pay.FailureReason + ")")
		moved, err := s.transition(ctx, pay.ID, models.BillPayStatusDebiting,
			map[string]any{"status": models.BillPayStatusFailed, "failure_reason": reason})
		if err != nil || !moved {
			return s.currentStatus(ctx, orderID, pay.Status), false, err
		}
		return models.BillPayStatusFailed, true, nil
	default:
		alertf("bill-resolved:"+orderID.String(),
			"[bills] ALERT bill_debit_resolved_unrecorded order=%s — debit already settled or reversed on-chain while the payment reads debiting", orderID)
		return pay.Status, false, nil
	}
}

// AlertLateDebitsBeyondWindow audits failed payments older than the 48h
// late-debit window whose debit is nonetheless pending on-chain. Alert only:
// that far out an operator decides. Returns how many it reported.
func (s *BillPaymentService) AlertLateDebitsBeyondWindow(ctx context.Context) (int, error) {
	if !s.IsEnabled() {
		return 0, ErrBillPayDisabled
	}
	cutoff := time.Now().Add(-billLateDebitScanWindow)
	reported := 0
	var cursor reconcileCursor
	const batch = 200
	for page := 0; page < 50; page++ {
		var rows []models.BillPayment
		q := s.db.WithContext(ctx).Model(&models.BillPayment{}).
			Where("status = ? AND updated_at <= ?", models.BillPayStatusFailed, cutoff)
		if err := cursor.page("deep", "", q, batch).Find(&rows).Error; err != nil {
			return reported, err
		}
		for i := range rows {
			pay := &rows[i]
			rec, err := s.swap.DebitRecord(ctx, billDebitRef(pay.OrderID))
			if err != nil || rec.State != DebitStatePending {
				continue
			}
			reported++
			log.Printf("[bills] ALERT bill_late_debit_beyond_window order=%s failed_at=%s on-chain user=%s amount=%s — operator review required",
				pay.OrderID, pay.UpdatedAt.Format(time.RFC3339), rec.User.Hex(), rec.Amount)
		}
		if len(rows) < batch {
			return reported, nil
		}
		cursor.advance("deep", len(rows), batch, rows[len(rows)-1].UpdatedAt, rows[len(rows)-1].ID)
	}
	return reported, nil
}

// AdoptLateDebit moves a `failed` payment back to `debited` when its debit is
// in fact pending on-chain — it mined after the payment was given up on. The
// caller decides what the debit is for (settle a delivered order, reverse one
// that never vended). Reports whether a debit was adopted.
func (s *BillPaymentService) AdoptLateDebit(ctx context.Context, orderID uuid.UUID) (bool, error) {
	if !s.IsEnabled() {
		return false, ErrBillPayDisabled
	}
	pay, err := s.paymentForOrder(ctx, orderID)
	if err != nil {
		return false, err
	}
	if pay.Status != models.BillPayStatusFailed {
		return false, nil
	}
	rec, err := s.swap.DebitRecord(ctx, billDebitRef(orderID))
	if err != nil {
		return false, fmt.Errorf("bill payment: read debit: %w", err)
	}
	if rec.State != DebitStatePending {
		return false, nil
	}
	if !billDebitMatches(rec, pay) {
		alertf("bill-mismatch:"+orderID.String(),
			"[bills] ALERT bill_debit_mismatch order=%s (failed payment) on-chain user=%s amount=%s", orderID, rec.User.Hex(), rec.Amount)
		return false, nil
	}
	moved, err := s.transition(ctx, pay.ID, models.BillPayStatusFailed, map[string]any{
		"status":         models.BillPayStatusDebited,
		"failure_reason": "debit landed after the payment was marked failed",
	})
	if moved {
		log.Printf("[bills] ALERT bill_late_debit order=%s — debit mined after the payment failed; adopting", orderID)
	}
	return moved, err
}

func billDebitMatches(rec DebitRecord, pay *models.BillPayment) bool {
	amount, ok := new(big.Int).SetString(strings.TrimSpace(pay.McgpAmount), 10)
	return ok && rec.Amount != nil && rec.Amount.Cmp(amount) == 0 &&
		rec.User == common.HexToAddress(pay.WalletAddress)
}

func (s *BillPaymentService) paymentForOrder(ctx context.Context, orderID uuid.UUID) (*models.BillPayment, error) {
	var pay models.BillPayment
	if err := s.db.WithContext(ctx).Where("order_id = ?", orderID).First(&pay).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBillPayNotFound
		}
		return nil, err
	}
	return &pay, nil
}

// transition applies updates only if the payment is still in `from`, so two
// reconcilers (or a reconciler and a request) cannot both move it.
func (s *BillPaymentService) transition(ctx context.Context, id uuid.UUID, from string, updates map[string]any) (bool, error) {
	res := s.db.WithContext(ctx).Model(&models.BillPayment{}).
		Where("id = ? AND status = ?", id, from).
		Updates(updates)
	return res.RowsAffected == 1, res.Error
}

func (s *BillPaymentService) currentStatus(ctx context.Context, orderID uuid.UUID, fallback string) string {
	if pay, err := s.paymentForOrder(ctx, orderID); err == nil {
		return pay.Status
	}
	return fallback
}

func (s *BillPaymentService) setStatus(ctx context.Context, pay *models.BillPayment, status, reason string) {
	pay.Status = status
	pay.FailureReason = truncateReason(reason) // shared with it_service (same 256 cap)
	_ = s.db.WithContext(ctx).Save(pay).Error
}

func (s *BillPaymentService) fail(ctx context.Context, pay *models.BillPayment, reason string) {
	s.setStatus(ctx, pay, models.BillPayStatusFailed, reason)
}

func bigOrZero(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
}
