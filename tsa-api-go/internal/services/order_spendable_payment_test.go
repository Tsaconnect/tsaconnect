package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// orderMcgpPaymentsDDL mirrors models.OrderMcgpPayment for SQLite tests.
const orderMcgpPaymentsDDL = `CREATE TABLE IF NOT EXISTS order_mcgp_payments (
	id TEXT PRIMARY KEY, order_id TEXT UNIQUE, buyer_id TEXT, buyer_wallet TEXT,
	mcgp_amount TEXT, from_bought TEXT, debit_ref TEXT UNIQUE, status TEXT,
	debit_tx_hash TEXT, resolve_tx_hash TEXT, release_reason TEXT,
	seller_wallet TEXT, seller_amount TEXT, seller_credit_tx_hash TEXT,
	buyer_amount TEXT, collect_tx_hash TEXT, collect_settle_tx_hash TEXT,
	decided_at DATETIME, debited_at DATETIME, failure_reason TEXT,
	created_at DATETIME, updated_at DATETIME)`

// ── contract double ─────────────────────────────────────────────────────
//
// fakeEntitlement models the parts of McgpEntitlementSwapV2 the order path
// touches — buckets, pendingSettle, accountedMcgp, the token balance it
// holds, debit records and used credit refs — and enforces the same
// requires, so a test can assert the contract's own invariants (backing,
// no double spend) rather than just call counts.
type fakeEntitlement struct {
	held     *big.Int // mcgpToken.balanceOf(contract)
	acct     *big.Int // accountedMcgp
	bought   map[common.Address]*big.Int
	credited map[common.Address]*big.Int
	pending  map[common.Address]*big.Int

	debits     map[[32]byte]DebitRecord
	fromBought map[[32]byte]*big.Int
	creditRefs map[[32]byte]bool

	limits      SpendableLimits
	limitsErr   error // SpendableLimits fails (an unreadable RPC)
	limitsReads int

	// failure injection
	debitSendErr  error // returned instead of executing (nothing happens)
	debitLostResp bool  // executes on "chain" but the response is lost
	verifyErr     error // every VerifyOperatorEvent fails
	creditLost    int   // next N credits execute but their response is lost
	creditSendErr error // credit refused before broadcast
	settleLost    bool  // next settle executes but its response is lost

	paused          bool
	windowRemaining *big.Int // credit window left; nil = unlimited
	creditWindowCap *big.Int // nil = no margin kept
	// debit window left (nil = unlimited) and its cap; a reverse never gives
	// usage back, as on-chain.
	debitWindowRemaining *big.Int
	debitWindowCap       *big.Int
	// nextDebitErr fails the next debit once, before it executes.
	nextDebitErr error
	// afterReverse runs once a reverse lands — a buyer acting in the gap.
	afterReverse func()

	debitCalls, settleCalls, reverseCalls, creditCalls, sequences int
	lastMaxFromBought                                             *big.Int
	calls                                                         []string
}

func (f *fakeEntitlement) CreditHeadroom(context.Context) (WindowHeadroom, error) {
	rem := f.windowRemaining
	if rem == nil {
		rem = mcgpBig(1_000_000_000)
	}
	return WindowHeadroom{Paused: f.paused, WindowCap: f.creditWindowCap, WindowRemaining: rem}, nil
}

func (f *fakeEntitlement) DebitHeadroom(context.Context) (WindowHeadroom, error) {
	rem := f.debitWindowRemaining
	if rem == nil {
		rem = mcgpBig(1_000_000_000)
	}
	return WindowHeadroom{Paused: f.paused, WindowCap: f.debitWindowCap, WindowRemaining: rem, MaxPerTx: f.limits.MaxDebitPerTx}, nil
}

func (f *fakeEntitlement) Backing() (*big.Int, *big.Int, error) {
	return new(big.Int).Set(f.held), new(big.Int).Set(f.acct), nil
}

func (f *fakeEntitlement) OperatorSequence(_ context.Context, fn func(ops OperatorOps) error) error {
	f.sequences++
	return fn(f)
}

func newFakeEntitlement() *fakeEntitlement {
	return &fakeEntitlement{
		held: new(big.Int), acct: new(big.Int),
		bought: map[common.Address]*big.Int{}, credited: map[common.Address]*big.Int{}, pending: map[common.Address]*big.Int{},
		debits: map[[32]byte]DebitRecord{}, fromBought: map[[32]byte]*big.Int{}, creditRefs: map[[32]byte]bool{},
		limits: SpendableLimits{MaxDebitPerTx: mcgpBig(10_000), MaxCreditPerTx: mcgpBig(10_000), MinCredit: big.NewInt(0)},
	}
}

func bal(m map[common.Address]*big.Int, a common.Address) *big.Int {
	if v, ok := m[a]; ok {
		return v
	}
	v := new(big.Int)
	m[a] = v
	return v
}

// buy gives user bought premium backed by MCGP the contract holds (buyPremium).
func (f *fakeEntitlement) buy(user string, amount *big.Int) {
	a := common.HexToAddress(user)
	bal(f.bought, a).Add(bal(f.bought, a), amount)
	f.acct.Add(f.acct, amount)
	f.held.Add(f.held, amount)
}

func (f *fakeEntitlement) premium(user string) *big.Int {
	a := common.HexToAddress(user)
	return new(big.Int).Add(bal(f.bought, a), bal(f.credited, a))
}

// invariants asserts accountedMcgp == Σ buckets and held >= accounted.
func (f *fakeEntitlement) invariants(t *testing.T) {
	t.Helper()
	sum := new(big.Int)
	for _, m := range []map[common.Address]*big.Int{f.bought, f.credited, f.pending} {
		for _, v := range m {
			sum.Add(sum, v)
		}
	}
	if sum.Cmp(f.acct) != 0 {
		t.Fatalf("accountedMcgp %s != Σ buckets %s", f.acct, sum)
	}
	if f.held.Cmp(f.acct) < 0 {
		t.Fatalf("Underbacked: held %s < accounted %s", f.held, f.acct)
	}
}

func revert(reason string) error {
	return notBroadcast(fmt.Errorf("gas estimation failed (transaction would likely revert): execution reverted: %s", reason))
}

func (f *fakeEntitlement) HasOperator() bool { return true }
func (f *fakeEntitlement) PremiumBalance(user string) (*big.Int, error) {
	return f.premium(user), nil
}
func (f *fakeEntitlement) SpendableLimits(context.Context) (SpendableLimits, error) {
	f.limitsReads++
	if f.limitsErr != nil {
		return SpendableLimits{}, f.limitsErr
	}
	return f.limits, nil
}
func (f *fakeEntitlement) DebitRecord(_ context.Context, ref [32]byte) (DebitRecord, error) {
	if r, ok := f.debits[ref]; ok {
		return r, nil
	}
	return DebitRecord{Amount: big.NewInt(0), State: DebitStateNone}, nil
}
func (f *fakeEntitlement) DebitFromBought(ref [32]byte) (*big.Int, error) {
	if v, ok := f.fromBought[ref]; ok {
		return v, nil
	}
	return big.NewInt(0), nil
}
func (f *fakeEntitlement) UsedCreditRef(_ context.Context, ref [32]byte) (bool, error) {
	return f.creditRefs[ref], nil
}
func (f *fakeEntitlement) VerifyOperatorEvent(string, string, common.Address, [32]byte) (*big.Int, error) {
	if f.verifyErr != nil {
		return nil, f.verifyErr
	}
	return big.NewInt(0), nil
}

