package ramp

import (
	"context"
	"errors"
	"log"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// itBuyReads is the slice of *services.ITService both IT-backed providers read
// through (spec §4.2) — an interface so the adapters test without a database.
type itBuyReads interface {
	OpenStableBuy(ctx context.Context, userID uuid.UUID) (*models.ITIntent, error)
	GetStableBuyForUser(ctx context.Context, userID, intentID uuid.UUID, provider string) (*models.ITIntent, error)
	StableBuyPayInto(ctx context.Context, intent *models.ITIntent) (*services.StableBuyInstructions, error)
	CancelBuy(ctx context.Context, userID, intentID uuid.UUID) (*models.ITIntent, error)
	CountAbandonedRoutedStableBuys(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error)
	RoutedAbandonCap() int
	// ConversionFor is the conversion record a buy feeds, or nil (N1).
	ConversionFor(ctx context.Context, intent *models.ITIntent) (*models.PaycrestOrderRecord, error)
}

// itBuyProvider is the read side the company wallet and Flutterwave share:
// one implementation, embedded by each (spec §4.2, DRY). name is both the
// ramp provider name and the it_intents.provider value.
type itBuyProvider struct {
	name  string
	reads itBuyReads
	now   func() time.Time
}

// CheckNoOpenOrder refuses while the user has a stablecoin buy awaiting
// payment — of ANY provider; the unique index spans them — naming it (D26).
func (p *itBuyProvider) CheckNoOpenOrder(ctx context.Context, userID uuid.UUID, side Side) error {
	if side != Buy {
		return nil
	}
	open, err := p.reads.OpenStableBuy(ctx, userID)
	if err != nil {
		return err
	}
	if open != nil {
		return WrapClass(ErrRefusedForUser, &services.StableBuyOpenError{IntentID: open.ID})
	}
	return nil
}

// lookup is the user's own stablecoin buy of THIS provider; anything else is
// ErrOrderNotFound, which also lets the router move on to the next provider.
func (p *itBuyProvider) lookup(ctx context.Context, userID uuid.UUID, nativeID string) (*models.ITIntent, error) {
	id, err := parseNative(nativeID)
	if err != nil {
		return nil, err
	}
	intent, err := p.reads.GetStableBuyForUser(ctx, userID, id, p.name)
	if errors.Is(err, services.ErrStableIntentNotFound) {
		return nil, ErrOrderNotFound
	}
	return intent, err
}

func (p *itBuyProvider) order(ctx context.Context, intent *models.ITIntent) *Order {
	o := &Order{Provider: p.name, IT: intent}
	payInto, err := p.reads.StableBuyPayInto(ctx, intent)
	if err != nil {
		log.Printf("[ramp] %s pay-into intent=%s: %v", p.name, intent.ID, err) // the order still reads
	} else {
		o.PayInto = payInto
	}
	rec, err := p.reads.ConversionFor(ctx, intent)
	if err != nil {
		log.Printf("[ramp] %s conversion intent=%s: %s", p.name, intent.ID, services.RedactAlertText(err.Error())) // the order still reads
		return o
	}
	o.Conversion = rec
	return o
}

func (p *itBuyProvider) Get(ctx context.Context, userID uuid.UUID, nativeID string) (*Order, error) {
	intent, err := p.lookup(ctx, userID, nativeID)
	if err != nil {
		return nil, err
	}
	return p.order(ctx, intent), nil
}

// List is empty by design: every IT list already reads /api/it/intents (D9).
func (p *itBuyProvider) List(context.Context, uuid.UUID, int) ([]Order, error) { return nil, nil }

// ClaimPayment is an ownership-checked read: the IT screen's own "I've sent
// the transfer" runs the on-demand lookup (D10).
func (p *itBuyProvider) ClaimPayment(ctx context.Context, userID uuid.UUID, nativeID string) (*Order, error) {
	return p.Get(ctx, userID, nativeID)
}

func (p *itBuyProvider) Cancel(ctx context.Context, userID uuid.UUID, nativeID string) (*Order, error) {
	intent, err := p.lookup(ctx, userID, nativeID)
	if err != nil {
		return nil, err
	}
	// CancelBuy logs ramp_it_open_unpaid itself, for every cancel path.
	cancelled, err := p.reads.CancelBuy(ctx, userID, intent.ID)
	if err != nil {
		return nil, err
	}
	return &Order{Provider: p.name, IT: cancelled}, nil
}

// checkAbandonCap refuses after too many unpaid routed buys in 24h, counted
// across both providers (spec §7, D14). Another provider may still serve.
func (p *itBuyProvider) checkAbandonCap(ctx context.Context, userID uuid.UUID) error {
	limit := p.reads.RoutedAbandonCap()
	if limit <= 0 {
		return nil
	}
	n, err := p.reads.CountAbandonedRoutedStableBuys(ctx, userID, p.now().Add(-24*time.Hour))
	if err != nil {
		return WrapClass(ErrVenueUnavailable, err)
	}
	if n >= int64(limit) {
		log.Printf("[ramp] METRIC ramp_abandon_cap_hit provider=%s user=%s abandoned=%d cap=%d", p.name, userID, n, limit)
		return WrapClass(ErrVenueUnavailable, ErrAbandonCapReached)
	}
	return nil
}

// VenueFees renders a quote's fee lines for the venue JSON (spec §14).
func VenueFees(fees []services.StableBuyFee) []VenueFee {
	out := make([]VenueFee, 0, len(fees))
	for _, f := range fees {
		out = append(out, VenueFee{Kind: f.Kind, Label: f.Label, Provider: f.Provider,
			Token: services.FormatStableUnits(f.Units), Ngn: services.FormatMinor(f.NgnMinor)})
	}
	return out
}

// itVenue is one IT-backed venue: the display rate, the all-in rate the
// router ranks on, what she receives after every fee, and the fee lines.
func itVenue(provider, chain, token string, q *services.StableBuyQuote, market float64) Venue {
	effective := services.FormatMinor(q.RatePerUnitMinor.String())
	display := effective
	if q.DisplayRateMinor != nil {
		display = services.FormatMinor(q.DisplayRateMinor.String())
	}
	spread := 0.0
	if eff, err := strconv.ParseFloat(effective, 64); err == nil && market > 0 {
		spread = (eff/market - 1) * 100
	}
	k := VenueKey{ChainKey: chain, Token: token}
	return Venue{
		Handle: EncodeHandle(provider, k), Provider: provider, ChainKey: chain, Token: token,
		Rate: display, EffectiveRate: effective, SpreadPercent: spread,
		TokenAmount: services.StableFromBaseUnits(q.AssetUnits), Fees: VenueFees(q.Fees),
		Kind: models.RouteKindDirect,
	}
}

// convertedLegToken is the settlement leg's token for every converted buy
// (Decision C3: USDC-only stock, owner ruling Q1).
const convertedLegToken = models.ITAssetUSDC

// WithConversion lets the company-wallet or Flutterwave provider also reach
// assets it does not deliver directly (spec §15). main.go calls it only when
// that provider's converted switch holds; a nil conv (or a typed-nil pricer)
// leaves p as it was, so no typed nil is ever wired into the interface.
func WithConversion(p Provider, conv ConvertedPricer) Provider {
	if p == nil || conv == nil {
		return p
	}
	if pq, ok := conv.(*services.PaycrestQuoteService); ok && pq == nil {
		return p
	}
	switch tp := p.(type) {
	case *TreasuryProvider:
		if tp != nil {
			tp.conv = conv
		}
	case *FlutterwaveProvider:
		if tp != nil {
			tp.conv = conv
		}
	}
	return p
}

// convertedVenue is one converted IT-backed venue (spec §15.7): the leg's
// display rate, the all-in rate on T, T itself and the leg's fee lines plus
// the Conversion (LiFi) line.
func convertedVenue(provider string, q *services.StableBuyQuote, pv *services.PaycrestVenue, market float64) (Venue, error) {
	terms, err := services.NewConvertedTerms(q, pv)
	if err != nil {
		return Venue{}, err
	}
	effective := services.FormatMinor(terms.EffectiveRateMinor.String())
	spread := 0.0
	if eff, err := strconv.ParseFloat(effective, 64); err == nil && market > 0 {
		spread = (eff/market - 1) * 100
	}
	return Venue{
		Handle: EncodeHandle(provider, VenueKey{ChainKey: pv.ChainKey, Token: pv.Token}), Provider: provider,
		ChainKey: pv.ChainKey, Token: pv.Token, Rate: pv.Rate, EffectiveRate: effective, SpreadPercent: spread,
		TokenAmount: services.StableFromBaseUnits(terms.OwedUnits), Fees: VenueFees(terms.Fees),
		Kind: models.RouteKindConverted, ViaChain: pv.ChainKey, ViaToken: pv.Token,
		RequestedChain: pv.RequestedChain, RequestedToken: pv.RequestedToken, RequestedChainID: pv.RequestedChainID,
		RequestedAddress: pv.RequestedAddress, RequestedDecimals: pv.RequestedDecimals,
		RequestedDestination: pv.RequestedDestination, EstimatedAmount: pv.EstimatedAmount,
		MarkupBps: pv.MarkupBps, CostBps: pv.CostBps, PromisedAmount: pv.PromisedAmount, ETASeconds: pv.ETASeconds,
	}, nil
}
