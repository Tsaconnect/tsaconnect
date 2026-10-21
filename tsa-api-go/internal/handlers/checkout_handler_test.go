package handlers

import (
	"bytes"
	"context"
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
	"github.com/ojimcy/tsa-api-go/internal/events"
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
				didit_session_id TEXT,
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
				mute_notifications INTEGER DEFAULT 0,
				mute_email INTEGER DEFAULT 0,
				tp_balance REAL DEFAULT 0,
				cashback_balance REAL DEFAULT 0,
				last_settled_global_index INTEGER,
				email_verified INTEGER DEFAULT 0,
				deleted_at DATETIME,
				submitted_for_verification_at DATETIME,
				created_at DATETIME,
			updated_at DATETIME,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME, declared_given_names TEXT, declared_surname TEXT
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
			shipping_residential REAL DEFAULT 0,
			shipping_same_city REAL DEFAULT 0,
			shipping_same_state REAL DEFAULT 0,
			shipping_same_country REAL DEFAULT 0,
			shipping_international REAL DEFAULT 0,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME, declared_given_names TEXT, declared_surname TEXT,
			promo_enabled INTEGER DEFAULT 0, promo_required_referrals INTEGER DEFAULT 0, promo_countdown_seconds INTEGER, promo_price REAL DEFAULT 0, promo_version INTEGER DEFAULT 0,
			deleted_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS orders (
			id TEXT PRIMARY KEY,
			buyer_id TEXT NOT NULL,
			seller_id TEXT NOT NULL,
			product_id TEXT NOT NULL,
			kind TEXT DEFAULT 'product',
			quantity INTEGER DEFAULT 1,
			token TEXT NOT NULL,
			product_amount TEXT NOT NULL,
			shipping_amount TEXT DEFAULT '0',
			platform_fee TEXT DEFAULT '0',
			total_amount TEXT NOT NULL,
			shipping_zone TEXT,
			shipping_city TEXT,
			shipping_state TEXT,
			shipping_country TEXT,
			shipping_recipient_name TEXT,
			shipping_phone TEXT,
			shipping_street TEXT,
			contract_order_id TEXT,
			escrow_contract_address TEXT,
			escrow_seller_wallet TEXT,
			escrow_tx_hash TEXT,
			approve_tx_hash TEXT,
			release_tx_hash TEXT, release_reverted_at DATETIME,
			buyer_upline TEXT,
			delivery_proof_url TEXT,
			tracking_number TEXT,
			merchant_approved_refund INTEGER DEFAULT 0,
			cancel_reason TEXT,
			cancel_requested_at DATETIME,
			merchant_approved_cancel INTEGER DEFAULT 0,
			disputed_at DATETIME,
			dispute_reason TEXT,
			dispute_raised_by TEXT,
			status TEXT DEFAULT 'pending_payment',
			buyer_confirmed_at DATETIME,
			seller_shipped_at DATETIME,
			seller_delivered_at DATETIME,
			escrow_expires_at DATETIME,
			tp_distributed_at DATETIME,
			notes TEXT,
			menu_selections TEXT,
			selected_attributes TEXT,
			purchase_note TEXT,
			service_answers TEXT,
			payment_source TEXT DEFAULT '',
			checkout_group_id TEXT,
			fund_with_swap BOOLEAN NOT NULL DEFAULT 0,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		orderMcgpPaymentsDDL,
		`CREATE TABLE IF NOT EXISTS delivery_addresses (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			label TEXT,
			recipient_name TEXT NOT NULL,
			phone TEXT NOT NULL,
			street TEXT NOT NULL,
			city TEXT NOT NULL,
			state TEXT NOT NULL,
			country TEXT NOT NULL,
			is_default INTEGER DEFAULT 0,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		// Mirrors the Postgres partial index: at most one default per user.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_delivery_addresses_one_default
			ON delivery_addresses (user_id) WHERE is_default = 1`,
		`CREATE TABLE IF NOT EXISTS categories (
			id TEXT PRIMARY KEY,
			title TEXT,
			description TEXT,
			type TEXT,
			service_fee_mode TEXT DEFAULT 'standard',
			service_type TEXT DEFAULT 'contact',
			parent_category_id TEXT,
			icon TEXT,
			image TEXT,
			color TEXT,
			is_active INTEGER DEFAULT 1,
			sort_order INTEGER DEFAULT 0,
			product_count INTEGER DEFAULT 0,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS category_booking_fees (
			id TEXT PRIMARY KEY,
			category_id TEXT NOT NULL,
			tier TEXT NOT NULL,
			amount_usd REAL DEFAULT 0,
			created_at DATETIME,
			updated_at DATETIME,
			UNIQUE(category_id, tier)
		)`,
		`CREATE TABLE IF NOT EXISTS service_contact_payments (
			id TEXT PRIMARY KEY,
			caller_id TEXT NOT NULL,
			service_provider_id TEXT,
			service_id TEXT NOT NULL,
			token TEXT,
			fee_amount TEXT,
			approve_tx_hash TEXT,
			pay_fee_tx_hash TEXT,
			created_at DATETIME
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
		`CREATE TABLE IF NOT EXISTS service_profiles (
			id TEXT PRIMARY KEY,
			product_id TEXT NOT NULL,
			access_fee_usd REAL DEFAULT 0,
			callout_fee_usd REAL DEFAULT 0,
			slot_duration_minutes INTEGER DEFAULT 0,
			max_capacity_per_slot INTEGER DEFAULT 1,
			service_address TEXT,
			coverage_area TEXT,
			operating_company TEXT,
			timezone TEXT DEFAULT 'Africa/Lagos',
			cancellation_policy TEXT,
			reschedule_policy TEXT,
			refund_policy TEXT,
			booking_rules TEXT,
			booking_type TEXT DEFAULT 'instant',
			pricing_method TEXT DEFAULT 'fixed',
			workflow TEXT DEFAULT 'auto_accept',
			payment_timing TEXT DEFAULT 'pay_before',
			deposit_percent INTEGER DEFAULT 0,
			booking_fee_usd REAL DEFAULT 0,
			field_config TEXT,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS service_menu_items (
			id TEXT PRIMARY KEY,
			product_id TEXT NOT NULL,
			name TEXT NOT NULL,
			description TEXT,
			price_usd REAL DEFAULT 0,
			image_url TEXT,
			is_available INTEGER DEFAULT 1,
			sort_order INTEGER DEFAULT 0,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS service_availabilities (
			id TEXT PRIMARY KEY,
			product_id TEXT NOT NULL,
			day_of_week INTEGER NOT NULL,
			start_time TEXT NOT NULL,
			end_time TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS service_bookings (
			id TEXT PRIMARY KEY,
			order_id TEXT NOT NULL,
			product_id TEXT NOT NULL,
			buyer_id TEXT NOT NULL,
			slot_start DATETIME NOT NULL,
			slot_end DATETIME NOT NULL,
			party_size INTEGER DEFAULT 1,
			status TEXT DEFAULT 'reserved',
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS booking_requests (
			id TEXT PRIMARY KEY,
			product_id TEXT NOT NULL,
			buyer_id TEXT NOT NULL,
			seller_id TEXT NOT NULL,
			token TEXT,
			service_tier TEXT,
			buyer_city TEXT,
			buyer_state TEXT,
			buyer_country TEXT,
			answers TEXT,
			status TEXT DEFAULT 'requested',
			reject_reason TEXT,
			quoted_amount_usd REAL,
			quote_expires_at DATETIME,
			order_id TEXT,
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
		ProductEscrowAddress:   "0x6c96B6EB227D1254247cD5015Bfc3e8Ade94415d",
		ServiceContractAddress: "0x3d761F72f4369e072767E830eE8Ce4c3A2144e6f",
		USDCTokenAddress:       "0x9f8AfF2706F52Ddb02921E245ec95Ade96767379",
		MCGPTokenAddress:       "0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7",
		Chains:                 map[string]config.ChainConfig{},
		TokenAddresses: map[string]string{
			"sonic:USDC": "0x9f8AfF2706F52Ddb02921E245ec95Ade96767379",
			"sonic:MCGP": "0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7",
		},
		// BlockchainService.TokenAddress resolves via Networks["mainnet"], NOT the
		// top-level TokenAddresses map above (which predates multi-network config).
		// Without this, every token lookup returns "" and handlers fail with
		// "Token X not configured" before reaching the behaviour under test.
		Networks: map[string]config.NetworkConfig{
			"mainnet": {
				Chains: map[string]config.ChainConfig{},
				TokenAddresses: map[string]string{
					"sonic:USDC": "0x9f8AfF2706F52Ddb02921E245ec95Ade96767379",
					"sonic:MCGP": "0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7",
				},
			},
		},
	}
	// Create a blockchain service with no real clients (avoids RPC connections)
	bs := services.NewBlockchainService(cfg)
	es := services.NewEscrowService(nil, cfg)
	otc := services.NewOTCService(nil, cfg)
	return NewCheckoutHandler(cfg, bs, es, events.NewBus(), otc)
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
	if zone != ShippingZoneSameCountry {
		t.Errorf("expected %s for missing location, got %s", ShippingZoneSameCountry, zone)
	}
}

func TestDetectShippingZone_CaseInsensitive(t *testing.T) {
	zone := DetectShippingZone("LAGOS", "LAGOS", "NIGERIA", "lagos", "lagos", "nigeria")
	if zone != ShippingZoneSameCity {
		t.Errorf("expected %s, got %s", ShippingZoneSameCity, zone)
	}
}

func TestDetectShippingZone_StateSuffixNormalization(t *testing.T) {
	zone := DetectShippingZone("Ankpa", "Kogi State", "Nigeria", "Lokoja", "Kogi", "Nigeria")
	if zone != ShippingZoneSameState {
		t.Errorf("expected %s, got %s", ShippingZoneSameState, zone)
	}
}

// ---------- Fee calculation tests ----------

// Without a chain read (no escrow client in tests), checkout falls back to the
// fee_config replica, pinned to the 2% product_merchant rate and $0.10 gas.
// If the configured rates change, these SHOULD fail: that is a product
// decision, not an incidental refactor.
func TestResolvePricingRates_FallsBackToFeeConfig(t *testing.T) {
	setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	ch.feeRates = nil

	for _, token := range []string{"USDC", "usdt"} {
		r, perr := ch.resolvePricingRates(context.Background(), token)
		if perr != nil {
			t.Fatalf("%s: %v", token, perr.msg)
		}
		if r.feeBPS != 200 || r.gasFee.String() != "100000" || r.decimals != 6 || r.pricePerMCGP != nil {
			t.Errorf("%s: fee %d gas %s decimals %d, want 200 / 100000 / 6", token, r.feeBPS, r.gasFee, r.decimals)
		}
	}
}

func TestEscrowFeeOn_WeiMath(t *testing.T) {
	// 2% of 15.5 USDC (15500000 at 6dp) = 310000, rounded down like the contract.
	if got := escrowFeeOn(big.NewInt(15500000), 200); got.String() != "310000" {
		t.Errorf("fee = %s, want 310000", got)
	}
	if got := escrowFeeOn(big.NewInt(49), 200); got.Sign() != 0 {
		t.Errorf("fee on 49 base units = %s, want 0 (rounded down)", got)
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

func TestCreateOrderFromCart_UsesProductLocationForShipping(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Ankpa", "Kogi State", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Abuja", "FCT", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")
	product := createTestProduct(t, db, seller.ID, 10.0)
	product.Location = "Lokoja, Kogi, Nigeria"
	product.ShippingSameState = 5.0
	product.ShippingSameCountry = 0
	if err := db.Save(product).Error; err != nil {
		t.Fatalf("failed to update product location: %v", err)
	}

	items := []models.CartItem{
		{
			ID:       uuid.New(),
			Product:  product.ID,
			Seller:   seller.ID,
			Quantity: 1,
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
		"buyerCity":    "Ankpa",
		"buyerState":   "Kogi State",
		"buyerCountry": "Nigeria",
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
	if len(orders) != 1 {
		t.Fatalf("expected 1 order, got %d", len(orders))
	}

	order := orders[0].(map[string]interface{})
	if order["shippingZone"] != ShippingZoneSameState {
		t.Errorf("expected shipping zone %s, got %v", ShippingZoneSameState, order["shippingZone"])
	}
	if order["shippingAmount"] != "5000000" {
		t.Errorf("expected shipping amount 5000000, got %v", order["shippingAmount"])
	}
}

func TestGetCartSummary_IncludesMerchantShippingMetadata(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	product.Location = "Ikeja, Lagos, Nigeria"
	if err := db.Save(product).Error; err != nil {
		t.Fatalf("failed to update product location: %v", err)
	}

	items := []models.CartItem{
		{
			ID:       uuid.New(),
			Product:  product.ID,
			Seller:   seller.ID,
			Quantity: 1,
			Price:    10.0,
			AddedAt:  time.Now(),
		},
	}
	createTestCart(t, db, buyer.ID, items)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Request, _ = http.NewRequest("GET", "/summary", nil)

	h.GetCartSummary(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	data := resp["data"].(map[string]interface{})
	itemsBySeller := data["itemsBySeller"].([]interface{})
	if len(itemsBySeller) != 1 {
		t.Fatalf("expected 1 seller group, got %d", len(itemsBySeller))
	}

	group := itemsBySeller[0].(map[string]interface{})
	enrichedItems := group["items"].([]interface{})
	if len(enrichedItems) != 1 {
		t.Fatalf("expected 1 enriched item, got %d", len(enrichedItems))
	}

	enriched := enrichedItems[0].(map[string]interface{})
	productData := enriched["product"].(map[string]interface{})
	sellerData := enriched["seller"].(map[string]interface{})

	if productData["location"] != "Ikeja, Lagos, Nigeria" {
		t.Errorf("expected product location to be included, got %v", productData["location"])
	}
	if productData["shippingSameState"] != 5.0 {
		t.Errorf("expected shippingSameState 5.0, got %v", productData["shippingSameState"])
	}
	if productData["shippingInternational"] != 25.0 {
		t.Errorf("expected shippingInternational 25.0, got %v", productData["shippingInternational"])
	}
	if sellerData["city"] != "Ikeja" {
		t.Errorf("expected seller city Ikeja, got %v", sellerData["city"])
	}
	if sellerData["country"] != "Nigeria" {
		t.Errorf("expected seller country Nigeria, got %v", sellerData["country"])
	}
	// Any buyer can put any listing in a cart, so the summary must not hand
	// out the seller's login email.
	if _, ok := sellerData["email"]; ok {
		t.Errorf("seller login email leaked in cart summary: %v", sellerData["email"])
	}
	if groupSeller, _ := group["seller"].(map[string]interface{}); groupSeller != nil {
		if _, ok := groupSeller["email"]; ok {
			t.Errorf("seller login email leaked in seller group: %v", groupSeller["email"])
		}
	}
}

func TestGetCartCount_SumsItemQuantities(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)

	items := []models.CartItem{
		{ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 2, Price: 10.0, AddedAt: time.Now()},
		{ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 3, Price: 10.0, AddedAt: time.Now()},
	}
	createTestCart(t, db, buyer.ID, items)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Request, _ = http.NewRequest("GET", "/count", nil)

	h.GetCartCount(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	if data["count"] != float64(5) {
		t.Errorf("expected count 5 (2+3), got %v", data["count"])
	}
}

func TestGetCartCount_NoActiveCartReturnsZero(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Request, _ = http.NewRequest("GET", "/count", nil)

	h.GetCartCount(c)

	// No active cart must be 0/200, not 404 — the badge endpoint degrades gracefully.
	if w.Code != http.StatusOK {
		t.Fatalf("expected %d (no cart -> 0, not 404), got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	if data["count"] != float64(0) {
		t.Errorf("expected count 0, got %v", data["count"])
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

	// An escrow order is delivered only with the seller's on-chain markDelivered
	// (a hash, or the escrow's own flag on a retry); without a readable chain
	// there is no proof, so it is refused rather than recorded.
	if w2.Code != http.StatusServiceUnavailable {
		t.Errorf("expected %d for a delivery that cannot be proven, got %d: %s", http.StatusServiceUnavailable, w2.Code, w2.Body.String())
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

// TestPrepareApprove_Returns503WhenNoClient pins the "no blockchain client →
// 503" guard on the endpoint that still has one.
//
// This test previously targeted PrepareEscrow, which no longer touches
// BlockchainService: it delegates to EscrowService.PrepareCreateOrder, which
// builds calldata offline and needs no RPC. So PrepareEscrow now legitimately
// returns 200 without a client, and the assertion was failing against a guard
// that had moved rather than a regression. PrepareApprove still needs a client
// (nonce + gas estimation for the ERC-20 approve), so the guard — and this
// coverage — belongs there.
func TestPrepareApprove_Returns503WhenNoClient(t *testing.T) {
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

	ch.PrepareApprove(c)

	// Without a blockchain client, should return 503 Service Unavailable
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected %d, got %d: %s", http.StatusServiceUnavailable, w.Code, w.Body.String())
	}
}

func TestPrepareEscrow_ValidationChecks(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusPendingPayment, "USDC")

	// Test without wallet address
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}
	c.Request, _ = http.NewRequest("POST", "/", nil)

	ch.PrepareEscrow(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d for no wallet, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}

	// Test wrong status
	escrowedOrder := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusEscrowed, "USDC")
	setUserWallet(t, db, buyer, "0x0000000000000000000000000000000000000001")

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Set("user", buyer)
	c2.Params = gin.Params{{Key: "id", Value: escrowedOrder.ID.String()}}
	c2.Request, _ = http.NewRequest("POST", "/", nil)

	ch.PrepareEscrow(c2)

	if w2.Code != http.StatusBadRequest {
		t.Errorf("expected %d for already escrowed order, got %d: %s", http.StatusBadRequest, w2.Code, w2.Body.String())
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

func TestGetShippingEstimate_UsesProductLocation(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	seller := createTestUserWithLocation(t, db, "Abuja", "FCT", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	product.Location = "Lokoja, Kogi, Nigeria"
	product.ShippingSameState = 5.0
	product.ShippingSameCountry = 0
	if err := db.Save(product).Error; err != nil {
		t.Fatalf("failed to update product location: %v", err)
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(
		"GET",
		fmt.Sprintf("/?productId=%s&buyerCity=Ankpa&buyerState=Kogi%%20State&buyerCountry=Nigeria", product.ID),
		nil,
	)

	ch.GetShippingEstimate(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})

	if data["zone"] != ShippingZoneSameState {
		t.Errorf("expected zone %s, got %v", ShippingZoneSameState, data["zone"])
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

// ---------- SubmitEscrow tests (T1) ----------

func TestSubmitEscrow_WrongBuyer(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusPendingPayment, "USDC")

	// Try as seller (not the buyer)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", seller)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}

	body, _ := json.Marshal(map[string]interface{}{
		"approveTxHash": "0xaaa",
		"escrowTxHash":  "0xbbb",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	ch.SubmitEscrow(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected %d, got %d: %s", http.StatusForbidden, w.Code, w.Body.String())
	}
}

func TestSubmitEscrow_WrongStatus(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusEscrowed, "USDC")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}

	body, _ := json.Marshal(map[string]interface{}{
		"approveTxHash": "0xaaa",
		"escrowTxHash":  "0xbbb",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	ch.SubmitEscrow(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d for already escrowed order, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestSubmitEscrow_MissingFields(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusPendingPayment, "USDC")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}

	body, _ := json.Marshal(map[string]interface{}{
		"approveTxHash": "0xaaa",
		// missing escrowTxHash
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	ch.SubmitEscrow(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d for missing fields, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

// ---------- SubmitConfirm tests (T2) ----------

func TestSubmitConfirm_WrongBuyer(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusDelivered, "USDC")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", seller)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}

	body, _ := json.Marshal(map[string]interface{}{
		"txHash": "0xccc",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	ch.SubmitConfirm(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected %d, got %d: %s", http.StatusForbidden, w.Code, w.Body.String())
	}
}

func TestSubmitConfirm_WrongStatus(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusPendingPayment, "USDC")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}

	body, _ := json.Marshal(map[string]interface{}{
		"txHash": "0xccc",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	ch.SubmitConfirm(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d for pending_payment order, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

// ---------- RequestRefund tests (T3) ----------

func TestRequestRefund_Escrowed(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, buyer, "0x0000000000000000000000000000000000000001")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusEscrowed, "USDC")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}
	c.Request, _ = http.NewRequest("POST", "/", nil)

	ch.RequestRefund(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected %d for escrowed refund, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	if data["refundTx"] == nil {
		t.Error("expected refundTx in response for escrowed order")
	}
}

func TestRequestRefund_Delivered(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, buyer, "0x0000000000000000000000000000000000000001")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusDelivered, "USDC")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}
	c.Request, _ = http.NewRequest("POST", "/", nil)

	ch.RequestRefund(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected %d for delivered refund, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	// Should update status to refund_requested
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	if data["status"] != models.OrderStatusRefundRequested {
		t.Errorf("expected status %s, got %v", models.OrderStatusRefundRequested, data["status"])
	}

	// Verify DB was updated
	var updated models.Order
	config.DB.First(&updated, "id = ?", order.ID)
	if updated.Status != models.OrderStatusRefundRequested {
		t.Errorf("expected DB status %s, got %s", models.OrderStatusRefundRequested, updated.Status)
	}
}

func TestRequestRefund_WrongStatus(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, buyer, "0x0000000000000000000000000000000000000001")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusCompleted, "USDC")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}
	c.Request, _ = http.NewRequest("POST", "/", nil)

	ch.RequestRefund(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d for completed order, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

func TestRequestRefund_OnlyBuyer(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusEscrowed, "USDC")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", seller)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}
	c.Request, _ = http.NewRequest("POST", "/", nil)

	ch.RequestRefund(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected %d for seller requesting refund, got %d: %s", http.StatusForbidden, w.Code, w.Body.String())
	}
}

// ---------- CancelOrder happy path tests (T4) ----------

func TestCancelOrder_HappyPath(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000003")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusEscrowed, "USDC")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", seller)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}
	c.Request, _ = http.NewRequest("POST", "/", nil)

	ch.CancelOrder(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data := resp["data"].(map[string]interface{})
	if data["cancelTx"] == nil {
		t.Error("expected cancelTx in response")
	}
}

func TestCancelOrder_WrongStatus(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000003")
	product := createTestProduct(t, db, seller.ID, 10.0)

	// pending_payment can be cancelled (valid transition) — test with delivered which can't
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusDelivered, "USDC")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", seller)
	c.Params = gin.Params{{Key: "id", Value: order.ID.String()}}
	c.Request, _ = http.NewRequest("POST", "/", nil)

	ch.CancelOrder(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d for delivered order, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

// ---------- Stock validation tests ----------

func TestCreateOrderFromCart_InsufficientStock(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")

	// Create product with only 1 in stock
	product := createTestProduct(t, db, seller.ID, 10.0)
	db.Model(product).Update("stock", 1)

	items := []models.CartItem{
		{
			ID:       uuid.New(),
			Product:  product.ID,
			Seller:   seller.ID,
			Quantity: 5, // Request more than available
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
		"token": "USDC",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	ch.CreateOrderFromCart(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d for insufficient stock, got %d: %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
}

// ---------- toWei string-based conversion tests ----------

func TestToWei_StringBased(t *testing.T) {
	tests := []struct {
		amount   string
		decimals int
		expected string
	}{
		{"10.0", 6, "10000000"},
		{"10.5", 6, "10500000"},
		{"0.000001", 6, "1"},
		{"1", 18, "1000000000000000000"},
		{"0.1", 18, "100000000000000000"},
		{"100", 6, "100000000"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%d", tt.amount, tt.decimals), func(t *testing.T) {
			result, err := toWei(tt.amount, tt.decimals)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.String() != tt.expected {
				t.Errorf("toWei(%s, %d) = %s, want %s", tt.amount, tt.decimals, result.String(), tt.expected)
			}
		})
	}
}

// ---------- isValidTransition tests ----------

func TestIsValidTransition(t *testing.T) {
	if !isValidTransition(models.OrderStatusPendingPayment, models.OrderStatusEscrowed) {
		t.Error("pending_payment -> escrowed should be valid")
	}
	if isValidTransition(models.OrderStatusCompleted, models.OrderStatusEscrowed) {
		t.Error("completed -> escrowed should not be valid")
	}
	if !isValidTransition(models.OrderStatusDelivered, models.OrderStatusCompleted) {
		t.Error("delivered -> completed should be valid")
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

// A listing can be suspended or deleted while it sits in a buyer's cart. The
// order path re-checks visibility so funds are never escrowed against a pulled
// listing — add-to-cart's check alone is not enough.
func TestCreateOrderFromCart_RejectsUnavailableListing(t *testing.T) {
	cases := []struct {
		name string
		pull func(db *gorm.DB, productID uuid.UUID)
	}{
		{"suspended", func(db *gorm.DB, id uuid.UUID) {
			db.Model(&models.Product{}).Where("id = ?", id).
				Update("status", models.ProductStatusSuspended)
		}},
		{"soft deleted", func(db *gorm.DB, id uuid.UUID) {
			db.Delete(&models.Product{}, "id = ?", id)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupCheckoutTestDB(t)
			ch := setupCheckoutHandler(t)

			buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
			seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
			setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")

			product := createTestProduct(t, db, seller.ID, 10.0)
			items := []models.CartItem{{
				ID: uuid.New(), Product: product.ID, Seller: seller.ID,
				Quantity: 1, Price: 10.0, AddedAt: time.Now(),
			}}
			createTestCart(t, db, buyer.ID, items)

			// Pulled after the item was already in the cart.
			tc.pull(db, product.ID)

			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set("user", buyer)
			body, _ := json.Marshal(map[string]interface{}{"token": "USDC"})
			c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			ch.CreateOrderFromCart(c)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for a %s listing, got %d: %s", tc.name, w.Code, w.Body.String())
			}
			var orders int64
			db.Model(&models.Order{}).Count(&orders)
			if orders != 0 {
				t.Fatalf("an order was created for a %s listing", tc.name)
			}
		})
	}
}

// Soft delete exists so order history survives. enrichOrdersWithPreview used a
// plain query, so GORM filtered the deleted product out and every past order of
// it serialized `product: null` — losing its name, price and image.
func TestOrderPreviewSurvivesProductDeletion(t *testing.T) {
	db := setupCheckoutTestDB(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 42.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusCompleted, "USDC")

	if err := db.Delete(&models.Product{}, "id = ?", product.ID).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	enriched := enrichOrdersWithPreview([]models.Order{*order})
	if len(enriched) != 1 {
		t.Fatalf("expected 1 order, got %d", len(enriched))
	}
	if enriched[0].Product == nil {
		t.Fatal("order lost its product preview after the product was deleted")
	}
	if enriched[0].Product.Name != product.Name {
		t.Fatalf("product name = %q, want %q", enriched[0].Product.Name, product.Name)
	}
	if enriched[0].Product.Price != 42.0 {
		t.Fatalf("product price = %v, want 42", enriched[0].Product.Price)
	}
}

// A seller can be suspended while their listing sits in a buyer's cart. The
// order path re-checks the SELLER too, not just the listing: a suspended seller
// is locked out of shipping and confirming, so escrowing against them would
// strand the buyer's funds.
func TestCreateOrderFromCart_RejectsSuspendedSeller(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")

	product := createTestProduct(t, db, seller.ID, 10.0)
	items := []models.CartItem{{
		ID: uuid.New(), Product: product.ID, Seller: seller.ID,
		Quantity: 1, Price: 10.0, AddedAt: time.Now(),
	}}
	createTestCart(t, db, buyer.ID, items)

	// Suspended after the item was already in the cart. The listing stays active.
	db.Model(&models.User{}).Where("id = ?", seller.ID).
		Update("account_status", models.AccountStatusSuspended)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	body, _ := json.Marshal(map[string]interface{}{"token": "USDC"})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	ch.CreateOrderFromCart(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a suspended seller, got %d: %s", w.Code, w.Body.String())
	}
	var orders int64
	db.Model(&models.Order{}).Count(&orders)
	if orders != 0 {
		t.Fatal("an order was escrowed against a suspended seller")
	}
}

// ---------- Saved delivery address ----------

func postCartOrder(t *testing.T, ch *CheckoutHandler, buyer *models.User, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	raw, _ := json.Marshal(body)
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	ch.CreateOrderFromCart(c)
	return w
}

func TestCreateOrderFromCart_SnapshotsSavedDeliveryAddress(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	// Profile says Abuja; the saved address says Ikeja — the address wins, so
	// the zone is priced from where the parcel actually goes.
	buyer := createTestUserWithLocation(t, db, "Abuja", "FCT", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")
	product := createTestProduct(t, db, seller.ID, 10.0)
	createTestCart(t, db, buyer.ID, []models.CartItem{
		{ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 1, Price: 10.0, AddedAt: time.Now()},
	})
	addr := models.DeliveryAddress{
		ID: uuid.New(), UserID: buyer.ID, RecipientName: "Ada Obi", Phone: "+2348012345678",
		Street: "12 Allen Avenue", City: "Ikeja", State: "Lagos", Country: "Nigeria",
	}
	if err := db.Create(&addr).Error; err != nil {
		t.Fatal(err)
	}

	w := postCartOrder(t, ch, buyer, map[string]interface{}{
		"token":             "USDC",
		"deliveryAddressId": addr.ID.String(),
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}

	var order models.Order
	if err := db.Where("buyer_id = ?", buyer.ID).First(&order).Error; err != nil {
		t.Fatal(err)
	}
	if order.ShippingRecipientName != "Ada Obi" || order.ShippingPhone != "+2348012345678" ||
		order.ShippingStreet != "12 Allen Avenue" || order.ShippingCity != "Ikeja" || order.ShippingState != "Lagos" {
		t.Errorf("address not snapshotted: %+v", order)
	}
	if order.ShippingZone != ShippingZoneSameCity {
		t.Errorf("want zone priced from the saved address (%s), got %s", ShippingZoneSameCity, order.ShippingZone)
	}
}

func TestCreateOrderFromCart_RejectsSomeoneElsesAddress(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	stranger := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")
	product := createTestProduct(t, db, seller.ID, 10.0)
	createTestCart(t, db, buyer.ID, []models.CartItem{
		{ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 1, Price: 10.0, AddedAt: time.Now()},
	})
	addr := models.DeliveryAddress{
		ID: uuid.New(), UserID: stranger.ID, RecipientName: "X", Phone: "1",
		Street: "s", City: "Lagos", State: "Lagos", Country: "Nigeria",
	}
	db.Create(&addr)

	w := postCartOrder(t, ch, buyer, map[string]interface{}{
		"token":             "USDC",
		"deliveryAddressId": addr.ID.String(),
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
	}
	var n int64
	db.Model(&models.Order{}).Where("buyer_id = ?", buyer.ID).Count(&n)
	if n != 0 {
		t.Errorf("want no orders created, got %d", n)
	}
}

func TestCreateOrderFromCart_ServiceLinesSkipTheAddressSnapshot(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")
	cat := createTestCategory(t, db, models.CategoryTypeService, models.ServiceFeeModeStandard)
	svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)
	payContactFee(t, db, buyer.ID, svc.ID)
	product := createTestProduct(t, db, seller.ID, 10.0)
	createTestCart(t, db, buyer.ID, []models.CartItem{
		{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, Price: 20.0, AddedAt: time.Now()},
		{ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 1, Price: 10.0, AddedAt: time.Now()},
	})
	addr := models.DeliveryAddress{
		ID: uuid.New(), UserID: buyer.ID, RecipientName: "Ada Obi", Phone: "+2348012345678",
		Street: "12 Allen Avenue", City: "Lagos", State: "Lagos", Country: "Nigeria",
	}
	db.Create(&addr)

	w := postCartOrder(t, ch, buyer, map[string]interface{}{"token": "USDC", "deliveryAddressId": addr.ID.String()})
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	var orders []models.Order
	db.Where("buyer_id = ?", buyer.ID).Find(&orders)
	if len(orders) != 2 {
		t.Fatalf("want 2 orders, got %d", len(orders))
	}
	for _, o := range orders {
		copied := o.ShippingStreet != "" || o.ShippingPhone != "" || o.ShippingRecipientName != ""
		if o.Kind == models.OrderKindService && copied {
			t.Errorf("service order carries the delivery address: %+v", o)
		}
		if o.Kind == models.OrderKindProduct && !copied {
			t.Errorf("product order is missing the delivery address: %+v", o)
		}
	}
}

func TestCreateOrderFromCart_MalformedAddressIDIsABadRequest(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")

	w := postCartOrder(t, ch, buyer, map[string]interface{}{"token": "USDC", "deliveryAddressId": "not-a-uuid"})
	if w.Code != http.StatusBadRequest || bytes.Contains(w.Body.Bytes(), []byte("token is required")) {
		t.Fatalf("want 400 without a token complaint, got %d: %s", w.Code, w.Body.String())
	}

	w = postCartOrder(t, ch, buyer, map[string]interface{}{})
	if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte("token is required")) {
		t.Fatalf("missing token: want 400 token is required, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateOrderFromCart_AddressLookupFailureIsNotA404(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	createTestCart(t, db, buyer.ID, []models.CartItem{
		{ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 1, Price: 10.0, AddedAt: time.Now()},
	})
	db.Exec("DROP TABLE delivery_addresses")

	w := postCartOrder(t, ch, buyer, map[string]interface{}{"token": "USDC", "deliveryAddressId": uuid.New().String()})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on a database error, got %d: %s", w.Code, w.Body.String())
	}
}

func TestClearCart_EmptiesTheActiveCart(t *testing.T) {
	db := setupCheckoutTestDB(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	cart := createTestCart(t, db, buyer.ID, []models.CartItem{
		{ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 2, Price: 10.0, AddedAt: time.Now()},
	})

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Request, _ = http.NewRequest("DELETE", "/", nil)
	(&Handlers{}).ClearCart(c)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var saved models.Cart
	db.First(&saved, "id = ?", cart.ID)
	if n := len(saved.GetItems()); n != 0 {
		t.Errorf("want an empty cart, got %d items", n)
	}
}
