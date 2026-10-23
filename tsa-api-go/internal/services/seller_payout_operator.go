package services

import (
	"context"
	"errors"
	"fmt"
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
	if p.Status != models.SellerPayoutNeedsReview {
		return ErrPayoutNotParked
	}
	if p.Venue == to {
		return fmt.Errorf("%w: it is already on the %s route", ErrPayoutBadRoute, to)
	}
	if to == models.SellerPayoutVenueCompany && a.orch.company == nil {
		return ErrPayoutRouteUnavailable
	}
	if to == models.SellerPayoutVenuePaycrest && !a.orch.treasuryReadyForBridge() {
		return ErrPayoutRouteUnavailable
	}
	if p.PaycrestOrderID != nil {
		return ErrPayoutRerouteUnsafe
	}
	// A company transfer that may have left blocks any change of route: only a
	// provider refusal (retry-safe, parked from the transfer) proves none exists.
	if p.CompanyAttempts > 0 && !(p.ParkedFrom == models.SellerPayoutPayingCompany && p.RetrySafe) {
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
		"venue": to, "parked_from": "", "retry_safe": false, "failure_reason": "",
		"attempt_started_at": a.orch.now(), "next_attempt_at": nil,
	}
	if p.CompanyAttempts > 0 {
		// Whatever comes next on the company route is a new attempt on a new reference.
		updates["company_ref_gen"] = gorm.Expr("company_ref_gen + 1")
		updates["company_transfer_id"] = ""
	}
	if err := a.orch.move(ctx, p, status, updates, models.SellerPayoutNeedsReview); err != nil {
		return notParkedIfRaced(err)
	}
	a.audit(ctx, "seller_payout.reroute", admin, p, map[string]any{
		"note": note, "from": p.Venue, "to": to, "status": status})
	return nil
}

// treasuryReadyForBridge is true when the Paycrest route can run at all: a live
// treasury with the Base lane the bridge lands on.
func (o *SellerPayoutOrchestrator) treasuryReadyForBridge() bool {
	return o.treasury != nil && o.treasury.Enabled() && o.treasury.Address() != "" && o.treasury.Lane("base") != nil
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
	Chain     string  `json:"chain"`
	Address   string  `json:"address"`
	NativeWei string  `json:"nativeWei"`
	FloorWei  string  `json:"floorWei"`
	StableUSD float64 `json:"stableUsd"`
	Low       bool    `json:"low"`
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
			availKobo := int64(avail * 100)
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
			c := TreasuryChainHealth{Chain: f.Chain, Address: f.Address, StableUSD: f.TokenUSD}
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
