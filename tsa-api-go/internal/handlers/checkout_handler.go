package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// CheckoutHandler handles checkout and order escrow endpoints.
type CheckoutHandler struct {
	Config            *config.Config
	BlockchainService *services.BlockchainService
	EscrowService     *services.EscrowService
	EventBus          *events.Bus
	otcService        *services.OTCService
}

// NewCheckoutHandler creates a new CheckoutHandler.
func NewCheckoutHandler(cfg *config.Config, bs *services.BlockchainService, es *services.EscrowService, bus *events.Bus, otc *services.OTCService) *CheckoutHandler {
	return &CheckoutHandler{
		Config:            cfg,
		BlockchainService: bs,
		EscrowService:     es,
		EventBus:          bus,
		otcService:        otc,
	}
}

// --- Shipping zone detection ---

const (
	ShippingZoneSameCity      = "same_city"
	ShippingZoneSameState     = "same_state"
	ShippingZoneSameCountry   = "same_country"
	ShippingZoneInternational = "international"
)

type shippingOrigin struct {
	City    string
	State   string
	Country string
}

func normalizeLocationComponent(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func normalizeStateComponent(value string) string {
	normalized := normalizeLocationComponent(value)
	for _, suffix := range []string{" state", " province", " region"} {
		if strings.HasSuffix(normalized, suffix) {
			return strings.TrimSpace(strings.TrimSuffix(normalized, suffix))
		}
	}
	if normalized == "federal capital territory" {
		return "fct"
	}
	return normalized
}

func parseProductLocation(location string) shippingOrigin {
	parts := strings.Split(location, ",")
	trimmed := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			trimmed = append(trimmed, part)
		}
	}

	origin := shippingOrigin{}
	if len(trimmed) > 0 {
		origin.City = trimmed[0]
	}
	if len(trimmed) > 1 {
		origin.State = trimmed[1]
	}
	if len(trimmed) > 2 {
		origin.Country = trimmed[2]
	}

	return origin
}

func resolveShippingOrigin(product *models.Product, seller *models.User) shippingOrigin {
	origin := parseProductLocation(product.Location)
	if seller != nil {
		if origin.City == "" {
			origin.City = seller.City
		}
		if origin.State == "" {
			origin.State = seller.State
		}
		if origin.Country == "" {
			origin.Country = seller.Country
		}
	}
	return origin
}

// DetectShippingZone compares buyer and seller locations.
func DetectShippingZone(buyerCity, buyerState, buyerCountry, sellerCity, sellerState, sellerCountry string) string {
	bc := normalizeLocationComponent(buyerCountry)
	sc := normalizeLocationComponent(sellerCountry)
	bs := normalizeStateComponent(buyerState)
	ss := normalizeStateComponent(sellerState)
	bci := normalizeLocationComponent(buyerCity)
	sci := normalizeLocationComponent(sellerCity)

	if bc == "" || sc == "" {
		return ShippingZoneSameCountry
	}

	if bc != sc {
		return ShippingZoneInternational
	}
	if bs != ss {
		return ShippingZoneSameCountry
	}
	if bci != sci {
		return ShippingZoneSameState
	}
	return ShippingZoneSameCity
}

// GetShippingRate returns the shipping cost for a product in a given zone.
func GetShippingRate(product *models.Product, zone string) float64 {
	switch zone {
	case ShippingZoneSameCity:
		return product.ShippingSameCity
	case ShippingZoneSameState:
		return product.ShippingSameState
	case ShippingZoneSameCountry:
		return product.ShippingSameCountry
	case ShippingZoneInternational:
		return product.ShippingInternational
	default:
		return product.ShippingInternational
	}
}

// --- Fee calculation (big.Int) ---

// CalculatePlatformFee returns 2% merchant fee for non-MCGP tokens, 0 for MCGP.
// This fee is baked into the listing price — the merchant pays it, not the buyer.
func CalculatePlatformFee(productAmount *big.Int, token string) *big.Int {
	if strings.ToUpper(token) == "MCGP" {
		return big.NewInt(0)
	}
	// 2% = productAmount * 200 / 10000
	fee := new(big.Int).Mul(productAmount, big.NewInt(int64(models.MerchantFeeBPS)))
	fee.Div(fee, big.NewInt(10000))
	return fee
}

// tokenDecimals returns the number of decimals for a given token symbol.
func tokenDecimals(token string) int {
	switch strings.ToUpper(token) {
	case "USDC", "USDT":
		return 6
	default:
		return 18
	}
}

// weiToUSDFloat converts a wei string amount back to a USD float value.
func weiToUSDFloat(weiStr string, token string) float64 {
	wei, ok := new(big.Int).SetString(weiStr, 10)
	if !ok || wei.Sign() == 0 {
		return 0
	}
	decimals := tokenDecimals(token)
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	fWei := new(big.Float).SetInt(wei)
	fDiv := new(big.Float).SetInt(divisor)
	result, _ := new(big.Float).Quo(fWei, fDiv).Float64()
	return result
}

