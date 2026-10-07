package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// paycrest_order_service.go — the brokered ramp lifecycle.
//
// Both legs are non-custodial. On a buy the user transfers naira straight to
// the liquidity provider's bank account and the stablecoin lands in their own
// connected wallet; on a sell they send from that same wallet and the provider
// pays their bank. We never hold either side, so this service brokers and
// records rather than settling.
//
// Consequences that shape the code below:
//
//   - We cannot make a user whole ourselves. When a provider fails, the money
//     is at a bank we have no relationship with, and there is no refund
//     endpoint. So the one thing this service must never do is let such an
//     order look like an ordinary expiry — see markUnmatched.
//   - There is no webhook endpoint yet, so progress comes from polling. Orders
//     are therefore designed to be safely re-read: Sync is idempotent and only
//     ever moves a row forward.

var (
	// ErrPaycrestOrderOpen enforces one open order per user per direction. A
	// second concurrent buy would leave the user staring at two provider
	// accounts with two countdowns and no way to tell which one to pay.
	ErrPaycrestOrderOpen = errors.New("you already have an order awaiting payment")
	// ErrPaycrestPayOrderOpen is ErrPaycrestOrderOpen when what is open is a
	// TSA Pay payment's order. Its own error because that order is not in her
	// order list (it is how the payment moves), so "cancel it from your
	// orders" would send her looking for something she cannot find. Wraps
	// ErrPaycrestOrderOpen: every existing check for an open order still holds.
	ErrPaycrestPayOrderOpen = fmt.Errorf("%w: a payment is in progress", ErrPaycrestOrderOpen)
	// ErrPaycrestBankUnsupported means the user's bank exists for us but not
	// for Paycrest. Explicit, because the alternative is a payout that
	// silently goes nowhere.
	ErrPaycrestBankUnsupported = errors.New("this bank cannot receive payouts through this route")

	// ErrPaycrestAccountShape means the identifier does not match the kind of
	// destination it is aimed at — a bank account number sent to a mobile-money
	// code, or a number too short to be either. Refused before the order
	// exists, because after the on-chain leg it is not correctable.
	ErrPaycrestAccountShape  = errors.New("that account number does not match the destination you chose")
	ErrPaycrestOrderNotYours = errors.New("order not found")
	// ErrPaycrestNotCancellable covers both "too late" and "already gone": an
	// order past the waiting-for-the-user stage, and one whose payment the user
	// has already claimed to have sent.
	ErrPaycrestNotCancellable = errors.New("this order can no longer be cancelled")
	// ErrPaycrestNotClaimable: a conversion record (ITC-) is paid through its
	// purchase, never into a Paycrest order, so it takes no payment claim.
	ErrPaycrestNotClaimable = errors.New("this order is paid through its purchase")
)

// ErrPaycrestNothingSent marks a refusal that happened before any request to
// open an order left us — a rate read or a bank-directory lookup failing. The
// ramp router may fail over on it (router spec §4.3/§9).
var ErrPaycrestNothingSent = errors.New("paycrest: refused before anything was sent")

// nothingSentError keeps the cause's own text — paycrestError and the app
// read it unchanged — while matching ErrPaycrestNothingSent too.
type nothingSentError struct{ cause error }

func (e nothingSentError) Error() string   { return e.cause.Error() }
func (e nothingSentError) Unwrap() []error { return []error{ErrPaycrestNothingSent, e.cause} }

// nothingSent marks err as a pre-create failure. Nil stays nil; an error
// already marked is returned as-is.
func nothingSent(err error) error {
	if err == nil || errors.Is(err, ErrPaycrestNothingSent) {
		return err
	}
	return nothingSentError{cause: err}
}

// PaycrestOrderer is the client surface this service needs.
type PaycrestOrderer interface {
	CreateOnrampOrder(ctx context.Context, req PaycrestOnrampRequest) (*PaycrestOrder, error)
	CreateOfframpOrder(ctx context.Context, req PaycrestOfframpRequest) (*PaycrestOrder, error)
	GetOrder(ctx context.Context, id string) (*PaycrestOrder, error)
}

// PaycrestOrderService brokers ramp orders.
type PaycrestOrderService struct {
	db     *gorm.DB
	pc     PaycrestOrderer
	quotes *PaycrestQuoteService
	dir    *PaycrestDirectory

	// currencies gates order creation against the live ramp-provider column —
	// see SetCurrencyRegistry.
	currencies *CurrencyRegistry

	// senderFeePercent is the company take (RAMP_COMPANY_TAKE_BPS, as a
	// percent) — the sender fee on every ramp order, direct, converted or
	// swapped, and our only margin on Paycrest (owner ruling 2026-10-07).
	// Empty defers to the dashboard profile default, which is what produced
	// the 0.5% observed on live orders.
	senderFeePercent string
	senderFeeAddress string
	// pay is TSA Pay's own split, which a payment's payout is priced on —
	// see PayFeeSplit. Zero leaves TSA Pay's payouts unpriced (refused).
	pay PayFeeSplit

	// ── Conversion (Phase 2, Task 9). Both dormant by default: conversionEnabled
	// false and treasuryAddr "" together mean every buy is created exactly as
	// before this feature existed — direct only, delivered to the user's own
	// wallet. Wiring calls SetConversionEnabled(true) and SetTreasury with
	// Treasury.Address() to turn it on.
	conversionEnabled bool
	treasuryAddr      string
	// treasuryCaps is an optional early gate: when wired, CreateBuy refuses an
	// obviously oversized converted order up front rather than only
	// discovering it once the background conversion orchestrator tries to
	// execute it. Nil applies no gate, same pattern as SetCurrencyRegistry —
	// the orchestrator itself still runs the authoritative CheckCaps (priced
	// off real on-chain decimals and actual in-flight exposure) immediately
	// before moving any money, so this is advisory, not the only check.
	treasuryCaps treasuryCapsChecker
}

// treasuryCapsChecker is the treasury surface CreateBuy consults for the
// early converted-order gate described above. *Treasury satisfies it.
type treasuryCapsChecker interface {
	CheckCaps(chainKey string, orderUSD, inFlightUSD float64) error
}

// SetConversionEnabled is the server-side kill switch for converted buys —
// off (the default) means CreateBuy never routes through the treasury.
func (s *PaycrestOrderService) SetConversionEnabled(on bool) { s.conversionEnabled = on }

// SetTreasury wires the address a converted buy tells Paycrest to deliver
// to. Callers pass Treasury.Address(), which is "" when the configured lanes
// disagree on their signer or none are configured at all — CreateBuy refuses
// a converted order rather than hand Paycrest an empty delivery address.
func (s *PaycrestOrderService) SetTreasury(addr string) { s.treasuryAddr = strings.TrimSpace(addr) }

// SetTreasuryCaps wires the optional early caps gate — see treasuryCaps'
// doc comment.
func (s *PaycrestOrderService) SetTreasuryCaps(c treasuryCapsChecker) { s.treasuryCaps = c }

// feePercentToSteps reads a sender-fee percent ("1", "0.5") as a whole number
// of 1/perPercent-percent steps, so fee arithmetic is exact rather than float.
func feePercentToSteps(pct string, perPercent int) (int, bool) {
	v, ok := parseSenderFeePercent(pct)
	if !ok {
		return 0, false
	}
	return int(math.Round(v * float64(perPercent))), true
}

// stepsToFeePercent writes whole steps back in the percent form Paycrest takes.
func stepsToFeePercent(steps, perPercent int) string {
	return strconv.FormatFloat(float64(steps)/float64(perPercent), 'f', -1, 64)
}

// feePercentToBps reads a sender-fee percent as integer bps, so F − B is exact.
func feePercentToBps(pct string) (int, bool) { return feePercentToSteps(pct, 100) }

// bpsToFeePercent writes integer bps back in the percent form Paycrest takes.
func bpsToFeePercent(bps int) string { return stepsToFeePercent(bps, 100) }

// SenderFeePercentFromBps writes RAMP_COMPANY_TAKE_BPS in the percent form
// Paycrest's sender fee takes ("" for no take, which the constructor reads
// as unset).
func SenderFeePercentFromBps(bps int64) string {
	if bps <= 0 || bps > math.MaxInt32 {
		return ""
	}
	return bpsToFeePercent(int(bps))
}

// ConversionPriced reports whether the company take is a usable sender fee,
// so a converted buy or swapped sell knows what Paycrest is sent. Unset or
// refused, Paycrest would apply its dashboard default instead — a fee nobody
// quoted — so conversion stays off.
func (s *PaycrestOrderService) ConversionPriced() bool {
	_, ok := parseSenderFeePercent(s.senderFeePercent)
	return ok
}

// TakePercent is the company take as the sender fee every ramp order sends.
func (s *PaycrestOrderService) TakePercent() string { return s.senderFeePercent }

