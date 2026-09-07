package sui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

const (
	testOwner    = "0x7b1f626b5257df007b1f626b5257df007b1f626b5257df007b1f626b5257df00"
	testStranger = "0x00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	suiCoin      = "0x2::sui::SUI"
)

func change(owner, coinType, amount string) DryRunBalanceChange {
	return DryRunBalanceChange{
		Owner:    json.RawMessage(`{"AddressOwner":"` + owner + `"}`),
		CoinType: coinType,
		Amount:   amount,
	}
}

func approved(n int64) *big.Int { return big.NewInt(n) }

// The whole point of the dry run: an aggregator's transaction is opaque, so the
// network's own account of what it would move is what gets checked.
//
// Gas is the subtlety here. Sui charges it to the sender's SUI coin, so the
// node reports it as an outflow alongside the swap — every case below that
// spends anything at all also pays gas, because a real transaction cannot not.
func TestCheckBalanceChanges(t *testing.T) {
	const usdc = "0xdba34672e30cb065b1f93e3ab55318768fd6fef66c15942c9f7cb846e2f900e7::usdc::USDC"
	// A real net-gas figure: the high end of a sample taken from mainnet.
	gas := big.NewInt(3_651_072)

	cases := []struct {
		name     string
		fromCoin string
		changes  []DryRunBalanceChange
		wantErr  string
	}{
		{
			// The swap is denominated in SUI, so the one coin leaving pays for
			// both the swap and its own gas. Bounding it by the approved amount
			// alone rejects every native swap there is.
			name:     "a native swap pays gas on top of what was approved",
			fromCoin: nativeCoinType,
			changes: []DryRunBalanceChange{
				change(testOwner, suiCoin, "-1003651072"),
				change(testOwner, usdc, "2500000"), // the swap's proceeds
			},
		},
		{
			// Gas is in MIST (9dp) while the approved amount is in USDC (6dp),
			// so the two are not comparable and must not share a ceiling.
			name:     "a token swap pays its gas in SUI",
			fromCoin: usdc,
			changes: []DryRunBalanceChange{
				change(testOwner, usdc, "-1000000"),
				change(testOwner, suiCoin, "-3651072"),
			},
		},
		{
			name:     "spends less than approved",
			fromCoin: nativeCoinType,
			changes:  []DryRunBalanceChange{change(testOwner, suiCoin, "-500000000")},
		},
		{
			// The route was quoted for 1 SUI; a transaction that takes more than
			// that plus its gas is not the one the user agreed to.
			name:     "a native swap spending beyond the approved amount and its gas",
			fromCoin: nativeCoinType,
			changes:  []DryRunBalanceChange{change(testOwner, suiCoin, "-1003651073")},
			wantErr:  "was approved",
		},
		{
			// SUI is allowed to leave to pay gas — but only as much gas as the
			// simulation itself reported, not an open-ended amount.
			name:     "a token swap taking more SUI than the gas it reported",
			fromCoin: usdc,
			changes: []DryRunBalanceChange{
				change(testOwner, usdc, "-1000000"),
				change(testOwner, suiCoin, "-3651073"),
			},
			wantErr: "for gas",
		},
		{
			// A coin the swap does not involve has no business leaving at all,
			// however small the amount.
			name:     "sweeps an unrelated coin",
			fromCoin: nativeCoinType,
			changes: []DryRunBalanceChange{
				change(testOwner, suiCoin, "-1003651072"),
				change(testOwner, usdc, "-1"),
			},
			wantErr: "does not involve",
		},
		{
			name:     "moves someone else's funds",
			fromCoin: nativeCoinType,
			changes:  []DryRunBalanceChange{change(testStranger, suiCoin, "-1")},
			wantErr:  "not yours",
		},
		{
			// A shared or wrapped object has no address owner, so an outflow
			// from one cannot be attributed — and must not be waved through.
			name:     "outflow we cannot attribute",
			fromCoin: nativeCoinType,
			changes: []DryRunBalanceChange{{
				Owner:    json.RawMessage(`{"Shared":{"initial_shared_version":3}}`),
				CoinType: suiCoin, Amount: "-1",
			}},
			wantErr: "cannot attribute",
		},
		{
			name:     "unreadable amount",
			fromCoin: nativeCoinType,
			changes: []DryRunBalanceChange{
				{Owner: json.RawMessage(`{"AddressOwner":"` + testOwner + `"}`), CoinType: suiCoin, Amount: "many"},
			},
			wantErr: "cannot read",
		},
		{
			// The short form is what LiFi and the RPC may hand back; it has to
			// resolve to the same coin as the canonical one or the from-token
			// looks unrelated and its own outflow is refused.
			name:     "the native coin's short form is the same coin",
			fromCoin: nativeCoinTypeShort,
			changes:  []DryRunBalanceChange{change(testOwner, nativeCoinType, "-1003651072")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkBalanceChanges(tc.changes, testOwner, tc.fromCoin, approved(1_000_000_000), gas)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want accepted, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want an error mentioning %q, got %v", tc.wantErr, err)
			}
			var invalid *chains.InvalidRequestError
			if !errors.As(err, &invalid) {
				t.Fatalf("want a caller-fixable error, got %T", err)
			}
		})
	}
}

