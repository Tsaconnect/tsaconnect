package services

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// The shape Flutterwave actually serves, captured from production on
// 2026-09-17 while central-mode credits were going unattributed:
//
//	data.transactions[]  — not a bare array
//	transaction_direction / transaction_type  — not direction / type
//	amount: {value, currency}  — not a bare number
//	balance: {before, after, currency}  — before/after ARE bare numbers
//	cursor: {has_more_items, limit, total}
//
// Every one of those differed from what the client expected, and the last
// three fail silently rather than loudly: a row that decodes with an empty
// direction and type is simply never seen as customer money.
const fwLiveStatementBody = `{
  "status": "success",
  "message": "Wallet statement retrieved",
  "data": {
    "cursor": {"has_more_items": false, "limit": 50, "total": 2},
    "transactions": [
      {
        "transaction_direction": "credit",
        "transaction_type": "funding",
        "amount": {"value": 300.00, "currency": "NGN"},
        "sent_amount": {"value": 300.00, "currency": "NGN"},
        "balance": {"before": 1000.50, "after": 1300.50, "currency": "NGN"},
        "reference": "100004250917TRF000001",
        "remarks": "GTBANK|ADA LOVELACE",
        "transaction_date": "2026-09-17T15:30:00Z"
      },
      {
        "transaction_direction": "debit",
        "transaction_type": "fee",
        "amount": {"value": 4.20, "currency": "NGN"},
        "sent_amount": {"value": 4.20, "currency": "NGN"},
        "balance": {"before": 1300.50, "after": 1296.30, "currency": "NGN"},
        "reference": "100004250917TRF000001",
        "remarks": "Funding fee",
        "transaction_date": "2026-09-17T15:30:01Z"
      }
    ]
  }
}`

func newLiveShapeClient(t *testing.T, body string, calls *int) *FlutterwaveClient {
	t.Helper()
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/wallets/statement", func(w http.ResponseWriter, r *http.Request) {
		*calls++
		_, _ = w.Write([]byte(body))
	})
	c, _ := newFWTestServer(t, mux)
	return c
}

func TestFetchWalletStatement_ReadsTheShapeFlutterwaveActuallyServes(t *testing.T) {
	calls := 0
	c := newLiveShapeClient(t, fwLiveStatementBody, &calls)
	now := time.Now().UTC()

	rows, err := c.FetchWalletStatement(context.Background(), "NGN", now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatalf("FetchWalletStatement: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 from data.transactions", len(rows))
	}

	credit := rows[0]
	// Direction and type decide whether a row is customer money at all.
	if credit.Direction != "credit" {
		t.Errorf("Direction = %q, want %q — a blank direction is never seen as funding", credit.Direction, "credit")
	}
	if credit.Type != fwRowTypeFunding {
		t.Errorf("Type = %q, want %q", credit.Type, fwRowTypeFunding)
	}
	if got := credit.Amount.Float64(); got != 300.00 {
		t.Errorf("Amount = %v, want 300.00 from amount.value", got)
	}
	if credit.Currency != "NGN" {
		t.Errorf("Currency = %q, want NGN from amount.currency", credit.Currency)
	}
	if got := credit.Balance.Before.Float64(); got != 1000.50 {
		t.Errorf("Balance.Before = %v, want 1000.50", got)
	}
	if got := credit.Balance.After.Float64(); got != 1300.50 {
		t.Errorf("Balance.After = %v, want 1300.50", got)
	}
	if credit.Reference != "100004250917TRF000001" || credit.Remarks != "GTBANK|ADA LOVELACE" {
		t.Errorf("reference/remarks did not survive: %q / %q", credit.Reference, credit.Remarks)
	}
	if credit.TransactionDate.IsZero() {
		t.Error("TransactionDate is zero — the ingestor windows on it")
	}
	if rows[1].Type != "fee" || rows[1].Direction != "debit" {
		t.Errorf("fee row = %q/%q, want debit/fee", rows[1].Direction, rows[1].Type)
	}

	// has_more_items=false is the provider saying the window is fully read.
	if calls != 1 {
		t.Errorf("made %d requests, want 1 — has_more_items=false ends the walk", calls)
	}
}

// The 2026-09-02 measurement saw a bare array. Keep reading it, so a provider
// that serves either shape cannot break ingestion again.
func TestFetchWalletStatement_StillReadsABareArray(t *testing.T) {
	calls := 0
	body := `{"status":"success","data":[{"direction":"credit","type":"funding","amount":12.5,` +
		`"balance":{"before":0,"after":12.5},"reference":"R1","remarks":"GTBANK|ADA","transaction_date":"2026-09-17T10:00:00Z"}]}`
	c := newLiveShapeClient(t, body, &calls)
	now := time.Now().UTC()

	rows, err := c.FetchWalletStatement(context.Background(), "NGN", now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatalf("legacy array: %v", err)
	}
	if len(rows) != 1 || rows[0].Direction != "credit" || rows[0].Type != fwRowTypeFunding || rows[0].Amount.Float64() != 12.5 {
		t.Fatalf("legacy row did not decode: %+v", rows)
	}
}
