package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ─── Pure matcher ────────────────────────────────────────────────────────────

// buyCandidate is one open PM buy trade considered for an inbound naira credit.
type buyCandidate struct {
	TradeID    uuid.UUID
	BuyerName  string // KYC name (users.name)
	FiatAmount string // the trade's expected NGN amount (numeric string, 2dp)
}

// matchBuyInbound returns the single trade whose buyer KYC name matches the
// sender AND whose fiat amount equals amountNGN. ok=false when zero or more than
// one candidate matches (ambiguous → manual queue, never a wrong release).
// Amount comparison is decimal-normalized to integer kobo (see ngnEqual).
func matchBuyInbound(cands []buyCandidate, senderName, amountNGN string) (uuid.UUID, bool) {
	var hits []uuid.UUID
	for _, c := range cands {
		if !ngnEqual(c.FiatAmount, amountNGN) {
			continue
		}
		if MatchesPayerName(senderName, c.BuyerName) {
			hits = append(hits, c.TradeID)
		}
	}
	if len(hits) == 1 {
		return hits[0], true
	}
	return uuid.Nil, false
}

// ngnEqual compares two NGN decimal strings by integer kobo (2dp), tolerating
// "1500", "1500.0", "1500.00". Any unparseable input returns false. Never uses
// float equality.
func ngnEqual(a, b string) bool {
	ka, ok := ngnToKobo(a)
	if !ok {
		return false
	}
	kb, ok := ngnToKobo(b)
	if !ok {
		return false
	}
	return ka == kb
}

// ngnToKobo parses a decimal NGN string into integer kobo (naira×100). The
// fractional part is padded/truncated to exactly 2 digits (inputs with more
// than 2 decimal places are TRUNCATED, not rejected — acceptable because NGN
// is a 2dp currency and both operands truncate identically). Returns ok=false
// on any parse error (empty, non-numeric, negative). A second dot causes
// ok=false because ParseInt rejects the dot character in the fractional part.
func ngnToKobo(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	whole := s
	frac := ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		whole = s[:i]
		frac = s[i+1:]
	}
	if whole == "" {
		return 0, false
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w < 0 {
		return 0, false
	}
	// Normalize fractional to exactly 2 digits.
	if len(frac) < 2 {
		frac = frac + strings.Repeat("0", 2-len(frac))
	} else {
		frac = frac[:2]
	}
	var f int64
	if frac != "" {
		var err error
		f, err = strconv.ParseInt(frac, 10, 64)
		if err != nil || f < 0 {
			return 0, false
		}
	}
	return w*100 + f, true
}

// ─── Service ─────────────────────────────────────────────────────────────────

// PMNameLookup resolves a user's KYC name (users.name) by id. Injected so the
// automation stays decoupled from the user store (and tests can stub names).
type PMNameLookup func(ctx context.Context, userID uuid.UUID) (string, error)

// PMAutomationService drives the PM buy-flow: a settled Nomba inbound credit is
// matched conservatively to exactly one open buy trade, then auto-settled
// (system markPaid → admin release). A non-match or ambiguity is a deliberate
// no-op, leaving the trade pending_payment for the manual-release queue.
type PMAutomationService struct {
	db         *gorm.DB
	pm         *PMService
	trades     *P2PTradeService
	signer     ChainSender
	name       PMNameLookup
	gateway    NombaGateway
	senderName string
	// itHooks routes IT-prefixed Flutterwave webhook references to the IT
	// service (see flutterwave_dispatch.go); nil when IT is dormant.
	itHooks ITWebhookHooks
	// fwCharges independently re-fetches a Flutterwave charge to verify a
	// charge.completed webhook before crediting (see flutterwave_dispatch.go).
	// nil → charge settlement is refused (fail-closed).
	fwCharges FlutterwaveChargeGetter
	// forwards resolves disburse results for P2P settlement forwards, which
	// share this webhook but are keyed by their own reference prefix. nil when
	// the instant forward is not wired — see HandlePayoutStatus.
	forwards *P2PSettlementForwarder

	// centralInbound is set when PM buyers pay the company account (see
	// SetCentralInbound).
	centralInbound bool
}

// SetSettlementForwarder routes settlement-forward disburse results to the
// forwarder. Without it a confirmed forward is never debited from the
// merchant's ledger, which over-credits them — the safe direction, but wrong.
func (s *PMAutomationService) SetSettlementForwarder(f *P2PSettlementForwarder) { s.forwards = f }

// NewPMAutomationService wires the PM buy-flow + sell-flow automation. gateway
// is the per-PM Nomba boundary used by the sell flow to pay the user's bank.
// senderName is the display name stamped on outgoing (sell-flow) transfers.
func NewPMAutomationService(db *gorm.DB, pm *PMService, trades *P2PTradeService, signer ChainSender, name PMNameLookup, gateway NombaGateway, senderName string) *PMAutomationService {
	return &PMAutomationService{db: db, pm: pm, trades: trades, signer: signer, name: name, gateway: gateway, senderName: senderName}
}

// inboundCandidateRow is the lightweight projection scanned from the
// p2p_trades⋈p2p_ads join.
type inboundCandidateRow struct {
	ID         uuid.UUID
	BuyerID    uuid.UUID
	FiatAmount string
}

