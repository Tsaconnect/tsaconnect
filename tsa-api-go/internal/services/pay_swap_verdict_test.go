package services

import (
	"strings"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// swapVerdictOf is pure: every fate of a sent swap, for both kinds of attempt,
// is pinned here directly, boundaries included. (The reconcile tests pin the same
// rules end to end through the executor.)
func TestSwapVerdictOf_EveryFate(t *testing.T) {
	const just = time.Nanosecond
	swapped, failed, delivered := models.SellIntentSwapped, models.SellIntentFailed, models.SellIntentDelivered
	type want struct {
		kind   swapVerdictKind
		end    bool
		alert  string
		reason string // a fragment of the recorded reason
	}
	wait := want{kind: verdictWait}
	rel := func(end bool, reason string) want { return want{verdictRelease, end, "", reason} }
	fail := func(alert string) want { return want{verdictFail, true, alert, ""} }

	f := func(delivery bool, intent string, hash bool, lifi, sub string, age time.Duration) swapFacts {
		return swapFacts{Delivery: delivery, IntentStatus: intent, HasHash: hash, LiFi: lifi, Sub: sub, Age: age}
	}
	for name, tc := range map[string]struct {
		facts swapFacts
		want  want
	}{
		// no swap ever recorded
		"naira, no hash, inside the grace":                   {f(false, models.SellIntentSwapping, false, "", "", payUnsignedSwapGrace), wait},
		"naira, no hash, past the grace":                     {f(false, models.SellIntentSwapping, false, "", "", payUnsignedSwapGrace+just), rel(true, "never sent")},
		"naira, cancelled, released at once":                 {f(false, failed, false, "", "", 0), rel(false, "cancelled before")},
		"delivery, no hash, past the naira grace":            {f(true, models.SellIntentSwapping, false, "", "", payUnsignedSwapGrace+just), wait},
		"delivery, no hash, inside its grace":                {f(true, models.SellIntentSwapping, false, "", "", payDeliveryUnsignedGrace), wait},
		"delivery, no hash, past its grace":                  {f(true, models.SellIntentSwapping, false, "", "", payDeliveryUnsignedGrace+just), rel(true, "never sent")},
		"delivery, cancelled, inside the grace":              {f(true, failed, false, "", "", payDeliveryUnsignedGrace), wait},
		"delivery, cancelled, past the grace":                {f(true, failed, false, "", "", payDeliveryUnsignedGrace+just), rel(false, "cancelled before")},
		"delivery, cancelled, the naira grace is not enough": {f(true, failed, false, "", "", payUnsignedSwapGrace+just), wait},

		// LiFi says the swap did not go through: conclusive for both kinds
		"naira, lifi failed":    {f(false, swapped, true, LiFiStatusFailed, "", 0), rel(true, "didn't go through")},
		"delivery, lifi failed": {f(true, swapped, true, LiFiStatusFailed, "", 0), rel(true, "didn't go through")},
		"naira, refunded":       {f(false, swapped, true, LiFiStatusDone, LiFiSubstatusRefunded, 0), rel(true, "didn't go through")},
		"delivery, refunded":    {f(true, swapped, true, LiFiStatusDone, LiFiSubstatusRefunded, 0), rel(true, "didn't go through")},
		"naira, partial":        {f(false, swapped, true, LiFiStatusDone, LiFiSubstatusPartial, 0), fail("swap_partial")},
		"delivery, partial":     {f(true, swapped, true, LiFiStatusDone, LiFiSubstatusPartial, 0), fail("swap_partial")},

		// done
		"delivery, completed, open sale: the proof decides": {f(true, swapped, true, LiFiStatusDone, LiFiSubstatusCompleted, 0), wait},
		"delivery, completed, delivered: the proof decides": {f(true, delivered, true, LiFiStatusDone, LiFiSubstatusCompleted, 0), wait},
		"delivery, completed, sale already ended":           {f(true, failed, true, LiFiStatusDone, LiFiSubstatusCompleted, 0), fail("delivery_unproven")},
		"delivery, done with no explanation":                {f(true, swapped, true, LiFiStatusDone, "", 0), fail("delivery_unproven")},
		"naira, done, inside the landed grace":              {f(false, swapped, true, LiFiStatusDone, LiFiSubstatusCompleted, payLandedNoOrderGrace), wait},
		"naira, done, past the landed grace":                {f(false, swapped, true, LiFiStatusDone, LiFiSubstatusCompleted, payLandedNoOrderGrace+just), rel(true, "swapped funds are in your wallet")},

		// pending
		"naira, pending, inside the bound":  {f(false, swapped, true, LiFiStatusPending, "", payPendingSwapMax), wait},
		"naira, pending, past the bound":    {f(false, swapped, true, LiFiStatusPending, "", payPendingSwapMax+just), fail("swap_pending_too_long")},
		"delivery, pending, inside":         {f(true, swapped, true, LiFiStatusPending, "", payPendingSwapMax), wait},
		"delivery, pending, past the bound": {f(true, swapped, true, LiFiStatusPending, "", payPendingSwapMax+just), fail("swap_pending_too_long")},

		// LiFi has no record
		"naira, unknown, inside the grace":   {f(false, swapped, true, "NOT_FOUND", "", payUnknownSwapGrace), wait},
		"naira, unknown, past the grace":     {f(false, swapped, true, "NOT_FOUND", "", payUnknownSwapGrace+just), rel(true, "could not be found")},
		"naira, no answer at all":            {f(false, swapped, true, "", "", payUnknownSwapGrace+just), rel(true, "could not be found")},
		"naira, invalid":                     {f(false, swapped, true, "INVALID", "", payUnknownSwapGrace+just), rel(true, "could not be found")},
		"delivery, unknown, the naira grace": {f(true, swapped, true, "NOT_FOUND", "", payUnknownSwapGrace+just), wait},
		"delivery, unknown, inside the day":  {f(true, swapped, true, "NOT_FOUND", "", payPendingSwapMax), wait},
		"delivery, unknown, past the day":    {f(true, swapped, true, "NOT_FOUND", "", payPendingSwapMax+just), fail("delivery_swap_unknown")},
		"delivery, invalid, past the day":    {f(true, swapped, true, "INVALID", "", payPendingSwapMax+just), fail("delivery_swap_unknown")},
		"delivery, no answer, past the day":  {f(true, swapped, true, "", "", payPendingSwapMax+just), fail("delivery_swap_unknown")},
	} {
		t.Run(name, func(t *testing.T) {
			got := swapVerdictOf(tc.facts)
			if got.kind != tc.want.kind || got.end != tc.want.end || got.alert != tc.want.alert || !strings.Contains(got.reason, tc.want.reason) {
				t.Fatalf("verdict = %+v, want %+v", got, tc.want)
			}
			if got.kind == verdictWait && (got.end || got.alert != "" || got.reason != "") {
				t.Fatalf("a wait carries no action: %+v", got)
			}
			if got.kind == verdictFail && got.alert == "" {
				t.Fatalf("a failure always alerts a human: %+v", got)
			}
		})
	}

	// A mismatch (the merchant was not credited in full) is a failure for both
	// kinds at any age, and ends the sale — except a delivered one, which is
	// terminal and has nothing to end.
	for _, intent := range []string{swapped, delivered} {
		facts := f(true, intent, true, LiFiStatusDone, LiFiSubstatusCompleted, time.Hour)
		facts.Mismatch = true
		got := swapVerdictOf(facts)
		if got.kind != verdictFail || got.alert != "delivery_mismatch" || got.end != (intent != delivered) {
			t.Fatalf("mismatch on %s: %+v", intent, got)
		}
	}
}

// The property the kinds must not drift on: a delivery is never RELEASED by
// anything a naira attempt would be released by unless LiFi is conclusive that the
// swap did not happen (failed or refunded).
func TestSwapVerdictOf_ADeliveryIsOnlyReleasedOnConclusiveEvidence(t *testing.T) {
	for _, intent := range []string{models.SellIntentSwapping, models.SellIntentSwapped, models.SellIntentFailed} {
		for _, hash := range []bool{true, false} {
			for _, lifi := range []string{"", "NOT_FOUND", "INVALID", LiFiStatusPending, LiFiStatusDone, LiFiStatusFailed} {
				for _, sub := range []string{"", LiFiSubstatusCompleted, LiFiSubstatusPartial, LiFiSubstatusRefunded} {
					for _, age := range []time.Duration{0, time.Minute, payUnsignedSwapGrace + time.Second, payUnknownSwapGrace + time.Second, payLandedNoOrderGrace + time.Second} {
						v := swapVerdictOf(swapFacts{Delivery: true, IntentStatus: intent, HasHash: hash, LiFi: lifi, Sub: sub, Age: age})
						if v.kind != verdictRelease {
							continue
						}
						conclusive := hash && (lifi == LiFiStatusFailed || (lifi == LiFiStatusDone && sub == LiFiSubstatusRefunded))
						noSwapEver := !hash && age > payDeliveryUnsignedGrace
						if !conclusive && !noSwapEver {
							t.Fatalf("delivery released on %s hash=%v %s/%s age=%s: %+v", intent, hash, lifi, sub, age, v)
						}
					}
				}
			}
		}
	}
}
