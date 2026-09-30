package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// marketplace_funding.go — "pay with another token" at marketplace checkout
// (owner decision 2026-09-29).
//
// ProductEscrowV2 records msg.sender as the buyer, and only the buyer can
// confirm or be refunded, so the escrow must be paid from HER wallet on Sonic.
// A checkout funded with any other token therefore runs in two halves:
//
//  1. One LiFi swap, signed by her, delivers the order token (Sonic USDC or
//     USDT) into her own wallet on Sonic — exact-output, sized by this server
//     from the orders it holds for the checkout group, less what she already
//     holds there. This is TSA Pay's delivery machinery reused: the same quoter
//     (in Delivery + PassThrough mode), the same intent (purpose marketplace,
//     referencing the group) and the same on-chain delivery proof.
//  2. The unchanged escrow flow (approve + createOrder) per order, from her
//     wallet, exactly as a stablecoin checkout. If she holds none of Sonic's
//     coin to sign those, the quote priced a top-up of S in (the funding's own
//     Sonic lane), sent once the delivery is proven.
//
// Nothing here holds her funds: the swap lands in her wallet whatever becomes
// of the intent, so an intent that fails or is written off never strands
// money — at worst she holds the order token and pays with it directly.
//
// Pricing is pass-through (owner decision): she pays LiFi's cost and the gas we
// pay for her, and nothing else; a route costing more than the budget (2% of
// the order total) is refused.
//
// Orders created for a funding hold stock before any money exists, so the hold
// is earned: it is kept alive only once LiFi has seen her swap (or for a short
// grace after she reports it), never past the intent's creation + 2h, and a
// buyer has at most one such checkout awaiting payment.

var (
	// ErrFundingUnavailable: the funding cannot be priced or started for a
	// reason of ours (a read that failed, a price we cannot get).
	ErrFundingUnavailable = errors.New("paying with another token isn't available right now — try again, or pay with USDC or USDT")
	// ErrFundingGroup: no checkout of hers by that id awaits payment in a
	// stablecoin — not hers, already paid or cancelled, MCGP, or mixed tokens.
	ErrFundingGroup = errors.New("these orders can't be paid with another token")
	// ErrFundingNotNeeded: her wallet already holds the whole total.
	ErrFundingNotNeeded = errors.New("your wallet already holds enough to pay — pay with it directly")
	// ErrFundingSameToken: she picked the order token itself on Sonic.
	ErrFundingSameToken = errors.New("that's the token this order is paid in — pay with it directly")
	// ErrFundingSource: the source is not her own wallet on an EVM network.
	ErrFundingSource = errors.New("pay from your own wallet on an EVM network")
	// ErrFundingTooCostly: the route costs more than the budget.
	ErrFundingTooCostly = errors.New("converting this token would cost more than 2% of your order — pick another token, or pay with MCGP or USDC sonic")
	// ErrFundingTooSmall: she already holds most of the total, so the swap is
	// too small to be worth its fixed costs.
	ErrFundingTooSmall = errors.New("you already hold most of this order's total, so swapping the rest would cost too much for its size — add a little to your wallet and pay with it directly")
	// ErrFundingNeedsGas: she will need Sonic's coin to pay the sellers after
	// the swap and we cannot cover it. Refused before she signs anything.
	ErrFundingNeedsGas = errors.New("after the swap you'd need a little S on Sonic to pay the seller, and we can't cover it right now — add some S to your wallet first, or pay with USDC or USDT")
	// ErrFundingKYC: paying this way needs gas we would send her (on the
	// source chain or on Sonic), and we only sponsor verified accounts.
	ErrFundingKYC = errors.New("verify your identity to have the network fees covered — or add a little gas to your wallet yourself and try again")
	// ErrFundingKeepS: she pays with S on Sonic, and what the swap spends would
	// leave too little S to sign the escrow payments afterwards.
	ErrFundingKeepS = errors.New("paying with S would leave too little S to pay the network fees afterwards — keep a little S, or pay with another token")
	// ErrFundingInProgress: she already has a checkout waiting on a funding
	// swap (or its orders still wait to be paid). One at a time.
	ErrFundingInProgress = errors.New("you already have a checkout being paid with another token — it closes on its own once that swap lands or is refunded; until then, finish it from the checkout or pay this order with Sonic USDC or USDT")
)

// Funding lifetimes. An unsigned intent is closed after fundingUnsignedTTL. A
// swap LiFi has not seen from her address within fundingUnseenGiveUp is taken
// for one never broadcast and closed. While LiFi follows it (or for
// fundingUnseenGrace after she reports it), the checkout's orders are kept
// alive — but never past the intent's creation + fundingHoldMax: each touch
// buys UnpaidOrderTTL, so touching stops at creation + fundingHoldMax −
// UnpaidOrderTTL. A swap never seen to land is written off after
// fundingGiveUp (it pays her own wallet whenever it does land).
const (
	fundingUnsignedTTL  = 30 * time.Minute
	fundingUnseenGrace  = 10 * time.Minute
	fundingUnseenGiveUp = time.Hour
	fundingHoldMax      = 2 * time.Hour
	fundingGiveUp       = 24 * time.Hour
	fundingBatch        = 200
	fundingMaxPages     = 5
	// fundingMaxBudgetBps bounds the budget as a share of the SWAP: a tiny
	// top-up swap is not allowed to cost half of itself however large the
	// order behind it.
	fundingMaxBudgetBps = 2_000
)

// fundingQuoter prices the swap. *RampSellQuoter fills it.
type fundingQuoter interface {
	Quote(ctx context.Context, in SellQuoteInput) (*SellSwapQuote, error)
	RefuseSource(chainID int64, token string) error
	TokenPriceUSD(ctx context.Context, chainID int64, token string) (*big.Rat, int, error)
}

