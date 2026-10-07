package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
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
// She pays the company take — the Paycrest sender fee, inside the rate she is
// shown — plus the round trip at cost (owner ruling 2026-10-07): l, LiFi's own
// cost, inside the route, and g, the gas we sponsor, recovered as our LiFi
// integrator fee and nothing more. Admission is l + g ≤ the source chain's
// RAMP_CONVERTED_FEE_MAX_BPS. The quote states the round trip as its
// "Conversion (LiFi)" fee line.
//
// A TSA Pay payment's converted payout (SellQuoteInput.TSAPay) keeps its own
// split instead (PayFeeSplit): its F is all-in, B is the slice of F a round
// trip may spend, admission is l + g ≤ B, and B − l − g is charged as our LiFi
// integrator fee. Nothing here moves funds, signs anything or writes a row: it
// is a price.

// rampSettlementChainID / rampSettlementToken are where a swapped sell lands
// before the offramp: USDC on Base, the one leg Paycrest fills reliably and
// the only chain the EIP-3009 relay sponsors.
const (
	rampSettlementChainID int64 = 8453
	rampSettlementToken         = "USDC"
)

// ErrSellRouteTooCostly: the round trip costs more than the sale may pass on
// (its chain's cap, or a TSA Pay payment's B and cap). Refused rather than
// quoted (design §3.2).
var ErrSellRouteTooCostly = errors.New("this token costs too much to sell for naira right now")

// ErrSellRouteUnpriced: LiFi gave the route no USD value, so its cost cannot
// be judged. It is a ErrSellRouteTooCostly to every caller that only checks
// that (a sale keeps its copy); a checkout tells the two apart.
var ErrSellRouteUnpriced = fmt.Errorf("%w: the route has no USD price", ErrSellRouteTooCostly)

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

// ErrSellWalletMCGP: the source is the plain MCGP in her own wallet, which is
// never used to pay or sold for naira (owner decision 2026-09-28). Only
// Spendable MCGP is, through the MCGP sell flow. She can still send it or swap
// it in All Swap, so the message says so.
var ErrSellWalletMCGP = errors.New("MCGP in your wallet can't be used to pay or be sold for naira — only Spendable MCGP can. You can still send it or swap it in All Swap")

// RefuseSource is ErrSellWalletMCGP when the source token is wallet MCGP. It
// is the one gate every sale and every TSA Pay payment passes: Quote runs it
// first, and the pay starter runs it before it locks a payment.
func (q *RampSellQuoter) RefuseSource(chainID int64, token string) error {
	if q != nil && q.chains.IsWalletMCGP(chainID, token) {
		return ErrSellWalletMCGP
	}
	return nil
}

// SellSwapQuote is one priced sell. Route is the executable quote the app
// signs; LiFiQuote is the same answer verbatim, which is what actually
// crosses the wire — the app's own swap signer already speaks it.
type SellSwapQuote struct {
	Route     *LiFiRoute      `json:"-"`
	LiFiQuote json.RawMessage `json:"lifiQuote"`
	// CostBps (l), GasBps (g) and FeeBps (our LiFi integrator fee: g at
	// cost, or B − l − g on a TSA Pay payout) are the budget split. They are
	// what an operator reads when a sell prices differently from what a user
	// reports.
	CostBps int `json:"costBps"`
	GasBps  int `json:"gasBps"`
	FeeBps  int `json:"feeBps"`
	// PassThrough: FeeBps only recovers gas we pay for her — it is not ours.
	// Every ramp sale and every delivery priced at cost carries it.
	PassThrough bool `json:"-"`
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
	// Rate is the Paycrest sell rate with only the company take in it.
	Rate string `json:"rate"`
	// EffectiveRate is FiatEstimate ÷ the USDC the route would have landed at
	// no cost, and Fees the round trip at cost as "Conversion (LiFi)" (spec
	// §14) — on a ramp sale only.
	EffectiveRate string     `json:"effectiveRate,omitempty"`
	Fees          []VenueFee `json:"fees,omitempty"`
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
	// ToChainID, ToToken, ToAddress and BudgetBps re-aim the sale at a
	// DELIVERY instead of an offramp: TSA Pay's crypto route, where the token
	// must reach a merchant's wallet. Zero values keep the sale's own — Base,
	// USDC and WalletAddress. A target that is not Base USDC in her own wallet
	// has no Paycrest leg: no naira estimate, no Base relay in g, and BudgetBps
	// (the whole fee) is what l + g is admitted against. A ramp sale's offramp
	// is given no BudgetBps: its budget is the quoter's own.
	ToChainID int64
	ToToken   string
	ToAddress string
	BudgetBps int
	// Delivery marks the sale as a delivery whatever its target looks like, so
	// it is never priced as an offramp (see targetFor).
	Delivery bool
	// PassThrough prices a delivery at cost: we keep no margin. Admission is
	// still l + g <= BudgetBps, but the unspent rest of the budget is NOT
	// charged as our integrator fee — the only fee on the route is g itself
	// (gas we pay on her behalf), and none at all when we pay none. A
	// marketplace checkout's funding swap (owner decision 2026-09-29).
	PassThrough bool
	// DestGasUSD is gas we will pay on the DESTINATION chain once the route has
	// delivered — a marketplace checkout's top-up of the coin its escrow txs
	// need — priced by the caller, who knows how much the wallet lacks. It is
	// part of g, so it counts against the budget like any gas we sponsor.
	DestGasUSD float64
	// TSAPay prices an offramp as a TSA Pay payment's converted payout, on
	// the payment's own split (PayFeeSplit) rather than the ramp's take. A
	// delivery ignores it.
	TSAPay bool
}

