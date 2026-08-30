package services

import (
	"context"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// These tests drive the fork through AutoCreditFromVA itself, not the inner
// settleStableBuyFromCharge directly. That distinction matters: the units bug
// (naira decimal string vs kobo) and the C1 redelivery regression (the
// pre-fork idempotency switch returning early before ever reaching the fork)
// were both invisible to tests that called the inner function with
// pre-converted minor units — they only show up by driving the same entry
// point Flutterwave's webhook actually calls.

// The webhook amount is a naira decimal string ("165025.00"), not kobo. The
// conversion happens at the AutoCreditFromVA boundary before the fork calls
// settleStableBuyFromCharge; assert what actually lands on the intent is in
// minor units, not the raw naira string misread as kobo.
func TestAutoCreditFromVA_ConvertsNairaToMinorUnitsForStableBuy(t *testing.T) {
	s, user, va := newVATestService(t)
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1650.25})
	ctx := context.Background()

	intent, _, err := s.CreateStableBuyIntent(ctx, user.ID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := s.AutoCreditFromVA(ctx, va.FwCustomerID, va.Reference, "chg_va_stable", "165025.00"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.ReceivedAmount != "16502500" {
		t.Fatalf("received = %q, want 16502500 kobo — the naira string parsed as minor units directly", got.ReceivedAmount)
	}
	if got.PaymentRef != "chg_va_stable" {
		t.Fatalf("payment ref = %q, want the charge id", got.PaymentRef)
	}
	if got.Status != models.ITStatusPaymentReceived {
		t.Fatalf("status = %q, want payment_received", got.Status)
	}
}

// A stablecoin buy needs no on-chain MCGP credit, so it must claim the charge
// even for a user who never registered users.wallet_address — the fork runs
// before that guard specifically so this case does not error and re-drive.
func TestAutoCreditFromVA_ClaimsStableBuyWithNoWallet(t *testing.T) {
	s, user, va := newVATestService(t)
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1650.25})
	ctx := context.Background()
	if err := s.db.Exec(`UPDATE users SET wallet_address = '' WHERE id = ?`, user.ID).Error; err != nil {
		t.Fatalf("clear wallet: %v", err)
	}

	if _, _, err := s.CreateStableBuyIntent(ctx, user.ID, testBuyQuote(), "POLYGON", testBuyEVM); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := s.AutoCreditFromVA(ctx, va.FwCustomerID, va.Reference, "chg_no_wallet", "165025.00"); err != nil {
		t.Fatalf("walletless stablecoin buyer errored: %v", err)
	}
}

// C1 regression: a crash between claiming a charge and pricing it must be
// completed by a REDELIVERY OF THE WEBHOOK — i.e. a second AutoCreditFromVA
// call for the same charge — not merely by a direct call to
// settleStableBuyFromCharge. The pre-fork idempotency switch previously
// returned nil for `payment_received` before ever reaching the fork, which
// made the unpriced intent permanently stuck despite Flutterwave retrying.
func TestAutoCreditFromVA_RedeliveryCompletesUnfinishedStablecoinPricing(t *testing.T) {
	s, user, va := newVATestService(t)
	s.SetRateQuoter(&buyRateQuoter{ngnPerUnit: 1650.25})
	ctx := context.Background()

	intent, _, err := s.CreateStableBuyIntent(ctx, user.ID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Simulate the crash: claimed by a first delivery, never priced.
	if err := s.db.Model(intent).Updates(map[string]any{
		"status":          models.ITStatusPaymentReceived,
		"payment_ref":     "chg_crash_va",
		"received_amount": "16502500",
		"repriced":        false,
	}).Error; err != nil {
		t.Fatalf("simulate crash: %v", err)
	}

	if err := s.AutoCreditFromVA(ctx, va.FwCustomerID, va.Reference, "chg_crash_va", "165025.00"); err != nil {
		t.Fatalf("redelivery errored: %v", err)
	}

	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if !got.Repriced {
		t.Fatal("redelivery via AutoCreditFromVA left the intent unpriced — the pre-fork switch swallowed it")
	}
	if got.AssetAmount != "98450000" {
		t.Fatalf("asset amount = %q, want the priced figure", got.AssetAmount)
	}
}
