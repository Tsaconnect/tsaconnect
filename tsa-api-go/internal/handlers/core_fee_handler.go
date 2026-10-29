package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// CoreFeeHandler serves fee authoring on the selected core set's FeeConfig
// (/api/admin/fees/core, behind RequireP2PAdmin). The chain is the record: the API reads it,
// builds the CONFIG_ROLE txs and re-reads it to verify; the admin's own wallet signs. The routes
// stay mounted with no set selected and answer 503, so the console can tell "off" from "missing".
type CoreFeeHandler struct {
	DB    *gorm.DB
	Admin *services.CoreFeeAdmin
}

func NewCoreFeeHandler(db *gorm.DB, admin *services.CoreFeeAdmin) *CoreFeeHandler {
	return &CoreFeeHandler{DB: db, Admin: admin}
}

func (h *CoreFeeHandler) ready(c *gin.Context) bool {
	if h.Admin == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "No core contract set is selected on this API")
		return false
	}
	return true
}

func (h *CoreFeeHandler) chainID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("chainId"), 10, 64)
	if err != nil || id <= 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "chainId must be a chain id")
		return 0, false
	}
	return id, true
}

func (h *CoreFeeHandler) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrCoreFeeInvalid):
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrCoreSetNotReady):
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "The core set on this chain has not passed its boot gates")
	default:
		utils.ErrorResponse(c, http.StatusBadGateway, "Could not read FeeConfig: "+services.RedactAlertText(err.Error()))
	}
}

// ListChains handles GET /api/admin/fees/core.
func (h *CoreFeeHandler) ListChains(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Core fee chains", gin.H{"chains": h.Admin.Chains()})
}

// State handles GET /api/admin/fees/core/:chainId — the live rates, limits and recipient.
func (h *CoreFeeHandler) State(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	id, ok := h.chainID(c)
	if !ok {
		return
	}
	st, err := h.Admin.State(c.Request.Context(), id)
	if err != nil {
		h.fail(c, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Core fee config", st)
}

// Plan handles POST /api/admin/fees/core/:chainId/plan {rates, limits, feeRecipient} — the
// unsigned txs that apply the change (none when the chain already holds it).
func (h *CoreFeeHandler) Plan(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	id, ok := h.chainID(c)
	if !ok {
		return
	}
	var change services.CoreFeeChange
	if err := c.ShouldBindJSON(&change); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Body must be {rates, limits, feeRecipient}")
		return
	}
	plan, err := h.Admin.Plan(c.Request.Context(), id, change)
	if err != nil {
		h.fail(c, err)
		return
	}
	if len(plan.Txs) > 0 {
		h.audit(c, "fee_config.planned", id, map[string]any{"changes": plan.Changes})
	}
	utils.SuccessResponse(c, http.StatusOK, "Fee change planned", plan)
}

// Verify handles POST /api/admin/fees/core/:chainId/verify {change, txHashes} — re-reads the
// chain; pending lists what it does not hold yet. The hashes are recorded, never trusted.
func (h *CoreFeeHandler) Verify(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	id, ok := h.chainID(c)
	if !ok {
		return
	}
	var req struct {
		Change   services.CoreFeeChange `json:"change"`
		TxHashes []string               `json:"txHashes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Body must be {change, txHashes}")
		return
	}
	pending, err := h.Admin.Verify(c.Request.Context(), id, req.Change)
	if err != nil {
		h.fail(c, err)
		return
	}
	if len(pending) == 0 {
		h.audit(c, "fee_config.applied", id, map[string]any{"change": req.Change, "txHashes": req.TxHashes})
	}
	utils.SuccessResponse(c, http.StatusOK, "Fee change verified", gin.H{"applied": len(pending) == 0, "pending": pending})
}

// audit writes the shared admin trail row (p2p_audit_logs).
func (h *CoreFeeHandler) audit(c *gin.Context, action string, chainID int64, extra map[string]any) {
	meta := map[string]any{"chainId": chainID}
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