// PayFeeSplit is TSA Pay's own Paycrest pricing, kept exactly as it was before
// the ramp moved to the company take (owner ruling 2026-10-07: merchant payout
// economics do not change). FeePercent is F (TSA_PAY_FEE_PERCENT): the
// platform fee a payment is split by and the sender fee on its direct payout,
// which KeepSurplus may raise to take the deposit's cushion. A converted
// payment's swap may spend BudgetBps of it (TSA_PAY_ROUND_TRIP_BUDGET_BPS,
// unspent rest as our LiFi fee), so its payout is sent F − B.
type PayFeeSplit struct {
	FeePercent string
	BudgetBps  int
}

// SenderFee is the payout's sender fee: F direct, F − B converted. "" when F
// is unusable or, converted, when 0 < B < F does not hold.
func (p PayFeeSplit) SenderFee(converted bool) string {
	pct, ok := parseSenderFeePercent(p.FeePercent)
	if !ok {
		return ""
	}
	if !converted {
		return strconv.FormatFloat(pct, 'f', -1, 64)
	}
	f, _ := feePercentToBps(p.FeePercent)
	if p.BudgetBps <= 0 || p.BudgetBps >= f {
		return ""
	}
	return bpsToFeePercent(f - p.BudgetBps)
}

// SetPayFeeSplit wires TSA Pay's split (see PayFeeSplit).
func (s *PaycrestOrderService) SetPayFeeSplit(p PayFeeSplit) { s.pay = p }

// ErrConversionUnavailable refuses a converted buy: the feature is off, no
// treasury is configured (or its lanes disagree on an address), or the
// treasury's exposure caps would refuse this order before the background
// orchestrator even tried to run it. Always a user-facing message, never a
// raw internal error surfacing as a 500.
var ErrConversionUnavailable = errors.New("delivering to that network isn't available for this purchase right now")

// ErrConversionPriceMoved refuses a converted buy whose fee rose past the one
// she was quoted: a dear route's fee is re-priced when the order is opened,
// and she agreed to the rate she saw, not to whatever it became since.
var ErrConversionPriceMoved = errors.New("the price changed since your quote")

// quotedFeeToleranceBps is how far a converted buy's fee may rise between the
// quote she saw and the order before it is refused — room for LiFi's own
// quote-to-quote jitter, not for a real price move.
const quotedFeeToleranceBps = 10

// SetCurrencyRegistry wires the server-side kill switch: CreateBuy and
// CreateSell refuse with ErrPaycrestDisabled when the order's currency does
// not resolve to RampProviderPaycrest. Optional — without it (nil), no gate
// is applied, so existing tests and any other construction site keep
// working. This is what makes flipping a currency's ramp_provider column
// actually stop the rail during an incident, rather than only changing what
// GET /it/currencies advertises to not-yet-updated app builds.
func (s *PaycrestOrderService) SetCurrencyRegistry(r *CurrencyRegistry) { s.currencies = r }

// Refusals only the user can fix. The ramp router (services/ramp) must tell
// these from provider failures so it never fails a user's own problem over
// to another provider. Wording unchanged from the plain errors they replace.
var (
	ErrBuyNeedsWallet         = errors.New("connect a wallet before buying")
	ErrBuyNeedsRefundAccount  = errors.New("a refund account is required in case the order fails")
	ErrSellNeedsWallet        = errors.New("connect a wallet before selling")
	ErrSellNeedsPayoutAccount = errors.New("a payout bank account is required")
)

// ErrOrderRequestSent marks an error from the provider's create call itself
// that is not a clean refusal (timeout, transport, 5xx): the request left us,
// so an order may exist upstream even though we hold no row for it.
var ErrOrderRequestSent = errors.New("paycrest: order request sent")

// ErrPaycrestOrderRejected: Paycrest answered the create with a 400/422 — it
// refused the request, so nothing was opened.
var ErrPaycrestOrderRejected = errors.New("paycrest: order request rejected")

// ErrBuyAttemptMayExist is the buy twin of ErrSellAttemptMayExist: Paycrest
// answered with an order but our row did not persist — and a failed commit
// does not prove nothing was written.
var ErrBuyAttemptMayExist = errors.New("a buy order may still exist for this attempt")

// markCreateErr classifies an error from the provider's create call.
func markCreateErr(err error) error {
	if paycrestRejected(err) {
		return fmt.Errorf("%w: %w", ErrPaycrestOrderRejected, err)
	}
	return fmt.Errorf("%w: %w", ErrOrderRequestSent, err)
}

// requirePaycrestEnabled refuses when the registry says this currency's ramp
// provider is not (or no longer) paycrest. A nil registry is "no gate" —
// see SetCurrencyRegistry.
func (s *PaycrestOrderService) requirePaycrestEnabled(fiat string) error {
	if s.currencies == nil {
		return nil
	}
	if s.currencies.RampProvider(fiat) != RampProviderPaycrest {
		return ErrPaycrestDisabled
	}
	return nil
}

// RequirePaycrestEnabled is the currency kill switch for callers outside
// this file (the ramp router's Paycrest adapter). Empty means NGN, as in
// CreateBuy/CreateSell.
func (s *PaycrestOrderService) RequirePaycrestEnabled(fiat string) error {
	if s == nil {
		return ErrPaycrestDisabled
	}
	f := strings.ToUpper(strings.TrimSpace(fiat))
	if f == "" {
		f = "NGN"
	}
	return s.requirePaycrestEnabled(f)
}

// ExplainMissingVenue exposes explainMissingVenue: it turns a bare
// ErrPaycrestNoVenue into ErrPaycrestLegClosed when the whole direction is
// closed, which the router reports differently from "no venue for this size".
func (s *PaycrestOrderService) ExplainMissingVenue(ctx context.Context, side, fiat string, err error) error {
	if s == nil {
		return err
	}
	return s.explainMissingVenue(ctx, side, strings.ToUpper(strings.TrimSpace(fiat)), err)
}

// AssertNoOpenOrder is assertNoOpenOrder for the ramp router's cross-provider
// pre-flight. It returns the same refusal CreateBuy/CreateSell would — a TSA
// Pay payment in progress keeps its own wording.
func (s *PaycrestOrderService) AssertNoOpenOrder(ctx context.Context, userID uuid.UUID, direction string) error {
	if s == nil {
		return ErrPaycrestDisabled
	}
	return s.assertNoOpenOrder(ctx, userID, direction)
}

// explainMissingVenue separates "nobody fills THIS amount" from "this currency
// does not trade this way at all".
//
// Both arrive as ErrPaycrestNoVenue, and the difference matters to the person
// reading it: the first is answered by trying another amount, the second never
// is. UGX and TZS have offramp providers only, so a UGX buy is permanently
// empty rather than momentarily unlucky.
//
// The app already hides a dead direction (GET /it/currencies reports the legs),
// so reaching this means a stale client or a leg that closed mid-session — the
// order is refused either way; only the wording changes.
func (s *PaycrestOrderService) explainMissingVenue(ctx context.Context, side, fiat string, err error) error {
	if !errors.Is(err, ErrPaycrestNoVenue) || s.quotes == nil {
		return err
	}
	legs, legErr := s.quotes.RampLegs(ctx)
	if legErr != nil {
		return err
	}
	l := legs[strings.ToUpper(strings.TrimSpace(fiat))]
	if (side == "buy" && !l.Buy) || (side == "sell" && !l.Sell) {
		verb := "Buying"
		if side == "sell" {
			verb = "Selling"
		}
		// Wrapped, because the handler maps sentinels to status codes with
		// errors.Is: an unwrapped error here falls to its default branch and
		// tells the user the provider is unreachable — "broken, try again" —
		// when the truth is that this direction is permanently closed.
		return fmt.Errorf("%w: %s is not available in %s right now",
			ErrPaycrestLegClosed, verb, strings.ToUpper(fiat))
	}
	return err
}

// NewPaycrestOrderService returns nil unless everything it needs is wired, so
// routes can be gated the same way the rest of the package is. feePercent is
// the company take as a sender-fee percent (SenderFeePercentFromBps).
func NewPaycrestOrderService(db *gorm.DB, pc PaycrestOrderer, quotes *PaycrestQuoteService, dir *PaycrestDirectory, feePercent, feeAddress string) *PaycrestOrderService {
	if db == nil || pc == nil || quotes == nil {
		return nil
	}
	feePercent = strings.TrimSpace(feePercent)
	if feePercent != "" {
		if pct, ok := parseSenderFeePercent(feePercent); ok {
			// Canonical form, so "0.50" compares equal to Paycrest's "0.5".
			feePercent = strconv.FormatFloat(pct, 'f', -1, 64)
		} else {
			log.Printf("[paycrest] ALARM RAMP_COMPANY_TAKE_BPS as a sender fee (%q%%) refused (want 0 < p <= %d%%); orders fall back to the dashboard fee and quotes show none",
				feePercent, maxSenderFeePercent)
			feePercent = ""
		}
	}
	feeAddress = strings.TrimSpace(feeAddress)
	if feeAddress != "" {
		if addr, ok := validSenderFeeAddress(feeAddress); ok {
			feeAddress = addr
		} else {
			log.Printf("[paycrest] ALARM PAYCREST_SENDER_FEE_ADDRESS=%q refused (not a valid, correctly checksummed EVM address); orders fall back to the dashboard fee address",
				feeAddress)
			feeAddress = ""
		}
	}
	// One source for the fee: the quote must price in exactly what the order sends.
	quotes.SetSenderFeePercent(feePercent)
	return &PaycrestOrderService{
		db: db, pc: pc, quotes: quotes, dir: dir,
		senderFeePercent: feePercent,
		senderFeeAddress: feeAddress,
	}
}

