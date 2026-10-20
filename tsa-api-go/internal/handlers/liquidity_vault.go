package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// VaultLiquidity is the shared-pool LP side (spec 2026-10-14 §5, §6);
// *services.VaultLPService satisfies it. Every call is scoped to the caller.
type VaultLiquidity interface {
	Me(ctx context.Context, userID uuid.UUID) (*services.LiquidityVaultMe, error)
	Activity(ctx context.Context, userID uuid.UUID, cursor string, limit int) (*services.LiquidityVaultActivityPage, error)
	Activate(ctx context.Context, userID uuid.UUID, wallet string) (*services.LiquidityVaultMe, error)
	PrepareDeposit(ctx context.Context, userID uuid.UUID, amount string) (*services.LPTxPlan, error)
	PrepareWithdraw(ctx context.Context, userID uuid.UUID, amount string) (*services.LPTxPlan, error)
	PrepareClaim(ctx context.Context, userID uuid.UUID, to string) (*services.LPTxPlan, error)
	ClaimForMe(ctx context.Context, userID uuid.UUID) (string, error)
	SetFiatMode(ctx context.Context, userID uuid.UUID, currency string) (*services.LiquidityVaultMe, error)
}

// SetVaultLiquidity wires the shared-pool LP side. Pass only a non-nil service.
func (h *P2PHandler) SetVaultLiquidity(v VaultLiquidity) { h.vaultLP = v }

// HasVaultLiquidity reports whether /liquidity serves the shared pool.
func (h *P2PHandler) HasVaultLiquidity() bool { return h != nil && h.vaultLP != nil }

// vaultLPError maps the LP side's refusals; false means it was not one of them.
func vaultLPError(c *gin.Context, err error) bool {
	switch {
	case errors.Is(err, services.ErrLPNotEligible):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, err.Error(), "kyc_verification_required")
	case errors.Is(err, services.ErrCompanyUserCannotBeLP):
		utils.ErrorResponseWithCode(c, http.StatusForbidden, "The company account cannot provide liquidity", services.CompanyAccountNotEligibleCode)
	case errors.Is(err, services.ErrLPFiatUnavailable):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, err.Error(), "fiat_unavailable")
	case errors.Is(err, services.ErrLPFiatCurrency):
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrLPPausedByAdmin):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "liquidity_paused")
	case errors.Is(err, services.ErrLPWalletInUse):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "wallet_in_use")
	case errors.Is(err, services.ErrLPOldWalletHasFunds):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "Withdraw and claim from your old wallet first", "liquidity_old_wallet_has_funds")
	case errors.Is(err, services.ErrLPWalletMismatch):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "liquidity_wallet_changed")
	case errors.Is(err, services.ErrLPNotAllowedYet):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "depositor_pending")
	case errors.Is(err, services.ErrLPPoolPaused):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "pool_paused")
	case errors.Is(err, services.ErrLPPoolFull):
		utils.ErrorResponseWithCode(c, http.StatusConflict, err.Error(), "pool_full")
	case errors.Is(err, services.ErrLPLimitsUnavailable):
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, err.Error(), "deposits_unavailable")
	case errors.Is(err, services.ErrLPClaimUnavailable):
		utils.ErrorResponseWithCode(c, http.StatusConflict, "Claims are not available right now. Try again later.", "claim_unavailable")
	case errors.Is(err, services.ErrLPDepositLocked):
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, err.Error(), "deposit_locked")
	case errors.Is(err, services.ErrLPAmount), errors.Is(err, services.ErrLPClaimTooSmall), errors.Is(err, services.ErrLPClaimTo),
		errors.Is(err, services.ErrLPNothingToClaim):
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrLPClaimRateLimit):
		utils.ErrorResponse(c, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, services.ErrNotLiquidityProvider):
		utils.ErrorResponse(c, http.StatusNotFound, "You are not a liquidity provider")
	case errors.Is(err, services.ErrLiquidityActivityCursor):
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid cursor")
	default:
		return false
	}
	return true
}

func (h *P2PHandler) vaultLPFail(c *gin.Context, what string, userID uuid.UUID, err error) {
	if vaultLPError(c, err) {
		return
	}
	log.Printf("[liquidity] %s user=%s: %s", what, userID, services.RedactAlertText(err.Error()))
	utils.ErrorResponse(c, http.StatusInternalServerError, "Could not "+what+" right now")
}

