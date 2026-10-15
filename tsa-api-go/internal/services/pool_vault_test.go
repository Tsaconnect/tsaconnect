package services

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// ─── Fakes ──────────────────────────────────────────────────────────────────

// revertDataErr is a node's refusal of an estimate that reverted: Error(string)
// revert data, as decodeRevertReason reads it.
type revertDataErr struct{ data string }

func (e revertDataErr) Error() string  { return "execution reverted" }
func (e revertDataErr) ErrorCode() int { return 3 }
func (e revertDataErr) ErrorData() any { return e.data }

func vaultRevert(reason string) error {
	strTy, _ := abi.NewType("string", "", nil)
	packed, _ := abi.Arguments{{Type: strTy}}.Pack(reason)
	data := "0x08c379a0" + hex.EncodeToString(packed)
	return fmt.Errorf("estimate gas (would revert): %w", withRevertReason(revertDataErr{data: data}))
}

// fakeVault is TsaLiquidityPool's books in memory (no collateral: the token
// balance, pending withdrawals per LP, outstanding), with the contract's own
// refusals at "estimate" time.
type fakeVault struct {
	mu                              sync.Mutex
	balance, outstanding, principal *big.Int
	pending                         map[string]*big.Int // lower-case LP → pending withdrawal
	pendingErr                      error
	paused                          bool
	used                            map[[32]byte]bool
	decoder                         *LiquidityVault
	caps                            VaultFillCaps
	capsErr                         error
	stateCalls                      int
	pendingDelay                    time.Duration // each PendingWithdrawal read takes this long
	pendingInflight, pendingMaxPar  int
}

func newFakeVault(balance, principal *big.Int) *fakeVault {
	return &fakeVault{balance: balance, outstanding: new(big.Int), principal: principal, pending: map[string]*big.Int{},
		used: map[[32]byte]bool{}, decoder: &LiquidityVault{addr: testVaultAddr},
		caps: VaultFillCaps{MaxFillPerTx: usdt18(10_000), MaxSpreadBps: big.NewInt(1_000), FillWindow: big.NewInt(86_400),
			FillWindowStart: big.NewInt(time.Now().Unix()), FilledInWindow: new(big.Int), FillWindowCap: usdt18(100_000)}}
}

func (v *fakeVault) FillCaps(context.Context) (*VaultFillCaps, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.capsErr != nil {
		return nil, v.capsErr
	}
	c := v.caps
	return &c, nil
}

func (v *fakeVault) Address() string { return testVaultAddr.Hex() }

// totalPendingLocked is Σ pending (v.mu held).
func (v *fakeVault) totalPendingLocked() *big.Int {
	sum := new(big.Int)
	for _, p := range v.pending {
		sum.Add(sum, p)
	}
	return sum
}

func (v *fakeVault) liquidLocked() *big.Int {
	return (&VaultState{Balance: v.balance, TotalPendingWithdrawals: v.totalPendingLocked()}).Liquid()
}

// setPending sets lp's pending withdrawal (the contract's books only: the
// tokens are whatever balance holds).
func (v *fakeVault) setPending(lp string, amount *big.Int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pending[strings.ToLower(lp)] = new(big.Int).Set(amount)
}

func (v *fakeVault) State(context.Context) (*VaultState, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.stateCalls++
	return &VaultState{Block: 1, Balance: new(big.Int).Set(v.balance), TotalPendingWithdrawals: v.totalPendingLocked(),
		Outstanding: new(big.Int).Set(v.outstanding), TotalPrincipal: new(big.Int).Set(v.principal), Paused: v.paused}, nil
}

func (v *fakeVault) PendingWithdrawal(_ context.Context, lp string) (*big.Int, error) {
	v.mu.Lock()
	if v.pendingDelay > 0 {
		v.pendingInflight++
		v.pendingMaxPar = max(v.pendingMaxPar, v.pendingInflight)
		delay := v.pendingDelay
		v.mu.Unlock()
		time.Sleep(delay)
		v.mu.Lock()
		v.pendingInflight--
	}
	defer v.mu.Unlock()
	if v.pendingErr != nil {
		return nil, v.pendingErr
	}
	if p, ok := v.pending[strings.ToLower(lp)]; ok {
		return new(big.Int).Set(p), nil
	}
	return new(big.Int), nil
}

func (v *fakeVault) PreparePayPending(_, lp string) ([]byte, error) {
	return json.Marshal(blockchain.UnsignedTx{To: testVaultAddr.Hex(), Value: "0", ChainID: "97", Data: "paypending:" + strings.ToLower(lp)})
}

// payPending is the contract's payPending(lp): min(pending, balance) to lp.
func (v *fakeVault) payPending(lp string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, ok := v.pending[strings.ToLower(lp)]
	if !ok {
		return
	}
	amount := new(big.Int).Set(p)
	if amount.Cmp(v.balance) > 0 {
		amount.Set(v.balance)
	}
	p.Sub(p, amount)
	v.balance.Sub(v.balance, amount)
}

func (v *fakeVault) Roles(context.Context) (*VaultRoles, error) {
	return &VaultRoles{CollateralProvider: "0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96", Guardian: "0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96"}, nil
}

func (v *fakeVault) FillUsed(_ context.Context, ref [32]byte) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.used[ref], nil
}

func (v *fakeVault) PrepareFill(_, to string, amount, spread *big.Int, ref [32]byte) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	switch {
	case v.paused:
		return nil, vaultRevert("EnforcedPause()")
	case v.used[ref]:
		return nil, vaultRevert("Bad or used ref")
	case amount.Cmp(v.liquidLocked()) > 0:
		return nil, vaultRevert("Insufficient liquidity")
	case new(big.Int).Add(amount, spread).Cmp(v.caps.MaxFillPerTx) > 0:
		return nil, vaultRevert("Bad fill amount")
	case new(big.Int).Mul(spread, big.NewInt(10_000)).Cmp(new(big.Int).Mul(amount, v.caps.MaxSpreadBps)) > 0:
		return nil, vaultRevert("Spread over maximum")
	case spread.Sign() > 0 && v.principal.Sign() == 0:
		return nil, vaultRevert("No LPs to credit")
	}
	data := fmt.Sprintf("fill:%s:%s:%s:%s", hex.EncodeToString(ref[:]), amount, spread, to)
	return json.Marshal(blockchain.UnsignedTx{To: testVaultAddr.Hex(), Value: "0", ChainID: "97", Data: data})
}

func (v *fakeVault) FilledIn(r *types.Receipt, ref [32]byte) *VaultEvent {
	return v.decoder.FilledIn(r, ref)
}

