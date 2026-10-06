package ramp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

func fakeNamed(name string, venues ...Venue) *fakeProvider {
	return &fakeProvider{name: name, quote: &ProviderQuote{MarketRate: 1348, Venues: venues}}
}

// feeVenue is a venue whose display rate differs from its all-in rate, as
// Flutterwave's does (spec §14).
func feeVenue(provider, chain, token, display, effective string) Venue {
	v := venue(provider, chain, token, display)
	v.EffectiveRate = effective
	v.Fees = []VenueFee{{Kind: "network", Label: "Network fee", Provider: "Flutterwave", Token: "1.5", Ngn: "2538.85"}}
	return v
}

func buyReq(token, chain string) Request {
	return Request{Side: Buy, FiatCode: "NGN", FiatAmount: "165025", Token: token, ChainKey: chain}
}

func TestOrder_CreatedAtReadsTheITIntent(t *testing.T) {
	at := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	if got := (Order{Provider: ProviderTreasury, IT: &models.ITIntent{CreatedAt: at}}).CreatedAt(); !got.Equal(at) {
		t.Fatalf("CreatedAt = %v", got)
	}
}

// §14 / D40: ranking is on the effective rate, not the display rate.
func TestRouterQuote_RanksOnTheEffectiveRate(t *testing.T) {
	// Flutterwave shows a lower display rate (1692.57) but costs more all-in (1745.61).
	fw := fakeNamed(ProviderFlutterwave, feeVenue(ProviderFlutterwave, "polygon", "USDC", "1692.57", "1745.61"))
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "polygon", "USDC", "1700.00"))
	res, err := testRouter(pc, fw).Quote(context.Background(), buyReq("USDC", "polygon"))
	if err != nil || res.Venues[0].Provider != ProviderPaycrest {
		t.Fatalf("best = %+v err = %v, want Paycrest's cheaper all-in rate", res.Best, err)
	}
	// Every venue leaves the router with both rates and a fee list.
	for _, v := range res.Venues {
		if v.EffectiveRate == "" || v.Fees == nil {
			t.Fatalf("venue %+v lacks effectiveRate or fees", v)
		}
	}
	raw, _ := json.Marshal(res.Venues[0])
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["effectiveRate"] != "1700.00" || m["rate"] != "1700.00" {
		t.Fatalf("paycrest venue JSON = %s", raw)
	}
	if fees, ok := m["fees"].([]any); !ok || len(fees) != 0 {
		t.Fatalf("paycrest fees = %#v, want []", m["fees"])
	}
}

// S6 / D36: an exact tie goes by registration order (treasury → paycrest →
// flutterwave in production). The company wallet and Flutterwave never tie on
// one target: the company wallet drops Flutterwave there (spec §15.2), so each
// tie is checked pairwise.
func TestRouterQuote_ExactTieFollowsRegistrationOrder(t *testing.T) {
	tr := fakeNamed(ProviderTreasury, venue(ProviderTreasury, "base", "USDC", "1382.57"))
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "base", "USDC", "1382.570"))
	fw := fakeNamed(ProviderFlutterwave, feeVenue(ProviderFlutterwave, "base", "USDC", "1300", "1382.57"))
	order := func(ps ...Provider) []string {
		res, err := testRouter(ps...).Quote(context.Background(), buyReq("USDC", "base"))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, v := range res.Venues {
			got = append(got, v.Provider)
		}
		return got
	}
	for _, c := range []struct {
		ps   []Provider
		want string
	}{
		{[]Provider{tr, pc}, "treasury,paycrest"},
		{[]Provider{pc, tr}, "paycrest,treasury"},
		{[]Provider{pc, fw}, "paycrest,flutterwave"},
		{[]Provider{fw, pc}, "flutterwave,paycrest"},
	} {
		if got := strings.Join(order(c.ps...), ","); got != c.want {
			t.Fatalf("order = %s, want %s: the tie-break must be registration order, nothing else", got, c.want)
		}
	}
}