// VaultLiquidityMe handles GET /api/liquidity/me in shared-pool mode.
func (h *P2PHandler) VaultLiquidityMe(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	me, err := h.vaultLP.Me(c.Request.Context(), user.ID)
	if err != nil {
		h.vaultLPFail(c, "load your liquidity", user.ID, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Liquidity", me)
}

// VaultLiquidityActivity handles GET /api/liquidity/me/activity?cursor=&limit=.
func (h *P2PHandler) VaultLiquidityActivity(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	page, err := h.vaultLP.Activity(c.Request.Context(), user.ID, c.Query("cursor"), limit)
	if err != nil {
		h.vaultLPFail(c, "load your activity", user.ID, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Liquidity activity", page)
}

// ActivateLiquidity handles POST /api/liquidity/activate: instant, on the
// caller's proven wallet (the vault pays withdrawals and claims to it).
func (h *P2PHandler) ActivateLiquidity(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	wallet, ok := requireProvenWallet(c, user)
	if !ok {
		return
	}
	me, err := h.vaultLP.Activate(c.Request.Context(), user.ID, wallet)
	if err != nil {
		h.vaultLPFail(c, "activate liquidity", user.ID, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Liquidity activated", me)
}

type liquidityAmountRequest struct {
	Amount string `json:"amount" binding:"required"`
}

// PrepareLiquidityDeposit handles POST /api/liquidity/deposit/prepare.
func (h *P2PHandler) PrepareLiquidityDeposit(c *gin.Context) {
	h.prepareLiquidity(c, "prepare your deposit", func(ctx context.Context, uid uuid.UUID, amount string) (*services.LPTxPlan, error) {
		return h.vaultLP.PrepareDeposit(ctx, uid, amount)
	})
}

// PrepareLiquidityWithdraw handles POST /api/liquidity/withdraw/prepare.
func (h *P2PHandler) PrepareLiquidityWithdraw(c *gin.Context) {
	h.prepareLiquidity(c, "prepare your withdrawal", func(ctx context.Context, uid uuid.UUID, amount string) (*services.LPTxPlan, error) {
		return h.vaultLP.PrepareWithdraw(ctx, uid, amount)
	})
}

func (h *P2PHandler) prepareLiquidity(c *gin.Context, what string, prepare func(context.Context, uuid.UUID, string) (*services.LPTxPlan, error)) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	var req liquidityAmountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "amount is required")
		return
	}
	plan, err := prepare(c.Request.Context(), user.ID, req.Amount)
	if err != nil {
		h.vaultLPFail(c, what, user.ID, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Sign and send", plan)
}

type liquidityClaimRequest struct {
	To string `json:"to"`
}

// PrepareLiquidityClaim handles POST /api/liquidity/claim/prepare {to?}.
func (h *P2PHandler) PrepareLiquidityClaim(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	var req liquidityClaimRequest
	_ = c.ShouldBindJSON(&req)
	plan, err := h.vaultLP.PrepareClaim(c.Request.Context(), user.ID, req.To)
	if err != nil {
		h.vaultLPFail(c, "prepare your claim", user.ID, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Sign and send", plan)
}

// ClaimLiquidityForMe handles POST /api/liquidity/claim-for: a gas-free claim
// paid to the caller's own LP wallet.
func (h *P2PHandler) ClaimLiquidityForMe(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	hash, err := h.vaultLP.ClaimForMe(c.Request.Context(), user.ID)
	if err != nil {
		h.vaultLPFail(c, "claim your earnings", user.ID, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Claim sent", gin.H{"txHash": hash})
}

type liquidityFiatRequest struct {
	Currency *string `json:"currency"` // "NGN", or "" to go back to USDT; required
}

// SetLiquidityFiat handles POST /api/liquidity/fiat {currency}: keep new
// deposits as naira ("NGN") or back as USDT (""). Deposits already made keep
// the mode they were indexed in.
func (h *P2PHandler) SetLiquidityFiat(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	var req liquidityFiatRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Currency == nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "currency is required")
		return
	}
	me, err := h.vaultLP.SetFiatMode(c.Request.Context(), user.ID, *req.Currency)
	if err != nil {
		h.vaultLPFail(c, "change your currency", user.ID, err)
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Liquidity", me)
}