// vaultSender is the release key: it builds inside Send, runs the caller's
// pre-broadcast hook (the key lane's allocation transaction), and "mines" the
// fill at the probe's head unless noMine — reverted when revert is set.
type vaultSender struct {
	vault   *fakeVault
	probe   *vaultProbe
	mu      sync.Mutex
	noMine  bool
	revert  bool
	notSent error // when set, Send refuses before anything is committed
	// beforeSend runs inside Send before the build: the gap between the
	// fill's decision (under the pool lock) and the lane's allocation.
	beforeSend func()
	sends      int
	lastData   string
}

func (s *vaultSender) Address() string { return "0x00000000000000000000000000000000000000a1" }
func (s *vaultSender) SignAndBroadcast(context.Context, []byte) (string, error) {
	return "", errors.New("the release key only signs through Send")
}

func (s *vaultSender) Send(ctx context.Context, _ int64, build func() ([]byte, error)) (string, error) {
	if s.notSent != nil {
		return "", notBroadcast(s.notSent)
	}
	if s.beforeSend != nil {
		s.beforeSend()
	}
	unsigned, err := build()
	if err != nil {
		return "", notBroadcast(err)
	}
	var u blockchain.UnsignedTx
	_ = json.Unmarshal(unsigned, &u)
	hash := crypto.Keccak256Hash([]byte(u.Data + fmt.Sprint(time.Now().UnixNano())))
	if err := emulatePreBroadcast(ctx, u.Nonce, hash.Hex()); err != nil {
		return "", notBroadcast(err)
	}
	s.mu.Lock()
	s.sends++
	s.lastData = u.Data
	noMine, revert := s.noMine, s.revert
	s.mu.Unlock()
	if !noMine && strings.HasPrefix(u.Data, "paypending:") {
		s.vault.payPending(strings.TrimPrefix(u.Data, "paypending:"))
	}
	if noMine || !strings.HasPrefix(u.Data, "fill:") {
		return hash.Hex(), nil
	}
	parts := strings.Split(strings.TrimPrefix(u.Data, "fill:"), ":")
	rawRef, _ := hex.DecodeString(parts[0])
	var ref [32]byte
	copy(ref[:], rawRef)
	amount, _ := new(big.Int).SetString(parts[1], 10)
	spread, _ := new(big.Int).SetString(parts[2], 10)
	to := common.HexToAddress(parts[3])
	r := &types.Receipt{Status: types.ReceiptStatusFailed, BlockNumber: new(big.Int).SetUint64(s.probe.head)}
	if !revert {
		s.vault.mu.Lock()
		s.vault.used[ref] = true
		s.vault.balance.Sub(s.vault.balance, amount)
		s.vault.outstanding.Add(s.vault.outstanding, new(big.Int).Add(amount, spread))
		s.vault.mu.Unlock()
		ev := parsedLiquidityVaultABI.Events["Filled"]
		data, _ := ev.Inputs.NonIndexed().Pack(amount, spread)
		r.Status = types.ReceiptStatusSuccessful
		r.Logs = []*types.Log{{Address: testVaultAddr, Data: data, TxHash: hash,
			Topics: []common.Hash{ev.ID, common.Hash(ref), common.BytesToHash(to.Bytes())}}}
	}
	s.probe.mu.Lock()
	s.probe.receipts[hash] = r
	s.probe.mu.Unlock()
	return hash.Hex(), nil
}

// vaultProbe serves fill receipts at a settable head; everything else goes to
// the pool chain fake.
type vaultProbe struct {
	TreasuryTxProbe
	mu       sync.Mutex
	head     uint64
	receipts map[common.Hash]*types.Receipt
}

func (p *vaultProbe) TransactionReceipt(ctx context.Context, h common.Hash) (*types.Receipt, error) {
	p.mu.Lock()
	r, ok := p.receipts[h]
	p.mu.Unlock()
	if ok {
		return r, nil
	}
	return nil, errors.New("not found")
}

func (p *vaultProbe) BlockNumber(context.Context) (uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.head, nil
}

func (p *vaultProbe) deepen(n uint64) {
	p.mu.Lock()
	p.head += n
	p.mu.Unlock()
}

// ─── Rig ────────────────────────────────────────────────────────────────────

type vaultRig struct {
	*vaultBase
	vault        *fakeVault
	sender       *vaultSender
	probe        *vaultProbe
	saleVerifier *recordingSaleVerifier
}

// newVaultRig is a central-mode ITService with a vault desk: the vault's
// token balance (all liquid) and LP principal in whole USDT (18 dp).
func newVaultRig(t *testing.T, balanceWhole, principalWhole int64) *vaultRig {
	t.Helper()
	return newVaultRigWith(t, balanceWhole, principalWhole, nil)
}

// newVaultRigWith is newVaultRig with the desk's operator and vault swapped by
// swap (nil keeps the lane-less fake sender and the fake vault).
func newVaultRigWith(t *testing.T, balanceWhole, principalWhole int64,
	swap func(*fakeVault) (ChainSender, VaultChain)) *vaultRig {
	t.Helper()
	base := newVaultBase(t)
	if err := base.db.AutoMigrate(&models.LiquidityLedgerEntry{}); err != nil {
		t.Fatalf("ledger table: %v", err)
	}
	sale, treasury, saleVerifier := newPoolSaleWallet(t, usdt18(1_000))
	vault := newFakeVault(usdt18(balanceWhole), usdt18(principalWhole))
	chain := &fakeTxProbe{head: 100}
	probe := &vaultProbe{TreasuryTxProbe: chain, head: 100, receipts: map[common.Hash]*types.Receipt{}}
	sender := &vaultSender{vault: vault, probe: probe}
	var deskSender ChainSender = sender
	var deskVault VaultChain = vault
	if swap != nil {
		deskSender, deskVault = swap(vault)
	}
	nonces := NewTreasuryNonceWatch("bsc", chain, treasury.Address(), 30*time.Second, nil)
	desk, err := NewPoolDesk(PoolDeskConfig{DB: base.db,
		Token:    PoolToken{ChainID: poolTestChain, ChainKey: "bsc", Symbol: "USDT", Address: poolTestUSDT, Decimals: 18},
		Treasury: treasury, Gates: base.gates, Probe: probe, Generation: chain.EndpointGeneration, Nonces: nonces,
		Sender: deskSender, Sale: sale, Vault: deskVault,
		ReleaseCommitments: newLaneCommitments("pool_fills", func(context.Context) (uint64, error) { return 0, nil })})
	if err != nil {
		t.Fatalf("vault desk: %v", err)
	}
	base.desk = desk
	base.it.SetPoolDesk(desk)
	return &vaultRig{vaultBase: base, vault: vault, sender: sender, probe: probe, saleVerifier: saleVerifier}
}

