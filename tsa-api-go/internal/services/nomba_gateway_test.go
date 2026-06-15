package services

import (
	"context"
	"testing"
)

// mockGateway is the test double the orchestration plan will reuse.
type mockGateway struct {
	payoutID  string
	payoutErr error
}

func (m *mockGateway) InitiatePayout(_ context.Context, _ PayoutRequest) (string, error) {
	return m.payoutID, m.payoutErr
}

func TestNombaGateway_MockConforms(t *testing.T) {
	var g NombaGateway = &mockGateway{payoutID: "po_1"}
	id, err := g.InitiatePayout(context.Background(), PayoutRequest{
		BankCode: "044", AccountNumber: "0123456789", AmountNGN: "1500.00", Reference: "t-1",
	})
	if err != nil || id != "po_1" {
		t.Fatalf("InitiatePayout = (%q, %v), want (po_1, nil)", id, err)
	}
	in := InboundPayment{SenderAccountName: "JOHN DOE", SenderAccountNumber: "0123", AmountNGN: "1500.00", DestinationAccount: "acct_123", EventID: "ev_1", Status: "settled"}
	ps := PayoutStatus{PayoutID: "po_1", Status: "success", EventID: "ev_2"}
	if in.Status != "settled" || ps.Status != "success" {
		t.Fatal("DTO fields unexpectedly empty")
	}
}
