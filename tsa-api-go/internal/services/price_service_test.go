package services

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestPriceService(t *testing.T, handler http.HandlerFunc) (*PriceService, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	ps := NewPriceService()
	ps.SetBaseURLForTest(srv.URL)
	return ps, &hits
}

func TestGetOHLC_ParsesCoinGeckoRowsAndSnapsDays(t *testing.T) {
	var gotPath, gotDays string
	ps, _ := newTestPriceService(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotDays = r.URL.Path, r.URL.Query().Get("days")
		w.Write([]byte(`[[1000,1.0,1.2,0.9,1.1],[2000,1.1,1.3,1.0,1.25],[3000,1.25]]`))
	})

	candles, err := ps.GetOHLC("usdc", 20)
	if err != nil {
		t.Fatalf("GetOHLC: %v", err)
	}
	if gotPath != "/coins/usd-coin/ohlc" {
		t.Errorf("path = %q, want /coins/usd-coin/ohlc", gotPath)
	}
	if gotDays != "30" {
		t.Errorf("days = %q, want 30 (20 snapped up to a CoinGecko bucket)", gotDays)
	}
	want := []Candle{
		{Timestamp: 1000, Open: 1.0, High: 1.2, Low: 0.9, Close: 1.1},
		{Timestamp: 2000, Open: 1.1, High: 1.3, Low: 1.0, Close: 1.25},
	}
	if len(candles) != len(want) {
		t.Fatalf("got %d candles, want %d (short rows dropped): %+v", len(candles), len(want), candles)
	}
	for i := range want {
		if candles[i] != want[i] {
			t.Errorf("candle %d = %+v, want %+v", i, candles[i], want[i])
		}
	}
}

func TestGetOHLC_UnknownSymbol(t *testing.T) {
	ps, hits := newTestPriceService(t, func(w http.ResponseWriter, r *http.Request) {})
	_, err := ps.GetOHLC("NOPE", 1)
	if !errors.Is(err, ErrUnknownSymbol) {
		t.Fatalf("err = %v, want ErrUnknownSymbol", err)
	}
	if *hits != 0 {
		t.Errorf("made %d upstream calls for an unknown symbol", *hits)
	}
}

func TestGetOHLC_UpstreamErrorIsNotCached(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	ps, hits := newTestPriceService(t, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`[[1000,1,1,1,1]]`))
	})
	if _, err := ps.GetOHLC("ETH", 1); err == nil {
		t.Fatal("want error on 429")
	}
	fail.Store(false)
	candles, err := ps.GetOHLC("ETH", 1)
	if err != nil || len(candles) != 1 {
		t.Fatalf("retry after failure: candles=%v err=%v", candles, err)
	}
	if *hits != 2 {
		t.Errorf("hits = %d, want 2 (a failure must not be cached)", *hits)
	}
}

func TestSeriesAreCachedPerSymbolAndDays(t *testing.T) {
	ps, hits := newTestPriceService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/coins/ethereum/market_chart" {
			w.Write([]byte(`{"prices":[[1000,4000],[2000,4100]]}`))
			return
		}
		w.Write([]byte(`[[1000,1,1,1,1]]`))
	})

	for i := 0; i < 3; i++ {
		if _, err := ps.GetOHLC("ETH", 7); err != nil {
			t.Fatal(err)
		}
		if _, err := ps.GetHistoricalPrice("ETH", 7); err != nil {
			t.Fatal(err)
		}
	}
	if *hits != 2 {
		t.Fatalf("hits = %d after repeated reads, want 2 (one per series)", *hits)
	}
	if _, err := ps.GetOHLC("ETH", 30); err != nil {
		t.Fatal(err)
	}
	if *hits != 3 {
		t.Errorf("hits = %d, want 3 (a different range is a different series)", *hits)
	}
}