// validSenderFeeAddress checks the address our fee is paid to. Nothing on-chain
// would ever flag a typo here — every fee would simply land at an address
// nobody controls — so it is refused at boot instead. A mixed-case address
// carries an EIP-55 checksum and must match it; the zero address burns the fee.
func validSenderFeeAddress(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "0x") || !common.IsHexAddress(raw) {
		return "", false
	}
	addr := common.HexToAddress(raw)
	if addr == (common.Address{}) {
		return "", false
	}
	hex := raw[2:]
	mixedCase := strings.ToLower(hex) != hex && strings.ToUpper(hex) != hex
	if mixedCase && raw != addr.Hex() {
		return "", false
	}
	return addr.Hex(), true
}

// ─── Buy (onramp) ──────────────────────────────────────────────────────────

// PaycrestBuyRequest is what the app sends to start a purchase.
type PaycrestBuyRequest struct {
	UserID uuid.UUID
	// MaxRate/PriceGuard are set only by the ramp router: the highest
	// fiat-per-token rate this buy may bind at, and what to do past it.
	// Empty/off on /api/paycrest/*, which keeps today's behaviour.
	MaxRate    string
	PriceGuard PriceGuardMode
	// WalletAddress is the user's connected wallet — where the stablecoin is
	// delivered. Already reconciled against the account by the handler.
	WalletAddress string
	FiatCode      string
	FiatAmount    string
	// ChainKey and Token name the venue the user picked from a fresh quote.
	ChainKey string
	Token    string
	// RequestedChain/RequestedToken are what the user actually wants
	// delivered. Equal to ChainKey/Token (or empty) means a direct buy —
	// Paycrest fills it as-is. Different means a converted buy: Paycrest
	// settles ChainKey/Token to the treasury and a background conversion
	// delivers RequestedChain/RequestedToken to the user instead. The
	// handler defaults both to ChainKey/Token when the app omits them, so
	// older builds keep behaving exactly as before this field existed.
	RequestedChain string
	RequestedToken string
	// RequestedChainID/RequestedTokenAddress are the same question asked the
	// only way an arbitrary token can be named: a LiFi chain id and a token
	// address. When either is set they are authoritative and the symbol pair
	// above is ignored. Decimals are deliberately NOT accepted from the
	// client — they decide how much of a token she gets.
	RequestedChainID      int64
	RequestedTokenAddress string
	// DestinationAddress is where the requested token is delivered when the
	// user's EVM wallet cannot receive it — her Solana address. Required for a
	// Solana request (resolveRequestedAsset refuses one without it) and
	// ignored on every EVM destination. A refund still goes to WalletAddress:
	// this names where the ASSET lands, never where money comes back to.
	DestinationAddress string
	// QuotedMarkupBps is the route cost B′ (the venue's markupBps) of the
	// converted quote she accepted. The order refuses a cost risen more than
	// quotedFeeToleranceBps past it; zero (an older app) skips the check.
	QuotedMarkupBps int
	// RefundAccount is where naira returns if the order fails. Defaults to the
	// user's own bank, so a refund goes back to whoever paid.
	RefundBankCode    string // NIBSS
	RefundAccount     string
	RefundAccountName string
}

// grossDownForSenderFee converts the amount the user asked to pay into the
// amount we submit, so that Paycrest's own fee arithmetic lands back on the
// user's number.
//
// Paycrest computes amountToTransfer = amount x (1 + senderFeePercent/100) —
// the fee is added ON TOP. Submitting the user's figure directly means someone
// who types 800 is told to transfer 804.02, and the screen ends up asserting
// both "you pay 800" and "transfer 804.02", which cannot both be true. Dividing
// first puts the fee in the rate instead: the user sends exactly what they
// typed and receives correspondingly less token. Same margin, one number.
//
// Returns the requested amount unchanged when the fee percent is unknown —
// which is the case when the take is unset or refused and the value comes
// from the dashboard profile instead. Guessing it would put the user's
// transfer a few kobo off the amount the provider is waiting for, and an
// inexact transfer is exactly what strands a payment.
func grossDownForSenderFee(requested, feePercent string) (string, bool) {
	pct, ok := parseSenderFeePercent(feePercent)
	if !ok {
		return requested, false
	}
	amt, ok := parsePositiveDecimal(requested)
	if !ok {
		return requested, false
	}
	return strconv.FormatFloat(amt/(1+pct/100), 'f', 2, 64), true
}

// userRate is the rate stored on the order and shown to the user: the
// provider's rate with the fee Paycrest charged priced in, so the provider's
// own rate stays recoverable from the row's SenderFeePercent.
func userRate(side, providerRate, feePercent string) string {
	pct, _ := parseSenderFeePercent(feePercent)
	rate, _, err := quotedRate(side, providerRate, pct)
	if err != nil {
		return providerRate
	}
	return rate
}

// chargedFeePercent is the fee Paycrest reports on the order, falling back to
// want (the fee this order was actually sent with — the take, or a TSA Pay
// payout's own) when it reports none.
//
// A mismatch is an operator alarm, not a rounding note: the quote priced in
// want, so a different charge means the user was shown one amount and paid or
// received another. It happens when the take is unset or refused — the
// request then omits the fee and the dashboard default applies — or when the
// dashboard changes under a set env var.
func (s *PaycrestOrderService) chargedFeePercent(order *PaycrestOrder, ref, want string) string {
	reported := strings.TrimSpace(order.SenderFeePercent)
	if reported == "" {
		return want
	}
	wantV, _ := parseSenderFeePercent(want)
	got, ok := parseSenderFeePercent(reported)
	if !ok || math.Abs(got-wantV) > 1e-9 {
		log.Printf("[paycrest] ALARM sender fee mismatch ref=%s sent=%q paycrest_charged=%q — quoted amounts are off for this order",
			ref, want, reported)
	}
	if !ok {
		// Never store what we could not read: the column is varchar(16), and a
		// failed insert here strands an order that already exists upstream.
		return want
	}
	return strconv.FormatFloat(got, 'f', -1, 64)
}

// isConvertedRequest reports whether the buy converts: the requested asset
// (by address when sent, else chain/token symbol) isn't the settlement leg.
//
// The address path resolves server-side and can therefore FAIL — an
// unresolvable chain id or token is not "a direct buy", it is a request we
// cannot honour, and saying so here is what keeps it from becoming an order.
func (s *PaycrestOrderService) isConvertedRequest(req PaycrestBuyRequest) (bool, error) {
	chainKey := strings.ToLower(strings.TrimSpace(req.ChainKey))
	token := strings.ToUpper(strings.TrimSpace(req.Token))
	if req.RequestedChainID != 0 || req.RequestedTokenAddress != "" {
		asset, err := resolveRequestedAsset(s.quotes.chains, s.quotes.solanaValidator,
			RequestedAsset{ChainID: req.RequestedChainID, Address: req.RequestedTokenAddress, Destination: req.DestinationAddress})
		if err != nil {
			return false, err
		}
		// A stablecoin Paycrest serves directly on that chain is a direct buy.
		direct := asset.Stable && strings.EqualFold(asset.ChainKey, chainKey) && strings.EqualFold(asset.StableSymbol, token)
		return !direct, nil
	}
	// Legacy symbol path: the handler defaults RequestedChain/Token to
	// ChainKey/Token when the app omits them, so an unset pair (or one equal
	// to ChainKey/Token) reads as a direct buy exactly as it always did.
	rc, rt := strings.ToLower(strings.TrimSpace(req.RequestedChain)), strings.ToUpper(strings.TrimSpace(req.RequestedToken))
	return (rc != "" || rt != "") && (rc != chainKey || rt != token), nil
}

// requestedAssetLabel names what the user asked for in a refusal the user
// will read: the symbol when there is one, the address otherwise.
func requestedAssetLabel(req PaycrestBuyRequest) string {
	if sym := strings.ToUpper(strings.TrimSpace(req.RequestedToken)); sym != "" {
		return sym
	}
	if req.RequestedTokenAddress != "" {
		return fmt.Sprintf("%s on chain %d", req.RequestedTokenAddress, req.RequestedChainID)
	}
	return strings.ToUpper(strings.TrimSpace(req.Token))
}

