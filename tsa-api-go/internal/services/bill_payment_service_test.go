package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ── double ───────────────────────────────────────────────────────────────

type fakeBillSwap struct {
	debitCalls   int
	settleCalls  int
	reverseCalls int
	lastAllow    *big.Int
	lastRef      [32]byte

	balance    *big.Int
	fromBought *big.Int
	debitErr   error
	verifyErr  error
	resolveErr error
	noOperator bool
}

func (f *fakeBillSwap) HasOperator() bool { return !f.noOperator }
func (f *fakeBillSwap) PremiumBalance(string) (*big.Int, error) {
	if f.balance == nil {
		return big.NewInt(0), nil
	}
	return f.balance, nil
}
func (f *fakeBillSwap) OperatorDebitSpendable(_ context.Context, _ string, _, maxFromBought *big.Int, ref [32]byte) (string, error) {
	f.debitCalls++
	f.lastAllow = maxFromBought
	f.lastRef = ref
	if f.debitErr != nil {
		return "", f.debitErr
	}
	return "0xdebit", nil
}
func (f *fakeBillSwap) OperatorSettleDebit(_ context.Context, _ [32]byte) (string, error) {
	f.settleCalls++
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	return "0xsettle", nil
}
func (f *fakeBillSwap) OperatorReverseDebit(_ context.Context, _ [32]byte) (string, error) {
	f.reverseCalls++
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	return "0xreverse", nil
}
func (f *fakeBillSwap) VerifyOperatorEvent(string, string, common.Address, [32]byte) (*big.Int, error) {
	if f.verifyErr != nil {
		return nil, f.verifyErr
	}
	return big.NewInt(0), nil
}
func (f *fakeBillSwap) DebitFromBought([32]byte) (*big.Int, error) {
	if f.fromBought == nil {
		return big.NewInt(0), nil
	}
	return f.fromBought, nil
}

// ── rig ──────────────────────────────────────────────────────────────────

const billWallet = "0x87c2422be9A11b71A5c530ecd156134D3b04E3cE"

func newBillRig(t *testing.T) (*BillPaymentService, *fakeBillSwap) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE bill_payments (
		id TEXT PRIMARY KEY, user_id TEXT, order_id TEXT UNIQUE, wallet_address TEXT,
		mcgp_amount TEXT, ngn_kobo TEXT, rate_kobo TEXT, from_bought TEXT,
		debit_ref TEXT UNIQUE, status TEXT, tx_hash TEXT, resolve_tx_hash TEXT,
		failure_reason TEXT, created_at DATETIME, updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	swap := &fakeBillSwap{balance: mcgpBig(1000)}
	return NewBillPaymentService(db, swap), swap
}

func mcgpBig(n int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
}

func debitInput(orderID uuid.UUID, amount int64) DebitInput {
	return DebitInput{
		UserID:        uuid.New(),
		OrderID:       orderID,
		WalletAddress: billWallet,
		McgpAmount:    mcgpBig(amount),
		NgnKobo:       big.NewInt(100_000),
		RateKobo:      big.NewInt(3_000),
		AllowBought:   mcgpBig(amount),
	}
}

// ── tests ────────────────────────────────────────────────────────────────

func TestBillPayment_DebitRecordsAndMovesPremium(t *testing.T) {
	svc, swap := newBillRig(t)
	swap.fromBought = mcgpBig(4)

	pay, err := svc.Debit(context.Background(), debitInput(uuid.New(), 10))
	if err != nil {
		t.Fatalf("debit: %v", err)
	}
	if pay.Status != models.BillPayStatusDebited {
		t.Fatalf("expected debited, got %q", pay.Status)
	}
	if swap.debitCalls != 1 {
		t.Fatalf("expected one debit, got %d", swap.debitCalls)
	}
	// Support needs to see when a bill consumed self-exitable premium.
	if pay.FromBought != mcgpBig(4).String() {
		t.Fatalf("fromBought not recorded: %q", pay.FromBought)
	}
}

// The guarantee that makes a retried request safe. A per-attempt ref would make
// the on-chain guard useless — the same defect the peer-pay path had.
func TestBillPayment_DebitIsIdempotentPerOrder(t *testing.T) {
	svc, swap := newBillRig(t)
	orderID := uuid.New()

	first, err := svc.Debit(context.Background(), debitInput(orderID, 10))
	if err != nil {
		t.Fatalf("first debit: %v", err)
	}
	second, err := svc.Debit(context.Background(), debitInput(orderID, 10))
	if err != nil {
		t.Fatalf("second debit: %v", err)
	}

	if swap.debitCalls != 1 {
		t.Fatalf("a retried order must not debit twice; got %d debits", swap.debitCalls)
	}
	if first.ID != second.ID {
		t.Fatal("expected the same payment row back")
	}
}

func TestBillPayment_DebitRefIsDerivedFromTheOrder(t *testing.T) {
	orderID := uuid.New()
	a := billDebitRef(orderID)
	b := billDebitRef(orderID)
	if a != b {
		t.Fatal("the ref must be stable for an order — otherwise a retry opens a second debit")
	}
	if billDebitRef(uuid.New()) == a {
		t.Fatal("distinct orders must not share a ref")
	}
}

func TestBillPayment_RejectsInsufficientBalanceBeforeBroadcasting(t *testing.T) {
	svc, swap := newBillRig(t)
	swap.balance = mcgpBig(5)

	_, err := svc.Debit(context.Background(), debitInput(uuid.New(), 10))
	if !errors.Is(err, ErrBillPayInsufficient) {
		t.Fatalf("expected ErrBillPayInsufficient, got %v", err)
	}
	if swap.debitCalls != 0 {
		t.Fatal("must not broadcast a debit that would revert")
	}
}

