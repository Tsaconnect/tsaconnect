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
	// Refusals that happen before any request is sent are rejections: no
	// transfer can exist.
	if !g.client.IsEnabled() {
		return "", &ProviderRejectedError{Err: ErrFlutterwaveDisabled}
	}
	// Prefer the explicit Amount/SourceCurrency pair; fall back to the legacy
	// naira-only field so callers that have not migrated keep working unchanged.
	rawAmount := strings.TrimSpace(req.Amount)
	if rawAmount == "" {
		rawAmount = strings.TrimSpace(req.AmountNGN)
	}
	amount, err := strconv.ParseFloat(rawAmount, 64)
	if err != nil || amount <= 0 {
		return "", &ProviderRejectedError{Err: fmt.Errorf("flutterwave payout: invalid amount %q", rawAmount)}
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

	// applies_to is ALWAYS destination_currency: `value` is what the recipient
	// receives, and Flutterwave debits our source balance with whatever that
	// costs, billing its own fee separately.
	//
	// This was previously source_currency for cross-currency payouts, on the
	// reasoning that we hold naira and should send a known naira amount. That
	// reasoning double-charged the user. Our fee engine already withholds a
	// provider-fee passthrough line and disburses the NET; sending that net as
	// the SOURCE amount meant Flutterwave then took its own fee out of it
	// before converting, so the user paid the provider fee twice — once to us,
	// once off the top — and received less than the amount they were quoted.
	//
	// Flutterwave's own on/off-ramp blueprint uses destination_currency for
	// every transfer including its two cross-currency conversions, so this is
	// the documented pattern rather than a workaround.
	//
	// The trade-off is deliberate: TSA now absorbs rate movement between quote
	// and execution, because we are committing to what the recipient gets. That
	// window is seconds, and a bounded FX exposure is a better thing to carry
	// than telling a user they will receive an amount that then arrives short.
	pi := FWPaymentInstruction{
		SourceCurrency:      srcCur,
		DestinationCurrency: destCur,
		Amount:              FWAmount{Value: amount, AppliesTo: "destination_currency"},
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
		if len(req.Details) > 0 {
			pi.Recipient = payoutRecipientFromDetails(req.Details)
			break
		}
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
	// A transfer that is already FAILED when it is created is a refusal, not a
	// payout in flight: no webhook will ever turn it into a success, and handing
	// its id back as live left sells waiting forever. Checked here rather than in
	// DirectTransfer because conversions and crypto deliveries depend on getting
	// the FAILED transfer back to mint their next attempt.
	if strings.EqualFold(strings.TrimSpace(transfer.Status), "FAILED") {
		return "", &ProviderRejectedError{
			Err:      fmt.Errorf("flutterwave payout: transfer %s was created FAILED", transfer.ID),
			Replayed: transfer.CacheHit,
		}
	}
	return transfer.ID, nil
}

// payoutRecipientFromDetails maps a corridor's recipient detail (keys from
// it_withdraw_catalog.go) onto the v4 recipient. Only what the corridor
// collected is set; everything else is omitted, so one mapper serves every
// corridor and Flutterwave's own validation stays the last word.
func payoutRecipientFromDetails(d map[string]string) FWTransferRecipient {
	r := FWTransferRecipient{
		Bank: &FWRecipientBank{
			Code:          d[PayoutFieldBank],
			AccountNumber: d[PayoutFieldAccountNumber],
			RoutingNumber: d[PayoutFieldRoutingNumber],
			SwiftCode:     d[PayoutFieldSwiftCode],
			SortCode:      d[PayoutFieldSortCode],
			Name:          d[PayoutFieldBankName],
			AccountType:   d[PayoutFieldAccountType],
			Branch:        d[PayoutFieldBranch],
		},
		Name:  &FWRecipientName{First: d[PayoutFieldFirstName], Last: d[PayoutFieldLastName]},
		Email: d[PayoutFieldEmail],
	}
	if d[PayoutFieldPhoneNumber] != "" {
		r.Phone = &FWRecipientPhone{CountryCode: d[PayoutFieldPhoneCode], Number: d[PayoutFieldPhoneNumber]}
	}
	if d[PayoutFieldAddressLine1] != "" {
		r.Address = &FWRecipientAddress{
			Line1:      d[PayoutFieldAddressLine1],
			City:       d[PayoutFieldCity],
			State:      d[PayoutFieldState],
			PostalCode: d[PayoutFieldPostalCode],
			Country:    d[PayoutFieldAddressCountry],
		}
	}
	return r
}
