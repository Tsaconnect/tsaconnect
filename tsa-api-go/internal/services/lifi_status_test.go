package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func lifiServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("txHash"); got == "" {
			t.Errorf("no txHash query parameter sent")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestLiFiStatus_ParsesADoneTransfer(t *testing.T) {
	srv := lifiServer(t, 200, `{
		"status": "DONE",
		"receiving": {
			"txHash": "0xabc123",
			"chainId": 137,
			"amount": "99884217",
			"token": {"symbol": "USDC", "decimals": 6},
			"toAddress": "0x742d35Cc6634C0532925a3b844Bc454e4438f44e"
		}
	}`)
	defer srv.Close()

	got, err := NewLiFiStatusClient(srv.URL, "").Status(context.Background(), "0xsrc")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Status != "DONE" {
		t.Errorf("status = %q, want DONE", got.Status)
	}
	if got.DestTxHash != "0xabc123" {
		t.Errorf("destTxHash = %q", got.DestTxHash)
	}
	// The bridge delivers less than was sent; the received amount is what the
	// payout must be computed from, so it has to survive parsing exactly.
	if got.ReceivedBaseUnits.String() != "99884217" {
		t.Errorf("received = %s, want 99884217", got.ReceivedBaseUnits)
	}
	if got.DestChainID != 137 {
		t.Errorf("destChainID = %d, want 137", got.DestChainID)
	}
}

func TestLiFiStatus_PendingIsNotAnError(t *testing.T) {
	srv := lifiServer(t, 200, `{"status":"PENDING"}`)
	defer srv.Close()

	got, err := NewLiFiStatusClient(srv.URL, "").Status(context.Background(), "0xsrc")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Status != "PENDING" {
		t.Fatalf("status = %q, want PENDING", got.Status)
	}
	if got.DestTxHash != "" {
		t.Fatalf("pending transfer reported a destination tx %q", got.DestTxHash)
	}
}

func TestLiFiStatus_SurfacesAFailedTransfer(t *testing.T) {
	srv := lifiServer(t, 200, `{"status":"FAILED"}`)
	defer srv.Close()

	got, err := NewLiFiStatusClient(srv.URL, "").Status(context.Background(), "0xsrc")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Status != "FAILED" {
		t.Fatalf("status = %q, want FAILED", got.Status)
	}
}

func TestLiFiStatus_ErrorsOnUpstreamFailure(t *testing.T) {
	srv := lifiServer(t, 500, `{"message":"boom"}`)
	defer srv.Close()

	if _, err := NewLiFiStatusClient(srv.URL, "").Status(context.Background(), "0xsrc"); err == nil {
		t.Fatal("upstream 500 swallowed")
	}
}

func TestLiFiStatus_SendsTheAPIKeyWhenConfigured(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("x-lifi-api-key")
		_, _ = w.Write([]byte(`{"status":"PENDING"}`))
	}))
	defer srv.Close()

	if _, err := NewLiFiStatusClient(srv.URL, "secret").Status(context.Background(), "0xsrc"); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if seen != "secret" {
		t.Fatalf("x-lifi-api-key = %q, want secret", seen)
	}
}

// Every field the orchestrator and the sell-side reconciler read off one
// status, parsed from one body: a token or a sending amount that silently
// drops to its zero value reads as "no evidence", which on the delivery path
// is indistinguishable from evidence of the wrong thing.
func TestLiFiStatus_ParsesBothSidesOfTheTransfer(t *testing.T) {
	srv := lifiServer(t, 200, `{
		"status": "DONE",
		"substatus": "COMPLETED",
		"transactionId": "0xtxid",
		"fromAddress": "0x1111111111111111111111111111111111111111",
		"sending": {
			"chainId": 8453,
			"amount": "100000000",
			"token": {"address": "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"}
		},
		"receiving": {
			"txHash": "0xabc123",
			"chainId": 56,
			"amount": "990",
			"token": {"address": "0x0000000000000000000000000000000000000000"},
			"toAddress": "0x742d35Cc6634C0532925a3b844Bc454e4438f44e"
		}
	}`)
	defer srv.Close()

	got, err := NewLiFiStatusClient(srv.URL, "").Status(context.Background(), "0xsrc")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.TransactionID != "0xtxid" {
		t.Errorf("transactionID = %q", got.TransactionID)
	}
	if got.FromAddress != "0x1111111111111111111111111111111111111111" {
		t.Errorf("fromAddress = %q", got.FromAddress)
	}
	if got.SendingChainID != 8453 {
		t.Errorf("sendingChainID = %d", got.SendingChainID)
	}
	if got.SendingToken != "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913" {
		t.Errorf("sendingToken = %q", got.SendingToken)
	}
	if got.SendingAmount == nil || got.SendingAmount.String() != "100000000" {
		t.Errorf("sendingAmount = %v", got.SendingAmount)
	}
	if got.ReceivingToken != "0x0000000000000000000000000000000000000000" {
		t.Errorf("receivingToken = %q", got.ReceivingToken)
	}
	if got.DestChainID != 56 {
		t.Errorf("destChainID = %d", got.DestChainID)
	}
}

