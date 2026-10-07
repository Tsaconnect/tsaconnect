package ramp

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// polygonVenue is the pricer's answer on Flutterwave's Polygon USDC leg.
func polygonVenue() *services.PaycrestVenue {
	v := optVenue()
	v.ChainKey, v.Network, v.ViaChain, v.Rate = "polygon", "polygon", "polygon", "1658.50"
	return v
}

func convertingFlutterwave(t *testing.T, pricer *fakePricer) (*FlutterwaveProvider, *fakeFWIT) {
	t.Helper()
	f := newFakeFWIT(t)
	p := newFlutterwaveProvider(f)
	WithConversion(p, pricer)
	return p, f
}

func convertedFWInput(limit string) OrderInput {
	in := fwInput(limit, services.PriceGuardEnforce)
	in.WalletAddress = "0x000000000000000000000000000000000000dEaD"
	return in
}

// §15.7 on route 2: Flutterwave's own fee lines first, then the LiFi line;
// the leg is Polygon USDC; the rate is all-in on T.
func TestFlutterwaveQuoteConverted_VenueCarriesTheLiFiLineAfterFlutterwavesFees(t *testing.T) {
	pricer := &fakePricer{venue: polygonVenue()}
	p, _ := convertingFlutterwave(t, pricer)
	venues, err := p.QuoteConverted(context.Background(), convertedTreasuryReq("165025"))
	if err != nil || len(venues) != 1 {
		t.Fatalf("venues = %+v err = %v", venues, err)
	}
	v := venues[0]
	if v.Kind != models.RouteKindConverted || v.Provider != ProviderFlutterwave || v.Rate != "1658.50" ||
		v.EffectiveRate != "1743.31" || v.ViaChain != "polygon" || v.ViaToken != "USDC" || v.RequestedToken != "USDT" {
		t.Fatalf("venue = %+v", v)
	}
	if len(v.Fees) != 4 || v.Fees[0].Provider != "Flutterwave" || v.Fees[1].Provider != "Flutterwave" || v.Fees[2].Provider != "Flutterwave" {
		t.Fatalf("fees = %+v", v.Fees)
	}
	if last := v.Fees[3]; last.Label != "Conversion (LiFi)" || last.Provider != "LiFi" || last.Kind != "conversion" || last.Token != "0.47569" {
		t.Fatalf("LiFi line = %+v", last)
	}
	if leg := pricer.legs[0]; leg.ChainKey != "polygon" || leg.Token != "USDC" || leg.Delivered.Cmp(big.NewInt(95_137_885)) != 0 {
		t.Fatalf("leg = %+v", leg)
	}
	name, k, err := DecodeHandle(v.Handle)
	if err != nil || name != ProviderFlutterwave || k != (VenueKey{"polygon", "USDC"}) {
		t.Fatalf("handle → %q %+v %v", name, k, err)
	}
}

func TestFlutterwaveQuoteConverted_OffWithoutAPricerOrARoute(t *testing.T) {
	if venues, _ := newFlutterwaveProvider(newFakeFWIT(t)).QuoteConverted(context.Background(), convertedTreasuryReq("165025")); len(venues) != 0 {
		t.Fatalf("no pricer: %+v", venues)
	}
	p, _ := convertingFlutterwave(t, &fakePricer{})
	if venues, _ := p.QuoteConverted(context.Background(), convertedTreasuryReq("165025")); len(venues) != 0 {
		t.Fatalf("no route: %+v", venues)
	}
	p2, f := convertingFlutterwave(t, &fakePricer{venue: polygonVenue()})
	f.rateErr = errors.New("rates down")
	if venues, _ := p2.QuoteConverted(context.Background(), convertedTreasuryReq("165025")); len(venues) != 0 {
		t.Fatalf("no rate: %+v", venues)
	}
	if venues, _ := p2.QuoteConverted(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: "165025"}); len(venues) != 0 {
		t.Fatalf("no target: %+v", venues)
	}
}

func TestFlutterwaveOpenConverted_GuardsOnTheAllInRateThenOpensBothRecords(t *testing.T) {
	pricer := &fakePricer{venue: polygonVenue()}
	p, f := convertingFlutterwave(t, pricer)
	f.intent = &models.ITIntent{ID: uuid.New()}
	if _, err := p.OpenConverted(context.Background(), convertedTreasuryReq("165025"), convertedFWInput("1743.00")); !errors.Is(err, services.ErrPriceMovedPastQuote) {
		t.Fatalf("guard: %v", err)
	}
	if len(f.convOpens) != 0 {
		t.Fatalf("opened past the guard: %+v", f.convOpens)
	}
	in := convertedFWInput("1755")
	o, err := p.OpenConverted(context.Background(), convertedTreasuryReq("165025"), in)
	if err != nil || o.Conversion == nil || o.Provider != ProviderFlutterwave || len(f.convOpens) != 1 {
		t.Fatalf("o=%+v err=%v opens=%+v", o, err, f.convOpens)
	}
	last := pricer.wants[len(pricer.wants)-1]
	if last.Recipient != in.WalletAddress || f.convOpens[0].WalletAddress != in.WalletAddress ||
		f.convOpens[0].Venue.TokenAmountBaseUnits != services.ConversionOwed(big.NewInt(95_137_885), 50).String() {
		t.Fatalf("want=%+v open=%+v", last, f.convOpens[0])
	}
}

// The IT agreement refusal is hers, not the venue's: no failover.
func TestFlutterwaveOpenConverted_AgreementRefusalIsForTheUser(t *testing.T) {
	p, f := convertingFlutterwave(t, &fakePricer{venue: polygonVenue()})
	f.openErr = services.ErrITAgreementRequired
	if _, err := p.OpenConverted(context.Background(), convertedTreasuryReq("165025"), convertedFWInput("1755")); !errors.Is(err, ErrRefusedForUser) {
		t.Fatalf("err = %v", err)
	}
}

func TestFlutterwaveOpenConverted_RefusalsBeforeTheWriteFailOver(t *testing.T) {
	p, f := convertingFlutterwave(t, &fakePricer{})
	if _, err := p.OpenConverted(context.Background(), convertedTreasuryReq("165025"), convertedFWInput("1755")); !errors.Is(err, ErrVenueUnavailable) {
		t.Fatalf("route gone: %v", err)
	}
	p2, f2 := convertingFlutterwave(t, &fakePricer{venue: polygonVenue()})
	f2.abandoned = 100
	if _, err := p2.OpenConverted(context.Background(), convertedTreasuryReq("165025"), convertedFWInput("1755")); !errors.Is(err, ErrAbandonCapReached) || len(f2.convOpens) != 0 {
		t.Fatalf("abandon cap: %v opens=%d", err, len(f2.convOpens))
	}
	f2.abandoned, f2.openErr = 0, services.ErrConversionUnavailable
	if _, err := p2.OpenConverted(context.Background(), convertedTreasuryReq("165025"), convertedFWInput("1755")); !errors.Is(err, ErrVenueUnavailable) || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("conversion unavailable: %v", err)
	}
	if len(f.convOpens) != 0 {
		t.Fatalf("opened with no route: %+v", f.convOpens)
	}
}
