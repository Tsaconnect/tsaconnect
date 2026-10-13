package services

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

const (
	LiquidityStatusNone     = "none"
	LiquidityStatusApplied  = "applied"
	LiquidityStatusRejected = "rejected"
	LiquidityStatusApproved = "approved"
	LiquidityStatusActive   = "active"
	LiquidityStatusPaused   = "paused"
)

var (
	ErrNotLiquidityProvider    = errors.New("liquidity: not a liquidity provider")
	ErrLiquidityActivityType   = errors.New("liquidity: activity type must be deposits, withdrawals or payouts")
	ErrLiquidityActivityCursor = errors.New("liquidity: invalid activity cursor")
)

type LiquidityChain struct {
	ID   int64  `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

type LiquidityToken struct {
	Symbol   string `json:"symbol"`
	Address  string `json:"address"`
	Decimals int    `json:"decimals"`
}

// LiquidityLimits are USD at the 1 USDT = $1 peg.
type LiquidityLimits struct {
	Min string `json:"min"`
	Max string `json:"max"`
}

type LiquidityVoucher struct {
	AdID      uuid.UUID  `json:"adId"`
	Status    string     `json:"status"`
	Deadline  *time.Time `json:"deadline,omitempty"`
	Remaining string     `json:"remaining"`
	RenewDue  bool       `json:"renewDue"`
}

// LiquidityMe is GET /api/liquidity/me (spec §15; plan B P12). Token amounts
// are decimal strings. lpRate, poolRate, share and earnings arrive with C1/C2.
type LiquidityMe struct {
	Status          string            `json:"status"`
	PoolOpen        bool              `json:"poolOpen"`
	Chain           *LiquidityChain   `json:"chain,omitempty"`
	Token           *LiquidityToken   `json:"token,omitempty"`
	Wallet          string            `json:"wallet,omitempty"`
	BalanceReadable bool              `json:"balanceReadable"`
	Available       string            `json:"available,omitempty"`
	Locked          string            `json:"locked,omitempty"`
	Stake           string            `json:"stake,omitempty"`
	AboveMax        string            `json:"aboveMax,omitempty"`
	Headroom        string            `json:"headroom,omitempty"`
	Limits          LiquidityLimits   `json:"limits"`
	Voucher         *LiquidityVoucher `json:"voucher,omitempty"`
	OwedNgn         string            `json:"owedNgn"`
	Paused          bool              `json:"paused"`
}

// LiquidityActivityItem never names the buyer or a trade (spec §19).
type LiquidityActivityItem struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	CreatedAt time.Time  `json:"createdAt"`
	Amount    string     `json:"amount,omitempty"`
	TxHash    string     `json:"txHash,omitempty"`
	AmountNgn string     `json:"amountNgn,omitempty"`
	Status    string     `json:"status,omitempty"`
	Reference string     `json:"reference,omitempty"`
	BankLast4 string     `json:"bankLast4,omitempty"`
	SettledAt *time.Time `json:"settledAt,omitempty"`
}

type LiquidityActivityPage struct {
	Items      []LiquidityActivityItem `json:"items"`
	NextCursor string                  `json:"nextCursor,omitempty"`
}

// LPAdminView is one LP row's extra columns on /admin/pm?kind=lp (spec §20).
type LPAdminView struct {
	Available       string     `json:"available,omitempty"`
	Locked          string     `json:"locked,omitempty"`
	Stake           string     `json:"stake,omitempty"`
	AboveMax        string     `json:"aboveMax,omitempty"`
	BalanceError    string     `json:"balanceError,omitempty"`
	VoucherDeadline *time.Time `json:"voucherDeadline,omitempty"`
	RenewDue        bool       `json:"renewDue"`
	OwedNgn         string     `json:"owedNgn"`
}

// LiquidityService is the LP read side (spec §15). It never takes another
// user's id: every read is scoped by the caller's LP row.
type LiquidityService struct {
	db        *gorm.DB
	pm        *PMService
	trade     *P2PTradeService
	stock     *PoolStock
	gate      PoolOpener
	chainName string
	now       func() time.Time
	// adminDeadline bounds one AdminLPViews call; adminLPViewDeadline.
	adminDeadline time.Duration
}

// NewLiquidityService builds the LP read side and subscribes stock to the trade
// service's confirmed deposits and withdrawals, so an LP's cached balance never
// outlives a move it made (review I4).
func NewLiquidityService(db *gorm.DB, pm *PMService, trade *P2PTradeService, stock *PoolStock, gate PoolOpener, chainName string) *LiquidityService {
	if trade != nil && stock != nil {
		trade.OnPoolBalanceMoved(stock.Forget)
	}
	return &LiquidityService{db: db, pm: pm, trade: trade, stock: stock, gate: gate, chainName: chainName, now: time.Now,
		adminDeadline: adminLPViewDeadline}
}

func trimUSD(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func (s *LiquidityService) Me(ctx context.Context, userID uuid.UUID) (*LiquidityMe, error) {
	minUSD, maxUSD := PoolLPLimitsUSD()
	out := &LiquidityMe{Status: LiquidityStatusNone, PoolOpen: s.gate != nil && s.gate.Open(), OwedNgn: "0.00",
		Limits: LiquidityLimits{Min: trimUSD(minUSD), Max: trimUSD(maxUSD)}}
	if tok, ok := s.stock.Token(); ok {
		out.Chain = &LiquidityChain{ID: tok.ChainID, Key: tok.ChainKey, Name: s.chainName}
		out.Token = &LiquidityToken{Symbol: tok.Symbol, Address: tok.Address, Decimals: tok.Decimals}
	}
	pm, err := s.pm.GetByUserIDKind(ctx, userID, models.PaymentMerchantKindLP)
	switch {
	case errors.Is(err, ErrPMNotFound):
		return s.fromRequest(ctx, userID, out)
	case err != nil:
		return nil, err
	}
	out.Wallet = pm.OperatorWalletAddress
	out.OwedNgn = pm.AvailableNGN
	switch pm.Status {
	case models.PaymentMerchantStatusActive:
		out.Status = LiquidityStatusActive
	case models.PaymentMerchantStatusPaused:
		out.Status, out.Paused = LiquidityStatusPaused, true
	case models.PaymentMerchantStatusRejected:
		out.Status = LiquidityStatusRejected
		return out, nil
	case models.PaymentMerchantStatusApproved: // activate first
		out.Status = LiquidityStatusApproved
		return out, nil
	default:
		// pending: the approval has not finished (or is being re-run), and
		// ActivateLP needs an approved row, so it still reads as under review
		// rather than offering an activation that fails (review M14).
		out.Status = LiquidityStatusApplied
		return out, nil
	}
	pos, err := s.stock.Position(ctx, pm, false)
	if err != nil {
		log.Printf("[liquidity] LP %s balance unreadable: %s", pm.ID, RedactAlertText(err.Error()))
		return out, nil
	}
	d := pos.Token.Decimals
	held := new(big.Int).Add(pos.Available, pos.Locked)
	headroom := new(big.Int).Sub(pos.Max, held)
	if headroom.Sign() < 0 {
		headroom.SetInt64(0)
	}
	out.BalanceReadable = true
	out.Available, out.Locked = BaseUnitsToDecimal(pos.Available, d), BaseUnitsToDecimal(pos.Locked, d)
	out.Stake, out.AboveMax = BaseUnitsToDecimal(pos.Stake, d), BaseUnitsToDecimal(pos.AboveMax, d)
	out.Headroom = BaseUnitsToDecimal(headroom, d)
	if ad, err := s.trade.LatestStockAd(ctx, pm, pos.Token.ChainID, pos.Token.Symbol); err == nil && ad != nil {
		rem, _ := new(big.Int).SetString(ad.Remaining, 10)
		out.Voucher = &LiquidityVoucher{AdID: ad.ID, Status: ad.Status, Deadline: ad.VoucherDeadline,
			Remaining: BaseUnitsToDecimal(rem, d), RenewDue: poolVoucherRenewDue(ad, s.now())}
	}
	return out, nil
}

// fromRequest reports an application that has no LP row yet.
func (s *LiquidityService) fromRequest(ctx context.Context, userID uuid.UUID, out *LiquidityMe) (*LiquidityMe, error) {
	var req models.MerchantRequest
	err := s.db.WithContext(ctx).Where("user_id = ? AND business_type = ?", userID, models.BusinessTypeLiquidityProvider).
		Order("created_at DESC").First(&req).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	switch req.Status {
	case models.MerchantRequestStatusPending:
		out.Status = LiquidityStatusApplied
	case models.MerchantRequestStatusRejected:
		out.Status = LiquidityStatusRejected
	case models.MerchantRequestStatusApproved:
		// Approved but the LP row is not there yet (setup failed; an admin
		// re-approves to resume). "approved" would offer an activation that
		// 404s, so it reads as still under review (review M14).
		out.Status = LiquidityStatusApplied
	}
	return out, nil
}

const liquidityAuditScan = 200

// activityCursor is a (created_at, id) keyset position, so rows that share a
// timestamp are never skipped across a page boundary (review M2).
type activityCursor struct {
	at time.Time
	id uuid.UUID // uuid.Nil: the first page, everything before at
}

// encode is "<RFC3339Nano createdAt>_<id>".
func (c activityCursor) encode() string {
	return c.at.UTC().Format(time.RFC3339Nano) + "_" + c.id.String()
}

func parseActivityCursor(v string) (activityCursor, error) {
	at, id, ok := strings.Cut(v, "_")
	if !ok {
		return activityCursor{}, ErrLiquidityActivityCursor
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return activityCursor{}, ErrLiquidityActivityCursor
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return activityCursor{}, ErrLiquidityActivityCursor
	}
	return activityCursor{at: t.UTC(), id: u}, nil
}

// before scopes q to rows strictly after the cursor in (created_at DESC, id
// DESC) order, and orders them so.
func (c activityCursor) before(q *gorm.DB) *gorm.DB {
	if c.id == uuid.Nil {
		q = q.Where("created_at < ?", c.at)
	} else {
		q = q.Where("(created_at < ? OR (created_at = ? AND id < ?))", c.at, c.at, c.id)
	}
	return q.Order("created_at DESC").Order("id DESC")
}

// Activity pages the LP's deposits, withdrawals (confirmed audit rows on the
// pool chain) or payouts (settlement forwards), newest first. cursor is the
// nextCursor of the previous page. Fills arrive with Plan C1.
func (s *LiquidityService) Activity(ctx context.Context, userID uuid.UUID, typ, cursor string, limit int) (*LiquidityActivityPage, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	pm, err := s.pm.GetByUserIDKind(ctx, userID, models.PaymentMerchantKindLP)
	if errors.Is(err, ErrPMNotFound) {
		return nil, ErrNotLiquidityProvider
	}
	if err != nil {
		return nil, err
	}
	// UTC: SQLite compares the stored text.
	from := activityCursor{at: s.now().UTC().Add(time.Hour)}
	if cursor != "" {
		if from, err = parseActivityCursor(cursor); err != nil {
			return nil, err
		}
	}
	switch typ {
	case "deposits":
		return s.auditActivity(ctx, userID, models.P2PAuditDepositConfirmed, "deposit", from, limit)
	case "withdrawals":
		return s.auditActivity(ctx, userID, models.P2PAuditWithdrawConfirmed, "withdrawal", from, limit)
	case "payouts":
		return s.payoutActivity(ctx, pm, from, limit)
	}
	return nil, ErrLiquidityActivityType
}

func (s *LiquidityService) auditActivity(ctx context.Context, userID uuid.UUID, action, kind string, from activityCursor, limit int) (*LiquidityActivityPage, error) {
	tok, ok := s.stock.Token()
	if !ok {
		return &LiquidityActivityPage{Items: []LiquidityActivityItem{}}, nil
	}
	var rows []models.P2PAuditLog
	q := s.db.WithContext(ctx).Where("actor_id = ? AND action = ?", userID, action)
	if err := from.before(q).Limit(liquidityAuditScan).Find(&rows).Error; err != nil {
		return nil, err
	}
	page := &LiquidityActivityPage{Items: []LiquidityActivityItem{}}
	for _, r := range rows {
		var meta struct {
			ChainID int64  `json:"chainId"`
			Amount  string `json:"amount"`
			TxHash  string `json:"txHash"`
		}
		if json.Unmarshal(r.Metadata, &meta) != nil || meta.ChainID != tok.ChainID {
			continue
		}
		amt, ok := new(big.Int).SetString(meta.Amount, 10)
		if !ok {
			continue
		}
		page.Items = append(page.Items, LiquidityActivityItem{ID: r.ID.String(), Type: kind, CreatedAt: r.CreatedAt,
			Amount: BaseUnitsToDecimal(amt, tok.Decimals), TxHash: meta.TxHash})
		if len(page.Items) == limit {
			page.NextCursor = activityCursor{at: r.CreatedAt, id: r.ID}.encode()
			return page, nil
		}
	}
	if len(rows) == liquidityAuditScan {
		last := rows[len(rows)-1]
		page.NextCursor = activityCursor{at: last.CreatedAt, id: last.ID}.encode()
	}
	return page, nil
}

func (s *LiquidityService) payoutActivity(ctx context.Context, lp *models.PaymentMerchant, from activityCursor, limit int) (*LiquidityActivityPage, error) {
	var rows []models.P2PSettlementForward
	q := s.db.WithContext(ctx).Where("pm_id = ?", lp.ID)
	if err := from.before(q).Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	last4 := lp.SettlementAccountNumber
	if len(last4) > 4 {
		last4 = last4[len(last4)-4:]
	}
	page := &LiquidityActivityPage{Items: []LiquidityActivityItem{}}
	for i, f := range rows {
		if i == limit {
			prev := rows[i-1]
			page.NextCursor = activityCursor{at: prev.CreatedAt, id: prev.ID}.encode()
			break
		}
		page.Items = append(page.Items, LiquidityActivityItem{ID: f.ID.String(), Type: "payout", CreatedAt: f.CreatedAt,
			AmountNgn: f.Amount, Status: f.Status, Reference: f.Reference, BankLast4: last4, SettledAt: f.SettledAt})
	}
	return page, nil
}

const (
	adminLPViewConcurrency = 8
	adminLPViewDeadline    = 5 * time.Second
)

// AdminLPViews fills the console's LP columns for many rows (review I7): at most
// adminLPViewConcurrency chain reads at once, all under one deadline, each
// through the 30 s cache. A read that misses the deadline reports itself
// unreadable; the page never waits on a slow RPC for long.
func (s *LiquidityService) AdminLPViews(ctx context.Context, lps []models.PaymentMerchant) map[uuid.UUID]*LPAdminView {
	ctx, cancel := context.WithTimeout(ctx, s.adminDeadline)
	defer cancel()
	out := make(map[uuid.UUID]*LPAdminView, len(lps))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, adminLPViewConcurrency)
	for i := range lps {
		lp := &lps[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				out[lp.ID] = &LPAdminView{OwedNgn: lp.AvailableNGN, BalanceError: "not read: the list deadline passed"}
				mu.Unlock()
				return
			}
			v := s.AdminLPView(ctx, lp)
			mu.Lock()
			out[lp.ID] = v
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// AdminLPView is the console's LP columns; an unreadable balance says so.
func (s *LiquidityService) AdminLPView(ctx context.Context, lp *models.PaymentMerchant) *LPAdminView {
	v := &LPAdminView{OwedNgn: lp.AvailableNGN}
	pos, err := s.stock.Position(ctx, lp, false)
	if err != nil {
		v.BalanceError = RedactAlertText(err.Error())
		return v
	}
	d := pos.Token.Decimals
	v.Available, v.Locked = BaseUnitsToDecimal(pos.Available, d), BaseUnitsToDecimal(pos.Locked, d)
	v.Stake, v.AboveMax = BaseUnitsToDecimal(pos.Stake, d), BaseUnitsToDecimal(pos.AboveMax, d)
	if ad, err := s.trade.LatestStockAd(ctx, lp, pos.Token.ChainID, pos.Token.Symbol); err == nil && ad != nil {
		v.VoucherDeadline, v.RenewDue = ad.VoucherDeadline, poolVoucherRenewDue(ad, s.now())
	}
	return v
}
