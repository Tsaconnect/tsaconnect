package services

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// LaneCommitments is the gas (units) a key lane already owes for work in
// flight, counted from the database and cached briefly so a quote does not
// re-count on every call. Registered on the lane as a reservation
// (KeyLane.AddReservation(name, c.Units)): every send but the owed ones
// themselves (ReservedSend) must leave enough for it. A count that cannot be
// read is an error, never zero — callers fail closed.
//
// One read runs at a time per generation, on its own context (never the first
// caller's, which may be cancelled for the others); each caller waits for it
// on its own ctx. UnitsFresh starts a new generation, and a read only stores
// its result when no newer generation has stored one, so a slow older read can
// never overwrite a fresher count.
type LaneCommitments struct {
	name  string
	count func(ctx context.Context) (uint64, error) // gas units owed
	now   func() time.Time

	mu        sync.Mutex
	units     uint64
	at        time.Time
	storedGen uint64 // generation of the stored count
	gen       uint64 // latest generation started
	flight    *commitmentsRead
}

type commitmentsRead struct {
	gen   uint64
	done  chan struct{}
	units uint64
	err   error
}

// laneCommitmentsTTL bounds how stale a quote's count may be; a failed count
// is never cached.
const laneCommitmentsTTL = 5 * time.Second

// laneCommitmentsReadTimeout bounds one count, whoever waits for it.
const laneCommitmentsReadTimeout = 10 * time.Second

func newLaneCommitments(name string, count func(ctx context.Context) (uint64, error)) *LaneCommitments {
	return &LaneCommitments{name: name, count: count, now: time.Now}
}

// Units is the gas owed, from a count younger than laneCommitmentsTTL. It is
// a LaneReservation.
func (c *LaneCommitments) Units(ctx context.Context) (uint64, error) {
	if c == nil {
		return 0, nil
	}
	c.mu.Lock()
	if !c.at.IsZero() && c.now().Sub(c.at) < laneCommitmentsTTL {
		u := c.units
		c.mu.Unlock()
		return u, nil
	}
	r := c.flight
	if r == nil {
		r = c.startLocked()
	}
	c.mu.Unlock()
	return c.wait(ctx, r)
}

// UnitsFresh counts now — for an open, which must not pass on a count that
// predates the order before it.
func (c *LaneCommitments) UnitsFresh(ctx context.Context) (uint64, error) {
	if c == nil {
		return 0, nil
	}
	c.mu.Lock()
	r := c.startLocked()
	c.mu.Unlock()
	return c.wait(ctx, r)
}

// startLocked starts a read of a new generation. Caller holds mu.
func (c *LaneCommitments) startLocked() *commitmentsRead {
	c.gen++
	r := &commitmentsRead{gen: c.gen, done: make(chan struct{})}
	c.flight = r
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), laneCommitmentsReadTimeout)
		defer cancel()
		n, err := c.count(ctx)
		c.mu.Lock()
		if err == nil && r.gen > c.storedGen {
			c.units, c.at, c.storedGen = n, c.now(), r.gen
		}
		if c.flight == r {
			c.flight = nil
		}
		c.mu.Unlock()
		if err != nil {
			err = fmt.Errorf("%s: %w", c.name, err)
		}
		r.units, r.err = n, err
		close(r.done)
	}()
	return r
}

func (c *LaneCommitments) wait(ctx context.Context, r *commitmentsRead) (uint64, error) {
	select {
	case <-r.done:
		return r.units, r.err
	case <-ctx.Done():
		return 0, fmt.Errorf("%s: %w", c.name, ctx.Err())
	}
}

// NewPoolFillCommitments counts the fills the release key still owes
// shared-pool orders: one per order that may still fill and has none recorded
// (an unpaid order only while it can still be claimed: poolReservingIntents).
func NewPoolFillCommitments(db *gorm.DB) *LaneCommitments {
	c := newLaneCommitments("pool_fills", nil)
	c.count = func(ctx context.Context) (uint64, error) {
		var n int64
		err := poolReservingIntents(db.WithContext(ctx).Model(&models.PoolOrder{}).
			Joins("JOIN it_intents ON it_intents.id = pool_orders.intent_id"), c.now()).
			Where("pool_orders.fill_units IS NOT NULL AND pool_orders.fill_tx_hash = ''").
			Count(&n).Error
		return uint64(n) * vaultFillGas, err
	}
	return c
}

