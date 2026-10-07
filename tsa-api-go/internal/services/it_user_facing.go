package services

import (
	"strings"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// UserFacingITFailure maps stored/technical IT failure strings to copy safe
// to show in the app. Full technical detail stays in logs / DB for operators.
func UserFacingITFailure(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "not operator"):
		return "We received your payment, but crediting is temporarily unavailable. Please check again shortly or contact support."
	case strings.Contains(lower, "gas estimation") || strings.Contains(lower, "execution reverted"):
		return "We received your payment, but crediting couldn’t finish yet. Please try again later or contact support."
	case strings.Contains(lower, "fx stale") || strings.Contains(lower, "awaiting rate") || strings.Contains(lower, "reprice"):
		return "We received your payment. A rate update is needed before we can credit your MCGP."
	case strings.Contains(lower, "underpaid"):
		return "The amount received was less than expected. Please contact support."
	case strings.Contains(lower, "superseded"):
		return "This order was replaced by a newer buy."
	case strings.Contains(lower, "credit") || strings.Contains(lower, "broadcast") || strings.Contains(lower, "verify"):
		return "We received your payment, but crediting didn’t complete. Please contact support with this order."
	default:
		return "Something went wrong with this order. Please contact support."
	}
}

// UserFacingOrderFailure is the ramp-order counterpart of UserFacingITFailure.
// A PaycrestOrderRecord's FailureReason is operator text — it names upstream
// vendors, transaction hashes and raw errors — so the user is told only what
// the order's state means for her money. The raw text stays in the DB and on
// the admin views.
func UserFacingOrderFailure(status, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	switch status {
	case models.PaycrestStatusRecoveryPending, models.PaycrestStatusUnmatched:
		return "This order couldn't finish automatically. We're on it — your funds are safe, and you'll be refunded if it can't complete."
	case models.PaycrestStatusRefunding:
		return "This order couldn't be completed, so your funds are being returned to you."
	case models.PaycrestStatusRefunded:
		return "This order couldn't be completed, so your funds were returned to you."
	case models.PaycrestStatusCancelled:
		if strings.EqualFold(raw, "cancelled by user") {
			return "You cancelled this order."
		}
		return "This order was cancelled."
	case models.PaycrestStatusExpired:
		return "This order expired before a payment arrived."
	case models.PaycrestStatusFailed:
		return "This order couldn't be completed. If money left your account, contact support with this order."
	default:
		// In flight or settled: whatever is stored is a stale operator note,
		// not something she can act on.
		return ""
	}
}

// UserFacingBillFailure maps a bill order's stored failure reason to copy safe
// to show in the app. The stored text can be the vendor's own remark or a raw
// error; it stays in the DB for the admin views. Our own reasons the user can
// act on are kept, anything else becomes a fixed sentence for the state.
func UserFacingBillFailure(status, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	switch status {
	case models.BillStatusVendFail, models.BillStatusRefunded:
		refund := "Your MCGP is being returned to your balance."
		if status == models.BillStatusRefunded {
			refund = "Your MCGP was returned to your balance."
		}
		switch strings.ToLower(raw) {
		case "plan no longer available":
			return "This plan is no longer available. " + refund
		case "service temporarily unavailable":
			return "This service is temporarily unavailable. " + refund
		}
		return "We couldn't complete this purchase. " + refund
	case models.BillStatusFailed:
		return "This payment didn't go through, so nothing was purchased. Please try again."
	case models.BillStatusExpired:
		return "This quote expired. Get a new quote to continue."
	default:
		// quoted / paying / vending carry a working note for the reconciler,
		// never something to show.
		return ""
	}
}
