package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// ramp_sell_quote.go — pricing a sell that needs a swap (design §3.2).
//
// She holds a token Paycrest cannot offramp. We route it through LiFi into
// USDC on Base, in HER OWN wallet — nothing is ever held by us — and then
// offramp that USDC at the Paycrest sell rate.
//
// The whole price is one number she never sees the parts of. F (the sender
// fee) is what she pays all-in; B is the slice of F a round trip may spend.
// Admission is l + g ≤ B, where l is LiFi's own cost and g is the gas we
// sponsor. Whatever is left, B − l − g, is charged as our LiFi integrator fee
// on the executable quote, so her all-in stays F however cheap the route was.
// Nothing here moves funds, signs anything or writes a row: it is a price.

// rampSettlementChainID / rampSettlementToken are where a swapped sell lands
// before the offramp: USDC on Base, the one leg Paycrest fills reliably and
// the only chain the EIP-3009 relay sponsors.
const (
	rampSettlementChainID int64 = 8453
	rampSettlementToken         = "USDC"
)

// ErrSellRouteTooCostly: the round trip costs more than B, so selling this
// token would eat into the rate she was quoted. Refused rather than quoted at
// a loss (design §3.2).
var ErrSellRouteTooCostly = errors.New("this token costs too much to sell for naira right now")

// ErrSellUnavailable: we cannot price this sell at all — no settlement token
// configured, or LiFi will not let us collect our fee on this chain. Never
// advertise a fee we cannot collect.
var ErrSellUnavailable = errors.New("selling this token for naira isn't available right now")

// ErrSellBadSourceAddress: the address the route would be priced from is not
// an address on that chain. A client bug, not an outage.
var ErrSellBadSourceAddress = errors.New("that isn't a valid address on that network")

// ErrSellSourceUnsupported: the source chain is one a sell cannot start from
// yet (Solana, Bitcoin, Sui). Said plainly rather than as a bad address.
//
// Solana is here because nothing can check what she would sign on it: a
// Solana route is a serialized transaction against whichever bridge program
// LiFi picked, not a call to the diamond whose calldata checkLiFiDeliveryTo
// decodes, and it carries no transactionId to bind the swap to our quote
// either. Until a Solana route can be verified the way an EVM one is, it is
// refused rather than handed over unread.
var ErrSellSourceUnsupported = errors.New("selling from this network isn't supported yet")

// SellSwapQuote is one priced sell. Route is the executable quote the app
// signs; LiFiQuote is the same answer verbatim, which is what actually
// crosses the wire — the app's own swap signer already speaks it.
type SellSwapQuote struct {
	Route     *LiFiRoute      `json:"-"`
	LiFiQuote json.RawMessage `json:"lifiQuote"`
	// CostBps (l), GasBps (g) and FeeBps (B − l − g) are the budget split.
	// They are shown to nobody, but they are what an operator reads when a
	// sell prices differently from what a user reports.
	CostBps int `json:"costBps"`
	GasBps  int `json:"gasBps"`
	FeeBps  int `json:"feeBps"`
	// GasTopUp: this quote budgeted a source-chain gas top-up into GasBps,
	// for THIS route. The app asks for a top-up exactly when this is set and
	// never re-derives the threshold itself — the server's answer is the one
	// the budget was admitted on.
	GasTopUp bool `json:"gasTopUp"`
	// TopUpWei is that top-up in the coin it is paid in (quotedTopUpWei of
	// Route). Empty when GasTopUp is false — nothing was budgeted, so nothing
	// may be sent — and also when it is true but the route quoted no gas in
	// wei (an unknown, priced live at send time from the route's gas limit).
	TopUpWei string `json:"-"`
	// USDCAmount is what lands on Base after our fee, in major units.
	USDCAmount   string `json:"usdcAmount"`
	FiatEstimate string `json:"fiatEstimate"`
	Rate         string `json:"rate"`
}

// SellQuoteInput names the sell. FromAddress is her address on the SOURCE
// chain; WalletAddress is her EVM wallet, the USDC receiver, and the caller
// must have reconciled it against the account before calling.
type SellQuoteInput struct {
	FromChainID int64
	FromToken   string
	FromAmount  string // base units
	// ToAmount, when set INSTEAD of FromAmount, asks for exactly this much
	// USDC (Base, 6dp base units) to land; the route reports what it costs.
	ToAmount      string
	FromAddress   string
	WalletAddress string
	FiatCode      string
}

