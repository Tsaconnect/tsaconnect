package handlers

import (
	"errors"
	"log"
	"math"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// ExchangeHandler handles exchange rate endpoints.
type ExchangeHandler struct {
	P2PService *services.P2PService
}

// NewExchangeHandler creates a new ExchangeHandler.
func NewExchangeHandler(ps *services.P2PService) *ExchangeHandler {
	return &ExchangeHandler{P2PService: ps}
}

// GetRates handles GET /api/exchange/rates.
// Returns live P2P exchange rates for all supported currencies.
func (eh *ExchangeHandler) GetRates(c *gin.Context) {
	snap, err := eh.P2PService.GetSnapshot()
	if err != nil {
		log.Printf("[ExchangeHandler] GetSnapshot failed: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Exchange rates are temporarily unavailable")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Exchange rates fetched", services.P2PRatesResponse{
		Base:  "USD",
		Rates: snap.Rates,
		// Use the snapshot's cachedAt as the authoritative timestamp; relying
		// on USD's UpdatedAt would be fragile if a future code path bumped it.
		UpdatedAt:    snap.CachedAt.Unix(),
		StaleSeconds: snap.StaleSeconds,
	})
}

// ConvertPrice handles POST /api/exchange/convert.
// Converts a USD amount to the specified local currency.
type convertPriceRequest struct {
	Amount   float64 `json:"amount" binding:"required"`
	Currency string  `json:"currency" binding:"required"`
}

func (eh *ExchangeHandler) ConvertPrice(c *gin.Context) {
	var req convertPriceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "amount and currency are required")
		return
	}
	// Reject negative, zero, NaN, ±Inf. `binding:"required"` already rejects 0
	// for floats, but does not catch NaN/Inf or negative values.
	if !isPositiveFinite(req.Amount) {
		utils.ErrorResponse(c, http.StatusBadRequest, "amount must be a positive finite number")
		return
	}

	// Single GetRate call. Distinguish "unknown currency" (real 400) from any
	// other error (upstream/internal — 500). Don't leak internal error
	// messages to clients on the 500 path.
	rate, err := eh.P2PService.GetRate(req.Currency)
	if err != nil {
		if errors.Is(err, services.ErrUnsupportedCurrency) {
			utils.ErrorResponse(c, http.StatusBadRequest, "unsupported currency")
			return
		}
		log.Printf("[ExchangeHandler] GetRate(%q) failed: %v", req.Currency, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Exchange rates are temporarily unavailable")
		return
	}

	localAmount := req.Amount * rate.MidRate

	utils.SuccessResponse(c, http.StatusOK, "Price converted", gin.H{
		"usdAmount":    req.Amount,
		"localAmount":  localAmount,
		"currency":     req.Currency,
		"symbol":       rate.Symbol,
		"rate":         rate.MidRate,
		"binanceBuy":   rate.BinanceBuy,
		"binanceSell":  rate.BinanceSell,
		"exchangeRate": rate.MidRate,
		// Stale signaling — clients quoting against this response MUST refuse
		// to settle if `stale` is true, or render a warning to the user.
		"stale":  rate.Stale,
		"source": rate.Source,
	})
}

func isPositiveFinite(f float64) bool {
	return f > 0 && !math.IsNaN(f) && !math.IsInf(f, 0)
}