// toWei converts a decimal string amount (e.g. "10.5") to a big.Int in the smallest unit.
// Uses string-based parsing to avoid float64 precision loss.
func toWei(amountStr string, decimals int) (*big.Int, error) {
	// Split on decimal point
	parts := strings.Split(amountStr, ".")
	intPart := parts[0]
	fracPart := ""
	if len(parts) == 2 {
		fracPart = parts[1]
	} else if len(parts) > 2 {
		return nil, fmt.Errorf("invalid decimal amount: %s", amountStr)
	}

	// Pad or truncate fractional part to match decimals
	if len(fracPart) > decimals {
		fracPart = fracPart[:decimals]
	} else {
		for len(fracPart) < decimals {
			fracPart += "0"
		}
	}

	// Combine into a single integer string
	combined := intPart + fracPart

	result, ok := new(big.Int).SetString(combined, 10)
	if !ok {
		return nil, fmt.Errorf("invalid amount: %s", amountStr)
	}
	return result, nil
}

// floatToDecimalStr converts a float64 to a string with enough precision.
func floatToDecimalStr(f float64, decimals int) string {
	return strconv.FormatFloat(f, 'f', decimals, 64)
}

// isValidTransition checks whether a status transition is allowed.
func isValidTransition(currentStatus, newStatus string) bool {
	for _, s := range models.ValidNextStatuses(currentStatus) {
		if s == newStatus {
			return true
		}
	}
	return false
}

// --- Request/Response types ---

type createOrderRequest struct {
	Token        string `json:"token" binding:"required"`
	BuyerCity    string `json:"buyerCity"`
	BuyerState   string `json:"buyerState"`
	BuyerCountry string `json:"buyerCountry"`
}

type submitEscrowRequest struct {
	ApproveTxHash string `json:"approveTxHash" binding:"required"`
	EscrowTxHash  string `json:"escrowTxHash" binding:"required"`
}

type deliverRequest struct {
	DeliveryProofURL string `json:"deliveryProofUrl" binding:"required"`
}

type submitConfirmRequest struct {
	TxHash string `json:"txHash" binding:"required"`
}

type adminResolveRequest struct {
	RefundBuyer bool `json:"refundBuyer"`
}

// --- Endpoints ---