// fundingIntents is the intent store the funding drives. *SellIntentService.
type fundingIntents interface {
	Create(ctx context.Context, user *models.User, in CreateSellIntentInput) (*models.RampSellIntent, *SellSwapQuote, error)
	RecordSwap(ctx context.Context, userID, id uuid.UUID, txHash string) error
	ConfirmDelivery(ctx context.Context, id uuid.UUID) (string, error)
	Abandon(ctx context.Context, userID, id uuid.UUID) error
	AbandonForReference(ctx context.Context, id uuid.UUID, swapTxHash *string) error
	Get(ctx context.Context, userID, id uuid.UUID) (*models.RampSellIntent, error)
	OpenFor(ctx context.Context, userID uuid.UUID, purpose string) (*models.RampSellIntent, error)
	IntentTopUpStore
	DestTopUpStore
}

// SourceGasSponsor sends the source chain's coin for her swap: the ramp's own
// sponsor lanes (*RampSponsorship), exactly as for a sale.
type SourceGasSponsor interface {
	TopUpForIntent(ctx context.Context, user *models.User, intent *models.RampSellIntent, store IntentTopUpStore) (GasTopUpResult, error)
}

// DestGasSponsor sends S on Sonic for her escrow txs: the funding's OWN Sonic
// lane (a *RampSponsorship holding only that lane), never a ramp lane.
type DestGasSponsor interface {
	TopUpDestForIntent(ctx context.Context, user *models.User, intent *models.RampSellIntent, store DestTopUpStore) (GasTopUpResult, error)
	CanSponsorChain(chainID int64) bool
	CheckTopUp(ctx context.Context, chainID int64, amount *big.Int) error
}

// OrderPullPricer is what the escrow will pull from her wallet for a checkout's
// orders: each total plus the escrow's own gas fee, read the way prepare-approve
// reads it (rates resolved once per token). The checkout handler fills it, so
// the funding and the approve can never size the same order two ways.
type OrderPullPricer interface {
	GroupPull(ctx context.Context, orders []models.Order) (*big.Int, error)
}

// FundingChain is the escrow chain's reads: her balance of the order token and
// of the chain's coin, and the gas price the escrow txs will pay.
type FundingChain interface {
	TokenBalance(ctx context.Context, token, wallet string) (*big.Int, error)
	NativeBalance(ctx context.Context, wallet string) (*big.Int, error)
	GasPrice(ctx context.Context) (*big.Int, error)
}

// MarketplaceFundingConfig is where a checkout is paid and what its funding
// may cost.
type MarketplaceFundingConfig struct {
	// ChainID is the escrow chain (Sonic mainnet, 146).
	ChainID int64
	// Tokens are the order tokens a checkout can be funded into, by symbol.
	Tokens map[string]string
	// BudgetBps caps LiFi's cost plus our gas, as bps of the order total.
	BudgetBps int
	// GasPerOrder is one order's approve + createOrder in gas units.
	GasPerOrder uint64
}

// MarketplaceFundingService drives a checkout's funding swap. Every figure is
// its own: the amount from the orders it holds, the balance from the chain.
type MarketplaceFundingService struct {
	db      *gorm.DB
	cfg     MarketplaceFundingConfig
	quoter  fundingQuoter
	intents fundingIntents
	pulls   OrderPullPricer
	chain   FundingChain
	// source / dest send gas; nil means none can be sent, so a wallet that
	// would need it is refused before signing.
	source SourceGasSponsor
	dest   DestGasSponsor
	now    func() time.Time
}

func NewMarketplaceFundingService(db *gorm.DB, cfg MarketplaceFundingConfig, quoter fundingQuoter, intents fundingIntents,
	pulls OrderPullPricer, chain FundingChain, now func() time.Time) *MarketplaceFundingService {
	if now == nil {
		now = time.Now
	}
	return &MarketplaceFundingService{db: db, cfg: cfg, quoter: quoter, intents: intents, pulls: pulls, chain: chain, now: now}
}

// SetSponsors wires gas sponsorship. Pass nil interfaces, never typed nils.
func (s *MarketplaceFundingService) SetSponsors(source SourceGasSponsor, dest DestGasSponsor) {
	s.source, s.dest = source, dest
}

// SetOrderPulls wires the order pricer (the checkout handler, built after this).
func (s *MarketplaceFundingService) SetOrderPulls(p OrderPullPricer) { s.pulls = p }

func (s *MarketplaceFundingService) ready() bool {
	return s != nil && s.db != nil && s.quoter != nil && s.intents != nil && s.pulls != nil &&
		s.chain != nil && s.cfg.ChainID > 0 && s.cfg.BudgetBps > 0 && len(s.cfg.Tokens) > 0
}

// Enabled reports the funding routes can serve.
func (s *MarketplaceFundingService) Enabled() bool { return s.ready() }

// FundingSource is the token she pays with: its chain, its address, and her
// address there (which must be her own proven wallet).
type FundingSource struct {
	ChainID int64
	Token   string
	Address string
}

// fundingPlan is one checkout's funding, sized entirely by the server.
type fundingPlan struct {
	orders    int // how many escrows the funds will pay
	symbol    string
	tokenAddr string
	total     *big.Int // what the escrow will pull, all orders
	balance   *big.Int // what her wallet already holds of it
	short     *big.Int // what the swap must deliver at least (the proof's floor)
	toAmount  *big.Int // short, padded for slippage: what the swap is asked for
	budgetBps int
	// budgetCapped: the budget hit fundingMaxBudgetBps (a small top-up swap).
	budgetCapped bool
	// Sonic gas: what her wallet must hold for the escrow txs (sonicNeed) and
	// holds now (sonicHave). destNeed/destShort are set when we top her up:
	// destShort (need − have now) is the most that top-up may ever send.
	sonicNeed, sonicHave *big.Int
	destNeed, destShort  *big.Int
	destGasUSD           float64
	// nativeSource: she pays with S itself, so the swap spends from the very
	// balance the escrow txs need; checked against the route (afterQuote).
	nativeSource bool
}

