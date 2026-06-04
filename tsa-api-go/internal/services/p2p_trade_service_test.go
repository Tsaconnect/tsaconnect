// Pool-model trade-service tests. Focused on the new voucher / deposit /
// openTrade flow; legacy lock-on-publish tests were removed in the Phase 2B
// migration since the underlying methods no longer exist.

package services

import (
	"bytes"
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

	// Per-method error injection — set to override the success default.
	verifyDepositErr   error
	verifyWithdrawnErr error
	verifyCancelledErr error
	verifyOpenedErr    error
	verifyReleasedErr  error
	verifyTradeCanErr  error
	verifyDisputedErr  error
	verifyResolvedErr  error

	// Event fixtures the verify methods return on success.
	depositedEvent *P2PDepositedEvent
	withdrawnEvent *P2PWithdrawnEvent
	cancelledEvent *P2PVoucherCancelledEvent
	openedEvent    *P2PTradeOpenedEvent
	releasedEvent  *P2PTradeReleasedEvent
	tradeCancelled *P2PTradeCancelledEvent
	disputedEvent  *P2PTradeDisputedEvent
	resolvedEvent  *P2PTradeResolvedEvent
	onChainTrade   *OnChainP2PTrade

	// Captured args for assertion in tests that need to verify the right
	// values were threaded through to the adapter (not just that *something*
	// was prepared).
	lastDepositArgs          depositArgs
	lastWithdrawArgs         depositArgs
	lastOpenTradeArgs        openTradeArgs
	lastCancelVoucherArgs    cancelVoucherArgs
	lastOpenTradeDepositArgs openTradeDepositArgs

	// Buy-ad / JIT verify fixtures + error injection.
	verifyOpenedDepositErr error
	openedDepositEvent     *P2PTradeOpenedWithDepositEvent
}

type depositArgs struct {
	Seller string
	Token  string
	Amount *big.Int
}
type openTradeArgs struct {
	TradeID [32]byte
	Voucher AdVoucher
	Sig     []byte
	Buyer   string
	Amount  *big.Int
}
type cancelVoucherArgs struct {
	Seller  string
	Voucher AdVoucher
}
type openTradeDepositArgs struct {
	TradeID  [32]byte
	Token    string
	Amount   *big.Int
	Merchant string
	From     string
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
	m.lastDepositArgs = depositArgs{Seller: seller, Token: token, Amount: new(big.Int).Set(amount)}
	return []byte(`{"tx":"deposit"}`), nil
}
func (m *mockEscrow) PrepareApprove(seller, token string, amount *big.Int) ([]byte, error) {
	m.preparedTxs = append(m.preparedTxs, "approve")
	return []byte(`{"tx":"approve"}`), nil
}
func (m *mockEscrow) ReadAllowance(seller, token string) (*big.Int, error) {
	// Default: zero allowance. Tests can override via a field if needed.
	return big.NewInt(0), nil
}
func (m *mockEscrow) PrepareWithdraw(seller, token string, amount *big.Int) ([]byte, error) {
	m.preparedTxs = append(m.preparedTxs, "withdraw")
	m.lastWithdrawArgs = depositArgs{Seller: seller, Token: token, Amount: new(big.Int).Set(amount)}
	return []byte(`{"tx":"withdraw"}`), nil
}
func (m *mockEscrow) PrepareCancelVoucher(seller string, v AdVoucher) ([]byte, error) {
	m.preparedTxs = append(m.preparedTxs, "cancelVoucher")
	m.lastCancelVoucherArgs = cancelVoucherArgs{Seller: seller, Voucher: v}
	return []byte(`{"tx":"cancelVoucher"}`), nil
}
func (m *mockEscrow) PrepareOpenTradeWithVoucher(tradeId [32]byte, v AdVoucher, sig []byte, buyer string, amount *big.Int) ([]byte, error) {
	m.preparedTxs = append(m.preparedTxs, "openTrade")
	sigCopy := make([]byte, len(sig))
	copy(sigCopy, sig)
	m.lastOpenTradeArgs = openTradeArgs{
		TradeID: tradeId, Voucher: v, Sig: sigCopy,
		Buyer: buyer, Amount: new(big.Int).Set(amount),
	}
	return []byte(`{"tx":"openTrade"}`), nil
}
func (m *mockEscrow) PrepareOpenTradeWithDeposit(tradeId [32]byte, token string, amount *big.Int, merchant, from string) ([]byte, error) {
	m.preparedTxs = append(m.preparedTxs, "openTradeWithDeposit")
	m.lastOpenTradeDepositArgs = openTradeDepositArgs{
		TradeID: tradeId, Token: token, Amount: new(big.Int).Set(amount),
		Merchant: merchant, From: from,
	}
	return []byte(`{"tx":"openTradeWithDeposit"}`), nil
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
	if m.verifyDepositErr != nil {
		return nil, m.verifyDepositErr
	}
	if m.depositedEvent != nil {
		return m.depositedEvent, nil
	}
	return &P2PDepositedEvent{Seller: seller, Token: token, Amount: big.NewInt(100)}, nil
}
func (m *mockEscrow) VerifyWithdrawn(txHash string, seller, token common.Address) (*P2PWithdrawnEvent, error) {
	if m.verifyWithdrawnErr != nil {
		return nil, m.verifyWithdrawnErr
	}
	if m.withdrawnEvent != nil {
		return m.withdrawnEvent, nil
	}
	return &P2PWithdrawnEvent{Seller: seller, Token: token, Amount: big.NewInt(50)}, nil
}
func (m *mockEscrow) VerifyVoucherCancelled(txHash string, hash [32]byte) (*P2PVoucherCancelledEvent, error) {
	if m.verifyCancelledErr != nil {
		return nil, m.verifyCancelledErr
	}
	if m.cancelledEvent != nil {
		return m.cancelledEvent, nil
	}
	return &P2PVoucherCancelledEvent{VoucherHash: hash}, nil
}
func (m *mockEscrow) VerifyTradeOpened(txHash string, tradeId [32]byte) (*P2PTradeOpenedEvent, error) {
	if m.verifyOpenedErr != nil {
		return nil, m.verifyOpenedErr
	}
	if m.openedEvent != nil {
		return m.openedEvent, nil
	}
	return &P2PTradeOpenedEvent{TradeID: tradeId, Buyer: common.HexToAddress("0xb000"), Amount: big.NewInt(100)}, nil
}
func (m *mockEscrow) VerifyTradeOpenedWithDeposit(txHash string, tradeId [32]byte) (*P2PTradeOpenedWithDepositEvent, error) {
	if m.verifyOpenedDepositErr != nil {
		return nil, m.verifyOpenedDepositErr
	}
	if m.openedDepositEvent != nil {
		return m.openedDepositEvent, nil
	}
	return &P2PTradeOpenedWithDepositEvent{TradeID: tradeId, Amount: big.NewInt(100)}, nil
}
func (m *mockEscrow) VerifyTradeReleased(txHash string, tradeId [32]byte) (*P2PTradeReleasedEvent, error) {
	if m.verifyReleasedErr != nil {
		return nil, m.verifyReleasedErr
	}
	if m.releasedEvent != nil {
		return m.releasedEvent, nil
	}
	return &P2PTradeReleasedEvent{TradeID: tradeId, AmountToBuyer: big.NewInt(99), FeeToSystem: big.NewInt(1)}, nil
}
func (m *mockEscrow) VerifyTradeCancelled(txHash string, tradeId [32]byte) (*P2PTradeCancelledEvent, error) {
	if m.verifyTradeCanErr != nil {
		return nil, m.verifyTradeCanErr
	}
	if m.tradeCancelled != nil {
		return m.tradeCancelled, nil
	}
	return &P2PTradeCancelledEvent{TradeID: tradeId, Amount: big.NewInt(100), ReturnedToPool: true}, nil
}
func (m *mockEscrow) VerifyTradeDisputed(txHash string, tradeId [32]byte) (*P2PTradeDisputedEvent, error) {
	if m.verifyDisputedErr != nil {
		return nil, m.verifyDisputedErr
	}
	if m.disputedEvent != nil {
		return m.disputedEvent, nil
	}
	return &P2PTradeDisputedEvent{TradeID: tradeId, By: common.HexToAddress("0xb000")}, nil
}
func (m *mockEscrow) VerifyTradeResolved(txHash string, tradeId [32]byte) (*P2PTradeResolvedEvent, error) {
	if m.verifyResolvedErr != nil {
		return nil, m.verifyResolvedErr
	}
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
			type TEXT, provider TEXT DEFAULT 'bank_transfer',
			country TEXT, currency TEXT,
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
			buyer_id TEXT NOT NULL, seller_id TEXT NOT NULL, opener_id TEXT,
			token TEXT, chain_id INTEGER,
			funding INTEGER NOT NULL DEFAULT 0,
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
			reason TEXT NOT NULL, evidence_urls TEXT,
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
	svc := NewP2PTradeService(db, NewP2PEscrowRegistryFromMap(map[int64]P2PEscrowAdapter{146: esc}, "1"), &mockRates{}, bus, 146)
	svc = svc.WithClock(fixedClock{t: time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)})
	svc.SetTokenResolver(func(chainID int64, symbol string) string {
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
	svc := NewP2PTradeService(db, NewP2PEscrowRegistryFromMap(map[int64]P2PEscrowAdapter{146: esc}, "1"), &mockRates{stale: true}, bus, 146).
		WithClock(fixedClock{t: time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)})
	svc.SetTokenResolver(func(chainID int64, s string) string { return "0x29219dd400f2Bf60E5a23d13Be72B486D4038894" })
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

	raw, err := svc.PrepareCancelAd(context.Background(), res.Ad.ID, sellerID, sellerAddr)
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
	_, err := svc.PrepareCancelAd(context.Background(), res.Ad.ID, otherSeller, sellerAddr)
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
	ev, err := svc.ConfirmDeposit(context.Background(), sellerID, sellerAddr, "USDT", "500", "0xabc")
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

// ─── Money-invariant + idempotency tests ───────────────────────────────────

// helper: full happy-path setup → returns a service, esc, sellerID, sellerAddr,
// adID for a SIGNED ad with a 100-USDT cap, ready to OpenTrade against.
func signedAdFixture(t *testing.T) (*P2PTradeService, *mockEscrow, uuid.UUID, string, uuid.UUID, *gorm.DB) {
	t.Helper()
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
	var hash [32]byte
	copy(hash[:], common.FromHex(res.Ad.VoucherHash))
	sig := signVoucherForTest(t, hash, key)
	if _, err := svc.SignAd(context.Background(), res.Ad.ID, sellerID, sellerAddr, sig); err != nil {
		t.Fatalf("SignAd: %v", err)
	}
	return svc, esc, sellerID, sellerAddr, res.Ad.ID, db
}

func TestConfirmDeposit_RejectsAmountMismatch(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	_, sellerAddr := newSellerKey(t)
	esc.depositedEvent = &P2PDepositedEvent{
		Seller: common.HexToAddress(sellerAddr),
		Token:  common.HexToAddress("0x29219dd400f2Bf60E5a23d13Be72B486D4038894"),
		Amount: big.NewInt(500),
	}
	// User intended 1000 but the on-chain event says 500 — the receipt
	// belongs to a different tx (or was tampered with). Must reject.
	_, err := svc.ConfirmDeposit(context.Background(), sellerID, sellerAddr, "USDT", "1000", "0xabc")
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
}

func TestConfirmWithdraw_RejectsAmountMismatch(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sellerID := uuid.New()
	_, sellerAddr := newSellerKey(t)
	esc.withdrawnEvent = &P2PWithdrawnEvent{
		Seller: common.HexToAddress(sellerAddr),
		Token:  common.HexToAddress("0x29219dd400f2Bf60E5a23d13Be72B486D4038894"),
		Amount: big.NewInt(50),
	}
	_, err := svc.ConfirmWithdraw(context.Background(), sellerID, sellerAddr, "USDT", "100", "0xabc")
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
}

func TestSignAd_IdempotentReplay(t *testing.T) {
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
		t.Fatalf("first SignAd: %v", err)
	}
	// Same signature replayed — should return OK (idempotent), not error.
	ad, err := svc.SignAd(context.Background(), res.Ad.ID, sellerID, sellerAddr, sig)
	if err != nil {
		t.Fatalf("idempotent SignAd: %v", err)
	}
	if ad.Status != models.P2PAdStatusActive {
		t.Errorf("status flipped on replay: %s", ad.Status)
	}
}

