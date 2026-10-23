package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// tp_admin_handler.go — the admin "System Fees & TradePoints" surface plus the
// TP leaderboard.
//
// This is a READ layer over data the fee engine already writes: fee_distributions
// holds the per-transaction carve (system / buyer cashback / upline cashback /
// provider / TP pool / global pool) and tp_earnings holds the eleven per-
// generation TP credits keyed by the same (source_type, source_id). Nothing here
// recomputes or mutates money.

type TPAdminHandler struct {
	DB           *gorm.DB
	SplitConfigs *services.FeeSplitConfigService
	Fees         *services.FeeDistributionService
	Program      *services.TPProgramConfigService
}

func NewTPAdminHandler(db *gorm.DB, splits *services.FeeSplitConfigService, fees *services.FeeDistributionService) *TPAdminHandler {
	return &TPAdminHandler{DB: db, SplitConfigs: splits, Fees: fees}
}

func (h *TPAdminHandler) requireAdmin(c *gin.Context) *models.User {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return nil
	}
	if !hasAdminPrivilege(c, user) {
		utils.ErrorResponse(c, http.StatusForbidden, "Access denied. Admin only.")
		return nil
	}
	return user
}

// ListFeeSplits — GET /api/admin/tp/splits.
// Returns the EFFECTIVE split per feature plus the compiled-in default, so the
// UI can show what's overridden and what a reset would restore. effectiveTpRate
// is the number the PM spec talks about: fee rate × TP share.
func (h *TPAdminHandler) ListFeeSplits(c *gin.Context) {
	if h.requireAdmin(c) == nil {
		return
	}
	defaults := services.DefaultFeeSplits()
	effective := h.SplitConfigs.Effective()

	// Fee rates live in fee_configs (BPS of the transaction); pair them up so
	// the admin sees the resulting section rate rather than two numbers to
	// multiply in their head.
	var feeCfgs []models.FeeConfig
	// direction IS NULL: only the direction-agnostic row is the feature's
	// headline rate. A per-leg row has no single "the fee rate" meaning, and
	// picking one by row order would make the displayed effective TP rate
	// depend on insertion order.
	h.DB.Where("active = ? AND chain_id IS NULL AND direction IS NULL", true).Find(&feeCfgs)
	bpsByFeature := map[string]int{}
	for _, fc := range feeCfgs {
		if _, seen := bpsByFeature[fc.Feature]; !seen {
			bpsByFeature[fc.Feature] = fc.BPS
		}
	}

	// TP is carved from what the platform keeps (System + TP slices), at the
	// program's total percentage — not from the TP slice on its own.
	programPct := h.Program.Current().TotalPct()

	rows := make([]gin.H, 0, len(effective))
	for feature, shares := range effective {
		def := defaults[feature]
		feeBps := bpsByFeature[feature]
		rows = append(rows, gin.H{
			"feature":       feature,
			"systemShare":   shares.System,
			"buyerShare":    shares.Buyer,
			"uplineShare":   shares.Upline,
			"providerShare": shares.Provider,
			"tpShare":       shares.TP,
			"isOverridden":  shares != def,
			"default": gin.H{
				"systemShare": def.System, "buyerShare": def.Buyer,
				"uplineShare": def.Upline, "providerShare": def.Provider, "tpShare": def.TP,
			},
			"feeBps": feeBps,
			// Percent of the TRANSACTION that becomes TP: the fee rate × what the
			// platform keeps × the program's TP percentage. A feature whose table
			// carries no TP (gas) mints none.
			"effectiveTpRatePct": effectiveTPRatePct(feeBps, shares, programPct),
			// Percent of the TRANSACTION paid back as cashback (buyer + upline +
			// provider shares of the fee).
			"cashbackRatePct": (float64(feeBps) / 10000.0) * (shares.Buyer + shares.Upline + shares.Provider) * 100,
		})
	}
	utils.SuccessResponse(c, http.StatusOK, "Fee splits", gin.H{"splits": rows})
}

func effectiveTPRatePct(feeBps int, shares services.FeeShares, programPct float64) float64 {
	if shares.TP <= 0 {
		return 0
	}
	return (float64(feeBps) / 10000.0) * (shares.System + shares.TP) * programPct * 100
}

