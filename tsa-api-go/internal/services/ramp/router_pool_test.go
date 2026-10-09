package ramp

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakePool is a pool provider with a band (pool_band).
type fakePool struct {
	*fakeProvider
	band int64
}

func (p *fakePool) BandBps() int64 { return p.band }

func pool(rate string) *fakePool {
	return &fakePool{fakeProvider: fakeNamed(ProviderPool, venue(ProviderPool, "bsc", "USDT", rate)), band: 50}
}

func names(vs []Venue) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Provider + ":" + v.ChainKey + ":" + v.Token + "@" + v.RankRate()
	}
	return out
}

func bscReq() Request { return buyReq("USDT", "bsc") }

// S17: within 0.5% of the cheapest third party the pool goes first for its target.
func TestRouter_PoolWinsWithinBand(t *testing.T) {
	r := NewRouter(Config{}, pool("1385.00"), fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "bsc", "USDT", "1380.00")))
	q, err := r.Quote(context.Background(), bscReq())
	if err != nil || q.Venues[0].Provider != ProviderPool || len(q.Venues) != 2 {
		t.Fatalf("pinned quote = %v %v, want the pool first then Paycrest", names(q.Venues), err)
	}
	q, _ = r.Quote(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: "100000"})
	if len(q.Venues) != 1 || q.Venues[0].Provider != ProviderPool {
		t.Fatalf("unpinned = %v, want one venue for bsc/USDT: the pool", names(q.Venues))
	}
}

// Outside the band the pool is not shown; what she sees is today's quote.
func TestRouter_PoolCompetesOutsideBand(t *testing.T) {
	r := NewRouter(Config{}, pool("1390.00"), fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "bsc", "USDT", "1380.00")))
	q, _ := r.Quote(context.Background(), bscReq())
	if len(q.Venues) != 1 || q.Venues[0].Provider != ProviderPaycrest {
		t.Fatalf("quote = %v, want Paycrest alone (1390 > 1380 × 1.005)", names(q.Venues))
	}
}

// The band is measured against Paycrest/Flutterwave only; the company wallet
// is never compared, and the preferred pool sits ahead of it.
func TestRouter_PoolBandIgnoresCompanyWallet(t *testing.T) {
	r := NewRouter(Config{}, pool("1385.00"),
		fakeNamed(ProviderTreasury, venue(ProviderTreasury, "bsc", "USDT", "1370.00")),
		fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "bsc", "USDT", "1380.00")))
	q, _ := r.Quote(context.Background(), bscReq())
	if q.Venues[0].Provider != ProviderPool || q.Venues[1].Provider != ProviderTreasury {
		t.Fatalf("quote = %v, want pool, company wallet, Paycrest", names(q.Venues))
	}
}

func TestRouter_PoolPreferredWhenNoThirdParty(t *testing.T) {
	r := NewRouter(Config{}, pool("1500.00"), fakeNamed(ProviderTreasury, venue(ProviderTreasury, "bsc", "USDT", "1370.00")))
	q, _ := r.Quote(context.Background(), bscReq())
	if q.Venues[0].Provider != ProviderPool {
		t.Fatalf("quote = %v, want the pool first (no third party to compare)", names(q.Venues))
	}
}

func TestRouter_CompanyWalletFirstUnchanged(t *testing.T) {
	r := NewRouter(Config{}, pool("1500.00"),
		fakeNamed(ProviderTreasury, venue(ProviderTreasury, "base", "USDC", "1390.00")),
		fakeNamed(ProviderFlutterwave, venue(ProviderFlutterwave, "base", "USDC", "1380.00")))
	q, _ := r.Quote(context.Background(), buyReq("USDC", "base"))
	if len(q.Venues) != 1 || q.Venues[0].Provider != ProviderTreasury {
		t.Fatalf("quote = %v, want only the company wallet (Flutterwave dropped as today)", names(q.Venues))
	}
}