func TestSignAd_DifferentSigOnActiveAd_Rejected(t *testing.T) {
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
	// Construct a second valid signature with the same key (varies because
	// ECDSA nonce is random). Should be rejected since the ad is already
	// active with a different stored sig.
	sig2 := signVoucherForTest(t, hash, key)
	if bytes.Equal(sig, sig2) {
		t.Skip("deterministic ECDSA: cannot exercise differing-sig replay")
	}
	_, err := svc.SignAd(context.Background(), res.Ad.ID, sellerID, sellerAddr, sig2)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestOpenTrade_RejectsExpiredVoucher(t *testing.T) {
	svc, _, _, _, adID, db := signedAdFixture(t)
	// Move clock past the 30-day voucher TTL.
	svc = svc.WithClock(fixedClock{t: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)})
	// Re-seed an active rate snapshot via the same mock for OpenTrade.
	buyerID := uuid.New()
	// Look up the ad's stored seller PM from the ad's payment_method_ids
	// (CreateAd populated it from makeCreateAdParams).
	var ad models.P2PAd
	db.First(&ad, "id = ?", adID)
	var pmID uuid.UUID
	if len(ad.PaymentMethodIDs) > 0 {
		pmID, _ = uuid.Parse(string(ad.PaymentMethodIDs[0]))
	}
	_, _, err := svc.OpenTrade(context.Background(), adID, buyerID, "0xb000",
		OpenTradeParams{FiatAmount: "1000", PaymentMethodID: pmID})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition for expired voucher, got %v", err)
	}
}

func TestOpenTrade_RejectsSelfTrade(t *testing.T) {
	svc, _, sellerID, _, adID, db := signedAdFixture(t)
	var ad models.P2PAd
	db.First(&ad, "id = ?", adID)
	var pmID uuid.UUID
	if len(ad.PaymentMethodIDs) > 0 {
		pmID, _ = uuid.Parse(string(ad.PaymentMethodIDs[0]))
	}
	_, _, err := svc.OpenTrade(context.Background(), adID, sellerID, "0xseller",
		OpenTradeParams{FiatAmount: "1000", PaymentMethodID: pmID})
	if !errors.Is(err, ErrSelfTrade) {
		t.Fatalf("expected ErrSelfTrade, got %v", err)
	}
}

func TestCancelAd_BlockedByOpenTrades(t *testing.T) {
	svc, _, sellerID, sellerAddr, adID, db := signedAdFixture(t)
	// Inject an open trade against the ad to simulate the busy state.
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	trade := &models.P2PTrade{
		ID: uuid.New(), AdID: adID, BuyerID: uuid.New(), SellerID: sellerID,
		Token: "USDT", ChainID: 146,
		TokenAmount: "1000000", FiatAmount: "1300", FiatCurrency: "NGN", Rate: "1300",
		PaymentMethodID: uuid.New(),
		Status:          models.P2PTradeStatusPendingPayment,
		ExpiresAt:       now.Add(30 * time.Minute),
	}
	if err := db.Create(trade).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
	_, err := svc.PrepareCancelAd(context.Background(), adID, sellerID, sellerAddr)
	if !errors.Is(err, ErrAdHasOpenTrades) {
		t.Fatalf("expected ErrAdHasOpenTrades, got %v", err)
	}
}

func TestConfirmCancelAd_RejectsCorruptVoucherHash(t *testing.T) {
	svc, _, sellerID, _, adID, db := signedAdFixture(t)
	// Corrupt the stored voucher hash (truncate to 30 bytes worth of hex).
	db.Model(&models.P2PAd{}).Where("id = ?", adID).Update("voucher_hash", "0x1234")
	_, err := svc.ConfirmCancelAd(context.Background(), adID, sellerID, "0xtx")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition (corrupt hex), got %v", err)
	}
}

func TestOpenTrade_RejectsCorruptStoredSignature(t *testing.T) {
	svc, _, _, _, adID, db := signedAdFixture(t)
	db.Model(&models.P2PAd{}).Where("id = ?", adID).Update("voucher_signature", "0xdead")
	var ad models.P2PAd
	db.First(&ad, "id = ?", adID)
	var pmID uuid.UUID
	if len(ad.PaymentMethodIDs) > 0 {
		pmID, _ = uuid.Parse(string(ad.PaymentMethodIDs[0]))
	}
	buyerID := uuid.New()
	_, _, err := svc.OpenTrade(context.Background(), adID, buyerID, "0xb000",
		OpenTradeParams{FiatAmount: "1000", PaymentMethodID: pmID})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition (corrupt sig), got %v", err)
	}
}

