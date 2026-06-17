// pm_withdrawal_test.go — TDD tests for PMAutomationService.WithdrawToSettlement.
// Tests must fail (compilation or runtime) before the implementation is added.

package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// ─── WithdrawToSettlement tests ───────────────────────────────────────────────

// TestWithdrawToSettlement_Success verifies:
//   - WithdrawNGN debits available_ngn atomically
//   - InitiatePayout is called with SubAccountID = pm.NombaSubAccountID and
//     the settlement bank details
//   - The payout id is returned
func TestWithdrawToSettlement_Success(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	pmSvc := NewPMService(db)

	// Seed a PM with a sub-account, settlement bank, and enough balance.
	pm := &models.PaymentMerchant{
		ID:                      uuid.New(),
		UserID:                  uuid.New(),
		Name:                    "Acme PM",
		Status:                  models.PaymentMerchantStatusActive,
		NombaSubAccountID:       "sub-acct-withdraw-1",
		SettlementBankCode:      "044",
		SettlementAccountNumber: "0123456789",
		SettlementAccountName:   "ACME CORP",
		AvailableNGN:            "5000.00",
		ReservedNGN:             "1000.00",
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed PM: %v", err)
	}

	gw := &fakeNombaGateway{payoutID: "po_withdraw_ok"}
	signer := &fakeReleaseSigner{addr: "0x0", hash: "0x0"}
	auto := NewPMAutomationService(db, pmSvc, nil, signer, nameMap(nil), gw, "TSA")

	// Withdraw 2000.00 — free balance is 5000-1000=4000 > 2000; should succeed.
	payoutID, err := auto.WithdrawToSettlement(context.Background(), pm.ID, "2000.00")
	if err != nil {
		t.Fatalf("WithdrawToSettlement: unexpected error: %v", err)
	}
	if payoutID != "po_withdraw_ok" {
		t.Errorf("payoutID = %q, want po_withdraw_ok", payoutID)
	}

	// Verify gateway was called with the correct sub-account and bank details.
	if len(gw.calls) != 1 {
		t.Fatalf("gateway calls = %d, want 1", len(gw.calls))
	}
	call := gw.calls[0]
	if call.SubAccountID != "sub-acct-withdraw-1" {
		t.Errorf("SubAccountID = %q, want sub-acct-withdraw-1", call.SubAccountID)
	}
	if call.BankCode != "044" {
		t.Errorf("BankCode = %q, want 044", call.BankCode)
	}
	if call.AccountNumber != "0123456789" {
		t.Errorf("AccountNumber = %q, want 0123456789", call.AccountNumber)
	}
	if call.AccountName != "ACME CORP" {
		t.Errorf("AccountName = %q, want ACME CORP", call.AccountName)
	}
	if call.AmountNGN != "2000.00" {
		t.Errorf("AmountNGN = %q, want 2000.00", call.AmountNGN)
	}
	if call.SenderName != "Acme PM" {
		t.Errorf("SenderName = %q, want Acme PM", call.SenderName)
	}

	// Verify available_ngn was debited (5000.00 - 2000.00 = 3000.00).
	var updated models.PaymentMerchant
	if err := db.First(&updated, "id = ?", pm.ID).Error; err != nil {
		t.Fatalf("reload PM: %v", err)
	}
	koboGot, ok := ngnToKobo(updated.AvailableNGN)
	if !ok {
		t.Fatalf("parse available_ngn %q", updated.AvailableNGN)
	}
	koboWant, _ := ngnToKobo("3000.00")
	if koboGot != koboWant {
		t.Errorf("available_ngn = %q, want ~3000.00 (kobo got=%d want=%d)", updated.AvailableNGN, koboGot, koboWant)
	}
}

// TestWithdrawToSettlement_Insufficient verifies:
//   - When amount > (available - reserved), ErrInsufficientWithdrawable is returned
//   - The gateway is NOT called
//   - The balance is unchanged
func TestWithdrawToSettlement_Insufficient(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	pmSvc := NewPMService(db)

	pm := &models.PaymentMerchant{
		ID:                      uuid.New(),
		UserID:                  uuid.New(),
		Name:                    "Acme PM",
		Status:                  models.PaymentMerchantStatusActive,
		NombaSubAccountID:       "sub-acct-insuf",
		SettlementBankCode:      "044",
		SettlementAccountNumber: "0123456789",
		SettlementAccountName:   "ACME CORP",
		AvailableNGN:            "3000.00",
		ReservedNGN:             "1500.00", // free = 1500.00
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed PM: %v", err)
	}

	gw := &fakeNombaGateway{payoutID: "should-not-be-called"}
	signer := &fakeReleaseSigner{addr: "0x0", hash: "0x0"}
	auto := NewPMAutomationService(db, pmSvc, nil, signer, nameMap(nil), gw, "TSA")

	// Attempt to withdraw 2000.00 — exceeds free balance of 1500.00.
	_, err := auto.WithdrawToSettlement(context.Background(), pm.ID, "2000.00")
	if !errors.Is(err, ErrInsufficientWithdrawable) {
		t.Fatalf("expected ErrInsufficientWithdrawable, got %v", err)
	}

	// Gateway must NOT have been called.
	if len(gw.calls) != 0 {
		t.Errorf("gateway should not be called; got %d calls", len(gw.calls))
	}

	// Balance must be unchanged.
	var updated models.PaymentMerchant
	if err2 := db.First(&updated, "id = ?", pm.ID).Error; err2 != nil {
		t.Fatalf("reload PM: %v", err2)
	}
	koboGot, ok := ngnToKobo(updated.AvailableNGN)
	if !ok {
		t.Fatalf("parse available_ngn %q", updated.AvailableNGN)
	}
	koboWant, _ := ngnToKobo("3000.00")
	if koboGot != koboWant {
		t.Errorf("available_ngn changed; got kobo=%d, want %d (3000.00)", koboGot, koboWant)
	}
}

