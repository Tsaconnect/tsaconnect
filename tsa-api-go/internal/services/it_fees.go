package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// it_fees.go — fee DEDUCTION for Instant Transfer.
//
// The fee-quote engine (fee_quote_service.go) used to be display-only: the app
// showed "Est. net if deducted" and the payout disbursed the gross. This wires
// the same quote into the money path, so what the user is shown is what moves:
//
//	sell:  quote at intent creation → disburse net → the fee stays in the
//	       payout balance as fiat
//	buy:   quote on the naira received → credit the MCGP equivalent of the net
//	       → the withheld asset is simply never minted, so it stays with the
//	       contract's float
//
// The carve that funds cashback and TradePoints is computed from the PLATFORM-
// KEPT portion only (swap + platform fee). The provider's fee is money that
// leaves for Flutterwave and the tax line is remitted — paying cashback on
// either would distribute revenue the platform never received.

// itDepositCurrency is the fiat rail IT collects buys on. Only NGN virtual
// accounts exist today, so the two pre-payment quote paths (intent creation and
// the operator reprice) can name it directly. The settlement path passes the
// VA's own Currency instead — when multi-currency VAs land, the money path is
// already correct and only these two quotes need resolving from the user's VA.
const itDepositCurrency = "NGN"

// ErrITFeeQuoteUnavailable is returned when fees can't be priced for a
// transfer. It fails the transfer rather than silently falling back to a
// zero-fee (or gross) settlement — a mispriced money movement is worse than a
// refused one.
var ErrITFeeQuoteUnavailable = errors.New("Instant Transfer fees are temporarily unpriceable — try again shortly")

// ErrITAmountBelowFees is returned when the fees meet or exceed the transfer,
// so nothing would reach the user.
var ErrITAmountBelowFees = errors.New("this amount is too small to cover the transfer fees")

// ITFeeQuoter is the narrow seam over FeeQuoteService this package needs.
// *FeeQuoteService satisfies it as-is.
type ITFeeQuoter interface {
	Quote(ctx context.Context, in FeeQuoteInput) (*FeeQuote, error)
}

// SetFeeQuoter wires the fee engine used to deduct IT fees. Required for
// deduction: with no quoter the service refuses to price a transfer rather
// than settling one for free (see ErrITFeeQuoteUnavailable).
func (s *ITService) SetFeeQuoter(q ITFeeQuoter) { s.feeQuoter = q }

// HasFeeQuoter reports whether fee deduction is wired.
func (s *ITService) HasFeeQuoter() bool { return s != nil && s.feeQuoter != nil }

// ITFeeBreakdown is the deduction applied to one leg, all in kobo.
type ITFeeBreakdown struct {
	// Gross is the naira value of the asset leg at the locked rate.
	Gross *big.Int
	// Fee is everything withheld; Net is what actually moves to the user.
	Fee *big.Int
	Net *big.Int
	// PlatformKept is the part of Fee the platform retains (swap + platform
	// fee), i.e. the base that funds cashback and TP. Provider fee and tax are
	// excluded — they are pass-through and remittance respectively.
	PlatformKept *big.Int
}

