package services

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

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

// The payer instruction is the mitigation the central-account attribution risk
// rests on. Do not soften it without reopening that decision.
//
// A credit binds by matching the sender's bank-account name against a payer's
// verified legal name, and a PARTIAL match is enough to bind when exactly one
// open order sits at the amount. That was accepted knowingly: if the real payer
// has no open order — superseded, expired, or not a user at all — the credit
// can bind to a stranger whose verified name is a token-subset of the sender's.
// We chose to keep crediting on a partial match rather than hold, ON THE
// UNDERSTANDING that payers are told to pay from their own account.
//
// Wording decided by the owner on 2026-09-15. "Delayed for review or credited
// to the wrong person" is what actually happens to a mismatched transfer — an
// earlier "cannot reach you" overstated it. The closing sentences explain the
// registered company name the payer's bank shows, which is not "TSA Connect";
// unexplained, a payer reads it as fraud and abandons or disputes the transfer.
const (
	payerInstructionNamed   = "Pay from a bank account named %s."
	payerInstructionOwnName = "Pay from a bank account in your own name."
	payerInstructionRest    = " Payments from other accounts can be delayed for review or credited to the wrong person." +
		" Send the exact amount shown." +
		" The account name shows as %s. This is TSA Connect's registered company account."
)

// PayerInstruction is the one wording every pay-into screen shows. A method on
// the account rather than a loose helper so the products that display it cannot
// drift into saying different things, and so no caller can render it without
// the account name it has to name.
//
// verifiedLegalName is the PAYER'S OWN Didit name — the exact string the matcher
// compares the sender against — so the payer is told precisely which account
// holder to send from. Pass it only on a response to that same user; empty
// falls back to "in your own name". Never log the result: it carries the name.
//
// CENTRAL MODE ONLY. In VA mode the payer is already paying into an account in
// their own name, which makes every sentence of this wrong.
func (c CentralAccount) PayerInstruction(verifiedLegalName string) string {
	// Fields+Join also flattens any newline or run of spaces the provider
	// returned, so the name renders as one line of plain text.
	name := strings.Join(strings.Fields(verifiedLegalName), " ")
	opening := payerInstructionOwnName
	if name != "" {
		opening = fmt.Sprintf(payerInstructionNamed, name)
	}
	return opening + fmt.Sprintf(payerInstructionRest, strings.TrimSpace(c.AccountName))
}

// VerifiedLegalNameOf reads a user's Didit legal name for their own payer
// instruction. Best-effort by design: any failure yields "", which renders the
// generic "in your own name" wording rather than failing the pay-into screen.
// Only the user id and the error are logged, never the name.
func VerifiedLegalNameOf(ctx context.Context, db *gorm.DB, userID uuid.UUID) string {
	if db == nil || userID == uuid.Nil {
		return ""
	}
	var row struct{ VerifiedLegalName sql.NullString }
	// Table("users") rather than the model, as requireVerifiedForMoneyIn does:
	// one column, without the model's PostgreSQL-only defaults.
	if err := db.WithContext(ctx).Table("users").
		Select("verified_legal_name").
		Where("id = ?", userID).
		Limit(1).
		Scan(&row).Error; err != nil {
		log.Printf("[payer instruction] legal name lookup failed for user=%s: %v", userID, err)
		return ""
	}
	return strings.TrimSpace(row.VerifiedLegalName.String)
}

// VerifiedLegalName is VerifiedLegalNameOf against the IT service's database.
func (s *ITService) VerifiedLegalName(ctx context.Context, userID uuid.UUID) string {
	return VerifiedLegalNameOf(ctx, s.db, userID)
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