// CreateOrderFromCart handles POST /api/orders — creates orders from cart grouped by seller.
func (ch *CheckoutHandler) CreateOrderFromCart(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	var req createOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "token is required")
		return
	}

	token := strings.ToUpper(req.Token)
	if token != "USDC" && token != "USDT" && token != "MCGP" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Unsupported token. Use USDC, USDT, or MCGP")
		return
	}

	// Get active cart
	var cart models.Cart
	if err := config.DB.Where("user_id = ? AND status = ?", user.ID, "active").First(&cart).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "No active cart found")
		return
	}

	items := cart.GetItems()
	if len(items) == 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cart is empty")
		return
	}

	// Use buyer location from request, fall back to user profile
	buyerCity := req.BuyerCity
	buyerState := req.BuyerState
	buyerCountry := req.BuyerCountry
	if buyerCity == "" {
		buyerCity = user.City
	}
	if buyerState == "" {
		buyerState = user.State
	}
	if buyerCountry == "" {
		buyerCountry = user.Country
	}

	// Group items by seller
	type sellerGroup struct {
		sellerID uuid.UUID
		items    []models.CartItem
	}
	groups := make(map[string]*sellerGroup)
	for _, item := range items {
		key := item.Seller.String()
		if groups[key] == nil {
			groups[key] = &sellerGroup{sellerID: item.Seller}
		}
		groups[key].items = append(groups[key].items, item)
	}

	decimals := tokenDecimals(token)
	var orders []models.Order

	// Wrap everything in a DB transaction
	tx := config.DB.Begin()
	if tx.Error != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to start transaction")
		return
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	for _, group := range groups {
		for _, item := range group.items {
			var product models.Product
			if err := tx.First(&product, "id = ?", item.Product).Error; err != nil {
				tx.Rollback()
				utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Product %s not found", item.Product))
				return
			}

			// Stock validation
			if product.Stock < item.Quantity {
				tx.Rollback()
				utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Insufficient stock for %s (available: %d, requested: %d)", product.Name, product.Stock, item.Quantity))
				return
			}

			// Decrement stock
			if err := tx.Model(&product).Update("stock", product.Stock-item.Quantity).Error; err != nil {
				tx.Rollback()
				utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update product stock")
				return
			}

			// Get seller for shipping zone detection
			var seller models.User
			if err := tx.First(&seller, "id = ?", group.sellerID).Error; err != nil {
				tx.Rollback()
				utils.ErrorResponse(c, http.StatusBadRequest, "Seller not found")
				return
			}

			origin := resolveShippingOrigin(&product, &seller)
			zone := DetectShippingZone(
				buyerCity,
				buyerState,
				buyerCountry,
				origin.City,
				origin.State,
				origin.Country,
			)
			shippingRate := GetShippingRate(&product, zone)

			// Convert to wei using string-based math (no float64 precision loss)
			productTotal := product.Price * float64(item.Quantity)
			shippingTotal := shippingRate * float64(item.Quantity)

			var productAmountWei, shippingAmountWei *big.Int

			if token == "MCGP" {
				// Convert USD prices to MCGP amounts using OTC contract rate
				pricePerMCGP, rateErr := ch.otcService.GetBuyPricePerToken()
				if rateErr != nil || pricePerMCGP.Sign() == 0 {
					tx.Rollback()
					utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to get MCGP exchange rate")
					return
				}

				// Convert product USD to MCGP: (usd * 1e6 * 1e18) / pricePerMCGP
				usdProductWei, _ := toWei(floatToDecimalStr(productTotal, 6), 6)
				productAmountWei = new(big.Int).Mul(usdProductWei, new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
				productAmountWei.Div(productAmountWei, pricePerMCGP)

				usdShipWei, _ := toWei(floatToDecimalStr(shippingTotal, 6), 6)
				shippingAmountWei = new(big.Int).Mul(usdShipWei, new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
				shippingAmountWei.Div(shippingAmountWei, pricePerMCGP)
			} else {
				// USDC/USDT: 1:1 with USD
				var err error
				productAmountWei, err = toWei(floatToDecimalStr(productTotal, decimals), decimals)
				if err != nil {
					tx.Rollback()
					utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to convert product amount")
					return
				}
				shippingAmountWei, err = toWei(floatToDecimalStr(shippingTotal, decimals), decimals)
				if err != nil {
					tx.Rollback()
					utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to convert shipping amount")
					return
				}
			}
			platformFeeWei := CalculatePlatformFee(productAmountWei, token)
			totalAmountWei := new(big.Int).Add(productAmountWei, shippingAmountWei)
			totalAmountWei.Add(totalAmountWei, platformFeeWei)

			// Determine buyer upline
			buyerUpline := ""
			if user.ReferredBy != nil {
				var referrer models.User
				if err := tx.First(&referrer, "id = ?", *user.ReferredBy).Error; err == nil {
					buyerUpline = referrer.WalletAddress
				}
			}

			order := models.Order{
				ID:              uuid.New(),
				BuyerID:         user.ID,
				SellerID:        group.sellerID,
				ProductID:       item.Product,
				Quantity:        item.Quantity,
				Token:           token,
				ProductAmount:   productAmountWei.String(),
				ShippingAmount:  shippingAmountWei.String(),
				PlatformFee:     platformFeeWei.String(),
				TotalAmount:     totalAmountWei.String(),
				ShippingZone:    zone,
				ShippingCity:    buyerCity,
				ShippingState:   buyerState,
				ShippingCountry: buyerCountry,
				BuyerUpline:     buyerUpline,
				Status:          models.OrderStatusPendingPayment,
				CreatedAt:       time.Now(),
				UpdatedAt:       time.Now(),
			}

			if err := tx.Create(&order).Error; err != nil {
				tx.Rollback()
				log.Printf("Failed to create order: %v", err)
				utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create order")
				return
			}

			orders = append(orders, order)
		}
	}

	// Mark cart as converted
	now := time.Now()
	cart.Status = "converted"
	cart.ConvertedAt = &now
	if err := tx.Save(&cart).Error; err != nil {
		tx.Rollback()
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update cart")
		return
	}

	if err := tx.Commit().Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to commit transaction")
		return
	}

	utils.SuccessResponse(c, http.StatusCreated, "Orders created successfully", gin.H{
		"orders": orders,
	})
}

// orderSystemFeeUSD computes the USD-denominated system fee for TP distribution.
// For MCGP orders, the buyer pays no platform fee or gas fee, so TP distributes 0.
func orderSystemFeeUSD(order *models.Order) float64 {
	platformFeeFloat := weiToUSDFloat(order.PlatformFee, order.Token)
	if strings.ToUpper(order.Token) == "MCGP" {
		return platformFeeFloat
	}
	return platformFeeFloat + models.GasFeeUSD
}

// getOrderForEscrow is a shared helper for prepare-approve and prepare-escrow.
func (ch *CheckoutHandler) getOrderForEscrow(c *gin.Context) (*models.Order, *models.User, *models.User, string, bool) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return nil, nil, nil, "", false
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return nil, nil, nil, "", false
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return nil, nil, nil, "", false
	}

	if order.BuyerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the buyer can prepare escrow")
		return nil, nil, nil, "", false
	}

	if !isValidTransition(order.Status, models.OrderStatusEscrowed) {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot prepare escrow: order is in %s status", order.Status))
		return nil, nil, nil, "", false
	}

	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return nil, nil, nil, "", false
	}

	var seller models.User
	if err := config.DB.First(&seller, "id = ?", order.SellerID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Seller not found")
		return nil, nil, nil, "", false
	}
	if seller.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Seller has no wallet address")
		return nil, nil, nil, "", false
	}

	tokenAddr := ch.BlockchainService.TokenAddress("sonic", order.Token)
	if tokenAddr == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Token %s not configured", order.Token))
		return nil, nil, nil, "", false
	}

	return &order, user, &seller, tokenAddr, true
}

