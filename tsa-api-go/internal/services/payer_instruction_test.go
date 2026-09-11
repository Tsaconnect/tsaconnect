package services

import (
	"context"
	"strings"
	"testing"
)

// wantPayerInstruction is the wording spelled out in full, on purpose.
//
// Comparing against the constant would pass no matter what the constant said,
// and this text is the only thing standing between the accepted partial-match
// risk and a credit landing on a stranger. Changing it has to be a deliberate
// edit here as well, where a reviewer sees the new words next to the old.
const wantPayerInstruction = "Pay from a bank account in your own name. " +
	"We match your transfer by that name, so money sent from someone else's account cannot reach you. " +
	"Send the exact amount shown. " +
	"The account name reads TSA CONNECT LTD, not TSA Connect — that is us."

func TestPayerInstruction_Wording(t *testing.T) {
	got := testCentralAccount().PayerInstruction()

	if got != wantPayerInstruction {
		t.Fatalf("PayerInstruction() =\n  %q\nwant\n  %q", got, wantPayerInstruction)
	}
	// Each clause carries one of the three things a payer has to act on. Named
	// separately so a rewrite that drops one fails on the clause it dropped
	// rather than on an opaque string diff.
	for _, must := range []string{"your own name", "exact amount", "TSA CONNECT LTD"} {
		if !strings.Contains(got, must) {
			t.Errorf("instruction no longer mentions %q: %q", must, got)
		}
	}
}

// The account name is interpolated, not a placeholder: a payer who sees a
// company name they do not recognise and no explanation for it reads the
// transfer as fraud.
func TestPayerInstruction_NamesTheActualAccount(t *testing.T) {
	acct := testCentralAccount()
	acct.AccountName = "  SOME OTHER LTD  "

	got := acct.PayerInstruction()

	if !strings.Contains(got, "reads SOME OTHER LTD, not") {
		t.Fatalf("instruction does not name the account it is shown beside: %q", got)
	}
}

func TestCreateStableBuyIntent_CentralModeCarriesThePayerInstruction(t *testing.T) {
	s, userID := newCentralStableBuyService(t)

	_, pay, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if pay.PayerInstruction != wantPayerInstruction {
		t.Fatalf("PayerInstruction = %q, want %q", pay.PayerInstruction, wantPayerInstruction)
	}
}

// In VA mode the account already bears the payer's own name, so every sentence
// of the instruction is false there — and the first one would have them hunting
// for a problem that does not exist.
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