func (f *fakeEntitlement) OperatorDebitSpendable(_ context.Context, user string, amount, maxFromBought *big.Int, ref [32]byte) (string, error) {
	f.debitCalls++
	f.lastMaxFromBought = maxFromBought
	if f.debitSendErr != nil {
		return "", f.debitSendErr
	}
	if f.nextDebitErr != nil {
		err := f.nextDebitErr
		f.nextDebitErr = nil
		return "", err
	}
	if f.paused {
		return "", revert("EnforcedPause")
	}
	if amount.Cmp(f.limits.MaxDebitPerTx) > 0 {
		return "", revert("Over per-tx debit cap")
	}
	if f.debitWindowRemaining != nil && f.debitWindowRemaining.Cmp(amount) < 0 {
		return "", revert("Over debit window cap")
	}
	if f.debits[ref].State != DebitStateNone {
		return "", revert("Debit exists")
	}
	a := common.HexToAddress(user)
	cr := bal(f.credited, a)
	fromCredited := new(big.Int).Set(amount)
	if cr.Cmp(amount) < 0 {
		fromCredited.Set(cr)
	}
	fromBought := new(big.Int).Sub(amount, fromCredited)
	if fromBought.Cmp(maxFromBought) > 0 {
		return "", revert("Exceeds allowed bought draw")
	}
	if bal(f.bought, a).Cmp(fromBought) < 0 {
		return "", revert("Insufficient premium balance")
	}
	cr.Sub(cr, fromCredited)
	bal(f.bought, a).Sub(bal(f.bought, a), fromBought)
	bal(f.pending, a).Add(bal(f.pending, a), amount)
	f.fromBought[ref] = fromBought
	f.debits[ref] = DebitRecord{User: a, Amount: new(big.Int).Set(amount), State: DebitStatePending}
	if f.debitWindowRemaining != nil {
		f.debitWindowRemaining = new(big.Int).Sub(f.debitWindowRemaining, amount)
	}
	f.calls = append(f.calls, "debit")
	if f.debitLostResp {
		f.debitLostResp = false
		return "", errors.New("send: context deadline exceeded")
	}
	return "0xdebit", nil
}

func (f *fakeEntitlement) OperatorSettleDebit(_ context.Context, ref [32]byte) (string, error) {
	f.settleCalls++
	if f.paused {
		return "", revert("EnforcedPause")
	}
	d, ok := f.debits[ref]
	if !ok || d.State != DebitStatePending {
		return "", revert("Not pending")
	}
	f.calls = append(f.calls, "settle")
	d.State = DebitStateResolved
	f.debits[ref] = d
	bal(f.pending, d.User).Sub(bal(f.pending, d.User), d.Amount)
	f.acct.Sub(f.acct, d.Amount)
	if f.settleLost {
		f.settleLost = false
		return "", errors.New("send: connection reset")
	}
	return "0xsettle", nil
}

func (f *fakeEntitlement) OperatorReverseDebit(_ context.Context, ref [32]byte) (string, error) {
	f.reverseCalls++
	d, ok := f.debits[ref]
	if !ok || d.State != DebitStatePending {
		return "", revert("Not pending")
	}
	d.State = DebitStateResolved
	f.debits[ref] = d
	fb := f.fromBought[ref]
	if fb == nil {
		fb = new(big.Int)
	}
	bal(f.pending, d.User).Sub(bal(f.pending, d.User), d.Amount)
	bal(f.credited, d.User).Add(bal(f.credited, d.User), new(big.Int).Sub(d.Amount, fb))
	bal(f.bought, d.User).Add(bal(f.bought, d.User), fb)
	f.calls = append(f.calls, "reverse")
	if f.afterReverse != nil {
		hook := f.afterReverse
		f.afterReverse = nil
		hook()
	}
	return "0xreverse", nil
}

func (f *fakeEntitlement) OperatorCreditPremium(_ context.Context, user string, amount *big.Int, ref [32]byte) (string, error) {
	f.creditCalls++
	if f.creditSendErr != nil {
		return "", f.creditSendErr
	}
	if f.paused {
		return "", revert("EnforcedPause")
	}
	if amount.Cmp(f.limits.MaxCreditPerTx) > 0 {
		return "", revert("Over per-tx cap")
	}
	if f.windowRemaining != nil {
		if f.windowRemaining.Cmp(amount) < 0 {
			return "", revert("Over window cap")
		}
		f.windowRemaining = new(big.Int).Sub(f.windowRemaining, amount)
	}
	if f.creditRefs[ref] {
		return "", revert("Ref already used")
	}
	next := new(big.Int).Add(f.acct, amount)
	if f.held.Cmp(next) < 0 {
		return "", revert("Underbacked: fund MCGP first")
	}
	f.creditRefs[ref] = true
	f.calls = append(f.calls, "credit")
	a := common.HexToAddress(user)
	bal(f.credited, a).Add(bal(f.credited, a), amount)
	f.acct = next
	if f.creditLost > 0 {
		f.creditLost--
		return "", errors.New("send: i/o timeout")
	}
	return "0xcredit", nil
}

// ── rig ──────────────────────────────────────────────────────────────────

const (
	orderBuyerWallet  = "0x87C2422Be9a11B71a5C530Ecd156134D3b04E3Ce"
	orderSellerWallet = "0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96"
)

type orderRig struct {
	db    *gorm.DB
	svc   *OrderSpendablePaymentService
	chain *fakeEntitlement
}

func newOrderRig(t *testing.T) *orderRig {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, ddl := range []string{
		`CREATE TABLE orders (id TEXT PRIMARY KEY, buyer_id TEXT, seller_id TEXT, product_id TEXT,
			quantity INTEGER, kind TEXT, token TEXT, status TEXT,
			product_amount TEXT, shipping_amount TEXT, platform_fee TEXT, total_amount TEXT,
			contract_order_id TEXT, escrow_contract_address TEXT, escrow_expires_at DATETIME,
			approve_tx_hash TEXT, escrow_tx_hash TEXT, payment_source TEXT DEFAULT '',
			seller_delivered_at DATETIME, disputed_at DATETIME,
			created_at DATETIME, updated_at DATETIME)`,
		orderMcgpPaymentsDDL,
		`CREATE TABLE products (id TEXT PRIMARY KEY, stock INTEGER, deleted_at DATETIME)`,
		`CREATE TABLE users (id TEXT PRIMARY KEY, wallet_address TEXT)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	chain := newFakeEntitlement()
	svc := NewOrderSpendablePaymentService(db, chain, nil)
	svc.driveInline = true
	return &orderRig{db: db, svc: svc, chain: chain}
}

type rigOrder struct {
	id, buyer, seller, product uuid.UUID
	total                      *big.Int
}

// pendingOrder stores an unpaid 1-unit MCGP product order for total MCGP.
func (r *orderRig) pendingOrder(t *testing.T, totalMcgp int64) rigOrder {
	t.Helper()
	o := rigOrder{id: uuid.New(), buyer: uuid.New(), seller: uuid.New(), product: uuid.New(), total: mcgpBig(totalMcgp)}
	now := time.Now()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO products (id, stock) VALUES (?, 5)`, []any{o.product}},
		{`INSERT INTO users (id, wallet_address) VALUES (?, ?)`, []any{o.seller, orderSellerWallet}},
		{`INSERT INTO orders (id, buyer_id, seller_id, product_id, quantity, kind, token, status,
			product_amount, shipping_amount, platform_fee, total_amount, payment_source, created_at, updated_at)
			VALUES (?, ?, ?, ?, 1, 'product', 'MCGP', ?, ?, '0', '0', ?, '', ?, ?)`,
			[]any{o.id, o.buyer, o.seller, o.product, models.OrderStatusPendingPayment, o.total.String(), o.total.String(), now, now}},
	} {
		if err := r.db.Exec(q.sql, q.args...).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return o
}

func (r *orderRig) fund(o rigOrder) (*models.OrderMcgpPayment, error) {
	defer func() { r.chain.calls = nil }() // tests assert what the resolution does
	return r.svc.Fund(context.Background(), OrderMcgpFundInput{
		OrderID: o.id, BuyerID: o.buyer, BuyerWallet: orderBuyerWallet, Amount: o.total,
	})
}

// decidedAgo backdates a payment's decision, e.g. past the uncollectable window.
func (r *orderRig) decidedAgo(t *testing.T, orderID uuid.UUID, d time.Duration) {
	t.Helper()
	if err := r.db.Exec(`UPDATE order_mcgp_payments SET decided_at = ?, updated_at = ? WHERE order_id = ?`,
		time.Now().Add(-d), time.Now().Add(-time.Hour), orderID).Error; err != nil {
		t.Fatalf("decidedAgo: %v", err)
	}
}

