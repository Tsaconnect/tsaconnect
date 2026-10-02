package services

import (
	"errors"
	"log"
	"math/big"
	"strings"
)

// PriceGuardMode is how CreateBuy/CreateSell treat a bound rate that is
// worse than the limit the ramp router set from the user's quote.
type PriceGuardMode string

const (
	// PriceGuardOff — no check. What /api/paycrest/* sends (the zero value).
	PriceGuardOff PriceGuardMode = ""
	// PriceGuardLog — record ramp_price_guard_would_refuse and carry on.
	// The rollout default: the quote prices off Paycrest's book while the
	// order binds through /v2/rates, and a week of these lines tells us how
	// often the two disagree by more than the tolerance before anyone
	// refuses a real order over it.
	PriceGuardLog PriceGuardMode = "log"
	// PriceGuardEnforce — refuse with ErrPriceMovedPastQuote, before the
	// provider is asked to open anything.
	PriceGuardEnforce PriceGuardMode = "enforce"
)

// ErrPriceMovedPastQuote: the bound rate is worse for the user than the
// limit allows. Nothing was sent to the provider.
var ErrPriceMovedPastQuote = errors.New("the price moved since your quote")

// checkPriceGuard compares a bound fiat-per-token rate with the limit. On a
// buy she pays naira per token, so bound must not exceed limit; on a sell she
// receives naira per token, so bound must not fall below it. Exact decimal
// arithmetic — never float. An empty limit or PriceGuardOff skips the check.
func checkPriceGuard(side, bound, limit string, mode PriceGuardMode) error {
	if mode == PriceGuardOff || strings.TrimSpace(limit) == "" {
		return nil
	}
	b, okB := new(big.Rat).SetString(strings.TrimSpace(bound))
	l, okL := new(big.Rat).SetString(strings.TrimSpace(limit))
	worse := !okB || !okL ||
		(side == "buy" && b.Cmp(l) > 0) ||
		(side == "sell" && b.Cmp(l) < 0)
	if !worse {
		return nil
	}
	log.Printf("[ramp] METRIC ramp_price_guard_would_refuse side=%s bound=%q limit=%q mode=%s", side, bound, limit, mode)
	if mode == PriceGuardEnforce {
		return ErrPriceMovedPastQuote
	}
	return nil
}
