package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupCheckoutTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	config.DB = db

	// Create tables with SQLite-compatible SQL — wallet_address NOT unique (empty strings conflict)
	baseSqls := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			username TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL UNIQUE,
			password TEXT NOT NULL,
			role TEXT DEFAULT 'user',
			phone_number TEXT,
			country TEXT,
			state TEXT,
			city TEXT,
			address TEXT,
			profile_photo TEXT,
			referral_code TEXT,
			referred_by TEXT,
			documents TEXT,
			facial_verification TEXT,
			verification_status TEXT DEFAULT 'pending',
			verification_notes TEXT,
			account_status TEXT DEFAULT 'active',
			last_login DATETIME,
			login_attempts INTEGER DEFAULT 0,
			lock_until DATETIME,
			wallet_address TEXT,
			seed_phrase_backed_up INTEGER DEFAULT 0,
			deleted_at DATETIME,
			submitted_for_verification_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME
		)`,
	}
	for _, s := range baseSqls {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("failed to create base table: %v", err)
		}
	}

	// Add checkout-specific tables
	sqls := []string{
		`CREATE TABLE IF NOT EXISTS products (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT,
			description TEXT,
			location TEXT,
			phone_number TEXT,
			email TEXT,
			company_name TEXT,
			price REAL,
			stock INTEGER,
			category_id TEXT,
			category_name TEXT,
			images TEXT,
			attributes TEXT,
			status TEXT DEFAULT 'active',
			type TEXT DEFAULT 'Product',
			is_featured INTEGER DEFAULT 0,
			views INTEGER DEFAULT 0,
			sales INTEGER DEFAULT 0,
			rating TEXT,
			metadata TEXT,
			shipping_same_city REAL DEFAULT 0,
			shipping_same_state REAL DEFAULT 0,
			shipping_same_country REAL DEFAULT 0,
			shipping_international REAL DEFAULT 0,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS orders (
			id TEXT PRIMARY KEY,
			buyer_id TEXT NOT NULL,
			seller_id TEXT NOT NULL,
			product_id TEXT NOT NULL,
			quantity INTEGER DEFAULT 1,
			token TEXT NOT NULL,
			product_amount TEXT NOT NULL,
			shipping_amount TEXT DEFAULT '0',
			platform_fee TEXT DEFAULT '0',
			total_amount TEXT NOT NULL,
			shipping_zone TEXT,
			contract_order_id TEXT,
			escrow_tx_hash TEXT,
			approve_tx_hash TEXT,
			release_tx_hash TEXT,
			buyer_upline TEXT,
			delivery_proof_url TEXT,
			status TEXT DEFAULT 'pending_payment',
			buyer_confirmed_at DATETIME,
			seller_delivered_at DATETIME,
			escrow_expires_at DATETIME,
			notes TEXT,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS carts (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			items TEXT,
			summary TEXT,
			shipping_address TEXT,
			billing_address TEXT,
			payment_method TEXT,
			payment_details TEXT,
			applied_coupon TEXT,
			shipping_method TEXT,
			shipping_provider TEXT,
			estimated_delivery TEXT,
			session_id TEXT,
			currency TEXT,
			language TEXT,
			status TEXT DEFAULT 'active',
			last_activity DATETIME,
			abandoned_at DATETIME,
			converted_at DATETIME,
			expires_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME
		)`,
	}
	for _, s := range sqls {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("failed to create table: %v", err)
		}
	}

	return db
}

func setupCheckoutHandler(t *testing.T) *CheckoutHandler {
	t.Helper()
	cfg := &config.Config{
		ProductEscrowAddress:  "0x6c96B6EB227D1254247cD5015Bfc3e8Ade94415d",
		ServiceContactAddress: "0x3d761F72f4369e072767E830eE8Ce4c3A2144e6f",
		USDCTokenAddress:      "0x9f8AfF2706F52Ddb02921E245ec95Ade96767379",
		MCGPTokenAddress:      "0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7",
		Chains:                map[string]config.ChainConfig{},
		TokenAddresses: map[string]string{
			"sonic:USDC": "0x9f8AfF2706F52Ddb02921E245ec95Ade96767379",
			"sonic:MCGP": "0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7",
		},
	}
	// Create a blockchain service with no real clients (avoids RPC connections)
	bs := services.NewBlockchainService(cfg)
	es := services.NewEscrowService(nil, cfg)
	return NewCheckoutHandler(cfg, bs, es)
}

func createTestProduct(t *testing.T, db *gorm.DB, sellerID uuid.UUID, price float64) *models.Product {
	t.Helper()
	product := &models.Product{
		ID:                    uuid.New(),
		UserID:                sellerID,
		Name:                  "Test Product " + uuid.New().String()[:8],
		Price:                 price,
		Stock:                 100,
		Status:                "active",
		Type:                  "Product",
		ShippingSameCity:      0,
		ShippingSameState:     5.0,
		ShippingSameCountry:   10.0,
		ShippingInternational: 25.0,
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}
	if err := db.Create(product).Error; err != nil {
		t.Fatalf("failed to create product: %v", err)
	}
	return product
}

func createTestUserWithLocation(t *testing.T, db *gorm.DB, city, state, country string) *models.User {
	t.Helper()
	user := &models.User{
		ID:       uuid.New(),
		Name:     "Test User",
		Username: "testuser_" + uuid.New().String()[:8],
		Email:    "test_" + uuid.New().String()[:8] + "@example.com",
		Password: "$2a$10$fakehash",
		City:     city,
		State:    state,
		Country:  country,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	return user
}

func createTestCart(t *testing.T, db *gorm.DB, userID uuid.UUID, items []models.CartItem) *models.Cart {
	t.Helper()
	now := time.Now()
	cart := &models.Cart{
		ID:             uuid.New(),
		UserID:         userID,
		PaymentMethod:  "crypto",
		ShippingMethod: "standard",
		Currency:       "USD",
		Language:       "en",
		Status:         "active",
		LastActivity:   now,
		ExpiresAt:      now.Add(30 * 24 * time.Hour),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	cart.SetItems(items)
	cart.SetSummary(models.CartSummary{})
	if err := db.Create(cart).Error; err != nil {
		t.Fatalf("failed to create cart: %v", err)
	}
	return cart
}

func createTestOrder(t *testing.T, db *gorm.DB, buyerID, sellerID, productID uuid.UUID, status, token string) *models.Order {
	t.Helper()
	order := &models.Order{
		ID:             uuid.New(),
		BuyerID:        buyerID,
		SellerID:       sellerID,
		ProductID:      productID,
		Quantity:       1,
		Token:          token,
		ProductAmount:  "10000000",
		ShippingAmount: "5000000",
		PlatformFee:    "1000000",
		TotalAmount:    "16000000",
		ShippingZone:   "same_state",
		Status:         status,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatalf("failed to create order: %v", err)
	}
	return order
}

// ---------- Shipping zone detection tests ----------

func TestDetectShippingZone_SameCity(t *testing.T) {
	zone := DetectShippingZone("Lagos", "Lagos", "Nigeria", "Lagos", "Lagos", "Nigeria")
	if zone != ShippingZoneSameCity {
		t.Errorf("expected %s, got %s", ShippingZoneSameCity, zone)
	}
}

func TestDetectShippingZone_SameState(t *testing.T) {
	zone := DetectShippingZone("Ikeja", "Lagos", "Nigeria", "Lagos", "Lagos", "Nigeria")
	if zone != ShippingZoneSameState {
		t.Errorf("expected %s, got %s", ShippingZoneSameState, zone)
	}
}

func TestDetectShippingZone_SameCountry(t *testing.T) {
	zone := DetectShippingZone("Lagos", "Lagos", "Nigeria", "Abuja", "FCT", "Nigeria")
	if zone != ShippingZoneSameCountry {
		t.Errorf("expected %s, got %s", ShippingZoneSameCountry, zone)
	}
}

func TestDetectShippingZone_International(t *testing.T) {
	zone := DetectShippingZone("Lagos", "Lagos", "Nigeria", "London", "England", "UK")
	if zone != ShippingZoneInternational {
		t.Errorf("expected %s, got %s", ShippingZoneInternational, zone)
	}
}

func TestDetectShippingZone_MissingLocation(t *testing.T) {
	zone := DetectShippingZone("", "", "", "Lagos", "Lagos", "Nigeria")
	if zone != ShippingZoneInternational {
		t.Errorf("expected %s for missing location, got %s", ShippingZoneInternational, zone)
	}
}

func TestDetectShippingZone_CaseInsensitive(t *testing.T) {
	zone := DetectShippingZone("LAGOS", "LAGOS", "NIGERIA", "lagos", "lagos", "nigeria")
	if zone != ShippingZoneSameCity {
		t.Errorf("expected %s, got %s", ShippingZoneSameCity, zone)
	}
}

// ---------- Fee calculation tests ----------

func TestCalculatePlatformFee_USDC(t *testing.T) {
	productAmount := big.NewInt(10000000) // 10 USDC (6 decimals)
	fee := CalculatePlatformFee(productAmount, "USDC")
	expected := big.NewInt(1000000) // 10% = 1 USDC
	if fee.Cmp(expected) != 0 {
		t.Errorf("expected fee %s, got %s", expected.String(), fee.String())
	}
}

func TestCalculatePlatformFee_MCGP_Zero(t *testing.T) {
	productAmount := big.NewInt(10000000000000000) // 0.01 MCGP (18 decimals)
	fee := CalculatePlatformFee(productAmount, "MCGP")
	if fee.Sign() != 0 {
		t.Errorf("expected 0 fee for MCGP, got %s", fee.String())
	}
}

func TestCalculatePlatformFee_WeiMath(t *testing.T) {
	// 15.5 USDC = 15500000 (6 decimals)
	productAmount := big.NewInt(15500000)
	fee := CalculatePlatformFee(productAmount, "USDT")
	// 10% of 15500000 = 1550000
	expected := big.NewInt(1550000)
	if fee.Cmp(expected) != 0 {
		t.Errorf("expected fee %s, got %s", expected.String(), fee.String())
	}
}

// ---------- Order creation tests ----------

func TestCreateOrderFromCart_Success(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")
	product := createTestProduct(t, db, seller.ID, 10.0)

	items := []models.CartItem{
		{
			ID:       uuid.New(),
			Product:  product.ID,
			Seller:   seller.ID,
			Quantity: 2,
			Price:    10.0,
			AddedAt:  time.Now(),
		},
	}
	createTestCart(t, db, buyer.ID, items)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)

	body, _ := json.Marshal(map[string]interface{}{
		"token":        "USDC",
		"buyerCity":    "Lagos",
		"buyerState":   "Lagos",
		"buyerCountry": "Nigeria",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	ch.CreateOrderFromCart(c)

	if w.Code != http.StatusCreated {
		t.Errorf("expected %d, got %d: %s", http.StatusCreated, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	data := resp["data"].(map[string]interface{})
	orders := data["orders"].([]interface{})
	if len(orders) != 1 {
		t.Errorf("expected 1 order, got %d", len(orders))
	}

	order := orders[0].(map[string]interface{})
	if order["token"] != "USDC" {
		t.Errorf("expected token USDC, got %v", order["token"])
	}
	if order["shippingZone"] != "same_state" {
		t.Errorf("expected shipping zone same_state, got %v", order["shippingZone"])
	}
}

func TestCreateOrderFromCart_MultiSeller(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller1 := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller2 := createTestUserWithLocation(t, db, "Abuja", "FCT", "Nigeria")
	setUserWallet(t, db, seller1, "0x0000000000000000000000000000000000000010")
	setUserWallet(t, db, seller2, "0x0000000000000000000000000000000000000011")

	product1 := createTestProduct(t, db, seller1.ID, 10.0)
	product2 := createTestProduct(t, db, seller2.ID, 20.0)

	items := []models.CartItem{
		{ID: uuid.New(), Product: product1.ID, Seller: seller1.ID, Quantity: 1, Price: 10.0, AddedAt: time.Now()},
		{ID: uuid.New(), Product: product2.ID, Seller: seller2.ID, Quantity: 1, Price: 20.0, AddedAt: time.Now()},
	}
	createTestCart(t, db, buyer.ID, items)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)

	body, _ := json.Marshal(map[string]interface{}{
		"token": "USDC",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	ch.CreateOrderFromCart(c)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d: %s", http.StatusCreated, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	data := resp["data"].(map[string]interface{})
	orders := data["orders"].([]interface{})
	if len(orders) != 2 {
		t.Errorf("expected 2 orders (one per seller), got %d", len(orders))
	}
}

// ---------- Status transition tests ----------

func TestStatusTransitions_Valid(t *testing.T) {
	tests := []struct {
		from string
		to   string
		ok   bool
	}{
		{models.OrderStatusPendingPayment, models.OrderStatusEscrowed, true},
		{models.OrderStatusPendingPayment, models.OrderStatusCancelled, true},
		{models.OrderStatusEscrowed, models.OrderStatusDelivered, true},
		{models.OrderStatusEscrowed, models.OrderStatusRefunded, true},
		{models.OrderStatusEscrowed, models.OrderStatusCancelled, true},
		{models.OrderStatusDelivered, models.OrderStatusCompleted, true},
		{models.OrderStatusDelivered, models.OrderStatusRefundRequested, true},
		{models.OrderStatusRefundRequested, models.OrderStatusRefunded, true},
		{models.OrderStatusRefundRequested, models.OrderStatusCompleted, true},
		// Invalid transitions
		{models.OrderStatusPendingPayment, models.OrderStatusCompleted, false},
		{models.OrderStatusCompleted, models.OrderStatusPendingPayment, false},
		{models.OrderStatusRefunded, models.OrderStatusEscrowed, false},
		{models.OrderStatusCancelled, models.OrderStatusEscrowed, false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s->%s", tt.from, tt.to), func(t *testing.T) {
			valid := models.ValidNextStatuses(tt.from)
			found := false
			for _, s := range valid {
				if s == tt.to {
					found = true
					break
				}
			}
			if found != tt.ok {
				t.Errorf("transition %s->%s: expected ok=%v, got ok=%v", tt.from, tt.to, tt.ok, found)
			}
		})
	}
}

// ---------- Auth checks ----------

func TestMarkDelivered_OnlySellerCanDeliver(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusEscrowed, "USDC")

	// Try as buyer (should fail)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}

	body, _ := json.Marshal(map[string]interface{}{
		"deliveryProofUrl": "https://example.com/proof.jpg",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	ch.MarkDelivered(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected %d for buyer attempting deliver, got %d: %s", http.StatusForbidden, w.Code, w.Body.String())
	}

	// Try as seller (should succeed)
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Set("user", seller)
	c2.Params = gin.Params{{Key: "id", Value: order.ID.String()}}

	body2, _ := json.Marshal(map[string]interface{}{
		"deliveryProofUrl": "https://example.com/proof.jpg",
	})
	c2.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body2))
	c2.Request.Header.Set("Content-Type", "application/json")

	ch.MarkDelivered(c2)

	if w2.Code != http.StatusOK {
		t.Errorf("expected %d for seller delivering, got %d: %s", http.StatusOK, w2.Code, w2.Body.String())
	}
}

func TestPrepareConfirm_OnlyBuyerCanConfirm(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, buyer, "0x0000000000000000000000000000000000000001")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusDelivered, "USDC")

	// Try as seller (should fail)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", seller)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}
	c.Request, _ = http.NewRequest("POST", "/", nil)

	ch.PrepareConfirm(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected %d for seller attempting confirm, got %d: %s", http.StatusForbidden, w.Code, w.Body.String())
	}

	// Try as buyer (should succeed)
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Set("user", buyer)
	c2.Params = gin.Params{{Key: "id", Value: order.ID.String()}}
	c2.Request, _ = http.NewRequest("POST", "/", nil)

	ch.PrepareConfirm(c2)

	if w2.Code != http.StatusOK {
		t.Errorf("expected %d for buyer confirming, got %d: %s", http.StatusOK, w2.Code, w2.Body.String())
	}
}

func TestCancelOrder_OnlySellerCanCancel(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000003")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusEscrowed, "USDC")

	// Try as buyer (should fail)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}
	c.Request, _ = http.NewRequest("POST", "/", nil)

	ch.CancelOrder(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected %d for buyer attempting cancel, got %d: %s", http.StatusForbidden, w.Code, w.Body.String())
	}
}

func TestAdminResolveDispute_RequiresRefundRequestedStatus(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	admin := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	admin.Role = models.RoleAdmin
	admin.WalletAddress = "0x0000000000000000000000000000000000000099"
	db.Save(admin)

	product := createTestProduct(t, db, seller.ID, 10.0)

	// Test with escrowed order (should fail)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusEscrowed, "USDC")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", admin)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}

	body, _ := json.Marshal(map[string]interface{}{"refundBuyer": true})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	ch.AdminResolveDispute(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d for non-disputed order, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

// ---------- Prepare escrow tests ----------

func TestPrepareEscrow_ReturnsUnsignedTx(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, buyer, "0x0000000000000000000000000000000000000001")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")

	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusPendingPayment, "USDC")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}
	c.Request, _ = http.NewRequest("POST", "/", nil)

	ch.PrepareEscrow(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	data := resp["data"].(map[string]interface{})

	// Check createOrderTx has proper fields
	createOrderTx, ok := data["createOrderTx"].(map[string]interface{})
	if !ok {
		t.Fatal("expected createOrderTx in response")
	}
	if createOrderTx["to"] == nil {
		t.Error("expected createOrderTx to have 'to' field")
	}
	if createOrderTx["data"] == nil {
		t.Error("expected createOrderTx to have 'data' field (ABI-encoded)")
	}

	// Check contractOrderId is present
	if data["contractOrderId"] == nil {
		t.Error("expected contractOrderId in response")
	}
}

// ---------- GetUserOrders ----------

func TestGetUserOrders_FilterByRole(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	user1 := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	user2 := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	product := createTestProduct(t, db, user2.ID, 10.0)

	// Create orders where user1 is buyer
	createTestOrder(t, db, user1.ID, user2.ID, product.ID, models.OrderStatusEscrowed, "USDC")
	createTestOrder(t, db, user1.ID, user2.ID, product.ID, models.OrderStatusPendingPayment, "USDC")

	// Create order where user1 is seller
	product2 := createTestProduct(t, db, user1.ID, 5.0)
	createTestOrder(t, db, user2.ID, user1.ID, product2.ID, models.OrderStatusEscrowed, "USDC")

	// Get all orders for user1
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user1)
	c.Request, _ = http.NewRequest("GET", "/?page=1&limit=20", nil)

	ch.GetUserOrders(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	orders := data["orders"].([]interface{})
	if len(orders) != 3 {
		t.Errorf("expected 3 orders total, got %d", len(orders))
	}

	// Filter by role=buyer
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Set("user", user1)
	c2.Request, _ = http.NewRequest("GET", "/?role=buyer", nil)

	ch.GetUserOrders(c2)

	var resp2 map[string]interface{}
	json.Unmarshal(w2.Body.Bytes(), &resp2)
	data2 := resp2["data"].(map[string]interface{})
	orders2 := data2["orders"].([]interface{})
	if len(orders2) != 2 {
		t.Errorf("expected 2 buyer orders, got %d", len(orders2))
	}
}

// ---------- GetShippingEstimate ----------

func TestGetShippingEstimate(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("GET", fmt.Sprintf("/?productId=%s&buyerCity=Ikeja&buyerState=Lagos&buyerCountry=Nigeria", product.ID), nil)

	ch.GetShippingEstimate(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})

	if data["zone"] != "same_state" {
		t.Errorf("expected zone same_state, got %v", data["zone"])
	}
	if data["shippingCost"].(float64) != 5.0 {
		t.Errorf("expected shipping cost 5.0, got %v", data["shippingCost"])
	}
}

// ---------- Order detail auth ----------

func TestGetOrderDetail_AccessControl(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	other := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusEscrowed, "USDC")

	// Access as unrelated user (should fail)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", other)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}
	c.Request, _ = http.NewRequest("GET", "/", nil)

	ch.GetOrderDetail(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected %d for unrelated user, got %d", http.StatusForbidden, w.Code)
	}

	// Access as buyer (should succeed)
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Set("user", buyer)
	c2.Params = gin.Params{{Key: "id", Value: order.ID.String()}}
	c2.Request, _ = http.NewRequest("GET", "/", nil)

	ch.GetOrderDetail(c2)

	if w2.Code != http.StatusOK {
		t.Errorf("expected %d for buyer, got %d", http.StatusOK, w2.Code)
	}
}

// ---------- GenerateOrderID ----------

func TestGenerateOrderID_Deterministic(t *testing.T) {
	id := uuid.New()
	result1 := services.GenerateOrderID(id)
	result2 := services.GenerateOrderID(id)
	if result1 != result2 {
		t.Error("expected deterministic order ID generation")
	}

	// Different UUID should produce different order ID
	id2 := uuid.New()
	result3 := services.GenerateOrderID(id2)
	if result1 == result3 {
		t.Error("expected different order IDs for different UUIDs")
	}
}