// CreateBuy prices the chosen venue afresh, opens the order, and returns the
// provider account for the user to pay.
//
// The re-quote is not defensive padding: liquidity moved 4× overnight in
// testing, so a venue chosen even a minute ago may no longer be fillable at
// this size. Binding the order to a price we just confirmed is the only way to
// avoid showing a rate the order will not carry.
func (s *PaycrestOrderService) CreateBuy(ctx context.Context, req PaycrestBuyRequest) (*models.PaycrestOrderRecord, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		return nil, ErrBuyNeedsWallet
	}
	if req.RefundBankCode == "" || req.RefundAccount == "" {
		return nil, ErrBuyNeedsRefundAccount
	}
	if err := s.assertNoOpenOrder(ctx, req.UserID, models.PaycrestDirectionBuy); err != nil {
		return nil, err
	}

	fiat := strings.ToUpper(strings.TrimSpace(req.FiatCode))
	if fiat == "" {
		fiat = "NGN"
	}
	if err := s.requirePaycrestEnabled(fiat); err != nil {
		return nil, err
	}
	// Money in: the order hands the user a bank account to pay. Refuse an
	// unverified buyer before anything below spends a provider call (non-NGN
	// destination lookup, rates, the order itself). After the kill switch, so a
	// ramp that is switched off says so to everyone rather than sending an
	// unverified user off to verify for a route that is not there.
	if err := requireVerifiedForMoneyIn(ctx, s.db, req.UserID); err != nil {
		if !errors.Is(err, ErrKYCVerificationRequired) {
			// Fails closed either way; log so an outage is not mistaken for a
			// run of provider errors.
			log.Printf("[paycrest] kyc gate user=%s: %v", req.UserID, err)
		}
		return nil, err
	}
	// Destination first, then price. Resolving the refund account is cheap and
	// cached, while VenueFor spends the slow, concurrency-throttled /v2/rates
	// call — no reason to spend it on a request a malformed destination is
	// about to refuse. The refund account only matters when an order fails,
	// which is exactly when a bad one cannot be corrected.
	refundInst, refundKind, err := s.paycrestBankCode(ctx, fiat, req.RefundBankCode)
	if err != nil {
		return nil, err
	}
	if err := validatePayoutIdentifier(refundKind, fiat, req.RefundAccount); err != nil {
		return nil, err
	}

	converting, cerr := s.isConvertedRequest(req)
	if cerr != nil {
		// The requested asset could not be resolved server-side. Refused here,
		// before an order exists: after it does, the user is paying naira for
		// something we cannot name.
		return nil, cerr
	}

	// The company take is the sender fee, direct or converted: a converted
	// order's route cost is passed on at cost in T, never carved out of it.
	feeForOrder := s.senderFeePercent
	if converting && !s.ConversionPriced() {
		return nil, fmt.Errorf("%w: the company take is not a usable Paycrest fee", ErrConversionUnavailable)
	}

	var venue *PaycrestVenue
	recipient := req.WalletAddress // direct: Paycrest pays the user directly
	if converting {
		// Binding ruling 5: refuse before spending a LiFi call, let alone
		// opening an order, when the feature is off or no treasury is wired.
		if !s.conversionEnabled {
			return nil, ErrConversionUnavailable
		}
		treasuryAddr := s.treasuryAddr
		// Binding ruling 3: Treasury.Address() == "" means the configured
		// lanes disagree on their signer, or none are configured. Never hand
		// Paycrest an empty (== burn) delivery address.
		if treasuryAddr == "" {
			return nil, ErrConversionUnavailable
		}
		venues, verr := s.quotes.ConvertedVenues(ctx, "buy", fiat, req.FiatAmount, []RequestedAsset{{
			Chain: req.RequestedChain, Token: req.RequestedToken,
			ChainID: req.RequestedChainID, Address: req.RequestedTokenAddress, Recipient: req.WalletAddress,
			Destination: req.DestinationAddress,
		}})
		if verr != nil {
			return nil, nothingSent(s.explainMissingVenue(ctx, "buy", fiat, verr))
		}
		if len(venues) == 0 {
			return nil, fmt.Errorf("%w: no route can deliver %s right now",
				ErrConversionUnavailable, requestedAssetLabel(req))
		}
		v := venues[0]
		if req.QuotedMarkupBps > 0 && v.MarkupBps > req.QuotedMarkupBps+quotedFeeToleranceBps {
			log.Printf("[paycrest] converted buy refused: fee moved from quoted markup %d to %d bps (%s)",
				req.QuotedMarkupBps, v.MarkupBps, requestedAssetLabel(req))
			return nil, ErrConversionPriceMoved
		}
		if s.treasuryCaps != nil {
			// Phase 2 moves only stablecoins, so the venue's own TokenAmount
			// is already close enough to USD for this early, advisory check.
			// The orchestrator still runs the authoritative CheckCaps, priced
			// off real on-chain decimals and actual in-flight exposure,
			// immediately before it moves any money.
			if err := s.treasuryCaps.CheckCaps(v.ChainKey, v.TokenAmount, 0); err != nil {
				log.Printf("[paycrest] converted buy refused by treasury caps chain=%s amount=%.2f: %v",
					v.ChainKey, v.TokenAmount, err)
				return nil, ErrConversionUnavailable
			}
		}
		venue = &v
		recipient = treasuryAddr
	} else {
		v, verr := s.quotes.VenueFor(ctx, "buy", fiat, req.FiatAmount, req.ChainKey, req.Token)
		if verr != nil {
			return nil, nothingSent(s.explainMissingVenue(ctx, "buy", fiat, verr))
		}
		// Before anything is sent: a refusal here leaves no order anywhere.
		if err := checkPriceGuard("buy", v.Rate, req.MaxRate, req.PriceGuard); err != nil {
			return nil, err
		}
		venue = v
	}

	// Submit the grossed-down figure so the provider asks the user for exactly
	// the amount they entered. FiatAmount on the row stays the user's number.
	submitAmount, grossedDown := grossDownForSenderFee(req.FiatAmount, feeForOrder)

	ref := paycrestReference("PCB", req.UserID)
	order, err := s.pc.CreateOnrampOrder(ctx, PaycrestOnrampRequest{
		AmountNGN:        submitAmount,
		FiatCode:         fiat,
		Token:            venue.Token,
		Network:          venue.Network,
		RecipientAddress: recipient,
		RefundAccount: PaycrestBankAccount{
			Institution:       refundInst,
			AccountIdentifier: req.RefundAccount,
			AccountName:       req.RefundAccountName,
		},
		Reference:        ref,
		SenderFeePercent: feeForOrder,
		SenderFeeAddress: s.senderFeeAddress,
	})
	if err != nil {
		return nil, markCreateErr(err)
	}

	feePercent := s.chargedFeePercent(order, ref, feeForOrder)
	row := &models.PaycrestOrderRecord{
		UserID:           req.UserID,
		Direction:        models.PaycrestDirectionBuy,
		Status:           models.PaycrestStatusAwaitingPayment,
		Reference:        ref,
		PaycrestID:       order.ID,
		FiatCode:         fiat,
		FiatAmount:       req.FiatAmount,
		Token:            venue.Token,
		ChainKey:         venue.ChainKey,
		Network:          venue.Network,
		Rate:             userRate("buy", order.Rate, feePercent),
		TokenAmount:      order.Amount,
		ProviderID:       paycrestProviderOf(order, venue),
		SenderFee:        order.SenderFee,
		SenderFeePercent: feePercent,
		// The user's own wallet is always recorded here, converted or not —
		// it is where the money is ultimately meant to land. Holder (below)
		// is what tracks who actually has it right now.
		WalletAddress: req.WalletAddress,
		// Refund destination is recorded so an operator chasing a stranded
		// deposit knows where the money was supposed to come back to.
		PayoutBankCode:    req.RefundBankCode,
		PayoutInstitution: refundInst,
		PayoutAccount:     req.RefundAccount,
		PayoutAccountName: req.RefundAccountName,
	}
	if converting {
		row.RouteKind = models.RouteKindConverted
		row.RequestedChain = venue.RequestedChain
		row.RequestedToken = venue.RequestedToken
		row.RequestedChainID = venue.RequestedChainID
		row.RequestedTokenAddress = venue.RequestedAddress
		row.RequestedTokenDecimals = venue.RequestedDecimals
		// Where the delivery leg actually pays. Taken from the VENUE, never
		// from the request: the venue carries the recipient the quoted route
		// was priced and calldata-checked against, so an order can never be
		// opened pointing somewhere the route does not pay. Empty on every EVM
		// destination, where the user's own wallet is the recipient.
		row.DestinationAddress = firstNonEmpty(venue.RequestedDestination, req.WalletAddress)
		// T, what she is owed, comes from PAYCREST'S OWN delivered amount D —
		// not from the venue's quote — less the route's cost B′, the one
		// formula the quote priced her on (ConversionOwed).
		orderAmountBaseUnits, perr := DecimalToBaseUnits(order.Amount, tokenDecimalsFor(s.quotes.chainID(venue.ChainKey), venue.Token))
		if perr != nil {
			// An unreadable amount cannot be silently left empty: an empty
			// quoted_amount reads as a LEGACY converted order (bridge
			// everything, keep nothing back), which would hand the user the
			// whole delivery and the company the loss — or, worse, promise
			// something no leg can be checked against. Park it for a human
			// instead; the order exists upstream either way.
			log.Printf("[paycrest] ALARM paycrest converted order amount unreadable ref=%s amount=%q: %v — parked for an operator",
				ref, order.Amount, perr)
			row.Status = models.PaycrestStatusRecoveryPending
			row.Fault = models.FaultPlatform
			row.FailureReason = "Paycrest reported an amount we could not read, so what the buyer is owed could not be computed. Needs manual settlement."
		} else {
			row.QuotedAmount = ConversionOwed(orderAmountBaseUnits, venue.MarkupBps).String()
		}
		row.EstimatedAmount = venue.EstimatedAmount
		row.MarkupBps = venue.MarkupBps
		row.PromisedAmount = venue.PromisedAmount
		// Paycrest holds the money until it delivers to the treasury — see
		// the Sync doc comment for how that maps onto our own statuses. Set
		// explicitly rather than relying on the zero value: the orchestrator's
		// custody gate treats anything that is not HolderTreasury/HolderUser
		// as still needing custody, so an unset Holder would happen to work
		// today but is not the same thing as stating it.
		row.Holder = models.HolderPaycrest
	} else {
		// Deliberate, not required: leaving RouteKind at its zero value would
		// read identically (Converted() treats "" the same as "direct"), and
		// every row written before this field existed is already "". Stamping
		// it explicitly going forward is just a clearer row to read later.
		row.RouteKind = models.RouteKindDirect
	}
	if pa := order.ProviderAccount; pa != nil {
		row.PayInstitution = pa.Institution
		row.PayAccountNumber = pa.AccountIdentifier
		row.PayAccountName = pa.AccountName
		// PayAmount is ALWAYS the provider's figure, never our prediction — it
		// is what the user must transfer exactly, and being a kobo out is what
		// strands a payment.
		row.PayAmount = pa.AmountToTransfer
		row.ValidUntil = parsePaycrestTime(pa.ValidUntil)

		// We divided; they multiplied. Rounding at two decimals can leave the
		// two a kobo apart, and the user would then see "pay 800" beside
		// "transfer 800.01". Log the drift rather than hide it: the transfer
		// amount above is still correct, but the screen's two numbers stop
		// agreeing and someone should know why.
		if grossedDown {
			if want, err1 := strconv.ParseFloat(req.FiatAmount, 64); err1 == nil {
				if got, err2 := strconv.ParseFloat(pa.AmountToTransfer, 64); err2 == nil {
					if diff := got - want; diff > 0.01 || diff < -0.01 {
						log.Printf("[paycrest] gross-down drift ref=%s requested=%s submitted=%s provider_wants=%s (%.2f off)",
							ref, req.FiatAmount, submitAmount, pa.AmountToTransfer, diff)
					}
				}
			}
		}
	}
	// Paycrest has answered: the row must land even if the HTTP request was
	// cancelled meanwhile (the app gives up at 30s), or the order exists
	// upstream with no row.
	if err := s.db.WithContext(context.WithoutCancel(ctx)).Create(row).Error; err != nil {
		// The order exists at Paycrest but we failed to record it. Log loudly
		// with the reference: without this line the user could pay an account
		// nothing on our side knows about.
		log.Printf("[paycrest] ALARM created order %s (ref=%s) but could not persist it: %v",
			order.ID, ref, err)
		return nil, fmt.Errorf("%w: %w", ErrBuyAttemptMayExist, err)
	}
	return row, nil
}

