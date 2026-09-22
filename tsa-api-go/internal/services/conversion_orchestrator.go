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
)

// shortfallToleranceBps is B3: a landing short of the promise by more than this
// is still topped up — the user is always made whole — but it is a margin loss
// finance has to see, so crossing it raises an alert.
const shortfallToleranceBps = 20

// defaultMaxShortfallUSD is the ceiling on an AUTOMATIC top-up. Below it, a
// shortfall is a rounding-scale cost of doing business and is paid silently.
// Above it, something is wrong in a way no top-up should paper over, so the
// order stops for a human with the balance recorded.
const defaultMaxShortfallUSD = 50

// conversionTickLimit is the default batch size for one Tick.
const conversionTickLimit = 20

// defaultStallWindow is how long an order may sit in converting without moving
// before an operator is asked to look at it.
const defaultStallWindow = 6 * time.Hour

// defaultLegCeiling is the HARD outer bound on a single leg, measured from that
// leg's OWN CreatedAt. The stall window exempts a bridge that is pending with a
// transaction hash, because it may still be in flight — and no answer LiFi
// gives can be trusted to say when it has stopped being. This ceiling closes
// that exemption from the other end: a leg pending this long is a human's
// problem whatever it looks like.
const defaultLegCeiling = 24 * time.Hour

// lifiQuoter is the quote side of LiFi this state machine needs.
// *LiFiQuoteClient satisfies it.
type lifiQuoter interface {
	Quote(ctx context.Context, req LiFiQuoteRequest) (*LiFiRoute, error)
}

// ConversionOrchestrator drives one converted buy from "Paycrest paid our
// treasury" to "the user holds at least what we promised" — or, failing that,
// to a refund or to a human.
//
// The ordering rule throughout is: RESERVE THE LEG ROW FIRST, then do anything
// slow, then broadcast. The reservation is what makes a duplicate tick safe —
// not the read of the leg list, which is stale the moment it returns. Two ticks
// racing here both call Reserve; exactly one wins the (order_id, kind, attempt)
// row and the loser returns without quoting or sending.
type ConversionOrchestrator struct {
	db       *gorm.DB
	treasury *Treasury
	legs     rampLegStore
	lifi     lifiQuoter
	status   *LiFiStatusClient
	// transfers is keyed by chain key: LogTransferVerifier wraps ONE RPC
	// client, so a single verifier could only ever prove a delivery on its own
	// chain and would silently never confirm anything on any other.
	transfers   map[string]ERC20TransferVerifier
	chains      *BlockchainService
	retryWindow time.Duration
	// maxShortfallUSD is the automatic top-up ceiling; see defaultMaxShortfallUSD.
	maxShortfallUSD float64
	// stallWindow is how long an order's LEG LEDGER may go without moving before
	// the order is treated as stuck. It is read from the legs, NEVER from
	// updated_at: the Paycrest poller bumps that column on every cycle, so it
	// says nothing about whether a conversion is progressing. It catches the
	// shapes that look dead — a lost SetTxHash write, a pending refund with no
	// oracle — and covers refunding as well as converting, because a refund
	// stuck on a pending leg is just as silent as a stuck conversion.
	//
	// It deliberately does NOT catch a bridge that is pending with a hash; that
	// set may still be in flight, and legCeiling is what bounds it.
	stallWindow time.Duration
	// legCeiling is the outer bound on ANY pending leg, liveness included.
	// See defaultLegCeiling.
	legCeiling time.Duration
	// budgetBps is B: the slice of the user-facing markup that pays for one
	// converted order's round trip (LiFi's own cost plus the treasury's gas).
	// Pushed in by the wiring via SetRoundTripBudgetBps, from the same config
	// value the quote and order services are given, so a route can never be
	// executed against a budget the order was not priced on.
	budgetBps int
	now       func() time.Time
}

// SetRoundTripBudgetBps sets B, the same way PaycrestOrderService takes it.
// buildRampConversion only ever reaches this point once ConversionPriced()
// has established 0 < B < F, so a zero here means nobody wired it — and a
// zero budget refuses every non-legacy route, which is the fail-closed
// direction.
func (o *ConversionOrchestrator) SetRoundTripBudgetBps(bps int) { o.budgetBps = bps }

// NewConversionOrchestrator wires one converted-buy state machine.
// stallWindow, legCeiling and maxShortfallUSD are the orchestrator's tunables
// (see their field docs above); a zero value for any of them keeps that
// tunable's code default (defaultStallWindow, defaultLegCeiling,
// defaultMaxShortfallUSD) rather than disabling the check, the same
// zero-means-default convention retryWindow already uses.
func NewConversionOrchestrator(
	db *gorm.DB, treasury *Treasury, legs rampLegStore,
	lifi lifiQuoter, status *LiFiStatusClient, transfers map[string]ERC20TransferVerifier,
	chains *BlockchainService, retryWindow, stallWindow, legCeiling time.Duration,
	maxShortfallUSD float64, now func() time.Time,
) *ConversionOrchestrator {
	if now == nil {
		now = time.Now
	}
	if retryWindow <= 0 {
		retryWindow = 30 * time.Minute
	}
	if stallWindow <= 0 {
		stallWindow = defaultStallWindow
	}
	if legCeiling <= 0 {
		legCeiling = defaultLegCeiling
	}
	if maxShortfallUSD <= 0 {
		maxShortfallUSD = defaultMaxShortfallUSD
	}
	keyed := make(map[string]ERC20TransferVerifier, len(transfers))
	for k, v := range transfers {
		keyed[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return &ConversionOrchestrator{
		db: db, treasury: treasury, legs: legs, lifi: lifi, status: status,
		transfers: keyed, chains: chains, retryWindow: retryWindow,
		maxShortfallUSD: maxShortfallUSD, stallWindow: stallWindow,
		legCeiling: legCeiling, now: now,
	}
}

// Tick advances every converted buy that is still in flight. One order's
// failure never stops the others: errors are joined and reported, not fatal.
func (o *ConversionOrchestrator) Tick(ctx context.Context, limit int) error {
	if o == nil || o.db == nil {
		return nil
	}
	if limit <= 0 {
		limit = conversionTickLimit
	}
	var orders []models.PaycrestOrderRecord
	err := o.db.WithContext(ctx).
		Where("route_kind = ? AND direction = ? AND status IN ?",
			models.RouteKindConverted, models.PaycrestDirectionBuy,
			[]string{models.PaycrestStatusConverting, models.PaycrestStatusRefunding}).
		// Ordered by when we last LOOKED, not when the order last moved, so a
		// row that cannot progress still yields its turn to the ones behind it.
		Order("ramp_checked_at ASC NULLS FIRST").Limit(limit).Find(&orders).Error
	if err != nil {
		return err
	}
	var errs []error
	for i := range orders {
		if err := o.Advance(ctx, &orders[i]); err != nil {
			log.Printf("[ramp-conversion] order=%s advance: %v", orders[i].ID, err)
			errs = append(errs, err)
		}
		o.afterStep(ctx, &orders[i])
	}
	return errors.Join(errs...)
}

// afterStep records that we looked, and notices when looking has stopped
// helping.
//
// Rotation and staleness are deliberately different questions asked of
// different columns. Rotation bumps ramp_checked_at unconditionally, so an
// order that cannot progress still yields its turn in the queue. Staleness is
// read from the LEG LEDGER, never from updated_at: nothing writes the order row
// between custody and settlement, so "updated_at is old" would be a hard
// deadline on the whole conversion — and PaycrestOrderService.Sync bumps
// updated_at on every poll anyway, which would both suppress the detector and
// leave ramp_checked_at permanently NULL at the head of the queue.
func (o *ConversionOrchestrator) afterStep(ctx context.Context, order *models.PaycrestOrderRecord) {
	err := o.db.WithContext(ctx).Model(&models.PaycrestOrderRecord{}).
		Where("id = ?", order.ID).
		UpdateColumn("ramp_checked_at", o.now().UTC()).Error
	if err != nil {
		log.Printf("[ramp-conversion] order=%s rotate: %v", order.ID, err)
		return
	}
	// Advance may have just moved this order (GORM writes back through the same
	// struct), so a terminal or parked status here means there is nothing stuck.
	if order.Status != models.PaycrestStatusConverting && order.Status != models.PaycrestStatusRefunding {
		return
	}
	legs, err := o.legs.ForOrder(ctx, order.ID)
	if err != nil {
		log.Printf("[ramp-conversion] order=%s stall check: %v", order.ID, err)
		return
	}
	// An order with NO legs has never been driven by this state machine, so
	// there is nothing here to call stuck. Its only available clock would be
	// order.CreatedAt, which measures the user's bank payment and Paycrest's
	// matching window — neither of which this orchestrator owns. Parking on it
	// would hand an operator an order the user may not have paid for, record
	// the FULL promise as owed against it, and then never recover: Advance
	// early-returns on NeedsOperator(), so the delivery that arrives an hour
	// later would never be seen.
	if len(legs) == 0 {
		return
	}
	// The outer ceiling is asked FIRST, because it is the one question that does
	// not care whether the leg looks alive. Everything below exempts a live
	// bridge, and several genuinely dead shapes — a DONE with a substatus we
	// cannot qualify, a NOT_FOUND, a landing no verifier will ever read — are
	// indistinguishable from a live one without this bound.
	if stuck := legPastCeiling(legs, o.now().Add(-o.legCeiling)); stuck != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_leg_over_ceiling_total=1 order=%s leg=%s kind=%s attempt=%d tx=%s",
			order.ID, stuck.ID, stuck.Kind, stuck.Attempt, stuck.TxHash)
		reason := fmt.Sprintf("%s leg attempt %d (tx %q) pending since %s, past the %s ceiling",
			stuck.Kind, stuck.Attempt, stuck.TxHash,
			stuck.CreatedAt.UTC().Format(time.RFC3339), o.legCeiling)
		if err := o.toRecovery(ctx, order, reason, nil); err != nil {
			log.Printf("[ramp-conversion] order=%s park over-ceiling leg: %v", order.ID, err)
		}
		return
	}
	// Inside the ceiling, a bridge that is genuinely in flight is not a stall,
	// however long it takes. Parking it would strand a user who may be about to
	// be paid, and — because nothing is confirmed yet — would record the FULL
	// promise as owed on an order where the money may already be moving.
	if live := liveBridgeLeg(legs); live != nil {
		return
	}
	since := lastLegActivity(legs)
	if since.IsZero() {
		return // a ledger with no usable clock is not evidence of anything
	}
	if !o.now().After(since.Add(o.stallWindow)) {
		return
	}
	reason := fmt.Sprintf("no leg activity since %s", since.UTC().Format(time.RFC3339))
	if stuck := latestPendingLeg(legs); stuck != nil {
		reason = fmt.Sprintf("%s; %s leg attempt %d (tx %q) never resolved",
			reason, stuck.Kind, stuck.Attempt, stuck.TxHash)
	}
	log.Printf("[ramp-conversion] ALERT ramp_conversion_stalled_total=1 order=%s status=%s holder=%s since=%s",
		order.ID, order.Status, order.Holder, since.UTC().Format(time.RFC3339))
	if err := o.toRecovery(ctx, order, reason, nil); err != nil {
		log.Printf("[ramp-conversion] order=%s park stalled: %v", order.ID, err)
	}
}

// liveBridgeLeg is a bridge attempt that is still pending AND has a transaction
// on the chain — the set LiFi can still report PENDING, or DONE awaiting
// verification, for.
//
// A pending bridge with NO hash is deliberately NOT live: that is the
// lost-SetTxHash shape the stall deadline exists to surface. A pending bridge
// whose hash LiFi has already called FAILED never reaches here either, because
// observeBridge parks it first. So "pending with a hash" is exactly the set
// that must never be parked by a timer, and it costs no extra LiFi call.
func liveBridgeLeg(legs []models.RampLeg) *models.RampLeg {
	for i := range legs {
		leg := &legs[i]
		if leg.Kind == models.RampLegKindBridge &&
			leg.Status == models.RampLegStatusPending && leg.TxHash != "" {
			return leg
		}
	}
	return nil
}

// legPastCeiling is the oldest leg still pending since before `deadline` — the
// hard backstop behind every liveness exemption in this file. Liveness is
// deliberately NOT consulted: the whole point is the shapes that look alive and
// are not.
func legPastCeiling(legs []models.RampLeg, deadline time.Time) *models.RampLeg {
	var out *models.RampLeg
	for i := range legs {
		leg := &legs[i]
		if leg.Status != models.RampLegStatusPending || leg.CreatedAt.IsZero() {
			continue
		}
		if !leg.CreatedAt.Before(deadline) {
			continue
		}
		if out == nil || leg.CreatedAt.Before(out.CreatedAt) {
			out = leg
		}
	}
	return out
}

// lastLegActivity is the most recent moment anything happened on this order's
// ledger: a leg reserved, or a leg confirmed. Zero when there are no legs.
func lastLegActivity(legs []models.RampLeg) time.Time {
	var out time.Time
	for i := range legs {
		leg := &legs[i]
		if leg.CreatedAt.After(out) {
			out = leg.CreatedAt
		}
		if leg.ConfirmedAt != nil && leg.ConfirmedAt.After(out) {
			out = *leg.ConfirmedAt
		}
	}
	return out
}