// resolveInboundPM maps a settled inbound credit to its PaymentMerchant.
//
// Resolution policy (accountRef-only):
//   - p.AccountRef non-empty → resolve via GetByAccountRef. On miss the
//     inbound belongs to an unknown corporate PM — return ErrPMNotFound (no-op)
//     and log.
//   - p.AccountRef empty → log and return ErrPMNotFound (no-op). An empty
//     accountRef is never a valid corporate-PM payment; there is no walletId
//     fallback.
//
// Returns ErrPMNotFound when the applicable key resolves no row.
func (s *PMAutomationService) resolveInboundPM(ctx context.Context, p InboundPayment) (*models.PaymentMerchant, error) {
	if strings.TrimSpace(p.AccountRef) != "" {
		pm, err := s.pm.GetByAccountRef(ctx, p.AccountRef)
		if err == nil {
			return pm, nil
		}
		if !errors.Is(err, ErrPMNotFound) {
			return nil, err
		}
		// accountRef present but unresolved: unknown corporate PM — no-op.
		log.Printf("[pm automation] inbound with unknown accountRef=%q (walletId=%q); not a known corporate PM — ignoring", p.AccountRef, p.WalletID)
		return nil, ErrPMNotFound
	}
	// Empty accountRef — no corporate PM match; ignoring.
	log.Printf("[pm automation] inbound with empty accountRef (walletId=%q) — no corporate PM match; ignoring", p.WalletID)
	return nil, ErrPMNotFound
}

// HandleInbound is the buy-flow entry point. Idempotency at the event level is
// the webhook ledger's job (caller); HandleInbound itself is safe to re-run —
// a re-run on an already-paid/released trade is absorbed (ErrInvalidTransition
// is ignored on markPaid; AutoRelease is idempotent on already-released).
func (s *PMAutomationService) HandleInbound(ctx context.Context, p InboundPayment) error {
	// 1. Only act on a final (settled) credit — never auto-release on a
	//    non-settled inbound (design §9).
	if !strings.EqualFold(p.Status, "settled") {
		return nil
	}

	// 2. Resolve the PM. Under the accountRef-only model a credit is attributed
	//    by the VA accountRef (GetByAccountRef). An empty accountRef is a no-op
	//    (no walletId fallback).
	pm, err := s.resolveInboundPM(ctx, p)
	if err != nil {
		if errors.Is(err, ErrPMNotFound) {
			log.Printf("[pm automation] inbound to unknown PM (accountRef=%q walletId=%q event=%s); ignoring", p.AccountRef, p.WalletID, p.EventID)
			return nil
		}
		return err
	}

	// 3. Paused PM: drain in-flight, no new auto-release.
	if !pm.IsActive() {
		return nil
	}

	// 3a. Credit the per-PM NGN ledger for EVERY settled inbound — whether or not
	//     it matches a buy trade below (an unmatched credit is a PM top-up). This
	//     runs inside the deduped webhook dispatch (WebhookEvent dedup guarantees
	//     once-per-event), so no extra idempotency guard is needed here: a given
	//     EventID is dispatched at most once, so the ledger credit fires exactly
	//     once per settled inbound.
	// Credit the currency the money actually arrived in. InboundPayment.Currency
	// is empty for legacy Nomba payloads, which were always naira.
	inboundCurrency := p.Currency
	if strings.TrimSpace(inboundCurrency) == "" {
		inboundCurrency = "NGN"
	}
	if err := s.pm.CreditIn(ctx, pm.ID, inboundCurrency, p.AmountNGN); err != nil {
		// A bad/unparseable amount fails the credit but must not abort the
		// webhook — failing causes 5xx + retries that never recover. Log and
		// continue (consistent with unknown-walletId → no-op above). A genuine
		// DB error is also skipped: losing a credit is less harmful than an
		// infinite webhook retry loop.
		log.Printf("[pm automation] CreditNGN skipped (pm=%s event=%s amount=%q): %v", pm.ID, p.EventID, p.AmountNGN, err)
	}

	// 4. Load candidate trades: this PM's open, pooled, on-chain-opened buy
	//    trades (i.e. trades on the PM's SELL ads).
	var rows []inboundCandidateRow
	if err := s.db.WithContext(ctx).
		Table("p2p_trades").
		Select("p2p_trades.id, p2p_trades.buyer_id, p2p_trades.fiat_amount").
		Joins("JOIN p2p_ads ON p2p_ads.id = p2p_trades.ad_id").
		Where("p2p_ads.pm_id = ?", pm.ID).
		Where("p2p_ads.side = ?", models.P2PAdSideSell).
		Where("p2p_trades.status = ?", models.P2PTradeStatusPendingPayment).
		Where("p2p_trades.funding = ?", models.P2PTradeFundingPooled).
		Where("p2p_trades.open_tx_hash <> ?", "").
		Scan(&rows).Error; err != nil {
		return err
	}

	cands := make([]buyCandidate, 0, len(rows))
	for _, r := range rows {
		name, err := s.name(ctx, r.BuyerID)
		if err != nil {
			// A name-lookup failure can't be allowed to make this candidate
			// silently match; skip it (it falls to the manual queue).
			log.Printf("[pm automation] name lookup failed for buyer=%s trade=%s: %v", r.BuyerID, r.ID, err)
			continue
		}
		cands = append(cands, buyCandidate{TradeID: r.ID, BuyerName: name, FiatAmount: r.FiatAmount})
	}

	// 5. Conservative match: exactly one name+amount hit, else no-op.
	tradeID, ok := matchBuyInbound(cands, p.SenderAccountName, p.AmountNGN)
	if !ok {
		return nil
	}

	// 6. The merchant's limits (PMService.CheckAutoSettle, shared with central
	//    inbound). A refusal routes to the manual queue, and so does a daily
	//    cap that cannot be evaluated: never auto-release on a check that did
	//    not run. Only the code is logged — the reason names the merchant.
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		return err
	}
	refusal, err := s.pm.CheckAutoSettle(ctx, pm, &trade)
	if err != nil {
		log.Printf("[pm automation] trade=%s: %v; routing to manual queue (fail-safe, no release)", tradeID, err)
		return nil
	}
	if refusal != nil {
		log.Printf("[pm automation] trade=%s refused auto-settle (%s); routing to manual queue", tradeID, refusal.Code)
		return nil
	}

	// 7. Record attribution so the manual queue can tell matched from
	//    unmatched. Best-effort: a failure here must not block the release.
	if err := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("id = ?", tradeID).
		Update("nomba_inbound_event_id", p.EventID).Error; err != nil {
		log.Printf("[pm automation] record event attribution failed for trade=%s: %v", tradeID, err)
	}

	// 8. System markPaid. ErrInvalidTransition means a re-run on an
	//    already-advanced trade — ignore and continue to release.
	if _, err := s.trades.SystemMarkPaid(ctx, tradeID, "nomba:"+p.EventID); err != nil {
		if !errors.Is(err, ErrInvalidTransition) {
			return err
		}
	}

	// 9. Admin-EOA release (idempotent on already-released).
	_, err = s.trades.AutoRelease(ctx, tradeID, s.signer)
	return err
}

