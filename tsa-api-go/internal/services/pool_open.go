package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// Open timing (spec S12, M3; plan C1 P9).
const (
	// PoolOpenCap is the whole pool attempt. ≥ 12 s of the router's 20 s
	// OpenBudget remain; the failover re-quote is bounded by ProviderTimeout
	// (8 s, usually under 1 s), so even a worst-case re-quote leaves ≥ 4 s for
	// the next venue's open (review M8: the re-quote is counted, not ignored).
	PoolOpenCap       = 8 * time.Second
	poolProveWindow   = 6 * time.Second
	poolProvePoll     = 300 * time.Millisecond
	poolProveDepth    = 2
	poolSliceWindow   = 60 * time.Minute // the pool escrow's paymentWindow (S3)
	poolRepickMinLeft = 2 * time.Second
	// poolHonourSafety is the room kept between the last moment an honoured
	// payment can be claimed (lock TTL + honour grace) and the escrow's
	// payment window, after which anyone may cancelByTimeout a slice (review
	// I4). Task 13's expiryPendingMargin (5 min) sits inside it.
	poolHonourSafety = 10 * time.Minute
)

// The failed-open brake (pre-merge review I-1): poolOpenFailLimit failed opens
// in a row that each sent at least one slice, inside poolOpenFailWindow, close
// the pool's gates. Code constants, not settings (decision 17).
const (
	poolOpenFailLimit  = 3
	poolOpenFailWindow = 10 * time.Minute
)

var errPoolFailSkipped = errors.New("liquidity pool: fail skipped, the order is not opening")

// poolOpenBreaker counts consecutive failed opens that sent slices. Each one
// locks LP stock for the slice window and spends treasury gas, so a run of
// them (an RPC slowdown past PoolOpenCap) would drain the pool's free stock
// with nothing to show. Its zero value is ready; safe for concurrent opens.
type poolOpenBreaker struct {
	mu    sync.Mutex
	fails []time.Time
}

// record notes one open's outcome and reports whether the brake trips. An
// opened order clears the count; a failure that sent nothing neither counts
// nor clears it.
func (b *poolOpenBreaker) record(opened bool, sent int, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if opened {
		b.fails = nil
		return false
	}
	if sent == 0 {
		return false
	}
	kept := b.fails[:0]
	for _, at := range b.fails {
		if now.Sub(at) <= poolOpenFailWindow {
			kept = append(kept, at)
		}
	}
	b.fails = append(kept, now)
	if len(b.fails) < poolOpenFailLimit {
		return false
	}
	b.fails = nil
	return true
}

// noteOpenOutcome feeds the brake. Tripped, it closes the gates through the
// same latch as a fee-snapshot breach: closed until a restart re-runs every
// gate (PoolChainGates.Close).
func (d *PoolDesk) noteOpenOutcome(opened bool, sent int, now time.Time) {
	if !d.breaker.record(opened, sent, now) {
		return
	}
	log.Printf("[liquidity] ALERT liquidity_pool_open_failing failed_opens=%d window=%s — %d opens in a row sent slices and failed (slow RPC?); the pool is closed until a restart",
		poolOpenFailLimit, poolOpenFailWindow, poolOpenFailLimit)
	d.c.Gates.Close(fmt.Sprintf("%d failed opens that sent slices within %s", poolOpenFailLimit, poolOpenFailWindow))
}

// PoolHonourFits (review I4): IT_STABLE_RATE_LOCK_SECONDS and
// RAMP_HONOUR_GRACE_SECONDS are env values. If they grow until an honoured
// payment could be claimed after the slices became cancellable on-chain, a
// paid order could have no stock behind it. Then pool orders refuse to open.
func (s *ITService) PoolHonourFits() error {
	need := s.stableLockTTL() + s.rampTerms().honourGrace + poolHonourSafety
	if need >= poolSliceWindow {
		return fmt.Errorf("%w: rate lock %s + honour grace %s + %s safety reaches the %s slice window",
			ErrPoolClosed, s.stableLockTTL(), s.rampTerms().honourGrace, poolHonourSafety, poolSliceWindow)
	}
	return nil
}

// ErrPoolDailyCapReached: pool_user_daily_max for this user (§10.4); the
// router fails over to another venue.
var ErrPoolDailyCapReached = errors.New("liquidity pool: daily purchase limit reached")

// scopePoolBuys is the pool's it_intents rows (poolProviders: the P7 lint).
func scopePoolBuys(db *gorm.DB) *gorm.DB {
	return db.Model(&models.ITIntent{}).Where("provider IN ?", poolProviders())
}

