package services

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ErrKYCVerificationRequired refuses a user who has not completed Didit
// verification at the moment they try to put money IN. Handlers map it to 403
// with a machine code so the app can send the user into the KYC flow instead of
// leaving them on a payment screen with nowhere to go.
//
// The message is instructions, not a verdict: a user told only that they were
// refused on a money screen contacts support, and support cannot do anything
// for them either.
var ErrKYCVerificationRequired = errors.New("verify your identity before paying in — finish verification in the app, then open this order again")

// requireVerifiedForMoneyIn is the entry gate for every path where the user is
// about to send us money: the MCGP buy order, the stablecoin buy intent, and
// opening a P2P trade as the buyer.
//
// It is deliberately NOT conditional on the inbound mode. Verification is the
// entry requirement for transacting on a money app; it is not an implementation
// detail of how an incoming credit gets attributed. Do not relax it "because we
// are still on virtual accounts" — an unverified order opened while VA mode is
// on is still sitting in the table on the day central mode is switched on.
//
// Under central-account attribution an unverified payer is also actively
// harmful to OTHER users. Credits are matched by the sender's bank-account name
// against the payer's verified legal name, so a claim from a user with no
// stored legal name can never be ruled out as the owner of a credit at that
// amount, and the matcher must hold the credit. A verified user paying a round
// figure then has their own money held because an unverified stranger happened
// to open an order for the same figure.
//
// BOTH clauses are required, and the second is the subtle one.
//
// A passed verification does not guarantee a stored legal name. The KYC webhook
// deliberately lets an approval stand when the name lookup fails, and the
// provider can withhold the attribute outright — which is exactly the set the
// backfill endpoint exists to repair. Such a user is `verified` and still has
// no name for the matcher to compare a sender against.
//
// The matcher's own test for "can this claim be identified?" is the stored name
// being non-empty, not the status. Gate on the status alone and a verified user
// with no name walks straight through, pays, has their credit held, and holds
// every other credit at that amount alongside it — the precise harm this gate
// exists to prevent, reached through a smaller door.
//
// So this must track whatever the MATCHER treats as identifiable. If that rule
// changes and this does not, the gap reopens silently.
//
// Fails closed: a user row that cannot be read, or does not exist, is not
// verified. The only way through is an explicit `verified` plus a name.
//
// Money-OUT paths (sells, withdrawals, payouts, escrow releases) must never
// call this. Refusing an exit traps value the user already holds, which is a
// worse failure than refusing an entry.
func requireVerifiedForMoneyIn(ctx context.Context, db *gorm.DB, userID uuid.UUID) error {
	var row struct {
		VerificationStatus sql.NullString
		VerifiedLegalName  sql.NullString
	}
	// Table("users"), not Model(&models.User{}): this reads two columns, and
	// going through the model drags in its PostgreSQL-only column defaults
	// (mirrors the username lookup in p2p_trade_service.go).
	res := db.WithContext(ctx).Table("users").
		Select("verification_status", "verified_legal_name").
		Where("id = ?", userID).
		Limit(1).
		Scan(&row)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 || row.VerificationStatus.String != models.VerificationStatusVerified {
		return ErrKYCVerificationRequired
	}
	if strings.TrimSpace(row.VerifiedLegalName.String) == "" {
		return ErrKYCVerificationRequired
	}
	return nil
}

// RequireVerifiedForMoneyIn exposes the gate to handlers that must refuse
// BEFORE they offer the user a different next step.
//
// The MCGP buy handler is the case: in virtual-account mode it answers a user
// with no VA by telling them to activate one, and that branch sits ahead of
// OpenBuyOrder. Without this call an unverified user would be sent to activate
// a rail that is being retired instead of being sent to verification.
func (s *ITService) RequireVerifiedForMoneyIn(ctx context.Context, userID uuid.UUID) error {
	return requireVerifiedForMoneyIn(ctx, s.db, userID)
}
