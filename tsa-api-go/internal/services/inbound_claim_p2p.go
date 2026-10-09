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

// P2P trades as a source of inbound naira claims, plus the instant forward of
// the merchant's proceeds to their own bank (central-account design §6.3,
// decision 7).
//
// ── The thing this file is easiest to get wrong ──
//
// A P2P trade has two people and they are NOT interchangeable here:
//
//	the BUYER    sends the naira, so THEIR name is on the bank transfer and
//	             theirs is the name the matcher compares against. That makes the
//	             buyer the claim's UserID.
//	the MERCHANT receives the naira, so THEIR ledger is credited and THEIR
//	             settlement bank is forwarded to.
//
// Everywhere below, `claim.UserID` is the buyer and `pm` is the merchant.
// Conflating them credits the wrong party's money to the other one.

// p2pForwardRefPrefix namespaces the reference we send the payout provider, so
// a disburse webhook can be routed back to a settlement forward rather than
// hunted for among trade ids. HandlePayoutStatus's own keys are trade UUIDs and
// payout ids, neither of which this can collide with.
const p2pForwardRefPrefix = "p2pfwd:"

// p2pForwardStaleAfter is how long a submitted-but-unconfirmed forward may sit
// before it is treated as never-acknowledged and alerted on.
//
// This is the failure mode with no webhook of its own: the provider took the
// transfer, said nothing further, and there is no event to wait for. Nothing
// else in the system would ever notice, which is precisely how value goes
// missing quietly on the MCGP sell leg.
const p2pForwardStaleAfter = 30 * time.Minute

// p2pForwardRedriveAfter is how long a `pending` forward is left alone before
// the sweep re-drives it.
//
// It exists to keep the sweep off the attempt Accept has just made
// synchronously. Forward re-reads the row and refuses anything that is not
// `pending`, but that read and the provider call are not one atomic step, so
// two attempts overlapping could submit the same transfer twice — and a
// double-paid merchant is not recoverable the way an unpaid one is.
const p2pForwardRedriveAfter = 2 * time.Minute

// p2pForwardMaxAttempts is how many times a forward may be held back before it
// is escalated to the failed queue instead of retried again.
//
// At the sweep's 5-minute cadence this is roughly half an hour of transient
// outage absorbed silently, which is the point: a provider blip should not need
// a human. Past it the cause is not transient, and a row that retries forever
// is as invisible to an operator as one nothing drives at all — the failure
// this whole file exists to prevent.
const p2pForwardMaxAttempts = 6

// p2pForwardSweepBatch caps how many rows a single sweep or operator listing
// pulls back.
//
// The table only ever grows — nothing is deleted, and `failed` is terminal — so
// an unbounded read is a query whose cost rises forever against a fixed
// 5-minute cadence. A provider outage is precisely when the backlog is largest
// and precisely when loading all of it would turn one bad afternoon into an
// outage of ours. Whatever a tick leaves behind is still there for the next
// one, because every one of these reads is ordered oldest-first.
const p2pForwardSweepBatch = 200

// ErrForwardNoSettlementAccount means the merchant has nowhere to be paid.
// Their proceeds stay on the ledger; this is a merchant-record problem, not a
// money problem.
var ErrForwardNoSettlementAccount = errors.New("p2p forward: merchant has no settlement bank configured")

// errP2PReleaseDeferred marks the one settle failure Accept must NOT report as
// an error: the trade has left pending_payment, so OpenClaims will never offer
// it again, AND it is in a state a background job still comes back for — see
// p2pReleaseIsRecoverable for which states those are and why.
//
// It is deliberately NOT every failed release. Wrapping a cancelled or disputed
// trade in this makes Accept return nil and the matcher stamp the credit
// `matched`, while no sweep's filter ever selects that trade — merchant paid,
// buyer's crypto escrowed forever, no alert. settleTrade re-reads the status
// precisely so this stays the narrow case it claims to be.
//
// Surfacing it would leave the credit `unmatched` for a matcher that can no
// longer find its claim, and the next tick would hold the credit as "no open
// claim at this amount" — a reason that is false, that BindManually refuses to
// act on because the claim is closed, and that leaves an operator only refund
// or ignore. Refunding returns naira for crypto PMRecoveryJob is about to
// release.
var errP2PReleaseDeferred = errors.New("release deferred to pm recovery")

// P2PTradeClaimSource implements ClaimSource for P2P trades awaiting a buyer's
// naira. See the file header for the buyer/merchant distinction.
type P2PTradeClaimSource struct {
	db     *gorm.DB
	pm     *PMService
	trades *P2PTradeService
	signer ChainSender

	// forwarder sweeps the merchant's proceeds onward. Optional: without it the
	// ledger credit still happens and the money simply waits for the existing
	// manual withdrawal path, which is the pre-decision-7 behaviour.
	forwarder *P2PSettlementForwarder
}

// NewP2PTradeClaimSource wires the P2P product into the inbound matcher.
func NewP2PTradeClaimSource(db *gorm.DB, pm *PMService, trades *P2PTradeService, signer ChainSender) *P2PTradeClaimSource {
	return &P2PTradeClaimSource{db: db, pm: pm, trades: trades, signer: signer}
}

// SetForwarder enables the instant per-trade forward to the merchant's bank.
func (s *P2PTradeClaimSource) SetForwarder(f *P2PSettlementForwarder) { s.forwarder = f }

// p2pClaimRow is the projection OpenClaims scans. Deliberately not the full
// trade: the matcher needs four fields and loading whole trades to build them
// would read every open trade's proof URLs and tx hashes on every poll tick.
type p2pClaimRow struct {
	ID           uuid.UUID
	BuyerID      uuid.UUID
	FiatAmount   string
	FiatCurrency string
	CreatedAt    time.Time
}

