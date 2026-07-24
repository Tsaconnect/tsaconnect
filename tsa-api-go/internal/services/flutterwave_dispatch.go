package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
)

// ITWebhookHooks is the IT-service slice the Flutterwave dispatcher routes
// IT-prefixed references to. *ITService satisfies it.
type ITWebhookHooks interface {
	AutoCreditFromInbound(ctx context.Context, paymentRef, amountNGN string) error
	AutoCreditFromVA(ctx context.Context, fwCustomerID, reference, chargeID, amountNGN string) error
	ResolvePayoutFromWebhook(ctx context.Context, merchantTxRef, status string) error
}

// SetITHooks wires IT webhook routing (buy auto-credit + sell payout close).
func (s *PMAutomationService) SetITHooks(h ITWebhookHooks) { s.itHooks = h }

// FlutterwaveChargeGetter re-fetches a charge from the Flutterwave API so a
// charge.completed webhook can be independently verified before any money
// moves. *FlutterwaveClient satisfies it.
type FlutterwaveChargeGetter interface {
	GetCharge(ctx context.Context, id string) (FWCharge, error)
}

// SetFlutterwaveChargeVerifier wires the API-side charge verification used by
// the charge.completed path. When left nil, charge settlement is refused
// (fail-closed) — a signed webhook is never sufficient on its own.
func (s *PMAutomationService) SetFlutterwaveChargeVerifier(g FlutterwaveChargeGetter) {
	s.fwCharges = g
}

// Charge-verification outcomes are split by whether a retry could ever change
// them — this matters because a failed webhook is re-driven by the reconciler
// (Flutterwave's own re-delivery is deduped away on webhook_id), and only a
// bounded/terminal disposition stops that loop:
//
//   - ErrChargeRejected — TERMINAL. The API contradicts the webhook in a way a
//     retry can't fix (known-dead status, wrong currency, reference mismatch,
//     missing charge id). Fail-closed + loud alarm; the event is marked
//     'rejected' and never re-driven.
//   - ErrChargePending — RETRYABLE. The charge is not yet succeeded but might
//     become so (pending/processing/unknown status). Left 'failed' so the
//     reconciler re-checks until it succeeds or turns terminal.
//   - ErrFWChargeNotFound — RETRYABLE, THEN TERMINAL. A 404 usually means the
//     charge is not visible to the API *yet* (read-after-write lag right after
//     the webhook fires), so it must be retried — but a charge that is still
//     absent much later never existed on this tenant and can never settle.
//     RetryFailedWebhooks owns that cutoff, because only it knows the event's
//     age; see fwChargeNotFoundGrace.
//   - a bare transport/API error — RETRYABLE (provider unreachable).
var (
	ErrChargeRejected = errors.New("flutterwave charge rejected (terminal: contradicts webhook, will not settle)")
	ErrChargePending  = errors.New("flutterwave charge not yet succeeded (retryable)")
)

// fwChargeTerminal reports whether a charge status is a KNOWN dead end (will
// never become succeeded). Unknown statuses are deliberately excluded so an
// unfamiliar in-progress state keeps retrying rather than dropping a real
// payment.
func fwChargeTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "cancelled", "canceled", "reversed", "void", "voided", "declined", "expired":
		return true
	}
	return false
}