// FundingQuote is what paying the checkout with one token costs her. Amounts
// are base units: the source token's, and the order token's (6 dp).
type FundingQuote struct {
	GroupID       string  `json:"groupId"`
	Orders        int     `json:"orders"`
	Token         string  `json:"token"`
	TokenAddress  string  `json:"tokenAddress"`
	ChainID       int64   `json:"chainId"`
	OrderTotal    string  `json:"orderTotal"`
	WalletBalance string  `json:"walletBalance"`
	Shortfall     string  `json:"shortfall"`
	ToAmount      string  `json:"toAmount"`
	FromAmount    string  `json:"fromAmount"`
	FromAmountUSD float64 `json:"fromAmountUsd"`
	// LiFiCostUSD is LiFi's own cost of the route; GasUSD the gas we pay for her
	// (source-chain top-up and Sonic top-up), recovered inside the route. She
	// pays these two and nothing else.
	LiFiCostUSD float64 `json:"lifiCostUsd"`
	GasUSD      float64 `json:"gasUsd"`
	CostBps     int     `json:"costBps"`
	GasBps      int     `json:"gasBps"`
	BudgetBps   int     `json:"budgetBps"`
	ETASeconds  int     `json:"etaSeconds"`
	Tool        string  `json:"tool"`
	// SourceGasTopUp: we send gas on the source chain before she signs.
	// DestGasTopUp: we send S on Sonic once the swap lands.
	SourceGasTopUp bool `json:"sourceGasTopUp"`
	DestGasTopUp   bool `json:"destGasTopUp"`
}

// CanStartCheckout refuses a new funded checkout while she has one in
// progress: an open funding intent, or orders created for a funding that still
// await payment. The one rule order creation and the cart quote share.
func (s *MarketplaceFundingService) CanStartCheckout(ctx context.Context, userID uuid.UUID) error {
	if !s.ready() {
		return ErrFundingUnavailable
	}
	open, err := s.intents.OpenFor(ctx, userID, models.SellIntentPurposeMarketplace)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrFundingUnavailable, err)
	}
	if open != nil {
		return ErrFundingInProgress
	}
	var pending int64
	if err := s.db.WithContext(ctx).Model(&models.Order{}).
		Where("buyer_id = ? AND fund_with_swap = ? AND status = ?", userID, true, models.OrderStatusPendingPayment).
		Count(&pending).Error; err != nil {
		return fmt.Errorf("%w: %v", ErrFundingUnavailable, err)
	}
	if pending > 0 {
		return ErrFundingInProgress
	}
	return nil
}

// Quote prices funding groupID with src. It creates nothing.
func (s *MarketplaceFundingService) Quote(ctx context.Context, user *models.User, groupID uuid.UUID, src FundingSource) (*FundingQuote, error) {
	if !s.ready() {
		return nil, ErrFundingUnavailable
	}
	plan, err := s.plan(ctx, user, groupID, src)
	if err != nil {
		return nil, err
	}
	return s.quote(ctx, user, groupID, src, plan)
}

// QuoteTotal prices funding a checkout that does not exist yet: the buyer's
// active cart, priced by the checkout's own code into `total` (what the escrow
// will pull, all orders) of `symbol` over `orders` escrows. The caller computes
// total from the server's cart — never from a client's figure. It shows her the
// cost before any order takes stock (and refuses while another funded checkout
// of hers is in progress, before its orders could be created); Start then
// re-plans from the orders themselves, and the app holds the signed route to
// what she was shown.
func (s *MarketplaceFundingService) QuoteTotal(ctx context.Context, user *models.User, symbol string, total *big.Int, orders int, src FundingSource) (*FundingQuote, error) {
	if !s.ready() {
		return nil, ErrFundingUnavailable
	}
	if user == nil {
		return nil, ErrFundingSource
	}
	if err := s.CanStartCheckout(ctx, user.ID); err != nil {
		return nil, err
	}
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if total == nil || total.Sign() <= 0 || orders <= 0 || s.cfg.Tokens[symbol] == "" {
		return nil, ErrFundingGroup
	}
	plan, err := s.size(ctx, user, src, symbol, total, orders)
	if err != nil {
		return nil, err
	}
	return s.quote(ctx, user, uuid.Nil, src, plan)
}

// quote asks the route for a plan and holds it to everything Start's intent
// will demand of it, so she is never shown a price that cannot be started.
func (s *MarketplaceFundingService) quote(ctx context.Context, user *models.User, groupID uuid.UUID, src FundingSource, plan *fundingPlan) (*FundingQuote, error) {
	q, err := s.quoter.Quote(ctx, s.quoteInput(user, src, plan))
	if err != nil {
		return nil, s.quoteError(plan, err)
	}
	if q == nil || q.Route == nil || q.Route.ToAmount == nil || q.Route.ToAmount.Cmp(plan.toAmount) < 0 || q.Route.ToAmountMin == nil {
		return nil, ErrFundingUnavailable
	}
	if err := checkDeliveryRoute(s.intentInput(user, groupID, src, plan), q.Route); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFundingUnavailable, err)
	}
	if err := s.afterQuote(user, plan, q); err != nil {
		return nil, err
	}
	return s.view(groupID, plan, q), nil
}

// FundingStart is a started funding: the intent, the route she signs, and the
// priced breakdown she agreed to.
type FundingStart struct {
	Intent *models.RampSellIntent
	Swap   *SellSwapQuote
	Quote  *FundingQuote
}