// OpenClaims returns every P2P trade currently waiting on a buyer's transfer.
//
// Scoped to merchant (PM) ads on purpose. A manual P2P trade has the buyer
// paying an individual seller's own bank directly — that money never touches
// the company account, so offering it as a claim would let an unrelated credit
// settle a trade nobody was paid for.
//
// The on-chain-open filter is the same one the existing PM buy flow uses: a
// trade with no open tx has no escrowed crypto behind it, so settling it would
// release value that was never locked.
func (s *P2PTradeClaimSource) OpenClaims(ctx context.Context, currency string) ([]InboundClaim, error) {
	cur := normaliseCurrency(currency)
	if cur == "" {
		return nil, errors.New("p2p claims: empty currency")
	}
	// Guarded like the MCGP sibling. An unwired source that panics here takes
	// the whole poll tick with it, losing every other product's attribution
	// too; an error is something the matcher can log and carry on past.
	if s == nil || s.db == nil {
		return nil, errors.New("p2p claims: not configured")
	}

	var rows []p2pClaimRow
	if err := s.db.WithContext(ctx).
		Table("p2p_trades").
		Scopes(notPoolSlice).
		Select("p2p_trades.id, p2p_trades.buyer_id, p2p_trades.fiat_amount, p2p_trades.fiat_currency, p2p_trades.created_at").
		Joins("JOIN p2p_ads ON p2p_ads.id = p2p_trades.ad_id").
		Where("p2p_ads.pm_id IS NOT NULL").
		Where("p2p_ads.side = ?", models.P2PAdSideSell).
		Where("p2p_trades.status = ?", models.P2PTradeStatusPendingPayment).
		Where("p2p_trades.funding = ?", models.P2PTradeFundingPooled).
		Where("p2p_trades.open_tx_hash <> ?", "").
		Where("p2p_trades.fiat_currency = ?", cur).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	claims := make([]InboundClaim, 0, len(rows))
	for _, r := range rows {
		kobo, ok := ngnToKobo(r.FiatAmount)
		if !ok || kobo <= 0 {
			// A claim we cannot price is not a claim. Skipping it means the
			// credit holds for an operator, which is the right outcome —
			// offering it at a guessed amount could bind the wrong trade.
			log.Printf("[inbound p2p] trade=%s has unusable fiat_amount %q; not offered as a claim", r.ID, r.FiatAmount)
			continue
		}
		// Rounded with the SAME helper the matcher rounds the credit with, so
		// the two sides cannot round in opposite directions. Rounded rather
		// than skipped: a fractional trade predates the cutover, and dropping
		// it would hide it from the matcher entirely — including from the hold
		// reason an operator reads when deciding where the money goes.
		kobo = RoundKoboToNaira(kobo)
		claims = append(claims, InboundClaim{
			Type: InboundClaimTypeP2PTrade,
			ID:   r.ID,
			// The BUYER. They send the money, so the transfer carries their
			// name — see the file header.
			UserID:     r.BuyerID,
			AmountKobo: kobo,
			// The trade's OWN currency, not the argument echoed back: echoing
			// it would make the matcher's assertion tautological and re-hide
			// the mismatch it exists to catch.
			Currency: normaliseCurrency(r.FiatCurrency),
			OpenedAt: r.CreatedAt,
		})
	}
	return claims, nil
}