// nativeBalanceReader answers "how much gas coin does this address hold on
// this chain". ok is false when nothing could read it — an unknown balance is
// treated as "she has none", because pricing a top-up we do not need costs a
// few bps and skipping one we do need strands her mid-sell.
type nativeBalanceReader func(ctx context.Context, chainID int64, addr string) (*big.Int, bool)

// RampSellQuoter prices a swapped sell. It owns no state beyond its wiring:
// every quote is asked fresh, because a route priced a minute ago is a price
// we cannot honour.
type RampSellQuoter struct {
	lifi   lifiQuoter
	quotes *PaycrestQuoteService
	chains *BlockchainService
	// budgetBps is B.
	budgetBps int
	// converted is the Paycrest sender fee a converted order is sent at
	// (F − B), as a percent string — the same value PaycrestOrderService
	// sends, so the estimate and the order cannot drift.
	converted string
	// sponsoredGasUSD is the EIP-3009 relay we pay on Base to move her USDC
	// into the offramp.
	sponsoredGasUSD float64
	nativeBalance   nativeBalanceReader
	// routers is, per source chain id, the contracts a route may send her
	// transaction to or have her approve — the same pinned allowlist the
	// treasury signs against. A chain with none is refused: see checkSignable.
	routers map[int64][]common.Address
}

func NewRampSellQuoter(lifi lifiQuoter, quotes *PaycrestQuoteService, chains *BlockchainService, budgetBps int, convertedFeePercent string, sponsoredGasUSD float64) *RampSellQuoter {
	return &RampSellQuoter{
		lifi: lifi, quotes: quotes, chains: chains,
		budgetBps: budgetBps, converted: convertedFeePercent, sponsoredGasUSD: sponsoredGasUSD,
	}
}

// SetNativeBalanceReader wires the source-chain gas check. Unset, no top-up
// is ever priced in — the shape a chain with no sponsor lane should have,
// since we would not be sending one.
func (q *RampSellQuoter) SetNativeBalanceReader(f nativeBalanceReader) { q.nativeBalance = f }

// SetRouterAllowlist wires the contracts a route may target, per source chain
// id. Unset, every sell is refused: a route whose target cannot be checked is
// never handed to her to sign.
func (q *RampSellQuoter) SetRouterAllowlist(routers map[int64][]common.Address) { q.routers = routers }

