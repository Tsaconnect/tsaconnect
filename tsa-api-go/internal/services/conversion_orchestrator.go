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
	now        func() time.Time
}

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
	// told to pay. Refuse the order rather than act on a guess.
	if o.treasury == nil || o.treasury.Address() == "" {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_treasury_total=1 order=%s", order.ID)
		return ErrSponsorNotAvailable
	}
	lane := o.treasury.Lane(order.ChainKey)
	if lane == nil {
		return ErrSponsorNotAvailable
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
		}
		// A refund we broadcast ourselves but could not record an outcome for.
		// Its fate is unknown, so this order stops here rather than risking a
		// second payment; the stall deadline surfaces it to an operator.
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

	// The promise is what the user RECEIVES, so it is denominated in the
	// destination token's base units. Source and destination precision differ
	// (BSC stables are 18dp against 6dp on base/polygon), and comparing a
	// promise in source units against a landing in destination units would make
	// every cross-precision route settle wrong by twelve orders of magnitude.
	dst, err := o.destination(order)
	if err != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_destination_total=1 order=%s err=%v", order.ID, err)
		return o.toRecovery(ctx, order, err.Error(), nil)
	}
	srcDecimals, err := lane.Native.GetTokenDecimals(lane.tokenAddress(order.Token))
	if err != nil {
		return fmt.Errorf("conversion: read decimals of %s on %s: %w", order.Token, lane.ChainKey, err)
	}
	promise := PromisedAmount(rescaleUnits(received, srcDecimals, dst.decimals), order.MarkupBps)

	updates := map[string]any{
		"holder":          models.HolderTreasury,
		"promised_amount": promise.String(),
	}
	if expected, ok := new(big.Int).SetString(order.TokenAmount, 10); ok && received.Cmp(expected) < 0 {
		// Short delivery: never convert more than actually landed, and never
		// promise more than what landed can cover.
		log.Printf("[ramp-conversion] order=%s delivery short: expected=%s received=%s — repricing the promise",
			order.ID, expected, received)
		updates["token_amount"] = received.String()
	}
	log.Printf("[ramp-conversion] order=%s custody=treasury tx=%s amount=%s promise=%s (%ddp->%ddp)",
		order.ID, order.TxHash, received, promise, srcDecimals, dst.decimals)
	return o.setOrder(ctx, order, updates)
}

// startBridge reserves the attempt FIRST, then quotes, then broadcasts. The
// quote is a network round-trip long enough for a second tick to enter this
// function, so reserving after it would let both ticks broadcast; reserving
// before it means the loser never even asks for a route.
func (o *ConversionOrchestrator) startBridge(ctx context.Context, order *models.PaycrestOrderRecord, lane *TreasuryLane) error {
	amount, ok := new(big.Int).SetString(order.TokenAmount, 10)
	if !ok {
		return fmt.Errorf("conversion: unreadable order amount %q", order.TokenAmount)
	}
	promised, ok := new(big.Int).SetString(order.PromisedAmount, 10)
	if !ok {
		return fmt.Errorf("conversion: unreadable promise %q for order %s", order.PromisedAmount, order.ID)
	}
	// Resolved before anything is reserved: an unroutable destination is a
	// configuration fault that will never fix itself, and burning the whole
	// retry window rediscovering that just delays the human.
	dst, err := o.destination(order)
	if err != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_destination_total=1 order=%s err=%v", order.ID, err)
		return o.toRecovery(ctx, order, err.Error(), nil)
	}
	// Caps gate NEW exposure, and this is the only path that creates any.
	if err := o.checkCaps(ctx, lane, order, order.Token, amount); err != nil {
		return err
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

	route, err := o.lifi.Quote(ctx, LiFiQuoteRequest{
		FromChainID: lane.ChainID,
		ToChainID:   o.chainID(order.RequestedChain),
		FromToken:   lane.tokenAddress(order.Token),
		ToToken:     dst.token,
		FromAddress: o.treasury.Address(),
		ToAddress:   order.WalletAddress, // LiFi delivers straight to the user
		FromAmount:  amount.String(),
	})
	if err != nil {
		// Nothing was broadcast, so this attempt is provably dead: fail it so
		// the next tick starts a clean attempt inside the window.
		if ferr := o.failLeg(detached(ctx), leg.ID, err.Error()); ferr != nil {
			return ferr
		}
		return err
	}
	// Decision 5, applied at execution time: a route whose guaranteed minimum
	// is below the promise cannot honour it, so it is not a route we may take.
	if route.ToAmountMin == nil || route.ToAmountMin.Cmp(promised) < 0 {
		log.Printf("[ramp-conversion] order=%s bridge#%d rejected route: toAmountMin=%v < promise=%s",
			order.ID, attempt, route.ToAmountMin, promised)
		return o.failLeg(detached(ctx), leg.ID, "route guarantees less than the promised amount")
	}

	txHash, broadcastAttempted, err := o.treasury.ExecuteRoute(ctx, lane, order.Token, amount, route)
	if err != nil {
		if broadcastAttempted {
			// Might have landed: keep the leg pending so no second route runs.
			log.Printf("[ramp-conversion] ALERT ramp_conversion_broadcast_uncertain_total=1 order=%s leg=%s err=%v",
				order.ID, leg.ID, err)
			return nil
		}
		return o.failLeg(detached(ctx), leg.ID, err.Error())
	}
	// Detached: the broadcast already happened, so losing this write to a
	// cancelled request would strand the leg with no hash.
	if err := o.legs.SetTxHash(detached(ctx), leg.ID, txHash, route.Tool); err != nil {
		return err
	}
	log.Printf("[ramp-conversion] order=%s bridge#%d chain=%s->%s tx=%s tool=%s",
		order.ID, attempt, lane.ChainKey, order.RequestedChain, txHash, route.Tool)
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
	if !strings.EqualFold(st.ToAddress, order.WalletAddress) {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_wrong_recipient_total=1 order=%s tx=%s reported=%s wallet=%s",
			order.ID, leg.TxHash, st.ToAddress, order.WalletAddress)
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
	if txHash == "" {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_dest_tx_total=1 order=%s stage=%s", order.ID, stage)
		return nil, nil
	}
	verifier := o.verifierFor(order.RequestedChain)
	if verifier == nil {
		// Same reasoning as taking custody: with no verifier the alternative is
		// settling on a number nobody checked.
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_verifier_total=1 order=%s chain=%s stage=%s",
			order.ID, order.RequestedChain, stage)
		return nil, nil
	}
	credited, err := verifier.VerifyTransfer(ctx, txHash, dst.token, order.WalletAddress)
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
	credited, err := o.verifyOnDestination(ctx, order, dst, st.DestTxHash, "landing")
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

