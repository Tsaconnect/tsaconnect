// P2P handler HTTP-level tests. Focused on wire-format + error mapping for
// the pool/voucher endpoints introduced in Phase 2B/3 (CreateAd, SignAd,
// Cancel, Deposit/Withdraw, Approve, OpenTrade). Deep service-layer
// behaviour is covered separately in internal/services/p2p_trade_service_test.go;
// these tests exercise the seam between gin → handler → trade service →
// adapter mock so wire regressions (response shape, status code mapping,
// status-aware error branching, idempotency-key header propagation) are
// caught.
//
// Auth is faked by directly calling c.Set("user", ...) via a tiny
// middleware shim, sidestepping the real JWT middleware.

package handlers

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ─── Mock escrow (handler-scoped; matches services.P2PEscrowAdapter) ──────

type mockEscrow struct {
	contractAddress string
	balance         services.OnChainBalance
	allowance       *big.Int
	depositedEvent     *services.P2PDepositedEvent
	withdrawnEvent     *services.P2PWithdrawnEvent
	cancelledEvent     *services.P2PVoucherCancelledEvent
	openedEvent        *services.P2PTradeOpenedEvent
	openedDepositEvent *services.P2PTradeOpenedWithDepositEvent
	releasedEvent      *services.P2PTradeReleasedEvent
	tradeCancelled     *services.P2PTradeCancelledEvent
	disputedEvent      *services.P2PTradeDisputedEvent
	resolvedEvent      *services.P2PTradeResolvedEvent
	onChainTrade       *services.OnChainP2PTrade
	hashFn             func(services.AdVoucher) [32]byte
	// Captured args so handler tests can assert the deposit path was taken
	// with the right roles/addresses.
	lastOpenTradeDepositArgs *handlerOpenTradeDepositArgs
}

type handlerOpenTradeDepositArgs struct {
	TradeID  [32]byte
	Token    string
	Amount   *big.Int
	Merchant string
	From     string
}

func newMockEscrow() *mockEscrow {
	return &mockEscrow{
		contractAddress: "0xb5B073BF578fE24311ED50e540D9d632f1679261",
		balance:         services.OnChainBalance{Available: big.NewInt(0), Locked: big.NewInt(0)},
		allowance:       big.NewInt(0),
		hashFn:          defaultHashVoucher,
	}
}

func defaultHashVoucher(v services.AdVoucher) [32]byte {
	body := crypto.Keccak256Hash(
		v.Seller.Bytes(), v.Token.Bytes(),
		v.MaxAmount.Bytes(), v.MinPerTrade.Bytes(), v.MaxPerTrade.Bytes(),
		v.Deadline.Bytes(), v.Nonce[:],
	)
	return crypto.Keccak256Hash([]byte("P2PEscrowHandlerTest"), body[:])
}

func (m *mockEscrow) ContractAddress() string { return m.contractAddress }
func (m *mockEscrow) PrepareApprove(_, _ string, _ *big.Int) ([]byte, error) {
	return []byte(`{"tx":"approve"}`), nil
}
func (m *mockEscrow) ReadAllowance(_, _ string) (*big.Int, error) {
	return new(big.Int).Set(m.allowance), nil
}
func (m *mockEscrow) PrepareDeposit(_, _ string, _ *big.Int) ([]byte, error) {
	return []byte(`{"tx":"deposit"}`), nil
}
func (m *mockEscrow) PrepareWithdraw(_, _ string, _ *big.Int) ([]byte, error) {
	return []byte(`{"tx":"withdraw"}`), nil
}
func (m *mockEscrow) PrepareCancelVoucher(_ string, _ services.AdVoucher) ([]byte, error) {
	return []byte(`{"tx":"cancelVoucher"}`), nil
}
func (m *mockEscrow) PrepareOpenTradeWithVoucher(_ [32]byte, _ services.AdVoucher, _ []byte, _ string, _ *big.Int) ([]byte, error) {
	return []byte(`{"tx":"openTrade"}`), nil
}
func (m *mockEscrow) PrepareOpenTradeWithDeposit(tradeId [32]byte, token string, amount *big.Int, merchant, from string) ([]byte, error) {
	m.lastOpenTradeDepositArgs = &handlerOpenTradeDepositArgs{
		TradeID: tradeId, Token: token, Amount: new(big.Int).Set(amount),
		Merchant: merchant, From: from,
	}
	return []byte(`{"tx":"openTradeWithDeposit"}`), nil
}
func (m *mockEscrow) PrepareMarkPaid(_ [32]byte, _ string) ([]byte, error) {
	return []byte(`{"tx":"markPaid"}`), nil
}
func (m *mockEscrow) PrepareRelease(_ [32]byte, _ string) ([]byte, error) {
	return []byte(`{"tx":"release"}`), nil
}
func (m *mockEscrow) PrepareCancelByTimeout(_ [32]byte, _ string) ([]byte, error) {
	return []byte(`{"tx":"cancelByTimeout"}`), nil
}
func (m *mockEscrow) PrepareRaiseDispute(_ [32]byte, _ string) ([]byte, error) {
	return []byte(`{"tx":"raiseDispute"}`), nil
}
func (m *mockEscrow) PrepareAdminResolve(_ [32]byte, _ bool, _ string) ([]byte, error) {
	return []byte(`{"tx":"adminResolve"}`), nil
}
func (m *mockEscrow) PrepareAdminRelease(_ [32]byte, _ string) ([]byte, error) {
	return []byte(`{"tx":"adminRelease"}`), nil
}
func (m *mockEscrow) VerifyDeposited(_ string, s, t common.Address) (*services.P2PDepositedEvent, error) {
	if m.depositedEvent != nil {
		return m.depositedEvent, nil
	}
	return &services.P2PDepositedEvent{Seller: s, Token: t, Amount: big.NewInt(100)}, nil
}
func (m *mockEscrow) VerifyWithdrawn(_ string, s, t common.Address) (*services.P2PWithdrawnEvent, error) {
	if m.withdrawnEvent != nil {
		return m.withdrawnEvent, nil
	}
	return &services.P2PWithdrawnEvent{Seller: s, Token: t, Amount: big.NewInt(50)}, nil
}
func (m *mockEscrow) VerifyVoucherCancelled(_ string, h [32]byte) (*services.P2PVoucherCancelledEvent, error) {
	if m.cancelledEvent != nil {
		return m.cancelledEvent, nil
	}
	return &services.P2PVoucherCancelledEvent{VoucherHash: h}, nil
}
func (m *mockEscrow) VerifyTradeOpened(_ string, id [32]byte) (*services.P2PTradeOpenedEvent, error) {
	if m.openedEvent != nil {
		return m.openedEvent, nil
	}
	return &services.P2PTradeOpenedEvent{TradeID: id, Amount: big.NewInt(100)}, nil
}
func (m *mockEscrow) VerifyTradeOpenedWithDeposit(_ string, id [32]byte) (*services.P2PTradeOpenedWithDepositEvent, error) {
	if m.openedDepositEvent != nil {
		return m.openedDepositEvent, nil
	}
	return &services.P2PTradeOpenedWithDepositEvent{TradeID: id, Amount: big.NewInt(100)}, nil
}
func (m *mockEscrow) VerifyTradeReleased(_ string, id [32]byte) (*services.P2PTradeReleasedEvent, error) {
	if m.releasedEvent != nil {
		return m.releasedEvent, nil
	}
	return &services.P2PTradeReleasedEvent{TradeID: id, AmountToBuyer: big.NewInt(99), FeeToSystem: big.NewInt(1)}, nil
}
func (m *mockEscrow) VerifyTradeCancelled(_ string, id [32]byte) (*services.P2PTradeCancelledEvent, error) {
	if m.tradeCancelled != nil {
		return m.tradeCancelled, nil
	}
	return &services.P2PTradeCancelledEvent{TradeID: id, Amount: big.NewInt(100), ReturnedToPool: true}, nil
}
func (m *mockEscrow) VerifyTradeDisputed(_ string, id [32]byte) (*services.P2PTradeDisputedEvent, error) {
	if m.disputedEvent != nil {
		return m.disputedEvent, nil
	}
	return &services.P2PTradeDisputedEvent{TradeID: id}, nil
}
func (m *mockEscrow) VerifyTradeResolved(_ string, id [32]byte) (*services.P2PTradeResolvedEvent, error) {
	if m.resolvedEvent != nil {
		return m.resolvedEvent, nil
	}
	return &services.P2PTradeResolvedEvent{TradeID: id, ReleasedToBuyer: true,
		AmountMoved: big.NewInt(99), FeeToSystem: big.NewInt(1)}, nil
}
func (m *mockEscrow) ReadTrade(_ [32]byte) (*services.OnChainP2PTrade, error) {
	if m.onChainTrade != nil {
		return m.onChainTrade, nil
	}
	return &services.OnChainP2PTrade{Status: services.OnChainP2PStatusNone}, nil
}
func (m *mockEscrow) ReadTradePlatformFeeBps(_ [32]byte) (uint16, bool) {
	return 0, false // pre-V3 default: no platform snapshot → accrual no-op
}
func (m *mockEscrow) ReadBalance(_, _ string) (*services.OnChainBalance, error) {
	avail := m.balance.Available
	if avail == nil {
		avail = big.NewInt(0)
	}
	lck := m.balance.Locked
	if lck == nil {
		lck = big.NewInt(0)
	}
	return &services.OnChainBalance{
		Available: new(big.Int).Set(avail),
		Locked:    new(big.Int).Set(lck),
	}, nil
}
func (m *mockEscrow) ReadVoucherFilled(_ [32]byte) (*big.Int, error) { return big.NewInt(0), nil }
func (m *mockEscrow) ReadVoucherCancelled(_ [32]byte) (bool, error)  { return false, nil }
func (m *mockEscrow) HashVoucher(v services.AdVoucher) ([32]byte, error) {
	return m.hashFn(v), nil
}