func (r *vaultRig) openFor(t *testing.T, user uuid.UUID, naira string) (*models.ITIntent, error) {
	t.Helper()
	intent, _, err := r.it.OpenPoolBuy(context.Background(), user, r.price(t, naira), testRoutedEVM)
	return intent, err
}

func (r *vaultRig) openPaid(t *testing.T, naira string) *models.ITIntent {
	t.Helper()
	intent, err := r.openFor(t, r.userID, naira)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ok, err := r.it.claimAndPriceStableBuy(context.Background(), intent, "sess_"+intent.ID.String()[:8], intent.AmountMinor, time.Now(), false, PayInCentral)
	if !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	got := r.intent(t, intent.ID)
	return &got
}

func (r *vaultRig) order(t *testing.T, id uuid.UUID) models.PoolOrder {
	t.Helper()
	var o models.PoolOrder
	if err := r.db.First(&o, "intent_id = ? AND generation = 1", id).Error; err != nil {
		t.Fatalf("pool order: %v", err)
	}
	return o
}

func (r *vaultRig) advance(t *testing.T, id uuid.UUID) error {
	t.Helper()
	return r.it.AdvancePoolBuy(context.Background(), id)
}

func (r *vaultRig) legs(t *testing.T, id uuid.UUID) []models.RampLeg {
	t.Helper()
	var legs []models.RampLeg
	r.db.Where("order_id = ?", id).Find(&legs)
	return legs
}

// ─── Open ───────────────────────────────────────────────────────────────────

// Spec §4.2: an open touches nothing on-chain; the order is claimable at once
// and its fill + spread are reserved.
func TestVaultOpen_ReservesWithoutAnOnChainStep(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent, err := r.openFor(t, r.userID, "5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if intent.Status != models.ITStatusPendingPayment || intent.RateExpiresAt == nil {
		t.Fatalf("status = %s expires = %v, want pending_payment with an honour window", intent.Status, intent.RateExpiresAt)
	}
	if r.sender.sends != 0 {
		t.Fatalf("sends = %d at open, want none", r.sender.sends)
	}
	o := r.order(t, intent.ID)
	fill := usdt18Units(t, intent.AssetAmount)
	if o.FillUnits == nil || *o.FillUnits != fill {
		t.Fatalf("fill units = %v, want %s", o.FillUnits, fill)
	}
	f, _ := new(big.Int).SetString(fill, 10)
	if o.SpreadUnits == nil || *o.SpreadUnits != vaultSpreadUnits(f, 50).String() {
		t.Fatalf("spread units = %v, want 0.5%% of the fill", o.SpreadUnits)
	}
	reserved, err := r.desk.vaultReserved(context.Background(), r.db)
	if err != nil || reserved.Fill.Cmp(f) != 0 || reserved.Spread.Cmp(vaultSpreadUnits(f, 50)) != 0 {
		t.Fatalf("reserved = %+v %v, want the fill held back (its spread only counted for the window)", reserved, err)
	}
}

// An order the vault's free liquidity cannot cover is refused at open
// (re-read under the lock, whatever the quote saw).
func TestVaultOpen_RefusedBeyondFreeLiquidity(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	p := r.price(t, "5000") // ~3.6 USDT
	r.vault.mu.Lock()
	r.vault.balance = usdt18(3)
	r.vault.mu.Unlock()
	if _, _, err := r.it.OpenPoolBuy(context.Background(), r.userID, p, testRoutedEVM); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("err = %v, want ErrPoolCannotFill", err)
	}
}

// No collateral: what limits an open is liquid (balance − pending
// withdrawals), and only the fill needs backing — never its spread.
func TestVaultOpen_LimitedByLiquidNotTheSpread(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	p := r.price(t, "5000")
	fill, err := CanonicalToChainUnits(p.Quote.AssetUnits, 18)
	if err != nil {
		t.Fatal(err)
	}
	// Balance holds plenty, but pending withdrawals leave one unit short of
	// the fill: refused (pending withdrawals are paid first).
	r.vault.setPending("0x00000000000000000000000000000000000000c1", new(big.Int).Sub(usdt18(100), new(big.Int).Sub(fill, big.NewInt(1))))
	if _, _, err := r.it.OpenPoolBuy(context.Background(), r.userID, p, testRoutedEVM); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("err = %v, want ErrPoolCannotFill with liquid one unit short", err)
	}
	// Liquid exactly the fill (nothing for the spread): opens.
	r.vault.setPending("0x00000000000000000000000000000000000000c1", new(big.Int).Sub(usdt18(100), fill))
	if _, _, err := r.it.OpenPoolBuy(context.Background(), r.userID, p, testRoutedEVM); err != nil {
		t.Fatalf("open with liquid = fill: %v", err)
	}
}

// Reservations: a second order sees the first one's reserve, not just the
// vault's outstanding.
func TestVaultOpen_SecondOrderSeesTheFirstsReserve(t *testing.T) {
	r := newVaultRig(t, 6, 25) // room for one ~3.6 USDT order, not two
	p := r.price(t, "5000")
	if _, _, err := r.it.OpenPoolBuy(context.Background(), r.userID, p, testRoutedEVM); err != nil {
		t.Fatalf("first open: %v", err)
	}
	other := seedVerifiedUser(t, r.db)
	if _, _, err := r.it.OpenPoolBuy(context.Background(), other, p, testRoutedEVM); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("second open err = %v, want ErrPoolCannotFill", err)
	}
}

// Pricing: one fill — a network line, never a payout line.
func TestVaultQuote_NoPayoutFeeLine(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	p := r.price(t, "5000")
	for _, f := range p.Quote.Fees {
		if f.Kind == FeeKindPayout {
			t.Fatalf("fees = %+v, want no payout line", p.Quote.Fees)
		}
	}
}

func TestVaultQuote_RefusedWhenTheVaultCannotCover(t *testing.T) {
	r := newVaultRig(t, 1, 25)
	if _, err := r.it.QuotePoolBuy(context.Background(), "USDT", "5000", "NGN", poolMid, specPoolSettings()); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("err = %v, want ErrPoolCannotFill", err)
	}
}

// ─── Settle ─────────────────────────────────────────────────────────────────

