package services

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

var (
	// ErrPayoutRerouteUnsafe: something was already sent for this payout (a bridge,
	// an approval, a Paycrest order, a company transfer that may have left), so
	// changing who delivers the naira could pay the seller twice.
	ErrPayoutRerouteUnsafe = errors.New("something was already sent for this payout: it cannot be re-routed")
	// ErrPayoutRouteUnavailable: the venue asked for cannot run on this server.
	ErrPayoutRouteUnavailable = errors.New("that route is not available on this server")
	// ErrPayoutBadRoute: not a venue.
	ErrPayoutBadRoute = errors.New("route must be company or paycrest")
)

// Reroute changes who delivers the naira for a parked payout that has not sent
// anything yet: the company route (the central account pays) or Paycrest (the
// bridge and a sale). It is the operator's way out of a payout one venue cannot
// run (a small order Paycrest and the bridge refuse, a central account short of
// float) without SQL.
//
// It refuses unless the payout provably moved nothing: no Paycrest order, no
// leg with a transaction, an approval or a confirmation, and no company transfer
// that may have left (a refused first attempt is fine: it proves none exists).
// The payout goes back to where its proven state is — awaiting_release if the
// release was never proven, received if it was — and runs the new venue from there.
func (a *SellerPayoutAdmin) Reroute(ctx context.Context, id, admin uuid.UUID, note, to string) error {
	if strings.TrimSpace(note) == "" {
		return ErrPayoutNeedsNote
	}
	to = strings.ToLower(strings.TrimSpace(to))
	if to != models.SellerPayoutVenueCompany && to != models.SellerPayoutVenuePaycrest {
		return ErrPayoutBadRoute
	}
	p, err := a.load(ctx, id)
	if err != nil {
		return err
	}
	return a.rerouteLoaded(ctx, p, admin, note, to)
}

// rerouteLoaded is Reroute on a row already read: every safety decision below is
// made on p, and the final update only applies while the row is still exactly p.
func (a *SellerPayoutAdmin) rerouteLoaded(ctx context.Context, p *models.SellerPayoutOrder, admin uuid.UUID, note, to string) error {
	if p.Status != models.SellerPayoutNeedsReview {
		return ErrPayoutNotParked
	}
	if p.Venue == to {
		return fmt.Errorf("%w: it is already on the %s route", ErrPayoutBadRoute, to)
	}
	if to == models.SellerPayoutVenueCompany {
		if err := a.companyCanTake(ctx, p); err != nil {
			return err
		}
	}
	if to == models.SellerPayoutVenuePaycrest && !a.orch.canBridge(p) {
		return ErrPayoutRouteUnavailable
	}
	if p.PaycrestOrderID != nil {
		return ErrPayoutRerouteUnsafe
	}
	// A company transfer that may have left blocks any change of route.
	if !companyNothingSent(p) {
		return ErrPayoutRerouteUnsafe
	}
	legs, err := a.orch.legs.ForOrder(ctx, p.ID)
	if err != nil {
		return err
	}
	for _, l := range legs {
		if l.TxHash != "" || l.ApproveTxHash != "" || l.Status == models.RampLegStatusConfirmed || l.Status == models.RampLegStatusPending {
			return ErrPayoutRerouteUnsafe
		}
	}
	// Back to the proven state: a release that was never proven is awaited again.
	status := models.SellerPayoutReceived
	if p.ReceivedAmount == "" || p.ParkedFrom == models.SellerPayoutAwaitingRelease {
		status = models.SellerPayoutAwaitingRelease
	}
	updates := map[string]any{
		"status": status, "venue": to, "parked_from": "", "retry_safe": false, "failure_reason": "",
		"attempt_started_at": a.orch.now(), "next_attempt_at": nil,
	}
	if p.CompanyAttempts > 0 {
		// Only a refusal on the first reference gets here (companyNothingSent): no
		// transfer exists, so the counter starts over, and any later company
		// attempt takes a new reference generation.
		updates["company_attempts"] = 0
		updates["company_ref_gen"] = gorm.Expr("company_ref_gen + 1")
		updates["company_transfer_id"] = ""
	}
	// Compare-and-set on everything the safety decision read, not just the status: a
	// retry and a new park by another operator in between changes at least one of them.
	res := a.db.WithContext(ctx).Model(&models.SellerPayoutOrder{}).
		Where("id = ? AND status = ? AND venue = ? AND parked_from = ? AND company_attempts = ? AND paycrest_order_id IS NULL AND updated_at = ?",
			p.ID, models.SellerPayoutNeedsReview, p.Venue, p.ParkedFrom, p.CompanyAttempts, p.UpdatedAt).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrPayoutNotParked
	}
	a.audit(ctx, "seller_payout.reroute", admin, p, map[string]any{
		"note": note, "fromVenue": p.Venue, "to": to, "status": status})
	return nil
}