// Accept settles the trade the matcher bound this credit to and pays the
// merchant.
//
// Order of operations, and the reason for each step's position:
//
//  1. Claim the credit's ProviderRef durably, in the SAME transaction as the
//     merchant's ledger credit. The row is the idempotency key; the credit is
//     what it authorises. Together or not at all.
//  2. Settle the trade on-chain (markPaid → release), giving the buyer their
//     crypto. This runs AFTER the ledger credit deliberately: the merchant's
//     claim on the naira must exist before anything else can fail.
//  3. Forward the proceeds to the merchant's bank, which never debits the
//     ledger by itself — see P2PSettlementForwarder.
//
// Idempotent on credit.ProviderRef via the unique index on
// p2p_settlement_forwards.provider_ref: a replayed credit finds its own row and
// returns, having moved nothing.
func (s *P2PTradeClaimSource) Accept(ctx context.Context, claimID uuid.UUID, credit *models.InboundCredit) error {
	if credit == nil {
		return errors.New("p2p accept: nil credit")
	}
	ref := strings.TrimSpace(credit.ProviderRef)
	if ref == "" {
		return errors.New("p2p accept: credit has no provider ref")
	}

	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", claimID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTradeNotFound
		}
		return err
	}
	if err := hideSlice(&trade); err != nil {
		return err // a pool slice is settled only by the pool's own path (§12.3)
	}
	// Re-asserted from the trade itself rather than trusted from OpenClaims.
	// The manual-assign path reaches here with a hand-typed claim id and never
	// runs that filter, and crediting a merchant's naira ledger out of a
	// transfer denominated in something else books money that never landed.
	if err := assertClaimCurrency("p2p", claimID, trade.FiatCurrency, credit); err != nil {
		return err
	}
	pm, err := s.merchantForTrade(ctx, trade.AdID)
	if err != nil {
		return err
	}

	// Nothing below this line may move money for a trade that has left
	// pending_payment. A fresh Accept should only ever see that status, because
	// both the matcher and BindManually reach here through OpenClaims — so any
	// other status means the trade moved between the claim being gathered and
	// this call. The timeout watchdog cancelling an expired trade on its
	// one-minute ticker is the real way that happens, and a transfer landing on
	// the deadline is exactly when the window is open.
	//
	// The damage that follows is silent, which is why the check is here rather
	// than deeper: AutoRelease refuses a cancelled or disputed trade, so the
	// buyer's crypto stays escrowed forever while the merchant has been
	// credited AND forwarded to their bank, and the credit reads `matched` with
	// nothing left to alert anyone.
	//
	// Unless this ref already owns this trade's forward row. That is a replay
	// of an attempt that credited and then failed to finish settling, and it is
	// the one case that MUST be re-driven — refusing it would wedge a half-done
	// settle forever with the money already booked and nothing coming back for
	// it.
	//
	// Scoped to this trade as well as this ref: a ref that settled some OTHER
	// trade is not a replay of this one, and admitting it would clear the guard
	// and then hand settleTrade a trade no credit ever paid for.
	replay, err := s.forwardFor(ctx, trade.ID, ref)
	if err != nil {
		return err
	}
	if trade.Status != models.P2PTradeStatusPendingPayment && replay == nil {
		return fmt.Errorf("p2p accept: trade %s is %s, not pending_payment; credit %s must not settle it",
			trade.ID, trade.Status, ref)
	}

	// The merchant's own limits, which the VA path has always applied
	// (HandleInbound steps 3 and 6). Only on a fresh admission: a replay
	// already passed them and has booked the money, so re-asking would strand
	// a credited ledger behind a hold. And not on an operator's assignment,
	// which is the answer to exactly this question.
	if replay == nil && !isManualBind(ctx) {
		if err := s.reviewGate(ctx, pm, &trade); err != nil {
			return err
		}
	}

	// fresh gates the LEDGER only. Everything after it is idempotent and is
	// re-driven on a replay on purpose: if a first attempt credited the
	// merchant and then failed to settle, short-circuiting here would leave the
	// trade wedged at pending_payment forever with the money already booked,
	// and nothing would ever come back to finish it.
	forward, _, err := s.claimAndCredit(ctx, &trade, pm, ref)
	if err != nil {
		return err
	}

	// Settle the trade. A failure here leaves the merchant credited and the
	// buyer's crypto still escrowed — recoverable, and the safe direction: the
	// alternative is releasing crypto against a ledger entry that does not
	// exist yet.
	//
	// A failed release is not an Accept failure ONLY when settleTrade has
	// re-read the trade and found a state a recovery job still owns, which is
	// what errP2PReleaseDeferred means. Then returning the error would strand
	// the credit on a hold reason that is not true. Every other failed
	// release — including a trade cancelled or disputed under us — comes back
	// as an ordinary error, because nothing would ever come back for it and a
	// `matched` credit over an unreleased trade alerts nobody.
	if err := s.settleTrade(ctx, trade.ID, ref); err != nil {
		if !errors.Is(err, errP2PReleaseDeferred) {
			return fmt.Errorf("p2p accept: settle trade %s: %w", trade.ID, err)
		}
		log.Printf("[inbound p2p] release deferred for trade=%s pm=%s; merchant credited, pm-recovery owns the release: %v",
			trade.ID, pm.ID, err)
	}

	if s.forwarder == nil {
		return nil // no instant forward wired; proceeds wait for manual withdrawal
	}
	// A forward failure is NOT an Accept failure. The credit is attributed, the
	// trade is settled and the merchant is credited — all of which stand. The
	// forward raises its own alert and leaves a row for an operator; erroring
	// here would make the matcher re-drive an already-settled credit.
	if err := s.forwarder.Forward(ctx, forward.ID); err != nil {
		log.Printf("[inbound p2p] forward deferred for trade=%s pm=%s: %v", trade.ID, pm.ID, err)
	}
	return nil
}

// reviewGate turns the merchant's auto-settle refusal into a hold, and holds
// a trade too small for the provider to transfer onward. A daily cap that
// cannot be evaluated is an ordinary error instead: that one IS transient, and
// the next tick should simply ask again.
func (s *P2PTradeClaimSource) reviewGate(ctx context.Context, pm *models.PaymentMerchant, trade *models.P2PTrade) error {
	refusal, err := s.pm.CheckAutoSettle(ctx, pm, trade)
	if err != nil {
		return fmt.Errorf("p2p accept: %w", err)
	}
	if refusal != nil {
		return &ClaimNeedsReviewError{Code: refusal.Code, Reason: refusal.Reason}
	}
	gross, ok := ngnToKobo(trade.FiatAmount)
	if !ok {
		return fmt.Errorf("p2p accept: trade %s has unusable fiat_amount %q", trade.ID, trade.FiatAmount)
	}
	if gross < defaultMinSellNgnKobo {
		return &ClaimNeedsReviewError{
			Code: models.InboundHoldPayoutBelowMinimum,
			Reason: fmt.Sprintf("P2P trade %s: merchant %q would be sent ₦%s, under the ₦%s transfer minimum. Refund the sender, or assign this credit and pay the merchant by hand",
				trade.ID, pm.Name, koboToNGNString(gross), koboToNGNString(defaultMinSellNgnKobo)),
		}
	}
	return nil
}

// merchantForTrade resolves the merchant behind a trade's ad.
func (s *P2PTradeClaimSource) merchantForTrade(ctx context.Context, adID uuid.UUID) (*models.PaymentMerchant, error) {
	var ad models.P2PAd
	if err := s.db.WithContext(ctx).First(&ad, "id = ?", adID).Error; err != nil {
		return nil, fmt.Errorf("p2p accept: load ad %s: %w", adID, err)
	}
	if ad.PMID == nil {
		// OpenClaims only offers PM ads, so reaching here means the matcher was
		// handed a claim id this source never produced.
		return nil, errors.New("p2p accept: trade is not on a merchant ad")
	}
	pm, err := s.pm.GetByID(ctx, *ad.PMID)
	if err != nil {
		return nil, fmt.Errorf("p2p accept: load merchant %s: %w", *ad.PMID, err)
	}
	return pm, nil
}

