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

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"

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

// getTradeTuple packs a getTrade answer: the whole Trade struct in declaration order.
func packGetTrade(t *testing.T, tup getTradeTuple) []byte {
	t.Helper()
	out, err := parsedP2PEscrowABI.Methods["getTrade"].Outputs.Pack(tup)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// One getTrade read carries the whole trade, its fee snapshot and its deadlines.
func TestReadTrade_DecodesTheWholeStruct(t *testing.T) {
	releaser := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	stub := &escrowCallStub{answers: map[string][]byte{"getTrade": packGetTrade(t, getTradeTuple{
		Seller: rtSeller, Amount: big.NewInt(123_456), Buyer: rtBuyer,
		CreatedAt: big.NewInt(1_700_000_000), ExpiresAt: big.NewInt(1_700_001_800),
		Status: uint8(OnChainP2PStatusDisputed), Funding: 1, Token: rtToken,
		AutoReleaseAt: big.NewInt(1_700_000_900), DisputedAt: big.NewInt(1_700_002_000),
		PlatformFeeBps: 190, GasFeeBps: 10, AutoReleaseWindow: big.NewInt(0),
		VoucherHash: rtHash, TradeReleaser: releaser,
	})}}
	tr, err := newEscrowOnStub(t, stub).ReadTrade([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.calls) != 1 || stub.calls[0] != "getTrade" {
		t.Fatalf("calls = %v, want one getTrade()", stub.calls)
	}
	if tr.Seller != rtSeller || tr.Buyer != rtBuyer || tr.Token != rtToken || tr.Amount.Int64() != 123_456 ||
		tr.CreatedAt.Int64() != 1_700_000_000 || tr.ExpiresAt.Int64() != 1_700_001_800 ||
		tr.AutoReleaseAt.Int64() != 1_700_000_900 || tr.DisputedAt.Int64() != 1_700_002_000 ||
		tr.Status != OnChainP2PStatusDisputed || !tr.Exists || tr.Funding != 1 || tr.VoucherHash != rtHash ||
		tr.TradeReleaser != releaser {
		t.Fatalf("trade = %+v", tr)
	}
	if tr.PlatformFeeBps != 190 || tr.GasFeeBps != 10 || tr.FeeBps != 200 {
		t.Fatalf("fees = %d + %d = %d", tr.PlatformFeeBps, tr.GasFeeBps, tr.FeeBps)
	}
}

func TestReadTrade_EmptySlotDoesNotExist(t *testing.T) {
	stub := &escrowCallStub{answers: map[string][]byte{"getTrade": packGetTrade(t, getTradeTuple{
		Amount: big.NewInt(0), CreatedAt: big.NewInt(0), ExpiresAt: big.NewInt(0), AutoReleaseAt: big.NewInt(0),
		DisputedAt: big.NewInt(0), AutoReleaseWindow: big.NewInt(0),
	})}}
	tr, err := newEscrowOnStub(t, stub).ReadTrade([32]byte{1})
	if err != nil || tr.Exists {
		t.Fatalf("trade = %+v, err = %v", tr, err)
	}
}

func TestReadTrade_TransportErrorIsReturned(t *testing.T) {
	stub := &escrowCallStub{answers: map[string][]byte{}} // every call errors
	if _, err := newEscrowOnStub(t, stub).ReadTrade([32]byte{1}); err == nil {
		t.Fatal("want the getTrade() error")
	}
}

func TestReadReleaser(t *testing.T) {
	want := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	out, err := parsedP2PEscrowABI.Methods["releaserOf"].Outputs.Pack(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := newEscrowOnStub(t, &escrowCallStub{answers: map[string][]byte{"releaserOf": out}}).ReadReleaser(rtSeller.Hex())
	if err != nil || got != want {
		t.Fatalf("releaser = %s, %v", got.Hex(), err)
	}
}

// TradeKey must be the contract's keccak256(abi.encode(opener, tradeId)).
func TestTradeKey_MatchesAbiEncode(t *testing.T) {
	addrT, _ := abi.NewType("address", "", nil)
	b32T, _ := abi.NewType("bytes32", "", nil)
	tradeID := HashID(uuid.New())
	enc, err := abi.Arguments{{Type: addrT}, {Type: b32T}}.Pack(rtBuyer, tradeID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := TradeKey(rtBuyer, tradeID), crypto.Keccak256Hash(enc); got != [32]byte(want) {
		t.Fatalf("TradeKey = %x, want %x", got, want)
	}
	if TradeKey(rtSeller, tradeID) == TradeKey(rtBuyer, tradeID) {
		t.Fatal("the opener must be part of the key")
	}
}
