package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// settleFeeEarnings is the single fee-settled chokepoint for the fiat/on-chain
// fee call sites (checkout, service-contact, swap, wallet-send). It hands the
// realized fee to the FeeDistributionService async (EnqueueFeeSettled) — the
// 50/10/10/30 split + route-and-continue TP walk + global-pool accrual. The
// (SourceType, SourceID) pair is the engine's idempotency key, mirroring the
// existing TPDistributedAt guard.
//
// It is fire-and-forget + idempotent, so it returns nil. svc is nil-safe (a
// no-op) so callers that haven't wired the engine still compile.
func settleFeeEarnings(svc *services.FeeDistributionService, ev services.FeeEvent) error {
	if svc != nil {
		svc.EnqueueFeeSettled(ev)
	}
	return nil
}

// feeAccrualInTx is the LEAK-PROOF settlement seam (design §8, B5/I7). It runs
// the full fee settlement (guard + split + TP/cashback) INSIDE the caller's
// transaction so a balance deduction can never commit without the accrual being
// recorded — if settlement errors, the caller's whole tx (the deduction
// included) rolls back. The caller fires MarkRecipientsDirty AFTER its tx commits.
//
// svc is nil-safe (a no-op) so a caller that hasn't wired the engine still
// compiles. The upline graph is read by the caller OUTSIDE the tx (the
// recursive-CTE read must not contend with the write tx on a single-connection
// pool); byGen is passed in.
func feeAccrualInTx(svc *services.FeeDistributionService, tx *gorm.DB, ev services.FeeEvent, byGen map[int]services.Ancestor, recipients map[uuid.UUID]struct{}) error {
	if svc == nil {
		return nil
	}
	return svc.SettleInTx(tx, ev, byGen, recipients)
}

// GetTPBalance handles GET /api/users/tp-balance (auth required).
func (h *Handlers) GetTPBalance(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"tpBalance": user.TPBalance,
		},
	})
}

// GetTPEarnings handles GET /api/users/tp-earnings?page=1&limit=20 (auth required).
func (h *Handlers) GetTPEarnings(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	var total int64
	config.DB.Model(&models.TPEarning{}).Where("user_id = ?", user.ID).Count(&total)

	type earningWithName struct {
		models.TPEarning
		SourceUserName string `json:"sourceUserName"`
	}

	var earnings []earningWithName
	config.DB.Model(&models.TPEarning{}).
		Select("tp_earnings.*, users.name as source_user_name").
		Joins("LEFT JOIN users ON users.id = tp_earnings.source_user_id").
		Where("tp_earnings.user_id = ?", user.ID).
		Order("tp_earnings.created_at DESC").
		Offset(offset).Limit(limit).
		Find(&earnings)

	if earnings == nil {
		earnings = []earningWithName{}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"earnings": earnings,
			"total":    total,
			"page":     page,
			"limit":    limit,
		},
	})
}

// GetReferralsWithTP handles GET /api/users/referrals (auth required).
func (h *Handlers) GetReferralsWithTP(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	type referralWithTP struct {
		ID                 uuid.UUID `json:"id"`
		Name               string    `json:"name"`
		Username           string    `json:"username"`
		CreatedAt          time.Time `json:"createdAt"`
		VerificationStatus string    `json:"verificationStatus"`
		TPContributed      float64   `json:"tpContributed"`
	}

	var referrals []referralWithTP
	config.DB.Model(&models.User{}).
		Select("users.id, users.name, users.username, users.created_at, users.verification_status, COALESCE(SUM(tp_earnings.tp_earned), 0) as tp_contributed").
		Joins("LEFT JOIN tp_earnings ON tp_earnings.source_user_id = users.id AND tp_earnings.user_id = ?", user.ID).
		Where("users.referred_by = ? AND users.deleted_at IS NULL", user.ID).
		Group("users.id, users.name, users.username, users.created_at, users.verification_status").
		Order("users.created_at DESC").
		Find(&referrals)

	if referrals == nil {
		referrals = []referralWithTP{}
	}

	verifiedCount := 0
	pendingCount := 0
	totalTP := 0.0
	for _, r := range referrals {
		if r.VerificationStatus == models.VerificationStatusVerified {
			verifiedCount++
		}
		if r.VerificationStatus == models.VerificationStatusPending || r.VerificationStatus == models.VerificationStatusInReview {
			pendingCount++
		}
		totalTP += r.TPContributed
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"totalReferrals":       len(referrals),
			"verifiedReferrals":    verifiedCount,
			"pendingReferrals":     pendingCount,
			"totalTPFromReferrals": totalTP,
			"referrals":            referrals,
		},
	})
}