// verifyFlutterwaveCharge re-fetches the charge named by the webhook and confirms
// it against the provider's authoritative record before crediting. It returns the
// API amount (the source of truth used for the actual credit) on success. See the
// sentinel docs above for the retryable/terminal split.
//
// A webhook/API amount divergence is NOT fatal — the API is authoritative, so we
// credit the API amount and log the divergence for observability.
func verifyFlutterwaveCharge(ctx context.Context, g FlutterwaveChargeGetter, p *InboundPayment) (amountNGN string, err error) {
	if p.ProviderChargeID == "" {
		// A charge.completed with no id is malformed and won't self-heal.
		return "", fmt.Errorf("%w: webhook carried no charge id (ref=%q)", ErrChargeRejected, p.AccountRef)
	}
	ch, err := g.GetCharge(ctx, p.ProviderChargeID)
	if err != nil {
		// Transport / provider error — retryable, do not classify as a rejection.
		// ErrFWChargeNotFound rides through wrapped so the retry job can age it
		// out into a terminal rejection rather than re-driving it forever.
		return "", fmt.Errorf("flutterwave verify charge %s: %w", p.ProviderChargeID, err)
	}
	if !fwSucceeded(ch.Status) {
		if fwChargeTerminal(ch.Status) {
			return "", fmt.Errorf("%w: api status %q for charge %s (ref=%q)", ErrChargeRejected, ch.Status, p.ProviderChargeID, p.AccountRef)
		}
		// Not-yet-succeeded (pending/processing/unknown) — retry until it settles.
		return "", fmt.Errorf("%w: api status %q for charge %s (ref=%q)", ErrChargePending, ch.Status, p.ProviderChargeID, p.AccountRef)
	}
	if cur := strings.ToUpper(strings.TrimSpace(ch.Currency)); cur != "NGN" {
		return "", fmt.Errorf("%w: api currency %q for charge %s", ErrChargeRejected, cur, p.ProviderChargeID)
	}
	if ch.Reference != p.AccountRef {
		// Attribution mismatch — crediting would apply funds to the wrong order.
		return "", fmt.Errorf("%w: api reference %q != webhook reference %q (charge %s)", ErrChargeRejected, ch.Reference, p.AccountRef, p.ProviderChargeID)
	}
	apiAmount := formatNaira(ch.Amount.String())
	if apiAmount != p.AmountNGN {
		log.Printf("[fw dispatch] charge %s webhook/api amount divergence: webhook=%s api=%s (ref=%q) — crediting api amount (source of truth)", p.ProviderChargeID, p.AmountNGN, apiAmount, p.AccountRef)
	}
	return apiAmount, nil
}

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
		// Independently verify the charge against the Flutterwave API before any
		// credit — a signed webhook is never trusted on its own. Fail-closed:
		// with no verifier configured, refuse to settle.
		if s.fwCharges == nil {
			log.Printf("[fw dispatch] ALARM charge verification unavailable — refusing to settle ref=%q event=%s", p.AccountRef, webhookID)
			return fmt.Errorf("flutterwave charge verification unavailable (ref=%q)", p.AccountRef)
		}
		verifiedAmountNGN, err := verifyFlutterwaveCharge(ctx, s.fwCharges, p)
		if err != nil {
			// Terminal rejections are alarmed once (they are marked 'rejected' and
			// never re-driven). Pending/transport errors stay quiet — they retry.
			if errors.Is(err, ErrChargeRejected) {
				log.Printf("[fw dispatch] ALARM charge verification REJECTED (terminal) ref=%q event=%s: %v", p.AccountRef, webhookID, err)
			}
			return err
		}
		// Credit the API-authoritative amount, not the webhook's.
		p.AmountNGN = verifiedAmountNGN
		if s.itHooks != nil {
			// Spec 1 standing VA (request refs are ITVA-…; response refs may differ).
			if strings.HasPrefix(p.AccountRef, ITVARefPrefix) {
				return s.itHooks.AutoCreditFromVA(ctx, p.CustomerID, p.AccountRef, p.ProviderChargeID, p.AmountNGN)
			}
			// Legacy per-intent dynamic buy VA (keep for one release).
			if strings.HasPrefix(p.AccountRef, "IT-") {
				return s.itHooks.AutoCreditFromInbound(ctx, p.AccountRef, p.AmountNGN)
			}
			// Standing VA whose response reference does not keep the ITVA- prefix —
			// resolve by Flutterwave customer id; unknown customers fall through to PM.
			if p.CustomerID != "" {
				err := s.itHooks.AutoCreditFromVA(ctx, p.CustomerID, p.AccountRef, p.ProviderChargeID, p.AmountNGN)
				if err == nil || !errors.Is(err, ErrITVANotFound) {
					return err
				}
			}
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