// poolFailedOpenSent (pre-merge review I-1) is a failed open that sent at
// least one slice: a slice with its open nonce recorded (written just before
// the broadcast) that was not reclaimed as provably never sent.
const poolFailedOpenSent = `status = ? AND failure_reason = ? AND EXISTS (SELECT 1 FROM p2p_trades t
	WHERE t.it_intent_id = it_intents.id AND t.open_nonce IS NOT NULL AND COALESCE(t.cancel_reason, '') <> ?)`

// CheckPoolDailyCap refuses when units would take her past pool_user_daily_max.
// A failed open that sent slices counts: it locked LP stock for the slice
// window as a live order does, so retrying from the card cannot drain the pool.
func (s *ITService) CheckPoolDailyCap(ctx context.Context, userID uuid.UUID, units *big.Int, set PoolSettings) error {
	return s.checkDailyBuyCap(ctx, s.db, userID, units, uuid.Nil, dailyBuyCap{
		scope: scopePoolBuys, limit: set.UserDailyMaxUnits, reached: ErrPoolDailyCapReached, metric: "ramp_pool_daily_cap_hit",
		logTag: "liquidity", who: "liquidity pool",
		alsoCounts:     poolFailedOpenSent,
		alsoCountsArgs: []any{models.ITStatusCancelled, models.ITFailurePoolOpenFailed, models.ITFailurePoolOpenFailed},
	})
}

// OpenPoolBuy opens a direct pool order: USDT on the pool chain to her own EVM
// wallet (spec §12.2).
func (s *ITService) OpenPoolBuy(ctx context.Context, userID uuid.UUID, p *PoolPrice, destAddress string) (*models.ITIntent, *StableBuyInstructions, error) {
	if d := s.poolDesk; d != nil {
		// N1, as the company wallet; parsed, never string-compared (review M-1).
		if s.wallet.IsConversionTreasury(destAddress) || SameEVMAddress(destAddress, s.converted.TreasuryAddress) ||
			SameEVMAddress(destAddress, d.c.Treasury.Address()) {
			return nil, nil, fmt.Errorf("%w: that address cannot receive a purchase", ErrBadDeliveryAddress)
		}
	}
	return s.openPoolBuy(ctx, userID, p, destAddress, nil)
}

// openPoolBuy is §12.2's ordered steps. Every slice is locked on-chain and
// proven before the intent becomes claimable and the pay-in is returned.
func (s *ITService) openPoolBuy(ctx context.Context, userID uuid.UUID, p *PoolPrice, destAddress string,
	also func(tx *gorm.DB, intent *models.ITIntent) error) (*models.ITIntent, *StableBuyInstructions, error) {
	d := s.poolDesk
	if d == nil || p == nil || p.Quote == nil {
		return nil, nil, ErrPoolClosed
	}
	started := d.now()
	// 1. Guards (the company wallet's order, plus the pool's own).
	if p.Settings.OrdersStopped {
		return nil, nil, ErrPoolOrdersStopped
	}
	if err := s.PoolHonourFits(); err != nil {
		log.Printf("[liquidity] ALERT liquidity_pool_honour_window_unsafe %s", err)
		return nil, nil, err
	}
	if !d.c.Gates.Open() || !d.c.Gates.ReleaseGasOK(ctx, p.Settings.SliceCap) {
		return nil, nil, ErrPoolClosed
	}
	if err := d.c.Nonces.Check(ctx); err != nil {
		return nil, nil, err // ErrTreasuryNonceStuck, or an unreadable nonce: closed either way
	}
	if !d.c.Treasury.CanPayForGas(ctx, d.ChainKey(), treasuryEscrowCallGas*uint64(p.Settings.SliceCap+1)) {
		return nil, nil, fmt.Errorf("%w: the treasury cannot pay to open slices", ErrPoolClosed)
	}
	if err := s.stableBuyPrechecks(ctx, userID, p.Quote, false); err != nil {
		return nil, nil, err
	}
	if err := s.CheckPoolDailyCap(ctx, userID, p.Quote.AssetUnits, p.Settings); err != nil {
		return nil, nil, err
	}
	// 2. One transaction under the pool lock: allocate, intent, snapshot, slices.
	var written []models.P2PTrade
	intent, payInto, err := s.openStableBuy(ctx, userID, p.Quote, d.ChainKey(), destAddress, stableBuyOpen{
		provider: models.ITProviderPool, routed: true, status: models.ITStatusOpening, deferHonour: true,
		minUnits: p.Settings.MinSliceUnits, maxUnits: p.Settings.OrderMaxUnits,
		validate: func(_, _, addr string) error {
			if !common.IsHexAddress(addr) {
				return fmt.Errorf("%w: delivery needs an EVM address", ErrBadDeliveryAddress)
			}
			return nil
		},
		insert: func(ctx context.Context, i *models.ITIntent) error {
			return d.insertOrderAt(ctx, i, p, also, &written, &p)
		},
	})
	if err != nil {
		return nil, nil, err
	}
	// 3–5. Broadcast, prove, re-pick once per failed slice.
	opens, repicks, feeBroken := d.openAndProve(ctx, intent, p, written, nil)
	// 6/7. Success or failure, on a context that survives the 8 s cap.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	outcome, reason := "opened", ""
	switch {
	case feeBroken:
		d.c.Gates.Close("a pool slice snapshotted a non-zero fee")
		log.Printf("[liquidity] ALERT liquidity_pool_fee_invariant_broken intent=%s — pool closed until a restart", intent.ID)
		outcome, reason = "failed", "fee_snapshot"
	case !allProven(opens, intent.AssetAmount, d):
		outcome, reason = "failed", "unproven_or_short"
	}
	if outcome == "opened" {
		if err := d.succeed(wctx, s, intent, opens); err != nil {
			outcome, reason = "failed", "succeed_cas"
		}
	}
	log.Printf("[liquidity] METRIC ramp_pool_open outcome=%s slices=%d repicks=%d reason=%q latency_ms=%d",
		outcome, len(opens), repicks, reason, d.now().Sub(started).Milliseconds())
	d.noteOpenOutcome(outcome == "opened", sentCount(opens), d.now())
	if outcome != "opened" {
		d.fail(wctx, intent, opens)
		return nil, nil, fmt.Errorf("%w (%s)", ErrPoolCannotFill, reason)
	}
	for _, o := range opens {
		d.c.Stock.Forget(d.c.Token.ChainID, o.ad.SellerAddress, d.c.Token.Symbol) // the next read is fresh
	}
	return intent, payInto, nil
}