// Start opens the funding intent for groupID: the server re-plans and
// re-quotes (nothing the client says about amounts is used) and stores the
// route she signs. It does not keep the orders alive: a hold is earned only
// once her swap is seen (see Reconcile).
func (s *MarketplaceFundingService) Start(ctx context.Context, user *models.User, groupID uuid.UUID, src FundingSource) (*FundingStart, error) {
	if !s.ready() {
		return nil, ErrFundingUnavailable
	}
	plan, err := s.plan(ctx, user, groupID, src)
	if err != nil {
		return nil, err
	}
	intent, q, err := s.intents.Create(ctx, user, s.intentInput(user, groupID, src, plan))
	if err != nil {
		return nil, s.quoteError(plan, err)
	}
	if err := s.afterQuote(user, plan, q); err != nil {
		// Nothing was signed: close it so it does not hold her one slot.
		if aerr := s.intents.AbandonForReference(ctx, intent.ID, nil); aerr != nil && !errors.Is(aerr, ErrSellIntentState) {
			log.Printf("[marketplace-funding] could not close refused intent %s: %v", intent.ID, aerr)
		}
		return nil, err
	}
	return &FundingStart{Intent: intent, Swap: q, Quote: s.view(groupID, plan, q)}, nil
}

// quoteError turns the quoter's refusals into the checkout's words.
func (s *MarketplaceFundingService) quoteError(plan *fundingPlan, err error) error {
	switch {
	case errors.Is(err, ErrSellRouteTooCostly):
		if plan != nil && plan.budgetCapped {
			return ErrFundingTooSmall
		}
		return ErrFundingTooCostly
	case errors.Is(err, ErrSellWalletMCGP), errors.Is(err, ErrSellIntentOpen), errors.Is(err, ErrFundingNeedsGas):
		return err
	case errors.Is(err, ErrSellSourceUnsupported), errors.Is(err, ErrSellBadSourceAddress):
		return ErrFundingSource
	case errors.Is(err, ErrLiFiNoRoute):
		return fmt.Errorf("%w: no route for this token right now", ErrFundingUnavailable)
	}
	if errors.Is(err, ErrSellUnavailable) {
		return fmt.Errorf("%w: %v", ErrFundingUnavailable, err)
	}
	return err
}

// afterQuote is what can only be judged once the route is known: whether it
// needs a source-chain top-up we would only send a verified account, and, when
// she pays with S itself, whether the S the swap spends leaves enough to sign
// the escrow payments.
func (s *MarketplaceFundingService) afterQuote(user *models.User, p *fundingPlan, q *SellSwapQuote) error {
	if q == nil || q.Route == nil {
		return ErrFundingUnavailable
	}
	if q.GasTopUp && !fundingVerified(user) {
		return ErrFundingKYC
	}
	if p.nativeSource && p.sonicNeed != nil {
		spend, ok := new(big.Int).SetString(strings.TrimSpace(q.Route.FromAmount), 10)
		if !ok || spend.Sign() <= 0 {
			return ErrFundingUnavailable
		}
		left := new(big.Int).Sub(p.sonicHave, spend)
		if gas := RouteGasWei(q.Route, 0); gas != nil {
			left.Sub(left, gas)
		}
		if left.Cmp(p.sonicNeed) < 0 {
			return ErrFundingKeepS
		}
	}
	return nil
}

func fundingVerified(user *models.User) bool {
	return user != nil && user.VerificationStatus == models.VerificationStatusVerified
}

func (s *MarketplaceFundingService) quoteInput(user *models.User, src FundingSource, p *fundingPlan) SellQuoteInput {
	return SellQuoteInput{
		FromChainID: src.ChainID, FromToken: strings.TrimSpace(src.Token), FromAddress: strings.TrimSpace(src.Address),
		WalletAddress: user.WalletAddress, ToAmount: p.toAmount.String(),
		ToChainID: s.cfg.ChainID, ToToken: p.tokenAddr, ToAddress: user.WalletAddress,
		BudgetBps: p.budgetBps, Delivery: true, PassThrough: true, DestGasUSD: p.destGasUSD,
	}
}

func (s *MarketplaceFundingService) intentInput(user *models.User, groupID uuid.UUID, src FundingSource, p *fundingPlan) CreateSellIntentInput {
	ref := groupID
	destNeed, destMax := "", ""
	if p.destNeed != nil {
		destNeed, destMax = p.destNeed.String(), p.destShort.String()
	}
	return CreateSellIntentInput{
		SourceChainID:    src.ChainID,
		SourceToken:      strings.TrimSpace(src.Token),
		SourceAddress:    strings.TrimSpace(src.Address),
		ToAmount:         p.toAmount.String(),
		DestChainID:      s.cfg.ChainID,
		DestTokenAddress: p.tokenAddr,
		DestAddress:      user.WalletAddress,
		DestAmount:       p.short.String(),
		BudgetBps:        p.budgetBps,
		// A stablecoin: what it is worth is its face value, to the cent.
		ExpectedToUSD:   stableUSD(p.toAmount),
		Purpose:         models.SellIntentPurposeMarketplace,
		ReferenceID:     &ref,
		PassThrough:     true,
		DestGasUSD:      p.destGasUSD,
		DestGasNeedWei:  destNeed,
		DestTopUpMaxWei: destMax,
	}
}

// stableUSD is a 6 dp stablecoin amount at face value.
func stableUSD(v *big.Int) float64 {
	f, _ := usdcRat(v).Float64()
	return f
}