// deliveryTarget is where a quoted route must deliver, and what it may spend.
type deliveryTarget struct {
	chainID   int64
	token     string
	address   string
	budgetBps int
	// offramp: the route lands USDC on Base in HER wallet for the Paycrest
	// leg. Only then is there a Base relay to sponsor and a naira figure to show.
	offramp bool
	// senderFee is the Paycrest sender fee an offramp's naira is priced at:
	// the take, or a TSA Pay payout's F − B. atCost marks a ramp sale, whose
	// round trip is passed on at cost under budgetBps, its chain's cap.
	senderFee string
	atCost    bool
}

// targetFor resolves in's delivery target, defaulting each zero field to the
// ordinary sale's. usdc is Base USDC's configured address.
func (q *RampSellQuoter) targetFor(in SellQuoteInput, usdc string) deliveryTarget {
	t := deliveryTarget{chainID: in.ToChainID, token: strings.TrimSpace(in.ToToken),
		address: strings.TrimSpace(in.ToAddress), budgetBps: in.BudgetBps}
	if t.chainID == 0 {
		t.chainID = rampSettlementChainID
	}
	if t.token == "" {
		t.token = usdc
	}
	if t.address == "" {
		t.address = in.WalletAddress
	}
	// A delivery is never an offramp, even one that happens to name Base USDC in
	// the payer's own wallet (a merchant paying themselves): it must get no Base
	// relay gas and no naira estimate. Spelling out the default target on an
	// ordinary sale, by contrast, is still that sale.
	t.offramp = !in.Delivery && t.chainID == rampSettlementChainID &&
		sameTokenAddress(t.token, usdc) && sameAddress(t.address, in.WalletAddress)
	return t
}

// TokenPriceUSD is LiFi's USD price for a token, read through the quoter's own
// LiFi client — the one the ramp already holds, so TSA Pay prices the merchant's
// token with it instead of building a second client.
func (q *RampSellQuoter) TokenPriceUSD(ctx context.Context, chainID int64, token string) (*big.Rat, int, error) {
	p, ok := q.lifi.(tokenPricer)
	if !ok {
		return nil, 0, fmt.Errorf("%w: the ramp's LiFi client cannot price a token", ErrSellUnavailable)
	}
	return p.TokenPriceUSD(ctx, chainID, token)
}

// tokenPricer is the price read *LiFiQuoteClient adds to the quoter interface.
// It is a separate interface so the quoter's fakes need not implement it; a
// client without it answers ErrSellUnavailable (never a wrong price).
type tokenPricer interface {
	TokenPriceUSD(ctx context.Context, chainID int64, token string) (*big.Rat, int, error)
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
	// take is the company take as the sender fee a ramp sale's offramp is
	// sent at — PaycrestOrderService.TakePercent, so the estimate and the
	// order cannot drift.
	take string
	// pay is TSA Pay's own split, for a payment's converted payout — the one
	// PaycrestOrderService opens that payout at (SetPayFeeSplit).
	pay PayFeeSplit
	// sponsoredGasUSD is the EIP-3009 relay we pay on Base to move her USDC
	// into the offramp.
	sponsoredGasUSD float64
	nativeBalance   nativeBalanceReader
	// routers is, per source chain id, the contracts a route may send her
	// transaction to or have her approve — the same pinned allowlist the
	// treasury signs against. A chain with none is refused: see checkSignable.
	routers map[int64][]common.Address
}

