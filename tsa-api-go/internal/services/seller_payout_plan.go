package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// SellerPayoutPlanner decides at checkout whether a marketplace order pays its
// seller fiat into their bank, and what that costs the buyer. When it does,
// the TREASURY is the order's on-chain seller (the escrow releases to it), the
// buyer's total carries the offramp costs (the gross-up), and the seller's
// destination and the naira they will receive are snapshotted on a
// seller_payouts row. Any doubt — no payout method, no treasury lane, no
// venue for the amount — returns no plan, and the order is made the ordinary
// way, paying the seller's wallet.
type SellerPayoutPlanner struct {
	methods  *SellerPayoutService
	rater    payoutVenueQuoter
	treasury *Treasury
	// senderFee is Paycrest's sender fee percent for a payout ("0.5"): the
	// same figure TSA Pay uses.
	senderFee func() string
	// bridgeBps is what the Sonic -> Base bridge is expected to cost. The
	// buyer pays for it in the gross-up; a route that costs more is absorbed
	// (up to sellerPayoutAbsorbBps) or parks, a cheaper one leaves the rest
	// with the platform.
	bridgeBps int64
	// The company route (WithCompany): the central account pays the naira and the
	// USDC stays in the treasury for an operator to rebalance.
	company        companyBalances
	companyDB      *gorm.DB
	companyTakeBps int64
	companyFeeKobo int64
	companyMaxKobo int64
	// promised holds the float this process has just promised to payouts not yet
	// persisted (a cart plans each line before any is saved, and checkouts run
	// concurrently), so two plans never count the same naira as free.
	promised   []floatPromise
	promisedMu sync.Mutex
	// bridge prices the real Sonic -> Base route at checkout; nil prices the
	// bridge at bridgeBps and trusts it.
	bridge lifiQuoter
	// sw is the operator's switch; nil is always on.
	sw payoutSwitch
}

type floatPromise struct {
	kobo  int64
	until time.Time
}

// floatPromiseTTL is how long an unpersisted promise holds float: long enough for
// the order to be saved, short enough that an abandoned checkout frees it.
const floatPromiseTTL = 2 * time.Minute

// promisedKobo is the naira promised to plans not yet saved; expired promises
// are dropped.
func (p *SellerPayoutPlanner) promisedKobo(now time.Time) int64 {
	p.promisedMu.Lock()
	defer p.promisedMu.Unlock()
	var total int64
	live := p.promised[:0]
	for _, f := range p.promised {
		if f.until.After(now) {
			live = append(live, f)
			total += f.kobo
		}
	}
	p.promised = live
	return total
}

func (p *SellerPayoutPlanner) promise(kobo int64, now time.Time) {
	p.promisedMu.Lock()
	p.promised = append(p.promised, floatPromise{kobo: kobo, until: now.Add(floatPromiseTTL)})
	p.promisedMu.Unlock()
}

// companyBalances is the slice of the central account the planner reads.
type companyBalances interface {
	FetchBalances(ctx context.Context) ([]FWBalance, error)
}

// companyMinPayoutKobo is the smallest transfer the company route sends (₦100:
// below it the provider's floor refuses).
const companyMinPayoutKobo = 10_000

// WithCompany turns on the company route, which is preferred over Paycrest
// whenever it can run: it is offered when the central account holds enough naira
// for this payout and every one already promised, and the payout is not above
// maxKobo (0 turns the route off). takeBps is the company's margin and feeKobo
// the provider's transfer fee; the buyer's gross-up carries both.
func (p *SellerPayoutPlanner) WithCompany(c companyBalances, db *gorm.DB, takeBps, feeKobo, maxKobo int64) *SellerPayoutPlanner {
	p.company, p.companyDB, p.companyTakeBps, p.companyFeeKobo, p.companyMaxKobo = c, db, takeBps, feeKobo, maxKobo
	return p
}

// WithBridge makes the planner quote the actual bridge at the order's size: a
// payout is offered only when that route fits the machine's cap, and the buyer
// is charged what it really costs. Without it the flat bridgeBps is trusted.
func (p *SellerPayoutPlanner) WithBridge(q lifiQuoter) *SellerPayoutPlanner {
	p.bridge = q
	return p
}

// WithSwitch stops new payout orders while the operator has payouts off.
func (p *SellerPayoutPlanner) WithSwitch(sw payoutSwitch) *SellerPayoutPlanner {
	p.sw = sw
	return p
}

// payoutVenueQuoter is the slice of PaycrestQuoteService the planner needs.
type payoutVenueQuoter interface {
	VenueForFee(ctx context.Context, side, fiatCode, fiatAmount, chainKey, token string, feePercent float64) (*PaycrestVenue, error)
}

