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
	"gorm.io/gorm/clause"
)

// The shared-pool order path (spec 2026-10-14 §4): a pool order is the
// company-wallet sale with one step in front. Open reserves the vault's free
// collateral off-chain (nothing on-chain before she pays); once paid, the
// release key fills the order from the vault into the conversion treasury,
// the fill is proven on its own receipt, and the treasury delivers through
// the pool's sale desk exactly as for a slice order.

const (
	// vaultFillGas budgets one fill on the release key: the same as a slice
	// release, so ReleaseGasOK's per-call arithmetic holds (a fill measures
	// well under it).
	vaultFillGas = poolReleaseCallGas
	// poolFillStallAfter: a fill with no proof this long after the order was
	// claimed for delivery is alerted (the lane keeps re-sending meanwhile).
	poolFillStallAfter = 10 * time.Minute
)

// poolVaultReservedStatuses are the order states that may still lead to a
// fill: each reserves its fill + spread until the fill is proven.
var poolVaultReservedStatuses = []string{models.ITStatusPendingPayment, models.ITStatusPaymentReceived,
	models.ITStatusDelivering, models.ITStatusBuyAwaitingRate}

func (d *PoolDesk) vaultMode() bool { return d != nil && d.c.Vault != nil }

// VaultMode reports whether pool orders fill from the shared-pool vault.
func (s *ITService) PoolVaultMode() bool { return s.HasPool() && s.poolDesk.vaultMode() }

// vaultFillRef is the order's fill ref on-chain: HashID of the intent, so a
// replayed fill for the same order is refused by the contract itself.
func vaultFillRef(intentID uuid.UUID) [32]byte { return HashID(intentID) }

func vaultFillOp(intentID uuid.UUID) string { return "pool_fill:" + intentID.String() }

// vaultSpreadUnits is the LPs' share of an order: fill × pool_spread, floored
// (the buyer paid M × (1 + spread) per token; the spread is theirs).
func vaultSpreadUnits(fill *big.Int, spreadBps int64) *big.Int {
	n := new(big.Int).Mul(fill, big.NewInt(spreadBps))
	return n.Quo(n, big.NewInt(10_000))
}

// vaultReserved is Σ(fill + spread) of pool orders that may still fill and
// whose fill is not yet proven: what the vault's free collateral must keep
// back for them. Summed in Go: chain units overflow SQLite's numerics.
func (d *PoolDesk) vaultReserved(ctx context.Context, db *gorm.DB) (*big.Int, error) {
	var rows []models.PoolOrder
	err := db.WithContext(ctx).Model(&models.PoolOrder{}).
		Joins("JOIN it_intents ON it_intents.id = pool_orders.intent_id").
		Where("it_intents.provider IN ? AND it_intents.status IN ?", poolProviders(), poolVaultReservedStatuses).
		Where("pool_orders.filled_at IS NULL AND pool_orders.fill_units IS NOT NULL").
		Select("pool_orders.intent_id, pool_orders.generation, pool_orders.fill_units, pool_orders.spread_units").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	sum := new(big.Int)
	for _, r := range rows {
		sum.Add(sum, bigFromDecimal(r.FillUnits))
		sum.Add(sum, bigFromDecimal(r.SpreadUnits))
	}
	return sum, nil
}

func bigFromDecimal(p *string) *big.Int {
	if p == nil {
		return new(big.Int)
	}
	n, ok := new(big.Int).SetString(*p, 10)
	if !ok {
		return new(big.Int)
	}
	return n
}

// vaultFree is collateral − outstanding − reserved, in chain units.
func (d *PoolDesk) vaultFree(ctx context.Context, db *gorm.DB, st *VaultState) (*big.Int, error) {
	reserved, err := d.vaultReserved(ctx, db)
	if err != nil {
		return nil, err
	}
	free := new(big.Int).Sub(st.FreeCapacity(), reserved)
	if free.Sign() < 0 {
		free.SetInt64(0)
	}
	return free, nil
}