func (r *orderRig) order(t *testing.T, id uuid.UUID) models.Order {
	t.Helper()
	var o models.Order
	if err := r.db.First(&o, "id = ?", id).Error; err != nil {
		t.Fatalf("load order: %v", err)
	}
	return o
}

func (r *orderRig) payment(t *testing.T, orderID uuid.UUID) models.OrderMcgpPayment {
	t.Helper()
	p, err := r.svc.PaymentForOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("load payment: %v", err)
	}
	return *p
}

// age backdates a payment so the reconciler, which leaves fresh rows to the
// request driving them, picks it up.
func (r *orderRig) age(t *testing.T, orderID uuid.UUID, d time.Duration) {
	t.Helper()
	if err := r.db.Exec(`UPDATE order_mcgp_payments SET updated_at = ? WHERE order_id = ?`, time.Now().Add(-d), orderID).Error; err != nil {
		t.Fatalf("age: %v", err)
	}
}

func setOrderStatus(t *testing.T, db *gorm.DB, id uuid.UUID, status string) {
	t.Helper()
	if err := db.Exec(`UPDATE orders SET status = ? WHERE id = ?`, status, id).Error; err != nil {
		t.Fatalf("set status: %v", err)
	}
}

func completeOrder(o *models.Order) func(tx *gorm.DB) error {
	return func(tx *gorm.DB) error {
		return tx.Model(&models.Order{}).Where("id = ?", o.ID).Update("status", models.OrderStatusCompleted).Error
	}
}

func (r *orderRig) release(t *testing.T, o rigOrder) (*models.OrderMcgpPayment, error) {
	t.Helper()
	ord := r.order(t, o.id)
	return r.svc.Release(context.Background(), o.id, OrderMcgpReleasePlan{
		Reason: models.OrderMcgpReleaseBuyerConfirmed, SellerWallet: orderSellerWallet, SellerAmount: o.total,
	}, completeOrder(&ord))
}

// ── fund ─────────────────────────────────────────────────────────────────

func TestOrderMcgp_FundHoldsSpendableAndEscrowsTheOrder(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)

	pay, err := r.fund(o)
	if err != nil {
		t.Fatalf("fund: %v", err)
	}
	if pay.Status != models.OrderMcgpHeld {
		t.Fatalf("payment status = %s, want held", pay.Status)
	}
	if r.chain.lastMaxFromBought.Cmp(o.total) != 0 {
		t.Errorf("maxFromBought = %s, want the whole amount (bought premium is spendable)", r.chain.lastMaxFromBought)
	}
	if pay.FromBought != o.total.String() {
		t.Errorf("fromBought = %q, want %s", pay.FromBought, o.total)
	}
	ord := r.order(t, o.id)
	if ord.Status != models.OrderStatusEscrowed || !ord.IsSpendableMCGP() || ord.EscrowExpiresAt == nil {
		t.Fatalf("order = %s/%q/%v, want escrowed spendable with an expiry", ord.Status, ord.PaymentSource, ord.EscrowExpiresAt)
	}
	if got := r.chain.premium(orderBuyerWallet); got.Cmp(mcgpBig(60)) != 0 {
		t.Fatalf("buyer spendable = %s, want 60 MCGP", got)
	}
	if want := common.BytesToHash(func() []byte { x := OrderMcgpDebitRef(o.id); return x[:] }()).Hex(); pay.DebitRef != want {
		t.Errorf("debit ref not derived from the order")
	}
	r.chain.invariants(t)
}

func TestOrderMcgp_FundIsIdempotentPerOrder(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)
	if _, err := r.fund(o); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := r.fund(o); err != nil {
		t.Fatalf("second: %v", err)
	}
	if r.chain.debitCalls != 1 {
		t.Fatalf("a retried pay must not debit twice; got %d debits", r.chain.debitCalls)
	}
}

func TestOrderMcgp_FundRefusesAShortBalanceBeforeBroadcasting(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(10))
	o := r.pendingOrder(t, 40)

	_, err := r.fund(o)
	var short *SpendableShortfallError
	if !errors.As(err, &short) || short.Need.Cmp(mcgpBig(40)) != 0 || short.Have.Cmp(mcgpBig(10)) != 0 {
		t.Fatalf("expected a shortfall of 40 vs 10, got %v", err)
	}
	if r.chain.debitCalls != 0 {
		t.Fatal("nothing may be broadcast for a known shortfall")
	}
	if ord := r.order(t, o.id); ord.Status != models.OrderStatusPendingPayment {
		t.Fatalf("order must stay payable, got %s", ord.Status)
	}
}

func TestOrderMcgp_FundRefusesAnAmountOverTheCaps(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(1000))
	r.chain.limits.MaxCreditPerTx = mcgpBig(30) // the seller credit could never carry it
	o := r.pendingOrder(t, 40)

	_, err := r.fund(o)
	var lim *SpendableLimitError
	if !errors.As(err, &lim) || lim.Max.Cmp(mcgpBig(30)) != 0 {
		t.Fatalf("expected a limit error at 30 MCGP, got %v", err)
	}
	if r.chain.debitCalls != 0 {
		t.Fatal("an over-cap payment must not be broadcast")
	}
}

// A debit the contract refuses at estimation never left: the payment fails,
// the order stays payable, and a later attempt under the same ref succeeds.
func TestOrderMcgp_RefusedDebitIsRetryableUnderTheSameRef(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)

	r.chain.debitSendErr = revert("Over debit window cap")
	if _, err := r.fund(o); !errors.Is(err, ErrSpendableOverWindow) {
		t.Fatalf("expected the window-cap refusal, got %v", err)
	}
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpFailed {
		t.Fatalf("payment = %s, want failed", p.Status)
	}
	r.chain.debitSendErr = nil
	pay, err := r.fund(o)
	if err != nil || pay.Status != models.OrderMcgpHeld {
		t.Fatalf("retry: %v / %+v", err, pay)
	}
	r.chain.invariants(t)
}

// The failure the whole state machine exists for: the debit executed but the
// response was lost. The row stays debiting (no double debit on retry), and
// the reconciler adopts the debit from chain state.
func TestOrderMcgp_LostDebitResponseIsAdoptedFromChainState(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)

	r.chain.debitLostResp = true
	if _, err := r.fund(o); !errors.Is(err, ErrOrderMcgpPending) {
		t.Fatalf("expected pending, got %v", err)
	}
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpDebiting {
		t.Fatalf("payment = %s, want debiting", p.Status)
	}
	// The unpaid-order sweep must not cancel an order under an in-flight debit.
	cancelled, err := CancelUnpaidOrder(r.db, &models.Order{ID: o.id, ProductID: o.product, Quantity: 1})
	if err != nil || cancelled {
		t.Fatalf("an order with a debit in flight must not be cancelled (cancelled=%v err=%v)", cancelled, err)
	}

	r.age(t, o.id, 10*time.Minute)
	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpHeld {
		t.Fatalf("payment = %s, want held", p.Status)
	}
	if ord := r.order(t, o.id); ord.Status != models.OrderStatusEscrowed {
		t.Fatalf("order = %s, want escrowed", ord.Status)
	}
	if r.chain.debitCalls != 1 {
		t.Fatalf("debits = %d, want exactly 1", r.chain.debitCalls)
	}
	r.chain.invariants(t)
}

// A debit that lands after its order closed is returned, not kept.
func TestOrderMcgp_DebitLandingAfterTheOrderClosedIsReturned(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)

	r.chain.debitLostResp = true
	_, _ = r.fund(o)
	setOrderStatus(t, r.db, o.id, models.OrderStatusCancelled) // e.g. an operator closed it

	r.age(t, o.id, 10*time.Minute)
	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpReversed {
		t.Fatalf("payment = %s, want reversed", p.Status)
	}
	if got := r.chain.premium(orderBuyerWallet); got.Cmp(mcgpBig(100)) != 0 {
		t.Fatalf("buyer spendable = %s, want 100 back", got)
	}
	r.chain.invariants(t)
}

// ── release ──────────────────────────────────────────────────────────────