// plan sizes groupID's funding from the orders the server holds and the chain.
func (s *MarketplaceFundingService) plan(ctx context.Context, user *models.User, groupID uuid.UUID, src FundingSource) (*fundingPlan, error) {
	if user == nil {
		return nil, ErrFundingSource
	}
	var orders []models.Order
	if err := s.db.WithContext(ctx).
		Where("checkout_group_id = ? AND buyer_id = ?", groupID, user.ID).
		Order("created_at ASC, id ASC").Find(&orders).Error; err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFundingUnavailable, err)
	}
	if len(orders) == 0 {
		return nil, ErrFundingGroup
	}
	symbol := strings.ToUpper(strings.TrimSpace(orders[0].Token))
	if s.cfg.Tokens[symbol] == "" {
		return nil, ErrFundingGroup
	}
	for i := range orders {
		o := &orders[i]
		if o.Status != models.OrderStatusPendingPayment || o.PaymentSource != "" || !strings.EqualFold(o.Token, symbol) {
			return nil, ErrFundingGroup
		}
	}
	total, err := s.pulls.GroupPull(ctx, orders)
	if err != nil || total == nil || total.Sign() <= 0 {
		return nil, fmt.Errorf("%w: pricing the orders: %v", ErrFundingUnavailable, err)
	}
	return s.size(ctx, user, src, symbol, total, len(orders))
}

// size is the funding of `total` (base units of symbol, what the escrows will
// pull) over `orders` escrows, from src: what her wallet already holds pays
// first, the rest is swapped, padded for slippage, and the S her escrow txs
// will need is priced in.
func (s *MarketplaceFundingService) size(ctx context.Context, user *models.User, src FundingSource, symbol string, total *big.Int, orders int) (*fundingPlan, error) {
	if user == nil || strings.TrimSpace(user.WalletAddress) == "" {
		return nil, ErrFundingSource
	}
	// From her own wallet, on an EVM chain: the address the escrow will also
	// pull from. The caller has already made sure the wallet is her proven one.
	if addressFamily(src.ChainID) != "evm" || !sameAddress(src.Address, user.WalletAddress) {
		return nil, ErrFundingSource
	}
	if err := s.quoter.RefuseSource(src.ChainID, strings.TrimSpace(src.Token)); err != nil {
		return nil, err
	}
	tokenAddr := s.cfg.Tokens[symbol]
	if src.ChainID == s.cfg.ChainID && sameTokenAddress(src.Token, tokenAddr) {
		return nil, ErrFundingSameToken
	}
	p := &fundingPlan{orders: orders, symbol: symbol, tokenAddr: tokenAddr, total: new(big.Int).Set(total),
		nativeSource: src.ChainID == s.cfg.ChainID && isNativeTokenAddress(src.Token)}

	// What she already holds pays first; only the rest is swapped. An
	// unreadable balance cannot size the swap, so nothing is quoted.
	balance, err := s.chain.TokenBalance(ctx, tokenAddr, user.WalletAddress)
	if err != nil || balance == nil {
		return nil, fmt.Errorf("%w: reading your %s balance: %v", ErrFundingUnavailable, symbol, err)
	}
	p.balance = balance
	p.short = new(big.Int).Sub(p.total, balance)
	if p.short.Sign() <= 0 {
		return nil, ErrFundingNotNeeded
	}
	p.toAmount = padForSlippage(p.short)
	p.budgetBps, p.budgetCapped = fundingBudgetBps(s.cfg.BudgetBps, p.total, p.toAmount)
	if err := s.planDestGas(ctx, user, p); err != nil {
		return nil, err
	}
	return p, nil
}

// fundingBudgetBps is the budget — BudgetBps of the ORDER TOTAL — expressed as
// bps of the swap the route is priced on (the quoter's l and g are shares of
// what is swapped), floored, and capped at fundingMaxBudgetBps of the swap
// (capped reports that the cap bound it).
func fundingBudgetBps(orderBps int, total, swap *big.Int) (bps int, capped bool) {
	if swap == nil || swap.Sign() <= 0 {
		return 0, false
	}
	b := new(big.Int).Mul(total, big.NewInt(int64(orderBps)))
	b.Quo(b, swap)
	if !b.IsInt64() || b.Int64() > fundingMaxBudgetBps {
		return fundingMaxBudgetBps, true
	}
	return int(b.Int64()), false
}

// planDestGas decides whether her wallet will need S to sign each order's
// approve + createOrder, and — when we would top her up — refuses up front
// whatever that top-up would later be refused for: an unverified account, no
// Sonic lane, a shortfall over the lane's ceiling, a float under its floor or a
// spent daily budget, or her own daily cap. Sized at today's price with the
// top-up buffer; an unreadable coin balance is "none", the same side the
// source-chain check errs on. Paying with S itself is never topped up: the
// swap's own spend is checked against the route (afterQuote).
func (s *MarketplaceFundingService) planDestGas(ctx context.Context, user *models.User, p *fundingPlan) error {
	price, err := s.chain.GasPrice(ctx)
	if err != nil || price == nil || price.Sign() <= 0 {
		return fmt.Errorf("%w: reading the Sonic gas price: %v", ErrFundingUnavailable, err)
	}
	need := new(big.Int).SetUint64(s.cfg.GasPerOrder)
	need.Mul(need, big.NewInt(int64(p.orders)))
	need.Mul(need, price)
	need.Mul(need, big.NewInt(topUpGasBuffer))
	have, err := s.chain.NativeBalance(ctx, user.WalletAddress)
	if err != nil || have == nil {
		have = new(big.Int)
	}
	p.sonicNeed, p.sonicHave = need, have
	if p.nativeSource || have.Cmp(need) >= 0 {
		return nil
	}
	short := new(big.Int).Sub(need, have)
	if !fundingVerified(user) {
		return ErrFundingKYC
	}
	if s.dest == nil || !s.dest.CanSponsorChain(s.cfg.ChainID) {
		return ErrFundingNeedsGas
	}
	if err := s.dest.CheckTopUp(ctx, s.cfg.ChainID, short); err != nil {
		log.Printf("[marketplace-funding] sonic top-up refused at quote user=%s short=%s: %v", user.ID, short, err)
		return fmt.Errorf("%w (%v)", ErrFundingNeedsGas, err)
	}
	y, m, d := s.now().UTC().Date()
	today, err := s.intents.CountDestToppedUpSince(ctx, user.ID, time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrFundingUnavailable, err)
	}
	if today >= intentTopUpDailyCap {
		return fmt.Errorf("%w (%v)", ErrFundingNeedsGas, ErrTopUpDailyCap)
	}
	coin, dec, err := s.quoter.TokenPriceUSD(ctx, s.cfg.ChainID, evmNativeAddress)
	if err != nil || coin == nil || coin.Sign() <= 0 {
		return fmt.Errorf("%w: pricing Sonic gas: %v", ErrFundingUnavailable, err)
	}
	if dec <= 0 {
		dec = 18
	}
	usd, _ := new(big.Rat).Mul(new(big.Rat).SetFrac(short, pow10(dec)), coin).Float64()
	p.destNeed, p.destShort, p.destGasUSD = need, short, usd
	return nil
}