// vaultDeliverable is the largest order (canonical units) the vault can take
// now: free ÷ (1 + spread). Cached poolTotalsTTL — public paths only (quotes,
// the card); an open always re-reads under the pool lock.
func (d *PoolDesk) vaultDeliverable(ctx context.Context, set PoolSettings) (*big.Int, error) {
	free, err := d.vaultCache.Get("free", d.now(), func() (*big.Int, error) {
		st, err := d.c.Vault.State(ctx)
		if err != nil {
			d.vaultCache.Put("free", d.now(), nil, err)
			return nil, err
		}
		f := new(big.Int)
		if !st.Paused {
			if f, err = d.vaultFree(ctx, d.c.DB, st); err != nil {
				d.vaultCache.Put("free", d.now(), nil, err)
				return nil, err
			}
		}
		d.vaultCache.Put("free", d.now(), f, nil)
		return f, nil
	})
	if err != nil {
		return nil, err
	}
	units := new(big.Int).Mul(free, big.NewInt(10_000))
	units.Quo(units, big.NewInt(10_000+set.SpreadBps))
	return ChainToCanonicalUnits(units, d.c.Token.Decimals)
}

// ─── Open ───────────────────────────────────────────────────────────────────

// openVaultBuy is the shared pool's open: the guards, then one transaction
// under the pool lock that re-checks the vault's free collateral and writes
// the intent (pending_payment at once — nothing to lock on-chain) and its
// snapshot.
func (s *ITService) openVaultBuy(ctx context.Context, userID uuid.UUID, p *PoolPrice, destAddress string,
	also func(tx *gorm.DB, intent *models.ITIntent) error) (*models.ITIntent, *StableBuyInstructions, error) {
	d := s.poolDesk
	if p.Settings.OrdersStopped {
		return nil, nil, ErrPoolOrdersStopped
	}
	if !d.c.Gates.Open() || !s.poolReleaseGasOK(ctx, 1, true) {
		return nil, nil, ErrPoolClosed
	}
	if err := d.c.Nonces.Check(ctx); err != nil {
		return nil, nil, err
	}
	if _, err := d.c.TreasuryCommitments.UnitsFresh(ctx); err != nil {
		log.Printf("[liquidity] ALERT liquidity_pool_treasury_commitments_unreadable_total=1 err=%s", RedactAlertText(err.Error()))
		return nil, nil, fmt.Errorf("%w: the treasury's commitments cannot be read", ErrPoolClosed)
	}
	if !d.c.Treasury.CanPayForGasFresh(ctx, d.ChainKey(), treasuryERC20Gas) {
		return nil, nil, fmt.Errorf("%w: the treasury cannot pay to deliver", ErrPoolClosed)
	}
	if err := s.stableBuyPrechecks(ctx, userID, p.Quote, false); err != nil {
		return nil, nil, err
	}
	if err := s.CheckPoolDailyCap(ctx, userID, p.Quote.AssetUnits, p.Settings); err != nil {
		return nil, nil, err
	}
	fill, err := CanonicalToChainUnits(p.Quote.AssetUnits, d.c.Token.Decimals)
	if err != nil {
		return nil, nil, err
	}
	spread := vaultSpreadUnits(fill, p.Settings.SpreadBps)
	intent, payInto, err := s.openStableBuy(ctx, userID, p.Quote, d.ChainKey(), destAddress, stableBuyOpen{
		provider: models.ITProviderPool, routed: true,
		minUnits: p.Settings.MinSliceUnits, maxUnits: p.Settings.OrderMaxUnits,
		validate: func(_, _, addr string) error {
			if !common.IsHexAddress(addr) {
				return fmt.Errorf("%w: delivery needs an EVM address", ErrBadDeliveryAddress)
			}
			return nil
		},
		insert: func(ctx context.Context, i *models.ITIntent) error {
			return d.insertVaultOrder(ctx, i, p, fill, spread, also)
		},
	})
	if err != nil {
		return nil, nil, err
	}
	d.vaultCache.Forget("free")
	log.Printf("[liquidity] METRIC ramp_pool_open outcome=opened mode=vault intent=%s fill=%s spread=%s", intent.ID, fill, spread)
	return intent, payInto, nil
}