// S30: pool=1 returns the pool alone, whatever the band; else today's result
// with poolAvailable=false.
func TestRouter_PoolOnlyQuote(t *testing.T) {
	r := NewRouter(Config{}, pool("1450.00"), fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "bsc", "USDT", "1380.00")))
	req := bscReq()
	req.PoolOnly = true
	q, _ := r.Quote(context.Background(), req)
	if len(q.Venues) != 1 || q.Venues[0].Provider != ProviderPool || q.PoolAvailable == nil || !*q.PoolAvailable {
		t.Fatalf("pool-only = %v avail=%v", names(q.Venues), q.PoolAvailable)
	}
	r2 := NewRouter(Config{}, fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "bsc", "USDT", "1380.00")))
	q, _ = r2.Quote(context.Background(), req)
	if len(q.Venues) != 1 || q.Venues[0].Provider != ProviderPaycrest || q.PoolAvailable == nil || *q.PoolAvailable {
		t.Fatalf("no pool = %v avail=%v", names(q.Venues), q.PoolAvailable)
	}
	q, _ = r2.Quote(context.Background(), bscReq())
	if q.PoolAvailable != nil {
		t.Fatal("poolAvailable is only set on a pool=1 quote")
	}
}

// Roadmap risk 4: with or without a pool provider, every non-pool venue is
// shown in exactly today's order.
func TestRouter_NonPoolOrderingUnchanged(t *testing.T) {
	providers := func() []Provider {
		return []Provider{
			fakeNamed(ProviderTreasury, venue(ProviderTreasury, "base", "USDC", "1390.00")),
			fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "base", "USDC", "1385.00"), venue(ProviderPaycrest, "bsc", "USDT", "1380.00"),
				venue(ProviderPaycrest, "polygon", "USDT", "1379.00")),
			fakeNamed(ProviderFlutterwave, venue(ProviderFlutterwave, "polygon", "USDC", "1395.00"), venue(ProviderFlutterwave, "base", "USDC", "1370.00")),
		}
	}
	for _, req := range []Request{bscReq(), buyReq("USDC", "base"), {Side: Buy, FiatCode: "NGN", FiatAmount: "100000"}} {
		want, _ := NewRouter(Config{}, providers()...).Quote(context.Background(), req)
		// Out of band: nothing changes at all.
		got, _ := NewRouter(Config{}, append([]Provider{pool("1500.00")}, providers()...)...).Quote(context.Background(), req)
		if !reflect.DeepEqual(names(got.Venues), names(want.Venues)) {
			t.Fatalf("%+v: out-of-band pool changed the quote:\n got %v\nwant %v", req, names(got.Venues), names(want.Venues))
		}
		// In band: the pool is inserted for bsc/USDT and every other venue keeps its order.
		got, _ = NewRouter(Config{}, append([]Provider{pool("1381.00")}, providers()...)...).Quote(context.Background(), req)
		strip := func(vs []Venue) []string {
			var out []string
			for _, v := range vs {
				if v.Target() != (VenueKey{"bsc", "USDT"}) {
					out = append(out, names([]Venue{v})[0])
				}
			}
			return out
		}
		if !reflect.DeepEqual(strip(got.Venues), strip(want.Venues)) {
			t.Fatalf("%+v: in-band pool reordered other targets:\n got %v\nwant %v", req, strip(got.Venues), strip(want.Venues))
		}
	}
}

// recorder logs every Open attempt in one shared, ordered log.
type recorder struct {
	Provider
	log *[]string
}

func (r recorder) Open(ctx context.Context, k VenueKey, req Request, in OrderInput) (*Order, error) {
	*r.log = append(*r.log, r.Name())
	return r.Provider.Open(ctx, k, req, in)
}

func (r recorder) BandBps() int64 {
	if b, ok := r.Provider.(PoolBander); ok {
		return b.BandBps()
	}
	return 0
}

// §11.2 step 5: in band the pool is tried first; out of band, last.
func TestRouterOpen_PoolFirstInBandLastOutOfBand(t *testing.T) {
	open := func(p *fakePool, pc *fakeProvider) []string {
		var log []string
		r := NewRouter(Config{}, recorder{p, &log}, recorder{pc, &log})
		req := openReq("", "1400.00")
		req.Request.Token, req.Request.ChainKey = "USDT", "bsc"
		_, _ = r.Open(context.Background(), req)
		return log
	}
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "bsc", "USDT", "1380.00"))
	pc.openResults = []openResult{{order: okOrder}}
	if got := open(pool("1381.00"), pc); !reflect.DeepEqual(got, []string{ProviderPool, ProviderPaycrest}) {
		t.Fatalf("in band: %v, want the pool tried first then Paycrest", got)
	}
	pc2 := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "bsc", "USDT", "1380.00"))
	pc2.openResults = []openResult{{err: ErrVenueUnavailable}}
	if got := open(pool("1450.00"), pc2); !reflect.DeepEqual(got, []string{ProviderPaycrest, ProviderPool}) {
		t.Fatalf("out of band: %v, want Paycrest first and the pool as the last failover", got)
	}
}

