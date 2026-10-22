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

// legRunner is the leg handling every treasury machine shares: the leg ledger,
// the treasury lanes, the per-chain transfer verifiers and the clock. The
// converted-buy orchestrator and the seller-payout machine both embed it, so a
// leg is reserved, sent, followed, recovered and failed by one piece of code.
// ref is whatever the leg's OrderID names (a Paycrest order, a payout); it is
// used only for logging.
type legRunner struct {
	db       *gorm.DB
	treasury *Treasury
	legs     rampLegStore
	// transfers is keyed by chain key: LogTransferVerifier wraps ONE RPC
	// client, so a single verifier could only ever prove a delivery on its own
	// chain and would silently never confirm anything on any other.
	transfers map[string]ERC20TransferVerifier
	now       func() time.Time
}

// recoverHashless resolves a pending leg that never recorded a hash, after
// hashlessRecoverAfter: each of its operations (the call, its route, its
// approve) is retired through RetireOp under the lane lock — one that holds
// an allocation is ADOPTED (its hash written on the leg, the approve's as
// approve_tx_hash) and the leg goes on in flight; when none does, every key
// is tombstoned for good and the leg is freed for a new attempt. A store
// that cannot answer leaves it for the next tick: never a permanent wedge,
// never a guess.
func (o *legRunner) recoverHashless(ctx context.Context, ref uuid.UUID, leg *models.RampLeg) error {
	if leg.TxHash != "" || leg.ApproveTxHash != "" || o.now().Before(leg.CreatedAt.Add(hashlessRecoverAfter)) {
		return nil
	}
	lane := o.treasury.Lane(leg.ChainKey)
	if lane == nil || lane.SponsorLane == nil || lane.KeyLane == nil || lane.Signer == nil {
		return nil
	}
	op := "ramp_leg:" + leg.ID.String()
	for _, k := range []string{op + ":route", op, op + ":approve"} {
		a, err := lane.KeyLane.RetireOp(ctx, lane.Signer.Address(), k, "", RetirePermanent)
		if err != nil {
			return nil // unknowable now: next tick
		}
		if a == nil {
			continue
		}
		h := lane.KeyLane.currentHash(ctx, a)
		if h == "" {
			return nil
		}
		log.Printf("[ramp-conversion] order=%s leg=%s adopting %s from the key lane (op %s)", ref, leg.ID, h, k)
		if strings.HasSuffix(k, ":approve") {
			return ignoreNotPending(o.legs.SetApproveHash(ctx, leg.ID, h))
		}
		return ignoreNotPending(o.legs.SetTxHash(ctx, leg.ID, h, leg.LiFiTool))
	}
	log.Printf("[ramp-conversion] order=%s leg=%s hashless and nothing committed: freed for a new attempt", ref, leg.ID)
	return ignoreNotPending(o.legs.FailExpected(detached(ctx), leg.ID, "", "not broadcast: nothing committed (proven by the key lane)"))
}

// resumeBridgeAfterApprove: a bridge leg whose approve was committed but
// whose route never was. Past the grace the leg's ':route' operation is
// RETIRED in the lane store (atomic: refused if it holds an allocation) and
// only then is the attempt failed (no funds moved: only an allowance was
// set), so the next tick sends the route on a new attempt — and the old
// route can never go out too.
func (o *legRunner) resumeBridgeAfterApprove(ctx context.Context, ref uuid.UUID, leg *models.RampLeg) error {
	if o.now().Before(leg.CreatedAt.Add(bridgeRouteGrace)) {
		return nil // the approve is in flight: the leg is too
	}
	lane := o.treasury.Lane(leg.ChainKey)
	if lane == nil || lane.SponsorLane == nil || lane.KeyLane == nil {
		return nil
	}
	// Atomic, under the lane lock: either the route was allocated (the leg
	// stays in flight on it) or its operation key is tombstoned so no slow or
	// concurrent attempt of it can ever be sent — only then is the leg freed,
	// and only while it still records no route hash.
	held, err := lane.KeyLane.RetireOp(ctx, lane.Signer.Address(), "ramp_leg:"+leg.ID.String()+":route", "", RetirePermanent)
	if err != nil {
		return err
	}
	if held != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_route_unrecorded_total=1 order=%s leg=%s — the route is committed but not on the leg", ref, leg.ID)
		return nil
	}
	log.Printf("[ramp-conversion] order=%s bridge#%d approve %s committed, route never sent: retired; retrying the route on a new attempt",
		ref, leg.Attempt, leg.ApproveTxHash)
	if err := o.legs.FailUnsent(detached(ctx), leg.ID, "approve only: the route was never sent"); err != nil && !errors.Is(err, ErrLegNotPending) {
		return err
	}
	return nil
}

