package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"golang.org/x/sync/singleflight"
)

// paycrest_quote.go — venue selection.
//
// Paycrest liquidity is not a fixed property of a pair. It belongs to
// individual providers who join and leave the book without notice, and who
// tier their pricing by order size. Measured over two consecutive days:
//
//	2026-09-03  USDT/base quoted +0.24% flat from $0.50 to $2,000
//	2026-09-04  USDT/base had NO buy provider at all; USDC/base quoted +1.12%
//	            at $0.58 but +0.25% at $364
//
// So a static "we support X on Y" list is a promise we cannot keep, and a
// cached rate is a price we cannot honour. Every quote therefore asks the API
// what is actually fillable AT THIS SIZE, and the order is created against
// that same answer.
//
// The user-visible consequence: the network picker only ever shows networks
// that can really fill the amount typed, so "no provider" is never discovered
// after the user has committed.

// PaycrestVenue is one fillable (token, network) with its live price.
type PaycrestVenue struct {
	// ChainKey is OUR key ("bsc"), not Paycrest's ("bnb-smart-chain") — the
	// app and blockchainService both speak this one.
	ChainKey string `json:"chain"`
	Network  string `json:"-"` // Paycrest's name, for the order payload
	Token    string `json:"token"`
	// Rate is fiat per token, as quoted.
	Rate string `json:"rate"`
	// RawRate is the provider's own rate for this size before any sender
	// fee, exactly as it quoted it — what Rate was priced from. Set by Quote
	// and VenueForFee.
	RawRate string `json:"-"`
	// SpreadPercent is Rate against the protocol's market reference. Positive
	// means the user pays above mid on a buy; negative means they receive
	// below mid on a sell. Surfaced so the app can show a real cost.
	SpreadPercent float64  `json:"spreadPercent"`
	ProviderIDs   []string `json:"-"`
	// TokenAmount is what the user receives (buy) or must send (sell).
	TokenAmount float64 `json:"tokenAmount"`
	// TokenAmountBaseUnits is TokenAmount as an exact integer in the token's
	// own base units, computed straight from the fiat amount and rate DECIMAL
	// STRINGS (see venueBaseUnits) rather than round-tripped through the
	// float64 above. A converted-buy promise is integer maths throughout, so
	// this is the figure that anchors it; empty when decimals could not be
	// resolved (no chain registry wired — direct quoting never needs this).
	TokenAmountBaseUnits string `json:"-"`

	// ── Conversion (Phase 2, Task 8). Zero-value/empty for a direct venue. ──

	// Kind is models.RouteKindDirect or models.RouteKindConverted, so the app
	// (and the order-creation path in Task 9) can tell the two apart.
	Kind string `json:"kind,omitempty"`
	// ViaChain/ViaToken are the Paycrest settlement leg a converted venue
	// actually creates the order against — same as ChainKey/Token, restated
	// under their own name so a converted venue can say what it converts
	// FROM without the caller confusing it with what the user asked for.
	ViaChain string `json:"viaChain,omitempty"`
	ViaToken string `json:"viaToken,omitempty"`
	// RequestedChain/RequestedToken are what the user actually asked for —
	// what the LiFi route delivers, and what Task 9's order must eventually
	// pay out. Both are DERIVED: the chain key from the chain id, the symbol
	// from LiFi's own toToken (or config, for a stablecoin) — never echoed
	// back from the request.
	RequestedChain string `json:"requestedChain,omitempty"`
	RequestedToken string `json:"requestedToken,omitempty"`
	// RequestedChainID/RequestedAddress/RequestedDecimals identify that asset
	// exactly, which the symbol pair above cannot. The order carries them
	// through so execution never has to re-guess what was bought.
	RequestedChainID  int64  `json:"requestedChainId,omitempty"`
	RequestedAddress  string `json:"requestedTokenAddress,omitempty"`
	RequestedDecimals int    `json:"requestedTokenDecimals,omitempty"`
	// RequestedDestination is where this route was actually priced to deliver,
	// when that is not the user's EVM wallet — her Solana address. Carried on
	// the venue rather than re-derived at order time so the order can only
	// ever record the recipient the quoted route was checked against.
	RequestedDestination string `json:"requestedDestination,omitempty"`
	// EstimatedAmount is what the route is expected to deliver, in the
	// requested token's own base units. An estimate, not a promise — a
	// volatile destination has no PromisedAmount at all.
	EstimatedAmount string `json:"estimatedAmount,omitempty"`
	// MarkupBps is B, the round-trip budget: the slice of the sender fee this
	// order keeps back to pay for its own conversion.
	MarkupBps int `json:"markupBps,omitempty"`
	// CostBps is what the priced route actually costs (LiFi's own cost, gas
	// excluded), so a venue can be compared against the budget it was offered
	// under.
	CostBps int `json:"costBps,omitempty"`
	// PromisedAmount is the guaranteed delivery, in the REQUESTED token's own
	// base units (destination decimals) — never the settlement leg's. See
	// ConvertedVenues for why: source and destination decimals differ in
	// practice (BSC stables are 18dp; base/polygon are 6dp), and the
	// orchestrator that executes these orders (Task 7) compares everything in
	// destination units.
	PromisedAmount string `json:"promisedAmount,omitempty"`
	// ETASeconds is the LiFi route's estimated execution time.
	ETASeconds int `json:"etaSeconds,omitempty"`
}

// PaycrestQuoteResult is the answer to "what can this user actually do with
// this amount right now".
type PaycrestQuoteResult struct {
	FiatCode   string          `json:"currency"`
	FiatAmount string          `json:"amount"`
	Side       string          `json:"side"` // "buy" (onramp) or "sell" (offramp)
	MarketRate float64         `json:"marketRate"`
	Venues     []PaycrestVenue `json:"venues"`
	// Best is Venues[0] when any exist — cheapest for the user.
	Best     *PaycrestVenue `json:"best"`
	QuotedAt time.Time      `json:"quotedAt"`
}

// ErrPaycrestNoVenue means nothing on the book can fill this size right now.
// Distinct from a transport failure: the user should be told to try a
// different amount, not that something broke.
var ErrPaycrestNoVenue = errors.New("paycrest: no network can fill this amount right now")

// ErrPaycrestBelowMinimum is the protocol floor, checked before we spend any
// API calls on an amount nobody will quote.
var ErrPaycrestBelowMinimum = errors.New("paycrest: amount is below the minimum order size")

// ErrPaycrestLegClosed means this currency does not trade in this direction at
// all — not that the book is thin for the requested size. UGX and TZS have
// offramp providers and no onramp, and no amount the user types will change
// that, so it must not be reported as the retryable "no venue" case.
var ErrPaycrestLegClosed = errors.New("paycrest: this currency does not trade this way")

// ErrPaycrestBookEmpty means the order book parsed successfully and contained
// nothing we can use. It is deliberately an ERROR rather than "no currency
// trades", because those two are indistinguishable in the data and wildly
// different in likelihood: every provider on the protocol going dark at once
// has never happened, while a renamed JSON field or an upstream blip produces
// exactly this and produces it silently.
//
// Callers that would otherwise read the empty result as "nothing trades" must
// treat this as "we could not find out" — see ITHandler.Currencies.
var ErrPaycrestBookEmpty = errors.New("paycrest: order book has no usable entries")

// PaycrestQuoter is the rate surface the quote service needs.
type PaycrestQuoter interface {
	// Markets is the whole order book in one fast call — the venue list comes
	// from here.
	Markets(ctx context.Context) (*PaycrestMarkets, error)
	// Rates is the authoritative per-order price. Slow and
	// concurrency-throttled upstream, so it is spent on ONE venue at order
	// time, never fanned out.
	Rates(ctx context.Context, network, token string, amount float64, fiat string) (*PaycrestRates, error)
	MarketRate(ctx context.Context, fiat, side string) (float64, error)
}