// §11: only the company wallet quotes below Paycrest's and Flutterwave's minimums.
func TestRouterQuote_CompanyWalletAloneBelowTheOtherMinimums(t *testing.T) {
	tr := fakeNamed(ProviderTreasury, venue(ProviderTreasury, "base", "USDC", "1382.60"))
	pc := &fakeProvider{name: ProviderPaycrest, quoteErr: ErrBelowMinimum}
	fw := &fakeProvider{name: ProviderFlutterwave, quoteErr: WrapClass(ErrBelowMinimum, services.ErrStableBuyBelowMinimum)}
	res, err := testRouter(tr, pc, fw).Quote(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: "100"})
	if err != nil || len(res.Venues) != 1 || res.Venues[0].Provider != ProviderTreasury {
		t.Fatalf("%+v %v", res, err)
	}
}

// §11: Flutterwave wins at a size where it is cheaper; above-maximum drops the wallet.
func TestRouterQuote_FlutterwaveWinsWhenCheaperAndTheWalletDropsAboveItsMaximum(t *testing.T) {
	tr := &fakeProvider{name: ProviderTreasury, quoteErr: WrapClass(ErrAboveMaximum, services.ErrStableBuyAboveMaximum)}
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "polygon", "USDC", "1460"))
	fw := fakeNamed(ProviderFlutterwave, feeVenue(ProviderFlutterwave, "polygon", "USDC", "1420", "1440"))
	res, err := testRouter(tr, pc, fw).Quote(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: "1500000"})
	if err != nil || res.Best.Provider != ProviderFlutterwave {
		t.Fatalf("best = %+v err = %v", res.Best, err)
	}
}

// D13: with Paycrest's naira buy leg closed and the wallet out of stock, a
// small amount must say "below the minimum", not "unavailable".
func TestRouterQuote_RangeBeatsAClosedLeg(t *testing.T) {
	q := func(errs ...error) error {
		var ps []Provider
		for i, e := range errs {
			ps = append(ps, &fakeProvider{name: string(rune('a' + i)), quoteErr: e})
		}
		_, err := testRouter(ps...).Quote(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: "5000"})
		return err
	}
	if err := q(ErrLegClosed, ErrBelowMinimum); !errors.Is(err, ErrBelowMinimum) {
		t.Errorf("closed + below → %v", err)
	}
	if err := q(ErrLegClosed, ErrAboveMaximum); !errors.Is(err, ErrAboveMaximum) || errors.Is(err, ErrBelowMinimum) {
		t.Errorf("closed + above → %v", err)
	}
	if err := q(ErrBelowMinimum, ErrAboveMaximum); !errors.Is(err, ErrBelowMinimum) {
		t.Errorf("below + above → %v", err)
	}
	if err := q(ErrLegClosed, ErrLegClosed); !errors.Is(err, ErrLegClosed) {
		t.Errorf("all closed → %v", err)
	}
	if err := q(ErrLegClosed, errors.New("timeout")); !errors.Is(err, ErrUnavailable) {
		t.Errorf("closed + outage → %v", err)
	}
}

// §4.4: the company wallet and Paycrest share Base, so a wallet refusal that
// is about its own limits (daily cap, stock short at open) fails over there.
func TestRouterOpen_CompanyWalletFailsOverToPaycrestOnTheSamePair(t *testing.T) {
	tr := fakeNamed(ProviderTreasury, venue(ProviderTreasury, "base", "USDC", "1382.57"))
	tr.openResults = []openResult{{err: WrapClass(ErrVenueUnavailable, errors.New("daily cap"))}}
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "base", "USDC", "1384"))
	pc.openResults = []openResult{{order: okOrder}}
	o, err := testRouter(tr, pc).Open(context.Background(), openReq(EncodeHandle(ProviderTreasury, VenueKey{"base", "USDC"}), "1382.57"))
	if err != nil || o != okOrder || len(pc.opened) != 1 || pc.opened[0] != (VenueKey{"base", "USDC"}) {
		t.Fatalf("o=%v err=%v pc=%v", o, err, pc.opened)
	}
}

