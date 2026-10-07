package handlers

import (
	"strings"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// The pay routes hand a paying customer the same order and intent records the
// sell routes hand a seller — but here the payout bank is the MERCHANT's, not
// hers. These views blank it on the pay routes only; the models' JSON, and so
// every ordinary sell route, is untouched, and the shapes are otherwise
// identical (the blanked fields are omitempty or empty strings).
//
// Bank code and account name are blanked outright. The account number keeps its
// last four digits, enough for her to recognise where the money is going and no
// use as a destination.

// payMaskAccount keeps the last four digits of an account number.
func payMaskAccount(n string) string {
	n = strings.TrimSpace(n)
	if n == "" {
		return ""
	}
	if len(n) <= 4 {
		return strings.Repeat("*", len(n))
	}
	return strings.Repeat("*", len(n)-4) + n[len(n)-4:]
}

// payOrderView is o as a paying customer may see it. o itself is not changed.
func payOrderView(o *models.PaycrestOrderRecord) *models.PaycrestOrderRecord {
	if o == nil {
		return nil
	}
	v := *o
	v.PayoutBankCode = ""
	v.PayoutAccountName = ""
	v.PayoutAccount = payMaskAccount(o.PayoutAccount)
	// Our fee on a pay order is chosen per order to take the deposit's cushion:
	// an internal figure, not one the payer was quoted (she sees the checkout's).
	v.SenderFee = ""
	v.SenderFeePercent = ""
	v.FailureReason = services.UserFacingOrderFailure(o.Status, o.FailureReason)
	return &v
}

// payIntentView is in as a paying customer may see it. in itself is not changed.
func payIntentView(in *models.RampSellIntent) *models.RampSellIntent {
	if in == nil {
		return nil
	}
	v := *in
	v.BankCode = ""
	v.AccountName = ""
	v.AccountNumber = payMaskAccount(in.AccountNumber)
	return &v
}

// The ordinary sell and order routes return the same records to their owner. A
// record that is a pay payment's attempt (PayPaymentID set, at creation) is
// masked there too; an ordinary sale's keeps its own details. The check is a
// field on the record itself, so no route makes a lookup and a list is never
// an N+1.
//
// Every owner view replaces FailureReason with UserFacingOrderFailure: the
// stored text is for operators (vendor names, hashes, raw errors). Admin
// routes read the record directly and keep it.

// orderForCaller is o as its owner may see it. o itself is not changed.
func orderForCaller(o *models.PaycrestOrderRecord) *models.PaycrestOrderRecord {
	if o == nil {
		return nil
	}
	if o.PayPaymentID != nil {
		return payOrderView(o)
	}
	v := *o
	v.FailureReason = services.UserFacingOrderFailure(o.Status, o.FailureReason)
	return &v
}

// intentForCaller is in as its owner may see it.
func intentForCaller(in *models.RampSellIntent) *models.RampSellIntent {
	if in != nil && in.PayPaymentID != nil {
		return payIntentView(in)
	}
	return in
}

// ordersForCaller is orderForCaller over rows, in a copy.
func ordersForCaller(rows []models.PaycrestOrderRecord) []models.PaycrestOrderRecord {
	out := make([]models.PaycrestOrderRecord, len(rows))
	for i := range rows {
		out[i] = *orderForCaller(&rows[i])
	}
	return out
}