// view is the breakdown she is shown, from the route she would sign.
func (s *MarketplaceFundingService) view(groupID uuid.UUID, p *fundingPlan, q *SellSwapQuote) *FundingQuote {
	r := q.Route
	group := ""
	if groupID != uuid.Nil {
		group = groupID.String()
	}
	return &FundingQuote{
		GroupID: group, Orders: p.orders, Token: p.symbol, TokenAddress: p.tokenAddr, ChainID: s.cfg.ChainID,
		OrderTotal: p.total.String(), WalletBalance: p.balance.String(), Shortfall: p.short.String(), ToAmount: p.toAmount.String(),
		FromAmount: r.FromAmount, FromAmountUSD: r.FromAmountUSD,
		LiFiCostUSD: r.FromAmountUSD * float64(q.CostBps) / 10_000, GasUSD: r.FromAmountUSD * float64(q.GasBps) / 10_000,
		CostBps: q.CostBps, GasBps: q.GasBps, BudgetBps: p.budgetBps,
		ETASeconds: r.ETASeconds, Tool: r.Tool,
		SourceGasTopUp: q.GasTopUp, DestGasTopUp: p.destNeed != nil,
	}
}

// ─── The steps after Start ─────────────────────────────────────────────────

// funding is her marketplace intent by id, or ErrSellIntentNotFound (also for
// a sale or a TSA Pay intent: those are driven by their own routes).
func (s *MarketplaceFundingService) funding(ctx context.Context, userID, id uuid.UUID) (*models.RampSellIntent, error) {
	if !s.ready() {
		return nil, ErrFundingUnavailable
	}
	in, err := s.intents.Get(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if !in.IsMarketplace() {
		return nil, ErrSellIntentNotFound
	}
	return in, nil
}

// Get is one of her funding intents.
func (s *MarketplaceFundingService) Get(ctx context.Context, userID, id uuid.UUID) (*models.RampSellIntent, error) {
	return s.funding(ctx, userID, id)
}

// Open is her funding intent still in progress, or nil.
func (s *MarketplaceFundingService) Open(ctx context.Context, userID uuid.UUID) (*models.RampSellIntent, error) {
	if !s.ready() {
		return nil, ErrFundingUnavailable
	}
	return s.intents.OpenFor(ctx, userID, models.SellIntentPurposeMarketplace)
}

// RecordSwap attaches the swap she broadcast (idempotent for the same hash). It
// earns her orders no time on its own: a hash is only a claim until LiFi sees it.
func (s *MarketplaceFundingService) RecordSwap(ctx context.Context, user *models.User, id uuid.UUID, txHash string) error {
	if _, err := s.funding(ctx, user.ID, id); err != nil {
		return err
	}
	return s.intents.RecordSwap(ctx, user.ID, id, txHash)
}

// FundingStatus is where a funding stands after a confirm.
type FundingStatus struct {
	Intent     *models.RampSellIntent
	DestTxHash string
	// NeedsGas: the funds landed, the quote budgeted S, and her wallet still
	// holds less than the escrow txs need — ask for the Sonic top-up.
	NeedsGas bool
}

// Confirm proves the delivery (ConfirmDelivery: LiFi binds the swap, Sonic's
// own receipt proves the credit) and moves the intent to delivered. A swap
// LiFi reports ended short closes the intent and answers SwapEndedShortError;
// one still on its way answers SwapNotLandedError (and, while LiFi follows it,
// keeps her orders alive within the hold).
func (s *MarketplaceFundingService) Confirm(ctx context.Context, user *models.User, id uuid.UUID) (*FundingStatus, error) {
	in, err := s.funding(ctx, user.ID, id)
	if err != nil {
		return nil, err
	}
	if in.Status != models.SellIntentSwapped && in.Status != models.SellIntentDelivered {
		return nil, ErrSellIntentState
	}
	dest, err := s.intents.ConfirmDelivery(ctx, id)
	if err != nil {
		if ended := s.endIfShort(ctx, in, err); ended != nil {
			return nil, ended
		}
		s.followInFlight(ctx, in, err, s.now())
		return nil, err
	}
	s.delivered(ctx, in)
	now, err := s.intents.Get(ctx, user.ID, id)
	if err != nil {
		return nil, err
	}
	return &FundingStatus{Intent: now, DestTxHash: dest, NeedsGas: s.needsDestGas(ctx, now)}, nil
}

// delivered is what follows a proven delivery: her orders' clock restarts (the
// funds are in her wallet and she is paying now) and, the first time, the gas
// recovered inside the route is logged against the gas we paid for her.
func (s *MarketplaceFundingService) delivered(ctx context.Context, in *models.RampSellIntent) {
	s.touchIntentGroup(ctx, in)
	if in.Status == models.SellIntentDelivered {
		return
	}
	recovered := in.QuotedFromUSD * float64(in.GasRecoveryBps) / 10_000
	log.Printf("[marketplace-funding] METRIC marketplace_gas_recovery intent=%s recovered_bps=%d recovered_usd=%.4f source_topup_wei=%s dest_topup_budgeted=%v",
		in.ID, in.GasRecoveryBps, recovered, orZero(in.TopUpWei), in.DestTopUpBudgeted)
}

// followInFlight keeps a swap's checkout alive while its swap is genuinely in
// flight: LiFi knows the hash as a transfer from her address on the source
// chain (the first sighting is recorded), or it was reported under
// fundingUnseenGrace ago. Never past the hold (holdOpen). err is
// ConfirmDelivery's "not landed" answer, which carries LiFi's status.
func (s *MarketplaceFundingService) followInFlight(ctx context.Context, in *models.RampSellIntent, err error, now time.Time) {
	var wait *SwapNotLandedError
	if !errors.As(err, &wait) {
		return
	}
	seen := in.SwapSeenAt != nil
	if !seen && swapSeenFrom(in, wait.Status) {
		seen = true
		if uerr := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).
			Where("id = ? AND swap_seen_at IS NULL", in.ID).Update("swap_seen_at", now).Error; uerr != nil {
			log.Printf("[marketplace-funding] could not record swap sighting on %s: %v", in.ID, uerr)
		}
		in.SwapSeenAt = &now
	}
	if !holdOpen(in, now) {
		return
	}
	if seen || (in.SwapRecordedAt != nil && now.Sub(*in.SwapRecordedAt) < fundingUnseenGrace) {
		s.touchIntentGroup(ctx, in)
	}
}