func TestOrderMcgp_ReleaseSettlesThenCreditsTheSeller(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)
	if _, err := r.fund(o); err != nil {
		t.Fatalf("fund: %v", err)
	}
	heldBefore := new(big.Int).Set(r.chain.held)

	pay, err := r.release(t, o)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if pay.Status != models.OrderMcgpReleased {
		t.Fatalf("payment = %s, want released", pay.Status)
	}
	if ord := r.order(t, o.id); ord.Status != models.OrderStatusCompleted {
		t.Fatalf("order = %s, want completed", ord.Status)
	}
	seller := common.HexToAddress(orderSellerWallet)
	if got := bal(r.chain.credited, seller); got.Cmp(mcgpBig(40)) != 0 {
		t.Fatalf("seller credited = %s, want 40 MCGP (MCGP is fee-exempt)", got)
	}
	if bal(r.chain.bought, seller).Sign() != 0 {
		t.Fatal("the seller must receive spend-only (credited) premium, never bought")
	}
	if r.chain.held.Cmp(heldBefore) != 0 {
		t.Fatal("a release must not move MCGP tokens — only the ledger")
	}
	// No surplus in the contract: settle first, in one operator sequence.
	if strings.Join(r.chain.calls, ",") != "settle,credit" || r.chain.sequences != 1 {
		t.Fatalf("calls=%v sequences=%d, want settle,credit in one sequence", r.chain.calls, r.chain.sequences)
	}
	if p := r.payment(t, o.id); p.DecidedAt == nil {
		t.Fatal("the decision time is recorded with the claim")
	}
	r.chain.invariants(t)
}

// With enough unaccounted MCGP already in the contract, the seller is
// credited BEFORE the debit is settled, so no settled value is ever uncredited.
func TestOrderMcgp_ReleaseCreditsFirstWhenSurplusAllows(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	r.chain.held.Add(r.chain.held, mcgpBig(50)) // platform surplus
	o := r.pendingOrder(t, 40)
	_, _ = r.fund(o)

	if pay, err := r.release(t, o); err != nil || pay.Status != models.OrderMcgpReleased {
		t.Fatalf("release: %v / %+v", err, pay)
	}
	if strings.Join(r.chain.calls, ",") != "credit,settle" {
		t.Fatalf("calls = %v, want credit before settle", r.chain.calls)
	}
	r.chain.invariants(t)
}

// Nothing is settled while the credit that must follow could not land.
func TestOrderMcgp_ReleaseDoesNotSettleWhenTheCreditCannotLand(t *testing.T) {
	for name, block := range map[string]func(f *fakeEntitlement){
		"paused":         func(f *fakeEntitlement) { f.paused = true },
		"window":         func(f *fakeEntitlement) { f.windowRemaining = mcgpBig(10) },
		"maxCreditPerTx": func(f *fakeEntitlement) { f.limits.MaxCreditPerTx = mcgpBig(10) },
	} {
		t.Run(name, func(t *testing.T) {
			r := newOrderRig(t)
			r.chain.buy(orderBuyerWallet, mcgpBig(100))
			o := r.pendingOrder(t, 40)
			if _, err := r.fund(o); err != nil {
				t.Fatalf("fund: %v", err)
			}
			block(r.chain)
			pay, err := r.release(t, o)
			if err != nil || pay.Status != models.OrderMcgpReleasing {
				t.Fatalf("release: %v / %+v", err, pay)
			}
			if r.chain.settleCalls != 0 || r.chain.creditCalls != 0 {
				t.Fatalf("settles=%d credits=%d: nothing may move while the credit is blocked", r.chain.settleCalls, r.chain.creditCalls)
			}
			if !strings.Contains(pay.FailureReason, "cannot land") {
				t.Errorf("reason = %q", pay.FailureReason)
			}

			// Unblocked, the reconciler finishes it.
			r.chain.paused, r.chain.windowRemaining, r.chain.limits.MaxCreditPerTx = false, nil, mcgpBig(10_000)
			r.age(t, o.id, 10*time.Minute)
			if _, err := r.svc.Reconcile(context.Background()); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if p := r.payment(t, o.id); p.Status != models.OrderMcgpReleased {
				t.Fatalf("payment = %s, want released", p.Status)
			}
			r.chain.invariants(t)
		})
	}
}

// A settle that landed with its credit refused afterwards (the window filled
// in between) is credited on a later pass, exactly once.
func TestOrderMcgp_SettledButUncreditedIsCreditedLater(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)
	_, _ = r.fund(o)

	r.chain.creditSendErr = revert("Over window cap")
	if pay, _ := r.release(t, o); pay.Status != models.OrderMcgpReleasing {
		t.Fatalf("payment = %s, want releasing", pay.Status)
	}
	if rec, _ := r.chain.DebitRecord(context.Background(), OrderMcgpDebitRef(o.id)); rec.State != DebitStateResolved {
		t.Fatal("the settle should have landed")
	}
	r.chain.creditSendErr = nil
	r.age(t, o.id, 10*time.Minute)
	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpReleased {
		t.Fatalf("payment = %s", p.Status)
	}
	if got := bal(r.chain.credited, common.HexToAddress(orderSellerWallet)); got.Cmp(mcgpBig(40)) != 0 {
		t.Fatalf("seller credited = %s, want 40", got)
	}
	if r.chain.settleCalls != 1 {
		t.Fatalf("settles = %d, want 1", r.chain.settleCalls)
	}
	r.chain.invariants(t)
}

// A release interrupted after the settle and after the credit executed (but
// before either response came back) is finished by the reconciler without a
// second settle or a second credit.
func TestOrderMcgp_InterruptedReleaseFinishesWithoutPayingTwice(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)
	if _, err := r.fund(o); err != nil {
		t.Fatalf("fund: %v", err)
	}

	r.chain.settleLost = true
	pay, err := r.release(t, o)
	if err != nil {
		t.Fatalf("release claim: %v", err)
	}
	if pay.Status != models.OrderMcgpReleasing {
		t.Fatalf("payment = %s, want releasing after a lost settle response", pay.Status)
	}
	if ord := r.order(t, o.id); ord.Status != models.OrderStatusCompleted {
		t.Fatalf("the decision is recorded with the claim: order = %s", ord.Status)
	}

	r.chain.creditLost = 1
	r.age(t, o.id, 10*time.Minute)
	_, _ = r.svc.Reconcile(context.Background())
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpReleasing {
		t.Fatalf("payment = %s, want still releasing after a lost credit response", p.Status)
	}

	r.age(t, o.id, 10*time.Minute)
	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpReleased {
		t.Fatalf("payment = %s, want released", p.Status)
	}
	if r.chain.settleCalls != 1 || r.chain.creditCalls != 1 {
		t.Fatalf("settles=%d credits=%d, want exactly one of each", r.chain.settleCalls, r.chain.creditCalls)
	}
	if got := bal(r.chain.credited, common.HexToAddress(orderSellerWallet)); got.Cmp(mcgpBig(40)) != 0 {
		t.Fatalf("seller credited = %s, want 40 exactly", got)
	}
	r.chain.invariants(t)
}

func TestOrderMcgp_ReleaseIsIdempotent(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)
	_, _ = r.fund(o)
	if _, err := r.release(t, o); err != nil {
		t.Fatalf("release: %v", err)
	}
	pay, err := r.release(t, o)
	if !errors.Is(err, ErrOrderMcgpAlreadyDecided) || pay == nil || pay.Status != models.OrderMcgpReleased {
		t.Fatalf("second release: %v / %+v, want ErrOrderMcgpAlreadyDecided with the payment", err, pay)
	}
	if _, err := r.svc.Refund(context.Background(), o.id, nil); !errors.Is(err, ErrOrderMcgpBadState) {
		t.Fatalf("refund after release: %v, want ErrOrderMcgpBadState", err)
	}
	if r.chain.settleCalls != 1 || r.chain.creditCalls != 1 {
		t.Fatalf("settles=%d credits=%d, want 1/1", r.chain.settleCalls, r.chain.creditCalls)
	}
}

// ── refund ───────────────────────────────────────────────────────────────