// forwardFor reports whether this exact credit has already claimed this exact
// trade, returning nil when it has not.
//
// Both keys together, never either alone: provider_ref and trade_id are each
// unique on their own, so a one-key match can be some other credit's row or
// some other trade's, and both would read back as "already ours".
func (s *P2PTradeClaimSource) forwardFor(ctx context.Context, tradeID uuid.UUID, ref string) (*models.P2PSettlementForward, error) {
	var fwd models.P2PSettlementForward
	err := s.db.WithContext(ctx).
		Where("trade_id = ? AND provider_ref = ?", tradeID, ref).
		First(&fwd).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("p2p accept: look up forward for trade %s: %w", tradeID, err)
	}
	return &fwd, nil
}

// claimAndCredit writes the forward row and credits the merchant's ledger in
// one transaction, returning fresh=false when this ref was already processed.
//
// The unique index on provider_ref is doing the real work: two pollers racing
// on the same credit both try to insert, one wins, the loser reads the winner's
// row and credits nothing.
func (s *P2PTradeClaimSource) claimAndCredit(
	ctx context.Context,
	trade *models.P2PTrade,
	pm *models.PaymentMerchant,
	ref string,
) (*models.P2PSettlementForward, bool, error) {
	forward := &models.P2PSettlementForward{
		ID:          uuid.New(),
		TradeID:     trade.ID,
		PMID:        pm.ID,
		ProviderRef: ref,
		Currency:    normaliseCurrency(trade.FiatCurrency),
		Amount:      trade.FiatAmount,
		Status:      models.P2PForwardPending,
		Reference:   p2pForwardRefPrefix + uuid.New().String(),
	}

	fresh := true
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(forward).Error; err != nil {
			if isUniqueViolation(err) {
				// Re-read on provider_ref ALONE. trade_id is unique too, so
				// matching on either key would let a DISTINCT second credit
				// bound to this trade read back the FIRST credit's row and
				// report success — after which the matcher stamps the second
				// credit `matched` with no ledger entry anywhere for its money.
				// That is a real transfer silently absorbed.
				//
				// Into a FRESH struct: the one we just tried to insert carries
				// a primary key, and GORM would fold that into the WHERE and
				// find nothing.
				var winner models.P2PSettlementForward
				readErr := tx.Where("provider_ref = ?", ref).First(&winner).Error
				if readErr == nil {
					fresh = false
					*forward = winner
					return nil
				}
				if !errors.Is(readErr, gorm.ErrRecordNotFound) {
					return readErr
				}
				// No row owns this ref, so the collision was on trade_id (or
				// the reference): other money has already settled this trade.
				// Erroring holds the credit for an operator, which is the only
				// safe outcome — reporting success would mark it settled and
				// nobody would ever be credited for it.
				return fmt.Errorf("trade %s is already settled by a different credit; credit %s must not be absorbed into it",
					trade.ID, ref)
			}
			return err
		}
		// Ledger FIRST, always. This is the merchant's claim on the money and
		// it must exist before any transfer is attempted — the invariant the
		// MCGP sell leg breaks by debiting on-chain before it pays out.
		return s.pm.CreditInTx(tx, pm.ID, forward.Currency, forward.Amount)
	})
	if err != nil {
		return nil, false, fmt.Errorf("p2p accept: claim and credit: %w", err)
	}
	return forward, fresh, nil
}

// settleTrade marks the trade paid and releases the escrowed crypto to the
// buyer, absorbing the transitions a replay would re-attempt.
func (s *P2PTradeClaimSource) settleTrade(ctx context.Context, tradeID uuid.UUID, ref string) error {
	// Record which credit settled this trade, for the same reason the Nomba
	// flow does: an operator looking at a released trade needs to be able to
	// find the money that paid for it.
	if err := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("id = ?", tradeID).
		Update("nomba_inbound_event_id", "inbound:"+ref).Error; err != nil {
		log.Printf("[inbound p2p] could not record attribution on trade=%s: %v", tradeID, err)
	}
	if _, err := s.trades.SystemMarkPaid(ctx, tradeID, "inbound:"+ref); err != nil {
		// Already advanced past pending_payment — a replay, or a concurrent
		// operator action. AutoRelease below is idempotent, so continue.
		if !errors.Is(err, ErrInvalidTransition) {
			// The trade is still pending_payment, so it is still an open claim
			// and the matcher can simply try again next tick. Reported as an
			// ordinary error on purpose: nothing has been lost and nothing else
			// needs to own it.
			return err
		}
	}
	if _, err := s.trades.AutoRelease(ctx, tradeID, s.signer); err != nil {
		// Deferral is earned by the trade's actual shape, not assumed from the
		// fact that a release failed. RecoverStuckTrades sweeps `paid` and only
		// `paid`, so that is the only status with a job behind it; deferring
		// any other one tells the matcher to stamp the credit `matched` for a
		// release nothing will ever come back to do.
		//
		// The status can still have moved under us — the guard in Accept cannot
		// cover the gap between itself and this line — so it is re-read here
		// rather than inferred from the branches above.
		status, statusErr := s.tradeStatus(ctx, tradeID)
		if statusErr != nil {
			// An unknown state is not a recoverable one. Reported as an
			// ordinary error so the credit stays visible: the alternative is
			// guessing `paid` and handing an unreleased trade to a sweep that
			// may never select it.
			return fmt.Errorf("trade %s: release failed (%v) and its status could not be re-read: %w",
				tradeID, err, statusErr)
		}
		if !p2pReleaseIsRecoverable(status) {
			return fmt.Errorf("trade %s is %s after a failed release, so no recovery job owns it: %v",
				tradeID, status, err)
		}
		// An orphan with an owner: the claim is gone from OpenClaims but a job
		// will come back for the release, so erroring would strand the credit
		// on a hold reason that is not true — see errP2PReleaseDeferred.
		return fmt.Errorf("%w: trade %s: %v", errP2PReleaseDeferred, tradeID, err)
	}
	return nil
}

