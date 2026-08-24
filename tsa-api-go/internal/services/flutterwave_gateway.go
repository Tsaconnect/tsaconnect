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
	// Prefer the explicit Amount/SourceCurrency pair; fall back to the legacy
	// naira-only field so callers that have not migrated keep working unchanged.
	rawAmount := strings.TrimSpace(req.Amount)
	if rawAmount == "" {
		rawAmount = strings.TrimSpace(req.AmountNGN)
	}
	amount, err := strconv.ParseFloat(rawAmount, 64)
	if err != nil || amount <= 0 {
		return "", fmt.Errorf("flutterwave payout: invalid amount %q", rawAmount)
	}
	narration := g.senderName
	if narration == "" {
		narration = "TSA Connect"
	}

	destCur := req.DestinationCurrency
	if strings.TrimSpace(destCur) == "" {
		destCur = "NGN"
	}
	destCur = strings.ToUpper(strings.TrimSpace(destCur))
	rail := req.RailType
	if strings.TrimSpace(rail) == "" {
		rail = "bank"
	}
	rail = strings.ToLower(strings.TrimSpace(rail))

	srcCur := strings.ToUpper(strings.TrimSpace(req.SourceCurrency))
	if srcCur == "" {
		srcCur = "NGN"
	}

	// applies_to governs fee incidence. For same-currency payouts (source ==
	// destination) we must keep the legacy behavior: the recipient gets
	// exactly `value`, fees come out of our source balance separately —
	// that's "destination_currency". For cross-currency payouts we hold the
	// source currency and send a known amount of it for FW to convert, so fees
	// must be taken from the source side — "source_currency".
	//
	// The test is source-vs-destination, not a naira literal. Those coincided
	// while every payout was funded from naira; once a payout can draw a cedi
	// balance, a GHS→GHS transfer must get the exact-amount treatment that
	// NGN→NGN gets, and comparing against "NGN" would silently deny it.
	appliesTo := "source_currency"
	if strings.EqualFold(destCur, srcCur) {
		appliesTo = "destination_currency"
	}

	pi := FWPaymentInstruction{
		SourceCurrency:      srcCur,
		DestinationCurrency: destCur,
		Amount:              FWAmount{Value: amount, AppliesTo: appliesTo},
	}
	switch rail {
	case "mobile_money":
		pi.Recipient = FWTransferRecipient{
			MobileMoney: &FWRecipientMobileMoney{
				Network: req.MobileNetwork,
				Country: req.MobileCountry,
				Msisdn:  req.MobileMsisdn,
			},
			Name: &FWRecipientName{Full: req.AccountName},
		}
	default: // "bank"
		pi.Recipient = FWTransferRecipient{
			Bank: &FWRecipientBank{
				Code:          req.BankCode,
				AccountNumber: req.AccountNumber,
			},
			Name: &FWRecipientName{Full: req.AccountName},
		}
	}

	transfer, err := g.client.DirectTransfer(ctx, FWDirectTransferRequest{
		Action:             "instant",
		Type:               rail,
		Reference:          req.Reference,
		Narration:          narration,
		PaymentInstruction: pi,
	})
	if err != nil {
		return "", err
	}
	return transfer.ID, nil
}