// ─── Sell (offramp) ────────────────────────────────────────────────────────

// PaycrestSellRequest starts a sale. The user sends the stablecoin themselves
// from their connected wallet.
type PaycrestSellRequest struct {
	UserID uuid.UUID
	// MinRate/PriceGuard: the lowest rate this sell may bind at (ramp router only).
	MinRate    string
	PriceGuard PriceGuardMode
	// WalletAddress funds the sale and receives the refund if it fails.
	WalletAddress string
	FiatCode      string
	// FiatAmount is what the user wants to receive; the token amount is
	// derived from the live quote.
	FiatAmount string
	ChainKey   string
	Token      string
	// Payout bank, in OUR (NIBSS) vocabulary.
	BankCode    string
	AccountNo   string
	AccountName string
	// PayPaymentID names the TSA Pay payment this sale is an attempt of, whose
	// payout bank is the merchant's: the order is marked with it so the
	// customer's routes mask that bank (models.PaycrestOrderRecord.PayPaymentID).
	PayPaymentID *uuid.UUID
	// KeepSurplus: the payout is owed EXACTLY FiatAmount (a TSA Pay merchant is
	// paid what they invoiced), so the deposit's cushion above it is ours. The
	// order's sender fee is raised to take it, never below the configured fee,
	// leaving the merchant FiatAmount plus payMerchantMargin. Direct sells only.
	KeepSurplus bool
}

// CreateSell opens an offramp and returns the address the user must send to.
func (s *PaycrestOrderService) CreateSell(ctx context.Context, req PaycrestSellRequest) (*models.PaycrestOrderRecord, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		return nil, ErrSellNeedsWallet
	}
	if req.BankCode == "" || req.AccountNo == "" {
		return nil, ErrSellNeedsPayoutAccount
	}
	if err := s.assertNoOpenOrder(ctx, req.UserID, models.PaycrestDirectionSell); err != nil {
		return nil, err
	}

	fiat := strings.ToUpper(strings.TrimSpace(req.FiatCode))
	if fiat == "" {
		fiat = "NGN"
	}
	if err := s.requirePaycrestEnabled(fiat); err != nil {
		return nil, err
	}
	// Destination before price, for the same reason as the buy path — and here
	// it matters more: this is where the user's money is pointed somewhere, and
	// after the on-chain leg there is nothing to correct.
	payoutInst, payoutKind, err := s.paycrestBankCode(ctx, fiat, req.BankCode)
	if err != nil {
		return nil, err
	}
	if err := validatePayoutIdentifier(payoutKind, fiat, req.AccountNo); err != nil {
		return nil, err
	}

	venue, err := s.quotes.VenueFor(ctx, "sell", fiat, req.FiatAmount, req.ChainKey, req.Token)
	if err != nil {
		return nil, nothingSent(s.explainMissingVenue(ctx, "sell", fiat, err))
	}
	if err := checkPriceGuard("sell", venue.Rate, req.MinRate, req.PriceGuard); err != nil {
		return nil, err
	}

	// Paycrest prices an offramp in token units. venue.TokenAmount is sized at
	// the fee-inclusive rate, so the deposit covers our fee and the payout is
	// still the naira the user entered.
	tokenAmount := strconv.FormatFloat(venue.TokenAmount, 'f', 6, 64)

	return s.openOfframp(ctx, req, venue, tokenAmount, req.FiatAmount, payoutInst, s.senderFeePercent)
}

// CreateSellForToken opens an offramp for an EXACT token amount she already
// holds — the swapped sell (design §5.3), where the USDC on Base is a fact on
// chain and the naira follows from it, rather than a naira figure she typed
// that the token amount is derived from. It is sent at the company take, like
// every ramp order: the swap's cost was passed on at cost in her LiFi route.
func (s *PaycrestOrderService) CreateSellForToken(ctx context.Context, req PaycrestSellRequest, tokenAmount string) (*models.PaycrestOrderRecord, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	return s.sellForToken(ctx, req, tokenAmount, s.senderFeePercent, false)
}

