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

var errPoolFailSkipped = errors.New("liquidity pool: fail skipped, the order is not opening")

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

// CheckPoolDailyCap refuses when units would take her past pool_user_daily_max.
func (s *ITService) CheckPoolDailyCap(ctx context.Context, userID uuid.UUID, units *big.Int, set PoolSettings) error {
	return s.checkDailyBuyCap(ctx, s.db, userID, units, uuid.Nil, dailyBuyCap{
		scope: scopePoolBuys, limit: set.UserDailyMaxUnits, reached: ErrPoolDailyCapReached, metric: "ramp_pool_daily_cap_hit",
	})
}

// OpenPoolBuy opens a direct pool order: USDT on the pool chain to her own EVM
// wallet (spec §12.2).
func (s *ITService) OpenPoolBuy(ctx context.Context, userID uuid.UUID, p *PoolPrice, destAddress string) (*models.ITIntent, *StableBuyInstructions, error) {
	if d := s.poolDesk; d != nil {
		dest := strings.TrimSpace(destAddress)
		for _, own := range []string{s.converted.TreasuryAddress, d.c.Treasury.Address()} {
			if own != "" && strings.EqualFold(dest, own) {
				return nil, nil, fmt.Errorf("%w: that address cannot receive a purchase", ErrBadDeliveryAddress) // N1, as the company wallet
			}
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
	if !d.c.Gates.Open() || !d.c.Gates.ReleaseGasOK(ctx) {
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
		insert: func(ctx context.Context, i *models.ITIntent) error { return d.insertOrder(ctx, i, p, also, &written) },
	})
	if err != nil {
		return nil, nil, err
	}
	// 3–5. Broadcast, prove, re-pick once per failed slice.
	opens, repicks, feeBroken := d.openAndProve(ctx, intent, p, written)
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
// fresh → once more), then the intent, its snapshot and one slice per
// allocation, each debiting its stock ad with the guarded CAS — all or nothing.
func (d *PoolDesk) insertOrder(ctx context.Context, intent *models.ITIntent, p *PoolPrice,
	also func(tx *gorm.DB, intent *models.ITIntent) error, out *[]models.P2PTrade) error {
	var closeAfter []models.P2PAd
	defer func() { d.closeCancelledAds(ctx, closeAfter) }() // F23, after the lock's transaction ends (review C2)
	return withAdvisoryLock(ctx, d.c.DB, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		units, ok := new(big.Int).SetString(intent.AssetAmount, 10)
		if !ok {
			return fmt.Errorf("liquidity pool: unusable amount %q", intent.AssetAmount)
		}
		plan, cancelled, err := d.plan(ctx, tx, units, p.Settings, nil)
		closeAfter = cancelled
		if err != nil {
			return err
		}
		intent.Asset, intent.DestChain = strings.ToUpper(intent.Asset), strings.ToUpper(intent.DestChain)
		if err := tx.Create(intent).Error; err != nil {
			return err
		}
		if err := tx.Create(d.snapshot(intent.ID, 1, p, plan)).Error; err != nil {
			return err
		}
		slices, err := d.writeSlices(tx, intent.ID, 1, p, plan)
		if err != nil {
			return err
		}
		*out = slices
		if also != nil {
			return also(tx, intent)
		}
		return nil
	})
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
func (d *PoolDesk) openAndProve(ctx context.Context, intent *models.ITIntent, p *PoolPrice, written []models.P2PTrade) ([]*sliceOpen, int, bool) {
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
		more, err := d.repick(ctx, intent, p, o, opens)
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

// broadcast opens each slice from the conversion treasury through
// CallEscrowRecorded: EscrowCalls enforced, nonce written on the slice before
// broadcast (once: a second attempt finds it set and sends nothing), and the
// whole burst on one RPC endpoint generation (S43).
func (d *PoolDesk) broadcast(ctx context.Context, opens []*sliceOpen) {
	lane := d.c.Treasury.Lane(d.ChainKey())
	gen := d.c.Generation()
	for _, o := range opens {
		if o.failed || o.sent {
			continue
		}
		if d.c.Generation() != gen {
			o.failed = true
			log.Printf("[liquidity] METRIC ramp_pool_open_burst_aborted slice=%s reason=endpoint_changed", o.slice.ID)
			continue
		}
		voucher, verr := d.c.Trades.adVoucher(&o.ad, o.ad.SellerAddress)
		sig, serr := decodeHexSig(o.ad.VoucherSignature)
		if verr != nil || serr != nil || o.units == nil {
			o.failed = true
			continue
		}
		hash, attempted, err := d.c.Treasury.CallEscrowRecorded(ctx, lane, d.c.EscrowAddr, func() ([]byte, error) {
			return d.c.Escrow.PrepareOpenTradeWithVoucher(HashID(o.slice.ID), voucher, sig, d.c.Treasury.Address(), o.units)
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
// every LP already in the order (S41), within the remaining slice budget.
func (d *PoolDesk) repick(ctx context.Context, intent *models.ITIntent, p *PoolPrice, failed *sliceOpen, opens []*sliceOpen) ([]*sliceOpen, error) {
	units, err := ChainToCanonicalUnits(failed.units, d.c.Token.Decimals)
	if err != nil {
		return nil, err
	}
	exclude := map[uuid.UUID]bool{}
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
		written, err = d.writeSlices(tx, intent.ID, 1, p, plan)
		return err
	})
	d.closeCancelledAds(ctx, closeAfter) // F23, after the lock's transaction ends (review C2)
	if err != nil {
		return nil, err
	}
	return d.opensFor(ctx, written), nil
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
