package ramp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// treasuryIT is the slice of *services.ITService the company wallet's adapter uses.
type treasuryIT interface {
	itBuyReads
	HasCompanyWallet() bool
	TreasuryChains() []string
	TreasuryTokens(chain string) []string
	TreasuryFreeUnits(ctx context.Context, chain, token string) (*big.Int, error)
	TreasuryCanPayGas(ctx context.Context, chain string) bool
	TreasuryMinOrderUnits(marketRate *big.Rat) *big.Int
	QuoteTreasuryBuy(asset, fiatMajor, currency string, marketRate *big.Rat) (*services.StableBuyQuote, error)
	CheckTreasuryDailyCap(ctx context.Context, userID uuid.UUID, units *big.Int) error
	OpenTreasuryBuy(ctx context.Context, userID uuid.UUID, q *services.StableBuyQuote, chain, destAddress string) (*models.ITIntent, *services.StableBuyInstructions, error)
}

// errStockDoesNotCover is the company wallet's "open, but not for this
// amount" — answered as above-maximum, and never naming the stock (§4.1).
var errStockDoesNotCover = errors.New("that amount is above what this route can fill right now")

var errTreasuryGasShort = errors.New("the company wallet cannot pay network fees right now")

// treasuryFiat is the only currency the company wallet sells for.
const treasuryFiat = "NGN"

// TreasuryProvider sells the company's own stablecoin stock (spec §4.1).
type TreasuryProvider struct {
	itBuyProvider
	it     treasuryIT
	market services.TreasuryRateSource
}

// NewTreasuryProvider returns a true nil Provider unless RAMP_TREASURY_BUY is
// on, the company wallet is wired and a market rate exists.
func NewTreasuryProvider(it *services.ITService, market services.TreasuryRateSource, enabled bool) Provider {
	if !enabled || it == nil || !it.HasCompanyWallet() || market == nil {
		return nil
	}
	return newTreasuryProvider(it, market)
}

func newTreasuryProvider(it treasuryIT, market services.TreasuryRateSource) *TreasuryProvider {
	return &TreasuryProvider{
		itBuyProvider: itBuyProvider{name: ProviderTreasury, reads: it, now: time.Now},
		it:            it, market: market,
	}
}

func (p *TreasuryProvider) Name() string { return ProviderTreasury }

// RampLegs: Buy open while some offered token covers the smallest order and
// its lane can pay gas; a market-rate failure reads as closed (D58).
func (p *TreasuryProvider) RampLegs(ctx context.Context) (map[string]services.RampLegs, error) {
	open := false
	if market, err := p.market.BuyRate(ctx, treasuryFiat); err == nil {
		smallest := p.it.TreasuryMinOrderUnits(market)
		for _, chain := range p.it.TreasuryChains() {
			for _, token := range p.it.TreasuryTokens(chain) {
				free, ferr := p.it.TreasuryFreeUnits(ctx, chain, token)
				if ferr == nil && smallest.Sign() > 0 && free.Cmp(smallest) >= 0 && p.it.TreasuryCanPayGas(ctx, chain) {
					open = true
				}
			}
		}
	}
	return map[string]services.RampLegs{treasuryFiat: {Buy: open}}, nil
}

