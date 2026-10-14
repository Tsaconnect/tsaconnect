package blockchain

import (
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"
)

// prepareRPC answers the three reads a builder makes; estimate is either a gas
// amount or a JSON-RPC error (with revert data).
func prepareRPC(t *testing.T, estimate string) *EVMClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(b, &req)
		w.Header().Set("Content-Type", "application/json")
		answer := `"result":"0x0"`
		switch req.Method {
		case "eth_getTransactionCount":
			answer = `"result":"0x7"`
		case "eth_gasPrice":
			answer = `"result":"0x3b9aca00"` // 1 gwei
		case "eth_estimateGas":
			answer = estimate
		}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,`+answer+`}`)
	}))
	t.Cleanup(srv.Close)
	c, err := NewEVMClient(srv.URL, 97)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const revertEstimate = `"error":{"code":3,"message":"execution reverted: Bad amount","data":"0x08c379a00000000000000000000000000000000000000000000000000000000000000020000000000000000000000000000000000000000000000000000000000000000a42616420616d6f756e7400000000000000000000000000000000000000000000"}`

// The shared builder writes the same UnsignedTx the hand-written builders did.
func TestPrepareContractCall_Shape(t *testing.T) {
	c := prepareRPC(t, `"result":"0xc350"`)
	token := "0x55d398326f99059ff775485246999027b3197955" // lower case: written as given
	approve, err := c.PrepareERC20Approve(token, "0x00000000000000000000000000000000000000a1", "0x00000000000000000000000000000000000000b2", big.NewInt(5))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := parsedERC20ABI.Pack("approve", common.HexToAddress("0x00000000000000000000000000000000000000b2"), big.NewInt(5))
	want, _ := json.Marshal(UnsignedTx{Nonce: 7, GasPrice: applyGasHeadroom(big.NewInt(1_000_000_000)).String(), GasLimit: 50_000,
		To: token, Value: "0", Data: common.Bytes2Hex(data), ChainID: "97"})
	if string(approve) != string(want) {
		t.Fatalf("approve = %s\nwant      %s", approve, want)
	}
	native, err := c.PrepareNativeTransfer("0x00000000000000000000000000000000000000a1", "0x00000000000000000000000000000000000000c3", big.NewInt(9))
	if err != nil {
		t.Fatal(err)
	}
	var u UnsignedTx
	_ = json.Unmarshal(native, &u)
	if u.Value != "9" || u.Data != "" || u.To != "0x00000000000000000000000000000000000000c3" || u.GasLimit != 50_000 {
		t.Fatalf("native = %s", native)
	}
}

// A failed estimate: the transfer builders keep their default gas; a call with
// no fallback is refused with ErrEstimateGas, the node's revert data intact.
func TestPrepareContractCall_EstimateFailure(t *testing.T) {
	c := prepareRPC(t, revertEstimate)
	from := "0x00000000000000000000000000000000000000a1"
	for name, tc := range map[string]struct {
		build func() ([]byte, error)
		gas   uint64
	}{
		"native":   {func() ([]byte, error) { return c.PrepareNativeTransfer(from, from, big.NewInt(1)) }, 21000},
		"transfer": {func() ([]byte, error) { return c.PrepareERC20Transfer(from, from, from, big.NewInt(1)) }, 100000},
		"approve":  {func() ([]byte, error) { return c.PrepareERC20Approve(from, from, from, big.NewInt(1)) }, 80000},
	} {
		raw, err := tc.build()
		var u UnsignedTx
		if err != nil || json.Unmarshal(raw, &u) != nil || u.GasLimit != tc.gas {
			t.Fatalf("%s: %v gas %d, want the default %d", name, err, u.GasLimit, tc.gas)
		}
	}
	_, err := c.PrepareContractCall(from, from, []byte{1, 2, 3, 4}, nil, 0)
	var de rpc.DataError
	if !errors.Is(err, ErrEstimateGas) || !errors.As(err, &de) || !strings.HasPrefix(err.Error(), "estimate gas (would revert): ") {
		t.Fatalf("err = %v, want ErrEstimateGas over the node's DataError", err)
	}
}