func NewSellerPayoutPlanner(methods *SellerPayoutService, rater payoutVenueQuoter, treasury *Treasury, senderFee func() string, bridgeBps int64) *SellerPayoutPlanner {
	return &SellerPayoutPlanner{methods: methods, rater: rater, treasury: treasury, senderFee: senderFee, bridgeBps: bridgeBps}
}

// PayoutPlan is one order line's fiat payout.
type PayoutPlan struct {
	Method *models.SellerPayoutMethod
	// GrossUp is added to the order's escrowed amount (its shippingAmount: the
	// escrow pays that to the seller in full and takes no fee on it).
	GrossUp *big.Int
	// FiatTargetKobo is the naira the seller is quoted: what Net buys at the
	// venue's rate, before the sender fee (which the deposit carries).
	FiatTargetKobo int64
	// Venue is who delivers the naira; empty means Paycrest.
	Venue string
}

// TreasuryAddress is the on-chain seller of every payout order.
func (p *SellerPayoutPlanner) TreasuryAddress() string {
	if p == nil || p.treasury == nil {
		return ""
	}
	return p.treasury.Address()
}

// sonicReady is true when the treasury can hold a payout's release: its signer
// is consistent, no kill switch, and the escrow is a call target on Sonic (the
// treasury signs the seller-only calls). Checked per order, so flipping the
// switch stops new payout orders.
func (p *SellerPayoutPlanner) sonicReady() bool {
	if p == nil || p.treasury == nil || !p.treasury.Enabled() || p.treasury.Killed() || p.treasury.Address() == "" {
		return false
	}
	sonic := p.treasury.Lane("sonic")
	return sonic != nil && len(sonic.CallTargets) > 0
}

// ready is sonicReady plus the Base lane the Paycrest route bridges to.
func (p *SellerPayoutPlanner) ready() bool {
	return p.sonicReady() && p.treasury.Lane("base") != nil
}

// Plan prices a payout for seller's net amount (the base units the seller
// would otherwise receive: productAmount + shippingAmount) in token. A nil
// plan with a nil error means "pay the wallet".
func (p *SellerPayoutPlanner) Plan(ctx context.Context, sellerID uuid.UUID, token string, net *big.Int) (*PayoutPlan, error) {
	token = strings.ToUpper(token)
	if p != nil && p.sw != nil {
		// Off, or unreadable: the order is made the ordinary way.
		if on, err := p.sw.Enabled(ctx); err != nil || !on {
			return nil, nil
		}
	}
	if !p.sonicReady() || (token != "USDC" && token != "USDT") || net == nil || net.Sign() <= 0 {
		return nil, nil
	}
	resolved, err := p.methods.Resolve(ctx, sellerID)
	if err != nil {
		return nil, err
	}
	m := resolved.Method
	if m == nil || m.Kind != models.SellerPayoutBank || !m.Verified || m.Currency != "NGN" {
		return nil, nil
	}
	// A payout the treasury's per-order cap would refuse after the buyer paid is
	// not offered: the order is made the ordinary way instead.
	if err := p.treasury.CheckCaps("sonic", unitsToUSD(net, 6), 0); err != nil {
		return nil, nil
	}
	// The company route is preferred: no bridge, no sale, no fixed fee to size.
	if plan := p.planCompany(ctx, m, net); plan != nil {
		return plan, nil
	}
	if !p.ready() {
		return nil, nil
	}
	fee, ok := feeFraction(p.senderFee())
	if !ok || fee.Cmp(big.NewRat(1, 1)) >= 0 {
		return nil, nil
	}
	rate, err := p.rate(ctx, net)
	if err != nil {
		// No venue for this size (or Paycrest is down): the wallet is paid, and
		// the buyer is never charged for a payout that cannot run.
		return nil, nil
	}
	netUSD := new(big.Rat).SetFrac(net, pow10(6)) // stablecoins, 6 dp on Sonic
	kobo := new(big.Rat).Mul(netUSD, rate)
	kobo.Mul(kobo, big.NewRat(100, 1))
	target := new(big.Int).Quo(kobo.Num(), kobo.Denom()) // floor
	if !target.IsInt64() || target.Sign() <= 0 {
		return nil, nil
	}
	bridgeBps, ok := p.bridgeCostBps(ctx, token, net, fee)
	if !ok {
		// The bridge does not fit this size (its fixed cost dwarfs a small
		// order) or cannot be priced: pay the wallet, charge the buyer nothing.
		return nil, nil
	}
	return &PayoutPlan{Method: m, GrossUp: payoutGrossUp(net, fee, bridgeBps), FiatTargetKobo: target.Int64()}, nil
}