func TestRecoverEIP712Signer_RejectsHighS(t *testing.T) {
	key, addr := newSellerKey(t)
	var hash [32]byte
	copy(hash[:], common.FromHex("0x"+strings.Repeat("ab", 32)))
	sig := signVoucherForTest(t, hash, key)

	// Sanity: canonical sig recovers correctly.
	got, err := recoverEIP712Signer(hash, sig)
	if err != nil {
		t.Fatalf("canonical recovery failed: %v", err)
	}
	if got.Hex() != addr {
		t.Fatalf("addr mismatch: got %s want %s", got.Hex(), addr)
	}

	// Flip the signature to its high-S complement and the recovery must reject.
	// secp256k1 N
	n, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141", 16)
	sLow := new(big.Int).SetBytes(sig[32:64])
	sHigh := new(big.Int).Sub(n, sLow)
	bad := make([]byte, 65)
	copy(bad, sig[:32])
	sHighBytes := sHigh.Bytes()
	copy(bad[64-len(sHighBytes):64], sHighBytes)
	v := sig[64]
	if v == 27 {
		bad[64] = 28
	} else {
		bad[64] = 27
	}
	_, err = recoverEIP712Signer(hash, bad)
	if err == nil || !strings.Contains(err.Error(), "non-canonical") {
		t.Fatalf("expected high-S rejection, got %v", err)
	}
}

func TestRecoverEIP712Signer_VNormalisation(t *testing.T) {
	key, addr := newSellerKey(t)
	var hash [32]byte
	copy(hash[:], common.FromHex("0x"+strings.Repeat("cd", 32)))
	sig := signVoucherForTest(t, hash, key)
	// Sig was bumped to 27/28 by signVoucherForTest. Flip back to 0/1 to
	// confirm normalisation still recovers the same address.
	sig[64] -= 27
	got, err := recoverEIP712Signer(hash, sig)
	if err != nil {
		t.Fatalf("v=0/1 normalisation: %v", err)
	}
	if got.Hex() != addr {
		t.Errorf("addr mismatch on normalised v: got %s want %s", got.Hex(), addr)
	}
}

func TestRecoverEIP712Signer_RejectsBadLength(t *testing.T) {
	var hash [32]byte
	if _, err := recoverEIP712Signer(hash, []byte{0x01, 0x02}); err == nil {
		t.Fatal("expected error on 2-byte sig")
	}
}

func TestPrepareCancelVoucher_CapturesCorrectVoucher(t *testing.T) {
	svc, esc, sellerID, sellerAddr, adID, _ := signedAdFixture(t)
	if _, err := svc.PrepareCancelAd(context.Background(), adID, sellerID, sellerAddr); err != nil {
		t.Fatalf("CancelAd: %v", err)
	}
	// The mock should have captured the voucher tuple. Verify the seller
	// matches and the nonce was non-zero.
	if !strings.EqualFold(esc.lastCancelVoucherArgs.Seller, sellerAddr) {
		t.Errorf("seller mismatch in cancelVoucher call: got %s", esc.lastCancelVoucherArgs.Seller)
	}
	if esc.lastCancelVoucherArgs.Voucher.Nonce == ([32]byte{}) {
		t.Error("zero nonce in cancelVoucher voucher")
	}
}

func TestPrepareOpenTrade_PassesVoucherAndSigToAdapter(t *testing.T) {
	svc, esc, _, _, adID, db := signedAdFixture(t)
	var ad models.P2PAd
	db.First(&ad, "id = ?", adID)
	var pmID uuid.UUID
	if len(ad.PaymentMethodIDs) > 0 {
		pmID, _ = uuid.Parse(string(ad.PaymentMethodIDs[0]))
	}
	if _, _, err := svc.OpenTrade(context.Background(), adID, uuid.New(), "0xb000",
		OpenTradeParams{FiatAmount: "1000", PaymentMethodID: pmID}); err != nil {
		t.Fatalf("OpenTrade: %v", err)
	}
	// The adapter must have received: matching voucher hash + 65-byte sig.
	gotHash := defaultHashVoucher(esc.lastOpenTradeArgs.Voucher)
	wantHash, _ := decodeHex32(ad.VoucherHash)
	if gotHash != wantHash {
		t.Errorf("voucher hash mismatch: passed-through voucher digests differently than the stored ad")
	}
	if len(esc.lastOpenTradeArgs.Sig) != 65 {
		t.Errorf("expected 65-byte sig forwarded, got %d", len(esc.lastOpenTradeArgs.Sig))
	}
}

// ─── LoadProvidersByMethodIDs ──────────────────────────────────────────────

// seedPaymentMethodWithProvider seeds a payment method with an explicit
// provider slug so LoadProvidersByMethodIDs has distinct rows to resolve.
func seedPaymentMethodWithProvider(t *testing.T, db *gorm.DB, userID uuid.UUID, provider string) uuid.UUID {
	t.Helper()
	pm := &models.P2PPaymentMethod{
		ID:            uuid.New(),
		UserID:        userID,
		Type:          models.P2PPaymentTypeBank,
		Provider:      provider,
		Country:       "NG",
		Currency:      "NGN",
		BankName:      "Test Bank",
		AccountNumber: "1234567890",
		AccountName:   "Test User",
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed pm (%s): %v", provider, err)
	}
	return pm.ID
}

func TestLoadProvidersByMethodIDs_ResolvesLiveAndExcludesDeleted(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	userID := uuid.New()

	bankID := seedPaymentMethodWithProvider(t, db, userID, "bank_transfer")
	opayID := seedPaymentMethodWithProvider(t, db, userID, "opay")
	deletedID := seedPaymentMethodWithProvider(t, db, userID, "palmpay")

	// Soft-delete the third method directly so the loader must exclude it.
	if err := db.Model(&models.P2PPaymentMethod{}).
		Where("id = ?", deletedID).
		Update("deleted_at", time.Now().UTC()).Error; err != nil {
		t.Fatalf("soft-delete pm: %v", err)
	}

	got, err := svc.LoadProvidersByMethodIDs(context.Background(),
		[]uuid.UUID{bankID, opayID, deletedID})
	if err != nil {
		t.Fatalf("LoadProvidersByMethodIDs: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 live providers, got %d: %+v", len(got), got)
	}
	if got[bankID] != "bank_transfer" {
		t.Errorf("bank method = %q, want bank_transfer", got[bankID])
	}
	if got[opayID] != "opay" {
		t.Errorf("opay method = %q, want opay", got[opayID])
	}
	if _, ok := got[deletedID]; ok {
		t.Errorf("soft-deleted method %s should be excluded, got %q", deletedID, got[deletedID])
	}
}

// ─── Buy-ad (two-sided / JIT) ──────────────────────────────────────────────

// makeBuyAdParams returns CreateAdParams for a 100 USDT buy-ad in NGN.
func makeBuyAdParams(pmID uuid.UUID) CreateAdParams {
	p := makeCreateAdParams(pmID)
	p.Side = "buy"
	return p
}

func TestCreateBuyAd_ActiveImmediately_NoVoucher_NoPoolCheck(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	merchantID := uuid.New()
	pmID := seedPaymentMethod(t, db, merchantID, "NGN")
	// Crucially: merchant has ZERO deposited balance — a buy-ad must not
	// require one (no pool precheck).
	esc.balance = OnChainBalance{Available: big.NewInt(0), Locked: big.NewInt(0)}
	_, merchantAddr := newSellerKey(t)

	res, err := svc.CreateAd(context.Background(), merchantID, merchantAddr, makeBuyAdParams(pmID))
	if err != nil {
		t.Fatalf("CreateAd(buy): %v", err)
	}
	if res.Ad.Side != models.P2PAdSideBuy {
		t.Errorf("side = %q, want buy", res.Ad.Side)
	}
	if res.Ad.Status != models.P2PAdStatusActive {
		t.Errorf("status = %q, want active (buy-ads skip signing)", res.Ad.Status)
	}
	if res.TypedData != nil {
		t.Errorf("buy-ad should have no typed data, got %+v", res.TypedData)
	}
	if res.Ad.VoucherHash != "" || res.Ad.VoucherSignature != "" {
		t.Errorf("buy-ad should have no voucher fields: %+v", res.Ad)
	}
	if res.Ad.Remaining != res.Ad.Amount || res.Ad.Reserved != "0" {
		t.Errorf("buy-ad capacity init wrong: remaining=%s reserved=%s amount=%s",
			res.Ad.Remaining, res.Ad.Reserved, res.Ad.Amount)
	}
	if !strings.EqualFold(res.Ad.SellerAddress, merchantAddr) {
		t.Errorf("buy-ad SellerAddress must store merchant wallet: got %s want %s",
			res.Ad.SellerAddress, merchantAddr)
	}
}