// PrepareApprove handles POST /api/orders/:id/prepare-approve — returns the ERC20 approve tx.
func (ch *CheckoutHandler) PrepareApprove(c *gin.Context) {
	order, user, _, tokenAddr, ok := ch.getOrderForEscrow(c)
	if !ok {
		return
	}

	totalAmount, _ := new(big.Int).SetString(order.TotalAmount, 10)

	// Add gas fee to approve amount — contract collects product + shipping + platformFee + gasFee
	approveAmount := new(big.Int).Set(totalAmount)
	if strings.ToUpper(order.Token) != "MCGP" {
		decimals := 6
		gasFeeStr := fmt.Sprintf("%.0f", models.GasFeeUSD*math.Pow10(decimals))
		gasFeeWei, _ := new(big.Int).SetString(gasFeeStr, 10)
		approveAmount.Add(approveAmount, gasFeeWei)
	}

	client := ch.BlockchainService.ClientForChain("sonic")
	if client == nil {
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Blockchain service unavailable")
		return
	}

	approveTxBytes, err := client.PrepareERC20Approve(tokenAddr, user.WalletAddress, ch.Config.ProductEscrowAddress, approveAmount)
	if err != nil {
		log.Printf("Failed to prepare approve tx: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare approve transaction")
		return
	}

	var approveTx map[string]interface{}
	if err := json.Unmarshal(approveTxBytes, &approveTx); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode approve transaction")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Approve transaction prepared", gin.H{
		"approveTx": approveTx,
	})
}

// PrepareEscrow handles POST /api/orders/:id/prepare-escrow — returns the createOrder tx.
// Must be called AFTER the approve tx has been broadcast so gas estimation succeeds.
func (ch *CheckoutHandler) PrepareEscrow(c *gin.Context) {
	order, user, seller, tokenAddr, ok := ch.getOrderForEscrow(c)
	if !ok {
		return
	}

	productAmount, _ := new(big.Int).SetString(order.ProductAmount, 10)
	shippingAmount, _ := new(big.Int).SetString(order.ShippingAmount, 10)

	contractOrderID := services.GenerateOrderID(order.ID)
	upline := order.BuyerUpline
	if upline == "" {
		upline = "0x0000000000000000000000000000000000000000"
	}

	createOrderTxBytes, err := ch.EscrowService.PrepareCreateOrder(
		contractOrderID,
		user.WalletAddress,
		seller.WalletAddress,
		tokenAddr,
		productAmount,
		shippingAmount,
		upline,
	)
	if err != nil {
		log.Printf("Failed to prepare createOrder tx: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare escrow transaction")
		return
	}

	var createOrderTx map[string]interface{}
	if err := json.Unmarshal(createOrderTxBytes, &createOrderTx); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode escrow transaction")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Escrow transaction prepared", gin.H{
		"createOrderTx":   createOrderTx,
		"contractOrderId": fmt.Sprintf("0x%x", contractOrderID),
	})
}

// SubmitEscrow handles POST /api/orders/:id/submit-escrow
func (ch *CheckoutHandler) SubmitEscrow(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var req submitEscrowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "approveTxHash and escrowTxHash are required")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.BuyerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the buyer can submit escrow")
		return
	}

	if !isValidTransition(order.Status, models.OrderStatusEscrowed) {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot submit escrow: order is in %s status", order.Status))
		return
	}

	// Verify approve tx
	approveReceipt := ch.BlockchainService.GetTransactionReceipt(req.ApproveTxHash, "sonic")
	switch approveReceipt.Status {
	case "confirmed":
		// OK
	case "error":
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Unable to verify approve transaction — try again later")
		return
	case "pending":
		utils.ErrorResponse(c, http.StatusConflict, "Approve transaction is still pending — please wait and retry")
		return
	default:
		utils.ErrorResponse(c, http.StatusBadRequest, "Approve transaction failed on-chain")
		return
	}

	// Verify escrow tx and extract OrderCreated event
	contractOrderID, err := ch.EscrowService.VerifyEscrowCreated(req.EscrowTxHash)
	if err != nil {
		log.Printf("Failed to verify escrow tx: %v", err)
		utils.ErrorResponse(c, http.StatusBadRequest, "Escrow transaction verification failed: "+err.Error())
		return
	}

	escrowExpires := time.Now().Add(30 * 24 * time.Hour)
	updates := map[string]interface{}{
		"status":            models.OrderStatusEscrowed,
		"approve_tx_hash":   req.ApproveTxHash,
		"escrow_tx_hash":    req.EscrowTxHash,
		"contract_order_id": fmt.Sprintf("0x%x", contractOrderID),
		"escrow_expires_at": escrowExpires,
	}

	if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}

	ch.EventBus.Publish(events.Event{
		Type:    events.OrderEscrowed,
		UserID:  user.ID,
		Title:   "Order Escrowed",
		Message: fmt.Sprintf("Your payment for order %s has been escrowed", order.ID),
		Data: map[string]interface{}{
			"orderId":         order.ID.String(),
			"status":          models.OrderStatusEscrowed,
			"escrowExpiresAt": escrowExpires,
		},
	})

	utils.SuccessResponse(c, http.StatusOK, "Escrow submitted successfully", gin.H{
		"orderId":         order.ID,
		"status":          models.OrderStatusEscrowed,
		"contractOrderId": fmt.Sprintf("0x%x", contractOrderID),
		"escrowExpiresAt": escrowExpires,
	})
}

