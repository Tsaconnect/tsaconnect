package services

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

// newTestRelayTxBuilder wires an EVMRelayTxBuilder to a local JSON-RPC stub
// that answers the three calls BuildUnsignedCall makes (nonce, gas price, gas
// estimate) with fixed values, so the test never touches a live chain.
func newTestRelayTxBuilder(t *testing.T) EVMRelayTxBuilder {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "eth_getTransactionCount":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":"0x5"}`, req.ID)
		case "eth_gasPrice":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":"0x3b9aca00"}`, req.ID) // 1 gwei
		case "eth_estimateGas":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":"0x5208"}`, req.ID) // 21000
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"stub RPC does not serve %s"}}`, req.ID, req.Method)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := blockchain.NewEVMClient(srv.URL, 146)
	if err != nil {
		t.Fatalf("stub rpc client: %v", err)
	}
	return EVMRelayTxBuilder{Client: client}
}

func TestBuildUnsignedCall_CarriesValueAndTheRoutesGasLimit(t *testing.T) {
	b := newTestRelayTxBuilder(t)
	raw, err := b.BuildUnsignedCall(
		"0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A",
		"0x1111111111111111111111111111111111111111",
		[]byte{0xde, 0xad, 0xbe, 0xef}, big.NewInt(7), 250_000,
	)
	if err != nil {
		t.Fatalf("BuildUnsignedCall: %v", err)
	}
	var tx struct {
		To       string `json:"to"`
		Data     string `json:"data"`
		Value    string `json:"value"`
		GasLimit uint64 `json:"gasLimit"`
	}
	if err := json.Unmarshal(raw, &tx); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.EqualFold(tx.To, "0x1111111111111111111111111111111111111111") {
		t.Fatalf("to = %s", tx.To)
	}
	if tx.Data != "deadbeef" && tx.Data != "0xdeadbeef" {
		t.Fatalf("data = %s", tx.Data)
	}
	if tx.Value != "7" || tx.GasLimit != 250_000 {
		t.Fatalf("value/gas = %s/%d", tx.Value, tx.GasLimit)
	}
}

func TestBuildUnsignedTx_StillSendsZeroValue(t *testing.T) {
	b := newTestRelayTxBuilder(t)
	raw, err := b.BuildUnsignedTx(
		"0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A",
		"0x1111111111111111111111111111111111111111",
		[]byte{0x01},
	)
	if err != nil {
		t.Fatal(err)
	}
	var tx struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &tx); err != nil {
		t.Fatal(err)
	}
	if tx.Value != "0" {
		t.Fatalf("value = %s; the existing relayer path must not change", tx.Value)
	}
}
