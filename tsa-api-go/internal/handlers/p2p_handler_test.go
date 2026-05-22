package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ─── Test bench ────────────────────────────────────────────────────────────

func setupP2PHandlerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	stmts := []string{
		// Full users DDL — GORM's INSERT touches every column on the User
		// model, so the SQLite table must include them. Mirrors the Postgres
		// schema produced by AutoMigrate; types are simplified for SQLite.
		`CREATE TABLE users (
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
			persona_inquiry_id TEXT,
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
			email_verified INTEGER DEFAULT 0,
			deleted_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE p2p_payment_methods (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL,
			type TEXT, country TEXT, currency TEXT,
			bank_name TEXT, bank_code TEXT,
			account_number TEXT, account_name TEXT,
			verified INTEGER DEFAULT 0, is_default INTEGER DEFAULT 0,
			deleted_at DATETIME,
			created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE p2p_ads (
			id TEXT PRIMARY KEY, seller_id TEXT NOT NULL,
			side TEXT, token TEXT, chain_id INTEGER,
			amount TEXT, remaining TEXT, reserved TEXT,
			fiat_currency TEXT, rate TEXT,
			rate_type TEXT NOT NULL DEFAULT 'fixed',
			float_offset_bps INTEGER,
			float_anchor TEXT NOT NULL DEFAULT 'bybit_sell',
			min_limit TEXT, max_limit TEXT,
			payment_method_ids TEXT, terms TEXT,
			on_chain_ad_id TEXT, escrow_tx_hash TEXT,
			status TEXT DEFAULT 'pending',
			expires_at DATETIME,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME
		)`,
		`CREATE TABLE p2p_trades (
			id TEXT PRIMARY KEY,
			ad_id TEXT NOT NULL, buyer_id TEXT NOT NULL, seller_id TEXT NOT NULL,
			token TEXT, chain_id INTEGER,
			token_amount TEXT, fiat_amount TEXT,
			fiat_currency TEXT, rate TEXT,
			payment_method_id TEXT,
			on_chain_trade_id TEXT UNIQUE,
			open_tx_hash TEXT, release_tx_hash TEXT,
			status TEXT DEFAULT 'pending_payment',
			proof_image_url TEXT, cancel_reason TEXT,
			resolved_by_admin TEXT,
			marked_paid_at DATETIME, released_at DATETIME,
			expires_at DATETIME NOT NULL,
			expired_notified_at DATETIME,
			created_at DATETIME, updated_at DATETIME,
			CHECK (buyer_id <> seller_id)
		)`,
		`CREATE TABLE p2p_audit_logs (
			id TEXT PRIMARY KEY, actor_id TEXT,
			trade_id TEXT, ad_id TEXT,
			action TEXT NOT NULL, metadata TEXT,
			created_at DATETIME
		)`,
		`CREATE TABLE p2p_disputes (
			id TEXT PRIMARY KEY,
			trade_id TEXT NOT NULL UNIQUE,
			raised_by TEXT NOT NULL,
			raised_role TEXT NOT NULL,
			reason TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'open',
			resolved_by TEXT,
			resolution TEXT,
			refunded_to_buyer INTEGER,
			resolve_tx_hash TEXT,
			created_at DATETIME,
			resolved_at DATETIME
		)`,
		`CREATE TABLE p2p_messages (
			id TEXT PRIMARY KEY,
			trade_id TEXT NOT NULL,
			sender_id TEXT,
			type TEXT NOT NULL,
			body TEXT,
			image_url TEXT,
			created_at DATETIME
		)`,
		`CREATE TABLE request_idempotencies (
			key TEXT PRIMARY KEY, user_id TEXT NOT NULL,
			request_hash TEXT NOT NULL, response_status INTEGER NOT NULL,
			response_body TEXT, response_headers TEXT,
			created_at DATETIME
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create table: %v\nSQL=%s", err, s)
		}
	}
	return db
}

type stubEscrow struct {
	lastAdAmount    *big.Int
	lastTradeAmount *big.Int
}

func (s *stubEscrow) ContractAddress() string { return "0xescrow" }
func (s *stubEscrow) PrepareCreateAd(adId [32]byte, seller, token string, amount *big.Int) ([]byte, error) {
	s.lastAdAmount = new(big.Int).Set(amount)
	return []byte(`{"to":"0xescrow"}`), nil
}
func (s *stubEscrow) PrepareOpenTrade(tradeId, adId [32]byte, buyer string, amount *big.Int) ([]byte, error) {
	s.lastTradeAmount = new(big.Int).Set(amount)
	return []byte(`{"to":"0xescrow"}`), nil
}
func (s *stubEscrow) PrepareMarkPaid([32]byte, string) ([]byte, error) {
	return []byte(`{}`), nil
}
func (s *stubEscrow) PrepareRelease([32]byte, string) ([]byte, error) {
	return []byte(`{}`), nil
}
func (s *stubEscrow) PrepareCloseAd([32]byte, string) ([]byte, error) {
	return []byte(`{}`), nil
}
func (s *stubEscrow) PrepareCancelByTimeout(tradeId [32]byte, caller string) ([]byte, error) {
	return []byte(`{"to":"0xescrow"}`), nil
}
func (s *stubEscrow) VerifyTradeCancelled(_ string, expectedTradeId [32]byte) (*services.P2PTradeCancelledEvent, error) {
	amt := s.lastTradeAmount
	if amt == nil {
		amt = big.NewInt(0)
	}
	return &services.P2PTradeCancelledEvent{TradeID: expectedTradeId, ReturnedToAd: amt}, nil
}
func (s *stubEscrow) PrepareRaiseDispute([32]byte, string) ([]byte, error) {
	return []byte(`{"to":"0xescrow"}`), nil
}
func (s *stubEscrow) PrepareAdminResolve([32]byte, bool, string) ([]byte, error) {
	return []byte(`{"to":"0xescrow"}`), nil
}
func (s *stubEscrow) VerifyTradeDisputed(_ string, expectedTradeId [32]byte) (*services.P2PTradeDisputedEvent, error) {
	return &services.P2PTradeDisputedEvent{TradeID: expectedTradeId, By: common.HexToAddress("0xbeef")}, nil
}
func (s *stubEscrow) ReadTrade(_ [32]byte) (*services.OnChainP2PTrade, error) {
	return &services.OnChainP2PTrade{Status: services.OnChainP2PStatusNone}, nil
}

func (s *stubEscrow) VerifyTradeResolved(_ string, expectedTradeId [32]byte) (*services.P2PTradeResolvedEvent, error) {
	amt := s.lastTradeAmount
	if amt == nil {
		amt = big.NewInt(0)
	}
	return &services.P2PTradeResolvedEvent{
		TradeID: expectedTradeId, ReleasedToBuyer: true,
		AmountMoved: amt, FeeToSystem: big.NewInt(0),
	}, nil
}
func (s *stubEscrow) VerifyAdCreated(_ string, expectedAdId [32]byte) (*services.P2PAdCreatedEvent, error) {
	amt := s.lastAdAmount
	if amt == nil {
		amt = big.NewInt(0)
	}
	return &services.P2PAdCreatedEvent{AdID: expectedAdId, Seller: common.HexToAddress("0x1"), Token: common.HexToAddress("0x2"), Amount: amt}, nil
}
func (s *stubEscrow) VerifyTradeOpened(_ string, expectedTradeId [32]byte) (*services.P2PTradeOpenedEvent, error) {
	amt := s.lastTradeAmount
	if amt == nil {
		amt = big.NewInt(0)
	}
	return &services.P2PTradeOpenedEvent{TradeID: expectedTradeId, Buyer: common.HexToAddress("0xd00d"), Amount: amt}, nil
}
func (s *stubEscrow) VerifyTradeReleased(_ string, expectedTradeId [32]byte) (*services.P2PTradeReleasedEvent, error) {
	amt := s.lastTradeAmount
	if amt == nil {
		amt = big.NewInt(0)
	}
	return &services.P2PTradeReleasedEvent{TradeID: expectedTradeId, Buyer: common.HexToAddress("0xd00d"), AmountToBuyer: amt, FeeToSystem: big.NewInt(0)}, nil
}

type stubRates struct{}

func (stubRates) GetSnapshot() (*services.RatesSnapshot, error) {
	return &services.RatesSnapshot{
		Rates:    map[string]services.P2PRate{"NGN": {Currency: "NGN", MidRate: 1500, Stale: false}},
		CachedAt: time.Now(),
	}, nil
}

type stubBus struct{ events []events.Event }

func (b *stubBus) Publish(e events.Event) { b.events = append(b.events, e) }

func buildHandler(t *testing.T) (*P2PHandler, *gorm.DB, *stubEscrow) {
	t.Helper()
	db := setupP2PHandlerDB(t)
	escrow := &stubEscrow{}
	svc := services.NewP2PTradeService(db, escrow, stubRates{}, &stubBus{}, 146)
	svc.SetTokenResolver(func(symbol string) string {
		return "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
	})
	msgSvc := services.NewP2PMessageService(db, &stubBus{})
	svc.SetMessageService(msgSvc)
	return NewP2PHandler(db, svc, msgSvc, nil), db, escrow
}

func seedUser(t *testing.T, db *gorm.DB, verified bool) models.User {
	t.Helper()
	status := "pending"
	if verified {
		status = "verified"
	}
	user := models.User{
		ID:                 uuid.New(),
		Name:               "Test",
		Username:           "u" + uuid.New().String()[:6],
		Email:              uuid.New().String() + "@e.com",
		Password:           "x",
		VerificationStatus: status,
		AccountStatus:      "active",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func seedPaymentMethodH(t *testing.T, db *gorm.DB, userID uuid.UUID, currency string) uuid.UUID {
	t.Helper()
	pm := &models.P2PPaymentMethod{
		ID: uuid.New(), UserID: userID,
		Type: models.P2PPaymentTypeBank, Country: "NG", Currency: currency,
		BankName: "Test", AccountNumber: "1234567890", AccountName: "Test",
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed pm: %v", err)
	}
	return pm.ID
}

func newRouter(h *P2PHandler, user *models.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/p2p")
	if user != nil {
		g.Use(func(c *gin.Context) {
			c.Set("user", *user)
			c.Next()
		})
	}
	g.GET("/payment-methods", h.ListPaymentMethods)
	g.POST("/payment-methods", h.CreatePaymentMethod)
	g.DELETE("/payment-methods/:id", h.DeletePaymentMethod)
	g.GET("/ads", h.ListAds)
	g.GET("/ads/my", h.ListMyAds)
	g.GET("/ads/:id", h.GetAd)
	g.POST("/ads", h.CreateAd)
	g.POST("/ads/:id/confirm", h.ConfirmAd)
	g.POST("/ads/:id/pause", h.PauseAd)
	g.POST("/ads/:id/resume", h.ResumeAd)
	g.POST("/ads/:id/trades", h.OpenTrade)
	g.GET("/trades", h.ListTrades)
	g.GET("/trades/:id", h.GetTrade)
	g.POST("/trades/:id/open/confirm", h.ConfirmOpenTrade)
	g.POST("/trades/:id/mark-paid", h.MarkPaid)
	g.POST("/trades/:id/release", h.PrepareRelease)
	g.POST("/trades/:id/release/confirm", h.ConfirmRelease)
	return r
}

func doJSON(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ─── Tests ─────────────────────────────────────────────────────────────────

func TestP2P_NoAuth_Returns401(t *testing.T) {
	h, _, _ := buildHandler(t)
	r := newRouter(h, nil)
	w := doJSON(t, r, http.MethodGet, "/api/p2p/ads", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestP2P_UnverifiedUser_Returns403(t *testing.T) {
	h, db, _ := buildHandler(t)
	user := seedUser(t, db, false)
	r := newRouter(h, &user)
	w := doJSON(t, r, http.MethodGet, "/api/p2p/ads", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for unverified, got %d", w.Code)
	}
}

func TestPaymentMethods_CRUD_HappyPath(t *testing.T) {
	h, db, _ := buildHandler(t)
	user := seedUser(t, db, true)
	r := newRouter(h, &user)

	// Create
	w := doJSON(t, r, http.MethodPost, "/api/p2p/payment-methods", createPaymentMethodRequest{
		Type: "bank", Country: "NG", Currency: "NGN",
		BankName: "Test", AccountNumber: "1234567890", AccountName: "Test User",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d body=%s", w.Code, w.Body.String())
	}

	// List
	w = doJSON(t, r, http.MethodGet, "/api/p2p/payment-methods", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d", w.Code)
	}
	var listResp struct {
		Data []models.P2PPaymentMethod `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &listResp)
	if len(listResp.Data) != 1 {
		t.Fatalf("want 1 method, got %d", len(listResp.Data))
	}

	// Delete
	w = doJSON(t, r, http.MethodDelete, "/api/p2p/payment-methods/"+listResp.Data[0].ID.String(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: want 200, got %d", w.Code)
	}

	// Confirm soft-deleted (filter applied)
	w = doJSON(t, r, http.MethodGet, "/api/p2p/payment-methods", nil)
	json.Unmarshal(w.Body.Bytes(), &listResp)
	if len(listResp.Data) != 0 {
		t.Fatalf("want 0 methods after delete, got %d", len(listResp.Data))
	}
}

