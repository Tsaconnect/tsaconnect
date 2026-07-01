package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// parseChainID parses a JSON string chainId key (e.g. "56") into an int64.
func parseChainID(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}

// FeeAdminHandler serves the admin configurable-FeeManager set-fee flow (design
// §5): author a desired-rate intent, plan the unsigned FeeManager.setRate tx(s),
// confirm a broadcast tx (verify receipt + read-back THEN refresh the replica +
// advance the intent state machine), and cancel an unapplied intent.
//
// Signed-but-not-broadcast: the backend PREPARES the unsigned txs; the multisig
// signs + broadcasts; the backend CONFIRMS. No backend key custody.
//
// ALL endpoints are super-admin gated (RequireP2PAdmin on the route).
type FeeAdminHandler struct {
	cfg     *config.Config
	intents *services.FeeRateIntentService
	feeCfg  *services.FeeConfigService
	reader  services.FeeManagerChainReader
}

// NewFeeAdminHandler constructs the handler. reader may be nil (no RPC client) —
// plan/confirm then surface "not configured" rather than crashing.
func NewFeeAdminHandler(cfg *config.Config, intents *services.FeeRateIntentService, feeCfg *services.FeeConfigService, reader services.FeeManagerChainReader) *FeeAdminHandler {
	return &FeeAdminHandler{cfg: cfg, intents: intents, feeCfg: feeCfg, reader: reader}
}

// rateInput is the JSON rate shape in the intent request (bps + flat USD in 1e6
// units + flags), mirroring the on-chain Rate struct.
type rateInput struct {
	BPS        uint16 `json:"bps"`
	FlatUSD    uint64 `json:"flatUsd"`
	MCGPExempt bool   `json:"mcgpExempt"`
	Active     bool   `json:"active"`
}

func (r rateInput) toRate() services.FeeManagerRate {
	return services.FeeManagerRate{BPS: r.BPS, FlatUSD: r.FlatUSD, MCGPExempt: r.MCGPExempt, Active: r.Active}
}

// createIntentRequest is POST /api/admin/fees/intent. `overrides` maps a chainID
// (as a JSON string key, e.g. "56") to that chain's rate; absent chains ride the
// global `default`.
type createIntentRequest struct {
	Feature   string               `json:"feature" binding:"required"`
	Token     string               `json:"token" binding:"required"`
	Default   rateInput            `json:"default"`
	Overrides map[string]rateInput `json:"overrides"`
}

// CreateIntent handles POST /api/admin/fees/intent — persists the desired-rate
// intent (state pending) and returns {intentId, status}.
func (h *FeeAdminHandler) CreateIntent(c *gin.Context) {
	if h.intents == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Fee admin not configured")
		return
	}
	var req createIntentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "feature and token are required")
		return
	}
	feature := strings.TrimSpace(req.Feature)
	token := strings.ToUpper(strings.TrimSpace(req.Token))
	if feature == "" || token == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "feature and token are required")
		return
	}
	// Clamp/validate bps against the backend mirror of the on-chain MAX_BPS.
	if int(req.Default.BPS) > services.MaxFeeBPS {
		utils.ErrorResponse(c, http.StatusBadRequest, "default bps exceeds MAX_BPS")
		return
	}
	overrides := map[int64]services.FeeManagerRate{}
	for chainStr, r := range req.Overrides {
		chainID, err := parseChainID(chainStr)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "invalid override chainId: "+chainStr)
			return
		}
		if int(r.BPS) > services.MaxFeeBPS {
			utils.ErrorResponse(c, http.StatusBadRequest, "override bps exceeds MAX_BPS for chain "+chainStr)
			return
		}
		overrides[chainID] = r.toRate()
	}

	intent, err := h.intents.Create(feature, token, req.Default.toRate(), overrides)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to persist fee rate intent")
		return
	}
	utils.SuccessResponse(c, http.StatusCreated, "Fee rate intent created", gin.H{
		"intentId": intent.ID.String(),
		"status":   string(intent.State),
	})
}

// ListIntents handles GET /api/admin/fees/intents — the grouped desired-state
// intents (per feature/token) driving the dashboard's pending-changes view.
// Optional ?state=pending,partially_applied filters by summary state.
func (h *FeeAdminHandler) ListIntents(c *gin.Context) {
	if h.intents == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Fee admin not configured")
		return
	}
	var states []models.FeeRateIntentState
	if raw := strings.TrimSpace(c.Query("state")); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				states = append(states, models.FeeRateIntentState(s))
			}
		}
	}
	views, err := h.intents.ListViews(states...)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to list fee rate intents")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Fee rate intents", gin.H{"intents": views})
}

// planRequest is POST /api/admin/fees/plan.
type planRequest struct {
	IntentID string `json:"intentId" binding:"required"`
}

// Plan handles POST /api/admin/fees/plan — resolves per-chain effective rates,
// reads each chain's on-chain rateFor, and returns the diff + unsigned setRate
// tx(s) (one per affected chain). Idempotent: empty txs when already matching.
func (h *FeeAdminHandler) Plan(c *gin.Context) {
	if h.intents == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Fee admin not configured")
		return
	}
	var req planRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "intentId is required")
		return
	}
	id, err := uuid.Parse(strings.TrimSpace(req.IntentID))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "invalid intentId")
		return
	}
	plan, err := h.intents.Plan(c.Request.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrFeeManagerNotConfigured):
			utils.ErrorResponse(c, http.StatusServiceUnavailable, "FeeManager not configured for any chain")
		case strings.Contains(err.Error(), "record not found"):
			utils.ErrorResponse(c, http.StatusNotFound, "Intent not found")
		default:
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to build plan")
		}
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Fee rate plan", plan)
}

