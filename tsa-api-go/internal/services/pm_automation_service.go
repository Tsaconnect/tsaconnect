package services

import (
	"context"
	"errors"
	"log"
	"math/big"
	"strconv"
	"strings"

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
// fractional part is padded/truncated to exactly 2 digits. Returns ok=false on
// any parse error (empty, non-numeric, multiple dots, negative).
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
		// A second dot makes the fractional part unparseable below.
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
	db     *gorm.DB
	pm     *PMService
	trades *P2PTradeService
	signer ReleaseSigner
	name   PMNameLookup
}

// NewPMAutomationService wires the PM buy-flow automation.
func NewPMAutomationService(db *gorm.DB, pm *PMService, trades *P2PTradeService, signer ReleaseSigner, name PMNameLookup) *PMAutomationService {
	return &PMAutomationService{db: db, pm: pm, trades: trades, signer: signer, name: name}
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

	// 6. Cap check (USD). Over-cap → manual queue.
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", tradeID).Error; err != nil {
		return err
	}
	usd := tradeUSDValue(&trade)
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
// baseUnitsToUSD / the handlers' weiToUSDFloat convention. Returns 0 on an
// unparseable amount.
func tradeUSDValue(trade *models.P2PTrade) float64 {
	amt, ok := new(big.Int).SetString(trade.TokenAmount, 10)
	if !ok {
		return 0
	}
	return baseUnitsToUSD(amt, tokenDecimalsFor(trade.ChainID, trade.Token))
}
