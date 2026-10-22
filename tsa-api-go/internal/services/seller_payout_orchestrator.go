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
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// The seller-payout machine pays a marketplace seller fiat into their bank
// once the buyer's confirmation has released the escrow to the treasury (the
// treasury is the order's on-chain seller):
//
//	release lands on Sonic -> bridge to Base USDC (LiFi) -> Paycrest sell for
//	exactly the quoted naira (refund address = treasury) -> deposit -> settled
//
// Every step moves on a PROOF — a Transfer log, a Paycrest status read back
// from its API — never on a report, and every step claims its state before it
// does anything slow, so a second tick cannot do it twice. Anything unclear
// parks the payout in needs_review. The machine never pays the seller's wallet
// and never retries an operation that may already have moved money; the way
// out of needs_review is a person who has read the chain and Paycrest.
//
// The legs are ramp_legs rows keyed by the payout's ID, handled by the shared
// legRunner exactly as the converted-buy machine handles its own.

// releaseProver is the slice of EscrowService that proves a release.
type releaseProver interface {
	VerifyOrderEvent(txHash, escrowAddr, event string, orderID [32]byte) (*types.Log, error)
}

// sellerPayoutSeller is the slice of PaycrestOrderService the machine uses.
type sellerPayoutSeller interface {
	CreateSellForFiat(ctx context.Context, req PaycrestSellRequest, converted bool, maxTokenBaseUnits *big.Int) (*models.PaycrestOrderRecord, error)
}

const (
	// sellerPayoutAbsorbBps is the most the platform gives up to rate drift:
	// 0.5% of the payout, as extra USDC deposited beyond what the bridge landed.
	// Past it the payout parks for a person instead of paying more.
	sellerPayoutAbsorbBps = 50
	// sellerPayoutMaxBridgeBps is the most a Sonic -> Base route may cost
	// (LiFi's cost plus the treasury's gas) before the attempt is freed.
	sellerPayoutMaxBridgeBps = 100
	// sellerPayoutOrderMargin is how close to the Paycrest order's expiry a
	// deposit is no longer sent: it would land after the order lapsed.
	sellerPayoutOrderMargin = 2 * time.Minute
	// sellerPayoutSellingGrace is how long a payout may sit in selling before
	// it is assumed the process died while opening the order.
	sellerPayoutSellingGrace = 2 * time.Minute
	// sellerPayoutSettleAfter is how long a deposited payout may wait for
	// Paycrest before it is parked rather than watched.
	sellerPayoutSettleAfter = 24 * time.Hour
)

var errSellerPayoutTransition = errors.New("seller payout is no longer in the expected state")

// SellerPayoutOrchestrator advances seller payouts. See the file comment.
type SellerPayoutOrchestrator struct {
	legRunner
	lifi     lifiQuoter
	status   *LiFiStatusClient
	paycrest sellerPayoutSeller
	escrow   releaseProver
	// escrowAddr is the configured escrow, for an order that predates recording its own.
	escrowAddr  string
	retryWindow time.Duration
	legCeiling  time.Duration
	// sellRetryWindow bounds how long a payout waits for a venue that refused
	// before anything was sent (rate moved, below the book's minimum, no venue).
	sellRetryWindow time.Duration
}