// bridgeCostBps is what the buyer's gross-up budgets for the bridge, and false
// when no payout should be offered. With a quoter it asks LiFi for the route at
// the amount the treasury will actually hold (net plus its own gross-up at the
// configured estimate), refuses one that leaves no slack under the machine's
// cap, and prices the rest at what it cost plus the slack.
func (p *SellerPayoutPlanner) bridgeCostBps(ctx context.Context, token string, net *big.Int, fee *big.Rat) (int64, bool) {
	if p.bridge == nil {
		return p.bridgeBps, true
	}
	sonic, base := p.treasury.Lane("sonic"), p.treasury.Lane("base")
	if sonic == nil || base == nil {
		return 0, false
	}
	held := new(big.Int).Add(net, payoutGrossUp(net, fee, p.bridgeBps))
	_, cost, err := quoteSellerPayoutBridge(ctx, p.bridge, sonic, base, token, p.treasury.Address(), held)
	if err != nil || cost < 0 || cost > sellerPayoutMaxBridgeBps-sellerPayoutBridgeSlackBps {
		return 0, false
	}
	bps := int64(cost) + sellerPayoutBridgeSlackBps
	if bps < p.bridgeBps {
		bps = p.bridgeBps
	}
	return bps, true
}

// rate is the provider's raw sell rate (naira per token) for about net: asked
// once at a reference size to learn the scale, then at the actual size, since
// the book's rate depends on it.
func (p *SellerPayoutPlanner) rate(ctx context.Context, net *big.Int) (*big.Rat, error) {
	pct, ok := parseSenderFeePercent(p.senderFee())
	if !ok {
		return nil, errors.New("unusable sender fee")
	}
	r0, err := p.referenceRate(ctx)
	if err != nil {
		return nil, err
	}
	guess := new(big.Rat).Mul(new(big.Rat).SetFrac(net, pow10(6)), r0)
	amount := new(big.Int).Quo(guess.Num(), guess.Denom())
	venue, err := p.rater.VenueForFee(ctx, "sell", "NGN", amount.String(), "base", "USDC", pct)
	if err != nil {
		return nil, err
	}
	r, ok := new(big.Rat).SetString(strings.TrimSpace(venue.RawRate))
	if !ok || r.Sign() <= 0 {
		return nil, fmt.Errorf("unusable rate %q", venue.RawRate)
	}
	return r, nil
}

// referenceRate is the provider's raw sell rate (naira per token) at a reference
// size: the scale the sized quote starts from, and the price the company route
// quotes at (it has no book whose rate depends on size).
func (p *SellerPayoutPlanner) referenceRate(ctx context.Context) (*big.Rat, error) {
	pct, ok := parseSenderFeePercent(p.senderFee())
	if !ok {
		return nil, errors.New("unusable sender fee")
	}
	ref, err := p.rater.VenueForFee(ctx, "sell", "NGN", "100000", "base", "USDC", pct)
	if err != nil {
		return nil, err
	}
	r0, ok := new(big.Rat).SetString(strings.TrimSpace(ref.RawRate))
	if !ok || r0.Sign() <= 0 {
		return nil, fmt.Errorf("unusable reference rate %q", ref.RawRate)
	}
	return r0, nil
}

