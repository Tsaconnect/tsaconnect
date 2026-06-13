package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// RewardDistributionHandler serves the SP6 root-publisher endpoints:
//   - admin: build a new cumulative epoch from current eligible claimables +
//     return the new root + an UNSIGNED setMerkleRoot tx (admin-signed, no
//     backend key — mirrors the P2P prepare-unsigned-tx pattern); list history.
//   - user: fetch the authed user's proof + cumulative amount for the latest
//     epoch so the app can build the claim(account, cumulative, proof) tx.
type RewardDistributionHandler struct {
	merkle *services.RewardMerkleService
}

// NewRewardDistributionHandler constructs the handler. merkle may be a service
// with an empty distributor address (feature dormant) — the endpoints then
// surface clear "not configured" / empty responses.
func NewRewardDistributionHandler(merkle *services.RewardMerkleService) *RewardDistributionHandler {
	return &RewardDistributionHandler{merkle: merkle}
}

// PublishDistribution handles POST /api/admin/reward-distributions/publish.
// Builds a NEW cumulative epoch from the current eligible claimables, persists
// it, and returns the new root + an UNSIGNED setMerkleRoot(root) tx for the
// admin to sign + broadcast. The backend never signs or broadcasts.
func (h *RewardDistributionHandler) PublishDistribution(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if h.merkle == nil || !h.merkle.IsConfigured() {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Reward distributor not configured")
		return
	}

	adminID := admin.ID
	build, err := h.merkle.BuildDistribution(c.Request.Context(), &adminID)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrRewardDistributorNotConfigured):
			utils.ErrorResponse(c, http.StatusServiceUnavailable, "Reward distributor not configured")
		case errors.Is(err, services.ErrNoEligibleClaimants):
			utils.ErrorResponse(c, http.StatusUnprocessableEntity, "No eligible claimants with a positive claimable balance")
		default:
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to build distribution")
		}
		return
	}

	// Build the unsigned setMerkleRoot tx for the admin to sign + broadcast.
	calldata, err := h.merkle.PrepareSetMerkleRoot(build.Distribution.MerkleRoot)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare setMerkleRoot tx")
		return
	}
	unsigned := blockchain.UnsignedTx{
		To:      h.merkle.DistributorAddress(),
		Value:   "0",
		Data:    common.Bytes2Hex(calldata),
		ChainID: strconv.FormatInt(h.merkle.ChainID(), 10),
	}
	unsignedJSON, _ := json.Marshal(unsigned)

	utils.SuccessResponse(c, http.StatusCreated, "Distribution built", gin.H{
		"distribution":       build.Distribution,
		"leafCount":          build.Distribution.LeafCount,
		"merkleRoot":         build.Distribution.MerkleRoot,
		"epoch":              build.Distribution.Epoch,
		"distributorAddress": h.merkle.DistributorAddress(),
		"chainId":            h.merkle.ChainID(),
		"unsignedTx":         unsignedTxJSON(unsignedJSON),
	})
}

// ListDistributions handles GET /api/admin/reward-distributions — published
// epoch history, newest first. Optional ?limit (default 100, max 500).
func (h *RewardDistributionHandler) ListDistributions(c *gin.Context) {
	if h.merkle == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Reward distributor not configured")
		return
	}
	limit := 100
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	rows, err := h.merkle.ListDistributions(c.Request.Context(), limit)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to list distributions")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Reward distributions", rows)
}

// confirmPublishRequest is the POST body recording an on-chain setMerkleRoot
// confirmation (the admin signed + broadcast the tx).
type confirmPublishRequest struct {
	Epoch  int64  `json:"epoch" binding:"required"`
	TxHash string `json:"txHash" binding:"required"`
}

// ConfirmPublish handles POST /api/admin/reward-distributions/confirm — records
// that the admin confirmed the setMerkleRoot tx on-chain (status → published).
//
// FINDING #3: the admin-supplied txHash is no longer trusted blindly. When an
// on-chain verifier is wired, MarkPublished requires the tx to have mined
// successfully AND the distributor's live root to equal this epoch's expected
// root before publishing; a wrong/early/failed hash is rejected (422) and the
// epoch stays unpublished, so a bad confirmation can't strand the epoch's claims.
func (h *RewardDistributionHandler) ConfirmPublish(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if h.merkle == nil || !h.merkle.IsConfigured() {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Reward distributor not configured")
		return
	}
	var req confirmPublishRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "epoch and txHash are required")
		return
	}
	if err := h.merkle.MarkPublished(c.Request.Context(), req.Epoch, req.TxHash); err != nil {
		switch {
		case errors.Is(err, services.ErrPublishVerificationFailed):
			// On-chain verification rejected the confirmation — the root was not
			// (correctly) set by this tx. Leave the epoch unpublished; the admin must
			// re-confirm with the mined txHash that actually set this epoch's root.
			utils.ErrorResponse(c, http.StatusUnprocessableEntity, "On-chain verification failed: the txHash did not set this epoch's root on-chain (epoch left unpublished)")
		case strings.Contains(err.Error(), "not found"):
			utils.ErrorResponse(c, http.StatusNotFound, "Epoch not found")
		default:
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to confirm publish")
		}
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Distribution published", gin.H{"epoch": req.Epoch})
}

// GetClaimData handles GET /api/rewards/claim-data — for the authed user,
// returns {cumulativeAmountBaseUnits, proof, distributorAddress, chainId,
// merkleRoot, account, included} from the latest published epoch so the app can
// build the claim tx. Returns included=false (HTTP 200) when the user isn't in
// the tree (ineligible / zero / no epoch yet).
func (h *RewardDistributionHandler) GetClaimData(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if h.merkle == nil {
		// Feature dormant — respond cleanly with "nothing to claim".
		utils.SuccessResponse(c, http.StatusOK, "Claim data", services.ClaimData{Included: false})
		return
	}

	data, err := h.merkle.ClaimDataForUser(c.Request.Context(), user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to load claim data")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Claim data", data)
}