// buyAdFixture creates an active buy-ad and returns the pieces a JIT-open test
// needs.
func buyAdFixture(t *testing.T) (svc *P2PTradeService, esc *mockEscrow, merchantID uuid.UUID, merchantAddr string, adID, pmID uuid.UUID, db *gorm.DB) {
	t.Helper()
	db = setupTradeDB(t)
	svc, esc, _ = newService(t, db)
	merchantID = uuid.New()
	pmID = seedPaymentMethod(t, db, merchantID, "NGN")
	esc.balance = OnChainBalance{Available: big.NewInt(0)}
	_, merchantAddr = newSellerKey(t)
	res, err := svc.CreateAd(context.Background(), merchantID, merchantAddr, makeBuyAdParams(pmID))
	if err != nil {
		t.Fatalf("createBuyAd: %v", err)
	}
	return svc, esc, merchantID, merchantAddr, res.Ad.ID, pmID, db
}

func TestOpenBuyAdTrade_RolesFundingAndCapacity(t *testing.T) {
	svc, esc, merchantID, merchantAddr, adID, _, db := buyAdFixture(t)
	userID := uuid.New() // the user selling crypto
	userAddr := "0x000000000000000000000000000000000000beef"
	userPm := seedPaymentMethod(t, db, userID, "NGN")

	trade, raw, err := svc.OpenTrade(context.Background(), adID, userID, userAddr,
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: userPm})
	if err != nil {
		t.Fatalf("OpenTrade(buy): %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("expected unsigned tx")
	}
	// Roles invert on a buy-ad: user is the SELLER (depositor), merchant is the
	// BUYER (recipient).
	if trade.SellerID != userID {
		t.Errorf("SellerID = %s, want opener/user %s", trade.SellerID, userID)
	}
	if trade.BuyerID != merchantID {
		t.Errorf("BuyerID = %s, want merchant %s", trade.BuyerID, merchantID)
	}
	if trade.Funding != models.P2PTradeFundingDepositedByTaker {
		t.Errorf("funding = %d, want DepositedByTaker(1)", trade.Funding)
	}
	if trade.Status != models.P2PTradeStatusPendingPayment {
		t.Errorf("status = %q", trade.Status)
	}
	// Adapter must have been called with the JIT deposit builder: from=user,
	// merchant=merchant wallet.
	if !strings.EqualFold(esc.lastOpenTradeDepositArgs.From, userAddr) {
		t.Errorf("deposit from = %s, want user %s", esc.lastOpenTradeDepositArgs.From, userAddr)
	}
	if !strings.EqualFold(esc.lastOpenTradeDepositArgs.Merchant, merchantAddr) {
		t.Errorf("deposit merchant = %s, want %s", esc.lastOpenTradeDepositArgs.Merchant, merchantAddr)
	}
	if esc.lastOpenTradeDepositArgs.Amount.String() != trade.TokenAmount {
		t.Errorf("deposit amount = %s, want %s", esc.lastOpenTradeDepositArgs.Amount, trade.TokenAmount)
	}
	// Off-chain capacity decremented.
	var ad models.P2PAd
	db.First(&ad, "id = ?", adID)
	wantRemaining := new(big.Int)
	amt, _ := new(big.Int).SetString("100000000", 10)
	tradeAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	wantRemaining.Sub(amt, tradeAmt)
	if ad.Remaining != wantRemaining.String() {
		t.Errorf("remaining = %s, want %s", ad.Remaining, wantRemaining)
	}
	if ad.Reserved != trade.TokenAmount {
		t.Errorf("reserved = %s, want %s", ad.Reserved, trade.TokenAmount)
	}
}

// On the sell side the user names the exact crypto amount to sell; the escrow
// must escrow EXACTLY that (so the app's prior approve matches the deposit),
// and fiat is derived from it (50 USDT × 1300 = 65000 NGN).
func TestOpenBuyAdTrade_TokenAmountIsAuthoritative(t *testing.T) {
	svc, esc, _, _, adID, _, db := buyAdFixture(t)
	userID := uuid.New()
	userAddr := "0x000000000000000000000000000000000000beef"
	userPm := seedPaymentMethod(t, db, userID, "NGN")

	trade, _, err := svc.OpenTrade(context.Background(), adID, userID, userAddr,
		OpenTradeParams{TokenAmount: "50000000", PaymentMethodID: userPm})
	if err != nil {
		t.Fatalf("OpenTrade(buy, tokenAmount): %v", err)
	}
	if trade.TokenAmount != "50000000" {
		t.Errorf("TokenAmount = %s, want exactly 50000000 (authoritative)", trade.TokenAmount)
	}
	if trade.FiatAmount != "65000" {
		t.Errorf("FiatAmount = %s, want 65000 (50 USDT × 1300)", trade.FiatAmount)
	}
	if esc.lastOpenTradeDepositArgs.Amount.String() != "50000000" {
		t.Errorf("deposit amount = %s, want exactly the approved 50000000", esc.lastOpenTradeDepositArgs.Amount)
	}
}

func TestOpenBuyAdTrade_RejectsMerchantSelfTake(t *testing.T) {
	svc, _, merchantID, _, adID, pmID, _ := buyAdFixture(t)
	// The merchant cannot take their own buy-ad.
	_, _, err := svc.OpenTrade(context.Background(), adID, merchantID, "0x000000000000000000000000000000000000beef",
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: pmID})
	if !errors.Is(err, ErrSelfTrade) {
		t.Fatalf("expected ErrSelfTrade, got %v", err)
	}
}

func TestOpenBuyAdTrade_RejectsExceedingCapacity(t *testing.T) {
	svc, _, _, _, adID, pmID, _ := buyAdFixture(t)
	userID := uuid.New()
	// 100 USDT @ 1300 = 130000 NGN cap; ask for way more fiat than capacity.
	_, _, err := svc.OpenTrade(context.Background(), adID, userID, "0x000000000000000000000000000000000000beef",
		OpenTradeParams{FiatAmount: "1000000", PaymentMethodID: pmID})
	if err == nil {
		t.Fatalf("expected capacity/limit rejection, got nil")
	}
}

func TestConfirmOpenBuyAdTrade_VerifiesDepositEvent(t *testing.T) {
	svc, esc, merchantID, merchantAddr, adID, _, db := buyAdFixture(t)
	userID := uuid.New()
	userAddr := "0x000000000000000000000000000000000000beef"
	userPm := seedPaymentMethod(t, db, userID, "NGN")
	trade, _, err := svc.OpenTrade(context.Background(), adID, userID, userAddr,
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: userPm})
	if err != nil {
		t.Fatalf("OpenTrade: %v", err)
	}
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	esc.openedDepositEvent = &P2PTradeOpenedWithDepositEvent{
		TradeID: HashID(trade.ID),
		Seller:  common.HexToAddress(userAddr),
		Buyer:   common.HexToAddress(merchantAddr),
		Amount:  tokenAmt,
	}
	// The opener (user/seller) confirms — not the merchant.
	got, err := svc.ConfirmOpenTrade(context.Background(), trade.ID, userID, userAddr, "0xopen")
	if err != nil {
		t.Fatalf("ConfirmOpenTrade(buy): %v", err)
	}
	if got.OpenTxHash != "0xopen" {
		t.Errorf("open tx hash not stored")
	}
	// The merchant (buyer) must NOT be able to confirm the open.
	_, err = svc.ConfirmOpenTrade(context.Background(), trade.ID, merchantID, merchantAddr, "0xopen2")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("merchant confirm expected ErrForbidden, got %v", err)
	}
}

func TestBuyAdTrade_MarkPaidByMerchant_ReleaseByUser(t *testing.T) {
	svc, esc, merchantID, _, adID, _, db := buyAdFixture(t)
	userID := uuid.New()
	userAddr := "0x000000000000000000000000000000000000beef"
	userPm := seedPaymentMethod(t, db, userID, "NGN")
	trade, _, err := svc.OpenTrade(context.Background(), adID, userID, userAddr,
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: userPm})
	if err != nil {
		t.Fatalf("OpenTrade: %v", err)
	}
	// Simulate ConfirmOpenTrade recording the on-chain open (sets OpenTxHash);
	// MarkPaid now requires the open to be confirmed on-chain.
	db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).Update("open_tx_hash", "0xopen")
	// MarkPaid is by the BuyerID = merchant (the fiat sender).
	if _, err := svc.MarkPaid(context.Background(), trade.ID, merchantID, "https://proof/x.png"); err != nil {
		t.Fatalf("MarkPaid(merchant): %v", err)
	}
	// Release is by the SellerID = user (crypto provider). Set the released
	// event to split the trade amount.
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	fee := new(big.Int).Div(new(big.Int).Mul(tokenAmt, big.NewInt(50)), big.NewInt(10000))
	toBuyer := new(big.Int).Sub(tokenAmt, fee)
	esc.releasedEvent = &P2PTradeReleasedEvent{TradeID: HashID(trade.ID), AmountToBuyer: toBuyer, FeeToSystem: fee}

	if _, err := svc.PrepareRelease(context.Background(), trade.ID, userID, userAddr); err != nil {
		t.Fatalf("PrepareRelease(user): %v", err)
	}
	got, err := svc.ConfirmRelease(context.Background(), trade.ID, userID, "0xrel")
	if err != nil {
		t.Fatalf("ConfirmRelease(user): %v", err)
	}
	if got.Status != models.P2PTradeStatusReleased {
		t.Errorf("status = %q, want released", got.Status)
	}
	// Released buy-ad: reserved decremented, remaining NOT re-credited.
	var ad models.P2PAd
	db.First(&ad, "id = ?", adID)
	if ad.Reserved != "0" {
		t.Errorf("reserved = %s, want 0 after release", ad.Reserved)
	}
	amt, _ := new(big.Int).SetString("100000000", 10)
	wantRemaining := new(big.Int).Sub(amt, tokenAmt)
	if ad.Remaining != wantRemaining.String() {
		t.Errorf("remaining = %s, want %s (no re-credit on release)", ad.Remaining, wantRemaining)
	}
}

