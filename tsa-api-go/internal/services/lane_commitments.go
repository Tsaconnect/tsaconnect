package services

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// LaneCommitments is the gas a key lane already owes for work in flight,
// counted from the database and cached briefly (single-flight) so a quote or
// an open does not re-count on every call. Registered on the lane as a
// reservation (KeyLane.AddReservation): every send but the owed ones
// themselves (ReservedSend) must leave enough for it. A count that cannot be
// read is an error, never zero — callers fail closed.
type LaneCommitments struct {
	name  string
	count func(ctx context.Context) (uint64, error) // gas units owed
	cache *cachedReader[uint64]
	now   func() time.Time
}

// laneCommitmentsTTL bounds how stale a count may be: a few seconds of lag is
// covered by gasFeeBuffer; a failed count is not cached.
const laneCommitmentsTTL = 5 * time.Second

func newLaneCommitments(name string, count func(ctx context.Context) (uint64, error)) *LaneCommitments {
	return &LaneCommitments{name: name, count: count, cache: newCachedReader[uint64](laneCommitmentsTTL, 0), now: time.Now}
}

// UnitsFresh is Units counted now — for an open, which must not pass on a
// count that predates the order before it.
func (c *LaneCommitments) UnitsFresh(ctx context.Context) (uint64, error) {
	if c == nil {
		return 0, nil
	}
	c.cache.Forget(c.name)
	return c.Units(ctx)
}

// Units is the gas owed, from a reading younger than laneCommitmentsTTL.
func (c *LaneCommitments) Units(ctx context.Context) (uint64, error) {
	if c == nil {
		return 0, nil
	}
	return c.cache.Get(c.name, c.now(), func() (uint64, error) {
		n, err := c.count(ctx)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", c.name, err)
		}
		c.cache.Put(c.name, c.now(), n, nil)
		return n, nil
	})
}

// Reservation is Units priced at price with gasFeeBuffer: a LaneReservation.
func (c *LaneCommitments) Reservation(ctx context.Context, price *big.Int) (*big.Int, error) {
	units, err := c.Units(ctx)
	if err != nil {
		return nil, err
	}
	return feeNeed(units, price, nil), nil
}

// NewPoolReleaseCommitments counts the release-key calls the pool chain's live
// slices still owe — one each: adminRelease once paid, cancelByTimeout (the
// expiry canceller, release key) if never paid, adminResolve if disputed.
func NewPoolReleaseCommitments(db *gorm.DB, chainID int64) *LaneCommitments {
	return newLaneCommitments(fmt.Sprintf("pool_releases_%d", chainID), func(ctx context.Context) (uint64, error) {
		var n int64
		err := db.WithContext(ctx).Model(&models.P2PTrade{}).
			Where("it_intent_id IS NOT NULL AND chain_id = ? AND status IN ?", chainID, poolSliceOutstandingStatuses).
			Count(&n).Error
		return uint64(n) * poolReleaseCallGas, err
	})
}

// poolSliceOutstandingStatuses are the slice states that still need one
// release-key transaction each.
var poolSliceOutstandingStatuses = []string{
	models.P2PTradeStatusPendingPayment, models.P2PTradeStatusPaid,
	models.P2PTradeStatusReleasing, models.P2PTradeStatusDisputed,
}

// treasuryRouteReserveGas is one converted order's owed route on a treasury
// lane: the ceiling a route may ask for, plus its approve.
const treasuryRouteReserveGas = maxRouteGasLimit + treasuryERC20Gas

// NewPoolTreasuryCommitments counts what the pool chain's treasury lane owes
// pool orders already in flight: one delivery transfer per pool order opened
// or paid but not delivered. (A converted delivery's route is counted by the
// conversion lane's own NewConversionTreasuryCommitments.)
func NewPoolTreasuryCommitments(db *gorm.DB) *LaneCommitments {
	return newLaneCommitments("pool_treasury_owed", func(ctx context.Context) (uint64, error) {
		var n int64
		err := db.WithContext(ctx).Model(&models.ITIntent{}).
			Where("provider IN ?", poolProviders()).
			Where("direction = ? AND status IN ?", models.ITDirectionBuy,
				[]string{models.ITStatusPendingPayment, models.ITStatusPaymentReceived, models.ITStatusDelivering}).
			Count(&n).Error
		return uint64(n) * treasuryERC20Gas, err
	})
}

// NewConversionTreasuryCommitments counts one route (+ approve) per converted
// buy still converting (or refunding) from chainKey's treasury lane.
func NewConversionTreasuryCommitments(db *gorm.DB, chainKey string) *LaneCommitments {
	return newLaneCommitments("treasury_converting_"+strings.ToLower(chainKey), func(ctx context.Context) (uint64, error) {
		n, err := convertingOn(ctx, db, chainKey)
		return n * treasuryRouteReserveGas, err
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