// UpsertFeeSplit — PUT /api/admin/tp/splits/:feature.
func (h *TPAdminHandler) UpsertFeeSplit(c *gin.Context) {
	admin := h.requireAdmin(c)
	if admin == nil {
		return
	}
	feature := strings.ToLower(strings.TrimSpace(c.Param("feature")))
	if feature == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "feature is required")
		return
	}
	// Only features the engine knows about — a typo would otherwise create a
	// row that silently never applies.
	if _, known := services.DefaultFeeSplits()[feature]; !known {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unknown fee feature")
		return
	}
	var in struct {
		SystemShare   float64 `json:"systemShare"`
		BuyerShare    float64 `json:"buyerShare"`
		UplineShare   float64 `json:"uplineShare"`
		ProviderShare float64 `json:"providerShare"`
		TPShare       float64 `json:"tpShare"`
		Active        *bool   `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	cfg := models.FeeSplitConfig{
		Feature: feature, SystemShare: in.SystemShare, BuyerShare: in.BuyerShare,
		UplineShare: in.UplineShare, ProviderShare: in.ProviderShare, TPShare: in.TPShare,
		Active: true, UpdatedBy: &admin.ID,
	}
	if in.Active != nil {
		cfg.Active = *in.Active
	}
	saved, err := h.SplitConfigs.Upsert(cfg)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Fee split updated", saved)
}

// ListFeeDistributions — GET /api/admin/tp/distributions?feature=&userId=&from=&to=
// The tracker table: one row per settled fee with its carve.
func (h *TPAdminHandler) ListFeeDistributions(c *gin.Context) {
	if h.requireAdmin(c) == nil {
		return
	}
	q := h.DB.WithContext(c.Request.Context()).Model(&models.FeeDistribution{}).Order("created_at DESC")

	if f := strings.ToLower(strings.TrimSpace(c.Query("feature"))); f != "" {
		q = q.Where("feature = ?", f)
	}
	if raw := strings.TrimSpace(c.Query("userId")); raw != "" {
		uid, err := uuid.Parse(raw)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid userId")
			return
		}
		q = q.Where("user_id = ?", uid)
	}
	if from, ok := parseDayParam(c.Query("from")); ok {
		q = q.Where("created_at >= ?", from)
	}
	if to, ok := parseDayParam(c.Query("to")); ok {
		// Inclusive of the whole end day.
		q = q.Where("created_at < ?", to.Add(24*time.Hour))
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load distributions")
		return
	}
	limit := clamp(atoiDefault(c.Query("limit"), defaultPageSize), 1, maxPageSize)
	offset := clamp(atoiDefault(c.Query("offset"), 0), 0, 1<<30)

	var rows []models.FeeDistribution
	if err := q.Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load distributions")
		return
	}

	// Resolve the transacting user's username per row — the tracker's
	// tap-through starts here. Batched to avoid a query per row.
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.UserID)
	}
	names := h.usernamesByID(c, ids)

	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		out = append(out, gin.H{
			"distribution": r,
			"username":     names[r.UserID],
		})
	}
	utils.SuccessResponse(c, http.StatusOK, "Fee distributions", gin.H{
		"distributions": out, "total": total, "limit": limit, "offset": offset,
	})
}

// GetFeeSummary — GET /api/admin/tp/fee-summary?from=&to=
//
// What reached the system wallet, and from where. Every fee row already carries
// the section that produced it (feature, e.g. "p2p") and the originating record
// (source_type/source_id), so the summary is a grouped read of the same ledger
// the tracker lists row-by-row: one line per (section, source type) with the
// total fee charged and the system's share of it, plus a grand total.
func (h *TPAdminHandler) GetFeeSummary(c *gin.Context) {
	if h.requireAdmin(c) == nil {
		return
	}
	q := h.DB.WithContext(c.Request.Context()).Model(&models.FeeDistribution{})
	if from, ok := parseDayParam(c.Query("from")); ok {
		q = q.Where("created_at >= ?", from)
	}
	if to, ok := parseDayParam(c.Query("to")); ok {
		q = q.Where("created_at < ?", to.Add(24*time.Hour))
	}

	type summaryRow struct {
		Feature     string  `json:"feature"`
		SourceType  string  `json:"sourceType"`
		Count       int64   `json:"count"`
		FeeUSD      float64 `json:"feeUsd"`
		SystemUSD   float64 `json:"systemUsd"`
		TPPoolUSD   float64 `json:"tpPoolUsd"`
		CashbackUSD float64 `json:"cashbackUsd"`
		// LastAt is when this section last settled a fee — the quickest check
		// that a section is live (and paying cashback) on the marketplace.
		// Scanned as text: MAX() comes back untyped on some drivers, and the
		// console parses it. Empty only for a section with no rows.
		LastAt string `json:"lastAt"`
	}
	var rows []summaryRow
	if err := q.
		Select(`feature,
			source_type,
			COUNT(*) AS count,
			COALESCE(SUM(fee_usd), 0) AS fee_usd,
			COALESCE(SUM(system_usd), 0) AS system_usd,
			COALESCE(SUM(tp_pool_usd), 0) AS tp_pool_usd,
			COALESCE(SUM(buyer_cashback_usd + upline_cashback_usd + provider_cashback_usd), 0) AS cashback_usd,
			MAX(created_at) AS last_at`).
		Group("feature, source_type").
		Order("system_usd DESC").
		Scan(&rows).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to summarise fees")
		return
	}

	var totalFee, totalSystem, totalTP, totalCashback float64
	var totalCount int64
	for _, r := range rows {
		totalFee += r.FeeUSD
		totalSystem += r.SystemUSD
		totalTP += r.TPPoolUSD
		totalCashback += r.CashbackUSD
		totalCount += r.Count
	}

	utils.SuccessResponse(c, http.StatusOK, "Fee summary", gin.H{
		"rows": rows,
		"totals": gin.H{
			"count": totalCount, "feeUsd": totalFee, "systemUsd": totalSystem,
			"tpPoolUsd": totalTP, "cashbackUsd": totalCashback,
		},
	})
}

// GetFeeDistributionDetail — GET /api/admin/tp/distributions/:sourceType/:sourceId
// The tap-through: the carve plus every TP and cashback recipient, with usernames.
func (h *TPAdminHandler) GetFeeDistributionDetail(c *gin.Context) {
	if h.requireAdmin(c) == nil {
		return
	}
	sourceType := strings.TrimSpace(c.Param("sourceType"))
	sourceID, err := uuid.Parse(c.Param("sourceId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid sourceId")
		return
	}
	ctx := c.Request.Context()

	var dist models.FeeDistribution
	if err := h.DB.WithContext(ctx).
		First(&dist, "source_type = ? AND source_id = ?", sourceType, sourceID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Distribution not found")
		return
	}

	var tpRows []models.TPEarning
	h.DB.WithContext(ctx).Where("source_type = ? AND source_id = ?", sourceType, sourceID).
		Order("generation ASC").Find(&tpRows)

	var cbRows []models.CashbackEarning
	h.DB.WithContext(ctx).Where("source_type = ? AND source_id = ?", sourceType, sourceID).
		Find(&cbRows)

	ids := []uuid.UUID{dist.UserID}
	for _, r := range tpRows {
		ids = append(ids, r.UserID)
	}
	for _, r := range cbRows {
		ids = append(ids, r.UserID)
	}
	names := h.usernamesByID(c, ids)

	tp := make([]gin.H, 0, len(tpRows))
	for _, r := range tpRows {
		tp = append(tp, gin.H{
			"userId": r.UserID, "username": names[r.UserID],
			"generation": r.Generation, "tpEarned": r.TPEarned,
		})
	}
	cb := make([]gin.H, 0, len(cbRows))
	for _, r := range cbRows {
		cb = append(cb, gin.H{
			"userId": r.UserID, "username": names[r.UserID],
			"amountUsd": r.AmountUSD, "token": r.Token,
		})
	}

	utils.SuccessResponse(c, http.StatusOK, "Distribution detail", gin.H{
		"distribution": dist,
		"username":     names[dist.UserID],
		"tpRecipients": tp,
		"cashback":     cb,
	})
}

// GetGlobalPool — GET /api/admin/tp/global-pool. Already computed by the
// engine; it just had no route.
func (h *TPAdminHandler) GetGlobalPool(c *gin.Context) {
	if h.requireAdmin(c) == nil {
		return
	}
	pool, err := h.Fees.GlobalPool(c.Request.Context())
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load the global pool")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Global TP pool", pool)
}

// tpProgramView is the admin payload: the program in effect plus the pool state
// and next payout, so the console shows what will happen on the 24th.
type tpProgramView struct {
	models.TPProgramConfig
	NextPayoutAt time.Time `json:"nextPayoutAt"`
	PoolTP       float64   `json:"poolTp"`
	PoolUSD      float64   `json:"poolUsd"`
}

func (h *TPAdminHandler) programView(c *gin.Context) tpProgramView {
	cfg := h.Program.Current()
	next := services.LastPayoutInstant(time.Now(), cfg.PayoutDay).AddDate(0, 1, 0)
	view := tpProgramView{TPProgramConfig: cfg, NextPayoutAt: next}
	if pool, err := h.Fees.GlobalPool(c.Request.Context()); err == nil {
		view.PoolTP = pool.AccruedTP - pool.IndexedTP
		view.PoolUSD = view.PoolTP / h.Fees.TPPerUSD()
	}
	return view
}

// GetTPProgram — GET /api/admin/tp/program.
func (h *TPAdminHandler) GetTPProgram(c *gin.Context) {
	if h.requireAdmin(c) == nil {
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "TP program", h.programView(c))
}

// UpdateTPProgram — PUT /api/admin/tp/program. Percentages are fractions of the
// system's share (0.05 = 5%).
func (h *TPAdminHandler) UpdateTPProgram(c *gin.Context) {
	admin := h.requireAdmin(c)
	if admin == nil {
		return
	}
	// Pointers: a field the caller leaves out keeps its current value rather than
	// silently saving 0 (which would switch the pool or the zero-fee reward off).
	var in struct {
		UserPct   *float64 `json:"userPct"`
		UplinePct *float64 `json:"uplinePct"`
		PoolPct   *float64 `json:"poolPct"`
		ZeroFeeTP *float64 `json:"zeroFeeTp"`
		PayoutDay *int     `json:"payoutDay"`

		SignupBonusTP     *float64 `json:"signupBonusTp"`
		OnboardingBonusTP *float64 `json:"onboardingBonusTp"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	next := h.Program.Current()
	if in.UserPct != nil {
		next.UserPct = *in.UserPct
	}
	if in.UplinePct != nil {
		next.UplinePct = *in.UplinePct
	}
	if in.PoolPct != nil {
		next.PoolPct = *in.PoolPct
	}
	if in.ZeroFeeTP != nil {
		next.ZeroFeeTP = *in.ZeroFeeTP
	}
	if in.PayoutDay != nil {
		next.PayoutDay = *in.PayoutDay
	}
	if in.SignupBonusTP != nil {
		next.SignupBonusTP = *in.SignupBonusTP
	}
	if in.OnboardingBonusTP != nil {
		next.OnboardingBonusTP = *in.OnboardingBonusTP
	}
	next.UpdatedBy = &admin.ID
	if _, err := h.Program.Save(next); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "TP program updated", h.programView(c))
}

