package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// ITHandler serves /api/it/* — Instant Transfer (buy/sell MCGP with Naira via
// the company PM, routed through Contract B's spend-only premium). Registered
// only when IT is configured (operator signer present); dormant otherwise.
//
// In this phase the NGN legs are admin-approved (buy credit / sell settle /
// reverse) because auto-credit is gated on the confirmed strong attribution key.
type ITHandler struct {
	db *gorm.DB
	it *services.ITService
}

func NewITHandler(db *gorm.DB, it *services.ITService) *ITHandler {
	return &ITHandler{db: db, it: it}
}

func (h *ITHandler) userWallet(userID uuid.UUID) string {
	var u models.User
	if err := h.db.First(&u, "id = ?", userID).Error; err != nil {
		return ""
	}
	return u.WalletAddress
}

// GET /api/it/rate — the live Naira buy/sell rates, derived from the admin-set
// USD price and the current FX feed. Same JSON shape as before so the app is
// unchanged. 404 when no price is set; 503 when FX pricing is unavailable.
func (h *ITHandler) GetRate(c *gin.Context) {
	buyKobo, sellKobo, fx, err := h.it.EffectiveKoboRates(c.Request.Context())
	if err != nil {
		if errors.Is(err, services.ErrITNoRate) {
			utils.ErrorResponse(c, http.StatusNotFound, "IT rate not set")
			return
		}
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Instant Transfer pricing is temporarily unavailable")
		return
	}
	// `stale` is additive (existing keys unchanged). The display tolerates a stale
	// rate, but the app should refuse to quote money against it — buy/sell on the
	// server reject stale FX outright.
	utils.SuccessResponse(c, http.StatusOK, "Rate", gin.H{
		"buyRateKobo": buyKobo.String(), "sellRateKobo": sellKobo.String(),
		"stale": fx.Stale,
	})
}

// GET /api/it/balance — premium breakdown + legacy.
func (h *ITHandler) GetBalance(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	swap := h.it.Swap()
	bought, _ := swap.PremiumBought(user.WalletAddress)
	credited, _ := swap.PremiumCredited(user.WalletAddress)
	pending, _ := swap.PendingSettle(user.WalletAddress)
	legacy, _ := swap.LegacyRemaining(user.WalletAddress)
	utils.SuccessResponse(c, http.StatusOK, "Balance", gin.H{
		"premiumBought":   bigOrZero(bought),   // self-exitable (USDC-bought)
		"premiumCredited": bigOrZero(credited), // IT/NGN, spend-only
		"pendingSettle":   bigOrZero(pending),  // in-flight NGN sell
		"legacyRemaining": bigOrZero(legacy),
	})
}

type itBuyReq struct {
	NgnKobo string `json:"ngnKobo"`
}

// POST /api/it/buy — create a buy intent; returns the payment reference to
// include in the Nomba transfer to the company PM.
func (h *ITHandler) Buy(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	var req itBuyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	intent, err := h.it.CreateBuyIntent(c.Request.Context(), user.ID, user.WalletAddress, req.NgnKobo, nil)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	// Surface the real pay-into account (the company PM's Nomba VA), not a
	// hardcoded note. payTo is null when no PM is configured so the app can show
	// a clear "payments unavailable" state instead of a fake account.
	var payTo interface{}
	if pm, _ := h.it.CompanyPM(c.Request.Context()); pm != nil {
		payTo = gin.H{
			"accountNumber": pm.InboundAccountNumber,
			"bank":          pm.InboundBank,
			"accountName":   pm.Name,
			"reference":     intent.PaymentRef,
		}
	}
	utils.SuccessResponse(c, http.StatusOK, "Buy intent created", gin.H{
		"intent":     intent,
		"paymentRef": intent.PaymentRef,
		"payTo":      payTo,
	})
}

type itSellReq struct {
	McgpAmount string `json:"mcgpAmount"`
}

// POST /api/it/sell — debit credited premium (on-chain) and open the NGN payout.
func (h *ITHandler) Sell(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Set up a wallet first")
		return
	}
	var req itSellReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	intent, err := h.it.CreateSellIntent(c.Request.Context(), user.ID, user.WalletAddress, req.McgpAmount)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Sell submitted", gin.H{"intent": intent})
}