// tradeUSDValue converts a trade's locked TokenAmount (base units) to a USD
// float, treating the settlement token (USDT/USDC) as ~$1 — mirroring
// baseUnitsToUSD / the handlers' weiToUSDFloat convention. Returns ok=false
// when TokenAmount cannot be parsed as a base-10 integer (e.g. empty, decimal,
// or non-numeric). Callers must not auto-release when ok=false.
func tradeUSDValue(trade *models.P2PTrade) (float64, bool) {
	amt, ok := new(big.Int).SetString(trade.TokenAmount, 10)
	if !ok {
		return 0, false
	}
	return baseUnitsToUSD(amt, tokenDecimalsFor(trade.ChainID, trade.Token)), true
}

// ─── Webhook dispatch + recovery ─────────────────────────────────────────────

// Dispatch classifies a raw Nomba webhook body by event_type and routes it to
// the right entry point: a "payout"-prefixed event → ParseNombaPayout +
// HandlePayoutStatus; anything else → ParseNombaInbound + HandleInbound. The
// result (including ErrNonSettlingEvent propagated from ParseNombaInbound for a
// recognized non-settling payment_* event) is returned to the caller so it can
// distinguish "handled", "intentionally no action", and "real failure".
//
// Centralizing classify→parse→dispatch here lets BOTH the webhook handler (live
// path) and the recovery job (RetryFailedWebhooks) drive the exact same logic.
func (s *PMAutomationService) Dispatch(ctx context.Context, body []byte) error {
	var env struct {
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("dispatch: classify webhook: %w", err)
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(env.EventType)), "payout") {
		p, err := ParseNombaPayout(body)
		if err != nil {
			return err
		}
		return s.HandlePayoutStatus(ctx, *p)
	}
	p, err := ParseNombaInbound(body)
	if err != nil {
		// Propagate ErrNonSettlingEvent (and any other parse error) unchanged.
		return err
	}
	return s.HandleInbound(ctx, *p)
}

// recoverStuckRow is the lightweight projection scanned for RecoverStuckTrades.
type recoverStuckRow struct {
	ID uuid.UUID
}

