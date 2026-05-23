// Pool-model trade-service tests. Focused on the new voucher / deposit /
// openTrade flow; legacy lock-on-publish tests were removed in the Phase 2B
// migration since the underlying methods no longer exist.

package services

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ─── Mock escrow (new pool-model adapter) ──────────────────────────────────

type mockEscrow struct {
	contractAddress string
	balance         OnChainBalance
	balanceErr      error
	voucherFilled   *big.Int
	voucherCanc     bool
	hashFunc        func(AdVoucher) [32]byte
	preparedTxs     []string
	depositedEvent  *P2PDepositedEvent
	withdrawnEvent  *P2PWithdrawnEvent
	cancelledEvent  *P2PVoucherCancelledEvent
	openedEvent     *P2PTradeOpenedEvent
	releasedEvent   *P2PTradeReleasedEvent
	tradeCancelled  *P2PTradeCancelledEvent
	disputedEvent   *P2PTradeDisputedEvent
	resolvedEvent   *P2PTradeResolvedEvent
	onChainTrade    *OnChainP2PTrade
}

func newMockEscrow() *mockEscrow {
	return &mockEscrow{
		contractAddress: "0xb5B073BF578fE24311ED50e540D9d632f1679261",
		balance:         OnChainBalance{Available: big.NewInt(0), Locked: big.NewInt(0)},
		voucherFilled:   big.NewInt(0),
		hashFunc:        defaultHashVoucher,
	}
}

// defaultHashVoucher mimics the contract's EIP-712 digest deterministically
// so SignAd's signature recovery has a stable target to verify against.
func defaultHashVoucher(v AdVoucher) [32]byte {
	domain := crypto.Keccak256Hash(
		[]byte("EIP712Domain"), []byte("TSAConnectP2PEscrow"), []byte("1"),
		v.Seller.Bytes(),
	)
	body := crypto.Keccak256Hash(
		v.Seller.Bytes(), v.Token.Bytes(),
		v.MaxAmount.Bytes(), v.MinPerTrade.Bytes(), v.MaxPerTrade.Bytes(),
		v.Deadline.Bytes(), v.Nonce[:],
	)
	return crypto.Keccak256Hash(domain[:], body[:])
}

func (m *mockEscrow) ContractAddress() string { return m.contractAddress }

func (m *mockEscrow) PrepareDeposit(seller, token string, amount *big.Int) ([]byte, error) {
	m.preparedTxs = append(m.preparedTxs, "deposit")
	return []byte(`{"tx":"deposit"}`), nil
}
func (m *mockEscrow) PrepareWithdraw(seller, token string, amount *big.Int) ([]byte, error) {
	m.preparedTxs = append(m.preparedTxs, "withdraw")
	return []byte(`{"tx":"withdraw"}`), nil
}
func (m *mockEscrow) PrepareCancelVoucher(seller string, v AdVoucher) ([]byte, error) {
	m.preparedTxs = append(m.preparedTxs, "cancelVoucher")
	return []byte(`{"tx":"cancelVoucher"}`), nil
}
func (m *mockEscrow) PrepareOpenTradeWithVoucher(tradeId [32]byte, v AdVoucher, sig []byte, buyer string, amount *big.Int) ([]byte, error) {
	m.preparedTxs = append(m.preparedTxs, "openTrade")
	return []byte(`{"tx":"openTrade"}`), nil
}
func (m *mockEscrow) PrepareMarkPaid(tradeId [32]byte, buyer string) ([]byte, error) {
	return []byte(`{"tx":"markPaid"}`), nil
}
func (m *mockEscrow) PrepareRelease(tradeId [32]byte, seller string) ([]byte, error) {
	return []byte(`{"tx":"release"}`), nil
}
func (m *mockEscrow) PrepareCancelByTimeout(tradeId [32]byte, caller string) ([]byte, error) {
	return []byte(`{"tx":"cancelByTimeout"}`), nil
}
func (m *mockEscrow) PrepareRaiseDispute(tradeId [32]byte, by string) ([]byte, error) {
	return []byte(`{"tx":"raiseDispute"}`), nil
}
func (m *mockEscrow) PrepareAdminResolve(tradeId [32]byte, refundBuyer bool, admin string) ([]byte, error) {
	return []byte(`{"tx":"adminResolve"}`), nil
}

