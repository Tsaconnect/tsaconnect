package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// FeeConfigHandler serves the admin fee-config endpoints. It mutates the
// fee_configs table and invalidates the FeeConfigService cache so rate changes
// take effect without a restart.
type FeeConfigHandler struct {
	DB      *gorm.DB
	Service *services.FeeConfigService
	Config  *config.Config
}

// NewFeeConfigHandler creates a FeeConfigHandler.
func NewFeeConfigHandler(db *gorm.DB, svc *services.FeeConfigService, cfg *config.Config) *FeeConfigHandler {
	return &FeeConfigHandler{DB: db, Service: svc, Config: cfg}
}

// ListFeeConfigs handles GET /api/admin/fee-config — returns all fee_config rows
// (active and inactive) ordered by feature then token.
func (h *FeeConfigHandler) ListFeeConfigs(c *gin.Context) {
	var rows []models.FeeConfig
	if err := h.DB.Order("feature ASC, token_symbol ASC").Find(&rows).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch fee config")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Fee configuration", rows)
}

// updateFeeConfigRequest is the PUT body. All fields are optional pointers so a
// partial update only touches the fields present in the request.
//
// Active is accepted but constrained: see UpdateFeeConfig. Deactivating a row is
// currently rejected because FeeConfigService.Load() filters active=true, so an
// inactive row falls out of the cache and RateFor reports found=false — which
// makes the package helpers fall through to the built-in seed default rather
// than disabling the fee. Until proper "disabled = 0 fee" semantics land, a
// disable would silently REVERT the fee to its seed default, a money-control
// footgun. Reactivating (active=true) is always allowed.
type updateFeeConfigRequest struct {
	BPS           *int     `json:"bps"`
	FlatAmountUSD *float64 `json:"flatAmountUsd"`
	Active        *bool    `json:"active"`
}

// UpdateFeeConfig handles PUT /api/admin/fee-config/:id — updates bps, flat
// amount, and/or active flag for a single row, then invalidates the cache.
//
// SUPERSEDED LEGACY SAVE PATH — NOT a sanctioned post-cutover writer. This
// handler writes fee_configs directly from admin INPUT (DB.Updates below) and
// invalidates the cache the moment the admin clicks save (Invalidate below).
// That "invalidate on save" is exactly the quote-ahead-of-chain (replica-leads-
// chain) pattern the configurable-FeeManager design §4 CQRS rule REMOVES: the
// replica must lag CONFIRMED chain truth, never lead it (see
// FeeConfigService.RefreshFromConfirmedRate, the ONLY sanctioned writer from
// chain truth). The full intent → on-chain setRate → confirm flow that replaces
// this handler is deferred to task #43 (design §13); at that cutover, retire
// UpdateFeeConfig's direct-write + Invalidate-on-save in favor of the
// confirm-driven RefreshFromConfirmedRate. Do not treat it as a green light to
// write the replica from admin input.
//
// NOTE: the fee_config table is now the live source of truth. Callers MUST
// ensure the cart/quote path reads config rather than the historical hardcoded
// constants, or an admin rate edit will diverge the quote from the charge.
func (h *FeeConfigHandler) UpdateFeeConfig(c *gin.Context) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid fee config ID")
		return
	}

	var req updateFeeConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	var row models.FeeConfig
	if err := h.DB.First(&row, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Fee config not found")
		return
	}
	if models.OnChainFeeFeatures[row.Feature] {
		utils.ErrorResponse(c, http.StatusConflict,
			"This fee is charged by a smart contract, so it can only change on-chain. Use Author on this row instead of editing it directly.")
		return
	}

	if models.IsPoolSetting(row.Feature) {
		bps, flat := row.BPS, row.FlatAmountUSD
		if req.BPS != nil {
			bps = *req.BPS
		}
		if req.FlatAmountUSD != nil {
			flat = *req.FlatAmountUSD
		}
		if err := services.ValidatePoolSetting(h.DB, row.Feature, bps, flat); err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
			return
		}
	}

	updates := map[string]interface{}{}
	if req.BPS != nil {
		if *req.BPS < 0 || *req.BPS > 10000 {
			utils.ErrorResponse(c, http.StatusBadRequest, "bps must be between 0 and 10000")
			return
		}
		updates["bps"] = *req.BPS
	}
	if req.FlatAmountUSD != nil {
		if *req.FlatAmountUSD < 0 {
			utils.ErrorResponse(c, http.StatusBadRequest, "flatAmountUsd must be >= 0")
			return
		}
		updates["flat_amount_usd"] = *req.FlatAmountUSD
	}
	if req.Active != nil {
		if !*req.Active {
			// Deactivating is not yet a real "fee off" control: Load() drops
			// inactive rows from the cache, so RateFor would return found=false
			// and the package helpers would fall back to the seed DEFAULT rate —
			// silently reverting the fee instead of disabling it. Reject until
			// disable-semantics (found=true, bps=0/flat=0) are implemented.
			utils.ErrorResponse(c, http.StatusBadRequest,
				"Deactivating a fee row is not supported: it would revert the fee to its seed default rather than disable it. Set bps/flatAmountUsd to 0 to zero a fee.")
			return
		}
		updates["active"] = *req.Active
	}

	if len(updates) == 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "No updatable fields provided (bps, flatAmountUsd, active)")
		return
	}

	// LEGACY direct write from admin input — superseded by the confirm-driven
	// RefreshFromConfirmedRate (task #43). See the function doc above.
	if err := h.DB.Model(&row).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update fee config")
		return
	}

	// LEGACY invalidate-on-save — the replica-leads-chain pattern the design §4
	// CQRS rule removes; superseded by RefreshFromConfirmedRate (task #43). See
	// the function doc above. Refreshes the in-memory cache so the edit takes
	// effect immediately.
	if h.Service != nil {
		if err := h.Service.Invalidate(); err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Fee config updated but cache refresh failed")
			return
		}
	}

	if err := h.DB.First(&row, "id = ?", id).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to reload fee config")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Fee config updated", row)
}
