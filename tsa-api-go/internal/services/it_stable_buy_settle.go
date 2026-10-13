package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// stableBuyClaimWindow bounds how long an unpaid stablecoin buy intent may
// claim an incoming charge. Nothing else expires or cancels one, so without
// this an abandoned intent sits in pending_payment forever and hijacks a
// later, unrelated payment from the SAME user — including a plain MCGP
// purchase, which has no stablecoin asset filter to protect it. 24h, not the
// (nonexistent) rate-lock TTL: there is deliberately no rate lock on buy, and
// a bank transfer can reasonably take most of a day.
const stableBuyClaimWindow = 24 * time.Hour

// settleStableBuyFromCharge attributes an incoming naira charge to this user's
// open stablecoin purchase, if they have one.
//
// Returns (true, nil) when the charge funded a buy and the caller must stop.
// Returns (false, nil) when there was no open stablecoin buy — the caller then
// continues to the existing MCGP credit path, which is left untouched.
//
// "Open" means pending_payment specifically. That is the only state which can
// claim money: an intent already converting or delivering has been funded and
// cannot absorb more, so it must not block or capture a later payment.
func (s *ITService) settleStableBuyFromCharge(ctx context.Context, userID uuid.UUID, chargeID, amountMinor string) (bool, error) {
	chargeID = strings.TrimSpace(chargeID)
	if chargeID == "" {
		return false, errors.New("it stable buy: empty charge id")
	}

	// Idempotency first: a redelivered webhook must not settle twice.
	replayed, claimed, err := s.finishStableBuyReplay(ctx, chargeID, true)
	if err != nil {
		return false, err
	}
	if replayed {
		return claimed, nil
	}

	// Claim atomically: the WHERE carries the expected status, so two
	// concurrent webhooks cannot both move the same intent. Only the amount is
	// written here — the price is struck below, once the intent is ours.
	amountMinor = strings.TrimSpace(amountMinor)

	// Pick ONE intent, then claim it by id. A bulk Updates over the whole
	// predicate would write the same payment_ref to every matching row if the
	// one-open-purchase check ever raced and let two through — and payment_ref
	// carries a partial UNIQUE index (config/database.go), so that INSERT-time
	// race would become a permanent webhook error instead of a claimed charge.
	var candidate models.ITIntent
	err = s.db.WithContext(ctx).
		Where("user_id = ? AND direction = ? AND status = ? AND asset IN ? AND created_at >= ?",
			userID, models.ITDirectionBuy, models.ITStatusPendingPayment,
			[]string{models.ITAssetUSDC, models.ITAssetUSDT}, time.Now().Add(-stableBuyClaimWindow)).
		Order("created_at ASC").
		First(&candidate).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil // no open stablecoin buy — fall through to MCGP
	} else if err != nil {
		return false, err
	}

	// The VA webhook carries no charge time and arrives within seconds, so
	// processing time stands in for arrival (D33).
	return s.claimAndPriceStableBuy(ctx, &candidate, chargeID, amountMinor, time.Now(), true, PayInVA)
}