// GetCashbackBalance handles GET /api/users/cashback-balance (auth required).
func (h *Handlers) GetCashbackBalance(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"cashbackBalance": user.CashbackBalance,
		},
	})
}

// GetCashbackEarnings handles GET /api/users/cashback-earnings?page=1&limit=20 (auth required).
func (h *Handlers) GetCashbackEarnings(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	var total int64
	config.DB.Model(&models.CashbackEarning{}).Where("user_id = ?", user.ID).Count(&total)

	type earningWithName struct {
		models.CashbackEarning
		SourceUserName string `json:"sourceUserName"`
	}

	var earnings []earningWithName
	config.DB.Model(&models.CashbackEarning{}).
		Select("cashback_earnings.*, users.name as source_user_name").
		Joins("LEFT JOIN users ON users.id = cashback_earnings.source_user_id").
		Where("cashback_earnings.user_id = ?", user.ID).
		Order("cashback_earnings.created_at DESC").
		Offset(offset).Limit(limit).
		Find(&earnings)

	if earnings == nil {
		earnings = []earningWithName{}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"earnings": earnings,
			"total":    total,
			"page":     page,
			"limit":    limit,
		},
	})
}

// GetCashbackSummary handles GET /api/users/cashback-summary (auth required).
// Returns the user's cashback total plus a per-activity breakdown (grouped by
// SourceType) so the app can render "your earnings for each activity". The
// SourceType → friendly-label mapping is done client-side; here we just
// aggregate the ledger so the figures are exact (not page-limited).
func (h *Handlers) GetCashbackSummary(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Unauthorized"})
		return
	}

	type activityRow struct {
		SourceType string  `json:"sourceType"`
		TotalUsd   float64 `json:"totalUsd"`
		Count      int64   `json:"count"`
	}

	var byActivity []activityRow
	config.DB.Model(&models.CashbackEarning{}).
		Select("source_type, COALESCE(SUM(amount_usd), 0) AS total_usd, COUNT(*) AS count").
		Where("user_id = ?", user.ID).
		Group("source_type").
		Order("total_usd DESC").
		Scan(&byActivity)

	if byActivity == nil {
		byActivity = []activityRow{}
	}

	var totalEarned float64
	for _, a := range byActivity {
		totalEarned += a.TotalUsd
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"cashbackBalance": user.CashbackBalance,
			"totalEarnedUsd":  totalEarned,
			"byActivity":      byActivity,
		},
	})
}

// RecordCashbackEarning creates a cashback earning record and updates user balance.
// Called by handlers when on-chain cashback is paid.
func RecordCashbackEarning(db *gorm.DB, userID uuid.UUID, sourceUserID uuid.UUID, sourceType string, sourceID uuid.UUID, amountUSD float64, txHash string) error {
	if amountUSD <= 0 {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		earning := models.CashbackEarning{
			ID:           uuid.New(),
			UserID:       userID,
			SourceUserID: sourceUserID,
			SourceType:   sourceType,
			SourceID:     sourceID,
			AmountUSD:    amountUSD,
			TxHash:       txHash,
		}
		if err := tx.Create(&earning).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.User{}).Where("id = ?", userID).
			Update("cashback_balance", gorm.Expr("cashback_balance + ?", amountUSD)).Error; err != nil {
			return err
		}
		return nil
	})
}