// Several in-band pool venues for one target (a direct and a converted venue
// reaching the same asset) stay best-first ahead of the third party.
func TestRouter_PoolsSharingATargetStayBestFirst(t *testing.T) {
	p := pool("1383.00")
	p.quote.Venues = append(p.quote.Venues, venue(ProviderPool, "bsc", "USDT", "1381.00"))
	r := NewRouter(Config{}, p, fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "bsc", "USDT", "1380.00")))
	q, _ := r.Quote(context.Background(), bscReq())
	want := []string{"tsa_pool:bsc:USDT@1381.00", "tsa_pool:bsc:USDT@1383.00", "paycrest:bsc:USDT@1380.00"}
	if !reflect.DeepEqual(names(q.Venues), want) {
		t.Fatalf("quote = %v, want %v", names(q.Venues), want)
	}
}

func poolCardOpen(handle, quoted string) OpenRequest {
	req := openReq(handle, quoted)
	req.Request.Token, req.Request.ChainKey, req.Request.PoolOnly = "USDT", "bsc", true
	return req
}

// Review I-3 / decision 14: a pool-card open is the pool's order or none —
// it never fails over to another provider, in band or not.
func TestRouterOpen_PoolCardNeverBecomesAnotherProvidersOrder(t *testing.T) {
	for _, handle := range []string{"", EncodeHandle(ProviderPool, VenueKey{"bsc", "USDT"})} {
		p := pool("1381.00")
		p.openResults = []openResult{{err: ErrVenueUnavailable}}
		pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "bsc", "USDT", "1380.00"))
		pc.openResults = []openResult{{order: okOrder}}
		o, err := testRouter(p, pc).Open(context.Background(), poolCardOpen(handle, "1381.00"))
		if o != nil || !errors.Is(err, ErrUnavailable) || len(pc.opened) != 0 {
			t.Fatalf("handle %q: o=%v err=%v paycrest opened=%v, want no order and Paycrest untouched", handle, o, err, pc.opened)
		}
	}
}

// On the card's screen she may tap another provider's offer (the pool could
// not fill): that is her choice, and it opens — and fails over — as any open does.
func TestRouterOpen_PoolCardFallbackChoiceOpens(t *testing.T) {
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "bsc", "USDT", "1380.00"))
	pc.openResults = []openResult{{err: ErrVenueUnavailable}}
	fw := fakeNamed(ProviderFlutterwave, venue(ProviderFlutterwave, "bsc", "USDT", "1381.00"))
	fw.openResults = []openResult{{order: okOrder}}
	o, err := testRouter(pc, fw).Open(context.Background(), poolCardOpen(EncodeHandle(ProviderPaycrest, VenueKey{"bsc", "USDT"}), "1380.00"))
	if err != nil || o != okOrder || len(fw.opened) != 1 {
		t.Fatalf("o=%v err=%v fw=%v, want her Paycrest choice to fail over normally", o, err, fw.opened)
	}
}

// Review I-3: a pool-card open's "price moved" re-show is the pool-only list,
// shaped exactly as the pool=1 quote shapes it.
func TestRouterOpen_PoolCardPriceMovedIsPoolOnly(t *testing.T) {
	p := pool("1450.00") // moved past the 50 bps tolerance on 1381
	pc := fakeNamed(ProviderPaycrest, venue(ProviderPaycrest, "bsc", "USDT", "1380.00"))
	_, err := testRouter(p, pc).Open(context.Background(), poolCardOpen("", "1381.00"))
	var pm *PriceMovedError
	if !errors.As(err, &pm) || pm.Quote.PoolAvailable == nil || !*pm.Quote.PoolAvailable ||
		!reflect.DeepEqual(names(pm.Quote.Venues), []string{"tsa_pool:bsc:USDT@1450.00"}) || len(pc.opened) != 0 {
		t.Fatalf("err=%v quote=%+v paycrest opened=%v, want the pool-only re-show", err, pm, pc.opened)
	}
}
