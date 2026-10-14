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

// fakeVault is TsaLiquidityPool's books in memory, with the contract's own
// refusals at "estimate" time.
type fakeVault struct {
	mu                                 sync.Mutex
	collateral, outstanding, principal *big.Int
	paused                             bool
	used                               map[[32]byte]bool
	decoder                            *LiquidityVault
}

func newFakeVault(collateral, principal *big.Int) *fakeVault {
	return &fakeVault{collateral: collateral, outstanding: new(big.Int), principal: principal,
		used: map[[32]byte]bool{}, decoder: &LiquidityVault{addr: testVaultAddr}}
}

func (v *fakeVault) Address() string { return testVaultAddr.Hex() }

func (v *fakeVault) State(context.Context) (*VaultState, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return &VaultState{Block: 1, Collateral: new(big.Int).Set(v.collateral), Outstanding: new(big.Int).Set(v.outstanding),
		TotalPrincipal: new(big.Int).Set(v.principal), Paused: v.paused}, nil
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
	case new(big.Int).Add(new(big.Int).Add(v.outstanding, amount), spread).Cmp(v.collateral) > 0:
		return nil, vaultRevert("Not covered by collateral")
	case spread.Sign() > 0 && v.principal.Sign() == 0:
		return nil, vaultRevert("No LPs to credit")
	}
	data := fmt.Sprintf("fill:%s:%s:%s:%s", hex.EncodeToString(ref[:]), amount, spread, to)
	return json.Marshal(blockchain.UnsignedTx{To: testVaultAddr.Hex(), Value: "0", ChainID: "97", Data: data})
}

func (v *fakeVault) FilledIn(r *types.Receipt, ref [32]byte) *VaultEvent {
	return v.decoder.FilledIn(r, ref)
}

func (v *fakeVault) PrepareSettleFromCollateral(_ string, amount *big.Int, ref [32]byte) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if amount.Cmp(v.outstanding) > 0 {
		return nil, vaultRevert("Bad amount")
	}
	return json.Marshal(blockchain.UnsignedTx{To: testVaultAddr.Hex(), Value: "0", ChainID: "97",
		Data: fmt.Sprintf("settle:%s:%s", hex.EncodeToString(ref[:]), amount)})
}

// vaultSender is the release key: it builds inside Send, runs the caller's
// pre-broadcast hook (the key lane's allocation transaction), and "mines" the
// fill at the probe's head unless noMine — reverted when revert is set.
type vaultSender struct {
	vault    *fakeVault
	probe    *vaultProbe
	mu       sync.Mutex
	noMine   bool
	revert   bool
	notSent  error // when set, Send refuses before anything is committed
	sends    int
	lastData string
}

func (s *vaultSender) Address() string { return "0x00000000000000000000000000000000000000a1" }
func (s *vaultSender) SignAndBroadcast(context.Context, []byte) (string, error) {
	return "", errors.New("the release key only signs through Send")
}