func (p *TreasuryProvider) Quote(ctx context.Context, r Request) (*ProviderQuote, error) {
	fiat := strings.ToUpper(strings.TrimSpace(r.FiatCode))
	if r.Side != Buy || fiat != treasuryFiat {
		return nil, ErrLegClosed
	}
	market, err := p.market.BuyRate(ctx, fiat)
	if err != nil {
		return nil, err // unavailable
	}
	// Price once (one stablecoin is $1 either way): the range comes first.
	if _, err := p.it.QuoteTreasuryBuy(models.ITAssetUSDC, r.FiatAmount, fiat, market); err != nil {
		return nil, classifyTreasuryQuote(err)
	}
	wantToken := strings.ToUpper(strings.TrimSpace(r.Token))
	wantChain := strings.ToLower(strings.TrimSpace(r.ChainKey))
	smallest := p.it.TreasuryMinOrderUnits(market)
	mid, _ := market.Float64()
	out := &ProviderQuote{MarketRate: mid}
	anyOpen, gasShort := false, false
	for _, chain := range p.it.TreasuryChains() {
		if wantChain != "" && chain != wantChain {
			continue
		}
		for _, token := range p.it.TreasuryTokens(chain) {
			if wantToken != "" && token != wantToken {
				continue
			}
			free, err := p.it.TreasuryFreeUnits(ctx, chain, token)
			if err != nil || free.Cmp(smallest) < 0 {
				continue // closed token (an unreadable balance reads as closed)
			}
			anyOpen = true
			q, err := p.it.QuoteTreasuryBuy(token, r.FiatAmount, fiat, market)
			if err != nil || free.Cmp(q.AssetUnits) < 0 {
				continue
			}
			if !p.it.TreasuryCanPayGas(ctx, chain) {
				gasShort = true
				log.Printf("[ramp] ALERT ramp_company_wallet_gas_short_total=1 chain=%s", chain)
				continue
			}
			out.Venues = append(out.Venues, itVenue(ProviderTreasury, chain, token, q, mid))
		}
	}
	switch {
	case len(out.Venues) > 0:
		return out, nil
	case gasShort:
		return nil, WrapClass(ErrVenueUnavailable, errTreasuryGasShort)
	case !anyOpen:
		return nil, ErrLegClosed
	default:
		return nil, WrapClass(ErrAboveMaximum, errStockDoesNotCover)
	}
}

func classifyTreasuryQuote(err error) error {
	switch {
	case errors.Is(err, services.ErrStableBuyBelowMinimum):
		return WrapClass(ErrBelowMinimum, err)
	case errors.Is(err, services.ErrStableBuyAboveMaximum):
		return WrapClass(ErrAboveMaximum, err)
	default:
		return err
	}
}

// Open (spec §4.1): re-quote at the current market rate, price guard, the
// per-user daily cap and the shared abandon cap, then the write under the lock.
func (p *TreasuryProvider) Open(ctx context.Context, k VenueKey, r Request, in OrderInput) (*Order, error) {
	fiat := strings.ToUpper(strings.TrimSpace(r.FiatCode))
	if r.Side != Buy || fiat != treasuryFiat {
		return nil, WrapClass(ErrVenueUnavailable, ErrLegClosed)
	}
	chain, token := strings.ToLower(k.ChainKey), strings.ToUpper(k.Token)
	if !p.offers(chain, token) {
		return nil, WrapClass(ErrNoVenue, fmt.Errorf("the company wallet does not sell %s on %s", token, chain))
	}
	market, err := p.market.BuyRate(ctx, fiat)
	if err != nil {
		return nil, WrapClass(ErrVenueUnavailable, err)
	}
	q, err := p.it.QuoteTreasuryBuy(token, r.FiatAmount, fiat, market)
	if err != nil {
		return nil, classifyITOpen(err, false)
	}
	bound := services.FormatMinor(q.RatePerUnitMinor.String())
	if err := services.CheckPriceGuard(ProviderTreasury, string(Buy), bound, in.LimitRate, in.Guard); err != nil {
		return nil, err
	}
	if err := p.it.CheckTreasuryDailyCap(ctx, in.UserID, q.AssetUnits); err != nil {
		return nil, classifyITOpen(err, false)
	}
	if err := p.checkAbandonCap(ctx, in.UserID); err != nil {
		return nil, err
	}
	// Spec §4.1: an order the wallet cannot send is never opened.
	if !p.it.TreasuryCanPayGas(ctx, chain) {
		log.Printf("[ramp] ALERT ramp_company_wallet_gas_short_total=1 chain=%s at=open", chain)
		return nil, WrapClass(ErrVenueUnavailable, errTreasuryGasShort)
	}
	intent, payInto, err := p.it.OpenTreasuryBuy(ctx, in.UserID, q, chain, in.WalletAddress)
	if err != nil {
		return nil, classifyITOpen(err, true)
	}
	return &Order{Provider: ProviderTreasury, IT: intent, PayInto: payInto}, nil
}

func (p *TreasuryProvider) offers(chain, token string) bool {
	for _, c := range p.it.TreasuryChains() {
		if c != chain {
			continue
		}
		for _, t := range p.it.TreasuryTokens(c) {
			if t == token {
				return true
			}
		}
	}
	return false
}

var _ Provider = (*TreasuryProvider)(nil)