// TestPrepareRelease_NotifiesBothParties locks the both-parties notification on
// release-start: the seller (releasing) AND the buyer ("funds on the way") each
// get a P2PTradeReleasing event. Release-start was previously a seller-only emit.
func TestPrepareRelease_NotifiesBothParties(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, bus := newService(t, db)
	sellerID := uuid.New()
	buyerID := uuid.New()
	_, sellerAddr := newSellerKey(t)
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	trade := &models.P2PTrade{
		ID: uuid.New(), AdID: uuid.New(), SellerID: sellerID, BuyerID: buyerID,
		Token: "USDT", ChainID: 146, Funding: models.P2PTradeFundingPooled,
		TokenAmount: "1000000", FiatAmount: "1300", FiatCurrency: "NGN", Rate: "1300",
		PaymentMethodID: uuid.New(), Status: models.P2PTradeStatusPaid, OpenTxHash: "0xopen",
		ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(trade).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
	if _, err := svc.PrepareRelease(context.Background(), trade.ID, sellerID, sellerAddr); err != nil {
		t.Fatalf("PrepareRelease: %v", err)
	}
	var sellerNotified, buyerNotified bool
	for _, e := range bus.published {
		if e.Type == events.P2PTradeReleasing {
			if e.UserID == sellerID {
				sellerNotified = true
			}
			if e.UserID == buyerID {
				buyerNotified = true
			}
		}
	}
	if !sellerNotified {
		t.Errorf("expected seller to receive P2PTradeReleasing")
	}
	if !buyerNotified {
		t.Errorf("expected buyer to receive P2PTradeReleasing (release-start heads-up)")
	}
}

// TestPrepareRelease_UnconfiguredChain_FailsLoud locks the Option-B safety
// property: a trade whose ChainID isn't in the escrow registry must error out
// of PrepareRelease (never default to another chain, never flip the trade to
// releasing). Guards the resolve-before-mutation ordering.
func TestPrepareRelease_UnconfiguredChain_FailsLoud(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)        // registry has chain 146 only
	sellerID := uuid.New()
	_, sellerAddr := newSellerKey(t)
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	trade := &models.P2PTrade{
		ID: uuid.New(), AdID: uuid.New(), SellerID: sellerID, BuyerID: uuid.New(),
		Token: "USDT", ChainID: 56, // BSC — NOT configured in the test registry
		Funding: models.P2PTradeFundingPooled,
		TokenAmount: "1000000", FiatAmount: "1300", FiatCurrency: "NGN", Rate: "1300",
		PaymentMethodID: uuid.New(), Status: models.P2PTradeStatusPaid, OpenTxHash: "0xopen",
		ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(trade).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
	if _, err := svc.PrepareRelease(context.Background(), trade.ID, sellerID, sellerAddr); err == nil {
		t.Fatalf("PrepareRelease on unconfigured chain 56 must error, got nil")
	}
	// And the trade must NOT have been flipped to releasing (resolve happens first).
	var after models.P2PTrade
	db.First(&after, "id = ?", trade.ID)
	if after.Status != models.P2PTradeStatusPaid {
		t.Errorf("status = %q after failed PrepareRelease, want still 'paid' (no mutation before fail-loud)", after.Status)
	}
}

// TestConfirmRelease_BuyAd_DoesNotCreditRemaining locks the invariant that
// releasing a buy-ad trade permanently consumes capacity: reserved is
// decremented to zero but remaining is NOT re-credited. This guards against
// regressions where a refactoring accidentally credits remaining on release.
func TestConfirmRelease_BuyAd_DoesNotCreditRemaining(t *testing.T) {
	svc, esc, merchantID, merchantAddr, adID, _, db := buyAdFixture(t)
	userID := uuid.New()
	userAddr := "0x000000000000000000000000000000000000beef"
	userPm := seedPaymentMethod(t, db, userID, "NGN")

	// Open a JIT trade — reserved incremented, remaining decremented at open.
	trade, _, err := svc.OpenTrade(context.Background(), adID, userID, userAddr,
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: userPm})
	if err != nil {
		t.Fatalf("OpenTrade: %v", err)
	}
	db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).Update("open_tx_hash", "0xopen")

	// Snapshot ad state immediately after open to derive expected values.
	var adAfterOpen models.P2PAd
	db.First(&adAfterOpen, "id = ?", adID)
	remainingAfterOpen := adAfterOpen.Remaining
	if adAfterOpen.Reserved == "0" {
		t.Fatalf("expected non-zero reserved after open, got 0")
	}

	// Merchant (BuyerID) marks fiat paid.
	if _, err := svc.MarkPaid(context.Background(), trade.ID, merchantID, "https://proof/x.png"); err != nil {
		t.Fatalf("MarkPaid(merchant): %v", err)
	}

	// Set up mock release event — escrow releases to the merchant.
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	fee := new(big.Int).Div(new(big.Int).Mul(tokenAmt, big.NewInt(50)), big.NewInt(10000))
	toBuyer := new(big.Int).Sub(tokenAmt, fee)
	esc.releasedEvent = &P2PTradeReleasedEvent{
		TradeID:       HashID(trade.ID),
		Buyer:         common.HexToAddress(merchantAddr),
		AmountToBuyer: toBuyer,
		FeeToSystem:   fee,
	}

	// PrepareRelease is by the SellerID = user (crypto provider).
	if _, err := svc.PrepareRelease(context.Background(), trade.ID, userID, userAddr); err != nil {
		t.Fatalf("PrepareRelease(user): %v", err)
	}
	got, err := svc.ConfirmRelease(context.Background(), trade.ID, userID, "0xrel-invariant")
	if err != nil {
		t.Fatalf("ConfirmRelease(user): %v", err)
	}
	if got.Status != models.P2PTradeStatusReleased {
		t.Errorf("status = %q, want released", got.Status)
	}

	// Invariant assertions: reserved must reach zero; remaining must be UNCHANGED
	// from after-open snapshot — capacity is consumed permanently, not re-credited.
	var adAfterRelease models.P2PAd
	db.First(&adAfterRelease, "id = ?", adID)
	if adAfterRelease.Reserved != "0" {
		t.Errorf("reserved = %s after release, want 0 (reserved must clear)", adAfterRelease.Reserved)
	}
	if adAfterRelease.Remaining != remainingAfterOpen {
		t.Errorf("remaining = %s after release, want %s (must NOT be re-credited on buy-ad release)",
			adAfterRelease.Remaining, remainingAfterOpen)
	}
}

func TestConfirmCancelBuyAdTrade_RestoresCapacityNoDoubleCredit(t *testing.T) {
	svc, esc, _, _, adID, _, db := buyAdFixture(t)
	userID := uuid.New()
	userAddr := "0x000000000000000000000000000000000000beef"
	userPm := seedPaymentMethod(t, db, userID, "NGN")
	trade, _, err := svc.OpenTrade(context.Background(), adID, userID, userAddr,
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: userPm})
	if err != nil {
		t.Fatalf("OpenTrade: %v", err)
	}
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	// Buy-ad cancel: on-chain crypto returned to the user's wallet, so
	// ReturnedToPool=false.
	esc.tradeCancelled = &P2PTradeCancelledEvent{TradeID: HashID(trade.ID), Amount: tokenAmt, ReturnedToPool: false}

	got, err := svc.ConfirmCancel(context.Background(), trade.ID, userID, "0xcancel")
	if err != nil {
		t.Fatalf("ConfirmCancel(buy): %v", err)
	}
	if got.Status != models.P2PTradeStatusCancelled {
		t.Errorf("status = %q, want cancelled", got.Status)
	}
	// Capacity fully restored (reserved=0, remaining=full amount). No double-credit.
	var ad models.P2PAd
	db.First(&ad, "id = ?", adID)
	if ad.Reserved != "0" {
		t.Errorf("reserved = %s, want 0", ad.Reserved)
	}
	if ad.Remaining != "100000000" {
		t.Errorf("remaining = %s, want 100000000 (capacity restored)", ad.Remaining)
	}
}