func TestSnapChartDays(t *testing.T) {
	cases := map[int]int{0: 1, 1: 1, 2: 7, 7: 7, 8: 14, 30: 30, 31: 90, 90: 90, 100: 180, 365: 365, 900: 365}
	for in, want := range cases {
		if got := SnapChartDays(in); got != want {
			t.Errorf("SnapChartDays(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestGetHistoricalPrice_SnapsDaysSoOddRangesShareACacheEntry(t *testing.T) {
	var gotDays []string
	var mu sync.Mutex
	ps, hits := newTestPriceService(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotDays = append(gotDays, r.URL.Query().Get("days"))
		mu.Unlock()
		w.Write([]byte(`{"prices":[[1000,1],[2000,2]]}`))
	})
	for _, d := range []int{20, 21, 29, 30} {
		if _, err := ps.GetHistoricalPrice("ETH", d); err != nil {
			t.Fatal(err)
		}
	}
	if *hits != 1 || gotDays[0] != "30" {
		t.Fatalf("hits = %d, days sent = %v; want one call for days=30", *hits, gotDays)
	}
}

func TestSeriesConcurrentMissesShareOneUpstreamCall(t *testing.T) {
	release := make(chan struct{})
	ps, hits := newTestPriceService(t, func(w http.ResponseWriter, r *http.Request) {
		<-release // hold the first call open so the others pile up behind it
		w.Write([]byte(`[[1000,1,1,1,1]]`))
	})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ps.GetOHLC("ETH", 1); err != nil {
				t.Error(err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if *hits != 1 {
		t.Errorf("hits = %d, want 1 (concurrent misses must share one fetch)", *hits)
	}
}

func TestAggregateCandles(t *testing.T) {
	mk := func(n int) []Candle {
		out := make([]Candle, n)
		for i := range out {
			f := float64(i)
			out[i] = Candle{Timestamp: int64(i), Open: f, High: f + 10, Low: f - 10, Close: f + 0.5}
		}
		return out
	}

	t.Run("under the cap is returned untouched", func(t *testing.T) {
		in := mk(5)
		if got := AggregateCandles(in, 5); len(got) != 5 || got[4] != in[4] {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("merges into at most max bars, newest group full", func(t *testing.T) {
		got := AggregateCandles(mk(10), 4) // group size 3 → [0] [1..3] [4..6] [7..9]
		if len(got) != 4 {
			t.Fatalf("len = %d, want 4", len(got))
		}
		last := got[3]
		want := Candle{Timestamp: 9, Open: 7, High: 19, Low: -3, Close: 9.5}
		if last != want {
			t.Errorf("last bar = %+v, want %+v", last, want)
		}
		first := got[0]
		if first != (Candle{Timestamp: 0, Open: 0, High: 10, Low: -10, Close: 0.5}) {
			t.Errorf("leading partial bar = %+v", first)
		}
	})
}

// fakeClock lets cache-expiry tests move time without sleeping.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestSeries_ServesLastGoodDataWhenCoinGeckoFails(t *testing.T) {
	var fail atomic.Bool
	ps, hits := newTestPriceService(t, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`[[1000,1,2,0.5,1.5],[2000,1.5,2,1,1.8]]`))
	})
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	ps.now = clock.now

	if _, err := ps.GetOHLC("ETH", 1); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	clock.advance(10 * time.Minute) // past the 24H TTL, inside the stale window

	candles, err := ps.GetOHLC("ETH", 1)
	if err != nil || len(candles) != 2 {
		t.Fatalf("want the last good 2 candles while CoinGecko is throttling, got %v, %v", candles, err)
	}
	if *hits != 2 {
		t.Errorf("hits = %d, want 2 (expired entry must still try upstream first)", *hits)
	}
}

func TestSeries_StaleDataHasAnAgeLimit(t *testing.T) {
	var fail atomic.Bool
	ps, _ := newTestPriceService(t, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`[[1000,1,2,0.5,1.5],[2000,1.5,2,1,1.8]]`))
	})
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	ps.now = clock.now

	if _, err := ps.GetOHLC("ETH", 1); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	clock.advance(2 * time.Hour) // a "24H" chart two hours old is no longer the last 24 hours

	if _, err := ps.GetOHLC("ETH", 1); err == nil {
		t.Fatal("want an error once the stale copy is too old to pass off as current")
	}
}

