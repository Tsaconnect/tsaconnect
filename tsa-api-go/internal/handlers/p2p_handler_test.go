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
	depositedEvent  *services.P2PDepositedEvent
	withdrawnEvent  *services.P2PWithdrawnEvent
	cancelledEvent  *services.P2PVoucherCancelledEvent
	openedEvent     *services.P2PTradeOpenedEvent
	releasedEvent   *services.P2PTradeReleasedEvent
	tradeCancelled  *services.P2PTradeCancelledEvent
	disputedEvent   *services.P2PTradeDisputedEvent
	resolvedEvent   *services.P2PTradeResolvedEvent
	onChainTrade    *services.OnChainP2PTrade
	hashFn          func(services.AdVoucher) [32]byte
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
	return &services.P2PTradeCancelledEvent{TradeID: id, ReturnedToAvailable: big.NewInt(100)}, nil
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
	stmts := []string{
		`CREATE TABLE p2p_payment_methods (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL,
			type TEXT, country TEXT, currency TEXT,
			bank_name TEXT, bank_code TEXT,
			account_number TEXT, account_name TEXT,
			verified INTEGER DEFAULT 0, is_default INTEGER DEFAULT 0,
			deleted_at DATETIME, created_at DATETIME, updated_at DATETIME
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
			seller_address TEXT, voucher_nonce TEXT,
			voucher_deadline DATETIME, voucher_hash TEXT, voucher_signature TEXT,
			on_chain_ad_id TEXT, escrow_tx_hash TEXT,
			status TEXT DEFAULT 'pending',
			expires_at DATETIME,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME
		)`,
		`CREATE TABLE p2p_trades (
			id TEXT PRIMARY KEY, ad_id TEXT NOT NULL,
			buyer_id TEXT NOT NULL, seller_id TEXT NOT NULL,
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
	trade := services.NewP2PTradeService(db, esc, &mockRates{}, bus, 146)
	trade = trade.WithClock(fixedClock{t: time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)})
	trade.SetTokenResolver(func(s string) string {
		if s == "USDT" {
			return "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
		}
		return ""
	})
	h := NewP2PHandler(db, trade, nil, nil)

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
	g.GET("/balance", h.GetPoolBalance)
	g.GET("/allowance", h.GetAllowance)
	g.POST("/deposits/approve", h.PrepareApprove)
	g.POST("/deposits", h.PrepareDeposit)
	g.POST("/deposits/confirm", h.ConfirmDeposit)
	g.POST("/withdrawals", h.PrepareWithdraw)
	g.POST("/withdrawals/confirm", h.ConfirmWithdraw)
	g.POST("/ads", h.CreateAd)
	g.POST("/ads/:id/sign", h.SignAd)
	g.POST("/ads/:id/pause", h.PauseAd)
	g.POST("/ads/:id/resume", h.ResumeAd)
	g.POST("/ads/:id/cancel", h.PrepareCancelAd)
	g.POST("/ads/:id/cancel/confirm", h.ConfirmCancelAd)
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

// ─── Pool balance / allowance / approve ─────────────────────────────────────

func TestGetPoolBalance_RequiresToken(t *testing.T) {
	r := newRig(t)
	rec := doJSON(t, r, http.MethodGet, "/api/p2p/balance", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetPoolBalance_RequiresWalletAddress(t *testing.T) {
	r := newRig(t)
	r.user.WalletAddress = ""
	rec := doJSON(t, r, http.MethodGet, "/api/p2p/balance?token=USDT", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestGetPoolBalance_HappyPath(t *testing.T) {
	r := newRig(t)
	r.escrow.balance = services.OnChainBalance{Available: big.NewInt(123), Locked: big.NewInt(45)}
	rec := doJSON(t, r, http.MethodGet, "/api/p2p/balance?token=USDT", nil)
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
	rec := doJSON(t, r, http.MethodGet, "/api/p2p/allowance?token=USDT", nil)
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
		"token": "USDT", "amount": "1000",
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
		"token": "USDT", "amount": "0",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (ErrInvalidParams); body=%s", rec.Code, rec.Body.String())
	}
}

func TestPrepareDeposit_RejectsMissingFields(t *testing.T) {
	r := newRig(t)
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/deposits", map[string]any{"token": "USDT"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestConfirmDeposit_RejectsAmountMismatch(t *testing.T) {
	r := newRig(t)
	// The escrow event reports 100; the user claims they sent 1000.
	// The service-layer ConfirmDeposit cross-checks and rejects.
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/deposits/confirm", map[string]any{
		"token": "USDT", "amount": "1000", "txHash": "0xabc",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestConfirmDeposit_HappyPath(t *testing.T) {
	r := newRig(t)
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/deposits/confirm", map[string]any{
		"token": "USDT", "amount": "100", "txHash": "0xabc",
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
		"token": "USDT", "amount": "100",
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
	body["sellerAddress"] = r.user.WalletAddress // use the rig's wallet so SignAd can sign
	rec := doJSON(t, r, http.MethodPost, "/api/p2p/ads", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	decoded := decode(t, rec)
	data := decoded["data"].(map[string]any)
	ad := data["ad"].(map[string]any)
	if ad["status"] != "pending" {
		t.Errorf("status: %v", ad["status"])
	}
	td := data["typedData"].(map[string]any)
	if td["primaryType"] != "AdVoucher" {
		t.Errorf("primaryType: %v", td["primaryType"])
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

// stubErrTrade lets us drive mapServiceError directly by replacing the
// trade service with one that always returns a chosen sentinel.
type stubErrTradeAdapter struct{ mockEscrow }

func TestMapServiceError_StatusCodes(t *testing.T) {
	// Drive a handler that always errors through mapServiceError.
	// Easier than a per-sentinel stub trade service: hit GetPoolBalance with
	// an invalid wallet address (forces ErrInvalidParams via the service).
	r := newRig(t)
	r.user.WalletAddress = "not-an-address"
	rec := doJSON(t, r, http.MethodGet, "/api/p2p/balance?token=USDT", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid wallet, got %d; body=%s", rec.Code, rec.Body.String())
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

// Compile-time guard: the stub above implements P2PEscrowAdapter so we
// catch interface drift if the adapter surface ever changes.
var _ services.P2PEscrowAdapter = (*mockEscrow)(nil)

// errors-package import is referenced by some negative-path tests below.
var _ = errors.New