func NewRampSellQuoter(lifi lifiQuoter, quotes *PaycrestQuoteService, chains *BlockchainService, takePercent string, sponsoredGasUSD float64) *RampSellQuoter {
	return &RampSellQuoter{
		lifi: lifi, quotes: quotes, chains: chains,
		take: takePercent, sponsoredGasUSD: sponsoredGasUSD,
	}
}

// SetPayFeeSplit wires TSA Pay's split, for SellQuoteInput.TSAPay offramps.
func (q *RampSellQuoter) SetPayFeeSplit(p PayFeeSplit) { q.pay = p }

// SetNativeBalanceReader wires the source-chain gas check. Unset, no top-up
// is ever priced in — the shape a chain with no sponsor lane should have,
// since we would not be sending one.
func (q *RampSellQuoter) SetNativeBalanceReader(f nativeBalanceReader) { q.nativeBalance = f }

// SetRouterAllowlist wires the contracts a route may target, per source chain
// id. Unset, every sell is refused: a route whose target cannot be checked is
// never handed to her to sign.
func (q *RampSellQuoter) SetRouterAllowlist(routers map[int64][]common.Address) { q.routers = routers }

func (q *RampSellQuoter) Quote(ctx context.Context, in SellQuoteInput) (*SellSwapQuote, error) {
	if q == nil || q.lifi == nil || q.chains == nil || in.BudgetBps < 0 || in.DestGasUSD < 0 || math.IsNaN(in.DestGasUSD) {
		return nil, ErrSellUnavailable
	}
	// Pass-through as an input is a delivery's: an offramp's pricing is the
	// quoter's own (at cost for a ramp sale, the split for a TSA Pay payout).
	if in.PassThrough && (!in.Delivery || in.BudgetBps <= 0) {
		return nil, ErrSellUnavailable
	}
	if err := q.RefuseSource(in.FromChainID, in.FromToken); err != nil {
		return nil, err
	}
	if family := addressFamily(in.FromChainID); family != "evm" {
		return nil, fmt.Errorf("%w (%s)", ErrSellSourceUnsupported, family)
	}
	// The token must be a 0x contract address. RefuseSource matches the
	// configured MCGP address; a spelling it does not recognise (no 0x, a
	// symbol LiFi might resolve itself) must not reach LiFi and come back as
	// MCGP. checkSignable re-checks the route's own action.fromToken.
	if !isHexTokenAddress(in.FromToken) {
		return nil, fmt.Errorf("%w: token %q is not a 0x token address", ErrSellBadSourceAddress, in.FromToken)
	}
	if err := validateAddressForChain(in.FromChainID, in.FromAddress, nil); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSellBadSourceAddress, err)
	}
	usdc := q.chains.TokenAddressForChainID(rampSettlementChainID, rampSettlementToken)
	// The receiver is her EVM wallet on Base, whoever the sender is — or, for
	// a delivery, the named recipient on the named chain. An address that is
	// not one on that chain (or on a chain nothing can decode a receiver for)
	// never reaches LiFi.
	target := q.targetFor(in, usdc)
	// Only an offramp needs Paycrest's quote service and a budget of the
	// quoter's own; a delivery names its token and is given its whole budget by
	// the caller, so a quoter wired without Paycrest (the marketplace's) still
	// prices one.
	if target.offramp && !q.priceOfframp(in, &target) {
		return nil, ErrSellUnavailable
	}
	if target.token == "" || target.budgetBps <= 0 {
		return nil, ErrSellUnavailable
	}
	if err := validateAddressForChain(target.chainID, target.address, nil); err != nil {
		return nil, fmt.Errorf("%w: receiving wallet: %v", ErrSellBadSourceAddress, err)
	}
	// Slippage is a tolerance, never a cost: it is not in l. But a route
	// admitted at 50 bps of total cost must not be quoted with 50 bps of
	// tolerance on top when the pair cannot justify it — and the destination
	// is always USDC, so a stable source makes a stable pair. Same decision,
	// same function, as a converted buy's destination.
	stableSource, _, _ := isStableDestination(q.chains, in.FromChainID, in.FromToken)
	req := LiFiQuoteRequest{
		FromChainID: in.FromChainID, ToChainID: target.chainID,
		FromToken: in.FromToken, ToToken: target.token,
		FromAddress: in.FromAddress, ToAddress: target.address,
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
		return nil, ErrSellRouteUnpriced
	}
	gas := q.gasFor(ctx, in, first, target.offramp)
	if in.PassThrough || target.atCost {
		return q.passThrough(ctx, req, in, usdc, target, first, l, gas)
	}

	route := first
	fee := target.budgetBps - l - gas.bps
	// raisedLimit > 0 marks a TSA Pay payout dearer than B that is still
	// offered at a higher fee: the most LiFi's route (her swap cost plus our
	// fee) may take on top of Paycrest's F − B share.
	raisedLimit := 0
	if fee < 0 {
		limit, ok := q.raisedCostLimit(in, target)
		// Our fee recovers the gas we pay for her sale (the Base relay and
		// any top-up) plus headroom for the fee-carrying route's own gas, so
		// our share stays Paycrest's F − B.
		fee = gas.bps + routeBudgetHeadroomBps
		if !ok || l+fee > limit {
			return nil, ErrSellRouteTooCostly
		}
		raisedLimit = limit
	}
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
			return nil, ErrSellRouteUnpriced
		}
		gas = q.gasFor(ctx, in, route, target.offramp)
		if raisedLimit > 0 {
			// Raised: she may pay up to the cap, and our fee must still cover
			// the gas THIS route has us pay.
			if l2 > raisedLimit || gas.bps > fee {
				return nil, ErrSellRouteTooCostly
			}
		} else if l2+gas.bps > target.budgetBps {
			return nil, ErrSellRouteTooCostly
		}
	}

	return q.finish(ctx, route, in, usdc, target, l, gas, fee)
}