func (m *mockEscrow) VerifyDeposited(txHash string, seller, token common.Address) (*P2PDepositedEvent, error) {
	if m.depositedEvent != nil {
		return m.depositedEvent, nil
	}
	return &P2PDepositedEvent{Seller: seller, Token: token, Amount: big.NewInt(100)}, nil
}
func (m *mockEscrow) VerifyWithdrawn(txHash string, seller, token common.Address) (*P2PWithdrawnEvent, error) {
	if m.withdrawnEvent != nil {
		return m.withdrawnEvent, nil
	}
	return &P2PWithdrawnEvent{Seller: seller, Token: token, Amount: big.NewInt(50)}, nil
}
func (m *mockEscrow) VerifyVoucherCancelled(txHash string, hash [32]byte) (*P2PVoucherCancelledEvent, error) {
	if m.cancelledEvent != nil {
		return m.cancelledEvent, nil
	}
	return &P2PVoucherCancelledEvent{VoucherHash: hash}, nil
}
func (m *mockEscrow) VerifyTradeOpened(txHash string, tradeId [32]byte) (*P2PTradeOpenedEvent, error) {
	if m.openedEvent != nil {
		return m.openedEvent, nil
	}
	return &P2PTradeOpenedEvent{TradeID: tradeId, Buyer: common.HexToAddress("0xb000"), Amount: big.NewInt(100)}, nil
}
func (m *mockEscrow) VerifyTradeReleased(txHash string, tradeId [32]byte) (*P2PTradeReleasedEvent, error) {
	if m.releasedEvent != nil {
		return m.releasedEvent, nil
	}
	return &P2PTradeReleasedEvent{TradeID: tradeId, AmountToBuyer: big.NewInt(99), FeeToSystem: big.NewInt(1)}, nil
}
func (m *mockEscrow) VerifyTradeCancelled(txHash string, tradeId [32]byte) (*P2PTradeCancelledEvent, error) {
	if m.tradeCancelled != nil {
		return m.tradeCancelled, nil
	}
	return &P2PTradeCancelledEvent{TradeID: tradeId, ReturnedToAvailable: big.NewInt(100)}, nil
}
func (m *mockEscrow) VerifyTradeDisputed(txHash string, tradeId [32]byte) (*P2PTradeDisputedEvent, error) {
	if m.disputedEvent != nil {
		return m.disputedEvent, nil
	}
	return &P2PTradeDisputedEvent{TradeID: tradeId, By: common.HexToAddress("0xb000")}, nil
}
func (m *mockEscrow) VerifyTradeResolved(txHash string, tradeId [32]byte) (*P2PTradeResolvedEvent, error) {
	if m.resolvedEvent != nil {
		return m.resolvedEvent, nil
	}
	return &P2PTradeResolvedEvent{TradeID: tradeId, ReleasedToBuyer: true,
		AmountMoved: big.NewInt(99), FeeToSystem: big.NewInt(1)}, nil
}
func (m *mockEscrow) ReadTrade(tradeId [32]byte) (*OnChainP2PTrade, error) {
	if m.onChainTrade != nil {
		return m.onChainTrade, nil
	}
	return &OnChainP2PTrade{Status: OnChainP2PStatusNone}, nil
}
func (m *mockEscrow) ReadBalance(seller, token string) (*OnChainBalance, error) {
	if m.balanceErr != nil {
		return nil, m.balanceErr
	}
	avail := m.balance.Available
	if avail == nil {
		avail = big.NewInt(0)
	}
	lck := m.balance.Locked
	if lck == nil {
		lck = big.NewInt(0)
	}
	return &OnChainBalance{
		Available: new(big.Int).Set(avail),
		Locked:    new(big.Int).Set(lck),
	}, nil
}
func (m *mockEscrow) ReadVoucherFilled(h [32]byte) (*big.Int, error) {
	return new(big.Int).Set(m.voucherFilled), nil
}
func (m *mockEscrow) ReadVoucherCancelled(h [32]byte) (bool, error) { return m.voucherCanc, nil }
func (m *mockEscrow) HashVoucher(v AdVoucher) ([32]byte, error)     { return m.hashFunc(v), nil }

