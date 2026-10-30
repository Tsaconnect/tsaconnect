package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// serviceQuote is the price of a service booking as checkout would charge it
// in USD, before the escrow's flat fee and any payout costs.
type serviceQuote struct {
	OnRequest      bool    `json:"onRequest"`
	BookingMode    string  `json:"bookingMode,omitempty"`
	PriceUnit      string  `json:"priceUnit,omitempty"`
	ChargesPerUnit bool    `json:"chargesPerUnit"`
	Units          int     `json:"units"`
	UnitPriceUSD   float64 `json:"unitPriceUsd,omitempty"` // per unit when ChargesPerUnit
	PriceUSD       float64 `json:"priceUsd"`               // the listing's charge (all units + access fee)
	CalloutUSD     float64 `json:"calloutUsd"`
	TotalUSD       float64 `json:"totalUsd"`
}

// QuoteServiceOrder handles POST /api/orders/service/quote — what
// CreateServiceOrder would charge for this booking, worked out by the same
// pricing (resolveBooking + resolveServicePricing) inside a transaction that
// is rolled back, so nothing is created or held. The booking sheet calls it as
// the buyer fills the form; a 400 means the booking is not priceable yet
// (missing dates, no slot, a full night) and carries the reason.
func (ch *CheckoutHandler) QuoteServiceOrder(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}
	var req createServiceOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "token and productId are required")
		return
	}
	productID, err := uuid.Parse(req.ProductID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	tx := config.DB.Begin()
	if tx.Error != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to start transaction")
		return
	}
	defer tx.Rollback() // a quote never writes

	var product models.Product
	if err := tx.First(&product, "id = ?", productID).Error; err != nil || product.Type != models.ProductTypeService {
		utils.ErrorResponse(c, http.StatusNotFound, "Service not found")
		return
	}
	var seller models.User
	if err := tx.First(&seller, "id = ?", product.UserID).Error; err != nil || !product.IsVisibleToPublic(&seller) {
		utils.ErrorResponse(c, http.StatusBadRequest, "This service is no longer available")
		return
	}

	serviceType, profile := resolveServiceTypeAndProfile(tx, &product)
	bk, msg := resolveBooking(tx, &product, &profile, serviceType, req.Quantity, answersOrEmpty(req.Answers))
	if msg != "" {
		utils.ErrorResponse(c, http.StatusBadRequest, msg)
		return
	}
	q := serviceQuote{BookingMode: bk.mode, PriceUnit: profile.PriceUnit, Units: max(bk.units, 1)}
	if profile.PricingMethod == models.PricingMethodOnRequest {
		q.OnRequest = true
		utils.SuccessResponse(c, http.StatusOK, "Priced on request", q)
		return
	}

	city := firstNonEmpty(req.BuyerCity, user.City)
	state := firstNonEmpty(req.BuyerState, user.State)
	country := firstNonEmpty(req.BuyerCountry, user.Country)
	origin := resolveShippingOrigin(&product, &seller)
	zone := DetectShippingZone(city, state, country, origin.City, origin.State, origin.Country)

	price, callout, _, _, _, perr := ch.resolveServicePricing(
		tx, user.ID, &product, serviceType, &profile, zone, req.ServiceTier, req.MenuSelections, req.BookingSlot, bk,
	)
	if perr != "" {
		utils.ErrorResponse(c, http.StatusBadRequest, perr)
		return
	}
	q.ChargesPerUnit = bk.units > 0 && models.ChargesPerUnit(bk.mode, profile.PriceUnit)
	if q.ChargesPerUnit {
		q.UnitPriceUSD = (price - profile.AccessFeeUSD) / float64(bk.units)
	}
	q.PriceUSD, q.CalloutUSD, q.TotalUSD = price, callout, price+callout
	utils.SuccessResponse(c, http.StatusOK, "Quote", q)
}