// finish checks the chosen route is one she may sign and builds the quote.
func (q *RampSellQuoter) finish(ctx context.Context, route *LiFiRoute, in SellQuoteInput, usdc string, target deliveryTarget, l int, gas sellGas, fee int) (*SellSwapQuote, error) {
	// She signs this route with her own key, and after that her tokens are
	// gone. The route is LiFi's answer, not ours, so it is checked before it
	// is handed over — see checkSignable.
	if err := q.checkSignable(route, in, usdc); err != nil {
		if errors.Is(err, ErrSellWalletMCGP) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrSellUnavailable, err)
	}

	out := &SellSwapQuote{
		Route: route, LiFiQuote: route.Raw,
		CostBps: l, GasBps: gas.bps, FeeBps: fee,
		GasTopUp: gas.topUp, TopUpWei: gas.topUpWei,
	}
	if !target.offramp {
		// A delivery has no naira leg, and what lands is not USDC on Base.
		return out, nil
	}
	out.USDCAmount = BaseUnitsToDecimal(route.ToAmount, 6)
	if err := q.priceInFiat(ctx, in.FiatCode, target.senderFee, route.ToAmount, out); err != nil {
		return nil, err
	}
	if target.atCost {
		sellFeeLines(out, route.ToAmount)
	}
	return out, nil
}

// priceOfframp gives an offramp its budget and sender fee, or false when it
// cannot be priced: a ramp sale passes its round trip on at cost under the
// source chain's cap and is sent at the take (a budget the caller names is
// refused); a TSA Pay payout keeps its split.
func (q *RampSellQuoter) priceOfframp(in SellQuoteInput, t *deliveryTarget) bool {
	if q.quotes == nil {
		return false
	}
	if in.TSAPay {
		// As before the ruling: a budget the caller names is used as given
		// and never raised (raisedCostLimit); otherwise the split's B.
		if t.budgetBps == 0 {
			t.budgetBps = q.pay.BudgetBps
		}
		t.senderFee = q.pay.SenderFee(true)
		return t.senderFee != ""
	}
	if _, ok := parseSenderFeePercent(q.take); !ok || in.BudgetBps != 0 {
		return false
	}
	chainKey, _ := q.chains.ChainNameForID(in.FromChainID)
	t.senderFee, t.budgetBps, t.atCost = q.take, q.quotes.convertedFeeMaxBps(chainKey), true
	return true
}