// sellForToken is CreateSellForToken at an explicit sender fee: the take for
// a ramp sale, a TSA Pay payout's own for CreateSellForFiat. keepSurplus opens
// a KeepSurplus order with fee as its base (a TSA Pay direct payout). It
// shares openOfframp with CreateSell rather than repeating it — one place
// opens an offramp and writes its row, whichever direction sized it.
func (s *PaycrestOrderService) sellForToken(ctx context.Context, req PaycrestSellRequest, tokenAmount, fee string, keepSurplus bool) (*models.PaycrestOrderRecord, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	if strings.TrimSpace(req.WalletAddress) == "" {
		return nil, ErrSellNeedsWallet
	}
	if req.BankCode == "" || req.AccountNo == "" {
		return nil, ErrSellNeedsPayoutAccount
	}
	// Checked HERE, where the order is opened, not only when the sale began:
	// a direct sell opened in between must still hold the one live sell.
	if err := s.assertNoOpenOrder(ctx, req.UserID, models.PaycrestDirectionSell); err != nil {
		return nil, err
	}
	amount, ok := parsePositiveDecimal(tokenAmount)
	if !ok {
		return nil, fmt.Errorf("paycrest: unusable sell amount %q", tokenAmount)
	}
	feeVal, ok := parseSenderFeePercent(fee)
	if !ok {
		// Sent with no usable fee, Paycrest would apply its dashboard default,
		// which is not the fee she was quoted.
		return nil, ErrConversionUnavailable
	}

	fiat := strings.ToUpper(strings.TrimSpace(req.FiatCode))
	if fiat == "" {
		fiat = "NGN"
	}
	if err := s.requirePaycrestEnabled(fiat); err != nil {
		return nil, err
	}
	payoutInst, payoutKind, err := s.paycrestBankCode(ctx, fiat, req.BankCode)
	if err != nil {
		return nil, err
	}
	if err := validatePayoutIdentifier(payoutKind, fiat, req.AccountNo); err != nil {
		return nil, err
	}

	// Paycrest quotes by order SIZE, so it has to be asked about roughly the
	// right one. The mid rate only lands us in the right tier; the rate that
	// binds is whatever the venue comes back with.
	market, err := s.quotes.cachedMarketRate(ctx, fiat, "sell")
	if err != nil {
		return nil, nothingSent(err)
	}
	venue, err := s.quotes.VenueForFee(ctx, "sell", fiat,
		strconv.FormatFloat(amount*market, 'f', 2, 64), req.ChainKey, req.Token, feeVal)
	if err != nil {
		return nil, nothingSent(s.explainMissingVenue(ctx, "sell", fiat, err))
	}
	// What she is actually owed: the tokens the chain proved, at the rate the
	// venue just quoted. Never the other way round, and never through a
	// float64 — fiatAt rounds it exactly the way her quote was rounded.
	rate, ok := new(big.Rat).SetString(strings.TrimSpace(venue.Rate))
	if _, plain := parsePositiveDecimal(venue.Rate); !ok || !plain {
		return nil, fmt.Errorf("paycrest: unusable sell rate %q", venue.Rate)
	}
	tokens, ok := new(big.Rat).SetString(strings.TrimSpace(tokenAmount))
	if !ok {
		return nil, fmt.Errorf("paycrest: unusable sell amount %q", tokenAmount)
	}
	fiatAmount := fiatAt(tokens, rate)
	if stated := strings.TrimSpace(req.FiatAmount); stated != "" {
		// A sale that owes an exact naira figure (a TSA Pay checkout) records
		// that figure, not whatever tokens × rate rounds to; CreateSellForFiat
		// then proves the order's payout actually covers it.
		fiatAmount = stated
		if owed, ok := new(big.Rat).SetString(stated); ok && keepSurplus {
			return s.openKeepSurplusOfframp(ctx, req, venue, tokens, tokenAmount, fiatAmount, payoutInst, fee, owed)
		}
	}

	return s.openOfframp(ctx, req, venue, tokenAmount, fiatAmount, payoutInst, fee)
}

// openKeepSurplusOfframp opens a KeepSurplus order at the fee that leaves the
// merchant exactly what they invoiced (see keepSurplusFeePercent). If Paycrest
// refuses that fee outright (a 4xx — nothing was created) it is opened once
// more at the configured fee, so a Paycrest that will not take a finely-stepped
// percent costs us the cushion, not the payment. Every choice is logged by the
// order's reference, beside any refusal CreateSellForFiat logs for it.
func (s *PaycrestOrderService) openKeepSurplusOfframp(ctx context.Context, req PaycrestSellRequest, venue *PaycrestVenue,
	tokens *big.Rat, tokenAmount, fiatAmount, payoutInst, base string, owed *big.Rat) (*models.PaycrestOrderRecord, error) {
	fee, capped := keepSurplusFeePercent(tokens, venue.RawRate, base, owed)
	if capped {
		log.Printf("[paycrest] ALERT keep_surplus_fee_capped_total=1 owed=%s deposit=%s quoted_rate=%s fee=%s%%: the deposit is larger than the invoice needs; the excess goes to the merchant",
			fiatAmount, tokenAmount, venue.RawRate, fee)
	}
	row, err := s.openOfframp(ctx, req, venue, tokenAmount, fiatAmount, payoutInst, fee)
	if err != nil && fee != base && paycrestRejected(err) {
		log.Printf("[paycrest] ALARM keep_surplus_fee_rejected_total=1 fee=%s%% err=%s — reopening at the configured %s%%",
			fee, RedactAlertText(err.Error()), base)
		fee = base
		row, err = s.openOfframp(ctx, req, venue, tokenAmount, fiatAmount, payoutInst, fee)
	}
	if err != nil {
		return nil, err
	}
	log.Printf("[paycrest] keep_surplus ref=%s owed=%s deposit=%s quoted_rate=%s fee_sent=%s%% fee_charged=%s%% base=%s%%",
		row.Reference, fiatAmount, tokenAmount, venue.RawRate, fee, row.SenderFeePercent, base)
	return row, nil
}

// openOfframp is the single offramp-creation path: it sends the order and
// writes the row. Everything above it decides HOW MUCH; this decides nothing
// about money at all, which is why both directions can share it.
func (s *PaycrestOrderService) openOfframp(ctx context.Context, req PaycrestSellRequest, venue *PaycrestVenue,
	tokenAmount, fiatAmount, payoutInst, senderFee string) (*models.PaycrestOrderRecord, error) {
	ref := paycrestReference("PCS", req.UserID)
	order, err := s.pc.CreateOfframpOrder(ctx, PaycrestOfframpRequest{
		AmountToken: tokenAmount,
		Token:       venue.Token,
		Network:     venue.Network,
		FiatCode:    fiatCodeOf(req),
		Recipient: PaycrestBankAccount{
			Institution:       payoutInst,
			AccountIdentifier: req.AccountNo,
			AccountName:       req.AccountName,
		},
		// Refunds return to the user's own wallet — the same one they send
		// from — so a failed sale leaves them exactly where they started.
		RefundAddress:    req.WalletAddress,
		Reference:        ref,
		SenderFeePercent: senderFee,
		SenderFeeAddress: s.senderFeeAddress,
	})
	if err != nil {
		return nil, markCreateErr(err)
	}

	feePercent := s.chargedFeePercent(order, ref, senderFee)
	row := &models.PaycrestOrderRecord{
		UserID:            req.UserID,
		Direction:         models.PaycrestDirectionSell,
		Status:            models.PaycrestStatusAwaitingDeposit,
		Reference:         ref,
		PaycrestID:        order.ID,
		FiatCode:          fiatCodeOf(req),
		FiatAmount:        fiatAmount,
		Token:             venue.Token,
		ChainKey:          venue.ChainKey,
		Network:           venue.Network,
		Rate:              userRate("sell", order.Rate, feePercent),
		RawRate:           storableRawRate(order.Rate),
		TokenAmount:       tokenAmount,
		ProviderID:        paycrestProviderOf(order, venue),
		SenderFee:         order.SenderFee,
		SenderFeePercent:  feePercent,
		WalletAddress:     req.WalletAddress,
		PayoutBankCode:    req.BankCode,
		PayoutInstitution: payoutInst,
		PayoutAccount:     req.AccountNo,
		PayoutAccountName: req.AccountName,
		PayPaymentID:      req.PayPaymentID,
	}
	if pa := order.ProviderAccount; pa != nil {
		row.DepositAddress = pa.ReceiveAddress
		row.ValidUntil = parsePaycrestTime(pa.ValidUntil)
	}
	// Paycrest has answered: the row must land even if the HTTP request was
	// cancelled meanwhile (the app gives up at 30s), or the order exists
	// upstream with no row.
	if err := s.db.WithContext(context.WithoutCancel(ctx)).Create(row).Error; err != nil {
		log.Printf("[paycrest] ALARM created offramp %s (ref=%s) but could not persist it: %v",
			order.ID, ref, err)
		// Paycrest has an order we hold no row for: a caller that pairs the
		// sale with something (a TSA Pay payment) must not treat it as "nothing
		// was opened".
		return nil, fmt.Errorf("%w: %v", ErrSellAttemptMayExist, err)
	}
	return row, nil
}

// fiatCodeOf is the order's currency, defaulted once.
func fiatCodeOf(req PaycrestSellRequest) string {
	fiat := strings.ToUpper(strings.TrimSpace(req.FiatCode))
	if fiat == "" {
		return "NGN"
	}
	return fiat
}

// ─── Progress ──────────────────────────────────────────────────────────────

