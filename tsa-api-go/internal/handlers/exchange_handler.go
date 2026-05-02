package handlers

import (
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

// GetRates handles GET /api/exchange/rates
// Returns live P2P exchange rates for all supported currencies.
func (eh *ExchangeHandler) GetRates(c *gin.Context) {
	rates, err := eh.P2PService.GetAllRates()
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch exchange rates")
		return
	}

	// Convert to response shape
	rateMap := make(map[string]services.P2PRate, len(rates))
	for code, rate := range rates {
		rateMap[code] = rate
	}

	utils.SuccessResponse(c, http.StatusOK, "Exchange rates fetched", services.P2PRatesResponse{
		Base:      "USD",
		Rates:     rateMap,
		UpdatedAt: rateMap["USD"].UpdatedAt, // all rates are fetched together
	})
}

// ConvertPrice handles POST /api/exchange/convert
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

	localAmount, err := eh.P2PService.ConvertUSD(req.Amount, req.Currency)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, err.Error())
		return
	}

	rate, _ := eh.P2PService.GetRate(req.Currency)

	utils.SuccessResponse(c, http.StatusOK, "Price converted", gin.H{
		"usdAmount":     req.Amount,
		"localAmount":   localAmount,
		"currency":      req.Currency,
		"symbol":        currencySymbolStatic(req.Currency),
		"rate":          rate.MidRate,
		"binanceBuy":    rate.BinanceBuy,
		"binanceSell":   rate.BinanceSell,
		"exchangeRate":  rate.MidRate,
	})
}

// currencySymbolStatic is a local copy to avoid importing the services package
// in a way that creates a circular dependency. Keep in sync.
func currencySymbolStatic(code string) string {
	symbols := map[string]string{
		"USD": "$", "NGN": "₦", "GHS": "GH₵", "KES": "KSh",
		"ZAR": "R", "UGX": "USh", "TZS": "TSh", "RWF": "FRw",
		"XOF": "CFA", "XAF": "FCFA", "EUR": "€", "GBP": "£",
	}
	if sym, ok := symbols[code]; ok {
		return sym
	}
	return ""
}