func TestOrderMcgp_RefundRestoresTheBuyerBucketForBucket(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(30))
	buyer := common.HexToAddress(orderBuyerWallet)
	bal(r.chain.credited, buyer).Add(bal(r.chain.credited, buyer), mcgpBig(20)) // spend-only from IT
	r.chain.acct.Add(r.chain.acct, mcgpBig(20))
	r.chain.held.Add(r.chain.held, mcgpBig(20))
	o := r.pendingOrder(t, 40) // 20 credited + 20 bought

	if _, err := r.fund(o); err != nil {
		t.Fatalf("fund: %v", err)
	}
	ord := r.order(t, o.id)
	pay, err := r.svc.Refund(context.Background(), o.id, func(tx *gorm.DB) error {
		if err := tx.Model(&models.Order{}).Where("id = ?", ord.ID).Update("status", models.OrderStatusRefunded).Error; err != nil {
			return err
		}
		return RestoreOrderStock(tx, &ord)
	})
	if err != nil || pay.Status != models.OrderMcgpReversed {
		t.Fatalf("refund: %v / %+v", err, pay)
	}
	if got := bal(r.chain.bought, buyer); got.Cmp(mcgpBig(30)) != 0 {
		t.Fatalf("bought = %s, want 30 (the bought share must come back as bought)", got)
	}
	if got := bal(r.chain.credited, buyer); got.Cmp(mcgpBig(20)) != 0 {
		t.Fatalf("credited = %s, want 20", got)
	}
	var stock int
	r.db.Raw(`SELECT stock FROM products WHERE id = ?`, o.product).Scan(&stock)
	if stock != 6 {
		t.Fatalf("stock = %d, want 6 (restored)", stock)
	}
	// Refund and release are exclusive.
	if _, err := r.release(t, o); !errors.Is(err, ErrOrderMcgpBadState) {
		t.Fatalf("release after refund: %v, want ErrOrderMcgpBadState", err)
	}
	r.chain.invariants(t)
}

// A claim whose order write fails leaves the payment held: the decision and
// the payment move together or not at all.
func TestOrderMcgp_ClaimRollsBackWithTheOrderWrite(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)
	_, _ = r.fund(o)

	_, err := r.svc.Refund(context.Background(), o.id, func(*gorm.DB) error { return ErrOrderMcgpBadState })
	if !errors.Is(err, ErrOrderMcgpBadState) {
		t.Fatalf("got %v", err)
	}
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpHeld {
		t.Fatalf("payment = %s, want held", p.Status)
	}
	if r.chain.reverseCalls != 0 {
		t.Fatal("nothing may move on-chain when the decision did not commit")
	}
}

// ── split + escrow expiry ────────────────────────────────────────────────

// A split never credits the buyer: the whole hold is reversed (buckets
// restored) and only the non-buyer share is debited back, credited premium
// first — so MCGP the buyer bought with USDC stays self-exitable.
func TestOrderMcgp_SplitKeepsTheBuyersBoughtPremiumSelfExitable(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(80))
	buyer, seller := common.HexToAddress(orderBuyerWallet), common.HexToAddress(orderSellerWallet)
	bal(r.chain.credited, buyer).Add(bal(r.chain.credited, buyer), mcgpBig(20)) // spend-only from IT
	r.chain.acct.Add(r.chain.acct, mcgpBig(20))
	r.chain.held.Add(r.chain.held, mcgpBig(20))
	o := r.pendingOrder(t, 100) // draws 20 credited + 80 bought
	_, _ = r.fund(o)
	ord := r.order(t, o.id)

	pay, err := r.svc.Release(context.Background(), o.id, OrderMcgpReleasePlan{
		Reason: models.OrderMcgpReleaseAdminResolved, SellerWallet: orderSellerWallet,
		SellerAmount: mcgpBig(10), BuyerAmount: mcgpBig(85),
	}, completeOrder(&ord))
	if err != nil || pay.Status != models.OrderMcgpReleased {
		t.Fatalf("split: %v / %+v", err, pay)
	}
	// Buyer keeps 85: the 15 taken back came out of credited first (all 20
	// credited would cover it), so all 80 bought stay bought.
	if got := bal(r.chain.bought, buyer); got.Cmp(mcgpBig(80)) != 0 {
		t.Fatalf("buyer bought = %s, want 80 (never downgraded to spend-only)", got)
	}
	if got := bal(r.chain.credited, buyer); got.Cmp(mcgpBig(5)) != 0 {
		t.Fatalf("buyer credited = %s, want 5", got)
	}
	if bal(r.chain.credited, seller).Cmp(mcgpBig(10)) != 0 {
		t.Fatalf("seller = %s, want 10", bal(r.chain.credited, seller))
	}
	if strings.Join(r.chain.calls, ",") != "reverse,debit,settle,credit" {
		t.Fatalf("calls = %v", r.chain.calls)
	}
	if excess := new(big.Int).Sub(r.chain.held, r.chain.acct); excess.Cmp(mcgpBig(5)) != 0 {
		t.Fatalf("platform excess = %s, want 5", excess)
	}
	r.chain.invariants(t)

	if _, err := r.svc.Release(context.Background(), uuid.New(), OrderMcgpReleasePlan{}, nil); !errors.Is(err, ErrOrderMcgpNotFound) {
		t.Fatalf("unknown order: %v", err)
	}
}

func TestOrderMcgp_ReleasePlanCannotPayMoreThanHeld(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)
	_, _ = r.fund(o)
	_, err := r.svc.Release(context.Background(), o.id, OrderMcgpReleasePlan{
		SellerWallet: orderSellerWallet, SellerAmount: mcgpBig(40), BuyerAmount: big.NewInt(1),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "more than was held") {
		t.Fatalf("expected an over-pay refusal, got %v", err)
	}
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpHeld {
		t.Fatalf("payment = %s, want held", p.Status)
	}
}

func expireEscrow(t *testing.T, db *gorm.DB, id uuid.UUID, delivered bool) {
	t.Helper()
	past := time.Now().Add(-(orderMcgpExpiryGrace + time.Hour))
	q := `UPDATE orders SET escrow_expires_at = ? WHERE id = ?`
	args := []any{past, id}
	if delivered {
		q = `UPDATE orders SET escrow_expires_at = ?, seller_delivered_at = ?, status = 'delivered' WHERE id = ?`
		args = []any{past, past, id}
	}
	if err := db.Exec(q, args...).Error; err != nil {
		t.Fatalf("expire: %v", err)
	}
}

// Mirrors ProductEscrowV2.autoRefund for a delivered-but-unconfirmed order:
// 90% buyer / 3% seller / 7% platform.
func TestOrderMcgp_ExpiredDeliveredHoldSplitsLikeAutoRefund(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 100)
	_, _ = r.fund(o)
	expireEscrow(t, r.db, o.id, true)

	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	p := r.payment(t, o.id)
	if p.Status != models.OrderMcgpReleased || p.ReleaseReason != models.OrderMcgpReleaseEscrowExpired {
		t.Fatalf("payment = %s/%s", p.Status, p.ReleaseReason)
	}
	buyer, seller := common.HexToAddress(orderBuyerWallet), common.HexToAddress(orderSellerWallet)
	// The buyer's 90 stays USDC-bought (self-exitable), never credited.
	if bal(r.chain.bought, buyer).Cmp(mcgpBig(90)) != 0 || bal(r.chain.credited, buyer).Sign() != 0 {
		t.Fatalf("buyer bought=%s credited=%s, want 90/0", bal(r.chain.bought, buyer), bal(r.chain.credited, buyer))
	}
	if bal(r.chain.credited, seller).Cmp(mcgpBig(3)) != 0 {
		t.Fatalf("seller = %s, want 3", bal(r.chain.credited, seller))
	}
	if excess := new(big.Int).Sub(r.chain.held, r.chain.acct); excess.Cmp(mcgpBig(7)) != 0 {
		t.Fatalf("platform = %s, want 7", excess)
	}
	if ord := r.order(t, o.id); ord.Status != models.OrderStatusRefunded {
		t.Fatalf("order = %s, want refunded", ord.Status)
	}
	r.chain.invariants(t)
}

