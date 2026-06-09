package handlers

import (
	"context"
	"fmt"
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

// GenerationPercentages defines the TP distribution % for each generation
// (0=self, 1-10=upline). The canonical ladder now lives in the models package
// (models.GenerationPercentages) so the legacy engine here and the v2
// FeeDistributionService share one source of truth without an import cycle.
// This alias keeps every existing reference (handlers.GenerationPercentages)
// unchanged.
var GenerationPercentages = models.GenerationPercentages

// settleFeeEarnings is the single fee-settled chokepoint for the fiat/on-chain
// fee call sites (checkout, service-contact, swap, wallet-send). It centralizes
// the v1↔v2 branch so each site has exactly ONE call:
//
//   - When the fee/TP v2 model is ON (cfg.IsFeeModelV2Enabled()) AND the v2
//     engine is wired, it hands the realized fee to the FeeDistributionService
//     async (EnqueueFeeSettled) — the new 50/10/10/30 split + route-and-continue
//     TP walk + global-pool accrual. The (SourceType, SourceID) pair is the
//     engine's idempotency key, mirroring the existing TPDistributedAt guard.
//   - Otherwise it preserves the EXACT legacy behavior: DistributeTPEarnings
//     with the same (userID, sourceType, sourceID, feeUSD). No behavior change
//     when the flag is off.
//
// It returns the legacy DistributeTPEarnings error only on the legacy path
// (callers already log it); the v2 path is fire-and-forget + idempotent, so it
// returns nil. svc/cfg are nil-safe so callers that haven't wired the engine
// still compile and fall through to legacy.
func settleFeeEarnings(svc *services.FeeDistributionService, cfg *config.Config, db *gorm.DB, ev services.FeeEvent) error {
	if cfg.IsFeeModelV2Enabled() && svc != nil {
		svc.EnqueueFeeSettled(ev)
		return nil
	}
	return DistributeTPEarnings(db, ev.UserID, ev.SourceType, ev.SourceID, ev.FeeUSD)
}

// feeAccrualInTx is the LEAK-PROOF v2 settlement seam (design §8, B5/I7). It
// runs the full fee settlement (guard + split + TP/cashback) INSIDE the caller's
// transaction so a balance deduction can never commit without the accrual being
// recorded — if settlement errors, the caller's whole tx (the deduction
// included) rolls back. Returns (dirty-recipients, error); the caller fires
// MarkRecipientsDirty AFTER its tx commits.
//
// FLAG-GATED: when cfg.IsFeeModelV2Enabled() is OFF (or svc is nil) this is a
// pure no-op — it writes NOTHING and returns (nil, nil), so the legacy path is
// byte-for-byte preserved. The CALLER chooses whether to run the legacy
// DistributeTPEarnings on the flag-off path (the on-chain send path historically
// did not, so withdrawal preserves that — no FeeEvent, no TP — when off).
//
// The upline graph is read here OUTSIDE the tx the caller will open (the
// recursive-CTE read must not contend with the write tx on a single-connection
// pool). Callers therefore call this to GET byGen + a settle closure is not how
// it works; instead this resolves uplines then returns a closure to run inside
// the tx. To keep call sites simple it instead exposes the two pieces the
// withdrawal handler wires explicitly.
func feeAccrualInTx(ctx context.Context, svc *services.FeeDistributionService, cfg *config.Config, tx *gorm.DB, ev services.FeeEvent, byGen map[int]services.Ancestor, recipients map[uuid.UUID]struct{}) error {
	if cfg == nil || !cfg.IsFeeModelV2Enabled() || svc == nil {
		return nil // flag OFF → byte-for-byte legacy (no FeeEvent, no accrual)
	}
	return svc.SettleInTx(tx, ev, byGen, recipients)
}

// DistributeTPEarnings distributes TP to the source user and up to 10 generations of upline.
// systemFeeUSD is the total system fee in USD from the transaction.
func DistributeTPEarnings(db *gorm.DB, sourceUserID uuid.UUID, sourceType string, sourceID uuid.UUID, systemFeeUSD float64) error {
	if systemFeeUSD <= 0 {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		currentUserID := sourceUserID

		for gen := 0; gen < len(GenerationPercentages); gen++ {
			if gen > 0 {
				var prevUser models.User
				if err := tx.Select("referred_by").First(&prevUser, "id = ?", currentUserID).Error; err != nil {
					return fmt.Errorf("load user %s at gen %d: %w", currentUserID, gen, err)
				}
				if prevUser.ReferredBy == nil {
					break
				}

				var referrer models.User
				if err := tx.Select("id, account_status").First(&referrer, "id = ?", *prevUser.ReferredBy).Error; err != nil {
					return fmt.Errorf("load referrer %s at gen %d: %w", *prevUser.ReferredBy, gen, err)
				}
				if referrer.AccountStatus != models.AccountStatusActive {
					break
				}

				currentUserID = referrer.ID
			}

			pct := GenerationPercentages[gen]
			tpEarned := systemFeeUSD * pct

			earning := models.TPEarning{
				ID:           uuid.New(),
				UserID:       currentUserID,
				SourceUserID: sourceUserID,
				SourceType:   sourceType,
				SourceID:     sourceID,
				Generation:   gen,
				FeeAmountUSD: systemFeeUSD,
				Percentage:   pct,
				TPEarned:     tpEarned,
				CreatedAt:    time.Now(),
			}
			if err := tx.Create(&earning).Error; err != nil {
				return err
			}

			if err := tx.Model(&models.User{}).Where("id = ?", currentUserID).
				Update("tp_balance", gorm.Expr("tp_balance + ?", tpEarned)).Error; err != nil {
				return err
			}
		}

		return nil
	})
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