// RecoverStuckTrades re-drives PM buy trades wedged at `paid` with no release:
// SystemMarkPaid (pending→paid) succeeded but AutoRelease then failed BEFORE the
// release tx landed on-chain, leaving an empty release_tx_hash. The chain
// reconciler only recovers these when the tx actually landed (chain shows
// Released); if the broadcast never landed, nothing else re-drives them. This
// sweep re-calls AutoRelease, which is idempotent (early-returns on already
// `released`) and money-invariant-gated, so re-running is safe.
//
// Scope: only `paid` PM trades (pm_id set) older than olderThan with an empty
// release_tx_hash. A `releasing` trade is deliberately excluded — it's either
// mid-flight or already owned by the chain reconciler's stale window. Per-trade
// failures are logged and skipped so one bad trade can't stop the sweep.
func (s *PMAutomationService) RecoverStuckTrades(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().Add(-olderThan)
	var rows []recoverStuckRow
	if err := s.db.WithContext(ctx).
		Table("p2p_trades").
		Select("p2p_trades.id").
		Joins("JOIN p2p_ads ON p2p_ads.id = p2p_trades.ad_id").
		Where("p2p_ads.pm_id IS NOT NULL").
		Where("p2p_trades.status = ?", models.P2PTradeStatusPaid).
		Where("p2p_trades.release_tx_hash = ? OR p2p_trades.release_tx_hash IS NULL", "").
		Where("p2p_trades.updated_at < ?", cutoff).
		Scan(&rows).Error; err != nil {
		return 0, err
	}

	recovered := 0
	for _, r := range rows {
		if _, err := s.trades.AutoRelease(ctx, r.ID, s.signer); err != nil {
			// One bad trade must not stop the sweep — log and continue.
			log.Printf("[pm recovery] AutoRelease failed for paid-orphan trade=%s: %v", r.ID, err)
			continue
		}
		recovered++
	}
	return recovered, nil
}

// retryWebhookRow is the lightweight projection scanned for RetryFailedWebhooks.
type retryWebhookRow struct {
	ID         uuid.UUID
	EventID    string
	Provider   string
	Payload    []byte
	ReceivedAt time.Time
}

// fwChargeNotFoundGrace is how long a charge may keep 404-ing before the event
// is treated as permanently unsettleable. Generous on purpose: a 404 shortly
// after the webhook is normal read-after-write lag at the provider, and giving
// up on a real payment is far worse than a few extra retries. Past this, the
// charge does not exist on this tenant (wrong environment, or an event from a
// provider account we no longer talk to) and re-driving it forever only
// crowds the oldest-first retry pool.
const fwChargeNotFoundGrace = 6 * time.Hour

// RetryFailedWebhooks re-dispatches fiat webhook events that the live handler
// could not settle: rows left `failed` (a dispatch error), or `pending` rows
// orphaned by a crash mid-dispatch (received_at older than staleAfter). For each
// it re-runs the provider-appropriate dispatcher (Nomba or Flutterwave); success
// (or a recognized non-settling event) flips the row to `processed`. A
// still-failing dispatch is left untouched to be retried on the next tick.
// Returns the count moved to `processed`.
//
// Both providers are covered: FW is the primary gateway, so a transiently-failed
// FW charge/transfer must be re-driven here — the FW handler's own dedup returns
// "already processed" on redelivery regardless of status, so this job is the only
// path that heals a failed FW event.
//
// This recovers transient-failure inbound/payout dispatches whose trade is still
// actionable. A paid-orphan is NOT recovered here (its trade is past
// pending_payment, so re-dispatch no-ops) — that's RecoverStuckTrades' job.
func (s *PMAutomationService) RetryFailedWebhooks(ctx context.Context, staleAfter time.Duration) (int, error) {
	cutoff := time.Now().Add(-staleAfter)
	var rows []retryWebhookRow
	if err := s.db.WithContext(ctx).
		Table("webhook_events").
		Select("id, event_id, provider, payload, received_at").
		Where("provider IN ?", []string{ProviderNomba, ProviderFlutterwave}).
		Where("status = ? OR (status = ? AND received_at < ?)", "failed", "pending", cutoff).
		Order("received_at ASC").
		Limit(100).
		Scan(&rows).Error; err != nil {
		return 0, err
	}

	retried := 0
	for _, r := range rows {
		var err error
		switch r.Provider {
		case ProviderFlutterwave:
			err = s.DispatchFlutterwave(ctx, r.Payload)
		default:
			err = s.Dispatch(ctx, r.Payload)
		}
		// A charge the provider still cannot find long after the event arrived
		// never existed here — age it out into the same terminal disposition as
		// an outright rejection, rather than re-driving it every tick forever.
		agedOutNotFound := err != nil &&
			errors.Is(err, ErrFWChargeNotFound) &&
			!r.ReceivedAt.IsZero() &&
			time.Since(r.ReceivedAt) > fwChargeNotFoundGrace

		// Same shape for a payout webhook the provider never finalises: past the
		// grace it stops being read-after-write lag, and re-driving it every tick
		// would hold a slot in this oldest-first pool forever.
		agedOutNotFinal := err != nil &&
			errors.Is(err, ErrITPayoutNotFinal) &&
			!r.ReceivedAt.IsZero() &&
			time.Since(r.ReceivedAt) > itPayoutWebhookGrace

		if err != nil && (errors.Is(err, ErrChargeRejected) || agedOutNotFound || agedOutNotFinal) {
			// Terminal — a retry can never settle it. Mark 'rejected' so it drops
			// out of the failed pool instead of being re-driven every tick.
			if agedOutNotFound {
				log.Printf("[pm recovery] ALARM event=%s charge still not found %s after arrival — marking rejected, NOT settled: %v",
					r.EventID, time.Since(r.ReceivedAt).Round(time.Minute), err)
			}
			if agedOutNotFinal {
				log.Printf("[pm recovery] ALERT it_payout_webhook_never_final event=%s still not final %s after arrival — marking rejected, NOT settled: %v",
					r.EventID, time.Since(r.ReceivedAt).Round(time.Minute), err)
			}
			if uerr := s.db.WithContext(ctx).
				Table("webhook_events").
				Where("id = ?", r.ID).
				Updates(map[string]any{"status": "rejected", "error": err.Error()}).Error; uerr != nil {
				log.Printf("[pm recovery] mark webhook event=%s rejected: %v", r.EventID, uerr)
				continue
			}
			retried++
			continue
		}
		if err != nil && !errors.Is(err, ErrNonSettlingEvent) {
			// Transient / not-yet-succeeded — leave it for the next tick, but
			// refresh the stored cause. Without this the row keeps the FIRST
			// failure forever, so an operator reading the table sees a stale
			// reason that no longer matches why it is failing now.
			log.Printf("[pm recovery] re-dispatch webhook event=%s still failing: %v", r.EventID, err)
			if uerr := s.db.WithContext(ctx).
				Table("webhook_events").
				Where("id = ?", r.ID).
				Update("error", err.Error()).Error; uerr != nil {
				log.Printf("[pm recovery] refresh error for event=%s: %v", r.EventID, uerr)
			}
			continue
		}
		// nil OR ErrNonSettlingEvent → done; flip to processed.
		if uerr := s.db.WithContext(ctx).
			Table("webhook_events").
			Where("id = ?", r.ID).
			Updates(map[string]any{"status": "processed", "processed_at": time.Now()}).Error; uerr != nil {
			log.Printf("[pm recovery] mark webhook event=%s processed: %v", r.EventID, uerr)
			continue
		}
		retried++
	}
	return retried, nil
}