// GetTPLeaderboard — GET /api/tp/leaderboard?limit= (auth).
// Ranks by the maintained users.tp_balance, so it's an indexed ORDER BY rather
// than an aggregate over tp_earnings.
func (h *TPAdminHandler) GetTPLeaderboard(c *gin.Context) {
	me := getUserFromContext(c)
	if me == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	limit := clamp(atoiDefault(c.Query("limit"), 50), 1, 100)

	var top []models.User
	if err := h.DB.WithContext(c.Request.Context()).
		Select("id", "username", "name", "tp_balance").
		Where("deleted_at IS NULL AND account_status = ?", models.AccountStatusActive).
		Order("tp_balance DESC, username ASC").
		Limit(limit).Find(&top).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load the leaderboard")
		return
	}

	entries := make([]gin.H, 0, len(top))
	for i, u := range top {
		entries = append(entries, gin.H{
			"rank": i + 1, "userId": u.ID, "username": u.Username,
			"name": u.Name, "tpBalance": u.TPBalance,
			"level": models.LevelFor(u.TPBalance),
			"isMe":  u.ID == me.ID,
		})
	}

	// The caller's own standing, even when outside the top N — a leaderboard
	// you can't find yourself on isn't much of a motivator.
	var ahead int64
	h.DB.WithContext(c.Request.Context()).Model(&models.User{}).
		Where("deleted_at IS NULL AND account_status = ? AND tp_balance > ?",
			models.AccountStatusActive, me.TPBalance).
		Count(&ahead)

	utils.SuccessResponse(c, http.StatusOK, "TP leaderboard", gin.H{
		"entries": entries,
		"me": gin.H{
			"rank": ahead + 1, "userId": me.ID, "username": me.Username,
			"tpBalance": me.TPBalance, "level": models.LevelFor(me.TPBalance),
		},
	})
}

// usernamesByID batches the id→username lookup used by the tracker rows.
func (h *TPAdminHandler) usernamesByID(c *gin.Context, ids []uuid.UUID) map[uuid.UUID]string {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out
	}
	var users []models.User
	h.DB.WithContext(c.Request.Context()).Select("id", "username").
		Where("id IN ?", ids).Find(&users)
	for _, u := range users {
		out[u.ID] = u.Username
	}
	return out
}

// parseDayParam accepts YYYY-MM-DD; anything else is treated as absent rather
// than erroring, so a stale bookmark doesn't 400 the whole tracker.
func parseDayParam(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
