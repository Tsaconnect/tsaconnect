package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// escrowCallStub answers eth_call by selector and records each selector called.
type escrowCallStub struct {
	mu      sync.Mutex
	answers map[string][]byte // method name → ABI-encoded output
	calls   []string
}

func newEscrowOnStub(t *testing.T, stub *escrowCallStub) *P2PEscrowService {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method != "eth_call" || len(req.Params) == 0 {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"eth_call only"}}`, req.ID)
			return
		}
		var msg struct {
			Input hexutil.Bytes `json:"input"`
			Data  hexutil.Bytes `json:"data"`
		}
		_ = json.Unmarshal(req.Params[0], &msg)
		in := msg.Input
		if len(in) == 0 {
			in = msg.Data
		}
		name := ""
		for n, m := range parsedP2PEscrowABI.Methods {
			if len(in) >= 4 && bytes.Equal(in[:4], m.ID) {
				name = n
			}
		}
		stub.mu.Lock()
		stub.calls = append(stub.calls, name)
		out, ok := stub.answers[name]
		stub.mu.Unlock()
		if !ok {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":3,"message":"execution reverted"}}`, req.ID)
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%q}`, req.ID, hexutil.Encode(out))
	}))
	t.Cleanup(srv.Close)
	client, err := blockchain.NewEVMClient(srv.URL, 146)
	if err != nil {
		t.Fatalf("stub rpc client: %v", err)
	}
	return NewP2PEscrowServiceForChain(client, &config.Config{Env: "development"}, "0x00000000000000000000000000000000000000e5")
}

var (
	rtSeller = common.HexToAddress("0x000000000000000000000000000000000000beef")
	rtBuyer  = common.HexToAddress("0x000000000000000000000000000000000000d00d")
	rtToken  = common.HexToAddress("0x000000000000000000000000000000000000feed")
	rtHash   = [32]byte{9}
)

// On a V3 escrow one trades(bytes32) read carries the whole trade and its
// fee snapshot, so confirm/backfill need no second call.
func TestReadTrade_V3IsOneCallWithTheFeeSnapshot(t *testing.T) {
	out, err := parsedP2PEscrowABI.Methods["trades"].Outputs.Pack(rtSeller, rtBuyer, rtToken, big.NewInt(123_456),
		big.NewInt(1_700_000_000), big.NewInt(1_700_000_900), uint8(OnChainP2PStatusPaid), uint8(1), uint16(7),
		uint16(100), uint16(50), rtHash)
	if err != nil {
		t.Fatal(err)
	}
	stub := &escrowCallStub{answers: map[string][]byte{"trades": out}}
	tr, err := newEscrowOnStub(t, stub).ReadTrade([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.calls) != 1 || stub.calls[0] != "trades" {
		t.Fatalf("calls = %v, want one trades()", stub.calls)
	}
	if tr.Seller != rtSeller || tr.Buyer != rtBuyer || tr.Token != rtToken || tr.Amount.Int64() != 123_456 ||
		tr.CreatedAt.Int64() != 1_700_000_000 || tr.AutoReleaseAt.Int64() != 1_700_000_900 ||
		tr.Status != OnChainP2PStatusPaid || !tr.Exists || tr.Funding != 1 || tr.FeeBps != 7 || tr.VoucherHash != rtHash {
		t.Fatalf("trade = %+v", tr)
	}
	if !tr.FeeSnapshotFound || tr.PlatformFeeBps != 100 || tr.GasFeeBps != 50 {
		t.Fatalf("snapshot = %v %d %d", tr.FeeSnapshotFound, tr.PlatformFeeBps, tr.GasFeeBps)
	}
}

// A pre-V3 escrow's trades getter has the old layout: fall back to getTrade,
// with no snapshot.
func TestReadTrade_PreV3FallsBackToGetTrade(t *testing.T) {
	getTrade, err := parsedP2PEscrowABI.Methods["getTrade"].Outputs.Pack(rtSeller, rtBuyer, rtToken, big.NewInt(5),
		big.NewInt(1_700_000_000), uint8(OnChainP2PStatusPending), uint8(0), uint16(50), big.NewInt(0), rtHash)
	if err != nil {
		t.Fatal(err)
	}
	stub := &escrowCallStub{answers: map[string][]byte{
		"trades":   make([]byte, 10*32), // the V2 struct: 10 words
		"getTrade": getTrade,
	}}
	tr, err := newEscrowOnStub(t, stub).ReadTrade([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if tr.FeeSnapshotFound || tr.FeeBps != 50 || tr.Amount.Int64() != 5 || !tr.Exists || tr.Seller != rtSeller {
		t.Fatalf("trade = %+v", tr)
	}
	if len(stub.calls) != 2 || stub.calls[1] != "getTrade" {
		t.Fatalf("calls = %v", stub.calls)
	}
}