// The whole path: fill sent with its hash recorded in the allocation's own
// write, proven 5 deep from its Filled event, then one sale leg; the order
// settles on the sale's proof with the LPs' spread in the books.
func TestVaultSettle_FillProveDeliverSettle(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent := r.openPaid(t, "5000")
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance 1: %v", err)
	}
	o := r.order(t, intent.ID)
	if r.sender.sends != 1 || o.FillTxHash == "" || o.FilledAt != nil {
		t.Fatalf("sends=%d hash=%q filled=%v, want one fill recorded and unproven", r.sender.sends, o.FillTxHash, o.FilledAt)
	}
	if n := len(r.legs(t, intent.ID)); n != 0 {
		t.Fatalf("legs = %d before the fill is deep, want none", n)
	}
	_ = r.advance(t, intent.ID) // still shallow: nothing new
	if r.sender.sends != 1 {
		t.Fatalf("sends = %d, a recorded fill is never re-sent", r.sender.sends)
	}
	r.probe.deepen(4) // 5 deep
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance 2: %v", err)
	}
	o = r.order(t, intent.ID)
	if o.FilledAt == nil || o.FillSpreadUnits == nil || *o.FillSpreadUnits != *o.SpreadUnits {
		t.Fatalf("filled=%v spread=%v, want proven with the snapshot's spread", o.FilledAt, o.FillSpreadUnits)
	}
	legs := r.legs(t, intent.ID)
	if len(legs) != 1 || legs[0].Amount != usdt18Units(t, intent.AssetAmount) {
		t.Fatalf("legs = %+v, want one sale of her amount", legs)
	}
	want, _ := new(big.Int).SetString(legs[0].Amount, 10)
	r.saleVerifier.err, r.saleVerifier.credited = nil, want
	r.db.Model(&models.RampLeg{}).Where("id = ?", legs[0].ID).Update("tx_hash", "0xsale")
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance 3: %v", err)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusSettled {
		t.Fatalf("status = %s, want settled", got.Status)
	}
	o = r.order(t, intent.ID)
	if o.SettledAt == nil || o.LPTotalKobo == nil || *o.LPTotalKobo != "0" {
		t.Fatalf("books = settled %v lp %v, want settled with no naira LP payout", o.SettledAt, o.LPTotalKobo)
	}
	if reserved, _ := r.desk.vaultReserved(context.Background(), r.db); reserved.Fill.Sign() != 0 {
		t.Fatalf("reserved = %s after the fill is proven, want 0", reserved.Fill)
	}
	if st, _ := r.vault.State(context.Background()); st.Outstanding.Cmp(new(big.Int).Add(want, bigFromDecimal(o.SpreadUnits))) != 0 ||
		st.Balance.Cmp(new(big.Int).Sub(usdt18(100), want)) != 0 {
		t.Fatalf("vault = balance %s outstanding %s, want the fill paid out and fill + spread owed", st.Balance, st.Outstanding)
	}
}

// With no LP principal the fill carries spread 0 (the contract would refuse a
// spread with nobody to credit); the spread stays with the company.
func TestVaultSettle_NoLPsFillsWithZeroSpread(t *testing.T) {
	r := newVaultRig(t, 100, 0)
	intent := r.openPaid(t, "5000")
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if o := r.order(t, intent.ID); o.FillSpreadUnits == nil || *o.FillSpreadUnits != "0" {
		t.Fatalf("fill spread = %v, want 0", o.FillSpreadUnits)
	}
	if !strings.Contains(r.sender.lastData, ":0:") {
		t.Fatalf("sent %q, want spread 0", r.sender.lastData)
	}
}

// The vault refuses the fill (liquid gone under the reserve — LPs withdrew):
// park pool_fill_refused, keep the reserve; refund is allowed (nothing
// recorded), and retry after a refill fills it.
func TestVaultSettle_RefusedFillParksThenRetries(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent := r.openPaid(t, "5000")
	r.vault.mu.Lock()
	r.vault.balance = usdt18(1)
	r.vault.mu.Unlock()
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got := r.intent(t, intent.ID)
	if got.Status != models.ITStatusBuyAwaitingRate || !strings.HasPrefix(got.FailureReason, models.ITFailurePoolFillRefused) {
		t.Fatalf("status = %s %q, want a pool_fill_refused park", got.Status, got.FailureReason)
	}
	if acts := r.it.poolOrderActions(context.Background(), &got); strings.Join(acts, ",") != "refund,retry-fill" {
		t.Fatalf("actions = %v", acts)
	}
	if _, err := r.it.RetryPoolFill(context.Background(), uuid.New(), intent.ID, "still short"); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("retry while short = %v, want ErrPoolCannotFill", err)
	}
	r.vault.mu.Lock()
	r.vault.balance = usdt18(100)
	r.vault.mu.Unlock()
	if _, err := r.it.RetryPoolFill(context.Background(), uuid.New(), intent.ID, "pool refilled"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance after retry: %v", err)
	}
	if o := r.order(t, intent.ID); o.FillTxHash == "" {
		t.Fatal("no fill after the retry")
	}
}

// A fill that mined and reverted is final at depth: the hash is cleared in the
// park's own write, so the order can be refunded or retried.
func TestVaultSettle_RevertedFillParksAndClearsTheHash(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent := r.openPaid(t, "5000")
	r.sender.revert = true
	_ = r.advance(t, intent.ID)
	r.probe.deepen(4)
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got := r.intent(t, intent.ID)
	if got.Status != models.ITStatusBuyAwaitingRate || !strings.Contains(got.FailureReason, "reverted") {
		t.Fatalf("status = %s %q, want a revert park", got.Status, got.FailureReason)
	}
	if o := r.order(t, intent.ID); o.FillTxHash != "" {
		t.Fatalf("fill hash = %q, want cleared", o.FillTxHash)
	}
	if _, err := r.it.refundPoolBuy(context.Background(), uuid.New(), intent.ID, "refund after revert"); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusRefunded {
		t.Fatalf("status = %s, want refunded", got.Status)
	}
}

// Never refund naira for an order whose fill is recorded: the USDT may already
// have left the vault.
func TestVaultRefund_RefusedOnceAFillIsRecorded(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent := r.openPaid(t, "5000")
	r.sender.noMine = true
	_ = r.advance(t, intent.ID)
	r.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Updates(map[string]any{
		"status": models.ITStatusBuyAwaitingRate, "failure_reason": "test"})
	if _, err := r.it.refundPoolBuy(context.Background(), uuid.New(), intent.ID, "try"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want ErrITBadState", err)
	}
}

// An RPC failure at build is a transient, never a park.
func TestVaultSettle_TransientBuildErrorIsNotAPark(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent := r.openPaid(t, "5000")
	r.sender.notSent = errors.New("dial tcp: i/o timeout")
	_ = r.advance(t, intent.ID)
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusDelivering {
		t.Fatalf("status = %s, want still delivering", got.Status)
	}
	r.sender.notSent = nil
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if o := r.order(t, intent.ID); o.FillTxHash == "" {
		t.Fatal("no fill once the RPC is back")
	}
}