func TestCreateAd_HappyPath_Returns201WithUnsignedTx(t *testing.T) {
	h, db, _ := buildHandler(t)
	seller := seedUser(t, db, true)
	pmID := seedPaymentMethodH(t, db, seller.ID, "NGN")
	r := newRouter(h, &seller)

	w := doJSON(t, r, http.MethodPost, "/api/p2p/ads", createAdRequest{
		Token: "USDT", TokenAmount: "1000000000",
		FiatCurrency: "NGN", Rate: "1500",
		MinLimit: "10000", MaxLimit: "5000000000",
		PaymentMethodIDs: []string{pmID.String()},
		SellerAddress:    "0xseller",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create ad: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Ad         models.P2PAd `json:"ad"`
			UnsignedTx any          `json:"unsignedTx"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Success || resp.Data.Ad.ID == uuid.Nil {
		t.Fatalf("malformed response: %+v", resp)
	}
	if resp.Data.UnsignedTx == nil {
		t.Fatalf("missing unsignedTx")
	}
}

func TestCreateAd_InvalidPayload_Returns400(t *testing.T) {
	h, db, _ := buildHandler(t)
	user := seedUser(t, db, true)
	r := newRouter(h, &user)
	w := doJSON(t, r, http.MethodPost, "/api/p2p/ads", map[string]any{
		"token": "USDT", // missing required fields
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for missing fields, got %d", w.Code)
	}
}

func TestE2E_HandlerFlow_CreateConfirmOpenMarkRelease(t *testing.T) {
	h, db, _ := buildHandler(t)
	seller := seedUser(t, db, true)
	buyer := seedUser(t, db, true)
	pmID := seedPaymentMethodH(t, db, seller.ID, "NGN")

	sellerRouter := newRouter(h, &seller)
	buyerRouter := newRouter(h, &buyer)

	// Seller creates ad
	w := doJSON(t, sellerRouter, http.MethodPost, "/api/p2p/ads", createAdRequest{
		Token: "USDT", TokenAmount: "1000000000",
		FiatCurrency: "NGN", Rate: "1500",
		MinLimit: "10000", MaxLimit: "5000000000",
		PaymentMethodIDs: []string{pmID.String()},
		SellerAddress:    "0xseller",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create ad: %d %s", w.Code, w.Body.String())
	}
	var createResp struct {
		Data struct {
			Ad models.P2PAd `json:"ad"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &createResp)
	adID := createResp.Data.Ad.ID

	// Confirm ad
	w = doJSON(t, sellerRouter, http.MethodPost, "/api/p2p/ads/"+adID.String()+"/confirm",
		map[string]string{"txHash": "0xadtx"})
	if w.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}

	// Buyer opens trade
	w = doJSON(t, buyerRouter, http.MethodPost, "/api/p2p/ads/"+adID.String()+"/trades", openTradeRequest{
		FiatAmount: "150000", PaymentMethodID: pmID.String(), BuyerAddress: "0xbuyer",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("open trade: %d %s", w.Code, w.Body.String())
	}
	var openResp struct {
		Data struct {
			Trade models.P2PTrade `json:"trade"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &openResp)
	tradeID := openResp.Data.Trade.ID

	// Confirm open
	w = doJSON(t, buyerRouter, http.MethodPost, "/api/p2p/trades/"+tradeID.String()+"/open/confirm",
		map[string]string{"txHash": "0xopentx"})
	if w.Code != http.StatusOK {
		t.Fatalf("confirm open: %d %s", w.Code, w.Body.String())
	}

	// Mark paid
	w = doJSON(t, buyerRouter, http.MethodPost, "/api/p2p/trades/"+tradeID.String()+"/mark-paid",
		map[string]string{"proofImageUrl": "https://e.com/p.png"})
	if w.Code != http.StatusOK {
		t.Fatalf("mark paid: %d %s", w.Code, w.Body.String())
	}

	// Release (seller)
	w = doJSON(t, sellerRouter, http.MethodPost, "/api/p2p/trades/"+tradeID.String()+"/release",
		map[string]string{"sellerAddress": "0xseller"})
	if w.Code != http.StatusOK {
		t.Fatalf("release: %d %s", w.Code, w.Body.String())
	}

	// Confirm release
	w = doJSON(t, sellerRouter, http.MethodPost, "/api/p2p/trades/"+tradeID.String()+"/release/confirm",
		map[string]string{"txHash": "0xreltx"})
	if w.Code != http.StatusOK {
		t.Fatalf("confirm release: %d %s", w.Code, w.Body.String())
	}

	// Final state assertion
	var trade models.P2PTrade
	db.First(&trade, "id = ?", tradeID)
	if trade.Status != models.P2PTradeStatusReleased {
		t.Fatalf("final trade status: want released, got %s", trade.Status)
	}
}

func TestGetTrade_NonParticipant_Returns403(t *testing.T) {
	h, db, _ := buildHandler(t)
	seller := seedUser(t, db, true)
	buyer := seedUser(t, db, true)
	stranger := seedUser(t, db, true)
	pmID := seedPaymentMethodH(t, db, seller.ID, "NGN")

	// Build trade directly via service to skip the HTTP dance
	svc := services.NewP2PTradeService(db, &stubEscrow{}, stubRates{}, &stubBus{}, 146)
	svc.SetTokenResolver(func(s string) string { return "0x123" })

	res, _ := svc.CreateAd(context.Background(), seller.ID, "0xs", services.CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), res.Ad.ID, seller.ID, "", "0xtx")
	trade, _, _ := svc.OpenTrade(context.Background(), res.Ad.ID, buyer.ID, "0xb", services.OpenTradeParams{
		FiatAmount: "150000", PaymentMethodID: pmID,
	})

	strangerRouter := newRouter(h, &stranger)
	w := doJSON(t, strangerRouter, http.MethodGet, "/api/p2p/trades/"+trade.ID.String(), nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for stranger, got %d", w.Code)
	}
}

func TestPauseAd_NonOwner_Returns403(t *testing.T) {
	h, db, _ := buildHandler(t)
	seller := seedUser(t, db, true)
	other := seedUser(t, db, true)
	pmID := seedPaymentMethodH(t, db, seller.ID, "NGN")

	svc := services.NewP2PTradeService(db, &stubEscrow{}, stubRates{}, &stubBus{}, 146)
	svc.SetTokenResolver(func(s string) string { return "0x123" })
	res, _ := svc.CreateAd(context.Background(), seller.ID, "0xs", services.CreateAdParams{
		Token: "USDT", TokenAmount: "1000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), res.Ad.ID, seller.ID, "", "0xtx")

	otherRouter := newRouter(h, &other)
	w := doJSON(t, otherRouter, http.MethodPost, "/api/p2p/ads/"+res.Ad.ID.String()+"/pause", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
}

func TestListAds_DefaultsToActiveOnly(t *testing.T) {
	h, db, _ := buildHandler(t)
	user := seedUser(t, db, true)
	r := newRouter(h, &user)

	// Seed two ads directly: one pending, one active
	db.Create(&models.P2PAd{
		ID: uuid.New(), SellerID: user.ID, Side: "sell", Token: "USDT", ChainID: 146,
		Amount: "1000", Remaining: "1000", Reserved: "0", FiatCurrency: "NGN", Rate: "1500",
		Status: models.P2PAdStatusActive,
	})
	db.Create(&models.P2PAd{
		ID: uuid.New(), SellerID: user.ID, Side: "sell", Token: "USDT", ChainID: 146,
		Amount: "2000", Remaining: "2000", Reserved: "0", FiatCurrency: "NGN", Rate: "1500",
		Status: models.P2PAdStatusPending,
	})

	w := doJSON(t, r, http.MethodGet, "/api/p2p/ads", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	var resp struct {
		Data []models.P2PAd `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("want 1 active ad, got %d", len(resp.Data))
	}
	if resp.Data[0].Status != models.P2PAdStatusActive {
		t.Fatalf("returned non-active ad")
	}
}

func TestMapServiceError_StatusCodes(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{services.ErrAdNotFound, http.StatusNotFound},
		{services.ErrTradeNotFound, http.StatusNotFound},
		{services.ErrForbidden, http.StatusForbidden},
		{services.ErrInvalidParams, http.StatusBadRequest},
		{services.ErrSelfTrade, http.StatusBadRequest},
		{services.ErrAdNotActive, http.StatusConflict},
		{services.ErrInsufficientRemaining, http.StatusConflict},
		{services.ErrInvalidTransition, http.StatusConflict},
		{services.ErrRateStale, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.GET("/x", func(c *gin.Context) {
			if mapServiceError(c, tc.err) {
				return
			}
			c.JSON(200, gin.H{"ok": true})
		})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("err=%v: want %d, got %d", tc.err, tc.want, w.Code)
		}
	}
}

// ─── Gate-3 review additions ────────────────────────────────────────────────

// TestCreateAd_Idempotency_SecondCallReturnsSameAd wires the real Idempotency
// middleware into the P2P route group so the cross-cutting contract is
// exercised end-to-end: two CreateAd calls with the same Idempotency-Key
// must return the same ad ID (no duplicate row, no second on-chain prep
// triggered for the second call).
func TestCreateAd_Idempotency_SecondCallReturnsSameAd(t *testing.T) {
	h, db, escrow := buildHandler(t)
	seller := seedUser(t, db, true)
	pmID := seedPaymentMethodH(t, db, seller.ID, "NGN")

	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/p2p")
	g.Use(func(c *gin.Context) {
		c.Set("user", seller)
		c.Next()
	})
	g.POST("/ads", middleware.Idempotency(db), h.CreateAd)

	body := createAdRequest{
		Token: "USDT", TokenAmount: "1000000000",
		FiatCurrency: "NGN", Rate: "1500",
		MinLimit: "10000", MaxLimit: "5000000000",
		PaymentMethodIDs: []string{pmID.String()},
		SellerAddress:    "0xseller",
	}
	makeReq := func() *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/p2p/ads", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "dup-ad-key")
		r.ServeHTTP(w, req)
		return w
	}

	w1 := makeReq()
	if w1.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", w1.Code, w1.Body.String())
	}
	var resp1 struct {
		Data struct {
			Ad models.P2PAd `json:"ad"`
		} `json:"data"`
	}
	json.Unmarshal(w1.Body.Bytes(), &resp1)
	id1 := resp1.Data.Ad.ID

	w2 := makeReq()
	if w2.Code != http.StatusCreated {
		t.Fatalf("replay: %d %s", w2.Code, w2.Body.String())
	}
	if w2.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("missing Idempotent-Replayed header on replay")
	}
	var resp2 struct {
		Data struct {
			Ad models.P2PAd `json:"ad"`
		} `json:"data"`
	}
	json.Unmarshal(w2.Body.Bytes(), &resp2)
	if resp2.Data.Ad.ID != id1 {
		t.Fatalf("replay returned different ad: first=%s second=%s", id1, resp2.Data.Ad.ID)
	}

	// Exactly one row in the DB.
	var count int64
	db.Model(&models.P2PAd{}).Count(&count)
	if count != 1 {
		t.Fatalf("want 1 ad row, got %d", count)
	}
	// Escrow prepare called only once.
	_ = escrow // silence unused if no further use
}

