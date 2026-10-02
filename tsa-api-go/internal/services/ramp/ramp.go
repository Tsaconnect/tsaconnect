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

// Request is what the user asked for. Token/ChainKey are optional on a quote
// (unpinned: best venue per pair) and required on an open.
type Request struct {
	Side       Side
	FiatCode   string
	FiatAmount string
	Token      string
	ChainKey   string
}

// Venue keeps today's PaycrestVenue JSON shape exactly — the app calls
// tokenAmount.toFixed(), so it stays a number — plus the opaque handle.
type Venue struct {
	Handle        string  `json:"handle"`
	Provider      string  `json:"-"`
	ChainKey      string  `json:"chain"`
	Token         string  `json:"token"`
	Rate          string  `json:"rate"`
	SpreadPercent float64 `json:"spreadPercent"`
	TokenAmount   float64 `json:"tokenAmount"`
	// Kind is always "direct" in v1 (converted venues stay on /paycrest);
	// emitted so the venue JSON is exactly today's.
	Kind string `json:"kind,omitempty"`
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

// Order is one provider's order. v1 carries the Paycrest record as-is so the
// handler applies its existing view (orderForCaller); a later provider adds
// its own field.
type Order struct {
	Provider string
	Paycrest *models.PaycrestOrderRecord
}

func (o Order) CreatedAt() time.Time {
	if o.Paycrest != nil {
		return o.Paycrest.CreatedAt
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

var (
	// Open outcomes (spec §4.1).
	ErrRefusedForUser   = errors.New("ramp: refused for this user")
	ErrOutcomeUnknown   = errors.New("ramp: order outcome unknown")
	ErrVenueUnavailable = errors.New("ramp: venue unavailable")
	// Quote / routing outcomes.
	ErrLegClosed     = errors.New("ramp: nobody trades this way right now")
	ErrBelowMinimum  = errors.New("ramp: amount below the minimum")
	ErrNoVenue       = errors.New("ramp: no venue can fill this request")
	ErrUnavailable   = errors.New("ramp: every provider failed")
	ErrOrderNotFound = errors.New("ramp: order not found")
	ErrInvalidQuote  = errors.New("ramp: missing or invalid quote")
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

// ResolveID maps an order id to its provider: a bare UUID is Paycrest's
// (never prefixed — spec §4.4); "<provider>:<native>" is any other's.
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
