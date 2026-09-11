package services

import "testing"

func fullCentralAccount() CentralAccount {
	return CentralAccount{
		AccountNumber: "9078978706",
		BankName:      "Flutterwave MFB",
		AccountName:   "TSA Connect",
	}
}

func TestCentralAccountIsConfigured(t *testing.T) {
	full := fullCentralAccount()
	tests := []struct {
		name string
		acct CentralAccount
		want bool
	}{
		{"complete", full, true},
		{"zero value", CentralAccount{}, false},
		{"no account number", CentralAccount{BankName: full.BankName, AccountName: full.AccountName}, false},
		{"no bank", CentralAccount{AccountNumber: full.AccountNumber, AccountName: full.AccountName}, false},
		{"no account name", CentralAccount{AccountNumber: full.AccountNumber, BankName: full.BankName}, false},
		{"whitespace only", CentralAccount{AccountNumber: "  ", BankName: " ", AccountName: "\t"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.acct.IsConfigured(); got != tc.want {
				t.Fatalf("IsConfigured() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A fully configured account must stay INERT until the mode is actually
// central. Pre-seeding INBOUND_CENTRAL_* ahead of a cutover is exactly how an
// operator stages this safely, and an earlier version of this code treated that
// as "central is on" — silently retiring every virtual account with no flag
// flip and no restart. This is the regression that must never come back.
func TestActiveCentralAccountRequiresCentralMode(t *testing.T) {
	s := &ITService{}
	s.SetInboundCentral(false, fullCentralAccount())

	if _, ok := s.ActiveCentralAccount(); ok {
		t.Fatal("a configured account activated while INBOUND_MODE=va; " +
			"pre-seeding the env vars must not retire virtual accounts")
	}
}

func TestActiveCentralAccountRequiresConfiguredAccount(t *testing.T) {
	s := &ITService{}
	s.SetInboundCentral(true, CentralAccount{AccountNumber: "9078978706"})

	if _, ok := s.ActiveCentralAccount(); ok {
		t.Fatal("an incomplete account activated; a payer would see a blank bank and name")
	}
}

func TestActiveCentralAccountReturnsAccountWhenEnabled(t *testing.T) {
	want := fullCentralAccount()
	s := &ITService{}
	s.SetInboundCentral(true, want)

	got, ok := s.ActiveCentralAccount()
	if !ok {
		t.Fatal("ActiveCentralAccount() not ok with central mode on and a full account")
	}
	if got != want {
		t.Fatalf("ActiveCentralAccount() = %+v, want %+v", got, want)
	}
}