// insertOrder is §12.2 step 2 under pool:<chain>:<TOKEN>: allocate (cached →
// fresh → once more), price the order on that allocation (fitPlanPrice), then
// the intent, its snapshot and one slice per allocation, each debiting its
// stock ad with the guarded CAS — all or nothing. A converted order (also
// set) keeps its quoted amount: its conversion record was priced on it.
func (d *PoolDesk) insertOrder(ctx context.Context, intent *models.ITIntent, p *PoolPrice,
	also func(tx *gorm.DB, intent *models.ITIntent) error, out *[]models.P2PTrade) error {
	return d.insertOrderAt(ctx, intent, p, also, out, nil)
}

// insertOrderAt is insertOrder that hands back, in priced, the price the order
// was opened at (p itself is never changed: a caller may share it).
func (d *PoolDesk) insertOrderAt(ctx context.Context, intent *models.ITIntent, p *PoolPrice,
	also func(tx *gorm.DB, intent *models.ITIntent) error, out *[]models.P2PTrade, priced **PoolPrice) error {
	var closeAfter []models.P2PAd
	defer func() { d.closeCancelledAds(ctx, closeAfter) }() // F23, after the lock's transaction ends (review C2)
	var opened *PoolPrice
	err := withAdvisoryLock(ctx, d.c.DB, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		plan, cancelled, err := d.plan(ctx, tx, p.Quote.AssetUnits, p.Settings, nil)
		closeAfter = cancelled
		if err != nil {
			return err
		}
		converted := also != nil
		fp, plan, more, err := fitPlanPrice(p, plan, planFit{allowWorse: !converted, allowBetter: !converted,
			replan: func(u *big.Int) ([]PoolSlicePlan, []models.P2PAd, error) { return d.plan(ctx, tx, u, p.Settings, nil) }})
		closeAfter = append(closeAfter, more...)
		if err != nil {
			return err
		}
		setIntentQuote(intent, fp.Quote)
		intent.Asset, intent.DestChain = strings.ToUpper(intent.Asset), strings.ToUpper(intent.DestChain)
		if err := tx.Create(intent).Error; err != nil {
			return err
		}
		if err := tx.Create(d.snapshot(intent.ID, 1, fp, plan)).Error; err != nil {
			return err
		}
		slices, err := d.writeSlices(tx, intent.ID, 1, fp, plan)
		if err != nil {
			return err
		}
		*out = slices
		opened = fp
		if also != nil {
			return also(tx, intent)
		}
		return nil
	})
	if err == nil && opened != nil && priced != nil {
		*priced = opened
	}
	return err
}

