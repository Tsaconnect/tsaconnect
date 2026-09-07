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
	testOwner   = "0x7b1f626b5257df007b1f626b5257df007b1f626b5257df007b1f626b5257df00"
	testStranger = "0x00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	suiCoin     = "0x2::sui::SUI"
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
func TestCheckBalanceChanges(t *testing.T) {
	cases := []struct {
		name    string
		changes []DryRunBalanceChange
		wantErr string
	}{
		{
			name: "spends exactly what was approved",
			changes: []DryRunBalanceChange{
				change(testOwner, suiCoin, "-1000000000"),
				change(testOwner, "0x…::usdc::USDC", "2500000"), // the swap's proceeds
			},
		},
		{
			name:    "spends less than approved",
			changes: []DryRunBalanceChange{change(testOwner, suiCoin, "-500000000")},
		},
		{
			// The route was quoted for 1 SUI; a transaction that takes more is
			// not the one the user agreed to, however plausible it looks.
			name:    "spends more than approved",
			changes: []DryRunBalanceChange{change(testOwner, suiCoin, "-1000000001")},
			wantErr: "was approved",
		},
		{
			// A second coin type leaving is judged on its own, so a route quoted
			// in SUI cannot quietly sweep an unrelated balance alongside it.
			name: "sweeps a second coin type",
			changes: []DryRunBalanceChange{
				change(testOwner, suiCoin, "-1000000000"),
				change(testOwner, "0x…::usdc::USDC", "-9999999999"),
			},
			wantErr: "was approved",
		},
		{
			name:    "moves someone else's funds",
			changes: []DryRunBalanceChange{change(testStranger, suiCoin, "-1")},
			wantErr: "not yours",
		},
		{
			// A shared or wrapped object has no address owner, so an outflow
			// from one cannot be attributed — and must not be waved through.
			name: "outflow we cannot attribute",
			changes: []DryRunBalanceChange{{
				Owner:    json.RawMessage(`{"Shared":{"initial_shared_version":3}}`),
				CoinType: suiCoin, Amount: "-1",
			}},
			wantErr: "cannot attribute",
		},
		{
			name: "unreadable amount",
			changes: []DryRunBalanceChange{
				{Owner: json.RawMessage(`{"AddressOwner":"` + testOwner + `"}`), CoinType: suiCoin, Amount: "many"},
			},
			wantErr: "cannot read",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkBalanceChanges(tc.changes, testOwner, approved(1_000_000_000))
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
	srv := mockSuiRPC(t, "success", []DryRunBalanceChange{change(testOwner, suiCoin, "-1000000000")}, false)
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