// Advance moves one converted buy forward by at most ONE step. It is safe to
// call repeatedly: every step either observes chain state or reserves a leg
// before doing anything slow or irreversible.
func (o *ConversionOrchestrator) Advance(ctx context.Context, order *models.PaycrestOrderRecord) error {
	if !order.Converted() || order.Direction != models.PaycrestDirectionBuy {
		return nil
	}
	if models.PaycrestOrderTerminal(order.Status) || order.NeedsOperator() {
		return nil
	}
	// An empty treasury address means the lanes disagree about their signer
	// (or none are configured), so we do not know which address Paycrest was
	// told to pay. A bare error here used to be a SILENT PERMANENT STALL for
	// any order that had already taken custody: Holder is Treasury, zero
	// legs exist yet, and afterStep's own stall detector returns early on
	// zero legs — so nothing ever parks, nothing alerts, and the retry
	// window (anchored on bridge attempt 1) never starts either, so the
	// automatic refund never fires. Reachable in production by hitting the
	// per-chain exposure cap after custody or by removing a chain from
	// RAMP_TREASURY_CHAINS while orders are in flight — park instead, so an
	// order that may already be holding user money is never invisible.
	if o.treasury == nil || o.treasury.Address() == "" {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_treasury_total=1 order=%s", order.ID)
		return o.toRecovery(ctx, order, "treasury lanes disagree on their signer, or none are configured", nil)
	}
	lane := o.treasury.Lane(order.ChainKey)
	if lane == nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_lane_total=1 order=%s chain=%s", order.ID, order.ChainKey)
		return o.toRecovery(ctx, order, fmt.Sprintf("no treasury lane configured for %s", order.ChainKey), nil)
	}

	// 1. Paycrest's own delivery must be PROVEN before we treat the funds as
	//    ours to move — a mined receipt is not proof the treasury was paid.
	//
	//    The test is "not known to be PAST custody", not "explicitly
	//    Paycrest's": custody is also where the promise is restated into the
	//    destination token's units, so any holder we do not recognise —
	//    including the zero value on a row written before Holder was set — has
	//    to come through here. Letting one slip past would leave a SOURCE-unit
	//    promise to be compared against destination-unit landings, which on a
	//    6dp->18dp pair settles about a trillion times early.
	//
	//    Treasury and user are the two states that can only be REACHED through
	//    custody, so they are the two that may skip it. In particular an order
	//    whose money already reached the user must fall through to the leg
	//    ledger below — sending it back through custody would hide the
	//    already-paid check that stops a second payment.
	if order.Holder != models.HolderTreasury && order.Holder != models.HolderUser {
		return o.takeCustody(ctx, order, lane)
	}

	legs, err := o.legs.ForOrder(ctx, order.ID)
	if err != nil {
		return err
	}

	// 2. A pending leg of any kind means one leg is already in flight for this
	//    order, and nothing else may move until it resolves.
	if pending := latestPendingLeg(legs); pending != nil {
		switch pending.Kind {
		case models.RampLegKindBridge:
			return o.observeBridge(ctx, order, pending)
		case models.RampLegKindDeliver:
			return o.observeDeliver(ctx, order, pending)
		case models.RampLegKindRefund:
			return o.observeRefund(ctx, order, pending)
		}
		// An unrecognised leg kind we broadcast ourselves but could not
		// record an outcome for. Its fate is unknown, so this order stops
		// here rather than risking a second payment; the stall deadline
		// surfaces it to an operator.
		log.Printf("[ramp-conversion] ALERT ramp_conversion_leg_stalled_total=1 order=%s leg=%s kind=%s attempt=%d tx=%s",
			order.ID, pending.ID, pending.Kind, pending.Attempt, pending.TxHash)
		return nil
	}

	// 3. A confirmed bridge leg: settle, or top up the shortfall.
	if done := latestLeg(legs, models.RampLegKindBridge, models.RampLegStatusConfirmed); done != nil {
		return o.settleOrTopUp(ctx, order, done)
	}

	// 4. A bridge attempt that BROADCAST but never confirmed may never be
	//    retried and may never be refunded: both would move the money a second
	//    time while the first transfer can still land.
	if stranded := broadcastBridgeLeg(legs); stranded != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_bridge_stranded_total=1 order=%s leg=%s attempt=%d tx=%s",
			order.ID, stranded.ID, stranded.Attempt, stranded.TxHash)
		return o.toRecovery(ctx, order,
			fmt.Sprintf("bridge attempt %d broadcast as %s but never confirmed", stranded.Attempt, stranded.TxHash), nil)
	}

	// 5. No bridge has ever broadcast. The retry window runs from the FIRST
	//    BRIDGE ATTEMPT, not from order creation: a bank payment that took an
	//    hour to clear must still get its conversion attempted, not refunded
	//    before it was ever tried. Attempt 1 is therefore always allowed.
	if first := bridgeLegByAttempt(legs, 1); first != nil &&
		o.now().After(first.CreatedAt.Add(o.retryWindow)) {
		return o.refund(ctx, order, lane, models.FaultPlatform)
	}
	return o.startBridge(ctx, order, lane)
}

// takeCustody proves Paycrest's delivery landed in the treasury before the
// order is allowed to move, and restates the promise in the units the USER will
// actually receive.
func (o *ConversionOrchestrator) takeCustody(ctx context.Context, order *models.PaycrestOrderRecord, lane *TreasuryLane) error {
	if order.TxHash == "" {
		return nil // nothing delivered yet
	}
	verifier := o.verifierFor(order.ChainKey)
	if verifier == nil {
		// Failing closed matters more here than anywhere: with no verifier the
		// alternative is an order that silently never confirms.
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_verifier_total=1 order=%s chain=%s", order.ID, order.ChainKey)
		return ErrSponsorNotAvailable
	}
	// VerifyTransfer sums EVERY matching Transfer log in the transaction, so a
	// delivery split across several transfers in one tx is credited in full.
	received, err := verifier.VerifyTransfer(ctx, order.TxHash, lane.tokenAddress(order.Token), o.treasury.Address())
	if err != nil {
		if errors.Is(err, ErrTransferReverted) || errors.Is(err, ErrTransferWrongRecipient) {
			log.Printf("[ramp-conversion] ALERT ramp_conversion_delivery_bad_total=1 order=%s tx=%s err=%v", order.ID, order.TxHash, err)
		}
		return nil // not yet, or not ours; the next tick looks again
	}
	if received == nil || received.Sign() <= 0 {
		return nil
	}
	// srcDecimals is read FIRST, before anything else: it is needed to even
	// INTERPRET order.TokenAmount, which is a plain human-readable decimal
	// string (Paycrest's own order.Amount, exactly as a direct order has
	// always stored it and the app renders it — see the field's doc comment)
	// — never base units. A short delivery must be detected by comparing
	// like with like: `received` (from VerifyTransfer, always base units) is
	// only comparable to order.TokenAmount once THAT is converted with the
	// SOURCE token's own decimals.
	srcDecimals, err := lane.Native.GetTokenDecimals(lane.tokenAddress(order.Token))
	if err != nil {
		// Custody is proven (VerifyTransfer already succeeded) but nothing
		// downstream can be computed without decimals — not even in what
		// currency to record the delivery. Park with holder=treasury in the
		// SAME write: nothing has recorded custody yet at this point, and a
		// transient RPC failure here must not read as "still needs custody"
		// forever (no later tick re-enters takeCustody once Holder is
		// Treasury — step 1's own gate — so a bare error would otherwise
		// repeat silently, invisible to the stall detector since a leg-less
		// order never trips afterStep).
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_decimals_total=1 order=%s token=%s chain=%s err=%v",
			order.ID, order.Token, lane.ChainKey, err)
		return o.toRecovery(ctx, order,
			fmt.Sprintf("could not read decimals of %s on %s: %v", order.Token, lane.ChainKey, err),
			map[string]any{"holder": models.HolderTreasury})
	}

	// The transaction proves money reached the treasury, not that it was
	// THIS order's money: Paycrest reporting one hash against two orders
	// would otherwise let the second bridge out of the treasury's own float.
	// Only what other orders have not already claimed is this order's.
	claimed, err := o.claimedFromDelivery(ctx, order, srcDecimals)
	if errors.Is(err, errUnreadableClaim) {
		// A corrupt row will not fix itself on the next tick; park it.
		log.Printf("[ramp-conversion] ALERT ramp_conversion_claim_unreadable_total=1 order=%s tx=%s err=%v", order.ID, order.TxHash, err)
		return o.toRecovery(ctx, order, err.Error(), nil)
	}
	if err != nil {
		return err
	}
	if claimed.Sign() > 0 {
		received = new(big.Int).Sub(received, claimed)
		if received.Sign() <= 0 {
			log.Printf("[ramp-conversion] ALERT ramp_conversion_delivery_reused_total=1 order=%s tx=%s already_claimed=%s",
				order.ID, order.TxHash, claimed)
			return o.toRecovery(ctx, order,
				fmt.Sprintf("delivery %s is already fully claimed by another order", order.TxHash), nil)
		}
	}

	// Custody is PROVEN the instant VerifyTransfer returns — record it before
	// resolving the destination, not after. o.destination() below can fail
	// (no treasury lane on the requested chain, or the token unconfigured
	// there) for reasons that have nothing to do with whether the money
	// landed, and a failure there used to leave Holder at its pre-custody
	// value while parking the order in recovery_pending. That produced a
	// park with a materially wrong record: the treasury verifiably held the
	// funds, but every reader of Holder — including the admin refund tool's
	// own custody gate in OwedToUser — saw an order that looked as though
	// custody was never taken, and refused to refund money that was
	// provably sitting in the treasury.
	custodyUpdates := map[string]any{"holder": models.HolderTreasury}
	expected, expErr := DecimalToBaseUnits(order.TokenAmount, srcDecimals)
	switch {
	case expErr != nil:
		// Cannot even tell whether delivery was short. Silently assuming it
		// was not is exactly the bug this whole fix exists to close — park
		// instead, so a human resolves what the row actually says.
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_amount_total=1 order=%s token_amount=%q err=%v",
			order.ID, order.TokenAmount, expErr)
		return o.toRecovery(ctx, order,
			fmt.Sprintf("unreadable order amount %q: %v", order.TokenAmount, expErr),
			map[string]any{"holder": models.HolderTreasury})
	case received.Cmp(expected) < 0:
		// Short delivery: never convert more than actually landed. Recorded
		// back in the SAME human-decimal convention TokenAmount already
		// carries — BaseUnitsToDecimal is DecimalToBaseUnits's exact
		// inverse, so nothing downstream that re-parses this field (every
		// site in this file does) can misread it.
		correctedDecimal := BaseUnitsToDecimal(received, srcDecimals)
		log.Printf("[ramp-conversion] order=%s delivery short: expected=%s received=%s (base units, %ddp) — repricing to %s",
			order.ID, expected, received, srcDecimals, correctedDecimal)
		custodyUpdates["token_amount"] = correctedDecimal
	case received.Cmp(expected) > 0:
		// Over-delivery: VerifyTransfer sums EVERY matching Transfer log in
		// the transaction, so a provider settling another one of our orders
		// in the SAME tx (both rows can carry the same hash), or any
		// unrelated transfer riding along, makes `received` exceed what
		// THIS order's own TokenAmount ever promised. startBridge only ever
		// sends order.TokenAmount (never `received`) — promising off the
		// inflated figure would guarantee a shortfall paid from margin or
		// parked, for money that was never this order's to begin with.
		// Clamp what the PROMISE is computed from to what this order
		// actually asked for; token_amount itself is left untouched; it
		// already equals `expected`.
		log.Printf("[ramp-conversion] order=%s delivery over-counted: expected=%s received=%s (base units, %ddp) — clamping to what this order promised",
			order.ID, expected, received, srcDecimals)
		received = expected
	}
	// What she is owed is scaled to what Paycrest actually delivered, in the
	// SAME write that records custody and the corrected token_amount. It has
	// to be this write: after it, TokenAmount equals what landed, so the
	// received/expected ratio no longer exists anywhere to recover it from,
	// and a later tick sizing the bridge off the original quote would try to
	// send more than the treasury received.
	if q, ok := new(big.Int).SetString(order.QuotedAmount, 10); ok {
		entitled := EntitledAmount(q, expected, received)
		custodyUpdates["quoted_amount"] = entitled.String()
		order.QuotedAmount = entitled.String()
	}
	order.Holder = models.HolderTreasury
	if v, ok := custodyUpdates["token_amount"].(string); ok {
		order.TokenAmount = v
	}
	if err := o.setOrder(ctx, order, custodyUpdates); err != nil {
		return err
	}

	// The promise is what the user RECEIVES, so it is denominated in the
	// destination token's base units. Source and destination precision differ
	// (BSC stables are 18dp against 6dp on base/polygon), and comparing a
	// promise in source units against a landing in destination units would make
	// every cross-precision route settle wrong by twelve orders of magnitude.
	dst, err := o.destination(order)
	if err != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_destination_total=1 order=%s err=%v", order.ID, err)
		// Holder is already Treasury as of the write above — toRecovery only
		// touches status/fault/failure_reason, so the park below correctly
		// records money the treasury verifiably holds, not money that looks
		// unclaimed.
		return o.toRecovery(ctx, order, err.Error(), nil)
	}
	// ONLY a stablecoin destination gets a promise here. Every other
	// destination's floor is LiFi's own toAmountMin, which does not exist
	// until the route is priced — startBridge writes it there, before the
	// broadcast. Stating a promise now in a volatile token would be a
	// guarantee against a price nobody has quoted.
	if dst.stable {
		var promise *big.Int
		if q, ok := new(big.Int).SetString(order.QuotedAmount, 10); ok {
			promise = StablePromise(q, srcDecimals, dst.decimals)
		} else { // legacy order: no quoted amount was ever recorded
			promise = PromisedAmount(rescaleUnits(received, srcDecimals, dst.decimals), order.MarkupBps)
		}
		log.Printf("[ramp-conversion] order=%s custody=treasury tx=%s amount=%s promise=%s (%ddp->%ddp)",
			order.ID, order.TxHash, received, promise, srcDecimals, dst.decimals)
		return o.setOrder(ctx, order, map[string]any{"promised_amount": promise.String()})
	}
	log.Printf("[ramp-conversion] order=%s custody=treasury tx=%s amount=%s owed=%s dest=%s on %s (non-stable: the promise is the route's own minimum)",
		order.ID, order.TxHash, received, order.QuotedAmount, dst.symbol, dst.chainKey)
	return nil
}