// insertVaultOrder: under the pool lock, the vault's state read now and every
// unfilled order's reservation counted, then the intent and its snapshot —
// all or nothing.
func (d *PoolDesk) insertVaultOrder(ctx context.Context, intent *models.ITIntent, p *PoolPrice, fill, spread *big.Int,
	also func(tx *gorm.DB, intent *models.ITIntent) error) error {
	return withAdvisoryLock(ctx, d.c.DB, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		rctx, cancel := context.WithTimeout(ctx, poolFreshReadTimeout)
		st, err := d.c.Vault.State(rctx)
		cancel()
		if err != nil {
			log.Printf("[liquidity] vault unreadable at open: %s", RedactAlertText(err.Error()))
			return fmt.Errorf("%w: the pool cannot be read", ErrPoolCannotFill)
		}
		if st.Paused {
			return fmt.Errorf("%w: the pool is paused", ErrPoolCannotFill)
		}
		free, err := d.vaultFree(ctx, tx, st)
		if err != nil {
			return err
		}
		need := new(big.Int).Add(fill, spread)
		if free.Cmp(need) < 0 {
			return fmt.Errorf("%w: the pool's free collateral is below this order", ErrPoolCannotFill)
		}
		intent.Asset, intent.DestChain = strings.ToUpper(intent.Asset), strings.ToUpper(intent.DestChain)
		if err := tx.Create(intent).Error; err != nil {
			return err
		}
		snap := d.snapshot(intent.ID, 1, p, []PoolSlicePlan{{Units: p.Quote.AssetUnits}})
		fs, ss := fill.String(), spread.String()
		snap.FillUnits, snap.SpreadUnits = &fs, &ss
		if err := tx.Create(snap).Error; err != nil {
			return err
		}
		if also != nil {
			return also(tx, intent)
		}
		return nil
	})
}

// ─── Settle ─────────────────────────────────────────────────────────────────

func (d *PoolDesk) vaultOrder(ctx context.Context, db *gorm.DB, intentID uuid.UUID) (*models.PoolOrder, error) {
	var o models.PoolOrder
	if err := db.WithContext(ctx).First(&o, "intent_id = ? AND generation = 1", intentID).Error; err != nil {
		return nil, err
	}
	if o.FillUnits == nil {
		return nil, fmt.Errorf("%w: pool order %s has no fill amount (a slice order?)", ErrITBadState, intentID)
	}
	return &o, nil
}

// advanceVaultDelivering drives a delivering vault order one step per tick:
// send the fill, prove it, then deliver (direct) or hand off (converted).
func (s *ITService) advanceVaultDelivering(ctx context.Context, d *PoolDesk, intent *models.ITIntent) error {
	o, err := d.vaultOrder(ctx, s.db, intent.ID)
	if err != nil {
		return err
	}
	if o.FilledAt == nil {
		if o.FillTxHash == "" {
			return s.sendVaultFill(ctx, d, intent)
		}
		proven, err := s.proveVaultFill(ctx, d, intent, o)
		if err != nil || !proven {
			return err
		}
		if o, err = d.vaultOrder(ctx, s.db, intent.ID); err != nil {
			return err
		}
	}
	rec, err := s.ConversionFor(ctx, intent)
	if err != nil {
		return err
	}
	if rec != nil {
		return s.handOffPoolOrder(ctx, intent, o.FillTxHash)
	}
	sd := s.poolSale()
	if err := s.sendSale(ctx, sd, intent.ID); err != nil {
		log.Printf("[liquidity] send intent=%s: %s", intent.ID, RedactAlertText(err.Error()))
	}
	return s.finalizeSale(ctx, sd, intent.ID)
}

// decideVaultFill: under the pool lock and the intent's row lock — the locks
// a refund takes — the order is still delivering and no fill is recorded.
func (s *ITService) decideVaultFill(ctx context.Context, d *PoolDesk, intentID uuid.UUID) (bool, error) {
	fill := false
	err := withAdvisoryLock(ctx, s.db, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		var locked models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "status").First(&locked, "id = ?", intentID).Error; err != nil {
			return err
		}
		if locked.Status != models.ITStatusDelivering {
			return nil
		}
		var o models.PoolOrder
		if err := tx.First(&o, "intent_id = ? AND generation = 1", intentID).Error; err != nil {
			return err
		}
		fill = o.FillTxHash == "" && o.FilledAt == nil
		return nil
	})
	return fill, err
}