// canBridge is true when the Paycrest route can run for p at all: the source lane,
// the Base lane the bridge lands on, a LiFi quoter and the Paycrest service.
func (o *SellerPayoutOrchestrator) canBridge(p *models.SellerPayoutOrder) bool {
	return o.treasury != nil && o.treasury.Enabled() && o.treasury.Address() != "" &&
		o.treasury.Lane(p.ChainKey) != nil && o.treasury.Lane("base") != nil && o.lifi != nil && o.paycrest != nil
}

// companyCanTake applies the company route's own limits to a payout being moved
// onto it: a central account is set up, the amount is within the route's range, and
// the float covers it on top of what is already promised. Anything else is refused
// here and not parked later.
func (a *SellerPayoutAdmin) companyCanTake(ctx context.Context, p *models.SellerPayoutOrder) error {
	if a.orch.company == nil {
		return ErrPayoutRouteUnavailable
	}
	if p.FiatTargetKobo < companyMinPayoutKobo || (a.orch.companyMaxKobo > 0 && p.FiatTargetKobo > a.orch.companyMaxKobo) {
		return fmt.Errorf("%w: ₦%.2f is outside what the company route pays", ErrPayoutRouteUnavailable, float64(p.FiatTargetKobo)/100)
	}
	promised, n, err := openCompanyPromises(ctx, a.db)
	if err != nil {
		return err
	}
	bals, err := a.orch.company.FetchBalances(ctx)
	if err != nil {
		return fmt.Errorf("cannot read the central account balance: %w", err)
	}
	avail, ok := ngnAvailable(bals)
	if need := promised + p.FiatTargetKobo + (n+1)*a.orch.companyFeeKobo; !ok || avail*100 < float64(need) {
		return fmt.Errorf("%w: the central account holds ₦%.2f and ₦%.2f is needed with what is already promised",
			ErrPayoutRouteUnavailable, avail, float64(need)/100)
	}
	return nil
}

// PayoutHealth is what an operator needs at a glance: can the company route pay,
// is the treasury able to act, what is waiting and for how long, and what is
// still to rebalance. Every part that could not be read says so; nothing is
// shown as zero when it is unknown.
type PayoutHealth struct {
	Central  *CentralHealth        `json:"central,omitempty"`
	Treasury []TreasuryChainHealth `json:"treasury"`
	// Counts is the open payouts by status.
	Counts map[string]int64 `json:"counts"`
	// OldestParkedAt is when the longest-waiting parked payout stopped.
	OldestParkedAt *time.Time `json:"oldestParkedAt,omitempty"`
	// RebalanceOwed is the USDC held for paid company payouts, per token, base units.
	RebalanceOwed map[string]string `json:"rebalanceOwed"`
	// Problems lists what could not be read.
	Problems []string `json:"problems,omitempty"`
}

