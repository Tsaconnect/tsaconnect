package handlers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// getActiveCart retrieves the active cart for a user, or returns nil.
func getActiveCart(ctx context.Context, userID primitive.ObjectID) (*models.Cart, error) {
	col := config.GetCollection("carts")
	var cart models.Cart
	err := col.FindOne(ctx, bson.M{"user": userID, "status": "active"}).Decode(&cart)
	if err != nil {
		return nil, err
	}
	return &cart, nil
}

// recalcAndSave recalculates the cart summary and saves it.
func recalcAndSave(ctx context.Context, cart *models.Cart) error {
	col := config.GetCollection("carts")
	cart.Summary = cart.CalculateSummary()
	now := time.Now()
	cart.LastActivity = now
	cart.UpdatedAt = now

	// Reset expiry if cart is active and non-empty
	if cart.Status == "active" && len(cart.Items) > 0 {
		cart.ExpiresAt = time.Now().Add(30 * 24 * time.Hour)
	}

	_, err := col.ReplaceOne(ctx, bson.M{"_id": cart.ID}, cart)
	return err
}

// GetOrCreateCart handles GET /api/cart/ - returns the user's active cart or creates a new one.
func (h *Handlers) GetOrCreateCart(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cart, err := getActiveCart(ctx, user.ID)
	if err != nil {
		// Create new cart
		now := time.Now()
		newCart := models.Cart{
			User:           user.ID,
			Items:          []models.CartItem{},
			Summary:        models.CartSummary{},
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

		col := config.GetCollection("carts")
		result, err := col.InsertOne(ctx, newCart)
		if err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create cart")
			return
		}
		newCart.ID = result.InsertedID.(primitive.ObjectID)
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

	productOID, err := primitive.ObjectIDFromHex(body.ProductID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid product ID")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get product details
	prodCol := config.GetCollection("products")
	var product models.Product
	err = prodCol.FindOne(ctx, bson.M{"_id": productOID}).Decode(&product)
	if err != nil {
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
	cart, err := getActiveCart(ctx, user.ID)
	if err != nil {
		now := time.Now()
		newCart := models.Cart{
			User:           user.ID,
			Items:          []models.CartItem{},
			Summary:        models.CartSummary{},
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
		col := config.GetCollection("carts")
		result, err := col.InsertOne(ctx, newCart)
		if err != nil {
			utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to create cart")
			return
		}
		newCart.ID = result.InsertedID.(primitive.ObjectID)
		cart = &newCart
	}

	// Check if item already exists
	existingIndex := -1
	for i, item := range cart.Items {
		if item.Product == productOID {
			existingIndex = i
			break
		}
	}

	now := time.Now()
	if existingIndex >= 0 {
		cart.Items[existingIndex].Quantity += body.Quantity
		cart.Items[existingIndex].UpdatedAt = now
	} else {
		cart.Items = append(cart.Items, models.CartItem{
			ID:                 primitive.NewObjectID(),
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

	if err := recalcAndSave(ctx, cart); err != nil {
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

	itemID, err := primitive.ObjectIDFromHex(c.Param("itemId"))
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cart, err := getActiveCart(ctx, user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	// Find item in cart
	itemIndex := -1
	for i, item := range cart.Items {
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
	prodCol := config.GetCollection("products")
	var product models.Product
	err = prodCol.FindOne(ctx, bson.M{"_id": cart.Items[itemIndex].Product}).Decode(&product)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Product not found")
		return
	}

	if body.Quantity > product.Stock {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Only %d items available in stock", product.Stock))
		return
	}

	if body.Quantity < 1 {
		// Remove item
		cart.Items = append(cart.Items[:itemIndex], cart.Items[itemIndex+1:]...)
	} else {
		cart.Items[itemIndex].Quantity = body.Quantity
		cart.Items[itemIndex].UpdatedAt = time.Now()
	}

	if err := recalcAndSave(ctx, cart); err != nil {
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

	itemID, err := primitive.ObjectIDFromHex(c.Param("itemId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid item ID")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cart, err := getActiveCart(ctx, user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	found := false
	newItems := make([]models.CartItem, 0, len(cart.Items))
	for _, item := range cart.Items {
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

	cart.Items = newItems
	if err := recalcAndSave(ctx, cart); err != nil {
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cart, err := getActiveCart(ctx, user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	cart.Items = []models.CartItem{}
	if err := recalcAndSave(ctx, cart); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to clear cart")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Cart cleared successfully", gin.H{
		"items":   []models.CartItem{},
		"summary": cart.Summary,
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cart, err := getActiveCart(ctx, user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	if len(cart.Items) == 0 {
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
	for _, item := range cart.Items {
		subtotal += item.Price * float64(item.Quantity)
	}

	if subtotal < couponData.MinPurchase {
		utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Minimum purchase of $%.0f required for this coupon", couponData.MinPurchase))
		return
	}

	cart.AppliedCoupon = &couponData
	if err := recalcAndSave(ctx, cart); err != nil {
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cart, err := getActiveCart(ctx, user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	cart.AppliedCoupon = nil
	if err := recalcAndSave(ctx, cart); err != nil {
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cart, err := getActiveCart(ctx, user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	cart.ShippingAddress = &address
	if err := recalcAndSave(ctx, cart); err != nil {
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cart, err := getActiveCart(ctx, user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	if body.SameAsShipping && cart.ShippingAddress != nil {
		body = models.BillingAddress{
			SameAsShipping: true,
			Name:           cart.ShippingAddress.Name,
			PhoneNumber:    cart.ShippingAddress.PhoneNumber,
			Address:        cart.ShippingAddress.Address,
			City:           cart.ShippingAddress.City,
			State:          cart.ShippingAddress.State,
			Country:        cart.ShippingAddress.Country,
			PostalCode:     cart.ShippingAddress.PostalCode,
		}
	}

	cart.BillingAddress = &body
	if err := recalcAndSave(ctx, cart); err != nil {
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
		Method            string                  `json:"method"`
		Provider          string                  `json:"provider"`
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cart, err := getActiveCart(ctx, user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	cart.ShippingMethod = body.Method
	cart.ShippingProvider = body.Provider
	if body.EstimatedDelivery != nil {
		cart.EstimatedDelivery = body.EstimatedDelivery
	}

	// Update shipping cost
	shippingCosts := map[string]float64{
		"standard": 5.00,
		"express":  12.00,
		"next_day": 25.00,
		"pickup":   0.00,
	}
	cart.Summary.Shipping = shippingCosts[body.Method]

	if err := recalcAndSave(ctx, cart); err != nil {
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
		Method  string                `json:"method"`
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cart, err := getActiveCart(ctx, user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	cart.PaymentMethod = body.Method
	if body.Details != nil {
		cart.PaymentDetails = body.Details
	}

	if err := recalcAndSave(ctx, cart); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to update payment method")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Payment method updated successfully", cart)
}

// ConvertToOrder handles POST /api/cart/checkout - validates and converts the cart to an order.
func (h *Handlers) ConvertToOrder(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cart, err := getActiveCart(ctx, user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	if len(cart.Items) == 0 {
		utils.ErrorResponse(c, http.StatusBadRequest, "Cannot convert empty cart to order")
		return
	}

	if cart.ShippingAddress == nil || cart.ShippingAddress.Address == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Shipping address is required")
		return
	}

	if cart.PaymentMethod == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Payment method is required")
		return
	}

	// Check product availability
	prodCol := config.GetCollection("products")
	for _, item := range cart.Items {
		var product models.Product
		err := prodCol.FindOne(ctx, bson.M{"_id": item.Product}).Decode(&product)
		if err != nil {
			utils.ErrorResponse(c, http.StatusBadRequest, "A product in your cart is no longer available")
			return
		}
		if product.Stock < item.Quantity {
			utils.ErrorResponse(c, http.StatusBadRequest, fmt.Sprintf("Only %d items available for \"%s\"", product.Stock, product.Name))
			return
		}
	}

	// Save order data before clearing
	orderData := gin.H{
		"orderId":           fmt.Sprintf("ORD-%d", time.Now().UnixMilli()),
		"cartId":            cart.ID,
		"summary":           cart.Summary,
		"items":             cart.Items,
		"shippingAddress":   cart.ShippingAddress,
		"billingAddress":    cart.BillingAddress,
		"paymentMethod":     cart.PaymentMethod,
		"shippingMethod":    cart.ShippingMethod,
		"estimatedDelivery": cart.EstimatedDelivery,
		"createdAt":         time.Now(),
	}

	// Convert cart
	now := time.Now()
	cart.Status = "converted"
	cart.ConvertedAt = &now
	cart.Items = []models.CartItem{}
	cart.AppliedCoupon = nil
	cart.Summary = cart.CalculateSummary()
	cart.LastActivity = now
	cart.UpdatedAt = now

	col := config.GetCollection("carts")
	_, err = col.ReplaceOne(ctx, bson.M{"_id": cart.ID}, cart)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to convert cart to order")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Order created successfully", orderData)
}

// ValidateCart handles GET /api/cart/validate - validates all cart items.
func (h *Handlers) ValidateCart(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Authentication required")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cart, err := getActiveCart(ctx, user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	if len(cart.Items) == 0 {
		utils.SuccessResponse(c, http.StatusOK, "Cart is empty", gin.H{
			"valid":  true,
			"issues": []interface{}{},
		})
		return
	}

	prodCol := config.GetCollection("products")
	var validationResults []gin.H
	var issues []gin.H

	for _, item := range cart.Items {
		var product models.Product
		err := prodCol.FindOne(ctx, bson.M{"_id": item.Product}).Decode(&product)

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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cart, err := getActiveCart(ctx, user.ID)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Cart not found")
		return
	}

	// Group items by seller
	sellerGroups := make(map[string]gin.H)
	for _, item := range cart.Items {
		sellerID := item.Seller.Hex()
		if _, ok := sellerGroups[sellerID]; !ok {
			sellerGroups[sellerID] = gin.H{
				"seller":   item.Seller,
				"items":    []models.CartItem{},
				"subtotal": 0.0,
			}
		}
		entry := sellerGroups[sellerID]
		entry["items"] = append(entry["items"].([]models.CartItem), item)
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
		"summary":         cart.Summary,
		"appliedCoupon":   cart.AppliedCoupon,
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

	cartID, err := primitive.ObjectIDFromHex(c.Param("cartId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid cart ID")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	col := config.GetCollection("carts")

	var cart models.Cart
	err = col.FindOne(ctx, bson.M{
		"_id":    cartID,
		"user":   user.ID,
		"status": "abandoned",
	}).Decode(&cart)
	if err != nil {
		utils.ErrorResponse(c, http.StatusNotFound, "Abandoned cart not found")
		return
	}

	now := time.Now()
	cart.Status = "active"
	cart.AbandonedAt = nil
	cart.LastActivity = now
	cart.UpdatedAt = now

	_, err = col.ReplaceOne(ctx, bson.M{"_id": cart.ID}, cart)
	if err != nil {
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

	filter := bson.M{
		"status":       "abandoned",
		"lastActivity": bson.M{"$lt": cutoffDate},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	col := config.GetCollection("carts")

	total, err := col.CountDocuments(ctx, filter)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to retrieve abandoned carts")
		return
	}

	skip := int64((page - 1) * limit)
	opts := options.Find().
		SetSort(bson.D{{Key: "lastActivity", Value: -1}}).
		SetSkip(skip).
		SetLimit(int64(limit))

	cursor, err := col.Find(ctx, filter, opts)
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to retrieve abandoned carts")
		return
	}
	defer cursor.Close(ctx)

	var carts []models.Cart
	if err := cursor.All(ctx, &carts); err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to decode carts")
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

	cartID, err := primitive.ObjectIDFromHex(c.Param("cartId"))
	if err != nil {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid cart ID")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	col := config.GetCollection("carts")

	var cart models.Cart
	err = col.FindOne(ctx, bson.M{"_id": cartID}).Decode(&cart)
	if err != nil {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	col := config.GetCollection("carts")

	result, err := col.DeleteMany(ctx, bson.M{
		"expiresAt": bson.M{"$lt": time.Now()},
		"status":    bson.M{"$ne": "converted"},
	})
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to cleanup expired carts")
		return
	}

	utils.SuccessResponse(c, http.StatusOK, "Expired carts cleaned up successfully", gin.H{
		"deletedCount": result.DeletedCount,
		"timestamp":    time.Now(),
	})
}
