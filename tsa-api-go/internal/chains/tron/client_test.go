package tron

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

func newTestClient(url string) *Client {
	cfg := config.NonEVMChainConfig{
		ChainKey: "tron", Family: "tron", RPCURL: url,
		NativeSymbol: "TRX", NativeDecimals: 6,
		Tokens:  map[string]string{"USDT": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"},
		Routers: []string{fixtureDiamond},
	}
	return NewClient(cfg, NewTronGrid(url, ""))
}

func TestEstimateFeeBlocksWhenNoTRX(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Both sender and recipient return a non-empty data array (both active).
		// Sender balance is 0 sun → HasEnoughGas=false.
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

// TestEstimateFeeAddsActivationForInactiveRecipient verifies that when the
// recipient address has no on-chain account (data=[] → Active=false), EstimateFee
// sets NeedsActivation=true and adds activationCostSun (1_100_000) to the estimate,
// resulting in nativeFeeEstSun+activationCostSun = 1_400_000 sun for a native send.
func TestEstimateFeeAddsActivationForInactiveRecipient(t *testing.T) {
	const recipientAddr = otherAddress
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, recipientAddr) {
			// Recipient has no on-chain account → Active=false.
			w.Write([]byte(`{"data":[]}`))
		} else {
			// Sender has 2 TRX (2_000_000 sun) — enough to cover native fee + activation.
			w.Write([]byte(`{"data":[{"balance":2000000,"trc20":[]}]}`))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	// Native send (TokenAddr="") so fee = nativeFeeEstSun + activationCostSun = 1_400_000.
	req := chains.TransferRequest{
		From: fixtureOwner, To: recipientAddr,
		Amount: "1000000", Symbol: "TRX", TokenAddr: "",
	}
	q, err := c.EstimateFee(context.Background(), req)
	if err != nil {
		t.Fatalf("EstimateFee: %v", err)
	}
	if !q.NeedsActivation {
		t.Fatal("expected NeedsActivation=true for inactive recipient")
	}
	if !q.HasEnoughGas {
		t.Fatalf("expected HasEnoughGas=true (sender has 2 TRX, fee is 1.4 TRX), got false")
	}
	const wantFee = "1400000"
	if q.EstFeeRaw != wantFee {
		t.Fatalf("EstFeeRaw: want %s got %s", wantFee, q.EstFeeRaw)
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
		From: fixtureOwner, To: otherAddress,
		Amount: "1000000", Symbol: symbol, TokenAddr: tokenAddr,
	}
}

// A bridge memo has no home in a Tron transfer. Dropping it silently would send
// real funds to a bridge with nothing tying the payment to the user's swap, so
// the guard must reject the request outright — and as a caller-fixable error,
// which is what the prepare handler turns into a useful message.
func TestPrepareRejectsBridgeMemo(t *testing.T) {
	c := newTestClient("http://unused")
	req := chains.TransferRequest{
		From:    "TQ5NMqJjW8b1cM7yUqEbNvxjHU3sJvQqbT",
		To:      "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
		Amount:  "1000000",
		Symbol:  "TRX",
		MemoHex: "3d3a4554482e455448",
	}
	_, err := c.PrepareTransfer(context.Background(), req)
	if err == nil {
		t.Fatal("want an error for a memo Tron cannot carry")
	}
	var invalid *chains.InvalidRequestError
	if !errors.As(err, &invalid) {
		t.Fatalf("want chains.InvalidRequestError, got %T (%v)", err, err)
	}
}
