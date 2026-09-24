package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// ─── Sell that pays an exact naira amount ──────────────────────────────────

var (
	// ErrSellShortfall: the USDC available cannot fund the stated naira. The
	// typed SellShortfallError says by how much.
	ErrSellShortfall = errors.New("not enough USDC to pay the full amount")
	// ErrSellRateMoved: the provider's order rate came back worse than the
	// venue the deposit was sized at, so the naira would have been short. The
	// order is cancelled and no deposit address is ever returned for it.
	ErrSellRateMoved = errors.New("the rate moved while opening the order — try again")
)

// ErrSellAttemptMayExist is CreateSellForFiat's answer when it could not prove
// that no fundable payout order was left behind: a short order it failed to
// cancel, or an order Paycrest opened that could not be recorded. A caller
// pairing it with a payment must never hand the payment back.
var ErrSellAttemptMayExist = errors.New("a payout order may still exist for this attempt")

// SellShortfallError is ErrSellShortfall with the missing amount in the
// token's base units, so a caller can say how much more is needed.
type SellShortfallError struct{ MissingBaseUnits *big.Int }

func (e *SellShortfallError) Error() string { return ErrSellShortfall.Error() }
func (e *SellShortfallError) Unwrap() error { return ErrSellShortfall }

// sellPayoutTolerance is how many naira a provider's order rate may come in
// under the venue the deposit was sized at before the order is refused: the
// two quotes are asked moments apart, and a kobo of drift is not a shortfall.
//
// It is paid for up front, never taken out of the merchant's naira: the deposit
// is sized for the amount owed PLUS this, and the order must then pay the full
// amount owed. The customer puts in about ₦1 more; the merchant is never short.
var sellPayoutTolerance = big.NewRat(1, 1)

// sellFeeRounding is what Paycrest's rounding of its sender fee (to 4 decimal
// places of the token) can take off the deposit before it is paid out: at most
// 0.0001 token. Doubled, so the allowance still holds after the fee's own share
// of the rate. Added to every deposit and taken off every token amount judged.
var sellFeeRounding = big.NewRat(2, 10_000)

// The stored and quoted rates are the provider's rate × (1 − fee) ROUNDED to
// the cent (quotedRate), while Paycrest pays at the unrounded figure. The
// rounding is at most half a cent either way, so the rate Paycrest really pays
// is never below the stated one by more than sellRateRounding.
var sellRateRounding = big.NewRat(1, 200) // ₦0.005 per USD

// sellRateHeadroom is taken off the venue's rate before a deposit is sized.
// It must exceed sellRateRounding, and does: sized at (rate − ₦0.01), the
// deposit pays at least the naira owed at ANY rate the provider can round to
// the quoted one, since the true rate T ≥ rate − 0.005 > rate − 0.01, so
// deposit × T ≥ deposit × (rate − 0.01) ≥ fiat. That holds for every amount —
// the shortfall it closes grows with the payment (≈ ₦17 at ₦5M) but the
// headroom is per-USD, not per-payment. What it costs the customer is about
// fiat × 0.01 / rate² USDC (≈ 0.02 USDC, ₦33, on ₦5M): the merchant is never
// the one who is short.
var sellRateHeadroom = big.NewRat(1, 100)

// SellDepositFor is the deposit, in the token's base units, that a sell paying
// exactly fiatAmount needs at the sender fee for converted (F − B, or F). It is
// the same VenueForFee call CreateSellForFiat sizes with; nothing is created.
//
// It is sized at the venue's rate less sellRateHeadroom and rounded UP to the
// base unit, so the payout at Paycrest's unrounded rate covers the naira — see
// sellRateHeadroom for the bound. Not venue.TokenAmountBaseUnits: that floors
// at the rounded rate.
func (s *PaycrestOrderService) SellDepositFor(ctx context.Context, fiatCode, fiatAmount, chainKey, token string, converted bool) (*big.Int, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	feeVal, ok := parseSenderFeePercent(s.senderFeeFor(converted))
	if !ok {
		return nil, ErrConversionUnavailable
	}
	fiat := strings.ToUpper(strings.TrimSpace(fiatCode))
	if fiat == "" {
		fiat = "NGN"
	}
	if err := s.requirePaycrestEnabled(fiat); err != nil {
		return nil, err
	}
	venue, err := s.quotes.VenueForFee(ctx, "sell", fiat, fiatAmount, chainKey, token, feeVal)
	if err != nil {
		return nil, s.explainMissingVenue(ctx, "sell", fiat, err)
	}
	fiatWanted, ok1 := new(big.Rat).SetString(strings.TrimSpace(fiatAmount))
	rate, ok2 := new(big.Rat).SetString(strings.TrimSpace(venue.Rate))
	if !ok1 || !ok2 || fiatWanted.Sign() <= 0 {
		return nil, fmt.Errorf("paycrest: unusable sell rate %q", venue.Rate)
	}
	sizing := new(big.Rat).Sub(rate, sellRateHeadroom)
	if sizing.Sign() <= 0 {
		return nil, fmt.Errorf("paycrest: unusable sell rate %q", venue.Rate)
	}
	// Sized for the naira owed plus the drift tolerance, then the fee rounding on
	// top: sellPayoutCovers accepts only an order that pays the whole amount.
	baseUnits := new(big.Rat).Quo(new(big.Rat).Add(fiatWanted, sellPayoutTolerance), sizing)
	baseUnits.Add(baseUnits, sellFeeRounding)
	baseUnits.Mul(baseUnits, new(big.Rat).SetInt(pow10(s.sellTokenDecimals(chainKey, token))))
	dep, rem := new(big.Int).QuoRem(baseUnits.Num(), baseUnits.Denom(), new(big.Int))
	if rem.Sign() > 0 {
		dep.Add(dep, big.NewInt(1))
	}
	if dep.Sign() <= 0 {
		return nil, fmt.Errorf("paycrest: cannot size a %s deposit for %s %s", token, fiat, fiatAmount)
	}
	return dep, nil
}