// quoteITFees prices one IT leg. flow is FeeFlowWithdraw for a sell (naira
// leaving to the user) and FeeFlowDeposit for a buy (naira arriving).
//
// Every IT leg is DENOMINATED in naira — the rate engine prices MCGP in NGN
// and the gateway converts at settlement — but payCurrency is what the user
// actually pays in or is paid out in, and that is what selects the platform
// tier (0.5% local, 2% foreign). Empty means the legacy NGN rail.
func (s *ITService) quoteITFees(ctx context.Context, flow FeeFlow, grossKobo *big.Int, payCurrency string) (*ITFeeBreakdown, error) {
	if grossKobo == nil || grossKobo.Sign() <= 0 {
		return nil, fmt.Errorf("it fees: gross must be positive")
	}
	if s.feeQuoter == nil {
		return nil, ErrITFeeQuoteUnavailable
	}
	if strings.TrimSpace(payCurrency) == "" {
		payCurrency = "NGN"
	}
	q, err := s.feeQuoter.Quote(ctx, FeeQuoteInput{
		Flow:            flow,
		Asset:           FeeAssetMCGP,
		Amount:          koboToNairaDecimal(grossKobo),
		Currency:        "NGN",
		PaymentCurrency: payCurrency,
	})
	if err != nil || q == nil {
		return nil, ErrITFeeQuoteUnavailable
	}
	if !q.Viable || q.NetAmount.Minor == nil || q.NetAmount.Minor.Sign() <= 0 {
		return nil, ErrITAmountBelowFees
	}

	// Platform-kept = swap + platform fee. Both are quoted in the same minor
	// units as the gross, so this is a plain sum, not a re-derivation.
	kept := big.NewInt(0)
	if q.SwapFee.Fee.Minor != nil {
		kept.Add(kept, q.SwapFee.Fee.Minor)
	}
	if q.PlatformFee.InCurrency.Minor != nil {
		kept.Add(kept, q.PlatformFee.InCurrency.Minor)
	}

	return &ITFeeBreakdown{
		Gross:        new(big.Int).Set(grossKobo),
		Fee:          new(big.Int).Set(q.TotalFees.Minor),
		Net:          new(big.Int).Set(q.NetAmount.Minor),
		PlatformKept: kept,
	}, nil
}

// creditableMcgp prices a buy: fees come off the naira received, and only the
// net buys MCGP. The withheld naira's worth of MCGP is never minted, so it
// stays with the contract's float — the deposit-side mirror of the sell, where
// the withheld naira stays in the payout balance.
//
// Every path that turns naira into credited MCGP goes through here (intent
// quote, VA settlement, operator reprice, indicative quote) so none of them can
// drift into crediting the gross.
func (s *ITService) creditableMcgp(ctx context.Context, paidKobo, buyRateKobo *big.Int, payCurrency string) (*big.Int, *ITFeeBreakdown, error) {
	fees, err := s.quoteITFees(ctx, FeeFlowDeposit, paidKobo, payCurrency)
	if err != nil {
		return nil, nil, err
	}
	mcgp := mcgpForNgn(fees.Net, buyRateKobo)
	if mcgp.Sign() <= 0 {
		return nil, nil, ErrITAmountBelowFees
	}
	return mcgp, fees, nil
}

// koboToNairaDecimal renders kobo as the 2dp decimal string the fee quote
// engine parses ("13785" → "137.85"). Integer math only.
func koboToNairaDecimal(kobo *big.Int) string {
	if kobo == nil {
		return "0.00"
	}
	naira, rem := new(big.Int).QuoRem(kobo, big.NewInt(100), new(big.Int))
	return fmt.Sprintf("%s.%02d", naira.String(), rem.Int64())
}

// applyFees records a breakdown on an intent (in-memory; the caller persists).
func applyFees(intent *models.ITIntent, b *ITFeeBreakdown) {
	if intent == nil || b == nil {
		return
	}
	intent.FeeNgnKobo = b.Fee.String()
	intent.NetNgnKobo = b.Net.String()
	intent.PlatformKeptKobo = b.PlatformKept.String()
}

// payoutKobo is the amount a sell actually disburses: the net when fees were
// applied, the gross for intents created before deduction shipped (their terms
// were quoted without a deduction and must settle that way).
func payoutKobo(intent *models.ITIntent) (*big.Int, error) {
	raw := intent.NetNgnKobo
	if raw == "" {
		raw = intent.NgnKobo
	}
	v, ok := new(big.Int).SetString(raw, 10)
	if !ok || v.Sign() <= 0 {
		return nil, fmt.Errorf("it payout: bad intent amount %q", raw)
	}
	return v, nil
}

