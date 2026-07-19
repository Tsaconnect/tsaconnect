package services

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// FlutterwaveGateway implements NombaGateway (the payout seam consumed by PM
// automation + PM withdrawals) against the Flutterwave v4 direct-transfer
// orchestrator. Payouts are funded from the merchant's NGN balance — the
// treasury keeps that balance pre-funded (crypto lands in FW via the
// dashboard-only deposit path; conversion happens via ConvertBalance).
type FlutterwaveGateway struct {
	client     *FlutterwaveClient
	senderName string
}

// NewFlutterwaveGateway wires the gateway; senderName lands in the transfer
// narration for recipient bank statements.
func NewFlutterwaveGateway(client *FlutterwaveClient, senderName string) *FlutterwaveGateway {
	return &FlutterwaveGateway{client: client, senderName: senderName}
}

// InitiatePayout sends NGN to a bank account and returns Flutterwave's
// transfer id. Final status arrives via the transfer.disburse webhook. The
// trade reference doubles as transfer reference AND idempotency key, so a
// duplicated call cannot double-pay.
func (g *FlutterwaveGateway) InitiatePayout(ctx context.Context, req PayoutRequest) (string, error) {
	if !g.client.IsEnabled() {
		return "", ErrFlutterwaveDisabled
	}
	amount, err := strconv.ParseFloat(strings.TrimSpace(req.AmountNGN), 64)
	if err != nil || amount <= 0 {
		return "", fmt.Errorf("flutterwave payout: invalid amount %q", req.AmountNGN)
	}
	narration := g.senderName
	if narration == "" {
		narration = "TSA Connect"
	}
	transfer, err := g.client.DirectTransfer(ctx, FWDirectTransferRequest{
		Action:    "instant",
		Type:      "bank",
		Reference: req.Reference,
		Narration: narration,
		PaymentInstruction: FWPaymentInstruction{
			SourceCurrency:      "NGN",
			DestinationCurrency: "NGN",
			Amount:              FWAmount{Value: amount, AppliesTo: "destination_currency"},
			Recipient: FWTransferRecipient{
				Bank: &FWRecipientBank{
					Code:          req.BankCode,
					AccountNumber: req.AccountNumber,
				},
				Name: &FWRecipientName{Full: req.AccountName},
			},
		},
	})
	if err != nil {
		return "", err
	}
	return transfer.ID, nil
}