// A receipt that does not carry this order's Filled event never proves it.
func TestVaultSettle_ProofNeedsThisOrdersFilledEvent(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent := r.openPaid(t, "5000")
	_ = r.advance(t, intent.ID)
	o := r.order(t, intent.ID)
	r.probe.mu.Lock()
	r.probe.receipts[common.HexToHash(o.FillTxHash)].Logs = nil
	r.probe.mu.Unlock()
	r.probe.deepen(4)
	if err := r.advance(t, intent.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("err = %v, want the mismatch refusal", err)
	}
	if o := r.order(t, intent.ID); o.FilledAt != nil {
		t.Fatal("proven without its event")
	}
}

// Claim: liquid gone between open and payment (LP withdrawals) parks the
// order pool_fill_refused at the claim; its reserve (the snapshot) stays, so
// a retry after a refill fills what she is owed.
func TestVaultClaim_ParksWhenTheVaultShrankSinceOpen(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent, err := r.openFor(t, r.userID, "5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	r.vault.setPending("0x00000000000000000000000000000000000000c1", usdt18(99)) // liquid 1
	ok, err := r.it.claimAndPriceStableBuy(context.Background(), intent, "sess_"+intent.ID.String()[:8], intent.AmountMinor, time.Now(), false, PayInCentral)
	if !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	got := r.intent(t, intent.ID)
	if got.Status != models.ITStatusBuyAwaitingRate || !strings.HasPrefix(got.FailureReason, models.ITFailurePoolFillRefused) {
		t.Fatalf("status = %s %q, want a pool_fill_refused park", got.Status, got.FailureReason)
	}
	if o := r.order(t, intent.ID); o.FillUnits == nil || *o.FillUnits != usdt18Units(t, intent.AssetAmount) {
		t.Fatalf("fill units = %v, want her owed amount kept", o.FillUnits)
	}
	if reserved, _ := r.desk.vaultReserved(context.Background(), r.db); reserved.Fill.Sign() == 0 {
		t.Fatal("the parked order's reserve was released")
	}
}

// Amount integrity: money that arrives late is re-priced at the live spread,
// and the LPs are credited that spread, not the open-time snapshot's.
func TestVaultClaim_RepricedArrivalCreditsTheLiveSpread(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	r.it.SetTreasuryMarketRate(&fixedTreasuryRate{rate: poolMid})
	intent, err := r.openFor(t, r.userID, "5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	r.db.Model(&models.PoolOrder{}).Where("intent_id = ?", intent.ID).Update("pool_spread_bps", 999) // stale snapshot
	late := time.Now().Add(time.Hour)
	r.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("rate_expires_at", time.Now().Add(-2*time.Hour))
	got := r.intent(t, intent.ID)
	ok, err := r.it.claimAndPriceStableBuy(context.Background(), &got, "sess_late", got.AmountMinor, late, false, PayInCentral)
	if !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	o := r.order(t, intent.ID)
	fill, _ := new(big.Int).SetString(*o.FillUnits, 10)
	live := CurrentPoolSettings().SpreadBps
	if o.PoolSpreadBps != int(live) || *o.SpreadUnits != vaultSpreadUnits(fill, live).String() {
		t.Fatalf("spread bps=%d units=%s, want the live %d bps", o.PoolSpreadBps, *o.SpreadUnits, live)
	}
}

// The console: reserved is what open orders' fills hold back of liquid, and
// free = liquid − reserved (pending withdrawals are outside liquid).
func TestVaultAdmin_SummaryShowsReserve(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent, err := r.openFor(t, r.userID, "5000")
	if err != nil {
		t.Fatal(err)
	}
	r.vault.setPending("0x00000000000000000000000000000000000000c1", usdt18(10))
	r.vault.mu.Lock()
	r.vault.outstanding = usdt18(7)
	r.vault.mu.Unlock()
	sum, err := r.it.VaultPoolSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	o := r.order(t, intent.ID)
	reserved := bigFromDecimal(o.FillUnits)
	free := new(big.Int).Sub(usdt18(90), reserved)
	if sum.Reserved != BaseUnitsToDecimal(reserved, 18) || sum.Free != BaseUnitsToDecimal(free, 18) || sum.Liquid != "90" ||
		sum.TotalPendingWithdrawals != "10" || sum.Outstanding != "7" || sum.TotalPrincipal != "25" {
		t.Fatalf("summary = %+v", sum)
	}
	if sum.Utilisation != "20.00" { // 7 ÷ (25 + 10)
		t.Fatalf("utilisation = %s, want outstanding ÷ (principal + pending) = 20.00", sum.Utilisation)
	}
}

// The console names who signs: the vault's roles, and the operator set
// replayed from the ledger (latest OperatorSet per account wins).
func TestVaultAdmin_SummaryNamesRolesAndOperators(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	server := r.sender.Address()
	other := "0x00000000000000000000000000000000000000b2"
	yes, no := true, false
	for i, e := range []struct {
		account string
		allowed *bool
	}{{other, &yes}, {server, &yes}, {other, &no}} {
		if err := r.db.Create(&models.LiquidityLedgerEntry{ID: uuid.New(), ChainID: poolTestChain, Vault: testVaultAddr.Hex(),
			TxHash: fmt.Sprintf("0x%064d", i), LogIndex: uint(i), Block: uint64(i + 1), Kind: VaultEventOperatorSet,
			Account: e.account, Allowed: e.allowed, BlockTime: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	sum, err := r.it.VaultPoolSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Operators) != 1 || !SameEVMAddress(sum.Operators[0], server) || !sum.ServerOperatorActive {
		t.Fatalf("operators = %v active=%v, want only the server operator", sum.Operators, sum.ServerOperatorActive)
	}
	if sum.CollateralProvider == "" || sum.Guardian == "" || sum.Decimals != 18 || sum.TokenAddress == "" {
		t.Fatalf("roles/token missing: %+v", sum)
	}
}

// ─── Review fixes (2026-10-14) ──────────────────────────────────────────────

// laneVault is the fake vault building real calldata, so a real KeyLane can
// sign and broadcast its calls.
type laneVault struct{ *fakeVault }

func laneCall(method string, args ...any) ([]byte, error) {
	data, err := parsedLiquidityVaultABI.Pack(method, args...)
	if err != nil {
		return nil, err
	}
	return json.Marshal(blockchain.UnsignedTx{GasPrice: "1000000000", GasLimit: vaultFillGas, To: testVaultAddr.Hex(),
		Value: "0", Data: common.Bytes2Hex(data), ChainID: big.NewInt(poolTestChain).String()})
}

func (v laneVault) PrepareFill(from, to string, amount, spread *big.Int, ref [32]byte) ([]byte, error) {
	if _, err := v.fakeVault.PrepareFill(from, to, amount, spread, ref); err != nil {
		return nil, err
	}
	return laneCall("fill", common.HexToAddress(to), amount, spread, ref)
}

func (v laneVault) PreparePayPending(_, lp string) ([]byte, error) {
	return laneCall("payPending", common.HexToAddress(lp))
}

// newLaneVaultRig is the vault rig whose operator is a real ChainReleaseSigner
// (in-memory KeyLane store) broadcasting to node.
func newLaneVaultRig(t *testing.T, balanceWhole, principalWhole int64) (*vaultRig, *chainNode, *KeyLane) {
	t.Helper()
	node := newChainNode(poolTestChain)
	node.head = 100
	signer := NewChainReleaseSigner(NewSignerRegistry(), newEcdsaKey().k, map[int64]ReleaseBroadcaster{poolTestChain: node})
	r := newVaultRigWith(t, balanceWhole, principalWhole, func(v *fakeVault) (ChainSender, VaultChain) {
		return signer, laneVault{v}
	})
	return r, node, signer.Lane(poolTestChain)
}

// H1: a fill that MINED and REVERTED holds an executed allocation, which the
// give-up rule never releases — the order used to stay delivering forever.
// The lane's own verdict (reverted at settle depth) parks it, and the retry
// takes a new allocation once the watch has freed the operation key.
func TestVaultSettle_MinedRevertedFillParksOnARealLane(t *testing.T) {
	r, node, lane := newLaneVaultRig(t, 100, 25)
	ctx := context.Background()
	intent := r.openPaid(t, "5000")
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance 1: %v", err)
	}
	o := r.order(t, intent.ID)
	if o.FillTxHash == "" {
		t.Fatal("no fill recorded by the lane's pre-broadcast hook")
	}
	h := common.HexToHash(o.FillTxHash)
	node.mu.Lock()
	node.reverted = map[common.Hash]bool{h: true}
	node.mined = 1
	node.mu.Unlock()
	node.mine(h, 101, 110)
	r.probe.mu.Lock()
	r.probe.receipts[h] = &types.Receipt{Status: types.ReceiptStatusFailed, BlockNumber: big.NewInt(101), TxHash: h}
	r.probe.head = 110
	r.probe.mu.Unlock()
	if err := lane.Watch(ctx); err != nil {
		t.Fatalf("watch: %v", err)
	}
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance 2: %v", err)
	}
	got := r.intent(t, intent.ID)
	if got.Status != models.ITStatusBuyAwaitingRate || !strings.Contains(got.FailureReason, "reverted") {
		t.Fatalf("status = %s %q, want a pool_fill_refused revert park", got.Status, got.FailureReason)
	}
	if o := r.order(t, intent.ID); o.FillTxHash != "" {
		t.Fatalf("fill hash = %q, want cleared", o.FillTxHash)
	}
	if _, err := r.it.RetryPoolFill(ctx, uuid.New(), intent.ID, "vault fixed"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance after retry: %v", err)
	}
	o = r.order(t, intent.ID)
	if o.FillTxHash == "" || common.HexToHash(o.FillTxHash) == h || len(node.atNonce(1)) != 1 {
		t.Fatalf("retry fill = %q (nonce 1 txs %d), want a new call at a new nonce", o.FillTxHash, len(node.atNonce(1)))
	}
}

// H2: a re-priced claim that parks keeps the snapshot equal to the intent's
// (open-time) amount; the retry prices it again on what she paid, so the fill
// is exactly what the sale desk then delivers.
func TestVaultClaim_RepricedParkRetryFillsWhatIsDelivered(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	r.it.SetTreasuryMarketRate(&fixedTreasuryRate{rate: poolMid})
	intent, err := r.openFor(t, r.userID, "5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	opened := intent.AssetAmount
	r.vault.mu.Lock()
	r.vault.balance = usdt18(1)
	r.vault.mu.Unlock()
	expected, _ := new(big.Int).SetString(intent.AmountMinor, 10)
	paid := new(big.Int).Quo(new(big.Int).Mul(expected, big.NewInt(6)), big.NewInt(5)) // paid 20% more: priced live
	if err := r.db.Exec(`INSERT INTO inbound_credits (id, provider, provider_ref, currency, amount_kobo, gross_kobo, sender_raw, transaction_date, seen_at)
		VALUES (?, 'flutterwave', 'sess_more', 'NGN', ?, ?, 'BUYER', ?, ?)`, uuid.NewString(), paid.Int64(), paid.Int64(), time.Now(), time.Now()).Error; err != nil {
		t.Fatal(err) // she paid through the central account
	}
	ok, err := r.it.claimAndPriceStableBuy(context.Background(), intent, "sess_more", paid.String(), time.Now(), false, PayInCentral)
	if !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	got := r.intent(t, intent.ID)
	if got.Status != models.ITStatusBuyAwaitingRate || !strings.HasSuffix(got.FailureReason, poolFillRepriceMark) {
		t.Fatalf("status = %s %q, want a re-priced pool_fill_refused park", got.Status, got.FailureReason)
	}
	if o := r.order(t, intent.ID); *o.FillUnits != usdt18Units(t, got.AssetAmount) {
		t.Fatalf("parked snapshot fill = %s, intent owes %s: they must agree", *o.FillUnits, usdt18Units(t, got.AssetAmount))
	}
	r.vault.mu.Lock()
	r.vault.balance = usdt18(100)
	r.vault.mu.Unlock()
	if _, err := r.it.RetryPoolFill(context.Background(), uuid.New(), intent.ID, "pool refilled"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	got = r.intent(t, intent.ID)
	o, _ := new(big.Int).SetString(opened, 10)
	now, _ := new(big.Int).SetString(got.AssetAmount, 10)
	if !got.Repriced || now.Cmp(o) <= 0 {
		t.Fatalf("retry asset = %s (repriced %v), want more than the opened %s", got.AssetAmount, got.Repriced, opened)
	}
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance 1: %v", err)
	}
	r.probe.deepen(4)
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance 2: %v", err)
	}
	ord := r.order(t, intent.ID)
	legs := r.legs(t, intent.ID)
	if ord.FilledAt == nil || len(legs) != 1 {
		t.Fatalf("filled=%v legs=%d, want proven and one sale", ord.FilledAt, len(legs))
	}
	if legs[0].Amount != *ord.FillUnits || legs[0].Amount != usdt18Units(t, got.AssetAmount) {
		t.Fatalf("filled %s, delivering %s, owed %s: all three must agree", *ord.FillUnits, legs[0].Amount, usdt18Units(t, got.AssetAmount))
	}
}

// H2 guard: a fill that disagrees with the intent's amount is never sent; the
// order parks for a retry, which re-reserves it from the intent.
func TestVaultSettle_FillAmountMismatchParksUnsent(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent := r.openPaid(t, "5000")
	r.db.Model(&models.PoolOrder{}).Where("intent_id = ?", intent.ID).Update("fill_units", usdt18(2).String())
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got := r.intent(t, intent.ID)
	if r.sender.sends != 0 || got.Status != models.ITStatusBuyAwaitingRate || !strings.Contains(got.FailureReason, "does not match") {
		t.Fatalf("sends=%d status=%s %q, want an unsent mismatch park", r.sender.sends, got.Status, got.FailureReason)
	}
	if _, err := r.it.RetryPoolFill(context.Background(), uuid.New(), intent.ID, "re-reserve"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if o := r.order(t, intent.ID); *o.FillUnits != usdt18Units(t, got.AssetAmount) {
		t.Fatalf("fill after retry = %s, want the intent's %s", *o.FillUnits, usdt18Units(t, got.AssetAmount))
	}
	if err := r.advance(t, intent.ID); err != nil || r.sender.sends != 1 {
		t.Fatalf("advance after retry: %v sends=%d", err, r.sender.sends)
	}
}

// H3: an unpaid order past the claim window can never be claimed, so it stops
// reserving liquidity (and stops counting as owed gas); a paid one reserves
// whatever its age.
func TestVaultReserve_AbandonedUnpaidOrderStopsReserving(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	ctx := context.Background()
	unpaid, err := r.openFor(t, r.userID, "5000")
	if err != nil {
		t.Fatal(err)
	}
	other := seedVerifiedUser(t, r.db)
	pi, err := r.openFor(t, other, "5000")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := r.it.claimAndPriceStableBuy(ctx, pi, "sess_old", pi.AmountMinor, time.Now(), false, PayInCentral); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	paid := r.intent(t, pi.ID)
	old := time.Now().Add(-stableBuyClaimWindow - time.Hour)
	r.db.Model(&models.ITIntent{}).Where("id IN ?", []uuid.UUID{unpaid.ID, paid.ID}).Update("created_at", old)
	o := r.order(t, paid.ID)
	want := bigFromDecimal(o.FillUnits)
	if reserved, err := r.desk.vaultReserved(ctx, r.db); err != nil || reserved.Fill.Cmp(want) != 0 {
		t.Fatalf("reserved = %v %v, want only the paid order's %s", reserved.Fill, err, want)
	}
	if n, err := NewPoolFillCommitments(r.db).UnitsFresh(ctx); err != nil || n != vaultFillGas {
		t.Fatalf("fill commitments = %d %v, want one fill (the paid order)", n, err)
	}
	if n, err := NewPoolTreasuryCommitments(r.db).UnitsFresh(ctx); err != nil || n != treasuryERC20Gas {
		t.Fatalf("treasury commitments = %d %v, want one delivery (the paid order)", n, err)
	}
	r.db.Model(&models.ITIntent{}).Where("id = ?", unpaid.ID).Update("created_at", time.Now())
	if reserved, _ := r.desk.vaultReserved(ctx, r.db); reserved.Fill.Cmp(want) <= 0 {
		t.Fatalf("reserved = %s, a claimable unpaid order must reserve too", reserved.Fill)
	}
}

// L6: an open the contract's own fill limits would refuse is refused at open.
func TestVaultOpen_RefusedByTheContractsFillCaps(t *testing.T) {
	for name, set := range map[string]func(c *VaultFillCaps){
		"per-tx":     func(c *VaultFillCaps) { c.MaxFillPerTx = usdt18(1) },
		"spread":     func(c *VaultFillCaps) { c.MaxSpreadBps = big.NewInt(10) },
		"window":     func(c *VaultFillCaps) { c.FilledInWindow = usdt18(99_999) },
		"unreadable": nil,
	} {
		t.Run(name, func(t *testing.T) {
			r := newVaultRig(t, 100, 25)
			r.vault.mu.Lock()
			if set == nil {
				r.vault.capsErr = errors.New("rpc down")
			} else {
				set(&r.vault.caps)
			}
			r.vault.mu.Unlock()
			if _, err := r.openFor(t, r.userID, "5000"); !errors.Is(err, ErrPoolCannotFill) {
				t.Fatalf("err = %v, want ErrPoolCannotFill", err)
			}
		})
	}
	// A window that has ended is rolled by the next fill: not counted.
	r := newVaultRig(t, 100, 25)
	r.vault.caps.FilledInWindow = usdt18(99_999)
	r.vault.caps.FillWindowStart = big.NewInt(time.Now().Add(-48 * time.Hour).Unix())
	if _, err := r.openFor(t, r.userID, "5000"); err != nil {
		t.Fatalf("open after the window ended: %v", err)
	}
}

// L6: the window counts every reserved, unproven order, not just the chain's
// filled amount.
func TestVaultOpen_WindowCapCountsReservedOrders(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	first, err := r.openFor(t, r.userID, "5000")
	if err != nil {
		t.Fatal(err)
	}
	o := r.order(t, first.ID)
	one := new(big.Int).Add(bigFromDecimal(o.FillUnits), bigFromDecimal(o.SpreadUnits))
	r.vault.caps.FillWindowCap = new(big.Int).Add(one, new(big.Int).Quo(one, big.NewInt(2))) // room for 1.5 orders
	other := seedVerifiedUser(t, r.db)
	if _, err := r.openFor(t, other, "5000"); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("second open err = %v, want the window cap refusal", err)
	}
}

// L7: a fill unproven past the stall window is reported as stalled, so the
// settle job's sustained release-stalled alert fires.
func TestVaultSettle_UnprovenFillReportsStalled(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent := r.openPaid(t, "5000")
	r.sender.noMine = true
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("fresh fill reported %v, want nothing yet", err)
	}
	r.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).UpdateColumn("updated_at", time.Now().Add(-poolFillStallAfter-time.Minute))
	if err := r.advance(t, intent.ID); !errors.Is(err, ErrPoolReleaseStalled) {
		t.Fatalf("err = %v, want ErrPoolReleaseStalled", err)
	}
}

