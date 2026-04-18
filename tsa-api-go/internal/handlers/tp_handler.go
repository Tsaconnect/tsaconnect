package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// GenerationPercentages defines the TP distribution % for each generation (0=self, 1-10=upline).
var GenerationPercentages = []float64{
	0.354, // gen 0: self
	0.177, // gen 1: direct referrer
	0.142, // gen 2
	0.106, // gen 3
	0.071, // gen 4
	0.057, // gen 5
	0.042, // gen 6
	0.028, // gen 7
	0.014, // gen 8
	0.007, // gen 9
	0.004, // gen 10
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