// PaycrestQuoteService prices and selects venues.
type PaycrestQuoteService struct {
	pc PaycrestQuoter
	// tokens we are willing to route, in preference order. USDC first: it has
	// consistently carried far deeper onramp liquidity than USDT on the NGN
	// corridor ($15,544 vs $0 on Base when last measured).
	tokens []string
	// chainKeys we will offer, in OUR vocabulary.
	chainKeys []string
	// senderFeePercent is our margin, priced into every rate we quote. Zero
	// quotes provider rates verbatim.
	senderFeePercent float64
	// roundTripBudgetBps is B, pushed here by
	// PaycrestOrderService.SetRoundTripBudgetBps the same way SetSenderFeePercent
	// pushes F, so the two services can never disagree on it.
	roundTripBudgetBps int
	// convertedFeeMaxByChain/convertedFeeMaxDefault cap the TOTAL fee a
	// converted buy or swapped sell may charge once a dear route raises its
	// budget above B. Unset (convertedFeeCapsSet false) applies
	// defaultConvertedFeeMaxBps. See SetConvertedFeeCaps.
	convertedFeeMaxByChain map[string]int
	convertedFeeMaxDefault int
	convertedFeeCapsSet    bool

	marketMu   sync.Mutex
	marketRate map[string]marketRateEntry

	bookMu     sync.Mutex
	book       *PaycrestMarkets
	bookAt     time.Time
	bookFailAt time.Time
	bookSF     singleflight.Group

	// ── Converted-buy pricing (Phase 2, Task 8). All dormant until wired: a
	// nil lifi/chains or an empty treasuryAddress makes ConvertedVenues
	// return no venues rather than erroring — a converted buy is an added
	// capability, never a requirement for the direct quote path above.
	lifi            lifiQuoter
	chains          *BlockchainService
	treasuryAddress string
	// treasuryLanes answers the one question chainID()/tokenAddress() below
	// CANNOT: not "does the chain registry know this chain" but "does the
	// treasury actually have an executable lane on it". Those are
	// deliberately different sets — RAMP_TREASURY_CHAINS can (and does) name
	// a strict subset of every chain config.Networks knows about (a missing
	// native-floor override, no chain client, or no chain id at boot all
	// drop a chain from `lanes` in buildRampConversion without removing it
	// from the registry) — so this must be asked separately, inside
	// ConvertedVenues itself, before a venue on an unlaned chain is ever
	// offered. *Treasury satisfies this; kept as an interface so tests can
	// fake it without building a whole Treasury.
	treasuryLanes treasuryLaneChecker
	// conversionMins/conversionMinDefault are Decision 6's per-chain floor,
	// measured on the settlement leg's stablecoin amount. conversionMinDefault
	// starts at the product default ($1) so a service that never calls
	// SetConversionMinimums still enforces one.
	conversionMins       map[string]float64
	conversionMinDefault float64
	// solanaValidator validates a Solana mint/recipient. Nil (no Solana chain
	// configured) refuses every Solana destination — see solanaAddressValidator.
	solanaValidator solanaAddressValidator
}

// SetSenderFeePercent prices our sender fee into every quoted rate, so the
// rate the user sees is the rate they get. NewPaycrestOrderService calls this
// with the fee it sends on orders, so the two cannot drift; anything
// unusable means no fee.
func (s *PaycrestQuoteService) SetSenderFeePercent(pct string) {
	s.senderFeePercent, _ = parseSenderFeePercent(pct)
}

// SetRoundTripBudgetBps sets B, pushed here by
// PaycrestOrderService.SetRoundTripBudgetBps.
func (s *PaycrestQuoteService) SetRoundTripBudgetBps(bps int) { s.roundTripBudgetBps = bps }

// defaultConvertedFeeMaxBps caps the fee a converted buy or swapped sell may
// charge (2%) on a service that was never given caps, so a dear route is
// refused rather than priced without limit.
const defaultConvertedFeeMaxBps = 200

// routeBudgetHeadroomBps is added on top of a route's measured cost when its
// budget is raised: the bridge is re-priced on a slightly different amount,
// and again at execution, so a budget set exactly at the probe's cost would
// refuse the route it was raised for.
const routeBudgetHeadroomBps = 10

// SetConvertedFeeCaps sets the most a converted buy or swapped sell may
// charge in total, in bps of the order, per chain (a buy's destination, a
// sell's source); def applies to any other chain. 0 turns raising off.
func (s *PaycrestQuoteService) SetConvertedFeeCaps(byChain map[string]int, def int) {
	s.convertedFeeMaxByChain = byChain
	s.convertedFeeMaxDefault = def
	s.convertedFeeCapsSet = true
}

// convertedFeeMaxBps is the fee cap for a chain. Anything outside
// 0..MaxConvertedFeeBps reads as 0 — raising off — so a bad value can only
// make it stricter.
func (s *PaycrestQuoteService) convertedFeeMaxBps(chainKey string) int {
	if !s.convertedFeeCapsSet {
		return defaultConvertedFeeMaxBps
	}
	limit, ok := s.convertedFeeMaxByChain[strings.ToLower(strings.TrimSpace(chainKey))]
	if !ok {
		limit = s.convertedFeeMaxDefault
	}
	if limit < 0 || limit > config.MaxConvertedFeeBps {
		return 0
	}
	return limit
}

// maxSenderFeePercent bounds what we accept from config. Well above any fee we
// would charge, low enough that a typo ("5" for "0.5") is refused rather than
// quietly charging users ten times over.
const maxSenderFeePercent = 5

// parseSenderFeePercent is the one reader of the fee percent. ok is false for
// empty, unparseable, non-finite, zero/negative or implausibly large values —
// ParseFloat happily accepts "NaN" and "Inf", which would otherwise flow into
// rates and order amounts.
func parseSenderFeePercent(pct string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(pct), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > maxSenderFeePercent {
		return 0, false
	}
	return v, true
}

// effectiveRate folds the sender fee into a provider rate.
//
// Paycrest charges the fee in token, on top of the order: a buyer pays for
// amount+fee, a seller's deposit has the fee taken out before conversion.
// Either way the user gets fewer tokens per naira (buy) or fewer naira per
// token (sell) than the provider quoted, so that is the rate they are shown.
// Sell uses (1-p) rather than 1/(1+p): whichever base Paycrest takes the fee
// on, the difference is p² and this way it lands in the user's favour.
func effectiveRate(side string, rate, feePercent float64) float64 {
	if feePercent <= 0 {
		return rate
	}
	if side == "buy" {
		return rate * (1 + feePercent/100)
	}
	return rate * (1 - feePercent/100)
}

// plainDecimal is the only shape an amount or rate may take: digits with an
// optional fractional part. "800." and ".5" are allowed because the app's
// amount field produces them mid-keystroke and quotes on every change.
var plainDecimal = regexp.MustCompile(`^([0-9]+(\.[0-9]*)?|\.[0-9]+)$`)

