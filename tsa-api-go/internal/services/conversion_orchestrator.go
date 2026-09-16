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

// shortfallToleranceBps is B3: a landing under the promise by this much is
// topped up from margin rather than retried. Anything larger is a real miss.
const shortfallToleranceBps = 20

// conversionTickLimit is the default batch size for one Tick.
const conversionTickLimit = 20

// lifiQuoter is the quote side of LiFi this state machine needs.
// *LiFiQuoteClient satisfies it.
type lifiQuoter interface {
	Quote(ctx context.Context, req LiFiQuoteRequest) (*LiFiRoute, error)
}

// ConversionOrchestrator drives one converted buy from "Paycrest paid our
// treasury" to "the user holds at least what we promised" — or, failing that,
// to a refund.
//
// Every step either observes chain/provider state or reserves a leg BEFORE it
// broadcasts, so the whole thing is safe to re-run: a crash, a duplicate tick
// or a cancelled request can cost a wasted quote, never a second conversion.
type ConversionOrchestrator struct {
	db          *gorm.DB
	treasury    *Treasury
	legs        rampLegStore
	lifi        lifiQuoter
	status      *LiFiStatusClient
	transfers   ERC20TransferVerifier
	chains      *BlockchainService
	retryWindow time.Duration
	now         func() time.Time
}