// Gas is what the sender actually pays, and exactly what the node subtracts
// from their SUI balance change — verified against mainnet, where a transaction
// that did nothing but pay gas reported a sender change of precisely -netGas.
func TestDryRunGasUsedNetGas(t *testing.T) {
	g := DryRunGasUsed{ComputationCost: "1000000", StorageCost: "2500000", StorageRebate: "2470000"}
	if got := g.NetGas(); got.Cmp(big.NewInt(1_030_000)) != 0 {
		t.Fatalf("net gas: got %s, want 1030000", got)
	}
	// A rebate bigger than the cost is a refund; it arrives as an inflow, so
	// there is no outflow to allow for.
	refund := DryRunGasUsed{ComputationCost: "1000", StorageCost: "0", StorageRebate: "313972"}
	if got := refund.NetGas(); got.Sign() != 0 {
		t.Fatalf("a net refund must floor at zero, got %s", got)
	}
	if got := (DryRunGasUsed{}).NetGas(); got.Sign() != 0 {
		t.Fatalf("an absent gas summary must be zero, got %s", got)
	}
}

// Sui's Owner is an enum: a bare string for Immutable, an object otherwise.
func TestBalanceChangeOwner(t *testing.T) {
	got, err := balanceChangeOwner(json.RawMessage(`{"AddressOwner":"0xABC"}`))
	if err != nil || got != "0xabc" {
		t.Fatalf("AddressOwner: got %q, %v", got, err)
	}
	if _, err := balanceChangeOwner(json.RawMessage(`"Immutable"`)); err == nil {
		t.Error("a string owner has no address and must not resolve")
	}
	if _, err := balanceChangeOwner(json.RawMessage(`{"Shared":{}}`)); err == nil {
		t.Error("a shared owner has no address and must not resolve")
	}
}

// mockNetGas is what the canned simulation below claims to cost, so the tests
// exercise the same amount+gas outflow a real transaction produces.
const mockNetGas = 3_651_072

