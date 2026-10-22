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
// SellDepositFor only: a KeepSurplus direct payout has no cushion, and our fee
// absorbs the drift instead (payFeeAbsorbLimit).
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

// assertSellRateHeadroomSound panics if sellRateHeadroom no longer exceeds
// sellRateRounding — the margin SellDepositFor's doc comment (above) derives
// the merchant-never-short proof from. Neither is a Go const (big.Rat can't
// be), so nothing else catches a change to either that silently breaks the
// proof; this fails at boot instead of leaving a payout to come in short.
func assertSellRateHeadroomSound(headroom, rounding *big.Rat) {
	if headroom.Cmp(rounding) <= 0 {
		panic(fmt.Sprintf(
			"sellRateHeadroom (%s) must exceed sellRateRounding (%s): SellDepositFor's merchant-never-short proof depends on it",
			headroom.RatString(), rounding.RatString()))
	}
}

func init() {
	assertSellRateHeadroomSound(sellRateHeadroom, sellRateRounding)
}

// SellDepositFor is the deposit, in the token's base units, that a TSA Pay
// payout paying exactly fiatAmount needs at the payment's own sender fee
// (PayFeeSplit: F − B converted, F direct), with the cushion below on top. It
// is the same VenueForFee call CreateSellForFiat sizes with; nothing is
// created. It sizes every payout EXCEPT a KeepSurplus direct one, whose
// deposit is the invoice plus the fee with no cushion — keepSurplusDeposit.
//
// It is sized at the venue's rate less sellRateHeadroom and rounded UP to the
// base unit, so the payout at Paycrest's unrounded rate covers the naira — see
// sellRateHeadroom for the bound. Not venue.TokenAmountBaseUnits: that floors
// at the rounded rate.
func (s *PaycrestOrderService) SellDepositFor(ctx context.Context, fiatCode, fiatAmount, chainKey, token string, converted bool) (*big.Int, error) {
	venue, fiatWanted, err := s.payoutVenue(ctx, fiatCode, fiatAmount, chainKey, token, converted)
	if err != nil {
		return nil, err
	}
	fiat := fiatCodeOf(PaycrestSellRequest{FiatCode: fiatCode})
	rate, ok := new(big.Rat).SetString(strings.TrimSpace(venue.Rate))
	if !ok {
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

// payoutVenue binds the venue a TSA Pay payout of fiatAmount is priced at, at
// the payment's own sender fee, and reads the amount owed.
func (s *PaycrestOrderService) payoutVenue(ctx context.Context, fiatCode, fiatAmount, chainKey, token string, converted bool) (*PaycrestVenue, *big.Rat, error) {
	if s == nil {
		return nil, nil, ErrPaycrestDisabled
	}
	feeVal, ok := parseSenderFeePercent(s.pay.SenderFee(converted))
	if !ok {
		return nil, nil, ErrConversionUnavailable
	}
	fiat := fiatCodeOf(PaycrestSellRequest{FiatCode: fiatCode})
	if err := s.requirePaycrestEnabled(fiat); err != nil {
		return nil, nil, err
	}
	venue, err := s.quotes.VenueForFee(ctx, "sell", fiat, fiatAmount, chainKey, token, feeVal)
	if err != nil {
		return nil, nil, s.explainMissingVenue(ctx, "sell", fiat, err)
	}
	owed, ok := new(big.Rat).SetString(strings.TrimSpace(fiatAmount))
	if !ok || owed.Sign() <= 0 {
		return nil, nil, fmt.Errorf("paycrest: unusable payout amount %q", fiatAmount)
	}
	return venue, owed, nil
}

// keepSurplusDeposit is a KeepSurplus payout's deposit, in base units: the
// invoice plus the configured fee at the venue's raw rate, owed / (rate × (1 −
// p)) — the total the customer was shown — rounded up to the base unit. No
// cushion: what rounding and drift need comes out of our fee (keepSurplusFee),
// up to payFeeAbsorbLimit.
func (s *PaycrestOrderService) keepSurplusDeposit(ctx context.Context, fiatCode, fiatAmount, chainKey, token string) (*big.Int, error) {
	venue, owed, err := s.payoutVenue(ctx, fiatCode, fiatAmount, chainKey, token, false)
	if err != nil {
		return nil, err
	}
	rate, ok1 := new(big.Rat).SetString(strings.TrimSpace(venue.RawRate))
	p, ok2 := feeFraction(s.pay.SenderFee(false))
	if !ok1 || !ok2 || rate.Sign() <= 0 || p.Cmp(big.NewRat(1, 1)) >= 0 {
		return nil, fmt.Errorf("paycrest: unusable sell rate %q", venue.RawRate)
	}
	net := new(big.Rat).Mul(rate, new(big.Rat).Sub(big.NewRat(1, 1), p))
	baseUnits := new(big.Rat).Quo(owed, net)
	baseUnits.Mul(baseUnits, new(big.Rat).SetInt(pow10(s.sellTokenDecimals(chainKey, token))))
	dep, rem := new(big.Int).QuoRem(baseUnits.Num(), baseUnits.Denom(), new(big.Int))
	if rem.Sign() > 0 {
		dep.Add(dep, big.NewInt(1))
	}
	return dep, nil
}

func (s *PaycrestOrderService) sellTokenDecimals(chainKey, token string) int {
	return tokenDecimalsFor(s.quotes.chainID(chainKey), token)
}

// CreateSellForFiat opens a TSA Pay payout that pays req.FiatAmount EXACTLY,
// funded by at most maxTokenBaseUnits of token (nil = no cap), at the
// payment's own sender fee (PayFeeSplit), never the ramp's take. It is the
// mirror of CreateSellForToken: there the tokens are the fact and the naira
// follows; here the naira is the fact and the deposit follows.
//
// A cap the deposit exceeds is a SellShortfallError before Paycrest hears of
// it. An order whose payout comes back short of the naira — the provider's rate
// moved between sizing and opening — is cancelled and refused: a deposit
// address is never handed out for a sale that would underpay.
func (s *PaycrestOrderService) CreateSellForFiat(ctx context.Context, req PaycrestSellRequest, converted bool, maxTokenBaseUnits *big.Int) (*models.PaycrestOrderRecord, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	keepSurplus := req.KeepSurplus && !converted
	var dep *big.Int
	var err error
	if keepSurplus {
		dep, err = s.keepSurplusDeposit(ctx, req.FiatCode, req.FiatAmount, req.ChainKey, req.Token)
	} else {
		dep, err = s.SellDepositFor(ctx, req.FiatCode, req.FiatAmount, req.ChainKey, req.Token, converted)
	}
	if err != nil {
		return nil, err
	}
	if maxTokenBaseUnits != nil && dep.Cmp(maxTokenBaseUnits) > 0 {
		return nil, &SellShortfallError{MissingBaseUnits: new(big.Int).Sub(dep, maxTokenBaseUnits)}
	}
	row, err := s.sellForToken(ctx, req, BaseUnitsToDecimal(dep, s.sellTokenDecimals(req.ChainKey, req.Token)),
		s.pay.SenderFee(converted), keepSurplus)
	if err != nil {
		return nil, err
	}
	covers := sellPayoutCovers
	if keepSurplus {
		covers = keepSurplusPayoutCovers
	}
	if !covers(row, req.FiatAmount) {
		// Visible to ops: how often the provider's rate moves between quote and
		// order, and by how much, is only measurable from these.
		log.Printf("[paycrest] sell_payout_refused_total=1 order=%s ref=%s owed=%s %s deposit=%s fee_pct=%s sender_fee=%s paycrest_rate=%s keep_surplus=%v",
			row.ID, row.Reference, req.FiatCode, req.FiatAmount, row.TokenAmount, row.SenderFeePercent, row.SenderFee, row.RawRate, req.KeepSurplus)
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
	_, err := s.cancelOrderAs(cctx, req.UserID, row.ID, map[string]any{
		"failure_reason":  "payout would be short of the amount owed",
		"deposit_address": "",
	}, req.SellerPayoutID != nil)
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
	owed, ok2 := new(big.Rat).SetString(strings.TrimSpace(fiatAmount))
	return ok1 && ok2 && payoutCovers(tokens, row.Rate, owed)
}

// payoutCovers is sellPayoutCovers' judgement on its parts: tokens paid out at
// a fee-priced, cent-rounded rate cover `owed` at the worst rate and fee
// rounding. The one test both the payout check and the per-order fee choice
// use, so a fee chosen at the quoted rate always passes the check at it.
func payoutCovers(tokens *big.Rat, rateNet string, owed *big.Rat) bool {
	rate, ok := new(big.Rat).SetString(strings.TrimSpace(rateNet))
	if !ok {
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

// ─── KeepSurplus: the merchant is paid what they invoiced; our fee absorbs ──
//
// A KeepSurplus payout (a TSA Pay direct payout) is funded with exactly what the
// customer was shown — the invoice plus our fee — and our fee, not a cushion on
// top, absorbs Paycrest's rounding and its rate moving between quote and order,
// up to payFeeAbsorbLimit. Past that the order is refused rather than paid for.
//
// It is judged by Paycrest's own arithmetic. The fee Paycrest takes is inside
// the deposit, deposit × p/(1 + p), but the fee it REPORTS is deposit × p, and
// the payout check trusts the larger, so the fee is chosen at the reported one
// (paycrestReportedSellFee). The difference, ~p² of the payment (≈0.01%), goes
// to the merchant: the price of never opening an order the check then refuses.
// Every rounding is taken against the merchant, and every order is checked
// against Paycrest's own answer.

// payFeeAbsorbLimit is the most our fee gives up on one TSA Pay payout so the
// merchant is paid the invoice exactly: Paycrest's rounding, and its rate moving
// between quote and order. Kobo to a few naira is ours to absorb; a bigger move
// refuses the order ("the rate moved") instead of coming out of the fee.
var payFeeAbsorbLimit = big.NewRat(5, 1) // ₦5

// surplusFeeStepsPerPercent is how finely a KeepSurplus fee is chosen: 0.0001%.
// A whole basis point is too coarse — on ₦50,000 it is ₦5, the whole of
// payFeeAbsorbLimit, so one bps step could spend the window on rounding alone.
// Paycrest takes the percent as a decimal string.
const surplusFeeStepsPerPercent = 10_000

// keepSurplusMaxUpliftSteps caps a KeepSurplus fee at the configured fee plus
// 0.5%. The fee rises above the configured one only when Paycrest prices better
// than the deposit was sized at; a fee that wants to climb past the cap means
// the deposit is far larger than the invoice needs, and the excess is left with
// the merchant — and alerted — rather than silently kept.
const keepSurplusMaxUpliftSteps = 50 * surplusFeeStepsPerPercent / 100

// paycrestFeeScale: Paycrest states a sender fee to 4 token decimals.
var paycrestFeeScale = big.NewInt(10_000)

// paycrestSellFee is the sender fee Paycrest takes from a sell deposit of
// `tokens` at `pct` percent: the fee inside the deposit, tokens × p/(1 + p) —
// the first live order's 0.0073 USDC fee on 0.742226 at 1% is exactly this —
// rounded UP to 4 decimals, against the merchant, whichever way Paycrest rounds.
func paycrestSellFee(tokens *big.Rat, pct string) (*big.Rat, bool) {
	p, ok := feeFraction(pct)
	if !ok {
		return nil, false
	}
	fee := new(big.Rat).Quo(new(big.Rat).Mul(tokens, p), new(big.Rat).Add(big.NewRat(1, 1), p))
	return roundUpToPaycrestFeeScale(fee), true
}

// paycrestReportedSellFee is the sender fee Paycrest REPORTS on a new sell
// order: tokens × p, to 4 decimals rounded up (0.0385 on 3.735942 at 1.0303%,
// live 2026-10-09). It overstates the fee taken (paycrestSellFee) by
// tokens × p²/(1 + p), and keepSurplusPayoutCovers trusts the larger, so a
// KeepSurplus fee is chosen against this one. Chosen against the smaller, the
// overstatement alone — about ₦0.13 per token at 1% and ₦1,350 — outran the
// drift margin past ~₦3k and refused every such order as "the rate moved" at
// an unmoved rate.
func paycrestReportedSellFee(tokens *big.Rat, pct string) (*big.Rat, bool) {
	p, ok := feeFraction(pct)
	if !ok {
		return nil, false
	}
	return roundUpToPaycrestFeeScale(new(big.Rat).Mul(tokens, p)), true
}

// feeFraction is a sender fee percent as a fraction: "1.5" → 0.015.
func feeFraction(pct string) (*big.Rat, bool) {
	p, ok := new(big.Rat).SetString(strings.TrimSpace(pct))
	if !ok || p.Sign() < 0 {
		return nil, false
	}
	return p.Quo(p, big.NewRat(100, 1)), true
}

// roundUpToPaycrestFeeScale states a fee to 4 token decimals, rounded up —
// against the merchant.
func roundUpToPaycrestFeeScale(fee *big.Rat) *big.Rat {
	scaled := new(big.Rat).Mul(fee, new(big.Rat).SetInt(paycrestFeeScale))
	units, rem := new(big.Int).QuoRem(scaled.Num(), scaled.Denom(), new(big.Int))
	if rem.Sign() > 0 {
		units.Add(units, big.NewInt(1))
	}
	return new(big.Rat).SetFrac(units, paycrestFeeScale)
}

// paycrestSellPayout is the naira Paycrest pays for `tokens` less `fee` at its
// raw rate, rounded DOWN to the kobo — against the merchant.
func paycrestSellPayout(tokens, fee, rawRate *big.Rat) *big.Rat {
	net := new(big.Rat).Sub(tokens, fee)
	if net.Sign() <= 0 || rawRate.Sign() <= 0 {
		return new(big.Rat)
	}
	payout := new(big.Rat).Mul(net, rawRate)
	kobo := new(big.Int).Quo(new(big.Int).Mul(payout.Num(), big.NewInt(100)), payout.Denom())
	return new(big.Rat).SetFrac(kobo, big.NewInt(100))
}

// keepSurplusFee is the sender fee for a KeepSurplus order: the highest
// 0.0001% step at which `tokens` at `rawRate` still pay `owed`, by Paycrest's
// arithmetic taken against the merchant — at the fee Paycrest will REPORT, the
// larger one the payout check trusts.
//
// It may fall below the configured fee `base`, but only as far as our fee gives
// up payFeeAbsorbLimit (keepSurplusFeeFloor); `short` says even that floor does
// not pay `owed`, and the floor is returned. It never rises above base +
// keepSurplusMaxUpliftSteps; `capped` says that cap stopped it. With no
// readable rate, `base` is returned and the payout check decides.
func keepSurplusFee(tokens *big.Rat, rawRate, base string, owed *big.Rat) (fee string, capped, short bool) {
	baseSteps, ok := feePercentToSteps(base, surplusFeeStepsPerPercent)
	rate, rateOK := new(big.Rat).SetString(strings.TrimSpace(rawRate))
	if !ok || !rateOK || rate.Sign() <= 0 {
		return base, false, false
	}
	clears := func(steps int) bool {
		f, ok := paycrestReportedSellFee(tokens, stepsToFeePercent(steps, surplusFeeStepsPerPercent))
		return ok && paycrestSellPayout(tokens, f, rate).Cmp(owed) >= 0
	}
	floor := keepSurplusFeeFloor(tokens, baseSteps, rate)
	if !clears(floor) {
		return stepsToFeePercent(floor, surplusFeeStepsPerPercent), false, true
	}
	top := min(baseSteps+keepSurplusMaxUpliftSteps, maxSenderFeePercent*surplusFeeStepsPerPercent)
	// Payout falls as the fee rises, so the highest clearing fee is a boundary.
	lo, hi := floor, top
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if clears(mid) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return stepsToFeePercent(lo, surplusFeeStepsPerPercent), lo == top && top > baseSteps, false
}

// keepSurplusFeeFloor is the lowest fee step at which what Paycrest takes from
// `tokens` is at most payFeeAbsorbLimit (at `rate`) under what it takes at the
// configured `baseSteps`.
func keepSurplusFeeFloor(tokens *big.Rat, baseSteps int, rate *big.Rat) int {
	baseFee, _ := paycrestSellFee(tokens, stepsToFeePercent(baseSteps, surplusFeeStepsPerPercent))
	withinWindow := func(steps int) bool {
		f, ok := paycrestSellFee(tokens, stepsToFeePercent(steps, surplusFeeStepsPerPercent))
		gaveUp := new(big.Rat).Mul(new(big.Rat).Sub(baseFee, f), rate)
		return ok && gaveUp.Cmp(payFeeAbsorbLimit) <= 0
	}
	// What is given up falls as the fee rises, so the lowest step within the
	// window is a boundary.
	lo, hi := 0, baseSteps
	for lo < hi {
		mid := (lo + hi) / 2
		if withinWindow(mid) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}

// openKeepSurplusOfframp opens a KeepSurplus order at the fee that leaves the
// merchant exactly what they invoiced at the quoted rate (see keepSurplusFee).
// Paycrest takes the finely-stepped percent as sent (fee_charged == fee_sent
// on every live order), so a refusal is not retried at another fee.
//
// Paycrest fills at its own rate, which can come back under the quote. A short
// order is then cancelled and reopened ONCE at the fee priced at Paycrest's own
// rate — the move comes out of our fee — provided that fee stays inside
// payFeeAbsorbLimit. Otherwise it is ErrSellRateMoved, and CreateSellForFiat
// still judges whatever order this returns. Every choice is logged by the
// order's reference.
func (s *PaycrestOrderService) openKeepSurplusOfframp(ctx context.Context, req PaycrestSellRequest, venue *PaycrestVenue,
	tokens *big.Rat, tokenAmount, fiatAmount, payoutInst, base string, owed *big.Rat) (*models.PaycrestOrderRecord, error) {
	fee, capped, short := keepSurplusFee(tokens, venue.RawRate, base, owed)
	if short {
		// The re-quote just before opening is already past the window: nothing
		// has been sent to Paycrest.
		log.Printf("[paycrest] keep_surplus_refused_total=1 owed=%s deposit=%s quoted_rate=%s: the fee cannot absorb the move",
			fiatAmount, tokenAmount, venue.RawRate)
		return nil, nothingSent(ErrSellRateMoved)
	}
	if capped {
		log.Printf("[paycrest] ALERT keep_surplus_fee_capped_total=1 owed=%s deposit=%s quoted_rate=%s fee=%s%%: the deposit is larger than the invoice needs; the excess goes to the merchant",
			fiatAmount, tokenAmount, venue.RawRate, fee)
	}
	row, err := s.openOfframp(ctx, req, venue, tokenAmount, fiatAmount, payoutInst, fee)
	if err != nil {
		return nil, err
	}
	log.Printf("[paycrest] keep_surplus ref=%s owed=%s deposit=%s quoted_rate=%s paycrest_rate=%s fee_sent=%s%% fee_charged=%s%% base=%s%%",
		row.Reference, fiatAmount, tokenAmount, venue.RawRate, row.RawRate, fee, row.SenderFeePercent, base)
	if keepSurplusPayoutCovers(row, fiatAmount) {
		return row, nil
	}
	refee, _, short := keepSurplusFee(tokens, row.RawRate, base, owed)
	if short || !feeStepBelow(refee, row.SenderFeePercent) {
		// Past the window, or no lower fee would help (an unreadable rate, or a
		// shortfall that is not the rate at all): CreateSellForFiat refuses it.
		return row, nil
	}
	if err := s.cancelShortOrder(ctx, row, req); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSellAttemptMayExist, ErrSellRateMoved)
	}
	log.Printf("[paycrest] keep_surplus_reopened_total=1 ref=%s owed=%s quoted_rate=%s paycrest_rate=%s fee=%s%% → %s%%",
		row.Reference, fiatAmount, venue.RawRate, row.RawRate, row.SenderFeePercent, refee)
	return s.openOfframp(ctx, req, venue, tokenAmount, fiatAmount, payoutInst, refee)
}

// feeStepBelow reports whether sender-fee percent a is at least one
// KeepSurplus step below b.
func feeStepBelow(a, b string) bool {
	x, ok1 := feePercentToSteps(a, surplusFeeStepsPerPercent)
	y, ok2 := feePercentToSteps(b, surplusFeeStepsPerPercent)
	return ok1 && ok2 && x < y
}

// rawRateColumnLen is PaycrestOrderRecord.RawRate's column size.
const rawRateColumnLen = 32

// storableRawRate is Paycrest's raw order rate as it can be stored. The string
// comes from Paycrest, unbounded, and the row is written AFTER Paycrest opened
// the order — a value the column refuses would leave a live deposit address we
// hold no row for. A plain positive decimal that fits is kept as it is; a longer
// one is cut DOWN to 8 decimals (a lower rate only makes the payout check
// stricter, never looser); anything else — or still too long — is "", which
// sends keepSurplusPayoutCovers back to sellPayoutCovers.
func storableRawRate(raw string) string {
	raw = strings.TrimSpace(raw)
	if !plainDecimal.MatchString(raw) {
		return ""
	}
	if len(raw) <= rawRateColumnLen {
		return raw
	}
	r, ok := new(big.Rat).SetString(raw)
	if !ok || r.Sign() <= 0 {
		return ""
	}
	scaled := new(big.Int).Quo(new(big.Int).Mul(r.Num(), big.NewInt(100_000_000)), r.Denom())
	cut := new(big.Rat).SetFrac(scaled, big.NewInt(100_000_000)).FloatString(8)
	if len(cut) > rawRateColumnLen {
		return ""
	}
	return cut
}

// keepSurplusPayoutCovers is the payout check for a KeepSurplus order, from
// Paycrest's own answer where it gives enough: the deposit less the fee — the
// larger of the fee Paycrest reported and our model of it at the percent it
// charged — at the raw rate Paycrest answered with, rounded down to the kobo.
// A row with no raw rate falls back to sellPayoutCovers.
func keepSurplusPayoutCovers(row *models.PaycrestOrderRecord, fiatAmount string) bool {
	tokens, ok1 := new(big.Rat).SetString(strings.TrimSpace(row.TokenAmount))
	owed, ok2 := new(big.Rat).SetString(strings.TrimSpace(fiatAmount))
	if !ok1 || !ok2 {
		return false
	}
	rate, ok := new(big.Rat).SetString(strings.TrimSpace(row.RawRate))
	if !ok || rate.Sign() <= 0 {
		return sellPayoutCovers(row, fiatAmount)
	}
	fee, ok := paycrestSellFee(tokens, row.SenderFeePercent)
	if !ok {
		return sellPayoutCovers(row, fiatAmount)
	}
	if reported, ok := new(big.Rat).SetString(strings.TrimSpace(row.SenderFee)); ok && reported.Cmp(fee) > 0 {
		fee = reported
	}
	return paycrestSellPayout(tokens, fee, rate).Cmp(owed) >= 0
}

// CancelPayoutSale withdraws a seller payout's sale that lapsed before the
// treasury funded it: the order is cancelled and its deposit address cleared in
// one write, like a short order, so nothing can be sent to it afterwards. Paycrest
// has no cancel endpoint; their copy lapses on its own. It refuses an order
// whose payment has been claimed.
func (s *PaycrestOrderService) CancelPayoutSale(ctx context.Context, row *models.PaycrestOrderRecord) error {
	if s == nil {
		return ErrPaycrestDisabled
	}
	_, err := s.cancelOrderAs(ctx, row.UserID, row.ID, map[string]any{
		"failure_reason":  "payout sale lapsed before it was funded",
		"deposit_address": "",
	}, true)
	return err
}