func (s *PaycrestOrderService) sellTokenDecimals(chainKey, token string) int {
	return tokenDecimalsFor(s.quotes.chainID(chainKey), token)
}

// CreateSellForFiat opens an offramp that pays req.FiatAmount EXACTLY, funded
// by at most maxTokenBaseUnits of token (nil = no cap). It is the mirror of
// CreateSellForToken: there the tokens are the fact and the naira follows; here
// the naira is the fact and the deposit follows.
//
// A cap the deposit exceeds is a SellShortfallError before Paycrest hears of
// it. An order whose payout comes back short of the naira — the provider's rate
// moved between sizing and opening — is cancelled and refused: a deposit
// address is never handed out for a sale that would underpay.
func (s *PaycrestOrderService) CreateSellForFiat(ctx context.Context, req PaycrestSellRequest, converted bool, maxTokenBaseUnits *big.Int) (*models.PaycrestOrderRecord, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	dep, err := s.SellDepositFor(ctx, req.FiatCode, req.FiatAmount, req.ChainKey, req.Token, converted)
	if err != nil {
		return nil, err
	}
	if maxTokenBaseUnits != nil && dep.Cmp(maxTokenBaseUnits) > 0 {
		return nil, &SellShortfallError{MissingBaseUnits: new(big.Int).Sub(dep, maxTokenBaseUnits)}
	}
	row, err := s.CreateSellForToken(ctx, req, BaseUnitsToDecimal(dep, s.sellTokenDecimals(req.ChainKey, req.Token)), converted)
	if err != nil {
		return nil, err
	}
	if !sellPayoutCovers(row, req.FiatAmount) {
		if err := s.cancelShortOrder(ctx, row, req); err != nil {
			// Still awaiting a deposit at an address she can see. Both answers
			// hold: the rate did move, AND an order may be open.
			return nil, fmt.Errorf("%w: %w", ErrSellAttemptMayExist, ErrSellRateMoved)
		}
		return nil, ErrSellRateMoved
	}
	return row, nil
}

// sellCancelTimeout bounds the write that takes a short order off the table
// once the request that opened it can no longer be relied on.
const sellCancelTimeout = 5 * time.Second

// cancelShortOrder cancels an order that would underpay and withdraws its
// deposit address in the same write, so nothing in her order list can be
// funded. It runs even if the request was cancelled or timed out — a dead
// request must not leave a short order fundable — and it is DB-only, exactly
// like Cancel: Paycrest has no cancel endpoint, so their copy lapses at its
// own validUntil, and any deposit sent to it refunds to her wallet.
//
// It returns the write's failure: an order that could not be cancelled is still
// fundable, and CreateSellForFiat's caller has to know.
func (s *PaycrestOrderService) cancelShortOrder(ctx context.Context, row *models.PaycrestOrderRecord, req PaycrestSellRequest) error {
	cctx, stop := context.WithTimeout(context.WithoutCancel(ctx), sellCancelTimeout)
	defer stop()
	_, err := s.cancelOrder(cctx, req.UserID, row.ID, map[string]any{
		"failure_reason":  "payout would be short of the amount owed",
		"deposit_address": "",
	})
	if err != nil {
		log.Printf("[paycrest] ALARM order %s pays short of %s %s and could not be cancelled: %v",
			row.ID, req.FiatCode, req.FiatAmount, err)
	}
	return err
}

// sellPayoutCovers reports whether the order's own token amount pays the naira
// owed in full. The row's rate is rounded to the cent, so it is judged at the
// worst rate that rounds to it (less sellRateRounding), and on the tokens left
// after the worst fee rounding (less sellFeeRounding): a payout that only
// clears at the rounded figures is not proven. The drift tolerance is not
// subtracted here — SellDepositFor already paid for it.
func sellPayoutCovers(row *models.PaycrestOrderRecord, fiatAmount string) bool {
	tokens, ok1 := new(big.Rat).SetString(strings.TrimSpace(row.TokenAmount))
	rate, ok2 := new(big.Rat).SetString(strings.TrimSpace(row.Rate))
	owed, ok3 := new(big.Rat).SetString(strings.TrimSpace(fiatAmount))
	if !ok1 || !ok2 || !ok3 {
		return false
	}
	worst := new(big.Rat).Sub(rate, sellRateRounding)
	paid := new(big.Rat).Sub(tokens, sellFeeRounding)
	if paid.Sign() <= 0 {
		return false
	}
	payout, _ := new(big.Rat).SetString(fiatAt(paid, worst))
	return payout.Cmp(owed) >= 0
}
