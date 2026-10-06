package ramp

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakeConvertedProvider struct {
	*fakeProvider
	converted   []Venue
	convResults []openResult
	convOpened  []Request
}

func (f *fakeConvertedProvider) QuoteConverted(context.Context, Request) ([]Venue, error) {
	return f.converted, nil
}
func (f *fakeConvertedProvider) OpenConverted(_ context.Context, r Request, _ OrderInput) (*Order, error) {
	f.convOpened = append(f.convOpened, r)
	if len(f.convResults) == 0 {
		return nil, errors.New("fake: no converted result")
	}
	res := f.convResults[0]
	if len(f.convResults) > 1 {
		f.convResults = f.convResults[1:]
	}
	return res.order, res.err
}

func converting(name string, v ...Venue) *fakeConvertedProvider {
	return &fakeConvertedProvider{fakeProvider: fakeNamed(name), converted: v}
}

// convVenue converts a USDC leg on settle into USDT on target.
func convVenue(provider, settle, target, effective string) Venue {
	return Venue{Handle: EncodeHandle(provider, VenueKey{settle, "USDC"}), Provider: provider,
		ChainKey: settle, Token: "USDC", Rate: effective, EffectiveRate: effective, Fees: []VenueFee{},
		Kind: models.RouteKindConverted, RequestedChain: target, RequestedToken: "USDT",
		RequestedChainID: 10, RequestedAddress: "0xOptUSDT", RequestedDecimals: 6}
}

var wantOptUSDT = &services.RequestedAsset{Chain: "optimism", Token: "USDT", ChainID: 10, Address: "0xOptUSDT"}

func convQuoteReq() Request {
	return Request{Side: Buy, FiatCode: "NGN", FiatAmount: "150000", UserID: uuid.New(), Want: wantOptUSDT}
}

func convOpenReq(handle, quoted string) OpenRequest {
	return OpenRequest{Request: convQuoteReq(), Handle: handle, QuotedRate: quoted,
		Input: OrderInput{UserID: uuid.New(), WalletAddress: "0xUser"}}
}

// Review Focus 6: Flutterwave is cheaper, but the company wallet can fill.
func TestRouterQuote_CompanyWalletFirstDropsFlutterwaveForTheTarget(t *testing.T) {
	tr := converting(ProviderTreasury, convVenue(ProviderTreasury, "base", "optimism", "1400"))
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "base", "USDC", "1384"))
	fw := converting(ProviderFlutterwave, convVenue(ProviderFlutterwave, "polygon", "optimism", "1390"))
	res, err := testRouter(tr, pc, fw).Quote(context.Background(), convQuoteReq())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range res.Venues {
		if v.Provider == ProviderFlutterwave {
			t.Fatalf("venues = %+v, want no Flutterwave venue for a target the company wallet fills", res.Venues)
		}
	}
	if len(res.Venues) != 2 {
		t.Fatalf("venues = %+v, want the company wallet's conversion and Paycrest's direct venue", res.Venues)
	}
}

func TestRouterQuote_FlutterwaveWhenTheCompanyWalletCannotFill(t *testing.T) {
	tr := converting(ProviderTreasury) // no converted venue: stock, maximum, daily cap…
	fw := converting(ProviderFlutterwave, convVenue(ProviderFlutterwave, "polygon", "optimism", "1390"))
	res, err := testRouter(tr, fw).Quote(context.Background(), convQuoteReq())
	if err != nil || len(res.Venues) != 1 || res.Venues[0].Provider != ProviderFlutterwave {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

// Decision C2: one venue per target, best effective rate, direct or converted.
func TestRouterQuote_OneVenuePerTargetBestRateWins(t *testing.T) {
	tr := converting(ProviderTreasury, convVenue(ProviderTreasury, "base", "optimism", "1390"))
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "optimism", "USDT", "1395"))
	res, err := testRouter(tr, pc).Quote(context.Background(), convQuoteReq())
	if err != nil || len(res.Venues) != 1 || res.Venues[0].Provider != ProviderTreasury {
		t.Fatalf("res = %+v err = %v, want the cheaper conversion alone", res, err)
	}
	pc2 := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "optimism", "USDT", "1380"))
	res, _ = testRouter(converting(ProviderTreasury, convVenue(ProviderTreasury, "base", "optimism", "1390")), pc2).Quote(context.Background(), convQuoteReq())
	if len(res.Venues) != 1 || res.Venues[0].Provider != ProviderPaycrest {
		t.Fatalf("res = %+v, want Paycrest's cheaper direct venue alone", res)
	}
}