// §11: no failover on an already-open refusal.
func TestRouterOpen_AnOpenPurchaseNeverFailsOver(t *testing.T) {
	id := uuid.New()
	refusal := WrapClass(ErrRefusedForUser, &services.StableBuyOpenError{IntentID: id})
	tr := fakeNamed(ProviderTreasury, venue(ProviderTreasury, "base", "USDC", "1382.57"))
	tr.openResults = []openResult{{err: refusal}}
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "base", "USDC", "1384"))
	pc.openResults = []openResult{{order: okOrder}}
	_, err := testRouter(tr, pc).Open(context.Background(), openReq(EncodeHandle(ProviderTreasury, VenueKey{"base", "USDC"}), "1382.57"))
	var open *services.StableBuyOpenError
	if !errors.As(err, &open) || open.IntentID != id || len(pc.opened) != 0 {
		t.Fatalf("err=%v paycrest opened %v", err, pc.opened)
	}
}

// D40: tolerance is checked on the effective rate. Confirming the display
// rate (lower than the all-in one) must read as a moved price, never open.
func TestRouterOpen_ToleranceUsesTheEffectiveRate(t *testing.T) {
	mk := func() *fakeProvider {
		fw := fakeNamed(ProviderFlutterwave, feeVenue(ProviderFlutterwave, "polygon", "USDC", "1692.57", "1745.61"))
		fw.openResults = []openResult{{order: okOrder}}
		return fw
	}
	req := func(quoted string) OpenRequest {
		r := openReq("", quoted)
		r.Request.ChainKey = "polygon"
		return r
	}
	if o, err := testRouter(mk()).Open(context.Background(), req("1745.61")); err != nil || o != okOrder {
		t.Fatalf("o=%v err=%v — a venue at its confirmed effective rate must open", o, err)
	}
	fw := mk()
	_, err := testRouter(fw).Open(context.Background(), req("1692.57"))
	var moved *PriceMovedError
	if !errors.As(err, &moved) || len(fw.opened) != 0 {
		t.Fatalf("err=%v opened=%v — the display rate is not what she pays", err, fw.opened)
	}
}

// notYoursProvider answers a missing id the way PaycrestOrderService does.
type notYoursProvider struct{ *fakeProvider }

func (p notYoursProvider) Get(context.Context, uuid.UUID, string) (*Order, error) {
	return nil, services.ErrPaycrestOrderNotYours
}
func (p notYoursProvider) Cancel(context.Context, uuid.UUID, string) (*Order, error) {
	return nil, services.ErrPaycrestOrderNotYours
}

// D11: a bare id is any provider's; they are asked in registration order.
func TestRouterReads_BareIDWalksTheProvidersInOrder(t *testing.T) {
	id := uuid.NewString()
	fwOrder := &Order{Provider: ProviderFlutterwave, IT: &models.ITIntent{}}
	tr := &fakeProvider{name: ProviderTreasury}
	pc := notYoursProvider{&fakeProvider{name: ProviderPaycrest}}
	fw := &fakeProvider{name: ProviderFlutterwave, orders: map[string]*Order{id: fwOrder}}
	r := testRouter(tr, pc, fw)
	ctx := context.Background()
	if o, err := r.Get(ctx, uuid.New(), id); err != nil || o != fwOrder {
		t.Fatalf("bare get: %v %v", o, err)
	}
	if o, err := r.Cancel(ctx, uuid.New(), id); err != nil || o != fwOrder {
		t.Fatalf("bare cancel: %v %v", o, err)
	}
	if o, err := r.Get(ctx, uuid.New(), ProviderFlutterwave+":"+id); err != nil || o != fwOrder {
		t.Fatalf("prefixed: %v %v", o, err)
	}
	if _, err := r.Get(ctx, uuid.New(), ProviderTreasury+":"+id); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("wrong prefix: %v", err)
	}
	if _, err := r.Get(ctx, uuid.New(), uuid.NewString()); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("nobody's id: %v", err)
	}
}
