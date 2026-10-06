package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

var (
	ErrTreasuryUnavailable = errors.New("company wallet: not available")
	// ErrTreasuryStockShort: free stock does not cover the order — at open
	// (a concurrent open won it), at a live re-price, or at send.
	ErrTreasuryStockShort = errors.New("company wallet: not enough stock for this order right now")
	// ErrTreasuryDailyCapReached: RAMP_TREASURY_DAILY_USER_MAX_USD for this user.
	ErrTreasuryDailyCapReached = errors.New("company wallet: daily purchase limit reached")
)

func (s *ITService) SetCompanyWallet(w *CompanyWallet) { s.wallet = w }
func (s *ITService) HasCompanyWallet() bool            { return s != nil && s.wallet != nil }
func (s *ITService) CompanyWallet() *CompanyWallet     { return s.wallet }

// TreasuryChains, TreasuryTokens and TreasuryCanPayGas are nil-safe: a dormant
// wallet offers nothing.
func (s *ITService) TreasuryChains() []string {
	if !s.HasCompanyWallet() {
		return nil
	}
	return s.wallet.QuotableChains()
}

func (s *ITService) TreasuryTokens(chain string) []string {
	if !s.HasCompanyWallet() {
		return nil
	}
	return s.wallet.Tokens(chain)
}

func (s *ITService) TreasuryCanPayGas(ctx context.Context, chain string) bool {
	return s.HasCompanyWallet() && s.wallet.CanPayForTransfer(ctx, chain)
}

// withStockLock runs fn in one transaction holding the (chain, token) advisory
// lock (spec §5.3, S4). Every company-wallet check-then-write goes through it:
// open, live pricing at claim, operator re-price. SQLite (tests) is
// single-writer, as the checkout lock notes.
func (s *ITService) withStockLock(ctx context.Context, chain, token string, fn func(tx *gorm.DB) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			key := "ramp_treasury:" + strings.ToLower(strings.TrimSpace(chain)) + ":" + strings.ToUpper(strings.TrimSpace(token))
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", key).Error; err != nil {
				return fmt.Errorf("company wallet: stock lock: %w", err)
			}
		}
		return fn(tx)
	})
}

// reservedTreasuryUnits is what open company-wallet orders have been promised
// on (chain, token), in 6-dp units — derived, never stored (S4): pending orders
// inside their honour window plus the grace (S5), and every paid, undelivered
// order. exclude leaves one order out (its own live re-price). Summed in Go
// (Decision D46).
func (s *ITService) reservedTreasuryUnits(ctx context.Context, db *gorm.DB, chain, token string, exclude uuid.UUID) (*big.Int, error) {
	cutoff := time.Now().Add(-s.rampTerms().honourGrace)
	q := db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("provider = ? AND direction = ? AND dest_chain = ? AND asset = ?",
			models.ITProviderTreasury, models.ITDirectionBuy,
			strings.ToUpper(strings.TrimSpace(chain)), strings.ToUpper(strings.TrimSpace(token))).
		Where("((status = ? AND rate_expires_at > ?) OR status IN ?)",
			models.ITStatusPendingPayment, cutoff,
			[]string{models.ITStatusPaymentReceived, models.ITStatusDelivering})
	if exclude != uuid.Nil {
		q = q.Where("id <> ?", exclude)
	}
	var amounts []string
	if err := q.Pluck("asset_amount", &amounts).Error; err != nil {
		return nil, fmt.Errorf("company wallet: read reservations: %w", err)
	}
	sum := new(big.Int)
	for _, a := range amounts {
		if v, ok := new(big.Int).SetString(strings.TrimSpace(a), 10); ok && v.Sign() > 0 {
			sum.Add(sum, v)
		}
	}
	return sum, nil
}

