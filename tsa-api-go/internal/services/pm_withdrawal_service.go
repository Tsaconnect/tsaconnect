package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ErrInsufficientWithdrawable is returned by WithdrawToSettlement when the
// requested amount exceeds the PM's free (available - reserved) NGN balance.
// The handler maps this to HTTP 409 so the operator sees "insufficient
// withdrawable balance" rather than a generic 500.
var ErrInsufficientWithdrawable = errors.New("pm_withdrawal: amount exceeds withdrawable (available minus open sell reservations) balance")

// WithdrawToSettlement atomically debits amountNGN from the PM's available
// balance (guarded against open sell reservations) and initiates a Nomba
// payout from TSA's single parent account to the PM's settlement bank.
//
// VA-only model: there is no per-PM sub-account to debit — the parent account
// holds all PM funds (attributed off-chain by the available_ngn ledger), so the
// withdrawal payout comes from the parent via the gateway's parent Transfer.
//
// Preconditions:
//   - pm.SettlementBankCode + pm.SettlementAccountNumber must be configured
//
// Flow:
//  1. Load the PM via GetByID. Missing settlement bank → clear error.
//  2. WithdrawNGN debits available_ngn atomically, guarded by (available - reserved) >= amount.
//     ok=false → ErrInsufficientWithdrawable; WithdrawNGN already failed — no compensation needed.
//  3. InitiatePayout via the gateway (parent account).
//     On gateway error → CreditNGN compensates the debited balance before returning.
//  4. Return the payout id.
func (s *PMAutomationService) WithdrawToSettlement(ctx context.Context, pmID uuid.UUID, amount string) (string, error) {
	// 1. Load PM.
	pm, err := s.pm.GetByID(ctx, pmID)
	if err != nil {
		return "", fmt.Errorf("pm_withdrawal: load PM %s: %w", pmID, err)
	}

	// Validate the settlement bank is configured (VA model: no sub-account check).
	if strings.TrimSpace(pm.SettlementBankCode) == "" || strings.TrimSpace(pm.SettlementAccountNumber) == "" {
		return "", fmt.Errorf("pm_withdrawal: PM %s has no settlement bank configured; cannot withdraw", pmID)
	}

	// 2. Atomically debit the free balance (available - reserved >= amount).
	ok, err := s.pm.WithdrawNGN(ctx, pmID, amount)
	if err != nil {
		return "", fmt.Errorf("pm_withdrawal: debit PM %s: %w", pmID, err)
	}
	if !ok {
		// WithdrawNGN affected 0 rows — free balance insufficient. Nothing debited,
		// no compensation needed.
		return "", ErrInsufficientWithdrawable
	}

	// 3. Initiate payout from TSA's parent account to the PM's settlement bank.
	// Use a short unique suffix on the reference to guard idempotency at Nomba's
	// level (two withdrawals for the same PM on the same day get distinct refs).
	// Each call gets a distinct reference — Nomba idempotency is per-call, not dedup-on-amount.
	ref := "withdraw:" + pmID.String() + ":" + shortRef()
	payoutID, err := s.gateway.InitiatePayout(ctx, PayoutRequest{
		BankCode:      pm.SettlementBankCode,
		AccountNumber: pm.SettlementAccountNumber,
		AccountName:   pm.SettlementAccountName,
		AmountNGN:     amount,
		Reference:     ref,
		SenderName:    pm.Name,
	})
	if err != nil {
		// Gateway failed AFTER the balance was debited — compensate by crediting
		// back the same amount so available_ngn returns to its pre-call value.
		if cErr := s.pm.CreditNGN(ctx, pmID, amount); cErr != nil {
			// Log and wrap both errors; the balance drift is critical — it will be
			// caught by the NGN reconciler (task §6) but surface both errors so
			// ops can investigate immediately.
			return "", fmt.Errorf("pm_withdrawal: gateway error (%w) AND compensation CreditNGN failed (%v); manual reconciliation required for PM %s", err, cErr, pmID)
		}
		return "", fmt.Errorf("pm_withdrawal: initiate payout for PM %s: %w", pmID, err)
	}

	return payoutID, nil
}

// shortRef generates an 8-hex-character suffix from a new UUID (without
// import cost of crypto/rand). Callers use it to make withdrawal references
// unique across multiple withdrawals for the same PM in the same day.
func shortRef() string {
	id := uuid.New()
	// Use the first 4 bytes of the UUID (already random) as an 8-char hex string.
	return fmt.Sprintf("%08x", id[:4])
}
