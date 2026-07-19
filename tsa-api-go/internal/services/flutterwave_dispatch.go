package services

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// ITWebhookHooks is the IT-service slice the Flutterwave dispatcher routes
// IT-prefixed references to. *ITService satisfies it.
type ITWebhookHooks interface {
	AutoCreditFromInbound(ctx context.Context, paymentRef, amountNGN string) error
	ResolvePayoutFromWebhook(ctx context.Context, merchantTxRef, status string) error
}

// SetITHooks wires IT webhook routing (buy auto-credit + sell payout close).
func (s *PMAutomationService) SetITHooks(h ITWebhookHooks) { s.itHooks = h }

// DispatchFlutterwave is the Flutterwave counterpart of Dispatch: it owns
// classify→parse→dispatch for v4 events so the webhook handler (live path)
// and the retry job drive identical logic.
//
//	charge.completed  → ParseFlutterwaveInbound  → HandleInbound
//	transfer.disburse → ParseFlutterwavePayout   → HandlePayoutStatus
//	transfer.reversal → logged, no automated action (funds returned AFTER a
//	                    success — crypto may already be released; this is an
//	                    operator case, surfaced via ErrNonSettlingEvent so the
//	                    event is marked processed rather than retried forever)
//	anything else     → ErrNonSettlingEvent (the v4 event vocabulary may grow;
//	                    unknown types are acknowledged, logged, and ignored)
func (s *PMAutomationService) DispatchFlutterwave(ctx context.Context, body []byte) error {
	eventType, webhookID, err := ClassifyFlutterwaveEvent(body)
	if err != nil {
		return fmt.Errorf("dispatch flutterwave: %w", err)
	}
	switch eventType {
	case fwEventChargeCompleted:
		p, err := ParseFlutterwaveInbound(body)
		if err != nil {
			// Propagates ErrNonSettlingEvent for failed/pending charges unchanged.
			return err
		}
		// Only NGN credits are attributable today (IT/PM ledgers are NGN-
		// denominated). A non-NGN credit is real money landing in the FW
		// balance with no automated home — park it for the operator instead of
		// mis-crediting an NGN ledger at 1:1.
		if p.Currency != "" && p.Currency != "NGN" {
			log.Printf("[fw dispatch] %s in unsupported currency %q (ref=%q event=%s) — operator action required", eventType, p.Currency, p.AccountRef, webhookID)
			return fmt.Errorf("%w: non-NGN charge currency %q", ErrNonSettlingEvent, p.Currency)
		}
		// IT buy references ("IT-…") route to the IT auto-credit; everything
		// else is a PM inbound.
		if s.itHooks != nil && strings.HasPrefix(p.AccountRef, "IT-") {
			return s.itHooks.AutoCreditFromInbound(ctx, p.AccountRef, p.AmountNGN)
		}
		return s.HandleInbound(ctx, *p)
	case fwEventTransferDisburse:
		p, err := ParseFlutterwavePayout(body)
		if err != nil {
			return err
		}
		// IT sell payouts route to the IT settle/reverse; bare-UUID refs are
		// PM-trade payouts.
		if s.itHooks != nil && strings.HasPrefix(p.MerchantTxRef, ITPayoutRefPrefix) {
			return s.itHooks.ResolvePayoutFromWebhook(ctx, p.MerchantTxRef, p.Status)
		}
		return s.HandlePayoutStatus(ctx, *p)
	case fwEventTransferReversal:
		p, err := ParseFlutterwavePayout(body)
		if err != nil {
			return err
		}
		log.Printf("[fw dispatch] transfer REVERSED (payoutId=%q ref=%q event=%s) — operator action required, no automated re-drive", p.PayoutID, p.MerchantTxRef, webhookID)
		return fmt.Errorf("%w: transfer reversal", ErrNonSettlingEvent)
	default:
		log.Printf("[fw dispatch] unknown event type %q (event=%s) — acknowledged, ignored", eventType, webhookID)
		return fmt.Errorf("%w: unknown flutterwave event type %q", ErrNonSettlingEvent, eventType)
	}
}