// GET /api/it/intents — caller's IT history.
func (h *ITHandler) ListIntents(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	intents, _ := h.it.ListUserIntents(c.Request.Context(), user.ID)
	utils.SuccessResponse(c, http.StatusOK, "Intents", gin.H{"intents": intents})
}

// GET /api/it/intents/:id — single intent (scoped to caller).
func (h *ITHandler) GetIntent(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid id")
		return
	}
	intent, err := h.it.GetIntent(c.Request.Context(), id)
	if err != nil || intent.UserID != user.ID {
		utils.ErrorResponse(c, http.StatusNotFound, "Intent not found")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Intent", gin.H{"intent": intent})
}

// ──────────────────────────────── admin ───────────────────────────────────

type itRateReq struct {
	BuyPriceUsdMicros int64 `json:"buyPriceUsdMicros"`
	SellDiscountBps   int   `json:"sellDiscountBps"`
}

// GET /api/it/admin/rate — the raw USD config plus a derived Naira preview for
// the admin dashboard. 404 when no price is set yet.
func (h *ITHandler) AdminGetRate(c *gin.Context) {
	rate, err := h.it.GetActiveRate(c.Request.Context())
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "IT rate not set")
		return
	}
	out := gin.H{
		"buyPriceUsdMicros": rate.BuyPriceUsdMicros,
		"sellDiscountBps":   rate.SellDiscountBps,
	}
	// Best-effort live preview; omit (not error) when the FX feed is down so the
	// admin can still see and edit the configured USD price.
	if buyKobo, sellKobo, fx, derr := h.it.EffectiveKoboRates(c.Request.Context()); derr == nil {
		out["derived"] = gin.H{
			"buyRateKobo":  buyKobo.String(),
			"sellRateKobo": sellKobo.String(),
			"ngnPerUsd":    fx.NgnPerUsd,
			"fxStale":      fx.Stale,
		}
	}
	utils.SuccessResponse(c, http.StatusOK, "Rate", out)
}

// POST /api/it/admin/rate
func (h *ITHandler) AdminSetRate(c *gin.Context) {
	admin := getUserFromContext(c)
	if admin == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var req itRateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	rate, err := h.it.SetRate(c.Request.Context(), admin.ID, req.BuyPriceUsdMicros, req.SellDiscountBps)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Rate updated", gin.H{"rate": rate})
}

// POST /api/it/admin/credit/:id — approve a paid buy intent (operator credits premium).
func (h *ITHandler) AdminApproveCredit(c *gin.Context) {
	h.adminIntentAction(c, func(intentID uuid.UUID, wallet string) (*models.ITIntent, error) {
		return h.it.ApproveBuyCredit(c.Request.Context(), intentID, wallet)
	})
}

// POST /api/it/admin/settle/:id — finalize a sell after a confirmed NGN payout.
func (h *ITHandler) AdminSettle(c *gin.Context) {
	h.adminIntentAction(c, func(intentID uuid.UUID, wallet string) (*models.ITIntent, error) {
		return h.it.SettleSell(c.Request.Context(), intentID, wallet)
	})
}

// POST /api/it/admin/reverse/:id — reverse a sell whose NGN payout failed.
func (h *ITHandler) AdminReverse(c *gin.Context) {
	h.adminIntentAction(c, func(intentID uuid.UUID, wallet string) (*models.ITIntent, error) {
		return h.it.ReverseSell(c.Request.Context(), intentID, wallet)
	})
}

func (h *ITHandler) adminIntentAction(c *gin.Context, fn func(uuid.UUID, string) (*models.ITIntent, error)) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid id")
		return
	}
	intent, err := h.it.GetIntent(c.Request.Context(), id)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Intent not found")
		return
	}
	wallet := h.userWallet(intent.UserID)
	if wallet == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Intent user has no wallet")
		return
	}
	out, err := fn(id, wallet)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "OK", gin.H{"intent": out})
}