// claimedFromDelivery totals what other converted buys have already taken
// custody of from this order's delivery transaction, in the source token's
// base units. A claim is custody actually recorded — holder moved off
// Paycrest, to the treasury or on to the user — for the same token on the
// same chain; an order still waiting on Paycrest has claimed nothing, and a
// direct order was paid to the user's wallet, never into the treasury's
// receipt. A claim survives settlement, so a delivered order's transaction
// can never be reused.
//
// A read-then-write with no lock: the orchestrator runs as one scheduler
// worker that never overlaps itself, in one process. A second process
// advancing orders would need a lock here.
var errUnreadableClaim = errors.New("conversion: unreadable claim on a delivery")

func (o *ConversionOrchestrator) claimedFromDelivery(ctx context.Context, order *models.PaycrestOrderRecord, decimals int) (*big.Int, error) {
	var others []models.PaycrestOrderRecord
	err := o.db.WithContext(ctx).
		Where("tx_hash = ? AND chain_key = ? AND token = ? AND route_kind = ? AND direction = ? AND id <> ? AND holder IN ?",
			order.TxHash, order.ChainKey, order.Token, models.RouteKindConverted, models.PaycrestDirectionBuy, order.ID,
			[]string{models.HolderTreasury, models.HolderUser}).
		Find(&others).Error
	if err != nil {
		return nil, fmt.Errorf("conversion: read claims on delivery %s: %w", order.TxHash, err)
	}
	claimed := new(big.Int)
	for _, other := range others {
		amount, err := DecimalToBaseUnits(other.TokenAmount, decimals)
		if err != nil {
			// An unreadable claim is still a claim. Treating it as zero would
			// hand its share of the delivery to this order.
			return nil, fmt.Errorf("%w: order %s claims delivery %s with amount %q: %v",
				errUnreadableClaim, other.ID, order.TxHash, other.TokenAmount, err)
		}
		claimed.Add(claimed, amount)
	}
	return claimed, nil
}

// errIfOutOfGas passes on only the one failure an operator must act on: a
// treasury lane that cannot pay for gas. Returned from a step, it fails the
// tick and trips the worker's failure-streak alert; every other refused
// route just frees the attempt for the next tick.
func errIfOutOfGas(err error) error {
	if errors.Is(err, ErrRelayerUnderfunded) {
		return err
	}
	return nil
}

// startBridge reserves the attempt FIRST, then quotes, then broadcasts. The
// quote is a network round-trip long enough for a second tick to enter this
// function, so reserving after it would let both ticks broadcast; reserving
// before it means the loser never even asks for a route.
func (o *ConversionOrchestrator) startBridge(ctx context.Context, order *models.PaycrestOrderRecord, lane *TreasuryLane) error {
	// order.TokenAmount is a plain human-readable decimal string (Paycrest's
	// own order.Amount, exactly as CreateBuy writes it and the app renders
	// it) — never base units. It must be read with the SOURCE token's own
	// decimals, exactly like takeCustody's own short-delivery check.
	srcDecimals, err := lane.Native.GetTokenDecimals(lane.tokenAddress(order.Token))
	if err != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_decimals_total=1 order=%s token=%s chain=%s err=%v",
			order.ID, order.Token, lane.ChainKey, err)
		return o.toRecovery(ctx, order,
			fmt.Sprintf("could not read decimals of %s on %s: %v", order.Token, lane.ChainKey, err), nil)
	}
	amount, err := DecimalToBaseUnits(order.TokenAmount, srcDecimals)
	if err != nil {
		// Same reasoning as the unreadable-promise branch just below: a bare
		// error here would repeat on every tick forever with no leg for the
		// stall detector to ever notice. Park it instead.
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_amount_total=1 order=%s token_amount=%q err=%v",
			order.ID, order.TokenAmount, err)
		return o.toRecovery(ctx, order,
			fmt.Sprintf("unreadable order amount %q: %v", order.TokenAmount, err), nil)
	}
	// Resolved before anything is reserved: an unroutable destination is a
	// configuration fault that will never fix itself, and burning the whole
	// retry window rediscovering that just delays the human. It is also what
	// decides whether there is a promise to read at all, so it comes first.
	dst, err := o.destination(order)
	if err != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_destination_total=1 order=%s err=%v", order.ID, err)
		return o.toRecovery(ctx, order, err.Error(), nil)
	}
	// Only a stablecoin destination carries a promise at this point;
	// takeCustody leaves every other one blank on purpose, and the route's own
	// toAmountMin becomes the floor further down.
	var promised *big.Int
	if dst.stable {
		var ok bool
		if promised, ok = new(big.Int).SetString(order.PromisedAmount, 10); !ok {
			// Defence in depth: takeCustody now parks rather than leaving this
			// state behind (see its own decimals-read failure handling), but if
			// anything else ever left an in-custody order with no readable
			// promise (a lost write, a corrupted row), a bare error here would
			// repeat on every tick forever — a leg-less order is invisible to
			// the stall detector (afterStep returns early when len(legs)==0),
			// so nothing else would ever surface it. Park it instead.
			log.Printf("[ramp-conversion] ALERT ramp_conversion_no_promise_total=1 order=%s promise=%q", order.ID, order.PromisedAmount)
			return o.toRecovery(ctx, order,
				fmt.Sprintf("unreadable promise %q for order %s", order.PromisedAmount, order.ID), nil)
		}
	}
	// Caps gate NEW exposure, and this is the only path that creates any. A
	// bare error here (the kill switch, or the per-order/per-chain cap) is
	// the same silent-permanent-stall shape as an unresolvable treasury/lane
	// in Advance: Holder is already Treasury, zero legs exist yet, so
	// afterStep's stall detector never fires and nothing ever surfaces this
	// order again. Park it — an operator can always retry once the cap
	// frees up or the kill switch lifts; invisible is worse than requiring
	// a nudge.
	if err := o.checkCaps(ctx, lane, order, order.Token, amount); err != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_caps_check_total=1 order=%s err=%v", order.ID, err)
		return o.toRecovery(ctx, order, fmt.Sprintf("cannot start the bridge: %v", err), nil)
	}

	attempt, err := o.legs.NextAttempt(ctx, order.ID, models.RampLegKindBridge)
	if err != nil {
		return err
	}
	leg := &models.RampLeg{
		OrderID: order.ID, Kind: models.RampLegKindBridge, Attempt: attempt,
		ChainKey: lane.ChainKey, Token: strings.ToUpper(order.Token),
		FromAddr: o.treasury.Address(), ToAddr: order.WalletAddress,
		Amount: amount.String(), Status: models.RampLegStatusPending,
		// Stamped from the orchestrator's own clock, not the database's: this
		// row is the anchor for the retry window, so the whole state machine
		// has to agree on what time it is.
		CreatedAt: o.now().UTC(),
	}
	existing, err := o.legs.Reserve(ctx, leg)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil // another tick already claimed this attempt
	}

	// A legacy order (no quoted amount was ever recorded) bridges the FULL
	// delivery against the promise it was written with — exactly today's
	// behaviour, which its stored promise is sized for.
	legacy := order.QuotedAmount == ""
	// A volatile destination needs real slippage room; stable-to-stable does
	// not, and quoting it loose would only widen the gap we then have to
	// honour.
	slippage := 0.005
	if dst.stable {
		slippage = 0.001
	}
	recipient := firstNonEmpty(order.DestinationAddress, order.WalletAddress)
	lreq := LiFiQuoteRequest{
		FromChainID: lane.ChainID, ToChainID: dst.chainID,
		FromToken: lane.tokenAddress(order.Token), ToToken: dst.token,
		FromAddress: o.treasury.Address(),
		ToAddress:   recipient, // LiFi delivers straight to the user
		Slippage:    slippage,
	}
	bridge := amount // legacy: the full delivery
	var route *LiFiRoute
	if legacy {
		lreq.FromAmount = bridge.String()
		if route, err = o.lifi.Quote(ctx, lreq); err != nil {
			// Nothing was broadcast, so this attempt is provably dead: fail it
			// so the next tick starts a clean attempt inside the window. The
			// error is still returned, so a LiFi outage reaches the worker's
			// failure-streak alert rather than looking like a quiet no-op.
			if ferr := o.failLeg(detached(ctx), leg.ID, err.Error()); ferr != nil {
				return ferr
			}
			return err
		}
	} else {
		// She is owed T; the treasury keeps the rest. Sending T/(1 − l) means
		// LiFi delivers about T in value, so the difference stays behind as
		// USDC rather than being handed to the bridge. l has to be measured on
		// a route of about the right size, hence the probe: LiFi's cost is not
		// linear in the amount.
		entitled, _ := new(big.Int).SetString(order.QuotedAmount, 10) // already scaled at custody
		lreq.FromAmount = entitled.String()
		probe, perr := o.lifi.Quote(ctx, lreq)
		if perr != nil {
			if ferr := o.failLeg(detached(ctx), leg.ID, perr.Error()); ferr != nil {
				return ferr
			}
			return perr
		}
		l, ok := LiFiCostBps(probe)
		b, fits := BridgeAmount(entitled, amount, l)
		if !ok || !fits {
			log.Printf("[ramp-conversion] order=%s bridge#%d route no longer fits: owed=%s received=%s lifi_bps=%d priced=%v",
				order.ID, attempt, entitled, amount, l, ok)
			return o.failLeg(detached(ctx), leg.ID, "the route no longer fits what was received")
		}
		lreq.FromAmount = b.String()
		if route, err = o.lifi.Quote(ctx, lreq); err != nil {
			if ferr := o.failLeg(detached(ctx), leg.ID, err.Error()); ferr != nil {
				return ferr
			}
			return err
		}
		// The whole round trip — LiFi's own cost plus the gas the treasury
		// pays — has to fit inside B, the slice of the user-facing markup this
		// conversion was priced on. Over it, the attempt is freed rather than
		// executed at a loss; the retry window is already running from attempt
		// 1, so a route that never comes back into budget refunds on its own.
		if l2, ok := LiFiCostBps(route); !ok || l2+TreasuryGasBps(route, treasuryERC20Gas) > o.budgetBps {
			return o.failLeg(detached(ctx), leg.ID,
				fmt.Sprintf("round trip over the %d bps budget", o.budgetBps))
		}
		bridge = b
		// The leg was reserved before the route was priced (that ordering is
		// what stops two ticks broadcasting), so its amount is only correct
		// now — and it must be correct BEFORE the broadcast, because it is
		// what every later reader treats as what we sent.
		if err := o.legs.SetAmount(detached(ctx), leg.ID, bridge.String()); err != nil {
			return err
		}
	}
	switch {
	case dst.stable:
		// Decision 5, applied at execution time: a route whose guaranteed
		// minimum is below the promise cannot honour it, so it is not a route
		// we may take.
		if route.ToAmountMin == nil || route.ToAmountMin.Cmp(promised) < 0 {
			log.Printf("[ramp-conversion] order=%s bridge#%d rejected route: toAmountMin=%v < promise=%s",
				order.ID, attempt, route.ToAmountMin, promised)
			return o.failLeg(detached(ctx), leg.ID, "route guarantees less than the promised amount")
		}
	case route.ToAmountMin == nil:
		// Nothing to promise against, and nothing to settle a landing with.
		return o.failLeg(detached(ctx), leg.ID, "route quotes no guaranteed minimum")
	default:
		// A non-stable destination's promise IS the route's own minimum, and
		// it is written BEFORE the broadcast: a landing that arrives with
		// nothing on record to compare it against could never be settled, and
		// it is never topped up afterwards.
		if err := o.setOrder(ctx, order, map[string]any{"promised_amount": route.ToAmountMin.String()}); err != nil {
			return err
		}
	}
	// The route's `to` is allowlisted inside ExecuteRoute, but the calldata
	// alone decides who the diamond pays. Checked here, where the order's
	// wallet and destination are known — LiFi's status would only report a
	// wrong landing after the treasury's funds had already left.
	if err := checkLiFiDelivery(route.Data, lane.ChainID, dst.chainID, recipient); err != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_route_delivery_refused_total=1 order=%s bridge#%d tool=%s err=%v",
			order.ID, attempt, route.Tool, err)
		return o.failLeg(detached(ctx), leg.ID, err.Error())
	}

	txHash, broadcastAttempted, err := o.treasury.ExecuteRoute(ctx, lane, order.Token, bridge, route)
	if err != nil {
		if broadcastAttempted {
			// Might have landed: keep the leg pending so no second route runs.
			log.Printf("[ramp-conversion] ALERT ramp_conversion_broadcast_uncertain_total=1 order=%s leg=%s err=%v",
				order.ID, leg.ID, err)
			return nil
		}
		if ferr := o.failLeg(detached(ctx), leg.ID, err.Error()); ferr != nil {
			return ferr
		}
		return errIfOutOfGas(err)
	}
	// Detached: the broadcast already happened, so losing this write to a
	// cancelled request would strand the leg with no hash.
	if err := o.legs.SetTxHash(detached(ctx), leg.ID, txHash, route.Tool); err != nil {
		return err
	}
	log.Printf("[ramp-conversion] order=%s bridge#%d chain=%s->%s token=%s sent=%s of %s tx=%s tool=%s",
		order.ID, attempt, lane.ChainKey, dst.chainKey, dst.symbol, bridge, amount, txHash, route.Tool)
	return nil
}