// swapSeenFrom: LiFi's status is a live or finished transfer sent by her, from
// the intent's source chain — the evidence a hash was really broadcast.
func swapSeenFrom(in *models.RampSellIntent, st *LiFiTransferStatus) bool {
	if st == nil || (st.Status != LiFiStatusPending && st.Status != LiFiStatusDone) {
		return false
	}
	if st.SendingChainID != 0 && st.SendingChainID != in.SourceChainID {
		return false
	}
	return sameAddress(st.FromAddress, in.SourceAddress)
}

// holdOpen: touching now keeps the orders alive until now + UnpaidOrderTTL,
// which must not pass the intent's creation + fundingHoldMax.
func holdOpen(in *models.RampSellIntent, now time.Time) bool {
	return now.Before(in.CreatedAt.Add(fundingHoldMax - UnpaidOrderTTL))
}

// endIfShort closes an intent whose swap LiFi reports FAILED, REFUNDED or
// PARTIAL, and answers how it ended; nil when err is not that.
func (s *MarketplaceFundingService) endIfShort(ctx context.Context, in *models.RampSellIntent, err error) error {
	var wait *SwapNotLandedError
	if !errors.As(err, &wait) || wait.Status == nil || !swapEndedShort(wait.Status) {
		return nil
	}
	if aerr := s.intents.AbandonForReference(ctx, in.ID, in.SwapTxHash); aerr != nil && !errors.Is(aerr, ErrSellIntentState) {
		log.Printf("[marketplace-funding] could not close ended intent %s: %v", in.ID, aerr)
	}
	return &SwapEndedShortError{Status: wait.Status.Status, Substatus: wait.Status.Substatus}
}

// swapEndedShort: LiFi's final word that the swap did not deliver as quoted.
func swapEndedShort(st *LiFiTransferStatus) bool {
	return st.Status == LiFiStatusFailed ||
		(st.Status == LiFiStatusDone && (st.Substatus == LiFiSubstatusRefunded || st.Substatus == LiFiSubstatusPartial))
}

// needsDestGas reports a delivered intent whose budgeted S top-up her wallet
// still lacks. An unreadable balance reads as lacking: the top-up itself
// skips when she already holds enough.
func (s *MarketplaceFundingService) needsDestGas(ctx context.Context, in *models.RampSellIntent) bool {
	if !in.DestTopUpBudgeted || in.Status != models.SellIntentDelivered || in.DestTopUpTxHash != "" {
		return false
	}
	need, ok := new(big.Int).SetString(strings.TrimSpace(in.DestGasNeedWei), 10)
	if !ok || need.Sign() <= 0 {
		return false
	}
	have, err := s.chain.NativeBalance(ctx, in.DestAddress)
	return err != nil || have == nil || have.Cmp(need) < 0
}

// Abandon ends her funding before its swap was sent. After that only the
// reconciler ends it (the swap may still land, and she must not be told to pay
// again meanwhile).
func (s *MarketplaceFundingService) Abandon(ctx context.Context, user *models.User, id uuid.UUID) error {
	if _, err := s.funding(ctx, user.ID, id); err != nil {
		return err
	}
	return s.intents.Abandon(ctx, user.ID, id)
}

// SourceGasTopUp sends the source chain's coin for her swap, exactly as a
// sale's top-up (TopUpForIntent: only when the quote budgeted it).
func (s *MarketplaceFundingService) SourceGasTopUp(ctx context.Context, user *models.User, id uuid.UUID) (GasTopUpResult, error) {
	in, err := s.funding(ctx, user.ID, id)
	if err != nil {
		return GasTopUpResult{}, err
	}
	if s.source == nil {
		return GasTopUpResult{}, ErrSponsorNotAvailable
	}
	return s.source.TopUpForIntent(ctx, user, in, s.intents)
}