// mockSuiRPC answers sui_dryRunTransactionBlock with a canned result.
func mockSuiRPC(t *testing.T, status string, changes []DryRunBalanceChange, fail bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "sui_dryRunTransactionBlock") {
			t.Errorf("unexpected RPC call: %s", body)
		}
		if fail {
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"nope"}}`))
			return
		}
		res := DryRunResult{BalanceChanges: changes}
		res.Effects.Status.Status = status
		res.Effects.GasUsed = DryRunGasUsed{
			ComputationCost: "3000000", StorageCost: "1000000", StorageRebate: "348928", // net 3651072
		}
		out, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "result": res})
		w.Write(out)
	}))
}

func aggClient(url string) *Client {
	return NewClient(config.NonEVMChainConfig{
		ChainKey: "sui", Family: "sui", Network: "mainnet",
		NativeSymbol: "SUI", NativeDecimals: 9,
	}, NewRPC(url))
}

func aggReq(txB64 string) chains.TransferRequest {
	return chains.TransferRequest{
		From: testOwner, Amount: "1000000000", Symbol: "SUI", TxBytesB64: txB64,
	}
}

// End to end through PrepareTransfer: a route the network says is safe yields a
// digest in the same shape a normal send does, so the existing signing and
// broadcast path carries it unchanged.
func TestPrepareFromTxBytes_AcceptsASafeRoute(t *testing.T) {
	srv := mockSuiRPC(t, "success", []DryRunBalanceChange{change(testOwner, suiCoin, "-1003651072")}, false)
	defer srv.Close()

	txB64 := base64.StdEncoding.EncodeToString([]byte("pretend-bcs-transaction-data"))
	prepared, err := aggClient(srv.URL).PrepareTransfer(context.Background(), aggReq(txB64))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	if prepared.Family != "sui" || prepared.Raw != txB64 {
		t.Fatalf("prepared should carry the route's own bytes: %+v", prepared)
	}
	if len(prepared.Payloads) != 1 || prepared.Payloads[0].Curve != chains.CurveEd25519 ||
		len(prepared.Payloads[0].HashHex) != 64 {
		t.Fatalf("want one 32-byte ed25519 digest, got %+v", prepared.Payloads)
	}
	// The digest must be the intent digest of the route's bytes — anything else
	// signs a different transaction than the one that was checked.
	want := signingDigest([]byte("pretend-bcs-transaction-data"))
	if prepared.Payloads[0].HashHex != hexOf(want[:]) {
		t.Fatal("digest does not match the transaction that was simulated")
	}
}

// A token swap is the case the amount bound gets wrong most easily: the
// approved amount is in the token's units while gas is in MIST, so the two
// cannot share a ceiling. 1 USDC (1e6) is smaller than this route's gas
// (3,651,072 MIST) — comparing them would refuse a perfectly good swap.
func TestPrepareFromTxBytes_AcceptsATokenSwapPayingGasInSui(t *testing.T) {
	const usdc = "0xdba34672e30cb065b1f93e3ab55318768fd6fef66c15942c9f7cb846e2f900e7::usdc::USDC"
	srv := mockSuiRPC(t, "success", []DryRunBalanceChange{
		change(testOwner, usdc, "-1000000"),
		change(testOwner, suiCoin, "-3651072"),
	}, false)
	defer srv.Close()

	req := aggReq(base64.StdEncoding.EncodeToString([]byte("tx")))
	req.Amount, req.Symbol, req.TokenAddr = "1000000", "USDC", usdc
	if _, err := aggClient(srv.URL).PrepareTransfer(context.Background(), req); err != nil {
		t.Fatalf("a token swap paying gas in SUI must be accepted, got %v", err)
	}
}

func TestPrepareFromTxBytes_RefusesWhatItCannotVouchFor(t *testing.T) {
	txB64 := base64.StdEncoding.EncodeToString([]byte("tx"))

	t.Run("simulation says it would fail", func(t *testing.T) {
		srv := mockSuiRPC(t, "failure", nil, false)
		defer srv.Close()
		_, err := aggClient(srv.URL).PrepareTransfer(context.Background(), aggReq(txB64))
		if err == nil || !strings.Contains(err.Error(), "would fail on-chain") {
			t.Fatalf("want a would-fail error, got %v", err)
		}
	})

	t.Run("simulation unavailable", func(t *testing.T) {
		srv := mockSuiRPC(t, "", nil, true)
		defer srv.Close()
		// Refusing costs a swap; signing blind can cost the wallet.
		_, err := aggClient(srv.URL).PrepareTransfer(context.Background(), aggReq(txB64))
		if err == nil || !strings.Contains(err.Error(), "could not be checked") {
			t.Fatalf("want a not-checked error, got %v", err)
		}
		// An unreachable node is not the caller's mistake. Marking it fixable
		// would answer 400 to what is really an upstream outage.
		var invalid *chains.InvalidRequestError
		if errors.As(err, &invalid) {
			t.Error("an upstream failure must not be reported as a bad request")
		}
	})

	// A failed call wraps net/http's error, which quotes the endpoint URL. For
	// a keyed provider that URL is a credential, so it must not survive into
	// anything the caller can read.
	t.Run("the endpoint never reaches the caller", func(t *testing.T) {
		const secret = "s3cret-api-key"
		// Port 1 is reserved and unservable, so the call fails inside net/http
		// rather than at the JSON-RPC layer — the path that carries the URL.
		_, err := aggClient("http://127.0.0.1:1/"+secret).PrepareTransfer(context.Background(), aggReq(txB64))
		if err == nil {
			t.Fatal("want an error from an unreachable endpoint")
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "127.0.0.1") {
			t.Fatalf("the endpoint leaked into the caller-visible error: %v", err)
		}
	})

	t.Run("not base64", func(t *testing.T) {
		srv := mockSuiRPC(t, "success", nil, false)
		defer srv.Close()
		_, err := aggClient(srv.URL).PrepareTransfer(context.Background(), aggReq("!!!not base64!!!"))
		if err == nil || !strings.Contains(err.Error(), "not valid base64") {
			t.Fatalf("want a base64 error, got %v", err)
		}
	})
}

func hexOf(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hexdigits[c>>4], hexdigits[c&0x0f])
	}
	return string(out)
}