// confirmRequest is POST /api/admin/fees/confirm.
type confirmRequest struct {
	IntentID string `json:"intentId" binding:"required"`
	ChainID  int64  `json:"chainId" binding:"required"`
	TxHash   string `json:"txHash" binding:"required"`
}

// Confirm handles POST /api/admin/fees/confirm — verifies the broadcast setRate
// tx and applies it to the replica + intent state machine (design §5).
//
// Verify-then-write (mirrors the hardened reward ConfirmPublish):
//  1. receipt status == 1, AND
//  2. on-chain rateFor on this chain == the intent's RESOLVED rate for this chain.
//
// ONLY when both pass: write the verified value into the fee_config replica
// (RefreshFromConfirmedRate) and advance the intent state machine
// (pending → partially_applied → applied as chains confirm). On a failed receipt
// OR a rate mismatch → 422, the intent stays pending for that chain, the replica
// is UNTOUCHED, and no retry is auto-issued (the admin re-signs / re-plans).
func (h *FeeAdminHandler) Confirm(c *gin.Context) {
	if h.intents == nil || h.feeCfg == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Fee admin not configured")
		return
	}
	if h.reader == nil || h.cfg == nil || len(h.cfg.FeeManagerChains) == 0 {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "FeeManager chain reader not configured")
		return
	}
	var req confirmRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "intentId, chainId and txHash are required")
		return
	}
	id, err := uuid.Parse(strings.TrimSpace(req.IntentID))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "invalid intentId")
		return
	}
	fmChain, ok := h.cfg.FeeManagerChains[req.ChainID]
	if !ok {
		utils.ErrorResponse(c, http.StatusBadRequest, "no FeeManager configured for chainId")
		return
	}
	intent, err := h.intents.Get(id)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Intent not found")
		return
	}

	// The rate this chain's setRate MUST have established (override ?? global).
	resolved, found, err := h.intents.Resolve(intent.Feature, intent.Token, req.ChainID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to resolve intent rate")
		return
	}
	if !found {
		utils.ErrorResponse(c, http.StatusBadRequest, "chainId is not in scope for this intent")
		return
	}

	ctx := c.Request.Context()

	// (1) Receipt must have mined successfully on this chain.
	mined, verr := h.reader.TxSucceeded(ctx, req.ChainID, req.TxHash)
	if verr != nil || !mined {
		// Not mined / unknown hash / failed receipt → refuse. Replica untouched.
		utils.ErrorResponse(c, http.StatusUnprocessableEntity,
			"On-chain verification failed: the txHash did not mine successfully (intent stays pending for this chain; replica untouched)")
		return
	}

	// (2) On-chain rateFor MUST equal the resolved intent for this chain.
	onchain, rerr := h.reader.RateFor(ctx, req.ChainID, fmChain.Address, intent.Feature, intent.Token)
	if rerr != nil {
		utils.ErrorResponse(c, http.StatusUnprocessableEntity,
			"On-chain verification failed: could not read back rateFor (intent stays pending for this chain; replica untouched)")
		return
	}
	if onchain != resolved {
		utils.ErrorResponse(c, http.StatusUnprocessableEntity,
			"On-chain verification failed: on-chain rateFor does not match the intent (intent stays pending for this chain; replica untouched)")
		return
	}

	// Verified — write the replica from the CONFIRMED on-chain value (chain truth,
	// not admin input) AND advance the intent state machine ATOMICALLY in one DB
	// transaction (findings #3 + #7): the replica write and the state advance
	// commit or roll back together (the cache can never hold a rate the intent
	// never advanced to), and the global-summary advance uses a compare-and-swap
	// so concurrent per-chain confirms can't race it to a wrong value.
	applied, serr := h.intents.ConfirmAndApply(h.feeCfg, id, req.ChainID, onchain)
	if serr != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Verified on-chain but failed to apply (replica + intent state rolled back together)")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Fee rate confirmed", gin.H{
		"intentId":      id.String(),
		"chainId":       req.ChainID,
		"applied":       applied,
		"verifiedValue": onchain,
	})
}

// CancelIntent handles DELETE /api/admin/fees/intent/:id — marks the intent
// cancelled (allowed only while NOT fully applied).
func (h *FeeAdminHandler) CancelIntent(c *gin.Context) {
	if h.intents == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Fee admin not configured")
		return
	}
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "invalid intent id")
		return
	}
	if err := h.intents.Cancel(id); err != nil {
		switch {
		case strings.Contains(err.Error(), "record not found"):
			utils.ErrorResponse(c, http.StatusNotFound, "Intent not found")
		case strings.Contains(err.Error(), "already fully applied"):
			utils.ErrorResponse(c, http.StatusConflict, "Intent is already fully applied; cannot cancel")
		default:
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to cancel intent")
		}
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Intent cancelled", gin.H{"intentId": id.String(), "status": "cancelled"})
}