// followLeg runs the treasury's followCall on a leg (its recorded hash moves
// to the signing of the same call that mined); true when the lane cancelled
// the call — the leg is then failed (it never executed) so the next tick
// starts a new attempt.
func (o *legRunner) followLeg(ctx context.Context, leg *models.RampLeg) bool {
	if leg == nil || leg.TxHash == "" || o.treasury == nil || o.db == nil {
		return false
	}
	var cancelled bool
	leg.TxHash, cancelled = o.treasury.followCall(ctx, leg.ChainKey, nil, o.db, &models.RampLeg{}, "tx_hash", leg.ID, leg.TxHash)
	if !cancelled {
		return false
	}
	// Give up only through RetireOp: nothing of this leg's operations may be
	// live (a late attempt) or executed.
	op := "ramp_leg:" + leg.ID.String()
	// The retry is a NEW leg (a new key): the old leg's operations are
	// retired for good.
	if !o.treasury.giveUp(ctx, leg.ChainKey, op, leg.TxHash, RetirePermanent) || !o.treasury.giveUp(ctx, leg.ChainKey, op+":route", "", RetirePermanent) {
		return false // in flight after all: keep observing
	}
	log.Printf("[ramp-conversion] ALERT ramp_conversion_leg_cancelled_total=1 leg=%s kind=%s tx=%s — the key lane cancelled it", leg.ID, leg.Kind, leg.TxHash)
	if err := o.legs.FailExpected(detached(ctx), leg.ID, leg.TxHash, "cancelled: "+ErrCallCancelled.Error()); err != nil && !errors.Is(err, ErrLegNotPending) {
		log.Printf("[ramp-conversion] fail cancelled leg %s: %v", leg.ID, err)
	}
	return true
}

// legSend is the send context of a treasury leg: the leg is the operation
// (a retry never gets a new nonce), and its hash (and LiFi tool) is written
// on the still-pending leg in the key lane's allocation transaction — the leg
// and the signed call commit together, so a refund or re-drive that won the
// leg first means nothing is signed, and a committed call always shows on its
// leg.
func (o *legRunner) legSend(ctx context.Context, legID uuid.UUID, tool string) context.Context {
	return WithPreBroadcast(WithOperationRef(ctx, "ramp_leg:"+legID.String()), func(hctx context.Context, _ uint64, hash string) error {
		return o.legs.SetTxHash(hctx, legID, hash, tool)
	})
}

// failLeg records a leg as failed, treating "already terminal" as another tick
// having got there first rather than as an error of its own.
// failLeg fails a leg that recorded no transaction (a refusal before any
// commit — the lane proved it, RetireOp — or a pre-send check).
func (o *legRunner) failLeg(ctx context.Context, id uuid.UUID, reason string) error {
	if err := o.legs.FailExpected(ctx, id, "", reason); err != nil && !errors.Is(err, ErrLegNotPending) {
		return err
	}
	return nil
}

func (o *legRunner) verifierFor(chainKey string) ERC20TransferVerifier {
	return o.transfers[strings.ToLower(strings.TrimSpace(chainKey))]
}

