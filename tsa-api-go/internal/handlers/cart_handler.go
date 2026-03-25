package handlers

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// getActiveCart retrieves the active cart for a user, or returns nil.
func getActiveCart(userID uuid.UUID) (*models.Cart, error) {
	var cart models.Cart
	if err := config.DB.Where("user_id = ? AND status = ?", userID, "active").First(&cart).Error; err != nil {
		return nil, err
	}
	return &cart, nil
}

// recalcAndSave recalculates the cart summary and saves it.
func recalcAndSave(cart *models.Cart) error {
	items := cart.GetItems()
	coupon := cart.GetAppliedCoupon()
	currentSummary := cart.GetSummary()

	summary := models.CalculateCartSummary(items, coupon, currentSummary.Shipping)
	cart.SetSummary(summary)

	now := time.Now()
	cart.LastActivity = now
	cart.UpdatedAt = now

	// Reset expiry if cart is active and non-empty
	if cart.Status == "active" && len(items) > 0 {
		cart.ExpiresAt = time.Now().Add(30 * 24 * time.Hour)
	}

	return config.DB.Save(cart).Error
}

// GetOrCreateCart handles GET /api/cart/ - returns the user's active cart or creates a new one.
func (h *Handlers) GetOrCreateCart(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	cart, err := getActiveCart(user.ID)
	if err != nil {
		// Create new cart
		now := time.Now()
		newCart := models.Cart{
			ID:             uuid.New(),
			UserID:         user.ID,
			PaymentMethod:  "card",
			ShippingMethod: "standard",
			Currency:       "USD",
			Language:       "en",
			Status:         "active",
			LastActivity:   now,
			ExpiresAt:      now.Add(30 * 24 * time.Hour),
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		newCart.SetItems([]models.CartItem{})
		newCart.SetSummary(models.CartSummary{})

		if err := config.DB.Create(&newCart).Error; err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create cart")
			return
		}
		cart = &newCart
	}

	utils.SuccessResponse(c, http.StatusOK, "Cart retrieved successfully", cart)
}

// AddToCart handles POST /api/cart/items - adds an item to the user's cart.
func (h *Handlers) AddToCart(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	var body struct {
		ProductID          string                     `json:"productId"`
		Quantity           int                        `json:"quantity"`
		SelectedAttributes []models.CartItemAttribute `json:"selectedAttributes"`
		Notes              string                     `json:"notes"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	if body.ProductID == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Product ID is required")
		return
	}

	if body.Quantity <= 0 {
		body.Quantity = 1
	}

	productOID, err := uuid.Parse(body.ProductID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	// Get product details
	var product models.Product
	if err := config.DB.First(&product, "id = ?", productOID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	if product.Status != "active" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Product is not available for purchase")
		return
	}

	if product.Stock < body.Quantity {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Only %d items available in stock", product.Stock))
		return
	}

	// Get or create cart
	cart, err := getActiveCart(user.ID)
	if err != nil {
		now := time.Now()
		newCart := models.Cart{
			ID:             uuid.New(),
			UserID:         user.ID,
			PaymentMethod:  "card",
			ShippingMethod: "standard",
			Currency:       "USD",
			Language:       "en",
			Status:         "active",
			LastActivity:   now,
			ExpiresAt:      now.Add(30 * 24 * time.Hour),
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		newCart.SetItems([]models.CartItem{})
		newCart.SetSummary(models.CartSummary{})

		if err := config.DB.Create(&newCart).Error; err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create cart")
			return
		}
		cart = &newCart
	}

	// Check if item already exists
	items := cart.GetItems()
	existingIndex := -1
	for i, item := range items {
		if item.Product == productOID {
			existingIndex = i
			break
		}
	}

	now := time.Now()
	if existingIndex >= 0 {
		items[existingIndex].Quantity += body.Quantity
		items[existingIndex].UpdatedAt = now
	} else {
		items = append(items, models.CartItem{
			ID:                 uuid.New(),
			Product:            productOID,
			Seller:             product.UserID,
			Quantity:           body.Quantity,
			Price:              product.Price,
			SelectedAttributes: body.SelectedAttributes,
			Notes:              body.Notes,
			AddedAt:            now,
			UpdatedAt:          now,
		})
	}

	cart.SetItems(items)

	if err := recalcAndSave(cart); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update cart")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Item added to cart successfully", cart)
}

// UpdateCartItem handles PUT /api/cart/items/:itemId - updates an item's quantity.
func (h *Handlers) UpdateCartItem(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	itemID, err := uuid.Parse(c.Param("itemId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid item ID")
		return
	}

	var body struct {
		Quantity int `json:"quantity"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Quantity < 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Valid quantity is required")
		return
	}

	cart, err := getActiveCart(user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	// Find item in cart
	items := cart.GetItems()
	itemIndex := -1
	for i, item := range items {
		if item.ID == itemID {
			itemIndex = i
			break
		}
	}
	if itemIndex == -1 {
		utils.ErrorResponse(c, http.StatusNotFound, "Item not found in cart")
		return
	}

	// Check stock
	var product models.Product
	if err := config.DB.First(&product, "id = ?", items[itemIndex].Product).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	if body.Quantity > product.Stock {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Only %d items available in stock", product.Stock))
		return
	}

	if body.Quantity < 1 {
		// Remove item
		items = append(items[:itemIndex], items[itemIndex+1:]...)
	} else {
		items[itemIndex].Quantity = body.Quantity
		items[itemIndex].UpdatedAt = time.Now()
	}

	cart.SetItems(items)

	if err := recalcAndSave(cart); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update cart")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Cart updated successfully", cart)
}