// shipRequest optional tracking info when marking order as shipped.
type shipRequest struct {
	TrackingNumber string `json:"trackingNumber"`
	Notes          string `json:"notes"`
}

// MarkShipped handles POST /api/orders/:id/ship
// Seller marks an escrowed order as shipped. Optional tracking number and notes.
func (ch *CheckoutHandler) MarkShipped(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var req shipRequest
	// Body is optional — ignore bind errors so seller can ship without tracking info
	_ = c.ShouldBindJSON(&req)

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.SellerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the seller can mark as shipped")
		return
	}

	if !isValidTransition(order.Status, models.OrderStatusShipped) {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot mark as shipped: order is in %s status", order.Status))
		return
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status":             models.OrderStatusShipped,
		"seller_shipped_at":  now,
	}
	if strings.TrimSpace(req.TrackingNumber) != "" {
		updates["tracking_number"] = strings.TrimSpace(req.TrackingNumber)
	}
	if strings.TrimSpace(req.Notes) != "" {
		updates["notes"] = strings.TrimSpace(req.Notes)
	}

	if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}

	shortOrderID := order.ID.String()[:8]
	shipMsg := fmt.Sprintf("Your order #%s has been shipped by the seller.", shortOrderID)
	if tn := strings.TrimSpace(req.TrackingNumber); tn != "" {
		shipMsg = fmt.Sprintf("Your order #%s has been shipped. Tracking: %s", shortOrderID, tn)
	}
	ch.EventBus.Publish(events.Event{
		Type:    events.OrderShipped,
		UserID:  order.BuyerID,
		Title:   "Order Shipped",
		Message: shipMsg,
		Data: map[string]interface{}{
			"orderId":        order.ID.String(),
			"status":         models.OrderStatusShipped,
			"trackingNumber": req.TrackingNumber,
		},
	})

	utils.SuccessResponse(c, http.StatusOK, "Order marked as shipped", gin.H{
		"orderId":         order.ID,
		"status":          models.OrderStatusShipped,
		"sellerShippedAt": now,
		"trackingNumber":  req.TrackingNumber,
	})
}

// MarkDelivered handles POST /api/orders/:id/deliver
func (ch *CheckoutHandler) MarkDelivered(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var req deliverRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "deliveryProofUrl is required")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.SellerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the seller can mark as delivered")
		return
	}

	if !isValidTransition(order.Status, models.OrderStatusDelivered) {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot mark as delivered: order is in %s status", order.Status))
		return
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status":              models.OrderStatusDelivered,
		"delivery_proof_url":  req.DeliveryProofURL,
		"seller_delivered_at": now,
	}

	if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}

	ch.EventBus.Publish(events.Event{
		Type:    events.OrderDelivered,
		UserID:  order.BuyerID,
		Title:   "Order Delivered",
		Message: fmt.Sprintf("Your order %s has been marked as delivered by the seller", order.ID),
		Data: map[string]interface{}{
			"orderId": order.ID.String(),
			"status":  models.OrderStatusDelivered,
		},
	})

	utils.SuccessResponse(c, http.StatusOK, "Order marked as delivered", gin.H{
		"orderId":           order.ID,
		"status":            models.OrderStatusDelivered,
		"sellerDeliveredAt": now,
	})
}

// refundDecisionRequest optional note when approving or rejecting a refund.
type refundDecisionRequest struct {
	Notes string `json:"notes"`
}

// ApproveRefund handles POST /api/orders/:id/approve-refund
// Merchant consents to refund. Order stays in refund_requested; admin executes on-chain.
// Sets merchant_approved_refund=true so admin dashboard knows.
func (ch *CheckoutHandler) ApproveRefund(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var req refundDecisionRequest
	_ = c.ShouldBindJSON(&req)

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.SellerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the seller can approve a refund")
		return
	}

	if order.Status != models.OrderStatusRefundRequested {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot approve refund: order is in %s status, must be %s", order.Status, models.OrderStatusRefundRequested))
		return
	}

	updates := map[string]interface{}{
		"merchant_approved_refund": true,
	}
	if strings.TrimSpace(req.Notes) != "" {
		updates["notes"] = strings.TrimSpace(req.Notes)
	}
	if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}

	ch.EventBus.Publish(events.Event{
		Type:    events.OrderRefundApproved,
		UserID:  order.BuyerID,
		Title:   "Refund Approved by Seller",
		Message: fmt.Sprintf("The seller has approved your refund request for order %s. An admin will finalize the refund shortly.", order.ID),
		Data: map[string]interface{}{
			"orderId": order.ID.String(),
			"status":  order.Status,
		},
	})

	utils.SuccessResponse(c, http.StatusOK, "Refund approved by seller", gin.H{
		"orderId":                order.ID,
		"status":                 order.Status,
		"merchantApprovedRefund": true,
	})
}