// A seller share below the contract's minimum credit is left with the
// platform instead of stalling the buyer's 90%.
func TestOrderMcgp_ExpirySplitFoldsADustSellerShare(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 100)
	_, _ = r.fund(o)
	r.chain.limits.MinCredit = mcgpBig(5) // 3% = 3 MCGP is below it
	r.svc.limits = nil                    // drop the cached caps
	expireEscrow(t, r.db, o.id, true)

	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpReleased || p.SellerAmount != "0" {
		t.Fatalf("payment = %s seller=%s", p.Status, p.SellerAmount)
	}
	if r.chain.creditCalls != 0 {
		t.Fatal("a dust share must not be credited")
	}
	if excess := new(big.Int).Sub(r.chain.held, r.chain.acct); excess.Cmp(mcgpBig(10)) != 0 {
		t.Fatalf("platform = %s, want 10", excess)
	}
	r.chain.invariants(t)
}

// If the buyer spends the restored MCGP before the non-buyer share is taken
// back, the split closes (alerted) instead of retrying forever.
func TestOrderMcgp_SplitWithAnUncollectableShareClosesForReview(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 100)
	_, _ = r.fund(o)
	buyer := common.HexToAddress(orderBuyerWallet)
	r.chain.afterReverse = func() {
		bal(r.chain.bought, buyer).SetInt64(0)
		r.chain.acct.Sub(r.chain.acct, mcgpBig(100))
		r.chain.held.Sub(r.chain.held, mcgpBig(100))
	}
	expireEscrow(t, r.db, o.id, true)

	// Inside the retry window a shortfall is treated as transient.
	_, _ = r.svc.Reconcile(context.Background())
	p := r.payment(t, o.id)
	if p.Status != models.OrderMcgpReleasing || !strings.Contains(p.FailureReason, "retrying") {
		t.Fatalf("within the window: payment = %s reason=%q", p.Status, p.FailureReason)
	}

	// Past it, with the chain re-read and still short, the split closes.
	r.decidedAgo(t, o.id, 25*time.Hour)
	_, _ = r.svc.Reconcile(context.Background())
	p = r.payment(t, o.id)
	if p.Status != models.OrderMcgpReleased || !strings.HasPrefix(p.FailureReason, orderMcgpUncollectablePrefix) {
		t.Fatalf("payment = %s reason=%q", p.Status, p.FailureReason)
	}
	if r.chain.creditCalls != 0 || r.chain.settleCalls != 0 {
		t.Fatal("nothing may be credited for a share that was never collected")
	}
	r.chain.invariants(t)
}

// A balance that covers the collect again (e.g. another pending debit was
// reversed) keeps the split retrying even past the window, and it completes.
func TestOrderMcgp_SplitShortfallThatRecoversCompletes(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 100)
	_, _ = r.fund(o)
	expireEscrow(t, r.db, o.id, true)
	r.chain.afterReverse = func() { r.chain.nextDebitErr = revert("Insufficient premium balance") }

	_, _ = r.svc.Reconcile(context.Background())
	// Past the window the refusal repeats, but the re-read balance covers the
	// collect (the refusal was a fluke): keep retrying, don't close.
	r.decidedAgo(t, o.id, 25*time.Hour)
	r.chain.nextDebitErr = revert("Insufficient premium balance")
	_, _ = r.svc.Reconcile(context.Background())
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpReleasing {
		t.Fatalf("payment = %s reason=%q; a covered balance must not close the split", p.Status, p.FailureReason)
	}
	r.age(t, o.id, 10*time.Minute)
	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpReleased || p.FailureReason != "" {
		t.Fatalf("payment = %s reason=%q", p.Status, p.FailureReason)
	}
	if bal(r.chain.credited, common.HexToAddress(orderSellerWallet)).Cmp(mcgpBig(3)) != 0 {
		t.Fatal("seller should be paid once the collect lands")
	}
	r.chain.invariants(t)
}

// The reverse is not sent while the collect would not fit the debit window:
// the buyer never holds 100% waiting on a window to reset.
func TestOrderMcgp_SplitWaitsForDebitWindowBeforeReversing(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 100)
	_, _ = r.fund(o)
	r.chain.debitWindowRemaining = mcgpBig(5) // collect is 10
	expireEscrow(t, r.db, o.id, true)

	_, _ = r.svc.Reconcile(context.Background())
	p := r.payment(t, o.id)
	if p.Status != models.OrderMcgpReleasing || r.chain.reverseCalls != 0 {
		t.Fatalf("payment = %s reverses=%d; nothing may move while the collect cannot fit", p.Status, r.chain.reverseCalls)
	}
	if rec, _ := r.chain.DebitRecord(context.Background(), OrderMcgpDebitRef(o.id)); rec.State != DebitStatePending {
		t.Fatal("the hold must still be pending")
	}

	// Also refused when the per-call cap was lowered below the collect.
	r.chain.debitWindowRemaining = nil
	r.chain.limits.MaxDebitPerTx = mcgpBig(5)
	r.age(t, o.id, 10*time.Minute)
	_, _ = r.svc.Reconcile(context.Background())
	if r.chain.reverseCalls != 0 {
		t.Fatal("maxDebitPerTx below the collect must block the reverse")
	}

	r.chain.limits.MaxDebitPerTx = mcgpBig(10_000)
	r.age(t, o.id, 10*time.Minute)
	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpReleased {
		t.Fatalf("payment = %s", p.Status)
	}
	r.chain.invariants(t)
}

// Collect fails transiently after the reverse landed (RPC error with the
// outcome unknown, then a window refusal): each later pass resumes from chain
// state and the split completes with exactly one reverse, collect, settle and
// credit.
func TestOrderMcgp_SplitCollectFailsTransientlyAfterTheReverse(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 100)
	_, _ = r.fund(o)
	expireEscrow(t, r.db, o.id, true)
	r.chain.afterReverse = func() { r.chain.nextDebitErr = errors.New("send: i/o timeout") }

	_, _ = r.svc.Reconcile(context.Background())
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpReleasing || r.chain.reverseCalls != 1 {
		t.Fatalf("payment = %s reverses=%d", p.Status, r.chain.reverseCalls)
	}

	r.chain.nextDebitErr = revert("Over debit window cap")
	r.age(t, o.id, 10*time.Minute)
	_, _ = r.svc.Reconcile(context.Background())
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpReleasing {
		t.Fatalf("payment = %s", p.Status)
	}

	r.age(t, o.id, 10*time.Minute)
	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	p := r.payment(t, o.id)
	if p.Status != models.OrderMcgpReleased {
		t.Fatalf("payment = %s reason=%q", p.Status, p.FailureReason)
	}
	if r.chain.reverseCalls != 1 || r.chain.settleCalls != 1 || r.chain.creditCalls != 1 {
		t.Fatalf("reverses=%d settles=%d credits=%d", r.chain.reverseCalls, r.chain.settleCalls, r.chain.creditCalls)
	}
	if bal(r.chain.bought, common.HexToAddress(orderBuyerWallet)).Cmp(mcgpBig(90)) != 0 {
		t.Fatal("buyer should keep 90 bought")
	}
	r.chain.invariants(t)
}

// Two drivers of one interrupted split (the request goroutine and the
// reconciler, each holding a copy loaded before either ran) act one after
// the other on fresh chain state: nothing is done twice.
func TestOrderMcgp_SplitTwoDriversDoNotDoubleAct(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 100)
	_, _ = r.fund(o)
	expireEscrow(t, r.db, o.id, true)
	r.chain.afterReverse = func() { r.chain.nextDebitErr = errors.New("send: i/o timeout") }
	_, _ = r.svc.Reconcile(context.Background()) // interrupted between reverse and collect

	a, b := r.payment(t, o.id), r.payment(t, o.id)
	if err := r.svc.drive(context.Background(), &a); err != nil {
		t.Fatalf("driver A: %v", err)
	}
	_ = r.svc.drive(context.Background(), &b) // stale copy: may only no-op or be refused
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpReleased {
		t.Fatalf("payment = %s", p.Status)
	}
	collects := 0
	for _, c := range r.chain.calls {
		if c == "debit" {
			collects++
		}
	}
	if collects != 1 || r.chain.reverseCalls != 1 || r.chain.settleCalls != 1 || r.chain.creditCalls != 1 {
		t.Fatalf("collects=%d reverses=%d settles=%d credits=%d, want one each", collects, r.chain.reverseCalls, r.chain.settleCalls, r.chain.creditCalls)
	}
	r.chain.invariants(t)
}

