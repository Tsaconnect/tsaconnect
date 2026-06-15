package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// RewardPayoutHandler serves the admin reward-payout-config endpoints: the
// admin-tunable mapping of (reward kind, payment token) → (payout token, chain).
// PM: payout token must be flexible, not hardcoded.
type RewardPayoutHandler struct {
	Service *services.RewardPayoutConfigService
}

// NewRewardPayoutHandler creates a RewardPayoutHandler.
func NewRewardPayoutHandler(svc *services.RewardPayoutConfigService) *RewardPayoutHandler {
	return &RewardPayoutHandler{Service: svc}
}

// ListRewardPayouts handles GET /api/admin/reward-payouts — the full mapping.
func (h *RewardPayoutHandler) ListRewardPayouts(c *gin.Context) {
	if h == nil || h.Service == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Reward payout config not available")
		return
	}
	rows, err := h.Service.List()
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load reward payout config")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Reward payout config", rows)
}

type upsertRewardPayoutRequest struct {
	RewardKind   string `json:"rewardKind"`
	PaymentToken string `json:"paymentToken"`
	PayoutToken  string `json:"payoutToken"`
	Chain        string `json:"chain"`
	Active       *bool  `json:"active"`
}

// UpsertRewardPayout handles PUT /api/admin/reward-payouts — create/update one
// (rewardKind, paymentToken) → (payoutToken, chain) mapping.
func (h *RewardPayoutHandler) UpsertRewardPayout(c *gin.Context) {
	if h == nil || h.Service == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Reward payout config not available")
		return
	}
	var req upsertRewardPayoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	if strings.TrimSpace(req.RewardKind) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "rewardKind is required")
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	row, err := h.Service.Upsert(req.RewardKind, req.PaymentToken, req.PayoutToken, req.Chain, active)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Reward payout config updated", row)
}