// sellFeeLines states a ramp sale's round trip at cost (spec §14): what LiFi
// took and the gas we recover — CostBps + FeeBps of what would have landed at
// no cost — as one "Conversion (LiFi)" line in USDC at the display rate, and
// the all-in rate on that no-cost amount (rounded down: never flattering).
// Gross − the line = what lands, exactly.
func sellFeeLines(out *SellSwapQuote, landed *big.Int) {
	total := out.CostBps + out.FeeBps
	rate, ok := new(big.Rat).SetString(strings.TrimSpace(out.Rate))
	fiat, okF := new(big.Rat).SetString(strings.TrimSpace(out.FiatEstimate))
	if total <= 0 || total >= 10_000 || landed == nil || landed.Sign() <= 0 || !ok || !okF {
		return
	}
	gross := new(big.Int).Mul(landed, big.NewInt(10_000))
	gross.Quo(gross, big.NewInt(int64(10_000-total)))
	cost := new(big.Int).Sub(gross, landed)
	if cost.Sign() <= 0 {
		return
	}
	out.Fees = []VenueFee{tokenFeeLine(FeeKindConversion, ConversionLiFiLabel, FeeProviderLiFi, cost, 6, rate)}
	eff := new(big.Rat).Quo(fiat, usdcRat(gross))
	kobo := new(big.Int).Quo(new(big.Int).Mul(eff.Num(), big.NewInt(100)), eff.Denom())
	out.EffectiveRate = FormatMinor(kobo.String())
}

// passThrough prices at cost — a delivery given in.PassThrough, and every ramp
// sale's offramp. first is the fee-free route and l, gas its cost and gas. She pays LiFi's cost plus the gas we pay
// for her, and nothing else: the integrator fee is exactly g — recovering gas,
// never a margin — and absent when g is 0. Admission is l + g <= the budget;
// the unspent rest of the budget is simply not charged.
//
// When g > 0 the fee-carrying route is asked for and admitted on its own
// figures: its cost already includes our fee, so l2 alone is what she pays. A
// route whose gas re-prices above the fee is still offered (her price is what
// the budget caps) and the few bps we under-recover are logged.
func (q *RampSellQuoter) passThrough(ctx context.Context, req LiFiQuoteRequest, in SellQuoteInput, usdc string, target deliveryTarget, first *LiFiRoute, l int, gas sellGas) (*SellSwapQuote, error) {
	if l+gas.bps > target.budgetBps {
		return nil, ErrSellRouteTooCostly
	}
	if gas.bps == 0 {
		return passedThrough(q.finish(ctx, first, in, usdc, target, l, gas, 0))
	}
	fee := gas.bps
	req.FeeBps = fee
	route, err := q.lifi.Quote(ctx, req)
	if err != nil {
		if errors.Is(err, ErrLiFiFeeUnconfigured) {
			// The gas we would pay for her could not be recovered on this chain.
			return nil, fmt.Errorf("%w: %v", ErrSellUnavailable, err)
		}
		return nil, err
	}
	if route == nil {
		return nil, ErrSellUnavailable
	}
	l2, ok := LiFiCostBps(route)
	if !ok {
		return nil, ErrSellRouteUnpriced
	}
	if l2 > target.budgetBps {
		return nil, ErrSellRouteTooCostly
	}
	gas2 := q.gasFor(ctx, in, route, target.offramp)
	if gas2.bps > fee {
		log.Printf("[ramp-sell] pass-through gas under-recovered by %d bps chain=%d tool=%s", gas2.bps-fee, in.FromChainID, route.Tool)
	}
	// CostBps reports LiFi's own cost, without the gas recovery inside l2.
	return passedThrough(q.finish(ctx, route, in, usdc, target, max(l2-fee, 0), gas2, fee))
}

// passedThrough marks a pass-through quote: its fee recovers gas, not ours.
func passedThrough(out *SellSwapQuote, err error) (*SellSwapQuote, error) {
	if out != nil {
		out.PassThrough = true
	}
	return out, err
}