// SonicGasTopUp sends S for her escrow txs once the funds have landed.
func (s *MarketplaceFundingService) SonicGasTopUp(ctx context.Context, user *models.User, id uuid.UUID) (GasTopUpResult, error) {
	in, err := s.funding(ctx, user.ID, id)
	if err != nil {
		return GasTopUpResult{}, err
	}
	if s.dest == nil {
		return GasTopUpResult{}, ErrDestGasUnavailable
	}
	res, err := s.dest.TopUpDestForIntent(ctx, user, in, s.intents)
	if err == nil {
		s.touchIntentGroup(ctx, in)
		if res.TxHash != "" {
			log.Printf("[marketplace-funding] METRIC marketplace_sonic_gas_sent intent=%s wei=%s recovered_bps=%d", in.ID, res.AmountWei, in.GasRecoveryBps)
		}
	}
	return res, err
}

// ─── Keeping the orders alive, and the reconciler ──────────────────────────

// touchIntentGroup restarts the unpaid-order clock of in's checkout.
func (s *MarketplaceFundingService) touchIntentGroup(ctx context.Context, in *models.RampSellIntent) {
	if in.ReferenceID == nil {
		return
	}
	if err := TouchUnpaidGroup(s.db.WithContext(ctx), *in.ReferenceID, in.UserID); err != nil {
		log.Printf("[marketplace-funding] could not keep group %s alive: %v", *in.ReferenceID, err)
	}
}

// fundingBackoff is how long the reconciler waits before looking at an intent
// again, by its age: LiFi is asked every tick while a swap is young, and less
// and less often as it ages toward being written off.
func fundingBackoff(age time.Duration) time.Duration {
	switch {
	case age < 30*time.Minute:
		return 0
	case age < fundingHoldMax:
		return 3 * time.Minute
	case age < fundingGiveUp/2:
		return 15 * time.Minute
	default:
		return time.Hour
	}
}

// Reconcile follows every funding intent still open, the way the pay
// reconciler follows a payment's: an unsigned one is closed after
// fundingUnsignedTTL; a sent one is proven delivered (its orders kept alive
// only while LiFi follows it, within the hold), closed when LiFi says it ended
// short or never saw it, and written off after fundingGiveUp. Intents are
// taken oldest-due first (next_check_at) and each visit pushes its next check
// out by fundingBackoff, so a long tail of stuck swaps cannot starve new ones.
// It returns the last error it met so a persistent failure trips the alert.
func (s *MarketplaceFundingService) Reconcile(ctx context.Context) error {
	if !s.ready() {
		return nil
	}
	now := s.now()
	var lastErr error
	for page := 0; page < fundingMaxPages; page++ {
		var due []models.RampSellIntent
		if err := s.db.WithContext(ctx).
			Where("purpose = ? AND status IN ?", models.SellIntentPurposeMarketplace,
				[]string{models.SellIntentSwapping, models.SellIntentSwapped}).
			Where("next_check_at IS NULL OR next_check_at <= ?", now).
			Order("COALESCE(next_check_at, created_at) ASC, id ASC").
			Limit(fundingBatch).Find(&due).Error; err != nil {
			return err
		}
		for i := range due {
			if err := ctx.Err(); err != nil {
				return err
			}
			in := &due[i]
			if err := s.reconcileOne(ctx, in, now); err != nil {
				lastErr = err
			}
			// Pushed past now even when nothing changed, so the next page and
			// the next tick move on to others.
			next := now.Add(max(fundingBackoff(now.Sub(in.CreatedAt)), time.Nanosecond))
			if err := s.db.WithContext(ctx).Model(&models.RampSellIntent{}).Where("id = ?", in.ID).
				UpdateColumn("next_check_at", next).Error; err != nil {
				lastErr = err
			}
		}
		if len(due) < fundingBatch {
			break
		}
	}
	return lastErr
}

func (s *MarketplaceFundingService) reconcileOne(ctx context.Context, in *models.RampSellIntent, now time.Time) error {
	age := now.Sub(in.CreatedAt)
	if in.SwapTxHash == nil {
		if age < fundingUnsignedTTL {
			return nil
		}
		// Never signed (or signed and never reported: whatever it does, it
		// pays her own wallet). A swap reported meanwhile is left alone.
		if err := s.intents.AbandonForReference(ctx, in.ID, nil); err != nil && !errors.Is(err, ErrSellIntentState) {
			return fmt.Errorf("close unsigned funding %s: %w", in.ID, err)
		}
		log.Printf("[marketplace-funding] closed unsigned funding intent %s after %s", in.ID, age.Round(time.Minute))
		return nil
	}
	_, err := s.intents.ConfirmDelivery(ctx, in.ID)
	switch {
	case err == nil:
		s.delivered(ctx, in)
		log.Printf("[marketplace-funding] funding intent %s delivered", in.ID)
		return nil
	case s.endIfShort(ctx, in, err) != nil:
		log.Printf("[marketplace-funding] funding intent %s ended short: %v", in.ID, err)
		return nil
	case errors.Is(err, ErrSwapNotLanded), errors.Is(err, ErrSwapMismatch):
		s.followInFlight(ctx, in, err, now)
		neverSeen := in.SwapSeenAt == nil && in.SwapRecordedAt != nil && now.Sub(*in.SwapRecordedAt) > fundingUnseenGiveUp
		if age < fundingGiveUp && !neverSeen {
			return nil
		}
		log.Printf("[marketplace-funding] ALERT marketplace_funding_given_up_total=1 intent=%s age=%s never_seen=%v: %v",
			in.ID, age.Round(time.Minute), neverSeen, err)
		if aerr := s.intents.AbandonForReference(ctx, in.ID, in.SwapTxHash); aerr != nil && !errors.Is(aerr, ErrSellIntentState) {
			return fmt.Errorf("write off funding %s: %w", in.ID, aerr)
		}
		return nil
	default:
		return fmt.Errorf("confirm funding %s: %w", in.ID, err)
	}
}