// observeBridge asks LiFi what became of a pending bridge, and nothing else.
//
// LiFi is the LOOKUP, never the proof — the same rule the IT sell path states.
// In particular a FAILED verdict on a transaction that DID broadcast is not
// permission to send again: LiFi reports FAILED for tool stalls and
// source-side ambiguity while the funds can still reach the user later.
func (o *ConversionOrchestrator) observeBridge(ctx context.Context, order *models.PaycrestOrderRecord, leg *models.RampLeg) error {
	if leg.TxHash == "" {
		// Reserved, but no broadcast outcome was ever recorded. There is
		// nothing to ask LiFi about.
		if o.now().After(leg.CreatedAt.Add(o.retryWindow)) {
			log.Printf("[ramp-conversion] ALERT ramp_conversion_leg_hashless_total=1 order=%s leg=%s attempt=%d",
				order.ID, leg.ID, leg.Attempt)
		}
		return nil
	}
	st, err := o.status.Status(ctx, leg.TxHash)
	if err != nil {
		return fmt.Errorf("conversion: bridge status for order %s: %w", order.ID, err)
	}
	switch st.Status {
	case LiFiStatusDone:
		return o.observeDone(ctx, order, leg, st)

	case LiFiStatusFailed:
		log.Printf("[ramp-conversion] ALERT ramp_conversion_bridge_failed_total=1 order=%s bridge#%d tx=%s",
			order.ID, leg.Attempt, leg.TxHash)
		return o.toRecovery(ctx, order,
			fmt.Sprintf("lifi reported bridge attempt %d (%s) failed after it broadcast", leg.Attempt, leg.TxHash), nil)

	default:
		return nil // still in flight
	}
}

// observeDone decides whether a DONE report is good enough to settle on.
// Several things have to hold, because "DONE" alone says nothing about who was
// paid or how much — and the last of them is the chain itself. LiFi is the
// LOOKUP that tells us WHICH transaction to go and read; the ERC-20 Transfer
// log in that transaction is the only thing that says what the user received.
func (o *ConversionOrchestrator) observeDone(ctx context.Context, order *models.PaycrestOrderRecord, leg *models.RampLeg, st *LiFiTransferStatus) error {
	switch st.Substatus {
	case LiFiSubstatusCompleted:
		// The only substatus that means the recipient got what was quoted.
	case LiFiSubstatusPartial, LiFiSubstatusRefunded:
		log.Printf("[ramp-conversion] ALERT ramp_conversion_bridge_not_completed_total=1 order=%s bridge#%d tx=%s substatus=%s",
			order.ID, leg.Attempt, leg.TxHash, st.Substatus)
		return o.toRecovery(ctx, order,
			fmt.Sprintf("bridge %s reported DONE/%s, not a completed delivery", leg.TxHash, st.Substatus), nil)
	default:
		log.Printf("[ramp-conversion] ALERT ramp_conversion_done_unqualified_total=1 order=%s tx=%s substatus=%q",
			order.ID, leg.TxHash, st.Substatus)
		return nil // never settle on a DONE we cannot qualify
	}
	if st.ReceivedBaseUnits == nil || st.ReceivedBaseUnits.Sign() <= 0 {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_done_without_amount_total=1 order=%s tx=%s", order.ID, leg.TxHash)
		return nil
	}
	// An unstated recipient is not the user's wallet; it is an unknown, and an
	// unknown recipient must never settle an order.
	if st.ToAddress == "" {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_done_without_recipient_total=1 order=%s tx=%s", order.ID, leg.TxHash)
		return nil
	}
	// The delivery target, not necessarily the account's wallet: they are the
	// same value today, but the route was quoted and the calldata checked
	// against DestinationAddress, so the landing is held to that same address.
	recipient := firstNonEmpty(order.DestinationAddress, order.WalletAddress)
	if !strings.EqualFold(st.ToAddress, recipient) {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_wrong_recipient_total=1 order=%s tx=%s reported=%s wallet=%s",
			order.ID, leg.TxHash, st.ToAddress, recipient)
		return fmt.Errorf("conversion: bridge for order %s reported a landing at %s, not the user's wallet", order.ID, st.ToAddress)
	}

	landed, err := o.verifyLanding(ctx, order, leg, st)
	if err != nil || landed == nil {
		return err // alerted inside; nothing settles on an unverified landing
	}
	if err := o.legs.Confirm(detached(ctx), leg.ID, landed.String(), o.now()); err != nil {
		if errors.Is(err, ErrLegNotPending) {
			return nil // another tick confirmed it and will settle it
		}
		return err
	}
	log.Printf("[ramp-conversion] order=%s bridge#%d landed tx=%s dest_tx=%s amount=%s (verified on-chain)",
		order.ID, leg.Attempt, leg.TxHash, st.DestTxHash, landed)

	confirmed := *leg
	confirmed.Status = models.RampLegStatusConfirmed
	confirmed.AmountReceived = landed.String()
	return o.settleOrTopUp(ctx, order, &confirmed)
}

// verifyOnDestination reads ONE transaction on the destination chain and
// returns what the order's wallet was actually credited by it.
//
//	(amount, nil) — proven; this is the only shape that may settle anything
//	(nil, nil)    — cannot be established YET; wait and look again (alerted)
//	(nil, err)    — parked for a human, or a real error
//
// It is the single gate for "never settle on a number nobody checked", shared
// by the bridge landing and the top-up, so neither can drift from the other.
func (o *ConversionOrchestrator) verifyOnDestination(ctx context.Context, order *models.PaycrestOrderRecord, dst *conversionDestination, txHash, stage string) (*big.Int, error) {
	return o.verifyTransferOnChain(ctx, order, dst.chainKey, dst.token, txHash, stage)
}

// verifyTransferOnChain reads ONE transaction on the given chain and returns
// what the order's own wallet was actually credited by it.
//
//	(amount, nil) — proven; this is the only shape that may settle anything
//	(nil, nil)    — cannot be established YET; wait and look again (alerted)
//	(nil, err)    — parked for a human, or a real error
//
// This is the single gate for "never settle on a number nobody checked",
// shared by the bridge landing, the top-up (both via verifyOnDestination,
// which always asks about order.RequestedChain), AND the refund (via
// observeRefund, which asks about the ORIGIN chain instead) — so none of the
// three can drift from one another in how "proven" is decided. A refund
// pays the user's own wallet exactly like the other two; it earns exactly
// the same proof.
func (o *ConversionOrchestrator) verifyTransferOnChain(ctx context.Context, order *models.PaycrestOrderRecord, chainKey, tokenAddr, txHash, stage string) (*big.Int, error) {
	if txHash == "" {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_dest_tx_total=1 order=%s stage=%s", order.ID, stage)
		return nil, nil
	}
	verifier := o.verifierFor(chainKey)
	if verifier == nil {
		// Same reasoning as taking custody: with no verifier the alternative is
		// settling on a number nobody checked.
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_verifier_total=1 order=%s chain=%s stage=%s",
			order.ID, chainKey, stage)
		return nil, nil
	}
	credited, err := verifier.VerifyTransfer(ctx, txHash, tokenAddr, order.WalletAddress)
	if err != nil {
		// A REVERTED transfer, or one that paid someone else, is a settled
		// question with a bad answer — it will never become true by waiting, so
		// polling it until the stall deadline just costs hours before a human
		// hears about it.
		//
		// NOTE: LogTransferVerifier never actually returns ErrTransferWrongRecipient.
		// It filters the Transfer logs by recipient and reports a landing that
		// paid someone else as ErrTransferNotFound — which lands in the "wait
		// and look again" branch below, and is then bounded by legCeiling. The
		// ErrTransferWrongRecipient arm is defensive only, for a future
		// verifier that does distinguish the two.
		if errors.Is(err, ErrTransferReverted) || errors.Is(err, ErrTransferWrongRecipient) {
			log.Printf("[ramp-conversion] ALERT ramp_conversion_dest_transfer_bad_total=1 order=%s stage=%s tx=%s err=%v",
				order.ID, stage, txHash, err)
			return nil, o.toRecovery(ctx, order,
				fmt.Sprintf("%s transfer %s did not credit the user: %v", stage, txHash, err), nil)
		}
		// Not mined yet, or not readable. The next tick looks again.
		log.Printf("[ramp-conversion] ALERT ramp_conversion_dest_unverified_total=1 order=%s stage=%s tx=%s err=%v",
			order.ID, stage, txHash, err)
		return nil, nil
	}
	if credited == nil || credited.Sign() <= 0 {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_dest_zero_total=1 order=%s stage=%s tx=%s", order.ID, stage, txHash)
		return nil, nil
	}
	return credited, nil
}

// verifyLanding proves the bridge landing LiFi named. LiFi is the LOOKUP that
// says WHICH transaction to read; the ERC-20 Transfer log in it is the only
// thing that says what the user received.
func (o *ConversionOrchestrator) verifyLanding(ctx context.Context, order *models.PaycrestOrderRecord, leg *models.RampLeg, st *LiFiTransferStatus) (*big.Int, error) {
	dst, err := o.destination(order)
	if err != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_destination_total=1 order=%s err=%v", order.ID, err)
		return nil, o.toRecovery(ctx, order, err.Error(), nil)
	}
	if !dst.stable && strings.EqualFold(dst.token, evmNativeAddress) {
		// A native coin leaves no Transfer log: LiFi's status is the signal,
		// held to the right token here and to LiFi's own minimum in settle.
		// Every ERC-20 — stable or not — still earns the on-chain proof below.
		if !strings.EqualFold(st.ReceivingToken, dst.token) {
			log.Printf("[ramp-conversion] ALERT ramp_conversion_wrong_token_total=1 order=%s got=%s want=%s", order.ID, st.ReceivingToken, dst.token)
			return nil, o.toRecovery(ctx, order, fmt.Sprintf("LiFi delivered %s, not %s", st.ReceivingToken, dst.token), map[string]any{"holder": models.HolderUser})
		}
		return st.ReceivedBaseUnits, nil
	}
	credited, err := o.verifyTransferOnChain(ctx, order, dst.chainKey, dst.token, st.DestTxHash, "landing")
	if err != nil || credited == nil {
		return nil, err
	}
	// The chain outranks LiFi. Where they disagree, what the wallet was credited
	// is what the user has, and the shortfall logic works from that.
	if st.ReceivedBaseUnits != nil && credited.Cmp(st.ReceivedBaseUnits) != 0 {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_landing_mismatch_total=1 order=%s dest_tx=%s lifi=%s chain=%s",
			order.ID, st.DestTxHash, st.ReceivedBaseUnits, credited)
	}
	return credited, nil
}

// settleOrTopUp compares what the user actually holds against the promise
// (base units, never floats) and makes up ANY difference from margin. A user
// who was promised an amount gets that amount; the size of the gap changes who
// gets told about it, not whether they are paid.
func (o *ConversionOrchestrator) settleOrTopUp(ctx context.Context, order *models.PaycrestOrderRecord, bridge *models.RampLeg) error {
	promised, ok := new(big.Int).SetString(order.PromisedAmount, 10)
	if !ok {
		return fmt.Errorf("conversion: unreadable promise %q for order %s", order.PromisedAmount, order.ID)
	}
	// What the user is still short is a question about the WHOLE ledger, not
	// about the bridge alone. A top-up that landed but whose settle write was
	// lost has already made them whole; recomputing from the bridge would pay
	// the same shortfall again on the next tick.
	legs, err := o.legs.ForOrder(ctx, order.ID)
	if err != nil {
		return err
	}
	shortfall := new(big.Int).Sub(promised, creditedToUser(order, legs))
	if shortfall.Sign() <= 0 {
		log.Printf("[ramp-conversion] order=%s settled via bridge#%d tx=%s promised=%s",
			order.ID, bridge.Attempt, bridge.TxHash, promised)
		return o.setOrder(ctx, order, map[string]any{
			"status": models.PaycrestStatusSettled, "holder": models.HolderUser,
		})
	}

	// A non-stable destination is never topped up: the promise it fell short
	// of is LiFi's OWN guaranteed minimum, which a route that delivered less
	// than it guaranteed has already broken, and the treasury holds no float
	// in the token to make up the difference with. A human decides.
	if dst, derr := o.destination(order); derr == nil && !dst.stable {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_below_lifi_min_total=1 order=%s shortfall=%s", order.ID, shortfall)
		return o.toRecovery(ctx, order, "landed below LiFi's own minimum", map[string]any{"holder": models.HolderUser})
	}

	// A top-up already in flight may not be joined by a second one. Its amount
	// is NOT counted above — creditedToUser counts confirmed legs only, and a
	// deliver leg now stays pending across ticks until its receipt is readable
	// — so recomputing the gap here would broadcast the same top-up again.
	// Counting unconfirmed top-ups as credited would be the other way to close
	// this, and it is the wrong one: a top-up that later fails would leave the
	// user permanently short. Wait instead; observeDeliver finishes this leg.
	if inFlight := latestLeg(legs, models.RampLegKindDeliver, models.RampLegStatusPending); inFlight != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_topup_in_flight_total=1 order=%s deliver#%d tx=%s shortfall=%s",
			order.ID, inFlight.Attempt, inFlight.TxHash, shortfall)
		return nil
	}

	missBps := new(big.Int).Quo(new(big.Int).Mul(shortfall, big.NewInt(10_000)), promised)
	if missBps.Int64() > shortfallToleranceBps {
		// Still topped up below — the user is made whole either way — but this
		// is margin we did not plan to spend, and finance has to see it.
		log.Printf("[ramp-conversion] ALERT ramp_conversion_promise_missed_total=1 order=%s bridge#%d tx=%s shortfall=%s promised=%s miss_bps=%s",
			order.ID, bridge.Attempt, bridge.TxHash, shortfall, promised, missBps)
	}

	dst, err := o.destination(order)
	if err != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_topup_lane_total=1 order=%s err=%v", order.ID, err)
		return o.toRecovery(ctx, order, err.Error(), map[string]any{"holder": models.HolderUser})
	}
	if usd := unitsToUSD(shortfall, dst.decimals); usd > o.maxShortfallUSD {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_shortfall_over_ceiling_total=1 order=%s bridge#%d tx=%s shortfall=%s (~$%.2f) ceiling=$%.2f",
			order.ID, bridge.Attempt, bridge.TxHash, shortfall, usd, o.maxShortfallUSD)
		return o.toRecovery(ctx, order,
			fmt.Sprintf("shortfall of ~$%.2f exceeds the $%.2f automatic top-up ceiling", usd, o.maxShortfallUSD),
			map[string]any{"holder": models.HolderUser})
	}
	return o.deliverShortfall(ctx, order, dst, shortfall)
}