// Reaching bought premium is opt-in at the contract, so the caller has to say so.
func TestBillPayment_PassesTheBoughtAllowanceThrough(t *testing.T) {
	svc, swap := newBillRig(t)
	in := debitInput(uuid.New(), 10)
	in.AllowBought = mcgpBig(3)

	if _, err := svc.Debit(context.Background(), in); err != nil {
		t.Fatalf("debit: %v", err)
	}
	if swap.lastAllow.Cmp(mcgpBig(3)) != 0 {
		t.Fatalf("allowance not forwarded: %s", swap.lastAllow)
	}
}

func TestBillPayment_DefaultsToNoBoughtDraw(t *testing.T) {
	svc, swap := newBillRig(t)
	in := debitInput(uuid.New(), 10)
	in.AllowBought = nil // caller said nothing

	if _, err := svc.Debit(context.Background(), in); err != nil {
		t.Fatalf("debit: %v", err)
	}
	// Silence must mean "credited only", never "help yourself".
	if swap.lastAllow.Sign() != 0 {
		t.Fatalf("an unset allowance must default to zero, got %s", swap.lastAllow)
	}
}

func TestBillPayment_SettleAndReverseAreTerminalAndIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		settle bool
		want   string
		calls  func(*fakeBillSwap) int
	}{
		{"settle", true, models.BillPayStatusSettled, func(f *fakeBillSwap) int { return f.settleCalls }},
		{"reverse", false, models.BillPayStatusReversed, func(f *fakeBillSwap) int { return f.reverseCalls }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, swap := newBillRig(t)
			orderID := uuid.New()
			if _, err := svc.Debit(context.Background(), debitInput(orderID, 10)); err != nil {
				t.Fatalf("setup: %v", err)
			}

			resolve := svc.Reverse
			if tc.settle {
				resolve = svc.Settle
			}
			pay, err := resolve(context.Background(), orderID)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if pay.Status != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, pay.Status)
			}

			// A duplicate callback from the provider must not send a second tx.
			if _, err := resolve(context.Background(), orderID); err != nil {
				t.Fatalf("second resolve should be idempotent: %v", err)
			}
			if got := tc.calls(swap); got != 1 {
				t.Fatalf("expected exactly one on-chain resolve, got %d", got)
			}
		})
	}
}

func TestBillPayment_CannotSettleAfterReversing(t *testing.T) {
	svc, _ := newBillRig(t)
	orderID := uuid.New()
	if _, err := svc.Debit(context.Background(), debitInput(orderID, 10)); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := svc.Reverse(context.Background(), orderID); err != nil {
		t.Fatalf("reverse: %v", err)
	}
	// The chain would reject this too, but failing here keeps the DB honest.
	if _, err := svc.Settle(context.Background(), orderID); !errors.Is(err, ErrBillPayBadState) {
		t.Fatalf("expected ErrBillPayBadState, got %v", err)
	}
}

func TestBillPayment_ResolveUnknownOrder(t *testing.T) {
	svc, _ := newBillRig(t)
	if _, err := svc.Settle(context.Background(), uuid.New()); !errors.Is(err, ErrBillPayNotFound) {
		t.Fatalf("expected ErrBillPayNotFound, got %v", err)
	}
}

// A debit that broadcast but could not be verified is NOT a failure: the MCGP may
// well be in pendingSettle on-chain. Marking it failed would strand it.
func TestBillPayment_UnverifiedDebitStaysReconcilable(t *testing.T) {
	svc, swap := newBillRig(t)
	swap.verifyErr = fmt.Errorf("rpc timeout")
	orderID := uuid.New()

	if _, err := svc.Debit(context.Background(), debitInput(orderID, 10)); err == nil {
		t.Fatal("expected the verify failure to surface")
	}

	stuck, err := svc.StuckPayments(context.Background(), 10)
	if err != nil {
		t.Fatalf("stuck: %v", err)
	}
	if len(stuck) != 1 || stuck[0].OrderID != orderID {
		t.Fatalf("an unverified debit must remain visible for reconciliation, got %+v", stuck)
	}
	if stuck[0].Status == models.BillPayStatusFailed {
		t.Fatal("must not be marked failed — the debit may exist on-chain")
	}
}

// A debit that never broadcast is a real failure and must not look reconcilable.
func TestBillPayment_UnbroadcastDebitIsFailedNotStuck(t *testing.T) {
	svc, swap := newBillRig(t)
	swap.debitErr = fmt.Errorf("nonce too low")

	if _, err := svc.Debit(context.Background(), debitInput(uuid.New(), 10)); err == nil {
		t.Fatal("expected the broadcast failure to surface")
	}
	stuck, err := svc.StuckPayments(context.Background(), 10)
	if err != nil {
		t.Fatalf("stuck: %v", err)
	}
	if len(stuck) != 0 {
		t.Fatalf("a debit that never broadcast is not stuck, got %+v", stuck)
	}
}

func TestBillPayment_DormantWithoutAnOperator(t *testing.T) {
	svc, swap := newBillRig(t)
	swap.noOperator = true

	if svc.IsEnabled() {
		t.Fatal("no operator signer means dormant")
	}
	if _, err := svc.Debit(context.Background(), debitInput(uuid.New(), 10)); !errors.Is(err, ErrBillPayDisabled) {
		t.Fatalf("expected ErrBillPayDisabled, got %v", err)
	}
	if swap.debitCalls != 0 {
		t.Fatal("dormant must not broadcast")
	}
}