// finishStableBuyReplay handles money we have already seen.
//
// replayed reports whether some intent already carries paymentRef; claimed
// reports whether that intent is a stablecoin buy (so the caller must stop)
// rather than an MCGP one (so the caller continues).
//
// It is not a bare "already done, return" check. A crash between claiming a
// payment and pricing it would otherwise leave the creation-time quote in place
// forever — the rate lock the design removed, reappearing in a crash window —
// so a replay finishes the job the interrupted attempt started.
//
// Shared by both entry points: a Flutterwave webhook redelivers, and the
// central-account poller re-reads the same statement row every 30 seconds, so
// both need exactly this behaviour and neither should own it alone.
//
// parkOnBadRate carries the same meaning as in claimAndPriceStableBuy — the
// webhook parks because nothing else will come back for it, the poller errors
// because something will, in 30 seconds.
func (s *ITService) finishStableBuyReplay(ctx context.Context, paymentRef string, parkOnBadRate bool) (replayed, claimed bool, err error) {
	var existing models.ITIntent
	lookup := s.db.WithContext(ctx).
		Where("payment_ref = ? AND direction = ?", paymentRef, models.ITDirectionBuy).
		First(&existing).Error
	if errors.Is(lookup, gorm.ErrRecordNotFound) {
		return false, false, nil
	}
	if lookup != nil {
		return false, false, lookup
	}
	if !IsStableAsset(existing.Asset) {
		return true, false, nil
	}
	if existing.Status == models.ITStatusPaymentReceived && !stableBuyPriced(&existing) {
		if _, pErr := s.priceStableBuy(ctx, &existing, existing.ReceivedAmount,
			models.ITStatusPaymentReceived, models.ITStatusPaymentReceived); pErr != nil {
			if !parkOnBadRate {
				return false, false, fmt.Errorf("it stable buy: cannot re-price %q: %w", paymentRef, pErr)
			}
			log.Printf("[it stable buy] ALARM payment=%s redelivery could not price: %v", paymentRef, pErr)
			// Status-guarded like every other write on this path: a watcher
			// tick that advanced the intent while we were on the rate call
			// must not be dragged backwards into the park.
			if uErr := s.db.WithContext(ctx).Model(&models.ITIntent{}).
				Where("id = ? AND status = ?", existing.ID, models.ITStatusPaymentReceived).
				Updates(map[string]any{
					"status":         models.ITStatusBuyAwaitingRate,
					"failure_reason": "could not price at settlement: " + pErr.Error(),
				}).Error; uErr != nil {
				return false, false, uErr
			}
		}
	}
	return true, true, nil
}

