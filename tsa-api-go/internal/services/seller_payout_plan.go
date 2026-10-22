package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

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
	// sw is the operator's switch; nil is always on.
	sw payoutSwitch
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
}

// TreasuryAddress is the on-chain seller of every payout order.
func (p *SellerPayoutPlanner) TreasuryAddress() string {
	if p == nil || p.treasury == nil {
		return ""
	}
	return p.treasury.Address()
}

// ready is true when the treasury can run a payout end to end: lanes for the
// escrow's chain (with the escrow as a call target) and for Base, and no kill
// switch. Checked per order, so flipping the switch stops new payout orders.
func (p *SellerPayoutPlanner) ready() bool {
	if p == nil || p.treasury == nil || !p.treasury.Enabled() || p.treasury.Killed() || p.treasury.Address() == "" {
		return false
	}
	sonic, base := p.treasury.Lane("sonic"), p.treasury.Lane("base")
	return sonic != nil && base != nil && len(sonic.CallTargets) > 0
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
	if !p.ready() || (token != "USDC" && token != "USDT") || net == nil || net.Sign() <= 0 {
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
	return &PayoutPlan{Method: m, GrossUp: payoutGrossUp(net, fee, p.bridgeBps), FiatTargetKobo: target.Int64()}, nil
}

// rate is the provider's raw sell rate (naira per token) for about net: asked
// once at a reference size to learn the scale, then at the actual size, since
// the book's rate depends on it.
func (p *SellerPayoutPlanner) rate(ctx context.Context, net *big.Int) (*big.Rat, error) {
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
		Status: models.SellerPayoutAwaitingRelease,
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
