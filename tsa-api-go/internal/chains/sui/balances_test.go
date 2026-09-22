package sui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

// Anyone can publish a coin whose metadata says "USDC" and airdrop it. The row
// keeps that symbol (it is all we have), but only our configured USDC is
// Listed, which is what pricing and the app's trust decisions key on.
func TestGetBalances_OnlyConfiguredCoinsAreListed(t *testing.T) {
	pkg := func(hex string) string { return "0x" + strings.Repeat("0", 64-len(hex)) + hex }
	realUSDC := pkg("dba1") + "::usdc::USDC"
	fakeUSDC := pkg("bad1") + "::usdc::USDC"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct{ Method string `json:"method"` }
		_ = json.Unmarshal(body, &req)
		var result interface{}
		switch req.Method {
		case "suix_getAllBalances":
			result = []SuiBalance{
				{CoinType: nativeCoinType, TotalBalance: "1000000000"},
				{CoinType: realUSDC, TotalBalance: "5000000"},
				{CoinType: fakeUSDC, TotalBalance: "500000000"},
			}
		case "suix_getCoinMetadata":
			result = SuiCoinMetadata{Decimals: 6, Symbol: "USDC"}
		default:
			t.Fatalf("unexpected rpc %s", req.Method)
		}
		out, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "result": result})
		w.Write(out)
	}))
	defer srv.Close()

	c := NewClient(config.NonEVMChainConfig{
		ChainKey: "sui", Family: "sui", Network: "mainnet", NativeSymbol: "SUI", NativeDecimals: 9,
		Tokens: map[string]string{"USDC": realUSDC},
	}, NewRPC(srv.URL))
	bals, err := c.GetBalances(t.Context(), testOwner)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, b := range bals {
		listed[b.TokenAddress] = b.Listed
	}
	if !listed[realUSDC] {
		t.Fatalf("configured USDC must be listed: %+v", bals)
	}
	if listed[fakeUSDC] {
		t.Fatalf("a discovered coin calling itself USDC must not be listed: %+v", bals)
	}
}