// Review Focus 6: open tries the company wallet first; Flutterwave only after it refused.
func TestRouterOpenConverted_FlutterwaveOnlyAfterTheCompanyWalletRefused(t *testing.T) {
	tr := converting(ProviderTreasury, convVenue(ProviderTreasury, "base", "optimism", "1400"))
	tr.convResults = []openResult{{err: WrapClass(ErrVenueUnavailable, errors.New("daily cap"))}}
	fw := converting(ProviderFlutterwave, convVenue(ProviderFlutterwave, "polygon", "optimism", "1390"))
	fw.convResults = []openResult{{order: okOrder}}
	o, err := testRouter(tr, fw).OpenConverted(context.Background(), convOpenReq("", "1400"))
	if err != nil || o != okOrder || len(tr.convOpened) != 1 || len(fw.convOpened) != 1 {
		t.Fatalf("o=%v err=%v treasury=%d flutterwave=%d", o, err, len(tr.convOpened), len(fw.convOpened))
	}

	tr2 := converting(ProviderTreasury, convVenue(ProviderTreasury, "base", "optimism", "1400"))
	tr2.convResults = []openResult{{order: okOrder}}
	fw2 := converting(ProviderFlutterwave, convVenue(ProviderFlutterwave, "polygon", "optimism", "1390"))
	if _, err := testRouter(tr2, fw2).OpenConverted(context.Background(), convOpenReq("", "1400")); err != nil || len(fw2.convOpened) != 0 {
		t.Fatalf("err=%v flutterwave tried %d times, want never", err, len(fw2.convOpened))
	}
}

// A Flutterwave venue she confirmed opens as she saw it.
func TestRouterOpenConverted_AConfirmedFlutterwaveHandleOpensAsShown(t *testing.T) {
	tr := converting(ProviderTreasury, convVenue(ProviderTreasury, "base", "optimism", "1400"))
	fw := converting(ProviderFlutterwave, convVenue(ProviderFlutterwave, "polygon", "optimism", "1390"))
	fw.convResults = []openResult{{order: okOrder}}
	h := EncodeHandle(ProviderFlutterwave, VenueKey{"polygon", "USDC"})
	if o, err := testRouter(tr, fw).OpenConverted(context.Background(), convOpenReq(h, "1390")); err != nil || o != okOrder || len(tr.convOpened) != 0 {
		t.Fatalf("o=%v err=%v treasury tried %d", o, err, len(tr.convOpened))
	}
}

// Failover may land on a DIRECT venue for the same target.
func TestRouterOpenConverted_FailsOverToADirectVenueForTheTarget(t *testing.T) {
	tr := converting(ProviderTreasury, convVenue(ProviderTreasury, "base", "optimism", "1400"))
	tr.convResults = []openResult{{err: WrapClass(ErrVenueUnavailable, errors.New("stock short"))}}
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "optimism", "USDT", "1401"))
	pc.openResults = []openResult{{order: okOrder}}
	o, err := testRouter(tr, pc).OpenConverted(context.Background(), convOpenReq("", "1400"))
	if err != nil || o != okOrder || len(pc.opened) != 1 || pc.opened[0] != (VenueKey{"optimism", "USDT"}) {
		t.Fatalf("o=%v err=%v paycrest=%v", o, err, pc.opened)
	}
}

func TestRouterOpenConverted_NeedsATarget(t *testing.T) {
	req := convOpenReq("", "1400")
	req.Request.Want = nil
	if _, err := testRouter(converting(ProviderTreasury)).OpenConverted(context.Background(), req); !errors.Is(err, ErrInvalidQuote) {
		t.Fatalf("err = %v", err)
	}
}

// The rule is per target, so it holds on a direct pair too (spec §15.2).
func TestRouterQuote_CompanyWalletDropsFlutterwaveOnADirectPair(t *testing.T) {
	tr := fakeNamed(ProviderTreasury, venue(ProviderTreasury, "base", "USDC", "1400"))
	fw := fakeNamed(ProviderFlutterwave, venue(ProviderFlutterwave, "base", "USDC", "1390"))
	res, err := testRouter(tr, fw).Quote(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: "150000", Token: "USDC", ChainKey: "base"})
	if err != nil || len(res.Venues) != 1 || res.Venues[0].Provider != ProviderTreasury {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}