// p2pReleaseIsRecoverable reports whether a trade whose release just failed has
// a background job that will come back for it.
//
// Exhaustive on purpose, because everything NOT in this list is a credit the
// matcher would mark `matched` with nobody left to finish the release:
//
//	paid       RecoverStuckTrades sweeps exactly this, the paid-orphan.
//	releasing  the chain reconciler reverts a stale `releasing` back to `paid`
//	           once the grace window says the release tx was lost, which hands
//	           it to RecoverStuckTrades. Two hops, but an owner at both.
//
// cancelled, disputed and resolved are the dangerous ones: AutoRelease refuses
// them, RecoverStuckTrades' `status = paid` filter never selects them, and the
// buyer's crypto stays escrowed for good.
//
// ── `releasing` can mean the escrow ALREADY paid out ──
//
// Not every `releasing` trade is a release that never landed. confirmReleaseCore
// checks its money-invariant AFTER verifying the on-chain event, so a receipt
// whose amounts do not add up aborts with the crypto already gone and the row
// still `releasing`. Deferring that is right — the reconciler settles it against
// on-chain truth — but it means a later Accept on the same trade (a replayed
// credit, a manual bind) reaches AutoRelease, which skips its flip for a trade
// already `releasing` and signs a SECOND adminRelease.
//
// Nothing on this side stops that. The guard is the escrow's own
// `require(status == Pending || Paid)` in adminRelease: a trade the contract has
// already marked Released reverts the second attempt, so the broadcast costs gas
// and returns an error instead of paying the buyer twice. Weakening that require
// — or routing a release through anything that does not have it — makes this
// path a double release.
func p2pReleaseIsRecoverable(status string) bool {
	return status == models.P2PTradeStatusPaid || status == models.P2PTradeStatusReleasing
}

// tradeStatus re-reads one trade's status, nothing else.
func (s *P2PTradeClaimSource) tradeStatus(ctx context.Context, tradeID uuid.UUID) (string, error) {
	var status string
	if err := s.db.WithContext(ctx).
		Model(&models.P2PTrade{}).
		Where("id = ?", tradeID).
		Pluck("status", &status).Error; err != nil {
		return "", err
	}
	if status == "" {
		return "", fmt.Errorf("trade %s has no status row", tradeID)
	}
	return status, nil
}

// ─── Instant forward ────────────────────────────────────────────────────────

// PayoutBalanceReader reports what the payout account can actually spend.
// *FlutterwaveClient satisfies it; the stablecoin sell leg reads the same call
// for the same reason.
type PayoutBalanceReader interface {
	FetchBalances(ctx context.Context) ([]FWBalance, error)
}

// P2PSettlementForwarder sweeps a settled trade's naira to the merchant's own
// bank, immediately, one transfer per trade (decision 7).
//
// ── Why this is not shaped like the MCGP sell leg ──
//
// The sell leg debits the user on-chain, fires the payout, treats a payout
// error as non-fatal, and reverses only when a `transfer.disburse` failed
// webhook arrives. A transfer rejected at submission never becomes a transfer,
// so no webhook is ever sent, so the reversal never runs and the user's value
// is simply gone.
//
// The inversion here is the fix: the ledger credit happens first and is never
// contingent on the transfer. A debit is a consequence of a CONFIRMED
// disbursement and of nothing else. Every other outcome — rejected, failed,
// silently unacknowledged — leaves the merchant credited and raises an alert,
// so the worst case is that a merchant is paid late, never that they are paid
// neither in bank nor in ledger.
type P2PSettlementForwarder struct {
	db       *gorm.DB
	pm       *PMService
	gateway  NombaGateway
	balances PayoutBalanceReader
	// senderName is the display name stamped on the outgoing transfer.
	senderName string
	now        func() time.Time
}

// NewP2PSettlementForwarder wires the forward. balances may be nil, which skips
// the pre-check — see checkBalance for what that costs.
func NewP2PSettlementForwarder(db *gorm.DB, pm *PMService, gateway NombaGateway, balances PayoutBalanceReader, senderName string) *P2PSettlementForwarder {
	return &P2PSettlementForwarder{
		db:         db,
		pm:         pm,
		gateway:    gateway,
		balances:   balances,
		senderName: strings.TrimSpace(senderName),
		now:        time.Now,
	}
}

// withClock is a test seam for the staleness sweep.
func (f *P2PSettlementForwarder) withClock(now func() time.Time) *P2PSettlementForwarder {
	f.now = now
	return f
}