// claimAndPriceStableBuy prices an intent on what actually landed and claims it
// for paymentRef, in ONE status-guarded write.
//
// Shared by the two ways money can arrive: a Flutterwave charge keyed on the
// charge id (VA mode) and a central-account credit keyed on the NIBSS session
// id (central mode). They differ only in how the intent was chosen — by the
// user's oldest open purchase, or by the matcher — so everything after that
// point is deliberately one function rather than two that drift.
//
// Returns (true, nil) when the money funded this intent and the caller must
// stop; (false, nil) when the claim was lost to a concurrent writer.
//
// parkOnBadRate decides what happens when the rate source will not answer, and
// the two callers want opposite things:
//
//	true  (webhook) — park the intent for an operator. The charge event will
//	                  not be redelivered forever and the money cannot be handed
//	                  back, so erroring would leave it unprocessed with nothing
//	                  scheduled to try again.
//	false (inbound) — write NOTHING and return the error. The credit already
//	                  has a durable inbound_credits row and the poller re-reads
//	                  it every 30 seconds, so a transient rate outage resolves
//	                  itself. Parking would convert a 30-second wait into a
//	                  manual operator recovery for no gain.
//
// arrivedAt is when the money arrived (D33); a routed buy is honoured or priced live from it.
// ch is the channel it came through, whose funding fee a live price passes on.
func (s *ITService) claimAndPriceStableBuy(
	ctx context.Context,
	candidate *models.ITIntent,
	paymentRef, amountMinor string,
	arrivedAt time.Time,
	parkOnBadRate bool,
	ch PayInChannel,
) (bool, error) {
	// Price BEFORE claiming, so that claiming and pricing are ONE write: a
	// window between them left the creation-time asset_amount in place.
	chargeID := paymentRef
	claim := map[string]any{
		"status":          models.ITStatusPaymentReceived,
		"payment_ref":     chargeID,
		"received_amount": amountMinor,
	}
	routed := IsRoutedStableBuy(candidate)
	var (
		q       *StableBuyQuote
		arrival *routedArrival
		qErr    error
	)
	if routed {
		arrival, qErr = s.priceRoutedArrival(ctx, candidate, amountMinor, arrivedAt, ch)
	} else if t, terr := providerTerms(candidate); terr != nil {
		qErr = terr // review M1: never priced at Flutterwave's old endpoint
	} else if t.pool {
		qErr = errPoolNotRouted // an unrouted pool row is never a Flutterwave quote; parks at once
	} else {
		q, qErr = s.QuoteStableBuy(ctx, candidate.Asset, amountMinor, candidate.Currency)
	}
	// D32: money that can never price parks on both paths; a transient outage
	// on the central path writes nothing and the poller retries in 30s.
	if qErr != nil && !parkOnBadRate && !isDeterministicPricingError(qErr) {
		return false, fmt.Errorf("it stable buy: cannot price %q at settlement: %w", paymentRef, qErr)
	}
	parkedReason := ""
	switch {
	case qErr != nil:
		// The money is here and cannot be handed back: park for an operator,
		// in the SAME write that claims. ITStatusBuyAwaitingRate, never the
		// MCGP flow's awaiting_rate (that would re-open the intent).
		log.Printf("[it stable buy] ALARM charge=%s landed but could not be priced: %v", chargeID, qErr)
		if isDeterministicPricingError(qErr) {
			log.Printf("[it stable buy] ALERT it_stable_buy_parked_unpriceable intent=%s payment=%s reason=%q",
				candidate.ID, chargeID, qErr.Error())
		}
		parkClaim(claim, "could not price at settlement: "+qErr.Error())
		parkedReason = parkReasonOf(qErr)
	case routed && arrival.needsPoolCoverage:
		// Claimed under the pool lock by claimPool, below.
	case routed:
		arrival.writeTo(claim)
	default:
		// The old endpoint: priced at the LIVE rate on what actually landed.
		setQuoteColumns(claim, q.AssetUnits, q.RatePerUnitMinor, nil, nil)
		claim["repriced"] = true
	}

	// The status in the WHERE is the concurrency guard.
	write := func(db *gorm.DB) *gorm.DB {
		return db.Model(&models.ITIntent{}).
			Where("id = ? AND status = ?", candidate.ID, models.ITStatusPendingPayment).
			Updates(claim)
	}
	var res *gorm.DB
	if routed && qErr == nil && arrival.needsPoolCoverage {
		var poolErr error
		res, parkedReason, poolErr = s.claimPool(ctx, candidate, claim, amountMinor, ch, arrival, write)
		if poolErr != nil {
			if !parkOnBadRate {
				return false, fmt.Errorf("it stable buy: cannot price pool %q: %w", paymentRef, poolErr)
			}
			parkClaim(claim, "could not price at settlement: "+RedactAlertText(poolErr.Error())) // review M13
			parkedReason = "rate_unavailable"
			res = write(s.db.WithContext(ctx))
		}
	} else if routed && qErr == nil && arrival.needsStock {
		// The company wallet, priced live: the stock is won under the lock,
		// with this order's own reservation excluded (spec §5.2/§5.3).
		// The live amount also counts against her rolling daily cap, with this
		// order's own row left out (it was capped at open at its old amount).
		var stockErr error
		lockErr := s.withStockLock(ctx, candidate.DestChain, candidate.Asset, func(tx *gorm.DB) error {
			capErr := s.checkTreasuryDailyCap(ctx, tx, candidate.UserID, arrival.units, candidate.ID)
			switch {
			case errors.Is(capErr, ErrTreasuryDailyCapReached):
				parkClaim(claim, "the live amount is above the buyer's daily company-wallet limit")
				parkedReason = "daily_cap"
				res = write(tx)
				return res.Error
			case capErr != nil:
				stockErr = capErr
				return capErr
			}
			free, _, err := s.freeTreasuryStock(ctx, tx, candidate.DestChain, candidate.Asset, candidate.ID, true)
			if err != nil {
				stockErr = err
				return err
			}
			if free.Cmp(arrival.units) < 0 {
				parkClaim(claim, "the company wallet's free stock does not cover the live amount")
				parkedReason = "stock"
			}
			res = write(tx)
			return res.Error
		})
		switch {
		case stockErr != nil:
			// The balance (or her daily total) could not be read: transient,
			// like a rate outage. A dormant wallet is too, but it must not
			// retry silently: nothing on this path can deliver until it is back.
			if errors.Is(stockErr, ErrTreasuryUnavailable) {
				log.Printf("[it stable buy] ALERT ramp_treasury_unavailable_at_settle intent=%s payment=%s — the company wallet is not configured",
					candidate.ID, chargeID)
			}
			if !parkOnBadRate {
				return false, fmt.Errorf("it stable buy: cannot check stock for %q: %w", paymentRef, stockErr)
			}
			parkClaim(claim, "could not read the company wallet's stock: "+stockErr.Error())
			parkedReason = "stock_unreadable"
			res = write(s.db.WithContext(ctx))
		case lockErr != nil:
			return false, lockErr
		}
	} else {
		res = write(s.db.WithContext(ctx))
	}
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		// Lost the race: another call claimed this intent first — maybe with
		// THIS chargeID, in which case falling through to MCGP would hand the
		// winner's charge to the wrong path. Re-check by payment_ref.
		var winner models.ITIntent
		wErr := s.db.WithContext(ctx).
			Where("payment_ref = ? AND direction = ?", chargeID, models.ITDirectionBuy).
			First(&winner).Error
		if wErr == nil {
			return IsStableAsset(winner.Asset), nil
		} else if !errors.Is(wErr, gorm.ErrRecordNotFound) {
			return false, wErr
		}
		return false, nil
	}

	// A converted buy's record follows its money: claimed (honoured, live or
	// parked) → pending (spec §15.4). Best effort: the handoff accepts either.
	if err := markConversion(ctx, s.db, candidate.ID, []string{models.PaycrestStatusAwaitingPayment},
		map[string]any{"status": models.PaycrestStatusPending}); err != nil {
		log.Printf("[it stable buy] ALERT ramp_conversion_mark_failed intent=%s to=pending err=%s", candidate.ID, RedactAlertText(err.Error()))
	}

	switch {
	case parkedReason != "":
		if parkedReason == "stock" || parkedReason == "daily_cap" || parkedReason == models.ITFailurePoolSlicesShort {
			// Deterministic for this money, like a pricing error (D32):
			// retrying cannot help, a human must decide.
			log.Printf("[it stable buy] ALERT it_stable_buy_parked_unpriceable intent=%s payment=%s reason=%q",
				candidate.ID, chargeID, parkedReason)
		}
		switch routedMetric(candidate) {
		case "treasury":
			log.Printf("[it stable buy] METRIC ramp_treasury_parked intent=%s reason=%s", candidate.ID, parkedReason)
		case "pool":
			log.Printf("[it stable buy] METRIC ramp_pool_parked intent=%s reason=%s", candidate.ID, parkedReason)
		case "unknown":
			log.Printf("[it stable buy] METRIC ramp_unknown_parked intent=%s provider=%q reason=%s", candidate.ID, candidate.Provider, parkedReason)
		}
	case routed && arrival.honoured:
		log.Printf("[it stable buy] payment=%s funded %s %s for user=%s (quote honoured)",
			chargeID, arrival.units, candidate.Asset, candidate.UserID)
		s.logHonoured(ctx, candidate, amountMinor, arrival.units, arrival.rate, ch)
	case routed:
		log.Printf("[it stable buy] METRIC ramp_%s_repriced intent=%s quoted_rate=%s live_rate=%s",
			routedMetric(candidate), candidate.ID, candidate.LockedRate, arrival.rate)
		s.notifyRateUpdated(candidate, arrival.units)
	default:
		log.Printf("[it stable buy] payment=%s funded %s %s for user=%s",
			chargeID, q.AssetUnits, candidate.Asset, candidate.UserID)
	}
	return true, nil
}