// RejectRefund handles POST /api/orders/:id/reject-refund
// Merchant disputes the refund request. Status returns to 'delivered' and the buyer
// can re-request or admin can manually resolve.
func (ch *CheckoutHandler) RejectRefund(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var req refundDecisionRequest
	_ = c.ShouldBindJSON(&req)

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.SellerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the seller can reject a refund")
		return
	}

	if order.Status != models.OrderStatusRefundRequested {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot reject refund: order is in %s status, must be %s", order.Status, models.OrderStatusRefundRequested))
		return
	}

	if !isValidTransition(order.Status, models.OrderStatusDelivered) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cannot transition back to delivered")
		return
	}

	updates := map[string]interface{}{
		"status":                   models.OrderStatusDelivered,
		"merchant_approved_refund": false,
	}
	if strings.TrimSpace(req.Notes) != "" {
		updates["notes"] = strings.TrimSpace(req.Notes)
	}
	if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}

	ch.EventBus.Publish(events.Event{
		Type:    events.OrderRefundRejected,
		UserID:  order.BuyerID,
		Title:   "Refund Rejected",
		Message: fmt.Sprintf("The seller has rejected your refund request for order %s. Contact support if you disagree.", order.ID),
		Data: map[string]interface{}{
			"orderId": order.ID.String(),
			"status":  models.OrderStatusDelivered,
		},
	})

	utils.SuccessResponse(c, http.StatusOK, "Refund rejected by seller", gin.H{
		"orderId": order.ID,
		"status":  models.OrderStatusDelivered,
	})
}

// PrepareConfirm handles POST /api/orders/:id/prepare-confirm
func (ch *CheckoutHandler) PrepareConfirm(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.BuyerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the buyer can confirm receipt")
		return
	}

	if !isValidTransition(order.Status, models.OrderStatusCompleted) {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot confirm receipt: order is in %s status", order.Status))
		return
	}

	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}

	contractOrderID := services.GenerateOrderID(order.ID)
	confirmTxBytes, err := ch.EscrowService.PrepareConfirmReceipt(contractOrderID, user.WalletAddress)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare confirm transaction")
		return
	}

	var confirmTx map[string]interface{}
	if err := json.Unmarshal(confirmTxBytes, &confirmTx); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode confirm transaction")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Confirm transaction prepared", gin.H{
		"confirmTx": confirmTx,
	})
}

// SubmitConfirm handles POST /api/orders/:id/submit-confirm
func (ch *CheckoutHandler) SubmitConfirm(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var req submitConfirmRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "txHash is required")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.BuyerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the buyer can confirm receipt")
		return
	}

	if !isValidTransition(order.Status, models.OrderStatusCompleted) {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot confirm receipt: order is in %s status", order.Status))
		return
	}

	// Verify tx
	receipt := ch.BlockchainService.GetTransactionReceipt(req.TxHash, "sonic")
	switch receipt.Status {
	case "confirmed":
		// OK
	case "error":
		utils.ErrorResponse(c, http.StatusServiceUnavailable, "Unable to verify confirm transaction — try again later")
		return
	case "pending":
		utils.ErrorResponse(c, http.StatusConflict, "Confirm transaction is still pending — please wait and retry")
		return
	default:
		utils.ErrorResponse(c, http.StatusBadRequest, "Confirm transaction failed on-chain")
		return
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status":             models.OrderStatusCompleted,
		"release_tx_hash":    req.TxHash,
		"buyer_confirmed_at": now,
	}

	if err := config.DB.Model(&order).Updates(updates).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
		return
	}

	if order.TPDistributedAt == nil {
		systemFeeUSD := orderSystemFeeUSD(&order)
		if err := DistributeTPEarnings(config.DB, order.BuyerID, "checkout", order.ID, systemFeeUSD); err != nil {
			log.Printf("TP distribution failed for order %s: %v", order.ID, err)
		} else {
			if err := config.DB.Model(&order).Update("tp_distributed_at", now).Error; err != nil {
				log.Printf("Failed to mark TP distributed for order %s: %v", order.ID, err)
			}
		}
	}

	ch.EventBus.Publish(events.Event{
		Type:    events.OrderCompleted,
		UserID:  order.SellerID,
		Title:   "Order Confirmed by Buyer",
		Message: fmt.Sprintf("The buyer confirmed receipt for order #%s. Funds have been released to your wallet.", order.ID.String()[:8]),
		Data: map[string]interface{}{
			"orderId": order.ID.String(),
			"status":  models.OrderStatusCompleted,
		},
	})

	utils.SuccessResponse(c, http.StatusOK, "Order completed", gin.H{
		"orderId":          order.ID,
		"status":           models.OrderStatusCompleted,
		"buyerConfirmedAt": now,
	})
}

