package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// swapServingCall returns a swap service whose RPC answers every eth_call with
// the given ABI-encoded bytes.
func swapServingCall(t *testing.T, out []byte) *MCGPSwapService {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "eth_call" {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%q}`, req.ID, hexutil.Encode(out))
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"eth_call only"}}`, req.ID)
	}))
	t.Cleanup(srv.Close)
	client, err := blockchain.NewEVMClient(srv.URL, 146)
	if err != nil {
		t.Fatalf("stub rpc client: %v", err)
	}
	return NewMCGPSwapService(client, &config.Config{Env: "development", MCGPSwapBAddress: testSwapAddr})
}

// The reconcilers decide whether a sell or bill debit exists from the
// contract's per-ref `debits` record, so the read must decode all three fields.
func TestDebitRecord_DecodesPendingDebit(t *testing.T) {
	user := common.HexToAddress("0x00000000000000000000000000000000000000AB")
	amount := new(big.Int).Mul(big.NewInt(42), oneE18)
	out, err := parsedMCGPSwapABI.Methods["debits"].Outputs.Pack(user, amount, uint8(DebitStatePending))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	s := swapServingCall(t, out)

	rec, err := s.DebitRecord(context.Background(), [32]byte{1})
	if err != nil {
		t.Fatalf("DebitRecord: %v", err)
	}
	if rec.State != DebitStatePending || rec.User != user || rec.Amount.Cmp(amount) != 0 {
		t.Fatalf("record = %+v, want pending %s for %s", rec, amount, user.Hex())
	}
}

func TestDebitRecord_UnknownRefIsNone(t *testing.T) {
	out, err := parsedMCGPSwapABI.Methods["debits"].Outputs.Pack(common.Address{}, big.NewInt(0), uint8(0))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	rec, err := swapServingCall(t, out).DebitRecord(context.Background(), [32]byte{2})
	if err != nil {
		t.Fatalf("DebitRecord: %v", err)
	}
	if rec.State != DebitStateNone {
		t.Fatalf("state = %d, want None", rec.State)
	}
}

// A failure while building the transaction happens before anything reaches
// the network, so callers may treat it as a definite "no debit". Everything
// past that point may have been broadcast.
func TestOperatorBroadcast_BuildFailureIsNotBroadcast(t *testing.T) {
	s := NewMCGPSwapService(nil, &config.Config{Env: "development"}) // no contract address
	s.SetOperatorSigner(&fakeReleaseSigner{addr: "0x000000000000000000000000000000000000A11CE", hash: "0xdead"})
	_, err := s.OperatorDebitSpendable(context.Background(), "0x00000000000000000000000000000000000000AB", big.NewInt(1), big.NewInt(1), [32]byte{3})
	if !errors.Is(err, ErrTxNotBroadcast) {
		t.Fatalf("err = %v, want ErrTxNotBroadcast", err)
	}
}

func TestOperatorBroadcast_SendFailureIsNotMarkedNotBroadcast(t *testing.T) {
	s := NewMCGPSwapService(nil, &config.Config{Env: "development", MCGPSwapBAddress: testSwapAddr})
	s.SetOperatorSigner(&fakeReleaseSigner{addr: "0x000000000000000000000000000000000000A11CE", err: errors.New("post: context deadline exceeded")})
	_, err := s.OperatorDebitSpendable(context.Background(), "0x00000000000000000000000000000000000000AB", big.NewInt(1), big.NewInt(1), [32]byte{4})
	if err == nil || errors.Is(err, ErrTxNotBroadcast) {
		t.Fatalf("err = %v, want an error NOT marked ErrTxNotBroadcast (outcome unknown)", err)
	}
}

func TestLocalKeySigner_SignFailureIsNotBroadcast(t *testing.T) {
	s := &LocalKeyReleaseSigner{}
	_, err := s.SignAndBroadcast(context.Background(), []byte("not json"))
	if !errors.Is(err, ErrTxNotBroadcast) {
		t.Fatalf("err = %v, want ErrTxNotBroadcast", err)
	}
}