func (q *RampSellQuoter) Quote(ctx context.Context, in SellQuoteInput) (*SellSwapQuote, error) {
	if q == nil || q.lifi == nil || q.quotes == nil || q.chains == nil || q.budgetBps <= 0 {
		return nil, ErrSellUnavailable
	}
	if family := addressFamily(in.FromChainID); family != "evm" {
		return nil, fmt.Errorf("%w (%s)", ErrSellSourceUnsupported, family)
	}
	if err := validateAddressForChain(in.FromChainID, in.FromAddress, nil); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSellBadSourceAddress, err)
	}
	// The receiver is her EVM wallet on Base, whoever the sender is.
	if err := validateAddressForChain(rampSettlementChainID, in.WalletAddress, nil); err != nil {
		return nil, fmt.Errorf("%w: receiving wallet: %v", ErrSellBadSourceAddress, err)
	}
	usdc := q.chains.TokenAddressForChainID(rampSettlementChainID, rampSettlementToken)
	if usdc == "" {
		return nil, ErrSellUnavailable
	}
	// Slippage is a tolerance, never a cost: it is not in l. But a route
	// admitted at 50 bps of total cost must not be quoted with 50 bps of
	// tolerance on top when the pair cannot justify it — and the destination
	// is always USDC, so a stable source makes a stable pair. Same decision,
	// same function, as a converted buy's destination.
	stableSource, _, _ := isStableDestination(q.chains, in.FromChainID, in.FromToken)
	req := LiFiQuoteRequest{
		FromChainID: in.FromChainID, ToChainID: rampSettlementChainID,
		FromToken: in.FromToken, ToToken: usdc,
		FromAddress: in.FromAddress, ToAddress: in.WalletAddress,
		FromAmount: in.FromAmount, ToAmount: in.ToAmount, Slippage: routeSlippage(stableSource),
	}

	// First pass, no fee: what does the round trip cost before we take
	// anything? Asking with our fee already on would price the fee into l and
	// charge her for it twice.
	first, err := q.lifi.Quote(ctx, req)
	if err != nil {
		return nil, err
	}
	if first == nil {
		// A quoter that answers with neither a route nor an error is broken,
		// not expensive. Say so rather than dereferencing it.
		return nil, ErrSellUnavailable
	}
	l, ok := LiFiCostBps(first)
	if !ok {
		return nil, ErrSellRouteTooCostly
	}
	gas := q.gasFor(ctx, in, first)
	if l+gas.bps > q.budgetBps {
		return nil, ErrSellRouteTooCostly
	}

	route := first
	fee := q.budgetBps - l - gas.bps
	if fee > 0 {
		req.FeeBps = fee
		route, err = q.lifi.Quote(ctx, req)
		if err != nil {
			if errors.Is(err, ErrLiFiFeeUnconfigured) {
				// LiFi has no fee wallet for us on this chain. We would be
				// quoting her a rate we cannot fund — refuse the sell.
				return nil, fmt.Errorf("%w: %v", ErrSellUnavailable, err)
			}
			return nil, err
		}
		if route == nil {
			return nil, ErrSellUnavailable
		}
		// The fee-carrying ask may land on another tool entirely, so the
		// budget has to be re-checked against THIS route — both halves of it.
		// l2 now includes our own fee, which is why it is compared to the
		// whole of B. g is re-priced from this route too: its gas, and
		// whether she can pay it, are this route's, and the top-up this sale
		// may later send is priced from this route alone. Admitting on the
		// first route's g would let a costlier tool spend past B.
		l2, ok2 := LiFiCostBps(route)
		if !ok2 {
			return nil, ErrSellRouteTooCostly
		}
		gas = q.gasFor(ctx, in, route)
		if l2+gas.bps > q.budgetBps {
			return nil, ErrSellRouteTooCostly
		}
	}

	// She signs this route with her own key, and after that her tokens are
	// gone. The route is LiFi's answer, not ours, so it is checked before it
	// is handed over — see checkSignable.
	if err := q.checkSignable(route, in, usdc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSellUnavailable, err)
	}

	out := &SellSwapQuote{
		Route: route, LiFiQuote: route.Raw,
		CostBps: l, GasBps: gas.bps, FeeBps: fee,
		GasTopUp: gas.topUp, TopUpWei: gas.topUpWei,
		USDCAmount: BaseUnitsToDecimal(route.ToAmount, 6),
	}
	if err := q.priceInFiat(ctx, in.FiatCode, route.ToAmount, out); err != nil {
		return nil, err
	}
	return out, nil
}

