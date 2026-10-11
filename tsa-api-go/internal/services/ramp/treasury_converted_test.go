package ramp

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakePricer struct {
	venue *services.PaycrestVenue
	legs  []services.SettlementLeg
	wants []services.RequestedAsset
}

func (f *fakePricer) PriceConvertedLeg(_ context.Context, leg services.SettlementLeg, want services.RequestedAsset) (*services.PaycrestVenue, error) {
	f.legs, f.wants = append(f.legs, leg), append(f.wants, want)
	if f.venue == nil {
		return nil, nil
	}
	v := *f.venue
	v.TokenAmountBaseUnits = services.ConversionOwed(leg.Delivered, v.MarkupBps).String()
	return &v, nil
}

func optVenue() *services.PaycrestVenue {
	return &services.PaycrestVenue{Kind: models.RouteKindConverted, ChainKey: "base", Network: "base", Token: "USDC",
		ViaChain: "base", ViaToken: "USDC", Rate: "1354.74", RequestedChain: "optimism", RequestedToken: "USDT",
		RequestedChainID: 10, RequestedAddress: "0xOptUSDT", RequestedDecimals: 6, MarkupBps: 50,
		EstimatedAmount: "7235000", PromisedAmount: "7241759", ETASeconds: 60}
}

func convertingTreasury(t *testing.T, pricer *fakePricer) (*TreasuryProvider, *fakeTreasuryIT) {
	t.Helper()
	f := newFakeTreasuryIT(t)
	p := newTestTreasuryProvider(f)
	WithConversion(p, pricer)
	return p, f
}

func convertedTreasuryReq(amount string) Request {
	return Request{Side: Buy, FiatCode: "NGN", FiatAmount: amount, UserID: uuid.New(),
		Want: &services.RequestedAsset{Chain: "optimism", Token: "USDT", ChainID: 10, Address: "0xOptUSDT"}}
}

// §15.7: the base rate, the all-in rate on T, and the LiFi line.
func TestTreasuryQuoteConverted_VenueCarriesTheLiFiLineAndTheAllInRate(t *testing.T) {
	pricer := &fakePricer{venue: optVenue()}
	p, _ := convertingTreasury(t, pricer)
	venues, err := p.QuoteConverted(context.Background(), convertedTreasuryReq("10000"))
	if err != nil || len(venues) != 1 {
		t.Fatalf("venues = %+v err = %v", venues, err)
	}
	v := venues[0]
	if v.Kind != models.RouteKindConverted || v.Provider != ProviderTreasury || v.Rate != "1354.74" || v.EffectiveRate != "1380.88" ||
		v.RequestedToken != "USDT" || v.RequestedChainID != 10 || v.ViaChain != "base" {
		t.Fatalf("venue = %+v", v)
	}
	last := v.Fees[len(v.Fees)-1]
	if last.Label != "Conversion (provider)" || last.Provider != "provider" || last.Kind != "conversion" || last.Token != "0.036391" || last.Ngn != "49.30" {
		t.Fatalf("fee = %+v", last)
	}
	if pricer.legs[0].ChainKey != "base" || pricer.legs[0].Delivered.Cmp(big.NewInt(7_278_150)) != 0 {
		t.Fatalf("leg = %+v", pricer.legs[0])
	}
}

// Decision C12: over her daily cap, the company wallet offers no conversion.
func TestTreasuryQuoteConverted_NoVenueOverHerDailyCap(t *testing.T) {
	p, f := convertingTreasury(t, &fakePricer{venue: optVenue()})
	f.dailyErr = services.ErrTreasuryDailyCapReached
	if venues, err := p.QuoteConverted(context.Background(), convertedTreasuryReq("10000")); err != nil || len(venues) != 0 {
		t.Fatalf("venues = %+v err = %v", venues, err)
	}
}

func TestTreasuryQuoteConverted_OffWithoutAPricerOrStock(t *testing.T) {
	f := newFakeTreasuryIT(t)
	if venues, _ := newTestTreasuryProvider(f).QuoteConverted(context.Background(), convertedTreasuryReq("10000")); len(venues) != 0 {
		t.Fatalf("no pricer: %+v", venues)
	}
	p, f2 := convertingTreasury(t, &fakePricer{venue: optVenue()})
	f2.free["base|USDC"] = big.NewInt(1)
	if venues, _ := p.QuoteConverted(context.Background(), convertedTreasuryReq("10000")); len(venues) != 0 {
		t.Fatalf("no stock: %+v", venues)
	}
	p3, f3 := convertingTreasury(t, &fakePricer{venue: optVenue()})
	f3.gasOK = false
	if venues, _ := p3.QuoteConverted(context.Background(), convertedTreasuryReq("10000")); len(venues) != 0 {
		t.Fatalf("no gas: %+v", venues)
	}
}

func TestTreasuryOpenConverted_GuardsOnTheAllInRateThenOpensBothRecords(t *testing.T) {
	pricer := &fakePricer{venue: optVenue()}
	p, f := convertingTreasury(t, pricer)
	in := trInput("1380.00") // below the all-in 1380.88
	in.Guard = services.PriceGuardEnforce
	if _, err := p.OpenConverted(context.Background(), convertedTreasuryReq("10000"), in); !errors.Is(err, services.ErrPriceMovedPastQuote) {
		t.Fatalf("guard: %v", err)
	}
	if len(f.convOpens) != 0 {
		t.Fatalf("opened past the guard: %+v", f.convOpens)
	}
	in = trInput("1381")
	o, err := p.OpenConverted(context.Background(), convertedTreasuryReq("10000"), in)
	if err != nil || o.Conversion == nil || len(f.convOpens) != 1 || f.convOpens[0].Venue.RequestedToken != "USDT" {
		t.Fatalf("o=%+v err=%v opens=%+v", o, err, f.convOpens)
	}
	// The venue opened is the one the server priced at open time, to her own
	// wallet, on the leg the server chose.
	last := pricer.wants[len(pricer.wants)-1]
	if last.Recipient != in.WalletAddress || f.convOpens[0].WalletAddress != in.WalletAddress ||
		f.convOpens[0].Venue.TokenAmountBaseUnits != "7241759" || f.opened[0].chain != "base" {
		t.Fatalf("want=%+v open=%+v call=%+v", last, f.convOpens[0], f.opened[0])
	}
}

