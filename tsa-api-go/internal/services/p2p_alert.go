package services

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

type P2PAlertKind string

const (
	P2PAlertPaidTradeCancelled P2PAlertKind = "paid_trade_cancelled"
	P2PAlertDisputeFiled       P2PAlertKind = "dispute_filed"
	P2PAlertPaidUnreleased     P2PAlertKind = "paid_unreleased"
	// P2PAlertExpiredWithProof: the expiry canceller did not refund an expired
	// direct trade whose buyer saved payment proof without marking paid on-chain.
	P2PAlertExpiredWithProof P2PAlertKind = "expired_with_proof"
)

const p2pAlertReasonMax = 300

// P2PAlert is a P2P money event an operator must look at. Amount is a human
// decimal ("25.5"), not base units.
type P2PAlert struct {
	Kind                     P2PAlertKind
	TradeID                  uuid.UUID
	Token, Amount            string
	FiatAmount, FiatCurrency string
	FromStatus               string
	MarkedPaidAt             *time.Time
	RaisedRole, Reason       string        // dispute_filed
	Age                      time.Duration // paid_unreleased
}

type P2PAlerter interface {
	NotifyP2P(ctx context.Context, a P2PAlert) error
}

// NotifyP2P emails the INBOUND_ALERT_EMAILS operators about a P2P money event.
// Same contract as NotifyStockLow: never errors, never blocks, no-op when
// unconfigured.
func (n *InboundHoldNotifier) NotifyP2P(_ context.Context, a P2PAlert) error {
	if n == nil || n.mailer == nil || len(n.to) == 0 {
		return nil
	}
	size := strings.TrimSpace(a.Amount + " " + a.Token)
	var subject, title, todo string
	switch a.Kind {
	case P2PAlertPaidTradeCancelled:
		subject = "P2P paid trade cancelled on-chain — " + size
		title = "A paid P2P trade was cancelled"
		todo = "The buyer had marked this trade paid and it was cancelled on-chain anyway. Check whether the fiat actually moved and refund or re-open as needed."
	case P2PAlertDisputeFiled:
		subject = "P2P dispute filed — " + size
		title = "A P2P dispute needs review"
		todo = "Review the trade, payment proof and chat, then resolve the dispute in the console."
	case P2PAlertPaidUnreleased:
		subject = fmt.Sprintf("P2P trade paid %s without release — %s", a.Age, size)
		title = "A paid P2P trade has not been released"
		todo = "The seller has not released the crypto. Check the trade and chase the seller, or let the buyer dispute."
	case P2PAlertExpiredWithProof:
		subject = "P2P trade expired with payment proof — " + size
		title = "An expired P2P trade has payment proof but no on-chain mark-paid"
		todo = "The buyer saved payment proof but her on-chain mark-paid never landed, so it was not auto-cancelled. Check the proof and chat: the buyer can still mark paid or dispute; either party can cancel by timeout."
	default:
		subject = "P2P trade alert — " + size
		title = "P2P trade alert"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Trade: %s\n", a.TradeID)
	fmt.Fprintf(&b, "Amount: %s\n", size)
	fmt.Fprintf(&b, "Fiat: %s %s\n", a.FiatAmount, a.FiatCurrency)
	fmt.Fprintf(&b, "Status when alerted: %s\n", a.FromStatus)
	if a.MarkedPaidAt != nil {
		fmt.Fprintf(&b, "Marked paid: %s UTC\n", a.MarkedPaidAt.UTC().Format("2006-01-02 15:04:05"))
	}
	if a.RaisedRole != "" {
		fmt.Fprintf(&b, "Raised by: %s\n", a.RaisedRole)
	}
	if a.Reason != "" {
		fmt.Fprintf(&b, "Reason: %s\n", capRunes(RedactAlertText(a.Reason), p2pAlertReasonMax))
	}
	if a.Age > 0 {
		fmt.Fprintf(&b, "Paid for: %s\n", a.Age)
	}
	if todo != "" {
		b.WriteString("\n" + todo)
	}

	ctaText, ctaURL := "", ""
	if n.consoleBaseURL != "" {
		switch a.Kind {
		case P2PAlertDisputeFiled:
			ctaText, ctaURL = "Open the dispute", n.consoleBaseURL+"/p2p-disputes/"+a.TradeID.String()
		case P2PAlertPaidUnreleased, P2PAlertExpiredWithProof:
			ctaText, ctaURL = "Open the trade", n.consoleBaseURL+"/p2p-trades/"+a.TradeID.String()
		}
	}
	n.enqueue(alertJob{
		kind: "p2p " + string(a.Kind), ref: a.TradeID.String(),
		subject: subject, title: title, message: b.String(), ctaText: ctaText, ctaURL: ctaURL,
	})
	return nil
}

func capRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// SetAlerter wires the operator alert channel. Nil-safe: unset means log-only.
func (s *P2PTradeService) SetAlerter(a P2PAlerter) { s.alerter = a }

// alert logs a greppable counter line and, when wired, emails operators. It
// never fails the transition that raised it.
func (s *P2PTradeService) alert(ctx context.Context, kind P2PAlertKind, t *models.P2PTrade, extra func(*P2PAlert)) {
	a := P2PAlert{Kind: kind, TradeID: t.ID, Token: t.Token, FiatAmount: t.FiatAmount, FiatCurrency: t.FiatCurrency,
		FromStatus: t.Status, MarkedPaidAt: t.MarkedPaidAt}
	if amt, ok := new(big.Int).SetString(t.TokenAmount, 10); ok {
		a.Amount = BaseUnitsToDecimal(amt, s.TokenDecimals(t.ChainID, t.Token))
	}
	if extra != nil {
		extra(&a)
	}
	log.Printf("[p2p] ALERT p2p_%s_total=1 trade=%s status=%s", kind, t.ID, a.FromStatus)
	if s.alerter == nil {
		return
	}
	_ = s.alerter.NotifyP2P(ctx, a)
}

// SellerPayoutAlert is a seller payout parked for a person.
type SellerPayoutAlert struct {
	PayoutID, OrderID uuid.UUID
	// From is the status it was in when it stopped; RetrySafe says an admin may
	// let it run again once the cause is fixed.
	From, Reason string
	RetrySafe    bool
}

// SellerPayoutAlerter tells operators a payout needs a person.
type SellerPayoutAlerter interface {
	NotifySellerPayoutReview(ctx context.Context, a SellerPayoutAlert)
}

// NotifySellerPayoutReview emails the INBOUND_ALERT_EMAILS operators. Same
// contract as NotifyP2P: never errors, never blocks, a no-op when unconfigured.
func (n *InboundHoldNotifier) NotifySellerPayoutReview(_ context.Context, a SellerPayoutAlert) {
	if n == nil || n.mailer == nil || len(n.to) == 0 {
		return
	}
	reason := RedactAlertText(a.Reason)
	if len(reason) > p2pAlertReasonMax {
		reason = reason[:p2pAlertReasonMax]
	}
	body := fmt.Sprintf("Payout: %s\nOrder: %s\nStopped in: %s\nReason: %s\nSafe to retry: %v\n\n"+
		"Read the chain and the Paycrest order, then use the console (Seller payouts) or the runbook. "+
		"Nothing was sent to the seller's wallet.", a.PayoutID, a.OrderID, a.From, reason, a.RetrySafe)
	for _, to := range n.to {
		if err := n.mailer.Send(to, "", "Seller payout needs review", "A seller payout is parked", body, "", ""); err != nil {
			log.Printf("[seller-payout] review alert to %s failed: %v", to, err)
		}
	}
}