func TestConfirmCancelBuyAdTrade_RejectsPoolFlagMismatch(t *testing.T) {
	svc, esc, _, _, adID, _, db := buyAdFixture(t)
	userID := uuid.New()
	userAddr := "0x000000000000000000000000000000000000beef"
	userPm := seedPaymentMethod(t, db, userID, "NGN")
	trade, _, err := svc.OpenTrade(context.Background(), adID, userID, userAddr,
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: userPm})
	if err != nil {
		t.Fatalf("OpenTrade: %v", err)
	}
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	// Wrong flag: a buy-ad cancel claiming ReturnedToPool=true must be rejected.
	esc.tradeCancelled = &P2PTradeCancelledEvent{TradeID: HashID(trade.ID), Amount: tokenAmt, ReturnedToPool: true}
	if _, err := svc.ConfirmCancel(context.Background(), trade.ID, userID, "0xcancel"); err == nil {
		t.Fatalf("expected rejection on pool-flag mismatch, got nil")
	}
}

// TestValidateCreateAd_RejectsNonNumeric locks the numeric-input validation
// (audit #5/#6): rate/token_amount/limits that can't be stored in their numeric
// columns must be a clean 400 (ErrInvalidParams), not a Postgres INSERT-time 500.
func TestValidateCreateAd_RejectsNonNumeric(t *testing.T) {
	svc, _, _, _, _, pmID, _ := buyAdFixture(t)
	base := CreateAdParams{
		Token: "USDT", TokenAmount: "1000000", FiatCurrency: "NGN",
		RateType: "fixed", Rate: "1500", MinLimit: "100", MaxLimit: "5000",
		PaymentMethodIDs: []uuid.UUID{pmID},
	}
	if err := svc.validateCreateAd(base); err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}
	bad := func(mut func(*CreateAdParams)) error { p := base; mut(&p); return svc.validateCreateAd(p) }
	cases := map[string]func(*CreateAdParams){
		"non-numeric rate":     func(p *CreateAdParams) { p.Rate = "abc" },
		"non-positive rate":    func(p *CreateAdParams) { p.Rate = "0" },
		"non-integer tokenAmt": func(p *CreateAdParams) { p.TokenAmount = "12.5" },
		"non-numeric minLimit": func(p *CreateAdParams) { p.MinLimit = "ten" },
		"negative maxLimit":    func(p *CreateAdParams) { p.MaxLimit = "-5" },
		"min greater than max": func(p *CreateAdParams) { p.MinLimit = "5000"; p.MaxLimit = "100" },
	}
	for name, mut := range cases {
		if err := bad(mut); err == nil {
			t.Errorf("%s: expected validation error, got nil", name)
		}
	}
}

// TestMarkPaid_RequiresConfirmedOpen locks audit #1: mark-paid is refused until the
// open is confirmed on-chain (OpenTxHash set), so a never-broadcast trade can't be
// flipped to `paid`.
func TestMarkPaid_RequiresConfirmedOpen(t *testing.T) {
	svc, _, merchantID, _, adID, _, db := buyAdFixture(t)
	userID := uuid.New()
	userPm := seedPaymentMethod(t, db, userID, "NGN")
	trade, _, err := svc.OpenTrade(context.Background(), adID, userID,
		"0x000000000000000000000000000000000000beef",
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: userPm})
	if err != nil {
		t.Fatalf("OpenTrade: %v", err)
	}
	// No ConfirmOpenTrade → OpenTxHash empty → mark-paid must be rejected.
	if _, err := svc.MarkPaid(context.Background(), trade.ID, merchantID, "https://proof/x.png"); err == nil {
		t.Fatalf("MarkPaid on an unconfirmed open should be rejected")
	}
}

// TestOpenTrade_RejectsDuplicateActiveTrade locks audit #2: a second open by the
// same opener on the same ad is rejected (would otherwise double-debit capacity).
func TestOpenTrade_RejectsDuplicateActiveTrade(t *testing.T) {
	svc, _, _, _, adID, _, db := buyAdFixture(t)
	userID := uuid.New()
	userPm := seedPaymentMethod(t, db, userID, "NGN")
	addr := "0x000000000000000000000000000000000000beef"
	if _, _, err := svc.OpenTrade(context.Background(), adID, userID, addr,
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: userPm}); err != nil {
		t.Fatalf("first OpenTrade: %v", err)
	}
	if _, _, err := svc.OpenTrade(context.Background(), adID, userID, addr,
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: userPm}); err == nil {
		t.Fatalf("second concurrent open should be rejected (ErrActiveTradeExists)")
	}
}

// TestPrepareRaiseDispute_StoresEvidence locks the dispute-wizard backend (#25):
// TestRankExpressAds locks the Express ranking (#22): buying crypto → lowest rate
// first; selling crypto → highest rate first.
func TestRankExpressAds(t *testing.T) {
	base := []models.P2PAd{{Rate: "1500"}, {Rate: "1490"}, {Rate: "1510"}}
	buy := append([]models.P2PAd(nil), base...)
	rankExpressAds(buy, "buy")
	if buy[0].Rate != "1490" || buy[2].Rate != "1510" {
		t.Errorf("buy rank wrong: %s,%s,%s", buy[0].Rate, buy[1].Rate, buy[2].Rate)
	}
	sell := append([]models.P2PAd(nil), base...)
	rankExpressAds(sell, "sell")
	if sell[0].Rate != "1510" || sell[2].Rate != "1490" {
		t.Errorf("sell rank wrong: %s,%s,%s", sell[0].Rate, sell[1].Rate, sell[2].Rate)
	}
}

// TestExpressMatch_RanksAndFilters locks the Express matcher (#22): best price first,
// with insufficient-capacity / out-of-limit ads excluded.
func TestExpressMatch_RanksAndFilters(t *testing.T) {
	svc, _, _, _, _, _, db := buyAdFixture(t)
	mk := func(rate, remaining, minL, maxL string) uuid.UUID {
		id := uuid.New()
		if err := db.Create(&models.P2PAd{
			ID: id, SellerID: uuid.New(), Side: models.P2PAdSideSell, Token: "USDT", ChainID: 14601,
			FiatCurrency: "NGN", Rate: rate, RateType: models.P2PAdRateTypeFixed,
			FloatAnchor: models.P2PAdFloatAnchorBybitSell,
			Amount:      remaining, Remaining: remaining, Reserved: "0",
			MinLimit: models.OptionalNumeric(minL), MaxLimit: models.OptionalNumeric(maxL),
			Status: models.P2PAdStatusActive,
		}).Error; err != nil {
			t.Fatalf("create ad: %v", err)
		}
		return id
	}
	cheap := mk("1490", "100000000", "0", "100000")
	mid := mk("1500", "100000000", "0", "100000")
	lowCap := mk("1480", "500000", "0", "100000") // cheapest but only 0.5 USDT → excluded

	matches, err := svc.ExpressMatch(context.Background(), ExpressMatchParams{
		Side: "buy", Token: "USDT", FiatCurrency: "NGN", Amount: "1000000",
	})
	if err != nil {
		t.Fatalf("ExpressMatch: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 eligible (lowCap excluded), got %d", len(matches))
	}
	if matches[0].ID != cheap {
		t.Errorf("best should be the cheapest eligible (1490), got %s", matches[0].Rate)
	}
	if matches[1].ID != mid {
		t.Errorf("second should be 1500, got %s", matches[1].Rate)
	}
	for _, m := range matches {
		if m.ID == lowCap {
			t.Errorf("insufficient-remaining ad should be excluded")
		}
	}
}