// ─── Other test fakes (rates, bus, clock) ──────────────────────────────────

type mockRates struct{ stale bool }

func (m *mockRates) GetSnapshot() (*RatesSnapshot, error) {
	if m.stale {
		return &RatesSnapshot{StaleSeconds: 600, Rates: map[string]P2PRate{
			"NGN": {Stale: true, BinanceSell: 1300, MidRate: 1300},
		}}, nil
	}
	return &RatesSnapshot{Rates: map[string]P2PRate{
		"NGN": {BinanceSell: 1300, MidRate: 1300},
	}}, nil
}

type mockBus struct{ published []events.Event }

func (m *mockBus) Publish(ev events.Event) { m.published = append(m.published, ev) }

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// ─── Test DB ────────────────────────────────────────────────────────────────

func setupTradeDB(t *testing.T) *gorm.DB {
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
			t.Fatalf("create table: %v\nSQL=%s", err, s)
		}
	}
	return db
}

func seedPaymentMethod(t *testing.T, db *gorm.DB, userID uuid.UUID, currency string) uuid.UUID {
	t.Helper()
	pm := &models.P2PPaymentMethod{
		ID:            uuid.New(),
		UserID:        userID,
		Type:          models.P2PPaymentTypeBank,
		Country:       "NG",
		Currency:      currency,
		BankName:      "Test Bank",
		AccountNumber: "1234567890",
		AccountName:   "Test User",
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed pm: %v", err)
	}
	return pm.ID
}

func newService(t *testing.T, db *gorm.DB) (*P2PTradeService, *mockEscrow, *mockBus) {
	t.Helper()
	esc := newMockEscrow()
	bus := &mockBus{}
	svc := NewP2PTradeService(db, esc, &mockRates{}, bus, 146)
	svc = svc.WithClock(fixedClock{t: time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)})
	svc.SetTokenResolver(func(symbol string) string {
		if symbol == "USDT" {
			return "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
		}
		return ""
	})
	return svc, esc, bus
}

// signVoucherForTest produces a valid signature over `hash` using the given
// private key. Matches the on-chain ECDSA.recover format (low-S enforced).
func signVoucherForTest(t *testing.T, hash [32]byte, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	sig, err := crypto.Sign(hash[:], key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// crypto.Sign already produces low-S sigs. Normalise v to 27/28.
	if sig[64] < 27 {
		sig[64] += 27
	}
	return sig
}

func newSellerKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	addr := crypto.PubkeyToAddress(k.PublicKey).Hex()
	return k, addr
}

// makeCreateAdParams returns CreateAdParams for a 100 USDT sell ad in NGN.
func makeCreateAdParams(pmID uuid.UUID) CreateAdParams {
	return CreateAdParams{
		Token:            "USDT",
		TokenAmount:      "100000000", // 100 USDT (6 dec)
		FiatCurrency:     "NGN",
		Rate:             "1300",
		RateType:         "fixed",
		MinLimit:         "1000",
		MaxLimit:         "130000",
		PaymentMethodIDs: []uuid.UUID{pmID},
	}
}

// ─── CreateAd ──────────────────────────────────────────────────────────────