// planCompany prices a payout the central account pays, or returns nil when the
// route cannot run (not configured, no rate, too small or too large, or not
// enough naira in the account for this payout plus every one already promised).
// The seller is quoted net at the reference rate, exactly as on the Paycrest
// route; the buyer's gross-up carries the transfer fee and the company's take.
func (p *SellerPayoutPlanner) planCompany(ctx context.Context, m *models.SellerPayoutMethod, net *big.Int) *PayoutPlan {
	if p.company == nil || p.companyDB == nil || p.companyMaxKobo <= 0 {
		return nil
	}
	rate, err := p.referenceRate(ctx)
	if err != nil {
		return nil
	}
	koboRat := new(big.Rat).Mul(new(big.Rat).SetFrac(net, pow10(6)), rate)
	koboRat.Mul(koboRat, big.NewRat(100, 1))
	kobo := new(big.Int).Quo(koboRat.Num(), koboRat.Denom()) // floor
	if !kobo.IsInt64() || kobo.Int64() < companyMinPayoutKobo || kobo.Int64() > p.companyMaxKobo {
		return nil
	}
	target := kobo.Int64()

	// The float must cover this payout and every company payout already promised.
	var open struct {
		Kobo int64
		N    int64
	}
	if err := p.companyDB.WithContext(ctx).Table("seller_payouts").
		Select("COALESCE(SUM(fiat_target_kobo), 0) AS kobo, COUNT(*) AS n").
		// A parked company payout is still owed from the float: one stopped before
		// or at the transfer keeps its claim on it.
		Where("venue = ? AND (status IN ? OR (status = ? AND parked_from IN ?))", models.SellerPayoutVenueCompany,
			[]string{models.SellerPayoutAwaitingRelease, models.SellerPayoutReceived, models.SellerPayoutPayingCompany},
			models.SellerPayoutNeedsReview, []string{models.SellerPayoutReceived, models.SellerPayoutPayingCompany}).
		Scan(&open).Error; err != nil {
		return nil
	}
	bals, err := p.company.FetchBalances(ctx)
	if err != nil {
		return nil
	}
	avail, ok := ngnAvailable(bals)
	now := time.Now()
	if !ok || avail*100 < float64(open.Kobo+p.promisedKobo(now)+target+(open.N+1)*p.companyFeeKobo) {
		return nil
	}
	p.promise(target+p.companyFeeKobo, now)

	// What the treasury must receive R so that R(1-take) = net + the fee in USDC.
	feeUSD := new(big.Rat).Quo(big.NewRat(p.companyFeeKobo, 100), rate)
	need := new(big.Rat).Add(new(big.Rat).SetInt(net), new(big.Rat).Mul(feeUSD, new(big.Rat).SetInt(pow10(6))))
	keep := new(big.Rat).Sub(big.NewRat(1, 1), big.NewRat(p.companyTakeBps, 10000))
	if keep.Sign() <= 0 {
		return nil
	}
	gross := new(big.Rat).Quo(need, keep)
	whole, rem := new(big.Int).QuoRem(gross.Num(), gross.Denom(), new(big.Int))
	if rem.Sign() > 0 {
		whole.Add(whole, big.NewInt(1))
	}
	return &PayoutPlan{Method: m, GrossUp: whole.Sub(whole, net), FiatTargetKobo: target, Venue: models.SellerPayoutVenueCompany}
}

// payoutGrossUp is what the buyer pays on top so the seller still nets `net`
// in naira: the treasury must deposit net/(1-fee) after bridging, and bridging
// keeps (1-bridge), so it must receive net/((1-fee)(1-bridge)). Rounded up,
// against the buyer by at most a base unit.
func payoutGrossUp(net *big.Int, fee *big.Rat, bridgeBps int64) *big.Int {
	keep := new(big.Rat).Mul(
		new(big.Rat).Sub(big.NewRat(1, 1), fee),
		new(big.Rat).Sub(big.NewRat(1, 1), big.NewRat(bridgeBps, 10000)),
	)
	gross := new(big.Rat).Quo(new(big.Rat).SetInt(net), keep)
	whole, rem := new(big.Int).QuoRem(gross.Num(), gross.Denom(), new(big.Int))
	if rem.Sign() > 0 {
		whole.Add(whole, big.NewInt(1))
	}
	return whole.Sub(whole, net)
}

// Persist records the payout for order inside tx, in the same transaction as
// the order itself. expected is what the escrow will credit the treasury:
// productAmount + shippingAmount including the gross-up.
func (p *SellerPayoutPlanner) Persist(tx *gorm.DB, order *models.Order, sellerWallet string, plan *PayoutPlan, expected *big.Int) error {
	m := plan.Method
	return tx.Create(&models.SellerPayoutOrder{
		ID: uuid.New(), MarketplaceOrderID: order.ID, SellerUserID: order.SellerID,
		Currency: m.Currency, InstitutionCode: m.InstitutionCode, InstitutionName: m.InstitutionName,
		AccountNumber: m.AccountNumber, AccountName: m.AccountName, SellerWallet: sellerWallet,
		ChainKey: "sonic", Token: strings.ToUpper(order.Token),
		ExpectedAmount: expected.String(), FiatTargetKobo: plan.FiatTargetKobo,
		Status: models.SellerPayoutAwaitingRelease, Venue: planVenue(plan),
	}).Error
}

// PayoutForOrder is the payout carried by a marketplace order, or nil.
func PayoutForOrder(ctx context.Context, db *gorm.DB, orderID uuid.UUID) (*models.SellerPayoutOrder, error) {
	var row models.SellerPayoutOrder
	err := db.WithContext(ctx).Where("marketplace_order_id = ?", orderID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func planVenue(plan *PayoutPlan) string {
	if plan.Venue == "" {
		return models.SellerPayoutVenuePaycrest
	}
	return plan.Venue
}