// Forward submits the transfer for one settlement forward.
//
// It never debits. The only debit in this file is in Confirm, on a confirmed
// success. Re-callable: a forward that is not `pending` has either already been
// submitted or already been resolved, and returns without touching anything.
func (f *P2PSettlementForwarder) Forward(ctx context.Context, forwardID uuid.UUID) error {
	var fwd models.P2PSettlementForward
	if err := f.db.WithContext(ctx).First(&fwd, "id = ?", forwardID).Error; err != nil {
		return err
	}
	if fwd.Status != models.P2PForwardPending {
		return nil
	}
	if f.gateway == nil {
		return f.hold(ctx, &fwd, "no payout gateway configured")
	}

	pm, err := f.pm.GetByID(ctx, fwd.PMID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(pm.SettlementBankCode) == "" || strings.TrimSpace(pm.SettlementAccountNumber) == "" {
		// Terminal until the merchant record is fixed. Their proceeds are on
		// the ledger and the existing withdrawal path can still pay them.
		if aErr := f.fail(ctx, &fwd, ErrForwardNoSettlementAccount.Error()); aErr != nil {
			return aErr
		}
		return ErrForwardNoSettlementAccount
	}

	if err := f.checkBalance(ctx, &fwd); err != nil {
		// Retryable: nothing was sent, so the row stays `pending` and a later
		// attempt can pick it up once the balance is visible.
		if hErr := f.hold(ctx, &fwd, err.Error()); hErr != nil {
			return hErr
		}
		return err
	}

	payoutID, err := f.gateway.InitiatePayout(ctx, PayoutRequest{
		BankCode:      pm.SettlementBankCode,
		AccountNumber: pm.SettlementAccountNumber,
		AccountName:   pm.SettlementAccountName,
		Amount:        fwd.Amount,
		AmountNGN:     fwd.Amount,
		// SourceCurrency and the (absent) DestinationCurrency are the same
		// balance: a merchant settles in the currency they were paid in, and
		// there is no FX leg here to get the denomination wrong.
		SourceCurrency: fwd.Currency,
		Reference:      fwd.Reference,
		SenderName:     f.senderName,
	})
	if err != nil {
		// REJECTED AT SUBMISSION. No transfer exists at the provider, so no
		// webhook will ever arrive to correct this — the exact hole that
		// strands value on the MCGP sell leg. The balance stays credited and
		// this becomes an operator's problem immediately, not silently.
		if fErr := f.fail(ctx, &fwd, "rejected at submission: "+err.Error()); fErr != nil {
			return fErr
		}
		return err
	}
	if strings.TrimSpace(payoutID) == "" {
		// Accepted, but with nothing to correlate a later webhook against. We
		// cannot tell a success from a failure for this transfer ever again, so
		// it must not be treated as in-flight and must never be debited on a
		// guess. Alert now.
		if fErr := f.fail(ctx, &fwd, "provider accepted the transfer but returned no payout id; disbursement cannot be confirmed"); fErr != nil {
			return fErr
		}
		return errors.New("p2p forward: provider returned no payout id")
	}

	now := f.now()
	return f.db.WithContext(ctx).Model(&models.P2PSettlementForward{}).
		Where("id = ? AND status = ?", fwd.ID, models.P2PForwardPending).
		Updates(map[string]any{
			"status":         models.P2PForwardSubmitted,
			"payout_id":      payoutID,
			"submitted_at":   now,
			"failure_reason": "",
			"alerted_at":     nil,
		}).Error
}

// checkBalance refuses to submit a transfer the payout account cannot fund.
//
// The stablecoin sell leg does this (it_stable_sell.go); the MCGP sell leg does
// not, and that omission is why an insufficient-funds rejection there strands a
// user's premium. The check is worth having here for a different reason: it is
// FREE. The merchant is already credited, so declining to submit costs a delay
// and nothing else, whereas submitting blind burns a provider rejection and
// puts a row into the failed queue that a human then has to read.
//
// A balance we cannot READ is also a refusal. Submitting on an unknown balance
// is the same bet as submitting on a known-bad one, and the downside is
// identical; the upside of guessing is only ever "we were right anyway".
func (f *P2PSettlementForwarder) checkBalance(ctx context.Context, fwd *models.P2PSettlementForward) error {
	if f.balances == nil {
		// No reader wired. Deliberately not fatal: the forward's safety comes
		// from the ledger ordering, not from this check, and refusing every
		// forward because the optional collaborator is absent would be worse
		// than the rejection it prevents.
		return nil
	}
	need, ok := ngnToKobo(fwd.Amount)
	if !ok {
		return fmt.Errorf("p2p forward: unusable amount %q", fwd.Amount)
	}
	balances, err := f.balances.FetchBalances(ctx)
	if err != nil {
		return fmt.Errorf("p2p forward: cannot read payout balance: %w", err)
	}
	for _, b := range balances {
		if !strings.EqualFold(b.Currency, fwd.Currency) {
			continue
		}
		// Compare in kobo. The balance arrives as a float and the amount as a
		// 2dp string; rounding the float to minor units is the only comparison
		// that does not drift.
		have := big.NewFloat(b.AvailableBalance)
		have.Mul(have, big.NewFloat(100))
		haveKobo, _ := have.Int64()
		if haveKobo < need {
			return fmt.Errorf("p2p forward: payout balance %s short: have %d kobo, need %d kobo",
				fwd.Currency, haveKobo, need)
		}
		return nil
	}
	return fmt.Errorf("p2p forward: payout account shows no %s balance line", fwd.Currency)
}

// Confirm resolves a submitted forward from the provider's disburse webhook.
//
// success=true is the ONLY path that debits available_ngn, and the debit and
// the status write commit together. Anything else leaves the merchant credited.
func (f *P2PSettlementForwarder) Confirm(ctx context.Context, reference string, success bool, reason string) error {
	ref := strings.TrimSpace(reference)
	if !strings.HasPrefix(ref, p2pForwardRefPrefix) {
		return nil // not ours
	}
	var fwd models.P2PSettlementForward
	err := f.db.WithContext(ctx).First(&fwd, "reference = ?", ref).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("[p2p forward] disburse result for unknown reference %q; ignoring", ref)
		return nil
	} else if err != nil {
		return err
	}
	if fwd.Status == models.P2PForwardSettled || fwd.Status == models.P2PForwardFailed {
		return nil // terminal; webhook redelivery
	}

	if !success {
		// The transfer did not land, so the money never left. The ledger entry
		// is correct as it stands and must NOT be debited.
		return f.fail(ctx, &fwd, "provider reported the transfer failed: "+reason)
	}

	now := f.now()
	return f.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.P2PSettlementForward{}).
			Where("id = ? AND status = ?", fwd.ID, models.P2PForwardSubmitted).
			Updates(map[string]any{
				"status":     models.P2PForwardSettled,
				"settled_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // someone else resolved it between our read and this write
		}
		ok, err := f.pm.WithdrawInTx(tx, fwd.PMID, fwd.Currency, fwd.Amount)
		if err != nil {
			return err
		}
		if !ok {
			// The money HAS left the account but the free balance could not
			// cover the debit — most plausibly because the proceeds were
			// reserved against another in-flight trade in the meantime.
			//
			// Returning an error rolls the whole transaction back, INCLUDING
			// the `settled` write above, so the row stays `submitted`: visible
			// to the sweep, and retried on the next webhook redelivery once the
			// reservation clears. The merchant currently holds both the cash
			// and the ledger entry — the safe side of the invariant, but still
			// wrong and still ours to correct.
			return fmt.Errorf("p2p forward: disbursement confirmed for %s but the %s ledger could not be debited (free balance short of %s); merchant is over-credited",
				fwd.ID, fwd.Currency, fwd.Amount)
		}
		return nil
	})
}

