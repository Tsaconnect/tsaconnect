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

// SetCentralAccount wires the company pay-into account for buy flows.
//
// Deliberately a setter on the existing automation-seam pattern rather than a
// constructor argument: ITService already takes its optional collaborators this
// way, and in VA mode there is no central account to pass.
func (s *ITService) SetCentralAccount(a CentralAccount) { s.centralAccount = a }

// CentralAccount returns the configured company pay-into account.
func (s *ITService) CentralAccount() CentralAccount { return s.centralAccount }