// Sync re-reads one order from Paycrest and moves our row forward. Idempotent
// and safe to call from both the poller and a user refresh.
func (s *PaycrestOrderService) Sync(ctx context.Context, row *models.PaycrestOrderRecord) (*models.PaycrestOrderRecord, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	if row == nil || row.PaycrestID == "" {
		return row, nil
	}
	if models.PaycrestOrderTerminal(row.Status) {
		return row, nil // nothing upstream can change it
	}

	remote, err := s.pc.GetOrder(ctx, row.PaycrestID)
	if err != nil {
		// A 404 shortly after creation is read-after-write lag; later it means
		// the order never existed. Either way, do not mutate the row on a read
		// failure — the poller will come back.
		return row, err
	}

	now := time.Now()
	updates := map[string]any{"last_checked_at": &now}
	if remote.AmountPaid != "" {
		updates["amount_paid"] = remote.AmountPaid
	}
	if remote.AmountReturned != "" {
		updates["amount_returned"] = remote.AmountReturned
	}
	if h := latestPaycrestTxHash(remote); h != "" {
		updates["tx_hash"] = h
	}

	next := mapPaycrestStatus(remote, row.Direction, row.PaymentClaimedAt != nil)
	switch {
	case row.Status == models.PaycrestStatusRecoveryPending:
		// Operator-owned. It is deliberately NOT terminal (the orchestrator
		// and an admin both still act on it), so DueForSync keeps selecting
		// the row — without this, the very next poll would write Paycrest's
		// own view over a status only a human may resolve.
		next = ""
	case row.Converted():
		switch {
		case row.Holder != models.HolderPaycrest:
			// Custody has already moved off Paycrest (to the treasury, then
			// eventually the user); the conversion orchestrator now owns this
			// order's status. DueForSync still selects it — converting and
			// refunding are both non-terminal — so without this guard the next
			// poll would resurrect Paycrest's own (by now stale) "settled" over
			// whatever the orchestrator has since written.
			next = ""
		case next == models.PaycrestStatusSettled:
			// Paycrest delivered to the TREASURY, not the user — the order is
			// not done, it has only started converting.
			next = models.PaycrestStatusConverting
		}
	}
	if next == models.PaycrestStatusUnmatched {
		// The user paid, the bank confirmed, and the protocol never saw it.
		// Loud, because nothing automated can resolve this.
		log.Printf("[paycrest] ALARM unmatched deposit order=%s ref=%s user=%s amount=%s %s "+
			"— funds are at the provider's bank with no API remedy; manual recovery required",
			remote.ID, row.Reference, row.UserID, row.PayAmount, row.FiatCode)
		updates["failure_reason"] = "Payment reached the provider but was never registered against the order. Manual recovery required."
	}
	if next != "" && next != row.Status {
		updates["status"] = next
		if next == models.PaycrestStatusSettled {
			updates["settled_at"] = &now
		}
	}

	if err := s.db.WithContext(ctx).Model(row).Updates(updates).Error; err != nil {
		return row, err
	}
	return s.Get(ctx, row.UserID, row.ID)
}

// ClaimPayment records that the user asserts they completed their side of
// the order — the bank transfer on a buy, the on-chain send on a sell —
// after tapping "I've sent the money"/"I've sent it". Scoped to the caller's
// own order via Get's ownership check (returns ErrPaycrestOrderNotYours
// otherwise) and idempotent: a retried tap, or one that lands after another
// request already recorded the claim, is a no-op past the first call, and
// the original claim time is preserved rather than bumped forward.
//
// Deliberately does NOT touch Status. Only mapPaycrestStatus (via Sync) moves
// an order's status; this just leaves evidence for the next Sync to read.
func (s *PaycrestOrderService) ClaimPayment(ctx context.Context, userID, orderID uuid.UUID) (*models.PaycrestOrderRecord, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	row, err := s.Get(ctx, userID, orderID)
	if err != nil {
		return nil, err
	}
	if IsITConversion(row) {
		return nil, ErrPaycrestNotClaimable // paid through its purchase
	}
	if row.PaymentClaimedAt != nil {
		return row, nil
	}
	now := time.Now()
	if err := s.db.WithContext(ctx).Model(&models.PaycrestOrderRecord{}).
		Where("id = ? AND user_id = ? AND payment_claimed_at IS NULL", orderID, userID).
		Update("payment_claimed_at", &now).Error; err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, orderID)
}

// mapPaycrestStatus translates their vocabulary into ours, and detects the one
// state they have no word for.
//
// claimed is row.PaymentClaimedAt != nil — whether THIS user tapped "I've
// sent the money"/"I've sent it" on THIS order. It has to come from our row,
// not from PaycrestOrder: that type is only Paycrest's API response and
// carries no memory of what our app showed the user. o.UnmatchedDeposit()
// reports a fact about their response (expired + amountPaid=0); whether that
// fact means "genuine unmatched deposit, page an operator" or "the user
// abandoned this before paying, which looks identical to Paycrest" is a
// judgment only our claim record can make — so the gate lives here, at the
// point where both pieces of information are in scope, rather than on
// PaycrestOrder itself.
func mapPaycrestStatus(o *PaycrestOrder, direction string, claimed bool) string {
	if o == nil {
		return ""
	}
	if o.UnmatchedDeposit() && claimed {
		return models.PaycrestStatusUnmatched
	}
	switch strings.ToLower(strings.TrimSpace(o.Status)) {
	case PaycrestStatusSettled:
		return models.PaycrestStatusSettled
	case PaycrestStatusSettling:
		return models.PaycrestStatusSettling
	case PaycrestStatusRefunded:
		return models.PaycrestStatusRefunded
	case PaycrestStatusExpired:
		return models.PaycrestStatusExpired
	case PaycrestStatusCancelled:
		return models.PaycrestStatusFailed
	case PaycrestStatusPending:
		// "pending" covers both "waiting for the user" and "provider assigned,
		// funds seen". amountPaid is what separates them.
		if paid := strings.TrimSpace(o.AmountPaid); paid != "" && paid != "0" {
			return models.PaycrestStatusPending
		}
		if direction == models.PaycrestDirectionSell {
			return models.PaycrestStatusAwaitingDeposit
		}
		return models.PaycrestStatusAwaitingPayment
	}
	return ""
}

// ─── Reads ─────────────────────────────────────────────────────────────────

// Get fetches one of the user's own orders.
func (s *PaycrestOrderService) Get(ctx context.Context, userID, orderID uuid.UUID) (*models.PaycrestOrderRecord, error) {
	var row models.PaycrestOrderRecord
	err := s.db.WithContext(ctx).
		Where("id = ? AND user_id = ?", orderID, userID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPaycrestOrderNotYours
	}
	return &row, err
}

// Cancel abandons an order the user has not paid or funded yet.
//
// It is local by necessity: Paycrest has no cancel endpoint (all 404 — see the
// PaycrestClient doc comment), so their copy of the order stays live until its
// own window closes. What this actually buys the user is the one-open-order
// lock in assertNoOpenOrder, which otherwise holds them for the full 30-minute
// validUntil before the poller can mark the order expired.
//
// Cancelling is refused once PaymentClaimedAt is set. That claim is the user
// saying money is on its way to a provider address; marking the order dead
// underneath it would tell them nothing is coming while the deposit is still
// in flight — the stranded-funds case that has no recovery path.
//
// The write is status-guarded rather than read-then-write so a poller sync (or
// a second tap) landing in the same instant wins, and this call reports the
// truth: the order is no longer cancellable.
func (s *PaycrestOrderService) Cancel(ctx context.Context, userID, orderID uuid.UUID) (*models.PaycrestOrderRecord, error) {
	return s.cancelOrder(ctx, userID, orderID, map[string]any{"failure_reason": "cancelled by user"})
}

// cancelOrder is Cancel's guarded write, with the columns that go in the same
// UPDATE as the status supplied by the caller — so a cancel that must also
// withdraw something (a short order's deposit address) cannot be half done.
func (s *PaycrestOrderService) cancelOrder(ctx context.Context, userID, orderID uuid.UUID, alsoSet map[string]any) (*models.PaycrestOrderRecord, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	row, err := s.Get(ctx, userID, orderID)
	if err != nil {
		return nil, err
	}
	// An IT-settled converted buy's record follows its IT purchase, which is
	// cancelled from its own order screen.
	if IsITConversion(row) || row.PaymentClaimedAt != nil {
		return nil, ErrPaycrestNotCancellable
	}

	updates := map[string]any{"status": models.PaycrestStatusCancelled}
	for k, v := range alsoSet {
		updates[k] = v
	}
	res := s.db.WithContext(ctx).Model(&models.PaycrestOrderRecord{}).
		Where("id = ? AND user_id = ? AND payment_claimed_at IS NULL AND status IN ?",
			orderID, userID, []string{
				models.PaycrestStatusAwaitingPayment,
				models.PaycrestStatusAwaitingDeposit,
			}).
		Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrPaycrestNotCancellable
	}
	return s.Get(ctx, userID, orderID)
}