// TestPrepareRaiseDispute_StoresEvidence locks the dispute-wizard backend (#25):
// evidence image URLs submitted with a dispute are stored + decoded on load, and
// the count/URL validation rejects bad input.
func TestPrepareRaiseDispute_StoresEvidence(t *testing.T) {
	svc, _, _, _, adID, _, db := buyAdFixture(t)
	userID := uuid.New()
	userPm := seedPaymentMethod(t, db, userID, "NGN")
	trade, _, err := svc.OpenTrade(context.Background(), adID, userID,
		"0x000000000000000000000000000000000000beef",
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: userPm})
	if err != nil {
		t.Fatalf("OpenTrade: %v", err)
	}
	db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).Update("open_tx_hash", "0xopen")

	urls := []string{"https://cdn.test/ev1.jpg", "https://cdn.test/ev2.jpg"}
	if _, err := svc.PrepareRaiseDispute(context.Background(), trade.ID, userID, "0xuser", "no crypto released", urls); err != nil {
		t.Fatalf("PrepareRaiseDispute with evidence: %v", err)
	}
	var d models.P2PDispute
	if err := db.First(&d, "trade_id = ?", trade.ID).Error; err != nil {
		t.Fatalf("load dispute: %v", err)
	}
	if len(d.Evidence) != 2 || d.Evidence[0] != urls[0] {
		t.Errorf("evidence = %v, want %v", d.Evidence, urls)
	}

	tooMany := make([]string, 7)
	for i := range tooMany {
		tooMany[i] = "https://cdn.test/x.jpg"
	}
	if _, err := svc.PrepareRaiseDispute(context.Background(), uuid.New(), uuid.New(), "0xz", "r", tooMany); err == nil {
		t.Errorf("expected rejection for >6 evidence images")
	}
	if _, err := svc.PrepareRaiseDispute(context.Background(), uuid.New(), uuid.New(), "0xz", "r", []string{"not-a-url"}); err == nil {
		t.Errorf("expected rejection for non-URL evidence")
	}
}

func TestConfirmAdminResolveBuyAd_RefundToSeller_RestoresCapacity(t *testing.T) {
	svc, esc, merchantID, _, adID, _, db := buyAdFixture(t)
	userID := uuid.New()
	userAddr := "0x000000000000000000000000000000000000beef"
	userPm := seedPaymentMethod(t, db, userID, "NGN")
	trade, _, err := svc.OpenTrade(context.Background(), adID, userID, userAddr,
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: userPm})
	if err != nil {
		t.Fatalf("OpenTrade: %v", err)
	}
	db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).Update("open_tx_hash", "0xopen")
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	// Drive trade → disputed (merchant raises).
	if _, err := svc.PrepareRaiseDispute(context.Background(), trade.ID, merchantID, "0xmerch", "no crypto", nil); err != nil {
		t.Fatalf("PrepareRaiseDispute: %v", err)
	}
	if _, err := svc.ConfirmRaiseDispute(context.Background(), trade.ID, merchantID, "0xdisp"); err != nil {
		t.Fatalf("ConfirmRaiseDispute: %v", err)
	}
	adminID := uuid.New()
	if _, err := svc.PrepareAdminResolveDispute(context.Background(), trade.ID, adminID, "0xadmin", false, "refund seller"); err != nil {
		t.Fatalf("PrepareAdminResolveDispute: %v", err)
	}
	// refundBuyer=false: crypto returns to the seller (user) on-chain; off-chain
	// capacity restored.
	esc.resolvedEvent = &P2PTradeResolvedEvent{TradeID: HashID(trade.ID), ReleasedToBuyer: false,
		AmountMoved: big.NewInt(0), FeeToSystem: big.NewInt(0)}
	if _, err := svc.ConfirmAdminResolveDispute(context.Background(), trade.ID, adminID, "0xresolve"); err != nil {
		t.Fatalf("ConfirmAdminResolveDispute: %v", err)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", adID)
	if ad.Reserved != "0" {
		t.Errorf("reserved = %s, want 0", ad.Reserved)
	}
	if ad.Remaining != "100000000" {
		t.Errorf("remaining = %s, want 100000000 (capacity restored)", ad.Remaining)
	}
	_ = tokenAmt
}

// ─── Reconciler funding-awareness ──────────────────────────────────────────

// seedBuyAdWithTrade creates an active buy-ad and a pending_payment JIT trade
// against it, decrementing the ad's off-chain capacity as a real open would.
// Returns the trade + ad ids so reconcile tests can assert capacity handling.
func seedBuyAdWithTrade(t *testing.T, db *gorm.DB, status string) (svc *P2PTradeService, esc *mockEscrow, tradeID, adID uuid.UUID) {
	t.Helper()
	svc, esc, _ = newService(t, db)
	merchantID := uuid.New()
	userID := uuid.New()
	adID = uuid.New()
	tradeID = uuid.New()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	// Capacity already partially consumed by the open: amount 100, reserved 30, remaining 70.
	ad := &models.P2PAd{
		ID: adID, SellerID: merchantID, Side: models.P2PAdSideBuy,
		Token: "USDT", ChainID: 146, Amount: "100", Remaining: "70", Reserved: "30",
		FiatCurrency: "NGN", Rate: "1300", RateType: "fixed",
		PaymentMethodIDs: stringifyUUIDs([]uuid.UUID{uuid.New()}),
		Status:           models.P2PAdStatusActive,
		SellerAddress:    "0x000000000000000000000000000000000000d00d",
	}
	if err := db.Create(ad).Error; err != nil {
		t.Fatalf("seed buy-ad: %v", err)
	}
	trade := &models.P2PTrade{
		ID: tradeID, AdID: adID, SellerID: userID, BuyerID: merchantID,
		Token: "USDT", ChainID: 146, Funding: models.P2PTradeFundingDepositedByTaker,
		TokenAmount: "30", FiatAmount: "39000", FiatCurrency: "NGN", Rate: "1300",
		PaymentMethodID: uuid.New(), Status: status,
		ExpiresAt: now.Add(30 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(trade).Error; err != nil {
		t.Fatalf("seed buy-ad trade: %v", err)
	}
	return svc, esc, tradeID, adID
}

// TestReconcile_OrphanOpen_ReclaimsCapacity locks audit #4: a pending_payment trade
// whose on-chain open never landed (OpenTxHash empty, not on chain) and whose window
// has elapsed is reclaimed by the reconciler — cancelled + ad capacity restored.
func TestReconcile_OrphanOpen_ReclaimsCapacity(t *testing.T) {
	svc, esc, _, _, adID, _, db := buyAdFixture(t)
	userID := uuid.New()
	userPm := seedPaymentMethod(t, db, userID, "NGN")
	trade, _, err := svc.OpenTrade(context.Background(), adID, userID,
		"0x000000000000000000000000000000000000beef",
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: userPm})
	if err != nil {
		t.Fatalf("OpenTrade: %v", err)
	}
	var afterOpen models.P2PAd
	db.First(&afterOpen, "id = ?", adID)
	if afterOpen.Reserved == "0" {
		t.Fatalf("expected reserved>0 after open")
	}
	// Make it an expired orphan: never confirmed (OpenTxHash empty) + past expiry.
	// Expire it relative to the fixture's fixed clock (2026-05-23 12:00 UTC).
	db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).Update("expires_at", time.Date(2026, 5, 23, 11, 0, 0, 0, time.UTC))
	// esc.onChainTrade left nil → ReadTrade returns Status=None, Exists=false.
	res, err := svc.ReconcileTrade(context.Background(), trade.ID)
	if err != nil {
		t.Fatalf("ReconcileTrade: %v", err)
	}
	if !res.Changed || res.ToStatus != models.P2PTradeStatusCancelled {
		t.Errorf("expected orphan reclaim → cancelled, got %+v", res)
	}
	var t2 models.P2PTrade
	db.First(&t2, "id = ?", trade.ID)
	if t2.Status != models.P2PTradeStatusCancelled {
		t.Errorf("trade status = %s, want cancelled", t2.Status)
	}
	var adAfter models.P2PAd
	db.First(&adAfter, "id = ?", adID)
	if adAfter.Reserved != "0" {
		t.Errorf("reserved = %s, want 0 (capacity reclaimed)", adAfter.Reserved)
	}
	_ = esc
}