// setIntentQuote writes q's price columns onto an intent not yet saved (the
// fields openStableBuy fills from a quote).
func setIntentQuote(intent *models.ITIntent, q *StableBuyQuote) {
	intent.AssetAmount = q.AssetUnits.String()
	intent.LockedRate, intent.RateKobo, intent.RateMinor = q.RatePerUnitMinor.String(), q.RatePerUnitMinor.String(), q.RatePerUnitMinor.String()
	if q.DisplayRateMinor != nil {
		intent.DisplayRate = q.DisplayRateMinor.String()
	}
	intent.Fees = marshalFees(q.Fees)
}

// planFit is how fitPlanPrice may move an order's amount.
type planFit struct {
	fixed       []*big.Int // slices already at the treasury (a re-delivery's releases)
	allowWorse  bool       // her amount may shrink (past p.Guard it is refused)
	allowBetter bool       // her amount may grow
	replan      func(units *big.Int) ([]PoolSlicePlan, []models.P2PAd, error)
}

// fitPlanPrice makes the ACTUAL allocation decide the price, so the slices
// locked are exactly what she is delivered and every per-slice cost is one
// the order really has. plan is the allocation for what p leaves her (less
// any fixed slices); p1 is the price on fixed + plan.
//   - p1 = p: the price stands.
//   - p1 < p (more or dearer slices than p was costed on): worse for her.
//     Refused when not allowed (a converted open's amount is pinned by its
//     record: can't-fill, failover) or past her price guard (price-moved:
//     she re-confirms). Else the largest planned slice gives up the
//     difference and she gets p1 — its cost lines are the unshrunk plan's,
//     never below the shrunk plan's (shrinking only lowers a payout tier).
//   - p1 > p (fewer or cheaper slices): better. When allowed, a fresh plan
//     for p1 is tried; when it costs no more than plan she gets p1 on it
//     (lines from plan: ≥ its cost). Otherwise she gets p on plan (p's
//     lines: ≥ plan's cost).
func fitPlanPrice(p *PoolPrice, plan []PoolSlicePlan, f planFit) (*PoolPrice, []PoolSlicePlan, []models.P2PAd, error) {
	fixedSum := sumUnits(f.fixed)
	withFixed := func(pl []PoolSlicePlan) []*big.Int {
		return append(append([]*big.Int(nil), f.fixed...), planUnits(pl)...)
	}
	n0 := p.Quote.AssetUnits
	p1, err := p.repriceOn(withFixed(plan))
	if err != nil {
		return nil, nil, nil, err
	}
	switch c := p1.Quote.AssetUnits.Cmp(n0); {
	case c == 0:
		return p1, plan, nil, nil
	case c < 0:
		if !f.allowWorse {
			return nil, nil, nil, fmt.Errorf("%w: its slices cost more than priced", ErrPoolCannotFill)
		}
		if err := CheckPriceGuard(models.ITProviderPool, "buy", FormatMinor(p1.Quote.RatePerUnitMinor.String()), p.Guard.Limit, p.Guard.Mode); err != nil {
			return nil, nil, nil, err
		}
		shrunk, ok := shrinkPlan(plan, new(big.Int).Sub(n0, p1.Quote.AssetUnits), p.Settings.MinSliceUnits)
		if !ok {
			return nil, nil, nil, fmt.Errorf("%w: its slices cannot give up their extra cost", ErrPoolCannotFill)
		}
		p1.SliceUnits = withFixed(shrunk)
		return p1, shrunk, nil, nil
	default:
		kept := *p
		kept.SliceUnits = withFixed(plan)
		if !f.allowBetter || f.replan == nil {
			return &kept, plan, nil, nil
		}
		plan2, cancelled, err := f.replan(new(big.Int).Sub(p1.Quote.AssetUnits, fixedSum))
		if err != nil {
			return &kept, plan, cancelled, nil
		}
		p2, err := p.repriceOn(withFixed(plan2))
		if err != nil || p2.Quote.AssetUnits.Cmp(p1.Quote.AssetUnits) < 0 {
			return &kept, plan, cancelled, nil
		}
		p1.SliceUnits = withFixed(plan2)
		return p1, plan2, cancelled, nil
	}
}

func planUnits(plan []PoolSlicePlan) []*big.Int {
	out := make([]*big.Int, len(plan))
	for i := range plan {
		out[i] = plan[i].Units
	}
	return out
}

// shrinkPlan takes diff off the largest slice, never below minSlice.
func shrinkPlan(plan []PoolSlicePlan, diff, minSlice *big.Int) ([]PoolSlicePlan, bool) {
	if len(plan) == 0 {
		return nil, false
	}
	at := 0
	for i := range plan {
		if plan[i].Units.Cmp(plan[at].Units) > 0 {
			at = i
		}
	}
	left := new(big.Int).Sub(plan[at].Units, diff)
	if left.Cmp(minSlice) < 0 {
		return nil, false
	}
	out := append([]PoolSlicePlan(nil), plan...)
	out[at].Units = left
	return out, true
}