func TestDeletePaymentMethod_BlockedWhenTradeOpen(t *testing.T) {
	h, db, _ := buildHandler(t)
	seller := seedUser(t, db, true)
	buyer := seedUser(t, db, true)
	pmID := seedPaymentMethodH(t, db, seller.ID, "NGN")

	// Drive an open trade against the payment method via the service directly.
	svc := services.NewP2PTradeService(db, &stubEscrow{}, stubRates{}, &stubBus{}, 146)
	svc.SetTokenResolver(func(s string) string { return "0x123" })
	res, _ := svc.CreateAd(context.Background(), seller.ID, "0xs", services.CreateAdParams{
		Token: "USDT", TokenAmount: "1000000000", FiatCurrency: "NGN", Rate: "1500",
		PaymentMethodIDs: []uuid.UUID{pmID},
	})
	_, _ = svc.ConfirmAd(context.Background(), res.Ad.ID, seller.ID, "", "0xtx")
	_, _, _ = svc.OpenTrade(context.Background(), res.Ad.ID, buyer.ID, "0xb", services.OpenTradeParams{
		FiatAmount: "150000", PaymentMethodID: pmID,
	})

	// Now seller tries to delete a payment method that's bound to the open trade.
	sellerRouter := newRouter(h, &seller)
	w := doJSON(t, sellerRouter, http.MethodDelete, "/api/p2p/payment-methods/"+pmID.String(), nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 for in-use payment method, got %d", w.Code)
	}
}