// SweepUnconfirmed alerts on forwards that were submitted and never answered.
//
// This is the third failure mode and the one with no event of its own. A
// transfer the provider accepted and then said nothing more about produces no
// webhook, success or failure, so nothing in the system would ever revisit it.
// The balance is still credited — the invariant holds — but "the merchant has
// not been paid and nobody knows" is not an acceptable resting state.
//
// It never changes the money. Its entire job is to make silence visible.
func (f *P2PSettlementForwarder) SweepUnconfirmed(ctx context.Context) (int, error) {
	cutoff := f.now().Add(-p2pForwardStaleAfter)
	var stale []models.P2PSettlementForward
	if err := f.db.WithContext(ctx).
		Where("status = ? AND submitted_at < ? AND alerted_at IS NULL",
			models.P2PForwardSubmitted, cutoff).
		Order("submitted_at ASC").
		Limit(p2pForwardSweepBatch).
		Find(&stale).Error; err != nil {
		return 0, err
	}
	for i := range stale {
		fwd := &stale[i]
		log.Printf("[p2p forward] ALARM forward=%s trade=%s pm=%s payout=%s submitted %s ago with no disburse result; "+
			"merchant remains credited %s %s — confirm with the provider before any manual payment",
			fwd.ID, fwd.TradeID, fwd.PMID, fwd.PayoutID, p2pForwardStaleAfter, fwd.Currency, fwd.Amount)
		if err := f.markAlerted(ctx, fwd.ID); err != nil {
			return i, err
		}
	}
	return len(stale), nil
}

// ListNeedingAttention returns forwards where the merchant is credited but no
// money reached their bank — the operator queue for this feature.
//
// Aged `pending` rows belong here as much as `failed` ones do. The merchant is
// credited and unpaid in both cases; the only difference is that a pending row
// may yet fix itself, which is why it is given p2pForwardStaleAfter first. A
// row still pending after that is a merchant nobody is paying and, before this,
// one nobody could even find.
func (f *P2PSettlementForwarder) ListNeedingAttention(ctx context.Context) ([]models.P2PSettlementForward, error) {
	var out []models.P2PSettlementForward
	err := f.db.WithContext(ctx).
		Where("status = ? OR (status = ? AND created_at < ?)",
			models.P2PForwardFailed,
			models.P2PForwardPending, f.now().Add(-p2pForwardStaleAfter)).
		Order("created_at ASC").
		Limit(p2pForwardSweepBatch).
		Find(&out).Error
	return out, err
}