// sendVaultFill sends the order's fill from the release key. The hash is
// written in the key lane's allocation transaction (the pre-broadcast hook),
// so a signed fill is never lost; the contract refuses a second fill of the
// same ref, so a fill is never paid twice.
func (s *ITService) sendVaultFill(ctx context.Context, d *PoolDesk, intent *models.ITIntent) error {
	ok, err := s.decideVaultFill(ctx, d, intent.ID)
	if err != nil || !ok {
		return err
	}
	o, err := d.vaultOrder(ctx, s.db, intent.ID)
	if err != nil {
		return err
	}
	ref := vaultFillRef(intent.ID)
	if used, err := d.c.Vault.FillUsed(ctx, ref); err != nil {
		return err
	} else if used {
		// Only our hook records a fill, in the same transaction as its
		// signing: a used ref with no hash here is not ours to repeat.
		log.Printf("[liquidity] ALERT liquidity_pool_fill_ref_used_unrecorded intent=%s — the vault already filled this ref; not sent", intent.ID)
		return fmt.Errorf("%w: the vault already filled intent %s", ErrITBadState, intent.ID)
	}
	st, err := d.c.Vault.State(ctx)
	if err != nil {
		return err
	}
	fill := bigFromDecimal(o.FillUnits)
	spread := bigFromDecimal(o.SpreadUnits)
	if st.TotalPrincipal.Sign() == 0 {
		spread = new(big.Int) // no LP to credit: the spread stays with the company (booked as take)
	}
	spreadStr := spread.String()
	sendCtx := WithPreBroadcast(ReservedSend(WithOperationRef(ctx, vaultFillOp(intent.ID))),
		func(hctx context.Context, _ uint64, hash string) error {
			res := LaneDB(hctx, s.db).Model(&models.PoolOrder{}).
				Where("intent_id = ? AND generation = 1 AND fill_tx_hash = '' AND filled_at IS NULL", intent.ID).
				Updates(map[string]any{"fill_tx_hash": hash, "fill_spread_units": spreadStr})
			if res.Error == nil && res.RowsAffected != 1 {
				return fmt.Errorf("pool order %s: a fill is already recorded", intent.ID)
			}
			return res.Error
		})
	treasury := d.c.Treasury.Address()
	hash, err := d.c.Sender.Send(sendCtx, d.c.Token.ChainID, func() ([]byte, error) {
		return d.c.Vault.PrepareFill(d.c.Sender.Address(), treasury, fill, spread, ref)
	})
	switch {
	case err == nil:
		log.Printf("[liquidity] METRIC liquidity_pool_fill_sent intent=%s tx=%s fill=%s spread=%s", intent.ID, hash, fill, spread)
		return nil
	case errors.Is(err, ErrTxNotBroadcast):
		// Nothing committed. A refusal the contract itself gives (estimate
		// reverted) is not a transient: park it for an operator.
		if reason := revertReasonOf(err); reason != "" {
			return s.parkVaultFill(ctx, d, intent.ID, "", "the vault refuses the fill: "+reason)
		}
		if time.Since(intent.UpdatedAt) > poolFillStallAfter {
			log.Printf("[liquidity] ALERT liquidity_pool_fill_stalled intent=%s err=%s", intent.ID, RedactAlertText(err.Error()))
		}
		return err
	default:
		// Attempted (in flight or uncertain): the hook recorded its hash; the
		// next tick proves it.
		log.Printf("[liquidity] fill intent=%s in flight: %s", intent.ID, RedactAlertText(err.Error()))
		return nil
	}
}

// revertReasonOf is the contract's own revert reason when the build refused
// on an estimate that reverted (the node's revert data), or "" for anything
// else — an RPC failure is a transient, never a park.
func revertReasonOf(err error) string { return decodeRevertReason(err) }

