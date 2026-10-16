package services

import (
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestUserFacingITFailure_NotOperator(t *testing.T) {
	raw := "credit broadcast: gas estimation failed (transaction would likely revert): failed to estimate gas: execution reverted: Not operator"
	got := UserFacingITFailure(raw)
	if got == "" || strings.Contains(got, "Not operator") || strings.Contains(got, "gas estimation") {
		t.Fatalf("still technical: %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "payment") {
		t.Fatalf("expected payment reassurance, got %q", got)
	}
}

func TestUserFacingITFailure_EndingStatesAreNotFaults(t *testing.T) {
	for raw, want := range map[string]string{
		"cancelled by user":                         "You cancelled this order.",
		"expired before payment":                    "This order expired before a payment arrived.",
		"expired unpaid — start a new order to buy": "This order expired before a payment arrived.",
		"refunded in naira by an operator":          "This order was cancelled and your payment was refunded.",
		models.ITFailurePoolOpenFailed:              "This order couldn't be started. No money was taken — please try again.",
	} {
		if got := UserFacingITFailure(raw); got != want {
			t.Errorf("%q → %q, want %q", raw, got, want)
		}
	}
}

func TestUserFacingITFailure_Empty(t *testing.T) {
	if UserFacingITFailure("") != "" {
		t.Fatal("empty in → empty out")
	}
}

// vendorNames are the third-party names a user must never see.
var vendorNames = []string{"lifi", "li.fi", "paycrest", "flutterwave", "clubkonnect", "nomba", "stargate", "0x"}

func assertNoVendor(t *testing.T, got string) {
	t.Helper()
	lower := strings.ToLower(got)
	for _, name := range vendorNames {
		if strings.Contains(lower, name) {
			t.Fatalf("user text names %q: %q", name, got)
		}
	}
}

func TestUserFacingOrderFailure_NeverPassesOperatorText(t *testing.T) {
	raw := "LiFi delivered USDC, not USDT (tx 0xabc123) — Paycrest settlement leg"
	for _, status := range []string{
		models.PaycrestStatusRecoveryPending, models.PaycrestStatusUnmatched, models.PaycrestStatusRefunding,
		models.PaycrestStatusRefunded, models.PaycrestStatusCancelled, models.PaycrestStatusExpired,
		models.PaycrestStatusFailed,
	} {
		got := UserFacingOrderFailure(status, raw)
		if got == "" {
			t.Fatalf("%s: a terminal or parked order with a reason must say something", status)
		}
		assertNoVendor(t, got)
	}
	if got := UserFacingOrderFailure(models.PaycrestStatusRecoveryPending, raw); !strings.Contains(got, "funds are safe") {
		t.Fatalf("recovery copy must reassure: %q", got)
	}
	if got := UserFacingOrderFailure(models.PaycrestStatusRefunded, raw); !strings.Contains(got, "returned") {
		t.Fatalf("refunded copy must say the money came back: %q", got)
	}
	if got := UserFacingOrderFailure(models.PaycrestStatusCancelled, "cancelled by user"); got != "You cancelled this order." {
		t.Fatalf("user cancel: %q", got)
	}
}

func TestUserFacingOrderFailure_HidesStaleNotesOnLiveOrders(t *testing.T) {
	for _, status := range []string{models.PaycrestStatusConverting, models.PaycrestStatusSettling, models.PaycrestStatusSettled} {
		if got := UserFacingOrderFailure(status, "lifi reported bridge attempt 2 failed"); got != "" {
			t.Fatalf("%s: want nothing, got %q", status, got)
		}
	}
	if UserFacingOrderFailure(models.PaycrestStatusRefunded, "  ") != "" {
		t.Fatal("no reason in, no reason out")
	}
}

func TestUserFacingBillFailure_NeverPassesVendorRemark(t *testing.T) {
	remark := "CLUBKONNECT: INVALID_RECIPIENT — Nomba fallback refused"
	for _, status := range []string{models.BillStatusVendFail, models.BillStatusRefunded, models.BillStatusFailed, models.BillStatusExpired} {
		got := UserFacingBillFailure(status, remark)
		if got == "" {
			t.Fatalf("%s: want a sentence", status)
		}
		assertNoVendor(t, got)
	}
	if got := UserFacingBillFailure(models.BillStatusVendFail, "vend outcome unknown: clubkonnect: timeout"); !strings.Contains(got, "being returned") {
		t.Fatalf("vend_failed: %q", got)
	}
	// Our own reasons survive, reworded.
	if got := UserFacingBillFailure(models.BillStatusVendFail, "plan no longer available"); !strings.Contains(got, "plan is no longer available") {
		t.Fatalf("own reason lost: %q", got)
	}
	if got := UserFacingBillFailure(models.BillStatusVendFail, "service temporarily unavailable"); !strings.Contains(got, "temporarily unavailable") {
		t.Fatalf("own reason lost: %q", got)
	}
	for _, status := range []string{models.BillStatusQuoted, models.BillStatusPaying, models.BillStatusVending, models.BillStatusDelivered} {
		if got := UserFacingBillFailure(status, "debit unconfirmed: clubkonnect: 502"); got != "" {
			t.Fatalf("%s: working note leaked: %q", status, got)
		}
	}
}