func NewConversionOrchestrator(
	db *gorm.DB, treasury *Treasury, legs rampLegStore,
	lifi lifiQuoter, status *LiFiStatusClient, transfers ERC20TransferVerifier,
	chains *BlockchainService, retryWindow time.Duration, now func() time.Time,
) *ConversionOrchestrator {
	if now == nil {
		now = time.Now
	}
	if retryWindow <= 0 {
		retryWindow = 30 * time.Minute
	}
	return &ConversionOrchestrator{
		db: db, treasury: treasury, legs: legs, lifi: lifi, status: status,
		transfers: transfers, chains: chains, retryWindow: retryWindow, now: now,
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
		Order("updated_at ASC").Limit(limit).Find(&orders).Error
	if err != nil {
		return err
	}
	var errs []error
	for i := range orders {
		if err := o.Advance(ctx, &orders[i]); err != nil {
			log.Printf("[ramp-conversion] order=%s advance: %v", orders[i].ID, err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Advance moves one converted buy forward by at most ONE step. It is safe to
// call repeatedly: every step either observes chain state or reserves a leg
// before broadcasting, so a re-tick during a pending leg does nothing.
func (o *ConversionOrchestrator) Advance(ctx context.Context, order *models.PaycrestOrderRecord) error {
	if !order.Converted() || order.Direction != models.PaycrestDirectionBuy {
		return nil
	}
	if models.PaycrestOrderTerminal(order.Status) {
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
	//    LogTransferVerifier already parses the Transfer log, checks the token
	//    and recipient, and sums the amount; reuse it rather than re-parsing.
	if order.Holder == models.HolderPaycrest {
		return o.takeCustody(ctx, order, lane)
	}

	legs, err := o.legs.ForOrder(ctx, order.ID)
	if err != nil {
		return err
	}

	// 2. A pending leg of any kind means one leg is already in flight for this
	//    order, and nothing else may move until it resolves.
	if pending := latestPendingLeg(legs); pending != nil {
		if pending.Kind == models.RampLegKindBridge {
			return o.observeBridge(ctx, order, lane, pending)
		}
		// A deliver/refund transfer we broadcast ourselves but could not
		// record an outcome for. Its fate is unknown, so this order stops
		// here for an operator rather than risking a second payment.
		log.Printf("[ramp-conversion] ALERT ramp_conversion_leg_stalled_total=1 order=%s leg=%s kind=%s attempt=%d tx=%s",
			order.ID, pending.ID, pending.Kind, pending.Attempt, pending.TxHash)
		return nil
	}

	// 3. A confirmed bridge leg: settle, or top up a small shortfall.
	if done := latestLeg(legs, models.RampLegKindBridge, models.RampLegStatusConfirmed); done != nil {
		return o.settleOrTopUp(ctx, order, done)
	}

	// 4. No live bridge leg. Retry while inside the window, else refund.
	if o.now().After(order.CreatedAt.Add(o.retryWindow)) {
		return o.refund(ctx, order, lane, models.FaultPlatform)
	}
	return o.startBridge(ctx, order, lane)
}

// takeCustody proves Paycrest's delivery landed in the treasury before the
// order is allowed to move. A short delivery reprices BOTH the amount and the
// promise: a promise computed from money that never arrived would commit us to
// paying out more than we hold.
func (o *ConversionOrchestrator) takeCustody(ctx context.Context, order *models.PaycrestOrderRecord, lane *TreasuryLane) error {
	if order.TxHash == "" {
		return nil // nothing delivered yet
	}
	received, err := o.transfers.VerifyTransfer(ctx, order.TxHash, lane.tokenAddress(order.Token), o.treasury.Address())
	if err != nil {
		if errors.Is(err, ErrTransferReverted) || errors.Is(err, ErrTransferWrongRecipient) {
			log.Printf("[ramp-conversion] ALERT ramp_conversion_delivery_bad_total=1 order=%s tx=%s err=%v", order.ID, order.TxHash, err)
		}
		return nil // not yet, or not ours; the next tick looks again
	}
	expected, _ := new(big.Int).SetString(order.TokenAmount, 10)
	if expected != nil && received.Cmp(expected) < 0 {
		// Short delivery: never convert more than actually landed, and never
		// promise more than what landed can cover.
		log.Printf("[ramp-conversion] order=%s delivery short: expected=%s received=%s — repricing the promise",
			order.ID, expected, received)
		return o.setOrder(ctx, order, map[string]any{
			"token_amount":    received.String(),
			"promised_amount": PromisedAmount(received, order.MarkupBps).String(),
			"holder":          models.HolderTreasury,
		})
	}
	log.Printf("[ramp-conversion] order=%s custody=treasury tx=%s amount=%s", order.ID, order.TxHash, received)
	return o.setOrder(ctx, order, map[string]any{"holder": models.HolderTreasury})
}

// startBridge quotes FRESH (never a cached or reused route), reserves the next
// attempt, then broadcasts. The reservation happens before the broadcast so a
// crash in between cannot produce a second conversion.
func (o *ConversionOrchestrator) startBridge(ctx context.Context, order *models.PaycrestOrderRecord, lane *TreasuryLane) error {
	amount, ok := new(big.Int).SetString(order.TokenAmount, 10)
	if !ok {
		return fmt.Errorf("conversion: unreadable order amount %q", order.TokenAmount)
	}
	// Caps first: the treasury documents that obligation but cannot enforce it,
	// and everything past this point can move real money.
	if err := o.checkCaps(ctx, lane, order, order.Token, amount); err != nil {
		return err
	}
	route, err := o.lifi.Quote(ctx, LiFiQuoteRequest{
		FromChainID: lane.ChainID,
		ToChainID:   o.chainID(order.RequestedChain),
		FromToken:   lane.tokenAddress(order.Token),
		ToToken:     o.tokenAddress(order.RequestedChain, order.RequestedToken),
		FromAddress: o.treasury.Address(),
		ToAddress:   order.WalletAddress, // LiFi delivers straight to the user
		FromAmount:  amount.String(),
	})
	if err != nil {
		return err // the next tick retries until the window closes
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
	}
	existing, err := o.legs.Reserve(ctx, leg)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil // another tick already claimed this attempt
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
// A DONE report is followed straight into the settle decision: confirming a
// landing is an observation, not a money movement, so the two belong to the
// same step.
func (o *ConversionOrchestrator) observeBridge(ctx context.Context, order *models.PaycrestOrderRecord, lane *TreasuryLane, leg *models.RampLeg) error {
	if leg.TxHash == "" {
		// Reserved, then the broadcast outcome was never recorded. There is
		// nothing to ask LiFi about; an operator has to resolve it.
		if o.now().After(order.CreatedAt.Add(o.retryWindow)) {
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
		if st.ReceivedBaseUnits == nil || st.ReceivedBaseUnits.Sign() <= 0 {
			log.Printf("[ramp-conversion] ALERT ramp_conversion_done_without_amount_total=1 order=%s tx=%s", order.ID, leg.TxHash)
			return nil
		}
		// A DONE that credited someone else is not this order's settlement.
		if st.ToAddress != "" && !strings.EqualFold(st.ToAddress, order.WalletAddress) {
			log.Printf("[ramp-conversion] ALERT ramp_conversion_wrong_recipient_total=1 order=%s tx=%s reported=%s wallet=%s",
				order.ID, leg.TxHash, st.ToAddress, order.WalletAddress)
			return fmt.Errorf("conversion: bridge for order %s reported a landing at %s, not the user's wallet", order.ID, st.ToAddress)
		}
		landed := st.ReceivedBaseUnits
		if err := o.legs.Confirm(detached(ctx), leg.ID, landed.String(), o.now()); err != nil {
			if errors.Is(err, ErrLegNotPending) {
				return nil // another tick confirmed it and will settle it
			}
			return err
		}
		log.Printf("[ramp-conversion] order=%s bridge#%d landed tx=%s amount=%s", order.ID, leg.Attempt, leg.TxHash, landed)
		confirmed := *leg
		confirmed.Status = models.RampLegStatusConfirmed
		confirmed.AmountReceived = landed.String()
		return o.settleOrTopUp(ctx, order, &confirmed)

	case LiFiStatusFailed:
		log.Printf("[ramp-conversion] order=%s bridge#%d failed tx=%s", order.ID, leg.Attempt, leg.TxHash)
		return o.failLeg(detached(ctx), leg.ID, "lifi reported the transfer failed")

	default:
		return nil // still in flight
	}
}

// settleOrTopUp compares what landed against the promise (base units, never
// floats). Short by <= 20 bps is made whole from margin; more is a miss.
func (o *ConversionOrchestrator) settleOrTopUp(ctx context.Context, order *models.PaycrestOrderRecord, bridge *models.RampLeg) error {
	promised, _ := new(big.Int).SetString(order.PromisedAmount, 10)
	landed, _ := new(big.Int).SetString(bridge.AmountReceived, 10)
	if promised == nil || landed == nil {
		return fmt.Errorf("conversion: unreadable promise/landing for order %s", order.ID)
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
		log.Printf("[ramp-conversion] order=%s settled landed=%s promised=%s", order.ID, landed, promised)
		return o.setOrder(ctx, order, map[string]any{
			"status": models.PaycrestStatusSettled, "holder": models.HolderUser,
		})
	}
	missBps := new(big.Int).Mul(shortfall, big.NewInt(10_000))
	missBps.Div(missBps, promised)
	if missBps.Int64() > shortfallToleranceBps {
		// A real miss — and NOT something a retry can fix: the money already
		// reached the user's wallet, so re-bridging would pay twice. The leg
		// stays confirmed (it did land), the balance is recorded as owed, and
		// the order leaves the automated path for an operator.
		log.Printf("[ramp-conversion] ALERT ramp_conversion_promise_missed_total=1 order=%s landed=%s promised=%s miss_bps=%s",
			order.ID, landed, promised, missBps)
		return o.setOrder(ctx, order, map[string]any{
			"status": models.PaycrestStatusRecoveryPending,
			"holder": models.HolderUser,
			"fault":  models.FaultPlatform,
		})
	}
	// Top up the difference in the token the user actually received.
	return o.deliverShortfall(ctx, order, shortfall)
}

// deliverShortfall pays the gap from the treasury in the token the user
// actually received, on the chain they received it — which is the REQUESTED
// chain/token, not the one Paycrest delivered to us.
func (o *ConversionOrchestrator) deliverShortfall(ctx context.Context, order *models.PaycrestOrderRecord, shortfall *big.Int) error {
	lane := o.treasury.Lane(order.RequestedChain)
	if lane == nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_no_topup_lane_total=1 order=%s chain=%s", order.ID, order.RequestedChain)
		return fmt.Errorf("conversion: no treasury lane on %s to top up order %s", order.RequestedChain, order.ID)
	}
	if err := o.checkCaps(ctx, lane, order, order.RequestedToken, shortfall); err != nil {
		return err
	}
	attempt, err := o.legs.NextAttempt(ctx, order.ID, models.RampLegKindDeliver)
	if err != nil {
		return err
	}
	leg := &models.RampLeg{
		OrderID: order.ID, Kind: models.RampLegKindDeliver, Attempt: attempt,
		ChainKey: lane.ChainKey, Token: strings.ToUpper(order.RequestedToken),
		FromAddr: o.treasury.Address(), ToAddr: order.WalletAddress,
		Amount: shortfall.String(), Status: models.RampLegStatusPending,
	}
	existing, err := o.legs.Reserve(ctx, leg)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}
	txHash, attempted, err := o.treasury.TransferToken(ctx, lane, order.RequestedToken, order.WalletAddress, shortfall)
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
	if err := o.legs.Confirm(detached(ctx), leg.ID, shortfall.String(), o.now()); err != nil && !errors.Is(err, ErrLegNotPending) {
		return err
	}
	log.Printf("[ramp-conversion] order=%s deliver#%d chain=%s token=%s amount=%s tx=%s",
		order.ID, attempt, lane.ChainKey, leg.Token, shortfall, txHash)
	// Detached: the top-up is already on-chain, so this write must not be lost
	// with a cancelled request.
	return o.setOrder(detached(ctx), order, map[string]any{
		"status": models.PaycrestStatusSettled, "holder": models.HolderUser,
	})
}

// refund returns the FULL Paycrest amount — markup included — in the token the
// treasury actually holds, on the chain Paycrest delivered to, to the wallet
// the order itself names.
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
	if err := o.checkCaps(ctx, lane, order, order.Token, amount); err != nil {
		return err
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

// owedFromLegs is what we still owe the user, in the requested token's base
// units: the promise less everything that actually reached their wallet. A
// settled or refunded order owes nothing by definition.
func owedFromLegs(order *models.PaycrestOrderRecord, legs []models.RampLeg) string {
	switch order.Status {
	case models.PaycrestStatusSettled, models.PaycrestStatusRefunded, models.PaycrestStatusRefunding:
		return "0"
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

// checkCaps is the gate before anything irreversible. Phase 2 moves only
// stablecoins, so a token's base-unit amount converts to USD by its decimals
// alone — no price feed, and no floats in the money itself.
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
// the float; a top-up on the destination chain is checked against the same
// figure rather than a second, per-destination one, because a top-up is a
// fraction of a percent of an order and the cap exists to bound the float.
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

// usdValue converts base units to a USD figure for the caps only — never for
// anything that is actually paid out.
func (o *ConversionOrchestrator) usdValue(lane *TreasuryLane, token string, amount *big.Int) (float64, error) {
	addr := lane.tokenAddress(token)
	if addr == "" {
		return 0, ErrSponsorNotAvailable
	}
	decimals, err := lane.Native.GetTokenDecimals(addr)
	if err != nil {
		return 0, fmt.Errorf("conversion: read decimals of %s on %s: %w", token, lane.ChainKey, err)
	}
	scale := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	usd, _ := new(big.Float).Quo(new(big.Float).SetInt(amount), scale).Float64()
	return usd, nil
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

// latestPendingLeg is the most recent leg of ANY kind still in flight — the
// "one leg at a time per order" invariant in one place.
func latestPendingLeg(legs []models.RampLeg) *models.RampLeg {
	var out *models.RampLeg
	for i := range legs {
		leg := &legs[i]
		if leg.Status != models.RampLegStatusPending {
			continue
		}
		if out == nil || leg.CreatedAt.After(out.CreatedAt) {
			out = leg
		}
	}
	return out
}

// detached returns a short-lived context that survives the caller's own
// cancellation, for writes that MUST land because the broadcast already
// happened — the same pattern as recordSponsorshipTx in sponsor_lane.go.
func detached(ctx context.Context) context.Context {
	out, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	context.AfterFunc(out, cancel) // release the timer once the write finishes
	return out
}