func (d *PoolDesk) snapshot(intentID uuid.UUID, gen int, p *PoolPrice, plan []PoolSlicePlan) *models.PoolOrder {
	opened := new(big.Int)
	for _, pl := range plan {
		opened.Add(opened, pl.Units)
	}
	return &models.PoolOrder{IntentID: intentID, Generation: gen, ChainID: d.c.Token.ChainID, Token: d.c.Token.Symbol,
		TokenDecimals: d.c.Token.Decimals, MidMinor: p.MidMinor.String(), PoolSpreadBps: int(p.Settings.SpreadBps),
		PoolTakeBps: int(p.Settings.TakeBps), FundingBps: int(p.FundingBps), BandBps: int(p.Settings.BandBps),
		LPRateMinor: p.LPRateMinor.String(), DisplayRateMinor: p.Quote.DisplayRateMinor.String(),
		SliceCount: len(plan), OpenedUnits: opened.String(), SurplusUnits: "0"}
}

// writeSlices inserts one p2p_trades row per plan entry (S9, S10): buyer the
// company PM user, opener NULL, tradeId = HashID(slice id), Rate = LP rate,
// FiatAmount = the LP's naira, TokenAmount in chain units.
func (d *PoolDesk) writeSlices(tx *gorm.DB, intentID uuid.UUID, gen int, p *PoolPrice, plan []PoolSlicePlan) ([]models.P2PTrade, error) {
	now := d.now()
	var out []models.P2PTrade
	for _, pl := range plan {
		chainUnits, err := CanonicalToChainUnits(pl.Units, d.c.Token.Decimals)
		if err != nil {
			return nil, err
		}
		res := tx.Model(&models.P2PAd{}).
			Where("id = ? AND status = ? AND CAST(remaining AS NUMERIC) >= CAST(? AS NUMERIC)", pl.Ad.ID, models.P2PAdStatusActive, chainUnits.String()).
			Updates(map[string]any{
				"remaining":  gorm.Expr("CAST(remaining AS NUMERIC) - CAST(? AS NUMERIC)", chainUnits.String()),
				"reserved":   gorm.Expr("CAST(reserved AS NUMERIC) + CAST(? AS NUMERIC)", chainUnits.String()),
				"updated_at": now,
			})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 0 {
			return nil, ErrPoolCannotFill // a concurrent open won the capacity
		}
		id := intentID
		tr := models.P2PTrade{ID: uuid.New(), AdID: pl.Ad.ID, BuyerID: d.c.BuyerUserID, SellerID: pl.Ad.SellerID,
			Token: d.c.Token.Symbol, ChainID: d.c.Token.ChainID, Funding: models.P2PTradeFundingPooled,
			TokenAmount: chainUnits.String(), FiatAmount: FormatMinor(PoolLPPayoutKobo(pl.Units, p.LPRateMinor).String()),
			FiatCurrency: "NGN", Rate: FormatMinor(p.LPRateMinor.String()), PaymentMethodID: uuid.Nil,
			Status: models.P2PTradeStatusPendingPayment, ExpiresAt: now.Add(poolSliceWindow),
			ITIntentID: &id, Generation: gen, CreatedAt: now, UpdatedAt: now}
		tr.OnChainTradeID = HashIDHex(tr.ID)
		if err := tx.Create(&tr).Error; err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, nil
}

// sliceOpen is one slice's open attempt.
type sliceOpen struct {
	slice  models.P2PTrade
	ad     models.P2PAd
	units  *big.Int // chain units
	hash   string
	sent   bool // a broadcast was attempted (its fate is the chain's)
	failed bool // provably never broadcast
	proven bool
	repick bool // already re-picked once
}

// openAndProve broadcasts every slice, proves them, and re-picks once per
// slice that provably never broadcast while ≥ 2 s remain (§12.2 steps 3–5).
// exclude names LPs a re-pick must never choose beyond those already in
// this burst (a re-delivery's S41 set: every LP holding a live or released
// slice of the order, earlier generations included — review m-3).
func (d *PoolDesk) openAndProve(ctx context.Context, intent *models.ITIntent, p *PoolPrice, written []models.P2PTrade, exclude map[uuid.UUID]bool) ([]*sliceOpen, int, bool) {
	opens := d.opensFor(ctx, written)
	d.broadcast(ctx, opens)
	deadline := d.now().Add(d.proveWindow)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	repicks := 0
	for _, o := range opens {
		if !o.failed || o.repick {
			continue
		}
		o.repick = true
		d.reclaim(ctx, o)
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < poolRepickMinLeft {
			continue
		}
		more, err := d.repick(ctx, intent, p, o, opens, exclude)
		if err != nil {
			log.Printf("[liquidity] re-pick for intent=%s failed: %s", intent.ID, RedactAlertText(err.Error()))
			continue
		}
		repicks++
		d.broadcast(ctx, more)
		opens = append(opens, more...)
	}
	return opens, repicks, d.prove(ctx, opens, deadline)
}

func (d *PoolDesk) opensFor(ctx context.Context, slices []models.P2PTrade) []*sliceOpen {
	var out []*sliceOpen
	for _, sl := range slices {
		var ad models.P2PAd
		units, _ := new(big.Int).SetString(sl.TokenAmount, 10)
		o := &sliceOpen{slice: sl, units: units}
		if err := d.c.DB.WithContext(ctx).First(&ad, "id = ?", sl.AdID).Error; err != nil {
			o.failed = true
		}
		o.ad = ad
		out = append(out, o)
	}
	return out
}

// broadcast opens each slice from the conversion treasury through one
// EscrowBurst: EscrowCalls enforced, nonce written on the slice before
// broadcast (once: a second attempt finds it set and sends nothing), and the
// whole burst on one RPC endpoint generation (S43). Pre-merge review I-1: the
// reads come first — the burst's gas once, every slice's open prepared
// (gas price, estimate) in parallel — so each send under the lane lock is
// only its nonce read, record and broadcast.
func (d *PoolDesk) broadcast(ctx context.Context, opens []*sliceOpen) {
	var todo []*sliceOpen
	for _, o := range opens {
		if !o.failed && !o.sent {
			todo = append(todo, o)
		}
	}
	if len(todo) == 0 {
		return
	}
	burst, err := d.c.Treasury.StartEscrowBurst(ctx, d.c.Treasury.Lane(d.ChainKey()), d.c.EscrowAddr, len(todo))
	if err != nil {
		for _, o := range todo {
			o.failed = true
		}
		log.Printf("[liquidity] burst of %d opens not sent: %s", len(todo), RedactAlertText(err.Error()))
		return
	}
	gen := d.c.Generation()
	prepared := d.prepareOpens(todo)
	treasury := common.HexToAddress(d.c.Treasury.Address())
	for i, o := range todo {
		if d.c.Generation() != gen {
			o.failed = true
			log.Printf("[liquidity] METRIC ramp_pool_open_burst_aborted slice=%s reason=endpoint_changed", o.slice.ID)
			continue
		}
		pre := prepared[i]
		if pre.err != nil {
			o.failed = true
			log.Printf("[liquidity] slice=%s open not sent: %s", o.slice.ID, RedactAlertText(pre.err.Error()))
			continue
		}
		hash, attempted, err := burst.Call(ctx, func() ([]byte, error) {
			// Under the lane lock, per slice: the chain's pending nonce, which
			// the lane's floor then raises past a slice the RPC has not seen.
			n, err := d.c.Probe.PendingNonceAt(ctx, treasury)
			if err != nil {
				return nil, fmt.Errorf("read the treasury nonce: %w", err)
			}
			u := pre.tx
			u.Nonce = n
			return json.Marshal(u)
		}, func(nonce uint64) error {
			res := d.c.DB.WithContext(ctx).Model(&models.P2PTrade{}).
				Where("id = ? AND open_nonce IS NULL", o.slice.ID).Update("open_nonce", int64(nonce))
			if res.Error == nil && res.RowsAffected != 1 {
				return errors.New("slice already carries an open nonce")
			}
			return res.Error
		})
		o.sent, o.hash = attempted, hash
		if err != nil && !attempted {
			o.failed = true
			log.Printf("[liquidity] slice=%s open not sent: %s", o.slice.ID, RedactAlertText(err.Error()))
		}
		if hash != "" {
			// Detached: the hash must land even when the 8 s cap has fired. A
			// lost write is reconciled by the recorded nonce (S44), never forgotten.
			hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			if err := d.c.DB.WithContext(hctx).Model(&models.P2PTrade{}).Where("id = ?", o.slice.ID).Update("open_tx_hash", hash).Error; err != nil {
				log.Printf("[liquidity] ALERT liquidity_pool_open_hash_unrecorded slice=%s nonce_recorded=true err=%s",
					o.slice.ID, RedactAlertText(err.Error()))
			}
			cancel()
		}
	}
}

// preparedOpen is one slice's open, prepared ahead of the burst's sends.
type preparedOpen struct {
	tx  blockchain.UnsignedTx
	err error
}

// prepareOpens builds every slice's openTrade in parallel, outside the lane
// lock: its voucher, gas price and estimate (an estimate that reverts is a
// slice provably never sent). The nonce each one read is replaced at send.
func (d *PoolDesk) prepareOpens(opens []*sliceOpen) []preparedOpen {
	out := make([]preparedOpen, len(opens))
	var wg sync.WaitGroup
	for i, o := range opens {
		voucher, verr := d.c.Trades.adVoucher(&o.ad, o.ad.SellerAddress)
		sig, serr := decodeHexSig(o.ad.VoucherSignature)
		if verr != nil || serr != nil || o.units == nil {
			out[i].err = errors.New("unusable voucher or amount")
			continue
		}
		wg.Add(1)
		go func(i int, o *sliceOpen) {
			defer wg.Done()
			raw, err := d.c.Escrow.PrepareOpenTradeWithVoucher(HashID(o.slice.ID), voucher, sig, d.c.Treasury.Address(), o.units)
			if err == nil {
				err = json.Unmarshal(raw, &out[i].tx)
			}
			out[i].err = err
		}(i, o)
	}
	wg.Wait()
	return out
}

// prove polls each sent slice until it is Pending on-chain exactly as
// written, snapshots (0, 0) and its receipt is 2 blocks deep; reports a
// non-zero fee snapshot (F15).
func (d *PoolDesk) prove(ctx context.Context, opens []*sliceOpen, deadline time.Time) bool {
	for {
		waiting := false
		for _, o := range opens {
			if o.failed || o.proven || !o.sent {
				continue
			}
			ok, feeBroken := d.proveOne(ctx, o)
			if feeBroken {
				return true
			}
			if ok {
				o.proven = true
				at := d.now()
				d.c.DB.WithContext(context.WithoutCancel(ctx)).Model(&models.P2PTrade{}).Where("id = ?", o.slice.ID).Update("opened_at", at)
				continue
			}
			waiting = true
		}
		if !waiting || !d.now().Before(deadline) {
			return false
		}
		select {
		case <-time.After(poolProvePoll):
		case <-ctx.Done():
			return false
		}
	}
}

func (d *PoolDesk) proveOne(ctx context.Context, o *sliceOpen) (proven, feeBroken bool) {
	id := HashID(o.slice.ID)
	t, err := d.c.Escrow.ReadTrade(id)
	if err != nil || !t.Exists || t.Status != OnChainP2PStatusPending ||
		!strings.EqualFold(t.Seller.Hex(), o.ad.SellerAddress) || !strings.EqualFold(t.Buyer.Hex(), d.c.Treasury.Address()) ||
		!strings.EqualFold(t.Token.Hex(), d.c.Token.Address) || t.Amount == nil || t.Amount.Cmp(o.units) != 0 {
		return false, false
	}
	platform, gas, found := d.c.Escrow.ReadTradeFeeSnapshot(id)
	if !found {
		return false, false
	}
	if platform != 0 || gas != 0 {
		return false, true
	}
	if o.hash == "" {
		return false, false // depth cannot be proven without the hash; the canceller decides by ReadTrade (S44)
	}
	r, err := d.c.Probe.TransactionReceipt(ctx, common.HexToHash(o.hash))
	if err != nil || r == nil || r.Status != 1 || r.BlockNumber == nil {
		return false, false
	}
	head, err := d.c.Probe.BlockNumber(ctx)
	return err == nil && head+1 >= r.BlockNumber.Uint64()+poolProveDepth, false
}

// reclaim cancels a provably-unbroadcast slice now and restores its ad (P9).
func (d *PoolDesk) reclaim(ctx context.Context, o *sliceOpen) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if _, err := d.c.Trades.reclaimUnopenedTrade(wctx, &o.slice, models.ITFailurePoolOpenFailed); err != nil {
		log.Printf("[liquidity] ALERT liquidity_pool_reclaim_failed slice=%s err=%s", o.slice.ID, RedactAlertText(err.Error()))
	}
}