// A collect whose outcome was unknown can mine after the split was closed as
// uncollectable; the reconciler finds it and completes the split, so the
// buyer's MCGP is not stranded in pendingSettle.
func TestOrderMcgp_OrphanCollectMinedAfterCloseIsRecovered(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 100)
	_, _ = r.fund(o)
	buyer := common.HexToAddress(orderBuyerWallet)
	// The buyer moves 95 out after the reverse, leaving too little to collect.
	r.chain.afterReverse = func() {
		bal(r.chain.bought, buyer).Sub(bal(r.chain.bought, buyer), mcgpBig(95))
		r.chain.acct.Sub(r.chain.acct, mcgpBig(95))
		r.chain.held.Sub(r.chain.held, mcgpBig(95))
	}
	expireEscrow(t, r.db, o.id, true)
	_, _ = r.svc.Reconcile(context.Background())
	r.decidedAgo(t, o.id, 25*time.Hour)
	_, _ = r.svc.Reconcile(context.Background())
	if p := r.payment(t, o.id); !strings.HasPrefix(p.FailureReason, orderMcgpUncollectablePrefix) {
		t.Fatalf("setup: payment = %s reason=%q", p.Status, p.FailureReason)
	}

	// The earlier collect broadcast now mines (the buyer topped up meanwhile).
	r.chain.buy(orderBuyerWallet, mcgpBig(20))
	if _, err := r.chain.OperatorDebitSpendable(context.Background(), orderBuyerWallet, mcgpBig(10), mcgpBig(10), orderMcgpCollectRef(o.id)); err != nil {
		t.Fatalf("mine orphan collect: %v", err)
	}
	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	p := r.payment(t, o.id)
	if p.Status != models.OrderMcgpReleased || p.FailureReason != "" {
		t.Fatalf("payment = %s reason=%q", p.Status, p.FailureReason)
	}
	if rec, _ := r.chain.DebitRecord(context.Background(), orderMcgpCollectRef(o.id)); rec.State != DebitStateResolved {
		t.Fatal("the orphan collect must be settled")
	}
	if bal(r.chain.pending, buyer).Sign() != 0 {
		t.Fatal("nothing may stay stranded in pendingSettle")
	}
	if bal(r.chain.credited, common.HexToAddress(orderSellerWallet)).Cmp(mcgpBig(3)) != 0 {
		t.Fatal("the seller is paid once the share is collected")
	}
	r.chain.invariants(t)
}

// A FULL release below minCredit is refused and left held for an admin —
// never settled with the seller paid nothing.
func TestOrderMcgp_FullReleaseBelowMinCreditStaysHeld(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 1) // funded while minCredit was 0
	_, _ = r.fund(o)
	r.chain.limits.MinCredit = mcgpBig(2) // raised after funding
	r.svc.limits = nil

	pay, err := r.release(t, o)
	if !errors.Is(err, ErrOrderMcgpPayoutBelowMin) {
		t.Fatalf("got %v, want ErrOrderMcgpPayoutBelowMin", err)
	}
	if pay.Status != models.OrderMcgpHeld || r.chain.settleCalls != 0 {
		t.Fatalf("payment = %s settles=%d: must stay held", pay.Status, r.chain.settleCalls)
	}
	if ord := r.order(t, o.id); ord.Status != models.OrderStatusEscrowed {
		t.Fatalf("order = %s, want still escrowed", ord.Status)
	}
	// The admin exit: a refund still works.
	if p, err := r.svc.Refund(context.Background(), o.id, nil); err != nil || p.Status != models.OrderMcgpReversed {
		t.Fatalf("refund: %v / %+v", err, p)
	}
}

// The credit headroom keeps a margin for operator transactions already in
// flight: a window that only just fits the credit is treated as full.
func TestOrderMcgp_CreditWindowMarginIsKept(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)
	_, _ = r.fund(o)
	r.chain.creditWindowCap = mcgpBig(200) // margin = 10
	r.chain.windowRemaining = mcgpBig(45)  // fits 40, but not 40 + margin

	if pay, _ := r.release(t, o); pay.Status != models.OrderMcgpReleasing || r.chain.settleCalls != 0 {
		t.Fatalf("payment = %s settles=%d", pay.Status, r.chain.settleCalls)
	}
}

// Expiry notices describe what actually happened, and only once the chain
// work is done.
func TestOrderMcgp_ExpiryNoticeMatchesTheOutcome(t *testing.T) {
	r := newOrderRig(t)
	bus := events.NewBus()
	t.Cleanup(bus.Stop)
	got := make(chan events.Event, 8)
	bus.Subscribe(events.OrderRefunded, func(e events.Event) { got <- e })
	r.svc.bus = bus
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 100)
	_, _ = r.fund(o)
	r.db.Exec(`INSERT INTO users (id, wallet_address) VALUES (?, ?)`, o.buyer, orderBuyerWallet)
	expireEscrow(t, r.db, o.id, true)

	// The seller's 3 MCGP is below minCredit, so it folds into the platform.
	r.chain.limits.MinCredit = mcgpBig(5)
	r.svc.limits = nil

	// Blocked by the debit window: decided, nothing moved, nothing announced.
	r.chain.debitWindowRemaining = mcgpBig(1)
	_, _ = r.svc.Reconcile(context.Background())
	select {
	case e := <-got:
		t.Fatalf("notice sent before the chain work finished: %q", e.Message)
	case <-time.After(200 * time.Millisecond):
	}

	// The notice then says the seller's share was folded.
	r.chain.debitWindowRemaining = nil
	r.age(t, o.id, 10*time.Minute)
	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	msgs := map[uuid.UUID]string{}
	for len(msgs) < 2 {
		select {
		case e := <-got:
			msgs[e.UserID] = e.Message
		case <-time.After(2 * time.Second):
			t.Fatalf("notices missing: %v", msgs)
		}
	}
	if !strings.Contains(msgs[o.buyer], "You keep 90 of your 100 MCGP") || !strings.Contains(msgs[o.buyer], "below the minimum payout") {
		t.Errorf("buyer notice = %q", msgs[o.buyer])
	}
	if !strings.Contains(msgs[o.seller], "below the minimum payout") {
		t.Errorf("seller notice = %q", msgs[o.seller])
	}
}

// The reconciler drives expiries inline, one after another — it never spawns
// goroutines that would queue on the operator signing lock.
func TestOrderMcgp_ExpiryDrivesInlineWithoutBackgroundWork(t *testing.T) {
	r := newOrderRig(t)
	r.svc.driveInline = false // production setting
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)
	_, _ = r.fund(o)
	expireEscrow(t, r.db, o.id, false)

	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// Finished by the time Reconcile returns: no goroutine was needed.
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpReversed {
		t.Fatalf("payment = %s, want reversed synchronously", p.Status)
	}
}

func TestWindowRemaining_RollsOnlyPastTheMargin(t *testing.T) {
	start, window, capAmt, used := big.NewInt(1000), big.NewInt(100), big.NewInt(50), big.NewInt(40)
	at := func(sec int64) *big.Int { return windowRemaining(time.Unix(sec, 0), start, window, capAmt, used) }
	if at(1100).Cmp(big.NewInt(10)) != 0 {
		t.Fatal("exactly at roll-over (clock may be ahead): still the old window")
	}
	if at(1129).Cmp(big.NewInt(10)) != 0 {
		t.Fatal("inside the margin: still the old window")
	}
	if at(1130).Cmp(capAmt) != 0 {
		t.Fatal("past the margin: the window has reset")
	}
}