// deliverShortfall pays the gap from the treasury in the token the user
// actually received, on the chain they received it — which is the REQUESTED
// chain/token, not the one Paycrest delivered to us.
//
// No cap check: a top-up completes an order the treasury is already exposed to.
// It creates no new exposure, and refusing it would leave the user short.
func (o *ConversionOrchestrator) deliverShortfall(ctx context.Context, order *models.PaycrestOrderRecord, dst *conversionDestination, shortfall *big.Int) error {
	// The treasury holds no float in an arbitrary token, so there is nothing
	// to pay a gap in. settleOrTopUp already parks a short non-stable landing
	// before it gets here; this is the same refusal stated where the money
	// would actually move, so no future caller can reach a nil lane.
	if !dst.stable || dst.lane == nil {
		return o.toRecovery(ctx, order, "no top-up exists for a non-stable destination", map[string]any{"holder": models.HolderUser})
	}
	if err := o.treasury.requireGas(ctx, dst.lane, treasuryERC20Gas); err != nil {
		return err
	}
	attempt, err := o.legs.NextAttempt(ctx, order.ID, models.RampLegKindDeliver)
	if err != nil {
		return err
	}
	leg := &models.RampLeg{
		OrderID: order.ID, Kind: models.RampLegKindDeliver, Attempt: attempt,
		ChainKey: dst.lane.ChainKey, Token: dst.symbol,
		FromAddr: o.treasury.Address(), ToAddr: order.WalletAddress,
		Amount: shortfall.String(), Status: models.RampLegStatusPending,
		CreatedAt: o.now().UTC(),
	}
	existing, err := o.legs.Reserve(ctx, leg)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}
	txHash, attempted, err := o.treasury.TransferToken(ctx, dst.lane, dst.symbol, order.WalletAddress, shortfall)
	if err != nil {
		if attempted {
			log.Printf("[ramp-conversion] ALERT ramp_conversion_broadcast_uncertain_total=1 order=%s leg=%s err=%v", order.ID, leg.ID, err)
			return nil
		}
		return o.failLeg(detached(ctx), leg.ID, err.Error())
	}
	if err := o.legs.SetTxHash(detached(ctx), leg.ID, txHash, ""); err != nil {
		return err
	}
	log.Printf("[ramp-conversion] order=%s deliver#%d chain=%s token=%s amount=%s tx=%s",
		order.ID, attempt, dst.lane.ChainKey, dst.symbol, shortfall, txHash)
	leg.TxHash = txHash
	// The broadcast is not the proof. If the receipt is not readable yet the leg
	// stays pending and observeDeliver finishes it on a later tick.
	return o.settleTopUp(ctx, order, dst, leg)
}

// observeDeliver resolves a top-up leg we already broadcast. It exists because
// a top-up, like a bridge, is only finished when the destination chain says so
// — and a receipt is rarely readable in the same instant we send.
func (o *ConversionOrchestrator) observeDeliver(ctx context.Context, order *models.PaycrestOrderRecord, leg *models.RampLeg) error {
	if leg.TxHash == "" {
		// Reserved, but no broadcast outcome was ever recorded. Nothing to read;
		// the stall deadline is what eventually surfaces this.
		log.Printf("[ramp-conversion] ALERT ramp_conversion_leg_stalled_total=1 order=%s leg=%s kind=%s attempt=%d",
			order.ID, leg.ID, leg.Kind, leg.Attempt)
		return nil
	}
	dst, err := o.destination(order)
	if err != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_destination_total=1 order=%s err=%v", order.ID, err)
		return o.toRecovery(ctx, order, err.Error(), nil)
	}
	return o.settleTopUp(ctx, order, dst, leg)
}

// settleTopUp confirms a top-up leg on the strength of the destination chain
// and settles the order. Unverified means wait — never settle.
func (o *ConversionOrchestrator) settleTopUp(ctx context.Context, order *models.PaycrestOrderRecord, dst *conversionDestination, leg *models.RampLeg) error {
	credited, err := o.verifyOnDestination(ctx, order, dst, leg.TxHash, "topup")
	if err != nil || credited == nil {
		return err // alerted inside; the leg stays pending for the next tick
	}
	if err := o.legs.Confirm(detached(ctx), leg.ID, credited.String(), o.now()); err != nil {
		if errors.Is(err, ErrLegNotPending) {
			return nil // another tick confirmed it and will settle it
		}
		return err
	}
	log.Printf("[ramp-conversion] order=%s deliver#%d confirmed tx=%s credited=%s (verified on-chain)",
		order.ID, leg.Attempt, leg.TxHash, credited)

	// The same question settleOrTopUp asks, asked again here: a credit that is
	// POSITIVE is not necessarily a SUFFICIENT one. A top-up that delivered
	// less than it was sent for leaves the user short, and settling on it would
	// close the order against them.
	promised, ok := new(big.Int).SetString(order.PromisedAmount, 10)
	if !ok {
		return fmt.Errorf("conversion: unreadable promise %q for order %s", order.PromisedAmount, order.ID)
	}
	legs, err := o.legs.ForOrder(ctx, order.ID)
	if err != nil {
		return err
	}
	if outstanding := new(big.Int).Sub(promised, creditedToUser(order, legs)); outstanding.Sign() > 0 {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_topup_short_total=1 order=%s deliver#%d tx=%s credited=%s outstanding=%s",
			order.ID, leg.Attempt, leg.TxHash, credited, outstanding)
		// No status write: the order stays open and the next tick tops up what
		// is still owed. setOrder with no columns of its own still recomputes
		// owed_to_user, so the remaining balance is on the record meanwhile.
		return o.setOrder(detached(ctx), order, map[string]any{})
	}
	// Detached: the top-up is already on-chain, so this write must not be lost
	// with a cancelled request.
	return o.setOrder(detached(ctx), order, map[string]any{
		"status": models.PaycrestStatusSettled, "holder": models.HolderUser,
	})
}

// observeRefund resolves a refund leg that has already been broadcast — by
// the automatic refund() below, or by RefundFromTreasury's admin action.
//
// A refund is, in every way that matters, just another payment to the
// user's own wallet: it earns exactly the same on-chain proof the bridge
// landing and the top-up already require, never less because it happens to
// be the recovery path. Confirming (and closing the order) on broadcast
// alone was the one remaining asymmetry Task 7's own on-chain-verification
// ruling forbade — a reverted or dropped refund transfer would otherwise
// leave a "confirmed" leg and a terminal "refunded" order recording a debt
// that was never actually paid, with owedFromLegs reporting "0" and
// NeedsOperator() false: every recovery tool would believe the debt was
// settled while the user got nothing.
func (o *ConversionOrchestrator) observeRefund(ctx context.Context, order *models.PaycrestOrderRecord, leg *models.RampLeg) error {
	if leg.TxHash == "" {
		// Reserved, but no broadcast outcome was ever recorded. Nothing to
		// verify yet; the stall deadline is what eventually surfaces this.
		log.Printf("[ramp-conversion] ALERT ramp_conversion_leg_stalled_total=1 order=%s leg=%s kind=%s attempt=%d",
			order.ID, leg.ID, leg.Kind, leg.Attempt)
		return nil
	}
	lane := o.treasury.Lane(leg.ChainKey)
	if lane == nil {
		return ErrSponsorNotAvailable
	}
	tokenAddr := lane.tokenAddress(leg.Token)
	if tokenAddr == "" {
		return fmt.Errorf("conversion: token %s is not configured on %s", leg.Token, leg.ChainKey)
	}
	// A refund leg is denominated in its OWN Token/ChainKey (the origin the
	// treasury actually paid from) — unlike a bridge leg, there is no
	// origin/destination split to resolve here; see RampLeg.AmountReceived's
	// own doc comment for that distinction.
	credited, err := o.verifyTransferOnChain(ctx, order, leg.ChainKey, tokenAddr, leg.TxHash, "refund")
	if err != nil || credited == nil {
		return err // alerted inside; the leg stays pending, or the order was parked
	}
	if err := o.legs.Confirm(detached(ctx), leg.ID, credited.String(), o.now()); err != nil {
		if errors.Is(err, ErrLegNotPending) {
			return nil // another tick (or admin call) already confirmed it
		}
		return err
	}
	log.Printf("[ramp-conversion] order=%s refund#%d confirmed tx=%s credited=%s (verified on-chain)",
		order.ID, leg.Attempt, leg.TxHash, credited)
	return o.finishRefund(detached(ctx), order, order.Fault)
}

// refund returns the FULL Paycrest amount — markup included — in the token the
// treasury actually holds, on the chain Paycrest delivered to, to the wallet
// the order itself names.
//
// No cap check: a refund returns money the treasury already holds.
func (o *ConversionOrchestrator) refund(ctx context.Context, order *models.PaycrestOrderRecord, lane *TreasuryLane, fault string) error {
	// order.TokenAmount is a plain human-readable decimal string (see
	// startBridge's own doc comment) — never base units.
	srcDecimals, err := lane.Native.GetTokenDecimals(lane.tokenAddress(order.Token))
	if err != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_decimals_total=1 order=%s token=%s chain=%s err=%v",
			order.ID, order.Token, lane.ChainKey, err)
		return o.toRecovery(ctx, order,
			fmt.Sprintf("could not read decimals of %s on %s: %v", order.Token, lane.ChainKey, err), nil)
	}
	amount, err := DecimalToBaseUnits(order.TokenAmount, srcDecimals)
	if err != nil {
		// Same reasoning as startBridge: a bare error here would repeat on
		// every tick forever, invisible to the stall detector. Park it.
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_amount_total=1 order=%s token_amount=%q err=%v",
			order.ID, order.TokenAmount, err)
		return o.toRecovery(ctx, order,
			fmt.Sprintf("unreadable order amount %q: %v", order.TokenAmount, err), nil)
	}
	// A refund that already landed finishes the order. The LEDGER is the record
	// of what moved, not the order's status column: if the "refunded" write was
	// lost, re-paying the full amount would refund the user twice.
	legs, err := o.legs.ForOrder(ctx, order.ID)
	if err != nil {
		return err
	}
	if done := latestLeg(legs, models.RampLegKindRefund, models.RampLegStatusConfirmed); done != nil {
		return o.finishRefund(ctx, order, fault)
	}
	// Mark the intent BEFORE broadcasting: a crash mid-refund must read as
	// "a refund was under way", never as an order still waiting to convert.
	if order.Status != models.PaycrestStatusRefunding {
		if err := o.setOrder(ctx, order, map[string]any{
			"status": models.PaycrestStatusRefunding, "fault": fault,
		}); err != nil {
			return err
		}
	}
	if err := o.treasury.requireGas(ctx, lane, treasuryERC20Gas); err != nil {
		// No leg: one written per tick while the lane is empty would read as
		// fresh activity and hide the order from the stall detector. The
		// error fails the tick, which is what pages someone.
		return err
	}
	attempt, err := o.legs.NextAttempt(ctx, order.ID, models.RampLegKindRefund)
	if err != nil {
		return err
	}
	leg := &models.RampLeg{
		OrderID: order.ID, Kind: models.RampLegKindRefund, Attempt: attempt,
		ChainKey: lane.ChainKey, Token: strings.ToUpper(order.Token),
		FromAddr: o.treasury.Address(), ToAddr: order.WalletAddress,
		Amount: amount.String(), Status: models.RampLegStatusPending,
		CreatedAt: o.now().UTC(),
	}
	existing, err := o.legs.Reserve(ctx, leg)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}
	txHash, attempted, err := o.treasury.TransferToken(ctx, lane, order.Token, order.WalletAddress, amount)
	if err != nil {
		if attempted {
			log.Printf("[ramp-conversion] ALERT ramp_conversion_broadcast_uncertain_total=1 order=%s leg=%s err=%v", order.ID, leg.ID, err)
			return nil
		}
		return o.failLeg(detached(ctx), leg.ID, err.Error())
	}
	if err := o.legs.SetTxHash(detached(ctx), leg.ID, txHash, ""); err != nil {
		return err
	}
	log.Printf("[ramp-conversion] order=%s refund#%d chain=%s token=%s amount=%s tx=%s fault=%s — awaiting on-chain confirmation",
		order.ID, attempt, lane.ChainKey, leg.Token, amount, txHash, fault)
	// Leave the leg PENDING: a broadcast is not proof. observeRefund verifies
	// it on the ORIGIN chain on a LATER tick (Advance's step 2 dispatches a
	// pending refund leg to it) — confirming here on broadcast alone was the
	// one refund-specific asymmetry Task 7's on-chain-verification ruling
	// forbade; see observeRefund's own doc comment for the exact failure it
	// closes.
	return nil
}