// ─── Review fixes (2026-10-14, round 2) ─────────────────────────────────────

// Money race: the fill is decided under the pool lock, which is released
// before the lane signs. Another tick parks the order and an operator refunds
// it in that gap (no hash yet); the pre-broadcast hook must then refuse, so
// nothing is signed and she is never both refunded and filled.
func TestVaultSettle_ParkAndRefundBeforeTheHookSendsNothing(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent := r.openPaid(t, "5000")
	var refundErr error
	r.sender.beforeSend = func() {
		r.sender.beforeSend = nil
		if err := r.it.parkVaultFill(context.Background(), r.desk, intent.ID, "", "parked by another tick"); err != nil {
			refundErr = err
			return
		}
		_, refundErr = r.it.refundPoolBuy(context.Background(), uuid.New(), intent.ID, "refunded in the gap")
	}
	_ = r.advance(t, intent.ID)
	if refundErr != nil {
		t.Fatalf("park+refund in the gap: %v", refundErr)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusRefunded {
		t.Fatalf("status = %s, want refunded", got.Status)
	}
	if o := r.order(t, intent.ID); o.FillTxHash != "" || r.sender.sends != 0 {
		t.Fatalf("fill hash = %q sends = %d, want nothing sent for a refunded order", o.FillTxHash, r.sender.sends)
	}
}

// Claim runs the contract's fill limits too: an amount over maxFillPerTx would
// only revert at estimate, so it parks at once with the reason, snapshot kept.
func TestVaultClaim_OverThePerFillLimitParksAtClaim(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent, err := r.openFor(t, r.userID, "5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	r.vault.mu.Lock()
	r.vault.caps.MaxFillPerTx = usdt18(1)
	r.vault.mu.Unlock()
	ok, err := r.it.claimAndPriceStableBuy(context.Background(), intent, "sess_"+intent.ID.String()[:8], intent.AmountMinor, time.Now(), false, PayInCentral)
	if !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	got := r.intent(t, intent.ID)
	if got.Status != models.ITStatusBuyAwaitingRate || got.FailureReason != models.ITFailurePoolFillRefused+": "+fillRefusedPerTx {
		t.Fatalf("status = %s %q, want a per-fill-limit park", got.Status, got.FailureReason)
	}
	if o := r.order(t, intent.ID); *o.FillUnits != usdt18Units(t, got.AssetAmount) {
		t.Fatalf("parked snapshot fill = %s, want the intent's %s", *o.FillUnits, usdt18Units(t, got.AssetAmount))
	}
	if err := r.advance(t, intent.ID); err != nil || r.sender.sends != 0 {
		t.Fatalf("advance: %v sends=%d, want nothing sent", err, r.sender.sends)
	}
}

// Retry refuses (stays parked) an amount the contract's fill limits refuse,
// instead of estimating it into another park; its own reservation is not
// counted twice against the window.
func TestVaultRetry_RefusedOverTheContractsFillCaps(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent := r.openPaid(t, "5000")
	r.vault.mu.Lock()
	r.vault.balance = usdt18(1)
	r.vault.mu.Unlock()
	if err := r.advance(t, intent.ID); err != nil {
		t.Fatalf("advance: %v", err)
	}
	r.vault.mu.Lock()
	r.vault.balance = usdt18(100)
	r.vault.caps.MaxFillPerTx = usdt18(1)
	r.vault.mu.Unlock()
	_, err := r.it.RetryPoolFill(context.Background(), uuid.New(), intent.ID, "pool refilled")
	if !errors.Is(err, ErrPoolCannotFill) || !strings.Contains(err.Error(), fillRefusedPerTx) {
		t.Fatalf("retry err = %v, want the per-fill-limit refusal", err)
	}
	if got := r.intent(t, intent.ID); got.Status != models.ITStatusBuyAwaitingRate {
		t.Fatalf("status = %s, want still parked", got.Status)
	}
	// A window with room for exactly this order: its own reserve is not
	// counted against itself.
	o := r.order(t, intent.ID)
	r.vault.mu.Lock()
	r.vault.caps.MaxFillPerTx = usdt18(10_000)
	r.vault.caps.FillWindowCap = new(big.Int).Add(bigFromDecimal(o.FillUnits), bigFromDecimal(o.SpreadUnits))
	r.vault.mu.Unlock()
	if _, err := r.it.RetryPoolFill(context.Background(), uuid.New(), intent.ID, "limit raised"); err != nil {
		t.Fatalf("retry within the window: %v", err)
	}
	if err := r.advance(t, intent.ID); err != nil || r.sender.sends != 1 {
		t.Fatalf("advance after retry: %v sends=%d", err, r.sender.sends)
	}
}

// The console's 30-day spread is this vault's: another vault on the same
// chain (a redeploy) never counts.
func TestVaultAdmin_Spread30dIsThisVaults(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	if err := r.db.AutoMigrate(&models.LiquidityIndexCursor{}); err != nil {
		t.Fatal(err)
	}
	books := &fakeBooks{LiquidityVault: &LiquidityVault{addr: testVaultAddr},
		state: VaultState{Balance: new(big.Int), TotalPendingWithdrawals: new(big.Int), Outstanding: new(big.Int), TotalPrincipal: new(big.Int)}}
	x, err := NewVaultIndexer(VaultIndexerConfig{DB: r.db, ChainID: poolTestChain, Vault: books, Reader: &fakeLogReader{head: 1}, DeployBlock: 100})
	if err != nil {
		t.Fatal(err)
	}
	r.it.SetVaultLedger(x)
	other := "0x00000000000000000000000000000000000000ee"
	for i, v := range []string{strings.ToLower(testVaultAddr.Hex()), other} {
		spread := usdt18(int64(i + 1)).String() // ours 1, the other vault's 2
		if err := r.db.Create(&models.LiquidityLedgerEntry{ID: uuid.New(), ChainID: poolTestChain, Vault: v,
			TxHash: fmt.Sprintf("0x%064x", i+1), Block: 101, Kind: VaultEventFilled, Spread: &spread, BlockTime: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	sum, err := r.it.VaultPoolSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sum.Spread30d != "1" {
		t.Fatalf("spread30d = %q, want this vault's 1", sum.Spread30d)
	}
}

// Testnet rehearsal 2026-10-15: the free-liquidity read is cached for 5 s, so
// a quote right after a pause was still served. A closed gate refuses at once.
func TestVaultQuote_RefusedWhileGatesClosed(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	if _, err := r.it.QuotePoolBuy(context.Background(), "USDT", "5000", "NGN", poolMid, specPoolSettings()); err != nil {
		t.Fatalf("open gates: %v", err)
	}
	r.gates.Close("vault: the vault is paused")
	if _, err := r.it.QuotePoolBuy(context.Background(), "USDT", "5000", "NGN", poolMid, specPoolSettings()); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("closed gates: %v, want ErrPoolClosed", err)
	}
}
