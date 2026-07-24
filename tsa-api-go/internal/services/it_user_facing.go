package services

import "strings"

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
