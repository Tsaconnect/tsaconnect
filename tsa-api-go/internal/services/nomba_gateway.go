package services

import "context"

// NombaGateway is the per-PM boundary to Nomba. A later plan implements it
// against the live API (payout endpoint + webhook payloads + signature) once
// design §12 is confirmed; the automation orchestration codes against this
// interface only.
type NombaGateway interface {
	// InitiatePayout sends NGN to a bank account from the PM's Nomba account and
	// returns Nomba's payout id. Status arrives later via a PayoutStatus webhook.
	InitiatePayout(ctx context.Context, req PayoutRequest) (payoutID string, err error)
}

// PayoutRequest is a sell-flow naira payout to the user's bank.
type PayoutRequest struct {
	BankCode      string
	AccountNumber string
	AmountNGN     string // decimal string, e.g. "1500.00"
	Reference     string // our trade reference (idempotency)
}

// InboundPayment is a normalized inbound naira credit (buy flow). The webhook
// receiver parses Nomba's raw payload into this. Status is "settled" when the
// credit is final (we only auto-release on settled — design §9).
type InboundPayment struct {
	EventID             string
	DestinationAccount  string // identifies which PM was paid
	SenderAccountName   string
	SenderAccountNumber string
	AmountNGN           string
	Status              string // "settled" | "pending" | ...
}

// PayoutStatus is a normalized payout-result webhook (sell flow).
type PayoutStatus struct {
	EventID  string
	PayoutID string
	Status   string // "success" | "failed" | "pending"
}