// RemoveFromCart handles DELETE /api/cart/items/:itemId - removes an item from the cart.
func (h *Handlers) RemoveFromCart(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	itemID, err := uuid.Parse(c.Param("itemId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid item ID")
		return
	}

	cart, err := getActiveCart(user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	items := cart.GetItems()
	found := false
	newItems := make([]models.CartItem, 0, len(items))
	for _, item := range items {
		if item.ID == itemID {
			found = true
			continue
		}
		newItems = append(newItems, item)
	}

	if !found {
		utils.ErrorResponse(c, http.StatusNotFound, "Item not found in cart")
		return
	}

	cart.SetItems(newItems)
	if err := recalcAndSave(cart); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update cart")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Item removed from cart successfully", cart)
}

// ClearCart handles DELETE /api/cart/clear - removes all items from the cart.
func (h *Handlers) ClearCart(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	cart, err := getActiveCart(user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	cart.SetItems([]models.CartItem{})
	if err := recalcAndSave(cart); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to clear cart")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Cart cleared successfully", gin.H{
		"items":   []models.CartItem{},
		"summary": cart.GetSummary(),
	})
}

// ApplyCoupon handles POST /api/cart/coupon - applies a coupon code to the cart.
func (h *Handlers) ApplyCoupon(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	var body struct {
		CouponCode string `json:"couponCode"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.CouponCode == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Coupon code is required")
		return
	}

	cart, err := getActiveCart(user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	items := cart.GetItems()
	if len(items) == 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cannot apply coupon to empty cart")
		return
	}

	// Mock coupon validation
	couponData := models.AppliedCoupon{
		Code:          body.CouponCode,
		DiscountType:  "percentage",
		DiscountValue: 10,
		MaxDiscount:   50,
		MinPurchase:   100,
		ExpiresAt:     time.Now().Add(30 * 24 * time.Hour),
		AppliedAt:     time.Now(),
	}

	// Recalculate subtotal to check min purchase
	subtotal := 0.0
	for _, item := range items {
		subtotal += item.Price * float64(item.Quantity)
	}

	if subtotal < couponData.MinPurchase {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Minimum purchase of $%.0f required for this coupon", couponData.MinPurchase))
		return
	}

	cart.SetAppliedCoupon(&couponData)
	if err := recalcAndSave(cart); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to apply coupon")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Coupon applied successfully", cart)
}

// RemoveCoupon handles DELETE /api/cart/coupon - removes the applied coupon.
func (h *Handlers) RemoveCoupon(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	cart, err := getActiveCart(user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	cart.SetAppliedCoupon(nil)
	if err := recalcAndSave(cart); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to remove coupon")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Coupon removed successfully", cart)
}

// UpdateShippingAddress handles PUT /api/cart/shipping-address - updates the shipping address.
func (h *Handlers) UpdateShippingAddress(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	var address models.Address
	if err := c.ShouldBindJSON(&address); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	if address.Name == "" || address.PhoneNumber == "" || address.Address == "" || address.City == "" || address.Country == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Missing required address fields")
		return
	}

	cart, err := getActiveCart(user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	addrJSON, _ := json.Marshal(address)
	cart.ShippingAddress = addrJSON
	if err := recalcAndSave(cart); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update shipping address")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Shipping address updated successfully", cart)
}

// UpdateBillingAddress handles PUT /api/cart/billing-address - updates the billing address.
func (h *Handlers) UpdateBillingAddress(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	var body models.BillingAddress
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	cart, err := getActiveCart(user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	if body.SameAsShipping && cart.ShippingAddress != nil {
		var shippingAddr models.Address
		if json.Unmarshal(cart.ShippingAddress, &shippingAddr) == nil {
			body = models.BillingAddress{
				SameAsShipping: true,
				Name:           shippingAddr.Name,
				PhoneNumber:    shippingAddr.PhoneNumber,
				Address:        shippingAddr.Address,
				City:           shippingAddr.City,
				State:          shippingAddr.State,
				Country:        shippingAddr.Country,
				PostalCode:     shippingAddr.PostalCode,
			}
		}
	}

	billingJSON, _ := json.Marshal(body)
	cart.BillingAddress = billingJSON
	if err := recalcAndSave(cart); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update billing address")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Billing address updated successfully", cart)
}

// UpdateShippingMethod handles PUT /api/cart/shipping-method - updates the shipping method.
func (h *Handlers) UpdateShippingMethod(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	var body struct {
		Method            string                    `json:"method"`
		Provider          string                    `json:"provider"`
		EstimatedDelivery *models.EstimatedDelivery `json:"estimatedDelivery"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	validMethods := map[string]bool{
		"standard": true, "express": true, "next_day": true, "pickup": true,
	}
	if !validMethods[body.Method] {
		utils.ErrorResponse(c, http.StatusBadRequest, "Valid shipping method is required")
		return
	}

	cart, err := getActiveCart(user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	cart.ShippingMethod = body.Method
	cart.ShippingProvider = body.Provider
	if body.EstimatedDelivery != nil {
		edJSON, _ := json.Marshal(body.EstimatedDelivery)
		cart.EstimatedDelivery = edJSON
	}

	// Update shipping cost
	shippingCosts := map[string]float64{
		"standard": 5.00,
		"express":  12.00,
		"next_day": 25.00,
		"pickup":   0.00,
	}
	summary := cart.GetSummary()
	summary.Shipping = shippingCosts[body.Method]
	cart.SetSummary(summary)

	if err := recalcAndSave(cart); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update shipping method")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Shipping method updated successfully", cart)
}

// UpdatePaymentMethod handles PUT /api/cart/payment-method - updates the payment method.
func (h *Handlers) UpdatePaymentMethod(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	var body struct {
		Method  string                 `json:"method"`
		Details *models.PaymentDetails `json:"details"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	validMethods := map[string]bool{
		"card": true, "bank_transfer": true, "wallet": true, "cash_on_delivery": true, "crypto": true,
	}
	if !validMethods[body.Method] {
		utils.ErrorResponse(c, http.StatusBadRequest, "Valid payment method is required")
		return
	}

	cart, err := getActiveCart(user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	cart.PaymentMethod = body.Method
	if body.Details != nil {
		detailsJSON, _ := json.Marshal(body.Details)
		cart.PaymentDetails = detailsJSON
	}

	if err := recalcAndSave(cart); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update payment method")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Payment method updated successfully", cart)
}

// ConvertToOrder handles POST /api/cart/checkout - redirects to the new escrow-based checkout.
// Deprecated: Use POST /api/orders instead for escrow-based checkout.
func (h *Handlers) ConvertToOrder(c *gin.Context) {
	utils.ErrorResponse(c, http.StatusGone, "This endpoint is deprecated. Use POST /api/orders for escrow-based checkout")
}

// ValidateCart handles GET /api/cart/validate - validates all cart items.
func (h *Handlers) ValidateCart(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	cart, err := getActiveCart(user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	items := cart.GetItems()
	if len(items) == 0 {
		utils.SuccessResponse(c, http.StatusOK, "Cart is empty", gin.H{
			"valid":  true,
			"issues": []interface{}{},
		})
		return
	}

	var validationResults []gin.H
	var issues []gin.H

	for _, item := range items {
		var product models.Product
		err := config.DB.First(&product, "id = ?", item.Product).Error

		validation := gin.H{
			"productId":         item.Product,
			"requestedQuantity": item.Quantity,
			"price":             item.Price,
			"issues":            []string{},
		}

		var itemIssues []string

		if err != nil {
			itemIssues = append(itemIssues, "Product no longer available")
			issues = append(issues, gin.H{
				"productId": item.Product,
				"issue":     "product_unavailable",
			})
		} else {
			validation["name"] = product.Name
			validation["availableStock"] = product.Stock
			validation["currentPrice"] = product.Price
			validation["isAvailable"] = product.Status == "active"

			if product.Stock < item.Quantity {
				msg := fmt.Sprintf("Only %d items available in stock", product.Stock)
				itemIssues = append(itemIssues, msg)
				issues = append(issues, gin.H{
					"productId":   item.Product,
					"productName": product.Name,
					"issue":       "insufficient_stock",
					"available":   product.Stock,
					"requested":   item.Quantity,
				})
			}

			if product.Status != "active" {
				itemIssues = append(itemIssues, "Product is not available for purchase")
				issues = append(issues, gin.H{
					"productId":   item.Product,
					"productName": product.Name,
					"issue":       "product_inactive",
					"status":      product.Status,
				})
			}

			if item.Price != product.Price {
				msg := fmt.Sprintf("Price has changed from $%.2f to $%.2f", item.Price, product.Price)
				itemIssues = append(itemIssues, msg)
				issues = append(issues, gin.H{
					"productId":   item.Product,
					"productName": product.Name,
					"issue":       "price_changed",
					"oldPrice":    item.Price,
					"newPrice":    product.Price,
				})
			}
		}

		validation["issues"] = itemIssues
		validation["isValid"] = len(itemIssues) == 0
		validationResults = append(validationResults, validation)
	}

	isValid := len(issues) == 0
	message := "Cart is valid"
	if !isValid {
		message = "Cart validation issues found"
	}

	responseData := gin.H{
		"valid":             isValid,
		"validationResults": validationResults,
		"issues":            issues,
	}
	if isValid {
		responseData["cart"] = cart
	}

	utils.SuccessResponse(c, http.StatusOK, message, responseData)
}

// GetCartSummary handles GET /api/cart/summary - returns cart summary with items grouped by seller.
func (h *Handlers) GetCartSummary(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	cart, err := getActiveCart(user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	items := cart.GetItems()

	// Collect unique product and seller IDs for batch lookup
	productIDs := make([]uuid.UUID, 0, len(items))
	sellerIDs := make([]uuid.UUID, 0, len(items))
	seenProducts := make(map[uuid.UUID]bool)
	seenSellers := make(map[uuid.UUID]bool)
	for _, item := range items {
		if !seenProducts[item.Product] {
			productIDs = append(productIDs, item.Product)
			seenProducts[item.Product] = true
		}
		if !seenSellers[item.Seller] {
			sellerIDs = append(sellerIDs, item.Seller)
			seenSellers[item.Seller] = true
		}
	}

	// Batch fetch products
	productMap := make(map[string]gin.H)
	if len(productIDs) > 0 {
		var products []models.Product
		config.DB.Where("id IN ?", productIDs).Find(&products)
		for _, p := range products {
			productMap[p.ID.String()] = gin.H{
				"_id":    p.ID,
				"name":   p.Name,
				"price":  p.Price,
				"stock":  p.Stock,
				"images": p.GetImages(),
			}
		}
	}

	// Batch fetch sellers (users)
	sellerMap := make(map[string]gin.H)
	if len(sellerIDs) > 0 {
		var users []models.User
		config.DB.Where("id IN ?", sellerIDs).Find(&users)
		for _, u := range users {
			sellerMap[u.ID.String()] = gin.H{
				"_id":   u.ID,
				"name":  u.Name,
				"email": u.Email,
			}
		}
	}

	// Build enriched items grouped by seller
	type enrichedItem struct {
		models.CartItem
		ProductData gin.H `json:"productData,omitempty"`
	}

	sellerGroups := make(map[string]gin.H)
	for _, item := range items {
		sellerID := item.Seller.String()
		if _, ok := sellerGroups[sellerID]; !ok {
			seller := sellerMap[sellerID]
			if seller == nil {
				seller = gin.H{"_id": item.Seller, "name": "Seller", "email": ""}
			}
			sellerGroups[sellerID] = gin.H{
				"seller":   seller,
				"items":    []gin.H{},
				"subtotal": 0.0,
			}
		}
		entry := sellerGroups[sellerID]

		enriched := gin.H{
			"_id":                item.ID,
			"product":           productMap[item.Product.String()],
			"seller":            sellerMap[sellerID],
			"quantity":          item.Quantity,
			"price":             item.Price,
			"selectedAttributes": item.SelectedAttributes,
			"notes":             item.Notes,
			"addedAt":           item.AddedAt,
			"updatedAt":         item.UpdatedAt,
		}

		entry["items"] = append(entry["items"].([]gin.H), enriched)
		entry["subtotal"] = entry["subtotal"].(float64) + item.Price*float64(item.Quantity)
		sellerGroups[sellerID] = entry
	}

	itemsBySeller := make([]gin.H, 0, len(sellerGroups))
	for _, group := range sellerGroups {
		itemsBySeller = append(itemsBySeller, group)
	}

	utils.SuccessResponse(c, http.StatusOK, "Cart summary retrieved successfully", gin.H{
		"cart":            cart,
		"itemsBySeller":   itemsBySeller,
		"summary":         cart.GetSummary(),
		"feeConfig":       models.GetFeeConfig(),
		"appliedCoupon":   cart.GetAppliedCoupon(),
		"shippingAddress": cart.ShippingAddress,
		"billingAddress":  cart.BillingAddress,
		"paymentMethod":   cart.PaymentMethod,
		"shippingMethod":  cart.ShippingMethod,
	})
}

// RestoreCart handles POST /api/cart/restore/:cartId - restores an abandoned cart.
func (h *Handlers) RestoreCart(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	cartID, err := uuid.Parse(c.Param("cartId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid cart ID")
		return
	}

	var cart models.Cart
	if err := config.DB.Where("id = ? AND user_id = ? AND status = ?", cartID, user.ID, "abandoned").First(&cart).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Abandoned cart not found")
		return
	}

	now := time.Now()
	cart.Status = "active"
	cart.AbandonedAt = nil
	cart.LastActivity = now
	cart.UpdatedAt = now

	if err := config.DB.Save(&cart).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to restore cart")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Cart restored successfully", cart)
}

// GetAbandonedCarts handles GET /api/cart/admin/abandoned - returns abandoned carts (admin only).
func (h *Handlers) GetAbandonedCarts(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	if user.Role != models.RoleAdmin && user.Role != models.RoleSuperAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Access denied. Admin privileges required.")
		return
	}

	days, _ := strconv.Atoi(c.DefaultQuery("days", "1"))
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}

	cutoffDate := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

	query := config.DB.Model(&models.Cart{}).
		Where("status = ? AND last_activity < ?", "abandoned", cutoffDate)

	var total int64
	if err := query.Count(&total).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to retrieve abandoned carts")
		return
	}

	offset := (page - 1) * limit

	var carts []models.Cart
	if err := query.Order("last_activity DESC").Offset(offset).Limit(limit).Find(&carts).Error; err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to retrieve abandoned carts")
		return
	}

	totalPages := int(math.Ceil(float64(total) / float64(limit)))

	utils.SuccessResponse(c, http.StatusOK, "Abandoned carts retrieved successfully", gin.H{
		"carts": carts,
		"pagination": gin.H{
			"page":        page,
			"limit":       limit,
			"total":       total,
			"totalPages":  totalPages,
			"hasNextPage": page < totalPages,
			"hasPrevPage": page > 1,
		},
		"filters": gin.H{
			"days":       days,
			"cutoffDate": cutoffDate,
		},
	})
}

// GetCartByID handles GET /api/cart/admin/:cartId - returns a specific cart (admin only).
func (h *Handlers) GetCartByID(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	if user.Role != models.RoleAdmin && user.Role != models.RoleSuperAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Access denied. Admin privileges required.")
		return
	}

	cartID, err := uuid.Parse(c.Param("cartId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid cart ID")
		return
	}

	var cart models.Cart
	if err := config.DB.First(&cart, "id = ?", cartID).Error; err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Cart retrieved successfully", cart)
}

// CleanupExpiredCarts handles DELETE /api/cart/admin/cleanup - removes expired carts (admin only).
func (h *Handlers) CleanupExpiredCarts(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	if user.Role != models.RoleAdmin && user.Role != models.RoleSuperAdmin {
		utils.ErrorResponse(c, http.StatusForbidden, "Access denied. Admin privileges required.")
		return
	}

	result := config.DB.Where("expires_at < ? AND status != ?", time.Now(), "converted").Delete(&models.Cart{})
	if result.Error != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to cleanup expired carts")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Expired carts cleaned up successfully", gin.H{
		"deletedCount": result.RowsAffected,
		"timestamp":    time.Now(),
	})
}