// freeTreasuryStock is balance − reserved, clamped at 0 with an ALERT when an
// operator withdrawal left the wallet over-committed (spec §5.3).
func (s *ITService) freeTreasuryStock(ctx context.Context, db *gorm.DB, chain, token string, exclude uuid.UUID, fresh bool) (free, balance *big.Int, err error) {
	if !s.HasCompanyWallet() {
		return nil, nil, ErrTreasuryUnavailable
	}
	balance, err = s.wallet.Balance(ctx, chain, token, fresh)
	if err != nil {
		return nil, nil, err
	}
	reserved, err := s.reservedTreasuryUnits(ctx, db, chain, token, exclude)
	if err != nil {
		return nil, nil, err
	}
	free = new(big.Int).Sub(balance, reserved)
	if free.Sign() < 0 {
		log.Printf("[it treasury buy] ALERT ramp_treasury_stock_overcommitted chain=%s token=%s balance=%s reserved=%s",
			strings.ToLower(chain), strings.ToUpper(token), balance, reserved)
		free.SetInt64(0)
	}
	return free, balance, nil
}

// TreasuryFreeUnits is free stock for quotes, legs and the monitor, cached 5s
// per (chain, token) on top of the 30s balance cache, so a burst of public
// quotes costs one reservation SUM. Never for a decision that moves money.
func (s *ITService) TreasuryFreeUnits(ctx context.Context, chain, token string) (*big.Int, error) {
	if !s.HasCompanyWallet() {
		return nil, ErrTreasuryUnavailable
	}
	if free, ok := s.wallet.cachedFree(chain, token); ok {
		return free, nil
	}
	free, _, err := s.freeTreasuryStock(ctx, s.db, chain, token, uuid.Nil, false)
	if err != nil {
		return nil, err
	}
	s.wallet.storeFree(chain, token, free)
	return free, nil
}

// OpenTreasuryBuy opens a company-wallet purchase (spec §4.1/§4.2): KYC (no IT
// agreement, §13 Q3), a chain and token the wallet offers, her own EVM address
// (Decision D48), the $200 maximum, and the stock taken under the lock.
func (s *ITService) OpenTreasuryBuy(ctx context.Context, userID uuid.UUID, q *StableBuyQuote, chain, destAddress string) (*models.ITIntent, *StableBuyInstructions, error) {
	return s.openTreasuryBuy(ctx, userID, q, chain, destAddress, nil)
}

// openTreasuryBuy is OpenTreasuryBuy with `also` written in the intent's own
// transaction, under the stock lock (a converted buy's record, Decision C4).
func (s *ITService) openTreasuryBuy(ctx context.Context, userID uuid.UUID, q *StableBuyQuote, chain, destAddress string,
	also func(tx *gorm.DB, intent *models.ITIntent) error) (*models.ITIntent, *StableBuyInstructions, error) {
	if !s.HasCompanyWallet() {
		return nil, nil, ErrTreasuryUnavailable
	}
	if q == nil {
		return nil, nil, errors.New("it treasury buy: nil quote")
	}
	if !s.wallet.Offers(chain, q.Asset) {
		return nil, nil, fmt.Errorf("%w: the company wallet does not sell %s on %s", ErrUnsupportedChainAsset, q.Asset, chain)
	}
	// This function writes money-bearing rows, so it enforces its own limits
	// rather than trusting that its caller quoted first.
	if !s.wallet.ReceiptsReadable(chain) {
		return nil, nil, fmt.Errorf("%w: %s cannot prove deliveries right now", ErrTreasuryUnavailable, chain)
	}
	lim := s.treasuryLimits()
	if q.FiatMinor == nil || q.FiatMinor.Cmp(lim.minKobo) < 0 {
		return nil, nil, fmt.Errorf("%w: minimum is %s %s", ErrStableBuyBelowMinimum, FormatMinor(lim.minKobo.String()), q.Currency)
	}
	if err := s.stableBuyPrechecks(ctx, userID, q, false); err != nil {
		return nil, nil, err
	}
	if q.AssetUnits != nil && q.AssetUnits.Sign() > 0 {
		if err := s.CheckTreasuryDailyCap(ctx, userID, q.AssetUnits); err != nil {
			return nil, nil, err
		}
	}
	return s.openStableBuy(ctx, userID, q, chain, destAddress, stableBuyOpen{
		provider: models.ITProviderTreasury, routed: true,
		maxUnits: lim.maxUnits,
		validate: func(_, _, addr string) error {
			if !common.IsHexAddress(addr) {
				return fmt.Errorf("%w: delivery needs an EVM address", ErrBadDeliveryAddress)
			}
			return nil
		},
		insert: func(ctx context.Context, i *models.ITIntent) error { return s.insertTreasuryIntentWith(ctx, i, also) },
	})
}

