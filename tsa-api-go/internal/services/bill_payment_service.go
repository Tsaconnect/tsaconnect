package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

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
	HasOperator() bool
}

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
	// broadcast, so a retry can never open a second debit.
	var existing models.BillPayment
	err := s.db.WithContext(ctx).Where("order_id = ?", in.OrderID).First(&existing).Error
	if err == nil {
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
		s.fail(ctx, pay, "debit broadcast: "+err.Error())
		return nil, err
	}
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
// broadcast but was never confirmed, or a vend that never reported back. The
// MCGP is not lost: it is on-chain in pendingSettle, waiting for a settle or a
// reverse. Surfacing them is what stops that becoming permanent.
func (s *BillPaymentService) StuckPayments(ctx context.Context, limit int) ([]models.BillPayment, error) {
	if !s.IsEnabled() {
		return nil, ErrBillPayDisabled
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []models.BillPayment
	err := s.db.WithContext(ctx).
		Where("status IN ?", []string{models.BillPayStatusDebiting, models.BillPayStatusDebited}).
		Order("created_at asc").
		Limit(limit).
		Find(&out).Error
	return out, err
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
