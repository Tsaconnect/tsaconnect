package services

// Role-aware email content for P2P trade lifecycle milestones. P2P trades
// already deliver via in-app + WebSocket + Expo push; email is the channel that
// reaches a user who has closed the app mid-trade, where the steps are
// time-sensitive (a seller waiting on "buyer marked paid", a buyer waiting on
// release). This composer is PURE (no DB/IO) so the per-event/per-role wording
// is unit-testable; the notification service loads the trade and sends.

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// p2pTradeEmailEvents is the curated set of trade-lifecycle events that ALSO
// send an email. Key milestones only: the transient 'releasing' step and
// per-message chat (p2p.message.new) are intentionally excluded so the inbox
// isn't flooded — push + in-app still fire for those. (Scope chosen with the
// product owner: key milestones, no per-message chat email.)
var p2pTradeEmailEvents = map[string]bool{
	events.P2PTradeOpened:    true,
	events.P2PTradePaid:      true,
	events.P2PTradeReleased:  true,
	events.P2PTradeCancelled: true,
	events.P2PTradeDisputed:  true,
	events.P2PTradeResolved:  true,
	events.P2PTradeExpired:   true,
}

type p2pEmailContent struct {
	Subject string
	Body    string
}

// p2pTokenDecimalsForChain mirrors the per-(chain) decimals used across the
// stack: BSC stablecoins are 18-decimal, Sonic (and everything else we trade)
// is 6. Kept in sync with the app's p2pTokenDecimals / the trade service's
// tokenDecimalsFor.
func p2pTokenDecimalsForChain(chainID int64) int {
	switch chainID {
	case 56, 97: // BSC mainnet / testnet
		return 18
	default: // Sonic (146 / 14601) and others
		return 6
	}
}

// humanizeTokenAmount renders a base-unit integer string (e.g. "25000000000000000000")
// as a trimmed decimal (e.g. "25"). Falls back to the raw input if unparseable.
func humanizeTokenAmount(base string, decimals int) string {
	n, ok := new(big.Int).SetString(strings.TrimSpace(base), 10)
	if !ok || decimals < 0 {
		return base
	}
	denom := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	q := new(big.Int)
	r := new(big.Int)
	q.QuoRem(n, denom, r)
	if r.Sign() == 0 || decimals == 0 {
		return q.String()
	}
	frac := fmt.Sprintf("%0*s", decimals, r.String())
	frac = strings.TrimRight(frac, "0")
	if frac == "" {
		return q.String()
	}
	return q.String() + "." + frac
}

func shortTradeRef(id uuid.UUID) string {
	s := id.String()
	if len(s) >= 8 {
		return s[:8]
	}
	return s
}