// usdValue converts base units to a USD figure for the caps and the top-up
// ceiling only — never for anything that is actually paid out.
func (o *legRunner) usdValue(lane *TreasuryLane, token string, amount *big.Int) (float64, error) {
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

// executeBridgeLeg sends a bridge leg that is already reserved and priced:
// the route's calldata must pay the recipient on the destination chain
// (refused before anything leaves the treasury), the approve and the route
// each record their hash on the leg through the key lane, and an uncertain
// broadcast keeps the leg pending so no second route can run. A refusal that
// provably sent nothing fails the leg so a new attempt can start.
func (o *legRunner) executeBridgeLeg(ctx context.Context, lane *TreasuryLane, leg *models.RampLeg, token string,
	bridge *big.Int, route *LiFiRoute, recipient string, dstChainID int64, dstFamily, dstToken string) error {
	// The route's `to` is allowlisted inside ExecuteRoute, but the calldata
	// alone decides who the diamond pays. Checked here, where the order's
	// wallet and destination are known — LiFi's status would only report a
	// wrong landing after the treasury's funds had already left.
	//
	// The address FAMILY decides how the receiver is read: a Solana route
	// carries LiFi's placeholder in BridgeData.receiver and the real recipient
	// inside the facet's own data, so checking it with the EVM decoder would
	// compare the order's wallet against that placeholder and pass anything.
	// The destination token goes through too — on Solana an SPL delivery lands
	// in a token account, which only the mint can identify.
	if err := checkLiFiDeliveryTo(route.Data, lane.ChainID, dstChainID, recipient, dstFamily, dstToken); err != nil {
		log.Printf("[ramp-conversion] ALERT ramp_conversion_route_delivery_refused_total=1 order=%s bridge#%d tool=%s err=%v",
			leg.OrderID, leg.Attempt, route.Tool, err)
		return o.failLeg(detached(ctx), leg.ID, err.Error())
	}

	// The approve (if one is due) records its hash on the leg in its own
	// allocation transaction; the route records the leg's tx hash in its.
	sendCtx := WithApproveHook(o.legSend(ctx, leg.ID, route.Tool), func(hctx context.Context, _ uint64, hash string) error {
		return o.legs.SetApproveHash(hctx, leg.ID, hash)
	})
	txHash, broadcastAttempted, err := o.treasury.ExecuteRoute(sendCtx, lane, token, bridge, route)
	if err != nil {
		if broadcastAttempted {
			// Might have landed: keep the leg pending so no second route runs.
			log.Printf("[ramp-conversion] ALERT ramp_conversion_broadcast_uncertain_total=1 order=%s leg=%s err=%v",
				leg.OrderID, leg.ID, err)
			return nil
		}
		if ferr := o.failLeg(detached(ctx), leg.ID, err.Error()); ferr != nil {
			return ferr
		}
		return errIfOutOfGas(err)
	}
	log.Printf("[ramp-conversion] order=%s bridge#%d chain=%s->%d token=%s sent=%s tx=%s tool=%s",
		leg.OrderID, leg.Attempt, lane.ChainKey, dstChainID, token, bridge, txHash, route.Tool)
	return nil
}

// errTransferNoTx and errTransferNoVerifier say why verifyTransfer could not
// even look: there is no hash yet, or no verifier for the chain. Both mean
// "wait", never "refused".
var (
	errTransferNoTx       = errors.New("no transfer hash to verify")
	errTransferNoVerifier = errors.New("no transfer verifier for the chain")
)

// transferProof is what verifyTransfer concluded.
type transferProof int

const (
	// transferLanded: the transfer credited the recipient; the amount is proven.
	transferLanded transferProof = iota
	// transferWait: not provable yet (not mined, unreadable, or credited nothing).
	transferWait
	// transferRefuted: it reverted or paid someone else; waiting will not fix it.
	transferRefuted
)

// verifyTransfer proves, from the chain's own Transfer logs, how much of
// tokenAddr txHash credited to recipient on chainKey. A mined receipt, a
// bridge's word or an API status is never the proof; this is. The callers own
// what a wait or a refusal means for their state machine.
func (o *legRunner) verifyTransfer(ctx context.Context, chainKey, tokenAddr, txHash, recipient string) (*big.Int, transferProof, error) {
	if txHash == "" {
		return nil, transferWait, errTransferNoTx
	}
	verifier := o.verifierFor(chainKey)
	if verifier == nil {
		return nil, transferWait, errTransferNoVerifier
	}
	credited, err := verifier.VerifyTransfer(ctx, txHash, tokenAddr, recipient)
	switch ClassifyTransfer(credited, err) {
	case TransferRefuted:
		return nil, transferRefuted, err
	case TransferUnproven:
		return nil, transferWait, err
	}
	return credited, transferLanded, nil
}

// lifiDoneVerdict is qualifyLiFiDone's answer about a DONE bridge status.
type lifiDoneVerdict int

const (
	// lifiDoneOK: completed, with an amount and the expected recipient.
	lifiDoneOK lifiDoneVerdict = iota
	// lifiDonePark: DONE but PARTIAL or REFUNDED — not a delivery; a human decides.
	lifiDonePark
	// lifiDoneWait: DONE that cannot be qualified yet; never settle on it.
	lifiDoneWait
	// lifiDoneWrongRecipient: it reports a landing somewhere else.
	lifiDoneWrongRecipient
)

// qualifyLiFiDone decides whether a DONE LiFi status may be followed by the
// on-chain landing check. LiFi's word is only a lookup; the second return is
// the alert suffix for a wait. The recipient is the address the route's
// calldata was already checked against.
func qualifyLiFiDone(st *LiFiTransferStatus, recipient string) (lifiDoneVerdict, string) {
	switch st.Substatus {
	case LiFiSubstatusCompleted:
		// The only substatus that means the recipient got what was quoted.
	case LiFiSubstatusPartial, LiFiSubstatusRefunded:
		return lifiDonePark, "bridge_not_completed"
	default:
		return lifiDoneWait, "done_unqualified"
	}
	if st.ReceivedBaseUnits == nil || st.ReceivedBaseUnits.Sign() <= 0 {
		return lifiDoneWait, "done_without_amount"
	}
	// An unstated recipient is not ours; it is an unknown, and an unknown
	// recipient must never settle anything.
	if st.ToAddress == "" {
		return lifiDoneWait, "done_without_recipient"
	}
	if !strings.EqualFold(st.ToAddress, recipient) {
		return lifiDoneWrongRecipient, "wrong_recipient"
	}
	return lifiDoneOK, ""
}
