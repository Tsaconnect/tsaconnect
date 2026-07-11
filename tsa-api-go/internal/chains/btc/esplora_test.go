package btc

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFeeRateSatVBPrefersEsploraEstimates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/fee-estimates", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"1": 25.1, "3": 12.4, "6": 6.0}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rate, err := NewEsplora(srv.URL).FeeRateSatVB(context.Background())
	if err != nil {
		t.Fatalf("FeeRateSatVB: %v", err)
	}
	if rate != 12.4 { // 3-block target preferred
		t.Fatalf("rate want 12.4 got %v", rate)
	}
}

func TestFeeRateSatVBFallsBackToMempoolRecommended(t *testing.T) {
	mux := http.NewServeMux()
	// mempool.space-style deployment: no /fee-estimates, only /v1/fees/recommended.
	mux.HandleFunc("/fee-estimates", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/v1/fees/recommended", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"fastestFee": 30, "halfHourFee": 15, "hourFee": 8}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rate, err := NewEsplora(srv.URL).FeeRateSatVB(context.Background())
	if err != nil {
		t.Fatalf("FeeRateSatVB: %v", err)
	}
	if rate != 15 {
		t.Fatalf("rate want 15 (halfHourFee) got %v", rate)
	}
}

func TestFeeRateSatVBFloorsAtOneSatPerVB(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/fee-estimates", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"3": 0.101}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rate, err := NewEsplora(srv.URL).FeeRateSatVB(context.Background())
	if err != nil {
		t.Fatalf("FeeRateSatVB: %v", err)
	}
	if rate != 1 {
		t.Fatalf("rate want floor 1 got %v", rate)
	}
}

func TestFeeRateSatVBErrorsWhenBothSourcesFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := NewEsplora(srv.URL).FeeRateSatVB(context.Background()); err == nil {
		t.Fatal("want error when both fee sources fail — a made-up rate must never be used")
	}
}

func TestBroadcastSurfacesNodeRejectReason(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/tx", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "sendrawtransaction RPC error: min relay fee not met", http.StatusBadRequest)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := NewEsplora(srv.URL).Broadcast(context.Background(), "deadbeef")
	if err == nil || !strings.Contains(err.Error(), "min relay fee not met") {
		t.Fatalf("want node reject reason surfaced, got %v", err)
	}
}

func TestGetJSONReturnsHTTPErrorWithStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Address on invalid network", http.StatusBadRequest)
	}))
	defer srv.Close()

	_, err := NewEsplora(srv.URL).GetAddressStats(context.Background(), "tb1qwrong")
	he, ok := err.(*httpError)
	if !ok || he.status != http.StatusBadRequest {
		t.Fatalf("want *httpError with 400, got %T %v", err, err)
	}
}
