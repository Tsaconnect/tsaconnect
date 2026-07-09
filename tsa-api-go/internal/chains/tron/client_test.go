package tron

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

func newTestClient(url string) *Client {
	cfg := config.NonEVMChainConfig{
		ChainKey: "tron", Family: "tron", RPCURL: url,
		NativeSymbol: "TRX", NativeDecimals: 6,
		Tokens: map[string]string{"USDT": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"},
	}
	return NewClient(cfg, NewTronGrid(url, ""))
}

func TestEstimateFeeBlocksWhenNoTRX(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// sender has 0 TRX; recipient (second lookup) inactive
		w.Write([]byte(`{"data":[{"balance":0,"trc20":[]}]}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	q, err := c.EstimateFee(context.Background(), chainsTransfer("USDT", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"))
	if err != nil {
		t.Fatalf("EstimateFee: %v", err)
	}
	if q.HasEnoughGas {
		t.Fatal("expected HasEnoughGas=false for a 0-TRX sender")
	}
	if q.GasSymbol != "TRX" {
		t.Fatalf("gas symbol want TRX got %s", q.GasSymbol)
	}
}

func TestValidateAddressRejectsBadPrefix(t *testing.T) {
	c := newTestClient("http://x")
	if err := c.ValidateAddress("0xabc"); err == nil {
		t.Fatal("expected rejection of non-Tron address")
	}
}

func chainsTransfer(symbol, tokenAddr string) chains.TransferRequest {
	return chains.TransferRequest{
		From: "TSenderaddressbase58xxxxxxxxxxxxxxx", To: "TRecipientbase58xxxxxxxxxxxxxxxxxx",
		Amount: "1000000", Symbol: symbol, TokenAddr: tokenAddr,
	}
}
