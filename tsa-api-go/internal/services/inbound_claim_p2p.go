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

// ErrForwardNoSettlementAccount means the merchant has nowhere to be paid.
// Their proceeds stay on the ledger; this is a merchant-record problem, not a
// money problem.
var ErrForwardNoSettlementAccount = errors.New("p2p forward: merchant has no settlement bank configured")

// P2PTradeClaimSource implements ClaimSource for P2P trades awaiting a buyer's
// naira. See the file header for the buyer/merchant distinction.
type P2PTradeClaimSource struct {
	db     *gorm.DB
	pm     *PMService
	trades *P2PTradeService
	signer ReleaseSigner

	// forwarder sweeps the merchant's proceeds onward. Optional: without it the
	// ledger credit still happens and the money simply waits for the existing
	// manual withdrawal path, which is the pre-decision-7 behaviour.
	forwarder *P2PSettlementForwarder
}

// NewP2PTradeClaimSource wires the P2P product into the inbound matcher.
func NewP2PTradeClaimSource(db *gorm.DB, pm *PMService, trades *P2PTradeService, signer ReleaseSigner) *P2PTradeClaimSource {
	return &P2PTradeClaimSource{db: db, pm: pm, trades: trades, signer: signer}
}

// SetForwarder enables the instant per-trade forward to the merchant's bank.
func (s *P2PTradeClaimSource) SetForwarder(f *P2PSettlementForwarder) { s.forwarder = f }

// p2pClaimRow is the projection OpenClaims scans. Deliberately not the full
// trade: the matcher needs four fields and loading whole trades to build them
// would read every open trade's proof URLs and tx hashes on every poll tick.
type p2pClaimRow struct {
	ID         uuid.UUID
	BuyerID    uuid.UUID
	FiatAmount string
	CreatedAt  time.Time
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

	var rows []p2pClaimRow
	if err := s.db.WithContext(ctx).
		Table("p2p_trades").
		Select("p2p_trades.id, p2p_trades.buyer_id, p2p_trades.fiat_amount, p2p_trades.created_at").
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
		kobo = roundKoboToNaira(kobo)
		claims = append(claims, InboundClaim{
			Type: InboundClaimTypeP2PTrade,
			ID:   r.ID,
			// The BUYER. They send the money, so the transfer carries their
			// name — see the file header.
			UserID:     r.BuyerID,
			AmountKobo: kobo,
			OpenedAt:   r.CreatedAt,
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
	pm, err := s.merchantForTrade(ctx, trade.AdID)
	if err != nil {
		return err
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
	if err := s.settleTrade(ctx, trade.ID, ref); err != nil {
		return fmt.Errorf("p2p accept: settle trade %s: %w", trade.ID, err)
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
				fresh = false
				// Re-read the row that won, whether it was claimed by this ref
				// or by this trade. Into a FRESH struct: the one we just tried
				// to insert carries a primary key, and GORM would fold that
				// into the WHERE and find nothing.
				var winner models.P2PSettlementForward
				if err := tx.Where("provider_ref = ? OR trade_id = ?", ref, trade.ID).
					First(&winner).Error; err != nil {
					return err
				}
				*forward = winner
				return nil
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
			return err
		}
	}
	_, err := s.trades.AutoRelease(ctx, tradeID, s.signer)
	return err
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
func (f *P2PSettlementForwarder) ListNeedingAttention(ctx context.Context) ([]models.P2PSettlementForward, error) {
	var out []models.P2PSettlementForward
	err := f.db.WithContext(ctx).
		Where("status = ?", models.P2PForwardFailed).
		Order("created_at ASC").
		Find(&out).Error
	return out, err
}

// fail marks a forward terminal and alerts. The balance is deliberately left
// alone: the merchant keeps the ledger entry precisely because the money did
// not reach them.
func (f *P2PSettlementForwarder) fail(ctx context.Context, fwd *models.P2PSettlementForward, reason string) error {
	log.Printf("[p2p forward] ALARM forward=%s trade=%s pm=%s NOT paid: %s — merchant remains credited %s %s, ledger untouched",
		fwd.ID, fwd.TradeID, fwd.PMID, reason, fwd.Currency, fwd.Amount)
	now := f.now()
	return f.db.WithContext(ctx).Model(&models.P2PSettlementForward{}).
		Where("id = ?", fwd.ID).
		Updates(map[string]any{
			"status":         models.P2PForwardFailed,
			"failure_reason": truncateReason(reason),
			"alerted_at":     now,
		}).Error
}

// hold leaves a forward retryable and records why it has not been sent. Unlike
// fail this is not terminal — nothing was submitted, so a later attempt is safe.
func (f *P2PSettlementForwarder) hold(ctx context.Context, fwd *models.P2PSettlementForward, reason string) error {
	log.Printf("[p2p forward] forward=%s trade=%s pm=%s deferred: %s — merchant remains credited %s %s",
		fwd.ID, fwd.TradeID, fwd.PMID, reason, fwd.Currency, fwd.Amount)
	return f.db.WithContext(ctx).Model(&models.P2PSettlementForward{}).
		Where("id = ? AND status = ?", fwd.ID, models.P2PForwardPending).
		Update("failure_reason", truncateReason(reason)).Error
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