// TestWithdrawToSettlement_GatewayFailureCompensates verifies:
//   - When the gateway returns an error AFTER WithdrawNGN succeeded, CreditNGN
//     restores the debited amount (available_ngn is back to its original value)
//   - The wrapped gateway error is returned
func TestWithdrawToSettlement_GatewayFailureCompensates(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	pmSvc := NewPMService(db)

	pm := &models.PaymentMerchant{
		ID:                      uuid.New(),
		UserID:                  uuid.New(),
		Name:                    "Acme PM",
		Status:                  models.PaymentMerchantStatusActive,
		NombaSubAccountID:       "sub-acct-gw-fail",
		SettlementBankCode:      "044",
		SettlementAccountNumber: "0123456789",
		SettlementAccountName:   "ACME CORP",
		AvailableNGN:            "4000.00",
		ReservedNGN:             "0",
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed PM: %v", err)
	}

	gwErr := errors.New("nomba rpc down")
	gw := &fakeNombaGateway{err: gwErr}
	signer := &fakeReleaseSigner{addr: "0x0", hash: "0x0"}
	auto := NewPMAutomationService(db, pmSvc, nil, signer, nameMap(nil), gw, "TSA")

	_, err := auto.WithdrawToSettlement(context.Background(), pm.ID, "1000.00")
	if err == nil {
		t.Fatalf("expected error from gateway failure, got nil")
	}

	// Balance must be compensated back to 4000.00.
	var updated models.PaymentMerchant
	if err2 := db.First(&updated, "id = ?", pm.ID).Error; err2 != nil {
		t.Fatalf("reload PM: %v", err2)
	}
	koboGot, ok := ngnToKobo(updated.AvailableNGN)
	if !ok {
		t.Fatalf("parse available_ngn %q", updated.AvailableNGN)
	}
	koboWant, _ := ngnToKobo("4000.00")
	if koboGot != koboWant {
		t.Errorf("available_ngn not compensated; got kobo=%d, want %d (4000.00)", koboGot, koboWant)
	}
}

// TestWithdrawToSettlement_MissingSubAccount verifies that a PM with no
// NombaSubAccountID returns a clear error and does NOT debit the balance.
func TestWithdrawToSettlement_MissingSubAccount(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	pmSvc := NewPMService(db)

	pm := &models.PaymentMerchant{
		ID:                      uuid.New(),
		UserID:                  uuid.New(),
		Name:                    "Unconfigured PM",
		Status:                  models.PaymentMerchantStatusActive,
		NombaSubAccountID:       "", // no sub-account configured
		SettlementBankCode:      "044",
		SettlementAccountNumber: "0123456789",
		SettlementAccountName:   "ACME CORP",
		AvailableNGN:            "5000.00",
		ReservedNGN:             "0",
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed PM: %v", err)
	}

	gw := &fakeNombaGateway{payoutID: "should-not-be-called"}
	signer := &fakeReleaseSigner{addr: "0x0", hash: "0x0"}
	auto := NewPMAutomationService(db, pmSvc, nil, signer, nameMap(nil), gw, "TSA")

	_, err := auto.WithdrawToSettlement(context.Background(), pm.ID, "1000.00")
	if err == nil {
		t.Fatalf("expected error for missing sub-account, got nil")
	}

	// Gateway must NOT have been called.
	if len(gw.calls) != 0 {
		t.Errorf("gateway should not be called for unconfigured PM; got %d calls", len(gw.calls))
	}

	// Balance must be untouched.
	var updated models.PaymentMerchant
	if err2 := db.First(&updated, "id = ?", pm.ID).Error; err2 != nil {
		t.Fatalf("reload PM: %v", err2)
	}
	koboGot, ok := ngnToKobo(updated.AvailableNGN)
	if !ok {
		t.Fatalf("parse available_ngn %q", updated.AvailableNGN)
	}
	koboWant, _ := ngnToKobo("5000.00")
	if koboGot != koboWant {
		t.Errorf("available_ngn changed; got kobo=%d, want %d (5000.00)", koboGot, koboWant)
	}
}
