// Package ramp routes NGN↔stablecoin buys and sells across providers. v1 has
// one provider (Paycrest); Flutterwave becomes one more Provider later. See
// docs/specs/2026-10-02-ramp-router-design.md (outside the repo).
package ramp

import (
	"context"
	"encoding/base64"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type Side string

const (
	Buy  Side = "buy"
	Sell Side = "sell"
)

// ProviderPaycrest names the Paycrest adapter. Its order ids are bare UUIDs
// forever: sponsorship, disputes and the app's hold keys parse them as UUIDs.
const ProviderPaycrest = "paycrest"

// The two IT-backed providers (spec 2026-10-03 rev 3). Their order ids are the
// bare IT intent UUIDs; "<provider>:<uuid>" is accepted too.
const (
	ProviderTreasury    = "treasury"    // the company wallet
	ProviderFlutterwave = "flutterwave" // Flutterwave buys and sends each order
)

// Request is what the user asked for. Token/ChainKey are optional on a quote
// (unpinned: best venue per pair) and required on an open.
type Request struct {
	Side       Side
	FiatCode   string
	FiatAmount string
	Token      string
	ChainKey   string
	// Want is the asset she asked for when no venue may deliver it directly
	// (spec §15); nil on a direct request. UserID is set only on the
	// authenticated converted quote (Decision C12).
	Want   *services.RequestedAsset
	UserID uuid.UUID
}

// Venue keeps today's PaycrestVenue JSON shape — the app calls
// tokenAmount.toFixed(), so it stays a number — plus the opaque handle and the
// fee display (spec §14): Rate is the display rate (our margin in, provider
// fees out), EffectiveRate the all-in rate the router ranks and guards on, and
// Fees one line per provider fee. TokenAmount is after every fee.
type Venue struct {
	Handle        string     `json:"handle"`
	Provider      string     `json:"-"`
	ChainKey      string     `json:"chain"`
	Token         string     `json:"token"`
	Rate          string     `json:"rate"`
	EffectiveRate string     `json:"effectiveRate"`
	SpreadPercent float64    `json:"spreadPercent"`
	TokenAmount   float64    `json:"tokenAmount"`
	Fees          []VenueFee `json:"fees"`
	// Kind is "direct" or "converted" (models.RouteKind*). A converted venue
	// delivers ChainKey/Token (the settlement leg) to the conversion treasury
	// and names what she receives in the Requested* fields, with Paycrest's
	// converted-venue JSON names.
	Kind                 string `json:"kind,omitempty"`
	ViaChain             string `json:"viaChain,omitempty"`
	ViaToken             string `json:"viaToken,omitempty"`
	RequestedChain       string `json:"requestedChain,omitempty"`
	RequestedToken       string `json:"requestedToken,omitempty"`
	RequestedChainID     int64  `json:"requestedChainId,omitempty"`
	RequestedAddress     string `json:"requestedTokenAddress,omitempty"`
	RequestedDecimals    int    `json:"requestedTokenDecimals,omitempty"`
	RequestedDestination string `json:"requestedDestination,omitempty"`
	EstimatedAmount      string `json:"estimatedAmount,omitempty"`
	MarkupBps            int    `json:"markupBps,omitempty"`
	CostBps              int    `json:"costBps,omitempty"`
	PromisedAmount       string `json:"promisedAmount,omitempty"`
	ETASeconds           int    `json:"etaSeconds,omitempty"`
}

// Target is what the venue delivers to her: the requested asset for a
// converted venue, its own pair for a direct one (Decision C2).
func (v Venue) Target() VenueKey {
	if v.Kind == models.RouteKindConverted {
		return VenueKey{ChainKey: strings.ToLower(v.RequestedChain), Token: strings.ToUpper(v.RequestedToken)}
	}
	return VenueKey{ChainKey: strings.ToLower(v.ChainKey), Token: strings.ToUpper(v.Token)}
}

// VenueFee is one provider fee inside a venue (spec §14): Token is the fee in
// tokens and Ngn its naira value at the display rate, both decimal strings.
type VenueFee struct {
	Kind     string `json:"kind"` // "conversion" | "network"
	Label    string `json:"label"`
	Provider string `json:"provider"` // display name for the tag (Decision D39)
	Token    string `json:"token"`
	Ngn      string `json:"ngn"`
}

// RankRate is the rate a venue is ranked and price-guarded on: the all-in
// rate, or the rate itself when the provider gives no separate one (D40).
func (v Venue) RankRate() string {
	if strings.TrimSpace(v.EffectiveRate) != "" {
		return v.EffectiveRate
	}
	return v.Rate
}

type VenueKey struct{ ChainKey, Token string }

// OrderInput is everything Open needs beyond the venue. LimitRate/Guard are
// filled by the Router from the user's quote and RAMP_PRICE_*.
type OrderInput struct {
	UserID            uuid.UUID
	WalletAddress     string
	LimitRate         string
	Guard             services.PriceGuardMode
	RefundBankCode    string
	RefundAccount     string
	RefundAccountName string
	BankCode          string
	AccountNo         string
	AccountName       string
}

// Order is one provider's order, carried as the provider's own record so the
// handler applies that provider's view.
type Order struct {
	Provider string
	Paycrest *models.PaycrestOrderRecord
	// IT is a company-wallet or Flutterwave buy (an it_intents row). PayInto
	// is where to pay while it awaits the transfer; nil after.
	IT      *models.ITIntent
	PayInto *services.StableBuyInstructions
	// Conversion is the record an IT-settled converted buy feeds (spec §15.4).
	Conversion *models.PaycrestOrderRecord
}

func (o Order) CreatedAt() time.Time {
	switch {
	case o.Paycrest != nil:
		return o.Paycrest.CreatedAt
	case o.IT != nil:
		return o.IT.CreatedAt
	}
	return time.Time{}
}

type ProviderQuote struct {
	Venues     []Venue
	MarketRate float64
}

type QuoteResult struct {
	FiatCode   string    `json:"currency"`
	FiatAmount string    `json:"amount"`
	Side       string    `json:"side"`
	MarketRate float64   `json:"marketRate"`
	Venues     []Venue   `json:"venues"`
	Best       *Venue    `json:"best"`
	QuotedAt   time.Time `json:"quotedAt"`
}

// Provider is one ramp provider. Every error it returns from Open is wrapped
// in exactly one class below (or is infrastructure, returned as-is);
// Quote errors wrap ErrLegClosed / ErrBelowMinimum / ErrNoVenue or are
// treated as the provider being unavailable.
type Provider interface {
	Name() string
	RampLegs(ctx context.Context) (map[string]services.RampLegs, error)
	Quote(ctx context.Context, r Request) (*ProviderQuote, error)
	Open(ctx context.Context, k VenueKey, r Request, in OrderInput) (*Order, error)
	// CheckNoOpenOrder returns nil, or this provider's own refusal for an
	// order that still blocks a new one, wrapped with WrapClass(ErrRefusedForUser, …)
	// so its wording survives — or an infrastructure error.
	CheckNoOpenOrder(ctx context.Context, userID uuid.UUID, side Side) error
	Get(ctx context.Context, userID uuid.UUID, nativeID string) (*Order, error)
	List(ctx context.Context, userID uuid.UUID, limit int) ([]Order, error)
	ClaimPayment(ctx context.Context, userID uuid.UUID, nativeID string) (*Order, error)
	Cancel(ctx context.Context, userID uuid.UUID, nativeID string) (*Order, error)
}

// ConvertedQuoter is a provider that also reaches a requested asset through a
// settlement leg it delivers to the conversion treasury (spec §15).
type ConvertedQuoter interface {
	QuoteConverted(ctx context.Context, r Request) ([]Venue, error)
	OpenConverted(ctx context.Context, r Request, in OrderInput) (*Order, error)
}

// ConvertedPricer prices a requested asset from a delivered leg.
// *services.PaycrestQuoteService satisfies it.
type ConvertedPricer interface {
	PriceConvertedLeg(ctx context.Context, leg services.SettlementLeg, want services.RequestedAsset) (*services.PaycrestVenue, error)
}

var _ ConvertedPricer = (*services.PaycrestQuoteService)(nil)

var (
	// Open outcomes (spec §4.1).
	ErrRefusedForUser   = errors.New("ramp: refused for this user")
	ErrOutcomeUnknown   = errors.New("ramp: order outcome unknown")
	ErrVenueUnavailable = errors.New("ramp: venue unavailable")
	// Quote / routing outcomes.
	ErrLegClosed    = errors.New("ramp: nobody trades this way right now")
	ErrBelowMinimum = errors.New("ramp: amount below the minimum")
	ErrAboveMaximum = errors.New("ramp: amount above the maximum")
	// ErrAbandonCapReached: too many unpaid routed buys cancelled or expired
	// in the last 24h (spec §7). Carried on ErrVenueUnavailable.
	ErrAbandonCapReached = errors.New("ramp: too many unpaid purchases abandoned today")
	ErrNoVenue           = errors.New("ramp: no venue can fill this request")
	ErrUnavailable       = errors.New("ramp: every provider failed")
	ErrOrderNotFound     = errors.New("ramp: order not found")
	ErrInvalidQuote      = errors.New("ramp: missing or invalid quote")
)

// PriceMovedError: nothing opened within tolerance; Quote is the fresh
// (unfiltered) quote the app shows for re-confirmation.
type PriceMovedError struct{ Quote *QuoteResult }

func (e *PriceMovedError) Error() string { return "ramp: the price moved past the quote" }
func (e *PriceMovedError) Is(target error) bool {
	return target == services.ErrPriceMovedPastQuote
}

// classed tags an error with its outcome class without changing its text:
// paycrestError shows some causes (KYC) to the user verbatim, so the class
// must never prefix the message. errors.Is matches both class and cause.
type classed struct{ class, cause error }

func (e classed) Error() string   { return e.cause.Error() }
func (e classed) Unwrap() []error { return []error{e.class, e.cause} }

// WrapClass classifies cause; a nil cause yields the bare class. Exported so
// adapters and other packages' tests build classified errors the same way.
func WrapClass(class, cause error) error {
	if cause == nil {
		return class
	}
	return classed{class: class, cause: cause}
}

// EncodeHandle is base64url(provider|chain|token), unpadded: opaque to the
// app, and it never names the provider in plain text.
func EncodeHandle(provider string, k VenueKey) string {
	return EncodeHandleRaw(provider + "|" + k.ChainKey + "|" + k.Token)
}

// EncodeHandleRaw encodes any string as a handle (tests build bad ones).
func EncodeHandleRaw(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func DecodeHandle(h string) (string, VenueKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(h))
	if err != nil || len(raw) == 0 {
		return "", VenueKey{}, ErrInvalidQuote
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", VenueKey{}, ErrInvalidQuote
	}
	return parts[0], VenueKey{ChainKey: parts[1], Token: parts[2]}, nil
}

// ResolveID splits "<provider>:<native>". A bare UUID comes back as Paycrest's
// for compatibility, but it may be any provider's (the IT providers' ids are
// bare too): the Router asks each provider in turn (Decision D11).
func ResolveID(id string) (string, string, error) {
	id = strings.TrimSpace(id)
	if _, err := uuid.Parse(id); err == nil {
		return ProviderPaycrest, id, nil
	}
	p, native, ok := strings.Cut(id, ":")
	if !ok || p == "" || native == "" {
		return "", "", ErrOrderNotFound
	}
	return p, native, nil
}

// LimitRate is the worst bound rate spec §5.2 accepts for a quoted rate:
// buy quoted×(1+t), sell quoted×(1−t). Eight decimals, exact.
func LimitRate(side Side, quoted string, tolBps int) (string, error) {
	q, ok := new(big.Rat).SetString(strings.TrimSpace(quoted))
	if !ok || q.Sign() <= 0 {
		return "", ErrInvalidQuote
	}
	factor := int64(10000 + tolBps)
	if side == Sell {
		factor = int64(10000 - tolBps)
	}
	return new(big.Rat).Mul(q, big.NewRat(factor, 10000)).FloatString(8), nil
}

// Acceptable reports whether a venue's rate is within the limit.
func Acceptable(side Side, rate, limit string) bool {
	r, ok1 := new(big.Rat).SetString(strings.TrimSpace(rate))
	l, ok2 := new(big.Rat).SetString(strings.TrimSpace(limit))
	if !ok1 || !ok2 {
		return false
	}
	if side == Sell {
		return r.Cmp(l) >= 0
	}
	return r.Cmp(l) <= 0
}

// RateBetter reports whether rate a is strictly better for the user than b:
// fewer naira per token on a buy, more on a sell. Unreadable rates lose.
func RateBetter(side Side, a, b string) bool {
	ra, okA := new(big.Rat).SetString(strings.TrimSpace(a))
	rb, okB := new(big.Rat).SetString(strings.TrimSpace(b))
	switch {
	case !okA:
		return false
	case !okB:
		return true
	case side == Sell:
		return ra.Cmp(rb) > 0
	default:
		return ra.Cmp(rb) < 0
	}
}