// finishRefund closes an order whose refund has provably landed. It is split
// out because it is reached twice: right after the broadcast, and again on a
// later tick if that first write was lost.
func (o *ConversionOrchestrator) finishRefund(ctx context.Context, order *models.PaycrestOrderRecord, fault string) error {
	return o.setOrder(ctx, order, map[string]any{
		"status": models.PaycrestStatusRefunded, "holder": models.HolderUser, "fault": fault,
	})
}

// toRecovery parks an order for a human with the reason recorded and the
// balance still owed computed from the legs. It is the endpoint for everything
// no retry can fix.
func (o *ConversionOrchestrator) toRecovery(ctx context.Context, order *models.PaycrestOrderRecord, reason string, extra map[string]any) error {
	log.Printf("[ramp-conversion] ALERT ramp_conversion_recovery_total=1 order=%s reason=%q", order.ID, reason)
	updates := map[string]any{
		"status":         models.PaycrestStatusRecoveryPending,
		"fault":          models.FaultPlatform,
		"failure_reason": reason,
	}
	for k, v := range extra {
		updates[k] = v
	}
	return o.setOrder(detached(ctx), order, updates)
}

// setOrder writes a narrow set of columns and ALWAYS recomputes owed_to_user
// from the leg ledger — a caller-supplied value is dropped, because what we
// owe is a fact about what moved on-chain, not something anyone gets to state.
func (o *ConversionOrchestrator) setOrder(ctx context.Context, order *models.PaycrestOrderRecord, updates map[string]any) error {
	delete(updates, "owed_to_user")

	next := *order
	if v, ok := updates["status"].(string); ok {
		next.Status = v
	}
	if v, ok := updates["promised_amount"].(string); ok {
		next.PromisedAmount = v
	}
	legs, err := o.legs.ForOrder(ctx, order.ID)
	if err != nil {
		return err
	}
	updates["owed_to_user"] = owedFromLegs(&next, legs)

	if v, ok := updates["status"].(string); ok && v == models.PaycrestStatusSettled {
		now := o.now()
		updates["settled_at"] = &now
	}
	return o.db.WithContext(ctx).Model(order).Updates(updates).Error
}

// owedFromLegs is what we still owe the user, in the destination token's base
// units: the promise less everything that actually reached their wallet.
//
// A refunding order still owes: the refund has not landed yet, and reporting
// zero would hide a live liability for exactly as long as it matters.
//
// An empty string means UNDETERMINED, not zero — see the in-flight case below.
func owedFromLegs(order *models.PaycrestOrderRecord, legs []models.RampLeg) string {
	switch order.Status {
	case models.PaycrestStatusSettled, models.PaycrestStatusRefunded:
		return "0"
	}
	// While a bridge is in flight the liability is genuinely UNDETERMINED: the
	// user may already hold the money, or may never receive it. Reporting the
	// full promise here would be the figure a human pays against, on an order
	// that might already be paid. An empty value is the honest answer, and the
	// failure_reason names the transaction still outstanding.
	if liveBridgeLeg(legs) != nil {
		return ""
	}
	promised, ok := new(big.Int).SetString(order.PromisedAmount, 10)
	if !ok {
		return "0"
	}
	owed := new(big.Int).Sub(promised, creditedToUser(order, legs))
	if owed.Sign() < 0 {
		return "0"
	}
	return owed.String()
}

// creditedToUser is everything the leg ledger says actually reached the user's
// own wallet: confirmed bridge and deliver legs only. It is the single answer
// to "have we paid them yet", so a lost status write cannot turn one payment
// into two.
//
// A confirmed BRIDGE leg with no recorded AmountReceived is skipped: its Amount
// is what we sent in SOURCE units, and counting that as destination units would
// credit money that may never have landed. A confirmed DELIVER leg falls back
// to its Amount, which we denominated in destination units ourselves — skipping
// that one would under-count what the user holds and re-pay them.
func creditedToUser(order *models.PaycrestOrderRecord, legs []models.RampLeg) *big.Int {
	credited := new(big.Int)
	for _, leg := range legs {
		if leg.Status != models.RampLegStatusConfirmed {
			continue
		}
		if leg.Kind != models.RampLegKindBridge && leg.Kind != models.RampLegKindDeliver {
			continue
		}
		if !strings.EqualFold(leg.ToAddr, order.WalletAddress) {
			continue
		}
		raw := leg.AmountReceived
		if raw == "" {
			// A deliver leg's Amount is ALREADY in destination units — we chose
			// it — so it is a safe stand-in and counting it is what stops a
			// re-pay. A bridge leg's Amount is what we SENT in source units,
			// which would credit money that may never have landed, so that one
			// is skipped instead.
			if leg.Kind != models.RampLegKindDeliver {
				log.Printf("[ramp-conversion] ALERT ramp_conversion_leg_without_amount_total=1 order=%s leg=%s kind=%s",
					order.ID, leg.ID, leg.Kind)
				continue
			}
			raw = leg.Amount
		}
		if v, ok := new(big.Int).SetString(raw, 10); ok {
			credited.Add(credited, v)
		}
	}
	return credited
}

// failLeg records a leg as failed, treating "already terminal" as another tick
// having got there first rather than as an error of its own.
func (o *ConversionOrchestrator) failLeg(ctx context.Context, id uuid.UUID, reason string) error {
	if err := o.legs.Fail(ctx, id, reason); err != nil && !errors.Is(err, ErrLegNotPending) {
		return err
	}
	return nil
}

// conversionDestination is where the user is actually paid: the requested
// token's address, its precision — the units every promise and landing is
// denominated in — and, for a STABLECOIN destination only, the treasury lane
// that can top it up.
//
// lane is nil and stable is false for every other destination (a native coin,
// a volatile ERC-20). That is not a missing field: the treasury holds no float
// in an arbitrary token, so there is nothing to top up with and no promise to
// top up TO — those destinations are settled against LiFi's own guaranteed
// minimum instead. Every reader that pays money must check stable/lane before
// dereferencing either.
type conversionDestination struct {
	lane     *TreasuryLane
	token    string
	symbol   string
	decimals int
	stable   bool
	chainID  int64
	chainKey string
}

func (o *ConversionOrchestrator) destination(order *models.PaycrestOrderRecord) (*conversionDestination, error) {
	chainID, token := order.RequestedChainID, order.RequestedTokenAddress
	symbol := strings.ToUpper(strings.TrimSpace(order.RequestedToken))
	if chainID == 0 || token == "" { // legacy order: symbol path
		chainID = o.chainID(order.RequestedChain)
		token = o.tokenAddress(order.RequestedChain, symbol)
	}
	if chainID == 0 || token == "" {
		return nil, fmt.Errorf("conversion: %s on %s is not resolvable", symbol, order.RequestedChain)
	}
	chainKey := order.RequestedChain
	if stable, _, _ := isStableDestination(o.chains, chainID, token); !stable {
		// Decimals come off the ORDER for a non-stable destination: they were
		// resolved from LiFi at quote time (Task 6), and there is no lane here
		// to read them from the chain with.
		if order.RequestedTokenDecimals <= 0 {
			return nil, fmt.Errorf("conversion: no decimals recorded for %s on %s", symbol, chainKey)
		}
		return &conversionDestination{token: token, symbol: symbol, decimals: order.RequestedTokenDecimals, chainID: chainID, chainKey: chainKey}, nil
	}
	lane := o.treasury.Lane(chainKey)
	if lane == nil {
		return nil, fmt.Errorf("conversion: no treasury lane on %s", chainKey)
	}
	decimals, err := lane.Native.GetTokenDecimals(token)
	if err != nil {
		return nil, fmt.Errorf("conversion: read decimals of %s on %s: %w", symbol, lane.ChainKey, err)
	}
	return &conversionDestination{lane: lane, token: token, symbol: symbol, decimals: decimals, stable: true, chainID: chainID, chainKey: chainKey}, nil
}

func (o *ConversionOrchestrator) verifierFor(chainKey string) ERC20TransferVerifier {
	return o.transfers[strings.ToLower(strings.TrimSpace(chainKey))]
}

// checkCaps is the gate before new exposure. Phase 2 moves only stablecoins, so
// a token's base-unit amount converts to USD by its decimals alone — no price
// feed, and no floats in the money itself.
func (o *ConversionOrchestrator) checkCaps(ctx context.Context, lane *TreasuryLane, order *models.PaycrestOrderRecord, token string, amount *big.Int) error {
	orderUSD, err := o.usdValue(lane, token, amount)
	if err != nil {
		return err
	}
	inFlight, err := o.inFlightUSD(ctx, lane, order.ID)
	if err != nil {
		return err
	}
	return o.treasury.CheckCaps(lane.ChainKey, orderUSD, inFlight)
}

// inFlightUSD is this chain's current exposure: every OTHER converted buy whose
// funds are in the treasury's hands on this chain. It is deliberately measured
// on the delivery chain (chain_key), which is where the treasury actually holds
// the float.
//
// A failure to read it fails CLOSED: an unknown exposure is not a zero one.
func (o *ConversionOrchestrator) inFlightUSD(ctx context.Context, lane *TreasuryLane, exclude uuid.UUID) (float64, error) {
	var rows []models.PaycrestOrderRecord
	err := o.db.WithContext(ctx).
		Select("id", "token", "token_amount").
		Where("route_kind = ? AND direction = ? AND chain_key = ? AND id <> ? AND status IN ?",
			models.RouteKindConverted, models.PaycrestDirectionBuy, lane.ChainKey, exclude,
			[]string{models.PaycrestStatusConverting, models.PaycrestStatusRefunding}).
		Find(&rows).Error
	if err != nil {
		return 0, fmt.Errorf("conversion: read in-flight exposure on %s: %w", lane.ChainKey, err)
	}
	var total float64
	for _, row := range rows {
		// row.TokenAmount is a plain human-readable decimal string (see
		// startBridge's own doc comment) — never base units. It has to be
		// converted with THIS row's own token's decimals before usdValue
		// (which expects base units) can price it.
		addr := lane.tokenAddress(row.Token)
		if addr == "" {
			// FAIL CLOSED, not skip: this row counts toward the chain's
			// in-flight exposure, so silently dropping it out of the total
			// would let CheckCaps wave a NEW bridge through underneath an
			// exposure that is not actually zero, only unmeasurable. Before
			// the per-row decimals conversion above existed, this exact
			// shape reached usdValue directly and usdValue's own "addr==''"
			// branch returned ErrSponsorNotAvailable, failing the whole
			// call closed — this restores that. A distinct metric name from
			// the unparseable-amount branch below: the amount here is fine,
			// only the token is unconfigured, and a dashboard must not read
			// the two as the same kind of problem.
			log.Printf("[ramp-conversion] ALERT ramp_conversion_exposure_token_unconfigured_total=1 order=%s token=%s chain=%s",
				row.ID, row.Token, lane.ChainKey)
			return 0, ErrSponsorNotAvailable
		}
		decimals, derr := lane.Native.GetTokenDecimals(addr)
		if derr != nil {
			// Unlike a single order's own tick, this loop prices EVERY other
			// in-flight order on the chain — a real RPC failure here is
			// exactly the "unknown exposure is not a zero one" case the
			// function's own doc comment already fails closed on.
			return 0, fmt.Errorf("conversion: read decimals of %s on %s: %w", row.Token, lane.ChainKey, derr)
		}
		amount, aerr := DecimalToBaseUnits(row.TokenAmount, decimals)
		if aerr != nil {
			log.Printf("[ramp-conversion] ALERT ramp_conversion_unreadable_amount_total=1 order=%s amount=%q err=%v", row.ID, row.TokenAmount, aerr)
			continue
		}
		usd, err := o.usdValue(lane, row.Token, amount)
		if err != nil {
			return 0, err
		}
		total += usd
	}
	return total, nil
}

// usdValue converts base units to a USD figure for the caps and the top-up
// ceiling only — never for anything that is actually paid out.
func (o *ConversionOrchestrator) usdValue(lane *TreasuryLane, token string, amount *big.Int) (float64, error) {
	addr := lane.tokenAddress(token)
	if addr == "" {
		return 0, ErrSponsorNotAvailable
	}
	decimals, err := lane.Native.GetTokenDecimals(addr)
	if err != nil {
		return 0, fmt.Errorf("conversion: read decimals of %s on %s: %w", token, lane.ChainKey, err)
	}
	return unitsToUSD(amount, decimals), nil
}

// unitsToUSD is a stablecoin-only shortcut: 1 token = 1 USD, so precision is
// the whole conversion. Never used for an amount that is paid out.
func unitsToUSD(amount *big.Int, decimals int) float64 {
	if amount == nil {
		return 0
	}
	scale := new(big.Float).SetInt(pow10(decimals))
	usd, _ := new(big.Float).Quo(new(big.Float).SetInt(amount), scale).Float64()
	return usd
}

// rescaleUnits restates a base-unit amount from one token's precision to
// another's. Scaling DOWN truncates, which is deliberate: the result is used to
// size a promise, and a promise rounded down is one we can always honour.
func rescaleUnits(amount *big.Int, from, to int) *big.Int {
	if amount == nil {
		return nil
	}
	switch {
	case to == from:
		return new(big.Int).Set(amount)
	case to > from:
		return new(big.Int).Mul(amount, pow10(to-from))
	default:
		return new(big.Int).Quo(amount, pow10(from-to))
	}
}

