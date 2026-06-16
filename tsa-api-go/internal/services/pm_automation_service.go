package services

import (
	"context"
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
	db      *gorm.DB
	pm      *PMService
	trades  *P2PTradeService
	signer  ReleaseSigner
	name    PMNameLookup
	gateway NombaGateway
}

// NewPMAutomationService wires the PM buy-flow + sell-flow automation. gateway
// is the per-PM Nomba boundary used by the sell flow to pay the user's bank.
func NewPMAutomationService(db *gorm.DB, pm *PMService, trades *P2PTradeService, signer ReleaseSigner, name PMNameLookup, gateway NombaGateway) *PMAutomationService {
	return &PMAutomationService{db: db, pm: pm, trades: trades, signer: signer, name: name, gateway: gateway}
}

// inboundCandidateRow is the lightweight projection scanned from the
// p2p_trades⋈p2p_ads join.
type inboundCandidateRow struct {
	ID         uuid.UUID
	BuyerID    uuid.UUID
	FiatAmount string
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

	// 2. Resolve the PM by the destination (inbound) account.
	pm, err := s.pm.GetByInboundAccount(ctx, p.DestinationAccount)
	if err != nil {
		if errors.Is(err, ErrPMNotFound) {
			log.Printf("[pm automation] inbound to unknown account %q (event=%s); ignoring", p.DestinationAccount, p.EventID)
			return nil
		}
		return err
	}

	// 3. Paused PM: drain in-flight, no new auto-release.
	if !pm.IsActive() {
		return nil
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

	// 6. Cap check (USD). Over-cap → manual queue. Unknown USD (unparseable
	//    TokenAmount) → also route to manual: never auto-release when the value
	//    of the trade cannot be verified.
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		return err
	}
	usd, usdOK := tradeUSDValue(&trade)
	if !usdOK {
		log.Printf("[pm automation] trade=%s has unparseable token_amount %q; routing to manual queue (cannot verify cap)", tradeID, trade.TokenAmount)
		return nil
	}
	if !WithinPerTradeCap(pm, usd) {
		log.Printf("[pm automation] trade=%s usd=%.2f over PM per-trade cap; routing to manual queue", tradeID, usd)
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

// ─── Sell flow (payout → release) ────────────────────────────────────────────

// InitiateSellPayout pays the user's bank for a deposited PM sell trade and
// records the Nomba payout id. Preconditions: trade is a deposited (funding=1)
// pending_payment trade with a confirmed deposit (open_tx_hash set) and a
// KYC-name-verified payout destination. Idempotent-ish: if a payout id is
// already recorded, it returns that id without re-initiating.
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
	payoutID, err := s.gateway.InitiatePayout(ctx, PayoutRequest{
		BankCode:      trade.PayoutBank,
		AccountNumber: trade.PayoutAccountNumber,
		AmountNGN:     trade.FiatAmount,
		Reference:     trade.ID.String(),
	})
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
	if strings.TrimSpace(p.PayoutID) == "" {
		log.Printf("[pm automation] payout status %q with empty payout id (event=%s); ignoring", p.Status, p.EventID)
		return nil
	}

	var trade models.P2PTrade
	err := s.db.WithContext(ctx).First(&trade, "nomba_payout_id = ?", p.PayoutID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// No trade for this payout id — a payout we don't track. Log and ignore.
		log.Printf("[pm automation] payout status %q for untracked payout id %q (event=%s); ignoring", p.Status, p.PayoutID, p.EventID)
		return nil
	}
	if err != nil {
		return err
	}

	switch strings.ToLower(p.Status) {
	case "success":
		// Money already left the PM's Nomba account → release token to the PM.
		_, err := s.trades.AutoRelease(ctx, trade.ID, s.signer)
		return err
	case "failed":
		// SCOPING: alert + manual/watchdog, NO auto-refund (see TODO above).
		// Record a marker on the trade so the manual queue / operator sees it,
		// and WARN-log for alerting. Leave the trade in pending_payment so the
		// timeout watchdog refunds the deposit on expiry.
		log.Printf("[pm automation] WARN payout FAILED for trade=%s payout=%s (event=%s); leaving pending_payment for watchdog/manual", trade.ID, p.PayoutID, p.EventID)
		if err := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
			Where("id = ?", trade.ID).
			Update("cancel_reason", "pm payout failed (nomba payout="+p.PayoutID+"); routed to manual").Error; err != nil {
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
// (pm_id IS NOT NULL), with NO matched inbound (nomba_inbound_event_id = ''),
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
	var rows []manualQueueRow
	if err := s.db.WithContext(ctx).
		Table("p2p_trades").
		Select("p2p_trades.id, p2p_trades.ad_id, p2p_trades.buyer_id, p2p_trades.token, p2p_trades.token_amount, p2p_trades.fiat_amount, p2p_trades.created_at").
		Joins("JOIN p2p_ads ON p2p_ads.id = p2p_trades.ad_id").
		Where("p2p_ads.pm_id IS NOT NULL").
		Where("p2p_ads.side = ?", models.P2PAdSideSell).
		Where("p2p_trades.status IN ?", []string{models.P2PTradeStatusPendingPayment, models.P2PTradeStatusPaid}).
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

// ManualRelease lets an operator settle a manually-verified PM buy trade:
// system markPaid (ref "manual:<adminID>") then adminRelease. adminID is the
// operator performing the release (for the ref/audit trail). ErrInvalidTransition
// on markPaid is tolerated (already paid) and execution continues to release.
func (s *PMAutomationService) ManualRelease(ctx context.Context, tradeID, adminID uuid.UUID) (*models.P2PTrade, error) {
	if _, err := s.trades.SystemMarkPaid(ctx, tradeID, "manual:"+adminID.String()); err != nil {
		if !errors.Is(err, ErrInvalidTransition) {
			return nil, err
		}
		// Already paid — continue to release.
	}
	return s.trades.AutoRelease(ctx, tradeID, s.signer)
}