// A sending amount LiFi omits (or writes unparseably) must stay nil, not
// become zero: "nothing was sent" is a claim, and this parser has no evidence
// for it.
func TestLiFiStatus_LeavesAnUnparseableSendingAmountNil(t *testing.T) {
	srv := lifiServer(t, 200, `{"status":"PENDING","sending":{"amount":"not-a-number"}}`)
	defer srv.Close()

	got, err := NewLiFiStatusClient(srv.URL, "").Status(context.Background(), "0xsrc")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.SendingAmount != nil {
		t.Fatalf("sendingAmount = %v, want nil", got.SendingAmount)
	}
}

// A REAL mainnet status, captured 2026-09-23 from li.quest for a USDT-on-BSC
// → USDC-on-Base transfer — the exact shape a swapped sell produces. Every
// field §5.3 binds a user's swap to her intent is asserted against the real
// body, because the binding is only as good as the parsing underneath it:
// a field that silently reads empty makes a genuine sell unclaimable.
//
// It is what caught the recipient: LiFi reports it at the TOP LEVEL of the
// status, and leaves receiving.toAddress null (observed on lifiIntents,
// mayan, relaydepository, nordstern and across alike).
func TestLiFiStatus_ParsesARealSwappedSellStatus(t *testing.T) {
	body, err := os.ReadFile("testdata/lifi_status_sell.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := lifiServer(t, 200, string(body))
	defer srv.Close()

	got, err := NewLiFiStatusClient(srv.URL, "").Status(context.Background(), "0xsrc")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Status != LiFiStatusDone || got.Substatus != LiFiSubstatusCompleted {
		t.Errorf("status = %q/%q", got.Status, got.Substatus)
	}
	if got.TransactionID != "0xf7a0218118eac132356b3ad9e6021ba32e6c1f35811269fe190fd60c75d5f20b" {
		t.Errorf("transactionID = %q", got.TransactionID)
	}
	if got.FromAddress != "0xe77b1452900b92a9d43cf87a079fe59c31b3f5ab" {
		t.Errorf("fromAddress = %q", got.FromAddress)
	}
	if got.ToAddress != "0xe77b1452900b92a9d43cf87a079fe59c31b3f5ab" {
		t.Errorf("toAddress = %q — LiFi reports the recipient at the top level, not under receiving", got.ToAddress)
	}
	if got.SendingChainID != 56 || got.SendingToken != "0x55d398326f99059fF775485246999027B3197955" {
		t.Errorf("sending = %d/%s", got.SendingChainID, got.SendingToken)
	}
	if got.SendingAmount == nil || got.SendingAmount.String() != "200000000000000000000" {
		t.Errorf("sendingAmount = %v", got.SendingAmount)
	}
	if got.DestChainID != 8453 || got.ReceivingToken != "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913" {
		t.Errorf("receiving = %d/%s", got.DestChainID, got.ReceivingToken)
	}
	if got.ReceivedBaseUnits == nil || got.ReceivedBaseUnits.String() != "199875400" {
		t.Errorf("received = %v", got.ReceivedBaseUnits)
	}
	if got.DestTxHash != "0x8fda092974d2e42be633243312f28e3bfe31cca8b962f053f607e5a418f35fd7" {
		t.Errorf("destTxHash = %q", got.DestTxHash)
	}
}

// receiving.toAddress still wins when LiFi does send one: it names the
// recipient of THIS leg, where the top-level toAddress is the transfer's
// stated destination. They agree in practice; preferring the specific one
// keeps a future divergence from being read as the wrong thing.
func TestLiFiStatus_PrefersTheReceivingLegsOwnRecipient(t *testing.T) {
	srv := lifiServer(t, 200, `{
		"status":"DONE","toAddress":"0x1111111111111111111111111111111111111111",
		"receiving":{"toAddress":"0x2222222222222222222222222222222222222222"}
	}`)
	defer srv.Close()

	got, err := NewLiFiStatusClient(srv.URL, "").Status(context.Background(), "0xsrc")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.ToAddress != "0x2222222222222222222222222222222222222222" {
		t.Fatalf("toAddress = %q", got.ToAddress)
	}
}

// The exact-output fixture is SYNTHETIC (see its _note): it stands in for a
// live /quote/toAmount capture that needs a funded wallet. What this pins is
// what the ramp binding depends on — that the parser tolerates the extra
// top-level keys, and that SendingAmount is the quote's action.fromAmount
// (not a figure reduced by a refund of unused input). A live capture must
// replace the file, and re-pass this test, before any deploy.
func TestLiFiStatus_ExactOutputSendingAmountIsTheQuotesFromAmount(t *testing.T) {
	body, err := os.ReadFile("testdata/lifi_status_exact_output.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var raw struct {
		Quote struct {
			Action struct {
				FromAmount string `json:"fromAmount"`
			} `json:"action"`
		} `json:"quote"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	srv := lifiServer(t, 200, string(body))
	defer srv.Close()
	got, err := NewLiFiStatusClient(srv.URL, "").Status(context.Background(), "0xsrc")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.SendingAmount == nil || got.SendingAmount.String() != raw.Quote.Action.FromAmount {
		t.Fatalf("sendingAmount = %v, want the quote's action.fromAmount %s", got.SendingAmount, raw.Quote.Action.FromAmount)
	}
	if got.Status != LiFiStatusDone || got.Substatus != LiFiSubstatusCompleted {
		t.Fatalf("status = %s/%s", got.Status, got.Substatus)
	}
}
