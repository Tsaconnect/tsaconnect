package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
