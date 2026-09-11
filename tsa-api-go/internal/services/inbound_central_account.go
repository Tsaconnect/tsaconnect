package services

import "strings"

// CentralAccount is the single company bank account every user pays into once
// INBOUND_MODE=central. It replaces the per-user virtual account that each buy
// flow used to hand out.
//
// It is a plain value, injected rather than read from config at the point of
// use, so the buy flows stay testable without a *config.Config — and so the two
// products that display it (MCGP buys and stablecoin buys) cannot drift into
// showing different account details.
//
// The zero value is "not configured", which is the correct state in VA mode.
// Every consumer MUST check IsConfigured before displaying it: an empty account
// number rendered into a pay-into screen is a user sending money nowhere.
type CentralAccount struct {
	AccountNumber string
	BankName      string
	AccountName   string
}

// IsConfigured reports whether the account can safely be shown to a payer.
// All three fields are required: a payer needs the number to send, the bank to
// select, and the name to confirm against before releasing money.
func (c CentralAccount) IsConfigured() bool {
	return strings.TrimSpace(c.AccountNumber) != "" &&
		strings.TrimSpace(c.BankName) != "" &&
		strings.TrimSpace(c.AccountName) != ""
}

// SetInboundCentral wires both the mode and the account together.
//
// One setter takes both ON PURPOSE. An earlier version set only the account and
// left callers to infer the mode from IsConfigured, which meant that merely
// setting the three INBOUND_CENTRAL_* env vars — precisely how an operator
// would stage a cutover — silently retired every virtual account without the
// flag ever being flipped. Coupling them here makes that state unrepresentable
// rather than merely discouraged.
func (s *ITService) SetInboundCentral(enabled bool, a CentralAccount) {
	s.inboundCentral = enabled
	s.centralAccount = a
}

// ActiveCentralAccount returns the account payers must be sent to right now.
//
// ok is false in VA mode even when an account is fully configured, so a call
// site cannot route payers to a pre-seeded account before the cutover. Prefer
// this over reading the fields directly: it is one check that cannot be half
// done, where "is central on?" and "do we have an account?" as two separate
// checks invite getting only one of them right.
func (s *ITService) ActiveCentralAccount() (CentralAccount, bool) {
	if !s.inboundCentral || !s.centralAccount.IsConfigured() {
		return CentralAccount{}, false
	}
	return s.centralAccount, true
}