// settleITFee books the platform-kept fee into the fee engine, which carves it
// into system / cashback / TP exactly like every other section. Idempotent per
// (source type, intent id), so a redelivered webhook can't double-distribute.
//
// Called only once the money has actually moved — a fee that was never
// collected must never mint TradePoints.
func (s *ITService) settleITFee(ctx context.Context, intent *models.ITIntent) {
	if s.fees == nil || intent == nil || intent.PlatformKeptKobo == "" {
		return
	}
	kept, ok := new(big.Int).SetString(intent.PlatformKeptKobo, 10)
	if !ok || kept.Sign() <= 0 {
		return
	}
	// Minor units → USD at the rate for the INTENT'S OWN currency.
	//
	// This previously used EffectiveKoboRates, which returns the NGN/USD rate
	// unconditionally. For a cedi intent that divided pesewas (~12/USD) by the
	// naira rate (~1400/USD), understating the fee's USD value by roughly 100x
	// — so cashback and TradePoints for Ghanaian users would have been
	// near-zero, silently. Inverted, it would mint ~100x the TP, which is real
	// distributed value.
	//
	// A stale or missing rate means we cannot value the fee, so the carve is
	// skipped rather than booked wrong; the fee itself is already retained.
	rate, ok := s.ratePerUsdFor(ctx, models.ITIntentCurrency(intent))
	if !ok {
		return
	}
	feeUSD := koboToUSD(kept, rate)
	if feeUSD <= 0 {
		return
	}
	s.fees.EnqueueFeeSettled(FeeEvent{
		Feature:    models.FeatureInstantTransfer,
		Token:      "MCGP",
		FeeUSD:     feeUSD,
		UserID:     intent.UserID,
		SourceType: itFeeSourceType,
		SourceID:   intent.ID,
	})
}

// itFeeSourceType namespaces IT fee distributions in the ledger (the admin
// tracker groups by this alongside "p2p_release", "checkout", …).
const itFeeSourceType = "it_intent"

// koboToUSD converts kobo to USD at ngnPerUsd naira per dollar.
func koboToUSD(kobo *big.Int, ngnPerUsd float64) float64 {
	if kobo == nil || ngnPerUsd <= 0 {
		return 0
	}
	naira := new(big.Float).Quo(new(big.Float).SetInt(kobo), big.NewFloat(100))
	usd, _ := new(big.Float).Quo(naira, big.NewFloat(ngnPerUsd)).Float64()
	return usd
}

// SetFeeDistributor wires the fee engine that carves IT fees into system,
// cashback and TP. Optional — without it fees are still withheld, they just
// don't fund cashback/TP (and the omission is visible in the tracker).
// ITFeeSink is the narrow seam over FeeDistributionService this package needs —
// matching ITFeeQuoter and FxProvider, so the fee-valuation path is testable
// without standing up the whole distribution engine. *FeeDistributionService
// satisfies it as-is.
type ITFeeSink interface {
	EnqueueFeeSettled(FeeEvent)
}

// SetFeeDistributor wires the carve destination. A nil distributor is valid and
// simply skips the carve.
func (s *ITService) SetFeeDistributor(f *FeeDistributionService) {
	if f == nil {
		s.fees = nil
		return
	}
	s.fees = f
}

// ratePerUsdFor resolves minor-units-per-USD for a currency.
//
// It prefers the multi-currency FxRateProvider. The FxProvider actually held by
// ITService is the narrow NGN-only interface, but the production implementation
// (p2pFxProvider) satisfies both, so a type assertion gets the richer view
// without threading a second dependency through every construction site.
//
// The NGN fallback exists for a provider that implements only the narrow
// interface, and is deliberately restricted to NGN: returning the naira rate
// for any other currency is precisely the ~100x bug this function was written
// to remove, so an unrecognised currency reports "cannot value" instead.
func (s *ITService) ratePerUsdFor(ctx context.Context, currency string) (float64, bool) {
	if s.fx == nil {
		return 0, false
	}
	if multi, ok := s.fx.(FxRateProvider); ok {
		rate, _, err := multi.RatePerUsd(ctx, currency)
		if err != nil || rate <= 0 {
			return 0, false
		}
		return rate, true
	}
	if !strings.EqualFold(strings.TrimSpace(currency), "NGN") {
		return 0, false
	}
	rate, _, err := s.fx.NgnPerUsd(ctx)
	if err != nil || rate <= 0 {
		return 0, false
	}
	return rate, true
}