// CentralHealth is the central account against what is already promised to it.
type CentralHealth struct {
	AvailableKobo int64 `json:"availableKobo"`
	PromisedKobo  int64 `json:"promisedKobo"`
	OpenPayouts   int64 `json:"openPayouts"`
	// FreeKobo is available less promised (and the transfer fee per open payout).
	FreeKobo int64 `json:"freeKobo"`
}

// TreasuryChainHealth is the treasury's native gas and kept stablecoins on a chain.
type TreasuryChainHealth struct {
	Chain     string `json:"chain"`
	Address   string `json:"address"`
	NativeWei string `json:"nativeWei"`
	FloorWei  string `json:"floorWei"`
	// StableUSD is null when a stablecoin balance could not be read (see problems).
	StableUSD *float64 `json:"stableUsd"`
	Low       bool     `json:"low"`
}

func (a *SellerPayoutAdmin) Health(ctx context.Context) (*PayoutHealth, error) {
	h := &PayoutHealth{Counts: map[string]int64{}, RebalanceOwed: map[string]string{}, Treasury: []TreasuryChainHealth{}}

	var rows []struct {
		Status string
		N      int64
	}
	if err := a.db.WithContext(ctx).Model(&models.SellerPayoutOrder{}).Select("status, COUNT(*) AS n").
		Where("status NOT IN ?", []string{models.SellerPayoutPaid, models.SellerPayoutPaidWallet, models.SellerPayoutCancelled}).
		Group("status").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		h.Counts[r.Status] = r.N
	}
	var parked []models.SellerPayoutOrder
	if err := a.db.WithContext(ctx).Where("status = ?", models.SellerPayoutNeedsReview).
		Order("updated_at ASC").Limit(1).Find(&parked).Error; err != nil {
		h.Problems = append(h.Problems, "oldest parked payout: "+err.Error())
	} else if len(parked) > 0 {
		t := parked[0].UpdatedAt
		h.OldestParkedAt = &t
	}
	if owed, err := a.CompanyOwed(ctx); err != nil {
		h.Problems = append(h.Problems, "rebalance owed: "+err.Error())
	} else {
		h.RebalanceOwed = owed.Totals
	}

	if a.orch.company != nil {
		promised, n, err := openCompanyPromises(ctx, a.db)
		bals, berr := a.orch.company.FetchBalances(ctx)
		switch {
		case err != nil:
			h.Problems = append(h.Problems, "company promises: "+err.Error())
		case berr != nil:
			h.Problems = append(h.Problems, "central account balance: "+berr.Error())
		default:
			avail, ok := ngnAvailable(bals)
			if !ok {
				h.Problems = append(h.Problems, "the central account lists no NGN balance")
				break
			}
			availKobo := int64(math.Round(avail * 100))
			h.Central = &CentralHealth{
				AvailableKobo: availKobo, PromisedKobo: promised, OpenPayouts: n,
				FreeKobo: availKobo - promised - n*a.orch.companyFeeKobo,
			}
		}
	}

	if a.orch.treasury != nil && a.orch.treasury.Enabled() {
		floats, err := a.orch.treasury.FloatBalances(ctx)
		if err != nil {
			h.Problems = append(h.Problems, "treasury balances: "+err.Error())
		}
		sort.Slice(floats, func(i, j int) bool { return floats[i].Chain < floats[j].Chain })
		for _, f := range floats {
			c := TreasuryChainHealth{Chain: f.Chain, Address: f.Address}
			if f.TokenReadFailed {
				h.Problems = append(h.Problems, "treasury "+f.Chain+" stablecoin balance")
			} else {
				usd := f.TokenUSD
				c.StableUSD = &usd
			}
			if f.BalanceWei != nil {
				c.NativeWei = f.BalanceWei.String()
			}
			if f.FloorWei != nil {
				c.FloorWei = f.FloorWei.String()
				c.Low = f.BalanceWei != nil && f.BalanceWei.Cmp(f.FloorWei) < 0
			}
			h.Treasury = append(h.Treasury, c)
		}
	}
	return h, nil
}
