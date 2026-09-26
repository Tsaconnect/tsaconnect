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
	covers := sellPayoutCovers
	if req.KeepSurplus && !converted {
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

// ─── KeepSurplus: the merchant is paid what they invoiced; the cushion is ours ─
//
// A KeepSurplus payout is judged by Paycrest's own arithmetic rather than the
// (1 − p) rate model above: that model under-states the payout by ~p² (≈0.01%),
// more than the whole cushion once an invoice passes ~₦20k, and would hand the
// surplus straight back to the merchant. Every rounding is still taken against
// the merchant, and the order is checked against Paycrest's own answer.

// payMerchantMarginFlat and payMerchantMarginPerToken are what a KeepSurplus
// payout is sized to clear above the naira owed: the drift budget. The fee is
// chosen at the QUOTED rate and Paycrest fills at its own, so a move worse than
// this between the two refuses the order ("the rate moved") instead of paying
// the merchant short. Fee and payout rounding are not in it — the payout model
// already takes them against the merchant. It grows with the order, like the
// rate headroom, so a large order is not refused on a single kobo tick.
var (
	payMerchantMarginFlat     = big.NewRat(20, 100) // ₦0.20
	payMerchantMarginPerToken = big.NewRat(1, 100)  // ₦0.01 per token
)

func payMerchantMargin(tokens *big.Rat) *big.Rat {
	return new(big.Rat).Add(payMerchantMarginFlat, new(big.Rat).Mul(tokens, payMerchantMarginPerToken))
}

// surplusFeeStepsPerPercent is how finely a KeepSurplus fee is chosen: 0.0001%.
// A whole basis point is too coarse — on ₦50,000 it is ₦5, more than the whole
// cushion there, so a bps-stepped fee would round the surplus straight back to
// the merchant. Paycrest takes the percent as a decimal string.
const surplusFeeStepsPerPercent = 10_000

// keepSurplusMaxUpliftSteps caps a KeepSurplus fee at the configured fee plus
// 0.5%. A CreateSellForFiat deposit's cushion is a small part of that; a fee
// that wants to climb past it means the deposit is far larger than the invoice
// needs, and the excess is left with the merchant — and alerted — rather than
// silently kept.
const keepSurplusMaxUpliftSteps = 50 * surplusFeeStepsPerPercent / 100

// paycrestFeeScale: Paycrest states a sender fee to 4 token decimals.
var paycrestFeeScale = big.NewInt(10_000)

// paycrestSellFee is the sender fee Paycrest takes from a sell deposit of
// `tokens` at `pct` percent: the fee inside the deposit, tokens × p/(1 + p) —
// the first live order's 0.0073 USDC fee on 0.742226 at 1% is exactly this —
// rounded UP to 4 decimals, against the merchant, whichever way Paycrest rounds.
func paycrestSellFee(tokens *big.Rat, pct string) (*big.Rat, bool) {
	p, ok := new(big.Rat).SetString(strings.TrimSpace(pct))
	if !ok || p.Sign() < 0 {
		return nil, false
	}
	p.Quo(p, big.NewRat(100, 1))
	fee := new(big.Rat).Quo(new(big.Rat).Mul(tokens, p), new(big.Rat).Add(big.NewRat(1, 1), p))
	scaled := new(big.Rat).Mul(fee, new(big.Rat).SetInt(paycrestFeeScale))
	units, rem := new(big.Int).QuoRem(scaled.Num(), scaled.Denom(), new(big.Int))
	if rem.Sign() > 0 {
		units.Add(units, big.NewInt(1))
	}
	return new(big.Rat).SetFrac(units, paycrestFeeScale), true
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

// keepSurplusFeePercent is the sender fee for a KeepSurplus order: the highest
// 0.0001% step at which `tokens` at the quoted raw rate still pay `owed` plus
// the margin, by Paycrest's arithmetic taken against the merchant. Never below
// the configured fee `base`, never above base + keepSurplusMaxUpliftSteps —
// `capped` says the cap, not the margin, stopped it. So the merchant receives
// what they invoiced (plus the margin) and the deposit's cushion — the drift
// tolerance, rate headroom and fee-rounding allowance it was sized with —
// becomes ours.
//
// Where even `base` does not clear the margin (no cushion to spare) or the rate
// is unreadable, `base` is returned: the order stands or falls on the payout
// check, exactly as before.
func keepSurplusFeePercent(tokens *big.Rat, rawRate, base string, owed *big.Rat) (fee string, capped bool) {
	baseSteps, ok := feePercentToSteps(base, surplusFeeStepsPerPercent)
	rate, rateOK := new(big.Rat).SetString(strings.TrimSpace(rawRate))
	if !ok || !rateOK || rate.Sign() <= 0 {
		return base, false
	}
	target := new(big.Rat).Add(owed, payMerchantMargin(tokens))
	clears := func(steps int) bool {
		f, ok := paycrestSellFee(tokens, stepsToFeePercent(steps, surplusFeeStepsPerPercent))
		return ok && paycrestSellPayout(tokens, f, rate).Cmp(target) >= 0
	}
	if !clears(baseSteps) {
		return base, false
	}
	top := min(baseSteps+keepSurplusMaxUpliftSteps, maxSenderFeePercent*surplusFeeStepsPerPercent)
	// Payout falls as the fee rises, so the highest clearing fee is a boundary.
	lo, hi := baseSteps, top
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if clears(mid) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return stepsToFeePercent(lo, surplusFeeStepsPerPercent), lo == top && top > baseSteps
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
