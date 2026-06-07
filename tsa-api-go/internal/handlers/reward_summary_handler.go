package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// RewardSummaryHandler serves the SP8 user-facing reward endpoints the app's TP
// screen needs:
//   - GET /api/rewards/summary      — the full TP/reward dashboard payload.
//   - GET /api/rewards/prepare-claim — the UNSIGNED claim(account, cumulative,
//     proof) tx for the app to sign + broadcast (convenience over the SP6
//     /rewards/claim-data, which stays the canonical claim source).
//
// Both are nil-safe: a dormant reward stack (summary service / merkle service
// unset) returns a clear, non-crashing response so dev environments keep booting.
type RewardSummaryHandler struct {
	summary *services.RewardSummaryService
	merkle  *services.RewardMerkleService
}

// NewRewardSummaryHandler constructs the handler. Either dependency may be nil
// (feature partially dormant) — the endpoints then surface a clear 503 / empty
// response rather than panicking.
func NewRewardSummaryHandler(summary *services.RewardSummaryService, merkle *services.RewardMerkleService) *RewardSummaryHandler {
	return &RewardSummaryHandler{summary: summary, merkle: merkle}
}

// GetRewardSummary handles GET /api/rewards/summary (auth). Returns the full
// reward dashboard for the authed user: tp_balance (refreshed to include the
// pending global share), claimable/claimed MCGP, current level + progress, the
// 15-level ladder, per-generation earnings, the global-pool share, and prizes
// due. See services.RewardSummary for the exact shape.
func (h *RewardSummaryHandler) GetRewardSummary(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if h.summary == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Reward summary not available")
		return
	}

	data, err := h.summary.Summary(c.Request.Context(), user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to build reward summary")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Reward summary", data)
}

// PrepareClaim handles GET /api/rewards/prepare-claim (auth). Returns the
// UNSIGNED claim(account, cumulativeAmount, proof) tx for the authed user, built
// from the latest published epoch (the same cumulative+proof as
// /rewards/claim-data, pre-encoded into the claim calldata). The app signs +
// broadcasts; the backend never signs. unsignedTx is null when the user has
// nothing claimable on the live root (the app shows "nothing to claim"). The
// canonical claim source remains GET /rewards/claim-data — this is a convenience
// matching the codebase's backend-prepared-unsigned-tx pattern (P2P
// PrepareDeposit, SP6 PrepareSetMerkleRoot).
func (h *RewardSummaryHandler) PrepareClaim(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if h.merkle == nil || !h.merkle.IsConfigured() {
		// Feature dormant — respond cleanly with "nothing to claim".
		utils.SuccessResponse(c, http.StatusOK, "Prepare claim", gin.H{
			"claimData":  services.ClaimData{Included: false},
			"unsignedTx": nil,
		})
		return
	}

	prepared, err := h.merkle.PrepareClaimForUser(c.Request.Context(), user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare claim")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Prepare claim", gin.H{
		"claimData":  prepared.Claimable,
		"unsignedTx": prepared.Tx,
	})
}