func TestTreasuryOpenConverted_ConversionGoneFailsOver(t *testing.T) {
	p, _ := convertingTreasury(t, &fakePricer{})
	if _, err := p.OpenConverted(context.Background(), convertedTreasuryReq("10000"), trInput("1390")); !errors.Is(err, ErrVenueUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestTreasuryOpenConverted_CapsRefuseBeforeTheWrite(t *testing.T) {
	p, f := convertingTreasury(t, &fakePricer{venue: optVenue()})
	f.dailyErr = services.ErrTreasuryDailyCapReached
	if _, err := p.OpenConverted(context.Background(), convertedTreasuryReq("10000"), trInput("1390")); !errors.Is(err, ErrVenueUnavailable) ||
		!errors.Is(err, services.ErrTreasuryDailyCapReached) || len(f.convOpens) != 0 {
		t.Fatalf("daily cap: err=%v opens=%d", err, len(f.convOpens))
	}
	f.dailyErr, f.abandoned = nil, 100
	if _, err := p.OpenConverted(context.Background(), convertedTreasuryReq("10000"), trInput("1390")); !errors.Is(err, ErrAbandonCapReached) || len(f.convOpens) != 0 {
		t.Fatalf("abandon cap: err=%v opens=%d", err, len(f.convOpens))
	}
}

// The service's own refusals (N2 and a conversion gone at the write) read as
// another provider's turn, and the abandon cap keeps its own answer.
func TestTreasuryOpenConverted_ServiceRefusalsFailOver(t *testing.T) {
	p, f := convertingTreasury(t, &fakePricer{venue: optVenue()})
	f.openErr = services.ErrStableBuyAbandonCapReached
	if _, err := p.OpenConverted(context.Background(), convertedTreasuryReq("10000"), trInput("1390")); !errors.Is(err, ErrVenueUnavailable) || !errors.Is(err, ErrAbandonCapReached) {
		t.Fatalf("abandon cap: %v", err)
	}
	f.openErr = services.ErrConversionUnavailable
	if _, err := p.OpenConverted(context.Background(), convertedTreasuryReq("10000"), trInput("1390")); !errors.Is(err, ErrVenueUnavailable) || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("conversion unavailable: %v", err)
	}
}

func TestTreasuryReads_OrderCarriesItsConversion(t *testing.T) {
	p, f := convertingTreasury(t, &fakePricer{venue: optVenue()})
	f.conversion = &models.PaycrestOrderRecord{Reference: services.ITConversionRef(f.intent.ID)}
	o, err := p.Get(context.Background(), uuid.New(), f.intent.ID.String())
	if err != nil || o.Conversion != f.conversion {
		t.Fatalf("o=%+v err=%v", o, err)
	}
}

func TestWithConversion_NilPricerLeavesTheProviderAsItWas(t *testing.T) {
	f := newFakeTreasuryIT(t)
	p := newTestTreasuryProvider(f)
	var typedNil *services.PaycrestQuoteService
	WithConversion(p, typedNil)
	WithConversion(p, nil)
	if p.conv != nil {
		t.Fatalf("conv = %#v", p.conv)
	}
	if got := WithConversion(nil, &fakePricer{}); got != nil {
		t.Fatalf("nil provider: %#v", got)
	}
}

// M4: only a reached daily cap drops the company wallet's converted venue —
// a cap that cannot be read keeps it (the open re-checks), so Flutterwave is
// never shown on a read error. Every skip is logged, redacted.
func TestTreasuryQuoteConverted_OnlyAReachedDailyCapDropsTheVenue(t *testing.T) {
	p, f := convertingTreasury(t, &fakePricer{venue: optVenue()})
	logs := captureRampLog(t)
	f.dailyErr = errors.New("daily cap read: dial tcp: i/o timeout")
	if venues, err := p.QuoteConverted(context.Background(), convertedTreasuryReq("10000")); err != nil || len(venues) != 1 {
		t.Fatalf("cap unreadable: venues = %+v err = %v", venues, err)
	}
	f.dailyErr = services.ErrTreasuryDailyCapReached
	if venues, _ := p.QuoteConverted(context.Background(), convertedTreasuryReq("10000")); len(venues) != 0 {
		t.Fatalf("cap reached: %+v", venues)
	}
	if !strings.Contains(logs.String(), "METRIC ramp_treasury_converted_quote_skipped reason=") {
		t.Fatalf("no skip metric: %q", logs.String())
	}
	logs.Reset()
	f.dailyErr = nil
	p.market = &fakeMarket{err: errors.New(`Post "https://base-mainnet.g.alchemy.com/v2/SECRETKEY123": timeout`)}
	if venues, _ := p.QuoteConverted(context.Background(), convertedTreasuryReq("10000")); len(venues) != 0 {
		t.Fatalf("no market rate: %+v", venues)
	}
	if !strings.Contains(logs.String(), "METRIC ramp_treasury_converted_quote_skipped") || strings.Contains(logs.String(), "SECRETKEY123") {
		t.Fatalf("skip log = %q", logs.String())
	}
}