// priceStableBuy re-prices `intent` on `fiatMinor` at the CURRENT live rate,
// moving it from `fromStatus` to `toStatus` in one status-guarded write.
//
// The status guard is not optional. Unguarded, a price write that lands late —
// the rate call is an HTTP round-trip — drags an intent BACKWARDS out of
// `converting` after the naira has already been spent; the next watcher tick
// then re-enters ConvertStableBuy and converts a second time with the SAME
// reference, leaving nothing but the provider's own idempotency between us and
// moving real money twice.
//
// Returns false when no row matched: something else advanced the intent first,
// and it is no longer ours to price.
//
// Repricing a `delivering` intent additionally requires delivery_id to still
// be empty, and that condition is part of the CAS rather than a caller's prior
// read. The watcher calls DeliverStableBuy on every delivering intent every
// 30s, and the slippage jam this recovery exists for clears the moment the
// balance settles — so "the watcher sends while the operator is mid-reprice"
// is a live sequence, and the rate call is an HTTP round-trip wide. Checked
// outside the write, it would rewrite asset_amount on a transfer already sent
// for the old figure: no double send, but the intent settles recording an
// amount that is not what was paid.
//
// The amount is a parameter rather than a read of intent.ReceivedAmount so
// that the first settlement can price BEFORE it claims — see
// settleStableBuyFromCharge. Callers after the claim pass ReceivedAmount.
func (s *ITService) priceStableBuy(ctx context.Context, intent *models.ITIntent, fiatMinor, fromStatus, toStatus string) (bool, error) {
	q, err := s.quoteForIntent(ctx, intent, fiatMinor)
	if err != nil {
		return false, err
	}
	cas := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status = ?", intent.ID, fromStatus)
	if fromStatus == models.ITStatusDelivering {
		cas = cas.Where("delivery_id = ?", "")
	}
	cols := map[string]any{
		"status":         toStatus,
		"repriced":       true,
		"failure_reason": "", // clear a stale "could not price at settlement" from a prior park
	}
	if IsRoutedStableBuy(intent) {
		setQuoteColumns(cols, q.AssetUnits, q.RatePerUnitMinor, q.DisplayRateMinor, q.Fees)
	} else {
		setQuoteColumns(cols, q.AssetUnits, q.RatePerUnitMinor, nil, nil)
	}
	res := cas.Updates(cols)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil
	}
	intent.Status = toStatus
	intent.AssetAmount = q.AssetUnits.String()
	intent.LockedRate = q.RatePerUnitMinor.String()
	intent.Repriced = true
	intent.FailureReason = ""
	return true, nil
}