// repick allocates the failed slice's units again, under the lock, excluding
// every LP already in the order (S41: this burst's, plus also), within the
// remaining slice budget.
func (d *PoolDesk) repick(ctx context.Context, intent *models.ITIntent, p *PoolPrice, failed *sliceOpen, opens []*sliceOpen, also map[uuid.UUID]bool) ([]*sliceOpen, error) {
	units, err := ChainToCanonicalUnits(failed.units, d.c.Token.Decimals)
	if err != nil {
		return nil, err
	}
	exclude := make(map[uuid.UUID]bool, len(also))
	for id := range also {
		exclude[id] = true
	}
	live := 0
	for _, o := range opens {
		if o.ad.PMID != nil {
			exclude[*o.ad.PMID] = true
		}
		if !o.failed {
			live++
		}
	}
	set := p.Settings
	set.SliceCap = p.Settings.SliceCap - live
	var written []models.P2PTrade
	var closeAfter []models.P2PAd
	err = withAdvisoryLock(ctx, d.c.DB, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		plan, cancelled, err := d.plan(ctx, tx, units, set, exclude)
		closeAfter = cancelled
		if err != nil {
			return err
		}
		written, err = d.writeSlices(tx, intent.ID, failed.slice.Generation, p, plan) // a re-delivery re-picks in its own generation
		return err
	})
	d.closeCancelledAds(ctx, closeAfter) // F23, after the lock's transaction ends (review C2)
	if err != nil {
		return nil, err
	}
	return d.opensFor(ctx, written), nil
}

