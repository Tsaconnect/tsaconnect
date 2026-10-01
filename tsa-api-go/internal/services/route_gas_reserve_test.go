package services

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// route_gas_reserve_test.go — a source-chain gas top-up must cover what the
// node RESERVES before it accepts her transactions (gas limit × the price she
// signs at), not what LiFi estimates they will use.
//
// The case is a real one (2026-10-01, Base): LiFi estimated ~163k gas for the
// swap but signed it at a 605,014 limit, both at 6,000,000 wei. The top-up was
// sized from the estimate (2.2e12 wei), so the node refused the swap with
// "insufficient funds … have 1928688019864 want 3630084000000".

const lifiPricedTxBody = `{
  "tool": "relay",
  "estimate": {"toAmount": "160000", "toAmountMin": "159000", "executionDuration": 4},
  "transactionRequest": {"to": "0xRouter", "data": "0xdeadbeef", "value": "0x0",
    "gasLimit": "0x93b5e", "gasPrice": "0x5b8d80"}
}`

func TestLiFiQuote_ParsesTheGasPriceSheSignsAt(t *testing.T) {
	for name, body := range map[string]string{
		"legacy gasPrice": lifiPricedTxBody,
		"EIP-1559 maxFeePerGas": `{"tool":"relay","estimate":{"toAmount":"1","toAmountMin":"1"},
		  "transactionRequest":{"to":"0xR","data":"0x01","value":"0x0","gasLimit":"0x93b5e","maxFeePerGas":"0x5b8d80","maxPriorityFeePerGas":"0x1"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(body)) }))
			defer srv.Close()
			route, err := NewLiFiQuoteClient(srv.URL, "", "").Quote(context.Background(), LiFiQuoteRequest{FromAmount: "1"})
			if err != nil {
				t.Fatal(err)
			}
			if route.GasLimit != 605_022 || route.TxGasPriceWei == nil || route.TxGasPriceWei.Int64() != 6_000_000 {
				t.Fatalf("gasLimit/price = %d/%v, want 605022/6000000", route.GasLimit, route.TxGasPriceWei)
			}
		})
	}
}

// incidentRoute is the 2026-10-01 swap: LiFi's estimate under-states what the
// node reserves by ~3.7x.
func incidentRoute() *LiFiRoute {
	return &LiFiRoute{
		GasLimit: 605_014, TxGasPriceWei: big.NewInt(6_000_000),
		GasCostWei: big.NewInt(979_000_000_000), GasCostUSD: 0.004, FromAmountUSD: 0.40,
	}
}

func TestRouteGasReserve_CoversTheApproveAndSwapLimitsAtTheSignedPrice(t *testing.T) {
	r := incidentRoute()
	reserve := routeGasReserveWei(r)
	// The node wanted 3,630,084,000,000 for the swap alone; the approve before
	// it is signed with a 65,000 limit at the same price.
	limits := new(big.Int).Mul(big.NewInt(605_014+lifiApproveGasLimit), big.NewInt(6_000_000))
	if reserve == nil || reserve.Cmp(limits) <= 0 {
		t.Fatalf("reserve = %v, want more than the signed limits %v (room for the L1 data fee)", reserve, limits)
	}
	if got, ok := new(big.Int).SetString(quotedTopUpWei(r), 10); !ok || got.Cmp(reserve) != 0 {
		t.Fatalf("quotedTopUpWei = %q, want the reserve %v", quotedTopUpWei(r), reserve)
	}
	// Her leftover 1,928,688,019,864 wei must read as "needs gas".
	q := &RampSellQuoter{nativeBalance: func(context.Context, int64, string) (*big.Int, bool) {
		return big.NewInt(1_928_688_019_864), true
	}}
	in := SellQuoteInput{FromChainID: 8453, FromToken: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", FromAddress: sellTestAddress}
	if !q.needsTopUp(context.Background(), in, r) {
		t.Fatal("a wallet holding less than the node reserves was judged funded")
	}
	// Priced at the gas's own USD rate, so what we charge matches what we send.
	wantUSD := 0.004 * float64(reserve.Int64()) / 979_000_000_000
	if got := TopUpUSDForRoute(r); got < wantUSD*0.999 || got > wantUSD*1.001 {
		t.Fatalf("TopUpUSDForRoute = %v, want %v", got, wantUSD)
	}
}

// A route LiFi gave no signing price keeps today's estimate-based sizing.
func TestRouteGasReserve_FallsBackToTheEstimateWithoutASignedPrice(t *testing.T) {
	r := incidentRoute()
	r.TxGasPriceWei = nil
	want := new(big.Int).Mul(RouteGasWei(r, treasuryERC20Gas), big.NewInt(topUpGasBuffer))
	if got := routeGasReserveWei(r); got == nil || got.Cmp(want) != 0 {
		t.Fatalf("reserve = %v, want the estimate-based %v", got, want)
	}
}

// Paying with a chain's own coin there is no approve: the reserve is the swap's
// signed limit at its price, plus the L1-fee margin — not LiFi's estimate.
func TestRouteSwapReserve_IsTheSwapLimitAtTheSignedPrice(t *testing.T) {
	r := incidentRoute()
	want := big.NewInt(605_014 * 6_000_000 * (100 + routeGasReserveMarginPct) / 100)
	if got := routeSwapReserveWei(r); got == nil || got.Cmp(want) != 0 {
		t.Fatalf("swap reserve = %v, want %v", got, want)
	}
	r.TxGasPriceWei = nil
	fallback := new(big.Int).Mul(RouteGasWei(r, 0), big.NewInt(topUpGasBuffer))
	if got := routeSwapReserveWei(r); got == nil || got.Cmp(fallback) != 0 {
		t.Fatalf("unpriced swap reserve = %v, want the buffered estimate %v", got, fallback)
	}
}

// A top-up is priced in dollars from the gas's own USD-per-wei. With no wei
// cost there is no such rate, so the top-up is an unknown on BOTH sides —
// never sized from the limit while priced from the estimate.
func TestRouteGasReserve_NoWeiCostIsAnUnknownOnBothSides(t *testing.T) {
	r := incidentRoute()
	r.GasCostWei = nil
	if got := routeGasReserveWei(r); got != nil {
		t.Fatalf("reserve = %v, want nil (no USD-per-wei to price it at)", got)
	}
	if got := quotedTopUpWei(r); got != "" {
		t.Fatalf("quotedTopUpWei = %q, want none", got)
	}
}

// The app signs every route's approve at a fixed limit; the reserve assumes
// the same one. Read it from the app so the two cannot drift apart silently.
func TestLiFiApproveGasLimit_MatchesTheApp(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "tsa-app", "services", "lifiFlow.ts"))
	if err != nil {
		t.Skipf("tsa-app not checked out beside the API: %v", err)
	}
	m := regexp.MustCompile(`approveTx\s*=\s*\{[^}]*?gasLimit:\s*'(\d+)'`).FindSubmatch(src)
	if m == nil {
		t.Fatal("approveTx.gasLimit not found in tsa-app/services/lifiFlow.ts — update this test with the app")
	}
	if got, _ := strconv.Atoi(string(m[1])); got != lifiApproveGasLimit {
		t.Fatalf("app approves with gasLimit %d, server reserves for %d", got, lifiApproveGasLimit)
	}
}