// RequestRefund handles POST /api/orders/:id/request-refund
func (ch *CheckoutHandler) RequestRefund(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.BuyerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the buyer can request a refund")
		return
	}

	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}

	switch order.Status {
	case models.OrderStatusEscrowed:
		// Before delivery — prepare unsigned requestRefund tx
		contractOrderID := services.GenerateOrderID(order.ID)
		refundTxBytes, err := ch.EscrowService.PrepareRequestRefund(contractOrderID, user.WalletAddress)
		if err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare refund transaction")
			return
		}
		var refundTx map[string]interface{}
		if err := json.Unmarshal(refundTxBytes, &refundTx); err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode refund transaction")
			return
		}

		utils.SuccessResponse(c, http.StatusOK, "Refund transaction prepared", gin.H{
			"refundTx": refundTx,
		})

	case models.OrderStatusDelivered:
		// After delivery — update to refund_requested for admin review
		if err := config.DB.Model(&order).Update("status", models.OrderStatusRefundRequested).Error; err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update order")
			return
		}
		ch.EventBus.Publish(events.Event{
			Type:    events.OrderRefunded,
			UserID:  user.ID,
			Title:   "Refund Requested",
			Message: fmt.Sprintf("Your refund request for order %s is under admin review", order.ID),
			Data: map[string]interface{}{
				"orderId": order.ID.String(),
				"status":  models.OrderStatusRefundRequested,
			},
		})

		utils.SuccessResponse(c, http.StatusOK, "Refund requested for admin review", gin.H{
			"orderId": order.ID,
			"status":  models.OrderStatusRefundRequested,
		})

	default:
		utils.ErrorResponse(c, http.StatusBadRequest, "Refund can only be requested when order is escrowed or delivered")
	}
}

// CancelOrder handles POST /api/orders/:id/cancel
func (ch *CheckoutHandler) CancelOrder(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.SellerID != user.ID {
		utils.ErrorResponse(c, http.StatusForbidden, "Only the seller can cancel an order")
		return
	}

	if !isValidTransition(order.Status, models.OrderStatusCancelled) {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot cancel: order is in %s status", order.Status))
		return
	}

	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "No wallet address registered")
		return
	}

	contractOrderID := services.GenerateOrderID(order.ID)
	cancelTxBytes, err := ch.EscrowService.PrepareCancelOrder(contractOrderID, user.WalletAddress)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare cancel transaction")
		return
	}

	var cancelTx map[string]interface{}
	if err := json.Unmarshal(cancelTxBytes, &cancelTx); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode cancel transaction")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Cancel transaction prepared", gin.H{
		"cancelTx": cancelTx,
	})
}

// GetUserOrders handles GET /api/orders
func (ch *CheckoutHandler) GetUserOrders(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	status := c.Query("status")
	role := c.Query("role")

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	query := config.DB.Model(&models.Order{})

	switch role {
	case "buyer":
		query = query.Where("buyer_id = ?", user.ID)
	case "seller":
		query = query.Where("seller_id = ?", user.ID)
	default:
		query = query.Where("buyer_id = ? OR seller_id = ?", user.ID, user.ID)
	}

	if status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	query.Count(&total)

	var orders []models.Order
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&orders).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch orders")
		return
	}

	enriched := enrichOrdersWithPreview(orders)

	totalPages := int(total) / limit
	if int(total)%limit != 0 {
		totalPages++
	}

	utils.SuccessResponse(c, http.StatusOK, "Orders retrieved", gin.H{
		"orders": enriched,
		"pagination": gin.H{
			"page":       page,
			"limit":      limit,
			"total":      total,
			"totalPages": totalPages,
		},
	})
}

// orderPreview is an enriched order with summaries of related entities.
// Kept as a flat struct to preserve all model fields in JSON output.
type orderPreview struct {
	models.Order
	Buyer   *userPreview    `json:"buyer,omitempty"`
	Seller  *userPreview    `json:"seller,omitempty"`
	Product *productPreview `json:"product,omitempty"`
}

type userPreview struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Username string `json:"username,omitempty"`
	Email    string `json:"email,omitempty"`
}

type productPreview struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Price    float64 `json:"price"`
	ImageURL string  `json:"imageUrl,omitempty"`
}

// enrichOrdersWithPreview loads related buyer/seller/product summaries in a single
// query each to avoid N+1 issues. Returns orders with nested previews or unchanged
// if the related entity can't be found.
func enrichOrdersWithPreview(orders []models.Order) []orderPreview {
	if len(orders) == 0 {
		return []orderPreview{}
	}

	buyerIDs := make(map[uuid.UUID]struct{})
	sellerIDs := make(map[uuid.UUID]struct{})
	productIDs := make(map[uuid.UUID]struct{})
	for _, o := range orders {
		buyerIDs[o.BuyerID] = struct{}{}
		sellerIDs[o.SellerID] = struct{}{}
		productIDs[o.ProductID] = struct{}{}
	}

	toSlice := func(m map[uuid.UUID]struct{}) []uuid.UUID {
		out := make([]uuid.UUID, 0, len(m))
		for id := range m {
			out = append(out, id)
		}
		return out
	}

	var users []models.User
	allUserIDs := append(toSlice(buyerIDs), toSlice(sellerIDs)...)
	if len(allUserIDs) > 0 {
		_ = config.DB.Where("id IN ?", allUserIDs).Find(&users).Error
	}
	userByID := make(map[uuid.UUID]*userPreview, len(users))
	for i := range users {
		u := &users[i]
		userByID[u.ID] = &userPreview{
			ID:       u.ID.String(),
			Name:     strings.TrimSpace(u.Name),
			Username: u.Username,
			Email:    u.Email,
		}
	}

	var products []models.Product
	if len(productIDs) > 0 {
		_ = config.DB.Where("id IN ?", toSlice(productIDs)).Find(&products).Error
	}
	productByID := make(map[uuid.UUID]*productPreview, len(products))
	for i := range products {
		p := &products[i]
		productByID[p.ID] = &productPreview{
			ID:       p.ID.String(),
			Name:     p.Name,
			Price:    p.Price,
			ImageURL: firstProductImageURL(p),
		}
	}

	out := make([]orderPreview, len(orders))
	for i, o := range orders {
		out[i] = orderPreview{
			Order:   o,
			Buyer:   userByID[o.BuyerID],
			Seller:  userByID[o.SellerID],
			Product: productByID[o.ProductID],
		}
	}
	return out
}