func TestSeriesTTL_GrowsWithRange(t *testing.T) {
	if !(seriesTTL(1) < seriesTTL(7) && seriesTTL(7) < seriesTTL(30) && seriesTTL(30) < seriesTTL(365)) {
		t.Fatalf("TTLs must grow with the range: 1d=%v 7d=%v 30d=%v 365d=%v",
			seriesTTL(1), seriesTTL(7), seriesTTL(30), seriesTTL(365))
	}
	if seriesTTL(1) > 5*time.Minute {
		t.Errorf("24H TTL = %v; a day chart must not lag more than 5 minutes", seriesTTL(1))
	}
}

func TestCoinIDs_WalletTokensAreMappedAndMCGPIsNot(t *testing.T) {
	ps := NewPriceService()
	for _, sym := range []string{"POL", "SUI", "WBTC", "WETH", "USDT", "S"} {
		if _, err := ps.coinID(sym); err != nil {
			t.Errorf("%s: %v", sym, err)
		}
	}
	// MCGP is not listed on CoinGecko; a mapping only produces upstream 404s.
	if _, err := ps.coinID("MCGP"); !errors.Is(err, ErrUnknownSymbol) {
		t.Errorf("MCGP: err = %v, want ErrUnknownSymbol", err)
	}
}

func TestCoinGeckoKey_SentAsHeaderOnEveryCall(t *testing.T) {
	cases := []struct {
		name, key, wantHeader string
		pro                   bool
	}{
		{"keyless", "", "", false},
		{"demo", "CG-demo", "x-cg-demo-api-key", false},
		{"pro", "CG-pro", "x-cg-pro-api-key", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got http.Header
			var mu sync.Mutex
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				got = r.Header.Clone()
				mu.Unlock()
				switch {
				case strings.HasSuffix(r.URL.Path, "/simple/price"):
					w.Write([]byte(`{"tether":{"usd":1}}`))
				default:
					w.Write([]byte(`[[1000,1,1,1,1],[2000,1,1,1,1]]`))
				}
			}))
			t.Cleanup(srv.Close)
			ps := NewPriceService()
			ps.UseAPIKey(tc.key, tc.pro)
			if tc.pro && ps.baseURL != coinGeckoProBaseURL {
				t.Errorf("pro key must use %s, got %s", coinGeckoProBaseURL, ps.baseURL)
			}
			ps.SetBaseURLForTest(srv.URL)

			for _, call := range []func() error{
				func() error { _, err := ps.GetPrices([]string{"USDT"}); return err },
				func() error { _, err := ps.GetOHLC("ETH", 1); return err },
			} {
				if err := call(); err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				h := got
				mu.Unlock()
				for _, name := range []string{"x-cg-demo-api-key", "x-cg-pro-api-key"} {
					want := ""
					if name == tc.wantHeader {
						want = tc.key
					}
					if h.Get(name) != want {
						t.Errorf("%s = %q, want %q", name, h.Get(name), want)
					}
				}
			}
		})
	}
}

func TestGetPrices_SameSymbolsInAnyOrderShareACacheEntry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write([]byte(`{"tether":{"usd":1},"ethereum":{"usd":2000}}`))
	}))
	t.Cleanup(srv.Close)
	ps := NewPriceService()
	ps.SetBaseURLForTest(srv.URL)

	for _, symbols := range [][]string{{"USDT", "ETH"}, {"ETH", "USDT"}} {
		if _, err := ps.GetPrices(symbols); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("CoinGecko called %d times, want 1", got)
	}
}