// checkSignable refuses a route she must not sign. Every field it reads is
// read from the body she is actually handed (route.Raw), by its exact JSON
// name, the way the app's JSON.parse reads it — and must agree with what this
// server parsed from the same body, so a body built to read one way here and
// another way on her phone is refused rather than checked half-way.
//
//   - WHERE her signature goes: the transaction's `to` and the approval
//     spender must be allowlisted routers on the source chain. Checking the
//     calldata alone would decode a payload "as if" it were a diamond call
//     while `to` named any contract at all — and her approval is unlimited.
//   - WHAT she approves: the token the approval names must be the token she
//     is selling, never another one she happens to hold.
//   - Which chain she signs for, and what the calldata delivers: a route that
//     pays someone else, lands anywhere but USDC on Base, or hands the funds
//     to a destination call would leave §5.3's binding check with nothing to
//     bind and "Finish your sale" with nothing to finish. Same check a
//     converted buy runs before it offers a venue.
func (q *RampSellQuoter) checkSignable(route *LiFiRoute, in SellQuoteInput, usdc string) error {
	signed, err := readSignedRoute(route.Raw)
	if err != nil {
		return err
	}
	if signed.To != route.To || signed.Data != route.Data ||
		signed.ApprovalAddress != route.ApprovalAddress || signed.ApprovalToken != route.ApprovalToken {
		log.Printf("[ramp-sell] ALERT ramp_sell_route_ambiguous_total=1 chain=%d tool=%s", in.FromChainID, route.Tool)
		return fmt.Errorf("%w: the quote body reads differently from the route it was parsed as", ErrTreasuryInvalidRoute)
	}
	if signed.ChainID != in.FromChainID {
		return fmt.Errorf("%w: route is for chain %d, not %d", ErrTreasuryInvalidRoute, signed.ChainID, in.FromChainID)
	}
	allowed := q.routers[in.FromChainID]
	if !routerAllowed(allowed, signed.To) {
		log.Printf("[ramp-sell] ALERT ramp_sell_route_to_refused_total=1 chain=%d to=%s tool=%s", in.FromChainID, signed.To, route.Tool)
		return fmt.Errorf("%w: route targets an address outside the allowlist", ErrTreasuryInvalidRoute)
	}
	if signed.ApprovalAddress != "" {
		if !routerAllowed(allowed, signed.ApprovalAddress) {
			log.Printf("[ramp-sell] ALERT ramp_sell_route_spender_refused_total=1 chain=%d spender=%s tool=%s",
				in.FromChainID, signed.ApprovalAddress, route.Tool)
			return fmt.Errorf("%w: route names a spender outside the allowlist", ErrTreasuryInvalidRoute)
		}
		if !sameTokenAddress(signed.ApprovalToken, in.FromToken) {
			return fmt.Errorf("%w: route approves %q, not the token being sold", ErrTreasuryInvalidRoute, signed.ApprovalToken)
		}
	}
	return checkLiFiDeliveryTo(signed.Data, in.FromChainID, rampSettlementChainID, in.WalletAddress, "evm", usdc)
}

// signedRoute is the part of a LiFi quote body the app acts on when it signs.
type signedRoute struct {
	To, Data                       string
	ChainID                        int64
	ApprovalAddress, ApprovalToken string
}

// readSignedRoute reads signedRoute out of a raw quote body by exact key.
// encoding/json matches struct fields case-insensitively (and takes the last
// of several matching keys), JSON.parse does neither, so the body is walked as
// maps here: a key the app would not read is not read.
func readSignedRoute(raw json.RawMessage) (signedRoute, error) {
	var out signedRoute
	top, err := jsonObject(raw)
	if err != nil {
		return out, fmt.Errorf("%w: quote body: %v", ErrTreasuryInvalidRoute, err)
	}
	tx, err := jsonObject(top["transactionRequest"])
	if err != nil {
		return out, fmt.Errorf("%w: transactionRequest: %v", ErrTreasuryInvalidRoute, err)
	}
	if err := json.Unmarshal(tx["chainId"], &out.ChainID); err != nil {
		return out, fmt.Errorf("%w: transactionRequest.chainId: %v", ErrTreasuryInvalidRoute, err)
	}
	if err := jsonString(tx, "to", &out.To); err != nil {
		return out, err
	}
	if err := jsonString(tx, "data", &out.Data); err != nil {
		return out, err
	}
	// Both optional: a route with no approval step carries neither.
	if estimate, err := jsonObject(top["estimate"]); err == nil {
		if err := jsonString(estimate, "approvalAddress", &out.ApprovalAddress); err != nil {
			return out, err
		}
	}
	if action, err := jsonObject(top["action"]); err == nil {
		if from, err := jsonObject(action["fromToken"]); err == nil {
			if err := jsonString(from, "address", &out.ApprovalToken); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

// jsonString reads obj[key] into dst when it is present. Absent and null both
// leave dst empty, as they do for the app.
func jsonString(obj map[string]json.RawMessage, key string, dst *string) error {
	v, ok := obj[key]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(v, dst); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrTreasuryInvalidRoute, key, err)
	}
	return nil
}

// jsonObject decodes one JSON object into its raw members, by exact key.
func jsonObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, errors.New("missing")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, errors.New("not an object")
	}
	return obj, nil
}

// sellGas is g for one route, and the top-up (if any) it was priced with.
// One value so the bps admitted and the wei later sent come from the same
// route and the same decision, never from two different asks.
type sellGas struct {
	bps      int
	topUp    bool
	topUpWei string
}