// firstProductImageURL extracts the first non-empty image URL from a product's images JSON.
func firstProductImageURL(p *models.Product) string {
	if len(p.Images) == 0 {
		return ""
	}
	var imgs []map[string]interface{}
	if err := json.Unmarshal(p.Images, &imgs); err != nil {
		return ""
	}
	for _, img := range imgs {
		if u, ok := img["url"].(string); ok && u != "" {
			return u
		}
	}
	return ""
}

// GetOrderDetail handles GET /api/orders/:id
func (ch *CheckoutHandler) GetOrderDetail(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	// Check access: buyer, seller, or admin
	if order.BuyerID != user.ID && order.SellerID != user.ID &&
		user.Role != models.RoleAdmin && user.Role != models.RoleSuperAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Access denied")
		return
	}

	enriched := enrichOrdersWithPreview([]models.Order{order})
	utils.SuccessResponse(c, http.StatusOK, "Order retrieved", enriched[0])
}

// GetShippingEstimate handles GET /api/orders/shipping-estimate
func (ch *CheckoutHandler) GetShippingEstimate(c *gin.Context) {
	productIDStr := c.Query("productId")
	buyerCity := c.Query("buyerCity")
	buyerState := c.Query("buyerState")
	buyerCountry := c.Query("buyerCountry")

	if productIDStr == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "productId is required")
		return
	}

	productID, err := uuid.Parse(productIDStr)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	var product models.Product
	if err := config.DB.First(&product, "id = ?", productID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	// Get seller location
	var seller models.User
	if err := config.DB.First(&seller, "id = ?", product.UserID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Seller not found")
		return
	}

	origin := resolveShippingOrigin(&product, &seller)
	zone := DetectShippingZone(
		buyerCity,
		buyerState,
		buyerCountry,
		origin.City,
		origin.State,
		origin.Country,
	)
	shippingCost := GetShippingRate(&product, zone)

	utils.SuccessResponse(c, http.StatusOK, "Shipping estimate", gin.H{
		"zone":         zone,
		"shippingCost": shippingCost,
		"currency":     "USD",
	})
}

// AdminResolveDispute handles POST /api/admin/orders/:id/resolve
func (ch *CheckoutHandler) AdminResolveDispute(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid order ID")
		return
	}

	var req adminResolveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	var order models.Order
	if err := config.DB.First(&order, "id = ?", orderID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Order not found")
		return
	}

	if order.Status != models.OrderStatusRefundRequested {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Cannot resolve dispute: order is in %s status, must be %s", order.Status, models.OrderStatusRefundRequested))
		return
	}

	if user.WalletAddress == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Admin wallet address required")
		return
	}

	contractOrderID := services.GenerateOrderID(order.ID)
	resolveTxBytes, err := ch.EscrowService.PrepareAdminResolve(contractOrderID, req.RefundBuyer, user.WalletAddress)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to prepare resolve transaction")
		return
	}

	var resolveTx map[string]interface{}
	if err := json.Unmarshal(resolveTxBytes, &resolveTx); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode resolve transaction")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Resolve transaction prepared", gin.H{
		"resolveTx":   resolveTx,
		"refundBuyer": req.RefundBuyer,
	})
}

// GetAllOrders handles GET /api/admin/orders — admin paginated order list.
func (ch *CheckoutHandler) GetAllOrders(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	status := c.Query("status")
	search := c.Query("search")

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	query := config.DB.Model(&models.Order{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if search != "" {
		query = query.Where("id::text ILIKE ?", "%"+search+"%")
	}

	var total int64
	query.Count(&total)

	var orders []models.Order
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&orders).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to fetch orders")
		return
	}

	totalPages := int(total) / limit
	if int(total)%limit != 0 {
		totalPages++
	}

	utils.SuccessResponse(c, http.StatusOK, "Orders fetched successfully", gin.H{
		"orders": orders,
		"pagination": gin.H{
			"page":       page,
			"limit":      limit,
			"total":      total,
			"totalPages": totalPages,
		},
	})
}