// ─── Sell flow (payout → release) ────────────────────────────────────────────

// InitiateSellPayout pays the user's bank for a deposited PM sell trade and
// records the Nomba payout id. Preconditions: trade is a deposited (funding=1)
// pending_payment trade with a confirmed deposit (open_tx_hash set) and a
// KYC-name-verified payout destination. Idempotent-ish: if a payout id is
// already recorded, it returns that id without re-initiating.
//
// VA-only model: all payouts come from TSA's single parent Nomba account (no
// per-PM sub-account routing). When the trade's ad belongs to a PM, SenderName
// is set to pm.Name for the transfer's display; for non-PM trades the
// service-level senderName is used. The debit account is always the parent.
func (s *PMAutomationService) InitiateSellPayout(ctx context.Context, tradeID uuid.UUID) (string, error) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrTradeNotFound
		}
		return "", err
	}
	if trade.NombaPayoutID != "" {
		return trade.NombaPayoutID, nil // already initiated — don't double-pay
	}
	if trade.Status != models.P2PTradeStatusPendingPayment ||
		trade.Funding != models.P2PTradeFundingDepositedByTaker ||
		strings.TrimSpace(trade.OpenTxHash) == "" {
		return "", fmt.Errorf("%w: trade not a confirmed deposited sell trade", ErrInvalidTransition)
	}
	if trade.PayoutAccountNumber == "" || trade.PayoutBank == "" {
		return "", fmt.Errorf("%w: payout destination missing", ErrInvalidParams)
	}

	// Resolve the PM (nil for non-PM trades) to determine which Nomba account to debit.
	pm, err := s.pmForTrade(ctx, tradeID)
	if err != nil {
		return "", fmt.Errorf("resolve PM for sell payout: %w", err)
	}

	// Build payout request; default to parent-account (legacy) path.
	req := PayoutRequest{
		BankCode:      trade.PayoutBank,
		AccountNumber: trade.PayoutAccountNumber,
		AmountNGN:     trade.FiatAmount,
		Reference:     trade.ID.String(),
		AccountName:   trade.PayoutAccountName,
		SenderName:    s.senderName,
	}
	if pm != nil {
		// PM trade: parent-debited (VA model); use the PM's name as the sender.
		req.SenderName = pm.Name
	}

	payoutID, err := s.gateway.InitiatePayout(ctx, req)
	if err != nil {
		return "", fmt.Errorf("initiate nomba payout: %w", err)
	}
	if strings.TrimSpace(payoutID) == "" {
		return "", fmt.Errorf("nomba returned empty payout id")
	}
	// Guard the write on nomba_payout_id='' so a concurrent caller can't clobber
	// an id another goroutine just recorded (then double-pay on the next webhook).
	if err := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("id = ? AND nomba_payout_id = ''", trade.ID).
		Update("nomba_payout_id", payoutID).Error; err != nil {
		return "", err
	}
	return payoutID, nil
}