func TestCreateAd_HappyPath_ReturnsTypedDataAndPersistsPending(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000), Locked: big.NewInt(0)}
	_, sellerAddr := newSellerKey(t)

	res, err := svc.CreateAd(context.Background(), sellerID, sellerAddr, makeCreateAdParams(pmID))
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	if res.Ad.Status != models.P2PAdStatusPending {
		t.Errorf("status = %q, want pending", res.Ad.Status)
	}
	if res.Ad.VoucherHash == "" || res.Ad.VoucherNonce == "" || res.Ad.SellerAddress == "" {
		t.Errorf("voucher fields not populated: %+v", res.Ad)
	}
	if res.TypedData == nil || res.TypedData.PrimaryType != "AdVoucher" {
		t.Fatalf("typed data missing or wrong primary type: %+v", res.TypedData)
	}
	if res.TypedData.Domain.VerifyingContract != esc.ContractAddress() {
		t.Errorf("verifyingContract mismatch")
	}
}

func TestCreateAd_RejectsInsufficientBalance(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	esc.balance = OnChainBalance{Available: big.NewInt(1_000), Locked: big.NewInt(0)} // way less than 100 USDT
	_, sellerAddr := newSellerKey(t)

	_, err := svc.CreateAd(context.Background(), sellerID, sellerAddr, makeCreateAdParams(pmID))
	if err == nil || !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
}

func TestCreateAd_RejectsStaleRate(t *testing.T) {
	db := setupTradeDB(t)
	esc := newMockEscrow()
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000)}
	bus := &mockBus{}
	svc := NewP2PTradeService(db, esc, &mockRates{stale: true}, bus, 146).
		WithClock(fixedClock{t: time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)})
	svc.SetTokenResolver(func(s string) string { return "0x29219dd400f2Bf60E5a23d13Be72B486D4038894" })
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	_, sellerAddr := newSellerKey(t)

	_, err := svc.CreateAd(context.Background(), sellerID, sellerAddr, makeCreateAdParams(pmID))
	if !errors.Is(err, ErrRateStale) {
		t.Fatalf("expected ErrRateStale, got %v", err)
	}
}

func TestCreateAd_RejectsOverflowAmount(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	esc.balance = OnChainBalance{Available: new(big.Int).Lsh(big.NewInt(1), 200)}
	_, sellerAddr := newSellerKey(t)
	p := makeCreateAdParams(pmID)
	p.TokenAmount = new(big.Int).Lsh(big.NewInt(1), 96).String() // 2**96

	_, err := svc.CreateAd(context.Background(), sellerID, sellerAddr, p)
	if !errors.Is(err, ErrAmountOverflow) {
		t.Fatalf("expected ErrAmountOverflow, got %v", err)
	}
}

// ─── SignAd ─────────────────────────────────────────────────────────────────

func TestSignAd_HappyPath(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000)}
	key, sellerAddr := newSellerKey(t)

	res, err := svc.CreateAd(context.Background(), sellerID, sellerAddr, makeCreateAdParams(pmID))
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}

	// Look up the voucher hash the service stored, sign it.
	var hash [32]byte
	copy(hash[:], common.FromHex(res.Ad.VoucherHash))
	sig := signVoucherForTest(t, hash, key)

	ad, err := svc.SignAd(context.Background(), res.Ad.ID, sellerID, sellerAddr, sig)
	if err != nil {
		t.Fatalf("SignAd: %v", err)
	}
	if ad.Status != models.P2PAdStatusActive {
		t.Errorf("status = %q, want active", ad.Status)
	}
	if !strings.EqualFold(ad.VoucherSignature, "0x"+common.Bytes2Hex(sig)) {
		t.Errorf("signature not stored")
	}
}

func TestSignAd_RejectsWrongSigner(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000)}
	_, sellerAddr := newSellerKey(t)
	otherKey, _ := newSellerKey(t)

	res, _ := svc.CreateAd(context.Background(), sellerID, sellerAddr, makeCreateAdParams(pmID))
	var hash [32]byte
	copy(hash[:], common.FromHex(res.Ad.VoucherHash))
	badSig := signVoucherForTest(t, hash, otherKey)

	_, err := svc.SignAd(context.Background(), res.Ad.ID, sellerID, sellerAddr, badSig)
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
}

func TestSignAd_RejectsBadSignatureLength(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000)}
	_, sellerAddr := newSellerKey(t)

	res, _ := svc.CreateAd(context.Background(), sellerID, sellerAddr, makeCreateAdParams(pmID))
	_, err := svc.SignAd(context.Background(), res.Ad.ID, sellerID, sellerAddr, []byte{0x01, 0x02})
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
}

