package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

type dataErr struct{ data string }

func (e dataErr) Error() string          { return "execution reverted" }
func (e dataErr) ErrorData() interface{} { return e.data }

var _ rpc.DataError = dataErr{}

func selectorOf(name string) string {
	id := parsedP2PEscrowABI.Errors[name].ID
	return hex0x(id[:4])
}

func hex0x(b []byte) string { return "0x" + common.Bytes2Hex(b) }

func TestDecodeRevertReason(t *testing.T) {
	onlyBuyer := selectorOf("OnlyBuyer")

	// Error(string) payload: selector + abi-encoded "Bad voucher signature".
	strTyp, _ := abi.NewType("string", "", nil)
	enc, err := abi.Arguments{{Type: strTyp}}.Pack("Bad voucher signature")
	if err != nil {
		t.Fatal(err)
	}
	errString := hex0x(append(common.FromHex("0x08c379a0"), enc...))
	panicData := "0x4e487b71" + strings.Repeat("0", 62) + "11"

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"custom error name", dataErr{onlyBuyer}, "OnlyBuyer()"},
		{"Error(string)", dataErr{errString}, "Bad voucher signature"},
		{"Panic", dataErr{panicData}, "panic 0x11"},
		{"unknown selector falls back to hex", dataErr{"0xdeadbeef"}, "0xdeadbeef"},
		{"wrapped DataError", fmt.Errorf("outer: %w", dataErr{onlyBuyer}), "OnlyBuyer()"},
		{"no data", errors.New("execution reverted"), ""},
		{"empty data", dataErr{"0x"}, ""},
		{"nil", nil, ""},
	}
	for _, c := range cases {
		if got := decodeRevertReason(c.err); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestWithRevertReason_NoDataUnchanged(t *testing.T) {
	in := errors.New("dial tcp: connection refused")
	if got := withRevertReason(in); got != in {
		t.Errorf("transport error must pass through untouched, got %v", got)
	}
}

// A real JSON-RPC round trip: eth_estimateGas answers code 3 + data, exactly
// like Sonic. The 422 text must name the custom error and stay a chain revert.
func TestBuildUnsignedTx_RevertNamesCustomError(t *testing.T) {
	selector := selectorOf("OnlyBuyer")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		var body string
		switch req.Method {
		case "eth_getTransactionCount":
			body = `"result":"0x1"`
		case "eth_gasPrice":
			body = `"result":"0x3b9aca00"`
		case "eth_estimateGas":
			body = fmt.Sprintf(`"error":{"code":3,"message":"execution reverted","data":%q}`, selector)
		default:
			body = `"result":"0x0"`
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,%s}`, req.ID, body)
	}))
	defer srv.Close()

	client, err := blockchain.NewEVMClient(srv.URL, 146)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewP2PEscrowServiceForChain(client, &config.Config{}, "0x1111111111111111111111111111111111111111")
	_, err = svc.buildUnsignedTx("0x2222222222222222222222222222222222222222", []byte{0x01})
	if err == nil {
		t.Fatal("expected revert error")
	}
	if !strings.Contains(err.Error(), "execution reverted: OnlyBuyer()") {
		t.Errorf("message lacks decoded reason: %v", err)
	}
	if !isChainRevert(err) {
		t.Errorf("must still classify as chain revert: %v", err)
	}
}