// RepriceParkedStableBuy is the operator recovery for a stablecoin buy that is
// holding the user's money and cannot advance on its own terms. It reprices
// what actually arrived at the CURRENT live rate. The watcher deliberately
// never does this by itself: both states below mean something was wrong, and
// resuming is a human's call.
//
// Two states qualify:
//
//	buy_awaiting_rate            money landed that could not be priced at the
//	                             time. Returns to payment_received, where the
//	                             watcher picks it up again.
//	delivering, delivery_id = "" the naira was converted but the outbound
//	                             transfer has not been sent. AssetAmount came
//	                             from the /transfers/rates PREVIEW at
//	                             settlement, while ConvertBalance executed at
//	                             its own rate less a fee; if that landed worse
//	                             than the spread cushion, DeliverStableBuy's
//	                             balance gate refuses forever and no other
//	                             endpoint can reach the intent. Stays in
//	                             `delivering` — the naira is already spent, so
//	                             sending it back to payment_received would have
//	                             the watcher convert a second time.
//
// Repricing a `delivering` intent uses the rate NOW, not what the conversion
// actually produced (FWTransfer carries no destination amount, so that figure
// is not available to us). If the rate has moved the other way the new amount
// can still exceed the balance; the call is safely repeatable.
func (s *ITService) RepriceParkedStableBuy(ctx context.Context, intentID uuid.UUID) (*models.ITIntent, error) {
	intent, err := s.getIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if intent.Direction != models.ITDirectionBuy || !IsStableAsset(intent.Asset) {
		return nil, ErrITBadState
	}
	terms, err := providerTerms(intent)
	if err != nil {
		return nil, err
	}
	if terms.companyWallet {
		// D3: market + take, the maximum, and stock won under the lock.
		return s.repriceTreasuryParked(ctx, intent, false, nil)
	}
	if terms.pool {
		return nil, fmt.Errorf("%w: a TSA pool buy is re-priced only by re-delivery", ErrITBadState)
	}
	if !terms.flutterwaveFulfils {
		return nil, fmt.Errorf("%w: %s has no re-price", ErrUnknownITProvider, terms.provider)
	}
	var to string
	switch {
	case intent.Status == models.ITStatusBuyAwaitingRate:
		if intent.ConversionAttempts >= maxStableConversionAttempts {
			// A park taken on the last conversion attempt (Flutterwave's 0302
			// floor) cannot convert again: resuming it would only stall the
			// watcher on ErrStableConversionExhausted. Refund it instead.
			return nil, fmt.Errorf("%w: its conversion attempts are exhausted (%d of %d); mark it refunded instead",
				ErrITBadState, intent.ConversionAttempts, maxStableConversionAttempts)
		}
		to = models.ITStatusPaymentReceived
	case intent.Status == models.ITStatusDelivering && intent.DeliveryID == "":
		if IsRoutedStableBuy(intent) && !intent.Repriced {
			// An honoured buy short of float waits for the top-up: re-pricing
			// would take back what she was promised (spec §5.2).
			return nil, fmt.Errorf("%w: an honoured routed buy waits for the float top-up", ErrITBadState)
		}
		to = models.ITStatusDelivering
	default:
		return nil, ErrITBadState
	}
	applied, err := s.priceStableBuy(ctx, intent, intent.ReceivedAmount, intent.Status, to)
	if err != nil {
		return nil, err
	}
	if !applied {
		// Someone moved it between our read and our write.
		return nil, ErrITBadState
	}
	return s.getIntent(ctx, intentID)
}