// Rows the expiry keeps skipping must not starve newer ones.
func TestOrderMcgp_ExpiryPagesPastRowsItCannotResolve(t *testing.T) {
	r := newOrderRig(t)
	orderMcgpScanLimit = 2
	t.Cleanup(func() { orderMcgpScanLimit = 50 })
	r.chain.buy(orderBuyerWallet, mcgpBig(1000))

	var stuck []rigOrder
	for i := 0; i < 2; i++ {
		o := r.pendingOrder(t, 10)
		_, _ = r.fund(o)
		expireEscrow(t, r.db, o.id, true)
		// An amount the expiry cannot split: it is skipped every tick.
		r.db.Exec(`UPDATE order_mcgp_payments SET mcgp_amount = 'bad', updated_at = ? WHERE order_id = ?`, time.Now().Add(-time.Hour), o.id)
		stuck = append(stuck, o)
	}
	fresh := r.pendingOrder(t, 10)
	_, _ = r.fund(fresh)
	expireEscrow(t, r.db, fresh.id, false)

	for i := 0; i < 3; i++ {
		_, _ = r.svc.Reconcile(context.Background())
	}
	if p := r.payment(t, fresh.id); p.Status != models.OrderMcgpReversed {
		t.Fatalf("fresh expiry = %s, want reversed (starved by the stuck page?)", p.Status)
	}
	for _, o := range stuck {
		if p := r.payment(t, o.id); p.Status != models.OrderMcgpHeld {
			t.Fatalf("stuck row = %s", p.Status)
		}
	}
}

func TestOrderMcgp_ExpiredUndeliveredHoldRefundsInFull(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)
	_, _ = r.fund(o)
	expireEscrow(t, r.db, o.id, false)

	if _, err := r.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpReversed {
		t.Fatalf("payment = %s, want reversed", p.Status)
	}
	if got := r.chain.premium(orderBuyerWallet); got.Cmp(mcgpBig(100)) != 0 {
		t.Fatalf("buyer spendable = %s, want 100", got)
	}
	if ord := r.order(t, o.id); ord.Status != models.OrderStatusRefunded {
		t.Fatalf("order = %s, want refunded", ord.Status)
	}
}

func TestOrderMcgp_ExpiryLeavesDisputedOrdersAlone(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)
	_, _ = r.fund(o)
	expireEscrow(t, r.db, o.id, false)
	r.db.Exec(`UPDATE orders SET disputed_at = ? WHERE id = ?`, time.Now(), o.id)

	_, _ = r.svc.Reconcile(context.Background())
	if p := r.payment(t, o.id); p.Status != models.OrderMcgpHeld {
		t.Fatalf("payment = %s, want held (an admin decides disputes)", p.Status)
	}
}

// ── legacy escrow orders ─────────────────────────────────────────────────

// An order funded through ProductEscrow keeps its escrow path: the unpaid
// sweep still reconciles it against the escrow, and nothing here claims it.
func TestOrderMcgp_FundRefusesAnOrderAlreadyInEscrow(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)
	r.db.Exec(`UPDATE orders SET escrow_tx_hash = '0xabc' WHERE id = ?`, o.id)

	if _, err := r.fund(o); !errors.Is(err, ErrOrderNotPayable) {
		t.Fatalf("expected ErrOrderNotPayable, got %v", err)
	}
	if r.chain.debitCalls != 0 {
		t.Fatal("an escrow-funded order must never also be debited")
	}
}

type fakeSpendableEscrow struct {
	held *OnChainOrder
	err  error
}

func (f fakeSpendableEscrow) EscrowedOrder(context.Context, [32]byte) (*OnChainOrder, error) {
	return f.held, f.err
}

// An older app may have paid through ProductEscrow and lost the report: the
// escrow is read before a legacy order is claimed, and a payment found there
// (or an unreadable escrow) stops the Spendable debit.
func TestOrderMcgp_FundChecksTheEscrowForAnUnreportedPayment(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)

	r.svc.SetEscrowReader(fakeSpendableEscrow{held: &OnChainOrder{}})
	if _, err := r.fund(o); !errors.Is(err, ErrOrderNotPayable) {
		t.Fatalf("escrow-funded: %v, want ErrOrderNotPayable", err)
	}
	r.svc.SetEscrowReader(fakeSpendableEscrow{err: errors.New("rpc down")})
	if _, err := r.fund(o); !errors.Is(err, ErrOrderMcgpEscrowUnknown) {
		t.Fatalf("unreadable escrow: %v, want ErrOrderMcgpEscrowUnknown", err)
	}
	if r.chain.debitCalls != 0 {
		t.Fatal("nothing may be debited while the escrow might hold a payment")
	}
	if ord := r.order(t, o.id); ord.PaymentSource != "" {
		t.Fatalf("the order must not be claimed: %q", ord.PaymentSource)
	}
	r.svc.SetEscrowReader(fakeSpendableEscrow{})
	if pay, err := r.fund(o); err != nil || pay.Status != models.OrderMcgpHeld {
		t.Fatalf("clear escrow: %v / %+v", err, pay)
	}
}

// A retry is pinned to the first attempt's wallet: the debit ref is per
// order, so a late first debit lands under that wallet.
func TestOrderMcgp_RetryRefusesAChangedWallet(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)
	r.chain.debitSendErr = revert("Over debit window cap")
	_, _ = r.fund(o)
	r.chain.debitSendErr = nil

	_, err := r.svc.Fund(context.Background(), OrderMcgpFundInput{
		OrderID: o.id, BuyerID: o.buyer, BuyerWallet: orderSellerWallet, Amount: o.total,
	})
	if !errors.Is(err, ErrOrderMcgpWalletChanged) {
		t.Fatalf("got %v, want ErrOrderMcgpWalletChanged", err)
	}
	if p := r.payment(t, o.id); !strings.EqualFold(p.BuyerWallet, orderBuyerWallet) {
		t.Fatalf("pinned wallet overwritten: %s", p.BuyerWallet)
	}
	if pay, err := r.fund(o); err != nil || pay.Status != models.OrderMcgpHeld {
		t.Fatalf("retry with the pinned wallet: %v / %+v", err, pay)
	}
}

// Asking again about an attempt whose outcome was unknown gives an honest
// answer: nothing landed → retry; landed after the order closed → not payable.
func TestOrderMcgp_FundAfterAnUnknownAttemptReportsWhatHappened(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))

	never := r.pendingOrder(t, 40)
	r.chain.debitSendErr = errors.New("send: i/o timeout") // unknown outcome, nothing landed
	if _, err := r.fund(never); !errors.Is(err, ErrOrderMcgpPending) {
		t.Fatalf("first: %v", err)
	}
	r.chain.debitSendErr = nil
	r.age(t, never.id, time.Hour)
	if _, err := r.fund(never); !errors.Is(err, ErrOrderMcgpRetry) {
		t.Fatalf("after abandon: %v, want ErrOrderMcgpRetry", err)
	}
	if pay, err := r.fund(never); err != nil || pay.Status != models.OrderMcgpHeld {
		t.Fatalf("retry: %v / %+v", err, pay)
	}

	closed := r.pendingOrder(t, 40)
	r.chain.debitLostResp = true
	_, _ = r.fund(closed)
	setOrderStatus(t, r.db, closed.id, models.OrderStatusCancelled)
	if _, err := r.fund(closed); !errors.Is(err, ErrOrderNotPayable) {
		t.Fatalf("landed after close: %v, want ErrOrderNotPayable", err)
	}
	if p := r.payment(t, closed.id); p.Status != models.OrderMcgpReversed {
		t.Fatalf("payment = %s, want reversed", p.Status)
	}
	r.chain.invariants(t)
}

func TestOrderMcgp_RefundAgainIsAlreadyDecided(t *testing.T) {
	r := newOrderRig(t)
	r.chain.buy(orderBuyerWallet, mcgpBig(100))
	o := r.pendingOrder(t, 40)
	_, _ = r.fund(o)
	if _, err := r.svc.Refund(context.Background(), o.id, nil); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if _, err := r.svc.Refund(context.Background(), o.id, nil); !errors.Is(err, ErrOrderMcgpAlreadyDecided) {
		t.Fatalf("second refund: %v, want ErrOrderMcgpAlreadyDecided", err)
	}
	if r.chain.reverseCalls != 1 {
		t.Fatalf("reverses = %d", r.chain.reverseCalls)
	}
}
