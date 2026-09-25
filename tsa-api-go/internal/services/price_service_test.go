package services

import (
	"errors"
	"net/http"
	"net/http/httptest"
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