// List returns the user's recent orders, newest first.
func (s *PaycrestOrderService) List(ctx context.Context, userID uuid.UUID, limit int) ([]models.PaycrestOrderRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	var rows []models.PaycrestOrderRecord
	// A TSA Pay payment's order is how the payment moves, not a sale of hers:
	// listed here it read as "Sell USDC — paid to your bank" when the money
	// went to the merchant. Her payments are listed as payments (GET /pay/history).
	// An IT-settled converted buy's conversion record (ITC-) is not listed
	// either: the IT purchase is her list entry.
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND pay_payment_id IS NULL AND reference NOT LIKE ?", userID, ITConversionRefPrefix+"%").
		Order("created_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// DueForSync selects non-terminal orders for the poller, oldest check first.
func (s *PaycrestOrderService) DueForSync(ctx context.Context, limit int) ([]models.PaycrestOrderRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	// Shared with PaycrestOrderTerminal: a status listed in one and not the
	// other lets the poller refresh a row Sync considers settled — which for a
	// user-cancelled order means overwriting our status with Paycrest's still-
	// live copy and re-locking the user out of the direction.
	terminal := models.PaycrestTerminalStatuses()
	var rows []models.PaycrestOrderRecord
	err := s.db.WithContext(ctx).
		Where("status NOT IN ? AND paycrest_id <> ''", terminal).
		Order("last_checked_at ASC NULLS FIRST").Limit(limit).Find(&rows).Error
	return rows, err
}

// ─── Helpers ───────────────────────────────────────────────────────────────

// assertNoOpenOrder keeps one live order per direction per user.
func (s *PaycrestOrderService) assertNoOpenOrder(ctx context.Context, userID uuid.UUID, direction string) error {
	open := []string{
		models.PaycrestStatusAwaitingPayment, models.PaycrestStatusAwaitingDeposit,
		models.PaycrestStatusPending, models.PaycrestStatusSettling,
	}
	// COUNT(pay_payment_id) counts only the payments' orders.
	var n struct{ OpenCount, PayCount int64 }
	if err := s.db.WithContext(ctx).Model(&models.PaycrestOrderRecord{}).
		Select("COUNT(*) AS open_count, COUNT(pay_payment_id) AS pay_count").
		// An IT-settled converted buy's record is not a Paycrest order: it
		// never holds her to one Paycrest order per direction.
		Where("user_id = ? AND direction = ? AND status IN ? AND reference NOT LIKE ?",
			userID, direction, open, ITConversionRefPrefix+"%").
		Scan(&n).Error; err != nil {
		return err
	}
	switch {
	case n.OpenCount == 0:
		return nil
	case n.OpenCount > n.PayCount:
		// An ordinary order of hers wins: that one she can find and cancel.
		return ErrPaycrestOrderOpen
	default:
		return ErrPaycrestPayOrderOpen
	}
}

// paycrestBankCode resolves the bank/institution code the app sent into
// Paycrest's own vocabulary, refusing rather than guessing — a wrong
// institution sends someone's payout to another bank.
//
// NGN is the only currency whose picker speaks NIBSS codes (see
// PaycrestDirectory.Institutions, which predates Paycrest and still matches
// the app's ngBanks.ts list) — so only NGN needs translating through the
// NIBSS<->Paycrest map. Every other currency's picker already sent Paycrest's
// own code straight from that same Institutions() call, so running it through
// the NIBSS map here would look up a Paycrest code as if it were a NIBSS one
// and always miss. For those currencies, confirm the code is a real
// institution for this fiat (via the same Institutions() the picker used) and
// pass it through unchanged.
// Returns the provider's code AND the destination kind, because the kind
// decides what a valid identifier looks like and this is the only place that
// already knows it.
func (s *PaycrestOrderService) paycrestBankCode(ctx context.Context, fiat, code string) (string, string, error) {
	if s.dir == nil {
		return "", "", ErrPaycrestDisabled
	}
	code = strings.TrimSpace(code)
	if strings.ToUpper(strings.TrimSpace(fiat)) != "NGN" {
		institutions, err := s.dir.Institutions(ctx, fiat)
		if err != nil {
			return "", "", nothingSent(err)
		}
		for _, inst := range institutions {
			if inst.Code == code {
				return code, inst.Type, nil
			}
		}
		return "", "", fmt.Errorf("%w (bank %s)", ErrPaycrestBankUnsupported, code)
	}
	m, err := s.dir.BankMap(ctx, fiat)
	if err != nil {
		return "", "", nothingSent(err)
	}
	paycrestCode, ok := m.NIBSSToPaycrest[code]
	if !ok {
		return "", "", fmt.Errorf("%w (bank %s)", ErrPaycrestBankUnsupported, code)
	}
	// NIBSS is a bank registry; every code in it takes a NUBAN account number.
	return paycrestCode, PaycrestInstitutionBank, nil
}

// msisdnPattern and nubanPattern are the only two identifier shapes we can
// state with confidence. Everything else stays "non-empty" rather than being
// forced through a rule we would be guessing at.
var (
	msisdnPattern = regexp.MustCompile(`^\d{9,15}$`)
	nubanPattern  = regexp.MustCompile(`^\d{10}$`)
)

// validatePayoutIdentifier checks the account identifier against the KIND of
// destination it is going to.
//
// The app already asks for the right thing per destination, but the app is not
// a trust boundary: an older build, or any direct API caller, can post a
// 10-digit bank account number against an MTN mobile-money code. The payout
// then fails at the provider AFTER the on-chain leg has moved, which is the
// one failure this whole area exists to prevent — and a short-but-real number
// does not fail at all, it pays a stranger.
func validatePayoutIdentifier(kind, fiat, identifier string) error {
	id := strings.TrimSpace(identifier)
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case PaycrestInstitutionMobileMoney:
		if !msisdnPattern.MatchString(id) {
			return fmt.Errorf("%w: a mobile money destination needs a phone number (9-15 digits)",
				ErrPaycrestAccountShape)
		}
	default:
		// NUBAN is the one bank format we know. Other currencies' account
		// numbers vary by country and bank, so requiring a length there would
		// reject real accounts — non-empty is enforced by the caller.
		if strings.EqualFold(strings.TrimSpace(fiat), "NGN") && !nubanPattern.MatchString(id) {
			return fmt.Errorf("%w: a Nigerian bank account number is 10 digits",
				ErrPaycrestAccountShape)
		}
	}
	return nil
}

// paycrestReference mints our idempotency key. The user id is embedded so an
// operator reading a Paycrest dashboard row can identify the customer without
// our database.
func paycrestReference(prefix string, userID uuid.UUID) string {
	return fmt.Sprintf("%s-%s-%s", prefix,
		strings.ReplaceAll(userID.String(), "-", "")[:12],
		strings.ReplaceAll(uuid.NewString(), "-", "")[:10])
}

// paycrestProviderOf prefers the provider the order actually landed on, and
// falls back to the quote's first ranked provider.
func paycrestProviderOf(o *PaycrestOrder, v *PaycrestVenue) string {
	if o != nil {
		var dest struct {
			ProviderID string `json:"providerId"`
		}
		if len(o.Destination) > 0 {
			_ = json.Unmarshal(o.Destination, &dest)
		}
		if dest.ProviderID != "" {
			return dest.ProviderID
		}
	}
	if v != nil && len(v.ProviderIDs) > 0 {
		return v.ProviderIDs[0]
	}
	return ""
}

// latestPaycrestTxHash returns the most recent on-chain hash in the audit log.
func latestPaycrestTxHash(o *PaycrestOrder) string {
	for i := len(o.TransactionLogs) - 1; i >= 0; i-- {
		if h := strings.TrimSpace(o.TransactionLogs[i].TxHash); h != "" {
			return h
		}
	}
	return ""
}

// parsePaycrestTime accepts the RFC3339 variants their API mixes (with and
// without sub-second precision). An unparseable value yields nil rather than a
// zero time, so a bad expiry never reads as "already expired".
func parsePaycrestTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}

// ─── Webhooks ──────────────────────────────────────────────────────────────

// ErrPaycrestOrderUnknown means an event named an order we have no row for.
// Not retryable: a redelivery cannot make the row appear, and the likeliest
// cause is an order created by another environment sharing the API key.
var ErrPaycrestOrderUnknown = errors.New("paycrest: webhook names an order we do not have")

// HandleWebhook advances an order from a verified webhook.
//
// The event is a HINT, never an authority. It tells us which order moved; Sync
// then re-reads that order from the API and decides. That matters here more
// than usual: the failure this integration must never mishandle is an expired
// order whose deposit was never registered, and only the API's amountPaid can
// distinguish it from an ordinary expiry.
//
// Looking up by OUR reference rather than their order id is deliberate — the
// reference exists on our row from the moment we create it, whereas their id
// only arrives with the response, so a webhook racing our own persist still
// finds its order.
func (s *PaycrestOrderService) HandleWebhook(ctx context.Context, ev *PaycrestWebhookEvent) error {
	if s == nil {
		return ErrPaycrestDisabled
	}
	if ev == nil {
		return errors.New("paycrest webhook: nil event")
	}

	var row models.PaycrestOrderRecord
	q := s.db.WithContext(ctx)
	var err error
	switch {
	case strings.TrimSpace(ev.Reference) != "":
		err = q.Where("reference = ?", ev.Reference).First(&row).Error
	case strings.TrimSpace(ev.OrderID) != "":
		err = q.Where("paycrest_id = ?", ev.OrderID).First(&row).Error
	default:
		return errors.New("paycrest webhook: event carries no identifier")
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w (ref=%q id=%q)", ErrPaycrestOrderUnknown, ev.Reference, ev.OrderID)
	}
	if err != nil {
		return err
	}

	// An event can arrive before our own create call has finished persisting
	// the provider's id. Fill it in so the poller and any later event agree on
	// which remote order this row is.
	if row.PaycrestID == "" && strings.TrimSpace(ev.OrderID) != "" {
		row.PaycrestID = ev.OrderID
		if err := s.db.WithContext(ctx).Model(&row).
			Update("paycrest_id", ev.OrderID).Error; err != nil {
			return err
		}
	}
	if models.PaycrestOrderTerminal(row.Status) {
		return nil // nothing an event can add
	}

	_, err = s.Sync(ctx, &row)
	return err
}
