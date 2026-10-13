package ramp

import (
	"context"
	"errors"
	"log"
	"math/big"
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
// shownFees: the pool's costs are shown as one "Fees" line (owner,
// 2026-10-13); every other provider's lines as priced.
func shownFees(provider string, fees []services.StableBuyFee, displayRateMinor *big.Int) []services.StableBuyFee {
	if provider == ProviderPool {
		return services.PoolUserFeeLines(fees, displayRateMinor)
	}
	return fees
}

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
		TokenAmount: services.StableFromBaseUnits(q.AssetUnits), Fees: VenueFees(shownFees(provider, q.Fees, q.DisplayRateMinor)),
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
	case *PoolProvider:
		if tp != nil {
			tp.conv = conv
		}
	}
	return p
}

// itConvertedLeg is a provider's settlement leg for a converted buy: the
// leg's quote, the chain and token it delivers, and the market mid.
type itConvertedLeg struct {
	q     *services.StableBuyQuote
	chain string
	token string // the leg's token; "" is USDC (convertedLegToken)
	mid   float64
	pool  *services.PoolPrice // the pool's price, for its open (nil for the others)
}

// itConverted is what differs between the IT-backed providers' converted
// buys; the flow around it is itBuyProvider's (one implementation, DRY).
type itConverted struct {
	conv ConvertedPricer
	// leg prices the settlement leg. At quote time (open false) an error only
	// skips the venue; at open it is the classified refusal.
	leg func(ctx context.Context, r Request, open bool) (*itConvertedLeg, error)
	// admit is the provider's own open-time check after the price guard
	// (nil: none).
	admit func(ctx context.Context, userID uuid.UUID, leg *itConvertedLeg) error
	// open writes the intent and its record.
	open func(ctx context.Context, userID uuid.UUID, leg *itConvertedLeg, in services.ConvertedOpen) (*models.ITIntent, *services.StableBuyInstructions, *models.PaycrestOrderRecord, error)
}

// quoteConverted is a provider's converted venue for r, or none — never an
// error. Every skip is logged (redacted), so a venue that silently vanished
// (and let a dearer provider show) can be seen.
func (p *itBuyProvider) quoteConverted(ctx context.Context, c itConverted, r Request) []Venue {
	skip := func(reason string) []Venue {
		log.Printf("[ramp] METRIC ramp_%s_converted_quote_skipped reason=%q", p.name, services.RedactAlertText(reason))
		return nil
	}
	leg, err := c.leg(ctx, r, false)
	if err != nil {
		return skip(err.Error())
	}
	pv, err := c.conv.PriceConvertedLeg(ctx, convertedLeg(leg.chain, leg.token, leg.q, r.FiatAmount, leg.mid), *r.Want)
	if err != nil {
		return skip("no conversion route: " + err.Error())
	}
	if pv == nil {
		return skip("no conversion route")
	}
	v, err := convertedVenue(p.name, leg.q, pv, leg.mid)
	if err != nil {
		return skip(err.Error())
	}
	return []Venue{v}
}

// openConverted re-prices both legs, guards on the new all-in rate, then
// opens the intent and its record (spec §15.7). The venue opened is always
// the one priced here, at open time, to her own wallet — never one the
// request carried — so the fee cap and her destination are the pricer's.
// Refusals before the write fail over to the next venue for her target.
func (p *itBuyProvider) openConverted(ctx context.Context, c itConverted, r Request, in OrderInput) (*Order, error) {
	leg, err := c.leg(ctx, r, true)
	if err != nil {
		return nil, err
	}
	want := *r.Want
	want.Recipient = in.WalletAddress
	pv, err := c.conv.PriceConvertedLeg(ctx, convertedLeg(leg.chain, leg.token, leg.q, r.FiatAmount, leg.mid), want)
	if err != nil || pv == nil {
		return nil, WrapClass(ErrVenueUnavailable, errConversionGone)
	}
	terms, err := services.NewConvertedTerms(leg.q, pv)
	if err != nil {
		return nil, WrapClass(ErrVenueUnavailable, err)
	}
	bound := services.FormatMinor(terms.EffectiveRateMinor.String())
	if err := services.CheckPriceGuard(p.name, string(Buy), bound, in.LimitRate, in.Guard); err != nil {
		return nil, err
	}
	if c.admit != nil {
		if err := c.admit(ctx, in.UserID, leg); err != nil {
			return nil, err
		}
	}
	if err := p.checkAbandonCap(ctx, in.UserID); err != nil {
		return nil, err
	}
	intent, payInto, rec, err := c.open(ctx, in.UserID, leg, services.ConvertedOpen{Venue: *pv, WalletAddress: in.WalletAddress})
	if err != nil {
		return nil, classifyITOpen(err, true)
	}
	return &Order{Provider: p.name, IT: intent, PayInto: payInto, Conversion: rec}, nil
}

// convertedVenue is one converted IT-backed venue (spec §15.7): the leg's
// display rate, the all-in rate on T, T itself and the leg's fee lines plus
// the Conversion (provider) line.
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
		TokenAmount: services.StableFromBaseUnits(terms.OwedUnits), Fees: VenueFees(shownFees(provider, terms.Fees, q.DisplayRateMinor)),
		Kind: models.RouteKindConverted, ViaChain: pv.ChainKey, ViaToken: pv.Token,
		RequestedChain: pv.RequestedChain, RequestedToken: pv.RequestedToken, RequestedChainID: pv.RequestedChainID,
		RequestedAddress: pv.RequestedAddress, RequestedDecimals: pv.RequestedDecimals,
		RequestedDestination: pv.RequestedDestination, EstimatedAmount: pv.EstimatedAmount,
		MarkupBps: pv.MarkupBps, CostBps: pv.CostBps, PromisedAmount: pv.PromisedAmount, ETASeconds: pv.ETASeconds,
	}, nil
}