// ─── Other fakes ────────────────────────────────────────────────────────────

type mockBus struct{ published []events.Event }

func (m *mockBus) Publish(ev events.Event) { m.published = append(m.published, ev) }

type mockRates struct{}

func (m *mockRates) GetSnapshot() (*services.RatesSnapshot, error) {
	return &services.RatesSnapshot{Rates: map[string]services.P2PRate{
		"NGN": {BinanceSell: 1300, MidRate: 1300},
	}}, nil
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// ─── Test rig: DB + service + router with faked auth ───────────────────────

func setupP2PTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// SQLite in-memory: each pool connection is an independent DB.
	// Pin to one connection so all queries share the same database.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	stmts := []string{
		`CREATE TABLE p2p_payment_methods (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL,
			type TEXT, provider TEXT DEFAULT 'bank_transfer',
			country TEXT, currency TEXT,
			bank_name TEXT, bank_code TEXT,
			account_number TEXT, account_name TEXT,
			verified INTEGER DEFAULT 0, is_default INTEGER DEFAULT 0,
			deleted_at DATETIME, created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE p2p_ads (
			id TEXT PRIMARY KEY, pm_id TEXT, seller_id TEXT NOT NULL,
			side TEXT, token TEXT, chain_id INTEGER,
			amount TEXT, remaining TEXT, reserved TEXT,
			fiat_currency TEXT, rate TEXT,
			rate_type TEXT NOT NULL DEFAULT 'fixed',
			float_offset_bps INTEGER,
			float_anchor TEXT NOT NULL DEFAULT 'bybit_sell',
			min_limit TEXT, max_limit TEXT,
			payment_method_ids TEXT, terms TEXT,
			seller_address TEXT, voucher_nonce TEXT,
			voucher_deadline DATETIME, voucher_hash TEXT, voucher_signature TEXT,
			on_chain_ad_id TEXT, escrow_tx_hash TEXT,
			status TEXT DEFAULT 'pending',
			expires_at DATETIME,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME
		)`,
		`CREATE TABLE p2p_trades (
			id TEXT PRIMARY KEY, ad_id TEXT NOT NULL,
			buyer_id TEXT NOT NULL, seller_id TEXT NOT NULL, opener_id TEXT,
			token TEXT, chain_id INTEGER,
			funding INTEGER NOT NULL DEFAULT 0,
			token_amount TEXT, fiat_amount TEXT,
			fiat_currency TEXT, rate TEXT,
			payment_method_id TEXT, payment_ref TEXT, nomba_inbound_event_id TEXT, nomba_payout_id TEXT, payout_account_number TEXT, payout_bank TEXT, payout_account_name TEXT,
			on_chain_trade_id TEXT UNIQUE,
			open_tx_hash TEXT, release_tx_hash TEXT,
			status TEXT DEFAULT 'pending_payment',
			proof_image_url TEXT, cancel_reason TEXT,
			ngn_reservation_released INTEGER NOT NULL DEFAULT 0,
			resolved_by_admin TEXT,
			marked_paid_at DATETIME, released_at DATETIME,
			expires_at DATETIME NOT NULL,
			expired_notified_at DATETIME,
			created_at DATETIME, updated_at DATETIME,
			CHECK (buyer_id <> seller_id)
		)`,
		`CREATE TABLE p2p_audit_logs (
			id TEXT PRIMARY KEY, actor_id TEXT, trade_id TEXT, ad_id TEXT,
			action TEXT NOT NULL, metadata TEXT, created_at DATETIME
		)`,
		`CREATE TABLE p2p_disputes (
			id TEXT PRIMARY KEY, trade_id TEXT NOT NULL UNIQUE,
			raised_by TEXT NOT NULL, raised_role TEXT NOT NULL,
			reason TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'open',
			resolved_by TEXT, resolution TEXT,
			refunded_to_buyer INTEGER, resolve_tx_hash TEXT,
			created_at DATETIME, resolved_at DATETIME
		)`,
		`CREATE TABLE p2p_messages (
			id TEXT PRIMARY KEY, trade_id TEXT NOT NULL,
			sender_id TEXT, type TEXT NOT NULL,
			body TEXT, image_url TEXT, created_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS p2p_merchant_stats (
			user_id TEXT PRIMARY KEY, released_count INTEGER DEFAULT 0, cancelled_count INTEGER DEFAULT 0,
			avg_release_secs_30d INTEGER, avg_pay_secs_30d INTEGER, thumbs_up INTEGER DEFAULT 0, thumbs_down INTEGER DEFAULT 0,
			last_seen_at DATETIME, verified BOOLEAN DEFAULT 0, trusted BOOLEAN DEFAULT 0, trusted_manual BOOLEAN DEFAULT 0, updated_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS p2p_ratings (
			id TEXT PRIMARY KEY, trade_id TEXT, rater_id TEXT, ratee_id TEXT, value INTEGER, created_at DATETIME,
			UNIQUE (trade_id, rater_id))`,
		`CREATE TABLE IF NOT EXISTS payment_merchants (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			nomba_account_id TEXT, nomba_creds_ref TEXT, nomba_wallet_id TEXT,
			inbound_account_number TEXT, inbound_bank TEXT,
			operator_wallet_address TEXT,
			nomba_sub_account_id TEXT,
			settlement_bank_code TEXT, settlement_account_number TEXT, settlement_account_name TEXT,
			available_ngn TEXT NOT NULL DEFAULT '0',
			reserved_ngn TEXT NOT NULL DEFAULT '0',
			provisioning_status TEXT NOT NULL DEFAULT 'pending',
			per_trade_cap_usd REAL NOT NULL DEFAULT 0, daily_cap_usd REAL NOT NULL DEFAULT 0,
			reviewed_by TEXT, reviewed_at DATETIME, review_note TEXT,
			created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY, name TEXT, username TEXT, email TEXT,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	return db
}

type rig struct {
	db       *gorm.DB
	escrow   *mockEscrow
	trade    *services.P2PTradeService
	handler  *P2PHandler
	router   *gin.Engine
	user     *models.User
	sellerID uuid.UUID
	sellerPK *ecdsa.PrivateKey
}

func newRig(t *testing.T) *rig {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := setupP2PTestDB(t)
	esc := newMockEscrow()
	bus := &mockBus{}
	trade := services.NewP2PTradeService(db, services.NewP2PEscrowRegistryFromMap(map[int64]services.P2PEscrowAdapter{146: esc}, "1"), &mockRates{}, bus, 146)
	trade = trade.WithClock(fixedClock{t: time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)})
	trade.SetTokenResolver(func(chainID int64, s string) string {
		if s == "USDT" {
			return "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
		}
		return ""
	})
	stats := services.NewMerchantStatsServiceNoBus(db)
	h := NewP2PHandler(db, trade, nil, nil, stats)

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey).Hex()
	sellerID := uuid.New()
	user := &models.User{
		ID:                 sellerID,
		WalletAddress:      addr,
		VerificationStatus: "verified",
		// Default rig user is a merchant — most ad-creation tests need it.
		// Tests that exercise the non-merchant path override this field.
		Role: models.RoleMerchant,
	}

	router := gin.New()
	// Auth shim: inject the test user into the gin context.
	router.Use(func(c *gin.Context) {
		c.Set("user", user)
		c.Next()
	})
	mountP2PRoutes(router, h)

	return &rig{
		db: db, escrow: esc, trade: trade, handler: h, router: router,
		user: user, sellerID: sellerID, sellerPK: key,
	}
}

// mountP2PRoutes mirrors the production route registration for the
// endpoints we cover here. Kept close to internal/routes/routes.go so any
// route shape change is caught by the tests below.
func mountP2PRoutes(r *gin.Engine, h *P2PHandler) {
	g := r.Group("/api/p2p")
	g.POST("/payment-methods", h.CreatePaymentMethod)
	g.GET("/balance", h.GetPoolBalance)
	g.GET("/allowance", h.GetAllowance)
	g.POST("/deposits/approve", h.PrepareApprove)
	g.POST("/deposits", h.PrepareDeposit)
	g.POST("/deposits/confirm", h.ConfirmDeposit)
	g.POST("/withdrawals", h.PrepareWithdraw)
	g.POST("/withdrawals/confirm", h.ConfirmWithdraw)
	g.POST("/ads", h.CreateAd)
	g.POST("/ads/:id/trades", h.OpenTrade)
	g.POST("/ads/:id/sign", h.SignAd)
	g.POST("/ads/:id/pause", h.PauseAd)
	g.POST("/ads/:id/resume", h.ResumeAd)
	g.POST("/ads/:id/cancel", h.PrepareCancelAd)
	g.POST("/ads/:id/cancel/confirm", h.ConfirmCancelAd)
	g.GET("/payment-providers", h.ListPaymentProviders)
	g.GET("/sellers/:id", h.GetSellerProfile)
	g.POST("/trades/:id/rate", h.RateTrade)
	g.GET("/trades/:id", h.GetTrade)
	g.GET("/ads/:id", h.GetAd)
}

// ─── HTTP helpers ──────────────────────────────────────────────────────────

func doJSON(t *testing.T, r *rig, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.router.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, rec.Body.String())
	}
	return out
}

func seedPaymentMethod(t *testing.T, db *gorm.DB, userID uuid.UUID, currency string) uuid.UUID {
	t.Helper()
	pm := &models.P2PPaymentMethod{
		ID:            uuid.New(),
		UserID:        userID,
		Type:          models.P2PPaymentTypeBank,
		Country:       "NG",
		Currency:      currency,
		BankName:      "Test",
		AccountNumber: "1234567890",
		AccountName:   "Test",
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed pm: %v", err)
	}
	return pm.ID
}

// ─── Payment provider catalog ───────────────────────────────────────────────

func TestListPaymentProviders_ReturnsNonEmptyData(t *testing.T) {
	r := newRig(t)
	rec := doJSON(t, r, http.MethodGet, "/api/p2p/payment-providers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if s, _ := body["success"].(bool); !s {
		t.Fatalf("expected success=true, got %v", body)
	}
	data, ok := body["data"].([]any)
	if !ok || len(data) == 0 {
		t.Fatalf("expected non-empty data array, got %v", body["data"])
	}
}

// ─── Pool balance / allowance / approve ─────────────────────────────────────

func TestGetPoolBalance_RequiresToken(t *testing.T) {
	r := newRig(t)
	rec := doJSON(t, r, http.MethodGet, "/api/p2p/balance?address="+r.user.WalletAddress, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetPoolBalance_RequiresWalletAddress(t *testing.T) {
	r := newRig(t)
	rec := doJSON(t, r, http.MethodGet, "/api/p2p/balance?token=USDT", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestGetPoolBalance_HappyPath(t *testing.T) {
	r := newRig(t)
	r.escrow.balance = services.OnChainBalance{Available: big.NewInt(123), Locked: big.NewInt(45)}
	rec := doJSON(t, r, http.MethodGet, "/api/p2p/balance?token=USDT&address="+r.user.WalletAddress, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	data := body["data"].(map[string]any)
	if data["available"] != "123" || data["locked"] != "45" {
		t.Errorf("data mismatch: %+v", data)
	}
}

func TestGetAllowance_HappyPath(t *testing.T) {
	r := newRig(t)
	r.escrow.allowance = big.NewInt(999)
	rec := doJSON(t, r, http.MethodGet, "/api/p2p/allowance?token=USDT&address="+r.user.WalletAddress, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := decode(t, rec)
	if body["data"].(map[string]any)["allowance"] != "999" {
		t.Errorf("allowance mismatch: %+v", body)
	}
}

func TestPrepareApprove_ReturnsCurrentAllowance(t *testing.T) {
	r := newRig(t)
	r.escrow.allowance = big.NewInt(500)
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/deposits/approve", map[string]any{
		"token": "USDT", "amount": "1000", "walletAddress": r.user.WalletAddress,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	data := body["data"].(map[string]any)
	if data["currentAllowance"] != "500" {
		t.Errorf("allowance: %v", data["currentAllowance"])
	}
	if data["unsignedTx"] == nil {
		t.Errorf("missing unsignedTx")
	}
}

// ─── Deposit / withdraw ─────────────────────────────────────────────────────

func TestPrepareDeposit_RejectsBadAmount(t *testing.T) {
	r := newRig(t)
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/deposits", map[string]any{
		"token": "USDT", "amount": "0", "walletAddress": r.user.WalletAddress,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (ErrInvalidParams); body=%s", rec.Code, rec.Body.String())
	}
}

func TestPrepareDeposit_RejectsMissingFields(t *testing.T) {
	r := newRig(t)
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/deposits", map[string]any{"token": "USDT", "walletAddress": r.user.WalletAddress})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	// gin's binding error includes the missing field name in the message; a
	// generic "Bad Request" with no field reference would mean the binding
	// layer was bypassed (e.g. someone added a manual unmarshal that doesn't
	// validate required tags).
	decoded := decode(t, rec)
	msg, _ := decoded["message"].(string)
	if !strings.Contains(strings.ToLower(msg), "amount") {
		t.Errorf("expected 'amount' field name in error message, got %q", msg)
	}
}

func TestConfirmDeposit_RejectsAmountMismatch(t *testing.T) {
	r := newRig(t)
	// The escrow event reports 100; the user claims they sent 1000.
	// The service-layer ConfirmDeposit cross-checks and rejects.
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/deposits/confirm", map[string]any{
		"token": "USDT", "amount": "1000", "txHash": "0xabc", "walletAddress": r.user.WalletAddress,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestConfirmDeposit_HappyPath(t *testing.T) {
	r := newRig(t)
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/deposits/confirm", map[string]any{
		"token": "USDT", "amount": "100", "txHash": "0xabc", "walletAddress": r.user.WalletAddress,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["data"].(map[string]any)["amount"] != "100" {
		t.Errorf("amount: %+v", body)
	}
}

func TestPrepareWithdraw_RejectsExceedingAvailable(t *testing.T) {
	r := newRig(t)
	r.escrow.balance = services.OnChainBalance{Available: big.NewInt(50), Locked: big.NewInt(0)}
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/withdrawals", map[string]any{
		"token": "USDT", "amount": "100", "walletAddress": r.user.WalletAddress,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// ─── Ads: create / sign / cancel ───────────────────────────────────────────

func makeCreateAdBody(pmID uuid.UUID) map[string]any {
	return map[string]any{
		"token":            "USDT",
		"tokenAmount":      "100000000",
		"fiatCurrency":     "NGN",
		"rate":             "1300",
		"rateType":         "fixed",
		"minLimit":         "1000",
		"maxLimit":         "130000",
		"paymentMethodIds": []string{pmID.String()},
		"sellerAddress":    "0x000000000000000000000000000000000000beef", // we override below for sig-valid tests
	}
}

func TestCreateAd_ReturnsTypedDataAndPending(t *testing.T) {
	r := newRig(t)
	r.escrow.balance = services.OnChainBalance{Available: big.NewInt(1_000_000_000)}
	pmID := seedPaymentMethod(t, r.db, r.sellerID, "NGN")
	body := makeCreateAdBody(pmID)
	body["sellerAddress"] = r.user.WalletAddress
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/ads", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	decoded := decode(t, rec)
	data := decoded["data"].(map[string]any)
	ad := data["ad"].(map[string]any)
	if ad["status"] != "pending" {
		t.Errorf("ad.status = %v, want pending", ad["status"])
	}
	if ad["voucherHash"] == nil || ad["voucherHash"] == "" {
		t.Errorf("ad.voucherHash missing or empty: %v", ad["voucherHash"])
	}

	// EIP-712 typed data: a wagmi/ethers signer will reject any structural
	// drift (missing domain field, reordered AdVoucher fields, etc.), so the
	// shape is part of the wire contract.
	td := data["typedData"].(map[string]any)
	if td["primaryType"] != "AdVoucher" {
		t.Errorf("primaryType = %v, want AdVoucher", td["primaryType"])
	}
	domain := td["domain"].(map[string]any)
	if domain["name"] != "TSAConnectP2PEscrow" {
		t.Errorf("domain.name = %v", domain["name"])
	}
	if domain["version"] != "1" {
		t.Errorf("domain.version = %v", domain["version"])
	}
	// JSON numbers decode as float64 — the chainId field is an int64 in Go.
	if cid, ok := domain["chainId"].(float64); !ok || int64(cid) <= 0 {
		t.Errorf("domain.chainId missing or non-positive: %v", domain["chainId"])
	}
	if vc, _ := domain["verifyingContract"].(string); !strings.HasPrefix(vc, "0x") || len(vc) != 42 {
		t.Errorf("domain.verifyingContract not a 20-byte hex address: %v", domain["verifyingContract"])
	}

	// AdVoucher type definition — field order must match AD_VOUCHER_TYPEHASH
	// in the contract; otherwise the EIP-712 digest differs and the contract
	// rejects the signature. Asserting the exact ordering here catches any
	// accidental rearrangement at the test boundary.
	types := td["types"].(map[string]any)
	voucherFields, ok := types["AdVoucher"].([]any)
	if !ok {
		t.Fatalf("types.AdVoucher missing or wrong shape: %v", types["AdVoucher"])
	}
	wantFields := []struct{ name, typ string }{
		{"seller", "address"},
		{"token", "address"},
		{"maxAmount", "uint256"},
		{"minPerTrade", "uint256"},
		{"maxPerTrade", "uint256"},
		{"deadline", "uint256"},
		{"nonce", "bytes32"},
	}
	if len(voucherFields) != len(wantFields) {
		t.Fatalf("types.AdVoucher has %d fields, want %d: %v", len(voucherFields), len(wantFields), voucherFields)
	}
	for i, want := range wantFields {
		f := voucherFields[i].(map[string]any)
		if f["name"] != want.name || f["type"] != want.typ {
			t.Errorf("types.AdVoucher[%d] = {%v,%v}, want {%s,%s}", i, f["name"], f["type"], want.name, want.typ)
		}
	}
	// EIP712Domain must also be present so wagmi can hash the domain
	// separator. (ethers strips it on the client side, but the server must
	// supply it as part of the canonical typed-data payload.)
	if _, ok := types["EIP712Domain"].([]any); !ok {
		t.Errorf("types.EIP712Domain missing")
	}

	// Message echoes the AdVoucher payload — the seller wallet must match the
	// requesting user (case-insensitive: contract emits checksummed, request
	// may be lowercase). nonce must be 0x-prefixed bytes32.
	msg := td["message"].(map[string]any)
	if !strings.EqualFold(msg["seller"].(string), r.user.WalletAddress) {
		t.Errorf("message.seller = %v, want %s", msg["seller"], r.user.WalletAddress)
	}
	if nonce, _ := msg["nonce"].(string); !strings.HasPrefix(nonce, "0x") || len(nonce) != 66 {
		t.Errorf("message.nonce not 0x-prefixed 32-byte hex: %v", msg["nonce"])
	}
	if msg["maxAmount"] != "100000000" {
		t.Errorf("message.maxAmount = %v, want 100000000", msg["maxAmount"])
	}
}

// TestCreateAd_RejectsNonMerchant: only merchant/admin tiers can publish
// ads. A regular 'user' role gets 403 before the request body is even
// validated, so any downstream resource (payment method, on-chain balance
// check) is untouched.
func TestCreateAd_RejectsNonMerchant(t *testing.T) {
	r := newRig(t)
	r.user.Role = models.RoleUser
	r.escrow.balance = services.OnChainBalance{Available: big.NewInt(1_000_000_000)}
	pmID := seedPaymentMethod(t, r.db, r.sellerID, "NGN")
	body := makeCreateAdBody(pmID)
	body["sellerAddress"] = r.user.WalletAddress
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/ads", body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	decoded := decode(t, rec)
	msg, _ := decoded["message"].(string)
	if !strings.Contains(strings.ToLower(msg), "merchant") {
		t.Errorf("expected 'merchant' in error message, got %q", msg)
	}
}

func TestCreateAd_InsufficientBalance_400(t *testing.T) {
	r := newRig(t)
	r.escrow.balance = services.OnChainBalance{Available: big.NewInt(1)}
	pmID := seedPaymentMethod(t, r.db, r.sellerID, "NGN")
	body := makeCreateAdBody(pmID)
	body["sellerAddress"] = r.user.WalletAddress
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/ads", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	// The service wraps ErrInvalidParams with "insufficient available balance"
	// so the FE can show a tailored message instead of a generic "bad
	// request". Asserting the substring guards against the wrap being lost.
	decoded := decode(t, rec)
	msg, _ := decoded["message"].(string)
	if !strings.Contains(strings.ToLower(msg), "insufficient") {
		t.Errorf("expected 'insufficient' in error message, got %q", msg)
	}
}

func TestSignAd_RequiresSellerAddress(t *testing.T) {
	r := newRig(t)
	r.escrow.balance = services.OnChainBalance{Available: big.NewInt(1_000_000_000)}
	pmID := seedPaymentMethod(t, r.db, r.sellerID, "NGN")
	createBody := makeCreateAdBody(pmID)
	createBody["sellerAddress"] = r.user.WalletAddress
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/ads", createBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d", rec.Code)
	}
	body := decode(t, rec)
	adID := body["data"].(map[string]any)["ad"].(map[string]any)["id"].(string)

	// Missing sellerAddress.
	rec = doJSON(t, r, http.MethodPost, "/api/p2p/ads/"+adID+"/sign", map[string]any{
		"signature": "0x" + strings.Repeat("00", 65),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing sellerAddress)", rec.Code)
	}
}

func TestSignAd_HappyPath(t *testing.T) {
	r := newRig(t)
	r.escrow.balance = services.OnChainBalance{Available: big.NewInt(1_000_000_000)}
	pmID := seedPaymentMethod(t, r.db, r.sellerID, "NGN")
	createBody := makeCreateAdBody(pmID)
	createBody["sellerAddress"] = r.user.WalletAddress
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/ads", createBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	adData := body["data"].(map[string]any)["ad"].(map[string]any)
	adID := adData["id"].(string)
	voucherHashHex := adData["voucherHash"].(string)

	// Sign the canonical voucher hash that the service stored.
	var hash [32]byte
	copy(hash[:], common.FromHex(voucherHashHex))
	sig, err := crypto.Sign(hash[:], r.sellerPK)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if sig[64] < 27 {
		sig[64] += 27
	}
	sigHex := "0x" + hex.EncodeToString(sig)

	rec = doJSON(t, r, http.MethodPost, "/api/p2p/ads/"+adID+"/sign", map[string]any{
		"signature":     sigHex,
		"sellerAddress": r.user.WalletAddress,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("sign status = %d; body=%s", rec.Code, rec.Body.String())
	}
	signed := decode(t, rec)
	if signed["data"].(map[string]any)["status"] != "active" {
		t.Errorf("status not active: %+v", signed["data"])
	}
}

// TestSignAd_RejectsWrongSigner covers the security-critical path: an
// attacker who learns an ad id must not be able to publish it by signing
// the voucher hash with a key they control. The recovered signer address
// is compared against the stored seller address; mismatch -> 403.
func TestSignAd_RejectsWrongSigner(t *testing.T) {
	r := newRig(t)
	r.escrow.balance = services.OnChainBalance{Available: big.NewInt(1_000_000_000)}
	pmID := seedPaymentMethod(t, r.db, r.sellerID, "NGN")
	createBody := makeCreateAdBody(pmID)
	createBody["sellerAddress"] = r.user.WalletAddress
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/ads", createBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	adData := body["data"].(map[string]any)["ad"].(map[string]any)
	adID := adData["id"].(string)
	voucherHashHex := adData["voucherHash"].(string)

	// Sign with a *different* key than the seller registered.
	attackerPK, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	var hash [32]byte
	copy(hash[:], common.FromHex(voucherHashHex))
	sig, err := crypto.Sign(hash[:], attackerPK)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if sig[64] < 27 {
		sig[64] += 27
	}
	sigHex := "0x" + hex.EncodeToString(sig)

	rec = doJSON(t, r, http.MethodPost, "/api/p2p/ads/"+adID+"/sign", map[string]any{
		"signature":     sigHex,
		"sellerAddress": r.user.WalletAddress,
	})
	// SignAd wraps ErrInvalidParams when the recovered signer differs from
	// the declared seller, so this surfaces as 400 — the request is
	// structurally valid but cryptographically wrong. The body must mention
	// the recovery mismatch so the FE can tell the user their wallet doesn't
	// match the ad's declared seller.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	decoded := decode(t, rec)
	msg, _ := decoded["message"].(string)
	// Pin the assertion to the recovery-mismatch branch specifically, not
	// the "wrong length" branch that also wraps ErrInvalidParams with
	// "signature ...". "recovered" only appears in the mismatch path.
	if !strings.Contains(strings.ToLower(msg), "recovered") {
		t.Errorf("expected 'recovered' in error message (recovery-mismatch path), got %q", msg)
	}
	// Confirm the attacker's key wasn't accepted: the stored ad must still be
	// pending (i.e. not transitioned to active).
	var ad models.P2PAd
	if err := r.db.First(&ad, "id = ?", adID).Error; err != nil {
		t.Fatalf("reload ad: %v", err)
	}
	if string(ad.Status) != "pending" {
		t.Errorf("ad.status = %q after wrong-signer sign, want pending", ad.Status)
	}
}

// TestPrepareDeposit_RejectsMalformedJSON covers the request-parsing seam:
// a malformed body must not panic or 500, it must produce a structured 400.
// (Catches anyone replacing gin's ShouldBindJSON with a raw json.Decoder
// that doesn't trap UnmarshalTypeError.)
func TestPrepareDeposit_RejectsMalformedJSON(t *testing.T) {
	r := newRig(t)
	req := httptest.NewRequest(http.MethodPost, "/api/p2p/deposits", bytes.NewReader([]byte(`{"token": "USDT", "amount":`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v; raw=%s", err, rec.Body.String())
	}
	if body["success"] != false {
		t.Errorf("success not false; body=%v", body)
	}
	// The 400 must come from gin's JSON binding layer specifically — if a
	// future pre-handler check (auth, content-length, etc.) returned 400
	// first, the binding-bypass guard this test promises would be moot.
	// gin/encoding-json surface this as "unexpected EOF" / "unexpected end".
	msg, _ := body["message"].(string)
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, "unexpected") && !strings.Contains(lower, "eof") && !strings.Contains(lower, "json") {
		t.Errorf("expected JSON-parse hint (unexpected/eof/json) in message, got %q", msg)
	}
}

func TestPauseAd_ResponseShape(t *testing.T) {
	r := newRig(t)
	signed := createSignedAd(t, r)
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/ads/"+signed+"/pause", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	// Phase 3 review fix: data field must be present (was missing before).
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("data missing on pause response; body=%v", body)
	}
	if data["status"] != "paused" {
		t.Errorf("data.status: %v", data["status"])
	}
}

// ─── Error mapping ──────────────────────────────────────────────────────────

// TestMapServiceError_StatusCodes drives mapServiceError directly with each
// sentinel so a missed case (e.g. someone adds a new ErrFoo to the service
// without wiring it into the switch) is caught at the test level instead of
// silently degrading to 500 in prod.
func TestMapServiceError_StatusCodes(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"AdNotFound", services.ErrAdNotFound, http.StatusNotFound},
		{"TradeNotFound", services.ErrTradeNotFound, http.StatusNotFound},
		{"PaymentMethodMissing", services.ErrPaymentMethodMissing, http.StatusNotFound},
		{"DisputeNotFound", services.ErrDisputeNotFound, http.StatusNotFound},
		{"Forbidden", services.ErrForbidden, http.StatusForbidden},
		{"InvalidParams", services.ErrInvalidParams, http.StatusBadRequest},
		{"AmountOutOfRange", services.ErrAmountOutOfRange, http.StatusBadRequest},
		{"AmountOverflow", services.ErrAmountOverflow, http.StatusBadRequest},
		{"UnsupportedToken", services.ErrUnsupportedToken, http.StatusBadRequest},
		{"SelfTrade", services.ErrSelfTrade, http.StatusBadRequest},
		{"AdNotActive", services.ErrAdNotActive, http.StatusConflict},
		{"InsufficientRemaining", services.ErrInsufficientRemaining, http.StatusConflict},
		{"InvalidTransition", services.ErrInvalidTransition, http.StatusConflict},
		{"AdHasOpenTrades", services.ErrAdHasOpenTrades, http.StatusConflict},
		{"DisputeAlreadyOpen", services.ErrDisputeAlreadyOpen, http.StatusConflict},
		{"RateStale", services.ErrRateStale, http.StatusServiceUnavailable},
		{"Phase2Migration", services.ErrPhase2Migration, http.StatusGone},
		{"unmapped defaults to 500", errors.New("untyped internal failure"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			if !mapServiceError(c, tc.err) {
				t.Fatalf("mapServiceError(%v) should have aborted", tc.err)
			}
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d (err=%v)", rec.Code, tc.status, tc.err)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body not JSON: %v; raw=%s", err, rec.Body.String())
			}
			if body["success"] != false {
				t.Errorf("success not false; body=%v", body)
			}
			if _, ok := body["message"].(string); !ok {
				t.Errorf("message missing or non-string; body=%v", body)
			}
		})
	}
	// nil error -> no abort, no response.
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	if mapServiceError(c, nil) {
		t.Errorf("mapServiceError(nil) should not abort")
	}
}

func TestPrepareCancelAd_BlockedByOpenTrades(t *testing.T) {
	r := newRig(t)
	adID := createSignedAd(t, r)
	// Seed an open trade against the ad so cancel is refused.
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	trade := &models.P2PTrade{
		ID: uuid.New(), AdID: uuid.MustParse(adID), BuyerID: uuid.New(), SellerID: r.sellerID,
		Token: "USDT", ChainID: 146,
		TokenAmount: "1000000", FiatAmount: "1300", FiatCurrency: "NGN", Rate: "1300",
		PaymentMethodID: uuid.New(),
		Status:          models.P2PTradeStatusPendingPayment,
		ExpiresAt:       now.Add(30 * time.Minute),
	}
	if err := r.db.Create(trade).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/ads/"+adID+"/cancel", map[string]any{
		"sellerAddress": r.user.WalletAddress,
	})
	// ErrAdHasOpenTrades → 409 Conflict.
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 (ErrAdHasOpenTrades), got %d; body=%s", rec.Code, rec.Body.String())
	}
}

// TestCreatePaymentMethod_InvalidProvider: provider is the required input and
// is validated against the catalog. A slug not in the catalog is rejected with
// 400 before any row is written, so type can never be derived from a bogus
// provider.
func TestCreatePaymentMethod_InvalidProvider(t *testing.T) {
	r := newRig(t)
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/payment-methods", map[string]any{
		"provider":      "not_a_real_provider",
		"country":       "NG",
		"currency":      "NGN",
		"bankName":      "Test Bank",
		"accountNumber": "1234567890",
		"accountName":   "Test User",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	decoded := decode(t, rec)
	if msg, _ := decoded["message"].(string); msg != "Invalid payment provider" {
		t.Errorf("message = %q, want %q", msg, "Invalid payment provider")
	}
}

// ─── Buy-ad (two-sided) handler tests ──────────────────────────────────────

func makeBuyAdBody(pmID uuid.UUID) map[string]any {
	b := makeCreateAdBody(pmID)
	b["side"] = "buy"
	return b
}

// TestCreateBuyAd_OK: a merchant posts a buy-ad. It comes back active (no
// signing step) with no typed-data and side=buy.
func TestCreateBuyAd_OK(t *testing.T) {
	r := newRig(t)
	// No deposited balance needed for a buy-ad.
	r.escrow.balance = services.OnChainBalance{Available: big.NewInt(0)}
	pmID := seedPaymentMethod(t, r.db, r.sellerID, "NGN")
	body := makeBuyAdBody(pmID)
	body["sellerAddress"] = r.user.WalletAddress
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/ads", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	decoded := decode(t, rec)
	data := decoded["data"].(map[string]any)
	ad := data["ad"].(map[string]any)
	if ad["side"] != "buy" {
		t.Errorf("ad.side = %v, want buy", ad["side"])
	}
	if ad["status"] != "active" {
		t.Errorf("ad.status = %v, want active", ad["status"])
	}
	if td := data["typedData"]; td != nil {
		t.Errorf("buy-ad should have no typedData, got %v", td)
	}
}

func TestCreateAd_RejectsInvalidSide(t *testing.T) {
	r := newRig(t)
	r.escrow.balance = services.OnChainBalance{Available: big.NewInt(1_000_000_000)}
	pmID := seedPaymentMethod(t, r.db, r.sellerID, "NGN")
	body := makeCreateAdBody(pmID)
	body["sellerAddress"] = r.user.WalletAddress
	body["side"] = "sideways"
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/ads", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// createBuyAd posts a buy-ad as the rig merchant and returns its id.
func createBuyAd(t *testing.T, r *rig) string {
	t.Helper()
	r.escrow.balance = services.OnChainBalance{Available: big.NewInt(0)}
	pmID := seedPaymentMethod(t, r.db, r.sellerID, "NGN")
	body := makeBuyAdBody(pmID)
	body["sellerAddress"] = r.user.WalletAddress
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/ads", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create buy-ad: status %d body %s", rec.Code, rec.Body.String())
	}
	return decode(t, rec)["data"].(map[string]any)["ad"].(map[string]any)["id"].(string)
}

// TestOpenBuyAdTrade_DepositPath: a (different) user opens the merchant's
// buy-ad. The handler must drive the service down the JIT deposit path with
// the right roles + wallets.
func TestOpenBuyAdTrade_DepositPath(t *testing.T) {
	r := newRig(t)
	adID := createBuyAd(t, r)

	// Switch the request user to the opener (a regular user selling crypto).
	opener := &models.User{
		ID:                 uuid.New(),
		WalletAddress:      "0x000000000000000000000000000000000000beef",
		VerificationStatus: "verified",
		Role:               models.RoleUser,
	}
	*r.user = *opener

	// On a buy-ad, the PM must be owned by the TAKER (opener), who receives the
	// fiat — not the merchant's allowlisted PM. Seed one for the opener.
	openerPm := seedPaymentMethod(t, r.db, opener.ID, "NGN")
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/ads/"+adID+"/trades", map[string]any{
		"fiatAmount":      "1300",
		"paymentMethodId": openerPm.String(),
		"sellerAddress":   opener.WalletAddress,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("open buy-ad trade status = %d; body=%s", rec.Code, rec.Body.String())
	}
	// The adapter must have been driven down the JIT deposit path with
	// from=opener wallet and merchant=ad poster wallet.
	if r.escrow.lastOpenTradeDepositArgs == nil {
		t.Fatalf("deposit path not taken — PrepareOpenTradeWithDeposit not called")
	}
	if !strings.EqualFold(r.escrow.lastOpenTradeDepositArgs.From, opener.WalletAddress) {
		t.Errorf("deposit from = %s, want opener %s", r.escrow.lastOpenTradeDepositArgs.From, opener.WalletAddress)
	}
	// Persisted trade: SellerID = opener (depositor), BuyerID = merchant.
	var trade models.P2PTrade
	if err := r.db.First(&trade, "ad_id = ?", adID).Error; err != nil {
		t.Fatalf("reload trade: %v", err)
	}
	if trade.SellerID != opener.ID {
		t.Errorf("trade.SellerID = %s, want opener %s", trade.SellerID, opener.ID)
	}
	if trade.BuyerID != r.sellerID {
		t.Errorf("trade.BuyerID = %s, want merchant %s", trade.BuyerID, r.sellerID)
	}
	if trade.Funding != models.P2PTradeFundingDepositedByTaker {
		t.Errorf("trade.Funding = %d, want DepositedByTaker", trade.Funding)
	}
}

// TestOpenBuyAdTrade_RejectsMerchantSelfTake: the merchant cannot take their
// own buy-ad → 400 (ErrSelfTrade).
func TestOpenBuyAdTrade_RejectsMerchantSelfTake(t *testing.T) {
	r := newRig(t)
	adID := createBuyAd(t, r)
	// r.user is still the merchant (ad poster).
	var ad models.P2PAd
	if err := r.db.First(&ad, "id = ?", adID).Error; err != nil {
		t.Fatalf("reload ad: %v", err)
	}
	var pmID string
	if len(ad.PaymentMethodIDs) > 0 {
		pmID = string(ad.PaymentMethodIDs[0])
	}
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/ads/"+adID+"/trades", map[string]any{
		"fiatAmount":      "1300",
		"paymentMethodId": pmID,
		"sellerAddress":   r.user.WalletAddress,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (ErrSelfTrade); body=%s", rec.Code, rec.Body.String())
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// createSignedAd posts an ad, signs the voucher with the rig's key, and
// returns the ad id (status: active). Used as a fixture by tests that need
// to operate against a published ad without re-implementing the dance.
func createSignedAd(t *testing.T, r *rig) string {
	t.Helper()
	r.escrow.balance = services.OnChainBalance{Available: big.NewInt(1_000_000_000)}
	pmID := seedPaymentMethod(t, r.db, r.sellerID, "NGN")
	createBody := makeCreateAdBody(pmID)
	createBody["sellerAddress"] = r.user.WalletAddress
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/ads", createBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d body %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	adData := body["data"].(map[string]any)["ad"].(map[string]any)
	adID := adData["id"].(string)
	voucherHashHex := adData["voucherHash"].(string)

	var hash [32]byte
	copy(hash[:], common.FromHex(voucherHashHex))
	sig, err := crypto.Sign(hash[:], r.sellerPK)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if sig[64] < 27 {
		sig[64] += 27
	}
	sigHex := "0x" + hex.EncodeToString(sig)
	rec = doJSON(t, r, http.MethodPost, "/api/p2p/ads/"+adID+"/sign", map[string]any{
		"signature":     sigHex,
		"sellerAddress": r.user.WalletAddress,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("sign: status %d body %s", rec.Code, rec.Body.String())
	}
	return adID
}

// ─── Seller profile & rate-trade tests ────────────────────────────────────

func TestRateTrade_RejectsNonReleased(t *testing.T) {
	r := newRig(t)
	tradeID := uuid.New()
	if err := r.db.Create(&models.P2PTrade{ID: tradeID, BuyerID: r.sellerID, SellerID: uuid.New(),
		Status: models.P2PTradeStatusPaid}).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/trades/"+tradeID.String()+"/rate", map[string]any{"value": 1})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRateTrade_HappyAndDuplicate(t *testing.T) {
	r := newRig(t)
	tradeID := uuid.New()
	if err := r.db.Create(&models.P2PTrade{ID: tradeID, BuyerID: r.sellerID, SellerID: uuid.New(),
		Status: models.P2PTradeStatusReleased}).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/trades/"+tradeID.String()+"/rate", map[string]any{"value": 1})
	if rec.Code != http.StatusCreated {
		t.Fatalf("first rate status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	rec2 := doJSON(t, r, http.MethodPost, "/api/p2p/trades/"+tradeID.String()+"/rate", map[string]any{"value": 1})
	if rec2.Code != http.StatusConflict {
		t.Fatalf("dup rate status = %d, want 409; body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestRateTrade_RejectsNonParty(t *testing.T) {
	r := newRig(t)
	tradeID := uuid.New()
	// rig.user (r.sellerID) is neither buyer nor seller here.
	if err := r.db.Create(&models.P2PTrade{ID: tradeID, BuyerID: uuid.New(), SellerID: uuid.New(),
		Status: models.P2PTradeStatusReleased}).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/trades/"+tradeID.String()+"/rate", map[string]any{"value": 1})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetSellerProfile_OK(t *testing.T) {
	r := newRig(t)
	rec := doJSON(t, r, http.MethodGet, "/api/p2p/sellers/"+uuid.New().String(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// ─── PM detail enrichment (isPm / pmPayTo / payerName) ─────────────────────

// seedPMTrade seeds a payment_merchants row + a PM ad (pm_id set) + a trade on
// it where the rig user (r.sellerID) is the BUYER, plus a users row carrying
// the buyer's KYC name. Returns the trade id.
func seedPMTrade(t *testing.T, r *rig, payerName string) uuid.UUID {
	t.Helper()
	pmID := uuid.New()
	if err := r.db.Exec(
		`INSERT INTO payment_merchants (id, user_id, name, status, inbound_account_number, inbound_bank) VALUES (?,?,?,?,?,?)`,
		pmID.String(), uuid.New().String(), "TSA Connect", models.PaymentMerchantStatusActive, "1234567890", "Nomba",
	).Error; err != nil {
		t.Fatalf("seed payment_merchant: %v", err)
	}
	adID := uuid.New()
	if err := r.db.Create(&models.P2PAd{ID: adID, PMID: &pmID, SellerID: uuid.New(), Side: models.P2PAdSideSell,
		Token: "USDT", FiatCurrency: "NGN", Amount: "0", Remaining: "0", Reserved: "0", Rate: "1500",
		Status: models.P2PAdStatusActive}).Error; err != nil {
		t.Fatalf("seed pm ad: %v", err)
	}
	// rig user is the buyer; give them a users row with a KYC name.
	if err := r.db.Exec(`INSERT INTO users (id, name) VALUES (?,?)`, r.sellerID.String(), payerName).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	tradeID := uuid.New()
	if err := r.db.Create(&models.P2PTrade{ID: tradeID, AdID: adID, BuyerID: r.sellerID, SellerID: uuid.New(),
		Token: "USDT", TokenAmount: "1000000", FiatAmount: "1500", FiatCurrency: "NGN", Rate: "1500",
		ExpiresAt: time.Now().Add(time.Hour), Status: models.P2PTradeStatusPendingPayment}).Error; err != nil {
		t.Fatalf("seed pm trade: %v", err)
	}
	return tradeID
}

func TestGetTrade_PMFields(t *testing.T) {
	r := newRig(t)
	tradeID := seedPMTrade(t, r, "JOHN DOE")
	rec := doJSON(t, r, http.MethodGet, "/api/p2p/trades/"+tradeID.String(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	data, _ := body["data"].(map[string]any)
	if data == nil {
		t.Fatalf("no data in body=%s", rec.Body.String())
	}
	if data["isPm"] != true {
		t.Fatalf("isPm = %v, want true; body=%s", data["isPm"], rec.Body.String())
	}
	if data["payerName"] != "JOHN DOE" {
		t.Fatalf("payerName = %v, want JOHN DOE; body=%s", data["payerName"], rec.Body.String())
	}
	payTo, _ := data["pmPayTo"].(map[string]any)
	if payTo == nil {
		t.Fatalf("pmPayTo missing; body=%s", rec.Body.String())
	}
	if payTo["accountNumber"] != "1234567890" {
		t.Fatalf("accountNumber = %v, want 1234567890", payTo["accountNumber"])
	}
	if payTo["accountName"] != "TSA Connect" {
		t.Fatalf("accountName = %v, want TSA Connect", payTo["accountName"])
	}
	if payTo["bank"] != "Nomba" {
		t.Fatalf("bank = %v, want Nomba", payTo["bank"])
	}
}

func TestGetTrade_NonPM(t *testing.T) {
	r := newRig(t)
	// Ad with no pm_id → non-PM trade.
	adID := uuid.New()
	if err := r.db.Create(&models.P2PAd{ID: adID, SellerID: uuid.New(), Side: models.P2PAdSideSell,
		Token: "USDT", FiatCurrency: "NGN", Amount: "0", Remaining: "0", Reserved: "0", Rate: "1500",
		Status: models.P2PAdStatusActive}).Error; err != nil {
		t.Fatalf("seed ad: %v", err)
	}
	tradeID := uuid.New()
	if err := r.db.Create(&models.P2PTrade{ID: tradeID, AdID: adID, BuyerID: r.sellerID, SellerID: uuid.New(),
		Token: "USDT", TokenAmount: "1000000", FiatAmount: "1500", FiatCurrency: "NGN", Rate: "1500",
		ExpiresAt: time.Now().Add(time.Hour), Status: models.P2PTradeStatusPendingPayment}).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
	rec := doJSON(t, r, http.MethodGet, "/api/p2p/trades/"+tradeID.String(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	data, _ := body["data"].(map[string]any)
	if data["isPm"] != false {
		t.Fatalf("isPm = %v, want false; body=%s", data["isPm"], rec.Body.String())
	}
	if _, present := data["pmPayTo"]; present {
		t.Fatalf("pmPayTo should be absent for non-PM trade; body=%s", rec.Body.String())
	}
}

func TestGetAd_PMFields(t *testing.T) {
	r := newRig(t)
	pmID := uuid.New()
	if err := r.db.Exec(
		`INSERT INTO payment_merchants (id, user_id, name, status, inbound_account_number, inbound_bank) VALUES (?,?,?,?,?,?)`,
		pmID.String(), uuid.New().String(), "TSA Connect", models.PaymentMerchantStatusActive, "1234567890", "Nomba",
	).Error; err != nil {
		t.Fatalf("seed payment_merchant: %v", err)
	}
	adID := uuid.New()
	if err := r.db.Create(&models.P2PAd{ID: adID, PMID: &pmID, SellerID: uuid.New(), Side: models.P2PAdSideSell,
		Token: "USDT", FiatCurrency: "NGN", Amount: "1000000", Remaining: "1000000", Reserved: "0", Rate: "1500",
		RateType: models.P2PAdRateTypeFixed, FloatAnchor: models.P2PAdFloatAnchorBybitSell,
		Status: models.P2PAdStatusActive}).Error; err != nil {
		t.Fatalf("seed pm ad: %v", err)
	}
	rec := doJSON(t, r, http.MethodGet, "/api/p2p/ads/"+adID.String(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	data, _ := body["data"].(map[string]any)
	if data["isPm"] != true {
		t.Fatalf("isPm = %v, want true; body=%s", data["isPm"], rec.Body.String())
	}
	payTo, _ := data["pmPayTo"].(map[string]any)
	if payTo == nil {
		t.Fatalf("pmPayTo missing; body=%s", rec.Body.String())
	}
	if payTo["accountNumber"] != "1234567890" || payTo["accountName"] != "TSA Connect" || payTo["bank"] != "Nomba" {
		t.Fatalf("pmPayTo = %v; body=%s", payTo, rec.Body.String())
	}
}

// Compile-time guard: mockEscrow implements P2PEscrowAdapter so we catch
// interface drift if the adapter surface ever changes.
var _ services.P2PEscrowAdapter = (*mockEscrow)(nil)