// refund returns the FULL Paycrest amount — markup included — in the token the
// treasury actually holds, on the chain Paycrest delivered to, to the wallet
// the order itself names.
//
// No cap check: a refund returns money the treasury already holds.
func (o *ConversionOrchestrator) refund(ctx context.Context, order *models.PaycrestOrderRecord, lane *TreasuryLane, fault string) error {
	amount, ok := new(big.Int).SetString(order.TokenAmount, 10)
	if !ok {
		return fmt.Errorf("conversion: unreadable order amount %q", order.TokenAmount)
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
	if err := o.legs.Confirm(detached(ctx), leg.ID, amount.String(), o.now()); err != nil && !errors.Is(err, ErrLegNotPending) {
		return err
	}
	log.Printf("[ramp-conversion] order=%s refund#%d chain=%s token=%s amount=%s tx=%s fault=%s",
		order.ID, attempt, lane.ChainKey, leg.Token, amount, txHash, fault)
	return o.finishRefund(detached(ctx), order, fault)
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

// conversionDestination is where the user is actually paid: the treasury lane
// on the requested chain, the requested token's address there, and its
// precision — the units every promise and landing is denominated in.
type conversionDestination struct {
	lane     *TreasuryLane
	token    string
	symbol   string
	decimals int
}

func (o *ConversionOrchestrator) destination(order *models.PaycrestOrderRecord) (*conversionDestination, error) {
	lane := o.treasury.Lane(order.RequestedChain)
	if lane == nil {
		return nil, fmt.Errorf("conversion: no treasury lane on %s", order.RequestedChain)
	}
	symbol := strings.ToUpper(strings.TrimSpace(order.RequestedToken))
	token := lane.tokenAddress(symbol)
	if token == "" {
		token = o.tokenAddress(order.RequestedChain, symbol)
	}
	if token == "" {
		return nil, fmt.Errorf("conversion: token %s is not configured on %s", symbol, order.RequestedChain)
	}
	decimals, err := lane.Native.GetTokenDecimals(token)
	if err != nil {
		return nil, fmt.Errorf("conversion: read decimals of %s on %s: %w", symbol, lane.ChainKey, err)
	}
	return &conversionDestination{lane: lane, token: token, symbol: symbol, decimals: decimals}, nil
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
		amount, ok := new(big.Int).SetString(row.TokenAmount, 10)
		if !ok {
			log.Printf("[ramp-conversion] ALERT ramp_conversion_unreadable_amount_total=1 order=%s amount=%q", row.ID, row.TokenAmount)
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
// NOTE: as the state machine currently stands this is unreachable — a leg with
// a hash is either pending (caught by step 2) or has already been sent to
// recovery by observeBridge. It is kept as defence in depth against a future
// path that fails a broadcast leg, which is the one mistake that would cost a
// user a second payment. Do not read it as a live guard.
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