// gasFor is g: the Base relay we sponsor, plus a source-chain top-up when she
// holds the token but not the coin that pays for moving it.
func (q *RampSellQuoter) gasFor(ctx context.Context, in SellQuoteInput, route *LiFiRoute) sellGas {
	usd := q.sponsoredGasUSD
	var g sellGas
	if q.needsTopUp(ctx, in, route) {
		usd += TopUpUSDForRoute(route)
		g.topUp, g.topUpWei = true, quotedTopUpWei(route)
	}
	g.bps = USDGasBps(usd, route.FromAmountUSD)
	return g
}

// needsTopUp reports whether this sell would have us send her gas on the
// source chain. Only EVM token sells can: a native-coin sell pays its own gas
// out of what it sends, and a non-EVM source has no sponsor lane.
//
// The threshold is the SAME gas TopUpUSDForRoute prices — the swap plus an
// approve — because a wallet holding the swap's gas to the wei still cannot
// send the approve, and judging her funded there leaves her holding a route
// she cannot execute.
func (q *RampSellQuoter) needsTopUp(ctx context.Context, in SellQuoteInput, route *LiFiRoute) bool {
	if q.nativeBalance == nil || addressFamily(in.FromChainID) != "evm" || isNativeTokenAddress(in.FromToken) {
		return false
	}
	need := RouteGasWei(route, treasuryERC20Gas)
	have, ok := q.nativeBalance(ctx, in.FromChainID, in.FromAddress)
	if need == nil || !ok || have == nil {
		// Either side of the comparison can be unknown: LiFi may not have
		// priced the gas in the coin she pays it in, or her balance may not
		// have been readable. Both are the same unknown, and both are
		// budgeted for rather than folded into a comparison that would
		// quietly answer "she is funded" — pricing a top-up she does not need
		// costs a few bps, skipping one she does strands her mid-sell.
		return true
	}
	return have.Cmp(need) < 0
}

// priceInFiat turns the landed USDC into naira at the rate the offramp will
// actually be opened at (F − B), binding to a live provider quote rather than
// the cached book.
func (q *RampSellQuoter) priceInFiat(ctx context.Context, fiat string, usdc *big.Int, out *SellSwapQuote) error {
	if strings.TrimSpace(fiat) == "" {
		fiat = "NGN"
	}
	market, err := q.quotes.cachedMarketRate(ctx, fiat, "sell")
	if err != nil {
		return err
	}
	// Paycrest prices by order SIZE, so it has to be asked about roughly the
	// right one. The mid rate is only used to land in the right tier; the
	// rate that binds is whatever comes back.
	guess := new(big.Rat).Mul(usdcRat(usdc), new(big.Rat).SetFloat64(market))
	pct, ok := parseSenderFeePercent(q.converted)
	if !ok {
		return ErrSellUnavailable
	}
	venue, err := q.quotes.VenueForFee(ctx, "sell", fiat, guess.FloatString(2),
		rampSettlementChainKey, rampSettlementToken, pct)
	if err != nil {
		return err
	}
	rate, ok := new(big.Rat).SetString(venue.Rate)
	if !ok {
		return fmt.Errorf("%w: unusable sell rate %q", ErrSellUnavailable, venue.Rate)
	}
	out.Rate = venue.Rate
	out.FiatEstimate = fiatAt(usdcRat(usdc), rate)
	return nil
}

// rampSettlementChainKey is the chain key the settlement venue is named by.
// Derived from the chain id would be nicer, but Paycrest's venues are keyed
// by our chain key and Base is the one settlement leg — see §3.2.
const rampSettlementChainKey = "base"

// fiatAt is tokens × rate in fiat, to the kobo (half away from zero). The one
// way a sell's naira figure is computed — the estimate she is quoted and the
// amount the order then owes her — so the two cannot differ by rounding.
func fiatAt(tokens, rate *big.Rat) string {
	return new(big.Rat).Mul(tokens, rate).FloatString(2)
}

// usdcRat is a 6dp USDC amount as an exact rational. Money is never floated
// through a float64 here: 9.98 × 1492.50 has to be 14895.15, not 14895.1499…
func usdcRat(v *big.Int) *big.Rat {
	if v == nil {
		return new(big.Rat)
	}
	return new(big.Rat).SetFrac(v, pow10(6))
}