func pow10(n int) *big.Int {
	if n < 0 {
		n = 0
	}
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

func (o *ConversionOrchestrator) chainID(chainKey string) int64 {
	if o.chains == nil {
		return 0
	}
	id, _ := o.chains.ChainIDForName(chainKey)
	return id
}

// tokenAddress resolves a token on any configured network, so a testnet
// deployment resolves its own addresses rather than silently reading mainnet's.
func (o *ConversionOrchestrator) tokenAddress(chainKey, symbol string) string {
	if o.chains == nil {
		return ""
	}
	id, ok := o.chains.ChainIDForName(chainKey)
	if !ok {
		return ""
	}
	return o.chains.TokenAddressForChainID(id, strings.ToUpper(strings.TrimSpace(symbol)))
}

// latestLeg is the highest-numbered attempt of one kind in one status.
func latestLeg(legs []models.RampLeg, kind, status string) *models.RampLeg {
	var out *models.RampLeg
	for i := range legs {
		leg := &legs[i]
		if leg.Kind != kind || leg.Status != status {
			continue
		}
		if out == nil || leg.Attempt > out.Attempt {
			out = leg
		}
	}
	return out
}

// latestPendingLeg is the leg of ANY kind still in flight — the "one leg at a
// time per order" invariant in one place. Ordered by attempt, not by
// CreatedAt: two rows written in the same clock tick are indistinguishable by
// time, and the attempt number is the field that actually sequences them.
func latestPendingLeg(legs []models.RampLeg) *models.RampLeg {
	var out *models.RampLeg
	for i := range legs {
		leg := &legs[i]
		if leg.Status != models.RampLegStatusPending {
			continue
		}
		if out == nil || leg.Attempt > out.Attempt {
			out = leg
		}
	}
	return out
}

// pendingLegOfKinds reports whether ANY leg of the given kinds is pending,
// returning the first one found in slice order (whatever is pending among a
// given kind at any moment is normally a single row — this makes no attempt
// to find "the latest" one, unlike latestPendingLeg's name).
//
// This exists because latestPendingLeg is the wrong tool for "is a bridge or
// a deliver leg pending": it picks the highest-ATTEMPT pending leg ACROSS
// EVERY KIND, but attempt numbers are sequenced independently PER KIND (see
// RampLeg.Attempt) — so a pending refund at attempt 5 would outrank, and
// hide, a pending bridge at attempt 1 that is just as live. Any caller that
// cares about a specific kind (or set of kinds) being pending must use this
// instead, not latestPendingLeg filtered after the fact.
func pendingLegOfKinds(legs []models.RampLeg, kinds ...string) *models.RampLeg {
	want := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		want[k] = true
	}
	for i := range legs {
		leg := &legs[i]
		if leg.Status == models.RampLegStatusPending && want[leg.Kind] {
			return leg
		}
	}
	return nil
}

// bridgeLegByAttempt finds one specific bridge attempt, whatever its status.
func bridgeLegByAttempt(legs []models.RampLeg, attempt int) *models.RampLeg {
	for i := range legs {
		if legs[i].Kind == models.RampLegKindBridge && legs[i].Attempt == attempt {
			return &legs[i]
		}
	}
	return nil
}

// broadcastBridgeLeg is any bridge attempt that put a transaction on the chain
// without reaching a confirmed outcome. Its funds are unaccounted for, so
// nothing else may spend against the same money.
//
// Within Advance's own step 4 this is genuinely unreachable — a leg with a
// hash is either still pending (caught by step 2, which returns before step 4
// ever runs) or has already been sent to recovery by observeBridge — and is
// kept there only as defence in depth against a future path that fails a
// broadcast leg without going through observeBridge first.
//
// It is NOT merely defensive everywhere, though — read it as a live guard
// wherever else it is called. observeBridge's own LiFi-FAILED case is exactly
// how this shape gets created and left behind: toRecovery parks the ORDER but
// never touches the LEG, so a bridge attempt LiFi reported failed after
// broadcasting sits forever as Kind=bridge, TxHash!="", Status=pending.
// OwedToUser calls this function directly to refuse to name a refund figure
// while that money might still land — reached the moment an admin looks at
// (or tries to refund) a recovery_pending order with a stranded bridge, which
// is a real, common path into a parked order.
func broadcastBridgeLeg(legs []models.RampLeg) *models.RampLeg {
	for i := range legs {
		leg := &legs[i]
		if leg.Kind == models.RampLegKindBridge && leg.TxHash != "" &&
			leg.Status != models.RampLegStatusConfirmed {
			return leg
		}
	}
	return nil
}

// ── Admin refund tooling (Task 12) ──────────────────────────────────────────
//
// This is the money-back path a HUMAN triggers, as opposed to refund() above
// (which the state machine triggers on itself after the retry window). Both
// end up moving money out of the treasury to order.WalletAddress, but this
// one answers a different question first: "given what is actually on the
// ledger right now, what — if anything — is safe to pay back, and in what
// token, on what chain?" An admin never gets to type an amount, a token or an
// address; all three are read off the order record and its own leg ledger.

var (
	// ErrNothingOwed is returned when the ledger says there is nothing left to
	// refund. Paying anyway would be a second payment to a user already made
	// whole.
	ErrNothingOwed = errors.New("this order has nothing owed to refund")
	// ErrOwedUndetermined is the OTHER refusal OwedToUser can produce, and it
	// is deliberately a DIFFERENT error from ErrNothingOwed: a leg is still in
	// flight, the user may already hold the money, and treating "we don't
	// know yet" the same as "there is nothing to pay" is exactly how an
	// operator double-pays someone whose top-up already landed.
	ErrOwedUndetermined = errors.New("what this order owes is not yet determined; a transfer is still in flight")
	// ErrRefundInFlight guards the "never race the state machine" rule: the
	// orchestrator itself could still finish paying this order (a bridge or
	// top-up leg is live or pending, or a refund is already under way), so an
	// admin refund must refuse rather than run alongside it.
	ErrRefundInFlight = errors.New("this order has a transfer already in flight; refund refused until it resolves")
	// ErrRefundNotApplicable is refused BEFORE any of the above questions are
	// even asked: this tool only ever answers for a converted BUY whose funds
	// the treasury actually took custody of. A direct order (Paycrest pays
	// the user itself — the treasury was never involved), a SELL order
	// (TokenAmount is what the user SENDS, not something the treasury ever
	// held), or a converted buy still awaiting payment (zero legs, custody
	// never taken) would otherwise fall through OwedToUser's origin branch
	// and refund platform float that was never this order's money.
	ErrRefundNotApplicable = errors.New("this order is not a converted buy the treasury took custody of")
)

// RampLegsInfo is one order's leg ledger plus what it currently owes,
// computed fresh from those same legs — the shared shape between the admin
// legs view and a dispute's detail, so neither can show a figure the other
// disagrees with.
//
// OwedToUser deliberately carries no omitempty: it has the same three states
// as models.PaycrestOrderRecord.OwedToUser ("" UNDETERMINED, "0" nothing
// owed, a number owed), and dropping the empty one over JSON would make an
// in-flight order indistinguishable from a fully settled one.
type RampLegsInfo struct {
	Legs       []models.RampLeg `json:"legs"`
	OwedToUser string           `json:"owedToUser"`
	// RefundPreview is the DIFFERENT figure a refund button would actually
	// move — see RefundPreview's own doc comment for why this is not the
	// same number as OwedToUser above. nil whenever there is nothing safe to
	// preview: the order isn't a refundable converted buy, or OwedToUser
	// itself reads "" (undetermined) or "0" (nothing owed).
	RefundPreview *RefundPreview `json:"refundPreview,omitempty"`
}

// RefundPreview is the exact triple (and its decimals) a treasury refund
// would move for one order, straight from ConversionOrchestrator.OwedToUser
// — the wire object an admin console's refund button must read.
//
// It is deliberately NOT the same figure as RampLegsInfo.OwedToUser above,
// which tracks the destination-denominated PROMISE (owedFromLegs). On an
// order whose bridge never confirmed, OwedToUser answers with the ORIGIN
// deposit instead — a different token, on a different chain, at a different
// decimal count (a 6dp/18dp pair differs by 1e12). A console that reads one
// field and assumes it is the other — or worse, guesses at decimals when
// none is given — will show a number the refund does not actually move.
type RefundPreview struct {
	Token    string `json:"token"`
	Chain    string `json:"chain"`
	Amount   string `json:"amount"`
	Decimals int    `json:"decimals"`
}

