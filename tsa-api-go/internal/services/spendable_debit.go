package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
)

// Shared pieces of the Spendable MCGP operator-debit path. Utility bills
// (BillPaymentService) and marketplace checkout (OrderSpendablePaymentService)
// both move a user's premium with debitSpendable → settleDebit / reverseDebit,
// so what they must agree on lives here once: the swap surface, how a ref is
// derived, how an on-chain debit is matched to our row, and how a debit whose
// broadcast outcome was lost is resolved from chain state.

// spendableDebitSwap is the slice of *MCGPSwapService an operator debit needs,
// declared on the consumer side so the orchestration is testable without a
// chain.
type spendableDebitSwap interface {
	OperatorDebitSpendable(ctx context.Context, user string, amount, maxFromBought *big.Int, debitRef [32]byte) (string, error)
	OperatorSettleDebit(ctx context.Context, debitRef [32]byte) (string, error)
	OperatorReverseDebit(ctx context.Context, debitRef [32]byte) (string, error)
	VerifyOperatorEvent(eventName, txHash string, user common.Address, ref [32]byte) (*big.Int, error)
	PremiumBalance(user string) (*big.Int, error)
	DebitFromBought(debitRef [32]byte) (*big.Int, error)
	DebitRecord(ctx context.Context, debitRef [32]byte) (DebitRecord, error)
	HasOperator() bool
}

// spendableRef derives a single-use on-chain key from a domain and a record id.
//
// Deterministic on purpose: the chain rejects a reused debit ref ("Debit
// exists") and a reused credit ref ("Ref already used"), so a retried request
// or reconciler pass that derives the same ref can never move money twice. A
// per-attempt ref would make that guard useless. The domain keeps unrelated
// flows (a bill, an order's debit, an order's seller credit) from colliding.
func spendableRef(domain string, id uuid.UUID) [32]byte {
	h := crypto.Keccak256Hash([]byte(domain + id.String()))
	var out [32]byte
	copy(out[:], h.Bytes())
	return out
}

// debitRecordMatches reports whether an on-chain debit is the one our row
// describes: same wallet, same amount. Anything else under our ref is a
// different debit and must never be adopted as ours.
func debitRecordMatches(rec DebitRecord, wallet, amount string) bool {
	want, ok := new(big.Int).SetString(strings.TrimSpace(amount), 10)
	return ok && rec.Amount != nil && rec.Amount.Cmp(want) == 0 &&
		rec.User == common.HexToAddress(wallet)
}

// debitingVerdict is what the chain says about a row still in `debiting`.
type debitingVerdict int

const (
	debitingWait     debitingVerdict = iota // too fresh, or nothing conclusive yet
	debitingLanded                          // Pending for this wallet+amount: adopt it
	debitingAbandon                         // nothing on-chain and old enough to give up
	debitingMismatch                        // a debit under our ref for someone/something else
	debitingResolved                        // already settled or reversed while we read debiting
)

// classifyDebiting turns a debit record into a verdict. Only positive chain
// evidence moves a row: "no debit yet" is as often a lagging node as a missing
// debit, so it only abandons after abandonAfter of silence.
func classifyDebiting(rec DebitRecord, wallet, amount string, idleFor, abandonAfter time.Duration) debitingVerdict {
	switch rec.State {
	case DebitStatePending:
		if !debitRecordMatches(rec, wallet, amount) {
			return debitingMismatch
		}
		return debitingLanded
	case DebitStateNone:
		if idleFor < abandonAfter {
			return debitingWait
		}
		return debitingAbandon
	default:
		return debitingResolved
	}
}

// Refusals the contract gives for a debit that can never succeed as sent.
// Each arrives while estimating gas (so the transaction was never broadcast)
// and is mapped to a clear, user-facing error.
var (
	ErrSpendableInsufficient = errors.New("not enough Spendable MCGP")
	ErrSpendableOverTxCap    = errors.New("above the per-payment limit for Spendable MCGP")
	ErrSpendableOverWindow   = errors.New("the Spendable MCGP payment limit for this period has been reached")
)

// classifyDebitRevert maps a not-broadcast debit error to one of the typed
// refusals above, or returns nil when the revert reason is not one we know.
func classifyDebitRevert(err error) error {
	if err == nil || !errors.Is(err, ErrTxNotBroadcast) {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Over per-tx debit cap"):
		return ErrSpendableOverTxCap
	case strings.Contains(msg, "Over debit window cap"):
		return ErrSpendableOverWindow
	case strings.Contains(msg, "Insufficient premium balance"),
		strings.Contains(msg, "Insufficient credited premium"),
		strings.Contains(msg, "Exceeds allowed bought draw"):
		return ErrSpendableInsufficient
	}
	return nil
}