// ─── CancelAd ───────────────────────────────────────────────────────────────

func TestCancelAd_PreparesUnsignedTx(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000)}
	key, sellerAddr := newSellerKey(t)

	res, _ := svc.CreateAd(context.Background(), sellerID, sellerAddr, makeCreateAdParams(pmID))
	var hash [32]byte
	copy(hash[:], common.FromHex(res.Ad.VoucherHash))
	sig := signVoucherForTest(t, hash, key)
	if _, err := svc.SignAd(context.Background(), res.Ad.ID, sellerID, sellerAddr, sig); err != nil {
		t.Fatalf("SignAd: %v", err)
	}

	raw, err := svc.CancelAd(context.Background(), res.Ad.ID, sellerID, sellerAddr)
	if err != nil {
		t.Fatalf("CancelAd: %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("expected unsigned tx, got empty")
	}
}

func TestCancelAd_RejectsForeignSeller(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000)}
	_, sellerAddr := newSellerKey(t)

	res, _ := svc.CreateAd(context.Background(), sellerID, sellerAddr, makeCreateAdParams(pmID))

	otherSeller := uuid.New()
	_, err := svc.CancelAd(context.Background(), res.Ad.ID, otherSeller, sellerAddr)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

// ─── Deposit / Withdraw ────────────────────────────────────────────────────

func TestPrepareDeposit_HappyPath(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	_, sellerAddr := newSellerKey(t)
	raw, err := svc.PrepareDeposit(context.Background(), sellerID, sellerAddr, "USDT", "1000000")
	if err != nil {
		t.Fatalf("PrepareDeposit: %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("empty tx")
	}
	// Audit log should have been written.
	var n int64
	db.Model(&models.P2PAuditLog{}).Where("action = ?", models.P2PAuditDepositPrepared).Count(&n)
	if n != 1 {
		t.Errorf("expected 1 deposit.prepared audit row, got %d", n)
	}
}

func TestPrepareDeposit_RejectsBadAmount(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sellerID := uuid.New()
	_, sellerAddr := newSellerKey(t)
	for _, bad := range []string{"", "0", "-1", "not-a-number"} {
		if _, err := svc.PrepareDeposit(context.Background(), sellerID, sellerAddr, "USDT", bad); err == nil {
			t.Errorf("amount=%q expected reject, got nil", bad)
		}
	}
}

func TestPrepareWithdraw_RejectsExceedingAvailable(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	esc.balance = OnChainBalance{Available: big.NewInt(50), Locked: big.NewInt(0)}
	sellerID := uuid.New()
	_, sellerAddr := newSellerKey(t)
	_, err := svc.PrepareWithdraw(context.Background(), sellerID, sellerAddr, "USDT", "100")
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
}

func TestConfirmDeposit_VerifiesEventAndAudits(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, bus := newService(t, db)
	sellerID := uuid.New()
	_, sellerAddr := newSellerKey(t)
	esc.depositedEvent = &P2PDepositedEvent{
		Seller: common.HexToAddress(sellerAddr),
		Token:  common.HexToAddress("0x29219dd400f2Bf60E5a23d13Be72B486D4038894"),
		Amount: big.NewInt(500),
	}
	ev, err := svc.ConfirmDeposit(context.Background(), sellerID, sellerAddr, "USDT", "0xabc")
	if err != nil {
		t.Fatalf("ConfirmDeposit: %v", err)
	}
	if ev.Amount.Cmp(big.NewInt(500)) != 0 {
		t.Errorf("amount mismatch")
	}
	var n int64
	db.Model(&models.P2PAuditLog{}).Where("action = ?", models.P2PAuditDepositConfirmed).Count(&n)
	if n != 1 {
		t.Errorf("expected 1 deposit.confirmed audit row, got %d", n)
	}
	found := false
	for _, e := range bus.published {
		if e.Type == events.P2PDepositConfirmed {
			found = true
		}
	}
	if !found {
		t.Errorf("expected P2PDepositConfirmed event")
	}
}

func TestGetPoolBalance_ReadsFromChain(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	esc.balance = OnChainBalance{Available: big.NewInt(123), Locked: big.NewInt(45)}
	_, sellerAddr := newSellerKey(t)
	bal, err := svc.GetPoolBalance(context.Background(), sellerAddr, "USDT")
	if err != nil {
		t.Fatalf("GetPoolBalance: %v", err)
	}
	if bal.Available.Cmp(big.NewInt(123)) != 0 || bal.Locked.Cmp(big.NewInt(45)) != 0 {
		t.Errorf("balance mismatch: %+v", bal)
	}
}

// ─── OpenTrade ─────────────────────────────────────────────────────────────

func TestOpenTrade_RequiresSignedAd(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000)}
	_, sellerAddr := newSellerKey(t)

	res, _ := svc.CreateAd(context.Background(), sellerID, sellerAddr, makeCreateAdParams(pmID))
	// Ad is pending (unsigned). OpenTrade should reject — but also need an
	// active status to even reach the voucher path. The service's first
	// guard is ad.Status != Active.
	buyerID := uuid.New()
	buyerPm := seedPaymentMethod(t, db, buyerID, "NGN")
	_, _, err := svc.OpenTrade(context.Background(), res.Ad.ID, buyerID, "0xbeef",
		OpenTradeParams{FiatAmount: "1000", PaymentMethodID: buyerPm})
	if !errors.Is(err, ErrAdNotActive) {
		t.Fatalf("expected ErrAdNotActive, got %v", err)
	}
}

