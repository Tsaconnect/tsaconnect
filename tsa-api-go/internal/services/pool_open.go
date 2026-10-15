package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// PoolOpenCap bounds a pool open inside the router's open budget: one fresh
// vault read and one transaction under the pool lock.
const PoolOpenCap = 5 * time.Second

var (
	// ErrPoolClosed: the pool cannot take an order now (gates, gas, nonce).
	ErrPoolClosed = errors.New("liquidity pool: closed right now; try again later")
	// ErrPoolCannotFill: the vault's free liquidity does not cover the order.
	ErrPoolCannotFill = errors.New("liquidity pool: that amount cannot be filled right now")
	// ErrPoolDailyCapReached: pool_user_daily_max for this user; the router
	// fails over to another venue.
	ErrPoolDailyCapReached = errors.New("liquidity pool: daily purchase limit reached")
)

// scopePoolBuys is the pool's it_intents rows (poolProviders: the P7 lint).
func scopePoolBuys(db *gorm.DB) *gorm.DB {
	return db.Model(&models.ITIntent{}).Where("provider IN ?", poolProviders())
}

// CheckPoolDailyCap refuses when units would take her past pool_user_daily_max.
func (s *ITService) CheckPoolDailyCap(ctx context.Context, userID uuid.UUID, units *big.Int, set PoolSettings) error {
	return s.checkDailyBuyCap(ctx, s.db, userID, units, uuid.Nil, dailyBuyCap{
		scope: scopePoolBuys, limit: set.UserDailyMaxUnits, reached: ErrPoolDailyCapReached, metric: "ramp_pool_daily_cap_hit",
		logTag: "liquidity", who: "liquidity pool",
	})
}

// OpenPoolBuy opens a direct pool order: the pool token on the pool chain to
// her own EVM wallet (spec 2026-10-14 §4.2).
func (s *ITService) OpenPoolBuy(ctx context.Context, userID uuid.UUID, p *PoolPrice, destAddress string) (*models.ITIntent, *StableBuyInstructions, error) {
	d := s.poolDesk
	if d == nil || p == nil || p.Quote == nil {
		return nil, nil, ErrPoolClosed
	}
	// Never the company's own wallets (parsed, never string-compared).
	if s.wallet.IsConversionTreasury(destAddress) || SameEVMAddress(destAddress, s.converted.TreasuryAddress) ||
		SameEVMAddress(destAddress, d.c.Treasury.Address()) {
		return nil, nil, fmt.Errorf("%w: that address cannot receive a purchase", ErrBadDeliveryAddress)
	}
	return s.openVaultBuy(ctx, userID, p, destAddress, nil)
}
