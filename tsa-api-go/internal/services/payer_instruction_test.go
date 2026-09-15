package services

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The wording is spelled out in full, on purpose.
//
// Comparing against the constants would pass no matter what they said, and this
// text is the mitigation the accepted partial-match risk rests on. Changing it
// has to be a deliberate edit here as well, where a reviewer sees the new words
// next to the old. Owner-approved copy, 2026-09-15.
const (
	wantPayerInstructionNamed = "Pay from a bank account named Ada Okafor. " +
		"Payments from other accounts can be delayed for review or credited to the wrong person. " +
		"Send the exact amount shown. " +
		"The account name shows as TSA CONNECT LTD. This is TSA Connect's registered company account."
	wantPayerInstructionGeneric = "Pay from a bank account in your own name. " +
		"Payments from other accounts can be delayed for review or credited to the wrong person. " +
		"Send the exact amount shown. " +
		"The account name shows as TSA CONNECT LTD. This is TSA Connect's registered company account."
)

func TestPayerInstruction_Wording_NamesTheVerifiedPayer(t *testing.T) {
	got := testCentralAccount().PayerInstruction("Ada Okafor")
	if got != wantPayerInstructionNamed {
		t.Fatalf("PayerInstruction(name) =\n  %q\nwant\n  %q", got, wantPayerInstructionNamed)
	}
}

// No stored legal name — rare, since money-in already refuses a verified user
// without one — must still warn, just without a name to quote.
func TestPayerInstruction_Wording_FallsBackWithoutAName(t *testing.T) {
	for _, name := range []string{"", "   ", "\n\t"} {
		if got := testCentralAccount().PayerInstruction(name); got != wantPayerInstructionGeneric {
			t.Fatalf("PayerInstruction(%q) =\n  %q\nwant\n  %q", name, got, wantPayerInstructionGeneric)
		}
	}
}

// A provider-supplied name with stray whitespace or a newline renders as one
// clean line rather than breaking the sentence.
func TestPayerInstruction_NormalisesTheName(t *testing.T) {
	got := testCentralAccount().PayerInstruction("  ADA \n  OKAFOR ")
	if !strings.HasPrefix(got, "Pay from a bank account named ADA OKAFOR. ") {
		t.Fatalf("name not normalised: %q", got)
	}
}

// The account name is interpolated from config, never a hardcoded company name.
func TestPayerInstruction_NamesTheActualAccount(t *testing.T) {
	acct := testCentralAccount()
	acct.AccountName = "  SOME OTHER LTD  "

	got := acct.PayerInstruction("")

	if !strings.Contains(got, "The account name shows as SOME OTHER LTD. This is") {
		t.Fatalf("instruction does not name the account it is shown beside: %q", got)
	}
}

func TestVerifiedLegalNameOf(t *testing.T) {
	s, userID := newStableBuyDBService(t)
	if got := VerifiedLegalNameOf(context.Background(), s.db, userID); got != "Ada Okafor" {
		t.Fatalf("VerifiedLegalNameOf(verified user) = %q, want %q", got, "Ada Okafor")
	}
	if got := VerifiedLegalNameOf(context.Background(), s.db, uuid.New()); got != "" {
		t.Fatalf("VerifiedLegalNameOf(unknown user) = %q, want empty", got)
	}
	if got := VerifiedLegalNameOf(context.Background(), nil, userID); got != "" {
		t.Fatalf("VerifiedLegalNameOf(nil db) = %q, want empty", got)
	}
}

func TestCreateStableBuyIntent_CentralModeCarriesThePayerInstruction(t *testing.T) {
	s, userID := newCentralStableBuyService(t)

	_, pay, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// The fixture user is verified as "Ada Okafor" (seedVerifiedUser).
	if pay.PayerInstruction != wantPayerInstructionNamed {
		t.Fatalf("PayerInstruction = %q, want %q", pay.PayerInstruction, wantPayerInstructionNamed)
	}
}

// In VA mode the account already bears the payer's own name, so every sentence
// of the instruction is false there.
func TestCreateStableBuyIntent_VAModeCarriesNoPayerInstruction(t *testing.T) {
	s, userID := newStableBuyDBService(t)

	_, pay, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if pay.PayerInstruction != "" {
		t.Fatalf("PayerInstruction = %q in VA mode, want empty", pay.PayerInstruction)
	}
}