// parsePositiveDecimal reads a user amount or a Paycrest rate. ParseFloat alone
// is not a boundary: it accepts "NaN", "Inf", hex floats and "1_000", and NaN
// passes every "<= 0" check on its way into an order amount.
func parsePositiveDecimal(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	if !plainDecimal.MatchString(v) {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsInf(f, 0) || f <= 0 {
		return 0, false
	}
	return f, true
}

// quotedRate is effectiveRate for a provider's rate string. With no fee the
// provider's string passes through untouched rather than being reformatted.
func quotedRate(side, rate string, feePercent float64) (string, float64, error) {
	r, ok := parsePositiveDecimal(rate)
	if !ok {
		return rate, 0, fmt.Errorf("paycrest: unusable rate %q", rate)
	}
	if feePercent <= 0 {
		return rate, r, nil
	}
	eff := effectiveRate(side, r, feePercent)
	return strconv.FormatFloat(eff, 'f', 2, 64), eff, nil
}

type marketRateEntry struct {
	rate float64
	err  error // a remembered failure, kept for paycrestMarketRateFailTTL
	at   time.Time
}

// paycrestMarketRateFailTTL is how long a failed reference-rate read is
// remembered, so an outage costs one call per window instead of one per public
// quote (spec 2026-10-03 §4.1).
const paycrestMarketRateFailTTL = 5 * time.Second

// paycrestMarketRateTTL keeps the reference mid fresh enough to size a naira
// amount into token units without hammering the endpoint. It is NOT used to
// price an order — only the per-order Rates() call does that.
const paycrestMarketRateTTL = 60 * time.Second

// paycrestQuoteFanoutTimeout bounds the whole venue sweep. Generous enough for
// a slow upstream, short enough that a user is never left waiting on one
// unresponsive provider — the venues that answered are still a usable quote.
// paycrestBookTTL keeps the order book fresh enough to reflect a provider
// leaving without re-fetching it on every keystroke. Short, because the whole
// reason this service re-quotes constantly is that the book moves.
const paycrestBookTTL = 20 * time.Second

// paycrestBookFetchTimeout bounds the shared book fetch itself, independently
// of how long any one caller is willing to wait for it.
const paycrestBookFetchTimeout = 10 * time.Second

// paycrestBookFailTTL suppresses re-fetching after a failure. Short enough that
// recovery is quick, long enough that an outage does not turn every request
// into an outbound connection.
const paycrestBookFailTTL = 5 * time.Second

// rampLegsMaxStale is how old a book may be and still answer a direction
// question. Generous next to the 20s TTL — this is a ceiling on a degraded
// path, not a refresh interval.
const rampLegsMaxStale = 5 * time.Minute

// ErrPaycrestBookUnavailable means we have no book at all and are in the
// cool-off after a failed fetch. Distinct from ErrPaycrestBookEmpty, which is a
// book we DID get and could not use.
var ErrPaycrestBookUnavailable = errors.New("paycrest: order book is unavailable")

// NewPaycrestQuoteService returns nil when the client is unavailable so
// callers gate on `!= nil`, consistent with the rest of the package.
func NewPaycrestQuoteService(pc PaycrestQuoter, tokens, chainKeys []string) *PaycrestQuoteService {
	if pc == nil {
		return nil
	}
	if len(tokens) == 0 {
		tokens = []string{"USDC", "USDT"}
	}
	if len(chainKeys) == 0 {
		chainKeys = []string{"base", "polygon", "bsc", "arbitrum", "ethereum"}
	}
	return &PaycrestQuoteService{
		pc:                   pc,
		tokens:               tokens,
		chainKeys:            chainKeys,
		marketRate:           map[string]marketRateEntry{},
		conversionMinDefault: defaultConversionMinUSD,
	}
}

// defaultConversionMinUSD is Decision 6's default floor for a converted buy,
// measured on the settlement leg's stablecoin amount. Individual chains
// (Linea, other high-gas destinations) get a higher floor via
// SetConversionMinimums; this is what applies to everything else.
const defaultConversionMinUSD = 1

// SetLiFiQuoter wires ConvertedVenues to a LiFi quoting backend. Takes the
// exact lifiQuoter interface ConversionOrchestrator (Task 7) declares — one
// interface, two independent callers, nothing to keep in sync by hand.
func (s *PaycrestQuoteService) SetLiFiQuoter(q lifiQuoter) {
	s.lifi = q
}

// SetChainRegistry gives ConvertedVenues chain IDs and token addresses to
// price a LiFi route from — the same registry ConversionOrchestrator resolves
// against at execution time, so a testnet deployment prices against its own
// addresses rather than silently reading mainnet's.
func (s *PaycrestQuoteService) SetChainRegistry(chains *BlockchainService) {
	s.chains = chains
}

// SetTreasuryAddress is who a converted-buy route is quoted from/to. Quoting
// only: SetLiFiQuoter's Quote never moves funds, it only prices what a route
// would cost if the treasury ran it.
func (s *PaycrestQuoteService) SetTreasuryAddress(addr string) {
	s.treasuryAddress = strings.TrimSpace(addr)
}

// treasuryLaneChecker is the one question ConvertedVenues needs about the
// treasury itself: does it have an executable lane on a given chain.
// *Treasury satisfies it via its own Lane method.
type treasuryLaneChecker interface {
	Lane(chainKey string) *TreasuryLane
	// CanPayForGas is whether chainKey's lane can pay for `units` of gas
	// right now (briefly cached), so a route is never offered that the
	// treasury could not send.
	CanPayForGas(ctx context.Context, chainKey string, units uint64) bool
}

// SetTreasuryLanes wires the REAL gate on which requested chains a converted
// buy may ever be offered on. Without it, ConvertedVenues offers nothing —
// same dormant-until-wired convention as a nil lifi/chains or an empty
// treasuryAddress — because offering a venue this treasury cannot execute on
// is exactly the live defect this exists to close: the user pays naira,
// custody lands, and only THEN does o.destination() discover there is
// nowhere to send it.
func (s *PaycrestQuoteService) SetTreasuryLanes(t treasuryLaneChecker) {
	s.treasuryLanes = t
}

// SetSolanaValidator wires the Solana chain client's own address check, which
// is what makes a Solana destination resolvable at all. Left unset, a Solana
// request resolves to nothing and is simply not offered — the same
// dormant-until-wired shape as a missing LiFi quoter or treasury.
func (s *PaycrestQuoteService) SetSolanaValidator(v solanaAddressValidator) {
	s.solanaValidator = v
}

// SetConversionMinimums installs the per-chain floor a converted buy must
// clear before it is offered (Decision 6). def <= 0 leaves the existing
// default in place rather than disabling the floor entirely.
func (s *PaycrestQuoteService) SetConversionMinimums(mins map[string]float64, def float64) {
	s.conversionMins = mins
	if def > 0 {
		s.conversionMinDefault = def
	}
}

// Quote asks every configured venue what it will do with this fiat amount and
// returns only the ones that answered, cheapest first.
//
// side is "buy" for an onramp (user spends fiat) or "sell" for an offramp
// (user receives fiat). fiatAmount is in major units ("1000" naira).
func (s *PaycrestQuoteService) Quote(ctx context.Context, side, fiatCode, fiatAmount string) (*PaycrestQuoteResult, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	side = strings.ToLower(strings.TrimSpace(side))
	if side != "buy" && side != "sell" {
		return nil, fmt.Errorf("paycrest quote: side must be buy or sell, got %q", side)
	}
	if fiatCode == "" {
		fiatCode = "NGN"
	}
	amt, ok := parsePositiveDecimal(fiatAmount)
	if !ok {
		return nil, fmt.Errorf("paycrest quote: unusable amount %q", fiatAmount)
	}

	mkt, err := s.cachedMarketRate(ctx, fiatCode, side)
	if err != nil {
		return nil, err
	}

	// Rates() takes TOKEN units, so size the request off the reference mid.
	// This is an estimate used only to land in the right pricing tier; the
	// rate that binds is whatever comes back.
	tokenAmt := amt / mkt
	if tokenAmt < PaycrestMinOrderUSD {
		return nil, fmt.Errorf("%w: %.2f %s is about %.4f, minimum is %.2f",
			ErrPaycrestBelowMinimum, amt, fiatCode, tokenAmt, PaycrestMinOrderUSD)
	}

	res := &PaycrestQuoteResult{
		FiatCode:   strings.ToUpper(fiatCode),
		FiatAmount: fiatAmount,
		Side:       side,
		MarketRate: mkt,
		QuotedAt:   time.Now(),
	}

	book, err := s.cachedBook(ctx)
	if err != nil {
		return nil, err
	}

	// Which of OUR chains Paycrest serves, so the book can be filtered without
	// asking about chains that could never appear.
	networkFor := map[string]string{}
	for _, chainKey := range s.chainKeys {
		if network, ok := PaycrestNetwork(chainKey); ok {
			networkFor[network] = chainKey
		}
	}
	wanted := map[string]bool{}
	for _, t := range s.tokens {
		wanted[strings.ToUpper(t)] = true
	}

	// Best entry per (token, network). A venue can have several providers
	// quoting different bands; the user only cares about the best one that
	// covers their size.
	type key struct{ token, chainKey string }
	best := map[key]PaycrestBookEntry{}
	for _, e := range book.Book {
		if !strings.EqualFold(e.Fiat, fiatCode) || !strings.EqualFold(e.Side, side) {
			continue
		}
		token := strings.ToUpper(e.Token)
		if !wanted[token] {
			continue
		}
		chainKey, ok := networkFor[strings.ToLower(e.Network)]
		if !ok {
			continue
		}
		// The size band is the whole point of reading the book: an entry that
		// cannot fill this amount must never reach the picker, or the user
		// selects a route the order then refuses.
		min, err1 := strconv.ParseFloat(e.Min, 64)
		max, err2 := strconv.ParseFloat(e.Max, 64)
		if err1 != nil || err2 != nil || tokenAmt < min || tokenAmt > max {
			continue
		}
		rate, ok := parsePositiveDecimal(e.Rate)
		if !ok {
			continue
		}
		k := key{token, chainKey}
		cur, seen := best[k]
		if !seen {
			best[k] = e
			continue
		}
		curRate, _ := strconv.ParseFloat(cur.Rate, 64)
		if (side == "buy" && rate < curRate) || (side == "sell" && rate > curRate) {
			best[k] = e
		}
	}

	for k, e := range best {
		// Already parsed once while choosing best, so this cannot fail.
		rateStr, rate, _ := quotedRate(side, e.Rate, s.senderFeePercent)
		network, _ := PaycrestNetwork(k.chainKey)
		res.Venues = append(res.Venues, PaycrestVenue{
			ChainKey:             k.chainKey,
			Network:              network,
			Token:                k.token,
			Rate:                 rateStr,
			RawRate:              e.Rate,
			SpreadPercent:        (rate/mkt - 1) * 100,
			ProviderIDs:          []string{e.ProviderID},
			TokenAmount:          amt / rate,
			TokenAmountBaseUnits: s.baseUnitsFor(k.chainKey, k.token, fiatAmount, rateStr),
			Kind:                 models.RouteKindDirect,
		})
	}

	if len(res.Venues) == 0 {
		return nil, fmt.Errorf("%w (%s %s %s)", ErrPaycrestNoVenue, fiatAmount, res.FiatCode, side)
	}

	// Cheapest for the user first: on a buy that is the lowest fiat-per-token;
	// on a sell it is the highest.
	sort.SliceStable(res.Venues, func(i, j int) bool {
		a, _ := strconv.ParseFloat(res.Venues[i].Rate, 64)
		b, _ := strconv.ParseFloat(res.Venues[j].Rate, 64)
		if a != b {
			if side == "buy" {
				return a < b // fewer naira per token is better on a buy
			}
			return a > b // more naira per token is better on a sell
		}
		// Providers routinely quote one rate across every network, so without a
		// tie-break the concurrent fan-out would reorder the picker on each
		// keystroke. Token then chain keeps it stable.
		if res.Venues[i].Token != res.Venues[j].Token {
			return res.Venues[i].Token < res.Venues[j].Token
		}
		return res.Venues[i].ChainKey < res.Venues[j].ChainKey
	})
	res.Best = &res.Venues[0]
	return res, nil
}

// ─── Ramp legs ─────────────────────────────────────────────────────────────

// RampLegs says which directions a currency can actually trade in right now.
// Both false means the currency has a provider on paper but nothing on the
// book — the app must offer neither button.
type RampLegs struct {
	Buy  bool `json:"buy"`
	Sell bool `json:"sell"`
}

// RampLegs reports, per fiat, which sides have live liquidity on a venue we
// route.
//
// This is derived rather than declared on purpose. Direction is a property of
// provider liquidity, not of our product decisions: UGX and TZS have offramp
// providers only today, and the day someone starts quoting a UGX onramp we
// should offer it without shipping a migration. The registry stays the
// authority on WHO serves a currency; the book answers WHICH WAY.
//
// It rides the same 20s-TTL book the venue picker uses, so the currency list
// costs nothing extra: one shared /v2/markets call (~0.2s) serves every caller,
// and a later fetch failure serves the previous book rather than erroring. An
// error here therefore means the book has NEVER loaded — see the caller for
// what gets served then.
func (s *PaycrestQuoteService) RampLegs(ctx context.Context) (map[string]RampLegs, error) {
	if s == nil {
		return nil, ErrPaycrestDisabled
	}
	book, err := s.cachedBook(ctx)
	if err != nil {
		return nil, err
	}

	wanted := make(map[string]bool, len(s.tokens))
	for _, t := range s.tokens {
		wanted[strings.ToUpper(t)] = true
	}
	networkFor := make(map[string]string, len(s.chainKeys))
	for _, chainKey := range s.chainKeys {
		if network, ok := PaycrestNetwork(chainKey); ok {
			networkFor[network] = chainKey
		}
	}
	// A leg claim is nothing like a quote: Quote re-prices against /v2/rates
	// before an order exists, so a slightly stale book there is harmless. Here
	// nothing re-checks. cachedBook serves a stale book indefinitely on
	// repeated failure, which would let an hour-old book keep a reopened leg
	// shut, or keep a closed one open, presented as current fact. Past the
	// ceiling we would rather say we do not know and let the caller fall back.
	s.bookMu.Lock()
	age := time.Since(s.bookAt)
	s.bookMu.Unlock()
	if age > rampLegsMaxStale {
		return nil, fmt.Errorf("%w (book is %s old)", ErrPaycrestBookUnavailable, age.Truncate(time.Second))
	}

	legs := legsByFiat(book, wanted, networkFor)
	if len(legs) == 0 {
		// Not "nothing trades anywhere" — that is not a state this protocol
		// reaches. An empty map here means the book was empty or shaped in a
		// way we no longer parse, and reporting it as fact would take every
		// currency (NGN included) dark on a single bad response.
		return nil, ErrPaycrestBookEmpty
	}
	return legs, nil
}

// legsByFiat is the pure core of RampLegs: group the book by fiat and side,
// counting only entries on a token and network we would actually route.
//
// Order-size bands are deliberately ignored. The currency list renders before
// the user has typed an amount, so applying a band here would mean inventing
// one — and a currency whose only liquidity is large would vanish from the
// picker even though it trades fine at that size. Quote() applies the band when
// there is a real amount to apply it to.
func legsByFiat(book *PaycrestMarkets, wantToken map[string]bool, networkFor map[string]string) map[string]RampLegs {
	out := map[string]RampLegs{}
	if book == nil {
		return out
	}
	for _, e := range book.Book {
		fiat := strings.ToUpper(strings.TrimSpace(e.Fiat))
		if fiat == "" {
			continue
		}
		if !wantToken[strings.ToUpper(strings.TrimSpace(e.Token))] {
			continue
		}
		if _, ok := networkFor[strings.ToLower(strings.TrimSpace(e.Network))]; !ok {
			continue
		}
		// Same sanity Quote applies when building venues: an entry priced at
		// zero or unparseable can never become one, so counting it as a live
		// leg would advertise a direction the quote screen then refuses.
		if rate, err := strconv.ParseFloat(e.Rate, 64); err != nil || rate <= 0 {
			continue
		}
		legs := out[fiat]
		switch strings.ToLower(strings.TrimSpace(e.Side)) {
		case "buy":
			legs.Buy = true
		case "sell":
			legs.Sell = true
		default:
			continue
		}
		out[fiat] = legs
	}
	return out
}

// VenueFor binds the price for one venue immediately before an order is
// created. This is where the single authoritative /v2/rates call is spent.
//
// The picker is built from the order book, which is fast but is a standing
// offer rather than a quote. Before money moves we ask the provider what it
// will actually do with THIS amount — the book can be seconds stale, and a
// venue that has since left must fail here rather than at Paycrest with the
// user already committed.
func (s *PaycrestQuoteService) VenueFor(ctx context.Context, side, fiatCode, fiatAmount, chainKey, token string) (*PaycrestVenue, error) {
	return s.VenueForFee(ctx, side, fiatCode, fiatAmount, chainKey, token, s.senderFeePercent)
}

// VenueForFee is VenueFor priced at a fee OTHER than the configured one. A
// converted sell is sent to Paycrest at F − B, not F, so the naira estimate
// it shows has to be the F − B one: quoting the user at F and ordering at
// F − B would understate what she receives by the whole budget.
func (s *PaycrestQuoteService) VenueForFee(ctx context.Context, side, fiatCode, fiatAmount, chainKey, token string, feePercent float64) (*PaycrestVenue, error) {
	res, err := s.Quote(ctx, side, fiatCode, fiatAmount)
	if err != nil {
		return nil, err
	}
	var venue *PaycrestVenue
	for i := range res.Venues {
		if strings.EqualFold(res.Venues[i].ChainKey, chainKey) &&
			strings.EqualFold(res.Venues[i].Token, token) {
			venue = &res.Venues[i]
			break
		}
	}
	if venue == nil {
		return nil, fmt.Errorf("%w: %s on %s is no longer fillable at this size",
			ErrPaycrestNoVenue, strings.ToUpper(token), chainKey)
	}

	amt, ok := parsePositiveDecimal(fiatAmount)
	if !ok {
		return nil, fmt.Errorf("paycrest venue: unusable amount %q", fiatAmount)
	}
	rates, err := s.pc.Rates(ctx, venue.Network, venue.Token, venue.TokenAmount, fiatCode)
	if err != nil {
		// The book said this venue was live and the provider now disagrees.
		// Surfacing it as "no venue" is right: the user should pick again
		// rather than be told something broke.
		if errors.Is(err, ErrPaycrestNoProvider) {
			return nil, fmt.Errorf("%w: %s on %s stopped quoting",
				ErrPaycrestNoVenue, strings.ToUpper(token), chainKey)
		}
		return nil, err
	}
	var q *PaycrestQuote
	if side == "buy" {
		q = rates.Buy
	} else {
		q = rates.Sell
	}
	if q == nil || strings.TrimSpace(q.Rate) == "" {
		return nil, fmt.Errorf("%w: %s on %s has no %s side right now",
			ErrPaycrestNoVenue, strings.ToUpper(token), chainKey, side)
	}
	rateStr, rate, err := quotedRate(side, q.Rate, feePercent)
	if err != nil {
		return nil, fmt.Errorf("paycrest venue: unusable rate %q", q.Rate)
	}

	// Bind to what the provider just said, not to the book.
	bound := *venue
	bound.Rate = rateStr
	bound.RawRate = strings.TrimSpace(q.Rate)
	bound.TokenAmount = amt / rate
	bound.TokenAmountBaseUnits = s.baseUnitsFor(bound.ChainKey, bound.Token, fiatAmount, rateStr)
	bound.SpreadPercent = (rate/res.MarketRate - 1) * 100
	if len(q.ProviderIDs) > 0 {
		bound.ProviderIDs = q.ProviderIDs
	}
	return &bound, nil
}

// providerRateNominalUSDC is the order size ProviderRate asks the provider
// about. Paycrest prices by size, so it has to be roughly the size orders are
// made at; TSA Pay's naira payouts are a few tens of dollars.
const providerRateNominalUSDC = 100

// ProviderRate is the provider's own SELL rate for USDC on Base with NO sender
// fee, exactly as it quoted it (fiat per USDC). It is the rate a naira-route
// order prices from, so a merchant's crypto is sized from it rather than from
// the cached market mid, which no order is ever opened at. The fee is applied
// (or not) by whoever opens the order; this is the figure underneath it.
func (s *PaycrestQuoteService) ProviderRate(ctx context.Context, fiat string) (*big.Rat, error) {
	fiat = strings.ToUpper(strings.TrimSpace(fiat))
	market, err := s.cachedMarketRate(ctx, fiat, "sell")
	if err != nil {
		return nil, err
	}
	mid := new(big.Rat).SetFloat64(market) // nil for NaN/Inf
	if mid == nil || mid.Sign() <= 0 {
		return nil, fmt.Errorf("paycrest provider rate: unusable market rate %v", market)
	}
	nominal := new(big.Rat).Mul(mid, big.NewRat(providerRateNominalUSDC, 1)).FloatString(2)
	venue, err := s.VenueForFee(ctx, "sell", fiat, nominal, rampSettlementChainKey, rampSettlementToken, 0)
	if err != nil {
		return nil, err
	}
	rate, ok := new(big.Rat).SetString(strings.TrimSpace(venue.Rate))
	if !ok || rate.Sign() <= 0 {
		return nil, fmt.Errorf("paycrest provider rate: unusable rate %q", venue.Rate)
	}
	return rate, nil
}

// ─── Converted venues (Phase 2, Task 8) ────────────────────────────────────

// RequestedAsset is one asset the user asked to receive, that this Paycrest
// book may or may not be able to fill directly.
//
// Two ways of naming it, because both are in flight: Chain/Token are the
// legacy symbol path (an older app build, and still how a direct venue is
// matched), while ChainID/Address name any token the asset picker can show.
// resolveRequestedAsset is the only reader — nothing here is trusted as
// given, least of all Chain when ChainID is set.
type RequestedAsset struct {
	Chain, Token string
	ChainID      int64
	Address      string
	// Recipient is the wallet the route is priced to. Empty prices to the
	// treasury, which is what an anonymous price check gets.
	Recipient string
	// Destination is where the asset is delivered when the user's EVM wallet
	// cannot receive it — her Solana address. Required for a Solana asset
	// (nothing can be delivered to a 20-byte address there) and ignored
	// everywhere else; validated by the chain client, never trusted as given.
	Destination string
}

// ConvertedVenues offers routes Paycrest cannot fill directly: it delivers a
// token it does serve to our treasury, and one LiFi route converts it to what
// the user asked for. A LiFi failure (including ErrLiFiNoRoute) just means
// that venue is not on offer today, never an error for the whole call.
//
// Two things decide whether a route may be offered at all, and both exist
// because offering a route we then refuse to execute takes the user's naira
// into an order that can only wait for a human:
//
//   - PRICE. The whole round trip — LiFi's own cost plus the gas the treasury
//     pays, including one approve — must fit inside B, the round-trip budget
//     held back out of the sender fee. There is no separate markup ladder any
//     more: the order is sent to Paycrest at F − B, so B is exactly what is
//     available to spend and the markup shown IS B.
//   - EXECUTABILITY. The route is priced twice: once on T to learn LiFi's
//     cost l, then again on T/(1 − l), which is what the treasury will really
//     bridge. That second route is the one checked — its gas against the
//     settlement lane, its calldata against the layouts we can decode, its
//     destination chain against the one asked for, and (stablecoin
//     destinations only) its own guaranteed minimum against the promise.
//
// PromisedAmount is stated in the REQUESTED token's base units, not the
// settlement leg's (the binding Task 7 controller ruling): the orchestrator
// that executes these orders rescales every comparison to destination units,
// so a promise left in settlement-leg units would be silently wrong by a
// factor of 10^decimalsDelta whenever the two tokens' decimals differ (BSC
// stables are 18dp; base/polygon/optimism are 6dp in this book). Only a
// stablecoin destination carries one at all — the value of a volatile token
// moves between quote and delivery, so promising a quantity of it would be a
// promise about a price we do not control.
func (s *PaycrestQuoteService) ConvertedVenues(ctx context.Context, side, fiatCode, fiatAmount string, want []RequestedAsset) ([]PaycrestVenue, error) {
	// treasuryLanes is in the dormant-until-wired list for the same reason as
	// the rest: every converted route needs the SETTLEMENT lane to pay its
	// gas, so without a lane checker nothing here is offerable — and the gas
	// check below would be a nil-interface call on a non-stable destination,
	// which never reaches the stable-only lane gate.
	if s == nil || s.lifi == nil || s.chains == nil || s.treasuryAddress == "" ||
		s.treasuryLanes == nil || len(want) == 0 {
		return nil, nil
	}
	// Conversion is a buy-only capability (the handler already gates on this
	// before calling in, but a future direct caller — another handler, a
	// job, a test — must not be able to get a converted SELL venue just by
	// skipping that gate).
	if !strings.EqualFold(side, models.PaycrestDirectionBuy) {
		return nil, nil
	}
	base, err := s.Quote(ctx, side, fiatCode, fiatAmount)
	if err != nil {
		return nil, err
	}
	out := []PaycrestVenue{}
	for _, req := range want {
		// Every drop below is a real, expected outcome (a route this specific
		// request cannot use) — not an error. Logged anyway, because the
		// generic "no route can deliver X" the client shows collapses a dozen
		// distinct reasons into one sentence, and nothing else says which one
		// fired. requestedAssetLabel matches what the client's own refusal
		// names, so a support conversation and a log line talk about the same
		// thing.
		label := requestedAssetLabel(PaycrestBuyRequest{
			RequestedChainID: req.ChainID, RequestedTokenAddress: req.Address, Token: req.Token,
		})
		skip := func(reason string, args ...any) {
			log.Printf("[ramp-conversion] quote-skip chain=%d token=%q label=%s reason=%s",
				req.ChainID, req.Address, label, fmt.Sprintf(reason, args...))
		}

		src := s.bestSourceVenue(base.Venues) // the venue this converted buy settles from, pinned to Base
		if src == nil {
			skip("no direct settlement venue is currently quotable")
			continue
		}
		routeAmount, ok := new(big.Int).SetString(src.TokenAmountBaseUnits, 10)
		if !ok || routeAmount.Sign() <= 0 {
			skip("settlement leg amount %q could not be resolved", src.TokenAmountBaseUnits)
			continue
		}
		// Everything about the destination is derived here, before a paid
		// third-party call is spent on it: an unresolvable chain id or token
		// address must never reach LiFi, and the chain key that the checks
		// below are keyed on comes from the chain id rather than from
		// whatever chain name the client happened to send.
		asset, rerr := resolveRequestedAsset(s.chains, s.solanaValidator, req)
		if rerr != nil {
			skip("requested asset could not be resolved: %v", rerr)
			continue
		}
		// Decision 3: a direct route always wins; never convert what Paycrest
		// can already deliver. Only a stablecoin can have a direct venue —
		// Paycrest fills nothing else — and the match is on the RESOLVED
		// chain/symbol, since on the address path req.Chain is empty.
		if asset.Stable && directVenueFor(base.Venues, RequestedAsset{Chain: asset.ChainKey, Token: asset.StableSymbol}) != nil {
			// Not a refusal — the direct venue wins, on purpose. No skip log.
			continue
		}
		// Decision 6: the per-chain minimum, measured on the settlement leg's
		// stablecoin amount, checked before spending a LiFi call on it.
		if !MeetsMinimumOrder(asset.ChainKey, src.TokenAmount, s.conversionMins, s.conversionMinDefault) {
			skip("below the %s minimum order (settlement amount %g)", asset.ChainKey, src.TokenAmount)
			continue
		}
		// Only a stablecoin destination may need a top-up, so only it needs a
		// lane there. Everything else is delivered by the bridge alone, and
		// the lane that has to pay for THAT is the settlement leg's, checked
		// below against the route's own gas.
		if asset.Stable && s.treasuryLanes.Lane(asset.ChainKey) == nil {
			skip("no treasury lane configured for stable destination chain %s", asset.ChainKey)
			continue
		}
		// A destination the wallet itself cannot receive on (Solana) names its
		// own recipient, and resolveRequestedAsset has already validated it.
		// It outranks req.Recipient, which is the user's EVM wallet and could
		// only ever be a route to nowhere on that chain.
		recipient := firstNonEmpty(asset.Destination, req.Recipient, s.treasuryAddress /* pricing only */)
		// A stable-to-stable route should not move on price, so hold it to
		// LiFi's tight default; anything else is a real market and needs the
		// room a normal swap takes.
		slippage := routeSlippage(asset.Stable)
		lreq := LiFiQuoteRequest{
			FromChainID: s.chainID(src.ChainKey), ToChainID: asset.ChainID,
			FromToken: s.tokenAddress(src.ChainKey, src.Token), ToToken: asset.Address,
			FromAddress: s.treasuryAddress, ToAddress: recipient, FromAmount: routeAmount.String(), Slippage: slippage,
			// The treasury bridges this itself the moment the order settles —
			// no human signs it — so the cheapest route is safe to take, and
			// conversion_orchestrator's execution-time quote must ask for the
			// same order or it could see a different cost than was admitted.
			Order: "CHEAPEST",
		}
		// First pass on T to learn l; then price what the treasury will actually bridge.
		probe, perr := s.lifi.Quote(ctx, lreq)
		if perr != nil {
			skip("LiFi probe quote failed: %v", perr)
			continue
		}
		l1, ok := LiFiCostBps(probe)
		if !ok || l1 >= 10_000 {
			skip("LiFi probe's cost is unreadable or at/above 100%%")
			continue
		}
		// This route's own budget B′: at least B, raised to cover a dearer
		// round trip (plus headroom for the re-price below and at execution).
		// Raising it raises HER fee by the same amount — Paycrest is still
		// sent F − B, so our share never shrinks — up to the destination
		// chain's cap.
		budget := s.roundTripBudgetBps
		if need := l1 + TreasuryGasBps(probe, treasuryERC20Gas) + routeBudgetHeadroomBps; need > budget {
			budget = need
			feeBps := s.senderFeeBps() - s.roundTripBudgetBps + budget
			if limit := s.convertedFeeMaxBps(asset.ChainKey); feeBps > limit {
				skip("route needs a %dbps fee, over the %s cap of %dbps", feeBps, asset.ChainKey, limit)
				continue
			}
			priced, ok := s.repricedAtFee(*src, fiatAmount, base.MarketRate, feeBps)
			if !ok {
				skip("settlement leg could not be repriced at a %dbps fee", feeBps)
				continue
			}
			src = &priced
			if routeAmount, ok = new(big.Int).SetString(src.TokenAmountBaseUnits, 10); !ok || routeAmount.Sign() <= 0 {
				skip("repriced settlement amount %q could not be resolved", src.TokenAmountBaseUnits)
				continue
			}
		}
		bridge := new(big.Int).Mul(routeAmount, big.NewInt(10_000))
		bridge.Quo(bridge, big.NewInt(int64(10_000-l1)))
		lreq.FromAmount = bridge.String()
		route, qerr := s.lifi.Quote(ctx, lreq)
		if qerr != nil {
			skip("LiFi bridge quote failed: %v", qerr)
			continue
		}
		// The bridge runs from the settlement leg's chain, so that lane must
		// be able to pay THIS route's gas plus an approve. Selling a route
		// the treasury cannot send takes the user's money into an order
		// that can only wait for a refill. Same gas rules as ExecuteRoute: a
		// missing limit costs the ceiling, one above it is never executed.
		routeGas := routeGasUnits(route)
		if routeGas > maxRouteGasLimit {
			skip("route gas %d exceeds the %d ceiling", routeGas, maxRouteGasLimit)
			continue
		}
		if !s.treasuryLanes.CanPayForGas(ctx, src.ChainKey, routeGas+treasuryERC20Gas) {
			skip("treasury cannot currently pay %s's gas for %d units (route+approve)",
				src.ChainKey, routeGas+treasuryERC20Gas)
			continue
		}
		l, ok := LiFiCostBps(route)
		if !ok {
			skip("LiFi bridge quote's own cost is unreadable")
			continue
		}
		if g := TreasuryGasBps(route, treasuryERC20Gas); l+g > budget {
			skip("cost %dbps + gas %dbps = %dbps exceeds the %dbps round-trip budget",
				l, g, l+g, budget)
			continue
		}
		// Offer only what execution accepts: known layout, no destination call, right chain.
		//
		// Where the recipient is already known here, run the whole check
		// startBridge will run rather than the structural half. A non-EVM
		// destination names its own recipient (resolveRequestedAsset has
		// validated it), and the structural check passes calldata that
		// execution then refuses on EVERY attempt — a facet whose layout says
		// nothing about where an SPL delivery lands, say. The user only finds
		// out after paying naira: the order opens, no bridge can be sent, and
		// it waits out the retry window to refund.
		//
		// The EVM family cannot be held to the same check: an anonymous price
		// check quotes to the treasury, so there is no user recipient here to
		// compare the calldata against.
		if family := addressFamily(asset.ChainID); family != "evm" {
			if err := checkLiFiDeliveryTo(route.Data, s.chainID(src.ChainKey), asset.ChainID, recipient, family, asset.Address); err != nil {
				skip("non-EVM delivery check failed: %v", err)
				continue
			}
		} else {
			raw, derr := hexutil.Decode(route.Data)
			if derr != nil {
				skip("route calldata could not be decoded: %v", derr)
				continue
			}
			if d, derr := decodeLiFiDelivery(raw, s.chainID(src.ChainKey)); derr != nil || d.HasDestinationCall || d.DestinationChainID != asset.ChainID {
				skip("structural delivery check failed: decodeErr=%v hasDestinationCall=%v destChainID=%v wantChainID=%d",
					derr, d.HasDestinationCall, d.DestinationChainID, asset.ChainID)
				continue
			}
		}
		decimals, symbol := route.ToTokenDecimals, strings.ToUpper(route.ToTokenSymbol) // from LiFi, never the client
		if asset.Stable {
			decimals, symbol = asset.StableDecimals, asset.StableSymbol
		}
		if decimals <= 0 {
			skip("resolved decimals %d is not positive", decimals)
			continue
		}
		v := PaycrestVenue{
			Kind: models.RouteKindConverted,
			// ChainKey/Token/Network stay the PAYCREST leg: that is what the
			// order is created against (Task 9). ViaChain/ViaToken say so
			// explicitly. Network carries through from src so CreateBuy has
			// Paycrest's own network name to submit — without it a converted
			// order would be created with an empty network.
			ChainKey: src.ChainKey, Token: src.Token, Network: src.Network,
			ViaChain: src.ChainKey, ViaToken: src.Token,
			RequestedChain: asset.ChainKey, RequestedToken: symbol,
			RequestedChainID: asset.ChainID, RequestedAddress: asset.Address, RequestedDecimals: decimals,
			RequestedDestination: asset.Destination,
			Rate: src.Rate, TokenAmount: src.TokenAmount, TokenAmountBaseUnits: src.TokenAmountBaseUnits,
			MarkupBps: budget, CostBps: l,
			EstimatedAmount: route.ToAmount.String(), ETASeconds: route.ETASeconds,
		}
		if asset.Stable {
			promise := StablePromise(routeAmount, tokenDecimalsFor(s.chainID(src.ChainKey), src.Token), decimals)
			if route.ToAmountMin == nil || route.ToAmountMin.Cmp(promise) < 0 {
				continue // execution would refuse it on every attempt
			}
			v.PromisedAmount = promise.String()
		}
		out = append(out, v)
	}
	return out, nil
}

// senderFeeBps is F, the fee every quoted rate is priced at, in bps.
func (s *PaycrestQuoteService) senderFeeBps() int {
	return int(math.Round(s.senderFeePercent * 100))
}

// repricedAtFee is a buy venue re-quoted at feeBps instead of F: the same
// provider rate, the same fiat, fewer tokens.
func (s *PaycrestQuoteService) repricedAtFee(v PaycrestVenue, fiatAmount string, marketRate float64, feeBps int) (PaycrestVenue, bool) {
	amt, ok := parsePositiveDecimal(fiatAmount)
	if !ok {
		return v, false
	}
	rateStr, rate, err := quotedRate(models.PaycrestDirectionBuy, v.RawRate, float64(feeBps)/100)
	if err != nil {
		return v, false
	}
	v.Rate = rateStr
	v.TokenAmount = amt / rate
	v.TokenAmountBaseUnits = s.baseUnitsFor(v.ChainKey, v.Token, fiatAmount, rateStr)
	if marketRate > 0 {
		v.SpreadPercent = (rate/marketRate - 1) * 100
	}
	return v, v.TokenAmountBaseUnits != ""
}

// directVenueFor finds a direct (already-fillable) venue for a requested
// asset, or nil. Used to enforce Decision 3: never convert what Paycrest can
// already deliver.
func directVenueFor(venues []PaycrestVenue, req RequestedAsset) *PaycrestVenue {
	for i := range venues {
		if strings.EqualFold(venues[i].ChainKey, req.Chain) && strings.EqualFold(venues[i].Token, req.Token) {
			return &venues[i]
		}
	}
	return nil
}

// bestSourceVenue picks the direct venue a converted buy settles from.
//
// Pinned to rampSettlementChainKey (Base) — the same chain the sell side
// already settles every swap to — so the treasury needs standing gas on one
// chain, not on whichever of Paycrest's own venues happens to quote
// cheapest this minute. Scanning every configured chain for the best rate
// used to mean the settlement leg could silently move to a chain the
// treasury had never been funded on, taking every converted buy down at
// once with no link back to which chain was actually responsible. The
// modest rate difference between Base and another chain's direct book is
// not worth that fragility, especially once a bridge's own gas cost is
// usually the bigger swing in what a user actually receives.
//
// Other chains are only consulted when Base has no direct listing for any
// preferred token at all — s.tokens is USDC-before-USDT because USDC has
// consistently carried deeper onramp liquidity (see the tokens field
// comment) — falling back to the cheapest/best-ranked venue (Venues[0],
// since Quote already sorts best-first) when nothing on Base will do.
func (s *PaycrestQuoteService) bestSourceVenue(venues []PaycrestVenue) *PaycrestVenue {
	for _, want := range s.tokens {
		for i := range venues {
			if strings.EqualFold(venues[i].ChainKey, rampSettlementChainKey) && strings.EqualFold(venues[i].Token, want) {
				return &venues[i]
			}
		}
	}
	for _, want := range s.tokens {
		for i := range venues {
			if strings.EqualFold(venues[i].Token, want) {
				return &venues[i]
			}
		}
	}
	if len(venues) == 0 {
		return nil
	}
	return &venues[0]
}

// chainID resolves OUR chain key to its numeric chain ID via the shared
// registry — nil registry or an unknown key both read as 0, which every
// caller here treats as "cannot price this leg" rather than a valid chain.
func (s *PaycrestQuoteService) chainID(chainKey string) int64 {
	if s.chains == nil {
		return 0
	}
	id, _ := s.chains.ChainIDForName(chainKey)
	return id
}

// tokenAddress resolves a token on any configured network, so a testnet
// deployment resolves its own addresses rather than silently reading
// mainnet's. Mirrors ConversionOrchestrator.tokenAddress (Task 7): same
// registry, same resolution order, kept in this file because the quote
// service has no dependency on the orchestrator otherwise.
func (s *PaycrestQuoteService) tokenAddress(chainKey, symbol string) string {
	if s.chains == nil {
		return ""
	}
	id, ok := s.chains.ChainIDForName(chainKey)
	if !ok {
		return ""
	}
	return s.chains.TokenAddressForChainID(id, strings.ToUpper(strings.TrimSpace(symbol)))
}

// baseUnitsFor is a venue's token amount in base units, as a decimal string.
// Empty when decimals cannot be resolved (no chain registry wired), which is
// fine for direct quoting/ordering — only ConvertedVenues' integer maths
// needs this figure.
func (s *PaycrestQuoteService) baseUnitsFor(chainKey, token, fiatAmount, rate string) string {
	id := s.chainID(chainKey)
	if id == 0 {
		return ""
	}
	amt, err := venueBaseUnits(fiatAmount, rate, tokenDecimalsFor(id, token))
	if err != nil {
		return ""
	}
	return amt.String()
}

// venueBaseUnits sizes fiatAmount/rate in base units directly from their
// DECIMAL STRINGS, via big.Rat — never through the float64 TokenAmount, so
// the figure that seeds a converted-buy promise is exact integer math, not a
// float round-trip. Floors on truncation: a promise rounded down is one we
// can always honour.
func venueBaseUnits(fiatAmount, rate string, decimals int) (*big.Int, error) {
	fiatR, ok := new(big.Rat).SetString(fiatAmount)
	if !ok {
		return nil, fmt.Errorf("paycrest venue: unusable fiat amount %q", fiatAmount)
	}
	rateR, ok := new(big.Rat).SetString(rate)
	if !ok || rateR.Sign() <= 0 {
		return nil, fmt.Errorf("paycrest venue: unusable rate %q", rate)
	}
	tokens := new(big.Rat).Quo(fiatR, rateR)
	tokens.Mul(tokens, new(big.Rat).SetInt(pow10(decimals)))
	return new(big.Int).Quo(tokens.Num(), tokens.Denom()), nil
}

func (s *PaycrestQuoteService) cachedMarketRate(ctx context.Context, fiat, side string) (float64, error) {
	key := strings.ToUpper(fiat) + ":" + side
	s.marketMu.Lock()
	if e, ok := s.marketRate[key]; ok {
		age := time.Since(e.at)
		if e.err == nil && age < paycrestMarketRateTTL {
			s.marketMu.Unlock()
			return e.rate, nil
		}
		if e.err != nil && age < paycrestMarketRateFailTTL {
			s.marketMu.Unlock()
			return 0, e.err
		}
	}
	s.marketMu.Unlock()

	rate, err := s.pc.MarketRate(ctx, fiat, side)
	if err != nil && ctx.Err() != nil {
		return 0, err // our caller gave up; that says nothing about Paycrest
	}
	s.marketMu.Lock()
	s.marketRate[key] = marketRateEntry{rate: rate, err: err, at: time.Now()}
	s.marketMu.Unlock()
	if err != nil {
		return 0, err
	}
	return rate, nil
}

// MarketRate is Paycrest's protocol reference rate (marketBuyRate /
// marketSellRate), cached 60s with failures remembered 5s. It exists even
// while the order book is empty, which is why the company wallet prices
// from it (spec 2026-10-03 §4.1).
func (s *PaycrestQuoteService) MarketRate(ctx context.Context, fiat, side string) (float64, error) {
	return s.cachedMarketRate(ctx, fiat, side)
}

// expireMarketRate ages every cached reference rate out. Test-only.
func (s *PaycrestQuoteService) expireMarketRate() {
	s.marketMu.Lock()
	for k, e := range s.marketRate {
		e.at = e.at.Add(-2 * paycrestMarketRateTTL)
		s.marketRate[k] = e
	}
	s.marketMu.Unlock()
}

// expireBook ages the cached book out so a test can exercise the refresh and
// stale-serve paths without reaching into the mutex and timestamps directly.
// Test-only, but it lives here so there is one place to update when the cache
// changes shape.
func (s *PaycrestQuoteService) expireBook() {
	s.bookMu.Lock()
	s.bookAt = s.bookAt.Add(-2 * paycrestBookTTL)
	s.bookFailAt = time.Time{}
	s.bookMu.Unlock()
}

// cachedBook fetches the order book at most once per TTL. The book is public
// and identical for every caller, so one fetch serves every concurrent quote.
//
// Three properties matter here, and each exists because of a way this endpoint
// can amplify a provider outage into a worse one:
//
//   - SINGLEFLIGHT. Every caller past the TTL used to launch its own fetch.
//     The currency list is now the highest-frequency consumer (the hub asks on
//     every focus), so a TTL boundary became a fan-out proportional to active
//     users, aimed at a provider that may already be struggling.
//
//   - A DETACHED fetch context. The currency list gives up after 2s; a quote
//     waits far longer. Sharing one fetch means the impatient caller would
//     cancel the patient one's work — and, worse, a fetch cancelled before it
//     lands never populates the cache, so under sustained latency the cache
//     could never warm and every request re-fired. The fetch therefore runs on
//     its own deadline and its own lifetime; callers only choose how long they
//     are willing to WAIT for it.
//
//   - NEGATIVE CACHING. After a failure, hold off briefly rather than letting
//     the next request retry immediately. A hanging upstream otherwise costs
//     every request its full patience before falling back.
func (s *PaycrestQuoteService) cachedBook(ctx context.Context) (*PaycrestMarkets, error) {
	s.bookMu.Lock()
	if s.book != nil && time.Since(s.bookAt) < paycrestBookTTL {
		b := s.book
		s.bookMu.Unlock()
		return b, nil
	}
	stale, failedAt := s.book, s.bookFailAt
	s.bookMu.Unlock()

	// Recently failed: serve what we have instead of re-attempting. Without
	// this, a hanging provider costs every caller its full timeout.
	if !failedAt.IsZero() && time.Since(failedAt) < paycrestBookFailTTL {
		if stale != nil {
			return stale, nil
		}
		return nil, ErrPaycrestBookUnavailable
	}

	type bookResult struct{ book *PaycrestMarkets }
	ch := s.bookSF.DoChan("book", func() (any, error) {
		// Detached from the caller: see the note above. The fetch outlives any
		// one request so that a caller who gives up still leaves the cache
		// warmer for the next.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), paycrestBookFetchTimeout)
		defer cancel()

		b, err := s.pc.Markets(fetchCtx)
		if err != nil {
			s.bookMu.Lock()
			s.bookFailAt = time.Now()
			s.bookMu.Unlock()
			return nil, err
		}
		s.bookMu.Lock()
		s.book, s.bookAt, s.bookFailAt = b, time.Now(), time.Time{}
		s.bookMu.Unlock()
		return bookResult{book: b}, nil
	})

	var (
		b   *PaycrestMarkets
		err error
	)
	select {
	case res := <-ch:
		err = res.Err
		if res.Val != nil {
			b = res.Val.(bookResult).book
		}
	case <-ctx.Done():
		// This caller is out of patience; the shared fetch continues. Fall
		// through to the stale book, or to an error if we have never had one.
		err = ctx.Err()
	}

	if err != nil {
		// Serve a stale book rather than denying every quote: a book from a
		// minute ago is far more useful than an error, and the order still
		// re-prices against /v2/rates before anything is created.
		s.bookMu.Lock()
		stale := s.book
		s.bookMu.Unlock()
		if stale != nil {
			return stale, nil
		}
		return nil, err
	}
	return b, nil
}
