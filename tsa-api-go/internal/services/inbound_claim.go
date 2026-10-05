package services

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Claim types. These are persisted on inbound_credits.matched_claim_type, so
// they are storage values and must not be renamed once any row carries them.
const (
	InboundClaimTypeITMCGP   = "it_mcgp"
	InboundClaimTypeITStable = "it_stable"
	InboundClaimTypeP2PTrade = "p2p_trade"
)

// InboundClaim is one open request for money, flattened so the matcher needs no
// product knowledge. Three products want inbound naira — MCGP buys, stablecoin
// buys and P2P trades — and the matcher must be able to arbitrate between them
// without importing any of their rules.
type InboundClaim struct {
	Type string
	ID   uuid.UUID

	// UserID is whose name is expected on the transfer, which is NOT always who
	// receives the value. On a P2P trade this is the BUYER (they send the naira)
	// while the merchant is credited.
	UserID uuid.UUID

	// AmountKobo is what the payer was told to send. Always a whole number of
	// naira (AmountKobo%100 == 0) because matching compares at naira
	// granularity — see the design's amount rule.
	AmountKobo int64

	// Currency is the ISO code this claim is priced in, upper-case and
	// trimmed. EVERY ClaimSource must set it on every claim it returns; the
	// matcher neither stamps it nor defaults it, and treats an empty value as
	// a source bug.
	//
	// It exists because OpenClaims takes a currency argument and the matcher
	// had no way to check the answer came back in it. The three sources do not
	// agree on what an empty argument means — one falls back to NGN, two
	// reject it — so "the source filtered correctly" was an assumption resting
	// on three different implementations staying in step forever. Set it from
	// the claim's OWN currency column, never by echoing the argument back:
	// echoing makes the check tautological and re-hides the bug it is for.
	Currency string

	OpenedAt time.Time
}

// ClaimSource is the seam between the matcher and one product's settle path.
//
// Implementations own their product's rules entirely; the matcher only asks
// "what is open?" and says "this credit is yours". Adding a fourth product is
// one implementation of this interface, never a change to the matcher.
type ClaimSource interface {
	// OpenClaims returns every claim currently awaiting payment in currency.
	// An empty slice and a nil error means "nothing open", which is ordinary.
	OpenClaims(ctx context.Context, currency string) ([]InboundClaim, error)

	// Accept binds credit to claimID and drives the product's settle path.
	//
	// MUST be idempotent on credit.ProviderRef. The poller re-runs every 30
	// seconds and a crash between the settle and the status write is expected,
	// so a replayed credit has to be a no-op rather than a second credit.
	//
	// Returning a *ClaimNeedsReviewError holds the credit for an operator
	// instead of retrying it; any other error is retried next tick.
	Accept(ctx context.Context, claimID uuid.UUID, credit *models.InboundCredit) error
}

// ClaimNeedsReviewError is how a source says "this credit is correctly
// attributed, but the product's own rules say a person must approve it before
// it settles" — a paused merchant, a trade over a cap. It must be returned
// before the source has moved any money.
//
// It is a hold rather than an ordinary error because nothing about it is
// transient: retrying every tick would re-derive the same refusal forever while
// the credit sat `unmatched`, where no operator queue looks.
type ClaimNeedsReviewError struct {
	// Code is one of the models.InboundHold* values. It reaches logs and alert
	// email, so it must name nobody.
	Code string
	// Reason is the console prose an operator reads.
	Reason string
}

func (e *ClaimNeedsReviewError) Error() string {
	return "claim needs review (" + e.Code + "): " + e.Reason
}

type manualBindKey struct{}

// withManualBind marks ctx as an operator's explicit assignment of a credit.
func withManualBind(ctx context.Context) context.Context {
	return context.WithValue(ctx, manualBindKey{}, true)
}

// isManualBind reports whether Accept is running on an operator's assignment.
// A source skips its review gates then: the assignment IS the review, and
// refusing it again would leave the operator only refund-or-ignore.
func isManualBind(ctx context.Context) bool {
	v, _ := ctx.Value(manualBindKey{}).(bool)
	return v
}