// claimPool claims a pool buy (spec §12.3) under the pool lock with the
// intent row locked — the lock the expiry canceller (decideSliceCancel) and
// F21 (ClaimSliceResolve) decide under, so whoever locks the intent first
// wins (P1, M7). The owed units are the honoured quote's, or for a buy priced
// live (S18) the current mid and settings'. Either way they must be covered
// by this order's slices that still back it (never a dropped one, review
// C-2): covered → claim (dropping the unselected slices, booking the
// surplus); not → park pool_slices_short (dropping every pending slice for
// the canceller). A deterministic pricing error parks and drops too. A
// transient one (no mid) writes nothing: the central poller retries in 30 s.
func (s *ITService) claimPool(ctx context.Context, candidate *models.ITIntent, claim map[string]any, amountMinor string,
	ch PayInChannel, arrival *routedArrival, write func(*gorm.DB) *gorm.DB) (*gorm.DB, string, error) {
	d := s.poolDesk
	if d == nil {
		return nil, "", fmt.Errorf("%w: the pool is not wired", ErrStableQuoteUnavailable)
	}
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
		now := d.now()
		releasedUnits, pending, err := d.orderSliceUnits(tx, locked.ID)
		if err != nil {
			return err
		}
		released := sumUnits(releasedUnits)
		pendingIDs := make([]uuid.UUID, len(pending))
		for i := range pending {
			pendingIDs[i] = pending[i].ID
		}
		if locked.Status != models.ITStatusPendingPayment {
			res = write(tx) // lost the race: RowsAffected 0, the caller re-checks by ref
			return res.Error
		}
		var p *PoolPrice
		var cov PoolCoverage
		if arrival.honoured {
			cov = EvaluatePoolCoverage(arrival.units, released, pending, CurrentPoolSettings().MinSliceUnits)
		} else {
			var perr error
			if p, cov, perr = s.poolSettlePrice(locked.Asset, locked.Currency, paid, mid, ch, releasedUnits, pending); perr != nil {
				parkClaim(claim, "could not price at settlement: "+RedactAlertText(perr.Error())) // review M13
				parked = parkReasonOf(perr)
				if err := d.dropAllSlices(tx, pendingIDs, now); err != nil {
					return err
				}
				res = write(tx)
				return res.Error
			}
		}
		if !cov.Covered {
			parkClaim(claim, models.ITFailurePoolSlicesShort+": the amount owed is not covered by this order's locked slices")
			parked = models.ITFailurePoolSlicesShort
			log.Printf("[liquidity] ALERT liquidity_pool_slices_short intent=%s owed=%s honoured=%v", locked.ID, cov.Owed, arrival.honoured)
			if err := d.dropAllSlices(tx, pendingIDs, now); err != nil {
				return err
			}
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
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		if err := d.dropAllSlices(tx, cov.Drop, now); err != nil {
			return err
		}
		var gen int
		if err := tx.Model(&models.PoolOrder{}).Where("intent_id = ?", locked.ID).
			Select("COALESCE(MAX(generation), 1)").Scan(&gen).Error; err != nil {
			return err
		}
		sr := tx.Model(&models.PoolOrder{}).Where("intent_id = ? AND generation = ?", locked.ID, gen).
			Update("surplus_units", cov.Surplus.String())
		if sr.Error == nil && sr.RowsAffected == 0 {
			// Review M-13: the claim stands (the money is hers); the
			// snapshot it books against is missing, so C2's accounting
			// would miss the surplus.
			log.Printf("[liquidity] ALERT liquidity_pool_surplus_unrecorded intent=%s generation=%d surplus=%s — no pool order snapshot", locked.ID, gen, cov.Surplus)
		}
		return sr.Error
	})
	return res, parked, err
}