// treasuryRouteReserveGas is one converted order's owed route on a treasury
// lane: the ceiling a route may ask for, plus its approve.
const treasuryRouteReserveGas = maxRouteGasLimit + treasuryERC20Gas

// NewPoolTreasuryCommitments counts what the pool chain's treasury lane owes
// pool orders already in flight: one delivery transfer per pool order opened
// or paid but not delivered — an unpaid one only while it can still be
// claimed (stableBuyClaimWindow). (A converted delivery's route is counted by
// the conversion lane's own NewConversionTreasuryCommitments.)
func NewPoolTreasuryCommitments(db *gorm.DB) *LaneCommitments {
	c := newLaneCommitments("pool_treasury_owed", nil)
	c.count = func(ctx context.Context) (uint64, error) {
		var n int64
		err := db.WithContext(ctx).Model(&models.ITIntent{}).
			Where("provider IN ?", poolProviders()).
			Where("direction = ?", models.ITDirectionBuy).
			Where("(status IN ? OR (status = ? AND created_at > ?))",
				[]string{models.ITStatusPaymentReceived, models.ITStatusDelivering}, models.ITStatusPendingPayment,
				c.now().Add(-stableBuyClaimWindow)).
			Count(&n).Error
		return uint64(n) * treasuryERC20Gas, err
	}
	return c
}

// NewConversionTreasuryCommitments counts one route (+ approve) per converted
// buy still converting (or refunding) from chainKey's treasury lane.
func NewConversionTreasuryCommitments(db *gorm.DB, chainKey string) *LaneCommitments {
	return newLaneCommitments("treasury_converting_"+strings.ToLower(chainKey), func(ctx context.Context) (uint64, error) {
		n, err := convertingOn(ctx, db, chainKey)
		return n * treasuryRouteReserveGas, err
	})
}

// NewSellerPayoutCommitments counts the gas the treasury lane on chainKey still
// owes seller payouts: a route (+ approve) per payout waiting to bridge from the
// escrow's chain, a transfer per payout waiting to deposit on Base.
func NewSellerPayoutCommitments(db *gorm.DB, chainKey string) *LaneCommitments {
	chainKey = strings.ToLower(chainKey)
	return newLaneCommitments("seller_payout_"+chainKey, func(ctx context.Context) (uint64, error) {
		statuses := sellerPayoutHeldOn[chainKey]
		if len(statuses) == 0 {
			return 0, nil
		}
		var n int64
		if err := db.WithContext(ctx).Model(&models.SellerPayoutOrder{}).Where("status IN ? AND COALESCE(venue, ?) <> ?", statuses, models.SellerPayoutVenuePaycrest, models.SellerPayoutVenueCompany).
			Count(&n).Error; err != nil {
			// A company payout needs no bridge or deposit, so it reserves no lane gas.
			return 0, err
		}
		if chainKey == "sonic" {
			return uint64(n) * treasuryRouteReserveGas, nil
		}
		return uint64(n) * treasuryERC20Gas, nil
	})
}

// NewCompanyWalletCommitments counts the company wallet's paid, undelivered
// sales on chainKey: one transfer each.
func NewCompanyWalletCommitments(db *gorm.DB, chainKey string) *LaneCommitments {
	return newLaneCommitments("company_wallet_owed_"+strings.ToLower(chainKey), func(ctx context.Context) (uint64, error) {
		var n int64
		err := db.WithContext(ctx).Model(&models.ITIntent{}).
			Where("provider IN ?", companyWalletProviders()).
			Where("direction = ? AND dest_chain = ? AND status IN ?",
				models.ITDirectionBuy, strings.ToUpper(chainKey),
				[]string{models.ITStatusPaymentReceived, models.ITStatusDelivering}).
			Count(&n).Error
		return uint64(n) * treasuryERC20Gas, err
	})
}

func convertingOn(ctx context.Context, db *gorm.DB, chainKey string) (uint64, error) {
	var n int64
	err := db.WithContext(ctx).Model(&models.PaycrestOrderRecord{}).
		Where("route_kind = ? AND direction = ? AND chain_key = ? AND status IN ?",
			models.RouteKindConverted, models.PaycrestDirectionBuy, chainKey,
			[]string{models.PaycrestStatusConverting, models.PaycrestStatusRefunding}).
		Count(&n).Error
	return uint64(n), err
}