// raisedCostLimit is, for a TSA Pay payout dearer than B, the most LiFi's
// route may take — her swap cost plus our fee — so that with Paycrest's F − B
// her all-in fee fits the source chain's cap. ok is false when the sale may
// not be raised: a delivery was given its whole fee by the caller.
func (q *RampSellQuoter) raisedCostLimit(in SellQuoteInput, target deliveryTarget) (int, bool) {
	if !target.offramp || in.BudgetBps != 0 {
		return 0, false
	}
	paycrestBps, ok := feePercentToBps(target.senderFee)
	if !ok {
		return 0, false
	}
	chainKey, _ := q.chains.ChainNameForID(in.FromChainID)
	limit := q.quotes.convertedFeeMaxBps(chainKey) - paycrestBps
	return limit, limit > 0
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
//   - WHICH token arrives: LiFi's calldata names the receiver and the
//     destination chain but not the asset, so the quote's own action.toToken
//     and action.toChainId must be the target's. A route delivering USDC.e
//     where the merchant asked for native USDC is refused before she signs,
//     not discovered by the delivery proof after her money has moved.
func (q *RampSellQuoter) checkSignable(route *LiFiRoute, in SellQuoteInput, usdc string) error {
	target := q.targetFor(in, usdc)
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
	// What the route actually spends, not only what she asked to sell: a
	// route that resolves to wallet MCGP is refused like a request for it.
	if signed.ApprovalToken != "" {
		if err := q.RefuseSource(in.FromChainID, signed.ApprovalToken); err != nil {
			log.Printf("[ramp-sell] ALERT ramp_sell_route_wallet_mcgp_total=1 chain=%d asked=%s tool=%s", in.FromChainID, in.FromToken, route.Tool)
			return err
		}
	}
	if signed.DestChainID != target.chainID {
		return fmt.Errorf("%w: route's action delivers on chain %d, not %d", ErrTreasuryInvalidRoute, signed.DestChainID, target.chainID)
	}
	if !sameTokenAddress(signed.DestToken, target.token) {
		log.Printf("[ramp-sell] ALERT ramp_sell_route_dest_token_refused_total=1 chain=%d dest=%s tool=%s", target.chainID, signed.DestToken, route.Tool)
		return fmt.Errorf("%w: route delivers %q, not the token owed", ErrTreasuryInvalidRoute, signed.DestToken)
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
	return checkLiFiDeliveryTo(signed.Data, in.FromChainID, target.chainID, target.address, "evm", target.token)
}

// signedRoute is the part of a LiFi quote body the app acts on when it signs,
// and what the quote says it delivers.
type signedRoute struct {
	To, Data                       string
	ChainID                        int64
	ApprovalAddress, ApprovalToken string
	DestToken                      string
	DestChainID                    int64
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
	action, err := jsonObject(top["action"])
	if err != nil {
		return out, fmt.Errorf("%w: action: %v", ErrTreasuryInvalidRoute, err)
	}
	if from, err := jsonObject(action["fromToken"]); err == nil {
		if err := jsonString(from, "address", &out.ApprovalToken); err != nil {
			return out, err
		}
	}
	// Required: a quote that does not say what it delivers cannot be checked.
	to, err := jsonObject(action["toToken"])
	if err != nil {
		return out, fmt.Errorf("%w: action.toToken: %v", ErrTreasuryInvalidRoute, err)
	}
	if err := jsonString(to, "address", &out.DestToken); err != nil {
		return out, err
	}
	if err := json.Unmarshal(action["toChainId"], &out.DestChainID); err != nil {
		return out, fmt.Errorf("%w: action.toChainId: %v", ErrTreasuryInvalidRoute, err)
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

// gasFor is g: the Base relay we sponsor (only when an offramp follows), plus a
// source-chain top-up when she holds the token but not the coin that pays for
// moving it.
func (q *RampSellQuoter) gasFor(ctx context.Context, in SellQuoteInput, route *LiFiRoute, offramp bool) sellGas {
	usd := in.DestGasUSD
	if offramp {
		usd += q.sponsoredGasUSD
	}
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
// The threshold is the SAME figure the top-up is sized from
// (routeGasReserveWei): what a node reserves for the approve and the swap, not
// what they are estimated to use — a wallet holding the estimate is refused.
func (q *RampSellQuoter) needsTopUp(ctx context.Context, in SellQuoteInput, route *LiFiRoute) bool {
	if q.nativeBalance == nil || addressFamily(in.FromChainID) != "evm" || isNativeTokenAddress(in.FromToken) {
		return false
	}
	need := routeGasReserveWei(route)
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

// priceInFiat turns the landed USDC into naira at the sender fee the offramp
// will actually be opened at (the take, or a TSA Pay payout's F − B), binding
// to a live provider quote rather than the cached book.
func (q *RampSellQuoter) priceInFiat(ctx context.Context, fiat, senderFee string, usdc *big.Int, out *SellSwapQuote) error {
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
	pct, ok := parseSenderFeePercent(senderFee)
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

// isHexTokenAddress is a 0x-prefixed 20-byte hex address. common.IsHexAddress
// alone also accepts the bare 40 hex digits.
func isHexTokenAddress(token string) bool {
	t := strings.TrimSpace(token)
	return len(t) > 2 && (t[:2] == "0x" || t[:2] == "0X") && common.IsHexAddress(t)
}