// proveVaultFill proves the recorded fill on its own receipt, following the
// release lane's speed-ups and cancels: mined, at sale depth, carrying the
// Filled event for this ref paying the treasury the order's fill.
func (s *ITService) proveVaultFill(ctx context.Context, d *PoolDesk, intent *models.ITIntent, o *models.PoolOrder) (bool, error) {
	hash := o.FillTxHash
	lane := laneOfSender(d.c.Sender, d.c.Token.ChainID)
	if lane != nil && lane.receipts != nil {
		mined, cancelled, err := lane.CallOutcome(ctx, hash, receiptExists(lane.receipts))
		if err != nil {
			return false, err
		}
		if cancelled {
			// Proven cancelled at depth: nothing of it ran. Give the operation
			// up (sequenced: the retry reuses the key) and clear the hash so the
			// next tick sends a new fill.
			if !lane.GiveUp(ctx, d.c.Sender.Address(), vaultFillOp(intent.ID), hash, RetireSequenced) {
				return false, nil
			}
			return false, s.clearVaultFill(ctx, intent.ID, hash, "cancelled")
		}
		hash = mined
	}
	r, err := d.c.Probe.TransactionReceipt(ctx, common.HexToHash(hash))
	if err != nil || r == nil || r.BlockNumber == nil {
		if time.Since(intent.UpdatedAt) > poolFillStallAfter {
			log.Printf("[liquidity] ALERT liquidity_pool_fill_stalled intent=%s tx=%s — no receipt", intent.ID, hash)
		}
		return false, nil
	}
	head, err := d.c.Probe.BlockNumber(ctx)
	if err != nil {
		return false, err
	}
	if head+1 < r.BlockNumber.Uint64()+s.saleDepth() {
		return false, nil // not deep enough yet
	}
	if r.Status != 1 {
		// Reverted at depth: final. Free the operation and park the order.
		if lane != nil && !lane.GiveUp(ctx, d.c.Sender.Address(), vaultFillOp(intent.ID), o.FillTxHash, RetireSequenced) {
			return false, nil
		}
		return false, s.parkVaultFill(ctx, d, intent.ID, o.FillTxHash, "the fill "+hash+" reverted")
	}
	ev := d.c.Vault.FilledIn(r, vaultFillRef(intent.ID))
	fill := bigFromDecimal(o.FillUnits)
	if ev == nil || !SameEVMAddress(ev.To, d.c.Treasury.Address()) || ev.Amount == nil || ev.Amount.Cmp(fill) != 0 {
		log.Printf("[liquidity] ALERT liquidity_pool_fill_mismatch intent=%s tx=%s — the receipt does not carry this order's fill", intent.ID, hash)
		return false, fmt.Errorf("%w: fill %s does not prove intent %s", ErrITBadState, hash, intent.ID)
	}
	now := d.now()
	res := s.db.WithContext(ctx).Model(&models.PoolOrder{}).
		Where("intent_id = ? AND generation = 1 AND filled_at IS NULL", intent.ID).
		Updates(map[string]any{"filled_at": now, "fill_tx_hash": hash, "fill_spread_units": ev.Spread.String(),
			"delivered_units": intent.AssetAmount})
	if res.Error != nil {
		return false, res.Error
	}
	d.vaultCache.Forget("free")
	log.Printf("[liquidity] METRIC liquidity_pool_fill_proven intent=%s tx=%s spread=%s", intent.ID, hash, ev.Spread)
	return true, nil
}

// clearVaultFill forgets a fill proven never to have run (cancelled), so the
// next tick sends a new one. Conditional on the hash: a newer write wins.
func (s *ITService) clearVaultFill(ctx context.Context, intentID uuid.UUID, hash, why string) error {
	res := s.db.WithContext(ctx).Model(&models.PoolOrder{}).
		Where("intent_id = ? AND generation = 1 AND fill_tx_hash = ? AND filled_at IS NULL", intentID, hash).
		Updates(map[string]any{"fill_tx_hash": "", "fill_spread_units": nil})
	if res.Error == nil && res.RowsAffected == 1 {
		log.Printf("[liquidity] fill intent=%s tx=%s %s — cleared, will be re-sent", intentID, hash, why)
	}
	return res.Error
}

// parkVaultFill moves a delivering order to buy_awaiting_rate with
// pool_fill_refused (reserve kept: retry or refund are the exits), clearing a
// reverted fill's hash in the same write.
func (s *ITService) parkVaultFill(ctx context.Context, d *PoolDesk, intentID uuid.UUID, revertedHash, why string) error {
	return withAdvisoryLock(ctx, s.db, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		var locked models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "status").First(&locked, "id = ?", intentID).Error; err != nil {
			return err
		}
		if locked.Status != models.ITStatusDelivering {
			return nil
		}
		if revertedHash != "" {
			if err := tx.Model(&models.PoolOrder{}).
				Where("intent_id = ? AND generation = 1 AND fill_tx_hash = ? AND filled_at IS NULL", intentID, revertedHash).
				Updates(map[string]any{"fill_tx_hash": "", "fill_spread_units": nil}).Error; err != nil {
				return err
			}
		}
		res := tx.Model(&models.ITIntent{}).Where("id = ? AND status = ?", intentID, models.ITStatusDelivering).
			Updates(map[string]any{"status": models.ITStatusBuyAwaitingRate,
				"failure_reason": models.ITFailurePoolFillRefused + ": " + why})
		if res.Error == nil && res.RowsAffected == 1 {
			log.Printf("[liquidity] ALERT liquidity_pool_fill_refused intent=%s reason=%q — retry the fill or refund", intentID, RedactAlertText(why))
		}
		return res.Error
	})
}

