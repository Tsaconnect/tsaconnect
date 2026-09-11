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
	Accept(ctx context.Context, claimID uuid.UUID, credit *models.InboundCredit) error
}