// sentCount is how many slices left this process (their fate is the chain's).
func sentCount(opens []*sliceOpen) int {
	n := 0
	for _, o := range opens {
		if o.sent {
			n++
		}
	}
	return n
}

// allProven: every live slice is proven and they cover the order exactly.
func allProven(opens []*sliceOpen, assetAmount string, d *PoolDesk) bool {
	want, _ := new(big.Int).SetString(assetAmount, 10)
	got := new(big.Int)
	for _, o := range opens {
		if o.failed {
			continue
		}
		if !o.proven {
			return false
		}
		c, err := ChainToCanonicalUnits(o.units, d.c.Token.Decimals)
		if err != nil {
			return false
		}
		got.Add(got, c)
	}
	return want != nil && got.Cmp(want) == 0
}

// succeed is §12.2 step 6: CAS opening → pending_payment and start the honour
// window now; a converted record's validity follows it. Every proven slice's
// opened_at is written in the SAME transaction (review M5): S18's orderSlices
// counts only opened slices, so a lost best-effort write would park a
// covered late payment. prove's own write stays as an early hint for the
// allocator's unconfirmed window.
func (d *PoolDesk) succeed(ctx context.Context, s *ITService, intent *models.ITIntent, opens []*sliceOpen) error {
	until := d.now().Add(s.stableLockTTL())
	at := d.now()
	var proven []uuid.UUID
	for _, o := range opens {
		if o.proven && !o.failed {
			proven = append(proven, o.slice.ID)
		}
	}
	return d.c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.ITIntent{}).Where("id = ? AND status = ?", intent.ID, models.ITStatusOpening).
			Updates(map[string]any{"status": models.ITStatusPendingPayment, "rate_expires_at": until})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errors.New("liquidity pool: the order left opening before it succeeded")
		}
		if err := tx.Model(&models.P2PTrade{}).Where("id IN ? AND opened_at IS NULL", proven).Update("opened_at", at).Error; err != nil {
			return err
		}
		intent.Status, intent.RateExpiresAt = models.ITStatusPendingPayment, &until
		return tx.Model(&models.PaycrestOrderRecord{}).Where("reference = ?", ITConversionRef(intent.ID)).
			Update("valid_until", until).Error
	})
}

