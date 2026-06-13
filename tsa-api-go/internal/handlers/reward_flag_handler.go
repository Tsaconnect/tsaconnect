package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// RewardFlagHandler serves the admin claim-eligibility endpoints (fee/TP
// overhaul SP7): listing/raising/clearing anti-sybil reward flags and reviewing
// a user's claim eligibility + reasons. All routes sit behind admin middleware.
type RewardFlagHandler struct {
	DB          *gorm.DB
	Eligibility *services.RewardEligibilityService
}

// NewRewardFlagHandler constructs a RewardFlagHandler.
func NewRewardFlagHandler(db *gorm.DB, elig *services.RewardEligibilityService) *RewardFlagHandler {
	return &RewardFlagHandler{DB: db, Eligibility: elig}
}

// ListRewardFlags handles GET /api/admin/reward-flags — lists flags, optionally
// filtered by ?status=active|cleared (default: all). Newest first.
func (h *RewardFlagHandler) ListRewardFlags(c *gin.Context) {
	q := h.DB.Model(&models.RewardFlag{})

	status := strings.ToLower(strings.TrimSpace(c.Query("status")))
	switch status {
	case "":
		// no filter
	case models.RewardFlagStatusActive, models.RewardFlagStatusCleared:
		q = q.Where("status = ?", status)
	default:
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid status filter (use 'active' or 'cleared')")
		return
	}

	if uid := strings.TrimSpace(c.Query("userId")); uid != "" {
		parsed, err := uuid.Parse(uid)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid userId")
			return
		}
		q = q.Where("user_id = ?", parsed)
	}

	var rows []models.RewardFlag
	if err := q.Order("created_at DESC").Find(&rows).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch reward flags")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Reward flags", rows)
}

// createRewardFlagRequest is the POST body for a manual admin flag.
type createRewardFlagRequest struct {
	UserID string `json:"userId" binding:"required"`
	Reason string `json:"reason" binding:"required"`
}

// CreateRewardFlag handles POST /api/admin/reward-flags — an admin manually
// flags a user. The flag is active immediately and gates the user's claim until
// cleared. Records the acting admin as FlaggedBy.
func (h *RewardFlagHandler) CreateRewardFlag(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req createRewardFlagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "userId and reason are required")
		return
	}

	userID, err := uuid.Parse(strings.TrimSpace(req.UserID))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid userId")
		return
	}

	// Verify the target user exists so an admin can't flag a typo'd id.
	var target models.User
	if err := h.DB.Select("id").First(&target, "id = ?", userID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.ErrorResponse(c, http.StatusNotFound, "User not found")
			return
		}
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to verify user")
		return
	}

	flag := models.RewardFlag{
		UserID:    userID,
		Reason:    strings.TrimSpace(req.Reason),
		Source:    models.RewardFlagSourceAdmin,
		Status:    models.RewardFlagStatusActive,
		FlaggedBy: &admin.ID,
	}
	if err := h.DB.Create(&flag).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create reward flag")
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Reward flag created", flag)
}

// clearRewardFlagRequest is the optional POST body for clearing a flag.
type clearRewardFlagRequest struct {
	Note string `json:"note"`
}

// ClearRewardFlag handles POST /api/admin/reward-flags/:id/clear — marks a flag
// cleared (it no longer gates eligibility; the row is retained for audit).
// Idempotent: clearing an already-cleared flag is a no-op success.
func (h *RewardFlagHandler) ClearRewardFlag(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Unauthorized")
		return
	}

	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid flag ID")
		return
	}

	var flag models.RewardFlag
	if err := h.DB.First(&flag, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.ErrorResponse(c, http.StatusNotFound, "Reward flag not found")
			return
		}
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load reward flag")
		return
	}

	var req clearRewardFlagRequest
	_ = c.ShouldBindJSON(&req)

	if flag.Status != models.RewardFlagStatusCleared {
		now := time.Now()
		updates := map[string]interface{}{
			"status":     models.RewardFlagStatusCleared,
			"cleared_by": admin.ID,
			"cleared_at": now,
		}
		if note := strings.TrimSpace(req.Note); note != "" {
			updates["reason"] = flag.Reason + " | cleared: " + note
		}
		if err := h.DB.Model(&flag).Updates(updates).Error; err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to clear reward flag")
			return
		}
		if err := h.DB.First(&flag, "id = ?", id).Error; err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to reload reward flag")
			return
		}
	}

	utils.SuccessResponse(c, http.StatusOK, "Reward flag cleared", flag)
}

// GetRewardEligibility handles GET /api/admin/reward-eligibility/:userId —
// returns a user's claim eligibility + the reasons they are (in)eligible, for
// admin review. This is the read side of the SP7 gate SP6 consumes at
// root-publish time.
func (h *RewardFlagHandler) GetRewardEligibility(c *gin.Context) {
	userID, err := uuid.Parse(strings.TrimSpace(c.Param("userId")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid userId")
		return
	}

	eligible, reasons, err := h.Eligibility.IsClaimEligible(c.Request.Context(), userID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to evaluate eligibility")
		return
	}
	if reasons == nil {
		reasons = []string{}
	}

	utils.SuccessResponse(c, http.StatusOK, "Claim eligibility", gin.H{
		"userId":   userID,
		"eligible": eligible,
		"reasons":  reasons,
	})
}
