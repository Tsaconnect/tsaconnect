package services

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

const lifiQuoteBody = `{
  "tool": "across",
  "estimate": {"toAmount": "99800000", "toAmountMin": "99700000", "approvalAddress": "0xAppr", "executionDuration": 32},
  "transactionRequest": {"to": "0xRouter", "data": "0xdeadbeef", "value": "0x0", "gasLimit": "0x36b0"}
}`

func TestLiFiQuote_ParsesARoute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fromAmount") != "100000000" {
			t.Errorf("fromAmount = %q", r.URL.Query().Get("fromAmount"))
		}
		if r.URL.Query().Get("toAddress") != "0xUser" {
			t.Errorf("toAddress = %q — the route must deliver to the user", r.URL.Query().Get("toAddress"))
		}
		w.Write([]byte(lifiQuoteBody))
	}))
	defer srv.Close()

	route, err := NewLiFiQuoteClient(srv.URL, "", "tsa").Quote(context.Background(), LiFiQuoteRequest{
		FromChainID: 8453, ToChainID: 56, FromToken: "0xUSDC", ToToken: "0xUSDT",
		FromAddress: "0xTreasury", ToAddress: "0xUser", FromAmount: "100000000",
	})
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if route.ToAmountMin.String() != "99700000" || route.Tool != "across" || route.ETASeconds != 32 {
		t.Fatalf("route = %+v", route)
	}
	if route.To != "0xRouter" || route.Data != "0xdeadbeef" || route.GasLimit != 14000 {
		t.Fatalf("tx request = %+v", route)
	}
}

func TestLiFiQuote_NoRouteIsATypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"No available quotes for the requested transfer"}`))
	}))
	defer srv.Close()
	if _, err := NewLiFiQuoteClient(srv.URL, "", "tsa").Quote(context.Background(), LiFiQuoteRequest{}); !errors.Is(err, ErrLiFiNoRoute) {
		t.Fatalf("want ErrLiFiNoRoute, got %v", err)
	}
}

func lifiQuoteServer(t *testing.T, check func(url.Values), body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r.URL.Query())
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const lifiQuoteFixture = `{"tool":"across","action":{"fromToken":{"address":"0xA"},"toToken":{"address":"0x0000000000000000000000000000000000000000","decimals":18,"symbol":"BNB"}},
 "estimate":{"toAmount":"990","toAmountMin":"989","fromAmountUSD":"100.00","toAmountUSD":"99.70","executionDuration":30,
 "gasCosts":[{"amountUSD":"0.02"},{"amountUSD":"0.01"}]},
 "transactionRequest":{"to":"0xB","data":"0x01","value":"0x0","gasLimit":"0x3d090"}}`

func TestLiFiQuote_ParsesUSDDecimalsAndKeepsTheRawBody(t *testing.T) {
	srv := lifiQuoteServer(t, func(q url.Values) {
		if q.Get("fee") != "0.0025" || q.Get("slippage") != "0.005" || q.Get("allowDestinationCall") != "false" {
			t.Errorf("fee/slippage/allowDestinationCall = %q/%q/%q", q.Get("fee"), q.Get("slippage"), q.Get("allowDestinationCall"))
		}
	}, lifiQuoteFixture, 200)
	route, err := NewLiFiQuoteClient(srv.URL, "", "").Quote(context.Background(), LiFiQuoteRequest{FeeBps: 25, Slippage: 0.005})
	if err != nil {
		t.Fatal(err)
	}
	if route.FromAmountUSD != 100 || route.ToAmountUSD != 99.70 || math.Abs(route.GasCostUSD-0.03) > 1e-9 {
		t.Fatalf("usd = %v/%v/%v", route.FromAmountUSD, route.ToAmountUSD, route.GasCostUSD)
	}
	if route.ToTokenDecimals != 18 || route.ToTokenSymbol != "BNB" || len(route.Raw) == 0 {
		t.Fatalf("decimals = %d raw = %d bytes", route.ToTokenDecimals, len(route.Raw))
	}
}

func TestLiFiQuote_DefaultsToStableSlippageAndNoFee(t *testing.T) {
	srv := lifiQuoteServer(t, func(q url.Values) {
		if q.Get("slippage") != "0.001" || q.Has("fee") {
			t.Errorf("slippage = %q, fee present = %v", q.Get("slippage"), q.Has("fee"))
		}
	}, lifiQuoteFixture, 200)
	if _, err := NewLiFiQuoteClient(srv.URL, "", "").Quote(context.Background(), LiFiQuoteRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestLiFiQuote_UnconfiguredFeeIsItsOwnError(t *testing.T) {
	srv := lifiQuoteServer(t, nil, `{"message":"Integrator tsa-connect is not configured for collecting fees on chain 56"}`, 400)
	_, err := NewLiFiQuoteClient(srv.URL, "", "").Quote(context.Background(), LiFiQuoteRequest{FeeBps: 30})
	if !errors.Is(err, ErrLiFiFeeUnconfigured) {
		t.Fatalf("err = %v, want ErrLiFiFeeUnconfigured", err)
	}
}