// fail marks a forward terminal and alerts. The balance is deliberately left
// alone: the merchant keeps the ledger entry precisely because the money did
// not reach them.
//
// Scoped to the status the caller actually observed, because "not paid" is only
// ever true of that status. A caller whose read has since gone stale is
// describing somebody else's row: most dangerously an attempt that read
// `pending` while another one SUBMITTED, where an unscoped write would list a
// merchant whose money is in flight as one nobody paid. That is the operator
// queue's cue to pay by hand, and the merchant is then paid twice — the one
// outcome the ledger-first ordering cannot undo, because the second payment is
// real money out of a real account.
//
// The alarm follows the write rather than preceding it for the same reason: a
// row this call did not resolve is not a merchant this call can say went
// unpaid.
func (f *P2PSettlementForwarder) fail(ctx context.Context, fwd *models.P2PSettlementForward, reason string) error {
	now := f.now()
	res := f.db.WithContext(ctx).Model(&models.P2PSettlementForward{}).
		Where("id = ? AND status = ?", fwd.ID, fwd.Status).
		Updates(map[string]any{
			"status":         models.P2PForwardFailed,
			"failure_reason": truncateReason(reason),
			"alerted_at":     now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		log.Printf("[p2p forward] forward=%s trade=%s pm=%s moved out of %q before it could be failed (%s); leaving it to whoever owns it now",
			fwd.ID, fwd.TradeID, fwd.PMID, fwd.Status, reason)
		return nil
	}
	log.Printf("[p2p forward] ALARM forward=%s trade=%s pm=%s NOT paid: %s — merchant remains credited %s %s, ledger untouched",
		fwd.ID, fwd.TradeID, fwd.PMID, reason, fwd.Currency, fwd.Amount)
	return nil
}

// hold leaves a forward retryable and records why it has not been sent. Unlike
// fail this is not terminal — nothing was submitted, so a later attempt is safe.
//
// Safe to retry is not the same as worth retrying forever, though. Once the
// attempts run out the cause is not a blip, and a row that only ever retries is
// invisible to every operator surface, so it is escalated to the failed queue
// where a human can see it. Nothing about the money changes either way: the
// merchant keeps the ledger entry because nothing reached their bank.
//
// The count is the DATABASE's, never the one read before the provider call.
// Two attempts can overlap — RedrivePending reads its whole batch up front, and
// a re-drive still waiting on the provider has written nothing yet — so both
// would compute the same next value and the second would overwrite the first
// with an identical number. The ceiling would then never arrive, and a
// permanently unpayable forward would retry forever on no operator surface,
// which is the one outcome the ceiling exists to rule out.
//
// The same database is what decides whether this attempt may act at all. Those
// overlapping attempts do not all fail: one of them can SUBMIT, and from that
// moment the merchant's money is in flight and nothing here is entitled to call
// the transfer unsendable. Escalating on a count this attempt did not earn would
// mark an in-flight transfer `failed`, which reads to an operator as a merchant
// nobody paid — and paying them by hand pays them twice.
func (f *P2PSettlementForwarder) hold(ctx context.Context, fwd *models.P2PSettlementForward, reason string) error {
	attempts, counted, err := f.countAttempt(ctx, fwd, reason)
	if err != nil {
		return err
	}
	if !counted {
		log.Printf("[p2p forward] forward=%s trade=%s pm=%s left alone: it is no longer pending, so this attempt (%s) is not the one that owns it",
			fwd.ID, fwd.TradeID, fwd.PMID, reason)
		return nil
	}
	if attempts >= p2pForwardMaxAttempts {
		return f.fail(ctx, fwd, fmt.Sprintf("held %d times and still unsendable: %s", attempts, reason))
	}
	log.Printf("[p2p forward] forward=%s trade=%s pm=%s deferred (attempt %d of %d): %s — merchant remains credited %s %s",
		fwd.ID, fwd.TradeID, fwd.PMID, attempts, p2pForwardMaxAttempts, reason, fwd.Currency, fwd.Amount)
	return nil
}

// countAttempt increments the row's attempt counter in the database and reports
// what it now stands at, so the ceiling is judged on the stored number rather
// than on a stale read. It reads back inside the same transaction because the
// two supported drivers do not agree on UPDATE ... RETURNING.
//
// A row that is no longer `pending` moved on between the caller's read and this
// write, so nothing is counted and counted=false says so. The number returned
// alongside it is zero rather than the row's stored one on purpose: the stored
// count belongs to whoever owns the row now, and a caller that judged the
// ceiling on it could escalate a forward that has since been SUBMITTED — a
// merchant whose money is in flight, listed to an operator as unpaid, and paid
// a second time by hand.
func (f *P2PSettlementForwarder) countAttempt(ctx context.Context, fwd *models.P2PSettlementForward, reason string) (int, bool, error) {
	var attempts int
	var counted bool
	err := f.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.P2PSettlementForward{}).
			Where("id = ? AND status = ?", fwd.ID, models.P2PForwardPending).
			Updates(map[string]any{
				"failure_reason": truncateReason(reason),
				"attempts":       gorm.Expr("attempts + 1"),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		counted = true
		var fresh models.P2PSettlementForward
		if err := tx.Select("attempts").First(&fresh, "id = ?", fwd.ID).Error; err != nil {
			return err
		}
		attempts = fresh.Attempts
		return nil
	})
	return attempts, counted, err
}

// RedrivePending re-attempts the forwards that were held back, and is the thing
// whose absence made `hold`'s "a later attempt can pick it up" untrue.
//
// Forward was only ever called from Accept. SweepUnconfirmed looks at
// `submitted` and ListNeedingAttention looked at `failed`, so a merchant held
// by an unreadable balance, a short balance or an absent gateway was credited,
// never paid, and on no operator surface at all — precisely the "nobody knows"
// state this file sets out to eliminate.
//
// It returns how many forwards it re-drove, not how many succeeded: a re-drive
// that holds again is still progress against the attempt ceiling. Errors are
// logged per row rather than returned, because one merchant's broken payout
// must not stop the next merchant's from going out.
func (f *P2PSettlementForwarder) RedrivePending(ctx context.Context) (int, error) {
	cutoff := f.now().Add(-p2pForwardRedriveAfter)
	var due []models.P2PSettlementForward
	if err := f.db.WithContext(ctx).
		Where("status = ? AND created_at < ?", models.P2PForwardPending, cutoff).
		Order("created_at ASC").
		Limit(p2pForwardSweepBatch).
		Find(&due).Error; err != nil {
		return 0, err
	}
	for i := range due {
		if err := f.Forward(ctx, due[i].ID); err != nil {
			log.Printf("[p2p forward] re-drive of forward=%s trade=%s still unsent: %v",
				due[i].ID, due[i].TradeID, err)
		}
	}
	return len(due), nil
}

func (f *P2PSettlementForwarder) markAlerted(ctx context.Context, id uuid.UUID) error {
	now := f.now()
	return f.db.WithContext(ctx).Model(&models.P2PSettlementForward{}).
		Where("id = ?", id).
		Update("alerted_at", now).Error
}

// Compile-time proof that this satisfies the matcher's seam. The matcher takes
// ClaimSource values and nothing else references these methods, so without this
// a signature drift would only surface at wiring time.
var _ ClaimSource = (*P2PTradeClaimSource)(nil)