func (s *vaultSender) Send(ctx context.Context, _ int64, build func() ([]byte, error)) (string, error) {
	if s.notSent != nil {
		return "", notBroadcast(s.notSent)
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
	*poolOpenRig
	vault        *fakeVault
	sender       *vaultSender
	probe        *vaultProbe
	saleVerifier *recordingSaleVerifier
}

// newVaultRig is a central-mode ITService with a vault desk: collateral and LP
// principal in whole USDT (18 dp).
func newVaultRig(t *testing.T, collateralWhole, principalWhole int64) *vaultRig {
	t.Helper()
	base := newPoolOpenRig(t)
	if err := base.db.AutoMigrate(&models.RampLeg{}, &models.JobRunMarker{}); err != nil {
		t.Fatal(err)
	}
	if err := base.db.Exec(models.RampLegsOneLiveSaleIndexSQL).Error; err != nil {
		t.Fatal(err)
	}
	sale, _, saleVerifier, _ := newPoolSaleWallet(t, usdt18(1_000))
	vault := newFakeVault(usdt18(collateralWhole), usdt18(principalWhole))
	probe := &vaultProbe{TreasuryTxProbe: base.chain, head: 100, receipts: map[common.Hash]*types.Receipt{}}
	sender := &vaultSender{vault: vault, probe: probe}
	desk, err := NewPoolDesk(PoolDeskConfig{DB: base.db, Token: base.desk.c.Token, Treasury: base.desk.c.Treasury,
		Gates: base.gates, Probe: probe, Generation: base.chain.EndpointGeneration, Nonces: base.desk.c.Nonces,
		Sender: sender, Verifier: &settleVerifier{credited: map[string]*big.Int{}}, Sale: sale, Vault: vault})
	if err != nil {
		t.Fatalf("vault desk: %v", err)
	}
	base.desk = desk
	base.it.SetPoolDesk(desk)
	return &vaultRig{poolOpenRig: base, vault: vault, sender: sender, probe: probe, saleVerifier: saleVerifier}
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
	if err != nil || reserved.Cmp(new(big.Int).Add(f, vaultSpreadUnits(f, 50))) != 0 {
		t.Fatalf("reserved = %v %v", reserved, err)
	}
}

// D3 off-chain: an order the free collateral cannot cover is refused at open
// (re-read under the lock, whatever the quote saw).
func TestVaultOpen_RefusedBeyondFreeCollateral(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	p := r.price(t, "5000") // ~3.6 USDT
	r.vault.mu.Lock()
	r.vault.collateral = usdt18(3)
	r.vault.mu.Unlock()
	if _, _, err := r.it.OpenPoolBuy(context.Background(), r.userID, p, testRoutedEVM); !errors.Is(err, ErrPoolCannotFill) {
		t.Fatalf("err = %v, want ErrPoolCannotFill", err)
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
	if len(p.SliceUnits) != 1 || p.SliceUnits[0].Cmp(p.Quote.AssetUnits) < 0 {
		t.Fatalf("slices = %v, want one fill covering %s", p.SliceUnits, p.Quote.AssetUnits)
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
	if reserved, _ := r.desk.vaultReserved(context.Background(), r.db); reserved.Sign() != 0 {
		t.Fatalf("reserved = %s after the fill is proven, want 0", reserved)
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

// The vault refuses the fill (collateral withdrawn under the reserve): park
// pool_fill_refused, keep the reserve; refund is allowed (nothing recorded),
// and retry after collateral is posted fills it.
func TestVaultSettle_RefusedFillParksThenRetries(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent := r.openPaid(t, "5000")
	r.vault.mu.Lock()
	r.vault.collateral = usdt18(1)
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
	r.vault.mu.Lock()
	r.vault.collateral = usdt18(100)
	r.vault.mu.Unlock()
	if _, err := r.it.RetryPoolFill(context.Background(), uuid.New(), intent.ID, "collateral posted"); err != nil {
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

// Claim: collateral withdrawn between open and payment parks the order
// pool_fill_refused at the claim; its reserve (the snapshot) stays, so a retry
// after collateral is posted fills what she is owed.
func TestVaultClaim_ParksWhenTheVaultShrankSinceOpen(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent, err := r.openFor(t, r.userID, "5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	r.vault.mu.Lock()
	r.vault.collateral = usdt18(1)
	r.vault.mu.Unlock()
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
	if reserved, _ := r.desk.vaultReserved(context.Background(), r.db); reserved.Sign() == 0 {
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

// The console: reserved is what open orders hold back, and free never counts it.
func TestVaultAdmin_SummaryShowsReserve(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	intent, err := r.openFor(t, r.userID, "5000")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := r.it.VaultPoolSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	o := r.order(t, intent.ID)
	reserved := new(big.Int).Add(bigFromDecimal(o.FillUnits), bigFromDecimal(o.SpreadUnits))
	free := new(big.Int).Sub(usdt18(100), reserved)
	if sum.Reserved != BaseUnitsToDecimal(reserved, 18) || sum.Free != BaseUnitsToDecimal(free, 18) || sum.Collateral != "100" {
		t.Fatalf("summary = %+v", sum)
	}
}

// Settle-from-collateral: a note, a positive amount, sent by the operator with
// a fresh ref, audited.
func TestVaultAdmin_SettleFromCollateral(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	if err := r.db.AutoMigrate(&models.P2PAuditLog{}); err != nil {
		t.Skip("p2p_audit_logs needs Postgres defaults:", err)
	}
	r.vault.mu.Lock()
	r.vault.outstanding = usdt18(10)
	r.vault.mu.Unlock()
	admin := uuid.New()
	if _, err := r.it.SettlePoolFromCollateral(context.Background(), admin, "5", ""); !errors.Is(err, ErrITOperatorNoteRequired) {
		t.Fatalf("no note: %v", err)
	}
	if _, err := r.it.SettlePoolFromCollateral(context.Background(), admin, "0", "x"); !errors.Is(err, ErrLPAmount) {
		t.Fatalf("zero: %v", err)
	}
	if _, err := r.it.SettlePoolFromCollateral(context.Background(), admin, "5", "PM keeps the naira for this week's fills"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if !strings.HasPrefix(r.sender.lastData, "settle:") {
		t.Fatalf("sent %q", r.sender.lastData)
	}
}