// buildP2PTradeEmail composes the email for a P2P trade milestone event,
// tailored to whether the recipient (event.UserID) is the trade's buyer or
// seller. Returns (content, true) when an email should be sent; (zero, false)
// when the event isn't an email milestone or the recipient isn't a party.
//
// No CTA button: the app is mobile-only with no universal/app-link setup, so a
// link in an email can't reliably open the trade. The body tells the user to
// open the app instead.
func buildP2PTradeEmail(event events.Event, trade *models.P2PTrade) (p2pEmailContent, bool) {
	if trade == nil || !p2pTradeEmailEvents[event.Type] {
		return p2pEmailContent{}, false
	}

	var role string
	switch event.UserID {
	case trade.BuyerID:
		role = "buyer"
	case trade.SellerID:
		role = "seller"
	default:
		// Not a party to this trade — don't email (shouldn't happen).
		return p2pEmailContent{}, false
	}

	// NOTE: EmailService.Send now renders the body through html/template, so it
	// escapes everything interpolated below. Do NOT escape by hand on the way
	// in — that double-escapes, and an "&" in a name arrives as "&amp;".
	//
	// This comment used to say the opposite, and said so correctly until the
	// template migration. It is kept rather than deleted because the old advice
	// is the intuitive one and someone will reach for it again.
	//
	// The values here are still constrained, non-free-text fields — token
	// symbol, fiat currency, numeric amounts and a truncated UUID — but that is
	// no longer what makes them safe.
	amt := humanizeTokenAmount(trade.TokenAmount, p2pTokenDecimalsForChain(trade.ChainID))
	tok := trade.Token
	fiat := trade.FiatAmount
	ccy := trade.FiatCurrency
	deal := fmt.Sprintf("%s %s (%s %s)", amt, tok, fiat, ccy)

	var subject, body string
	switch event.Type {
	case events.P2PTradeOpened:
		if role == "buyer" {
			subject = fmt.Sprintf("Action needed: pay for your %s trade", tok)
			body = fmt.Sprintf("Your trade to buy %s is open. Send %s %s to the seller's payment method within 30 minutes, then mark it paid in the app.", deal, fiat, ccy)
		} else {
			subject = "A buyer opened a trade on your ad"
			body = fmt.Sprintf("A buyer opened a trade for %s. They have 30 minutes to pay; you'll be alerted to confirm payment and release.", deal)
		}
	case events.P2PTradePaid:
		if role == "seller" {
			subject = "Payment marked sent — confirm & release"
			body = fmt.Sprintf("The buyer marked %s %s as paid for your %s trade. Check that the funds actually arrived in your account, and only release the crypto once you've confirmed receipt.", fiat, ccy, deal)
		} else {
			subject = "Payment marked — awaiting seller release"
			body = fmt.Sprintf("You marked %s %s as paid for your %s trade. The seller will verify and release your crypto; we'll email you when it's released.", fiat, ccy, deal)
		}
	case events.P2PTradeReleased:
		if role == "buyer" {
			subject = fmt.Sprintf("Received: %s %s", amt, tok)
			body = fmt.Sprintf("Your %s has been released to your wallet. The trade is complete.", deal)
		} else {
			subject = fmt.Sprintf("Trade complete — %s %s released", amt, tok)
			body = fmt.Sprintf("You released %s. The trade is complete.", deal)
		}
	case events.P2PTradeCancelled:
		subject = "Trade cancelled"
		if role == "seller" {
			body = fmt.Sprintf("Your %s trade was cancelled and the crypto returned to you.", deal)
		} else {
			body = fmt.Sprintf("Your %s trade was cancelled.", deal)
		}
	case events.P2PTradeDisputed:
		// P2PTradeDisputed fires three times: a prepare-time nudge to the raiser
		// ("sign to submit", BEFORE the dispute is on-chain) plus the two
		// on-chain confirm emits. Only the prepare emit carries a "role" marker
		// (p2p_trade_service PrepareRaiseDispute) — skip it so we email once the
		// dispute is actually open, not when it's merely drafted (and so the
		// raiser doesn't get a premature duplicate). Confirm emits have no
		// "role" key and proceed.
		if _, isPrepare := event.Data["role"]; isPrepare {
			return p2pEmailContent{}, false
		}
		subject = "Dispute opened on your trade"
		body = fmt.Sprintf("A dispute is now open on your %s trade. The funds are frozen pending admin review — open the app to add evidence and track the case.", deal)
	case events.P2PTradeResolved:
		subject = "Dispute resolved"
		body = fmt.Sprintf("The dispute on your %s trade has been resolved by an admin. Open the app to see the outcome.", deal)
	case events.P2PTradeExpired:
		subject = "Your P2P trade expired"
		body = fmt.Sprintf("Your %s trade expired because payment wasn't completed in time.", deal)
	default:
		return p2pEmailContent{}, false
	}

	body += fmt.Sprintf(" Open the TSA Connect app to view it. (Trade reference: %s)", shortTradeRef(trade.ID))
	return p2pEmailContent{Subject: subject, Body: body}, true
}
