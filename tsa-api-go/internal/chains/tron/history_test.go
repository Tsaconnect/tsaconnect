package tron

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

func TestHistoryParsesAndSetsDirection(t *testing.T) {
	addr := "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	hexAddr, err := Base58ToHex(addr)
	if err != nil {
		t.Fatalf("Base58ToHex: %v", err)
	}
	counterHex, _ := Base58ToHex("TUEZSdKsoDHQMeZwihtdoBiN46zxhGWYdH")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/trc20") {
			// Incoming USDT (to == me), 2.5 USDT
			w.Write([]byte(`{"data":[{"transaction_id":"trc1","block_timestamp":1700000001000,` +
				`"from":"TUEZSdKsoDHQMeZwihtdoBiN46zxhGWYdH","to":"` + addr + `","value":"2500000",` +
				`"token_info":{"symbol":"USDT","decimals":6,"address":"` + addr + `"}}]}`))
			return
		}
		// Outgoing TRX (owner == me), 5 TRX, SUCCESS, newer than the TRC-20 row
		w.Write([]byte(`{"data":[{"txID":"trx1","block_timestamp":1700000002000,` +
			`"ret":[{"contractRet":"SUCCESS"}],"raw_data":{"contract":[{"type":"TransferContract",` +
			`"parameter":{"value":{"amount":5000000,"owner_address":"` + hexAddr + `","to_address":"` + counterHex + `"}}}]}}]}`))
	}))
	defer srv.Close()

	c := NewClient(config.NonEVMChainConfig{}, NewTronGrid(srv.URL, ""))
	items, _, err := c.History(context.Background(), addr, "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}

	// Sorted newest-first: TRX (out) then USDT (in).
	trx := items[0]
	if trx.Symbol != "TRX" || trx.Direction != "out" || trx.AmountRaw != "5000000" || trx.Decimals != 6 {
		t.Fatalf("TRX row wrong: %+v", trx)
	}
	if trx.BlockTime != 1700000002 { // ms → s
		t.Fatalf("blockTime not converted to seconds: %d", trx.BlockTime)
	}
	if !strings.HasPrefix(trx.Counter, "T") { // hex counterparty prettified to base58
		t.Fatalf("TRX counterparty not base58: %q", trx.Counter)
	}

	usdt := items[1]
	if usdt.Symbol != "USDT" || usdt.Direction != "in" || usdt.AmountRaw != "2500000" {
		t.Fatalf("USDT row wrong: %+v", usdt)
	}
	if usdt.Counter != "TUEZSdKsoDHQMeZwihtdoBiN46zxhGWYdH" {
		t.Fatalf("USDT counterparty (base58 passthrough) wrong: %q", usdt.Counter)
	}
}