// insertTreasuryIntent writes the order only if free stock covers it, under
// the lock: two opens can never both take the last tokens (S4).
func (s *ITService) insertTreasuryIntent(ctx context.Context, intent *models.ITIntent) error {
	return s.insertTreasuryIntentWith(ctx, intent, nil)
}

// insertTreasuryIntentWith is insertTreasuryIntent plus `also`, run in the
// same transaction under the same lock: the intent and what it brings commit
// together or not at all.
func (s *ITService) insertTreasuryIntentWith(ctx context.Context, intent *models.ITIntent, also func(tx *gorm.DB, intent *models.ITIntent) error) error {
	units, ok := new(big.Int).SetString(strings.TrimSpace(intent.AssetAmount), 10)
	if !ok || units.Sign() <= 0 {
		return fmt.Errorf("it treasury buy: unusable asset amount %q", intent.AssetAmount)
	}
	// The reservation filters on upper-case chain and token; a row written any
	// other way would be invisible to it.
	intent.Asset = strings.ToUpper(strings.TrimSpace(intent.Asset))
	intent.DestChain = strings.ToUpper(strings.TrimSpace(intent.DestChain))
	chain := strings.ToLower(intent.DestChain)
	return s.withStockLock(ctx, chain, intent.Asset, func(tx *gorm.DB) error {
		free, _, err := s.freeTreasuryStock(ctx, tx, chain, intent.Asset, uuid.Nil, true)
		if err != nil {
			return err
		}
		if free.Cmp(units) < 0 {
			log.Printf("[it treasury buy] METRIC ramp_treasury_stock_short_at_open chain=%s token=%s needed=%s free=%s",
				chain, intent.Asset, units, free)
			return ErrTreasuryStockShort
		}
		if err := tx.Create(intent).Error; err != nil {
			return err
		}
		if also != nil {
			return also(tx, intent)
		}
		return nil
	})
}

// CheckTreasuryDailyCap refuses when this order would take the user past
// RAMP_TREASURY_DAILY_USER_MAX_USD over a rolling 24h (§13 Q2, D59).
func (s *ITService) CheckTreasuryDailyCap(ctx context.Context, userID uuid.UUID, units *big.Int) error {
	return s.checkTreasuryDailyCap(ctx, s.db, userID, units, uuid.Nil)
}

// checkTreasuryDailyCap is CheckTreasuryDailyCap on db, leaving out exclude:
// an order being priced live counts at its new amount, not twice.
func (s *ITService) checkTreasuryDailyCap(ctx context.Context, db *gorm.DB, userID uuid.UUID, units *big.Int, exclude uuid.UUID) error {
	var amounts []string
	q := db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("user_id = ? AND provider = ? AND direction = ? AND status NOT IN ? AND created_at >= ?",
			userID, models.ITProviderTreasury, models.ITDirectionBuy,
			[]string{models.ITStatusCancelled, models.ITStatusRefunded}, time.Now().Add(-24*time.Hour))
	if exclude != uuid.Nil {
		q = q.Where("id <> ?", exclude)
	}
	if err := q.Pluck("asset_amount", &amounts).Error; err != nil {
		return fmt.Errorf("company wallet: read daily total: %w", err)
	}
	total := new(big.Int).Set(units)
	for _, a := range amounts {
		if v, ok := new(big.Int).SetString(strings.TrimSpace(a), 10); ok {
			total.Add(total, v)
		}
	}
	if lim := s.treasuryLimits().dailyUserMaxUnits; total.Cmp(lim) > 0 {
		log.Printf("[it treasury buy] METRIC ramp_treasury_daily_cap_hit user=%s total_units=%s cap_units=%s", userID, total, lim)
		return ErrTreasuryDailyCapReached
	}
	return nil
}