// RetryPoolFill is the operator's exit from a pool_fill_refused park once the
// cause is fixed (collateral posted, vault unpaused): back to delivering.
func (s *ITService) RetryPoolFill(ctx context.Context, adminID, intentID uuid.UUID, note string) (*models.ITIntent, error) {
	if note = strings.TrimSpace(note); note == "" {
		return nil, ErrITOperatorNoteRequired
	}
	d := s.poolDesk
	if !d.vaultMode() {
		return nil, ErrPoolSettleUnwired
	}
	err := withAdvisoryLock(ctx, s.db, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		var intent models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&intent, "id = ?", intentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrStableIntentNotFound
			}
			return err
		}
		if !isPoolBuy(&intent) || intent.Status != models.ITStatusBuyAwaitingRate ||
			!strings.HasPrefix(intent.FailureReason, models.ITFailurePoolFillRefused) {
			return fmt.Errorf("%w: only a %s park can retry its fill", ErrITBadState, models.ITFailurePoolFillRefused)
		}
		res := tx.Model(&models.ITIntent{}).Where("id = ? AND status = ?", intentID, models.ITStatusBuyAwaitingRate).
			Updates(map[string]any{"status": models.ITStatusDelivering, "failure_reason": ""})
		if res.Error != nil || res.RowsAffected != 1 {
			return fmt.Errorf("%w: the order moved", ErrITBadState)
		}
		return auditIntent(tx, intentID, adminID, models.ITAuditPoolFillRetried, note, map[string]any{"parkedReason": intent.FailureReason})
	})
	if err != nil {
		return nil, err
	}
	return s.getIntent(ctx, intentID)
}

// vaultRefundable: a vault order may be refunded in naira only while no fill
// is recorded (a reverted one is cleared when the order is parked).
func vaultRefundable(tx *gorm.DB, intentID uuid.UUID) error {
	var o models.PoolOrder
	if err := tx.First(&o, "intent_id = ? AND generation = 1", intentID).Error; err != nil {
		return err
	}
	if o.FillTxHash != "" || o.FilledAt != nil {
		return fmt.Errorf("%w: this order's fill is recorded (%s) — it must be delivered, never refunded", ErrITBadState, o.FillTxHash)
	}
	return nil
}

// vaultOrderActions are the operator actions a vault order allows right now.
func (s *ITService) vaultOrderActions(ctx context.Context, intent *models.ITIntent) []string {
	out := []string{}
	refundable := vaultRefundable(s.db.WithContext(ctx), intent.ID) == nil
	switch intent.Status {
	case models.ITStatusPaymentReceived:
		if refundable {
			out = append(out, "refund")
		}
	case models.ITStatusBuyAwaitingRate:
		if refundable {
			out = append(out, "refund")
		}
		if strings.HasPrefix(intent.FailureReason, models.ITFailurePoolFillRefused) {
			out = append(out, "retry-fill")
		}
	case models.ITStatusDelivering:
		if leg, err := s.liveSaleLeg(ctx, intent.ID); err == nil && leg != nil && leg.Status == models.RampLegStatusPending &&
			time.Since(leg.CreatedAt) > s.treasuryLimits().sendStall {
			out = append(out, "resend")
		}
	}
	return out
}

// ─── Claim ──────────────────────────────────────────────────────────────────