// HandlePayoutStatus settles a sell trade on the payout-result webhook. On
// success the escrowed token is released to the PM (AutoRelease). On failure we
// record a marker + alert and leave the trade pending_payment so the existing
// timeout watchdog refunds the deposit on expiry (or an operator handles it via
// the manual queue) — we deliberately do NOT auto-refund on-chain here.
//
// TODO(pm-phase2): automated bounded-retry + on-chain refund once
// adminResolve-from-pending semantics are confirmed.
func (s *PMAutomationService) HandlePayoutStatus(ctx context.Context, p PayoutStatus) error {
	// A settlement forward is not a sell payout, and its reference is not a
	// trade UUID — so without this branch a disburse result for one would fall
	// through to the "untracked payout, ignoring" log below, and the merchant's
	// ledger would never be debited for money that actually reached their bank.
	// Routed on the reference prefix, which no trade id can carry.
	if s.forwards != nil && strings.HasPrefix(strings.TrimSpace(p.MerchantTxRef), p2pForwardRefPrefix) {
		status := strings.ToLower(strings.TrimSpace(p.Status))
		switch status {
		case "success":
			return s.forwards.Confirm(ctx, p.MerchantTxRef, true, "")
		case "failed":
			return s.forwards.Confirm(ctx, p.MerchantTxRef, false, "payout "+p.PayoutID+" (event "+p.EventID+")")
		default:
			return nil // pending or unknown — await a terminal result
		}
	}

	// Resolve the trade. The deterministic key is merchantTxRef (= the trade UUID
	// we sent when initiating the payout). Fall back to the stored nomba_payout_id
	// only if merchantTxRef is empty/unparseable. Require at least one usable key:
	// a parseable merchantTxRef OR a non-empty payout id — else an empty
	// "nomba_payout_id = ''" lookup would match an arbitrary untracked trade and
	// release it. If BOTH are unusable, this is a no-op.
	var trade models.P2PTrade
	var err error
	switch {
	case strings.TrimSpace(p.MerchantTxRef) != "":
		tradeID, parseErr := uuid.Parse(strings.TrimSpace(p.MerchantTxRef))
		if parseErr != nil {
			// merchantTxRef present but not a UUID — fall back to payout id below.
			if strings.TrimSpace(p.PayoutID) == "" {
				log.Printf("[pm automation] payout status %q with unparseable merchantTxRef %q and empty payout id (event=%s); ignoring", p.Status, p.MerchantTxRef, p.EventID)
				return nil
			}
			err = s.db.WithContext(ctx).First(&trade, "nomba_payout_id = ?", p.PayoutID).Error
		} else {
			err = s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error
		}
	case strings.TrimSpace(p.PayoutID) != "":
		err = s.db.WithContext(ctx).First(&trade, "nomba_payout_id = ?", p.PayoutID).Error
	default:
		// Neither a usable merchantTxRef nor a payout id — no key to match on.
		log.Printf("[pm automation] payout status %q with no usable key (empty merchantTxRef + payout id) (event=%s); ignoring", p.Status, p.EventID)
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// No trade for this payout — a payout we don't track. Log and ignore.
		log.Printf("[pm automation] payout status %q for untracked payout (merchantTxRef=%q payoutId=%q event=%s); ignoring", p.Status, p.MerchantTxRef, p.PayoutID, p.EventID)
		return nil
	}
	if err != nil {
		return err
	}

	// Resolve the PM owning this trade's ad (nil when not a PM trade) so the NGN
	// ledger can be settled alongside the on-chain release / failure handling.
	pm, pmErr := s.pmForTrade(ctx, trade.ID)
	if pmErr != nil {
		return pmErr
	}

	switch strings.ToLower(p.Status) {
	case "success":
		// Money already left the PM's Nomba account → consume the held NGN
		// reservation (available-=fiat, reserved-=fiat) BEFORE releasing the
		// token, then release the escrowed crypto to the PM. ConsumeNGN is
		// guarded (reserved >= amount); a missing/short reservation surfaces as an
		// error rather than silently releasing crypto against an unbacked ledger.
		if pm != nil {
			if err := s.pm.ConsumeIn(ctx, pm.ID, trade.FiatCurrency, trade.FiatAmount); err != nil {
				if errors.Is(err, ErrNGNAlreadySettled) {
					// Retry-tolerant: a re-dispatched success event (its first
					// dispatch consumed the reservation but its release failed
					// transiently). reserved_ngn is already 0, so consume is a
					// no-op here — do NOT touch available_ngn again. Fall through
					// to AutoRelease (idempotent) so the wedged trade releases.
					log.Printf("[pm automation] payout success consume no-op (already settled) for pm=%s trade=%s (event=%s); proceeding to release", pm.ID, trade.ID, p.EventID)
				} else {
					return fmt.Errorf("consume PM ledger on payout success (pm=%s trade=%s): %w", pm.ID, trade.ID, err)
				}
			}
		}
		_, err := s.trades.AutoRelease(ctx, trade.ID, s.signer)
		return err
	case "failed":
		// SCOPING: alert + manual/watchdog, NO auto-refund (see TODO above).
		// Release the held NGN reservation back to the PM's free balance (the
		// payout did not leave the account), record a marker so the manual queue /
		// operator sees it, and WARN-log for alerting. Leave the trade in
		// pending_payment so the timeout watchdog refunds the deposit on expiry.
		if pm != nil {
			if err := s.pm.ReleaseIn(ctx, pm.ID, trade.FiatCurrency, trade.FiatAmount); err != nil {
				return fmt.Errorf("release PM ledger on payout failure (pm=%s trade=%s): %w", pm.ID, trade.ID, err)
			}
		}
		log.Printf("[pm automation] WARN payout FAILED for trade=%s payout=%s (event=%s); leaving pending_payment for watchdog/manual", trade.ID, p.PayoutID, p.EventID)
		// Mark the reservation as already released so the later cancel path
		// (timeout watchdog / ConfirmCancel) does NOT release it a second time —
		// a double-release would steal another live trade's reservation or hard-
		// error and wedge the cancel. Set in the same UPDATE as cancel_reason.
		// Only set the marker when there was a PM reservation to release (pm != nil);
		// a non-PM trade never reserved, so leave the marker false.
		updates := map[string]any{
			"cancel_reason": "pm payout failed (nomba payout=" + p.PayoutID + "); routed to manual",
		}
		if pm != nil {
			updates["ngn_reservation_released"] = true
		}
		if err := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
			Where("id = ?", trade.ID).
			Updates(updates).Error; err != nil {
			return err
		}
		return nil
	default:
		// "pending" or unknown — no-op; await a terminal webhook.
		return nil
	}
}