func TestReconcileBuyAdTrade_Cancelled_RestoresCapacity(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, tradeID, adID := seedBuyAdWithTrade(t, db, models.P2PTradeStatusPendingPayment)
	esc.onChainTrade = &OnChainP2PTrade{
		Status: OnChainP2PStatusCancelled, Funding: models.P2PTradeFundingDepositedByTaker,
		Amount: big.NewInt(30), Exists: true,
	}
	out, err := svc.ReconcileTrade(context.Background(), tradeID)
	if err != nil {
		t.Fatalf("ReconcileTrade: %v", err)
	}
	if !out.Changed || out.ToStatus != models.P2PTradeStatusCancelled {
		t.Fatalf("expected cancelled drift, got %+v", out)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", adID)
	// reserved 30->0, remaining 70->100 (capacity restored, no on-chain pool credit).
	if ad.Reserved != "0" || ad.Remaining != "100" {
		t.Errorf("buy-ad cancel reconcile: reserved=%s remaining=%s, want 0/100", ad.Reserved, ad.Remaining)
	}
}

func TestReconcileBuyAdTrade_Released_DecrementsReservedOnly(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, tradeID, adID := seedBuyAdWithTrade(t, db, models.P2PTradeStatusPaid)
	esc.onChainTrade = &OnChainP2PTrade{
		Status: OnChainP2PStatusReleased, Funding: models.P2PTradeFundingDepositedByTaker,
		Amount: big.NewInt(30), Exists: true,
	}
	out, err := svc.ReconcileTrade(context.Background(), tradeID)
	if err != nil {
		t.Fatalf("ReconcileTrade: %v", err)
	}
	if !out.Changed || out.ToStatus != models.P2PTradeStatusReleased {
		t.Fatalf("expected released drift, got %+v", out)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", adID)
	// reserved 30->0; remaining unchanged at 70 (consumed permanently on release).
	if ad.Reserved != "0" || ad.Remaining != "70" {
		t.Errorf("buy-ad release reconcile: reserved=%s remaining=%s, want 0/70", ad.Reserved, ad.Remaining)
	}
}

func TestReconcileBuyAdTrade_Resolved_RefundSeller_RestoresCapacity(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, tradeID, adID := seedBuyAdWithTrade(t, db, models.P2PTradeStatusDisputed)
	// Seed a dispute row with refundedToBuyer=false so reconcile knows the branch.
	refundBuyer := false
	if err := db.Create(&models.P2PDispute{
		ID: uuid.New(), TradeID: tradeID, RaisedBy: uuid.New(),
		RaisedRole: models.P2PDisputeRoleBuyer, Reason: "x",
		Status: models.P2PDisputeStatusOpen, RefundedToBuyer: &refundBuyer,
	}).Error; err != nil {
		t.Fatalf("seed dispute: %v", err)
	}
	esc.onChainTrade = &OnChainP2PTrade{
		Status: OnChainP2PStatusResolved, Funding: models.P2PTradeFundingDepositedByTaker,
		Amount: big.NewInt(30), Exists: true,
	}
	out, err := svc.ReconcileTrade(context.Background(), tradeID)
	if err != nil {
		t.Fatalf("ReconcileTrade: %v", err)
	}
	if !out.Changed || out.ToStatus != models.P2PTradeStatusResolved {
		t.Fatalf("expected resolved drift, got %+v", out)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", adID)
	// refundBuyer=false → reserved 30->0, remaining 70->100 (capacity restored).
	if ad.Reserved != "0" || ad.Remaining != "100" {
		t.Errorf("buy-ad resolve(refund seller) reconcile: reserved=%s remaining=%s, want 0/100", ad.Reserved, ad.Remaining)
	}
}

func TestReconcileBuyAdTrade_Resolved_RefundBuyer_NoRemainingCredit(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, tradeID, adID := seedBuyAdWithTrade(t, db, models.P2PTradeStatusDisputed)
	refundBuyer := true // crypto went to the merchant (buyer)
	if err := db.Create(&models.P2PDispute{
		ID: uuid.New(), TradeID: tradeID, RaisedBy: uuid.New(),
		RaisedRole: models.P2PDisputeRoleSeller, Reason: "x",
		Status: models.P2PDisputeStatusOpen, RefundedToBuyer: &refundBuyer,
	}).Error; err != nil {
		t.Fatalf("seed dispute: %v", err)
	}
	esc.onChainTrade = &OnChainP2PTrade{
		Status: OnChainP2PStatusResolved, Funding: models.P2PTradeFundingDepositedByTaker,
		Amount: big.NewInt(30), Exists: true,
	}
	if _, err := svc.ReconcileTrade(context.Background(), tradeID); err != nil {
		t.Fatalf("ReconcileTrade: %v", err)
	}
	var ad models.P2PAd
	db.First(&ad, "id = ?", adID)
	// refundBuyer=true → reserved 30->0; remaining stays 70 (no re-credit).
	if ad.Reserved != "0" || ad.Remaining != "70" {
		t.Errorf("buy-ad resolve(refund buyer) reconcile: reserved=%s remaining=%s, want 0/70", ad.Reserved, ad.Remaining)
	}
}

func TestCreateAd_RPCFailureNoLongerSilent(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	esc.balanceErr = errors.New("rpc dial failed")
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")
	_, sellerAddr := newSellerKey(t)
	_, err := svc.CreateAd(context.Background(), sellerID, sellerAddr, makeCreateAdParams(pmID))
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams when ReadBalance errors, got %v", err)
	}
}

func TestTokenDecimalsFor_PerChain(t *testing.T) {
	cases := []struct {
		chainID int64
		symbol  string
		want    int
	}{
		{146, "USDT", 6}, {146, "USDC", 6}, {146, "MCGP", 18}, // Sonic mainnet
		{14601, "USDT", 6},                                     // Sonic testnet
		{56, "USDT", 18}, {56, "USDC", 18},                     // BSC mainnet (Binance-Peg = 18)
		{97, "USDT", 18}, {97, "USDC", 18},                     // BSC testnet
		{146, "WTF", 18},                                       // unknown symbol → 18
	}
	for _, c := range cases {
		if got := tokenDecimalsFor(c.chainID, c.symbol); got != c.want {
			t.Errorf("tokenDecimalsFor(%d,%q) = %d, want %d", c.chainID, c.symbol, got, c.want)
		}
	}
}

// TestReconcileTrade_UnconfiguredChain_FailsLoud locks §8.5: a non-terminal
// trade whose ChainID isn't in the escrow registry must error out of
// reconciliation (never read the wrong chain's escrow and orphan-reclaim it).
func TestReconcileTrade_UnconfiguredChain_FailsLoud(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db) // registry has chain 146 only
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	trade := &models.P2PTrade{
		ID: uuid.New(), AdID: uuid.New(), SellerID: uuid.New(), BuyerID: uuid.New(),
		Token: "USDT", ChainID: 56, // BSC — not configured in the test registry
		Funding: models.P2PTradeFundingPooled,
		TokenAmount: "1000000", FiatAmount: "1300", FiatCurrency: "NGN", Rate: "1300",
		PaymentMethodID: uuid.New(), Status: models.P2PTradeStatusPendingPayment,
		ExpiresAt: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now,
	}
	if err := db.Create(trade).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
	if _, err := svc.ReconcileTrade(context.Background(), trade.ID); err == nil {
		t.Fatalf("ReconcileTrade on unconfigured chain 56 must error, got nil")
	}
	// The trade must NOT have been orphan-reclaimed/cancelled by mis-reading Sonic.
	var after models.P2PTrade
	db.First(&after, "id = ?", trade.ID)
	if after.Status != models.P2PTradeStatusPendingPayment {
		t.Errorf("status = %q after failed reconcile, want unchanged 'pending_payment'", after.Status)
	}
}

// ─── CreateAd chain/asset validation ──────────────────────────────────────────

// TestCreateAd_RejectsUnconfiguredChain: an ad on chain 56 must be rejected
// when the registry (chain 146 only) has no escrow for chain 56.
func TestCreateAd_RejectsUnconfiguredChain(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db) // registry: chain 146 only
	sellerID := uuid.New()
	pmID := seedPaymentMethod(t, db, sellerID, "NGN")

	params := makeCreateAdParams(pmID)
	params.ChainID = 56 // BSC — not in the allow-all FromMap registry

	_, err := svc.CreateAd(context.Background(), sellerID, "0x1234567890123456789012345678901234567890", params)
	if err == nil {
		t.Fatal("CreateAd with unconfigured chain 56 must return an error, got nil")
	}
	if !errors.Is(err, ErrInvalidParams) {
		t.Errorf("error = %v, want ErrInvalidParams", err)
	}
}

// TestCreateAd_RejectsCuratedAssetMismatch: verify AssetAllowed rejects
// MCGP on BSC via the registry directly (curated registry wiring into newService
// is heavy; the registry-level test in p2p_escrow_registry_test.go is the
// authoritative curation test per Plan 5 guidance).
func TestCreateAd_RejectsCuratedAssetMismatch(t *testing.T) {
	// Build a registry directly with BSC (chain 56) allowing only USDT/USDC.
	reg := &P2PEscrowRegistry{byChain: map[int64]*chainEscrow{
		56: {service: NewP2PEscrowServiceForChain(nil, nil, "0xBSC"), eip712Version: "2", domainChainID: 56, assets: []string{"USDT", "USDC"}},
	}}
	// Confirm MCGP is denied on BSC and USDT is allowed.
	if reg.AssetAllowed(56, "MCGP") {
		t.Error("MCGP must NOT be allowed on BSC")
	}
	if !reg.AssetAllowed(56, "USDT") {
		t.Error("USDT must be allowed on BSC")
	}
}