// LegsAndOwed is the read-only view behind both the admin /legs endpoint and
// a paycrest_order dispute's detail: the order's own leg ledger, and what it
// owes computed the SAME way setOrder computes it (owedFromLegs), so the
// number an operator reads here can never drift from the one the state
// machine itself is acting on.
func (o *ConversionOrchestrator) LegsAndOwed(ctx context.Context, order *models.PaycrestOrderRecord) (*RampLegsInfo, error) {
	if o == nil || order == nil {
		return nil, ErrSponsorNotAvailable
	}
	legs, err := o.legs.ForOrder(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	info := &RampLegsInfo{Legs: legs, OwedToUser: owedFromLegs(order, legs)}
	// A refund preview is a convenience, never a reason to fail the legs
	// view: an order this tool cannot refund (direct, sell, not yet
	// custodied, undetermined, nothing owed) simply carries no preview.
	if preview, perr := o.refundPreview(ctx, order); perr == nil {
		info.RefundPreview = preview
	}
	return info, nil
}

// refundPreview resolves OwedToUser into the wire object an admin console's
// refund button must actually read — see RefundPreview's doc comment for why
// this cannot be inferred from RampLegsInfo.OwedToUser alone.
func (o *ConversionOrchestrator) refundPreview(ctx context.Context, order *models.PaycrestOrderRecord) (*RefundPreview, error) {
	if o == nil || o.treasury == nil {
		return nil, ErrSponsorNotAvailable
	}
	token, chainKey, amount, err := o.OwedToUser(ctx, order)
	if err != nil {
		return nil, err
	}
	if amount == "" || amount == "0" {
		return nil, ErrNothingOwed
	}
	lane := o.treasury.Lane(chainKey)
	if lane == nil {
		return nil, ErrSponsorNotAvailable
	}
	addr := lane.tokenAddress(token)
	if addr == "" {
		return nil, ErrSponsorNotAvailable
	}
	decimals, err := lane.Native.GetTokenDecimals(addr)
	if err != nil {
		return nil, err
	}
	return &RefundPreview{Token: token, Chain: lane.ChainKey, Amount: amount, Decimals: decimals}, nil
}

// LegsAndOwedByOrderID is LegsAndOwed for a caller that only has the order id
// (an HTTP handler, typically) — it loads the order itself so callers never
// need their own database access to ask this question.
func (o *ConversionOrchestrator) LegsAndOwedByOrderID(ctx context.Context, orderID uuid.UUID) (*models.PaycrestOrderRecord, *RampLegsInfo, error) {
	if o == nil || o.db == nil {
		return nil, nil, ErrSponsorNotAvailable
	}
	var order models.PaycrestOrderRecord
	if err := o.db.WithContext(ctx).First(&order, "id = ?", orderID).Error; err != nil {
		return nil, nil, err
	}
	info, err := o.LegsAndOwed(ctx, &order)
	if err != nil {
		return nil, nil, err
	}
	return &order, info, nil
}

// OwedToUser is what an admin refund would actually pay back RIGHT NOW, and
// where — the token, the chain, and the amount in that token's base units.
// It answers a narrower question than owedFromLegs (which tracks the
// destination-denominated PROMISE): while nothing has ever bridged out, the
// treasury still holds the ORIGIN deposit untouched, and paying that back
// exactly is simpler and safer than re-synthesising a destination-token
// figure for a conversion that never happened. Once a bridge HAS confirmed,
// the origin funds are gone and any remainder owed is the destination-side
// shortfall a top-up would otherwise pay.
//
// amount has the same three states order.OwedToUser does: a number, "0" for
// nothing owed, or "" for UNDETERMINED — see owedFromLegs and the package
// doc on models.PaycrestOrderRecord.OwedToUser. token/chain are meaningless
// (and left blank) whenever amount is "0" or "".
//
// Returns ErrRefundNotApplicable for anything that is not a converted BUY
// the treasury actually took custody of — see that sentinel's doc comment.
func (o *ConversionOrchestrator) OwedToUser(ctx context.Context, order *models.PaycrestOrderRecord) (token, chain, amount string, err error) {
	if o == nil || order == nil {
		return "", "", "", ErrSponsorNotAvailable
	}
	// Custody gate, mirroring Advance's own step 1: Holder==Treasury/User is
	// exactly the pair of states reachable only by the treasury actually
	// having taken custody (takeCustody sets Treasury; settle/refund/topup
	// set User). Anything else — a direct order, a sell, or a converted buy
	// still sitting with Paycrest — is refused outright, before any of the
	// leg arithmetic below runs, so it can never fall through to the origin
	// branch and refund money the treasury never held.
	if !order.Converted() || order.Direction != models.PaycrestDirectionBuy ||
		(order.Holder != models.HolderTreasury && order.Holder != models.HolderUser) {
		return "", "", "", ErrRefundNotApplicable
	}
	switch order.Status {
	case models.PaycrestStatusSettled, models.PaycrestStatusRefunded:
		return "", "", "0", nil
	}
	legs, err := o.legs.ForOrder(ctx, order.ID)
	if err != nil {
		return "", "", "", err
	}
	// The LEDGER outranks order.Status: a refund whose status write was lost
	// must still read as paid, or a retry here would pay the user twice.
	if refunded := latestLeg(legs, models.RampLegKindRefund, models.RampLegStatusConfirmed); refunded != nil {
		return "", "", "0", nil
	}
	// Any bridge or deliver leg still in flight makes the liability genuinely
	// UNDETERMINED — the user may already hold the money, and nobody may pay
	// against a figure computed while that is still open. This is
	// deliberately broader than liveBridgeLeg (which only counts a pending
	// bridge WITH a hash): a reserved-but-hashless leg here is likewise not
	// safe to pay through, only safe to leave for the stall detector.
	// Scoped PER KIND via pendingLegOfKinds, not latestPendingLeg: attempts
	// are numbered independently per kind, so a pending refund at a higher
	// attempt number must never hide a pending bridge at a lower one.
	if pendingLegOfKinds(legs, models.RampLegKindBridge, models.RampLegKindDeliver) != nil {
		return "", "", "", nil
	}
	// A bridge attempt that broadcast a transaction but never reached a
	// terminal (confirmed) outcome is likewise UNDETERMINED, not refundable
	// — Advance's own step 4 treats this exact shape (LiFi reported FAILED,
	// or the row was simply left dangling) as money that can still land, for
	// the same reason. Refunding the origin deposit on top of a bridge that
	// might yet deliver would pay the user twice.
	if broadcastBridgeLeg(legs) != nil {
		return "", "", "", nil
	}
	if confirmedBridge := latestLeg(legs, models.RampLegKindBridge, models.RampLegStatusConfirmed); confirmedBridge != nil {
		owed := owedFromLegs(order, legs)
		if owed == "" || owed == "0" {
			return "", "", owed, nil
		}
		// Reuse o.destination() rather than trusting order.RequestedToken/
		// RequestedChain directly: it is the same resolution+validation the
		// state machine itself uses to route a top-up, so an unroutable
		// destination surfaces as a real error here too, instead of handing
		// back a token/chain pair no treasury lane can actually pay through.
		dst, derr := o.destination(order)
		if derr != nil {
			return "", "", "", derr
		}
		// A non-stable destination has no treasury lane to pay from and no
		// promise worth paying to: `owed` here is a gap against LiFi's own
		// minimum in a token the treasury does not hold. Naming a figure (or
		// dereferencing the nil lane) would hand an operator a refund they
		// cannot make; a human decides what to do with these.
		if !dst.stable || dst.lane == nil {
			return "", "", "", ErrOwedUndetermined
		}
		return dst.symbol, dst.lane.ChainKey, owed, nil
	}
	// Nothing ever bridged out: the treasury still holds the ORIGIN deposit
	// untouched, in the token and on the chain it actually landed in.
	// order.TokenAmount is a plain human-readable decimal string (see
	// startBridge's own doc comment) — never base units — so it needs the
	// ORIGIN token's own decimals, resolved off the origin lane, before it
	// can be compared or paid as base units.
	originLane := o.treasury.Lane(order.ChainKey)
	if originLane == nil {
		return "", "", "", ErrSponsorNotAvailable
	}
	originAddr := originLane.tokenAddress(order.Token)
	if originAddr == "" {
		return "", "", "", fmt.Errorf("conversion: token %s is not configured on %s", order.Token, order.ChainKey)
	}
	originDecimals, err := originLane.Native.GetTokenDecimals(originAddr)
	if err != nil {
		return "", "", "", fmt.Errorf("conversion: read decimals of %s on %s: %w", order.Token, order.ChainKey, err)
	}
	orderAmt, err := DecimalToBaseUnits(order.TokenAmount, originDecimals)
	if err != nil {
		return "", "", "", fmt.Errorf("conversion: unreadable order amount %q for order %s: %w", order.TokenAmount, order.ID, err)
	}
	if orderAmt.Sign() <= 0 {
		return "", "", "0", nil
	}
	return strings.ToUpper(strings.TrimSpace(order.Token)), order.ChainKey, orderAmt.String(), nil
}

// RefundFromTreasury is the admin action: pay back exactly what OwedToUser
// says is owed, to the order's OWN wallet, and nowhere else. The caller
// supplies only adminID (for the audit log) and orderID — never an amount, a
// token or an address.
func (o *ConversionOrchestrator) RefundFromTreasury(ctx context.Context, adminID, orderID uuid.UUID) (*models.RampLeg, error) {
	if o == nil || o.db == nil {
		return nil, ErrSponsorNotAvailable
	}
	var order models.PaycrestOrderRecord
	if err := o.db.WithContext(ctx).First(&order, "id = ?", orderID).Error; err != nil {
		return nil, err
	}

	legs, err := o.legs.ForOrder(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	// Idempotent: a refund that already landed finishes the question without
	// sending anything a second time. (This read can be stale by the time we
	// reach Reserve below — see the fresh re-check right before it — but it
	// is a correct, cheap early exit for the overwhelmingly common case.)
	if existing := latestLeg(legs, models.RampLegKindRefund, models.RampLegStatusConfirmed); existing != nil {
		return existing, nil
	}
	// A refund already broadcast (by an earlier call to this same action, or
	// by the automatic refund() path) but not yet resolved: attempt to
	// VERIFY it now rather than refusing outright. This is what actually
	// unsticks an order sitting in recovery_pending — Advance's own tick
	// never touches a parked order again, so if the admin does not
	// re-attempt verification here, nothing else ever will.
	if pending := latestLeg(legs, models.RampLegKindRefund, models.RampLegStatusPending); pending != nil {
		return o.resolveAdminRefund(ctx, adminID, &order, pending)
	}

	// OwedToUser is the single gate on custody (refuses ErrRefundNotApplicable
	// for a direct order, a sell, or a converted buy never taken into
	// custody) and on Rule 5 for a bridge or top-up (refuses "" the moment
	// either kind is still pending, or a bridge broadcast without landing —
	// the user may already hold the money). Asked FIRST, and separately from
	// the in-flight re-check below, so neither of THESE refusals is ever
	// masked by the more generic ErrRefundInFlight.
	token, chainKey, amountStr, err := o.OwedToUser(ctx, &order)
	if err != nil {
		return nil, err
	}
	if amountStr == "" {
		return nil, ErrOwedUndetermined
	}
	amount, ok := new(big.Int).SetString(amountStr, 10)
	if !ok {
		// A malformed figure is a bug in OwedToUser's own arithmetic, not a
		// business answer of "nothing owed" — conflating the two is exactly
		// the kind of error-sentinel confusion this task exists to prevent.
		return nil, fmt.Errorf("conversion: unreadable owed amount %q for order %s", amountStr, order.ID)
	}
	if amount.Sign() <= 0 {
		return nil, ErrNothingOwed
	}
	if o.treasury == nil || o.treasury.Address() == "" {
		return nil, ErrSponsorNotAvailable
	}
	lane := o.treasury.Lane(chainKey)
	if lane == nil {
		return nil, ErrSponsorNotAvailable
	}

	// Mark the intent BEFORE broadcasting, exactly as the automatic refund()
	// above does: a crash between here and the broadcast must read as "a
	// refund was under way", never as an order still waiting to convert.
	//
	// EXCEPT when the order is recovery_pending: that status is the ONLY
	// thing NeedsOperator() keys on, and Tick's own query selects
	// "refunding" for the automatic state machine to keep advancing. A
	// crash right after this write — before anything broadcast — would drop
	// a human-parked order out of the operator queue and hand it back to
	// Advance, which could resume converting an order an operator
	// deliberately stopped. The leg row Reserve is about to create is
	// already a crash-visible record that a refund is under way; the
	// order's own status does not also need to say so when a human already
	// parked it on purpose. The unconditional status=refunded write on
	// success, below, still applies regardless of the starting status: a
	// refund that actually lands must close the order out.
	if order.Status != models.PaycrestStatusRefunding && order.Status != models.PaycrestStatusRecoveryPending {
		if err := o.setOrder(ctx, &order, map[string]any{
			"status": models.PaycrestStatusRefunding, "fault": models.FaultPlatform,
		}); err != nil {
			return nil, err
		}
		// setOrder writes through GORM's map-based Updates, which this file
		// never trusts to also refresh the struct in hand (see takeCustody's
		// own explicit order.Holder mirror) — order.Fault is read again
		// below, by observeRefund's finishRefund call, so it must be current.
		order.Fault = models.FaultPlatform
	}

	// A FRESH, LAST-MOMENT read, immediately before reserving. The `legs`
	// fetched above can be stale by now — OwedToUser and the status write
	// each did their own round trip — and in that window the orchestrator's
	// OWN automatic refund() could have reserved an attempt of its own.
	// Refusing on ANY refund leg (pending OR confirmed) rather than trusting
	// NextAttempt to serialise the two callers is what actually closes that
	// race: two independent NextAttempt calls can each compute a different,
	// non-colliding attempt number and both go on to broadcast, which the
	// (order_id, kind, attempt) unique index does nothing to stop — it only
	// rejects two callers landing on the SAME attempt number.
	freshLegs, err := o.legs.ForOrder(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	if again := latestLeg(freshLegs, models.RampLegKindRefund, models.RampLegStatusConfirmed); again != nil {
		return again, nil
	}
	if pending := latestLeg(freshLegs, models.RampLegKindRefund, models.RampLegStatusPending); pending != nil {
		return o.resolveAdminRefund(ctx, adminID, &order, pending)
	}

	if err := o.treasury.requireGas(ctx, lane, treasuryERC20Gas); err != nil {
		return nil, err
	}
	attempt, err := o.legs.NextAttempt(ctx, order.ID, models.RampLegKindRefund)
	if err != nil {
		return nil, err
	}
	newLeg := &models.RampLeg{
		OrderID: order.ID, Kind: models.RampLegKindRefund, Attempt: attempt,
		ChainKey: lane.ChainKey, Token: strings.ToUpper(token),
		FromAddr: o.treasury.Address(), ToAddr: order.WalletAddress,
		Amount: amount.String(), Status: models.RampLegStatusPending,
		CreatedAt: o.now().UTC(),
	}
	reserved, err := o.legs.Reserve(ctx, newLeg)
	if err != nil {
		return nil, err
	}
	if reserved != nil {
		// Another call already claimed this attempt: try to resolve ITS
		// outcome rather than refusing outright — same reasoning as the two
		// pending-leg checks above.
		return o.resolveAdminRefund(ctx, adminID, &order, reserved)
	}

	txHash, attempted, err := o.treasury.TransferToken(ctx, lane, token, order.WalletAddress, amount)
	if err != nil {
		if attempted {
			// Something may have landed: the leg MUST stay pending — never
			// freed — so nothing can pay this order a second time while its
			// outcome is unknown.
			log.Printf("[ramp-conversion] ALERT ramp_treasury_refund_broadcast_uncertain_total=1 order=%s admin=%s leg=%s err=%v",
				order.ID, adminID, newLeg.ID, err)
			return nil, err
		}
		// Nothing was broadcast (attempted=false), but this write MUST still
		// land even if the caller's own request context is what is about to
		// be cancelled — a lost "failed" write here would leave the leg row
		// permanently pending, blocking every future refund attempt on this
		// order. Same detached(ctx) pattern refund() and startBridge use.
		if ferr := o.failLeg(detached(ctx), newLeg.ID, err.Error()); ferr != nil {
			return nil, ferr
		}
		return nil, err
	}
	// Detached: the transfer already broadcast, so this write must survive
	// the caller's own context ending — exactly refund()'s own pattern.
	if err := o.legs.SetTxHash(detached(ctx), newLeg.ID, txHash, ""); err != nil {
		return nil, err
	}
	newLeg.TxHash = txHash
	log.Printf("[ramp-conversion] order=%s refund#%d chain=%s token=%s amount=%s tx=%s admin=%s — verifying on-chain before closing the order",
		order.ID, attempt, lane.ChainKey, token, amount, txHash, adminID)

	// A broadcast is not proof: Task 7's on-chain-verification ruling
	// applies to a refund exactly as much as a bridge landing or a top-up —
	// confirming (and closing the order) here on broadcast alone is the one
	// asymmetry that let a reverted or dropped refund transfer record a
	// "confirmed" leg and a terminal "refunded" order for a debt that was
	// never actually paid. This is a synchronous admin action and a refund
	// is a single same-chain transfer, so attempting the proof in the SAME
	// call — rather than waiting for a later tick, which this order may
	// never receive again once it is recovery_pending — is both correct and
	// the only way an admin ever gets a definitive answer from this action.
	return o.resolveAdminRefund(ctx, adminID, &order, newLeg)
}

// resolveAdminRefund asks observeRefund to verify a refund leg on-chain and,
// if that lands it, logs the admin audit trail (Decision 6) and returns the
// now-confirmed leg. If the leg is still genuinely pending — not yet mined,
// or the RPC has not caught up — it is returned AS-IS with no error: a
// broadcast is a real, useful answer for an admin to see, just not yet a
// confirmed one, and refusing outright here would leave a recovery_pending
// order with no way to ever be re-checked (Advance's own tick does not
// touch a parked order again, so a repeat call to this same action is what
// has to stand in for that tick).
func (o *ConversionOrchestrator) resolveAdminRefund(ctx context.Context, adminID uuid.UUID, order *models.PaycrestOrderRecord, pending *models.RampLeg) (*models.RampLeg, error) {
	if err := o.observeRefund(ctx, order, pending); err != nil {
		return nil, err
	}
	legs, err := o.legs.ForOrder(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	if confirmed := latestLeg(legs, models.RampLegKindRefund, models.RampLegStatusConfirmed); confirmed != nil {
		log.Printf("[ramp-conversion] ALERT ramp_treasury_refund_total=1 order=%s admin=%s amount=%s tx=%s",
			order.ID, adminID, confirmed.AmountReceived, confirmed.TxHash)
		return confirmed, nil
	}
	return pending, nil
}

// detached returns a short-lived context that survives the caller's own
// cancellation, for writes that MUST land because the broadcast already
// happened — the same pattern as recordSponsorshipTx in sponsor_lane.go.
//
// The cancel func cannot be deferred here (the context has to outlive this
// call), so it is registered with AfterFunc instead: the timer is released as
// soon as the context is done, whether that is the write finishing or the
// timeout firing.
func detached(ctx context.Context) context.Context {
	out, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	context.AfterFunc(out, cancel)
	return out
}