// fail is §12.2 step 7: CAS opening → cancelled (pool_open_failed; hidden;
// never an abandon), every slice of the order dropped so the canceller frees
// the ones that landed, and a converted record expired with it.
func (d *PoolDesk) fail(ctx context.Context, intent *models.ITIntent, opens []*sliceOpen) {
	now := d.now()
	err := d.c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.ITIntent{}).Where("id = ? AND status = ?", intent.ID, models.ITStatusOpening).
			Updates(map[string]any{"status": models.ITStatusCancelled, "failure_reason": models.ITFailurePoolOpenFailed})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			// Review M6: the order is no longer opening — succeed may have
			// committed and then reported an error. Its slices back a live
			// order: never drop them here.
			log.Printf("[liquidity] ALERT liquidity_pool_open_fail_skipped intent=%s — not opening any more; slices kept", intent.ID)
			return errPoolFailSkipped
		}
		if err := tx.Model(&models.P2PTrade{}).Where("it_intent_id = ? AND dropped_at IS NULL", intent.ID).
			Update("dropped_at", now).Error; err != nil {
			return err
		}
		return markConversion(ctx, tx, intent.ID, []string{models.PaycrestStatusAwaitingPayment},
			map[string]any{"status": models.PaycrestStatusExpired})
	})
	if err != nil && !errors.Is(err, errPoolFailSkipped) {
		log.Printf("[liquidity] ALERT liquidity_pool_open_fail_write intent=%s err=%s — the opening sweep will cancel it", intent.ID, RedactAlertText(err.Error()))
	}
	for _, o := range opens {
		if o.failed && !o.repick {
			d.reclaim(ctx, o)
		}
	}
}