// ─── Manual-release queue ─────────────────────────────────────────────────────

// ManualQueueItem is an operator-facing row for an unmatched PM buy trade.
type ManualQueueItem struct {
	TradeID     uuid.UUID `json:"tradeId"`
	AdID        uuid.UUID `json:"adId"`
	BuyerID     uuid.UUID `json:"buyerId"`
	BuyerName   string    `json:"buyerName"`
	Token       string    `json:"token"`
	TokenAmount string    `json:"tokenAmount"`
	FiatAmount  string    `json:"fiatAmount"`
	CreatedAt   time.Time `json:"createdAt"`
}

// ErrSettleViaInboundAssign refuses an operator release of a PM buy trade that
// has no attributed payment while buyers pay the company account. Its money,
// if any arrived, is a held inbound credit, and only assigning that credit
// credits the merchant; releasing from here would pay out the crypto and leave
// the merchant unpaid with the assign no longer possible.
var ErrSettleViaInboundAssign = errors.New("this trade's payment goes to the company account: assign its held credit under Admin → Inbound credits instead of releasing it here")

// SetCentralInbound is set at boot from the same decision that routes PM
// buyers to the company account. The manual queue then lists only `paid`
// orphans — trades whose money is already attributed — and ManualRelease
// refuses the rest with ErrSettleViaInboundAssign.
func (s *PMAutomationService) SetCentralInbound(central bool) { s.centralInbound = central }

// manualQueueRow is the lightweight projection scanned from the join for
// ListBuyManualQueue.
type manualQueueRow struct {
	ID          uuid.UUID
	AdID        uuid.UUID
	BuyerID     uuid.UUID
	Token       string
	TokenAmount string
	FiatAmount  string
	CreatedAt   time.Time
}