// claimVault is claimPool for a vault order (spec §12.3 on the shared pool),
// under the pool lock with the intent row locked. The amount owed — the
// honoured quote's, or a live re-price for money that arrived late — must fit
// the vault's free collateral with this order's own reservation counted
// back in. Covered: claim, and the snapshot's fill and spread become what she
// is owed. Not: park pool_fill_refused, the snapshot already holding the owed
// fill so that a retry (after collateral is posted) fills the right amount. A
// transient error (no mid, vault unreadable) writes nothing: the central
// poller retries.
func (s *ITService) claimVault(ctx context.Context, candidate *models.ITIntent, claim map[string]any, amountMinor string,
	ch PayInChannel, arrival *routedArrival, write func(*gorm.DB) *gorm.DB) (*gorm.DB, string, error) {
	d := s.poolDesk
	paid, ok := new(big.Int).SetString(strings.TrimSpace(amountMinor), 10)
	if !ok || paid.Sign() <= 0 {
		return nil, "", fmt.Errorf("%w: unusable amount %q", ErrStableBuyBelowMinimum, amountMinor)
	}
	var mid *big.Rat
	if !arrival.honoured {
		if s.treasuryRates == nil {
			return nil, "", fmt.Errorf("%w: the pool is not wired", ErrStableQuoteUnavailable)
		}
		var err error
		if mid, err = s.treasuryRates.BuyRate(ctx, candidate.Currency); err != nil {
			return nil, "", err
		}
	}
	var res *gorm.DB
	parked := ""
	err := withAdvisoryLock(ctx, s.db, poolLockKey(d.ChainKey(), d.TokenSymbol()), "liquidity pool: order lock", func(tx *gorm.DB) error {
		var locked models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", candidate.ID).Error; err != nil {
			return err
		}
		if locked.Status != models.ITStatusPendingPayment {
			res = write(tx) // lost the race: RowsAffected 0, the caller re-checks by ref
			return res.Error
		}
		set := CurrentPoolSettings()
		owed := arrival.units
		var p *PoolPrice
		if !arrival.honoured {
			var perr error
			if p, perr = s.poolQuoteAt(locked.Asset, locked.Currency, paid, mid, set, ch, false, nil); perr != nil {
				parkClaim(claim, "could not price at settlement: "+RedactAlertText(perr.Error()))
				parked = parkReasonOf(perr)
				res = write(tx)
				return res.Error
			}
			owed = p.Quote.AssetUnits
		}
		o, err := d.vaultOrder(ctx, tx, locked.ID)
		if err != nil {
			return err
		}
		fill, err := CanonicalToChainUnits(owed, d.c.Token.Decimals)
		if err != nil {
			return err
		}
		spread := vaultSpreadUnits(fill, int64(o.PoolSpreadBps))
		rctx, cancel := context.WithTimeout(ctx, poolFreshReadTimeout)
		st, err := d.c.Vault.State(rctx)
		cancel()
		if err != nil {
			return fmt.Errorf("liquidity pool: the vault is unreadable at claim: %w", err)
		}
		reserved, err := d.vaultReserved(ctx, tx)
		if err != nil {
			return err
		}
		// Every other order's reserve comes off; her own is what this decides
		// (never clamp before adding it back: that would hide a shortfall).
		reserved.Sub(reserved, bigFromDecimal(o.FillUnits)).Sub(reserved, bigFromDecimal(o.SpreadUnits))
		free := new(big.Int).Sub(st.FreeCapacity(), reserved)
		fs, ss := fill.String(), spread.String()
		if err := tx.Model(&models.PoolOrder{}).Where("intent_id = ? AND generation = 1 AND fill_tx_hash = ''", locked.ID).
			Updates(map[string]any{"fill_units": fs, "spread_units": ss}).Error; err != nil {
			return err
		}
		if st.Paused || free.Cmp(new(big.Int).Add(fill, spread)) < 0 {
			parkClaim(claim, models.ITFailurePoolFillRefused+": the pool's free collateral does not cover the amount owed")
			parked = models.ITFailurePoolFillRefused
			log.Printf("[liquidity] ALERT liquidity_pool_fill_refused intent=%s owed=%s honoured=%v — at claim", locked.ID, owed, arrival.honoured)
			res = write(tx)
			return res.Error
		}
		if arrival.honoured {
			arrival.writeTo(claim)
		} else {
			setQuoteColumns(claim, p.Quote.AssetUnits, p.Quote.RatePerUnitMinor, p.Quote.DisplayRateMinor, p.Quote.Fees)
			claim["repriced"] = true
			arrival.units, arrival.rate, arrival.quote = p.Quote.AssetUnits, p.Quote.RatePerUnitMinor, p.Quote
		}
		res = write(tx)
		return res.Error
	})
	if err == nil {
		d.vaultCache.Forget("free")
	}
	return res, parked, err
}