func TestOpenTrade_HappyPath(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000)}
	key, sellerAddr := newSellerKey(t)

	res, _ := svc.CreateAd(context.Background(), sellerID, sellerAddr, makeCreateAdParams(pmID))
	var hash [32]byte
	copy(hash[:], common.FromHex(res.Ad.VoucherHash))
	sig := signVoucherForTest(t, hash, key)
	if _, err := svc.SignAd(context.Background(), res.Ad.ID, sellerID, sellerAddr, sig); err != nil {
		t.Fatalf("SignAd: %v", err)
	}

	// Buyer picks one of the seller's payment methods (which is what the
	// ad's allowlist contains).
	buyerID := uuid.New()
	trade, raw, err := svc.OpenTrade(context.Background(), res.Ad.ID, buyerID, "0xbeef",
		OpenTradeParams{FiatAmount: "1000", PaymentMethodID: pmID})
	if err != nil {
		t.Fatalf("OpenTrade: %v", err)
	}
	if trade.Status != models.P2PTradeStatusPendingPayment {
		t.Errorf("trade status = %q", trade.Status)
	}
	if len(raw) == 0 {
		t.Fatalf("expected unsigned tx")
	}
}

// ─── Pause/Resume (unchanged from V1) ──────────────────────────────────────

func TestPauseResume_OnlySeller(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	esc.balance = OnChainBalance{Available: big.NewInt(1_000_000_000)}
	key, sellerAddr := newSellerKey(t)
	res, _ := svc.CreateAd(context.Background(), sellerID, sellerAddr, makeCreateAdParams(pmID))
	var hash [32]byte
	copy(hash[:], common.FromHex(res.Ad.VoucherHash))
	svc.SignAd(context.Background(), res.Ad.ID, sellerID, sellerAddr, signVoucherForTest(t, hash, key))

	// Foreign caller cannot pause.
	if err := svc.PauseAd(context.Background(), res.Ad.ID, uuid.New()); !errors.Is(err, ErrForbidden) {
		t.Errorf("foreign pause: expected ErrForbidden, got %v", err)
	}
	if err := svc.PauseAd(context.Background(), res.Ad.ID, sellerID); err != nil {
		t.Errorf("seller pause: %v", err)
	}
	if err := svc.ResumeAd(context.Background(), res.Ad.ID, sellerID); err != nil {
		t.Errorf("seller resume: %v", err)
	}
}
