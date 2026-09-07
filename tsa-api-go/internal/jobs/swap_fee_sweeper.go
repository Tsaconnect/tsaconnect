package jobs

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// SwapFeeSweeper collects platform fees for swaps whose client never reported
// back.
//
// An aggregator swap's fee cannot be computed where the transaction is
// submitted — the amount there is a quantity of the from-token, not dollars —
// so the app settles it afterwards against a re-queried USD figure. The swap is
// irreversibly on-chain by then, and the client owing that call may be killed,
// backgrounded or offline before it makes it. Submit records the debt
// (models.PendingSwapFee); this sweeps whatever is still outstanding.
//
// It is the safety net, not the primary path: it prices the swap from our own
// price service and charges the CONSERVATIVE rate, because it has no quote to
// verify against. A client that settles promptly gets the exact figure and a
// cheaper rate.
//
// Idempotency: accrual uses the same source key as the settle endpoint —
// uuidv5("allswap:"+TxHash) — so a sweep racing a late client settle collapses
// into one fee event in the engine's guard table rather than charging twice.
type SwapFeeSweeper struct {
	DB           *gorm.DB
	FeeService   *services.FeeDistributionService
	PriceService *services.PriceService

	// Grace is how long a swap may stay unsettled before being swept. It must
	// comfortably exceed a normal settle round-trip so the fast path wins the
	// race in the ordinary case.
	Grace time.Duration
	// ConservativeBps is the rate applied to a swept swap.
	ConservativeBps int
	// MaxAttempts bounds retries. A row that keeps failing (an unpriceable
	// token, say) is parked as abandoned and alerted on rather than retried for
	// ever.
	MaxAttempts int
	// BatchSize bounds the work done in one tick.
	BatchSize int
	// Tick is the sweep period.
	Tick time.Duration

	// enqueue accrues one fee event. A field so tests can capture accruals
	// without standing up the engine's ledger tables; nil means use FeeService.
	enqueue func(services.FeeEvent)
}

// accrue returns the sink for fee events, or nil when there is nowhere to put
// them (fee engine off).
func (s *SwapFeeSweeper) accrue() func(services.FeeEvent) {
	if s.enqueue != nil {
		return s.enqueue
	}
	if s.FeeService != nil {
		return s.FeeService.EnqueueFeeSettled
	}
	return nil
}

func (s *SwapFeeSweeper) Name() string { return "swap-fee-sweeper" }

func (s *SwapFeeSweeper) Interval() time.Duration {
	if s.Tick <= 0 {
		return 5 * time.Minute
	}
	return s.Tick
}

func (s *SwapFeeSweeper) grace() time.Duration {
	if s.Grace <= 0 {
		return 10 * time.Minute
	}
	return s.Grace
}

func (s *SwapFeeSweeper) maxAttempts() int {
	if s.MaxAttempts <= 0 {
		return 5
	}
	return s.MaxAttempts
}

func (s *SwapFeeSweeper) batchSize() int {
	if s.BatchSize <= 0 {
		return 100
	}
	return s.BatchSize
}

func (s *SwapFeeSweeper) Run(ctx context.Context) error {
	if s.DB == nil || s.accrue() == nil {
		return nil // fee engine off — nothing to collect into
	}

	var due []models.PendingSwapFee
	cutoff := time.Now().Add(-s.grace())
	if err := s.DB.WithContext(ctx).
		Where("status = ? AND created_at < ?", models.PendingSwapFeePending, cutoff).
		Order("created_at asc").
		Limit(s.batchSize()).
		Find(&due).Error; err != nil {
		return fmt.Errorf("load pending swap fees: %w", err)
	}

	for i := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.sweepOne(ctx, &due[i])
	}
	return nil
}

func (s *SwapFeeSweeper) sweepOne(ctx context.Context, row *models.PendingSwapFee) {
	basisUSD, ok := s.priceUSD(row)
	if !ok {
		s.recordFailure(ctx, row, "no USD price for "+row.TokenSymbol)
		return
	}

	feeUSD := basisUSD * float64(s.conservativeBps()) / 10000.0
	if feeUSD <= 0 {
		s.recordFailure(ctx, row, "priced basis produced a non-positive fee")
		return
	}

	// Same key the settle endpoint uses, so the two paths cannot both charge.
	s.accrue()(services.FeeEvent{
		Feature:    row.Feature,
		Token:      row.TokenSymbol,
		FeeUSD:     feeUSD,
		UserID:     row.UserID,
		SourceType: "allswap",
		SourceID:   uuid.NewSHA1(uuid.NameSpaceURL, []byte("allswap:"+row.TxHash)),
	})

	now := time.Now()
	if err := s.DB.WithContext(ctx).Model(row).Updates(map[string]interface{}{
		"status": models.PendingSwapFeeSettled, "settled_at": now, "attempts": row.Attempts + 1,
	}).Error; err != nil {
		log.Printf("[fees] swept swap %s but failed to mark it settled: %v", row.TxHash, err)
		return
	}
	log.Printf("[fees] swap_fee_swept_total=1 tx=%s feature=%s basis=$%.2f fee=$%.6f "+
		"(client never settled; conservative %d bps)", row.TxHash, row.Feature, basisUSD, feeUSD, s.conservativeBps())
}

func (s *SwapFeeSweeper) conservativeBps() int {
	if s.ConservativeBps <= 0 {
		return 350
	}
	return s.ConservativeBps
}

// priceUSD values the swap from our own price service. There is no quote to
// re-check here, so an unpriceable token yields no fee rather than a guess.
func (s *SwapFeeSweeper) priceUSD(row *models.PendingSwapFee) (float64, bool) {
	if s.PriceService == nil || row.TokenSymbol == "" || row.AmountHuman <= 0 {
		return 0, false
	}
	prices, err := s.PriceService.GetPrices([]string{row.TokenSymbol})
	if err != nil {
		return 0, false
	}
	p, ok := prices[row.TokenSymbol]
	if !ok || p.USD <= 0 || math.IsNaN(p.USD) || math.IsInf(p.USD, 0) {
		return 0, false
	}
	return row.AmountHuman * p.USD, true
}

func (s *SwapFeeSweeper) recordFailure(ctx context.Context, row *models.PendingSwapFee, reason string) {
	attempts := row.Attempts + 1
	updates := map[string]interface{}{"attempts": attempts, "last_err": reason}
	if attempts >= s.maxAttempts() {
		updates["status"] = models.PendingSwapFeeAbandoned
		log.Printf("[fees] ALERT swap_fee_abandoned_total=1 tx=%s user=%s token=%s amount=%v — "+
			"%s after %d attempts; NO fee accrued, reconcile manually",
			row.TxHash, row.UserID, row.TokenSymbol, row.AmountHuman, reason, attempts)
	}
	if err := s.DB.WithContext(ctx).Model(row).Updates(updates).Error; err != nil {
		log.Printf("[fees] failed to record sweep failure for %s: %v", row.TxHash, err)
	}
}