func NewSellerPayoutOrchestrator(db *gorm.DB, treasury *Treasury, legs rampLegStore, lifi lifiQuoter,
	status *LiFiStatusClient, transfers map[string]ERC20TransferVerifier, paycrest sellerPayoutSeller,
	escrow releaseProver, escrowAddr string,
	retryWindow, legCeiling time.Duration, now func() time.Time) *SellerPayoutOrchestrator {
	if now == nil {
		now = time.Now
	}
	if retryWindow <= 0 {
		retryWindow = 30 * time.Minute
	}
	if legCeiling <= 0 {
		legCeiling = defaultLegCeiling
	}
	keyed := make(map[string]ERC20TransferVerifier, len(transfers))
	for k, v := range transfers {
		keyed[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return &SellerPayoutOrchestrator{
		legRunner: legRunner{db: db, treasury: treasury, legs: legs, transfers: keyed, now: now},
		lifi:      lifi, status: status, paycrest: paycrest, escrow: escrow, escrowAddr: escrowAddr,
		retryWindow: retryWindow, legCeiling: legCeiling, sellRetryWindow: retryWindow,
	}
}

// Tick advances every payout that is still moving, least recently looked at
// first. One payout's failure never stops the others.
func (o *SellerPayoutOrchestrator) Tick(ctx context.Context, limit int) error {
	var rows []models.SellerPayoutOrder
	if err := o.db.WithContext(ctx).
		Where("status NOT IN ?", []string{models.SellerPayoutPaid, models.SellerPayoutCancelled, models.SellerPayoutNeedsReview}).
		Order("checked_at ASC NULLS FIRST").Limit(limit).Find(&rows).Error; err != nil {
		return err
	}
	var errs []error
	for i := range rows {
		p := &rows[i]
		if err := o.Advance(ctx, p); err != nil {
			log.Printf("[seller-payout] payout=%s status=%s: %v", p.ID, p.Status, err)
			errs = append(errs, err)
		}
		now := o.now()
		o.db.WithContext(ctx).Model(&models.SellerPayoutOrder{}).Where("id = ?", p.ID).UpdateColumn("checked_at", now)
	}
	return errors.Join(errs...)
}

// Advance takes one step for p.
func (o *SellerPayoutOrchestrator) Advance(ctx context.Context, p *models.SellerPayoutOrder) error {
	if models.SellerPayoutTerminal(p.Status) || p.Status == models.SellerPayoutNeedsReview {
		return nil
	}
	if o.treasury == nil || o.treasury.Address() == "" {
		return o.park(ctx, p, "treasury lanes disagree on their signer, or none are configured")
	}
	switch p.Status {
	case models.SellerPayoutAwaitingRelease:
		return o.awaitRelease(ctx, p)
	case models.SellerPayoutReceived, models.SellerPayoutBridging:
		return o.advanceBridge(ctx, p)
	case models.SellerPayoutBridged:
		return o.openSell(ctx, p)
	case models.SellerPayoutSelling:
		return o.recoverSelling(ctx, p)
	case models.SellerPayoutOrdered, models.SellerPayoutDepositing:
		return o.advanceDeposit(ctx, p)
	case models.SellerPayoutDeposited:
		return o.followSell(ctx, p)
	}
	return nil
}

// move changes the payout's status only while it is still in one of from, so
// of two ticks (or a tick and a person) only one wins a transition.
func (o *SellerPayoutOrchestrator) move(ctx context.Context, p *models.SellerPayoutOrder, to string, updates map[string]any, from ...string) error {
	if updates == nil {
		updates = map[string]any{}
	}
	updates["status"] = to
	res := o.db.WithContext(ctx).Model(&models.SellerPayoutOrder{}).
		Where("id = ? AND status IN ?", p.ID, from).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errSellerPayoutTransition
	}
	p.Status = to
	if v, ok := updates["received_amount"].(string); ok {
		p.ReceivedAmount = v
	}
	if v, ok := updates["paycrest_order_id"].(uuid.UUID); ok {
		p.PaycrestOrderID = &v
	}
	return nil
}

// park hands the payout to a person, with the reason. It is allowed from any
// state that is not terminal, and records the alert operators watch for.
func (o *SellerPayoutOrchestrator) park(ctx context.Context, p *models.SellerPayoutOrder, reason string) error {
	log.Printf("[seller-payout] ALERT seller_payout_review_total=1 payout=%s order=%s status=%s reason=%s",
		p.ID, p.MarketplaceOrderID, p.Status, RedactAlertText(reason))
	res := o.db.WithContext(ctx).Model(&models.SellerPayoutOrder{}).
		Where("id = ? AND status NOT IN ?", p.ID, []string{models.SellerPayoutPaid, models.SellerPayoutCancelled}).
		Updates(map[string]any{"status": models.SellerPayoutNeedsReview, "failure_reason": truncateReason(reason)})
	if res.Error != nil {
		return res.Error
	}
	p.Status = models.SellerPayoutNeedsReview
	return nil
}

func (o *SellerPayoutOrchestrator) treasuryAddr() string { return o.treasury.Address() }

func (o *SellerPayoutOrchestrator) lane(chainKey string) *TreasuryLane {
	return o.treasury.Lane(chainKey)
}

// ── release ────────────────────────────────────────────────────────────────

// awaitRelease waits for the buyer's confirmation (or an admin's resolve in
// the seller's favour) and then PROVES the treasury was credited: the order's
// release transaction is read for the Transfer to the treasury. The order
// status alone moves nothing.
func (o *SellerPayoutOrchestrator) awaitRelease(ctx context.Context, p *models.SellerPayoutOrder) error {
	var order models.Order
	if err := o.db.WithContext(ctx).First(&order, "id = ?", p.MarketplaceOrderID).Error; err != nil {
		return err
	}
	switch order.Status {
	case models.OrderStatusCancelled, models.OrderStatusRefunded:
		// The escrow went back to the buyer: nothing reached the treasury.
		return o.move(ctx, p, models.SellerPayoutCancelled, map[string]any{"failure_reason": "order " + order.Status}, models.SellerPayoutAwaitingRelease)
	case models.OrderStatusCompleted:
	default:
		return nil
	}
	lane := o.lane(p.ChainKey)
	if lane == nil {
		return o.park(ctx, p, fmt.Sprintf("no treasury lane for %s", p.ChainKey))
	}
	if order.ReleaseTxHash == "" {
		return nil // recorded by the confirm; the next tick reads it
	}
	// The escrow's own record of THIS order's release: paid as seller to the
	// treasury, exactly what the order was priced to pay. A transfer to the
	// treasury in some transaction proves nothing about this order.
	escrowAddr := firstNonEmpty(order.EscrowContractAddress, o.escrowAddr)
	l, err := o.escrow.VerifyOrderEvent(order.ReleaseTxHash, escrowAddr, "OrderReleased", GenerateOrderID(order.ID))
	switch {
	case errors.Is(err, ErrReceiptPending):
		return nil
	case errors.Is(err, ErrOrderEventNotFound):
		return o.park(ctx, p, fmt.Sprintf("release %s holds no OrderReleased for this order: %v", order.ReleaseTxHash, err))
	case err != nil:
		return nil // unreadable now: the next tick looks again
	}
	seller, sellerAmount, err := OrderReleasedTo(l)
	if err != nil {
		return o.park(ctx, p, err.Error())
	}
	if !strings.EqualFold(seller.Hex(), common.HexToAddress(o.treasuryAddr()).Hex()) {
		return o.park(ctx, p, fmt.Sprintf("the escrow released to %s, not the treasury", seller.Hex()))
	}
	want, ok := new(big.Int).SetString(p.ExpectedAmount, 10)
	if !ok || want.Sign() <= 0 || sellerAmount.Cmp(want) != 0 {
		return o.park(ctx, p, fmt.Sprintf("the escrow released %s, the order was priced to pay %q", sellerAmount, p.ExpectedAmount))
	}
	// And the treasury really was credited that much.
	credited, proof, err := o.verifyTransfer(ctx, p.ChainKey, lane.tokenAddress(p.Token), order.ReleaseTxHash, o.treasuryAddr())
	switch proof {
	case transferWait:
		if err != nil && !errors.Is(err, errTransferNoTx) && !errors.Is(err, errTransferNoVerifier) {
			log.Printf("[seller-payout] payout=%s release %s not provable yet: %v", p.ID, order.ReleaseTxHash, err)
		}
		return nil
	case transferRefuted:
		return o.park(ctx, p, fmt.Sprintf("release %s did not credit the treasury: %v", order.ReleaseTxHash, err))
	}
	if credited.Cmp(sellerAmount) < 0 {
		return o.park(ctx, p, fmt.Sprintf("release credited %s of the %s the escrow reported", credited, sellerAmount))
	}
	// What is bridged and sold is what the escrow reported, never more: a
	// transfer-heavy transaction cannot enlarge a payout.
	credited = sellerAmount
	return o.move(ctx, p, models.SellerPayoutReceived, map[string]any{"received_amount": credited.String()}, models.SellerPayoutAwaitingRelease)
}

// ── bridge ─────────────────────────────────────────────────────────────────

// advanceBridge runs the Sonic -> Base leg, reading the leg ledger the way the
// converted-buy machine does: a pending leg is observed, a confirmed one ends
// the step, a bridge that BROADCAST and never confirmed is never retried, and
// only a fresh start (or one whose earlier attempts provably sent nothing) may
// reserve a new attempt.
func (o *SellerPayoutOrchestrator) advanceBridge(ctx context.Context, p *models.SellerPayoutOrder) error {
	legs, err := o.legs.ForOrder(ctx, p.ID)
	if err != nil {
		return err
	}
	if pending := latestPendingLeg(legs); pending != nil {
		if pending.Kind != models.RampLegKindBridge {
			return o.park(ctx, p, fmt.Sprintf("unexpected %s leg in flight during the bridge", pending.Kind))
		}
		return o.observeBridge(ctx, p, pending)
	}
	if done := latestLeg(legs, models.RampLegKindBridge, models.RampLegStatusConfirmed); done != nil {
		return o.move(ctx, p, models.SellerPayoutBridged, nil, models.SellerPayoutReceived, models.SellerPayoutBridging)
	}
	if stranded := broadcastBridgeLeg(legs); stranded != nil {
		return o.park(ctx, p, fmt.Sprintf("bridge attempt %d broadcast as %s but never confirmed", stranded.Attempt, stranded.TxHash))
	}
	// The retry window runs from the first attempt, not from the release.
	if first := bridgeLegByAttempt(legs, 1); first != nil && o.now().After(first.CreatedAt.Add(o.retryWindow)) {
		return o.park(ctx, p, "no bridge route fit inside the retry window")
	}
	return o.startBridge(ctx, p, legs)
}

func (o *SellerPayoutOrchestrator) startBridge(ctx context.Context, p *models.SellerPayoutOrder, _ []models.RampLeg) error {
	ctx = ReservedSend(ctx) // an owed send: it spends the lane reservation held for it
	src, dst := o.lane(p.ChainKey), o.lane("base")
	if src == nil || dst == nil {
		return o.park(ctx, p, "no treasury lane for the bridge")
	}
	amount, ok := new(big.Int).SetString(p.ReceivedAmount, 10)
	if !ok || amount.Sign() <= 0 {
		return o.park(ctx, p, fmt.Sprintf("unreadable received amount %q", p.ReceivedAmount))
	}
	orderUSD, err := o.usdValue(src, p.Token, amount)
	if err != nil {
		return o.park(ctx, p, fmt.Sprintf("cannot value the bridge: %v", err))
	}
	inFlight, err := o.inFlightUSD(ctx, p.ChainKey, p.ID)
	if err != nil {
		return err
	}
	// A cap or kill-switch refusal after the money arrived parks: a payout
	// with no leg is invisible to any stall check, so it must not just error.
	if err := o.treasury.CheckCaps(p.ChainKey, orderUSD, inFlight); err != nil {
		return o.park(ctx, p, fmt.Sprintf("cannot start the bridge: %v", err))
	}

	attempt, err := o.legs.NextAttempt(ctx, p.ID, models.RampLegKindBridge)
	if err != nil {
		return err
	}
	leg := &models.RampLeg{
		OrderID: p.ID, Kind: models.RampLegKindBridge, Attempt: attempt,
		ChainKey: p.ChainKey, Token: strings.ToUpper(p.Token),
		FromAddr: o.treasuryAddr(), ToAddr: o.treasuryAddr(),
		Amount: amount.String(), Status: models.RampLegStatusPending, CreatedAt: o.now().UTC(),
	}
	if existing, err := o.legs.Reserve(ctx, leg); err != nil || existing != nil {
		return err // another tick claimed this attempt
	}
	// Claimed before anything slow: the leg row is the idempotency gate, the
	// status only tells observers where to look.
	if err := o.move(ctx, p, models.SellerPayoutBridging, nil, models.SellerPayoutReceived, models.SellerPayoutBridging); err != nil && !errors.Is(err, errSellerPayoutTransition) {
		return err
	}

	baseUSDC := dst.tokenAddress("USDC")
	route, err := o.lifi.Quote(ctx, LiFiQuoteRequest{
		FromChainID: src.ChainID, ToChainID: dst.ChainID,
		FromToken: src.tokenAddress(p.Token), ToToken: baseUSDC,
		FromAddress: o.treasuryAddr(), ToAddress: o.treasuryAddr(),
		FromAmount: amount.String(),
		Slippage:   0.001, // stable to stable
		Order:      "CHEAPEST",
	})
	if err != nil {
		// Nothing was broadcast, so this attempt is provably dead: fail it so
		// the next tick starts a clean one inside the window.
		if ferr := o.failLeg(detached(ctx), leg.ID, err.Error()); ferr != nil {
			return ferr
		}
		return err
	}
	if route.ToAmountMin == nil {
		return o.failLeg(detached(ctx), leg.ID, "route quotes no guaranteed minimum")
	}
	if l, ok := LiFiCostBps(route); !ok || l+TreasuryGasBps(route, treasuryERC20Gas) > sellerPayoutMaxBridgeBps {
		return o.failLeg(detached(ctx), leg.ID, fmt.Sprintf("route over the %d bps budget", sellerPayoutMaxBridgeBps))
	}
	return o.executeBridgeLeg(ctx, src, leg, p.Token, amount, route, o.treasuryAddr(), dst.ChainID, "evm", baseUSDC)
}

// inFlightUSD is this chain's exposure from payouts whose funds sit in the
// treasury (received through deposited), other than exclude.
func (o *SellerPayoutOrchestrator) inFlightUSD(ctx context.Context, chainKey string, exclude uuid.UUID) (float64, error) {
	var rows []models.SellerPayoutOrder
	if err := o.db.WithContext(ctx).
		Where("chain_key = ? AND id <> ? AND status IN ?", chainKey, exclude, []string{
			models.SellerPayoutReceived, models.SellerPayoutBridging, models.SellerPayoutBridged,
			models.SellerPayoutSelling, models.SellerPayoutOrdered, models.SellerPayoutDepositing,
		}).Find(&rows).Error; err != nil {
		return 0, err // an unknown exposure is not a zero one
	}
	var total float64
	for _, r := range rows {
		n, ok := new(big.Int).SetString(r.ReceivedAmount, 10)
		if !ok {
			continue
		}
		total += unitsToUSD(n, 6) // stablecoins only, 6 dp on every payout chain
	}
	return total, nil
}

func (o *SellerPayoutOrchestrator) observeBridge(ctx context.Context, p *models.SellerPayoutOrder, leg *models.RampLeg) error {
	if leg.TxHash == "" && leg.ApproveTxHash != "" {
		return o.resumeBridgeAfterApprove(ctx, p.ID, leg)
	}
	if leg.TxHash == "" {
		return o.recoverHashless(ctx, p.ID, leg)
	}
	if legPastCeiling([]models.RampLeg{*leg}, o.now().Add(-o.legCeiling)) != nil {
		return o.park(ctx, p, fmt.Sprintf("bridge leg %s pending past the ceiling", leg.ID))
	}
	if o.followLeg(ctx, leg) {
		return nil
	}
	st, err := o.status.Status(ctx, leg.TxHash)
	if err != nil {
		return fmt.Errorf("bridge status for payout %s: %w", p.ID, err)
	}
	switch st.Status {
	case LiFiStatusDone:
		return o.observeBridgeDone(ctx, p, leg, st)
	case LiFiStatusFailed:
		return o.park(ctx, p, fmt.Sprintf("lifi reported bridge attempt %d (%s) failed after it broadcast", leg.Attempt, leg.TxHash))
	}
	return nil // still in flight
}

func (o *SellerPayoutOrchestrator) observeBridgeDone(ctx context.Context, p *models.SellerPayoutOrder, leg *models.RampLeg, st *LiFiTransferStatus) error {
	switch verdict, reason := qualifyLiFiDone(st, o.treasuryAddr()); verdict {
	case lifiDonePark:
		return o.park(ctx, p, fmt.Sprintf("bridge %s reported DONE/%s, not a completed delivery", leg.TxHash, st.Substatus))
	case lifiDoneWrongRecipient:
		return o.park(ctx, p, fmt.Sprintf("bridge %s reported a landing at %s, not the treasury", leg.TxHash, st.ToAddress))
	case lifiDoneWait:
		log.Printf("[seller-payout] payout=%s bridge %s DONE not qualified yet: %s", p.ID, leg.TxHash, reason)
		return nil
	}
	dst := o.lane("base")
	if dst == nil {
		return o.park(ctx, p, "no Base lane to verify the landing")
	}
	// LiFi only says WHICH transaction to read; the Transfer log is the proof.
	landed, proof, err := o.verifyTransfer(ctx, "base", dst.tokenAddress("USDC"), st.DestTxHash, o.treasuryAddr())
	switch proof {
	case transferWait:
		return nil
	case transferRefuted:
		return o.park(ctx, p, fmt.Sprintf("bridge landing %s did not credit the treasury: %v", st.DestTxHash, err))
	}
	if err := o.legs.Confirm(detached(ctx), leg.ID, landed.String(), o.now()); err != nil && !errors.Is(err, ErrLegNotPending) {
		return err
	}
	return o.move(ctx, p, models.SellerPayoutBridged, nil, models.SellerPayoutReceived, models.SellerPayoutBridging)
}

// ── sell ───────────────────────────────────────────────────────────────────

// openSell opens the Paycrest sell for the quoted naira, refund address the
// treasury. The payout is CLAIMED (bridged -> selling) before Paycrest is
// called, so only one tick ever asks; a crash in between is handled by
// recoverSelling, never by asking again.
func (o *SellerPayoutOrchestrator) openSell(ctx context.Context, p *models.SellerPayoutOrder) error {
	legs, err := o.legs.ForOrder(ctx, p.ID)
	if err != nil {
		return err
	}
	bridge := latestLeg(legs, models.RampLegKindBridge, models.RampLegStatusConfirmed)
	if bridge == nil {
		return o.park(ctx, p, "bridged with no confirmed bridge leg")
	}
	landed, ok := new(big.Int).SetString(bridge.AmountReceived, 10)
	if !ok || landed.Sign() <= 0 {
		return o.park(ctx, p, fmt.Sprintf("unreadable bridge landing %q", bridge.AmountReceived))
	}
	if o.sellStarted(legs).Before(o.now().Add(-o.sellRetryWindow)) {
		return o.park(ctx, p, "no venue would take the sale inside the retry window")
	}
	if err := o.move(ctx, p, models.SellerPayoutSelling, nil, models.SellerPayoutBridged); err != nil {
		if errors.Is(err, errSellerPayoutTransition) {
			return nil
		}
		return err
	}
	// The platform absorbs rate drift up to sellerPayoutAbsorbBps of the
	// landing, as USDC deposited beyond it; more than that is a shortfall.
	ceiling := new(big.Int).Add(landed, new(big.Int).Div(new(big.Int).Mul(landed, big.NewInt(sellerPayoutAbsorbBps)), big.NewInt(10000)))
	row, err := o.paycrest.CreateSellForFiat(ctx, PaycrestSellRequest{
		UserID: p.SellerUserID, WalletAddress: o.treasuryAddr(),
		FiatCode: p.Currency, FiatAmount: payFiatAmount(p.FiatTargetKobo),
		ChainKey: "base", Token: "USDC",
		BankCode: p.InstitutionCode, AccountNo: p.AccountNumber, AccountName: p.AccountName,
		SellerPayoutID: &p.ID, KeepSurplus: true,
	}, false, ceiling)
	if err != nil {
		var short *SellShortfallError
		switch {
		case errors.As(err, &short):
			return o.park(ctx, p, fmt.Sprintf("the rate moved beyond the %d bps the platform absorbs: %s more USDC needed", sellerPayoutAbsorbBps, short.MissingBaseUnits))
		case errors.Is(err, ErrSellAttemptMayExist):
			// An order may be fundable: asking again could open a second one.
			return o.park(ctx, p, "a Paycrest order may exist for this attempt: "+err.Error())
		case errors.Is(err, ErrSellRateMoved), errors.Is(err, ErrPaycrestNothingSent):
			// Provably nothing was opened: back to bridged for another try.
			log.Printf("[seller-payout] payout=%s sell refused, nothing sent: %v", p.ID, err)
			return o.move(ctx, p, models.SellerPayoutBridged, nil, models.SellerPayoutSelling)
		}
		// Unknown whether Paycrest heard of it: never retry blindly.
		return o.park(ctx, p, "opening the sale failed with an unknown outcome: "+err.Error())
	}
	return o.move(ctx, p, models.SellerPayoutOrdered, map[string]any{"paycrest_order_id": row.ID}, models.SellerPayoutSelling)
}

// sellStarted is when this payout's sale attempts began: its bridge landing.
func (o *SellerPayoutOrchestrator) sellStarted(legs []models.RampLeg) time.Time {
	if b := latestLeg(legs, models.RampLegKindBridge, models.RampLegStatusConfirmed); b != nil && b.ConfirmedAt != nil {
		return *b.ConfirmedAt
	}
	return o.now()
}

// recoverSelling resolves a payout left in selling: the process died, or the
// write recording the order failed. An order carrying this payout's ID is
// adopted; none is not proof that none exists at Paycrest, so past the grace
// the payout parks rather than opening a second.
func (o *SellerPayoutOrchestrator) recoverSelling(ctx context.Context, p *models.SellerPayoutOrder) error {
	var rows []models.PaycrestOrderRecord
	if err := o.db.WithContext(ctx).Where("seller_payout_id = ?", p.ID).Find(&rows).Error; err != nil {
		return err
	}
	switch {
	case len(rows) == 1:
		return o.move(ctx, p, models.SellerPayoutOrdered, map[string]any{"paycrest_order_id": rows[0].ID}, models.SellerPayoutSelling)
	case len(rows) > 1:
		return o.park(ctx, p, fmt.Sprintf("%d Paycrest orders carry this payout", len(rows)))
	case o.now().After(p.UpdatedAt.Add(sellerPayoutSellingGrace)):
		return o.park(ctx, p, "was opening the sale when it stopped, and no order is recorded")
	}
	return nil
}

// ── deposit ────────────────────────────────────────────────────────────────

// advanceDeposit sends the one deposit leg to the Paycrest receive address.
// At most one deposit is ever live or done (a unique index), a deposit that
// broadcast is never resent, and one that landed is proven from its Transfer
// log before the order is marked paid-for.
func (o *SellerPayoutOrchestrator) advanceDeposit(ctx context.Context, p *models.SellerPayoutOrder) error {
	order, err := o.paycrestOrder(ctx, p)
	if err != nil {
		return err
	}
	legs, err := o.legs.ForOrder(ctx, p.ID)
	if err != nil {
		return err
	}
	if pending := latestPendingLeg(legs); pending != nil {
		if pending.Kind != models.RampLegKindDeposit {
			return o.park(ctx, p, fmt.Sprintf("unexpected %s leg in flight during the deposit", pending.Kind))
		}
		return o.observeDeposit(ctx, p, order, pending)
	}
	if done := latestLeg(legs, models.RampLegKindDeposit, models.RampLegStatusConfirmed); done != nil {
		return o.move(ctx, p, models.SellerPayoutDeposited, nil, models.SellerPayoutOrdered, models.SellerPayoutDepositing)
	}
	return o.sendDeposit(ctx, p, order)
}

func (o *SellerPayoutOrchestrator) paycrestOrder(ctx context.Context, p *models.SellerPayoutOrder) (*models.PaycrestOrderRecord, error) {
	if p.PaycrestOrderID == nil {
		return nil, fmt.Errorf("payout %s has no Paycrest order", p.ID)
	}
	var row models.PaycrestOrderRecord
	if err := o.db.WithContext(ctx).First(&row, "id = ?", *p.PaycrestOrderID).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (o *SellerPayoutOrchestrator) sendDeposit(ctx context.Context, p *models.SellerPayoutOrder, order *models.PaycrestOrderRecord) error {
	ctx = ReservedSend(ctx)
	lane := o.lane("base")
	if lane == nil {
		return o.park(ctx, p, "no Base lane for the deposit")
	}
	if order.SellerPayoutID == nil || *order.SellerPayoutID != p.ID || order.Status != models.PaycrestStatusAwaitingDeposit {
		return o.park(ctx, p, fmt.Sprintf("Paycrest order %s is %q, not an open order of this payout", order.ID, order.Status))
	}
	if !common.IsHexAddress(order.DepositAddress) {
		return o.park(ctx, p, "the Paycrest order has no usable deposit address")
	}
	// A deposit that lands after the order lapsed is an unmatched deposit.
	if order.ValidUntil != nil && o.now().Add(sellerPayoutOrderMargin).After(*order.ValidUntil) {
		return o.park(ctx, p, "the Paycrest order is about to expire before it was funded")
	}
	decimals, err := lane.Native.GetTokenDecimals(lane.tokenAddress("USDC"))
	if err != nil {
		return o.park(ctx, p, fmt.Sprintf("could not read USDC decimals on Base: %v", err))
	}
	amount, err := DecimalToBaseUnits(order.TokenAmount, decimals)
	if err != nil || amount.Sign() <= 0 {
		return o.park(ctx, p, fmt.Sprintf("unreadable deposit amount %q", order.TokenAmount))
	}
	attempt, err := o.legs.NextAttempt(ctx, p.ID, models.RampLegKindDeposit)
	if err != nil {
		return err
	}
	leg := &models.RampLeg{
		OrderID: p.ID, Kind: models.RampLegKindDeposit, Attempt: attempt,
		ChainKey: "base", Token: "USDC", FromAddr: o.treasuryAddr(), ToAddr: order.DepositAddress,
		Amount: amount.String(), Status: models.RampLegStatusPending, CreatedAt: o.now().UTC(),
	}
	if existing, err := o.legs.Reserve(ctx, leg); err != nil || existing != nil {
		return err
	}
	if err := o.move(ctx, p, models.SellerPayoutDepositing, nil, models.SellerPayoutOrdered, models.SellerPayoutDepositing); err != nil && !errors.Is(err, errSellerPayoutTransition) {
		return err
	}
	txHash, attempted, err := o.treasury.TransferToken(o.legSend(ctx, leg.ID, "deposit"), lane, "USDC", order.DepositAddress, amount)
	if err != nil {
		if attempted {
			// Might have landed: the leg stays pending so no second deposit runs.
			log.Printf("[seller-payout] ALERT seller_payout_deposit_uncertain_total=1 payout=%s leg=%s err=%v", p.ID, leg.ID, err)
			return nil
		}
		if ferr := o.failLeg(detached(ctx), leg.ID, err.Error()); ferr != nil {
			return ferr
		}
		return err
	}
	log.Printf("[seller-payout] payout=%s deposit sent %s USDC to %s tx=%s", p.ID, amount, order.DepositAddress, txHash)
	return nil
}

func (o *SellerPayoutOrchestrator) observeDeposit(ctx context.Context, p *models.SellerPayoutOrder, order *models.PaycrestOrderRecord, leg *models.RampLeg) error {
	if leg.TxHash == "" {
		return o.recoverHashless(ctx, p.ID, leg)
	}
	if legPastCeiling([]models.RampLeg{*leg}, o.now().Add(-o.legCeiling)) != nil {
		return o.park(ctx, p, fmt.Sprintf("deposit leg %s pending past the ceiling", leg.ID))
	}
	if o.followLeg(ctx, leg) {
		return nil
	}
	credited, proof, err := o.verifyTransfer(ctx, "base", o.lane("base").tokenAddress("USDC"), leg.TxHash, order.DepositAddress)
	switch proof {
	case transferWait:
		return nil
	case transferRefuted:
		return o.park(ctx, p, fmt.Sprintf("deposit %s did not credit the Paycrest address: %v", leg.TxHash, err))
	}
	if err := o.legs.Confirm(detached(ctx), leg.ID, credited.String(), o.now()); err != nil && !errors.Is(err, ErrLegNotPending) {
		return err
	}
	// Paycrest reads a treasury deposit as unmatched unless the payment is
	// claimed, so the claim is made here, now that the deposit is proven.
	now := o.now()
	if err := o.db.WithContext(ctx).Model(&models.PaycrestOrderRecord{}).
		Where("id = ? AND payment_claimed_at IS NULL", order.ID).UpdateColumn("payment_claimed_at", now).Error; err != nil {
		return err
	}
	return o.move(ctx, p, models.SellerPayoutDeposited, nil, models.SellerPayoutOrdered, models.SellerPayoutDepositing)
}

// ── settle ─────────────────────────────────────────────────────────────────

// followSell reads the Paycrest order the poller keeps in sync. Settled is
// paid; refunded, expired, unmatched, failed or cancelled all park — what to
// do with money that came back is a person's call, never an automatic wallet
// payment; anything else waits.
func (o *SellerPayoutOrchestrator) followSell(ctx context.Context, p *models.SellerPayoutOrder) error {
	order, err := o.paycrestOrder(ctx, p)
	if err != nil {
		return err
	}
	switch order.Status {
	case models.PaycrestStatusSettled:
		return o.move(ctx, p, models.SellerPayoutPaid, nil, models.SellerPayoutDeposited)
	case models.PaycrestStatusRefunded, models.PaycrestStatusExpired, models.PaycrestStatusUnmatched,
		models.PaycrestStatusFailed, models.PaycrestStatusCancelled:
		return o.park(ctx, p, fmt.Sprintf("the Paycrest order ended %s after the deposit", order.Status))
	}
	if o.now().After(p.UpdatedAt.Add(sellerPayoutSettleAfter)) {
		return o.park(ctx, p, fmt.Sprintf("the Paycrest order is still %s a day after the deposit", order.Status))
	}
	return nil
}
