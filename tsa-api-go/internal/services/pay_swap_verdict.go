package services

import (
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// A sent swap's fate is decided here, in one pure function, for both kinds of
// attempt. Before this the reconciler spelled the decision out per arm (naira
// and delivery each with their own copy of the "end the intent, then alert, then
// fail" block), and the two drifted apart — a delivery's cancelled intent was
// released at once while its swap could still mine. What differs between the
// kinds is now a few lines in this file, side by side.

// swapFacts is everything the verdict depends on: nothing else is read.
type swapFacts struct {
	// Delivery is a crypto payment's attempt (the swap pays the merchant's
	// wallet); false is a naira one (the swap lands USDC in her own wallet).
	Delivery bool
	// IntentStatus is the sell intent's own status; HasHash whether her swap was
	// ever recorded on it.
	IntentStatus string
	HasHash      bool
	// LiFi and Sub are what LiFi says became of the swap ("" when it has no
	// record). Mismatch is ConfirmDelivery's answer that LiFi called the swap
	// done but the merchant's wallet was not credited in full.
	LiFi, Sub string
	Mismatch  bool
	// Age is how long ago the customer took the payment.
	Age time.Duration
}

type swapVerdictKind int

const (
	verdictWait    swapVerdictKind = iota // nothing to decide yet
	verdictRelease                        // hand the payment back for another attempt
	verdictFail                           // fail it for support
)

// swapVerdict is what to do: how it ends, whether the intent is ended first (so
// a swap that lands later can no longer open a payout for a payment that has
// moved on), the alert to raise for a human, and the reason recorded.
type swapVerdict struct {
	kind   swapVerdictKind
	end    bool
	alert  string
	reason string
}

var (
	verdictWaiting = swapVerdict{}
)

func releaseAfterEnding(reason string) swapVerdict {
	return swapVerdict{kind: verdictRelease, end: true, reason: reason}
}

func failAfterEnding(alert, reason string) swapVerdict {
	return swapVerdict{kind: verdictFail, end: true, alert: alert, reason: reason}
}

// swapVerdictOf decides the fate of an attempt whose intent is past ordering
// (ordering and ordered are followed elsewhere). Every ending that follows a
// possible landing is a failure, never a release, wherever the value would go to
// the merchant.
func swapVerdictOf(f swapFacts) swapVerdict {
	if !f.HasHash {
		return noSwapVerdict(f)
	}
	if f.Mismatch {
		// LiFi says done but the merchant was not credited in full: the value may
		// have reached them, so the payment fails. The dead sale is ended first —
		// unless it is delivered, which is terminal and has nothing to end.
		v := failAfterEnding("delivery_mismatch", "the transfer didn't deliver the full amount — support is reviewing it")
		v.end = f.IntentStatus != models.SellIntentDelivered
		return v
	}
	switch {
	case f.LiFi == LiFiStatusFailed || (f.LiFi == LiFiStatusDone && f.Sub == LiFiSubstatusRefunded):
		return releaseAfterEnding("the swap didn't go through — your funds are back in your wallet")
	case f.LiFi == LiFiStatusDone && f.Sub == LiFiSubstatusPartial:
		return failAfterEnding("swap_partial", "the swap only partly completed — support is reviewing it")
	case f.LiFi == LiFiStatusDone && f.Sub == LiFiSubstatusCompleted && f.Delivery &&
		(f.IntentStatus == models.SellIntentSwapped || f.IntentStatus == models.SellIntentDelivered):
		// COMPLETED, on a sale ConfirmDelivery still looks at: the swap finished
		// between its read and ours, and the merchant WAS paid. Not a failure —
		// the next tick's proof decides.
		return verdictWaiting
	case f.LiFi == LiFiStatusDone && f.Delivery:
		// A delivery LiFi calls done that ConfirmDelivery cannot prove — not
		// completed, or a sale already ended: the merchant may have been paid, so
		// it is never released.
		return failAfterEnding("delivery_unproven", "the transfer couldn't be confirmed — support is reviewing it")
	case f.LiFi == LiFiStatusDone:
		if f.Age <= payLandedNoOrderGrace {
			return verdictWaiting
		}
		// The USDC landed in her wallet and she never paid with it; it stays
		// hers, and nothing can reach the merchant once the intent is ended.
		return releaseAfterEnding("the payment wasn't completed — the swapped funds are in your wallet")
	case f.LiFi == LiFiStatusPending:
		if f.Age <= payPendingSwapMax {
			return verdictWaiting
		}
		return failAfterEnding("swap_pending_too_long", "the swap is taking too long — support is reviewing it")
	default: // NOT_FOUND, INVALID, or no answer
		if f.Delivery {
			// A recorded swap LiFi cannot find may still be in the mempool, and a
			// delivery's pays the merchant when it mines: never released on this,
			// only failed once a day has passed.
			if f.Age <= payPendingSwapMax {
				return verdictWaiting
			}
			return failAfterEnding("delivery_swap_unknown", "the transfer can't be found — support is reviewing it")
		}
		if f.Age <= payUnknownSwapGrace {
			return verdictWaiting
		}
		return releaseAfterEnding("the swap could not be found")
	}
}

// noSwapVerdict is the fate of an intent no swap was ever recorded on. A naira
// sale's tokens never left her wallet, so a cancelled intent frees the payment at
// once. A delivery's swap, if she signed and never reported it, pays the MERCHANT
// when it mines — her cancelling (which ends the intent and bars any later hash)
// proves nothing about that, so the delivery grace applies to a cancelled intent
// too (see payDeliveryUnsignedGrace for the residual risk).
func noSwapVerdict(f swapFacts) swapVerdict {
	grace := payUnsignedSwapGrace
	if f.Delivery {
		grace = payDeliveryUnsignedGrace
	}
	if f.IntentStatus == models.SellIntentFailed {
		if f.Delivery && f.Age <= grace {
			return verdictWaiting
		}
		return swapVerdict{kind: verdictRelease, reason: "the swap was cancelled before it was sent"}
	}
	if f.Age <= grace {
		return verdictWaiting
	}
	return releaseAfterEnding("the swap was never sent")
}