// ListBuyManualQueue returns PM buy trades stuck awaiting manual review:
// pending_payment OR paid, pooled sell-ad funding (funding=0), on a PM ad
// (pm_id IS NOT NULL), with NO matched inbound (nomba_inbound_event_id = ”),
// with open_tx_hash set (on-chain open confirmed), and not yet released
// (release_tx_hash empty). Most recent first.
//
// The `paid` branch recovers a `paid`-orphan: the buy flow does
// SystemMarkPaid (pending_payment→paid) then AutoRelease, and if AutoRelease
// fails after markPaid the rollback leaves the trade at `paid` with no
// release. A bare `paid` trade is invisible to the timeout watchdog (filters
// pending_payment) and the reconciler (on-chain still Pending; it never
// reverts paid→pending), so it would never recover. Surfacing it here lets an
// operator re-drive it via ManualRelease (which tolerates the already-paid
// ErrInvalidTransition from SystemMarkPaid and re-runs AutoRelease). The
// release_tx_hash guard ensures already-released trades never appear.
func (s *PMAutomationService) ListBuyManualQueue(ctx context.Context) ([]ManualQueueItem, error) {
	statuses := []string{models.P2PTradeStatusPendingPayment, models.P2PTradeStatusPaid}
	if s.centralInbound {
		// Unattributed trades are settled by assigning their held credit; see
		// ErrSettleViaInboundAssign.
		statuses = []string{models.P2PTradeStatusPaid}
	}
	var rows []manualQueueRow
	if err := s.db.WithContext(ctx).
		Table("p2p_trades").
		Select("p2p_trades.id, p2p_trades.ad_id, p2p_trades.buyer_id, p2p_trades.token, p2p_trades.token_amount, p2p_trades.fiat_amount, p2p_trades.created_at").
		Joins("JOIN p2p_ads ON p2p_ads.id = p2p_trades.ad_id").
		Where("p2p_ads.pm_id IS NOT NULL").
		Where("p2p_ads.side = ?", models.P2PAdSideSell).
		Where("p2p_trades.status IN ?", statuses).
		Where("p2p_trades.release_tx_hash = ? OR p2p_trades.release_tx_hash IS NULL", "").
		Where("p2p_trades.funding = ?", models.P2PTradeFundingPooled).
		Where("p2p_trades.open_tx_hash <> ?", "").
		Where("p2p_trades.nomba_inbound_event_id = ?", "").
		Order("p2p_trades.created_at DESC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	items := make([]ManualQueueItem, 0, len(rows))
	for _, r := range rows {
		name, err := s.name(ctx, r.BuyerID)
		if err != nil {
			// Skip-name-on-error: leave BuyerName "" rather than failing the whole
			// queue listing (one bad name lookup must not hide valid queue items).
			log.Printf("[pm manual queue] name lookup failed for buyer=%s trade=%s: %v", r.BuyerID, r.ID, err)
			name = ""
		}
		items = append(items, ManualQueueItem{
			TradeID:     r.ID,
			AdID:        r.AdID,
			BuyerID:     r.BuyerID,
			BuyerName:   name,
			Token:       r.Token,
			TokenAmount: r.TokenAmount,
			FiatAmount:  r.FiatAmount,
			CreatedAt:   r.CreatedAt,
		})
	}
	return items, nil
}

// pmForTrade resolves the PaymentMerchant owning the given trade's ad, or
// (nil, nil) when the trade is not on a PM ad (ad.pm_id IS NULL) or the PM row
// is gone. A genuine query error is returned. Used by ManualRelease to apply the
// PM daily cap without changing behaviour for non-PM trades.
func (s *PMAutomationService) pmForTrade(ctx context.Context, tradeID uuid.UUID) (*models.PaymentMerchant, error) {
	var row struct{ PMID *uuid.UUID }
	err := s.db.WithContext(ctx).
		Table("p2p_trades").
		Select("p2p_ads.pm_id AS pm_id").
		Joins("JOIN p2p_ads ON p2p_ads.id = p2p_trades.ad_id").
		Where("p2p_trades.id = ?", tradeID).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.PMID == nil {
		return nil, nil // not a PM trade
	}
	pm, err := s.pm.GetByID(ctx, *row.PMID)
	if err != nil {
		if errors.Is(err, ErrPMNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return pm, nil
}

// tradeUSDForCap loads the trade and returns its USD value (tradeUSDValue). ok
// is false when the trade can't be loaded or its token_amount is unparseable —
// in which case the daily-cap gate is skipped (the trade can't be valued; the
// existing per-trade guard already blocks the auto path for such rows).
func (s *PMAutomationService) tradeUSDForCap(ctx context.Context, tradeID uuid.UUID) (float64, bool) {
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		return 0, false
	}
	return tradeUSDValue(&trade)
}

// ManualRelease lets an operator settle a manually-verified PM buy trade:
// system markPaid (ref "manual:<adminID>") then adminRelease. adminID is the
// operator performing the release (for the ref/audit trail). ErrInvalidTransition
// on markPaid is tolerated (already paid) and execution continues to release.
func (s *PMAutomationService) ManualRelease(ctx context.Context, tradeID, adminID uuid.UUID) (*models.P2PTrade, error) {
	// Daily-cap gate (operator path). Reach the PM via the trade's ad's pm_id. If
	// the trade belongs to a PM with a daily cap, an over-cap release returns a
	// clear error so the operator sees it (we never silently release over cap). A
	// non-PM trade (no pm_id) or a query miss leaves the legacy behaviour intact.
	//
	// The cap only applies to a fresh admission (pending_payment). A trade already
	// at paid is a paid-orphan re-drive: its capacity was already admitted when it
	// first transitioned to paid, so counting it again here would double-count it
	// and spuriously block a legitimate rescue.
	if pm, err := s.pmForTrade(ctx, tradeID); err != nil {
		return nil, err
	} else if pm != nil {
		// Load the trade status to distinguish a fresh pending_payment admission
		// from a paid-orphan re-drive.
		var tradeStatus struct{ Status string }
		if err := s.db.WithContext(ctx).
			Table("p2p_trades").
			Select("status").
			Where("id = ?", tradeID).
			Scan(&tradeStatus).Error; err != nil {
			return nil, fmt.Errorf("load trade status for cap check: %w", err)
		}
		if tradeStatus.Status == models.P2PTradeStatusPendingPayment && s.centralInbound {
			return nil, ErrSettleViaInboundAssign
		}
		if tradeStatus.Status == models.P2PTradeStatusPendingPayment {
			// Fresh admission — apply the daily-cap gate.
			usd, ok := s.tradeUSDForCap(ctx, tradeID)
			if ok {
				within, derr := s.pm.WithinDailyCap(ctx, pm, usd)
				if derr != nil {
					return nil, fmt.Errorf("daily-cap check: %w", derr)
				}
				if !within {
					return nil, fmt.Errorf("%w: release of $%.2f would exceed PM daily cap $%.2f", ErrInvalidParams, usd, pm.DailyCapUSD)
				}
			}
		}
		// paid (orphan re-drive): skip the daily-cap gate — this is completing an
		// already-admitted trade, not admitting a new one.
	}

	if _, err := s.trades.SystemMarkPaid(ctx, tradeID, "manual:"+adminID.String()); err != nil {
		if !errors.Is(err, ErrInvalidTransition) {
			return nil, err
		}
		// Already paid — continue to release.
	}
	return s.trades.AutoRelease(ctx, tradeID, s.signer)
}
