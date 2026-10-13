package handlers

import (
	"encoding/json"
	"math/big"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// SignerAdminHandler is the super-admin view of the hot keys' send lanes and
// the two ways out of a held lane (signer consolidation runbook): fill a gap
// a send from outside the server left with a 0-value self-transfer, or clear a
// hold after inspecting the chain. Every action writes an audit row; nothing
// here ever reads or logs key material.
type SignerAdminHandler struct {
	DB       *gorm.DB
	Registry *services.SignerRegistry
}

type signerLaneRequest struct {
	ChainID int64   `json:"chainId" binding:"required"`
	Address string  `json:"address" binding:"required"`
	Nonce   *uint64 `json:"nonce"`
	// MaxGasPriceWei is raise-price-cap's new per-allocation ceiling (wei, decimal).
	MaxGasPriceWei string `json:"maxGasPriceWei"`
}

// Lanes is GET /api/admin/signer/lanes.
func (h *SignerAdminHandler) Lanes(c *gin.Context) {
	utils.SuccessResponse(c, http.StatusOK, "signer lanes", h.Registry.Status(c.Request.Context()))
}

// FillGap is POST /api/admin/signer/lanes/fill-gap {chainId, address, nonce}.
func (h *SignerAdminHandler) FillGap(c *gin.Context) {
	req, lane, ok := h.lane(c)
	if !ok {
		return
	}
	if req.Nonce == nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "nonce is required")
		return
	}
	hash, err := lane.FillGap(c.Request.Context(), *req.Nonce)
	if err != nil {
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
		return
	}
	h.audit(c, "signer.lane_gap_filled", req, map[string]any{"txHash": hash})
	utils.SuccessResponse(c, http.StatusOK, "gap filled", gin.H{"txHash": hash})
}

// Retry is POST /api/admin/signer/lanes/retry {chainId, address, nonce}: a
// nonce held at the speed-up limit gets a fresh count (the fee cap still
// applies, at the next attempt's live price).
func (h *SignerAdminHandler) Retry(c *gin.Context) {
	req, lane, ok := h.lane(c)
	if !ok {
		return
	}
	if req.Nonce == nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "nonce is required")
		return
	}
	if err := lane.RetrySpeedUp(c.Request.Context(), *req.Nonce); err != nil {
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
		return
	}
	h.audit(c, "signer.lane_speed_up_retry", req, nil)
	utils.SuccessResponse(c, http.StatusOK, "speed-up retry armed", nil)
}

// RaisePriceCap is POST /api/admin/signer/lanes/raise-price-cap {chainId,
// address, nonce, maxGasPriceWei}: the exit from a lane held at the speed-up
// fee cap. That one allocation gets its own gas-price ceiling (above every
// signing of it, at most 10× the chain's default ceiling) and a fresh bump
// count; the hold clears and the upkeep re-prices the SAME call. Audited with
// the previous and the new cap.
func (h *SignerAdminHandler) RaisePriceCap(c *gin.Context) {
	req, lane, ok := h.lane(c)
	if !ok {
		return
	}
	capWei, valid := new(big.Int).SetString(strings.TrimSpace(req.MaxGasPriceWei), 10)
	if req.Nonce == nil || !valid || capWei.Sign() <= 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "nonce and a positive maxGasPriceWei are required")
		return
	}
	prev, err := lane.RaisePriceCap(c.Request.Context(), *req.Nonce, capWei)
	if err != nil {
		utils.ErrorResponse(c, http.StatusConflict, err.Error())
		return
	}
	h.audit(c, "signer.lane_price_cap_raised", req, map[string]any{"previousCapWei": prev, "capWei": capWei.String()})
	utils.SuccessResponse(c, http.StatusOK, "price cap raised", nil)
}

// ClearHold is POST /api/admin/signer/lanes/clear-hold {chainId, address}.
func (h *SignerAdminHandler) ClearHold(c *gin.Context) {
	req, lane, ok := h.lane(c)
	if !ok {
		return
	}
	nonce, reason, held := lane.Held()
	if !held {
		utils.ErrorResponse(c, http.StatusConflict, "the lane is not held")
		return
	}
	if err := lane.ClearHold(c.Request.Context()); err != nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	h.audit(c, "signer.lane_hold_cleared", req, map[string]any{"heldNonce": nonce, "heldReason": reason})
	utils.SuccessResponse(c, http.StatusOK, "hold cleared", nil)
}

func (h *SignerAdminHandler) lane(c *gin.Context) (signerLaneRequest, *services.KeyLane, bool) {
	var req signerLaneRequest
	if err := c.ShouldBindJSON(&req); err != nil || !common.IsHexAddress(req.Address) {
		utils.ErrorResponse(c, http.StatusBadRequest, "chainId and a hex address are required")
		return req, nil, false
	}
	lane := h.Registry.LaneFor(req.ChainID, strings.ToLower(common.HexToAddress(req.Address).Hex()))
	if lane == nil {
		utils.ErrorResponse(c, http.StatusNotFound, "no signer lane for that chain and address")
		return req, nil, false
	}
	return req, lane, true
}

// audit writes the action's audit row (the P2P audit log, the shared admin
// trail): who, which lane, what. Never key material.
func (h *SignerAdminHandler) audit(c *gin.Context, action string, req signerLaneRequest, extra map[string]any) {
	meta := map[string]any{"chainId": req.ChainID, "address": strings.ToLower(req.Address)}
	if req.Nonce != nil {
		meta["nonce"] = *req.Nonce
	}
	for k, v := range extra {
		meta[k] = v
	}
	raw, _ := json.Marshal(meta)
	row := models.P2PAuditLog{ID: uuid.New(), Action: action, Metadata: datatypes.JSON(raw)}
	if u := getUserFromContext(c); u != nil {
		row.ActorID = &u.ID
	}
	if h.DB != nil {
		_ = h.DB.WithContext(c.Request.Context()).Create(&row).Error
	}
}
